package localworker

import (
	aa25ast "go/ast"
	aa25parser "go/parser"
	aa25token "go/token"
	aa25big "math/big"
	aa25strings "strings"
	aa25testing "testing"
)

// Every trace below is synthetic and every tick is injected. Expected schedules
// are computed with math/big from the nominal definition, not with engine code.
const (
	aa25TestHz        uint64 = 1_000_000_000
	aa25TestAnchor    uint64 = 1000
	aa25TestSource    byte   = 0x10
	aa25TestResponder byte   = 0x15
	aa25TestPB        byte   = 0xB5
	aa25TestSB        byte   = 0x09
)

type aa25TestTransition struct {
	aa25Tick  uint64
	aa25Level AA25Level
}

type aa25Bench struct {
	aa25T            *aa25testing.T
	aa25Hz           uint64
	aa25Now          uint64
	aa25External     []aa25TestTransition
	aa25Cursor       int
	aa25ExternalNow  AA25Level
	aa25Writes       []aa25TestTransition
	aa25Reads        []aa25TestTransition
	aa25Events       []AA25Event
	aa25Propagation  uint64
	aa25Disconnected bool
	aa25SinkLimited  bool
	aa25SinkFree     int
	aa25Engine       *AA25Engine
	aa25Config       AA25Config
	aa25SendLevel    AA25Level
}

func (aa25Self *aa25Bench) AA25NowTicks() uint64 { return aa25Self.aa25Now }

func (aa25Self *aa25Bench) AA25SampleLevel() AA25Level {
	aa25Level := aa25Self.aa25LineLevel()
	aa25Self.aa25Reads = append(aa25Self.aa25Reads, aa25TestTransition{aa25Self.aa25Now, aa25Level})
	return aa25Level
}

func (aa25Self *aa25Bench) aa25LineLevel() AA25Level {
	for aa25Self.aa25Cursor < len(aa25Self.aa25External) && aa25Self.aa25External[aa25Self.aa25Cursor].aa25Tick <= aa25Self.aa25Now {
		aa25Self.aa25ExternalNow = aa25Self.aa25External[aa25Self.aa25Cursor].aa25Level
		aa25Self.aa25Cursor++
	}
	aa25Level := aa25Self.aa25ExternalNow
	if aa25Self.aa25Disconnected {
		return aa25Level
	}
	for aa25Index := len(aa25Self.aa25Writes) - 1; aa25Index >= 0; aa25Index-- {
		if aa25Self.aa25Writes[aa25Index].aa25Tick+aa25Self.aa25Propagation <= aa25Self.aa25Now {
			if aa25Self.aa25Writes[aa25Index].aa25Level == AA25LevelLow {
				return AA25LevelLow
			}
			break
		}
	}
	return aa25Level
}

func (aa25Self *aa25Bench) AA25WriteEdge(aa25Level AA25Level) {
	aa25Self.aa25Writes = append(aa25Self.aa25Writes, aa25TestTransition{aa25Self.aa25Now, aa25Level})
}

func (aa25Self *aa25Bench) AA25TryPublish(aa25Event AA25Event) bool {
	if aa25Self.aa25SinkLimited {
		if aa25Self.aa25SinkFree == 0 {
			return false
		}
		aa25Self.aa25SinkFree--
	}
	aa25Self.aa25Events = append(aa25Self.aa25Events, aa25Event)
	return true
}

func aa25TestConfig() AA25Config {
	return AA25Config{AA25TickHz: aa25TestHz, AA25ResponderAddress: aa25TestResponder}
}

func aa25NewBench(aa25T *aa25testing.T, aa25Config AA25Config) *aa25Bench {
	aa25T.Helper()
	aa25Self := &aa25Bench{
		aa25T: aa25T, aa25Hz: aa25Config.AA25TickHz, aa25Now: aa25TestAnchor, aa25Config: aa25Config,
		aa25ExternalNow: AA25LevelHigh, aa25SendLevel: AA25LevelHigh,
		aa25Reads:  make([]aa25TestTransition, 0, 1<<17),
		aa25Events: make([]AA25Event, 0, 1<<13),
		aa25Writes: make([]aa25TestTransition, 0, 1<<9),
	}
	aa25Engine, aa25Err := AA25NewEngine(aa25Config, AA25Ports{AA25Levels: aa25Self, AA25Edges: aa25Self, AA25Ticks: aa25Self, AA25Sink: aa25Self})
	if aa25Err != nil {
		aa25T.Fatalf("aa25 engine: %v", aa25Err)
	}
	aa25Self.aa25Engine = aa25Engine
	return aa25Self
}

func (aa25Self *aa25Bench) aa25Offer(aa25Generation uint64, aa25PB, aa25SB, aa25NN byte, aa25Payload ...byte) AA25Prepared {
	aa25Self.aa25T.Helper()
	aa25Prepared, aa25Err := AA25PrepareResponse(aa25Generation, aa25PB, aa25SB, aa25NN, aa25Payload)
	if aa25Err != nil {
		aa25Self.aa25T.Fatalf("aa25 prepare: %v", aa25Err)
	}
	if !aa25Self.aa25Engine.AA25OfferPrepared(aa25Prepared) {
		aa25Self.aa25T.Fatal("aa25 control queue full")
	}
	return aa25Prepared
}

// aa25Step advances the clock to an exact tick and performs one engine pass.
func (aa25Self *aa25Bench) aa25Step(aa25Tick uint64) {
	aa25Self.aa25Now = aa25Tick
	aa25Self.aa25Engine.AA25Step()
}

// aa25Run polls on the engine's own next deadline, so every step is on time.
func (aa25Self *aa25Bench) aa25Run(aa25Until uint64) {
	aa25Self.aa25T.Helper()
	for aa25Guard := 0; aa25Guard < 1<<22; aa25Guard++ {
		aa25Next := aa25Self.aa25Engine.AA25NextDeadlineTick()
		if aa25Next > aa25Until {
			return
		}
		if aa25Next > aa25Self.aa25Now {
			aa25Self.aa25Now = aa25Next
		}
		aa25Self.aa25Engine.AA25Step()
	}
	aa25Self.aa25T.Fatal("aa25 bench made no progress")
}

func (aa25Self *aa25Bench) aa25RunUntilEvent(aa25Kind AA25EventKind, aa25Limit uint64) {
	aa25Self.aa25T.Helper()
	for aa25Guard := 0; aa25Guard < 1<<22; aa25Guard++ {
		if len(aa25Self.aa25Select(aa25Kind)) > 0 {
			return
		}
		aa25Next := aa25Self.aa25Engine.AA25NextDeadlineTick()
		if aa25Next > aa25Limit {
			aa25Self.aa25T.Fatalf("aa25 event kind %d never occurred; events=%d faults=%v", aa25Kind, len(aa25Self.aa25Events), aa25Self.aa25Faults())
		}
		if aa25Next > aa25Self.aa25Now {
			aa25Self.aa25Now = aa25Next
		}
		aa25Self.aa25Engine.AA25Step()
	}
	aa25Self.aa25T.Fatal("aa25 bench made no progress")
}

func aa25Rat(aa25Num, aa25Den uint64) *aa25big.Rat {
	return new(aa25big.Rat).SetFrac(new(aa25big.Int).SetUint64(aa25Num), new(aa25big.Int).SetUint64(aa25Den))
}

func aa25Ceil(aa25Value *aa25big.Rat) uint64 {
	aa25Quotient, aa25Remainder := new(aa25big.Int).QuoRem(aa25Value.Num(), aa25Value.Denom(), new(aa25big.Int))
	if aa25Remainder.Sign() != 0 {
		aa25Quotient.Add(aa25Quotient, aa25big.NewInt(1))
	}
	return aa25Quotient.Uint64()
}

func (aa25Self *aa25Bench) aa25SamplePeriod() *aa25big.Rat {
	return aa25Rat(aa25Self.aa25Hz, 24000)
}

func (aa25Self *aa25Bench) aa25GridTime(aa25Samples uint64) *aa25big.Rat {
	aa25Value := new(aa25big.Rat).Mul(aa25Rat(aa25Samples, 1), aa25Self.aa25SamplePeriod())
	return aa25Value.Add(aa25Value, aa25Rat(aa25TestAnchor, 1))
}

// aa25GridDeadline is the independent definition: the first whole tick at or
// after anchor + n/24000 seconds.
func (aa25Self *aa25Bench) aa25GridDeadline(aa25Samples uint64) uint64 {
	return aa25Ceil(aa25Self.aa25GridTime(aa25Samples))
}

func (aa25Self *aa25Bench) aa25OnGrid(aa25Tick uint64) bool {
	aa25Elapsed := aa25Rat(aa25Tick-aa25TestAnchor, 1)
	aa25Elapsed.Quo(aa25Elapsed, aa25Self.aa25SamplePeriod())
	aa25Floor := new(aa25big.Int).Quo(aa25Elapsed.Num(), aa25Elapsed.Denom()).Uint64()
	return aa25Self.aa25GridDeadline(aa25Floor) == aa25Tick || aa25Self.aa25GridDeadline(aa25Floor+1) == aa25Tick
}

func (aa25Self *aa25Bench) aa25AddTransition(aa25Time *aa25big.Rat, aa25Level AA25Level) {
	if aa25Level == aa25Self.aa25SendLevel {
		return
	}
	aa25Self.aa25SendLevel = aa25Level
	aa25Self.aa25External = append(aa25Self.aa25External, aa25TestTransition{aa25Ceil(aa25Time), aa25Level})
}

// aa25SendCharacter appends one character with an explicit stop level and a
// sender bit period of aa25Scale nominal bits; it returns the character's end.
func (aa25Self *aa25Bench) aa25SendCharacter(aa25Start *aa25big.Rat, aa25Scale *aa25big.Rat, aa25Symbol byte, aa25Stop AA25Level) *aa25big.Rat {
	aa25Bit := new(aa25big.Rat).Mul(aa25Rat(aa25Self.aa25Hz, 2400), aa25Scale)
	aa25Boundary := func(aa25K uint64) *aa25big.Rat {
		aa25Value := new(aa25big.Rat).Mul(aa25Rat(aa25K, 1), aa25Bit)
		return aa25Value.Add(aa25Value, aa25Start)
	}
	aa25Self.aa25AddTransition(aa25Boundary(0), AA25LevelLow)
	for aa25K := uint64(0); aa25K < 8; aa25K++ {
		aa25Self.aa25AddTransition(aa25Boundary(aa25K+1), AA25Level(aa25Symbol>>aa25K&1))
	}
	aa25Self.aa25AddTransition(aa25Boundary(9), aa25Stop)
	aa25End := aa25Boundary(10)
	aa25Self.aa25AddTransition(aa25End, AA25LevelHigh)
	return aa25End
}

// aa25Send appends contiguous nominal characters and returns their end.
func (aa25Self *aa25Bench) aa25Send(aa25Start *aa25big.Rat, aa25Symbols ...byte) *aa25big.Rat {
	for _, aa25Symbol := range aa25Symbols {
		aa25Start = aa25Self.aa25SendCharacter(aa25Start, aa25Rat(1, 1), aa25Symbol, AA25LevelHigh)
	}
	return aa25Start
}

func aa25TestExpand(aa25Logical []byte) []byte {
	aa25Wire := make([]byte, 0, 2*len(aa25Logical))
	for _, aa25Value := range aa25Logical {
		switch aa25Value {
		case 0xA9:
			aa25Wire = append(aa25Wire, 0xA9, 0x00)
		case 0xAA:
			aa25Wire = append(aa25Wire, 0xA9, 0x01)
		default:
			aa25Wire = append(aa25Wire, aa25Value)
		}
	}
	return aa25Wire
}

// aa25TestCRC multiplies by x^8 modulo x^8+0x9B over GF(2) one symbol at a time.
// TestAA25CRCVectors pins it to literal vectors.
func aa25TestCRC(aa25Logical []byte) byte {
	aa25Remainder := uint16(0)
	for _, aa25Symbol := range aa25TestExpand(aa25Logical) {
		aa25Value := aa25Remainder << 8
		for aa25Bit := 15; aa25Bit >= 8; aa25Bit-- {
			if aa25Value&(1<<aa25Bit) != 0 {
				aa25Value ^= 0x19B << (aa25Bit - 8)
			}
		}
		aa25Remainder = aa25Value ^ uint16(aa25Symbol)
	}
	return byte(aa25Remainder)
}

func aa25TestRequest(aa25Target, aa25PB, aa25SB byte, aa25Data ...byte) []byte {
	aa25Logical := append([]byte{aa25TestSource, aa25Target, aa25PB, aa25SB, byte(len(aa25Data))}, aa25Data...)
	aa25Logical = append(aa25Logical, aa25TestCRC(aa25Logical))
	return aa25TestExpand(aa25Logical)
}

func (aa25Self *aa25Bench) aa25Select(aa25Kind AA25EventKind) []AA25Event {
	var aa25Result []AA25Event
	for _, aa25Event := range aa25Self.aa25Events {
		if aa25Event.AA25Kind == aa25Kind {
			aa25Result = append(aa25Result, aa25Event)
		}
	}
	return aa25Result
}

func (aa25Self *aa25Bench) aa25Faults() []AA25Fault {
	var aa25Result []AA25Fault
	for _, aa25Event := range aa25Self.aa25Select(AA25EventFault) {
		aa25Result = append(aa25Result, aa25Event.AA25Fault)
	}
	return aa25Result
}

func (aa25Self *aa25Bench) aa25HasFault(aa25Fault AA25Fault) bool {
	for _, aa25Value := range aa25Self.aa25Faults() {
		if aa25Value == aa25Fault {
			return true
		}
	}
	return false
}

func (aa25Self *aa25Bench) aa25RawSymbols(aa25Echo bool) []byte {
	var aa25Result []byte
	for _, aa25Event := range aa25Self.aa25Select(AA25EventRawSymbol) {
		if aa25Event.AA25Echo == aa25Echo {
			aa25Result = append(aa25Result, aa25Event.AA25Raw)
		}
	}
	return aa25Result
}

func (aa25Self *aa25Bench) aa25LogicalBytes(aa25Echo bool) []byte {
	var aa25Result []byte
	for _, aa25Event := range aa25Self.aa25Select(AA25EventLogicalByte) {
		if aa25Event.AA25Echo == aa25Echo {
			aa25Result = append(aa25Result, aa25Event.AA25Logical)
		}
	}
	return aa25Result
}

