package localworker

import (
	aa24binary "encoding/binary"
	aa24errors "errors"
	aa24math "math"
	aa24atomic "sync/atomic"
	aa24unsafe "unsafe"
)

// This file is the AA24 reference model of the offline local-worker ABI: a byte
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
	// AA24ABIVersion is the only accepted layout version.
	AA24ABIVersion uint32 = 1

	aa24HeaderBytes     uint64 = 64
	aa24DescriptorBytes uint64 = 64
	aa24EnvelopeBytes   uint32 = 128
	aa24BuildBytes      uint64 = 32
	aa24Alignment       uint64 = 8
	aa24LaneCount       uint32 = 4

	aa24HeaderMagic        = "LWB1"
	aa24OffsetVersion      = 4
	aa24OffsetHeaderBytes  = 8
	aa24OffsetTotalBytes   = 12
	aa24OffsetWorker       = 16
	aa24OffsetStream       = 24
	aa24OffsetLaneCount    = 32
	aa24OffsetDescBytes    = 36
	aa24OffsetDescTable    = 40
	aa24OffsetArena        = 48
	aa24OffsetArenaBytes   = 56
	aa24DescOffsetSlot     = 4
	aa24DescOffsetCapacity = 8
	aa24DescOffsetReserved = 12
	aa24DescOffsetRegion   = 16
	aa24DescOffsetProducer = 24
	aa24DescOffsetConsumer = 32
	aa24DescOffsetTail     = 40

	aa24CapacityCeiling uint32 = 1 << 20
	aa24TableCeiling    uint32 = 1 << 16
	aa24ReservedSlots   uint64 = 1
)

// Refusals are sentinels. A validation refusal has no effect; a refusal raised by
// a live lane fences the endpoint where its operation says so.
var (
	ErrAA24InvalidLimits   = aa24errors.New("localworker: invalid ABI limits or configuration")
	ErrAA24Identity        = aa24errors.New("localworker: zero or mismatched worker or stream identity")
	ErrAA24ABI             = aa24errors.New("localworker: ABI magic, version or fixed size mismatch")
	ErrAA24Build           = aa24errors.New("localworker: build identity mismatch")
	ErrAA24MappingSize     = aa24errors.New("localworker: mapping size mismatch")
	ErrAA24Alignment       = aa24errors.New("localworker: misaligned mapping or offset")
	ErrAA24Bounds          = aa24errors.New("localworker: region outside the mapping")
	ErrAA24Overlap         = aa24errors.New("localworker: regions overlap")
	ErrAA24Lane            = aa24errors.New("localworker: invalid lane kind, slot size or capacity")
	ErrAA24Reserved        = aa24errors.New("localworker: reserved bytes are not zero")
	ErrAA24Index           = aa24errors.New("localworker: lane index invariant broken")
	ErrAA24Ownership       = aa24errors.New("localworker: role does not own this lane index")
	ErrAA24Full            = aa24errors.New("localworker: lane is full")
	ErrAA24ControlFull     = aa24errors.New("localworker: control lane is saturated")
	ErrAA24Counter         = aa24errors.New("localworker: counter refuses to wrap")
	ErrAA24Truncated       = aa24errors.New("localworker: record does not fit its slot")
	ErrAA24Oversize        = aa24errors.New("localworker: payload exceeds the configured cap")
	ErrAA24Buffer          = aa24errors.New("localworker: destination buffer is too small")
	ErrAA24Record          = aa24errors.New("localworker: malformed record")
	ErrAA24Stale           = aa24errors.New("localworker: stale identity dropped")
	ErrAA24Revoked         = aa24errors.New("localworker: plan revocation observed")
	ErrAA24Expired         = aa24errors.New("localworker: owner, plan or request deadline expired")
	ErrAA24OutputFenced    = aa24errors.New("localworker: output decisions are fenced")
	ErrAA24Gap             = aa24errors.New("localworker: protocol gap raised")
	ErrAA24Clock           = aa24errors.New("localworker: clock regression or impossible capture time")
	ErrAA24NotFenced       = aa24errors.New("localworker: no fence to acknowledge")
	ErrAA24NotAcknowledged = aa24errors.New("localworker: fence acknowledgement is unsettled")
	ErrAA24Capacity        = aa24errors.New("localworker: bounded table exhausted")
	ErrAA24Detached        = aa24errors.New("localworker: endpoint is torn down")
	ErrAA24Mismatch        = aa24errors.New("localworker: exact command identity mismatch")
)

// AA24LaneKind names the four lanes by their documented descriptor value.
type AA24LaneKind uint32

const (
	AA24LaneHostControl AA24LaneKind = iota + 1
	AA24LaneWorkerControl
	AA24LaneHostData
	AA24LaneWorkerData
)

// AA24RecordKind is the slot envelope kind.
type AA24RecordKind uint32

const (
	AA24RecordPublish AA24RecordKind = iota + 1
	AA24RecordRevoke
	AA24RecordAcknowledge
	AA24RecordGap
	AA24RecordStop
	AA24RecordCommand
	AA24RecordRaw
	AA24RecordCompletion
)

// AA24Status is the slot envelope status.
type AA24Status uint32

const (
	AA24StatusPending AA24Status = iota
	AA24StatusAccepted
	AA24StatusRefused
	AA24StatusRetired
)

// AA24Direction is the slot envelope direction.
type AA24Direction uint8

const (
	AA24DirectionControl AA24Direction = iota
	AA24DirectionReceive
	AA24DirectionEcho
)

const aa24MaxPhase uint8 = 5

// AA24OwnerToken is a nonzero, nonreused command-owner reservation.
type AA24OwnerToken uint64

// AA24BuildIdentity is an opaque digest binding both processes to one build.
type AA24BuildIdentity [aa24BuildBytes]byte

// AA24Binding is the identity a mapping must carry exactly.
type AA24Binding struct {
	AA24Build  AA24BuildIdentity
	AA24Worker AA23WorkerID
	AA24Stream AA23StreamGeneration
}

func (aa24Binding AA24Binding) aa24Validate() error {
	if aa24Binding.AA24Build == (AA24BuildIdentity{}) {
		return ErrAA24Build
	}
	if aa24Binding.AA24Worker == 0 || aa24Binding.AA24Stream == 0 {
		return ErrAA24Identity
	}
	return nil
}

// AA24Limits holds the finite fixture parameters.
type AA24Limits struct {
	AA24MaxMappingBytes     uint32
	AA24MaxSlotBytes        uint32
	AA24MaxCapacity         uint32
	AA24MaxPayloadBytes     uint32
	AA24MaxArenaBytes       uint64
	AA24MaxPlans            uint32
	AA24MaxOutstanding      uint32
	AA24ControlServiceTicks AA23Tick
}

func (aa24Limits AA24Limits) aa24Validate() error {
	if aa24Limits.AA24MaxMappingBytes == 0 || aa24Limits.AA24MaxCapacity == 0 ||
		aa24Limits.AA24MaxPayloadBytes == 0 || aa24Limits.AA24MaxArenaBytes == 0 ||
		aa24Limits.AA24MaxPlans == 0 || aa24Limits.AA24MaxOutstanding == 0 ||
		aa24Limits.AA24ControlServiceTicks == 0 {
		return ErrAA24InvalidLimits
	}
	if aa24Limits.AA24MaxSlotBytes < aa24EnvelopeBytes ||
		aa24Limits.AA24MaxPayloadBytes > aa24Limits.AA24MaxSlotBytes-aa24EnvelopeBytes ||
		aa24Limits.AA24MaxCapacity > aa24CapacityCeiling ||
		aa24Limits.AA24MaxPlans > aa24TableCeiling || aa24Limits.AA24MaxOutstanding > aa24TableCeiling {
		return ErrAA24InvalidLimits
	}
	return nil
}

// AA24Expected is what one side demands of a mapping before any access. The
// floors carry nonreused plan and token identities across a resynchronization.
type AA24Expected struct {
	AA24Binding      AA24Binding
	AA24MappingBytes uint32
	AA24Limits       AA24Limits
	AA24PlanFloor    AA23PlanGeneration
	AA24TokenFloor   AA24OwnerToken
}

// AA24LaneConfig sizes one lane.
type AA24LaneConfig struct {
	AA24SlotBytes uint32
	AA24Capacity  uint32
}

// AA24Config sizes one mapping. Lanes are indexed by kind minus one.
type AA24Config struct {
	AA24Lanes      [aa24LaneCount]AA24LaneConfig
	AA24ArenaBytes uint64
}

type aa24LaneLayout struct {
	aa24Kind           AA24LaneKind
	aa24SlotBytes      uint32
	aa24Capacity       uint32
	aa24SlotOffset     uint64
	aa24ProducerOffset uint64
	aa24ConsumerOffset uint64
}

// AA24Layout is the validated, process-local copy of a mapping's fixed layout.
// Nothing reads the header or descriptors again after validation.
type AA24Layout struct {
	AA24TotalBytes       uint32
	AA24DescriptorOffset uint64
	AA24ArenaOffset      uint64
	AA24ArenaBytes       uint64
	aa24Lanes            [aa24LaneCount]aa24LaneLayout
}

func aa24AddChecked(aa24First uint64, aa24Second uint64) (uint64, bool) {
	aa24Sum := aa24First + aa24Second
	return aa24Sum, aa24Sum >= aa24First
}

func aa24MulChecked(aa24First uint64, aa24Second uint64) (uint64, bool) {
	if aa24First != 0 && aa24Second > aa24math.MaxUint64/aa24First {
		return 0, false
	}
	return aa24First * aa24Second, true
}

func aa24Aligned(aa24Offset uint64) bool {
	return aa24Offset%aa24Alignment == 0
}

func aa24BaseAligned(aa24Mem []byte) bool {
	return len(aa24Mem) > 0 && uintptr(aa24unsafe.Pointer(&aa24Mem[0]))%uintptr(aa24Alignment) == 0
}

// aa24CheckLane validates one lane's sizes. Each control lane needs a slot beyond
// the reserved one, so ordinary control can never occupy the gap or stop slot.
func aa24CheckLane(aa24Kind AA24LaneKind, aa24SlotBytes uint32, aa24Capacity uint32, aa24Limits AA24Limits) (uint64, error) {
	if aa24SlotBytes < aa24EnvelopeBytes || !aa24Aligned(uint64(aa24SlotBytes)) ||
		aa24SlotBytes > aa24Limits.AA24MaxSlotBytes || aa24Capacity == 0 ||
		aa24Capacity > aa24Limits.AA24MaxCapacity {
		return 0, ErrAA24Lane
	}
	if (aa24Kind == AA24LaneHostControl || aa24Kind == AA24LaneWorkerControl) && uint64(aa24Capacity) <= aa24ReservedSlots {
		return 0, ErrAA24Lane
	}
	aa24Region, aa24OK := aa24MulChecked(uint64(aa24SlotBytes), uint64(aa24Capacity))
	if !aa24OK {
		return 0, ErrAA24Bounds
	}
	return aa24Region, nil
}

// AA24InitMapping writes a fresh mapping into caller memory and validates it. A
// refusal leaves the memory untouched.
func AA24InitMapping(aa24Mem []byte, aa24Binding AA24Binding, aa24Config AA24Config, aa24Limits AA24Limits) (AA24Expected, AA24Layout, error) {
	if aa24Err := aa24Limits.aa24Validate(); aa24Err != nil {
		return AA24Expected{}, AA24Layout{}, aa24Err
	}
	if aa24Err := aa24Binding.aa24Validate(); aa24Err != nil {
		return AA24Expected{}, AA24Layout{}, aa24Err
	}
	if !aa24BaseAligned(aa24Mem) {
		return AA24Expected{}, AA24Layout{}, ErrAA24Alignment
	}
	if aa24Config.AA24ArenaBytes <= aa24BuildBytes || aa24Config.AA24ArenaBytes > aa24Limits.AA24MaxArenaBytes ||
		!aa24Aligned(aa24Config.AA24ArenaBytes) {
		return AA24Expected{}, AA24Layout{}, ErrAA24InvalidLimits
	}
	aa24Cursor := aa24HeaderBytes + uint64(aa24LaneCount)*aa24DescriptorBytes
	var aa24Regions [aa24LaneCount]uint64
	for aa24Index, aa24Lane := range aa24Config.AA24Lanes {
		aa24Size, aa24Err := aa24CheckLane(AA24LaneKind(aa24Index+1), aa24Lane.AA24SlotBytes, aa24Lane.AA24Capacity, aa24Limits)
		if aa24Err != nil {
			return AA24Expected{}, AA24Layout{}, aa24Err
		}
		aa24Regions[aa24Index] = aa24Cursor
		var aa24OK bool
		if aa24Cursor, aa24OK = aa24AddChecked(aa24Cursor, aa24Size); !aa24OK {
			return AA24Expected{}, AA24Layout{}, ErrAA24Bounds
		}
	}
	aa24ArenaOffset := aa24Cursor
	aa24Total, aa24OK := aa24AddChecked(aa24ArenaOffset, aa24Config.AA24ArenaBytes)
	if !aa24OK || aa24Total > uint64(aa24Limits.AA24MaxMappingBytes) || aa24Total != uint64(len(aa24Mem)) {
		return AA24Expected{}, AA24Layout{}, ErrAA24MappingSize
	}
	clear(aa24Mem)
	copy(aa24Mem, aa24HeaderMagic)
	aa24Put32 := aa24binary.LittleEndian.PutUint32
	aa24Put64 := aa24binary.LittleEndian.PutUint64
	aa24Put32(aa24Mem[aa24OffsetVersion:], AA24ABIVersion)
	aa24Put32(aa24Mem[aa24OffsetHeaderBytes:], uint32(aa24HeaderBytes))
	aa24Put32(aa24Mem[aa24OffsetTotalBytes:], uint32(aa24Total))
	aa24Put64(aa24Mem[aa24OffsetWorker:], uint64(aa24Binding.AA24Worker))
	aa24Put64(aa24Mem[aa24OffsetStream:], uint64(aa24Binding.AA24Stream))
	aa24Put32(aa24Mem[aa24OffsetLaneCount:], aa24LaneCount)
	aa24Put32(aa24Mem[aa24OffsetDescBytes:], uint32(aa24DescriptorBytes))
	aa24Put64(aa24Mem[aa24OffsetDescTable:], aa24HeaderBytes)
	aa24Put64(aa24Mem[aa24OffsetArena:], aa24ArenaOffset)
	aa24Put64(aa24Mem[aa24OffsetArenaBytes:], aa24Config.AA24ArenaBytes)
	for aa24Index, aa24Lane := range aa24Config.AA24Lanes {
		aa24Desc := aa24Mem[aa24HeaderBytes+uint64(aa24Index)*aa24DescriptorBytes:]
		aa24Put32(aa24Desc, uint32(aa24Index+1))
		aa24Put32(aa24Desc[aa24DescOffsetSlot:], aa24Lane.AA24SlotBytes)
		aa24Put32(aa24Desc[aa24DescOffsetCapacity:], aa24Lane.AA24Capacity)
		aa24Put64(aa24Desc[aa24DescOffsetRegion:], aa24Regions[aa24Index])
	}
	copy(aa24Mem[aa24ArenaOffset:], aa24Binding.AA24Build[:])
	aa24Expected := AA24Expected{AA24Binding: aa24Binding, AA24MappingBytes: uint32(aa24Total), AA24Limits: aa24Limits}
	aa24Layout, aa24Err := AA24ValidateMapping(aa24Mem, aa24Expected)
	return aa24Expected, aa24Layout, aa24Err
}

