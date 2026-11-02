package localworker

import (
	aa26errors "errors"
	aa26math "math"
	aa26bits "math/bits"
)

// This file is the AA26 offline responder path. It composes the AA23 plan pin, the
// AA24 fence, plan reference and owner-token checks, and the AA25 receive events
// into one transaction-scoped responder over injected time and injected ports.
//
// Status: Hypothesis. It opens no device and owns no transport. It transmits only
// through an explicitly injected AA26WirePort, nothing in a default configuration
// constructs one, and it adds no arming switch. Timing, error rates and platform
// suitability remain Unknown; tick arithmetic is model behavior, not a measurement.
//
// The responder is serialized: one caller drives it, its AA23WorkerView and its
// AA24Worker. It never waits, spins, retries, starts a goroutine or invokes a
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
//     exact AA23 wire key, an open AA24 stream with the same unrevoked plan
//     reference, and every deadline still in the future.
//   - The only failure action AA23 admits is silence, so a malformed request, an
//     unmatched request and an initiator NACK all end in silence.
//   - The responder never calls AA26StartInitiator. Its wire authority excludes local
//     initiator starts from selection to closure; a refused start is returned, never
//     queued, so this is no second scheduler.
//   - A supersession leaves the selected generation fixed: the transaction finishes
//     on the bytes it pinned. Any revocation, including unregistration, closes the
//     transaction before its next symbol and clears that address's binding, so no
//     later destination is admitted until a new plan is adopted.
//   - The observer is a bounded ring. A full ring drops and counts events; it never
//     delays, refuses or reorders a wire decision.

const (
	aa26SymbolAck  byte = 0x00
	aa26SymbolNack byte = 0xFF

	// aa26PrefixCapacity holds the opening SYN, the initiator and the target.
	aa26PrefixCapacity = 3
	// aa26MaxRequestData is the protocol maximum request data length.
	aa26MaxRequestData byte = 16

	aa26ObserverCeiling uint32 = 1 << 16
)

// The AA26 prefix is mandatory for every identifier in this ticket (enforced by
// TestAA26EveryDeclaredIdentifierIsPrefixed), which conflicts with the ErrFoo
// naming form that ST1012 expects.
var (
	AA26ErrInvalidConfig = aa26errors.New("localworker: aa26 invalid responder configuration")         //nolint:staticcheck // ST1012: AA26 prefix is mandatory
	AA26ErrIdentity      = aa26errors.New("localworker: aa26 worker, stream or clock domain mismatch") //nolint:staticcheck // ST1012: AA26 prefix is mandatory
	AA26ErrAuthority     = aa26errors.New("localworker: aa26 plan reference is not the adopted plan")  //nolint:staticcheck // ST1012: AA26 prefix is mandatory
	AA26ErrWireBusy      = aa26errors.New("localworker: aa26 wire authority is held")                  //nolint:staticcheck // ST1012: AA26 prefix is mandatory
	AA26ErrNotHeld       = aa26errors.New("localworker: aa26 no matching initiator hold")              //nolint:staticcheck // ST1012: AA26 prefix is mandatory
	AA26ErrRefused       = aa26errors.New("localworker: aa26 wire port refused")                       //nolint:staticcheck // ST1012: AA26 prefix is mandatory
	AA26ErrClock         = aa26errors.New("localworker: aa26 clock regression")                        //nolint:staticcheck // ST1012: AA26 prefix is mandatory
	AA26ErrAddress       = aa26errors.New("localworker: aa26 address not admitted")                    //nolint:staticcheck // ST1012: AA26 prefix is mandatory
)

// AA26WirePort is the one injected wire authority. Neither method may block.
type AA26WirePort interface {
	// AA26EmitResponderSymbol transmits one responder-direction symbol whose start
	// bit begins at or after aa26NotBefore. It never arbitrates and never sends a
	// START. False means the symbol cannot be placed and nothing was sent.
	AA26EmitResponderSymbol(aa26Symbol byte, aa26NotBefore uint64) bool
	// AA26StartInitiator begins arbitration for one already-admitted local initiator
	// command. The responder path never calls it.
	AA26StartInitiator(aa26Identity AA24CommandIdentity, aa26Initiator byte) bool
}

// AA26Config holds fixture parameters, never qualified timing budgets. Ticks of
// the AA25 counter are read as ticks of AA26ClockDomain.
type AA26Config struct {
	AA26TickHz             uint64
	AA26ClockDomain        uint64
	AA26TurnaroundTicks    uint64
	AA26EchoAllowanceTicks uint64
	AA26IdleCloseTicks     uint64
	AA26ObserverCapacity   uint32
}

// aa26Validate returns the whole-tick character duration.
func (aa26Config AA26Config) aa26Validate() (uint64, bool) {
	if aa26Config.AA26TickHz < AA25NominalSampleRate || aa26Config.AA26ClockDomain == 0 ||
		aa26Config.AA26TurnaroundTicks > aa26Config.AA26TickHz ||
		aa26Config.AA26ObserverCapacity == 0 || aa26Config.AA26ObserverCapacity > aa26ObserverCeiling {
		return 0, false
	}
	aa26Character := aa25RationalDeadline(0, aa25BitsPerCharacter, aa26Config.AA26TickHz, AA25NominalBaud)
	if aa26Config.AA26EchoAllowanceTicks >= aa26Character || aa26Config.AA26IdleCloseTicks < aa26Character {
		return 0, false
	}
	// Both offsets added to an observed tick must be representable on their own.
	_, aa26CarryTurnaround := aa26bits.Add64(aa26Character, aa26Config.AA26TurnaroundTicks, 0)
	_, aa26CarryEcho := aa26bits.Add64(aa26Character, aa26Config.AA26EchoAllowanceTicks, 0)
	if aa26CarryTurnaround|aa26CarryEcho != 0 {
		return 0, false
	}
	return aa26Character, true
}

// AA26CloseReason states why a transaction released its wire authority.
type AA26CloseReason uint8

