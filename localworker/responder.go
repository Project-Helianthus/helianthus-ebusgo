package localworker

import (
	"errors"
	"math"
	"math/bits"
)

// This file is the offline responder path. It composes the plan pin, the
// IPC fence, plan reference and owner-token checks, and the wire engine receive events
// into one transaction-scoped responder over injected time and injected ports.
//
// Status: Hypothesis. It opens no device and owns no transport. It transmits only
// through an explicitly injected WirePort, nothing in a default configuration
// constructs one, and it adds no arming switch. Timing, error rates and platform
// suitability remain Unknown; tick arithmetic is model behavior, not a measurement.
//
// The responder is serialized: one caller drives it, its WorkerView and its
// Worker. It never waits, spins, retries, starts a goroutine or invokes a
// callback other than the injected wire port.
//
// Policies fixed by this model:
//   - Selection is provisional at the destination symbol. From then on the
//     subscriber's observer receives the already-observed raw prefix (opening SYN,
//     initiator, target) and every later raw symbol of the transaction in order.
//     An escape prefix is delivered as a raw symbol at once; its logical byte is a
//     separate event once the suffix arrives.
//   - The response is prepared once the request data is complete, before the CRC.
//     The ACK and each response symbol need a valid CRC, the expected phase, the
//     exact plan wire key, an open IPC stream with the same unrevoked plan
//     reference, and every deadline still in the future.
//   - The only failure action a plan admits is silence, so a malformed request, an
//     unmatched request and an initiator NACK all end in silence.
//   - The responder never calls StartInitiator. Its wire authority excludes local
//     initiator starts from selection to closure; a refused start is returned, never
//     queued, so this is no second scheduler.
//   - A supersession leaves the selected generation fixed: the transaction finishes
//     on the bytes it pinned. Any revocation, including unregistration, closes the
//     transaction before its next symbol and clears that address's binding, so no
//     later destination is admitted until a new plan is adopted.
//   - The observer is a bounded ring. A full ring drops and counts events; it never
//     delays, refuses or reorders a wire decision.

const (
	responderSymbolAck  byte = 0x00
	responderSymbolNack byte = 0xFF

	// prefixCapacity holds the opening SYN, the initiator and the target.
	prefixCapacity = 3
	// maxRequestData is the protocol maximum request data length.
	maxRequestData byte = 16

	observerCeiling uint32 = 1 << 16
)

// The responder refuses configuration, identity, authority, wire, clock and
// address faults with these errors.
var (
	ErrResponderInvalidConfig = errors.New("localworker: aa26 invalid responder configuration")
	ErrResponderIdentity      = errors.New("localworker: aa26 worker, stream or clock domain mismatch")
	ErrAuthority              = errors.New("localworker: aa26 plan reference is not the adopted plan")
	ErrWireBusy               = errors.New("localworker: aa26 wire authority is held")
	ErrNotHeld                = errors.New("localworker: aa26 no matching initiator hold")
	ErrRefused                = errors.New("localworker: aa26 wire port refused")
	ErrResponderClock         = errors.New("localworker: aa26 clock regression")
	ErrAddress                = errors.New("localworker: aa26 address not admitted")
)

// WirePort is the one injected wire authority. Neither method may block.
type WirePort interface {
	// EmitResponderSymbol transmits one responder-direction symbol whose start
	// bit begins at or after notBefore. It never arbitrates and never sends a
	// START. False means the symbol cannot be placed and nothing was sent.
	EmitResponderSymbol(symbol byte, notBefore uint64) bool
	// StartInitiator begins arbitration for one already-admitted local initiator
	// command. The responder path never calls it.
	StartInitiator(identity CommandIdentity, initiator byte) bool
}

// ResponderConfig holds fixture parameters, never qualified timing budgets. Ticks of
// the wire engine counter are read as ticks of ClockDomain.
type ResponderConfig struct {
	TickHz             uint64
	ClockDomain        uint64
	TurnaroundTicks    uint64
	EchoAllowanceTicks uint64
	IdleCloseTicks     uint64
	ObserverCapacity   uint32
}

