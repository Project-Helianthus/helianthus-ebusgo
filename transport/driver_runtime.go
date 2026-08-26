package transport

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrDriverUnavailable means that this runtime has no generation currently
	// admitted for transport invocation. It is deliberately lifecycle-local;
	// callers must not infer a protocol or endpoint failure from it.
	ErrDriverUnavailable = errors.New("ebus driver runtime unavailable")

	// ErrStaleDriverGeneration means an admission belonged to a generation that
	// has since been withdrawn or retired. The callback was not invoked.
	ErrStaleDriverGeneration = errors.New("ebus driver runtime generation is stale")

	// ErrDriverSafetyQuarantined means a previous generation could not be proven
	// closed. The runtime rejects construction and replacement for this process
	// epoch so that it cannot create a second owner for the same bus resource.
	ErrDriverSafetyQuarantined = errors.New("ebus driver runtime safety quarantined")

	// ErrDriverStopTimeout reports a bounded drain that expired but whose close
	// was subsequently confirmed. It is distinct from safety quarantine.
	ErrDriverStopTimeout = errors.New("ebus driver runtime stop timed out")
)

// DriverLifecycleHandle is a transport-owned, data-only close handshake.
// The adapter worker receives exactly one request on CloseRequest, starts or
// completes its own teardown, then closes Closed only once resources and any
// adapter-owned closer have actually retired. DriverRuntime merely selects on
// these channels; it never invokes arbitrary transport close code or starts a
// cleanup goroutine.
type DriverLifecycleHandle struct {
	CloseRequest chan<- struct{}
	Closed       <-chan struct{}
}

// ManagedRawTransport binds an existing RawTransport to its lifecycle handle.
// It is additive: legacy RawTransport implementations and callers remain
// unchanged because only DriverRuntime factories use this wrapper.
type ManagedRawTransport struct {
	Transport RawTransport
	Lifecycle DriverLifecycleHandle
}

// DriverRuntimeFactory constructs one fresh transport generation. The context
// is owned by that generation and is canceled before drain and close begin.
// A failed construction never advances DriverRuntime.Generation.
type DriverRuntimeFactory func(context.Context) (*ManagedRawTransport, error)

// DriverRuntimeConfig bounds each teardown phase independently. Stop spends at
// most one DrainTimeout waiting for admitted work and then a fresh DrainTimeout
// requesting and proving close, for a documented total bound of twice this
// duration (plus scheduler latency).
type DriverRuntimeConfig struct {
	DrainTimeout time.Duration
}

// DriverRuntime is the protocol-boundary lifecycle seam for one eBUS runtime.
// It does not interpret frames or export eBUS details to consumers; it only
// owns construction, admission, retirement, and resource-safety fencing.
type DriverRuntime struct {
	factory DriverRuntimeFactory
	timeout time.Duration

	// operationMu linearizes Start, Stop, and Replace for this runtime.
	operationMu sync.Mutex
	mu          sync.Mutex

	generation  uint64
	revision    uint64
	current     *driverRuntimeGeneration
	quarantined bool
	retiring    bool

	// constructions contains every Start or Replace factory context registered
	// before that operation waits for operationMu. Stop cancels this set before
	// taking operationMu, preventing a blocked factory from hiding behind the
	// lifecycle serializer.
	constructionSeq uint64
	constructions   map[uint64]*driverConstruction
}

type driverRuntimeGeneration struct {
	transport *ManagedRawTransport
	cancel    context.CancelFunc

	accepting       bool
	inFlight        int
	activeCallbacks int
	drained         chan struct{}
}

// driverConstruction separates caller cancellation authority (valid only
// before admission) from the runtime-owned generation context. The forwarding
// callback is detached under forwardMu before the generation can be admitted.
type driverConstruction struct {
	id            uint64
	runtimeCtx    context.Context
	runtimeCancel context.CancelFunc
	callerCtx     context.Context
	stopForward   func() bool
	// releaseRetiring identifies the one construction owned by Replace while
	// the retirement fence is held. Its admission publishes the replacement
	// and releases that fence under the same mutex.
	releaseRetiring bool

	state constructionState
	done  chan struct{}
}

type constructionState uint8

const (
	constructionProvisional constructionState = iota + 1
	constructionCancelRequested
	constructionAdmitted
	constructionFinished
)

