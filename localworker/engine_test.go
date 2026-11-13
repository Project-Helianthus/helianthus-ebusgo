package localworker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"testing"
)

// Every trace below is synthetic and every tick is injected. Expected schedules
// are computed with math/big from the nominal definition, not with engine code.
const (
	engineTestHz     uint64 = 1_000_000_000
	testAnchor       uint64 = 1000
	engineTestSource byte   = 0x10
	testResponder    byte   = 0x15
	engineTestPB     byte   = 0xB5
	engineTestSB     byte   = 0x09
)

type testTransition struct {
	tick  uint64
	level Level
}

type bench struct {
	t            *testing.T
	hz           uint64
	now          uint64
	external     []testTransition
	cursor       int
	externalNow  Level
	writes       []testTransition
	reads        []testTransition
	events       []Event
	propagation  uint64
	disconnected bool
	sinkLimited  bool
	sinkFree     int
	engine       *Engine
	config       EngineConfig
	sendLevel    Level
}

func (b *bench) NowTicks() uint64 { return b.now }

func (b *bench) SampleLevel() Level {
	level := b.lineLevel()
	b.reads = append(b.reads, testTransition{b.now, level})
	return level
}

func (b *bench) lineLevel() Level {
	for b.cursor < len(b.external) && b.external[b.cursor].tick <= b.now {
		b.externalNow = b.external[b.cursor].level
		b.cursor++
	}
	level := b.externalNow
	if b.disconnected {
		return level
	}
	for index := len(b.writes) - 1; index >= 0; index-- {
		if b.writes[index].tick+b.propagation <= b.now {
			if b.writes[index].level == LevelLow {
				return LevelLow
			}
			break
		}
	}
	return level
}

func (b *bench) WriteEdge(level Level) {
	b.writes = append(b.writes, testTransition{b.now, level})
}

func (b *bench) TryPublish(event Event) bool {
	if b.sinkLimited {
		if b.sinkFree == 0 {
			return false
		}
		b.sinkFree--
	}
	b.events = append(b.events, event)
	return true
}

func engineTestConfig() EngineConfig {
	return EngineConfig{TickHz: engineTestHz, ResponderAddress: testResponder}
}

func newBench(t *testing.T, config EngineConfig) *bench {
	t.Helper()
	b := &bench{
		t: t, hz: config.TickHz, now: testAnchor, config: config,
		externalNow: LevelHigh, sendLevel: LevelHigh,
		reads:  make([]testTransition, 0, 1<<17),
		events: make([]Event, 0, 1<<13),
		writes: make([]testTransition, 0, 1<<9),
	}
	engine, err := NewEngine(config, Ports{Levels: b, Edges: b, Ticks: b, Sink: b})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	b.engine = engine
	return b
}

func (b *bench) offer(generation uint64, pb, sb, nn byte, payload ...byte) Prepared {
	b.t.Helper()
	prepared, err := PrepareResponse(generation, pb, sb, nn, payload)
	if err != nil {
		b.t.Fatalf("prepare: %v", err)
	}
	if !b.engine.OfferPrepared(prepared) {
		b.t.Fatal("control queue full")
	}
	return prepared
}

// step advances the clock to an exact tick and performs one engine pass.
func (b *bench) step(tick uint64) {
	b.now = tick
	b.engine.Step()
}

// run polls on the engine's own next deadline, so every step is on time.
func (b *bench) run(until uint64) {
	b.t.Helper()
	for guard := 0; guard < 1<<22; guard++ {
		next := b.engine.NextDeadlineTick()
		if next > until {
			return
		}
		if next > b.now {
			b.now = next
		}
		b.engine.Step()
	}
	b.t.Fatal("bench made no progress")
}

func (b *bench) runUntilEvent(kind EventKind, limit uint64) {
	b.t.Helper()
	for guard := 0; guard < 1<<22; guard++ {
		if len(b.eventsOfKind(kind)) > 0 {
			return
		}
		next := b.engine.NextDeadlineTick()
		if next > limit {
			b.t.Fatalf("event kind %d never occurred; events=%d faults=%v", kind, len(b.events), b.faults())
		}
		if next > b.now {
			b.now = next
		}
		b.engine.Step()
	}
	b.t.Fatal("bench made no progress")
}

func rat(num, den uint64) *big.Rat {
	return new(big.Rat).SetFrac(new(big.Int).SetUint64(num), new(big.Int).SetUint64(den))
}

func ceil(value *big.Rat) uint64 {
	quotient, remainder := new(big.Int).QuoRem(value.Num(), value.Denom(), new(big.Int))
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient.Uint64()
}

func (b *bench) samplePeriod() *big.Rat {
	return rat(b.hz, 24000)
}

func (b *bench) gridTime(samples uint64) *big.Rat {
	value := new(big.Rat).Mul(rat(samples, 1), b.samplePeriod())
	return value.Add(value, rat(testAnchor, 1))
}

// gridDeadline is the independent definition: the first whole tick at or
// after anchor + n/24000 seconds.
func (b *bench) gridDeadline(samples uint64) uint64 {
	return ceil(b.gridTime(samples))
}

func (b *bench) onGrid(tick uint64) bool {
	elapsed := rat(tick-testAnchor, 1)
	elapsed.Quo(elapsed, b.samplePeriod())
	floor := new(big.Int).Quo(elapsed.Num(), elapsed.Denom()).Uint64()
	return b.gridDeadline(floor) == tick || b.gridDeadline(floor+1) == tick
}

func (b *bench) addTransition(time *big.Rat, level Level) {
	if level == b.sendLevel {
		return
	}
	b.sendLevel = level
	b.external = append(b.external, testTransition{ceil(time), level})
}

// sendCharacter appends one character with an explicit stop level and a
// sender bit period of scale nominal bits; it returns the character's end.
func (b *bench) sendCharacter(start *big.Rat, scale *big.Rat, symbol byte, stop Level) *big.Rat {
	bit := new(big.Rat).Mul(rat(b.hz, 2400), scale)
	boundary := func(k uint64) *big.Rat {
		value := new(big.Rat).Mul(rat(k, 1), bit)
		return value.Add(value, start)
	}
	b.addTransition(boundary(0), LevelLow)
	for k := uint64(0); k < 8; k++ {
		b.addTransition(boundary(k+1), Level(symbol>>k&1))
	}
	b.addTransition(boundary(9), stop)
	end := boundary(10)
	b.addTransition(end, LevelHigh)
	return end
}

// send appends contiguous nominal characters and returns their end.
func (b *bench) send(start *big.Rat, symbols ...byte) *big.Rat {
	for _, symbol := range symbols {
		start = b.sendCharacter(start, rat(1, 1), symbol, LevelHigh)
	}
	return start
}

func engineTestExpand(logical []byte) []byte {
	wire := make([]byte, 0, 2*len(logical))
	for _, value := range logical {
		switch value {
		case 0xA9:
			wire = append(wire, 0xA9, 0x00)
		case 0xAA:
			wire = append(wire, 0xA9, 0x01)
		default:
			wire = append(wire, value)
		}
	}
	return wire
}

// engineTestCRC multiplies by x^8 modulo x^8+0x9B over GF(2) one symbol at a time.
// TestAA25CRCVectors pins it to literal vectors.
func engineTestCRC(logical []byte) byte {
	remainder := uint16(0)
	for _, symbol := range engineTestExpand(logical) {
		value := remainder << 8
		for bit := 15; bit >= 8; bit-- {
			if value&(1<<bit) != 0 {
				value ^= 0x19B << (bit - 8)
			}
		}
		remainder = value ^ uint16(symbol)
	}
	return byte(remainder)
}

func testRequest(target, pb, sb byte, data ...byte) []byte {
	logical := append([]byte{engineTestSource, target, pb, sb, byte(len(data))}, data...)
	logical = append(logical, engineTestCRC(logical))
	return engineTestExpand(logical)
}

func (b *bench) eventsOfKind(kind EventKind) []Event {
	var result []Event
	for _, event := range b.events {
		if event.Kind == kind {
			result = append(result, event)
		}
	}
	return result
}

func (b *bench) faults() []Fault {
	var result []Fault
	for _, event := range b.eventsOfKind(EventFault) {
		result = append(result, event.Fault)
	}
	return result
}

func (b *bench) hasFault(fault Fault) bool {
	for _, value := range b.faults() {
		if value == fault {
			return true
		}
	}
	return false
}

func (b *bench) rawSymbols(echo bool) []byte {
	var result []byte
	for _, event := range b.eventsOfKind(EventRawSymbol) {
		if event.Echo == echo {
			result = append(result, event.Raw)
		}
	}
	return result
}

func (b *bench) logicalBytes(echo bool) []byte {
	var result []byte
	for _, event := range b.eventsOfKind(EventLogicalByte) {
		if event.Echo == echo {
			result = append(result, event.Logical)
		}
	}
	return result
}

func equalBytes(left, right []byte) bool {
	return string(left) == string(right)
}

// expectedEdges derives the exact transmit schedule from the wire bytes.
func (b *bench) expectedEdges(firstLow uint64, wire []byte) []testTransition {
	base := rat(firstLow+b.config.TurnaroundTicks, 1)
	bit := rat(b.hz, 2400)
	level := LevelHigh
	var result []testTransition
	add := func(k uint64, want Level) {
		if want != level {
			time := new(big.Rat).Mul(rat(10+k, 1), bit)
			result = append(result, testTransition{ceil(time.Add(time, base)), want})
			level = want
		}
	}
	for index, symbol := range wire {
		k := uint64(index) * 10
		add(k, LevelLow)
		for data := uint64(0); data < 8; data++ {
			add(k+1+data, Level(symbol>>data&1))
		}
		add(k+9, LevelHigh)
	}
	return result
}

func equalEdges(left, right []testTransition) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func preparedWire(prepared Prepared) []byte {
	return prepared.wire[:prepared.wireLen]
}

// finalStart returns the first-low tick of the last request symbol.
func (b *bench) finalStart() uint64 {
	raw := b.eventsOfKind(EventRawSymbol)
	last := uint64(0)
	for _, event := range raw {
		if !event.Echo {
			last = event.StartTick
		}
	}
	return last
}

// idle is a start time past the startup or post-fault resynchronization.
func (b *bench) idle(from uint64) *big.Rat {
	elapsed := rat(from-testAnchor, 1)
	elapsed.Quo(elapsed, b.samplePeriod())
	floor := new(big.Int).Quo(elapsed.Num(), elapsed.Denom()).Uint64()
	return b.gridTime(floor + 120)
}

// sendRequest queues SYN plus the request after startup idle and returns
// the time the request ends.
func (b *bench) sendRequest(wire []byte) *big.Rat {
	end := b.send(b.gridTime(120), 0xAA)
	return b.send(end, wire...)
}

