package localworker

import (
	"crypto/sha256"
	errorstest "errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// These tests drive one serialized responder over synthetic frames, injected time
// and in-memory ports. They prove model rules only, never wire timing on hardware.

const (
	responderTestHz uint64 = 24000
	// testCharacter is ten bits of ten ticks: 24000 ticks per second at 2400 Bd.
	testCharacter uint64 = 100
	// testStopCentre is where the wire engine reports a symbol: the last central
	// sample of the stop bit, before that character ends.
	testStopCentre    uint64 = 96
	testTurnaround    uint64 = 7
	testEchoAllowance uint64 = 20
	testIdle          uint64 = 2000

	testWorker          = WorkerID(41)
	testStream          = StreamGeneration(51)
	responderTestSource = SourceGeneration(61)
	testAddress         = byte(0x15)
	testOtherAddress    = byte(0x26)
	testInitiator       = byte(0x10)
	testOtherInitiator  = byte(0x31)
	responderTestPB     = byte(0x07)
	responderTestSB     = byte(0x04)
	testFar             = Tick(500000)
	testSetupTick       = uint64(100)
	testFrameTick       = uint64(1000)
)

// testRequestData and testResponseData carry both symbols that need escaping.
var (
	testRequestData  = []byte{0x01, 0xA9, 0xAA}
	testResponseData = []byte{'F', 0xA9, 0xAA, '1'}
)

var testSilence = FailurePolicy{
	Unavailable: FailureSilence,
	Malformed:   FailureSilence,
	Timeout:     FailureSilence,
}

var testLimits = PlanLimits{
	MaxSubscribers:          4,
	MaxRegistrations:        4,
	MaxMasters:              2,
	MaxPlanSlots:            4,
	MaxEntriesPerPlan:       4,
	MaxRequestBytes:         16,
	MaxEncodedResponseBytes: 40,
	MaxPlanBytes:            128,
	MaxLeaseTicks:           1000000,
}

func responderTestConfig(capacity uint32) ResponderConfig {
	return ResponderConfig{
		TickHz:             responderTestHz,
		ClockDomain:        DefaultClockDomain,
		TurnaroundTicks:    testTurnaround,
		EchoAllowanceTicks: testEchoAllowance,
		IdleCloseTicks:     testIdle,
		ObserverCapacity:   capacity,
	}
}

func testBuild() BuildIdentity {
	return BuildIdentity(sha256.Sum256([]byte("synthetic build")))
}

// responderTestExpand is the reference escaping, written apart from production code.
func responderTestExpand(logical []byte) []byte {
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

// responderTestCRC divides by x^8+0x9B over GF(2), one wire symbol at a time.
func responderTestCRC(logical []byte) byte {
	remainder := uint16(0)
	for _, symbol := range responderTestExpand(logical) {
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

func testRequestWire(source byte, target byte, pb byte, sb byte, data []byte) []byte {
	logical := append([]byte{source, target, pb, sb, byte(len(data))}, data...)
	return responderTestExpand(append(logical, responderTestCRC(logical)))
}

func testEncode(data []byte) []byte {
	logical := append([]byte{byte(len(data))}, data...)
	return responderTestExpand(append(logical, responderTestCRC(logical)))
}

func TestAA26ReferenceCRCVectors(t *testing.T) {
	for _, tc := range []struct {
		logical []byte
		want    byte
	}{
		{nil, 0x00},
		{[]byte{0x01, 0x02}, 0x99},
		{[]byte{0x10, 0xFE, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA}, 0x77},
	} {
		if got := responderTestCRC(tc.logical); got != tc.want {
			t.Fatalf("reference CRC(% X) = %02X; want %02X", tc.logical, got, tc.want)
		}
	}
	encoded := testEncode(testResponseData)
	production, err := EncodeResponse(nil, testResponseData)
	if err != nil || string(production) != string(encoded) {
		t.Fatalf("reference encoding % X differs from plan % X (%v)", encoded, production, err)
	}
}

type testEmit struct {
	symbol    byte
	notBefore uint64
}

// testWire records every port call. With a line attached it also drives the
// emitted character onto that line, so the wire engine observes the echo.
type testWire struct {
	emits  []testEmit
	starts []CommandIdentity
	refuse bool
	line   *testLine
}

func (wire *testWire) EmitResponderSymbol(symbol byte, notBefore uint64) bool {
	if wire.refuse {
		return false
	}
	if wire.line != nil {
		if notBefore <= wire.line.now {
			return false
		}
		wire.line.paint(notBefore, symbol, true)
	}
	wire.emits = append(wire.emits, testEmit{symbol: symbol, notBefore: notBefore})
	return true
}

func (wire *testWire) StartInitiator(identity CommandIdentity, initiator byte) bool {
	wire.starts = append(wire.starts, identity)
	return true
}

type options struct {
	capacity    uint32
	planExpiry  Tick
	lease       Tick
	sourceLease Tick
	request     []byte
}

func defaultOptions() options {
	return options{
		capacity:    256,
		planExpiry:  testFar,
		lease:       testFar,
		sourceLease: testFar,
		request:     testRequestData,
	}
}

type responderFixture struct {
	t         *testing.T
	options   options
	registry  *Registry
	view      *WorkerView
	host      *Host
	ipc       *Worker
	wire      *testWire
	responder *Responder
	tick      uint64
	stream    StreamGeneration
	key       RegistrationKey
	notice    PublishNotice
	ref       PlanRef
	echoed    int
}

func responderNewFixture(t *testing.T, opts options) *responderFixture {
	t.Helper()
	registry, err := NewRegistry(testLimits, 0)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	view, err := NewWorkerView(registry.Pool(), testWorker, testStream)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	responderWantOK(t, "bind worker", registry.BindWorker(testWorker, testStream))
	subscriber, err := registry.OpenSubscriber()
	responderWantOK(t, "open subscriber", err)
	f := &responderFixture{
		t:        t,
		options:  opts,
		registry: registry,
		view:     view,
		tick:     testSetupTick,
		stream:   testStream,
		key:      RegistrationKey{Subscriber: subscriber, Address: testAddress, Epoch: 1},
		wire:     &testWire{},
	}
	f.register(f.key)
	f.host, f.ipc = attach(t, testStream)
	f.responder, err = NewResponder(responderTestConfig(opts.capacity), view, f.ipc, f.wire)
	responderWantOK(t, "new responder", err)
	f.install(1, f.entry(opts.request, testResponseData))
	return f
}

func attach(t *testing.T, stream StreamGeneration) (*Host, *Worker) {
	t.Helper()
	mem := fixtureMemory(fixtureBytes())
	binding := Binding{Build: testBuild(), Worker: testWorker, Stream: stream}
	expected, _, err := InitMapping(mem, binding, fixtureConfig, ipcFixtureLimits)
	responderWantOK(t, "init mapping", err)
	host, err := AttachHost(mem, expected)
	responderWantOK(t, "attach host", err)
	ipc, err := AttachWorker(mem, expected)
	responderWantOK(t, "attach worker", err)
	return host, ipc
}

func responderWantOK(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func responderWantErr(t *testing.T, label string, got error, want error) {
	t.Helper()
	if !errorstest.Is(got, want) {
		t.Fatalf("%s: err = %v, want %v", label, got, want)
	}
}

func (f *responderFixture) now() ClockReading {
	return ClockReading{Domain: DefaultClockDomain, Tick: Tick(f.tick)}
}

func (f *responderFixture) at(tick uint64) {
	if tick < f.tick {
		f.t.Fatalf("fixture time runs backwards: %d < %d", tick, f.tick)
	}
	f.tick = tick
}

func (f *responderFixture) register(key RegistrationKey) {
	f.t.Helper()
	_, err := f.registry.Register(Registration{
		Key:               key,
		LeaseExpiry:       f.options.lease,
		Source:            responderTestSource,
		SourceLeaseExpiry: f.options.sourceLease,
		Failure:           testSilence,
	}, f.now())
	responderWantOK(f.t, "register", err)
}

func (f *responderFixture) entry(request []byte, response []byte) PlanEntry {
	return PlanEntry{
		Source:          SourceConstraint{Source: testInitiator},
		Destination:     f.key.Address,
		PB:              responderTestPB,
		SB:              responderTestSB,
		RequestLength:   uint32(len(request)),
		Request:         append([]byte(nil), request...),
		Operation:       OperationRead,
		AllowedPhase:    PhaseResponse,
		MaxEncodedBytes: 40,
		EncodedResponse: testEncode(response),
	}
}

func (f *responderFixture) identity(plan PlanGeneration) PlanIdentity {
	return PlanIdentity{
		Registration: f.key,
		Worker:       testWorker,
		Stream:       f.stream,
		Plan:         plan,
		Source:       responderTestSource,
	}
}

func (f *responderFixture) refFor(plan PlanGeneration) PlanRef {
	return PlanRef{
		Key:        f.key,
		Worker:     testWorker,
		Stream:     f.stream,
		Plan:       plan,
		PlanExpiry: f.options.planExpiry,
	}
}

// publish publishes one generation on both control planes and returns the
// plan notice; the IPC lane carries published, which tests may vary.
func (f *responderFixture) publish(plan PlanGeneration, published PlanRef, entries ...PlanEntry) PublishNotice {
	f.t.Helper()
	slot, err := f.registry.PreparePlan(PlanCandidate{
		Identity:   f.identity(plan),
		PlanExpiry: f.options.planExpiry,
		Failure:    testSilence,
		Entries:    entries,
	}, f.now())
	responderWantOK(f.t, "prepare plan", err)
	notice, err := f.registry.PublishPrepared(slot, f.now())
	responderWantOK(f.t, "publish plan", err)
	arena := f.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	responderWantOK(f.t, "publish", f.host.PublishPlan(published, arena, 8, f.now()))
	responderWantOK(f.t, "adopt", f.ipc.PollControl(f.now()))
	return notice
}

func (f *responderFixture) install(plan PlanGeneration, entries ...PlanEntry) {
	f.t.Helper()
	ref := f.refFor(plan)
	notice := f.publish(plan, ref, entries...)
	ack, ready, err := f.responder.AdoptPlan(notice, ref, f.tick)
	responderWantOK(f.t, "adopt plan", err)
	if ready {
		responderWantOK(f.t, "predecessor acknowledgement", f.registry.ObserveAcknowledgement(ack))
	}
	f.notice = notice
	f.ref = ref
}

// symbol delivers one raw symbol the way the wire engine reports it.
func (f *responderFixture) symbol(raw byte, start uint64) {
	f.responder.TryPublish(Event{
		Kind:      EventRawSymbol,
		StartTick: start,
		Tick:      start + testStopCentre,
		Raw:       raw,
	})
	if start+testStopCentre > f.tick {
		f.tick = start + testStopCentre
	}
}

// send delivers contiguous characters and returns the end of the last one.
func (f *responderFixture) send(start uint64, raws ...byte) uint64 {
	for _, raw := range raws {
		f.symbol(raw, start)
		start += testCharacter
	}
	return start
}

// echoPending loops every emitted symbol back as its own echo, starting at its
// scheduled tick, and returns the end of the last echoed character.
func (f *responderFixture) echoPending() uint64 {
	end := uint64(0)
	for f.echoed < len(f.wire.emits) {
		emit := f.wire.emits[f.echoed]
		f.echoed++
		f.symbol(emit.symbol, emit.notBefore)
		end = emit.notBefore + testCharacter
	}
	return end
}

// request sends SYN plus the fixture request and returns the end of its CRC.
func (f *responderFixture) request(start uint64) uint64 {
	wire := testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, f.options.request)
	return f.send(start, append([]byte{0xAA}, wire...)...)
}

// complete runs one whole exchange and returns the end of the initiator ACK.
func (f *responderFixture) complete(start uint64) uint64 {
	f.request(start)
	end := f.echoPending()
	if end == 0 {
		f.t.Fatal("exchange emitted nothing")
	}
	return f.send(end, 0x00)
}

func (f *responderFixture) drain() []ObserverEvent {
	return f.responder.DrainObserver(make([]ObserverEvent, 0, 512))
}

func (f *responderFixture) expectedEmits(crcStart uint64) []testEmit {
	ack := crcStart + testCharacter + testTurnaround
	result := []testEmit{{symbol: 0x00, notBefore: ack}}
	for index, symbol := range testEncode(testResponseData) {
		result = append(result, testEmit{symbol: symbol, notBefore: ack + uint64(index+1)*testCharacter})
	}
	return result
}

func equalEmits(left []testEmit, right []testEmit) bool {
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

func rawOf(events []ObserverEvent) []byte {
	var result []byte
	for _, event := range events {
		if event.Kind == ObserverRaw {
			result = append(result, event.Raw)
		}
	}
	return result
}

func lastClosed(t *testing.T, events []ObserverEvent) ObserverEvent {
	t.Helper()
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == ObserverClosed {
			return events[index]
		}
	}
	t.Fatal("no closure event")
	return ObserverEvent{}
}

// wantClosedClean proves that nothing of a closed transaction survives: no
// active key, no pin in the worker view and no prepared or awaited symbol.
func (f *responderFixture) wantClosedClean(label string) {
	f.t.Helper()
	responder := f.responder
	if _, active := responder.Active(); active {
		f.t.Fatalf("%s: transaction still active", label)
	}
	if f.view.pinActive {
		f.t.Fatalf("%s: worker view still holds a pin", label)
	}
	if responder.prepared || responder.replyLen != 0 || responder.offset != 0 ||
		responder.awaitEcho || responder.pin != (PlanPin{}) || responder.ref != (PlanRef{}) {
		f.t.Fatalf("%s: per-transaction state survived closure", label)
	}
}

func TestAA26ConstructionRefusesMissingOrMismatchedParts(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	config := responderTestConfig(4)
	wire := &testWire{}
	_, err := NewResponder(config, nil, f.ipc, wire)
	responderWantErr(t, "nil view", err, ErrResponderInvalidConfig)
	_, err = NewResponder(config, f.view, nil, wire)
	responderWantErr(t, "nil ipc", err, ErrResponderInvalidConfig)
	_, err = NewResponder(config, f.view, f.ipc, nil)
	responderWantErr(t, "nil wire", err, ErrResponderInvalidConfig)
	for _, tc := range []struct {
		name   string
		mutate func(*ResponderConfig)
	}{
		{"tick rate below the sample rate", func(value *ResponderConfig) { value.TickHz = NominalSampleRate - 1 }},
		{"zero clock domain", func(value *ResponderConfig) { value.ClockDomain = 0 }},
		{"turnaround beyond one second", func(value *ResponderConfig) { value.TurnaroundTicks = responderTestHz + 1 }},
		{"echo allowance of a whole character", func(value *ResponderConfig) { value.EchoAllowanceTicks = testCharacter }},
		{"idle bound below a character", func(value *ResponderConfig) { value.IdleCloseTicks = testCharacter - 1 }},
		{"zero observer capacity", func(value *ResponderConfig) { value.ObserverCapacity = 0 }},
		{"observer capacity above the ceiling", func(value *ResponderConfig) { value.ObserverCapacity = observerCeiling + 1 }},
	} {
		mutated := config
		tc.mutate(&mutated)
		_, err = NewResponder(mutated, f.view, f.ipc, wire)
		responderWantErr(t, tc.name, err, ErrResponderInvalidConfig)
	}
	other := config
	other.ClockDomain = DefaultClockDomain + 1
	_, err = NewResponder(other, f.view, f.ipc, wire)
	responderWantErr(t, "clock domain", err, ErrResponderIdentity)
	_, otherStream := attach(t, testStream+1)
	_, err = NewResponder(config, f.view, otherStream, wire)
	responderWantErr(t, "stream generation", err, ErrResponderIdentity)
	otherView, err := NewWorkerView(f.registry.Pool(), testWorker+1, testStream)
	responderWantOK(t, "other worker view", err)
	_, err = NewResponder(config, otherView, f.ipc, wire)
	responderWantErr(t, "worker incarnation", err, ErrResponderIdentity)
}

func TestAA26SelectsAtDestinationAndDeliversThePrefixAtOnce(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	wire := testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)
	start := testFrameTick
	f.send(start, 0xAA, wire[0])
	if _, active := f.responder.Active(); active || len(f.drain()) != 0 {
		t.Fatal("selection or delivery before the destination symbol")
	}
	f.symbol(wire[1], start+2*testCharacter)
	key, active := f.responder.Active()
	if !active || !f.view.pinActive {
		t.Fatal("no provisional selection at the destination symbol")
	}
	if key.Identity != f.notice.Identity || key.Transaction != 1 {
		t.Fatalf("selected key %+v; want identity %+v transaction 1", key, f.notice.Identity)
	}
	prefix := f.drain()
	want := []ObserverEvent{
		{Kind: ObserverRaw, Raw: 0xAA, Sequence: 1, StartTick: start},
		{Kind: ObserverRaw, Raw: testInitiator, Sequence: 2, StartTick: start + testCharacter},
		{Kind: ObserverLogical, Logical: testInitiator, Sequence: 2, StartTick: start + testCharacter},
		{Kind: ObserverRaw, Raw: testAddress, Sequence: 3, StartTick: start + 2*testCharacter},
		{Kind: ObserverLogical, Logical: testAddress, Sequence: 3, StartTick: start + 2*testCharacter},
	}
	if len(prefix) != len(want) {
		t.Fatalf("prefix has %d events, want %d: %+v", len(prefix), len(want), prefix)
	}
	for index, expected := range want {
		expected.Key = key
		expected.Tick = expected.StartTick + testStopCentre
		expected.Phase = PhaseRequest
		if prefix[index] != expected {
			t.Fatalf("prefix event %d = %+v; want %+v", index, prefix[index], expected)
		}
	}
	// Every later raw symbol is delivered on arrival; nothing is emitted before the CRC.
	next := start + 3*testCharacter
	for index := 2; index < len(wire)-1; index++ {
		f.symbol(wire[index], next)
		events := f.drain()
		if len(events) == 0 || events[0].Kind != ObserverRaw || events[0].Raw != wire[index] ||
			events[0].Sequence != uint64(index+2) {
			t.Fatalf("symbol %d not delivered in order: %+v", index, events)
		}
		if len(f.wire.emits) != 0 {
			t.Fatalf("emission before the CRC at symbol %d", index)
		}
		next += testCharacter
	}
	if !f.responder.prepared || f.responder.Stats().Prepared != 1 {
		t.Fatal("response not prepared before the CRC")
	}
	f.symbol(wire[len(wire)-1], next)
	if len(f.wire.emits) != 1 {
		t.Fatalf("CRC produced %d emissions, want the ACK", len(f.wire.emits))
	}
}

func TestAA26EscapePrefixIsRawAtOnceAndLogicalLater(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	wire := testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)
	// Wire: QQ ZZ PB SB NN 01 A9 00 A9 01 CRC; index 6 is the first escape prefix.
	prefix := 6
	if wire[prefix] != 0xA9 || wire[prefix+1] != 0x00 {
		t.Fatalf("fixture wire % X has no escape at %d", wire, prefix)
	}
	end := f.send(testFrameTick, append([]byte{0xAA}, wire[:prefix+1]...)...)
	events := f.drain()
	last := events[len(events)-1]
	if last.Kind != ObserverRaw || last.Raw != 0xA9 || last.Sequence != uint64(prefix+2) {
		t.Fatalf("escape prefix not delivered as a raw symbol at once: %+v", last)
	}
	f.symbol(wire[prefix+1], end)
	events = f.drain()
	if len(events) != 2 || events[0].Kind != ObserverRaw || events[0].Raw != 0x00 ||
		events[1].Kind != ObserverLogical || events[1].Logical != 0xA9 || !events[1].Escaped ||
		events[1].Sequence != events[0].Sequence {
		t.Fatalf("escape suffix events = %+v", events)
	}
	// An escape prefix followed by anything but a suffix is malformed: silence.
	bad := responderNewFixture(t, defaultOptions())
	badWire := append(append([]byte{0xAA}, wire[:prefix+1]...), 0x02)
	bad.send(testFrameTick, badWire...)
	if len(bad.wire.emits) != 0 || lastClosed(t, bad.drain()).Reason != CloseMalformed {
		t.Fatal("invalid escape suffix did not end in silence")
	}
	bad.wantClosedClean("invalid escape")
}

func TestAA26AcknowledgesOnlyAValidCRCAfterItsCharacterEnds(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	end := f.request(testFrameTick)
	crcStart := end - testCharacter
	if len(f.wire.emits) != 1 {
		t.Fatalf("emissions after a valid CRC: %+v", f.wire.emits)
	}
	ack := f.wire.emits[0]
	// The decision happens at the stop-bit centre, but the ACK may only start once
	// the CRC character has ended and the turnaround has passed.
	if ack.symbol != 0x00 || ack.notBefore != crcStart+testCharacter+testTurnaround {
		t.Fatalf("ACK = %+v; want 00 at %d", ack, crcStart+testCharacter+testTurnaround)
	}
	if f.tick >= ack.notBefore {
		t.Fatal("decision tick is not before the scheduled ACK")
	}

	badCRC := responderNewFixture(t, defaultOptions())
	wire := testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)
	wire[len(wire)-1] ^= 0x01
	badCRC.send(testFrameTick, append([]byte{0xAA}, wire...)...)
	events := badCRC.drain()
	if len(badCRC.wire.emits) != 0 {
		t.Fatalf("a bad CRC was answered: %+v", badCRC.wire.emits)
	}
	if string(rawOf(events)) != string(append([]byte{0xAA}, wire...)) ||
		lastClosed(t, events).Reason != CloseMalformed {
		t.Fatalf("bad CRC observation = %+v", events)
	}
	badCRC.wantClosedClean("bad CRC")

	// A CRC that itself needs escaping is decided at its suffix, never at its prefix.
	data := []byte(nil)
	for value := 0; value < 256 && data == nil; value++ {
		candidate := []byte{byte(value)}
		logical := append([]byte{testInitiator, testAddress, responderTestPB, responderTestSB, 1}, candidate...)
		if crc := responderTestCRC(logical); crc == 0xA9 || crc == 0xAA {
			data = candidate
		}
	}
	if data == nil {
		t.Fatal("no single data byte yields an escaped CRC")
	}
	options := defaultOptions()
	options.request = data
	escaped := responderNewFixture(t, options)
	escapedWire := append([]byte{0xAA}, testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, data)...)
	next := escaped.send(testFrameTick, escapedWire[:len(escapedWire)-1]...)
	if len(escaped.wire.emits) != 0 {
		t.Fatal("ACK emitted on the escape prefix of the CRC")
	}
	escaped.symbol(escapedWire[len(escapedWire)-1], next)
	if len(escaped.wire.emits) != 1 || escaped.wire.emits[0].notBefore != next+testCharacter+testTurnaround {
		t.Fatalf("escaped CRC emissions = %+v", escaped.wire.emits)
	}
}

