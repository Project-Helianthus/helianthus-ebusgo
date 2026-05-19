// Package telegram_fsm implements the per-telegram phase state machine
// described in helianthus-docs-ebus/architecture/adaptermux/
// frame-atomic-visibility-v8.md §3 + v6 §3.
//
// This is the shared library for v8 §1.11: "modelled-on (not literal
// reuse of) the existing passive_transaction_reconstructor.go in
// helianthus-ebusgateway." The FSM transition logic is the same as
// what the passive reconstructor already implements, but extracted
// into a self-contained pure-state-machine package consumable by:
//
//   - The proxy classifier in helianthus-ebus-adapter-proxy (Step B3,
//     wiring the AA-aware escape decoder + per-session staging buffer
//   - this FSM into the adapter-facing read path).
//   - The gateway's own bus.go for active-client echo matching
//     (future work, not in v8 scope).
//   - Future passive observers.
//
// The FSM is intentionally:
//
//   - **Pure**. No I/O, no goroutines, no clocks. Transitions are
//     synchronous functions of (current state, input byte).
//   - **Allocation-free in the Feed hot path**. No slices grown per
//     byte, no maps, no fmt.Sprintf inside Feed. The String() methods
//     do use fmt.Sprintf as a fallback for unknown enum values; those
//     are not on the hot path.
//   - **Single-byte serial**. Callers feed one decoded byte at a time
//     per v8 invariant I9 (classify under current phase, then
//     transition).
//   - **NOT thread-safe**. Per v8 invariant I11, callers serialize
//     access on a per-session goroutine.
//
// This first iteration (Step B2) covers the active write-out path:
// IDLE, ARBITRATING, MASTER_HEADER, MASTER_DATA, MASTER_CRC,
// WAIT_MASTER_ACK. Subsequent iterations will add SLAVE_* phases,
// MASTER_RETX / SLAVE_RETX, PASSIVE_TRACKING, and WAIT_TERMINATOR_SYN.
package telegram_fsm

import "fmt"

// State enumerates the phases of an eBUS telegram per v6 §3. Each
// State has a per-phase rule inside Machine.Feed; the transition to
// the next state is committed inside Feed after the per-byte
// classification per v8 invariant I9 (classify under current phase,
// then transition).
//
// v8's PASSIVE_TRACKING composite state and the SLAVE_* phases land
// in subsequent commits.
type State uint8

const (
	// StateIdle is the genuinely-quiet phase between telegrams. The
	// proxy forwards every byte unconditionally; AA-injection during
	// IDLE is forwarded as wire AUTO-SYN by design (per v8 §3 IDLE
	// pass-through, honest residual §14).
	StateIdle State = iota

	// StateArbitrating is the brief window between an ENH STARTED
	// event for an active session and the first echo byte. Per v8
	// §4: raw 0xAA in ARBITRATING is adapter-spurious AA-injection
	// (no real wire bytes should arrive in this window besides the
	// arbitration result), so it is dropped.
	StateArbitrating

	// StateMasterHeader consumes the 5 initiator-frame header bytes:
	// QQ (source address), ZZ (destination address), PB (primary
	// command), SB (secondary command), NN (data length).
	StateMasterHeader

	// StateMasterData consumes the NN data bytes after the header,
	// where NN was the 5th MASTER_HEADER byte.
	StateMasterData

	// StateMasterCRC consumes the single CRC byte that follows
	// MASTER_DATA. Per v8 §1.6 the proxy can validate the CRC against
	// the initiator-frame bytes it observed; mismatch is admin-logged.
	StateMasterCRC

	// StateWaitMasterAck waits for the target's ACK (0x00) or NACK
	// (0xFF) after the initiator CRC. ACK leads to SLAVE_LENGTH for
	// initiator-target, WAIT_TERMINATOR_SYN for initiator-initiator
	// or broadcast (ZZ=0xFE). NACK with retx_count<1 leads to
	// MASTER_RETX; NACK with retx_count>=1 leads to ABORTED. Per v8
	// invariant I6 (retx cap = 1 per phase).
	StateWaitMasterAck

	// StateAborted is a terminal state reached on protocol fault or
	// NACK exhaustion. Per v8 §8 the FSM emits an admin event and
	// returns to IDLE without injecting synthetic byte-stream events.
	StateAborted
)

// String returns a short identifier suitable for admin logs.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "IDLE"
	case StateArbitrating:
		return "ARBITRATING"
	case StateMasterHeader:
		return "MASTER_HEADER"
	case StateMasterData:
		return "MASTER_DATA"
	case StateMasterCRC:
		return "MASTER_CRC"
	case StateWaitMasterAck:
		return "WAIT_MASTER_ACK"
	case StateAborted:
		return "ABORTED"
	default:
		return fmt.Sprintf("State(%d)", s)
	}
}

