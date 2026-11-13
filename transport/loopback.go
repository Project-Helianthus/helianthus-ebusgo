package transport

import (
	"sync"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// Loopback is an in-memory RawTransport for tests and simulations.
type Loopback struct {
	mutex   sync.Mutex
	cond    *sync.Cond
	closed  bool
	buffer  []byte
	readPos int
}

// NewLoopback returns a loopback transport instance.
func NewLoopback() *Loopback {
	lb := &Loopback{}
	lb.cond = sync.NewCond(&lb.mutex)
	return lb
}

// ReadByte returns the oldest unread written byte, blocking until one is
// written or the loopback is closed. Bytes written before Close stay readable;
// once they are drained it returns ErrTransportClosed (unwrapped).
func (lb *Loopback) ReadByte() (byte, error) {
	lb.mutex.Lock()
	defer lb.mutex.Unlock()

	for lb.readPos >= len(lb.buffer) && !lb.closed {
		lb.cond.Wait()
	}

	if lb.readPos >= len(lb.buffer) {
		return 0, ebuserrors.ErrTransportClosed
	}

	value := lb.buffer[lb.readPos]
	lb.readPos++
	if lb.readPos == len(lb.buffer) {
		lb.buffer = lb.buffer[:0]
		lb.readPos = 0
	}
	return value, nil
}

// Write appends a copy of payload to the read queue and wakes blocked readers.
// It does not wait for a reader and returns ErrTransportClosed after Close.
func (lb *Loopback) Write(payload []byte) (int, error) {
	lb.mutex.Lock()
	defer lb.mutex.Unlock()

	if lb.closed {
		return 0, ebuserrors.ErrTransportClosed
	}
	if len(payload) == 0 {
		return 0, nil
	}

	lb.buffer = append(lb.buffer, payload...)
	lb.cond.Broadcast()
	return len(payload), nil
}

// Close wakes blocked readers and rejects further writes. It is idempotent
// and always returns nil.
func (lb *Loopback) Close() error {
	lb.mutex.Lock()
	defer lb.mutex.Unlock()

	if lb.closed {
		return nil
	}

	lb.closed = true
	lb.cond.Broadcast()
	return nil
}

var _ RawTransport = (*Loopback)(nil)
