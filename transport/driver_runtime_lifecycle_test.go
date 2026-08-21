package transport_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// TestDriverRuntime_DormantStartStopReplaceFencesGenerations fixes the
// provider-side lifecycle seam before the gateway DriverManager consumes it.
// The runtime starts dormant, serializes lifecycle work, withdraws admission
// before draining the retired generation, and never lets a stale generation
// invoke the raw transport.
func TestDriverRuntime_DormantStartStopReplaceFencesGenerations(t *testing.T) {
	t.Parallel()

	var constructed atomic.Int32
	first := newManagedLifecycleTransport()
	second := newManagedLifecycleTransport()
	instances := []*managedLifecycleTransport{first, second}

	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			index := int(constructed.Add(1)) - 1
			return managedTransport(instances[index]), nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: time.Second},
	)

	if got := constructed.Load(); got != 0 {
		t.Fatalf("dormant construction made %d instances; want 0", got)
	}
	if got := runtime.Generation(); got != 0 {
		t.Fatalf("dormant generation = %d; want 0", got)
	}
	if err := runtime.Invoke(context.Background(), func(transport.RawTransport) error { return nil }); !errors.Is(err, transport.ErrDriverUnavailable) {
		t.Fatalf("dormant invocation error = %v; want ErrDriverUnavailable", err)
	}

	firstGeneration, err := runtime.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if firstGeneration != 1 {
		t.Fatalf("first generation = %d; want 1", firstGeneration)
	}

	firstAdmission, err := runtime.Admit(context.Background())
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if firstAdmission.Generation() != firstGeneration {
		t.Fatalf("first admission generation = %d; want %d", firstAdmission.Generation(), firstGeneration)
	}
	if err := firstAdmission.Release(); err != nil {
		t.Fatalf("first admission release error = %v", err)
	}

	secondGeneration, err := runtime.Replace(context.Background())
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if secondGeneration != 2 {
		t.Fatalf("replacement generation = %d; want 2", secondGeneration)
	}
	if got := first.closeCalls.Load(); got != 1 {
		t.Fatalf("retired generation Close calls = %d; want 1", got)
	}
	if err := firstAdmission.Invoke(func(transport.RawTransport) error { return nil }); !errors.Is(err, transport.ErrStaleDriverGeneration) {
		t.Fatalf("stale admission invocation error = %v; want ErrStaleDriverGeneration", err)
	}

	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := second.closeCalls.Load(); got != 1 {
		t.Fatalf("active generation Close calls = %d; want 1", got)
	}
	if err := runtime.Invoke(context.Background(), func(transport.RawTransport) error { return nil }); !errors.Is(err, transport.ErrDriverUnavailable) {
		t.Fatalf("post-stop invocation error = %v; want ErrDriverUnavailable", err)
	}
}

// TestDriverRuntime_UnconfirmedCloseQuarantinesProcessEpoch makes the safety
// boundary explicit: after close cannot be confirmed, no retry, replacement,
// or construction is permitted until a process restart creates a new runtime.
func TestDriverRuntime_UnconfirmedCloseQuarantinesProcessEpoch(t *testing.T) {
	t.Parallel()

	var constructed atomic.Int32
	instance := newManagedLifecycleTransport()
	instance.closeConfirms = false
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			constructed.Add(1)
			return managedTransport(instance), nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: time.Millisecond},
	)

	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := runtime.Stop(context.Background()); !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("Stop() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	if !runtime.SafetyQuarantined() {
		t.Fatal("runtime is not safety quarantined after unconfirmed close")
	}
	if got := instance.rawCloseCalls.Load(); got != 0 {
		t.Fatalf("runtime called blocking RawTransport.Close %d times; want 0", got)
	}
	if _, err := runtime.Start(context.Background()); !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("quarantined Start() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	if _, err := runtime.Replace(context.Background()); !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("quarantined Replace() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	if got := constructed.Load(); got != 1 {
		t.Fatalf("quarantine constructed %d instances; want exactly 1", got)
	}
}

