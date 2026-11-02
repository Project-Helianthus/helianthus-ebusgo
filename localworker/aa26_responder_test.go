package localworker

import (
	aa26sha256 "crypto/sha256"
	aa26errorstest "errors"
	aa26ast "go/ast"
	aa26parser "go/parser"
	aa26token "go/token"
	aa26strings "strings"
	aa26testing "testing"
)

// These tests drive one serialized responder over synthetic frames, injected time
// and in-memory ports. They prove model rules only, never wire timing on hardware.

const (
	aa26TestHz uint64 = 24000
	// aa26TestCharacter is ten bits of ten ticks: 24000 ticks per second at 2400 Bd.
	aa26TestCharacter uint64 = 100
	// aa26TestStopCentre is where the AA25 engine reports a symbol: the last central
	// sample of the stop bit, before that character ends.
	aa26TestStopCentre    uint64 = 96
	aa26TestTurnaround    uint64 = 7
	aa26TestEchoAllowance uint64 = 20
	aa26TestIdle          uint64 = 2000

	aa26TestWorker         = AA23WorkerID(41)
	aa26TestStream         = AA23StreamGeneration(51)
	aa26TestSource         = AA23SourceGeneration(61)
	aa26TestAddress        = byte(0x15)
	aa26TestOtherAddress   = byte(0x26)
	aa26TestInitiator      = byte(0x10)
	aa26TestOtherInitiator = byte(0x31)
	aa26TestPB             = byte(0x07)
	aa26TestSB             = byte(0x04)
	aa26TestFar            = AA23Tick(500000)
	aa26TestSetupTick      = uint64(100)
	aa26TestFrameTick      = uint64(1000)
)

// aa26TestRequestData and aa26TestResponseData carry both symbols that need escaping.
var (
	aa26TestRequestData  = []byte{0x01, 0xA9, 0xAA}
	aa26TestResponseData = []byte{'F', 0xA9, 0xAA, '1'}
)

var aa26TestSilence = AA23FailurePolicy{
	AA23Unavailable: AA23FailureSilence,
	AA23Malformed:   AA23FailureSilence,
	AA23Timeout:     AA23FailureSilence,
}

var aa26TestLimits = AA23Limits{
	AA23MaxSubscribers:          4,
	AA23MaxRegistrations:        4,
	AA23MaxMasters:              2,
	AA23MaxPlanSlots:            4,
	AA23MaxEntriesPerPlan:       4,
	AA23MaxRequestBytes:         16,
	AA23MaxEncodedResponseBytes: 40,
	AA23MaxPlanBytes:            128,
	AA23MaxLeaseTicks:           1000000,
}

func aa26TestConfig(aa26Capacity uint32) AA26Config {
	return AA26Config{
		AA26TickHz:             aa26TestHz,
		AA26ClockDomain:        AA24DefaultClockDomain,
		AA26TurnaroundTicks:    aa26TestTurnaround,
		AA26EchoAllowanceTicks: aa26TestEchoAllowance,
		AA26IdleCloseTicks:     aa26TestIdle,
		AA26ObserverCapacity:   aa26Capacity,
	}
}

func aa26TestBuild() AA24BuildIdentity {
	return AA24BuildIdentity(aa26sha256.Sum256([]byte("aa26 synthetic build")))
}

// aa26TestExpand is the reference escaping, written apart from production code.
func aa26TestExpand(aa26Logical []byte) []byte {
	aa26Wire := make([]byte, 0, 2*len(aa26Logical))
	for _, aa26Value := range aa26Logical {
		switch aa26Value {
		case 0xA9:
			aa26Wire = append(aa26Wire, 0xA9, 0x00)
		case 0xAA:
			aa26Wire = append(aa26Wire, 0xA9, 0x01)
		default:
			aa26Wire = append(aa26Wire, aa26Value)
		}
	}
	return aa26Wire
}

// aa26TestCRC divides by x^8+0x9B over GF(2), one wire symbol at a time.
func aa26TestCRC(aa26Logical []byte) byte {
	aa26Remainder := uint16(0)
	for _, aa26Symbol := range aa26TestExpand(aa26Logical) {
		aa26Value := aa26Remainder << 8
		for aa26Bit := 15; aa26Bit >= 8; aa26Bit-- {
			if aa26Value&(1<<aa26Bit) != 0 {
				aa26Value ^= 0x19B << (aa26Bit - 8)
			}
		}
		aa26Remainder = aa26Value ^ uint16(aa26Symbol)
	}
	return byte(aa26Remainder)
}

func aa26TestRequestWire(aa26Source byte, aa26Target byte, aa26PB byte, aa26SB byte, aa26Data []byte) []byte {
	aa26Logical := append([]byte{aa26Source, aa26Target, aa26PB, aa26SB, byte(len(aa26Data))}, aa26Data...)
	return aa26TestExpand(append(aa26Logical, aa26TestCRC(aa26Logical)))
}

func aa26TestEncode(aa26Data []byte) []byte {
	aa26Logical := append([]byte{byte(len(aa26Data))}, aa26Data...)
	return aa26TestExpand(append(aa26Logical, aa26TestCRC(aa26Logical)))
}

func TestAA26ReferenceCRCVectors(aa26T *aa26testing.T) {
	for _, aa26Case := range []struct {
		aa26Logical []byte
		aa26Want    byte
	}{
		{nil, 0x00},
		{[]byte{0x01, 0x02}, 0x99},
		{[]byte{0x10, 0xFE, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA}, 0x77},
	} {
		if aa26Got := aa26TestCRC(aa26Case.aa26Logical); aa26Got != aa26Case.aa26Want {
			aa26T.Fatalf("aa26 reference CRC(% X) = %02X; want %02X", aa26Case.aa26Logical, aa26Got, aa26Case.aa26Want)
		}
	}
	aa26Encoded := aa26TestEncode(aa26TestResponseData)
	aa26Production, aa26Err := AA23EncodeResponse(nil, aa26TestResponseData)
	if aa26Err != nil || string(aa26Production) != string(aa26Encoded) {
		aa26T.Fatalf("aa26 reference encoding % X differs from AA23 % X (%v)", aa26Encoded, aa26Production, aa26Err)
	}
}

type aa26TestEmit struct {
	aa26Symbol    byte
	aa26NotBefore uint64
}

// aa26TestWire records every port call. With a line attached it also drives the
// emitted character onto that line, so the AA25 engine observes the echo.
type aa26TestWire struct {
	aa26Emits  []aa26TestEmit
	aa26Starts []AA24CommandIdentity
	aa26Refuse bool
	aa26Line   *aa26TestLine
}

func (aa26Wire *aa26TestWire) AA26EmitResponderSymbol(aa26Symbol byte, aa26NotBefore uint64) bool {
	if aa26Wire.aa26Refuse {
		return false
	}
	if aa26Wire.aa26Line != nil {
		if aa26NotBefore <= aa26Wire.aa26Line.aa26Now {
			return false
		}
		aa26Wire.aa26Line.aa26Paint(aa26NotBefore, aa26Symbol, true)
	}
	aa26Wire.aa26Emits = append(aa26Wire.aa26Emits, aa26TestEmit{aa26Symbol: aa26Symbol, aa26NotBefore: aa26NotBefore})
	return true
}

func (aa26Wire *aa26TestWire) AA26StartMaster(aa26Identity AA24CommandIdentity, aa26Initiator byte) bool {
	aa26Wire.aa26Starts = append(aa26Wire.aa26Starts, aa26Identity)
	return true
}

type aa26Options struct {
	aa26Capacity    uint32
	aa26PlanExpiry  AA23Tick
	aa26Lease       AA23Tick
	aa26SourceLease AA23Tick
	aa26Request     []byte
}

func aa26DefaultOptions() aa26Options {
	return aa26Options{
		aa26Capacity:    256,
		aa26PlanExpiry:  aa26TestFar,
		aa26Lease:       aa26TestFar,
		aa26SourceLease: aa26TestFar,
		aa26Request:     aa26TestRequestData,
	}
}

type aa26Fixture struct {
	aa26T         *aa26testing.T
	aa26Options   aa26Options
	aa26Registry  *AA23Registry
	aa26View      *AA23WorkerView
	aa26Host      *AA24Host
	aa26IPC       *AA24Worker
	aa26Wire      *aa26TestWire
	aa26Responder *AA26Responder
	aa26Tick      uint64
	aa26Stream    AA23StreamGeneration
	aa26Key       AA23RegistrationKey
	aa26Notice    AA23PublishNotice
	aa26Ref       AA24PlanRef
	aa26Echoed    int
}

func aa26NewFixture(aa26T *aa26testing.T, aa26Opts aa26Options) *aa26Fixture {
	aa26T.Helper()
	aa26Registry, aa26Err := AA23NewRegistry(aa26TestLimits, 0)
	if aa26Err != nil {
		aa26T.Fatalf("aa26 registry: %v", aa26Err)
	}
	aa26View, aa26Err := AA23NewWorkerView(aa26Registry.AA23Pool(), aa26TestWorker, aa26TestStream)
	if aa26Err != nil {
		aa26T.Fatalf("aa26 view: %v", aa26Err)
	}
	aa26WantOK(aa26T, "bind worker", aa26Registry.AA23BindWorker(aa26TestWorker, aa26TestStream))
	aa26Subscriber, aa26Err := aa26Registry.AA23OpenSubscriber()
	aa26WantOK(aa26T, "open subscriber", aa26Err)
	aa26Self := &aa26Fixture{
		aa26T:        aa26T,
		aa26Options:  aa26Opts,
		aa26Registry: aa26Registry,
		aa26View:     aa26View,
		aa26Tick:     aa26TestSetupTick,
		aa26Stream:   aa26TestStream,
		aa26Key:      AA23RegistrationKey{AA23Subscriber: aa26Subscriber, AA23Address: aa26TestAddress, AA23Epoch: 1},
		aa26Wire:     &aa26TestWire{},
	}
	aa26Self.aa26Register(aa26Self.aa26Key)
	aa26Self.aa26Host, aa26Self.aa26IPC = aa26Attach(aa26T, aa26TestStream)
	aa26Self.aa26Responder, aa26Err = AA26NewResponder(aa26TestConfig(aa26Opts.aa26Capacity), aa26View, aa26Self.aa26IPC, aa26Self.aa26Wire)
	aa26WantOK(aa26T, "new responder", aa26Err)
	aa26Self.aa26Install(1, aa26Self.aa26Entry(aa26Opts.aa26Request, aa26TestResponseData))
	return aa26Self
}

func aa26Attach(aa26T *aa26testing.T, aa26Stream AA23StreamGeneration) (*AA24Host, *AA24Worker) {
	aa26T.Helper()
	aa26Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa26Binding := AA24Binding{AA24Build: aa26TestBuild(), AA24Worker: aa26TestWorker, AA24Stream: aa26Stream}
	aa26Expected, _, aa26Err := AA24InitMapping(aa26Mem, aa26Binding, aa24FixtureConfig, aa24FixtureLimits)
	aa26WantOK(aa26T, "init mapping", aa26Err)
	aa26Host, aa26Err := AA24AttachHost(aa26Mem, aa26Expected)
	aa26WantOK(aa26T, "attach host", aa26Err)
	aa26IPC, aa26Err := AA24AttachWorker(aa26Mem, aa26Expected)
	aa26WantOK(aa26T, "attach worker", aa26Err)
	return aa26Host, aa26IPC
}

func aa26WantOK(aa26T *aa26testing.T, aa26Label string, aa26Err error) {
	aa26T.Helper()
	if aa26Err != nil {
		aa26T.Fatalf("aa26 %s: %v", aa26Label, aa26Err)
	}
}

