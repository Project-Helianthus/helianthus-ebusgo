// Package localworker holds offline models for a local eBUS worker.
//
// The AA25 engine is a pure bit-level model at a nominal 10x sampling rate. It
// runs only against the injected level, edge, tick and event ports below. It
// opens no device, is not an OS UART or ENS transport, and has no default path
// that can transmit: without an explicitly offered AA25Prepared response it never
// calls the edge port. Its exact arithmetic is model behavior, not a measured
// latency or a hardware result; timing suitability remains Unknown.
package localworker

import (
	aa25errors "errors"
	aa25bits "math/bits"
)

const (
	// AA25NominalBaud and AA25NominalSampleRate are nominal model rates.
	AA25NominalBaud       uint64 = 2400
	AA25NominalSampleRate uint64 = 24000

	aa25SamplesPerBit       uint64 = 10
	aa25BitsPerCharacter    uint64 = 10
	aa25SamplesPerCharacter uint64 = aa25SamplesPerBit * aa25BitsPerCharacter
	aa25CentralFirst        uint64 = 4
	aa25CentralLast         uint64 = 6
	aa25StopCentralLast     uint64 = aa25SamplesPerBit*(aa25BitsPerCharacter-1) + aa25CentralLast
	aa25FinalOffset         uint64 = aa25SamplesPerCharacter - 1

	// aa25ResyncIdleSamples is one whole character of idle: no stream position
	// inside a character sequence can hold the line high this long.
	aa25ResyncIdleSamples uint64 = aa25SamplesPerCharacter

	// aa25MaxPreparedPayload is a fixed model capacity, not a protocol limit.
	aa25MaxPreparedPayload = 16
	aa25MaxPreparedWire    = 1 + 2*(1+aa25MaxPreparedPayload+1)
	aa25ControlCapacity    = 4

	// aa25MaxRequestNN is the protocol maximum request data length.
	aa25MaxRequestNN byte = 16

	aa25SymbolEscape byte = 0xA9
	aa25SymbolSyn    byte = 0xAA

	// aa25AddressBroadcast has no ACK or target-response phase.
	aa25AddressBroadcast byte = 0xFE
	aa25EscapedEscape    byte = 0x00
	aa25EscapedSyn       byte = 0x01
	aa25SymbolAck        byte = 0x00
	aa25SymbolNack       byte = 0xFF
	aa25CRCPolynomial    byte = 0x9B
)

// AA25Level is a digital bus level. The idle (recessive) level is high.
type AA25Level uint8

const (
	AA25LevelLow AA25Level = iota
	AA25LevelHigh
)

// AA25LevelPort reads the current RX level. It must not block.
type AA25LevelPort interface {
	AA25SampleLevel() AA25Level
}

// AA25EdgePort drives the TX line to a level. It must not block.
type AA25EdgePort interface {
	AA25WriteEdge(aa25Level AA25Level)
}

// AA25TickPort reads a monotonic counter. It must not block.
type AA25TickPort interface {
	AA25NowTicks() uint64
}

// AA25EventSink accepts one event without blocking. False means the event was
// lost, which the engine treats as a protocol gap and never retries in place.
type AA25EventSink interface {
	AA25TryPublish(aa25Event AA25Event) bool
}

// AA25Ports groups the injected ports. Every port is required.
type AA25Ports struct {
	AA25Levels AA25LevelPort
	AA25Edges  AA25EdgePort
	AA25Ticks  AA25TickPort
	AA25Sink   AA25EventSink
}

// AA25Config holds model parameters. The turnaround, tolerance and echo
// allowance are injected fixture values, not qualified timing budgets.
type AA25Config struct {
	AA25TickHz               uint64
	AA25ResponderAddress     byte
	AA25TurnaroundTicks      uint64
	AA25TXLateToleranceTicks uint64
	AA25EchoAllowanceTicks   uint64
}

// AA25EventKind identifies an engine event.
type AA25EventKind uint8

const (
	AA25EventRawSymbol AA25EventKind = iota + 1
	AA25EventLogicalByte
	AA25EventGap
	AA25EventFault
	AA25EventProvisional
	AA25EventFrameValid
	AA25EventAdmitted
	AA25EventTXIntent
	AA25EventTXRelease
	AA25EventTXComplete
	AA25EventLoss
)

// AA25Fault classifies an AA25EventFault.
type AA25Fault uint8

const (
	AA25FaultNone AA25Fault = iota
	AA25FaultFalseStart
	AA25FaultBadStop
	AA25FaultEscapeInvalid
	AA25FaultSynInFrame
	AA25FaultAddressInvalid
	AA25FaultBadCRC
	AA25FaultTickRegression
	AA25FaultStaleGeneration
	AA25FaultRXBeforeResponse
	AA25FaultEchoMismatch
	AA25FaultEchoMissing
	AA25FaultEchoUnsolicited
	AA25FaultTXLate
	AA25FaultLengthInvalid
	AA25FaultNackExhausted
)

// AA25Event is a value record for the injected sink. AA25Tick is the actual
// counter value at the observation or write, never a scheduled time.
// AA25EventRawSymbol and AA25EventLogicalByte carry only RX observations; TX
// intent is AA25EventTXIntent and is never reported as an observed symbol.
type AA25Event struct {
	AA25Kind        AA25EventKind
	AA25Fault       AA25Fault
	AA25Tick        uint64
	AA25SampleIndex uint64
	AA25StartTick   uint64
	AA25Deadline    uint64
	AA25Missed      uint64
	AA25Generation  uint64
	AA25Raw         byte
	AA25Logical     byte
	AA25WasEscaped  bool
	AA25Echo        bool
	AA25Level       AA25Level
	AA25Dissent     uint8
	AA25EdgePhase   [aa25SamplesPerBit]uint8
}

// AA25Stats is a snapshot of bounded engine counters.
type AA25Stats struct {
	AA25Observations       uint64
	AA25MissedSamples      uint64
	AA25MaxObservationLate uint64
	AA25TXEdges            uint64
	AA25EchoedSymbols      uint64
	AA25EventsDropped      uint64
	AA25ControlsApplied    uint64
	AA25Withdrawals        uint64
}

