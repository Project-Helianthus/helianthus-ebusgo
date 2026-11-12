package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/localworker"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// Every profile, digest and event below is synthetic. The digest is computed
// from a fixed label, never copied from a qualification record.
func testDigestText() string {
	sum := sha256.Sum256([]byte("aa31.synthetic.digest"))
	return hex.EncodeToString(sum[:])
}

func testDocument() ProfileDocument {
	return ProfileDocument{
		ProfileID:           "aa31-synthetic-profile",
		Family:              "aa31-synthetic-family",
		Qualification:       "proven",
		QualificationDigest: testDigestText(),
		GPIO: GPIODocument{
			MapLength:     0x40,
			AccessWidth:   4,
			Ordering:      "device",
			RXLevelOffset: 0x10,
			TXSetOffset:   0x20,
			TXClearOffset: 0x30,
			RXMask:        1 << 3,
			TXMask:        1 << 5,
		},
		Counter:       CounterDocument{Hz: MonotonicHz},
		MaxLeaseTicks: 5_000,
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	source, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("aa31 marshal: %v", err)
	}
	return source
}

// Catches, per field, a JSON tag or decode path that drops or swaps it:
// profile_id, family, qualification, qualification_digest, map_length,
// access_width, ordering, rx_level_offset, tx_set_offset, tx_clear_offset,
// rx_mask, tx_mask, rx_inverted, tx_inverted, counter hz and max_lease_ticks.
func TestAA31DecodeProfileKeepsEveryField(t *testing.T) {
	otherSum := sha256.Sum256([]byte("aa31.other.digest"))
	mutations := map[string]func(*ProfileDocument){
		"profile_id":    func(d *ProfileDocument) { d.ProfileID = "aa31-other-profile" },
		"family":        func(d *ProfileDocument) { d.Family = "aa31-other-family" },
		"qualification": func(d *ProfileDocument) { d.Qualification = "hypothesis" },
		"qualification_digest": func(d *ProfileDocument) {
			d.QualificationDigest = hex.EncodeToString(otherSum[:])
		},
		"map_length":      func(d *ProfileDocument) { d.GPIO.MapLength = 0x80 },
		"access_width":    func(d *ProfileDocument) { d.GPIO.AccessWidth = 8 },
		"ordering":        func(d *ProfileDocument) { d.GPIO.Ordering = "relaxed" },
		"rx_level_offset": func(d *ProfileDocument) { d.GPIO.RXLevelOffset = 0x14 },
		"tx_set_offset":   func(d *ProfileDocument) { d.GPIO.TXSetOffset = 0x24 },
		"tx_clear_offset": func(d *ProfileDocument) { d.GPIO.TXClearOffset = 0x34 },
		"rx_mask":         func(d *ProfileDocument) { d.GPIO.RXMask = 1 << 4 },
		"tx_mask":         func(d *ProfileDocument) { d.GPIO.TXMask = 1 << 6 },
		"rx_inverted":     func(d *ProfileDocument) { d.GPIO.RXInverted = true },
		"tx_inverted":     func(d *ProfileDocument) { d.GPIO.TXInverted = true },
		"counter_hz":      func(d *ProfileDocument) { d.Counter.Hz = 1_000_000 },
		"max_lease_ticks": func(d *ProfileDocument) { d.MaxLeaseTicks = 6_000 },
	}
	base := testDocument()
	decoded, err := DecodeProfile(bytes.NewReader(encode(t, base)))
	if err != nil || decoded != base {
		t.Fatalf("aa31 base decode = %+v, %v", decoded, err)
	}
	for name, mutate := range mutations {
		want := testDocument()
		mutate(&want)
		if want == base {
			t.Fatalf("aa31 mutation %s changed nothing", name)
		}
		got, err := DecodeProfile(bytes.NewReader(encode(t, want)))
		if err != nil || got != want {
			t.Fatalf("aa31 decode with %s = %+v, %v; want %+v", name, got, err, want)
		}
	}
}