func tickAfter(time *big.Rat, samples uint64, b *bench) uint64 {
	value := new(big.Rat).Mul(rat(samples, 1), b.samplePeriod())
	return ceil(value.Add(value, time))
}

func TestAA25CRCVectors(t *testing.T) {
	// Literal vectors of the public CRC definition, including wire expansion.
	for _, tc := range []struct {
		logical []byte
		want    byte
	}{
		{nil, 0x00},
		{[]byte{0x01, 0x02}, 0x99},
		{[]byte{0x10, 0xFE, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA}, 0x77},
	} {
		if got := engineTestCRC(tc.logical); got != tc.want {
			t.Fatalf("reference CRC(% X) = %02X; want %02X", tc.logical, got, tc.want)
		}
		state := byte(0)
		for _, symbol := range engineTestExpand(tc.logical) {
			state = engineCRCUpdate(state, symbol)
		}
		if state != tc.want {
			t.Fatalf("incremental CRC(% X) = %02X; want %02X", tc.logical, state, tc.want)
		}
	}
}

func TestAA25RationalScheduleRetainsRemainders(t *testing.T) {
	whole, remainder := rationalAt(1, engineTestHz, 24000)
	if whole != 41666 || remainder != 16000 {
		t.Fatalf("one sample = %d rem %d", whole, remainder)
	}
	whole, remainder = rationalAt(3, engineTestHz, 24000)
	if whole != 125000 || remainder != 0 {
		t.Fatalf("three samples = %d rem %d", whole, remainder)
	}
	for _, hz := range []uint64{24000, 1_000_003, 19_200_000, 54_000_000, engineTestHz, 3_000_000_001} {
		for _, den := range []uint64{24000, 2400} {
			for _, index := range []uint64{0, 1, 2, 3, 7, 239, 24000, 86_400 * 24000, 1 << 40} {
				exact := new(big.Rat).Mul(rat(index, 1), rat(hz, den))
				want := ceil(exact.Add(exact, rat(testAnchor, 1)))
				if got := rationalDeadline(testAnchor, index, hz, den); got != want {
					t.Fatalf("hz=%d den=%d n=%d: %d; want %d", hz, den, index, got, want)
				}
			}
		}
	}
	// One simulated second has no drift: 24000 steps land exactly on +1e9 ticks,
	// where a truncated 41666-tick period would be 16000 ticks short.
	b := newBench(t, engineTestConfig())
	b.run(testAnchor + engineTestHz)
	if len(b.reads) != 24001 {
		t.Fatalf("reads in one second = %d", len(b.reads))
	}
	if got := b.reads[24000].tick; got != testAnchor+engineTestHz {
		t.Fatalf("drift: %d", got)
	}
	for index, read := range b.reads {
		if want := b.gridDeadline(uint64(index)); read.tick != want {
			t.Fatalf("sample %d at %d; want %d", index, read.tick, want)
		}
	}
	if b.engine.Stats().MissedSamples != 0 {
		t.Fatal("on-time polling reported a gap")
	}
}

func TestAA25LateObservationKeepsActualTimestampAndAbsoluteSchedule(t *testing.T) {
	b := newBench(t, engineTestConfig())
	b.run(b.gridDeadline(9))
	late := b.gridDeadline(10) + 7000
	b.step(late)
	read := b.reads[len(b.reads)-1]
	if read.tick != late {
		t.Fatalf("observation timestamp %d; want actual %d", read.tick, late)
	}
	if next, want := b.engine.NextDeadlineTick(), b.gridDeadline(11); next != want {
		t.Fatalf("next deadline %d; want absolute %d", next, want)
	}
	stats := b.engine.Stats()
	if stats.MaxObservationLate != 7000 || stats.MissedSamples != 0 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestAA25DecodesEverySymbolAtBitCentres(t *testing.T) {
	b := newBench(t, engineTestConfig())
	// SYN goes last: a leading SYN would turn the remaining bytes into a frame
	// whose length byte exceeds the protocol maximum.
	symbols := make([]byte, 0, 256)
	for value := 0; value < 256; value++ {
		if byte(value) != 0xAA {
			symbols = append(symbols, byte(value))
		}
	}
	symbols = append(symbols, 0xAA)
	end := b.send(b.gridTime(120), symbols...)
	b.run(tickAfter(end, 5, b))
	if got := b.rawSymbols(false); !equalBytes(got, symbols) {
		t.Fatalf("decoded % X", got)
	}
	for _, event := range b.eventsOfKind(EventRawSymbol) {
		if event.Dissent != 0 {
			t.Fatalf("clean symbol dissent: %+v", event)
		}
		for position, count := range event.EdgePhase {
			if position != 0 && count != 0 {
				t.Fatalf("aligned edge at position %d: %+v", position, event)
			}
		}
		// The symbol is timestamped at the observation that validated its stop.
		if event.Tick != b.gridDeadline(event.SampleIndex) {
			t.Fatalf("symbol timestamp: %+v", event)
		}
		if offset := event.SampleIndex - (event.StartTick-testAnchor)*24000/engineTestHz; offset < 95 || offset > 97 {
			t.Fatalf("symbol completed at offset %d", offset)
		}
	}
	if len(b.faults()) != 0 {
		t.Fatalf("faults %v", b.faults())
	}
}

func TestAA25CentralObservationsDecideBits(t *testing.T) {
	// The 0xFF data bits are high: pulses inside one data bit vary which of its
	// ten observations are low. Start-relative offsets; bit 1 occupies 10..19.
	for _, tc := range []struct {
		name         string
		first, count uint64
		want         byte
		dissent      uint8
	}{
		{"none", 0, 0, 0xFF, 0},
		{"non-central-before", 11, 3, 0xFF, 0},
		{"non-central-after", 17, 3, 0xFF, 0},
		{"one-central-first", 14, 1, 0xFF, 1},
		{"one-central-middle", 15, 1, 0xFF, 1},
		{"one-central-last", 16, 1, 0xFF, 1},
		{"two-central-flip", 14, 2, 0xFE, 1},
		{"all-central-flip", 14, 3, 0xFE, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, engineTestConfig())
			start := uint64(120)
			b.external = []testTransition{
				{b.gridDeadline(start), LevelLow},
				{b.gridDeadline(start + 10), LevelHigh},
			}
			if tc.count != 0 {
				b.external = append(b.external,
					testTransition{b.gridDeadline(start + tc.first), LevelLow},
					testTransition{b.gridDeadline(start + tc.first + tc.count), LevelHigh})
			}
			b.run(b.gridDeadline(start + 110))
			raw := b.eventsOfKind(EventRawSymbol)
			if len(raw) != 1 || raw[0].Raw != tc.want || raw[0].Dissent != tc.dissent {
				t.Fatalf("raw %+v; want %02X dissent %d", raw, tc.want, tc.dissent)
			}
			if tc.count != 0 && tc.name == "non-central-before" &&
				(raw[0].EdgePhase[1] != 1 || raw[0].EdgePhase[4] != 1) {
				t.Fatalf("edge phase not retained: %+v", raw[0].EdgePhase)
			}
		})
	}
}

func TestAA25PhaseAnchorsToFirstObservedLow(t *testing.T) {
	// Each sender start falls inside one sample period; the first observation at
	// or after it is the anchor, and decoding is unchanged.
	symbols := []byte{0x55, 0x00, 0xFF, 0x3C, 0xA9, 0x01}
	for _, fraction := range []*big.Rat{rat(0, 1), rat(1, 4), rat(1, 2), rat(3, 4), rat(999, 1000)} {
		b := newBench(t, engineTestConfig())
		shift := new(big.Rat).Mul(fraction, b.samplePeriod())
		start := new(big.Rat).Add(b.gridTime(120), shift)
		end := b.send(start, symbols...)
		b.run(tickAfter(end, 5, b))
		if got := b.rawSymbols(false); !equalBytes(got, symbols) {
			t.Fatalf("fraction %v decoded % X", fraction, got)
		}
		wantSample := uint64(121)
		if fraction.Sign() == 0 {
			wantSample = 120
		}
		if got, want := b.eventsOfKind(EventRawSymbol)[0].StartTick, b.gridDeadline(wantSample); got != want {
			t.Fatalf("fraction %v anchor %d; want %d", fraction, got, want)
		}
	}
}

func TestAA25DriftReanchorsEachCharacter(t *testing.T) {
	symbols := []byte{0x55, 0xAA, 0x00, 0xFF, 0x0F, 0xF0, 0x81, 0x7E, 0x55, 0x33}
	send := func(b *bench, scale *big.Rat) *big.Rat {
		start := b.gridTime(120)
		for _, symbol := range symbols {
			start = b.sendCharacter(start, scale, symbol, LevelHigh)
		}
		return start
	}
	for _, scale := range []*big.Rat{rat(97, 100), rat(103, 100)} {
		b := newBench(t, engineTestConfig())
		end := send(b, scale)
		b.run(tickAfter(end, 5, b))
		if got := b.rawSymbols(false); !equalBytes(got, symbols) {
			t.Fatalf("scale %v decoded % X", scale, got)
		}
		// Bit-rate error appears in the retained edge phase instead of position 0 only.
		spread := false
		for _, event := range b.eventsOfKind(EventRawSymbol) {
			for position, count := range event.EdgePhase {
				spread = spread || (position != 0 && count != 0)
			}
		}
		if !spread {
			t.Fatalf("scale %v left no phase evidence", scale)
		}
	}
	// Control: an excessive rate error must not decode cleanly.
	b := newBench(t, engineTestConfig())
	end := send(b, rat(115, 100))
	b.run(tickAfter(end, 5, b))
	if equalBytes(b.rawSymbols(false), symbols) {
		t.Fatal("15% slow sender decoded cleanly")
	}
}

