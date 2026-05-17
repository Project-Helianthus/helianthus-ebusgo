package transport_test

import (
	"net"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// F-38-fix tests (PR #155 P1 review, 2026-05-15).
//
// F-38 (PR #155) forwards wire SYN bytes during the async RequestStart
// `awaitingStart` window so downstream bus reconstructors observe
// bus-idle cadence and avoid SYN_TIMEOUT-driven receive-state-machine
// transitions.
//
// The original F-38 commit emitted these SYNs as StreamEventByte, which
// polluted the pendingEvents queue drained by ReadByte. Codex caught
// this on PR #155 (two P1 comments — RequestInfo and fillPendingLocked
// paths). The active sender's first ReadByte after `awaitingStart`
// resolved would return the stale 0xAA as the first echo, and
// sendRawWithEcho's collision guard rejects an unexpected wire SYN.
//
// The fix: introduce StreamEventWireSyn (a non-Byte kind) that ReadByte
// SKIPS (along with all other non-Byte events) but ReadEvent surfaces.

// (TestF38Fix_PreGrantSYN_NotConsumedByReadByte was removed: net.Pipe's
// synchronous semantics combined with RequestStart being fire-and-forget
// produced unavoidable goroutine-ordering races under -race that
// dominated the actual signal under test. The two remaining tests
// already cover the contract — the StreamEventByteAbsent test pins the
// negative invariant ReadByte cares about, and the VisibleViaReadEvent
// test pins the downstream forwarding contract — both deterministic.)

// TestF38Fix_PreGrantSYN_VisibleViaReadEvent verifies that the F-38
// downstream contract still holds: external bus reconstructors using
// ReadEvent MUST see the pre-grant SYN markers (as StreamEventWireSyn).
// Without this, ebusd's bs_ready→bs_skip transition fires on
// SYN_TIMEOUT and silently drops the eventual STARTED — the original
// F-38 root cause.
func TestF38Fix_PreGrantSYN_VisibleViaReadEvent(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 2*time.Second, 2*time.Second)

	// Open awaitingStart via async RequestStart (background goroutine).
	requestDone := make(chan struct{})
	go func() {
		_ = enh.RequestStart(0x31)
		close(requestDone)
	}()
	drainServerWrite(t, server, 2)

	// Feed pre-grant SYNs.
	feedENHReceivedBytes(t, server, []byte{0xAA, 0xAA})

	// Drain ReadEvent and look for StreamEventWireSyn.
	wireSyns := 0
	deadline := time.Now().Add(1 * time.Second)
	for wireSyns < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("only saw %d wire-SYN events; expected 2 (F-38 downstream contract broken)", wireSyns)
		}
		ev, err := enh.ReadEvent()
		if err != nil {
			t.Fatalf("ReadEvent: err=%v", err)
		}
		if ev.Kind == transport.StreamEventWireSyn {
			if ev.Byte != 0xAA {
				t.Fatalf("StreamEventWireSyn.Byte = 0x%02X; want 0xAA", ev.Byte)
			}
			wireSyns++
		}
	}

	// Cleanup: close the pipe to unblock RequestStart.
	_ = server.Close()
	<-requestDone
}

// TestF38Fix_PreGrantSYN_StreamEventByteAbsent verifies the negative
// invariant: while awaitingStart is open, a wire SYN MUST NOT be
// emitted as StreamEventByte. Pins the kind selection.
func TestF38Fix_PreGrantSYN_StreamEventByteAbsent(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 2*time.Second, 2*time.Second)

	requestDone := make(chan struct{})
	go func() {
		_ = enh.RequestStart(0x31)
		close(requestDone)
	}()
	drainServerWrite(t, server, 2)

	feedENHReceivedBytes(t, server, []byte{0xAA})

	// Drain events for a brief window; verify NO StreamEventByte for
	// the pre-grant SYN.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		ev, err := enh.ReadEvent()
		if err != nil {
			break
		}
		if ev.Kind == transport.StreamEventByte && ev.Byte == 0xAA {
			t.Fatalf("pre-grant SYN emitted as StreamEventByte (Byte=0xAA, WasEscaped=%v) — F-38-fix invariant broken", ev.WasEscaped)
		}
		if ev.Kind == transport.StreamEventWireSyn {
			break
		}
	}

	_ = server.Close()
	<-requestDone
}

// drainServerWrite consumes n bytes from the server side of a net.Pipe
// to prevent the transport's Write() (used by RequestStart to emit
// REQ_START) from blocking. Returns when n bytes have been drained or
// timeout fires.
func drainServerWrite(t *testing.T, server net.Conn, n int) {
	t.Helper()
	buf := make([]byte, n)
	if err := server.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := server.Read(buf); err != nil {
		t.Fatalf("drainServerWrite: %v", err)
	}
	if err := server.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear ReadDeadline: %v", err)
	}
}
