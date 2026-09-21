package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

type lifecycleRead struct {
	b   byte
	err error
}

// lifecycleTransport is a deterministic protocol-facing lifecycle seam. Its
// first arbitration collides; later attempts succeed and writes echo directly.
type lifecycleTransport struct {
	mu             sync.Mutex
	reads          []lifecycleRead
	starts         int
	token          uint64
	decisions      []transport.CollisionRecoveryDecision
	tokenCalled    chan struct{}
	decisionCalled chan struct{}
	readEntered    chan struct{}
	readGate       <-chan struct{}
	onDecision     func(transport.CollisionRecoveryDecision)
}

func (t *lifecycleTransport) StartArbitration(byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.starts++
	if t.starts == 1 {
		return ebuserrors.ErrBusCollision
	}
	return nil
}
func (t *lifecycleTransport) ArbitrationSendsSource() bool { return false }
func (t *lifecycleTransport) ReadByte() (byte, error) {
	if t.readEntered != nil {
		select {
		case t.readEntered <- struct{}{}:
		default:
		}
	}
	if t.readGate != nil {
		<-t.readGate
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.reads) == 0 {
		return 0, ebuserrors.ErrTimeout
	}
	r := t.reads[0]
	t.reads = t.reads[1:]
	return r.b, r.err
}
func (t *lifecycleTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range p {
		t.reads = append(t.reads, lifecycleRead{b: b})
	}
	return len(p), nil
}
func (*lifecycleTransport) Close() error { return nil }
func (t *lifecycleTransport) CollisionRecoveryToken() uint64 {
	if t.tokenCalled != nil {
		select {
		case t.tokenCalled <- struct{}{}:
		default:
		}
	}
	return t.token
}
func (t *lifecycleTransport) CompleteCollisionRecovery(token uint64, d transport.CollisionRecoveryDecision) {
	t.mu.Lock()
	var callback func(transport.CollisionRecoveryDecision)
	if token == t.token && token != 0 && len(t.decisions) == 0 {
		t.decisions = append(t.decisions, d)
		callback = t.onDecision
		if t.decisionCalled != nil {
			select {
			case t.decisionCalled <- struct{}{}:
			default:
			}
		}
	}
	t.mu.Unlock()
	if callback != nil {
		callback(d)
	}
}
func (t *lifecycleTransport) snapshot() (int, []transport.CollisionRecoveryDecision) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.starts, append([]transport.CollisionRecoveryDecision(nil), t.decisions...)
}

func lifecycleBus(t *testing.T, tr transport.RawTransport) (*protocol.Bus, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	b := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	b.Run(ctx)
	return b, ctx, cancel
}
func lifecycleFrame() protocol.Frame {
	return protocol.Frame{Source: 0x10, Target: protocol.AddressBroadcast, Primary: 1, Secondary: 2}
}

func TestCollisionRecoveryLifecycle_RetryAfterTwoSynsExactlyOnce(t *testing.T) {
	tr := &lifecycleTransport{token: 7, reads: []lifecycleRead{{b: protocol.SymbolSyn}, {b: protocol.SymbolSyn}}}
	b, ctx, cancel := lifecycleBus(t, tr)
	defer cancel()
	if _, err := b.Send(ctx, lifecycleFrame()); err != nil {
		t.Fatalf("Send=%v", err)
	}
	starts, ds := tr.snapshot()
	if starts != 2 || len(ds) != 1 || ds[0] != transport.CollisionRecoveryRetry {
		t.Fatalf("starts/decisions=%d/%v, want 2/[Retry]", starts, ds)
	}
}

func TestCollisionRecoveryLifecycle_AbandonsWithoutRetry(t *testing.T) {
	tr := &lifecycleTransport{token: 9}
	b, _, cancel := lifecycleBus(t, tr)
	defer cancel()
	if _, err := b.Send(context.Background(), lifecycleFrame()); !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send=%v", err)
	}
	starts, ds := tr.snapshot()
	if starts != 1 || len(ds) != 1 || ds[0] != transport.CollisionRecoveryAbandon {
		t.Fatalf("starts/decisions=%d/%v, want 1/[Abandon]", starts, ds)
	}
}

func TestCollisionRecoveryLifecycle_CancelDuringBackoffAbandonsOnce(t *testing.T) {
	tr := &lifecycleTransport{token: 11, tokenCalled: make(chan struct{}, 1), decisionCalled: make(chan struct{}, 1)}
	b, ctx, stop := lifecycleBus(t, tr)
	defer stop()
	requestCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := b.Send(requestCtx, lifecycleFrame()); done <- err }()
	select {
	case <-tr.tokenCalled:
	case <-time.After(time.Second):
		t.Fatal("collision lifecycle not entered")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Send=%v", err)
	}
	select {
	case <-tr.decisionCalled:
	case <-time.After(time.Second):
		t.Fatal("abandon decision not observed")
	}
	_, ds := tr.snapshot()
	if len(ds) != 1 || ds[0] != transport.CollisionRecoveryAbandon {
		t.Fatalf("decisions=%v", ds)
	}
}