// AA24ValidateMapping checks every field, sum and product before any record is
// reachable. It only reads, so a refused replacement cannot disturb the state of
// an earlier valid mapping. It admits only a fresh mapping: both indices of each
// lane must still be zero.
func AA24ValidateMapping(aa24Mem []byte, aa24Expected AA24Expected) (AA24Layout, error) {
	aa24Limits := aa24Expected.AA24Limits
	if aa24Err := aa24Limits.aa24Validate(); aa24Err != nil {
		return AA24Layout{}, aa24Err
	}
	if aa24Err := aa24Expected.AA24Binding.aa24Validate(); aa24Err != nil {
		return AA24Layout{}, aa24Err
	}
	if !aa24BaseAligned(aa24Mem) {
		return AA24Layout{}, ErrAA24Alignment
	}
	aa24Length := uint64(len(aa24Mem))
	if aa24Length < aa24HeaderBytes || aa24Length > uint64(aa24Limits.AA24MaxMappingBytes) ||
		aa24Length != uint64(aa24Expected.AA24MappingBytes) {
		return AA24Layout{}, ErrAA24MappingSize
	}
	aa24Get32 := aa24binary.LittleEndian.Uint32
	aa24Get64 := aa24binary.LittleEndian.Uint64
	if string(aa24Mem[:len(aa24HeaderMagic)]) != aa24HeaderMagic ||
		aa24Get32(aa24Mem[aa24OffsetVersion:]) != AA24ABIVersion ||
		uint64(aa24Get32(aa24Mem[aa24OffsetHeaderBytes:])) != aa24HeaderBytes ||
		aa24Get32(aa24Mem[aa24OffsetLaneCount:]) != aa24LaneCount ||
		uint64(aa24Get32(aa24Mem[aa24OffsetDescBytes:])) != aa24DescriptorBytes {
		return AA24Layout{}, ErrAA24ABI
	}
	if uint64(aa24Get32(aa24Mem[aa24OffsetTotalBytes:])) != aa24Length {
		return AA24Layout{}, ErrAA24MappingSize
	}
	if aa24Get64(aa24Mem[aa24OffsetWorker:]) != uint64(aa24Expected.AA24Binding.AA24Worker) ||
		aa24Get64(aa24Mem[aa24OffsetStream:]) != uint64(aa24Expected.AA24Binding.AA24Stream) {
		return AA24Layout{}, ErrAA24Identity
	}
	aa24Layout := AA24Layout{
		AA24TotalBytes:       uint32(aa24Length),
		AA24DescriptorOffset: aa24Get64(aa24Mem[aa24OffsetDescTable:]),
		AA24ArenaOffset:      aa24Get64(aa24Mem[aa24OffsetArena:]),
		AA24ArenaBytes:       aa24Get64(aa24Mem[aa24OffsetArenaBytes:]),
	}
	if !aa24Aligned(aa24Layout.AA24DescriptorOffset) || !aa24Aligned(aa24Layout.AA24ArenaOffset) {
		return AA24Layout{}, ErrAA24Alignment
	}
	aa24TableBytes := uint64(aa24LaneCount) * aa24DescriptorBytes
	aa24TableEnd, aa24OK := aa24AddChecked(aa24Layout.AA24DescriptorOffset, aa24TableBytes)
	if !aa24OK || aa24TableEnd > aa24Length {
		return AA24Layout{}, ErrAA24Bounds
	}
	if aa24Layout.AA24ArenaBytes <= aa24BuildBytes || aa24Layout.AA24ArenaBytes > aa24Limits.AA24MaxArenaBytes {
		return AA24Layout{}, ErrAA24Bounds
	}
	aa24ArenaEnd, aa24OK := aa24AddChecked(aa24Layout.AA24ArenaOffset, aa24Layout.AA24ArenaBytes)
	if !aa24OK || aa24ArenaEnd > aa24Length {
		return AA24Layout{}, ErrAA24Bounds
	}
	aa24Spans := [][2]uint64{
		{0, aa24HeaderBytes},
		{aa24Layout.AA24DescriptorOffset, aa24TableEnd},
		{aa24Layout.AA24ArenaOffset, aa24ArenaEnd},
	}
	var aa24Seen [aa24LaneCount]bool
	for aa24Index := uint64(0); aa24Index < uint64(aa24LaneCount); aa24Index++ {
		aa24DescOffset := aa24Layout.AA24DescriptorOffset + aa24Index*aa24DescriptorBytes
		aa24Desc := aa24Mem[aa24DescOffset : aa24DescOffset+aa24DescriptorBytes]
		aa24Kind := AA24LaneKind(aa24Get32(aa24Desc))
		if aa24Kind < AA24LaneHostControl || aa24Kind > AA24LaneWorkerData || aa24Seen[aa24Kind-1] {
			return AA24Layout{}, ErrAA24Lane
		}
		aa24Seen[aa24Kind-1] = true
		if aa24Get32(aa24Desc[aa24DescOffsetReserved:]) != 0 {
			return AA24Layout{}, ErrAA24Reserved
		}
		for _, aa24Byte := range aa24Desc[aa24DescOffsetTail:] {
			if aa24Byte != 0 {
				return AA24Layout{}, ErrAA24Reserved
			}
		}
		aa24SlotBytes := aa24Get32(aa24Desc[aa24DescOffsetSlot:])
		aa24Capacity := aa24Get32(aa24Desc[aa24DescOffsetCapacity:])
		aa24RegionBytes, aa24Err := aa24CheckLane(aa24Kind, aa24SlotBytes, aa24Capacity, aa24Limits)
		if aa24Err != nil {
			return AA24Layout{}, aa24Err
		}
		aa24SlotOffset := aa24Get64(aa24Desc[aa24DescOffsetRegion:])
		if !aa24Aligned(aa24SlotOffset) {
			return AA24Layout{}, ErrAA24Alignment
		}
		aa24RegionEnd, aa24OK := aa24AddChecked(aa24SlotOffset, aa24RegionBytes)
		if !aa24OK || aa24RegionEnd > aa24Length {
			return AA24Layout{}, ErrAA24Bounds
		}
		if aa24Get64(aa24Desc[aa24DescOffsetProducer:]) != 0 || aa24Get64(aa24Desc[aa24DescOffsetConsumer:]) != 0 {
			return AA24Layout{}, ErrAA24Index
		}
		aa24Spans = append(aa24Spans, [2]uint64{aa24SlotOffset, aa24RegionEnd})
		aa24Layout.aa24Lanes[aa24Kind-1] = aa24LaneLayout{
			aa24Kind:           aa24Kind,
			aa24SlotBytes:      aa24SlotBytes,
			aa24Capacity:       aa24Capacity,
			aa24SlotOffset:     aa24SlotOffset,
			aa24ProducerOffset: aa24DescOffset + aa24DescOffsetProducer,
			aa24ConsumerOffset: aa24DescOffset + aa24DescOffsetConsumer,
		}
	}
	for aa24First := range aa24Spans {
		for aa24Second := aa24First + 1; aa24Second < len(aa24Spans); aa24Second++ {
			if aa24Spans[aa24First][0] < aa24Spans[aa24Second][1] && aa24Spans[aa24Second][0] < aa24Spans[aa24First][1] {
				return AA24Layout{}, ErrAA24Overlap
			}
		}
	}
	var aa24Build AA24BuildIdentity
	copy(aa24Build[:], aa24Mem[aa24Layout.AA24ArenaOffset:])
	if aa24Build != aa24Expected.AA24Binding.AA24Build {
		return AA24Layout{}, ErrAA24Build
	}
	return aa24Layout, nil
}

// aa24LoadAcquire and aa24StoreRelease are the only accesses to shared indices.
// The validator proved each offset aligned and in bounds against an aligned base.
func aa24LoadAcquire(aa24Mem []byte, aa24Offset uint64) uint64 {
	aa24Native := aa24atomic.LoadUint64((*uint64)(aa24unsafe.Pointer(&aa24Mem[aa24Offset])))
	var aa24Bytes [8]byte
	aa24binary.NativeEndian.PutUint64(aa24Bytes[:], aa24Native)
	return aa24binary.LittleEndian.Uint64(aa24Bytes[:])
}

func aa24StoreRelease(aa24Mem []byte, aa24Offset uint64, aa24Value uint64) {
	var aa24Bytes [8]byte
	aa24binary.LittleEndian.PutUint64(aa24Bytes[:], aa24Value)
	aa24atomic.StoreUint64((*uint64)(aa24unsafe.Pointer(&aa24Mem[aa24Offset])), aa24binary.NativeEndian.Uint64(aa24Bytes[:]))
}

// AA24Record is the detached form of one slot. Its payload never aliases shared
// memory.
type AA24Record struct {
	AA24Kind            AA24RecordKind
	AA24Worker          AA23WorkerID
	AA24Stream          AA23StreamGeneration
	AA24Transaction     AA23TransactionOrdinal
	AA24Plan            AA23PlanGeneration
	AA24Subscriber      AA23SubscriberID
	AA24Epoch           AA23RegistrationEpoch
	AA24OwnerToken      AA24OwnerToken
	AA24OwnerExpiry     AA23Tick
	AA24CaptureSequence uint64
	AA24CaptureTime     AA23Tick
	AA24PlanExpiry      AA23Tick
	AA24RequestDeadline AA23Tick
	AA24ArenaOffset     uint64
	AA24PlanBytes       uint32
	AA24Status          AA24Status
	AA24Target          byte
	AA24RawOctet        byte
	AA24Direction       AA24Direction
	AA24Phase           uint8
	AA24Flags           uint32
	AA24Payload         []byte
}

// AA24Delivery separates when a record was observed from when it was dequeued.
// The dequeue time never replaces the capture time in the record.
type AA24Delivery struct {
	AA24Record      AA24Record
	AA24DequeueTime AA23Tick
}

// AA24PlanRef is the exact identity of one published plan.
type AA24PlanRef struct {
	AA24Key        AA23RegistrationKey
	AA24Worker     AA23WorkerID
	AA24Stream     AA23StreamGeneration
	AA24Plan       AA23PlanGeneration
	AA24PlanExpiry AA23Tick
}

func (aa24Ref AA24PlanRef) aa24Matches(aa24Other AA24PlanRef) bool {
	if !aa24Ref.AA24Key.aa23Matches(aa24Other.AA24Key) {
		return false
	}
	if aa24Ref.AA24Worker != aa24Other.AA24Worker {
		return false
	}
	if aa24Ref.AA24Stream != aa24Other.AA24Stream {
		return false
	}
	if aa24Ref.AA24Plan != aa24Other.AA24Plan {
		return false
	}
	return aa24Ref.AA24PlanExpiry == aa24Other.AA24PlanExpiry
}

// AA24CommandIdentity is the exact identity of one reserved command.
type AA24CommandIdentity struct {
	AA24Ref             AA24PlanRef
	AA24Transaction     AA23TransactionOrdinal
	AA24OwnerToken      AA24OwnerToken
	AA24OwnerExpiry     AA23Tick
	AA24RequestDeadline AA23Tick
}

func (aa24Identity AA24CommandIdentity) aa24Matches(aa24Other AA24CommandIdentity) bool {
	if !aa24Identity.AA24Ref.aa24Matches(aa24Other.AA24Ref) {
		return false
	}
	if aa24Identity.AA24Transaction != aa24Other.AA24Transaction {
		return false
	}
	if aa24Identity.AA24OwnerToken != aa24Other.AA24OwnerToken {
		return false
	}
	if aa24Identity.AA24OwnerExpiry != aa24Other.AA24OwnerExpiry {
		return false
	}
	return aa24Identity.AA24RequestDeadline == aa24Other.AA24RequestDeadline
}

// aa24Live uses exclusive deadlines: expiry equality is expired.
func (aa24Identity AA24CommandIdentity) aa24Live(aa24Now AA23Tick) bool {
	return aa24Now < aa24Identity.AA24OwnerExpiry && aa24Now < aa24Identity.AA24Ref.AA24PlanExpiry &&
		aa24Now < aa24Identity.AA24RequestDeadline
}

func (aa24Record *AA24Record) aa24ApplyRef(aa24Ref AA24PlanRef) {
	aa24Record.AA24Subscriber = aa24Ref.AA24Key.AA23Subscriber
	aa24Record.AA24Target = aa24Ref.AA24Key.AA23Address
	aa24Record.AA24Epoch = aa24Ref.AA24Key.AA23Epoch
	aa24Record.AA24Worker = aa24Ref.AA24Worker
	aa24Record.AA24Stream = aa24Ref.AA24Stream
	aa24Record.AA24Plan = aa24Ref.AA24Plan
	aa24Record.AA24PlanExpiry = aa24Ref.AA24PlanExpiry
}