func TestAA26CompletesThePreparedResponseWithoutInitiatorStart(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	requestEnd := f.request(testFrameTick)
	end := f.echoPending()
	expected := f.expectedEmits(requestEnd - testCharacter)
	if !equalEmits(f.wire.emits, expected) {
		t.Fatalf("emissions = %+v; want %+v", f.wire.emits, expected)
	}
	if _, active := f.responder.Active(); !active {
		t.Fatal("transaction closed before the initiator ACK")
	}
	f.send(end, 0x00, 0xAA)
	if len(f.wire.starts) != 0 {
		t.Fatalf("responder issued %d initiator starts", len(f.wire.starts))
	}
	events := f.drain()
	wantRaw := append(append([]byte{0xAA}, testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)...), 0x00)
	wantRaw = append(append(wantRaw, testEncode(testResponseData)...), 0x00)
	if string(rawOf(events)) != string(wantRaw) {
		t.Fatalf("observed raw % X; want % X", rawOf(events), wantRaw)
	}
	sequence := uint64(0)
	echoes := 0
	for _, event := range events {
		if event.Kind != ObserverRaw {
			continue
		}
		if event.Sequence != sequence+1 {
			t.Fatalf("raw sequence jumps from %d to %d", sequence, event.Sequence)
		}
		sequence = event.Sequence
		if event.Echo {
			echoes++
		}
	}
	if echoes != len(expected) {
		t.Fatalf("%d echoes marked; want %d", echoes, len(expected))
	}
	if lastClosed(t, events).Reason != CloseCompleted {
		t.Fatalf("closure = %+v", lastClosed(t, events))
	}
	f.wantClosedClean("completed")
	// A repeat is a new transaction with its own ordinal and its own checks.
	before := len(f.wire.emits)
	f.request(end + 10*testCharacter)
	key, active := f.responder.Active()
	if !active || key.Transaction != 2 || len(f.wire.emits) != before+1 {
		t.Fatalf("repeat transaction key=%+v active=%v emits=%d", key, active, len(f.wire.emits))
	}
}