// IsTerminal reports whether the state is a terminal one (telegram
// has either successfully completed or aborted). Terminal states
// must transition back to IDLE before the next telegram begins.
//
// In this initial implementation only ABORTED is terminal; DONE
// (success terminator) is represented by a transition back to IDLE
// after WAIT_TERMINATOR_SYN sees its SYN (added in a later Step B2
// iteration once WAIT_TERMINATOR_SYN is wired).
func (s State) IsTerminal() bool {
	return s == StateAborted
}

// Decision describes the action the FSM dictates for a single input
// byte. Machine.Feed returns this; callers act on it (forward, drop,
// or admin-event-and-forward) before feeding the next byte.
//
// Decisions are tagged with their interpretive role so the caller
// can route the byte to:
//
//   - The session's TCP egress queue (DecisionForward).
//   - The bit bucket (DecisionDropAaInjection).
//   - The byte stream AND the protocol-fault admin channel
//     (DecisionProtocolFault — per v8 invariant I10, the byte is
//     forwarded to preserve wire fidelity AND an admin event is
//     surfaced for operator visibility).
type Decision uint8

const (
	// DecisionForward means the byte is a legitimate part of the
	// telegram in its current phase and should be emitted to all
	// downstream observers.
	DecisionForward Decision = iota

	// DecisionDropAaInjection means the byte is an adapter-spurious
	// 0xAA that should not reach any observer. The FSM does NOT
	// advance; the next byte is still classified under the same
	// phase. Per v8 §4 (AA-injection filter).
	DecisionDropAaInjection

	// DecisionProtocolFault means the byte is neither a legitimate
	// phase byte nor an absorbable AA-injection. Callers should
	// emit an admin event AND forward the byte (per v8 invariant I10:
	// PROTOCOL_FAULT visibility — drop nothing from the byte stream,
	// admin-log the fault). The FSM transitions to ABORTED.
	DecisionProtocolFault
)

// String returns a short identifier suitable for admin logs.
func (d Decision) String() string {
	switch d {
	case DecisionForward:
		return "forward"
	case DecisionDropAaInjection:
		return "drop_aa_injection"
	case DecisionProtocolFault:
		return "protocol_fault"
	default:
		return fmt.Sprintf("Decision(%d)", d)
	}
}

// Machine is a single-telegram FSM instance. Construct with New().
//
// Machine is NOT thread-safe. Per v8 invariant I11, callers must
// serialize Feed() on a per-session goroutine.
//
// The zero value is NOT usable; use New().
type Machine struct {
	state State

	// masterNN holds the data-length byte observed in MASTER_HEADER.
	// Used by MASTER_DATA to count down bytes to consume before
	// transitioning to MASTER_CRC.
	masterNN byte

	// masterBytesConsumed counts bytes consumed in the current phase.
	// Reset on every phase transition. Used by MASTER_HEADER
	// (counting to 5), MASTER_DATA (counting to masterNN), and
	// MASTER_CRC (counting to 1).
	masterBytesConsumed byte

	// masterDest is the ZZ byte from MASTER_HEADER. Determines
	// post-MASTER_CRC routing: broadcast (0xFE) → terminator;
	// otherwise → WAIT_MASTER_ACK.
	masterDest byte

	// masterRetxCount counts initiator-frame retransmissions per v8
	// invariant I6. Bounded at MaxRetxPerPhase = 1 per eBUS V1.3.1.
	masterRetxCount byte
}

// MaxRetxPerPhase is the eBUS V1.3.1 single-retransmit cap per phase
// (v8 I6). The initiator may retransmit its frame once on NACK from
// target; the target may retransmit its response once on NACK from
// the initiator. After one retransmit, the next NACK aborts the
// telegram.
const MaxRetxPerPhase byte = 1

// BroadcastDestination is the eBUS broadcast ZZ value (0xFE). Per
// v6 §3 an initiator frame with ZZ=0xFE skips the WAIT_MASTER_ACK / target
// phases and goes directly to WAIT_TERMINATOR_SYN.
const BroadcastDestination byte = 0xFE

// ACKByte (0x00) is the target's positive acknowledgement after
// receiving a valid initiator CRC. Per v6 §3 it leads to either
// SLAVE_LENGTH (for initiator-target frames) or WAIT_TERMINATOR_SYN
// (for initiator-initiator frames).
const ACKByte byte = 0x00

