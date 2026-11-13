package localworker

import (
	"encoding/binary"
	"errors"
	"math"
	"sync/atomic"
	"unsafe"
)

// This file is the IPC reference model of the offline local-worker ABI: a byte
// layout validator, SPSC lanes and an independent control lane over memory the
// caller supplies. It maps nothing and opens no device. Shared storage holds only
// little-endian fixed-width fields, so no Go pointer, slice or string header ever
// crosses it, and no peer offset becomes an address before it passes validation.
//
// Status: Hypothesis. Index publication uses sync/atomic, but tests in one process
// prove nothing about cross-process ordering on any Linux, architecture or
// toolchain; that audit and real descriptor mapping remain Unknown. Capacities,
// budgets and ticks are fixture parameters, never product timing.
//
// Two model decisions narrow the layout without changing a documented field:
// the build identity occupies the first bytes of the plan arena, because the
// header has no free field for it, and an exact stop for the stream acknowledges a
// gap, because no separate acknowledgement kind exists. One slot of each control
// lane is reserved so a gap and a stop always fit.

const (
	// ABIVersion is the only accepted layout version.
	ABIVersion uint32 = 1

	headerBytes     uint64 = 64
	descriptorBytes uint64 = 64
	envelopeBytes   uint32 = 128
	buildBytes      uint64 = 32
	alignment       uint64 = 8
	laneCount       uint32 = 4

	headerMagic        = "LWB1"
	offsetVersion      = 4
	offsetHeaderBytes  = 8
	offsetTotalBytes   = 12
	offsetWorker       = 16
	offsetStream       = 24
	offsetLaneCount    = 32
	offsetDescBytes    = 36
	offsetDescTable    = 40
	offsetArena        = 48
	offsetArenaBytes   = 56
	descOffsetSlot     = 4
	descOffsetCapacity = 8
	descOffsetReserved = 12
	descOffsetRegion   = 16
	descOffsetProducer = 24
	descOffsetConsumer = 32
	descOffsetTail     = 40

	capacityCeiling uint32 = 1 << 20
	tableCeiling    uint32 = 1 << 16
	reservedSlots   uint64 = 1
)

// Refusals are sentinels. A validation refusal has no effect; a refusal raised by
// a live lane fences the endpoint where its operation says so.
var (
	ErrIPCInvalidLimits = errors.New("localworker: invalid ABI limits or configuration")
	ErrIPCIdentity      = errors.New("localworker: zero or mismatched worker or stream identity")
	ErrABI              = errors.New("localworker: ABI magic, version or fixed size mismatch")
	ErrBuild            = errors.New("localworker: build identity mismatch")
	ErrMappingSize      = errors.New("localworker: mapping size mismatch")
	ErrAlignment        = errors.New("localworker: misaligned mapping or offset")
	ErrBounds           = errors.New("localworker: region outside the mapping")
	ErrOverlap          = errors.New("localworker: regions overlap")
	ErrLane             = errors.New("localworker: invalid lane kind, slot size or capacity")
	ErrReserved         = errors.New("localworker: reserved bytes are not zero")
	ErrIndex            = errors.New("localworker: lane index invariant broken")
	ErrOwnership        = errors.New("localworker: role does not own this lane index")
	ErrFull             = errors.New("localworker: lane is full")
	ErrControlFull      = errors.New("localworker: control lane is saturated")
	ErrCounter          = errors.New("localworker: counter refuses to wrap")
	ErrTruncated        = errors.New("localworker: record does not fit its slot")
	ErrIPCOversize      = errors.New("localworker: payload exceeds the configured cap")
	ErrBuffer           = errors.New("localworker: destination buffer is too small")
	ErrRecord           = errors.New("localworker: malformed record")
	ErrStale            = errors.New("localworker: stale identity dropped")
	ErrIPCRevoked       = errors.New("localworker: plan revocation observed")
	ErrIPCExpired       = errors.New("localworker: owner, plan or request deadline expired")
	ErrOutputFenced     = errors.New("localworker: output decisions are fenced")
	ErrGap              = errors.New("localworker: protocol gap raised")
	ErrIPCClock         = errors.New("localworker: clock regression or impossible capture time")
	ErrNotFenced        = errors.New("localworker: no fence to acknowledge")
	ErrNotAcknowledged  = errors.New("localworker: fence acknowledgement is unsettled")
	ErrIPCCapacity      = errors.New("localworker: bounded table exhausted")
	ErrDetached         = errors.New("localworker: endpoint is torn down")
	ErrMismatch         = errors.New("localworker: exact command identity mismatch")
)

// LaneKind names the four lanes by their documented descriptor value.
type LaneKind uint32

const (
	LaneHostControl LaneKind = iota + 1
	LaneWorkerControl
	LaneHostData
	LaneWorkerData
)

// RecordKind is the slot envelope kind.
type RecordKind uint32

const (
	RecordPublish RecordKind = iota + 1
	RecordRevoke
	RecordAcknowledge
	RecordGap
	RecordStop
	RecordCommand
	RecordRaw
	RecordCompletion
)

// Status is the slot envelope status.
type Status uint32

const (
	StatusPending Status = iota
	StatusAccepted
	StatusRefused
	StatusRetired
)

// Direction is the slot envelope direction.
type Direction uint8

const (
	DirectionControl Direction = iota
	DirectionReceive
	DirectionEcho
)

const maxPhase uint8 = 5

// OwnerToken is a nonzero, nonreused command-owner reservation.
type OwnerToken uint64

// BuildIdentity is an opaque digest binding both processes to one build.
type BuildIdentity [buildBytes]byte

// Binding is the identity a mapping must carry exactly.
type Binding struct {
	Build  BuildIdentity
	Worker WorkerID
	Stream StreamGeneration
}

func (binding Binding) validate() error {
	if binding.Build == (BuildIdentity{}) {
		return ErrBuild
	}
	if binding.Worker == 0 || binding.Stream == 0 {
		return ErrIPCIdentity
	}
	return nil
}

// IPCLimits holds the finite fixture parameters.
type IPCLimits struct {
	MaxMappingBytes     uint32
	MaxSlotBytes        uint32
	MaxCapacity         uint32
	MaxPayloadBytes     uint32
	MaxArenaBytes       uint64
	MaxPlans            uint32
	MaxOutstanding      uint32
	ControlServiceTicks Tick
}

func (limits IPCLimits) validate() error {
	if limits.MaxMappingBytes == 0 || limits.MaxCapacity == 0 ||
		limits.MaxPayloadBytes == 0 || limits.MaxArenaBytes == 0 ||
		limits.MaxPlans == 0 || limits.MaxOutstanding == 0 ||
		limits.ControlServiceTicks == 0 {
		return ErrIPCInvalidLimits
	}
	if limits.MaxSlotBytes < envelopeBytes ||
		limits.MaxPayloadBytes > limits.MaxSlotBytes-envelopeBytes ||
		limits.MaxCapacity > capacityCeiling ||
		limits.MaxPlans > tableCeiling || limits.MaxOutstanding > tableCeiling {
		return ErrIPCInvalidLimits
	}
	return nil
}

// Expected is what one side demands of a mapping before any access. The
// floors carry nonreused plan and token identities across a resynchronization.
type Expected struct {
	Binding      Binding
	MappingBytes uint32
	Limits       IPCLimits
	PlanFloor    PlanGeneration
	TokenFloor   OwnerToken
	// ClockDomain is the one clock domain both endpoints share. It must be
	// nonzero: each endpoint's guard is seeded with it, so a reading from any other
	// domain latches the clock fence and is never compared with a deadline or
	// capture time. InitMapping returns the default domain.
	ClockDomain uint64
}

// DefaultClockDomain is the clock domain InitMapping puts in its expectation.
const DefaultClockDomain = uint64(1)

// seedClock builds a guard bound to the expected clock domain.
func seedClock(domain uint64) clockGuard {
	if domain == 0 {
		return clockGuard{}
	}
	return clockGuard{started: true, domain: domain}
}

// LaneConfig sizes one lane.
type LaneConfig struct {
	SlotBytes uint32
	Capacity  uint32
}

// IPCConfig sizes one mapping. Lanes are indexed by kind minus one.
type IPCConfig struct {
	Lanes      [laneCount]LaneConfig
	ArenaBytes uint64
}

type laneLayout struct {
	kind           LaneKind
	slotBytes      uint32
	capacity       uint32
	slotOffset     uint64
	producerOffset uint64
	consumerOffset uint64
}

// Layout is the validated, process-local copy of a mapping's fixed layout.
// Nothing reads the header or descriptors again after validation.
type Layout struct {
	TotalBytes       uint32
	DescriptorOffset uint64
	ArenaOffset      uint64
	ArenaBytes       uint64
	lanes            [laneCount]laneLayout
}

func addChecked(first uint64, second uint64) (uint64, bool) {
	sum := first + second
	return sum, sum >= first
}

func mulChecked(first uint64, second uint64) (uint64, bool) {
	if first != 0 && second > math.MaxUint64/first {
		return 0, false
	}
	return first * second, true
}

func aligned(offset uint64) bool {
	return offset%alignment == 0
}

func baseAligned(mem []byte) bool {
	return len(mem) > 0 && uintptr(unsafe.Pointer(&mem[0]))%uintptr(alignment) == 0
}

// checkLane validates one lane's sizes. Each control lane needs a slot beyond
// the reserved one, so ordinary control can never occupy the gap or stop slot.
func checkLane(kind LaneKind, slotBytes uint32, capacity uint32, limits IPCLimits) (uint64, error) {
	if slotBytes < envelopeBytes || !aligned(uint64(slotBytes)) ||
		slotBytes > limits.MaxSlotBytes || capacity == 0 ||
		capacity > limits.MaxCapacity {
		return 0, ErrLane
	}
	if (kind == LaneHostControl || kind == LaneWorkerControl) && uint64(capacity) <= reservedSlots {
		return 0, ErrLane
	}
	region, ok := mulChecked(uint64(slotBytes), uint64(capacity))
	if !ok {
		return 0, ErrBounds
	}
	return region, nil
}