const (
	AA26CloseNone AA26CloseReason = iota
	// AA26CloseCompleted: the initiator acknowledged the response.
	AA26CloseCompleted
	// AA26CloseInitiatorNACK: the initiator refused the response; silence follows.
	AA26CloseInitiatorNACK
	// AA26CloseMalformed: bad CRC, escape, length or final symbol; silence.
	AA26CloseMalformed
	// AA26CloseNoMatch: a valid request matched no entry; silence.
	AA26CloseNoMatch
	// AA26CloseInterrupted: a SYN ended the transaction before completion.
	AA26CloseInterrupted
	// AA26CloseFraming: the wire model lost a character.
	AA26CloseFraming
	// AA26CloseCollision: an echo differed, came early or never came.
	AA26CloseCollision
	// AA26CloseRevoked: the plan was revoked on either control plane.
	AA26CloseRevoked
	// AA26CloseExpired: a plan, lease or reference deadline passed.
	AA26CloseExpired
	// AA26CloseFenced: the AA24 stream closed output decisions.
	AA26CloseFenced
	// AA26CloseGap: observation lost continuity; admission stays disarmed.
	AA26CloseGap
	// AA26CloseClock: a clock fault; admission stays disarmed.
	AA26CloseClock
	// AA26CloseStream: the worker moved to a new stream generation.
	AA26CloseStream
	// AA26CloseRefused: the wire port could not place a symbol.
	AA26CloseRefused
	// AA26CloseAuthority: another exact-identity check failed.
	AA26CloseAuthority
	// AA26CloseIdle: no symbol arrived within the idle bound.
	AA26CloseIdle

	AA26CloseReasonCount
)

// AA26ObserverKind identifies an observer event.
type AA26ObserverKind uint8

const (
	AA26ObserverRaw AA26ObserverKind = iota + 1
	AA26ObserverLogical
	AA26ObserverClosed
)

// AA26ObserverEvent is one detached record for the subscriber of the selected
// transaction. AA26Sequence numbers every raw symbol the responder observed in
// this stream, selected or not, so the delivered prefix proves its continuity.
type AA26ObserverEvent struct {
	AA26Kind      AA26ObserverKind
	AA26Key       AA23WireKey
	AA26Sequence  uint64
	AA26StartTick uint64
	AA26Tick      uint64
	AA26Phase     AA23Phase
	AA26Raw       byte
	AA26Logical   byte
	AA26Escaped   bool
	AA26Echo      bool
	AA26Reason    AA26CloseReason
	AA26Lost      uint64
}

// AA26Stats is a snapshot of bounded counters.
type AA26Stats struct {
	AA26Selected         uint64
	AA26SelectRefused    uint64
	AA26Prepared         uint64
	AA26Emitted          uint64
	AA26ObserverDropped  uint64
	AA26InitiatorStarted uint64
	AA26InitiatorRefused uint64
	AA26Closed           [AA26CloseReasonCount]uint64
}

type aa26Binding struct {
	aa26Valid    bool
	aa26Identity AA23PlanIdentity
	aa26Ref      AA24PlanRef
}

type aa26PrefixSymbol struct {
	aa26Raw      byte
	aa26Sequence uint64
	aa26Start    uint64
	aa26Tick     uint64
}

type aa26Frame uint8

const (
	// aa26FrameUnsynced waits for a SYN.
	aa26FrameUnsynced aa26Frame = iota
	// aa26FrameSkip ignores a frame that is not this responder's until a SYN.
	aa26FrameSkip
	aa26FrameSource
	aa26FrameTarget
	aa26FramePB
	aa26FrameSB
	aa26FrameNN
	aa26FrameData
	aa26FrameCRC
	aa26FrameTargetACK
	aa26FrameResponse
	aa26FrameInitiatorACK
)

// AA26Responder is the transaction-scoped responder of one worker incarnation.
type AA26Responder struct {
	aa26Config    AA26Config
	aa26CharTicks uint64
	aa26View      *AA23WorkerView
	aa26IPC       *AA24Worker
	aa26Wire      AA26WirePort
	aa26Stats     AA26Stats

	aa26Bindings     [aa23AddressSpace]aa26Binding
	aa26Disarmed     bool
	aa26LastTick     uint64
	aa26LastRaw      uint64
	aa26NextSequence uint64
	aa26Ordinal      AA23TransactionOrdinal

	aa26Frame     aa26Frame
	aa26Escape    bool
	aa26CRC       byte
	aa26Source    byte
	aa26Target    byte
	aa26PB        byte
	aa26SB        byte
	aa26NN        byte
	aa26DataLen   byte
	aa26Data      [aa26MaxRequestData]byte
	aa26Prefix    [aa26PrefixCapacity]aa26PrefixSymbol
	aa26PrefixLen int

	aa26Active        bool
	aa26Pin           AA23PlanPin
	aa26Key           AA23WireKey
	aa26Ref           AA24PlanRef
	aa26Prepared      bool
	aa26Entry         uint32
	aa26ReplyLen      uint32
	aa26Offset        uint32
	aa26AwaitEcho     bool
	aa26EchoSymbol    byte
	aa26EchoNotBefore uint64
	aa26Lost          uint64

	aa26InitiatorHeld         bool
	aa26InitiatorHeldIdentity AA24CommandIdentity

	aa26Ack      AA23Acknowledgement
	aa26AckReady bool

	aa26Ring      []AA26ObserverEvent
	aa26RingHead  uint32
	aa26RingCount uint32
}

// AA26NewResponder binds one worker view and one AA24 worker endpoint of the same
// worker incarnation, stream and clock domain. It starts with no adopted plan.
func AA26NewResponder(aa26Config AA26Config, aa26View *AA23WorkerView, aa26IPC *AA24Worker, aa26Wire AA26WirePort) (*AA26Responder, error) {
	if aa26View == nil || aa26IPC == nil || aa26Wire == nil {
		return nil, AA26ErrInvalidConfig
	}
	aa26Character, aa26OK := aa26Config.aa26Validate()
	if !aa26OK {
		return nil, AA26ErrInvalidConfig
	}
	if !aa26IPCBelongs(aa26IPC, aa26View.aa23Worker, aa26View.aa23Stream, aa26Config.AA26ClockDomain) {
		return nil, AA26ErrIdentity
	}
	return &AA26Responder{
		aa26Config:       aa26Config,
		aa26CharTicks:    aa26Character,
		aa26View:         aa26View,
		aa26IPC:          aa26IPC,
		aa26Wire:         aa26Wire,
		aa26NextSequence: 1,
		aa26Ring:         make([]AA26ObserverEvent, aa26Config.AA26ObserverCapacity),
	}, nil
}