// AA25Prepared is an immutable prepared response for one request shape.
type AA25Prepared struct {
	AA25Generation uint64
	AA25MatchPB    byte
	AA25MatchSB    byte
	AA25MatchNN    byte
	aa25Wire       [aa25MaxPreparedWire]byte
	aa25WireLen    uint8
}

// The AA25 prefix is mandatory for every identifier in this ticket (enforced by
// TestAA25EveryDeclaredIdentifierIsPrefixed), which conflicts with the ErrFoo
// naming form that ST1012 expects.
var (
	AA25ErrInvalidConfig   = aa25errors.New("localworker: aa25 invalid config")            //nolint:staticcheck // ST1012: AA25 prefix is mandatory
	AA25ErrMissingPort     = aa25errors.New("localworker: aa25 missing port")              //nolint:staticcheck // ST1012: AA25 prefix is mandatory
	AA25ErrInvalidResponse = aa25errors.New("localworker: aa25 invalid prepared response") //nolint:staticcheck // ST1012: AA25 prefix is mandatory
)

// AA25PrepareResponse precomputes the raw wire form of the initial ACK and the
// response. It belongs to the preparation path, never to the timing path.
func AA25PrepareResponse(aa25Generation uint64, aa25MatchPB, aa25MatchSB, aa25MatchNN byte, aa25Payload []byte) (AA25Prepared, error) {
	if aa25Generation == 0 || len(aa25Payload) > aa25MaxPreparedPayload {
		return AA25Prepared{}, AA25ErrInvalidResponse
	}
	aa25Result := AA25Prepared{
		AA25Generation: aa25Generation,
		AA25MatchPB:    aa25MatchPB,
		AA25MatchSB:    aa25MatchSB,
		AA25MatchNN:    aa25MatchNN,
	}
	aa25Result.aa25AppendRaw(aa25SymbolAck)
	aa25CRC := byte(0)
	aa25CRC = aa25Result.aa25AppendLogical(byte(len(aa25Payload)), aa25CRC)
	for _, aa25Value := range aa25Payload {
		aa25CRC = aa25Result.aa25AppendLogical(aa25Value, aa25CRC)
	}
	aa25Result.aa25AppendLogical(aa25CRC, aa25CRC)
	return aa25Result, nil
}

func (aa25Prepared *AA25Prepared) aa25AppendRaw(aa25Symbol byte) {
	aa25Prepared.aa25Wire[aa25Prepared.aa25WireLen] = aa25Symbol
	aa25Prepared.aa25WireLen++
}

// aa25AppendLogical writes the escaped form and returns the CRC over that form.
func (aa25Prepared *AA25Prepared) aa25AppendLogical(aa25Value, aa25CRC byte) byte {
	switch aa25Value {
	case aa25SymbolEscape:
		aa25Prepared.aa25AppendRaw(aa25SymbolEscape)
		aa25Prepared.aa25AppendRaw(aa25EscapedEscape)
		return aa25CRCUpdate(aa25CRCUpdate(aa25CRC, aa25SymbolEscape), aa25EscapedEscape)
	case aa25SymbolSyn:
		aa25Prepared.aa25AppendRaw(aa25SymbolEscape)
		aa25Prepared.aa25AppendRaw(aa25EscapedSyn)
		return aa25CRCUpdate(aa25CRCUpdate(aa25CRC, aa25SymbolEscape), aa25EscapedSyn)
	default:
		aa25Prepared.aa25AppendRaw(aa25Value)
		return aa25CRCUpdate(aa25CRC, aa25Value)
	}
}

// aa25CRCUpdate advances CRC8 (polynomial 0x9B, init 0x00) by one wire symbol.
func aa25CRCUpdate(aa25CRC, aa25Symbol byte) byte {
	for aa25Bit := 0; aa25Bit < 8; aa25Bit++ {
		if aa25CRC&0x80 != 0 {
			aa25CRC = aa25CRC<<1 ^ aa25CRCPolynomial
		} else {
			aa25CRC <<= 1
		}
	}
	return aa25CRC ^ aa25Symbol
}

// aa25RationalAt returns the exact value of aa25Index*aa25Num/aa25Den as whole
// ticks plus the retained remainder, saturating where the quotient overflows.
func aa25RationalAt(aa25Index, aa25Num, aa25Den uint64) (uint64, uint64) {
	aa25Hi, aa25Lo := aa25bits.Mul64(aa25Index, aa25Num)
	if aa25Hi >= aa25Den {
		return ^uint64(0), 0
	}
	return aa25bits.Div64(aa25Hi, aa25Lo, aa25Den)
}

// aa25RationalDeadline is the first whole tick at or after the exact instant.
func aa25RationalDeadline(aa25Anchor, aa25Index, aa25Num, aa25Den uint64) uint64 {
	aa25Whole, aa25Remainder := aa25RationalAt(aa25Index, aa25Num, aa25Den)
	if aa25Remainder != 0 {
		aa25Whole++
	}
	return aa25Anchor + aa25Whole
}

type aa25RXState uint8

const (
	aa25RXResync aa25RXState = iota
	aa25RXHunt
	aa25RXCharacter
	aa25RXStopTail
)

type aa25FramePhase uint8

const (
	aa25FrameUnsynced aa25FramePhase = iota
	aa25FrameSource
	aa25FrameTarget
	aa25FramePB
	aa25FrameSB
	aa25FrameNN
	aa25FrameData
	aa25FrameCRC
	aa25FrameTrailing
)

type aa25TXState uint8

const (
	aa25TXIdle aa25TXState = iota
	aa25TXWaiting
	aa25TXActive
	aa25TXAwaitAck
)

type aa25ControlKind uint8

const (
	aa25ControlOffer aa25ControlKind = iota + 1
	aa25ControlWithdraw
)

type aa25Control struct {
	aa25Kind     aa25ControlKind
	aa25Prepared AA25Prepared
}