// InitMapping writes a fresh mapping into caller memory and validates it. A
// refusal leaves the memory untouched.
func InitMapping(mem []byte, binding Binding, config IPCConfig, limits IPCLimits) (Expected, Layout, error) {
	if err := limits.validate(); err != nil {
		return Expected{}, Layout{}, err
	}
	if err := binding.validate(); err != nil {
		return Expected{}, Layout{}, err
	}
	if !baseAligned(mem) {
		return Expected{}, Layout{}, ErrAlignment
	}
	if config.ArenaBytes <= buildBytes || config.ArenaBytes > limits.MaxArenaBytes ||
		!aligned(config.ArenaBytes) {
		return Expected{}, Layout{}, ErrIPCInvalidLimits
	}
	cursor := headerBytes + uint64(laneCount)*descriptorBytes
	var regions [laneCount]uint64
	for index, lane := range config.Lanes {
		size, err := checkLane(LaneKind(index+1), lane.SlotBytes, lane.Capacity, limits)
		if err != nil {
			return Expected{}, Layout{}, err
		}
		regions[index] = cursor
		var ok bool
		if cursor, ok = addChecked(cursor, size); !ok {
			return Expected{}, Layout{}, ErrBounds
		}
	}
	arenaOffset := cursor
	total, ok := addChecked(arenaOffset, config.ArenaBytes)
	if !ok || total > uint64(limits.MaxMappingBytes) || total != uint64(len(mem)) {
		return Expected{}, Layout{}, ErrMappingSize
	}
	clear(mem)
	copy(mem, headerMagic)
	put32 := binary.LittleEndian.PutUint32
	put64 := binary.LittleEndian.PutUint64
	put32(mem[offsetVersion:], ABIVersion)
	put32(mem[offsetHeaderBytes:], uint32(headerBytes))
	put32(mem[offsetTotalBytes:], uint32(total))
	put64(mem[offsetWorker:], uint64(binding.Worker))
	put64(mem[offsetStream:], uint64(binding.Stream))
	put32(mem[offsetLaneCount:], laneCount)
	put32(mem[offsetDescBytes:], uint32(descriptorBytes))
	put64(mem[offsetDescTable:], headerBytes)
	put64(mem[offsetArena:], arenaOffset)
	put64(mem[offsetArenaBytes:], config.ArenaBytes)
	for index, lane := range config.Lanes {
		desc := mem[headerBytes+uint64(index)*descriptorBytes:]
		put32(desc, uint32(index+1))
		put32(desc[descOffsetSlot:], lane.SlotBytes)
		put32(desc[descOffsetCapacity:], lane.Capacity)
		put64(desc[descOffsetRegion:], regions[index])
	}
	copy(mem[arenaOffset:], binding.Build[:])
	expected := Expected{Binding: binding, MappingBytes: uint32(total), Limits: limits,
		ClockDomain: DefaultClockDomain}
	layout, err := ValidateMapping(mem, expected)
	return expected, layout, err
}

// ValidateMapping checks every field, sum and product before any record is
// reachable. It only reads, so a refused replacement cannot disturb the state of
// an earlier valid mapping. It admits only a fresh mapping: both indices of each
// lane must still be zero.
func ValidateMapping(mem []byte, expected Expected) (Layout, error) {
	limits := expected.Limits
	if err := limits.validate(); err != nil {
		return Layout{}, err
	}
	if err := expected.Binding.validate(); err != nil {
		return Layout{}, err
	}
	if !baseAligned(mem) {
		return Layout{}, ErrAlignment
	}
	length := uint64(len(mem))
	if length < headerBytes || length > uint64(limits.MaxMappingBytes) ||
		length != uint64(expected.MappingBytes) {
		return Layout{}, ErrMappingSize
	}
	get32 := binary.LittleEndian.Uint32
	get64 := binary.LittleEndian.Uint64
	if string(mem[:len(headerMagic)]) != headerMagic ||
		get32(mem[offsetVersion:]) != ABIVersion ||
		uint64(get32(mem[offsetHeaderBytes:])) != headerBytes ||
		get32(mem[offsetLaneCount:]) != laneCount ||
		uint64(get32(mem[offsetDescBytes:])) != descriptorBytes {
		return Layout{}, ErrABI
	}
	if uint64(get32(mem[offsetTotalBytes:])) != length {
		return Layout{}, ErrMappingSize
	}
	if get64(mem[offsetWorker:]) != uint64(expected.Binding.Worker) ||
		get64(mem[offsetStream:]) != uint64(expected.Binding.Stream) {
		return Layout{}, ErrIPCIdentity
	}
	layout := Layout{
		TotalBytes:       uint32(length),
		DescriptorOffset: get64(mem[offsetDescTable:]),
		ArenaOffset:      get64(mem[offsetArena:]),
		ArenaBytes:       get64(mem[offsetArenaBytes:]),
	}
	if !aligned(layout.DescriptorOffset) || !aligned(layout.ArenaOffset) {
		return Layout{}, ErrAlignment
	}
	tableBytes := uint64(laneCount) * descriptorBytes
	tableEnd, ok := addChecked(layout.DescriptorOffset, tableBytes)
	if !ok || tableEnd > length {
		return Layout{}, ErrBounds
	}
	if layout.ArenaBytes <= buildBytes || layout.ArenaBytes > limits.MaxArenaBytes {
		return Layout{}, ErrBounds
	}
	arenaEnd, ok := addChecked(layout.ArenaOffset, layout.ArenaBytes)
	if !ok || arenaEnd > length {
		return Layout{}, ErrBounds
	}
	spans := [][2]uint64{
		{0, headerBytes},
		{layout.DescriptorOffset, tableEnd},
		{layout.ArenaOffset, arenaEnd},
	}
	var seen [laneCount]bool
	for index := uint64(0); index < uint64(laneCount); index++ {
		descOffset := layout.DescriptorOffset + index*descriptorBytes
		desc := mem[descOffset : descOffset+descriptorBytes]
		kind := LaneKind(get32(desc))
		if kind < LaneHostControl || kind > LaneWorkerData || seen[kind-1] {
			return Layout{}, ErrLane
		}
		seen[kind-1] = true
		if get32(desc[descOffsetReserved:]) != 0 {
			return Layout{}, ErrReserved
		}
		for _, octet := range desc[descOffsetTail:] {
			if octet != 0 {
				return Layout{}, ErrReserved
			}
		}
		slotBytes := get32(desc[descOffsetSlot:])
		capacity := get32(desc[descOffsetCapacity:])
		regionBytes, err := checkLane(kind, slotBytes, capacity, limits)
		if err != nil {
			return Layout{}, err
		}
		slotOffset := get64(desc[descOffsetRegion:])
		if !aligned(slotOffset) {
			return Layout{}, ErrAlignment
		}
		regionEnd, ok := addChecked(slotOffset, regionBytes)
		if !ok || regionEnd > length {
			return Layout{}, ErrBounds
		}
		if get64(desc[descOffsetProducer:]) != 0 || get64(desc[descOffsetConsumer:]) != 0 {
			return Layout{}, ErrIndex
		}
		spans = append(spans, [2]uint64{slotOffset, regionEnd})
		layout.lanes[kind-1] = laneLayout{
			kind:           kind,
			slotBytes:      slotBytes,
			capacity:       capacity,
			slotOffset:     slotOffset,
			producerOffset: descOffset + descOffsetProducer,
			consumerOffset: descOffset + descOffsetConsumer,
		}
	}
	for first := range spans {
		for second := first + 1; second < len(spans); second++ {
			if spans[first][0] < spans[second][1] && spans[second][0] < spans[first][1] {
				return Layout{}, ErrOverlap
			}
		}
	}
	var build BuildIdentity
	copy(build[:], mem[layout.ArenaOffset:])
	if build != expected.Binding.Build {
		return Layout{}, ErrBuild
	}
	return layout, nil
}

// loadAcquire and storeRelease are the only accesses to shared indices.
// The validator proved each offset aligned and in bounds against an aligned base.
func loadAcquire(mem []byte, offset uint64) uint64 {
	native := atomic.LoadUint64((*uint64)(unsafe.Pointer(&mem[offset])))
	var bytes [8]byte
	binary.NativeEndian.PutUint64(bytes[:], native)
	return binary.LittleEndian.Uint64(bytes[:])
}

func storeRelease(mem []byte, offset uint64, value uint64) {
	var bytes [8]byte
	binary.LittleEndian.PutUint64(bytes[:], value)
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&mem[offset])), binary.NativeEndian.Uint64(bytes[:]))
}

// Record is the detached form of one slot. Its payload never aliases shared
// memory.
type Record struct {
	Kind            RecordKind
	Worker          WorkerID
	Stream          StreamGeneration
	Transaction     TransactionOrdinal
	Plan            PlanGeneration
	Subscriber      SubscriberID
	Epoch           RegistrationEpoch
	OwnerToken      OwnerToken
	OwnerExpiry     Tick
	CaptureSequence uint64
	CaptureTime     Tick
	PlanExpiry      Tick
	RequestDeadline Tick
	ArenaOffset     uint64
	PlanBytes       uint32
	Status          Status
	Target          byte
	RawOctet        byte
	Direction       Direction
	Phase           uint8
	Flags           uint32
	Payload         []byte
}

// Delivery separates when a record was observed from when it was dequeued.
// The dequeue time never replaces the capture time in the record.
type Delivery struct {
	Record      Record
	DequeueTime Tick
}

// PlanRef is the exact identity of one published plan.
type PlanRef struct {
	Key        RegistrationKey
	Worker     WorkerID
	Stream     StreamGeneration
	Plan       PlanGeneration
	PlanExpiry Tick
}

func (ref PlanRef) matches(other PlanRef) bool {
	if !ref.Key.matches(other.Key) {
		return false
	}
	if ref.Worker != other.Worker {
		return false
	}
	if ref.Stream != other.Stream {
		return false
	}
	if ref.Plan != other.Plan {
		return false
	}
	return ref.PlanExpiry == other.PlanExpiry
}

// CommandIdentity is the exact identity of one reserved command.
type CommandIdentity struct {
	Ref             PlanRef
	Transaction     TransactionOrdinal
	OwnerToken      OwnerToken
	OwnerExpiry     Tick
	RequestDeadline Tick
}

func (identity CommandIdentity) matches(other CommandIdentity) bool {
	if !identity.Ref.matches(other.Ref) {
		return false
	}
	if identity.Transaction != other.Transaction {
		return false
	}
	if identity.OwnerToken != other.OwnerToken {
		return false
	}
	if identity.OwnerExpiry != other.OwnerExpiry {
		return false
	}
	return identity.RequestDeadline == other.RequestDeadline
}

// live uses exclusive deadlines: expiry equality is expired.
func (identity CommandIdentity) live(now Tick) bool {
	return now < identity.OwnerExpiry && now < identity.Ref.PlanExpiry &&
		now < identity.RequestDeadline
}