// validate returns the whole-tick character duration.
func (config ResponderConfig) validate() (uint64, bool) {
	if config.TickHz < NominalSampleRate || config.ClockDomain == 0 ||
		config.TurnaroundTicks > config.TickHz ||
		config.ObserverCapacity == 0 || config.ObserverCapacity > observerCeiling {
		return 0, false
	}
	character := rationalDeadline(0, bitsPerCharacter, config.TickHz, NominalBaud)
	if config.EchoAllowanceTicks >= character || config.IdleCloseTicks < character {
		return 0, false
	}
	// Both offsets added to an observed tick must be representable on their own.
	_, carryTurnaround := bits.Add64(character, config.TurnaroundTicks, 0)
	_, carryEcho := bits.Add64(character, config.EchoAllowanceTicks, 0)
	if carryTurnaround|carryEcho != 0 {
		return 0, false
	}
	return character, true
}

// CloseReason states why a transaction released its wire authority.
type CloseReason uint8

const (
	CloseNone CloseReason = iota
	// CloseCompleted: the initiator acknowledged the response.
	CloseCompleted
	// CloseInitiatorNACK: the initiator refused the response; silence follows.
	CloseInitiatorNACK
	// CloseMalformed: bad CRC, escape, length or final symbol; silence.
	CloseMalformed
	// CloseNoMatch: a valid request matched no entry; silence.
	CloseNoMatch
	// CloseInterrupted: a SYN ended the transaction before completion.
	CloseInterrupted
	// CloseFraming: the wire model lost a character.
	CloseFraming
	// CloseCollision: an echo differed, came early or never came.
	CloseCollision
	// CloseRevoked: the plan was revoked on either control plane.
	CloseRevoked
	// CloseExpired: a plan, lease or reference deadline passed.
	CloseExpired
	// CloseFenced: the IPC stream closed output decisions.
	CloseFenced
	// CloseGap: observation lost continuity; admission stays disarmed.
	CloseGap
	// CloseClock: a clock fault; admission stays disarmed.
	CloseClock
	// CloseStream: the worker moved to a new stream generation.
	CloseStream
	// CloseRefused: the wire port could not place a symbol.
	CloseRefused
	// CloseAuthority: another exact-identity check failed.
	CloseAuthority
	// CloseIdle: no symbol arrived within the idle bound.
	CloseIdle

	CloseReasonCount
)

// ObserverKind identifies an observer event.
type ObserverKind uint8

const (
	ObserverRaw ObserverKind = iota + 1
	ObserverLogical
	ObserverClosed
)

// ObserverEvent is one detached record for the subscriber of the selected
// transaction. Sequence numbers every raw symbol the responder observed in
// this stream, selected or not, so the delivered prefix proves its continuity.
type ObserverEvent struct {
	Kind      ObserverKind
	Key       WireKey
	Sequence  uint64
	StartTick uint64
	Tick      uint64
	Phase     Phase
	Raw       byte
	Logical   byte
	Escaped   bool
	Echo      bool
	Reason    CloseReason
	Lost      uint64
}

// ResponderStats is a snapshot of bounded counters.
type ResponderStats struct {
	Selected         uint64
	SelectRefused    uint64
	Prepared         uint64
	Emitted          uint64
	ObserverDropped  uint64
	InitiatorStarted uint64
	InitiatorRefused uint64
	Closed           [CloseReasonCount]uint64
}

type binding struct {
	valid    bool
	identity PlanIdentity
	ref      PlanRef
}

type prefixSymbol struct {
	raw      byte
	sequence uint64
	start    uint64
	tick     uint64
}

type frame uint8

const (
	// responderFrameUnsynced waits for a SYN.
	responderFrameUnsynced frame = iota
	// frameSkip ignores a frame that is not this responder's until a SYN.
	frameSkip
	responderFrameSource
	responderFrameTarget
	responderFramePB
	responderFrameSB
	responderFrameNN
	responderFrameData
	responderFrameCRC
	frameTargetACK
	frameResponse
	frameInitiatorACK
)

