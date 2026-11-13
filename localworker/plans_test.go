package localworker

import (
	"errors"
	"math"
	"reflect"
	"sort"
	"testing"
)

const (
	planFixtureAddress  byte = 0x15
	fixtureOtherAddress byte = 0x26
	fixtureInitiator    byte = 0x10
	fixtureOtherSource  byte = 0x31
	planFixtureWorker        = WorkerID(11)
	planFixtureStream        = StreamGeneration(21)
	fixtureSource            = SourceGeneration(31)
	planFixtureDomain        = uint64(77)
	fixtureStart             = Tick(100)
	fixtureFarExpiry         = Tick(5000)
)

var fixtureSilence = FailurePolicy{
	Unavailable: FailureSilence,
	Malformed:   FailureSilence,
	Timeout:     FailureSilence,
}

var planFixtureLimits = PlanLimits{
	MaxSubscribers:          4,
	MaxRegistrations:        4,
	MaxMasters:              2,
	MaxPlanSlots:            3,
	MaxEntriesPerPlan:       4,
	MaxRequestBytes:         8,
	MaxEncodedResponseBytes: 40,
	MaxPlanBytes:            96,
	MaxLeaseTicks:           100000,
}

type planFixture struct {
	t          *testing.T
	registry   *Registry
	view       *WorkerView
	tick       Tick
	subscriber SubscriberID
	key        RegistrationKey
	txn        TransactionOrdinal
}

func planNewFixture(t *testing.T) *planFixture {
	t.Helper()
	return newFixtureWithLimits(t, planFixtureLimits)
}

func newFixtureWithLimits(t *testing.T, limits PlanLimits) *planFixture {
	t.Helper()
	registry, err := NewRegistry(limits, 0)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	view, err := NewWorkerView(registry.Pool(), planFixtureWorker, planFixtureStream)
	if err != nil {
		t.Fatalf("NewWorkerView: %v", err)
	}
	if err := registry.BindWorker(planFixtureWorker, planFixtureStream); err != nil {
		t.Fatalf("BindWorker: %v", err)
	}
	subscriber, err := registry.OpenSubscriber()
	if err != nil {
		t.Fatalf("OpenSubscriber: %v", err)
	}
	current := &planFixture{
		t:          t,
		registry:   registry,
		view:       view,
		tick:       fixtureStart,
		subscriber: subscriber,
		key:        RegistrationKey{Subscriber: subscriber, Address: planFixtureAddress, Epoch: 1},
	}
	current.mustRegister(current.registration(current.key))
	return current
}

func (current *planFixture) now() ClockReading {
	return ClockReading{Domain: planFixtureDomain, Tick: current.tick}
}

func (current *planFixture) registration(key RegistrationKey) Registration {
	return Registration{
		Key:               key,
		LeaseExpiry:       fixtureFarExpiry,
		Source:            fixtureSource,
		SourceLeaseExpiry: fixtureFarExpiry,
		Failure:           fixtureSilence,
	}
}

func (current *planFixture) mustRegister(registration Registration) {
	current.t.Helper()
	if _, err := current.registry.Register(registration, current.now()); err != nil {
		current.t.Fatalf("Register(%+v): %v", registration, err)
	}
}

// fixtureData is synthetic and carries both symbols that need escaping.
func fixtureData(marker byte) []byte {
	return []byte{marker, 'F', 'A', 'M', '0', '1', planSymbolEscape, planSymbolSyn}
}

func mustEncode(t *testing.T, data []byte) []byte {
	t.Helper()
	wire, err := EncodeResponse(nil, data)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	return wire
}

func (current *planFixture) entry(marker byte) PlanEntry {
	current.t.Helper()
	return PlanEntry{
		Source:          SourceConstraint{Source: fixtureInitiator},
		Destination:     current.key.Address,
		PB:              0x07,
		SB:              0x04,
		RequestLength:   0,
		Operation:       OperationRead,
		AllowedPhase:    PhaseResponse,
		MaxEncodedBytes: 40,
		EncodedResponse: mustEncode(current.t, fixtureData(marker)),
	}
}

func (current *planFixture) identity(plan PlanGeneration) PlanIdentity {
	return PlanIdentity{
		Registration: current.key,
		Worker:       planFixtureWorker,
		Stream:       planFixtureStream,
		Plan:         plan,
		Source:       fixtureSource,
	}
}

func (current *planFixture) candidate(plan PlanGeneration, marker byte) PlanCandidate {
	current.t.Helper()
	return PlanCandidate{
		Identity:   current.identity(plan),
		PlanExpiry: fixtureFarExpiry,
		Failure:    fixtureSilence,
		Entries:    []PlanEntry{current.entry(marker)},
	}
}

func (current *planFixture) publish(candidate PlanCandidate) PublishNotice {
	current.t.Helper()
	ref, err := current.registry.PreparePlan(candidate, current.now())
	if err != nil {
		current.t.Fatalf("PreparePlan: %v", err)
	}
	notice, err := current.registry.PublishPrepared(ref, current.now())
	if err != nil {
		current.t.Fatalf("PublishPrepared: %v", err)
	}
	return notice
}

func (current *planFixture) install(plan PlanGeneration, marker byte) PublishNotice {
	current.t.Helper()
	notice := current.publish(current.candidate(plan, marker))
	ack, ready, err := current.view.AdoptPlan(notice, current.now())
	if err != nil {
		current.t.Fatalf("AdoptPlan: %v", err)
	}
	if ready {
		if err := current.registry.ObserveAcknowledgement(ack); err != nil {
			current.t.Fatalf("ObserveAcknowledgement: %v", err)
		}
	}
	return notice
}

func (current *planFixture) selectAddress(address byte) (PlanPin, error) {
	current.txn++
	return current.view.SelectProvisional(address, current.txn, current.now())
}

func (current *planFixture) mustSelect() PlanPin {
	current.t.Helper()
	pin, err := current.selectAddress(current.key.Address)
	if err != nil {
		current.t.Fatalf("SelectProvisional: %v", err)
	}
	return pin
}

// settle delivers every pending notice and every resulting acknowledgement.
func (current *planFixture) settle() {
	current.t.Helper()
	notices := current.registry.PendingRevocations(make([]RevocationNotice, 0, 8))
	for _, notice := range notices {
		ack, ready, err := current.view.ObserveRevocation(notice)
		if err != nil {
			current.t.Fatalf("ObserveRevocation(%+v): %v", notice, err)
		}
		if !ready {
			current.t.Fatalf("no acknowledgement for %+v", notice)
		}
		if err := current.registry.ObserveAcknowledgement(ack); err != nil {
			current.t.Fatalf("ObserveAcknowledgement: %v", err)
		}
	}
}

func (current *planFixture) responseBytes(pin PlanPin) ([]byte, error) {
	current.t.Helper()
	key := pin.Key()
	length, err := pin.ResponseLength(key, 0, current.now())
	if err != nil {
		return nil, err
	}
	output := make([]byte, 0, length)
	for offset := uint32(0); offset < length; offset++ {
		symbol, err := pin.ResponseSymbol(key, 0, offset, PhaseResponse, current.now())
		if err != nil {
			return output, err
		}
		output = append(output, symbol)
	}
	return output, nil
}

func planWantErr(t *testing.T, label string, got error, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s: err = %v, want %v", label, got, want)
	}
}

func planWantOK(t *testing.T, label string, got error) {
	t.Helper()
	if got != nil {
		t.Fatalf("%s: unexpected err %v", label, got)
	}
}

// identityAxes varies each plan identity field while the others stay valid.
var identityAxes = []struct {
	name   string
	mutate func(*PlanIdentity)
}{
	{"subscriber", func(i *PlanIdentity) { i.Registration.Subscriber++ }},
	{"address", func(i *PlanIdentity) { i.Registration.Address = fixtureOtherAddress }},
	{"registrationEpoch", func(i *PlanIdentity) { i.Registration.Epoch++ }},
	{"worker", func(i *PlanIdentity) { i.Worker++ }},
	{"stream", func(i *PlanIdentity) { i.Stream++ }},
	{"plan", func(i *PlanIdentity) { i.Plan++ }},
	{"source", func(i *PlanIdentity) { i.Source++ }},
}

func TestAA23LimitsRefuseEveryZeroAndImpossibleValue(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PlanLimits)
	}{
		{"subscribers", func(l *PlanLimits) { l.MaxSubscribers = 0 }},
		{"registrations", func(l *PlanLimits) { l.MaxRegistrations = 0 }},
		{"masters", func(l *PlanLimits) { l.MaxMasters = 0 }},
		{"slots", func(l *PlanLimits) { l.MaxPlanSlots = 0 }},
		{"entries", func(l *PlanLimits) { l.MaxEntriesPerPlan = 0 }},
		{"request", func(l *PlanLimits) { l.MaxRequestBytes = 0 }},
		{"response", func(l *PlanLimits) { l.MaxEncodedResponseBytes = 0 }},
		{"planBytes", func(l *PlanLimits) { l.MaxPlanBytes = 0 }},
		{"lease", func(l *PlanLimits) { l.MaxLeaseTicks = 0 }},
		{"registrationsOverAddressSpace", func(l *PlanLimits) { l.MaxRegistrations = addressSpace + 1 }},
		{"mastersOverAddressSpace", func(l *PlanLimits) { l.MaxMasters = addressSpace + 1 }},
		{"subscribersOverAddressSpace", func(l *PlanLimits) { l.MaxSubscribers = addressSpace + 1 }},
		{"requestOverNativeLength", func(l *PlanLimits) { l.MaxRequestBytes = maxNativeLength + 1 }},
		{"responseOverNativeLength", func(l *PlanLimits) { l.MaxEncodedResponseBytes = maxEncodedResponse + 1 }},
		{"planSmallerThanOneEntry", func(l *PlanLimits) { l.MaxPlanBytes = 47 }},
		{"poolOverCeiling", func(l *PlanLimits) {
			slotBytes := uint64(l.MaxPlanBytes) + uint64(l.MaxEntriesPerPlan)*entryAccountingBytes
			l.MaxPlanSlots = uint32(poolByteCeiling/slotBytes) + 1
		}},
		{"slotOverCeiling", func(l *PlanLimits) { l.MaxPlanBytes = math.MaxUint32 }},
		{"entriesOverCeiling", func(l *PlanLimits) { l.MaxEntriesPerPlan = math.MaxUint32 }},
	}
	if _, err := NewRegistry(planFixtureLimits, 0); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	for _, tc := range cases {
		limits := planFixtureLimits
		tc.mutate(&limits)
		registry, err := NewRegistry(limits, 0)
		planWantErr(t, tc.name, err, ErrPlanInvalidLimits)
		if registry != nil {
			t.Fatalf("%s: registry returned on refusal", tc.name)
		}
	}
}

func TestAA23DuplicateAddressRefusedAndIncumbentUnchanged(t *testing.T) {
	f := planNewFixture(t)
	second, err := f.registry.OpenSubscriber()
	planWantOK(t, "open", err)
	incumbent := f.registry.Registration(f.key)

	otherSubscriber := f.registration(RegistrationKey{Subscriber: second, Address: planFixtureAddress, Epoch: 1})
	_, err = f.registry.Register(otherSubscriber, f.now())
	planWantErr(t, "other subscriber", err, ErrDuplicateAddress)

	laterEpoch := f.registration(RegistrationKey{Subscriber: f.subscriber, Address: planFixtureAddress, Epoch: 2})
	_, err = f.registry.Register(laterEpoch, f.now())
	planWantErr(t, "later epoch of the same subscriber", err, ErrDuplicateAddress)

	if got := f.registry.Registration(f.key); got != incumbent {
		t.Fatalf("incumbent changed: %+v", got)
	}
	elsewhere := f.registration(RegistrationKey{Subscriber: second, Address: fixtureOtherAddress, Epoch: 1})
	f.mustRegister(elsewhere)
	if got := f.registry.Registration(elsewhere.Key); got.State != RegistrationActive {
		t.Fatalf("positive control: %+v", got)
	}
}

