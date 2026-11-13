// Package localworker holds offline models for a local eBUS worker.
//
// The wire engine is a pure bit-level model at a nominal 10x sampling rate. It
// runs only against the injected level, edge, tick and event ports below. It
// opens no device, is not an OS UART or ENS transport, and has no default path
// that can transmit: without an explicitly offered Prepared response it never
// calls the edge port. Its exact arithmetic is model behavior, not a measured
// latency or a hardware result; timing suitability remains Unknown.
package localworker

import (
	"errors"
	"math/bits"
)

const (
	// NominalBaud and NominalSampleRate are nominal model rates.
	NominalBaud       uint64 = 2400
	NominalSampleRate uint64 = 24000

	samplesPerBit       uint64 = 10
	bitsPerCharacter    uint64 = 10
	samplesPerCharacter uint64 = samplesPerBit * bitsPerCharacter
	centralFirst        uint64 = 4
	centralLast         uint64 = 6
	stopCentralLast     uint64 = samplesPerBit*(bitsPerCharacter-1) + centralLast
	finalOffset         uint64 = samplesPerCharacter - 1

	// resyncIdleSamples is one whole character of idle: no stream position
	// inside a character sequence can hold the line high this long.
	resyncIdleSamples uint64 = samplesPerCharacter

	// maxPreparedPayload is a fixed model capacity, not a protocol limit.
	maxPreparedPayload = 16
	maxPreparedWire    = 1 + 2*(1+maxPreparedPayload+1)
	controlCapacity    = 4

	// maxRequestNN is the protocol maximum request data length.
	maxRequestNN byte = 16

	engineSymbolEscape byte = 0xA9
	engineSymbolSyn    byte = 0xAA

	// engineAddressBroadcast has no ACK or target-response phase.
	engineAddressBroadcast byte = 0xFE
	engineEscapedEscape    byte = 0x00
	engineEscapedSyn       byte = 0x01
	engineSymbolAck        byte = 0x00
	engineSymbolNack       byte = 0xFF
	engineCRCPolynomial    byte = 0x9B
)

// Level is a digital bus level. The idle (recessive) level is high.
type Level uint8

const (
	LevelLow Level = iota
	LevelHigh
)

// LevelPort reads the current RX level. It must not block.
type LevelPort interface {
	SampleLevel() Level
}

// EdgePort drives the TX line to a level. It must not block.
type EdgePort interface {
	WriteEdge(level Level)
}

// TickPort reads a monotonic counter. It must not block.
type TickPort interface {
	NowTicks() uint64
}

// EventSink accepts one event without blocking. False means the event was
// lost, which the engine treats as a protocol gap and never retries in place.
type EventSink interface {
	TryPublish(event Event) bool
}

// Ports groups the injected ports. Every port is required.
type Ports struct {
	Levels LevelPort
	Edges  EdgePort
	Ticks  TickPort
	Sink   EventSink
}

// EngineConfig holds model parameters. The turnaround, tolerance and echo
// allowance are injected fixture values, not qualified timing budgets.
type EngineConfig struct {
	TickHz               uint64
	ResponderAddress     byte
	TurnaroundTicks      uint64
	TXLateToleranceTicks uint64
	EchoAllowanceTicks   uint64
}

// EventKind identifies an engine event.
type EventKind uint8

const (
	EventRawSymbol EventKind = iota + 1
	EventLogicalByte
	EventGap
	EventFault
	EventProvisional
	EventFrameValid
	EventAdmitted
	EventTXIntent
	EventTXRelease
	EventTXComplete
	EventLoss
)

// Fault classifies an EventFault.
type Fault uint8

const (
	FaultNone Fault = iota
	FaultFalseStart
	FaultBadStop
	FaultEscapeInvalid
	FaultSynInFrame
	FaultAddressInvalid
	FaultBadCRC
	FaultTickRegression
	FaultStaleGeneration
	FaultRXBeforeResponse
	FaultEchoMismatch
	FaultEchoMissing
	FaultEchoUnsolicited
	FaultTXLate
	FaultLengthInvalid
	FaultNackExhausted
)

// Event is a value record for the injected sink. Tick is the actual
// counter value at the observation or write, never a scheduled time.
// EventRawSymbol and EventLogicalByte carry only RX observations; TX
// intent is EventTXIntent and is never reported as an observed symbol.
type Event struct {
	Kind        EventKind
	Fault       Fault
	Tick        uint64
	SampleIndex uint64
	StartTick   uint64
	Deadline    uint64
	Missed      uint64
	Generation  uint64
	Raw         byte
	Logical     byte
	WasEscaped  bool
	Echo        bool
	Level       Level
	Dissent     uint8
	EdgePhase   [samplesPerBit]uint8
}