// Catches: accepting a document shape the owner did not write, so a typo or
// a second document silently falls back to zero values.
func TestAA31DecodeProfileRejectsMalformedDocuments(t *testing.T) {
	valid := string(encode(t, testDocument()))
	withField := func(field, value string) string {
		document := testDocument()
		source := encode(t, document)
		var generic map[string]any
		if err := json.Unmarshal(source, &generic); err != nil {
			t.Fatalf("aa31 unmarshal: %v", err)
		}
		generic[field] = value
		return string(encode(t, generic))
	}
	cases := map[string]string{
		"empty":                 "",
		"not_json":              "aa31",
		"unknown_field":         withField("physical_address", "0"),
		"trailing_document":     valid + valid,
		"trailing_garbage":      valid + " x",
		"qualification_label":   withField("qualification", "Proven"),
		"qualification_empty":   withField("qualification", ""),
		"digest_short":          withField("qualification_digest", testDigestText()[2:]),
		"digest_long":           withField("qualification_digest", testDigestText()+"00"),
		"digest_not_hex":        withField("qualification_digest", "zz"+testDigestText()[2:]),
		"oversized":             valid + strings.Repeat(" ", MaxProfileBytes),
		"gpio_wrong_type":       withField("gpio", "device"),
		"ordering_unknown_word": strings.Replace(valid, `"ordering":"device"`, `"ordering":"uncached"`, 1),
		"ordering_empty":        strings.Replace(valid, `"ordering":"device"`, `"ordering":""`, 1),
	}
	for name, source := range cases {
		if _, err := DecodeProfile(strings.NewReader(source)); !errors.Is(err, ErrProfile) {
			t.Fatalf("aa31 %s decode = %v, want AA31ErrProfile", name, err)
		}
	}
	if _, err := DecodeProfile(strings.NewReader(valid + "\n\t ")); err != nil {
		t.Fatalf("aa31 trailing whitespace = %v", err)
	}
}

// Catches: a qualification label mapping onto the wrong evidence level, which
// would let a hypothesis profile reach admission as proven.
func TestAA31ParseQualificationMapsOnlyProvenAffirmatively(t *testing.T) {
	cases := map[string]transport.AA22Evidence{
		"unknown":    transport.AA22Unknown,
		"hypothesis": transport.AA22Hypothesis,
		"proven":     transport.AA22Proven,
	}
	for label, want := range cases {
		if got, err := ParseQualification(label); err != nil || got != want {
			t.Fatalf("aa31 qualification %q = %v, %v", label, got, err)
		}
	}
	for _, label := range []string{"", "PROVEN", "qualified", "proven "} {
		if got, err := ParseQualification(label); !errors.Is(err, ErrProfile) || got == transport.AA22Proven {
			t.Fatalf("aa31 qualification %q = %v, %v", label, got, err)
		}
	}
}

// Catches: a digest parser that truncates, pads or ignores case differently
// from the hex it is given.
func TestAA31ParseDigestRoundTrips(t *testing.T) {
	sum := sha256.Sum256([]byte("aa31.synthetic.digest"))
	for _, text := range []string{testDigestText(), strings.ToUpper(testDigestText())} {
		if got, err := ParseDigest(text); err != nil || got != sum {
			t.Fatalf("aa31 digest %q = %x, %v", text, got, err)
		}
	}
}

// Catches: an overflowing or truncating duration conversion and an unbounded
// or zero-length capture budget.
func TestAA31BudgetTicksIsBoundedAndExact(t *testing.T) {
	cases := []struct {
		duration time.Duration
		hz       uint64
		want     uint64
	}{
		{time.Second, MonotonicHz, MonotonicHz},
		{50 * time.Millisecond, 1_000_000, 50_000},
		{MaxDuration, MonotonicHz, uint64(MaxDuration)},
		{MaxDuration, 1 << 40, 3600 << 40},
		{time.Nanosecond, MonotonicHz, 1},
	}
	for _, tc := range cases {
		if got, err := BudgetTicks(tc.duration, tc.hz); err != nil || got != tc.want {
			t.Fatalf("aa31 budget %v at %d Hz = %d, %v; want %d", tc.duration, tc.hz, got, err, tc.want)
		}
	}
	refused := []struct {
		duration time.Duration
		hz       uint64
	}{
		{0, MonotonicHz},
		{-time.Second, MonotonicHz},
		{MaxDuration + 1, MonotonicHz},
		{time.Second, 0},
		{time.Nanosecond, 1_000_000},
		{MaxDuration, ^uint64(0)},
	}
	for _, tc := range refused {
		if _, err := BudgetTicks(tc.duration, tc.hz); !errors.Is(err, ErrLimits) {
			t.Fatalf("aa31 budget %v at %d Hz = %v, want AA31ErrLimits", tc.duration, tc.hz, err)
		}
	}
}

