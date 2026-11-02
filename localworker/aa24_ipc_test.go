package localworker

import (
	aa24sha256 "crypto/sha256"
	aa24binarytest "encoding/binary"
	aa24errorstest "errors"
	aa24mathtest "math"
	aa24testing "testing"
	aa24unsafetest "unsafe"
)

// These tests run one serialized schedule over byte arrays with injected time. They
// prove the model's validation and state rules, never cross-process ordering.

const (
	aa24FixtureDomain     = uint64(7)
	aa24FixtureWorker     = AA23WorkerID(11)
	aa24FixtureStream     = AA23StreamGeneration(21)
	aa24FixtureAddress    = byte(0x15)
	aa24FixtureExpiry     = AA23Tick(5000)
	aa24FixtureOwner      = AA23Tick(4000)
	aa24FixtureDeadline   = AA23Tick(3000)
	aa24FixtureSlotBytes  = uint32(152)
	aa24FixtureArenaBytes = uint64(96)
	aa24FixtureBudget     = AA23Tick(100)
)

var aa24FixtureLimits = AA24Limits{
	AA24MaxMappingBytes:     1 << 16,
	AA24MaxSlotBytes:        256,
	AA24MaxCapacity:         64,
	AA24MaxPayloadBytes:     16,
	AA24MaxArenaBytes:       1024,
	AA24MaxPlans:            4,
	AA24MaxOutstanding:      4,
	AA24ControlServiceTicks: aa24FixtureBudget,
}

var aa24FixtureConfig = AA24Config{
	AA24Lanes: [aa24LaneCount]AA24LaneConfig{
		{AA24SlotBytes: aa24FixtureSlotBytes, AA24Capacity: 3},
		{AA24SlotBytes: aa24FixtureSlotBytes, AA24Capacity: 3},
		{AA24SlotBytes: aa24FixtureSlotBytes, AA24Capacity: 2},
		{AA24SlotBytes: aa24FixtureSlotBytes, AA24Capacity: 2},
	},
	AA24ArenaBytes: aa24FixtureArenaBytes,
}

func aa24FixtureBuild() AA24BuildIdentity {
	return AA24BuildIdentity(aa24sha256.Sum256([]byte("aa24 synthetic build")))
}

func aa24FixtureBinding() AA24Binding {
	return AA24Binding{AA24Build: aa24FixtureBuild(), AA24Worker: aa24FixtureWorker, AA24Stream: aa24FixtureStream}
}

func aa24FixtureBytes() int {
	aa24Total := 64 + 4*64 + aa24FixtureArenaBytes
	for _, aa24Lane := range aa24FixtureConfig.AA24Lanes {
		aa24Total += uint64(aa24Lane.AA24SlotBytes) * uint64(aa24Lane.AA24Capacity)
	}
	return int(aa24Total)
}

// aa24FixtureMemory returns byte storage with a guaranteed 8-byte aligned base.
func aa24FixtureMemory(aa24Bytes int) []byte {
	aa24Words := make([]uint64, (aa24Bytes+7)/8)
	return aa24unsafetest.Slice((*byte)(aa24unsafetest.Pointer(&aa24Words[0])), aa24Bytes)
}

func aa24At(aa24Tick AA23Tick) AA23ClockReading {
	return AA23ClockReading{AA23Domain: aa24FixtureDomain, AA23Tick: aa24Tick}
}

func aa24FixtureRef(aa24Plan AA23PlanGeneration) AA24PlanRef {
	return AA24PlanRef{
		AA24Key:        AA23RegistrationKey{AA23Subscriber: 3, AA23Address: aa24FixtureAddress, AA23Epoch: 5},
		AA24Worker:     aa24FixtureWorker,
		AA24Stream:     aa24FixtureStream,
		AA24Plan:       aa24Plan,
		AA24PlanExpiry: aa24FixtureExpiry,
	}
}

func aa24FixtureCommand(aa24Plan AA23PlanGeneration, aa24Token AA24OwnerToken) AA24CommandIdentity {
	return AA24CommandIdentity{
		AA24Ref:             aa24FixtureRef(aa24Plan),
		AA24Transaction:     AA23TransactionOrdinal(aa24Token),
		AA24OwnerToken:      aa24Token,
		AA24OwnerExpiry:     aa24FixtureOwner,
		AA24RequestDeadline: aa24FixtureDeadline,
	}
}

func aa24FixtureRaw(aa24Octet byte) AA24Observation {
	return AA24Observation{AA24Transaction: 9, AA24RawOctet: aa24Octet, AA24Direction: AA24DirectionReceive, AA24Phase: 1}
}

type aa24Pair struct {
	aa24T        *aa24testing.T
	aa24Mem      []byte
	aa24Expected AA24Expected
	aa24Host     *AA24Host
	aa24Worker   *AA24Worker
}

func aa24FixtureMapping(aa24T *aa24testing.T) ([]byte, AA24Expected) {
	aa24T.Helper()
	aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Mem, aa24FixtureBinding(), aa24FixtureConfig, aa24FixtureLimits)
	if aa24Err != nil {
		aa24T.Fatalf("init: %v", aa24Err)
	}
	return aa24Mem, aa24Expected
}

func aa24NewPair(aa24T *aa24testing.T) *aa24Pair {
	aa24T.Helper()
	aa24Mem, aa24Expected := aa24FixtureMapping(aa24T)
	return aa24AttachPair(aa24T, aa24Mem, aa24Expected)
}

func aa24AttachPair(aa24T *aa24testing.T, aa24Mem []byte, aa24Expected AA24Expected) *aa24Pair {
	aa24T.Helper()
	aa24Host, aa24Err := AA24AttachHost(aa24Mem, aa24Expected)
	if aa24Err != nil {
		aa24T.Fatalf("attach host: %v", aa24Err)
	}
	aa24Worker, aa24Err := AA24AttachWorker(aa24Mem, aa24Expected)
	if aa24Err != nil {
		aa24T.Fatalf("attach worker: %v", aa24Err)
	}
	return &aa24Pair{aa24T: aa24T, aa24Mem: aa24Mem, aa24Expected: aa24Expected, aa24Host: aa24Host, aa24Worker: aa24Worker}
}

func aa24Want(aa24T *aa24testing.T, aa24Label string, aa24Got error, aa24Expected error) {
	aa24T.Helper()
	if !aa24errorstest.Is(aa24Got, aa24Expected) {
		aa24T.Fatalf("%s: got %v, want %v", aa24Label, aa24Got, aa24Expected)
	}
}

func (aa24Pair *aa24Pair) aa24Publish(aa24Plan AA23PlanGeneration, aa24Tick AA23Tick) AA24PlanRef {
	aa24Pair.aa24T.Helper()
	aa24Ref := aa24FixtureRef(aa24Plan)
	aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24Want(aa24Pair.aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24Ref, aa24ArenaStart, 8, aa24At(aa24Tick)), nil)
	aa24Want(aa24Pair.aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(aa24Tick)), nil)
	return aa24Ref
}

func (aa24Pair *aa24Pair) aa24Admit(aa24Plan AA23PlanGeneration, aa24Token AA24OwnerToken, aa24Tick AA23Tick) AA24CommandIdentity {
	aa24Pair.aa24T.Helper()
	aa24Identity := aa24FixtureCommand(aa24Plan, aa24Token)
	aa24Want(aa24Pair.aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, []byte{1, 2}, aa24At(aa24Tick)), nil)
	_, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(aa24Tick))
	if aa24Err != nil || !aa24OK {
		aa24Pair.aa24T.Fatalf("next command: ok=%v err=%v", aa24OK, aa24Err)
	}
	return aa24Identity
}

func (aa24Pair *aa24Pair) aa24Poll(aa24Tick AA23Tick) (AA24Delivery, bool, error) {
	return aa24Pair.aa24Host.AA24Poll(make([]byte, 0, 16), aa24At(aa24Tick))
}

func aa24PutSlot64(aa24Slot []byte, aa24Offset int, aa24Value uint64) {
	aa24binarytest.LittleEndian.PutUint64(aa24Slot[aa24Offset:], aa24Value)
}

func TestAA24LayoutWritesTheDocumentedBytes(aa24T *aa24testing.T) {
	aa24Mem, aa24Expected := aa24FixtureMapping(aa24T)
	aa24Layout, aa24Err := AA24ValidateMapping(aa24Mem, aa24Expected)
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	aa24Get32 := aa24binarytest.LittleEndian.Uint32
	aa24Get64 := aa24binarytest.LittleEndian.Uint64
	if string(aa24Mem[0:4]) != "LWB1" || aa24Get32(aa24Mem[4:]) != 1 || aa24Get32(aa24Mem[8:]) != 64 ||
		int(aa24Get32(aa24Mem[12:])) != len(aa24Mem) || aa24Get64(aa24Mem[16:]) != 11 || aa24Get64(aa24Mem[24:]) != 21 ||
		aa24Get32(aa24Mem[32:]) != 4 || aa24Get32(aa24Mem[36:]) != 64 || aa24Get64(aa24Mem[40:]) != 64 {
		aa24T.Fatalf("header bytes: % x", aa24Mem[:64])
	}
	aa24ArenaOffset := aa24Get64(aa24Mem[48:])
	if aa24ArenaOffset != aa24Layout.AA24ArenaOffset || aa24Get64(aa24Mem[56:]) != aa24FixtureArenaBytes ||
		aa24ArenaOffset+aa24FixtureArenaBytes != uint64(len(aa24Mem)) {
		aa24T.Fatalf("arena: offset %d layout %+v", aa24ArenaOffset, aa24Layout)
	}
	aa24Build := aa24FixtureBuild()
	if string(aa24Mem[aa24ArenaOffset:aa24ArenaOffset+32]) != string(aa24Build[:]) {
		aa24T.Fatal("build identity is not at the arena start")
	}
	aa24Cursor := uint64(64 + 4*64)
	for aa24Index := 0; aa24Index < 4; aa24Index++ {
		aa24Desc := aa24Mem[64+aa24Index*64:]
		if int(aa24Get32(aa24Desc)) != aa24Index+1 || aa24Get32(aa24Desc[4:]) != aa24FixtureSlotBytes ||
			aa24Get32(aa24Desc[8:]) != aa24FixtureConfig.AA24Lanes[aa24Index].AA24Capacity ||
			aa24Get32(aa24Desc[12:]) != 0 || aa24Get64(aa24Desc[16:]) != aa24Cursor ||
			aa24Get64(aa24Desc[24:]) != 0 || aa24Get64(aa24Desc[32:]) != 0 {
			aa24T.Fatalf("descriptor %d: % x", aa24Index, aa24Desc[:64])
		}
		aa24Cursor += uint64(aa24FixtureSlotBytes) * uint64(aa24FixtureConfig.AA24Lanes[aa24Index].AA24Capacity)
	}
}

func TestAA24ValidateRefusesEachLayoutFieldBeforeAccess(aa24T *aa24testing.T) {
	aa24Put32 := aa24binarytest.LittleEndian.PutUint32
	aa24Put64 := aa24binarytest.LittleEndian.PutUint64
	aa24Desc := func(aa24Index int) int { return 64 + aa24Index*64 }
	aa24Region := func(aa24Mem []byte, aa24Index int) uint64 {
		return aa24binarytest.LittleEndian.Uint64(aa24Mem[aa24Desc(aa24Index)+16:])
	}
	aa24Cases := []struct {
		aa24Name   string
		aa24Mutate func([]byte)
		aa24Err    error
	}{
		{"magic", func(aa24Mem []byte) { aa24Mem[3] = 'X' }, ErrAA24ABI},
		{"version", func(aa24Mem []byte) { aa24Put32(aa24Mem[4:], 2) }, ErrAA24ABI},
		{"header bytes", func(aa24Mem []byte) { aa24Put32(aa24Mem[8:], 72) }, ErrAA24ABI},
		{"total bytes", func(aa24Mem []byte) { aa24Put32(aa24Mem[12:], uint32(len(aa24Mem))-8) }, ErrAA24MappingSize},
		{"worker", func(aa24Mem []byte) { aa24Put64(aa24Mem[16:], 12) }, ErrAA24Identity},
		{"stream", func(aa24Mem []byte) { aa24Put64(aa24Mem[24:], 22) }, ErrAA24Identity},
		{"lane count", func(aa24Mem []byte) { aa24Put32(aa24Mem[32:], 3) }, ErrAA24ABI},
		{"descriptor bytes", func(aa24Mem []byte) { aa24Put32(aa24Mem[36:], 72) }, ErrAA24ABI},
		{"descriptor misaligned", func(aa24Mem []byte) { aa24Put64(aa24Mem[40:], 68) }, ErrAA24Alignment},
		{"descriptor out of bounds", func(aa24Mem []byte) { aa24Put64(aa24Mem[40:], uint64(len(aa24Mem))) }, ErrAA24Bounds},
		{"descriptor sum overflow", func(aa24Mem []byte) { aa24Put64(aa24Mem[40:], aa24mathtest.MaxUint64-7) }, ErrAA24Bounds},
		{"arena misaligned", func(aa24Mem []byte) {
			aa24Put64(aa24Mem[48:], aa24binarytest.LittleEndian.Uint64(aa24Mem[48:])+4)
		}, ErrAA24Alignment},
		{"arena out of bounds", func(aa24Mem []byte) { aa24Put64(aa24Mem[48:], uint64(len(aa24Mem))) }, ErrAA24Bounds},
		{"arena sum overflow", func(aa24Mem []byte) { aa24Put64(aa24Mem[48:], aa24mathtest.MaxUint64-7) }, ErrAA24Bounds},
		{"arena bytes zero", func(aa24Mem []byte) { aa24Put64(aa24Mem[56:], 0) }, ErrAA24Bounds},
		{"arena only build", func(aa24Mem []byte) { aa24Put64(aa24Mem[56:], 32) }, ErrAA24Bounds},
		{"arena bytes over limit", func(aa24Mem []byte) { aa24Put64(aa24Mem[56:], 2048) }, ErrAA24Bounds},
		{"arena over slot region", func(aa24Mem []byte) {
			aa24Target := aa24Region(aa24Mem, 3)
			aa24Arena := aa24binarytest.LittleEndian.Uint64(aa24Mem[48:])
			copy(aa24Mem[aa24Target:], aa24Mem[aa24Arena:aa24Arena+32])
			aa24Put64(aa24Mem[48:], aa24Target)
		}, ErrAA24Overlap},
		{"lane kind unknown", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(0):], 5) }, ErrAA24Lane},
		{"lane kind zero", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(0):], 0) }, ErrAA24Lane},
		{"lane kind duplicate", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(1):], 1) }, ErrAA24Lane},
		{"slot bytes below envelope", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(2)+4:], 120) }, ErrAA24Lane},
		{"slot bytes not multiple", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(2)+4:], 132) }, ErrAA24Lane},
		{"slot bytes over limit", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(2)+4:], 264) }, ErrAA24Lane},
		{"capacity zero", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(2)+8:], 0) }, ErrAA24Lane},
		{"capacity over limit", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(2)+8:], 65) }, ErrAA24Lane},
		{"host control without reserved room", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(0)+8:], 1) }, ErrAA24Lane},
		{"worker control without reserved room", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(1)+8:], 1) }, ErrAA24Lane},
		{"capacity past mapping", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(3)+8:], 64) }, ErrAA24Bounds},
		{"reserved word", func(aa24Mem []byte) { aa24Put32(aa24Mem[aa24Desc(1)+12:], 1) }, ErrAA24Reserved},
		{"reserved tail first", func(aa24Mem []byte) { aa24Mem[aa24Desc(1)+40] = 1 }, ErrAA24Reserved},
		{"reserved tail last", func(aa24Mem []byte) { aa24Mem[aa24Desc(1)+63] = 1 }, ErrAA24Reserved},
		{"slot region misaligned", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(2)+16:], aa24Region(aa24Mem, 2)+4) }, ErrAA24Alignment},
		{"slot region out of bounds", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(2)+16:], uint64(len(aa24Mem))) }, ErrAA24Bounds},
		{"slot region sum overflow", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(2)+16:], aa24mathtest.MaxUint64-7) }, ErrAA24Bounds},
		{"slot region over header", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(2)+16:], 0) }, ErrAA24Overlap},
		{"slot region over descriptors", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(2)+16:], 64) }, ErrAA24Overlap},
		{"slot region over neighbour", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(2)+16:], aa24Region(aa24Mem, 1)) }, ErrAA24Overlap},
		{"slot region one word into neighbour", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(2)+16:], aa24Region(aa24Mem, 2)+8) }, ErrAA24Overlap},
		{"producer index", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(3)+24:], 1) }, ErrAA24Index},
		{"consumer index", func(aa24Mem []byte) { aa24Put64(aa24Mem[aa24Desc(3)+32:], 1) }, ErrAA24Index},
		{"build first byte", func(aa24Mem []byte) { aa24Mem[aa24binarytest.LittleEndian.Uint64(aa24Mem[48:])] ^= 1 }, ErrAA24Build},
		{"build last byte", func(aa24Mem []byte) { aa24Mem[aa24binarytest.LittleEndian.Uint64(aa24Mem[48:])+31] ^= 1 }, ErrAA24Build},
	}
	for _, aa24Case := range aa24Cases {
		aa24T.Run(aa24Case.aa24Name, func(aa24T *aa24testing.T) {
			aa24Mem, aa24Expected := aa24FixtureMapping(aa24T)
			if _, aa24Err := AA24ValidateMapping(aa24Mem, aa24Expected); aa24Err != nil {
				aa24T.Fatalf("positive control: %v", aa24Err)
			}
			aa24Case.aa24Mutate(aa24Mem)
			_, aa24Err := AA24ValidateMapping(aa24Mem, aa24Expected)
			aa24Want(aa24T, aa24Case.aa24Name, aa24Err, aa24Case.aa24Err)
			_, aa24Err = AA24AttachHost(aa24Mem, aa24Expected)
			aa24Want(aa24T, "host attach", aa24Err, aa24Case.aa24Err)
			_, aa24Err = AA24AttachWorker(aa24Mem, aa24Expected)
			aa24Want(aa24T, "worker attach", aa24Err, aa24Case.aa24Err)
		})
	}
}

func TestAA24ValidateDescriptorTablePlacement(aa24T *aa24testing.T) {
	aa24Mem, aa24Expected := aa24FixtureMapping(aa24T)
	aa24Table := append([]byte(nil), aa24Mem[64:64+4*64]...)
	aa24Target := aa24binarytest.LittleEndian.Uint64(aa24Table[3*64+16:])
	copy(aa24Mem[aa24Target:], aa24Table)
	aa24binarytest.LittleEndian.PutUint64(aa24Mem[40:], aa24Target)
	_, aa24Err := AA24ValidateMapping(aa24Mem, aa24Expected)
	aa24Want(aa24T, "table over a slot region", aa24Err, ErrAA24Overlap)

	// Header bytes never decode as a valid descriptor, so a table placed over the
	// header is refused while its first descriptor is read.
	aa24Placements := []struct {
		aa24Offset uint64
		aa24Err    error
	}{
		{0, ErrAA24Lane}, {8, ErrAA24Lane}, {16, ErrAA24Lane}, {24, ErrAA24Lane},
		{32, ErrAA24Reserved}, {40, ErrAA24Lane}, {48, ErrAA24Lane}, {56, ErrAA24Lane},
	}
	for _, aa24Placement := range aa24Placements {
		aa24Mem, aa24Expected = aa24FixtureMapping(aa24T)
		aa24binarytest.LittleEndian.PutUint64(aa24Mem[40:], aa24Placement.aa24Offset)
		_, aa24Err = AA24ValidateMapping(aa24Mem, aa24Expected)
		aa24Want(aa24T, "table over the header", aa24Err, aa24Placement.aa24Err)
	}
}

func TestAA24ValidateVariesExpectedIdentity(aa24T *aa24testing.T) {
	aa24Mem, aa24Expected := aa24FixtureMapping(aa24T)
	aa24Cases := []struct {
		aa24Name   string
		aa24Mutate func(*AA24Expected)
		aa24Err    error
	}{
		{"build", func(aa24E *AA24Expected) { aa24E.AA24Binding.AA24Build[31] ^= 1 }, ErrAA24Build},
		{"zero build", func(aa24E *AA24Expected) { aa24E.AA24Binding.AA24Build = AA24BuildIdentity{} }, ErrAA24Build},
		{"worker", func(aa24E *AA24Expected) { aa24E.AA24Binding.AA24Worker++ }, ErrAA24Identity},
		{"zero worker", func(aa24E *AA24Expected) { aa24E.AA24Binding.AA24Worker = 0 }, ErrAA24Identity},
		{"stream", func(aa24E *AA24Expected) { aa24E.AA24Binding.AA24Stream++ }, ErrAA24Identity},
		{"zero stream", func(aa24E *AA24Expected) { aa24E.AA24Binding.AA24Stream = 0 }, ErrAA24Identity},
		{"mapping bytes", func(aa24E *AA24Expected) { aa24E.AA24MappingBytes += 8 }, ErrAA24MappingSize},
		{"mapping limit", func(aa24E *AA24Expected) { aa24E.AA24Limits.AA24MaxMappingBytes = 64 }, ErrAA24MappingSize},
		{"arena limit", func(aa24E *AA24Expected) { aa24E.AA24Limits.AA24MaxArenaBytes = 64 }, ErrAA24Bounds},
		{"slot limit", func(aa24E *AA24Expected) {
			aa24E.AA24Limits.AA24MaxSlotBytes = 144
			aa24E.AA24Limits.AA24MaxPayloadBytes = 16
		}, ErrAA24Lane},
		{"capacity limit", func(aa24E *AA24Expected) { aa24E.AA24Limits.AA24MaxCapacity = 2 }, ErrAA24Lane},
		{"limit zero", func(aa24E *AA24Expected) { aa24E.AA24Limits.AA24MaxPlans = 0 }, ErrAA24InvalidLimits},
	}
	for _, aa24Case := range aa24Cases {
		aa24Variant := aa24Expected
		aa24Case.aa24Mutate(&aa24Variant)
		_, aa24Err := AA24ValidateMapping(aa24Mem, aa24Variant)
		aa24Want(aa24T, aa24Case.aa24Name, aa24Err, aa24Case.aa24Err)
	}
	if _, aa24Err := AA24ValidateMapping(aa24Mem, aa24Expected); aa24Err != nil {
		aa24T.Fatalf("positive control after variants: %v", aa24Err)
	}
}