// testLine is a wired-AND line of injected levels, one sample per tick.
type testLine struct {
	now   uint64
	base  uint64
	low   []bool
	edges uint64
}

func (line *testLine) NowTicks() uint64 { return line.now }

func (line *testLine) SampleLevel() Level {
	index := line.now - line.base
	if index < uint64(len(line.low)) && line.low[index] {
		return LevelLow
	}
	return LevelHigh
}

func (line *testLine) WriteEdge(Level) { line.edges++ }

func (line *testLine) drive(from uint64, ticks uint64) {
	for tick := from; tick < from+ticks; tick++ {
		line.low[tick-line.base] = true
	}
}

// paint drives one 8N1 character, least significant bit first.
func (line *testLine) paint(start uint64, symbol byte, stopHigh bool) {
	bit := testCharacter / 10
	line.drive(start, bit)
	for index := uint64(0); index < 8; index++ {
		if symbol>>index&1 == 0 {
			line.drive(start+(index+1)*bit, bit)
		}
	}
	if !stopHigh {
		line.drive(start+9*bit, bit)
	}
}

func (line *testLine) paintAll(start uint64, symbols []byte) uint64 {
	for _, symbol := range symbols {
		line.paint(start, symbol, true)
		start += testCharacter
	}
	return start
}

func runEngine(line *testLine, engine *Engine, until uint64) {
	for line.now < until {
		line.now++
		engine.Step()
	}
}