func aa26WantErr(aa26T *aa26testing.T, aa26Label string, aa26Got error, aa26Want error) {
	aa26T.Helper()
	if !aa26errorstest.Is(aa26Got, aa26Want) {
		aa26T.Fatalf("aa26 %s: err = %v, want %v", aa26Label, aa26Got, aa26Want)
	}
}

func (aa26Self *aa26Fixture) aa26Now() AA23ClockReading {
	return AA23ClockReading{AA23Domain: AA24DefaultClockDomain, AA23Tick: AA23Tick(aa26Self.aa26Tick)}
}

func (aa26Self *aa26Fixture) aa26At(aa26Tick uint64) {
	if aa26Tick < aa26Self.aa26Tick {
		aa26Self.aa26T.Fatalf("aa26 fixture time runs backwards: %d < %d", aa26Tick, aa26Self.aa26Tick)
	}
	aa26Self.aa26Tick = aa26Tick
}

func (aa26Self *aa26Fixture) aa26Register(aa26Key AA23RegistrationKey) {
	aa26Self.aa26T.Helper()
	_, aa26Err := aa26Self.aa26Registry.AA23Register(AA23Registration{
		AA23Key:               aa26Key,
		AA23LeaseExpiry:       aa26Self.aa26Options.aa26Lease,
		AA23Source:            aa26TestSource,
		AA23SourceLeaseExpiry: aa26Self.aa26Options.aa26SourceLease,
		AA23Failure:           aa26TestSilence,
	}, aa26Self.aa26Now())
	aa26WantOK(aa26Self.aa26T, "register", aa26Err)
}

func (aa26Self *aa26Fixture) aa26Entry(aa26Request []byte, aa26Response []byte) AA23PlanEntry {
	return AA23PlanEntry{
		AA23Source:          AA23SourceConstraint{AA23Source: aa26TestInitiator},
		AA23Destination:     aa26Self.aa26Key.AA23Address,
		AA23PB:              aa26TestPB,
		AA23SB:              aa26TestSB,
		AA23RequestLength:   uint32(len(aa26Request)),
		AA23Request:         append([]byte(nil), aa26Request...),
		AA23Operation:       AA23OperationRead,
		AA23AllowedPhase:    AA23PhaseResponse,
		AA23MaxEncodedBytes: 40,
		AA23EncodedResponse: aa26TestEncode(aa26Response),
	}
}

func (aa26Self *aa26Fixture) aa26Identity(aa26Plan AA23PlanGeneration) AA23PlanIdentity {
	return AA23PlanIdentity{
		AA23Registration: aa26Self.aa26Key,
		AA23Worker:       aa26TestWorker,
		AA23Stream:       aa26Self.aa26Stream,
		AA23Plan:         aa26Plan,
		AA23Source:       aa26TestSource,
	}
}

func (aa26Self *aa26Fixture) aa26RefFor(aa26Plan AA23PlanGeneration) AA24PlanRef {
	return AA24PlanRef{
		AA24Key:        aa26Self.aa26Key,
		AA24Worker:     aa26TestWorker,
		AA24Stream:     aa26Self.aa26Stream,
		AA24Plan:       aa26Plan,
		AA24PlanExpiry: aa26Self.aa26Options.aa26PlanExpiry,
	}
}

// aa26Publish publishes one generation on both control planes and returns the
// AA23 notice; the AA24 lane carries aa26Published, which tests may vary.
func (aa26Self *aa26Fixture) aa26Publish(aa26Plan AA23PlanGeneration, aa26Published AA24PlanRef, aa26Entries ...AA23PlanEntry) AA23PublishNotice {
	aa26Self.aa26T.Helper()
	aa26Slot, aa26Err := aa26Self.aa26Registry.AA23PreparePlan(AA23PlanCandidate{
		AA23Identity:   aa26Self.aa26Identity(aa26Plan),
		AA23PlanExpiry: aa26Self.aa26Options.aa26PlanExpiry,
		AA23Failure:    aa26TestSilence,
		AA23Entries:    aa26Entries,
	}, aa26Self.aa26Now())
	aa26WantOK(aa26Self.aa26T, "prepare plan", aa26Err)
	aa26Notice, aa26Err := aa26Self.aa26Registry.AA23PublishPrepared(aa26Slot, aa26Self.aa26Now())
	aa26WantOK(aa26Self.aa26T, "publish plan", aa26Err)
	aa26Arena := aa26Self.aa26Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa26WantOK(aa26Self.aa26T, "aa24 publish", aa26Self.aa26Host.AA24PublishPlan(aa26Published, aa26Arena, 8, aa26Self.aa26Now()))
	aa26WantOK(aa26Self.aa26T, "aa24 adopt", aa26Self.aa26IPC.AA24PollControl(aa26Self.aa26Now()))
	return aa26Notice
}

func (aa26Self *aa26Fixture) aa26Install(aa26Plan AA23PlanGeneration, aa26Entries ...AA23PlanEntry) {
	aa26Self.aa26T.Helper()
	aa26Ref := aa26Self.aa26RefFor(aa26Plan)
	aa26Notice := aa26Self.aa26Publish(aa26Plan, aa26Ref, aa26Entries...)
	aa26Ack, aa26Ready, aa26Err := aa26Self.aa26Responder.AA26AdoptPlan(aa26Notice, aa26Ref, aa26Self.aa26Tick)
	aa26WantOK(aa26Self.aa26T, "adopt plan", aa26Err)
	if aa26Ready {
		aa26WantOK(aa26Self.aa26T, "predecessor acknowledgement", aa26Self.aa26Registry.AA23ObserveAcknowledgement(aa26Ack))
	}
	aa26Self.aa26Notice = aa26Notice
	aa26Self.aa26Ref = aa26Ref
}

// aa26Symbol delivers one raw symbol the way the AA25 engine reports it.
func (aa26Self *aa26Fixture) aa26Symbol(aa26Raw byte, aa26Start uint64) {
	aa26Self.aa26Responder.AA25TryPublish(AA25Event{
		AA25Kind:      AA25EventRawSymbol,
		AA25StartTick: aa26Start,
		AA25Tick:      aa26Start + aa26TestStopCentre,
		AA25Raw:       aa26Raw,
	})
	if aa26Start+aa26TestStopCentre > aa26Self.aa26Tick {
		aa26Self.aa26Tick = aa26Start + aa26TestStopCentre
	}
}

// aa26Send delivers contiguous characters and returns the end of the last one.
func (aa26Self *aa26Fixture) aa26Send(aa26Start uint64, aa26Raws ...byte) uint64 {
	for _, aa26Raw := range aa26Raws {
		aa26Self.aa26Symbol(aa26Raw, aa26Start)
		aa26Start += aa26TestCharacter
	}
	return aa26Start
}

// aa26EchoPending loops every emitted symbol back as its own echo, starting at its
// scheduled tick, and returns the end of the last echoed character.
func (aa26Self *aa26Fixture) aa26EchoPending() uint64 {
	aa26End := uint64(0)
	for aa26Self.aa26Echoed < len(aa26Self.aa26Wire.aa26Emits) {
		aa26Emit := aa26Self.aa26Wire.aa26Emits[aa26Self.aa26Echoed]
		aa26Self.aa26Echoed++
		aa26Self.aa26Symbol(aa26Emit.aa26Symbol, aa26Emit.aa26NotBefore)
		aa26End = aa26Emit.aa26NotBefore + aa26TestCharacter
	}
	return aa26End
}

// aa26Request sends SYN plus the fixture request and returns the end of its CRC.
func (aa26Self *aa26Fixture) aa26Request(aa26Start uint64) uint64 {
	aa26Wire := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26Self.aa26Options.aa26Request)
	return aa26Self.aa26Send(aa26Start, append([]byte{0xAA}, aa26Wire...)...)
}

// aa26Complete runs one whole exchange and returns the end of the initiator ACK.
func (aa26Self *aa26Fixture) aa26Complete(aa26Start uint64) uint64 {
	aa26Self.aa26Request(aa26Start)
	aa26End := aa26Self.aa26EchoPending()
	if aa26End == 0 {
		aa26Self.aa26T.Fatal("aa26 exchange emitted nothing")
	}
	return aa26Self.aa26Send(aa26End, 0x00)
}

func (aa26Self *aa26Fixture) aa26Drain() []AA26ObserverEvent {
	return aa26Self.aa26Responder.AA26DrainObserver(make([]AA26ObserverEvent, 0, 512))
}

func (aa26Self *aa26Fixture) aa26ExpectedEmits(aa26CRCStart uint64) []aa26TestEmit {
	aa26Ack := aa26CRCStart + aa26TestCharacter + aa26TestTurnaround
	aa26Result := []aa26TestEmit{{aa26Symbol: 0x00, aa26NotBefore: aa26Ack}}
	for aa26Index, aa26Symbol := range aa26TestEncode(aa26TestResponseData) {
		aa26Result = append(aa26Result, aa26TestEmit{aa26Symbol: aa26Symbol, aa26NotBefore: aa26Ack + uint64(aa26Index+1)*aa26TestCharacter})
	}
	return aa26Result
}

func aa26EqualEmits(aa26Left []aa26TestEmit, aa26Right []aa26TestEmit) bool {
	if len(aa26Left) != len(aa26Right) {
		return false
	}
	for aa26Index := range aa26Left {
		if aa26Left[aa26Index] != aa26Right[aa26Index] {
			return false
		}
	}
	return true
}

func aa26RawOf(aa26Events []AA26ObserverEvent) []byte {
	var aa26Result []byte
	for _, aa26Event := range aa26Events {
		if aa26Event.AA26Kind == AA26ObserverRaw {
			aa26Result = append(aa26Result, aa26Event.AA26Raw)
		}
	}
	return aa26Result
}

func aa26LastClosed(aa26T *aa26testing.T, aa26Events []AA26ObserverEvent) AA26ObserverEvent {
	aa26T.Helper()
	for aa26Index := len(aa26Events) - 1; aa26Index >= 0; aa26Index-- {
		if aa26Events[aa26Index].AA26Kind == AA26ObserverClosed {
			return aa26Events[aa26Index]
		}
	}
	aa26T.Fatal("aa26 no closure event")
	return AA26ObserverEvent{}
}

// aa26WantClosedClean proves that nothing of a closed transaction survives: no
// active key, no pin in the worker view and no prepared or awaited symbol.
func (aa26Self *aa26Fixture) aa26WantClosedClean(aa26Label string) {
	aa26Self.aa26T.Helper()
	aa26Responder := aa26Self.aa26Responder
	if _, aa26Active := aa26Responder.AA26Active(); aa26Active {
		aa26Self.aa26T.Fatalf("aa26 %s: transaction still active", aa26Label)
	}
	if aa26Self.aa26View.aa23PinActive {
		aa26Self.aa26T.Fatalf("aa26 %s: worker view still holds a pin", aa26Label)
	}
	if aa26Responder.aa26Prepared || aa26Responder.aa26ReplyLen != 0 || aa26Responder.aa26Offset != 0 ||
		aa26Responder.aa26AwaitEcho || aa26Responder.aa26Pin != (AA23PlanPin{}) || aa26Responder.aa26Ref != (AA24PlanRef{}) {
		aa26Self.aa26T.Fatalf("aa26 %s: per-transaction state survived closure", aa26Label)
	}
}

