package localworker

import (
	"crypto/sha256"
	binarytest "encoding/binary"
	errorstest "errors"
	mathtest "math"
	"testing"
	unsafetest "unsafe"
)

// These tests run one serialized schedule over byte arrays with injected time. They
// prove the model's validation and state rules, never cross-process ordering.

const (
	ipcFixtureDomain  = DefaultClockDomain
	ipcFixtureWorker  = WorkerID(11)
	ipcFixtureStream  = StreamGeneration(21)
	ipcFixtureAddress = byte(0x15)
	fixtureExpiry     = Tick(5000)
	fixtureOwner      = Tick(4000)
	fixtureDeadline   = Tick(3000)
	fixtureSlotBytes  = uint32(152)
	fixtureArenaBytes = uint64(96)
	fixtureBudget     = Tick(100)
)

var ipcFixtureLimits = IPCLimits{
	MaxMappingBytes:     1 << 16,
	MaxSlotBytes:        256,
	MaxCapacity:         64,
	MaxPayloadBytes:     16,
	MaxArenaBytes:       1024,
	MaxPlans:            4,
	MaxOutstanding:      4,
	ControlServiceTicks: fixtureBudget,
}

var fixtureConfig = IPCConfig{
	Lanes: [laneCount]LaneConfig{
		{SlotBytes: fixtureSlotBytes, Capacity: 3},
		{SlotBytes: fixtureSlotBytes, Capacity: 3},
		{SlotBytes: fixtureSlotBytes, Capacity: 2},
		{SlotBytes: fixtureSlotBytes, Capacity: 2},
	},
	ArenaBytes: fixtureArenaBytes,
}

func fixtureBuild() BuildIdentity {
	return BuildIdentity(sha256.Sum256([]byte("synthetic build")))
}

func fixtureBinding() Binding {
	return Binding{Build: fixtureBuild(), Worker: ipcFixtureWorker, Stream: ipcFixtureStream}
}

func fixtureBytes() int {
	total := 64 + 4*64 + fixtureArenaBytes
	for _, lane := range fixtureConfig.Lanes {
		total += uint64(lane.SlotBytes) * uint64(lane.Capacity)
	}
	return int(total)
}

// fixtureMemory returns byte storage with a guaranteed 8-byte aligned base.
func fixtureMemory(bytes int) []byte {
	words := make([]uint64, (bytes+7)/8)
	return unsafetest.Slice((*byte)(unsafetest.Pointer(&words[0])), bytes)
}

func at(tick Tick) ClockReading {
	return ClockReading{Domain: ipcFixtureDomain, Tick: tick}
}

func fixtureRef(plan PlanGeneration) PlanRef {
	return PlanRef{
		Key:        RegistrationKey{Subscriber: 3, Address: ipcFixtureAddress, Epoch: 5},
		Worker:     ipcFixtureWorker,
		Stream:     ipcFixtureStream,
		Plan:       plan,
		PlanExpiry: fixtureExpiry,
	}
}

func fixtureCommand(plan PlanGeneration, token OwnerToken) CommandIdentity {
	return CommandIdentity{
		Ref:             fixtureRef(plan),
		Transaction:     TransactionOrdinal(token),
		OwnerToken:      token,
		OwnerExpiry:     fixtureOwner,
		RequestDeadline: fixtureDeadline,
	}
}

func fixtureRaw(octet byte) Observation {
	return Observation{Transaction: 9, RawOctet: octet, Direction: DirectionReceive, Phase: 1}
}

type pair struct {
	t        *testing.T
	mem      []byte
	expected Expected
	host     *Host
	worker   *Worker
}

func fixtureMapping(t *testing.T) ([]byte, Expected) {
	t.Helper()
	mem := fixtureMemory(fixtureBytes())
	expected, _, err := InitMapping(mem, fixtureBinding(), fixtureConfig, ipcFixtureLimits)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	return mem, expected
}

func newPair(t *testing.T) *pair {
	t.Helper()
	mem, expected := fixtureMapping(t)
	return attachPair(t, mem, expected)
}

func attachPair(t *testing.T, mem []byte, expected Expected) *pair {
	t.Helper()
	host, err := AttachHost(mem, expected)
	if err != nil {
		t.Fatalf("attach host: %v", err)
	}
	worker, err := AttachWorker(mem, expected)
	if err != nil {
		t.Fatalf("attach worker: %v", err)
	}
	return &pair{t: t, mem: mem, expected: expected, host: host, worker: worker}
}

func want(t *testing.T, label string, got error, expected error) {
	t.Helper()
	if !errorstest.Is(got, expected) {
		t.Fatalf("%s: got %v, want %v", label, got, expected)
	}
}

func (pair *pair) publish(plan PlanGeneration, tick Tick) PlanRef {
	pair.t.Helper()
	ref := fixtureRef(plan)
	arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	want(pair.t, "publish", pair.host.PublishPlan(ref, arenaStart, 8, at(tick)), nil)
	want(pair.t, "worker poll", pair.worker.PollControl(at(tick)), nil)
	return ref
}

func (pair *pair) admit(plan PlanGeneration, token OwnerToken, tick Tick) CommandIdentity {
	pair.t.Helper()
	identity := fixtureCommand(plan, token)
	want(pair.t, "send", pair.host.SendCommand(identity, []byte{1, 2}, at(tick)), nil)
	_, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(tick))
	if err != nil || !ok {
		pair.t.Fatalf("next command: ok=%v err=%v", ok, err)
	}
	return identity
}

func (pair *pair) poll(tick Tick) (Delivery, bool, error) {
	return pair.host.Poll(make([]byte, 0, 16), at(tick))
}

func putSlot64(slot []byte, offset int, value uint64) {
	binarytest.LittleEndian.PutUint64(slot[offset:], value)
}

func TestAA24LayoutWritesTheDocumentedBytes(t *testing.T) {
	mem, expected := fixtureMapping(t)
	layout, err := ValidateMapping(mem, expected)
	if err != nil {
		t.Fatal(err)
	}
	get32 := binarytest.LittleEndian.Uint32
	get64 := binarytest.LittleEndian.Uint64
	if string(mem[0:4]) != "LWB1" || get32(mem[4:]) != 1 || get32(mem[8:]) != 64 ||
		int(get32(mem[12:])) != len(mem) || get64(mem[16:]) != 11 || get64(mem[24:]) != 21 ||
		get32(mem[32:]) != 4 || get32(mem[36:]) != 64 || get64(mem[40:]) != 64 {
		t.Fatalf("header bytes: % x", mem[:64])
	}
	arenaOffset := get64(mem[48:])
	if arenaOffset != layout.ArenaOffset || get64(mem[56:]) != fixtureArenaBytes ||
		arenaOffset+fixtureArenaBytes != uint64(len(mem)) {
		t.Fatalf("arena: offset %d layout %+v", arenaOffset, layout)
	}
	build := fixtureBuild()
	if string(mem[arenaOffset:arenaOffset+32]) != string(build[:]) {
		t.Fatal("build identity is not at the arena start")
	}
	cursor := uint64(64 + 4*64)
	for index := 0; index < 4; index++ {
		desc := mem[64+index*64:]
		if int(get32(desc)) != index+1 || get32(desc[4:]) != fixtureSlotBytes ||
			get32(desc[8:]) != fixtureConfig.Lanes[index].Capacity ||
			get32(desc[12:]) != 0 || get64(desc[16:]) != cursor ||
			get64(desc[24:]) != 0 || get64(desc[32:]) != 0 {
			t.Fatalf("descriptor %d: % x", index, desc[:64])
		}
		cursor += uint64(fixtureSlotBytes) * uint64(fixtureConfig.Lanes[index].Capacity)
	}
}

func TestAA24ValidateRefusesEachLayoutFieldBeforeAccess(t *testing.T) {
	put32 := binarytest.LittleEndian.PutUint32
	put64 := binarytest.LittleEndian.PutUint64
	desc := func(index int) int { return 64 + index*64 }
	region := func(mem []byte, index int) uint64 {
		return binarytest.LittleEndian.Uint64(mem[desc(index)+16:])
	}
	cases := []struct {
		name   string
		mutate func([]byte)
		err    error
	}{
		{"magic", func(mem []byte) { mem[3] = 'X' }, ErrABI},
		{"version", func(mem []byte) { put32(mem[4:], 2) }, ErrABI},
		{"header bytes", func(mem []byte) { put32(mem[8:], 72) }, ErrABI},
		{"total bytes", func(mem []byte) { put32(mem[12:], uint32(len(mem))-8) }, ErrMappingSize},
		{"worker", func(mem []byte) { put64(mem[16:], 12) }, ErrIPCIdentity},
		{"stream", func(mem []byte) { put64(mem[24:], 22) }, ErrIPCIdentity},
		{"lane count", func(mem []byte) { put32(mem[32:], 3) }, ErrABI},
		{"descriptor bytes", func(mem []byte) { put32(mem[36:], 72) }, ErrABI},
		{"descriptor misaligned", func(mem []byte) { put64(mem[40:], 68) }, ErrAlignment},
		{"descriptor out of bounds", func(mem []byte) { put64(mem[40:], uint64(len(mem))) }, ErrBounds},
		{"descriptor sum overflow", func(mem []byte) { put64(mem[40:], mathtest.MaxUint64-7) }, ErrBounds},
		{"arena misaligned", func(mem []byte) {
			put64(mem[48:], binarytest.LittleEndian.Uint64(mem[48:])+4)
		}, ErrAlignment},
		{"arena out of bounds", func(mem []byte) { put64(mem[48:], uint64(len(mem))) }, ErrBounds},
		{"arena sum overflow", func(mem []byte) { put64(mem[48:], mathtest.MaxUint64-7) }, ErrBounds},
		{"arena bytes zero", func(mem []byte) { put64(mem[56:], 0) }, ErrBounds},
		{"arena only build", func(mem []byte) { put64(mem[56:], 32) }, ErrBounds},
		{"arena bytes over limit", func(mem []byte) { put64(mem[56:], 2048) }, ErrBounds},
		{"arena over slot region", func(mem []byte) {
			target := region(mem, 3)
			arena := binarytest.LittleEndian.Uint64(mem[48:])
			copy(mem[target:], mem[arena:arena+32])
			put64(mem[48:], target)
		}, ErrOverlap},
		{"lane kind unknown", func(mem []byte) { put32(mem[desc(0):], 5) }, ErrLane},
		{"lane kind zero", func(mem []byte) { put32(mem[desc(0):], 0) }, ErrLane},
		{"lane kind duplicate", func(mem []byte) { put32(mem[desc(1):], 1) }, ErrLane},
		{"slot bytes below envelope", func(mem []byte) { put32(mem[desc(2)+4:], 120) }, ErrLane},
		{"slot bytes not multiple", func(mem []byte) { put32(mem[desc(2)+4:], 132) }, ErrLane},
		{"slot bytes over limit", func(mem []byte) { put32(mem[desc(2)+4:], 264) }, ErrLane},
		{"capacity zero", func(mem []byte) { put32(mem[desc(2)+8:], 0) }, ErrLane},
		{"capacity over limit", func(mem []byte) { put32(mem[desc(2)+8:], 65) }, ErrLane},
		{"host control without reserved room", func(mem []byte) { put32(mem[desc(0)+8:], 1) }, ErrLane},
		{"worker control without reserved room", func(mem []byte) { put32(mem[desc(1)+8:], 1) }, ErrLane},
		{"capacity past mapping", func(mem []byte) { put32(mem[desc(3)+8:], 64) }, ErrBounds},
		{"reserved word", func(mem []byte) { put32(mem[desc(1)+12:], 1) }, ErrReserved},
		{"reserved tail first", func(mem []byte) { mem[desc(1)+40] = 1 }, ErrReserved},
		{"reserved tail last", func(mem []byte) { mem[desc(1)+63] = 1 }, ErrReserved},
		{"slot region misaligned", func(mem []byte) { put64(mem[desc(2)+16:], region(mem, 2)+4) }, ErrAlignment},
		{"slot region out of bounds", func(mem []byte) { put64(mem[desc(2)+16:], uint64(len(mem))) }, ErrBounds},
		{"slot region sum overflow", func(mem []byte) { put64(mem[desc(2)+16:], mathtest.MaxUint64-7) }, ErrBounds},
		{"slot region over header", func(mem []byte) { put64(mem[desc(2)+16:], 0) }, ErrOverlap},
		{"slot region over descriptors", func(mem []byte) { put64(mem[desc(2)+16:], 64) }, ErrOverlap},
		{"slot region over neighbour", func(mem []byte) { put64(mem[desc(2)+16:], region(mem, 1)) }, ErrOverlap},
		{"slot region one word into neighbour", func(mem []byte) { put64(mem[desc(2)+16:], region(mem, 2)+8) }, ErrOverlap},
		{"producer index", func(mem []byte) { put64(mem[desc(3)+24:], 1) }, ErrIndex},
		{"consumer index", func(mem []byte) { put64(mem[desc(3)+32:], 1) }, ErrIndex},
		{"build first byte", func(mem []byte) { mem[binarytest.LittleEndian.Uint64(mem[48:])] ^= 1 }, ErrBuild},
		{"build last byte", func(mem []byte) { mem[binarytest.LittleEndian.Uint64(mem[48:])+31] ^= 1 }, ErrBuild},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem, expected := fixtureMapping(t)
			if _, err := ValidateMapping(mem, expected); err != nil {
				t.Fatalf("positive control: %v", err)
			}
			tc.mutate(mem)
			_, err := ValidateMapping(mem, expected)
			want(t, tc.name, err, tc.err)
			_, err = AttachHost(mem, expected)
			want(t, "host attach", err, tc.err)
			_, err = AttachWorker(mem, expected)
			want(t, "worker attach", err, tc.err)
		})
	}
}

func TestAA24ValidateDescriptorTablePlacement(t *testing.T) {
	mem, expected := fixtureMapping(t)
	table := append([]byte(nil), mem[64:64+4*64]...)
	target := binarytest.LittleEndian.Uint64(table[3*64+16:])
	copy(mem[target:], table)
	binarytest.LittleEndian.PutUint64(mem[40:], target)
	_, err := ValidateMapping(mem, expected)
	want(t, "table over a slot region", err, ErrOverlap)

	// Header bytes never decode as a valid descriptor, so a table placed over the
	// header is refused while its first descriptor is read.
	placements := []struct {
		offset uint64
		err    error
	}{
		{0, ErrLane}, {8, ErrLane}, {16, ErrLane}, {24, ErrLane},
		{32, ErrReserved}, {40, ErrLane}, {48, ErrLane}, {56, ErrLane},
	}
	for _, placement := range placements {
		mem, expected = fixtureMapping(t)
		binarytest.LittleEndian.PutUint64(mem[40:], placement.offset)
		_, err = ValidateMapping(mem, expected)
		want(t, "table over the header", err, placement.err)
	}
}

func TestAA24ValidateVariesExpectedIdentity(t *testing.T) {
	mem, expected := fixtureMapping(t)
	cases := []struct {
		name   string
		mutate func(*Expected)
		err    error
	}{
		{"build", func(e *Expected) { e.Binding.Build[31] ^= 1 }, ErrBuild},
		{"zero build", func(e *Expected) { e.Binding.Build = BuildIdentity{} }, ErrBuild},
		{"worker", func(e *Expected) { e.Binding.Worker++ }, ErrIPCIdentity},
		{"zero worker", func(e *Expected) { e.Binding.Worker = 0 }, ErrIPCIdentity},
		{"stream", func(e *Expected) { e.Binding.Stream++ }, ErrIPCIdentity},
		{"zero stream", func(e *Expected) { e.Binding.Stream = 0 }, ErrIPCIdentity},
		{"mapping bytes", func(e *Expected) { e.MappingBytes += 8 }, ErrMappingSize},
		{"mapping limit", func(e *Expected) { e.Limits.MaxMappingBytes = 64 }, ErrMappingSize},
		{"arena limit", func(e *Expected) { e.Limits.MaxArenaBytes = 64 }, ErrBounds},
		{"slot limit", func(e *Expected) {
			e.Limits.MaxSlotBytes = 144
			e.Limits.MaxPayloadBytes = 16
		}, ErrLane},
		{"capacity limit", func(e *Expected) { e.Limits.MaxCapacity = 2 }, ErrLane},
		{"limit zero", func(e *Expected) { e.Limits.MaxPlans = 0 }, ErrIPCInvalidLimits},
	}
	for _, tc := range cases {
		variant := expected
		tc.mutate(&variant)
		_, err := ValidateMapping(mem, variant)
		want(t, tc.name, err, tc.err)
	}
	if _, err := ValidateMapping(mem, expected); err != nil {
		t.Fatalf("positive control after variants: %v", err)
	}
}

func TestAA24ValidateRefusesBadBaseAndShortMappings(t *testing.T) {
	mem, expected := fixtureMapping(t)
	shifted := fixtureMemory(fixtureBytes() + 8)[1 : fixtureBytes()+1]
	copy(shifted, mem)
	_, err := ValidateMapping(shifted, expected)
	want(t, "misaligned base", err, ErrAlignment)
	_, err = ValidateMapping(nil, expected)
	want(t, "empty", err, ErrAlignment)
	_, err = ValidateMapping(mem[:32], expected)
	want(t, "shorter than the header", err, ErrMappingSize)
	_, err = ValidateMapping(mem[:len(mem)-8], expected)
	want(t, "truncated mapping", err, ErrMappingSize)
}

