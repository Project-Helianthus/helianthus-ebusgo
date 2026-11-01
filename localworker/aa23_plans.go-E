// Package localworker models the offline local eBUS responder worker.
//
// Status: Hypothesis. Everything here is a pure, deterministic software model
// over injected time. It opens no device, owns no transport and cannot transmit:
// nothing in this package is reachable from a default configuration, and it adds
// no arming switch. Timing, error rates and platform suitability remain Unknown.
//
// This file freezes the AA23 registration, plan and pool API for its consumers.
// A consumer needing a different API stops rather than editing these types.
//
// The model is serialized: one caller drives an AA23Registry and its
// AA23WorkerView at a time, and the shared-memory lanes and their atomic ordering
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
	aa23errors "errors"
	aa23math "math"
)

const (
	aa23SymbolEscape     byte = 0xA9
	aa23SymbolSyn        byte = 0xAA
	aa23EscapedEscape    byte = 0x00
	aa23EscapedSyn       byte = 0x01
	aa23AddressBroadcast byte = 0xFE
	aa23CRCPolynomial    byte = 0x9B

	aa23AddressSpace       = 256
	aa23MaxNativeLength    = 255
	aa23MaxEncodedResponse = 2 * (1 + aa23MaxNativeLength + 1)

	aa23PoolByteCeiling uint64 = 1 << 26
)

// Refusals are sentinels; a refusal changes no incumbent state unless its
// operation says otherwise.
var (
	ErrAA23InvalidLimits        = aa23errors.New("localworker: invalid limits")
	ErrAA23InvalidIdentity      = aa23errors.New("localworker: zero or malformed identity")
	ErrAA23InvalidAddress       = aa23errors.New("localworker: address not admitted")
	ErrAA23DuplicateAddress     = aa23errors.New("localworker: address already registered")
	ErrAA23RegistrationConflict = aa23errors.New("localworker: same key with different content")
	ErrAA23RegistrationRetiring = aa23errors.New("localworker: predecessor wire authority unsettled")
	ErrAA23NotRegistered        = aa23errors.New("localworker: no active registration for this exact key")
	ErrAA23StaleEpoch           = aa23errors.New("localworker: registration epoch does not advance")
	ErrAA23UnknownSubscriber    = aa23errors.New("localworker: subscriber is not open")
	ErrAA23Capacity             = aa23errors.New("localworker: bounded capacity exhausted")
	ErrAA23PhysicalConflict     = aa23errors.New("localworker: known physical address conflict")
	ErrAA23MasterConflict       = aa23errors.New("localworker: master identity held by another subscriber")
	ErrAA23ClockClosed          = aa23errors.New("localworker: clock regression or incomparable domain closed admission")
	ErrAA23Expired              = aa23errors.New("localworker: lease or plan expired")
	ErrAA23LeaseTooLong         = aa23errors.New("localworker: lease exceeds the configured bound")
	ErrAA23InvalidPlan          = aa23errors.New("localworker: plan candidate is malformed")
	ErrAA23AmbiguousPlan        = aa23errors.New("localworker: plan entries overlap")
	ErrAA23Oversize             = aa23errors.New("localworker: plan exceeds a configured bound")
	ErrAA23Unsupported          = aa23errors.New("localworker: unsupported operation or failure policy")
	ErrAA23PoolExhausted        = aa23errors.New("localworker: no free plan slot")
	ErrAA23StaleGeneration      = aa23errors.New("localworker: generation does not advance")
	ErrAA23CounterExhausted     = aa23errors.New("localworker: counter refuses to wrap")
	ErrAA23IdentityMismatch     = aa23errors.New("localworker: exact identity mismatch")
	ErrAA23SlotState            = aa23errors.New("localworker: slot ownership forbids this transition")
	ErrAA23SlotPinned           = aa23errors.New("localworker: slot is pinned by a transaction")
	ErrAA23NoWorker             = aa23errors.New("localworker: no worker incarnation bound")
	ErrAA23NoPlan               = aa23errors.New("localworker: no adopted plan for this address")
	ErrAA23NoMatch              = aa23errors.New("localworker: request matches no plan entry")
	ErrAA23Revoked              = aa23errors.New("localworker: plan revocation observed")
	ErrAA23PinBusy              = aa23errors.New("localworker: a transaction already holds a pin")
	ErrAA23PinReleased          = aa23errors.New("localworker: pin no longer held")
	ErrAA23StaleTransaction     = aa23errors.New("localworker: transaction ordinal does not advance")
	ErrAA23Phase                = aa23errors.New("localworker: entry does not allow this phase")
)

type (
	// AA23SubscriberID is a host-assigned, never reused subscriber incarnation.
	AA23SubscriberID uint64
	// AA23RegistrationEpoch advances per subscriber and is never reused.
	AA23RegistrationEpoch uint64
	// AA23WorkerID is a worker incarnation; successors inherit no authority.
	AA23WorkerID uint64
	// AA23StreamGeneration names one uninterrupted observation stream of a worker.
	AA23StreamGeneration uint64
	// AA23PlanGeneration names one immutable publication within a worker.
	AA23PlanGeneration uint64
	// AA23SourceGeneration names the source state a plan was compiled from.
	AA23SourceGeneration uint64
	// AA23TransactionOrdinal is assigned at request start within a stream.
	AA23TransactionOrdinal uint64
	// AA23Tick is one injected monotonic clock tick.
	AA23Tick uint64
)

// AA23ClockReading is one injected monotonic reading. Readings pass as values,
// so the model never calls out to a clock. Domain zero is not comparable.
type AA23ClockReading struct {
	AA23Domain uint64
	AA23Tick   AA23Tick
}

// AA23Limits holds the finite fixture parameters. They are bounds for the model,
// never product timing or error acceptance.
type AA23Limits struct {
	AA23MaxSubscribers          uint32
	AA23MaxRegistrations        uint32
	AA23MaxMasters              uint32
	AA23MaxPlanSlots            uint32
	AA23MaxEntriesPerPlan       uint32
	AA23MaxRequestBytes         uint32
	AA23MaxEncodedResponseBytes uint32
	AA23MaxPlanBytes            uint32
	AA23MaxLeaseTicks           AA23Tick
}

func (aa23Limits AA23Limits) aa23Validate() error {
	if aa23Limits.AA23MaxSubscribers == 0 || aa23Limits.AA23MaxRegistrations == 0 ||
		aa23Limits.AA23MaxMasters == 0 || aa23Limits.AA23MaxPlanSlots == 0 ||
		aa23Limits.AA23MaxEntriesPerPlan == 0 || aa23Limits.AA23MaxRequestBytes == 0 ||
		aa23Limits.AA23MaxEncodedResponseBytes == 0 ||
		aa23Limits.AA23MaxPlanBytes == 0 || aa23Limits.AA23MaxLeaseTicks == 0 {
		return ErrAA23InvalidLimits
	}
	if aa23Limits.AA23MaxRegistrations > aa23AddressSpace || aa23Limits.AA23MaxMasters > aa23AddressSpace ||
		aa23Limits.AA23MaxSubscribers > aa23AddressSpace {
		return ErrAA23InvalidLimits
	}
	if aa23Limits.AA23MaxRequestBytes > aa23MaxNativeLength ||
		aa23Limits.AA23MaxEncodedResponseBytes > aa23MaxEncodedResponse {
		return ErrAA23InvalidLimits
	}
	if uint64(aa23Limits.AA23MaxPlanBytes) < uint64(aa23Limits.AA23MaxRequestBytes)+uint64(aa23Limits.AA23MaxEncodedResponseBytes) {
		return ErrAA23InvalidLimits
	}
	aa23EntryBytes := uint64(aa23Limits.AA23MaxEntriesPerPlan) * aa23EntryAccountingBytes
	aa23SlotBytes := uint64(aa23Limits.AA23MaxPlanBytes) + aa23EntryBytes
	if aa23SlotBytes < aa23EntryBytes || aa23SlotBytes > aa23PoolByteCeiling ||
		uint64(aa23Limits.AA23MaxPlanSlots) > aa23PoolByteCeiling/aa23SlotBytes {
		return ErrAA23InvalidLimits
	}
	return nil
}

const aa23EntryAccountingBytes uint64 = 64

// AA23RegistrationKey compares as one complete tuple, never by address alone.
type AA23RegistrationKey struct {
	AA23Subscriber AA23SubscriberID
	AA23Address    byte
	AA23Epoch      AA23RegistrationEpoch
}

func (aa23Key AA23RegistrationKey) aa23Matches(aa23Other AA23RegistrationKey) bool {
	if aa23Key.AA23Subscriber != aa23Other.AA23Subscriber {
		return false
	}
	if aa23Key.AA23Address != aa23Other.AA23Address {
		return false
	}
	return aa23Key.AA23Epoch == aa23Other.AA23Epoch
}

// AA23FailureAction states what the responder does in a failure class. Refusal
// is not a universal NACK rule, so only silence is admitted until a NACK action
// has native phase support and independent evidence.
type AA23FailureAction uint8

const (
	AA23FailureUnspecified AA23FailureAction = iota
	AA23FailureSilence
	AA23FailureNACK
)

// AA23FailurePolicy must name every failure class explicitly.
type AA23FailurePolicy struct {
	AA23Unavailable AA23FailureAction
	AA23Malformed   AA23FailureAction
	AA23Timeout     AA23FailureAction
}

func (aa23Policy AA23FailurePolicy) aa23Validate() error {
	for _, aa23Action := range [...]AA23FailureAction{aa23Policy.AA23Unavailable, aa23Policy.AA23Malformed, aa23Policy.AA23Timeout} {
		if aa23Action != AA23FailureSilence {
			return ErrAA23Unsupported
		}
	}
	return nil
}

// AA23Registration is the detached content of one registration. It grants a
// target address only: no master identity, arbitration or general transmit right.
type AA23Registration struct {
	AA23Key               AA23RegistrationKey
	AA23LeaseExpiry       AA23Tick
	AA23Source            AA23SourceGeneration
	AA23SourceLeaseExpiry AA23Tick
	AA23Failure           AA23FailurePolicy
}

// AA23RegistrationState is the lifecycle of one registration key.
type AA23RegistrationState uint8

const (
	AA23RegistrationAbsent AA23RegistrationState = iota
	AA23RegistrationActive
	// AA23RegistrationRetiring holds the address until every plan of the key has
	// been acknowledged or its worker's termination has been observed.
	AA23RegistrationRetiring
)

// AA23RegistrationView is a detached snapshot of one registration.
type AA23RegistrationView struct {
	AA23Registered        AA23Registration
	AA23State             AA23RegistrationState
	AA23LeaseExpiry       AA23Tick
	AA23Source            AA23SourceGeneration
	AA23SourceLeaseExpiry AA23Tick
}