// TestDriverRuntime_StopWithdrawsAdmissionBeforeDrain proves the critical
// ordering: new work is rejected after stop begins, even while pre-withdrawal
// work still owns the bounded drain and before Close is allowed to run.
func TestDriverRuntime_StopWithdrawsAdmissionBeforeDrain(t *testing.T) {
	t.Parallel()

	instance := newManagedLifecycleTransport()
	generationContexts := make(chan context.Context, 1)
	runtime := transport.NewDriverRuntime(
		func(ctx context.Context) (*transport.ManagedRawTransport, error) {
			generationContexts <- ctx
			return managedTransport(instance), nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: time.Second},
	)

	if got := runtime.Revision(); got != 1 {
		t.Fatalf("initial revision = %d; want 1", got)
	}
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := runtime.Revision(); got != 2 {
		t.Fatalf("post-start revision = %d; want 2", got)
	}
	generationCtx := <-generationContexts

	admission, err := runtime.Admit(context.Background())
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- runtime.Stop(context.Background()) }()

	select {
	case <-generationCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Stop() did not cancel the exact active generation")
	}
	if _, err := runtime.Admit(context.Background()); !errors.Is(err, transport.ErrDriverUnavailable) {
		t.Fatalf("post-withdrawal admission error = %v; want ErrDriverUnavailable", err)
	}
	if got := instance.closeCalls.Load(); got != 0 {
		t.Fatalf("Close ran before admitted work drained; calls = %d", got)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("admission release error = %v", err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := runtime.Revision(); got != 3 {
		t.Fatalf("post-stop revision = %d; want 3", got)
	}
}

// TestDriverRuntime_StartCanceledAfterFactoryQuarantinesFailedCleanup covers
// the direct Start entry: an unadmitted transport still has to be retired and
// a failed cleanup must fence the process epoch before another constructor can
// run.
func TestDriverRuntime_StartCanceledAfterFactoryQuarantinesFailedCleanup(t *testing.T) {
	t.Parallel()

	var constructed atomic.Int32
	returned := make(chan struct{})
	allowReturn := make(chan struct{})
	instance := newManagedLifecycleTransport()
	instance.closeConfirms = false
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			constructed.Add(1)
			close(returned)
			<-allowReturn
			return managedTransport(instance), nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: 50 * time.Millisecond},
	)

	startCtx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := runtime.Start(startCtx)
		result <- err
	}()
	<-returned
	cancel()
	close(allowReturn)

	if err := <-result; !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("canceled Start() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	assertQuarantineRejectsNewConstruction(t, runtime, &constructed)
}

// TestDriverRuntime_ReplaceCanceledAfterFactoryQuarantinesFailedCleanup covers
// the start portion of serialized Replace after the old generation retired.
func TestDriverRuntime_ReplaceCanceledAfterFactoryQuarantinesFailedCleanup(t *testing.T) {
	t.Parallel()

	var constructed atomic.Int32
	returned := make(chan struct{})
	allowReturn := make(chan struct{})
	first := newManagedLifecycleTransport()
	second := newManagedLifecycleTransport()
	second.closeConfirms = false
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			switch constructed.Add(1) {
			case 1:
				return managedTransport(first), nil
			case 2:
				close(returned)
				<-allowReturn
				return managedTransport(second), nil
			default:
				return nil, fmt.Errorf("unexpected constructor call")
			}
		},
		transport.DriverRuntimeConfig{DrainTimeout: 50 * time.Millisecond},
	)
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("initial Start() error = %v", err)
	}

	replaceCtx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := runtime.Replace(replaceCtx)
		result <- err
	}()
	<-returned
	cancel()
	close(allowReturn)

	if err := <-result; !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("canceled Replace() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	assertQuarantineRejectsNewConstruction(t, runtime, &constructed)
}