func TestAA25RejectsBadStartAndBadStop(t *testing.T) {
	t.Run("false-start", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		// Low only for offsets 0..3, before the start bit's central observations.
		b.external = []testTransition{
			{b.gridDeadline(120), LevelLow},
			{b.gridDeadline(124), LevelHigh},
		}
		b.run(b.gridDeadline(260))
		if !b.hasFault(FaultFalseStart) || len(b.eventsOfKind(EventRawSymbol)) != 0 {
			t.Fatalf("faults %v events %d", b.faults(), len(b.events))
		}
	})
	t.Run("start-majority-control", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		// One high central observation (offset 5) cannot reject the start bit.
		b.external = []testTransition{
			{b.gridDeadline(120), LevelLow},
			{b.gridDeadline(125), LevelHigh},
			{b.gridDeadline(126), LevelLow},
			{b.gridDeadline(130), LevelHigh},
		}
		b.run(b.gridDeadline(260))
		raw := b.eventsOfKind(EventRawSymbol)
		if len(raw) != 1 || raw[0].Raw != 0xFF || raw[0].Dissent != 1 {
			t.Fatalf("raw %+v faults %v", raw, b.faults())
		}
	})
	t.Run("bad-stop-blocks-response", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x11)
		wire := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
		end := b.send(b.gridTime(120), 0xAA)
		end = b.send(end, wire[:len(wire)-1]...)
		end = b.sendCharacter(end, rat(1, 1), wire[len(wire)-1], LevelLow)
		b.run(tickAfter(end, 400, b))
		if !b.hasFault(FaultBadStop) {
			t.Fatalf("faults %v", b.faults())
		}
		if len(b.writes) != 0 || len(b.eventsOfKind(EventAdmitted)) != 0 {
			t.Fatalf("responded after bad stop: %+v", b.writes)
		}
		// The bad symbol is not published as observed.
		if got := len(b.rawSymbols(false)); got != len(wire) {
			t.Fatalf("raw count %d; want %d", got, len(wire))
		}
	})
}

func TestAA25ResyncNeedsOneWholeIdleCharacter(t *testing.T) {
	// Samples 0..N-1 are high and sample N is the first low: N=100 is the exact
	// boundary. Mutation caught: any other idle count.
	for _, tc := range []struct {
		highSamples uint64
		decoded     int
	}{{99, 0}, {100, 1}, {101, 1}} {
		b := newBench(t, engineTestConfig())
		end := b.send(b.gridTime(tc.highSamples), 0x00)
		b.run(tickAfter(end, 20, b))
		if got := len(b.rawSymbols(false)); got != tc.decoded {
			t.Fatalf("%d idle samples decoded %d symbols; want %d", tc.highSamples, got, tc.decoded)
		}
	}
	// After a gap inside a busy stream no mid-character edge is taken as a start.
	b := newBench(t, engineTestConfig())
	stream := make([]byte, 12)
	for index := range stream {
		stream[index] = 0x55
	}
	end := b.send(b.gridTime(120), stream...)
	b.run(b.gridDeadline(120 + 250))
	before := len(b.rawSymbols(false))
	b.step(b.gridDeadline(120 + 253))
	b.run(tickAfter(end, 0, b))
	if got := len(b.rawSymbols(false)); got != before {
		t.Fatalf("decoded %d symbols inside a busy stream after a gap", got-before)
	}
}

func TestAA25ResponseScheduleEchoAndCompletion(t *testing.T) {
	for _, hz := range []uint64{engineTestHz, 19_200_000, 1_000_003} {
		config := engineTestConfig()
		config.TickHz = hz
		b := newBench(t, config)
		prepared := b.offer(7, engineTestPB, engineTestSB, 2, 0x11, 0x22)
		request := testRequest(testResponder, engineTestPB, engineTestSB, 0x01, 0x02)
		end := b.sendRequest(request)
		b.run(tickAfter(end, 700, b))

		wire := preparedWire(prepared)
		wantWire := []byte{0x00, 0x02, 0x11, 0x22, engineTestCRC([]byte{0x02, 0x11, 0x22})}
		if !equalBytes(wire, wantWire) {
			t.Fatalf("prepared wire % X; want % X", wire, wantWire)
		}
		want := b.expectedEdges(b.finalStart(), wire)
		if !equalEdges(b.writes, want) {
			t.Fatalf("hz=%d edges %v; want %v (faults %v)", hz, b.writes, want, b.faults())
		}
		if got := b.rawSymbols(true); !equalBytes(got, wire) {
			t.Fatalf("echo % X; want % X", got, wire)
		}
		if got := b.rawSymbols(false); !equalBytes(got, append([]byte{0xAA}, request...)) {
			t.Fatalf("request raw % X", got)
		}
		complete := b.eventsOfKind(EventTXComplete)
		if len(complete) != 1 || complete[0].Generation != 7 {
			t.Fatalf("complete %+v faults %v", complete, b.faults())
		}
		// Completion waits for the last stop bit's scheduled end, not its echo vote.
		wantEnd := new(big.Rat).Mul(rat(10+10*uint64(len(wire)), 1), rat(hz, 2400))
		wantEnd.Add(wantEnd, rat(b.finalStart(), 1))
		if complete[0].Tick != ceil(wantEnd) {
			t.Fatalf("completion at %d; want %d", complete[0].Tick, ceil(wantEnd))
		}
		if len(b.faults()) != 0 || b.engine.Stats().EchoedSymbols != uint64(len(wire)) {
			t.Fatalf("faults %v stats %+v", b.faults(), b.engine.Stats())
		}
	}
}

func TestAA25NoEmissionBeforeFinalReceivePhase(t *testing.T) {
	// Admission happens at the stop bit's central observations; the first edge
	// stays at the exact character end plus turnaround for each turnaround.
	for _, turnaround := range []uint64{0, 1, 12345, 416_667} {
		config := engineTestConfig()
		config.TurnaroundTicks = turnaround
		b := newBench(t, config)
		b.offer(1, engineTestPB, engineTestSB, 0, 0x11)
		end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB))
		b.runUntilEvent(EventAdmitted, tickAfter(end, 10, b))
		admitted := b.eventsOfKind(EventAdmitted)[0]
		firstLow := b.finalStart()
		expected := rat(firstLow+turnaround, 1)
		character := new(big.Rat).Mul(rat(10, 1), rat(engineTestHz, 2400))
		wantEdge := ceil(expected.Add(expected, character))
		if len(b.writes) != 0 || admitted.Tick >= wantEdge || admitted.Deadline != wantEdge {
			t.Fatalf("early state: writes=%v admitted=%+v want=%d", b.writes, admitted, wantEdge)
		}
		b.run(wantEdge - 1)
		if len(b.writes) != 0 {
			t.Fatalf("edge before %d: %v", wantEdge, b.writes)
		}
		b.run(wantEdge)
		if len(b.writes) != 1 || b.writes[0] != (testTransition{wantEdge, LevelLow}) {
			t.Fatalf("turnaround %d first write %v; want %d", turnaround, b.writes, wantEdge)
		}
	}
	t.Run("final-phase-guard", func(t *testing.T) {
		// Even when the deadline tick is reached, an unobserved final phase blocks TX.
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 0, 0x11)
		end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB))
		b.runUntilEvent(EventAdmitted, tickAfter(end, 10, b))
		b.now = b.eventsOfKind(EventAdmitted)[0].Deadline
		b.engine.stepTX(b.now)
		if len(b.writes) != 0 {
			t.Fatalf("emitted before the final receive phase: %v", b.writes)
		}
	})
}

func TestAA25TXEdgesKeepTheirOwnGrid(t *testing.T) {
	t.Run("turnaround-off-grid", func(t *testing.T) {
		config := engineTestConfig()
		config.TurnaroundTicks = 12345
		b := newBench(t, config)
		prepared := b.offer(1, engineTestPB, engineTestSB, 1, 0x55)
		end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
		b.run(tickAfter(end, 500, b))
		want := b.expectedEdges(b.finalStart(), preparedWire(prepared))
		if !equalEdges(b.writes, want) {
			t.Fatalf("edges %v; want %v", b.writes, want)
		}
		offGrid := 0
		for _, write := range b.writes {
			if !b.onGrid(write.tick) {
				offGrid++
			}
		}
		if offGrid != len(b.writes) {
			t.Fatalf("%d of %d edges off the RX grid", offGrid, len(b.writes))
		}
	})
	t.Run("late-anchor-observation", func(t *testing.T) {
		// The final character's first low is observed late: TX follows the actual
		// timestamp and does not snap back onto the RX grid.
		b := newBench(t, engineTestConfig())
		prepared := b.offer(1, engineTestPB, engineTestSB, 0, 0x55)
		request := testRequest(testResponder, engineTestPB, engineTestSB)
		b.sendRequest(request)
		final := uint64(120 + 100*len(request))
		b.run(b.gridDeadline(final - 1))
		lateTick := b.gridDeadline(final) + 7000
		b.step(lateTick)
		b.run(b.gridDeadline(final + 500))
		if b.finalStart() != lateTick {
			t.Fatalf("anchor %d; want actual %d", b.finalStart(), lateTick)
		}
		want := b.expectedEdges(lateTick, preparedWire(prepared))
		if !equalEdges(b.writes, want) {
			t.Fatalf("edges %v; want %v faults %v", b.writes, want, b.faults())
		}
		if b.onGrid(b.writes[0].tick) {
			t.Fatalf("edge %d snapped to RX grid", b.writes[0].tick)
		}
	})
}

func TestAA25RawEscapePrefixPrecedesSuffixAndContinuity(t *testing.T) {
	// Literal wire of the public vector: logical A9 and AA inside the data, CRC
	// over the expanded form. The responder address is the vector's destination.
	wire := testRequest(testResponder, 0xB5, 0x05, 0x27, 0xA9, 0x15, 0xAA)
	logical := []byte{engineTestSource, testResponder, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA}
	logical = append(logical, engineTestCRC(logical))
	config := engineTestConfig()
	b := newBench(t, config)
	b.offer(1, 0xB5, 0x05, 4, 0x33)
	end := b.sendRequest(wire)
	b.run(tickAfter(end, 400, b))
	if !equalBytes(b.rawSymbols(false), append([]byte{0xAA}, wire...)) {
		t.Fatalf("raw % X", b.rawSymbols(false))
	}
	if !equalBytes(b.logicalBytes(false), logical) {
		t.Fatalf("logical % X", b.logicalBytes(false))
	}
	events := b.events
	prefixes := 0
	for index, event := range events {
		if event.Kind != EventRawSymbol || event.Raw != 0xA9 {
			continue
		}
		prefixes++
		// The prefix is out before the suffix character has even started, and
		// the very next stream record is the suffix's raw symbol, not a logical byte.
		next := events[index+1]
		if next.Kind != EventRawSymbol || next.StartTick <= event.Tick {
			t.Fatalf("prefix withheld or merged: %+v then %+v", event, next)
		}
		decoded := events[index+2]
		if decoded.Kind != EventLogicalByte || !decoded.WasEscaped || decoded.Tick != next.Tick {
			t.Fatalf("suffix logical: %+v", decoded)
		}
	}
	if prefixes != 2 {
		t.Fatalf("prefixes %d", prefixes)
	}
	if len(b.eventsOfKind(EventFrameValid)) != 1 || len(b.eventsOfKind(EventTXComplete)) != 1 {
		t.Fatalf("valid escaped frame did not complete: faults %v", b.faults())
	}
}