func TestAA23ExactRetryAllocatesNothingAndDifferentContentConflicts(t *testing.T) {
	f := planNewFixture(t)
	original := f.registration(f.key)
	view, err := f.registry.Register(original, f.now())
	planWantOK(t, "exact retry", err)
	if view.State != RegistrationActive || view.Registered != original {
		t.Fatalf("exact retry state: %+v", view)
	}
	allocs := testing.AllocsPerRun(10, func() {
		if _, err := f.registry.Register(original, f.now()); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("exact retry allocated %v", allocs)
	}
	cases := []struct {
		name   string
		mutate func(*Registration)
	}{
		{"lease", func(r *Registration) { r.LeaseExpiry++ }},
		{"source", func(r *Registration) { r.Source++ }},
		{"sourceLease", func(r *Registration) { r.SourceLeaseExpiry++ }},
		{"failureUnavailable", func(r *Registration) { r.Failure.Unavailable = FailureNACK }},
		{"failureMalformed", func(r *Registration) { r.Failure.Malformed = FailureNACK }},
		{"failureTimeout", func(r *Registration) { r.Failure.Timeout = FailureNACK }},
	}
	for _, tc := range cases {
		changed := original
		tc.mutate(&changed)
		_, err := f.registry.Register(changed, f.now())
		planWantErr(t, tc.name, err, ErrRegistrationConflict)
	}
	if got := f.registry.Registration(f.key).Registered; got != original {
		t.Fatalf("conflict changed the incumbent: %+v", got)
	}
}

func TestAA23RegistrationAdmissionRefusals(t *testing.T) {
	f := planNewFixture(t)
	second, err := f.registry.OpenSubscriber()
	planWantOK(t, "open", err)
	valid := f.registration(RegistrationKey{Subscriber: second, Address: fixtureOtherAddress, Epoch: 3})
	cases := []struct {
		name   string
		mutate func(*Registration)
		want   error
	}{
		{"zeroSubscriber", func(r *Registration) { r.Key.Subscriber = 0 }, ErrInvalidIdentity},
		{"zeroEpoch", func(r *Registration) { r.Key.Epoch = 0 }, ErrInvalidIdentity},
		{"zeroSource", func(r *Registration) { r.Source = 0 }, ErrInvalidIdentity},
		{"unallocatedSubscriber", func(r *Registration) { r.Key.Subscriber = 99 }, ErrUnknownSubscriber},
		{"broadcast", func(r *Registration) { r.Key.Address = planAddressBroadcast }, ErrInvalidAddress},
		{"escape", func(r *Registration) { r.Key.Address = planSymbolEscape }, ErrInvalidAddress},
		{"syn", func(r *Registration) { r.Key.Address = planSymbolSyn }, ErrInvalidAddress},
		{"initiator", func(r *Registration) { r.Key.Address = fixtureInitiator }, ErrInvalidAddress},
		{"leaseEqualsNow", func(r *Registration) { r.LeaseExpiry = fixtureStart }, ErrPlanExpired},
		{"leaseTooLong", func(r *Registration) {
			r.LeaseExpiry = fixtureStart + planFixtureLimits.MaxLeaseTicks + 1
		}, ErrLeaseTooLong},
		{"sourceLeaseEqualsNow", func(r *Registration) { r.SourceLeaseExpiry = fixtureStart }, ErrPlanExpired},
		{"nackPolicy", func(r *Registration) { r.Failure.Malformed = FailureNACK }, ErrUnsupported},
		{"unspecifiedPolicy", func(r *Registration) { r.Failure.Timeout = FailureUnspecified }, ErrUnsupported},
	}
	for _, tc := range cases {
		candidate := valid
		tc.mutate(&candidate)
		_, err := f.registry.Register(candidate, f.now())
		planWantErr(t, tc.name, err, tc.want)
		if f.registry.byAddress[fixtureOtherAddress] != 0 {
			t.Fatalf("%s: refusal left an occupant", tc.name)
		}
	}
	f.mustRegister(valid)
}

func TestAA23EpochNeverReusedAndRefusesWrap(t *testing.T) {
	f := planNewFixture(t)
	key := func(epoch RegistrationEpoch) RegistrationKey {
		return RegistrationKey{Subscriber: f.subscriber, Address: planFixtureAddress, Epoch: epoch}
	}
	if state := f.registry.Unregister(key(1)); state != RegistrationAbsent {
		t.Fatalf("no plan means no wire authority to settle: %v", state)
	}
	_, err := f.registry.Register(f.registration(key(1)), f.now())
	planWantErr(t, "same epoch after cleanup", err, ErrStaleEpoch)
	f.mustRegister(f.registration(key(math.MaxUint64)))
	f.registry.Unregister(key(math.MaxUint64))
	_, err = f.registry.Register(f.registration(key(2)), f.now())
	planWantErr(t, "wrapped epoch", err, ErrCounterExhausted)
}

func TestAA23SubscriberAllocationBoundedAndNeverReused(t *testing.T) {
	limits := planFixtureLimits
	limits.MaxSubscribers = 1
	registry, err := NewRegistry(limits, 40)
	planWantOK(t, "new", err)
	first, err := registry.OpenSubscriber()
	planWantOK(t, "open", err)
	if first != 41 {
		t.Fatalf("floor not honored: %d", first)
	}
	_, err = registry.OpenSubscriber()
	planWantErr(t, "table full", err, ErrPlanCapacity)
	registry.DisconnectSubscriber(first)
	registry.DisconnectSubscriber(first)
	next, err := registry.OpenSubscriber()
	planWantOK(t, "open after disconnect", err)
	if next != 42 {
		t.Fatalf("identity reused or skipped: %d", next)
	}
	now := ClockReading{Domain: planFixtureDomain, Tick: fixtureStart}
	_, err = registry.Register(Registration{
		Key:               RegistrationKey{Subscriber: first, Address: planFixtureAddress, Epoch: 1},
		LeaseExpiry:       fixtureFarExpiry,
		Source:            fixtureSource,
		SourceLeaseExpiry: fixtureFarExpiry,
		Failure:           fixtureSilence,
	}, now)
	planWantErr(t, "disconnected incarnation", err, ErrUnknownSubscriber)

	exhausted, err := NewRegistry(planFixtureLimits, math.MaxUint64)
	planWantOK(t, "new at ceiling", err)
	_, err = exhausted.OpenSubscriber()
	planWantErr(t, "allocator wrap", err, ErrCounterExhausted)
}

func TestAA23RegistrationCapacityRefusesWithoutEviction(t *testing.T) {
	limits := planFixtureLimits
	limits.MaxRegistrations = 1
	f := newFixtureWithLimits(t, limits)
	other := f.registration(RegistrationKey{Subscriber: f.subscriber, Address: fixtureOtherAddress, Epoch: 2})
	_, err := f.registry.Register(other, f.now())
	planWantErr(t, "full", err, ErrPlanCapacity)
	if f.registry.Registration(f.key).State != RegistrationActive {
		t.Fatal("capacity refusal evicted the incumbent")
	}
	if f.registry.byAddress[fixtureOtherAddress] != 0 {
		t.Fatal("capacity refusal left an occupant")
	}
}

func TestAA23UnregisterRequiresEveryKeyField(t *testing.T) {
	f := planNewFixture(t)
	cases := []struct {
		name   string
		mutate func(*RegistrationKey)
	}{
		{"subscriber", func(k *RegistrationKey) { k.Subscriber++ }},
		{"address", func(k *RegistrationKey) { k.Address = fixtureOtherAddress }},
		{"epoch", func(k *RegistrationKey) { k.Epoch++ }},
		{"olderEpoch", func(k *RegistrationKey) { k.Epoch = 0 }},
	}
	for _, tc := range cases {
		stale := f.key
		tc.mutate(&stale)
		if state := f.registry.Unregister(stale); state != RegistrationAbsent {
			t.Fatalf("%s: state %v", tc.name, state)
		}
		if f.registry.Registration(f.key).State != RegistrationActive {
			t.Fatalf("%s: stale key removed the incumbent", tc.name)
		}
	}
	if state := f.registry.Unregister(f.key); state != RegistrationAbsent {
		t.Fatalf("exact key: %v", state)
	}
	if state := f.registry.Unregister(f.key); state != RegistrationAbsent {
		t.Fatalf("repeat: %v", state)
	}
	successor := RegistrationKey{Subscriber: f.subscriber, Address: planFixtureAddress, Epoch: 2}
	f.mustRegister(f.registration(successor))
	if state := f.registry.Unregister(f.key); state != RegistrationAbsent {
		t.Fatalf("late cleanup of old epoch: %v", state)
	}
	if f.registry.Registration(successor).State != RegistrationActive {
		t.Fatal("late cleanup of the old epoch removed its successor")
	}
}

func TestAA23AddressHeldUntilPredecessorAuthoritySettled(t *testing.T) {
	f := planNewFixture(t)
	f.install(1, 0xA1)
	if state := f.registry.Unregister(f.key); state != RegistrationRetiring {
		t.Fatalf("state %v", state)
	}
	if state := f.registry.Unregister(f.key); state != RegistrationRetiring {
		t.Fatalf("repeat state %v", state)
	}
	successor := RegistrationKey{Subscriber: f.subscriber, Address: planFixtureAddress, Epoch: 2}
	_, err := f.registry.Register(f.registration(successor), f.now())
	planWantErr(t, "unsettled predecessor", err, ErrDuplicateAddress)
	_, err = f.registry.Register(f.registration(f.key), f.now())
	planWantErr(t, "retiring retry", err, ErrRegistrationRetiring)
	f.settle()
	f.mustRegister(f.registration(successor))
}

func TestAA23DisconnectRetiresOnlyThatSubscriber(t *testing.T) {
	f := planNewFixture(t)
	f.install(1, 0xA1)
	planWantOK(t, "initiator", f.registry.BindMaster(f.subscriber, fixtureInitiator))
	other, err := f.registry.OpenSubscriber()
	planWantOK(t, "open", err)
	planWantOK(t, "other initiator", f.registry.BindMaster(other, fixtureOtherSource))
	otherKey := RegistrationKey{Subscriber: other, Address: fixtureOtherAddress, Epoch: 1}
	f.mustRegister(f.registration(otherKey))

	f.registry.DisconnectSubscriber(f.subscriber)
	if state := f.registry.Registration(f.key).State; state != RegistrationRetiring {
		t.Fatalf("own registration: %v", state)
	}
	if f.registry.Registration(otherKey).State != RegistrationActive {
		t.Fatal("unrelated registration retired")
	}
	if f.registry.MasterOwner(fixtureInitiator) != 0 {
		t.Fatal("own initiator identity survived disconnect")
	}
	if f.registry.MasterOwner(fixtureOtherSource) != other {
		t.Fatal("unrelated initiator identity released")
	}
	row := f.registry.findSubscriber(f.subscriber)
	if row == nil {
		t.Fatal("row freed before the retiring registration settled")
	}
	closed := f.registration(RegistrationKey{Subscriber: f.subscriber, Address: 0x27, Epoch: 2})
	_, err = f.registry.Register(closed, f.now())
	planWantErr(t, "closed incarnation with an unsettled row", err, ErrUnknownSubscriber)
	f.settle()
	if f.registry.Registration(f.key).State != RegistrationAbsent {
		t.Fatal("not settled")
	}
	if f.registry.findSubscriber(f.subscriber) != nil {
		t.Fatal("closed subscriber row not freed after settling")
	}
}

func TestAA23ResetRetiresAllAndKeepsHighWaterMarks(t *testing.T) {
	f := planNewFixture(t)
	f.install(1, 0xA1)
	planWantOK(t, "initiator", f.registry.BindMaster(f.subscriber, fixtureInitiator))
	f.registry.Reset()
	if f.registry.Registration(f.key).State != RegistrationRetiring {
		t.Fatal("reset did not retire")
	}
	if f.registry.MasterOwner(fixtureInitiator) != 0 {
		t.Fatal("reset kept a initiator identity")
	}
	f.settle()
	_, err := f.registry.Register(f.registration(f.key), f.now())
	planWantErr(t, "epoch after reset", err, ErrStaleEpoch)
	next := f.key
	next.Epoch = 2
	f.mustRegister(f.registration(next))
	f.key = next
	candidate := f.candidate(1, 0xA2)
	_, err = f.registry.PreparePlan(candidate, f.now())
	planWantErr(t, "plan generation after reset", err, ErrStaleGeneration)
	f.publish(f.candidate(2, 0xA2))
}

func TestAA23MasterIdentityIsSeparateFromTargetRegistration(t *testing.T) {
	f := planNewFixture(t)
	if f.registry.MasterOwner(fixtureInitiator) != 0 {
		t.Fatal("registration implied a initiator identity")
	}
	planWantErr(t, "target as initiator", f.registry.BindMaster(f.subscriber, planFixtureAddress), ErrInvalidAddress)
	planWantErr(t, "unknown owner", f.registry.BindMaster(99, fixtureInitiator), ErrUnknownSubscriber)
	planWantOK(t, "bind", f.registry.BindMaster(f.subscriber, fixtureInitiator))
	planWantOK(t, "repeat", f.registry.BindMaster(f.subscriber, fixtureInitiator))
	if f.registry.masterCount != 1 {
		t.Fatalf("repeat counted twice: %d", f.registry.masterCount)
	}
	other, err := f.registry.OpenSubscriber()
	planWantOK(t, "open", err)
	planWantErr(t, "other owner", f.registry.BindMaster(other, fixtureInitiator), ErrMasterConflict)

	f.registry.Unregister(f.key)
	if f.registry.MasterOwner(fixtureInitiator) != f.subscriber {
		t.Fatal("unregistering a target released the initiator identity")
	}
	f.registry.ReleaseMaster(other, fixtureInitiator)
	if f.registry.MasterOwner(fixtureInitiator) != f.subscriber {
		t.Fatal("another owner released the initiator identity")
	}
	next := RegistrationKey{Subscriber: f.subscriber, Address: planFixtureAddress, Epoch: 2}
	f.mustRegister(f.registration(next))
	f.registry.ReleaseMaster(f.subscriber, fixtureInitiator)
	if f.registry.Registration(next).State != RegistrationActive {
		t.Fatal("releasing a initiator identity retired a target registration")
	}
	planWantOK(t, "first", f.registry.BindMaster(other, 0x01))
	planWantOK(t, "second", f.registry.BindMaster(other, 0x03))
	planWantErr(t, "capacity", f.registry.BindMaster(other, 0x07), ErrPlanCapacity)
}

func TestAA23KnownPhysicalConflictInhibitsAndClearRevivesNothing(t *testing.T) {
	f := planNewFixture(t)
	notice := f.install(1, 0xA1)
	f.registry.MarkPhysicalConflict(planFixtureAddress)
	if !f.registry.PhysicalConflict(planFixtureAddress) {
		t.Fatal("not recorded")
	}
	if f.registry.Pool().SlotState(notice.Slot.Index) != SlotRetained {
		t.Fatal("published plan survived a known conflict")
	}
	_, err := f.registry.PreparePlan(f.candidate(2, 0xA2), f.now())
	planWantErr(t, "prepare under conflict", err, ErrPhysicalConflict)
	f.settle()
	f.registry.ClearPhysicalConflict(planFixtureAddress)
	_, err = f.selectAddress(planFixtureAddress)
	planWantErr(t, "clear revived a plan", err, ErrNoPlan)
	f.install(2, 0xA2)
	f.mustSelect()

	f.registry.MarkPhysicalConflict(fixtureOtherAddress)
	other := f.registration(RegistrationKey{Subscriber: f.subscriber, Address: fixtureOtherAddress, Epoch: 2})
	_, err = f.registry.Register(other, f.now())
	planWantErr(t, "register under conflict", err, ErrPhysicalConflict)
	f.registry.MarkPhysicalConflict(fixtureInitiator)
	planWantErr(t, "initiator under conflict", f.registry.BindMaster(f.subscriber, fixtureInitiator), ErrPhysicalConflict)
	f.registry.ClearPhysicalConflict(fixtureOtherAddress)
	f.mustRegister(other)
}

func TestAA23PublishRefusedWhileConflictAppearsDuringWriting(t *testing.T) {
	f := planNewFixture(t)
	ref, err := f.registry.PreparePlan(f.candidate(1, 0xA1), f.now())
	planWantOK(t, "prepare", err)
	f.registry.MarkPhysicalConflict(planFixtureAddress)
	if f.registry.Pool().SlotState(ref.Index) != SlotFree {
		t.Fatal("a conflict must abort an unpublished slot")
	}
	_, err = f.registry.PublishPrepared(ref, f.now())
	planWantErr(t, "publish", err, ErrSlotState)
}

func TestAA23CandidateValidationRefusesWholePlanAndKeepsPredecessor(t *testing.T) {
	synWire := []byte{0x01, planSymbolSyn, planCRCUpdate(planCRCUpdate(0, 0x01), planSymbolSyn)}
	badCRC := mustEncode(t, []byte{0x01, 0x02})
	badCRC[len(badCRC)-1] ^= 0x01
	cases := []struct {
		name   string
		mutate func(*PlanCandidate)
		want   error
	}{
		{"statefulOperation", func(c *PlanCandidate) { c.Entries[0].Operation = OperationStateful }, ErrUnsupported},
		{"unspecifiedOperation", func(c *PlanCandidate) { c.Entries[0].Operation = OperationUnspecified }, ErrUnsupported},
		{"phaseRequest", func(c *PlanCandidate) { c.Entries[0].AllowedPhase = PhaseRequest }, ErrUnsupported},
		{"phaseNone", func(c *PlanCandidate) { c.Entries[0].AllowedPhase = PhaseNone }, ErrUnsupported},
		{"failureNACK", func(c *PlanCandidate) { c.Failure.Unavailable = FailureNACK }, ErrUnsupported},
		{"failureUnspecified", func(c *PlanCandidate) { c.Failure.Malformed = FailureUnspecified }, ErrUnsupported},
		{"wrongDestination", func(c *PlanCandidate) { c.Entries[0].Destination = fixtureOtherAddress }, ErrInvalidPlan},
		{"sourceNotInitiator", func(c *PlanCandidate) { c.Entries[0].Source.Source = fixtureOtherAddress }, ErrInvalidPlan},
		{"anyWithSource", func(c *PlanCandidate) { c.Entries[0].Source.Any = true }, ErrInvalidPlan},
		{"lengthShorterThanData", func(c *PlanCandidate) { c.Entries[0].Request = []byte{0x01} }, ErrInvalidPlan},
		{"lengthLongerThanData", func(c *PlanCandidate) { c.Entries[0].RequestLength = 1 }, ErrInvalidPlan},
		{"requestOverBound", func(c *PlanCandidate) {
			c.Entries[0].Request = make([]byte, 9)
			c.Entries[0].RequestLength = 9
		}, ErrPlanOversize},
		{"zeroMaxEncoded", func(c *PlanCandidate) { c.Entries[0].MaxEncodedBytes = 0 }, ErrInvalidPlan},
		{"encodedOverEntryMax", func(c *PlanCandidate) {
			c.Entries[0].MaxEncodedBytes = uint32(len(c.Entries[0].EncodedResponse)) - 1
		}, ErrPlanOversize},
		{"entryMaxOverLimit", func(c *PlanCandidate) { c.Entries[0].MaxEncodedBytes = 41 }, ErrPlanOversize},
		{"noEntries", func(c *PlanCandidate) { c.Entries = nil }, ErrInvalidPlan},
		{"emptyResponse", func(c *PlanCandidate) { c.Entries[0].EncodedResponse = nil }, ErrInvalidPlan},
		{"rawSyn", func(c *PlanCandidate) { c.Entries[0].EncodedResponse = synWire }, ErrInvalidPlan},
		{"danglingEscape", func(c *PlanCandidate) {
			c.Entries[0].EncodedResponse = []byte{0x00, planSymbolEscape}
		}, ErrInvalidPlan},
		{"badEscapeSuffix", func(c *PlanCandidate) {
			c.Entries[0].EncodedResponse = []byte{0x00, planSymbolEscape, 0x02}
		}, ErrInvalidPlan},
		{"badCRC", func(c *PlanCandidate) { c.Entries[0].EncodedResponse = badCRC }, ErrInvalidPlan},
		{"lengthByteTooLarge", func(c *PlanCandidate) { c.Entries[0].EncodedResponse = []byte{0x02, 0x00} }, ErrInvalidPlan},
		{"trailingWireByte", func(c *PlanCandidate) {
			c.Entries[0].EncodedResponse = append(mustEncode(t, nil), 0x00)
		}, ErrInvalidPlan},
		{"tooManyEntries", func(c *PlanCandidate) {
			c.Entries = append(c.Entries, c.Entries[0], c.Entries[0], c.Entries[0], c.Entries[0])
		}, ErrPlanOversize},
		{"planBytesOverBound", func(c *PlanCandidate) {
			base := c.Entries[0]
			base.EncodedResponse = mustEncode(t, make([]byte, 30))
			second, third, fourth := base, base, base
			second.SB, third.SB, fourth.SB = 0x05, 0x06, 0x07
			c.Entries = []PlanEntry{base, second, third, fourth}
		}, ErrPlanOversize},
		{"duplicateEntry", func(c *PlanCandidate) { c.Entries = append(c.Entries, c.Entries[0]) }, ErrAmbiguousPlan},
		{"anySourceOverlapsExact", func(c *PlanCandidate) {
			anyEntry := c.Entries[0]
			anyEntry.Source = SourceConstraint{Any: true}
			c.Entries = append(c.Entries, anyEntry)
		}, ErrAmbiguousPlan},
		{"exactOverlapsAnySource", func(c *PlanCandidate) {
			anyEntry := c.Entries[0]
			anyEntry.Source = SourceConstraint{Any: true}
			c.Entries = []PlanEntry{anyEntry, c.Entries[0]}
		}, ErrAmbiguousPlan},
		{"zeroPlan", func(c *PlanCandidate) { c.Identity.Plan = 0 }, ErrInvalidIdentity},
		{"zeroSource", func(c *PlanCandidate) { c.Identity.Source = 0 }, ErrInvalidIdentity},
		{"zeroWorker", func(c *PlanCandidate) { c.Identity.Worker = 0 }, ErrInvalidIdentity},
		{"zeroStream", func(c *PlanCandidate) { c.Identity.Stream = 0 }, ErrInvalidIdentity},
		{"planExpiryEqualsNow", func(c *PlanCandidate) { c.PlanExpiry = fixtureStart }, ErrPlanExpired},
	}
	for _, tc := range cases {
		f := planNewFixture(t)
		predecessor := f.install(1, 0xA1)
		candidate := f.candidate(2, 0xA2)
		tc.mutate(&candidate)
		top := f.registry.planTop
		_, err := f.registry.PreparePlan(candidate, f.now())
		planWantErr(t, tc.name, err, tc.want)
		want := SlotCounts{Free: 2, Published: 1}
		if got := f.registry.Pool().Counts(); got != want {
			t.Fatalf("%s: pool %+v", tc.name, got)
		}
		if f.registry.planTop != top {
			t.Fatalf("%s: refusal consumed a generation", tc.name)
		}
		pin := f.mustSelect()
		if pin.Key().Identity != predecessor.Identity {
			t.Fatalf("%s: predecessor replaced", tc.name)
		}
	}
}

func TestAA23NonOverlappingSelectorsInstallTogether(t *testing.T) {
	f := planNewFixture(t)
	base := f.entry(0xA1)
	base.Request = []byte{0x01, 0x02}
	base.RequestLength = 2
	variants := []struct {
		name   string
		mutate func(*PlanEntry)
	}{
		{"source", func(e *PlanEntry) { e.Source.Source = fixtureOtherSource }},
		{"pb", func(e *PlanEntry) { e.PB++ }},
		{"sb", func(e *PlanEntry) { e.SB++ }},
		{"length", func(e *PlanEntry) {
			e.Request = []byte{0x01}
			e.RequestLength = 1
		}},
		{"data", func(e *PlanEntry) { e.Request = []byte{0x01, 0x03} }},
	}
	for index, variant := range variants {
		second := base
		variant.mutate(&second)
		candidate := f.candidate(PlanGeneration(index+1), 0xA1)
		candidate.Entries = []PlanEntry{base, second}
		ref, err := f.registry.PreparePlan(candidate, f.now())
		planWantOK(t, variant.name, err)
		planWantOK(t, variant.name, f.registry.AbortPrepared(ref))
	}
}

func TestAA23PrepareVariesEveryIdentityAxis(t *testing.T) {
	cases := []struct {
		name string
		want error
	}{
		{"subscriber", ErrNotRegistered},
		{"address", ErrInvalidPlan},
		{"registrationEpoch", ErrNotRegistered},
		{"worker", ErrIdentityMismatch},
		{"stream", ErrIdentityMismatch},
		{"source", ErrIdentityMismatch},
	}
	for _, tc := range cases {
		f := planNewFixture(t)
		candidate := f.candidate(1, 0xA1)
		for _, axis := range identityAxes {
			if axis.name == tc.name {
				axis.mutate(&candidate.Identity)
			}
		}
		_, err := f.registry.PreparePlan(candidate, f.now())
		planWantErr(t, tc.name, err, tc.want)
		f.publish(f.candidate(1, 0xA1))
	}
	f := planNewFixture(t)
	elsewhere := f.candidate(1, 0xA1)
	elsewhere.Identity.Registration.Address = fixtureOtherAddress
	elsewhere.Entries[0].Destination = fixtureOtherAddress
	_, err := f.registry.PreparePlan(elsewhere, f.now())
	planWantErr(t, "address with a matching entry", err, ErrNotRegistered)
}

func TestAA23PlanGenerationAdvancesAndRefusesWrap(t *testing.T) {
	f := planNewFixture(t)
	f.publish(f.candidate(5, 0xA1))
	for _, stale := range []PlanGeneration{1, 4, 5} {
		_, err := f.registry.PreparePlan(f.candidate(stale, 0xA2), f.now())
		planWantErr(t, "stale generation", err, ErrStaleGeneration)
	}
	ref, err := f.registry.PreparePlan(f.candidate(6, 0xA2), f.now())
	planWantOK(t, "advance", err)
	planWantOK(t, "abort", f.registry.AbortPrepared(ref))
	_, err = f.registry.PreparePlan(f.candidate(6, 0xA3), f.now())
	planWantErr(t, "aborted generation lent to other bytes", err, ErrStaleGeneration)

	ref, err = f.registry.PreparePlan(f.candidate(math.MaxUint64, 0xA2), f.now())
	planWantOK(t, "ceiling", err)
	planWantOK(t, "abort", f.registry.AbortPrepared(ref))
	_, err = f.registry.PreparePlan(f.candidate(1, 0xA2), f.now())
	planWantErr(t, "wrap", err, ErrCounterExhausted)
}

func TestAA23WritingStaysInvisibleAndPublishIsComplete(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	ref, err := f.registry.PreparePlan(f.candidate(2, 0xA2), f.now())
	planWantOK(t, "prepare", err)
	pool := f.registry.Pool()
	if pool.SlotState(ref.Index) != SlotWriting {
		t.Fatal("not writing")
	}
	early := PublishNotice{Slot: ref, Identity: f.identity(2)}
	_, _, err = f.view.AdoptPlan(early, f.now())
	planWantErr(t, "adopt while writing", err, ErrSlotState)
	if pool.SlotState(first.Slot.Index) != SlotPublished {
		t.Fatal("preparing displaced the predecessor")
	}
	pin := f.mustSelect()
	if pin.Key().Identity.Plan != 1 {
		t.Fatalf("selected %+v", pin.Key())
	}
	_, _, err = pin.Release()
	planWantOK(t, "release", err)
	notice, err := f.registry.PublishPrepared(ref, f.now())
	planWantOK(t, "publish", err)
	if notice.Identity != f.identity(2) || notice.Slot != ref {
		t.Fatalf("notice %+v", notice)
	}
	if pool.SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("superseded plan must stay retained")
	}
	_, err = f.registry.PublishPrepared(ref, f.now())
	planWantErr(t, "publish twice", err, ErrSlotState)
	planWantErr(t, "abort published", f.registry.AbortPrepared(ref), ErrSlotState)
}