func (aa24Record *AA24Record) aa24ApplyIdentity(aa24Identity AA24CommandIdentity) {
	aa24Record.aa24ApplyRef(aa24Identity.AA24Ref)
	aa24Record.AA24Transaction = aa24Identity.AA24Transaction
	aa24Record.AA24OwnerToken = aa24Identity.AA24OwnerToken
	aa24Record.AA24OwnerExpiry = aa24Identity.AA24OwnerExpiry
	aa24Record.AA24RequestDeadline = aa24Identity.AA24RequestDeadline
}

func (aa24Record *AA24Record) aa24Ref() AA24PlanRef {
	return AA24PlanRef{
		AA24Key: AA23RegistrationKey{
			AA23Subscriber: aa24Record.AA24Subscriber,
			AA23Address:    aa24Record.AA24Target,
			AA23Epoch:      aa24Record.AA24Epoch,
		},
		AA24Worker:     aa24Record.AA24Worker,
		AA24Stream:     aa24Record.AA24Stream,
		AA24Plan:       aa24Record.AA24Plan,
		AA24PlanExpiry: aa24Record.AA24PlanExpiry,
	}
}

func (aa24Record *AA24Record) aa24Identity() AA24CommandIdentity {
	return AA24CommandIdentity{
		AA24Ref:             aa24Record.aa24Ref(),
		AA24Transaction:     aa24Record.AA24Transaction,
		AA24OwnerToken:      aa24Record.AA24OwnerToken,
		AA24OwnerExpiry:     aa24Record.AA24OwnerExpiry,
		AA24RequestDeadline: aa24Record.AA24RequestDeadline,
	}
}

func aa24KindOnLane(aa24Kind AA24RecordKind, aa24Lane AA24LaneKind) bool {
	switch aa24Lane {
	case AA24LaneHostControl:
		return aa24Kind == AA24RecordPublish || aa24Kind == AA24RecordRevoke || aa24Kind == AA24RecordStop
	case AA24LaneWorkerControl:
		return aa24Kind == AA24RecordAcknowledge || aa24Kind == AA24RecordGap
	case AA24LaneHostData:
		return aa24Kind == AA24RecordCommand
	case AA24LaneWorkerData:
		return aa24Kind == AA24RecordRaw || aa24Kind == AA24RecordCompletion
	}
	return false
}

// aa24Shape refuses a record whose fields do not match its kind. Stream-wide and
// unselected records must keep unused identity fields at zero.
func (aa24Record *AA24Record) aa24Shape(aa24Lane AA24LaneKind, aa24Layout *AA24Layout) bool {
	if !aa24KindOnLane(aa24Record.AA24Kind, aa24Lane) || aa24Record.AA24Flags != 0 ||
		aa24Record.AA24Worker == 0 || aa24Record.AA24Stream == 0 ||
		aa24Record.AA24Status > AA24StatusRetired || aa24Record.AA24Direction > AA24DirectionEcho ||
		aa24Record.AA24Phase > aa24MaxPhase {
		return false
	}
	aa24RegistrationFull := aa24Record.AA24Subscriber != 0 && aa23IsTargetAddress(aa24Record.AA24Target) && aa24Record.AA24Epoch != 0
	aa24RegistrationZero := aa24Record.AA24Subscriber == 0 && aa24Record.AA24Target == 0 && aa24Record.AA24Epoch == 0
	aa24PlanFull := aa24Record.AA24Plan != 0 && aa24Record.AA24PlanExpiry != 0
	aa24PlanZero := aa24Record.AA24Plan == 0 && aa24Record.AA24PlanExpiry == 0
	aa24OwnerFull := aa24Record.AA24Transaction != 0 && aa24Record.AA24OwnerToken != 0 &&
		aa24Record.AA24OwnerExpiry != 0 && aa24Record.AA24RequestDeadline != 0
	aa24TokenZero := aa24Record.AA24OwnerToken == 0 && aa24Record.AA24OwnerExpiry == 0 && aa24Record.AA24RequestDeadline == 0
	aa24OwnerZero := aa24Record.AA24Transaction == 0 && aa24TokenZero
	aa24CaptureFull := aa24Record.AA24CaptureSequence != 0 && aa24Record.AA24CaptureTime != 0
	aa24CaptureZero := aa24Record.AA24CaptureSequence == 0 && aa24Record.AA24CaptureTime == 0
	aa24ArenaZero := aa24Record.AA24ArenaOffset == 0 && aa24Record.AA24PlanBytes == 0
	aa24ControlPlain := aa24Record.AA24Direction == AA24DirectionControl && aa24Record.AA24RawOctet == 0
	aa24StatusZero := aa24Record.AA24Status == AA24StatusPending
	aa24NoPhase := aa24Record.AA24Phase == 0

	switch aa24Record.AA24Kind {
	case AA24RecordPublish:
		return aa24RegistrationFull && aa24PlanFull && aa24OwnerZero && aa24CaptureZero && aa24ControlPlain &&
			aa24StatusZero && aa24NoPhase && aa24Record.aa24ArenaInBounds(aa24Layout)
	case AA24RecordRevoke:
		return aa24RegistrationFull && aa24PlanFull && aa24OwnerZero && aa24CaptureZero && aa24ArenaZero &&
			aa24ControlPlain && aa24StatusZero && aa24NoPhase
	case AA24RecordAcknowledge:
		return aa24RegistrationFull && aa24PlanFull && aa24OwnerZero && aa24CaptureFull && aa24ArenaZero &&
			aa24ControlPlain && aa24NoPhase && aa24Record.AA24Status == AA24StatusRetired
	case AA24RecordGap:
		return aa24RegistrationZero && aa24PlanZero && aa24OwnerZero && aa24CaptureFull && aa24ArenaZero &&
			aa24ControlPlain && aa24StatusZero && aa24NoPhase
	case AA24RecordStop:
		return aa24RegistrationZero && aa24PlanZero && aa24OwnerZero && aa24CaptureZero && aa24ArenaZero &&
			aa24ControlPlain && aa24StatusZero && aa24NoPhase
	case AA24RecordCommand:
		return aa24RegistrationFull && aa24PlanFull && aa24OwnerFull && aa24CaptureZero && aa24ArenaZero &&
			aa24ControlPlain && aa24StatusZero
	case AA24RecordRaw:
		aa24Selected := aa24RegistrationFull && aa24PlanFull && aa24OwnerFull
		aa24Unselected := aa24RegistrationZero && aa24PlanZero && aa24TokenZero && aa24Record.AA24Transaction != 0
		return (aa24Selected || aa24Unselected) && aa24CaptureFull && aa24ArenaZero && aa24StatusZero &&
			aa24Record.AA24Direction != AA24DirectionControl && len(aa24Record.AA24Payload) == 0
	case AA24RecordCompletion:
		return aa24RegistrationFull && aa24PlanFull && aa24OwnerFull && aa24CaptureFull && aa24ArenaZero &&
			aa24ControlPlain && aa24Record.AA24Status != AA24StatusPending
	}
	return false
}

// aa24ArenaInBounds keeps a plan reference inside the arena, past its build identity.
func (aa24Record *AA24Record) aa24ArenaInBounds(aa24Layout *AA24Layout) bool {
	aa24End, aa24OK := aa24AddChecked(aa24Record.AA24ArenaOffset, uint64(aa24Record.AA24PlanBytes))
	return aa24OK && aa24Record.AA24PlanBytes != 0 &&
		aa24Record.AA24ArenaOffset >= aa24Layout.AA24ArenaOffset+aa24BuildBytes &&
		aa24End <= aa24Layout.AA24ArenaOffset+aa24Layout.AA24ArenaBytes
}

func (aa24Record *AA24Record) aa24Encode(aa24Slot []byte) {
	aa24Put32 := aa24binary.LittleEndian.PutUint32
	aa24Put64 := aa24binary.LittleEndian.PutUint64
	clear(aa24Slot)
	aa24Put32(aa24Slot[0:], uint32(aa24Record.AA24Kind))
	aa24Put32(aa24Slot[4:], uint32(len(aa24Record.AA24Payload)))
	aa24Put64(aa24Slot[8:], uint64(aa24Record.AA24Worker))
	aa24Put64(aa24Slot[16:], uint64(aa24Record.AA24Stream))
	aa24Put64(aa24Slot[24:], uint64(aa24Record.AA24Transaction))
	aa24Put64(aa24Slot[32:], uint64(aa24Record.AA24Plan))
	aa24Put64(aa24Slot[40:], uint64(aa24Record.AA24Subscriber))
	aa24Put64(aa24Slot[48:], uint64(aa24Record.AA24Epoch))
	aa24Put64(aa24Slot[56:], uint64(aa24Record.AA24OwnerToken))
	aa24Put64(aa24Slot[64:], uint64(aa24Record.AA24OwnerExpiry))
	aa24Put64(aa24Slot[72:], aa24Record.AA24CaptureSequence)
	aa24Put64(aa24Slot[80:], uint64(aa24Record.AA24CaptureTime))
	aa24Put64(aa24Slot[88:], uint64(aa24Record.AA24PlanExpiry))
	aa24Put64(aa24Slot[96:], uint64(aa24Record.AA24RequestDeadline))
	aa24Put64(aa24Slot[104:], aa24Record.AA24ArenaOffset)
	aa24Put32(aa24Slot[112:], aa24Record.AA24PlanBytes)
	aa24Put32(aa24Slot[116:], uint32(aa24Record.AA24Status))
	aa24Slot[120] = aa24Record.AA24Target
	aa24Slot[121] = aa24Record.AA24RawOctet
	aa24Slot[122] = byte(aa24Record.AA24Direction)
	aa24Slot[123] = aa24Record.AA24Phase
	aa24Put32(aa24Slot[124:], aa24Record.AA24Flags)
	copy(aa24Slot[aa24EnvelopeBytes:], aa24Record.AA24Payload)
}

func aa24DecodeEnvelope(aa24Slot []byte) AA24Record {
	aa24Get32 := aa24binary.LittleEndian.Uint32
	aa24Get64 := aa24binary.LittleEndian.Uint64
	return AA24Record{
		AA24Kind:            AA24RecordKind(aa24Get32(aa24Slot[0:])),
		AA24Worker:          AA23WorkerID(aa24Get64(aa24Slot[8:])),
		AA24Stream:          AA23StreamGeneration(aa24Get64(aa24Slot[16:])),
		AA24Transaction:     AA23TransactionOrdinal(aa24Get64(aa24Slot[24:])),
		AA24Plan:            AA23PlanGeneration(aa24Get64(aa24Slot[32:])),
		AA24Subscriber:      AA23SubscriberID(aa24Get64(aa24Slot[40:])),
		AA24Epoch:           AA23RegistrationEpoch(aa24Get64(aa24Slot[48:])),
		AA24OwnerToken:      AA24OwnerToken(aa24Get64(aa24Slot[56:])),
		AA24OwnerExpiry:     AA23Tick(aa24Get64(aa24Slot[64:])),
		AA24CaptureSequence: aa24Get64(aa24Slot[72:]),
		AA24CaptureTime:     AA23Tick(aa24Get64(aa24Slot[80:])),
		AA24PlanExpiry:      AA23Tick(aa24Get64(aa24Slot[88:])),
		AA24RequestDeadline: AA23Tick(aa24Get64(aa24Slot[96:])),
		AA24ArenaOffset:     aa24Get64(aa24Slot[104:]),
		AA24PlanBytes:       aa24Get32(aa24Slot[112:]),
		AA24Status:          AA24Status(aa24Get32(aa24Slot[116:])),
		AA24Target:          aa24Slot[120],
		AA24RawOctet:        aa24Slot[121],
		AA24Direction:       AA24Direction(aa24Slot[122]),
		AA24Phase:           aa24Slot[123],
		AA24Flags:           aa24Get32(aa24Slot[124:]),
	}
}

// AA24LaneStats are process-local counters, because the documented layout has no
// shared field for them. HighWater is the largest occupancy this side observed.
type AA24LaneStats struct {
	AA24HighWater uint64
	AA24Enqueued  uint64
	AA24Dequeued  uint64
	AA24Refused   uint64
	AA24Dropped   uint64
}

// aa24Lane is one side's view of an SPSC lane. A side writes exactly one of the
// two indices and treats the other as peer-controlled input to revalidate.
type aa24Lane struct {
	aa24Mem              []byte
	aa24Layout           aa24LaneLayout
	aa24ArenaLayout      *AA24Layout
	aa24PayloadCap       uint32
	aa24IsProducer       bool
	aa24LocalProducer    uint64
	aa24LocalConsumer    uint64
	aa24Stats            AA24LaneStats
	aa24Saturated        bool
	aa24ProgressConsumer uint64
	aa24ProgressTick     AA23Tick
}

func (aa24Lane *aa24Lane) aa24Bump(aa24Counter *uint64) {
	if *aa24Counter == aa24math.MaxUint64 {
		aa24Lane.aa24Saturated = true
		return
	}
	*aa24Counter++
}

func (aa24Lane *aa24Lane) aa24Indices() (uint64, uint64, error) {
	aa24Producer := aa24LoadAcquire(aa24Lane.aa24Mem, aa24Lane.aa24Layout.aa24ProducerOffset)
	aa24Consumer := aa24LoadAcquire(aa24Lane.aa24Mem, aa24Lane.aa24Layout.aa24ConsumerOffset)
	if aa24Consumer > aa24Producer || aa24Producer-aa24Consumer > uint64(aa24Lane.aa24Layout.aa24Capacity) {
		return 0, 0, ErrAA24Index
	}
	if aa24Lane.aa24IsProducer {
		if aa24Producer != aa24Lane.aa24LocalProducer || aa24Consumer < aa24Lane.aa24LocalConsumer {
			return 0, 0, ErrAA24Index
		}
		aa24Lane.aa24LocalConsumer = aa24Consumer
		return aa24Producer, aa24Consumer, nil
	}
	if aa24Consumer != aa24Lane.aa24LocalConsumer || aa24Producer < aa24Lane.aa24LocalProducer {
		return 0, 0, ErrAA24Index
	}
	aa24Lane.aa24LocalProducer = aa24Producer
	return aa24Producer, aa24Consumer, nil
}