func TestAA24InitRefusalLeavesMemoryUntouched(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Binding, *IPCConfig, *IPCLimits)
		err    error
	}{
		{"zero build", func(b *Binding, _ *IPCConfig, _ *IPCLimits) { b.Build = BuildIdentity{} }, ErrBuild},
		{"zero worker", func(b *Binding, _ *IPCConfig, _ *IPCLimits) { b.Worker = 0 }, ErrIPCIdentity},
		{"zero stream", func(b *Binding, _ *IPCConfig, _ *IPCLimits) { b.Stream = 0 }, ErrIPCIdentity},
		{"control capacity", func(_ *Binding, c *IPCConfig, _ *IPCLimits) { c.Lanes[1].Capacity = 1 }, ErrLane},
		{"slot bytes", func(_ *Binding, c *IPCConfig, _ *IPCLimits) { c.Lanes[3].SlotBytes = 150 }, ErrLane},
		{"arena too small", func(_ *Binding, c *IPCConfig, _ *IPCLimits) { c.ArenaBytes = 32 }, ErrIPCInvalidLimits},
		{"arena misaligned", func(_ *Binding, c *IPCConfig, _ *IPCLimits) { c.ArenaBytes = 100 }, ErrIPCInvalidLimits},
		{"size mismatch", func(_ *Binding, c *IPCConfig, _ *IPCLimits) { c.ArenaBytes = 104 }, ErrMappingSize},
		{"mapping over limit", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxMappingBytes = 64 }, ErrMappingSize},
		{"limit mapping", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxMappingBytes = 0 }, ErrIPCInvalidLimits},
		{"limit capacity", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxCapacity = 0 }, ErrIPCInvalidLimits},
		{"limit capacity ceiling", func(_ *Binding, _ *IPCConfig, l *IPCLimits) {
			l.MaxCapacity = capacityCeiling + 1
		}, ErrIPCInvalidLimits},
		{"limit payload", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxPayloadBytes = 0 }, ErrIPCInvalidLimits},
		{"limit payload over slot", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxPayloadBytes = 129 }, ErrIPCInvalidLimits},
		{"limit slot", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxSlotBytes = 127 }, ErrIPCInvalidLimits},
		{"limit arena", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxArenaBytes = 0 }, ErrIPCInvalidLimits},
		{"limit plans", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxPlans = 0 }, ErrIPCInvalidLimits},
		{"limit plans ceiling", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxPlans = tableCeiling + 1 }, ErrIPCInvalidLimits},
		{"limit outstanding", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.MaxOutstanding = 0 }, ErrIPCInvalidLimits},
		{"limit budget", func(_ *Binding, _ *IPCConfig, l *IPCLimits) { l.ControlServiceTicks = 0 }, ErrIPCInvalidLimits},
	}
	for _, tc := range cases {
		binding, config, limits := fixtureBinding(), fixtureConfig, ipcFixtureLimits
		tc.mutate(&binding, &config, &limits)
		mem := fixtureMemory(fixtureBytes())
		for index := range mem {
			mem[index] = 0xA5
		}
		_, _, err := InitMapping(mem, binding, config, limits)
		want(t, tc.name, err, tc.err)
		for _, octet := range mem {
			if octet != 0xA5 {
				t.Fatalf("%s: refusal changed caller memory", tc.name)
			}
		}
	}
	shifted := fixtureMemory(fixtureBytes() + 8)[1 : fixtureBytes()+1]
	_, _, err := InitMapping(shifted, fixtureBinding(), fixtureConfig, ipcFixtureLimits)
	want(t, "misaligned init", err, ErrAlignment)
	for _, octet := range shifted {
		if octet != 0 {
			t.Fatal("a misaligned init wrote caller memory")
		}
	}
}

func TestAA24EnvelopeBytesAndDetachedPayload(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	identity := fixtureCommand(1, 1)
	want(t, "send", pair.host.SendCommand(identity, []byte{0xCA, 0xFE}, at(10)), nil)
	slot := pair.host.lane(LaneHostData).slot(0)
	get32 := binarytest.LittleEndian.Uint32
	get64 := binarytest.LittleEndian.Uint64
	fields := []struct {
		name   string
		offset int
		value  uint64
	}{
		{"worker", 8, 11}, {"stream", 16, 21}, {"transaction", 24, 1}, {"plan", 32, 1},
		{"subscriber", 40, 3}, {"epoch", 48, 5}, {"token", 56, 1}, {"owner expiry", 64, uint64(fixtureOwner)},
		{"sequence", 72, 0}, {"capture", 80, 0}, {"plan expiry", 88, uint64(fixtureExpiry)},
		{"deadline", 96, uint64(fixtureDeadline)}, {"arena offset", 104, 0},
	}
	for _, field := range fields {
		if get64(slot[field.offset:]) != field.value {
			t.Fatalf("%s at %d: %d", field.name, field.offset, get64(slot[field.offset:]))
		}
	}
	if get32(slot) != uint32(RecordCommand) || get32(slot[4:]) != 2 || get32(slot[112:]) != 0 ||
		get32(slot[116:]) != 0 || slot[120] != ipcFixtureAddress || slot[121] != 0 || slot[122] != 0 ||
		slot[123] != 0 || get32(slot[124:]) != 0 || slot[128] != 0xCA || slot[129] != 0xFE || slot[130] != 0 {
		t.Fatalf("envelope bytes: % x", slot)
	}
	delivery, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(11))
	if err != nil || !ok {
		t.Fatalf("next: %v %v", ok, err)
	}
	if !delivery.Record.identity().matches(identity) {
		t.Fatalf("identity: %+v", delivery.Record)
	}
	clear(slot)
	if delivery.Record.Payload[0] != 0xCA || delivery.Record.Payload[1] != 0xFE {
		t.Fatal("delivered payload aliases shared memory")
	}
}

func TestAA24MappingBytesAreRelocatableWithoutPointers(t *testing.T) {
	source := newPair(t)
	target := fixtureMemory(fixtureBytes())
	expected := source.expected
	copy(target, source.mem)
	worker, err := AttachWorker(target, expected)
	if err != nil {
		t.Fatal(err)
	}
	arenaStart := source.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	want(t, "publish", source.host.PublishPlan(fixtureRef(1), arenaStart, 8, at(10)), nil)
	want(t, "send", source.host.SendCommand(fixtureCommand(1, 1), []byte{7}, at(10)), nil)
	copy(target, source.mem)
	for index := range source.mem {
		source.mem[index] = 0xFF
	}
	delivery, ok, err := worker.NextCommand(make([]byte, 0, 16), at(11))
	if err != nil || !ok || delivery.Record.Payload[0] != 7 {
		t.Fatalf("copied bytes alone must carry the record: ok=%v err=%v %+v", ok, err, delivery)
	}
}

func TestAA24RingOrderWrapAndCounters(t *testing.T) {
	pair := newPair(t)
	sequence := uint64(0)
	for round := 0; round < 5; round++ {
		tick := Tick(10 + round)
		for octet := byte(0); octet < 2; octet++ {
			got, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(tick))
			sequence++
			if err != nil || got != sequence {
				t.Fatalf("observe %d: %d %v", sequence, got, err)
			}
		}
		for octet := byte(0); octet < 2; octet++ {
			delivery, ok, err := pair.poll(tick)
			if err != nil || !ok || delivery.Record.RawOctet != octet {
				t.Fatalf("poll round %d: %+v %v %v", round, delivery, ok, err)
			}
		}
		if _, ok, err := pair.poll(tick); ok || err != nil {
			t.Fatalf("empty lane: %v %v", ok, err)
		}
	}
	stats := pair.worker.LaneStats(LaneWorkerData)
	if stats.Enqueued != 10 || stats.HighWater != 2 || stats.Refused != 0 {
		t.Fatalf("producer stats: %+v", stats)
	}
	stats = pair.host.LaneStats(LaneWorkerData)
	if stats.Dequeued != 10 || stats.HighWater != 2 {
		t.Fatalf("consumer stats: %+v", stats)
	}
	if !pair.host.OutputOpen() || !pair.worker.OutputOpen() {
		t.Fatal("ordinary traffic fenced an endpoint")
	}
}

func TestAA24LaneOwnershipIsExclusive(t *testing.T) {
	pair := newPair(t)
	record := Record{Kind: RecordStop, Worker: ipcFixtureWorker, Stream: ipcFixtureStream}
	want(t, "worker writes host control", pair.worker.lane(LaneHostControl).write(&record, 0), ErrOwnership)
	_, _, err := pair.host.lane(LaneHostControl).read(nil)
	want(t, "host reads its own control", err, ErrOwnership)
	want(t, "host writes worker data", pair.host.lane(LaneWorkerData).write(&record, 0), ErrOwnership)
	_, _, err = pair.worker.lane(LaneWorkerData).read(nil)
	want(t, "worker reads its own data", err, ErrOwnership)
}

func TestAA24LiveIndexInvariantsVaryProducerAndConsumer(t *testing.T) {
	// The consumer has seen producer 2 and released consumer 1. Each case breaks
	// exactly one invariant from that state.
	cases := []struct {
		name     string
		producer uint64
		consumer uint64
	}{
		{"occupancy above capacity", 4, 1},
		{"producer regression", 1, 1},
		{"consumer regression", 2, 0},
		{"consumer tampered forward", 2, 2},
		{"consumer ahead of producer", 2, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pair := newPair(t)
			lane := pair.host.lane(LaneWorkerData)
			for octet := byte(0); octet < 2; octet++ {
				if _, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(10)); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok, err := pair.poll(10); !ok || err != nil {
				t.Fatalf("positive control: %v %v", ok, err)
			}
			storeRelease(pair.mem, lane.layout.producerOffset, tc.producer)
			storeRelease(pair.mem, lane.layout.consumerOffset, tc.consumer)
			_, ok, err := pair.poll(11)
			want(t, "consumer view", err, ErrIndex)
			if ok || pair.host.OutputOpen() || pair.host.Fence().Reason != FenceCorruption {
				t.Fatalf("host stayed open: %+v", pair.host.Fence())
			}
		})
	}
	t.Run("occupancy above capacity from a clean consumer", func(t *testing.T) {
		pair := newPair(t)
		lane := pair.host.lane(LaneWorkerData)
		storeRelease(pair.mem, lane.layout.producerOffset, 3)
		_, _, err := pair.poll(10)
		want(t, "occupancy", err, ErrIndex)
	})
	t.Run("producer sees consumer ahead", func(t *testing.T) {
		pair := newPair(t)
		lane := pair.worker.lane(LaneWorkerData)
		if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(10)); err != nil {
			t.Fatal(err)
		}
		storeRelease(pair.mem, lane.layout.consumerOffset, 2)
		_, err := pair.worker.ObserveRaw(fixtureRaw(2), at(10))
		want(t, "consumer ahead", err, ErrIndex)
	})
	t.Run("producer view", func(t *testing.T) {
		pair := newPair(t)
		lane := pair.worker.lane(LaneWorkerData)
		storeRelease(pair.mem, lane.layout.producerOffset, 1)
		_, err := pair.worker.ObserveRaw(fixtureRaw(1), at(10))
		want(t, "own index tampered", err, ErrIndex)
		if pair.worker.OutputOpen() {
			t.Fatal("worker stayed open")
		}
	})
	t.Run("producer sees consumer regress", func(t *testing.T) {
		pair := newPair(t)
		lane := pair.worker.lane(LaneWorkerData)
		if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(10)); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := pair.poll(10); !ok || err != nil {
			t.Fatalf("poll: %v %v", ok, err)
		}
		if _, err := pair.worker.ObserveRaw(fixtureRaw(2), at(10)); err != nil {
			t.Fatal(err)
		}
		storeRelease(pair.mem, lane.layout.consumerOffset, 0)
		_, err := pair.worker.ObserveRaw(fixtureRaw(3), at(10))
		want(t, "consumer regression", err, ErrIndex)
	})
}

func TestAA24CountersRefuseToWrap(t *testing.T) {
	t.Run("lane index", func(t *testing.T) {
		pair := newPair(t)
		lane := pair.worker.lane(LaneWorkerData)
		maximum := uint64(mathtest.MaxUint64)
		storeRelease(pair.mem, lane.layout.producerOffset, maximum)
		storeRelease(pair.mem, lane.layout.consumerOffset, maximum)
		lane.localProducer, lane.localConsumer = maximum, maximum
		_, err := pair.worker.ObserveRaw(fixtureRaw(1), at(10))
		want(t, "producer at maximum", err, ErrCounter)
		if loadAcquire(pair.mem, lane.layout.producerOffset) != maximum {
			t.Fatal("producer index wrapped")
		}
		if pair.worker.Fence().Reason != FenceCounter || pair.worker.Lost() != 1 {
			t.Fatalf("fence %+v lost %d", pair.worker.Fence(), pair.worker.Lost())
		}
	})
	t.Run("capture sequence keeps the final ordinal for the gap", func(t *testing.T) {
		pair := newPair(t)
		pair.worker.nextSequence = mathtest.MaxUint64 - 1
		pair.host.nextCapture = mathtest.MaxUint64 - 1
		sequence, err := pair.worker.ObserveRaw(fixtureRaw(1), at(10))
		if err != nil || sequence != mathtest.MaxUint64-1 {
			t.Fatalf("last ordinary ordinal: %d %v", sequence, err)
		}
		_, err = pair.worker.ObserveRaw(fixtureRaw(2), at(11))
		want(t, "exhausted", err, ErrCounter)
		delivery, ok, err := pair.poll(12)
		if err != nil || !ok || delivery.Record.Kind != RecordGap ||
			delivery.Record.CaptureSequence != mathtest.MaxUint64 {
			t.Fatalf("gap: %+v %v %v", delivery, ok, err)
		}
		if pair.host.nextCapture == 0 {
			t.Fatal("host capture cursor wrapped")
		}
	})
	t.Run("host capture cursor stops at the final ordinal", func(t *testing.T) {
		pair := newPair(t)
		host := pair.host
		host.nextCapture = mathtest.MaxUint64
		if !host.accountSequence(mathtest.MaxUint64, true) || host.nextCapture != mathtest.MaxUint64 {
			t.Fatalf("direct: next %d", host.nextCapture)
		}
		host.nextCapture = mathtest.MaxUint64 - 1
		if !host.accountSequence(mathtest.MaxUint64, true) || !host.accountSequence(mathtest.MaxUint64-1, false) ||
			host.nextCapture != mathtest.MaxUint64 {
			t.Fatalf("collapse: next %d", host.nextCapture)
		}
	})
	t.Run("loss counter saturates and fences", func(t *testing.T) {
		pair := newPair(t)
		pair.worker.lost = mathtest.MaxUint64
		for octet := byte(0); octet < 2; octet++ {
			if _, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(10)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pair.worker.ObserveRaw(fixtureRaw(2), at(10)); !errorstest.Is(err, ErrGap) {
			t.Fatalf("symbol past the lane: got %v, want %v", err, ErrGap)
		}
		if pair.worker.Lost() != mathtest.MaxUint64 || pair.worker.OutputOpen() {
			t.Fatalf("lost %d open %v", pair.worker.Lost(), pair.worker.OutputOpen())
		}
	})
	t.Run("lane statistic saturates and fences", func(t *testing.T) {
		pair := newPair(t)
		lane := pair.worker.lane(LaneWorkerData)
		lane.stats.Enqueued = mathtest.MaxUint64
		if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(10)); err != nil {
			t.Fatal(err)
		}
		if lane.stats.Enqueued != mathtest.MaxUint64 {
			t.Fatal("statistic wrapped")
		}
		want(t, "poll", pair.worker.PollControl(at(11)), nil)
		if pair.worker.Fence().Reason != FenceCounter {
			t.Fatalf("fence %+v", pair.worker.Fence())
		}
		host := pair.host.lane(LaneWorkerData)
		host.stats.Dequeued = mathtest.MaxUint64
		if _, ok, err := pair.poll(12); err != nil || !ok {
			t.Fatalf("host poll: %v %v", ok, err)
		}
		if _, _, err := pair.poll(13); err != nil {
			t.Fatal(err)
		}
		if pair.host.OutputOpen() {
			t.Fatal("host stayed open after a saturated statistic")
		}
	})
}

func TestAA24TruncatedAndOversizeRecordsFenceBeforeUse(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]byte)
		err    error
	}{
		{"payload past the slot", func(slot []byte) { binarytest.LittleEndian.PutUint32(slot[4:], 25) }, ErrTruncated},
		{"payload length huge", func(slot []byte) { binarytest.LittleEndian.PutUint32(slot[4:], mathtest.MaxUint32) }, ErrTruncated},
		{"payload above the cap", func(slot []byte) { binarytest.LittleEndian.PutUint32(slot[4:], 17) }, ErrIPCOversize},
		{"padding not zero", func(slot []byte) { slot[151] = 1 }, ErrRecord},
		{"unknown kind", func(slot []byte) { binarytest.LittleEndian.PutUint32(slot, 9) }, ErrRecord},
		{"wrong-lane kind", func(slot []byte) {
			binarytest.LittleEndian.PutUint32(slot, uint32(RecordStop))
		}, ErrRecord},
		{"flags", func(slot []byte) { binarytest.LittleEndian.PutUint32(slot[124:], 1) }, ErrRecord},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pair := newPair(t)
			if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(10)); err != nil {
				t.Fatal(err)
			}
			lane := pair.host.lane(LaneWorkerData)
			tc.mutate(lane.slot(0))
			_, ok, err := pair.poll(11)
			want(t, tc.name, err, tc.err)
			if ok || lane.localConsumer != 0 ||
				loadAcquire(pair.mem, lane.layout.consumerOffset) != 0 {
				t.Fatal("a malformed record was consumed")
			}
			if pair.host.Fence().Reason != FenceCorruption {
				t.Fatalf("fence %+v", pair.host.Fence())
			}
		})
	}
	t.Run("short destination refuses without fencing", func(t *testing.T) {
		pair := newPair(t)
		pair.publish(1, 10)
		want(t, "send", pair.host.SendCommand(fixtureCommand(1, 1), []byte{1, 2, 3}, at(10)), nil)
		_, _, err := pair.worker.NextCommand(make([]byte, 0, 2), at(11))
		want(t, "short", err, ErrBuffer)
		if !pair.worker.OutputOpen() {
			t.Fatal("short caller buffer fenced the stream")
		}
		if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 3), at(11)); !ok || err != nil {
			t.Fatalf("retry: %v %v", ok, err)
		}
	})
	t.Run("oversize send has no effect", func(t *testing.T) {
		pair := newPair(t)
		pair.publish(1, 10)
		want(t, "oversize", pair.host.SendCommand(fixtureCommand(1, 1), make([]byte, 17), at(10)), ErrIPCOversize)
		if !pair.host.OutputOpen() || pair.host.lane(LaneHostData).localProducer != 0 {
			t.Fatal("oversize send changed state")
		}
		want(t, "same token retried", pair.host.SendCommand(fixtureCommand(1, 1), make([]byte, 16), at(10)), nil)
	})
}