func aa26IPCBelongs(aa26IPC *AA24Worker, aa26Worker AA23WorkerID, aa26Stream AA23StreamGeneration, aa26Domain uint64) bool {
	aa26Expected := aa26IPC.aa24Expected
	return !aa26IPC.aa24Detached && aa26Expected.AA24Binding.AA24Worker == aa26Worker &&
		aa26Expected.AA24Binding.AA24Stream == aa26Stream && aa26Expected.AA24ClockDomain == aa26Domain
}

func (aa26Responder *AA26Responder) aa26Reading(aa26Tick uint64) AA23ClockReading {
	return AA23ClockReading{AA23Domain: aa26Responder.aa26Config.AA26ClockDomain, AA23Tick: AA23Tick(aa26Tick)}
}

// AA26Stats returns a snapshot of the counters.
func (aa26Responder *AA26Responder) AA26Stats() AA26Stats {
	return aa26Responder.aa26Stats
}

// AA26Active returns the exact wire key of the selected transaction, if any.
func (aa26Responder *AA26Responder) AA26Active() (AA23WireKey, bool) {
	return aa26Responder.aa26Key, aa26Responder.aa26Active
}

// AA26Disarmed reports whether a gap or clock fault closed admission until the
// next stream generation.
func (aa26Responder *AA26Responder) AA26Disarmed() bool {
	return aa26Responder.aa26Disarmed
}

// AA26TakeAcknowledgement returns the plan acknowledgement a closure produced, if
// any. Only the latest is kept: a lost one is recovered by delivering the pending
// revocation notice again, which acknowledges once no pin remains.
func (aa26Responder *AA26Responder) AA26TakeAcknowledgement() (AA23Acknowledgement, bool) {
	aa26Ack, aa26Ready := aa26Responder.aa26Ack, aa26Responder.aa26AckReady
	aa26Responder.aa26Ack = AA23Acknowledgement{}
	aa26Responder.aa26AckReady = false
	return aa26Ack, aa26Ready
}

// AA26DrainObserver appends queued observer events in order, up to cap(dst),
// without allocating.
func (aa26Responder *AA26Responder) AA26DrainObserver(aa26Dst []AA26ObserverEvent) []AA26ObserverEvent {
	aa26Capacity := uint32(len(aa26Responder.aa26Ring))
	for aa26Responder.aa26RingCount > 0 && len(aa26Dst) < cap(aa26Dst) {
		aa26Dst = append(aa26Dst, aa26Responder.aa26Ring[aa26Responder.aa26RingHead])
		aa26Responder.aa26Ring[aa26Responder.aa26RingHead] = AA26ObserverEvent{}
		aa26Responder.aa26RingHead = (aa26Responder.aa26RingHead + 1) % aa26Capacity
		aa26Responder.aa26RingCount--
	}
	return aa26Dst
}

// AA26AdoptPlan adopts one AA23 publication together with the AA24 reference that
// names the same plan on the control lane. Both must agree on every field before
// any effect.
func (aa26Responder *AA26Responder) AA26AdoptPlan(aa26Notice AA23PublishNotice, aa26Ref AA24PlanRef, aa26Tick uint64) (AA23Acknowledgement, bool, error) {
	if aa26Tick < aa26Responder.aa26LastTick {
		return AA23Acknowledgement{}, false, AA26ErrClock
	}
	aa26Identity := aa26Notice.AA23Identity
	if !aa26Ref.AA24Key.aa23Matches(aa26Identity.AA23Registration) || aa26Ref.AA24Worker != aa26Identity.AA23Worker ||
		aa26Ref.AA24Stream != aa26Identity.AA23Stream || aa26Ref.AA24Plan != aa26Identity.AA23Plan {
		return AA23Acknowledgement{}, false, AA26ErrAuthority
	}
	aa26Slot, aa26Err := aa26Responder.aa26View.aa23Pool.aa23Resolve(aa26Notice.AA23Slot)
	if aa26Err != nil {
		return AA23Acknowledgement{}, false, aa26Err
	}
	if aa26Slot.aa23Validity.AA23PlanExpiry != aa26Ref.AA24PlanExpiry {
		return AA23Acknowledgement{}, false, AA26ErrAuthority
	}
	aa26Ack, aa26Ready, aa26Err := aa26Responder.aa26View.AA23AdoptPlan(aa26Notice, aa26Responder.aa26Reading(aa26Tick))
	if aa26Err != nil {
		return AA23Acknowledgement{}, false, aa26Err
	}
	aa26Responder.aa26LastTick = aa26Tick
	aa26Responder.aa26Bindings[aa26Identity.AA23Registration.AA23Address] = aa26Binding{
		aa26Valid:    true,
		aa26Identity: aa26Identity,
		aa26Ref:      aa26Ref,
	}
	return aa26Ack, aa26Ready, nil
}

// AA26ObserveRevocation forwards one AA23 revocation. A supersession leaves a
// selected transaction on its fixed generation; any other reason closes it now.
func (aa26Responder *AA26Responder) AA26ObserveRevocation(aa26Notice AA23RevocationNotice) (AA23Acknowledgement, bool, error) {
	aa26Ack, aa26Ready, aa26Err := aa26Responder.aa26View.AA23ObserveRevocation(aa26Notice)
	if aa26Err != nil {
		return AA23Acknowledgement{}, false, aa26Err
	}
	aa26Bound := &aa26Responder.aa26Bindings[aa26Notice.AA23Identity.AA23Registration.AA23Address]
	if aa26Bound.aa26Valid && aa26Bound.aa26Identity.aa23Matches(aa26Notice.AA23Identity) {
		*aa26Bound = aa26Binding{}
	}
	if aa26Responder.aa26Active && aa26Notice.AA23Reason != AA23RetireSuperseded &&
		aa26Responder.aa26Key.AA23Identity.aa23Matches(aa26Notice.AA23Identity) {
		aa26Reason := AA26CloseRevoked
		if aa26Notice.AA23Reason == AA23RetireExpired {
			aa26Reason = AA26CloseExpired
		}
		aa26Responder.aa26Close(aa26Reason, aa26Responder.aa26LastTick)
		aa26Ack, aa26Ready = aa26Responder.AA26TakeAcknowledgement()
	}
	return aa26Ack, aa26Ready, nil
}