// Responder is the transaction-scoped responder of one worker incarnation.
type Responder struct {
	config    ResponderConfig
	charTicks uint64
	view      *WorkerView
	ipc       *Worker
	wire      WirePort
	stats     ResponderStats

	bindings     [addressSpace]binding
	disarmed     bool
	lastTick     uint64
	lastRaw      uint64
	nextSequence uint64
	ordinal      TransactionOrdinal

	frame     frame
	escape    bool
	crc       byte
	source    byte
	target    byte
	pb        byte
	sb        byte
	nn        byte
	dataLen   byte
	data      [maxRequestData]byte
	prefix    [prefixCapacity]prefixSymbol
	prefixLen int

	active        bool
	pin           PlanPin
	key           WireKey
	ref           PlanRef
	prepared      bool
	entry         uint32
	replyLen      uint32
	offset        uint32
	awaitEcho     bool
	echoSymbol    byte
	echoNotBefore uint64
	lost          uint64

	initiatorHeld         bool
	initiatorHeldIdentity CommandIdentity

	ack      Acknowledgement
	ackReady bool

	ring      []ObserverEvent
	ringHead  uint32
	ringCount uint32
}

// NewResponder binds one worker view and one IPC worker endpoint of the same
// worker incarnation, stream and clock domain. It starts with no adopted plan.
func NewResponder(config ResponderConfig, view *WorkerView, ipc *Worker, wire WirePort) (*Responder, error) {
	if view == nil || ipc == nil || wire == nil {
		return nil, ErrResponderInvalidConfig
	}
	character, ok := config.validate()
	if !ok {
		return nil, ErrResponderInvalidConfig
	}
	if !ipcBelongs(ipc, view.worker, view.stream, config.ClockDomain) {
		return nil, ErrResponderIdentity
	}
	return &Responder{
		config:       config,
		charTicks:    character,
		view:         view,
		ipc:          ipc,
		wire:         wire,
		nextSequence: 1,
		ring:         make([]ObserverEvent, config.ObserverCapacity),
	}, nil
}

func ipcBelongs(ipc *Worker, worker WorkerID, stream StreamGeneration, domain uint64) bool {
	expected := ipc.expected
	return !ipc.detached && expected.Binding.Worker == worker &&
		expected.Binding.Stream == stream && expected.ClockDomain == domain
}

func (responder *Responder) reading(tick uint64) ClockReading {
	return ClockReading{Domain: responder.config.ClockDomain, Tick: Tick(tick)}
}

// ResponderStats returns a snapshot of the counters.
func (responder *Responder) Stats() ResponderStats {
	return responder.stats
}

// Active returns the exact wire key of the selected transaction, if any.
func (responder *Responder) Active() (WireKey, bool) {
	return responder.key, responder.active
}

// Disarmed reports whether a gap or clock fault closed admission until the
// next stream generation.
func (responder *Responder) Disarmed() bool {
	return responder.disarmed
}

// TakeAcknowledgement returns the plan acknowledgement a closure produced, if
// any. Only the latest is kept: a lost one is recovered by delivering the pending
// revocation notice again, which acknowledges once no pin remains.
func (responder *Responder) TakeAcknowledgement() (Acknowledgement, bool) {
	ack, ready := responder.ack, responder.ackReady
	responder.ack = Acknowledgement{}
	responder.ackReady = false
	return ack, ready
}

// DrainObserver appends queued observer events in order, up to cap(dst),
// without allocating.
func (responder *Responder) DrainObserver(dst []ObserverEvent) []ObserverEvent {
	capacity := uint32(len(responder.ring))
	for responder.ringCount > 0 && len(dst) < cap(dst) {
		dst = append(dst, responder.ring[responder.ringHead])
		responder.ring[responder.ringHead] = ObserverEvent{}
		responder.ringHead = (responder.ringHead + 1) % capacity
		responder.ringCount--
	}
	return dst
}

// AdoptPlan adopts one plan publication together with the IPC reference that
// names the same plan on the control lane. Both must agree on every field before
// any effect.
func (responder *Responder) AdoptPlan(notice PublishNotice, ref PlanRef, tick uint64) (Acknowledgement, bool, error) {
	if tick < responder.lastTick {
		return Acknowledgement{}, false, ErrResponderClock
	}
	identity := notice.Identity
	if !ref.Key.matches(identity.Registration) || ref.Worker != identity.Worker ||
		ref.Stream != identity.Stream || ref.Plan != identity.Plan {
		return Acknowledgement{}, false, ErrAuthority
	}
	slot, err := responder.view.pool.resolve(notice.Slot)
	if err != nil {
		return Acknowledgement{}, false, err
	}
	if slot.validity.PlanExpiry != ref.PlanExpiry {
		return Acknowledgement{}, false, ErrAuthority
	}
	ack, ready, err := responder.view.AdoptPlan(notice, responder.reading(tick))
	if err != nil {
		return Acknowledgement{}, false, err
	}
	responder.lastTick = tick
	responder.bindings[identity.Registration.Address] = binding{
		valid:    true,
		identity: identity,
		ref:      ref,
	}
	return ack, ready, nil
}