// NACKByte (0xFF) is the target's negative acknowledgement. Per v6
// §3 / v8 I6 it triggers a MASTER_RETX (if retx_count<1) or ABORTED
// (if retx_count>=1).
const NACKByte byte = 0xFF

// SynByte (0xAA) is the eBUS AUTO-SYN value. Per v8 §4, raw 0xAA
// (was_escaped=false) in active-write phases is AA-injection from
// adapter buffering and is dropped; escape-decoded 0xAA
// (was_escaped=true) is a legitimate payload byte.
const SynByte byte = 0xAA

// New returns a Machine in StateIdle, ready to accept its first
// byte via Feed().
func New() *Machine {
	return &Machine{state: StateIdle}
}

// State returns the Machine's current state. Useful for admin
// observability and tests.
func (m *Machine) State() State {
	return m.state
}

// ResetToIdle returns the Machine to StateIdle and clears all
// per-telegram state. Useful after a transport RESETTED event (v8
// invariant I5) or after consuming a WAIT_TERMINATOR_SYN.
func (m *Machine) ResetToIdle() {
	m.state = StateIdle
	m.masterNN = 0
	m.masterBytesConsumed = 0
	m.masterDest = 0
	m.masterRetxCount = 0
}

// Feed consumes one decoded byte and returns the Decision for that
// byte. Per v8 invariant I9, the Decision is classified under the
// CURRENT state, then (if the byte completes the phase, e.g., the
// 5th MASTER_HEADER byte) the Machine transitions to the next phase
// before Feed returns. Callers see the Decision computed against
// the phase that owned the byte; subsequent State() calls reflect
// the post-transition state.
//
// The boolean wasEscaped distinguishes a wire-emitted 0xAA byte
// (wasEscaped=false, the AUTO-SYN) from an escape-decoded logical
// 0xAA byte (wasEscaped=true, payload). Per v8 §4 the AA-injection
// filter uses this provenance.
//
// Feed is NOT safe for concurrent use; callers must serialize per
// v8 invariant I11.
func (m *Machine) Feed(b byte, wasEscaped bool) Decision {
	switch m.state {
	case StateIdle:
		return m.feedIdle(b, wasEscaped)
	case StateArbitrating:
		return m.feedArbitrating(b, wasEscaped)
	case StateMasterHeader:
		return m.feedMasterHeader(b, wasEscaped)
	case StateMasterData:
		return m.feedMasterData(b, wasEscaped)
	case StateMasterCRC:
		return m.feedMasterCRC(b, wasEscaped)
	case StateWaitMasterAck:
		return m.feedWaitMasterAck(b, wasEscaped)
	case StateAborted:
		// Per v8 §8: after ABORTED, callers should ResetToIdle
		// before feeding more bytes. Feeding while ABORTED is a
		// caller bug; report PROTOCOL_FAULT to surface it.
		return DecisionProtocolFault
	default:
		// Defensive: unreachable for the current State enum.
		return DecisionProtocolFault
	}
}

// EnterArbitrating transitions the Machine from IDLE to ARBITRATING
// in response to an ENH STARTED control event for this session. The
// next Feed() call will be classified under STATE_ARBITRATING.
//
// Per v6 §3 the STARTED→ARBITRATING transition is event-driven (not
// byte-driven), so it lives outside Feed.
func (m *Machine) EnterArbitrating() {
	m.state = StateArbitrating
}

// feedIdle handles the IDLE state. Per v8 §3 IDLE pass-through, every
// byte is forwarded regardless of value. Wire AUTO-SYNs and
// adapter-spurious AA-injection are indistinguishable in IDLE without
// wire-time information (honest residual §14).
func (m *Machine) feedIdle(b byte, wasEscaped bool) Decision {
	_ = b
	_ = wasEscaped
	return DecisionForward
}

// feedArbitrating handles the ARBITRATING state. Per v8 §4: no real
// wire bytes should arrive in this window (the adapter holds the wire
// for arbitration outcome resolution). Any byte that does is either
// adapter-spurious AA-injection (drop) or a genuine fault (admin).
func (m *Machine) feedArbitrating(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	// Some adapters may emit the resolved-source byte here as a normal
	// echo when arbitration succeeds. For now treat any non-SYN byte
	// as the first MASTER_HEADER byte (QQ): forward, transition to
	// MASTER_HEADER, and arrange for the byte to count in MASTER_HEADER.
	//
	// The transition is committed AFTER returning (v8 I9: classify
	// under current state first); the next state advance happens in
	// transitionAfterArbitrating.
	m.transitionAfterArbitrating(b)
	return DecisionForward
}