// AA23PlanIdentity is the complete identity of one published plan.
type AA23PlanIdentity struct {
	AA23Registration AA23RegistrationKey
	AA23Worker       AA23WorkerID
	AA23Stream       AA23StreamGeneration
	AA23Plan         AA23PlanGeneration
	AA23Source       AA23SourceGeneration
}

func (aa23Identity AA23PlanIdentity) aa23Matches(aa23Other AA23PlanIdentity) bool {
	if !aa23Identity.AA23Registration.aa23Matches(aa23Other.AA23Registration) {
		return false
	}
	if aa23Identity.AA23Worker != aa23Other.AA23Worker {
		return false
	}
	if aa23Identity.AA23Stream != aa23Other.AA23Stream {
		return false
	}
	if aa23Identity.AA23Plan != aa23Other.AA23Plan {
		return false
	}
	return aa23Identity.AA23Source == aa23Other.AA23Source
}

// AA23WireKey is the identity a pinned transaction presents at every boundary.
type AA23WireKey struct {
	AA23Identity    AA23PlanIdentity
	AA23Transaction AA23TransactionOrdinal
}

// AA23Operation classifies an entry. Only reads can be installed.
type AA23Operation uint8

const (
	AA23OperationUnspecified AA23Operation = iota
	AA23OperationRead
	AA23OperationStateful
)

// AA23Phase names the native transaction phase an entry may answer in.
type AA23Phase uint8

const (
	AA23PhaseNone AA23Phase = iota
	AA23PhaseRequest
	AA23PhaseTargetACK
	AA23PhaseResponse
	AA23PhaseInitiatorACK
	AA23PhaseEnded
)

// AA23SourceConstraint restricts the initiator of a request. Any and an exact
// source are mutually exclusive.
type AA23SourceConstraint struct {
	AA23Any    bool
	AA23Source byte
}

// AA23PlanEntry is one exact match and its prepared response. AA23Request holds
// logical request data. AA23EncodedResponse holds the wire form of the native
// response after the target ACK: LEN DATA CRC, escaped; see AA23EncodeResponse.
type AA23PlanEntry struct {
	AA23Source          AA23SourceConstraint
	AA23Destination     byte
	AA23PB              byte
	AA23SB              byte
	AA23RequestLength   uint32
	AA23Request         []byte
	AA23Operation       AA23Operation
	AA23AllowedPhase    AA23Phase
	AA23MaxEncodedBytes uint32
	AA23EncodedResponse []byte
}

// AA23PlanCandidate is the detached input of one publication. The registration
// and source leases are bound from the registry, never from the candidate.
type AA23PlanCandidate struct {
	AA23Identity   AA23PlanIdentity
	AA23PlanExpiry AA23Tick
	AA23Failure    AA23FailurePolicy
	AA23Entries    []AA23PlanEntry
}

// AA23PlanValidity holds the exclusive deadlines frozen into a published plan.
// A later renewal never extends them; it needs a fresh plan generation.
type AA23PlanValidity struct {
	AA23PlanExpiry         AA23Tick
	AA23RegistrationExpiry AA23Tick
	AA23SourceLeaseExpiry  AA23Tick
}

// aa23Check admits only strictly before each deadline: equality is expired.
func (aa23Validity AA23PlanValidity) aa23Check(aa23Now AA23ClockReading) error {
	if aa23Now.AA23Tick >= aa23Validity.AA23PlanExpiry {
		return ErrAA23Expired
	}
	if aa23Now.AA23Tick >= aa23Validity.AA23RegistrationExpiry {
		return ErrAA23Expired
	}
	if aa23Now.AA23Tick >= aa23Validity.AA23SourceLeaseExpiry {
		return ErrAA23Expired
	}
	return nil
}

// AA23SlotState is the ownership of one pool slot. Only the host moves it.
type AA23SlotState uint8

const (
	AA23SlotInvalid AA23SlotState = iota
	// AA23SlotFree is host-owned, holds nothing and can start a new incarnation.
	AA23SlotFree
	// AA23SlotWriting is host-owned and invisible to the worker.
	AA23SlotWriting
	// AA23SlotPublished is complete and immutable, and may be adopted.
	AA23SlotPublished
	// AA23SlotRetained is retired, but the worker may still hold a reference.
	AA23SlotRetained
	// AA23SlotReclaimable has proof that no reader remains.
	AA23SlotReclaimable
)

// AA23SlotRef names one incarnation of a slot; reuse invalidates older refs.
type AA23SlotRef struct {
	AA23Index       uint32
	AA23Incarnation uint64
}

// AA23SlotCounts is a detached ownership census of the pool.
type AA23SlotCounts struct {
	AA23Free        uint32
	AA23Writing     uint32
	AA23Published   uint32
	AA23Retained    uint32
	AA23Reclaimable uint32
}

// AA23RetireReason ranks why a slot left publication. A stronger reason replaces
// a weaker one so a re-delivered notice can only tighten the worker's view.
type AA23RetireReason uint8

const (
	AA23RetireNone AA23RetireReason = iota
	// AA23RetireSuperseded stops new selection; a selected transaction may finish.
	AA23RetireSuperseded
	// AA23RetireExpired closes admission from the plan.
	AA23RetireExpired
	// AA23RetireRevoked closes admission, including for a selected transaction.
	AA23RetireRevoked
)

// AA23PublishNotice tells the worker about one complete generation.
type AA23PublishNotice struct {
	AA23Slot     AA23SlotRef
	AA23Identity AA23PlanIdentity
}

// AA23RevocationNotice tells the worker that one plan left publication. Sending
// it proves nothing: the host retains the slot until an acknowledgement.
type AA23RevocationNotice struct {
	AA23Slot     AA23SlotRef
	AA23Identity AA23PlanIdentity
	AA23Reason   AA23RetireReason
}

// AA23Acknowledgement states that selection of this exact plan is fenced and no
// reader remains.
type AA23Acknowledgement struct {
	AA23Slot     AA23SlotRef
	AA23Identity AA23PlanIdentity
}

type aa23EntryRecord struct {
	aa23SourceAny    bool
	aa23Source       byte
	aa23PB           byte
	aa23SB           byte
	aa23Phase        AA23Phase
	aa23RequestStart uint32
	aa23RequestLen   uint32
	aa23ReplyStart   uint32
	aa23ReplyLen     uint32
}

type aa23Slot struct {
	aa23State       AA23SlotState
	aa23Incarnation uint64
	aa23Identity    AA23PlanIdentity
	aa23Validity    AA23PlanValidity
	aa23Reason      AA23RetireReason
	aa23Pinned      bool
	aa23EntryCount  uint32
	aa23Entries     []aa23EntryRecord
	aa23Bytes       []byte
}

// AA23Pool is the preallocated plan storage. All slot storage exists from
// construction, and a slot returns to free only through an observed proof.
type AA23Pool struct {
	aa23Slots []aa23Slot
}

func aa23NewPool(aa23Limits AA23Limits) *AA23Pool {
	aa23Pool := &AA23Pool{aa23Slots: make([]aa23Slot, aa23Limits.AA23MaxPlanSlots)}
	for aa23Index := range aa23Pool.aa23Slots {
		aa23Pool.aa23Slots[aa23Index] = aa23Slot{
			aa23State:       AA23SlotFree,
			aa23Incarnation: 1,
			aa23Entries:     make([]aa23EntryRecord, aa23Limits.AA23MaxEntriesPerPlan),
			aa23Bytes:       make([]byte, aa23Limits.AA23MaxPlanBytes),
		}
	}
	return aa23Pool
}

func (aa23Pool *AA23Pool) aa23Resolve(aa23Ref AA23SlotRef) (*aa23Slot, error) {
	if uint64(aa23Ref.AA23Index) >= uint64(len(aa23Pool.aa23Slots)) {
		return nil, ErrAA23SlotState
	}
	aa23Current := &aa23Pool.aa23Slots[aa23Ref.AA23Index]
	if aa23Current.aa23Incarnation != aa23Ref.AA23Incarnation {
		return nil, ErrAA23SlotState
	}
	return aa23Current, nil
}

// AA23SlotState returns the current ownership of a slot index.
func (aa23Pool *AA23Pool) AA23SlotState(aa23Index uint32) AA23SlotState {
	if uint64(aa23Index) >= uint64(len(aa23Pool.aa23Slots)) {
		return AA23SlotInvalid
	}
	return aa23Pool.aa23Slots[aa23Index].aa23State
}

// AA23Counts returns a census of every slot.
func (aa23Pool *AA23Pool) AA23Counts() AA23SlotCounts {
	var aa23Counts AA23SlotCounts
	for aa23Index := range aa23Pool.aa23Slots {
		switch aa23Pool.aa23Slots[aa23Index].aa23State {
		case AA23SlotFree:
			aa23Counts.AA23Free++
		case AA23SlotWriting:
			aa23Counts.AA23Writing++
		case AA23SlotPublished:
			aa23Counts.AA23Published++
		case AA23SlotRetained:
			aa23Counts.AA23Retained++
		case AA23SlotReclaimable:
			aa23Counts.AA23Reclaimable++
		}
	}
	return aa23Counts
}

// AA23Reclaim moves a proven reclaimable slot to free under a new incarnation.
// An incarnation that cannot advance retires the slot rather than wrapping, so
// capacity shrinks instead of an old reference becoming valid again.
func (aa23Pool *AA23Pool) AA23Reclaim(aa23Ref AA23SlotRef) error {
	aa23Current, aa23Err := aa23Pool.aa23Resolve(aa23Ref)
	if aa23Err != nil {
		return aa23Err
	}
	if aa23Current.aa23State != AA23SlotReclaimable {
		return ErrAA23SlotState
	}
	return aa23Current.aa23Release()
}

// AA23PendingReclaims appends each reclaimable slot, up to the capacity of dst,
// without allocating. A slot whose incarnation cannot advance stays listed.
func (aa23Pool *AA23Pool) AA23PendingReclaims(aa23Dst []AA23SlotRef) []AA23SlotRef {
	for aa23Index := range aa23Pool.aa23Slots {
		if len(aa23Dst) == cap(aa23Dst) {
			return aa23Dst
		}
		aa23Current := &aa23Pool.aa23Slots[aa23Index]
		if aa23Current.aa23State == AA23SlotReclaimable {
			aa23Dst = append(aa23Dst, aa23Current.aa23Ref(aa23Index))
		}
	}
	return aa23Dst
}