// TestDriverRuntime_StopBoundedWhenRawCloseBlocks ensures that the lifecycle
// serializer never waits indefinitely on a legacy RawTransport.Close call.
func TestDriverRuntime_StopBoundedWhenRawCloseBlocks(t *testing.T) {
	t.Parallel()

	instance := newManagedLifecycleTransport()
	instance.closeConfirms = false
	instance.closeRelease = make(chan struct{})
	bound := 25 * time.Millisecond
	var constructed atomic.Int32
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			constructed.Add(1)
			return managedTransport(instance), nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: bound},
	)
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	stopped := make(chan error, 1)
	startedAt := time.Now()
	go func() { stopped <- runtime.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
			t.Fatalf("Stop() error = %v; want ErrDriverSafetyQuarantined", err)
		}
		if elapsed := time.Since(startedAt); elapsed > 4*bound {
			t.Fatalf("Stop() took %s; want bounded by %s", elapsed, 4*bound)
		}
	case <-time.After(4 * bound):
		t.Error("Stop() remained blocked behind RawTransport.Close")
		close(instance.closeRelease) // clean up the current implementation's closer
		<-stopped
	}
	if !runtime.SafetyQuarantined() {
		t.Fatal("runtime is not safety quarantined after unconfirmed close")
	}
	if got := instance.rawCloseCalls.Load(); got != 0 {
		t.Fatalf("runtime called blocking RawTransport.Close %d times; want 0", got)
	}
	assertQuarantineRejectsNewConstruction(t, runtime, &constructed)
}

// TestDriverRuntime_StopQuarantinesWhenCloseRequestIsNeverAccepted proves
// that runtime teardown is bounded even when an adversarial adapter has no
// worker receiving its transport-owned close request.
func TestDriverRuntime_StopQuarantinesWhenCloseRequestIsNeverAccepted(t *testing.T) {
	t.Parallel()

	request := make(chan struct{}) // no receiver
	closed := make(chan struct{})  // no proof
	raw := newManagedLifecycleTransport()
	var constructed atomic.Int32
	bound := 25 * time.Millisecond
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			constructed.Add(1)
			return &transport.ManagedRawTransport{
				Transport: raw,
				Lifecycle: transport.DriverLifecycleHandle{
					CloseRequest: request,
					Closed:       closed,
				},
			}, nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: bound},
	)
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	assertBoundedQuarantinedStop(t, runtime, raw, bound)
	assertQuarantineRejectsNewConstruction(t, runtime, &constructed)
}

// TestDriverRuntime_StopQuarantinesWhenCloseIsNeverConfirmed proves the
// distinct accepted-request path: a close worker receives the request but
// never publishes closure proof.
func TestDriverRuntime_StopQuarantinesWhenCloseIsNeverConfirmed(t *testing.T) {
	t.Parallel()

	request := make(chan struct{})
	closed := make(chan struct{}) // worker never closes it
	raw := newManagedLifecycleTransport()
	var constructed atomic.Int32
	bound := 25 * time.Millisecond
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			constructed.Add(1)
			return &transport.ManagedRawTransport{
				Transport: raw,
				Lifecycle: transport.DriverLifecycleHandle{
					CloseRequest: request,
					Closed:       closed,
				},
			}, nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: bound},
	)
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	accepted := make(chan struct{})
	go func() {
		<-request
		close(accepted)
	}()

	assertBoundedQuarantinedStop(t, runtime, raw, bound)
	select {
	case <-accepted:
	default:
		t.Fatal("close request was not accepted before quarantine")
	}
	assertQuarantineRejectsNewConstruction(t, runtime, &constructed)
}

// TestDriverRuntime_InvalidLifecycleHandleQuarantinesAtAdmission prevents a
// factory from admitting a transport that has no data-only teardown proof.
func TestDriverRuntime_InvalidLifecycleHandleQuarantinesAtAdmission(t *testing.T) {
	t.Parallel()

	var constructed atomic.Int32
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			constructed.Add(1)
			return &transport.ManagedRawTransport{Transport: newManagedLifecycleTransport()}, nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: time.Second},
	)
	if _, err := runtime.Start(context.Background()); !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("Start() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	assertQuarantineRejectsNewConstruction(t, runtime, &constructed)
}