// EngineStats is a snapshot of bounded engine counters.
type EngineStats struct {
	Observations       uint64
	MissedSamples      uint64
	MaxObservationLate uint64
	TXEdges            uint64
	EchoedSymbols      uint64
	EventsDropped      uint64
	ControlsApplied    uint64
	Withdrawals        uint64
}

// Prepared is an immutable prepared response for one request shape.
type Prepared struct {
	Generation uint64
	MatchPB    byte
	MatchSB    byte
	MatchNN    byte
	wire       [maxPreparedWire]byte
	wireLen    uint8
}

// The engine refuses a configuration, a missing port and a prepared response
// with these errors.
var (
	ErrEngineInvalidConfig = errors.New("localworker: aa25 invalid config")
	ErrMissingPort         = errors.New("localworker: aa25 missing port")
	ErrInvalidResponse     = errors.New("localworker: aa25 invalid prepared response")
)

// PrepareResponse precomputes the raw wire form of the initial ACK and the
// response. It belongs to the preparation path, never to the timing path.
func PrepareResponse(generation uint64, matchPB, matchSB, matchNN byte, payload []byte) (Prepared, error) {
	if generation == 0 || len(payload) > maxPreparedPayload {
		return Prepared{}, ErrInvalidResponse
	}
	result := Prepared{
		Generation: generation,
		MatchPB:    matchPB,
		MatchSB:    matchSB,
		MatchNN:    matchNN,
	}
	result.appendRaw(engineSymbolAck)
	crc := byte(0)
	crc = result.appendLogical(byte(len(payload)), crc)
	for _, value := range payload {
		crc = result.appendLogical(value, crc)
	}
	result.appendLogical(crc, crc)
	return result, nil
}

func (prepared *Prepared) appendRaw(symbol byte) {
	prepared.wire[prepared.wireLen] = symbol
	prepared.wireLen++
}

// appendLogical writes the escaped form and returns the CRC over that form.
func (prepared *Prepared) appendLogical(value, crc byte) byte {
	switch value {
	case engineSymbolEscape:
		prepared.appendRaw(engineSymbolEscape)
		prepared.appendRaw(engineEscapedEscape)
		return engineCRCUpdate(engineCRCUpdate(crc, engineSymbolEscape), engineEscapedEscape)
	case engineSymbolSyn:
		prepared.appendRaw(engineSymbolEscape)
		prepared.appendRaw(engineEscapedSyn)
		return engineCRCUpdate(engineCRCUpdate(crc, engineSymbolEscape), engineEscapedSyn)
	default:
		prepared.appendRaw(value)
		return engineCRCUpdate(crc, value)
	}
}

// engineCRCUpdate advances CRC8 (polynomial 0x9B, init 0x00) by one wire symbol.
func engineCRCUpdate(crc, symbol byte) byte {
	for bit := 0; bit < 8; bit++ {
		if crc&0x80 != 0 {
			crc = crc<<1 ^ engineCRCPolynomial
		} else {
			crc <<= 1
		}
	}
	return crc ^ symbol
}

// rationalAt returns the exact value of index*num/den as whole
// ticks plus the retained remainder, saturating where the quotient overflows.
func rationalAt(index, num, den uint64) (uint64, uint64) {
	hi, lo := bits.Mul64(index, num)
	if hi >= den {
		return ^uint64(0), 0
	}
	return bits.Div64(hi, lo, den)
}

// rationalDeadline is the first whole tick at or after the exact instant.
func rationalDeadline(anchor, index, num, den uint64) uint64 {
	whole, remainder := rationalAt(index, num, den)
	if remainder != 0 {
		whole++
	}
	return anchor + whole
}

type rxState uint8

const (
	rxResync rxState = iota
	rxHunt
	rxCharacter
	rxStopTail
)

type framePhase uint8

const (
	engineFrameUnsynced framePhase = iota
	engineFrameSource
	engineFrameTarget
	engineFramePB
	engineFrameSB
	engineFrameNN
	engineFrameData
	engineFrameCRC
	frameTrailing
)

type txState uint8

const (
	txIdle txState = iota
	txWaiting
	txActive
	txAwaitAck
)

type controlKind uint8

const (
	controlOffer controlKind = iota + 1
	controlWithdraw
)

