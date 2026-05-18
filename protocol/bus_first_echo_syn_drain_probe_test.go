package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// Round-8 forensic probe (no behavior change). Verifies that the new
// firstByteWireSynObserved / firstByteSynDrain_* / histogram counters
// in sendRawWithEcho accurately classify the population of "first
// byte after arbitration sees wire SYN" events without changing the
// surfaced error.
//
// Source spec: dispatch brief 2026-05-17, instr/round-8-
// firstEchoSynDrainProbe. Probes the Attack-1 residual leak surfaced
// after round-7 (~4-5% per-active-frame echo_mismatch traced to
// grant→first-byte AUTO-SYN race surviving the postGrantPreEcho 50ms
// window).
//
// The probe site lives in protocol/bus.go sendRawWithEcho, at the
// existing ENH guard "echo == SymbolSyn && !echoWasEscaped && raw !=
// SymbolSyn". When isFirstByteAfterArbitration is also true, the
// probe reads up to 8 additional bytes via readByteWithEscape,
// classifies the first non-SYN, and records a histogram. The bus
// still returns ErrBusCollision in the exact same conditions as
// pre-instrumentation; the probed bytes are sacrificed (the
// sendWithRetries retry path re-arbitrates anyway).

// reuses echoF23TestTransport / echoF23Event from bus_echo_f23_test.go
// (same package). That transport scripts (byte, wasEscaped, err)
// echoes via ReadByteWithEscape, implements EscapeAware
// (unescaped=true), and ArbitrationSendsSource()=true so the bus
// writes telegram[1]=DST as the FIRST byte after arbitration.

// captureBus wires a bus + observer pair with the standard
// configuration the round-8 probe tests share. Returns the bus, the
// underlying transport, and a thread-safe slice of observed events.
func captureBus(t *testing.T, tr *echoF23TestTransport) *protocol.Bus {
	t.Helper()
	cfg := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, cfg, 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	t.Cleanup(runCancel)
	bus.Run(runCtx)
	return bus
}

// directedFrame returns a P0/P0-companion directed frame (Source
// 0x10, Target 0x08) suitable for driving sendInitiatorTelegram
// against the echoF23TestTransport. With ArbitrationSendsSource=true
// and unescaped=true, the FIRST wire byte the bus writes is DST=0x08.
func directedFrame() protocol.Frame {
	return protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}
}