func aa25EqualBytes(aa25Left, aa25Right []byte) bool {
	return string(aa25Left) == string(aa25Right)
}

// aa25ExpectedEdges derives the exact transmit schedule from the wire bytes.
func (aa25Self *aa25Bench) aa25ExpectedEdges(aa25FirstLow uint64, aa25Wire []byte) []aa25TestTransition {
	aa25Base := aa25Rat(aa25FirstLow+aa25Self.aa25Config.AA25TurnaroundTicks, 1)
	aa25Bit := aa25Rat(aa25Self.aa25Hz, 2400)
	aa25Level := AA25LevelHigh
	var aa25Result []aa25TestTransition
	aa25Add := func(aa25K uint64, aa25Want AA25Level) {
		if aa25Want != aa25Level {
			aa25Time := new(aa25big.Rat).Mul(aa25Rat(10+aa25K, 1), aa25Bit)
			aa25Result = append(aa25Result, aa25TestTransition{aa25Ceil(aa25Time.Add(aa25Time, aa25Base)), aa25Want})
			aa25Level = aa25Want
		}
	}
	for aa25Index, aa25Symbol := range aa25Wire {
		aa25K := uint64(aa25Index) * 10
		aa25Add(aa25K, AA25LevelLow)
		for aa25Data := uint64(0); aa25Data < 8; aa25Data++ {
			aa25Add(aa25K+1+aa25Data, AA25Level(aa25Symbol>>aa25Data&1))
		}
		aa25Add(aa25K+9, AA25LevelHigh)
	}
	return aa25Result
}

func aa25EqualEdges(aa25Left, aa25Right []aa25TestTransition) bool {
	if len(aa25Left) != len(aa25Right) {
		return false
	}
	for aa25Index := range aa25Left {
		if aa25Left[aa25Index] != aa25Right[aa25Index] {
			return false
		}
	}
	return true
}

func aa25PreparedWire(aa25Prepared AA25Prepared) []byte {
	return aa25Prepared.aa25Wire[:aa25Prepared.aa25WireLen]
}

// aa25FinalStart returns the first-low tick of the last request symbol.
func (aa25Self *aa25Bench) aa25FinalStart() uint64 {
	aa25Raw := aa25Self.aa25Select(AA25EventRawSymbol)
	aa25Last := uint64(0)
	for _, aa25Event := range aa25Raw {
		if !aa25Event.AA25Echo {
			aa25Last = aa25Event.AA25StartTick
		}
	}
	return aa25Last
}

// aa25Idle is a start time past the startup or post-fault resynchronization.
func (aa25Self *aa25Bench) aa25Idle(aa25From uint64) *aa25big.Rat {
	aa25Elapsed := aa25Rat(aa25From-aa25TestAnchor, 1)
	aa25Elapsed.Quo(aa25Elapsed, aa25Self.aa25SamplePeriod())
	aa25Floor := new(aa25big.Int).Quo(aa25Elapsed.Num(), aa25Elapsed.Denom()).Uint64()
	return aa25Self.aa25GridTime(aa25Floor + 120)
}

// aa25SendRequest queues SYN plus the request after startup idle and returns
// the time the request ends.
func (aa25Self *aa25Bench) aa25SendRequest(aa25Wire []byte) *aa25big.Rat {
	aa25End := aa25Self.aa25Send(aa25Self.aa25GridTime(120), 0xAA)
	return aa25Self.aa25Send(aa25End, aa25Wire...)
}

func aa25TickAfter(aa25Time *aa25big.Rat, aa25Samples uint64, aa25Self *aa25Bench) uint64 {
	aa25Value := new(aa25big.Rat).Mul(aa25Rat(aa25Samples, 1), aa25Self.aa25SamplePeriod())
	return aa25Ceil(aa25Value.Add(aa25Value, aa25Time))
}

func TestAA25CRCVectors(aa25T *aa25testing.T) {
	// Literal vectors of the public CRC definition, including wire expansion.
	for _, aa25Case := range []struct {
		aa25Logical []byte
		aa25Want    byte
	}{
		{nil, 0x00},
		{[]byte{0x01, 0x02}, 0x99},
		{[]byte{0x10, 0xFE, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA}, 0x77},
	} {
		if aa25Got := aa25TestCRC(aa25Case.aa25Logical); aa25Got != aa25Case.aa25Want {
			aa25T.Fatalf("aa25 reference CRC(% X) = %02X; want %02X", aa25Case.aa25Logical, aa25Got, aa25Case.aa25Want)
		}
		aa25State := byte(0)
		for _, aa25Symbol := range aa25TestExpand(aa25Case.aa25Logical) {
			aa25State = aa25CRCUpdate(aa25State, aa25Symbol)
		}
		if aa25State != aa25Case.aa25Want {
			aa25T.Fatalf("aa25 incremental CRC(% X) = %02X; want %02X", aa25Case.aa25Logical, aa25State, aa25Case.aa25Want)
		}
	}
}

func TestAA25RationalScheduleRetainsRemainders(aa25T *aa25testing.T) {
	aa25Whole, aa25Remainder := aa25RationalAt(1, aa25TestHz, 24000)
	if aa25Whole != 41666 || aa25Remainder != 16000 {
		aa25T.Fatalf("aa25 one sample = %d rem %d", aa25Whole, aa25Remainder)
	}
	aa25Whole, aa25Remainder = aa25RationalAt(3, aa25TestHz, 24000)
	if aa25Whole != 125000 || aa25Remainder != 0 {
		aa25T.Fatalf("aa25 three samples = %d rem %d", aa25Whole, aa25Remainder)
	}
	for _, aa25Hz := range []uint64{24000, 1_000_003, 19_200_000, 54_000_000, aa25TestHz, 3_000_000_001} {
		for _, aa25Den := range []uint64{24000, 2400} {
			for _, aa25Index := range []uint64{0, 1, 2, 3, 7, 239, 24000, 86_400 * 24000, 1 << 40} {
				aa25Exact := new(aa25big.Rat).Mul(aa25Rat(aa25Index, 1), aa25Rat(aa25Hz, aa25Den))
				aa25Want := aa25Ceil(aa25Exact.Add(aa25Exact, aa25Rat(aa25TestAnchor, 1)))
				if aa25Got := aa25RationalDeadline(aa25TestAnchor, aa25Index, aa25Hz, aa25Den); aa25Got != aa25Want {
					aa25T.Fatalf("aa25 hz=%d den=%d n=%d: %d; want %d", aa25Hz, aa25Den, aa25Index, aa25Got, aa25Want)
				}
			}
		}
	}
	// One simulated second has no drift: 24000 steps land exactly on +1e9 ticks,
	// where a truncated 41666-tick period would be 16000 ticks short.
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25Self.aa25Run(aa25TestAnchor + aa25TestHz)
	if len(aa25Self.aa25Reads) != 24001 {
		aa25T.Fatalf("aa25 reads in one second = %d", len(aa25Self.aa25Reads))
	}
	if aa25Got := aa25Self.aa25Reads[24000].aa25Tick; aa25Got != aa25TestAnchor+aa25TestHz {
		aa25T.Fatalf("aa25 drift: %d", aa25Got)
	}
	for aa25Index, aa25Read := range aa25Self.aa25Reads {
		if aa25Want := aa25Self.aa25GridDeadline(uint64(aa25Index)); aa25Read.aa25Tick != aa25Want {
			aa25T.Fatalf("aa25 sample %d at %d; want %d", aa25Index, aa25Read.aa25Tick, aa25Want)
		}
	}
	if aa25Self.aa25Engine.AA25Stats().AA25MissedSamples != 0 {
		aa25T.Fatal("aa25 on-time polling reported a gap")
	}
}

func TestAA25LateObservationKeepsActualTimestampAndAbsoluteSchedule(aa25T *aa25testing.T) {
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25Self.aa25Run(aa25Self.aa25GridDeadline(9))
	aa25Late := aa25Self.aa25GridDeadline(10) + 7000
	aa25Self.aa25Step(aa25Late)
	aa25Read := aa25Self.aa25Reads[len(aa25Self.aa25Reads)-1]
	if aa25Read.aa25Tick != aa25Late {
		aa25T.Fatalf("aa25 observation timestamp %d; want actual %d", aa25Read.aa25Tick, aa25Late)
	}
	if aa25Next, aa25Want := aa25Self.aa25Engine.AA25NextDeadlineTick(), aa25Self.aa25GridDeadline(11); aa25Next != aa25Want {
		aa25T.Fatalf("aa25 next deadline %d; want absolute %d", aa25Next, aa25Want)
	}
	aa25Stats := aa25Self.aa25Engine.AA25Stats()
	if aa25Stats.AA25MaxObservationLate != 7000 || aa25Stats.AA25MissedSamples != 0 {
		aa25T.Fatalf("aa25 stats %+v", aa25Stats)
	}
}

func TestAA25DecodesEverySymbolAtBitCentres(aa25T *aa25testing.T) {
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	// SYN goes last: a leading SYN would turn the remaining bytes into a frame
	// whose length byte exceeds the protocol maximum.
	aa25Symbols := make([]byte, 0, 256)
	for aa25Value := 0; aa25Value < 256; aa25Value++ {
		if byte(aa25Value) != 0xAA {
			aa25Symbols = append(aa25Symbols, byte(aa25Value))
		}
	}
	aa25Symbols = append(aa25Symbols, 0xAA)
	aa25End := aa25Self.aa25Send(aa25Self.aa25GridTime(120), aa25Symbols...)
	aa25Self.aa25Run(aa25TickAfter(aa25End, 5, aa25Self))
	if aa25Got := aa25Self.aa25RawSymbols(false); !aa25EqualBytes(aa25Got, aa25Symbols) {
		aa25T.Fatalf("aa25 decoded % X", aa25Got)
	}
	for _, aa25Event := range aa25Self.aa25Select(AA25EventRawSymbol) {
		if aa25Event.AA25Dissent != 0 {
			aa25T.Fatalf("aa25 clean symbol dissent: %+v", aa25Event)
		}
		for aa25Position, aa25Count := range aa25Event.AA25EdgePhase {
			if aa25Position != 0 && aa25Count != 0 {
				aa25T.Fatalf("aa25 aligned edge at position %d: %+v", aa25Position, aa25Event)
			}
		}
		// The symbol is timestamped at the observation that validated its stop.
		if aa25Event.AA25Tick != aa25Self.aa25GridDeadline(aa25Event.AA25SampleIndex) {
			aa25T.Fatalf("aa25 symbol timestamp: %+v", aa25Event)
		}
		if aa25Offset := aa25Event.AA25SampleIndex - (aa25Event.AA25StartTick-aa25TestAnchor)*24000/aa25TestHz; aa25Offset < 95 || aa25Offset > 97 {
			aa25T.Fatalf("aa25 symbol completed at offset %d", aa25Offset)
		}
	}
	if len(aa25Self.aa25Faults()) != 0 {
		aa25T.Fatalf("aa25 faults %v", aa25Self.aa25Faults())
	}
}

func TestAA25CentralObservationsDecideBits(aa25T *aa25testing.T) {
	// The 0xFF data bits are high: pulses inside one data bit vary which of its
	// ten observations are low. Start-relative offsets; bit 1 occupies 10..19.
	for _, aa25Case := range []struct {
		aa25Name             string
		aa25First, aa25Count uint64
		aa25Want             byte
		aa25Dissent          uint8
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
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			aa25Self := aa25NewBench(aa25T, aa25TestConfig())
			aa25Start := uint64(120)
			aa25Self.aa25External = []aa25TestTransition{
				{aa25Self.aa25GridDeadline(aa25Start), AA25LevelLow},
				{aa25Self.aa25GridDeadline(aa25Start + 10), AA25LevelHigh},
			}
			if aa25Case.aa25Count != 0 {
				aa25Self.aa25External = append(aa25Self.aa25External,
					aa25TestTransition{aa25Self.aa25GridDeadline(aa25Start + aa25Case.aa25First), AA25LevelLow},
					aa25TestTransition{aa25Self.aa25GridDeadline(aa25Start + aa25Case.aa25First + aa25Case.aa25Count), AA25LevelHigh})
			}
			aa25Self.aa25Run(aa25Self.aa25GridDeadline(aa25Start + 110))
			aa25Raw := aa25Self.aa25Select(AA25EventRawSymbol)
			if len(aa25Raw) != 1 || aa25Raw[0].AA25Raw != aa25Case.aa25Want || aa25Raw[0].AA25Dissent != aa25Case.aa25Dissent {
				aa25T.Fatalf("aa25 raw %+v; want %02X dissent %d", aa25Raw, aa25Case.aa25Want, aa25Case.aa25Dissent)
			}
			if aa25Case.aa25Count != 0 && aa25Case.aa25Name == "non-central-before" &&
				(aa25Raw[0].AA25EdgePhase[1] != 1 || aa25Raw[0].AA25EdgePhase[4] != 1) {
				aa25T.Fatalf("aa25 edge phase not retained: %+v", aa25Raw[0].AA25EdgePhase)
			}
		})
	}
}

func TestAA25PhaseAnchorsToFirstObservedLow(aa25T *aa25testing.T) {
	// Each sender start falls inside one sample period; the first observation at
	// or after it is the anchor, and decoding is unchanged.
	aa25Symbols := []byte{0x55, 0x00, 0xFF, 0x3C, 0xA9, 0x01}
	for _, aa25Fraction := range []*aa25big.Rat{aa25Rat(0, 1), aa25Rat(1, 4), aa25Rat(1, 2), aa25Rat(3, 4), aa25Rat(999, 1000)} {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Shift := new(aa25big.Rat).Mul(aa25Fraction, aa25Self.aa25SamplePeriod())
		aa25Start := new(aa25big.Rat).Add(aa25Self.aa25GridTime(120), aa25Shift)
		aa25End := aa25Self.aa25Send(aa25Start, aa25Symbols...)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 5, aa25Self))
		if aa25Got := aa25Self.aa25RawSymbols(false); !aa25EqualBytes(aa25Got, aa25Symbols) {
			aa25T.Fatalf("aa25 fraction %v decoded % X", aa25Fraction, aa25Got)
		}
		aa25WantSample := uint64(121)
		if aa25Fraction.Sign() == 0 {
			aa25WantSample = 120
		}
		if aa25Got, aa25Want := aa25Self.aa25Select(AA25EventRawSymbol)[0].AA25StartTick, aa25Self.aa25GridDeadline(aa25WantSample); aa25Got != aa25Want {
			aa25T.Fatalf("aa25 fraction %v anchor %d; want %d", aa25Fraction, aa25Got, aa25Want)
		}
	}
}