func newLoopback(t *testing.T) (*responderFixture, *testLine, *Engine) {
	t.Helper()
	f := responderNewFixture(t, defaultOptions())
	line := &testLine{now: f.tick + 1, base: f.tick + 1, low: make([]bool, 8000)}
	f.wire.line = line
	engine, err := NewEngine(EngineConfig{TickHz: responderTestHz, ResponderAddress: 0x52},
		Ports{Levels: line, Edges: line, Ticks: line, Sink: f.responder})
	responderWantOK(t, "engine", err)
	engine.Step()
	return f, line, engine
}

func TestAA26EngineLoopbackCompletesThroughTheAA25WireModel(t *testing.T) {
	f, line, engine := newLoopback(t)
	request := append([]byte{0xAA}, testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)...)
	start := line.base + 3*testCharacter
	requestEnd := line.paintAll(start, request)
	expected := f.expectedEmits(requestEnd - testCharacter)
	responseEnd := expected[len(expected)-1].notBefore + testCharacter
	runEngine(line, engine, responseEnd)
	if !equalEmits(f.wire.emits, expected) {
		t.Fatalf("loopback emissions = %+v; want %+v", f.wire.emits, expected)
	}
	ackStart := responseEnd + testCharacter/2
	line.paintAll(ackStart, []byte{0x00, 0xAA})
	runEngine(line, engine, ackStart+3*testCharacter)
	if line.edges != 0 || engine.Stats().TXEdges != 0 || len(f.wire.starts) != 0 {
		t.Fatal("the wire engine transmitted or an initiator start was issued")
	}
	events := f.drain()
	wantRaw := append(append([]byte(nil), request...), 0x00)
	wantRaw = append(append(wantRaw, testEncode(testResponseData)...), 0x00)
	if string(rawOf(events)) != string(wantRaw) {
		t.Fatalf("loopback observed % X; want % X", rawOf(events), wantRaw)
	}
	if lastClosed(t, events).Reason != CloseCompleted {
		t.Fatalf("loopback closure = %+v", lastClosed(t, events))
	}
	f.wantClosedClean("loopback")

	// A CRC character whose stop bit is low never becomes a symbol: no ACK.
	bad, badLine, badEngine := newLoopback(t)
	next := badLine.paintAll(start, request[:len(request)-1])
	badLine.paint(next, request[len(request)-1], false)
	runEngine(badLine, badEngine, next+5*testCharacter)
	if len(bad.wire.emits) != 0 {
		t.Fatalf("ACK after a bad stop bit: %+v", bad.wire.emits)
	}
	if bad.responder.Stats().Closed[CloseFraming] != 1 {
		t.Fatalf("bad stop closure stats = %+v", bad.responder.Stats())
	}
	bad.wantClosedClean("bad stop")
}

func TestAA26SaturatedObserverNeverDelaysThePreparedACK(t *testing.T) {
	free := responderNewFixture(t, defaultOptions())
	freeEnd := free.complete(testFrameTick)
	options := defaultOptions()
	options.capacity = 1
	full := responderNewFixture(t, options)
	// Fill the single observer slot before the transaction and never drain it.
	full.responder.deliver(ObserverEvent{Kind: ObserverRaw})
	wire := append([]byte{0xAA}, testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)...)
	next := full.send(testFrameTick, wire[:len(wire)-1]...)
	full.symbol(wire[len(wire)-1], next)
	// The ACK is scheduled inside the very call that delivered the CRC.
	if len(full.wire.emits) != 1 {
		t.Fatal("ACK scheduling waited for the saturated observer")
	}
	fullEnd := full.send(full.echoPending(), 0x00)
	if fullEnd != freeEnd || !equalEmits(full.wire.emits, free.wire.emits) {
		t.Fatalf("saturated emissions %+v differ from %+v", full.wire.emits, free.wire.emits)
	}
	stats := full.responder.Stats()
	if stats.ObserverDropped == 0 || stats.Closed[CloseCompleted] != 1 {
		t.Fatalf("saturated stats = %+v", stats)
	}
	events := full.drain()
	if len(events) != 1 || events[0].Key != (WireKey{}) {
		t.Fatalf("a full ring was overwritten: %+v", events)
	}
	if free.responder.Stats().ObserverDropped != 0 {
		t.Fatal("control run dropped observer events")
	}
}

