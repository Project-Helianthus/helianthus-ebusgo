// Package localworker models the offline local eBUS responder worker.
//
// Status: Hypothesis. Everything here is a pure, deterministic software model
// over injected time. It opens no device, owns no transport and cannot transmit:
// nothing in this package is reachable from a default configuration, and it adds
// no arming switch. Timing, error rates and platform suitability remain Unknown.
//
// This file freezes the registration, plan and pool API for its consumers.
// A consumer needing a different API stops rather than editing these types.
//
// The model is serialized: one caller drives a Registry and its
// WorkerView at a time, and the shared-memory lanes and their atomic ordering
// belong to a separate contract. The registry stands for the host and the view
// for the worker. They exchange only explicit notices and acknowledgements, so
// host publication is never treated as proof of what the worker observed. No
// operation retries, spins, waits or invokes a callback.
//
// Local registration cannot prove the absence of an identical physical responder:
// the conflict table only records conflicts that are already known. Stateful
// operations and NACK failure policies are refused.
package localworker

import (
	"bytes"
	"errors"
	"math"
)

const (
	planSymbolEscape     byte = 0xA9
	planSymbolSyn        byte = 0xAA
	planEscapedEscape    byte = 0x00
	planEscapedSyn       byte = 0x01
	planAddressBroadcast byte = 0xFE
	planCRCPolynomial    byte = 0x9B

	addressSpace       = 256
	maxNativeLength    = 255
	maxEncodedResponse = 2 * (1 + maxNativeLength + 1)

	poolByteCeiling uint64 = 1 << 26
)

// Refusals are sentinels; a refusal changes no incumbent state unless its
// operation says otherwise.
var (
	ErrPlanInvalidLimits    = errors.New("localworker: invalid limits")
	ErrInvalidIdentity      = errors.New("localworker: zero or malformed identity")
	ErrInvalidAddress       = errors.New("localworker: address not admitted")
	ErrDuplicateAddress     = errors.New("localworker: address already registered")
	ErrRegistrationConflict = errors.New("localworker: same key with different content")
	ErrRegistrationRetiring = errors.New("localworker: predecessor wire authority unsettled")
	ErrNotRegistered        = errors.New("localworker: no active registration for this exact key")
	ErrStaleEpoch           = errors.New("localworker: registration epoch does not advance")
	ErrUnknownSubscriber    = errors.New("localworker: subscriber is not open")
	ErrPlanCapacity         = errors.New("localworker: bounded capacity exhausted")
	ErrPhysicalConflict     = errors.New("localworker: known physical address conflict")
	ErrMasterConflict       = errors.New("localworker: initiator identity held by another subscriber")
	ErrClockClosed          = errors.New("localworker: clock regression or incomparable domain closed admission")
	ErrPlanExpired          = errors.New("localworker: lease or plan expired")
	ErrLeaseTooLong         = errors.New("localworker: lease exceeds the configured bound")
	ErrInvalidPlan          = errors.New("localworker: plan candidate is malformed")
	ErrAmbiguousPlan        = errors.New("localworker: plan entries overlap")
	ErrPlanOversize         = errors.New("localworker: plan exceeds a configured bound")
	ErrUnsupported          = errors.New("localworker: unsupported operation or failure policy")
	ErrPoolExhausted        = errors.New("localworker: no free plan slot")
	ErrStaleGeneration      = errors.New("localworker: generation does not advance")
	ErrCounterExhausted     = errors.New("localworker: counter refuses to wrap")
	ErrIdentityMismatch     = errors.New("localworker: exact identity mismatch")
	ErrSlotState            = errors.New("localworker: slot ownership forbids this transition")
	ErrSlotPinned           = errors.New("localworker: slot is pinned by a transaction")
	ErrNoWorker             = errors.New("localworker: no worker incarnation bound")
	ErrNoPlan               = errors.New("localworker: no adopted plan for this address")
	ErrNoMatch              = errors.New("localworker: request matches no plan entry")
	ErrPlanRevoked          = errors.New("localworker: plan revocation observed")
	ErrPinBusy              = errors.New("localworker: a transaction already holds a pin")
	ErrPinReleased          = errors.New("localworker: pin no longer held")
	ErrStaleTransaction     = errors.New("localworker: transaction ordinal does not advance")
	ErrPhase                = errors.New("localworker: entry does not allow this phase")
)

type (
	// SubscriberID is a host-assigned, never reused subscriber incarnation.
	SubscriberID uint64
	// RegistrationEpoch advances per subscriber and is never reused.
	RegistrationEpoch uint64
	// WorkerID is a worker incarnation; successors inherit no authority.
	WorkerID uint64
	// StreamGeneration names one uninterrupted observation stream of a worker.
	StreamGeneration uint64
	// PlanGeneration names one immutable publication within a worker.
	PlanGeneration uint64
	// SourceGeneration names the source state a plan was compiled from.
	SourceGeneration uint64
	// TransactionOrdinal is assigned at request start within a stream.
	TransactionOrdinal uint64
	// Tick is one injected monotonic clock tick.
	Tick uint64
)

// ClockReading is one injected monotonic reading. Readings pass as values,
// so the model never calls out to a clock. Domain zero is not comparable.
type ClockReading struct {
	Domain uint64
	Tick   Tick
}

// PlanLimits holds the finite fixture parameters. They are bounds for the model,
// never product timing or error acceptance.
type PlanLimits struct {
	MaxSubscribers          uint32
	MaxRegistrations        uint32
	MaxMasters              uint32
	MaxPlanSlots            uint32
	MaxEntriesPerPlan       uint32
	MaxRequestBytes         uint32
	MaxEncodedResponseBytes uint32
	MaxPlanBytes            uint32
	MaxLeaseTicks           Tick
}

func (limits PlanLimits) validate() error {
	if limits.MaxSubscribers == 0 || limits.MaxRegistrations == 0 ||
		limits.MaxMasters == 0 || limits.MaxPlanSlots == 0 ||
		limits.MaxEntriesPerPlan == 0 || limits.MaxRequestBytes == 0 ||
		limits.MaxEncodedResponseBytes == 0 ||
		limits.MaxPlanBytes == 0 || limits.MaxLeaseTicks == 0 {
		return ErrPlanInvalidLimits
	}
	if limits.MaxRegistrations > addressSpace || limits.MaxMasters > addressSpace ||
		limits.MaxSubscribers > addressSpace {
		return ErrPlanInvalidLimits
	}
	if limits.MaxRequestBytes > maxNativeLength ||
		limits.MaxEncodedResponseBytes > maxEncodedResponse {
		return ErrPlanInvalidLimits
	}
	if uint64(limits.MaxPlanBytes) < uint64(limits.MaxRequestBytes)+uint64(limits.MaxEncodedResponseBytes) {
		return ErrPlanInvalidLimits
	}
	entryBytes := uint64(limits.MaxEntriesPerPlan) * entryAccountingBytes
	slotBytes := uint64(limits.MaxPlanBytes) + entryBytes
	if slotBytes < entryBytes || slotBytes > poolByteCeiling ||
		uint64(limits.MaxPlanSlots) > poolByteCeiling/slotBytes {
		return ErrPlanInvalidLimits
	}
	return nil
}

const entryAccountingBytes uint64 = 64

// RegistrationKey compares as one complete tuple, never by address alone.
type RegistrationKey struct {
	Subscriber SubscriberID
	Address    byte
	Epoch      RegistrationEpoch
}

func (key RegistrationKey) matches(other RegistrationKey) bool {
	if key.Subscriber != other.Subscriber {
		return false
	}
	if key.Address != other.Address {
		return false
	}
	return key.Epoch == other.Epoch
}