type control struct {
	kind     controlKind
	prepared Prepared
}

// Engine is single-threaded. Step does constant bounded work: at most
// one observation, one edge, one housekeeping command and a fixed event count.
type Engine struct {
	config EngineConfig
	ports  Ports
	stats  EngineStats

	// reserveTicks is the construction-time response schedule reserve.
	reserveTicks uint64

	sampleAnchor   uint64
	nextSample     uint64
	nextDeadline   uint64
	lastNow        uint64
	tickRegressing bool

	rxState        rxState
	previousLevel  Level
	idleRun        uint64
	charStartIndex uint64
	charStartTick  uint64
	charEcho       bool
	charLevel      Level
	charHighVotes  uint8
	charDissent    uint8
	charShift      byte
	charEdgePhase  [samplesPerBit]uint8
	finalComplete  bool

	framePhase    framePhase
	escapePending bool
	frameCRC      byte
	frameSource   byte
	framePB       byte
	frameSB       byte
	frameNN       byte
	frameLeft     byte

	slot           Prepared
	slotValid      bool
	pinned         Prepared
	pinnedValid    bool
	lastGeneration uint64

	txState     txState
	txAnchor    uint64
	txBit       uint64
	txBase      uint64
	txRetried   bool
	retryCancel bool
	ackSince    uint64
	txLevel     Level
	echoIndex   uint64
	lossPending bool
	lossApplied bool

	controls     [controlCapacity]control
	controlHead  uint8
	controlCount uint8
}

// NewEngine validates its inputs and anchors the sample schedule at the
// current tick. It starts passive: RX resynchronizing, TX untouched and no
// prepared response.
func NewEngine(config EngineConfig, ports Ports) (*Engine, error) {
	if ports.Levels == nil || ports.Edges == nil || ports.Ticks == nil || ports.Sink == nil {
		return nil, ErrMissingPort
	}
	if config.TickHz < NominalSampleRate ||
		config.ResponderAddress == engineSymbolEscape ||
		config.ResponderAddress == engineSymbolSyn ||
		config.ResponderAddress == engineAddressBroadcast ||
		initiatorAddress(config.ResponderAddress) ||
		config.TurnaroundTicks > config.TickHz {
		return nil, ErrEngineInvalidConfig
	}
	bitWhole, _ := rationalAt(1, config.TickHz, NominalBaud)
	charWhole, _ := rationalAt(bitsPerCharacter, config.TickHz, NominalBaud)
	if config.TXLateToleranceTicks >= bitWhole || config.EchoAllowanceTicks >= charWhole {
		return nil, ErrEngineInvalidConfig
	}
	reserve, representable := scheduleReserve(config)
	now := ports.Ticks.NowTicks()
	if !representable || !scheduleFits(reserve, now) {
		return nil, ErrEngineInvalidConfig
	}
	return &Engine{
		config:        config,
		ports:         ports,
		reserveTicks:  reserve,
		sampleAnchor:  now,
		nextDeadline:  now,
		lastNow:       now,
		previousLevel: LevelHigh,
		charLevel:     LevelHigh,
		txLevel:       LevelHigh,
	}, nil
}

// scheduleReserve sums, once at construction, the ticks past a response
// anchor that the longest response may reach: the response horizon including the
// final receive character, then the turnaround, echo allowance and late
// tolerance. False means that sum does not fit the 64-bit tick counter.
func scheduleReserve(config EngineConfig) (uint64, bool) {
	horizon := rationalDeadline(0, (1+maxPreparedWire)*bitsPerCharacter,
		config.TickHz, NominalBaud)
	sum, carryTurnaround := bits.Add64(horizon, config.TurnaroundTicks, 0)
	sum, carryEcho := bits.Add64(sum, config.EchoAllowanceTicks, 0)
	sum, carryLate := bits.Add64(sum, config.TXLateToleranceTicks, 0)
	return sum, (carryTurnaround | carryEcho | carryLate) == 0
}

// scheduleFits reports whether a response anchored at anchor can reserve
// reserve ticks without wrapping absolute ticks. It is a single comparison.
func scheduleFits(reserve, anchor uint64) bool {
	return anchor <= ^uint64(0)-reserve
}

// EngineStats returns a snapshot of the bounded counters.
func (engine *Engine) Stats() EngineStats {
	return engine.stats
}