func TestAA26AdoptionRequiresTheExactAA24PlanReference(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PlanRef)
	}{
		{"subscriber", func(ref *PlanRef) { ref.Key.Subscriber++ }},
		{"address", func(ref *PlanRef) { ref.Key.Address = testOtherAddress }},
		{"registration epoch", func(ref *PlanRef) { ref.Key.Epoch++ }},
		{"worker", func(ref *PlanRef) { ref.Worker++ }},
		{"stream", func(ref *PlanRef) { ref.Stream++ }},
		{"plan generation", func(ref *PlanRef) { ref.Plan++ }},
		{"plan expiry", func(ref *PlanRef) { ref.PlanExpiry-- }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := responderNewFixture(t, defaultOptions())
			ref := f.refFor(2)
			notice := f.publish(2, ref, f.entry(testRequestData, []byte{'N'}))
			mutated := ref
			tc.mutate(&mutated)
			_, _, err := f.responder.AdoptPlan(notice, mutated, f.tick)
			responderWantErr(t, "mutated adoption", err, ErrAuthority)
			if f.view.planTop[testAddress] != 1 {
				t.Fatal("a refused adoption reached the worker view")
			}
			// The exact pair still adopts afterwards: the refusal had no effect.
			_, _, err = f.responder.AdoptPlan(notice, ref, f.tick)
			responderWantOK(t, "exact adoption", err)
		})
	}
}

func TestAA26SelectionRequiresTheSamePlanOnTheAA24Lane(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PlanRef)
	}{
		{"exact control", func(*PlanRef) {}},
		{"subscriber", func(ref *PlanRef) { ref.Key.Subscriber++ }},
		{"address", func(ref *PlanRef) { ref.Key.Address = testOtherAddress }},
		{"registration epoch", func(ref *PlanRef) { ref.Key.Epoch++ }},
		{"plan generation", func(ref *PlanRef) { ref.Plan++ }},
		{"plan expiry", func(ref *PlanRef) { ref.PlanExpiry-- }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := responderNewFixture(t, defaultOptions())
			ref := f.refFor(2)
			published := ref
			tc.mutate(&published)
			notice := f.publish(2, published, f.entry(testRequestData, testResponseData))
			_, _, err := f.responder.AdoptPlan(notice, ref, f.tick)
			responderWantOK(t, "adopt", err)
			f.request(testFrameTick)
			selected := f.responder.Stats().Selected == 1
			answered := len(f.wire.emits) == 1
			if tc.name == "exact control" {
				if !selected || !answered {
					t.Fatal("the exact IPC reference was not admitted")
				}
				return
			}
			if selected || answered || len(f.drain()) != 0 {
				t.Fatal("selected without the same plan on the IPC lane")
			}
		})
	}
}

func testCommand(f *responderFixture, token OwnerToken) CommandIdentity {
	return CommandIdentity{
		Ref:             f.ref,
		Transaction:     TransactionOrdinal(token),
		OwnerToken:      token,
		OwnerExpiry:     400000,
		RequestDeadline: 300000,
	}
}

func admitCommand(f *responderFixture, identity CommandIdentity) {
	f.t.Helper()
	responderWantOK(f.t, "send command", f.host.SendCommand(identity, []byte{0x01}, f.now()))
	_, ok, err := f.ipc.NextCommand(make([]byte, 0, 16), f.now())
	if err != nil || !ok {
		f.t.Fatalf("next command ok=%v err=%v", ok, err)
	}
}

func TestAA26InitiatorStartsUseOnlyTheAdmittedOwnerAndWaitForClosure(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	identity := testCommand(f, 7)
	admitCommand(f, identity)
	// The responder transaction holds the wire from selection to closure.
	requestEnd := f.send(testFrameTick, 0xAA, testInitiator, testAddress)
	responderWantErr(t, "start during selection", f.responder.StartInitiator(identity, testOtherInitiator, f.tick), ErrWireBusy)
	rest := testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)[2:]
	f.send(requestEnd, rest...)
	f.symbol(0x00, f.wire.emits[0].notBefore)
	f.echoed = 1
	responderWantErr(t, "start during response", f.responder.StartInitiator(identity, testOtherInitiator, f.tick), ErrWireBusy)
	if len(f.wire.starts) != 0 {
		t.Fatal("an initiator start reached the wire during a response")
	}
	f.send(f.echoPending(), 0x00)
	f.wantClosedClean("before initiator")
	// Nothing was queued: the refused start is gone until the caller offers it again.
	if len(f.wire.starts) != 0 {
		t.Fatal("a refused start was replayed")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*CommandIdentity)
	}{
		{"subscriber", func(value *CommandIdentity) { value.Ref.Key.Subscriber++ }},
		{"address", func(value *CommandIdentity) { value.Ref.Key.Address = testOtherAddress }},
		{"registration epoch", func(value *CommandIdentity) { value.Ref.Key.Epoch++ }},
		{"worker", func(value *CommandIdentity) { value.Ref.Worker++ }},
		{"stream", func(value *CommandIdentity) { value.Ref.Stream++ }},
		{"plan generation", func(value *CommandIdentity) { value.Ref.Plan++ }},
		{"plan expiry", func(value *CommandIdentity) { value.Ref.PlanExpiry-- }},
		{"transaction", func(value *CommandIdentity) { value.Transaction++ }},
		{"owner token", func(value *CommandIdentity) { value.OwnerToken++ }},
		{"owner expiry", func(value *CommandIdentity) { value.OwnerExpiry-- }},
		{"request deadline", func(value *CommandIdentity) { value.RequestDeadline-- }},
	} {
		mutated := identity
		tc.mutate(&mutated)
		if f.responder.StartInitiator(mutated, testOtherInitiator, f.tick) == nil {
			t.Fatalf("%s: a command the IPC worker never admitted took the wire", tc.name)
		}
	}
	if len(f.wire.starts) != 0 {
		t.Fatal("a mutated owner reached the wire")
	}
	responderWantErr(t, "responder address as initiator", f.responder.StartInitiator(identity, testAddress, f.tick), ErrAddress)
	responderWantOK(t, "admitted start", f.responder.StartInitiator(identity, testOtherInitiator, f.tick))
	if len(f.wire.starts) != 1 || f.wire.starts[0] != identity {
		t.Fatalf("starts = %+v", f.wire.starts)
	}
	responderWantErr(t, "second start", f.responder.StartInitiator(identity, testOtherInitiator, f.tick), ErrWireBusy)
	// While the initiator holds the wire, a frame to the registered address is not selected.
	f.request(f.tick + testCharacter)
	if _, active := f.responder.Active(); active || len(f.wire.emits) != len(f.expectedEmits(0)) {
		t.Fatal("the responder answered during an initiator hold")
	}
	mutated := identity
	mutated.OwnerToken++
	responderWantErr(t, "end with another owner", f.responder.EndInitiator(mutated), ErrNotHeld)
	responderWantOK(t, "end initiator", f.responder.EndInitiator(identity))
	f.request(f.tick + testCharacter)
	if _, active := f.responder.Active(); !active {
		t.Fatal("the responder stayed excluded after the initiator hold ended")
	}
}

