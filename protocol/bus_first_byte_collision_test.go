package protocol_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// TestFirstByteEchoMismatch_ForeignInitiator_RoutesToCollision (batch-26
// round-7) verifies the first-byte-after-arbitration foreign-initiator
// split: when sendRawWithEcho's FIRST write after arbitration receives
// a wire echo of a master-class byte that is NOT what we wrote, the
// failure is classified as ErrBusCollision (BusOutcomeCollision +
// BusEventCollision), NOT as a generic echo_mismatch.
//
// Setup: ENH-style escapeFlaggedReader transport with
// ArbitrationSendsSource=true. Send a frame to slave-class target
// 0x08. With includeSource=false (set automatically when the transport
// is an arbitrationTransport with ArbitrationSendsSource()=true) the
// first byte sendInitiatorTelegram writes is telegram[1]=0x08 (DST).
// Mock the echo for that byte as 0x31 — a master-class byte (per
// sourceAddressTableV1: P1 priority Bus interface) — which is the
// classic "foreign initiator won arbitration, its SOF is on the wire
// where our DST echo should be" shape.
//
// Pre-round-7 result: bus.go's generic `if echo != raw` branch fired,
// emitting BusEventEchoMismatch and an error containing "echo mismatch".
// busOutcomeFromError mapped that to BusOutcomeEchoMismatch via the
// containsEchoMismatch substring check; the retry path still fired
// (it gates on Is(ErrBusCollision) || ... and gets retry-as-collision)
// but downstream counters at the gateway recorded the event under
// echo_mismatch — inflating the echo_mismatch population with what
// is structurally a collision.
//
// Post-round-7 result: the new isFirstByteAfterArbitration branch
// fires BEFORE the generic value-mismatch branch:
//   - emits BusEventCollision (not BusEventEchoMismatch)
//   - error message "first-byte arbitration loss: ..." (NOT containing
//     "echo mismatch"), so busOutcomeFromError routes to
//     BusOutcomeCollision via the containsEchoMismatch=false branch.
func TestFirstByteEchoMismatch_ForeignInitiator_RoutesToCollision(t *testing.T) {
	t.Parallel()

	// Transport: ENH-style (unescaped=true, ArbitrationSendsSource=true).
	// The echoF23TestTransport from bus_echo_f23_test.go fits perfectly
	// — reuse instead of duplicating the transport mock.
	tr := &echoF23TestTransport{unescaped: true}

	// Capture all bus events for kind/outcome assertions.
	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		observed = append(observed, ev)
		return nil
	})

	cfg := protocol.DefaultBusConfig()
	cfg.Observer = observer
	bus := protocol.NewBus(tr, cfg, 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	// Unbounded ctx — see TestBus_EscapeAware_SynEcho_RealWireSynAccepted
	// for the rationale (collision short-circuits without policy retry
	// when ctx is unbounded).
	ctx := context.Background()

	// Build a directed frame to slave-class target 0x08. With ENH +
	// ArbitrationSendsSource=true, sendInitiatorTelegram skips index 0
	// (SRC is sent during arbitration) and writes telegram[1]=0x08
	// (DST) as the FIRST byte after arbitration.
	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}

	// Echo queue: the FIRST echo (for DST=0x08) is the foreign-initiator
	// byte 0x31 (a master-class P1 address per sourceAddressTableV1).
	// AddressClassOf(0x31) == AddressClassMaster → new branch fires.
	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: 0x31, wasEscaped: false},
	}
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("Send returned nil; want wrapped ErrBusCollision (first-byte arbitration loss)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}

	// Assertion (a): the error message MUST NOT contain "echo mismatch"
	// — that substring is what busOutcomeFromError uses to discriminate
	// echo_mismatch from collision, and the round-7 contract is that
	// foreign-initiator first-byte cases route as collision.
	if strings.Contains(err.Error(), "echo mismatch") {
		t.Errorf("err = %q; MUST NOT contain \"echo mismatch\" substring (busOutcomeFromError would route to BusOutcomeEchoMismatch)", err.Error())
	}
	// Assertion (b): the error message MUST contain the arbitration-loss
	// marker so dashboards / log filters can isolate this class.
	if !strings.Contains(err.Error(), "first-byte arbitration loss") {
		t.Errorf("err = %q; want substring \"first-byte arbitration loss\"", err.Error())
	}

	// Assertion (c): the observer received a BusEventCollision event for
	// the foreign-initiator byte. There should be NO BusEventEchoMismatch
	// for this scenario (the new branch fires BEFORE the generic
	// value-mismatch branch).
	obsMu.Lock()
	defer obsMu.Unlock()
	var sawCollision bool
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventCollision {
			sawCollision = true
			if ev.Outcome != protocol.BusOutcomeCollision {
				t.Errorf("BusEventCollision Outcome = %v, want BusOutcomeCollision", ev.Outcome)
			}
			if ev.Byte != 0x31 {
				t.Errorf("BusEventCollision Byte = 0x%02X, want 0x31 (the foreign-initiator wire byte)", ev.Byte)
			}
			if ev.EchoWasEscaped {
				t.Errorf("BusEventCollision EchoWasEscaped = true, want false (the gate requires non-escaped echo)")
			}
		}
		if ev.Kind == protocol.BusEventEchoMismatch {
			t.Errorf("unexpected BusEventEchoMismatch — round-7 routes first-byte foreign-initiator to BusEventCollision, not echo_mismatch: %+v", ev)
		}
	}
	if !sawCollision {
		t.Errorf("no BusEventCollision observed; events seen: %d", len(observed))
		for i, ev := range observed {
			t.Logf("  [%d] Kind=%v Outcome=%v Byte=0x%02X", i, ev.Kind, ev.Outcome, ev.Byte)
		}
	}
}