func TestAA24RecordShapeVariesEveryKindField(t *testing.T) {
	pair := newPair(t)
	layout := pair.host.lanes[0].arenaLayout
	identity := fixtureCommand(1, 1)
	arenaStart := layout.ArenaOffset + buildBytes
	stream := Record{Worker: ipcFixtureWorker, Stream: ipcFixtureStream}
	build := func(kind RecordKind, fill func(*Record)) Record {
		record := stream
		record.Kind = kind
		fill(&record)
		return record
	}
	publish := build(RecordPublish, func(r *Record) {
		r.applyRef(identity.Ref)
		r.ArenaOffset, r.PlanBytes = arenaStart, 8
	})
	revoke := build(RecordRevoke, func(r *Record) { r.applyRef(identity.Ref) })
	ack := build(RecordAcknowledge, func(r *Record) {
		r.applyRef(identity.Ref)
		r.Status, r.CaptureSequence, r.CaptureTime = StatusRetired, 1, 1
	})
	gap := build(RecordGap, func(r *Record) { r.CaptureSequence, r.CaptureTime = 1, 1 })
	stop := build(RecordStop, func(*Record) {})
	command := build(RecordCommand, func(r *Record) { r.applyIdentity(identity) })
	unselected := build(RecordRaw, func(r *Record) {
		r.Transaction, r.Direction, r.CaptureSequence, r.CaptureTime = 1, DirectionReceive, 1, 1
	})
	selected := build(RecordRaw, func(r *Record) {
		r.applyIdentity(identity)
		r.Direction, r.CaptureSequence, r.CaptureTime = DirectionEcho, 1, 1
	})
	completion := build(RecordCompletion, func(r *Record) {
		r.applyIdentity(identity)
		r.Status, r.CaptureSequence, r.CaptureTime = StatusAccepted, 1, 1
	})
	valid := []struct {
		name   string
		lane   LaneKind
		record Record
	}{
		{"publish", LaneHostControl, publish},
		{"revoke", LaneHostControl, revoke},
		{"stop", LaneHostControl, stop},
		{"acknowledge", LaneWorkerControl, ack},
		{"gap", LaneWorkerControl, gap},
		{"command", LaneHostData, command},
		{"raw unselected", LaneWorkerData, unselected},
		{"raw selected", LaneWorkerData, selected},
		{"completion", LaneWorkerData, completion},
	}
	// Each field flips between zero and nonzero, or takes a value no kind allows, so
	// no field stays a valid value for its kind. Exact nonzero comparison is tested
	// by the identity tests.
	mutations := []struct {
		name   string
		mutate func(*Record)
	}{
		{"worker", func(r *Record) { flip(&r.Worker) }},
		{"stream", func(r *Record) { flip(&r.Stream) }},
		{"transaction", func(r *Record) { flip(&r.Transaction) }},
		{"plan", func(r *Record) { flip(&r.Plan) }},
		{"subscriber", func(r *Record) { flip(&r.Subscriber) }},
		{"epoch", func(r *Record) { flip(&r.Epoch) }},
		{"token", func(r *Record) { flip(&r.OwnerToken) }},
		{"owner expiry", func(r *Record) { flip(&r.OwnerExpiry) }},
		{"sequence", func(r *Record) { flip(&r.CaptureSequence) }},
		{"capture", func(r *Record) { flip(&r.CaptureTime) }},
		{"plan expiry", func(r *Record) { flip(&r.PlanExpiry) }},
		{"deadline", func(r *Record) { flip(&r.RequestDeadline) }},
		{"arena offset", func(r *Record) { flip(&r.ArenaOffset) }},
		{"plan bytes", func(r *Record) { flip(&r.PlanBytes) }},
		{"target", func(r *Record) { flip(&r.Target) }},
		{"status", func(r *Record) { r.Status = 4 }},
		{"direction", func(r *Record) { r.Direction = 3 }},
		{"phase", func(r *Record) { r.Phase = 6 }},
		{"flags", func(r *Record) { r.Flags = 1 }},
	}
	for _, tc := range valid {
		original := tc.record
		if !original.shape(tc.lane, layout) {
			t.Fatalf("%s: positive control refused: %+v", tc.name, original)
		}
		for _, mutation := range mutations {
			variant := original
			mutation.mutate(&variant)
			if variant.shape(tc.lane, layout) {
				t.Fatalf("%s: %s change admitted: %+v", tc.name, mutation.name, variant)
			}
		}
		for _, other := range []LaneKind{LaneHostControl, LaneWorkerControl, LaneHostData, LaneWorkerData} {
			if other != tc.lane && original.shape(other, layout) {
				t.Fatalf("%s admitted on lane %d", tc.name, other)
			}
		}
	}
	for _, tc := range valid[:5] {
		variants := []func(*Record){
			func(r *Record) { r.Direction = DirectionReceive },
			func(r *Record) { r.Phase = 1 },
			func(r *Record) { r.RawOctet = 1 },
		}
		for index, mutate := range variants {
			variant := tc.record
			mutate(&variant)
			if variant.shape(tc.lane, layout) {
				t.Fatalf("%s: control variant %d admitted", tc.name, index)
			}
		}
	}
	for _, original := range []Record{publish, revoke, gap, stop} {
		variant := original
		variant.Status = StatusAccepted
		lane := LaneHostControl
		if original.Kind == RecordGap {
			lane = LaneWorkerControl
		}
		if variant.shape(lane, layout) {
			t.Fatalf("kind %d: status admitted", original.Kind)
		}
	}
	bad := publish
	bad.ArenaOffset = layout.ArenaOffset
	if bad.shape(LaneHostControl, layout) {
		t.Fatal("plan reference over the build identity admitted")
	}
	bad = publish
	bad.ArenaOffset, bad.PlanBytes = layout.ArenaOffset+fixtureArenaBytes-4, 8
	if bad.shape(LaneHostControl, layout) {
		t.Fatal("plan reference past the arena admitted")
	}
	bad = publish
	bad.ArenaOffset = mathtest.MaxUint64 - 2
	if bad.shape(LaneHostControl, layout) {
		t.Fatal("overflowing plan reference admitted")
	}
	bad = publish
	bad.Target = 0xFE
	if bad.shape(LaneHostControl, layout) {
		t.Fatal("broadcast target admitted")
	}
	bad = selected
	bad.Payload = []byte{1}
	if bad.shape(LaneWorkerData, layout) {
		t.Fatal("raw event payload admitted")
	}
	bad = unselected
	bad.Direction = DirectionControl
	if bad.shape(LaneWorkerData, layout) {
		t.Fatal("raw event without a direction admitted")
	}
	bad = completion
	bad.Status = StatusPending
	if bad.shape(LaneWorkerData, layout) {
		t.Fatal("pending completion admitted")
	}
	bad = ack
	bad.Status = StatusAccepted
	if bad.shape(LaneWorkerControl, layout) {
		t.Fatal("acknowledgement without retirement admitted")
	}
}

func flip[value ~uint8 | ~uint32 | ~uint64](field *value) {
	if *field == 0 {
		*field = 1
		return
	}
	*field = 0
}

func TestAA24FullDataLaneRaisesGapOutsideTheLane(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	identity := pair.admit(1, 1, 10)
	for octet := byte(0); octet < 2; octet++ {
		if _, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(20)); err != nil {
			t.Fatal(err)
		}
	}
	sequence, err := pair.worker.ObserveRaw(fixtureRaw(3), at(30))
	want(t, "full lane", err, ErrGap)
	if sequence != 3 || pair.worker.Lost() != 1 || pair.worker.OutputOpen() {
		t.Fatalf("seq %d lost %d open %v", sequence, pair.worker.Lost(), pair.worker.OutputOpen())
	}
	if stats := pair.worker.LaneStats(LaneWorkerData); stats.Refused != 1 || stats.HighWater != 2 {
		t.Fatalf("stats %+v", stats)
	}
	want(t, "selection fenced", pair.worker.AdmitOutput(identity, at(30)), ErrOutputFenced)
	_, err = pair.worker.ObserveRaw(fixtureRaw(4), at(31))
	want(t, "sampling after the gap returns", err, ErrOutputFenced)
	if pair.worker.Lost() != 2 {
		t.Fatalf("lost %d", pair.worker.Lost())
	}
	delivery, ok, err := pair.poll(40)
	if err != nil || !ok || delivery.Record.Kind != RecordGap ||
		delivery.Record.CaptureSequence != 3 || delivery.Record.CaptureTime != 30 ||
		delivery.DequeueTime != 40 {
		t.Fatalf("gap comes first with the lost symbol's identity: %+v %v %v", delivery, ok, err)
	}
	fence := pair.host.Fence()
	if fence != (FenceState{Reason: FenceWorkerGap, Sequence: 3, CaptureTime: 30}) {
		t.Fatalf("fence %+v", fence)
	}
	cancelled := pair.host.CancelledCommands(nil)
	if len(cancelled) != 1 || !cancelled[0].matches(identity) {
		t.Fatalf("cancelled %+v", cancelled)
	}
	if plans := pair.host.QuarantinedPlans(nil); len(plans) != 1 || !plans[0].matches(identity.Ref) {
		t.Fatalf("quarantined %+v", plans)
	}
	want(t, "new command fenced", pair.host.SendCommand(fixtureCommand(1, 2), nil, at(40)), ErrOutputFenced)
	want(t, "new publication fenced", pair.host.PublishPlan(fixtureRef(2), 0, 0, at(40)), ErrOutputFenced)
	for octet := byte(0); octet < 2; octet++ {
		delivery, ok, err = pair.poll(41)
		if err != nil || !ok || delivery.Record.RawOctet != octet {
			t.Fatalf("retained evidence %d: %+v %v %v", octet, delivery, ok, err)
		}
	}
	if pair.host.OutputOpen() {
		t.Fatal("draining evidence reopened output decisions")
	}
}

func TestAA24GapFitsAfterOrdinaryControlFillsItsShare(t *testing.T) {
	pair := newPair(t)
	refs := make([]PlanRef, 0, 3)
	for plan := PlanGeneration(1); plan <= 3; plan++ {
		refs = append(refs, pair.publish(plan, 10))
	}
	for _, ref := range refs {
		want(t, "revoke", pair.host.RevokePlan(ref, at(11)), nil)
		want(t, "worker poll", pair.worker.PollControl(at(11)), nil)
	}
	want(t, "first ack", pair.worker.AcknowledgeRevocation(refs[0], at(12)), nil)
	want(t, "second ack", pair.worker.AcknowledgeRevocation(refs[1], at(12)), nil)
	want(t, "third ack would take the reserved slot", pair.worker.AcknowledgeRevocation(refs[2], at(12)), ErrControlFull)
	if pair.worker.Fence().Reason != FenceControlSaturated {
		t.Fatalf("fence %+v", pair.worker.Fence())
	}
	var kinds []RecordKind
	for round := 0; round < 3; round++ {
		delivery, ok, err := pair.poll(13)
		if err != nil || !ok {
			t.Fatalf("poll %d: %v %v", round, ok, err)
		}
		kinds = append(kinds, delivery.Record.Kind)
	}
	if kinds[0] != RecordAcknowledge || kinds[1] != RecordAcknowledge || kinds[2] != RecordGap {
		t.Fatalf("kinds %v", kinds)
	}
	want(t, "retried ack still exact", pair.worker.AcknowledgeRevocation(refs[2], at(14)), nil)
	delivery, ok, err := pair.poll(15)
	if err != nil || !ok || delivery.Record.Plan != 3 {
		t.Fatalf("late ack: %+v %v %v", delivery, ok, err)
	}
	if plans := pair.host.QuarantinedPlans(nil); len(plans) != 0 {
		t.Fatalf("plans stayed quarantined: %+v", plans)
	}
}

func TestAA24LatchedGapSurvivesAnOccupiedControlLane(t *testing.T) {
	pair := newPair(t)
	control := pair.worker.lane(LaneWorkerControl)
	filler := Record{Kind: RecordGap, Worker: ipcFixtureWorker, Stream: ipcFixtureStream, CaptureSequence: 1, CaptureTime: 1}
	for round := 0; round < 3; round++ {
		want(t, "fill", control.write(&filler, 0), nil)
	}
	pair.worker.nextSequence = 2
	for octet := byte(0); octet < 2; octet++ {
		if _, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(20)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pair.worker.ObserveRaw(fixtureRaw(2), at(20)); !errorstest.Is(err, ErrGap) {
		t.Fatalf("symbol past the lane: got %v, want %v", err, ErrGap)
	}
	if pair.worker.gapWritten || !pair.worker.gapLatched {
		t.Fatal("gap must be latched while no slot exists")
	}
	for round := 0; round < 3; round++ {
		if _, ok, err := pair.poll(21); err != nil || !ok {
			t.Fatalf("drain: %v %v", ok, err)
		}
	}
	if _, err := pair.worker.ObserveRaw(fixtureRaw(9), at(22)); !errorstest.Is(err, ErrOutputFenced) {
		t.Fatalf("sampling while the gap flushes: got %v, want %v", err, ErrOutputFenced)
	}
	if !pair.worker.gapWritten {
		t.Fatal("latched gap was not flushed")
	}
	delivery, ok, err := pair.poll(23)
	if err != nil || !ok || delivery.Record.Kind != RecordGap ||
		delivery.Record.CaptureSequence != 4 || delivery.Record.CaptureTime != 20 {
		t.Fatalf("flushed gap: %+v %v %v", delivery, ok, err)
	}
}

func TestAA24ControlNeverWaitsBehindDataBacklog(t *testing.T) {
	t.Run("revocation overtakes queued commands", func(t *testing.T) {
		pair := newPair(t)
		ref := pair.publish(1, 10)
		want(t, "first", pair.host.SendCommand(fixtureCommand(1, 1), nil, at(11)), nil)
		want(t, "second", pair.host.SendCommand(fixtureCommand(1, 2), nil, at(11)), nil)
		want(t, "data lane full", pair.host.SendCommand(fixtureCommand(1, 3), nil, at(11)), ErrFull)
		if !pair.host.OutputOpen() {
			t.Fatal("a full data lane fenced the host")
		}
		want(t, "revoke beside full data", pair.host.RevokePlan(ref, at(12)), nil)
		_, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(13))
		want(t, "queued command behind revocation", err, ErrIPCRevoked)
		if ok {
			t.Fatal("a queued command outran its revocation")
		}
	})
	t.Run("revocation stops an already admitted command at the next symbol", func(t *testing.T) {
		pair := newPair(t)
		ref := pair.publish(1, 10)
		identity := pair.admit(1, 1, 10)
		want(t, "positive control", pair.worker.AdmitOutput(identity, at(11)), nil)
		want(t, "revoke", pair.host.RevokePlan(ref, at(12)), nil)
		want(t, "next symbol", pair.worker.AdmitOutput(identity, at(12)), ErrIPCRevoked)
	})
	t.Run("stop overtakes a saturated worker data lane", func(t *testing.T) {
		pair := newPair(t)
		pair.publish(1, 10)
		identity := pair.admit(1, 1, 10)
		for octet := byte(0); octet < 2; octet++ {
			if _, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(11)); err != nil {
				t.Fatal(err)
			}
		}
		want(t, "stop", pair.host.RequestStop(at(12)), nil)
		want(t, "next symbol", pair.worker.AdmitOutput(identity, at(12)), ErrOutputFenced)
		if pair.worker.Fence().Reason != FenceStop {
			t.Fatalf("fence %+v", pair.worker.Fence())
		}
	})
	t.Run("acknowledgement overtakes older data at the host", func(t *testing.T) {
		pair := newPair(t)
		ref := pair.publish(1, 10)
		want(t, "revoke", pair.host.RevokePlan(ref, at(10)), nil)
		want(t, "worker poll", pair.worker.PollControl(at(10)), nil)
		if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(11)); err != nil {
			t.Fatal(err)
		}
		want(t, "ack", pair.worker.AcknowledgeRevocation(ref, at(12)), nil)
		delivery, ok, err := pair.poll(13)
		if err != nil || !ok || delivery.Record.Kind != RecordAcknowledge || delivery.Record.CaptureSequence != 2 {
			t.Fatalf("ack first: %+v %v %v", delivery, ok, err)
		}
		delivery, ok, err = pair.poll(13)
		if err != nil || !ok || delivery.Record.Kind != RecordRaw || delivery.Record.CaptureSequence != 1 {
			t.Fatalf("older raw after: %+v %v %v", delivery, ok, err)
		}
		if !pair.host.OutputOpen() || pair.host.nextCapture != 3 || len(pair.host.explained) != 0 {
			t.Fatalf("interleaving is not a gap: %+v next %d", pair.host.Fence(), pair.host.nextCapture)
		}
	})
}

func TestAA24CaptureTimeIsNeverDequeueTime(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	identity := pair.admit(1, 1, 10)
	if _, err := pair.worker.ObserveRaw(fixtureRaw(0x5A), at(20)); err != nil {
		t.Fatal(err)
	}
	want(t, "complete", pair.worker.Complete(identity, StatusAccepted, at(25)), nil)
	delivery, ok, err := pair.poll(90)
	if err != nil || !ok {
		t.Fatalf("poll: %v %v", ok, err)
	}
	if delivery.Record.CaptureTime != 20 || delivery.DequeueTime != 90 || delivery.Record.CaptureSequence != 1 {
		t.Fatalf("raw times: %+v", delivery)
	}
	delivery, ok, err = pair.poll(95)
	if err != nil || !ok {
		t.Fatalf("poll: %v %v", ok, err)
	}
	if delivery.Record.CaptureTime != 25 || delivery.DequeueTime != 95 || delivery.Record.Kind != RecordCompletion {
		t.Fatalf("completion times: %+v", delivery)
	}
	if len(pair.host.outstanding) != 0 {
		t.Fatal("completion left the command outstanding")
	}
}

func TestAA24CaptureAndClockVariationsFence(t *testing.T) {
	observe := func(t *testing.T, pair *pair) {
		t.Helper()
		for round := Tick(0); round < 2; round++ {
			if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(20+round)); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("capture after dequeue", func(t *testing.T) {
		pair := newPair(t)
		observe(t, pair)
		_, ok, err := pair.poll(15)
		want(t, "impossible capture", err, ErrIPCClock)
		if ok || pair.host.Fence().Reason != FenceClock {
			t.Fatalf("fence %+v", pair.host.Fence())
		}
	})
	t.Run("capture regression", func(t *testing.T) {
		pair := newPair(t)
		observe(t, pair)
		putSlot64(pair.host.lane(LaneWorkerData).slot(1), 80, 19)
		if _, ok, err := pair.poll(30); err != nil || !ok {
			t.Fatalf("first: %v %v", ok, err)
		}
		_, _, err := pair.poll(30)
		want(t, "regression", err, ErrIPCClock)
	})
	t.Run("sequence duplicate", func(t *testing.T) {
		pair := newPair(t)
		observe(t, pair)
		putSlot64(pair.host.lane(LaneWorkerData).slot(1), 72, 1)
		if _, ok, err := pair.poll(30); err != nil || !ok {
			t.Fatalf("first: %v %v", ok, err)
		}
		delivery, ok, err := pair.poll(30)
		want(t, "duplicate", err, ErrGap)
		if !ok || delivery.Record.CaptureSequence != 1 {
			t.Fatalf("evidence is retained: %+v", delivery)
		}
		fence := pair.host.Fence()
		if fence.Reason != FenceDiscontinuity || fence.Sequence != 1 || fence.CaptureTime != 21 {
			t.Fatalf("fence %+v", fence)
		}
	})
	t.Run("sequence skip", func(t *testing.T) {
		pair := newPair(t)
		observe(t, pair)
		putSlot64(pair.host.lane(LaneWorkerData).slot(1), 72, 3)
		if _, ok, err := pair.poll(30); err != nil || !ok {
			t.Fatalf("first: %v %v", ok, err)
		}
		_, _, err := pair.poll(30)
		want(t, "skip", err, ErrGap)
	})
	t.Run("explained list is bounded", func(t *testing.T) {
		pair := newPair(t)
		host := pair.host
		for sequence := uint64(2); sequence < 5; sequence++ {
			if !host.accountSequence(sequence, true) {
				t.Fatalf("seq %d within the bound", sequence)
			}
		}
		if host.accountSequence(5, true) {
			t.Fatal("explained list grew past its bound")
		}
		if !host.accountSequence(1, false) || host.nextCapture != 5 {
			t.Fatalf("catch-up: next %d", host.nextCapture)
		}
	})
	clocks := []struct {
		name    string
		reading ClockReading
	}{
		{"regression", at(19)},
		{"other domain", ClockReading{Domain: ipcFixtureDomain + 1, Tick: 30}},
		{"zero domain", ClockReading{Tick: 30}},
	}
	for _, clock := range clocks {
		t.Run("worker clock "+clock.name, func(t *testing.T) {
			pair := newPair(t)
			if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(20)); err != nil {
				t.Fatal(err)
			}
			_, err := pair.worker.ObserveRaw(fixtureRaw(2), clock.reading)
			want(t, "observe", err, ErrIPCClock)
			delivery, ok, err := pair.poll(40)
			if err != nil || !ok || delivery.Record.Kind != RecordGap ||
				delivery.Record.CaptureTime != 20 || delivery.Record.CaptureSequence != 2 {
				t.Fatalf("gap uses the last trusted tick: %+v %v %v", delivery, ok, err)
			}
		})
		t.Run("host clock "+clock.name, func(t *testing.T) {
			pair := newPair(t)
			if _, _, err := pair.poll(20); err != nil {
				t.Fatal(err)
			}
			_, _, err := pair.host.Poll(nil, clock.reading)
			want(t, "poll", err, ErrIPCClock)
			if pair.host.Fence().Reason != FenceClock {
				t.Fatalf("fence %+v", pair.host.Fence())
			}
		})
	}
	t.Run("zero tick", func(t *testing.T) {
		pair := newPair(t)
		_, err := pair.worker.ObserveRaw(fixtureRaw(1), at(0))
		want(t, "worker", err, ErrIPCClock)
		_, _, err = pair.host.Poll(nil, at(0))
		want(t, "host", err, ErrIPCClock)
	})
}