func TestAA26ExpiredInitiatorHoldReleasesAtItsExclusiveDeadline(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	identity := testCommand(f, 3)
	identity.OwnerExpiry = Tick(testFrameTick + 50)
	admitCommand(f, identity)
	responderWantOK(t, "start", f.responder.StartInitiator(identity, testOtherInitiator, f.tick))
	f.responder.Poll(testFrameTick + 49)
	if !f.responder.initiatorHeld {
		t.Fatal("initiator hold dropped before its deadline")
	}
	f.responder.Poll(testFrameTick + 50)
	if f.responder.initiatorHeld {
		t.Fatal("initiator hold survived its exclusive deadline")
	}
}

func TestAA26CommandSelectorSourceAndDataVaryIndependently(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source byte
		pb     byte
		sb     byte
		data   []byte
		answer bool
	}{
		{"exact control", testInitiator, responderTestPB, responderTestSB, testRequestData, true},
		{"source", testOtherInitiator, responderTestPB, responderTestSB, testRequestData, false},
		{"primary selector", testInitiator, responderTestPB + 1, responderTestSB, testRequestData, false},
		{"secondary selector", testInitiator, responderTestPB, responderTestSB + 1, testRequestData, false},
		{"data length", testInitiator, responderTestPB, responderTestSB, testRequestData[:2], false},
		{"data content", testInitiator, responderTestPB, responderTestSB, []byte{0x02, 0xA9, 0xAA}, false},
		{"data order", testInitiator, responderTestPB, responderTestSB, []byte{0x01, 0xAA, 0xA9}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := responderNewFixture(t, defaultOptions())
			wire := testRequestWire(tc.source, testAddress, tc.pb, tc.sb, tc.data)
			f.send(testFrameTick, append([]byte{0xAA}, wire...)...)
			events := f.drain()
			if tc.answer {
				if len(f.wire.emits) != 1 {
					t.Fatal("exact request not answered")
				}
				return
			}
			if len(f.wire.emits) != 0 {
				t.Fatal("an unmatched request was answered")
			}
			if lastClosed(t, events).Reason != CloseNoMatch ||
				string(rawOf(events)) != string(append([]byte{0xAA}, wire...)) {
				t.Fatalf("unmatched observation = %+v", events)
			}
			f.wantClosedClean(tc.name)
		})
	}
}

func TestAA26PhaseRulesEndInSilence(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	f.request(testFrameTick)
	end := f.echoPending()
	emitted := len(f.wire.emits)
	f.send(end, 0xFF)
	// The only admitted failure action is silence: no repeat after a NACK.
	if len(f.wire.emits) != emitted || lastClosed(t, f.drain()).Reason != CloseInitiatorNACK {
		t.Fatal("the response was repeated after an initiator NACK")
	}
	f.wantClosedClean("NACK")

	odd := responderNewFixture(t, defaultOptions())
	odd.request(testFrameTick)
	odd.send(odd.echoPending(), 0x5A)
	if lastClosed(t, odd.drain()).Reason != CloseMalformed {
		t.Fatal("an unexpected final symbol was not malformed")
	}

	idle := responderNewFixture(t, defaultOptions())
	idle.request(testFrameTick)
	idle.echoPending()
	lastRaw := idle.responder.lastRaw
	idle.responder.Poll(lastRaw + testIdle - 1)
	if _, active := idle.responder.Active(); !active {
		t.Fatal("closed before the idle bound")
	}
	idle.responder.Poll(lastRaw + testIdle)
	if lastClosed(t, idle.drain()).Reason != CloseIdle {
		t.Fatal("silent initiator did not close at the idle bound")
	}

	// No ACK phase exists for a broadcast or an initiator destination.
	for _, target := range []byte{0xFE, testOtherInitiator} {
		none := responderNewFixture(t, defaultOptions())
		wire := testRequestWire(testInitiator, target, responderTestPB, responderTestSB, testRequestData)
		none.send(testFrameTick, append([]byte{0xAA}, wire...)...)
		if none.responder.Stats().Selected != 0 || len(none.wire.emits) != 0 {
			t.Fatalf("destination %02X was selected", target)
		}
	}
	// Symbols after a completed transaction belong to nobody until a new frame.
	after := responderNewFixture(t, defaultOptions())
	afterEnd := after.complete(testFrameTick)
	after.drain()
	after.send(afterEnd, 0x00, 0x05, 0xA9)
	if len(after.drain()) != 0 || len(after.wire.emits) != len(after.expectedEmits(0)) {
		t.Fatal("symbols after completion were delivered or answered")
	}
}

// startForDecision returns the SYN start that puts the CRC decision at a tick.
func startForDecision(decision uint64) uint64 {
	wire := testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)
	return decision - testStopCentre - uint64(len(wire))*testCharacter
}

func TestAA26DeadlinesAreExclusiveForEachExpiry(t *testing.T) {
	deadline := Tick(5000)
	for _, tc := range []struct {
		name   string
		mutate func(*options)
	}{
		{"plan expiry", func(value *options) { value.planExpiry = deadline }},
		{"registration lease", func(value *options) { value.lease = deadline }},
		{"source lease", func(value *options) { value.sourceLease = deadline }},
	} {
		for _, offset := range []uint64{1, 0} {
			options := defaultOptions()
			tc.mutate(&options)
			f := responderNewFixture(t, options)
			f.request(startForDecision(uint64(deadline) - offset))
			answered := len(f.wire.emits) == 1
			if answered != (offset == 1) {
				t.Fatalf("%s: decision %d ticks before the deadline answered=%v", tc.name, offset, answered)
			}
			if offset == 0 {
				if lastClosed(t, f.drain()).Reason != CloseExpired {
					t.Fatalf("%s: expiry closure missing", tc.name)
				}
				f.wantClosedClean(tc.name)
			}
		}
	}
}

// midResponseTick is the observation tick of the response symbol 0 echo, the
// point at which midResponse stops.
func midResponseTick() uint64 {
	wire := testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)
	crcStart := testFrameTick + uint64(len(wire))*testCharacter
	return crcStart + 2*testCharacter + testTurnaround + testStopCentre
}

// midResponse starts an exchange and stops with response symbol 1 in flight.
func midResponse(t *testing.T) *responderFixture {
	t.Helper()
	return midResponseWith(t, defaultOptions())
}

func midResponseWith(t *testing.T, options options) *responderFixture {
	t.Helper()
	f := responderNewFixture(t, options)
	f.request(testFrameTick)
	for index := 0; index < 2; index++ {
		emit := f.wire.emits[f.echoed]
		f.echoed++
		f.symbol(emit.symbol, emit.notBefore)
	}
	if len(f.wire.emits) != 3 || f.tick != midResponseTick() {
		t.Fatalf("mid-response setup has %d emissions at %d", len(f.wire.emits), f.tick)
	}
	return f
}

// deliverNotices hands every pending plan revocation to the responder and
// returns the acknowledgements to the registry.
func (f *responderFixture) deliverNotices() {
	f.t.Helper()
	for _, notice := range f.registry.PendingRevocations(make([]RevocationNotice, 0, 8)) {
		ack, ready, err := f.responder.ObserveRevocation(notice)
		responderWantOK(f.t, "observe revocation", err)
		if !ready {
			f.t.Fatalf("no acknowledgement for %+v: a pin survived", notice)
		}
		responderWantOK(f.t, "acknowledge", f.registry.ObserveAcknowledgement(ack))
	}
}