// FailureAction states what the responder does in a failure class. Refusal
// is not a universal NACK rule, so only silence is admitted until a NACK action
// has native phase support and independent evidence.
type FailureAction uint8

const (
	FailureUnspecified FailureAction = iota
	FailureSilence
	FailureNACK
)

// FailurePolicy must name every failure class explicitly.
type FailurePolicy struct {
	Unavailable FailureAction
	Malformed   FailureAction
	Timeout     FailureAction
}

func (policy FailurePolicy) validate() error {
	for _, action := range [...]FailureAction{policy.Unavailable, policy.Malformed, policy.Timeout} {
		if action != FailureSilence {
			return ErrUnsupported
		}
	}
	return nil
}

// Registration is the detached content of one registration. It grants a
// target address only: no initiator identity, arbitration or general transmit right.
type Registration struct {
	Key               RegistrationKey
	LeaseExpiry       Tick
	Source            SourceGeneration
	SourceLeaseExpiry Tick
	Failure           FailurePolicy
}

// RegistrationState is the lifecycle of one registration key.
type RegistrationState uint8

const (
	RegistrationAbsent RegistrationState = iota
	RegistrationActive
	// RegistrationRetiring holds the address until every plan of the key has
	// been acknowledged or its worker's termination has been observed.
	RegistrationRetiring
)

// RegistrationView is a detached snapshot of one registration.
type RegistrationView struct {
	Registered        Registration
	State             RegistrationState
	LeaseExpiry       Tick
	Source            SourceGeneration
	SourceLeaseExpiry Tick
}

// PlanIdentity is the complete identity of one published plan.
type PlanIdentity struct {
	Registration RegistrationKey
	Worker       WorkerID
	Stream       StreamGeneration
	Plan         PlanGeneration
	Source       SourceGeneration
}

func (identity PlanIdentity) matches(other PlanIdentity) bool {
	if !identity.Registration.matches(other.Registration) {
		return false
	}
	if identity.Worker != other.Worker {
		return false
	}
	if identity.Stream != other.Stream {
		return false
	}
	if identity.Plan != other.Plan {
		return false
	}
	return identity.Source == other.Source
}

// WireKey is the identity a pinned transaction presents at every boundary.
type WireKey struct {
	Identity    PlanIdentity
	Transaction TransactionOrdinal
}

// Operation classifies an entry. Only reads can be installed.
type Operation uint8

const (
	OperationUnspecified Operation = iota
	OperationRead
	OperationStateful
)

// Phase names the native transaction phase an entry may answer in.
type Phase uint8

const (
	PhaseNone Phase = iota
	PhaseRequest
	PhaseTargetACK
	PhaseResponse
	PhaseInitiatorACK
	PhaseEnded
)

// SourceConstraint restricts the initiator of a request. Any and an exact
// source are mutually exclusive.
type SourceConstraint struct {
	Any    bool
	Source byte
}

// PlanEntry is one exact match and its prepared response. Request holds
// logical request data. EncodedResponse holds the wire form of the native
// response after the target ACK: LEN DATA CRC, escaped; see EncodeResponse.
type PlanEntry struct {
	Source          SourceConstraint
	Destination     byte
	PB              byte
	SB              byte
	RequestLength   uint32
	Request         []byte
	Operation       Operation
	AllowedPhase    Phase
	MaxEncodedBytes uint32
	EncodedResponse []byte
}

// PlanCandidate is the detached input of one publication. The registration
// and source leases are bound from the registry, never from the candidate.
type PlanCandidate struct {
	Identity   PlanIdentity
	PlanExpiry Tick
	Failure    FailurePolicy
	Entries    []PlanEntry
}

// PlanValidity holds the exclusive deadlines frozen into a published plan.
// A later renewal never extends them; it needs a fresh plan generation.
type PlanValidity struct {
	Domain             uint64
	PlanExpiry         Tick
	RegistrationExpiry Tick
	SourceLeaseExpiry  Tick
}

// check admits only strictly before each deadline: equality is expired.
func (validity PlanValidity) check(now ClockReading) error {
	if now.Domain != validity.Domain {
		// Deadlines of another clock domain are incomparable with this reading.
		return ErrClockClosed
	}
	if now.Tick >= validity.PlanExpiry {
		return ErrPlanExpired
	}
	if now.Tick >= validity.RegistrationExpiry {
		return ErrPlanExpired
	}
	if now.Tick >= validity.SourceLeaseExpiry {
		return ErrPlanExpired
	}
	return nil
}

// SlotState is the ownership of one pool slot. Only the host moves it.
type SlotState uint8

const (
	SlotInvalid SlotState = iota
	// SlotFree is host-owned, holds nothing and can start a new incarnation.
	SlotFree
	// SlotWriting is host-owned and invisible to the worker.
	SlotWriting
	// SlotPublished is complete and immutable, and may be adopted.
	SlotPublished
	// SlotRetained is retired, but the worker may still hold a reference.
	SlotRetained
	// SlotReclaimable has proof that no reader remains.
	SlotReclaimable
)

// SlotRef names one incarnation of a slot; reuse invalidates older refs.
type SlotRef struct {
	Index       uint32
	Incarnation uint64
}

// SlotCounts is a detached ownership census of the pool.
type SlotCounts struct {
	Free        uint32
	Writing     uint32
	Published   uint32
	Retained    uint32
	Reclaimable uint32
}

// RetireReason ranks why a slot left publication. A stronger reason replaces
// a weaker one so a re-delivered notice can only tighten the worker's view.
type RetireReason uint8

const (
	RetireNone RetireReason = iota
	// RetireSuperseded stops new selection; a selected transaction may finish.
	RetireSuperseded
	// RetireExpired closes admission from the plan.
	RetireExpired
	// RetireRevoked closes admission, including for a selected transaction.
	RetireRevoked
)

// PublishNotice tells the worker about one complete generation.
type PublishNotice struct {
	Slot     SlotRef
	Identity PlanIdentity
}

// RevocationNotice tells the worker that one plan left publication. Sending
// it proves nothing: the host retains the slot until an acknowledgement.
type RevocationNotice struct {
	Slot     SlotRef
	Identity PlanIdentity
	Reason   RetireReason
}

// Acknowledgement states that selection of this exact plan is fenced and no
// reader remains.
type Acknowledgement struct {
	Slot     SlotRef
	Identity PlanIdentity
}

type entryRecord struct {
	sourceAny    bool
	source       byte
	pb           byte
	sb           byte
	phase        Phase
	requestStart uint32
	requestLen   uint32
	replyStart   uint32
	replyLen     uint32
}

type slot struct {
	state       SlotState
	incarnation uint64
	identity    PlanIdentity
	validity    PlanValidity
	reason      RetireReason
	pinned      bool
	entryCount  uint32
	entries     []entryRecord
	bytes       []byte
}

// Pool is the preallocated plan storage. All slot storage exists from
// construction, and a slot returns to free only through an observed proof.
type Pool struct {
	slots []slot
}

func newPool(limits PlanLimits) *Pool {
	pool := &Pool{slots: make([]slot, limits.MaxPlanSlots)}
	for index := range pool.slots {
		pool.slots[index] = slot{
			state:       SlotFree,
			incarnation: 1,
			entries:     make([]entryRecord, limits.MaxEntriesPerPlan),
			bytes:       make([]byte, limits.MaxPlanBytes),
		}
	}
	return pool
}

func (pool *Pool) resolve(ref SlotRef) (*slot, error) {
	if uint64(ref.Index) >= uint64(len(pool.slots)) {
		return nil, ErrSlotState
	}
	current := &pool.slots[ref.Index]
	if current.incarnation != ref.Incarnation {
		return nil, ErrSlotState
	}
	return current, nil
}