func (aa23Current *aa23Slot) aa23Release() error {
	if aa23Current.aa23Incarnation == aa23math.MaxUint64 {
		return ErrAA23CounterExhausted
	}
	aa23Current.aa23Incarnation++
	aa23Current.aa23State = AA23SlotFree
	aa23Current.aa23Identity = AA23PlanIdentity{}
	aa23Current.aa23Validity = AA23PlanValidity{}
	aa23Current.aa23Reason = AA23RetireNone
	aa23Current.aa23EntryCount = 0
	return nil
}

func (aa23Current *aa23Slot) aa23Ref(aa23Index int) AA23SlotRef {
	return AA23SlotRef{AA23Index: uint32(aa23Index), AA23Incarnation: aa23Current.aa23Incarnation}
}

type aa23ClockGuard struct {
	aa23Started bool
	aa23Closed  bool
	aa23Domain  uint64
	aa23Last    AA23Tick
}

// aa23Observe latches closed on any regression or incomparable domain. Wall
// clock is never substituted and only a new incarnation reopens admission.
func (aa23Guard *aa23ClockGuard) aa23Observe(aa23Now AA23ClockReading) error {
	if aa23Guard.aa23Closed {
		return ErrAA23ClockClosed
	}
	if aa23Now.AA23Domain == 0 {
		aa23Guard.aa23Closed = true
		return ErrAA23ClockClosed
	}
	if !aa23Guard.aa23Started {
		aa23Guard.aa23Started = true
		aa23Guard.aa23Domain = aa23Now.AA23Domain
		aa23Guard.aa23Last = aa23Now.AA23Tick
		return nil
	}
	if aa23Now.AA23Domain != aa23Guard.aa23Domain || aa23Now.AA23Tick < aa23Guard.aa23Last {
		aa23Guard.aa23Closed = true
		return ErrAA23ClockClosed
	}
	aa23Guard.aa23Last = aa23Now.AA23Tick
	return nil
}

func aa23IsInitiatorNibble(aa23Nibble byte) bool {
	switch aa23Nibble {
	case 0x0, 0x1, 0x3, 0x7, 0xF:
		return true
	}
	return false
}

func aa23IsInitiatorAddress(aa23Address byte) bool {
	return aa23IsInitiatorNibble(aa23Address>>4) && aa23IsInitiatorNibble(aa23Address&0x0F)
}

// aa23IsTargetAddress admits an initiator/target destination: no broadcast,
// initiator pattern, escape or SYN.
func aa23IsTargetAddress(aa23Address byte) bool {
	switch aa23Address {
	case aa23AddressBroadcast, aa23SymbolEscape, aa23SymbolSyn:
		return false
	}
	return !aa23IsInitiatorAddress(aa23Address)
}

func aa23CRCStep(aa23CRC byte, aa23Value byte) byte {
	for aa23Bit := 0; aa23Bit < 8; aa23Bit++ {
		if aa23CRC&0x80 != 0 {
			aa23CRC = aa23CRC<<1 ^ aa23CRCPolynomial
		} else {
			aa23CRC <<= 1
		}
	}
	return aa23CRC ^ aa23Value
}

// aa23CRCUpdate feeds one logical byte in its wire-expanded form.
func aa23CRCUpdate(aa23CRC byte, aa23Value byte) byte {
	switch aa23Value {
	case aa23SymbolEscape:
		return aa23CRCStep(aa23CRCStep(aa23CRC, aa23SymbolEscape), aa23EscapedEscape)
	case aa23SymbolSyn:
		return aa23CRCStep(aa23CRCStep(aa23CRC, aa23SymbolEscape), aa23EscapedSyn)
	}
	return aa23CRCStep(aa23CRC, aa23Value)
}

func aa23AppendEscaped(aa23Dst []byte, aa23Value byte) []byte {
	switch aa23Value {
	case aa23SymbolEscape:
		return append(aa23Dst, aa23SymbolEscape, aa23EscapedEscape)
	case aa23SymbolSyn:
		return append(aa23Dst, aa23SymbolEscape, aa23EscapedSyn)
	}
	return append(aa23Dst, aa23Value)
}

// AA23EncodeResponse appends the native target response for logical data to dst:
// LEN DATA CRC with CRC over LEN DATA, then escaping over every byte.
func AA23EncodeResponse(aa23Dst []byte, aa23Data []byte) ([]byte, error) {
	if len(aa23Data) > aa23MaxNativeLength {
		return aa23Dst, ErrAA23Oversize
	}
	aa23Length := byte(len(aa23Data))
	aa23CRC := aa23CRCUpdate(0, aa23Length)
	aa23Dst = aa23AppendEscaped(aa23Dst, aa23Length)
	for _, aa23Value := range aa23Data {
		aa23CRC = aa23CRCUpdate(aa23CRC, aa23Value)
		aa23Dst = aa23AppendEscaped(aa23Dst, aa23Value)
	}
	return aa23AppendEscaped(aa23Dst, aa23CRC), nil
}

// aa23ValidateEncodedResponse proves the wire form holds exactly one native
// response: no raw SYN, only complete escape pairs, a matching length and CRC.
func aa23ValidateEncodedResponse(aa23Wire []byte) error {
	var aa23CRC byte
	aa23Decoded := 0
	aa23Declared := -1
	aa23ReceivedCRC := -1
	for aa23Offset := 0; aa23Offset < len(aa23Wire); aa23Offset++ {
		aa23Value := aa23Wire[aa23Offset]
		switch aa23Value {
		case aa23SymbolSyn:
			return ErrAA23InvalidPlan
		case aa23SymbolEscape:
			aa23Offset++
			if aa23Offset >= len(aa23Wire) {
				return ErrAA23InvalidPlan
			}
			switch aa23Wire[aa23Offset] {
			case aa23EscapedEscape:
				aa23Value = aa23SymbolEscape
			case aa23EscapedSyn:
				aa23Value = aa23SymbolSyn
			default:
				return ErrAA23InvalidPlan
			}
		}
		if aa23Decoded == 0 {
			aa23Declared = int(aa23Value)
		}
		if aa23Decoded == aa23Declared+1 {
			aa23ReceivedCRC = int(aa23Value)
		} else {
			aa23CRC = aa23CRCUpdate(aa23CRC, aa23Value)
		}
		aa23Decoded++
	}
	if aa23Declared < 0 || aa23Decoded != aa23Declared+2 || aa23ReceivedCRC != int(aa23CRC) {
		return ErrAA23InvalidPlan
	}
	return nil
}

type aa23RegistrationRecord struct {
	aa23Used        bool
	aa23State       AA23RegistrationState
	aa23Registered  AA23Registration
	aa23LeaseExpiry AA23Tick
	aa23Source      AA23SourceGeneration
	aa23SourceLease AA23Tick
	aa23Published   int32
}

type aa23SubscriberRecord struct {
	aa23ID            AA23SubscriberID
	aa23Open          bool
	aa23EpochHigh     AA23RegistrationEpoch
	aa23Registrations uint32
}

// AA23Registry is the host side: registrations, master identities, known
// conflicts and the plan publisher. Every table is sized at construction.
type AA23Registry struct {
	aa23Limits        AA23Limits
	aa23Pool          *AA23Pool
	aa23Clock         aa23ClockGuard
	aa23SubscriberTop AA23SubscriberID
	aa23Subscribers   []aa23SubscriberRecord
	aa23Records       []aa23RegistrationRecord
	aa23ByAddress     [aa23AddressSpace]int32
	aa23Masters       [aa23AddressSpace]AA23SubscriberID
	aa23MasterCount   uint32
	aa23Conflicts     [aa23AddressSpace]bool
	aa23WorkerBound   bool
	aa23Worker        AA23WorkerID
	aa23Stream        AA23StreamGeneration
	aa23WorkerTop     AA23WorkerID
	aa23PlanTop       AA23PlanGeneration
}

// AA23NewRegistry validates limits before any effect. aa23SubscriberFloor is the
// highest subscriber identity a predecessor may have used, zero when none did.
func AA23NewRegistry(aa23Limits AA23Limits, aa23SubscriberFloor AA23SubscriberID) (*AA23Registry, error) {
	if aa23Err := aa23Limits.aa23Validate(); aa23Err != nil {
		return nil, aa23Err
	}
	return &AA23Registry{
		aa23Limits:        aa23Limits,
		aa23Pool:          aa23NewPool(aa23Limits),
		aa23SubscriberTop: aa23SubscriberFloor,
		aa23Subscribers:   make([]aa23SubscriberRecord, aa23Limits.AA23MaxSubscribers),
		aa23Records:       make([]aa23RegistrationRecord, aa23Limits.AA23MaxRegistrations),
	}, nil
}

// AA23Pool returns the plan pool shared with worker views.
func (aa23Registry *AA23Registry) AA23Pool() *AA23Pool {
	return aa23Registry.aa23Pool
}

// aa23ObserveClock closes admission and retires every plan on a clock fault.
func (aa23Registry *AA23Registry) aa23ObserveClock(aa23Now AA23ClockReading) error {
	aa23WasClosed := aa23Registry.aa23Clock.aa23Closed
	aa23Err := aa23Registry.aa23Clock.aa23Observe(aa23Now)
	if aa23Err != nil && !aa23WasClosed {
		aa23Registry.aa23RetireWhere(AA23RetireRevoked, func(*aa23Slot) bool { return true })
	}
	return aa23Err
}

// aa23RetireWhere applies only model-internal predicates, never caller code.
func (aa23Registry *AA23Registry) aa23RetireWhere(aa23Reason AA23RetireReason, aa23Selected func(*aa23Slot) bool) {
	for aa23Index := range aa23Registry.aa23Pool.aa23Slots {
		aa23Current := &aa23Registry.aa23Pool.aa23Slots[aa23Index]
		if aa23Current.aa23State == AA23SlotFree || aa23Current.aa23State == AA23SlotReclaimable {
			continue
		}
		if aa23Selected(aa23Current) {
			aa23Registry.aa23RetireSlot(aa23Current, aa23Reason)
		}
	}
}

func (aa23Registry *AA23Registry) aa23RetireSlot(aa23Current *aa23Slot, aa23Reason AA23RetireReason) {
	switch aa23Current.aa23State {
	case AA23SlotWriting:
		// Never visible to a worker, so no acknowledgement can be owed. An
		// exhausted incarnation stays out of service rather than wrapping.
		if aa23Current.aa23Release() != nil {
			aa23Current.aa23State = AA23SlotReclaimable
		}
	case AA23SlotPublished:
		aa23Current.aa23State = AA23SlotRetained
		aa23Current.aa23Reason = aa23Reason
		aa23Record := aa23Registry.aa23FindExact(aa23Current.aa23Identity.AA23Registration)
		if aa23Record != nil {
			aa23Record.aa23Published = -1
		}
	case AA23SlotRetained:
		if aa23Reason > aa23Current.aa23Reason {
			aa23Current.aa23Reason = aa23Reason
		}
	}
}

