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
// Step B2 covers the full active-session path AND the PASSIVE_TRACKING
// composite state for foreign-initiator telegrams:
//   - IDLE, ARBITRATING (event-driven entry via EnterArbitrating)
//   - MASTER_HEADER, MASTER_DATA, MASTER_CRC, WAIT_MASTER_ACK, MASTER_RETX
//   - SLAVE_LENGTH, SLAVE_DATA, SLAVE_CRC, WAIT_SLAVE_ACK, SLAVE_RETX
//   - WAIT_TERMINATOR_SYN (success terminator)
//   - ABORTED (terminal fault)
//   - PASSIVE_TRACKING (composite, entered via EnterPassiveTracking)
//
// Step B3 wires this FSM into the proxy's adapter-facing read path.
package telegram_fsm

import "fmt"

// State enumerates the phases of an eBUS telegram per v6 §3. Each
// State has a per-phase rule inside Machine.Feed; the transition to
// the next state is committed inside Feed after the per-byte
// classification per v8 invariant I9 (classify under current phase,
// then transition).
//
// StatePassiveTracking is the v8 §3.1 composite state used for
// foreign-initiator telegrams; see the type docs below.
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

	// StateMasterRetx is the explicit retransmit state. Reached on
	// the first NACK in WAIT_MASTER_ACK. The initiator re-sends the
	// full frame (QQ ZZ PB SB NN DATA CRC). Per eBUS V1.3.1 / v8 I6
	// at most one retransmit per phase; a second NACK abort.
	StateMasterRetx

	// StateSlaveLength consumes the target's response length byte
	// (NN'). Per v6 §3 follows ACK from target in WAIT_MASTER_ACK.
	// NN' > 16 violates eBUS V1.3.1; immediate abort.
	StateSlaveLength

	// StateSlaveData consumes NN' data bytes from the target.
	StateSlaveData

	// StateSlaveCRC consumes the target's response CRC byte. The
	// initiator validates it; v8 §1.6 admin-logs CRC mismatch.
	StateSlaveCRC

	// StateWaitSlaveAck waits for the initiator's ACK/NACK of the
	// target's response. ACK leads to WAIT_TERMINATOR_SYN; NACK with
	// retx_count<1 leads to SLAVE_RETX; NACK with retx_count>=1 leads
	// to ABORTED.
	StateWaitSlaveAck

	// StateSlaveRetx is the explicit retransmit state for the target
	// half. Reached on the first NACK in WAIT_SLAVE_ACK. The target
	// re-sends its response (NN' DATA CRC). Per v8 I6 at most one
	// retransmit; a second NACK aborts.
	StateSlaveRetx

	// StateWaitTerminatorSyn waits for the final wire AUTO-SYN that
	// marks telegram completion. Per v6 §3 reached on:
	//   - MASTER_CRC with ZZ=0xFE (broadcast — no ACK/response).
	//   - WAIT_MASTER_ACK with ACK + initiator-initiator frame
	//     (PB=0xFE typically — no response phase).
	//   - WAIT_SLAVE_ACK with ACK (response acknowledged).
	// The terminator SYN itself IS a raw 0xAA byte; per v8 §4 it is
	// forwarded (not dropped as AA-injection — the WAIT_TERMINATOR_SYN
	// state is the one phase where raw 0xAA is legitimate). Per v8
	// §3 the per-state timeout is 100 ms.
	StateWaitTerminatorSyn

	// StateAborted is a terminal state reached on protocol fault or
	// NACK exhaustion. Per v8 §8 the FSM emits an admin event and
	// returns to IDLE without injecting synthetic byte-stream events.
	StateAborted

	// StatePassiveTracking is the composite state for foreign-initiator
	// telegrams per v8 §3 / §3.1. Internally the FSM runs the SAME
	// per-byte sub-phase logic as active mode (MASTER_HEADER → … →
	// WAIT_TERMINATOR_SYN, including MASTER_RETX and SLAVE_RETX), but
	// the Machine reports State() == StatePassiveTracking to the
	// caller so the classifier can route bytes through a no-staging
	// path (the proxy did not originate the bytes, so there's no
	// staging buffer to FIFO-match against).
	//
	// Entry: callers invoke EnterPassiveTracking() when they observe
	// the first non-SYN byte after IDLE without their own STARTED
	// event preceding (i.e., a foreign initiator won arbitration on the
	// wire). The entering byte counts as MASTER_HEADER byte 0 (QQ).
	//
	// Exit: terminator SYN observed in WAIT_TERMINATOR_SYN sub-phase
	// → return to StateIdle (passive flag cleared by ResetToIdle).
	StatePassiveTracking
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
	case StateMasterRetx:
		return "MASTER_RETX"
	case StateSlaveLength:
		return "SLAVE_LENGTH"
	case StateSlaveData:
		return "SLAVE_DATA"
	case StateSlaveCRC:
		return "SLAVE_CRC"
	case StateWaitSlaveAck:
		return "WAIT_SLAVE_ACK"
	case StateSlaveRetx:
		return "SLAVE_RETX"
	case StateWaitTerminatorSyn:
		return "WAIT_TERMINATOR_SYN"
	case StateAborted:
		return "ABORTED"
	case StatePassiveTracking:
		return "PASSIVE_TRACKING"
	default:
		return fmt.Sprintf("State(%d)", s)
	}
}