// SlotState returns the current ownership of a slot index.
func (pool *Pool) SlotState(index uint32) SlotState {
	if uint64(index) >= uint64(len(pool.slots)) {
		return SlotInvalid
	}
	return pool.slots[index].state
}

// Counts returns a census of every slot.
func (pool *Pool) Counts() SlotCounts {
	var counts SlotCounts
	for index := range pool.slots {
		switch pool.slots[index].state {
		case SlotFree:
			counts.Free++
		case SlotWriting:
			counts.Writing++
		case SlotPublished:
			counts.Published++
		case SlotRetained:
			counts.Retained++
		case SlotReclaimable:
			counts.Reclaimable++
		}
	}
	return counts
}

// Reclaim moves a proven reclaimable slot to free under a new incarnation.
// An incarnation that cannot advance retires the slot rather than wrapping, so
// capacity shrinks instead of an old reference becoming valid again.
func (pool *Pool) Reclaim(ref SlotRef) error {
	current, err := pool.resolve(ref)
	if err != nil {
		return err
	}
	if current.state != SlotReclaimable {
		return ErrSlotState
	}
	return current.release()
}

// PendingReclaims appends each reclaimable slot, up to the capacity of dst,
// without allocating. A slot whose incarnation cannot advance stays listed.
func (pool *Pool) PendingReclaims(dst []SlotRef) []SlotRef {
	for index := range pool.slots {
		if len(dst) == cap(dst) {
			return dst
		}
		current := &pool.slots[index]
		if current.state == SlotReclaimable {
			dst = append(dst, current.ref(index))
		}
	}
	return dst
}

func (current *slot) release() error {
	if current.incarnation == math.MaxUint64 {
		return ErrCounterExhausted
	}
	current.incarnation++
	current.state = SlotFree
	current.identity = PlanIdentity{}
	current.validity = PlanValidity{}
	current.reason = RetireNone
	current.entryCount = 0
	return nil
}

func (current *slot) ref(index int) SlotRef {
	return SlotRef{Index: uint32(index), Incarnation: current.incarnation}
}

type clockGuard struct {
	started bool
	closed  bool
	domain  uint64
	last    Tick
}

// observe latches closed on any regression or incomparable domain. Wall
// clock is never substituted and only a new incarnation reopens admission.
func (guard *clockGuard) observe(now ClockReading) error {
	if guard.closed {
		return ErrClockClosed
	}
	if now.Domain == 0 {
		guard.closed = true
		return ErrClockClosed
	}
	if !guard.started {
		guard.started = true
		guard.domain = now.Domain
		guard.last = now.Tick
		return nil
	}
	if now.Domain != guard.domain || now.Tick < guard.last {
		guard.closed = true
		return ErrClockClosed
	}
	guard.last = now.Tick
	return nil
}

func isInitiatorNibble(nibble byte) bool {
	switch nibble {
	case 0x0, 0x1, 0x3, 0x7, 0xF:
		return true
	}
	return false
}

func isInitiatorAddress(address byte) bool {
	return isInitiatorNibble(address>>4) && isInitiatorNibble(address&0x0F)
}

// isTargetAddress admits an initiator/target destination: no broadcast,
// initiator pattern, escape or SYN.
func isTargetAddress(address byte) bool {
	switch address {
	case planAddressBroadcast, planSymbolEscape, planSymbolSyn:
		return false
	}
	return !isInitiatorAddress(address)
}

func crcStep(crc byte, value byte) byte {
	for bit := 0; bit < 8; bit++ {
		if crc&0x80 != 0 {
			crc = crc<<1 ^ planCRCPolynomial
		} else {
			crc <<= 1
		}
	}
	return crc ^ value
}

// planCRCUpdate feeds one logical byte in its wire-expanded form.
func planCRCUpdate(crc byte, value byte) byte {
	switch value {
	case planSymbolEscape:
		return crcStep(crcStep(crc, planSymbolEscape), planEscapedEscape)
	case planSymbolSyn:
		return crcStep(crcStep(crc, planSymbolEscape), planEscapedSyn)
	}
	return crcStep(crc, value)
}

func appendEscaped(dst []byte, value byte) []byte {
	switch value {
	case planSymbolEscape:
		return append(dst, planSymbolEscape, planEscapedEscape)
	case planSymbolSyn:
		return append(dst, planSymbolEscape, planEscapedSyn)
	}
	return append(dst, value)
}

// EncodeResponse appends the native target response for logical data to dst:
// LEN DATA CRC with CRC over LEN DATA, then escaping over every byte.
func EncodeResponse(dst []byte, data []byte) ([]byte, error) {
	if len(data) > maxNativeLength {
		return dst, ErrPlanOversize
	}
	length := byte(len(data))
	crc := planCRCUpdate(0, length)
	dst = appendEscaped(dst, length)
	for _, value := range data {
		crc = planCRCUpdate(crc, value)
		dst = appendEscaped(dst, value)
	}
	return appendEscaped(dst, crc), nil
}

// validateEncodedResponse proves the wire form holds exactly one native
// response: no raw SYN, only complete escape pairs, a matching length and CRC.
func validateEncodedResponse(wire []byte) error {
	var crc byte
	decoded := 0
	declared := -1
	receivedCRC := -1
	for offset := 0; offset < len(wire); offset++ {
		value := wire[offset]
		switch value {
		case planSymbolSyn:
			return ErrInvalidPlan
		case planSymbolEscape:
			offset++
			if offset >= len(wire) {
				return ErrInvalidPlan
			}
			switch wire[offset] {
			case planEscapedEscape:
				value = planSymbolEscape
			case planEscapedSyn:
				value = planSymbolSyn
			default:
				return ErrInvalidPlan
			}
		}
		if decoded == 0 {
			declared = int(value)
		}
		if decoded == declared+1 {
			receivedCRC = int(value)
		} else {
			crc = planCRCUpdate(crc, value)
		}
		decoded++
	}
	if declared < 0 || decoded != declared+2 || receivedCRC != int(crc) {
		return ErrInvalidPlan
	}
	return nil
}

type registrationRecord struct {
	used        bool
	state       RegistrationState
	registered  Registration
	leaseExpiry Tick
	source      SourceGeneration
	sourceLease Tick
	published   int32
	publishTop  PlanGeneration
}

type subscriberRecord struct {
	id            SubscriberID
	open          bool
	epochHigh     RegistrationEpoch
	registrations uint32
}

// Registry is the host side: registrations, initiator identities, known
// conflicts and the plan publisher. Every table is sized at construction.
type Registry struct {
	limits        PlanLimits
	pool          *Pool
	clock         clockGuard
	subscriberTop SubscriberID
	subscribers   []subscriberRecord
	records       []registrationRecord
	byAddress     [addressSpace]int32
	masters       [addressSpace]SubscriberID
	masterCount   uint32
	conflicts     [addressSpace]bool
	workerBound   bool
	worker        WorkerID
	stream        StreamGeneration
	workerTop     WorkerID
	planTop       PlanGeneration
	// overlapScratch is the preallocated index space for overlap sorting.
	overlapScratch []int
}

// NewRegistry validates limits before any effect. subscriberFloor is the
// highest subscriber identity a predecessor may have used, zero when none did.
func NewRegistry(limits PlanLimits, subscriberFloor SubscriberID) (*Registry, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &Registry{
		limits:         limits,
		pool:           newPool(limits),
		subscriberTop:  subscriberFloor,
		subscribers:    make([]subscriberRecord, limits.MaxSubscribers),
		records:        make([]registrationRecord, limits.MaxRegistrations),
		overlapScratch: make([]int, limits.MaxEntriesPerPlan),
	}, nil
}

// Pool returns the plan pool shared with worker views.
func (registry *Registry) Pool() *Pool {
	return registry.pool
}