func (aa24Lane *aa24Lane) aa24Slot(aa24Index uint64) []byte {
	aa24SlotBytes := uint64(aa24Lane.aa24Layout.aa24SlotBytes)
	aa24Start := aa24Lane.aa24Layout.aa24SlotOffset + (aa24Index%uint64(aa24Lane.aa24Layout.aa24Capacity))*aa24SlotBytes
	return aa24Lane.aa24Mem[aa24Start : aa24Start+aa24SlotBytes]
}

func (aa24Lane *aa24Lane) aa24HasRoom(aa24Reserve uint64) (bool, error) {
	aa24Producer, aa24Consumer, aa24Err := aa24Lane.aa24Indices()
	if aa24Err != nil {
		return false, aa24Err
	}
	return aa24Producer-aa24Consumer+aa24Reserve < uint64(aa24Lane.aa24Layout.aa24Capacity), nil
}

// aa24Write fills a whole owned slot and only then release-publishes the index.
// It never overwrites an unread slot and never waits. aa24Reserve keeps slots free
// for a record that must not be lost.
func (aa24Lane *aa24Lane) aa24Write(aa24Record *AA24Record, aa24Reserve uint64) error {
	if !aa24Lane.aa24IsProducer {
		return ErrAA24Ownership
	}
	if uint64(len(aa24Record.AA24Payload)) > uint64(aa24Lane.aa24PayloadCap) {
		return ErrAA24Oversize
	}
	if !aa24Record.aa24Shape(aa24Lane.aa24Layout.aa24Kind, aa24Lane.aa24ArenaLayout) {
		return ErrAA24Record
	}
	aa24HasRoom, aa24Err := aa24Lane.aa24HasRoom(aa24Reserve)
	if aa24Err != nil {
		return aa24Err
	}
	if !aa24HasRoom {
		aa24Lane.aa24Bump(&aa24Lane.aa24Stats.AA24Refused)
		return ErrAA24Full
	}
	aa24Producer := aa24Lane.aa24LocalProducer
	if aa24Producer == aa24math.MaxUint64 {
		return ErrAA24Counter
	}
	aa24Record.aa24Encode(aa24Lane.aa24Slot(aa24Producer))
	aa24StoreRelease(aa24Lane.aa24Mem, aa24Lane.aa24Layout.aa24ProducerOffset, aa24Producer+1)
	aa24Lane.aa24LocalProducer = aa24Producer + 1
	aa24Lane.aa24Bump(&aa24Lane.aa24Stats.AA24Enqueued)
	if aa24Occupancy := aa24Producer + 1 - aa24Lane.aa24LocalConsumer; aa24Occupancy > aa24Lane.aa24Stats.AA24HighWater {
		aa24Lane.aa24Stats.AA24HighWater = aa24Occupancy
	}
	return nil
}

// aa24Read acquires the producer index, validates a whole detached copy and only
// then releases the slot. A malformed record stays unconsumed.
func (aa24Lane *aa24Lane) aa24Read(aa24Dst []byte) (AA24Record, bool, error) {
	if aa24Lane.aa24IsProducer {
		return AA24Record{}, false, ErrAA24Ownership
	}
	aa24Producer, aa24Consumer, aa24Err := aa24Lane.aa24Indices()
	if aa24Err != nil {
		return AA24Record{}, false, aa24Err
	}
	if aa24Producer == aa24Consumer {
		return AA24Record{}, false, nil
	}
	aa24Slot := aa24Lane.aa24Slot(aa24Consumer)
	aa24PayloadBytes := aa24binary.LittleEndian.Uint32(aa24Slot[4:])
	if aa24PayloadBytes > aa24Lane.aa24Layout.aa24SlotBytes-aa24EnvelopeBytes {
		return AA24Record{}, false, ErrAA24Truncated
	}
	if aa24PayloadBytes > aa24Lane.aa24PayloadCap {
		return AA24Record{}, false, ErrAA24Oversize
	}
	if uint64(aa24PayloadBytes) > uint64(cap(aa24Dst)) {
		return AA24Record{}, false, ErrAA24Buffer
	}
	aa24Payload := aa24Slot[aa24EnvelopeBytes : aa24EnvelopeBytes+aa24PayloadBytes]
	for _, aa24Byte := range aa24Slot[aa24EnvelopeBytes+aa24PayloadBytes:] {
		if aa24Byte != 0 {
			return AA24Record{}, false, ErrAA24Record
		}
	}
	aa24Record := aa24DecodeEnvelope(aa24Slot)
	aa24Record.AA24Payload = append(aa24Dst[:0], aa24Payload...)
	if !aa24Record.aa24Shape(aa24Lane.aa24Layout.aa24Kind, aa24Lane.aa24ArenaLayout) {
		return AA24Record{}, false, ErrAA24Record
	}
	aa24StoreRelease(aa24Lane.aa24Mem, aa24Lane.aa24Layout.aa24ConsumerOffset, aa24Consumer+1)
	aa24Lane.aa24LocalConsumer = aa24Consumer + 1
	aa24Lane.aa24Bump(&aa24Lane.aa24Stats.AA24Dequeued)
	if aa24Occupancy := aa24Producer - aa24Consumer; aa24Occupancy > aa24Lane.aa24Stats.AA24HighWater {
		aa24Lane.aa24Stats.AA24HighWater = aa24Occupancy
	}
	return aa24Record, true, nil
}

// aa24ServiceExpired reports an unread control backlog that has made no progress
// for the whole budget. Reaching the budget exactly counts as expired.
func (aa24Lane *aa24Lane) aa24ServiceExpired(aa24Budget AA23Tick, aa24Now AA23Tick) (bool, error) {
	aa24Producer, aa24Consumer, aa24Err := aa24Lane.aa24Indices()
	if aa24Err != nil {
		return false, aa24Err
	}
	if aa24Producer == aa24Consumer || aa24Consumer != aa24Lane.aa24ProgressConsumer || aa24Lane.aa24ProgressTick == 0 {
		aa24Lane.aa24ProgressConsumer = aa24Consumer
		aa24Lane.aa24ProgressTick = aa24Now
		return false, nil
	}
	return aa24Now-aa24Lane.aa24ProgressTick >= aa24Budget, nil
}

func aa24AttachLanes(aa24Mem []byte, aa24Layout *AA24Layout, aa24Limits AA24Limits, aa24Worker bool) [aa24LaneCount]aa24Lane {
	var aa24Lanes [aa24LaneCount]aa24Lane
	for aa24Index := range aa24Lanes {
		aa24View := aa24Layout.aa24Lanes[aa24Index]
		aa24Cap := aa24View.aa24SlotBytes - aa24EnvelopeBytes
		if aa24Cap > aa24Limits.AA24MaxPayloadBytes {
			aa24Cap = aa24Limits.AA24MaxPayloadBytes
		}
		aa24WorkerProduces := aa24View.aa24Kind == AA24LaneWorkerControl || aa24View.aa24Kind == AA24LaneWorkerData
		aa24Lanes[aa24Index] = aa24Lane{
			aa24Mem:         aa24Mem,
			aa24Layout:      aa24View,
			aa24ArenaLayout: aa24Layout,
			aa24PayloadCap:  aa24Cap,
			aa24IsProducer:  aa24WorkerProduces == aa24Worker,
		}
	}
	return aa24Lanes
}

// AA24FenceReason states why an endpoint closed output decisions.
type AA24FenceReason uint8

const (
	AA24FenceNone AA24FenceReason = iota
	AA24FenceDataFull
	AA24FenceControlSaturated
	AA24FenceControlDeadline
	AA24FenceWorkerGap
	AA24FenceDiscontinuity
	AA24FenceCorruption
	AA24FenceCounter
	AA24FenceClock
	AA24FenceStop
	AA24FenceWorkerTerminated
)

// AA24FenceState is the first cause of a closed stream view. Sequence and capture
// time are the observation it refers to; a local cause without one keeps them zero.
type AA24FenceState struct {
	AA24Reason      AA24FenceReason
	AA24Sequence    uint64
	AA24CaptureTime AA23Tick
}

// AA24Observation is one raw symbol. A zero selection is an unselected observation.
type AA24Observation struct {
	AA24Transaction AA23TransactionOrdinal
	AA24RawOctet    byte
	AA24Direction   AA24Direction
	AA24Phase       uint8
	AA24Selection   AA24CommandIdentity
}

type aa24AdoptedPlan struct {
	aa24Ref     AA24PlanRef
	aa24Revoked bool
}

// AA24Worker is the worker side: sole producer of the worker lanes and sole
// consumer of the host lanes. Once a stream is closed, nothing reopens it; recovery
// is a new validated mapping with an advanced generation.
type AA24Worker struct {
	aa24Expected     AA24Expected
	aa24Lanes        [aa24LaneCount]aa24Lane
	aa24Clock        aa23ClockGuard
	aa24NextSequence uint64
	aa24LastCapture  AA23Tick
	aa24Fence        AA24FenceState
	aa24GapLatched   bool
	aa24GapWritten   bool
	aa24GapIndex     uint64
	aa24GapRecord    AA24Record
	aa24StopObserved bool
	aa24Adopted      []aa24AdoptedPlan
	aa24RefusedRefs  []AA24PlanRef
	aa24RetireRetry  []AA24PlanRef
	aa24Scratch      []byte
	aa24PlanFloor    AA23PlanGeneration
	aa24TokenFloor   AA24OwnerToken
	aa24Active       AA24CommandIdentity
	aa24HasActive    bool
	aa24Lost         uint64
	aa24Refused      uint64
	aa24Detached     bool
}

// AA24AttachWorker validates a fresh mapping before any lane becomes reachable.
func AA24AttachWorker(aa24Mem []byte, aa24Expected AA24Expected) (*AA24Worker, error) {
	aa24Layout, aa24Err := AA24ValidateMapping(aa24Mem, aa24Expected)
	if aa24Err != nil {
		return nil, aa24Err
	}
	return &AA24Worker{
		aa24Expected:     aa24Expected,
		aa24Lanes:        aa24AttachLanes(aa24Mem, &aa24Layout, aa24Expected.AA24Limits, true),
		aa24NextSequence: 1,
		aa24Adopted:      make([]aa24AdoptedPlan, 0, aa24Expected.AA24Limits.AA24MaxPlans),
		aa24RefusedRefs:  make([]AA24PlanRef, 0, aa24Expected.AA24Limits.AA24MaxPlans),
		aa24RetireRetry:  make([]AA24PlanRef, 0, aa24Expected.AA24Limits.AA24MaxPlans),
		aa24Scratch:      make([]byte, 0, aa24Expected.AA24Limits.AA24MaxPayloadBytes),
		aa24PlanFloor:    aa24Expected.AA24PlanFloor,
		aa24TokenFloor:   aa24Expected.AA24TokenFloor,
	}, nil
}

func (aa24Worker *AA24Worker) aa24Lane(aa24Kind AA24LaneKind) *aa24Lane {
	return &aa24Worker.aa24Lanes[aa24Kind-1]
}

// AA24OutputOpen reports whether new output decisions may be taken.
func (aa24Worker *AA24Worker) AA24OutputOpen() bool {
	return !aa24Worker.aa24Detached && aa24Worker.aa24Fence.AA24Reason == AA24FenceNone
}

// AA24Fence returns the first fence cause.
func (aa24Worker *AA24Worker) AA24Fence() AA24FenceState { return aa24Worker.aa24Fence }

// AA24Lost counts observations and completions that could not be queued.
func (aa24Worker *AA24Worker) AA24Lost() uint64 { return aa24Worker.aa24Lost }

// AA24PublishRefused counts publications dropped without adoption.
func (aa24Worker *AA24Worker) AA24PublishRefused() uint64 { return aa24Worker.aa24Refused }

// AA24LaneStats returns this side's counters for one lane.
func (aa24Worker *AA24Worker) AA24LaneStats(aa24Kind AA24LaneKind) AA24LaneStats {
	return aa24Worker.aa24Lane(aa24Kind).aa24Stats
}

func (aa24Worker *AA24Worker) aa24Close(aa24Reason AA24FenceReason, aa24Sequence uint64, aa24Tick AA23Tick) {
	if aa24Worker.aa24Fence.AA24Reason == AA24FenceNone {
		aa24Worker.aa24Fence = AA24FenceState{AA24Reason: aa24Reason, AA24Sequence: aa24Sequence, AA24CaptureTime: aa24Tick}
	}
	aa24Worker.aa24HasActive = false
}

// aa24RaiseGap closes output decisions before it touches any lane, then latches the one
// gap of this stream. The latch survives a failed write, so a gap is never lost.
func (aa24Worker *AA24Worker) aa24RaiseGap(aa24Reason AA24FenceReason, aa24Sequence uint64, aa24Tick AA23Tick) {
	aa24Worker.aa24Close(aa24Reason, aa24Sequence, aa24Tick)
	if !aa24Worker.aa24GapLatched {
		aa24Worker.aa24GapLatched = true
		aa24Worker.aa24GapRecord = AA24Record{
			AA24Kind:            AA24RecordGap,
			AA24Worker:          aa24Worker.aa24Expected.AA24Binding.AA24Worker,
			AA24Stream:          aa24Worker.aa24Expected.AA24Binding.AA24Stream,
			AA24CaptureSequence: aa24Sequence,
			AA24CaptureTime:     aa24Tick,
		}
	}
	aa24Worker.aa24FlushGap()
}

