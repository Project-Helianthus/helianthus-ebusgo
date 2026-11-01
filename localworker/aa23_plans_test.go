package localworker

import (
	aa23errors "errors"
	aa23math "math"
	aa23reflect "reflect"
	aa23sort "sort"
	aa23testing "testing"
)

const (
	aa23FixtureAddress      byte = 0x15
	aa23FixtureOtherAddress byte = 0x26
	aa23FixtureInitiator    byte = 0x10
	aa23FixtureOtherSource  byte = 0x31
	aa23FixtureWorker            = AA23WorkerID(11)
	aa23FixtureStream            = AA23StreamGeneration(21)
	aa23FixtureSource            = AA23SourceGeneration(31)
	aa23FixtureDomain            = uint64(77)
	aa23FixtureStart             = AA23Tick(100)
	aa23FixtureFarExpiry         = AA23Tick(5000)
)

var aa23FixtureSilence = AA23FailurePolicy{
	AA23Unavailable: AA23FailureSilence,
	AA23Malformed:   AA23FailureSilence,
	AA23Timeout:     AA23FailureSilence,
}

var aa23FixtureLimits = AA23Limits{
	AA23MaxSubscribers:          4,
	AA23MaxRegistrations:        4,
	AA23MaxMasters:              2,
	AA23MaxPlanSlots:            3,
	AA23MaxEntriesPerPlan:       4,
	AA23MaxRequestBytes:         8,
	AA23MaxEncodedResponseBytes: 40,
	AA23MaxPlanBytes:            96,
	AA23MaxLeaseTicks:           100000,
}

type aa23Fixture struct {
	aa23T          *aa23testing.T
	aa23Registry   *AA23Registry
	aa23View       *AA23WorkerView
	aa23Tick       AA23Tick
	aa23Subscriber AA23SubscriberID
	aa23Key        AA23RegistrationKey
	aa23Txn        AA23TransactionOrdinal
}

func aa23NewFixture(aa23T *aa23testing.T) *aa23Fixture {
	aa23T.Helper()
	return aa23NewFixtureWithLimits(aa23T, aa23FixtureLimits)
}

func aa23NewFixtureWithLimits(aa23T *aa23testing.T, aa23Limits AA23Limits) *aa23Fixture {
	aa23T.Helper()
	aa23Registry, aa23Err := AA23NewRegistry(aa23Limits, 0)
	if aa23Err != nil {
		aa23T.Fatalf("AA23NewRegistry: %v", aa23Err)
	}
	aa23View, aa23Err := AA23NewWorkerView(aa23Registry.AA23Pool(), aa23FixtureWorker, aa23FixtureStream)
	if aa23Err != nil {
		aa23T.Fatalf("AA23NewWorkerView: %v", aa23Err)
	}
	if aa23Err := aa23Registry.AA23BindWorker(aa23FixtureWorker, aa23FixtureStream); aa23Err != nil {
		aa23T.Fatalf("AA23BindWorker: %v", aa23Err)
	}
	aa23Subscriber, aa23Err := aa23Registry.AA23OpenSubscriber()
	if aa23Err != nil {
		aa23T.Fatalf("AA23OpenSubscriber: %v", aa23Err)
	}
	aa23Current := &aa23Fixture{
		aa23T:          aa23T,
		aa23Registry:   aa23Registry,
		aa23View:       aa23View,
		aa23Tick:       aa23FixtureStart,
		aa23Subscriber: aa23Subscriber,
		aa23Key:        AA23RegistrationKey{AA23Subscriber: aa23Subscriber, AA23Address: aa23FixtureAddress, AA23Epoch: 1},
	}
	aa23Current.aa23MustRegister(aa23Current.aa23Registration(aa23Current.aa23Key))
	return aa23Current
}

func (aa23Current *aa23Fixture) aa23Now() AA23ClockReading {
	return AA23ClockReading{AA23Domain: aa23FixtureDomain, AA23Tick: aa23Current.aa23Tick}
}

func (aa23Current *aa23Fixture) aa23Registration(aa23Key AA23RegistrationKey) AA23Registration {
	return AA23Registration{
		AA23Key:               aa23Key,
		AA23LeaseExpiry:       aa23FixtureFarExpiry,
		AA23Source:            aa23FixtureSource,
		AA23SourceLeaseExpiry: aa23FixtureFarExpiry,
		AA23Failure:           aa23FixtureSilence,
	}
}

func (aa23Current *aa23Fixture) aa23MustRegister(aa23Registration AA23Registration) {
	aa23Current.aa23T.Helper()
	if _, aa23Err := aa23Current.aa23Registry.AA23Register(aa23Registration, aa23Current.aa23Now()); aa23Err != nil {
		aa23Current.aa23T.Fatalf("AA23Register(%+v): %v", aa23Registration, aa23Err)
	}
}

// aa23FixtureData is synthetic and carries both symbols that need escaping.
func aa23FixtureData(aa23Marker byte) []byte {
	return []byte{aa23Marker, 'F', 'A', 'M', '0', '1', aa23SymbolEscape, aa23SymbolSyn}
}

func aa23MustEncode(aa23T *aa23testing.T, aa23Data []byte) []byte {
	aa23T.Helper()
	aa23Wire, aa23Err := AA23EncodeResponse(nil, aa23Data)
	if aa23Err != nil {
		aa23T.Fatalf("AA23EncodeResponse: %v", aa23Err)
	}
	return aa23Wire
}

func (aa23Current *aa23Fixture) aa23Entry(aa23Marker byte) AA23PlanEntry {
	aa23Current.aa23T.Helper()
	return AA23PlanEntry{
		AA23Source:          AA23SourceConstraint{AA23Source: aa23FixtureInitiator},
		AA23Destination:     aa23Current.aa23Key.AA23Address,
		AA23PB:              0x07,
		AA23SB:              0x04,
		AA23RequestLength:   0,
		AA23Operation:       AA23OperationRead,
		AA23AllowedPhase:    AA23PhaseResponse,
		AA23MaxEncodedBytes: 40,
		AA23EncodedResponse: aa23MustEncode(aa23Current.aa23T, aa23FixtureData(aa23Marker)),
	}
}

func (aa23Current *aa23Fixture) aa23Identity(aa23Plan AA23PlanGeneration) AA23PlanIdentity {
	return AA23PlanIdentity{
		AA23Registration: aa23Current.aa23Key,
		AA23Worker:       aa23FixtureWorker,
		AA23Stream:       aa23FixtureStream,
		AA23Plan:         aa23Plan,
		AA23Source:       aa23FixtureSource,
	}
}

func (aa23Current *aa23Fixture) aa23Candidate(aa23Plan AA23PlanGeneration, aa23Marker byte) AA23PlanCandidate {
	aa23Current.aa23T.Helper()
	return AA23PlanCandidate{
		AA23Identity:   aa23Current.aa23Identity(aa23Plan),
		AA23PlanExpiry: aa23FixtureFarExpiry,
		AA23Failure:    aa23FixtureSilence,
		AA23Entries:    []AA23PlanEntry{aa23Current.aa23Entry(aa23Marker)},
	}
}

func (aa23Current *aa23Fixture) aa23Publish(aa23Candidate AA23PlanCandidate) AA23PublishNotice {
	aa23Current.aa23T.Helper()
	aa23Ref, aa23Err := aa23Current.aa23Registry.AA23PreparePlan(aa23Candidate, aa23Current.aa23Now())
	if aa23Err != nil {
		aa23Current.aa23T.Fatalf("AA23PreparePlan: %v", aa23Err)
	}
	aa23Notice, aa23Err := aa23Current.aa23Registry.AA23PublishPrepared(aa23Ref, aa23Current.aa23Now())
	if aa23Err != nil {
		aa23Current.aa23T.Fatalf("AA23PublishPrepared: %v", aa23Err)
	}
	return aa23Notice
}

func (aa23Current *aa23Fixture) aa23Install(aa23Plan AA23PlanGeneration, aa23Marker byte) AA23PublishNotice {
	aa23Current.aa23T.Helper()
	aa23Notice := aa23Current.aa23Publish(aa23Current.aa23Candidate(aa23Plan, aa23Marker))
	aa23Ack, aa23Ready, aa23Err := aa23Current.aa23View.AA23AdoptPlan(aa23Notice, aa23Current.aa23Now())
	if aa23Err != nil {
		aa23Current.aa23T.Fatalf("AA23AdoptPlan: %v", aa23Err)
	}
	if aa23Ready {
		if aa23Err := aa23Current.aa23Registry.AA23ObserveAcknowledgement(aa23Ack); aa23Err != nil {
			aa23Current.aa23T.Fatalf("AA23ObserveAcknowledgement: %v", aa23Err)
		}
	}
	return aa23Notice
}

func (aa23Current *aa23Fixture) aa23Select(aa23Address byte) (AA23PlanPin, error) {
	aa23Current.aa23Txn++
	return aa23Current.aa23View.AA23SelectProvisional(aa23Address, aa23Current.aa23Txn, aa23Current.aa23Now())
}

func (aa23Current *aa23Fixture) aa23MustSelect() AA23PlanPin {
	aa23Current.aa23T.Helper()
	aa23Pin, aa23Err := aa23Current.aa23Select(aa23Current.aa23Key.AA23Address)
	if aa23Err != nil {
		aa23Current.aa23T.Fatalf("AA23SelectProvisional: %v", aa23Err)
	}
	return aa23Pin
}

// aa23Settle delivers every pending notice and every resulting acknowledgement.
func (aa23Current *aa23Fixture) aa23Settle() {
	aa23Current.aa23T.Helper()
	aa23Notices := aa23Current.aa23Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 8))
	for _, aa23Notice := range aa23Notices {
		aa23Ack, aa23Ready, aa23Err := aa23Current.aa23View.AA23ObserveRevocation(aa23Notice)
		if aa23Err != nil {
			aa23Current.aa23T.Fatalf("AA23ObserveRevocation(%+v): %v", aa23Notice, aa23Err)
		}
		if !aa23Ready {
			aa23Current.aa23T.Fatalf("no acknowledgement for %+v", aa23Notice)
		}
		if aa23Err := aa23Current.aa23Registry.AA23ObserveAcknowledgement(aa23Ack); aa23Err != nil {
			aa23Current.aa23T.Fatalf("AA23ObserveAcknowledgement: %v", aa23Err)
		}
	}
}

func (aa23Current *aa23Fixture) aa23ResponseBytes(aa23Pin AA23PlanPin) ([]byte, error) {
	aa23Current.aa23T.Helper()
	aa23Key := aa23Pin.AA23Key()
	aa23Length, aa23Err := aa23Pin.AA23ResponseLength(aa23Key, 0, aa23Current.aa23Now())
	if aa23Err != nil {
		return nil, aa23Err
	}
	aa23Output := make([]byte, 0, aa23Length)
	for aa23Offset := uint32(0); aa23Offset < aa23Length; aa23Offset++ {
		aa23Symbol, aa23Err := aa23Pin.AA23ResponseSymbol(aa23Key, 0, aa23Offset, AA23PhaseResponse, aa23Current.aa23Now())
		if aa23Err != nil {
			return aa23Output, aa23Err
		}
		aa23Output = append(aa23Output, aa23Symbol)
	}
	return aa23Output, nil
}

func aa23WantErr(aa23T *aa23testing.T, aa23Label string, aa23Got error, aa23Want error) {
	aa23T.Helper()
	if !aa23errors.Is(aa23Got, aa23Want) {
		aa23T.Fatalf("%s: err = %v, want %v", aa23Label, aa23Got, aa23Want)
	}
}

func aa23WantOK(aa23T *aa23testing.T, aa23Label string, aa23Got error) {
	aa23T.Helper()
	if aa23Got != nil {
		aa23T.Fatalf("%s: unexpected err %v", aa23Label, aa23Got)
	}
}

// aa23IdentityAxes varies each plan identity field while the others stay valid.
var aa23IdentityAxes = []struct {
	aa23Name   string
	aa23Mutate func(*AA23PlanIdentity)
}{
	{"subscriber", func(aa23I *AA23PlanIdentity) { aa23I.AA23Registration.AA23Subscriber++ }},
	{"address", func(aa23I *AA23PlanIdentity) { aa23I.AA23Registration.AA23Address = aa23FixtureOtherAddress }},
	{"registrationEpoch", func(aa23I *AA23PlanIdentity) { aa23I.AA23Registration.AA23Epoch++ }},
	{"worker", func(aa23I *AA23PlanIdentity) { aa23I.AA23Worker++ }},
	{"stream", func(aa23I *AA23PlanIdentity) { aa23I.AA23Stream++ }},
	{"plan", func(aa23I *AA23PlanIdentity) { aa23I.AA23Plan++ }},
	{"source", func(aa23I *AA23PlanIdentity) { aa23I.AA23Source++ }},
}

func TestAA23LimitsRefuseEveryZeroAndImpossibleValue(aa23T *aa23testing.T) {
	aa23Cases := []struct {
		aa23Name   string
		aa23Mutate func(*AA23Limits)
	}{
		{"subscribers", func(aa23L *AA23Limits) { aa23L.AA23MaxSubscribers = 0 }},
		{"registrations", func(aa23L *AA23Limits) { aa23L.AA23MaxRegistrations = 0 }},
		{"masters", func(aa23L *AA23Limits) { aa23L.AA23MaxMasters = 0 }},
		{"slots", func(aa23L *AA23Limits) { aa23L.AA23MaxPlanSlots = 0 }},
		{"entries", func(aa23L *AA23Limits) { aa23L.AA23MaxEntriesPerPlan = 0 }},
		{"request", func(aa23L *AA23Limits) { aa23L.AA23MaxRequestBytes = 0 }},
		{"response", func(aa23L *AA23Limits) { aa23L.AA23MaxEncodedResponseBytes = 0 }},
		{"planBytes", func(aa23L *AA23Limits) { aa23L.AA23MaxPlanBytes = 0 }},
		{"lease", func(aa23L *AA23Limits) { aa23L.AA23MaxLeaseTicks = 0 }},
		{"registrationsOverAddressSpace", func(aa23L *AA23Limits) { aa23L.AA23MaxRegistrations = aa23AddressSpace + 1 }},
		{"mastersOverAddressSpace", func(aa23L *AA23Limits) { aa23L.AA23MaxMasters = aa23AddressSpace + 1 }},
		{"subscribersOverAddressSpace", func(aa23L *AA23Limits) { aa23L.AA23MaxSubscribers = aa23AddressSpace + 1 }},
		{"requestOverNativeLength", func(aa23L *AA23Limits) { aa23L.AA23MaxRequestBytes = aa23MaxNativeLength + 1 }},
		{"responseOverNativeLength", func(aa23L *AA23Limits) { aa23L.AA23MaxEncodedResponseBytes = aa23MaxEncodedResponse + 1 }},
		{"planSmallerThanOneEntry", func(aa23L *AA23Limits) { aa23L.AA23MaxPlanBytes = 47 }},
		{"poolOverCeiling", func(aa23L *AA23Limits) {
			aa23SlotBytes := uint64(aa23L.AA23MaxPlanBytes) + uint64(aa23L.AA23MaxEntriesPerPlan)*aa23EntryAccountingBytes
			aa23L.AA23MaxPlanSlots = uint32(aa23PoolByteCeiling/aa23SlotBytes) + 1
		}},
		{"slotOverCeiling", func(aa23L *AA23Limits) { aa23L.AA23MaxPlanBytes = aa23math.MaxUint32 }},
		{"entriesOverCeiling", func(aa23L *AA23Limits) { aa23L.AA23MaxEntriesPerPlan = aa23math.MaxUint32 }},
	}
	if _, aa23Err := AA23NewRegistry(aa23FixtureLimits, 0); aa23Err != nil {
		aa23T.Fatalf("positive control: %v", aa23Err)
	}
	for _, aa23Case := range aa23Cases {
		aa23Limits := aa23FixtureLimits
		aa23Case.aa23Mutate(&aa23Limits)
		aa23Registry, aa23Err := AA23NewRegistry(aa23Limits, 0)
		aa23WantErr(aa23T, aa23Case.aa23Name, aa23Err, ErrAA23InvalidLimits)
		if aa23Registry != nil {
			aa23T.Fatalf("%s: registry returned on refusal", aa23Case.aa23Name)
		}
	}
}