func TestAA26InvalidationLeavesNoResponseAuthority(t *testing.T) {
	// The lease ends between the response symbol 0 echo and the in-flight echo.
	lease := Tick(midResponseTick() + 2)
	for _, tc := range []struct {
		name     string
		reason   CloseReason
		disarmed bool
		lease    Tick
		unbound  bool
		trigger  func(*responderFixture)
	}{
		{"unregister", CloseRevoked, false, 0, true, func(f *responderFixture) {
			f.registry.Unregister(f.key)
			f.deliverNotices()
		}},
		{"subscriber disconnect", CloseRevoked, false, 0, true, func(f *responderFixture) {
			f.registry.DisconnectSubscriber(f.key.Subscriber)
			f.deliverNotices()
		}},
		{"registry reset", CloseRevoked, false, 0, true, func(f *responderFixture) {
			f.registry.Reset()
			f.deliverNotices()
		}},
		{"physical conflict", CloseRevoked, false, 0, true, func(f *responderFixture) {
			f.registry.MarkPhysicalConflict(testAddress)
			f.deliverNotices()
		}},
		{"source generation", CloseRevoked, false, 0, true, func(f *responderFixture) {
			responderWantOK(f.t, "advance source", f.registry.AdvanceSource(f.key, responderTestSource+1, testFar, f.now()))
			f.deliverNotices()
		}},
		{"lease deadline", CloseExpired, false, lease, false, func(f *responderFixture) {
			f.responder.Poll(uint64(lease))
		}},
		{"lease expiry notice", CloseExpired, false, lease, true, func(f *responderFixture) {
			f.at(uint64(lease))
			responderWantOK(f.t, "expire due", f.registry.ExpireDue(f.now()))
			f.deliverNotices()
		}},
		{"revocation", CloseRevoked, false, 0, false, func(f *responderFixture) {
			responderWantOK(f.t, "revoke", f.host.RevokePlan(f.ref, f.now()))
			f.responder.Poll(f.tick)
		}},
		{"stop", CloseFenced, false, 0, false, func(f *responderFixture) {
			responderWantOK(f.t, "stop", f.host.RequestStop(f.now()))
			f.responder.Poll(f.tick)
		}},
		{"wire gap", CloseGap, true, 0, false, func(f *responderFixture) {
			f.responder.TryPublish(Event{Kind: EventGap, Tick: f.tick})
		}},
		{"event loss", CloseGap, true, 0, false, func(f *responderFixture) {
			f.responder.TryPublish(Event{Kind: EventLoss, Tick: f.tick})
		}},
		{"tick regression", CloseClock, true, 0, false, func(f *responderFixture) {
			f.responder.TryPublish(Event{Kind: EventFault, Fault: FaultTickRegression, Tick: 1})
		}},
		{"lost character", CloseFraming, false, 0, false, func(f *responderFixture) {
			f.responder.TryPublish(Event{Kind: EventFault, Fault: FaultBadStop, Tick: f.tick})
		}},
		{"echo mismatch", CloseCollision, false, 0, false, func(f *responderFixture) {
			emit := f.wire.emits[2]
			f.symbol(emit.symbol^0x01, emit.notBefore)
		}},
		{"SYN instead of echo", CloseCollision, false, 0, false, func(f *responderFixture) {
			f.symbol(0xAA, f.wire.emits[2].notBefore)
		}},
		{"early echo", CloseCollision, false, 0, false, func(f *responderFixture) {
			emit := f.wire.emits[2]
			f.symbol(emit.symbol, emit.notBefore-1)
		}},
		{"missing echo", CloseCollision, false, 0, false, func(f *responderFixture) {
			emit := f.wire.emits[2]
			f.responder.Poll(emit.notBefore + testCharacter + testEchoAllowance)
		}},
		{"stream generation", CloseStream, false, 0, true, func(f *responderFixture) {
			_, successor := attach(f.t, testStream+1)
			responderWantOK(f.t, "advance stream", f.responder.AdvanceStream(testStream+1, successor))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := defaultOptions()
			if tc.lease != 0 {
				options.lease = tc.lease
			}
			f := midResponseWith(t, options)
			inFlight := f.wire.emits[2]
			tc.trigger(f)
			f.wantClosedClean(tc.name)
			closed := lastClosed(t, f.drain())
			if closed.Reason != tc.reason || closed.Key.Identity != f.notice.Identity {
				t.Fatalf("closure = %+v; want reason %d", closed, tc.reason)
			}
			// A host-side retirement or a new stream also drops the address binding, so
			// nothing but a newly adopted plan can admit the address again.
			if f.responder.bindings[testAddress].valid == tc.unbound {
				t.Fatalf("binding valid=%v; want unbound=%v", !tc.unbound, tc.unbound)
			}
			if f.responder.Disarmed() != tc.disarmed {
				t.Fatalf("disarmed = %v", f.responder.Disarmed())
			}
			// Replaying the in-flight echo and the remaining response authorizes nothing.
			replay := f.tick + testCharacter
			f.symbol(inFlight.symbol, replay)
			f.send(replay+testCharacter, testEncode(testResponseData)[2:]...)
			if len(f.wire.emits) != 3 {
				t.Fatalf("%d emissions after invalidation; want the 3 before it", len(f.wire.emits))
			}
			f.wantClosedClean("replay")
		})
	}
}

func TestAA26PortRefusalClosesAndRetainsNothing(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	f.request(testFrameTick)
	ack := f.wire.emits[0]
	f.wire.refuse = true
	f.symbol(ack.symbol, ack.notBefore)
	f.wantClosedClean("refused")
	if lastClosed(t, f.drain()).Reason != CloseRefused {
		t.Fatal("refused emission did not close the transaction")
	}
}

func TestAA26SupersessionKeepsTheSelectedGeneration(t *testing.T) {
	f := midResponse(t)
	old := f.notice.Identity
	newer := []byte{'N', 'E', 'W'}
	f.install(2, f.entry(testRequestData, newer))
	f.deliverNoticesExceptPinned(old)
	end := f.echoPending()
	expected := f.expectedEmits(f.wire.emits[0].notBefore - testCharacter - testTurnaround)
	if !equalEmits(f.wire.emits, expected) {
		t.Fatalf("superseded transaction changed bytes: %+v", f.wire.emits)
	}
	f.send(end, 0x00)
	if ack, ready := f.responder.TakeAcknowledgement(); !ready || ack.Identity != old {
		t.Fatalf("superseded plan not acknowledged at closure: %+v %v", ack, ready)
	} else {
		responderWantOK(t, "acknowledge superseded", f.registry.ObserveAcknowledgement(ack))
	}
	f.wire.emits = nil
	f.echoed = 0
	f.request(end + 10*testCharacter)
	f.echoPending()
	key, _ := f.responder.Active()
	if key.Identity.Plan != 2 || len(f.wire.emits) != 1+len(testEncode(newer)) {
		t.Fatalf("next transaction key=%+v emits=%+v", key, f.wire.emits)
	}
}

// deliverNoticesExceptPinned delivers pending notices; the pinned plan's
// acknowledgement must wait for its transaction to close.
func (f *responderFixture) deliverNoticesExceptPinned(pinned PlanIdentity) {
	f.t.Helper()
	for _, notice := range f.registry.PendingRevocations(make([]RevocationNotice, 0, 8)) {
		_, ready, err := f.responder.ObserveRevocation(notice)
		responderWantOK(f.t, "observe revocation", err)
		if notice.Identity == pinned && ready {
			f.t.Fatal("a pinned generation was acknowledged")
		}
	}
}