// IsTerminal reports whether the state is a terminal one (telegram
// has either successfully completed or aborted). Successful
// completion is represented by the FSM returning to StateIdle from
// WAIT_TERMINATOR_SYN; ABORTED is the only state that explicitly
// represents a terminated-with-fault telegram.
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
	// Reset on every phase transition. Used by MASTER_HEADER (counting
	// to 5), MASTER_DATA (counting to masterNN), SLAVE_DATA (counting
	// to slaveNN), and the single-byte phases (counting to 1). Name
	// retained for diff stability across part 1 → part 2 of Step B2.
	masterBytesConsumed byte

	// masterDest is the ZZ byte from MASTER_HEADER. Determines
	// post-MASTER_CRC routing: broadcast (0xFE) → terminator;
	// otherwise → WAIT_MASTER_ACK. Step B3 will add full
	// address-class disambiguation via protocol.AddressClassOf to
	// distinguish initiator-initiator (no response) from
	// initiator-target (with response).
	masterDest byte

	// masterRetxCount counts initiator-frame retransmissions per v8
	// invariant I6. Bounded at MaxRetxPerPhase = 1 per eBUS V1.3.1.
	masterRetxCount byte

	// slaveNN holds the target's response length (NN'), set in
	// SLAVE_LENGTH and consumed by SLAVE_DATA.
	slaveNN byte

	// slaveRetxCount counts target-response retransmissions per v8
	// invariant I6. Independent of masterRetxCount.
	slaveRetxCount byte

	// passive indicates that the Machine is tracking a foreign-
	// initiator telegram (PASSIVE_TRACKING composite state per v8
	// §3.1). When true, State() returns StatePassiveTracking instead
	// of the internal sub-phase. The per-phase Feed handlers behave
	// identically regardless of this flag — the difference is purely
	// the entry point (EnterArbitrating vs EnterPassiveTracking) and
	// the externally-reported State.
	passive bool
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

// State returns the Machine's current state. When the Machine is
// tracking a foreign-initiator telegram (after EnterPassiveTracking),
// State() returns StatePassiveTracking for any non-IDLE non-terminal
// internal sub-phase — this is the user-facing composite state per
// v8 §3.1.
//
// IMPORTANT: terminal states (StateAborted) are NEVER masked by the
// composite — they pass through directly. This preserves the
// IsTerminal()-based termination contract for callers that detect
// "telegram finished with fault" via `m.State().IsTerminal()`.
// Without this carve-out, passive-mode NACK exhaustion would surface
// as StatePassiveTracking (not terminal) and the caller would miss
// the abort signal.
//
// To inspect the internal sub-phase during passive tracking (e.g.,
// for testing), use InternalState().
//
// Once the FSM exits passive tracking (terminator SYN observed →
// IDLE, or RESETTED), State() returns the actual state again.
func (m *Machine) State() State {
	if m.passive && m.state != StateIdle && !m.state.IsTerminal() {
		return StatePassiveTracking
	}
	return m.state
}

// InternalState returns the internal sub-phase regardless of passive
// mode. Useful for tests that need to assert the exact sub-phase
// during foreign-telegram tracking.
func (m *Machine) InternalState() State {
	return m.state
}

// IsPassive reports whether the Machine is currently tracking a
// foreign-initiator telegram (entered via EnterPassiveTracking).
// Returns false in IDLE, in active mode, or after ResetToIdle.
func (m *Machine) IsPassive() bool {
	return m.passive
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
	m.slaveNN = 0
	m.slaveRetxCount = 0
	m.passive = false
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
	case StateMasterRetx:
		return m.feedMasterRetx(b, wasEscaped)
	case StateSlaveLength:
		return m.feedSlaveLength(b, wasEscaped)
	case StateSlaveData:
		return m.feedSlaveData(b, wasEscaped)
	case StateSlaveCRC:
		return m.feedSlaveCRC(b, wasEscaped)
	case StateWaitSlaveAck:
		return m.feedWaitSlaveAck(b, wasEscaped)
	case StateSlaveRetx:
		return m.feedSlaveRetx(b, wasEscaped)
	case StateWaitTerminatorSyn:
		return m.feedWaitTerminatorSyn(b, wasEscaped)
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
	m.passive = false
}

