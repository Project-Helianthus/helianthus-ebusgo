package transport_test

import (
	"net"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// F-XX (batch-22 Attack 2 instrumentation, 2026-05-15) tests for
// PostGrantWindowExpiredCount.
//
// The postGrantPreEcho window opens on ENH_RES_STARTED and MUST close
// by one of three paths:
//   a) first non-SYN echo arrival (normal — no counter increment)
//   b) deadline expiry (Attack 2 counter increments)
//   c) lifecycle reset (no counter increment)
//
// The production deadline was widened to 5s in batch-24 round-5 to
// cover the entire gateway transaction duration; deadline-expiry
// tests in this file override it down to 50 ms via
// SetPostGrantPreEchoTimeoutForTest so they can assert the expiry
// branch without sleeping 5s+ each.
//
// The expiry-path counter is forensic — it measures how often the
// window closes via timeout rather than a real echo, which correlates
// with the leak surface for post-window-close idle 0xAA SYNs.

// TestPostGrantWindowExpired_NotIncrementedOnFirstEcho verifies the
// negative invariant: when a real non-SYN echo arrives BEFORE the
// 50 ms deadline, the counter MUST NOT increment.
func TestPostGrantWindowExpired_NotIncrementedOnFirstEcho(t *testing.T) {
	// NOT t.Parallel: serializes with the deadline-expiry override
	// tests in this file, which mutate postGrantPreEchoTimeout
	// package-globally. This test runs against the production 5 s
	// value (real echo arrives in <10 ms, well under either value),
	// but a concurrent override would still confuse a future reader.

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 2*time.Second, 2*time.Second)

	before := enh.PostGrantWindowExpiredCount()

	// Open the postGrantPreEcho window via a RequestStart-style flow:
	// fire async RequestStart; drain the REQ_START write so the pipe
	// does not block; send STARTED for the matching initiator; then
	// immediately deliver a real (non-SYN) echo.
	requestDone := make(chan struct{})
	go func() {
		_ = enh.RequestStart(0x31)
		close(requestDone)
	}()
	drainServerWrite(t, server, 2)

	// Drive STARTED first via ReadEvent so the postGrantPreEcho
	// window opens before we feed the first echo. This guarantees
	// ordering — without it, net.Pipe's lack of cross-goroutine
	// ordering between STARTED and the echo write could let the
	// test pass even when it never exercised the close path
	// (Codex review of round-3).
	startedSeq := transport.EncodeENH(transport.ENHResStarted, 0x31)
	go func() { _, _ = server.Write(startedSeq[:]) }()
	startedDeadline := time.Now().Add(1 * time.Second)
	startedSeen := false
	for !startedSeen && time.Now().Before(startedDeadline) {
		ev, err := enh.ReadEvent()
		if err != nil {
			t.Fatalf("ReadEvent: %v", err)
		}
		if ev.Kind == transport.StreamEventStarted {
			startedSeen = true
		}
	}
	if !startedSeen {
		t.Fatal("did not observe STARTED event — postGrantPreEcho window never opened")
	}

	// Window is now open. Feed a real non-SYN echo well within the
	// 50 ms deadline (the test runs in <10 ms typically). The
	// non-SYN echo branch closes the window via the first-echo path,
	// NOT the expiry path — counter MUST stay at baseline.
	feedENHReceivedBytes(t, server, []byte{0x55})
	deadline := time.Now().Add(1 * time.Second)
	echoSeen := false
	for !echoSeen && time.Now().Before(deadline) {
		b, _, err := enh.ReadByteWithEscape()
		if err != nil {
			t.Fatalf("ReadByteWithEscape: %v", err)
		}
		if b == 0x55 {
			echoSeen = true
		}
	}
	if !echoSeen {
		t.Fatal("did not observe the 0x55 echo — window-close path was not exercised")
	}

	if got := enh.PostGrantWindowExpiredCount(); got != before {
		t.Fatalf("PostGrantWindowExpiredCount = %d, want %d (no expiry on first-echo close path)", got, before)
	}
}

// TestPostGrantWindowExpired_IncrementedOnDeadline verifies the
// positive invariant: when no non-SYN echo arrives within the
// postGrantPreEcho deadline after the window opens, the next byte
// arrival triggers the expiry path and increments the counter.
//
// The test overrides postGrantPreEchoTimeout to 50 ms (production
// value is 5 s) so the 80 ms sleep below provably exceeds it without
// adding 5 s+ of real wall time to the suite.
//
// Sequencing:
//   1. RequestStart writes REQ_START, awaitingStart=true.
//   2. Async-write STARTED on the wire.
//   3. ReadEvent — this drives fillPendingLocked which processes
//      STARTED (opens postGrantPreEcho window) and returns.
//   4. Sleep > 50 ms (overridden postGrantPreEchoTimeout).
//   5. Async-write a non-SYN byte.
//   6. ReadByteWithEscape — fillPendingLocked sees the byte, observes
//      postGrantPreEchoExpired()=true, closes window, increments
//      the expiry counter.
func TestPostGrantWindowExpired_IncrementedOnDeadline(t *testing.T) {
	// NOT t.Parallel: overrides postGrantPreEchoTimeout via a package-
	// global mutation; running concurrently with
	// TestPostGrantPreEchoTimeout_CoversTransactionDuration would race
	// the constant-value assertion. See SetPostGrantPreEchoTimeoutForTest.
	defer transport.SetPostGrantPreEchoTimeoutForTest(50 * time.Millisecond)()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 2*time.Second, 2*time.Second)

	before := enh.PostGrantWindowExpiredCount()

	// Step 1: RequestStart.
	go func() { _ = enh.RequestStart(0x31) }()
	drainServerWrite(t, server, 2)

	// Step 2: async-write STARTED.
	startedSeq := transport.EncodeENH(transport.ENHResStarted, 0x31)
	go func() { _, _ = server.Write(startedSeq[:]) }()

	// Step 3: drain ReadEvent until we see STARTED — this drives
	// fillPendingLocked through openPostGrantPreEchoWindow.
	startedDeadline := time.Now().Add(1 * time.Second)
	startedSeen := false
	for !startedSeen && time.Now().Before(startedDeadline) {
		ev, err := enh.ReadEvent()
		if err != nil {
			t.Fatalf("ReadEvent: %v", err)
		}
		if ev.Kind == transport.StreamEventStarted {
			startedSeen = true
		}
	}
	if !startedSeen {
		t.Fatal("did not observe STARTED event — window never opened")
	}

	// Step 4: wait past the 50 ms (overridden) postGrantPreEchoTimeout.
	time.Sleep(80 * time.Millisecond)

	// Step 5: async-write a real byte. The next read will drive
	// fillPendingLocked into the expiry branch.
	go func() {
		echoSeq := transport.EncodeENH(transport.ENHResReceived, 0x55)
		_, _ = server.Write(echoSeq[:])
	}()

	// Step 6: read; expiry path increments the counter.
	drainDeadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(drainDeadline) {
		b, _, err := enh.ReadByteWithEscape()
		if err == nil && b == 0x55 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Tight invariant per Codex review: exactly one expiry window
	// was opened and closed via deadline, so the counter must
	// advance by EXACTLY one. `got > before` would mask an
	// accidental double-increment for the same expired window.
	if got := enh.PostGrantWindowExpiredCount(); got != before+1 {
		t.Fatalf("PostGrantWindowExpiredCount = %d (was %d), want %d (exactly one expiry per window)", got, before, before+1)
	}
}