func (record *Record) applyRef(ref PlanRef) {
	record.Subscriber = ref.Key.Subscriber
	record.Target = ref.Key.Address
	record.Epoch = ref.Key.Epoch
	record.Worker = ref.Worker
	record.Stream = ref.Stream
	record.Plan = ref.Plan
	record.PlanExpiry = ref.PlanExpiry
}

func (record *Record) applyIdentity(identity CommandIdentity) {
	record.applyRef(identity.Ref)
	record.Transaction = identity.Transaction
	record.OwnerToken = identity.OwnerToken
	record.OwnerExpiry = identity.OwnerExpiry
	record.RequestDeadline = identity.RequestDeadline
}

func (record *Record) ref() PlanRef {
	return PlanRef{
		Key: RegistrationKey{
			Subscriber: record.Subscriber,
			Address:    record.Target,
			Epoch:      record.Epoch,
		},
		Worker:     record.Worker,
		Stream:     record.Stream,
		Plan:       record.Plan,
		PlanExpiry: record.PlanExpiry,
	}
}

func (record *Record) identity() CommandIdentity {
	return CommandIdentity{
		Ref:             record.ref(),
		Transaction:     record.Transaction,
		OwnerToken:      record.OwnerToken,
		OwnerExpiry:     record.OwnerExpiry,
		RequestDeadline: record.RequestDeadline,
	}
}

func kindOnLane(kind RecordKind, lane LaneKind) bool {
	switch lane {
	case LaneHostControl:
		return kind == RecordPublish || kind == RecordRevoke || kind == RecordStop
	case LaneWorkerControl:
		return kind == RecordAcknowledge || kind == RecordGap
	case LaneHostData:
		return kind == RecordCommand
	case LaneWorkerData:
		return kind == RecordRaw || kind == RecordCompletion
	}
	return false
}

// shape refuses a record whose fields do not match its kind. Stream-wide and
// unselected records must keep unused identity fields at zero.
func (record *Record) shape(lane LaneKind, layout *Layout) bool {
	if !kindOnLane(record.Kind, lane) || record.Flags != 0 ||
		record.Worker == 0 || record.Stream == 0 ||
		record.Status > StatusRetired || record.Direction > DirectionEcho ||
		record.Phase > maxPhase {
		return false
	}
	registrationFull := record.Subscriber != 0 && isTargetAddress(record.Target) && record.Epoch != 0
	registrationZero := record.Subscriber == 0 && record.Target == 0 && record.Epoch == 0
	planFull := record.Plan != 0 && record.PlanExpiry != 0
	planZero := record.Plan == 0 && record.PlanExpiry == 0
	ownerFull := record.Transaction != 0 && record.OwnerToken != 0 &&
		record.OwnerExpiry != 0 && record.RequestDeadline != 0
	tokenZero := record.OwnerToken == 0 && record.OwnerExpiry == 0 && record.RequestDeadline == 0
	ownerZero := record.Transaction == 0 && tokenZero
	captureFull := record.CaptureSequence != 0 && record.CaptureTime != 0
	captureZero := record.CaptureSequence == 0 && record.CaptureTime == 0
	arenaZero := record.ArenaOffset == 0 && record.PlanBytes == 0
	controlPlain := record.Direction == DirectionControl && record.RawOctet == 0
	statusZero := record.Status == StatusPending
	noPhase := record.Phase == 0

	switch record.Kind {
	case RecordPublish:
		return registrationFull && planFull && ownerZero && captureZero && controlPlain &&
			statusZero && noPhase && record.arenaInBounds(layout)
	case RecordRevoke:
		return registrationFull && planFull && ownerZero && captureZero && arenaZero &&
			controlPlain && statusZero && noPhase
	case RecordAcknowledge:
		return registrationFull && planFull && ownerZero && captureFull && arenaZero &&
			controlPlain && noPhase && record.Status == StatusRetired
	case RecordGap:
		return registrationZero && planZero && ownerZero && captureFull && arenaZero &&
			controlPlain && statusZero && noPhase
	case RecordStop:
		return registrationZero && planZero && ownerZero && captureZero && arenaZero &&
			controlPlain && statusZero && noPhase
	case RecordCommand:
		return registrationFull && planFull && ownerFull && captureZero && arenaZero &&
			controlPlain && statusZero && noPhase
	case RecordRaw:
		selected := registrationFull && planFull && ownerFull
		unselected := registrationZero && planZero && tokenZero && record.Transaction != 0
		return (selected || unselected) && captureFull && arenaZero && statusZero &&
			record.Direction != DirectionControl && len(record.Payload) == 0
	case RecordCompletion:
		return registrationFull && planFull && ownerFull && captureFull && arenaZero &&
			controlPlain && noPhase && len(record.Payload) == 0 && record.Status != StatusPending
	}
	return false
}

// arenaInBounds keeps a plan reference inside the arena, past its build identity.
func (record *Record) arenaInBounds(layout *Layout) bool {
	end, ok := addChecked(record.ArenaOffset, uint64(record.PlanBytes))
	return ok && record.PlanBytes != 0 &&
		record.ArenaOffset >= layout.ArenaOffset+buildBytes &&
		end <= layout.ArenaOffset+layout.ArenaBytes
}

func (record *Record) encode(slot []byte) {
	put32 := binary.LittleEndian.PutUint32
	put64 := binary.LittleEndian.PutUint64
	clear(slot)
	put32(slot[0:], uint32(record.Kind))
	put32(slot[4:], uint32(len(record.Payload)))
	put64(slot[8:], uint64(record.Worker))
	put64(slot[16:], uint64(record.Stream))
	put64(slot[24:], uint64(record.Transaction))
	put64(slot[32:], uint64(record.Plan))
	put64(slot[40:], uint64(record.Subscriber))
	put64(slot[48:], uint64(record.Epoch))
	put64(slot[56:], uint64(record.OwnerToken))
	put64(slot[64:], uint64(record.OwnerExpiry))
	put64(slot[72:], record.CaptureSequence)
	put64(slot[80:], uint64(record.CaptureTime))
	put64(slot[88:], uint64(record.PlanExpiry))
	put64(slot[96:], uint64(record.RequestDeadline))
	put64(slot[104:], record.ArenaOffset)
	put32(slot[112:], record.PlanBytes)
	put32(slot[116:], uint32(record.Status))
	slot[120] = record.Target
	slot[121] = record.RawOctet
	slot[122] = byte(record.Direction)
	slot[123] = record.Phase
	put32(slot[124:], record.Flags)
	copy(slot[envelopeBytes:], record.Payload)
}

func decodeEnvelope(slot []byte) Record {
	get32 := binary.LittleEndian.Uint32
	get64 := binary.LittleEndian.Uint64
	return Record{
		Kind:            RecordKind(get32(slot[0:])),
		Worker:          WorkerID(get64(slot[8:])),
		Stream:          StreamGeneration(get64(slot[16:])),
		Transaction:     TransactionOrdinal(get64(slot[24:])),
		Plan:            PlanGeneration(get64(slot[32:])),
		Subscriber:      SubscriberID(get64(slot[40:])),
		Epoch:           RegistrationEpoch(get64(slot[48:])),
		OwnerToken:      OwnerToken(get64(slot[56:])),
		OwnerExpiry:     Tick(get64(slot[64:])),
		CaptureSequence: get64(slot[72:]),
		CaptureTime:     Tick(get64(slot[80:])),
		PlanExpiry:      Tick(get64(slot[88:])),
		RequestDeadline: Tick(get64(slot[96:])),
		ArenaOffset:     get64(slot[104:]),
		PlanBytes:       get32(slot[112:]),
		Status:          Status(get32(slot[116:])),
		Target:          slot[120],
		RawOctet:        slot[121],
		Direction:       Direction(slot[122]),
		Phase:           slot[123],
		Flags:           get32(slot[124:]),
	}
}

// LaneStats are process-local counters, because the documented layout has no
// shared field for them. HighWater is the largest occupancy this side observed.
type LaneStats struct {
	HighWater uint64
	Enqueued  uint64
	Dequeued  uint64
	Refused   uint64
	Dropped   uint64
}

// lane is one side's view of an SPSC lane. A side writes exactly one of the
// two indices and treats the other as peer-controlled input to revalidate.
type lane struct {
	mem              []byte
	layout           laneLayout
	arenaLayout      *Layout
	payloadCap       uint32
	isProducer       bool
	localProducer    uint64
	localConsumer    uint64
	stats            LaneStats
	saturated        bool
	progressConsumer uint64
	progressTick     Tick
}

func (lane *lane) bump(counter *uint64) {
	if *counter == math.MaxUint64 {
		lane.saturated = true
		return
	}
	*counter++
}

func (lane *lane) indices() (uint64, uint64, error) {
	producer := loadAcquire(lane.mem, lane.layout.producerOffset)
	consumer := loadAcquire(lane.mem, lane.layout.consumerOffset)
	if consumer > producer || producer-consumer > uint64(lane.layout.capacity) {
		return 0, 0, ErrIndex
	}
	if lane.isProducer {
		if producer != lane.localProducer || consumer < lane.localConsumer {
			return 0, 0, ErrIndex
		}
		lane.localConsumer = consumer
		return producer, consumer, nil
	}
	if consumer != lane.localConsumer || producer < lane.localProducer {
		return 0, 0, ErrIndex
	}
	lane.localProducer = producer
	return producer, consumer, nil
}

func (lane *lane) slot(index uint64) []byte {
	slotBytes := uint64(lane.layout.slotBytes)
	start := lane.layout.slotOffset + (index%uint64(lane.layout.capacity))*slotBytes
	return lane.mem[start : start+slotBytes]
}

func (lane *lane) hasRoom(reserve uint64) (bool, error) {
	producer, consumer, err := lane.indices()
	if err != nil {
		return false, err
	}
	return producer-consumer+reserve < uint64(lane.layout.capacity), nil
}