// aa23FindExact resolves a complete key; an address alone identifies nothing.
func (aa23Registry *AA23Registry) aa23FindExact(aa23Key AA23RegistrationKey) *aa23RegistrationRecord {
	aa23Index := aa23Registry.aa23ByAddress[aa23Key.AA23Address]
	if aa23Index == 0 {
		return nil
	}
	aa23Record := &aa23Registry.aa23Records[aa23Index-1]
	if !aa23Record.aa23Registered.AA23Key.aa23Matches(aa23Key) {
		return nil
	}
	return aa23Record
}

func (aa23Registry *AA23Registry) aa23FindSubscriber(aa23ID AA23SubscriberID) *aa23SubscriberRecord {
	if aa23ID == 0 {
		return nil
	}
	for aa23Index := range aa23Registry.aa23Subscribers {
		if aa23Registry.aa23Subscribers[aa23Index].aa23ID == aa23ID {
			return &aa23Registry.aa23Subscribers[aa23Index]
		}
	}
	return nil
}

// AA23OpenSubscriber allocates a new, never reused subscriber incarnation.
func (aa23Registry *AA23Registry) AA23OpenSubscriber() (AA23SubscriberID, error) {
	if aa23Registry.aa23SubscriberTop == aa23math.MaxUint64 {
		return 0, ErrAA23CounterExhausted
	}
	for aa23Index := range aa23Registry.aa23Subscribers {
		aa23Record := &aa23Registry.aa23Subscribers[aa23Index]
		if aa23Record.aa23ID != 0 {
			continue
		}
		aa23Registry.aa23SubscriberTop++
		*aa23Record = aa23SubscriberRecord{aa23ID: aa23Registry.aa23SubscriberTop, aa23Open: true}
		return aa23Record.aa23ID, nil
	}
	return 0, ErrAA23Capacity
}

// aa23CheckLease bounds a lease by subtraction, which the first check keeps from
// underflowing, so no deadline addition can overflow.
func (aa23Registry *AA23Registry) aa23CheckLease(aa23Expiry AA23Tick, aa23Now AA23ClockReading) error {
	if aa23Now.AA23Tick >= aa23Expiry {
		return ErrAA23Expired
	}
	if aa23Expiry-aa23Now.AA23Tick > aa23Registry.aa23Limits.AA23MaxLeaseTicks {
		return ErrAA23LeaseTooLong
	}
	return nil
}

// AA23Register admits one target address for one accountable subscriber. An exact
// retry returns the current state and allocates nothing; any other claim on an
// occupied address is refused and leaves the incumbent untouched, including a
// later epoch of the same subscriber. There is no last-writer-wins selection.
func (aa23Registry *AA23Registry) AA23Register(aa23Candidate AA23Registration, aa23Now AA23ClockReading) (AA23RegistrationView, error) {
	if aa23Err := aa23Registry.aa23ObserveClock(aa23Now); aa23Err != nil {
		return AA23RegistrationView{}, aa23Err
	}
	aa23Key := aa23Candidate.AA23Key
	if aa23Key.AA23Subscriber == 0 || aa23Key.AA23Epoch == 0 || aa23Candidate.AA23Source == 0 {
		return AA23RegistrationView{}, ErrAA23InvalidIdentity
	}
	if !aa23IsTargetAddress(aa23Key.AA23Address) {
		return AA23RegistrationView{}, ErrAA23InvalidAddress
	}
	if aa23Occupant := aa23Registry.aa23ByAddress[aa23Key.AA23Address]; aa23Occupant != 0 {
		aa23Record := &aa23Registry.aa23Records[aa23Occupant-1]
		if !aa23Record.aa23Registered.AA23Key.aa23Matches(aa23Key) {
			return AA23RegistrationView{}, ErrAA23DuplicateAddress
		}
		if aa23Record.aa23Registered != aa23Candidate {
			return AA23RegistrationView{}, ErrAA23RegistrationConflict
		}
		if aa23Record.aa23State != AA23RegistrationActive {
			return AA23RegistrationView{}, ErrAA23RegistrationRetiring
		}
		return aa23Record.aa23View(), nil
	}
	aa23Subscriber := aa23Registry.aa23FindSubscriber(aa23Key.AA23Subscriber)
	if aa23Subscriber == nil || !aa23Subscriber.aa23Open {
		return AA23RegistrationView{}, ErrAA23UnknownSubscriber
	}
	if aa23Registry.aa23Conflicts[aa23Key.AA23Address] {
		return AA23RegistrationView{}, ErrAA23PhysicalConflict
	}
	if aa23Subscriber.aa23EpochHigh == aa23math.MaxUint64 {
		return AA23RegistrationView{}, ErrAA23CounterExhausted
	}
	if aa23Key.AA23Epoch <= aa23Subscriber.aa23EpochHigh {
		return AA23RegistrationView{}, ErrAA23StaleEpoch
	}
	if aa23Err := aa23Candidate.AA23Failure.aa23Validate(); aa23Err != nil {
		return AA23RegistrationView{}, aa23Err
	}
	if aa23Err := aa23Registry.aa23CheckLease(aa23Candidate.AA23LeaseExpiry, aa23Now); aa23Err != nil {
		return AA23RegistrationView{}, aa23Err
	}
	if aa23Err := aa23Registry.aa23CheckLease(aa23Candidate.AA23SourceLeaseExpiry, aa23Now); aa23Err != nil {
		return AA23RegistrationView{}, aa23Err
	}
	for aa23Index := range aa23Registry.aa23Records {
		aa23Record := &aa23Registry.aa23Records[aa23Index]
		if aa23Record.aa23Used {
			continue
		}
		*aa23Record = aa23RegistrationRecord{
			aa23Used:        true,
			aa23State:       AA23RegistrationActive,
			aa23Registered:  aa23Candidate,
			aa23LeaseExpiry: aa23Candidate.AA23LeaseExpiry,
			aa23Source:      aa23Candidate.AA23Source,
			aa23SourceLease: aa23Candidate.AA23SourceLeaseExpiry,
			aa23Published:   -1,
		}
		aa23Registry.aa23ByAddress[aa23Key.AA23Address] = int32(aa23Index) + 1
		aa23Subscriber.aa23EpochHigh = aa23Key.AA23Epoch
		aa23Subscriber.aa23Registrations++
		return aa23Record.aa23View(), nil
	}
	return AA23RegistrationView{}, ErrAA23Capacity
}

func (aa23Record *aa23RegistrationRecord) aa23View() AA23RegistrationView {
	return AA23RegistrationView{
		AA23Registered:        aa23Record.aa23Registered,
		AA23State:             aa23Record.aa23State,
		AA23LeaseExpiry:       aa23Record.aa23LeaseExpiry,
		AA23Source:            aa23Record.aa23Source,
		AA23SourceLeaseExpiry: aa23Record.aa23SourceLease,
	}
}

// AA23Registration returns a detached snapshot for an exact key.
func (aa23Registry *AA23Registry) AA23Registration(aa23Key AA23RegistrationKey) AA23RegistrationView {
	aa23Record := aa23Registry.aa23FindExact(aa23Key)
	if aa23Record == nil {
		return AA23RegistrationView{}
	}
	return aa23Record.aa23View()
}

// aa23Retire moves a registration to retiring and settles it when no plan of the
// key can still be referenced.
func (aa23Registry *AA23Registry) aa23Retire(aa23Record *aa23RegistrationRecord, aa23Reason AA23RetireReason) {
	aa23Key := aa23Record.aa23Registered.AA23Key
	aa23Record.aa23State = AA23RegistrationRetiring
	aa23Registry.aa23RetireWhere(aa23Reason, func(aa23Current *aa23Slot) bool {
		return aa23Current.aa23Identity.AA23Registration.aa23Matches(aa23Key)
	})
	aa23Registry.aa23Settle(aa23Key)
}

// aa23Settle frees the address only once no plan of a retiring key remains.
func (aa23Registry *AA23Registry) aa23Settle(aa23Key AA23RegistrationKey) {
	aa23Record := aa23Registry.aa23FindExact(aa23Key)
	if aa23Record == nil || aa23Record.aa23State != AA23RegistrationRetiring {
		return
	}
	for aa23Index := range aa23Registry.aa23Pool.aa23Slots {
		aa23Current := &aa23Registry.aa23Pool.aa23Slots[aa23Index]
		if aa23Current.aa23State != AA23SlotPublished && aa23Current.aa23State != AA23SlotRetained {
			continue
		}
		if aa23Current.aa23Identity.AA23Registration.aa23Matches(aa23Key) {
			return
		}
	}
	aa23Registry.aa23ByAddress[aa23Key.AA23Address] = 0
	*aa23Record = aa23RegistrationRecord{}
	aa23Subscriber := aa23Registry.aa23FindSubscriber(aa23Key.AA23Subscriber)
	if aa23Subscriber == nil {
		return
	}
	aa23Subscriber.aa23Registrations--
	aa23Registry.aa23ForgetClosedSubscriber(aa23Subscriber)
}

// aa23ForgetClosedSubscriber frees a table row; the identity itself is not reused.
func (aa23Registry *AA23Registry) aa23ForgetClosedSubscriber(aa23Subscriber *aa23SubscriberRecord) {
	if aa23Subscriber.aa23Open || aa23Subscriber.aa23Registrations != 0 {
		return
	}
	*aa23Subscriber = aa23SubscriberRecord{}
}

// AA23Unregister retires an exact key and returns its resulting state. Repeating
// it is harmless, and a stale epoch or another subscriber's key removes nothing.
func (aa23Registry *AA23Registry) AA23Unregister(aa23Key AA23RegistrationKey) AA23RegistrationState {
	aa23Record := aa23Registry.aa23FindExact(aa23Key)
	if aa23Record == nil {
		return AA23RegistrationAbsent
	}
	if aa23Record.aa23State == AA23RegistrationActive {
		aa23Registry.aa23Retire(aa23Record, AA23RetireRevoked)
	}
	return aa23Registry.AA23Registration(aa23Key).AA23State
}

