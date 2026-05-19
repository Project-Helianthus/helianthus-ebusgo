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

// TestENHCounters_WallClockTimeout pins that
// AdminEventEscapePendingTimeout (0xA9 followed by a byte
// >EscapePendingTimeout later) increments
// EscapePendingTimeoutTotal AND emits the current byte (data-
// preserving recovery — re-processed in NORMAL state). The
// DecodeFaultTotal counter MUST stay at zero (timeout is not a
// drop-everything fault).
//
// Test sync model: net.Pipe is synchronous (Write blocks until Read
// consumes). The pacedWriteGoroutine uses this property to interleave
// real wall-clock sleeps BETWEEN byte writes — when the goroutine's
// first Write returns, we know the transport has already fed the
// 0xA9 to the decoder (entering pending state); the sleep then
// counts toward the 32 ms cap; the second Write feeds the 0x55,
// which sees elapsed > EscapePendingTimeout and fires the cap.
func TestENHCounters_WallClockTimeout(t *testing.T) {
	t.Parallel()

	// Read timeout MUST exceed the 32 ms cap + scheduling jitter so
	// the transport's net.Conn.Read deadline doesn't fire while we
	// are deliberately stalling the wire.
	const readTimeout = 2 * time.Second

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, readTimeout, readTimeout)

	// Encode each byte upfront.
	leadFrame := encodeENHWireByte(0xA9)
	tailFrame := encodeENHWireByte(0x55)

	// Paced writer: write the lead, wait for the transport to read
	// it (net.Pipe Write returns when the read completes), sleep
	// past the cap, then write the trailing byte.
	go func() {
		_, _ = server.Write(leadFrame)
		// Lead is now in the decoder's pending state. Sleep past
		// EscapePendingTimeout (32 ms) + a small jitter margin.
		time.Sleep(transport.EscapePendingTimeout + 10*time.Millisecond)
		_, _ = server.Write(tailFrame)
	}()

	events := drainBytes(t, enh, 1)
	if events[0].Byte != 0x55 || events[0].WasEscaped {
		t.Fatalf("post-timeout event = {Byte=0x%02X WasEscaped=%v}; want {0x55 false} (data-preserving recovery)",
			events[0].Byte, events[0].WasEscaped)
	}

	if got := enh.EscapePendingTimeoutTotal(); got != 1 {
		t.Errorf("EscapePendingTimeoutTotal() = %d; want 1", got)
	}
	// Timeout is data-preserving, NOT a drop-everything fault.
	if got := enh.DecodeFaultTotal(); got != 0 {
		t.Errorf("DecodeFaultTotal() = %d; want 0 (timeout is data-preserving, not a fault)", got)
	}
	if got := enh.EscapeRecoveryTotal(); got != 0 {
		t.Errorf("EscapeRecoveryTotal() = %d; want 0", got)
	}
	if got := enh.EscapeBudgetExhaustedTotal(); got != 0 {
		t.Errorf("EscapeBudgetExhaustedTotal() = %d; want 0", got)
	}
}

// encodeENHWireByte returns the 2-byte ENHResReceived encoding of
// `wire`. Local helper for the paced-write test pattern.
func encodeENHWireByte(wire byte) []byte {
	seq := transport.EncodeENH(transport.ENHResReceived, wire)
	return []byte{seq[0], seq[1]}
}

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