// Catches: a recorder that grows past its capacity, blocks, or loses count of
// refused events.
func TestAA31RecorderIsBoundedAndCountsRefusals(t *testing.T) {
	for _, capacity := range []int{0, -1, MaxEvents + 1} {
		if _, err := NewRecorder(capacity); !errors.Is(err, ErrLimits) {
			t.Fatalf("aa31 recorder capacity %d = %v", capacity, err)
		}
	}
	recorder, err := NewRecorder(2)
	if err != nil {
		t.Fatal(err)
	}
	for index := uint64(1); index <= 5; index++ {
		accepted := recorder.AA25TryPublish(localworker.AA25Event{AA25Kind: localworker.AA25EventRawSymbol, AA25Tick: index})
		if accepted != (index <= 2) {
			t.Fatalf("aa31 publish %d accepted=%v", index, accepted)
		}
	}
	events := recorder.Events()
	if !recorder.Full() || len(events) != 2 || cap(events) != 2 || events[0].AA25Tick != 1 || events[1].AA25Tick != 2 || recorder.Refused() != 3 {
		t.Fatalf("aa31 recorder = %+v refused %d", events, recorder.Refused())
	}
}

type fakeTicks struct {
	values []uint64
	reads  int
}

func (ticks *fakeTicks) AA25NowTicks() uint64 {
	value := ticks.values[len(ticks.values)-1]
	if ticks.reads < len(ticks.values) {
		value = ticks.values[ticks.reads]
	}
	ticks.reads++
	return value
}

type fakeStepper struct {
	steps    int
	recorder *Recorder
	publish  bool
	onStep   func()
}

func (stepper *fakeStepper) AA25Step() {
	stepper.steps++
	if stepper.publish {
		stepper.recorder.AA25TryPublish(localworker.AA25Event{AA25Kind: localworker.AA25EventGap})
	}
	if stepper.onStep != nil {
		stepper.onStep()
	}
}

// Catches: a loop that ignores any one stop condition, overruns its budget, or
// reports wall time instead of counter ticks.
func TestAA31RunStopsOnEveryBound(t *testing.T) {
	newRecorder := func(capacity int) *Recorder {
		recorder, err := NewRecorder(capacity)
		if err != nil {
			t.Fatal(err)
		}
		return recorder
	}

	recorder := newRecorder(16)
	ticks := &fakeTicks{values: []uint64{100, 140, 180, 199, 200, 900}}
	result := Run(&fakeStepper{}, ticks, recorder, 100, nil)
	if result != (Result{Steps: 4, ElapsedTicks: 100, Stop: StopBudget}) {
		t.Fatalf("aa31 budget run = %+v", result)
	}

	recorder = newRecorder(3)
	result = Run(&fakeStepper{recorder: recorder, publish: true},
		&fakeTicks{values: []uint64{10, 11, 12, 13}}, recorder, 1_000, nil)
	if result != (Result{Steps: 3, ElapsedTicks: 3, Stop: StopRecorderFull}) {
		t.Fatalf("aa31 full run = %+v", result)
	}

	var stop atomic.Bool
	stepper := &fakeStepper{}
	stepper.onStep = func() {
		if stepper.steps == 2 {
			stop.Store(true)
		}
	}
	result = Run(stepper, &fakeTicks{values: []uint64{5, 6, 7}}, newRecorder(4), 1_000, &stop)
	if result != (Result{Steps: 2, ElapsedTicks: 2, Stop: StopRequested}) {
		t.Fatalf("aa31 requested run = %+v", result)
	}
	stopped := &fakeStepper{}
	if result = Run(stopped, &fakeTicks{values: []uint64{5}}, newRecorder(4), 1_000, &stop); result.Stop != StopRequested || stopped.steps != 0 {
		t.Fatalf("aa31 pre-stopped run = %+v after %d steps", result, stopped.steps)
	}

	result = Run(&fakeStepper{}, &fakeTicks{values: []uint64{50, 60, 40}}, newRecorder(4), 1_000, nil)
	if result != (Result{Steps: 2, ElapsedTicks: 10, Stop: StopTickRegression}) {
		t.Fatalf("aa31 regression run = %+v", result)
	}

	// A reading below the previous one stops the run even when it stays above the start.
	result = Run(&fakeStepper{}, &fakeTicks{values: []uint64{100, 140, 120, 200}}, newRecorder(4), 1_000, nil)
	if result != (Result{Steps: 2, ElapsedTicks: 40, Stop: StopTickRegression}) {
		t.Fatalf("aa31 regression above start run = %+v", result)
	}

	names := map[string]bool{}
	for _, reason := range []StopReason{StopNone, StopBudget, StopRecorderFull, StopRequested, StopTickRegression} {
		names[StopName(reason)] = true
	}
	if len(names) != 5 {
		t.Fatalf("aa31 stop names collide: %v", names)
	}
}