// AA23DisconnectSubscriber retires every registration and master identity of a
// subscriber incarnation. The identity is closed for good; repeating is harmless.
func (aa23Registry *AA23Registry) AA23DisconnectSubscriber(aa23ID AA23SubscriberID) {
	aa23Subscriber := aa23Registry.aa23FindSubscriber(aa23ID)
	if aa23Subscriber == nil {
		return
	}
	aa23Subscriber.aa23Open = false
	aa23Registry.aa23ReleaseMastersWhere(aa23ID)
	for aa23Index := range aa23Registry.aa23Records {
		aa23Record := &aa23Registry.aa23Records[aa23Index]
		if aa23Record.aa23Used && aa23Record.aa23State == AA23RegistrationActive &&
			aa23Record.aa23Registered.AA23Key.AA23Subscriber == aa23ID {
			aa23Registry.aa23Retire(aa23Record, AA23RetireRevoked)
		}
	}
	if aa23Subscriber = aa23Registry.aa23FindSubscriber(aa23ID); aa23Subscriber != nil {
		aa23Registry.aa23ForgetClosedSubscriber(aa23Subscriber)
	}
}

// AA23Reset retires every registration and master identity. Subscribers stay
// open, and every high-water mark survives so no identity can be reused.
func (aa23Registry *AA23Registry) AA23Reset() {
	aa23Registry.aa23ReleaseMastersWhere(0)
	for aa23Index := range aa23Registry.aa23Records {
		aa23Record := &aa23Registry.aa23Records[aa23Index]
		if aa23Record.aa23Used && aa23Record.aa23State == AA23RegistrationActive {
			aa23Registry.aa23Retire(aa23Record, AA23RetireRevoked)
		}
	}
}

func (aa23Registry *AA23Registry) aa23ReleaseMastersWhere(aa23ID AA23SubscriberID) {
	for aa23Address := range aa23Registry.aa23Masters {
		if aa23Owner := aa23Registry.aa23Masters[aa23Address]; aa23Owner != 0 && (aa23ID == 0 || aa23Owner == aa23ID) {
			aa23Registry.aa23Masters[aa23Address] = 0
			aa23Registry.aa23MasterCount--
		}
	}
}

// AA23BindMaster records a local master identity. It is kept apart from target
// registration: neither grants, releases or implies the other, and this table
// performs no arbitration. A repeat by the same owner is harmless.
func (aa23Registry *AA23Registry) AA23BindMaster(aa23ID AA23SubscriberID, aa23Address byte) error {
	if aa23Subscriber := aa23Registry.aa23FindSubscriber(aa23ID); aa23Subscriber == nil || !aa23Subscriber.aa23Open {
		return ErrAA23UnknownSubscriber
	}
	if !aa23IsInitiatorAddress(aa23Address) {
		return ErrAA23InvalidAddress
	}
	if aa23Registry.aa23Conflicts[aa23Address] {
		return ErrAA23PhysicalConflict
	}
	switch aa23Registry.aa23Masters[aa23Address] {
	case aa23ID:
		return nil
	case 0:
	default:
		return ErrAA23MasterConflict
	}
	if aa23Registry.aa23MasterCount >= aa23Registry.aa23Limits.AA23MaxMasters {
		return ErrAA23Capacity
	}
	aa23Registry.aa23Masters[aa23Address] = aa23ID
	aa23Registry.aa23MasterCount++
	return nil
}

// AA23ReleaseMaster drops a master identity held by exactly this owner.
func (aa23Registry *AA23Registry) AA23ReleaseMaster(aa23ID AA23SubscriberID, aa23Address byte) {
	if aa23ID != 0 && aa23Registry.aa23Masters[aa23Address] == aa23ID {
		aa23Registry.aa23Masters[aa23Address] = 0
		aa23Registry.aa23MasterCount--
	}
}

// AA23MasterOwner reports the subscriber holding a master identity, zero if none.
func (aa23Registry *AA23Registry) AA23MasterOwner(aa23Address byte) AA23SubscriberID {
	return aa23Registry.aa23Masters[aa23Address]
}

// AA23MarkPhysicalConflict inhibits an address that is known to conflict and
// revokes its plans, so clearing the mark never revives an old generation.
func (aa23Registry *AA23Registry) AA23MarkPhysicalConflict(aa23Address byte) {
	aa23Registry.aa23Conflicts[aa23Address] = true
	aa23Registry.aa23RetireWhere(AA23RetireRevoked, func(aa23Current *aa23Slot) bool {
		return aa23Current.aa23Identity.AA23Registration.AA23Address == aa23Address
	})
}

// AA23ClearPhysicalConflict lifts the inhibition; it proves no absence of a
// physical responder and restores no plan.
func (aa23Registry *AA23Registry) AA23ClearPhysicalConflict(aa23Address byte) {
	aa23Registry.aa23Conflicts[aa23Address] = false
}

// AA23PhysicalConflict reports whether an address is currently inhibited.
func (aa23Registry *AA23Registry) AA23PhysicalConflict(aa23Address byte) bool {
	return aa23Registry.aa23Conflicts[aa23Address]
}

// AA23RenewRegistration raises the lease for future plans only. Published plans
// keep their frozen deadlines, and an expired lease cannot be revived.
func (aa23Registry *AA23Registry) AA23RenewRegistration(aa23Key AA23RegistrationKey, aa23Expiry AA23Tick, aa23Now AA23ClockReading) error {
	if aa23Err := aa23Registry.aa23ObserveClock(aa23Now); aa23Err != nil {
		return aa23Err
	}
	aa23Record := aa23Registry.aa23FindExact(aa23Key)
	if aa23Record == nil || aa23Record.aa23State != AA23RegistrationActive {
		return ErrAA23NotRegistered
	}
	if aa23Now.AA23Tick >= aa23Record.aa23LeaseExpiry {
		return ErrAA23Expired
	}
	if aa23Expiry < aa23Record.aa23LeaseExpiry {
		return ErrAA23StaleGeneration
	}
	if aa23Err := aa23Registry.aa23CheckLease(aa23Expiry, aa23Now); aa23Err != nil {
		return aa23Err
	}
	aa23Record.aa23LeaseExpiry = aa23Expiry
	return nil
}

// AA23AdvanceSource moves a registration to a newer source generation and revokes
// every plan compiled from an older one.
func (aa23Registry *AA23Registry) AA23AdvanceSource(aa23Key AA23RegistrationKey, aa23Source AA23SourceGeneration, aa23Expiry AA23Tick, aa23Now AA23ClockReading) error {
	if aa23Err := aa23Registry.aa23ObserveClock(aa23Now); aa23Err != nil {
		return aa23Err
	}
	aa23Record := aa23Registry.aa23FindExact(aa23Key)
	if aa23Record == nil || aa23Record.aa23State != AA23RegistrationActive {
		return ErrAA23NotRegistered
	}
	if aa23Source <= aa23Record.aa23Source {
		return ErrAA23StaleGeneration
	}
	if aa23Err := aa23Registry.aa23CheckLease(aa23Expiry, aa23Now); aa23Err != nil {
		return aa23Err
	}
	aa23Record.aa23Source = aa23Source
	aa23Record.aa23SourceLease = aa23Expiry
	aa23Registry.aa23RetireWhere(AA23RetireRevoked, func(aa23Current *aa23Slot) bool {
		return aa23Current.aa23Identity.AA23Registration.aa23Matches(aa23Key)
	})
	return nil
}

// AA23ExpireDue retires what has reached its exclusive deadline. Expiry closes
// admission only: a retained slot still waits for an acknowledgement or for
// observed worker termination, so a paused worker quarantines its storage.
func (aa23Registry *AA23Registry) AA23ExpireDue(aa23Now AA23ClockReading) error {
	if aa23Err := aa23Registry.aa23ObserveClock(aa23Now); aa23Err != nil {
		return aa23Err
	}
	aa23Registry.aa23RetireWhere(AA23RetireExpired, func(aa23Current *aa23Slot) bool {
		return aa23Current.aa23State == AA23SlotPublished && aa23Current.aa23Validity.aa23Check(aa23Now) != nil
	})
	for aa23Index := range aa23Registry.aa23Records {
		aa23Record := &aa23Registry.aa23Records[aa23Index]
		if aa23Record.aa23Used && aa23Record.aa23State == AA23RegistrationActive && aa23Now.AA23Tick >= aa23Record.aa23LeaseExpiry {
			aa23Registry.aa23Retire(aa23Record, AA23RetireExpired)
		}
	}
	return nil
}

// AA23BindWorker binds a strictly newer worker incarnation or stream generation.
// Every plan of the predecessor leaves publication; its storage stays retained
// until that predecessor acknowledges or its termination is observed. Plan
// generations are monotonic within a worker, across its stream changes.
func (aa23Registry *AA23Registry) AA23BindWorker(aa23Worker AA23WorkerID, aa23Stream AA23StreamGeneration) error {
	if aa23Worker == 0 || aa23Stream == 0 {
		return ErrAA23InvalidIdentity
	}
	aa23NewWorker := aa23Worker > aa23Registry.aa23WorkerTop
	aa23NewStream := aa23Worker == aa23Registry.aa23WorkerTop && aa23Registry.aa23WorkerBound && aa23Stream > aa23Registry.aa23Stream
	if !aa23NewWorker && !aa23NewStream {
		return ErrAA23StaleGeneration
	}
	aa23Registry.aa23RetireWhere(AA23RetireRevoked, func(*aa23Slot) bool { return true })
	if aa23NewWorker {
		aa23Registry.aa23PlanTop = 0
	}
	aa23Registry.aa23WorkerBound = true
	aa23Registry.aa23Worker = aa23Worker
	aa23Registry.aa23WorkerTop = aa23Worker
	aa23Registry.aa23Stream = aa23Stream
	return nil
}

// AA23ObserveWorkerTerminated records supervisor proof that a worker incarnation
// has exited and all access to its mapping has ceased. This is the only way
// storage is reclaimed without an acknowledgement. Registrations stay, but need
// fresh plans under a newly bound worker.
func (aa23Registry *AA23Registry) AA23ObserveWorkerTerminated(aa23Worker AA23WorkerID) error {
	if aa23Worker == 0 || aa23Worker > aa23Registry.aa23WorkerTop {
		return ErrAA23InvalidIdentity
	}
	if aa23Registry.aa23WorkerBound && aa23Registry.aa23Worker == aa23Worker {
		aa23Registry.aa23WorkerBound = false
		aa23Registry.aa23RetireWhere(AA23RetireRevoked, func(aa23Current *aa23Slot) bool {
			return aa23Current.aa23State == AA23SlotWriting
		})
	}
	for aa23Index := range aa23Registry.aa23Pool.aa23Slots {
		aa23Current := &aa23Registry.aa23Pool.aa23Slots[aa23Index]
		if aa23Current.aa23State != AA23SlotPublished && aa23Current.aa23State != AA23SlotRetained {
			continue
		}
		if aa23Current.aa23Identity.AA23Worker != aa23Worker {
			continue
		}
		aa23Key := aa23Current.aa23Identity.AA23Registration
		aa23Registry.aa23RetireSlot(aa23Current, AA23RetireRevoked)
		aa23Current.aa23Pinned = false
		aa23Current.aa23State = AA23SlotReclaimable
		aa23Registry.aa23Settle(aa23Key)
	}
	return nil
}