// write fills a whole owned slot and only then release-publishes the index.
// It never overwrites an unread slot and never waits. reserve keeps slots free
// for a record that must not be lost.
func (lane *lane) write(record *Record, reserve uint64) error {
	if !lane.isProducer {
		return ErrOwnership
	}
	if uint64(len(record.Payload)) > uint64(lane.payloadCap) {
		return ErrIPCOversize
	}
	if !record.shape(lane.layout.kind, lane.arenaLayout) {
		return ErrRecord
	}
	hasRoom, err := lane.hasRoom(reserve)
	if err != nil {
		return err
	}
	if !hasRoom {
		lane.bump(&lane.stats.Refused)
		return ErrFull
	}
	producer := lane.localProducer
	if producer == math.MaxUint64 {
		return ErrCounter
	}
	record.encode(lane.slot(producer))
	storeRelease(lane.mem, lane.layout.producerOffset, producer+1)
	lane.localProducer = producer + 1
	if lane.layout.kind == LaneWorkerControl && producer == lane.localConsumer {
		// An empty lane has no deadline. Start the new backlog's budget at
		// this control record's trusted emission tick, not an idle service tick.
		lane.progressConsumer = lane.localConsumer
		lane.progressTick = record.CaptureTime
	}
	lane.bump(&lane.stats.Enqueued)
	if occupancy := producer + 1 - lane.localConsumer; occupancy > lane.stats.HighWater {
		lane.stats.HighWater = occupancy
	}
	return nil
}

// read acquires the producer index, validates a whole detached copy and only
// then releases the slot. A malformed record stays unconsumed.
func (lane *lane) read(dst []byte) (Record, bool, error) {
	record, ok, err := lane.peek(dst)
	if err != nil || !ok {
		return record, ok, err
	}
	lane.release()
	return record, true, nil
}

// release frees the slot of the record the last peek returned.
func (lane *lane) release() {
	producer, consumer, err := lane.indices()
	if err != nil || producer == consumer {
		return
	}
	storeRelease(lane.mem, lane.layout.consumerOffset, consumer+1)
	lane.localConsumer = consumer + 1
	lane.bump(&lane.stats.Dequeued)
	if occupancy := producer - consumer; occupancy > lane.stats.HighWater {
		lane.stats.HighWater = occupancy
	}
}

// peek validates a whole detached copy of the next record and leaves the slot owned.
func (lane *lane) peek(dst []byte) (Record, bool, error) {
	if lane.isProducer {
		return Record{}, false, ErrOwnership
	}
	producer, consumer, err := lane.indices()
	if err != nil {
		return Record{}, false, err
	}
	if producer == consumer {
		return Record{}, false, nil
	}
	slot := lane.slot(consumer)
	payloadBytes := binary.LittleEndian.Uint32(slot[4:])
	if payloadBytes > lane.layout.slotBytes-envelopeBytes {
		return Record{}, false, ErrTruncated
	}
	if payloadBytes != 0 && (lane.layout.kind == LaneHostControl || lane.layout.kind == LaneWorkerControl) {
		return Record{}, false, ErrRecord
	}
	if payloadBytes != 0 && RecordKind(binary.LittleEndian.Uint32(slot[0:])) == RecordCompletion {
		// A completion never carries a payload, so it is malformed framing before any
		// buffer size can matter.
		return Record{}, false, ErrRecord
	}
	if payloadBytes > lane.payloadCap {
		return Record{}, false, ErrIPCOversize
	}
	if uint64(payloadBytes) > uint64(cap(dst)) {
		return Record{}, false, ErrBuffer
	}
	payload := slot[envelopeBytes : envelopeBytes+payloadBytes]
	for _, octet := range slot[envelopeBytes+payloadBytes:] {
		if octet != 0 {
			return Record{}, false, ErrRecord
		}
	}
	record := decodeEnvelope(slot)
	record.Payload = append(dst[:0], payload...)
	if !record.shape(lane.layout.kind, lane.arenaLayout) {
		return Record{}, false, ErrRecord
	}
	return record, true, nil
}

// serviceExpired reports an unread control backlog that has made no progress
// for the whole budget. Reaching the budget exactly counts as expired.
func (lane *lane) serviceExpired(budget Tick, now Tick) (bool, error) {
	producer, consumer, err := lane.indices()
	if err != nil {
		return false, err
	}
	if producer == consumer || consumer != lane.progressConsumer || lane.progressTick == 0 {
		lane.progressConsumer = consumer
		lane.progressTick = now
		return false, nil
	}
	return now-lane.progressTick >= budget, nil
}

func attachLanes(mem []byte, layout *Layout, limits IPCLimits, worker bool) [laneCount]lane {
	var lanes [laneCount]lane
	for index := range lanes {
		view := layout.lanes[index]
		payloadCap := view.slotBytes - envelopeBytes
		if payloadCap > limits.MaxPayloadBytes {
			payloadCap = limits.MaxPayloadBytes
		}
		workerProduces := view.kind == LaneWorkerControl || view.kind == LaneWorkerData
		lanes[index] = lane{
			mem:         mem,
			layout:      view,
			arenaLayout: layout,
			payloadCap:  payloadCap,
			isProducer:  workerProduces == worker,
		}
	}
	return lanes
}

// FenceReason states why an endpoint closed output decisions.
type FenceReason uint8

const (
	FenceNone FenceReason = iota
	FenceDataFull
	FenceControlSaturated
	FenceControlDeadline
	FenceWorkerGap
	FenceDiscontinuity
	FenceCorruption
	FenceCounter
	FenceClock
	FenceStop
	FenceWorkerTerminated
)

// FenceState is the first cause of a closed stream view. Sequence and capture
// time are the observation it refers to; a local cause without one keeps them zero.
type FenceState struct {
	Reason      FenceReason
	Sequence    uint64
	CaptureTime Tick
}

// Observation is one raw symbol. A zero selection is an unselected observation.
type Observation struct {
	Transaction TransactionOrdinal
	RawOctet    byte
	Direction   Direction
	Phase       uint8
	Selection   CommandIdentity
}

// valid checks the observation shape the raw record requires, without state.
func (observation Observation) valid() bool {
	if observation.Transaction == 0 || observation.Direction == DirectionControl ||
		observation.Direction > DirectionEcho || observation.Phase > maxPhase {
		return false
	}
	selection := observation.Selection
	if selection == (CommandIdentity{}) {
		return true
	}
	ref := selection.Ref
	return selection.Transaction == observation.Transaction &&
		ref.Key.Subscriber != 0 && isTargetAddress(ref.Key.Address) &&
		ref.Key.Epoch != 0 && ref.Plan != 0 && ref.PlanExpiry != 0 &&
		selection.OwnerToken != 0 && selection.OwnerExpiry != 0 &&
		selection.RequestDeadline != 0
}

type adoptedPlan struct {
	ref     PlanRef
	revoked bool
}

// Worker is the worker side: sole producer of the worker lanes and sole
// consumer of the host lanes. Once a stream is closed, nothing reopens it; recovery
// is a new validated mapping with an advanced generation.
type Worker struct {
	expected     Expected
	lanes        [laneCount]lane
	clock        clockGuard
	nextSequence uint64
	lastCapture  Tick
	fence        FenceState
	gapLatched   bool
	gapWritten   bool
	gapIndex     uint64
	gapRecord    Record
	stopObserved bool
	adopted      []adoptedPlan
	refusedRefs  []PlanRef
	retireRetry  []PlanRef
	scratch      []byte
	planFloor    PlanGeneration
	tokenFloor   OwnerToken
	active       CommandIdentity
	hasActive    bool
	lost         uint64
	refused      uint64
	detached     bool
}

// AttachWorker validates a fresh mapping before any lane becomes reachable.
func AttachWorker(mem []byte, expected Expected) (*Worker, error) {
	if expected.ClockDomain == 0 {
		return nil, ErrIPCClock
	}
	layout, err := ValidateMapping(mem, expected)
	if err != nil {
		return nil, err
	}
	return &Worker{
		expected:     expected,
		clock:        seedClock(expected.ClockDomain),
		lanes:        attachLanes(mem, &layout, expected.Limits, true),
		nextSequence: 1,
		adopted:      make([]adoptedPlan, 0, expected.Limits.MaxPlans),
		refusedRefs:  make([]PlanRef, 0, expected.Limits.MaxPlans),
		retireRetry:  make([]PlanRef, 0, expected.Limits.MaxPlans),
		scratch:      make([]byte, 0, expected.Limits.MaxPayloadBytes),
		planFloor:    expected.PlanFloor,
		tokenFloor:   expected.TokenFloor,
	}, nil
}

func (worker *Worker) lane(kind LaneKind) *lane {
	return &worker.lanes[kind-1]
}

// OutputOpen reports whether new output decisions may be taken.
func (worker *Worker) OutputOpen() bool {
	return !worker.detached && worker.fence.Reason == FenceNone
}

// Fence returns the first fence cause.
func (worker *Worker) Fence() FenceState { return worker.fence }

// Lost counts observations and completions that could not be queued.
func (worker *Worker) Lost() uint64 { return worker.lost }

// PublishRefused counts publications dropped without adoption.
func (worker *Worker) PublishRefused() uint64 { return worker.refused }

// LaneStats returns this side's counters for one lane.
func (worker *Worker) LaneStats(kind LaneKind) LaneStats {
	return worker.lane(kind).stats
}

func (worker *Worker) closeWorker(reason FenceReason, sequence uint64, tick Tick) {
	if worker.fence.Reason == FenceNone {
		worker.fence = FenceState{Reason: reason, Sequence: sequence, CaptureTime: tick}
	}
	worker.hasActive = false
}

// raiseGap closes output decisions before it touches any lane, then latches the one
// gap of this stream. The latch survives a failed write, so a gap is never lost.
func (worker *Worker) raiseGap(reason FenceReason, sequence uint64, tick Tick) {
	worker.closeWorker(reason, sequence, tick)
	if !worker.gapLatched {
		worker.gapLatched = true
		worker.gapRecord = Record{
			Kind:            RecordGap,
			Worker:          worker.expected.Binding.Worker,
			Stream:          worker.expected.Binding.Stream,
			CaptureSequence: sequence,
			CaptureTime:     tick,
		}
	}
	worker.flushGap()
}

func (worker *Worker) flushGap() {
	if !worker.gapLatched || worker.gapWritten {
		return
	}
	control := worker.lane(LaneWorkerControl)
	index := control.localProducer
	if control.write(&worker.gapRecord, 0) == nil {
		worker.gapWritten = true
		worker.gapIndex = index
	}
}

// gapSequence takes an ordinal for a gap with no lost observation of its own.
// The final ordinal stays available, because only a gap may use it.
func (worker *Worker) gapSequence() uint64 {
	sequence := worker.nextSequence
	if sequence != math.MaxUint64 {
		worker.nextSequence = sequence + 1
	}
	return sequence
}

func (worker *Worker) trustedTick() Tick {
	if worker.clock.last != 0 {
		return worker.clock.last
	}
	return 1
}