func TestAA25EscapedCRCAnchorsOnSuffix(t *testing.T) {
	for _, wantCRC := range []byte{0xA9, 0xAA} {
		data := -1
		for candidate := 0; candidate < 256; candidate++ {
			logical := []byte{engineTestSource, testResponder, engineTestPB, engineTestSB, 1, byte(candidate)}
			if engineTestCRC(logical) == wantCRC {
				data = candidate
				break
			}
		}
		if data < 0 {
			t.Fatalf("no data byte yields CRC %02X", wantCRC)
		}
		b := newBench(t, engineTestConfig())
		prepared := b.offer(1, engineTestPB, engineTestSB, 1, 0x44)
		request := testRequest(testResponder, engineTestPB, engineTestSB, byte(data))
		if request[len(request)-2] != 0xA9 {
			t.Fatalf("CRC not escaped: % X", request)
		}
		end := b.sendRequest(request)
		b.run(tickAfter(end, 500, b))
		raw := b.eventsOfKind(EventRawSymbol)
		suffix := uint64(0)
		for _, event := range raw {
			if !event.Echo {
				suffix = event.StartTick
			}
		}
		// end is the suffix's end: the first edge is a whole character after
		// the suffix start, never a character after the prefix.
		if want := b.expectedEdges(suffix, preparedWire(prepared)); !equalEdges(b.writes, want) {
			t.Fatalf("CRC %02X edges %v; want %v", wantCRC, b.writes, want)
		}
	}
	// A response whose own CRC needs escaping is prepared in its wire form.
	for _, wantCRC := range []byte{0xA9, 0xAA} {
		for candidate := 0; candidate < 256; candidate++ {
			if engineTestCRC([]byte{0x01, byte(candidate)}) != wantCRC {
				continue
			}
			prepared, err := PrepareResponse(1, 0, 0, 0, []byte{byte(candidate)})
			if err != nil {
				t.Fatal(err)
			}
			wire := preparedWire(prepared)
			want := append(append([]byte{0x00}, engineTestExpand([]byte{0x01, byte(candidate)})...), engineTestExpand([]byte{wantCRC})...)
			if !equalBytes(wire, want) {
				t.Fatalf("wire % X; want % X", wire, want)
			}
		}
	}
}

func TestAA25NonInitiatorSourceNeverPins(t *testing.T) {
	// Each source is paired with the responder as destination and a matching
	// PB/SB/NN shape, so only the source class differs from the valid control.
	for _, tc := range []struct {
		name   string
		source byte
		admit  bool
	}{
		{"initiator-source-control", engineTestSource, true},
		{"responder-source", 0x08, false},
		{"responder-source-high", 0xF5, false},
		{"responder-source-peer", 0x04, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, engineTestConfig())
			b.offer(1, engineTestPB, engineTestSB, 1, 0x11)
			logical := []byte{tc.source, testResponder, engineTestPB, engineTestSB, 1, 0x01}
			logical = append(logical, engineTestCRC(logical))
			end := b.sendRequest(engineTestExpand(logical))
			b.run(tickAfter(end, 500, b))
			done := len(b.eventsOfKind(EventTXComplete)) == 1
			if done != tc.admit {
				t.Fatalf("admit %v want %v: faults %v", done, tc.admit, b.faults())
			}
			if !tc.admit && (len(b.writes) != 0 ||
				len(b.eventsOfKind(EventProvisional)) != 0 || !b.hasFault(FaultAddressInvalid)) {
				t.Fatalf("non-initiator source drove the bus: writes %v faults %v", b.writes, b.faults())
			}
		})
	}
}

func TestAA25FrameFaultsStaySilent(t *testing.T) {
	good := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
	badCRC := append([]byte{}, good...)
	badCRC[len(badCRC)-1] ^= 0x40
	header := good[:5]
	for _, tc := range []struct {
		name  string
		wire  []byte
		fault Fault
	}{
		{"bad-crc", badCRC, FaultBadCRC},
		{"invalid-escape", append(append([]byte{}, header...), 0xA9, 0x05, 0x00), FaultEscapeInvalid},
		{"escape-then-syn", append(append([]byte{}, header...), 0xA9, 0xAA), FaultEscapeInvalid},
		{"syn-in-frame", append(append([]byte{}, header...), 0xAA), FaultSynInFrame},
		{"escaped-source", []byte{0xA9, 0x00, testResponder}, FaultAddressInvalid},
		{"escaped-target", []byte{engineTestSource, 0xA9, 0x01}, FaultAddressInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, engineTestConfig())
			b.offer(1, engineTestPB, engineTestSB, 1, 0x11)
			end := b.sendRequest(tc.wire)
			b.run(tickAfter(end, 300, b))
			if !b.hasFault(tc.fault) || len(b.writes) != 0 {
				t.Fatalf("faults %v writes %v", b.faults(), b.writes)
			}
			// The prepared response survives a malformed frame: last-known-good state.
			end = b.send(b.idle(b.now), 0xAA)
			end = b.send(end, good...)
			b.run(tickAfter(end, 500, b))
			if len(b.eventsOfKind(EventTXComplete)) != 1 {
				t.Fatalf("recovery failed: faults %v", b.faults())
			}
		})
	}
	t.Run("dropped-frame-needs-a-new-syn", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x11)
		// A complete valid request follows the fault without a SYN between them.
		end := b.sendRequest(append([]byte{0xA9, 0x05}, good...))
		b.run(tickAfter(end, 300, b))
		if !b.hasFault(FaultEscapeInvalid) || len(b.writes) != 0 ||
			len(b.eventsOfKind(EventFrameValid)) != 0 || len(b.logicalBytes(false)) != 0 {
			t.Fatalf("parsed without SYN: writes %v logical % X", b.writes, b.logicalBytes(false))
		}
	})
	t.Run("no-syn-no-frame", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x11)
		end := b.send(b.gridTime(120), good...)
		b.run(tickAfter(end, 300, b))
		if len(b.writes) != 0 || len(b.logicalBytes(false)) != 0 || len(b.rawSymbols(false)) != len(good) {
			t.Fatalf("joined mid-stream: writes %v logical % X", b.writes, b.logicalBytes(false))
		}
	})
}

func TestAA25ResponseIdentityVariesEachField(t *testing.T) {
	// Each row varies one admission field of a CRC-valid request with the others
	// still matching. Mutation caught: dropping that field's comparison.
	offer := func(b *bench) { b.offer(1, engineTestPB, engineTestSB, 1, 0x11) }
	for _, tc := range []struct {
		name       string
		wire       []byte
		responds   bool
		provisions bool
	}{
		{"exact-control", testRequest(testResponder, engineTestPB, engineTestSB, 0x01), true, true},
		{"target", testRequest(testResponder+1, engineTestPB, engineTestSB, 0x01), false, false},
		{"pb", testRequest(testResponder, engineTestPB+1, engineTestSB, 0x01), false, true},
		{"sb", testRequest(testResponder, engineTestPB, engineTestSB+1, 0x01), false, true},
		{"nn", testRequest(testResponder, engineTestPB, engineTestSB, 0x01, 0x02), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, engineTestConfig())
			offer(b)
			end := b.sendRequest(tc.wire)
			b.run(tickAfter(end, 500, b))
			if len(b.eventsOfKind(EventFrameValid)) != 1 {
				t.Fatalf("control frame invalid: faults %v", b.faults())
			}
			if (len(b.writes) != 0) != tc.responds {
				t.Fatalf("writes %v; responds=%v", b.writes, tc.responds)
			}
			if (len(b.eventsOfKind(EventProvisional)) != 0) != tc.provisions {
				t.Fatalf("provisional mismatch: %v", b.eventsOfKind(EventProvisional))
			}
		})
	}
	t.Run("provisional-is-not-emission", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		offer(b)
		end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
		b.runUntilEvent(EventProvisional, tickAfter(end, 0, b))
		if len(b.writes) != 0 || len(b.eventsOfKind(EventAdmitted)) != 0 {
			t.Fatalf("destination time emitted: %v", b.writes)
		}
	})
}

func TestAA25DefaultEngineNeverTransmits(t *testing.T) {
	b := newBench(t, engineTestConfig())
	end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
	b.run(tickAfter(end, 500, b))
	if len(b.eventsOfKind(EventFrameValid)) != 1 {
		t.Fatal("control frame not validated")
	}
	if len(b.writes) != 0 || len(b.eventsOfKind(EventProvisional)) != 0 || b.engine.Stats().TXEdges != 0 {
		t.Fatalf("unprepared engine wrote %v", b.writes)
	}
}

func TestAA25GapIsRecordedNotSynthesized(t *testing.T) {
	build := func(t *testing.T) (*bench, []byte, *big.Rat) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x11)
		request := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
		return b, request, b.sendRequest(request)
	}
	t.Run("control-no-gap", func(t *testing.T) {
		b, _, end := build(t)
		b.run(tickAfter(end, 500, b))
		if len(b.eventsOfKind(EventTXComplete)) != 1 || len(b.eventsOfKind(EventGap)) != 0 {
			t.Fatalf("control faults %v", b.faults())
		}
	})
	t.Run("mid-frame", func(t *testing.T) {
		b, _, end := build(t)
		// Inside the second request character, sample 350 and six more are missed.
		b.run(b.gridDeadline(349))
		readsBefore := len(b.reads)
		jump := b.gridDeadline(356) + 5
		b.step(jump)
		if got := len(b.reads) - readsBefore; got != 1 {
			t.Fatalf("catch-up reads: %d", got)
		}
		if b.reads[len(b.reads)-1].tick != jump {
			t.Fatal("read not at the actual tick")
		}
		gaps := b.eventsOfKind(EventGap)
		if len(gaps) != 1 || gaps[0].SampleIndex != 350 || gaps[0].Missed != 6 || gaps[0].Tick != jump {
			t.Fatalf("gap %+v", gaps)
		}
		if next, want := b.engine.NextDeadlineTick(), b.gridDeadline(357); next != want {
			t.Fatalf("next %d; want absolute %d", next, want)
		}
		b.run(tickAfter(end, 500, b))
		if len(b.writes) != 0 || len(b.eventsOfKind(EventAdmitted)) != 0 {
			t.Fatalf("responded across a gap: %v", b.writes)
		}
		stats := b.engine.Stats()
		if stats.MissedSamples != 6 || stats.Withdrawals != 1 {
			t.Fatalf("stats %+v", stats)
		}
		// Withdrawal holds across a good frame until a new generation arrives.
		good := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
		end = b.send(b.idle(b.now), 0xAA)
		end = b.send(end, good...)
		b.run(tickAfter(end, 500, b))
		if len(b.writes) != 0 || len(b.eventsOfKind(EventFrameValid)) != 1 {
			t.Fatalf("withdrawal did not hold: %v", b.writes)
		}
		b.offer(2, engineTestPB, engineTestSB, 1, 0x11)
		end = b.send(b.idle(b.now), 0xAA)
		end = b.send(end, good...)
		b.run(tickAfter(end, 500, b))
		complete := b.eventsOfKind(EventTXComplete)
		if len(complete) != 1 || complete[0].Generation != 2 {
			t.Fatalf("recovery %+v faults %v", complete, b.faults())
		}
	})
	t.Run("between-escape-prefix-and-suffix", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x11)
		request := testRequest(testResponder, engineTestPB, engineTestSB, 0xA9)
		end := b.sendRequest(request)
		// SYN + 5 header symbols precede the prefix; the gap is in the prefix tail.
		prefixEnd := uint64(120 + 100*(1+5+1))
		b.run(b.gridDeadline(prefixEnd - 3))
		rawBefore := b.rawSymbols(false)
		if rawBefore[len(rawBefore)-1] != 0xA9 {
			t.Fatalf("prefix not observed: % X", rawBefore)
		}
		logicalBefore := len(b.logicalBytes(false))
		b.step(b.gridDeadline(prefixEnd + 1))
		b.run(tickAfter(end, 500, b))
		if len(b.logicalBytes(false)) != logicalBefore {
			t.Fatalf("decoded across a gap: % X", b.logicalBytes(false))
		}
		if len(b.eventsOfKind(EventGap)) != 1 || len(b.writes) != 0 {
			t.Fatalf("gap %v writes %v", b.eventsOfKind(EventGap), b.writes)
		}
	})
}