// AA26AdvanceStream moves to a new stream generation, as after a reconnect or a
// resynchronized gap, with the successor AA24 endpoint for that stream. The
// selected transaction closes, every binding is dropped and admission re-arms.
func (aa26Responder *AA26Responder) AA26AdvanceStream(aa26Stream AA23StreamGeneration, aa26IPC *AA24Worker) error {
	aa26View := aa26Responder.aa26View
	if aa26IPC == nil || !aa26IPCBelongs(aa26IPC, aa26View.aa23Worker, aa26Stream, aa26Responder.aa26Config.AA26ClockDomain) {
		return AA26ErrIdentity
	}
	if aa26Stream <= aa26View.aa23Stream {
		return ErrAA23StaleGeneration
	}
	aa26Responder.aa26Close(AA26CloseStream, aa26Responder.aa26LastTick)
	if aa26Err := aa26View.AA23AdvanceStream(aa26Stream); aa26Err != nil {
		return aa26Err
	}
	aa26Responder.aa26IPC = aa26IPC
	aa26Responder.aa26Bindings = [aa23AddressSpace]aa26Binding{}
	aa26Responder.aa26Ordinal = 0
	aa26Responder.aa26Disarmed = false
	aa26Responder.aa26InitiatorHeld = false
	aa26Responder.aa26InitiatorHeldIdentity = AA24CommandIdentity{}
	aa26Responder.aa26Frame = aa26FrameUnsynced
	aa26Responder.aa26Escape = false
	aa26Responder.aa26PrefixLen = 0
	return nil
}

// AA26StartInitiator lets one local initiator command take the wire. The command must
// be the one the AA24 worker already admitted, exactly; the responder holds no
// queue and refuses while a transaction holds the wire authority.
func (aa26Responder *AA26Responder) AA26StartInitiator(aa26Identity AA24CommandIdentity, aa26Initiator byte, aa26Tick uint64) error {
	if aa26Tick < aa26Responder.aa26LastTick {
		return AA26ErrClock
	}
	if !aa23IsInitiatorAddress(aa26Initiator) {
		return AA26ErrAddress
	}
	if aa26Responder.aa26Active || aa26Responder.aa26InitiatorHeld {
		aa26Responder.aa26Stats.AA26InitiatorRefused++
		return AA26ErrWireBusy
	}
	if aa26Err := aa26Responder.aa26IPC.AA24AdmitOutput(aa26Identity, aa26Responder.aa26Reading(aa26Tick)); aa26Err != nil {
		aa26Responder.aa26Stats.AA26InitiatorRefused++
		return aa26Err
	}
	aa26Responder.aa26LastTick = aa26Tick
	if !aa26Responder.aa26Wire.AA26StartInitiator(aa26Identity, aa26Initiator) {
		return AA26ErrRefused
	}
	aa26Responder.aa26InitiatorHeld = true
	aa26Responder.aa26InitiatorHeldIdentity = aa26Identity
	aa26Responder.aa26Stats.AA26InitiatorStarted++
	return nil
}

// AA26EndInitiator releases the initiator hold of exactly this command.
func (aa26Responder *AA26Responder) AA26EndInitiator(aa26Identity AA24CommandIdentity) error {
	if !aa26Responder.aa26InitiatorHeld || !aa26Responder.aa26InitiatorHeldIdentity.aa24Matches(aa26Identity) {
		return AA26ErrNotHeld
	}
	aa26Responder.aa26InitiatorHeld = false
	aa26Responder.aa26InitiatorHeldIdentity = AA24CommandIdentity{}
	return nil
}

// AA26Poll is bounded housekeeping at an injected tick: an expired initiator hold is
// dropped, and a selected transaction closes on a missing echo, an idle bus or any
// failed authority check.
func (aa26Responder *AA26Responder) AA26Poll(aa26Tick uint64) {
	if aa26Tick < aa26Responder.aa26LastTick {
		aa26Responder.aa26RaiseGap(AA26CloseClock, aa26Responder.aa26LastTick)
		return
	}
	aa26Responder.aa26LastTick = aa26Tick
	if aa26Responder.aa26InitiatorHeld && !aa26Responder.aa26InitiatorHeldIdentity.aa24Live(AA23Tick(aa26Tick)) {
		aa26Responder.aa26InitiatorHeld = false
		aa26Responder.aa26InitiatorHeldIdentity = AA24CommandIdentity{}
	}
	if !aa26Responder.aa26Active {
		return
	}
	if aa26Responder.aa26AwaitEcho {
		aa26Deadline, aa26OK := aa26Responder.aa26Add(aa26Responder.aa26EchoNotBefore, aa26Responder.aa26CharTicks+aa26Responder.aa26Config.AA26EchoAllowanceTicks)
		if aa26OK && aa26Tick >= aa26Deadline {
			aa26Responder.aa26Close(AA26CloseCollision, aa26Tick)
			return
		}
	} else if aa26Tick-aa26Responder.aa26LastRaw >= aa26Responder.aa26Config.AA26IdleCloseTicks {
		aa26Responder.aa26Close(AA26CloseIdle, aa26Tick)
		return
	}
	if aa26Reason := aa26Responder.aa26Authority(aa26Tick); aa26Reason != AA26CloseNone {
		aa26Responder.aa26Close(aa26Reason, aa26Tick)
	}
}

// AA25TryPublish consumes one AA25 engine event. It never refuses: the wire
// decision is complete before it returns and the observer ring never blocks, so
// the engine never sees a loss caused by a slow subscriber.
func (aa26Responder *AA26Responder) AA25TryPublish(aa26Event AA25Event) bool {
	switch aa26Event.AA25Kind {
	case AA25EventRawSymbol:
		aa26Responder.aa26RawSymbol(aa26Event.AA25Raw, aa26Event.AA25StartTick, aa26Event.AA25Tick)
	case AA25EventGap, AA25EventLoss:
		aa26Responder.aa26RaiseGap(AA26CloseGap, aa26Event.AA25Tick)
	case AA25EventFault:
		switch aa26Event.AA25Fault {
		case AA25FaultTickRegression:
			aa26Responder.aa26RaiseGap(AA26CloseClock, aa26Responder.aa26LastTick)
		case AA25FaultFalseStart, AA25FaultBadStop:
			aa26Responder.aa26Close(AA26CloseFraming, aa26Responder.aa26LastTick)
			aa26Responder.aa26Frame = aa26FrameUnsynced
		}
	}
	return true
}