// TestDriverRuntime_ClosedCloseRequestQuarantinesWithoutPanic proves that an
// adapter violating request-channel ownership cannot crash the process or hold
// the lifecycle serializer hostage.
func TestDriverRuntime_ClosedCloseRequestQuarantinesWithoutPanic(t *testing.T) {
	t.Parallel()

	request := make(chan struct{})
	close(request)
	closed := make(chan struct{})
	raw := newManagedLifecycleTransport()
	var constructed atomic.Int32
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			constructed.Add(1)
			return &transport.ManagedRawTransport{
				Transport: raw,
				Lifecycle: transport.DriverLifecycleHandle{CloseRequest: request, Closed: closed},
			}, nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: 25 * time.Millisecond},
	)
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	stopped := stopWithoutPanic(t, runtime)
	if !errors.Is(stopped, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("Stop() error = %v; want ErrDriverSafetyQuarantined", stopped)
	}
	assertQuarantineRejectsNewConstruction(t, runtime, &constructed)
}

// TestDriverRuntime_DrainTimeoutCanSafelyRetireWithFreshCloseBudget proves
// that a timed-out admitted lease is not an automatic quarantine when a fresh
// close phase can still request and prove retirement.
func TestDriverRuntime_DrainTimeoutCanSafelyRetireWithFreshCloseBudget(t *testing.T) {
	t.Parallel()

	firstRequest := make(chan struct{})
	firstClosed := make(chan struct{})
	first := newManagedLifecycleTransport()
	second := newManagedLifecycleTransport()
	var constructed atomic.Int32
	bound := 20 * time.Millisecond
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			switch constructed.Add(1) {
			case 1:
				return &transport.ManagedRawTransport{
					Transport: first,
					Lifecycle: transport.DriverLifecycleHandle{CloseRequest: firstRequest, Closed: firstClosed},
				}, nil
			case 2:
				return managedTransport(second), nil
			default:
				return nil, fmt.Errorf("unexpected constructor call")
			}
		},
		transport.DriverRuntimeConfig{DrainTimeout: bound},
	)
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	lease, err := runtime.Admit(context.Background())
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	go func() {
		<-firstRequest
		close(firstClosed)
	}()

	startedAt := time.Now()
	if err := runtime.Stop(context.Background()); !errors.Is(err, transport.ErrDriverStopTimeout) {
		t.Fatalf("Stop() error = %v; want ErrDriverStopTimeout", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 6*bound {
		t.Fatalf("Stop() took %s; want bounded drain plus close phases", elapsed)
	}
	if runtime.SafetyQuarantined() {
		t.Fatal("safe close confirmation after drain timeout quarantined runtime")
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("held lease Release() error = %v", err)
	}
	if generation, err := runtime.Start(context.Background()); err != nil || generation != 2 {
		t.Fatalf("safe post-timeout Start() = (%d, %v); want (2, nil)", generation, err)
	}
}

// TestDriverRuntime_ConcurrentStopSerializesOneRetirementRequest uses a
// barrier to race two stops against one generation. Only one lifecycle request
// may reach the adapter, and the released serializer must admit a later start.
func TestDriverRuntime_ConcurrentStopSerializesOneRetirementRequest(t *testing.T) {
	t.Parallel()

	first := newManagedLifecycleTransport()
	second := newManagedLifecycleTransport()
	var constructed atomic.Int32
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			switch constructed.Add(1) {
			case 1:
				return managedTransport(first), nil
			case 2:
				return managedTransport(second), nil
			default:
				return nil, fmt.Errorf("duplicate constructor call")
			}
		},
		transport.DriverRuntimeConfig{DrainTimeout: time.Second},
	)
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	barrier := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-barrier
			results <- runtime.Stop(context.Background())
		}()
	}
	close(barrier)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Stop() error = %v", err)
		}
	}
	if got := first.closeCalls.Load(); got != 1 {
		t.Fatalf("first retired generation close requests = %d; want 1", got)
	}
	if runtime.SafetyQuarantined() {
		t.Fatal("concurrent Stop() quarantined a confirmed retirement")
	}
	if generation, err := runtime.Start(context.Background()); err != nil || generation != 2 {
		t.Fatalf("post-concurrent-stop Start() = (%d, %v); want (2, nil)", generation, err)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("cleanup Stop() error = %v", err)
	}
	if got := second.closeCalls.Load(); got != 1 {
		t.Fatalf("second retired generation close requests = %d; want 1", got)
	}
}