// refVariants changes one plan identity field at a time.
func refVariants() []struct {
	name   string
	mutate func(*PlanRef)
} {
	return []struct {
		name   string
		mutate func(*PlanRef)
	}{
		{"subscriber", func(r *PlanRef) { r.Key.Subscriber++ }},
		{"address", func(r *PlanRef) { r.Key.Address = 0x26 }},
		{"epoch", func(r *PlanRef) { r.Key.Epoch++ }},
		{"worker", func(r *PlanRef) { r.Worker++ }},
		{"stream", func(r *PlanRef) { r.Stream++ }},
		{"plan", func(r *PlanRef) { r.Plan++ }},
		{"plan expiry", func(r *PlanRef) { r.PlanExpiry++ }},
	}
}

func TestAA24CommandIdentityVariesEachFieldAtTheOutputBoundary(t *testing.T) {
	base := fixtureCommand(1, 1)
	for _, variant := range refVariants() {
		t.Run("ref "+variant.name, func(t *testing.T) {
			pair := newPair(t)
			pair.publish(1, 10)
			identity := pair.admit(1, 1, 10)
			want(t, "positive control", pair.worker.AdmitOutput(identity, at(11)), nil)
			changed := identity
			variant.mutate(&changed.Ref)
			want(t, variant.name, pair.worker.AdmitOutput(changed, at(11)), ErrStale)
			want(t, "complete", pair.worker.Complete(changed, StatusAccepted, at(11)), ErrMismatch)
			want(t, "original stays admitted", pair.worker.AdmitOutput(identity, at(11)), nil)
		})
	}
	commands := []struct {
		name   string
		mutate func(*CommandIdentity)
	}{
		{"transaction", func(i *CommandIdentity) { i.Transaction++ }},
		{"owner token", func(i *CommandIdentity) { i.OwnerToken++ }},
		{"owner expiry", func(i *CommandIdentity) { i.OwnerExpiry++ }},
		{"request deadline", func(i *CommandIdentity) { i.RequestDeadline++ }},
	}
	for _, variant := range commands {
		t.Run("command "+variant.name, func(t *testing.T) {
			pair := newPair(t)
			pair.publish(1, 10)
			pair.admit(1, 1, 10)
			changed := base
			variant.mutate(&changed)
			want(t, variant.name, pair.worker.AdmitOutput(changed, at(11)), ErrMismatch)
			want(t, "complete", pair.worker.Complete(changed, StatusAccepted, at(11)), ErrMismatch)
			want(t, "positive control", pair.worker.AdmitOutput(base, at(11)), nil)
		})
	}
}

func TestAA24DeadlinesAreExclusiveForEachField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CommandIdentity)
	}{
		{"owner expiry", func(i *CommandIdentity) { i.OwnerExpiry = 50 }},
		{"request deadline", func(i *CommandIdentity) { i.RequestDeadline = 50 }},
		{"plan expiry", func(i *CommandIdentity) { i.Ref.PlanExpiry = 50 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identity := fixtureCommand(1, 1)
			tc.mutate(&identity)
			pair := newPair(t)
			arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
			want(t, "publish", pair.host.PublishPlan(identity.Ref, arenaStart, 8, at(10)), nil)
			want(t, "poll", pair.worker.PollControl(at(10)), nil)
			want(t, "send", pair.host.SendCommand(identity, nil, at(10)), nil)
			_, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(49))
			if err != nil || !ok {
				t.Fatalf("one tick before: %v %v", ok, err)
			}
			want(t, "one tick before output", pair.worker.AdmitOutput(identity, at(49)), nil)
			want(t, "equality is expired", pair.worker.AdmitOutput(identity, at(50)), ErrIPCExpired)
			want(t, "expiry ends selection", pair.worker.Complete(identity, StatusRetired, at(50)), ErrMismatch)
			second := identity
			second.OwnerToken = 2
			want(t, "host refuses expired", pair.host.SendCommand(second, nil, at(50)), ErrIPCExpired)
		})
		t.Run(tc.name+" at worker dequeue", func(t *testing.T) {
			identity := fixtureCommand(1, 1)
			tc.mutate(&identity)
			pair := newPair(t)
			arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
			want(t, "publish", pair.host.PublishPlan(identity.Ref, arenaStart, 8, at(10)), nil)
			want(t, "poll", pair.worker.PollControl(at(10)), nil)
			want(t, "send", pair.host.SendCommand(identity, nil, at(10)), nil)
			_, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(50))
			want(t, "stale in the queue", err, ErrIPCExpired)
			if ok || pair.worker.LaneStats(LaneHostData).Dropped != 1 {
				t.Fatalf("expired command must drop: %v", ok)
			}
		})
	}
}

func TestAA24StaleCommandsAreDroppedNotReplayed(t *testing.T) {
	inject := func(t *testing.T, pair *pair, identity CommandIdentity) {
		t.Helper()
		record := Record{Kind: RecordCommand}
		record.applyIdentity(identity)
		want(t, "inject", pair.host.lane(LaneHostData).write(&record, 0), nil)
	}
	for _, variant := range refVariants() {
		t.Run(variant.name, func(t *testing.T) {
			pair := newPair(t)
			pair.publish(1, 10)
			changed := fixtureCommand(1, 1)
			variant.mutate(&changed.Ref)
			want(t, "host refuses", pair.host.SendCommand(changed, nil, at(11)), ErrStale)
			inject(t, pair, changed)
			_, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(11))
			want(t, "worker drops", err, ErrStale)
			if ok || !pair.worker.OutputOpen() || pair.worker.LaneStats(LaneHostData).Dropped != 1 {
				t.Fatalf("drop must not fence: %v", ok)
			}
			pair.admit(1, 2, 12)
		})
	}
	t.Run("token floor", func(t *testing.T) {
		pair := newPair(t)
		pair.publish(1, 10)
		identity := pair.admit(1, 5, 10)
		want(t, "complete", pair.worker.Complete(identity, StatusAccepted, at(11)), nil)
		want(t, "host reuse", pair.host.SendCommand(identity, nil, at(11)), ErrStale)
		want(t, "host older", pair.host.SendCommand(fixtureCommand(1, 4), nil, at(11)), ErrStale)
		inject(t, pair, identity)
		_, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(12))
		want(t, "replayed token", err, ErrStale)
		if ok {
			t.Fatal("replay admitted")
		}
	})
	t.Run("late completion after a fence is stale", func(t *testing.T) {
		pair := newPair(t)
		pair.publish(1, 10)
		identity := pair.admit(1, 1, 10)
		pair.host.ObserveWorkerTerminated()
		want(t, "complete", pair.worker.Complete(identity, StatusAccepted, at(11)), nil)
		_, ok, err := pair.poll(12)
		want(t, "cancelled command", err, ErrStale)
		if ok {
			t.Fatal("late completion delivered as current")
		}
	})
	t.Run("completion varied by each field", func(t *testing.T) {
		variants := []func(*CommandIdentity){
			func(i *CommandIdentity) { i.Transaction++ },
			func(i *CommandIdentity) { i.OwnerToken++ },
			func(i *CommandIdentity) { i.OwnerExpiry++ },
			func(i *CommandIdentity) { i.RequestDeadline++ },
			func(i *CommandIdentity) { i.Ref.Key.Subscriber++ },
			func(i *CommandIdentity) { i.Ref.Key.Address = 0x26 },
			func(i *CommandIdentity) { i.Ref.Key.Epoch++ },
			func(i *CommandIdentity) { i.Ref.Plan++ },
			func(i *CommandIdentity) { i.Ref.PlanExpiry++ },
		}
		for _, variant := range variants {
			pair := newPair(t)
			pair.publish(1, 10)
			identity := pair.admit(1, 1, 10)
			changed := identity
			variant(&changed)
			pair.worker.active = changed
			want(t, "complete", pair.worker.Complete(changed, StatusAccepted, at(11)), nil)
			_, ok, err := pair.poll(12)
			want(t, "host exact match", err, ErrStale)
			if ok || len(pair.host.outstanding) != 1 {
				t.Fatalf("outstanding %d", len(pair.host.outstanding))
			}
		}
	})
}

func TestAA24RecordsFromAnotherWorkerOrStreamAreDropped(t *testing.T) {
	identities := []struct {
		name   string
		worker WorkerID
		stream StreamGeneration
	}{
		{"worker", ipcFixtureWorker + 1, ipcFixtureStream},
		{"stream", ipcFixtureWorker, ipcFixtureStream + 1},
	}
	for _, identity := range identities {
		t.Run(identity.name, func(t *testing.T) {
			pair := newPair(t)
			stop := Record{Kind: RecordStop, Worker: identity.worker, Stream: identity.stream}
			want(t, "stale stop", pair.host.lane(LaneHostControl).write(&stop, 0), nil)
			want(t, "poll", pair.worker.PollControl(at(10)), nil)
			if !pair.worker.OutputOpen() || pair.worker.LaneStats(LaneHostControl).Dropped != 1 {
				t.Fatal("stale stop closed the worker")
			}
			gap := Record{Kind: RecordGap, Worker: identity.worker, Stream: identity.stream, CaptureSequence: 1, CaptureTime: 1}
			want(t, "stale gap", pair.worker.lane(LaneWorkerControl).write(&gap, 0), nil)
			_, ok, err := pair.poll(11)
			want(t, "gap dropped", err, ErrStale)
			if ok || !pair.host.OutputOpen() {
				t.Fatal("stale gap fenced the host")
			}
			raw := Record{Kind: RecordRaw, Worker: identity.worker, Stream: identity.stream, Transaction: 1, Direction: DirectionReceive, CaptureSequence: 1, CaptureTime: 1}
			want(t, "stale raw", pair.worker.lane(LaneWorkerData).write(&raw, 0), nil)
			_, ok, err = pair.poll(12)
			want(t, "raw dropped", err, ErrStale)
			if ok || pair.host.nextCapture != 1 {
				t.Fatal("stale raw advanced continuity")
			}
		})
	}
}

func TestAA24RevocationAcknowledgementIsExact(t *testing.T) {
	for _, variant := range refVariants() {
		t.Run(variant.name, func(t *testing.T) {
			pair := newPair(t)
			ref := pair.publish(1, 10)
			changed := ref
			variant.mutate(&changed)
			want(t, "host refuses a varied revocation", pair.host.RevokePlan(changed, at(11)), ErrStale)
			revoke := Record{Kind: RecordRevoke}
			revoke.applyRef(changed)
			want(t, "inject", pair.host.lane(LaneHostControl).write(&revoke, 1), nil)
			want(t, "worker poll", pair.worker.PollControl(at(11)), nil)
			if pending := pair.worker.PendingRevocations(nil); len(pending) != 0 {
				t.Fatalf("varied revocation fenced a plan: %+v", pending)
			}
			want(t, "revoke", pair.host.RevokePlan(ref, at(12)), nil)
			want(t, "worker poll", pair.worker.PollControl(at(12)), nil)
			want(t, "worker refuses a varied ack", pair.worker.AcknowledgeRevocation(changed, at(12)), ErrStale)
			pair.worker.nextSequence = 2
			stale := Record{Kind: RecordAcknowledge, Status: StatusRetired, CaptureSequence: 1, CaptureTime: 12}
			stale.applyRef(changed)
			want(t, "inject ack", pair.worker.lane(LaneWorkerControl).write(&stale, 1), nil)
			_, ok, err := pair.poll(13)
			want(t, "stale ack", err, ErrStale)
			if plans := pair.host.QuarantinedPlans(nil); ok || len(plans) != 1 {
				t.Fatalf("stale ack retired a plan: %+v", plans)
			}
			want(t, "exact ack", pair.worker.AcknowledgeRevocation(ref, at(14)), nil)
			delivery, ok, err := pair.poll(15)
			if err != nil || !ok || delivery.Record.Kind != RecordAcknowledge {
				t.Fatalf("exact ack: %+v %v %v", delivery, ok, err)
			}
			if plans := pair.host.QuarantinedPlans(nil); len(plans) != 0 {
				t.Fatalf("exact ack kept the plan: %+v", plans)
			}
		})
	}
	t.Run("publication is not acknowledgement", func(t *testing.T) {
		pair := newPair(t)
		ref := pair.publish(1, 10)
		stale := Record{Kind: RecordAcknowledge, Status: StatusRetired, CaptureSequence: 1, CaptureTime: 11}
		stale.applyRef(ref)
		want(t, "inject", pair.worker.lane(LaneWorkerControl).write(&stale, 1), nil)
		_, _, err := pair.poll(11)
		want(t, "ack for a plan never revoked", err, ErrStale)
		if len(pair.host.QuarantinedPlans(nil)) != 1 {
			t.Fatal("plan retired without revocation")
		}
	})
}

func TestAA24PublicationOutputDecisions(t *testing.T) {
	pair := newPair(t)
	arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	pair.publish(2, 10)
	want(t, "plan does not advance", pair.host.PublishPlan(fixtureRef(2), arenaStart, 8, at(11)), ErrStale)
	want(t, "older plan", pair.host.PublishPlan(fixtureRef(1), arenaStart, 8, at(11)), ErrStale)
	for _, variant := range refVariants() {
		if variant.name != "worker" && variant.name != "stream" {
			continue
		}
		changed := fixtureRef(3)
		variant.mutate(&changed)
		want(t, variant.name, pair.host.PublishPlan(changed, arenaStart, 8, at(11)), ErrStale)
	}
	expired := fixtureRef(3)
	expired.PlanExpiry = 11
	want(t, "plan expiry equality", pair.host.PublishPlan(expired, arenaStart, 8, at(11)), ErrIPCExpired)
	want(t, "arena over the build identity", pair.host.PublishPlan(fixtureRef(3), arenaStart-1, 8, at(11)), ErrRecord)
	want(t, "empty arena reference", pair.host.PublishPlan(fixtureRef(3), arenaStart, 0, at(11)), ErrRecord)
	if !pair.host.OutputOpen() || len(pair.host.QuarantinedPlans(nil)) != 1 {
		t.Fatal("refused publications changed state")
	}
	for plan := PlanGeneration(3); plan <= 5; plan++ {
		pair.publish(plan, 12)
	}
	want(t, "bounded table", pair.host.PublishPlan(fixtureRef(6), arenaStart, 8, at(13)), ErrIPCCapacity)

	t.Run("worker refuses what its bounded table cannot hold", func(t *testing.T) {
		pair := newPair(t)
		record := Record{Kind: RecordPublish, ArenaOffset: arenaStart, PlanBytes: 8}
		record.applyRef(fixtureRef(9))
		lane := pair.host.lane(LaneHostControl)
		want(t, "first", lane.write(&record, 1), nil)
		want(t, "poll", pair.worker.PollControl(at(10)), nil)
		want(t, "stale generation", lane.write(&record, 1), nil)
		want(t, "poll", pair.worker.PollControl(at(10)), nil)
		if pair.worker.PublishRefused() != 1 || len(pair.worker.adopted) != 1 {
			t.Fatalf("refused %d adopted %d", pair.worker.PublishRefused(), len(pair.worker.adopted))
		}
		record.applyRef(fixtureRef(10))
		record.PlanExpiry = 10
		want(t, "expired", lane.write(&record, 1), nil)
		want(t, "poll", pair.worker.PollControl(at(10)), nil)
		if pair.worker.PublishRefused() != 2 {
			t.Fatalf("refused %d", pair.worker.PublishRefused())
		}
		pair.worker.adopted = pair.worker.adopted[:cap(pair.worker.adopted)]
		record.applyRef(fixtureRef(11))
		want(t, "over capacity", lane.write(&record, 1), nil)
		want(t, "poll", pair.worker.PollControl(at(10)), nil)
		if pair.worker.PublishRefused() != 3 {
			t.Fatalf("refused %d", pair.worker.PublishRefused())
		}
	})
	t.Run("outstanding commands are bounded", func(t *testing.T) {
		pair := newPair(t)
		pair.publish(1, 10)
		for len(pair.host.outstanding) < cap(pair.host.outstanding) {
			pair.host.outstanding = append(pair.host.outstanding, fixtureCommand(1, 1))
		}
		want(t, "table full", pair.host.SendCommand(fixtureCommand(1, 1), nil, at(10)), ErrIPCCapacity)
	})
}

func TestAA24HostControlSaturationFencesAndKeepsStopRoom(t *testing.T) {
	pair := newPair(t)
	ref := pair.publish(1, 10)
	identity := pair.admit(1, 1, 10)
	arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	want(t, "second", pair.host.PublishPlan(fixtureRef(2), arenaStart, 8, at(11)), nil)
	want(t, "third", pair.host.PublishPlan(fixtureRef(3), arenaStart, 8, at(11)), nil)
	want(t, "revoke hits the reserved slot", pair.host.RevokePlan(ref, at(11)), ErrControlFull)
	fence := pair.host.Fence()
	if fence.Reason != FenceControlSaturated {
		t.Fatalf("fence %+v", fence)
	}
	if cancelled := pair.host.CancelledCommands(nil); len(cancelled) != 1 || !cancelled[0].matches(identity) {
		t.Fatalf("cancelled %+v", cancelled)
	}
	want(t, "new host command", pair.host.SendCommand(fixtureCommand(1, 2), nil, at(11)), ErrOutputFenced)
	if plans := pair.host.QuarantinedPlans(nil); len(plans) != 3 {
		t.Fatalf("predecessor must stay quarantined: %+v", plans)
	}
	want(t, "stop fits", pair.host.AcknowledgeGap(0, at(12)), nil)
	want(t, "worker drains", pair.worker.PollControl(at(13)), nil)
	if pair.worker.Fence().Reason != FenceStop {
		t.Fatalf("worker fence %+v", pair.worker.Fence())
	}
	want(t, "retried revoke beside the fence", pair.host.RevokePlan(ref, at(14)), nil)
	want(t, "worker poll", pair.worker.PollControl(at(14)), nil)
	want(t, "ack on a fenced stream", pair.worker.AcknowledgeRevocation(ref, at(15)), nil)
	if _, ok, err := pair.poll(16); !ok || err != nil {
		t.Fatalf("host poll: %v %v", ok, err)
	}
	if plans := pair.host.QuarantinedPlans(nil); len(plans) != 2 {
		t.Fatalf("plans %+v", plans)
	}
}