// aa26RaiseGap closes the transaction and disarms admission until the next stream.
func (aa26Responder *AA26Responder) aa26RaiseGap(aa26Reason AA26CloseReason, aa26Tick uint64) {
	if aa26Tick < aa26Responder.aa26LastTick {
		aa26Tick = aa26Responder.aa26LastTick
	}
	aa26Responder.aa26Close(aa26Reason, aa26Tick)
	aa26Responder.aa26Disarmed = true
	aa26Responder.aa26Frame = aa26FrameUnsynced
}

func (aa26Responder *AA26Responder) aa26RawSymbol(aa26Raw byte, aa26Start uint64, aa26Tick uint64) {
	if aa26Tick < aa26Responder.aa26LastTick || aa26Start > aa26Tick {
		aa26Responder.aa26RaiseGap(AA26CloseClock, aa26Responder.aa26LastTick)
		return
	}
	aa26Responder.aa26LastTick = aa26Tick
	aa26Responder.aa26LastRaw = aa26Tick
	if aa26Responder.aa26NextSequence == aa26math.MaxUint64 {
		aa26Responder.aa26RaiseGap(AA26CloseGap, aa26Tick)
		return
	}
	aa26Sequence := aa26Responder.aa26NextSequence
	aa26Responder.aa26NextSequence++
	if aa26Responder.aa26Active && aa26Responder.aa26AwaitEcho {
		aa26Responder.aa26Echo(aa26Raw, aa26Sequence, aa26Start, aa26Tick)
		return
	}
	if aa26Raw == aa23SymbolSyn {
		if aa26Responder.aa26Active {
			aa26Responder.aa26DeliverRaw(aa26Raw, aa26Sequence, aa26Start, aa26Tick, false)
			aa26Responder.aa26Close(AA26CloseInterrupted, aa26Tick)
		}
		aa26Responder.aa26StartFrame(aa26Sequence, aa26Start, aa26Tick)
		return
	}
	switch aa26Responder.aa26Frame {
	case aa26FrameSource:
		aa26Responder.aa26ObserveSource(aa26Raw, aa26Sequence, aa26Start, aa26Tick)
	case aa26FrameTarget:
		aa26Responder.aa26ObserveTarget(aa26Raw, aa26Sequence, aa26Start, aa26Tick)
	case aa26FramePB, aa26FrameSB, aa26FrameNN, aa26FrameData, aa26FrameCRC:
		aa26Responder.aa26ObserveRequest(aa26Raw, aa26Sequence, aa26Start, aa26Tick)
	case aa26FrameInitiatorACK:
		aa26Responder.aa26DeliverRaw(aa26Raw, aa26Sequence, aa26Start, aa26Tick, false)
		switch aa26Raw {
		case aa26SymbolAck:
			aa26Responder.aa26Close(AA26CloseCompleted, aa26Tick)
		case aa26SymbolNack:
			aa26Responder.aa26Close(AA26CloseInitiatorNACK, aa26Tick)
		default:
			aa26Responder.aa26Close(AA26CloseMalformed, aa26Tick)
		}
	case aa26FrameTargetACK, aa26FrameResponse:
		// Only the echo of an emitted symbol belongs here, and none is awaited.
		aa26Responder.aa26DeliverRaw(aa26Raw, aa26Sequence, aa26Start, aa26Tick, false)
		aa26Responder.aa26Close(AA26CloseCollision, aa26Tick)
	}
}

func (aa26Responder *AA26Responder) aa26StartFrame(aa26Sequence uint64, aa26Start uint64, aa26Tick uint64) {
	aa26Responder.aa26Frame = aa26FrameSource
	aa26Responder.aa26Escape = false
	aa26Responder.aa26CRC = 0
	aa26Responder.aa26Prefix[0] = aa26PrefixSymbol{aa26Raw: aa23SymbolSyn, aa26Sequence: aa26Sequence, aa26Start: aa26Start, aa26Tick: aa26Tick}
	aa26Responder.aa26PrefixLen = 1
}

func (aa26Responder *AA26Responder) aa26AppendPrefix(aa26Raw byte, aa26Sequence uint64, aa26Start uint64, aa26Tick uint64) {
	aa26Responder.aa26Prefix[aa26Responder.aa26PrefixLen] = aa26PrefixSymbol{aa26Raw: aa26Raw, aa26Sequence: aa26Sequence, aa26Start: aa26Start, aa26Tick: aa26Tick}
	aa26Responder.aa26PrefixLen++
	aa26Responder.aa26CRC = aa23CRCStep(aa26Responder.aa26CRC, aa26Raw)
}

// aa26ObserveSource takes the initiator; an ordinal belongs to every request start.
func (aa26Responder *AA26Responder) aa26ObserveSource(aa26Raw byte, aa26Sequence uint64, aa26Start uint64, aa26Tick uint64) {
	if !aa23IsInitiatorAddress(aa26Raw) {
		aa26Responder.aa26Frame = aa26FrameSkip
		return
	}
	if aa26Responder.aa26Ordinal == aa26math.MaxUint64 {
		aa26Responder.aa26RaiseGap(AA26CloseGap, aa26Tick)
		return
	}
	aa26Responder.aa26Ordinal++
	aa26Responder.aa26Source = aa26Raw
	aa26Responder.aa26AppendPrefix(aa26Raw, aa26Sequence, aa26Start, aa26Tick)
	aa26Responder.aa26Frame = aa26FrameTarget
}

// aa26ObserveTarget selects at destination time and hands over the whole prefix.
func (aa26Responder *AA26Responder) aa26ObserveTarget(aa26Raw byte, aa26Sequence uint64, aa26Start uint64, aa26Tick uint64) {
	aa26Responder.aa26AppendPrefix(aa26Raw, aa26Sequence, aa26Start, aa26Tick)
	aa26Responder.aa26Target = aa26Raw
	aa26Responder.aa26Frame = aa26FrameSkip
	if !aa23IsTargetAddress(aa26Raw) || !aa26Responder.aa26Select(aa26Raw, aa26Tick) {
		return
	}
	aa26Responder.aa26Frame = aa26FramePB
	for aa26Index := 0; aa26Index < aa26Responder.aa26PrefixLen; aa26Index++ {
		aa26Symbol := aa26Responder.aa26Prefix[aa26Index]
		aa26Responder.aa26DeliverRaw(aa26Symbol.aa26Raw, aa26Symbol.aa26Sequence, aa26Symbol.aa26Start, aa26Symbol.aa26Tick, false)
		if aa26Index > 0 {
			aa26Responder.aa26DeliverLogical(aa26Symbol.aa26Raw, false, aa26Symbol.aa26Sequence, aa26Symbol.aa26Start, aa26Symbol.aa26Tick)
		}
	}
}