func TestAA25DriftReanchorsEachCharacter(aa25T *aa25testing.T) {
	aa25Symbols := []byte{0x55, 0xAA, 0x00, 0xFF, 0x0F, 0xF0, 0x81, 0x7E, 0x55, 0x33}
	aa25Send := func(aa25Self *aa25Bench, aa25Scale *aa25big.Rat) *aa25big.Rat {
		aa25Start := aa25Self.aa25GridTime(120)
		for _, aa25Symbol := range aa25Symbols {
			aa25Start = aa25Self.aa25SendCharacter(aa25Start, aa25Scale, aa25Symbol, AA25LevelHigh)
		}
		return aa25Start
	}
	for _, aa25Scale := range []*aa25big.Rat{aa25Rat(97, 100), aa25Rat(103, 100)} {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25End := aa25Send(aa25Self, aa25Scale)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 5, aa25Self))
		if aa25Got := aa25Self.aa25RawSymbols(false); !aa25EqualBytes(aa25Got, aa25Symbols) {
			aa25T.Fatalf("aa25 scale %v decoded % X", aa25Scale, aa25Got)
		}
		// Bit-rate error appears in the retained edge phase instead of position 0 only.
		aa25Spread := false
		for _, aa25Event := range aa25Self.aa25Select(AA25EventRawSymbol) {
			for aa25Position, aa25Count := range aa25Event.AA25EdgePhase {
				aa25Spread = aa25Spread || (aa25Position != 0 && aa25Count != 0)
			}
		}
		if !aa25Spread {
			aa25T.Fatalf("aa25 scale %v left no phase evidence", aa25Scale)
		}
	}
	// Control: an excessive rate error must not decode cleanly.
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25End := aa25Send(aa25Self, aa25Rat(115, 100))
	aa25Self.aa25Run(aa25TickAfter(aa25End, 5, aa25Self))
	if aa25EqualBytes(aa25Self.aa25RawSymbols(false), aa25Symbols) {
		aa25T.Fatal("aa25 15% slow sender decoded cleanly")
	}
}

func TestAA25RejectsBadStartAndBadStop(aa25T *aa25testing.T) {
	aa25T.Run("false-start", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		// Low only for offsets 0..3, before the start bit's central observations.
		aa25Self.aa25External = []aa25TestTransition{
			{aa25Self.aa25GridDeadline(120), AA25LevelLow},
			{aa25Self.aa25GridDeadline(124), AA25LevelHigh},
		}
		aa25Self.aa25Run(aa25Self.aa25GridDeadline(260))
		if !aa25Self.aa25HasFault(AA25FaultFalseStart) || len(aa25Self.aa25Select(AA25EventRawSymbol)) != 0 {
			aa25T.Fatalf("aa25 faults %v events %d", aa25Self.aa25Faults(), len(aa25Self.aa25Events))
		}
	})
	aa25T.Run("start-majority-control", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		// One high central observation (offset 5) cannot reject the start bit.
		aa25Self.aa25External = []aa25TestTransition{
			{aa25Self.aa25GridDeadline(120), AA25LevelLow},
			{aa25Self.aa25GridDeadline(125), AA25LevelHigh},
			{aa25Self.aa25GridDeadline(126), AA25LevelLow},
			{aa25Self.aa25GridDeadline(130), AA25LevelHigh},
		}
		aa25Self.aa25Run(aa25Self.aa25GridDeadline(260))
		aa25Raw := aa25Self.aa25Select(AA25EventRawSymbol)
		if len(aa25Raw) != 1 || aa25Raw[0].AA25Raw != 0xFF || aa25Raw[0].AA25Dissent != 1 {
			aa25T.Fatalf("aa25 raw %+v faults %v", aa25Raw, aa25Self.aa25Faults())
		}
	})
	aa25T.Run("bad-stop-blocks-response", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x11)
		aa25Wire := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
		aa25End := aa25Self.aa25Send(aa25Self.aa25GridTime(120), 0xAA)
		aa25End = aa25Self.aa25Send(aa25End, aa25Wire[:len(aa25Wire)-1]...)
		aa25End = aa25Self.aa25SendCharacter(aa25End, aa25Rat(1, 1), aa25Wire[len(aa25Wire)-1], AA25LevelLow)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 400, aa25Self))
		if !aa25Self.aa25HasFault(AA25FaultBadStop) {
			aa25T.Fatalf("aa25 faults %v", aa25Self.aa25Faults())
		}
		if len(aa25Self.aa25Writes) != 0 || len(aa25Self.aa25Select(AA25EventAdmitted)) != 0 {
			aa25T.Fatalf("aa25 responded after bad stop: %+v", aa25Self.aa25Writes)
		}
		// The bad symbol is not published as observed.
		if aa25Got := len(aa25Self.aa25RawSymbols(false)); aa25Got != len(aa25Wire) {
			aa25T.Fatalf("aa25 raw count %d; want %d", aa25Got, len(aa25Wire))
		}
	})
}

func TestAA25ResyncNeedsOneWholeIdleCharacter(aa25T *aa25testing.T) {
	// Samples 0..N-1 are high and sample N is the first low: N=100 is the exact
	// boundary. Mutation caught: any other idle count.
	for _, aa25Case := range []struct {
		aa25HighSamples uint64
		aa25Decoded     int
	}{{99, 0}, {100, 1}, {101, 1}} {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25End := aa25Self.aa25Send(aa25Self.aa25GridTime(aa25Case.aa25HighSamples), 0x00)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 20, aa25Self))
		if aa25Got := len(aa25Self.aa25RawSymbols(false)); aa25Got != aa25Case.aa25Decoded {
			aa25T.Fatalf("aa25 %d idle samples decoded %d symbols; want %d", aa25Case.aa25HighSamples, aa25Got, aa25Case.aa25Decoded)
		}
	}
	// After a gap inside a busy stream no mid-character edge is taken as a start.
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25Stream := make([]byte, 12)
	for aa25Index := range aa25Stream {
		aa25Stream[aa25Index] = 0x55
	}
	aa25End := aa25Self.aa25Send(aa25Self.aa25GridTime(120), aa25Stream...)
	aa25Self.aa25Run(aa25Self.aa25GridDeadline(120 + 250))
	aa25Before := len(aa25Self.aa25RawSymbols(false))
	aa25Self.aa25Step(aa25Self.aa25GridDeadline(120 + 253))
	aa25Self.aa25Run(aa25TickAfter(aa25End, 0, aa25Self))
	if aa25Got := len(aa25Self.aa25RawSymbols(false)); aa25Got != aa25Before {
		aa25T.Fatalf("aa25 decoded %d symbols inside a busy stream after a gap", aa25Got-aa25Before)
	}
}

func TestAA25ResponseScheduleEchoAndCompletion(aa25T *aa25testing.T) {
	for _, aa25Hz := range []uint64{aa25TestHz, 19_200_000, 1_000_003} {
		aa25Config := aa25TestConfig()
		aa25Config.AA25TickHz = aa25Hz
		aa25Self := aa25NewBench(aa25T, aa25Config)
		aa25Prepared := aa25Self.aa25Offer(7, aa25TestPB, aa25TestSB, 2, 0x11, 0x22)
		aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01, 0x02)
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 700, aa25Self))

		aa25Wire := aa25PreparedWire(aa25Prepared)
		aa25WantWire := []byte{0x00, 0x02, 0x11, 0x22, aa25TestCRC([]byte{0x02, 0x11, 0x22})}
		if !aa25EqualBytes(aa25Wire, aa25WantWire) {
			aa25T.Fatalf("aa25 prepared wire % X; want % X", aa25Wire, aa25WantWire)
		}
		aa25Want := aa25Self.aa25ExpectedEdges(aa25Self.aa25FinalStart(), aa25Wire)
		if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
			aa25T.Fatalf("aa25 hz=%d edges %v; want %v (faults %v)", aa25Hz, aa25Self.aa25Writes, aa25Want, aa25Self.aa25Faults())
		}
		if aa25Got := aa25Self.aa25RawSymbols(true); !aa25EqualBytes(aa25Got, aa25Wire) {
			aa25T.Fatalf("aa25 echo % X; want % X", aa25Got, aa25Wire)
		}
		if aa25Got := aa25Self.aa25RawSymbols(false); !aa25EqualBytes(aa25Got, append([]byte{0xAA}, aa25Request...)) {
			aa25T.Fatalf("aa25 request raw % X", aa25Got)
		}
		aa25Complete := aa25Self.aa25Select(AA25EventTXComplete)
		if len(aa25Complete) != 1 || aa25Complete[0].AA25Generation != 7 {
			aa25T.Fatalf("aa25 complete %+v faults %v", aa25Complete, aa25Self.aa25Faults())
		}
		// Completion waits for the last stop bit's scheduled end, not its echo vote.
		aa25WantEnd := new(aa25big.Rat).Mul(aa25Rat(10+10*uint64(len(aa25Wire)), 1), aa25Rat(aa25Hz, 2400))
		aa25WantEnd.Add(aa25WantEnd, aa25Rat(aa25Self.aa25FinalStart(), 1))
		if aa25Complete[0].AA25Tick != aa25Ceil(aa25WantEnd) {
			aa25T.Fatalf("aa25 completion at %d; want %d", aa25Complete[0].AA25Tick, aa25Ceil(aa25WantEnd))
		}
		if len(aa25Self.aa25Faults()) != 0 || aa25Self.aa25Engine.AA25Stats().AA25EchoedSymbols != uint64(len(aa25Wire)) {
			aa25T.Fatalf("aa25 faults %v stats %+v", aa25Self.aa25Faults(), aa25Self.aa25Engine.AA25Stats())
		}
	}
}

func TestAA25NoEmissionBeforeFinalReceivePhase(aa25T *aa25testing.T) {
	// Admission happens at the stop bit's central observations; the first edge
	// stays at the exact character end plus turnaround for each turnaround.
	for _, aa25Turnaround := range []uint64{0, 1, 12345, 416_667} {
		aa25Config := aa25TestConfig()
		aa25Config.AA25TurnaroundTicks = aa25Turnaround
		aa25Self := aa25NewBench(aa25T, aa25Config)
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 0, 0x11)
		aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB))
		aa25Self.aa25RunUntilEvent(AA25EventAdmitted, aa25TickAfter(aa25End, 10, aa25Self))
		aa25Admitted := aa25Self.aa25Select(AA25EventAdmitted)[0]
		aa25FirstLow := aa25Self.aa25FinalStart()
		aa25Expected := aa25Rat(aa25FirstLow+aa25Turnaround, 1)
		aa25Character := new(aa25big.Rat).Mul(aa25Rat(10, 1), aa25Rat(aa25TestHz, 2400))
		aa25WantEdge := aa25Ceil(aa25Expected.Add(aa25Expected, aa25Character))
		if len(aa25Self.aa25Writes) != 0 || aa25Admitted.AA25Tick >= aa25WantEdge || aa25Admitted.AA25Deadline != aa25WantEdge {
			aa25T.Fatalf("aa25 early state: writes=%v admitted=%+v want=%d", aa25Self.aa25Writes, aa25Admitted, aa25WantEdge)
		}
		aa25Self.aa25Run(aa25WantEdge - 1)
		if len(aa25Self.aa25Writes) != 0 {
			aa25T.Fatalf("aa25 edge before %d: %v", aa25WantEdge, aa25Self.aa25Writes)
		}
		aa25Self.aa25Run(aa25WantEdge)
		if len(aa25Self.aa25Writes) != 1 || aa25Self.aa25Writes[0] != (aa25TestTransition{aa25WantEdge, AA25LevelLow}) {
			aa25T.Fatalf("aa25 turnaround %d first write %v; want %d", aa25Turnaround, aa25Self.aa25Writes, aa25WantEdge)
		}
	}
	aa25T.Run("final-phase-guard", func(aa25T *aa25testing.T) {
		// Even when the deadline tick is reached, an unobserved final phase blocks TX.
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 0, 0x11)
		aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB))
		aa25Self.aa25RunUntilEvent(AA25EventAdmitted, aa25TickAfter(aa25End, 10, aa25Self))
		aa25Self.aa25Now = aa25Self.aa25Select(AA25EventAdmitted)[0].AA25Deadline
		aa25Self.aa25Engine.aa25StepTX(aa25Self.aa25Now)
		if len(aa25Self.aa25Writes) != 0 {
			aa25T.Fatalf("aa25 emitted before the final receive phase: %v", aa25Self.aa25Writes)
		}
	})
}

func TestAA25TXEdgesKeepTheirOwnGrid(aa25T *aa25testing.T) {
	aa25T.Run("turnaround-off-grid", func(aa25T *aa25testing.T) {
		aa25Config := aa25TestConfig()
		aa25Config.AA25TurnaroundTicks = 12345
		aa25Self := aa25NewBench(aa25T, aa25Config)
		aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x55)
		aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		aa25Want := aa25Self.aa25ExpectedEdges(aa25Self.aa25FinalStart(), aa25PreparedWire(aa25Prepared))
		if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
			aa25T.Fatalf("aa25 edges %v; want %v", aa25Self.aa25Writes, aa25Want)
		}
		aa25OffGrid := 0
		for _, aa25Write := range aa25Self.aa25Writes {
			if !aa25Self.aa25OnGrid(aa25Write.aa25Tick) {
				aa25OffGrid++
			}
		}
		if aa25OffGrid != len(aa25Self.aa25Writes) {
			aa25T.Fatalf("aa25 %d of %d edges off the RX grid", aa25OffGrid, len(aa25Self.aa25Writes))
		}
	})
	aa25T.Run("late-anchor-observation", func(aa25T *aa25testing.T) {
		// The final character's first low is observed late: TX follows the actual
		// timestamp and does not snap back onto the RX grid.
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 0, 0x55)
		aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB)
		aa25Self.aa25SendRequest(aa25Request)
		aa25Final := uint64(120 + 100*len(aa25Request))
		aa25Self.aa25Run(aa25Self.aa25GridDeadline(aa25Final - 1))
		aa25LateTick := aa25Self.aa25GridDeadline(aa25Final) + 7000
		aa25Self.aa25Step(aa25LateTick)
		aa25Self.aa25Run(aa25Self.aa25GridDeadline(aa25Final + 500))
		if aa25Self.aa25FinalStart() != aa25LateTick {
			aa25T.Fatalf("aa25 anchor %d; want actual %d", aa25Self.aa25FinalStart(), aa25LateTick)
		}
		aa25Want := aa25Self.aa25ExpectedEdges(aa25LateTick, aa25PreparedWire(aa25Prepared))
		if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
			aa25T.Fatalf("aa25 edges %v; want %v faults %v", aa25Self.aa25Writes, aa25Want, aa25Self.aa25Faults())
		}
		if aa25Self.aa25OnGrid(aa25Self.aa25Writes[0].aa25Tick) {
			aa25T.Fatalf("aa25 edge %d snapped to RX grid", aa25Self.aa25Writes[0].aa25Tick)
		}
	})
}