// aa23ValidateCandidate checks the whole detached candidate before any effect.
func (aa23Registry *AA23Registry) aa23ValidateCandidate(aa23Candidate *AA23PlanCandidate) error {
	aa23Limits := aa23Registry.aa23Limits
	aa23Identity := aa23Candidate.AA23Identity
	if aa23Identity.AA23Plan == 0 || aa23Identity.AA23Source == 0 || aa23Identity.AA23Worker == 0 || aa23Identity.AA23Stream == 0 {
		return ErrAA23InvalidIdentity
	}
	if aa23Err := aa23Candidate.AA23Failure.aa23Validate(); aa23Err != nil {
		return aa23Err
	}
	if len(aa23Candidate.AA23Entries) == 0 {
		return ErrAA23InvalidPlan
	}
	if uint64(len(aa23Candidate.AA23Entries)) > uint64(aa23Limits.AA23MaxEntriesPerPlan) {
		return ErrAA23Oversize
	}
	aa23Total := uint64(0)
	for aa23Index := range aa23Candidate.AA23Entries {
		aa23Entry := &aa23Candidate.AA23Entries[aa23Index]
		if aa23Err := aa23Registry.aa23ValidateEntry(aa23Entry, aa23Identity.AA23Registration.AA23Address); aa23Err != nil {
			return aa23Err
		}
		aa23Total += uint64(len(aa23Entry.AA23Request)) + uint64(len(aa23Entry.AA23EncodedResponse))
		if aa23Total > uint64(aa23Limits.AA23MaxPlanBytes) {
			return ErrAA23Oversize
		}
		for aa23Prior := 0; aa23Prior < aa23Index; aa23Prior++ {
			if aa23EntriesOverlap(&aa23Candidate.AA23Entries[aa23Prior], aa23Entry) {
				return ErrAA23AmbiguousPlan
			}
		}
	}
	return nil
}

func (aa23Registry *AA23Registry) aa23ValidateEntry(aa23Entry *AA23PlanEntry, aa23Address byte) error {
	if aa23Entry.AA23Operation != AA23OperationRead {
		return ErrAA23Unsupported
	}
	if aa23Entry.AA23AllowedPhase != AA23PhaseResponse {
		return ErrAA23Unsupported
	}
	if aa23Entry.AA23Destination != aa23Address {
		return ErrAA23InvalidPlan
	}
	if aa23Entry.AA23Source.AA23Any {
		if aa23Entry.AA23Source.AA23Source != 0 {
			return ErrAA23InvalidPlan
		}
	} else if !aa23IsInitiatorAddress(aa23Entry.AA23Source.AA23Source) {
		return ErrAA23InvalidPlan
	}
	if uint64(aa23Entry.AA23RequestLength) != uint64(len(aa23Entry.AA23Request)) {
		return ErrAA23InvalidPlan
	}
	if uint64(len(aa23Entry.AA23Request)) > uint64(aa23Registry.aa23Limits.AA23MaxRequestBytes) {
		return ErrAA23Oversize
	}
	aa23Encoded := uint64(len(aa23Entry.AA23EncodedResponse))
	if aa23Entry.AA23MaxEncodedBytes == 0 {
		return ErrAA23InvalidPlan
	}
	if aa23Entry.AA23MaxEncodedBytes > aa23Registry.aa23Limits.AA23MaxEncodedResponseBytes || aa23Encoded > uint64(aa23Entry.AA23MaxEncodedBytes) {
		return ErrAA23Oversize
	}
	return aa23ValidateEncodedResponse(aa23Entry.AA23EncodedResponse)
}

// aa23EntriesOverlap reports whether one request could select both entries.
func aa23EntriesOverlap(aa23First *AA23PlanEntry, aa23Second *AA23PlanEntry) bool {
	if aa23First.AA23PB != aa23Second.AA23PB || aa23First.AA23SB != aa23Second.AA23SB {
		return false
	}
	if string(aa23First.AA23Request) != string(aa23Second.AA23Request) {
		return false
	}
	return aa23First.AA23Source.AA23Any || aa23Second.AA23Source.AA23Any ||
		aa23First.AA23Source.AA23Source == aa23Second.AA23Source.AA23Source
}

// aa23CheckPublishable holds at both prepare and publish, so a change during
// writing cannot publish a generation that is already stale.
func (aa23Registry *AA23Registry) aa23CheckPublishable(aa23Identity AA23PlanIdentity, aa23PlanExpiry AA23Tick, aa23Now AA23ClockReading) (*aa23RegistrationRecord, error) {
	if !aa23Registry.aa23WorkerBound {
		return nil, ErrAA23NoWorker
	}
	if aa23Identity.AA23Worker != aa23Registry.aa23Worker {
		return nil, ErrAA23IdentityMismatch
	}
	if aa23Identity.AA23Stream != aa23Registry.aa23Stream {
		return nil, ErrAA23IdentityMismatch
	}
	aa23Record := aa23Registry.aa23FindExact(aa23Identity.AA23Registration)
	if aa23Record == nil || aa23Record.aa23State != AA23RegistrationActive {
		return nil, ErrAA23NotRegistered
	}
	if aa23Identity.AA23Source != aa23Record.aa23Source {
		return nil, ErrAA23IdentityMismatch
	}
	if aa23Registry.aa23Conflicts[aa23Identity.AA23Registration.AA23Address] {
		return nil, ErrAA23PhysicalConflict
	}
	aa23Validity := AA23PlanValidity{
		AA23PlanExpiry:         aa23PlanExpiry,
		AA23RegistrationExpiry: aa23Record.aa23LeaseExpiry,
		AA23SourceLeaseExpiry:  aa23Record.aa23SourceLease,
	}
	if aa23Err := aa23Validity.aa23Check(aa23Now); aa23Err != nil {
		return nil, aa23Err
	}
	return aa23Record, nil
}

// AA23PreparePlan validates a whole candidate, then copies it into a free slot.
// The slot stays invisible to the worker and earlier generations stay published.
// A full pool refuses without evicting anything. The generation is consumed here:
// an aborted preparation cannot lend its number to different bytes.
func (aa23Registry *AA23Registry) AA23PreparePlan(aa23Candidate AA23PlanCandidate, aa23Now AA23ClockReading) (AA23SlotRef, error) {
	if aa23Err := aa23Registry.aa23ObserveClock(aa23Now); aa23Err != nil {
		return AA23SlotRef{}, aa23Err
	}
	if aa23Err := aa23Registry.aa23ValidateCandidate(&aa23Candidate); aa23Err != nil {
		return AA23SlotRef{}, aa23Err
	}
	aa23Record, aa23Err := aa23Registry.aa23CheckPublishable(aa23Candidate.AA23Identity, aa23Candidate.AA23PlanExpiry, aa23Now)
	if aa23Err != nil {
		return AA23SlotRef{}, aa23Err
	}
	if aa23Err := aa23Registry.aa23CheckLease(aa23Candidate.AA23PlanExpiry, aa23Now); aa23Err != nil {
		return AA23SlotRef{}, aa23Err
	}
	if aa23Registry.aa23PlanTop == aa23math.MaxUint64 {
		return AA23SlotRef{}, ErrAA23CounterExhausted
	}
	if aa23Candidate.AA23Identity.AA23Plan <= aa23Registry.aa23PlanTop {
		return AA23SlotRef{}, ErrAA23StaleGeneration
	}
	for aa23Index := range aa23Registry.aa23Pool.aa23Slots {
		aa23Current := &aa23Registry.aa23Pool.aa23Slots[aa23Index]
		if aa23Current.aa23State != AA23SlotFree {
			continue
		}
		aa23Registry.aa23PlanTop = aa23Candidate.AA23Identity.AA23Plan
		aa23Current.aa23State = AA23SlotWriting
		aa23Current.aa23Identity = aa23Candidate.AA23Identity
		aa23Current.aa23Validity = AA23PlanValidity{
			AA23PlanExpiry:         aa23Candidate.AA23PlanExpiry,
			AA23RegistrationExpiry: aa23Record.aa23LeaseExpiry,
			AA23SourceLeaseExpiry:  aa23Record.aa23SourceLease,
		}
		aa23Current.aa23Copy(aa23Candidate.AA23Entries)
		return aa23Current.aa23Ref(aa23Index), nil
	}
	return AA23SlotRef{}, ErrAA23PoolExhausted
}

// aa23Copy detaches the candidate; caller buffers are never retained.
func (aa23Current *aa23Slot) aa23Copy(aa23Entries []AA23PlanEntry) {
	aa23Cursor := uint32(0)
	for aa23Index := range aa23Entries {
		aa23Entry := &aa23Entries[aa23Index]
		aa23RequestLen := uint32(copy(aa23Current.aa23Bytes[aa23Cursor:], aa23Entry.AA23Request))
		aa23ReplyStart := aa23Cursor + aa23RequestLen
		aa23ReplyLen := uint32(copy(aa23Current.aa23Bytes[aa23ReplyStart:], aa23Entry.AA23EncodedResponse))
		aa23Current.aa23Entries[aa23Index] = aa23EntryRecord{
			aa23SourceAny:    aa23Entry.AA23Source.AA23Any,
			aa23Source:       aa23Entry.AA23Source.AA23Source,
			aa23PB:           aa23Entry.AA23PB,
			aa23SB:           aa23Entry.AA23SB,
			aa23Phase:        aa23Entry.AA23AllowedPhase,
			aa23RequestStart: aa23Cursor,
			aa23RequestLen:   aa23RequestLen,
			aa23ReplyStart:   aa23ReplyStart,
			aa23ReplyLen:     aa23ReplyLen,
		}
		aa23Cursor = aa23ReplyStart + aa23ReplyLen
	}
	aa23Current.aa23EntryCount = uint32(len(aa23Entries))
}

// AA23AbortPrepared returns a prepared slot to free. It was never visible, so no
// acknowledgement is owed.
func (aa23Registry *AA23Registry) AA23AbortPrepared(aa23Ref AA23SlotRef) error {
	aa23Current, aa23Err := aa23Registry.aa23Pool.aa23Resolve(aa23Ref)
	if aa23Err != nil {
		return aa23Err
	}
	if aa23Current.aa23State != AA23SlotWriting {
		return ErrAA23SlotState
	}
	return aa23Current.aa23Release()
}