// aa26Select pins the current plan provisionally. It authorizes no output.
func (aa26Responder *AA26Responder) aa26Select(aa26Address byte, aa26Tick uint64) bool {
	if aa26Responder.aa26Disarmed {
		aa26Responder.aa26Stats.AA26SelectRefused++
		return false
	}
	if aa26Responder.aa26InitiatorHeld {
		if aa26Responder.aa26InitiatorHeldIdentity.aa24Live(AA23Tick(aa26Tick)) {
			aa26Responder.aa26Stats.AA26SelectRefused++
			return false
		}
		aa26Responder.aa26InitiatorHeld = false
		aa26Responder.aa26InitiatorHeldIdentity = AA24CommandIdentity{}
	}
	aa26Bound := aa26Responder.aa26Bindings[aa26Address]
	if !aa26Bound.aa26Valid || aa26Responder.aa26IPCReason(aa26Bound.aa26Ref, aa26Tick) != AA26CloseNone {
		aa26Responder.aa26Stats.AA26SelectRefused++
		return false
	}
	aa26Pin, aa26Err := aa26Responder.aa26View.AA23SelectProvisional(aa26Address, aa26Responder.aa26Ordinal, aa26Responder.aa26Reading(aa26Tick))
	if aa26Err != nil {
		aa26Responder.aa26Stats.AA26SelectRefused++
		return false
	}
	if !aa26Pin.AA23Key().AA23Identity.aa23Matches(aa26Bound.aa26Identity) {
		if aa26Ack, aa26Ready, aa26Err := aa26Pin.AA23Release(); aa26Err == nil && aa26Ready {
			aa26Responder.aa26Ack, aa26Responder.aa26AckReady = aa26Ack, true
		}
		aa26Responder.aa26Stats.AA26SelectRefused++
		return false
	}
	aa26Responder.aa26Active = true
	aa26Responder.aa26Pin = aa26Pin
	aa26Responder.aa26Key = aa26Pin.AA23Key()
	aa26Responder.aa26Ref = aa26Bound.aa26Ref
	aa26Responder.aa26Lost = 0
	aa26Responder.aa26Stats.AA26Selected++
	return true
}

// aa26ObserveRequest delivers every raw symbol at once, including an escape prefix,
// and decodes logical bytes separately.
func (aa26Responder *AA26Responder) aa26ObserveRequest(aa26Raw byte, aa26Sequence uint64, aa26Start uint64, aa26Tick uint64) {
	aa26Responder.aa26DeliverRaw(aa26Raw, aa26Sequence, aa26Start, aa26Tick, false)
	aa26InCRC := aa26Responder.aa26Frame == aa26FrameCRC
	if aa26Responder.aa26Escape {
		aa26Responder.aa26Escape = false
		var aa26Logical byte
		switch aa26Raw {
		case aa23EscapedEscape:
			aa26Logical = aa23SymbolEscape
		case aa23EscapedSyn:
			aa26Logical = aa23SymbolSyn
		default:
			aa26Responder.aa26Close(AA26CloseMalformed, aa26Tick)
			return
		}
		if !aa26InCRC {
			aa26Responder.aa26CRC = aa23CRCStep(aa26Responder.aa26CRC, aa26Raw)
		}
		aa26Responder.aa26ObserveLogical(aa26Logical, true, aa26Sequence, aa26Start, aa26Tick)
		return
	}
	if !aa26InCRC {
		aa26Responder.aa26CRC = aa23CRCStep(aa26Responder.aa26CRC, aa26Raw)
	}
	if aa26Raw == aa23SymbolEscape {
		aa26Responder.aa26Escape = true
		return
	}
	aa26Responder.aa26ObserveLogical(aa26Raw, false, aa26Sequence, aa26Start, aa26Tick)
}

func (aa26Responder *AA26Responder) aa26ObserveLogical(aa26Logical byte, aa26Escaped bool, aa26Sequence uint64, aa26Start uint64, aa26Tick uint64) {
	aa26Responder.aa26DeliverLogical(aa26Logical, aa26Escaped, aa26Sequence, aa26Start, aa26Tick)
	switch aa26Responder.aa26Frame {
	case aa26FramePB:
		aa26Responder.aa26PB = aa26Logical
		aa26Responder.aa26Frame = aa26FrameSB
	case aa26FrameSB:
		aa26Responder.aa26SB = aa26Logical
		aa26Responder.aa26Frame = aa26FrameNN
	case aa26FrameNN:
		if aa26Logical > aa26MaxRequestData {
			aa26Responder.aa26Close(AA26CloseMalformed, aa26Tick)
			return
		}
		aa26Responder.aa26NN = aa26Logical
		aa26Responder.aa26DataLen = 0
		aa26Responder.aa26Frame = aa26FrameData
		if aa26Logical == 0 {
			aa26Responder.aa26Frame = aa26FrameCRC
			aa26Responder.aa26Prepare(aa26Tick)
		}
	case aa26FrameData:
		aa26Responder.aa26Data[aa26Responder.aa26DataLen] = aa26Logical
		aa26Responder.aa26DataLen++
		if aa26Responder.aa26DataLen == aa26Responder.aa26NN {
			aa26Responder.aa26Frame = aa26FrameCRC
			aa26Responder.aa26Prepare(aa26Tick)
		}
	case aa26FrameCRC:
		aa26Responder.aa26Decide(aa26Logical, aa26Start, aa26Tick)
	}
}

