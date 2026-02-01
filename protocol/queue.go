package protocol

import "container/heap"

type queueItem struct {
	frame    Frame
	priority byte
	seq      uint64
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

func (q *priorityQueue) push(frame Frame) {
	item := &queueItem{
		frame:    frame,
		priority: frame.Source,
		seq:      q.seq,
	}
	q.seq++
	heap.Push(q, item)
}

func (q *priorityQueue) pop() (Frame, bool) {
	if q.Len() == 0 {
		return Frame{}, false
	}
	item := heap.Pop(q).(*queueItem)
	return item.frame, true
}