// driverRuntimeTestHooks exists only to make construction linearization races
// deterministic in the in-package lifecycle tests. It is nil in production.
type driverRuntimeTestHooks struct {
	AfterConstructionSnapshot  func()
	BeforeConstructionDetach   func()
	afterConstructionAdmission func()
}

var driverRuntimeHooks struct {
	sync.Mutex
	hooks driverRuntimeTestHooks
}

func setDriverRuntimeTestHooks(hooks driverRuntimeTestHooks) func() {
	driverRuntimeHooks.Lock()
	previous := driverRuntimeHooks.hooks
	driverRuntimeHooks.hooks = hooks
	driverRuntimeHooks.Unlock()
	return func() {
		driverRuntimeHooks.Lock()
		driverRuntimeHooks.hooks = previous
		driverRuntimeHooks.Unlock()
	}
}

func runAfterConstructionSnapshotHook() {
	driverRuntimeHooks.Lock()
	hook := driverRuntimeHooks.hooks.AfterConstructionSnapshot
	driverRuntimeHooks.Unlock()
	if hook != nil {
		hook()
	}
}

func runBeforeConstructionDetachHook() {
	driverRuntimeHooks.Lock()
	hook := driverRuntimeHooks.hooks.BeforeConstructionDetach
	driverRuntimeHooks.Unlock()
	if hook != nil {
		hook()
	}
}

func runAfterConstructionAdmissionHook() {
	driverRuntimeHooks.Lock()
	hook := driverRuntimeHooks.hooks.afterConstructionAdmission
	driverRuntimeHooks.Unlock()
	if hook != nil {
		hook()
	}
}

// driverLease owns one admitted invocation. Although its concrete type is
// intentionally private, callers of Admit can use its exported methods. This
// prevents an adapter-local lifecycle detail becoming an exported API type.
type driverLease struct {
	runtime    *DriverRuntime
	generation *driverRuntimeGeneration
	id         uint64

	mu             sync.Mutex
	invoked        bool
	invoking       bool
	released       bool
	releasePending bool
}

// NewDriverRuntime constructs a dormant runtime. It does not call factory and
// does not open a transport; Start is the only admission point for generation 1.
func NewDriverRuntime(factory DriverRuntimeFactory, cfg DriverRuntimeConfig) *DriverRuntime {
	timeout := cfg.DrainTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	return &DriverRuntime{factory: factory, timeout: timeout, revision: 1}
}

// Generation returns the last admitted generation. It remains stable after a
// stop; only successful fresh construction advances it.
func (r *DriverRuntime) Generation() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

// Revision monotonically changes for every lifecycle-visible mutation.
func (r *DriverRuntime) Revision() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revision
}

// SafetyQuarantined reports process-epoch quarantine after an unconfirmed close.
func (r *DriverRuntime) SafetyQuarantined() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.quarantined
}

// Start admits a new generation only while stopped. It is idempotent for the
// active generation and never reuses a retired transport.
func (r *DriverRuntime) Start(ctx context.Context) (uint64, error) {
	construction, constructionID, existing, err := r.registerConstruction(ctx, false, false)
	if construction == nil {
		return existing, err
	}
	defer r.finishConstruction(constructionID, construction)
	return r.startLocked(construction)
}

// Replace retires the active generation before constructing a new one. If the
// old generation cannot be proven closed, quarantine prevents replacement.
func (r *DriverRuntime) Replace(ctx context.Context) (uint64, error) {
	if err := r.cancelConstructions(); err != nil {
		return 0, err
	}

	r.operationMu.Lock()
	if err := r.stopLocked(true); err != nil {
		// A confirmed close can still report a drain timeout. No replacement was
		// reserved in that case, so release the fence before returning and let a
		// later explicit Start choose whether to construct again.
		if !r.SafetyQuarantined() {
			r.mu.Lock()
			r.retiring = false
			r.mu.Unlock()
		}
		r.operationMu.Unlock()
		return 0, err
	}
	// Keep the retirement fence until this exact replacement is admitted. The
	// reservation is made while operationMu is held, so an external Start
	// cannot slip a factory between old proof and replacement publication.
	construction, constructionID, existing, err := r.registerConstruction(ctx, true, true)
	r.operationMu.Unlock()
	if construction == nil {
		return existing, err
	}
	defer r.finishConstruction(constructionID, construction)
	return r.startLocked(construction)
}