func TestAA23ChangesDuringWritingAbortWithoutReplacing(t *testing.T) {
	cases := []struct {
		name         string
		change       func(*planFixture)
		want         error
		predecessorP bool
	}{
		{"unregister", func(f *planFixture) { f.registry.Unregister(f.key) }, ErrSlotState, false},
		{"source", func(f *planFixture) {
			planWantOK(f.t, "advance", f.registry.AdvanceSource(f.key, fixtureSource+1, fixtureFarExpiry, f.now()))
		}, ErrSlotState, false},
		{"stream", func(f *planFixture) {
			planWantOK(f.t, "bind", f.registry.BindWorker(planFixtureWorker, planFixtureStream+1))
		}, ErrSlotState, false},
		{"planExpires", func(f *planFixture) { f.tick = fixtureFarExpiry }, ErrPlanExpired, true},
	}
	for _, tc := range cases {
		f := planNewFixture(t)
		first := f.install(1, 0xA1)
		ref, err := f.registry.PreparePlan(f.candidate(2, 0xA2), f.now())
		planWantOK(t, tc.name, err)
		tc.change(f)
		_, err = f.registry.PublishPrepared(ref, f.now())
		planWantErr(t, tc.name, err, tc.want)
		pool := f.registry.Pool()
		if pool.SlotState(ref.Index) != SlotFree {
			t.Fatalf("%s: stale writing slot not returned", tc.name)
		}
		published := pool.SlotState(first.Slot.Index) == SlotPublished
		if published != tc.predecessorP {
			t.Fatalf("%s: predecessor published = %v", tc.name, published)
		}
	}
}