func TestAA26ConstructionRefusesMissingOrMismatchedParts(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Config := aa26TestConfig(4)
	aa26Wire := &aa26TestWire{}
	_, aa26Err := AA26NewResponder(aa26Config, nil, aa26Self.aa26IPC, aa26Wire)
	aa26WantErr(aa26T, "nil view", aa26Err, AA26ErrInvalidConfig)
	_, aa26Err = AA26NewResponder(aa26Config, aa26Self.aa26View, nil, aa26Wire)
	aa26WantErr(aa26T, "nil ipc", aa26Err, AA26ErrInvalidConfig)
	_, aa26Err = AA26NewResponder(aa26Config, aa26Self.aa26View, aa26Self.aa26IPC, nil)
	aa26WantErr(aa26T, "nil wire", aa26Err, AA26ErrInvalidConfig)
	for _, aa26Case := range []struct {
		aa26Name   string
		aa26Mutate func(*AA26Config)
	}{
		{"tick rate below the sample rate", func(aa26Value *AA26Config) { aa26Value.AA26TickHz = AA25NominalSampleRate - 1 }},
		{"zero clock domain", func(aa26Value *AA26Config) { aa26Value.AA26ClockDomain = 0 }},
		{"turnaround beyond one second", func(aa26Value *AA26Config) { aa26Value.AA26TurnaroundTicks = aa26TestHz + 1 }},
		{"echo allowance of a whole character", func(aa26Value *AA26Config) { aa26Value.AA26EchoAllowanceTicks = aa26TestCharacter }},
		{"idle bound below a character", func(aa26Value *AA26Config) { aa26Value.AA26IdleCloseTicks = aa26TestCharacter - 1 }},
		{"zero observer capacity", func(aa26Value *AA26Config) { aa26Value.AA26ObserverCapacity = 0 }},
		{"observer capacity above the ceiling", func(aa26Value *AA26Config) { aa26Value.AA26ObserverCapacity = aa26ObserverCeiling + 1 }},
	} {
		aa26Mutated := aa26Config
		aa26Case.aa26Mutate(&aa26Mutated)
		_, aa26Err = AA26NewResponder(aa26Mutated, aa26Self.aa26View, aa26Self.aa26IPC, aa26Wire)
		aa26WantErr(aa26T, aa26Case.aa26Name, aa26Err, AA26ErrInvalidConfig)
	}
	aa26Other := aa26Config
	aa26Other.AA26ClockDomain = AA24DefaultClockDomain + 1
	_, aa26Err = AA26NewResponder(aa26Other, aa26Self.aa26View, aa26Self.aa26IPC, aa26Wire)
	aa26WantErr(aa26T, "clock domain", aa26Err, AA26ErrIdentity)
	_, aa26OtherStream := aa26Attach(aa26T, aa26TestStream+1)
	_, aa26Err = AA26NewResponder(aa26Config, aa26Self.aa26View, aa26OtherStream, aa26Wire)
	aa26WantErr(aa26T, "stream generation", aa26Err, AA26ErrIdentity)
	aa26OtherView, aa26Err := AA23NewWorkerView(aa26Self.aa26Registry.AA23Pool(), aa26TestWorker+1, aa26TestStream)
	aa26WantOK(aa26T, "other worker view", aa26Err)
	_, aa26Err = AA26NewResponder(aa26Config, aa26OtherView, aa26Self.aa26IPC, aa26Wire)
	aa26WantErr(aa26T, "worker incarnation", aa26Err, AA26ErrIdentity)
}

func TestAA26SelectsAtDestinationAndDeliversThePrefixAtOnce(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Wire := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)
	aa26Start := aa26TestFrameTick
	aa26Self.aa26Send(aa26Start, 0xAA, aa26Wire[0])
	if _, aa26Active := aa26Self.aa26Responder.AA26Active(); aa26Active || len(aa26Self.aa26Drain()) != 0 {
		aa26T.Fatal("aa26 selection or delivery before the destination symbol")
	}
	aa26Self.aa26Symbol(aa26Wire[1], aa26Start+2*aa26TestCharacter)
	aa26Key, aa26Active := aa26Self.aa26Responder.AA26Active()
	if !aa26Active || !aa26Self.aa26View.aa23PinActive {
		aa26T.Fatal("aa26 no provisional selection at the destination symbol")
	}
	if aa26Key.AA23Identity != aa26Self.aa26Notice.AA23Identity || aa26Key.AA23Transaction != 1 {
		aa26T.Fatalf("aa26 selected key %+v; want identity %+v transaction 1", aa26Key, aa26Self.aa26Notice.AA23Identity)
	}
	aa26Prefix := aa26Self.aa26Drain()
	aa26Want := []AA26ObserverEvent{
		{AA26Kind: AA26ObserverRaw, AA26Raw: 0xAA, AA26Sequence: 1, AA26StartTick: aa26Start},
		{AA26Kind: AA26ObserverRaw, AA26Raw: aa26TestInitiator, AA26Sequence: 2, AA26StartTick: aa26Start + aa26TestCharacter},
		{AA26Kind: AA26ObserverLogical, AA26Logical: aa26TestInitiator, AA26Sequence: 2, AA26StartTick: aa26Start + aa26TestCharacter},
		{AA26Kind: AA26ObserverRaw, AA26Raw: aa26TestAddress, AA26Sequence: 3, AA26StartTick: aa26Start + 2*aa26TestCharacter},
		{AA26Kind: AA26ObserverLogical, AA26Logical: aa26TestAddress, AA26Sequence: 3, AA26StartTick: aa26Start + 2*aa26TestCharacter},
	}
	if len(aa26Prefix) != len(aa26Want) {
		aa26T.Fatalf("aa26 prefix has %d events, want %d: %+v", len(aa26Prefix), len(aa26Want), aa26Prefix)
	}
	for aa26Index, aa26Expected := range aa26Want {
		aa26Expected.AA26Key = aa26Key
		aa26Expected.AA26Tick = aa26Expected.AA26StartTick + aa26TestStopCentre
		aa26Expected.AA26Phase = AA23PhaseRequest
		if aa26Prefix[aa26Index] != aa26Expected {
			aa26T.Fatalf("aa26 prefix event %d = %+v; want %+v", aa26Index, aa26Prefix[aa26Index], aa26Expected)
		}
	}
	// Every later raw symbol is delivered on arrival; nothing is emitted before the CRC.
	aa26Next := aa26Start + 3*aa26TestCharacter
	for aa26Index := 2; aa26Index < len(aa26Wire)-1; aa26Index++ {
		aa26Self.aa26Symbol(aa26Wire[aa26Index], aa26Next)
		aa26Events := aa26Self.aa26Drain()
		if len(aa26Events) == 0 || aa26Events[0].AA26Kind != AA26ObserverRaw || aa26Events[0].AA26Raw != aa26Wire[aa26Index] ||
			aa26Events[0].AA26Sequence != uint64(aa26Index+2) {
			aa26T.Fatalf("aa26 symbol %d not delivered in order: %+v", aa26Index, aa26Events)
		}
		if len(aa26Self.aa26Wire.aa26Emits) != 0 {
			aa26T.Fatalf("aa26 emission before the CRC at symbol %d", aa26Index)
		}
		aa26Next += aa26TestCharacter
	}
	if !aa26Self.aa26Responder.aa26Prepared || aa26Self.aa26Responder.AA26Stats().AA26Prepared != 1 {
		aa26T.Fatal("aa26 response not prepared before the CRC")
	}
	aa26Self.aa26Symbol(aa26Wire[len(aa26Wire)-1], aa26Next)
	if len(aa26Self.aa26Wire.aa26Emits) != 1 {
		aa26T.Fatalf("aa26 CRC produced %d emissions, want the ACK", len(aa26Self.aa26Wire.aa26Emits))
	}
}

func TestAA26EscapePrefixIsRawAtOnceAndLogicalLater(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Wire := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)
	// Wire: QQ ZZ PB SB NN 01 A9 00 A9 01 CRC; index 6 is the first escape prefix.
	aa26Prefix := 6
	if aa26Wire[aa26Prefix] != 0xA9 || aa26Wire[aa26Prefix+1] != 0x00 {
		aa26T.Fatalf("aa26 fixture wire % X has no escape at %d", aa26Wire, aa26Prefix)
	}
	aa26End := aa26Self.aa26Send(aa26TestFrameTick, append([]byte{0xAA}, aa26Wire[:aa26Prefix+1]...)...)
	aa26Events := aa26Self.aa26Drain()
	aa26Last := aa26Events[len(aa26Events)-1]
	if aa26Last.AA26Kind != AA26ObserverRaw || aa26Last.AA26Raw != 0xA9 || aa26Last.AA26Sequence != uint64(aa26Prefix+2) {
		aa26T.Fatalf("aa26 escape prefix not delivered as a raw symbol at once: %+v", aa26Last)
	}
	aa26Self.aa26Symbol(aa26Wire[aa26Prefix+1], aa26End)
	aa26Events = aa26Self.aa26Drain()
	if len(aa26Events) != 2 || aa26Events[0].AA26Kind != AA26ObserverRaw || aa26Events[0].AA26Raw != 0x00 ||
		aa26Events[1].AA26Kind != AA26ObserverLogical || aa26Events[1].AA26Logical != 0xA9 || !aa26Events[1].AA26Escaped ||
		aa26Events[1].AA26Sequence != aa26Events[0].AA26Sequence {
		aa26T.Fatalf("aa26 escape suffix events = %+v", aa26Events)
	}
	// An escape prefix followed by anything but a suffix is malformed: silence.
	aa26Bad := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26BadWire := append(append([]byte{0xAA}, aa26Wire[:aa26Prefix+1]...), 0x02)
	aa26Bad.aa26Send(aa26TestFrameTick, aa26BadWire...)
	if len(aa26Bad.aa26Wire.aa26Emits) != 0 || aa26LastClosed(aa26T, aa26Bad.aa26Drain()).AA26Reason != AA26CloseMalformed {
		aa26T.Fatal("aa26 invalid escape suffix did not end in silence")
	}
	aa26Bad.aa26WantClosedClean("invalid escape")
}