func TestAA26UnregisterDuringResponseStopsAndDisarmsAdmission(t *testing.T) {
	f := midResponse(t)
	f.registry.Unregister(f.key)
	f.deliverNotices()
	f.wantClosedClean("unregister")
	if f.registry.Registration(f.key).State != RegistrationAbsent {
		t.Fatal("registration did not settle after the closure acknowledgement")
	}
	emitted := len(f.wire.emits)
	f.request(f.tick + 10*testCharacter)
	if len(f.wire.emits) != emitted || f.responder.Stats().Selected != 1 {
		t.Fatal("a later destination was admitted after unregistration")
	}
	// Re-registration under a new epoch needs its own plan before anything is answered.
	f.key.Epoch++
	f.register(f.key)
	f.request(f.tick + testCharacter)
	if len(f.wire.emits) != emitted {
		t.Fatal("a new registration inherited the old plan")
	}
	f.install(2, f.entry(testRequestData, []byte{'R'}))
	f.request(f.tick + testCharacter)
	key, active := f.responder.Active()
	if !active || key.Identity.Registration.Epoch != 2 || len(f.wire.emits) != emitted+1 {
		t.Fatalf("new epoch not admitted: key=%+v", key)
	}
}

func TestAA26OrdinalsSequencesAndPreparationNeverCarryOver(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	// Transaction 1 is prepared and then interrupted before its CRC.
	wire := testRequestWire(testInitiator, testAddress, responderTestPB, responderTestSB, testRequestData)
	next := f.send(testFrameTick, append([]byte{0xAA}, wire[:len(wire)-1]...)...)
	if !f.responder.prepared {
		t.Fatal("setup did not prepare")
	}
	next = f.send(next, 0xAA)
	f.wantClosedClean("interrupted")
	if lastClosed(t, f.drain()).Reason != CloseInterrupted {
		t.Fatal("SYN did not interrupt the transaction")
	}
	// Transaction 2 is a valid but unmatched request: the earlier preparation is gone.
	other := testRequestWire(testInitiator, testAddress, responderTestPB+1, responderTestSB, testRequestData)
	f.send(next, other...)
	if len(f.wire.emits) != 0 {
		t.Fatal("a stale preparation answered another request")
	}
	events := f.drain()
	if events[0].Key.Transaction != 2 || events[0].Sequence != uint64(len(wire)+1) {
		t.Fatalf("second transaction starts with %+v", events[0])
	}
	// A new stream generation drops every binding of the old one ...
	successorHost, successor := attach(t, testStream+1)
	responderWantErr(t, "successor of another stream", f.responder.AdvanceStream(testStream+2, successor), ErrResponderIdentity)
	responderWantOK(t, "advance stream", f.responder.AdvanceStream(testStream+1, successor))
	responderWantErr(t, "stale stream", f.responder.AdvanceStream(testStream+1, successor), ErrStaleGeneration)
	for address := range f.responder.bindings {
		if f.responder.bindings[address].valid {
			t.Fatalf("binding for %02X survived the stream change", address)
		}
	}
	f.request(f.tick + testCharacter)
	if f.responder.Stats().Selected != 2 {
		t.Fatal("an old-stream plan was selected on the new stream")
	}
	// ... and its ordinals restart: the unselected request above was ordinal 1 of
	// the new stream, so the next request is ordinal 2, not 4.
	responderWantOK(t, "bind stream", f.registry.BindWorker(testWorker, testStream+1))
	f.deliverNotices()
	f.stream = testStream + 1
	f.host, f.ipc = successorHost, successor
	f.install(2, f.entry(testRequestData, testResponseData))
	f.request(f.tick + testCharacter)
	key, active := f.responder.Active()
	if !active || key.Transaction != 2 || key.Identity.Stream != testStream+1 || len(f.wire.emits) != 1 {
		t.Fatalf("new-stream transaction key=%+v active=%v", key, active)
	}
}

func TestAA26GapDisarmsUntilTheNextStream(t *testing.T) {
	f := responderNewFixture(t, defaultOptions())
	f.responder.TryPublish(Event{Kind: EventGap, Tick: testFrameTick})
	if !f.responder.Disarmed() {
		t.Fatal("gap did not disarm admission")
	}
	f.request(testFrameTick + testCharacter)
	if f.responder.Stats().Selected != 0 || len(f.drain()) != 0 {
		t.Fatal("selection after a gap without a new stream")
	}
	_, successor := attach(t, testStream+1)
	responderWantOK(t, "advance stream", f.responder.AdvanceStream(testStream+1, successor))
	if f.responder.Disarmed() {
		t.Fatal("a new stream did not re-arm admission")
	}
}

func responderParseSource(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func TestAA26ProductionShapeHasNoWaitAndNoResponderStart(t *testing.T) {
	file := responderParseSource(t, "responder.go")
	for _, importSpec := range file.Imports {
		switch importSpec.Path.Value {
		case `"errors"`, `"math"`, `"math/bits"`:
		default:
			t.Fatalf("forbidden import %s", importSpec.Path.Value)
		}
	}
	for _, decl := range file.Decls {
		name := ""
		if funcDecl, isFunc := decl.(*ast.FuncDecl); isFunc {
			name = funcDecl.Name.Name
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.GoStmt, *ast.DeferStmt, *ast.SelectStmt, *ast.SendStmt, *ast.ChanType, *ast.FuncLit:
				t.Fatalf("forbidden construct %T in %s", value, name)
			case *ast.SelectorExpr:
				// Only the explicit initiator-start entry point may reach the port's START.
				if value.Sel.Name == "StartInitiator" && name != "StartInitiator" && name != "" {
					t.Fatalf("%s reaches the initiator start", name)
				}
			}
			return true
		})
	}
}

// TestAA26ClosedEventCarriesThePhaseActiveAtClosure closes through the same IPC
// revocation in each phase; it catches a closure event that reports the ended
// phase because the frame state was reset before the event was built.
func TestAA26ClosedEventCarriesThePhaseActiveAtClosure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase Phase
		setup func(*testing.T) *responderFixture
	}{
		{"request", PhaseRequest, func(t *testing.T) *responderFixture {
			f := responderNewFixture(t, defaultOptions())
			f.send(testFrameTick, 0xAA, testInitiator, testAddress)
			return f
		}},
		{"target ack", PhaseTargetACK, func(t *testing.T) *responderFixture {
			f := responderNewFixture(t, defaultOptions())
			f.request(testFrameTick)
			return f
		}},
		{"response", PhaseResponse, midResponse},
		{"initiator ack", PhaseInitiatorACK, func(t *testing.T) *responderFixture {
			f := responderNewFixture(t, defaultOptions())
			f.request(testFrameTick)
			f.echoPending()
			return f
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.setup(t)
			if _, active := f.responder.Active(); !active {
				t.Fatal("setup left no active transaction")
			}
			f.drain()
			responderWantOK(t, "revoke", f.host.RevokePlan(f.ref, f.now()))
			f.responder.Poll(f.tick)
			closed := lastClosed(t, f.drain())
			if closed.Reason != CloseRevoked || closed.Phase != tc.phase {
				t.Fatalf("closure reason=%d phase=%d; want revoked in phase %d", closed.Reason, closed.Phase, tc.phase)
			}
			f.wantClosedClean(tc.name)
		})
	}
}