// TestFirstEchoSynDrainProbe_NoDrain_WhenConditionFalse pins the
// negative control: when the first echo equals the written byte
// (raw=DST=0x08, echo=0x08), the probe MUST NOT fire. All counters
// remain zero, no histogram bucket is incremented.
//
// We don't care whether subsequent bytes succeed or time out — the
// invariant is that the probe is gated strictly on the wire-SYN
// guard condition, not just on isFirstByteAfterArbitration.
func TestFirstEchoSynDrainProbe_NoDrain_WhenConditionFalse(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}
	bus := captureBus(t, tr)
	ctx := context.Background()

	// Echo queue: first echo == DST (correct). Anything after either
	// times out (Send returns timeout) or drains harmlessly. The
	// probe ONLY fires on (raw != SYN, echo == SYN, !escaped); not
	// fulfilled here.
	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: 0x08, wasEscaped: false}, // DST echoed correctly
	}
	tr.mu.Unlock()

	_, _ = bus.Send(ctx, directedFrame()) // outcome irrelevant

	if got := bus.FirstByteWireSynObserved(); got != 0 {
		t.Errorf("FirstByteWireSynObserved = %d; want 0 (probe condition not met)", got)
	}
	if got := bus.FirstByteSynDrainWouldRecoverReal(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldRecoverReal = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainWouldHitForeignInit(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldHitForeignInit = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainWouldHitOther(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldHitOther = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainExhausted(); got != 0 {
		t.Errorf("FirstByteSynDrainExhausted = %d; want 0", got)
	}
	hist := bus.FirstByteSynDrainHistogram()
	for i, v := range hist {
		if v != 0 {
			t.Errorf("Histogram[%d] = %d; want 0 (probe must not fire on clean first echo)", i, v)
		}
	}
}

// TestFirstEchoSynDrainProbe_SingleSynThenRealEcho_HistogramBucket1_RecoverReal
// pins the "drain would have worked" measurement path. The wire
// echo sequence after sending DST=0x08 is:
//
//	[0xAA, false]  ← wire SYN, triggers probe + original guard
//	[0x08, false]  ← what would have been the real DST echo
//
// Probe behavior: increment observed=1, drain one byte (counted as 1
// SYN), then read second byte 0x08 == raw → wouldRecoverReal=1,
// histogram[1]=1. The bus still returns ErrBusCollision with the
// "unexpected syn while waiting for echo" message; no behavior change.
func TestFirstEchoSynDrainProbe_SingleSynThenRealEcho_HistogramBucket1_RecoverReal(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}
	bus := captureBus(t, tr)
	ctx := context.Background()

	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: protocol.SymbolSyn, wasEscaped: false}, // poisoned first echo
		{value: 0x08, wasEscaped: false},               // what the real echo WOULD have been
	}
	tr.mu.Unlock()

	_, err := bus.Send(ctx, directedFrame())
	if err == nil {
		t.Fatal("Send err = nil; want wrapped ErrBusCollision (behavior must be unchanged by probe)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}

	if got := bus.FirstByteWireSynObserved(); got != 1 {
		t.Errorf("FirstByteWireSynObserved = %d; want 1", got)
	}
	if got := bus.FirstByteSynDrainWouldRecoverReal(); got != 1 {
		t.Errorf("FirstByteSynDrainWouldRecoverReal = %d; want 1", got)
	}
	if got := bus.FirstByteSynDrainWouldHitForeignInit(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldHitForeignInit = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainWouldHitOther(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldHitOther = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainExhausted(); got != 0 {
		t.Errorf("FirstByteSynDrainExhausted = %d; want 0", got)
	}
	hist := bus.FirstByteSynDrainHistogram()
	if hist[1] != 1 {
		t.Errorf("Histogram[1] = %d; want 1 (one SYN drained before real echo)", hist[1])
	}
	for i, v := range hist {
		if i == 1 {
			continue
		}
		if v != 0 {
			t.Errorf("Histogram[%d] = %d; want 0", i, v)
		}
	}
}

// TestFirstEchoSynDrainProbe_ThreeSynsThenForeignInit_HistogramBucket3_WouldHitForeignInit
// pins the "drain hits foreign initiator SOF" measurement path. The
// wire sequence is three idle SYNs followed by 0x31 (P1 initiator,
// AddressClassMaster). Round-8 cares about this case because a
// foreign initiator that arbitrates AFTER the idle-SYN burst would
// have stomped on our write regardless — the drain doesn't recover
// us, but it does correctly classify the failure as collision (not
// echo_mismatch).
func TestFirstEchoSynDrainProbe_ThreeSynsThenForeignInit_HistogramBucket3_WouldHitForeignInit(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}
	bus := captureBus(t, tr)
	ctx := context.Background()

	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: protocol.SymbolSyn, wasEscaped: false}, // poisoned first echo
		{value: protocol.SymbolSyn, wasEscaped: false}, // drain[0]
		{value: protocol.SymbolSyn, wasEscaped: false}, // drain[1]
		{value: protocol.SymbolSyn, wasEscaped: false}, // drain[2]
		{value: 0x31, wasEscaped: false},               // foreign-initiator SOF (P1 master)
	}
	tr.mu.Unlock()

	_, err := bus.Send(ctx, directedFrame())
	if err == nil {
		t.Fatal("Send err = nil; want wrapped ErrBusCollision")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}

	if got := bus.FirstByteWireSynObserved(); got != 1 {
		t.Errorf("FirstByteWireSynObserved = %d; want 1", got)
	}
	if got := bus.FirstByteSynDrainWouldHitForeignInit(); got != 1 {
		t.Errorf("FirstByteSynDrainWouldHitForeignInit = %d; want 1", got)
	}
	if got := bus.FirstByteSynDrainWouldRecoverReal(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldRecoverReal = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainWouldHitOther(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldHitOther = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainExhausted(); got != 0 {
		t.Errorf("FirstByteSynDrainExhausted = %d; want 0", got)
	}
	hist := bus.FirstByteSynDrainHistogram()
	if hist[3] != 1 {
		t.Errorf("Histogram[3] = %d; want 1 (3 SYNs drained before foreign initiator)", hist[3])
	}
	for i, v := range hist {
		if i == 3 {
			continue
		}
		if v != 0 {
			t.Errorf("Histogram[%d] = %d; want 0", i, v)
		}
	}
}

// TestFirstEchoSynDrainProbe_AllSyns_HistogramBucket8_Exhausted pins
// the "drain cap exhausted" case. The bus is fed the wire-SYN
// trigger plus 8 additional raw SYNs (total 9). Probe reads probeCap
// (=8) additional bytes, all are SYNs, so the burst exceeded the
// cap. Round-8 uses this counter to decide whether a different cap
// is needed for round-9.
func TestFirstEchoSynDrainProbe_AllSyns_HistogramBucket8_Exhausted(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}
	bus := captureBus(t, tr)
	ctx := context.Background()

	echo := make([]echoF23Event, 0, 9)
	for i := 0; i < 9; i++ {
		echo = append(echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	}
	tr.mu.Lock()
	tr.echo = echo
	tr.mu.Unlock()

	_, err := bus.Send(ctx, directedFrame())
	if err == nil {
		t.Fatal("Send err = nil; want wrapped ErrBusCollision")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}

	if got := bus.FirstByteWireSynObserved(); got != 1 {
		t.Errorf("FirstByteWireSynObserved = %d; want 1", got)
	}
	if got := bus.FirstByteSynDrainExhausted(); got != 1 {
		t.Errorf("FirstByteSynDrainExhausted = %d; want 1", got)
	}
	if got := bus.FirstByteSynDrainWouldRecoverReal(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldRecoverReal = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainWouldHitForeignInit(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldHitForeignInit = %d; want 0", got)
	}
	if got := bus.FirstByteSynDrainWouldHitOther(); got != 0 {
		t.Errorf("FirstByteSynDrainWouldHitOther = %d; want 0", got)
	}
	hist := bus.FirstByteSynDrainHistogram()
	if hist[8] != 1 {
		t.Errorf("Histogram[8] = %d; want 1 (8 SYNs drained = cap exhausted)", hist[8])
	}
	for i, v := range hist {
		if i == 8 {
			continue
		}
		if v != 0 {
			t.Errorf("Histogram[%d] = %d; want 0", i, v)
		}
	}
}

// TestFirstEchoSynDrainProbe_NoEffect_OnNonFirstByte pins the
// gating invariant: the probe must NEVER fire when
// isFirstByteAfterArbitration is false. Setup: first echo (for
// DST=0x08) is correct; the SECOND echo (for PB=0xB5) is a wire SYN.
// The second byte's wire-SYN guard still fires and returns
// ErrBusCollision, but the probe stays silent — round-8 only cares
// about the grant→first-byte race window, not generic mid-frame
// collisions.
func TestFirstEchoSynDrainProbe_NoEffect_OnNonFirstByte(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}
	bus := captureBus(t, tr)
	ctx := context.Background()

	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: 0x08, wasEscaped: false},               // correct DST echo (first byte clean)
		{value: protocol.SymbolSyn, wasEscaped: false}, // wire-SYN intrusion on PB position
	}
	tr.mu.Unlock()

	_, err := bus.Send(ctx, directedFrame())
	if err == nil {
		t.Fatal("Send err = nil; want wrapped ErrBusCollision from non-first-byte wire SYN")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}

	if got := bus.FirstByteWireSynObserved(); got != 0 {
		t.Errorf("FirstByteWireSynObserved = %d; want 0 (probe must not fire on byte index > 0)", got)
	}
	hist := bus.FirstByteSynDrainHistogram()
	for i, v := range hist {
		if v != 0 {
			t.Errorf("Histogram[%d] = %d; want 0 (probe must not fire on non-first byte)", i, v)
		}
	}
}

