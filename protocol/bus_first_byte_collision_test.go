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
// failure is classified as ErrBusCollision (BusOutcomeCollision via
// busOutcomeFromError), NOT as a generic echo_mismatch.
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
//   - error message "first-byte arbitration loss: ..." (NOT containing
//     "echo mismatch"), so busOutcomeFromError routes to
//     BusOutcomeCollision via the containsEchoMismatch=false branch.
//
// Codex r2 defect 2 (MEDIUM): no separate BusEventCollision is emitted —
// downstream observability already counts via the existing
// BusEventRetry / BusOutcomeCollision arm in
// bus_observability_store.go (busOutcomeFromError on the wrapped
// error feeds the retry Outcome). The test asserts on the ERROR
// classification, not on a dedicated BusEvent emission.
func TestFirstByteEchoMismatch_ForeignInitiator_RoutesToCollision(t *testing.T) {
	t.Parallel()

	// Transport: ENH-style (unescaped=true, ArbitrationSendsSource=true).
	// The echoF23TestTransport from bus_echo_f23_test.go fits perfectly
	// — reuse instead of duplicating the transport mock.
	tr := &echoF23TestTransport{unescaped: true}

	// Capture all bus events: post-defect-2 we assert there is NO
	// BusEventEchoMismatch (the new branch must NOT fall through to
	// the generic echo_mismatch emit).
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

	// Assertion (c): no BusEventEchoMismatch was emitted — the new
	// branch returns BEFORE the generic emit, and Codex r2 defect 2
	// dropped the direct BusEventCollision emit since the standard
	// outcome path (BusEventRetry with Outcome=BusOutcomeCollision)
	// is the canonical observability surface.
	obsMu.Lock()
	defer obsMu.Unlock()
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventEchoMismatch {
			t.Errorf("unexpected BusEventEchoMismatch — round-7 routes first-byte foreign-initiator to the collision outcome, not echo_mismatch: %+v", ev)
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
		if ev.Kind == protocol.BusEventEchoMismatch {
			sawEchoMismatch = true
		}
	}
	if !sawEchoMismatch {
		t.Errorf("no BusEventEchoMismatch observed; events seen: %d", len(observed))
	}
}

// TestFirstByteEchoMismatch_NonMaster_StaysEchoMismatch is the
// guard-layer test for the AddressClassMaster gate (Codex r2 minor
// test gap): a first-byte echo that is NOT a master-class byte is
// NOT an arbitration-loss observation — it is wire corruption or a
// bit-flip — and MUST still route through the generic
// BusEventEchoMismatch / "echo mismatch" path.
//
// Setup: DST=0x08 is the first byte after arbitration; the mock
// returns echo 0x55. AddressClassOf(0x55) inspects nibbles (5,5);
// neither 5 ∈ {0,1,3,7,F}, so the byte is AddressClassSlave (or
// AddressClassReserved). The round-7 gate
// `AddressClassOf(echo) == AddressClassMaster` is FALSE → new branch
// does NOT fire → generic "echo mismatch" path fires.
func TestFirstByteEchoMismatch_NonMaster_StaysEchoMismatch(t *testing.T) {
	t.Parallel()

	// Sanity: 0x55 must NOT be master-class for this test to mean
	// what its name says.
	if protocol.AddressClassOf(0x55) == protocol.AddressClassMaster {
		t.Fatalf("test premise broken: AddressClassOf(0x55) = AddressClassMaster; pick a non-master sentinel")
	}

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

	// Echo queue: the FIRST echo (for DST=0x08) is 0x55 — a
	// non-master-class byte. New branch is gated on
	// AddressClassMaster; not satisfied → falls through to generic.
	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: 0x55, wasEscaped: false},
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
		t.Errorf("err = %q; non-master first-byte mismatch must keep \"echo mismatch\" substring (routes to BusOutcomeEchoMismatch)", err.Error())
	}
	if strings.Contains(err.Error(), "first-byte arbitration loss") {
		t.Errorf("err = %q; non-master first-byte mismatch MUST NOT use first-byte arbitration loss message", err.Error())
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	var sawEchoMismatch bool
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventEchoMismatch {
			sawEchoMismatch = true
		}
	}
	if !sawEchoMismatch {
		t.Errorf("no BusEventEchoMismatch observed for non-master first-byte mismatch; events seen: %d", len(observed))
	}
}

// TestFirstByteEchoMismatch_WasEscaped_StaysEchoMismatch is the
// guard-layer test for the !echoWasEscaped gate (Codex r2 minor
// test gap): even when the first-byte echo is a master-class byte
// that does NOT match what we wrote, if it arrived via the
// escape-decoded path (WasEscaped=true), it cannot be a
// wire-level arbitration-loss signal — it is an escape-stream
// payload byte from an unrelated frame, NOT another initiator's SOF.
//
// Setup: DST=0x08 is the first byte after arbitration; the mock
// returns echo 0x10 (a master-class byte, P1 ID 0x10 per
// sourceAddressTableV1) WITH WasEscaped=true. The round-7 gate
// `!echoWasEscaped` is FALSE → new branch does NOT fire → falls
// through to the generic value-mismatch branch (echoes 0x10 vs raw
// 0x08, so `echo != raw` is true) → BusEventEchoMismatch.
func TestFirstByteEchoMismatch_WasEscaped_StaysEchoMismatch(t *testing.T) {
	t.Parallel()

	// Sanity: 0x10 must be master-class for this test to exercise the
	// !echoWasEscaped gate (otherwise the AddressClassMaster gate
	// would already exclude it and we'd be testing the wrong axis).
	if protocol.AddressClassOf(0x10) != protocol.AddressClassMaster {
		t.Fatalf("test premise broken: AddressClassOf(0x10) ≠ AddressClassMaster; pick a master sentinel")
	}

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

	// Echo queue: the FIRST echo (for DST=0x08) is 0x10 with
	// WasEscaped=true. Master-class byte (passes AddressClassMaster
	// gate) but WasEscaped=true defeats the !echoWasEscaped gate →
	// falls through to generic value-mismatch branch.
	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: 0x10, wasEscaped: true},
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
		t.Errorf("err = %q; escape-decoded first-byte mismatch must keep \"echo mismatch\" substring (routes to BusOutcomeEchoMismatch)", err.Error())
	}
	if strings.Contains(err.Error(), "first-byte arbitration loss") {
		t.Errorf("err = %q; escape-decoded first-byte mismatch MUST NOT use first-byte arbitration loss message — !echoWasEscaped gate must defeat the collision classification", err.Error())
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	var sawEchoMismatch bool
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventEchoMismatch {
			sawEchoMismatch = true
			if !ev.EchoWasEscaped {
				t.Errorf("BusEventEchoMismatch EchoWasEscaped = false, want true (provenance forwarded from the escape-decoded echo)")
			}
		}
	}
	if !sawEchoMismatch {
		t.Errorf("no BusEventEchoMismatch observed for escape-decoded first-byte mismatch; events seen: %d", len(observed))
	}
}