// observeClock closes admission and retires every plan on a clock fault.
func (registry *Registry) observeClock(now ClockReading) error {
	wasClosed := registry.clock.closed
	err := registry.clock.observe(now)
	if err != nil && !wasClosed {
		registry.retireWhere(RetireRevoked, func(*slot) bool { return true })
	}
	return err
}

// retireWhere applies only model-internal predicates, never caller code.
func (registry *Registry) retireWhere(reason RetireReason, selected func(*slot) bool) {
	for index := range registry.pool.slots {
		current := &registry.pool.slots[index]
		if current.state == SlotFree || current.state == SlotReclaimable {
			continue
		}
		if selected(current) {
			registry.retireSlot(current, reason)
		}
	}
}

func (registry *Registry) retireSlot(current *slot, reason RetireReason) {
	switch current.state {
	case SlotWriting:
		// Never visible to a worker, so no acknowledgement can be owed. An
		// exhausted incarnation stays out of service rather than wrapping.
		if current.release() != nil {
			current.state = SlotReclaimable
		}
	case SlotPublished:
		current.state = SlotRetained
		current.reason = reason
		record := registry.findExact(current.identity.Registration)
		if record != nil {
			record.published = -1
		}
	case SlotRetained:
		if reason > current.reason {
			current.reason = reason
		}
	}
}

// findExact resolves a complete key; an address alone identifies nothing.
func (registry *Registry) findExact(key RegistrationKey) *registrationRecord {
	index := registry.byAddress[key.Address]
	if index == 0 {
		return nil
	}
	record := &registry.records[index-1]
	if !record.registered.Key.matches(key) {
		return nil
	}
	return record
}

func (registry *Registry) findSubscriber(id SubscriberID) *subscriberRecord {
	if id == 0 {
		return nil
	}
	for index := range registry.subscribers {
		if registry.subscribers[index].id == id {
			return &registry.subscribers[index]
		}
	}
	return nil
}

// OpenSubscriber allocates a new, never reused subscriber incarnation.
func (registry *Registry) OpenSubscriber() (SubscriberID, error) {
	if registry.subscriberTop == math.MaxUint64 {
		return 0, ErrCounterExhausted
	}
	for index := range registry.subscribers {
		record := &registry.subscribers[index]
		if record.id != 0 {
			continue
		}
		registry.subscriberTop++
		*record = subscriberRecord{id: registry.subscriberTop, open: true}
		return record.id, nil
	}
	return 0, ErrPlanCapacity
}

// checkLease bounds a lease by subtraction, which the first check keeps from
// underflowing, so no deadline addition can overflow.
func (registry *Registry) checkLease(expiry Tick, now ClockReading) error {
	if now.Tick >= expiry {
		return ErrPlanExpired
	}
	if expiry-now.Tick > registry.limits.MaxLeaseTicks {
		return ErrLeaseTooLong
	}
	return nil
}

// Register admits one target address for one accountable subscriber. An exact
// retry returns the current state and allocates nothing; any other claim on an
// occupied address is refused and leaves the incumbent untouched, including a
// later epoch of the same subscriber. There is no last-writer-wins selection.
func (registry *Registry) Register(candidate Registration, now ClockReading) (RegistrationView, error) {
	if err := registry.observeClock(now); err != nil {
		return RegistrationView{}, err
	}
	key := candidate.Key
	if key.Subscriber == 0 || key.Epoch == 0 || candidate.Source == 0 {
		return RegistrationView{}, ErrInvalidIdentity
	}
	if !isTargetAddress(key.Address) {
		return RegistrationView{}, ErrInvalidAddress
	}
	if occupant := registry.byAddress[key.Address]; occupant != 0 {
		record := &registry.records[occupant-1]
		if !record.registered.Key.matches(key) {
			return RegistrationView{}, ErrDuplicateAddress
		}
		if record.registered != candidate {
			return RegistrationView{}, ErrRegistrationConflict
		}
		if record.state != RegistrationActive {
			return RegistrationView{}, ErrRegistrationRetiring
		}
		return record.view(), nil
	}
	subscriber := registry.findSubscriber(key.Subscriber)
	if subscriber == nil || !subscriber.open {
		return RegistrationView{}, ErrUnknownSubscriber
	}
	if registry.conflicts[key.Address] {
		return RegistrationView{}, ErrPhysicalConflict
	}
	if subscriber.epochHigh == math.MaxUint64 {
		return RegistrationView{}, ErrCounterExhausted
	}
	if key.Epoch <= subscriber.epochHigh {
		return RegistrationView{}, ErrStaleEpoch
	}
	if err := candidate.Failure.validate(); err != nil {
		return RegistrationView{}, err
	}
	if err := registry.checkLease(candidate.LeaseExpiry, now); err != nil {
		return RegistrationView{}, err
	}
	if err := registry.checkLease(candidate.SourceLeaseExpiry, now); err != nil {
		return RegistrationView{}, err
	}
	for index := range registry.records {
		record := &registry.records[index]
		if record.used {
			continue
		}
		*record = registrationRecord{
			used:        true,
			state:       RegistrationActive,
			registered:  candidate,
			leaseExpiry: candidate.LeaseExpiry,
			source:      candidate.Source,
			sourceLease: candidate.SourceLeaseExpiry,
			published:   -1,
		}
		registry.byAddress[key.Address] = int32(index) + 1
		subscriber.epochHigh = key.Epoch
		subscriber.registrations++
		return record.view(), nil
	}
	return RegistrationView{}, ErrPlanCapacity
}

func (record *registrationRecord) view() RegistrationView {
	return RegistrationView{
		Registered:        record.registered,
		State:             record.state,
		LeaseExpiry:       record.leaseExpiry,
		Source:            record.source,
		SourceLeaseExpiry: record.sourceLease,
	}
}

// Registration returns a detached snapshot for an exact key.
func (registry *Registry) Registration(key RegistrationKey) RegistrationView {
	record := registry.findExact(key)
	if record == nil {
		return RegistrationView{}
	}
	return record.view()
}

// retire moves a registration to retiring and settles it when no plan of the
// key can still be referenced.
func (registry *Registry) retire(record *registrationRecord, reason RetireReason) {
	key := record.registered.Key
	record.state = RegistrationRetiring
	registry.retireWhere(reason, func(current *slot) bool {
		return current.identity.Registration.matches(key)
	})
	registry.settle(key)
}

// settle frees the address only once no plan of a retiring key remains.
func (registry *Registry) settle(key RegistrationKey) {
	record := registry.findExact(key)
	if record == nil || record.state != RegistrationRetiring {
		return
	}
	for index := range registry.pool.slots {
		current := &registry.pool.slots[index]
		if current.state != SlotPublished && current.state != SlotRetained {
			continue
		}
		if current.identity.Registration.matches(key) {
			return
		}
	}
	registry.byAddress[key.Address] = 0
	*record = registrationRecord{}
	subscriber := registry.findSubscriber(key.Subscriber)
	if subscriber == nil {
		return
	}
	subscriber.registrations--
	registry.forgetClosedSubscriber(subscriber)
}

// forgetClosedSubscriber frees a table row; the identity itself is not reused.
func (registry *Registry) forgetClosedSubscriber(subscriber *subscriberRecord) {
	if subscriber.open || subscriber.registrations != 0 {
		return
	}
	*subscriber = subscriberRecord{}
}

// Unregister retires an exact key and returns its resulting state. Repeating
// it is harmless, and a stale epoch or another subscriber's key removes nothing.
func (registry *Registry) Unregister(key RegistrationKey) RegistrationState {
	record := registry.findExact(key)
	if record == nil {
		return RegistrationAbsent
	}
	if record.state == RegistrationActive {
		registry.retire(record, RetireRevoked)
	}
	return registry.Registration(key).State
}

