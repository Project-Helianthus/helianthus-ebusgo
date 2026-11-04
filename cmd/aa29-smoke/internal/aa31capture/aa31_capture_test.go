package aa31capture

import (
	aa31bytes "bytes"
	aa31sha256 "crypto/sha256"
	aa31hex "encoding/hex"
	aa31json "encoding/json"
	aa31errors "errors"
	aa31ast "go/ast"
	aa31parser "go/parser"
	aa31token "go/token"
	aa31strings "strings"
	aa31atomic "sync/atomic"
	aa31testing "testing"
	aa31time "time"

	aa31localworker "github.com/Project-Helianthus/helianthus-ebusgo/localworker"
	aa31transport "github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// Every profile, digest and event below is synthetic. The digest is computed
// from a fixed label, never copied from a qualification record.
func aa31TestDigestText() string {
	aa31Sum := aa31sha256.Sum256([]byte("aa31.synthetic.digest"))
	return aa31hex.EncodeToString(aa31Sum[:])
}

func aa31TestDocument() AA31ProfileDocument {
	return AA31ProfileDocument{
		AA31ProfileID:           "aa31-synthetic-profile",
		AA31Family:              "aa31-synthetic-family",
		AA31Qualification:       "proven",
		AA31QualificationDigest: aa31TestDigestText(),
		AA31GPIO: AA31GPIODocument{
			AA31MapLength:     0x40,
			AA31AccessWidth:   4,
			AA31Ordering:      "device",
			AA31RXLevelOffset: 0x10,
			AA31TXSetOffset:   0x20,
			AA31TXClearOffset: 0x30,
			AA31RXMask:        1 << 3,
			AA31TXMask:        1 << 5,
		},
		AA31Counter:       AA31CounterDocument{AA31Hz: AA31MonotonicHz},
		AA31MaxLeaseTicks: 5_000,
	}
}

func aa31Encode(aa31T *aa31testing.T, aa31Value any) []byte {
	aa31T.Helper()
	aa31Source, aa31Err := aa31json.Marshal(aa31Value)
	if aa31Err != nil {
		aa31T.Fatalf("aa31 marshal: %v", aa31Err)
	}
	return aa31Source
}

// Catches, per field, a JSON tag or decode path that drops or swaps it:
// profile_id, family, qualification, qualification_digest, map_length,
// access_width, ordering, rx_level_offset, tx_set_offset, tx_clear_offset,
// rx_mask, tx_mask, rx_inverted, tx_inverted, counter hz and max_lease_ticks.
func TestAA31DecodeProfileKeepsEveryField(aa31T *aa31testing.T) {
	aa31OtherSum := aa31sha256.Sum256([]byte("aa31.other.digest"))
	aa31Mutations := map[string]func(*AA31ProfileDocument){
		"profile_id":    func(aa31D *AA31ProfileDocument) { aa31D.AA31ProfileID = "aa31-other-profile" },
		"family":        func(aa31D *AA31ProfileDocument) { aa31D.AA31Family = "aa31-other-family" },
		"qualification": func(aa31D *AA31ProfileDocument) { aa31D.AA31Qualification = "hypothesis" },
		"qualification_digest": func(aa31D *AA31ProfileDocument) {
			aa31D.AA31QualificationDigest = aa31hex.EncodeToString(aa31OtherSum[:])
		},
		"map_length":      func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31MapLength = 0x80 },
		"access_width":    func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31AccessWidth = 8 },
		"ordering":        func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31Ordering = "relaxed" },
		"rx_level_offset": func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31RXLevelOffset = 0x14 },
		"tx_set_offset":   func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31TXSetOffset = 0x24 },
		"tx_clear_offset": func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31TXClearOffset = 0x34 },
		"rx_mask":         func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31RXMask = 1 << 4 },
		"tx_mask":         func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31TXMask = 1 << 6 },
		"rx_inverted":     func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31RXInverted = true },
		"tx_inverted":     func(aa31D *AA31ProfileDocument) { aa31D.AA31GPIO.AA31TXInverted = true },
		"counter_hz":      func(aa31D *AA31ProfileDocument) { aa31D.AA31Counter.AA31Hz = 1_000_000 },
		"max_lease_ticks": func(aa31D *AA31ProfileDocument) { aa31D.AA31MaxLeaseTicks = 6_000 },
	}
	aa31Base := aa31TestDocument()
	aa31Decoded, aa31Err := AA31DecodeProfile(aa31bytes.NewReader(aa31Encode(aa31T, aa31Base)))
	if aa31Err != nil || aa31Decoded != aa31Base {
		aa31T.Fatalf("aa31 base decode = %+v, %v", aa31Decoded, aa31Err)
	}
	for aa31Name, aa31Mutate := range aa31Mutations {
		aa31Want := aa31TestDocument()
		aa31Mutate(&aa31Want)
		if aa31Want == aa31Base {
			aa31T.Fatalf("aa31 mutation %s changed nothing", aa31Name)
		}
		aa31Got, aa31Err := AA31DecodeProfile(aa31bytes.NewReader(aa31Encode(aa31T, aa31Want)))
		if aa31Err != nil || aa31Got != aa31Want {
			aa31T.Fatalf("aa31 decode with %s = %+v, %v; want %+v", aa31Name, aa31Got, aa31Err, aa31Want)
		}
	}
}