// OfferPrepared queues a prepared response for bounded housekeeping. False
// means the fixed queue is full or the value was not produced by
// PrepareResponse; the offer is not retained.
func (engine *Engine) OfferPrepared(prepared Prepared) bool {
	if prepared.Generation == 0 || prepared.wireLen == 0 {
		return false
	}
	return engine.pushControl(control{kind: controlOffer, prepared: prepared})
}

// OfferWithdraw queues withdrawal of admission for bounded housekeeping.
func (engine *Engine) OfferWithdraw() bool {
	return engine.pushControl(control{kind: controlWithdraw})
}

func (engine *Engine) pushControl(item control) bool {
	if engine.controlCount == controlCapacity {
		return false
	}
	slot := (engine.controlHead + engine.controlCount) % controlCapacity
	engine.controls[slot] = item
	engine.controlCount++
	return true
}

// NextDeadlineTick returns the earliest required sample, TX boundary or
// echo deadline for a caller that polls toward it.
func (engine *Engine) NextDeadlineTick() uint64 {
	next := engine.nextDeadline
	if engine.txState == txIdle {
		return next
	}
	if engine.txState == txAwaitAck {
		if candidate := engine.ackDeadline(); candidate < next {
			next = candidate
		}
		return next
	}
	length := uint64(engine.pinned.wireLen)
	if engine.txBit < length*bitsPerCharacter || engine.echoIndex == length {
		if candidate := engine.txDeadline(engine.txBit); candidate < next {
			next = candidate
		}
	}
	if engine.txState == txActive && engine.echoIndex < length {
		if candidate := engine.echoDeadline(); candidate < next {
			next = candidate
		}
	}
	return next
}

// Step performs one pass. Observation precedes emission, so a boundary
// observed at the emission tick still vetoes it; housekeeping runs last.
func (engine *Engine) Step() {
	now := engine.ports.Ticks.NowTicks()
	if now < engine.lastNow {
		if !engine.tickRegressing {
			engine.tickRegressing = true
			engine.publishFault(FaultTickRegression, now)
			engine.invalidateReception(now)
			engine.withdrawAdmission()
		}
		engine.applyLoss(now)
		return
	}
	engine.tickRegressing = false
	engine.lastNow = now
	engine.stepRX(now)
	engine.applyLoss(now)
	engine.stepTX(now)
	engine.applyLoss(now)
	engine.stepHousekeeping(now)
}

func (engine *Engine) stepRX(now uint64) {
	if now < engine.nextDeadline {
		return
	}
	index, _ := rationalAt(now-engine.sampleAnchor, NominalSampleRate, engine.config.TickHz)
	if missed := index - engine.nextSample; missed > 0 {
		// Missed observations cannot be reconstructed: one read happens now and
		// the absence becomes a gap. No tolerance is qualified, so it is zero.
		engine.stats.MissedSamples += missed
		engine.publish(Event{
			Kind:        EventGap,
			Tick:        now,
			SampleIndex: engine.nextSample,
			Missed:      missed,
		})
		engine.invalidateReception(now)
		engine.withdrawAdmission()
	}
	level := engine.ports.Levels.SampleLevel()
	engine.stats.Observations++
	deadline := engine.sampleDeadline(index)
	if late := now - deadline; late > engine.stats.MaxObservationLate {
		engine.stats.MaxObservationLate = late
	}
	engine.nextSample = index + 1
	engine.nextDeadline = engine.sampleDeadline(index + 1)
	engine.observe(level, index, now)
}

func (engine *Engine) sampleDeadline(index uint64) uint64 {
	return rationalDeadline(engine.sampleAnchor, index, engine.config.TickHz, NominalSampleRate)
}

func (engine *Engine) observe(level Level, index, now uint64) {
	switch engine.rxState {
	case rxResync:
		if level == LevelHigh {
			engine.idleRun++
			if engine.idleRun >= resyncIdleSamples {
				engine.rxState = rxHunt
			}
		} else {
			engine.idleRun = 0
		}
	case rxHunt:
		if level == LevelLow && engine.previousLevel == LevelHigh {
			engine.beginCharacter(index, now)
		}
	case rxCharacter:
		engine.observeCharacter(level, index, now)
	case rxStopTail:
		if level == LevelLow {
			engine.beginCharacter(index, now)
		} else if index-engine.charStartIndex >= finalOffset {
			engine.finalComplete = true
			engine.rxState = rxHunt
		}
	}
	engine.previousLevel = level
}