func TestAA23DuplicateAddressRefusedAndIncumbentUnchanged(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Second, aa23Err := aa23F.aa23Registry.AA23OpenSubscriber()
	aa23WantOK(aa23T, "open", aa23Err)
	aa23Incumbent := aa23F.aa23Registry.AA23Registration(aa23F.aa23Key)

	aa23OtherSubscriber := aa23F.aa23Registration(AA23RegistrationKey{AA23Subscriber: aa23Second, AA23Address: aa23FixtureAddress, AA23Epoch: 1})
	_, aa23Err = aa23F.aa23Registry.AA23Register(aa23OtherSubscriber, aa23F.aa23Now())
	aa23WantErr(aa23T, "other subscriber", aa23Err, ErrAA23DuplicateAddress)

	aa23LaterEpoch := aa23F.aa23Registration(AA23RegistrationKey{AA23Subscriber: aa23F.aa23Subscriber, AA23Address: aa23FixtureAddress, AA23Epoch: 2})
	_, aa23Err = aa23F.aa23Registry.AA23Register(aa23LaterEpoch, aa23F.aa23Now())
	aa23WantErr(aa23T, "later epoch of the same subscriber", aa23Err, ErrAA23DuplicateAddress)

	if aa23Got := aa23F.aa23Registry.AA23Registration(aa23F.aa23Key); aa23Got != aa23Incumbent {
		aa23T.Fatalf("incumbent changed: %+v", aa23Got)
	}
	aa23Elsewhere := aa23F.aa23Registration(AA23RegistrationKey{AA23Subscriber: aa23Second, AA23Address: aa23FixtureOtherAddress, AA23Epoch: 1})
	aa23F.aa23MustRegister(aa23Elsewhere)
	if aa23Got := aa23F.aa23Registry.AA23Registration(aa23Elsewhere.AA23Key); aa23Got.AA23State != AA23RegistrationActive {
		aa23T.Fatalf("positive control: %+v", aa23Got)
	}
}

func TestAA23ExactRetryAllocatesNothingAndDifferentContentConflicts(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Original := aa23F.aa23Registration(aa23F.aa23Key)
	aa23View, aa23Err := aa23F.aa23Registry.AA23Register(aa23Original, aa23F.aa23Now())
	aa23WantOK(aa23T, "exact retry", aa23Err)
	if aa23View.AA23State != AA23RegistrationActive || aa23View.AA23Registered != aa23Original {
		aa23T.Fatalf("exact retry state: %+v", aa23View)
	}
	aa23Allocs := aa23testing.AllocsPerRun(10, func() {
		if _, aa23Err := aa23F.aa23Registry.AA23Register(aa23Original, aa23F.aa23Now()); aa23Err != nil {
			aa23T.Fatal(aa23Err)
		}
	})
	if aa23Allocs != 0 {
		aa23T.Fatalf("exact retry allocated %v", aa23Allocs)
	}
	aa23Cases := []struct {
		aa23Name   string
		aa23Mutate func(*AA23Registration)
	}{
		{"lease", func(aa23R *AA23Registration) { aa23R.AA23LeaseExpiry++ }},
		{"source", func(aa23R *AA23Registration) { aa23R.AA23Source++ }},
		{"sourceLease", func(aa23R *AA23Registration) { aa23R.AA23SourceLeaseExpiry++ }},
		{"failureUnavailable", func(aa23R *AA23Registration) { aa23R.AA23Failure.AA23Unavailable = AA23FailureNACK }},
		{"failureMalformed", func(aa23R *AA23Registration) { aa23R.AA23Failure.AA23Malformed = AA23FailureNACK }},
		{"failureTimeout", func(aa23R *AA23Registration) { aa23R.AA23Failure.AA23Timeout = AA23FailureNACK }},
	}
	for _, aa23Case := range aa23Cases {
		aa23Changed := aa23Original
		aa23Case.aa23Mutate(&aa23Changed)
		_, aa23Err := aa23F.aa23Registry.AA23Register(aa23Changed, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name, aa23Err, ErrAA23RegistrationConflict)
	}
	if aa23Got := aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23Registered; aa23Got != aa23Original {
		aa23T.Fatalf("conflict changed the incumbent: %+v", aa23Got)
	}
}

func TestAA23RegistrationAdmissionRefusals(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Second, aa23Err := aa23F.aa23Registry.AA23OpenSubscriber()
	aa23WantOK(aa23T, "open", aa23Err)
	aa23Valid := aa23F.aa23Registration(AA23RegistrationKey{AA23Subscriber: aa23Second, AA23Address: aa23FixtureOtherAddress, AA23Epoch: 3})
	aa23Cases := []struct {
		aa23Name   string
		aa23Mutate func(*AA23Registration)
		aa23Want   error
	}{
		{"zeroSubscriber", func(aa23R *AA23Registration) { aa23R.AA23Key.AA23Subscriber = 0 }, ErrAA23InvalidIdentity},
		{"zeroEpoch", func(aa23R *AA23Registration) { aa23R.AA23Key.AA23Epoch = 0 }, ErrAA23InvalidIdentity},
		{"zeroSource", func(aa23R *AA23Registration) { aa23R.AA23Source = 0 }, ErrAA23InvalidIdentity},
		{"unallocatedSubscriber", func(aa23R *AA23Registration) { aa23R.AA23Key.AA23Subscriber = 99 }, ErrAA23UnknownSubscriber},
		{"broadcast", func(aa23R *AA23Registration) { aa23R.AA23Key.AA23Address = aa23AddressBroadcast }, ErrAA23InvalidAddress},
		{"escape", func(aa23R *AA23Registration) { aa23R.AA23Key.AA23Address = aa23SymbolEscape }, ErrAA23InvalidAddress},
		{"syn", func(aa23R *AA23Registration) { aa23R.AA23Key.AA23Address = aa23SymbolSyn }, ErrAA23InvalidAddress},
		{"initiator", func(aa23R *AA23Registration) { aa23R.AA23Key.AA23Address = aa23FixtureInitiator }, ErrAA23InvalidAddress},
		{"leaseEqualsNow", func(aa23R *AA23Registration) { aa23R.AA23LeaseExpiry = aa23FixtureStart }, ErrAA23Expired},
		{"leaseTooLong", func(aa23R *AA23Registration) {
			aa23R.AA23LeaseExpiry = aa23FixtureStart + aa23FixtureLimits.AA23MaxLeaseTicks + 1
		}, ErrAA23LeaseTooLong},
		{"sourceLeaseEqualsNow", func(aa23R *AA23Registration) { aa23R.AA23SourceLeaseExpiry = aa23FixtureStart }, ErrAA23Expired},
		{"nackPolicy", func(aa23R *AA23Registration) { aa23R.AA23Failure.AA23Malformed = AA23FailureNACK }, ErrAA23Unsupported},
		{"unspecifiedPolicy", func(aa23R *AA23Registration) { aa23R.AA23Failure.AA23Timeout = AA23FailureUnspecified }, ErrAA23Unsupported},
	}
	for _, aa23Case := range aa23Cases {
		aa23Candidate := aa23Valid
		aa23Case.aa23Mutate(&aa23Candidate)
		_, aa23Err := aa23F.aa23Registry.AA23Register(aa23Candidate, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name, aa23Err, aa23Case.aa23Want)
		if aa23F.aa23Registry.aa23ByAddress[aa23FixtureOtherAddress] != 0 {
			aa23T.Fatalf("%s: refusal left an occupant", aa23Case.aa23Name)
		}
	}
	aa23F.aa23MustRegister(aa23Valid)
}

func TestAA23EpochNeverReusedAndRefusesWrap(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Key := func(aa23Epoch AA23RegistrationEpoch) AA23RegistrationKey {
		return AA23RegistrationKey{AA23Subscriber: aa23F.aa23Subscriber, AA23Address: aa23FixtureAddress, AA23Epoch: aa23Epoch}
	}
	if aa23State := aa23F.aa23Registry.AA23Unregister(aa23Key(1)); aa23State != AA23RegistrationAbsent {
		aa23T.Fatalf("no plan means no wire authority to settle: %v", aa23State)
	}
	_, aa23Err := aa23F.aa23Registry.AA23Register(aa23F.aa23Registration(aa23Key(1)), aa23F.aa23Now())
	aa23WantErr(aa23T, "same epoch after cleanup", aa23Err, ErrAA23StaleEpoch)
	aa23F.aa23MustRegister(aa23F.aa23Registration(aa23Key(aa23math.MaxUint64)))
	aa23F.aa23Registry.AA23Unregister(aa23Key(aa23math.MaxUint64))
	_, aa23Err = aa23F.aa23Registry.AA23Register(aa23F.aa23Registration(aa23Key(2)), aa23F.aa23Now())
	aa23WantErr(aa23T, "wrapped epoch", aa23Err, ErrAA23CounterExhausted)
}

func TestAA23SubscriberAllocationBoundedAndNeverReused(aa23T *aa23testing.T) {
	aa23Limits := aa23FixtureLimits
	aa23Limits.AA23MaxSubscribers = 1
	aa23Registry, aa23Err := AA23NewRegistry(aa23Limits, 40)
	aa23WantOK(aa23T, "new", aa23Err)
	aa23First, aa23Err := aa23Registry.AA23OpenSubscriber()
	aa23WantOK(aa23T, "open", aa23Err)
	if aa23First != 41 {
		aa23T.Fatalf("floor not honored: %d", aa23First)
	}
	_, aa23Err = aa23Registry.AA23OpenSubscriber()
	aa23WantErr(aa23T, "table full", aa23Err, ErrAA23Capacity)
	aa23Registry.AA23DisconnectSubscriber(aa23First)
	aa23Registry.AA23DisconnectSubscriber(aa23First)
	aa23Next, aa23Err := aa23Registry.AA23OpenSubscriber()
	aa23WantOK(aa23T, "open after disconnect", aa23Err)
	if aa23Next != 42 {
		aa23T.Fatalf("identity reused or skipped: %d", aa23Next)
	}
	aa23Now := AA23ClockReading{AA23Domain: aa23FixtureDomain, AA23Tick: aa23FixtureStart}
	_, aa23Err = aa23Registry.AA23Register(AA23Registration{
		AA23Key:               AA23RegistrationKey{AA23Subscriber: aa23First, AA23Address: aa23FixtureAddress, AA23Epoch: 1},
		AA23LeaseExpiry:       aa23FixtureFarExpiry,
		AA23Source:            aa23FixtureSource,
		AA23SourceLeaseExpiry: aa23FixtureFarExpiry,
		AA23Failure:           aa23FixtureSilence,
	}, aa23Now)
	aa23WantErr(aa23T, "disconnected incarnation", aa23Err, ErrAA23UnknownSubscriber)

	aa23Exhausted, aa23Err := AA23NewRegistry(aa23FixtureLimits, aa23math.MaxUint64)
	aa23WantOK(aa23T, "new at ceiling", aa23Err)
	_, aa23Err = aa23Exhausted.AA23OpenSubscriber()
	aa23WantErr(aa23T, "allocator wrap", aa23Err, ErrAA23CounterExhausted)
}

func TestAA23RegistrationCapacityRefusesWithoutEviction(aa23T *aa23testing.T) {
	aa23Limits := aa23FixtureLimits
	aa23Limits.AA23MaxRegistrations = 1
	aa23F := aa23NewFixtureWithLimits(aa23T, aa23Limits)
	aa23Other := aa23F.aa23Registration(AA23RegistrationKey{AA23Subscriber: aa23F.aa23Subscriber, AA23Address: aa23FixtureOtherAddress, AA23Epoch: 2})
	_, aa23Err := aa23F.aa23Registry.AA23Register(aa23Other, aa23F.aa23Now())
	aa23WantErr(aa23T, "full", aa23Err, ErrAA23Capacity)
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationActive {
		aa23T.Fatal("capacity refusal evicted the incumbent")
	}
	if aa23F.aa23Registry.aa23ByAddress[aa23FixtureOtherAddress] != 0 {
		aa23T.Fatal("capacity refusal left an occupant")
	}
}

func TestAA23UnregisterRequiresEveryKeyField(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Cases := []struct {
		aa23Name   string
		aa23Mutate func(*AA23RegistrationKey)
	}{
		{"subscriber", func(aa23K *AA23RegistrationKey) { aa23K.AA23Subscriber++ }},
		{"address", func(aa23K *AA23RegistrationKey) { aa23K.AA23Address = aa23FixtureOtherAddress }},
		{"epoch", func(aa23K *AA23RegistrationKey) { aa23K.AA23Epoch++ }},
		{"olderEpoch", func(aa23K *AA23RegistrationKey) { aa23K.AA23Epoch = 0 }},
	}
	for _, aa23Case := range aa23Cases {
		aa23Stale := aa23F.aa23Key
		aa23Case.aa23Mutate(&aa23Stale)
		if aa23State := aa23F.aa23Registry.AA23Unregister(aa23Stale); aa23State != AA23RegistrationAbsent {
			aa23T.Fatalf("%s: state %v", aa23Case.aa23Name, aa23State)
		}
		if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationActive {
			aa23T.Fatalf("%s: stale key removed the incumbent", aa23Case.aa23Name)
		}
	}
	if aa23State := aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key); aa23State != AA23RegistrationAbsent {
		aa23T.Fatalf("exact key: %v", aa23State)
	}
	if aa23State := aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key); aa23State != AA23RegistrationAbsent {
		aa23T.Fatalf("repeat: %v", aa23State)
	}
	aa23Successor := AA23RegistrationKey{AA23Subscriber: aa23F.aa23Subscriber, AA23Address: aa23FixtureAddress, AA23Epoch: 2}
	aa23F.aa23MustRegister(aa23F.aa23Registration(aa23Successor))
	if aa23State := aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key); aa23State != AA23RegistrationAbsent {
		aa23T.Fatalf("late cleanup of old epoch: %v", aa23State)
	}
	if aa23F.aa23Registry.AA23Registration(aa23Successor).AA23State != AA23RegistrationActive {
		aa23T.Fatal("late cleanup of the old epoch removed its successor")
	}
}

func TestAA23AddressHeldUntilPredecessorAuthoritySettled(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Install(1, 0xA1)
	if aa23State := aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key); aa23State != AA23RegistrationRetiring {
		aa23T.Fatalf("state %v", aa23State)
	}
	if aa23State := aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key); aa23State != AA23RegistrationRetiring {
		aa23T.Fatalf("repeat state %v", aa23State)
	}
	aa23Successor := AA23RegistrationKey{AA23Subscriber: aa23F.aa23Subscriber, AA23Address: aa23FixtureAddress, AA23Epoch: 2}
	_, aa23Err := aa23F.aa23Registry.AA23Register(aa23F.aa23Registration(aa23Successor), aa23F.aa23Now())
	aa23WantErr(aa23T, "unsettled predecessor", aa23Err, ErrAA23DuplicateAddress)
	_, aa23Err = aa23F.aa23Registry.AA23Register(aa23F.aa23Registration(aa23F.aa23Key), aa23F.aa23Now())
	aa23WantErr(aa23T, "retiring retry", aa23Err, ErrAA23RegistrationRetiring)
	aa23F.aa23Settle()
	aa23F.aa23MustRegister(aa23F.aa23Registration(aa23Successor))
}