func TestAA24ValidateRefusesBadBaseAndShortMappings(aa24T *aa24testing.T) {
	aa24Mem, aa24Expected := aa24FixtureMapping(aa24T)
	aa24Shifted := aa24FixtureMemory(aa24FixtureBytes() + 8)[1 : aa24FixtureBytes()+1]
	copy(aa24Shifted, aa24Mem)
	_, aa24Err := AA24ValidateMapping(aa24Shifted, aa24Expected)
	aa24Want(aa24T, "misaligned base", aa24Err, ErrAA24Alignment)
	_, aa24Err = AA24ValidateMapping(nil, aa24Expected)
	aa24Want(aa24T, "empty", aa24Err, ErrAA24Alignment)
	_, aa24Err = AA24ValidateMapping(aa24Mem[:32], aa24Expected)
	aa24Want(aa24T, "shorter than the header", aa24Err, ErrAA24MappingSize)
	_, aa24Err = AA24ValidateMapping(aa24Mem[:len(aa24Mem)-8], aa24Expected)
	aa24Want(aa24T, "truncated mapping", aa24Err, ErrAA24MappingSize)
}

func TestAA24InitRefusalLeavesMemoryUntouched(aa24T *aa24testing.T) {
	aa24Cases := []struct {
		aa24Name   string
		aa24Mutate func(*AA24Binding, *AA24Config, *AA24Limits)
		aa24Err    error
	}{
		{"zero build", func(aa24B *AA24Binding, _ *AA24Config, _ *AA24Limits) { aa24B.AA24Build = AA24BuildIdentity{} }, ErrAA24Build},
		{"zero worker", func(aa24B *AA24Binding, _ *AA24Config, _ *AA24Limits) { aa24B.AA24Worker = 0 }, ErrAA24Identity},
		{"zero stream", func(aa24B *AA24Binding, _ *AA24Config, _ *AA24Limits) { aa24B.AA24Stream = 0 }, ErrAA24Identity},
		{"control capacity", func(_ *AA24Binding, aa24C *AA24Config, _ *AA24Limits) { aa24C.AA24Lanes[1].AA24Capacity = 1 }, ErrAA24Lane},
		{"slot bytes", func(_ *AA24Binding, aa24C *AA24Config, _ *AA24Limits) { aa24C.AA24Lanes[3].AA24SlotBytes = 150 }, ErrAA24Lane},
		{"arena too small", func(_ *AA24Binding, aa24C *AA24Config, _ *AA24Limits) { aa24C.AA24ArenaBytes = 32 }, ErrAA24InvalidLimits},
		{"arena misaligned", func(_ *AA24Binding, aa24C *AA24Config, _ *AA24Limits) { aa24C.AA24ArenaBytes = 100 }, ErrAA24InvalidLimits},
		{"size mismatch", func(_ *AA24Binding, aa24C *AA24Config, _ *AA24Limits) { aa24C.AA24ArenaBytes = 104 }, ErrAA24MappingSize},
		{"mapping over limit", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxMappingBytes = 64 }, ErrAA24MappingSize},
		{"limit mapping", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxMappingBytes = 0 }, ErrAA24InvalidLimits},
		{"limit capacity", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxCapacity = 0 }, ErrAA24InvalidLimits},
		{"limit capacity ceiling", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) {
			aa24L.AA24MaxCapacity = aa24CapacityCeiling + 1
		}, ErrAA24InvalidLimits},
		{"limit payload", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxPayloadBytes = 0 }, ErrAA24InvalidLimits},
		{"limit payload over slot", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxPayloadBytes = 129 }, ErrAA24InvalidLimits},
		{"limit slot", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxSlotBytes = 127 }, ErrAA24InvalidLimits},
		{"limit arena", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxArenaBytes = 0 }, ErrAA24InvalidLimits},
		{"limit plans", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxPlans = 0 }, ErrAA24InvalidLimits},
		{"limit plans ceiling", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxPlans = aa24TableCeiling + 1 }, ErrAA24InvalidLimits},
		{"limit outstanding", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24MaxOutstanding = 0 }, ErrAA24InvalidLimits},
		{"limit budget", func(_ *AA24Binding, _ *AA24Config, aa24L *AA24Limits) { aa24L.AA24ControlServiceTicks = 0 }, ErrAA24InvalidLimits},
	}
	for _, aa24Case := range aa24Cases {
		aa24Binding, aa24Config, aa24Limits := aa24FixtureBinding(), aa24FixtureConfig, aa24FixtureLimits
		aa24Case.aa24Mutate(&aa24Binding, &aa24Config, &aa24Limits)
		aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
		for aa24Index := range aa24Mem {
			aa24Mem[aa24Index] = 0xA5
		}
		_, _, aa24Err := AA24InitMapping(aa24Mem, aa24Binding, aa24Config, aa24Limits)
		aa24Want(aa24T, aa24Case.aa24Name, aa24Err, aa24Case.aa24Err)
		for _, aa24Byte := range aa24Mem {
			if aa24Byte != 0xA5 {
				aa24T.Fatalf("%s: refusal changed caller memory", aa24Case.aa24Name)
			}
		}
	}
	aa24Shifted := aa24FixtureMemory(aa24FixtureBytes() + 8)[1 : aa24FixtureBytes()+1]
	_, _, aa24Err := AA24InitMapping(aa24Shifted, aa24FixtureBinding(), aa24FixtureConfig, aa24FixtureLimits)
	aa24Want(aa24T, "misaligned init", aa24Err, ErrAA24Alignment)
	for _, aa24Byte := range aa24Shifted {
		if aa24Byte != 0 {
			aa24T.Fatal("a misaligned init wrote caller memory")
		}
	}
}

func TestAA24EnvelopeBytesAndDetachedPayload(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24FixtureCommand(1, 1)
	aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, []byte{0xCA, 0xFE}, aa24At(10)), nil)
	aa24Slot := aa24Pair.aa24Host.aa24Lane(AA24LaneHostData).aa24Slot(0)
	aa24Get32 := aa24binarytest.LittleEndian.Uint32
	aa24Get64 := aa24binarytest.LittleEndian.Uint64
	aa24Fields := []struct {
		aa24Name   string
		aa24Offset int
		aa24Value  uint64
	}{
		{"worker", 8, 11}, {"stream", 16, 21}, {"transaction", 24, 1}, {"plan", 32, 1},
		{"subscriber", 40, 3}, {"epoch", 48, 5}, {"token", 56, 1}, {"owner expiry", 64, uint64(aa24FixtureOwner)},
		{"sequence", 72, 0}, {"capture", 80, 0}, {"plan expiry", 88, uint64(aa24FixtureExpiry)},
		{"deadline", 96, uint64(aa24FixtureDeadline)}, {"arena offset", 104, 0},
	}
	for _, aa24Field := range aa24Fields {
		if aa24Get64(aa24Slot[aa24Field.aa24Offset:]) != aa24Field.aa24Value {
			aa24T.Fatalf("%s at %d: %d", aa24Field.aa24Name, aa24Field.aa24Offset, aa24Get64(aa24Slot[aa24Field.aa24Offset:]))
		}
	}
	if aa24Get32(aa24Slot) != uint32(AA24RecordCommand) || aa24Get32(aa24Slot[4:]) != 2 || aa24Get32(aa24Slot[112:]) != 0 ||
		aa24Get32(aa24Slot[116:]) != 0 || aa24Slot[120] != aa24FixtureAddress || aa24Slot[121] != 0 || aa24Slot[122] != 0 ||
		aa24Slot[123] != 0 || aa24Get32(aa24Slot[124:]) != 0 || aa24Slot[128] != 0xCA || aa24Slot[129] != 0xFE || aa24Slot[130] != 0 {
		aa24T.Fatalf("envelope bytes: % x", aa24Slot)
	}
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(11))
	if aa24Err != nil || !aa24OK {
		aa24T.Fatalf("next: %v %v", aa24OK, aa24Err)
	}
	if !aa24Delivery.AA24Record.aa24Identity().aa24Matches(aa24Identity) {
		aa24T.Fatalf("identity: %+v", aa24Delivery.AA24Record)
	}
	clear(aa24Slot)
	if aa24Delivery.AA24Record.AA24Payload[0] != 0xCA || aa24Delivery.AA24Record.AA24Payload[1] != 0xFE {
		aa24T.Fatal("delivered payload aliases shared memory")
	}
}

func TestAA24MappingBytesAreRelocatableWithoutPointers(aa24T *aa24testing.T) {
	aa24Source := aa24NewPair(aa24T)
	aa24Target := aa24FixtureMemory(aa24FixtureBytes())
	aa24Expected := aa24Source.aa24Expected
	copy(aa24Target, aa24Source.aa24Mem)
	aa24Worker, aa24Err := AA24AttachWorker(aa24Target, aa24Expected)
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	aa24ArenaStart := aa24Source.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24Want(aa24T, "publish", aa24Source.aa24Host.AA24PublishPlan(aa24FixtureRef(1), aa24ArenaStart, 8, aa24At(10)), nil)
	aa24Want(aa24T, "send", aa24Source.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 1), []byte{7}, aa24At(10)), nil)
	copy(aa24Target, aa24Source.aa24Mem)
	for aa24Index := range aa24Source.aa24Mem {
		aa24Source.aa24Mem[aa24Index] = 0xFF
	}
	aa24Delivery, aa24OK, aa24Err := aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(11))
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Payload[0] != 7 {
		aa24T.Fatalf("copied bytes alone must carry the record: ok=%v err=%v %+v", aa24OK, aa24Err, aa24Delivery)
	}
}

func TestAA24RingOrderWrapAndCounters(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Sequence := uint64(0)
	for aa24Round := 0; aa24Round < 5; aa24Round++ {
		aa24Tick := AA23Tick(10 + aa24Round)
		for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
			aa24Got, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(aa24Tick))
			aa24Sequence++
			if aa24Err != nil || aa24Got != aa24Sequence {
				aa24T.Fatalf("observe %d: %d %v", aa24Sequence, aa24Got, aa24Err)
			}
		}
		for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
			aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(aa24Tick)
			if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24RawOctet != aa24Octet {
				aa24T.Fatalf("poll round %d: %+v %v %v", aa24Round, aa24Delivery, aa24OK, aa24Err)
			}
		}
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(aa24Tick); aa24OK || aa24Err != nil {
			aa24T.Fatalf("empty lane: %v %v", aa24OK, aa24Err)
		}
	}
	aa24Stats := aa24Pair.aa24Worker.AA24LaneStats(AA24LaneWorkerData)
	if aa24Stats.AA24Enqueued != 10 || aa24Stats.AA24HighWater != 2 || aa24Stats.AA24Refused != 0 {
		aa24T.Fatalf("producer stats: %+v", aa24Stats)
	}
	aa24Stats = aa24Pair.aa24Host.AA24LaneStats(AA24LaneWorkerData)
	if aa24Stats.AA24Dequeued != 10 || aa24Stats.AA24HighWater != 2 {
		aa24T.Fatalf("consumer stats: %+v", aa24Stats)
	}
	if !aa24Pair.aa24Host.AA24OutputOpen() || !aa24Pair.aa24Worker.AA24OutputOpen() {
		aa24T.Fatal("ordinary traffic fenced an endpoint")
	}
}

func TestAA24LaneOwnershipIsExclusive(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Record := AA24Record{AA24Kind: AA24RecordStop, AA24Worker: aa24FixtureWorker, AA24Stream: aa24FixtureStream}
	aa24Want(aa24T, "worker writes host control", aa24Pair.aa24Worker.aa24Lane(AA24LaneHostControl).aa24Write(&aa24Record, 0), ErrAA24Ownership)
	_, _, aa24Err := aa24Pair.aa24Host.aa24Lane(AA24LaneHostControl).aa24Read(nil)
	aa24Want(aa24T, "host reads its own control", aa24Err, ErrAA24Ownership)
	aa24Want(aa24T, "host writes worker data", aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData).aa24Write(&aa24Record, 0), ErrAA24Ownership)
	_, _, aa24Err = aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData).aa24Read(nil)
	aa24Want(aa24T, "worker reads its own data", aa24Err, ErrAA24Ownership)
}

func TestAA24LiveIndexInvariantsVaryProducerAndConsumer(aa24T *aa24testing.T) {
	// The consumer has seen producer 2 and released consumer 1. Each case breaks
	// exactly one invariant from that state.
	aa24Cases := []struct {
		aa24Name     string
		aa24Producer uint64
		aa24Consumer uint64
	}{
		{"occupancy above capacity", 4, 1},
		{"producer regression", 1, 1},
		{"consumer regression", 2, 0},
		{"consumer tampered forward", 2, 2},
		{"consumer ahead of producer", 2, 3},
	}
	for _, aa24Case := range aa24Cases {
		aa24T.Run(aa24Case.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			aa24Lane := aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData)
			for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
				if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(10)); aa24Err != nil {
					aa24T.Fatal(aa24Err)
				}
			}
			if _, aa24OK, aa24Err := aa24Pair.aa24Poll(10); !aa24OK || aa24Err != nil {
				aa24T.Fatalf("positive control: %v %v", aa24OK, aa24Err)
			}
			aa24StoreRelease(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ProducerOffset, aa24Case.aa24Producer)
			aa24StoreRelease(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ConsumerOffset, aa24Case.aa24Consumer)
			_, aa24OK, aa24Err := aa24Pair.aa24Poll(11)
			aa24Want(aa24T, "consumer view", aa24Err, ErrAA24Index)
			if aa24OK || aa24Pair.aa24Host.AA24OutputOpen() || aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceCorruption {
				aa24T.Fatalf("host stayed open: %+v", aa24Pair.aa24Host.AA24Fence())
			}
		})
	}
	aa24T.Run("occupancy above capacity from a clean consumer", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Lane := aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData)
		aa24StoreRelease(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ProducerOffset, 3)
		_, _, aa24Err := aa24Pair.aa24Poll(10)
		aa24Want(aa24T, "occupancy", aa24Err, ErrAA24Index)
	})
	aa24T.Run("producer sees consumer ahead", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Lane := aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData)
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(10)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
		aa24StoreRelease(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ConsumerOffset, 2)
		_, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(2), aa24At(10))
		aa24Want(aa24T, "consumer ahead", aa24Err, ErrAA24Index)
	})
	aa24T.Run("producer view", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Lane := aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData)
		aa24StoreRelease(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ProducerOffset, 1)
		_, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(10))
		aa24Want(aa24T, "own index tampered", aa24Err, ErrAA24Index)
		if aa24Pair.aa24Worker.AA24OutputOpen() {
			aa24T.Fatal("worker stayed open")
		}
	})
	aa24T.Run("producer sees consumer regress", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Lane := aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData)
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(10)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(10); !aa24OK || aa24Err != nil {
			aa24T.Fatalf("poll: %v %v", aa24OK, aa24Err)
		}
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(2), aa24At(10)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
		aa24StoreRelease(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ConsumerOffset, 0)
		_, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(3), aa24At(10))
		aa24Want(aa24T, "consumer regression", aa24Err, ErrAA24Index)
	})
}

func TestAA24CountersRefuseToWrap(aa24T *aa24testing.T) {
	aa24T.Run("lane index", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Lane := aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData)
		aa24Maximum := uint64(aa24mathtest.MaxUint64)
		aa24StoreRelease(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ProducerOffset, aa24Maximum)
		aa24StoreRelease(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ConsumerOffset, aa24Maximum)
		aa24Lane.aa24LocalProducer, aa24Lane.aa24LocalConsumer = aa24Maximum, aa24Maximum
		_, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(10))
		aa24Want(aa24T, "producer at maximum", aa24Err, ErrAA24Counter)
		if aa24LoadAcquire(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ProducerOffset) != aa24Maximum {
			aa24T.Fatal("producer index wrapped")
		}
		if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceCounter || aa24Pair.aa24Worker.AA24Lost() != 1 {
			aa24T.Fatalf("fence %+v lost %d", aa24Pair.aa24Worker.AA24Fence(), aa24Pair.aa24Worker.AA24Lost())
		}
	})
	aa24T.Run("capture sequence keeps the final ordinal for the gap", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Worker.aa24NextSequence = aa24mathtest.MaxUint64 - 1
		aa24Pair.aa24Host.aa24NextCapture = aa24mathtest.MaxUint64 - 1
		aa24Sequence, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(10))
		if aa24Err != nil || aa24Sequence != aa24mathtest.MaxUint64-1 {
			aa24T.Fatalf("last ordinary ordinal: %d %v", aa24Sequence, aa24Err)
		}
		_, aa24Err = aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(2), aa24At(11))
		aa24Want(aa24T, "exhausted", aa24Err, ErrAA24Counter)
		aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(12)
		if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordGap ||
			aa24Delivery.AA24Record.AA24CaptureSequence != aa24mathtest.MaxUint64 {
			aa24T.Fatalf("gap: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
		}
		if aa24Pair.aa24Host.aa24NextCapture == 0 {
			aa24T.Fatal("host capture cursor wrapped")
		}
	})
	aa24T.Run("host capture cursor stops at the final ordinal", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Host := aa24Pair.aa24Host
		aa24Host.aa24NextCapture = aa24mathtest.MaxUint64
		if !aa24Host.aa24AccountSequence(aa24mathtest.MaxUint64, true) || aa24Host.aa24NextCapture != aa24mathtest.MaxUint64 {
			aa24T.Fatalf("direct: next %d", aa24Host.aa24NextCapture)
		}
		aa24Host.aa24NextCapture = aa24mathtest.MaxUint64 - 1
		if !aa24Host.aa24AccountSequence(aa24mathtest.MaxUint64, true) || !aa24Host.aa24AccountSequence(aa24mathtest.MaxUint64-1, false) ||
			aa24Host.aa24NextCapture != aa24mathtest.MaxUint64 {
			aa24T.Fatalf("collapse: next %d", aa24Host.aa24NextCapture)
		}
	})
	aa24T.Run("loss counter saturates and fences", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Worker.aa24Lost = aa24mathtest.MaxUint64
		for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
			if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(10)); aa24Err != nil {
				aa24T.Fatal(aa24Err)
			}
		}
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(2), aa24At(10)); !aa24errorstest.Is(aa24Err, ErrAA24Gap) {
			aa24T.Fatalf("symbol past the lane: got %v, want %v", aa24Err, ErrAA24Gap)
		}
		if aa24Pair.aa24Worker.AA24Lost() != aa24mathtest.MaxUint64 || aa24Pair.aa24Worker.AA24OutputOpen() {
			aa24T.Fatalf("lost %d open %v", aa24Pair.aa24Worker.AA24Lost(), aa24Pair.aa24Worker.AA24OutputOpen())
		}
	})
	aa24T.Run("lane statistic saturates and fences", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Lane := aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData)
		aa24Lane.aa24Stats.AA24Enqueued = aa24mathtest.MaxUint64
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(10)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
		if aa24Lane.aa24Stats.AA24Enqueued != aa24mathtest.MaxUint64 {
			aa24T.Fatal("statistic wrapped")
		}
		aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(11)), nil)
		if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceCounter {
			aa24T.Fatalf("fence %+v", aa24Pair.aa24Worker.AA24Fence())
		}
		aa24Host := aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData)
		aa24Host.aa24Stats.AA24Dequeued = aa24mathtest.MaxUint64
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(12); aa24Err != nil || !aa24OK {
			aa24T.Fatalf("host poll: %v %v", aa24OK, aa24Err)
		}
		if _, _, aa24Err := aa24Pair.aa24Poll(13); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
		if aa24Pair.aa24Host.AA24OutputOpen() {
			aa24T.Fatal("host stayed open after a saturated statistic")
		}
	})
}

func TestAA24TruncatedAndOversizeRecordsFenceBeforeUse(aa24T *aa24testing.T) {
	aa24Cases := []struct {
		aa24Name   string
		aa24Mutate func([]byte)
		aa24Err    error
	}{
		{"payload past the slot", func(aa24Slot []byte) { aa24binarytest.LittleEndian.PutUint32(aa24Slot[4:], 25) }, ErrAA24Truncated},
		{"payload length huge", func(aa24Slot []byte) { aa24binarytest.LittleEndian.PutUint32(aa24Slot[4:], aa24mathtest.MaxUint32) }, ErrAA24Truncated},
		{"payload above the cap", func(aa24Slot []byte) { aa24binarytest.LittleEndian.PutUint32(aa24Slot[4:], 17) }, ErrAA24Oversize},
		{"padding not zero", func(aa24Slot []byte) { aa24Slot[151] = 1 }, ErrAA24Record},
		{"unknown kind", func(aa24Slot []byte) { aa24binarytest.LittleEndian.PutUint32(aa24Slot, 9) }, ErrAA24Record},
		{"wrong-lane kind", func(aa24Slot []byte) {
			aa24binarytest.LittleEndian.PutUint32(aa24Slot, uint32(AA24RecordStop))
		}, ErrAA24Record},
		{"flags", func(aa24Slot []byte) { aa24binarytest.LittleEndian.PutUint32(aa24Slot[124:], 1) }, ErrAA24Record},
	}
	for _, aa24Case := range aa24Cases {
		aa24T.Run(aa24Case.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(10)); aa24Err != nil {
				aa24T.Fatal(aa24Err)
			}
			aa24Lane := aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData)
			aa24Case.aa24Mutate(aa24Lane.aa24Slot(0))
			_, aa24OK, aa24Err := aa24Pair.aa24Poll(11)
			aa24Want(aa24T, aa24Case.aa24Name, aa24Err, aa24Case.aa24Err)
			if aa24OK || aa24Lane.aa24LocalConsumer != 0 ||
				aa24LoadAcquire(aa24Pair.aa24Mem, aa24Lane.aa24Layout.aa24ConsumerOffset) != 0 {
				aa24T.Fatal("a malformed record was consumed")
			}
			if aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceCorruption {
				aa24T.Fatalf("fence %+v", aa24Pair.aa24Host.AA24Fence())
			}
		})
	}
	aa24T.Run("short destination refuses without fencing", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 1), []byte{1, 2, 3}, aa24At(10)), nil)
		_, _, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 2), aa24At(11))
		aa24Want(aa24T, "short", aa24Err, ErrAA24Buffer)
		if !aa24Pair.aa24Worker.AA24OutputOpen() {
			aa24T.Fatal("short caller buffer fenced the stream")
		}
		if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 3), aa24At(11)); !aa24OK || aa24Err != nil {
			aa24T.Fatalf("retry: %v %v", aa24OK, aa24Err)
		}
	})
	aa24T.Run("oversize send has no effect", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		aa24Want(aa24T, "oversize", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 1), make([]byte, 17), aa24At(10)), ErrAA24Oversize)
		if !aa24Pair.aa24Host.AA24OutputOpen() || aa24Pair.aa24Host.aa24Lane(AA24LaneHostData).aa24LocalProducer != 0 {
			aa24T.Fatal("oversize send changed state")
		}
		aa24Want(aa24T, "same token retried", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 1), make([]byte, 16), aa24At(10)), nil)
	})
}

