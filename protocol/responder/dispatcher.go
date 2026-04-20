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

// Handle routes a decoded frame. Return semantics:
//
//   - (true, nil): frame.Target matched localAddr and the bound FSM
//     accepted the transition (Idle → AckReceived).
//   - (false, ErrInvalidTransition): frame.Target matched localAddr but
//     the FSM rejected the transition (e.g. an out-of-order inbound
//     arrived while the FSM was not in StateIdle). Surfaces the sentinel
//     so callers can log/metric the protocol violation instead of
//     silently treating the frame as accepted.
//   - (false, nil): frame.Target did NOT match localAddr — the frame is
//     dropped silently (dropped counter increments). This preserves the
//     bus-hygiene invariant: a responder lane MUST NOT ACK non-local
//     frames, and MUST NOT emit noise for them.
//
// A nil bound FSM behaves as (true, nil) after the ZZ match — useful for
// exercising the filter in isolation.
func (d *LocalResponderDispatcher) Handle(frame DecodedFrame) (bool, error) {
	d.mu.Lock()
	if frame.Target != d.localAddr {
		d.dropped++
		d.mu.Unlock()
		return false, nil
	}
	fsm := d.fsm
	d.mu.Unlock()
	if fsm != nil {
		// CRC validity is the FrameDecoder's contract — a DecodedFrame
		// in hand implies CRC passed.
		if _, err := fsm.OnInboundFrame(true); err != nil {
			return false, err
		}
	}
	return true, nil
}

func init() {
	registerExport("LocalResponderDispatcher", reflect.TypeOf((*LocalResponderDispatcher)(nil)))
}