// aa26Prepare matches the complete request before its CRC arrives. An unmatched
// request stays selected for observation and ends in silence.
func (aa26Responder *AA26Responder) aa26Prepare(aa26Tick uint64) {
	aa26Now := aa26Responder.aa26Reading(aa26Tick)
	aa26Entry, aa26Err := aa26Responder.aa26Pin.AA23Match(aa26Responder.aa26Key, AA23RequestQuery{
		AA23Source:      aa26Responder.aa26Source,
		AA23Destination: aa26Responder.aa26Target,
		AA23PB:          aa26Responder.aa26PB,
		AA23SB:          aa26Responder.aa26SB,
		AA23Data:        aa26Responder.aa26Data[:aa26Responder.aa26DataLen],
	}, aa26Now)
	if aa26errors.Is(aa26Err, ErrAA23NoMatch) {
		return
	}
	if aa26Err != nil {
		aa26Responder.aa26Close(aa26ReasonFromAA23(aa26Err), aa26Tick)
		return
	}
	aa26Length, aa26Err := aa26Responder.aa26Pin.AA23ResponseLength(aa26Responder.aa26Key, aa26Entry, aa26Now)
	if aa26Err != nil {
		aa26Responder.aa26Close(aa26ReasonFromAA23(aa26Err), aa26Tick)
		return
	}
	aa26Responder.aa26Prepared = true
	aa26Responder.aa26Entry = aa26Entry
	aa26Responder.aa26ReplyLen = aa26Length
	aa26Responder.aa26Stats.AA26Prepared++
}

// aa26Decide runs once the final CRC character passed its stop bit. The ACK starts
// one character after that character's observed start, plus the turnaround.
func (aa26Responder *AA26Responder) aa26Decide(aa26Received byte, aa26Start uint64, aa26Tick uint64) {
	if aa26Received != aa26Responder.aa26CRC {
		aa26Responder.aa26Close(AA26CloseMalformed, aa26Tick)
		return
	}
	if !aa26Responder.aa26Prepared {
		aa26Responder.aa26Close(AA26CloseNoMatch, aa26Tick)
		return
	}
	if aa26Reason := aa26Responder.aa26Authority(aa26Tick); aa26Reason != AA26CloseNone {
		aa26Responder.aa26Close(aa26Reason, aa26Tick)
		return
	}
	aa26NotBefore, aa26OK := aa26Responder.aa26Add(aa26Start, aa26Responder.aa26Config.AA26TurnaroundTicks+aa26Responder.aa26CharTicks)
	if !aa26OK {
		aa26Responder.aa26Close(AA26CloseRefused, aa26Tick)
		return
	}
	aa26Responder.aa26Frame = aa26FrameTargetACK
	aa26Responder.aa26Emit(aa26SymbolAck, aa26NotBefore, aa26Tick)
}

// aa26Echo checks the echo of the last emitted symbol, then places the next one
// where the echoed character ends.
func (aa26Responder *AA26Responder) aa26Echo(aa26Raw byte, aa26Sequence uint64, aa26Start uint64, aa26Tick uint64) {
	if aa26Raw != aa26Responder.aa26EchoSymbol || aa26Start < aa26Responder.aa26EchoNotBefore {
		aa26Responder.aa26DeliverRaw(aa26Raw, aa26Sequence, aa26Start, aa26Tick, false)
		aa26Responder.aa26Close(AA26CloseCollision, aa26Tick)
		if aa26Raw == aa23SymbolSyn {
			aa26Responder.aa26StartFrame(aa26Sequence, aa26Start, aa26Tick)
		}
		return
	}
	aa26Responder.aa26AwaitEcho = false
	aa26Responder.aa26DeliverRaw(aa26Raw, aa26Sequence, aa26Start, aa26Tick, true)
	if aa26Responder.aa26Frame == aa26FrameTargetACK {
		aa26Responder.aa26Frame = aa26FrameResponse
	} else {
		aa26Responder.aa26Offset++
	}
	if aa26Responder.aa26Offset == aa26Responder.aa26ReplyLen {
		aa26Responder.aa26Frame = aa26FrameInitiatorACK
		return
	}
	if aa26Reason := aa26Responder.aa26Authority(aa26Tick); aa26Reason != AA26CloseNone {
		aa26Responder.aa26Close(aa26Reason, aa26Tick)
		return
	}
	aa26Symbol, aa26Err := aa26Responder.aa26Pin.AA23ResponseSymbol(aa26Responder.aa26Key, aa26Responder.aa26Entry,
		aa26Responder.aa26Offset, AA23PhaseResponse, aa26Responder.aa26Reading(aa26Tick))
	if aa26Err != nil {
		aa26Responder.aa26Close(aa26ReasonFromAA23(aa26Err), aa26Tick)
		return
	}
	aa26NotBefore, aa26OK := aa26Responder.aa26Add(aa26Start, aa26Responder.aa26CharTicks)
	if !aa26OK {
		aa26Responder.aa26Close(AA26CloseRefused, aa26Tick)
		return
	}
	aa26Responder.aa26Emit(aa26Symbol, aa26NotBefore, aa26Tick)
}

func (aa26Responder *AA26Responder) aa26Emit(aa26Symbol byte, aa26NotBefore uint64, aa26Tick uint64) {
	if !aa26Responder.aa26Wire.AA26EmitResponderSymbol(aa26Symbol, aa26NotBefore) {
		aa26Responder.aa26Close(AA26CloseRefused, aa26Tick)
		return
	}
	aa26Responder.aa26AwaitEcho = true
	aa26Responder.aa26EchoSymbol = aa26Symbol
	aa26Responder.aa26EchoNotBefore = aa26NotBefore
	aa26Responder.aa26Stats.AA26Emitted++
}

// aa26Authority repeats every output check: armed admission, the open AA24
// stream with the same unrevoked plan reference, then the exact AA23 wire key
// with its revocation and every deadline.
func (aa26Responder *AA26Responder) aa26Authority(aa26Tick uint64) AA26CloseReason {
	if aa26Responder.aa26Disarmed {
		return AA26CloseGap
	}
	if aa26Reason := aa26Responder.aa26IPCReason(aa26Responder.aa26Ref, aa26Tick); aa26Reason != AA26CloseNone {
		return aa26Reason
	}
	if aa26Err := aa26Responder.aa26Pin.AA23Validate(aa26Responder.aa26Key, aa26Responder.aa26Reading(aa26Tick)); aa26Err != nil {
		return aa26ReasonFromAA23(aa26Err)
	}
	return AA26CloseNone
}