func TestAA24RecordShapeVariesEveryKindField(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Layout := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout
	aa24Identity := aa24FixtureCommand(1, 1)
	aa24ArenaStart := aa24Layout.AA24ArenaOffset + aa24BuildBytes
	aa24Stream := AA24Record{AA24Worker: aa24FixtureWorker, AA24Stream: aa24FixtureStream}
	aa24Build := func(aa24Kind AA24RecordKind, aa24Fill func(*AA24Record)) AA24Record {
		aa24Record := aa24Stream
		aa24Record.AA24Kind = aa24Kind
		aa24Fill(&aa24Record)
		return aa24Record
	}
	aa24Publish := aa24Build(AA24RecordPublish, func(aa24R *AA24Record) {
		aa24R.aa24ApplyRef(aa24Identity.AA24Ref)
		aa24R.AA24ArenaOffset, aa24R.AA24PlanBytes = aa24ArenaStart, 8
	})
	aa24Revoke := aa24Build(AA24RecordRevoke, func(aa24R *AA24Record) { aa24R.aa24ApplyRef(aa24Identity.AA24Ref) })
	aa24Ack := aa24Build(AA24RecordAcknowledge, func(aa24R *AA24Record) {
		aa24R.aa24ApplyRef(aa24Identity.AA24Ref)
		aa24R.AA24Status, aa24R.AA24CaptureSequence, aa24R.AA24CaptureTime = AA24StatusRetired, 1, 1
	})
	aa24Gap := aa24Build(AA24RecordGap, func(aa24R *AA24Record) { aa24R.AA24CaptureSequence, aa24R.AA24CaptureTime = 1, 1 })
	aa24Stop := aa24Build(AA24RecordStop, func(*AA24Record) {})
	aa24Command := aa24Build(AA24RecordCommand, func(aa24R *AA24Record) { aa24R.aa24ApplyIdentity(aa24Identity) })
	aa24Unselected := aa24Build(AA24RecordRaw, func(aa24R *AA24Record) {
		aa24R.AA24Transaction, aa24R.AA24Direction, aa24R.AA24CaptureSequence, aa24R.AA24CaptureTime = 1, AA24DirectionReceive, 1, 1
	})
	aa24Selected := aa24Build(AA24RecordRaw, func(aa24R *AA24Record) {
		aa24R.aa24ApplyIdentity(aa24Identity)
		aa24R.AA24Direction, aa24R.AA24CaptureSequence, aa24R.AA24CaptureTime = AA24DirectionEcho, 1, 1
	})
	aa24Completion := aa24Build(AA24RecordCompletion, func(aa24R *AA24Record) {
		aa24R.aa24ApplyIdentity(aa24Identity)
		aa24R.AA24Status, aa24R.AA24CaptureSequence, aa24R.AA24CaptureTime = AA24StatusAccepted, 1, 1
	})
	aa24Valid := []struct {
		aa24Name   string
		aa24Lane   AA24LaneKind
		aa24Record AA24Record
	}{
		{"publish", AA24LaneHostControl, aa24Publish},
		{"revoke", AA24LaneHostControl, aa24Revoke},
		{"stop", AA24LaneHostControl, aa24Stop},
		{"acknowledge", AA24LaneWorkerControl, aa24Ack},
		{"gap", AA24LaneWorkerControl, aa24Gap},
		{"command", AA24LaneHostData, aa24Command},
		{"raw unselected", AA24LaneWorkerData, aa24Unselected},
		{"raw selected", AA24LaneWorkerData, aa24Selected},
		{"completion", AA24LaneWorkerData, aa24Completion},
	}
	// Each field flips between zero and nonzero, or takes a value no kind allows, so
	// no field stays a valid value for its kind. Exact nonzero comparison is tested
	// by the identity tests.
	aa24Mutations := []struct {
		aa24Name   string
		aa24Mutate func(*AA24Record)
	}{
		{"worker", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24Worker) }},
		{"stream", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24Stream) }},
		{"transaction", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24Transaction) }},
		{"plan", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24Plan) }},
		{"subscriber", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24Subscriber) }},
		{"epoch", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24Epoch) }},
		{"token", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24OwnerToken) }},
		{"owner expiry", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24OwnerExpiry) }},
		{"sequence", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24CaptureSequence) }},
		{"capture", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24CaptureTime) }},
		{"plan expiry", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24PlanExpiry) }},
		{"deadline", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24RequestDeadline) }},
		{"arena offset", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24ArenaOffset) }},
		{"plan bytes", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24PlanBytes) }},
		{"target", func(aa24R *AA24Record) { aa24Flip(&aa24R.AA24Target) }},
		{"status", func(aa24R *AA24Record) { aa24R.AA24Status = 4 }},
		{"direction", func(aa24R *AA24Record) { aa24R.AA24Direction = 3 }},
		{"phase", func(aa24R *AA24Record) { aa24R.AA24Phase = 6 }},
		{"flags", func(aa24R *AA24Record) { aa24R.AA24Flags = 1 }},
	}
	for _, aa24Case := range aa24Valid {
		aa24Original := aa24Case.aa24Record
		if !aa24Original.aa24Shape(aa24Case.aa24Lane, aa24Layout) {
			aa24T.Fatalf("%s: positive control refused: %+v", aa24Case.aa24Name, aa24Original)
		}
		for _, aa24Mutation := range aa24Mutations {
			aa24Variant := aa24Original
			aa24Mutation.aa24Mutate(&aa24Variant)
			if aa24Variant.aa24Shape(aa24Case.aa24Lane, aa24Layout) {
				aa24T.Fatalf("%s: %s change admitted: %+v", aa24Case.aa24Name, aa24Mutation.aa24Name, aa24Variant)
			}
		}
		for _, aa24Other := range []AA24LaneKind{AA24LaneHostControl, AA24LaneWorkerControl, AA24LaneHostData, AA24LaneWorkerData} {
			if aa24Other != aa24Case.aa24Lane && aa24Original.aa24Shape(aa24Other, aa24Layout) {
				aa24T.Fatalf("%s admitted on lane %d", aa24Case.aa24Name, aa24Other)
			}
		}
	}
	for _, aa24Case := range aa24Valid[:5] {
		aa24Variants := []func(*AA24Record){
			func(aa24R *AA24Record) { aa24R.AA24Direction = AA24DirectionReceive },
			func(aa24R *AA24Record) { aa24R.AA24Phase = 1 },
			func(aa24R *AA24Record) { aa24R.AA24RawOctet = 1 },
		}
		for aa24Index, aa24Mutate := range aa24Variants {
			aa24Variant := aa24Case.aa24Record
			aa24Mutate(&aa24Variant)
			if aa24Variant.aa24Shape(aa24Case.aa24Lane, aa24Layout) {
				aa24T.Fatalf("%s: control variant %d admitted", aa24Case.aa24Name, aa24Index)
			}
		}
	}
	for _, aa24Original := range []AA24Record{aa24Publish, aa24Revoke, aa24Gap, aa24Stop} {
		aa24Variant := aa24Original
		aa24Variant.AA24Status = AA24StatusAccepted
		aa24Lane := AA24LaneHostControl
		if aa24Original.AA24Kind == AA24RecordGap {
			aa24Lane = AA24LaneWorkerControl
		}
		if aa24Variant.aa24Shape(aa24Lane, aa24Layout) {
			aa24T.Fatalf("kind %d: status admitted", aa24Original.AA24Kind)
		}
	}
	aa24Bad := aa24Publish
	aa24Bad.AA24ArenaOffset = aa24Layout.AA24ArenaOffset
	if aa24Bad.aa24Shape(AA24LaneHostControl, aa24Layout) {
		aa24T.Fatal("plan reference over the build identity admitted")
	}
	aa24Bad = aa24Publish
	aa24Bad.AA24ArenaOffset, aa24Bad.AA24PlanBytes = aa24Layout.AA24ArenaOffset+aa24FixtureArenaBytes-4, 8
	if aa24Bad.aa24Shape(AA24LaneHostControl, aa24Layout) {
		aa24T.Fatal("plan reference past the arena admitted")
	}
	aa24Bad = aa24Publish
	aa24Bad.AA24ArenaOffset = aa24mathtest.MaxUint64 - 2
	if aa24Bad.aa24Shape(AA24LaneHostControl, aa24Layout) {
		aa24T.Fatal("overflowing plan reference admitted")
	}
	aa24Bad = aa24Publish
	aa24Bad.AA24Target = 0xFE
	if aa24Bad.aa24Shape(AA24LaneHostControl, aa24Layout) {
		aa24T.Fatal("broadcast target admitted")
	}
	aa24Bad = aa24Selected
	aa24Bad.AA24Payload = []byte{1}
	if aa24Bad.aa24Shape(AA24LaneWorkerData, aa24Layout) {
		aa24T.Fatal("raw event payload admitted")
	}
	aa24Bad = aa24Unselected
	aa24Bad.AA24Direction = AA24DirectionControl
	if aa24Bad.aa24Shape(AA24LaneWorkerData, aa24Layout) {
		aa24T.Fatal("raw event without a direction admitted")
	}
	aa24Bad = aa24Completion
	aa24Bad.AA24Status = AA24StatusPending
	if aa24Bad.aa24Shape(AA24LaneWorkerData, aa24Layout) {
		aa24T.Fatal("pending completion admitted")
	}
	aa24Bad = aa24Ack
	aa24Bad.AA24Status = AA24StatusAccepted
	if aa24Bad.aa24Shape(AA24LaneWorkerControl, aa24Layout) {
		aa24T.Fatal("acknowledgement without retirement admitted")
	}
}

func aa24Flip[aa24Value ~uint8 | ~uint32 | ~uint64](aa24Field *aa24Value) {
	if *aa24Field == 0 {
		*aa24Field = 1
		return
	}
	*aa24Field = 0
}

func TestAA24FullDataLaneRaisesGapOutsideTheLane(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
	for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(20)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
	}
	aa24Sequence, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(3), aa24At(30))
	aa24Want(aa24T, "full lane", aa24Err, ErrAA24Gap)
	if aa24Sequence != 3 || aa24Pair.aa24Worker.AA24Lost() != 1 || aa24Pair.aa24Worker.AA24OutputOpen() {
		aa24T.Fatalf("seq %d lost %d open %v", aa24Sequence, aa24Pair.aa24Worker.AA24Lost(), aa24Pair.aa24Worker.AA24OutputOpen())
	}
	if aa24Stats := aa24Pair.aa24Worker.AA24LaneStats(AA24LaneWorkerData); aa24Stats.AA24Refused != 1 || aa24Stats.AA24HighWater != 2 {
		aa24T.Fatalf("stats %+v", aa24Stats)
	}
	aa24Want(aa24T, "selection fenced", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(30)), ErrAA24OutputFenced)
	_, aa24Err = aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(4), aa24At(31))
	aa24Want(aa24T, "sampling after the gap returns", aa24Err, ErrAA24OutputFenced)
	if aa24Pair.aa24Worker.AA24Lost() != 2 {
		aa24T.Fatalf("lost %d", aa24Pair.aa24Worker.AA24Lost())
	}
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(40)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordGap ||
		aa24Delivery.AA24Record.AA24CaptureSequence != 3 || aa24Delivery.AA24Record.AA24CaptureTime != 30 ||
		aa24Delivery.AA24DequeueTime != 40 {
		aa24T.Fatalf("gap comes first with the lost symbol's identity: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	aa24Fence := aa24Pair.aa24Host.AA24Fence()
	if aa24Fence != (AA24FenceState{AA24Reason: AA24FenceWorkerGap, AA24Sequence: 3, AA24CaptureTime: 30}) {
		aa24T.Fatalf("fence %+v", aa24Fence)
	}
	aa24Cancelled := aa24Pair.aa24Host.AA24CancelledCommands(nil)
	if len(aa24Cancelled) != 1 || !aa24Cancelled[0].aa24Matches(aa24Identity) {
		aa24T.Fatalf("cancelled %+v", aa24Cancelled)
	}
	if aa24Plans := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Plans) != 1 || !aa24Plans[0].aa24Matches(aa24Identity.AA24Ref) {
		aa24T.Fatalf("quarantined %+v", aa24Plans)
	}
	aa24Want(aa24T, "new command fenced", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 2), nil, aa24At(40)), ErrAA24OutputFenced)
	aa24Want(aa24T, "new publication fenced", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(2), 0, 0, aa24At(40)), ErrAA24OutputFenced)
	for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
		aa24Delivery, aa24OK, aa24Err = aa24Pair.aa24Poll(41)
		if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24RawOctet != aa24Octet {
			aa24T.Fatalf("retained evidence %d: %+v %v %v", aa24Octet, aa24Delivery, aa24OK, aa24Err)
		}
	}
	if aa24Pair.aa24Host.AA24OutputOpen() {
		aa24T.Fatal("draining evidence reopened output decisions")
	}
}

func TestAA24GapFitsAfterOrdinaryControlFillsItsShare(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Refs := make([]AA24PlanRef, 0, 3)
	for aa24Plan := AA23PlanGeneration(1); aa24Plan <= 3; aa24Plan++ {
		aa24Refs = append(aa24Refs, aa24Pair.aa24Publish(aa24Plan, 10))
	}
	for _, aa24Ref := range aa24Refs {
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(11)), nil)
		aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(11)), nil)
	}
	aa24Want(aa24T, "first ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Refs[0], aa24At(12)), nil)
	aa24Want(aa24T, "second ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Refs[1], aa24At(12)), nil)
	aa24Want(aa24T, "third ack would take the reserved slot", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Refs[2], aa24At(12)), ErrAA24ControlFull)
	if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceControlSaturated {
		aa24T.Fatalf("fence %+v", aa24Pair.aa24Worker.AA24Fence())
	}
	var aa24Kinds []AA24RecordKind
	for aa24Round := 0; aa24Round < 3; aa24Round++ {
		aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
		if aa24Err != nil || !aa24OK {
			aa24T.Fatalf("poll %d: %v %v", aa24Round, aa24OK, aa24Err)
		}
		aa24Kinds = append(aa24Kinds, aa24Delivery.AA24Record.AA24Kind)
	}
	if aa24Kinds[0] != AA24RecordAcknowledge || aa24Kinds[1] != AA24RecordAcknowledge || aa24Kinds[2] != AA24RecordGap {
		aa24T.Fatalf("kinds %v", aa24Kinds)
	}
	aa24Want(aa24T, "retried ack still exact", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Refs[2], aa24At(14)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(15)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Plan != 3 {
		aa24T.Fatalf("late ack: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	if aa24Plans := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Plans) != 0 {
		aa24T.Fatalf("plans stayed quarantined: %+v", aa24Plans)
	}
}

func TestAA24LatchedGapSurvivesAnOccupiedControlLane(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Control := aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerControl)
	aa24Filler := AA24Record{AA24Kind: AA24RecordGap, AA24Worker: aa24FixtureWorker, AA24Stream: aa24FixtureStream, AA24CaptureSequence: 1, AA24CaptureTime: 1}
	for aa24Round := 0; aa24Round < 3; aa24Round++ {
		aa24Want(aa24T, "fill", aa24Control.aa24Write(&aa24Filler, 0), nil)
	}
	aa24Pair.aa24Worker.aa24NextSequence = 2
	for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(20)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
	}
	if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(2), aa24At(20)); !aa24errorstest.Is(aa24Err, ErrAA24Gap) {
		aa24T.Fatalf("symbol past the lane: got %v, want %v", aa24Err, ErrAA24Gap)
	}
	if aa24Pair.aa24Worker.aa24GapWritten || !aa24Pair.aa24Worker.aa24GapLatched {
		aa24T.Fatal("gap must be latched while no slot exists")
	}
	for aa24Round := 0; aa24Round < 3; aa24Round++ {
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(21); aa24Err != nil || !aa24OK {
			aa24T.Fatalf("drain: %v %v", aa24OK, aa24Err)
		}
	}
	if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(9), aa24At(22)); !aa24errorstest.Is(aa24Err, ErrAA24OutputFenced) {
		aa24T.Fatalf("sampling while the gap flushes: got %v, want %v", aa24Err, ErrAA24OutputFenced)
	}
	if !aa24Pair.aa24Worker.aa24GapWritten {
		aa24T.Fatal("latched gap was not flushed")
	}
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(23)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordGap ||
		aa24Delivery.AA24Record.AA24CaptureSequence != 4 || aa24Delivery.AA24Record.AA24CaptureTime != 20 {
		aa24T.Fatalf("flushed gap: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

func TestAA24ControlNeverWaitsBehindDataBacklog(aa24T *aa24testing.T) {
	aa24T.Run("revocation overtakes queued commands", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Ref := aa24Pair.aa24Publish(1, 10)
		aa24Want(aa24T, "first", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 1), nil, aa24At(11)), nil)
		aa24Want(aa24T, "second", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 2), nil, aa24At(11)), nil)
		aa24Want(aa24T, "data lane full", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 3), nil, aa24At(11)), ErrAA24Full)
		if !aa24Pair.aa24Host.AA24OutputOpen() {
			aa24T.Fatal("a full data lane fenced the host")
		}
		aa24Want(aa24T, "revoke beside full data", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(12)), nil)
		_, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(13))
		aa24Want(aa24T, "queued command behind revocation", aa24Err, ErrAA24Revoked)
		if aa24OK {
			aa24T.Fatal("a queued command outran its revocation")
		}
	})
	aa24T.Run("revocation stops an already admitted command at the next symbol", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Ref := aa24Pair.aa24Publish(1, 10)
		aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
		aa24Want(aa24T, "positive control", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(11)), nil)
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(12)), nil)
		aa24Want(aa24T, "next symbol", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(12)), ErrAA24Revoked)
	})
	aa24T.Run("stop overtakes a saturated worker data lane", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
		for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
			if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(11)); aa24Err != nil {
				aa24T.Fatal(aa24Err)
			}
		}
		aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(12)), nil)
		aa24Want(aa24T, "next symbol", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(12)), ErrAA24OutputFenced)
		if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceStop {
			aa24T.Fatalf("fence %+v", aa24Pair.aa24Worker.AA24Fence())
		}
	})
	aa24T.Run("acknowledgement overtakes older data at the host", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Ref := aa24Pair.aa24Publish(1, 10)
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(10)), nil)
		aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(11)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
		aa24Want(aa24T, "ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(12)), nil)
		aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
		if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordAcknowledge || aa24Delivery.AA24Record.AA24CaptureSequence != 2 {
			aa24T.Fatalf("ack first: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
		}
		aa24Delivery, aa24OK, aa24Err = aa24Pair.aa24Poll(13)
		if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordRaw || aa24Delivery.AA24Record.AA24CaptureSequence != 1 {
			aa24T.Fatalf("older raw after: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
		}
		if !aa24Pair.aa24Host.AA24OutputOpen() || aa24Pair.aa24Host.aa24NextCapture != 3 || len(aa24Pair.aa24Host.aa24Explained) != 0 {
			aa24T.Fatalf("interleaving is not a gap: %+v next %d", aa24Pair.aa24Host.AA24Fence(), aa24Pair.aa24Host.aa24NextCapture)
		}
	})
}

func TestAA24CaptureTimeIsNeverDequeueTime(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
	if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(0x5A), aa24At(20)); aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(25)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(90)
	if aa24Err != nil || !aa24OK {
		aa24T.Fatalf("poll: %v %v", aa24OK, aa24Err)
	}
	if aa24Delivery.AA24Record.AA24CaptureTime != 20 || aa24Delivery.AA24DequeueTime != 90 || aa24Delivery.AA24Record.AA24CaptureSequence != 1 {
		aa24T.Fatalf("raw times: %+v", aa24Delivery)
	}
	aa24Delivery, aa24OK, aa24Err = aa24Pair.aa24Poll(95)
	if aa24Err != nil || !aa24OK {
		aa24T.Fatalf("poll: %v %v", aa24OK, aa24Err)
	}
	if aa24Delivery.AA24Record.AA24CaptureTime != 25 || aa24Delivery.AA24DequeueTime != 95 || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion {
		aa24T.Fatalf("completion times: %+v", aa24Delivery)
	}
	if len(aa24Pair.aa24Host.aa24Outstanding) != 0 {
		aa24T.Fatal("completion left the command outstanding")
	}
}

func TestAA24CaptureAndClockVariationsFence(aa24T *aa24testing.T) {
	aa24Observe := func(aa24T *aa24testing.T, aa24Pair *aa24Pair) {
		aa24T.Helper()
		for aa24Round := AA23Tick(0); aa24Round < 2; aa24Round++ {
			if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(20+aa24Round)); aa24Err != nil {
				aa24T.Fatal(aa24Err)
			}
		}
	}
	aa24T.Run("capture after dequeue", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Observe(aa24T, aa24Pair)
		_, aa24OK, aa24Err := aa24Pair.aa24Poll(15)
		aa24Want(aa24T, "impossible capture", aa24Err, ErrAA24Clock)
		if aa24OK || aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceClock {
			aa24T.Fatalf("fence %+v", aa24Pair.aa24Host.AA24Fence())
		}
	})
	aa24T.Run("capture regression", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Observe(aa24T, aa24Pair)
		aa24PutSlot64(aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData).aa24Slot(1), 80, 19)
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(30); aa24Err != nil || !aa24OK {
			aa24T.Fatalf("first: %v %v", aa24OK, aa24Err)
		}
		_, _, aa24Err := aa24Pair.aa24Poll(30)
		aa24Want(aa24T, "regression", aa24Err, ErrAA24Clock)
	})
	aa24T.Run("sequence duplicate", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Observe(aa24T, aa24Pair)
		aa24PutSlot64(aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData).aa24Slot(1), 72, 1)
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(30); aa24Err != nil || !aa24OK {
			aa24T.Fatalf("first: %v %v", aa24OK, aa24Err)
		}
		aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(30)
		aa24Want(aa24T, "duplicate", aa24Err, ErrAA24Gap)
		if !aa24OK || aa24Delivery.AA24Record.AA24CaptureSequence != 1 {
			aa24T.Fatalf("evidence is retained: %+v", aa24Delivery)
		}
		aa24Fence := aa24Pair.aa24Host.AA24Fence()
		if aa24Fence.AA24Reason != AA24FenceDiscontinuity || aa24Fence.AA24Sequence != 1 || aa24Fence.AA24CaptureTime != 21 {
			aa24T.Fatalf("fence %+v", aa24Fence)
		}
	})
	aa24T.Run("sequence skip", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Observe(aa24T, aa24Pair)
		aa24PutSlot64(aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData).aa24Slot(1), 72, 3)
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(30); aa24Err != nil || !aa24OK {
			aa24T.Fatalf("first: %v %v", aa24OK, aa24Err)
		}
		_, _, aa24Err := aa24Pair.aa24Poll(30)
		aa24Want(aa24T, "skip", aa24Err, ErrAA24Gap)
	})
	aa24T.Run("explained list is bounded", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Host := aa24Pair.aa24Host
		for aa24Sequence := uint64(2); aa24Sequence < 5; aa24Sequence++ {
			if !aa24Host.aa24AccountSequence(aa24Sequence, true) {
				aa24T.Fatalf("seq %d within the bound", aa24Sequence)
			}
		}
		if aa24Host.aa24AccountSequence(5, true) {
			aa24T.Fatal("explained list grew past its bound")
		}
		if !aa24Host.aa24AccountSequence(1, false) || aa24Host.aa24NextCapture != 5 {
			aa24T.Fatalf("catch-up: next %d", aa24Host.aa24NextCapture)
		}
	})
	aa24Clocks := []struct {
		aa24Name    string
		aa24Reading AA23ClockReading
	}{
		{"regression", aa24At(19)},
		{"other domain", AA23ClockReading{AA23Domain: aa24FixtureDomain + 1, AA23Tick: 30}},
		{"zero domain", AA23ClockReading{AA23Tick: 30}},
	}
	for _, aa24Clock := range aa24Clocks {
		aa24T.Run("worker clock "+aa24Clock.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(20)); aa24Err != nil {
				aa24T.Fatal(aa24Err)
			}
			_, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(2), aa24Clock.aa24Reading)
			aa24Want(aa24T, "observe", aa24Err, ErrAA24Clock)
			aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(40)
			if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordGap ||
				aa24Delivery.AA24Record.AA24CaptureTime != 20 || aa24Delivery.AA24Record.AA24CaptureSequence != 2 {
				aa24T.Fatalf("gap uses the last trusted tick: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
			}
		})
		aa24T.Run("host clock "+aa24Clock.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			if _, _, aa24Err := aa24Pair.aa24Poll(20); aa24Err != nil {
				aa24T.Fatal(aa24Err)
			}
			_, _, aa24Err := aa24Pair.aa24Host.AA24Poll(nil, aa24Clock.aa24Reading)
			aa24Want(aa24T, "poll", aa24Err, ErrAA24Clock)
			if aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceClock {
				aa24T.Fatalf("fence %+v", aa24Pair.aa24Host.AA24Fence())
			}
		})
	}
	aa24T.Run("zero tick", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		_, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(0))
		aa24Want(aa24T, "worker", aa24Err, ErrAA24Clock)
		_, _, aa24Err = aa24Pair.aa24Host.AA24Poll(nil, aa24At(0))
		aa24Want(aa24T, "host", aa24Err, ErrAA24Clock)
	})
}