// TestDriverRuntime_ConcurrentStopReplaceSerializesRetirementAndConstruction
// proves that racing operations neither duplicate a generation constructor nor
// send more than one close request per generation they retire.
func TestDriverRuntime_ConcurrentStopReplaceSerializesRetirementAndConstruction(t *testing.T) {
	t.Parallel()

	first := newManagedLifecycleTransport()
	second := newManagedLifecycleTransport()
	var constructed atomic.Int32
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			switch constructed.Add(1) {
			case 1:
				return managedTransport(first), nil
			case 2:
				return managedTransport(second), nil
			default:
				return nil, fmt.Errorf("duplicate constructor call")
			}
		},
		transport.DriverRuntimeConfig{DrainTimeout: time.Second},
	)
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	barrier := make(chan struct{})
	stopResult := make(chan error, 1)
	replaceResult := make(chan error, 1)
	go func() {
		<-barrier
		stopResult <- runtime.Stop(context.Background())
	}()
	go func() {
		<-barrier
		_, err := runtime.Replace(context.Background())
		replaceResult <- err
	}()
	close(barrier)
	if err := <-stopResult; err != nil {
		t.Fatalf("concurrent Stop() error = %v", err)
	}
	replaceErr := <-replaceResult
	if replaceErr != nil && !errors.Is(replaceErr, context.Canceled) {
		t.Fatalf("concurrent Replace() error = %v", replaceErr)
	}
	if got := constructed.Load(); got < 1 || got > 2 {
		t.Fatalf("constructors after Stop||Replace = %d; want 1 or 2", got)
	}
	if got := first.closeCalls.Load(); got != 1 {
		t.Fatalf("first retired generation close requests = %d; want 1", got)
	}
	if runtime.SafetyQuarantined() {
		t.Fatal("confirmed Stop||Replace retirement quarantined runtime")
	}
	if errors.Is(replaceErr, context.Canceled) {
		// Stop observed the provisional Replace registration first. The
		// replacement either observes cancellation before construction or
		// returns one provisional resource which must be retired without ever
		// becoming an admitted generation.
		if got := constructed.Load(); got == 2 {
			if closes := second.closeCalls.Load(); closes != 1 {
				t.Fatalf("canceled provisional replacement close requests = %d; want 1", closes)
			}
		}
		if got := runtime.Generation(); got != 1 {
			t.Fatalf("Stop-canceled Replace admitted generation = %d; want 1", got)
		}
		if _, err := runtime.Admit(context.Background()); !errors.Is(err, transport.ErrDriverUnavailable) {
			t.Fatalf("Stop-canceled Replace final Admit() error = %v; want ErrDriverUnavailable", err)
		}
		return
	}

	lease, err := runtime.Admit(context.Background())
	if errors.Is(err, transport.ErrDriverUnavailable) {
		// Replace retired first and Stop retired the replacement. The second
		// generation must have received exactly one request too.
		if got := second.closeCalls.Load(); got != 1 {
			t.Fatalf("second retired generation close requests = %d; want 1", got)
		}
		return
	}
	if err != nil {
		t.Fatalf("final Admit() error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("final lease Release() error = %v", err)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("final cleanup Stop() error = %v", err)
	}
	if got := second.closeCalls.Load(); got != 1 {
		t.Fatalf("second retired generation close requests = %d; want 1", got)
	}
}