// Catches: accepting a document shape the owner did not write, so a typo or
// a second document silently falls back to zero values.
func TestAA31DecodeProfileRejectsMalformedDocuments(aa31T *aa31testing.T) {
	aa31Valid := string(aa31Encode(aa31T, aa31TestDocument()))
	aa31WithField := func(aa31Field, aa31Value string) string {
		aa31Document := aa31TestDocument()
		aa31Source := aa31Encode(aa31T, aa31Document)
		var aa31Generic map[string]any
		if aa31Err := aa31json.Unmarshal(aa31Source, &aa31Generic); aa31Err != nil {
			aa31T.Fatalf("aa31 unmarshal: %v", aa31Err)
		}
		aa31Generic[aa31Field] = aa31Value
		return string(aa31Encode(aa31T, aa31Generic))
	}
	aa31Cases := map[string]string{
		"empty":                 "",
		"not_json":              "aa31",
		"unknown_field":         aa31WithField("physical_address", "0"),
		"trailing_document":     aa31Valid + aa31Valid,
		"trailing_garbage":      aa31Valid + " x",
		"qualification_label":   aa31WithField("qualification", "Proven"),
		"qualification_empty":   aa31WithField("qualification", ""),
		"digest_short":          aa31WithField("qualification_digest", aa31TestDigestText()[2:]),
		"digest_long":           aa31WithField("qualification_digest", aa31TestDigestText()+"00"),
		"digest_not_hex":        aa31WithField("qualification_digest", "zz"+aa31TestDigestText()[2:]),
		"oversized":             aa31Valid + aa31strings.Repeat(" ", AA31MaxProfileBytes),
		"gpio_wrong_type":       aa31WithField("gpio", "device"),
		"ordering_unknown_word": aa31strings.Replace(aa31Valid, `"ordering":"device"`, `"ordering":"uncached"`, 1),
		"ordering_empty":        aa31strings.Replace(aa31Valid, `"ordering":"device"`, `"ordering":""`, 1),
	}
	for aa31Name, aa31Source := range aa31Cases {
		if _, aa31Err := AA31DecodeProfile(aa31strings.NewReader(aa31Source)); !aa31errors.Is(aa31Err, AA31ErrProfile) {
			aa31T.Fatalf("aa31 %s decode = %v, want AA31ErrProfile", aa31Name, aa31Err)
		}
	}
	if _, aa31Err := AA31DecodeProfile(aa31strings.NewReader(aa31Valid + "\n\t ")); aa31Err != nil {
		aa31T.Fatalf("aa31 trailing whitespace = %v", aa31Err)
	}
}