func TestAA23PublishedBytesAreDetachedFromCallerBuffers(t *testing.T) {
	f := planNewFixture(t)
	candidate := f.candidate(1, 0xA1)
	candidate.Entries[0].Request = []byte{0x42}
	candidate.Entries[0].RequestLength = 1
	want := append([]byte(nil), candidate.Entries[0].EncodedResponse...)
	notice := f.publish(candidate)
	candidate.Entries[0].Request[0] = 0x43
	candidate.Entries[0].EncodedResponse[0] = 0x00
	_, _, err := f.view.AdoptPlan(notice, f.now())
	planWantOK(t, "adopt", err)
	pin := f.mustSelect()
	got, err := f.responseBytes(pin)
	planWantOK(t, "response", err)
	if string(got) != string(want) {
		t.Fatalf("response bytes changed: %x want %x", got, want)
	}
	query := RequestQuery{Source: fixtureInitiator, Destination: planFixtureAddress, PB: 0x07, SB: 0x04, Data: []byte{0x42}}
	_, err = pin.Match(pin.Key(), query, f.now())
	planWantOK(t, "original request", err)
	query.Data = []byte{0x43}
	_, err = pin.Match(pin.Key(), query, f.now())
	planWantErr(t, "mutated request", err, ErrNoMatch)
}

func TestAA23PreparedUpdateNeverMixesInFlightResponse(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	pin := f.mustSelect()
	key := pin.Key()
	length, err := pin.ResponseLength(key, 0, f.now())
	planWantOK(t, "length", err)
	wantFirst := mustEncode(t, fixtureData(0xA1))
	wantSecond := mustEncode(t, fixtureData(0xA2))
	if string(wantFirst) == string(wantSecond) {
		t.Fatal("fixture generations must differ")
	}
	got := make([]byte, 0, length)
	for offset := uint32(0); offset < length; offset++ {
		if offset == 2 {
			notice := f.publish(f.candidate(2, 0xA2))
			_, ready, err := f.view.AdoptPlan(notice, f.now())
			planWantOK(t, "adopt mid response", err)
			if ready {
				t.Fatal("acknowledged a plan still held by a reader")
			}
		}
		symbol, err := pin.ResponseSymbol(key, 0, offset, PhaseResponse, f.now())
		planWantOK(t, "symbol", err)
		got = append(got, symbol)
	}
	if string(got) != string(wantFirst) {
		t.Fatalf("in-flight bytes mixed: %x want %x", got, wantFirst)
	}
	ack, ready, err := pin.Release()
	planWantOK(t, "release", err)
	if !ready || ack.Identity != first.Identity || ack.Slot != first.Slot {
		t.Fatalf("release ack %+v ready %v", ack, ready)
	}
	planWantOK(t, "ack", f.registry.ObserveAcknowledgement(ack))
	next := f.mustSelect()
	if next.Key().Identity.Plan != 2 {
		t.Fatalf("next transaction %+v", next.Key())
	}
	second, err := f.responseBytes(next)
	planWantOK(t, "next response", err)
	if string(second) != string(wantSecond) {
		t.Fatalf("next transaction bytes %x", second)
	}
}

func TestAA23RetainedSlotIsNotReusedBeforeAcknowledgement(t *testing.T) {
	limits := planFixtureLimits
	limits.MaxPlanSlots = 2
	f := newFixtureWithLimits(t, limits)
	first := f.install(1, 0xA1)
	pin := f.mustSelect()
	second := f.publish(f.candidate(2, 0xA2))
	pool := f.registry.Pool()

	_, err := f.registry.PreparePlan(f.candidate(3, 0xA3), f.now())
	planWantErr(t, "pool pressure", err, ErrPoolExhausted)
	if got, want := pool.Counts(), (SlotCounts{Published: 1, Retained: 1}); got != want {
		t.Fatalf("pool under pressure: %+v", got)
	}
	planWantErr(t, "reclaim retained", pool.Reclaim(first.Slot), ErrSlotState)
	planWantErr(t, "reclaim published", pool.Reclaim(second.Slot), ErrSlotState)
	forged := Acknowledgement(first)
	planWantErr(t, "ack while pinned", f.registry.ObserveAcknowledgement(forged), ErrSlotPinned)
	if pool.SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("forged acknowledgement moved a pinned slot")
	}
	published := Acknowledgement(second)
	planWantErr(t, "ack of a published plan", f.registry.ObserveAcknowledgement(published), ErrSlotState)

	bytes, err := f.responseBytes(pin)
	planWantOK(t, "pinned bytes under pressure", err)
	if string(bytes) != string(mustEncode(t, fixtureData(0xA1))) {
		t.Fatalf("retained bytes overwritten: %x", bytes)
	}
	_, ready, err := pin.Release()
	planWantOK(t, "release", err)
	if ready {
		t.Fatal("no retirement observed yet, so no acknowledgement is due")
	}
	f.settle()
	if pool.SlotState(first.Slot.Index) != SlotReclaimable {
		t.Fatal("not reclaimable after acknowledgement")
	}
	_, err = f.registry.PreparePlan(f.candidate(3, 0xA3), f.now())
	planWantErr(t, "reclaimable is not yet free", err, ErrPoolExhausted)
	pending := pool.PendingReclaims(make([]SlotRef, 0, 2))
	if len(pending) != 1 || pending[0] != first.Slot {
		t.Fatalf("pending reclaims %+v", pending)
	}
	planWantOK(t, "reclaim", pool.Reclaim(first.Slot))
	ref, err := f.registry.PreparePlan(f.candidate(3, 0xA3), f.now())
	planWantOK(t, "prepare after reclaim", err)
	if ref.Index != first.Slot.Index || ref.Incarnation != first.Slot.Incarnation+1 {
		t.Fatalf("slot reuse without a new incarnation: %+v", ref)
	}
}

func TestAA23OldReferencesCannotReachAReusedSlot(t *testing.T) {
	limits := planFixtureLimits
	limits.MaxPlanSlots = 1
	f := newFixtureWithLimits(t, limits)
	first := f.install(1, 0xA1)
	pool := f.registry.Pool()
	pin := f.mustSelect()
	_, _, err := pin.Release()
	planWantOK(t, "release", err)
	f.registry.Unregister(f.key)
	f.settle()
	planWantOK(t, "reclaim", pool.Reclaim(first.Slot))

	f.key.Epoch = 2
	f.mustRegister(f.registration(f.key))
	second := f.publish(f.candidate(2, 0xA2))
	if second.Slot.Index != first.Slot.Index {
		t.Fatal("fixture must reuse the only slot")
	}

	oldAck := Acknowledgement(first)
	planWantErr(t, "old ack", f.registry.ObserveAcknowledgement(oldAck), ErrSlotState)
	oldNotice := RevocationNotice{Slot: first.Slot, Identity: first.Identity, Reason: RetireRevoked}
	_, _, err = f.view.ObserveRevocation(oldNotice)
	planWantErr(t, "old notice", err, ErrSlotState)
	_, _, err = f.view.AdoptPlan(first, f.now())
	planWantErr(t, "old publish notice", err, ErrSlotState)
	planWantErr(t, "old reclaim", pool.Reclaim(first.Slot), ErrSlotState)
	planWantErr(t, "old abort", f.registry.AbortPrepared(first.Slot), ErrSlotState)
	_, _, err = pin.Release()
	planWantErr(t, "old pin", err, ErrPinReleased)
	if pool.SlotState(second.Slot.Index) != SlotPublished {
		t.Fatal("stale reference disturbed the successor")
	}
	outOfRange := SlotRef{Index: 9, Incarnation: 1}
	planWantErr(t, "out of range", pool.Reclaim(outOfRange), ErrSlotState)
	if pool.SlotState(9) != SlotInvalid {
		t.Fatal("out of range state")
	}
}