func TestAA25RawEscapePrefixPrecedesSuffixAndContinuity(aa25T *aa25testing.T) {
	// Literal wire of the public vector: logical A9 and AA inside the data, CRC
	// over the expanded form. The responder address is the vector's destination.
	aa25Wire := aa25TestRequest(aa25TestResponder, 0xB5, 0x05, 0x27, 0xA9, 0x15, 0xAA)
	aa25Logical := []byte{aa25TestSource, aa25TestResponder, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA}
	aa25Logical = append(aa25Logical, aa25TestCRC(aa25Logical))
	aa25Config := aa25TestConfig()
	aa25Self := aa25NewBench(aa25T, aa25Config)
	aa25Self.aa25Offer(1, 0xB5, 0x05, 4, 0x33)
	aa25End := aa25Self.aa25SendRequest(aa25Wire)
	aa25Self.aa25Run(aa25TickAfter(aa25End, 400, aa25Self))
	if !aa25EqualBytes(aa25Self.aa25RawSymbols(false), append([]byte{0xAA}, aa25Wire...)) {
		aa25T.Fatalf("aa25 raw % X", aa25Self.aa25RawSymbols(false))
	}
	if !aa25EqualBytes(aa25Self.aa25LogicalBytes(false), aa25Logical) {
		aa25T.Fatalf("aa25 logical % X", aa25Self.aa25LogicalBytes(false))
	}
	aa25Events := aa25Self.aa25Events
	aa25Prefixes := 0
	for aa25Index, aa25Event := range aa25Events {
		if aa25Event.AA25Kind != AA25EventRawSymbol || aa25Event.AA25Raw != 0xA9 {
			continue
		}
		aa25Prefixes++
		// The prefix is out before the suffix character has even started, and
		// the very next stream record is the suffix's raw symbol, not a logical byte.
		aa25Next := aa25Events[aa25Index+1]
		if aa25Next.AA25Kind != AA25EventRawSymbol || aa25Next.AA25StartTick <= aa25Event.AA25Tick {
			aa25T.Fatalf("aa25 prefix withheld or merged: %+v then %+v", aa25Event, aa25Next)
		}
		aa25Decoded := aa25Events[aa25Index+2]
		if aa25Decoded.AA25Kind != AA25EventLogicalByte || !aa25Decoded.AA25WasEscaped || aa25Decoded.AA25Tick != aa25Next.AA25Tick {
			aa25T.Fatalf("aa25 suffix logical: %+v", aa25Decoded)
		}
	}
	if aa25Prefixes != 2 {
		aa25T.Fatalf("aa25 prefixes %d", aa25Prefixes)
	}
	if len(aa25Self.aa25Select(AA25EventFrameValid)) != 1 || len(aa25Self.aa25Select(AA25EventTXComplete)) != 1 {
		aa25T.Fatalf("aa25 valid escaped frame did not complete: faults %v", aa25Self.aa25Faults())
	}
}

func TestAA25EscapedCRCAnchorsOnSuffix(aa25T *aa25testing.T) {
	for _, aa25WantCRC := range []byte{0xA9, 0xAA} {
		aa25Data := -1
		for aa25Candidate := 0; aa25Candidate < 256; aa25Candidate++ {
			aa25Logical := []byte{aa25TestSource, aa25TestResponder, aa25TestPB, aa25TestSB, 1, byte(aa25Candidate)}
			if aa25TestCRC(aa25Logical) == aa25WantCRC {
				aa25Data = aa25Candidate
				break
			}
		}
		if aa25Data < 0 {
			aa25T.Fatalf("aa25 no data byte yields CRC %02X", aa25WantCRC)
		}
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x44)
		aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, byte(aa25Data))
		if aa25Request[len(aa25Request)-2] != 0xA9 {
			aa25T.Fatalf("aa25 CRC not escaped: % X", aa25Request)
		}
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		aa25Raw := aa25Self.aa25Select(AA25EventRawSymbol)
		aa25Suffix := uint64(0)
		for _, aa25Event := range aa25Raw {
			if !aa25Event.AA25Echo {
				aa25Suffix = aa25Event.AA25StartTick
			}
		}
		// aa25End is the suffix's end: the first edge is a whole character after
		// the suffix start, never a character after the prefix.
		if aa25Want := aa25Self.aa25ExpectedEdges(aa25Suffix, aa25PreparedWire(aa25Prepared)); !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
			aa25T.Fatalf("aa25 CRC %02X edges %v; want %v", aa25WantCRC, aa25Self.aa25Writes, aa25Want)
		}
	}
	// A response whose own CRC needs escaping is prepared in its wire form.
	for _, aa25WantCRC := range []byte{0xA9, 0xAA} {
		for aa25Candidate := 0; aa25Candidate < 256; aa25Candidate++ {
			if aa25TestCRC([]byte{0x01, byte(aa25Candidate)}) != aa25WantCRC {
				continue
			}
			aa25Prepared, aa25Err := AA25PrepareResponse(1, 0, 0, 0, []byte{byte(aa25Candidate)})
			if aa25Err != nil {
				aa25T.Fatal(aa25Err)
			}
			aa25Wire := aa25PreparedWire(aa25Prepared)
			aa25Want := append(append([]byte{0x00}, aa25TestExpand([]byte{0x01, byte(aa25Candidate)})...), aa25TestExpand([]byte{aa25WantCRC})...)
			if !aa25EqualBytes(aa25Wire, aa25Want) {
				aa25T.Fatalf("aa25 wire % X; want % X", aa25Wire, aa25Want)
			}
		}
	}
}

func TestAA25NonInitiatorSourceNeverPins(aa25T *aa25testing.T) {
	// Each source is paired with the responder as destination and a matching
	// PB/SB/NN shape, so only the source class differs from the valid control.
	for _, aa25Case := range []struct {
		aa25Name   string
		aa25Source byte
		aa25Admit  bool
	}{
		{"initiator-source-control", aa25TestSource, true},
		{"responder-source", 0x08, false},
		{"responder-source-high", 0xF5, false},
		{"responder-source-peer", 0x04, false},
	} {
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			aa25Self := aa25NewBench(aa25T, aa25TestConfig())
			aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x11)
			aa25Logical := []byte{aa25Case.aa25Source, aa25TestResponder, aa25TestPB, aa25TestSB, 1, 0x01}
			aa25Logical = append(aa25Logical, aa25TestCRC(aa25Logical))
			aa25End := aa25Self.aa25SendRequest(aa25TestExpand(aa25Logical))
			aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
			aa25Done := len(aa25Self.aa25Select(AA25EventTXComplete)) == 1
			if aa25Done != aa25Case.aa25Admit {
				aa25T.Fatalf("aa25 admit %v want %v: faults %v", aa25Done, aa25Case.aa25Admit, aa25Self.aa25Faults())
			}
			if !aa25Case.aa25Admit && (len(aa25Self.aa25Writes) != 0 ||
				len(aa25Self.aa25Select(AA25EventProvisional)) != 0 || !aa25Self.aa25HasFault(AA25FaultAddressInvalid)) {
				aa25T.Fatalf("aa25 non-initiator source drove the bus: writes %v faults %v", aa25Self.aa25Writes, aa25Self.aa25Faults())
			}
		})
	}
}

func TestAA25FrameFaultsStaySilent(aa25T *aa25testing.T) {
	aa25Good := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
	aa25BadCRC := append([]byte{}, aa25Good...)
	aa25BadCRC[len(aa25BadCRC)-1] ^= 0x40
	aa25Header := aa25Good[:5]
	for _, aa25Case := range []struct {
		aa25Name  string
		aa25Wire  []byte
		aa25Fault AA25Fault
	}{
		{"bad-crc", aa25BadCRC, AA25FaultBadCRC},
		{"invalid-escape", append(append([]byte{}, aa25Header...), 0xA9, 0x05, 0x00), AA25FaultEscapeInvalid},
		{"escape-then-syn", append(append([]byte{}, aa25Header...), 0xA9, 0xAA), AA25FaultEscapeInvalid},
		{"syn-in-frame", append(append([]byte{}, aa25Header...), 0xAA), AA25FaultSynInFrame},
		{"escaped-source", []byte{0xA9, 0x00, aa25TestResponder}, AA25FaultAddressInvalid},
		{"escaped-target", []byte{aa25TestSource, 0xA9, 0x01}, AA25FaultAddressInvalid},
	} {
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			aa25Self := aa25NewBench(aa25T, aa25TestConfig())
			aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x11)
			aa25End := aa25Self.aa25SendRequest(aa25Case.aa25Wire)
			aa25Self.aa25Run(aa25TickAfter(aa25End, 300, aa25Self))
			if !aa25Self.aa25HasFault(aa25Case.aa25Fault) || len(aa25Self.aa25Writes) != 0 {
				aa25T.Fatalf("aa25 faults %v writes %v", aa25Self.aa25Faults(), aa25Self.aa25Writes)
			}
			// The prepared response survives a malformed frame: last-known-good state.
			aa25End = aa25Self.aa25Send(aa25Self.aa25Idle(aa25Self.aa25Now), 0xAA)
			aa25End = aa25Self.aa25Send(aa25End, aa25Good...)
			aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
			if len(aa25Self.aa25Select(AA25EventTXComplete)) != 1 {
				aa25T.Fatalf("aa25 recovery failed: faults %v", aa25Self.aa25Faults())
			}
		})
	}
	aa25T.Run("dropped-frame-needs-a-new-syn", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x11)
		// A complete valid request follows the fault without a SYN between them.
		aa25End := aa25Self.aa25SendRequest(append([]byte{0xA9, 0x05}, aa25Good...))
		aa25Self.aa25Run(aa25TickAfter(aa25End, 300, aa25Self))
		if !aa25Self.aa25HasFault(AA25FaultEscapeInvalid) || len(aa25Self.aa25Writes) != 0 ||
			len(aa25Self.aa25Select(AA25EventFrameValid)) != 0 || len(aa25Self.aa25LogicalBytes(false)) != 0 {
			aa25T.Fatalf("aa25 parsed without SYN: writes %v logical % X", aa25Self.aa25Writes, aa25Self.aa25LogicalBytes(false))
		}
	})
	aa25T.Run("no-syn-no-frame", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x11)
		aa25End := aa25Self.aa25Send(aa25Self.aa25GridTime(120), aa25Good...)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 300, aa25Self))
		if len(aa25Self.aa25Writes) != 0 || len(aa25Self.aa25LogicalBytes(false)) != 0 || len(aa25Self.aa25RawSymbols(false)) != len(aa25Good) {
			aa25T.Fatalf("aa25 joined mid-stream: writes %v logical % X", aa25Self.aa25Writes, aa25Self.aa25LogicalBytes(false))
		}
	})
}

func TestAA25ResponseIdentityVariesEachField(aa25T *aa25testing.T) {
	// Each row varies one admission field of a CRC-valid request with the others
	// still matching. Mutation caught: dropping that field's comparison.
	aa25Offer := func(aa25Self *aa25Bench) { aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x11) }
	for _, aa25Case := range []struct {
		aa25Name       string
		aa25Wire       []byte
		aa25Responds   bool
		aa25Provisions bool
	}{
		{"exact-control", aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01), true, true},
		{"target", aa25TestRequest(aa25TestResponder+1, aa25TestPB, aa25TestSB, 0x01), false, false},
		{"pb", aa25TestRequest(aa25TestResponder, aa25TestPB+1, aa25TestSB, 0x01), false, true},
		{"sb", aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB+1, 0x01), false, true},
		{"nn", aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01, 0x02), false, true},
	} {
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			aa25Self := aa25NewBench(aa25T, aa25TestConfig())
			aa25Offer(aa25Self)
			aa25End := aa25Self.aa25SendRequest(aa25Case.aa25Wire)
			aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
			if len(aa25Self.aa25Select(AA25EventFrameValid)) != 1 {
				aa25T.Fatalf("aa25 control frame invalid: faults %v", aa25Self.aa25Faults())
			}
			if (len(aa25Self.aa25Writes) != 0) != aa25Case.aa25Responds {
				aa25T.Fatalf("aa25 writes %v; responds=%v", aa25Self.aa25Writes, aa25Case.aa25Responds)
			}
			if (len(aa25Self.aa25Select(AA25EventProvisional)) != 0) != aa25Case.aa25Provisions {
				aa25T.Fatalf("aa25 provisional mismatch: %v", aa25Self.aa25Select(AA25EventProvisional))
			}
		})
	}
	aa25T.Run("provisional-is-not-emission", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Offer(aa25Self)
		aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
		aa25Self.aa25RunUntilEvent(AA25EventProvisional, aa25TickAfter(aa25End, 0, aa25Self))
		if len(aa25Self.aa25Writes) != 0 || len(aa25Self.aa25Select(AA25EventAdmitted)) != 0 {
			aa25T.Fatalf("aa25 destination time emitted: %v", aa25Self.aa25Writes)
		}
	})
}

func TestAA25DefaultEngineNeverTransmits(aa25T *aa25testing.T) {
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
	aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
	if len(aa25Self.aa25Select(AA25EventFrameValid)) != 1 {
		aa25T.Fatal("aa25 control frame not validated")
	}
	if len(aa25Self.aa25Writes) != 0 || len(aa25Self.aa25Select(AA25EventProvisional)) != 0 || aa25Self.aa25Engine.AA25Stats().AA25TXEdges != 0 {
		aa25T.Fatalf("aa25 unprepared engine wrote %v", aa25Self.aa25Writes)
	}
}