// startResponse runs to just before the first TX edge of a 0xAB payload.
func startResponse(t *testing.T, config EngineConfig) (*bench, Prepared, []testTransition) {
	t.Helper()
	b := newBench(t, config)
	prepared := b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
	end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
	b.runUntilEvent(EventAdmitted, tickAfter(end, 10, b))
	edges := b.expectedEdges(b.finalStart(), preparedWire(prepared))
	b.run(edges[0].tick - 1)
	return b, prepared, edges
}

func TestAA25LateTXFailsWithoutCatchUp(t *testing.T) {
	t.Run("expired-start-is-not-started", func(t *testing.T) {
		config := engineTestConfig()
		config.TurnaroundTicks = 1234
		b, _, edges := startResponse(t, config)
		b.step(edges[0].tick + 1)
		b.run(edges[len(edges)-1].tick + 1_000_000)
		if !b.hasFault(FaultTXLate) || len(b.writes) != 0 {
			t.Fatalf("faults %v writes %v", b.faults(), b.writes)
		}
		if b.engine.Stats().Withdrawals != 1 {
			t.Fatalf("admission retained: %+v", b.engine.Stats())
		}
	})
	t.Run("within-tolerance-keeps-absolute-schedule", func(t *testing.T) {
		config := engineTestConfig()
		config.TurnaroundTicks = 1234
		config.TXLateToleranceTicks = 1
		b, _, edges := startResponse(t, config)
		b.step(edges[0].tick + 1)
		b.run(edges[len(edges)-1].tick + 1_000_000)
		want := append([]testTransition{{edges[0].tick + 1, LevelLow}}, edges[1:]...)
		if !equalEdges(b.writes, want) {
			t.Fatalf("edges %v; want %v faults %v", b.writes, want, b.faults())
		}
		if intents := b.eventsOfKind(EventTXIntent); intents[0].Deadline != edges[0].tick || intents[0].Tick != edges[0].tick+1 {
			t.Fatalf("intent lost the deadline: %+v", intents[0])
		}
	})
	t.Run("mid-response-releases-once", func(t *testing.T) {
		config := engineTestConfig()
		config.TurnaroundTicks = 1234
		b, _, edges := startResponse(t, config)
		// The ACK symbol is all low: the second edge is its stop-bit rise.
		b.run(edges[1].tick - 1)
		if len(b.writes) != 1 {
			t.Fatalf("writes %v", b.writes)
		}
		lateTick := edges[1].tick + 1
		b.step(lateTick)
		b.run(edges[len(edges)-1].tick + 1_000_000)
		want := []testTransition{edges[0], {lateTick, LevelHigh}}
		if !equalEdges(b.writes, want) {
			t.Fatalf("edges %v; want %v", b.writes, want)
		}
		if !b.hasFault(FaultTXLate) || len(b.eventsOfKind(EventTXRelease)) != 1 || len(b.eventsOfKind(EventTXComplete)) != 0 {
			t.Fatalf("faults %v", b.faults())
		}
	})
	t.Run("long-stall-compresses-nothing", func(t *testing.T) {
		config := engineTestConfig()
		config.TurnaroundTicks = 1234
		b, _, edges := startResponse(t, config)
		b.run(edges[0].tick)
		stall := edges[len(edges)-1].tick + 1
		b.step(stall)
		for index := 0; index < 50; index++ {
			b.step(stall + uint64(index))
		}
		b.run(stall + 1_000_000)
		want := []testTransition{edges[0], {stall, LevelHigh}}
		if !equalEdges(b.writes, want) {
			t.Fatalf("edges %v; want %v", b.writes, want)
		}
	})
}

func TestAA25EchoIsObservedNeverInferred(t *testing.T) {
	config := engineTestConfig()
	config.TurnaroundTicks = 1234
	t.Run("no-echo-is-a-fault", func(t *testing.T) {
		b, _, edges := startResponse(t, config)
		b.disconnected = true
		b.run(edges[len(edges)-1].tick + 1_000_000)
		if !b.hasFault(FaultEchoMissing) {
			t.Fatalf("faults %v", b.faults())
		}
		if len(b.rawSymbols(true)) != 0 || b.engine.Stats().EchoedSymbols != 0 {
			t.Fatalf("echo inferred from intent: % X", b.rawSymbols(true))
		}
		if len(b.eventsOfKind(EventTXIntent)) == 0 || len(b.eventsOfKind(EventTXComplete)) != 0 {
			t.Fatal("intent or completion wrong")
		}
		// The ACK's first start edge happened; the second character was never started.
		if len(b.writes) > 3 {
			t.Fatalf("continued without echo: %v", b.writes)
		}
	})
	t.Run("collision-on-data-bit", func(t *testing.T) {
		b, prepared, edges := startResponse(t, config)
		// The second wire symbol is NN=1 (bit 0 high): another driver holds it low.
		if preparedWire(prepared)[1] != 0x01 {
			t.Fatalf("wire % X", preparedWire(prepared))
		}
		secondStart := edges[2].tick
		bit := rat(engineTestHz, 2400)
		b.external = append(b.external,
			testTransition{secondStart, LevelLow},
			testTransition{secondStart + 3*ceil(bit), LevelHigh})
		b.run(edges[len(edges)-1].tick + 1_000_000)
		if !b.hasFault(FaultEchoMismatch) {
			t.Fatalf("faults %v", b.faults())
		}
		stats := b.engine.Stats()
		if stats.EchoedSymbols != 1 || stats.Withdrawals != 1 || len(b.eventsOfKind(EventTXComplete)) != 0 {
			t.Fatalf("stats %+v", stats)
		}
		// Line released once at the mismatch and no later scheduled edge runs.
		writes := b.writes
		if writes[len(writes)-1].level != LevelHigh || writes[len(writes)-1].tick >= edges[len(edges)-1].tick {
			t.Fatalf("writes %v", writes)
		}
	})
	t.Run("matching-levels-remain-observations", func(t *testing.T) {
		// Control: an identical second driver is indistinguishable by level.
		b, prepared, edges := startResponse(t, config)
		b.external = append(b.external, edges...)
		b.run(edges[len(edges)-1].tick + 1_000_000)
		if !equalBytes(b.rawSymbols(true), preparedWire(prepared)) || len(b.faults()) != 0 {
			t.Fatalf("raw % X faults %v", b.rawSymbols(true), b.faults())
		}
	})
	t.Run("unsolicited-start-while-transmitting", func(t *testing.T) {
		b, _, edges := startResponse(t, config)
		// In the stop tail of the ACK echo, before the next character is started.
		tail := edges[0].tick + 97*ceil(rat(engineTestHz, 24000))
		b.external = append(b.external,
			testTransition{tail, LevelLow},
			testTransition{tail + ceil(rat(engineTestHz, 24000)), LevelHigh})
		b.run(edges[len(edges)-1].tick + 1_000_000)
		if !b.hasFault(FaultEchoUnsolicited) || len(b.writes) != 2 {
			t.Fatalf("faults %v writes %v", b.faults(), b.writes)
		}
	})
	t.Run("another-start-before-response-yields", func(t *testing.T) {
		for _, lead := range []uint64{1, 5, 9} {
			local := config
			local.TurnaroundTicks = 416_667
			_, _, edges := startResponse(t, local)
			start := edges[0].tick - lead*ceil(rat(engineTestHz, 24000))
			// startResponse ran past this time, so rebuild on a fresh bench.
			b := newBench(t, local)
			b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
			end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
			b.external = append(b.external, testTransition{start, LevelLow}, testTransition{start + 10*ceil(rat(engineTestHz, 2400)), LevelHigh})
			b.run(tickAfter(end, 500, b))
			if !b.hasFault(FaultRXBeforeResponse) || len(b.writes) != 0 {
				t.Fatalf("lead %d faults %v writes %v", lead, b.faults(), b.writes)
			}
		}
	})
	t.Run("start-in-final-stop-tail-yields", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
		request := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
		end := b.sendRequest(request)
		// Offset 98 of the final character: after stop validation, before its end.
		final := uint64(120 + 100*len(request))
		b.external = append(b.external, testTransition{b.gridDeadline(final + 98), LevelLow}, testTransition{b.gridDeadline(final + 198), LevelHigh})
		b.run(tickAfter(end, 500, b))
		if !b.hasFault(FaultRXBeforeResponse) || len(b.writes) != 0 {
			t.Fatalf("faults %v writes %v", b.faults(), b.writes)
		}
	})
	t.Run("start-at-the-emission-tick-vetoes-it", func(t *testing.T) {
		// Zero turnaround: the deadline tick is also a sample tick. Observation
		// runs first, so the other start wins and nothing is driven.
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
		request := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
		end := b.sendRequest(request)
		b.send(end, 0x00)
		b.run(tickAfter(end, 300, b))
		if !b.hasFault(FaultRXBeforeResponse) || len(b.writes) != 0 {
			t.Fatalf("faults %v writes %v", b.faults(), b.writes)
		}
	})
	t.Run("collision-on-stop-bit", func(t *testing.T) {
		b, _, edges := startResponse(t, config)
		// Another driver holds the ACK's stop bit low; the next character never starts.
		bit := ceil(rat(engineTestHz, 2400))
		b.external = append(b.external,
			testTransition{edges[1].tick, LevelLow},
			testTransition{edges[1].tick + bit, LevelHigh})
		b.run(edges[len(edges)-1].tick + 1_000_000)
		if !b.hasFault(FaultBadStop) || !b.hasFault(FaultEchoMismatch) || len(b.writes) != 2 {
			t.Fatalf("faults %v writes %v", b.faults(), b.writes)
		}
		if b.engine.Stats().EchoedSymbols != 0 {
			t.Fatalf("stats %+v", b.engine.Stats())
		}
	})
	t.Run("propagation-within-allowance", func(t *testing.T) {
		local := config
		local.EchoAllowanceTicks = 12 * ceil(rat(engineTestHz, 24000))
		b, prepared, edges := startResponse(t, local)
		b.propagation = 8 * ceil(rat(engineTestHz, 24000))
		b.run(edges[len(edges)-1].tick + 2_000_000)
		if !equalEdges(b.writes, edges) || len(b.eventsOfKind(EventTXComplete)) != 1 {
			t.Fatalf("writes %v faults %v", b.writes, b.faults())
		}
		if !equalBytes(b.rawSymbols(true), preparedWire(prepared)) {
			t.Fatalf("echo % X", b.rawSymbols(true))
		}
		complete := b.eventsOfKind(EventTXComplete)[0]
		final := b.eventsOfKind(EventRawSymbol)
		if complete.Tick < final[len(final)-1].Tick {
			t.Fatal("completion preceded the observed final echo")
		}
	})
	t.Run("propagation-beyond-allowance", func(t *testing.T) {
		b, _, edges := startResponse(t, config)
		b.propagation = 8 * ceil(rat(engineTestHz, 24000))
		b.run(edges[len(edges)-1].tick + 2_000_000)
		if !b.hasFault(FaultEchoMissing) {
			t.Fatalf("faults %v", b.faults())
		}
	})
}