// aa24RefVariants changes one plan identity field at a time.
func aa24RefVariants() []struct {
	aa24Name   string
	aa24Mutate func(*AA24PlanRef)
} {
	return []struct {
		aa24Name   string
		aa24Mutate func(*AA24PlanRef)
	}{
		{"subscriber", func(aa24R *AA24PlanRef) { aa24R.AA24Key.AA23Subscriber++ }},
		{"address", func(aa24R *AA24PlanRef) { aa24R.AA24Key.AA23Address = 0x26 }},
		{"epoch", func(aa24R *AA24PlanRef) { aa24R.AA24Key.AA23Epoch++ }},
		{"worker", func(aa24R *AA24PlanRef) { aa24R.AA24Worker++ }},
		{"stream", func(aa24R *AA24PlanRef) { aa24R.AA24Stream++ }},
		{"plan", func(aa24R *AA24PlanRef) { aa24R.AA24Plan++ }},
		{"plan expiry", func(aa24R *AA24PlanRef) { aa24R.AA24PlanExpiry++ }},
	}
}

func TestAA24CommandIdentityVariesEachFieldAtTheOutputBoundary(aa24T *aa24testing.T) {
	aa24Base := aa24FixtureCommand(1, 1)
	for _, aa24Variant := range aa24RefVariants() {
		aa24T.Run("ref "+aa24Variant.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			aa24Pair.aa24Publish(1, 10)
			aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
			aa24Want(aa24T, "positive control", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(11)), nil)
			aa24Changed := aa24Identity
			aa24Variant.aa24Mutate(&aa24Changed.AA24Ref)
			aa24Want(aa24T, aa24Variant.aa24Name, aa24Pair.aa24Worker.AA24AdmitOutput(aa24Changed, aa24At(11)), ErrAA24Stale)
			aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Changed, AA24StatusAccepted, aa24At(11)), ErrAA24Mismatch)
			aa24Want(aa24T, "original stays admitted", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(11)), nil)
		})
	}
	aa24Commands := []struct {
		aa24Name   string
		aa24Mutate func(*AA24CommandIdentity)
	}{
		{"transaction", func(aa24I *AA24CommandIdentity) { aa24I.AA24Transaction++ }},
		{"owner token", func(aa24I *AA24CommandIdentity) { aa24I.AA24OwnerToken++ }},
		{"owner expiry", func(aa24I *AA24CommandIdentity) { aa24I.AA24OwnerExpiry++ }},
		{"request deadline", func(aa24I *AA24CommandIdentity) { aa24I.AA24RequestDeadline++ }},
	}
	for _, aa24Variant := range aa24Commands {
		aa24T.Run("command "+aa24Variant.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			aa24Pair.aa24Publish(1, 10)
			aa24Pair.aa24Admit(1, 1, 10)
			aa24Changed := aa24Base
			aa24Variant.aa24Mutate(&aa24Changed)
			aa24Want(aa24T, aa24Variant.aa24Name, aa24Pair.aa24Worker.AA24AdmitOutput(aa24Changed, aa24At(11)), ErrAA24Mismatch)
			aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Changed, AA24StatusAccepted, aa24At(11)), ErrAA24Mismatch)
			aa24Want(aa24T, "positive control", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Base, aa24At(11)), nil)
		})
	}
}

func TestAA24DeadlinesAreExclusiveForEachField(aa24T *aa24testing.T) {
	aa24Cases := []struct {
		aa24Name   string
		aa24Mutate func(*AA24CommandIdentity)
	}{
		{"owner expiry", func(aa24I *AA24CommandIdentity) { aa24I.AA24OwnerExpiry = 50 }},
		{"request deadline", func(aa24I *AA24CommandIdentity) { aa24I.AA24RequestDeadline = 50 }},
		{"plan expiry", func(aa24I *AA24CommandIdentity) { aa24I.AA24Ref.AA24PlanExpiry = 50 }},
	}
	for _, aa24Case := range aa24Cases {
		aa24T.Run(aa24Case.aa24Name, func(aa24T *aa24testing.T) {
			aa24Identity := aa24FixtureCommand(1, 1)
			aa24Case.aa24Mutate(&aa24Identity)
			aa24Pair := aa24NewPair(aa24T)
			aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
			aa24Want(aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24Identity.AA24Ref, aa24ArenaStart, 8, aa24At(10)), nil)
			aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
			aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, nil, aa24At(10)), nil)
			_, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(49))
			if aa24Err != nil || !aa24OK {
				aa24T.Fatalf("one tick before: %v %v", aa24OK, aa24Err)
			}
			aa24Want(aa24T, "one tick before output", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(49)), nil)
			aa24Want(aa24T, "equality is expired", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(50)), ErrAA24Expired)
			aa24Want(aa24T, "expiry ends selection", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusRetired, aa24At(50)), ErrAA24Mismatch)
			aa24Second := aa24Identity
			aa24Second.AA24OwnerToken = 2
			aa24Want(aa24T, "host refuses expired", aa24Pair.aa24Host.AA24SendCommand(aa24Second, nil, aa24At(50)), ErrAA24Expired)
		})
		aa24T.Run(aa24Case.aa24Name+" at worker dequeue", func(aa24T *aa24testing.T) {
			aa24Identity := aa24FixtureCommand(1, 1)
			aa24Case.aa24Mutate(&aa24Identity)
			aa24Pair := aa24NewPair(aa24T)
			aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
			aa24Want(aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24Identity.AA24Ref, aa24ArenaStart, 8, aa24At(10)), nil)
			aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
			aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, nil, aa24At(10)), nil)
			_, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(50))
			aa24Want(aa24T, "stale in the queue", aa24Err, ErrAA24Expired)
			if aa24OK || aa24Pair.aa24Worker.AA24LaneStats(AA24LaneHostData).AA24Dropped != 1 {
				aa24T.Fatalf("expired command must drop: %v", aa24OK)
			}
		})
	}
}

func TestAA24StaleCommandsAreDroppedNotReplayed(aa24T *aa24testing.T) {
	aa24Inject := func(aa24T *aa24testing.T, aa24Pair *aa24Pair, aa24Identity AA24CommandIdentity) {
		aa24T.Helper()
		aa24Record := AA24Record{AA24Kind: AA24RecordCommand}
		aa24Record.aa24ApplyIdentity(aa24Identity)
		aa24Want(aa24T, "inject", aa24Pair.aa24Host.aa24Lane(AA24LaneHostData).aa24Write(&aa24Record, 0), nil)
	}
	for _, aa24Variant := range aa24RefVariants() {
		aa24T.Run(aa24Variant.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			aa24Pair.aa24Publish(1, 10)
			aa24Changed := aa24FixtureCommand(1, 1)
			aa24Variant.aa24Mutate(&aa24Changed.AA24Ref)
			aa24Want(aa24T, "host refuses", aa24Pair.aa24Host.AA24SendCommand(aa24Changed, nil, aa24At(11)), ErrAA24Stale)
			aa24Inject(aa24T, aa24Pair, aa24Changed)
			_, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(11))
			aa24Want(aa24T, "worker drops", aa24Err, ErrAA24Stale)
			if aa24OK || !aa24Pair.aa24Worker.AA24OutputOpen() || aa24Pair.aa24Worker.AA24LaneStats(AA24LaneHostData).AA24Dropped != 1 {
				aa24T.Fatalf("drop must not fence: %v", aa24OK)
			}
			aa24Pair.aa24Admit(1, 2, 12)
		})
	}
	aa24T.Run("token floor", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		aa24Identity := aa24Pair.aa24Admit(1, 5, 10)
		aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(11)), nil)
		aa24Want(aa24T, "host reuse", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, nil, aa24At(11)), ErrAA24Stale)
		aa24Want(aa24T, "host older", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 4), nil, aa24At(11)), ErrAA24Stale)
		aa24Inject(aa24T, aa24Pair, aa24Identity)
		_, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(12))
		aa24Want(aa24T, "replayed token", aa24Err, ErrAA24Stale)
		if aa24OK {
			aa24T.Fatal("replay admitted")
		}
	})
	aa24T.Run("late completion after a fence is stale", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
		aa24Pair.aa24Host.AA24ObserveWorkerTerminated()
		aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(11)), nil)
		_, aa24OK, aa24Err := aa24Pair.aa24Poll(12)
		aa24Want(aa24T, "cancelled command", aa24Err, ErrAA24Stale)
		if aa24OK {
			aa24T.Fatal("late completion delivered as current")
		}
	})
	aa24T.Run("completion varied by each field", func(aa24T *aa24testing.T) {
		aa24Variants := []func(*AA24CommandIdentity){
			func(aa24I *AA24CommandIdentity) { aa24I.AA24Transaction++ },
			func(aa24I *AA24CommandIdentity) { aa24I.AA24OwnerToken++ },
			func(aa24I *AA24CommandIdentity) { aa24I.AA24OwnerExpiry++ },
			func(aa24I *AA24CommandIdentity) { aa24I.AA24RequestDeadline++ },
			func(aa24I *AA24CommandIdentity) { aa24I.AA24Ref.AA24Key.AA23Subscriber++ },
			func(aa24I *AA24CommandIdentity) { aa24I.AA24Ref.AA24Key.AA23Address = 0x26 },
			func(aa24I *AA24CommandIdentity) { aa24I.AA24Ref.AA24Key.AA23Epoch++ },
			func(aa24I *AA24CommandIdentity) { aa24I.AA24Ref.AA24Plan++ },
			func(aa24I *AA24CommandIdentity) { aa24I.AA24Ref.AA24PlanExpiry++ },
		}
		for _, aa24Variant := range aa24Variants {
			aa24Pair := aa24NewPair(aa24T)
			aa24Pair.aa24Publish(1, 10)
			aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
			aa24Changed := aa24Identity
			aa24Variant(&aa24Changed)
			aa24Pair.aa24Worker.aa24Active = aa24Changed
			aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Changed, AA24StatusAccepted, aa24At(11)), nil)
			_, aa24OK, aa24Err := aa24Pair.aa24Poll(12)
			aa24Want(aa24T, "host exact match", aa24Err, ErrAA24Stale)
			if aa24OK || len(aa24Pair.aa24Host.aa24Outstanding) != 1 {
				aa24T.Fatalf("outstanding %d", len(aa24Pair.aa24Host.aa24Outstanding))
			}
		}
	})
}

func TestAA24RecordsFromAnotherWorkerOrStreamAreDropped(aa24T *aa24testing.T) {
	aa24Identities := []struct {
		aa24Name   string
		aa24Worker AA23WorkerID
		aa24Stream AA23StreamGeneration
	}{
		{"worker", aa24FixtureWorker + 1, aa24FixtureStream},
		{"stream", aa24FixtureWorker, aa24FixtureStream + 1},
	}
	for _, aa24Identity := range aa24Identities {
		aa24T.Run(aa24Identity.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			aa24Stop := AA24Record{AA24Kind: AA24RecordStop, AA24Worker: aa24Identity.aa24Worker, AA24Stream: aa24Identity.aa24Stream}
			aa24Want(aa24T, "stale stop", aa24Pair.aa24Host.aa24Lane(AA24LaneHostControl).aa24Write(&aa24Stop, 0), nil)
			aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
			if !aa24Pair.aa24Worker.AA24OutputOpen() || aa24Pair.aa24Worker.AA24LaneStats(AA24LaneHostControl).AA24Dropped != 1 {
				aa24T.Fatal("stale stop closed the worker")
			}
			aa24Gap := AA24Record{AA24Kind: AA24RecordGap, AA24Worker: aa24Identity.aa24Worker, AA24Stream: aa24Identity.aa24Stream, AA24CaptureSequence: 1, AA24CaptureTime: 1}
			aa24Want(aa24T, "stale gap", aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerControl).aa24Write(&aa24Gap, 0), nil)
			_, aa24OK, aa24Err := aa24Pair.aa24Poll(11)
			aa24Want(aa24T, "gap dropped", aa24Err, ErrAA24Stale)
			if aa24OK || !aa24Pair.aa24Host.AA24OutputOpen() {
				aa24T.Fatal("stale gap fenced the host")
			}
			aa24Raw := AA24Record{AA24Kind: AA24RecordRaw, AA24Worker: aa24Identity.aa24Worker, AA24Stream: aa24Identity.aa24Stream, AA24Transaction: 1, AA24Direction: AA24DirectionReceive, AA24CaptureSequence: 1, AA24CaptureTime: 1}
			aa24Want(aa24T, "stale raw", aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData).aa24Write(&aa24Raw, 0), nil)
			_, aa24OK, aa24Err = aa24Pair.aa24Poll(12)
			aa24Want(aa24T, "raw dropped", aa24Err, ErrAA24Stale)
			if aa24OK || aa24Pair.aa24Host.aa24NextCapture != 1 {
				aa24T.Fatal("stale raw advanced continuity")
			}
		})
	}
}

func TestAA24RevocationAcknowledgementIsExact(aa24T *aa24testing.T) {
	for _, aa24Variant := range aa24RefVariants() {
		aa24T.Run(aa24Variant.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			aa24Ref := aa24Pair.aa24Publish(1, 10)
			aa24Changed := aa24Ref
			aa24Variant.aa24Mutate(&aa24Changed)
			aa24Want(aa24T, "host refuses a varied revocation", aa24Pair.aa24Host.AA24RevokePlan(aa24Changed, aa24At(11)), ErrAA24Stale)
			aa24Revoke := AA24Record{AA24Kind: AA24RecordRevoke}
			aa24Revoke.aa24ApplyRef(aa24Changed)
			aa24Want(aa24T, "inject", aa24Pair.aa24Host.aa24Lane(AA24LaneHostControl).aa24Write(&aa24Revoke, 1), nil)
			aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(11)), nil)
			if aa24Pending := aa24Pair.aa24Worker.AA24PendingRevocations(nil); len(aa24Pending) != 0 {
				aa24T.Fatalf("varied revocation fenced a plan: %+v", aa24Pending)
			}
			aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(12)), nil)
			aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
			aa24Want(aa24T, "worker refuses a varied ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Changed, aa24At(12)), ErrAA24Stale)
			aa24Pair.aa24Worker.aa24NextSequence = 2
			aa24Stale := AA24Record{AA24Kind: AA24RecordAcknowledge, AA24Status: AA24StatusRetired, AA24CaptureSequence: 1, AA24CaptureTime: 12}
			aa24Stale.aa24ApplyRef(aa24Changed)
			aa24Want(aa24T, "inject ack", aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerControl).aa24Write(&aa24Stale, 1), nil)
			_, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
			aa24Want(aa24T, "stale ack", aa24Err, ErrAA24Stale)
			if aa24Plans := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); aa24OK || len(aa24Plans) != 1 {
				aa24T.Fatalf("stale ack retired a plan: %+v", aa24Plans)
			}
			aa24Want(aa24T, "exact ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(14)), nil)
			aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(15)
			if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordAcknowledge {
				aa24T.Fatalf("exact ack: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
			}
			if aa24Plans := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Plans) != 0 {
				aa24T.Fatalf("exact ack kept the plan: %+v", aa24Plans)
			}
		})
	}
	aa24T.Run("publication is not acknowledgement", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Ref := aa24Pair.aa24Publish(1, 10)
		aa24Stale := AA24Record{AA24Kind: AA24RecordAcknowledge, AA24Status: AA24StatusRetired, AA24CaptureSequence: 1, AA24CaptureTime: 11}
		aa24Stale.aa24ApplyRef(aa24Ref)
		aa24Want(aa24T, "inject", aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerControl).aa24Write(&aa24Stale, 1), nil)
		_, _, aa24Err := aa24Pair.aa24Poll(11)
		aa24Want(aa24T, "ack for a plan never revoked", aa24Err, ErrAA24Stale)
		if len(aa24Pair.aa24Host.AA24QuarantinedPlans(nil)) != 1 {
			aa24T.Fatal("plan retired without revocation")
		}
	})
}

func TestAA24PublicationOutputDecisions(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24Pair.aa24Publish(2, 10)
	aa24Want(aa24T, "plan does not advance", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(2), aa24ArenaStart, 8, aa24At(11)), ErrAA24Stale)
	aa24Want(aa24T, "older plan", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(1), aa24ArenaStart, 8, aa24At(11)), ErrAA24Stale)
	for _, aa24Variant := range aa24RefVariants() {
		if aa24Variant.aa24Name != "worker" && aa24Variant.aa24Name != "stream" {
			continue
		}
		aa24Changed := aa24FixtureRef(3)
		aa24Variant.aa24Mutate(&aa24Changed)
		aa24Want(aa24T, aa24Variant.aa24Name, aa24Pair.aa24Host.AA24PublishPlan(aa24Changed, aa24ArenaStart, 8, aa24At(11)), ErrAA24Stale)
	}
	aa24Expired := aa24FixtureRef(3)
	aa24Expired.AA24PlanExpiry = 11
	aa24Want(aa24T, "plan expiry equality", aa24Pair.aa24Host.AA24PublishPlan(aa24Expired, aa24ArenaStart, 8, aa24At(11)), ErrAA24Expired)
	aa24Want(aa24T, "arena over the build identity", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(3), aa24ArenaStart-1, 8, aa24At(11)), ErrAA24Record)
	aa24Want(aa24T, "empty arena reference", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(3), aa24ArenaStart, 0, aa24At(11)), ErrAA24Record)
	if !aa24Pair.aa24Host.AA24OutputOpen() || len(aa24Pair.aa24Host.AA24QuarantinedPlans(nil)) != 1 {
		aa24T.Fatal("refused publications changed state")
	}
	for aa24Plan := AA23PlanGeneration(3); aa24Plan <= 5; aa24Plan++ {
		aa24Pair.aa24Publish(aa24Plan, 12)
	}
	aa24Want(aa24T, "bounded table", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(6), aa24ArenaStart, 8, aa24At(13)), ErrAA24Capacity)

	aa24T.Run("worker refuses what its bounded table cannot hold", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Record := AA24Record{AA24Kind: AA24RecordPublish, AA24ArenaOffset: aa24ArenaStart, AA24PlanBytes: 8}
		aa24Record.aa24ApplyRef(aa24FixtureRef(9))
		aa24Lane := aa24Pair.aa24Host.aa24Lane(AA24LaneHostControl)
		aa24Want(aa24T, "first", aa24Lane.aa24Write(&aa24Record, 1), nil)
		aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
		aa24Want(aa24T, "stale generation", aa24Lane.aa24Write(&aa24Record, 1), nil)
		aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
		if aa24Pair.aa24Worker.AA24PublishRefused() != 1 || len(aa24Pair.aa24Worker.aa24Adopted) != 1 {
			aa24T.Fatalf("refused %d adopted %d", aa24Pair.aa24Worker.AA24PublishRefused(), len(aa24Pair.aa24Worker.aa24Adopted))
		}
		aa24Record.aa24ApplyRef(aa24FixtureRef(10))
		aa24Record.AA24PlanExpiry = 10
		aa24Want(aa24T, "expired", aa24Lane.aa24Write(&aa24Record, 1), nil)
		aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
		if aa24Pair.aa24Worker.AA24PublishRefused() != 2 {
			aa24T.Fatalf("refused %d", aa24Pair.aa24Worker.AA24PublishRefused())
		}
		aa24Pair.aa24Worker.aa24Adopted = aa24Pair.aa24Worker.aa24Adopted[:cap(aa24Pair.aa24Worker.aa24Adopted)]
		aa24Record.aa24ApplyRef(aa24FixtureRef(11))
		aa24Want(aa24T, "over capacity", aa24Lane.aa24Write(&aa24Record, 1), nil)
		aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
		if aa24Pair.aa24Worker.AA24PublishRefused() != 3 {
			aa24T.Fatalf("refused %d", aa24Pair.aa24Worker.AA24PublishRefused())
		}
	})
	aa24T.Run("outstanding commands are bounded", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		for len(aa24Pair.aa24Host.aa24Outstanding) < cap(aa24Pair.aa24Host.aa24Outstanding) {
			aa24Pair.aa24Host.aa24Outstanding = append(aa24Pair.aa24Host.aa24Outstanding, aa24FixtureCommand(1, 1))
		}
		aa24Want(aa24T, "table full", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 1), nil, aa24At(10)), ErrAA24Capacity)
	})
}