// ObserveRevocation forwards one plan revocation. A supersession leaves a
// selected transaction on its fixed generation; any other reason closes it now.
func (responder *Responder) ObserveRevocation(notice RevocationNotice) (Acknowledgement, bool, error) {
	ack, ready, err := responder.view.ObserveRevocation(notice)
	if err != nil {
		return Acknowledgement{}, false, err
	}
	bound := &responder.bindings[notice.Identity.Registration.Address]
	if bound.valid && bound.identity.matches(notice.Identity) {
		*bound = binding{}
	}
	if responder.active && notice.Reason != RetireSuperseded &&
		responder.key.Identity.matches(notice.Identity) {
		reason := CloseRevoked
		if notice.Reason == RetireExpired {
			reason = CloseExpired
		}
		responder.closeResponder(reason, responder.lastTick)
		ack, ready = responder.TakeAcknowledgement()
	}
	return ack, ready, nil
}

// AdvanceStream moves to a new stream generation, as after a reconnect or a
// resynchronized gap, with the successor IPC endpoint for that stream. The
// selected transaction closes, every binding is dropped and admission re-arms.
func (responder *Responder) AdvanceStream(stream StreamGeneration, ipc *Worker) error {
	view := responder.view
	if ipc == nil || !ipcBelongs(ipc, view.worker, stream, responder.config.ClockDomain) {
		return ErrResponderIdentity
	}
	if stream <= view.stream {
		return ErrStaleGeneration
	}
	responder.closeResponder(CloseStream, responder.lastTick)
	if err := view.AdvanceStream(stream); err != nil {
		return err
	}
	responder.ipc = ipc
	responder.bindings = [addressSpace]binding{}
	responder.ordinal = 0
	responder.disarmed = false
	responder.initiatorHeld = false
	responder.initiatorHeldIdentity = CommandIdentity{}
	responder.frame = responderFrameUnsynced
	responder.escape = false
	responder.prefixLen = 0
	return nil
}

// StartInitiator lets one local initiator command take the wire. The command must
// be the one the IPC worker already admitted, exactly; the responder holds no
// queue and refuses while a transaction holds the wire authority.
func (responder *Responder) StartInitiator(identity CommandIdentity, initiator byte, tick uint64) error {
	if tick < responder.lastTick {
		return ErrResponderClock
	}
	if !isInitiatorAddress(initiator) {
		return ErrAddress
	}
	if responder.active || responder.initiatorHeld {
		responder.stats.InitiatorRefused++
		return ErrWireBusy
	}
	if err := responder.ipc.AdmitOutput(identity, responder.reading(tick)); err != nil {
		responder.stats.InitiatorRefused++
		return err
	}
	responder.lastTick = tick
	if !responder.wire.StartInitiator(identity, initiator) {
		return ErrRefused
	}
	responder.initiatorHeld = true
	responder.initiatorHeldIdentity = identity
	responder.stats.InitiatorStarted++
	return nil
}

// EndInitiator releases the initiator hold of exactly this command.
func (responder *Responder) EndInitiator(identity CommandIdentity) error {
	if !responder.initiatorHeld || !responder.initiatorHeldIdentity.matches(identity) {
		return ErrNotHeld
	}
	responder.initiatorHeld = false
	responder.initiatorHeldIdentity = CommandIdentity{}
	return nil
}