// DisconnectSubscriber retires every registration and initiator identity of a
// subscriber incarnation. The identity is closed for good; repeating is harmless.
func (registry *Registry) DisconnectSubscriber(id SubscriberID) {
	subscriber := registry.findSubscriber(id)
	if subscriber == nil {
		return
	}
	subscriber.open = false
	registry.releaseMastersWhere(id)
	for index := range registry.records {
		record := &registry.records[index]
		if record.used && record.state == RegistrationActive &&
			record.registered.Key.Subscriber == id {
			registry.retire(record, RetireRevoked)
		}
	}
	if subscriber = registry.findSubscriber(id); subscriber != nil {
		registry.forgetClosedSubscriber(subscriber)
	}
}

// Reset retires every registration and initiator identity. Subscribers stay
// open, and every high-water mark survives so no identity can be reused.
func (registry *Registry) Reset() {
	registry.releaseMastersWhere(0)
	for index := range registry.records {
		record := &registry.records[index]
		if record.used && record.state == RegistrationActive {
			registry.retire(record, RetireRevoked)
		}
	}
}

func (registry *Registry) releaseMastersWhere(id SubscriberID) {
	for address := range registry.masters {
		if owner := registry.masters[address]; owner != 0 && (id == 0 || owner == id) {
			registry.masters[address] = 0
			registry.masterCount--
		}
	}
}

// BindMaster records a local initiator identity. It is kept apart from target
// registration: neither grants, releases or implies the other, and this table
// performs no arbitration. A repeat by the same owner is harmless.
func (registry *Registry) BindMaster(id SubscriberID, address byte) error {
	if subscriber := registry.findSubscriber(id); subscriber == nil || !subscriber.open {
		return ErrUnknownSubscriber
	}
	if !isInitiatorAddress(address) {
		return ErrInvalidAddress
	}
	if registry.conflicts[address] {
		return ErrPhysicalConflict
	}
	switch registry.masters[address] {
	case id:
		return nil
	case 0:
	default:
		return ErrMasterConflict
	}
	if registry.masterCount >= registry.limits.MaxMasters {
		return ErrPlanCapacity
	}
	registry.masters[address] = id
	registry.masterCount++
	return nil
}

// ReleaseMaster drops a initiator identity held by exactly this owner.
func (registry *Registry) ReleaseMaster(id SubscriberID, address byte) {
	if id != 0 && registry.masters[address] == id {
		registry.masters[address] = 0
		registry.masterCount--
	}
}

// MasterOwner reports the subscriber holding a initiator identity, zero if none.
func (registry *Registry) MasterOwner(address byte) SubscriberID {
	return registry.masters[address]
}

// MarkPhysicalConflict inhibits an address that is known to conflict and
// revokes its plans, so clearing the mark never revives an old generation.
func (registry *Registry) MarkPhysicalConflict(address byte) {
	registry.conflicts[address] = true
	registry.retireWhere(RetireRevoked, func(current *slot) bool {
		return current.identity.Registration.Address == address
	})
}

// ClearPhysicalConflict lifts the inhibition; it proves no absence of a
// physical responder and restores no plan.
func (registry *Registry) ClearPhysicalConflict(address byte) {
	registry.conflicts[address] = false
}

// PhysicalConflict reports whether an address is currently inhibited.
func (registry *Registry) PhysicalConflict(address byte) bool {
	return registry.conflicts[address]
}

// RenewRegistration raises the lease for future plans only. Published plans
// keep their frozen deadlines, and an expired lease cannot be revived.
func (registry *Registry) RenewRegistration(key RegistrationKey, expiry Tick, now ClockReading) error {
	if err := registry.observeClock(now); err != nil {
		return err
	}
	record := registry.findExact(key)
	if record == nil || record.state != RegistrationActive {
		return ErrNotRegistered
	}
	if now.Tick >= record.leaseExpiry {
		return ErrPlanExpired
	}
	if expiry < record.leaseExpiry {
		return ErrStaleGeneration
	}
	if err := registry.checkLease(expiry, now); err != nil {
		return err
	}
	record.leaseExpiry = expiry
	return nil
}

// AdvanceSource moves a registration to a newer source generation and revokes
// every plan compiled from an older one.
func (registry *Registry) AdvanceSource(key RegistrationKey, source SourceGeneration, expiry Tick, now ClockReading) error {
	if err := registry.observeClock(now); err != nil {
		return err
	}
	record := registry.findExact(key)
	if record == nil || record.state != RegistrationActive {
		return ErrNotRegistered
	}
	if source <= record.source {
		return ErrStaleGeneration
	}
	if err := registry.checkLease(expiry, now); err != nil {
		return err
	}
	record.source = source
	record.sourceLease = expiry
	registry.retireWhere(RetireRevoked, func(current *slot) bool {
		return current.identity.Registration.matches(key)
	})
	return nil
}

// ExpireDue retires what has reached its exclusive deadline. Expiry closes
// admission only: a retained slot still waits for an acknowledgement or for
// observed worker termination, so a paused worker quarantines its storage.
func (registry *Registry) ExpireDue(now ClockReading) error {
	if err := registry.observeClock(now); err != nil {
		return err
	}
	registry.retireWhere(RetireExpired, func(current *slot) bool {
		return current.state == SlotPublished && current.validity.check(now) != nil
	})
	for index := range registry.records {
		record := &registry.records[index]
		if record.used && record.state == RegistrationActive && now.Tick >= record.leaseExpiry {
			registry.retire(record, RetireExpired)
		}
	}
	return nil
}

// BindWorker binds a strictly newer worker incarnation or stream generation.
// Every plan of the predecessor leaves publication; its storage stays retained
// until that predecessor acknowledges or its termination is observed. Plan
// generations are monotonic within a worker, across its stream changes.
func (registry *Registry) BindWorker(worker WorkerID, stream StreamGeneration) error {
	if worker == 0 || stream == 0 {
		return ErrInvalidIdentity
	}
	newWorker := worker > registry.workerTop
	newStream := worker == registry.workerTop && registry.workerBound && stream > registry.stream
	if !newWorker && !newStream {
		return ErrStaleGeneration
	}
	registry.retireWhere(RetireRevoked, func(*slot) bool { return true })
	if newWorker {
		registry.planTop = 0
		for index := range registry.records {
			registry.records[index].publishTop = 0
		}
	}
	registry.workerBound = true
	registry.worker = worker
	registry.workerTop = worker
	registry.stream = stream
	return nil
}

// ObserveWorkerTerminated records supervisor proof that a worker incarnation
// has exited and all access to its mapping has ceased. This is the only way
// storage is reclaimed without an acknowledgement. Registrations stay, but need
// fresh plans under a newly bound worker.
func (registry *Registry) ObserveWorkerTerminated(worker WorkerID) error {
	if worker == 0 || worker > registry.workerTop {
		return ErrInvalidIdentity
	}
	if registry.workerBound && registry.worker == worker {
		registry.workerBound = false
		registry.retireWhere(RetireRevoked, func(current *slot) bool {
			return current.state == SlotWriting
		})
	}
	for index := range registry.pool.slots {
		current := &registry.pool.slots[index]
		if current.state != SlotPublished && current.state != SlotRetained {
			continue
		}
		if current.identity.Worker != worker {
			continue
		}
		key := current.identity.Registration
		registry.retireSlot(current, RetireRevoked)
		current.pinned = false
		current.state = SlotReclaimable
		registry.settle(key)
	}
	return nil
}

