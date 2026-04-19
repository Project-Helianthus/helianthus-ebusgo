// M4c1 PR-B: LocalResponderDispatcher — inbound-frame router.
//
// Routes decoded frames whose ZZ matches the configured local responder
// address; drops frames addressed elsewhere silently (bus-hygiene
// invariant: a responder lane MUST NOT ACK non-local frames).

package responder

import (
	"reflect"
	"sync"
)

// LocalResponderDispatcher owns the ZZ filter + FSM handoff for a single
// local responder address. Thread-safe under its own mutex; the bound FSM
// has its own mutex, so concurrent Handle calls serialise cleanly.
type LocalResponderDispatcher struct {
	mu        sync.Mutex
	localAddr byte
	fsm       *FSM
	// dropped counts frames filtered out by the ZZ filter. Exposed via
	// Dropped() for observability / testing.
	dropped uint64
}

// NewLocalResponderDispatcher binds a dispatcher to the configured local
// responder address and an FSM instance. `fsm` MAY be nil if the caller
// only wants to exercise the ZZ filter (the state transitions become
// no-ops).
func NewLocalResponderDispatcher(localAddr byte, fsm *FSM) *LocalResponderDispatcher {
	return &LocalResponderDispatcher{
		localAddr: localAddr,
		fsm:       fsm,
	}
}

// LocalAddress returns the configured local responder address.
func (d *LocalResponderDispatcher) LocalAddress() byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.localAddr
}

// Dropped returns the count of frames filtered out by the ZZ filter.
func (d *LocalResponderDispatcher) Dropped() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

// Handle routes a decoded frame. If frame.Target != localAddr the frame
// is dropped silently (dropped counter increments). Otherwise the bound
// FSM advances via OnInboundFrame(true). Returns `true` if the frame was
// accepted for local handling, `false` if filtered out.
func (d *LocalResponderDispatcher) Handle(frame DecodedFrame) bool {
	d.mu.Lock()
	if frame.Target != d.localAddr {
		d.dropped++
		d.mu.Unlock()
		return false
	}
	fsm := d.fsm
	d.mu.Unlock()
	if fsm != nil {
		// CRC validity is the FrameDecoder's contract — a DecodedFrame
		// in hand implies CRC passed.
		_, _ = fsm.OnInboundFrame(true)
	}
	return true
}

func init() {
	registerExport("LocalResponderDispatcher", reflect.TypeOf((*LocalResponderDispatcher)(nil)))
}