func (aa24Worker *AA24Worker) aa24FlushGap() {
	if !aa24Worker.aa24GapLatched || aa24Worker.aa24GapWritten {
		return
	}
	aa24Control := aa24Worker.aa24Lane(AA24LaneWorkerControl)
	aa24Index := aa24Control.aa24LocalProducer
	if aa24Control.aa24Write(&aa24Worker.aa24GapRecord, 0) == nil {
		aa24Worker.aa24GapWritten = true
		aa24Worker.aa24GapIndex = aa24Index
	}
}

// aa24GapSequence takes an ordinal for a gap with no lost observation of its own.
// The final ordinal stays available, because only a gap may use it.
func (aa24Worker *AA24Worker) aa24GapSequence() uint64 {
	aa24Sequence := aa24Worker.aa24NextSequence
	if aa24Sequence != aa24math.MaxUint64 {
		aa24Worker.aa24NextSequence = aa24Sequence + 1
	}
	return aa24Sequence
}

func (aa24Worker *AA24Worker) aa24TrustedTick() AA23Tick {
	if aa24Worker.aa24Clock.aa23Last != 0 {
		return aa24Worker.aa24Clock.aa23Last
	}
	return 1
}

// aa24RaiseLocalGap handles a cause that lost no observation of its own.
func (aa24Worker *AA24Worker) aa24RaiseLocalGap(aa24Reason AA24FenceReason) {
	if aa24Worker.aa24GapLatched {
		aa24Worker.aa24FlushGap()
		return
	}
	aa24Tick := aa24Worker.aa24TrustedTick()
	if aa24Worker.aa24LastCapture > aa24Tick {
		aa24Tick = aa24Worker.aa24LastCapture
	}
	aa24Worker.aa24RaiseGap(aa24Reason, aa24Worker.aa24GapSequence(), aa24Tick)
}

// aa24AllocateSequence refuses to wrap and keeps the final ordinal for the gap.
func (aa24Worker *AA24Worker) aa24AllocateSequence(aa24Tick AA23Tick) (uint64, error) {
	if aa24Worker.aa24NextSequence == aa24math.MaxUint64 {
		aa24Worker.aa24RaiseGap(AA24FenceCounter, aa24math.MaxUint64, aa24Tick)
		return 0, ErrAA24Counter
	}
	aa24Sequence := aa24Worker.aa24NextSequence
	aa24Worker.aa24NextSequence = aa24Sequence + 1
	return aa24Sequence, nil
}

// aa24Tick observes the clock and retries a latched gap. Tick zero is refused
// because zero means an unused time field in the envelope.
func (aa24Worker *AA24Worker) aa24Tick(aa24Now AA23ClockReading) error {
	if aa24Worker.aa24Detached {
		return ErrAA24Detached
	}
	if aa24Err := aa24Worker.aa24Clock.aa23Observe(aa24Now); aa24Err != nil || aa24Now.AA23Tick == 0 {
		aa24Worker.aa24RaiseLocalGap(AA24FenceClock)
		return ErrAA24Clock
	}
	aa24Worker.aa24FlushGap()
	return nil
}

// aa24Service runs before every output decision. It drains the bounded control
// lane first, so a revocation or stop never waits behind any data backlog.
func (aa24Worker *AA24Worker) aa24Service(aa24Now AA23ClockReading) error {
	if aa24Err := aa24Worker.aa24Tick(aa24Now); aa24Err != nil {
		if aa24Err == ErrAA24Clock {
			// The latched clock fence still lets a stop through, so the host can
			// acknowledge the gap; nothing here makes an output decision.
			aa24Worker.aa24DrainControl(aa24Now.AA23Tick, true)
		}
		return aa24Err
	}
	aa24Worker.aa24DrainControl(aa24Now.AA23Tick, false)
	for aa24Index := range aa24Worker.aa24Lanes {
		if aa24Worker.aa24Lanes[aa24Index].aa24Saturated {
			aa24Worker.aa24RaiseLocalGap(AA24FenceCounter)
		}
	}
	aa24Expired, aa24Err := aa24Worker.aa24Lane(AA24LaneWorkerControl).aa24ServiceExpired(aa24Worker.aa24Expected.AA24Limits.AA24ControlServiceTicks, aa24Now.AA23Tick)
	if aa24Err != nil {
		aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
	} else if aa24Expired {
		aa24Worker.aa24RaiseLocalGap(AA24FenceControlDeadline)
	}
	aa24Worker.aa24RetryRetired(aa24Now.AA23Tick)
	return nil
}

// aa24DrainControl reads the bounded host control lane. A fenced drain consumes
// publications without adopting them.
func (aa24Worker *AA24Worker) aa24DrainControl(aa24Now AA23Tick, aa24Fenced bool) {
	aa24Control := aa24Worker.aa24Lane(AA24LaneHostControl)
	for aa24Budget := aa24Control.aa24Layout.aa24Capacity; aa24Budget > 0; aa24Budget-- {
		aa24Record, aa24OK, aa24Err := aa24Control.aa24Read(aa24Worker.aa24Scratch)
		if aa24Err != nil {
			aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
			return
		}
		if !aa24OK {
			break
		}
		aa24Worker.aa24HandleControl(&aa24Record, aa24Now, aa24Fenced)
	}
}

// aa24RetryRetired posts the retirement of refused publications whose first
// acknowledgement found no room. A reference stays until its post succeeds.
func (aa24Worker *AA24Worker) aa24RetryRetired(aa24Now AA23Tick) {
	for aa24Index := 0; aa24Index < len(aa24Worker.aa24RetireRetry); {
		aa24Ref := aa24Worker.aa24RetireRetry[aa24Index]
		aa24HasRoom, aa24Err := aa24Worker.aa24Lane(AA24LaneWorkerControl).aa24HasRoom(aa24ReservedSlots)
		if aa24Err != nil || !aa24HasRoom || aa24Worker.aa24PostRetired(aa24Ref, aa24Now) != nil {
			return
		}
		aa24Worker.aa24RetireRetry = append(aa24Worker.aa24RetireRetry[:aa24Index], aa24Worker.aa24RetireRetry[aa24Index+1:]...)
		aa24Worker.aa24DropRefused(aa24Ref)
	}
}

func (aa24Worker *AA24Worker) aa24DropRefused(aa24Ref AA24PlanRef) {
	for aa24Index := range aa24Worker.aa24RefusedRefs {
		if aa24Worker.aa24RefusedRefs[aa24Index].aa24Matches(aa24Ref) {
			aa24Worker.aa24RefusedRefs = append(aa24Worker.aa24RefusedRefs[:aa24Index], aa24Worker.aa24RefusedRefs[aa24Index+1:]...)
			return
		}
	}
}

func (aa24Worker *AA24Worker) aa24IdentityMatches(aa24Record *AA24Record) bool {
	return aa24Record.AA24Worker == aa24Worker.aa24Expected.AA24Binding.AA24Worker &&
		aa24Record.AA24Stream == aa24Worker.aa24Expected.AA24Binding.AA24Stream
}

func (aa24Worker *AA24Worker) aa24HandleControl(aa24Record *AA24Record, aa24Now AA23Tick, aa24Fenced bool) {
	aa24Control := aa24Worker.aa24Lane(AA24LaneHostControl)
	if !aa24Worker.aa24IdentityMatches(aa24Record) {
		aa24Control.aa24Bump(&aa24Control.aa24Stats.AA24Dropped)
		return
	}
	switch aa24Record.AA24Kind {
	case AA24RecordStop:
		aa24Worker.aa24StopObserved = true
		aa24Worker.aa24Close(AA24FenceStop, 0, 0)
	case AA24RecordRevoke:
		aa24Ref := aa24Record.aa24Ref()
		for aa24Index := range aa24Worker.aa24Adopted {
			if aa24Worker.aa24Adopted[aa24Index].aa24Ref.aa24Matches(aa24Ref) {
				aa24Worker.aa24Adopted[aa24Index].aa24Revoked = true
				if aa24Worker.aa24HasActive && aa24Worker.aa24Active.AA24Ref.aa24Matches(aa24Ref) {
					aa24Worker.aa24HasActive = false
				}
				return
			}
		}
		// A refused publication was never adopted, so nothing here refers to it and
		// its exact retirement is acknowledged at once.
		for aa24Index := range aa24Worker.aa24RefusedRefs {
			if aa24Worker.aa24RefusedRefs[aa24Index].aa24Matches(aa24Ref) {
				// The reference leaves only after the acknowledgement is written;
				// until then the service pass retries it.
				if aa24Worker.aa24PostRetired(aa24Ref, aa24Now) == nil {
					aa24Worker.aa24DropRefused(aa24Ref)
				} else if len(aa24Worker.aa24RetireRetry) < cap(aa24Worker.aa24RetireRetry) {
					aa24Worker.aa24RetireRetry = append(aa24Worker.aa24RetireRetry, aa24Ref)
				}
				return
			}
		}
		aa24Control.aa24Bump(&aa24Control.aa24Stats.AA24Dropped)
	case AA24RecordPublish:
		if aa24Fenced {
			aa24Control.aa24Bump(&aa24Control.aa24Stats.AA24Dropped)
			return
		}
		if aa24Record.AA24Plan <= aa24Worker.aa24PlanFloor || aa24Now >= aa24Record.AA24PlanExpiry ||
			len(aa24Worker.aa24Adopted) == cap(aa24Worker.aa24Adopted) {
			aa24Worker.aa24Refused++
			if len(aa24Worker.aa24RefusedRefs) < cap(aa24Worker.aa24RefusedRefs) {
				aa24Worker.aa24RefusedRefs = append(aa24Worker.aa24RefusedRefs, aa24Record.aa24Ref())
			} else {
				aa24Worker.aa24RaiseLocalGap(AA24FenceControlSaturated)
			}
			return
		}
		aa24Worker.aa24PlanFloor = aa24Record.AA24Plan
		aa24Worker.aa24Adopted = append(aa24Worker.aa24Adopted, aa24AdoptedPlan{aa24Ref: aa24Record.aa24Ref()})
	}
}

// AA24PollControl services the control lane without touching any data lane.
func (aa24Worker *AA24Worker) AA24PollControl(aa24Now AA23ClockReading) error {
	return aa24Worker.aa24Service(aa24Now)
}

// AA24PendingRevocations appends plans the worker fenced but has not acknowledged.
func (aa24Worker *AA24Worker) AA24PendingRevocations(aa24Dst []AA24PlanRef) []AA24PlanRef {
	for aa24Index := range aa24Worker.aa24Adopted {
		if aa24Worker.aa24Adopted[aa24Index].aa24Revoked {
			aa24Dst = append(aa24Dst, aa24Worker.aa24Adopted[aa24Index].aa24Ref)
		}
	}
	return aa24Dst
}

// AA24ObserveRaw records one symbol at its observation time and never waits. A full
// data lane drops the symbol and raises a gap on the separate control lane, using
// the lost symbol's own ordinal and capture time.
func (aa24Worker *AA24Worker) AA24ObserveRaw(aa24Observation AA24Observation, aa24Now AA23ClockReading) (uint64, error) {
	if aa24Err := aa24Worker.aa24Tick(aa24Now); aa24Err != nil {
		if aa24Err != ErrAA24Detached {
			aa24Worker.aa24CountLost()
		}
		return 0, aa24Err
	}
	if !aa24Worker.AA24OutputOpen() {
		aa24Worker.aa24CountLost()
		return 0, ErrAA24OutputFenced
	}
	aa24Record := AA24Record{
		AA24Kind:      AA24RecordRaw,
		AA24RawOctet:  aa24Observation.AA24RawOctet,
		AA24Direction: aa24Observation.AA24Direction,
		AA24Phase:     aa24Observation.AA24Phase,
	}
	aa24Record.aa24ApplyIdentity(aa24Observation.AA24Selection)
	aa24Record.AA24Worker = aa24Worker.aa24Expected.AA24Binding.AA24Worker
	aa24Record.AA24Stream = aa24Worker.aa24Expected.AA24Binding.AA24Stream
	aa24Record.AA24Transaction = aa24Observation.AA24Transaction
	aa24Sequence, aa24Err := aa24Worker.aa24AllocateSequence(aa24Now.AA23Tick)
	if aa24Err != nil {
		aa24Worker.aa24CountLost()
		return 0, aa24Err
	}
	aa24Record.AA24CaptureSequence = aa24Sequence
	aa24Record.AA24CaptureTime = aa24Now.AA23Tick
	aa24Worker.aa24LastCapture = aa24Now.AA23Tick
	return aa24Sequence, aa24Worker.aa24WriteData(&aa24Record)
}

func (aa24Worker *AA24Worker) aa24CountLost() {
	if aa24Worker.aa24Lost == aa24math.MaxUint64 {
		aa24Worker.aa24RaiseLocalGap(AA24FenceCounter)
		return
	}
	aa24Worker.aa24Lost++
}

func (aa24Worker *AA24Worker) aa24WriteData(aa24Record *AA24Record) error {
	aa24Err := aa24Worker.aa24Lane(AA24LaneWorkerData).aa24Write(aa24Record, 0)
	switch aa24Err {
	case nil:
		return nil
	case ErrAA24Full:
		aa24Worker.aa24RaiseGap(AA24FenceDataFull, aa24Record.AA24CaptureSequence, aa24Record.AA24CaptureTime)
		aa24Worker.aa24CountLost()
		return ErrAA24Gap
	case ErrAA24Record:
		aa24Worker.aa24RaiseGap(AA24FenceCorruption, aa24Record.AA24CaptureSequence, aa24Record.AA24CaptureTime)
		return ErrAA24Record
	default:
		aa24Reason := AA24FenceCorruption
		if aa24Err == ErrAA24Counter {
			aa24Reason = AA24FenceCounter
		}
		aa24Worker.aa24RaiseGap(aa24Reason, aa24Record.AA24CaptureSequence, aa24Record.AA24CaptureTime)
		aa24Worker.aa24CountLost()
		return aa24Err
	}
}