func TestAA23DisconnectRetiresOnlyThatSubscriber(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Install(1, 0xA1)
	aa23WantOK(aa23T, "master", aa23F.aa23Registry.AA23BindMaster(aa23F.aa23Subscriber, aa23FixtureInitiator))
	aa23Other, aa23Err := aa23F.aa23Registry.AA23OpenSubscriber()
	aa23WantOK(aa23T, "open", aa23Err)
	aa23WantOK(aa23T, "other master", aa23F.aa23Registry.AA23BindMaster(aa23Other, aa23FixtureOtherSource))
	aa23OtherKey := AA23RegistrationKey{AA23Subscriber: aa23Other, AA23Address: aa23FixtureOtherAddress, AA23Epoch: 1}
	aa23F.aa23MustRegister(aa23F.aa23Registration(aa23OtherKey))

	aa23F.aa23Registry.AA23DisconnectSubscriber(aa23F.aa23Subscriber)
	if aa23State := aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State; aa23State != AA23RegistrationRetiring {
		aa23T.Fatalf("own registration: %v", aa23State)
	}
	if aa23F.aa23Registry.AA23Registration(aa23OtherKey).AA23State != AA23RegistrationActive {
		aa23T.Fatal("unrelated registration retired")
	}
	if aa23F.aa23Registry.AA23MasterOwner(aa23FixtureInitiator) != 0 {
		aa23T.Fatal("own master identity survived disconnect")
	}
	if aa23F.aa23Registry.AA23MasterOwner(aa23FixtureOtherSource) != aa23Other {
		aa23T.Fatal("unrelated master identity released")
	}
	aa23Row := aa23F.aa23Registry.aa23FindSubscriber(aa23F.aa23Subscriber)
	if aa23Row == nil {
		aa23T.Fatal("row freed before the retiring registration settled")
	}
	aa23Closed := aa23F.aa23Registration(AA23RegistrationKey{AA23Subscriber: aa23F.aa23Subscriber, AA23Address: 0x27, AA23Epoch: 2})
	_, aa23Err = aa23F.aa23Registry.AA23Register(aa23Closed, aa23F.aa23Now())
	aa23WantErr(aa23T, "closed incarnation with an unsettled row", aa23Err, ErrAA23UnknownSubscriber)
	aa23F.aa23Settle()
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationAbsent {
		aa23T.Fatal("not settled")
	}
	if aa23F.aa23Registry.aa23FindSubscriber(aa23F.aa23Subscriber) != nil {
		aa23T.Fatal("closed subscriber row not freed after settling")
	}
}

func TestAA23ResetRetiresAllAndKeepsHighWaterMarks(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Install(1, 0xA1)
	aa23WantOK(aa23T, "master", aa23F.aa23Registry.AA23BindMaster(aa23F.aa23Subscriber, aa23FixtureInitiator))
	aa23F.aa23Registry.AA23Reset()
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationRetiring {
		aa23T.Fatal("reset did not retire")
	}
	if aa23F.aa23Registry.AA23MasterOwner(aa23FixtureInitiator) != 0 {
		aa23T.Fatal("reset kept a master identity")
	}
	aa23F.aa23Settle()
	_, aa23Err := aa23F.aa23Registry.AA23Register(aa23F.aa23Registration(aa23F.aa23Key), aa23F.aa23Now())
	aa23WantErr(aa23T, "epoch after reset", aa23Err, ErrAA23StaleEpoch)
	aa23Next := aa23F.aa23Key
	aa23Next.AA23Epoch = 2
	aa23F.aa23MustRegister(aa23F.aa23Registration(aa23Next))
	aa23F.aa23Key = aa23Next
	aa23Candidate := aa23F.aa23Candidate(1, 0xA2)
	_, aa23Err = aa23F.aa23Registry.AA23PreparePlan(aa23Candidate, aa23F.aa23Now())
	aa23WantErr(aa23T, "plan generation after reset", aa23Err, ErrAA23StaleGeneration)
	aa23F.aa23Publish(aa23F.aa23Candidate(2, 0xA2))
}

func TestAA23MasterIdentityIsSeparateFromTargetRegistration(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	if aa23F.aa23Registry.AA23MasterOwner(aa23FixtureInitiator) != 0 {
		aa23T.Fatal("registration implied a master identity")
	}
	aa23WantErr(aa23T, "target as master", aa23F.aa23Registry.AA23BindMaster(aa23F.aa23Subscriber, aa23FixtureAddress), ErrAA23InvalidAddress)
	aa23WantErr(aa23T, "unknown owner", aa23F.aa23Registry.AA23BindMaster(99, aa23FixtureInitiator), ErrAA23UnknownSubscriber)
	aa23WantOK(aa23T, "bind", aa23F.aa23Registry.AA23BindMaster(aa23F.aa23Subscriber, aa23FixtureInitiator))
	aa23WantOK(aa23T, "repeat", aa23F.aa23Registry.AA23BindMaster(aa23F.aa23Subscriber, aa23FixtureInitiator))
	if aa23F.aa23Registry.aa23MasterCount != 1 {
		aa23T.Fatalf("repeat counted twice: %d", aa23F.aa23Registry.aa23MasterCount)
	}
	aa23Other, aa23Err := aa23F.aa23Registry.AA23OpenSubscriber()
	aa23WantOK(aa23T, "open", aa23Err)
	aa23WantErr(aa23T, "other owner", aa23F.aa23Registry.AA23BindMaster(aa23Other, aa23FixtureInitiator), ErrAA23MasterConflict)

	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	if aa23F.aa23Registry.AA23MasterOwner(aa23FixtureInitiator) != aa23F.aa23Subscriber {
		aa23T.Fatal("unregistering a target released the master identity")
	}
	aa23F.aa23Registry.AA23ReleaseMaster(aa23Other, aa23FixtureInitiator)
	if aa23F.aa23Registry.AA23MasterOwner(aa23FixtureInitiator) != aa23F.aa23Subscriber {
		aa23T.Fatal("another owner released the master identity")
	}
	aa23Next := AA23RegistrationKey{AA23Subscriber: aa23F.aa23Subscriber, AA23Address: aa23FixtureAddress, AA23Epoch: 2}
	aa23F.aa23MustRegister(aa23F.aa23Registration(aa23Next))
	aa23F.aa23Registry.AA23ReleaseMaster(aa23F.aa23Subscriber, aa23FixtureInitiator)
	if aa23F.aa23Registry.AA23Registration(aa23Next).AA23State != AA23RegistrationActive {
		aa23T.Fatal("releasing a master identity retired a target registration")
	}
	aa23WantOK(aa23T, "first", aa23F.aa23Registry.AA23BindMaster(aa23Other, 0x01))
	aa23WantOK(aa23T, "second", aa23F.aa23Registry.AA23BindMaster(aa23Other, 0x03))
	aa23WantErr(aa23T, "capacity", aa23F.aa23Registry.AA23BindMaster(aa23Other, 0x07), ErrAA23Capacity)
}

func TestAA23KnownPhysicalConflictInhibitsAndClearRevivesNothing(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Notice := aa23F.aa23Install(1, 0xA1)
	aa23F.aa23Registry.AA23MarkPhysicalConflict(aa23FixtureAddress)
	if !aa23F.aa23Registry.AA23PhysicalConflict(aa23FixtureAddress) {
		aa23T.Fatal("not recorded")
	}
	if aa23F.aa23Registry.AA23Pool().AA23SlotState(aa23Notice.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("published plan survived a known conflict")
	}
	_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(2, 0xA2), aa23F.aa23Now())
	aa23WantErr(aa23T, "prepare under conflict", aa23Err, ErrAA23PhysicalConflict)
	aa23F.aa23Settle()
	aa23F.aa23Registry.AA23ClearPhysicalConflict(aa23FixtureAddress)
	_, aa23Err = aa23F.aa23Select(aa23FixtureAddress)
	aa23WantErr(aa23T, "clear revived a plan", aa23Err, ErrAA23NoPlan)
	aa23F.aa23Install(2, 0xA2)
	aa23F.aa23MustSelect()

	aa23F.aa23Registry.AA23MarkPhysicalConflict(aa23FixtureOtherAddress)
	aa23Other := aa23F.aa23Registration(AA23RegistrationKey{AA23Subscriber: aa23F.aa23Subscriber, AA23Address: aa23FixtureOtherAddress, AA23Epoch: 2})
	_, aa23Err = aa23F.aa23Registry.AA23Register(aa23Other, aa23F.aa23Now())
	aa23WantErr(aa23T, "register under conflict", aa23Err, ErrAA23PhysicalConflict)
	aa23F.aa23Registry.AA23MarkPhysicalConflict(aa23FixtureInitiator)
	aa23WantErr(aa23T, "master under conflict", aa23F.aa23Registry.AA23BindMaster(aa23F.aa23Subscriber, aa23FixtureInitiator), ErrAA23PhysicalConflict)
	aa23F.aa23Registry.AA23ClearPhysicalConflict(aa23FixtureOtherAddress)
	aa23F.aa23MustRegister(aa23Other)
}

func TestAA23PublishRefusedWhileConflictAppearsDuringWriting(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Ref, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(1, 0xA1), aa23F.aa23Now())
	aa23WantOK(aa23T, "prepare", aa23Err)
	aa23F.aa23Registry.AA23MarkPhysicalConflict(aa23FixtureAddress)
	if aa23F.aa23Registry.AA23Pool().AA23SlotState(aa23Ref.AA23Index) != AA23SlotFree {
		aa23T.Fatal("a conflict must abort an unpublished slot")
	}
	_, aa23Err = aa23F.aa23Registry.AA23PublishPrepared(aa23Ref, aa23F.aa23Now())
	aa23WantErr(aa23T, "publish", aa23Err, ErrAA23SlotState)
}

func TestAA23CandidateValidationRefusesWholePlanAndKeepsPredecessor(aa23T *aa23testing.T) {
	aa23SynWire := []byte{0x01, aa23SymbolSyn, aa23CRCUpdate(aa23CRCUpdate(0, 0x01), aa23SymbolSyn)}
	aa23BadCRC := aa23MustEncode(aa23T, []byte{0x01, 0x02})
	aa23BadCRC[len(aa23BadCRC)-1] ^= 0x01
	aa23Cases := []struct {
		aa23Name   string
		aa23Mutate func(*AA23PlanCandidate)
		aa23Want   error
	}{
		{"statefulOperation", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23Operation = AA23OperationStateful }, ErrAA23Unsupported},
		{"unspecifiedOperation", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23Operation = AA23OperationUnspecified }, ErrAA23Unsupported},
		{"phaseRequest", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23AllowedPhase = AA23PhaseRequest }, ErrAA23Unsupported},
		{"phaseNone", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23AllowedPhase = AA23PhaseNone }, ErrAA23Unsupported},
		{"failureNACK", func(aa23C *AA23PlanCandidate) { aa23C.AA23Failure.AA23Unavailable = AA23FailureNACK }, ErrAA23Unsupported},
		{"failureUnspecified", func(aa23C *AA23PlanCandidate) { aa23C.AA23Failure.AA23Malformed = AA23FailureUnspecified }, ErrAA23Unsupported},
		{"wrongDestination", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23Destination = aa23FixtureOtherAddress }, ErrAA23InvalidPlan},
		{"sourceNotInitiator", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23Source.AA23Source = aa23FixtureOtherAddress }, ErrAA23InvalidPlan},
		{"anyWithSource", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23Source.AA23Any = true }, ErrAA23InvalidPlan},
		{"lengthShorterThanData", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23Request = []byte{0x01} }, ErrAA23InvalidPlan},
		{"lengthLongerThanData", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23RequestLength = 1 }, ErrAA23InvalidPlan},
		{"requestOverBound", func(aa23C *AA23PlanCandidate) {
			aa23C.AA23Entries[0].AA23Request = make([]byte, 9)
			aa23C.AA23Entries[0].AA23RequestLength = 9
		}, ErrAA23Oversize},
		{"zeroMaxEncoded", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23MaxEncodedBytes = 0 }, ErrAA23InvalidPlan},
		{"encodedOverEntryMax", func(aa23C *AA23PlanCandidate) {
			aa23C.AA23Entries[0].AA23MaxEncodedBytes = uint32(len(aa23C.AA23Entries[0].AA23EncodedResponse)) - 1
		}, ErrAA23Oversize},
		{"entryMaxOverLimit", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23MaxEncodedBytes = 41 }, ErrAA23Oversize},
		{"noEntries", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries = nil }, ErrAA23InvalidPlan},
		{"emptyResponse", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23EncodedResponse = nil }, ErrAA23InvalidPlan},
		{"rawSyn", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23EncodedResponse = aa23SynWire }, ErrAA23InvalidPlan},
		{"danglingEscape", func(aa23C *AA23PlanCandidate) {
			aa23C.AA23Entries[0].AA23EncodedResponse = []byte{0x00, aa23SymbolEscape}
		}, ErrAA23InvalidPlan},
		{"badEscapeSuffix", func(aa23C *AA23PlanCandidate) {
			aa23C.AA23Entries[0].AA23EncodedResponse = []byte{0x00, aa23SymbolEscape, 0x02}
		}, ErrAA23InvalidPlan},
		{"badCRC", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23EncodedResponse = aa23BadCRC }, ErrAA23InvalidPlan},
		{"lengthByteTooLarge", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries[0].AA23EncodedResponse = []byte{0x02, 0x00} }, ErrAA23InvalidPlan},
		{"trailingWireByte", func(aa23C *AA23PlanCandidate) {
			aa23C.AA23Entries[0].AA23EncodedResponse = append(aa23MustEncode(aa23T, nil), 0x00)
		}, ErrAA23InvalidPlan},
		{"tooManyEntries", func(aa23C *AA23PlanCandidate) {
			aa23C.AA23Entries = append(aa23C.AA23Entries, aa23C.AA23Entries[0], aa23C.AA23Entries[0], aa23C.AA23Entries[0], aa23C.AA23Entries[0])
		}, ErrAA23Oversize},
		{"planBytesOverBound", func(aa23C *AA23PlanCandidate) {
			aa23Base := aa23C.AA23Entries[0]
			aa23Base.AA23EncodedResponse = aa23MustEncode(aa23T, make([]byte, 30))
			aa23Second, aa23Third, aa23Fourth := aa23Base, aa23Base, aa23Base
			aa23Second.AA23SB, aa23Third.AA23SB, aa23Fourth.AA23SB = 0x05, 0x06, 0x07
			aa23C.AA23Entries = []AA23PlanEntry{aa23Base, aa23Second, aa23Third, aa23Fourth}
		}, ErrAA23Oversize},
		{"duplicateEntry", func(aa23C *AA23PlanCandidate) { aa23C.AA23Entries = append(aa23C.AA23Entries, aa23C.AA23Entries[0]) }, ErrAA23AmbiguousPlan},
		{"anySourceOverlapsExact", func(aa23C *AA23PlanCandidate) {
			aa23Any := aa23C.AA23Entries[0]
			aa23Any.AA23Source = AA23SourceConstraint{AA23Any: true}
			aa23C.AA23Entries = append(aa23C.AA23Entries, aa23Any)
		}, ErrAA23AmbiguousPlan},
		{"exactOverlapsAnySource", func(aa23C *AA23PlanCandidate) {
			aa23Any := aa23C.AA23Entries[0]
			aa23Any.AA23Source = AA23SourceConstraint{AA23Any: true}
			aa23C.AA23Entries = []AA23PlanEntry{aa23Any, aa23C.AA23Entries[0]}
		}, ErrAA23AmbiguousPlan},
		{"zeroPlan", func(aa23C *AA23PlanCandidate) { aa23C.AA23Identity.AA23Plan = 0 }, ErrAA23InvalidIdentity},
		{"zeroSource", func(aa23C *AA23PlanCandidate) { aa23C.AA23Identity.AA23Source = 0 }, ErrAA23InvalidIdentity},
		{"zeroWorker", func(aa23C *AA23PlanCandidate) { aa23C.AA23Identity.AA23Worker = 0 }, ErrAA23InvalidIdentity},
		{"zeroStream", func(aa23C *AA23PlanCandidate) { aa23C.AA23Identity.AA23Stream = 0 }, ErrAA23InvalidIdentity},
		{"planExpiryEqualsNow", func(aa23C *AA23PlanCandidate) { aa23C.AA23PlanExpiry = aa23FixtureStart }, ErrAA23Expired},
	}
	for _, aa23Case := range aa23Cases {
		aa23F := aa23NewFixture(aa23T)
		aa23Predecessor := aa23F.aa23Install(1, 0xA1)
		aa23Candidate := aa23F.aa23Candidate(2, 0xA2)
		aa23Case.aa23Mutate(&aa23Candidate)
		aa23Top := aa23F.aa23Registry.aa23PlanTop
		_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23Candidate, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name, aa23Err, aa23Case.aa23Want)
		aa23Want := AA23SlotCounts{AA23Free: 2, AA23Published: 1}
		if aa23Got := aa23F.aa23Registry.AA23Pool().AA23Counts(); aa23Got != aa23Want {
			aa23T.Fatalf("%s: pool %+v", aa23Case.aa23Name, aa23Got)
		}
		if aa23F.aa23Registry.aa23PlanTop != aa23Top {
			aa23T.Fatalf("%s: refusal consumed a generation", aa23Case.aa23Name)
		}
		aa23Pin := aa23F.aa23MustSelect()
		if aa23Pin.AA23Key().AA23Identity != aa23Predecessor.AA23Identity {
			aa23T.Fatalf("%s: predecessor replaced", aa23Case.aa23Name)
		}
	}
}

