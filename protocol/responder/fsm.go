// M4c1 PR-B: responder ACK / response / final-ACK FSM.
//
// States (see `fsm_test.go` for the locked contract):
//
//	Idle
//	  └─(inbound-frame-for-local, CRC ok)────▶ AckReceived
//	  └─(inbound-frame-for-local, CRC bad)───▶ NackReceived ──▶ Idle
//	AckReceived
//	  └─(emit ACK)──▶ ResponseSent (after emitting response payload)
//	ResponseSent
//	  └─(initiator final ACK)──▶ Idle
//	  └─(initiator NACK, retries < N)──▶ AckReceived (re-send)
//	  └─(initiator NACK, retries = N)──▶ Idle (aborted)

package responder

import (
	"errors"
	"reflect"
	"sync"
)

// State is the FSM state type.
type State int

// FSM state constants. The exact names are locked by
// `TestM4c1_PRB_FSM_States_Declared`.
const (
	StateIdle State = iota
	StateAckReceived
	StateNackReceived
	StateResponseSent
)

// String returns the state's declared identifier.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "StateIdle"
	case StateAckReceived:
		return "StateAckReceived"
	case StateNackReceived:
		return "StateNackReceived"
	case StateResponseSent:
		return "StateResponseSent"
	default:
		return "StateUnknown"
	}
}

// FSM-level errors.
var (
	// ErrInvalidTransition is returned when a transition method is
	// called from a state that does not permit it.
	ErrInvalidTransition = errors.New("responder: invalid FSM transition")
	// ErrRetriesExhausted is returned from OnInitiatorNack when the
	// configured retry budget has been consumed.
	ErrRetriesExhausted = errors.New("responder: retries exhausted")
)

// DefaultMaxNackRetries is the default retry budget for ResponseSent →
// AckReceived re-send. Conservative value per eBUS spec (typical
// responder-side retry budget is 1–3). Callers may override via
// `FSM.MaxNackRetries`.
const DefaultMaxNackRetries = 3

// FSM implements the responder state machine. All public methods are safe
// for concurrent use (mutex-guarded).
type FSM struct {
	mu             sync.Mutex
	state          State
	retries        int
	MaxNackRetries int
}

// NewFSM returns an FSM in StateIdle.
func NewFSM() *FSM {
	return &FSM{
		state:          StateIdle,
		MaxNackRetries: DefaultMaxNackRetries,
	}
}

// State returns the current FSM state.
func (f *FSM) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// OnInboundFrame records the receipt of an inbound-for-local frame.
// `crcOk` selects the Idle → AckReceived (true) or
// Idle → NackReceived (false) edge. NackReceived auto-recovers to Idle.
func (f *FSM) OnInboundFrame(crcOk bool) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateIdle {
		return f.state, ErrInvalidTransition
	}
	if crcOk {
		f.state = StateAckReceived
		f.retries = 0
	} else {
		// Transient NackReceived → auto-recover to Idle in a single
		// atomic transition. Observers using State() see Idle; the
		// explicit NackReceived state is declared so the FSM surface
		// is complete per the locked spec.
		f.state = StateNackReceived
		f.state = StateIdle
	}
	return f.state, nil
}

// OnEmitResponse advances AckReceived → ResponseSent after the responder
// has emitted its ACK+payload onto the wire.
func (f *FSM) OnEmitResponse() (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateAckReceived {
		return f.state, ErrInvalidTransition
	}
	f.state = StateResponseSent
	return f.state, nil
}

// OnInitiatorFinalAck advances ResponseSent → Idle when the initiator's
// final ACK has been observed.
func (f *FSM) OnInitiatorFinalAck() (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateResponseSent {
		return f.state, ErrInvalidTransition
	}
	f.state = StateIdle
	f.retries = 0
	return f.state, nil
}

// OnInitiatorNack reacts to an initiator NACK in ResponseSent. If retries
// remain it returns AckReceived (caller re-emits the response). If the
// retry budget is exhausted it returns Idle + ErrRetriesExhausted.
func (f *FSM) OnInitiatorNack() (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateResponseSent {
		return f.state, ErrInvalidTransition
	}
	// Invariant: MaxNackRetries=N permits exactly N retries. The N-th
	// retry succeeds (returns StateAckReceived); the (N+1)-th NACK
	// exhausts the budget. Compare BEFORE incrementing so that f.retries
	// records the number of retries already consumed.
	if f.retries >= f.MaxNackRetries {
		f.state = StateIdle
		return f.state, ErrRetriesExhausted
	}
	f.retries++
	f.state = StateAckReceived
	return f.state, nil
}

// Reset forces the FSM back to Idle. Intended for test harnesses and
// recovery after unrecoverable transport error.
func (f *FSM) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = StateIdle
	f.retries = 0
}

func init() {
	registerExport("FSM", reflect.TypeOf((*FSM)(nil)))
	registerExport("StateIdle", StateIdle)
	registerExport("StateAckReceived", StateAckReceived)
	registerExport("StateNackReceived", StateNackReceived)
	registerExport("StateResponseSent", StateResponseSent)
}