// TestFirstByteEchoMismatch_NonFirstByte_StaysEchoMismatch is the
// negative control for the round-7 split: a value mismatch on a
// NON-first-byte (mid-frame) write must still route through the
// existing BusEventEchoMismatch / "echo mismatch" path — the new
// branch is gated on isFirstByteAfterArbitration to avoid pulling
// generic mid-frame corruption into the collision bucket.
//
// Setup: the same target/source as the positive test, but the FIRST
// echo (DST=0x08) is correct. The SECOND write (PB=0xB5) gets a
// foreign master-class echo of 0x31. Round-7's first-byte gate is
// false at this position → the generic value-mismatch branch fires
// → BusEventEchoMismatch + "echo mismatch" error string.
func TestFirstByteEchoMismatch_NonFirstByte_StaysEchoMismatch(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		observed = append(observed, ev)
		return nil
	})

	cfg := protocol.DefaultBusConfig()
	cfg.Observer = observer
	bus := protocol.NewBus(tr, cfg, 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}

	// Echo queue: first byte (DST) echoes cleanly; second byte (PB) gets
	// a foreign master-class echo of 0x31. isFirstByteAfterArbitration
	// is FALSE at the PB position → new branch does NOT fire → generic
	// "echo mismatch" path fires.
	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: 0x08, wasEscaped: false}, // DST echo clean
		{value: 0x31, wasEscaped: false}, // PB echo poisoned (mid-frame)
	}
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("Send returned nil; want ErrBusCollision wrapping \"echo mismatch\"")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}
	if !strings.Contains(err.Error(), "echo mismatch") {
		t.Errorf("err = %q; mid-frame mismatch must keep \"echo mismatch\" substring (routes to BusOutcomeEchoMismatch)", err.Error())
	}
	if strings.Contains(err.Error(), "first-byte arbitration loss") {
		t.Errorf("err = %q; mid-frame mismatch MUST NOT use first-byte arbitration loss message", err.Error())
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	var sawEchoMismatch bool
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventCollision {
			t.Errorf("unexpected BusEventCollision on mid-frame mismatch — round-7 gate is first-byte-only: %+v", ev)
		}
		if ev.Kind == protocol.BusEventEchoMismatch {
			sawEchoMismatch = true
		}
	}
	if !sawEchoMismatch {
		t.Errorf("no BusEventEchoMismatch observed; events seen: %d", len(observed))
	}
}
