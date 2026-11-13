// Package capture holds the platform-neutral parts of the passive-capture-smoke
// passive-capture command: the owner-provisioned profile document, the
// monotonic counter port, the bounded event recorder, the capture loop and the
// line-oriented report. It opens no device and drives no line.
//
// Status: Hypothesis. A capture records what the wire engine decoded from the
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
	ErrProfile = errors.New("passive-capture-smoke: profile document invalid")
	ErrLimits  = errors.New("passive-capture-smoke: capture limits invalid")
)

// GPIODocument mirrors the local GPIO descriptor. It carries offsets inside
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

// CounterDocument mirrors the local GPIO counter descriptor.
type CounterDocument struct {
	Hz uint64 `json:"hz"`
}

// ProfileDocument is the owner-provisioned local GPIO platform profile in JSON
// form. Decoding checks only the document shape; admission, qualification and
// descriptor validity stay with the local GPIO backend.
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

// KnownOrdering reports whether a document ordering label is one the local GPIO
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

// CounterHz reports the counter rate.
func (counter *MonotonicCounter) CounterHz() uint64 {
	return MonotonicHz
}

// ReadCounter reads elapsed monotonic nanoseconds since the anchor.
func (counter *MonotonicCounter) ReadCounter() uint64 {
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

// Recorder is a fixed-capacity wire engine event sink. It allocates only at
// construction; a full recorder refuses and counts every further event.
type Recorder struct {
	events  []localworker.Event
	refused uint64
}

// NewRecorder allocates a recorder for 1..MaxEvents events.
func NewRecorder(capacity int) (*Recorder, error) {
	if capacity <= 0 || capacity > MaxEvents {
		return nil, ErrLimits
	}
	return &Recorder{events: make([]localworker.Event, 0, capacity)}, nil
}

// TryPublish records one event without blocking.
func (recorder *Recorder) TryPublish(event localworker.Event) bool {
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
func (recorder *Recorder) Events() []localworker.Event {
	return recorder.events
}

// Refused returns the number of refused events.
func (recorder *Recorder) Refused() uint64 {
	return recorder.refused
}

// Stepper is the part of the wire engine the capture loop drives.
type Stepper interface {
	Step()
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
func Run(engine Stepper, ticks localworker.TickPort, recorder *Recorder, budgetTicks uint64, stop *atomic.Bool) Result {
	var result Result
	start := ticks.NowTicks()
	prev := start
	for {
		if stop != nil && stop.Load() {
			result.Stop = StopRequested
			return result
		}
		engine.Step()
		result.Steps++
		now := ticks.NowTicks()
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
func KindName(kind localworker.EventKind) string {
	switch kind {
	case localworker.EventRawSymbol:
		return "raw_symbol"
	case localworker.EventLogicalByte:
		return "logical_byte"
	case localworker.EventGap:
		return "gap"
	case localworker.EventFault:
		return "fault"
	case localworker.EventProvisional:
		return "provisional"
	case localworker.EventFrameValid:
		return "frame_valid"
	case localworker.EventAdmitted:
		return "admitted"
	case localworker.EventTXIntent:
		return "tx_intent"
	case localworker.EventTXRelease:
		return "tx_release"
	case localworker.EventTXComplete:
		return "tx_complete"
	case localworker.EventLoss:
		return "loss"
	}
	return fmt.Sprintf("kind_%d", kind)
}

// FaultName returns the report label for an engine fault.
func FaultName(fault localworker.Fault) string {
	switch fault {
	case localworker.FaultNone:
		return ""
	case localworker.FaultFalseStart:
		return "false_start"
	case localworker.FaultBadStop:
		return "bad_stop"
	case localworker.FaultEscapeInvalid:
		return "escape_invalid"
	case localworker.FaultSynInFrame:
		return "syn_in_frame"
	case localworker.FaultAddressInvalid:
		return "address_invalid"
	case localworker.FaultBadCRC:
		return "bad_crc"
	case localworker.FaultTickRegression:
		return "tick_regression"
	case localworker.FaultStaleGeneration:
		return "stale_generation"
	case localworker.FaultRXBeforeResponse:
		return "rx_before_response"
	case localworker.FaultEchoMismatch:
		return "echo_mismatch"
	case localworker.FaultEchoMissing:
		return "echo_missing"
	case localworker.FaultEchoUnsolicited:
		return "echo_unsolicited"
	case localworker.FaultTXLate:
		return "tx_late"
	case localworker.FaultLengthInvalid:
		return "length_invalid"
	case localworker.FaultNackExhausted:
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
func Record(event localworker.Event) EventRecord {
	return EventRecord{
		Record:      "event",
		Kind:        KindName(event.Kind),
		Fault:       FaultName(event.Fault),
		Tick:        event.Tick,
		SampleIndex: event.SampleIndex,
		StartTick:   event.StartTick,
		Deadline:    event.Deadline,
		Missed:      event.Missed,
		Generation:  event.Generation,
		Raw:         event.Raw,
		Logical:     event.Logical,
		WasEscaped:  event.WasEscaped,
		Echo:        event.Echo,
		Level:       uint8(event.Level),
		Dissent:     event.Dissent,
		EdgePhase:   event.EdgePhase,
	}
}

// BackendCounters copies the local GPIO backend counters the report checks.
type BackendCounters struct {
	StoredEdges   uint64 `json:"stored_edges"`
	RefusedEdges  uint64 `json:"refused_edges"`
	Releases      uint64 `json:"releases"`
	LeaseExpiries uint64 `json:"lease_expiries"`
}

// EngineCounters copies the wire engine counters.
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

// EngineCountersFrom copies every wire engine counter.
func EngineCountersFrom(stats localworker.EngineStats) EngineCounters {
	return EngineCounters{
		Observations:       stats.Observations,
		MissedSamples:      stats.MissedSamples,
		MaxObservationLate: stats.MaxObservationLate,
		TXEdges:            stats.TXEdges,
		EchoedSymbols:      stats.EchoedSymbols,
		EventsDropped:      stats.EventsDropped,
		ControlsApplied:    stats.ControlsApplied,
		Withdrawals:        stats.Withdrawals,
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
func WriteReport(writer io.Writer, events []localworker.Event, summary Summary) error {
	encoder := json.NewEncoder(writer)
	for _, event := range events {
		if err := encoder.Encode(Record(event)); err != nil {
			return err
		}
	}
	return encoder.Encode(Seal(summary))
}