// raiseLocalGap handles a cause that lost no observation of its own.
func (worker *Worker) raiseLocalGap(reason FenceReason) {
	if worker.gapLatched {
		worker.flushGap()
		return
	}
	tick := worker.trustedTick()
	if worker.lastCapture > tick {
		tick = worker.lastCapture
	}
	worker.raiseGap(reason, worker.gapSequence(), tick)
}

// allocateSequence refuses to wrap and keeps the final ordinal for the gap.
func (worker *Worker) allocateSequence(tick Tick) (uint64, error) {
	if worker.nextSequence == math.MaxUint64 {
		worker.raiseGap(FenceCounter, math.MaxUint64, tick)
		return 0, ErrCounter
	}
	sequence := worker.nextSequence
	worker.nextSequence = sequence + 1
	return sequence, nil
}

// tick observes the clock and retries a latched gap. Tick zero is refused
// because zero means an unused time field in the envelope.
func (worker *Worker) tick(now ClockReading) error {
	if worker.detached {
		return ErrDetached
	}
	if err := worker.clock.observe(now); err != nil || now.Tick == 0 {
		worker.raiseLocalGap(FenceClock)
		return ErrIPCClock
	}
	worker.flushGap()
	return nil
}

// service runs before every output decision. It drains the bounded control
// lane first, so a revocation or stop never waits behind any data backlog.
func (worker *Worker) service(now ClockReading) error {
	if err := worker.tick(now); err != nil {
		if err == ErrIPCClock {
			// The latched clock fence still lets a stop through, so the host can
			// acknowledge the gap; nothing here makes an output decision.
			// Records emitted here carry the last trusted tick, never the regressed one.
			trusted := worker.trustedTick()
			if worker.lastCapture > trusted {
				trusted = worker.lastCapture
			}
			worker.drainControl(trusted, true)
			worker.retryRetired(trusted)
		}
		return err
	}
	worker.drainControl(now.Tick, false)
	for index := range worker.lanes {
		if worker.lanes[index].saturated {
			worker.raiseLocalGap(FenceCounter)
		}
	}
	expired, err := worker.lane(LaneWorkerControl).serviceExpired(worker.expected.Limits.ControlServiceTicks, now.Tick)
	if err != nil {
		worker.raiseLocalGap(FenceCorruption)
	} else if expired {
		worker.raiseLocalGap(FenceControlDeadline)
	}
	worker.retryRetired(now.Tick)
	return nil
}

// drainControl reads the bounded host control lane. A fenced drain consumes
// publications without adopting them.
func (worker *Worker) drainControl(now Tick, fenced bool) {
	control := worker.lane(LaneHostControl)
	for budget := control.layout.capacity; budget > 0; budget-- {
		record, ok, err := control.peek(worker.scratch)
		if err != nil {
			worker.raiseLocalGap(FenceCorruption)
			return
		}
		if !ok {
			break
		}
		// The slot stays owned until the record is fully handled, so an empty lane
		// proves no handling, such as a retirement post, is still pending.
		if !worker.handleControl(&record, now, fenced) {
			return
		}
		control.release()
	}
}

// retryRetired posts the retirement of refused publications whose first
// acknowledgement found no room. A reference stays until its post succeeds.
func (worker *Worker) retryRetired(now Tick) {
	for index := 0; index < len(worker.retireRetry); {
		ref := worker.retireRetry[index]
		hasRoom, err := worker.lane(LaneWorkerControl).hasRoom(reservedSlots)
		if err != nil || !hasRoom || worker.postRetired(ref, now) != nil {
			return
		}
		worker.retireRetry = append(worker.retireRetry[:index], worker.retireRetry[index+1:]...)
		worker.dropRefused(ref)
	}
}

func (worker *Worker) dropRefused(ref PlanRef) {
	for index := range worker.refusedRefs {
		if worker.refusedRefs[index].matches(ref) {
			worker.refusedRefs = append(worker.refusedRefs[:index], worker.refusedRefs[index+1:]...)
			return
		}
	}
}

func (worker *Worker) identityMatches(record *Record) bool {
	return record.Worker == worker.expected.Binding.Worker &&
		record.Stream == worker.expected.Binding.Stream
}

// handleControl reports false when the record must stay queued and be handled again.
func (worker *Worker) handleControl(record *Record, now Tick, fenced bool) bool {
	control := worker.lane(LaneHostControl)
	if !worker.identityMatches(record) {
		control.bump(&control.stats.Dropped)
		return true
	}
	switch record.Kind {
	case RecordStop:
		worker.stopObserved = true
		worker.closeWorker(FenceStop, 0, 0)
	case RecordRevoke:
		ref := record.ref()
		for index := range worker.adopted {
			if worker.adopted[index].ref.matches(ref) {
				worker.adopted[index].revoked = true
				if worker.hasActive && worker.active.Ref.matches(ref) {
					worker.hasActive = false
				}
				return true
			}
		}
		// A refused publication was never adopted, so nothing here refers to it and
		// its exact retirement is acknowledged at once.
		for index := range worker.refusedRefs {
			if worker.refusedRefs[index].matches(ref) {
				// The revocation stays queued until the acknowledgement is written,
				// so the host cannot settle past an unposted retirement.
				if worker.postRetired(ref, now) != nil {
					return false
				}
				worker.dropRefused(ref)
				return true
			}
		}
		control.bump(&control.stats.Dropped)
	case RecordPublish:
		if fenced || worker.fence.Reason != FenceNone {
			// A fenced drain, or a publication met while any fence is raised, adopts nothing, but the host still lists the plan, so the
			// reference is kept as refused and its exact revocation retires it.
			worker.refused++
			if len(worker.refusedRefs) < cap(worker.refusedRefs) {
				worker.refusedRefs = append(worker.refusedRefs, record.ref())
			} else {
				control.bump(&control.stats.Dropped)
			}
			return true
		}
		if record.Plan <= worker.planFloor || now >= record.PlanExpiry ||
			len(worker.adopted) == cap(worker.adopted) {
			worker.refused++
			if len(worker.refusedRefs) < cap(worker.refusedRefs) {
				worker.refusedRefs = append(worker.refusedRefs, record.ref())
			} else {
				worker.raiseLocalGap(FenceControlSaturated)
			}
			return true
		}
		worker.planFloor = record.Plan
		worker.adopted = append(worker.adopted, adoptedPlan{ref: record.ref()})
	}
	return true
}

// PollControl services the control lane without touching any data lane.
func (worker *Worker) PollControl(now ClockReading) error {
	return worker.service(now)
}

// PendingRevocations appends plans the worker fenced but has not acknowledged.
func (worker *Worker) PendingRevocations(dst []PlanRef) []PlanRef {
	for index := range worker.adopted {
		if worker.adopted[index].revoked {
			dst = append(dst, worker.adopted[index].ref)
		}
	}
	return dst
}

// ObserveRaw records one symbol at its observation time and never waits. A full
// data lane drops the symbol and raises a gap on the separate control lane, using
// the lost symbol's own ordinal and capture time.
func (worker *Worker) ObserveRaw(observation Observation, now ClockReading) (uint64, error) {
	// A malformed observation is a caller error: it is refused before the clock, the
	// sequence or any capture state is touched, and it raises no gap.
	if !observation.valid() {
		return 0, ErrRecord
	}
	if err := worker.tick(now); err != nil {
		if err != ErrDetached {
			worker.countLost()
		}
		return 0, err
	}
	if !worker.OutputOpen() {
		worker.countLost()
		return 0, ErrOutputFenced
	}
	// A selection names the active command exactly; anything else would attribute a
	// symbol to an identity the worker never admitted.
	if observation.Selection != (CommandIdentity{}) &&
		(!worker.hasActive || !worker.active.matches(observation.Selection)) {
		return 0, ErrMismatch
	}
	record := Record{
		Kind:      RecordRaw,
		RawOctet:  observation.RawOctet,
		Direction: observation.Direction,
		Phase:     observation.Phase,
	}
	record.applyIdentity(observation.Selection)
	record.Worker = worker.expected.Binding.Worker
	record.Stream = worker.expected.Binding.Stream
	record.Transaction = observation.Transaction
	sequence, err := worker.allocateSequence(now.Tick)
	if err != nil {
		worker.countLost()
		return 0, err
	}
	record.CaptureSequence = sequence
	record.CaptureTime = now.Tick
	worker.lastCapture = now.Tick
	return sequence, worker.writeData(&record)
}

func (worker *Worker) countLost() {
	if worker.lost == math.MaxUint64 {
		worker.raiseLocalGap(FenceCounter)
		return
	}
	worker.lost++
}

func (worker *Worker) writeData(record *Record) error {
	err := worker.lane(LaneWorkerData).write(record, 0)
	switch err {
	case nil:
		return nil
	case ErrFull:
		worker.raiseGap(FenceDataFull, record.CaptureSequence, record.CaptureTime)
		worker.countLost()
		return ErrGap
	case ErrRecord:
		worker.raiseGap(FenceCorruption, record.CaptureSequence, record.CaptureTime)
		return ErrRecord
	default:
		reason := FenceCorruption
		if err == ErrCounter {
			reason = FenceCounter
		}
		worker.raiseGap(reason, record.CaptureSequence, record.CaptureTime)
		worker.countLost()
		return err
	}
}

// NextCommand drains control first, then admits at most one command. A dropped
// stale command consumes its slot and is never replayed.
func (worker *Worker) NextCommand(dst []byte, now ClockReading) (Delivery, bool, error) {
	if err := worker.service(now); err != nil {
		return Delivery{}, false, err
	}
	if !worker.OutputOpen() {
		return Delivery{}, false, ErrOutputFenced
	}
	if worker.hasActive {
		if worker.active.live(now.Tick) {
			return Delivery{}, false, nil
		}
		worker.hasActive = false
	}
	data := worker.lane(LaneHostData)
	record, ok, err := data.read(dst)
	if err == ErrBuffer {
		return Delivery{}, false, err
	}
	if err != nil {
		worker.raiseLocalGap(FenceCorruption)
		return Delivery{}, false, err
	}
	if !ok {
		return Delivery{}, false, nil
	}
	if !worker.identityMatches(&record) || record.OwnerToken <= worker.tokenFloor {
		data.bump(&data.stats.Dropped)
		return Delivery{}, false, ErrStale
	}
	worker.tokenFloor = record.OwnerToken
	identity := record.identity()
	if err := worker.checkPlan(identity.Ref); err != nil {
		data.bump(&data.stats.Dropped)
		return Delivery{}, false, err
	}
	if !identity.live(now.Tick) {
		data.bump(&data.stats.Dropped)
		return Delivery{}, false, ErrIPCExpired
	}
	worker.active = identity
	worker.hasActive = true
	return Delivery{Record: record, DequeueTime: now.Tick}, true, nil
}

