//go:build !tinygo

package transport

import (
	"net"
	"testing"
	"time"
)

// TestPostGrantPreEchoTimeout_CoversTransactionDuration verifies that
// the postGrantPreEcho window (round-5 batch-24 widening) remains open
// well beyond the previous 50ms bound. This is the falsifiability gate
// for the round-5 change: if a future regression reverts the constant
// to 50ms, this test fails.
//
// Setup:
//  1. Construct an ENHTransport over a no-op pipe.
//  2. Open the window via openPostGrantPreEchoWindow.
//  3. Sleep 100ms (twice the OLD 50ms timeout).
//  4. Assert postGrantPreEchoExpired() == false — the window is still open.
//
// The test deliberately stops well short of 5s so it stays fast (CI-cheap).
// A 100ms sleep is long enough to refute the old 50ms timeout while short
// enough not to slow the suite.
func TestPostGrantPreEchoTimeout_CoversTransactionDuration(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := NewENHTransport(client, 2*time.Second, 2*time.Second)

	enh.openPostGrantPreEchoWindow()
	if !enh.postGrantPreEcho.Load() {
		t.Fatal("openPostGrantPreEchoWindow did not set the flag")
	}

	// Sleep 100ms — well past the old 50ms timeout, far short of the
	// new 5s timeout.
	time.Sleep(100 * time.Millisecond)

	if enh.postGrantPreEchoExpired() {
		t.Fatalf("postGrantPreEchoExpired() returned true after 100ms; "+
			"expected false because batch-24 round-5 widened the "+
			"timeout to cover the full transaction duration "+
			"(constant=%s)", postGrantPreEchoTimeout)
	}
	if got, want := postGrantPreEchoTimeout, 5*time.Second; got != want {
		t.Fatalf("postGrantPreEchoTimeout = %s, want %s "+
			"(batch-24 round-5)", got, want)
	}
}