// Poll is bounded housekeeping at an injected tick: an expired initiator hold is
// dropped, and a selected transaction closes on a missing echo, an idle bus or any
// failed authority check.
func (responder *Responder) Poll(tick uint64) {
	if tick < responder.lastTick {
		responder.raiseGap(CloseClock, responder.lastTick)
		return
	}
	responder.lastTick = tick
	if responder.initiatorHeld && !responder.initiatorHeldIdentity.live(Tick(tick)) {
		responder.initiatorHeld = false
		responder.initiatorHeldIdentity = CommandIdentity{}
	}
	if !responder.active {
		return
	}
	if responder.awaitEcho {
		deadline, ok := responder.add(responder.echoNotBefore, responder.charTicks+responder.config.EchoAllowanceTicks)
		if ok && tick >= deadline {
			responder.closeResponder(CloseCollision, tick)
			return
		}
	} else if tick-responder.lastRaw >= responder.config.IdleCloseTicks {
		responder.closeResponder(CloseIdle, tick)
		return
	}
	if reason := responder.authority(tick); reason != CloseNone {
		responder.closeResponder(reason, tick)
	}
}

// TryPublish consumes one wire engine event. It never refuses: the wire
// decision is complete before it returns and the observer ring never blocks, so
// the engine never sees a loss caused by a slow subscriber.
func (responder *Responder) TryPublish(event Event) bool {
	switch event.Kind {
	case EventRawSymbol:
		responder.rawSymbol(event.Raw, event.StartTick, event.Tick)
	case EventGap, EventLoss:
		responder.raiseGap(CloseGap, event.Tick)
	case EventFault:
		switch event.Fault {
		case FaultTickRegression:
			responder.raiseGap(CloseClock, responder.lastTick)
		case FaultFalseStart, FaultBadStop:
			responder.closeResponder(CloseFraming, responder.lastTick)
			responder.frame = responderFrameUnsynced
		}
	}
	return true
}

// raiseGap closes the transaction and disarms admission until the next stream.
func (responder *Responder) raiseGap(reason CloseReason, tick uint64) {
	if tick < responder.lastTick {
		tick = responder.lastTick
	}
	responder.closeResponder(reason, tick)
	responder.disarmed = true
	responder.frame = responderFrameUnsynced
}

func (responder *Responder) rawSymbol(raw byte, start uint64, tick uint64) {
	if tick < responder.lastTick || start > tick {
		responder.raiseGap(CloseClock, responder.lastTick)
		return
	}
	responder.lastTick = tick
	responder.lastRaw = tick
	if responder.nextSequence == math.MaxUint64 {
		responder.raiseGap(CloseGap, tick)
		return
	}
	sequence := responder.nextSequence
	responder.nextSequence++
	if responder.active && responder.awaitEcho {
		responder.echo(raw, sequence, start, tick)
		return
	}
	if raw == planSymbolSyn {
		if responder.active {
			responder.deliverRaw(raw, sequence, start, tick, false)
			responder.closeResponder(CloseInterrupted, tick)
		}
		responder.startFrame(sequence, start, tick)
		return
	}
	switch responder.frame {
	case responderFrameSource:
		responder.observeSource(raw, sequence, start, tick)
	case responderFrameTarget:
		responder.observeTarget(raw, sequence, start, tick)
	case responderFramePB, responderFrameSB, responderFrameNN, responderFrameData, responderFrameCRC:
		responder.observeRequest(raw, sequence, start, tick)
	case frameInitiatorACK:
		responder.deliverRaw(raw, sequence, start, tick, false)
		switch raw {
		case responderSymbolAck:
			responder.closeResponder(CloseCompleted, tick)
		case responderSymbolNack:
			responder.closeResponder(CloseInitiatorNACK, tick)
		default:
			responder.closeResponder(CloseMalformed, tick)
		}
	case frameTargetACK, frameResponse:
		// Only the echo of an emitted symbol belongs here, and none is awaited.
		responder.deliverRaw(raw, sequence, start, tick, false)
		responder.closeResponder(CloseCollision, tick)
	}
}

func (responder *Responder) startFrame(sequence uint64, start uint64, tick uint64) {
	responder.frame = responderFrameSource
	responder.escape = false
	responder.crc = 0
	responder.prefix[0] = prefixSymbol{raw: planSymbolSyn, sequence: sequence, start: start, tick: tick}
	responder.prefixLen = 1
}

func (responder *Responder) appendPrefix(raw byte, sequence uint64, start uint64, tick uint64) {
	responder.prefix[responder.prefixLen] = prefixSymbol{raw: raw, sequence: sequence, start: start, tick: tick}
	responder.prefixLen++
	responder.crc = crcStep(responder.crc, raw)
}