func TestAA23SlotIncarnationRefusesWrap(t *testing.T) {
	limits := planFixtureLimits
	limits.MaxPlanSlots = 1
	f := newFixtureWithLimits(t, limits)
	pool := f.registry.Pool()
	pool.slots[0].incarnation = math.MaxUint64
	first := f.install(1, 0xA1)
	f.registry.Unregister(f.key)
	f.settle()
	planWantErr(t, "reclaim at the ceiling", pool.Reclaim(first.Slot), ErrCounterExhausted)
	if pool.SlotState(0) != SlotReclaimable || pool.slots[0].incarnation != math.MaxUint64 {
		t.Fatal("exhausted slot must stay out of service without wrapping")
	}
	f.key.Epoch = 2
	f.mustRegister(f.registration(f.key))
	_, err := f.registry.PreparePlan(f.candidate(2, 0xA2), f.now())
	planWantErr(t, "capacity shrinks", err, ErrPoolExhausted)

	writer := newFixtureWithLimits(t, limits)
	writer.registry.Pool().slots[0].incarnation = math.MaxUint64
	ref, err := writer.registry.PreparePlan(writer.candidate(1, 0xA1), writer.now())
	planWantOK(t, "prepare", err)
	planWantErr(t, "abort at the ceiling", writer.registry.AbortPrepared(ref), ErrCounterExhausted)
}

func TestAA23AcknowledgementVariesEveryIdentityAxis(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	f.registry.Unregister(f.key)
	pool := f.registry.Pool()
	exact := Acknowledgement(first)
	for _, axis := range identityAxes {
		stale := exact
		axis.mutate(&stale.Identity)
		planWantErr(t, axis.name, f.registry.ObserveAcknowledgement(stale), ErrIdentityMismatch)
		if pool.SlotState(first.Slot.Index) != SlotRetained {
			t.Fatalf("%s: stale acknowledgement moved the slot", axis.name)
		}
	}
	incarnation := exact
	incarnation.Slot.Incarnation++
	planWantErr(t, "incarnation", f.registry.ObserveAcknowledgement(incarnation), ErrSlotState)
	index := exact
	index.Slot.Index++
	planWantErr(t, "index", f.registry.ObserveAcknowledgement(index), ErrSlotState)
	planWantOK(t, "exact", f.registry.ObserveAcknowledgement(exact))
	planWantErr(t, "repeat", f.registry.ObserveAcknowledgement(exact), ErrSlotState)
}

func TestAA23AdoptVariesEveryIdentityAxis(t *testing.T) {
	f := planNewFixture(t)
	notice := f.publish(f.candidate(1, 0xA1))
	for _, axis := range identityAxes {
		stale := notice
		axis.mutate(&stale.Identity)
		_, _, err := f.view.AdoptPlan(stale, f.now())
		planWantErr(t, axis.name, err, ErrIdentityMismatch)
	}
	otherWorker, err := NewWorkerView(f.registry.Pool(), planFixtureWorker+1, planFixtureStream)
	planWantOK(t, "worker view", err)
	_, _, err = otherWorker.AdoptPlan(notice, f.now())
	planWantErr(t, "other worker", err, ErrIdentityMismatch)
	otherStream, err := NewWorkerView(f.registry.Pool(), planFixtureWorker, planFixtureStream+1)
	planWantOK(t, "stream view", err)
	_, _, err = otherStream.AdoptPlan(notice, f.now())
	planWantErr(t, "other stream", err, ErrIdentityMismatch)
	replay, err := NewWorkerView(f.registry.Pool(), planFixtureWorker, planFixtureStream)
	planWantOK(t, "replay view", err)
	replay.planTop[planFixtureAddress] = notice.Identity.Plan
	_, _, err = replay.AdoptPlan(notice, f.now())
	planWantErr(t, "plan generation replay", err, ErrStaleGeneration)
	_, _, err = f.view.AdoptPlan(notice, f.now())
	planWantOK(t, "exact", err)
	f.mustSelect()
}

func TestAA23WireKeyVariesEveryAxisAtTheOutputBoundary(t *testing.T) {
	f := planNewFixture(t)
	f.install(1, 0xA1)
	pin := f.mustSelect()
	exact := pin.Key()
	if exact.Identity != f.identity(1) || exact.Transaction != f.txn {
		t.Fatalf("frozen key %+v", exact)
	}
	planWantOK(t, "exact", pin.Validate(exact, f.now()))
	check := func(label string, stale WireKey) {
		t.Helper()
		planWantErr(t, label, pin.Validate(stale, f.now()), ErrIdentityMismatch)
		_, err := pin.ResponseSymbol(stale, 0, 0, PhaseResponse, f.now())
		planWantErr(t, label+" symbol", err, ErrIdentityMismatch)
		_, err = pin.ResponseLength(stale, 0, f.now())
		planWantErr(t, label+" length", err, ErrIdentityMismatch)
		query := RequestQuery{Source: fixtureInitiator, Destination: planFixtureAddress, PB: 0x07, SB: 0x04}
		_, err = pin.Match(stale, query, f.now())
		planWantErr(t, label+" match", err, ErrIdentityMismatch)
	}
	for _, axis := range identityAxes {
		stale := exact
		axis.mutate(&stale.Identity)
		check(axis.name, stale)
	}
	transaction := exact
	transaction.Transaction++
	check("transaction", transaction)
	planWantOK(t, "still valid", pin.Validate(exact, f.now()))
	_, err := pin.ResponseSymbol(exact, 0, 0, PhaseTargetACK, f.now())
	planWantErr(t, "phase", err, ErrPhase)
	_, err = pin.ResponseSymbol(exact, 1, 0, PhaseResponse, f.now())
	planWantErr(t, "entry", err, ErrNoMatch)
	_, err = pin.ResponseLength(exact, 1, f.now())
	planWantErr(t, "length entry", err, ErrNoMatch)
	length, err := pin.ResponseLength(exact, 0, f.now())
	planWantOK(t, "length", err)
	_, err = pin.ResponseSymbol(exact, 0, length, PhaseResponse, f.now())
	planWantErr(t, "past the end", err, ErrPlanOversize)
	var zero PlanPin
	planWantErr(t, "zero pin", zero.Validate(exact, f.now()), ErrPinReleased)
	_, _, err = zero.Release()
	planWantErr(t, "zero release", err, ErrPinReleased)
}

func TestAA23MatchVariesEverySelector(t *testing.T) {
	f := planNewFixture(t)
	candidate := f.candidate(1, 0xA1)
	candidate.Entries[0].Request = []byte{0x01, 0x02}
	candidate.Entries[0].RequestLength = 2
	anyEntry := f.entry(0xB1)
	anyEntry.Source = SourceConstraint{Any: true}
	anyEntry.SB = 0x05
	candidate.Entries = append(candidate.Entries, anyEntry)
	notice := f.publish(candidate)
	_, _, err := f.view.AdoptPlan(notice, f.now())
	planWantOK(t, "adopt", err)
	pin := f.mustSelect()
	key := pin.Key()
	exact := RequestQuery{Source: fixtureInitiator, Destination: planFixtureAddress, PB: 0x07, SB: 0x04, Data: []byte{0x01, 0x02}}
	entry, err := pin.Match(key, exact, f.now())
	planWantOK(t, "exact", err)
	if entry != 0 {
		t.Fatalf("entry %d", entry)
	}
	cases := []struct {
		name   string
		mutate func(*RequestQuery)
	}{
		{"source", func(q *RequestQuery) { q.Source = fixtureOtherSource }},
		{"sourceNotInitiator", func(q *RequestQuery) { q.Source = fixtureOtherAddress }},
		{"destination", func(q *RequestQuery) { q.Destination = fixtureOtherAddress }},
		{"pb", func(q *RequestQuery) { q.PB++ }},
		{"sb", func(q *RequestQuery) { q.SB = 0x06 }},
		{"lengthShort", func(q *RequestQuery) { q.Data = []byte{0x01} }},
		{"anySourceStillNeedsInitiator", func(q *RequestQuery) {
			q.Source = fixtureOtherAddress
			q.SB = 0x05
			q.Data = nil
		}},
		{"lengthLong", func(q *RequestQuery) { q.Data = []byte{0x01, 0x02, 0x03} }},
		{"firstByte", func(q *RequestQuery) { q.Data = []byte{0x09, 0x02} }},
		{"lastByte", func(q *RequestQuery) { q.Data = []byte{0x01, 0x09} }},
	}
	for _, tc := range cases {
		query := exact
		tc.mutate(&query)
		_, err := pin.Match(key, query, f.now())
		planWantErr(t, tc.name, err, ErrNoMatch)
	}
	anyQuery := RequestQuery{Source: fixtureOtherSource, Destination: planFixtureAddress, PB: 0x07, SB: 0x05}
	entry, err = pin.Match(key, anyQuery, f.now())
	planWantOK(t, "any source", err)
	if entry != 1 {
		t.Fatalf("any source entry %d", entry)
	}
	length, err := pin.ResponseLength(key, 1, f.now())
	planWantOK(t, "second length", err)
	want := mustEncode(t, fixtureData(0xB1))
	if uint64(length) != uint64(len(want)) {
		t.Fatalf("second length %d", length)
	}
	for offset := range want {
		symbol, err := pin.ResponseSymbol(key, 1, uint32(offset), PhaseResponse, f.now())
		planWantOK(t, "second symbol", err)
		if symbol != want[offset] {
			t.Fatalf("second entry offset %d: %x", offset, symbol)
		}
	}
}

func TestAA23ExpiryEqualityIsExpiredForEveryDeadline(t *testing.T) {
	const deadline = Tick(3000)
	cases := []struct {
		name   string
		lease  Tick
		source Tick
		plan   Tick
	}{
		{"registrationLease", deadline, fixtureFarExpiry, fixtureFarExpiry},
		{"sourceLease", fixtureFarExpiry, deadline, fixtureFarExpiry},
		{"planExpiry", fixtureFarExpiry, fixtureFarExpiry, deadline},
	}
	setup := func(lease, source, plan Tick) (*planFixture, PublishNotice) {
		f := planNewFixture(t)
		f.registry.Unregister(f.key)
		f.key.Epoch = 2
		registration := f.registration(f.key)
		registration.LeaseExpiry = lease
		registration.SourceLeaseExpiry = source
		f.mustRegister(registration)
		candidate := f.candidate(1, 0xA1)
		candidate.PlanExpiry = plan
		return f, f.publish(candidate)
	}
	for _, tc := range cases {
		before, notice := setup(tc.lease, tc.source, tc.plan)
		before.tick = deadline - 1
		_, _, err := before.view.AdoptPlan(notice, before.now())
		planWantOK(t, tc.name+" adopt before", err)
		pin := before.mustSelect()
		planWantOK(t, tc.name+" validate before", pin.Validate(pin.Key(), before.now()))
		_, err = pin.ResponseSymbol(pin.Key(), 0, 0, PhaseResponse, before.now())
		planWantOK(t, tc.name+" symbol before", err)
		before.tick = deadline
		planWantErr(t, tc.name+" validate at", pin.Validate(pin.Key(), before.now()), ErrPlanExpired)
		_, err = pin.ResponseSymbol(pin.Key(), 0, 0, PhaseResponse, before.now())
		planWantErr(t, tc.name+" symbol at", err, ErrPlanExpired)
		_, _, err = pin.Release()
		planWantOK(t, tc.name+" release", err)
		_, err = before.selectAddress(planFixtureAddress)
		planWantErr(t, tc.name+" select at", err, ErrPlanExpired)

		at, atNotice := setup(tc.lease, tc.source, tc.plan)
		at.tick = deadline
		_, _, err = at.view.AdoptPlan(atNotice, at.now())
		planWantErr(t, tc.name+" adopt at", err, ErrPlanExpired)
		_, err = at.registry.PreparePlan(at.candidate(2, 0xA2), at.now())
		if tc.name == "planExpiry" {
			planWantOK(t, "a candidate with a later plan expiry stays valid", err)
		} else {
			planWantErr(t, tc.name+" prepare at", err, ErrPlanExpired)
		}

		late, _ := setup(tc.lease, tc.source, fixtureFarExpiry)
		ref, err := late.registry.PreparePlan(late.candidate(2, 0xA2), late.now())
		if tc.name == "planExpiry" {
			continue
		}
		planWantOK(t, tc.name+" prepare early", err)
		late.tick = deadline
		_, err = late.registry.PublishPrepared(ref, late.now())
		planWantErr(t, tc.name+" publish at", err, ErrPlanExpired)
	}
}