// Catches: two event kinds or two faults sharing a report label, or a known
// value falling through to the numeric fallback.
func TestAA31KindAndFaultNamesAreDistinct(t *testing.T) {
	kinds := map[string]bool{}
	for kind := localworker.AA25EventRawSymbol; kind <= localworker.AA25EventLoss; kind++ {
		name := KindName(kind)
		if strings.HasPrefix(name, "kind_") || kinds[name] {
			t.Fatalf("aa31 kind %d label %q", kind, name)
		}
		kinds[name] = true
	}
	if KindName(localworker.AA25EventLoss+1) != "kind_12" || KindName(0) != "kind_0" {
		t.Fatal("aa31 unknown kinds must keep their number")
	}
	faults := map[string]bool{}
	for fault := localworker.AA25FaultNone; fault <= localworker.AA25FaultNackExhausted; fault++ {
		name := FaultName(fault)
		if strings.HasPrefix(name, "fault_") || faults[name] || (name == "") != (fault == localworker.AA25FaultNone) {
			t.Fatalf("aa31 fault %d label %q", fault, name)
		}
		faults[name] = true
	}
	if FaultName(localworker.AA25FaultNackExhausted+1) != "fault_16" {
		t.Fatal("aa31 unknown faults must keep their number")
	}
}

func testEvent() localworker.AA25Event {
	return localworker.AA25Event{
		AA25Kind:        localworker.AA25EventFault,
		AA25Fault:       localworker.AA25FaultBadCRC,
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
		AA25Level:       localworker.AA25LevelHigh,
		AA25Dissent:     3,
		AA25EdgePhase:   [10]uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	}
}

// Catches, per field, a record conversion that drops, swaps or reuses one
// event field: kind, fault, tick, sample index, start tick, deadline, missed,
// generation, raw, logical, was-escaped, echo, level, dissent and edge phase.
func TestAA31RecordKeepsEveryEventField(t *testing.T) {
	mutations := map[string]func(*localworker.AA25Event){
		"kind":         func(e *localworker.AA25Event) { e.AA25Kind = localworker.AA25EventRawSymbol },
		"fault":        func(e *localworker.AA25Event) { e.AA25Fault = localworker.AA25FaultBadStop },
		"tick":         func(e *localworker.AA25Event) { e.AA25Tick++ },
		"sample_index": func(e *localworker.AA25Event) { e.AA25SampleIndex++ },
		"start_tick":   func(e *localworker.AA25Event) { e.AA25StartTick++ },
		"deadline":     func(e *localworker.AA25Event) { e.AA25Deadline++ },
		"missed":       func(e *localworker.AA25Event) { e.AA25Missed++ },
		"generation":   func(e *localworker.AA25Event) { e.AA25Generation++ },
		"raw":          func(e *localworker.AA25Event) { e.AA25Raw++ },
		"logical":      func(e *localworker.AA25Event) { e.AA25Logical++ },
		"was_escaped":  func(e *localworker.AA25Event) { e.AA25WasEscaped = false },
		"echo":         func(e *localworker.AA25Event) { e.AA25Echo = false },
		"level":        func(e *localworker.AA25Event) { e.AA25Level = localworker.AA25LevelLow },
		"dissent":      func(e *localworker.AA25Event) { e.AA25Dissent++ },
		"edge_phase":   func(e *localworker.AA25Event) { e.AA25EdgePhase[9]++ },
	}
	base := Record(testEvent())
	want := EventRecord{
		Record: "event", Kind: "fault", Fault: "bad_crc", Tick: 11, SampleIndex: 12,
		StartTick: 13, Deadline: 14, Missed: 15, Generation: 16, Raw: 0xA9,
		Logical: 0xAA, WasEscaped: true, Echo: true, Level: 1, Dissent: 3,
		EdgePhase: [10]uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	}
	if base != want {
		t.Fatalf("aa31 record = %+v, want %+v", base, want)
	}
	for name, mutate := range mutations {
		event := testEvent()
		mutate(&event)
		if Record(event) == base {
			t.Fatalf("aa31 record ignores %s", name)
		}
	}
}