func TestAA25GapIsRecordedNotSynthesized(aa25T *aa25testing.T) {
	aa25Build := func(aa25T *aa25testing.T) (*aa25Bench, []byte, *aa25big.Rat) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x11)
		aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
		return aa25Self, aa25Request, aa25Self.aa25SendRequest(aa25Request)
	}
	aa25T.Run("control-no-gap", func(aa25T *aa25testing.T) {
		aa25Self, _, aa25End := aa25Build(aa25T)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if len(aa25Self.aa25Select(AA25EventTXComplete)) != 1 || len(aa25Self.aa25Select(AA25EventGap)) != 0 {
			aa25T.Fatalf("aa25 control faults %v", aa25Self.aa25Faults())
		}
	})
	aa25T.Run("mid-frame", func(aa25T *aa25testing.T) {
		aa25Self, _, aa25End := aa25Build(aa25T)
		// Inside the second request character, sample 350 and six more are missed.
		aa25Self.aa25Run(aa25Self.aa25GridDeadline(349))
		aa25ReadsBefore := len(aa25Self.aa25Reads)
		aa25Jump := aa25Self.aa25GridDeadline(356) + 5
		aa25Self.aa25Step(aa25Jump)
		if aa25Got := len(aa25Self.aa25Reads) - aa25ReadsBefore; aa25Got != 1 {
			aa25T.Fatalf("aa25 catch-up reads: %d", aa25Got)
		}
		if aa25Self.aa25Reads[len(aa25Self.aa25Reads)-1].aa25Tick != aa25Jump {
			aa25T.Fatal("aa25 read not at the actual tick")
		}
		aa25Gaps := aa25Self.aa25Select(AA25EventGap)
		if len(aa25Gaps) != 1 || aa25Gaps[0].AA25SampleIndex != 350 || aa25Gaps[0].AA25Missed != 6 || aa25Gaps[0].AA25Tick != aa25Jump {
			aa25T.Fatalf("aa25 gap %+v", aa25Gaps)
		}
		if aa25Next, aa25Want := aa25Self.aa25Engine.AA25NextDeadlineTick(), aa25Self.aa25GridDeadline(357); aa25Next != aa25Want {
			aa25T.Fatalf("aa25 next %d; want absolute %d", aa25Next, aa25Want)
		}
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if len(aa25Self.aa25Writes) != 0 || len(aa25Self.aa25Select(AA25EventAdmitted)) != 0 {
			aa25T.Fatalf("aa25 responded across a gap: %v", aa25Self.aa25Writes)
		}
		aa25Stats := aa25Self.aa25Engine.AA25Stats()
		if aa25Stats.AA25MissedSamples != 6 || aa25Stats.AA25Withdrawals != 1 {
			aa25T.Fatalf("aa25 stats %+v", aa25Stats)
		}
		// Withdrawal holds across a good frame until a new generation arrives.
		aa25Good := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
		aa25End = aa25Self.aa25Send(aa25Self.aa25Idle(aa25Self.aa25Now), 0xAA)
		aa25End = aa25Self.aa25Send(aa25End, aa25Good...)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if len(aa25Self.aa25Writes) != 0 || len(aa25Self.aa25Select(AA25EventFrameValid)) != 1 {
			aa25T.Fatalf("aa25 withdrawal did not hold: %v", aa25Self.aa25Writes)
		}
		aa25Self.aa25Offer(2, aa25TestPB, aa25TestSB, 1, 0x11)
		aa25End = aa25Self.aa25Send(aa25Self.aa25Idle(aa25Self.aa25Now), 0xAA)
		aa25End = aa25Self.aa25Send(aa25End, aa25Good...)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		aa25Complete := aa25Self.aa25Select(AA25EventTXComplete)
		if len(aa25Complete) != 1 || aa25Complete[0].AA25Generation != 2 {
			aa25T.Fatalf("aa25 recovery %+v faults %v", aa25Complete, aa25Self.aa25Faults())
		}
	})
	aa25T.Run("between-escape-prefix-and-suffix", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x11)
		aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0xA9)
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		// SYN + 5 header symbols precede the prefix; the gap is in the prefix tail.
		aa25PrefixEnd := uint64(120 + 100*(1+5+1))
		aa25Self.aa25Run(aa25Self.aa25GridDeadline(aa25PrefixEnd - 3))
		aa25RawBefore := aa25Self.aa25RawSymbols(false)
		if aa25RawBefore[len(aa25RawBefore)-1] != 0xA9 {
			aa25T.Fatalf("aa25 prefix not observed: % X", aa25RawBefore)
		}
		aa25LogicalBefore := len(aa25Self.aa25LogicalBytes(false))
		aa25Self.aa25Step(aa25Self.aa25GridDeadline(aa25PrefixEnd + 1))
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if len(aa25Self.aa25LogicalBytes(false)) != aa25LogicalBefore {
			aa25T.Fatalf("aa25 decoded across a gap: % X", aa25Self.aa25LogicalBytes(false))
		}
		if len(aa25Self.aa25Select(AA25EventGap)) != 1 || len(aa25Self.aa25Writes) != 0 {
			aa25T.Fatalf("aa25 gap %v writes %v", aa25Self.aa25Select(AA25EventGap), aa25Self.aa25Writes)
		}
	})
}

// aa25StartResponse runs to just before the first TX edge of a 0xAB payload.
func aa25StartResponse(aa25T *aa25testing.T, aa25Config AA25Config) (*aa25Bench, AA25Prepared, []aa25TestTransition) {
	aa25T.Helper()
	aa25Self := aa25NewBench(aa25T, aa25Config)
	aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
	aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
	aa25Self.aa25RunUntilEvent(AA25EventAdmitted, aa25TickAfter(aa25End, 10, aa25Self))
	aa25Edges := aa25Self.aa25ExpectedEdges(aa25Self.aa25FinalStart(), aa25PreparedWire(aa25Prepared))
	aa25Self.aa25Run(aa25Edges[0].aa25Tick - 1)
	return aa25Self, aa25Prepared, aa25Edges
}

func TestAA25LateTXFailsWithoutCatchUp(aa25T *aa25testing.T) {
	aa25T.Run("expired-start-is-not-started", func(aa25T *aa25testing.T) {
		aa25Config := aa25TestConfig()
		aa25Config.AA25TurnaroundTicks = 1234
		aa25Self, _, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		aa25Self.aa25Step(aa25Edges[0].aa25Tick + 1)
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 1_000_000)
		if !aa25Self.aa25HasFault(AA25FaultTXLate) || len(aa25Self.aa25Writes) != 0 {
			aa25T.Fatalf("aa25 faults %v writes %v", aa25Self.aa25Faults(), aa25Self.aa25Writes)
		}
		if aa25Self.aa25Engine.AA25Stats().AA25Withdrawals != 1 {
			aa25T.Fatalf("aa25 admission retained: %+v", aa25Self.aa25Engine.AA25Stats())
		}
	})
	aa25T.Run("within-tolerance-keeps-absolute-schedule", func(aa25T *aa25testing.T) {
		aa25Config := aa25TestConfig()
		aa25Config.AA25TurnaroundTicks = 1234
		aa25Config.AA25TXLateToleranceTicks = 1
		aa25Self, _, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		aa25Self.aa25Step(aa25Edges[0].aa25Tick + 1)
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 1_000_000)
		aa25Want := append([]aa25TestTransition{{aa25Edges[0].aa25Tick + 1, AA25LevelLow}}, aa25Edges[1:]...)
		if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
			aa25T.Fatalf("aa25 edges %v; want %v faults %v", aa25Self.aa25Writes, aa25Want, aa25Self.aa25Faults())
		}
		if aa25Intents := aa25Self.aa25Select(AA25EventTXIntent); aa25Intents[0].AA25Deadline != aa25Edges[0].aa25Tick || aa25Intents[0].AA25Tick != aa25Edges[0].aa25Tick+1 {
			aa25T.Fatalf("aa25 intent lost the deadline: %+v", aa25Intents[0])
		}
	})
	aa25T.Run("mid-response-releases-once", func(aa25T *aa25testing.T) {
		aa25Config := aa25TestConfig()
		aa25Config.AA25TurnaroundTicks = 1234
		aa25Self, _, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		// The ACK symbol is all low: the second edge is its stop-bit rise.
		aa25Self.aa25Run(aa25Edges[1].aa25Tick - 1)
		if len(aa25Self.aa25Writes) != 1 {
			aa25T.Fatalf("aa25 writes %v", aa25Self.aa25Writes)
		}
		aa25LateTick := aa25Edges[1].aa25Tick + 1
		aa25Self.aa25Step(aa25LateTick)
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 1_000_000)
		aa25Want := []aa25TestTransition{aa25Edges[0], {aa25LateTick, AA25LevelHigh}}
		if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
			aa25T.Fatalf("aa25 edges %v; want %v", aa25Self.aa25Writes, aa25Want)
		}
		if !aa25Self.aa25HasFault(AA25FaultTXLate) || len(aa25Self.aa25Select(AA25EventTXRelease)) != 1 || len(aa25Self.aa25Select(AA25EventTXComplete)) != 0 {
			aa25T.Fatalf("aa25 faults %v", aa25Self.aa25Faults())
		}
	})
	aa25T.Run("long-stall-compresses-nothing", func(aa25T *aa25testing.T) {
		aa25Config := aa25TestConfig()
		aa25Config.AA25TurnaroundTicks = 1234
		aa25Self, _, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		aa25Self.aa25Run(aa25Edges[0].aa25Tick)
		aa25Stall := aa25Edges[len(aa25Edges)-1].aa25Tick + 1
		aa25Self.aa25Step(aa25Stall)
		for aa25Index := 0; aa25Index < 50; aa25Index++ {
			aa25Self.aa25Step(aa25Stall + uint64(aa25Index))
		}
		aa25Self.aa25Run(aa25Stall + 1_000_000)
		aa25Want := []aa25TestTransition{aa25Edges[0], {aa25Stall, AA25LevelHigh}}
		if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
			aa25T.Fatalf("aa25 edges %v; want %v", aa25Self.aa25Writes, aa25Want)
		}
	})
}