func TestAA26AcknowledgesOnlyAValidCRCAfterItsCharacterEnds(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26End := aa26Self.aa26Request(aa26TestFrameTick)
	aa26CRCStart := aa26End - aa26TestCharacter
	if len(aa26Self.aa26Wire.aa26Emits) != 1 {
		aa26T.Fatalf("aa26 emissions after a valid CRC: %+v", aa26Self.aa26Wire.aa26Emits)
	}
	aa26Ack := aa26Self.aa26Wire.aa26Emits[0]
	// The decision happens at the stop-bit centre, but the ACK may only start once
	// the CRC character has ended and the turnaround has passed.
	if aa26Ack.aa26Symbol != 0x00 || aa26Ack.aa26NotBefore != aa26CRCStart+aa26TestCharacter+aa26TestTurnaround {
		aa26T.Fatalf("aa26 ACK = %+v; want 00 at %d", aa26Ack, aa26CRCStart+aa26TestCharacter+aa26TestTurnaround)
	}
	if aa26Self.aa26Tick >= aa26Ack.aa26NotBefore {
		aa26T.Fatal("aa26 decision tick is not before the scheduled ACK")
	}

	aa26BadCRC := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Wire := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)
	aa26Wire[len(aa26Wire)-1] ^= 0x01
	aa26BadCRC.aa26Send(aa26TestFrameTick, append([]byte{0xAA}, aa26Wire...)...)
	aa26Events := aa26BadCRC.aa26Drain()
	if len(aa26BadCRC.aa26Wire.aa26Emits) != 0 {
		aa26T.Fatalf("aa26 a bad CRC was answered: %+v", aa26BadCRC.aa26Wire.aa26Emits)
	}
	if string(aa26RawOf(aa26Events)) != string(append([]byte{0xAA}, aa26Wire...)) ||
		aa26LastClosed(aa26T, aa26Events).AA26Reason != AA26CloseMalformed {
		aa26T.Fatalf("aa26 bad CRC observation = %+v", aa26Events)
	}
	aa26BadCRC.aa26WantClosedClean("bad CRC")

	// A CRC that itself needs escaping is decided at its suffix, never at its prefix.
	aa26Data := []byte(nil)
	for aa26Value := 0; aa26Value < 256 && aa26Data == nil; aa26Value++ {
		aa26Candidate := []byte{byte(aa26Value)}
		aa26Logical := append([]byte{aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, 1}, aa26Candidate...)
		if aa26CRC := aa26TestCRC(aa26Logical); aa26CRC == 0xA9 || aa26CRC == 0xAA {
			aa26Data = aa26Candidate
		}
	}
	if aa26Data == nil {
		aa26T.Fatal("aa26 no single data byte yields an escaped CRC")
	}
	aa26Options := aa26DefaultOptions()
	aa26Options.aa26Request = aa26Data
	aa26Escaped := aa26NewFixture(aa26T, aa26Options)
	aa26EscapedWire := append([]byte{0xAA}, aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26Data)...)
	aa26Next := aa26Escaped.aa26Send(aa26TestFrameTick, aa26EscapedWire[:len(aa26EscapedWire)-1]...)
	if len(aa26Escaped.aa26Wire.aa26Emits) != 0 {
		aa26T.Fatal("aa26 ACK emitted on the escape prefix of the CRC")
	}
	aa26Escaped.aa26Symbol(aa26EscapedWire[len(aa26EscapedWire)-1], aa26Next)
	if len(aa26Escaped.aa26Wire.aa26Emits) != 1 || aa26Escaped.aa26Wire.aa26Emits[0].aa26NotBefore != aa26Next+aa26TestCharacter+aa26TestTurnaround {
		aa26T.Fatalf("aa26 escaped CRC emissions = %+v", aa26Escaped.aa26Wire.aa26Emits)
	}
}

func TestAA26CompletesThePreparedResponseWithoutMasterStart(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26RequestEnd := aa26Self.aa26Request(aa26TestFrameTick)
	aa26End := aa26Self.aa26EchoPending()
	aa26Expected := aa26Self.aa26ExpectedEmits(aa26RequestEnd - aa26TestCharacter)
	if !aa26EqualEmits(aa26Self.aa26Wire.aa26Emits, aa26Expected) {
		aa26T.Fatalf("aa26 emissions = %+v; want %+v", aa26Self.aa26Wire.aa26Emits, aa26Expected)
	}
	if _, aa26Active := aa26Self.aa26Responder.AA26Active(); !aa26Active {
		aa26T.Fatal("aa26 transaction closed before the initiator ACK")
	}
	aa26Self.aa26Send(aa26End, 0x00, 0xAA)
	if len(aa26Self.aa26Wire.aa26Starts) != 0 {
		aa26T.Fatalf("aa26 responder issued %d master starts", len(aa26Self.aa26Wire.aa26Starts))
	}
	aa26Events := aa26Self.aa26Drain()
	aa26WantRaw := append(append([]byte{0xAA}, aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)...), 0x00)
	aa26WantRaw = append(append(aa26WantRaw, aa26TestEncode(aa26TestResponseData)...), 0x00)
	if string(aa26RawOf(aa26Events)) != string(aa26WantRaw) {
		aa26T.Fatalf("aa26 observed raw % X; want % X", aa26RawOf(aa26Events), aa26WantRaw)
	}
	aa26Sequence := uint64(0)
	aa26Echoes := 0
	for _, aa26Event := range aa26Events {
		if aa26Event.AA26Kind != AA26ObserverRaw {
			continue
		}
		if aa26Event.AA26Sequence != aa26Sequence+1 {
			aa26T.Fatalf("aa26 raw sequence jumps from %d to %d", aa26Sequence, aa26Event.AA26Sequence)
		}
		aa26Sequence = aa26Event.AA26Sequence
		if aa26Event.AA26Echo {
			aa26Echoes++
		}
	}
	if aa26Echoes != len(aa26Expected) {
		aa26T.Fatalf("aa26 %d echoes marked; want %d", aa26Echoes, len(aa26Expected))
	}
	if aa26LastClosed(aa26T, aa26Events).AA26Reason != AA26CloseCompleted {
		aa26T.Fatalf("aa26 closure = %+v", aa26LastClosed(aa26T, aa26Events))
	}
	aa26Self.aa26WantClosedClean("completed")
	// A repeat is a new transaction with its own ordinal and its own checks.
	aa26Before := len(aa26Self.aa26Wire.aa26Emits)
	aa26Self.aa26Request(aa26End + 10*aa26TestCharacter)
	aa26Key, aa26Active := aa26Self.aa26Responder.AA26Active()
	if !aa26Active || aa26Key.AA23Transaction != 2 || len(aa26Self.aa26Wire.aa26Emits) != aa26Before+1 {
		aa26T.Fatalf("aa26 repeat transaction key=%+v active=%v emits=%d", aa26Key, aa26Active, len(aa26Self.aa26Wire.aa26Emits))
	}
}

// aa26TestLine is a wired-AND line of injected levels, one sample per tick.
type aa26TestLine struct {
	aa26Now   uint64
	aa26Base  uint64
	aa26Low   []bool
	aa26Edges uint64
}

func (aa26Line *aa26TestLine) AA25NowTicks() uint64 { return aa26Line.aa26Now }

func (aa26Line *aa26TestLine) AA25SampleLevel() AA25Level {
	aa26Index := aa26Line.aa26Now - aa26Line.aa26Base
	if aa26Index < uint64(len(aa26Line.aa26Low)) && aa26Line.aa26Low[aa26Index] {
		return AA25LevelLow
	}
	return AA25LevelHigh
}

func (aa26Line *aa26TestLine) AA25WriteEdge(AA25Level) { aa26Line.aa26Edges++ }

func (aa26Line *aa26TestLine) aa26Drive(aa26From uint64, aa26Ticks uint64) {
	for aa26Tick := aa26From; aa26Tick < aa26From+aa26Ticks; aa26Tick++ {
		aa26Line.aa26Low[aa26Tick-aa26Line.aa26Base] = true
	}
}

// aa26Paint drives one 8N1 character, least significant bit first.
func (aa26Line *aa26TestLine) aa26Paint(aa26Start uint64, aa26Symbol byte, aa26StopHigh bool) {
	aa26Bit := aa26TestCharacter / 10
	aa26Line.aa26Drive(aa26Start, aa26Bit)
	for aa26Index := uint64(0); aa26Index < 8; aa26Index++ {
		if aa26Symbol>>aa26Index&1 == 0 {
			aa26Line.aa26Drive(aa26Start+(aa26Index+1)*aa26Bit, aa26Bit)
		}
	}
	if !aa26StopHigh {
		aa26Line.aa26Drive(aa26Start+9*aa26Bit, aa26Bit)
	}
}

func (aa26Line *aa26TestLine) aa26PaintAll(aa26Start uint64, aa26Symbols []byte) uint64 {
	for _, aa26Symbol := range aa26Symbols {
		aa26Line.aa26Paint(aa26Start, aa26Symbol, true)
		aa26Start += aa26TestCharacter
	}
	return aa26Start
}

func aa26RunEngine(aa26Line *aa26TestLine, aa26Engine *AA25Engine, aa26Until uint64) {
	for aa26Line.aa26Now < aa26Until {
		aa26Line.aa26Now++
		aa26Engine.AA25Step()
	}
}

func aa26NewLoopback(aa26T *aa26testing.T) (*aa26Fixture, *aa26TestLine, *AA25Engine) {
	aa26T.Helper()
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Line := &aa26TestLine{aa26Now: aa26Self.aa26Tick + 1, aa26Base: aa26Self.aa26Tick + 1, aa26Low: make([]bool, 8000)}
	aa26Self.aa26Wire.aa26Line = aa26Line
	aa26Engine, aa26Err := AA25NewEngine(AA25Config{AA25TickHz: aa26TestHz, AA25ResponderAddress: 0x52},
		AA25Ports{AA25Levels: aa26Line, AA25Edges: aa26Line, AA25Ticks: aa26Line, AA25Sink: aa26Self.aa26Responder})
	aa26WantOK(aa26T, "aa25 engine", aa26Err)
	aa26Engine.AA25Step()
	return aa26Self, aa26Line, aa26Engine
}

func TestAA26EngineLoopbackCompletesThroughTheAA25WireModel(aa26T *aa26testing.T) {
	aa26Self, aa26Line, aa26Engine := aa26NewLoopback(aa26T)
	aa26Request := append([]byte{0xAA}, aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)...)
	aa26Start := aa26Line.aa26Base + 3*aa26TestCharacter
	aa26RequestEnd := aa26Line.aa26PaintAll(aa26Start, aa26Request)
	aa26Expected := aa26Self.aa26ExpectedEmits(aa26RequestEnd - aa26TestCharacter)
	aa26ResponseEnd := aa26Expected[len(aa26Expected)-1].aa26NotBefore + aa26TestCharacter
	aa26RunEngine(aa26Line, aa26Engine, aa26ResponseEnd)
	if !aa26EqualEmits(aa26Self.aa26Wire.aa26Emits, aa26Expected) {
		aa26T.Fatalf("aa26 loopback emissions = %+v; want %+v", aa26Self.aa26Wire.aa26Emits, aa26Expected)
	}
	aa26AckStart := aa26ResponseEnd + aa26TestCharacter/2
	aa26Line.aa26PaintAll(aa26AckStart, []byte{0x00, 0xAA})
	aa26RunEngine(aa26Line, aa26Engine, aa26AckStart+3*aa26TestCharacter)
	if aa26Line.aa26Edges != 0 || aa26Engine.AA25Stats().AA25TXEdges != 0 || len(aa26Self.aa26Wire.aa26Starts) != 0 {
		aa26T.Fatal("aa26 the AA25 engine transmitted or a master start was issued")
	}
	aa26Events := aa26Self.aa26Drain()
	aa26WantRaw := append(append([]byte(nil), aa26Request...), 0x00)
	aa26WantRaw = append(append(aa26WantRaw, aa26TestEncode(aa26TestResponseData)...), 0x00)
	if string(aa26RawOf(aa26Events)) != string(aa26WantRaw) {
		aa26T.Fatalf("aa26 loopback observed % X; want % X", aa26RawOf(aa26Events), aa26WantRaw)
	}
	if aa26LastClosed(aa26T, aa26Events).AA26Reason != AA26CloseCompleted {
		aa26T.Fatalf("aa26 loopback closure = %+v", aa26LastClosed(aa26T, aa26Events))
	}
	aa26Self.aa26WantClosedClean("loopback")

	// A CRC character whose stop bit is low never becomes a symbol: no ACK.
	aa26Bad, aa26BadLine, aa26BadEngine := aa26NewLoopback(aa26T)
	aa26Next := aa26BadLine.aa26PaintAll(aa26Start, aa26Request[:len(aa26Request)-1])
	aa26BadLine.aa26Paint(aa26Next, aa26Request[len(aa26Request)-1], false)
	aa26RunEngine(aa26BadLine, aa26BadEngine, aa26Next+5*aa26TestCharacter)
	if len(aa26Bad.aa26Wire.aa26Emits) != 0 {
		aa26T.Fatalf("aa26 ACK after a bad stop bit: %+v", aa26Bad.aa26Wire.aa26Emits)
	}
	if aa26Bad.aa26Responder.AA26Stats().AA26Closed[AA26CloseFraming] != 1 {
		aa26T.Fatalf("aa26 bad stop closure stats = %+v", aa26Bad.aa26Responder.AA26Stats())
	}
	aa26Bad.aa26WantClosedClean("bad stop")
}

