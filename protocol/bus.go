package protocol

import (
	"context"
	"sync"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

const defaultQueueCapacity = 64

// Bus orchestrates prioritized frame sending.
type Bus struct {
	queueMu sync.Mutex
	queue   *priorityQueue
	notify  chan struct{}
	outCap  int
	outChan chan Frame
	closed  bool
}

// NewBus initializes a Bus with a configurable queue capacity.
func NewBus(queueCapacity int) *Bus {
	if queueCapacity <= 0 {
		queueCapacity = defaultQueueCapacity
	}

	return &Bus{
		queue:  newPriorityQueue(),
		notify: make(chan struct{}, 1), // capacity 1 to coalesce wake-ups.
		outCap: queueCapacity,
	}
}

// Send enqueues a frame for prioritized sending.
func (b *Bus) Send(ctx context.Context, frame Frame) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	b.queueMu.Lock()
	if b.closed {
		b.queueMu.Unlock()
		return ebuserrors.ErrTransportClosed
	}
	b.queue.push(frame)
	b.queueMu.Unlock()

	select {
	case b.notify <- struct{}{}:
	default:
	}
	return nil
}

// Run starts the queue draining loop and returns an outbound channel of frames.
func (b *Bus) Run(ctx context.Context) <-chan Frame {
	b.queueMu.Lock()
	if b.outChan != nil {
		out := b.outChan
		b.queueMu.Unlock()
		return out
	}
	out := make(chan Frame, b.outCap) // buffered to decouple sender and consumer.
	b.outChan = out
	b.queueMu.Unlock()

	// Goroutine exits when ctx.Done() is closed; closes outbound channel.
	go b.runLoop(ctx, out)
	return out
}

func (b *Bus) runLoop(ctx context.Context, out chan Frame) {
	defer close(out)

	for {
		b.drain(ctx, out)

		select {
		case <-ctx.Done():
			b.markClosed()
			return
		case <-b.notify:
		}
	}
}

func (b *Bus) drain(ctx context.Context, out chan Frame) {
	for {
		frame, ok := b.dequeue()
		if !ok {
			return
		}

		select {
		case out <- frame:
		case <-ctx.Done():
			b.markClosed()
			return
		}
	}
}

func (b *Bus) dequeue() (Frame, bool) {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()

	return b.queue.pop()
}

func (b *Bus) markClosed() {
	b.queueMu.Lock()
	b.closed = true
	b.queueMu.Unlock()
}