func TestAA25EchoIsObservedNeverInferred(aa25T *aa25testing.T) {
	aa25Config := aa25TestConfig()
	aa25Config.AA25TurnaroundTicks = 1234
	aa25T.Run("no-echo-is-a-fault", func(aa25T *aa25testing.T) {
		aa25Self, _, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		aa25Self.aa25Disconnected = true
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 1_000_000)
		if !aa25Self.aa25HasFault(AA25FaultEchoMissing) {
			aa25T.Fatalf("aa25 faults %v", aa25Self.aa25Faults())
		}
		if len(aa25Self.aa25RawSymbols(true)) != 0 || aa25Self.aa25Engine.AA25Stats().AA25EchoedSymbols != 0 {
			aa25T.Fatalf("aa25 echo inferred from intent: % X", aa25Self.aa25RawSymbols(true))
		}
		if len(aa25Self.aa25Select(AA25EventTXIntent)) == 0 || len(aa25Self.aa25Select(AA25EventTXComplete)) != 0 {
			aa25T.Fatal("aa25 intent or completion wrong")
		}
		// The ACK's first start edge happened; the second character was never started.
		if len(aa25Self.aa25Writes) > 3 {
			aa25T.Fatalf("aa25 continued without echo: %v", aa25Self.aa25Writes)
		}
	})
	aa25T.Run("collision-on-data-bit", func(aa25T *aa25testing.T) {
		aa25Self, aa25Prepared, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		// The second wire symbol is NN=1 (bit 0 high): another driver holds it low.
		if aa25PreparedWire(aa25Prepared)[1] != 0x01 {
			aa25T.Fatalf("aa25 wire % X", aa25PreparedWire(aa25Prepared))
		}
		aa25SecondStart := aa25Edges[2].aa25Tick
		aa25Bit := aa25Rat(aa25TestHz, 2400)
		aa25Self.aa25External = append(aa25Self.aa25External,
			aa25TestTransition{aa25SecondStart, AA25LevelLow},
			aa25TestTransition{aa25SecondStart + 3*aa25Ceil(aa25Bit), AA25LevelHigh})
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 1_000_000)
		if !aa25Self.aa25HasFault(AA25FaultEchoMismatch) {
			aa25T.Fatalf("aa25 faults %v", aa25Self.aa25Faults())
		}
		aa25Stats := aa25Self.aa25Engine.AA25Stats()
		if aa25Stats.AA25EchoedSymbols != 1 || aa25Stats.AA25Withdrawals != 1 || len(aa25Self.aa25Select(AA25EventTXComplete)) != 0 {
			aa25T.Fatalf("aa25 stats %+v", aa25Stats)
		}
		// Line released once at the mismatch and no later scheduled edge runs.
		aa25Writes := aa25Self.aa25Writes
		if aa25Writes[len(aa25Writes)-1].aa25Level != AA25LevelHigh || aa25Writes[len(aa25Writes)-1].aa25Tick >= aa25Edges[len(aa25Edges)-1].aa25Tick {
			aa25T.Fatalf("aa25 writes %v", aa25Writes)
		}
	})
	aa25T.Run("matching-levels-remain-observations", func(aa25T *aa25testing.T) {
		// Control: an identical second driver is indistinguishable by level.
		aa25Self, aa25Prepared, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		aa25Self.aa25External = append(aa25Self.aa25External, aa25Edges...)
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 1_000_000)
		if !aa25EqualBytes(aa25Self.aa25RawSymbols(true), aa25PreparedWire(aa25Prepared)) || len(aa25Self.aa25Faults()) != 0 {
			aa25T.Fatalf("aa25 raw % X faults %v", aa25Self.aa25RawSymbols(true), aa25Self.aa25Faults())
		}
	})
	aa25T.Run("unsolicited-start-while-transmitting", func(aa25T *aa25testing.T) {
		aa25Self, _, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		// In the stop tail of the ACK echo, before the next character is started.
		aa25Tail := aa25Edges[0].aa25Tick + 97*aa25Ceil(aa25Rat(aa25TestHz, 24000))
		aa25Self.aa25External = append(aa25Self.aa25External,
			aa25TestTransition{aa25Tail, AA25LevelLow},
			aa25TestTransition{aa25Tail + aa25Ceil(aa25Rat(aa25TestHz, 24000)), AA25LevelHigh})
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 1_000_000)
		if !aa25Self.aa25HasFault(AA25FaultEchoUnsolicited) || len(aa25Self.aa25Writes) != 2 {
			aa25T.Fatalf("aa25 faults %v writes %v", aa25Self.aa25Faults(), aa25Self.aa25Writes)
		}
	})
	aa25T.Run("another-start-before-response-yields", func(aa25T *aa25testing.T) {
		for _, aa25Lead := range []uint64{1, 5, 9} {
			aa25Local := aa25Config
			aa25Local.AA25TurnaroundTicks = 416_667
			_, _, aa25Edges := aa25StartResponse(aa25T, aa25Local)
			aa25Start := aa25Edges[0].aa25Tick - aa25Lead*aa25Ceil(aa25Rat(aa25TestHz, 24000))
			// aa25StartResponse ran past this time, so rebuild on a fresh bench.
			aa25Self := aa25NewBench(aa25T, aa25Local)
			aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
			aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
			aa25Self.aa25External = append(aa25Self.aa25External, aa25TestTransition{aa25Start, AA25LevelLow}, aa25TestTransition{aa25Start + 10*aa25Ceil(aa25Rat(aa25TestHz, 2400)), AA25LevelHigh})
			aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
			if !aa25Self.aa25HasFault(AA25FaultRXBeforeResponse) || len(aa25Self.aa25Writes) != 0 {
				aa25T.Fatalf("aa25 lead %d faults %v writes %v", aa25Lead, aa25Self.aa25Faults(), aa25Self.aa25Writes)
			}
		}
	})
	aa25T.Run("start-in-final-stop-tail-yields", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
		aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		// Offset 98 of the final character: after stop validation, before its end.
		aa25Final := uint64(120 + 100*len(aa25Request))
		aa25Self.aa25External = append(aa25Self.aa25External, aa25TestTransition{aa25Self.aa25GridDeadline(aa25Final + 98), AA25LevelLow}, aa25TestTransition{aa25Self.aa25GridDeadline(aa25Final + 198), AA25LevelHigh})
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if !aa25Self.aa25HasFault(AA25FaultRXBeforeResponse) || len(aa25Self.aa25Writes) != 0 {
			aa25T.Fatalf("aa25 faults %v writes %v", aa25Self.aa25Faults(), aa25Self.aa25Writes)
		}
	})
	aa25T.Run("start-at-the-emission-tick-vetoes-it", func(aa25T *aa25testing.T) {
		// Zero turnaround: the deadline tick is also a sample tick. Observation
		// runs first, so the other start wins and nothing is driven.
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
		aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		aa25Self.aa25Send(aa25End, 0x00)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 300, aa25Self))
		if !aa25Self.aa25HasFault(AA25FaultRXBeforeResponse) || len(aa25Self.aa25Writes) != 0 {
			aa25T.Fatalf("aa25 faults %v writes %v", aa25Self.aa25Faults(), aa25Self.aa25Writes)
		}
	})
	aa25T.Run("collision-on-stop-bit", func(aa25T *aa25testing.T) {
		aa25Self, _, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		// Another driver holds the ACK's stop bit low; the next character never starts.
		aa25Bit := aa25Ceil(aa25Rat(aa25TestHz, 2400))
		aa25Self.aa25External = append(aa25Self.aa25External,
			aa25TestTransition{aa25Edges[1].aa25Tick, AA25LevelLow},
			aa25TestTransition{aa25Edges[1].aa25Tick + aa25Bit, AA25LevelHigh})
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 1_000_000)
		if !aa25Self.aa25HasFault(AA25FaultBadStop) || !aa25Self.aa25HasFault(AA25FaultEchoMismatch) || len(aa25Self.aa25Writes) != 2 {
			aa25T.Fatalf("aa25 faults %v writes %v", aa25Self.aa25Faults(), aa25Self.aa25Writes)
		}
		if aa25Self.aa25Engine.AA25Stats().AA25EchoedSymbols != 0 {
			aa25T.Fatalf("aa25 stats %+v", aa25Self.aa25Engine.AA25Stats())
		}
	})
	aa25T.Run("propagation-within-allowance", func(aa25T *aa25testing.T) {
		aa25Local := aa25Config
		aa25Local.AA25EchoAllowanceTicks = 12 * aa25Ceil(aa25Rat(aa25TestHz, 24000))
		aa25Self, aa25Prepared, aa25Edges := aa25StartResponse(aa25T, aa25Local)
		aa25Self.aa25Propagation = 8 * aa25Ceil(aa25Rat(aa25TestHz, 24000))
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 2_000_000)
		if !aa25EqualEdges(aa25Self.aa25Writes, aa25Edges) || len(aa25Self.aa25Select(AA25EventTXComplete)) != 1 {
			aa25T.Fatalf("aa25 writes %v faults %v", aa25Self.aa25Writes, aa25Self.aa25Faults())
		}
		if !aa25EqualBytes(aa25Self.aa25RawSymbols(true), aa25PreparedWire(aa25Prepared)) {
			aa25T.Fatalf("aa25 echo % X", aa25Self.aa25RawSymbols(true))
		}
		aa25Complete := aa25Self.aa25Select(AA25EventTXComplete)[0]
		aa25Final := aa25Self.aa25Select(AA25EventRawSymbol)
		if aa25Complete.AA25Tick < aa25Final[len(aa25Final)-1].AA25Tick {
			aa25T.Fatal("aa25 completion preceded the observed final echo")
		}
	})
	aa25T.Run("propagation-beyond-allowance", func(aa25T *aa25testing.T) {
		aa25Self, _, aa25Edges := aa25StartResponse(aa25T, aa25Config)
		aa25Self.aa25Propagation = 8 * aa25Ceil(aa25Rat(aa25TestHz, 24000))
		aa25Self.aa25Run(aa25Edges[len(aa25Edges)-1].aa25Tick + 2_000_000)
		if !aa25Self.aa25HasFault(AA25FaultEchoMissing) {
			aa25T.Fatalf("aa25 faults %v", aa25Self.aa25Faults())
		}
	})
}

func TestAA25HousekeepingTakesFixedSteps(aa25T *aa25testing.T) {
	aa25T.Run("one-command-per-step", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		for aa25Index := 0; aa25Index < aa25ControlCapacity; aa25Index++ {
			if !aa25Self.aa25Engine.AA25OfferWithdraw() {
				aa25T.Fatalf("aa25 queue rejected command %d", aa25Index)
			}
		}
		if aa25Self.aa25Engine.AA25OfferWithdraw() {
			aa25T.Fatal("aa25 queue grew past capacity")
		}
		for aa25Index := 0; aa25Index < aa25ControlCapacity; aa25Index++ {
			aa25ReadsBefore := len(aa25Self.aa25Reads)
			aa25Self.aa25Step(aa25Self.aa25GridDeadline(uint64(aa25Index)))
			if aa25Got := aa25Self.aa25Engine.AA25Stats().AA25ControlsApplied; aa25Got != uint64(aa25Index+1) {
				aa25T.Fatalf("aa25 step %d applied %d commands", aa25Index, aa25Got)
			}
			if len(aa25Self.aa25Reads) != aa25ReadsBefore+1 {
				aa25T.Fatalf("aa25 housekeeping displaced the sample at step %d", aa25Index)
			}
		}
		if !aa25Self.aa25Engine.AA25OfferWithdraw() {
			aa25T.Fatal("aa25 queue did not free capacity")
		}
	})
	aa25T.Run("stale-generation-is-refused", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(5, aa25TestPB, aa25TestSB, 1, 0x05)
		aa25Self.aa25Offer(5, aa25TestPB, aa25TestSB, 1, 0x06)
		aa25Self.aa25Offer(4, aa25TestPB, aa25TestSB, 1, 0x07)
		aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		aa25Stale := 0
		for _, aa25Fault := range aa25Self.aa25Faults() {
			if aa25Fault == AA25FaultStaleGeneration {
				aa25Stale++
			}
		}
		aa25Echo := aa25Self.aa25RawSymbols(true)
		if aa25Stale != 2 || len(aa25Echo) < 3 || aa25Echo[2] != 0x05 {
			aa25T.Fatalf("aa25 stale=%d echo % X", aa25Stale, aa25Echo)
		}
	})
	aa25T.Run("replacement-does-not-change-pinned-generation", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x05)
		aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		aa25Self.aa25RunUntilEvent(AA25EventProvisional, aa25TickAfter(aa25End, 0, aa25Self))
		aa25Self.aa25Offer(2, aa25TestPB, aa25TestSB, 1, 0x06)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		aa25Complete := aa25Self.aa25Select(AA25EventTXComplete)
		aa25Echo := aa25Self.aa25RawSymbols(true)
		if len(aa25Complete) != 1 || aa25Complete[0].AA25Generation != 1 || aa25Echo[2] != 0x05 {
			aa25T.Fatalf("aa25 complete %+v echo % X", aa25Complete, aa25Echo)
		}
		aa25End = aa25Self.aa25Send(aa25Self.aa25Idle(aa25Self.aa25Now), 0xAA)
		aa25End = aa25Self.aa25Send(aa25End, aa25Request...)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		aa25Complete = aa25Self.aa25Select(AA25EventTXComplete)
		if len(aa25Complete) != 2 || aa25Complete[1].AA25Generation != 2 {
			aa25T.Fatalf("aa25 next generation %+v", aa25Complete)
		}
	})
	aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
	aa25T.Run("withdraw-after-destination-silences", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x05)
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		aa25Self.aa25RunUntilEvent(AA25EventProvisional, aa25TickAfter(aa25End, 0, aa25Self))
		aa25Self.aa25Engine.AA25OfferWithdraw()
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if len(aa25Self.aa25Writes) != 0 || len(aa25Self.aa25Select(AA25EventFrameValid)) != 1 {
			aa25T.Fatalf("aa25 writes %v", aa25Self.aa25Writes)
		}
	})
	aa25T.Run("withdraw-before-first-edge-silences", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x05)
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		aa25Self.aa25RunUntilEvent(AA25EventAdmitted, aa25TickAfter(aa25End, 10, aa25Self))
		aa25Self.aa25Engine.AA25OfferWithdraw()
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if len(aa25Self.aa25Writes) != 0 {
			aa25T.Fatalf("aa25 writes %v", aa25Self.aa25Writes)
		}
	})
	aa25T.Run("withdraw-on-wire-finishes-then-silences", func(aa25T *aa25testing.T) {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x05)
		aa25End := aa25Self.aa25SendRequest(aa25Request)
		aa25Self.aa25RunUntilEvent(AA25EventTXIntent, aa25TickAfter(aa25End, 10, aa25Self))
		aa25Self.aa25Engine.AA25OfferWithdraw()
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if len(aa25Self.aa25Select(AA25EventTXComplete)) != 1 {
			aa25T.Fatalf("aa25 on-wire response did not finish: faults %v", aa25Self.aa25Faults())
		}
		aa25Writes := len(aa25Self.aa25Writes)
		aa25End = aa25Self.aa25Send(aa25Self.aa25Idle(aa25Self.aa25Now), 0xAA)
		aa25End = aa25Self.aa25Send(aa25End, aa25Request...)
		aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
		if len(aa25Self.aa25Writes) != aa25Writes {
			aa25T.Fatal("aa25 responded after withdrawal")
		}
	})
}

func TestAA25SinkLossInvalidatesWithoutRetryLoop(aa25T *aa25testing.T) {
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x05)
	aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
	aa25End := aa25Self.aa25SendRequest(aa25Request)
	aa25Self.aa25RunUntilEvent(AA25EventProvisional, aa25TickAfter(aa25End, 0, aa25Self))
	// The sink refuses everything: one protocol record is lost.
	aa25Self.aa25SinkLimited = true
	aa25Self.aa25SinkFree = 0
	aa25Self.aa25Run(aa25Self.aa25Now + 200*aa25Ceil(aa25Self.aa25SamplePeriod()))
	aa25Stats := aa25Self.aa25Engine.AA25Stats()
	if aa25Stats.AA25EventsDropped == 0 || aa25Stats.AA25Withdrawals != 1 {
		aa25T.Fatalf("aa25 stats %+v", aa25Stats)
	}
	aa25Self.aa25SinkLimited = false
	aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
	if len(aa25Self.aa25Writes) != 0 || len(aa25Self.aa25Select(AA25EventAdmitted)) != 0 {
		aa25T.Fatalf("aa25 responded after loss: %v", aa25Self.aa25Writes)
	}
	aa25Loss := aa25Self.aa25Select(AA25EventLoss)
	if len(aa25Loss) != 1 || aa25Loss[0].AA25Missed != aa25Stats.AA25EventsDropped {
		aa25T.Fatalf("aa25 loss marker %+v; stats %+v", aa25Loss, aa25Stats)
	}
}

func TestAA25ReofferDuringPersistentSinkLossStaysSilent(aa25T *aa25testing.T) {
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x05)
	aa25Request := aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)
	aa25End := aa25Self.aa25SendRequest(aa25Request)
	aa25Self.aa25RunUntilEvent(AA25EventProvisional, aa25TickAfter(aa25End, 0, aa25Self))
	aa25Self.aa25SinkLimited = true
	aa25Self.aa25SinkFree = 0
	aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
	// A newer generation is offered while the sink is still refusing events.
	aa25Self.aa25Offer(2, aa25TestPB, aa25TestSB, 1, 0x06)
	aa25Self.aa25Run(aa25Self.aa25Now + 200*aa25Ceil(aa25Self.aa25SamplePeriod()))
	aa25End = aa25Self.aa25Send(aa25Self.aa25Idle(aa25Self.aa25Now), 0xAA)
	aa25End = aa25Self.aa25Send(aa25End, aa25Request...)
	aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
	if len(aa25Self.aa25Writes) != 0 {
		aa25T.Fatalf("aa25 responded during persistent loss: %v", aa25Self.aa25Writes)
	}
	// Control: once the sink recovers, the repeated offer is admitted.
	aa25Self.aa25SinkLimited = false
	aa25Self.aa25Run(aa25Self.aa25Now + 200*aa25Ceil(aa25Self.aa25SamplePeriod()))
	aa25Self.aa25Offer(2, aa25TestPB, aa25TestSB, 1, 0x06)
	aa25Self.aa25Run(aa25Self.aa25Now + 200*aa25Ceil(aa25Self.aa25SamplePeriod()))
	aa25End = aa25Self.aa25Send(aa25Self.aa25Idle(aa25Self.aa25Now), 0xAA)
	aa25End = aa25Self.aa25Send(aa25End, aa25Request...)
	aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
	if len(aa25Self.aa25Select(AA25EventTXComplete)) != 1 {
		aa25T.Fatalf("aa25 recovered offer not admitted: faults %v", aa25Self.aa25Faults())
	}
}