func TestAA26SaturatedObserverNeverDelaysThePreparedACK(aa26T *aa26testing.T) {
	aa26Free := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26FreeEnd := aa26Free.aa26Complete(aa26TestFrameTick)
	aa26Options := aa26DefaultOptions()
	aa26Options.aa26Capacity = 1
	aa26Full := aa26NewFixture(aa26T, aa26Options)
	// Fill the single observer slot before the transaction and never drain it.
	aa26Full.aa26Responder.aa26Deliver(AA26ObserverEvent{AA26Kind: AA26ObserverRaw})
	aa26Wire := append([]byte{0xAA}, aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)...)
	aa26Next := aa26Full.aa26Send(aa26TestFrameTick, aa26Wire[:len(aa26Wire)-1]...)
	aa26Full.aa26Symbol(aa26Wire[len(aa26Wire)-1], aa26Next)
	// The ACK is scheduled inside the very call that delivered the CRC.
	if len(aa26Full.aa26Wire.aa26Emits) != 1 {
		aa26T.Fatal("aa26 ACK scheduling waited for the saturated observer")
	}
	aa26FullEnd := aa26Full.aa26Send(aa26Full.aa26EchoPending(), 0x00)
	if aa26FullEnd != aa26FreeEnd || !aa26EqualEmits(aa26Full.aa26Wire.aa26Emits, aa26Free.aa26Wire.aa26Emits) {
		aa26T.Fatalf("aa26 saturated emissions %+v differ from %+v", aa26Full.aa26Wire.aa26Emits, aa26Free.aa26Wire.aa26Emits)
	}
	aa26Stats := aa26Full.aa26Responder.AA26Stats()
	if aa26Stats.AA26ObserverDropped == 0 || aa26Stats.AA26Closed[AA26CloseCompleted] != 1 {
		aa26T.Fatalf("aa26 saturated stats = %+v", aa26Stats)
	}
	aa26Events := aa26Full.aa26Drain()
	if len(aa26Events) != 1 || aa26Events[0].AA26Key != (AA23WireKey{}) {
		aa26T.Fatalf("aa26 a full ring was overwritten: %+v", aa26Events)
	}
	if aa26Free.aa26Responder.AA26Stats().AA26ObserverDropped != 0 {
		aa26T.Fatal("aa26 control run dropped observer events")
	}
}

func TestAA26AdoptionRequiresTheExactAA24PlanReference(aa26T *aa26testing.T) {
	for _, aa26Case := range []struct {
		aa26Name   string
		aa26Mutate func(*AA24PlanRef)
	}{
		{"subscriber", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Key.AA23Subscriber++ }},
		{"address", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Key.AA23Address = aa26TestOtherAddress }},
		{"registration epoch", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Key.AA23Epoch++ }},
		{"worker", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Worker++ }},
		{"stream", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Stream++ }},
		{"plan generation", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Plan++ }},
		{"plan expiry", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24PlanExpiry-- }},
	} {
		aa26T.Run(aa26Case.aa26Name, func(aa26T *aa26testing.T) {
			aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
			aa26Ref := aa26Self.aa26RefFor(2)
			aa26Notice := aa26Self.aa26Publish(2, aa26Ref, aa26Self.aa26Entry(aa26TestRequestData, []byte{'N'}))
			aa26Mutated := aa26Ref
			aa26Case.aa26Mutate(&aa26Mutated)
			_, _, aa26Err := aa26Self.aa26Responder.AA26AdoptPlan(aa26Notice, aa26Mutated, aa26Self.aa26Tick)
			aa26WantErr(aa26T, "mutated adoption", aa26Err, AA26ErrAuthority)
			if aa26Self.aa26View.aa23PlanTop[aa26TestAddress] != 1 {
				aa26T.Fatal("aa26 a refused adoption reached the worker view")
			}
			// The exact pair still adopts afterwards: the refusal had no effect.
			_, _, aa26Err = aa26Self.aa26Responder.AA26AdoptPlan(aa26Notice, aa26Ref, aa26Self.aa26Tick)
			aa26WantOK(aa26T, "exact adoption", aa26Err)
		})
	}
}

func TestAA26SelectionRequiresTheSamePlanOnTheAA24Lane(aa26T *aa26testing.T) {
	for _, aa26Case := range []struct {
		aa26Name   string
		aa26Mutate func(*AA24PlanRef)
	}{
		{"exact control", func(*AA24PlanRef) {}},
		{"subscriber", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Key.AA23Subscriber++ }},
		{"address", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Key.AA23Address = aa26TestOtherAddress }},
		{"registration epoch", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Key.AA23Epoch++ }},
		{"plan generation", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24Plan++ }},
		{"plan expiry", func(aa26Ref *AA24PlanRef) { aa26Ref.AA24PlanExpiry-- }},
	} {
		aa26T.Run(aa26Case.aa26Name, func(aa26T *aa26testing.T) {
			aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
			aa26Ref := aa26Self.aa26RefFor(2)
			aa26Published := aa26Ref
			aa26Case.aa26Mutate(&aa26Published)
			aa26Notice := aa26Self.aa26Publish(2, aa26Published, aa26Self.aa26Entry(aa26TestRequestData, aa26TestResponseData))
			_, _, aa26Err := aa26Self.aa26Responder.AA26AdoptPlan(aa26Notice, aa26Ref, aa26Self.aa26Tick)
			aa26WantOK(aa26T, "adopt", aa26Err)
			aa26Self.aa26Request(aa26TestFrameTick)
			aa26Selected := aa26Self.aa26Responder.AA26Stats().AA26Selected == 1
			aa26Answered := len(aa26Self.aa26Wire.aa26Emits) == 1
			if aa26Case.aa26Name == "exact control" {
				if !aa26Selected || !aa26Answered {
					aa26T.Fatal("aa26 the exact AA24 reference was not admitted")
				}
				return
			}
			if aa26Selected || aa26Answered || len(aa26Self.aa26Drain()) != 0 {
				aa26T.Fatal("aa26 selected without the same plan on the AA24 lane")
			}
		})
	}
}

func aa26TestCommand(aa26Self *aa26Fixture, aa26Token AA24OwnerToken) AA24CommandIdentity {
	return AA24CommandIdentity{
		AA24Ref:             aa26Self.aa26Ref,
		AA24Transaction:     AA23TransactionOrdinal(aa26Token),
		AA24OwnerToken:      aa26Token,
		AA24OwnerExpiry:     400000,
		AA24RequestDeadline: 300000,
	}
}

func aa26AdmitCommand(aa26Self *aa26Fixture, aa26Identity AA24CommandIdentity) {
	aa26Self.aa26T.Helper()
	aa26WantOK(aa26Self.aa26T, "send command", aa26Self.aa26Host.AA24SendCommand(aa26Identity, []byte{0x01}, aa26Self.aa26Now()))
	_, aa26OK, aa26Err := aa26Self.aa26IPC.AA24NextCommand(make([]byte, 0, 16), aa26Self.aa26Now())
	if aa26Err != nil || !aa26OK {
		aa26Self.aa26T.Fatalf("aa26 next command ok=%v err=%v", aa26OK, aa26Err)
	}
}

func TestAA26MasterStartsUseOnlyTheAdmittedOwnerAndWaitForClosure(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Identity := aa26TestCommand(aa26Self, 7)
	aa26AdmitCommand(aa26Self, aa26Identity)
	// The responder transaction holds the wire from selection to closure.
	aa26RequestEnd := aa26Self.aa26Send(aa26TestFrameTick, 0xAA, aa26TestInitiator, aa26TestAddress)
	aa26WantErr(aa26T, "start during selection", aa26Self.aa26Responder.AA26StartMaster(aa26Identity, aa26TestOtherInitiator, aa26Self.aa26Tick), AA26ErrWireBusy)
	aa26Rest := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)[2:]
	aa26Self.aa26Send(aa26RequestEnd, aa26Rest...)
	aa26Self.aa26Symbol(0x00, aa26Self.aa26Wire.aa26Emits[0].aa26NotBefore)
	aa26Self.aa26Echoed = 1
	aa26WantErr(aa26T, "start during response", aa26Self.aa26Responder.AA26StartMaster(aa26Identity, aa26TestOtherInitiator, aa26Self.aa26Tick), AA26ErrWireBusy)
	if len(aa26Self.aa26Wire.aa26Starts) != 0 {
		aa26T.Fatal("aa26 a master start reached the wire during a response")
	}
	aa26Self.aa26Send(aa26Self.aa26EchoPending(), 0x00)
	aa26Self.aa26WantClosedClean("before master")
	// Nothing was queued: the refused start is gone until the caller offers it again.
	if len(aa26Self.aa26Wire.aa26Starts) != 0 {
		aa26T.Fatal("aa26 a refused start was replayed")
	}
	for _, aa26Case := range []struct {
		aa26Name   string
		aa26Mutate func(*AA24CommandIdentity)
	}{
		{"subscriber", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24Ref.AA24Key.AA23Subscriber++ }},
		{"address", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24Ref.AA24Key.AA23Address = aa26TestOtherAddress }},
		{"registration epoch", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24Ref.AA24Key.AA23Epoch++ }},
		{"worker", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24Ref.AA24Worker++ }},
		{"stream", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24Ref.AA24Stream++ }},
		{"plan generation", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24Ref.AA24Plan++ }},
		{"plan expiry", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24Ref.AA24PlanExpiry-- }},
		{"transaction", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24Transaction++ }},
		{"owner token", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24OwnerToken++ }},
		{"owner expiry", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24OwnerExpiry-- }},
		{"request deadline", func(aa26Value *AA24CommandIdentity) { aa26Value.AA24RequestDeadline-- }},
	} {
		aa26Mutated := aa26Identity
		aa26Case.aa26Mutate(&aa26Mutated)
		if aa26Self.aa26Responder.AA26StartMaster(aa26Mutated, aa26TestOtherInitiator, aa26Self.aa26Tick) == nil {
			aa26T.Fatalf("aa26 %s: a command the AA24 worker never admitted took the wire", aa26Case.aa26Name)
		}
	}
	if len(aa26Self.aa26Wire.aa26Starts) != 0 {
		aa26T.Fatal("aa26 a mutated owner reached the wire")
	}
	aa26WantErr(aa26T, "responder address as initiator", aa26Self.aa26Responder.AA26StartMaster(aa26Identity, aa26TestAddress, aa26Self.aa26Tick), AA26ErrAddress)
	aa26WantOK(aa26T, "admitted start", aa26Self.aa26Responder.AA26StartMaster(aa26Identity, aa26TestOtherInitiator, aa26Self.aa26Tick))
	if len(aa26Self.aa26Wire.aa26Starts) != 1 || aa26Self.aa26Wire.aa26Starts[0] != aa26Identity {
		aa26T.Fatalf("aa26 starts = %+v", aa26Self.aa26Wire.aa26Starts)
	}
	aa26WantErr(aa26T, "second start", aa26Self.aa26Responder.AA26StartMaster(aa26Identity, aa26TestOtherInitiator, aa26Self.aa26Tick), AA26ErrWireBusy)
	// While the master holds the wire, a frame to the registered address is not selected.
	aa26Self.aa26Request(aa26Self.aa26Tick + aa26TestCharacter)
	if _, aa26Active := aa26Self.aa26Responder.AA26Active(); aa26Active || len(aa26Self.aa26Wire.aa26Emits) != len(aa26Self.aa26ExpectedEmits(0)) {
		aa26T.Fatal("aa26 the responder answered during a master hold")
	}
	aa26Mutated := aa26Identity
	aa26Mutated.AA24OwnerToken++
	aa26WantErr(aa26T, "end with another owner", aa26Self.aa26Responder.AA26EndMaster(aa26Mutated), AA26ErrNotHeld)
	aa26WantOK(aa26T, "end master", aa26Self.aa26Responder.AA26EndMaster(aa26Identity))
	aa26Self.aa26Request(aa26Self.aa26Tick + aa26TestCharacter)
	if _, aa26Active := aa26Self.aa26Responder.AA26Active(); !aa26Active {
		aa26T.Fatal("aa26 the responder stayed excluded after the master hold ended")
	}
}

