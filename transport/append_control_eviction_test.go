package transport

import (
	"net"
	"testing"
	"time"
)

// PR #155 P2 (Codex review, 2026-05-17): appendControlEventLocked
// previously only evicted StreamEventByte under cap pressure,
// falling back to dropping the oldest control event when no byte
// entry was found. After the F-38-fix introduction of
// StreamEventWireSyn (a non-Byte, explicitly lossy, capped backlog
// kind), a SYN flood during the awaitingStart window could fill
// pendingEvents alongside an earlier STARTED/FAILED — and the next
// real control event would evict that boundary instead of a
// passive WireSyn marker. The fix promotes StreamEventWireSyn into
// the evictable class.
//
// This test is in the `transport` package (not `transport_test`)
// so it can call the unexported helpers directly and drive the
// queue into the exact at-cap state needed without flooding a
// net.Pipe-backed transport for thousands of bytes.

// TestAppendControlEventLocked_EvictsWireSynBeforeControl verifies
// that under cap pressure, a passive WireSyn is dropped to make
// room — NOT the existing STARTED control event.
func TestAppendControlEventLocked_EvictsWireSynBeforeControl(t *testing.T) {
	// Local net.Pipe is only needed because NewENHTransport requires
	// a net.Conn; we never read/write through it in this unit test.
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	tr := NewENHTransport(client, time.Second, time.Second)

	// Drive readMu acquisition (the helpers require it). Use a
	// fresh goroutine with proper lock release pattern.
	tr.readMu.Lock()

	// Pre-fill the queue: 1 STARTED control event, rest WireSyn,
	// total = maxPendingEvents (at cap).
	tr.pendingEvents = make([]StreamEvent, 0, maxPendingEvents)
	tr.pendingEvents = append(tr.pendingEvents, StreamEvent{
		Kind: StreamEventStarted,
		Data: 0x31,
	})
	for len(tr.pendingEvents) < maxPendingEvents {
		tr.pendingEvents = append(tr.pendingEvents, StreamEvent{
			Kind: StreamEventWireSyn,
			Byte: 0xAA,
		})
	}
	if len(tr.pendingEvents) != maxPendingEvents {
		tr.readMu.Unlock()
		t.Fatalf("setup: len=%d, want maxPendingEvents=%d", len(tr.pendingEvents), maxPendingEvents)
	}

	// Append another control event (FAILED). Eviction must remove a
	// WireSyn — NOT the earlier STARTED.
	tr.appendControlEventLocked(StreamEvent{
		Kind: StreamEventFailed,
		Data: 0x10,
	})

	// Verify STARTED at head is preserved.
	if tr.pendingEvents[0].Kind != StreamEventStarted {
		tr.readMu.Unlock()
		t.Fatalf("STARTED head was evicted; got Kind=%d at head — PR #155 P2 fix not applied", tr.pendingEvents[0].Kind)
	}
	// Verify FAILED is now at tail.
	tail := tr.pendingEvents[len(tr.pendingEvents)-1]
	if tail.Kind != StreamEventFailed || tail.Data != 0x10 {
		tr.readMu.Unlock()
		t.Fatalf("FAILED not appended at tail; got Kind=%d Data=0x%02X", tail.Kind, tail.Data)
	}
	// Verify queue length is still at cap (one WireSyn evicted, one
	// FAILED appended).
	if len(tr.pendingEvents) != maxPendingEvents {
		tr.readMu.Unlock()
		t.Fatalf("queue length drift; got %d, want %d", len(tr.pendingEvents), maxPendingEvents)
	}
	// Verify the count of WireSyn entries decreased by exactly one.
	wireSynCount := 0
	for _, ev := range tr.pendingEvents {
		if ev.Kind == StreamEventWireSyn {
			wireSynCount++
		}
	}
	expected := maxPendingEvents - 2 // -1 for STARTED head, -1 for FAILED tail
	if wireSynCount != expected {
		tr.readMu.Unlock()
		t.Fatalf("WireSyn count = %d, want %d (one WireSyn should have been evicted, not the STARTED)", wireSynCount, expected)
	}

	tr.readMu.Unlock()
}

// TestAppendControlEventLocked_PrefersByteOverWireSyn verifies the
// secondary invariant: when BOTH byte events and WireSyn events are
// present, byte events are evicted first (preserves the existing
// pre-PR-#155-P2 priority — actual data bytes are recoverable from
// re-reading bus state, whereas WireSyn markers are bus-idle
// signals downstream consumers may already have observed via other
// means).
func TestAppendControlEventLocked_PrefersByteOverWireSyn(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	tr := NewENHTransport(client, time.Second, time.Second)

	tr.readMu.Lock()
	tr.pendingEvents = make([]StreamEvent, 0, maxPendingEvents)
	// Alternate: STARTED, Byte, WireSyn, WireSyn, ... up to cap.
	tr.pendingEvents = append(tr.pendingEvents,
		StreamEvent{Kind: StreamEventStarted, Data: 0x31},
		StreamEvent{Kind: StreamEventByte, Byte: 0x55},
	)
	for len(tr.pendingEvents) < maxPendingEvents {
		tr.pendingEvents = append(tr.pendingEvents, StreamEvent{
			Kind: StreamEventWireSyn,
			Byte: 0xAA,
		})
	}

	tr.appendControlEventLocked(StreamEvent{
		Kind: StreamEventFailed,
		Data: 0x10,
	})

	// Byte 0x55 should be evicted (it appears earlier in scan order
	// than the WireSyns).
	for _, ev := range tr.pendingEvents {
		if ev.Kind == StreamEventByte && ev.Byte == 0x55 {
			tr.readMu.Unlock()
			t.Fatal("StreamEventByte 0x55 was not evicted; eviction order changed — byte should be preferred over WireSyn")
		}
	}
	tr.readMu.Unlock()
}