// TestDriverRuntime_InvokeReleaseStopKeepsCallbackLeased proves that Release
// during a callback is pending-only: Stop cannot request close until the raw
// callback exits, and duplicate Invoke/Release calls remain harmless.
func TestDriverRuntime_InvokeReleaseStopKeepsCallbackLeased(t *testing.T) {
	t.Parallel()

	request := make(chan struct{})
	closed := make(chan struct{})
	raw := &callbackProbeTransport{}
	runtime := transport.NewDriverRuntime(
		func(context.Context) (*transport.ManagedRawTransport, error) {
			return &transport.ManagedRawTransport{
				Transport: raw,
				Lifecycle: transport.DriverLifecycleHandle{CloseRequest: request, Closed: closed},
			}, nil
		},
		transport.DriverRuntimeConfig{DrainTimeout: time.Second},
	)
	closeRequested := make(chan struct{})
	go func() {
		<-request
		close(closeRequested)
		raw.closed.Store(true)
		close(closed)
	}()
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	lease, err := runtime.Admit(context.Background())
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}

	callbackStarted := make(chan struct{})
	allowCallbackReturn := make(chan struct{})
	callbackReturned := make(chan struct{})
	invokeDone := make(chan error, 1)
	go func() {
		invokeDone <- lease.Invoke(func(tr transport.RawTransport) error {
			close(callbackStarted)
			<-allowCallbackReturn
			defer close(callbackReturned)
			_, err := tr.Write([]byte{0x01})
			return err
		})
	}()
	<-callbackStarted
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() during callback error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("duplicate Release() error = %v", err)
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- runtime.Stop(context.Background()) }()

	select {
	case <-closeRequested:
		t.Fatal("Stop() requested close before callback returned")
	case <-time.After(30 * time.Millisecond):
	}
	close(allowCallbackReturn)
	if err := <-invokeDone; err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	<-callbackReturned
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := raw.postCloseWrites.Load(); got != 0 {
		t.Fatalf("post-close writes = %d; want 0", got)
	}
	if err := lease.Invoke(func(transport.RawTransport) error { return nil }); !errors.Is(err, transport.ErrStaleDriverGeneration) {
		t.Fatalf("duplicate Invoke() error = %v; want ErrStaleDriverGeneration", err)
	}
}

// TestDriverRuntime_StartFactoryUsesCallerCancellation ensures a factory that
// blocks solely on its received context cannot outlive caller cancellation or
// leak an admitted generation.
func TestDriverRuntime_StartFactoryUsesCallerCancellation(t *testing.T) {
	t.Parallel()

	factoryStarted := make(chan context.Context, 1)
	second := newManagedLifecycleTransport()
	var calls atomic.Int32
	runtime := transport.NewDriverRuntime(
		func(ctx context.Context) (*transport.ManagedRawTransport, error) {
			switch calls.Add(1) {
			case 1:
				factoryStarted <- ctx
				<-ctx.Done()
				return nil, ctx.Err()
			case 2:
				return managedTransport(second), nil
			default:
				return nil, fmt.Errorf("unexpected constructor call")
			}
		},
		transport.DriverRuntimeConfig{DrainTimeout: 50 * time.Millisecond},
	)
	callerCtx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() {
		_, err := runtime.Start(callerCtx)
		startDone <- err
	}()
	<-factoryStarted
	cancel()
	select {
	case err := <-startDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Start() error = %v; want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("caller cancellation did not release blocked factory")
	}
	if got := runtime.Generation(); got != 0 {
		t.Fatalf("generation after canceled factory = %d; want 0", got)
	}
	if generation, err := runtime.Start(context.Background()); err != nil || generation != 1 {
		t.Fatalf("later Start() = (%d, %v); want (1, nil)", generation, err)
	}
}

// TestDriverRuntime_StopCancelsRegisteredFactory proves the ordering rule: a
// Start registered before Stop is canceled even though its factory is blocking
// and no lifecycle serializer lock may delay the cancellation signal.
func TestDriverRuntime_StopCancelsRegisteredFactory(t *testing.T) {
	t.Parallel()

	factoryStarted := make(chan context.Context, 1)
	second := newManagedLifecycleTransport()
	var calls atomic.Int32
	runtime := transport.NewDriverRuntime(
		func(ctx context.Context) (*transport.ManagedRawTransport, error) {
			switch calls.Add(1) {
			case 1:
				factoryStarted <- ctx
				<-ctx.Done()
				return nil, ctx.Err()
			case 2:
				return managedTransport(second), nil
			default:
				return nil, fmt.Errorf("unexpected constructor call")
			}
		},
		transport.DriverRuntimeConfig{DrainTimeout: 50 * time.Millisecond},
	)
	startDone := make(chan error, 1)
	go func() {
		_, err := runtime.Start(context.Background())
		startDone <- err
	}()
	<-factoryStarted
	stopDone := make(chan error, 1)
	go func() { stopDone <- runtime.Stop(context.Background()) }()
	select {
	case err := <-startDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop-canceled Start() error = %v; want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Stop() did not cancel registered blocked factory")
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Stop() remained blocked after factory cancellation")
	}
	if got := runtime.Generation(); got != 0 {
		t.Fatalf("generation after Stop-canceled factory = %d; want 0", got)
	}
	if generation, err := runtime.Start(context.Background()); err != nil || generation != 1 {
		t.Fatalf("later Start() = (%d, %v); want (1, nil)", generation, err)
	}
}