// Catches, per counter, a copy that drops or swaps one engine counter.
func TestAA31EngineCountersKeepEveryField(t *testing.T) {
	got := EngineCountersFrom(localworker.AA25Stats{
		AA25Observations: 1, AA25MissedSamples: 2, AA25MaxObservationLate: 3, AA25TXEdges: 4,
		AA25EchoedSymbols: 5, AA25EventsDropped: 6, AA25ControlsApplied: 7, AA25Withdrawals: 8,
	})
	want := EngineCounters{
		Observations: 1, MissedSamples: 2, MaxObservationLate: 3, TXEdges: 4,
		EchoedSymbols: 5, EventsDropped: 6, ControlsApplied: 7, Withdrawals: 8,
	}
	if got != want {
		t.Fatalf("aa31 engine counters = %+v", got)
	}
}

// Catches: a passive verdict that ignores any one write indicator, or a caller
// that could assert passivity or evidence levels itself.
func TestAA31SealDerivesPassiveFromEveryWriteIndicator(t *testing.T) {
	clean := Seal(Summary{Passive: false, TimingEvidence: "proven", Engine: EngineCounters{Observations: 9}})
	if !clean.Passive || clean.Record != "summary" || clean.TimingEvidence != "unknown" || clean.CaptureEvidence != "hypothesis" {
		t.Fatalf("aa31 clean seal = %+v", clean)
	}
	dirty := map[string]func(*Summary){
		"engine_tx_edges":        func(s *Summary) { s.Engine.TXEdges = 1 },
		"backend_stored_edges":   func(s *Summary) { s.Backend.StoredEdges = 1 },
		"backend_refused_edges":  func(s *Summary) { s.Backend.RefusedEdges = 1 },
		"backend_releases":       func(s *Summary) { s.Backend.Releases = 1 },
		"backend_lease_expiries": func(s *Summary) { s.Backend.LeaseExpiries = 1 },
		"window_store_attempts":  func(s *Summary) { s.WindowStores = 1 },
	}
	for name, mutate := range dirty {
		summary := Summary{Passive: true}
		mutate(&summary)
		if Seal(summary).Passive {
			t.Fatalf("aa31 seal ignores %s", name)
		}
	}
}

// Catches: a report that reorders events, omits the summary or emits more
// than one JSON value per line.
func TestAA31WriteReportEmitsEventsThenSummary(t *testing.T) {
	first := testEvent()
	second := testEvent()
	second.AA25Kind = localworker.AA25EventRawSymbol
	second.AA25Tick = 99
	var out bytes.Buffer
	if err := WriteReport(&out, []localworker.AA25Event{first, second}, Summary{ProfileID: "aa31-synthetic-profile"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("aa31 report lines = %q", lines)
	}
	var event EventRecord
	if err := json.Unmarshal([]byte(lines[1]), &event); err != nil || event != Record(second) {
		t.Fatalf("aa31 second event = %+v, %v", event, err)
	}
	var summary Summary
	if err := json.Unmarshal([]byte(lines[2]), &summary); err != nil || summary.Record != "summary" || !summary.Passive || summary.ProfileID != "aa31-synthetic-profile" {
		t.Fatalf("aa31 summary = %+v, %v", summary, err)
	}
}

// Catches: a counter port that reports a rate it does not tick at or that can
// run backwards.
func TestAA31MonotonicCounterIsNanosecondsAndMonotonic(t *testing.T) {
	counter := NewMonotonicCounter()
	if counter.AA29CounterHz() != MonotonicHz || MonotonicHz != uint64(time.Second) {
		t.Fatalf("aa31 counter rate = %d", counter.AA29CounterHz())
	}
	previous := counter.AA29ReadCounter()
	for index := 0; index < 1_000; index++ {
		now := counter.AA29ReadCounter()
		if now < previous {
			t.Fatalf("aa31 counter regressed %d -> %d", previous, now)
		}
		previous = now
	}
	before := counter.AA29ReadCounter()
	time.Sleep(2 * time.Millisecond)
	if elapsed := counter.AA29ReadCounter() - before; elapsed < uint64(2*time.Millisecond) {
		t.Fatalf("aa31 counter advanced %d ticks across 2ms", elapsed)
	}
}