func TestAA25TickRegressionFailsClosed(aa25T *aa25testing.T) {
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0x05)
	aa25Self.aa25Run(aa25Self.aa25GridDeadline(10))
	aa25Reads := len(aa25Self.aa25Reads)
	aa25Tick := aa25Self.aa25Now
	aa25Self.aa25Step(aa25Tick - 10)
	aa25Self.aa25Step(aa25Tick - 5)
	if len(aa25Self.aa25Reads) != aa25Reads {
		aa25T.Fatal("aa25 sampled on a regressed clock")
	}
	aa25Count := 0
	for _, aa25Fault := range aa25Self.aa25Faults() {
		if aa25Fault == AA25FaultTickRegression {
			aa25Count++
		}
	}
	if aa25Count != 1 || aa25Self.aa25Engine.AA25Stats().AA25Withdrawals != 1 {
		aa25T.Fatalf("aa25 regressions %d stats %+v", aa25Count, aa25Self.aa25Engine.AA25Stats())
	}
	aa25End := aa25Self.aa25Send(aa25Self.aa25Idle(aa25Tick), 0xAA)
	aa25End = aa25Self.aa25Send(aa25End, aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01)...)
	aa25Self.aa25Run(aa25TickAfter(aa25End, 500, aa25Self))
	if len(aa25Self.aa25Select(AA25EventFrameValid)) != 1 || len(aa25Self.aa25Writes) != 0 {
		aa25T.Fatalf("aa25 after regression: valid %d writes %v", len(aa25Self.aa25Select(AA25EventFrameValid)), aa25Self.aa25Writes)
	}
}

func TestAA25ConfigAndPreparedValidation(aa25T *aa25testing.T) {
	aa25Bench := &aa25Bench{aa25Now: aa25TestAnchor}
	aa25Ports := AA25Ports{AA25Levels: aa25Bench, AA25Edges: aa25Bench, AA25Ticks: aa25Bench, AA25Sink: aa25Bench}
	if _, aa25Err := AA25NewEngine(aa25TestConfig(), aa25Ports); aa25Err != nil {
		aa25T.Fatalf("aa25 control config: %v", aa25Err)
	}
	aa25BitTicks := aa25TestHz / 2400
	aa25CharacterTicks := aa25TestHz / 240
	for _, aa25Mutate := range []func(*AA25Config){
		func(aa25Config *AA25Config) { aa25Config.AA25TickHz = 0 },
		func(aa25Config *AA25Config) { aa25Config.AA25TickHz = 23999 },
		func(aa25Config *AA25Config) { aa25Config.AA25ResponderAddress = 0xA9 },
		func(aa25Config *AA25Config) { aa25Config.AA25ResponderAddress = 0xAA },
		func(aa25Config *AA25Config) { aa25Config.AA25ResponderAddress = 0xFE },
		func(aa25Config *AA25Config) { aa25Config.AA25ResponderAddress = 0x31 },
		func(aa25Config *AA25Config) { aa25Config.AA25ResponderAddress = 0x10 },
		func(aa25Config *AA25Config) { aa25Config.AA25TXLateToleranceTicks = aa25BitTicks },
		func(aa25Config *AA25Config) { aa25Config.AA25EchoAllowanceTicks = aa25CharacterTicks },
	} {
		aa25Config := aa25TestConfig()
		aa25Mutate(&aa25Config)
		if _, aa25Err := AA25NewEngine(aa25Config, aa25Ports); aa25Err != AA25ErrInvalidConfig {
			aa25T.Fatalf("aa25 config %+v: %v", aa25Config, aa25Err)
		}
	}
	for _, aa25Mutate := range []func(*AA25Ports){
		func(aa25Value *AA25Ports) { aa25Value.AA25Levels = nil },
		func(aa25Value *AA25Ports) { aa25Value.AA25Edges = nil },
		func(aa25Value *AA25Ports) { aa25Value.AA25Ticks = nil },
		func(aa25Value *AA25Ports) { aa25Value.AA25Sink = nil },
	} {
		aa25Value := aa25Ports
		aa25Mutate(&aa25Value)
		if _, aa25Err := AA25NewEngine(aa25TestConfig(), aa25Value); aa25Err != AA25ErrMissingPort {
			aa25T.Fatalf("aa25 ports: %v", aa25Err)
		}
	}
	if _, aa25Err := AA25PrepareResponse(0, 0, 0, 0, nil); aa25Err != AA25ErrInvalidResponse {
		aa25T.Fatalf("aa25 generation zero: %v", aa25Err)
	}
	if _, aa25Err := AA25PrepareResponse(1, 0, 0, 0, make([]byte, aa25MaxPreparedPayload+1)); aa25Err != AA25ErrInvalidResponse {
		aa25T.Fatalf("aa25 oversize payload: %v", aa25Err)
	}
	// The worst-case payload fits the fixed wire capacity in escaped form.
	aa25Worst := make([]byte, aa25MaxPreparedPayload)
	for aa25Index := range aa25Worst {
		aa25Worst[aa25Index] = 0xAA
	}
	aa25Prepared, aa25Err := AA25PrepareResponse(1, 0, 0, 0, aa25Worst)
	if aa25Err != nil || int(aa25Prepared.aa25WireLen) > aa25MaxPreparedWire {
		aa25T.Fatalf("aa25 worst case: %v len %d", aa25Err, aa25Prepared.aa25WireLen)
	}
}

func TestAA25TimingPathDoesNotAllocate(aa25T *aa25testing.T) {
	for _, aa25Disconnected := range []bool{false, true} {
		aa25Self := aa25NewBench(aa25T, aa25TestConfig())
		aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 2, 0xA9, 0xAA)
		aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0xA9, 0xAA))
		aa25Self.aa25Run(aa25TickAfter(aa25End, 0, aa25Self))
		aa25Self.aa25Disconnected = aa25Disconnected
		aa25Allocations := aa25testing.AllocsPerRun(1200, func() {
			if aa25Next := aa25Self.aa25Engine.AA25NextDeadlineTick(); aa25Next > aa25Self.aa25Now {
				aa25Self.aa25Now = aa25Next
			}
			aa25Self.aa25Engine.AA25Step()
		})
		if aa25Allocations != 0 {
			aa25T.Fatalf("aa25 disconnected=%v allocations per step: %v", aa25Disconnected, aa25Allocations)
		}
		if aa25Self.aa25Engine.AA25Stats().AA25TXEdges == 0 {
			aa25T.Fatal("aa25 allocation window did not cover transmission")
		}
	}
}

func aa25ParseSource(aa25T *aa25testing.T, aa25Path string) *aa25ast.File {
	aa25T.Helper()
	aa25File, aa25Err := aa25parser.ParseFile(aa25token.NewFileSet(), aa25Path, nil, 0)
	if aa25Err != nil {
		aa25T.Fatalf("aa25 parse %s: %v", aa25Path, aa25Err)
	}
	return aa25File
}

func TestAA25TimingPathStaticShape(aa25T *aa25testing.T) {
	aa25File := aa25ParseSource(aa25T, "aa25_engine.go")
	for _, aa25Import := range aa25File.Imports {
		if aa25Path := aa25Import.Path.Value; aa25Path != `"errors"` && aa25Path != `"math/bits"` {
			aa25T.Fatalf("aa25 forbidden import %s", aa25Path)
		}
	}
	aa25Allowed := map[string]bool{"aa25CRCUpdate": true, "AA25PrepareResponse": true}
	for _, aa25Decl := range aa25File.Decls {
		aa25Func, aa25IsFunc := aa25Decl.(*aa25ast.FuncDecl)
		aa25Name := ""
		if aa25IsFunc {
			aa25Name = aa25Func.Name.Name
		}
		aa25ast.Inspect(aa25Decl, func(aa25Node aa25ast.Node) bool {
			switch aa25Value := aa25Node.(type) {
			case *aa25ast.GoStmt, *aa25ast.DeferStmt, *aa25ast.SelectStmt, *aa25ast.SendStmt, *aa25ast.ChanType, *aa25ast.FuncLit, *aa25ast.MapType:
				aa25T.Fatalf("aa25 forbidden construct %T in %s", aa25Value, aa25Name)
			case *aa25ast.ForStmt, *aa25ast.RangeStmt:
				// Only the eight-bit CRC step and the preparation path may iterate.
				if !aa25Allowed[aa25Name] {
					aa25T.Fatalf("aa25 loop in timing path function %s", aa25Name)
				}
			case *aa25ast.CallExpr:
				if aa25Ident, aa25OK := aa25Value.Fun.(*aa25ast.Ident); aa25OK {
					switch aa25Ident.Name {
					case "append", "make", "new", "panic", "print", "println":
						aa25T.Fatalf("aa25 forbidden call %s in %s", aa25Ident.Name, aa25Name)
					}
				}
			}
			return true
		})
	}
}

func aa25CheckPrefix(aa25T *aa25testing.T, aa25Name string) {
	aa25T.Helper()
	if aa25Name != "_" && aa25Name != "localworker" &&
		!aa25strings.HasPrefix(aa25Name, "aa25") &&
		!aa25strings.HasPrefix(aa25Name, "AA25") &&
		!aa25strings.HasPrefix(aa25Name, "TestAA25") {
		aa25T.Fatalf("aa25 unprefixed identifier %q", aa25Name)
	}
}

func TestAA25EveryDeclaredIdentifierIsPrefixed(aa25T *aa25testing.T) {
	for _, aa25Path := range []string{"aa25_engine.go", "aa25_engine_test.go"} {
		aa25File := aa25ParseSource(aa25T, aa25Path)
		for _, aa25Import := range aa25File.Imports {
			if aa25Import.Name == nil {
				aa25T.Fatalf("aa25 import %s has no prefixed alias", aa25Import.Path.Value)
			}
			aa25CheckPrefix(aa25T, aa25Import.Name.Name)
		}
		aa25ast.Inspect(aa25File, func(aa25Node aa25ast.Node) bool {
			switch aa25Value := aa25Node.(type) {
			case *aa25ast.FuncDecl:
				aa25CheckPrefix(aa25T, aa25Value.Name.Name)
			case *aa25ast.TypeSpec:
				aa25CheckPrefix(aa25T, aa25Value.Name.Name)
			case *aa25ast.ValueSpec:
				for _, aa25Name := range aa25Value.Names {
					aa25CheckPrefix(aa25T, aa25Name.Name)
				}
			case *aa25ast.Field:
				for _, aa25Name := range aa25Value.Names {
					aa25CheckPrefix(aa25T, aa25Name.Name)
				}
			case *aa25ast.AssignStmt:
				if aa25Value.Tok == aa25token.DEFINE {
					for _, aa25Left := range aa25Value.Lhs {
						if aa25Ident, aa25OK := aa25Left.(*aa25ast.Ident); aa25OK {
							aa25CheckPrefix(aa25T, aa25Ident.Name)
						}
					}
				}
			case *aa25ast.RangeStmt:
				if aa25Value.Tok == aa25token.DEFINE {
					for _, aa25Left := range []aa25ast.Expr{aa25Value.Key, aa25Value.Value} {
						if aa25Ident, aa25OK := aa25Left.(*aa25ast.Ident); aa25OK {
							aa25CheckPrefix(aa25T, aa25Ident.Name)
						}
					}
				}
			}
			return true
		})
	}
}

// aa25RunUntilCompletes polls on the engine's own deadlines until the given
// number of TXComplete events exists, so no later input is skipped past.
func (aa25Self *aa25Bench) aa25RunUntilCompletes(aa25Count int, aa25Limit uint64) {
	aa25Self.aa25T.Helper()
	for aa25Guard := 0; aa25Guard < 1<<22; aa25Guard++ {
		if len(aa25Self.aa25Select(AA25EventTXComplete)) >= aa25Count {
			return
		}
		aa25Next := aa25Self.aa25Engine.AA25NextDeadlineTick()
		if aa25Next > aa25Limit {
			aa25Self.aa25T.Fatalf("aa25 completes never reached %d; faults %v", aa25Count, aa25Self.aa25Faults())
		}
		if aa25Next > aa25Self.aa25Now {
			aa25Self.aa25Now = aa25Next
		}
		aa25Self.aa25Engine.AA25Step()
	}
	aa25Self.aa25T.Fatal("aa25 bench made no progress")
}

// aa25QueueFinalSymbol queues one initiator symbol a fixed gap after the
// latest TXComplete and returns its end.
func (aa25Self *aa25Bench) aa25QueueFinalSymbol(aa25Symbol byte, aa25Completes int) *aa25big.Rat {
	aa25Self.aa25T.Helper()
	aa25Complete := aa25Self.aa25Select(AA25EventTXComplete)
	if len(aa25Complete) != aa25Completes {
		aa25Self.aa25T.Fatalf("aa25 completes %d; want %d (faults %v)", len(aa25Complete), aa25Completes, aa25Self.aa25Faults())
	}
	return aa25Self.aa25Send(aa25Rat(aa25Complete[aa25Completes-1].AA25Tick+aa25Self.aa25Hz/2400*2, 1), aa25Symbol)
}