// Catches: a qualification label mapping onto the wrong evidence level, which
// would let a hypothesis profile reach admission as proven.
func TestAA31ParseQualificationMapsOnlyProvenAffirmatively(aa31T *aa31testing.T) {
	aa31Cases := map[string]aa31transport.AA22Evidence{
		"unknown":    aa31transport.AA22Unknown,
		"hypothesis": aa31transport.AA22Hypothesis,
		"proven":     aa31transport.AA22Proven,
	}
	for aa31Label, aa31Want := range aa31Cases {
		if aa31Got, aa31Err := AA31ParseQualification(aa31Label); aa31Err != nil || aa31Got != aa31Want {
			aa31T.Fatalf("aa31 qualification %q = %v, %v", aa31Label, aa31Got, aa31Err)
		}
	}
	for _, aa31Label := range []string{"", "PROVEN", "qualified", "proven "} {
		if aa31Got, aa31Err := AA31ParseQualification(aa31Label); !aa31errors.Is(aa31Err, AA31ErrProfile) || aa31Got == aa31transport.AA22Proven {
			aa31T.Fatalf("aa31 qualification %q = %v, %v", aa31Label, aa31Got, aa31Err)
		}
	}
}

// Catches: a digest parser that truncates, pads or ignores case differently
// from the hex it is given.
func TestAA31ParseDigestRoundTrips(aa31T *aa31testing.T) {
	aa31Sum := aa31sha256.Sum256([]byte("aa31.synthetic.digest"))
	for _, aa31Text := range []string{aa31TestDigestText(), aa31strings.ToUpper(aa31TestDigestText())} {
		if aa31Got, aa31Err := AA31ParseDigest(aa31Text); aa31Err != nil || aa31Got != aa31Sum {
			aa31T.Fatalf("aa31 digest %q = %x, %v", aa31Text, aa31Got, aa31Err)
		}
	}
}

// Catches: an overflowing or truncating duration conversion and an unbounded
// or zero-length capture budget.
func TestAA31BudgetTicksIsBoundedAndExact(aa31T *aa31testing.T) {
	aa31Cases := []struct {
		aa31Duration aa31time.Duration
		aa31Hz       uint64
		aa31Want     uint64
	}{
		{aa31time.Second, AA31MonotonicHz, AA31MonotonicHz},
		{50 * aa31time.Millisecond, 1_000_000, 50_000},
		{AA31MaxDuration, AA31MonotonicHz, uint64(AA31MaxDuration)},
		{AA31MaxDuration, 1 << 40, 3600 << 40},
		{aa31time.Nanosecond, AA31MonotonicHz, 1},
	}
	for _, aa31Case := range aa31Cases {
		if aa31Got, aa31Err := AA31BudgetTicks(aa31Case.aa31Duration, aa31Case.aa31Hz); aa31Err != nil || aa31Got != aa31Case.aa31Want {
			aa31T.Fatalf("aa31 budget %v at %d Hz = %d, %v; want %d", aa31Case.aa31Duration, aa31Case.aa31Hz, aa31Got, aa31Err, aa31Case.aa31Want)
		}
	}
	aa31Refused := []struct {
		aa31Duration aa31time.Duration
		aa31Hz       uint64
	}{
		{0, AA31MonotonicHz},
		{-aa31time.Second, AA31MonotonicHz},
		{AA31MaxDuration + 1, AA31MonotonicHz},
		{aa31time.Second, 0},
		{aa31time.Nanosecond, 1_000_000},
		{AA31MaxDuration, ^uint64(0)},
	}
	for _, aa31Case := range aa31Refused {
		if _, aa31Err := AA31BudgetTicks(aa31Case.aa31Duration, aa31Case.aa31Hz); !aa31errors.Is(aa31Err, AA31ErrLimits) {
			aa31T.Fatalf("aa31 budget %v at %d Hz = %v, want AA31ErrLimits", aa31Case.aa31Duration, aa31Case.aa31Hz, aa31Err)
		}
	}
}