func TestAA24ControlServiceDeadlineUsesInjectedTime(t *testing.T) {
	t.Run("host control unread", func(t *testing.T) {
		pair := newPair(t)
		arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
		want(t, "publish", pair.host.PublishPlan(fixtureRef(1), arenaStart, 8, at(10)), nil)
		if _, _, err := pair.poll(10 + fixtureBudget - 1); err != nil || !pair.host.OutputOpen() {
			t.Fatalf("before the budget: %v %+v", err, pair.host.Fence())
		}
		if _, _, err := pair.poll(10 + fixtureBudget); err != nil {
			t.Fatal(err)
		}
		if pair.host.Fence().Reason != FenceControlDeadline {
			t.Fatalf("equality is expired: %+v", pair.host.Fence())
		}
	})
	t.Run("consumer progress restarts the budget", func(t *testing.T) {
		pair := newPair(t)
		arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
		want(t, "publish", pair.host.PublishPlan(fixtureRef(1), arenaStart, 8, at(10)), nil)
		want(t, "worker poll", pair.worker.PollControl(at(50)), nil)
		want(t, "publish", pair.host.PublishPlan(fixtureRef(2), arenaStart, 8, at(60)), nil)
		if _, _, err := pair.poll(159); err != nil || !pair.host.OutputOpen() {
			t.Fatalf("progress ignored: %v %+v", err, pair.host.Fence())
		}
		if _, _, err := pair.poll(160); err != nil || pair.host.OutputOpen() {
			t.Fatalf("budget from last progress: %v", err)
		}
	})
	t.Run("partial consumer progress restarts the budget", func(t *testing.T) {
		pair := newPair(t)
		refs := []PlanRef{pair.publish(1, 10), pair.publish(2, 10)}
		for _, ref := range refs {
			want(t, "revoke", pair.host.RevokePlan(ref, at(10)), nil)
			want(t, "poll", pair.worker.PollControl(at(10)), nil)
			want(t, "ack", pair.worker.AcknowledgeRevocation(ref, at(10)), nil)
		}
		if _, ok, err := pair.poll(60); !ok || err != nil {
			t.Fatalf("host poll: %v %v", ok, err)
		}
		want(t, "progress seen", pair.worker.PollControl(at(60)), nil)
		want(t, "past the first budget", pair.worker.PollControl(at(60+fixtureBudget-1)), nil)
		if !pair.worker.OutputOpen() {
			t.Fatalf("progress ignored: %+v", pair.worker.Fence())
		}
		want(t, "budget from last progress", pair.worker.PollControl(at(60+fixtureBudget)), nil)
		if pair.worker.Fence().Reason != FenceControlDeadline {
			t.Fatalf("fence %+v", pair.worker.Fence())
		}
	})
	t.Run("worker control unread closes the worker without host delivery", func(t *testing.T) {
		pair := newPair(t)
		ref := pair.publish(1, 10)
		want(t, "revoke", pair.host.RevokePlan(ref, at(10)), nil)
		want(t, "poll", pair.worker.PollControl(at(10)), nil)
		want(t, "ack", pair.worker.AcknowledgeRevocation(ref, at(10)), nil)
		want(t, "before", pair.worker.PollControl(at(10+fixtureBudget-1)), nil)
		if !pair.worker.OutputOpen() {
			t.Fatal("closed before the budget")
		}
		want(t, "at", pair.worker.PollControl(at(10+fixtureBudget)), nil)
		if pair.worker.Fence().Reason != FenceControlDeadline {
			t.Fatalf("fence %+v", pair.worker.Fence())
		}
	})
}

func TestAA24GapMustBeAcknowledgedBeforeResynchronization(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	stale := fixtureCommand(1, 1)
	want(t, "queued command", pair.host.SendCommand(stale, []byte{1}, at(10)), nil)
	next := ipcFixtureStream + 1

	_, err := pair.host.ResyncExpected(next)
	want(t, "open host", err, ErrNotFenced)
	want(t, "no fence to acknowledge", pair.host.AcknowledgeGap(0, at(10)), ErrNotFenced)
	_, err = pair.worker.ResyncExpected(next, pair.host.planFloor, pair.host.tokenFloor)
	want(t, "open worker", err, ErrNotAcknowledged)

	for octet := byte(0); octet < 2; octet++ {
		if _, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(20)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pair.worker.ObserveRaw(fixtureRaw(2), at(20)); !errorstest.Is(err, ErrGap) {
		t.Fatalf("symbol past the lane: got %v, want %v", err, ErrGap)
	}
	_, err = pair.worker.ResyncExpected(next, pair.host.planFloor, pair.host.tokenFloor)
	want(t, "gap unacknowledged at worker", err, ErrNotAcknowledged)
	if _, ok, err := pair.poll(21); !ok || err != nil {
		t.Fatalf("gap poll: %v %v", ok, err)
	}
	_, err = pair.host.ResyncExpected(next)
	want(t, "stop not sent", err, ErrNotAcknowledged)
	want(t, "wrong gap sequence", pair.host.AcknowledgeGap(2, at(22)), ErrStale)
	_, err = pair.host.ResyncExpected(next)
	want(t, "wrong acknowledgement", err, ErrNotAcknowledged)

	want(t, "exact acknowledgement", pair.host.AcknowledgeGap(3, at(22)), nil)
	want(t, "repeat is harmless", pair.host.AcknowledgeGap(3, at(22)), nil)
	if pair.host.lane(LaneHostControl).localProducer != 2 {
		t.Fatal("repeat queued a second stop")
	}
	_, err = pair.host.ResyncExpected(next)
	want(t, "stop unread", err, ErrNotAcknowledged)
	want(t, "worker reads stop", pair.worker.PollControl(at(23)), nil)

	generations := []StreamGeneration{ipcFixtureStream, ipcFixtureStream - 1, 0}
	for _, generation := range generations {
		_, err = pair.host.ResyncExpected(generation)
		want(t, "host generation must advance", err, ErrStale)
		_, err = pair.worker.ResyncExpected(generation, pair.host.planFloor, pair.host.tokenFloor)
		want(t, "worker generation must advance", err, ErrStale)
	}
	hostNext, err := pair.host.ResyncExpected(next)
	if err != nil {
		t.Fatal(err)
	}
	workerNext, err := pair.worker.ResyncExpected(next, pair.host.planFloor, pair.host.tokenFloor)
	if err != nil {
		t.Fatal(err)
	}
	if hostNext.Binding != workerNext.Binding || hostNext.Binding.Stream != next ||
		hostNext.PlanFloor != 1 || hostNext.TokenFloor != 1 {
		t.Fatalf("successors: %+v / %+v", hostNext, workerNext)
	}

	oldMem := append([]byte(nil), pair.mem...)
	fresh := fixtureMemory(fixtureBytes())
	if _, err := AttachHost(fresh, hostNext); err == nil {
		t.Fatal("an uninitialized replacement was admitted")
	}
	if pair.host.Fence().Reason != FenceWorkerGap || string(oldMem) != string(pair.mem) {
		t.Fatal("a refused replacement changed the previous mapping's state")
	}
	binding := fixtureBinding()
	binding.Stream = next
	expected, _, err := InitMapping(fresh, binding, fixtureConfig, ipcFixtureLimits)
	if err != nil {
		t.Fatal(err)
	}
	expected.PlanFloor, expected.TokenFloor = hostNext.PlanFloor, hostNext.TokenFloor
	if _, err := ValidateMapping(fresh, pair.expected); !errorstest.Is(err, ErrIPCIdentity) {
		t.Fatalf("predecessor identity must refuse the successor: %v", err)
	}
	successor := attachPair(t, fresh, expected)
	if delivery, ok, err := successor.worker.NextCommand(make([]byte, 0, 16), at(30)); ok || err != nil {
		t.Fatalf("a queued command replayed: %+v %v", delivery, err)
	}
	arenaStart := successor.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	oldPlan := fixtureRef(1)
	oldPlan.Stream = next
	want(t, "plan generation stays advancing", successor.host.PublishPlan(oldPlan, arenaStart, 8, at(30)), ErrStale)
	newPlan := fixtureRef(2)
	newPlan.Stream = next
	want(t, "new plan", successor.host.PublishPlan(newPlan, arenaStart, 8, at(30)), nil)
	want(t, "worker poll", successor.worker.PollControl(at(30)), nil)
	oldToken := CommandIdentity{Ref: newPlan, Transaction: 1, OwnerToken: 1, OwnerExpiry: fixtureOwner, RequestDeadline: fixtureDeadline}
	want(t, "token stays nonreused", successor.host.SendCommand(oldToken, nil, at(30)), ErrStale)
	sequence, err := successor.worker.ObserveRaw(fixtureRaw(1), at(31))
	if err != nil || sequence != 1 {
		t.Fatalf("fresh stream sequence: %d %v", sequence, err)
	}
}

func TestAA24WorkerStaysFencedUntilItsGapIsConsumed(t *testing.T) {
	pair := newPair(t)
	for octet := byte(0); octet < 2; octet++ {
		if _, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(20)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pair.worker.ObserveRaw(fixtureRaw(2), at(20)); !errorstest.Is(err, ErrGap) {
		t.Fatalf("symbol past the lane: got %v, want %v", err, ErrGap)
	}
	want(t, "host stops without reading the gap", pair.host.RequestStop(at(21)), nil)
	want(t, "worker reads stop", pair.worker.PollControl(at(22)), nil)
	next := ipcFixtureStream + 1
	_, err := pair.worker.ResyncExpected(next, pair.host.planFloor, pair.host.tokenFloor)
	want(t, "worker: gap unread", err, ErrNotAcknowledged)
	_, err = pair.host.ResyncExpected(next)
	want(t, "host: gap unread", err, ErrNotAcknowledged)
	if _, ok, err := pair.poll(23); !ok || err != nil {
		t.Fatalf("gap poll: %v %v", ok, err)
	}
	if pair.host.Fence().Reason != FenceWorkerGap {
		t.Fatalf("gap must replace the local cause: %+v", pair.host.Fence())
	}
	if _, err := pair.worker.ResyncExpected(next, pair.host.planFloor, pair.host.tokenFloor); err != nil {
		t.Fatal(err)
	}
	if _, err := pair.host.ResyncExpected(next); err != nil {
		t.Fatal(err)
	}
}

func TestAA24RestartAdvancesTheWorkerIncarnation(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	identity := pair.admit(1, 1, 10)
	next := ipcFixtureWorker + 1
	_, err := pair.host.RestartExpected(next, 1)
	want(t, "worker not proven gone", err, ErrNotAcknowledged)
	pair.host.ObserveWorkerTerminated()
	if pair.host.OutputOpen() || pair.host.Fence().Reason != FenceWorkerTerminated {
		t.Fatalf("fence %+v", pair.host.Fence())
	}
	if cancelled := pair.host.CancelledCommands(nil); len(cancelled) != 1 || !cancelled[0].matches(identity) {
		t.Fatalf("cancelled %+v", cancelled)
	}
	_, err = pair.host.RestartExpected(ipcFixtureWorker, 1)
	want(t, "same incarnation", err, ErrStale)
	_, err = pair.host.RestartExpected(ipcFixtureWorker-1, 1)
	want(t, "older incarnation", err, ErrStale)
	_, err = pair.host.RestartExpected(next, 0)
	want(t, "zero stream", err, ErrStale)
	expected, err := pair.host.RestartExpected(next, 1)
	if err != nil || expected.Binding.Worker != next || expected.Binding.Stream != 1 || expected.TokenFloor != 1 {
		t.Fatalf("restart expected: %+v %v", expected, err)
	}
	if _, err := AttachWorker(pair.mem, expected); !errorstest.Is(err, ErrIPCIdentity) {
		t.Fatalf("predecessor mapping must refuse the successor identity: %v", err)
	}
	fresh := fixtureMemory(fixtureBytes())
	binding := expected.Binding
	if _, _, err := InitMapping(fresh, binding, fixtureConfig, ipcFixtureLimits); err != nil {
		t.Fatal(err)
	}
	successor := attachPair(t, fresh, expected)
	if _, ok, err := successor.poll(30); ok || err != nil {
		t.Fatalf("no predecessor record survives: %v %v", ok, err)
	}
	want(t, "predecessor identity on the successor", successor.host.SendCommand(identity, nil, at(30)), ErrStale)
}

func TestAA24TeardownDoesNotDrainSaturatedData(t *testing.T) {
	t.Run("stop path", func(t *testing.T) {
		pair := newPair(t)
		for octet := byte(0); octet < 2; octet++ {
			if _, err := pair.worker.ObserveRaw(fixtureRaw(octet), at(10)); err != nil {
				t.Fatal(err)
			}
		}
		want(t, "open worker", pair.worker.Teardown(), ErrNotAcknowledged)
		want(t, "open host", pair.host.Teardown(), ErrNotAcknowledged)
		want(t, "stop", pair.host.RequestStop(at(11)), nil)
		want(t, "stop unread", pair.host.Teardown(), ErrNotAcknowledged)
		want(t, "worker reads stop", pair.worker.PollControl(at(12)), nil)
		want(t, "host teardown", pair.host.Teardown(), nil)
		want(t, "worker teardown", pair.worker.Teardown(), nil)
		want(t, "repeat host", pair.host.Teardown(), nil)
		want(t, "repeat worker", pair.worker.Teardown(), nil)
		ref := fixtureRef(1)
		identity := fixtureCommand(1, 1)
		_, err := pair.worker.ObserveRaw(fixtureRaw(1), at(13))
		want(t, "observe", err, ErrDetached)
		want(t, "poll", pair.worker.PollControl(at(13)), ErrDetached)
		_, _, err = pair.worker.NextCommand(nil, at(13))
		want(t, "next", err, ErrDetached)
		want(t, "admit", pair.worker.AdmitOutput(identity, at(13)), ErrDetached)
		want(t, "complete", pair.worker.Complete(identity, StatusRetired, at(13)), ErrDetached)
		want(t, "ack", pair.worker.AcknowledgeRevocation(ref, at(13)), ErrDetached)
		_, err = pair.worker.ResyncExpected(ipcFixtureStream+1, pair.host.planFloor, pair.host.tokenFloor)
		want(t, "worker resync", err, ErrDetached)
		_, _, err = pair.host.Poll(nil, at(13))
		want(t, "host poll", err, ErrDetached)
		want(t, "publish", pair.host.PublishPlan(ref, 0, 0, at(13)), ErrDetached)
		want(t, "revoke", pair.host.RevokePlan(ref, at(13)), ErrDetached)
		want(t, "send", pair.host.SendCommand(identity, nil, at(13)), ErrDetached)
		want(t, "ack gap", pair.host.AcknowledgeGap(0, at(13)), ErrDetached)
		want(t, "stop", pair.host.RequestStop(at(13)), ErrDetached)
		_, err = pair.host.ResyncExpected(ipcFixtureStream + 1)
		want(t, "host resync", err, ErrDetached)
		_, err = pair.host.RestartExpected(ipcFixtureWorker+1, 1)
		want(t, "host restart", err, ErrDetached)
		if pair.worker.OutputOpen() || pair.host.OutputOpen() {
			t.Fatal("a torn-down endpoint reports open output decisions")
		}
	})
	t.Run("terminated worker", func(t *testing.T) {
		pair := newPair(t)
		pair.host.ObserveWorkerTerminated()
		want(t, "host teardown", pair.host.Teardown(), nil)
	})
}

func TestAA24ExpiredOutstandingCommandsAreRetired(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	want(t, "poll", pair.worker.PollControl(at(10)), nil)
	limit := cap(pair.host.outstanding)
	for index := 0; index < limit; index++ {
		identity := fixtureCommand(1, OwnerToken(index+1))
		identity.RequestDeadline = 12
		pair.host.outstanding = append(pair.host.outstanding, identity)
	}
	want(t, "full while live", pair.host.SendCommand(fixtureCommand(1, OwnerToken(limit+1)), nil, at(11)), ErrIPCCapacity)
	pair.host.lastControlTime = 12
	want(t, "expired retired", pair.host.SendCommand(fixtureCommand(1, OwnerToken(limit+1)), nil, at(12)), nil)
	if count := len(pair.host.outstanding); count != 1 {
		t.Fatalf("outstanding %d", count)
	}
}

func TestAA24RefusedPublicationIsRetiredByRevocation(t *testing.T) {
	pair := newPair(t)
	ref := fixtureRef(1)
	ref.PlanExpiry = 12
	arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	want(t, "publish", pair.host.PublishPlan(ref, arenaStart, 8, at(10)), nil)
	want(t, "worker sees it expired", pair.worker.PollControl(at(12)), nil)
	if pair.worker.PublishRefused() != 1 || len(pair.worker.adopted) != 0 {
		t.Fatalf("refused %d", pair.worker.PublishRefused())
	}
	want(t, "revoke", pair.host.RevokePlan(ref, at(12)), nil)
	want(t, "worker poll", pair.worker.PollControl(at(12)), nil)
	delivery, ok, err := pair.poll(13)
	if err != nil || !ok || delivery.Record.Kind != RecordAcknowledge {
		t.Fatalf("retirement: %+v %v %v", delivery, ok, err)
	}
	if plans := pair.host.QuarantinedPlans(nil); len(plans) != 0 {
		t.Fatalf("refused plan stayed quarantined: %+v", plans)
	}
}

func TestAA24RefusedRetirementSurvivesSaturatedControl(t *testing.T) {
	pair := newPair(t)
	worker := pair.worker
	for plan := PlanGeneration(1); plan <= 2; plan++ {
		tick := Tick(10 * plan)
		ref := pair.publish(plan, tick)
		want(t, "revoke", pair.host.RevokePlan(ref, at(tick+1)), nil)
		want(t, "worker poll", worker.PollControl(at(tick+1)), nil)
		want(t, "ack fills a usable control slot", worker.AcknowledgeRevocation(ref, at(tick+2)), nil)
	}
	refused := fixtureRef(3)
	worker.refusedRefs = append(worker.refusedRefs, refused)
	record := Record{Kind: RecordRevoke}
	record.applyRef(refused)
	want(t, "revoke of the refused plan", pair.host.writeControl(&record), nil)
	want(t, "worker poll on saturated control", worker.PollControl(at(23)), nil)
	if len(worker.refusedRefs) != 1 || !hostControlUnread(t, worker) {
		t.Fatalf("reference and its revocation must stay until acknowledged: refused %d", len(worker.refusedRefs))
	}
	control := worker.lane(LaneWorkerControl)
	before, _, err := control.indices()
	if err != nil {
		t.Fatal(err)
	}
	for tick := Tick(24); tick < 27; tick++ {
		if _, ok, err := pair.poll(tick); !ok || err != nil {
			t.Fatalf("host poll: %v %v", ok, err)
		}
	}
	want(t, "worker retry", worker.PollControl(at(28)), nil)
	if len(worker.refusedRefs) != 0 || len(worker.retireRetry) != 0 {
		t.Fatalf("retired reference lingers: refused %d retry %d", len(worker.refusedRefs), len(worker.retireRetry))
	}
	if after, _, err := control.indices(); err != nil || after != before+1 {
		t.Fatalf("retirement must be written once the room returns: before %d after %d err %v", before, after, err)
	}
}

func TestAA24RevokedActiveCommandsLeaveOutstanding(t *testing.T) {
	pair := newPair(t)
	for cycle := PlanGeneration(1); cycle <= 6; cycle++ {
		tick := Tick(10 * cycle)
		ref := pair.publish(cycle, tick)
		pair.admit(cycle, OwnerToken(cycle), tick+1)
		if len(pair.host.outstanding) != 1 {
			t.Fatalf("cycle %d: outstanding %d", cycle, len(pair.host.outstanding))
		}
		want(t, "revoke", pair.host.RevokePlan(ref, at(tick+2)), nil)
		want(t, "worker poll", pair.worker.PollControl(at(tick+3)), nil)
		want(t, "ack", pair.worker.AcknowledgeRevocation(ref, at(tick+4)), nil)
		if _, ok, err := pair.poll(tick + 5); !ok || err != nil {
			t.Fatalf("host poll: %v %v", ok, err)
		}
		if len(pair.host.outstanding) != 0 {
			t.Fatalf("cycle %d: commands of the retired plan stay outstanding: %d", cycle, len(pair.host.outstanding))
		}
	}
}

func TestAA24StopStillFlowsAfterClockFence(t *testing.T) {
	t.Run("worker clock fence", func(t *testing.T) {
		pair := newPair(t)
		if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(20)); err != nil {
			t.Fatal(err)
		}
		want(t, "regressed worker clock", pair.worker.PollControl(at(19)), ErrIPCClock)
		delivery, ok, err := pair.poll(30)
		if err != nil || !ok || delivery.Record.Kind != RecordGap {
			t.Fatalf("gap: %+v %v %v", delivery, ok, err)
		}
		want(t, "acknowledge", pair.host.AcknowledgeGap(delivery.Record.CaptureSequence, at(31)), nil)
		want(t, "fenced worker drains the stop", pair.worker.PollControl(at(19)), ErrIPCClock)
		if _, err := pair.worker.ResyncExpected(ipcFixtureStream+1, pair.host.planFloor, pair.host.tokenFloor); err != nil {
			t.Fatalf("resync stays blocked: %v", err)
		}
	})
	t.Run("host clock fence", func(t *testing.T) {
		for _, name := range []string{"acknowledge", "request"} {
			pair := newPair(t)
			want(t, name+" poll", pair.worker.PollControl(at(10)), nil)
			if _, _, err := pair.poll(30); err != nil {
				t.Fatal(err)
			}
			_, _, err := pair.poll(29)
			want(t, name+" regressed host clock", err, ErrIPCClock)
			if name == "acknowledge" {
				want(t, "acknowledge on a fenced clock", pair.host.AcknowledgeGap(0, at(29)), nil)
			} else {
				want(t, "request on a fenced clock", pair.host.RequestStop(at(29)), nil)
			}
			want(t, name+" worker drains", pair.worker.PollControl(at(40)), nil)
			if pair.worker.Fence().Reason != FenceStop {
				t.Fatalf("%s: worker fence %+v", name, pair.worker.Fence())
			}
		}
	})
}

func TestAA24QueuedCompletionSurvivesExpiryCleanup(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	first := fixtureCommand(1, 1)
	first.RequestDeadline = 20
	want(t, "send", pair.host.SendCommand(first, []byte{1}, at(10)), nil)
	if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(11)); err != nil || !ok {
		t.Fatalf("next: %v %v", ok, err)
	}
	want(t, "complete before the deadline", pair.worker.Complete(first, StatusAccepted, at(12)), nil)
	want(t, "second send at the first deadline", pair.host.SendCommand(fixtureCommand(1, 2), nil, at(20)), nil)
	delivery, ok, err := pair.poll(21)
	if err != nil || !ok || delivery.Record.Kind != RecordCompletion || delivery.Record.OwnerToken != 1 {
		t.Fatalf("queued completion lost: %+v %v %v", delivery, ok, err)
	}
	// Positive control: the same completion is not accepted twice.
	replay := Record{Kind: RecordCompletion, Status: StatusAccepted, CaptureSequence: 2, CaptureTime: 22}
	replay.applyIdentity(first)
	replay.Worker, replay.Stream = ipcFixtureWorker, ipcFixtureStream
	want(t, "inject replay", pair.worker.lane(LaneWorkerData).write(&replay, 0), nil)
	_, _, err = pair.poll(23)
	want(t, "replay is stale", err, ErrStale)
}