// validateCandidate checks the whole detached candidate before any effect.
func (registry *Registry) validateCandidate(candidate *PlanCandidate) error {
	limits := registry.limits
	identity := candidate.Identity
	if identity.Plan == 0 || identity.Source == 0 || identity.Worker == 0 || identity.Stream == 0 {
		return ErrInvalidIdentity
	}
	if err := candidate.Failure.validate(); err != nil {
		return err
	}
	if len(candidate.Entries) == 0 {
		return ErrInvalidPlan
	}
	if uint64(len(candidate.Entries)) > uint64(limits.MaxEntriesPerPlan) {
		return ErrPlanOversize
	}
	total := uint64(0)
	for index := range candidate.Entries {
		entry := &candidate.Entries[index]
		if err := registry.validateEntry(entry, identity.Registration.Address); err != nil {
			return err
		}
		total += uint64(len(entry.Request)) + uint64(len(entry.EncodedResponse))
		if total > uint64(limits.MaxPlanBytes) {
			return ErrPlanOversize
		}
	}
	if planOverlaps(candidate.Entries, registry.overlapScratch) {
		return ErrAmbiguousPlan
	}
	return nil
}

func (registry *Registry) validateEntry(entry *PlanEntry, address byte) error {
	if entry.Operation != OperationRead {
		return ErrUnsupported
	}
	if entry.AllowedPhase != PhaseResponse {
		return ErrUnsupported
	}
	if entry.Destination != address {
		return ErrInvalidPlan
	}
	if entry.Source.Any {
		if entry.Source.Source != 0 {
			return ErrInvalidPlan
		}
	} else if !isInitiatorAddress(entry.Source.Source) {
		return ErrInvalidPlan
	}
	if uint64(entry.RequestLength) != uint64(len(entry.Request)) {
		return ErrInvalidPlan
	}
	if uint64(len(entry.Request)) > uint64(registry.limits.MaxRequestBytes) {
		return ErrPlanOversize
	}
	encoded := uint64(len(entry.EncodedResponse))
	if entry.MaxEncodedBytes == 0 {
		return ErrInvalidPlan
	}
	if entry.MaxEncodedBytes > registry.limits.MaxEncodedResponseBytes || encoded > uint64(entry.MaxEncodedBytes) {
		return ErrPlanOversize
	}
	return validateEncodedResponse(entry.EncodedResponse)
}

// overlapLess orders entries by PB, SB, request bytes, then source with the
// any-source constraint first, so entries that could overlap are adjacent.
func overlapLess(first *PlanEntry, second *PlanEntry) bool {
	if first.PB != second.PB {
		return first.PB < second.PB
	}
	if first.SB != second.SB {
		return first.SB < second.SB
	}
	if order := bytes.Compare(first.Request, second.Request); order != 0 {
		return order < 0
	}
	if first.Source.Any != second.Source.Any {
		return first.Source.Any
	}
	return first.Source.Source < second.Source.Source
}

// siftDown restores the max-heap property below root within order[:end].
func siftDown(entries []PlanEntry, order []int, root int, end int) {
	for {
		child := 2*root + 1
		if child >= end {
			return
		}
		if child+1 < end && overlapLess(&entries[order[child]], &entries[order[child+1]]) {
			child++
		}
		if !overlapLess(&entries[order[root]], &entries[order[child]]) {
			return
		}
		order[root], order[child] = order[child], order[root]
		root = child
	}
}

// planOverlaps reports whether any two entries could select the same
// request. It sorts a caller-provided index scratch (heapsort, no allocation)
// and compares neighbours, so the cost is n log n rather than all pairs.
func planOverlaps(entries []PlanEntry, order []int) bool {
	order = order[:len(entries)]
	for index := range order {
		order[index] = index
	}
	for start := len(order)/2 - 1; start >= 0; start-- {
		siftDown(entries, order, start, len(order))
	}
	for end := len(order) - 1; end > 0; end-- {
		order[0], order[end] = order[end], order[0]
		siftDown(entries, order, 0, end)
	}
	for index := 1; index < len(order); index++ {
		prior := &entries[order[index-1]]
		next := &entries[order[index]]
		if prior.PB != next.PB || prior.SB != next.SB ||
			!bytes.Equal(prior.Request, next.Request) {
			continue
		}
		// Same key: any-source sorts first, so it overlaps every later neighbour.
		if prior.Source.Any || prior.Source.Source == next.Source.Source {
			return true
		}
	}
	return false
}

// entriesOverlap reports whether one request could select both entries.
func entriesOverlap(first *PlanEntry, second *PlanEntry) bool {
	if first.PB != second.PB || first.SB != second.SB {
		return false
	}
	if string(first.Request) != string(second.Request) {
		return false
	}
	return first.Source.Any || second.Source.Any ||
		first.Source.Source == second.Source.Source
}

// checkPublishable holds at both prepare and publish, so a change during
// writing cannot publish a generation that is already stale.
func (registry *Registry) checkPublishable(identity PlanIdentity, planExpiry Tick, now ClockReading) (*registrationRecord, error) {
	if !registry.workerBound {
		return nil, ErrNoWorker
	}
	if identity.Worker != registry.worker {
		return nil, ErrIdentityMismatch
	}
	if identity.Stream != registry.stream {
		return nil, ErrIdentityMismatch
	}
	record := registry.findExact(identity.Registration)
	if record == nil || record.state != RegistrationActive {
		return nil, ErrNotRegistered
	}
	if identity.Source != record.source {
		return nil, ErrIdentityMismatch
	}
	if registry.conflicts[identity.Registration.Address] {
		return nil, ErrPhysicalConflict
	}
	validity := PlanValidity{
		Domain:             registry.clock.domain,
		PlanExpiry:         planExpiry,
		RegistrationExpiry: record.leaseExpiry,
		SourceLeaseExpiry:  record.sourceLease,
	}
	if err := validity.check(now); err != nil {
		return nil, err
	}
	return record, nil
}

// PreparePlan validates a whole candidate, then copies it into a free slot.
// The slot stays invisible to the worker and earlier generations stay published.
// A full pool refuses without evicting anything. The generation is consumed here:
// an aborted preparation cannot lend its number to different bytes.
func (registry *Registry) PreparePlan(candidate PlanCandidate, now ClockReading) (SlotRef, error) {
	if err := registry.observeClock(now); err != nil {
		return SlotRef{}, err
	}
	if err := registry.validateCandidate(&candidate); err != nil {
		return SlotRef{}, err
	}
	record, err := registry.checkPublishable(candidate.Identity, candidate.PlanExpiry, now)
	if err != nil {
		return SlotRef{}, err
	}
	if err := registry.checkLease(candidate.PlanExpiry, now); err != nil {
		return SlotRef{}, err
	}
	if registry.planTop == math.MaxUint64 {
		return SlotRef{}, ErrCounterExhausted
	}
	if candidate.Identity.Plan <= registry.planTop {
		return SlotRef{}, ErrStaleGeneration
	}
	for index := range registry.pool.slots {
		current := &registry.pool.slots[index]
		if current.state != SlotFree {
			continue
		}
		registry.planTop = candidate.Identity.Plan
		current.state = SlotWriting
		current.identity = candidate.Identity
		current.validity = PlanValidity{
			Domain:             registry.clock.domain,
			PlanExpiry:         candidate.PlanExpiry,
			RegistrationExpiry: record.leaseExpiry,
			SourceLeaseExpiry:  record.sourceLease,
		}
		current.copyEntries(candidate.Entries)
		return current.ref(index), nil
	}
	return SlotRef{}, ErrPoolExhausted
}