func TestAA23NonOverlappingSelectorsInstallTogether(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Base := aa23F.aa23Entry(0xA1)
	aa23Base.AA23Request = []byte{0x01, 0x02}
	aa23Base.AA23RequestLength = 2
	aa23Variants := []struct {
		aa23Name   string
		aa23Mutate func(*AA23PlanEntry)
	}{
		{"source", func(aa23E *AA23PlanEntry) { aa23E.AA23Source.AA23Source = aa23FixtureOtherSource }},
		{"pb", func(aa23E *AA23PlanEntry) { aa23E.AA23PB++ }},
		{"sb", func(aa23E *AA23PlanEntry) { aa23E.AA23SB++ }},
		{"length", func(aa23E *AA23PlanEntry) {
			aa23E.AA23Request = []byte{0x01}
			aa23E.AA23RequestLength = 1
		}},
		{"data", func(aa23E *AA23PlanEntry) { aa23E.AA23Request = []byte{0x01, 0x03} }},
	}
	for aa23Index, aa23Variant := range aa23Variants {
		aa23Second := aa23Base
		aa23Variant.aa23Mutate(&aa23Second)
		aa23Candidate := aa23F.aa23Candidate(AA23PlanGeneration(aa23Index+1), 0xA1)
		aa23Candidate.AA23Entries = []AA23PlanEntry{aa23Base, aa23Second}
		aa23Ref, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23Candidate, aa23F.aa23Now())
		aa23WantOK(aa23T, aa23Variant.aa23Name, aa23Err)
		aa23WantOK(aa23T, aa23Variant.aa23Name, aa23F.aa23Registry.AA23AbortPrepared(aa23Ref))
	}
}

func TestAA23PrepareVariesEveryIdentityAxis(aa23T *aa23testing.T) {
	aa23Cases := []struct {
		aa23Name string
		aa23Want error
	}{
		{"subscriber", ErrAA23NotRegistered},
		{"address", ErrAA23InvalidPlan},
		{"registrationEpoch", ErrAA23NotRegistered},
		{"worker", ErrAA23IdentityMismatch},
		{"stream", ErrAA23IdentityMismatch},
		{"source", ErrAA23IdentityMismatch},
	}
	for _, aa23Case := range aa23Cases {
		aa23F := aa23NewFixture(aa23T)
		aa23Candidate := aa23F.aa23Candidate(1, 0xA1)
		for _, aa23Axis := range aa23IdentityAxes {
			if aa23Axis.aa23Name == aa23Case.aa23Name {
				aa23Axis.aa23Mutate(&aa23Candidate.AA23Identity)
			}
		}
		_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23Candidate, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name, aa23Err, aa23Case.aa23Want)
		aa23F.aa23Publish(aa23F.aa23Candidate(1, 0xA1))
	}
	aa23F := aa23NewFixture(aa23T)
	aa23Elsewhere := aa23F.aa23Candidate(1, 0xA1)
	aa23Elsewhere.AA23Identity.AA23Registration.AA23Address = aa23FixtureOtherAddress
	aa23Elsewhere.AA23Entries[0].AA23Destination = aa23FixtureOtherAddress
	_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23Elsewhere, aa23F.aa23Now())
	aa23WantErr(aa23T, "address with a matching entry", aa23Err, ErrAA23NotRegistered)
}

func TestAA23PlanGenerationAdvancesAndRefusesWrap(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Publish(aa23F.aa23Candidate(5, 0xA1))
	for _, aa23Stale := range []AA23PlanGeneration{1, 4, 5} {
		_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(aa23Stale, 0xA2), aa23F.aa23Now())
		aa23WantErr(aa23T, "stale generation", aa23Err, ErrAA23StaleGeneration)
	}
	aa23Ref, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(6, 0xA2), aa23F.aa23Now())
	aa23WantOK(aa23T, "advance", aa23Err)
	aa23WantOK(aa23T, "abort", aa23F.aa23Registry.AA23AbortPrepared(aa23Ref))
	_, aa23Err = aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(6, 0xA3), aa23F.aa23Now())
	aa23WantErr(aa23T, "aborted generation lent to other bytes", aa23Err, ErrAA23StaleGeneration)

	aa23Ref, aa23Err = aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(aa23math.MaxUint64, 0xA2), aa23F.aa23Now())
	aa23WantOK(aa23T, "ceiling", aa23Err)
	aa23WantOK(aa23T, "abort", aa23F.aa23Registry.AA23AbortPrepared(aa23Ref))
	_, aa23Err = aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(1, 0xA2), aa23F.aa23Now())
	aa23WantErr(aa23T, "wrap", aa23Err, ErrAA23CounterExhausted)
}

func TestAA23WritingStaysInvisibleAndPublishIsComplete(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Ref, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(2, 0xA2), aa23F.aa23Now())
	aa23WantOK(aa23T, "prepare", aa23Err)
	aa23Pool := aa23F.aa23Registry.AA23Pool()
	if aa23Pool.AA23SlotState(aa23Ref.AA23Index) != AA23SlotWriting {
		aa23T.Fatal("not writing")
	}
	aa23Early := AA23PublishNotice{AA23Slot: aa23Ref, AA23Identity: aa23F.aa23Identity(2)}
	_, _, aa23Err = aa23F.aa23View.AA23AdoptPlan(aa23Early, aa23F.aa23Now())
	aa23WantErr(aa23T, "adopt while writing", aa23Err, ErrAA23SlotState)
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotPublished {
		aa23T.Fatal("preparing displaced the predecessor")
	}
	aa23Pin := aa23F.aa23MustSelect()
	if aa23Pin.AA23Key().AA23Identity.AA23Plan != 1 {
		aa23T.Fatalf("selected %+v", aa23Pin.AA23Key())
	}
	_, _, aa23Err = aa23Pin.AA23Release()
	aa23WantOK(aa23T, "release", aa23Err)
	aa23Notice, aa23Err := aa23F.aa23Registry.AA23PublishPrepared(aa23Ref, aa23F.aa23Now())
	aa23WantOK(aa23T, "publish", aa23Err)
	if aa23Notice.AA23Identity != aa23F.aa23Identity(2) || aa23Notice.AA23Slot != aa23Ref {
		aa23T.Fatalf("notice %+v", aa23Notice)
	}
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("superseded plan must stay retained")
	}
	_, aa23Err = aa23F.aa23Registry.AA23PublishPrepared(aa23Ref, aa23F.aa23Now())
	aa23WantErr(aa23T, "publish twice", aa23Err, ErrAA23SlotState)
	aa23WantErr(aa23T, "abort published", aa23F.aa23Registry.AA23AbortPrepared(aa23Ref), ErrAA23SlotState)
}

func TestAA23ChangesDuringWritingAbortWithoutReplacing(aa23T *aa23testing.T) {
	aa23Cases := []struct {
		aa23Name         string
		aa23Change       func(*aa23Fixture)
		aa23Want         error
		aa23PredecessorP bool
	}{
		{"unregister", func(aa23F *aa23Fixture) { aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key) }, ErrAA23SlotState, false},
		{"source", func(aa23F *aa23Fixture) {
			aa23WantOK(aa23F.aa23T, "advance", aa23F.aa23Registry.AA23AdvanceSource(aa23F.aa23Key, aa23FixtureSource+1, aa23FixtureFarExpiry, aa23F.aa23Now()))
		}, ErrAA23SlotState, false},
		{"stream", func(aa23F *aa23Fixture) {
			aa23WantOK(aa23F.aa23T, "bind", aa23F.aa23Registry.AA23BindWorker(aa23FixtureWorker, aa23FixtureStream+1))
		}, ErrAA23SlotState, false},
		{"planExpires", func(aa23F *aa23Fixture) { aa23F.aa23Tick = aa23FixtureFarExpiry }, ErrAA23Expired, true},
	}
	for _, aa23Case := range aa23Cases {
		aa23F := aa23NewFixture(aa23T)
		aa23First := aa23F.aa23Install(1, 0xA1)
		aa23Ref, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(2, 0xA2), aa23F.aa23Now())
		aa23WantOK(aa23T, aa23Case.aa23Name, aa23Err)
		aa23Case.aa23Change(aa23F)
		_, aa23Err = aa23F.aa23Registry.AA23PublishPrepared(aa23Ref, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name, aa23Err, aa23Case.aa23Want)
		aa23Pool := aa23F.aa23Registry.AA23Pool()
		if aa23Pool.AA23SlotState(aa23Ref.AA23Index) != AA23SlotFree {
			aa23T.Fatalf("%s: stale writing slot not returned", aa23Case.aa23Name)
		}
		aa23Published := aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) == AA23SlotPublished
		if aa23Published != aa23Case.aa23PredecessorP {
			aa23T.Fatalf("%s: predecessor published = %v", aa23Case.aa23Name, aa23Published)
		}
	}
}

func TestAA23PublishedBytesAreDetachedFromCallerBuffers(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Candidate := aa23F.aa23Candidate(1, 0xA1)
	aa23Candidate.AA23Entries[0].AA23Request = []byte{0x42}
	aa23Candidate.AA23Entries[0].AA23RequestLength = 1
	aa23Want := append([]byte(nil), aa23Candidate.AA23Entries[0].AA23EncodedResponse...)
	aa23Notice := aa23F.aa23Publish(aa23Candidate)
	aa23Candidate.AA23Entries[0].AA23Request[0] = 0x43
	aa23Candidate.AA23Entries[0].AA23EncodedResponse[0] = 0x00
	_, _, aa23Err := aa23F.aa23View.AA23AdoptPlan(aa23Notice, aa23F.aa23Now())
	aa23WantOK(aa23T, "adopt", aa23Err)
	aa23Pin := aa23F.aa23MustSelect()
	aa23Got, aa23Err := aa23F.aa23ResponseBytes(aa23Pin)
	aa23WantOK(aa23T, "response", aa23Err)
	if string(aa23Got) != string(aa23Want) {
		aa23T.Fatalf("response bytes changed: %x want %x", aa23Got, aa23Want)
	}
	aa23Query := AA23RequestQuery{AA23Source: aa23FixtureInitiator, AA23Destination: aa23FixtureAddress, AA23PB: 0x07, AA23SB: 0x04, AA23Data: []byte{0x42}}
	_, aa23Err = aa23Pin.AA23Match(aa23Pin.AA23Key(), aa23Query, aa23F.aa23Now())
	aa23WantOK(aa23T, "original request", aa23Err)
	aa23Query.AA23Data = []byte{0x43}
	_, aa23Err = aa23Pin.AA23Match(aa23Pin.AA23Key(), aa23Query, aa23F.aa23Now())
	aa23WantErr(aa23T, "mutated request", aa23Err, ErrAA23NoMatch)
}

func TestAA23PreparedUpdateNeverMixesInFlightResponse(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Pin := aa23F.aa23MustSelect()
	aa23Key := aa23Pin.AA23Key()
	aa23Length, aa23Err := aa23Pin.AA23ResponseLength(aa23Key, 0, aa23F.aa23Now())
	aa23WantOK(aa23T, "length", aa23Err)
	aa23WantFirst := aa23MustEncode(aa23T, aa23FixtureData(0xA1))
	aa23WantSecond := aa23MustEncode(aa23T, aa23FixtureData(0xA2))
	if string(aa23WantFirst) == string(aa23WantSecond) {
		aa23T.Fatal("fixture generations must differ")
	}
	aa23Got := make([]byte, 0, aa23Length)
	for aa23Offset := uint32(0); aa23Offset < aa23Length; aa23Offset++ {
		if aa23Offset == 2 {
			aa23Notice := aa23F.aa23Publish(aa23F.aa23Candidate(2, 0xA2))
			_, aa23Ready, aa23Err := aa23F.aa23View.AA23AdoptPlan(aa23Notice, aa23F.aa23Now())
			aa23WantOK(aa23T, "adopt mid response", aa23Err)
			if aa23Ready {
				aa23T.Fatal("acknowledged a plan still held by a reader")
			}
		}
		aa23Symbol, aa23Err := aa23Pin.AA23ResponseSymbol(aa23Key, 0, aa23Offset, AA23PhaseResponse, aa23F.aa23Now())
		aa23WantOK(aa23T, "symbol", aa23Err)
		aa23Got = append(aa23Got, aa23Symbol)
	}
	if string(aa23Got) != string(aa23WantFirst) {
		aa23T.Fatalf("in-flight bytes mixed: %x want %x", aa23Got, aa23WantFirst)
	}
	aa23Ack, aa23Ready, aa23Err := aa23Pin.AA23Release()
	aa23WantOK(aa23T, "release", aa23Err)
	if !aa23Ready || aa23Ack.AA23Identity != aa23First.AA23Identity || aa23Ack.AA23Slot != aa23First.AA23Slot {
		aa23T.Fatalf("release ack %+v ready %v", aa23Ack, aa23Ready)
	}
	aa23WantOK(aa23T, "ack", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Ack))
	aa23Next := aa23F.aa23MustSelect()
	if aa23Next.AA23Key().AA23Identity.AA23Plan != 2 {
		aa23T.Fatalf("next transaction %+v", aa23Next.AA23Key())
	}
	aa23Second, aa23Err := aa23F.aa23ResponseBytes(aa23Next)
	aa23WantOK(aa23T, "next response", aa23Err)
	if string(aa23Second) != string(aa23WantSecond) {
		aa23T.Fatalf("next transaction bytes %x", aa23Second)
	}
}

func TestAA23RetainedSlotIsNotReusedBeforeAcknowledgement(aa23T *aa23testing.T) {
	aa23Limits := aa23FixtureLimits
	aa23Limits.AA23MaxPlanSlots = 2
	aa23F := aa23NewFixtureWithLimits(aa23T, aa23Limits)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Pin := aa23F.aa23MustSelect()
	aa23Second := aa23F.aa23Publish(aa23F.aa23Candidate(2, 0xA2))
	aa23Pool := aa23F.aa23Registry.AA23Pool()

	_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(3, 0xA3), aa23F.aa23Now())
	aa23WantErr(aa23T, "pool pressure", aa23Err, ErrAA23PoolExhausted)
	if aa23Got, aa23Want := aa23Pool.AA23Counts(), (AA23SlotCounts{AA23Published: 1, AA23Retained: 1}); aa23Got != aa23Want {
		aa23T.Fatalf("pool under pressure: %+v", aa23Got)
	}
	aa23WantErr(aa23T, "reclaim retained", aa23Pool.AA23Reclaim(aa23First.AA23Slot), ErrAA23SlotState)
	aa23WantErr(aa23T, "reclaim published", aa23Pool.AA23Reclaim(aa23Second.AA23Slot), ErrAA23SlotState)
	aa23Forged := AA23Acknowledgement(aa23First)
	aa23WantErr(aa23T, "ack while pinned", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Forged), ErrAA23SlotPinned)
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("forged acknowledgement moved a pinned slot")
	}
	aa23Published := AA23Acknowledgement(aa23Second)
	aa23WantErr(aa23T, "ack of a published plan", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Published), ErrAA23SlotState)

	aa23Bytes, aa23Err := aa23F.aa23ResponseBytes(aa23Pin)
	aa23WantOK(aa23T, "pinned bytes under pressure", aa23Err)
	if string(aa23Bytes) != string(aa23MustEncode(aa23T, aa23FixtureData(0xA1))) {
		aa23T.Fatalf("retained bytes overwritten: %x", aa23Bytes)
	}
	_, aa23Ready, aa23Err := aa23Pin.AA23Release()
	aa23WantOK(aa23T, "release", aa23Err)
	if aa23Ready {
		aa23T.Fatal("no retirement observed yet, so no acknowledgement is due")
	}
	aa23F.aa23Settle()
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotReclaimable {
		aa23T.Fatal("not reclaimable after acknowledgement")
	}
	_, aa23Err = aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(3, 0xA3), aa23F.aa23Now())
	aa23WantErr(aa23T, "reclaimable is not yet free", aa23Err, ErrAA23PoolExhausted)
	aa23Pending := aa23Pool.AA23PendingReclaims(make([]AA23SlotRef, 0, 2))
	if len(aa23Pending) != 1 || aa23Pending[0] != aa23First.AA23Slot {
		aa23T.Fatalf("pending reclaims %+v", aa23Pending)
	}
	aa23WantOK(aa23T, "reclaim", aa23Pool.AA23Reclaim(aa23First.AA23Slot))
	aa23Ref, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(3, 0xA3), aa23F.aa23Now())
	aa23WantOK(aa23T, "prepare after reclaim", aa23Err)
	if aa23Ref.AA23Index != aa23First.AA23Slot.AA23Index || aa23Ref.AA23Incarnation != aa23First.AA23Slot.AA23Incarnation+1 {
		aa23T.Fatalf("slot reuse without a new incarnation: %+v", aa23Ref)
	}
}