// transitionAfterArbitrating commits the post-classification state
// change for the ARBITRATING state. The byte that triggered the
// transition is the first MASTER_HEADER byte (QQ); record it as
// consumed inside the new state.
func (m *Machine) transitionAfterArbitrating(b byte) {
	m.state = StateMasterHeader
	m.masterBytesConsumed = 1
	// QQ is recorded but not validated here; v8 §1.7's
	// initiator-class validation happens in a future iteration that
	// wires the AddressClassOf check.
	_ = b
}

// feedMasterHeader handles the MASTER_HEADER state. The state
// consumes 5 bytes: QQ, ZZ, PB, SB, NN. The 2nd byte (ZZ) is
// remembered to route post-CRC; the 5th byte (NN) is the data length.
func (m *Machine) feedMasterHeader(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		// Raw 0xAA during MASTER_HEADER is AA-injection (v8 §4).
		return DecisionDropAaInjection
	}
	// Consume byte. masterBytesConsumed reflects bytes already
	// consumed BEFORE this one (initialized to 1 by ARBITRATING for
	// QQ; or 0 for direct entry — kept compatible with both paths).
	switch m.masterBytesConsumed {
	case 1:
		// 2nd byte is ZZ (destination).
		m.masterDest = b
	case 4:
		// 5th byte is NN (data length).
		m.masterNN = b
		if b > 16 {
			// Per v8 §1.7: NN > 16 violates eBUS V1.3.1 spec (max 16
			// data bytes). Fault.
			m.state = StateAborted
			return DecisionProtocolFault
		}
	}
	m.masterBytesConsumed++

	if m.masterBytesConsumed >= 5 {
		// MASTER_HEADER complete. Decide next state based on NN.
		if m.masterNN == 0 {
			// No data bytes; skip MASTER_DATA, go straight to CRC.
			m.state = StateMasterCRC
			m.masterBytesConsumed = 0
		} else {
			m.state = StateMasterData
			m.masterBytesConsumed = 0
		}
	}
	return DecisionForward
}

// feedMasterData handles the MASTER_DATA state. Consumes masterNN
// bytes then transitions to MASTER_CRC.
func (m *Machine) feedMasterData(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	m.masterBytesConsumed++
	if m.masterBytesConsumed >= m.masterNN {
		m.state = StateMasterCRC
		m.masterBytesConsumed = 0
	}
	return DecisionForward
}

// feedMasterCRC handles the MASTER_CRC state. Consumes the single
// CRC byte. Per v6 §3: if ZZ == 0xFE (broadcast) the FSM goes to
// WAIT_TERMINATOR_SYN; otherwise to WAIT_MASTER_ACK. In this initial
// Step B2 iteration WAIT_TERMINATOR_SYN is not yet wired, so the
// broadcast path falls back to IDLE (caller is expected to forward
// the post-CRC SYN as terminator) — this will be tightened in a
// subsequent commit when WAIT_TERMINATOR_SYN is added.
func (m *Machine) feedMasterCRC(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	_ = b // CRC validation deferred to v8 §1.6 implementation.
	if m.masterDest == BroadcastDestination {
		// Broadcast: skip ACK phase, return to IDLE pending terminator.
		// (WAIT_TERMINATOR_SYN added in a later commit.)
		m.ResetToIdle()
	} else {
		m.state = StateWaitMasterAck
	}
	return DecisionForward
}

// feedWaitMasterAck handles the WAIT_MASTER_ACK state. Expects ACK
// (0x00), NACK (0xFF), or AA-injection. Any other byte is a protocol
// fault.
func (m *Machine) feedWaitMasterAck(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	switch b {
	case ACKByte:
		// ACK accepted. Next state in v6 §3 is SLAVE_LENGTH for
		// initiator-target or WAIT_TERMINATOR_SYN for
		// initiator-initiator. Target phases land in a follow-up
		// commit; for now ABORT to IDLE so behavior is well-defined.
		m.ResetToIdle()
		return DecisionForward
	case NACKByte:
		if m.masterRetxCount < MaxRetxPerPhase {
			m.masterRetxCount++
			// Per v6 §3, MASTER_RETX state resets header tracking
			// and waits for the resend of QQ. In this initial
			// iteration, restart MASTER_HEADER directly (MASTER_RETX
			// state added in a follow-up commit).
			m.state = StateMasterHeader
			m.masterBytesConsumed = 0
			m.masterNN = 0
			m.masterDest = 0
			return DecisionForward
		}
		// Retx budget exhausted (v8 invariant I6).
		m.state = StateAborted
		return DecisionForward
	default:
		// Any other byte during ACK wait is malformed.
		m.state = StateAborted
		return DecisionProtocolFault
	}
}
