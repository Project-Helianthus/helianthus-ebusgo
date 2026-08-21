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

// DriverRuntimeConfig bounds both the pre-close drain and post-close proof.
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
}

type driverRuntimeGeneration struct {
	transport *ManagedRawTransport
	cancel    context.CancelFunc

	accepting bool
	inFlight  int
	drained   chan struct{}
}

// driverLease owns one admitted invocation. Although its concrete type is
// intentionally private, callers of Admit can use its exported methods. This
// prevents an adapter-local lifecycle detail becoming an exported API type.
type driverLease struct {
	runtime    *DriverRuntime
	generation *driverRuntimeGeneration
	id         uint64
	once       sync.Once
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
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	return r.startLocked(ctx)
}

// Replace retires the active generation before constructing a new one. If the
// old generation cannot be proven closed, quarantine prevents replacement.
func (r *DriverRuntime) Replace(ctx context.Context) (uint64, error) {
	r.operationMu.Lock()
	defer r.operationMu.Unlock()

	if err := r.stopLocked(); err != nil {
		return 0, err
	}
	return r.startLocked(ctx)
}

// Stop withdraws admission, cancels the exact generation, drains already
// admitted work, then closes and proves the generation retired.
func (r *DriverRuntime) Stop(context.Context) error {
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	return r.stopLocked()
}

func (r *DriverRuntime) startLocked(ctx context.Context) (uint64, error) {
	// Start owns operationMu in the public method; inline the implementation to
	// keep Replace one indivisible stop-then-start operation.
	r.mu.Lock()
	if r.quarantined {
		r.mu.Unlock()
		return 0, ErrDriverSafetyQuarantined
	}
	if r.current != nil {
		generation := r.generation
		r.mu.Unlock()
		return generation, nil
	}
	factory := r.factory
	r.mu.Unlock()
	if factory == nil {
		return 0, ErrDriverUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	generationCtx, cancel := context.WithCancel(context.Background())
	managed, err := factory(generationCtx)
	if err != nil {
		cancel()
		return 0, err
	}
	if !validManagedTransport(managed) {
		cancel()
		r.quarantine()
		return 0, ErrDriverSafetyQuarantined
	}
	if err := ctx.Err(); err != nil {
		if !r.retireUnadmitted(managed, cancel) {
			return 0, ErrDriverSafetyQuarantined
		}
		return 0, err
	}
	r.mu.Lock()
	if r.quarantined {
		r.mu.Unlock()
		if !r.retireUnadmitted(managed, cancel) {
			return 0, ErrDriverSafetyQuarantined
		}
		return 0, ErrDriverSafetyQuarantined
	}
	defer r.mu.Unlock()
	r.generation++
	r.revision++
	r.current = &driverRuntimeGeneration{transport: managed, cancel: cancel, accepting: true, drained: make(chan struct{})}
	return r.generation, nil
}

func (r *DriverRuntime) stopLocked() error {
	r.mu.Lock()
	if r.quarantined {
		r.mu.Unlock()
		return ErrDriverSafetyQuarantined
	}
	generation := r.current
	if generation == nil {
		r.mu.Unlock()
		return nil
	}
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

	deadline, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	drainTimedOut := !waitForGeneration(deadline, generation.drained)
	if !r.retireManaged(deadline, generation.transport, generation.cancel) {
		r.quarantine()
		return ErrDriverSafetyQuarantined
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
	select {
	case managed.Lifecycle.CloseRequest <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	select {
	case <-managed.Lifecycle.Closed:
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
	if fn == nil {
		return ErrDriverUnavailable
	}
	a.runtime.mu.Lock()
	valid := !a.runtime.quarantined && a.runtime.current == a.generation && a.generation.accepting
	a.runtime.mu.Unlock()
	if !valid {
		return ErrStaleDriverGeneration
	}
	return fn(a.generation.transport.Transport)
}

// Release ends the admission's in-flight ownership. It is safe to call once
// or repeatedly; only the first call changes drain accounting.
func (a *driverLease) Release() error {
	a.once.Do(func() {
		a.runtime.mu.Lock()
		defer a.runtime.mu.Unlock()
		if a.generation.inFlight > 0 {
			a.generation.inFlight--
			if a.generation.inFlight == 0 && !a.generation.accepting {
				close(a.generation.drained)
			}
		}
	})
	return nil
}
