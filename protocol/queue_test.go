package protocol

import "testing"

func TestPriorityQueue_OrderBySource(t *testing.T) {
	t.Parallel()

	pq := newPriorityQueue()
	pq.push(Frame{Source: 0x30, Primary: 0x01})
	pq.push(Frame{Source: 0x08, Primary: 0x02})
	pq.push(Frame{Source: 0x10, Primary: 0x03})

	want := []byte{0x08, 0x10, 0x30}
	for i, expected := range want {
		frame, ok := pq.pop()
		if !ok {
			t.Fatalf("pop[%d] missing frame", i)
		}
		if frame.Source != expected {
			t.Fatalf("pop[%d] source = 0x%02x; want 0x%02x", i, frame.Source, expected)
		}
	}
}

func TestPriorityQueue_FIFOForSameSource(t *testing.T) {
	t.Parallel()

	pq := newPriorityQueue()
	pq.push(Frame{Source: 0x10, Primary: 0x01})
	pq.push(Frame{Source: 0x10, Primary: 0x02})
	pq.push(Frame{Source: 0x10, Primary: 0x03})

	for i, expected := range []byte{0x01, 0x02, 0x03} {
		frame, ok := pq.pop()
		if !ok {
			t.Fatalf("pop[%d] missing frame", i)
		}
		if frame.Primary != expected {
			t.Fatalf("pop[%d] primary = 0x%02x; want 0x%02x", i, frame.Primary, expected)
		}
	}
}
