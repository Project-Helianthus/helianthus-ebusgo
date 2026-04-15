package protocol

import (
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

func TestPriorityQueue_OrderBySource(t *testing.T) {
	t.Parallel()

	pq := newPriorityQueue()
	pq.push(&busRequest{frame: Frame{Source: 0x30, Primary: 0x01}})
	pq.push(&busRequest{frame: Frame{Source: 0x08, Primary: 0x02}})
	pq.push(&busRequest{frame: Frame{Source: 0x10, Primary: 0x03}})

	want := []byte{0x08, 0x10, 0x30}
	for i, expected := range want {
		request, ok := pq.pop()
		if !ok {
			t.Fatalf("pop[%d] missing frame", i)
		}
		if request.frame.Source != expected {
			t.Fatalf("pop[%d] source = 0x%02x; want 0x%02x", i, request.frame.Source, expected)
		}
	}
}

func TestPriorityQueue_FIFOForSameSource(t *testing.T) {
	t.Parallel()

	pq := newPriorityQueue()
	pq.push(&busRequest{frame: Frame{Source: 0x10, Primary: 0x01}})
	pq.push(&busRequest{frame: Frame{Source: 0x10, Primary: 0x02}})
	pq.push(&busRequest{frame: Frame{Source: 0x10, Primary: 0x03}})

	for i, expected := range []byte{0x01, 0x02, 0x03} {
		request, ok := pq.pop()
		if !ok {
			t.Fatalf("pop[%d] missing frame", i)
		}
		if request.frame.Primary != expected {
			t.Fatalf("pop[%d] primary = 0x%02x; want 0x%02x", i, request.frame.Primary, expected)
		}
	}
}

func TestPriorityQueue_TransportOpPriority(t *testing.T) {
	t.Parallel()

	pq := newPriorityQueue()

	// Transport op has zero-valued frame (Source=0x00), but should get
	// mid-range priority (0x80) instead of highest (0x00).
	pq.push(&busRequest{
		transportOp: func(transport.RawTransport) error { return nil },
	})
	// Regular frame with Source=0x10 (lower than 0x80).
	pq.push(&busRequest{frame: Frame{Source: 0x10, Primary: 0xAA}})

	// Source 0x10 should come first (lower priority value = higher priority).
	first, ok := pq.pop()
	if !ok {
		t.Fatal("pop[0] missing")
	}
	if first.frame.Primary != 0xAA {
		t.Fatalf("pop[0] expected regular frame (Primary=0xAA), got Primary=0x%02x", first.frame.Primary)
	}
	if first.transportOp != nil {
		t.Fatal("pop[0] expected regular frame, got transport op")
	}

	second, ok := pq.pop()
	if !ok {
		t.Fatal("pop[1] missing")
	}
	if second.transportOp == nil {
		t.Fatal("pop[1] expected transport op, got regular frame")
	}
}

func TestPriorityQueue_StarvationPromotion(t *testing.T) {
	t.Parallel()

	pq := newPriorityQueue()

	// Push a low-priority request first.
	pq.push(&busRequest{frame: Frame{Source: 0xFF, Primary: 0xBB}})

	// Pop it partially through starvation cycles by pushing and popping
	// high-priority requests. Each pop() increments age on remaining items.
	for i := 0; i < starvationThreshold; i++ {
		pq.push(&busRequest{frame: Frame{Source: 0x01, Primary: byte(i)}})
		got, ok := pq.pop()
		if !ok {
			t.Fatalf("pop[%d] missing", i)
		}
		// High-priority (Source=0x01) should always win until starvation kicks in.
		if got.frame.Source == 0xFF {
			// The low-priority item was promoted early — that is acceptable
			// as long as it happens at or after threshold. Since age starts
			// at 0 and increments each pop, promotion happens at pop number
			// starvationThreshold (0-indexed: the 8th pop).
			if i < starvationThreshold-1 {
				t.Fatalf("low-priority item promoted too early at pop %d", i)
			}
			return // Promoted — success
		}
	}

	// After starvationThreshold pops, the low-priority item should have been
	// promoted. It should now be at priority 0 and dequeue next.
	pq.push(&busRequest{frame: Frame{Source: 0x01, Primary: 0xCC}})
	got, ok := pq.pop()
	if !ok {
		t.Fatal("final pop missing")
	}
	if got.frame.Primary != 0xBB {
		t.Fatalf("expected starved item (Primary=0xBB), got Primary=0x%02x Source=0x%02x", got.frame.Primary, got.frame.Source)
	}
}
