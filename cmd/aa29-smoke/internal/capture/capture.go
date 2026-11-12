// Package capture holds the platform-neutral parts of the aa29-smoke
// passive-capture command: the owner-provisioned profile document, the
// monotonic counter port, the bounded event recorder, the capture loop and the
// line-oriented report. It opens no device and drives no line.
//
// Status: Hypothesis. A capture records what the AA25 engine decoded from the
// sampled RX level; it is not a timing measurement, and physical timing, sample
// jitter and the electrical frontend remain Unknown.
package capture

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"sync/atomic"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/localworker"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

const (
	// MaxProfileBytes bounds a profile document read.
	MaxProfileBytes = 1 << 16
	// MonotonicHz is the rate of MonotonicCounter: one tick per
	// nanosecond of the process monotonic clock.
	MonotonicHz uint64 = 1_000_000_000
	// MaxDuration and MaxEvents bound one capture.
	MaxDuration = time.Hour
	MaxEvents   = 1 << 20

	digestBytes = 32
)

// ErrProfile and ErrLimits classify a refused profile document and refused
// capture limits.
var (
	ErrProfile = errors.New("aa29-smoke: profile document invalid")
	ErrLimits  = errors.New("aa29-smoke: capture limits invalid")
)

// GPIODocument mirrors the AA29 GPIO descriptor. It carries offsets inside
// the mapped window and pin masks, never a physical address. The descriptor
// role is implied by its position in the document.
type GPIODocument struct {
	MapLength     uint64 `json:"map_length"`
	AccessWidth   uint8  `json:"access_width"`
	Ordering      string `json:"ordering"`
	RXLevelOffset uint64 `json:"rx_level_offset"`
	TXSetOffset   uint64 `json:"tx_set_offset"`
	TXClearOffset uint64 `json:"tx_clear_offset"`
	RXMask        uint32 `json:"rx_mask"`
	TXMask        uint32 `json:"tx_mask"`
	RXInverted    bool   `json:"rx_inverted"`
	TXInverted    bool   `json:"tx_inverted"`
}

// CounterDocument mirrors the AA29 counter descriptor.
type CounterDocument struct {
	Hz uint64 `json:"hz"`
}

// ProfileDocument is the owner-provisioned AA29 platform profile in JSON
// form. Decoding checks only the document shape; admission, qualification and
// descriptor validity stay with the AA29 backend.
type ProfileDocument struct {
	ProfileID           string          `json:"profile_id"`
	Family              string          `json:"family"`
	Qualification       string          `json:"qualification"`
	QualificationDigest string          `json:"qualification_digest"`
	GPIO                GPIODocument    `json:"gpio"`
	Counter             CounterDocument `json:"counter"`
	MaxLeaseTicks       uint64          `json:"max_lease_ticks"`
}