func (worker *Worker) checkPlan(ref PlanRef) error {
	for index := range worker.adopted {
		if worker.adopted[index].ref.matches(ref) {
			if worker.adopted[index].revoked {
				return ErrIPCRevoked
			}
			return nil
		}
	}
	return ErrStale
}

// AdmitOutput rechecks control, every exact key and every deadline. It must run
// immediately before each output symbol, and an expired command stops being active.
func (worker *Worker) AdmitOutput(identity CommandIdentity, now ClockReading) error {
	if err := worker.service(now); err != nil {
		return err
	}
	if !worker.OutputOpen() {
		return ErrOutputFenced
	}
	if err := worker.checkPlan(identity.Ref); err != nil {
		return err
	}
	if !worker.hasActive || !worker.active.matches(identity) {
		return ErrMismatch
	}
	if !identity.live(now.Tick) {
		worker.hasActive = false
		return ErrIPCExpired
	}
	return nil
}

// Complete posts the result of the active command through the data lane.
func (worker *Worker) Complete(identity CommandIdentity, status Status, now ClockReading) error {
	if err := worker.tick(now); err != nil {
		return err
	}
	if status == StatusPending || status > StatusRetired {
		return ErrRecord
	}
	if !worker.hasActive || !worker.active.matches(identity) {
		return ErrMismatch
	}
	if !identity.live(now.Tick) {
		worker.hasActive = false
		return ErrIPCExpired
	}
	sequence, err := worker.allocateSequence(now.Tick)
	if err != nil {
		return err
	}
	record := Record{Kind: RecordCompletion, Status: status}
	record.applyIdentity(identity)
	record.CaptureSequence = sequence
	record.CaptureTime = now.Tick
	worker.lastCapture = now.Tick
	worker.hasActive = false
	return worker.writeData(&record)
}

// AcknowledgeRevocation states that no reader or deferred output still refers
// to the plan. The caller invokes it only after that holds. It may run on a fenced
// stream, and it never takes the slot reserved for the gap: without room it fences.
func (worker *Worker) AcknowledgeRevocation(ref PlanRef, now ClockReading) error {
	stamp := now.Tick
	if err := worker.service(now); err != nil {
		if err != ErrIPCClock {
			return err
		}
		// A latched clock fence refuses new work, never the retirement of a plan whose
		// exact revocation was consumed; it is stamped with the last trusted tick.
		stamp = worker.trustedTick()
		if worker.lastCapture > stamp {
			stamp = worker.lastCapture
		}
	}
	found := -1
	for index := range worker.adopted {
		if worker.adopted[index].revoked && worker.adopted[index].ref.matches(ref) {
			found = index
			break
		}
	}
	if found < 0 {
		return ErrStale
	}
	if err := worker.postRetired(ref, stamp); err != nil {
		return err
	}
	worker.adopted = append(worker.adopted[:found], worker.adopted[found+1:]...)
	return nil
}

// postRetired writes the exact retirement acknowledgement on the worker control
// lane without taking the slot reserved for the gap.
func (worker *Worker) postRetired(ref PlanRef, now Tick) error {
	control := worker.lane(LaneWorkerControl)
	hasRoom, err := control.hasRoom(reservedSlots)
	if err != nil {
		worker.raiseLocalGap(FenceCorruption)
		return err
	}
	if !hasRoom {
		worker.raiseLocalGap(FenceControlSaturated)
		return ErrControlFull
	}
	sequence, err := worker.allocateSequence(now)
	if err != nil {
		return err
	}
	record := Record{Kind: RecordAcknowledge, Status: StatusRetired}
	record.applyRef(ref)
	record.CaptureSequence = sequence
	record.CaptureTime = now
	if err := control.write(&record, reservedSlots); err != nil {
		worker.raiseLocalGap(FenceCorruption)
		return err
	}
	worker.lastCapture = now
	return nil
}

// fenceAcknowledged reports whether the exact stop was observed and any raised
// gap was consumed by the host. Resynchronization and teardown share it.
func (worker *Worker) fenceAcknowledged() error {
	if !worker.stopObserved {
		return ErrNotAcknowledged
	}
	// Host control queued behind the stop, such as a late revocation, must be read first.
	producer, read, err := worker.lane(LaneHostControl).indices()
	if err != nil || producer != read {
		return ErrNotAcknowledged
	}
	if worker.gapLatched {
		control := worker.lane(LaneWorkerControl)
		if !worker.gapWritten {
			return ErrNotAcknowledged
		}
		_, consumer, err := control.indices()
		if err != nil || consumer <= worker.gapIndex {
			return ErrNotAcknowledged
		}
	}
	return nil
}

// ResyncExpected names the successor mapping once the host has acknowledged the
// fence: the exact stop was observed and any raised gap was consumed. The host is
// authoritative for the identity floors, since it may have assigned identities the
// worker never drained; the successor carries the higher of each worker floor and
// the supplied host floor, so recovery cannot reuse an assigned identity.
func (worker *Worker) ResyncExpected(next StreamGeneration, hostPlanFloor PlanGeneration, hostTokenFloor OwnerToken) (Expected, error) {
	if worker.detached {
		return Expected{}, ErrDetached
	}
	if err := worker.fenceAcknowledged(); err != nil {
		return Expected{}, err
	}
	if next <= worker.expected.Binding.Stream {
		return Expected{}, ErrStale
	}
	successor := worker.expected
	successor.Binding.Stream = next
	successor.PlanFloor = worker.planFloor
	if hostPlanFloor > successor.PlanFloor {
		successor.PlanFloor = hostPlanFloor
	}
	successor.TokenFloor = worker.tokenFloor
	if hostTokenFloor > successor.TokenFloor {
		successor.TokenFloor = hostTokenFloor
	}
	return successor, nil
}

// Teardown releases the mapping after output decisions closed. It never drains a lane,
// so a saturated data lane cannot hold shutdown.
func (worker *Worker) Teardown() error {
	if worker.detached {
		return nil
	}
	if worker.OutputOpen() || worker.fenceAcknowledged() != nil {
		return ErrNotAcknowledged
	}
	// A revoked plan stays until its retirement acknowledgement was posted, so the
	// host quarantine can be released by the exact record.
	if len(worker.retireRetry) > 0 {
		return ErrNotAcknowledged
	}
	for _, held := range worker.adopted {
		if held.revoked {
			return ErrNotAcknowledged
		}
	}
	worker.detached = true
	worker.lanes = [laneCount]lane{}
	worker.adopted = nil
	worker.refusedRefs = nil
	worker.retireRetry = nil
	return nil
}

type hostPlan struct {
	ref      PlanRef
	revoking bool
}

// Host is the host side: the one serialized dispatcher that produces the host
// lanes and consumes the worker lanes. A publish or revoke enqueue is never treated
// as worker acknowledgement, and a fenced plan stays listed until its exact
// acknowledgement. Reclamation proof stays with the plan pool.
type Host struct {
	expected        Expected
	lanes           [laneCount]lane
	clock           clockGuard
	fence           FenceState
	stopSent        bool
	stopIndex       uint64
	terminated      bool
	plans           []hostPlan
	planFloor       PlanGeneration
	tokenFloor      OwnerToken
	outstanding     []CommandIdentity
	lingering       []lingered
	cancelled       []CommandIdentity
	nextCapture     uint64
	explained       []uint64
	lastControlTime Tick
	lastDataCapture Tick
	detached        bool
	// retainedStrict marks outstanding identities kept through a fence; they match
	// a completion only while the data consumer has not passed retainedHorizon.
	retainedStrict  bool
	retainedHorizon uint64
}

// AttachHost validates a fresh mapping before any lane becomes reachable.
func AttachHost(mem []byte, expected Expected) (*Host, error) {
	if expected.ClockDomain == 0 {
		return nil, ErrIPCClock
	}
	layout, err := ValidateMapping(mem, expected)
	if err != nil {
		return nil, err
	}
	limits := expected.Limits
	lanes := attachLanes(mem, &layout, limits, false)
	return &Host{
		expected:    expected,
		clock:       seedClock(expected.ClockDomain),
		lanes:       lanes,
		plans:       make([]hostPlan, 0, limits.MaxPlans),
		planFloor:   expected.PlanFloor,
		tokenFloor:  expected.TokenFloor,
		outstanding: make([]CommandIdentity, 0, limits.MaxOutstanding),
		lingering:   make([]lingered, 0, lanes[LaneWorkerData-1].layout.capacity),
		cancelled:   make([]CommandIdentity, 0, limits.MaxOutstanding),
		nextCapture: 1,
		explained:   make([]uint64, 0, lanes[LaneWorkerControl-1].layout.capacity),
	}, nil
}

func (host *Host) lane(kind LaneKind) *lane {
	return &host.lanes[kind-1]
}

// OutputOpen reports whether new commands and publications may be queued.
func (host *Host) OutputOpen() bool {
	return !host.detached && host.fence.Reason == FenceNone
}

// Fence returns the fence cause; a worker gap replaces a local first cause.
func (host *Host) Fence() FenceState { return host.fence }

// LaneStats returns this side's counters for one lane.
func (host *Host) LaneStats(kind LaneKind) LaneStats {
	return host.lane(kind).stats
}

// CancelledCommands appends the commands a fence revoked.
func (host *Host) CancelledCommands(dst []CommandIdentity) []CommandIdentity {
	return append(dst, host.cancelled...)
}

// QuarantinedPlans appends plans with no exact acknowledgement yet.
func (host *Host) QuarantinedPlans(dst []PlanRef) []PlanRef {
	for index := range host.plans {
		dst = append(dst, host.plans[index].ref)
	}
	return dst
}

