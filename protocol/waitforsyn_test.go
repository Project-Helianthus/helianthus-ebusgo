package protocol

import (
	"context"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// synTestTransport is a minimal transport for waitForSyn unit tests.
// As of F-23 (batch-19), the transport implements both RawTransport
// and EscapeFlaggedReader so tests can exercise either path via the
// `unescaped` flag and per-event `wasEscaped` markers.
type synTestTransport struct {
	events    []synTestEvent
	pos       int
	unescaped bool
}

type synTestEvent struct {
	value      byte
	err        error
	wasEscaped bool // set true to simulate an escape-decoded payload byte
}

func (t *synTestTransport) ReadByte() (byte, error) {
	if t.pos >= len(t.events) {
		return 0, ebuserrors.ErrTransportClosed
	}
	ev := t.events[t.pos]
	t.pos++
	return ev.value, ev.err
}

// ReadByteWithEscape mirrors ReadByte and exposes the per-event
// WasEscaped flag. F-23 (batch-19, 2026-05-13): the waitForSyn ENH
// path now type-asserts the transport for EscapeFlaggedReader so
// escape-decoded payload 0xAA bytes (wasEscaped=true) can be
// distinguished from real wire SYNs (wasEscaped=false).
func (t *synTestTransport) ReadByteWithEscape() (byte, bool, error) {
	if t.pos >= len(t.events) {
		return 0, false, ebuserrors.ErrTransportClosed
	}
	ev := t.events[t.pos]
	t.pos++
	return ev.value, ev.wasEscaped, ev.err
}

func (t *synTestTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *synTestTransport) Close() error                { return nil }
func (t *synTestTransport) BytesAreUnescaped() bool     { return t.unescaped }

var _ transport.RawTransport = (*synTestTransport)(nil)
var _ transport.EscapeAware = (*synTestTransport)(nil)
var _ transport.EscapeFlaggedReader = (*synTestTransport)(nil)

func TestWaitForSyn_UnescapedTransport_CountsRawSyn(t *testing.T) {
	t.Parallel()

	tr := &synTestTransport{
		unescaped: true,
		events: []synTestEvent{
			{value: 0x42},
			{value: SymbolSyn}, // SYN #1
			{value: 0x10},
			{value: SymbolSyn}, // SYN #2
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 2)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil", err)
	}
	if tr.pos != 4 {
		t.Fatalf("consumed %d events; want 4", tr.pos)
	}
}

func TestWaitForSyn_PlainTransport_DoesNotCountEscapedSyn(t *testing.T) {
	t.Parallel()

	// Stream: 0xA9, 0x01 (escaped SYN = data byte, not real SYN),
	//         then 0xAA (real SYN).
	// waitForSyn(count=1) should skip the escaped sequence and only count
	// the standalone 0xAA.
	tr := &synTestTransport{
		unescaped: false,
		events: []synTestEvent{
			{value: SymbolEscape}, // escape prefix
			{value: 0x01},         // -> decoded as data 0xAA, NOT a SYN boundary
			{value: SymbolSyn},    // real SYN #1
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 1)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil", err)
	}
	if tr.pos != 3 {
		t.Fatalf("consumed %d events; want 3", tr.pos)
	}
}

func TestWaitForSyn_PlainTransport_EscapedSynNotCounted(t *testing.T) {
	t.Parallel()

	// Stream: 0xA9, 0x01 (escaped SYN data), 0xA9, 0x01 (another escaped),
	//         0xAA (real SYN #1), 0xAA (real SYN #2).
	// waitForSyn(count=2) must see exactly 2 real SYNs.
	tr := &synTestTransport{
		unescaped: false,
		events: []synTestEvent{
			{value: SymbolEscape},
			{value: 0x01},
			{value: SymbolEscape},
			{value: 0x01},
			{value: SymbolSyn}, // real SYN #1
			{value: SymbolSyn}, // real SYN #2
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 2)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil", err)
	}
	if tr.pos != 6 {
		t.Fatalf("consumed %d events; want 6", tr.pos)
	}
}

func TestWaitForSyn_ContinuesOnInvalidPayload(t *testing.T) {
	t.Parallel()

	// Stream: ErrInvalidPayload (continuable), then SYN.
	tr := &synTestTransport{
		unescaped: false,
		events: []synTestEvent{
			{err: ebuserrors.ErrInvalidPayload},
			{value: SymbolSyn},
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 1)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil (ErrInvalidPayload should be continuable)", err)
	}
}

func TestWaitForSyn_ContinuesOnInvalidPayload_Unescaped(t *testing.T) {
	t.Parallel()

	tr := &synTestTransport{
		unescaped: true,
		events: []synTestEvent{
			{err: ebuserrors.ErrInvalidPayload},
			{value: SymbolSyn},
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 1)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil (ErrInvalidPayload should be continuable)", err)
	}
}

func TestWaitForSyn_ZeroCountReturnsImmediately(t *testing.T) {
	t.Parallel()

	tr := &synTestTransport{unescaped: false}
	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx := context.Background()

	err := bus.waitForSyn(ctx, ctx, 0)
	if err != nil {
		t.Fatalf("waitForSyn(0) error = %v; want nil", err)
	}
}

func TestWaitForSyn_ContinuesOnTimeout(t *testing.T) {
	t.Parallel()

	tr := &synTestTransport{
		unescaped: false,
		events: []synTestEvent{
			{err: ebuserrors.ErrTimeout},
			{value: SymbolSyn},
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 1)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil", err)
	}
}

func TestWaitForSyn_ContinuesOnAdapterReset(t *testing.T) {
	t.Parallel()

	tr := &synTestTransport{
		unescaped: false,
		events: []synTestEvent{
			{err: ebuserrors.ErrAdapterReset},
			{value: SymbolSyn},
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 1)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil", err)
	}
}

// TestWaitForSyn_UnescapedTransport_SkipsEscapedSymbolSyn pins the
// F-23 (batch-19, 2026-05-13) fix for the Codex bot review on
// Project-Helianthus/helianthus-ebusgo#154: when the ENH-class
// transport now correctly delivers escape-decoded payload 0xAA
// bytes (originally wire `0xA9 0x01`), the SYN-counting logic must
// skip those — they are user payload, not bus-idle markers.
//
// Pre-F-23, the same bytes arrived as raw `0xA9, 0x01` (two bytes,
// neither equal to 0xAA) so the bug was masked by the ENH leak.
// Post-F-23 the transport surfaces logical 0xAA with
// WasEscaped=true; waitForSyn MUST observe that flag.
//
// The collision retry/backoff path uses waitForSyn to decide when
// the bus has truly gone idle. A false SYN-count there would let
// the bus try to re-arbitrate while another telegram is still on
// the wire — exactly the symptom Codex flagged.
func TestWaitForSyn_UnescapedTransport_SkipsEscapedSymbolSyn(t *testing.T) {
	t.Parallel()

	// Stream: escape-decoded 0xAA (payload byte, NOT a SYN),
	//         escape-decoded 0xAA (still payload, still not a SYN),
	//         raw 0xAA (real wire SYN — counts).
	tr := &synTestTransport{
		unescaped: true,
		events: []synTestEvent{
			{value: SymbolSyn, wasEscaped: true},  // payload 0xAA — must skip
			{value: SymbolSyn, wasEscaped: true},  // payload 0xAA — must skip
			{value: SymbolSyn, wasEscaped: false}, // real SYN #1
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 1)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil", err)
	}
	if tr.pos != 3 {
		t.Fatalf("consumed %d events; want 3 (must traverse the two escape-decoded 0xAA bytes before counting the real SYN)", tr.pos)
	}
}

// TestWaitForSyn_UnescapedTransport_EscapedSynCountedAsSyn_PreFixRegression
// is the failing assertion under the pre-F-23-fix behavior. With the
// fix in place, the test expects waitForSyn to count ONLY the real
// SYN; a buggy implementation that ignores WasEscaped would terminate
// after the first escape-decoded 0xAA, leaving tr.pos at 1 and
// failing the consumption check.
func TestWaitForSyn_UnescapedTransport_EscapedSynCountedAsSyn_PreFixRegression(t *testing.T) {
	t.Parallel()

	tr := &synTestTransport{
		unescaped: true,
		events: []synTestEvent{
			{value: SymbolSyn, wasEscaped: true},  // would falsely terminate pre-fix
			{value: SymbolSyn, wasEscaped: false}, // the real SYN — must be the one that counts
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 1)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil", err)
	}
	if tr.pos != 2 {
		t.Fatalf("consumed %d events; want 2 (pre-fix bug would consume only 1)", tr.pos)
	}
}

func TestWaitForSyn_PlainTransport_ErrorResetsEscapeState(t *testing.T) {
	t.Parallel()

	// Stream: 0xA9 (escape prefix), then ErrTimeout resets escape state,
	// then 0xAA is a real SYN (not continuation of the interrupted escape).
	tr := &synTestTransport{
		unescaped: false,
		events: []synTestEvent{
			{value: SymbolEscape},
			{err: ebuserrors.ErrTimeout},
			{value: SymbolSyn}, // real SYN, not part of escape sequence
		},
	}

	bus := NewBus(tr, DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := bus.waitForSyn(ctx, ctx, 1)
	if err != nil {
		t.Fatalf("waitForSyn error = %v; want nil", err)
	}
	if tr.pos != 3 {
		t.Fatalf("consumed %d events; want 3", tr.pos)
	}
}