// AA23PublishPrepared atomically makes a complete generation current for its
// registration. Its predecessor can no longer be published, but remains retained
// until acknowledged. A candidate gone stale while writing is aborted and replaces
// nothing.
func (aa23Registry *AA23Registry) AA23PublishPrepared(aa23Ref AA23SlotRef, aa23Now AA23ClockReading) (AA23PublishNotice, error) {
	if aa23Err := aa23Registry.aa23ObserveClock(aa23Now); aa23Err != nil {
		return AA23PublishNotice{}, aa23Err
	}
	aa23Current, aa23Err := aa23Registry.aa23Pool.aa23Resolve(aa23Ref)
	if aa23Err != nil {
		return AA23PublishNotice{}, aa23Err
	}
	if aa23Current.aa23State != AA23SlotWriting {
		return AA23PublishNotice{}, ErrAA23SlotState
	}
	aa23Record, aa23Err := aa23Registry.aa23CheckPublishable(aa23Current.aa23Identity, aa23Current.aa23Validity.AA23PlanExpiry, aa23Now)
	if aa23Err != nil {
		if aa23Current.aa23Release() != nil {
			aa23Current.aa23State = AA23SlotReclaimable
		}
		return AA23PublishNotice{}, aa23Err
	}
	if aa23Record.aa23Published >= 0 {
		aa23Registry.aa23RetireSlot(&aa23Registry.aa23Pool.aa23Slots[aa23Record.aa23Published], AA23RetireSuperseded)
	}
	aa23Current.aa23State = AA23SlotPublished
	aa23Record.aa23Published = int32(aa23Ref.AA23Index)
	return AA23PublishNotice{AA23Slot: aa23Ref, AA23Identity: aa23Current.aa23Identity}, nil
}

// AA23PendingRevocations appends a notice for each retained slot, up to the
// capacity of dst, without allocating. Notices stay pending until acknowledged,
// so a lost notice or acknowledgement is recovered by delivering it again.
func (aa23Registry *AA23Registry) AA23PendingRevocations(aa23Dst []AA23RevocationNotice) []AA23RevocationNotice {
	for aa23Index := range aa23Registry.aa23Pool.aa23Slots {
		if len(aa23Dst) == cap(aa23Dst) {
			return aa23Dst
		}
		aa23Current := &aa23Registry.aa23Pool.aa23Slots[aa23Index]
		if aa23Current.aa23State != AA23SlotRetained {
			continue
		}
		aa23Dst = append(aa23Dst, AA23RevocationNotice{
			AA23Slot:     aa23Current.aa23Ref(aa23Index),
			AA23Identity: aa23Current.aa23Identity,
			AA23Reason:   aa23Current.aa23Reason,
		})
	}
	return aa23Dst
}

// AA23ObserveAcknowledgement moves a retained slot to reclaimable only for the
// exact slot incarnation and plan identity. A stale acknowledgement cannot retire
// a successor or clear another registration.
func (aa23Registry *AA23Registry) AA23ObserveAcknowledgement(aa23Ack AA23Acknowledgement) error {
	aa23Current, aa23Err := aa23Registry.aa23Pool.aa23Resolve(aa23Ack.AA23Slot)
	if aa23Err != nil {
		return aa23Err
	}
	if aa23Current.aa23State != AA23SlotRetained {
		return ErrAA23SlotState
	}
	if !aa23Current.aa23Identity.aa23Matches(aa23Ack.AA23Identity) {
		return ErrAA23IdentityMismatch
	}
	if aa23Current.aa23Pinned {
		return ErrAA23SlotPinned
	}
	aa23Current.aa23State = AA23SlotReclaimable
	aa23Registry.aa23Settle(aa23Current.aa23Identity.AA23Registration)
	return nil
}

type aa23WorkerSlotRecord struct {
	aa23Incarnation uint64
	aa23Superseded  bool
	aa23Revoked     bool
}

// AA23WorkerView is the worker's own view of the pool. It moves no slot state,
// and its admission changes only on what it has itself observed.
type AA23WorkerView struct {
	aa23Pool           *AA23Pool
	aa23Worker         AA23WorkerID
	aa23Stream         AA23StreamGeneration
	aa23Clock          aa23ClockGuard
	aa23PlanTop        [aa23AddressSpace]AA23PlanGeneration
	aa23TransactionTop AA23TransactionOrdinal
	aa23Current        [aa23AddressSpace]AA23SlotRef
	aa23Records        []aa23WorkerSlotRecord
	aa23PinActive      bool
	aa23PinKey         AA23WireKey
	aa23PinSlot        AA23SlotRef
}

// AA23NewWorkerView creates the view of one worker incarnation and stream.
func AA23NewWorkerView(aa23Pool *AA23Pool, aa23Worker AA23WorkerID, aa23Stream AA23StreamGeneration) (*AA23WorkerView, error) {
	if aa23Pool == nil || aa23Worker == 0 || aa23Stream == 0 {
		return nil, ErrAA23InvalidIdentity
	}
	return &AA23WorkerView{
		aa23Pool:    aa23Pool,
		aa23Worker:  aa23Worker,
		aa23Stream:  aa23Stream,
		aa23Records: make([]aa23WorkerSlotRecord, len(aa23Pool.aa23Slots)),
	}, nil
}

func (aa23View *AA23WorkerView) aa23Record(aa23Ref AA23SlotRef) *aa23WorkerSlotRecord {
	aa23Record := &aa23View.aa23Records[aa23Ref.AA23Index]
	if aa23Record.aa23Incarnation != aa23Ref.AA23Incarnation {
		*aa23Record = aa23WorkerSlotRecord{aa23Incarnation: aa23Ref.AA23Incarnation}
	}
	return aa23Record
}

func (aa23View *AA23WorkerView) aa23HoldsPin(aa23Ref AA23SlotRef) bool {
	return aa23View.aa23PinActive && aa23View.aa23PinSlot == aa23Ref
}

// aa23Acknowledge reports readiness only once the slot has no reader.
func (aa23View *AA23WorkerView) aa23Acknowledge(aa23Ref AA23SlotRef, aa23Identity AA23PlanIdentity) (AA23Acknowledgement, bool) {
	if aa23View.aa23HoldsPin(aa23Ref) {
		return AA23Acknowledgement{}, false
	}
	return AA23Acknowledgement{AA23Slot: aa23Ref, AA23Identity: aa23Identity}, true
}

// AA23AdoptPlan installs a published generation at a safe boundary. It may return
// an acknowledgement for the predecessor when no transaction holds that slot.
func (aa23View *AA23WorkerView) AA23AdoptPlan(aa23Notice AA23PublishNotice, aa23Now AA23ClockReading) (AA23Acknowledgement, bool, error) {
	if aa23Err := aa23View.aa23Clock.aa23Observe(aa23Now); aa23Err != nil {
		return AA23Acknowledgement{}, false, aa23Err
	}
	aa23Current, aa23Err := aa23View.aa23Pool.aa23Resolve(aa23Notice.AA23Slot)
	if aa23Err != nil {
		return AA23Acknowledgement{}, false, aa23Err
	}
	if aa23Current.aa23State != AA23SlotPublished {
		return AA23Acknowledgement{}, false, ErrAA23SlotState
	}
	aa23Identity := aa23Notice.AA23Identity
	if !aa23Current.aa23Identity.aa23Matches(aa23Identity) {
		return AA23Acknowledgement{}, false, ErrAA23IdentityMismatch
	}
	if aa23Identity.AA23Worker != aa23View.aa23Worker {
		return AA23Acknowledgement{}, false, ErrAA23IdentityMismatch
	}
	if aa23Identity.AA23Stream != aa23View.aa23Stream {
		return AA23Acknowledgement{}, false, ErrAA23IdentityMismatch
	}
	aa23Address := aa23Identity.AA23Registration.AA23Address
	if aa23Identity.AA23Plan <= aa23View.aa23PlanTop[aa23Address] {
		return AA23Acknowledgement{}, false, ErrAA23StaleGeneration
	}
	if aa23Err := aa23Current.aa23Validity.aa23Check(aa23Now); aa23Err != nil {
		return AA23Acknowledgement{}, false, aa23Err
	}
	aa23View.aa23PlanTop[aa23Address] = aa23Identity.AA23Plan
	aa23Previous := aa23View.aa23Current[aa23Address]
	aa23View.aa23Current[aa23Address] = aa23Notice.AA23Slot
	if aa23Previous.AA23Incarnation == 0 {
		return AA23Acknowledgement{}, false, nil
	}
	aa23Old, aa23Err := aa23View.aa23Pool.aa23Resolve(aa23Previous)
	if aa23Err != nil {
		return AA23Acknowledgement{}, false, nil
	}
	aa23View.aa23Record(aa23Previous).aa23Superseded = true
	aa23Ack, aa23Ready := aa23View.aa23Acknowledge(aa23Previous, aa23Old.aa23Identity)
	return aa23Ack, aa23Ready, nil
}

// AA23ObserveRevocation is the boundary at which a revocation takes effect for
// this worker. Selection is fenced at once. A superseded plan may finish a selected
// transaction, while any other reason also closes that transaction at its next
// check. The acknowledgement waits until no transaction holds the slot.
func (aa23View *AA23WorkerView) AA23ObserveRevocation(aa23Notice AA23RevocationNotice) (AA23Acknowledgement, bool, error) {
	aa23Current, aa23Err := aa23View.aa23Pool.aa23Resolve(aa23Notice.AA23Slot)
	if aa23Err != nil {
		return AA23Acknowledgement{}, false, aa23Err
	}
	if aa23Current.aa23State != AA23SlotRetained {
		return AA23Acknowledgement{}, false, ErrAA23SlotState
	}
	if !aa23Current.aa23Identity.aa23Matches(aa23Notice.AA23Identity) {
		return AA23Acknowledgement{}, false, ErrAA23IdentityMismatch
	}
	if aa23Notice.AA23Identity.AA23Worker != aa23View.aa23Worker {
		return AA23Acknowledgement{}, false, ErrAA23IdentityMismatch
	}
	if aa23Notice.AA23Identity.AA23Stream > aa23View.aa23Stream {
		return AA23Acknowledgement{}, false, ErrAA23IdentityMismatch
	}
	aa23Record := aa23View.aa23Record(aa23Notice.AA23Slot)
	if aa23Notice.AA23Reason == AA23RetireSuperseded {
		aa23Record.aa23Superseded = true
	} else {
		aa23Record.aa23Revoked = true
	}
	aa23Address := aa23Notice.AA23Identity.AA23Registration.AA23Address
	if aa23View.aa23Current[aa23Address] == aa23Notice.AA23Slot {
		aa23View.aa23Current[aa23Address] = AA23SlotRef{}
	}
	aa23Ack, aa23Ready := aa23View.aa23Acknowledge(aa23Notice.AA23Slot, aa23Notice.AA23Identity)
	return aa23Ack, aa23Ready, nil
}