// Catches: a recorder that grows past its capacity, blocks, or loses count of
// refused events.
func TestAA31RecorderIsBoundedAndCountsRefusals(aa31T *aa31testing.T) {
	for _, aa31Capacity := range []int{0, -1, AA31MaxEvents + 1} {
		if _, aa31Err := AA31NewRecorder(aa31Capacity); !aa31errors.Is(aa31Err, AA31ErrLimits) {
			aa31T.Fatalf("aa31 recorder capacity %d = %v", aa31Capacity, aa31Err)
		}
	}
	aa31Recorder, aa31Err := AA31NewRecorder(2)
	if aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	for aa31Index := uint64(1); aa31Index <= 5; aa31Index++ {
		aa31Accepted := aa31Recorder.AA25TryPublish(aa31localworker.AA25Event{AA25Kind: aa31localworker.AA25EventRawSymbol, AA25Tick: aa31Index})
		if aa31Accepted != (aa31Index <= 2) {
			aa31T.Fatalf("aa31 publish %d accepted=%v", aa31Index, aa31Accepted)
		}
	}
	aa31Events := aa31Recorder.AA31Events()
	if !aa31Recorder.AA31Full() || len(aa31Events) != 2 || cap(aa31Events) != 2 || aa31Events[0].AA25Tick != 1 || aa31Events[1].AA25Tick != 2 || aa31Recorder.AA31Refused() != 3 {
		aa31T.Fatalf("aa31 recorder = %+v refused %d", aa31Events, aa31Recorder.AA31Refused())
	}
}

type aa31FakeTicks struct {
	aa31Values []uint64
	aa31Reads  int
}

func (aa31Ticks *aa31FakeTicks) AA25NowTicks() uint64 {
	aa31Value := aa31Ticks.aa31Values[len(aa31Ticks.aa31Values)-1]
	if aa31Ticks.aa31Reads < len(aa31Ticks.aa31Values) {
		aa31Value = aa31Ticks.aa31Values[aa31Ticks.aa31Reads]
	}
	aa31Ticks.aa31Reads++
	return aa31Value
}

type aa31FakeStepper struct {
	aa31Steps    int
	aa31Recorder *AA31Recorder
	aa31Publish  bool
	aa31OnStep   func()
}

func (aa31Stepper *aa31FakeStepper) AA25Step() {
	aa31Stepper.aa31Steps++
	if aa31Stepper.aa31Publish {
		aa31Stepper.aa31Recorder.AA25TryPublish(aa31localworker.AA25Event{AA25Kind: aa31localworker.AA25EventGap})
	}
	if aa31Stepper.aa31OnStep != nil {
		aa31Stepper.aa31OnStep()
	}
}