func TestAA24HostControlSaturationFencesAndKeepsStopRoom(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
	aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24Want(aa24T, "second", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(2), aa24ArenaStart, 8, aa24At(11)), nil)
	aa24Want(aa24T, "third", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(3), aa24ArenaStart, 8, aa24At(11)), nil)
	aa24Want(aa24T, "revoke hits the reserved slot", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(11)), ErrAA24ControlFull)
	aa24Fence := aa24Pair.aa24Host.AA24Fence()
	if aa24Fence.AA24Reason != AA24FenceControlSaturated {
		aa24T.Fatalf("fence %+v", aa24Fence)
	}
	if aa24Cancelled := aa24Pair.aa24Host.AA24CancelledCommands(nil); len(aa24Cancelled) != 1 || !aa24Cancelled[0].aa24Matches(aa24Identity) {
		aa24T.Fatalf("cancelled %+v", aa24Cancelled)
	}
	aa24Want(aa24T, "new host command", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 2), nil, aa24At(11)), ErrAA24OutputFenced)
	if aa24Plans := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Plans) != 3 {
		aa24T.Fatalf("predecessor must stay quarantined: %+v", aa24Plans)
	}
	aa24Want(aa24T, "stop fits", aa24Pair.aa24Host.AA24AcknowledgeGap(0, aa24At(12)), nil)
	aa24Want(aa24T, "worker drains", aa24Pair.aa24Worker.AA24PollControl(aa24At(13)), nil)
	if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceStop {
		aa24T.Fatalf("worker fence %+v", aa24Pair.aa24Worker.AA24Fence())
	}
	aa24Want(aa24T, "retried revoke beside the fence", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(14)), nil)
	aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(14)), nil)
	aa24Want(aa24T, "ack on a fenced stream", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(15)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Poll(16); !aa24OK || aa24Err != nil {
		aa24T.Fatalf("host poll: %v %v", aa24OK, aa24Err)
	}
	if aa24Plans := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Plans) != 2 {
		aa24T.Fatalf("plans %+v", aa24Plans)
	}
}

func TestAA24ControlServiceDeadlineUsesInjectedTime(aa24T *aa24testing.T) {
	aa24T.Run("host control unread", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
		aa24Want(aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(1), aa24ArenaStart, 8, aa24At(10)), nil)
		if _, _, aa24Err := aa24Pair.aa24Poll(10 + aa24FixtureBudget - 1); aa24Err != nil || !aa24Pair.aa24Host.AA24OutputOpen() {
			aa24T.Fatalf("before the budget: %v %+v", aa24Err, aa24Pair.aa24Host.AA24Fence())
		}
		if _, _, aa24Err := aa24Pair.aa24Poll(10 + aa24FixtureBudget); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
		if aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceControlDeadline {
			aa24T.Fatalf("equality is expired: %+v", aa24Pair.aa24Host.AA24Fence())
		}
	})
	aa24T.Run("consumer progress restarts the budget", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
		aa24Want(aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(1), aa24ArenaStart, 8, aa24At(10)), nil)
		aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(50)), nil)
		aa24Want(aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(2), aa24ArenaStart, 8, aa24At(60)), nil)
		if _, _, aa24Err := aa24Pair.aa24Poll(159); aa24Err != nil || !aa24Pair.aa24Host.AA24OutputOpen() {
			aa24T.Fatalf("progress ignored: %v %+v", aa24Err, aa24Pair.aa24Host.AA24Fence())
		}
		if _, _, aa24Err := aa24Pair.aa24Poll(160); aa24Err != nil || aa24Pair.aa24Host.AA24OutputOpen() {
			aa24T.Fatalf("budget from last progress: %v", aa24Err)
		}
	})
	aa24T.Run("partial consumer progress restarts the budget", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Refs := []AA24PlanRef{aa24Pair.aa24Publish(1, 10), aa24Pair.aa24Publish(2, 10)}
		for _, aa24Ref := range aa24Refs {
			aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(10)), nil)
			aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
			aa24Want(aa24T, "ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(10)), nil)
		}
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(60); !aa24OK || aa24Err != nil {
			aa24T.Fatalf("host poll: %v %v", aa24OK, aa24Err)
		}
		aa24Want(aa24T, "progress seen", aa24Pair.aa24Worker.AA24PollControl(aa24At(60)), nil)
		aa24Want(aa24T, "past the first budget", aa24Pair.aa24Worker.AA24PollControl(aa24At(60+aa24FixtureBudget-1)), nil)
		if !aa24Pair.aa24Worker.AA24OutputOpen() {
			aa24T.Fatalf("progress ignored: %+v", aa24Pair.aa24Worker.AA24Fence())
		}
		aa24Want(aa24T, "budget from last progress", aa24Pair.aa24Worker.AA24PollControl(aa24At(60+aa24FixtureBudget)), nil)
		if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceControlDeadline {
			aa24T.Fatalf("fence %+v", aa24Pair.aa24Worker.AA24Fence())
		}
	})
	aa24T.Run("worker control unread closes the worker without host delivery", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Ref := aa24Pair.aa24Publish(1, 10)
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(10)), nil)
		aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
		aa24Want(aa24T, "ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(10)), nil)
		aa24Want(aa24T, "before", aa24Pair.aa24Worker.AA24PollControl(aa24At(10+aa24FixtureBudget-1)), nil)
		if !aa24Pair.aa24Worker.AA24OutputOpen() {
			aa24T.Fatal("closed before the budget")
		}
		aa24Want(aa24T, "at", aa24Pair.aa24Worker.AA24PollControl(aa24At(10+aa24FixtureBudget)), nil)
		if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceControlDeadline {
			aa24T.Fatalf("fence %+v", aa24Pair.aa24Worker.AA24Fence())
		}
	})
}

func TestAA24GapMustBeAcknowledgedBeforeResynchronization(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Stale := aa24FixtureCommand(1, 1)
	aa24Want(aa24T, "queued command", aa24Pair.aa24Host.AA24SendCommand(aa24Stale, []byte{1}, aa24At(10)), nil)
	aa24Next := aa24FixtureStream + 1

	_, aa24Err := aa24Pair.aa24Host.AA24ResyncExpected(aa24Next)
	aa24Want(aa24T, "open host", aa24Err, ErrAA24NotFenced)
	aa24Want(aa24T, "no fence to acknowledge", aa24Pair.aa24Host.AA24AcknowledgeGap(0, aa24At(10)), ErrAA24NotFenced)
	_, aa24Err = aa24Pair.aa24Worker.AA24ResyncExpected(aa24Next, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor)
	aa24Want(aa24T, "open worker", aa24Err, ErrAA24NotAcknowledged)

	for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(20)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
	}
	if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(2), aa24At(20)); !aa24errorstest.Is(aa24Err, ErrAA24Gap) {
		aa24T.Fatalf("symbol past the lane: got %v, want %v", aa24Err, ErrAA24Gap)
	}
	_, aa24Err = aa24Pair.aa24Worker.AA24ResyncExpected(aa24Next, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor)
	aa24Want(aa24T, "gap unacknowledged at worker", aa24Err, ErrAA24NotAcknowledged)
	if _, aa24OK, aa24Err := aa24Pair.aa24Poll(21); !aa24OK || aa24Err != nil {
		aa24T.Fatalf("gap poll: %v %v", aa24OK, aa24Err)
	}
	_, aa24Err = aa24Pair.aa24Host.AA24ResyncExpected(aa24Next)
	aa24Want(aa24T, "stop not sent", aa24Err, ErrAA24NotAcknowledged)
	aa24Want(aa24T, "wrong gap sequence", aa24Pair.aa24Host.AA24AcknowledgeGap(2, aa24At(22)), ErrAA24Stale)
	_, aa24Err = aa24Pair.aa24Host.AA24ResyncExpected(aa24Next)
	aa24Want(aa24T, "wrong acknowledgement", aa24Err, ErrAA24NotAcknowledged)

	aa24Want(aa24T, "exact acknowledgement", aa24Pair.aa24Host.AA24AcknowledgeGap(3, aa24At(22)), nil)
	aa24Want(aa24T, "repeat is harmless", aa24Pair.aa24Host.AA24AcknowledgeGap(3, aa24At(22)), nil)
	if aa24Pair.aa24Host.aa24Lane(AA24LaneHostControl).aa24LocalProducer != 2 {
		aa24T.Fatal("repeat queued a second stop")
	}
	_, aa24Err = aa24Pair.aa24Host.AA24ResyncExpected(aa24Next)
	aa24Want(aa24T, "stop unread", aa24Err, ErrAA24NotAcknowledged)
	aa24Want(aa24T, "worker reads stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(23)), nil)

	aa24Generations := []AA23StreamGeneration{aa24FixtureStream, aa24FixtureStream - 1, 0}
	for _, aa24Generation := range aa24Generations {
		_, aa24Err = aa24Pair.aa24Host.AA24ResyncExpected(aa24Generation)
		aa24Want(aa24T, "host generation must advance", aa24Err, ErrAA24Stale)
		_, aa24Err = aa24Pair.aa24Worker.AA24ResyncExpected(aa24Generation, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor)
		aa24Want(aa24T, "worker generation must advance", aa24Err, ErrAA24Stale)
	}
	aa24HostNext, aa24Err := aa24Pair.aa24Host.AA24ResyncExpected(aa24Next)
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	aa24WorkerNext, aa24Err := aa24Pair.aa24Worker.AA24ResyncExpected(aa24Next, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor)
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	if aa24HostNext.AA24Binding != aa24WorkerNext.AA24Binding || aa24HostNext.AA24Binding.AA24Stream != aa24Next ||
		aa24HostNext.AA24PlanFloor != 1 || aa24HostNext.AA24TokenFloor != 1 {
		aa24T.Fatalf("successors: %+v / %+v", aa24HostNext, aa24WorkerNext)
	}

	aa24OldMem := append([]byte(nil), aa24Pair.aa24Mem...)
	aa24Fresh := aa24FixtureMemory(aa24FixtureBytes())
	if _, aa24Err := AA24AttachHost(aa24Fresh, aa24HostNext); aa24Err == nil {
		aa24T.Fatal("an uninitialized replacement was admitted")
	}
	if aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceWorkerGap || string(aa24OldMem) != string(aa24Pair.aa24Mem) {
		aa24T.Fatal("a refused replacement changed the previous mapping's state")
	}
	aa24Binding := aa24FixtureBinding()
	aa24Binding.AA24Stream = aa24Next
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Fresh, aa24Binding, aa24FixtureConfig, aa24FixtureLimits)
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	aa24Expected.AA24PlanFloor, aa24Expected.AA24TokenFloor = aa24HostNext.AA24PlanFloor, aa24HostNext.AA24TokenFloor
	if _, aa24Err := AA24ValidateMapping(aa24Fresh, aa24Pair.aa24Expected); !aa24errorstest.Is(aa24Err, ErrAA24Identity) {
		aa24T.Fatalf("predecessor identity must refuse the successor: %v", aa24Err)
	}
	aa24Successor := aa24AttachPair(aa24T, aa24Fresh, aa24Expected)
	if aa24Delivery, aa24OK, aa24Err := aa24Successor.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(30)); aa24OK || aa24Err != nil {
		aa24T.Fatalf("a queued command replayed: %+v %v", aa24Delivery, aa24Err)
	}
	aa24ArenaStart := aa24Successor.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24OldPlan := aa24FixtureRef(1)
	aa24OldPlan.AA24Stream = aa24Next
	aa24Want(aa24T, "plan generation stays advancing", aa24Successor.aa24Host.AA24PublishPlan(aa24OldPlan, aa24ArenaStart, 8, aa24At(30)), ErrAA24Stale)
	aa24NewPlan := aa24FixtureRef(2)
	aa24NewPlan.AA24Stream = aa24Next
	aa24Want(aa24T, "new plan", aa24Successor.aa24Host.AA24PublishPlan(aa24NewPlan, aa24ArenaStart, 8, aa24At(30)), nil)
	aa24Want(aa24T, "worker poll", aa24Successor.aa24Worker.AA24PollControl(aa24At(30)), nil)
	aa24OldToken := AA24CommandIdentity{AA24Ref: aa24NewPlan, AA24Transaction: 1, AA24OwnerToken: 1, AA24OwnerExpiry: aa24FixtureOwner, AA24RequestDeadline: aa24FixtureDeadline}
	aa24Want(aa24T, "token stays nonreused", aa24Successor.aa24Host.AA24SendCommand(aa24OldToken, nil, aa24At(30)), ErrAA24Stale)
	aa24Sequence, aa24Err := aa24Successor.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(31))
	if aa24Err != nil || aa24Sequence != 1 {
		aa24T.Fatalf("fresh stream sequence: %d %v", aa24Sequence, aa24Err)
	}
}

func TestAA24WorkerStaysFencedUntilItsGapIsConsumed(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(20)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
	}
	if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(2), aa24At(20)); !aa24errorstest.Is(aa24Err, ErrAA24Gap) {
		aa24T.Fatalf("symbol past the lane: got %v, want %v", aa24Err, ErrAA24Gap)
	}
	aa24Want(aa24T, "host stops without reading the gap", aa24Pair.aa24Host.AA24RequestStop(aa24At(21)), nil)
	aa24Want(aa24T, "worker reads stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(22)), nil)
	aa24Next := aa24FixtureStream + 1
	_, aa24Err := aa24Pair.aa24Worker.AA24ResyncExpected(aa24Next, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor)
	aa24Want(aa24T, "worker: gap unread", aa24Err, ErrAA24NotAcknowledged)
	_, aa24Err = aa24Pair.aa24Host.AA24ResyncExpected(aa24Next)
	aa24Want(aa24T, "host: gap unread", aa24Err, ErrAA24NotAcknowledged)
	if _, aa24OK, aa24Err := aa24Pair.aa24Poll(23); !aa24OK || aa24Err != nil {
		aa24T.Fatalf("gap poll: %v %v", aa24OK, aa24Err)
	}
	if aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceWorkerGap {
		aa24T.Fatalf("gap must replace the local cause: %+v", aa24Pair.aa24Host.AA24Fence())
	}
	if _, aa24Err := aa24Pair.aa24Worker.AA24ResyncExpected(aa24Next, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor); aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	if _, aa24Err := aa24Pair.aa24Host.AA24ResyncExpected(aa24Next); aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
}

func TestAA24RestartAdvancesTheWorkerIncarnation(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
	aa24Next := aa24FixtureWorker + 1
	_, aa24Err := aa24Pair.aa24Host.AA24RestartExpected(aa24Next, 1)
	aa24Want(aa24T, "worker not proven gone", aa24Err, ErrAA24NotAcknowledged)
	aa24Pair.aa24Host.AA24ObserveWorkerTerminated()
	if aa24Pair.aa24Host.AA24OutputOpen() || aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceWorkerTerminated {
		aa24T.Fatalf("fence %+v", aa24Pair.aa24Host.AA24Fence())
	}
	if aa24Cancelled := aa24Pair.aa24Host.AA24CancelledCommands(nil); len(aa24Cancelled) != 1 || !aa24Cancelled[0].aa24Matches(aa24Identity) {
		aa24T.Fatalf("cancelled %+v", aa24Cancelled)
	}
	_, aa24Err = aa24Pair.aa24Host.AA24RestartExpected(aa24FixtureWorker, 1)
	aa24Want(aa24T, "same incarnation", aa24Err, ErrAA24Stale)
	_, aa24Err = aa24Pair.aa24Host.AA24RestartExpected(aa24FixtureWorker-1, 1)
	aa24Want(aa24T, "older incarnation", aa24Err, ErrAA24Stale)
	_, aa24Err = aa24Pair.aa24Host.AA24RestartExpected(aa24Next, 0)
	aa24Want(aa24T, "zero stream", aa24Err, ErrAA24Stale)
	aa24Expected, aa24Err := aa24Pair.aa24Host.AA24RestartExpected(aa24Next, 1)
	if aa24Err != nil || aa24Expected.AA24Binding.AA24Worker != aa24Next || aa24Expected.AA24Binding.AA24Stream != 1 || aa24Expected.AA24TokenFloor != 1 {
		aa24T.Fatalf("restart expected: %+v %v", aa24Expected, aa24Err)
	}
	if _, aa24Err := AA24AttachWorker(aa24Pair.aa24Mem, aa24Expected); !aa24errorstest.Is(aa24Err, ErrAA24Identity) {
		aa24T.Fatalf("predecessor mapping must refuse the successor identity: %v", aa24Err)
	}
	aa24Fresh := aa24FixtureMemory(aa24FixtureBytes())
	aa24Binding := aa24Expected.AA24Binding
	if _, _, aa24Err := AA24InitMapping(aa24Fresh, aa24Binding, aa24FixtureConfig, aa24FixtureLimits); aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	aa24Successor := aa24AttachPair(aa24T, aa24Fresh, aa24Expected)
	if _, aa24OK, aa24Err := aa24Successor.aa24Poll(30); aa24OK || aa24Err != nil {
		aa24T.Fatalf("no predecessor record survives: %v %v", aa24OK, aa24Err)
	}
	aa24Want(aa24T, "predecessor identity on the successor", aa24Successor.aa24Host.AA24SendCommand(aa24Identity, nil, aa24At(30)), ErrAA24Stale)
}

func TestAA24TeardownDoesNotDrainSaturatedData(aa24T *aa24testing.T) {
	aa24T.Run("stop path", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		for aa24Octet := byte(0); aa24Octet < 2; aa24Octet++ {
			if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(aa24Octet), aa24At(10)); aa24Err != nil {
				aa24T.Fatal(aa24Err)
			}
		}
		aa24Want(aa24T, "open worker", aa24Pair.aa24Worker.AA24Teardown(), ErrAA24NotAcknowledged)
		aa24Want(aa24T, "open host", aa24Pair.aa24Host.AA24Teardown(), ErrAA24NotAcknowledged)
		aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(11)), nil)
		aa24Want(aa24T, "stop unread", aa24Pair.aa24Host.AA24Teardown(), ErrAA24NotAcknowledged)
		aa24Want(aa24T, "worker reads stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
		aa24Want(aa24T, "host teardown", aa24Pair.aa24Host.AA24Teardown(), nil)
		aa24Want(aa24T, "worker teardown", aa24Pair.aa24Worker.AA24Teardown(), nil)
		aa24Want(aa24T, "repeat host", aa24Pair.aa24Host.AA24Teardown(), nil)
		aa24Want(aa24T, "repeat worker", aa24Pair.aa24Worker.AA24Teardown(), nil)
		aa24Ref := aa24FixtureRef(1)
		aa24Identity := aa24FixtureCommand(1, 1)
		_, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(13))
		aa24Want(aa24T, "observe", aa24Err, ErrAA24Detached)
		aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(13)), ErrAA24Detached)
		_, _, aa24Err = aa24Pair.aa24Worker.AA24NextCommand(nil, aa24At(13))
		aa24Want(aa24T, "next", aa24Err, ErrAA24Detached)
		aa24Want(aa24T, "admit", aa24Pair.aa24Worker.AA24AdmitOutput(aa24Identity, aa24At(13)), ErrAA24Detached)
		aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusRetired, aa24At(13)), ErrAA24Detached)
		aa24Want(aa24T, "ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(13)), ErrAA24Detached)
		_, aa24Err = aa24Pair.aa24Worker.AA24ResyncExpected(aa24FixtureStream+1, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor)
		aa24Want(aa24T, "worker resync", aa24Err, ErrAA24Detached)
		_, _, aa24Err = aa24Pair.aa24Host.AA24Poll(nil, aa24At(13))
		aa24Want(aa24T, "host poll", aa24Err, ErrAA24Detached)
		aa24Want(aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24Ref, 0, 0, aa24At(13)), ErrAA24Detached)
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(13)), ErrAA24Detached)
		aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, nil, aa24At(13)), ErrAA24Detached)
		aa24Want(aa24T, "ack gap", aa24Pair.aa24Host.AA24AcknowledgeGap(0, aa24At(13)), ErrAA24Detached)
		aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(13)), ErrAA24Detached)
		_, aa24Err = aa24Pair.aa24Host.AA24ResyncExpected(aa24FixtureStream + 1)
		aa24Want(aa24T, "host resync", aa24Err, ErrAA24Detached)
		_, aa24Err = aa24Pair.aa24Host.AA24RestartExpected(aa24FixtureWorker+1, 1)
		aa24Want(aa24T, "host restart", aa24Err, ErrAA24Detached)
		if aa24Pair.aa24Worker.AA24OutputOpen() || aa24Pair.aa24Host.AA24OutputOpen() {
			aa24T.Fatal("a torn-down endpoint reports open output decisions")
		}
	})
	aa24T.Run("terminated worker", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Host.AA24ObserveWorkerTerminated()
		aa24Want(aa24T, "host teardown", aa24Pair.aa24Host.AA24Teardown(), nil)
	})
}

func TestAA24ExpiredOutstandingCommandsAreRetired(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Want(aa24T, "poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
	aa24Max := cap(aa24Pair.aa24Host.aa24Outstanding)
	for aa24Index := 0; aa24Index < aa24Max; aa24Index++ {
		aa24Identity := aa24FixtureCommand(1, AA24OwnerToken(aa24Index+1))
		aa24Identity.AA24RequestDeadline = 12
		aa24Pair.aa24Host.aa24Outstanding = append(aa24Pair.aa24Host.aa24Outstanding, aa24Identity)
	}
	aa24Want(aa24T, "full while live", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, AA24OwnerToken(aa24Max+1)), nil, aa24At(11)), ErrAA24Capacity)
	aa24Pair.aa24Host.aa24LastControlTime = 12
	aa24Want(aa24T, "expired retired", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, AA24OwnerToken(aa24Max+1)), nil, aa24At(12)), nil)
	if aa24Len := len(aa24Pair.aa24Host.aa24Outstanding); aa24Len != 1 {
		aa24T.Fatalf("outstanding %d", aa24Len)
	}
}

func TestAA24RefusedPublicationIsRetiredByRevocation(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24FixtureRef(1)
	aa24Ref.AA24PlanExpiry = 12
	aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24Want(aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24Ref, aa24ArenaStart, 8, aa24At(10)), nil)
	aa24Want(aa24T, "worker sees it expired", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
	if aa24Pair.aa24Worker.AA24PublishRefused() != 1 || len(aa24Pair.aa24Worker.aa24Adopted) != 0 {
		aa24T.Fatalf("refused %d", aa24Pair.aa24Worker.AA24PublishRefused())
	}
	aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(12)), nil)
	aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordAcknowledge {
		aa24T.Fatalf("retirement: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	if aa24Plans := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Plans) != 0 {
		aa24T.Fatalf("refused plan stayed quarantined: %+v", aa24Plans)
	}
}