// copyEntries detaches the candidate; caller buffers are never retained.
func (current *slot) copyEntries(entries []PlanEntry) {
	cursor := uint32(0)
	for index := range entries {
		entry := &entries[index]
		requestLen := uint32(copy(current.bytes[cursor:], entry.Request))
		replyStart := cursor + requestLen
		replyLen := uint32(copy(current.bytes[replyStart:], entry.EncodedResponse))
		current.entries[index] = entryRecord{
			sourceAny:    entry.Source.Any,
			source:       entry.Source.Source,
			pb:           entry.PB,
			sb:           entry.SB,
			phase:        entry.AllowedPhase,
			requestStart: cursor,
			requestLen:   requestLen,
			replyStart:   replyStart,
			replyLen:     replyLen,
		}
		cursor = replyStart + replyLen
	}
	current.entryCount = uint32(len(entries))
}

// AbortPrepared returns a prepared slot to free. It was never visible, so no
// acknowledgement is owed.
func (registry *Registry) AbortPrepared(ref SlotRef) error {
	current, err := registry.pool.resolve(ref)
	if err != nil {
		return err
	}
	if current.state != SlotWriting {
		return ErrSlotState
	}
	return current.release()
}

// PublishPrepared atomically makes a complete generation current for its
// registration. Its predecessor can no longer be published, but remains retained
// until acknowledged. A candidate gone stale while writing is aborted and replaces
// nothing.
func (registry *Registry) PublishPrepared(ref SlotRef, now ClockReading) (PublishNotice, error) {
	if err := registry.observeClock(now); err != nil {
		return PublishNotice{}, err
	}
	current, err := registry.pool.resolve(ref)
	if err != nil {
		return PublishNotice{}, err
	}
	if current.state != SlotWriting {
		return PublishNotice{}, ErrSlotState
	}
	record, err := registry.checkPublishable(current.identity, current.validity.PlanExpiry, now)
	if err != nil {
		if current.release() != nil {
			current.state = SlotReclaimable
		}
		return PublishNotice{}, err
	}
	if err := current.validity.check(now); err != nil {
		// The frozen deadlines govern, not a lease renewed after preparation.
		if current.release() != nil {
			current.state = SlotReclaimable
		}
		return PublishNotice{}, err
	}
	if current.identity.Plan <= record.publishTop {
		// The high-water mark survives retirement of the current generation.
		if current.release() != nil {
			current.state = SlotReclaimable
		}
		return PublishNotice{}, ErrStaleGeneration
	}
	if record.published >= 0 {
		// Prepared out of order: a later generation is already current, so this
		// older one can never become current. It was never visible, so it is
		// returned like any other stale candidate.
		if current.identity.Plan <= registry.pool.slots[record.published].identity.Plan {
			if current.release() != nil {
				current.state = SlotReclaimable
			}
			return PublishNotice{}, ErrStaleGeneration
		}
		registry.retireSlot(&registry.pool.slots[record.published], RetireSuperseded)
	}
	current.state = SlotPublished
	record.published = int32(ref.Index)
	record.publishTop = current.identity.Plan
	return PublishNotice{Slot: ref, Identity: current.identity}, nil
}

// PendingRevocations appends a notice for each retained slot, up to the
// capacity of dst, without allocating. Notices stay pending until acknowledged,
// so a lost notice or acknowledgement is recovered by delivering it again.
func (registry *Registry) PendingRevocations(dst []RevocationNotice) []RevocationNotice {
	for index := range registry.pool.slots {
		if len(dst) == cap(dst) {
			return dst
		}
		current := &registry.pool.slots[index]
		if current.state != SlotRetained {
			continue
		}
		dst = append(dst, RevocationNotice{
			Slot:     current.ref(index),
			Identity: current.identity,
			Reason:   current.reason,
		})
	}
	return dst
}

// ObserveAcknowledgement moves a retained slot to reclaimable only for the
// exact slot incarnation and plan identity. A stale acknowledgement cannot retire
// a successor or clear another registration.
func (registry *Registry) ObserveAcknowledgement(ack Acknowledgement) error {
	current, err := registry.pool.resolve(ack.Slot)
	if err != nil {
		return err
	}
	if current.state != SlotRetained {
		return ErrSlotState
	}
	if !current.identity.matches(ack.Identity) {
		return ErrIdentityMismatch
	}
	if current.pinned {
		return ErrSlotPinned
	}
	current.state = SlotReclaimable
	registry.settle(current.identity.Registration)
	return nil
}

type workerSlotRecord struct {
	incarnation uint64
	superseded  bool
	revoked     bool
}

// WorkerView is the worker's own view of the pool. It moves no slot state,
// and its admission changes only on what it has itself observed.
type WorkerView struct {
	pool           *Pool
	worker         WorkerID
	stream         StreamGeneration
	clock          clockGuard
	planTop        [addressSpace]PlanGeneration
	transactionTop TransactionOrdinal
	current        [addressSpace]SlotRef
	records        []workerSlotRecord
	pinActive      bool
	pinKey         WireKey
	pinSlot        SlotRef
}

// NewWorkerView creates the view of one worker incarnation and stream.
func NewWorkerView(pool *Pool, worker WorkerID, stream StreamGeneration) (*WorkerView, error) {
	if pool == nil || worker == 0 || stream == 0 {
		return nil, ErrInvalidIdentity
	}
	return &WorkerView{
		pool:    pool,
		worker:  worker,
		stream:  stream,
		records: make([]workerSlotRecord, len(pool.slots)),
	}, nil
}

func (view *WorkerView) record(ref SlotRef) *workerSlotRecord {
	record := &view.records[ref.Index]
	if record.incarnation != ref.Incarnation {
		*record = workerSlotRecord{incarnation: ref.Incarnation}
	}
	return record
}

func (view *WorkerView) holdsPin(ref SlotRef) bool {
	return view.pinActive && view.pinSlot == ref
}

// acknowledge reports readiness only once the slot has no reader.
func (view *WorkerView) acknowledge(ref SlotRef, identity PlanIdentity) (Acknowledgement, bool) {
	if view.holdsPin(ref) {
		return Acknowledgement{}, false
	}
	return Acknowledgement{Slot: ref, Identity: identity}, true
}

// AdoptPlan installs a published generation at a safe boundary. It may return
// an acknowledgement for the predecessor when no transaction holds that slot.
func (view *WorkerView) AdoptPlan(notice PublishNotice, now ClockReading) (Acknowledgement, bool, error) {
	if err := view.clock.observe(now); err != nil {
		return Acknowledgement{}, false, err
	}
	current, err := view.pool.resolve(notice.Slot)
	if err != nil {
		return Acknowledgement{}, false, err
	}
	if current.state != SlotPublished {
		return Acknowledgement{}, false, ErrSlotState
	}
	identity := notice.Identity
	if !current.identity.matches(identity) {
		return Acknowledgement{}, false, ErrIdentityMismatch
	}
	if identity.Worker != view.worker {
		return Acknowledgement{}, false, ErrIdentityMismatch
	}
	if identity.Stream != view.stream {
		return Acknowledgement{}, false, ErrIdentityMismatch
	}
	address := identity.Registration.Address
	if identity.Plan <= view.planTop[address] {
		return Acknowledgement{}, false, ErrStaleGeneration
	}
	if err := current.validity.check(now); err != nil {
		return Acknowledgement{}, false, err
	}
	view.planTop[address] = identity.Plan
	previous := view.current[address]
	view.current[address] = notice.Slot
	if previous.Incarnation == 0 {
		return Acknowledgement{}, false, nil
	}
	old, err := view.pool.resolve(previous)
	if err != nil {
		return Acknowledgement{}, false, nil
	}
	view.record(previous).superseded = true
	ack, ready := view.acknowledge(previous, old.identity)
	return ack, ready, nil
}