// AA24NextCommand drains control first, then admits at most one command. A dropped
// stale command consumes its slot and is never replayed.
func (aa24Worker *AA24Worker) AA24NextCommand(aa24Dst []byte, aa24Now AA23ClockReading) (AA24Delivery, bool, error) {
	if aa24Err := aa24Worker.aa24Service(aa24Now); aa24Err != nil {
		return AA24Delivery{}, false, aa24Err
	}
	if !aa24Worker.AA24OutputOpen() {
		return AA24Delivery{}, false, ErrAA24OutputFenced
	}
	if aa24Worker.aa24HasActive {
		return AA24Delivery{}, false, nil
	}
	aa24Data := aa24Worker.aa24Lane(AA24LaneHostData)
	aa24Record, aa24OK, aa24Err := aa24Data.aa24Read(aa24Dst)
	if aa24Err == ErrAA24Buffer {
		return AA24Delivery{}, false, aa24Err
	}
	if aa24Err != nil {
		aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
		return AA24Delivery{}, false, aa24Err
	}
	if !aa24OK {
		return AA24Delivery{}, false, nil
	}
	if !aa24Worker.aa24IdentityMatches(&aa24Record) || aa24Record.AA24OwnerToken <= aa24Worker.aa24TokenFloor {
		aa24Data.aa24Bump(&aa24Data.aa24Stats.AA24Dropped)
		return AA24Delivery{}, false, ErrAA24Stale
	}
	aa24Worker.aa24TokenFloor = aa24Record.AA24OwnerToken
	aa24Identity := aa24Record.aa24Identity()
	if aa24Err := aa24Worker.aa24CheckPlan(aa24Identity.AA24Ref); aa24Err != nil {
		aa24Data.aa24Bump(&aa24Data.aa24Stats.AA24Dropped)
		return AA24Delivery{}, false, aa24Err
	}
	if !aa24Identity.aa24Live(aa24Now.AA23Tick) {
		aa24Data.aa24Bump(&aa24Data.aa24Stats.AA24Dropped)
		return AA24Delivery{}, false, ErrAA24Expired
	}
	aa24Worker.aa24Active = aa24Identity
	aa24Worker.aa24HasActive = true
	return AA24Delivery{AA24Record: aa24Record, AA24DequeueTime: aa24Now.AA23Tick}, true, nil
}

func (aa24Worker *AA24Worker) aa24CheckPlan(aa24Ref AA24PlanRef) error {
	for aa24Index := range aa24Worker.aa24Adopted {
		if aa24Worker.aa24Adopted[aa24Index].aa24Ref.aa24Matches(aa24Ref) {
			if aa24Worker.aa24Adopted[aa24Index].aa24Revoked {
				return ErrAA24Revoked
			}
			return nil
		}
	}
	return ErrAA24Stale
}

// AA24AdmitOutput rechecks control, every exact key and every deadline. It must run
// immediately before each output symbol, and an expired command stops being active.
func (aa24Worker *AA24Worker) AA24AdmitOutput(aa24Identity AA24CommandIdentity, aa24Now AA23ClockReading) error {
	if aa24Err := aa24Worker.aa24Service(aa24Now); aa24Err != nil {
		return aa24Err
	}
	if !aa24Worker.AA24OutputOpen() {
		return ErrAA24OutputFenced
	}
	if aa24Err := aa24Worker.aa24CheckPlan(aa24Identity.AA24Ref); aa24Err != nil {
		return aa24Err
	}
	if !aa24Worker.aa24HasActive || !aa24Worker.aa24Active.aa24Matches(aa24Identity) {
		return ErrAA24Mismatch
	}
	if !aa24Identity.aa24Live(aa24Now.AA23Tick) {
		aa24Worker.aa24HasActive = false
		return ErrAA24Expired
	}
	return nil
}

// AA24Complete posts the result of the active command through the data lane.
func (aa24Worker *AA24Worker) AA24Complete(aa24Identity AA24CommandIdentity, aa24Status AA24Status, aa24Now AA23ClockReading) error {
	if aa24Err := aa24Worker.aa24Tick(aa24Now); aa24Err != nil {
		return aa24Err
	}
	if !aa24Worker.aa24HasActive || !aa24Worker.aa24Active.aa24Matches(aa24Identity) {
		return ErrAA24Mismatch
	}
	aa24Sequence, aa24Err := aa24Worker.aa24AllocateSequence(aa24Now.AA23Tick)
	if aa24Err != nil {
		return aa24Err
	}
	aa24Record := AA24Record{AA24Kind: AA24RecordCompletion, AA24Status: aa24Status}
	aa24Record.aa24ApplyIdentity(aa24Identity)
	aa24Record.AA24CaptureSequence = aa24Sequence
	aa24Record.AA24CaptureTime = aa24Now.AA23Tick
	aa24Worker.aa24LastCapture = aa24Now.AA23Tick
	aa24Worker.aa24HasActive = false
	return aa24Worker.aa24WriteData(&aa24Record)
}

// AA24AcknowledgeRevocation states that no reader or deferred output still refers
// to the plan. The caller invokes it only after that holds. It may run on a fenced
// stream, and it never takes the slot reserved for the gap: without room it fences.
func (aa24Worker *AA24Worker) AA24AcknowledgeRevocation(aa24Ref AA24PlanRef, aa24Now AA23ClockReading) error {
	if aa24Err := aa24Worker.aa24Service(aa24Now); aa24Err != nil {
		return aa24Err
	}
	aa24Found := -1
	for aa24Index := range aa24Worker.aa24Adopted {
		if aa24Worker.aa24Adopted[aa24Index].aa24Revoked && aa24Worker.aa24Adopted[aa24Index].aa24Ref.aa24Matches(aa24Ref) {
			aa24Found = aa24Index
			break
		}
	}
	if aa24Found < 0 {
		return ErrAA24Stale
	}
	if aa24Err := aa24Worker.aa24PostRetired(aa24Ref, aa24Now.AA23Tick); aa24Err != nil {
		return aa24Err
	}
	aa24Worker.aa24Adopted = append(aa24Worker.aa24Adopted[:aa24Found], aa24Worker.aa24Adopted[aa24Found+1:]...)
	return nil
}

// aa24PostRetired writes the exact retirement acknowledgement on the worker control
// lane without taking the slot reserved for the gap.
func (aa24Worker *AA24Worker) aa24PostRetired(aa24Ref AA24PlanRef, aa24Now AA23Tick) error {
	aa24Control := aa24Worker.aa24Lane(AA24LaneWorkerControl)
	aa24HasRoom, aa24Err := aa24Control.aa24HasRoom(aa24ReservedSlots)
	if aa24Err != nil {
		aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
		return aa24Err
	}
	if !aa24HasRoom {
		aa24Worker.aa24RaiseLocalGap(AA24FenceControlSaturated)
		return ErrAA24ControlFull
	}
	aa24Sequence, aa24Err := aa24Worker.aa24AllocateSequence(aa24Now)
	if aa24Err != nil {
		return aa24Err
	}
	aa24Record := AA24Record{AA24Kind: AA24RecordAcknowledge, AA24Status: AA24StatusRetired}
	aa24Record.aa24ApplyRef(aa24Ref)
	aa24Record.AA24CaptureSequence = aa24Sequence
	aa24Record.AA24CaptureTime = aa24Now
	if aa24Err := aa24Control.aa24Write(&aa24Record, aa24ReservedSlots); aa24Err != nil {
		aa24Worker.aa24RaiseLocalGap(AA24FenceCorruption)
		return aa24Err
	}
	aa24Worker.aa24LastCapture = aa24Now
	return nil
}

// aa24FenceAcknowledged reports whether the exact stop was observed and any raised
// gap was consumed by the host. Resynchronization and teardown share it.
func (aa24Worker *AA24Worker) aa24FenceAcknowledged() error {
	if !aa24Worker.aa24StopObserved {
		return ErrAA24NotAcknowledged
	}
	if aa24Worker.aa24GapLatched {
		aa24Control := aa24Worker.aa24Lane(AA24LaneWorkerControl)
		if !aa24Worker.aa24GapWritten {
			return ErrAA24NotAcknowledged
		}
		_, aa24Consumer, aa24Err := aa24Control.aa24Indices()
		if aa24Err != nil || aa24Consumer <= aa24Worker.aa24GapIndex {
			return ErrAA24NotAcknowledged
		}
	}
	return nil
}

// AA24ResyncExpected names the successor mapping once the host has acknowledged the
// fence: the exact stop was observed and any raised gap was consumed. The host is
// authoritative for the identity floors, since it may have assigned identities the
// worker never drained; the successor carries the higher of each worker floor and
// the supplied host floor, so recovery cannot reuse an assigned identity.
func (aa24Worker *AA24Worker) AA24ResyncExpected(aa24Next AA23StreamGeneration, aa24HostPlanFloor AA23PlanGeneration, aa24HostTokenFloor AA24OwnerToken) (AA24Expected, error) {
	if aa24Worker.aa24Detached {
		return AA24Expected{}, ErrAA24Detached
	}
	if aa24Err := aa24Worker.aa24FenceAcknowledged(); aa24Err != nil {
		return AA24Expected{}, aa24Err
	}
	if aa24Next <= aa24Worker.aa24Expected.AA24Binding.AA24Stream {
		return AA24Expected{}, ErrAA24Stale
	}
	aa24Successor := aa24Worker.aa24Expected
	aa24Successor.AA24Binding.AA24Stream = aa24Next
	aa24Successor.AA24PlanFloor = aa24Worker.aa24PlanFloor
	if aa24HostPlanFloor > aa24Successor.AA24PlanFloor {
		aa24Successor.AA24PlanFloor = aa24HostPlanFloor
	}
	aa24Successor.AA24TokenFloor = aa24Worker.aa24TokenFloor
	if aa24HostTokenFloor > aa24Successor.AA24TokenFloor {
		aa24Successor.AA24TokenFloor = aa24HostTokenFloor
	}
	return aa24Successor, nil
}

// AA24Teardown releases the mapping after output decisions closed. It never drains a lane,
// so a saturated data lane cannot hold shutdown.
func (aa24Worker *AA24Worker) AA24Teardown() error {
	if aa24Worker.aa24Detached {
		return nil
	}
	if aa24Worker.AA24OutputOpen() || aa24Worker.aa24FenceAcknowledged() != nil {
		return ErrAA24NotAcknowledged
	}
	aa24Worker.aa24Detached = true
	aa24Worker.aa24Lanes = [aa24LaneCount]aa24Lane{}
	aa24Worker.aa24Adopted = nil
	aa24Worker.aa24RefusedRefs = nil
	aa24Worker.aa24RetireRetry = nil
	return nil
}

type aa24HostPlan struct {
	aa24Ref      AA24PlanRef
	aa24Revoking bool
}

// AA24Host is the host side: the one serialized dispatcher that produces the host
// lanes and consumes the worker lanes. A publish or revoke enqueue is never treated
// as worker acknowledgement, and a fenced plan stays listed until its exact
// acknowledgement. Reclamation proof stays with the plan pool.
type AA24Host struct {
	aa24Expected        AA24Expected
	aa24Lanes           [aa24LaneCount]aa24Lane
	aa24Clock           aa23ClockGuard
	aa24Fence           AA24FenceState
	aa24StopSent        bool
	aa24StopIndex       uint64
	aa24Terminated      bool
	aa24Plans           []aa24HostPlan
	aa24PlanFloor       AA23PlanGeneration
	aa24TokenFloor      AA24OwnerToken
	aa24Outstanding     []AA24CommandIdentity
	aa24Lingering       []aa24Lingered
	aa24Cancelled       []AA24CommandIdentity
	aa24NextCapture     uint64
	aa24Explained       []uint64
	aa24LastControlTime AA23Tick
	aa24LastDataCapture AA23Tick
	aa24Detached        bool
}

// AA24AttachHost validates a fresh mapping before any lane becomes reachable.
func AA24AttachHost(aa24Mem []byte, aa24Expected AA24Expected) (*AA24Host, error) {
	aa24Layout, aa24Err := AA24ValidateMapping(aa24Mem, aa24Expected)
	if aa24Err != nil {
		return nil, aa24Err
	}
	aa24Limits := aa24Expected.AA24Limits
	aa24Lanes := aa24AttachLanes(aa24Mem, &aa24Layout, aa24Limits, false)
	return &AA24Host{
		aa24Expected:    aa24Expected,
		aa24Lanes:       aa24Lanes,
		aa24Plans:       make([]aa24HostPlan, 0, aa24Limits.AA24MaxPlans),
		aa24PlanFloor:   aa24Expected.AA24PlanFloor,
		aa24TokenFloor:  aa24Expected.AA24TokenFloor,
		aa24Outstanding: make([]AA24CommandIdentity, 0, aa24Limits.AA24MaxOutstanding),
		aa24Lingering:   make([]aa24Lingered, 0, aa24Lanes[AA24LaneWorkerData-1].aa24Layout.aa24Capacity),
		aa24Cancelled:   make([]AA24CommandIdentity, 0, aa24Limits.AA24MaxOutstanding),
		aa24NextCapture: 1,
		aa24Explained:   make([]uint64, 0, aa24Lanes[AA24LaneWorkerControl-1].aa24Layout.aa24Capacity),
	}, nil
}

func (aa24Host *AA24Host) aa24Lane(aa24Kind AA24LaneKind) *aa24Lane {
	return &aa24Host.aa24Lanes[aa24Kind-1]
}

// AA24OutputOpen reports whether new commands and publications may be queued.
func (aa24Host *AA24Host) AA24OutputOpen() bool {
	return !aa24Host.aa24Detached && aa24Host.aa24Fence.AA24Reason == AA24FenceNone
}

// AA24Fence returns the fence cause; a worker gap replaces a local first cause.
func (aa24Host *AA24Host) AA24Fence() AA24FenceState { return aa24Host.aa24Fence }

// AA24LaneStats returns this side's counters for one lane.
func (aa24Host *AA24Host) AA24LaneStats(aa24Kind AA24LaneKind) AA24LaneStats {
	return aa24Host.aa24Lane(aa24Kind).aa24Stats
}