func TestAA24RefusedRetirementSurvivesSaturatedControl(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Worker := aa24Pair.aa24Worker
	for aa24Plan := AA23PlanGeneration(1); aa24Plan <= 2; aa24Plan++ {
		aa24Tick := AA23Tick(10 * aa24Plan)
		aa24Ref := aa24Pair.aa24Publish(aa24Plan, aa24Tick)
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(aa24Tick+1)), nil)
		aa24Want(aa24T, "worker poll", aa24Worker.AA24PollControl(aa24At(aa24Tick+1)), nil)
		aa24Want(aa24T, "ack fills a usable control slot", aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(aa24Tick+2)), nil)
	}
	aa24Refused := aa24FixtureRef(3)
	aa24Worker.aa24RefusedRefs = append(aa24Worker.aa24RefusedRefs, aa24Refused)
	aa24Record := AA24Record{AA24Kind: AA24RecordRevoke}
	aa24Record.aa24ApplyRef(aa24Refused)
	aa24Want(aa24T, "revoke of the refused plan", aa24Pair.aa24Host.aa24WriteControl(&aa24Record), nil)
	aa24Want(aa24T, "worker poll on saturated control", aa24Worker.AA24PollControl(aa24At(23)), nil)
	if len(aa24Worker.aa24RefusedRefs) != 1 || !aa24HostControlUnread(aa24T, aa24Worker) {
		aa24T.Fatalf("reference and its revocation must stay until acknowledged: refused %d", len(aa24Worker.aa24RefusedRefs))
	}
	aa24Control := aa24Worker.aa24Lane(AA24LaneWorkerControl)
	aa24Before, _, aa24Err := aa24Control.aa24Indices()
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	for aa24Tick := AA23Tick(24); aa24Tick < 27; aa24Tick++ {
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(aa24Tick); !aa24OK || aa24Err != nil {
			aa24T.Fatalf("host poll: %v %v", aa24OK, aa24Err)
		}
	}
	aa24Want(aa24T, "worker retry", aa24Worker.AA24PollControl(aa24At(28)), nil)
	if len(aa24Worker.aa24RefusedRefs) != 0 || len(aa24Worker.aa24RetireRetry) != 0 {
		aa24T.Fatalf("retired reference lingers: refused %d retry %d", len(aa24Worker.aa24RefusedRefs), len(aa24Worker.aa24RetireRetry))
	}
	if aa24After, _, aa24Err := aa24Control.aa24Indices(); aa24Err != nil || aa24After != aa24Before+1 {
		aa24T.Fatalf("retirement must be written once the room returns: before %d after %d err %v", aa24Before, aa24After, aa24Err)
	}
}

func TestAA24RevokedActiveCommandsLeaveOutstanding(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	for aa24Cycle := AA23PlanGeneration(1); aa24Cycle <= 6; aa24Cycle++ {
		aa24Tick := AA23Tick(10 * aa24Cycle)
		aa24Ref := aa24Pair.aa24Publish(aa24Cycle, aa24Tick)
		aa24Pair.aa24Admit(aa24Cycle, AA24OwnerToken(aa24Cycle), aa24Tick+1)
		if len(aa24Pair.aa24Host.aa24Outstanding) != 1 {
			aa24T.Fatalf("cycle %d: outstanding %d", aa24Cycle, len(aa24Pair.aa24Host.aa24Outstanding))
		}
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(aa24Tick+2)), nil)
		aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(aa24Tick+3)), nil)
		aa24Want(aa24T, "ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(aa24Tick+4)), nil)
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(aa24Tick + 5); !aa24OK || aa24Err != nil {
			aa24T.Fatalf("host poll: %v %v", aa24OK, aa24Err)
		}
		if len(aa24Pair.aa24Host.aa24Outstanding) != 0 {
			aa24T.Fatalf("cycle %d: commands of the retired plan stay outstanding: %d", aa24Cycle, len(aa24Pair.aa24Host.aa24Outstanding))
		}
	}
}

func TestAA24StopStillFlowsAfterClockFence(aa24T *aa24testing.T) {
	aa24T.Run("worker clock fence", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(20)); aa24Err != nil {
			aa24T.Fatal(aa24Err)
		}
		aa24Want(aa24T, "regressed worker clock", aa24Pair.aa24Worker.AA24PollControl(aa24At(19)), ErrAA24Clock)
		aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(30)
		if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordGap {
			aa24T.Fatalf("gap: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
		}
		aa24Want(aa24T, "acknowledge", aa24Pair.aa24Host.AA24AcknowledgeGap(aa24Delivery.AA24Record.AA24CaptureSequence, aa24At(31)), nil)
		aa24Want(aa24T, "fenced worker drains the stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(19)), ErrAA24Clock)
		if _, aa24Err := aa24Pair.aa24Worker.AA24ResyncExpected(aa24FixtureStream+1, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor); aa24Err != nil {
			aa24T.Fatalf("resync stays blocked: %v", aa24Err)
		}
	})
	aa24T.Run("host clock fence", func(aa24T *aa24testing.T) {
		for _, aa24Name := range []string{"acknowledge", "request"} {
			aa24Pair := aa24NewPair(aa24T)
			aa24Want(aa24T, aa24Name+" poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
			if _, _, aa24Err := aa24Pair.aa24Poll(30); aa24Err != nil {
				aa24T.Fatal(aa24Err)
			}
			_, _, aa24Err := aa24Pair.aa24Poll(29)
			aa24Want(aa24T, aa24Name+" regressed host clock", aa24Err, ErrAA24Clock)
			if aa24Name == "acknowledge" {
				aa24Want(aa24T, "acknowledge on a fenced clock", aa24Pair.aa24Host.AA24AcknowledgeGap(0, aa24At(29)), nil)
			} else {
				aa24Want(aa24T, "request on a fenced clock", aa24Pair.aa24Host.AA24RequestStop(aa24At(29)), nil)
			}
			aa24Want(aa24T, aa24Name+" worker drains", aa24Pair.aa24Worker.AA24PollControl(aa24At(40)), nil)
			if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceStop {
				aa24T.Fatalf("%s: worker fence %+v", aa24Name, aa24Pair.aa24Worker.AA24Fence())
			}
		}
	})
}

func TestAA24QueuedCompletionSurvivesExpiryCleanup(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24First := aa24FixtureCommand(1, 1)
	aa24First.AA24RequestDeadline = 20
	aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24First, []byte{1}, aa24At(10)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(11)); aa24Err != nil || !aa24OK {
		aa24T.Fatalf("next: %v %v", aa24OK, aa24Err)
	}
	aa24Want(aa24T, "complete before the deadline", aa24Pair.aa24Worker.AA24Complete(aa24First, AA24StatusAccepted, aa24At(12)), nil)
	aa24Want(aa24T, "second send at the first deadline", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 2), nil, aa24At(20)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(21)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("queued completion lost: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	// Positive control: the same completion is not accepted twice.
	aa24Replay := AA24Record{AA24Kind: AA24RecordCompletion, AA24Status: AA24StatusAccepted, AA24CaptureSequence: 2, AA24CaptureTime: 22}
	aa24Replay.aa24ApplyIdentity(aa24First)
	aa24Replay.AA24Worker, aa24Replay.AA24Stream = aa24FixtureWorker, aa24FixtureStream
	aa24Want(aa24T, "inject replay", aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData).aa24Write(&aa24Replay, 0), nil)
	_, _, aa24Err = aa24Pair.aa24Poll(23)
	aa24Want(aa24T, "replay is stale", aa24Err, ErrAA24Stale)
}

func TestAA24QueuedCompletionSurvivesQueuedRetirement(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
	aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(11)), nil)
	aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(12)), nil)
	aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(13)), nil)
	aa24Want(aa24T, "retire", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(14)), nil)
	aa24First, aa24OK, aa24Err := aa24Pair.aa24Poll(15)
	if aa24Err != nil || !aa24OK || aa24First.AA24Record.AA24Kind != AA24RecordAcknowledge {
		aa24T.Fatalf("control first: %+v %v %v", aa24First, aa24OK, aa24Err)
	}
	aa24Second, aa24OK, aa24Err := aa24Pair.aa24Poll(16)
	if aa24Err != nil || !aa24OK || aa24Second.AA24Record.AA24Kind != AA24RecordCompletion {
		aa24T.Fatalf("queued completion lost: %+v %v %v", aa24Second, aa24OK, aa24Err)
	}
	if aa24Len := len(aa24Pair.aa24Host.aa24Outstanding); aa24Len != 0 {
		aa24T.Fatalf("outstanding %d", aa24Len)
	}
}

func TestAA24WorkerTeardownRequiresTheStopHandshake(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
	if aa24Pair.aa24Worker.AA24OutputOpen() {
		aa24T.Fatal("local fault must close output")
	}
	aa24Want(aa24T, "fenced but no stop", aa24Pair.aa24Worker.AA24Teardown(), ErrAA24NotAcknowledged)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(11)), nil)
	aa24Want(aa24T, "stop read, gap unread", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
	aa24Want(aa24T, "gap not consumed", aa24Pair.aa24Worker.AA24Teardown(), ErrAA24NotAcknowledged)
	if _, aa24OK, aa24Err := aa24Pair.aa24Poll(13); aa24Err != nil || !aa24OK {
		aa24T.Fatalf("host reads gap: %v %v", aa24OK, aa24Err)
	}
	aa24Want(aa24T, "acknowledged", aa24Pair.aa24Worker.AA24Teardown(), nil)
}

// aa24LingerPair builds a pair whose outstanding capacity is one, so expiry of each
// command moves its identity into the lingering list.
func aa24LingerPair(aa24T *aa24testing.T) *aa24Pair {
	aa24T.Helper()
	aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa24Limits := aa24FixtureLimits
	aa24Limits.AA24MaxOutstanding = 1
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Mem, aa24FixtureBinding(), aa24FixtureConfig, aa24Limits)
	if aa24Err != nil {
		aa24T.Fatalf("init: %v", aa24Err)
	}
	return aa24AttachPair(aa24T, aa24Mem, aa24Expected)
}

// aa24LingerCommand sends and completes one command; its request deadline is tick+10.
func (aa24Pair *aa24Pair) aa24LingerCommand(aa24Token AA24OwnerToken, aa24Tick AA23Tick) {
	aa24Pair.aa24T.Helper()
	aa24Identity := aa24FixtureCommand(1, aa24Token)
	aa24Identity.AA24RequestDeadline = aa24Tick + 10
	aa24Want(aa24Pair.aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, nil, aa24At(aa24Tick)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(aa24Tick)); aa24Err != nil || !aa24OK {
		aa24Pair.aa24T.Fatalf("next: %v %v", aa24OK, aa24Err)
	}
	aa24Want(aa24Pair.aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(aa24Tick+1)), nil)
}

// Catches an expiry list bounded by outstanding capacity: the second expiry would
// evict the first identity while its completion is still queued.
func TestAA24LingeringIdentitiesSurviveUnpolledExpiries(aa24T *aa24testing.T) {
	aa24Pair := aa24LingerPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Pair.aa24LingerCommand(1, 10)
	aa24Pair.aa24LingerCommand(2, 20)
	aa24Want(aa24T, "third send expires the second", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 3), nil, aa24At(30)), nil)
	for _, aa24Token := range []AA24OwnerToken{1, 2} {
		aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(31)
		if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion || aa24Delivery.AA24Record.AA24OwnerToken != aa24Token {
			aa24T.Fatalf("token %d: %+v %v %v", aa24Token, aa24Delivery, aa24OK, aa24Err)
		}
	}
}

// Catches a fence that clears the lingering list: the queued completion of an
// expired command must stay matchable while old data drains.
func TestAA24FenceKeepsQueuedCompletionIdentities(aa24T *aa24testing.T) {
	aa24Pair := aa24LingerPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Pair.aa24LingerCommand(1, 10)
	aa24Want(aa24T, "second send expires the first", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 2), nil, aa24At(20)), nil)
	aa24Want(aa24T, "fence", aa24Pair.aa24Host.AA24RequestStop(aa24At(22)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(23)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("expired completion after the fence: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches a fence that leaves an expiry-lingering identity non-strict: a completion
// posted after the stop request would then be accepted as pre-fence evidence.
func TestAA24FenceTightensExistingLingeringIdentities(aa24T *aa24testing.T) {
	aa24Pair := aa24LingerPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24First := aa24FixtureCommand(1, 1)
	aa24First.AA24RequestDeadline = 20
	aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24First, nil, aa24At(10)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(11)); aa24Err != nil || !aa24OK {
		aa24T.Fatalf("next: %v %v", aa24OK, aa24Err)
	}
	aa24Want(aa24T, "second send expires the first", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 2), nil, aa24At(20)), nil)
	aa24Want(aa24T, "stop fences", aa24Pair.aa24Host.AA24RequestStop(aa24At(22)), nil)
	aa24Late := AA24Record{AA24Kind: AA24RecordCompletion, AA24Status: AA24StatusAccepted, AA24CaptureSequence: 1, AA24CaptureTime: 23}
	aa24Late.aa24ApplyIdentity(aa24First)
	aa24Late.AA24Worker, aa24Late.AA24Stream = aa24FixtureWorker, aa24FixtureStream
	aa24Want(aa24T, "post-fence completion", aa24Pair.aa24Worker.aa24Lane(AA24LaneWorkerData).aa24Write(&aa24Late, 0), nil)
	_, _, aa24Err := aa24Pair.aa24Poll(24)
	aa24Want(aa24T, "post-fence completion is stale", aa24Err, ErrAA24Stale)
}

// Catches a fence that keeps the expiry-time horizon of a lingering identity: a valid
// completion queued before the fence would be discarded as stale.
func TestAA24FenceAdoptsHeadForExpiryLingeringIdentity(aa24T *aa24testing.T) {
	aa24Pair := aa24LingerPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24First := aa24FixtureCommand(1, 1)
	aa24First.AA24RequestDeadline = 20
	aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24First, nil, aa24At(10)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(11)); aa24Err != nil || !aa24OK {
		aa24T.Fatalf("next: %v %v", aa24OK, aa24Err)
	}
	aa24Want(aa24T, "second send expires the first at horizon zero", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 2), nil, aa24At(20)), nil)
	aa24Want(aa24T, "descheduled worker completes", aa24Pair.aa24Worker.AA24Complete(aa24First, AA24StatusAccepted, aa24At(15)), nil)
	aa24Want(aa24T, "stop before polling", aa24Pair.aa24Host.AA24RequestStop(aa24At(22)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(24)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("pre-fence completion: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches teardown on stop consumption alone: the worker's gap stays unread, so the
// worker could never settle its own teardown.
func TestAA24HostTeardownWaitsForTheWorkerGapToBeRead(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(11)), nil)
	aa24Want(aa24T, "worker reads stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
	aa24Want(aa24T, "gap unread", aa24Pair.aa24Host.AA24Teardown(), ErrAA24NotAcknowledged)
	aa24Want(aa24T, "worker teardown blocked", aa24Pair.aa24Worker.AA24Teardown(), ErrAA24NotAcknowledged)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordGap {
		aa24T.Fatalf("gap: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	aa24Want(aa24T, "worker teardown", aa24Pair.aa24Worker.AA24Teardown(), nil)
	aa24Want(aa24T, "host teardown", aa24Pair.aa24Host.AA24Teardown(), nil)
}

// Catches a poll that returns on the clock fence without draining control: the
// worker's gap would stay unread and graceful resynchronization could not settle.
func TestAA24ClockFencedPollDrainsControl(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	if _, _, aa24Err := aa24Pair.aa24Poll(30); aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	aa24Pair.aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(29)
	aa24Want(aa24T, "regressed host clock", aa24Err, ErrAA24Clock)
	if !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordGap || aa24Delivery.AA24DequeueTime != 0 {
		aa24T.Fatalf("gap drained on the fenced clock: %+v %v", aa24Delivery, aa24OK)
	}
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(29)), nil)
	aa24Want(aa24T, "worker reads stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(40)), nil)
	if _, aa24Err := aa24Pair.aa24Host.AA24ResyncExpected(aa24FixtureStream + 1); aa24Err != nil {
		aa24T.Fatalf("resync after the fenced drain: %v", aa24Err)
	}
}

// aa24LingerExpiring sends a command with a short deadline and lets the worker drop it
// at dequeue after the deadline, so no completion exists for it.
func (aa24Pair *aa24Pair) aa24LingerExpiring(aa24Token AA24OwnerToken, aa24Tick AA23Tick) {
	aa24Pair.aa24T.Helper()
	aa24Identity := aa24FixtureCommand(1, aa24Token)
	aa24Identity.AA24RequestDeadline = aa24Tick + 10
	aa24Want(aa24Pair.aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, nil, aa24At(aa24Tick)), nil)
	_, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(aa24Tick+11))
	aa24Want(aa24Pair.aa24T, "worker dequeue", aa24Err, ErrAA24Expired)
	if aa24OK {
		aa24Pair.aa24T.Fatal("an expired command was admitted")
	}
}

// Catches a lingering list that evicts its oldest identity when commands expire at
// worker dequeue without completions: the unread completion of the first command
// would be consumed as stale.
func TestAA24LingeringKeepsQueuedCompletionBesideSilentExpiries(aa24T *aa24testing.T) {
	aa24Pair := aa24LingerPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Pair.aa24LingerCommand(1, 10)
	aa24Pair.aa24LingerExpiring(2, 20)
	aa24Pair.aa24LingerExpiring(3, 40)
	aa24Want(aa24T, "no room to retire the third", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 4), nil, aa24At(60)), ErrAA24Capacity)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(61)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("queued completion: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	aa24Want(aa24T, "send after the data tail passed", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 4), nil, aa24At(62)), nil)
}

// Catches a poll that reads another control record while the early-control list is
// full and older data is visible: acknowledgements that overtake the data one at a
// time would breach the reorder bound and fence a valid stream.
func TestAA24PollDrainsVisibleDataBeforeTheEarlyControlListOverflows(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Refs := []AA24PlanRef{aa24Pair.aa24Publish(1, 10), aa24Pair.aa24Publish(2, 10), aa24Pair.aa24Publish(3, 10), aa24Pair.aa24Publish(4, 10)}
	for _, aa24Ref := range aa24Refs {
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(11)), nil)
		aa24Want(aa24T, "worker poll", aa24Pair.aa24Worker.AA24PollControl(aa24At(11)), nil)
	}
	if _, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24FixtureRaw(1), aa24At(12)); aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	for aa24Index, aa24Ref := range aa24Refs {
		aa24Want(aa24T, "ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(13)), nil)
		aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(14)
		if aa24Index == 3 {
			if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordRaw {
				aa24T.Fatalf("older raw drained first: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
			}
			aa24Delivery, aa24OK, aa24Err = aa24Pair.aa24Poll(14)
		}
		if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordAcknowledge {
			aa24T.Fatalf("ack %d: %+v %v %v", aa24Index, aa24Delivery, aa24OK, aa24Err)
		}
	}
	if !aa24Pair.aa24Host.AA24OutputOpen() || aa24Pair.aa24Host.aa24NextCapture != 6 {
		aa24T.Fatalf("valid stream fenced: %+v next %d", aa24Pair.aa24Host.AA24Fence(), aa24Pair.aa24Host.aa24NextCapture)
	}
}

// Catches a worker successor whose floors regress below identities the host already
// assigned but the worker never drained.
func TestAA24WorkerResyncKeepsHostAssignedFloors(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Pair.aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
	aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 7), nil, aa24At(11)), nil)
	aa24Want(aa24T, "publish second", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(2), aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset+aa24BuildBytes+8, 8, aa24At(11)), nil)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(12)), nil)
	aa24Want(aa24T, "worker reads stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(13)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Poll(14); aa24Err != nil || !aa24OK {
		aa24T.Fatalf("host reads gap: %v %v", aa24OK, aa24Err)
	}
	aa24Next := aa24FixtureStream + 1
	aa24HostNext, aa24Err := aa24Pair.aa24Host.AA24ResyncExpected(aa24Next)
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	aa24WorkerNext, aa24Err := aa24Pair.aa24Worker.AA24ResyncExpected(aa24Next, aa24Pair.aa24Host.aa24PlanFloor, aa24Pair.aa24Host.aa24TokenFloor)
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	if aa24HostNext.AA24PlanFloor != 2 || aa24HostNext.AA24TokenFloor != 7 || aa24WorkerNext.AA24PlanFloor != aa24HostNext.AA24PlanFloor ||
		aa24WorkerNext.AA24TokenFloor != aa24HostNext.AA24TokenFloor {
		aa24T.Fatalf("floors: host %+v worker %+v", aa24HostNext, aa24WorkerNext)
	}
}

// Catches a gap fence that drops outstanding identities: a completion queued before
// the gap would drain as stale and its retained evidence would be discarded.
func TestAA24GapFenceKeepsEarlierQueuedCompletion(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
	aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(11)), nil)
	aa24Pair.aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
	aa24Gap, aa24OK, aa24Err := aa24Pair.aa24Poll(12)
	if aa24Err != nil || !aa24OK || aa24Gap.AA24Record.AA24Kind != AA24RecordGap {
		aa24T.Fatalf("gap first: %+v %v %v", aa24Gap, aa24OK, aa24Err)
	}
	aa24Done, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
	if aa24Err != nil || !aa24OK || aa24Done.AA24Record.AA24Kind != AA24RecordCompletion || aa24Done.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("queued completion after the gap: %+v %v %v", aa24Done, aa24OK, aa24Err)
	}
}