// AA25Engine is single-threaded. AA25Step does constant bounded work: at most
// one observation, one edge, one housekeeping command and a fixed event count.
type AA25Engine struct {
	aa25Config AA25Config
	aa25Ports  AA25Ports
	aa25Stats  AA25Stats

	aa25SampleAnchor   uint64
	aa25NextSample     uint64
	aa25NextDeadline   uint64
	aa25LastNow        uint64
	aa25TickRegressing bool

	aa25RXState        aa25RXState
	aa25PreviousLevel  AA25Level
	aa25IdleRun        uint64
	aa25CharStartIndex uint64
	aa25CharStartTick  uint64
	aa25CharEcho       bool
	aa25CharLevel      AA25Level
	aa25CharHighVotes  uint8
	aa25CharDissent    uint8
	aa25CharShift      byte
	aa25CharEdgePhase  [aa25SamplesPerBit]uint8
	aa25FinalComplete  bool

	aa25FramePhase    aa25FramePhase
	aa25EscapePending bool
	aa25FrameCRC      byte
	aa25FrameSource   byte
	aa25FramePB       byte
	aa25FrameSB       byte
	aa25FrameNN       byte
	aa25FrameLeft     byte

	aa25Slot           AA25Prepared
	aa25SlotValid      bool
	aa25Pinned         AA25Prepared
	aa25PinnedValid    bool
	aa25LastGeneration uint64

	aa25TXState     aa25TXState
	aa25TXAnchor    uint64
	aa25TXBit       uint64
	aa25TXBase      uint64
	aa25TXRetried   bool
	aa25RetryCancel bool
	aa25AckSince    uint64
	aa25TXLevel     AA25Level
	aa25EchoIndex   uint64
	aa25LossPending bool
	aa25LossApplied bool

	aa25Controls     [aa25ControlCapacity]aa25Control
	aa25ControlHead  uint8
	aa25ControlCount uint8
}

// AA25NewEngine validates its inputs and anchors the sample schedule at the
// current tick. It starts passive: RX resynchronizing, TX untouched and no
// prepared response.
func AA25NewEngine(aa25Config AA25Config, aa25Ports AA25Ports) (*AA25Engine, error) {
	if aa25Ports.AA25Levels == nil || aa25Ports.AA25Edges == nil || aa25Ports.AA25Ticks == nil || aa25Ports.AA25Sink == nil {
		return nil, AA25ErrMissingPort
	}
	if aa25Config.AA25TickHz < AA25NominalSampleRate ||
		aa25Config.AA25ResponderAddress == aa25SymbolEscape ||
		aa25Config.AA25ResponderAddress == aa25SymbolSyn ||
		aa25Config.AA25ResponderAddress == aa25AddressBroadcast ||
		aa25InitiatorAddress(aa25Config.AA25ResponderAddress) {
		return nil, AA25ErrInvalidConfig
	}
	aa25BitWhole, _ := aa25RationalAt(1, aa25Config.AA25TickHz, AA25NominalBaud)
	aa25CharWhole, _ := aa25RationalAt(aa25BitsPerCharacter, aa25Config.AA25TickHz, AA25NominalBaud)
	if aa25Config.AA25TXLateToleranceTicks >= aa25BitWhole || aa25Config.AA25EchoAllowanceTicks >= aa25CharWhole {
		return nil, AA25ErrInvalidConfig
	}
	aa25Now := aa25Ports.AA25Ticks.AA25NowTicks()
	return &AA25Engine{
		aa25Config:        aa25Config,
		aa25Ports:         aa25Ports,
		aa25SampleAnchor:  aa25Now,
		aa25NextDeadline:  aa25Now,
		aa25LastNow:       aa25Now,
		aa25PreviousLevel: AA25LevelHigh,
		aa25CharLevel:     AA25LevelHigh,
		aa25TXLevel:       AA25LevelHigh,
	}, nil
}

// AA25Stats returns a snapshot of the bounded counters.
func (aa25Engine *AA25Engine) AA25Stats() AA25Stats {
	return aa25Engine.aa25Stats
}

// AA25OfferPrepared queues a prepared response for bounded housekeeping. False
// means the fixed queue is full; the offer is not retained.
func (aa25Engine *AA25Engine) AA25OfferPrepared(aa25Prepared AA25Prepared) bool {
	return aa25Engine.aa25PushControl(aa25Control{aa25Kind: aa25ControlOffer, aa25Prepared: aa25Prepared})
}

// AA25OfferWithdraw queues withdrawal of admission for bounded housekeeping.
func (aa25Engine *AA25Engine) AA25OfferWithdraw() bool {
	return aa25Engine.aa25PushControl(aa25Control{aa25Kind: aa25ControlWithdraw})
}

func (aa25Engine *AA25Engine) aa25PushControl(aa25Item aa25Control) bool {
	if aa25Engine.aa25ControlCount == aa25ControlCapacity {
		return false
	}
	aa25Slot := (aa25Engine.aa25ControlHead + aa25Engine.aa25ControlCount) % aa25ControlCapacity
	aa25Engine.aa25Controls[aa25Slot] = aa25Item
	aa25Engine.aa25ControlCount++
	return true
}

// AA25NextDeadlineTick returns the earliest required sample, TX boundary or
// echo deadline for a caller that polls toward it.
func (aa25Engine *AA25Engine) AA25NextDeadlineTick() uint64 {
	aa25Next := aa25Engine.aa25NextDeadline
	if aa25Engine.aa25TXState == aa25TXIdle {
		return aa25Next
	}
	if aa25Engine.aa25TXState == aa25TXAwaitAck {
		if aa25Candidate := aa25Engine.aa25AckDeadline(); aa25Candidate < aa25Next {
			aa25Next = aa25Candidate
		}
		return aa25Next
	}
	aa25Length := uint64(aa25Engine.aa25Pinned.aa25WireLen)
	if aa25Engine.aa25TXBit < aa25Length*aa25BitsPerCharacter || aa25Engine.aa25EchoIndex == aa25Length {
		if aa25Candidate := aa25Engine.aa25TXDeadline(aa25Engine.aa25TXBit); aa25Candidate < aa25Next {
			aa25Next = aa25Candidate
		}
	}
	if aa25Engine.aa25TXState == aa25TXActive && aa25Engine.aa25EchoIndex < aa25Length {
		if aa25Candidate := aa25Engine.aa25EchoDeadline(); aa25Candidate < aa25Next {
			aa25Next = aa25Candidate
		}
	}
	return aa25Next
}