// DecodeProfile reads exactly one bounded JSON document with no unknown
// fields, a known qualification and ordering, and a full-length hex digest.
func DecodeProfile(reader io.Reader) (ProfileDocument, error) {
	source, err := io.ReadAll(io.LimitReader(reader, MaxProfileBytes+1))
	if err != nil {
		return ProfileDocument{}, fmt.Errorf("%w: %w", ErrProfile, err)
	}
	if len(source) > MaxProfileBytes {
		return ProfileDocument{}, fmt.Errorf("%w: document exceeds %d bytes", ErrProfile, MaxProfileBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.DisallowUnknownFields()
	var document ProfileDocument
	if err := decoder.Decode(&document); err != nil {
		return ProfileDocument{}, fmt.Errorf("%w: %w", ErrProfile, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ProfileDocument{}, fmt.Errorf("%w: trailing data", ErrProfile)
	}
	if _, err := ParseQualification(document.Qualification); err != nil {
		return ProfileDocument{}, err
	}
	if !KnownOrdering(document.GPIO.Ordering) {
		return ProfileDocument{}, fmt.Errorf("%w: unknown ordering %q", ErrProfile, document.GPIO.Ordering)
	}
	if _, err := ParseDigest(document.QualificationDigest); err != nil {
		return ProfileDocument{}, err
	}
	return document, nil
}

// ParseQualification maps a document qualification label onto AA22
// evidence. Only "proven" is affirmative; the backend refuses the rest.
func ParseQualification(label string) (transport.AA22Evidence, error) {
	switch label {
	case "unknown":
		return transport.AA22Unknown, nil
	case "hypothesis":
		return transport.AA22Hypothesis, nil
	case "proven":
		return transport.AA22Proven, nil
	}
	return transport.AA22Unknown, fmt.Errorf("%w: unknown qualification %q", ErrProfile, label)
}

// KnownOrdering reports whether a document ordering label is one the AA29
// descriptor can express: "unknown", "relaxed" or "device".
func KnownOrdering(label string) bool {
	return label == "unknown" || label == "relaxed" || label == "device"
}

// ParseDigest decodes a lower- or upper-case hex SHA-256 digest.
func ParseDigest(text string) ([digestBytes]byte, error) {
	var digest [digestBytes]byte
	if len(text) != 2*digestBytes {
		return digest, fmt.Errorf("%w: qualification digest must be %d hex digits", ErrProfile, 2*digestBytes)
	}
	if _, err := hex.Decode(digest[:], []byte(text)); err != nil {
		return [digestBytes]byte{}, fmt.Errorf("%w: %w", ErrProfile, err)
	}
	return digest, nil
}

// MonotonicCounter is a counter port over the process monotonic clock. It
// never blocks and never goes backwards. Its rate is MonotonicHz, so an
// admitted profile must declare that rate for the counter.
type MonotonicCounter struct {
	base time.Time
}

// NewMonotonicCounter anchors tick zero at the call.
func NewMonotonicCounter() *MonotonicCounter {
	return &MonotonicCounter{base: time.Now()}
}

// AA29CounterHz reports the counter rate.
func (counter *MonotonicCounter) AA29CounterHz() uint64 {
	return MonotonicHz
}

// AA29ReadCounter reads elapsed monotonic nanoseconds since the anchor.
func (counter *MonotonicCounter) AA29ReadCounter() uint64 {
	return uint64(time.Since(counter.base))
}

// BudgetTicks converts a capture duration to counter ticks, rejecting a
// non-positive or over-long duration, a zero rate and any product that does not
// fit 64 bits.
func BudgetTicks(duration time.Duration, hz uint64) (uint64, error) {
	if duration <= 0 || duration > MaxDuration || hz == 0 {
		return 0, ErrLimits
	}
	high, low := bits.Mul64(uint64(duration), hz)
	if high >= uint64(time.Second) {
		return 0, ErrLimits
	}
	ticks, _ := bits.Div64(high, low, uint64(time.Second))
	if ticks == 0 {
		return 0, ErrLimits
	}
	return ticks, nil
}

// Recorder is a fixed-capacity AA25 event sink. It allocates only at
// construction; a full recorder refuses and counts every further event.
type Recorder struct {
	events  []localworker.AA25Event
	refused uint64
}

// NewRecorder allocates a recorder for 1..MaxEvents events.
func NewRecorder(capacity int) (*Recorder, error) {
	if capacity <= 0 || capacity > MaxEvents {
		return nil, ErrLimits
	}
	return &Recorder{events: make([]localworker.AA25Event, 0, capacity)}, nil
}

// AA25TryPublish records one event without blocking.
func (recorder *Recorder) AA25TryPublish(event localworker.AA25Event) bool {
	if len(recorder.events) == cap(recorder.events) {
		recorder.refused++
		return false
	}
	recorder.events = append(recorder.events, event)
	return true
}

// Full reports whether the next event would be refused.
func (recorder *Recorder) Full() bool {
	return len(recorder.events) == cap(recorder.events)
}

// Events returns the recorded events in publication order.
func (recorder *Recorder) Events() []localworker.AA25Event {
	return recorder.events
}

// Refused returns the number of refused events.
func (recorder *Recorder) Refused() uint64 {
	return recorder.refused
}

// Stepper is the part of the AA25 engine the capture loop drives.
type Stepper interface {
	AA25Step()
}

// StopReason says why a capture ended.
type StopReason uint8

const (
	StopNone StopReason = iota
	StopBudget
	StopRecorderFull
	StopRequested
	StopTickRegression
)

// StopName returns the report label for a stop reason.
func StopName(reason StopReason) string {
	switch reason {
	case StopBudget:
		return "budget"
	case StopRecorderFull:
		return "recorder_full"
	case StopRequested:
		return "requested"
	case StopTickRegression:
		return "tick_regression"
	}
	return "none"
}

// Result summarizes one capture loop in counter ticks only.
type Result struct {
	Steps        uint64
	ElapsedTicks uint64
	Stop         StopReason
}

// Run steps the engine until the tick budget is spent, the recorder is
// full, a stop is requested or the counter regresses. It never offers a
// prepared response, so the engine it drives never transmits.
func Run(engine Stepper, ticks localworker.AA25TickPort, recorder *Recorder, budgetTicks uint64, stop *atomic.Bool) Result {
	var result Result
	start := ticks.AA25NowTicks()
	prev := start
	for {
		if stop != nil && stop.Load() {
			result.Stop = StopRequested
			return result
		}
		engine.AA25Step()
		result.Steps++
		now := ticks.AA25NowTicks()
		if now < prev {
			result.Stop = StopTickRegression
			return result
		}
		prev = now
		result.ElapsedTicks = now - start
		if recorder.Full() {
			result.Stop = StopRecorderFull
			return result
		}
		if result.ElapsedTicks >= budgetTicks {
			result.Stop = StopBudget
			return result
		}
	}
}

// KindName returns the report label for an engine event kind.
func KindName(kind localworker.AA25EventKind) string {
	switch kind {
	case localworker.AA25EventRawSymbol:
		return "raw_symbol"
	case localworker.AA25EventLogicalByte:
		return "logical_byte"
	case localworker.AA25EventGap:
		return "gap"
	case localworker.AA25EventFault:
		return "fault"
	case localworker.AA25EventProvisional:
		return "provisional"
	case localworker.AA25EventFrameValid:
		return "frame_valid"
	case localworker.AA25EventAdmitted:
		return "admitted"
	case localworker.AA25EventTXIntent:
		return "tx_intent"
	case localworker.AA25EventTXRelease:
		return "tx_release"
	case localworker.AA25EventTXComplete:
		return "tx_complete"
	case localworker.AA25EventLoss:
		return "loss"
	}
	return fmt.Sprintf("kind_%d", kind)
}

// FaultName returns the report label for an engine fault.
func FaultName(fault localworker.AA25Fault) string {
	switch fault {
	case localworker.AA25FaultNone:
		return ""
	case localworker.AA25FaultFalseStart:
		return "false_start"
	case localworker.AA25FaultBadStop:
		return "bad_stop"
	case localworker.AA25FaultEscapeInvalid:
		return "escape_invalid"
	case localworker.AA25FaultSynInFrame:
		return "syn_in_frame"
	case localworker.AA25FaultAddressInvalid:
		return "address_invalid"
	case localworker.AA25FaultBadCRC:
		return "bad_crc"
	case localworker.AA25FaultTickRegression:
		return "tick_regression"
	case localworker.AA25FaultStaleGeneration:
		return "stale_generation"
	case localworker.AA25FaultRXBeforeResponse:
		return "rx_before_response"
	case localworker.AA25FaultEchoMismatch:
		return "echo_mismatch"
	case localworker.AA25FaultEchoMissing:
		return "echo_missing"
	case localworker.AA25FaultEchoUnsolicited:
		return "echo_unsolicited"
	case localworker.AA25FaultTXLate:
		return "tx_late"
	case localworker.AA25FaultLengthInvalid:
		return "length_invalid"
	case localworker.AA25FaultNackExhausted:
		return "nack_exhausted"
	}
	return fmt.Sprintf("fault_%d", fault)
}

// EventRecord is the report form of one engine event. Ticks are counter
// ticks relative to the counter anchor, never wall-clock times.
type EventRecord struct {
	Record      string    `json:"record"`
	Kind        string    `json:"kind"`
	Fault       string    `json:"fault,omitempty"`
	Tick        uint64    `json:"tick"`
	SampleIndex uint64    `json:"sample_index"`
	StartTick   uint64    `json:"start_tick"`
	Deadline    uint64    `json:"deadline"`
	Missed      uint64    `json:"missed"`
	Generation  uint64    `json:"generation"`
	Raw         byte      `json:"raw"`
	Logical     byte      `json:"logical"`
	WasEscaped  bool      `json:"was_escaped"`
	Echo        bool      `json:"echo"`
	Level       uint8     `json:"level"`
	Dissent     uint8     `json:"dissent"`
	EdgePhase   [10]uint8 `json:"edge_phase"`
}

// Record converts one engine event, keeping every field.
func Record(event localworker.AA25Event) EventRecord {
	return EventRecord{
		Record:      "event",
		Kind:        KindName(event.AA25Kind),
		Fault:       FaultName(event.AA25Fault),
		Tick:        event.AA25Tick,
		SampleIndex: event.AA25SampleIndex,
		StartTick:   event.AA25StartTick,
		Deadline:    event.AA25Deadline,
		Missed:      event.AA25Missed,
		Generation:  event.AA25Generation,
		Raw:         event.AA25Raw,
		Logical:     event.AA25Logical,
		WasEscaped:  event.AA25WasEscaped,
		Echo:        event.AA25Echo,
		Level:       uint8(event.AA25Level),
		Dissent:     event.AA25Dissent,
		EdgePhase:   event.AA25EdgePhase,
	}
}

// BackendCounters copies the AA29 backend counters the report checks.
type BackendCounters struct {
	StoredEdges   uint64 `json:"stored_edges"`
	RefusedEdges  uint64 `json:"refused_edges"`
	Releases      uint64 `json:"releases"`
	LeaseExpiries uint64 `json:"lease_expiries"`
}

// EngineCounters copies the AA25 engine counters.
type EngineCounters struct {
	Observations       uint64 `json:"observations"`
	MissedSamples      uint64 `json:"missed_samples"`
	MaxObservationLate uint64 `json:"max_observation_late"`
	TXEdges            uint64 `json:"tx_edges"`
	EchoedSymbols      uint64 `json:"echoed_symbols"`
	EventsDropped      uint64 `json:"events_dropped"`
	ControlsApplied    uint64 `json:"controls_applied"`
	Withdrawals        uint64 `json:"withdrawals"`
}

// EngineCountersFrom copies every AA25 engine counter.
func EngineCountersFrom(stats localworker.AA25Stats) EngineCounters {
	return EngineCounters{
		Observations:       stats.AA25Observations,
		MissedSamples:      stats.AA25MissedSamples,
		MaxObservationLate: stats.AA25MaxObservationLate,
		TXEdges:            stats.AA25TXEdges,
		EchoedSymbols:      stats.AA25EchoedSymbols,
		EventsDropped:      stats.AA25EventsDropped,
		ControlsApplied:    stats.AA25ControlsApplied,
		Withdrawals:        stats.AA25Withdrawals,
	}
}

// Summary is the final report record. Passive is derived, never set by
// a caller: see Seal.
type Summary struct {
	Record          string          `json:"record"`
	ProfileID       string          `json:"profile_id"`
	Family          string          `json:"family"`
	CounterHz       uint64          `json:"counter_hz"`
	BudgetTicks     uint64          `json:"budget_ticks"`
	ElapsedTicks    uint64          `json:"elapsed_ticks"`
	Steps           uint64          `json:"steps"`
	Stop            string          `json:"stop"`
	EventsRecorded  uint64          `json:"events_recorded"`
	EventsRefused   uint64          `json:"events_refused"`
	Engine          EngineCounters  `json:"engine"`
	Backend         BackendCounters `json:"backend"`
	WindowStores    uint64          `json:"window_store_attempts"`
	TimingEvidence  string          `json:"timing_evidence"`
	CaptureEvidence string          `json:"capture_evidence"`
	Passive         bool            `json:"passive"`
}

// Seal fills the derived fields. A capture is passive only if the engine
// produced no TX edge, the backend stored, refused and released nothing, no
// lease expired and the window saw no store attempt.
func Seal(summary Summary) Summary {
	summary.Record = "summary"
	summary.TimingEvidence = "unknown"
	summary.CaptureEvidence = "hypothesis"
	summary.Passive = summary.Engine.TXEdges == 0 &&
		summary.Backend == BackendCounters{} &&
		summary.WindowStores == 0
	return summary
}

// WriteReport writes one JSON line per recorded event, then the sealed
// summary line.
func WriteReport(writer io.Writer, events []localworker.AA25Event, summary Summary) error {
	encoder := json.NewEncoder(writer)
	for _, event := range events {
		if err := encoder.Encode(Record(event)); err != nil {
			return err
		}
	}
	return encoder.Encode(Seal(summary))
}