// Catches: a loop that ignores any one stop condition, overruns its budget, or
// reports wall time instead of counter ticks.
func TestAA31RunStopsOnEveryBound(aa31T *aa31testing.T) {
	aa31NewRecorder := func(aa31Capacity int) *AA31Recorder {
		aa31Recorder, aa31Err := AA31NewRecorder(aa31Capacity)
		if aa31Err != nil {
			aa31T.Fatal(aa31Err)
		}
		return aa31Recorder
	}

	aa31Recorder := aa31NewRecorder(16)
	aa31Ticks := &aa31FakeTicks{aa31Values: []uint64{100, 140, 180, 199, 200, 900}}
	aa31Result := AA31Run(&aa31FakeStepper{}, aa31Ticks, aa31Recorder, 100, nil)
	if aa31Result != (AA31Result{AA31Steps: 4, AA31ElapsedTicks: 100, AA31Stop: AA31StopBudget}) {
		aa31T.Fatalf("aa31 budget run = %+v", aa31Result)
	}

	aa31Recorder = aa31NewRecorder(3)
	aa31Result = AA31Run(&aa31FakeStepper{aa31Recorder: aa31Recorder, aa31Publish: true},
		&aa31FakeTicks{aa31Values: []uint64{10, 11, 12, 13}}, aa31Recorder, 1_000, nil)
	if aa31Result != (AA31Result{AA31Steps: 3, AA31ElapsedTicks: 3, AA31Stop: AA31StopRecorderFull}) {
		aa31T.Fatalf("aa31 full run = %+v", aa31Result)
	}

	var aa31Stop aa31atomic.Bool
	aa31Stepper := &aa31FakeStepper{}
	aa31Stepper.aa31OnStep = func() {
		if aa31Stepper.aa31Steps == 2 {
			aa31Stop.Store(true)
		}
	}
	aa31Result = AA31Run(aa31Stepper, &aa31FakeTicks{aa31Values: []uint64{5, 6, 7}}, aa31NewRecorder(4), 1_000, &aa31Stop)
	if aa31Result != (AA31Result{AA31Steps: 2, AA31ElapsedTicks: 2, AA31Stop: AA31StopRequested}) {
		aa31T.Fatalf("aa31 requested run = %+v", aa31Result)
	}
	aa31Stopped := &aa31FakeStepper{}
	if aa31Result = AA31Run(aa31Stopped, &aa31FakeTicks{aa31Values: []uint64{5}}, aa31NewRecorder(4), 1_000, &aa31Stop); aa31Result.AA31Stop != AA31StopRequested || aa31Stopped.aa31Steps != 0 {
		aa31T.Fatalf("aa31 pre-stopped run = %+v after %d steps", aa31Result, aa31Stopped.aa31Steps)
	}

	aa31Result = AA31Run(&aa31FakeStepper{}, &aa31FakeTicks{aa31Values: []uint64{50, 60, 40}}, aa31NewRecorder(4), 1_000, nil)
	if aa31Result != (AA31Result{AA31Steps: 2, AA31ElapsedTicks: 10, AA31Stop: AA31StopTickRegression}) {
		aa31T.Fatalf("aa31 regression run = %+v", aa31Result)
	}

	// A reading below the previous one stops the run even when it stays above the start.
	aa31Result = AA31Run(&aa31FakeStepper{}, &aa31FakeTicks{aa31Values: []uint64{100, 140, 120, 200}}, aa31NewRecorder(4), 1_000, nil)
	if aa31Result != (AA31Result{AA31Steps: 2, AA31ElapsedTicks: 40, AA31Stop: AA31StopTickRegression}) {
		aa31T.Fatalf("aa31 regression above start run = %+v", aa31Result)
	}

	aa31Names := map[string]bool{}
	for _, aa31Reason := range []AA31StopReason{AA31StopNone, AA31StopBudget, AA31StopRecorderFull, AA31StopRequested, AA31StopTickRegression} {
		aa31Names[AA31StopName(aa31Reason)] = true
	}
	if len(aa31Names) != 5 {
		aa31T.Fatalf("aa31 stop names collide: %v", aa31Names)
	}
}

// Catches: two event kinds or two faults sharing a report label, or a known
// value falling through to the numeric fallback.
func TestAA31KindAndFaultNamesAreDistinct(aa31T *aa31testing.T) {
	aa31Kinds := map[string]bool{}
	for aa31Kind := aa31localworker.AA25EventRawSymbol; aa31Kind <= aa31localworker.AA25EventLoss; aa31Kind++ {
		aa31Name := AA31KindName(aa31Kind)
		if aa31strings.HasPrefix(aa31Name, "kind_") || aa31Kinds[aa31Name] {
			aa31T.Fatalf("aa31 kind %d label %q", aa31Kind, aa31Name)
		}
		aa31Kinds[aa31Name] = true
	}
	if AA31KindName(aa31localworker.AA25EventLoss+1) != "kind_12" || AA31KindName(0) != "kind_0" {
		aa31T.Fatal("aa31 unknown kinds must keep their number")
	}
	aa31Faults := map[string]bool{}
	for aa31Fault := aa31localworker.AA25FaultNone; aa31Fault <= aa31localworker.AA25FaultNackExhausted; aa31Fault++ {
		aa31Name := AA31FaultName(aa31Fault)
		if aa31strings.HasPrefix(aa31Name, "fault_") || aa31Faults[aa31Name] || (aa31Name == "") != (aa31Fault == aa31localworker.AA25FaultNone) {
			aa31T.Fatalf("aa31 fault %d label %q", aa31Fault, aa31Name)
		}
		aa31Faults[aa31Name] = true
	}
	if AA31FaultName(aa31localworker.AA25FaultNackExhausted+1) != "fault_16" {
		aa31T.Fatal("aa31 unknown faults must keep their number")
	}
}