// AA25Step performs one pass. Observation precedes emission, so a boundary
// observed at the emission tick still vetoes it; housekeeping runs last.
func (aa25Engine *AA25Engine) AA25Step() {
	aa25Now := aa25Engine.aa25Ports.AA25Ticks.AA25NowTicks()
	if aa25Now < aa25Engine.aa25LastNow {
		if !aa25Engine.aa25TickRegressing {
			aa25Engine.aa25TickRegressing = true
			aa25Engine.aa25PublishFault(AA25FaultTickRegression, aa25Now)
			aa25Engine.aa25InvalidateReception(aa25Now)
			aa25Engine.aa25WithdrawAdmission()
		}
		aa25Engine.aa25ApplyLoss(aa25Now)
		return
	}
	aa25Engine.aa25TickRegressing = false
	aa25Engine.aa25LastNow = aa25Now
	aa25Engine.aa25StepRX(aa25Now)
	aa25Engine.aa25ApplyLoss(aa25Now)
	aa25Engine.aa25StepTX(aa25Now)
	aa25Engine.aa25ApplyLoss(aa25Now)
	aa25Engine.aa25StepHousekeeping(aa25Now)
}

func (aa25Engine *AA25Engine) aa25StepRX(aa25Now uint64) {
	if aa25Now < aa25Engine.aa25NextDeadline {
		return
	}
	aa25Index, _ := aa25RationalAt(aa25Now-aa25Engine.aa25SampleAnchor, AA25NominalSampleRate, aa25Engine.aa25Config.AA25TickHz)
	if aa25Missed := aa25Index - aa25Engine.aa25NextSample; aa25Missed > 0 {
		// Missed observations cannot be reconstructed: one read happens now and
		// the absence becomes a gap. No tolerance is qualified, so it is zero.
		aa25Engine.aa25Stats.AA25MissedSamples += aa25Missed
		aa25Engine.aa25Publish(AA25Event{
			AA25Kind:        AA25EventGap,
			AA25Tick:        aa25Now,
			AA25SampleIndex: aa25Engine.aa25NextSample,
			AA25Missed:      aa25Missed,
		})
		aa25Engine.aa25InvalidateReception(aa25Now)
		aa25Engine.aa25WithdrawAdmission()
	}
	aa25Level := aa25Engine.aa25Ports.AA25Levels.AA25SampleLevel()
	aa25Engine.aa25Stats.AA25Observations++
	aa25Deadline := aa25Engine.aa25SampleDeadline(aa25Index)
	if aa25Late := aa25Now - aa25Deadline; aa25Late > aa25Engine.aa25Stats.AA25MaxObservationLate {
		aa25Engine.aa25Stats.AA25MaxObservationLate = aa25Late
	}
	aa25Engine.aa25NextSample = aa25Index + 1
	aa25Engine.aa25NextDeadline = aa25Engine.aa25SampleDeadline(aa25Index + 1)
	aa25Engine.aa25Observe(aa25Level, aa25Index, aa25Now)
}

func (aa25Engine *AA25Engine) aa25SampleDeadline(aa25Index uint64) uint64 {
	return aa25RationalDeadline(aa25Engine.aa25SampleAnchor, aa25Index, aa25Engine.aa25Config.AA25TickHz, AA25NominalSampleRate)
}

func (aa25Engine *AA25Engine) aa25Observe(aa25Level AA25Level, aa25Index, aa25Now uint64) {
	switch aa25Engine.aa25RXState {
	case aa25RXResync:
		if aa25Level == AA25LevelHigh {
			aa25Engine.aa25IdleRun++
			if aa25Engine.aa25IdleRun >= aa25ResyncIdleSamples {
				aa25Engine.aa25RXState = aa25RXHunt
			}
		} else {
			aa25Engine.aa25IdleRun = 0
		}
	case aa25RXHunt:
		if aa25Level == AA25LevelLow && aa25Engine.aa25PreviousLevel == AA25LevelHigh {
			aa25Engine.aa25BeginCharacter(aa25Index, aa25Now)
		}
	case aa25RXCharacter:
		aa25Engine.aa25ObserveCharacter(aa25Level, aa25Index, aa25Now)
	case aa25RXStopTail:
		if aa25Level == AA25LevelLow {
			aa25Engine.aa25BeginCharacter(aa25Index, aa25Now)
		} else if aa25Index-aa25Engine.aa25CharStartIndex >= aa25FinalOffset {
			aa25Engine.aa25FinalComplete = true
			aa25Engine.aa25RXState = aa25RXHunt
		}
	}
	aa25Engine.aa25PreviousLevel = aa25Level
}

// aa25BeginCharacter anchors character phase at the observed first low level.
func (aa25Engine *AA25Engine) aa25BeginCharacter(aa25Index, aa25Now uint64) {
	aa25Engine.aa25RXState = aa25RXCharacter
	aa25Engine.aa25CharStartIndex = aa25Index
	aa25Engine.aa25CharStartTick = aa25Now
	aa25Engine.aa25CharLevel = AA25LevelLow
	aa25Engine.aa25CharHighVotes = 0
	aa25Engine.aa25CharDissent = 0
	aa25Engine.aa25CharShift = 0
	aa25Engine.aa25CharEdgePhase = [aa25SamplesPerBit]uint8{}
	aa25Engine.aa25FinalComplete = false
	// A start seen once every response symbol has echoed is the awaited final
	// symbol, not an extra echo: complete the response before judging it.
	if aa25Engine.aa25TXState == aa25TXActive && aa25Engine.aa25TXBit == uint64(aa25Engine.aa25Pinned.aa25WireLen)*aa25BitsPerCharacter &&
		aa25Engine.aa25EchoIndex == uint64(aa25Engine.aa25Pinned.aa25WireLen) &&
		aa25Now >= aa25Engine.aa25TXDeadline(aa25Engine.aa25TXBit) {
		aa25Engine.aa25CompleteTX(aa25Now)
	}
	aa25Engine.aa25CharEcho = aa25Engine.aa25TXState == aa25TXActive
	switch aa25Engine.aa25TXState {
	case aa25TXWaiting:
		aa25Engine.aa25PublishFault(AA25FaultRXBeforeResponse, aa25Now)
		aa25Engine.aa25AbortTX(aa25Now)
		aa25Engine.aa25DropFrame()
		aa25Engine.aa25WithdrawAdmission()
	case aa25TXActive:
		if aa25Engine.aa25EchoIndex >= (aa25Engine.aa25TXBit+aa25BitsPerCharacter-1)/aa25BitsPerCharacter {
			aa25Engine.aa25FailResponse(AA25FaultEchoUnsolicited, aa25Now)
		}
	}
}

