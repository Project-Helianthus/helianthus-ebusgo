package protocol

import "container/heap"

// starvationThreshold is the number of pop operations after which a queued
// item is promoted to highest priority. This prevents indefinite starvation
// of low-priority requests when higher-priority ones keep arriving.
const starvationThreshold = 8

// transportOpPriority is the fixed priority assigned to raw transport
// operations (RawTransportOp). Without this, transport ops get priority 0x00
// (from zero-valued frame.Source) which incorrectly makes them highest
// priority. 0x80 places them at mid-range.
const transportOpPriority = 0x80

type queueItem struct {
	request  *busRequest
	priority byte
	seq      uint64
	age      int
	index    int
}

type priorityQueue struct {
	items []*queueItem
	seq   uint64
}

func newPriorityQueue() *priorityQueue {
	pq := &priorityQueue{}
	heap.Init(pq)
	return pq
}

func (q *priorityQueue) Len() int {
	return len(q.items)
}

func (q *priorityQueue) Less(i, j int) bool {
	if q.items[i].priority == q.items[j].priority {
		return q.items[i].seq < q.items[j].seq
	}
	return q.items[i].priority < q.items[j].priority
}

func (q *priorityQueue) Swap(i, j int) {
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.items[i].index = i
	q.items[j].index = j
}

func (q *priorityQueue) Push(x any) {
	item := x.(*queueItem)
	item.index = len(q.items)
	q.items = append(q.items, item)
}

func (q *priorityQueue) Pop() any {
	n := len(q.items)
	item := q.items[n-1]
	q.items[n-1] = nil
	item.index = -1
	q.items = q.items[:n-1]
	return item
}

func (q *priorityQueue) push(request *busRequest) {
	priority := request.frame.Source
	if request.transportOp != nil {
		priority = transportOpPriority
	}
	item := &queueItem{
		request:  request,
		priority: priority,
		seq:      q.seq,
	}
	q.seq++
	heap.Push(q, item)
}

func (q *priorityQueue) pop() (*busRequest, bool) {
	if q.Len() == 0 {
		return nil, false
	}
	// Age remaining items before popping. Two-phase approach: first
	// increment ages and collect items to promote, then apply promotions
	// with heap.Fix. This avoids mutating the heap during iteration
	// (heap.Fix calls Swap which reorders q.items).
	var promoted []*queueItem
	for _, item := range q.items {
		item.age++
		if item.age >= starvationThreshold && item.priority > 0 {
			promoted = append(promoted, item)
		}
	}
	for _, item := range promoted {
		item.priority = 0
		heap.Fix(q, item.index)
	}
	item := heap.Pop(q).(*queueItem)
	return item.request, true
}