func (host *Host) closeWorker(reason FenceReason, sequence uint64, tick Tick) {
	if host.fence.Reason == FenceNone || reason == FenceWorkerGap {
		host.fence = FenceState{Reason: reason, Sequence: sequence, CaptureTime: tick}
	}
	// A completion queued before the fence may still wait in the data lane, so each
	// cancelled identity stays matchable until the data tail passes the head seen now.
	// An identity with no room to linger stays outstanding, so a completion queued
	// before the fence still matches instead of draining as stale.
	// An entry already lingering from expiry cleanup is tightened to the same horizon,
	// so a completion posted after the fence is stale there too.
	// The horizon is read once, so every identity shares one snapshot of the head, and
	// a retained identity keeps the horizon it already held when it moves to lingering.
	var fenceHorizon uint64
	if !host.detached {
		fenceData := host.lane(LaneWorkerData)
		fenceHorizon = loadAcquire(fenceData.mem, fenceData.layout.producerOffset)
		for index := range host.lingering {
			held := &host.lingering[index]
			if !held.strict {
				// The expiry snapshot may predate a valid completion the worker queued
				// before the fence, so the entry adopts the fence horizon.
				held.strict = true
				held.horizon = fenceHorizon
			}
		}
	}
	lingerHorizon := fenceHorizon
	if host.retainedStrict && host.retainedHorizon < lingerHorizon {
		lingerHorizon = host.retainedHorizon
	}
	kept := host.outstanding[:0]
	for _, identity := range host.outstanding {
		if !host.cancelling(identity) {
			host.cancelled = append(host.cancelled, identity)
		}
		if host.detached || !host.linger(identity, true, &lingerHorizon, false) {
			if !host.detached {
				kept = append(kept, identity)
			}
		}
	}
	host.outstanding = kept
	// Identities that found no lingering room stay outstanding but share the fence
	// horizon, so a completion posted after the fence is stale for them too.
	if len(kept) > 0 && !host.detached {
		if !host.retainedStrict || host.retainedHorizon > fenceHorizon {
			host.retainedHorizon = fenceHorizon
		}
		host.retainedStrict = true
	}
}

// cancelling reports that a fence already listed the identity as cancelled.
func (host *Host) cancelling(identity CommandIdentity) bool {
	for _, held := range host.cancelled {
		if held.matches(identity) {
			return true
		}
	}
	return false
}

func (host *Host) tick(now ClockReading) error {
	if host.detached {
		return ErrDetached
	}
	if err := host.clock.observe(now); err != nil || now.Tick == 0 {
		host.closeWorker(FenceClock, 0, 0)
		return ErrIPCClock
	}
	for index := range host.lanes {
		if host.lanes[index].saturated {
			host.closeWorker(FenceCounter, 0, 0)
		}
	}
	expired, err := host.lane(LaneHostControl).serviceExpired(host.expected.Limits.ControlServiceTicks, now.Tick)
	if err != nil {
		host.closeWorker(FenceCorruption, 0, 0)
	} else if expired {
		host.closeWorker(FenceControlDeadline, 0, 0)
	}
	return nil
}

func (host *Host) findPlan(ref PlanRef) int {
	for index := range host.plans {
		if host.plans[index].ref.matches(ref) {
			return index
		}
	}
	return -1
}

func (host *Host) refOnMapping(ref PlanRef) bool {
	return ref.Worker == host.expected.Binding.Worker &&
		ref.Stream == host.expected.Binding.Stream
}

// writeControl keeps the reserved slot free and fences on saturation. It never
// waits: the worker's own deadlines stay the authority over its output.
func (host *Host) writeControl(record *Record) error {
	err := host.lane(LaneHostControl).write(record, reservedSlots)
	switch err {
	case nil, ErrRecord, ErrIPCOversize:
		return err
	case ErrFull:
		host.closeWorker(FenceControlSaturated, 0, 0)
		return ErrControlFull
	default:
		host.closeWorker(FenceCorruption, 0, 0)
		return err
	}
}

// PublishPlan queues a reference to an immutable arena range.
func (host *Host) PublishPlan(ref PlanRef, arenaOffset uint64, planBytes uint32, now ClockReading) error {
	if err := host.tick(now); err != nil {
		return err
	}
	if !host.OutputOpen() {
		return ErrOutputFenced
	}
	if !host.refOnMapping(ref) || ref.Plan <= host.planFloor {
		return ErrStale
	}
	if now.Tick >= ref.PlanExpiry {
		return ErrIPCExpired
	}
	if len(host.plans) == cap(host.plans) {
		return ErrIPCCapacity
	}
	record := Record{Kind: RecordPublish, ArenaOffset: arenaOffset, PlanBytes: planBytes}
	record.applyRef(ref)
	if err := host.writeControl(&record); err != nil {
		return err
	}
	host.planFloor = ref.Plan
	host.plans = append(host.plans, hostPlan{ref: ref})
	return nil
}

// RevokePlan queues a revocation, also on a fenced stream. Saturation fences
// the host and keeps the plan quarantined; the call may be repeated. A revocation
// queued after the stop must be read before either side settles or tears down.
func (host *Host) RevokePlan(ref PlanRef, now ClockReading) error {
	// The revocation record carries no time, so a latched host clock fence does not
	// stop it: the fence refuses new work, never the revocation of published work.
	if err := host.tick(now); err != nil && err != ErrIPCClock {
		return err
	}
	index := host.findPlan(ref)
	if index < 0 {
		return ErrStale
	}
	if host.plans[index].revoking {
		return nil
	}
	record := Record{Kind: RecordRevoke}
	record.applyRef(ref)
	if err := host.writeControl(&record); err != nil {
		return err
	}
	host.plans[index].revoking = true
	return nil
}

// lingered is a retired identity with the worker data head seen at retirement:
// a completion queued before that head is consumed once the data tail passes it.
type lingered struct {
	identity CommandIdentity
	horizon  uint64
	// strict marks a fence-cancelled identity: only a record queued before the
	// horizon is evidence, a completion posted after the fence stays stale.
	strict bool
	// retired marks an identity whose plan retirement the worker acknowledged:
	// the worker then holds no deferred output for it, so it cannot publish later.
	retired bool
}

// linger keeps an identity that no longer occupies outstanding capacity, since
// a completion queued before the worker refused or retired it may still be waiting
// in the data lane. A fence keeps the list, so evidence queued earlier stays
// matchable while old data drains. The list is bounded by the data lane capacity.
// A full list first drops entries whose horizon the data tail has passed; it never
// evicts an entry that may still have a queued completion. Without room it reports
// false and the caller keeps the identity outstanding.
func (host *Host) linger(identity CommandIdentity, strict bool, at *uint64, retired bool) bool {
	if cap(host.lingering) == 0 {
		return false
	}
	data := host.lane(LaneWorkerData)
	if len(host.lingering) == cap(host.lingering) {
		kept := host.lingering[:0]
		// A non-strict entry may still meet a completion the worker queues after the
		// horizon captured at expiry, so it is evicted only once the consumer passed
		// the producer index current now.
		produced := loadAcquire(data.mem, data.layout.producerOffset)
		progress := host.lastControlTime
		if host.lastDataCapture > progress {
			progress = host.lastDataCapture
		}
		for _, held := range host.lingering {
			// Worker progress proves publication impossible only once a worker record
			// carries a capture time at or past the identity's deadline: the worker
			// refuses a later completion. A paused worker with an empty lane proves
			// nothing, so the entry is kept and new work is refused instead.
			pending := !held.strict && !held.retired && (produced > data.localConsumer || held.identity.live(progress))
			if held.horizon > data.localConsumer || pending {
				kept = append(kept, held)
			}
		}
		host.lingering = kept
		if len(host.lingering) == cap(host.lingering) {
			return false
		}
	}
	var horizon uint64
	if at != nil {
		horizon = *at
	} else {
		horizon = loadAcquire(data.mem, data.layout.producerOffset)
	}
	host.lingering = append(host.lingering, lingered{identity: identity, horizon: horizon, strict: strict, retired: retired})
	return true
}

// retireExpired moves outstanding commands whose deadline has passed out of the
// capacity count: the worker refuses them, though an earlier completion may linger.
// An identity with no room to linger stays outstanding.
func (host *Host) retireExpired(now Tick) {
	kept := host.outstanding[:0]
	for _, identity := range host.outstanding {
		if identity.live(now) || !host.lingerMoved(identity, false) {
			kept = append(kept, identity)
		}
	}
	host.outstanding = kept
}

// lingerMoved moves an outstanding identity to lingering and keeps the strict
// horizon a fence already set for retained identities.
func (host *Host) lingerMoved(identity CommandIdentity, retired bool) bool {
	if host.retainedStrict {
		horizon := host.retainedHorizon
		return host.linger(identity, true, &horizon, retired)
	}
	return host.linger(identity, false, nil, retired)
}

// SendCommand queues one command. A full data lane refuses it without any
// other effect, and control lanes stay usable.
func (host *Host) SendCommand(identity CommandIdentity, payload []byte, now ClockReading) error {
	if err := host.tick(now); err != nil {
		return err
	}
	if !host.OutputOpen() {
		return ErrOutputFenced
	}
	index := host.findPlan(identity.Ref)
	if index < 0 || identity.OwnerToken <= host.tokenFloor {
		return ErrStale
	}
	if host.plans[index].revoking {
		return ErrIPCRevoked
	}
	if !identity.live(now.Tick) {
		return ErrIPCExpired
	}
	host.retireExpired(now.Tick)
	if len(host.outstanding) == cap(host.outstanding) {
		return ErrIPCCapacity
	}
	record := Record{Kind: RecordCommand, Payload: payload}
	record.applyIdentity(identity)
	data := host.lane(LaneHostData)
	if err := data.write(&record, 0); err != nil {
		if err != ErrFull && err != ErrRecord && err != ErrIPCOversize {
			host.closeWorker(FenceCorruption, 0, 0)
		}
		return err
	}
	host.tokenFloor = identity.OwnerToken
	host.outstanding = append(host.outstanding, identity)
	return nil
}

// accountSequence verifies one worker ordinal. Control records may arrive ahead
// of older data, so they stay explained in a bounded list until data catches up.
// Exceeding that list breaches the reorder bound and is a gap.
func (host *Host) accountSequence(sequence uint64, control bool) bool {
	if sequence < host.nextCapture {
		return false
	}
	if sequence > host.nextCapture {
		if !control || len(host.explained) == cap(host.explained) {
			return false
		}
		for _, held := range host.explained {
			if held == sequence {
				return false
			}
		}
		host.explained = append(host.explained, sequence)
		return true
	}
	if sequence == math.MaxUint64 {
		return true
	}
	host.nextCapture = sequence + 1
	for advanced := true; advanced; {
		advanced = false
		for index, held := range host.explained {
			if held == host.nextCapture && held != math.MaxUint64 {
				host.nextCapture++
				host.explained = append(host.explained[:index], host.explained[index+1:]...)
				advanced = true
				break
			}
		}
	}
	return true
}

func (host *Host) identityMatches(record *Record) bool {
	return record.Worker == host.expected.Binding.Worker &&
		record.Stream == host.expected.Binding.Stream
}