func (aa25Engine *AA25Engine) aa25ObserveCharacter(aa25Level AA25Level, aa25Index, aa25Now uint64) {
	aa25Offset := aa25Index - aa25Engine.aa25CharStartIndex
	aa25Position := aa25Offset % aa25SamplesPerBit
	if aa25Level != aa25Engine.aa25CharLevel {
		aa25Engine.aa25CharLevel = aa25Level
		if aa25Engine.aa25CharEdgePhase[aa25Position] != ^uint8(0) {
			aa25Engine.aa25CharEdgePhase[aa25Position]++
		}
	}
	if aa25Position < aa25CentralFirst || aa25Position > aa25CentralLast {
		return
	}
	if aa25Level == AA25LevelHigh {
		aa25Engine.aa25CharHighVotes++
	}
	if aa25Position != aa25CentralLast {
		return
	}
	aa25Votes := aa25Engine.aa25CharHighVotes
	aa25Engine.aa25CharHighVotes = 0
	aa25High := aa25Votes >= 2
	if aa25High {
		aa25Engine.aa25CharDissent += 3 - aa25Votes
	} else {
		aa25Engine.aa25CharDissent += aa25Votes
	}
	aa25Bit := aa25Offset / aa25SamplesPerBit
	switch {
	case aa25Bit == 0:
		if aa25High {
			aa25Engine.aa25RXState = aa25RXHunt
			aa25Engine.aa25FailCharacter(AA25FaultFalseStart, aa25Now)
		}
	case aa25Bit < aa25BitsPerCharacter-1:
		if aa25High {
			aa25Engine.aa25CharShift |= 1 << (aa25Bit - 1)
		}
		if aa25Engine.aa25CharEcho && aa25Engine.aa25TXState == aa25TXActive {
			aa25Intended := aa25Engine.aa25Pinned.aa25Wire[aa25Engine.aa25EchoIndex]>>(aa25Bit-1)&1 == 1
			if aa25Intended != aa25High {
				aa25Engine.aa25FailResponse(AA25FaultEchoMismatch, aa25Now)
			}
		}
	default:
		if !aa25High {
			aa25Engine.aa25RXState = aa25RXResync
			aa25Engine.aa25IdleRun = 0
			aa25Engine.aa25FailCharacter(AA25FaultBadStop, aa25Now)
			return
		}
		aa25Engine.aa25RXState = aa25RXStopTail
		aa25Engine.aa25CompleteSymbol(aa25Index, aa25Now)
	}
}

// aa25FailCharacter handles a framing fault: the frame is lost, a response in
// progress is abandoned, and a prepared response not in use stays valid.
func (aa25Engine *AA25Engine) aa25FailCharacter(aa25Fault AA25Fault, aa25Now uint64) {
	aa25Engine.aa25PublishFault(aa25Fault, aa25Now)
	if aa25Engine.aa25CharEcho && aa25Engine.aa25TXState == aa25TXActive {
		aa25Engine.aa25FailResponse(AA25FaultEchoMismatch, aa25Now)
		return
	}
	aa25Engine.aa25AbortTX(aa25Now)
	aa25Engine.aa25DropFrame()
}

func (aa25Engine *AA25Engine) aa25CompleteSymbol(aa25Index, aa25Now uint64) {
	aa25Symbol := aa25Engine.aa25CharShift
	aa25Echo := aa25Engine.aa25CharEcho
	if aa25Echo && aa25Engine.aa25TXState == aa25TXActive {
		aa25Engine.aa25EchoIndex++
		aa25Engine.aa25Stats.AA25EchoedSymbols++
	}
	aa25Engine.aa25Publish(AA25Event{
		AA25Kind:        AA25EventRawSymbol,
		AA25Tick:        aa25Now,
		AA25SampleIndex: aa25Index,
		AA25StartTick:   aa25Engine.aa25CharStartTick,
		AA25Raw:         aa25Symbol,
		AA25Echo:        aa25Echo,
		AA25Dissent:     aa25Engine.aa25CharDissent,
		AA25EdgePhase:   aa25Engine.aa25CharEdgePhase,
	})
	if aa25Engine.aa25TXState == aa25TXAwaitAck {
		aa25Engine.aa25HandleFinalAck(aa25Symbol, aa25Now)
	}
	aa25Engine.aa25FeedFrame(aa25Symbol, aa25Now)
}

// aa25FeedFrame updates escape and CRC state one raw symbol at a time. An escape
// prefix advances the CRC at once and yields no logical byte until its suffix.
func (aa25Engine *AA25Engine) aa25FeedFrame(aa25Symbol byte, aa25Now uint64) {
	aa25Phase := aa25Engine.aa25FramePhase
	if aa25Phase == aa25FrameUnsynced {
		if aa25Symbol == aa25SymbolSyn {
			aa25Engine.aa25StartFrame()
		}
		return
	}
	if aa25Engine.aa25EscapePending {
		aa25Engine.aa25EscapePending = false
		var aa25Logical byte
		switch aa25Symbol {
		case aa25EscapedEscape:
			aa25Logical = aa25SymbolEscape
		case aa25EscapedSyn:
			aa25Logical = aa25SymbolSyn
		default:
			aa25Engine.aa25PublishFault(AA25FaultEscapeInvalid, aa25Now)
			aa25Engine.aa25AbortTX(aa25Now)
			aa25Engine.aa25DropFrame()
			if aa25Symbol == aa25SymbolSyn {
				aa25Engine.aa25StartFrame()
			}
			return
		}
		if aa25Phase != aa25FrameCRC {
			aa25Engine.aa25FrameCRC = aa25CRCUpdate(aa25Engine.aa25FrameCRC, aa25Symbol)
		}
		aa25Engine.aa25DeliverLogical(aa25Logical, true, aa25Now)
		return
	}
	switch aa25Symbol {
	case aa25SymbolSyn:
		if aa25Phase != aa25FrameSource && aa25Phase != aa25FrameTrailing {
			aa25Engine.aa25PublishFault(AA25FaultSynInFrame, aa25Now)
		}
		aa25Engine.aa25StartFrame()
	case aa25SymbolEscape:
		aa25Engine.aa25EscapePending = true
		if aa25Phase != aa25FrameCRC {
			aa25Engine.aa25FrameCRC = aa25CRCUpdate(aa25Engine.aa25FrameCRC, aa25Symbol)
		}
	default:
		if aa25Phase != aa25FrameCRC {
			aa25Engine.aa25FrameCRC = aa25CRCUpdate(aa25Engine.aa25FrameCRC, aa25Symbol)
		}
		aa25Engine.aa25DeliverLogical(aa25Symbol, false, aa25Now)
	}
}