func TestAA25HousekeepingTakesFixedSteps(t *testing.T) {
	t.Run("one-command-per-step", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		for index := 0; index < controlCapacity; index++ {
			if !b.engine.OfferWithdraw() {
				t.Fatalf("queue rejected command %d", index)
			}
		}
		if b.engine.OfferWithdraw() {
			t.Fatal("queue grew past capacity")
		}
		for index := 0; index < controlCapacity; index++ {
			readsBefore := len(b.reads)
			b.step(b.gridDeadline(uint64(index)))
			if got := b.engine.Stats().ControlsApplied; got != uint64(index+1) {
				t.Fatalf("step %d applied %d commands", index, got)
			}
			if len(b.reads) != readsBefore+1 {
				t.Fatalf("housekeeping displaced the sample at step %d", index)
			}
		}
		if !b.engine.OfferWithdraw() {
			t.Fatal("queue did not free capacity")
		}
	})
	t.Run("stale-generation-is-refused", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(5, engineTestPB, engineTestSB, 1, 0x05)
		b.offer(5, engineTestPB, engineTestSB, 1, 0x06)
		b.offer(4, engineTestPB, engineTestSB, 1, 0x07)
		end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
		b.run(tickAfter(end, 500, b))
		stale := 0
		for _, fault := range b.faults() {
			if fault == FaultStaleGeneration {
				stale++
			}
		}
		echo := b.rawSymbols(true)
		if stale != 2 || len(echo) < 3 || echo[2] != 0x05 {
			t.Fatalf("stale=%d echo % X", stale, echo)
		}
	})
	t.Run("replacement-does-not-change-pinned-generation", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x05)
		request := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
		end := b.sendRequest(request)
		b.runUntilEvent(EventProvisional, tickAfter(end, 0, b))
		b.offer(2, engineTestPB, engineTestSB, 1, 0x06)
		b.run(tickAfter(end, 500, b))
		complete := b.eventsOfKind(EventTXComplete)
		echo := b.rawSymbols(true)
		if len(complete) != 1 || complete[0].Generation != 1 || echo[2] != 0x05 {
			t.Fatalf("complete %+v echo % X", complete, echo)
		}
		end = b.send(b.idle(b.now), 0xAA)
		end = b.send(end, request...)
		b.run(tickAfter(end, 500, b))
		complete = b.eventsOfKind(EventTXComplete)
		if len(complete) != 2 || complete[1].Generation != 2 {
			t.Fatalf("next generation %+v", complete)
		}
	})
	request := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
	t.Run("withdraw-after-destination-silences", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x05)
		end := b.sendRequest(request)
		b.runUntilEvent(EventProvisional, tickAfter(end, 0, b))
		b.engine.OfferWithdraw()
		b.run(tickAfter(end, 500, b))
		if len(b.writes) != 0 || len(b.eventsOfKind(EventFrameValid)) != 1 {
			t.Fatalf("writes %v", b.writes)
		}
	})
	t.Run("withdraw-before-first-edge-silences", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x05)
		end := b.sendRequest(request)
		b.runUntilEvent(EventAdmitted, tickAfter(end, 10, b))
		b.engine.OfferWithdraw()
		b.run(tickAfter(end, 500, b))
		if len(b.writes) != 0 {
			t.Fatalf("writes %v", b.writes)
		}
	})
	t.Run("withdraw-on-wire-finishes-then-silences", func(t *testing.T) {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 1, 0x05)
		end := b.sendRequest(request)
		b.runUntilEvent(EventTXIntent, tickAfter(end, 10, b))
		b.engine.OfferWithdraw()
		b.run(tickAfter(end, 500, b))
		if len(b.eventsOfKind(EventTXComplete)) != 1 {
			t.Fatalf("on-wire response did not finish: faults %v", b.faults())
		}
		writes := len(b.writes)
		end = b.send(b.idle(b.now), 0xAA)
		end = b.send(end, request...)
		b.run(tickAfter(end, 500, b))
		if len(b.writes) != writes {
			t.Fatal("responded after withdrawal")
		}
	})
}

func TestAA25SinkLossInvalidatesWithoutRetryLoop(t *testing.T) {
	b := newBench(t, engineTestConfig())
	b.offer(1, engineTestPB, engineTestSB, 1, 0x05)
	request := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
	end := b.sendRequest(request)
	b.runUntilEvent(EventProvisional, tickAfter(end, 0, b))
	// The sink refuses everything: one protocol record is lost.
	b.sinkLimited = true
	b.sinkFree = 0
	b.run(b.now + 200*ceil(b.samplePeriod()))
	stats := b.engine.Stats()
	if stats.EventsDropped == 0 || stats.Withdrawals != 1 {
		t.Fatalf("stats %+v", stats)
	}
	b.sinkLimited = false
	b.run(tickAfter(end, 500, b))
	if len(b.writes) != 0 || len(b.eventsOfKind(EventAdmitted)) != 0 {
		t.Fatalf("responded after loss: %v", b.writes)
	}
	loss := b.eventsOfKind(EventLoss)
	if len(loss) != 1 || loss[0].Missed != stats.EventsDropped {
		t.Fatalf("loss marker %+v; stats %+v", loss, stats)
	}
}

func TestAA25ReofferDuringPersistentSinkLossStaysSilent(t *testing.T) {
	b := newBench(t, engineTestConfig())
	b.offer(1, engineTestPB, engineTestSB, 1, 0x05)
	request := testRequest(testResponder, engineTestPB, engineTestSB, 0x01)
	end := b.sendRequest(request)
	b.runUntilEvent(EventProvisional, tickAfter(end, 0, b))
	b.sinkLimited = true
	b.sinkFree = 0
	b.run(tickAfter(end, 500, b))
	// A newer generation is offered while the sink is still refusing events.
	b.offer(2, engineTestPB, engineTestSB, 1, 0x06)
	b.run(b.now + 200*ceil(b.samplePeriod()))
	end = b.send(b.idle(b.now), 0xAA)
	end = b.send(end, request...)
	b.run(tickAfter(end, 500, b))
	if len(b.writes) != 0 {
		t.Fatalf("responded during persistent loss: %v", b.writes)
	}
	// Control: once the sink recovers, the repeated offer is admitted.
	b.sinkLimited = false
	b.run(b.now + 200*ceil(b.samplePeriod()))
	b.offer(2, engineTestPB, engineTestSB, 1, 0x06)
	b.run(b.now + 200*ceil(b.samplePeriod()))
	end = b.send(b.idle(b.now), 0xAA)
	end = b.send(end, request...)
	b.run(tickAfter(end, 500, b))
	if len(b.eventsOfKind(EventTXComplete)) != 1 {
		t.Fatalf("recovered offer not admitted: faults %v", b.faults())
	}
}

func TestAA25TickRegressionFailsClosed(t *testing.T) {
	b := newBench(t, engineTestConfig())
	b.offer(1, engineTestPB, engineTestSB, 1, 0x05)
	b.run(b.gridDeadline(10))
	reads := len(b.reads)
	tick := b.now
	b.step(tick - 10)
	b.step(tick - 5)
	if len(b.reads) != reads {
		t.Fatal("sampled on a regressed clock")
	}
	count := 0
	for _, fault := range b.faults() {
		if fault == FaultTickRegression {
			count++
		}
	}
	if count != 1 || b.engine.Stats().Withdrawals != 1 {
		t.Fatalf("regressions %d stats %+v", count, b.engine.Stats())
	}
	end := b.send(b.idle(tick), 0xAA)
	end = b.send(end, testRequest(testResponder, engineTestPB, engineTestSB, 0x01)...)
	b.run(tickAfter(end, 500, b))
	if len(b.eventsOfKind(EventFrameValid)) != 1 || len(b.writes) != 0 {
		t.Fatalf("after regression: valid %d writes %v", len(b.eventsOfKind(EventFrameValid)), b.writes)
	}
}