func aa31TestEvent() aa31localworker.AA25Event {
	return aa31localworker.AA25Event{
		AA25Kind:        aa31localworker.AA25EventFault,
		AA25Fault:       aa31localworker.AA25FaultBadCRC,
		AA25Tick:        11,
		AA25SampleIndex: 12,
		AA25StartTick:   13,
		AA25Deadline:    14,
		AA25Missed:      15,
		AA25Generation:  16,
		AA25Raw:         0xA9,
		AA25Logical:     0xAA,
		AA25WasEscaped:  true,
		AA25Echo:        true,
		AA25Level:       aa31localworker.AA25LevelHigh,
		AA25Dissent:     3,
		AA25EdgePhase:   [10]uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	}
}

// Catches, per field, a record conversion that drops, swaps or reuses one
// event field: kind, fault, tick, sample index, start tick, deadline, missed,
// generation, raw, logical, was-escaped, echo, level, dissent and edge phase.
func TestAA31RecordKeepsEveryEventField(aa31T *aa31testing.T) {
	aa31Mutations := map[string]func(*aa31localworker.AA25Event){
		"kind":         func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Kind = aa31localworker.AA25EventRawSymbol },
		"fault":        func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Fault = aa31localworker.AA25FaultBadStop },
		"tick":         func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Tick++ },
		"sample_index": func(aa31E *aa31localworker.AA25Event) { aa31E.AA25SampleIndex++ },
		"start_tick":   func(aa31E *aa31localworker.AA25Event) { aa31E.AA25StartTick++ },
		"deadline":     func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Deadline++ },
		"missed":       func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Missed++ },
		"generation":   func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Generation++ },
		"raw":          func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Raw++ },
		"logical":      func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Logical++ },
		"was_escaped":  func(aa31E *aa31localworker.AA25Event) { aa31E.AA25WasEscaped = false },
		"echo":         func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Echo = false },
		"level":        func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Level = aa31localworker.AA25LevelLow },
		"dissent":      func(aa31E *aa31localworker.AA25Event) { aa31E.AA25Dissent++ },
		"edge_phase":   func(aa31E *aa31localworker.AA25Event) { aa31E.AA25EdgePhase[9]++ },
	}
	aa31Base := AA31Record(aa31TestEvent())
	aa31Want := AA31EventRecord{
		AA31Record: "event", AA31Kind: "fault", AA31Fault: "bad_crc", AA31Tick: 11, AA31SampleIndex: 12,
		AA31StartTick: 13, AA31Deadline: 14, AA31Missed: 15, AA31Generation: 16, AA31Raw: 0xA9,
		AA31Logical: 0xAA, AA31WasEscaped: true, AA31Echo: true, AA31Level: 1, AA31Dissent: 3,
		AA31EdgePhase: [10]uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	}
	if aa31Base != aa31Want {
		aa31T.Fatalf("aa31 record = %+v, want %+v", aa31Base, aa31Want)
	}
	for aa31Name, aa31Mutate := range aa31Mutations {
		aa31Event := aa31TestEvent()
		aa31Mutate(&aa31Event)
		if AA31Record(aa31Event) == aa31Base {
			aa31T.Fatalf("aa31 record ignores %s", aa31Name)
		}
	}
}

// Catches, per counter, a copy that drops or swaps one engine counter.
func TestAA31EngineCountersKeepEveryField(aa31T *aa31testing.T) {
	aa31Got := AA31EngineCountersFrom(aa31localworker.AA25Stats{
		AA25Observations: 1, AA25MissedSamples: 2, AA25MaxObservationLate: 3, AA25TXEdges: 4,
		AA25EchoedSymbols: 5, AA25EventsDropped: 6, AA25ControlsApplied: 7, AA25Withdrawals: 8,
	})
	aa31Want := AA31EngineCounters{
		AA31Observations: 1, AA31MissedSamples: 2, AA31MaxObservationLate: 3, AA31TXEdges: 4,
		AA31EchoedSymbols: 5, AA31EventsDropped: 6, AA31ControlsApplied: 7, AA31Withdrawals: 8,
	}
	if aa31Got != aa31Want {
		aa31T.Fatalf("aa31 engine counters = %+v", aa31Got)
	}
}