func (aa25Engine *AA25Engine) aa25StartFrame() {
	aa25Engine.aa25FramePhase = aa25FrameSource
	aa25Engine.aa25EscapePending = false
	aa25Engine.aa25FrameCRC = 0
	aa25Engine.aa25PinnedValid = aa25Engine.aa25TXState != aa25TXIdle
}

// aa25DropFrame requires a fresh SYN before any byte can open a frame again.
func (aa25Engine *AA25Engine) aa25DropFrame() {
	aa25Engine.aa25FramePhase = aa25FrameUnsynced
	aa25Engine.aa25EscapePending = false
	aa25Engine.aa25PinnedValid = aa25Engine.aa25TXState != aa25TXIdle
}

func (aa25Engine *AA25Engine) aa25DeliverLogical(aa25Logical byte, aa25WasEscaped bool, aa25Now uint64) {
	aa25Engine.aa25Publish(AA25Event{
		AA25Kind:        AA25EventLogicalByte,
		AA25Tick:        aa25Now,
		AA25SampleIndex: aa25Engine.aa25CharStartIndex,
		AA25StartTick:   aa25Engine.aa25CharStartTick,
		AA25Logical:     aa25Logical,
		AA25WasEscaped:  aa25WasEscaped,
		AA25Echo:        aa25Engine.aa25CharEcho,
	})
	switch aa25Engine.aa25FramePhase {
	case aa25FrameSource:
		if aa25WasEscaped {
			aa25Engine.aa25PublishFault(AA25FaultAddressInvalid, aa25Now)
			aa25Engine.aa25DropFrame()
			return
		}
		aa25Engine.aa25FrameSource = aa25Logical
		aa25Engine.aa25FramePhase = aa25FrameTarget
	case aa25FrameTarget:
		if aa25WasEscaped {
			aa25Engine.aa25PublishFault(AA25FaultAddressInvalid, aa25Now)
			aa25Engine.aa25DropFrame()
			return
		}
		aa25Engine.aa25FramePhase = aa25FramePB
		if aa25Logical == aa25Engine.aa25Config.AA25ResponderAddress &&
			!aa25InitiatorAddress(aa25Engine.aa25FrameSource) {
			// Only an initiator addresses a responder: any other source class never pins.
			aa25Engine.aa25PublishFault(AA25FaultAddressInvalid, aa25Now)
			aa25Engine.aa25DropFrame()
			return
		}
		if aa25Logical == aa25Engine.aa25Config.AA25ResponderAddress && aa25Engine.aa25SlotValid {
			// Destination time reserves the generation and nothing else: no
			// edge can be scheduled before framing and CRC validate.
			aa25Engine.aa25Pinned = aa25Engine.aa25Slot
			aa25Engine.aa25PinnedValid = true
			aa25Engine.aa25Publish(AA25Event{
				AA25Kind:       AA25EventProvisional,
				AA25Tick:       aa25Now,
				AA25Generation: aa25Engine.aa25Pinned.AA25Generation,
			})
		}
	case aa25FramePB:
		aa25Engine.aa25FramePB = aa25Logical
		aa25Engine.aa25FramePhase = aa25FrameSB
	case aa25FrameSB:
		aa25Engine.aa25FrameSB = aa25Logical
		aa25Engine.aa25FramePhase = aa25FrameNN
	case aa25FrameNN:
		if aa25Logical > aa25MaxRequestNN {
			aa25Engine.aa25PublishFault(AA25FaultLengthInvalid, aa25Now)
			aa25Engine.aa25DropFrame()
			return
		}
		aa25Engine.aa25FrameNN = aa25Logical
		aa25Engine.aa25FrameLeft = aa25Logical
		aa25Engine.aa25FramePhase = aa25FrameData
		if aa25Logical == 0 {
			aa25Engine.aa25FramePhase = aa25FrameCRC
		}
	case aa25FrameData:
		aa25Engine.aa25FrameLeft--
		if aa25Engine.aa25FrameLeft == 0 {
			aa25Engine.aa25FramePhase = aa25FrameCRC
		}
	case aa25FrameCRC:
		aa25Engine.aa25FramePhase = aa25FrameTrailing
		if aa25Logical != aa25Engine.aa25FrameCRC {
			aa25Engine.aa25PublishFault(AA25FaultBadCRC, aa25Now)
			aa25Engine.aa25PinnedValid = false
			return
		}
		aa25Engine.aa25Publish(AA25Event{AA25Kind: AA25EventFrameValid, AA25Tick: aa25Now})
		aa25Engine.aa25AdmitResponse(aa25Now)
	}
}

// aa25AdmitResponse schedules a pinned response whose shape matches exactly; a
// pin exists only for this frame and only after the destination matched.
// The schedule is anchored to the observed start of the final character and is
// independent of the RX grid; a stop bit validated at its centre does not move it.
func (aa25Engine *AA25Engine) aa25AdmitResponse(aa25Now uint64) {
	if !aa25Engine.aa25PinnedValid {
		return
	}
	if aa25Engine.aa25FramePB != aa25Engine.aa25Pinned.AA25MatchPB ||
		aa25Engine.aa25FrameSB != aa25Engine.aa25Pinned.AA25MatchSB ||
		aa25Engine.aa25FrameNN != aa25Engine.aa25Pinned.AA25MatchNN {
		aa25Engine.aa25PinnedValid = false
		return
	}
	aa25Engine.aa25TXState = aa25TXWaiting
	aa25Engine.aa25TXAnchor = aa25Engine.aa25CharStartTick
	aa25Engine.aa25TXBit = 0
	aa25Engine.aa25TXBase = 0
	aa25Engine.aa25TXRetried = false
	aa25Engine.aa25RetryCancel = false
	aa25Engine.aa25EchoIndex = 0
	aa25Engine.aa25Publish(AA25Event{
		AA25Kind:       AA25EventAdmitted,
		AA25Tick:       aa25Now,
		AA25Deadline:   aa25Engine.aa25TXDeadline(0),
		AA25Generation: aa25Engine.aa25Pinned.AA25Generation,
	})
}