// beginCharacter anchors character phase at the observed first low level.
func (engine *Engine) beginCharacter(index, now uint64) {
	engine.rxState = rxCharacter
	engine.charStartIndex = index
	engine.charStartTick = now
	engine.charLevel = LevelLow
	engine.charHighVotes = 0
	engine.charDissent = 0
	engine.charShift = 0
	engine.charEdgePhase = [samplesPerBit]uint8{}
	engine.finalComplete = false
	// A start seen once every response symbol has echoed is the awaited final
	// symbol, not an extra echo: complete the response before judging it.
	if engine.txState == txActive && engine.txBit == uint64(engine.pinned.wireLen)*bitsPerCharacter &&
		engine.echoIndex == uint64(engine.pinned.wireLen) &&
		now >= engine.txDeadline(engine.txBit) {
		engine.completeTX(now)
	}
	engine.charEcho = engine.txState == txActive
	switch engine.txState {
	case txWaiting:
		engine.publishFault(FaultRXBeforeResponse, now)
		engine.abortTX(now)
		engine.dropFrame()
		engine.withdrawAdmission()
	case txActive:
		if engine.echoIndex >= (engine.txBit+bitsPerCharacter-1)/bitsPerCharacter {
			engine.failResponse(FaultEchoUnsolicited, now)
		}
	}
}

func (engine *Engine) observeCharacter(level Level, index, now uint64) {
	offset := index - engine.charStartIndex
	position := offset % samplesPerBit
	if level != engine.charLevel {
		engine.charLevel = level
		if engine.charEdgePhase[position] != ^uint8(0) {
			engine.charEdgePhase[position]++
		}
	}
	if position < centralFirst || position > centralLast {
		return
	}
	if level == LevelHigh {
		engine.charHighVotes++
	}
	if position != centralLast {
		return
	}
	votes := engine.charHighVotes
	engine.charHighVotes = 0
	high := votes >= 2
	if high {
		engine.charDissent += 3 - votes
	} else {
		engine.charDissent += votes
	}
	bit := offset / samplesPerBit
	switch {
	case bit == 0:
		if high {
			engine.rxState = rxHunt
			engine.failCharacter(FaultFalseStart, now)
		}
	case bit < bitsPerCharacter-1:
		if high {
			engine.charShift |= 1 << (bit - 1)
		}
		if engine.charEcho && engine.txState == txActive {
			intended := engine.pinned.wire[engine.echoIndex]>>(bit-1)&1 == 1
			if intended != high {
				engine.failResponse(FaultEchoMismatch, now)
			}
		}
	default:
		if !high {
			engine.rxState = rxResync
			engine.idleRun = 0
			engine.failCharacter(FaultBadStop, now)
			return
		}
		engine.rxState = rxStopTail
		engine.completeSymbol(index, now)
	}
}

// failCharacter handles a framing fault: the frame is lost, a response in
// progress is abandoned, and a prepared response not in use stays valid.
func (engine *Engine) failCharacter(fault Fault, now uint64) {
	engine.publishFault(fault, now)
	if engine.charEcho && engine.txState == txActive {
		engine.failResponse(FaultEchoMismatch, now)
		return
	}
	engine.abortTX(now)
	engine.dropFrame()
}

func (engine *Engine) completeSymbol(index, now uint64) {
	symbol := engine.charShift
	echo := engine.charEcho
	if echo && engine.txState == txActive {
		engine.echoIndex++
		engine.stats.EchoedSymbols++
	}
	engine.publish(Event{
		Kind:        EventRawSymbol,
		Tick:        now,
		SampleIndex: index,
		StartTick:   engine.charStartTick,
		Raw:         symbol,
		Echo:        echo,
		Dissent:     engine.charDissent,
		EdgePhase:   engine.charEdgePhase,
	})
	if engine.txState == txAwaitAck {
		engine.handleFinalAck(symbol, now)
	}
	engine.feedFrame(symbol, now)
}