func TestAA24QueuedCompletionSurvivesQueuedRetirement(t *testing.T) {
	pair := newPair(t)
	ref := pair.publish(1, 10)
	identity := pair.admit(1, 1, 10)
	want(t, "complete", pair.worker.Complete(identity, StatusAccepted, at(11)), nil)
	want(t, "revoke", pair.host.RevokePlan(ref, at(12)), nil)
	want(t, "worker poll", pair.worker.PollControl(at(13)), nil)
	want(t, "retire", pair.worker.AcknowledgeRevocation(ref, at(14)), nil)
	first, ok, err := pair.poll(15)
	if err != nil || !ok || first.Record.Kind != RecordAcknowledge {
		t.Fatalf("control first: %+v %v %v", first, ok, err)
	}
	second, ok, err := pair.poll(16)
	if err != nil || !ok || second.Record.Kind != RecordCompletion {
		t.Fatalf("queued completion lost: %+v %v %v", second, ok, err)
	}
	if count := len(pair.host.outstanding); count != 0 {
		t.Fatalf("outstanding %d", count)
	}
}

func TestAA24WorkerTeardownRequiresTheStopHandshake(t *testing.T) {
	pair := newPair(t)
	pair.worker.raiseLocalGap(FenceCorruption)
	if pair.worker.OutputOpen() {
		t.Fatal("local fault must close output")
	}
	want(t, "fenced but no stop", pair.worker.Teardown(), ErrNotAcknowledged)
	want(t, "stop", pair.host.RequestStop(at(11)), nil)
	want(t, "stop read, gap unread", pair.worker.PollControl(at(12)), nil)
	want(t, "gap not consumed", pair.worker.Teardown(), ErrNotAcknowledged)
	if _, ok, err := pair.poll(13); err != nil || !ok {
		t.Fatalf("host reads gap: %v %v", ok, err)
	}
	want(t, "acknowledged", pair.worker.Teardown(), nil)
}

// lingerPair builds a pair whose outstanding capacity is one, so expiry of each
// command moves its identity into the lingering list.
func lingerPair(t *testing.T) *pair {
	t.Helper()
	mem := fixtureMemory(fixtureBytes())
	limits := ipcFixtureLimits
	limits.MaxOutstanding = 1
	expected, _, err := InitMapping(mem, fixtureBinding(), fixtureConfig, limits)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	return attachPair(t, mem, expected)
}

// lingerCommand sends and completes one command; its request deadline is tick+10.
func (pair *pair) lingerCommand(token OwnerToken, tick Tick) {
	pair.t.Helper()
	identity := fixtureCommand(1, token)
	identity.RequestDeadline = tick + 10
	want(pair.t, "send", pair.host.SendCommand(identity, nil, at(tick)), nil)
	if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(tick)); err != nil || !ok {
		pair.t.Fatalf("next: %v %v", ok, err)
	}
	want(pair.t, "complete", pair.worker.Complete(identity, StatusAccepted, at(tick+1)), nil)
}

// Catches an expiry list bounded by outstanding capacity: the second expiry would
// evict the first identity while its completion is still queued.
func TestAA24LingeringIdentitiesSurviveUnpolledExpiries(t *testing.T) {
	pair := lingerPair(t)
	pair.publish(1, 10)
	pair.lingerCommand(1, 10)
	pair.lingerCommand(2, 20)
	want(t, "third send expires the second", pair.host.SendCommand(fixtureCommand(1, 3), nil, at(30)), nil)
	for _, token := range []OwnerToken{1, 2} {
		delivery, ok, err := pair.poll(31)
		if err != nil || !ok || delivery.Record.Kind != RecordCompletion || delivery.Record.OwnerToken != token {
			t.Fatalf("token %d: %+v %v %v", token, delivery, ok, err)
		}
	}
}

// Catches a fence that clears the lingering list: the queued completion of an
// expired command must stay matchable while old data drains.
func TestAA24FenceKeepsQueuedCompletionIdentities(t *testing.T) {
	pair := lingerPair(t)
	pair.publish(1, 10)
	pair.lingerCommand(1, 10)
	want(t, "second send expires the first", pair.host.SendCommand(fixtureCommand(1, 2), nil, at(20)), nil)
	want(t, "fence", pair.host.RequestStop(at(22)), nil)
	delivery, ok, err := pair.poll(23)
	if err != nil || !ok || delivery.Record.OwnerToken != 1 {
		t.Fatalf("expired completion after the fence: %+v %v %v", delivery, ok, err)
	}
}

// Catches a fence that leaves an expiry-lingering identity non-strict: a completion
// posted after the stop request would then be accepted as pre-fence evidence.
func TestAA24FenceTightensExistingLingeringIdentities(t *testing.T) {
	pair := lingerPair(t)
	pair.publish(1, 10)
	first := fixtureCommand(1, 1)
	first.RequestDeadline = 20
	want(t, "send", pair.host.SendCommand(first, nil, at(10)), nil)
	if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(11)); err != nil || !ok {
		t.Fatalf("next: %v %v", ok, err)
	}
	want(t, "second send expires the first", pair.host.SendCommand(fixtureCommand(1, 2), nil, at(20)), nil)
	want(t, "stop fences", pair.host.RequestStop(at(22)), nil)
	late := Record{Kind: RecordCompletion, Status: StatusAccepted, CaptureSequence: 1, CaptureTime: 23}
	late.applyIdentity(first)
	late.Worker, late.Stream = ipcFixtureWorker, ipcFixtureStream
	want(t, "post-fence completion", pair.worker.lane(LaneWorkerData).write(&late, 0), nil)
	_, _, err := pair.poll(24)
	want(t, "post-fence completion is stale", err, ErrStale)
}

// Catches a fence that keeps the expiry-time horizon of a lingering identity: a valid
// completion queued before the fence would be discarded as stale.
func TestAA24FenceAdoptsHeadForExpiryLingeringIdentity(t *testing.T) {
	pair := lingerPair(t)
	pair.publish(1, 10)
	first := fixtureCommand(1, 1)
	first.RequestDeadline = 20
	want(t, "send", pair.host.SendCommand(first, nil, at(10)), nil)
	if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(11)); err != nil || !ok {
		t.Fatalf("next: %v %v", ok, err)
	}
	want(t, "second send expires the first at horizon zero", pair.host.SendCommand(fixtureCommand(1, 2), nil, at(20)), nil)
	want(t, "descheduled worker completes", pair.worker.Complete(first, StatusAccepted, at(15)), nil)
	want(t, "stop before polling", pair.host.RequestStop(at(22)), nil)
	delivery, ok, err := pair.poll(24)
	if err != nil || !ok || delivery.Record.Kind != RecordCompletion || delivery.Record.OwnerToken != 1 {
		t.Fatalf("pre-fence completion: %+v %v %v", delivery, ok, err)
	}
}

// Catches teardown on stop consumption alone: the worker's gap stays unread, so the
// worker could never settle its own teardown.
func TestAA24HostTeardownWaitsForTheWorkerGapToBeRead(t *testing.T) {
	pair := newPair(t)
	pair.worker.raiseLocalGap(FenceCorruption)
	want(t, "stop", pair.host.RequestStop(at(11)), nil)
	want(t, "worker reads stop", pair.worker.PollControl(at(12)), nil)
	want(t, "gap unread", pair.host.Teardown(), ErrNotAcknowledged)
	want(t, "worker teardown blocked", pair.worker.Teardown(), ErrNotAcknowledged)
	delivery, ok, err := pair.poll(13)
	if err != nil || !ok || delivery.Record.Kind != RecordGap {
		t.Fatalf("gap: %+v %v %v", delivery, ok, err)
	}
	want(t, "worker teardown", pair.worker.Teardown(), nil)
	want(t, "host teardown", pair.host.Teardown(), nil)
}

// Catches a poll that returns on the clock fence without draining control: the
// worker's gap would stay unread and graceful resynchronization could not settle.
func TestAA24ClockFencedPollDrainsControl(t *testing.T) {
	pair := newPair(t)
	if _, _, err := pair.poll(30); err != nil {
		t.Fatal(err)
	}
	pair.worker.raiseLocalGap(FenceCorruption)
	delivery, ok, err := pair.poll(29)
	want(t, "regressed host clock", err, ErrIPCClock)
	if !ok || delivery.Record.Kind != RecordGap || delivery.DequeueTime != 0 {
		t.Fatalf("gap drained on the fenced clock: %+v %v", delivery, ok)
	}
	want(t, "stop", pair.host.RequestStop(at(29)), nil)
	want(t, "worker reads stop", pair.worker.PollControl(at(40)), nil)
	if _, err := pair.host.ResyncExpected(ipcFixtureStream + 1); err != nil {
		t.Fatalf("resync after the fenced drain: %v", err)
	}
}

// lingerExpiring sends a command with a short deadline and lets the worker drop it
// at dequeue after the deadline, so no completion exists for it.
func (pair *pair) lingerExpiring(token OwnerToken, tick Tick) {
	pair.t.Helper()
	identity := fixtureCommand(1, token)
	identity.RequestDeadline = tick + 10
	want(pair.t, "send", pair.host.SendCommand(identity, nil, at(tick)), nil)
	_, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(tick+11))
	want(pair.t, "worker dequeue", err, ErrIPCExpired)
	if ok {
		pair.t.Fatal("an expired command was admitted")
	}
}

// Catches a lingering list that evicts its oldest identity when commands expire at
// worker dequeue without completions: the unread completion of the first command
// would be consumed as stale.
func TestAA24LingeringKeepsQueuedCompletionBesideSilentExpiries(t *testing.T) {
	pair := lingerPair(t)
	pair.publish(1, 10)
	pair.lingerCommand(1, 10)
	pair.lingerExpiring(2, 20)
	pair.lingerExpiring(3, 40)
	want(t, "no room to retire the third", pair.host.SendCommand(fixtureCommand(1, 4), nil, at(60)), ErrIPCCapacity)
	delivery, ok, err := pair.poll(61)
	if err != nil || !ok || delivery.Record.Kind != RecordCompletion || delivery.Record.OwnerToken != 1 {
		t.Fatalf("queued completion: %+v %v %v", delivery, ok, err)
	}
	want(t, "send after the data tail passed", pair.host.SendCommand(fixtureCommand(1, 4), nil, at(62)), nil)
}

// Catches a poll that reads another control record while the early-control list is
// full and older data is visible: acknowledgements that overtake the data one at a
// time would breach the reorder bound and fence a valid stream.
func TestAA24PollDrainsVisibleDataBeforeTheEarlyControlListOverflows(t *testing.T) {
	pair := newPair(t)
	refs := []PlanRef{pair.publish(1, 10), pair.publish(2, 10), pair.publish(3, 10), pair.publish(4, 10)}
	for _, ref := range refs {
		want(t, "revoke", pair.host.RevokePlan(ref, at(11)), nil)
		want(t, "worker poll", pair.worker.PollControl(at(11)), nil)
	}
	if _, err := pair.worker.ObserveRaw(fixtureRaw(1), at(12)); err != nil {
		t.Fatal(err)
	}
	for index, ref := range refs {
		want(t, "ack", pair.worker.AcknowledgeRevocation(ref, at(13)), nil)
		delivery, ok, err := pair.poll(14)
		if index == 3 {
			if err != nil || !ok || delivery.Record.Kind != RecordRaw {
				t.Fatalf("older raw drained first: %+v %v %v", delivery, ok, err)
			}
			delivery, ok, err = pair.poll(14)
		}
		if err != nil || !ok || delivery.Record.Kind != RecordAcknowledge {
			t.Fatalf("ack %d: %+v %v %v", index, delivery, ok, err)
		}
	}
	if !pair.host.OutputOpen() || pair.host.nextCapture != 6 {
		t.Fatalf("valid stream fenced: %+v next %d", pair.host.Fence(), pair.host.nextCapture)
	}
}

// Catches a worker successor whose floors regress below identities the host already
// assigned but the worker never drained.
func TestAA24WorkerResyncKeepsHostAssignedFloors(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	pair.worker.raiseLocalGap(FenceCorruption)
	want(t, "send", pair.host.SendCommand(fixtureCommand(1, 7), nil, at(11)), nil)
	want(t, "publish second", pair.host.PublishPlan(fixtureRef(2), pair.host.lanes[0].arenaLayout.ArenaOffset+buildBytes+8, 8, at(11)), nil)
	want(t, "stop", pair.host.RequestStop(at(12)), nil)
	want(t, "worker reads stop", pair.worker.PollControl(at(13)), nil)
	if _, ok, err := pair.poll(14); err != nil || !ok {
		t.Fatalf("host reads gap: %v %v", ok, err)
	}
	next := ipcFixtureStream + 1
	hostNext, err := pair.host.ResyncExpected(next)
	if err != nil {
		t.Fatal(err)
	}
	workerNext, err := pair.worker.ResyncExpected(next, pair.host.planFloor, pair.host.tokenFloor)
	if err != nil {
		t.Fatal(err)
	}
	if hostNext.PlanFloor != 2 || hostNext.TokenFloor != 7 || workerNext.PlanFloor != hostNext.PlanFloor ||
		workerNext.TokenFloor != hostNext.TokenFloor {
		t.Fatalf("floors: host %+v worker %+v", hostNext, workerNext)
	}
}

// Catches a gap fence that drops outstanding identities: a completion queued before
// the gap would drain as stale and its retained evidence would be discarded.
func TestAA24GapFenceKeepsEarlierQueuedCompletion(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	identity := pair.admit(1, 1, 10)
	want(t, "complete", pair.worker.Complete(identity, StatusAccepted, at(11)), nil)
	pair.worker.raiseLocalGap(FenceCorruption)
	gap, ok, err := pair.poll(12)
	if err != nil || !ok || gap.Record.Kind != RecordGap {
		t.Fatalf("gap first: %+v %v %v", gap, ok, err)
	}
	done, ok, err := pair.poll(13)
	if err != nil || !ok || done.Record.Kind != RecordCompletion || done.Record.OwnerToken != 1 {
		t.Fatalf("queued completion after the gap: %+v %v %v", done, ok, err)
	}
}

