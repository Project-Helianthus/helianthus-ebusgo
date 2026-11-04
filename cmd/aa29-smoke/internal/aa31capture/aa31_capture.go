// Package aa31capture holds the platform-neutral parts of the aa29-smoke
// passive-capture command: the owner-provisioned profile document, the
// monotonic counter port, the bounded event recorder, the capture loop and the
// line-oriented report. It opens no device and drives no line.
//
// Status: Hypothesis. A capture records what the AA25 engine decoded from the
// sampled RX level; it is not a timing measurement, and physical timing, sample
// jitter and the electrical frontend remain Unknown.
package aa31capture

import (
	aa31bytes "bytes"
	aa31hex "encoding/hex"
	aa31json "encoding/json"
	aa31errors "errors"
	aa31fmt "fmt"
	aa31io "io"
	aa31bits "math/bits"
	aa31atomic "sync/atomic"
	aa31time "time"

	aa31localworker "github.com/Project-Helianthus/helianthus-ebusgo/localworker"
	aa31transport "github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

const (
	// AA31MaxProfileBytes bounds a profile document read.
	AA31MaxProfileBytes = 1 << 16
	// AA31MonotonicHz is the rate of AA31MonotonicCounter: one tick per
	// nanosecond of the process monotonic clock.
	AA31MonotonicHz uint64 = 1_000_000_000
	// AA31MaxDuration and AA31MaxEvents bound one capture.
	AA31MaxDuration = aa31time.Hour
	AA31MaxEvents   = 1 << 20

	aa31DigestBytes = 32
)

// The AA31 prefix is mandatory for every identifier in this ticket (enforced by
// TestAA31EveryDeclaredIdentifierIsPrefixed), which conflicts with the ErrFoo
// naming form that ST1012 expects.
var (
	AA31ErrProfile = aa31errors.New("aa29-smoke: profile document invalid") //nolint:staticcheck // ST1012: AA31 prefix is mandatory
	AA31ErrLimits  = aa31errors.New("aa29-smoke: capture limits invalid")   //nolint:staticcheck // ST1012: AA31 prefix is mandatory
)

// AA31GPIODocument mirrors the AA29 GPIO descriptor. It carries offsets inside
// the mapped window and pin masks, never a physical address. The descriptor
// role is implied by its position in the document.
type AA31GPIODocument struct {
	AA31MapLength     uint64 `json:"map_length"`
	AA31AccessWidth   uint8  `json:"access_width"`
	AA31Ordering      string `json:"ordering"`
	AA31RXLevelOffset uint64 `json:"rx_level_offset"`
	AA31TXSetOffset   uint64 `json:"tx_set_offset"`
	AA31TXClearOffset uint64 `json:"tx_clear_offset"`
	AA31RXMask        uint32 `json:"rx_mask"`
	AA31TXMask        uint32 `json:"tx_mask"`
	AA31RXInverted    bool   `json:"rx_inverted"`
	AA31TXInverted    bool   `json:"tx_inverted"`
}

// AA31CounterDocument mirrors the AA29 counter descriptor.
type AA31CounterDocument struct {
	AA31Hz uint64 `json:"hz"`
}

// AA31ProfileDocument is the owner-provisioned AA29 platform profile in JSON
// form. Decoding checks only the document shape; admission, qualification and
// descriptor validity stay with the AA29 backend.
type AA31ProfileDocument struct {
	AA31ProfileID           string              `json:"profile_id"`
	AA31Family              string              `json:"family"`
	AA31Qualification       string              `json:"qualification"`
	AA31QualificationDigest string              `json:"qualification_digest"`
	AA31GPIO                AA31GPIODocument    `json:"gpio"`
	AA31Counter             AA31CounterDocument `json:"counter"`
	AA31MaxLeaseTicks       uint64              `json:"max_lease_ticks"`
}

// AA31DecodeProfile reads exactly one bounded JSON document with no unknown
// fields, a known qualification and ordering, and a full-length hex digest.
func AA31DecodeProfile(aa31Reader aa31io.Reader) (AA31ProfileDocument, error) {
	aa31Source, aa31Err := aa31io.ReadAll(aa31io.LimitReader(aa31Reader, AA31MaxProfileBytes+1))
	if aa31Err != nil {
		return AA31ProfileDocument{}, aa31fmt.Errorf("%w: %w", AA31ErrProfile, aa31Err)
	}
	if len(aa31Source) > AA31MaxProfileBytes {
		return AA31ProfileDocument{}, aa31fmt.Errorf("%w: document exceeds %d bytes", AA31ErrProfile, AA31MaxProfileBytes)
	}
	aa31Decoder := aa31json.NewDecoder(aa31bytes.NewReader(aa31Source))
	aa31Decoder.DisallowUnknownFields()
	var aa31Document AA31ProfileDocument
	if aa31Err := aa31Decoder.Decode(&aa31Document); aa31Err != nil {
		return AA31ProfileDocument{}, aa31fmt.Errorf("%w: %w", AA31ErrProfile, aa31Err)
	}
	if _, aa31Err := aa31Decoder.Token(); !aa31errors.Is(aa31Err, aa31io.EOF) {
		return AA31ProfileDocument{}, aa31fmt.Errorf("%w: trailing data", AA31ErrProfile)
	}
	if _, aa31Err := AA31ParseQualification(aa31Document.AA31Qualification); aa31Err != nil {
		return AA31ProfileDocument{}, aa31Err
	}
	if !AA31KnownOrdering(aa31Document.AA31GPIO.AA31Ordering) {
		return AA31ProfileDocument{}, aa31fmt.Errorf("%w: unknown ordering %q", AA31ErrProfile, aa31Document.AA31GPIO.AA31Ordering)
	}
	if _, aa31Err := AA31ParseDigest(aa31Document.AA31QualificationDigest); aa31Err != nil {
		return AA31ProfileDocument{}, aa31Err
	}
	return aa31Document, nil
}

// AA31ParseQualification maps a document qualification label onto AA22
// evidence. Only "proven" is affirmative; the backend refuses the rest.
func AA31ParseQualification(aa31Label string) (aa31transport.AA22Evidence, error) {
	switch aa31Label {
	case "unknown":
		return aa31transport.AA22Unknown, nil
	case "hypothesis":
		return aa31transport.AA22Hypothesis, nil
	case "proven":
		return aa31transport.AA22Proven, nil
	}
	return aa31transport.AA22Unknown, aa31fmt.Errorf("%w: unknown qualification %q", AA31ErrProfile, aa31Label)
}

// AA31KnownOrdering reports whether a document ordering label is one the AA29
// descriptor can express: "unknown", "relaxed" or "device".
func AA31KnownOrdering(aa31Label string) bool {
	return aa31Label == "unknown" || aa31Label == "relaxed" || aa31Label == "device"
}

// AA31ParseDigest decodes a lower- or upper-case hex SHA-256 digest.
func AA31ParseDigest(aa31Text string) ([aa31DigestBytes]byte, error) {
	var aa31Digest [aa31DigestBytes]byte
	if len(aa31Text) != 2*aa31DigestBytes {
		return aa31Digest, aa31fmt.Errorf("%w: qualification digest must be %d hex digits", AA31ErrProfile, 2*aa31DigestBytes)
	}
	if _, aa31Err := aa31hex.Decode(aa31Digest[:], []byte(aa31Text)); aa31Err != nil {
		return [aa31DigestBytes]byte{}, aa31fmt.Errorf("%w: %w", AA31ErrProfile, aa31Err)
	}
	return aa31Digest, nil
}

// AA31MonotonicCounter is a counter port over the process monotonic clock. It
// never blocks and never goes backwards. Its rate is AA31MonotonicHz, so an
// admitted profile must declare that rate for the counter.
type AA31MonotonicCounter struct {
	aa31Base aa31time.Time
}

// AA31NewMonotonicCounter anchors tick zero at the call.
func AA31NewMonotonicCounter() *AA31MonotonicCounter {
	return &AA31MonotonicCounter{aa31Base: aa31time.Now()}
}

// AA29CounterHz reports the counter rate.
func (aa31Counter *AA31MonotonicCounter) AA29CounterHz() uint64 {
	return AA31MonotonicHz
}

// AA29ReadCounter reads elapsed monotonic nanoseconds since the anchor.
func (aa31Counter *AA31MonotonicCounter) AA29ReadCounter() uint64 {
	return uint64(aa31time.Since(aa31Counter.aa31Base))
}

// AA31BudgetTicks converts a capture duration to counter ticks, rejecting a
// non-positive or over-long duration, a zero rate and any product that does not
// fit 64 bits.
func AA31BudgetTicks(aa31Duration aa31time.Duration, aa31Hz uint64) (uint64, error) {
	if aa31Duration <= 0 || aa31Duration > AA31MaxDuration || aa31Hz == 0 {
		return 0, AA31ErrLimits
	}
	aa31High, aa31Low := aa31bits.Mul64(uint64(aa31Duration), aa31Hz)
	if aa31High >= uint64(aa31time.Second) {
		return 0, AA31ErrLimits
	}
	aa31Ticks, _ := aa31bits.Div64(aa31High, aa31Low, uint64(aa31time.Second))
	if aa31Ticks == 0 {
		return 0, AA31ErrLimits
	}
	return aa31Ticks, nil
}

// AA31Recorder is a fixed-capacity AA25 event sink. It allocates only at
// construction; a full recorder refuses and counts every further event.
type AA31Recorder struct {
	aa31Events  []aa31localworker.AA25Event
	aa31Refused uint64
}

// AA31NewRecorder allocates a recorder for 1..AA31MaxEvents events.
func AA31NewRecorder(aa31Capacity int) (*AA31Recorder, error) {
	if aa31Capacity <= 0 || aa31Capacity > AA31MaxEvents {
		return nil, AA31ErrLimits
	}
	return &AA31Recorder{aa31Events: make([]aa31localworker.AA25Event, 0, aa31Capacity)}, nil
}

// AA25TryPublish records one event without blocking.
func (aa31Recorder *AA31Recorder) AA25TryPublish(aa31Event aa31localworker.AA25Event) bool {
	if len(aa31Recorder.aa31Events) == cap(aa31Recorder.aa31Events) {
		aa31Recorder.aa31Refused++
		return false
	}
	aa31Recorder.aa31Events = append(aa31Recorder.aa31Events, aa31Event)
	return true
}

// AA31Full reports whether the next event would be refused.
func (aa31Recorder *AA31Recorder) AA31Full() bool {
	return len(aa31Recorder.aa31Events) == cap(aa31Recorder.aa31Events)
}

// AA31Events returns the recorded events in publication order.
func (aa31Recorder *AA31Recorder) AA31Events() []aa31localworker.AA25Event {
	return aa31Recorder.aa31Events
}

// AA31Refused returns the number of refused events.
func (aa31Recorder *AA31Recorder) AA31Refused() uint64 {
	return aa31Recorder.aa31Refused
}

// AA31Stepper is the part of the AA25 engine the capture loop drives.
type AA31Stepper interface {
	AA25Step()
}

// AA31StopReason says why a capture ended.
type AA31StopReason uint8

const (
	AA31StopNone AA31StopReason = iota
	AA31StopBudget
	AA31StopRecorderFull
	AA31StopRequested
	AA31StopTickRegression
)

// AA31StopName returns the report label for a stop reason.
func AA31StopName(aa31Reason AA31StopReason) string {
	switch aa31Reason {
	case AA31StopBudget:
		return "budget"
	case AA31StopRecorderFull:
		return "recorder_full"
	case AA31StopRequested:
		return "requested"
	case AA31StopTickRegression:
		return "tick_regression"
	}
	return "none"
}

// AA31Result summarizes one capture loop in counter ticks only.
type AA31Result struct {
	AA31Steps        uint64
	AA31ElapsedTicks uint64
	AA31Stop         AA31StopReason
}

// AA31Run steps the engine until the tick budget is spent, the recorder is
// full, a stop is requested or the counter regresses. It never offers a
// prepared response, so the engine it drives never transmits.
func AA31Run(aa31Engine AA31Stepper, aa31Ticks aa31localworker.AA25TickPort, aa31Recorder *AA31Recorder, aa31BudgetTicks uint64, aa31Stop *aa31atomic.Bool) AA31Result {
	var aa31Result AA31Result
	aa31Start := aa31Ticks.AA25NowTicks()
	aa31Prev := aa31Start
	for {
		if aa31Stop != nil && aa31Stop.Load() {
			aa31Result.AA31Stop = AA31StopRequested
			return aa31Result
		}
		aa31Engine.AA25Step()
		aa31Result.AA31Steps++
		aa31Now := aa31Ticks.AA25NowTicks()
		if aa31Now < aa31Prev {
			aa31Result.AA31Stop = AA31StopTickRegression
			return aa31Result
		}
		aa31Prev = aa31Now
		aa31Result.AA31ElapsedTicks = aa31Now - aa31Start
		if aa31Recorder.AA31Full() {
			aa31Result.AA31Stop = AA31StopRecorderFull
			return aa31Result
		}
		if aa31Result.AA31ElapsedTicks >= aa31BudgetTicks {
			aa31Result.AA31Stop = AA31StopBudget
			return aa31Result
		}
	}
}

// AA31KindName returns the report label for an engine event kind.
func AA31KindName(aa31Kind aa31localworker.AA25EventKind) string {
	switch aa31Kind {
	case aa31localworker.AA25EventRawSymbol:
		return "raw_symbol"
	case aa31localworker.AA25EventLogicalByte:
		return "logical_byte"
	case aa31localworker.AA25EventGap:
		return "gap"
	case aa31localworker.AA25EventFault:
		return "fault"
	case aa31localworker.AA25EventProvisional:
		return "provisional"
	case aa31localworker.AA25EventFrameValid:
		return "frame_valid"
	case aa31localworker.AA25EventAdmitted:
		return "admitted"
	case aa31localworker.AA25EventTXIntent:
		return "tx_intent"
	case aa31localworker.AA25EventTXRelease:
		return "tx_release"
	case aa31localworker.AA25EventTXComplete:
		return "tx_complete"
	case aa31localworker.AA25EventLoss:
		return "loss"
	}
	return aa31fmt.Sprintf("kind_%d", aa31Kind)
}

// AA31FaultName returns the report label for an engine fault.
func AA31FaultName(aa31Fault aa31localworker.AA25Fault) string {
	switch aa31Fault {
	case aa31localworker.AA25FaultNone:
		return ""
	case aa31localworker.AA25FaultFalseStart:
		return "false_start"
	case aa31localworker.AA25FaultBadStop:
		return "bad_stop"
	case aa31localworker.AA25FaultEscapeInvalid:
		return "escape_invalid"
	case aa31localworker.AA25FaultSynInFrame:
		return "syn_in_frame"
	case aa31localworker.AA25FaultAddressInvalid:
		return "address_invalid"
	case aa31localworker.AA25FaultBadCRC:
		return "bad_crc"
	case aa31localworker.AA25FaultTickRegression:
		return "tick_regression"
	case aa31localworker.AA25FaultStaleGeneration:
		return "stale_generation"
	case aa31localworker.AA25FaultRXBeforeResponse:
		return "rx_before_response"
	case aa31localworker.AA25FaultEchoMismatch:
		return "echo_mismatch"
	case aa31localworker.AA25FaultEchoMissing:
		return "echo_missing"
	case aa31localworker.AA25FaultEchoUnsolicited:
		return "echo_unsolicited"
	case aa31localworker.AA25FaultTXLate:
		return "tx_late"
	case aa31localworker.AA25FaultLengthInvalid:
		return "length_invalid"
	case aa31localworker.AA25FaultNackExhausted:
		return "nack_exhausted"
	}
	return aa31fmt.Sprintf("fault_%d", aa31Fault)
}

// AA31EventRecord is the report form of one engine event. Ticks are counter
// ticks relative to the counter anchor, never wall-clock times.
type AA31EventRecord struct {
	AA31Record      string    `json:"record"`
	AA31Kind        string    `json:"kind"`
	AA31Fault       string    `json:"fault,omitempty"`
	AA31Tick        uint64    `json:"tick"`
	AA31SampleIndex uint64    `json:"sample_index"`
	AA31StartTick   uint64    `json:"start_tick"`
	AA31Deadline    uint64    `json:"deadline"`
	AA31Missed      uint64    `json:"missed"`
	AA31Generation  uint64    `json:"generation"`
	AA31Raw         byte      `json:"raw"`
	AA31Logical     byte      `json:"logical"`
	AA31WasEscaped  bool      `json:"was_escaped"`
	AA31Echo        bool      `json:"echo"`
	AA31Level       uint8     `json:"level"`
	AA31Dissent     uint8     `json:"dissent"`
	AA31EdgePhase   [10]uint8 `json:"edge_phase"`
}

// AA31Record converts one engine event, keeping every field.
func AA31Record(aa31Event aa31localworker.AA25Event) AA31EventRecord {
	return AA31EventRecord{
		AA31Record:      "event",
		AA31Kind:        AA31KindName(aa31Event.AA25Kind),
		AA31Fault:       AA31FaultName(aa31Event.AA25Fault),
		AA31Tick:        aa31Event.AA25Tick,
		AA31SampleIndex: aa31Event.AA25SampleIndex,
		AA31StartTick:   aa31Event.AA25StartTick,
		AA31Deadline:    aa31Event.AA25Deadline,
		AA31Missed:      aa31Event.AA25Missed,
		AA31Generation:  aa31Event.AA25Generation,
		AA31Raw:         aa31Event.AA25Raw,
		AA31Logical:     aa31Event.AA25Logical,
		AA31WasEscaped:  aa31Event.AA25WasEscaped,
		AA31Echo:        aa31Event.AA25Echo,
		AA31Level:       uint8(aa31Event.AA25Level),
		AA31Dissent:     aa31Event.AA25Dissent,
		AA31EdgePhase:   aa31Event.AA25EdgePhase,
	}
}

// AA31BackendCounters copies the AA29 backend counters the report checks.
type AA31BackendCounters struct {
	AA31StoredEdges   uint64 `json:"stored_edges"`
	AA31RefusedEdges  uint64 `json:"refused_edges"`
	AA31Releases      uint64 `json:"releases"`
	AA31LeaseExpiries uint64 `json:"lease_expiries"`
}

// AA31EngineCounters copies the AA25 engine counters.
type AA31EngineCounters struct {
	AA31Observations       uint64 `json:"observations"`
	AA31MissedSamples      uint64 `json:"missed_samples"`
	AA31MaxObservationLate uint64 `json:"max_observation_late"`
	AA31TXEdges            uint64 `json:"tx_edges"`
	AA31EchoedSymbols      uint64 `json:"echoed_symbols"`
	AA31EventsDropped      uint64 `json:"events_dropped"`
	AA31ControlsApplied    uint64 `json:"controls_applied"`
	AA31Withdrawals        uint64 `json:"withdrawals"`
}

// AA31EngineCountersFrom copies every AA25 engine counter.
func AA31EngineCountersFrom(aa31Stats aa31localworker.AA25Stats) AA31EngineCounters {
	return AA31EngineCounters{
		AA31Observations:       aa31Stats.AA25Observations,
		AA31MissedSamples:      aa31Stats.AA25MissedSamples,
		AA31MaxObservationLate: aa31Stats.AA25MaxObservationLate,
		AA31TXEdges:            aa31Stats.AA25TXEdges,
		AA31EchoedSymbols:      aa31Stats.AA25EchoedSymbols,
		AA31EventsDropped:      aa31Stats.AA25EventsDropped,
		AA31ControlsApplied:    aa31Stats.AA25ControlsApplied,
		AA31Withdrawals:        aa31Stats.AA25Withdrawals,
	}
}

// AA31Summary is the final report record. AA31Passive is derived, never set by
// a caller: see AA31Seal.
type AA31Summary struct {
	AA31Record          string              `json:"record"`
	AA31ProfileID       string              `json:"profile_id"`
	AA31Family          string              `json:"family"`
	AA31CounterHz       uint64              `json:"counter_hz"`
	AA31BudgetTicks     uint64              `json:"budget_ticks"`
	AA31ElapsedTicks    uint64              `json:"elapsed_ticks"`
	AA31Steps           uint64              `json:"steps"`
	AA31Stop            string              `json:"stop"`
	AA31EventsRecorded  uint64              `json:"events_recorded"`
	AA31EventsRefused   uint64              `json:"events_refused"`
	AA31Engine          AA31EngineCounters  `json:"engine"`
	AA31Backend         AA31BackendCounters `json:"backend"`
	AA31WindowStores    uint64              `json:"window_store_attempts"`
	AA31TimingEvidence  string              `json:"timing_evidence"`
	AA31CaptureEvidence string              `json:"capture_evidence"`
	AA31Passive         bool                `json:"passive"`
}

// AA31Seal fills the derived fields. A capture is passive only if the engine
// produced no TX edge, the backend stored, refused and released nothing, no
// lease expired and the window saw no store attempt.
func AA31Seal(aa31Summary AA31Summary) AA31Summary {
	aa31Summary.AA31Record = "summary"
	aa31Summary.AA31TimingEvidence = "unknown"
	aa31Summary.AA31CaptureEvidence = "hypothesis"
	aa31Summary.AA31Passive = aa31Summary.AA31Engine.AA31TXEdges == 0 &&
		aa31Summary.AA31Backend == AA31BackendCounters{} &&
		aa31Summary.AA31WindowStores == 0
	return aa31Summary
}

// AA31WriteReport writes one JSON line per recorded event, then the sealed
// summary line.
func AA31WriteReport(aa31Writer aa31io.Writer, aa31Events []aa31localworker.AA25Event, aa31Summary AA31Summary) error {
	aa31Encoder := aa31json.NewEncoder(aa31Writer)
	for _, aa31Event := range aa31Events {
		if aa31Err := aa31Encoder.Encode(AA31Record(aa31Event)); aa31Err != nil {
			return aa31Err
		}
	}
	return aa31Encoder.Encode(AA31Seal(aa31Summary))
}