// observeSource takes the initiator; an ordinal belongs to every request start.
func (responder *Responder) observeSource(raw byte, sequence uint64, start uint64, tick uint64) {
	if !isInitiatorAddress(raw) {
		responder.frame = frameSkip
		return
	}
	if responder.ordinal == math.MaxUint64 {
		responder.raiseGap(CloseGap, tick)
		return
	}
	responder.ordinal++
	responder.source = raw
	responder.appendPrefix(raw, sequence, start, tick)
	responder.frame = responderFrameTarget
}

// observeTarget selects at destination time and hands over the whole prefix.
func (responder *Responder) observeTarget(raw byte, sequence uint64, start uint64, tick uint64) {
	responder.appendPrefix(raw, sequence, start, tick)
	responder.target = raw
	responder.frame = frameSkip
	if !isTargetAddress(raw) || !responder.selectPlan(raw, tick) {
		return
	}
	responder.frame = responderFramePB
	for index := 0; index < responder.prefixLen; index++ {
		symbol := responder.prefix[index]
		responder.deliverRaw(symbol.raw, symbol.sequence, symbol.start, symbol.tick, false)
		if index > 0 {
			responder.deliverLogical(symbol.raw, false, symbol.sequence, symbol.start, symbol.tick)
		}
	}
}

// selectPlan pins the current plan provisionally. It authorizes no output.
func (responder *Responder) selectPlan(address byte, tick uint64) bool {
	if responder.disarmed {
		responder.stats.SelectRefused++
		return false
	}
	if responder.initiatorHeld {
		if responder.initiatorHeldIdentity.live(Tick(tick)) {
			responder.stats.SelectRefused++
			return false
		}
		responder.initiatorHeld = false
		responder.initiatorHeldIdentity = CommandIdentity{}
	}
	bound := responder.bindings[address]
	if !bound.valid || responder.ipcReason(bound.ref, tick) != CloseNone {
		responder.stats.SelectRefused++
		return false
	}
	pin, err := responder.view.SelectProvisional(address, responder.ordinal, responder.reading(tick))
	if err != nil {
		responder.stats.SelectRefused++
		return false
	}
	if !pin.Key().Identity.matches(bound.identity) {
		if ack, ready, err := pin.Release(); err == nil && ready {
			responder.ack, responder.ackReady = ack, true
		}
		responder.stats.SelectRefused++
		return false
	}
	responder.active = true
	responder.pin = pin
	responder.key = pin.Key()
	responder.ref = bound.ref
	responder.lost = 0
	responder.stats.Selected++
	return true
}

// observeRequest delivers every raw symbol at once, including an escape prefix,
// and decodes logical bytes separately.
func (responder *Responder) observeRequest(raw byte, sequence uint64, start uint64, tick uint64) {
	responder.deliverRaw(raw, sequence, start, tick, false)
	inCRC := responder.frame == responderFrameCRC
	if responder.escape {
		responder.escape = false
		var logical byte
		switch raw {
		case planEscapedEscape:
			logical = planSymbolEscape
		case planEscapedSyn:
			logical = planSymbolSyn
		default:
			responder.closeResponder(CloseMalformed, tick)
			return
		}
		if !inCRC {
			responder.crc = crcStep(responder.crc, raw)
		}
		responder.observeLogical(logical, true, sequence, start, tick)
		return
	}
	if !inCRC {
		responder.crc = crcStep(responder.crc, raw)
	}
	if raw == planSymbolEscape {
		responder.escape = true
		return
	}
	responder.observeLogical(raw, false, sequence, start, tick)
}

func (responder *Responder) observeLogical(logical byte, escaped bool, sequence uint64, start uint64, tick uint64) {
	responder.deliverLogical(logical, escaped, sequence, start, tick)
	switch responder.frame {
	case responderFramePB:
		responder.pb = logical
		responder.frame = responderFrameSB
	case responderFrameSB:
		responder.sb = logical
		responder.frame = responderFrameNN
	case responderFrameNN:
		if logical > maxRequestData {
			responder.closeResponder(CloseMalformed, tick)
			return
		}
		responder.nn = logical
		responder.dataLen = 0
		responder.frame = responderFrameData
		if logical == 0 {
			responder.frame = responderFrameCRC
			responder.prepare(tick)
		}
	case responderFrameData:
		responder.data[responder.dataLen] = logical
		responder.dataLen++
		if responder.dataLen == responder.nn {
			responder.frame = responderFrameCRC
			responder.prepare(tick)
		}
	case responderFrameCRC:
		responder.decide(logical, start, tick)
	}
}