// feedFrame updates escape and CRC state one raw symbol at a time. An escape
// prefix advances the CRC at once and yields no logical byte until its suffix.
func (engine *Engine) feedFrame(symbol byte, now uint64) {
	phase := engine.framePhase
	if phase == engineFrameUnsynced {
		if symbol == engineSymbolSyn {
			engine.startFrame()
		}
		return
	}
	if engine.escapePending {
		engine.escapePending = false
		var logical byte
		switch symbol {
		case engineEscapedEscape:
			logical = engineSymbolEscape
		case engineEscapedSyn:
			logical = engineSymbolSyn
		default:
			engine.publishFault(FaultEscapeInvalid, now)
			engine.abortTX(now)
			engine.dropFrame()
			if symbol == engineSymbolSyn {
				engine.startFrame()
			}
			return
		}
		if phase != engineFrameCRC {
			engine.frameCRC = engineCRCUpdate(engine.frameCRC, symbol)
		}
		engine.deliverLogical(logical, true, now)
		return
	}
	switch symbol {
	case engineSymbolSyn:
		if phase != engineFrameSource && phase != frameTrailing {
			engine.publishFault(FaultSynInFrame, now)
		}
		engine.startFrame()
	case engineSymbolEscape:
		engine.escapePending = true
		if phase != engineFrameCRC {
			engine.frameCRC = engineCRCUpdate(engine.frameCRC, symbol)
		}
	default:
		if phase != engineFrameCRC {
			engine.frameCRC = engineCRCUpdate(engine.frameCRC, symbol)
		}
		engine.deliverLogical(symbol, false, now)
	}
}

func (engine *Engine) startFrame() {
	engine.framePhase = engineFrameSource
	engine.escapePending = false
	engine.frameCRC = 0
	engine.pinnedValid = engine.txState != txIdle
}

// dropFrame requires a fresh SYN before any byte can open a frame again.
func (engine *Engine) dropFrame() {
	engine.framePhase = engineFrameUnsynced
	engine.escapePending = false
	engine.pinnedValid = engine.txState != txIdle
}

func (engine *Engine) deliverLogical(logical byte, wasEscaped bool, now uint64) {
	engine.publish(Event{
		Kind:        EventLogicalByte,
		Tick:        now,
		SampleIndex: engine.charStartIndex,
		StartTick:   engine.charStartTick,
		Logical:     logical,
		WasEscaped:  wasEscaped,
		Echo:        engine.charEcho,
	})
	switch engine.framePhase {
	case engineFrameSource:
		if wasEscaped {
			engine.publishFault(FaultAddressInvalid, now)
			engine.dropFrame()
			return
		}
		engine.frameSource = logical
		engine.framePhase = engineFrameTarget
	case engineFrameTarget:
		if wasEscaped {
			engine.publishFault(FaultAddressInvalid, now)
			engine.dropFrame()
			return
		}
		engine.framePhase = engineFramePB
		if logical == engine.config.ResponderAddress &&
			!initiatorAddress(engine.frameSource) {
			// Only an initiator addresses a responder: any other source class never pins.
			engine.publishFault(FaultAddressInvalid, now)
			engine.dropFrame()
			return
		}
		if logical == engine.config.ResponderAddress && engine.slotValid {
			// Destination time reserves the generation and nothing else: no
			// edge can be scheduled before framing and CRC validate.
			engine.pinned = engine.slot
			engine.pinnedValid = true
			engine.publish(Event{
				Kind:       EventProvisional,
				Tick:       now,
				Generation: engine.pinned.Generation,
			})
		}
	case engineFramePB:
		engine.framePB = logical
		engine.framePhase = engineFrameSB
	case engineFrameSB:
		engine.frameSB = logical
		engine.framePhase = engineFrameNN
	case engineFrameNN:
		if logical > maxRequestNN {
			engine.publishFault(FaultLengthInvalid, now)
			engine.dropFrame()
			return
		}
		engine.frameNN = logical
		engine.frameLeft = logical
		engine.framePhase = engineFrameData
		if logical == 0 {
			engine.framePhase = engineFrameCRC
		}
	case engineFrameData:
		engine.frameLeft--
		if engine.frameLeft == 0 {
			engine.framePhase = engineFrameCRC
		}
	case engineFrameCRC:
		engine.framePhase = frameTrailing
		if logical != engine.frameCRC {
			engine.publishFault(FaultBadCRC, now)
			engine.pinnedValid = false
			return
		}
		engine.publish(Event{Kind: EventFrameValid, Tick: now})
		engine.admitResponse(now)
	}
}