// Poll delivers one worker record with its dequeue time. The data head is
// sampled before control is read, so every older control record of a visible data
// record has been seen and a remaining discontinuity is truly unexplained. Control
// takes effect at once, even while older data waits. A fenced host keeps delivering
// retained evidence without reopening anything.
func (host *Host) Poll(dst []byte, now ClockReading) (Delivery, bool, error) {
	if err := host.tick(now); err != nil {
		if err != ErrIPCClock {
			return Delivery{}, false, err
		}
		return host.pollFencedControl(dst)
	}
	data := host.lane(LaneWorkerData)
	dataHead := loadAcquire(data.mem, data.layout.producerOffset)
	control := host.lane(LaneWorkerControl)
	// While the early-control list is full and older data is visible, that data is
	// drained first: another control record could not be explained, and the overtaking
	// count is bounded by the data already queued rather than by the lane capacity.
	drainData := len(host.explained) == cap(host.explained) && data.localConsumer < dataHead
	var record Record
	var ok bool
	var err error
	if !drainData {
		record, ok, err = control.read(dst)
		if err != nil {
			return Delivery{}, false, host.readFailed(err)
		}
		if ok {
			return host.handleControl(&record, now.Tick, false)
		}
	}
	if data.localConsumer >= dataHead {
		if _, _, err := data.indices(); err != nil {
			return Delivery{}, false, host.readFailed(err)
		}
		return Delivery{}, false, nil
	}
	record, ok, err = data.read(dst)
	if err != nil {
		return Delivery{}, false, host.readFailed(err)
	}
	if !ok {
		return Delivery{}, false, nil
	}
	return host.handleData(&record, now.Tick)
}

// pollFencedControl keeps draining the worker control lane after a host clock
// fence, so a retirement acknowledgement or gap is consumed and graceful
// resynchronization can settle. The unreliable clock is not trusted: the record's
// dequeue time is zero, its capture time is not compared, data stays fenced and the
// call still reports the clock refusal beside any consumed record.
func (host *Host) pollFencedControl(dst []byte) (Delivery, bool, error) {
	control := host.lane(LaneWorkerControl)
	record, ok, err := control.read(dst)
	if err != nil {
		return Delivery{}, false, host.readFailed(err)
	}
	if !ok {
		return Delivery{}, false, ErrIPCClock
	}
	delivery, handled, err := host.handleControl(&record, 0, true)
	if err == nil {
		err = ErrIPCClock
	}
	return delivery, handled, err
}

func (host *Host) readFailed(err error) error {
	if err != ErrBuffer {
		host.closeWorker(FenceCorruption, 0, 0)
	}
	return err
}

func (host *Host) captureRegressed(record *Record, last *Tick, now Tick) bool {
	if record.CaptureTime > now || record.CaptureTime < *last {
		return true
	}
	*last = record.CaptureTime
	return false
}

func (host *Host) handleControl(record *Record, now Tick, clockFenced bool) (Delivery, bool, error) {
	control := host.lane(LaneWorkerControl)
	if !host.identityMatches(record) {
		control.bump(&control.stats.Dropped)
		return Delivery{}, false, ErrStale
	}
	regressed := !clockFenced && host.captureRegressed(record, &host.lastControlTime, now)
	delivery := Delivery{Record: *record, DequeueTime: now}
	if record.Kind == RecordGap {
		host.accountSequence(record.CaptureSequence, true)
		host.closeWorker(FenceWorkerGap, record.CaptureSequence, record.CaptureTime)
		return delivery, true, nil
	}
	if regressed {
		host.closeWorker(FenceClock, record.CaptureSequence, record.CaptureTime)
		return Delivery{}, false, ErrIPCClock
	}
	if host.OutputOpen() && !host.accountSequence(record.CaptureSequence, true) {
		host.closeWorker(FenceDiscontinuity, record.CaptureSequence, record.CaptureTime)
	}
	index := host.findPlan(record.ref())
	if index < 0 || !host.plans[index].revoking {
		control.bump(&control.stats.Dropped)
		return Delivery{}, false, ErrStale
	}
	host.plans = append(host.plans[:index], host.plans[index+1:]...)
	// The worker cleared its active state for the retired plan and refuses its
	// commands without a completion, so they cannot stay outstanding.
	retired := record.ref()
	kept := host.outstanding[:0]
	for _, identity := range host.outstanding {
		if !identity.Ref.matches(retired) || !host.lingerMoved(identity, true) {
			kept = append(kept, identity)
		}
	}
	host.outstanding = kept
	return delivery, true, nil
}

func (host *Host) handleData(record *Record, now Tick) (Delivery, bool, error) {
	data := host.lane(LaneWorkerData)
	if !host.identityMatches(record) {
		data.bump(&data.stats.Dropped)
		return Delivery{}, false, ErrStale
	}
	if host.captureRegressed(record, &host.lastDataCapture, now) {
		host.closeWorker(FenceClock, record.CaptureSequence, record.CaptureTime)
		return Delivery{}, false, ErrIPCClock
	}
	delivery := Delivery{Record: *record, DequeueTime: now}
	if host.OutputOpen() && !host.accountSequence(record.CaptureSequence, false) {
		host.closeWorker(FenceDiscontinuity, record.CaptureSequence, record.CaptureTime)
		return delivery, true, ErrGap
	}
	if record.Kind == RecordCompletion {
		identity := record.identity()
		for index := range host.outstanding {
			if host.outstanding[index].matches(identity) && (!host.retainedStrict || data.localConsumer <= host.retainedHorizon) {
				host.outstanding = append(host.outstanding[:index], host.outstanding[index+1:]...)
				return delivery, true, nil
			}
		}
		for index := range host.lingering {
			held := &host.lingering[index]
			// The record was just read, so its data index is one below the consumer.
			if held.identity.matches(identity) && (!held.strict || data.localConsumer <= held.horizon) {
				host.lingering = append(host.lingering[:index], host.lingering[index+1:]...)
				return delivery, true, nil
			}
		}
		data.bump(&data.stats.Dropped)
		return Delivery{}, false, ErrStale
	}
	return delivery, true, nil
}

func (host *Host) sendStop() error {
	if host.stopSent {
		return nil
	}
	control := host.lane(LaneHostControl)
	index := control.localProducer
	record := Record{
		Kind:   RecordStop,
		Worker: host.expected.Binding.Worker,
		Stream: host.expected.Binding.Stream,
	}
	if err := control.write(&record, 0); err != nil {
		return err
	}
	host.stopSent = true
	host.stopIndex = index
	return nil
}

// AcknowledgeGap names the exact observed fence and queues the stop that
// acknowledges it. The stop uses the reserved control slot.
func (host *Host) AcknowledgeGap(sequence uint64, now ClockReading) error {
	if err := host.tick(now); err != nil && err != ErrIPCClock {
		return err
	}
	if host.fence.Reason == FenceNone {
		return ErrNotFenced
	}
	if sequence != host.fence.Sequence {
		return ErrStale
	}
	return host.sendStop()
}

// RequestStop fences this side and queues the stream's stop for teardown.
func (host *Host) RequestStop(now ClockReading) error {
	if err := host.tick(now); err != nil && err != ErrIPCClock {
		return err
	}
	host.closeWorker(FenceStop, 0, 0)
	return host.sendStop()
}

// stopConsumed reports that the worker's consumer index passed the stop.
func (host *Host) stopConsumed() bool {
	if !host.stopSent {
		return false
	}
	_, consumer, err := host.lane(LaneHostControl).indices()
	return err == nil && consumer > host.stopIndex
}

// controlSettled reports that the worker consumed the stop and that no worker
// control record stays unread, so a pending gap or acknowledgement is not lost.
func (host *Host) controlSettled() bool {
	if !host.stopConsumed() {
		return false
	}
	producer, consumer, err := host.lane(LaneWorkerControl).indices()
	if err != nil || producer != consumer {
		return false
	}
	// A revocation queued behind the stop must be consumed before settlement.
	produced, read, err := host.lane(LaneHostControl).indices()
	if err != nil || produced != read {
		return false
	}
	// A revoked plan stays quarantined until its exact retirement is consumed.
	for index := range host.plans {
		if host.plans[index].revoking {
			return false
		}
	}
	return true
}

// ResyncExpected names the successor mapping once the fence is acknowledged: the
// worker consumed the stop and no worker control record stays unread.
func (host *Host) ResyncExpected(next StreamGeneration) (Expected, error) {
	if host.detached {
		return Expected{}, ErrDetached
	}
	if host.fence.Reason == FenceNone {
		return Expected{}, ErrNotFenced
	}
	if !host.controlSettled() {
		return Expected{}, ErrNotAcknowledged
	}
	if next <= host.expected.Binding.Stream {
		return Expected{}, ErrStale
	}
	return host.successor(host.expected.Binding.Worker, next), nil
}

func (host *Host) successor(worker WorkerID, stream StreamGeneration) Expected {
	successor := host.expected
	successor.Binding.Worker = worker
	successor.Binding.Stream = stream
	successor.PlanFloor = host.planFloor
	successor.TokenFloor = host.tokenFloor
	return successor
}

// ObserveWorkerTerminated records supervisor proof that the worker process is
// gone and that all its access to this mapping has ceased.
func (host *Host) ObserveWorkerTerminated() {
	host.terminated = true
	host.closeWorker(FenceWorkerTerminated, 0, 0)
}

// RestartExpected names a replacement worker's mapping. The worker incarnation
// strictly advances, floors carry over and no queued record survives, because the
// successor is a fresh mapping.
func (host *Host) RestartExpected(nextWorker WorkerID, nextStream StreamGeneration) (Expected, error) {
	if host.detached {
		return Expected{}, ErrDetached
	}
	if !host.terminated {
		return Expected{}, ErrNotAcknowledged
	}
	if nextWorker <= host.expected.Binding.Worker || nextStream == 0 {
		return Expected{}, ErrStale
	}
	return host.successor(nextWorker, nextStream), nil
}

// Teardown releases the mapping once the worker consumed the stop and the host
// read every worker control record (the worker's own teardown needs a raised gap
// consumed), or the worker is proven terminated. Stop consumption alone does not
// prove the worker ceased touching the mapping; that proof stays with the
// supervisor through ObserveWorkerTerminated. It never drains a lane, so a saturated data lane cannot hold shutdown.
func (host *Host) Teardown() error {
	if host.detached {
		return nil
	}
	if !host.terminated && !host.controlSettled() {
		return ErrNotAcknowledged
	}
	host.detached = true
	host.lanes = [laneCount]lane{}
	host.plans = nil
	host.outstanding = nil
	host.lingering = nil
	return nil
}