func TestCollisionRecoveryLifecycle_CancelDuringSynWaitAbandonsOnce(t *testing.T) {
	gate := make(chan struct{})
	tr := &lifecycleTransport{token: 12, tokenCalled: make(chan struct{}, 1), decisionCalled: make(chan struct{}, 1), readEntered: make(chan struct{}, 1), readGate: gate}
	b, ctx, stop := lifecycleBus(t, tr)
	defer stop()
	req, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := b.Send(req, lifecycleFrame()); done <- err }()
	select {
	case <-tr.readEntered:
	case <-time.After(time.Second):
		t.Fatal("waitForSyn did not read")
	}
	cancel()
	close(gate)
	select {
	case <-tr.decisionCalled:
	case <-time.After(time.Second):
		t.Fatal("abandon not observed")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Send=%v", err)
	}
	starts, ds := tr.snapshot()
	if starts != 1 || len(ds) != 1 || ds[0] != transport.CollisionRecoveryAbandon {
		t.Fatalf("starts/decisions=%d/%v", starts, ds)
	}
}

func TestCollisionRecoveryLifecycle_DelaysRebidUntilRetryDecision(t *testing.T) {
	release := make(chan struct{})
	tr := &lifecycleTransport{token: 13, reads: []lifecycleRead{{b: protocol.SymbolSyn}, {b: protocol.SymbolSyn}}, onDecision: func(d transport.CollisionRecoveryDecision) {
		if d == transport.CollisionRecoveryRetry {
			<-release
		}
	}}
	b, ctx, cancel := lifecycleBus(t, tr)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.Send(ctx, lifecycleFrame()); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		starts, ds := tr.snapshot()
		if len(ds) == 1 {
			if starts != 1 {
				t.Fatalf("retry Start before decision returned: %d", starts)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry decision not reached")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Send=%v", err)
	}
	starts, ds := tr.snapshot()
	if starts != 2 || len(ds) != 1 || ds[0] != transport.CollisionRecoveryRetry {
		t.Fatalf("starts/decisions=%d/%v", starts, ds)
	}
}

func TestCollisionRecoveryLifecycle_CancelAfterTwoSynsCompletesRetryOnceWithoutRebid(t *testing.T) {
	tr := &lifecycleTransport{
		token: 14,
		reads: []lifecycleRead{{b: protocol.SymbolSyn}, {b: protocol.SymbolSyn}},
	}
	b, runCtx, stop := lifecycleBus(t, tr)
	defer stop()
	requestCtx, cancelRequest := context.WithCancel(runCtx)
	defer cancelRequest()
	tr.onDecision = func(d transport.CollisionRecoveryDecision) {
		if d == transport.CollisionRecoveryRetry {
			cancelRequest()
		}
	}

	if _, err := b.Send(requestCtx, lifecycleFrame()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send=%v, want context cancellation after terminal Retry decision", err)
	}
	starts, ds := tr.snapshot()
	if starts != 1 || len(ds) != 1 || ds[0] != transport.CollisionRecoveryRetry {
		t.Fatalf("starts/decisions=%d/%v, want 1/[Retry] with no duplicate Abandon", starts, ds)
	}
}

type noLifecycleTransport struct{ inner *lifecycleTransport }

func (t *noLifecycleTransport) StartArbitration(b byte) error { return t.inner.StartArbitration(b) }
func (*noLifecycleTransport) ArbitrationSendsSource() bool    { return false }
func (t *noLifecycleTransport) ReadByte() (byte, error)       { return t.inner.ReadByte() }
func (t *noLifecycleTransport) Write(p []byte) (int, error)   { return t.inner.Write(p) }
func (*noLifecycleTransport) Close() error                    { return nil }
func TestCollisionRecoveryLifecycle_NonImplementingTransportKeepsRetryBehavior(t *testing.T) {
	inner := &lifecycleTransport{reads: []lifecycleRead{{b: protocol.SymbolSyn}, {b: protocol.SymbolSyn}}}
	tr := &noLifecycleTransport{inner: inner}
	b, ctx, cancel := lifecycleBus(t, tr)
	defer cancel()
	if _, err := b.Send(ctx, lifecycleFrame()); err != nil {
		t.Fatalf("Send=%v", err)
	}
	starts, ds := inner.snapshot()
	if starts != 2 {
		t.Fatalf("fallback starts=%d, want 2", starts)
	}
	if len(ds) != 0 {
		t.Fatalf("fallback decisions=%v", ds)
	}
}

func TestCollisionRecoveryLifecycle_ZeroTokenSkipsCompletion(t *testing.T) {
	tr := &lifecycleTransport{token: 0}
	b, _, cancel := lifecycleBus(t, tr)
	defer cancel()
	if _, err := b.Send(context.Background(), lifecycleFrame()); !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send=%v, want bounded collision result", err)
	}
	starts, ds := tr.snapshot()
	if starts != 1 || len(ds) != 0 {
		t.Fatalf("starts/decisions=%d/%v, want 1/[] for zero token", starts, ds)
	}
}