func TestAA23RenewalNeverExtendsAPublishedPlan(t *testing.T) {
	const deadline = Tick(3000)
	f := planNewFixture(t)
	f.registry.Unregister(f.key)
	f.key.Epoch = 2
	registration := f.registration(f.key)
	registration.LeaseExpiry = deadline
	f.mustRegister(registration)
	f.install(1, 0xA1)
	pin := f.mustSelect()

	planWantErr(t, "shrink", f.registry.RenewRegistration(f.key, deadline-1, f.now()), ErrStaleGeneration)
	stale := f.key
	stale.Epoch = 1
	planWantErr(t, "stale key", f.registry.RenewRegistration(stale, fixtureFarExpiry, f.now()), ErrNotRegistered)
	planWantOK(t, "renew", f.registry.RenewRegistration(f.key, fixtureFarExpiry, f.now()))
	if got := f.registry.Registration(f.key).LeaseExpiry; got != fixtureFarExpiry {
		t.Fatalf("lease %d", got)
	}
	f.tick = deadline
	planWantErr(t, "selected lease extended in place", pin.Validate(pin.Key(), f.now()), ErrPlanExpired)
	_, _, err := pin.Release()
	planWantOK(t, "release", err)
	_, err = f.selectAddress(planFixtureAddress)
	planWantErr(t, "published lease extended in place", err, ErrPlanExpired)
	notice := f.install(2, 0xA2)
	fresh := f.mustSelect()
	if fresh.Key().Identity != notice.Identity {
		t.Fatalf("fresh generation %+v", fresh.Key())
	}

	f.tick = fixtureFarExpiry
	planWantErr(t, "expired lease cannot revive", f.registry.RenewRegistration(f.key, fixtureFarExpiry+1, f.now()), ErrPlanExpired)
}

func TestAA23SourceGenerationFencesOlderPlans(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	stale := f.key
	stale.Subscriber++
	planWantErr(t, "stale key", f.registry.AdvanceSource(stale, fixtureSource+1, fixtureFarExpiry, f.now()), ErrNotRegistered)
	planWantErr(t, "equal source", f.registry.AdvanceSource(f.key, fixtureSource, fixtureFarExpiry, f.now()), ErrStaleGeneration)
	planWantErr(t, "lease at now", f.registry.AdvanceSource(f.key, fixtureSource+1, f.tick, f.now()), ErrPlanExpired)
	pool := f.registry.Pool()
	if pool.SlotState(first.Slot.Index) != SlotPublished {
		t.Fatal("refused advance retired a plan")
	}
	planWantOK(t, "advance", f.registry.AdvanceSource(f.key, fixtureSource+1, fixtureFarExpiry, f.now()))
	if pool.SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("older source plan stayed published")
	}
	_, err := f.registry.PreparePlan(f.candidate(2, 0xA2), f.now())
	planWantErr(t, "older source candidate", err, ErrIdentityMismatch)
	fresh := f.candidate(2, 0xA2)
	fresh.Identity.Source = fixtureSource + 1
	f.publish(fresh)
	if _, err := f.registry.Register(f.registration(f.key), f.now()); err != nil {
		t.Fatalf("exact retry compares original content: %v", err)
	}
}

func TestAA23RevocationTakesEffectOnlyWhenObserved(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	f.registry.Unregister(f.key)

	unobserved := f.mustSelect()
	planWantOK(t, "host publication is not observation", unobserved.Validate(unobserved.Key(), f.now()))

	notices := f.registry.PendingRevocations(make([]RevocationNotice, 0, 4))
	if len(notices) != 1 || notices[0].Identity != first.Identity || notices[0].Reason != RetireRevoked {
		t.Fatalf("notices %+v", notices)
	}
	ack, ready, err := f.view.ObserveRevocation(notices[0])
	planWantOK(t, "observe", err)
	if ready || ack != (Acknowledgement{}) {
		t.Fatal("acknowledged while a reader holds the plan")
	}
	planWantErr(t, "validate", unobserved.Validate(unobserved.Key(), f.now()), ErrPlanRevoked)
	_, err = unobserved.ResponseSymbol(unobserved.Key(), 0, 0, PhaseResponse, f.now())
	planWantErr(t, "symbol", err, ErrPlanRevoked)
	ack, ready, err = unobserved.Release()
	planWantOK(t, "release", err)
	if !ready {
		t.Fatal("release must yield the deferred acknowledgement")
	}
	planWantOK(t, "ack", f.registry.ObserveAcknowledgement(ack))
	if f.registry.Registration(f.key).State != RegistrationAbsent {
		t.Fatal("not settled by the exact acknowledgement")
	}
	_, err = f.selectAddress(planFixtureAddress)
	planWantErr(t, "future admission", err, ErrNoPlan)
}

func TestAA23RevocationNoticeVariesEveryIdentityAxis(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	published := RevocationNotice{Slot: first.Slot, Identity: first.Identity, Reason: RetireRevoked}
	_, _, err := f.view.ObserveRevocation(published)
	planWantErr(t, "published slot cannot be revoked by a notice alone", err, ErrSlotState)
	f.registry.Unregister(f.key)
	exact := f.registry.PendingRevocations(make([]RevocationNotice, 0, 1))[0]
	for _, axis := range identityAxes {
		stale := exact
		axis.mutate(&stale.Identity)
		_, _, err := f.view.ObserveRevocation(stale)
		planWantErr(t, axis.name, err, ErrIdentityMismatch)
	}
	pin := f.mustSelect()
	planWantOK(t, "stale notices did not fence", pin.Validate(pin.Key(), f.now()))
	_, _, err = pin.Release()
	planWantOK(t, "release", err)

	otherWorker, err := NewWorkerView(f.registry.Pool(), planFixtureWorker+1, planFixtureStream)
	planWantOK(t, "other worker", err)
	_, _, err = otherWorker.ObserveRevocation(exact)
	planWantErr(t, "other worker", err, ErrIdentityMismatch)
	olderStream, err := NewWorkerView(f.registry.Pool(), planFixtureWorker, planFixtureStream-1)
	planWantOK(t, "older stream", err)
	_, _, err = olderStream.ObserveRevocation(exact)
	planWantErr(t, "a notice from a future stream", err, ErrIdentityMismatch)

	ack, ready, err := f.view.ObserveRevocation(exact)
	planWantOK(t, "exact", err)
	if !ready {
		t.Fatal("no reader, so the acknowledgement is due")
	}
	again, ready, err := f.view.ObserveRevocation(exact)
	planWantOK(t, "lost acknowledgement is recovered by redelivery", err)
	if !ready || again != ack {
		t.Fatal("redelivery must repeat the same acknowledgement")
	}
	planWantOK(t, "ack", f.registry.ObserveAcknowledgement(again))
}

func TestAA23ObservingSupersessionStopsSelectionButFinishesTransaction(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	pin := f.mustSelect()
	f.publish(f.candidate(2, 0xA2))
	notices := f.registry.PendingRevocations(make([]RevocationNotice, 0, 4))
	if len(notices) != 1 || notices[0].Reason != RetireSuperseded || notices[0].Identity != first.Identity {
		t.Fatalf("notices %+v", notices)
	}
	_, ready, err := f.view.ObserveRevocation(notices[0])
	planWantOK(t, "observe", err)
	if ready {
		t.Fatal("acknowledged while held")
	}
	bytes, err := f.responseBytes(pin)
	planWantOK(t, "selected transaction finishes", err)
	if string(bytes) != string(mustEncode(t, fixtureData(0xA1))) {
		t.Fatalf("bytes %x", bytes)
	}

	f.registry.Unregister(f.key)
	upgraded := f.registry.PendingRevocations(make([]RevocationNotice, 0, 4))
	seen := 0
	for _, notice := range upgraded {
		if notice.Reason != RetireRevoked {
			t.Fatalf("a stronger reason must replace a weaker one: %+v", notice)
		}
		if notice.Identity == first.Identity {
			seen++
			_, _, err = f.view.ObserveRevocation(notice)
			planWantOK(t, "observe upgrade", err)
		}
	}
	if seen != 1 {
		t.Fatalf("notices %+v", upgraded)
	}
	planWantErr(t, "revocation overrides the finish allowance", pin.Validate(pin.Key(), f.now()), ErrPlanRevoked)
}

func TestAA23PendingRevocationsHonorsCapacityWithoutAllocating(t *testing.T) {
	f := planNewFixture(t)
	f.install(1, 0xA1)
	f.publish(f.candidate(2, 0xA2))
	f.registry.Unregister(f.key)
	buffer := make([]RevocationNotice, 0, 1)
	if got := f.registry.PendingRevocations(buffer); len(got) != 1 {
		t.Fatalf("capacity not honored: %d", len(got))
	}
	if got := f.registry.PendingRevocations(make([]RevocationNotice, 0, 8)); len(got) != 2 {
		t.Fatalf("pending %d", len(got))
	}
	refs := f.registry.Pool().PendingReclaims(make([]SlotRef, 0, 1))
	if len(refs) != 0 {
		t.Fatalf("nothing is reclaimable yet: %+v", refs)
	}
	allocs := testing.AllocsPerRun(10, func() {
		f.registry.PendingRevocations(buffer)
	})
	if allocs != 0 {
		t.Fatalf("allocated %v", allocs)
	}
}

func TestAA23ExpiryQuarantinesUntilAcknowledgementOrTermination(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	pool := f.registry.Pool()
	f.tick = fixtureFarExpiry - 1
	planWantOK(t, "before", f.registry.ExpireDue(f.now()))
	if pool.SlotState(first.Slot.Index) != SlotPublished {
		t.Fatal("retired before the deadline")
	}
	if f.registry.Registration(f.key).State != RegistrationActive {
		t.Fatal("registration retired before the deadline")
	}
	f.tick = fixtureFarExpiry
	planWantOK(t, "at", f.registry.ExpireDue(f.now()))
	if pool.SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("expiry must retain, never free, the slot")
	}
	if f.registry.Registration(f.key).State != RegistrationRetiring {
		t.Fatal("expired registration must retire and hold its address")
	}
	f.tick += 100000
	planWantOK(t, "paused worker", f.registry.ExpireDue(f.now()))
	if pool.SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("elapsed time alone reclaimed storage")
	}
	notice := f.registry.PendingRevocations(make([]RevocationNotice, 0, 1))[0]
	if notice.Reason != RetireExpired {
		t.Fatalf("reason %v", notice.Reason)
	}
	ack, ready, err := f.view.ObserveRevocation(notice)
	planWantOK(t, "observe", err)
	if !ready {
		t.Fatal("no reader")
	}
	planWantOK(t, "ack", f.registry.ObserveAcknowledgement(ack))
	if pool.SlotState(first.Slot.Index) != SlotReclaimable {
		t.Fatal("not reclaimable")
	}
}

func TestAA23SourceLeaseExpiryRetiresPlanButKeepsRegistration(t *testing.T) {
	const deadline = Tick(3000)
	f := planNewFixture(t)
	f.registry.Unregister(f.key)
	f.key.Epoch = 2
	registration := f.registration(f.key)
	registration.SourceLeaseExpiry = deadline
	f.mustRegister(registration)
	first := f.publish(f.candidate(1, 0xA1))
	f.tick = deadline
	planWantOK(t, "expire", f.registry.ExpireDue(f.now()))
	if f.registry.Pool().SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("plan stayed published past its source lease")
	}
	if f.registry.Registration(f.key).State != RegistrationActive {
		t.Fatal("an expired source lease does not end the registration")
	}
}