func TestAA23OldReferencesCannotReachAReusedSlot(aa23T *aa23testing.T) {
	aa23Limits := aa23FixtureLimits
	aa23Limits.AA23MaxPlanSlots = 1
	aa23F := aa23NewFixtureWithLimits(aa23T, aa23Limits)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Pool := aa23F.aa23Registry.AA23Pool()
	aa23Pin := aa23F.aa23MustSelect()
	_, _, aa23Err := aa23Pin.AA23Release()
	aa23WantOK(aa23T, "release", aa23Err)
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	aa23F.aa23Settle()
	aa23WantOK(aa23T, "reclaim", aa23Pool.AA23Reclaim(aa23First.AA23Slot))

	aa23F.aa23Key.AA23Epoch = 2
	aa23F.aa23MustRegister(aa23F.aa23Registration(aa23F.aa23Key))
	aa23Second := aa23F.aa23Publish(aa23F.aa23Candidate(2, 0xA2))
	if aa23Second.AA23Slot.AA23Index != aa23First.AA23Slot.AA23Index {
		aa23T.Fatal("fixture must reuse the only slot")
	}

	aa23OldAck := AA23Acknowledgement(aa23First)
	aa23WantErr(aa23T, "old ack", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23OldAck), ErrAA23SlotState)
	aa23OldNotice := AA23RevocationNotice{AA23Slot: aa23First.AA23Slot, AA23Identity: aa23First.AA23Identity, AA23Reason: AA23RetireRevoked}
	_, _, aa23Err = aa23F.aa23View.AA23ObserveRevocation(aa23OldNotice)
	aa23WantErr(aa23T, "old notice", aa23Err, ErrAA23SlotState)
	_, _, aa23Err = aa23F.aa23View.AA23AdoptPlan(aa23First, aa23F.aa23Now())
	aa23WantErr(aa23T, "old publish notice", aa23Err, ErrAA23SlotState)
	aa23WantErr(aa23T, "old reclaim", aa23Pool.AA23Reclaim(aa23First.AA23Slot), ErrAA23SlotState)
	aa23WantErr(aa23T, "old abort", aa23F.aa23Registry.AA23AbortPrepared(aa23First.AA23Slot), ErrAA23SlotState)
	_, _, aa23Err = aa23Pin.AA23Release()
	aa23WantErr(aa23T, "old pin", aa23Err, ErrAA23PinReleased)
	if aa23Pool.AA23SlotState(aa23Second.AA23Slot.AA23Index) != AA23SlotPublished {
		aa23T.Fatal("stale reference disturbed the successor")
	}
	aa23OutOfRange := AA23SlotRef{AA23Index: 9, AA23Incarnation: 1}
	aa23WantErr(aa23T, "out of range", aa23Pool.AA23Reclaim(aa23OutOfRange), ErrAA23SlotState)
	if aa23Pool.AA23SlotState(9) != AA23SlotInvalid {
		aa23T.Fatal("out of range state")
	}
}

func TestAA23SlotIncarnationRefusesWrap(aa23T *aa23testing.T) {
	aa23Limits := aa23FixtureLimits
	aa23Limits.AA23MaxPlanSlots = 1
	aa23F := aa23NewFixtureWithLimits(aa23T, aa23Limits)
	aa23Pool := aa23F.aa23Registry.AA23Pool()
	aa23Pool.aa23Slots[0].aa23Incarnation = aa23math.MaxUint64
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	aa23F.aa23Settle()
	aa23WantErr(aa23T, "reclaim at the ceiling", aa23Pool.AA23Reclaim(aa23First.AA23Slot), ErrAA23CounterExhausted)
	if aa23Pool.AA23SlotState(0) != AA23SlotReclaimable || aa23Pool.aa23Slots[0].aa23Incarnation != aa23math.MaxUint64 {
		aa23T.Fatal("exhausted slot must stay out of service without wrapping")
	}
	aa23F.aa23Key.AA23Epoch = 2
	aa23F.aa23MustRegister(aa23F.aa23Registration(aa23F.aa23Key))
	_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(2, 0xA2), aa23F.aa23Now())
	aa23WantErr(aa23T, "capacity shrinks", aa23Err, ErrAA23PoolExhausted)

	aa23Writer := aa23NewFixtureWithLimits(aa23T, aa23Limits)
	aa23Writer.aa23Registry.AA23Pool().aa23Slots[0].aa23Incarnation = aa23math.MaxUint64
	aa23Ref, aa23Err := aa23Writer.aa23Registry.AA23PreparePlan(aa23Writer.aa23Candidate(1, 0xA1), aa23Writer.aa23Now())
	aa23WantOK(aa23T, "prepare", aa23Err)
	aa23WantErr(aa23T, "abort at the ceiling", aa23Writer.aa23Registry.AA23AbortPrepared(aa23Ref), ErrAA23CounterExhausted)
}

func TestAA23AcknowledgementVariesEveryIdentityAxis(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	aa23Pool := aa23F.aa23Registry.AA23Pool()
	aa23Exact := AA23Acknowledgement(aa23First)
	for _, aa23Axis := range aa23IdentityAxes {
		aa23Stale := aa23Exact
		aa23Axis.aa23Mutate(&aa23Stale.AA23Identity)
		aa23WantErr(aa23T, aa23Axis.aa23Name, aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Stale), ErrAA23IdentityMismatch)
		if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
			aa23T.Fatalf("%s: stale acknowledgement moved the slot", aa23Axis.aa23Name)
		}
	}
	aa23Incarnation := aa23Exact
	aa23Incarnation.AA23Slot.AA23Incarnation++
	aa23WantErr(aa23T, "incarnation", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Incarnation), ErrAA23SlotState)
	aa23Index := aa23Exact
	aa23Index.AA23Slot.AA23Index++
	aa23WantErr(aa23T, "index", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Index), ErrAA23SlotState)
	aa23WantOK(aa23T, "exact", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Exact))
	aa23WantErr(aa23T, "repeat", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Exact), ErrAA23SlotState)
}

func TestAA23AdoptVariesEveryIdentityAxis(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Notice := aa23F.aa23Publish(aa23F.aa23Candidate(1, 0xA1))
	for _, aa23Axis := range aa23IdentityAxes {
		aa23Stale := aa23Notice
		aa23Axis.aa23Mutate(&aa23Stale.AA23Identity)
		_, _, aa23Err := aa23F.aa23View.AA23AdoptPlan(aa23Stale, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Axis.aa23Name, aa23Err, ErrAA23IdentityMismatch)
	}
	aa23OtherWorker, aa23Err := AA23NewWorkerView(aa23F.aa23Registry.AA23Pool(), aa23FixtureWorker+1, aa23FixtureStream)
	aa23WantOK(aa23T, "worker view", aa23Err)
	_, _, aa23Err = aa23OtherWorker.AA23AdoptPlan(aa23Notice, aa23F.aa23Now())
	aa23WantErr(aa23T, "other worker", aa23Err, ErrAA23IdentityMismatch)
	aa23OtherStream, aa23Err := AA23NewWorkerView(aa23F.aa23Registry.AA23Pool(), aa23FixtureWorker, aa23FixtureStream+1)
	aa23WantOK(aa23T, "stream view", aa23Err)
	_, _, aa23Err = aa23OtherStream.AA23AdoptPlan(aa23Notice, aa23F.aa23Now())
	aa23WantErr(aa23T, "other stream", aa23Err, ErrAA23IdentityMismatch)
	aa23Replay, aa23Err := AA23NewWorkerView(aa23F.aa23Registry.AA23Pool(), aa23FixtureWorker, aa23FixtureStream)
	aa23WantOK(aa23T, "replay view", aa23Err)
	aa23Replay.aa23PlanTop[aa23FixtureAddress] = aa23Notice.AA23Identity.AA23Plan
	_, _, aa23Err = aa23Replay.AA23AdoptPlan(aa23Notice, aa23F.aa23Now())
	aa23WantErr(aa23T, "plan generation replay", aa23Err, ErrAA23StaleGeneration)
	_, _, aa23Err = aa23F.aa23View.AA23AdoptPlan(aa23Notice, aa23F.aa23Now())
	aa23WantOK(aa23T, "exact", aa23Err)
	aa23F.aa23MustSelect()
}

func TestAA23WireKeyVariesEveryAxisAtTheOutputBoundary(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Install(1, 0xA1)
	aa23Pin := aa23F.aa23MustSelect()
	aa23Exact := aa23Pin.AA23Key()
	if aa23Exact.AA23Identity != aa23F.aa23Identity(1) || aa23Exact.AA23Transaction != aa23F.aa23Txn {
		aa23T.Fatalf("frozen key %+v", aa23Exact)
	}
	aa23WantOK(aa23T, "exact", aa23Pin.AA23Validate(aa23Exact, aa23F.aa23Now()))
	aa23Check := func(aa23Label string, aa23Stale AA23WireKey) {
		aa23T.Helper()
		aa23WantErr(aa23T, aa23Label, aa23Pin.AA23Validate(aa23Stale, aa23F.aa23Now()), ErrAA23IdentityMismatch)
		_, aa23Err := aa23Pin.AA23ResponseSymbol(aa23Stale, 0, 0, AA23PhaseResponse, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Label+" symbol", aa23Err, ErrAA23IdentityMismatch)
		_, aa23Err = aa23Pin.AA23ResponseLength(aa23Stale, 0, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Label+" length", aa23Err, ErrAA23IdentityMismatch)
		aa23Query := AA23RequestQuery{AA23Source: aa23FixtureInitiator, AA23Destination: aa23FixtureAddress, AA23PB: 0x07, AA23SB: 0x04}
		_, aa23Err = aa23Pin.AA23Match(aa23Stale, aa23Query, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Label+" match", aa23Err, ErrAA23IdentityMismatch)
	}
	for _, aa23Axis := range aa23IdentityAxes {
		aa23Stale := aa23Exact
		aa23Axis.aa23Mutate(&aa23Stale.AA23Identity)
		aa23Check(aa23Axis.aa23Name, aa23Stale)
	}
	aa23Transaction := aa23Exact
	aa23Transaction.AA23Transaction++
	aa23Check("transaction", aa23Transaction)
	aa23WantOK(aa23T, "still valid", aa23Pin.AA23Validate(aa23Exact, aa23F.aa23Now()))
	_, aa23Err := aa23Pin.AA23ResponseSymbol(aa23Exact, 0, 0, AA23PhaseTargetACK, aa23F.aa23Now())
	aa23WantErr(aa23T, "phase", aa23Err, ErrAA23Phase)
	_, aa23Err = aa23Pin.AA23ResponseSymbol(aa23Exact, 1, 0, AA23PhaseResponse, aa23F.aa23Now())
	aa23WantErr(aa23T, "entry", aa23Err, ErrAA23NoMatch)
	_, aa23Err = aa23Pin.AA23ResponseLength(aa23Exact, 1, aa23F.aa23Now())
	aa23WantErr(aa23T, "length entry", aa23Err, ErrAA23NoMatch)
	aa23Length, aa23Err := aa23Pin.AA23ResponseLength(aa23Exact, 0, aa23F.aa23Now())
	aa23WantOK(aa23T, "length", aa23Err)
	_, aa23Err = aa23Pin.AA23ResponseSymbol(aa23Exact, 0, aa23Length, AA23PhaseResponse, aa23F.aa23Now())
	aa23WantErr(aa23T, "past the end", aa23Err, ErrAA23Oversize)
	var aa23Zero AA23PlanPin
	aa23WantErr(aa23T, "zero pin", aa23Zero.AA23Validate(aa23Exact, aa23F.aa23Now()), ErrAA23PinReleased)
	_, _, aa23Err = aa23Zero.AA23Release()
	aa23WantErr(aa23T, "zero release", aa23Err, ErrAA23PinReleased)
}

func TestAA23MatchVariesEverySelector(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Candidate := aa23F.aa23Candidate(1, 0xA1)
	aa23Candidate.AA23Entries[0].AA23Request = []byte{0x01, 0x02}
	aa23Candidate.AA23Entries[0].AA23RequestLength = 2
	aa23Any := aa23F.aa23Entry(0xB1)
	aa23Any.AA23Source = AA23SourceConstraint{AA23Any: true}
	aa23Any.AA23SB = 0x05
	aa23Candidate.AA23Entries = append(aa23Candidate.AA23Entries, aa23Any)
	aa23Notice := aa23F.aa23Publish(aa23Candidate)
	_, _, aa23Err := aa23F.aa23View.AA23AdoptPlan(aa23Notice, aa23F.aa23Now())
	aa23WantOK(aa23T, "adopt", aa23Err)
	aa23Pin := aa23F.aa23MustSelect()
	aa23Key := aa23Pin.AA23Key()
	aa23Exact := AA23RequestQuery{AA23Source: aa23FixtureInitiator, AA23Destination: aa23FixtureAddress, AA23PB: 0x07, AA23SB: 0x04, AA23Data: []byte{0x01, 0x02}}
	aa23Entry, aa23Err := aa23Pin.AA23Match(aa23Key, aa23Exact, aa23F.aa23Now())
	aa23WantOK(aa23T, "exact", aa23Err)
	if aa23Entry != 0 {
		aa23T.Fatalf("entry %d", aa23Entry)
	}
	aa23Cases := []struct {
		aa23Name   string
		aa23Mutate func(*AA23RequestQuery)
	}{
		{"source", func(aa23Q *AA23RequestQuery) { aa23Q.AA23Source = aa23FixtureOtherSource }},
		{"sourceNotInitiator", func(aa23Q *AA23RequestQuery) { aa23Q.AA23Source = aa23FixtureOtherAddress }},
		{"destination", func(aa23Q *AA23RequestQuery) { aa23Q.AA23Destination = aa23FixtureOtherAddress }},
		{"pb", func(aa23Q *AA23RequestQuery) { aa23Q.AA23PB++ }},
		{"sb", func(aa23Q *AA23RequestQuery) { aa23Q.AA23SB = 0x06 }},
		{"lengthShort", func(aa23Q *AA23RequestQuery) { aa23Q.AA23Data = []byte{0x01} }},
		{"anySourceStillNeedsInitiator", func(aa23Q *AA23RequestQuery) {
			aa23Q.AA23Source = aa23FixtureOtherAddress
			aa23Q.AA23SB = 0x05
			aa23Q.AA23Data = nil
		}},
		{"lengthLong", func(aa23Q *AA23RequestQuery) { aa23Q.AA23Data = []byte{0x01, 0x02, 0x03} }},
		{"firstByte", func(aa23Q *AA23RequestQuery) { aa23Q.AA23Data = []byte{0x09, 0x02} }},
		{"lastByte", func(aa23Q *AA23RequestQuery) { aa23Q.AA23Data = []byte{0x01, 0x09} }},
	}
	for _, aa23Case := range aa23Cases {
		aa23Query := aa23Exact
		aa23Case.aa23Mutate(&aa23Query)
		_, aa23Err := aa23Pin.AA23Match(aa23Key, aa23Query, aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name, aa23Err, ErrAA23NoMatch)
	}
	aa23AnyQuery := AA23RequestQuery{AA23Source: aa23FixtureOtherSource, AA23Destination: aa23FixtureAddress, AA23PB: 0x07, AA23SB: 0x05}
	aa23Entry, aa23Err = aa23Pin.AA23Match(aa23Key, aa23AnyQuery, aa23F.aa23Now())
	aa23WantOK(aa23T, "any source", aa23Err)
	if aa23Entry != 1 {
		aa23T.Fatalf("any source entry %d", aa23Entry)
	}
	aa23Length, aa23Err := aa23Pin.AA23ResponseLength(aa23Key, 1, aa23F.aa23Now())
	aa23WantOK(aa23T, "second length", aa23Err)
	aa23Want := aa23MustEncode(aa23T, aa23FixtureData(0xB1))
	if uint64(aa23Length) != uint64(len(aa23Want)) {
		aa23T.Fatalf("second length %d", aa23Length)
	}
	for aa23Offset := range aa23Want {
		aa23Symbol, aa23Err := aa23Pin.AA23ResponseSymbol(aa23Key, 1, uint32(aa23Offset), AA23PhaseResponse, aa23F.aa23Now())
		aa23WantOK(aa23T, "second symbol", aa23Err)
		if aa23Symbol != aa23Want[aa23Offset] {
			aa23T.Fatalf("second entry offset %d: %x", aa23Offset, aa23Symbol)
		}
	}
}