func TestAA25FinalNackRetransmitsResponseOnce(aa25T *aa25testing.T) {
	// Each row follows an identical admitted exchange with one initiator symbol.
	// Mutation caught: dropping the NACK pin (no retransmit), retransmitting the
	// leading ACK too, anchoring the resend to the old request, or looping.
	for _, aa25Case := range []struct {
		aa25Name    string
		aa25Symbols []byte
		aa25Resends int
		aa25Fault   bool
	}{
		{"ack-control", []byte{0x00}, 0, false},
		{"nack-once", []byte{0xFF}, 1, false},
		{"nack-then-ack", []byte{0xFF, 0x00}, 1, false},
		{"nack-twice", []byte{0xFF, 0xFF}, 1, true},
	} {
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			aa25Self := aa25NewBench(aa25T, aa25TestConfig())
			aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
			aa25Wire := aa25PreparedWire(aa25Prepared)
			aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
			aa25Self.aa25RunUntilCompletes(1, aa25TickAfter(aa25End, 2000, aa25Self))
			aa25Want := aa25Self.aa25ExpectedEdges(aa25Self.aa25FinalStart(), aa25Wire)
			for aa25Index, aa25Symbol := range aa25Case.aa25Symbols {
				aa25SymbolEnd := aa25Self.aa25QueueFinalSymbol(aa25Symbol, aa25Index+1)
				if aa25Symbol == 0xFF && aa25Index < aa25Case.aa25Resends {
					aa25Self.aa25RunUntilCompletes(aa25Index+2, aa25TickAfter(aa25SymbolEnd, 2000, aa25Self))
					aa25Resend := aa25Self.aa25ExpectedEdges(aa25Self.aa25FinalStart(), aa25Wire[1:])
					aa25Want = append(aa25Want, aa25Resend...)
				} else {
					aa25Self.aa25Run(aa25TickAfter(aa25SymbolEnd, 700, aa25Self))
				}
			}
			if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
				aa25T.Fatalf("aa25 edges %v; want %v (faults %v)", aa25Self.aa25Writes, aa25Want, aa25Self.aa25Faults())
			}
			if aa25Self.aa25HasFault(AA25FaultNackExhausted) != aa25Case.aa25Fault {
				aa25T.Fatalf("aa25 faults %v; exhausted want %v", aa25Self.aa25Faults(), aa25Case.aa25Fault)
			}
			if aa25Got := len(aa25Self.aa25Select(AA25EventTXComplete)); aa25Got != 1+aa25Case.aa25Resends {
				aa25T.Fatalf("aa25 completes %d; want %d", aa25Got, 1+aa25Case.aa25Resends)
			}
			if aa25Case.aa25Resends > 0 {
				if aa25Echo := aa25Self.aa25RawSymbols(true); len(aa25Echo) != len(aa25Wire)+len(aa25Wire)-1 {
					aa25T.Fatalf("aa25 echo % X", aa25Echo)
				}
			}
		})
	}
}

func TestAA25RejectsRequestLengthAboveProtocolMaximum(aa25T *aa25testing.T) {
	// The prepared entry matches NN=17, so only the length limit can stop the
	// response. Mutation caught: admitting NN above 16.
	for _, aa25Case := range []struct {
		aa25NN    byte
		aa25Admit bool
	}{{16, true}, {17, false}} {
		aa25T.Run("nn"+string(rune('0'+aa25Case.aa25NN/10))+string(rune('0'+aa25Case.aa25NN%10)), func(aa25T *aa25testing.T) {
			aa25Self := aa25NewBench(aa25T, aa25TestConfig())
			aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, aa25Case.aa25NN, 0x11)
			aa25Data := make([]byte, aa25Case.aa25NN)
			aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, aa25Data...))
			aa25Self.aa25Run(aa25TickAfter(aa25End, 700, aa25Self))
			if (len(aa25Self.aa25Writes) != 0) != aa25Case.aa25Admit {
				aa25T.Fatalf("aa25 nn=%d writes %v faults %v", aa25Case.aa25NN, aa25Self.aa25Writes, aa25Self.aa25Faults())
			}
			if aa25Self.aa25HasFault(AA25FaultLengthInvalid) == aa25Case.aa25Admit {
				aa25T.Fatalf("aa25 nn=%d faults %v", aa25Case.aa25NN, aa25Self.aa25Faults())
			}
		})
	}
}

func TestAA25WithdrawalAfterCompleteDropsNackRetransmit(aa25T *aa25testing.T) {
	// Mutation caught: leaving the final-ACK wait active across a withdrawal, so
	// a later NACK resends a response whose admission was withdrawn.
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
	aa25Wire := aa25PreparedWire(aa25Prepared)
	aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
	aa25Self.aa25RunUntilCompletes(1, aa25TickAfter(aa25End, 2000, aa25Self))
	aa25Want := aa25Self.aa25ExpectedEdges(aa25Self.aa25FinalStart(), aa25Wire)
	if !aa25Self.aa25Engine.AA25OfferWithdraw() {
		aa25T.Fatal("aa25 withdraw refused")
	}
	aa25Self.aa25Step(aa25Self.aa25Now + 1)
	aa25SymbolEnd := aa25Self.aa25QueueFinalSymbol(0xFF, 1)
	aa25Self.aa25Run(aa25TickAfter(aa25SymbolEnd, 2000, aa25Self))
	if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
		aa25T.Fatalf("aa25 edges %v; want %v", aa25Self.aa25Writes, aa25Want)
	}
	if len(aa25Self.aa25Select(AA25EventTXComplete)) != 1 {
		aa25T.Fatalf("aa25 completes %d; want 1", len(aa25Self.aa25Select(AA25EventTXComplete)))
	}
}

func TestAA25FinalAckWaitExpiresBeforeLateNack(aa25T *aa25testing.T) {
	// Each row sends a NACK after the same completed response. Mutation caught:
	// an unbounded final-ACK wait that treats a stray 0xFF as a NACK.
	for _, aa25Case := range []struct {
		aa25Name   string
		aa25Delay  uint64
		aa25Resend bool
	}{{"within-budget", 1000, true}, {"after-budget", 300_000_000, false}} {
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			aa25Self := aa25NewBench(aa25T, aa25TestConfig())
			aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
			aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
			aa25Self.aa25RunUntilCompletes(1, aa25TickAfter(aa25End, 2000, aa25Self))
			aa25Done := aa25Self.aa25Select(AA25EventTXComplete)[0].AA25Tick
			aa25Writes := len(aa25Self.aa25Writes)
			aa25Self.aa25Run(aa25Done + aa25Case.aa25Delay)
			aa25Symbol := aa25Self.aa25Send(aa25Rat(aa25Self.aa25Now, 1), 0xFF)
			aa25Self.aa25Run(aa25TickAfter(aa25Symbol, 2000, aa25Self))
			if aa25Resent := len(aa25Self.aa25Writes) > aa25Writes; aa25Resent != aa25Case.aa25Resend {
				aa25T.Fatalf("aa25 resent %v want %v (faults %v)", aa25Resent, aa25Case.aa25Resend, aa25Self.aa25Faults())
			}
		})
	}
}

func TestAA25FinalSymbolAtResponseBoundaryIsNotUnsolicitedEcho(aa25T *aa25testing.T) {
	// The initiator starts its final symbol exactly where the response's last
	// stop bit ends. Mutation caught: judging that start before the fully echoed
	// response moves to the final-ACK wait, which faults it as unsolicited echo,
	// suppresses TXComplete, withdraws the response and drops the NACK resend.
	for _, aa25Case := range []struct {
		aa25Name    string
		aa25Symbol  byte
		aa25Resends int
	}{{"ack", 0x00, 0}, {"nack", 0xFF, 1}} {
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			// An integral sample period puts the response end on the sample grid,
			// so the final symbol's first low sample and the completion deadline
			// fall in the same pass.
			aa25Config := aa25TestConfig()
			aa25Config.AA25TickHz = 24_000_000
			aa25Self := aa25NewBench(aa25T, aa25Config)
			aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
			aa25Wire := aa25PreparedWire(aa25Prepared)
			aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
			aa25Self.aa25RunUntilEvent(AA25EventAdmitted, aa25TickAfter(aa25End, 2000, aa25Self))
			aa25Start := aa25Self.aa25FinalStart()
			aa25Want := aa25Self.aa25ExpectedEdges(aa25Start, aa25Wire)
			aa25Boundary := aa25Rat(aa25Start+aa25Self.aa25Config.AA25TurnaroundTicks, 1)
			aa25Span := aa25Rat(10+10*uint64(len(aa25Wire)), 1)
			aa25Boundary.Add(aa25Boundary, aa25Span.Mul(aa25Span, aa25Rat(aa25Self.aa25Hz, 2400)))
			aa25SymbolEnd := aa25Self.aa25Send(aa25Boundary, aa25Case.aa25Symbol)
			if aa25Case.aa25Resends > 0 {
				aa25Self.aa25RunUntilCompletes(2, aa25TickAfter(aa25SymbolEnd, 2000, aa25Self))
				aa25Want = append(aa25Want, aa25Self.aa25ExpectedEdges(aa25Self.aa25FinalStart(), aa25Wire[1:])...)
			}
			aa25Self.aa25Run(aa25TickAfter(aa25SymbolEnd, 2000, aa25Self))
			if aa25Self.aa25HasFault(AA25FaultEchoUnsolicited) {
				aa25T.Fatalf("aa25 boundary symbol faulted as unsolicited echo: %v", aa25Self.aa25Faults())
			}
			if aa25Got := len(aa25Self.aa25Select(AA25EventTXComplete)); aa25Got != 1+aa25Case.aa25Resends {
				aa25T.Fatalf("aa25 completes %d; want %d (faults %v)", aa25Got, 1+aa25Case.aa25Resends, aa25Self.aa25Faults())
			}
			if !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
				aa25T.Fatalf("aa25 edges %v; want %v", aa25Self.aa25Writes, aa25Want)
			}
		})
	}
}

func TestAA25StartBeforeResponseEndIsUnsolicited(aa25T *aa25testing.T) {
	// The initiator's start is observed 0, 1 or 3 samples relative to the end of
	// the response's last character. Mutation caught: completing the response
	// from the start shortcut without requiring the completion deadline, which
	// publishes TXComplete early and accepts the early symbol as a final ACK.
	for _, aa25Case := range []struct {
		aa25Name     string
		aa25EarlyBy  uint64
		aa25Unsolict bool
	}{{"control-at-boundary", 0, false}, {"one-sample-early", 1, true}, {"three-samples-early", 3, true}} {
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			aa25Config := aa25TestConfig()
			aa25Config.AA25TickHz = 24_000_000
			aa25Self := aa25NewBench(aa25T, aa25Config)
			aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
			aa25Wire := aa25PreparedWire(aa25Prepared)
			aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
			aa25Self.aa25RunUntilEvent(AA25EventAdmitted, aa25TickAfter(aa25End, 2000, aa25Self))
			aa25Start := aa25Self.aa25FinalStart()
			aa25Boundary := aa25Rat(aa25Start+aa25Self.aa25Config.AA25TurnaroundTicks, 1)
			aa25Span := aa25Rat(10+10*uint64(len(aa25Wire)), 1)
			aa25Boundary.Add(aa25Boundary, aa25Span.Mul(aa25Span, aa25Rat(aa25Self.aa25Hz, 2400)))
			aa25Boundary.Sub(aa25Boundary, aa25Rat(aa25Case.aa25EarlyBy*(aa25Self.aa25Hz/24000), 1))
			aa25SymbolEnd := aa25Self.aa25Send(aa25Boundary, 0xFF)
			aa25Self.aa25Run(aa25TickAfter(aa25SymbolEnd, 2000, aa25Self))
			if aa25Got := aa25Self.aa25HasFault(AA25FaultEchoUnsolicited); aa25Got != aa25Case.aa25Unsolict {
				aa25T.Fatalf("aa25 unsolicited fault %v want %v (faults %v)", aa25Got, aa25Case.aa25Unsolict, aa25Self.aa25Faults())
			}
			aa25Completes := len(aa25Self.aa25Select(AA25EventTXComplete))
			if aa25Case.aa25Unsolict && aa25Completes != 0 {
				aa25T.Fatalf("aa25 completes %d before the response end", aa25Completes)
			}
			if !aa25Case.aa25Unsolict && aa25Completes != 2 {
				aa25T.Fatalf("aa25 completes %d; want 2 (response then NACK resend)", aa25Completes)
			}
		})
	}
}

func TestAA25WithdrawalDuringTransmitCancelsNackRetransmit(aa25T *aa25testing.T) {
	// Withdrawal arrives while the response is on the wire; the response still
	// finishes. Mutation caught: letting completion enter the final-ACK wait
	// without retaining the cancellation, so a later NACK retransmits anyway.
	for _, aa25Case := range []struct {
		aa25Name     string
		aa25Withdraw bool
	}{{"control-no-withdraw", false}, {"withdrawn", true}} {
		aa25T.Run(aa25Case.aa25Name, func(aa25T *aa25testing.T) {
			aa25Self := aa25NewBench(aa25T, aa25TestConfig())
			aa25Prepared := aa25Self.aa25Offer(1, aa25TestPB, aa25TestSB, 1, 0xAB)
			aa25Wire := aa25PreparedWire(aa25Prepared)
			aa25End := aa25Self.aa25SendRequest(aa25TestRequest(aa25TestResponder, aa25TestPB, aa25TestSB, 0x01))
			aa25Self.aa25RunUntilEvent(AA25EventTXIntent, aa25TickAfter(aa25End, 2000, aa25Self))
			if aa25Case.aa25Withdraw {
				if !aa25Self.aa25Engine.AA25OfferWithdraw() {
					aa25T.Fatal("aa25 withdraw refused")
				}
				aa25Self.aa25Step(aa25Self.aa25Now)
			}
			aa25Self.aa25RunUntilCompletes(1, aa25TickAfter(aa25End, 4000, aa25Self))
			aa25Want := aa25Self.aa25ExpectedEdges(aa25Self.aa25FinalStart(), aa25Wire)
			aa25SymbolEnd := aa25Self.aa25QueueFinalSymbol(0xFF, 1)
			aa25Self.aa25Run(aa25TickAfter(aa25SymbolEnd, 2000, aa25Self))
			if aa25Resent := len(aa25Self.aa25Writes) > len(aa25Want); aa25Resent == aa25Case.aa25Withdraw {
				aa25T.Fatalf("aa25 resent %v with withdraw %v (faults %v)", aa25Resent, aa25Case.aa25Withdraw, aa25Self.aa25Faults())
			}
			if aa25Case.aa25Withdraw && !aa25EqualEdges(aa25Self.aa25Writes, aa25Want) {
				aa25T.Fatalf("aa25 edges %v; want %v", aa25Self.aa25Writes, aa25Want)
			}
		})
	}
}

func TestAA25OfferRejectsUnpreparedResponse(aa25T *aa25testing.T) {
	aa25Self := aa25NewBench(aa25T, aa25TestConfig())
	if aa25Self.aa25Engine.AA25OfferPrepared(AA25Prepared{AA25Generation: 1, AA25MatchPB: aa25TestPB, AA25MatchSB: aa25TestSB, AA25MatchNN: 1}) {
		aa25T.Fatal("aa25 hand-built prepared value was queued")
	}
	if aa25Self.aa25Engine.AA25OfferPrepared(AA25Prepared{}) {
		aa25T.Fatal("aa25 zero prepared value was queued")
	}
	aa25Valid, aa25Err := AA25PrepareResponse(1, aa25TestPB, aa25TestSB, 1, []byte{0xAB})
	if aa25Err != nil || !aa25Self.aa25Engine.AA25OfferPrepared(aa25Valid) {
		aa25T.Fatalf("aa25 prepared value rejected: %v", aa25Err)
	}
}