// Catches a fence that drops an outstanding identity when the lingering list is
// full: the queued completion would drain as stale and be discarded.
func TestAA24FenceKeepsIdentityWhenLingeringIsFull(aa24T *aa24testing.T) {
	aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa24Config := aa24FixtureConfig
	aa24Config.AA24Lanes[AA24LaneWorkerData-1].AA24Capacity = 2
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Mem, aa24FixtureBinding(), aa24Config, aa24FixtureLimits)
	if aa24Err != nil {
		aa24T.Fatalf("init: %v", aa24Err)
	}
	aa24Pair := aa24AttachPair(aa24T, aa24Mem, aa24Expected)
	aa24Pair.aa24Publish(1, 10)
	aa24Pair.aa24Host.aa24Lingering = append(aa24Pair.aa24Host.aa24Lingering[:0], aa24Lingered{aa24Horizon: 1 << 40}, aa24Lingered{aa24Horizon: 1 << 40})
	aa24Pair.aa24Host.aa24Lingering = aa24Pair.aa24Host.aa24Lingering[:cap(aa24Pair.aa24Host.aa24Lingering)]
	aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
	aa24Want(aa24T, "complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(11)), nil)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(12)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion {
		aa24T.Fatalf("queued completion: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches a retained outstanding identity that skips the fence horizon: a completion
// posted after the fence, reported as cancelled, would be accepted.
func TestAA24FenceRetainedOutstandingRejectsPostFenceCompletion(aa24T *aa24testing.T) {
	aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa24Config := aa24FixtureConfig
	aa24Config.AA24Lanes[AA24LaneWorkerData-1].AA24Capacity = 2
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Mem, aa24FixtureBinding(), aa24Config, aa24FixtureLimits)
	if aa24Err != nil {
		aa24T.Fatalf("init: %v", aa24Err)
	}
	aa24Pair := aa24AttachPair(aa24T, aa24Mem, aa24Expected)
	aa24Pair.aa24Publish(1, 10)
	aa24Pair.aa24Host.aa24Lingering = append(aa24Pair.aa24Host.aa24Lingering[:0], aa24Lingered{aa24Horizon: 1 << 40}, aa24Lingered{aa24Horizon: 1 << 40})
	aa24Pair.aa24Host.aa24Lingering = aa24Pair.aa24Host.aa24Lingering[:cap(aa24Pair.aa24Host.aa24Lingering)]
	aa24Early := aa24Pair.aa24Admit(1, 1, 10)
	aa24Want(aa24T, "early complete", aa24Pair.aa24Worker.AA24Complete(aa24Early, AA24StatusAccepted, aa24At(10)), nil)
	aa24Identity := aa24Pair.aa24Admit(1, 2, 10)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(11)), nil)
	if len(aa24Pair.aa24Host.aa24Outstanding) != 2 {
		aa24T.Fatalf("identities not retained: %+v", aa24Pair.aa24Host.aa24Outstanding)
	}
	aa24Want(aa24T, "complete after fence", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(12)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("pre-fence completion: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	aa24Delivery, aa24OK, aa24Err = aa24Pair.aa24Poll(14)
	if aa24OK || !aa24errorstest.Is(aa24Err, ErrAA24Stale) {
		aa24T.Fatalf("post-fence completion accepted: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches a repeated stop that moves a retained cancelled identity into lingering with
// a newer horizon once polling frees room: its post-fence completion would be accepted.
func TestAA24RepeatedStopKeepsRetainedHorizonAfterRoomFrees(aa24T *aa24testing.T) {
	aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa24Config := aa24FixtureConfig
	aa24Config.AA24Lanes[AA24LaneWorkerData-1].AA24Capacity = 2
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Mem, aa24FixtureBinding(), aa24Config, aa24FixtureLimits)
	if aa24Err != nil {
		aa24T.Fatalf("init: %v", aa24Err)
	}
	aa24Pair := aa24AttachPair(aa24T, aa24Mem, aa24Expected)
	aa24Pair.aa24Publish(1, 10)
	aa24Pair.aa24Host.aa24Lingering = append(aa24Pair.aa24Host.aa24Lingering[:0], aa24Lingered{aa24Horizon: 1}, aa24Lingered{aa24Horizon: 1})
	aa24Pair.aa24Host.aa24Lingering = aa24Pair.aa24Host.aa24Lingering[:cap(aa24Pair.aa24Host.aa24Lingering)]
	aa24Early := aa24Pair.aa24Admit(1, 1, 10)
	aa24Want(aa24T, "early complete", aa24Pair.aa24Worker.AA24Complete(aa24Early, AA24StatusAccepted, aa24At(10)), nil)
	aa24Identity := aa24Pair.aa24Admit(1, 2, 10)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(11)), nil)
	aa24Want(aa24T, "complete after fence", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(12)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("pre-fence completion: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	aa24Want(aa24T, "second stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(14)), nil)
	aa24Delivery, aa24OK, aa24Err = aa24Pair.aa24Poll(15)
	if aa24OK || !aa24errorstest.Is(aa24Err, ErrAA24Stale) {
		aa24T.Fatalf("post-fence completion accepted: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches a delayed termination notice after a graceful teardown that reads the
// cleared lane mapping and panics.
func TestAA24ObserveWorkerTerminatedAfterTeardownIsSafe(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(11)), nil)
	aa24Want(aa24T, "worker reads stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
	aa24Want(aa24T, "host teardown", aa24Pair.aa24Host.AA24Teardown(), nil)
	aa24Pair.aa24Host.AA24ObserveWorkerTerminated()
	if aa24Pair.aa24Host.AA24OutputOpen() {
		aa24T.Fatal("detached host reports open output")
	}
}

// Catches a settlement or teardown that ignores a revocation queued behind the stop:
// the quarantined plan would be cleared without its exact retirement acknowledgement.
func TestAA24RevokeAfterStopMustBeReadBeforeTeardown(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24Pair.aa24Publish(1, 10)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(11)), nil)
	aa24Want(aa24T, "worker reads publish and stop", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
	aa24Want(aa24T, "late revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(13)), nil)
	aa24Want(aa24T, "worker teardown", aa24Pair.aa24Worker.AA24Teardown(), ErrAA24NotAcknowledged)
	aa24Want(aa24T, "host teardown", aa24Pair.aa24Host.AA24Teardown(), ErrAA24NotAcknowledged)
	if _, aa24Err := aa24Pair.aa24Host.AA24ResyncExpected(aa24FixtureStream + 1); !aa24errorstest.Is(aa24Err, ErrAA24NotAcknowledged) {
		aa24T.Fatalf("resync: %v", aa24Err)
	}
	aa24Want(aa24T, "worker reads revoke", aa24Pair.aa24Worker.AA24PollControl(aa24At(14)), nil)
	// The adopted plan's retirement is still unposted, so the lanes are empty yet the
	// host plan stays quarantined and nothing may settle.
	if aa24Pair.aa24Host.aa24ControlSettled() {
		aa24T.Fatal("settled past an unacknowledged adopted revocation")
	}
	aa24Want(aa24T, "host teardown before acknowledgement", aa24Pair.aa24Host.AA24Teardown(), ErrAA24NotAcknowledged)
	if _, aa24Err := aa24Pair.aa24Host.AA24ResyncExpected(aa24FixtureStream + 1); !aa24errorstest.Is(aa24Err, ErrAA24NotAcknowledged) {
		aa24T.Fatalf("resync before acknowledgement: %v", aa24Err)
	}
	aa24Want(aa24T, "acknowledge", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(15)), nil)
	aa24Want(aa24T, "host teardown before the retirement is read", aa24Pair.aa24Host.AA24Teardown(), ErrAA24NotAcknowledged)
	if aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(16); aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordAcknowledge {
		aa24T.Fatalf("retirement: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	if !aa24Pair.aa24Host.aa24ControlSettled() {
		aa24T.Fatal("not settled after the retirement was consumed")
	}
	aa24Want(aa24T, "worker teardown after read", aa24Pair.aa24Worker.AA24Teardown(), nil)
	aa24Want(aa24T, "host teardown after read", aa24Pair.aa24Host.AA24Teardown(), nil)
}

// Catches an eviction that trusts the producer snapshot taken at expiry: the worker
// captured a completion before its deadline, queued it after the host expired the
// command, and a full lingering list dropped the identity so the completion drained
// as stale.
func TestAA24LateCompletionAfterExpirySurvivesFullLingering(aa24T *aa24testing.T) {
	aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa24Config := aa24FixtureConfig
	aa24Config.AA24Lanes[AA24LaneWorkerData-1].AA24Capacity = 2
	aa24Limits := aa24FixtureLimits
	aa24Limits.AA24MaxOutstanding = 1
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Mem, aa24FixtureBinding(), aa24Config, aa24Limits)
	if aa24Err != nil {
		aa24T.Fatalf("init: %v", aa24Err)
	}
	aa24Pair := aa24AttachPair(aa24T, aa24Mem, aa24Expected)
	aa24Pair.aa24Publish(1, 5)
	aa24Identity := func(aa24Token AA24OwnerToken, aa24Tick AA23Tick) AA24CommandIdentity {
		aa24Command := aa24FixtureCommand(1, aa24Token)
		aa24Command.AA24RequestDeadline = aa24Tick + 10
		return aa24Command
	}
	aa24First := aa24Identity(1, 10)
	aa24Want(aa24T, "send first", aa24Pair.aa24Host.AA24SendCommand(aa24First, nil, aa24At(10)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(10)); aa24Err != nil || !aa24OK {
		aa24T.Fatalf("next: %v %v", aa24OK, aa24Err)
	}
	// Each later send expires the previous command on the host, filling the list.
	aa24Want(aa24T, "send second", aa24Pair.aa24Host.AA24SendCommand(aa24Identity(2, 30), nil, aa24At(30)), nil)
	aa24Want(aa24T, "send third", aa24Pair.aa24Host.AA24SendCommand(aa24Identity(3, 50), nil, aa24At(50)), nil)
	// The descheduled worker now queues the completion it captured before the deadline.
	aa24Want(aa24T, "late completion", aa24Pair.aa24Worker.AA24Complete(aa24First, AA24StatusAccepted, aa24At(15)), nil)
	aa24Want(aa24T, "fourth send finds no room", aa24Pair.aa24Host.AA24SendCommand(aa24Identity(4, 70), nil, aa24At(70)), ErrAA24Capacity)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(71)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("late completion: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches a completion shape check that ignores the unused payload and phase: a
// corrupted record would be surfaced, or retried as a buffer error, instead of
// fencing malformed framing.
func TestAA24CompletionWithPayloadOrPhaseIsFenced(aa24T *aa24testing.T) {
	aa24Cases := []struct {
		aa24Name  string
		aa24Patch func([]byte)
		aa24Dst   int
	}{
		{"phase", func(aa24Slot []byte) { aa24Slot[123] = 1 }, 16},
		{"payload fits", func(aa24Slot []byte) { aa24binarytest.LittleEndian.PutUint32(aa24Slot[4:], 2) }, 16},
		{"payload exceeds buffer", func(aa24Slot []byte) { aa24binarytest.LittleEndian.PutUint32(aa24Slot[4:], 2) }, 0},
	}
	for _, aa24Case := range aa24Cases {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
		aa24Want(aa24T, aa24Case.aa24Name+" complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(11)), nil)
		aa24Case.aa24Patch(aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerData).aa24Slot(0))
		_, aa24OK, aa24Err := aa24Pair.aa24Host.AA24Poll(make([]byte, 0, aa24Case.aa24Dst), aa24At(12))
		if aa24OK || aa24Err != ErrAA24Record || aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceCorruption {
			aa24T.Fatalf("%s: ok=%v err=%v fence=%+v", aa24Case.aa24Name, aa24OK, aa24Err, aa24Pair.aa24Host.AA24Fence())
		}
	}
	// The unpatched record is the positive control.
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
	aa24Want(aa24T, "control complete", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(11)), nil)
	if aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(12); aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion {
		aa24T.Fatalf("control: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches a completion with an invalid status that clears the active command first.
func TestAA24InvalidCompletionStatusKeepsActiveCommand(aa24T *aa24testing.T) {
	for _, aa24Status := range []AA24Status{AA24StatusPending, AA24StatusRetired + 1} {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		aa24Identity := aa24Pair.aa24Admit(1, 1, 10)
		aa24Want(aa24T, "invalid status", aa24Pair.aa24Worker.AA24Complete(aa24Identity, aa24Status, aa24At(11)), ErrAA24Record)
		aa24Want(aa24T, "valid completion", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(12)), nil)
	}
}

// Catches a clock-fenced drain that stamps a refused plan's retirement with the
// regressed tick: the host would reject it as a clock regression after the worker
// dequeued the revoke, leaving the plan quarantined.
func TestAA24FencedDrainStampsRetirementWithTrustedTick(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Refused := aa24FixtureRef(2)
	aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24Want(aa24T, "publish refused", aa24Pair.aa24Host.AA24PublishPlan(aa24Refused, aa24ArenaStart, 8, aa24At(11)), nil)
	aa24Pair.aa24Worker.aa24RefusedRefs = append(aa24Pair.aa24Worker.aa24RefusedRefs, aa24Refused)
	aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Refused, aa24At(12)), nil)
	aa24Want(aa24T, "regressed worker clock", aa24Pair.aa24Worker.AA24PollControl(aa24At(5)), ErrAA24Clock)
	for aa24Tick := AA23Tick(20); aa24Tick < 24; aa24Tick++ {
		if _, _, aa24Err := aa24Pair.aa24Poll(aa24Tick); aa24Err != nil {
			aa24T.Fatalf("host poll %d: %v", aa24Tick, aa24Err)
		}
	}
	for _, aa24Held := range aa24Pair.aa24Host.AA24QuarantinedPlans(nil) {
		if aa24Held.aa24Matches(aa24Refused) {
			aa24T.Fatal("refused plan stayed quarantined")
		}
	}
}

// Catches a retirement acknowledgement that moves a retained fence-cancelled identity
// to lingering with a fresh non-strict horizon: its post-fence completion would be
// accepted.
func TestAA24RetirementKeepsRetainedFenceHorizon(aa24T *aa24testing.T) {
	aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa24Config := aa24FixtureConfig
	aa24Config.AA24Lanes[AA24LaneWorkerData-1].AA24Capacity = 2
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Mem, aa24FixtureBinding(), aa24Config, aa24FixtureLimits)
	if aa24Err != nil {
		aa24T.Fatalf("init: %v", aa24Err)
	}
	aa24Pair := aa24AttachPair(aa24T, aa24Mem, aa24Expected)
	aa24Ref := aa24Pair.aa24Publish(1, 10)
	aa24Pair.aa24Host.aa24Lingering = append(aa24Pair.aa24Host.aa24Lingering[:0], aa24Lingered{aa24Horizon: 1}, aa24Lingered{aa24Horizon: 1})
	aa24Pair.aa24Host.aa24Lingering = aa24Pair.aa24Host.aa24Lingering[:cap(aa24Pair.aa24Host.aa24Lingering)]
	aa24Early := aa24Pair.aa24Admit(1, 1, 10)
	aa24Want(aa24T, "early complete", aa24Pair.aa24Worker.AA24Complete(aa24Early, AA24StatusAccepted, aa24At(10)), nil)
	aa24Identity := aa24Pair.aa24Admit(1, 2, 10)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(11)), nil)
	aa24Want(aa24T, "complete after fence", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusAccepted, aa24At(12)), nil)
	// The older evidence drains first, so the retirement finds lingering room.
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(13)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("older evidence: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
	aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(14)), nil)
	aa24Want(aa24T, "worker reads stop and revoke", aa24Pair.aa24Worker.AA24PollControl(aa24At(15)), nil)
	aa24Want(aa24T, "ack", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(16)), nil)
	for aa24Tick := AA23Tick(17); aa24Tick < 24; aa24Tick++ {
		aa24Delivery, aa24OK, aa24Err = aa24Pair.aa24Poll(aa24Tick)
		if aa24OK && aa24Delivery.AA24Record.AA24Kind == AA24RecordCompletion && aa24Delivery.AA24Record.AA24OwnerToken == 2 {
			aa24T.Fatalf("post-fence completion accepted: %+v %v", aa24Delivery, aa24Err)
		}
	}
}

// Catches an idle service tick reused for a new backlog, and a timer reset by
// another enqueue while the first acknowledgement remains unread.
func TestAA24ControlDeadlineStartsWithBacklogAfterIdle(aa24T *aa24testing.T) {
	for _, aa24Case := range []struct {
		aa24Name    string
		aa24Consume bool
		aa24Append  bool
	}{
		{aa24Name: "host consumes acknowledgement", aa24Consume: true},
		{aa24Name: "unread acknowledgement expires"},
		{aa24Name: "another acknowledgement preserves deadline", aa24Append: true},
	} {
		aa24T.Run(aa24Case.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
			aa24Refs := []AA24PlanRef{aa24FixtureRef(1), aa24FixtureRef(2)}
			for aa24Index := range aa24Refs {
				aa24Refs[aa24Index].AA24PlanExpiry = 12
				aa24Want(aa24T, "publish", aa24Pair.aa24Host.AA24PublishPlan(aa24Refs[aa24Index], aa24ArenaStart, 8, aa24At(10)), nil)
			}
			aa24Want(aa24T, "refuse expired plans", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
			if aa24Pair.aa24Worker.AA24PublishRefused() != 2 || len(aa24Pair.aa24Worker.aa24Adopted) != 0 {
				aa24T.Fatal("positive control: both plans must be refused")
			}
			aa24Want(aa24T, "empty service", aa24Pair.aa24Worker.AA24PollControl(aa24At(20)), nil)
			aa24Want(aa24T, "revoke after idle", aa24Pair.aa24Host.AA24RevokePlan(aa24Refs[0], aa24At(200)), nil)
			aa24Want(aa24T, "enqueue retirement during service", aa24Pair.aa24Worker.AA24PollControl(aa24At(200)), nil)
			if !aa24Pair.aa24Worker.AA24OutputOpen() {
				aa24T.Fatalf("new backlog inherited idle time: %+v", aa24Pair.aa24Worker.AA24Fence())
			}
			if aa24Case.aa24Consume {
				aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(201)
				if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordAcknowledge || aa24Delivery.AA24Record.AA24Plan != aa24Refs[0].AA24Plan {
					aa24T.Fatalf("retirement: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
				}
			}
			if aa24Case.aa24Append {
				aa24Want(aa24T, "second revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Refs[1], aa24At(250)), nil)
				aa24Want(aa24T, "second retirement", aa24Pair.aa24Worker.AA24PollControl(aa24At(250)), nil)
			}
			aa24Want(aa24T, "before deadline", aa24Pair.aa24Worker.AA24PollControl(aa24At(299)), nil)
			if !aa24Pair.aa24Worker.AA24OutputOpen() {
				aa24T.Fatalf("closed before full budget: %+v", aa24Pair.aa24Worker.AA24Fence())
			}
			aa24Want(aa24T, "at deadline", aa24Pair.aa24Worker.AA24PollControl(aa24At(300)), nil)
			if aa24Case.aa24Consume {
				if !aa24Pair.aa24Worker.AA24OutputOpen() {
					aa24T.Fatalf("consumed backlog expired: %+v", aa24Pair.aa24Worker.AA24Fence())
				}
			} else if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceControlDeadline {
				aa24T.Fatalf("unread backlog did not expire: %+v", aa24Pair.aa24Worker.AA24Fence())
			}
		})
	}
}

// Catches an ordinal accounting that appends an early control ordinal again: two
// retirement acknowledgements carrying the same ordinal would both be accepted.
func TestAA24DuplicateEarlyControlOrdinalIsRejected(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Host := aa24Pair.aa24Host
	if !aa24Host.aa24AccountSequence(2, true) {
		aa24T.Fatal("first early control ordinal must be explained")
	}
	if aa24Host.aa24AccountSequence(2, true) {
		aa24T.Fatal("a repeated early control ordinal was accepted")
	}
	if len(aa24Host.aa24Explained) != 1 {
		aa24T.Fatalf("explained list holds %d entries, want 1", len(aa24Host.aa24Explained))
	}
	if !aa24Host.aa24AccountSequence(1, false) || aa24Host.aa24NextCapture != 3 {
		aa24T.Fatalf("positive control: next capture %d, want 3", aa24Host.aa24NextCapture)
	}
}

// Catches a completion that skips the deadline check: with equality meaning expired,
// completing exactly at the owner or request deadline without a prior admitted output
// must be refused before a sequence is allocated and must clear the active command.
func TestAA24CompleteAtDeadlineIsRefused(aa24T *aa24testing.T) {
	for _, aa24Case := range []struct {
		aa24Name   string
		aa24Mutate func(*AA24CommandIdentity)
	}{
		{"request deadline", func(aa24Identity *AA24CommandIdentity) { aa24Identity.AA24RequestDeadline = 100 }},
		{"owner expiry", func(aa24Identity *AA24CommandIdentity) { aa24Identity.AA24OwnerExpiry = 100 }},
	} {
		aa24T.Run(aa24Case.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			aa24Pair.aa24Publish(1, 10)
			aa24Identity := aa24FixtureCommand(1, 1)
			aa24Case.aa24Mutate(&aa24Identity)
			aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24Identity, []byte{1}, aa24At(11)), nil)
			if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(12)); aa24Err != nil || !aa24OK {
				aa24T.Fatalf("next command: %v %v", aa24OK, aa24Err)
			}
			aa24Next := aa24Pair.aa24Worker.aa24NextSequence
			aa24Want(aa24T, "complete at the deadline", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusRetired, aa24At(100)), ErrAA24Expired)
			if aa24Pair.aa24Worker.aa24NextSequence != aa24Next || aa24Pair.aa24Worker.aa24HasActive {
				aa24T.Fatalf("sequence %d (want %d) active %v", aa24Pair.aa24Worker.aa24NextSequence, aa24Next, aa24Pair.aa24Worker.aa24HasActive)
			}
		})
	}
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Identity := aa24Pair.aa24Admit(1, 1, 11)
	aa24Want(aa24T, "positive control: complete before the deadline", aa24Pair.aa24Worker.AA24Complete(aa24Identity, AA24StatusRetired, aa24At(12)), nil)
}

// Catches a latched clock-fence service branch that skips the retirement retry: the
// queued exact retirement of a refused publication must be emitted once the worker
// control lane drains, even while the regressed clock stays latched.
func TestAA24ClockFenceStillRetriesRetirement(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Worker := aa24Pair.aa24Worker
	for aa24Plan := AA23PlanGeneration(1); aa24Plan <= 2; aa24Plan++ {
		aa24Tick := AA23Tick(10 * aa24Plan)
		aa24Ref := aa24Pair.aa24Publish(aa24Plan, aa24Tick)
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(aa24Tick+1)), nil)
		aa24Want(aa24T, "worker poll", aa24Worker.AA24PollControl(aa24At(aa24Tick+1)), nil)
		aa24Want(aa24T, "ack", aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(aa24Tick+2)), nil)
	}
	aa24Refused := aa24FixtureRef(3)
	aa24Worker.aa24RefusedRefs = append(aa24Worker.aa24RefusedRefs, aa24Refused)
	aa24Record := AA24Record{AA24Kind: AA24RecordRevoke}
	aa24Record.aa24ApplyRef(aa24Refused)
	aa24Want(aa24T, "revoke of the refused plan", aa24Pair.aa24Host.aa24WriteControl(&aa24Record), nil)
	aa24Want(aa24T, "worker poll on saturated control", aa24Worker.AA24PollControl(aa24At(23)), nil)
	if !aa24HostControlUnread(aa24T, aa24Worker) {
		aa24T.Fatal("revocation must stay queued while its retirement is unposted")
	}
	for aa24Tick := AA23Tick(24); aa24Tick < 27; aa24Tick++ {
		if _, aa24OK, aa24Err := aa24Pair.aa24Poll(aa24Tick); !aa24OK || aa24Err != nil {
			aa24T.Fatalf("host poll: %v %v", aa24OK, aa24Err)
		}
	}
	aa24Want(aa24T, "regressed worker clock", aa24Worker.AA24PollControl(aa24At(5)), ErrAA24Clock)
	if len(aa24Worker.aa24RetireRetry) != 0 || len(aa24Worker.aa24RefusedRefs) != 0 {
		aa24T.Fatalf("retirement not emitted under the latched fence: retry %d refused %d", len(aa24Worker.aa24RetireRetry), len(aa24Worker.aa24RefusedRefs))
	}
}