// TestFirstEchoSynDrainProbe_NoEffect_OnEscapedPayload pins the
// !echoWasEscaped gate: when the first echo is logical 0xAA but
// WasEscaped=true (an escape-decoded payload 0xAA from unrelated
// in-flight traffic), the F-23 r4 guard fires (different branch
// further down sendRawWithEcho), but the probe MUST NOT — its
// guard explicitly excludes escape-decoded SYNs because that's a
// different physical event (escaped data byte, not a real wire SYN
// after grant).
func TestFirstEchoSynDrainProbe_NoEffect_OnEscapedPayload(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}
	bus := captureBus(t, tr)
	ctx := context.Background()

	tr.mu.Lock()
	tr.echo = []echoF23Event{
		// Logical 0xAA but escape-decoded — this is a payload 0xAA
		// from unrelated wire traffic (wire `A9 01` pre-decoded). The
		// probe condition `!echoWasEscaped` is FALSE, so the probe
		// stays quiet even though the byte value matches SYN.
		{value: protocol.SymbolSyn, wasEscaped: true},
	}
	tr.mu.Unlock()

	_, _ = bus.Send(ctx, directedFrame())
	// We don't assert on the error: depending on which downstream
	// guard fires (or whether the test transport's queue empties to
	// timeout), the surfaced error can vary. What matters is the
	// probe counters.

	if got := bus.FirstByteWireSynObserved(); got != 0 {
		t.Errorf("FirstByteWireSynObserved = %d; want 0 (probe must not fire on escape-decoded payload 0xAA)", got)
	}
	hist := bus.FirstByteSynDrainHistogram()
	for i, v := range hist {
		if v != 0 {
			t.Errorf("Histogram[%d] = %d; want 0 (probe must not fire on escape-decoded 0xAA)", i, v)
		}
	}
}

// Suppress unused-import warnings if the test file evolves to drop
// the sync import — pinned here to keep parallel with the sibling
// tests in bus_first_byte_collision_test.go which use sync.Mutex
// for the observer slice.
var _ = sync.Mutex{}