// AA24CancelledCommands appends the commands a fence revoked.
func (aa24Host *AA24Host) AA24CancelledCommands(aa24Dst []AA24CommandIdentity) []AA24CommandIdentity {
	return append(aa24Dst, aa24Host.aa24Cancelled...)
}

// AA24QuarantinedPlans appends plans with no exact acknowledgement yet.
func (aa24Host *AA24Host) AA24QuarantinedPlans(aa24Dst []AA24PlanRef) []AA24PlanRef {
	for aa24Index := range aa24Host.aa24Plans {
		aa24Dst = append(aa24Dst, aa24Host.aa24Plans[aa24Index].aa24Ref)
	}
	return aa24Dst
}

func (aa24Host *AA24Host) aa24Close(aa24Reason AA24FenceReason, aa24Sequence uint64, aa24Tick AA23Tick) {
	if aa24Host.aa24Fence.AA24Reason == AA24FenceNone || aa24Reason == AA24FenceWorkerGap {
		aa24Host.aa24Fence = AA24FenceState{AA24Reason: aa24Reason, AA24Sequence: aa24Sequence, AA24CaptureTime: aa24Tick}
	}
	aa24Host.aa24Cancelled = append(aa24Host.aa24Cancelled, aa24Host.aa24Outstanding...)
	aa24Host.aa24Outstanding = aa24Host.aa24Outstanding[:0]
}

func (aa24Host *AA24Host) aa24Tick(aa24Now AA23ClockReading) error {
	if aa24Host.aa24Detached {
		return ErrAA24Detached
	}
	if aa24Err := aa24Host.aa24Clock.aa23Observe(aa24Now); aa24Err != nil || aa24Now.AA23Tick == 0 {
		aa24Host.aa24Close(AA24FenceClock, 0, 0)
		return ErrAA24Clock
	}
	for aa24Index := range aa24Host.aa24Lanes {
		if aa24Host.aa24Lanes[aa24Index].aa24Saturated {
			aa24Host.aa24Close(AA24FenceCounter, 0, 0)
		}
	}
	aa24Expired, aa24Err := aa24Host.aa24Lane(AA24LaneHostControl).aa24ServiceExpired(aa24Host.aa24Expected.AA24Limits.AA24ControlServiceTicks, aa24Now.AA23Tick)
	if aa24Err != nil {
		aa24Host.aa24Close(AA24FenceCorruption, 0, 0)
	} else if aa24Expired {
		aa24Host.aa24Close(AA24FenceControlDeadline, 0, 0)
	}
	return nil
}

func (aa24Host *AA24Host) aa24FindPlan(aa24Ref AA24PlanRef) int {
	for aa24Index := range aa24Host.aa24Plans {
		if aa24Host.aa24Plans[aa24Index].aa24Ref.aa24Matches(aa24Ref) {
			return aa24Index
		}
	}
	return -1
}

func (aa24Host *AA24Host) aa24RefOnMapping(aa24Ref AA24PlanRef) bool {
	return aa24Ref.AA24Worker == aa24Host.aa24Expected.AA24Binding.AA24Worker &&
		aa24Ref.AA24Stream == aa24Host.aa24Expected.AA24Binding.AA24Stream
}

// aa24WriteControl keeps the reserved slot free and fences on saturation. It never
// waits: the worker's own deadlines stay the authority over its output.
func (aa24Host *AA24Host) aa24WriteControl(aa24Record *AA24Record) error {
	aa24Err := aa24Host.aa24Lane(AA24LaneHostControl).aa24Write(aa24Record, aa24ReservedSlots)
	switch aa24Err {
	case nil, ErrAA24Record, ErrAA24Oversize:
		return aa24Err
	case ErrAA24Full:
		aa24Host.aa24Close(AA24FenceControlSaturated, 0, 0)
		return ErrAA24ControlFull
	default:
		aa24Host.aa24Close(AA24FenceCorruption, 0, 0)
		return aa24Err
	}
}

// AA24PublishPlan queues a reference to an immutable arena range.
func (aa24Host *AA24Host) AA24PublishPlan(aa24Ref AA24PlanRef, aa24ArenaOffset uint64, aa24PlanBytes uint32, aa24Now AA23ClockReading) error {
	if aa24Err := aa24Host.aa24Tick(aa24Now); aa24Err != nil {
		return aa24Err
	}
	if !aa24Host.AA24OutputOpen() {
		return ErrAA24OutputFenced
	}
	if !aa24Host.aa24RefOnMapping(aa24Ref) || aa24Ref.AA24Plan <= aa24Host.aa24PlanFloor {
		return ErrAA24Stale
	}
	if aa24Now.AA23Tick >= aa24Ref.AA24PlanExpiry {
		return ErrAA24Expired
	}
	if len(aa24Host.aa24Plans) == cap(aa24Host.aa24Plans) {
		return ErrAA24Capacity
	}
	aa24Record := AA24Record{AA24Kind: AA24RecordPublish, AA24ArenaOffset: aa24ArenaOffset, AA24PlanBytes: aa24PlanBytes}
	aa24Record.aa24ApplyRef(aa24Ref)
	if aa24Err := aa24Host.aa24WriteControl(&aa24Record); aa24Err != nil {
		return aa24Err
	}
	aa24Host.aa24PlanFloor = aa24Ref.AA24Plan
	aa24Host.aa24Plans = append(aa24Host.aa24Plans, aa24HostPlan{aa24Ref: aa24Ref})
	return nil
}

// AA24RevokePlan queues a revocation, also on a fenced stream. Saturation fences
// the host and keeps the plan quarantined; the call may be repeated.
func (aa24Host *AA24Host) AA24RevokePlan(aa24Ref AA24PlanRef, aa24Now AA23ClockReading) error {
	if aa24Err := aa24Host.aa24Tick(aa24Now); aa24Err != nil {
		return aa24Err
	}
	aa24Index := aa24Host.aa24FindPlan(aa24Ref)
	if aa24Index < 0 {
		return ErrAA24Stale
	}
	if aa24Host.aa24Plans[aa24Index].aa24Revoking {
		return nil
	}
	aa24Record := AA24Record{AA24Kind: AA24RecordRevoke}
	aa24Record.aa24ApplyRef(aa24Ref)
	if aa24Err := aa24Host.aa24WriteControl(&aa24Record); aa24Err != nil {
		return aa24Err
	}
	aa24Host.aa24Plans[aa24Index].aa24Revoking = true
	return nil
}

// aa24Lingered is a retired identity with the worker data head seen at retirement:
// a completion queued before that head is consumed once the data tail passes it.
type aa24Lingered struct {
	aa24Identity AA24CommandIdentity
	aa24Horizon  uint64
}

// aa24Linger keeps an identity that no longer occupies outstanding capacity, since
// a completion queued before the worker refused or retired it may still be waiting
// in the data lane. A fence keeps the list, so evidence queued earlier stays
// matchable while old data drains. The list is bounded by the data lane capacity.
// A full list first drops entries whose horizon the data tail has passed; it never
// evicts an entry that may still have a queued completion. Without room it reports
// false and the caller keeps the identity outstanding.
func (aa24Host *AA24Host) aa24Linger(aa24Identity AA24CommandIdentity) bool {
	if cap(aa24Host.aa24Lingering) == 0 {
		return false
	}
	aa24Data := aa24Host.aa24Lane(AA24LaneWorkerData)
	if len(aa24Host.aa24Lingering) == cap(aa24Host.aa24Lingering) {
		aa24Kept := aa24Host.aa24Lingering[:0]
		for _, aa24Held := range aa24Host.aa24Lingering {
			if aa24Held.aa24Horizon > aa24Data.aa24LocalConsumer {
				aa24Kept = append(aa24Kept, aa24Held)
			}
		}
		aa24Host.aa24Lingering = aa24Kept
		if len(aa24Host.aa24Lingering) == cap(aa24Host.aa24Lingering) {
			return false
		}
	}
	aa24Horizon := aa24LoadAcquire(aa24Data.aa24Mem, aa24Data.aa24Layout.aa24ProducerOffset)
	aa24Host.aa24Lingering = append(aa24Host.aa24Lingering, aa24Lingered{aa24Identity: aa24Identity, aa24Horizon: aa24Horizon})
	return true
}

// aa24RetireExpired moves outstanding commands whose deadline has passed out of the
// capacity count: the worker refuses them, though an earlier completion may linger.
// An identity with no room to linger stays outstanding.
func (aa24Host *AA24Host) aa24RetireExpired(aa24Now AA23Tick) {
	aa24Kept := aa24Host.aa24Outstanding[:0]
	for _, aa24Identity := range aa24Host.aa24Outstanding {
		if aa24Identity.aa24Live(aa24Now) || !aa24Host.aa24Linger(aa24Identity) {
			aa24Kept = append(aa24Kept, aa24Identity)
		}
	}
	aa24Host.aa24Outstanding = aa24Kept
}

// AA24SendCommand queues one command. A full data lane refuses it without any
// other effect, and control lanes stay usable.
func (aa24Host *AA24Host) AA24SendCommand(aa24Identity AA24CommandIdentity, aa24Payload []byte, aa24Now AA23ClockReading) error {
	if aa24Err := aa24Host.aa24Tick(aa24Now); aa24Err != nil {
		return aa24Err
	}
	if !aa24Host.AA24OutputOpen() {
		return ErrAA24OutputFenced
	}
	aa24Index := aa24Host.aa24FindPlan(aa24Identity.AA24Ref)
	if aa24Index < 0 || aa24Identity.AA24OwnerToken <= aa24Host.aa24TokenFloor {
		return ErrAA24Stale
	}
	if aa24Host.aa24Plans[aa24Index].aa24Revoking {
		return ErrAA24Revoked
	}
	if !aa24Identity.aa24Live(aa24Now.AA23Tick) {
		return ErrAA24Expired
	}
	aa24Host.aa24RetireExpired(aa24Now.AA23Tick)
	if len(aa24Host.aa24Outstanding) == cap(aa24Host.aa24Outstanding) {
		return ErrAA24Capacity
	}
	aa24Record := AA24Record{AA24Kind: AA24RecordCommand, AA24Payload: aa24Payload}
	aa24Record.aa24ApplyIdentity(aa24Identity)
	aa24Data := aa24Host.aa24Lane(AA24LaneHostData)
	if aa24Err := aa24Data.aa24Write(&aa24Record, 0); aa24Err != nil {
		if aa24Err != ErrAA24Full && aa24Err != ErrAA24Record && aa24Err != ErrAA24Oversize {
			aa24Host.aa24Close(AA24FenceCorruption, 0, 0)
		}
		return aa24Err
	}
	aa24Host.aa24TokenFloor = aa24Identity.AA24OwnerToken
	aa24Host.aa24Outstanding = append(aa24Host.aa24Outstanding, aa24Identity)
	return nil
}

// aa24AccountSequence verifies one worker ordinal. Control records may arrive ahead
// of older data, so they stay explained in a bounded list until data catches up.
// Exceeding that list breaches the reorder bound and is a gap.
func (aa24Host *AA24Host) aa24AccountSequence(aa24Sequence uint64, aa24Control bool) bool {
	if aa24Sequence < aa24Host.aa24NextCapture {
		return false
	}
	if aa24Sequence > aa24Host.aa24NextCapture {
		if !aa24Control || len(aa24Host.aa24Explained) == cap(aa24Host.aa24Explained) {
			return false
		}
		aa24Host.aa24Explained = append(aa24Host.aa24Explained, aa24Sequence)
		return true
	}
	if aa24Sequence == aa24math.MaxUint64 {
		return true
	}
	aa24Host.aa24NextCapture = aa24Sequence + 1
	for aa24Advanced := true; aa24Advanced; {
		aa24Advanced = false
		for aa24Index, aa24Held := range aa24Host.aa24Explained {
			if aa24Held == aa24Host.aa24NextCapture && aa24Held != aa24math.MaxUint64 {
				aa24Host.aa24NextCapture++
				aa24Host.aa24Explained = append(aa24Host.aa24Explained[:aa24Index], aa24Host.aa24Explained[aa24Index+1:]...)
				aa24Advanced = true
				break
			}
		}
	}
	return true
}

func (aa24Host *AA24Host) aa24IdentityMatches(aa24Record *AA24Record) bool {
	return aa24Record.AA24Worker == aa24Host.aa24Expected.AA24Binding.AA24Worker &&
		aa24Record.AA24Stream == aa24Host.aa24Expected.AA24Binding.AA24Stream
}

// AA24Poll delivers one worker record with its dequeue time. The data head is
// sampled before control is read, so every older control record of a visible data
// record has been seen and a remaining discontinuity is truly unexplained. Control
// takes effect at once, even while older data waits. A fenced host keeps delivering
// retained evidence without reopening anything.
func (aa24Host *AA24Host) AA24Poll(aa24Dst []byte, aa24Now AA23ClockReading) (AA24Delivery, bool, error) {
	if aa24Err := aa24Host.aa24Tick(aa24Now); aa24Err != nil {
		if aa24Err != ErrAA24Clock {
			return AA24Delivery{}, false, aa24Err
		}
		return aa24Host.aa24PollFencedControl(aa24Dst)
	}
	aa24Data := aa24Host.aa24Lane(AA24LaneWorkerData)
	aa24DataHead := aa24LoadAcquire(aa24Data.aa24Mem, aa24Data.aa24Layout.aa24ProducerOffset)
	aa24Control := aa24Host.aa24Lane(AA24LaneWorkerControl)
	// While the early-control list is full and older data is visible, that data is
	// drained first: another control record could not be explained, and the overtaking
	// count is bounded by the data already queued rather than by the lane capacity.
	aa24DrainData := len(aa24Host.aa24Explained) == cap(aa24Host.aa24Explained) && aa24Data.aa24LocalConsumer < aa24DataHead
	var aa24Record AA24Record
	var aa24OK bool
	var aa24Err error
	if !aa24DrainData {
		aa24Record, aa24OK, aa24Err = aa24Control.aa24Read(aa24Dst)
		if aa24Err != nil {
			return AA24Delivery{}, false, aa24Host.aa24ReadFailed(aa24Err)
		}
		if aa24OK {
			return aa24Host.aa24HandleControl(&aa24Record, aa24Now.AA23Tick, false)
		}
	}
	if aa24Data.aa24LocalConsumer >= aa24DataHead {
		if _, _, aa24Err := aa24Data.aa24Indices(); aa24Err != nil {
			return AA24Delivery{}, false, aa24Host.aa24ReadFailed(aa24Err)
		}
		return AA24Delivery{}, false, nil
	}
	aa24Record, aa24OK, aa24Err = aa24Data.aa24Read(aa24Dst)
	if aa24Err != nil {
		return AA24Delivery{}, false, aa24Host.aa24ReadFailed(aa24Err)
	}
	if !aa24OK {
		return AA24Delivery{}, false, nil
	}
	return aa24Host.aa24HandleData(&aa24Record, aa24Now.AA23Tick)
}