// prepare matches the complete request before its CRC arrives. An unmatched
// request stays selected for observation and ends in silence.
func (responder *Responder) prepare(tick uint64) {
	now := responder.reading(tick)
	entry, err := responder.pin.Match(responder.key, RequestQuery{
		Source:      responder.source,
		Destination: responder.target,
		PB:          responder.pb,
		SB:          responder.sb,
		Data:        responder.data[:responder.dataLen],
	}, now)
	if errors.Is(err, ErrNoMatch) {
		return
	}
	if err != nil {
		responder.closeResponder(reasonFromPlan(err), tick)
		return
	}
	length, err := responder.pin.ResponseLength(responder.key, entry, now)
	if err != nil {
		responder.closeResponder(reasonFromPlan(err), tick)
		return
	}
	responder.prepared = true
	responder.entry = entry
	responder.replyLen = length
	responder.stats.Prepared++
}

// decide runs once the final CRC character passed its stop bit. The ACK starts
// one character after that character's observed start, plus the turnaround.
func (responder *Responder) decide(received byte, start uint64, tick uint64) {
	if received != responder.crc {
		responder.closeResponder(CloseMalformed, tick)
		return
	}
	if !responder.prepared {
		responder.closeResponder(CloseNoMatch, tick)
		return
	}
	if reason := responder.authority(tick); reason != CloseNone {
		responder.closeResponder(reason, tick)
		return
	}
	notBefore, ok := responder.add(start, responder.config.TurnaroundTicks+responder.charTicks)
	if !ok {
		responder.closeResponder(CloseRefused, tick)
		return
	}
	responder.frame = frameTargetACK
	responder.emit(responderSymbolAck, notBefore, tick)
}

// echo checks the echo of the last emitted symbol, then places the next one
// where the echoed character ends.
func (responder *Responder) echo(raw byte, sequence uint64, start uint64, tick uint64) {
	if raw != responder.echoSymbol || start < responder.echoNotBefore {
		responder.deliverRaw(raw, sequence, start, tick, false)
		responder.closeResponder(CloseCollision, tick)
		if raw == planSymbolSyn {
			responder.startFrame(sequence, start, tick)
		}
		return
	}
	responder.awaitEcho = false
	responder.deliverRaw(raw, sequence, start, tick, true)
	if responder.frame == frameTargetACK {
		responder.frame = frameResponse
	} else {
		responder.offset++
	}
	if responder.offset == responder.replyLen {
		responder.frame = frameInitiatorACK
		return
	}
	if reason := responder.authority(tick); reason != CloseNone {
		responder.closeResponder(reason, tick)
		return
	}
	symbol, err := responder.pin.ResponseSymbol(responder.key, responder.entry,
		responder.offset, PhaseResponse, responder.reading(tick))
	if err != nil {
		responder.closeResponder(reasonFromPlan(err), tick)
		return
	}
	notBefore, ok := responder.add(start, responder.charTicks)
	if !ok {
		responder.closeResponder(CloseRefused, tick)
		return
	}
	responder.emit(symbol, notBefore, tick)
}

func (responder *Responder) emit(symbol byte, notBefore uint64, tick uint64) {
	if !responder.wire.EmitResponderSymbol(symbol, notBefore) {
		responder.closeResponder(CloseRefused, tick)
		return
	}
	responder.awaitEcho = true
	responder.echoSymbol = symbol
	responder.echoNotBefore = notBefore
	responder.stats.Emitted++
}

// authority repeats every output check: armed admission, the open IPC
// stream with the same unrevoked plan reference, then the exact plan wire key
// with its revocation and every deadline.
func (responder *Responder) authority(tick uint64) CloseReason {
	if responder.disarmed {
		return CloseGap
	}
	if reason := responder.ipcReason(responder.ref, tick); reason != CloseNone {
		return reason
	}
	if err := responder.pin.Validate(responder.key, responder.reading(tick)); err != nil {
		return reasonFromPlan(err)
	}
	return CloseNone
}