func TestAA25ConfigScheduleOverflow(t *testing.T) {
	bench := &bench{now: testAnchor}
	ports := Ports{Levels: bench, Edges: bench, Ticks: bench, Sink: bench}
	config := engineTestConfig()
	config.TickHz = ^uint64(0)
	config.TurnaroundTicks = config.TickHz
	if _, err := NewEngine(config, ports); err != ErrEngineInvalidConfig {
		t.Fatalf("overflowing turnaround accepted: %v", err)
	}
	// Compute the inclusive response horizon independently with arbitrary precision.
	horizon := new(big.Int).SetUint64(config.TickHz)
	horizon.Mul(horizon, big.NewInt(380))
	horizon.Add(horizon, big.NewInt(2399))
	horizon.Quo(horizon, big.NewInt(2400))
	config.EchoAllowanceTicks = 1
	config.TXLateToleranceTicks = 1
	config.TurnaroundTicks = ^uint64(0) - testAnchor - horizon.Uint64() - 2
	if _, err := NewEngine(config, ports); err != nil {
		t.Fatalf("representable boundary rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*EngineConfig)
	}{
		{"turnaround", func(value *EngineConfig) { value.TurnaroundTicks++ }},
		{"echoAllowance", func(value *EngineConfig) { value.EchoAllowanceTicks++ }},
		{"lateTolerance", func(value *EngineConfig) { value.TXLateToleranceTicks++ }},
		{"anchor", func(value *EngineConfig) { bench.now++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bench.now = testAnchor
			value := config
			tc.mutate(&value)
			if _, err := NewEngine(value, ports); err != ErrEngineInvalidConfig {
				t.Fatalf("overflowing schedule accepted: %v", err)
			}
		})
	}
	// A reserve sum that wraps the 64-bit counter would look small; each addition
	// must report its own carry, not only the first one.
	wrap := config
	wrap.EchoAllowanceTicks = 1
	wrap.TXLateToleranceTicks = 0
	wrap.TurnaroundTicks = ^uint64(0) - horizon.Uint64()
	bench.now = testAnchor
	if _, err := NewEngine(wrap, ports); err != ErrEngineInvalidConfig {
		t.Fatalf("echo allowance wrap accepted: %v", err)
	}
	wrap.TXLateToleranceTicks = 1
	wrap.TurnaroundTicks--
	if _, err := NewEngine(wrap, ports); err != ErrEngineInvalidConfig {
		t.Fatalf("late tolerance wrap accepted: %v", err)
	}
}

func TestAA25ResponseAnchorOverflow(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, overflow := range []bool{false, true} {
			b := newBench(t, engineTestConfig())
			prepared, err := PrepareResponse(1, engineTestPB, engineTestSB, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			engine := b.engine
			engine.pinned = prepared
			engine.pinnedValid = true
			engine.framePB = engineTestPB
			engine.frameSB = engineTestSB
			// At the nominal test rate the maximum horizon is ceil(380e9/2400).
			engine.charStartTick = ^uint64(0) - 158_333_334
			if overflow {
				engine.charStartTick++
			}
			now := engine.charStartTick
			if retry {
				engine.txState = txAwaitAck
				engine.ackSince = now - engineTestHz/5 + 1
				engine.handleFinalAck(engineSymbolNack, now)
			} else {
				engine.admitResponse(now)
			}
			if overflow {
				if engine.txState != txIdle || engine.pinnedValid ||
					len(b.eventsOfKind(EventAdmitted)) != 0 {
					t.Fatalf("overflow admitted, retry %v", retry)
				}
				faults := b.faults()
				if len(faults) != 1 || faults[0] != FaultTXLate {
					t.Fatalf("overflow faults %v, retry %v", faults, retry)
				}
			} else if engine.txState != txWaiting || !engine.pinnedValid {
				t.Fatalf("representable anchor rejected, retry %v", retry)
			}
			if len(b.writes) != 0 {
				t.Fatal("admission drove an edge")
			}
		}
	}
}

func TestAA25ConfigAndPreparedValidation(t *testing.T) {
	bench := &bench{now: testAnchor}
	ports := Ports{Levels: bench, Edges: bench, Ticks: bench, Sink: bench}
	if _, err := NewEngine(engineTestConfig(), ports); err != nil {
		t.Fatalf("control config: %v", err)
	}
	boundary := engineTestConfig()
	boundary.TurnaroundTicks = boundary.TickHz
	if _, err := NewEngine(boundary, ports); err != nil {
		t.Fatalf("turnaround boundary control: %v", err)
	}
	bitTicks := engineTestHz / 2400
	characterTicks := engineTestHz / 240
	for _, mutate := range []func(*EngineConfig){
		func(config *EngineConfig) { config.TickHz = 0 },
		func(config *EngineConfig) { config.TickHz = 23999 },
		func(config *EngineConfig) { config.ResponderAddress = 0xA9 },
		func(config *EngineConfig) { config.ResponderAddress = 0xAA },
		func(config *EngineConfig) { config.ResponderAddress = 0xFE },
		func(config *EngineConfig) { config.ResponderAddress = 0x31 },
		func(config *EngineConfig) { config.ResponderAddress = 0x10 },
		func(config *EngineConfig) { config.TXLateToleranceTicks = bitTicks },
		func(config *EngineConfig) { config.EchoAllowanceTicks = characterTicks },
		func(config *EngineConfig) { config.TurnaroundTicks = config.TickHz + 1 },
		func(config *EngineConfig) {
			config.TickHz = 24_000_000
			config.TurnaroundTicks = ^uint64(0) - 100_000
		},
	} {
		config := engineTestConfig()
		mutate(&config)
		if _, err := NewEngine(config, ports); err != ErrEngineInvalidConfig {
			t.Fatalf("config %+v: %v", config, err)
		}
	}
	for _, mutate := range []func(*Ports){
		func(value *Ports) { value.Levels = nil },
		func(value *Ports) { value.Edges = nil },
		func(value *Ports) { value.Ticks = nil },
		func(value *Ports) { value.Sink = nil },
	} {
		value := ports
		mutate(&value)
		if _, err := NewEngine(engineTestConfig(), value); err != ErrMissingPort {
			t.Fatalf("ports: %v", err)
		}
	}
	if _, err := PrepareResponse(0, 0, 0, 0, nil); err != ErrInvalidResponse {
		t.Fatalf("generation zero: %v", err)
	}
	if _, err := PrepareResponse(1, 0, 0, 0, make([]byte, maxPreparedPayload+1)); err != ErrInvalidResponse {
		t.Fatalf("oversize payload: %v", err)
	}
	// The worst-case payload fits the fixed wire capacity in escaped form.
	worst := make([]byte, maxPreparedPayload)
	for index := range worst {
		worst[index] = 0xAA
	}
	prepared, err := PrepareResponse(1, 0, 0, 0, worst)
	if err != nil || int(prepared.wireLen) > maxPreparedWire {
		t.Fatalf("worst case: %v len %d", err, prepared.wireLen)
	}
}

func TestAA25TimingPathDoesNotAllocate(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		b := newBench(t, engineTestConfig())
		b.offer(1, engineTestPB, engineTestSB, 2, 0xA9, 0xAA)
		end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0xA9, 0xAA))
		b.run(tickAfter(end, 0, b))
		b.disconnected = disconnected
		allocations := testing.AllocsPerRun(1200, func() {
			if next := b.engine.NextDeadlineTick(); next > b.now {
				b.now = next
			}
			b.engine.Step()
		})
		if allocations != 0 {
			t.Fatalf("disconnected=%v allocations per step: %v", disconnected, allocations)
		}
		if b.engine.Stats().TXEdges == 0 {
			t.Fatal("allocation window did not cover transmission")
		}
	}
}

func engineParseSource(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func TestAA25TimingPathStaticShape(t *testing.T) {
	file := engineParseSource(t, "engine.go")
	for _, importSpec := range file.Imports {
		if path := importSpec.Path.Value; path != `"errors"` && path != `"math/bits"` {
			t.Fatalf("forbidden import %s", path)
		}
	}
	allowed := map[string]bool{"engineCRCUpdate": true, "PrepareResponse": true}
	for _, decl := range file.Decls {
		funcDecl, isFunc := decl.(*ast.FuncDecl)
		name := ""
		if isFunc {
			name = funcDecl.Name.Name
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.GoStmt, *ast.DeferStmt, *ast.SelectStmt, *ast.SendStmt, *ast.ChanType, *ast.FuncLit, *ast.MapType:
				t.Fatalf("forbidden construct %T in %s", value, name)
			case *ast.ForStmt, *ast.RangeStmt:
				// Only the eight-bit CRC step and the preparation path may iterate.
				if !allowed[name] {
					t.Fatalf("loop in timing path function %s", name)
				}
			case *ast.CallExpr:
				if ident, ok := value.Fun.(*ast.Ident); ok {
					switch ident.Name {
					case "append", "make", "new", "panic", "print", "println":
						t.Fatalf("forbidden call %s in %s", ident.Name, name)
					}
				}
			}
			return true
		})
	}
}

// runUntilCompletes polls on the engine's own deadlines until the given
// number of TXComplete events exists, so no later input is skipped past.
func (b *bench) runUntilCompletes(count int, limit uint64) {
	b.t.Helper()
	for guard := 0; guard < 1<<22; guard++ {
		if len(b.eventsOfKind(EventTXComplete)) >= count {
			return
		}
		next := b.engine.NextDeadlineTick()
		if next > limit {
			b.t.Fatalf("completes never reached %d; faults %v", count, b.faults())
		}
		if next > b.now {
			b.now = next
		}
		b.engine.Step()
	}
	b.t.Fatal("bench made no progress")
}

// queueFinalSymbol queues one initiator symbol a fixed gap after the
// latest TXComplete and returns its end.
func (b *bench) queueFinalSymbol(symbol byte, completes int) *big.Rat {
	b.t.Helper()
	complete := b.eventsOfKind(EventTXComplete)
	if len(complete) != completes {
		b.t.Fatalf("completes %d; want %d (faults %v)", len(complete), completes, b.faults())
	}
	return b.send(rat(complete[completes-1].Tick+b.hz/2400*2, 1), symbol)
}

func TestAA25FinalNackRetransmitsResponseOnce(t *testing.T) {
	// Each row follows an identical admitted exchange with one initiator symbol.
	// Mutation caught: dropping the NACK pin (no retransmit), retransmitting the
	// leading ACK too, anchoring the resend to the old request, or looping.
	for _, tc := range []struct {
		name    string
		symbols []byte
		resends int
		fault   bool
	}{
		{"ack-control", []byte{0x00}, 0, false},
		{"nack-once", []byte{0xFF}, 1, false},
		{"nack-then-ack", []byte{0xFF, 0x00}, 1, false},
		{"nack-twice", []byte{0xFF, 0xFF}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, engineTestConfig())
			prepared := b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
			wire := preparedWire(prepared)
			end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
			b.runUntilCompletes(1, tickAfter(end, 2000, b))
			want := b.expectedEdges(b.finalStart(), wire)
			for index, symbol := range tc.symbols {
				symbolEnd := b.queueFinalSymbol(symbol, index+1)
				if symbol == 0xFF && index < tc.resends {
					b.runUntilCompletes(index+2, tickAfter(symbolEnd, 2000, b))
					resend := b.expectedEdges(b.finalStart(), wire[1:])
					want = append(want, resend...)
				} else {
					b.run(tickAfter(symbolEnd, 700, b))
				}
			}
			if !equalEdges(b.writes, want) {
				t.Fatalf("edges %v; want %v (faults %v)", b.writes, want, b.faults())
			}
			if b.hasFault(FaultNackExhausted) != tc.fault {
				t.Fatalf("faults %v; exhausted want %v", b.faults(), tc.fault)
			}
			if got := len(b.eventsOfKind(EventTXComplete)); got != 1+tc.resends {
				t.Fatalf("completes %d; want %d", got, 1+tc.resends)
			}
			if tc.resends > 0 {
				if echo := b.rawSymbols(true); len(echo) != len(wire)+len(wire)-1 {
					t.Fatalf("echo % X", echo)
				}
			}
		})
	}
}