func TestAA26ExpiredMasterHoldReleasesAtItsExclusiveDeadline(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Identity := aa26TestCommand(aa26Self, 3)
	aa26Identity.AA24OwnerExpiry = AA23Tick(aa26TestFrameTick + 50)
	aa26AdmitCommand(aa26Self, aa26Identity)
	aa26WantOK(aa26T, "start", aa26Self.aa26Responder.AA26StartMaster(aa26Identity, aa26TestOtherInitiator, aa26Self.aa26Tick))
	aa26Self.aa26Responder.AA26Poll(aa26TestFrameTick + 49)
	if !aa26Self.aa26Responder.aa26Master {
		aa26T.Fatal("aa26 master hold dropped before its deadline")
	}
	aa26Self.aa26Responder.AA26Poll(aa26TestFrameTick + 50)
	if aa26Self.aa26Responder.aa26Master {
		aa26T.Fatal("aa26 master hold survived its exclusive deadline")
	}
}

func TestAA26CommandSelectorSourceAndDataVaryIndependently(aa26T *aa26testing.T) {
	for _, aa26Case := range []struct {
		aa26Name   string
		aa26Source byte
		aa26PB     byte
		aa26SB     byte
		aa26Data   []byte
		aa26Answer bool
	}{
		{"exact control", aa26TestInitiator, aa26TestPB, aa26TestSB, aa26TestRequestData, true},
		{"source", aa26TestOtherInitiator, aa26TestPB, aa26TestSB, aa26TestRequestData, false},
		{"primary selector", aa26TestInitiator, aa26TestPB + 1, aa26TestSB, aa26TestRequestData, false},
		{"secondary selector", aa26TestInitiator, aa26TestPB, aa26TestSB + 1, aa26TestRequestData, false},
		{"data length", aa26TestInitiator, aa26TestPB, aa26TestSB, aa26TestRequestData[:2], false},
		{"data content", aa26TestInitiator, aa26TestPB, aa26TestSB, []byte{0x02, 0xA9, 0xAA}, false},
		{"data order", aa26TestInitiator, aa26TestPB, aa26TestSB, []byte{0x01, 0xAA, 0xA9}, false},
	} {
		aa26T.Run(aa26Case.aa26Name, func(aa26T *aa26testing.T) {
			aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
			aa26Wire := aa26TestRequestWire(aa26Case.aa26Source, aa26TestAddress, aa26Case.aa26PB, aa26Case.aa26SB, aa26Case.aa26Data)
			aa26Self.aa26Send(aa26TestFrameTick, append([]byte{0xAA}, aa26Wire...)...)
			aa26Events := aa26Self.aa26Drain()
			if aa26Case.aa26Answer {
				if len(aa26Self.aa26Wire.aa26Emits) != 1 {
					aa26T.Fatal("aa26 exact request not answered")
				}
				return
			}
			if len(aa26Self.aa26Wire.aa26Emits) != 0 {
				aa26T.Fatal("aa26 an unmatched request was answered")
			}
			if aa26LastClosed(aa26T, aa26Events).AA26Reason != AA26CloseNoMatch ||
				string(aa26RawOf(aa26Events)) != string(append([]byte{0xAA}, aa26Wire...)) {
				aa26T.Fatalf("aa26 unmatched observation = %+v", aa26Events)
			}
			aa26Self.aa26WantClosedClean(aa26Case.aa26Name)
		})
	}
}

func TestAA26PhaseRulesEndInSilence(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Self.aa26Request(aa26TestFrameTick)
	aa26End := aa26Self.aa26EchoPending()
	aa26Emitted := len(aa26Self.aa26Wire.aa26Emits)
	aa26Self.aa26Send(aa26End, 0xFF)
	// The only admitted failure action is silence: no repeat after a NACK.
	if len(aa26Self.aa26Wire.aa26Emits) != aa26Emitted || aa26LastClosed(aa26T, aa26Self.aa26Drain()).AA26Reason != AA26CloseInitiatorNACK {
		aa26T.Fatal("aa26 the response was repeated after an initiator NACK")
	}
	aa26Self.aa26WantClosedClean("NACK")

	aa26Odd := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Odd.aa26Request(aa26TestFrameTick)
	aa26Odd.aa26Send(aa26Odd.aa26EchoPending(), 0x5A)
	if aa26LastClosed(aa26T, aa26Odd.aa26Drain()).AA26Reason != AA26CloseMalformed {
		aa26T.Fatal("aa26 an unexpected final symbol was not malformed")
	}

	aa26Idle := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Idle.aa26Request(aa26TestFrameTick)
	aa26Idle.aa26EchoPending()
	aa26LastRaw := aa26Idle.aa26Responder.aa26LastRaw
	aa26Idle.aa26Responder.AA26Poll(aa26LastRaw + aa26TestIdle - 1)
	if _, aa26Active := aa26Idle.aa26Responder.AA26Active(); !aa26Active {
		aa26T.Fatal("aa26 closed before the idle bound")
	}
	aa26Idle.aa26Responder.AA26Poll(aa26LastRaw + aa26TestIdle)
	if aa26LastClosed(aa26T, aa26Idle.aa26Drain()).AA26Reason != AA26CloseIdle {
		aa26T.Fatal("aa26 silent initiator did not close at the idle bound")
	}

	// No ACK phase exists for a broadcast or an initiator destination.
	for _, aa26Target := range []byte{0xFE, aa26TestOtherInitiator} {
		aa26None := aa26NewFixture(aa26T, aa26DefaultOptions())
		aa26Wire := aa26TestRequestWire(aa26TestInitiator, aa26Target, aa26TestPB, aa26TestSB, aa26TestRequestData)
		aa26None.aa26Send(aa26TestFrameTick, append([]byte{0xAA}, aa26Wire...)...)
		if aa26None.aa26Responder.AA26Stats().AA26Selected != 0 || len(aa26None.aa26Wire.aa26Emits) != 0 {
			aa26T.Fatalf("aa26 destination %02X was selected", aa26Target)
		}
	}
	// Symbols after a completed transaction belong to nobody until a new frame.
	aa26After := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26AfterEnd := aa26After.aa26Complete(aa26TestFrameTick)
	aa26After.aa26Drain()
	aa26After.aa26Send(aa26AfterEnd, 0x00, 0x05, 0xA9)
	if len(aa26After.aa26Drain()) != 0 || len(aa26After.aa26Wire.aa26Emits) != len(aa26After.aa26ExpectedEmits(0)) {
		aa26T.Fatal("aa26 symbols after completion were delivered or answered")
	}
}

// aa26StartForDecision returns the SYN start that puts the CRC decision at a tick.
func aa26StartForDecision(aa26Decision uint64) uint64 {
	aa26Wire := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)
	return aa26Decision - aa26TestStopCentre - uint64(len(aa26Wire))*aa26TestCharacter
}

func TestAA26DeadlinesAreExclusiveForEachExpiry(aa26T *aa26testing.T) {
	aa26Deadline := AA23Tick(5000)
	for _, aa26Case := range []struct {
		aa26Name   string
		aa26Mutate func(*aa26Options)
	}{
		{"plan expiry", func(aa26Value *aa26Options) { aa26Value.aa26PlanExpiry = aa26Deadline }},
		{"registration lease", func(aa26Value *aa26Options) { aa26Value.aa26Lease = aa26Deadline }},
		{"source lease", func(aa26Value *aa26Options) { aa26Value.aa26SourceLease = aa26Deadline }},
	} {
		for _, aa26Offset := range []uint64{1, 0} {
			aa26Options := aa26DefaultOptions()
			aa26Case.aa26Mutate(&aa26Options)
			aa26Self := aa26NewFixture(aa26T, aa26Options)
			aa26Self.aa26Request(aa26StartForDecision(uint64(aa26Deadline) - aa26Offset))
			aa26Answered := len(aa26Self.aa26Wire.aa26Emits) == 1
			if aa26Answered != (aa26Offset == 1) {
				aa26T.Fatalf("aa26 %s: decision %d ticks before the deadline answered=%v", aa26Case.aa26Name, aa26Offset, aa26Answered)
			}
			if aa26Offset == 0 {
				if aa26LastClosed(aa26T, aa26Self.aa26Drain()).AA26Reason != AA26CloseExpired {
					aa26T.Fatalf("aa26 %s: expiry closure missing", aa26Case.aa26Name)
				}
				aa26Self.aa26WantClosedClean(aa26Case.aa26Name)
			}
		}
	}
}

// aa26MidResponseTick is the observation tick of the response symbol 0 echo, the
// point at which aa26MidResponse stops.
func aa26MidResponseTick() uint64 {
	aa26Wire := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)
	aa26CRCStart := aa26TestFrameTick + uint64(len(aa26Wire))*aa26TestCharacter
	return aa26CRCStart + 2*aa26TestCharacter + aa26TestTurnaround + aa26TestStopCentre
}

// aa26MidResponse starts an exchange and stops with response symbol 1 in flight.
func aa26MidResponse(aa26T *aa26testing.T) *aa26Fixture {
	aa26T.Helper()
	return aa26MidResponseWith(aa26T, aa26DefaultOptions())
}

func aa26MidResponseWith(aa26T *aa26testing.T, aa26Options aa26Options) *aa26Fixture {
	aa26T.Helper()
	aa26Self := aa26NewFixture(aa26T, aa26Options)
	aa26Self.aa26Request(aa26TestFrameTick)
	for aa26Index := 0; aa26Index < 2; aa26Index++ {
		aa26Emit := aa26Self.aa26Wire.aa26Emits[aa26Self.aa26Echoed]
		aa26Self.aa26Echoed++
		aa26Self.aa26Symbol(aa26Emit.aa26Symbol, aa26Emit.aa26NotBefore)
	}
	if len(aa26Self.aa26Wire.aa26Emits) != 3 || aa26Self.aa26Tick != aa26MidResponseTick() {
		aa26T.Fatalf("aa26 mid-response setup has %d emissions at %d", len(aa26Self.aa26Wire.aa26Emits), aa26Self.aa26Tick)
	}
	return aa26Self
}

// aa26DeliverNotices hands every pending AA23 revocation to the responder and
// returns the acknowledgements to the registry.
func (aa26Self *aa26Fixture) aa26DeliverNotices() {
	aa26Self.aa26T.Helper()
	for _, aa26Notice := range aa26Self.aa26Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 8)) {
		aa26Ack, aa26Ready, aa26Err := aa26Self.aa26Responder.AA26ObserveRevocation(aa26Notice)
		aa26WantOK(aa26Self.aa26T, "observe revocation", aa26Err)
		if !aa26Ready {
			aa26Self.aa26T.Fatalf("aa26 no acknowledgement for %+v: a pin survived", aa26Notice)
		}
		aa26WantOK(aa26Self.aa26T, "acknowledge", aa26Self.aa26Registry.AA23ObserveAcknowledgement(aa26Ack))
	}
}