// ipcReason services the bounded IPC control lane first, so a queued
// revocation or stop is seen before the decision. The reference expiry needs no
// check of its own: adoption requires it to equal the plan expiry, which the
// pin enforces.
func (responder *Responder) ipcReason(ref PlanRef, tick uint64) CloseReason {
	ipc := responder.ipc
	if ipc.PollControl(responder.reading(tick)) != nil || !ipc.OutputOpen() {
		return CloseFenced
	}
	switch ipc.checkPlan(ref) {
	case nil:
		return CloseNone
	case ErrIPCRevoked:
		return CloseRevoked
	default:
		return CloseAuthority
	}
}

func reasonFromPlan(err error) CloseReason {
	switch {
	case errors.Is(err, ErrPlanRevoked):
		return CloseRevoked
	case errors.Is(err, ErrPlanExpired):
		return CloseExpired
	case errors.Is(err, ErrClockClosed):
		return CloseClock
	default:
		return CloseAuthority
	}
}

func (responder *Responder) add(base uint64, extra uint64) (uint64, bool) {
	sum, carry := bits.Add64(base, extra, 0)
	return sum, carry == 0
}

// closeResponder releases the pin and every per-transaction field, so nothing of the
// closed transaction can authorize a later symbol.
func (responder *Responder) closeResponder(reason CloseReason, tick uint64) {
	phase := responder.phase()
	responder.frame = frameSkip
	responder.escape = false
	if reason == CloseClock {
		responder.disarmed = true
	}
	if !responder.active {
		return
	}
	if ack, ready, err := responder.pin.Release(); err == nil && ready {
		responder.ack, responder.ackReady = ack, true
	}
	responder.deliver(ObserverEvent{
		Kind:   ObserverClosed,
		Key:    responder.key,
		Tick:   tick,
		Phase:  phase,
		Reason: reason,
		Lost:   responder.lost,
	})
	responder.stats.Closed[reason]++
	responder.active = false
	responder.pin = PlanPin{}
	responder.key = WireKey{}
	responder.ref = PlanRef{}
	responder.prepared = false
	responder.entry = 0
	responder.replyLen = 0
	responder.offset = 0
	responder.awaitEcho = false
	responder.echoSymbol = 0
	responder.echoNotBefore = 0
	responder.lost = 0
}

func (responder *Responder) phase() Phase {
	switch responder.frame {
	case responderFrameSource, responderFrameTarget, responderFramePB, responderFrameSB, responderFrameNN, responderFrameData, responderFrameCRC:
		return PhaseRequest
	case frameTargetACK:
		return PhaseTargetACK
	case frameResponse:
		return PhaseResponse
	case frameInitiatorACK:
		return PhaseInitiatorACK
	}
	return PhaseEnded
}

func (responder *Responder) deliverRaw(raw byte, sequence uint64, start uint64, tick uint64, isEcho bool) {
	responder.deliver(ObserverEvent{
		Kind:      ObserverRaw,
		Key:       responder.key,
		Sequence:  sequence,
		StartTick: start,
		Tick:      tick,
		Phase:     responder.phase(),
		Raw:       raw,
		Echo:      isEcho,
	})
}

func (responder *Responder) deliverLogical(logical byte, escaped bool, sequence uint64, start uint64, tick uint64) {
	responder.deliver(ObserverEvent{
		Kind:      ObserverLogical,
		Key:       responder.key,
		Sequence:  sequence,
		StartTick: start,
		Tick:      tick,
		Phase:     responder.phase(),
		Logical:   logical,
		Escaped:   escaped,
	})
}

// deliver appends to the ring or counts a loss; it never waits.
func (responder *Responder) deliver(event ObserverEvent) {
	capacity := uint32(len(responder.ring))
	if responder.ringCount == capacity {
		if responder.stats.ObserverDropped != math.MaxUint64 {
			responder.stats.ObserverDropped++
		}
		if responder.lost != math.MaxUint64 {
			responder.lost++
		}
		return
	}
	responder.ring[(responder.ringHead+responder.ringCount)%capacity] = event
	responder.ringCount++
}