func TestAA25RejectsRequestLengthAboveProtocolMaximum(t *testing.T) {
	// The prepared entry matches NN=17, so only the length limit can stop the
	// response. Mutation caught: admitting NN above 16.
	for _, tc := range []struct {
		nn    byte
		admit bool
	}{{16, true}, {17, false}} {
		t.Run("nn"+string(rune('0'+tc.nn/10))+string(rune('0'+tc.nn%10)), func(t *testing.T) {
			b := newBench(t, engineTestConfig())
			b.offer(1, engineTestPB, engineTestSB, tc.nn, 0x11)
			data := make([]byte, tc.nn)
			end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, data...))
			b.run(tickAfter(end, 700, b))
			if (len(b.writes) != 0) != tc.admit {
				t.Fatalf("nn=%d writes %v faults %v", tc.nn, b.writes, b.faults())
			}
			if b.hasFault(FaultLengthInvalid) == tc.admit {
				t.Fatalf("nn=%d faults %v", tc.nn, b.faults())
			}
		})
	}
}

func TestAA25WithdrawalAfterCompleteDropsNackRetransmit(t *testing.T) {
	// Mutation caught: leaving the final-ACK wait active across a withdrawal, so
	// a later NACK resends a response whose admission was withdrawn.
	b := newBench(t, engineTestConfig())
	prepared := b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
	wire := preparedWire(prepared)
	end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
	b.runUntilCompletes(1, tickAfter(end, 2000, b))
	want := b.expectedEdges(b.finalStart(), wire)
	if !b.engine.OfferWithdraw() {
		t.Fatal("withdraw refused")
	}
	b.step(b.now + 1)
	symbolEnd := b.queueFinalSymbol(0xFF, 1)
	b.run(tickAfter(symbolEnd, 2000, b))
	if !equalEdges(b.writes, want) {
		t.Fatalf("edges %v; want %v", b.writes, want)
	}
	if len(b.eventsOfKind(EventTXComplete)) != 1 {
		t.Fatalf("completes %d; want 1", len(b.eventsOfKind(EventTXComplete)))
	}
}

func TestAA25FinalAckWaitExpiresBeforeLateNack(t *testing.T) {
	// Each row sends a NACK after the same completed response. Mutation caught:
	// an unbounded final-ACK wait that treats a stray 0xFF as a NACK.
	for _, tc := range []struct {
		name   string
		delay  uint64
		resend bool
	}{{"within-budget", 1000, true}, {"after-budget", 300_000_000, false}} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, engineTestConfig())
			b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
			end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
			b.runUntilCompletes(1, tickAfter(end, 2000, b))
			done := b.eventsOfKind(EventTXComplete)[0].Tick
			writes := len(b.writes)
			b.run(done + tc.delay)
			symbol := b.send(rat(b.now, 1), 0xFF)
			b.run(tickAfter(symbol, 2000, b))
			if resent := len(b.writes) > writes; resent != tc.resend {
				t.Fatalf("resent %v want %v (faults %v)", resent, tc.resend, b.faults())
			}
		})
	}
}

func TestAA25FinalSymbolAtResponseBoundaryIsNotUnsolicitedEcho(t *testing.T) {
	// The initiator starts its final symbol exactly where the response's last
	// stop bit ends. Mutation caught: judging that start before the fully echoed
	// response moves to the final-ACK wait, which faults it as unsolicited echo,
	// suppresses TXComplete, withdraws the response and drops the NACK resend.
	for _, tc := range []struct {
		name    string
		symbol  byte
		resends int
	}{{"ack", 0x00, 0}, {"nack", 0xFF, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			// An integral sample period puts the response end on the sample grid,
			// so the final symbol's first low sample and the completion deadline
			// fall in the same pass.
			config := engineTestConfig()
			config.TickHz = 24_000_000
			b := newBench(t, config)
			prepared := b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
			wire := preparedWire(prepared)
			end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
			b.runUntilEvent(EventAdmitted, tickAfter(end, 2000, b))
			start := b.finalStart()
			want := b.expectedEdges(start, wire)
			boundary := rat(start+b.config.TurnaroundTicks, 1)
			span := rat(10+10*uint64(len(wire)), 1)
			boundary.Add(boundary, span.Mul(span, rat(b.hz, 2400)))
			symbolEnd := b.send(boundary, tc.symbol)
			if tc.resends > 0 {
				b.runUntilCompletes(2, tickAfter(symbolEnd, 2000, b))
				want = append(want, b.expectedEdges(b.finalStart(), wire[1:])...)
			}
			b.run(tickAfter(symbolEnd, 2000, b))
			if b.hasFault(FaultEchoUnsolicited) {
				t.Fatalf("boundary symbol faulted as unsolicited echo: %v", b.faults())
			}
			if got := len(b.eventsOfKind(EventTXComplete)); got != 1+tc.resends {
				t.Fatalf("completes %d; want %d (faults %v)", got, 1+tc.resends, b.faults())
			}
			if !equalEdges(b.writes, want) {
				t.Fatalf("edges %v; want %v", b.writes, want)
			}
		})
	}
}

func TestAA25StartBeforeResponseEndIsUnsolicited(t *testing.T) {
	// The initiator's start is observed 0, 1 or 3 samples relative to the end of
	// the response's last character. Mutation caught: completing the response
	// from the start shortcut without requiring the completion deadline, which
	// publishes TXComplete early and accepts the early symbol as a final ACK.
	for _, tc := range []struct {
		name     string
		earlyBy  uint64
		unsolict bool
	}{{"control-at-boundary", 0, false}, {"one-sample-early", 1, true}, {"three-samples-early", 3, true}} {
		t.Run(tc.name, func(t *testing.T) {
			config := engineTestConfig()
			config.TickHz = 24_000_000
			b := newBench(t, config)
			prepared := b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
			wire := preparedWire(prepared)
			end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
			b.runUntilEvent(EventAdmitted, tickAfter(end, 2000, b))
			start := b.finalStart()
			boundary := rat(start+b.config.TurnaroundTicks, 1)
			span := rat(10+10*uint64(len(wire)), 1)
			boundary.Add(boundary, span.Mul(span, rat(b.hz, 2400)))
			boundary.Sub(boundary, rat(tc.earlyBy*(b.hz/24000), 1))
			symbolEnd := b.send(boundary, 0xFF)
			b.run(tickAfter(symbolEnd, 2000, b))
			if got := b.hasFault(FaultEchoUnsolicited); got != tc.unsolict {
				t.Fatalf("unsolicited fault %v want %v (faults %v)", got, tc.unsolict, b.faults())
			}
			completes := len(b.eventsOfKind(EventTXComplete))
			if tc.unsolict && completes != 0 {
				t.Fatalf("completes %d before the response end", completes)
			}
			if !tc.unsolict && completes != 2 {
				t.Fatalf("completes %d; want 2 (response then NACK resend)", completes)
			}
		})
	}
}

func TestAA25WithdrawalDuringTransmitCancelsNackRetransmit(t *testing.T) {
	// Withdrawal arrives while the response is on the wire; the response still
	// finishes. Mutation caught: letting completion enter the final-ACK wait
	// without retaining the cancellation, so a later NACK retransmits anyway.
	for _, tc := range []struct {
		name     string
		withdraw bool
	}{{"control-no-withdraw", false}, {"withdrawn", true}} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, engineTestConfig())
			prepared := b.offer(1, engineTestPB, engineTestSB, 1, 0xAB)
			wire := preparedWire(prepared)
			end := b.sendRequest(testRequest(testResponder, engineTestPB, engineTestSB, 0x01))
			b.runUntilEvent(EventTXIntent, tickAfter(end, 2000, b))
			if tc.withdraw {
				if !b.engine.OfferWithdraw() {
					t.Fatal("withdraw refused")
				}
				b.step(b.now)
			}
			b.runUntilCompletes(1, tickAfter(end, 4000, b))
			want := b.expectedEdges(b.finalStart(), wire)
			symbolEnd := b.queueFinalSymbol(0xFF, 1)
			b.run(tickAfter(symbolEnd, 2000, b))
			if resent := len(b.writes) > len(want); resent == tc.withdraw {
				t.Fatalf("resent %v with withdraw %v (faults %v)", resent, tc.withdraw, b.faults())
			}
			if tc.withdraw && !equalEdges(b.writes, want) {
				t.Fatalf("edges %v; want %v", b.writes, want)
			}
		})
	}
}

func TestAA25OfferRejectsUnpreparedResponse(t *testing.T) {
	b := newBench(t, engineTestConfig())
	if b.engine.OfferPrepared(Prepared{Generation: 1, MatchPB: engineTestPB, MatchSB: engineTestSB, MatchNN: 1}) {
		t.Fatal("hand-built prepared value was queued")
	}
	if b.engine.OfferPrepared(Prepared{}) {
		t.Fatal("zero prepared value was queued")
	}
	valid, err := PrepareResponse(1, engineTestPB, engineTestSB, 1, []byte{0xAB})
	if err != nil || !b.engine.OfferPrepared(valid) {
		t.Fatalf("prepared value rejected: %v", err)
	}
}

func TestAA25AckDeadlineSaturatesNearMaxTick(t *testing.T) {
	// Each row pins the ACK wait start near the top of the tick range at 1 GHz
	// (200 ms = 200_000_000 ticks). Mutation caught: an unchecked
	// AckSince+allowance that wraps and expires the wait immediately.
	const maxTick = ^uint64(0)
	for _, tc := range []struct {
		name  string
		since uint64
		want  uint64
	}{
		{"wraps-by-40ms", maxTick - 160_000_000, maxTick},
		{"exact-fit", maxTick - 200_000_000, maxTick},
		{"ordinary", 1000, 1000 + 200_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &Engine{config: engineTestConfig(), ackSince: tc.since}
			if got := engine.ackDeadline(); got != tc.want {
				t.Fatalf("deadline = %d, want %d", got, tc.want)
			}
			if tc.want == maxTick && engine.ackDeadline() <= tc.since {
				t.Fatalf("deadline not after wait start")
			}
		})
	}
}