// EnterPassiveTracking transitions the Machine from IDLE directly to
// the MASTER_HEADER sub-phase under the PASSIVE_TRACKING composite
// state. Per v8 §3.1: callers invoke this when they observe the first
// non-SYN byte after IDLE without a preceding STARTED-for-us event
// (i.e., a foreign initiator won arbitration on the wire). The
// entering byte counts as MASTER_HEADER byte 0 (foreign initiator's
// QQ); the NEXT Feed() call will process byte 1 (ZZ) per the
// MASTER_HEADER handler.
//
// In passive mode, all per-phase Feed handlers behave identically to
// active mode: same AA-injection drop rules, same NACK retx caps,
// same WAIT_TERMINATOR_SYN exit. The Machine reports State() ==
// StatePassiveTracking externally; internal sub-phase tracking is
// available via InternalState().
//
// Exit: when the FSM reaches StateIdle (via WAIT_TERMINATOR_SYN
// observed, or via abort), the passive flag is cleared and the
// Machine is ready to enter either active or passive tracking again.
//
// EnterPassiveTracking is NOT safe for concurrent use; callers
// serialize per v8 invariant I11.
func (m *Machine) EnterPassiveTracking() {
	m.state = StateMasterHeader
	m.masterBytesConsumed = 1
	m.masterNN = 0
	m.masterDest = 0
	m.masterRetxCount = 0
	m.slaveNN = 0
	m.slaveRetxCount = 0
	m.passive = true
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
// WAIT_TERMINATOR_SYN; otherwise to WAIT_MASTER_ACK.
func (m *Machine) feedMasterCRC(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	_ = b // CRC validation deferred to v8 §1.6 implementation.
	if m.masterDest == BroadcastDestination {
		// Broadcast: skip ACK phase, await final terminator SYN.
		m.state = StateWaitTerminatorSyn
	} else {
		m.state = StateWaitMasterAck
	}
	return DecisionForward
}

// feedWaitMasterAck handles the WAIT_MASTER_ACK state. Expects ACK
// (0x00) → SLAVE_LENGTH, NACK (0xFF) → MASTER_RETX (if budget) or
// ABORTED, AA-injection drop, anything else protocol fault.
func (m *Machine) feedWaitMasterAck(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	switch b {
	case ACKByte:
		// ACK from target → enter response phase. The current
		// implementation routes EVERY non-broadcast frame to
		// SLAVE_LENGTH (initiator-target assumption).
		//
		// INITIATOR-INITIATOR FRAMES ARE NOT YET SUPPORTED here:
		// for i2i, v6 §3 says ACK → WAIT_TERMINATOR_SYN directly
		// (no response phase). The current code would land in
		// SLAVE_LENGTH and then receive the wire-real terminator
		// SYN — which SLAVE_LENGTH drops as AA-injection per v8 §4,
		// leaving the FSM stuck. Address-class routing via
		// AddressClassOf is the fix and lands in Step B3.
		// Until then, i2i frames must not be fed through this FSM;
		// callers should detect i2i upstream and use an alternate
		// path or skip FSM-mediated classification for those frames.
		m.state = StateSlaveLength
		m.masterBytesConsumed = 0
		return DecisionForward
	case NACKByte:
		if m.masterRetxCount < MaxRetxPerPhase {
			m.masterRetxCount++
			// Per v6 §3, transition to MASTER_RETX. The initiator
			// re-sends the full frame; the next bytes will form
			// the resent QQ ZZ PB SB NN sequence. MASTER_RETX
			// itself just immediately advances to MASTER_HEADER
			// when the next byte arrives.
			m.state = StateMasterRetx
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

// feedMasterRetx handles the MASTER_RETX state. Per v6 §3, the
// initiator's NACK retransmit re-sends the entire initiator frame. The
// first byte after the NACK is the resent QQ; transition to
// MASTER_HEADER and consume the byte as header byte 1.
//
// Raw 0xAA in MASTER_RETX is AA-injection (same as ARBITRATING — no
// real wire bytes should arrive in this micro-window).
func (m *Machine) feedMasterRetx(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	// Reset header tracking for the retransmit.
	m.state = StateMasterHeader
	m.masterBytesConsumed = 1
	m.masterNN = 0
	m.masterDest = 0
	_ = b // QQ recorded by header consumption; validation deferred.
	return DecisionForward
}

// feedSlaveLength handles the SLAVE_LENGTH state. Consumes the
// target's NN' response-length byte. Per v8 §1.7: NN' > 16 violates
// eBUS V1.3.1; immediate abort. Raw 0xAA is AA-injection.
//
// Special case NN' == 0: skip SLAVE_DATA, go directly to SLAVE_CRC.
// The frame is initiator-target with empty response payload (an
// acknowledgement-only frame). Initiator-initiator frames are
// NOT supported here yet (see feedWaitMasterAck comment); they
// would never legitimately reach SLAVE_LENGTH once Step B3 lands
// address-class routing.
func (m *Machine) feedSlaveLength(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	if b > 16 {
		// Per v8 §1.7 / I6: NN' > 16 is spec-illegal.
		m.state = StateAborted
		return DecisionProtocolFault
	}
	m.slaveNN = b
	if b == 0 {
		// Empty response — go straight to CRC.
		m.state = StateSlaveCRC
		m.masterBytesConsumed = 0
	} else {
		m.state = StateSlaveData
		m.masterBytesConsumed = 0
	}
	return DecisionForward
}

// feedSlaveData handles the SLAVE_DATA state. Consumes slaveNN data
// bytes then transitions to SLAVE_CRC. Raw 0xAA is AA-injection;
// escape-decoded 0xAA is a legitimate payload byte.
func (m *Machine) feedSlaveData(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	m.masterBytesConsumed++
	if m.masterBytesConsumed >= m.slaveNN {
		m.state = StateSlaveCRC
		m.masterBytesConsumed = 0
	}
	return DecisionForward
}

// feedSlaveCRC handles the SLAVE_CRC state. Consumes the target's
// CRC byte. Per v6 §3 always transitions to WAIT_SLAVE_ACK
// (initiator must ACK/NACK the response). CRC validation is
// deferred to v8 §1.6 admin-channel implementation.
func (m *Machine) feedSlaveCRC(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	_ = b // CRC validation deferred to v8 §1.6.
	m.state = StateWaitSlaveAck
	return DecisionForward
}

// feedWaitSlaveAck handles the WAIT_SLAVE_ACK state. Expects ACK
// (initiator accepts response) → WAIT_TERMINATOR_SYN; NACK
// (initiator rejects) → SLAVE_RETX (if budget) or ABORTED.
func (m *Machine) feedWaitSlaveAck(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	switch b {
	case ACKByte:
		m.state = StateWaitTerminatorSyn
		return DecisionForward
	case NACKByte:
		if m.slaveRetxCount < MaxRetxPerPhase {
			m.slaveRetxCount++
			m.state = StateSlaveRetx
			return DecisionForward
		}
		// Retx budget exhausted (v8 invariant I6).
		m.state = StateAborted
		return DecisionForward
	default:
		// Malformed during target-ACK wait.
		m.state = StateAborted
		return DecisionProtocolFault
	}
}

// feedSlaveRetx handles the SLAVE_RETX state. Per v6 §3, on initiator
// NACK the target re-sends its response (NN' DATA CRC). The first
// byte after the NACK is the resent NN'; transition to SLAVE_LENGTH
// and feed the byte through that handler.
func (m *Machine) feedSlaveRetx(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		return DecisionDropAaInjection
	}
	// Reset target-response tracking for the retransmit and consume this byte
	// as the resent NN'. We mirror feedSlaveLength's logic inline so
	// that the single Feed call's Decision is computed under
	// SLAVE_RETX's branch (per v8 I9 classify-then-transition).
	if b > 16 {
		m.state = StateAborted
		return DecisionProtocolFault
	}
	m.slaveNN = b
	if b == 0 {
		m.state = StateSlaveCRC
	} else {
		m.state = StateSlaveData
	}
	m.masterBytesConsumed = 0
	return DecisionForward
}

// feedWaitTerminatorSyn handles the WAIT_TERMINATOR_SYN state. Per
// v6 §3 the terminator IS a raw 0xAA byte (wasEscaped=false). Unlike
// every other active phase, raw 0xAA here is FORWARDED (not dropped
// as AA-injection) — it is the legitimate end-of-telegram marker.
//
// Anything else is a protocol fault. After the terminator, FSM
// returns to IDLE (v8 §3 success exit).
func (m *Machine) feedWaitTerminatorSyn(b byte, wasEscaped bool) Decision {
	if b == SynByte && !wasEscaped {
		// Terminator SYN observed. Return to IDLE.
		m.ResetToIdle()
		return DecisionForward
	}
	// Any non-SYN byte at this point is unexpected.
	m.state = StateAborted
	return DecisionProtocolFault
}