// Catches a fence that drops an outstanding identity when the lingering list is
// full: the queued completion would drain as stale and be discarded.
func TestAA24FenceKeepsIdentityWhenLingeringIsFull(t *testing.T) {
	mem := fixtureMemory(fixtureBytes())
	config := fixtureConfig
	config.Lanes[LaneWorkerData-1].Capacity = 2
	expected, _, err := InitMapping(mem, fixtureBinding(), config, ipcFixtureLimits)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	pair := attachPair(t, mem, expected)
	pair.publish(1, 10)
	pair.host.lingering = append(pair.host.lingering[:0], lingered{horizon: 1 << 40}, lingered{horizon: 1 << 40})
	pair.host.lingering = pair.host.lingering[:cap(pair.host.lingering)]
	identity := pair.admit(1, 1, 10)
	want(t, "complete", pair.worker.Complete(identity, StatusAccepted, at(11)), nil)
	want(t, "stop", pair.host.RequestStop(at(12)), nil)
	delivery, ok, err := pair.poll(13)
	if err != nil || !ok || delivery.Record.Kind != RecordCompletion {
		t.Fatalf("queued completion: %+v %v %v", delivery, ok, err)
	}
}

// Catches a retained outstanding identity that skips the fence horizon: a completion
// posted after the fence, reported as cancelled, would be accepted.
func TestAA24FenceRetainedOutstandingRejectsPostFenceCompletion(t *testing.T) {
	mem := fixtureMemory(fixtureBytes())
	config := fixtureConfig
	config.Lanes[LaneWorkerData-1].Capacity = 2
	expected, _, err := InitMapping(mem, fixtureBinding(), config, ipcFixtureLimits)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	pair := attachPair(t, mem, expected)
	pair.publish(1, 10)
	pair.host.lingering = append(pair.host.lingering[:0], lingered{horizon: 1 << 40}, lingered{horizon: 1 << 40})
	pair.host.lingering = pair.host.lingering[:cap(pair.host.lingering)]
	early := pair.admit(1, 1, 10)
	want(t, "early complete", pair.worker.Complete(early, StatusAccepted, at(10)), nil)
	identity := pair.admit(1, 2, 10)
	want(t, "stop", pair.host.RequestStop(at(11)), nil)
	if len(pair.host.outstanding) != 2 {
		t.Fatalf("identities not retained: %+v", pair.host.outstanding)
	}
	want(t, "complete after fence", pair.worker.Complete(identity, StatusAccepted, at(12)), nil)
	delivery, ok, err := pair.poll(13)
	if err != nil || !ok || delivery.Record.OwnerToken != 1 {
		t.Fatalf("pre-fence completion: %+v %v %v", delivery, ok, err)
	}
	delivery, ok, err = pair.poll(14)
	if ok || !errorstest.Is(err, ErrStale) {
		t.Fatalf("post-fence completion accepted: %+v %v %v", delivery, ok, err)
	}
}

// Catches a repeated stop that moves a retained cancelled identity into lingering with
// a newer horizon once polling frees room: its post-fence completion would be accepted.
func TestAA24RepeatedStopKeepsRetainedHorizonAfterRoomFrees(t *testing.T) {
	mem := fixtureMemory(fixtureBytes())
	config := fixtureConfig
	config.Lanes[LaneWorkerData-1].Capacity = 2
	expected, _, err := InitMapping(mem, fixtureBinding(), config, ipcFixtureLimits)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	pair := attachPair(t, mem, expected)
	pair.publish(1, 10)
	pair.host.lingering = append(pair.host.lingering[:0], lingered{horizon: 1}, lingered{horizon: 1})
	pair.host.lingering = pair.host.lingering[:cap(pair.host.lingering)]
	early := pair.admit(1, 1, 10)
	want(t, "early complete", pair.worker.Complete(early, StatusAccepted, at(10)), nil)
	identity := pair.admit(1, 2, 10)
	want(t, "stop", pair.host.RequestStop(at(11)), nil)
	want(t, "complete after fence", pair.worker.Complete(identity, StatusAccepted, at(12)), nil)
	delivery, ok, err := pair.poll(13)
	if err != nil || !ok || delivery.Record.OwnerToken != 1 {
		t.Fatalf("pre-fence completion: %+v %v %v", delivery, ok, err)
	}
	want(t, "second stop", pair.host.RequestStop(at(14)), nil)
	delivery, ok, err = pair.poll(15)
	if ok || !errorstest.Is(err, ErrStale) {
		t.Fatalf("post-fence completion accepted: %+v %v %v", delivery, ok, err)
	}
}

// Catches a delayed termination notice after a graceful teardown that reads the
// cleared lane mapping and panics.
func TestAA24ObserveWorkerTerminatedAfterTeardownIsSafe(t *testing.T) {
	pair := newPair(t)
	want(t, "stop", pair.host.RequestStop(at(11)), nil)
	want(t, "worker reads stop", pair.worker.PollControl(at(12)), nil)
	want(t, "host teardown", pair.host.Teardown(), nil)
	pair.host.ObserveWorkerTerminated()
	if pair.host.OutputOpen() {
		t.Fatal("detached host reports open output")
	}
}

// Catches a settlement or teardown that ignores a revocation queued behind the stop:
// the quarantined plan would be cleared without its exact retirement acknowledgement.
func TestAA24RevokeAfterStopMustBeReadBeforeTeardown(t *testing.T) {
	pair := newPair(t)
	ref := pair.publish(1, 10)
	want(t, "stop", pair.host.RequestStop(at(11)), nil)
	want(t, "worker reads publish and stop", pair.worker.PollControl(at(12)), nil)
	want(t, "late revoke", pair.host.RevokePlan(ref, at(13)), nil)
	want(t, "worker teardown", pair.worker.Teardown(), ErrNotAcknowledged)
	want(t, "host teardown", pair.host.Teardown(), ErrNotAcknowledged)
	if _, err := pair.host.ResyncExpected(ipcFixtureStream + 1); !errorstest.Is(err, ErrNotAcknowledged) {
		t.Fatalf("resync: %v", err)
	}
	want(t, "worker reads revoke", pair.worker.PollControl(at(14)), nil)
	// The adopted plan's retirement is still unposted, so the lanes are empty yet the
	// host plan stays quarantined and nothing may settle.
	if pair.host.controlSettled() {
		t.Fatal("settled past an unacknowledged adopted revocation")
	}
	want(t, "host teardown before acknowledgement", pair.host.Teardown(), ErrNotAcknowledged)
	if _, err := pair.host.ResyncExpected(ipcFixtureStream + 1); !errorstest.Is(err, ErrNotAcknowledged) {
		t.Fatalf("resync before acknowledgement: %v", err)
	}
	want(t, "acknowledge", pair.worker.AcknowledgeRevocation(ref, at(15)), nil)
	want(t, "host teardown before the retirement is read", pair.host.Teardown(), ErrNotAcknowledged)
	if delivery, ok, err := pair.poll(16); err != nil || !ok || delivery.Record.Kind != RecordAcknowledge {
		t.Fatalf("retirement: %+v %v %v", delivery, ok, err)
	}
	if !pair.host.controlSettled() {
		t.Fatal("not settled after the retirement was consumed")
	}
	want(t, "worker teardown after read", pair.worker.Teardown(), nil)
	want(t, "host teardown after read", pair.host.Teardown(), nil)
}

// Catches an eviction that trusts the producer snapshot taken at expiry: the worker
// captured a completion before its deadline, queued it after the host expired the
// command, and a full lingering list dropped the identity so the completion drained
// as stale.
func TestAA24LateCompletionAfterExpirySurvivesFullLingering(t *testing.T) {
	mem := fixtureMemory(fixtureBytes())
	config := fixtureConfig
	config.Lanes[LaneWorkerData-1].Capacity = 2
	limits := ipcFixtureLimits
	limits.MaxOutstanding = 1
	expected, _, err := InitMapping(mem, fixtureBinding(), config, limits)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	pair := attachPair(t, mem, expected)
	pair.publish(1, 5)
	identity := func(token OwnerToken, tick Tick) CommandIdentity {
		command := fixtureCommand(1, token)
		command.RequestDeadline = tick + 10
		return command
	}
	first := identity(1, 10)
	want(t, "send first", pair.host.SendCommand(first, nil, at(10)), nil)
	if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(10)); err != nil || !ok {
		t.Fatalf("next: %v %v", ok, err)
	}
	// Each later send expires the previous command on the host, filling the list.
	want(t, "send second", pair.host.SendCommand(identity(2, 30), nil, at(30)), nil)
	want(t, "send third", pair.host.SendCommand(identity(3, 50), nil, at(50)), nil)
	// The descheduled worker now queues the completion it captured before the deadline.
	want(t, "late completion", pair.worker.Complete(first, StatusAccepted, at(15)), nil)
	want(t, "fourth send finds no room", pair.host.SendCommand(identity(4, 70), nil, at(70)), ErrIPCCapacity)
	delivery, ok, err := pair.poll(71)
	if err != nil || !ok || delivery.Record.Kind != RecordCompletion || delivery.Record.OwnerToken != 1 {
		t.Fatalf("late completion: %+v %v %v", delivery, ok, err)
	}
}

// Catches a completion shape check that ignores the unused payload and phase: a
// corrupted record would be surfaced, or retried as a buffer error, instead of
// fencing malformed framing.
func TestAA24CompletionWithPayloadOrPhaseIsFenced(t *testing.T) {
	cases := []struct {
		name  string
		patch func([]byte)
		dst   int
	}{
		{"phase", func(slot []byte) { slot[123] = 1 }, 16},
		{"payload fits", func(slot []byte) { binarytest.LittleEndian.PutUint32(slot[4:], 2) }, 16},
		{"payload exceeds buffer", func(slot []byte) { binarytest.LittleEndian.PutUint32(slot[4:], 2) }, 0},
	}
	for _, tc := range cases {
		pair := newPair(t)
		pair.publish(1, 10)
		identity := pair.admit(1, 1, 10)
		want(t, tc.name+" complete", pair.worker.Complete(identity, StatusAccepted, at(11)), nil)
		tc.patch(pair.host.lane(LaneWorkerData).slot(0))
		_, ok, err := pair.host.Poll(make([]byte, 0, tc.dst), at(12))
		if ok || err != ErrRecord || pair.host.Fence().Reason != FenceCorruption {
			t.Fatalf("%s: ok=%v err=%v fence=%+v", tc.name, ok, err, pair.host.Fence())
		}
	}
	// The unpatched record is the positive control.
	pair := newPair(t)
	pair.publish(1, 10)
	identity := pair.admit(1, 1, 10)
	want(t, "control complete", pair.worker.Complete(identity, StatusAccepted, at(11)), nil)
	if delivery, ok, err := pair.poll(12); err != nil || !ok || delivery.Record.Kind != RecordCompletion {
		t.Fatalf("control: %+v %v %v", delivery, ok, err)
	}
}

// Catches a completion with an invalid status that clears the active command first.
func TestAA24InvalidCompletionStatusKeepsActiveCommand(t *testing.T) {
	for _, status := range []Status{StatusPending, StatusRetired + 1} {
		pair := newPair(t)
		pair.publish(1, 10)
		identity := pair.admit(1, 1, 10)
		want(t, "invalid status", pair.worker.Complete(identity, status, at(11)), ErrRecord)
		want(t, "valid completion", pair.worker.Complete(identity, StatusAccepted, at(12)), nil)
	}
}

// Catches a clock-fenced drain that stamps a refused plan's retirement with the
// regressed tick: the host would reject it as a clock regression after the worker
// dequeued the revoke, leaving the plan quarantined.
func TestAA24FencedDrainStampsRetirementWithTrustedTick(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	refused := fixtureRef(2)
	arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	want(t, "publish refused", pair.host.PublishPlan(refused, arenaStart, 8, at(11)), nil)
	pair.worker.refusedRefs = append(pair.worker.refusedRefs, refused)
	want(t, "revoke", pair.host.RevokePlan(refused, at(12)), nil)
	want(t, "regressed worker clock", pair.worker.PollControl(at(5)), ErrIPCClock)
	for tick := Tick(20); tick < 24; tick++ {
		if _, _, err := pair.poll(tick); err != nil {
			t.Fatalf("host poll %d: %v", tick, err)
		}
	}
	for _, held := range pair.host.QuarantinedPlans(nil) {
		if held.matches(refused) {
			t.Fatal("refused plan stayed quarantined")
		}
	}
}

// Catches a retirement acknowledgement that moves a retained fence-cancelled identity
// to lingering with a fresh non-strict horizon: its post-fence completion would be
// accepted.
func TestAA24RetirementKeepsRetainedFenceHorizon(t *testing.T) {
	mem := fixtureMemory(fixtureBytes())
	config := fixtureConfig
	config.Lanes[LaneWorkerData-1].Capacity = 2
	expected, _, err := InitMapping(mem, fixtureBinding(), config, ipcFixtureLimits)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	pair := attachPair(t, mem, expected)
	ref := pair.publish(1, 10)
	pair.host.lingering = append(pair.host.lingering[:0], lingered{horizon: 1}, lingered{horizon: 1})
	pair.host.lingering = pair.host.lingering[:cap(pair.host.lingering)]
	early := pair.admit(1, 1, 10)
	want(t, "early complete", pair.worker.Complete(early, StatusAccepted, at(10)), nil)
	identity := pair.admit(1, 2, 10)
	want(t, "stop", pair.host.RequestStop(at(11)), nil)
	want(t, "complete after fence", pair.worker.Complete(identity, StatusAccepted, at(12)), nil)
	// The older evidence drains first, so the retirement finds lingering room.
	delivery, ok, err := pair.poll(13)
	if err != nil || !ok || delivery.Record.OwnerToken != 1 {
		t.Fatalf("older evidence: %+v %v %v", delivery, ok, err)
	}
	want(t, "revoke", pair.host.RevokePlan(ref, at(14)), nil)
	want(t, "worker reads stop and revoke", pair.worker.PollControl(at(15)), nil)
	want(t, "ack", pair.worker.AcknowledgeRevocation(ref, at(16)), nil)
	for tick := Tick(17); tick < 24; tick++ {
		delivery, ok, err = pair.poll(tick)
		if ok && delivery.Record.Kind == RecordCompletion && delivery.Record.OwnerToken == 2 {
			t.Fatalf("post-fence completion accepted: %+v %v", delivery, err)
		}
	}
}

// Catches an idle service tick reused for a new backlog, and a timer reset by
// another enqueue while the first acknowledgement remains unread.
func TestAA24ControlDeadlineStartsWithBacklogAfterIdle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		consume   bool
		appendAck bool
	}{
		{name: "host consumes acknowledgement", consume: true},
		{name: "unread acknowledgement expires"},
		{name: "another acknowledgement preserves deadline", appendAck: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair := newPair(t)
			arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
			refs := []PlanRef{fixtureRef(1), fixtureRef(2)}
			for index := range refs {
				refs[index].PlanExpiry = 12
				want(t, "publish", pair.host.PublishPlan(refs[index], arenaStart, 8, at(10)), nil)
			}
			want(t, "refuse expired plans", pair.worker.PollControl(at(12)), nil)
			if pair.worker.PublishRefused() != 2 || len(pair.worker.adopted) != 0 {
				t.Fatal("positive control: both plans must be refused")
			}
			want(t, "empty service", pair.worker.PollControl(at(20)), nil)
			want(t, "revoke after idle", pair.host.RevokePlan(refs[0], at(200)), nil)
			want(t, "enqueue retirement during service", pair.worker.PollControl(at(200)), nil)
			if !pair.worker.OutputOpen() {
				t.Fatalf("new backlog inherited idle time: %+v", pair.worker.Fence())
			}
			if tc.consume {
				delivery, ok, err := pair.poll(201)
				if err != nil || !ok || delivery.Record.Kind != RecordAcknowledge || delivery.Record.Plan != refs[0].Plan {
					t.Fatalf("retirement: %+v %v %v", delivery, ok, err)
				}
			}
			if tc.appendAck {
				want(t, "second revoke", pair.host.RevokePlan(refs[1], at(250)), nil)
				want(t, "second retirement", pair.worker.PollControl(at(250)), nil)
			}
			want(t, "before deadline", pair.worker.PollControl(at(299)), nil)
			if !pair.worker.OutputOpen() {
				t.Fatalf("closed before full budget: %+v", pair.worker.Fence())
			}
			want(t, "at deadline", pair.worker.PollControl(at(300)), nil)
			if tc.consume {
				if !pair.worker.OutputOpen() {
					t.Fatalf("consumed backlog expired: %+v", pair.worker.Fence())
				}
			} else if pair.worker.Fence().Reason != FenceControlDeadline {
				t.Fatalf("unread backlog did not expire: %+v", pair.worker.Fence())
			}
		})
	}
}

// Catches an ordinal accounting that appends an early control ordinal again: two
// retirement acknowledgements carrying the same ordinal would both be accepted.
func TestAA24DuplicateEarlyControlOrdinalIsRejected(t *testing.T) {
	pair := newPair(t)
	host := pair.host
	if !host.accountSequence(2, true) {
		t.Fatal("first early control ordinal must be explained")
	}
	if host.accountSequence(2, true) {
		t.Fatal("a repeated early control ordinal was accepted")
	}
	if len(host.explained) != 1 {
		t.Fatalf("explained list holds %d entries, want 1", len(host.explained))
	}
	if !host.accountSequence(1, false) || host.nextCapture != 3 {
		t.Fatalf("positive control: next capture %d, want 3", host.nextCapture)
	}
}

// Catches a completion that skips the deadline check: with equality meaning expired,
// completing exactly at the owner or request deadline without a prior admitted output
// must be refused before a sequence is allocated and must clear the active command.
func TestAA24CompleteAtDeadlineIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CommandIdentity)
	}{
		{"request deadline", func(identity *CommandIdentity) { identity.RequestDeadline = 100 }},
		{"owner expiry", func(identity *CommandIdentity) { identity.OwnerExpiry = 100 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair := newPair(t)
			pair.publish(1, 10)
			identity := fixtureCommand(1, 1)
			tc.mutate(&identity)
			want(t, "send", pair.host.SendCommand(identity, []byte{1}, at(11)), nil)
			if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(12)); err != nil || !ok {
				t.Fatalf("next command: %v %v", ok, err)
			}
			next := pair.worker.nextSequence
			want(t, "complete at the deadline", pair.worker.Complete(identity, StatusRetired, at(100)), ErrIPCExpired)
			if pair.worker.nextSequence != next || pair.worker.hasActive {
				t.Fatalf("sequence %d (want %d) active %v", pair.worker.nextSequence, next, pair.worker.hasActive)
			}
		})
	}
	pair := newPair(t)
	pair.publish(1, 10)
	identity := pair.admit(1, 1, 11)
	want(t, "positive control: complete before the deadline", pair.worker.Complete(identity, StatusRetired, at(12)), nil)
}

