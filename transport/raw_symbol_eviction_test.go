package transport

import (
	"net"
	"testing"
	"time"
)

// Issue helianthus-ebusgateway#1003: under raw capture a control event at
// the queue bound evicts the oldest raw item together with the logical item
// it completed, so a later reader never pairs a raw symbol with an unrelated
// logical item.
func TestAppendControlEventLocked_RawCaptureEvictsPairAsUnit(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	tr := NewENHTransport(client, time.Second, time.Second)

	tr.readMu.Lock()
	tr.rawSymbolItems = true
	limit := tr.pendingCapLocked()
	if limit != 2*maxPendingEvents {
		tr.readMu.Unlock()
		t.Fatalf("raw-capture bound = %d; want %d", limit, 2*maxPendingEvents)
	}
	for i := 0; len(tr.pendingEvents) < limit; i++ {
		symbol := byte(i % 0x80)
		tr.queueObservedSymbolLocked(symbol, StreamEvent{Kind: StreamEventByte, Byte: symbol}, true)
	}
	tr.appendControlEventLocked(StreamEvent{Kind: StreamEventStarted, Data: 0x31})
	if got := len(tr.pendingEvents); got != limit-1 {
		tr.readMu.Unlock()
		t.Fatalf("queue length after eviction = %d; want %d (one raw+logical pair removed, control appended)", got, limit-1)
	}
	tr.readMu.Unlock()

	for i := 1; i < limit/2; i++ {
		ev, symbol, hasSymbol, err := tr.ReadEventWithRawSymbol()
		if err != nil {
			t.Fatalf("ReadEventWithRawSymbol item %d: %v", i, err)
		}
		want := byte(i % 0x80)
		if !hasSymbol || symbol != want || ev.Kind != StreamEventByte || ev.Byte != want {
			t.Fatalf("item %d = {symbol=0x%02X has=%v ev=%+v}; want aligned pair for 0x%02X", i, symbol, hasSymbol, ev, want)
		}
	}
	ev, _, hasSymbol, err := tr.ReadEventWithRawSymbol()
	if err != nil || hasSymbol || ev.Kind != StreamEventStarted || ev.Data != 0x31 {
		t.Fatalf("tail = {ev=%+v has=%v err=%v}; want STARTED 0x31", ev, hasSymbol, err)
	}
}

// Without raw capture the observed-symbol helper is exactly the previous
// logical emission: no raw items, the original bound, nothing queued for a
// symbol that completed no logical item.
func TestQueueObservedSymbolLocked_LogicalOnlyWithoutRawCapture(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	tr := NewENHTransport(client, time.Second, time.Second)

	tr.readMu.Lock()
	defer tr.readMu.Unlock()
	tr.queueObservedSymbolLocked(0xA9, StreamEvent{}, false)
	if len(tr.pendingEvents) != 0 {
		t.Fatalf("lead without raw capture queued %d items; want 0", len(tr.pendingEvents))
	}
	for i := 0; i < maxPendingEvents+4; i++ {
		tr.queueObservedSymbolLocked(0x10, StreamEvent{Kind: StreamEventByte, Byte: 0x10}, true)
	}
	if got := len(tr.pendingEvents); got != maxPendingEvents {
		t.Fatalf("logical-only queue length = %d; want bound %d", got, maxPendingEvents)
	}
	for i, ev := range tr.pendingEvents {
		if ev.Kind != StreamEventByte {
			t.Fatalf("item %d kind = %d; want only logical bytes", i, ev.Kind)
		}
	}
}

// An escape lead is accepted under raw capture only when its suffix can
// still be queued, so a lead is never delivered without its completion.
func TestQueueObservedSymbolLocked_LeadReservesSuffixCapacity(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	tr := NewENHTransport(client, time.Second, time.Second)

	tr.readMu.Lock()
	defer tr.readMu.Unlock()
	tr.rawSymbolItems = true
	limit := tr.pendingCapLocked()
	for len(tr.pendingEvents) < limit-1 {
		tr.queueObservedSymbolLocked(0x10, StreamEvent{Kind: StreamEventByte, Byte: 0x10}, true)
	}
	tr.appendControlEventLocked(StreamEvent{Kind: StreamEventStarted})
	if len(tr.pendingEvents) != limit-1 {
		// Force an odd fill: one free slot below the bound.
		t.Fatalf("setup length = %d; want %d", len(tr.pendingEvents), limit-1)
	}
	before := len(tr.pendingEvents)
	tr.queueObservedSymbolLocked(0xA9, StreamEvent{}, false)
	if got := len(tr.pendingEvents); got != before {
		t.Fatalf("lead accepted with one free slot: length %d -> %d; want dropped", before, got)
	}
}