// Stop withdraws admission, cancels the exact generation, drains already
// admitted work, then closes and proves the generation retired.
func (r *DriverRuntime) Stop(context.Context) error {
	if err := r.cancelConstructions(); err != nil {
		return err
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	return r.stopLocked(false)
}

func (r *DriverRuntime) startLocked(construction *driverConstruction) (uint64, error) {
	// Start owns operationMu in the public method; inline the implementation to
	// keep Replace one indivisible stop-then-start operation.
	r.mu.Lock()
	if r.quarantined {
		r.mu.Unlock()
		construction.runtimeCancel()
		return 0, ErrDriverSafetyQuarantined
	}
	if r.retiring {
		if !construction.releaseRetiring {
			r.mu.Unlock()
			construction.runtimeCancel()
			return 0, ErrDriverUnavailable
		}
	}
	if r.current != nil {
		generation := r.generation
		r.mu.Unlock()
		construction.runtimeCancel()
		return generation, nil
	}
	factory := r.factory
	r.mu.Unlock()
	if factory == nil {
		construction.runtimeCancel()
		return 0, ErrDriverUnavailable
	}
	managed, err := factory(construction.runtimeCtx)
	if err != nil {
		if managed != nil {
			if !r.retireUnadmitted(managed, construction.runtimeCancel) {
				return 0, ErrDriverSafetyQuarantined
			}
		} else {
			construction.runtimeCancel()
		}
		return 0, err
	}
	if !validManagedTransport(managed) {
		construction.runtimeCancel()
		r.quarantine()
		return 0, ErrDriverSafetyQuarantined
	}
	generation, admitted := r.admitConstruction(construction, managed)
	if !admitted {
		if !r.retireUnadmitted(managed, construction.runtimeCancel) {
			return 0, ErrDriverSafetyQuarantined
		}
		if r.SafetyQuarantined() {
			return 0, ErrDriverSafetyQuarantined
		}
		return 0, construction.cancellationError()
	}
	runAfterConstructionAdmissionHook()
	return generation, nil
}

func (r *DriverRuntime) registerConstruction(ctx context.Context, allowRetiring, releaseRetiring bool) (*driverConstruction, uint64, uint64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if r.quarantined {
		r.mu.Unlock()
		runtimeCancel()
		return nil, 0, 0, ErrDriverSafetyQuarantined
	}
	if r.retiring && !allowRetiring {
		r.mu.Unlock()
		runtimeCancel()
		return nil, 0, 0, ErrDriverUnavailable
	}
	if allowRetiring && !r.retiring {
		r.mu.Unlock()
		runtimeCancel()
		return nil, 0, 0, ErrDriverUnavailable
	}
	if r.current != nil {
		generation := r.generation
		r.mu.Unlock()
		runtimeCancel()
		return nil, 0, generation, nil
	}
	if len(r.constructions) != 0 {
		r.mu.Unlock()
		runtimeCancel()
		return nil, 0, 0, ErrDriverUnavailable
	}
	r.constructionSeq++
	id := r.constructionSeq
	construction := &driverConstruction{id: id, runtimeCtx: runtimeCtx, runtimeCancel: runtimeCancel, callerCtx: ctx, releaseRetiring: releaseRetiring, state: constructionProvisional, done: make(chan struct{})}
	if r.constructions == nil {
		r.constructions = make(map[uint64]*driverConstruction)
	}
	r.constructions[id] = construction
	r.mu.Unlock()
	construction.stopForward = context.AfterFunc(ctx, func() { r.requestConstructionCancel(id) })
	return construction, id, 0, nil
}

func (r *DriverRuntime) finishConstruction(id uint64, construction *driverConstruction) {
	r.mu.Lock()
	if construction.state != constructionAdmitted {
		construction.state = constructionFinished
		delete(r.constructions, id)
		close(construction.done)
		if construction.releaseRetiring && !r.quarantined {
			r.retiring = false
		}
	}
	r.mu.Unlock()
	if construction.stopForward != nil {
		construction.stopForward()
	}
}

func (r *DriverRuntime) cancelConstructions() error {
	r.mu.Lock()
	done := make([]<-chan struct{}, 0, len(r.constructions))
	for _, construction := range r.constructions {
		if construction.state == constructionProvisional {
			construction.state = constructionCancelRequested
			// Cancel under the same lock that owns state transition. No bare
			// cancel snapshot can survive a later atomic admission publication.
			construction.runtimeCancel()
		}
		if construction.state == constructionCancelRequested {
			done = append(done, construction.done)
		}
	}
	runAfterConstructionSnapshotHook()
	r.mu.Unlock()
	if len(done) == 0 {
		return nil
	}
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	for _, completed := range done {
		select {
		case <-completed:
		case <-timer.C:
			r.quarantine()
			return ErrDriverSafetyQuarantined
		}
	}
	return nil
}

func (r *DriverRuntime) requestConstructionCancel(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	construction := r.constructions[id]
	if construction != nil && construction.state == constructionProvisional {
		construction.state = constructionCancelRequested
		construction.runtimeCancel()
	}
}

func (r *DriverRuntime) admitConstruction(construction *driverConstruction, managed *ManagedRawTransport) (uint64, bool) {
	runBeforeConstructionDetachHook()
	r.mu.Lock()
	defer r.mu.Unlock()
	if construction.state != constructionProvisional || construction.callerCtx.Err() != nil || construction.runtimeCtx.Err() != nil || r.quarantined {
		return 0, false
	}
	// State transition, map removal, and current publication share r.mu. Any
	// Stop/Replace cancel action arriving afterwards sees no provisional entry
	// and cannot retain a valid cancellation token for this generation.
	construction.state = constructionAdmitted
	delete(r.constructions, construction.id)
	r.generation++
	r.revision++
	r.current = &driverRuntimeGeneration{transport: managed, cancel: construction.runtimeCancel, accepting: true, drained: make(chan struct{})}
	if construction.releaseRetiring {
		r.retiring = false
	}
	if construction.stopForward != nil {
		construction.stopForward()
	}
	return r.generation, true
}

func (c *driverConstruction) cancellationError() error {
	if err := c.callerCtx.Err(); err != nil {
		return err
	}
	if err := c.runtimeCtx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

func (r *DriverRuntime) stopLocked(keepRetiring bool) error {
	r.mu.Lock()
	if r.quarantined {
		r.mu.Unlock()
		return ErrDriverSafetyQuarantined
	}
	generation := r.current
	if generation == nil {
		r.retiring = keepRetiring
		r.mu.Unlock()
		return nil
	}
	// Fence remains held from atomic withdrawal through drain, close request,
	// and final proof. External Start cannot reserve a factory in this window.
	r.retiring = true
	// This critical section is the effective-capability withdrawal boundary:
	// after accepting becomes false no new invocation can increment inFlight.
	generation.accepting = false
	r.current = nil
	r.revision++
	if generation.inFlight == 0 {
		close(generation.drained)
	}
	generation.cancel()
	r.mu.Unlock()

	drainDeadline, cancelDrain := context.WithTimeout(context.Background(), r.timeout)
	drainTimedOut := !waitForGeneration(drainDeadline, generation.drained)
	cancelDrain()
	if drainTimedOut {
		r.mu.Lock()
		activeCallbacks := generation.activeCallbacks
		r.mu.Unlock()
		if activeCallbacks > 0 {
			r.quarantine()
			return ErrDriverSafetyQuarantined
		}
	}

	closeDeadline, cancelClose := context.WithTimeout(context.Background(), r.timeout)
	defer cancelClose()
	if !r.retireManaged(closeDeadline, generation.transport, generation.cancel) {
		r.quarantine()
		return ErrDriverSafetyQuarantined
	}
	if !keepRetiring {
		r.mu.Lock()
		r.retiring = false
		r.mu.Unlock()
	}
	if drainTimedOut {
		return ErrDriverStopTimeout
	}
	return nil
}

func (r *DriverRuntime) retireUnadmitted(managed *ManagedRawTransport, cancel context.CancelFunc) bool {
	deadline, stop := context.WithTimeout(context.Background(), r.timeout)
	defer stop()
	if !r.retireManaged(deadline, managed, cancel) {
		r.quarantine()
		return false
	}
	return true
}

func (r *DriverRuntime) retireManaged(ctx context.Context, managed *ManagedRawTransport, cancel context.CancelFunc) bool {
	cancel()
	if !validManagedTransport(managed) {
		return false
	}
	if !sendCloseRequest(ctx, managed.Lifecycle.CloseRequest) {
		return false
	}
	select {
	case <-managed.Lifecycle.Closed:
		return true
	case <-ctx.Done():
		return false
	}
}

// sendCloseRequest contains the only send to the adapter-owned request
// channel. A closed channel is an adapter ownership violation; recover turns
// it into failed lifecycle proof instead of allowing a process panic. No
// goroutine is started and an unaccepted request is bounded by ctx.
func sendCloseRequest(ctx context.Context, request chan<- struct{}) (accepted bool) {
	defer func() {
		if recover() != nil {
			accepted = false
		}
	}()
	select {
	case request <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func validManagedTransport(managed *ManagedRawTransport) bool {
	if managed == nil || managed.Transport == nil || managed.Lifecycle.CloseRequest == nil || managed.Lifecycle.Closed == nil {
		return false
	}
	select {
	case <-managed.Lifecycle.Closed:
		return false
	default:
		return true
	}
}

func (r *DriverRuntime) quarantine() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.quarantined {
		r.quarantined = true
		r.revision++
	}
}

func waitForGeneration(ctx context.Context, drained <-chan struct{}) bool {
	select {
	case <-drained:
		return true
	case <-ctx.Done():
		return false
	}
}

// Admit captures the current generation while it remains effective. Stop and
// Replace withdraw admission under the same mutex before canceling that exact
// generation, so post-withdrawal callers cannot reach the raw transport.
func (r *DriverRuntime) Admit(context.Context) (*driverLease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.quarantined {
		return nil, ErrDriverSafetyQuarantined
	}
	generation := r.current
	if generation == nil || !generation.accepting {
		return nil, ErrDriverUnavailable
	}
	generation.inFlight++
	return &driverLease{runtime: r, generation: generation, id: r.generation}, nil
}

// Invoke admits and releases one callback atomically with respect to retirement.
func (r *DriverRuntime) Invoke(ctx context.Context, fn func(RawTransport) error) error {
	admission, err := r.Admit(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = admission.Release() }()
	return admission.Invoke(fn)
}

// Generation returns the generation captured at admission.
func (a *driverLease) Generation() uint64 { return a.id }

// Invoke runs fn only while the admission still belongs to the effective
// generation. Stale callbacks receive a typed error and do not touch transport.
func (a *driverLease) Invoke(fn func(RawTransport) error) error {
	a.mu.Lock()
	if a.invoked || a.released {
		a.mu.Unlock()
		return ErrStaleDriverGeneration
	}
	a.invoked = true
	a.invoking = true
	a.mu.Unlock()

	a.runtime.mu.Lock()
	valid := !a.runtime.quarantined && a.runtime.current == a.generation && a.generation.accepting
	if valid {
		a.generation.activeCallbacks++
	}
	a.runtime.mu.Unlock()
	if !valid {
		a.finishInvocation(false)
		return ErrStaleDriverGeneration
	}
	// Install cleanup immediately after the successful claim. It deliberately
	// does not recover: callback panics keep their normal propagation semantics
	// while active callback and releasePending accounting is still completed.
	defer a.finishInvocation(true)
	if fn == nil {
		return ErrDriverUnavailable
	}
	return fn(a.generation.transport.Transport)
}

// Release ends the admission's in-flight ownership. It is safe to call once
// or repeatedly; only the first call changes drain accounting.
func (a *driverLease) Release() error {
	a.mu.Lock()
	if a.released {
		a.mu.Unlock()
		return nil
	}
	if a.invoking {
		a.releasePending = true
		a.mu.Unlock()
		return nil
	}
	a.released = true
	a.mu.Unlock()
	a.releaseGeneration()
	return nil
}

func (a *driverLease) finishInvocation(activeCallback bool) {
	if activeCallback {
		a.runtime.mu.Lock()
		a.generation.activeCallbacks--
		a.runtime.mu.Unlock()
	}

	a.mu.Lock()
	a.invoking = false
	releaseNow := a.releasePending && !a.released
	if releaseNow {
		a.released = true
	}
	a.mu.Unlock()
	if releaseNow {
		a.releaseGeneration()
	}
}

func (a *driverLease) releaseGeneration() {
	a.runtime.mu.Lock()
	defer a.runtime.mu.Unlock()
	if a.generation.inFlight > 0 {
		a.generation.inFlight--
		if a.generation.inFlight == 0 && !a.generation.accepting {
			close(a.generation.drained)
		}
	}
}