// Catches: leaving an expired active command in place so AA24NextCommand never
// dequeues later work.
func TestAA24NextCommandRetiresExpiredActiveCommand(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Short := aa24FixtureCommand(1, 1)
	aa24Short.AA24RequestDeadline = 50
	aa24Want(aa24T, "send short", aa24Pair.aa24Host.AA24SendCommand(aa24Short, []byte{1}, aa24At(10)), nil)
	aa24Want(aa24T, "send next", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 2), []byte{2}, aa24At(10)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(11)); !aa24OK || aa24Err != nil {
		aa24T.Fatalf("first: %v %v", aa24OK, aa24Err)
	}
	if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(49)); aa24OK || aa24Err != nil {
		aa24T.Fatalf("still active: %v %v", aa24OK, aa24Err)
	}
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(50))
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24OwnerToken != 2 {
		aa24T.Fatalf("after expiry: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches: reporting a buffer shortfall before refusing a payload on a
// payload-free control record, which leaves the corrupt slot retryable.
func TestAA24ControlRecordPayloadFencesWithZeroCapacityDestination(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
	aa24Lane := aa24Pair.aa24Host.aa24Lane(AA24LaneWorkerControl)
	aa24binarytest.LittleEndian.PutUint32(aa24Lane.aa24Slot(0)[4:], 4)
	_, aa24OK, aa24Err := aa24Pair.aa24Host.AA24Poll(nil, aa24At(11))
	aa24Want(aa24T, "payload on control", aa24Err, ErrAA24Record)
	if aa24OK || aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceCorruption {
		aa24T.Fatalf("fence %+v", aa24Pair.aa24Host.AA24Fence())
	}
}

// Catches an acknowledgement path that returns the latched clock error before the
// retirement of an already revoked plan, which leaves the host plan quarantined.
func TestAA24RetirementAcceptedAfterLatchedClockFence(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24Pair.aa24Publish(1, 10)
	aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(11)), nil)
	aa24Want(aa24T, "regressed worker clock consumes the revocation", aa24Pair.aa24Worker.AA24PollControl(aa24At(5)), ErrAA24Clock)
	aa24Want(aa24T, "retirement under the latched fence", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(5)), nil)
	aa24Want(aa24T, "second retirement", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(5)), ErrAA24Stale)
	for aa24Tick := AA23Tick(20); aa24Tick < 24; aa24Tick++ {
		if _, _, aa24Err := aa24Pair.aa24Poll(aa24Tick); aa24Err != nil {
			aa24T.Fatalf("host poll %d: %v", aa24Tick, aa24Err)
		}
	}
	if aa24Left := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Left) != 0 {
		aa24T.Fatalf("plan stayed quarantined: %+v", aa24Left)
	}
}

// Catches a fenced drain that discards a queued publication: its exact revocation
// would find nothing to retire and the host would keep the plan indefinitely.
func TestAA24FencedDrainKeepsRefusedPublication(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24FixtureRef(1)
	aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24Want(aa24T, "publish stays queued", aa24Pair.aa24Host.AA24PublishPlan(aa24Ref, aa24ArenaStart, 8, aa24At(10)), nil)
	// A completion attempt reads the worker clock without draining the control lane.
	aa24Want(aa24T, "clock read", aa24Pair.aa24Worker.AA24Complete(aa24FixtureCommand(1, 1), AA24StatusAccepted, aa24At(10)), ErrAA24Mismatch)
	aa24Want(aa24T, "clock fault before the drain", aa24Pair.aa24Worker.AA24PollControl(aa24At(5)), ErrAA24Clock)
	if len(aa24Pair.aa24Worker.aa24Adopted) != 0 || len(aa24Pair.aa24Worker.aa24RefusedRefs) != 1 {
		aa24T.Fatalf("adopted %d refused %d", len(aa24Pair.aa24Worker.aa24Adopted), len(aa24Pair.aa24Worker.aa24RefusedRefs))
	}
	aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(11)), nil)
	aa24Want(aa24T, "fenced worker retires the revocation", aa24Pair.aa24Worker.AA24PollControl(aa24At(5)), ErrAA24Clock)
	for aa24Tick := AA23Tick(20); aa24Tick < 24; aa24Tick++ {
		if _, _, aa24Err := aa24Pair.aa24Poll(aa24Tick); aa24Err != nil {
			aa24T.Fatalf("host poll %d: %v", aa24Tick, aa24Err)
		}
	}
	if aa24Left := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Left) != 0 {
		aa24T.Fatalf("plan stayed quarantined: %+v", aa24Left)
	}
}

// Catches a host revoke that returns the latched host clock error before queueing
// the revocation of published work.
func TestAA24HostRevokeQueuedUnderLatchedClockFence(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24Pair.aa24Publish(1, 10)
	_, _, aa24Err := aa24Pair.aa24Poll(5)
	aa24Want(aa24T, "regressed host clock", aa24Err, ErrAA24Clock)
	aa24Want(aa24T, "revoke on a fenced clock", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(5)), nil)
	aa24Want(aa24T, "worker reads the revocation", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
	aa24Want(aa24T, "retirement", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(13)), nil)
}

// Catches a drain that decides fencing once per pass from the clock alone: a
// publication queued before the worker learns of a data-lane gap fence would be
// adopted and stay quarantined until an explicit acknowledgement.
func TestAA24PublicationQueuedBeforeGapFenceIsRefused(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24FixtureRef(1)
	aa24ArenaStart := aa24Pair.aa24Host.aa24Lanes[0].aa24ArenaLayout.AA24ArenaOffset + aa24BuildBytes
	aa24Want(aa24T, "publish stays queued", aa24Pair.aa24Host.AA24PublishPlan(aa24Ref, aa24ArenaStart, 8, aa24At(10)), nil)
	aa24Pair.aa24Worker.aa24RaiseLocalGap(AA24FenceCounter)
	if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceCounter {
		aa24T.Fatalf("fence %+v", aa24Pair.aa24Worker.AA24Fence())
	}
	aa24Want(aa24T, "worker drains under the gap fence", aa24Pair.aa24Worker.AA24PollControl(aa24At(11)), nil)
	if len(aa24Pair.aa24Worker.aa24Adopted) != 0 || len(aa24Pair.aa24Worker.aa24RefusedRefs) != 1 {
		aa24T.Fatalf("adopted %d refused %d", len(aa24Pair.aa24Worker.aa24Adopted), len(aa24Pair.aa24Worker.aa24RefusedRefs))
	}
	aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(12)), nil)
	aa24Want(aa24T, "worker retires the refused plan", aa24Pair.aa24Worker.AA24PollControl(aa24At(12)), nil)
	for aa24Tick := AA23Tick(20); aa24Tick < 26; aa24Tick++ {
		_, _, _ = aa24Pair.aa24Poll(aa24Tick)
	}
	if aa24Left := aa24Pair.aa24Host.AA24QuarantinedPlans(nil); len(aa24Left) != 0 {
		aa24T.Fatalf("plan stayed quarantined: %+v", aa24Left)
	}
}

// TestAA24ObserveRawRefusesBeforeSideEffects pins the selected-observation ingress:
// each refusal leaves the sequence, capture time, fence and loss counter untouched,
// and an exact active selection is the only accepted selection.
func TestAA24ObserveRawRefusesBeforeSideEffects(aa24T *aa24testing.T) {
	type aa24Case struct {
		aa24Name   string
		aa24Active bool
		aa24Build  func(aa24Active AA24CommandIdentity) AA24Observation
		aa24Want   error
	}
	aa24Select := func(aa24Mutate func(*AA24Observation, *AA24CommandIdentity)) func(AA24CommandIdentity) AA24Observation {
		return func(aa24Active AA24CommandIdentity) AA24Observation {
			aa24Observation := aa24FixtureRaw(7)
			aa24Observation.AA24Transaction = aa24Active.AA24Transaction
			aa24Observation.AA24Selection = aa24Active
			aa24Mutate(&aa24Observation, &aa24Observation.AA24Selection)
			return aa24Observation
		}
	}
	aa24Cases := []aa24Case{
		{"mismatched transaction", true, aa24Select(func(aa24O *AA24Observation, _ *AA24CommandIdentity) { aa24O.AA24Transaction++ }), ErrAA24Record},
		{"selection without active command", false, aa24Select(func(*AA24Observation, *AA24CommandIdentity) {}), ErrAA24Mismatch},
		{"selection for a different command", true, aa24Select(func(aa24O *AA24Observation, aa24S *AA24CommandIdentity) {
			aa24S.AA24OwnerToken++
		}), ErrAA24Mismatch},
		{"selection with a different plan", true, aa24Select(func(_ *AA24Observation, aa24S *AA24CommandIdentity) {
			aa24S.AA24Ref.AA24Plan++
		}), ErrAA24Mismatch},
		{"selection missing owner token", true, aa24Select(func(_ *AA24Observation, aa24S *AA24CommandIdentity) {
			aa24S.AA24OwnerToken = 0
		}), ErrAA24Record},
		{"zero transaction", true, aa24Select(func(aa24O *AA24Observation, _ *AA24CommandIdentity) {
			aa24O.AA24Selection = AA24CommandIdentity{}
			aa24O.AA24Transaction = 0
		}), ErrAA24Record},
		{"control direction", true, aa24Select(func(aa24O *AA24Observation, _ *AA24CommandIdentity) {
			aa24O.AA24Selection = AA24CommandIdentity{}
			aa24O.AA24Direction = AA24DirectionControl
		}), ErrAA24Record},
		{"direction out of range", true, aa24Select(func(aa24O *AA24Observation, _ *AA24CommandIdentity) {
			aa24O.AA24Selection = AA24CommandIdentity{}
			aa24O.AA24Direction = AA24DirectionEcho + 1
		}), ErrAA24Record},
		{"phase out of range", true, aa24Select(func(aa24O *AA24Observation, _ *AA24CommandIdentity) {
			aa24O.AA24Selection = AA24CommandIdentity{}
			aa24O.AA24Phase = aa24MaxPhase + 1
		}), ErrAA24Record},
	}
	for _, aa24Case := range aa24Cases {
		aa24T.Run(aa24Case.aa24Name, func(aa24T *aa24testing.T) {
			aa24Pair := aa24NewPair(aa24T)
			var aa24Active AA24CommandIdentity
			if aa24Case.aa24Active {
				aa24Pair.aa24Publish(1, 10)
				aa24Active = aa24Pair.aa24Admit(1, 1, 10)
			} else {
				aa24Active = aa24FixtureCommand(1, 1)
			}
			aa24Worker := aa24Pair.aa24Worker
			aa24Next, aa24Last, aa24Lost, aa24Fence := aa24Worker.aa24NextSequence, aa24Worker.aa24LastCapture, aa24Worker.aa24Lost, aa24Worker.aa24Fence
			aa24Got, aa24Err := aa24Worker.AA24ObserveRaw(aa24Case.aa24Build(aa24Active), aa24At(11))
			aa24Want(aa24T, "refusal", aa24Err, aa24Case.aa24Want)
			if aa24Got != 0 || aa24Worker.aa24NextSequence != aa24Next || aa24Worker.aa24LastCapture != aa24Last ||
				aa24Worker.aa24Lost != aa24Lost || aa24Worker.aa24Fence != aa24Fence || !aa24Worker.AA24OutputOpen() {
				aa24T.Fatalf("refusal had side effects: seq=%d next=%d last=%d lost=%d fence=%+v", aa24Got, aa24Worker.aa24NextSequence, aa24Worker.aa24LastCapture, aa24Worker.aa24Lost, aa24Worker.aa24Fence)
			}
		})
	}
	aa24T.Run("exact active selection is accepted", func(aa24T *aa24testing.T) {
		aa24Pair := aa24NewPair(aa24T)
		aa24Pair.aa24Publish(1, 10)
		aa24Active := aa24Pair.aa24Admit(1, 1, 10)
		aa24Observation := aa24FixtureRaw(7)
		aa24Observation.AA24Transaction = aa24Active.AA24Transaction
		aa24Observation.AA24Selection = aa24Active
		aa24Got, aa24Err := aa24Pair.aa24Worker.AA24ObserveRaw(aa24Observation, aa24At(11))
		aa24Want(aa24T, "accepted", aa24Err, nil)
		if aa24Got == 0 || aa24Pair.aa24Worker.aa24LastCapture != 11 {
			aa24T.Fatalf("sequence %d capture %d", aa24Got, aa24Pair.aa24Worker.aa24LastCapture)
		}
	})
}

func aa24HostControlUnread(aa24T *aa24testing.T, aa24Worker *AA24Worker) bool {
	aa24T.Helper()
	aa24Produced, aa24Read, aa24Err := aa24Worker.aa24Lane(AA24LaneHostControl).aa24Indices()
	if aa24Err != nil {
		aa24T.Fatal(aa24Err)
	}
	return aa24Produced != aa24Read
}

// Catches releasing a late revocation's input slot before its retirement is posted:
// with the worker control lane drained by the host, the host must not settle or tear
// down while the retirement is still unwritten, and must settle once it is.
func TestAA24SettlementWaitsForUnpostedRetirement(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Worker := aa24Pair.aa24Worker
	for aa24Plan := AA23PlanGeneration(1); aa24Plan <= 2; aa24Plan++ {
		aa24Tick := AA23Tick(10 * aa24Plan)
		aa24Ref := aa24Pair.aa24Publish(aa24Plan, aa24Tick)
		aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(aa24Tick+1)), nil)
		aa24Want(aa24T, "worker poll", aa24Worker.AA24PollControl(aa24At(aa24Tick+1)), nil)
		aa24Want(aa24T, "ack", aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(aa24Tick+2)), nil)
	}
	aa24Refused := aa24FixtureRef(3)
	aa24Worker.aa24RefusedRefs = append(aa24Worker.aa24RefusedRefs, aa24Refused)
	aa24Host := aa24Pair.aa24Host
	aa24Want(aa24T, "stop", aa24Host.AA24RequestStop(aa24At(22)), nil)
	aa24Record := AA24Record{AA24Kind: AA24RecordRevoke}
	aa24Record.aa24ApplyRef(aa24Refused)
	aa24Want(aa24T, "late revoke", aa24Host.aa24WriteControl(&aa24Record), nil)
	aa24Want(aa24T, "worker poll", aa24Worker.AA24PollControl(aa24At(23)), nil)
	// The host drains the worker control lane to empty; the retirement is still unposted.
	aa24Control := aa24Worker.aa24Lane(AA24LaneWorkerControl)
	aa24Produced, _, _ := aa24Control.aa24Indices()
	aa24HostSide := aa24Host.aa24Lane(AA24LaneWorkerControl)
	for {
		_, aa24Consumer, _ := aa24HostSide.aa24Indices()
		if aa24Consumer == aa24Produced {
			break
		}
		if _, aa24OK, aa24Err := aa24Host.AA24Poll(make([]byte, 0, 16), aa24At(24)); !aa24OK || aa24Err != nil {
			aa24T.Fatalf("host poll: %v %v", aa24OK, aa24Err)
		}
	}
	if aa24Host.aa24ControlSettled() {
		aa24T.Fatal("settled past an unposted retirement")
	}
	aa24Want(aa24T, "teardown refused", aa24Host.AA24Teardown(), ErrAA24NotAcknowledged)
	aa24Want(aa24T, "worker retry", aa24Worker.AA24PollControl(aa24At(25)), nil)
	// The injected reference is unknown to the host list, so the read is dropped as stale.
	if _, _, aa24Err := aa24Host.AA24Poll(make([]byte, 0, 16), aa24At(26)); aa24Err != ErrAA24Stale {
		aa24T.Fatalf("retirement not read: %v", aa24Err)
	}
	if !aa24Host.aa24ControlSettled() {
		aa24T.Fatal("not settled after the retirement was read")
	}
}

// Catches guards seeded independently per endpoint: two endpoints handed readings
// from different nonzero domains would both accept them and compare incomparable
// deadlines and capture times. Each endpoint is varied alone while the other is valid.
func TestAA24SharedClockDomainRejectsForeignReadings(aa24T *aa24testing.T) {
	aa24Foreign := AA23ClockReading{AA23Domain: aa24FixtureDomain + 1, AA23Tick: 11}
	aa24Fresh := func() *aa24Pair {
		aa24Mem, aa24Expected := aa24FixtureMapping(aa24T)
		aa24Expected.AA24ClockDomain = aa24FixtureDomain
		return aa24AttachPair(aa24T, aa24Mem, aa24Expected)
	}
	aa24Pair := aa24Fresh()
	aa24Want(aa24T, "worker same domain", aa24Pair.aa24Worker.AA24PollControl(aa24At(10)), nil)
	aa24Want(aa24T, "host same domain", aa24Pair.aa24Host.AA24RequestStop(aa24At(10)), nil)

	aa24Pair = aa24Fresh()
	aa24Want(aa24T, "worker foreign domain", aa24Pair.aa24Worker.AA24PollControl(aa24Foreign), ErrAA24Clock)
	if aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceClock {
		aa24T.Fatalf("worker fence %+v", aa24Pair.aa24Worker.AA24Fence())
	}
	aa24Pair = aa24Fresh()
	aa24Want(aa24T, "host foreign domain", aa24Pair.aa24Host.AA24PublishPlan(aa24FixtureRef(1), 0, 0, aa24Foreign), ErrAA24Clock)
	if aa24Pair.aa24Host.AA24Fence().AA24Reason != AA24FenceClock {
		aa24T.Fatalf("host fence %+v", aa24Pair.aa24Host.AA24Fence())
	}
}

// Catches a command shape check that ignores the phase: a corrupted command slot
// would be delivered to the output path instead of fencing the mapping.
func TestAA24CommandWithPhaseIsFenced(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Pair.aa24Publish(1, 10)
	aa24Want(aa24T, "send", aa24Pair.aa24Host.AA24SendCommand(aa24FixtureCommand(1, 1), nil, aa24At(11)), nil)
	aa24Pair.aa24Host.aa24Lane(AA24LaneHostData).aa24Slot(0)[123] = 1
	_, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(12))
	if aa24OK || aa24Err != ErrAA24Record || aa24Pair.aa24Worker.AA24Fence().AA24Reason != AA24FenceCorruption {
		aa24T.Fatalf("ok=%v err=%v fence=%+v", aa24OK, aa24Err, aa24Pair.aa24Worker.AA24Fence())
	}
}

// Catches eviction on an empty data lane: with a worker paused before it publishes a
// completion captured before the deadline, the identity must stay matchable.
func TestAA24PausedWorkerCompletionSurvivesEvictionAttempt(aa24T *aa24testing.T) {
	aa24Mem := aa24FixtureMemory(aa24FixtureBytes())
	aa24Config := aa24FixtureConfig
	aa24Config.AA24Lanes[AA24LaneWorkerData-1].AA24Capacity = 2
	aa24Limits := aa24FixtureLimits
	aa24Limits.AA24MaxOutstanding = 1
	aa24Expected, _, aa24Err := AA24InitMapping(aa24Mem, aa24FixtureBinding(), aa24Config, aa24Limits)
	if aa24Err != nil {
		aa24T.Fatalf("init: %v", aa24Err)
	}
	aa24Pair := aa24AttachPair(aa24T, aa24Mem, aa24Expected)
	aa24Pair.aa24Publish(1, 5)
	aa24Identity := func(aa24Token AA24OwnerToken, aa24Tick AA23Tick) AA24CommandIdentity {
		aa24Command := aa24FixtureCommand(1, aa24Token)
		aa24Command.AA24RequestDeadline = aa24Tick + 10
		return aa24Command
	}
	aa24First := aa24Identity(1, 10)
	aa24Want(aa24T, "send first", aa24Pair.aa24Host.AA24SendCommand(aa24First, nil, aa24At(10)), nil)
	if _, aa24OK, aa24Err := aa24Pair.aa24Worker.AA24NextCommand(make([]byte, 0, 16), aa24At(10)); aa24Err != nil || !aa24OK {
		aa24T.Fatalf("next: %v %v", aa24OK, aa24Err)
	}
	aa24Want(aa24T, "send second", aa24Pair.aa24Host.AA24SendCommand(aa24Identity(2, 30), nil, aa24At(30)), nil)
	aa24Want(aa24T, "send third", aa24Pair.aa24Host.AA24SendCommand(aa24Identity(3, 50), nil, aa24At(50)), nil)
	// Eviction is attempted while the data lane is still empty and the worker paused.
	aa24Want(aa24T, "eviction refused", aa24Pair.aa24Host.AA24SendCommand(aa24Identity(4, 70), nil, aa24At(70)), ErrAA24Capacity)
	aa24Want(aa24T, "late completion", aa24Pair.aa24Worker.AA24Complete(aa24First, AA24StatusAccepted, aa24At(15)), nil)
	aa24Delivery, aa24OK, aa24Err := aa24Pair.aa24Poll(71)
	if aa24Err != nil || !aa24OK || aa24Delivery.AA24Record.AA24Kind != AA24RecordCompletion || aa24Delivery.AA24Record.AA24OwnerToken != 1 {
		aa24T.Fatalf("late completion: %+v %v %v", aa24Delivery, aa24OK, aa24Err)
	}
}

// Catches a teardown that clears a revoked plan before its retirement was posted,
// which would leave the host quarantine unsettled for good.
func TestAA24TeardownWaitsForPendingRetirement(aa24T *aa24testing.T) {
	aa24Pair := aa24NewPair(aa24T)
	aa24Ref := aa24Pair.aa24Publish(1, 10)
	aa24Want(aa24T, "revoke", aa24Pair.aa24Host.AA24RevokePlan(aa24Ref, aa24At(11)), nil)
	aa24Want(aa24T, "stop", aa24Pair.aa24Host.AA24RequestStop(aa24At(12)), nil)
	aa24Want(aa24T, "worker drain", aa24Pair.aa24Worker.AA24PollControl(aa24At(13)), nil)
	aa24Want(aa24T, "teardown before retirement", aa24Pair.aa24Worker.AA24Teardown(), ErrAA24NotAcknowledged)
	aa24Want(aa24T, "retire", aa24Pair.aa24Worker.AA24AcknowledgeRevocation(aa24Ref, aa24At(14)), nil)
	aa24Want(aa24T, "teardown after retirement", aa24Pair.aa24Worker.AA24Teardown(), nil)
}