// Catches a latched clock-fence service branch that skips the retirement retry: the
// queued exact retirement of a refused publication must be emitted once the worker
// control lane drains, even while the regressed clock stays latched.
func TestAA24ClockFenceStillRetriesRetirement(t *testing.T) {
	pair := newPair(t)
	worker := pair.worker
	for plan := PlanGeneration(1); plan <= 2; plan++ {
		tick := Tick(10 * plan)
		ref := pair.publish(plan, tick)
		want(t, "revoke", pair.host.RevokePlan(ref, at(tick+1)), nil)
		want(t, "worker poll", worker.PollControl(at(tick+1)), nil)
		want(t, "ack", worker.AcknowledgeRevocation(ref, at(tick+2)), nil)
	}
	refused := fixtureRef(3)
	worker.refusedRefs = append(worker.refusedRefs, refused)
	record := Record{Kind: RecordRevoke}
	record.applyRef(refused)
	want(t, "revoke of the refused plan", pair.host.writeControl(&record), nil)
	want(t, "worker poll on saturated control", worker.PollControl(at(23)), nil)
	if !hostControlUnread(t, worker) {
		t.Fatal("revocation must stay queued while its retirement is unposted")
	}
	for tick := Tick(24); tick < 27; tick++ {
		if _, ok, err := pair.poll(tick); !ok || err != nil {
			t.Fatalf("host poll: %v %v", ok, err)
		}
	}
	want(t, "regressed worker clock", worker.PollControl(at(5)), ErrIPCClock)
	if len(worker.retireRetry) != 0 || len(worker.refusedRefs) != 0 {
		t.Fatalf("retirement not emitted under the latched fence: retry %d refused %d", len(worker.retireRetry), len(worker.refusedRefs))
	}
}

// Catches: leaving an expired active command in place so NextCommand never
// dequeues later work.
func TestAA24NextCommandRetiresExpiredActiveCommand(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	short := fixtureCommand(1, 1)
	short.RequestDeadline = 50
	want(t, "send short", pair.host.SendCommand(short, []byte{1}, at(10)), nil)
	want(t, "send next", pair.host.SendCommand(fixtureCommand(1, 2), []byte{2}, at(10)), nil)
	if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(11)); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(49)); ok || err != nil {
		t.Fatalf("still active: %v %v", ok, err)
	}
	delivery, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(50))
	if err != nil || !ok || delivery.Record.OwnerToken != 2 {
		t.Fatalf("after expiry: %+v %v %v", delivery, ok, err)
	}
}

// Catches: reporting a buffer shortfall before refusing a payload on a
// payload-free control record, which leaves the corrupt slot retryable.
func TestAA24ControlRecordPayloadFencesWithZeroCapacityDestination(t *testing.T) {
	pair := newPair(t)
	pair.worker.raiseLocalGap(FenceCorruption)
	lane := pair.host.lane(LaneWorkerControl)
	binarytest.LittleEndian.PutUint32(lane.slot(0)[4:], 4)
	_, ok, err := pair.host.Poll(nil, at(11))
	want(t, "payload on control", err, ErrRecord)
	if ok || pair.host.Fence().Reason != FenceCorruption {
		t.Fatalf("fence %+v", pair.host.Fence())
	}
}

// Catches an acknowledgement path that returns the latched clock error before the
// retirement of an already revoked plan, which leaves the host plan quarantined.
func TestAA24RetirementAcceptedAfterLatchedClockFence(t *testing.T) {
	pair := newPair(t)
	ref := pair.publish(1, 10)
	want(t, "revoke", pair.host.RevokePlan(ref, at(11)), nil)
	want(t, "regressed worker clock consumes the revocation", pair.worker.PollControl(at(5)), ErrIPCClock)
	want(t, "retirement under the latched fence", pair.worker.AcknowledgeRevocation(ref, at(5)), nil)
	want(t, "second retirement", pair.worker.AcknowledgeRevocation(ref, at(5)), ErrStale)
	for tick := Tick(20); tick < 24; tick++ {
		if _, _, err := pair.poll(tick); err != nil {
			t.Fatalf("host poll %d: %v", tick, err)
		}
	}
	if left := pair.host.QuarantinedPlans(nil); len(left) != 0 {
		t.Fatalf("plan stayed quarantined: %+v", left)
	}
}

// Catches a fenced drain that discards a queued publication: its exact revocation
// would find nothing to retire and the host would keep the plan indefinitely.
func TestAA24FencedDrainKeepsRefusedPublication(t *testing.T) {
	pair := newPair(t)
	ref := fixtureRef(1)
	arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	want(t, "publish stays queued", pair.host.PublishPlan(ref, arenaStart, 8, at(10)), nil)
	// A completion attempt reads the worker clock without draining the control lane.
	want(t, "clock read", pair.worker.Complete(fixtureCommand(1, 1), StatusAccepted, at(10)), ErrMismatch)
	want(t, "clock fault before the drain", pair.worker.PollControl(at(5)), ErrIPCClock)
	if len(pair.worker.adopted) != 0 || len(pair.worker.refusedRefs) != 1 {
		t.Fatalf("adopted %d refused %d", len(pair.worker.adopted), len(pair.worker.refusedRefs))
	}
	want(t, "revoke", pair.host.RevokePlan(ref, at(11)), nil)
	want(t, "fenced worker retires the revocation", pair.worker.PollControl(at(5)), ErrIPCClock)
	for tick := Tick(20); tick < 24; tick++ {
		if _, _, err := pair.poll(tick); err != nil {
			t.Fatalf("host poll %d: %v", tick, err)
		}
	}
	if left := pair.host.QuarantinedPlans(nil); len(left) != 0 {
		t.Fatalf("plan stayed quarantined: %+v", left)
	}
}

// Catches a host revoke that returns the latched host clock error before queueing
// the revocation of published work.
func TestAA24HostRevokeQueuedUnderLatchedClockFence(t *testing.T) {
	pair := newPair(t)
	ref := pair.publish(1, 10)
	_, _, err := pair.poll(5)
	want(t, "regressed host clock", err, ErrIPCClock)
	want(t, "revoke on a fenced clock", pair.host.RevokePlan(ref, at(5)), nil)
	want(t, "worker reads the revocation", pair.worker.PollControl(at(12)), nil)
	want(t, "retirement", pair.worker.AcknowledgeRevocation(ref, at(13)), nil)
}

// Catches a drain that decides fencing once per pass from the clock alone: a
// publication queued before the worker learns of a data-lane gap fence would be
// adopted and stay quarantined until an explicit acknowledgement.
func TestAA24PublicationQueuedBeforeGapFenceIsRefused(t *testing.T) {
	pair := newPair(t)
	ref := fixtureRef(1)
	arenaStart := pair.host.lanes[0].arenaLayout.ArenaOffset + buildBytes
	want(t, "publish stays queued", pair.host.PublishPlan(ref, arenaStart, 8, at(10)), nil)
	pair.worker.raiseLocalGap(FenceCounter)
	if pair.worker.Fence().Reason != FenceCounter {
		t.Fatalf("fence %+v", pair.worker.Fence())
	}
	want(t, "worker drains under the gap fence", pair.worker.PollControl(at(11)), nil)
	if len(pair.worker.adopted) != 0 || len(pair.worker.refusedRefs) != 1 {
		t.Fatalf("adopted %d refused %d", len(pair.worker.adopted), len(pair.worker.refusedRefs))
	}
	want(t, "revoke", pair.host.RevokePlan(ref, at(12)), nil)
	want(t, "worker retires the refused plan", pair.worker.PollControl(at(12)), nil)
	for tick := Tick(20); tick < 26; tick++ {
		_, _, _ = pair.poll(tick)
	}
	if left := pair.host.QuarantinedPlans(nil); len(left) != 0 {
		t.Fatalf("plan stayed quarantined: %+v", left)
	}
}

// TestAA24ObserveRawRefusesBeforeSideEffects pins the selected-observation ingress:
// each refusal leaves the sequence, capture time, fence and loss counter untouched,
// and an exact active selection is the only accepted selection.
func TestAA24ObserveRawRefusesBeforeSideEffects(t *testing.T) {
	type tc struct {
		name   string
		active bool
		build  func(active CommandIdentity) Observation
		want   error
	}
	selectObservation := func(mutate func(*Observation, *CommandIdentity)) func(CommandIdentity) Observation {
		return func(active CommandIdentity) Observation {
			observation := fixtureRaw(7)
			observation.Transaction = active.Transaction
			observation.Selection = active
			mutate(&observation, &observation.Selection)
			return observation
		}
	}
	cases := []tc{
		{"mismatched transaction", true, selectObservation(func(o *Observation, _ *CommandIdentity) { o.Transaction++ }), ErrRecord},
		{"selection without active command", false, selectObservation(func(*Observation, *CommandIdentity) {}), ErrMismatch},
		{"selection for a different command", true, selectObservation(func(o *Observation, s *CommandIdentity) {
			s.OwnerToken++
		}), ErrMismatch},
		{"selection with a different plan", true, selectObservation(func(_ *Observation, s *CommandIdentity) {
			s.Ref.Plan++
		}), ErrMismatch},
		{"selection missing owner token", true, selectObservation(func(_ *Observation, s *CommandIdentity) {
			s.OwnerToken = 0
		}), ErrRecord},
		{"zero transaction", true, selectObservation(func(o *Observation, _ *CommandIdentity) {
			o.Selection = CommandIdentity{}
			o.Transaction = 0
		}), ErrRecord},
		{"control direction", true, selectObservation(func(o *Observation, _ *CommandIdentity) {
			o.Selection = CommandIdentity{}
			o.Direction = DirectionControl
		}), ErrRecord},
		{"direction out of range", true, selectObservation(func(o *Observation, _ *CommandIdentity) {
			o.Selection = CommandIdentity{}
			o.Direction = DirectionEcho + 1
		}), ErrRecord},
		{"phase out of range", true, selectObservation(func(o *Observation, _ *CommandIdentity) {
			o.Selection = CommandIdentity{}
			o.Phase = maxPhase + 1
		}), ErrRecord},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pair := newPair(t)
			var active CommandIdentity
			if tc.active {
				pair.publish(1, 10)
				active = pair.admit(1, 1, 10)
			} else {
				active = fixtureCommand(1, 1)
			}
			worker := pair.worker
			next, last, lost, fence := worker.nextSequence, worker.lastCapture, worker.lost, worker.fence
			got, err := worker.ObserveRaw(tc.build(active), at(11))
			want(t, "refusal", err, tc.want)
			if got != 0 || worker.nextSequence != next || worker.lastCapture != last ||
				worker.lost != lost || worker.fence != fence || !worker.OutputOpen() {
				t.Fatalf("refusal had side effects: seq=%d next=%d last=%d lost=%d fence=%+v", got, worker.nextSequence, worker.lastCapture, worker.lost, worker.fence)
			}
		})
	}
	t.Run("exact active selection is accepted", func(t *testing.T) {
		pair := newPair(t)
		pair.publish(1, 10)
		active := pair.admit(1, 1, 10)
		observation := fixtureRaw(7)
		observation.Transaction = active.Transaction
		observation.Selection = active
		got, err := pair.worker.ObserveRaw(observation, at(11))
		want(t, "accepted", err, nil)
		if got == 0 || pair.worker.lastCapture != 11 {
			t.Fatalf("sequence %d capture %d", got, pair.worker.lastCapture)
		}
	})
}

func hostControlUnread(t *testing.T, worker *Worker) bool {
	t.Helper()
	produced, read, err := worker.lane(LaneHostControl).indices()
	if err != nil {
		t.Fatal(err)
	}
	return produced != read
}

// Catches releasing a late revocation's input slot before its retirement is posted:
// with the worker control lane drained by the host, the host must not settle or tear
// down while the retirement is still unwritten, and must settle once it is.
func TestAA24SettlementWaitsForUnpostedRetirement(t *testing.T) {
	pair := newPair(t)
	worker := pair.worker
	for plan := PlanGeneration(1); plan <= 2; plan++ {
		tick := Tick(10 * plan)
		ref := pair.publish(plan, tick)
		want(t, "revoke", pair.host.RevokePlan(ref, at(tick+1)), nil)
		want(t, "worker poll", worker.PollControl(at(tick+1)), nil)
		want(t, "ack", worker.AcknowledgeRevocation(ref, at(tick+2)), nil)
	}
	refused := fixtureRef(3)
	worker.refusedRefs = append(worker.refusedRefs, refused)
	host := pair.host
	want(t, "stop", host.RequestStop(at(22)), nil)
	record := Record{Kind: RecordRevoke}
	record.applyRef(refused)
	want(t, "late revoke", host.writeControl(&record), nil)
	want(t, "worker poll", worker.PollControl(at(23)), nil)
	// The host drains the worker control lane to empty; the retirement is still unposted.
	control := worker.lane(LaneWorkerControl)
	produced, _, _ := control.indices()
	hostSide := host.lane(LaneWorkerControl)
	for {
		_, consumer, _ := hostSide.indices()
		if consumer == produced {
			break
		}
		if _, ok, err := host.Poll(make([]byte, 0, 16), at(24)); !ok || err != nil {
			t.Fatalf("host poll: %v %v", ok, err)
		}
	}
	if host.controlSettled() {
		t.Fatal("settled past an unposted retirement")
	}
	want(t, "teardown refused", host.Teardown(), ErrNotAcknowledged)
	want(t, "worker retry", worker.PollControl(at(25)), nil)
	// The injected reference is unknown to the host list, so the read is dropped as stale.
	if _, _, err := host.Poll(make([]byte, 0, 16), at(26)); err != ErrStale {
		t.Fatalf("retirement not read: %v", err)
	}
	if !host.controlSettled() {
		t.Fatal("not settled after the retirement was read")
	}
}

// Catches guards seeded independently per endpoint: two endpoints handed readings
// from different nonzero domains would both accept them and compare incomparable
// deadlines and capture times. Each endpoint is varied alone while the other is valid.
func TestAA24SharedClockDomainRejectsForeignReadings(t *testing.T) {
	foreign := ClockReading{Domain: ipcFixtureDomain + 1, Tick: 11}
	fresh := func() *pair {
		mem, expected := fixtureMapping(t)
		expected.ClockDomain = ipcFixtureDomain
		return attachPair(t, mem, expected)
	}
	pair := fresh()
	want(t, "worker same domain", pair.worker.PollControl(at(10)), nil)
	want(t, "host same domain", pair.host.RequestStop(at(10)), nil)

	pair = fresh()
	want(t, "worker foreign domain", pair.worker.PollControl(foreign), ErrIPCClock)
	if pair.worker.Fence().Reason != FenceClock {
		t.Fatalf("worker fence %+v", pair.worker.Fence())
	}
	pair = fresh()
	want(t, "host foreign domain", pair.host.PublishPlan(fixtureRef(1), 0, 0, foreign), ErrIPCClock)
	if pair.host.Fence().Reason != FenceClock {
		t.Fatalf("host fence %+v", pair.host.Fence())
	}
}

// Catches a command shape check that ignores the phase: a corrupted command slot
// would be delivered to the output path instead of fencing the mapping.
func TestAA24CommandWithPhaseIsFenced(t *testing.T) {
	pair := newPair(t)
	pair.publish(1, 10)
	want(t, "send", pair.host.SendCommand(fixtureCommand(1, 1), nil, at(11)), nil)
	pair.host.lane(LaneHostData).slot(0)[123] = 1
	_, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(12))
	if ok || err != ErrRecord || pair.worker.Fence().Reason != FenceCorruption {
		t.Fatalf("ok=%v err=%v fence=%+v", ok, err, pair.worker.Fence())
	}
}

// Catches eviction on an empty data lane: with a worker paused before it publishes a
// completion captured before the deadline, the identity must stay matchable.
func TestAA24PausedWorkerCompletionSurvivesEvictionAttempt(t *testing.T) {
	mem := fixtureMemory(fixtureBytes())
	config := fixtureConfig
	config.Lanes[LaneWorkerData-1].Capacity = 2
	limits := ipcFixtureLimits
	limits.MaxOutstanding = 1
	expected, _, err := InitMapping(mem, fixtureBinding(), config, limits)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	pair := attachPair(t, mem, expected)
	pair.publish(1, 5)
	identity := func(token OwnerToken, tick Tick) CommandIdentity {
		command := fixtureCommand(1, token)
		command.RequestDeadline = tick + 10
		return command
	}
	first := identity(1, 10)
	want(t, "send first", pair.host.SendCommand(first, nil, at(10)), nil)
	if _, ok, err := pair.worker.NextCommand(make([]byte, 0, 16), at(10)); err != nil || !ok {
		t.Fatalf("next: %v %v", ok, err)
	}
	want(t, "send second", pair.host.SendCommand(identity(2, 30), nil, at(30)), nil)
	want(t, "send third", pair.host.SendCommand(identity(3, 50), nil, at(50)), nil)
	// Eviction is attempted while the data lane is still empty and the worker paused.
	want(t, "eviction refused", pair.host.SendCommand(identity(4, 70), nil, at(70)), ErrIPCCapacity)
	want(t, "late completion", pair.worker.Complete(first, StatusAccepted, at(15)), nil)
	delivery, ok, err := pair.poll(71)
	if err != nil || !ok || delivery.Record.Kind != RecordCompletion || delivery.Record.OwnerToken != 1 {
		t.Fatalf("late completion: %+v %v %v", delivery, ok, err)
	}
}

// Catches a teardown that clears a revoked plan before its retirement was posted,
// which would leave the host quarantine unsettled for good.
func TestAA24TeardownWaitsForPendingRetirement(t *testing.T) {
	pair := newPair(t)
	ref := pair.publish(1, 10)
	want(t, "revoke", pair.host.RevokePlan(ref, at(11)), nil)
	want(t, "stop", pair.host.RequestStop(at(12)), nil)
	want(t, "worker drain", pair.worker.PollControl(at(13)), nil)
	want(t, "teardown before retirement", pair.worker.Teardown(), ErrNotAcknowledged)
	want(t, "retire", pair.worker.AcknowledgeRevocation(ref, at(14)), nil)
	want(t, "teardown after retirement", pair.worker.Teardown(), nil)
}

// Catches an attach that accepts an unset clock domain: host and worker could then
// each adopt the domain of their first reading and compare incomparable deadlines.
// Each endpoint is varied alone with the default expectation otherwise valid.
func TestAA24AttachRequiresSharedClockDomain(t *testing.T) {
	mem, expected := fixtureMapping(t)
	if expected.ClockDomain == 0 {
		t.Fatal("default expectation has no clock domain")
	}
	zero := expected
	zero.ClockDomain = 0
	if _, err := AttachHost(mem, zero); err != ErrIPCClock {
		t.Fatalf("host zero domain: %v", err)
	}
	if _, err := AttachWorker(mem, zero); err != ErrIPCClock {
		t.Fatalf("worker zero domain: %v", err)
	}
	if _, err := AttachHost(mem, expected); err != nil {
		t.Fatalf("host default domain: %v", err)
	}
	if _, err := AttachWorker(mem, expected); err != nil {
		t.Fatalf("worker default domain: %v", err)
	}
}