// aa25TXDeadline is the absolute schedule of TX bit boundary aa25Bit, counted
// from the end of the request's final character plus the turnaround.
func (aa25Engine *AA25Engine) aa25TXDeadline(aa25Bit uint64) uint64 {
	return aa25RationalDeadline(aa25Engine.aa25TXAnchor+aa25Engine.aa25Config.AA25TurnaroundTicks,
		aa25BitsPerCharacter+aa25Bit-aa25Engine.aa25TXBase, aa25Engine.aa25Config.AA25TickHz, AA25NominalBaud)
}

func (aa25Engine *AA25Engine) aa25EchoDeadline() uint64 {
	return aa25Engine.aa25TXDeadline((aa25Engine.aa25EchoIndex+1)*aa25BitsPerCharacter) + aa25Engine.aa25Config.AA25EchoAllowanceTicks
}

func (aa25Engine *AA25Engine) aa25TXBitLevel(aa25Bit uint64) AA25Level {
	aa25Position := aa25Bit % aa25BitsPerCharacter
	switch aa25Position {
	case 0:
		return AA25LevelLow
	case aa25BitsPerCharacter - 1:
		return AA25LevelHigh
	default:
		return AA25Level(aa25Engine.aa25Pinned.aa25Wire[aa25Bit/aa25BitsPerCharacter] >> (aa25Position - 1) & 1)
	}
}

func (aa25Engine *AA25Engine) aa25StepTX(aa25Now uint64) {
	switch aa25Engine.aa25TXState {
	case aa25TXIdle:
		return
	case aa25TXAwaitAck:
		// The final-ACK wait is bounded; a late symbol is not this exchange's NACK.
		if aa25Now >= aa25Engine.aa25AckDeadline() {
			aa25Engine.aa25AbortTX(aa25Now)
		}
		return
	case aa25TXWaiting:
		if !aa25Engine.aa25FinalComplete || aa25Now < aa25Engine.aa25TXDeadline(aa25Engine.aa25TXBase) {
			return
		}
		aa25Engine.aa25TXState = aa25TXActive
	}
	aa25Length := uint64(aa25Engine.aa25Pinned.aa25WireLen)
	aa25Total := aa25Length * aa25BitsPerCharacter
	if aa25Engine.aa25EchoIndex < aa25Length && aa25Now >= aa25Engine.aa25EchoDeadline() {
		aa25Engine.aa25FailResponse(AA25FaultEchoMissing, aa25Now)
		return
	}
	if aa25Engine.aa25TXBit < aa25Total {
		aa25Deadline := aa25Engine.aa25TXDeadline(aa25Engine.aa25TXBit)
		if aa25Now < aa25Deadline {
			return
		}
		aa25Level := aa25Engine.aa25TXBitLevel(aa25Engine.aa25TXBit)
		if aa25Level != aa25Engine.aa25TXLevel {
			if aa25Now > aa25Deadline+aa25Engine.aa25Config.AA25TXLateToleranceTicks {
				aa25Engine.aa25FailResponse(AA25FaultTXLate, aa25Now)
				return
			}
			aa25Engine.aa25DriveEdge(aa25Level, aa25Deadline, aa25Now)
		}
		aa25Engine.aa25TXBit++
		return
	}
	if aa25Engine.aa25EchoIndex == aa25Length && aa25Now >= aa25Engine.aa25TXDeadline(aa25Total) {
		aa25Engine.aa25CompleteTX(aa25Now)
	}
}

// aa25CompleteTX ends the transmission once every symbol was driven and echoed.
func (aa25Engine *AA25Engine) aa25CompleteTX(aa25Now uint64) {
	aa25Engine.aa25Publish(AA25Event{
		AA25Kind:       AA25EventTXComplete,
		AA25Tick:       aa25Now,
		AA25Generation: aa25Engine.aa25Pinned.AA25Generation,
	})
	// The pin stays until the initiator's final symbol: a NACK there
	// retransmits the response portion once.
	aa25Engine.aa25TXState = aa25TXAwaitAck
	aa25Engine.aa25AckSince = aa25Now
}

// aa25AckDeadline is the end of the final-ACK wait: 200 ms of injected ticks.
func (aa25Engine *AA25Engine) aa25AckDeadline() uint64 {
	return aa25Engine.aa25AckSince + aa25Engine.aa25Config.AA25TickHz/5
}

// aa25HandleFinalAck consumes the initiator's symbol after the response. One
// NACK resends the response portion on a fresh absolute schedule anchored to
// that symbol; anything else ends the exchange. Never a retry loop.
func (aa25Engine *AA25Engine) aa25HandleFinalAck(aa25Symbol byte, aa25Now uint64) {
	aa25Expired := aa25Now >= aa25Engine.aa25AckDeadline()
	aa25Engine.aa25TXState = aa25TXIdle
	aa25Engine.aa25PinnedValid = false
	if aa25Symbol != aa25SymbolNack || aa25Expired {
		return
	}
	if aa25Engine.aa25RetryCancel {
		return
	}
	if aa25Engine.aa25TXRetried {
		aa25Engine.aa25PublishFault(AA25FaultNackExhausted, aa25Now)
		return
	}
	aa25Engine.aa25TXRetried = true
	aa25Engine.aa25PinnedValid = true
	aa25Engine.aa25TXState = aa25TXWaiting
	aa25Engine.aa25TXAnchor = aa25Engine.aa25CharStartTick
	aa25Engine.aa25TXBase = aa25BitsPerCharacter
	aa25Engine.aa25TXBit = aa25BitsPerCharacter
	aa25Engine.aa25EchoIndex = 1
}

func (aa25Engine *AA25Engine) aa25DriveEdge(aa25Level AA25Level, aa25Deadline, aa25Now uint64) {
	aa25Engine.aa25Ports.AA25Edges.AA25WriteEdge(aa25Level)
	aa25Engine.aa25TXLevel = aa25Level
	aa25Engine.aa25Stats.AA25TXEdges++
	aa25Engine.aa25Publish(AA25Event{
		AA25Kind:       AA25EventTXIntent,
		AA25Tick:       aa25Now,
		AA25Deadline:   aa25Deadline,
		AA25Level:      aa25Level,
		AA25Generation: aa25Engine.aa25Pinned.AA25Generation,
	})
}