func TestAA23ExpiryEqualityIsExpiredForEveryDeadline(aa23T *aa23testing.T) {
	const aa23Deadline = AA23Tick(3000)
	aa23Cases := []struct {
		aa23Name   string
		aa23Lease  AA23Tick
		aa23Source AA23Tick
		aa23Plan   AA23Tick
	}{
		{"registrationLease", aa23Deadline, aa23FixtureFarExpiry, aa23FixtureFarExpiry},
		{"sourceLease", aa23FixtureFarExpiry, aa23Deadline, aa23FixtureFarExpiry},
		{"planExpiry", aa23FixtureFarExpiry, aa23FixtureFarExpiry, aa23Deadline},
	}
	aa23Setup := func(aa23Lease, aa23Source, aa23Plan AA23Tick) (*aa23Fixture, AA23PublishNotice) {
		aa23F := aa23NewFixture(aa23T)
		aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
		aa23F.aa23Key.AA23Epoch = 2
		aa23Registration := aa23F.aa23Registration(aa23F.aa23Key)
		aa23Registration.AA23LeaseExpiry = aa23Lease
		aa23Registration.AA23SourceLeaseExpiry = aa23Source
		aa23F.aa23MustRegister(aa23Registration)
		aa23Candidate := aa23F.aa23Candidate(1, 0xA1)
		aa23Candidate.AA23PlanExpiry = aa23Plan
		return aa23F, aa23F.aa23Publish(aa23Candidate)
	}
	for _, aa23Case := range aa23Cases {
		aa23Before, aa23Notice := aa23Setup(aa23Case.aa23Lease, aa23Case.aa23Source, aa23Case.aa23Plan)
		aa23Before.aa23Tick = aa23Deadline - 1
		_, _, aa23Err := aa23Before.aa23View.AA23AdoptPlan(aa23Notice, aa23Before.aa23Now())
		aa23WantOK(aa23T, aa23Case.aa23Name+" adopt before", aa23Err)
		aa23Pin := aa23Before.aa23MustSelect()
		aa23WantOK(aa23T, aa23Case.aa23Name+" validate before", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23Before.aa23Now()))
		_, aa23Err = aa23Pin.AA23ResponseSymbol(aa23Pin.AA23Key(), 0, 0, AA23PhaseResponse, aa23Before.aa23Now())
		aa23WantOK(aa23T, aa23Case.aa23Name+" symbol before", aa23Err)
		aa23Before.aa23Tick = aa23Deadline
		aa23WantErr(aa23T, aa23Case.aa23Name+" validate at", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23Before.aa23Now()), ErrAA23Expired)
		_, aa23Err = aa23Pin.AA23ResponseSymbol(aa23Pin.AA23Key(), 0, 0, AA23PhaseResponse, aa23Before.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name+" symbol at", aa23Err, ErrAA23Expired)
		_, _, aa23Err = aa23Pin.AA23Release()
		aa23WantOK(aa23T, aa23Case.aa23Name+" release", aa23Err)
		_, aa23Err = aa23Before.aa23Select(aa23FixtureAddress)
		aa23WantErr(aa23T, aa23Case.aa23Name+" select at", aa23Err, ErrAA23Expired)

		aa23At, aa23AtNotice := aa23Setup(aa23Case.aa23Lease, aa23Case.aa23Source, aa23Case.aa23Plan)
		aa23At.aa23Tick = aa23Deadline
		_, _, aa23Err = aa23At.aa23View.AA23AdoptPlan(aa23AtNotice, aa23At.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name+" adopt at", aa23Err, ErrAA23Expired)
		_, aa23Err = aa23At.aa23Registry.AA23PreparePlan(aa23At.aa23Candidate(2, 0xA2), aa23At.aa23Now())
		if aa23Case.aa23Name == "planExpiry" {
			aa23WantOK(aa23T, "a candidate with a later plan expiry stays valid", aa23Err)
		} else {
			aa23WantErr(aa23T, aa23Case.aa23Name+" prepare at", aa23Err, ErrAA23Expired)
		}

		aa23Late, _ := aa23Setup(aa23Case.aa23Lease, aa23Case.aa23Source, aa23FixtureFarExpiry)
		aa23Ref, aa23Err := aa23Late.aa23Registry.AA23PreparePlan(aa23Late.aa23Candidate(2, 0xA2), aa23Late.aa23Now())
		if aa23Case.aa23Name == "planExpiry" {
			continue
		}
		aa23WantOK(aa23T, aa23Case.aa23Name+" prepare early", aa23Err)
		aa23Late.aa23Tick = aa23Deadline
		_, aa23Err = aa23Late.aa23Registry.AA23PublishPrepared(aa23Ref, aa23Late.aa23Now())
		aa23WantErr(aa23T, aa23Case.aa23Name+" publish at", aa23Err, ErrAA23Expired)
	}
}

func TestAA23RenewalNeverExtendsAPublishedPlan(aa23T *aa23testing.T) {
	const aa23Deadline = AA23Tick(3000)
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	aa23F.aa23Key.AA23Epoch = 2
	aa23Registration := aa23F.aa23Registration(aa23F.aa23Key)
	aa23Registration.AA23LeaseExpiry = aa23Deadline
	aa23F.aa23MustRegister(aa23Registration)
	aa23F.aa23Install(1, 0xA1)
	aa23Pin := aa23F.aa23MustSelect()

	aa23WantErr(aa23T, "shrink", aa23F.aa23Registry.AA23RenewRegistration(aa23F.aa23Key, aa23Deadline-1, aa23F.aa23Now()), ErrAA23StaleGeneration)
	aa23Stale := aa23F.aa23Key
	aa23Stale.AA23Epoch = 1
	aa23WantErr(aa23T, "stale key", aa23F.aa23Registry.AA23RenewRegistration(aa23Stale, aa23FixtureFarExpiry, aa23F.aa23Now()), ErrAA23NotRegistered)
	aa23WantOK(aa23T, "renew", aa23F.aa23Registry.AA23RenewRegistration(aa23F.aa23Key, aa23FixtureFarExpiry, aa23F.aa23Now()))
	if aa23Got := aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23LeaseExpiry; aa23Got != aa23FixtureFarExpiry {
		aa23T.Fatalf("lease %d", aa23Got)
	}
	aa23F.aa23Tick = aa23Deadline
	aa23WantErr(aa23T, "selected lease extended in place", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23F.aa23Now()), ErrAA23Expired)
	_, _, aa23Err := aa23Pin.AA23Release()
	aa23WantOK(aa23T, "release", aa23Err)
	_, aa23Err = aa23F.aa23Select(aa23FixtureAddress)
	aa23WantErr(aa23T, "published lease extended in place", aa23Err, ErrAA23Expired)
	aa23Notice := aa23F.aa23Install(2, 0xA2)
	aa23Fresh := aa23F.aa23MustSelect()
	if aa23Fresh.AA23Key().AA23Identity != aa23Notice.AA23Identity {
		aa23T.Fatalf("fresh generation %+v", aa23Fresh.AA23Key())
	}

	aa23F.aa23Tick = aa23FixtureFarExpiry
	aa23WantErr(aa23T, "expired lease cannot revive", aa23F.aa23Registry.AA23RenewRegistration(aa23F.aa23Key, aa23FixtureFarExpiry+1, aa23F.aa23Now()), ErrAA23Expired)
}

func TestAA23SourceGenerationFencesOlderPlans(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Stale := aa23F.aa23Key
	aa23Stale.AA23Subscriber++
	aa23WantErr(aa23T, "stale key", aa23F.aa23Registry.AA23AdvanceSource(aa23Stale, aa23FixtureSource+1, aa23FixtureFarExpiry, aa23F.aa23Now()), ErrAA23NotRegistered)
	aa23WantErr(aa23T, "equal source", aa23F.aa23Registry.AA23AdvanceSource(aa23F.aa23Key, aa23FixtureSource, aa23FixtureFarExpiry, aa23F.aa23Now()), ErrAA23StaleGeneration)
	aa23WantErr(aa23T, "lease at now", aa23F.aa23Registry.AA23AdvanceSource(aa23F.aa23Key, aa23FixtureSource+1, aa23F.aa23Tick, aa23F.aa23Now()), ErrAA23Expired)
	aa23Pool := aa23F.aa23Registry.AA23Pool()
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotPublished {
		aa23T.Fatal("refused advance retired a plan")
	}
	aa23WantOK(aa23T, "advance", aa23F.aa23Registry.AA23AdvanceSource(aa23F.aa23Key, aa23FixtureSource+1, aa23FixtureFarExpiry, aa23F.aa23Now()))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("older source plan stayed published")
	}
	_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(2, 0xA2), aa23F.aa23Now())
	aa23WantErr(aa23T, "older source candidate", aa23Err, ErrAA23IdentityMismatch)
	aa23Fresh := aa23F.aa23Candidate(2, 0xA2)
	aa23Fresh.AA23Identity.AA23Source = aa23FixtureSource + 1
	aa23F.aa23Publish(aa23Fresh)
	if _, aa23Err := aa23F.aa23Registry.AA23Register(aa23F.aa23Registration(aa23F.aa23Key), aa23F.aa23Now()); aa23Err != nil {
		aa23T.Fatalf("exact retry compares original content: %v", aa23Err)
	}
}

func TestAA23RevocationTakesEffectOnlyWhenObserved(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)

	aa23Unobserved := aa23F.aa23MustSelect()
	aa23WantOK(aa23T, "host publication is not observation", aa23Unobserved.AA23Validate(aa23Unobserved.AA23Key(), aa23F.aa23Now()))

	aa23Notices := aa23F.aa23Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 4))
	if len(aa23Notices) != 1 || aa23Notices[0].AA23Identity != aa23First.AA23Identity || aa23Notices[0].AA23Reason != AA23RetireRevoked {
		aa23T.Fatalf("notices %+v", aa23Notices)
	}
	aa23Ack, aa23Ready, aa23Err := aa23F.aa23View.AA23ObserveRevocation(aa23Notices[0])
	aa23WantOK(aa23T, "observe", aa23Err)
	if aa23Ready || aa23Ack != (AA23Acknowledgement{}) {
		aa23T.Fatal("acknowledged while a reader holds the plan")
	}
	aa23WantErr(aa23T, "validate", aa23Unobserved.AA23Validate(aa23Unobserved.AA23Key(), aa23F.aa23Now()), ErrAA23Revoked)
	_, aa23Err = aa23Unobserved.AA23ResponseSymbol(aa23Unobserved.AA23Key(), 0, 0, AA23PhaseResponse, aa23F.aa23Now())
	aa23WantErr(aa23T, "symbol", aa23Err, ErrAA23Revoked)
	aa23Ack, aa23Ready, aa23Err = aa23Unobserved.AA23Release()
	aa23WantOK(aa23T, "release", aa23Err)
	if !aa23Ready {
		aa23T.Fatal("release must yield the deferred acknowledgement")
	}
	aa23WantOK(aa23T, "ack", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Ack))
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationAbsent {
		aa23T.Fatal("not settled by the exact acknowledgement")
	}
	_, aa23Err = aa23F.aa23Select(aa23FixtureAddress)
	aa23WantErr(aa23T, "future admission", aa23Err, ErrAA23NoPlan)
}

func TestAA23RevocationNoticeVariesEveryIdentityAxis(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Published := AA23RevocationNotice{AA23Slot: aa23First.AA23Slot, AA23Identity: aa23First.AA23Identity, AA23Reason: AA23RetireRevoked}
	_, _, aa23Err := aa23F.aa23View.AA23ObserveRevocation(aa23Published)
	aa23WantErr(aa23T, "published slot cannot be revoked by a notice alone", aa23Err, ErrAA23SlotState)
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	aa23Exact := aa23F.aa23Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 1))[0]
	for _, aa23Axis := range aa23IdentityAxes {
		aa23Stale := aa23Exact
		aa23Axis.aa23Mutate(&aa23Stale.AA23Identity)
		_, _, aa23Err := aa23F.aa23View.AA23ObserveRevocation(aa23Stale)
		aa23WantErr(aa23T, aa23Axis.aa23Name, aa23Err, ErrAA23IdentityMismatch)
	}
	aa23Pin := aa23F.aa23MustSelect()
	aa23WantOK(aa23T, "stale notices did not fence", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23F.aa23Now()))
	_, _, aa23Err = aa23Pin.AA23Release()
	aa23WantOK(aa23T, "release", aa23Err)

	aa23OtherWorker, aa23Err := AA23NewWorkerView(aa23F.aa23Registry.AA23Pool(), aa23FixtureWorker+1, aa23FixtureStream)
	aa23WantOK(aa23T, "other worker", aa23Err)
	_, _, aa23Err = aa23OtherWorker.AA23ObserveRevocation(aa23Exact)
	aa23WantErr(aa23T, "other worker", aa23Err, ErrAA23IdentityMismatch)
	aa23OlderStream, aa23Err := AA23NewWorkerView(aa23F.aa23Registry.AA23Pool(), aa23FixtureWorker, aa23FixtureStream-1)
	aa23WantOK(aa23T, "older stream", aa23Err)
	_, _, aa23Err = aa23OlderStream.AA23ObserveRevocation(aa23Exact)
	aa23WantErr(aa23T, "a notice from a future stream", aa23Err, ErrAA23IdentityMismatch)

	aa23Ack, aa23Ready, aa23Err := aa23F.aa23View.AA23ObserveRevocation(aa23Exact)
	aa23WantOK(aa23T, "exact", aa23Err)
	if !aa23Ready {
		aa23T.Fatal("no reader, so the acknowledgement is due")
	}
	aa23Again, aa23Ready, aa23Err := aa23F.aa23View.AA23ObserveRevocation(aa23Exact)
	aa23WantOK(aa23T, "lost acknowledgement is recovered by redelivery", aa23Err)
	if !aa23Ready || aa23Again != aa23Ack {
		aa23T.Fatal("redelivery must repeat the same acknowledgement")
	}
	aa23WantOK(aa23T, "ack", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Again))
}

func TestAA23ObservingSupersessionStopsSelectionButFinishesTransaction(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Pin := aa23F.aa23MustSelect()
	aa23F.aa23Publish(aa23F.aa23Candidate(2, 0xA2))
	aa23Notices := aa23F.aa23Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 4))
	if len(aa23Notices) != 1 || aa23Notices[0].AA23Reason != AA23RetireSuperseded || aa23Notices[0].AA23Identity != aa23First.AA23Identity {
		aa23T.Fatalf("notices %+v", aa23Notices)
	}
	_, aa23Ready, aa23Err := aa23F.aa23View.AA23ObserveRevocation(aa23Notices[0])
	aa23WantOK(aa23T, "observe", aa23Err)
	if aa23Ready {
		aa23T.Fatal("acknowledged while held")
	}
	aa23Bytes, aa23Err := aa23F.aa23ResponseBytes(aa23Pin)
	aa23WantOK(aa23T, "selected transaction finishes", aa23Err)
	if string(aa23Bytes) != string(aa23MustEncode(aa23T, aa23FixtureData(0xA1))) {
		aa23T.Fatalf("bytes %x", aa23Bytes)
	}

	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	aa23Upgraded := aa23F.aa23Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 4))
	aa23Seen := 0
	for _, aa23Notice := range aa23Upgraded {
		if aa23Notice.AA23Reason != AA23RetireRevoked {
			aa23T.Fatalf("a stronger reason must replace a weaker one: %+v", aa23Notice)
		}
		if aa23Notice.AA23Identity == aa23First.AA23Identity {
			aa23Seen++
			_, _, aa23Err = aa23F.aa23View.AA23ObserveRevocation(aa23Notice)
			aa23WantOK(aa23T, "observe upgrade", aa23Err)
		}
	}
	if aa23Seen != 1 {
		aa23T.Fatalf("notices %+v", aa23Upgraded)
	}
	aa23WantErr(aa23T, "revocation overrides the finish allowance", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23F.aa23Now()), ErrAA23Revoked)
}

