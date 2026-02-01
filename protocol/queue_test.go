package protocol

import "testing"

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