// Catches: a passive verdict that ignores any one write indicator, or a caller
// that could assert passivity or evidence levels itself.
func TestAA31SealDerivesPassiveFromEveryWriteIndicator(aa31T *aa31testing.T) {
	aa31Clean := AA31Seal(AA31Summary{AA31Passive: false, AA31TimingEvidence: "proven", AA31Engine: AA31EngineCounters{AA31Observations: 9}})
	if !aa31Clean.AA31Passive || aa31Clean.AA31Record != "summary" || aa31Clean.AA31TimingEvidence != "unknown" || aa31Clean.AA31CaptureEvidence != "hypothesis" {
		aa31T.Fatalf("aa31 clean seal = %+v", aa31Clean)
	}
	aa31Dirty := map[string]func(*AA31Summary){
		"engine_tx_edges":        func(aa31S *AA31Summary) { aa31S.AA31Engine.AA31TXEdges = 1 },
		"backend_stored_edges":   func(aa31S *AA31Summary) { aa31S.AA31Backend.AA31StoredEdges = 1 },
		"backend_refused_edges":  func(aa31S *AA31Summary) { aa31S.AA31Backend.AA31RefusedEdges = 1 },
		"backend_releases":       func(aa31S *AA31Summary) { aa31S.AA31Backend.AA31Releases = 1 },
		"backend_lease_expiries": func(aa31S *AA31Summary) { aa31S.AA31Backend.AA31LeaseExpiries = 1 },
		"window_store_attempts":  func(aa31S *AA31Summary) { aa31S.AA31WindowStores = 1 },
	}
	for aa31Name, aa31Mutate := range aa31Dirty {
		aa31Summary := AA31Summary{AA31Passive: true}
		aa31Mutate(&aa31Summary)
		if AA31Seal(aa31Summary).AA31Passive {
			aa31T.Fatalf("aa31 seal ignores %s", aa31Name)
		}
	}
}

// Catches: a report that reorders events, omits the summary or emits more
// than one JSON value per line.
func TestAA31WriteReportEmitsEventsThenSummary(aa31T *aa31testing.T) {
	aa31First := aa31TestEvent()
	aa31Second := aa31TestEvent()
	aa31Second.AA25Kind = aa31localworker.AA25EventRawSymbol
	aa31Second.AA25Tick = 99
	var aa31Out aa31bytes.Buffer
	if aa31Err := AA31WriteReport(&aa31Out, []aa31localworker.AA25Event{aa31First, aa31Second}, AA31Summary{AA31ProfileID: "aa31-synthetic-profile"}); aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	aa31Lines := aa31strings.Split(aa31strings.TrimSuffix(aa31Out.String(), "\n"), "\n")
	if len(aa31Lines) != 3 {
		aa31T.Fatalf("aa31 report lines = %q", aa31Lines)
	}
	var aa31Event AA31EventRecord
	if aa31Err := aa31json.Unmarshal([]byte(aa31Lines[1]), &aa31Event); aa31Err != nil || aa31Event != AA31Record(aa31Second) {
		aa31T.Fatalf("aa31 second event = %+v, %v", aa31Event, aa31Err)
	}
	var aa31Summary AA31Summary
	if aa31Err := aa31json.Unmarshal([]byte(aa31Lines[2]), &aa31Summary); aa31Err != nil || aa31Summary.AA31Record != "summary" || !aa31Summary.AA31Passive || aa31Summary.AA31ProfileID != "aa31-synthetic-profile" {
		aa31T.Fatalf("aa31 summary = %+v, %v", aa31Summary, aa31Err)
	}
}

// Catches: a counter port that reports a rate it does not tick at or that can
// run backwards.
func TestAA31MonotonicCounterIsNanosecondsAndMonotonic(aa31T *aa31testing.T) {
	aa31Counter := AA31NewMonotonicCounter()
	if aa31Counter.AA29CounterHz() != AA31MonotonicHz || AA31MonotonicHz != uint64(aa31time.Second) {
		aa31T.Fatalf("aa31 counter rate = %d", aa31Counter.AA29CounterHz())
	}
	aa31Previous := aa31Counter.AA29ReadCounter()
	for aa31Index := 0; aa31Index < 1_000; aa31Index++ {
		aa31Now := aa31Counter.AA29ReadCounter()
		if aa31Now < aa31Previous {
			aa31T.Fatalf("aa31 counter regressed %d -> %d", aa31Previous, aa31Now)
		}
		aa31Previous = aa31Now
	}
	aa31Before := aa31Counter.AA29ReadCounter()
	aa31time.Sleep(2 * aa31time.Millisecond)
	if aa31Elapsed := aa31Counter.AA29ReadCounter() - aa31Before; aa31Elapsed < uint64(2*aa31time.Millisecond) {
		aa31T.Fatalf("aa31 counter advanced %d ticks across 2ms", aa31Elapsed)
	}
}