// aa24PollFencedControl keeps draining the worker control lane after a host clock
// fence, so a retirement acknowledgement or gap is consumed and graceful
// resynchronization can settle. The unreliable clock is not trusted: the record's
// dequeue time is zero, its capture time is not compared, data stays fenced and the
// call still reports the clock refusal beside any consumed record.
func (aa24Host *AA24Host) aa24PollFencedControl(aa24Dst []byte) (AA24Delivery, bool, error) {
	aa24Control := aa24Host.aa24Lane(AA24LaneWorkerControl)
	aa24Record, aa24OK, aa24Err := aa24Control.aa24Read(aa24Dst)
	if aa24Err != nil {
		return AA24Delivery{}, false, aa24Host.aa24ReadFailed(aa24Err)
	}
	if !aa24OK {
		return AA24Delivery{}, false, ErrAA24Clock
	}
	aa24Delivery, aa24Handled, aa24Err := aa24Host.aa24HandleControl(&aa24Record, 0, true)
	if aa24Err == nil {
		aa24Err = ErrAA24Clock
	}
	return aa24Delivery, aa24Handled, aa24Err
}

func (aa24Host *AA24Host) aa24ReadFailed(aa24Err error) error {
	if aa24Err != ErrAA24Buffer {
		aa24Host.aa24Close(AA24FenceCorruption, 0, 0)
	}
	return aa24Err
}

func (aa24Host *AA24Host) aa24CaptureRegressed(aa24Record *AA24Record, aa24Last *AA23Tick, aa24Now AA23Tick) bool {
	if aa24Record.AA24CaptureTime > aa24Now || aa24Record.AA24CaptureTime < *aa24Last {
		return true
	}
	*aa24Last = aa24Record.AA24CaptureTime
	return false
}

func (aa24Host *AA24Host) aa24HandleControl(aa24Record *AA24Record, aa24Now AA23Tick, aa24ClockFenced bool) (AA24Delivery, bool, error) {
	aa24Control := aa24Host.aa24Lane(AA24LaneWorkerControl)
	if !aa24Host.aa24IdentityMatches(aa24Record) {
		aa24Control.aa24Bump(&aa24Control.aa24Stats.AA24Dropped)
		return AA24Delivery{}, false, ErrAA24Stale
	}
	aa24Regressed := !aa24ClockFenced && aa24Host.aa24CaptureRegressed(aa24Record, &aa24Host.aa24LastControlTime, aa24Now)
	aa24Delivery := AA24Delivery{AA24Record: *aa24Record, AA24DequeueTime: aa24Now}
	if aa24Record.AA24Kind == AA24RecordGap {
		aa24Host.aa24AccountSequence(aa24Record.AA24CaptureSequence, true)
		aa24Host.aa24Close(AA24FenceWorkerGap, aa24Record.AA24CaptureSequence, aa24Record.AA24CaptureTime)
		return aa24Delivery, true, nil
	}
	if aa24Regressed {
		aa24Host.aa24Close(AA24FenceClock, aa24Record.AA24CaptureSequence, aa24Record.AA24CaptureTime)
		return AA24Delivery{}, false, ErrAA24Clock
	}
	if aa24Host.AA24OutputOpen() && !aa24Host.aa24AccountSequence(aa24Record.AA24CaptureSequence, true) {
		aa24Host.aa24Close(AA24FenceDiscontinuity, aa24Record.AA24CaptureSequence, aa24Record.AA24CaptureTime)
	}
	aa24Index := aa24Host.aa24FindPlan(aa24Record.aa24Ref())
	if aa24Index < 0 || !aa24Host.aa24Plans[aa24Index].aa24Revoking {
		aa24Control.aa24Bump(&aa24Control.aa24Stats.AA24Dropped)
		return AA24Delivery{}, false, ErrAA24Stale
	}
	aa24Host.aa24Plans = append(aa24Host.aa24Plans[:aa24Index], aa24Host.aa24Plans[aa24Index+1:]...)
	// The worker cleared its active state for the retired plan and refuses its
	// commands without a completion, so they cannot stay outstanding.
	aa24Retired := aa24Record.aa24Ref()
	aa24Kept := aa24Host.aa24Outstanding[:0]
	for _, aa24Identity := range aa24Host.aa24Outstanding {
		if !aa24Identity.AA24Ref.aa24Matches(aa24Retired) || !aa24Host.aa24Linger(aa24Identity) {
			aa24Kept = append(aa24Kept, aa24Identity)
		}
	}
	aa24Host.aa24Outstanding = aa24Kept
	return aa24Delivery, true, nil
}

func (aa24Host *AA24Host) aa24HandleData(aa24Record *AA24Record, aa24Now AA23Tick) (AA24Delivery, bool, error) {
	aa24Data := aa24Host.aa24Lane(AA24LaneWorkerData)
	if !aa24Host.aa24IdentityMatches(aa24Record) {
		aa24Data.aa24Bump(&aa24Data.aa24Stats.AA24Dropped)
		return AA24Delivery{}, false, ErrAA24Stale
	}
	if aa24Host.aa24CaptureRegressed(aa24Record, &aa24Host.aa24LastDataCapture, aa24Now) {
		aa24Host.aa24Close(AA24FenceClock, aa24Record.AA24CaptureSequence, aa24Record.AA24CaptureTime)
		return AA24Delivery{}, false, ErrAA24Clock
	}
	aa24Delivery := AA24Delivery{AA24Record: *aa24Record, AA24DequeueTime: aa24Now}
	if aa24Host.AA24OutputOpen() && !aa24Host.aa24AccountSequence(aa24Record.AA24CaptureSequence, false) {
		aa24Host.aa24Close(AA24FenceDiscontinuity, aa24Record.AA24CaptureSequence, aa24Record.AA24CaptureTime)
		return aa24Delivery, true, ErrAA24Gap
	}
	if aa24Record.AA24Kind == AA24RecordCompletion {
		aa24Identity := aa24Record.aa24Identity()
		for aa24Index := range aa24Host.aa24Outstanding {
			if aa24Host.aa24Outstanding[aa24Index].aa24Matches(aa24Identity) {
				aa24Host.aa24Outstanding = append(aa24Host.aa24Outstanding[:aa24Index], aa24Host.aa24Outstanding[aa24Index+1:]...)
				return aa24Delivery, true, nil
			}
		}
		for aa24Index := range aa24Host.aa24Lingering {
			if aa24Host.aa24Lingering[aa24Index].aa24Identity.aa24Matches(aa24Identity) {
				aa24Host.aa24Lingering = append(aa24Host.aa24Lingering[:aa24Index], aa24Host.aa24Lingering[aa24Index+1:]...)
				return aa24Delivery, true, nil
			}
		}
		aa24Data.aa24Bump(&aa24Data.aa24Stats.AA24Dropped)
		return AA24Delivery{}, false, ErrAA24Stale
	}
	return aa24Delivery, true, nil
}

func (aa24Host *AA24Host) aa24SendStop() error {
	if aa24Host.aa24StopSent {
		return nil
	}
	aa24Control := aa24Host.aa24Lane(AA24LaneHostControl)
	aa24Index := aa24Control.aa24LocalProducer
	aa24Record := AA24Record{
		AA24Kind:   AA24RecordStop,
		AA24Worker: aa24Host.aa24Expected.AA24Binding.AA24Worker,
		AA24Stream: aa24Host.aa24Expected.AA24Binding.AA24Stream,
	}
	if aa24Err := aa24Control.aa24Write(&aa24Record, 0); aa24Err != nil {
		return aa24Err
	}
	aa24Host.aa24StopSent = true
	aa24Host.aa24StopIndex = aa24Index
	return nil
}

// AA24AcknowledgeGap names the exact observed fence and queues the stop that
// acknowledges it. The stop uses the reserved control slot.
func (aa24Host *AA24Host) AA24AcknowledgeGap(aa24Sequence uint64, aa24Now AA23ClockReading) error {
	if aa24Err := aa24Host.aa24Tick(aa24Now); aa24Err != nil && aa24Err != ErrAA24Clock {
		return aa24Err
	}
	if aa24Host.aa24Fence.AA24Reason == AA24FenceNone {
		return ErrAA24NotFenced
	}
	if aa24Sequence != aa24Host.aa24Fence.AA24Sequence {
		return ErrAA24Stale
	}
	return aa24Host.aa24SendStop()
}

// AA24RequestStop fences this side and queues the stream's stop for teardown.
func (aa24Host *AA24Host) AA24RequestStop(aa24Now AA23ClockReading) error {
	if aa24Err := aa24Host.aa24Tick(aa24Now); aa24Err != nil && aa24Err != ErrAA24Clock {
		return aa24Err
	}
	aa24Host.aa24Close(AA24FenceStop, 0, 0)
	return aa24Host.aa24SendStop()
}

// aa24StopConsumed reports that the worker's consumer index passed the stop.
func (aa24Host *AA24Host) aa24StopConsumed() bool {
	if !aa24Host.aa24StopSent {
		return false
	}
	_, aa24Consumer, aa24Err := aa24Host.aa24Lane(AA24LaneHostControl).aa24Indices()
	return aa24Err == nil && aa24Consumer > aa24Host.aa24StopIndex
}

// aa24ControlSettled reports that the worker consumed the stop and that no worker
// control record stays unread, so a pending gap or acknowledgement is not lost.
func (aa24Host *AA24Host) aa24ControlSettled() bool {
	if !aa24Host.aa24StopConsumed() {
		return false
	}
	aa24Producer, aa24Consumer, aa24Err := aa24Host.aa24Lane(AA24LaneWorkerControl).aa24Indices()
	return aa24Err == nil && aa24Producer == aa24Consumer
}

// AA24ResyncExpected names the successor mapping once the fence is acknowledged: the
// worker consumed the stop and no worker control record stays unread.
func (aa24Host *AA24Host) AA24ResyncExpected(aa24Next AA23StreamGeneration) (AA24Expected, error) {
	if aa24Host.aa24Detached {
		return AA24Expected{}, ErrAA24Detached
	}
	if aa24Host.aa24Fence.AA24Reason == AA24FenceNone {
		return AA24Expected{}, ErrAA24NotFenced
	}
	if !aa24Host.aa24ControlSettled() {
		return AA24Expected{}, ErrAA24NotAcknowledged
	}
	if aa24Next <= aa24Host.aa24Expected.AA24Binding.AA24Stream {
		return AA24Expected{}, ErrAA24Stale
	}
	return aa24Host.aa24Successor(aa24Host.aa24Expected.AA24Binding.AA24Worker, aa24Next), nil
}

func (aa24Host *AA24Host) aa24Successor(aa24Worker AA23WorkerID, aa24Stream AA23StreamGeneration) AA24Expected {
	aa24Successor := aa24Host.aa24Expected
	aa24Successor.AA24Binding.AA24Worker = aa24Worker
	aa24Successor.AA24Binding.AA24Stream = aa24Stream
	aa24Successor.AA24PlanFloor = aa24Host.aa24PlanFloor
	aa24Successor.AA24TokenFloor = aa24Host.aa24TokenFloor
	return aa24Successor
}

// AA24ObserveWorkerTerminated records supervisor proof that the worker process is
// gone and that all its access to this mapping has ceased.
func (aa24Host *AA24Host) AA24ObserveWorkerTerminated() {
	aa24Host.aa24Terminated = true
	aa24Host.aa24Close(AA24FenceWorkerTerminated, 0, 0)
}

// AA24RestartExpected names a replacement worker's mapping. The worker incarnation
// strictly advances, floors carry over and no queued record survives, because the
// successor is a fresh mapping.
func (aa24Host *AA24Host) AA24RestartExpected(aa24NextWorker AA23WorkerID, aa24NextStream AA23StreamGeneration) (AA24Expected, error) {
	if aa24Host.aa24Detached {
		return AA24Expected{}, ErrAA24Detached
	}
	if !aa24Host.aa24Terminated {
		return AA24Expected{}, ErrAA24NotAcknowledged
	}
	if aa24NextWorker <= aa24Host.aa24Expected.AA24Binding.AA24Worker || aa24NextStream == 0 {
		return AA24Expected{}, ErrAA24Stale
	}
	return aa24Host.aa24Successor(aa24NextWorker, aa24NextStream), nil
}

// AA24Teardown releases the mapping once the worker consumed the stop and the host
// read every worker control record (the worker's own teardown needs a raised gap
// consumed), or the worker is proven terminated. Stop consumption alone does not
// prove the worker ceased touching the mapping; that proof stays with the
// supervisor through AA24ObserveWorkerTerminated. It never drains a lane, so a saturated data lane cannot hold shutdown.
func (aa24Host *AA24Host) AA24Teardown() error {
	if aa24Host.aa24Detached {
		return nil
	}
	if !aa24Host.aa24Terminated && !aa24Host.aa24ControlSettled() {
		return ErrAA24NotAcknowledged
	}
	aa24Host.aa24Detached = true
	aa24Host.aa24Lanes = [aa24LaneCount]aa24Lane{}
	aa24Host.aa24Plans = nil
	aa24Host.aa24Outstanding = nil
	aa24Host.aa24Lingering = nil
	return nil
}