// AA23AdvanceStream records a gap or resynchronization. Every selection of the
// old stream ends at once: the pin is dropped and no plan can be selected until a
// generation of the new stream is adopted. Plan generations keep advancing.
func (aa23View *AA23WorkerView) AA23AdvanceStream(aa23Stream AA23StreamGeneration) error {
	if aa23Stream <= aa23View.aa23Stream {
		return ErrAA23StaleGeneration
	}
	if aa23View.aa23PinActive {
		aa23View.aa23DropPin()
	}
	aa23View.aa23Stream = aa23Stream
	aa23View.aa23TransactionTop = 0
	aa23View.aa23Current = [aa23AddressSpace]AA23SlotRef{}
	return nil
}

func (aa23View *AA23WorkerView) aa23DropPin() {
	aa23Current := &aa23View.aa23Pool.aa23Slots[aa23View.aa23PinSlot.AA23Index]
	if aa23Current.aa23Incarnation == aa23View.aa23PinSlot.AA23Incarnation {
		aa23Current.aa23Pinned = false
	}
	aa23View.aa23PinActive = false
	aa23View.aa23PinKey = AA23WireKey{}
	aa23View.aa23PinSlot = AA23SlotRef{}
}

// AA23PlanPin holds one generation for one transaction. It is a plain value with
// no view of plan storage: bytes are reached only through checked accessors, so
// nothing mutable escapes and no other generation can be mixed in.
type AA23PlanPin struct {
	aa23View *AA23WorkerView
	aa23Key  AA23WireKey
}

// AA23SelectProvisional selects and pins the current plan at destination time. It
// is provisional and authorizes no output. The ordinal is consumed even when
// selection is refused, because it belongs to the observed transaction.
func (aa23View *AA23WorkerView) AA23SelectProvisional(aa23Address byte, aa23Transaction AA23TransactionOrdinal, aa23Now AA23ClockReading) (AA23PlanPin, error) {
	if aa23Err := aa23View.aa23Clock.aa23Observe(aa23Now); aa23Err != nil {
		return AA23PlanPin{}, aa23Err
	}
	if aa23Transaction == 0 {
		return AA23PlanPin{}, ErrAA23InvalidIdentity
	}
	if aa23Transaction <= aa23View.aa23TransactionTop {
		return AA23PlanPin{}, ErrAA23StaleTransaction
	}
	aa23View.aa23TransactionTop = aa23Transaction
	if aa23View.aa23PinActive {
		return AA23PlanPin{}, ErrAA23PinBusy
	}
	aa23Ref := aa23View.aa23Current[aa23Address]
	if aa23Ref.AA23Incarnation == 0 {
		return AA23PlanPin{}, ErrAA23NoPlan
	}
	aa23Current, aa23Err := aa23View.aa23Pool.aa23Resolve(aa23Ref)
	if aa23Err != nil {
		return AA23PlanPin{}, aa23Err
	}
	if aa23Current.aa23State != AA23SlotPublished && aa23Current.aa23State != AA23SlotRetained {
		return AA23PlanPin{}, ErrAA23SlotState
	}
	if aa23Err := aa23Current.aa23Validity.aa23Check(aa23Now); aa23Err != nil {
		return AA23PlanPin{}, aa23Err
	}
	aa23Key := AA23WireKey{AA23Identity: aa23Current.aa23Identity, AA23Transaction: aa23Transaction}
	aa23Current.aa23Pinned = true
	aa23View.aa23PinActive = true
	aa23View.aa23PinKey = aa23Key
	aa23View.aa23PinSlot = aa23Ref
	return AA23PlanPin{aa23View: aa23View, aa23Key: aa23Key}, nil
}

// AA23Key returns the exact identity frozen at selection.
func (aa23Pin AA23PlanPin) AA23Key() AA23WireKey {
	return aa23Pin.aa23Key
}

// aa23Live is the whole admission check, repeated at every boundary.
func (aa23Pin AA23PlanPin) aa23Live(aa23Expect AA23WireKey, aa23Now AA23ClockReading) (*aa23Slot, error) {
	aa23View := aa23Pin.aa23View
	if aa23View == nil {
		return nil, ErrAA23PinReleased
	}
	if aa23Err := aa23View.aa23Clock.aa23Observe(aa23Now); aa23Err != nil {
		return nil, aa23Err
	}
	if !aa23View.aa23PinActive || aa23View.aa23PinKey != aa23Pin.aa23Key {
		return nil, ErrAA23PinReleased
	}
	if !aa23Expect.AA23Identity.aa23Matches(aa23Pin.aa23Key.AA23Identity) {
		return nil, ErrAA23IdentityMismatch
	}
	if aa23Expect.AA23Transaction != aa23Pin.aa23Key.AA23Transaction {
		return nil, ErrAA23IdentityMismatch
	}
	aa23Current, aa23Err := aa23View.aa23Pool.aa23Resolve(aa23View.aa23PinSlot)
	if aa23Err != nil {
		return nil, aa23Err
	}
	if aa23Current.aa23State != AA23SlotPublished && aa23Current.aa23State != AA23SlotRetained {
		return nil, ErrAA23SlotState
	}
	if aa23View.aa23Record(aa23View.aa23PinSlot).aa23Revoked {
		return nil, ErrAA23Revoked
	}
	if aa23Err := aa23Current.aa23Validity.aa23Check(aa23Now); aa23Err != nil {
		return nil, aa23Err
	}
	return aa23Current, nil
}

// AA23Validate rechecks exact keys, observed revocation and every deadline.
func (aa23Pin AA23PlanPin) AA23Validate(aa23Expect AA23WireKey, aa23Now AA23ClockReading) error {
	_, aa23Err := aa23Pin.aa23Live(aa23Expect, aa23Now)
	return aa23Err
}

// AA23RequestQuery is one complete, already framing- and CRC-validated request in
// logical bytes. Framing and CRC validation belong to the wire engine.
type AA23RequestQuery struct {
	AA23Source      byte
	AA23Destination byte
	AA23PB          byte
	AA23SB          byte
	AA23Data        []byte
}

// AA23Match finds the single exact entry of the pinned plan with a bounded scan.
func (aa23Pin AA23PlanPin) AA23Match(aa23Expect AA23WireKey, aa23Query AA23RequestQuery, aa23Now AA23ClockReading) (uint32, error) {
	aa23Current, aa23Err := aa23Pin.aa23Live(aa23Expect, aa23Now)
	if aa23Err != nil {
		return 0, aa23Err
	}
	if aa23Query.AA23Destination != aa23Current.aa23Identity.AA23Registration.AA23Address {
		return 0, ErrAA23NoMatch
	}
	if !aa23IsInitiatorAddress(aa23Query.AA23Source) {
		return 0, ErrAA23NoMatch
	}
	for aa23Index := uint32(0); aa23Index < aa23Current.aa23EntryCount; aa23Index++ {
		aa23Entry := &aa23Current.aa23Entries[aa23Index]
		if !aa23Entry.aa23SourceAny && aa23Entry.aa23Source != aa23Query.AA23Source {
			continue
		}
		if aa23Entry.aa23PB != aa23Query.AA23PB || aa23Entry.aa23SB != aa23Query.AA23SB {
			continue
		}
		if uint64(aa23Entry.aa23RequestLen) != uint64(len(aa23Query.AA23Data)) {
			continue
		}
		aa23Request := aa23Current.aa23Bytes[aa23Entry.aa23RequestStart : aa23Entry.aa23RequestStart+aa23Entry.aa23RequestLen]
		if string(aa23Request) == string(aa23Query.AA23Data) {
			return aa23Index, nil
		}
	}
	return 0, ErrAA23NoMatch
}

// AA23ResponseLength returns the encoded length of one entry of the pinned plan.
func (aa23Pin AA23PlanPin) AA23ResponseLength(aa23Expect AA23WireKey, aa23Entry uint32, aa23Now AA23ClockReading) (uint32, error) {
	aa23Current, aa23Err := aa23Pin.aa23Live(aa23Expect, aa23Now)
	if aa23Err != nil {
		return 0, aa23Err
	}
	if aa23Entry >= aa23Current.aa23EntryCount {
		return 0, ErrAA23NoMatch
	}
	return aa23Current.aa23Entries[aa23Entry].aa23ReplyLen, nil
}

// AA23ResponseSymbol returns one prepared wire symbol after a full recheck at that
// output boundary. This is the only way out for response bytes.
func (aa23Pin AA23PlanPin) AA23ResponseSymbol(aa23Expect AA23WireKey, aa23Entry uint32, aa23Offset uint32, aa23Phase AA23Phase, aa23Now AA23ClockReading) (byte, error) {
	aa23Current, aa23Err := aa23Pin.aa23Live(aa23Expect, aa23Now)
	if aa23Err != nil {
		return 0, aa23Err
	}
	if aa23Entry >= aa23Current.aa23EntryCount {
		return 0, ErrAA23NoMatch
	}
	aa23Record := &aa23Current.aa23Entries[aa23Entry]
	if aa23Phase != aa23Record.aa23Phase {
		return 0, ErrAA23Phase
	}
	if aa23Offset >= aa23Record.aa23ReplyLen {
		return 0, ErrAA23Oversize
	}
	return aa23Current.aa23Bytes[aa23Record.aa23ReplyStart+aa23Offset], nil
}

// AA23Release ends the transaction's hold. A slot whose retirement this worker
// has already observed yields its acknowledgement here.
func (aa23Pin AA23PlanPin) AA23Release() (AA23Acknowledgement, bool, error) {
	aa23View := aa23Pin.aa23View
	if aa23View == nil || !aa23View.aa23PinActive || aa23View.aa23PinKey != aa23Pin.aa23Key {
		return AA23Acknowledgement{}, false, ErrAA23PinReleased
	}
	aa23Ref := aa23View.aa23PinSlot
	aa23Identity := aa23Pin.aa23Key.AA23Identity
	aa23Record := aa23View.aa23Record(aa23Ref)
	aa23Retired := aa23Record.aa23Superseded || aa23Record.aa23Revoked
	aa23View.aa23DropPin()
	if !aa23Retired {
		return AA23Acknowledgement{}, false, nil
	}
	aa23Ack, aa23Ready := aa23View.aa23Acknowledge(aa23Ref, aa23Identity)
	return aa23Ack, aa23Ready, nil
}