// aa26IPCReason services the bounded AA24 control lane first, so a queued
// revocation or stop is seen before the decision. The reference expiry needs no
// check of its own: adoption requires it to equal the AA23 plan expiry, which the
// pin enforces.
func (aa26Responder *AA26Responder) aa26IPCReason(aa26Ref AA24PlanRef, aa26Tick uint64) AA26CloseReason {
	aa26IPC := aa26Responder.aa26IPC
	if aa26IPC.AA24PollControl(aa26Responder.aa26Reading(aa26Tick)) != nil || !aa26IPC.AA24OutputOpen() {
		return AA26CloseFenced
	}
	switch aa26IPC.aa24CheckPlan(aa26Ref) {
	case nil:
		return AA26CloseNone
	case ErrAA24Revoked:
		return AA26CloseRevoked
	default:
		return AA26CloseAuthority
	}
}

func aa26ReasonFromAA23(aa26Err error) AA26CloseReason {
	switch {
	case aa26errors.Is(aa26Err, ErrAA23Revoked):
		return AA26CloseRevoked
	case aa26errors.Is(aa26Err, ErrAA23Expired):
		return AA26CloseExpired
	case aa26errors.Is(aa26Err, ErrAA23ClockClosed):
		return AA26CloseClock
	default:
		return AA26CloseAuthority
	}
}

func (aa26Responder *AA26Responder) aa26Add(aa26Base uint64, aa26Extra uint64) (uint64, bool) {
	aa26Sum, aa26Carry := aa26bits.Add64(aa26Base, aa26Extra, 0)
	return aa26Sum, aa26Carry == 0
}

// aa26Close releases the pin and every per-transaction field, so nothing of the
// closed transaction can authorize a later symbol.
func (aa26Responder *AA26Responder) aa26Close(aa26Reason AA26CloseReason, aa26Tick uint64) {
	aa26Phase := aa26Responder.aa26Phase()
	aa26Responder.aa26Frame = aa26FrameSkip
	aa26Responder.aa26Escape = false
	if aa26Reason == AA26CloseClock {
		aa26Responder.aa26Disarmed = true
	}
	if !aa26Responder.aa26Active {
		return
	}
	if aa26Ack, aa26Ready, aa26Err := aa26Responder.aa26Pin.AA23Release(); aa26Err == nil && aa26Ready {
		aa26Responder.aa26Ack, aa26Responder.aa26AckReady = aa26Ack, true
	}
	aa26Responder.aa26Deliver(AA26ObserverEvent{
		AA26Kind:   AA26ObserverClosed,
		AA26Key:    aa26Responder.aa26Key,
		AA26Tick:   aa26Tick,
		AA26Phase:  aa26Phase,
		AA26Reason: aa26Reason,
		AA26Lost:   aa26Responder.aa26Lost,
	})
	aa26Responder.aa26Stats.AA26Closed[aa26Reason]++
	aa26Responder.aa26Active = false
	aa26Responder.aa26Pin = AA23PlanPin{}
	aa26Responder.aa26Key = AA23WireKey{}
	aa26Responder.aa26Ref = AA24PlanRef{}
	aa26Responder.aa26Prepared = false
	aa26Responder.aa26Entry = 0
	aa26Responder.aa26ReplyLen = 0
	aa26Responder.aa26Offset = 0
	aa26Responder.aa26AwaitEcho = false
	aa26Responder.aa26EchoSymbol = 0
	aa26Responder.aa26EchoNotBefore = 0
	aa26Responder.aa26Lost = 0
}

func (aa26Responder *AA26Responder) aa26Phase() AA23Phase {
	switch aa26Responder.aa26Frame {
	case aa26FrameSource, aa26FrameTarget, aa26FramePB, aa26FrameSB, aa26FrameNN, aa26FrameData, aa26FrameCRC:
		return AA23PhaseRequest
	case aa26FrameTargetACK:
		return AA23PhaseTargetACK
	case aa26FrameResponse:
		return AA23PhaseResponse
	case aa26FrameInitiatorACK:
		return AA23PhaseInitiatorACK
	}
	return AA23PhaseEnded
}

func (aa26Responder *AA26Responder) aa26DeliverRaw(aa26Raw byte, aa26Sequence uint64, aa26Start uint64, aa26Tick uint64, aa26IsEcho bool) {
	aa26Responder.aa26Deliver(AA26ObserverEvent{
		AA26Kind:      AA26ObserverRaw,
		AA26Key:       aa26Responder.aa26Key,
		AA26Sequence:  aa26Sequence,
		AA26StartTick: aa26Start,
		AA26Tick:      aa26Tick,
		AA26Phase:     aa26Responder.aa26Phase(),
		AA26Raw:       aa26Raw,
		AA26Echo:      aa26IsEcho,
	})
}

func (aa26Responder *AA26Responder) aa26DeliverLogical(aa26Logical byte, aa26Escaped bool, aa26Sequence uint64, aa26Start uint64, aa26Tick uint64) {
	aa26Responder.aa26Deliver(AA26ObserverEvent{
		AA26Kind:      AA26ObserverLogical,
		AA26Key:       aa26Responder.aa26Key,
		AA26Sequence:  aa26Sequence,
		AA26StartTick: aa26Start,
		AA26Tick:      aa26Tick,
		AA26Phase:     aa26Responder.aa26Phase(),
		AA26Logical:   aa26Logical,
		AA26Escaped:   aa26Escaped,
	})
}

// aa26Deliver appends to the ring or counts a loss; it never waits.
func (aa26Responder *AA26Responder) aa26Deliver(aa26Event AA26ObserverEvent) {
	aa26Capacity := uint32(len(aa26Responder.aa26Ring))
	if aa26Responder.aa26RingCount == aa26Capacity {
		if aa26Responder.aa26Stats.AA26ObserverDropped != aa26math.MaxUint64 {
			aa26Responder.aa26Stats.AA26ObserverDropped++
		}
		if aa26Responder.aa26Lost != aa26math.MaxUint64 {
			aa26Responder.aa26Lost++
		}
		return
	}
	aa26Responder.aa26Ring[(aa26Responder.aa26RingHead+aa26Responder.aa26RingCount)%aa26Capacity] = aa26Event
	aa26Responder.aa26RingCount++
}