func stopWithoutPanic(t *testing.T, runtime *transport.DriverRuntime) (err error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("Stop() panicked: %v", recovered)
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return runtime.Stop(context.Background())
}

func assertBoundedQuarantinedStop(t *testing.T, runtime *transport.DriverRuntime, raw *managedLifecycleTransport, bound time.Duration) {
	t.Helper()
	startedAt := time.Now()
	err := runtime.Stop(context.Background())
	if !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("Stop() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 4*bound {
		t.Fatalf("Stop() took %s; want bounded by %s", elapsed, 4*bound)
	}
	if !runtime.SafetyQuarantined() {
		t.Fatal("runtime is not safety quarantined")
	}
	if got := raw.rawCloseCalls.Load(); got != 0 {
		t.Fatalf("runtime called RawTransport.Close %d times; want 0", got)
	}
}

func assertQuarantineRejectsNewConstruction(t *testing.T, runtime *transport.DriverRuntime, constructed *atomic.Int32) {
	t.Helper()
	if !runtime.SafetyQuarantined() {
		t.Fatal("runtime is not safety quarantined")
	}
	before := constructed.Load()
	if _, err := runtime.Start(context.Background()); !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("quarantined Start() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	if _, err := runtime.Replace(context.Background()); !errors.Is(err, transport.ErrDriverSafetyQuarantined) {
		t.Fatalf("quarantined Replace() error = %v; want ErrDriverSafetyQuarantined", err)
	}
	if got := constructed.Load(); got != before {
		t.Fatalf("quarantine constructed %d additional instances; want 0", got-before)
	}
}

type managedLifecycleTransport struct {
	closeCalls    atomic.Int32
	rawCloseCalls atomic.Int32
	confirmClose  chan struct{}
	closeConfirms bool
	closeRelease  chan struct{}
}

type callbackProbeTransport struct {
	closed          atomic.Bool
	postCloseWrites atomic.Int32
}

func (t *callbackProbeTransport) ReadByte() (byte, error) { return 0, transport.ErrDriverUnavailable }

func (t *callbackProbeTransport) Write(payload []byte) (int, error) {
	if t.closed.Load() {
		t.postCloseWrites.Add(1)
		return 0, transport.ErrDriverUnavailable
	}
	return len(payload), nil
}

func (t *callbackProbeTransport) Close() error { return nil }

func newManagedLifecycleTransport() *managedLifecycleTransport {
	return &managedLifecycleTransport{confirmClose: make(chan struct{}), closeConfirms: true}
}

func managedTransport(raw *managedLifecycleTransport) *transport.ManagedRawTransport {
	request := make(chan struct{})
	go func() {
		<-request
		raw.closeCalls.Add(1)
		if raw.closeConfirms {
			close(raw.confirmClose)
		}
	}()
	return &transport.ManagedRawTransport{
		Transport: raw,
		Lifecycle: transport.DriverLifecycleHandle{
			CloseRequest: request,
			Closed:       raw.confirmClose,
		},
	}
}

func (t *managedLifecycleTransport) ReadByte() (byte, error) {
	return 0, transport.ErrDriverUnavailable
}

func (t *managedLifecycleTransport) Write([]byte) (int, error) {
	return 0, transport.ErrDriverUnavailable
}

func (t *managedLifecycleTransport) Close() error {
	t.rawCloseCalls.Add(1)
	if t.closeRelease != nil {
		<-t.closeRelease
	}
	return nil
}