func TestAA26InvalidationLeavesNoResponseAuthority(aa26T *aa26testing.T) {
	// The lease ends between the response symbol 0 echo and the in-flight echo.
	aa26Lease := AA23Tick(aa26MidResponseTick() + 2)
	for _, aa26Case := range []struct {
		aa26Name     string
		aa26Reason   AA26CloseReason
		aa26Disarmed bool
		aa26Lease    AA23Tick
		aa26Unbound  bool
		aa26Trigger  func(*aa26Fixture)
	}{
		{"unregister", AA26CloseRevoked, false, 0, true, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Registry.AA23Unregister(aa26Self.aa26Key)
			aa26Self.aa26DeliverNotices()
		}},
		{"subscriber disconnect", AA26CloseRevoked, false, 0, true, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Registry.AA23DisconnectSubscriber(aa26Self.aa26Key.AA23Subscriber)
			aa26Self.aa26DeliverNotices()
		}},
		{"registry reset", AA26CloseRevoked, false, 0, true, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Registry.AA23Reset()
			aa26Self.aa26DeliverNotices()
		}},
		{"physical conflict", AA26CloseRevoked, false, 0, true, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Registry.AA23MarkPhysicalConflict(aa26TestAddress)
			aa26Self.aa26DeliverNotices()
		}},
		{"source generation", AA26CloseRevoked, false, 0, true, func(aa26Self *aa26Fixture) {
			aa26WantOK(aa26Self.aa26T, "advance source", aa26Self.aa26Registry.AA23AdvanceSource(aa26Self.aa26Key, aa26TestSource+1, aa26TestFar, aa26Self.aa26Now()))
			aa26Self.aa26DeliverNotices()
		}},
		{"lease deadline", AA26CloseExpired, false, aa26Lease, false, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Responder.AA26Poll(uint64(aa26Lease))
		}},
		{"lease expiry notice", AA26CloseExpired, false, aa26Lease, true, func(aa26Self *aa26Fixture) {
			aa26Self.aa26At(uint64(aa26Lease))
			aa26WantOK(aa26Self.aa26T, "expire due", aa26Self.aa26Registry.AA23ExpireDue(aa26Self.aa26Now()))
			aa26Self.aa26DeliverNotices()
		}},
		{"aa24 revocation", AA26CloseRevoked, false, 0, false, func(aa26Self *aa26Fixture) {
			aa26WantOK(aa26Self.aa26T, "aa24 revoke", aa26Self.aa26Host.AA24RevokePlan(aa26Self.aa26Ref, aa26Self.aa26Now()))
			aa26Self.aa26Responder.AA26Poll(aa26Self.aa26Tick)
		}},
		{"aa24 stop", AA26CloseFenced, false, 0, false, func(aa26Self *aa26Fixture) {
			aa26WantOK(aa26Self.aa26T, "aa24 stop", aa26Self.aa26Host.AA24RequestStop(aa26Self.aa26Now()))
			aa26Self.aa26Responder.AA26Poll(aa26Self.aa26Tick)
		}},
		{"wire gap", AA26CloseGap, true, 0, false, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Responder.AA25TryPublish(AA25Event{AA25Kind: AA25EventGap, AA25Tick: aa26Self.aa26Tick})
		}},
		{"event loss", AA26CloseGap, true, 0, false, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Responder.AA25TryPublish(AA25Event{AA25Kind: AA25EventLoss, AA25Tick: aa26Self.aa26Tick})
		}},
		{"tick regression", AA26CloseClock, true, 0, false, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Responder.AA25TryPublish(AA25Event{AA25Kind: AA25EventFault, AA25Fault: AA25FaultTickRegression, AA25Tick: 1})
		}},
		{"lost character", AA26CloseFraming, false, 0, false, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Responder.AA25TryPublish(AA25Event{AA25Kind: AA25EventFault, AA25Fault: AA25FaultBadStop, AA25Tick: aa26Self.aa26Tick})
		}},
		{"echo mismatch", AA26CloseCollision, false, 0, false, func(aa26Self *aa26Fixture) {
			aa26Emit := aa26Self.aa26Wire.aa26Emits[2]
			aa26Self.aa26Symbol(aa26Emit.aa26Symbol^0x01, aa26Emit.aa26NotBefore)
		}},
		{"SYN instead of echo", AA26CloseCollision, false, 0, false, func(aa26Self *aa26Fixture) {
			aa26Self.aa26Symbol(0xAA, aa26Self.aa26Wire.aa26Emits[2].aa26NotBefore)
		}},
		{"early echo", AA26CloseCollision, false, 0, false, func(aa26Self *aa26Fixture) {
			aa26Emit := aa26Self.aa26Wire.aa26Emits[2]
			aa26Self.aa26Symbol(aa26Emit.aa26Symbol, aa26Emit.aa26NotBefore-1)
		}},
		{"missing echo", AA26CloseCollision, false, 0, false, func(aa26Self *aa26Fixture) {
			aa26Emit := aa26Self.aa26Wire.aa26Emits[2]
			aa26Self.aa26Responder.AA26Poll(aa26Emit.aa26NotBefore + aa26TestCharacter + aa26TestEchoAllowance)
		}},
		{"stream generation", AA26CloseStream, false, 0, true, func(aa26Self *aa26Fixture) {
			_, aa26Successor := aa26Attach(aa26Self.aa26T, aa26TestStream+1)
			aa26WantOK(aa26Self.aa26T, "advance stream", aa26Self.aa26Responder.AA26AdvanceStream(aa26TestStream+1, aa26Successor))
		}},
	} {
		aa26T.Run(aa26Case.aa26Name, func(aa26T *aa26testing.T) {
			aa26Options := aa26DefaultOptions()
			if aa26Case.aa26Lease != 0 {
				aa26Options.aa26Lease = aa26Case.aa26Lease
			}
			aa26Self := aa26MidResponseWith(aa26T, aa26Options)
			aa26InFlight := aa26Self.aa26Wire.aa26Emits[2]
			aa26Case.aa26Trigger(aa26Self)
			aa26Self.aa26WantClosedClean(aa26Case.aa26Name)
			aa26Closed := aa26LastClosed(aa26T, aa26Self.aa26Drain())
			if aa26Closed.AA26Reason != aa26Case.aa26Reason || aa26Closed.AA26Key.AA23Identity != aa26Self.aa26Notice.AA23Identity {
				aa26T.Fatalf("aa26 closure = %+v; want reason %d", aa26Closed, aa26Case.aa26Reason)
			}
			// A host-side retirement or a new stream also drops the address binding, so
			// nothing but a newly adopted plan can admit the address again.
			if aa26Self.aa26Responder.aa26Bindings[aa26TestAddress].aa26Valid == aa26Case.aa26Unbound {
				aa26T.Fatalf("aa26 binding valid=%v; want unbound=%v", !aa26Case.aa26Unbound, aa26Case.aa26Unbound)
			}
			if aa26Self.aa26Responder.AA26Disarmed() != aa26Case.aa26Disarmed {
				aa26T.Fatalf("aa26 disarmed = %v", aa26Self.aa26Responder.AA26Disarmed())
			}
			// Replaying the in-flight echo and the remaining response authorizes nothing.
			aa26Replay := aa26Self.aa26Tick + aa26TestCharacter
			aa26Self.aa26Symbol(aa26InFlight.aa26Symbol, aa26Replay)
			aa26Self.aa26Send(aa26Replay+aa26TestCharacter, aa26TestEncode(aa26TestResponseData)[2:]...)
			if len(aa26Self.aa26Wire.aa26Emits) != 3 {
				aa26T.Fatalf("aa26 %d emissions after invalidation; want the 3 before it", len(aa26Self.aa26Wire.aa26Emits))
			}
			aa26Self.aa26WantClosedClean("replay")
		})
	}
}

func TestAA26PortRefusalClosesAndRetainsNothing(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Self.aa26Request(aa26TestFrameTick)
	aa26Ack := aa26Self.aa26Wire.aa26Emits[0]
	aa26Self.aa26Wire.aa26Refuse = true
	aa26Self.aa26Symbol(aa26Ack.aa26Symbol, aa26Ack.aa26NotBefore)
	aa26Self.aa26WantClosedClean("refused")
	if aa26LastClosed(aa26T, aa26Self.aa26Drain()).AA26Reason != AA26CloseRefused {
		aa26T.Fatal("aa26 refused emission did not close the transaction")
	}
}

func TestAA26SupersessionKeepsTheSelectedGeneration(aa26T *aa26testing.T) {
	aa26Self := aa26MidResponse(aa26T)
	aa26Old := aa26Self.aa26Notice.AA23Identity
	aa26Newer := []byte{'N', 'E', 'W'}
	aa26Self.aa26Install(2, aa26Self.aa26Entry(aa26TestRequestData, aa26Newer))
	aa26Self.aa26DeliverNoticesExceptPinned(aa26Old)
	aa26End := aa26Self.aa26EchoPending()
	aa26Expected := aa26Self.aa26ExpectedEmits(aa26Self.aa26Wire.aa26Emits[0].aa26NotBefore - aa26TestCharacter - aa26TestTurnaround)
	if !aa26EqualEmits(aa26Self.aa26Wire.aa26Emits, aa26Expected) {
		aa26T.Fatalf("aa26 superseded transaction changed bytes: %+v", aa26Self.aa26Wire.aa26Emits)
	}
	aa26Self.aa26Send(aa26End, 0x00)
	if aa26Ack, aa26Ready := aa26Self.aa26Responder.AA26TakeAcknowledgement(); !aa26Ready || aa26Ack.AA23Identity != aa26Old {
		aa26T.Fatalf("aa26 superseded plan not acknowledged at closure: %+v %v", aa26Ack, aa26Ready)
	} else {
		aa26WantOK(aa26T, "acknowledge superseded", aa26Self.aa26Registry.AA23ObserveAcknowledgement(aa26Ack))
	}
	aa26Self.aa26Wire.aa26Emits = nil
	aa26Self.aa26Echoed = 0
	aa26Self.aa26Request(aa26End + 10*aa26TestCharacter)
	aa26Self.aa26EchoPending()
	aa26Key, _ := aa26Self.aa26Responder.AA26Active()
	if aa26Key.AA23Identity.AA23Plan != 2 || len(aa26Self.aa26Wire.aa26Emits) != 1+len(aa26TestEncode(aa26Newer)) {
		aa26T.Fatalf("aa26 next transaction key=%+v emits=%+v", aa26Key, aa26Self.aa26Wire.aa26Emits)
	}
}

// aa26DeliverNoticesExceptPinned delivers pending notices; the pinned plan's
// acknowledgement must wait for its transaction to close.
func (aa26Self *aa26Fixture) aa26DeliverNoticesExceptPinned(aa26Pinned AA23PlanIdentity) {
	aa26Self.aa26T.Helper()
	for _, aa26Notice := range aa26Self.aa26Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 8)) {
		_, aa26Ready, aa26Err := aa26Self.aa26Responder.AA26ObserveRevocation(aa26Notice)
		aa26WantOK(aa26Self.aa26T, "observe revocation", aa26Err)
		if aa26Notice.AA23Identity == aa26Pinned && aa26Ready {
			aa26Self.aa26T.Fatal("aa26 a pinned generation was acknowledged")
		}
	}
}