// ObserveRevocation is the boundary at which a revocation takes effect for
// this worker. Selection is fenced at once. A superseded plan may finish a selected
// transaction, while any other reason also closes that transaction at its next
// check. The acknowledgement waits until no transaction holds the slot.
func (view *WorkerView) ObserveRevocation(notice RevocationNotice) (Acknowledgement, bool, error) {
	current, err := view.pool.resolve(notice.Slot)
	if err != nil {
		return Acknowledgement{}, false, err
	}
	if current.state != SlotRetained {
		return Acknowledgement{}, false, ErrSlotState
	}
	if !current.identity.matches(notice.Identity) {
		return Acknowledgement{}, false, ErrIdentityMismatch
	}
	if notice.Identity.Worker != view.worker {
		return Acknowledgement{}, false, ErrIdentityMismatch
	}
	if notice.Identity.Stream > view.stream {
		return Acknowledgement{}, false, ErrIdentityMismatch
	}
	record := view.record(notice.Slot)
	if notice.Reason == RetireSuperseded {
		record.superseded = true
	} else {
		record.revoked = true
	}
	address := notice.Identity.Registration.Address
	if view.current[address] == notice.Slot {
		view.current[address] = SlotRef{}
	}
	ack, ready := view.acknowledge(notice.Slot, notice.Identity)
	return ack, ready, nil
}

// AdvanceStream records a gap or resynchronization. Every selection of the
// old stream ends at once: the pin is dropped and no plan can be selected until a
// generation of the new stream is adopted. Plan generations keep advancing.
func (view *WorkerView) AdvanceStream(stream StreamGeneration) error {
	if stream <= view.stream {
		return ErrStaleGeneration
	}
	if view.pinActive {
		view.dropPin()
	}
	view.stream = stream
	view.transactionTop = 0
	view.current = [addressSpace]SlotRef{}
	return nil
}

func (view *WorkerView) dropPin() {
	current := &view.pool.slots[view.pinSlot.Index]
	if current.incarnation == view.pinSlot.Incarnation {
		current.pinned = false
	}
	view.pinActive = false
	view.pinKey = WireKey{}
	view.pinSlot = SlotRef{}
}

// PlanPin holds one generation for one transaction. It is a plain value with
// no view of plan storage: bytes are reached only through checked accessors, so
// nothing mutable escapes and no other generation can be mixed in.
type PlanPin struct {
	view *WorkerView
	key  WireKey
}

// SelectProvisional selects and pins the current plan at destination time. It
// is provisional and authorizes no output. The ordinal is consumed even when
// selection is refused, because it belongs to the observed transaction.
func (view *WorkerView) SelectProvisional(address byte, transaction TransactionOrdinal, now ClockReading) (PlanPin, error) {
	if err := view.clock.observe(now); err != nil {
		return PlanPin{}, err
	}
	if transaction == 0 {
		return PlanPin{}, ErrInvalidIdentity
	}
	if transaction <= view.transactionTop {
		return PlanPin{}, ErrStaleTransaction
	}
	view.transactionTop = transaction
	if view.pinActive {
		return PlanPin{}, ErrPinBusy
	}
	ref := view.current[address]
	if ref.Incarnation == 0 {
		return PlanPin{}, ErrNoPlan
	}
	current, err := view.pool.resolve(ref)
	if err != nil {
		return PlanPin{}, err
	}
	if current.state != SlotPublished && current.state != SlotRetained {
		return PlanPin{}, ErrSlotState
	}
	if err := current.validity.check(now); err != nil {
		return PlanPin{}, err
	}
	key := WireKey{Identity: current.identity, Transaction: transaction}
	current.pinned = true
	view.pinActive = true
	view.pinKey = key
	view.pinSlot = ref
	return PlanPin{view: view, key: key}, nil
}

// Key returns the exact identity frozen at selection.
func (pin PlanPin) Key() WireKey {
	return pin.key
}

// live is the whole admission check, repeated at every boundary.
func (pin PlanPin) live(expect WireKey, now ClockReading) (*slot, error) {
	view := pin.view
	if view == nil {
		return nil, ErrPinReleased
	}
	if err := view.clock.observe(now); err != nil {
		return nil, err
	}
	if !view.pinActive || view.pinKey != pin.key {
		return nil, ErrPinReleased
	}
	if !expect.Identity.matches(pin.key.Identity) {
		return nil, ErrIdentityMismatch
	}
	if expect.Transaction != pin.key.Transaction {
		return nil, ErrIdentityMismatch
	}
	current, err := view.pool.resolve(view.pinSlot)
	if err != nil {
		return nil, err
	}
	if current.state != SlotPublished && current.state != SlotRetained {
		return nil, ErrSlotState
	}
	if view.record(view.pinSlot).revoked {
		return nil, ErrPlanRevoked
	}
	if err := current.validity.check(now); err != nil {
		return nil, err
	}
	return current, nil
}

// Validate rechecks exact keys, observed revocation and every deadline.
func (pin PlanPin) Validate(expect WireKey, now ClockReading) error {
	_, err := pin.live(expect, now)
	return err
}

// RequestQuery is one complete, already framing- and CRC-validated request in
// logical bytes. Framing and CRC validation belong to the wire engine.
type RequestQuery struct {
	Source      byte
	Destination byte
	PB          byte
	SB          byte
	Data        []byte
}

// Match finds the single exact entry of the pinned plan with a bounded scan.
func (pin PlanPin) Match(expect WireKey, query RequestQuery, now ClockReading) (uint32, error) {
	current, err := pin.live(expect, now)
	if err != nil {
		return 0, err
	}
	if query.Destination != current.identity.Registration.Address {
		return 0, ErrNoMatch
	}
	if !isInitiatorAddress(query.Source) {
		return 0, ErrNoMatch
	}
	for index := uint32(0); index < current.entryCount; index++ {
		entry := &current.entries[index]
		if !entry.sourceAny && entry.source != query.Source {
			continue
		}
		if entry.pb != query.PB || entry.sb != query.SB {
			continue
		}
		if uint64(entry.requestLen) != uint64(len(query.Data)) {
			continue
		}
		request := current.bytes[entry.requestStart : entry.requestStart+entry.requestLen]
		if string(request) == string(query.Data) {
			return index, nil
		}
	}
	return 0, ErrNoMatch
}

// ResponseLength returns the encoded length of one entry of the pinned plan.
func (pin PlanPin) ResponseLength(expect WireKey, entry uint32, now ClockReading) (uint32, error) {
	current, err := pin.live(expect, now)
	if err != nil {
		return 0, err
	}
	if entry >= current.entryCount {
		return 0, ErrNoMatch
	}
	return current.entries[entry].replyLen, nil
}

// ResponseSymbol returns one prepared wire symbol after a full recheck at that
// output boundary. This is the only way out for response bytes.
func (pin PlanPin) ResponseSymbol(expect WireKey, entry uint32, offset uint32, phase Phase, now ClockReading) (byte, error) {
	current, err := pin.live(expect, now)
	if err != nil {
		return 0, err
	}
	if entry >= current.entryCount {
		return 0, ErrNoMatch
	}
	record := &current.entries[entry]
	if phase != record.phase {
		return 0, ErrPhase
	}
	if offset >= record.replyLen {
		return 0, ErrPlanOversize
	}
	return current.bytes[record.replyStart+offset], nil
}

// Release ends the transaction's hold. A slot whose retirement this worker
// has already observed yields its acknowledgement here.
func (pin PlanPin) Release() (Acknowledgement, bool, error) {
	view := pin.view
	if view == nil || !view.pinActive || view.pinKey != pin.key {
		return Acknowledgement{}, false, ErrPinReleased
	}
	ref := view.pinSlot
	identity := pin.key.Identity
	record := view.record(ref)
	retired := record.superseded || record.revoked
	view.dropPin()
	if !retired {
		return Acknowledgement{}, false, nil
	}
	ack, ready := view.acknowledge(ref, identity)
	return ack, ready, nil
}