func TestAA23WorkerBindingAdvancesAndRetiresPredecessors(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(5, 0xA1)
	pool := f.registry.Pool()
	for _, tc := range []struct {
		name   string
		worker WorkerID
		stream StreamGeneration
		want   error
	}{
		{"zeroWorker", 0, planFixtureStream, ErrInvalidIdentity},
		{"zeroStream", planFixtureWorker, 0, ErrInvalidIdentity},
		{"sameTuple", planFixtureWorker, planFixtureStream, ErrStaleGeneration},
		{"olderStream", planFixtureWorker, planFixtureStream - 1, ErrStaleGeneration},
		{"olderWorker", planFixtureWorker - 1, planFixtureStream + 1, ErrStaleGeneration},
	} {
		planWantErr(t, tc.name, f.registry.BindWorker(tc.worker, tc.stream), tc.want)
		if pool.SlotState(first.Slot.Index) != SlotPublished {
			t.Fatalf("%s: refused binding retired a plan", tc.name)
		}
	}
	pin := f.mustSelect()
	planWantOK(t, "new stream", f.registry.BindWorker(planFixtureWorker, planFixtureStream+1))
	if pool.SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("old stream plan stayed published")
	}
	_, err := f.registry.PreparePlan(f.candidate(6, 0xA2), f.now())
	planWantErr(t, "candidate of the old stream", err, ErrIdentityMismatch)
	candidate := f.candidate(5, 0xA2)
	candidate.Identity.Stream = planFixtureStream + 1
	_, err = f.registry.PreparePlan(candidate, f.now())
	planWantErr(t, "plan generation does not restart with a stream", err, ErrStaleGeneration)

	planWantErr(t, "view stream stale", f.view.AdvanceStream(planFixtureStream), ErrStaleGeneration)
	planWantOK(t, "view stream", f.view.AdvanceStream(planFixtureStream+1))
	planWantErr(t, "pin of the old stream", pin.Validate(pin.Key(), f.now()), ErrPinReleased)
	if pool.slots[first.Slot.Index].pinned {
		t.Fatal("stream change kept a slot pinned")
	}
	f.txn = 0
	_, err = f.selectAddress(planFixtureAddress)
	planWantErr(t, "no plan survives a stream change", err, ErrNoPlan)
	f.settle()
	if pool.SlotState(first.Slot.Index) != SlotReclaimable {
		t.Fatal("old stream plan must still be acknowledged")
	}

	fresh := f.candidate(6, 0xA2)
	fresh.Identity.Stream = planFixtureStream + 1
	notice := f.publish(fresh)
	_, _, err = f.view.AdoptPlan(notice, f.now())
	planWantOK(t, "adopt in new stream", err)
	f.mustSelect()
}

func TestAA23WorkerTerminationIsTheOnlyProoflessReclaimPath(t *testing.T) {
	f := planNewFixture(t)
	first := f.install(1, 0xA1)
	pin := f.mustSelect()
	pool := f.registry.Pool()
	next := planFixtureWorker + 1
	planWantOK(t, "bind successor", f.registry.BindWorker(next, 1))
	if pool.SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("a successor cannot inherit the predecessor's plan")
	}
	planWantErr(t, "zero", f.registry.ObserveWorkerTerminated(0), ErrInvalidIdentity)
	planWantErr(t, "unknown", f.registry.ObserveWorkerTerminated(next+1), ErrInvalidIdentity)
	planWantOK(t, "termination of another worker", f.registry.ObserveWorkerTerminated(next))
	if pool.SlotState(first.Slot.Index) != SlotRetained {
		t.Fatal("termination of another worker reclaimed this slot")
	}
	_, err := f.registry.PreparePlan(f.candidate(1, 0xA2), f.now())
	planWantErr(t, "no bound worker", err, ErrNoWorker)
	planWantErr(t, "terminated worker cannot rebind", f.registry.BindWorker(next, 2), ErrStaleGeneration)

	planWantOK(t, "terminated", f.registry.ObserveWorkerTerminated(planFixtureWorker))
	if pool.SlotState(first.Slot.Index) != SlotReclaimable {
		t.Fatal("termination proof must reclaim")
	}
	planWantErr(t, "dead pin", pin.Validate(pin.Key(), f.now()), ErrSlotState)
	_, err = f.selectAddress(planFixtureAddress)
	planWantErr(t, "dead view busy", err, ErrPinBusy)
	_, _, err = pin.Release()
	planWantOK(t, "dead pin release", err)
	_, err = f.selectAddress(planFixtureAddress)
	planWantErr(t, "dead view select", err, ErrSlotState)
	planWantOK(t, "reclaim", pool.Reclaim(first.Slot))
	_, err = f.selectAddress(planFixtureAddress)
	planWantErr(t, "dead view after reuse", err, ErrSlotState)

	planWantOK(t, "bind third", f.registry.BindWorker(next+1, 1))
	candidate := f.candidate(1, 0xA2)
	candidate.Identity.Worker = next + 1
	candidate.Identity.Stream = 1
	f.publish(candidate)
	if f.registry.Registration(f.key).State != RegistrationActive {
		t.Fatal("termination of a worker must keep the registration")
	}
}

func TestAA23TerminationSettlesRetiringRegistration(t *testing.T) {
	f := planNewFixture(t)
	f.install(1, 0xA1)
	f.registry.Unregister(f.key)
	if f.registry.Registration(f.key).State != RegistrationRetiring {
		t.Fatal("not retiring")
	}
	planWantOK(t, "terminated", f.registry.ObserveWorkerTerminated(planFixtureWorker))
	if f.registry.Registration(f.key).State != RegistrationAbsent {
		t.Fatal("termination proof must settle the address")
	}
	_, err := f.registry.PreparePlan(f.candidate(2, 0xA2), f.now())
	planWantErr(t, "no worker bound", err, ErrNoWorker)
}

func TestAA23TerminationAbortsWritingSlot(t *testing.T) {
	f := planNewFixture(t)
	ref, err := f.registry.PreparePlan(f.candidate(1, 0xA1), f.now())
	planWantOK(t, "prepare", err)
	planWantOK(t, "terminated", f.registry.ObserveWorkerTerminated(planFixtureWorker))
	if f.registry.Pool().SlotState(ref.Index) != SlotFree {
		t.Fatal("writing slot survived its worker")
	}
	_, err = f.registry.PublishPrepared(ref, f.now())
	planWantErr(t, "publish", err, ErrSlotState)
}

func TestAA23ClockFaultsCloseAdmissionForGood(t *testing.T) {
	faults := []struct {
		name string
		now  ClockReading
	}{
		{"regression", ClockReading{Domain: planFixtureDomain, Tick: fixtureStart - 1}},
		{"otherDomain", ClockReading{Domain: planFixtureDomain + 1, Tick: fixtureStart + 1}},
		{"zeroDomain", ClockReading{Tick: fixtureStart + 1}},
	}
	for _, fault := range faults {
		f := planNewFixture(t)
		first := f.install(1, 0xA1)
		pin := f.mustSelect()

		planWantErr(t, fault.name+" worker", pin.Validate(pin.Key(), fault.now), ErrClockClosed)
		planWantErr(t, fault.name+" worker stays closed", pin.Validate(pin.Key(), f.now()), ErrClockClosed)
		_, err := f.selectAddress(planFixtureAddress)
		planWantErr(t, fault.name+" select", err, ErrClockClosed)
		_, _, err = pin.Release()
		planWantOK(t, fault.name+" release", err)

		planWantErr(t, fault.name+" host", f.registry.ExpireDue(fault.now), ErrClockClosed)
		if f.registry.Pool().SlotState(first.Slot.Index) != SlotRetained {
			t.Fatalf("%s: plan stayed published on a clock fault", fault.name)
		}
		_, err = f.registry.PreparePlan(f.candidate(2, 0xA2), f.now())
		planWantErr(t, fault.name+" prepare", err, ErrClockClosed)
		_, err = f.registry.Register(f.registration(f.key), f.now())
		planWantErr(t, fault.name+" register", err, ErrClockClosed)
		f.registry.Unregister(f.key)
		if f.registry.Registration(f.key).State != RegistrationRetiring {
			t.Fatalf("%s: cleanup must stay available after a clock fault", fault.name)
		}
	}
}

func TestAA23ZeroDomainClosesAFreshClock(t *testing.T) {
	registry, err := NewRegistry(planFixtureLimits, 0)
	planWantOK(t, "new", err)
	zero := ClockReading{Tick: fixtureStart}
	valid := ClockReading{Domain: planFixtureDomain, Tick: fixtureStart}
	planWantErr(t, "host", registry.ExpireDue(zero), ErrClockClosed)
	planWantErr(t, "host stays closed", registry.ExpireDue(valid), ErrClockClosed)
	view, err := NewWorkerView(registry.Pool(), planFixtureWorker, planFixtureStream)
	planWantOK(t, "view", err)
	_, err = view.SelectProvisional(planFixtureAddress, 1, zero)
	planWantErr(t, "worker", err, ErrClockClosed)
	_, err = view.SelectProvisional(planFixtureAddress, 2, valid)
	planWantErr(t, "worker stays closed", err, ErrClockClosed)
}

func TestAA23TransactionOrdinalAdvancesAndOnePinAtATime(t *testing.T) {
	f := planNewFixture(t)
	f.install(1, 0xA1)
	_, err := f.view.SelectProvisional(planFixtureAddress, 0, f.now())
	planWantErr(t, "zero", err, ErrInvalidIdentity)
	pin, err := f.view.SelectProvisional(planFixtureAddress, 5, f.now())
	planWantOK(t, "select", err)
	_, err = f.view.SelectProvisional(planFixtureAddress, 5, f.now())
	planWantErr(t, "same ordinal", err, ErrStaleTransaction)
	_, err = f.view.SelectProvisional(planFixtureAddress, 4, f.now())
	planWantErr(t, "older ordinal", err, ErrStaleTransaction)
	_, err = f.view.SelectProvisional(planFixtureAddress, 6, f.now())
	planWantErr(t, "second pin", err, ErrPinBusy)
	planWantOK(t, "still held", pin.Validate(pin.Key(), f.now()))
	_, _, err = pin.Release()
	planWantOK(t, "release", err)
	_, _, err = pin.Release()
	planWantErr(t, "repeat release", err, ErrPinReleased)
	planWantErr(t, "released pin", pin.Validate(pin.Key(), f.now()), ErrPinReleased)
	_, err = f.view.SelectProvisional(planFixtureAddress, 6, f.now())
	planWantErr(t, "ordinal consumed by a refused selection", err, ErrStaleTransaction)
	_, err = f.view.SelectProvisional(fixtureOtherAddress, 7, f.now())
	planWantErr(t, "unregistered address", err, ErrNoPlan)
	_, err = f.view.SelectProvisional(planFixtureAddress, 8, f.now())
	planWantOK(t, "advance", err)
}

func TestAA23WorkerViewRefusesZeroIdentity(t *testing.T) {
	registry, err := NewRegistry(planFixtureLimits, 0)
	planWantOK(t, "new", err)
	_, err = NewWorkerView(nil, 1, 1)
	planWantErr(t, "pool", err, ErrInvalidIdentity)
	_, err = NewWorkerView(registry.Pool(), 0, 1)
	planWantErr(t, "worker", err, ErrInvalidIdentity)
	_, err = NewWorkerView(registry.Pool(), 1, 0)
	planWantErr(t, "stream", err, ErrInvalidIdentity)
}

func TestAA23LeaseArithmeticRefusesOverflow(t *testing.T) {
	limits := planFixtureLimits
	limits.MaxLeaseTicks = 1000
	registry, err := NewRegistry(limits, 0)
	planWantOK(t, "new", err)
	subscriber, err := registry.OpenSubscriber()
	planWantOK(t, "open", err)
	now := ClockReading{Domain: planFixtureDomain, Tick: math.MaxUint64 - 1500}
	registration := Registration{
		Key:               RegistrationKey{Subscriber: subscriber, Address: planFixtureAddress, Epoch: 1},
		LeaseExpiry:       math.MaxUint64,
		Source:            fixtureSource,
		SourceLeaseExpiry: math.MaxUint64 - 1500 + 1000,
		Failure:           fixtureSilence,
	}
	_, err = registry.Register(registration, now)
	planWantErr(t, "beyond the bound near the ceiling", err, ErrLeaseTooLong)
	registration.LeaseExpiry = math.MaxUint64 - 1500 + 1000
	_, err = registry.Register(registration, now)
	planWantOK(t, "exactly the bound near the ceiling", err)
}

func TestAA23EncodeResponseFollowsNativeCRCAndEscaping(t *testing.T) {
	crc := func(data []byte) byte {
		var value byte
		for _, octet := range data {
			value = planCRCUpdate(value, octet)
		}
		return value
	}
	for _, vector := range []struct {
		data []byte
		want byte
	}{
		{nil, 0x00},
		{[]byte{0x01, 0x02}, 0x99},
		{[]byte{0x10, 0xFE, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA}, 0x77},
	} {
		if got := crc(vector.data); got != vector.want {
			t.Fatalf("CRC(%x) = %02x, want %02x", vector.data, got, vector.want)
		}
	}
	wire, err := EncodeResponse([]byte{0x55}, []byte{0xA9, 0xAA, 0x07})
	planWantOK(t, "encode", err)
	sum := crc([]byte{0x03, 0xA9, 0xAA, 0x07})
	want := appendEscaped([]byte{0x55, 0x03, 0xA9, 0x00, 0xA9, 0x01, 0x07}, sum)
	if string(wire) != string(want) {
		t.Fatalf("wire %x, want %x", wire, want)
	}
	planWantOK(t, "own encoding validates", validateEncodedResponse(wire[1:]))
	empty, err := EncodeResponse(nil, nil)
	planWantOK(t, "empty", err)
	if string(empty) != string([]byte{0x00, 0x00}) {
		t.Fatalf("empty %x", empty)
	}
	prefix := []byte{0x55}
	kept, err := EncodeResponse(prefix, make([]byte, maxNativeLength+1))
	planWantErr(t, "over the length byte", err, ErrPlanOversize)
	if string(kept) != string(prefix) {
		t.Fatalf("refusal changed dst: %x", kept)
	}
	for value := 0; value < 256; value++ {
		round, err := EncodeResponse(nil, []byte{byte(value)})
		planWantOK(t, "round", err)
		planWantOK(t, "round validates", validateEncodedResponse(round))
	}
}