func TestAA26UnregisterDuringResponseStopsAndDisarmsAdmission(aa26T *aa26testing.T) {
	aa26Self := aa26MidResponse(aa26T)
	aa26Self.aa26Registry.AA23Unregister(aa26Self.aa26Key)
	aa26Self.aa26DeliverNotices()
	aa26Self.aa26WantClosedClean("unregister")
	if aa26Self.aa26Registry.AA23Registration(aa26Self.aa26Key).AA23State != AA23RegistrationAbsent {
		aa26T.Fatal("aa26 registration did not settle after the closure acknowledgement")
	}
	aa26Emitted := len(aa26Self.aa26Wire.aa26Emits)
	aa26Self.aa26Request(aa26Self.aa26Tick + 10*aa26TestCharacter)
	if len(aa26Self.aa26Wire.aa26Emits) != aa26Emitted || aa26Self.aa26Responder.AA26Stats().AA26Selected != 1 {
		aa26T.Fatal("aa26 a later destination was admitted after unregistration")
	}
	// Re-registration under a new epoch needs its own plan before anything is answered.
	aa26Self.aa26Key.AA23Epoch++
	aa26Self.aa26Register(aa26Self.aa26Key)
	aa26Self.aa26Request(aa26Self.aa26Tick + aa26TestCharacter)
	if len(aa26Self.aa26Wire.aa26Emits) != aa26Emitted {
		aa26T.Fatal("aa26 a new registration inherited the old plan")
	}
	aa26Self.aa26Install(2, aa26Self.aa26Entry(aa26TestRequestData, []byte{'R'}))
	aa26Self.aa26Request(aa26Self.aa26Tick + aa26TestCharacter)
	aa26Key, aa26Active := aa26Self.aa26Responder.AA26Active()
	if !aa26Active || aa26Key.AA23Identity.AA23Registration.AA23Epoch != 2 || len(aa26Self.aa26Wire.aa26Emits) != aa26Emitted+1 {
		aa26T.Fatalf("aa26 new epoch not admitted: key=%+v", aa26Key)
	}
}

func TestAA26OrdinalsSequencesAndPreparationNeverCarryOver(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	// Transaction 1 is prepared and then interrupted before its CRC.
	aa26Wire := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB, aa26TestSB, aa26TestRequestData)
	aa26Next := aa26Self.aa26Send(aa26TestFrameTick, append([]byte{0xAA}, aa26Wire[:len(aa26Wire)-1]...)...)
	if !aa26Self.aa26Responder.aa26Prepared {
		aa26T.Fatal("aa26 setup did not prepare")
	}
	aa26Next = aa26Self.aa26Send(aa26Next, 0xAA)
	aa26Self.aa26WantClosedClean("interrupted")
	if aa26LastClosed(aa26T, aa26Self.aa26Drain()).AA26Reason != AA26CloseInterrupted {
		aa26T.Fatal("aa26 SYN did not interrupt the transaction")
	}
	// Transaction 2 is a valid but unmatched request: the earlier preparation is gone.
	aa26Other := aa26TestRequestWire(aa26TestInitiator, aa26TestAddress, aa26TestPB+1, aa26TestSB, aa26TestRequestData)
	aa26Self.aa26Send(aa26Next, aa26Other...)
	if len(aa26Self.aa26Wire.aa26Emits) != 0 {
		aa26T.Fatal("aa26 a stale preparation answered another request")
	}
	aa26Events := aa26Self.aa26Drain()
	if aa26Events[0].AA26Key.AA23Transaction != 2 || aa26Events[0].AA26Sequence != uint64(len(aa26Wire)+1) {
		aa26T.Fatalf("aa26 second transaction starts with %+v", aa26Events[0])
	}
	// A new stream generation drops every binding of the old one ...
	aa26SuccessorHost, aa26Successor := aa26Attach(aa26T, aa26TestStream+1)
	aa26WantErr(aa26T, "successor of another stream", aa26Self.aa26Responder.AA26AdvanceStream(aa26TestStream+2, aa26Successor), AA26ErrIdentity)
	aa26WantOK(aa26T, "advance stream", aa26Self.aa26Responder.AA26AdvanceStream(aa26TestStream+1, aa26Successor))
	aa26WantErr(aa26T, "stale stream", aa26Self.aa26Responder.AA26AdvanceStream(aa26TestStream+1, aa26Successor), ErrAA23StaleGeneration)
	for aa26Address := range aa26Self.aa26Responder.aa26Bindings {
		if aa26Self.aa26Responder.aa26Bindings[aa26Address].aa26Valid {
			aa26T.Fatalf("aa26 binding for %02X survived the stream change", aa26Address)
		}
	}
	aa26Self.aa26Request(aa26Self.aa26Tick + aa26TestCharacter)
	if aa26Self.aa26Responder.AA26Stats().AA26Selected != 2 {
		aa26T.Fatal("aa26 an old-stream plan was selected on the new stream")
	}
	// ... and its ordinals restart: the unselected request above was ordinal 1 of
	// the new stream, so the next request is ordinal 2, not 4.
	aa26WantOK(aa26T, "bind stream", aa26Self.aa26Registry.AA23BindWorker(aa26TestWorker, aa26TestStream+1))
	aa26Self.aa26DeliverNotices()
	aa26Self.aa26Stream = aa26TestStream + 1
	aa26Self.aa26Host, aa26Self.aa26IPC = aa26SuccessorHost, aa26Successor
	aa26Self.aa26Install(2, aa26Self.aa26Entry(aa26TestRequestData, aa26TestResponseData))
	aa26Self.aa26Request(aa26Self.aa26Tick + aa26TestCharacter)
	aa26Key, aa26Active := aa26Self.aa26Responder.AA26Active()
	if !aa26Active || aa26Key.AA23Transaction != 2 || aa26Key.AA23Identity.AA23Stream != aa26TestStream+1 || len(aa26Self.aa26Wire.aa26Emits) != 1 {
		aa26T.Fatalf("aa26 new-stream transaction key=%+v active=%v", aa26Key, aa26Active)
	}
}

func TestAA26GapDisarmsUntilTheNextStream(aa26T *aa26testing.T) {
	aa26Self := aa26NewFixture(aa26T, aa26DefaultOptions())
	aa26Self.aa26Responder.AA25TryPublish(AA25Event{AA25Kind: AA25EventGap, AA25Tick: aa26TestFrameTick})
	if !aa26Self.aa26Responder.AA26Disarmed() {
		aa26T.Fatal("aa26 gap did not disarm admission")
	}
	aa26Self.aa26Request(aa26TestFrameTick + aa26TestCharacter)
	if aa26Self.aa26Responder.AA26Stats().AA26Selected != 0 || len(aa26Self.aa26Drain()) != 0 {
		aa26T.Fatal("aa26 selection after a gap without a new stream")
	}
	_, aa26Successor := aa26Attach(aa26T, aa26TestStream+1)
	aa26WantOK(aa26T, "advance stream", aa26Self.aa26Responder.AA26AdvanceStream(aa26TestStream+1, aa26Successor))
	if aa26Self.aa26Responder.AA26Disarmed() {
		aa26T.Fatal("aa26 a new stream did not re-arm admission")
	}
}

func aa26ParseSource(aa26T *aa26testing.T, aa26Path string) *aa26ast.File {
	aa26T.Helper()
	aa26File, aa26Err := aa26parser.ParseFile(aa26token.NewFileSet(), aa26Path, nil, 0)
	if aa26Err != nil {
		aa26T.Fatalf("aa26 parse %s: %v", aa26Path, aa26Err)
	}
	return aa26File
}

func TestAA26ProductionShapeHasNoWaitAndNoResponderStart(aa26T *aa26testing.T) {
	aa26File := aa26ParseSource(aa26T, "aa26_responder.go")
	for _, aa26Import := range aa26File.Imports {
		switch aa26Import.Path.Value {
		case `"errors"`, `"math"`, `"math/bits"`:
		default:
			aa26T.Fatalf("aa26 forbidden import %s", aa26Import.Path.Value)
		}
	}
	for _, aa26Decl := range aa26File.Decls {
		aa26Name := ""
		if aa26Func, aa26IsFunc := aa26Decl.(*aa26ast.FuncDecl); aa26IsFunc {
			aa26Name = aa26Func.Name.Name
		}
		aa26ast.Inspect(aa26Decl, func(aa26Node aa26ast.Node) bool {
			switch aa26Value := aa26Node.(type) {
			case *aa26ast.GoStmt, *aa26ast.DeferStmt, *aa26ast.SelectStmt, *aa26ast.SendStmt, *aa26ast.ChanType, *aa26ast.FuncLit:
				aa26T.Fatalf("aa26 forbidden construct %T in %s", aa26Value, aa26Name)
			case *aa26ast.SelectorExpr:
				// Only the explicit master-start entry point may reach the port's START.
				if aa26Value.Sel.Name == "AA26StartMaster" && aa26Name != "AA26StartMaster" && aa26Name != "" {
					aa26T.Fatalf("aa26 %s reaches the master start", aa26Name)
				}
			}
			return true
		})
	}
}

func aa26CheckPrefix(aa26T *aa26testing.T, aa26Name string) {
	aa26T.Helper()
	switch aa26Name {
	case "_", "localworker":
		return
	case "AA25TryPublish", "AA25NowTicks", "AA25SampleLevel", "AA25WriteEdge":
		// Method names fixed by the AA25 port interfaces these types implement.
		return
	}
	if !aa26strings.HasPrefix(aa26Name, "aa26") && !aa26strings.HasPrefix(aa26Name, "AA26") && !aa26strings.HasPrefix(aa26Name, "TestAA26") {
		aa26T.Fatalf("aa26 unprefixed identifier %q", aa26Name)
	}
}

func TestAA26EveryDeclaredIdentifierIsPrefixed(aa26T *aa26testing.T) {
	for _, aa26Path := range []string{"aa26_responder.go", "aa26_responder_test.go"} {
		aa26File := aa26ParseSource(aa26T, aa26Path)
		for _, aa26Import := range aa26File.Imports {
			if aa26Import.Name == nil {
				aa26T.Fatalf("aa26 import %s has no prefixed alias", aa26Import.Path.Value)
			}
			aa26CheckPrefix(aa26T, aa26Import.Name.Name)
		}
		aa26ast.Inspect(aa26File, func(aa26Node aa26ast.Node) bool {
			switch aa26Value := aa26Node.(type) {
			case *aa26ast.FuncDecl:
				aa26CheckPrefix(aa26T, aa26Value.Name.Name)
			case *aa26ast.TypeSpec:
				aa26CheckPrefix(aa26T, aa26Value.Name.Name)
			case *aa26ast.ValueSpec:
				for _, aa26Name := range aa26Value.Names {
					aa26CheckPrefix(aa26T, aa26Name.Name)
				}
			case *aa26ast.Field:
				for _, aa26Name := range aa26Value.Names {
					aa26CheckPrefix(aa26T, aa26Name.Name)
				}
			case *aa26ast.AssignStmt:
				if aa26Value.Tok == aa26token.DEFINE {
					for _, aa26Left := range aa26Value.Lhs {
						if aa26Ident, aa26OK := aa26Left.(*aa26ast.Ident); aa26OK {
							aa26CheckPrefix(aa26T, aa26Ident.Name)
						}
					}
				}
			case *aa26ast.RangeStmt:
				for _, aa26Bound := range []aa26ast.Expr{aa26Value.Key, aa26Value.Value} {
					if aa26Ident, aa26OK := aa26Bound.(*aa26ast.Ident); aa26OK {
						aa26CheckPrefix(aa26T, aa26Ident.Name)
					}
				}
			}
			return true
		})
	}
}