func aa31CheckPrefix(aa31T *aa31testing.T, aa31Name string) {
	aa31T.Helper()
	if aa31Name == "_" || aa31strings.HasPrefix(aa31Name, "aa31") || aa31strings.HasPrefix(aa31Name, "AA31") || aa31strings.HasPrefix(aa31Name, "TestAA31") {
		return
	}
	aa31T.Fatalf("aa31 identifier %q lacks the AA31 prefix", aa31Name)
}

// aa31CheckSourcePrefixes requires the AA31 prefix on every import alias and
// declared identifier except the listed port methods.
func aa31CheckSourcePrefixes(aa31T *aa31testing.T, aa31Paths []string, aa31Ported map[string]bool) {
	aa31T.Helper()
	for _, aa31Path := range aa31Paths {
		aa31File, aa31Err := aa31parser.ParseFile(aa31token.NewFileSet(), aa31Path, nil, aa31parser.SkipObjectResolution)
		if aa31Err != nil {
			aa31T.Fatalf("aa31 parse %s: %v", aa31Path, aa31Err)
		}
		for _, aa31Import := range aa31File.Imports {
			if aa31Import.Name == nil {
				aa31T.Fatalf("aa31 import %s has no prefixed alias", aa31Import.Path.Value)
			}
			aa31CheckPrefix(aa31T, aa31Import.Name.Name)
		}
		aa31ast.Inspect(aa31File, func(aa31Node aa31ast.Node) bool {
			switch aa31Value := aa31Node.(type) {
			case *aa31ast.FuncDecl:
				if !aa31Ported[aa31Value.Name.Name] {
					aa31CheckPrefix(aa31T, aa31Value.Name.Name)
				}
			case *aa31ast.TypeSpec:
				aa31CheckPrefix(aa31T, aa31Value.Name.Name)
			case *aa31ast.ValueSpec:
				for _, aa31Name := range aa31Value.Names {
					aa31CheckPrefix(aa31T, aa31Name.Name)
				}
			case *aa31ast.Field:
				for _, aa31Name := range aa31Value.Names {
					if !aa31Ported[aa31Name.Name] {
						aa31CheckPrefix(aa31T, aa31Name.Name)
					}
				}
			case *aa31ast.AssignStmt:
				if aa31Value.Tok == aa31token.DEFINE {
					for _, aa31Left := range aa31Value.Lhs {
						if aa31Ident, aa31OK := aa31Left.(*aa31ast.Ident); aa31OK {
							aa31CheckPrefix(aa31T, aa31Ident.Name)
						}
					}
				}
			case *aa31ast.RangeStmt:
				if aa31Value.Tok == aa31token.DEFINE {
					for _, aa31Left := range []aa31ast.Expr{aa31Value.Key, aa31Value.Value} {
						if aa31Ident, aa31OK := aa31Left.(*aa31ast.Ident); aa31OK {
							aa31CheckPrefix(aa31T, aa31Ident.Name)
						}
					}
				}
			}
			return true
		})
	}
}

// Method names that satisfy or restate the AA25 and AA29 port interfaces carry
// those tickets' prefixes by contract and are the only exemptions.
func TestAA31EveryDeclaredIdentifierIsPrefixed(aa31T *aa31testing.T) {
	aa31CheckSourcePrefixes(aa31T, []string{
		"aa31_capture.go", "aa31_capture_test.go", "aa31_mapping_unix.go", "aa31_mapping_unix_test.go",
	}, map[string]bool{
		"AA25TryPublish":  true,
		"AA25NowTicks":    true,
		"AA25Step":        true,
		"AA29CounterHz":   true,
		"AA29ReadCounter": true,
	})
}