func TestAA23PendingRevocationsHonorsCapacityWithoutAllocating(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Install(1, 0xA1)
	aa23F.aa23Publish(aa23F.aa23Candidate(2, 0xA2))
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	aa23Buffer := make([]AA23RevocationNotice, 0, 1)
	if aa23Got := aa23F.aa23Registry.AA23PendingRevocations(aa23Buffer); len(aa23Got) != 1 {
		aa23T.Fatalf("capacity not honored: %d", len(aa23Got))
	}
	if aa23Got := aa23F.aa23Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 8)); len(aa23Got) != 2 {
		aa23T.Fatalf("pending %d", len(aa23Got))
	}
	aa23Refs := aa23F.aa23Registry.AA23Pool().AA23PendingReclaims(make([]AA23SlotRef, 0, 1))
	if len(aa23Refs) != 0 {
		aa23T.Fatalf("nothing is reclaimable yet: %+v", aa23Refs)
	}
	aa23Allocs := aa23testing.AllocsPerRun(10, func() {
		aa23F.aa23Registry.AA23PendingRevocations(aa23Buffer)
	})
	if aa23Allocs != 0 {
		aa23T.Fatalf("allocated %v", aa23Allocs)
	}
}

func TestAA23ExpiryQuarantinesUntilAcknowledgementOrTermination(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Pool := aa23F.aa23Registry.AA23Pool()
	aa23F.aa23Tick = aa23FixtureFarExpiry - 1
	aa23WantOK(aa23T, "before", aa23F.aa23Registry.AA23ExpireDue(aa23F.aa23Now()))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotPublished {
		aa23T.Fatal("retired before the deadline")
	}
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationActive {
		aa23T.Fatal("registration retired before the deadline")
	}
	aa23F.aa23Tick = aa23FixtureFarExpiry
	aa23WantOK(aa23T, "at", aa23F.aa23Registry.AA23ExpireDue(aa23F.aa23Now()))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("expiry must retain, never free, the slot")
	}
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationRetiring {
		aa23T.Fatal("expired registration must retire and hold its address")
	}
	aa23F.aa23Tick += 100000
	aa23WantOK(aa23T, "paused worker", aa23F.aa23Registry.AA23ExpireDue(aa23F.aa23Now()))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("elapsed time alone reclaimed storage")
	}
	aa23Notice := aa23F.aa23Registry.AA23PendingRevocations(make([]AA23RevocationNotice, 0, 1))[0]
	if aa23Notice.AA23Reason != AA23RetireExpired {
		aa23T.Fatalf("reason %v", aa23Notice.AA23Reason)
	}
	aa23Ack, aa23Ready, aa23Err := aa23F.aa23View.AA23ObserveRevocation(aa23Notice)
	aa23WantOK(aa23T, "observe", aa23Err)
	if !aa23Ready {
		aa23T.Fatal("no reader")
	}
	aa23WantOK(aa23T, "ack", aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Ack))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotReclaimable {
		aa23T.Fatal("not reclaimable")
	}
}

func TestAA23SourceLeaseExpiryRetiresPlanButKeepsRegistration(aa23T *aa23testing.T) {
	const aa23Deadline = AA23Tick(3000)
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	aa23F.aa23Key.AA23Epoch = 2
	aa23Registration := aa23F.aa23Registration(aa23F.aa23Key)
	aa23Registration.AA23SourceLeaseExpiry = aa23Deadline
	aa23F.aa23MustRegister(aa23Registration)
	aa23First := aa23F.aa23Publish(aa23F.aa23Candidate(1, 0xA1))
	aa23F.aa23Tick = aa23Deadline
	aa23WantOK(aa23T, "expire", aa23F.aa23Registry.AA23ExpireDue(aa23F.aa23Now()))
	if aa23F.aa23Registry.AA23Pool().AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("plan stayed published past its source lease")
	}
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationActive {
		aa23T.Fatal("an expired source lease does not end the registration")
	}
}

func TestAA23WorkerBindingAdvancesAndRetiresPredecessors(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(5, 0xA1)
	aa23Pool := aa23F.aa23Registry.AA23Pool()
	for _, aa23Case := range []struct {
		aa23Name   string
		aa23Worker AA23WorkerID
		aa23Stream AA23StreamGeneration
		aa23Want   error
	}{
		{"zeroWorker", 0, aa23FixtureStream, ErrAA23InvalidIdentity},
		{"zeroStream", aa23FixtureWorker, 0, ErrAA23InvalidIdentity},
		{"sameTuple", aa23FixtureWorker, aa23FixtureStream, ErrAA23StaleGeneration},
		{"olderStream", aa23FixtureWorker, aa23FixtureStream - 1, ErrAA23StaleGeneration},
		{"olderWorker", aa23FixtureWorker - 1, aa23FixtureStream + 1, ErrAA23StaleGeneration},
	} {
		aa23WantErr(aa23T, aa23Case.aa23Name, aa23F.aa23Registry.AA23BindWorker(aa23Case.aa23Worker, aa23Case.aa23Stream), aa23Case.aa23Want)
		if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotPublished {
			aa23T.Fatalf("%s: refused binding retired a plan", aa23Case.aa23Name)
		}
	}
	aa23Pin := aa23F.aa23MustSelect()
	aa23WantOK(aa23T, "new stream", aa23F.aa23Registry.AA23BindWorker(aa23FixtureWorker, aa23FixtureStream+1))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("old stream plan stayed published")
	}
	_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(6, 0xA2), aa23F.aa23Now())
	aa23WantErr(aa23T, "candidate of the old stream", aa23Err, ErrAA23IdentityMismatch)
	aa23Candidate := aa23F.aa23Candidate(5, 0xA2)
	aa23Candidate.AA23Identity.AA23Stream = aa23FixtureStream + 1
	_, aa23Err = aa23F.aa23Registry.AA23PreparePlan(aa23Candidate, aa23F.aa23Now())
	aa23WantErr(aa23T, "plan generation does not restart with a stream", aa23Err, ErrAA23StaleGeneration)

	aa23WantErr(aa23T, "view stream stale", aa23F.aa23View.AA23AdvanceStream(aa23FixtureStream), ErrAA23StaleGeneration)
	aa23WantOK(aa23T, "view stream", aa23F.aa23View.AA23AdvanceStream(aa23FixtureStream+1))
	aa23WantErr(aa23T, "pin of the old stream", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23F.aa23Now()), ErrAA23PinReleased)
	if aa23Pool.aa23Slots[aa23First.AA23Slot.AA23Index].aa23Pinned {
		aa23T.Fatal("stream change kept a slot pinned")
	}
	aa23F.aa23Txn = 0
	_, aa23Err = aa23F.aa23Select(aa23FixtureAddress)
	aa23WantErr(aa23T, "no plan survives a stream change", aa23Err, ErrAA23NoPlan)
	aa23F.aa23Settle()
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotReclaimable {
		aa23T.Fatal("old stream plan must still be acknowledged")
	}

	aa23Fresh := aa23F.aa23Candidate(6, 0xA2)
	aa23Fresh.AA23Identity.AA23Stream = aa23FixtureStream + 1
	aa23Notice := aa23F.aa23Publish(aa23Fresh)
	_, _, aa23Err = aa23F.aa23View.AA23AdoptPlan(aa23Notice, aa23F.aa23Now())
	aa23WantOK(aa23T, "adopt in new stream", aa23Err)
	aa23F.aa23MustSelect()
}

func TestAA23WorkerTerminationIsTheOnlyProoflessReclaimPath(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23First := aa23F.aa23Install(1, 0xA1)
	aa23Pin := aa23F.aa23MustSelect()
	aa23Pool := aa23F.aa23Registry.AA23Pool()
	aa23Next := aa23FixtureWorker + 1
	aa23WantOK(aa23T, "bind successor", aa23F.aa23Registry.AA23BindWorker(aa23Next, 1))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("a successor cannot inherit the predecessor's plan")
	}
	aa23WantErr(aa23T, "zero", aa23F.aa23Registry.AA23ObserveWorkerTerminated(0), ErrAA23InvalidIdentity)
	aa23WantErr(aa23T, "unknown", aa23F.aa23Registry.AA23ObserveWorkerTerminated(aa23Next+1), ErrAA23InvalidIdentity)
	aa23WantOK(aa23T, "termination of another worker", aa23F.aa23Registry.AA23ObserveWorkerTerminated(aa23Next))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
		aa23T.Fatal("termination of another worker reclaimed this slot")
	}
	_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(1, 0xA2), aa23F.aa23Now())
	aa23WantErr(aa23T, "no bound worker", aa23Err, ErrAA23NoWorker)
	aa23WantErr(aa23T, "terminated worker cannot rebind", aa23F.aa23Registry.AA23BindWorker(aa23Next, 2), ErrAA23StaleGeneration)

	aa23WantOK(aa23T, "terminated", aa23F.aa23Registry.AA23ObserveWorkerTerminated(aa23FixtureWorker))
	if aa23Pool.AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotReclaimable {
		aa23T.Fatal("termination proof must reclaim")
	}
	aa23WantErr(aa23T, "dead pin", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23F.aa23Now()), ErrAA23SlotState)
	_, aa23Err = aa23F.aa23Select(aa23FixtureAddress)
	aa23WantErr(aa23T, "dead view busy", aa23Err, ErrAA23PinBusy)
	_, _, aa23Err = aa23Pin.AA23Release()
	aa23WantOK(aa23T, "dead pin release", aa23Err)
	_, aa23Err = aa23F.aa23Select(aa23FixtureAddress)
	aa23WantErr(aa23T, "dead view select", aa23Err, ErrAA23SlotState)
	aa23WantOK(aa23T, "reclaim", aa23Pool.AA23Reclaim(aa23First.AA23Slot))
	_, aa23Err = aa23F.aa23Select(aa23FixtureAddress)
	aa23WantErr(aa23T, "dead view after reuse", aa23Err, ErrAA23SlotState)

	aa23WantOK(aa23T, "bind third", aa23F.aa23Registry.AA23BindWorker(aa23Next+1, 1))
	aa23Candidate := aa23F.aa23Candidate(1, 0xA2)
	aa23Candidate.AA23Identity.AA23Worker = aa23Next + 1
	aa23Candidate.AA23Identity.AA23Stream = 1
	aa23F.aa23Publish(aa23Candidate)
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationActive {
		aa23T.Fatal("termination of a worker must keep the registration")
	}
}

func TestAA23TerminationSettlesRetiringRegistration(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Install(1, 0xA1)
	aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationRetiring {
		aa23T.Fatal("not retiring")
	}
	aa23WantOK(aa23T, "terminated", aa23F.aa23Registry.AA23ObserveWorkerTerminated(aa23FixtureWorker))
	if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationAbsent {
		aa23T.Fatal("termination proof must settle the address")
	}
	_, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(2, 0xA2), aa23F.aa23Now())
	aa23WantErr(aa23T, "no worker bound", aa23Err, ErrAA23NoWorker)
}

func TestAA23TerminationAbortsWritingSlot(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Ref, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(1, 0xA1), aa23F.aa23Now())
	aa23WantOK(aa23T, "prepare", aa23Err)
	aa23WantOK(aa23T, "terminated", aa23F.aa23Registry.AA23ObserveWorkerTerminated(aa23FixtureWorker))
	if aa23F.aa23Registry.AA23Pool().AA23SlotState(aa23Ref.AA23Index) != AA23SlotFree {
		aa23T.Fatal("writing slot survived its worker")
	}
	_, aa23Err = aa23F.aa23Registry.AA23PublishPrepared(aa23Ref, aa23F.aa23Now())
	aa23WantErr(aa23T, "publish", aa23Err, ErrAA23SlotState)
}

func TestAA23ClockFaultsCloseAdmissionForGood(aa23T *aa23testing.T) {
	aa23Faults := []struct {
		aa23Name string
		aa23Now  AA23ClockReading
	}{
		{"regression", AA23ClockReading{AA23Domain: aa23FixtureDomain, AA23Tick: aa23FixtureStart - 1}},
		{"otherDomain", AA23ClockReading{AA23Domain: aa23FixtureDomain + 1, AA23Tick: aa23FixtureStart + 1}},
		{"zeroDomain", AA23ClockReading{AA23Tick: aa23FixtureStart + 1}},
	}
	for _, aa23Fault := range aa23Faults {
		aa23F := aa23NewFixture(aa23T)
		aa23First := aa23F.aa23Install(1, 0xA1)
		aa23Pin := aa23F.aa23MustSelect()

		aa23WantErr(aa23T, aa23Fault.aa23Name+" worker", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23Fault.aa23Now), ErrAA23ClockClosed)
		aa23WantErr(aa23T, aa23Fault.aa23Name+" worker stays closed", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23F.aa23Now()), ErrAA23ClockClosed)
		_, aa23Err := aa23F.aa23Select(aa23FixtureAddress)
		aa23WantErr(aa23T, aa23Fault.aa23Name+" select", aa23Err, ErrAA23ClockClosed)
		_, _, aa23Err = aa23Pin.AA23Release()
		aa23WantOK(aa23T, aa23Fault.aa23Name+" release", aa23Err)

		aa23WantErr(aa23T, aa23Fault.aa23Name+" host", aa23F.aa23Registry.AA23ExpireDue(aa23Fault.aa23Now), ErrAA23ClockClosed)
		if aa23F.aa23Registry.AA23Pool().AA23SlotState(aa23First.AA23Slot.AA23Index) != AA23SlotRetained {
			aa23T.Fatalf("%s: plan stayed published on a clock fault", aa23Fault.aa23Name)
		}
		_, aa23Err = aa23F.aa23Registry.AA23PreparePlan(aa23F.aa23Candidate(2, 0xA2), aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Fault.aa23Name+" prepare", aa23Err, ErrAA23ClockClosed)
		_, aa23Err = aa23F.aa23Registry.AA23Register(aa23F.aa23Registration(aa23F.aa23Key), aa23F.aa23Now())
		aa23WantErr(aa23T, aa23Fault.aa23Name+" register", aa23Err, ErrAA23ClockClosed)
		aa23F.aa23Registry.AA23Unregister(aa23F.aa23Key)
		if aa23F.aa23Registry.AA23Registration(aa23F.aa23Key).AA23State != AA23RegistrationRetiring {
			aa23T.Fatalf("%s: cleanup must stay available after a clock fault", aa23Fault.aa23Name)
		}
	}
}

func TestAA23ZeroDomainClosesAFreshClock(aa23T *aa23testing.T) {
	aa23Registry, aa23Err := AA23NewRegistry(aa23FixtureLimits, 0)
	aa23WantOK(aa23T, "new", aa23Err)
	aa23Zero := AA23ClockReading{AA23Tick: aa23FixtureStart}
	aa23Valid := AA23ClockReading{AA23Domain: aa23FixtureDomain, AA23Tick: aa23FixtureStart}
	aa23WantErr(aa23T, "host", aa23Registry.AA23ExpireDue(aa23Zero), ErrAA23ClockClosed)
	aa23WantErr(aa23T, "host stays closed", aa23Registry.AA23ExpireDue(aa23Valid), ErrAA23ClockClosed)
	aa23View, aa23Err := AA23NewWorkerView(aa23Registry.AA23Pool(), aa23FixtureWorker, aa23FixtureStream)
	aa23WantOK(aa23T, "view", aa23Err)
	_, aa23Err = aa23View.AA23SelectProvisional(aa23FixtureAddress, 1, aa23Zero)
	aa23WantErr(aa23T, "worker", aa23Err, ErrAA23ClockClosed)
	_, aa23Err = aa23View.AA23SelectProvisional(aa23FixtureAddress, 2, aa23Valid)
	aa23WantErr(aa23T, "worker stays closed", aa23Err, ErrAA23ClockClosed)
}

