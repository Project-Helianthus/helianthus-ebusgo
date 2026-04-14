package protocol

import (
	"context"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// synTestTransport is a minimal transport for waitForSyn unit tests.
type synTestTransport struct {
	events    []synTestEvent
	pos       int
	unescaped bool
}

type synTestEvent struct {
	value byte
	err   error
}

func (t *synTestTransport) ReadByte() (byte, error) {
	if t.pos >= len(t.events) {
		return 0, ebuserrors.ErrTransportClosed
	}
	ev := t.events[t.pos]
	t.pos++
	return ev.value, ev.err
}

func (t *synTestTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *synTestTransport) Close() error                { return nil }
func (t *synTestTransport) BytesAreUnescaped() bool      { return t.unescaped }

var _ transport.RawTransport = (*synTestTransport)(nil)
var _ transport.EscapeAware = (*synTestTransport)(nil)

func TestWaitForSyn_UnescapedTransport_CountsRawSyn(t *testing.T) {
	t.Parallel()

	tr := &synTestTransport{
		unescaped: true,
		events: []synTestEvent{
			{value: 0x42},
			{value: SymbolSyn},  // SYN #1
			{value: 0x10},
			{value: SymbolSyn},  // SYN #2
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
			{value: 0x01},        // -> decoded as data 0xAA, NOT a SYN boundary
			{value: SymbolSyn},   // real SYN #1
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
