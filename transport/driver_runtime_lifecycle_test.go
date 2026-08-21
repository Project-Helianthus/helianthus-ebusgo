package transport_test

import (
	"context"
	"errors"
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
		func(context.Context) (transport.ManagedRawTransport, error) {
			index := int(constructed.Add(1)) - 1
			return instances[index], nil
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
	instance.confirmClose = make(chan struct{}) // never confirmed
	runtime := transport.NewDriverRuntime(
		func(context.Context) (transport.ManagedRawTransport, error) {
			constructed.Add(1)
			return instance, nil
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
		func(ctx context.Context) (transport.ManagedRawTransport, error) {
			generationContexts <- ctx
			return instance, nil
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

type managedLifecycleTransport struct {
	closeCalls   atomic.Int32
	confirmClose chan struct{}
}

func newManagedLifecycleTransport() *managedLifecycleTransport {
	confirmed := make(chan struct{})
	close(confirmed)
	return &managedLifecycleTransport{confirmClose: confirmed}
}

func (t *managedLifecycleTransport) ReadByte() (byte, error) {
	return 0, transport.ErrDriverUnavailable
}

func (t *managedLifecycleTransport) Write([]byte) (int, error) {
	return 0, transport.ErrDriverUnavailable
}

func (t *managedLifecycleTransport) Close() error {
	t.closeCalls.Add(1)
	return nil
}

func (t *managedLifecycleTransport) WaitClosed(ctx context.Context) error {
	select {
	case <-t.confirmClose:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ transport.ManagedRawTransport = (*managedLifecycleTransport)(nil)