func TestAA23TransactionOrdinalAdvancesAndOnePinAtATime(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23F.aa23Install(1, 0xA1)
	_, aa23Err := aa23F.aa23View.AA23SelectProvisional(aa23FixtureAddress, 0, aa23F.aa23Now())
	aa23WantErr(aa23T, "zero", aa23Err, ErrAA23InvalidIdentity)
	aa23Pin, aa23Err := aa23F.aa23View.AA23SelectProvisional(aa23FixtureAddress, 5, aa23F.aa23Now())
	aa23WantOK(aa23T, "select", aa23Err)
	_, aa23Err = aa23F.aa23View.AA23SelectProvisional(aa23FixtureAddress, 5, aa23F.aa23Now())
	aa23WantErr(aa23T, "same ordinal", aa23Err, ErrAA23StaleTransaction)
	_, aa23Err = aa23F.aa23View.AA23SelectProvisional(aa23FixtureAddress, 4, aa23F.aa23Now())
	aa23WantErr(aa23T, "older ordinal", aa23Err, ErrAA23StaleTransaction)
	_, aa23Err = aa23F.aa23View.AA23SelectProvisional(aa23FixtureAddress, 6, aa23F.aa23Now())
	aa23WantErr(aa23T, "second pin", aa23Err, ErrAA23PinBusy)
	aa23WantOK(aa23T, "still held", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23F.aa23Now()))
	_, _, aa23Err = aa23Pin.AA23Release()
	aa23WantOK(aa23T, "release", aa23Err)
	_, _, aa23Err = aa23Pin.AA23Release()
	aa23WantErr(aa23T, "repeat release", aa23Err, ErrAA23PinReleased)
	aa23WantErr(aa23T, "released pin", aa23Pin.AA23Validate(aa23Pin.AA23Key(), aa23F.aa23Now()), ErrAA23PinReleased)
	_, aa23Err = aa23F.aa23View.AA23SelectProvisional(aa23FixtureAddress, 6, aa23F.aa23Now())
	aa23WantErr(aa23T, "ordinal consumed by a refused selection", aa23Err, ErrAA23StaleTransaction)
	_, aa23Err = aa23F.aa23View.AA23SelectProvisional(aa23FixtureOtherAddress, 7, aa23F.aa23Now())
	aa23WantErr(aa23T, "unregistered address", aa23Err, ErrAA23NoPlan)
	_, aa23Err = aa23F.aa23View.AA23SelectProvisional(aa23FixtureAddress, 8, aa23F.aa23Now())
	aa23WantOK(aa23T, "advance", aa23Err)
}

func TestAA23WorkerViewRefusesZeroIdentity(aa23T *aa23testing.T) {
	aa23Registry, aa23Err := AA23NewRegistry(aa23FixtureLimits, 0)
	aa23WantOK(aa23T, "new", aa23Err)
	_, aa23Err = AA23NewWorkerView(nil, 1, 1)
	aa23WantErr(aa23T, "pool", aa23Err, ErrAA23InvalidIdentity)
	_, aa23Err = AA23NewWorkerView(aa23Registry.AA23Pool(), 0, 1)
	aa23WantErr(aa23T, "worker", aa23Err, ErrAA23InvalidIdentity)
	_, aa23Err = AA23NewWorkerView(aa23Registry.AA23Pool(), 1, 0)
	aa23WantErr(aa23T, "stream", aa23Err, ErrAA23InvalidIdentity)
}

func TestAA23LeaseArithmeticRefusesOverflow(aa23T *aa23testing.T) {
	aa23Limits := aa23FixtureLimits
	aa23Limits.AA23MaxLeaseTicks = 1000
	aa23Registry, aa23Err := AA23NewRegistry(aa23Limits, 0)
	aa23WantOK(aa23T, "new", aa23Err)
	aa23Subscriber, aa23Err := aa23Registry.AA23OpenSubscriber()
	aa23WantOK(aa23T, "open", aa23Err)
	aa23Now := AA23ClockReading{AA23Domain: aa23FixtureDomain, AA23Tick: aa23math.MaxUint64 - 1500}
	aa23Registration := AA23Registration{
		AA23Key:               AA23RegistrationKey{AA23Subscriber: aa23Subscriber, AA23Address: aa23FixtureAddress, AA23Epoch: 1},
		AA23LeaseExpiry:       aa23math.MaxUint64,
		AA23Source:            aa23FixtureSource,
		AA23SourceLeaseExpiry: aa23math.MaxUint64 - 1500 + 1000,
		AA23Failure:           aa23FixtureSilence,
	}
	_, aa23Err = aa23Registry.AA23Register(aa23Registration, aa23Now)
	aa23WantErr(aa23T, "beyond the bound near the ceiling", aa23Err, ErrAA23LeaseTooLong)
	aa23Registration.AA23LeaseExpiry = aa23math.MaxUint64 - 1500 + 1000
	_, aa23Err = aa23Registry.AA23Register(aa23Registration, aa23Now)
	aa23WantOK(aa23T, "exactly the bound near the ceiling", aa23Err)
}

func TestAA23EncodeResponseFollowsNativeCRCAndEscaping(aa23T *aa23testing.T) {
	aa23CRC := func(aa23Data []byte) byte {
		var aa23Value byte
		for _, aa23Byte := range aa23Data {
			aa23Value = aa23CRCUpdate(aa23Value, aa23Byte)
		}
		return aa23Value
	}
	for _, aa23Vector := range []struct {
		aa23Data []byte
		aa23Want byte
	}{
		{nil, 0x00},
		{[]byte{0x01, 0x02}, 0x99},
		{[]byte{0x10, 0xFE, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA}, 0x77},
	} {
		if aa23Got := aa23CRC(aa23Vector.aa23Data); aa23Got != aa23Vector.aa23Want {
			aa23T.Fatalf("CRC(%x) = %02x, want %02x", aa23Vector.aa23Data, aa23Got, aa23Vector.aa23Want)
		}
	}
	aa23Wire, aa23Err := AA23EncodeResponse([]byte{0x55}, []byte{0xA9, 0xAA, 0x07})
	aa23WantOK(aa23T, "encode", aa23Err)
	aa23Sum := aa23CRC([]byte{0x03, 0xA9, 0xAA, 0x07})
	aa23Want := aa23AppendEscaped([]byte{0x55, 0x03, 0xA9, 0x00, 0xA9, 0x01, 0x07}, aa23Sum)
	if string(aa23Wire) != string(aa23Want) {
		aa23T.Fatalf("wire %x, want %x", aa23Wire, aa23Want)
	}
	aa23WantOK(aa23T, "own encoding validates", aa23ValidateEncodedResponse(aa23Wire[1:]))
	aa23Empty, aa23Err := AA23EncodeResponse(nil, nil)
	aa23WantOK(aa23T, "empty", aa23Err)
	if string(aa23Empty) != string([]byte{0x00, 0x00}) {
		aa23T.Fatalf("empty %x", aa23Empty)
	}
	aa23Prefix := []byte{0x55}
	aa23Kept, aa23Err := AA23EncodeResponse(aa23Prefix, make([]byte, aa23MaxNativeLength+1))
	aa23WantErr(aa23T, "over the length byte", aa23Err, ErrAA23Oversize)
	if string(aa23Kept) != string(aa23Prefix) {
		aa23T.Fatalf("refusal changed dst: %x", aa23Kept)
	}
	for aa23Value := 0; aa23Value < 256; aa23Value++ {
		aa23Round, aa23Err := AA23EncodeResponse(nil, []byte{byte(aa23Value)})
		aa23WantOK(aa23T, "round", aa23Err)
		aa23WantOK(aa23T, "round validates", aa23ValidateEncodedResponse(aa23Round))
	}
}

func TestAA23AddressClassificationCoversEveryOctet(aa23T *aa23testing.T) {
	aa23Initiators := map[byte]bool{}
	for _, aa23High := range []byte{0x0, 0x1, 0x3, 0x7, 0xF} {
		for _, aa23Low := range []byte{0x0, 0x1, 0x3, 0x7, 0xF} {
			aa23Initiators[aa23High<<4|aa23Low] = true
		}
	}
	for aa23Value := 0; aa23Value < 256; aa23Value++ {
		aa23Address := byte(aa23Value)
		aa23Want := !aa23Initiators[aa23Address] && aa23Address != 0xFE && aa23Address != 0xA9 && aa23Address != 0xAA
		if aa23Got := aa23IsTargetAddress(aa23Address); aa23Got != aa23Want {
			aa23T.Fatalf("target(%02x) = %v", aa23Address, aa23Got)
		}
		if aa23Got := aa23IsInitiatorAddress(aa23Address); aa23Got != aa23Initiators[aa23Address] {
			aa23T.Fatalf("initiator(%02x) = %v", aa23Address, aa23Got)
		}
	}
}

func TestAA23SteadyStatePathsAllocateNothing(aa23T *aa23testing.T) {
	aa23F := aa23NewFixture(aa23T)
	aa23Candidate := aa23F.aa23Candidate(1, 0xA1)
	aa23Plan := AA23PlanGeneration(0)
	aa23Query := AA23RequestQuery{AA23Source: aa23FixtureInitiator, AA23Destination: aa23FixtureAddress, AA23PB: 0x07, AA23SB: 0x04}
	aa23Buffer := make([]AA23RevocationNotice, 0, 4)
	aa23Allocs := aa23testing.AllocsPerRun(50, func() {
		aa23Plan++
		aa23Candidate.AA23Identity.AA23Plan = aa23Plan
		aa23Ref, aa23Err := aa23F.aa23Registry.AA23PreparePlan(aa23Candidate, aa23F.aa23Now())
		if aa23Err != nil {
			aa23T.Fatal(aa23Err)
		}
		aa23Notice, aa23Err := aa23F.aa23Registry.AA23PublishPrepared(aa23Ref, aa23F.aa23Now())
		if aa23Err != nil {
			aa23T.Fatal(aa23Err)
		}
		if _, _, aa23Err := aa23F.aa23View.AA23AdoptPlan(aa23Notice, aa23F.aa23Now()); aa23Err != nil {
			aa23T.Fatal(aa23Err)
		}
		aa23F.aa23Txn++
		aa23Pin, aa23Err := aa23F.aa23View.AA23SelectProvisional(aa23FixtureAddress, aa23F.aa23Txn, aa23F.aa23Now())
		if aa23Err != nil {
			aa23T.Fatal(aa23Err)
		}
		aa23Key := aa23Pin.AA23Key()
		if _, aa23Err := aa23Pin.AA23Match(aa23Key, aa23Query, aa23F.aa23Now()); aa23Err != nil {
			aa23T.Fatal(aa23Err)
		}
		if _, aa23Err := aa23Pin.AA23ResponseSymbol(aa23Key, 0, 0, AA23PhaseResponse, aa23F.aa23Now()); aa23Err != nil {
			aa23T.Fatal(aa23Err)
		}
		if _, _, aa23Err := aa23Pin.AA23Release(); aa23Err != nil {
			aa23T.Fatal(aa23Err)
		}
		for _, aa23Pending := range aa23F.aa23Registry.AA23PendingRevocations(aa23Buffer) {
			aa23Ack, aa23Ready, aa23Err := aa23F.aa23View.AA23ObserveRevocation(aa23Pending)
			if aa23Err != nil || !aa23Ready {
				aa23T.Fatal(aa23Err, aa23Ready)
			}
			if aa23Err := aa23F.aa23Registry.AA23ObserveAcknowledgement(aa23Ack); aa23Err != nil {
				aa23T.Fatal(aa23Err)
			}
			if aa23Err := aa23F.aa23Registry.AA23Pool().AA23Reclaim(aa23Ack.AA23Slot); aa23Err != nil {
				aa23T.Fatal(aa23Err)
			}
		}
	})
	if aa23Allocs != 0 {
		aa23T.Fatalf("allocated %v per cycle", aa23Allocs)
	}
}

// TestAA23ExportedAPITakesNoCallbacks proves no caller code can enter the model.
func TestAA23ExportedAPITakesNoCallbacks(aa23T *aa23testing.T) {
	for _, aa23Type := range aa23FrozenTypes() {
		for aa23Index := 0; aa23Index < aa23Type.NumMethod(); aa23Index++ {
			aa23Method := aa23Type.Method(aa23Index)
			for aa23In := 0; aa23In < aa23Method.Type.NumIn(); aa23In++ {
				switch aa23Method.Type.In(aa23In).Kind() {
				case aa23reflect.Func, aa23reflect.Chan, aa23reflect.Interface:
					aa23T.Fatalf("%s.%s accepts %s", aa23Type, aa23Method.Name, aa23Method.Type.In(aa23In))
				}
			}
		}
	}
	for _, aa23Value := range []any{
		AA23Limits{}, AA23Registration{}, AA23RegistrationView{}, AA23PlanIdentity{}, AA23WireKey{},
		AA23PlanEntry{}, AA23PlanCandidate{}, AA23PlanValidity{}, AA23SlotRef{}, AA23SlotCounts{},
		AA23PublishNotice{}, AA23RevocationNotice{}, AA23Acknowledgement{}, AA23RequestQuery{},
	} {
		aa23Type := aa23reflect.TypeOf(aa23Value)
		for aa23Index := 0; aa23Index < aa23Type.NumField(); aa23Index++ {
			switch aa23Type.Field(aa23Index).Type.Kind() {
			case aa23reflect.Func, aa23reflect.Chan, aa23reflect.Interface, aa23reflect.Map, aa23reflect.Pointer:
				aa23T.Fatalf("%s.%s is %s", aa23Type, aa23Type.Field(aa23Index).Name, aa23Type.Field(aa23Index).Type)
			}
		}
	}
}

func aa23FrozenTypes() []aa23reflect.Type {
	return []aa23reflect.Type{
		aa23reflect.TypeOf((*AA23Registry)(nil)),
		aa23reflect.TypeOf((*AA23Pool)(nil)),
		aa23reflect.TypeOf((*AA23WorkerView)(nil)),
		aa23reflect.TypeOf(AA23PlanPin{}),
	}
}

func TestAA23ExportedAPIIsFrozen(aa23T *aa23testing.T) {
	aa23Want := map[string][]string{
		"*localworker.AA23Registry": {
			"AA23AbortPrepared", "AA23AdvanceSource", "AA23BindMaster", "AA23BindWorker", "AA23ClearPhysicalConflict",
			"AA23DisconnectSubscriber", "AA23ExpireDue", "AA23MarkPhysicalConflict", "AA23MasterOwner",
			"AA23ObserveAcknowledgement", "AA23ObserveWorkerTerminated", "AA23OpenSubscriber", "AA23PendingRevocations",
			"AA23PhysicalConflict", "AA23Pool", "AA23PreparePlan", "AA23PublishPrepared", "AA23Register",
			"AA23Registration", "AA23ReleaseMaster", "AA23RenewRegistration", "AA23Reset", "AA23Unregister",
		},
		"*localworker.AA23Pool": {"AA23Counts", "AA23PendingReclaims", "AA23Reclaim", "AA23SlotState"},
		"*localworker.AA23WorkerView": {
			"AA23AdoptPlan", "AA23AdvanceStream", "AA23ObserveRevocation", "AA23SelectProvisional",
		},
		"localworker.AA23PlanPin": {
			"AA23Key", "AA23Match", "AA23Release", "AA23ResponseLength", "AA23ResponseSymbol", "AA23Validate",
		},
	}
	for _, aa23Type := range aa23FrozenTypes() {
		aa23Got := make([]string, 0, aa23Type.NumMethod())
		for aa23Index := 0; aa23Index < aa23Type.NumMethod(); aa23Index++ {
			aa23Got = append(aa23Got, aa23Type.Method(aa23Index).Name)
		}
		aa23Expected := append([]string(nil), aa23Want[aa23Type.String()]...)
		aa23sort.Strings(aa23Got)
		aa23sort.Strings(aa23Expected)
		if !aa23reflect.DeepEqual(aa23Got, aa23Expected) {
			aa23T.Fatalf("%s methods changed: %v", aa23Type, aa23Got)
		}
	}
}