func TestAA23AddressClassificationCoversEveryOctet(t *testing.T) {
	initiators := map[byte]bool{}
	for _, high := range []byte{0x0, 0x1, 0x3, 0x7, 0xF} {
		for _, low := range []byte{0x0, 0x1, 0x3, 0x7, 0xF} {
			initiators[high<<4|low] = true
		}
	}
	for value := 0; value < 256; value++ {
		address := byte(value)
		want := !initiators[address] && address != 0xFE && address != 0xA9 && address != 0xAA
		if got := isTargetAddress(address); got != want {
			t.Fatalf("target(%02x) = %v", address, got)
		}
		if got := isInitiatorAddress(address); got != initiators[address] {
			t.Fatalf("initiator(%02x) = %v", address, got)
		}
	}
}

func TestAA23SteadyStatePathsAllocateNothing(t *testing.T) {
	f := planNewFixture(t)
	candidate := f.candidate(1, 0xA1)
	plan := PlanGeneration(0)
	query := RequestQuery{Source: fixtureInitiator, Destination: planFixtureAddress, PB: 0x07, SB: 0x04}
	buffer := make([]RevocationNotice, 0, 4)
	allocs := testing.AllocsPerRun(50, func() {
		plan++
		candidate.Identity.Plan = plan
		ref, err := f.registry.PreparePlan(candidate, f.now())
		if err != nil {
			t.Fatal(err)
		}
		notice, err := f.registry.PublishPrepared(ref, f.now())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.view.AdoptPlan(notice, f.now()); err != nil {
			t.Fatal(err)
		}
		f.txn++
		pin, err := f.view.SelectProvisional(planFixtureAddress, f.txn, f.now())
		if err != nil {
			t.Fatal(err)
		}
		key := pin.Key()
		if _, err := pin.Match(key, query, f.now()); err != nil {
			t.Fatal(err)
		}
		if _, err := pin.ResponseSymbol(key, 0, 0, PhaseResponse, f.now()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := pin.Release(); err != nil {
			t.Fatal(err)
		}
		for _, pending := range f.registry.PendingRevocations(buffer) {
			ack, ready, err := f.view.ObserveRevocation(pending)
			if err != nil || !ready {
				t.Fatal(err, ready)
			}
			if err := f.registry.ObserveAcknowledgement(ack); err != nil {
				t.Fatal(err)
			}
			if err := f.registry.Pool().Reclaim(ack.Slot); err != nil {
				t.Fatal(err)
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("allocated %v per cycle", allocs)
	}
}

// TestAA23ExportedAPITakesNoCallbacks proves no caller code can enter the model.
func TestAA23ExportedAPITakesNoCallbacks(t *testing.T) {
	for _, typ := range frozenTypes() {
		for index := 0; index < typ.NumMethod(); index++ {
			method := typ.Method(index)
			for in := 0; in < method.Type.NumIn(); in++ {
				switch method.Type.In(in).Kind() {
				case reflect.Func, reflect.Chan, reflect.Interface:
					t.Fatalf("%s.%s accepts %s", typ, method.Name, method.Type.In(in))
				}
			}
		}
	}
	for _, value := range []any{
		PlanLimits{}, Registration{}, RegistrationView{}, PlanIdentity{}, WireKey{},
		PlanEntry{}, PlanCandidate{}, PlanValidity{}, SlotRef{}, SlotCounts{},
		PublishNotice{}, RevocationNotice{}, Acknowledgement{}, RequestQuery{},
	} {
		typ := reflect.TypeOf(value)
		for index := 0; index < typ.NumField(); index++ {
			switch typ.Field(index).Type.Kind() {
			case reflect.Func, reflect.Chan, reflect.Interface, reflect.Map, reflect.Pointer:
				t.Fatalf("%s.%s is %s", typ, typ.Field(index).Name, typ.Field(index).Type)
			}
		}
	}
}

func frozenTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeOf((*Registry)(nil)),
		reflect.TypeOf((*Pool)(nil)),
		reflect.TypeOf((*WorkerView)(nil)),
		reflect.TypeOf(PlanPin{}),
	}
}

func TestAA23ExportedAPIIsFrozen(t *testing.T) {
	want := map[string][]string{
		"*localworker.Registry": {
			"AbortPrepared", "AdvanceSource", "BindMaster", "BindWorker", "ClearPhysicalConflict",
			"DisconnectSubscriber", "ExpireDue", "MarkPhysicalConflict", "MasterOwner",
			"ObserveAcknowledgement", "ObserveWorkerTerminated", "OpenSubscriber", "PendingRevocations",
			"PhysicalConflict", "Pool", "PreparePlan", "PublishPrepared", "Register",
			"Registration", "ReleaseMaster", "RenewRegistration", "Reset", "Unregister",
		},
		"*localworker.Pool": {"Counts", "PendingReclaims", "Reclaim", "SlotState"},
		"*localworker.WorkerView": {
			"AdoptPlan", "AdvanceStream", "ObserveRevocation", "SelectProvisional",
		},
		"localworker.PlanPin": {
			"Key", "Match", "Release", "ResponseLength", "ResponseSymbol", "Validate",
		},
	}
	for _, typ := range frozenTypes() {
		got := make([]string, 0, typ.NumMethod())
		for index := 0; index < typ.NumMethod(); index++ {
			got = append(got, typ.Method(index).Name)
		}
		expected := append([]string(nil), want[typ.String()]...)
		sort.Strings(got)
		sort.Strings(expected)
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("%s methods changed: %v", typ, got)
		}
	}
}

// Catches a publication that does not compare against the registration's current
// generation: publishing 1 after 2 would retire 2 and strand the worker.
func TestAA23OutOfOrderPublicationIsRefused(t *testing.T) {
	f := planNewFixture(t)
	older, err := f.registry.PreparePlan(f.candidate(1, 0xB1), f.now())
	planWantOK(t, "prepare 1", err)
	newer, err := f.registry.PreparePlan(f.candidate(2, 0xB2), f.now())
	planWantOK(t, "prepare 2", err)
	notice, err := f.registry.PublishPrepared(newer, f.now())
	planWantOK(t, "publish 2", err)
	ack, ready, err := f.view.AdoptPlan(notice, f.now())
	planWantOK(t, "adopt 2", err)
	if ready {
		planWantOK(t, "ack 2", f.registry.ObserveAcknowledgement(ack))
	}
	_, err = f.registry.PublishPrepared(older, f.now())
	planWantErr(t, "publish 1 after 2", err, ErrStaleGeneration)
	pool := f.registry.Pool()
	if pool.SlotState(newer.Index) != SlotPublished {
		t.Fatal("stale publication retired the current generation")
	}
	if pool.SlotState(older.Index) != SlotFree {
		t.Fatal("stale candidate slot not returned")
	}
	if pin := f.mustSelect(); pin.Key().Identity.Plan != 2 {
		t.Fatalf("selected %+v", pin.Key())
	}
}

// Catches a publication check that forgets the highest published generation once
// the current one is retired: generation 1 must not publish after 2 expired.
func TestAA23PublicationHighWaterSurvivesRetirement(t *testing.T) {
	f := planNewFixture(t)
	older, err := f.registry.PreparePlan(f.candidate(1, 0xC1), f.now())
	planWantOK(t, "prepare 1", err)
	short := f.candidate(2, 0xC2)
	short.PlanExpiry = 200
	notice := f.publish(short)
	ack, ready, err := f.view.AdoptPlan(notice, f.now())
	planWantOK(t, "adopt 2", err)
	if ready {
		planWantOK(t, "ack 2", f.registry.ObserveAcknowledgement(ack))
	}
	f.tick = 200
	planWantOK(t, "expire", f.registry.ExpireDue(f.now()))
	_, err = f.registry.PublishPrepared(older, f.now())
	planWantErr(t, "publish 1 after 2 retired", err, ErrStaleGeneration)
	if f.registry.Pool().SlotState(older.Index) != SlotFree {
		t.Fatal("stale candidate slot not returned")
	}
}

// Catches publication that checks the renewed registry deadline instead of the
// deadline frozen at preparation.
func TestAA23PublishUsesFrozenRegistrationDeadline(t *testing.T) {
	const deadline = Tick(3000)
	f := planNewFixture(t)
	f.registry.Unregister(f.key)
	f.key.Epoch = 2
	registration := f.registration(f.key)
	registration.LeaseExpiry = deadline
	f.mustRegister(registration)
	ref, err := f.registry.PreparePlan(f.candidate(1, 0xD1), f.now())
	planWantOK(t, "prepare", err)
	planWantOK(t, "renew", f.registry.RenewRegistration(f.key, fixtureFarExpiry, f.now()))
	f.tick = deadline
	_, err = f.registry.PublishPrepared(ref, f.now())
	planWantErr(t, "publish at frozen deadline", err, ErrPlanExpired)
	if f.registry.Pool().SlotState(ref.Index) != SlotFree {
		t.Fatal("expired candidate slot not returned")
	}
}

// Catches a worker that compares host-domain deadlines against its own readings
// from a different clock domain.
func TestAA23WorkerRejectsAnotherClockDomain(t *testing.T) {
	f := planNewFixture(t)
	notice := f.publish(f.candidate(1, 0xE1))
	other := ClockReading{Domain: planFixtureDomain + 1, Tick: fixtureStart}
	_, _, err := f.view.AdoptPlan(notice, other)
	planWantErr(t, "adopt from another domain", err, ErrClockClosed)
	_, _, err = f.view.AdoptPlan(notice, f.now())
	planWantErr(t, "worker stays closed", err, ErrClockClosed)
}

// Positive control: a worker reading the publishing domain still adopts.
func TestAA23WorkerAdoptsInThePublishingClockDomain(t *testing.T) {
	f := planNewFixture(t)
	notice := f.publish(f.candidate(1, 0xE2))
	_, _, err := f.view.AdoptPlan(notice, f.now())
	planWantOK(t, "adopt", err)
}

func TestAA23OverlapSortMatchesPairwiseOracle(t *testing.T) {
	sources := []SourceConstraint{{Any: true}, {Source: 0x10}, {Source: 0x30}}
	requests := [][]byte{nil, {0x01}, {0x02}}
	var all []PlanEntry
	for _, pb := range []byte{0xB5, 0xB6} {
		for _, sb := range []byte{0x04, 0x05} {
			for _, req := range requests {
				for _, src := range sources {
					all = append(all, PlanEntry{PB: pb, SB: sb, Request: req, Source: src})
				}
			}
		}
	}
	scratch := make([]int, 3)
	for i := range all {
		for j := range all {
			if i == j {
				continue
			}
			// A third, non-overlapping entry exercises neighbour ordering.
			other := PlanEntry{PB: 0x01, SB: 0x01, Source: SourceConstraint{Source: 0x10}}
			pair := []PlanEntry{all[i], other, all[j]}
			got := planOverlaps(pair, scratch)
			if want := entriesOverlap(&all[i], &all[j]); got != want {
				t.Fatalf("entries %d,%d: sorted %v oracle %v", i, j, got, want)
			}
		}
	}
}

func TestAA23OverlapSortLargePlan(t *testing.T) {
	const count = 50000
	entries := make([]PlanEntry, count+1)
	for n := 0; n < count; n++ {
		entries[n] = PlanEntry{PB: byte(n >> 8), SB: byte(n), Source: SourceConstraint{Any: true}}
	}
	scratch := make([]int, len(entries))
	if planOverlaps(entries[:count], scratch) {
		t.Fatal("distinct entries reported overlap")
	}
	entries[count] = PlanEntry{PB: 0, SB: 7, Source: SourceConstraint{Source: 0x10}}
	if !planOverlaps(entries, scratch) {
		t.Fatal("late overlap with an any-source entry missed")
	}
}