// admitResponse schedules a pinned response whose shape matches exactly; a
// pin exists only for this frame and only after the destination matched.
// The schedule is anchored to the observed start of the final character and is
// independent of the RX grid; a stop bit validated at its centre does not move it.
func (engine *Engine) admitResponse(now uint64) {
	if !engine.pinnedValid {
		return
	}
	if engine.framePB != engine.pinned.MatchPB ||
		engine.frameSB != engine.pinned.MatchSB ||
		engine.frameNN != engine.pinned.MatchNN {
		engine.pinnedValid = false
		return
	}
	if !scheduleFits(engine.reserveTicks, engine.charStartTick) {
		engine.pinnedValid = false
		engine.publishFault(FaultTXLate, now)
		return
	}
	engine.txState = txWaiting
	engine.txAnchor = engine.charStartTick
	engine.txBit = 0
	engine.txBase = 0
	engine.txRetried = false
	engine.retryCancel = false
	engine.echoIndex = 0
	engine.publish(Event{
		Kind:       EventAdmitted,
		Tick:       now,
		Deadline:   engine.txDeadline(0),
		Generation: engine.pinned.Generation,
	})
}

// txDeadline is the absolute schedule of TX bit boundary bit, counted
// from the end of the request's final character plus the turnaround.
func (engine *Engine) txDeadline(bit uint64) uint64 {
	return rationalDeadline(engine.txAnchor+engine.config.TurnaroundTicks,
		bitsPerCharacter+bit-engine.txBase, engine.config.TickHz, NominalBaud)
}

func (engine *Engine) echoDeadline() uint64 {
	return engine.txDeadline((engine.echoIndex+1)*bitsPerCharacter) + engine.config.EchoAllowanceTicks
}

func (engine *Engine) txBitLevel(bit uint64) Level {
	position := bit % bitsPerCharacter
	switch position {
	case 0:
		return LevelLow
	case bitsPerCharacter - 1:
		return LevelHigh
	default:
		return Level(engine.pinned.wire[bit/bitsPerCharacter] >> (position - 1) & 1)
	}
}

func (engine *Engine) stepTX(now uint64) {
	switch engine.txState {
	case txIdle:
		return
	case txAwaitAck:
		// The final-ACK wait is bounded; a late symbol is not this exchange's NACK.
		if now >= engine.ackDeadline() {
			engine.abortTX(now)
		}
		return
	case txWaiting:
		if !engine.finalComplete || now < engine.txDeadline(engine.txBase) {
			return
		}
		engine.txState = txActive
	}
	length := uint64(engine.pinned.wireLen)
	total := length * bitsPerCharacter
	if engine.echoIndex < length && now >= engine.echoDeadline() {
		engine.failResponse(FaultEchoMissing, now)
		return
	}
	if engine.txBit < total {
		deadline := engine.txDeadline(engine.txBit)
		if now < deadline {
			return
		}
		level := engine.txBitLevel(engine.txBit)
		if level != engine.txLevel {
			if now > deadline+engine.config.TXLateToleranceTicks {
				engine.failResponse(FaultTXLate, now)
				return
			}
			engine.driveEdge(level, deadline, now)
		}
		engine.txBit++
		return
	}
	if engine.echoIndex == length && now >= engine.txDeadline(total) {
		engine.completeTX(now)
	}
}

// completeTX ends the transmission once every symbol was driven and echoed.
func (engine *Engine) completeTX(now uint64) {
	engine.publish(Event{
		Kind:       EventTXComplete,
		Tick:       now,
		Generation: engine.pinned.Generation,
	})
	// The pin stays until the initiator's final symbol: a NACK there
	// retransmits the response portion once.
	engine.txState = txAwaitAck
	engine.ackSince = now
}

// ackDeadline is the end of the final-ACK wait: 200 ms of injected ticks.
func (engine *Engine) ackDeadline() uint64 {
	allowance := engine.config.TickHz / 5
	if engine.ackSince > ^uint64(0)-allowance {
		return ^uint64(0)
	}
	return engine.ackSince + allowance
}

// handleFinalAck consumes the initiator's symbol after the response. One
// NACK resends the response portion on a fresh absolute schedule anchored to
// that symbol; anything else ends the exchange. Never a retry loop.
func (engine *Engine) handleFinalAck(symbol byte, now uint64) {
	expired := now >= engine.ackDeadline()
	engine.txState = txIdle
	engine.pinnedValid = false
	if symbol != engineSymbolNack || expired {
		return
	}
	if engine.retryCancel {
		return
	}
	if engine.txRetried {
		engine.publishFault(FaultNackExhausted, now)
		return
	}
	if !scheduleFits(engine.reserveTicks, engine.charStartTick) {
		engine.publishFault(FaultTXLate, now)
		return
	}
	engine.txRetried = true
	engine.pinnedValid = true
	engine.txState = txWaiting
	engine.txAnchor = engine.charStartTick
	engine.txBase = bitsPerCharacter
	engine.txBit = bitsPerCharacter
	engine.echoIndex = 1
}