// aa25AbortTX ends a response with no catch-up. A driven-low line gets exactly
// one release; software cannot undo a level already stretched while descheduled.
func (aa25Engine *AA25Engine) aa25AbortTX(aa25Now uint64) {
	if aa25Engine.aa25TXState == aa25TXIdle {
		return
	}
	aa25Engine.aa25TXState = aa25TXIdle
	aa25Engine.aa25PinnedValid = false
	if aa25Engine.aa25TXLevel == AA25LevelLow {
		aa25Engine.aa25Ports.AA25Edges.AA25WriteEdge(AA25LevelHigh)
		aa25Engine.aa25TXLevel = AA25LevelHigh
		aa25Engine.aa25Stats.AA25TXEdges++
		aa25Engine.aa25Publish(AA25Event{AA25Kind: AA25EventTXRelease, AA25Tick: aa25Now, AA25Level: AA25LevelHigh})
	}
}

// aa25FailResponse handles collisions, missing echo and missed deadlines: the
// response stops, reception resynchronizes and admission is withdrawn.
func (aa25Engine *AA25Engine) aa25FailResponse(aa25Fault AA25Fault, aa25Now uint64) {
	aa25Engine.aa25PublishFault(aa25Fault, aa25Now)
	aa25Engine.aa25InvalidateReception(aa25Now)
	aa25Engine.aa25WithdrawAdmission()
}

func (aa25Engine *AA25Engine) aa25InvalidateReception(aa25Now uint64) {
	aa25Engine.aa25AbortTX(aa25Now)
	aa25Engine.aa25DropFrame()
	aa25Engine.aa25RXState = aa25RXResync
	aa25Engine.aa25IdleRun = 0
	aa25Engine.aa25FinalComplete = false
}

// aa25WithdrawAdmission clears the prepared response. Only a new offer with a
// newer generation restores admission.
func (aa25Engine *AA25Engine) aa25WithdrawAdmission() {
	aa25Engine.aa25SlotValid = false
	aa25Engine.aa25PinnedValid = false
	aa25Engine.aa25Stats.AA25Withdrawals++
}

// aa25StepHousekeeping applies at most one queued command per step.
func (aa25Engine *AA25Engine) aa25StepHousekeeping(aa25Now uint64) {
	if aa25Engine.aa25ControlCount == 0 {
		return
	}
	aa25Item := &aa25Engine.aa25Controls[aa25Engine.aa25ControlHead]
	aa25Engine.aa25ControlHead = (aa25Engine.aa25ControlHead + 1) % aa25ControlCapacity
	aa25Engine.aa25ControlCount--
	aa25Engine.aa25Stats.AA25ControlsApplied++
	switch aa25Item.aa25Kind {
	case aa25ControlOffer:
		// Admission stays disabled while a refused event is unreported; the
		// generation is not consumed, so the same offer may be repeated once the
		// loss marker has been accepted.
		if aa25Engine.aa25LossPending {
			return
		}
		if aa25Item.aa25Prepared.AA25Generation <= aa25Engine.aa25LastGeneration {
			aa25Engine.aa25PublishFault(AA25FaultStaleGeneration, aa25Now)
			return
		}
		aa25Engine.aa25LastGeneration = aa25Item.aa25Prepared.AA25Generation
		aa25Engine.aa25Slot = aa25Item.aa25Prepared
		aa25Engine.aa25SlotValid = true
	case aa25ControlWithdraw:
		aa25Engine.aa25SlotValid = false
		aa25Engine.aa25Stats.AA25Withdrawals++
		// A response already on the wire keeps its pinned generation and finishes;
		// anything not yet on the wire loses admission here.
		if aa25Engine.aa25TXState == aa25TXWaiting || aa25Engine.aa25TXState == aa25TXAwaitAck {
			aa25Engine.aa25AbortTX(aa25Now)
		}
		// Retry cancellation outlives completion of a response still on the wire.
		aa25Engine.aa25RetryCancel = true
		if aa25Engine.aa25TXState == aa25TXIdle {
			aa25Engine.aa25PinnedValid = false
		}
	}
}

func (aa25Engine *AA25Engine) aa25PublishFault(aa25Fault AA25Fault, aa25Now uint64) {
	aa25Engine.aa25Publish(AA25Event{AA25Kind: AA25EventFault, AA25Fault: aa25Fault, AA25Tick: aa25Now})
}

func (aa25Engine *AA25Engine) aa25Publish(aa25Event AA25Event) {
	if !aa25Engine.aa25Ports.AA25Sink.AA25TryPublish(aa25Event) {
		aa25Engine.aa25Stats.AA25EventsDropped++
		aa25Engine.aa25LossPending = true
	}
}

// aa25ApplyLoss treats a refused event as a protocol gap. Once per loss it fails
// the affected state; the marker is then offered once per step without draining.
func (aa25Engine *AA25Engine) aa25ApplyLoss(aa25Now uint64) {
	if !aa25Engine.aa25LossPending {
		return
	}
	if !aa25Engine.aa25LossApplied {
		aa25Engine.aa25LossApplied = true
		aa25Engine.aa25InvalidateReception(aa25Now)
		aa25Engine.aa25WithdrawAdmission()
	}
	if aa25Engine.aa25Ports.AA25Sink.AA25TryPublish(AA25Event{
		AA25Kind:   AA25EventLoss,
		AA25Tick:   aa25Now,
		AA25Missed: aa25Engine.aa25Stats.AA25EventsDropped,
	}) {
		aa25Engine.aa25LossPending = false
		aa25Engine.aa25LossApplied = false
	}
}

// aa25InitiatorAddress reports whether both address nibbles are initiator
// parts (0, 1, 3, 7 or F), the only class that may open a request.
func aa25InitiatorAddress(aa25Address byte) bool {
	return aa25InitiatorNibble(aa25Address&0x0F) && aa25InitiatorNibble(aa25Address>>4)
}

func aa25InitiatorNibble(aa25Nibble byte) bool {
	return aa25Nibble == 0x0 || aa25Nibble == 0x1 || aa25Nibble == 0x3 || aa25Nibble == 0x7 || aa25Nibble == 0xF
}
