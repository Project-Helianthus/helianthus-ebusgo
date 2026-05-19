package transport_test

import (
	"net"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// Phase 1 Step B1 (frame-atomic-visibility v8 §1.3 / §5, invariant
// I4): integration tests at the ENHTransport boundary verifying
// that the v8 admin events from the AA-aware escape decoder are
// correctly routed to the public counter accessors. These tests are
// the regression guard for Codex's round-1 MINOR finding — the
// decoder-only tests in ebus_escape_v8_test.go prove the decoder
// emits admin events; these tests prove the ENHTransport
// feedEscapeDecoderLocked plumbing wires those events to the right
// counters.

// TestENHCounters_PlainBytes_AllZero pins the baseline — a stream
// with no escape sequences must leave all v8 counters at zero.
func TestENHCounters_PlainBytes_AllZero(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	feedENHReceivedBytes(t, server, []byte{0x00, 0x55, 0xFE, 0x7F})

	_ = drainBytes(t, enh, 4)

	assertCountersZero(t, enh)
}

// TestENHCounters_AaAbsorbed pins that an in-budget AA-injection
// increments EscapeAaAbsorbedTotal per absorbed byte and leaves the
// fault counters at zero (clean recovery).
func TestENHCounters_AaAbsorbed(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	// 0xA9 (lead), 0xAA (absorbed), 0xAA (absorbed), 0x01 (real completion → emits 0xAA WasEscaped=true)
	feedENHReceivedBytes(t, server, []byte{0xA9, 0xAA, 0xAA, 0x01})

	events := drainBytes(t, enh, 1)
	if events[0].Byte != 0xAA || !events[0].WasEscaped {
		t.Fatalf("decoded event = {Byte=0x%02X WasEscaped=%v}; want {0xAA true}",
			events[0].Byte, events[0].WasEscaped)
	}

	if got := enh.EscapeAaAbsorbedTotal(); got != 2 {
		t.Errorf("EscapeAaAbsorbedTotal() = %d; want 2 (one per absorbed AA)", got)
	}
	if got := enh.EscapeRecoveryTotal(); got != 0 {
		t.Errorf("EscapeRecoveryTotal() = %d; want 0 (clean recovery)", got)
	}
	if got := enh.EscapeBudgetExhaustedTotal(); got != 0 {
		t.Errorf("EscapeBudgetExhaustedTotal() = %d; want 0", got)
	}
	if got := enh.EscapePendingTimeoutTotal(); got != 0 {
		t.Errorf("EscapePendingTimeoutTotal() = %d; want 0", got)
	}
	if got := enh.DecodeFaultTotal(); got != 0 {
		t.Errorf("DecodeFaultTotal() = %d; want 0 (absorption is not a fault)", got)
	}
}

// TestENHCounters_InvalidSecondByte_Recovery pins that
// AdminEventEscapeRecovery (0xA9 followed by a byte that's not
// 0x00/0x01/0xAA) increments both EscapeRecoveryTotal and
// DecodeFaultTotal, drops the offending bytes, and leaves the
// stream consistent for subsequent bytes.
func TestENHCounters_InvalidSecondByte_Recovery(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	// 0xA9 (lead), 0xFF (invalid → recovery, drop both), 0x55 (plain byte, emitted)
	feedENHReceivedBytes(t, server, []byte{0xA9, 0xFF, 0x55})

	events := drainBytes(t, enh, 1)
	if events[0].Byte != 0x55 || events[0].WasEscaped {
		t.Fatalf("decoded event = {Byte=0x%02X WasEscaped=%v}; want {0x55 false}",
			events[0].Byte, events[0].WasEscaped)
	}

	if got := enh.EscapeRecoveryTotal(); got != 1 {
		t.Errorf("EscapeRecoveryTotal() = %d; want 1", got)
	}
	if got := enh.DecodeFaultTotal(); got != 1 {
		t.Errorf("DecodeFaultTotal() = %d; want 1 (recovery is a fault)", got)
	}
	if got := enh.EscapeBudgetExhaustedTotal(); got != 0 {
		t.Errorf("EscapeBudgetExhaustedTotal() = %d; want 0", got)
	}
	if got := enh.EscapeAaAbsorbedTotal(); got != 0 {
		t.Errorf("EscapeAaAbsorbedTotal() = %d; want 0 (no AA absorbed before recovery)", got)
	}
}

// TestENHCounters_BudgetExhausted pins that
// AdminEventEscapeBudgetExhausted (0xA9 + 9 consecutive AAs)
// increments both EscapeBudgetExhaustedTotal and DecodeFaultTotal,
// AND increments EscapeAaAbsorbedTotal by exactly
// MaxAaAbsorptionsPerEscapePair (the 8 absorbed AAs, NOT the
// over-budget 9th).
func TestENHCounters_BudgetExhausted(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	// 0xA9 + 9 consecutive AAs (1 over budget).
	stream := []byte{0xA9}
	for i := 0; i < transport.MaxAaAbsorptionsPerEscapePair+1; i++ {
		stream = append(stream, 0xAA)
	}
	// Trailing plain byte to confirm clean resume.
	stream = append(stream, 0x77)
	feedENHReceivedBytes(t, server, stream)

	events := drainBytes(t, enh, 1)
	if events[0].Byte != 0x77 || events[0].WasEscaped {
		t.Fatalf("post-exhaust event = {Byte=0x%02X WasEscaped=%v}; want {0x77 false}",
			events[0].Byte, events[0].WasEscaped)
	}

	if got := enh.EscapeBudgetExhaustedTotal(); got != 1 {
		t.Errorf("EscapeBudgetExhaustedTotal() = %d; want 1", got)
	}
	if got := enh.DecodeFaultTotal(); got != 1 {
		t.Errorf("DecodeFaultTotal() = %d; want 1", got)
	}
	if got := enh.EscapeAaAbsorbedTotal(); got != uint64(transport.MaxAaAbsorptionsPerEscapePair) {
		t.Errorf("EscapeAaAbsorbedTotal() = %d; want %d (only the in-budget AAs count)",
			got, transport.MaxAaAbsorptionsPerEscapePair)
	}
	if got := enh.EscapeRecoveryTotal(); got != 0 {
		t.Errorf("EscapeRecoveryTotal() = %d; want 0", got)
	}
}

// NOTE — wall-clock timeout NOT covered by an integration test.
//
// Codex round-2 review on PR #165 correctly observed that any
// integration test that relies on a wall-clock sleep between two
// net.Pipe writes has an unavoidable race: net.Pipe.Write returns
// when the matching Read consumes the bytes from the underlying
// conn, but it does NOT prove the transport's read loop has yet
// fed those bytes through the escape decoder (the decoder feed
// happens AFTER the conn.Read returns, in the same goroutine but
// after a Parse step). If the scheduler delays the reader, the
// test's wall-clock sleep can elapse before the decoder records
// the lead's `leadObservedAt`, and the timeout will not fire.
//
// We have three orthogonal layers of coverage that, together, pin
// the wall-clock timeout contract without this race:
//
//   - `ebus_escape_v8_test.go::TestFeed_WallClockCap_*`: pins the
//     decoder-level timeout semantics deterministically with
//     synthetic times. Three tests cover beyond-cap, exact-boundary,
//     and timeout-byte-is-a-new-0xA9.
//   - `enh_transport_v8_counters_test.go` (this file): pins the
//     ENHTransport plumbing for the OTHER three admin event kinds
//     (Recovery, BudgetExhausted, AaAbsorbed); the timeout case's
//     wiring is a one-line `t.escapePendingTimeoutTotal.Add(1)`
//     visually parallel to the tested Recovery and BudgetExhausted
//     branches.
//   - `v8_constant_drift_test.go`: pins
//     transport.EscapePendingTimeout == protocol.FrameAtomicV8EscapePendingTimeout
//     so the constant cannot silently regress.
//
// If a future refactor moves the timeout wiring to a non-trivial
// shape, this note should be revisited — either a clock-injection
// hook on ENHTransport or a test-only "wait until decoder pending"
// synchronization barrier would unlock a deterministic integration
// test.

// TestENHCounters_AccumulatesAcrossEvents pins that the counters
// accumulate monotonically across multiple events of the same kind
// in a single ENHTransport instance.
func TestENHCounters_AccumulatesAcrossEvents(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	// Two consecutive recovery faults, then plain byte to confirm
	// the decoder is still healthy.
	feedENHReceivedBytes(t, server, []byte{
		0xA9, 0xFF, // recovery #1
		0xA9, 0xFE, // recovery #2
		0x33, // plain byte (emitted)
	})

	events := drainBytes(t, enh, 1)
	if events[0].Byte != 0x33 || events[0].WasEscaped {
		t.Fatalf("final event = {Byte=0x%02X WasEscaped=%v}; want {0x33 false}",
			events[0].Byte, events[0].WasEscaped)
	}

	if got := enh.EscapeRecoveryTotal(); got != 2 {
		t.Errorf("EscapeRecoveryTotal() = %d; want 2 (monotonic accumulation)", got)
	}
	if got := enh.DecodeFaultTotal(); got != 2 {
		t.Errorf("DecodeFaultTotal() = %d; want 2", got)
	}
}

// assertCountersZero is a helper used by the baseline test.
func assertCountersZero(t *testing.T, enh *transport.ENHTransport) {
	t.Helper()
	if got := enh.EscapeAaAbsorbedTotal(); got != 0 {
		t.Errorf("EscapeAaAbsorbedTotal() = %d; want 0", got)
	}
	if got := enh.EscapePendingTimeoutTotal(); got != 0 {
		t.Errorf("EscapePendingTimeoutTotal() = %d; want 0", got)
	}
	if got := enh.EscapeRecoveryTotal(); got != 0 {
		t.Errorf("EscapeRecoveryTotal() = %d; want 0", got)
	}
	if got := enh.EscapeBudgetExhaustedTotal(); got != 0 {
		t.Errorf("EscapeBudgetExhaustedTotal() = %d; want 0", got)
	}
	if got := enh.DecodeFaultTotal(); got != 0 {
		t.Errorf("DecodeFaultTotal() = %d; want 0", got)
	}
}