func (engine *Engine) driveEdge(level Level, deadline, now uint64) {
	engine.ports.Edges.WriteEdge(level)
	engine.txLevel = level
	engine.stats.TXEdges++
	engine.publish(Event{
		Kind:       EventTXIntent,
		Tick:       now,
		Deadline:   deadline,
		Level:      level,
		Generation: engine.pinned.Generation,
	})
}

// abortTX ends a response with no catch-up. A driven-low line gets exactly
// one release; software cannot undo a level already stretched while descheduled.
func (engine *Engine) abortTX(now uint64) {
	if engine.txState == txIdle {
		return
	}
	engine.txState = txIdle
	engine.pinnedValid = false
	if engine.txLevel == LevelLow {
		engine.ports.Edges.WriteEdge(LevelHigh)
		engine.txLevel = LevelHigh
		engine.stats.TXEdges++
		engine.publish(Event{Kind: EventTXRelease, Tick: now, Level: LevelHigh})
	}
}

// failResponse handles collisions, missing echo and missed deadlines: the
// response stops, reception resynchronizes and admission is withdrawn.
func (engine *Engine) failResponse(fault Fault, now uint64) {
	engine.publishFault(fault, now)
	engine.invalidateReception(now)
	engine.withdrawAdmission()
}

func (engine *Engine) invalidateReception(now uint64) {
	engine.abortTX(now)
	engine.dropFrame()
	engine.rxState = rxResync
	engine.idleRun = 0
	engine.finalComplete = false
}

// withdrawAdmission clears the prepared response. Only a new offer with a
// newer generation restores admission.
func (engine *Engine) withdrawAdmission() {
	engine.slotValid = false
	engine.pinnedValid = false
	engine.stats.Withdrawals++
}

// stepHousekeeping applies at most one queued command per step.
func (engine *Engine) stepHousekeeping(now uint64) {
	if engine.controlCount == 0 {
		return
	}
	item := &engine.controls[engine.controlHead]
	engine.controlHead = (engine.controlHead + 1) % controlCapacity
	engine.controlCount--
	engine.stats.ControlsApplied++
	switch item.kind {
	case controlOffer:
		// Admission stays disabled while a refused event is unreported; the
		// generation is not consumed, so the same offer may be repeated once the
		// loss marker has been accepted.
		if engine.lossPending {
			return
		}
		if item.prepared.Generation <= engine.lastGeneration {
			engine.publishFault(FaultStaleGeneration, now)
			return
		}
		engine.lastGeneration = item.prepared.Generation
		engine.slot = item.prepared
		engine.slotValid = true
	case controlWithdraw:
		engine.slotValid = false
		engine.stats.Withdrawals++
		// A response already on the wire keeps its pinned generation and finishes;
		// anything not yet on the wire loses admission here.
		if engine.txState == txWaiting || engine.txState == txAwaitAck {
			engine.abortTX(now)
		}
		// Retry cancellation outlives completion of a response still on the wire.
		engine.retryCancel = true
		if engine.txState == txIdle {
			engine.pinnedValid = false
		}
	}
}

func (engine *Engine) publishFault(fault Fault, now uint64) {
	engine.publish(Event{Kind: EventFault, Fault: fault, Tick: now})
}

func (engine *Engine) publish(event Event) {
	if !engine.ports.Sink.TryPublish(event) {
		engine.stats.EventsDropped++
		engine.lossPending = true
	}
}

// applyLoss treats a refused event as a protocol gap. Once per loss it fails
// the affected state; the marker is then offered once per step without draining.
func (engine *Engine) applyLoss(now uint64) {
	if !engine.lossPending {
		return
	}
	if !engine.lossApplied {
		engine.lossApplied = true
		engine.invalidateReception(now)
		engine.withdrawAdmission()
	}
	if engine.ports.Sink.TryPublish(Event{
		Kind:   EventLoss,
		Tick:   now,
		Missed: engine.stats.EventsDropped,
	}) {
		engine.lossPending = false
		engine.lossApplied = false
	}
}

// initiatorAddress reports whether both address nibbles are initiator
// parts (0, 1, 3, 7 or F), the only class that may open a request.
func initiatorAddress(address byte) bool {
	return initiatorNibble(address&0x0F) && initiatorNibble(address>>4)
}

func initiatorNibble(nibble byte) bool {
	return nibble == 0x0 || nibble == 0x1 || nibble == 0x3 || nibble == 0x7 || nibble == 0xF
}
