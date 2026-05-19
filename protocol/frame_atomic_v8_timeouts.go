// Package protocol — frame-atomic visibility v8 per-phase timeout constants.
//
// These constants are the spec-aligned per-state timeout and budget values
// for the telegram FSM described in:
//
//	helianthus-docs-ebus/architecture/adaptermux/frame-atomic-visibility-v8.md
//
// v8 is the converged design after eight rounds of adversarial review. The
// numbered sections in v8.md are scoped to the *delta* over v7; the full
// FSM phase timeout table is canonically defined in v6 §3 ("The FSM
// (unchanged from v5 §3 plus passive-mode transitions) ... Per-state
// timeouts pinned from eBUS V1.3.1 spec"), with v7 §1.5 and v8 §1.5
// reaffirming WAIT_MASTER_ACK = 200 ms, and v8 §1.3 introducing the
// ESCAPE_PENDING 32 ms hard cap.
//
// These constants are DECLARED here so the Step A migration can land
// independently as a doc-aligned addition with no behavior change. Actual
// wiring into the active read path happens in Step B, when the telegram
// FSM is extracted into a shared library (per v8 §1.11 and the migration
// list in v8.md, last block).
//
// Current bus.go does not have per-phase timeouts in the read path; it
// inherits the transport-level read timeout (default 5s on adapter-direct
// transports). Step B will replace per-symbol reads with phase-aware
// readByteWithDeadline(phase) calls keyed off these constants.
//
// All wall-clock values are engineering choices per v8 §1.5 and v6 §3:
//
//   - The eBUS V1.3.1 spec mandates that the target respond within the
//     AUTO-SYN slot (~35–45 ms). The longer values here (e.g., 200 ms
//     for ACK phases) provide tolerance for slow or contended targets
//     without introducing spec violations on our side.
//   - Reducing these values below the documented constants will create
//     spurious aborts on healthy-but-slow targets; increasing them past
//     these constants will mask real stuck-target detection.
//
// The corresponding Prometheus alert (per v8 §1.9) on
// helianthus_round9_absorb_fired_proxy_mediated_total must be deployed
// alongside any code path that wires these timeouts in, since the alert
// gates the legacy-fallback verification of v8 §10.
package protocol

import "time"

// Per-phase wall-clock timeouts from the v8 telegram FSM. Each value
// traces to a specific section of the design doc, cited inline.
//
// All values are constants. Downstream consumers (the proxy in
// helianthus-ebus-adapter-proxy, the future FSM extraction in
// protocol/telegram_fsm) must reference these names rather than
// duplicating the literals.
const (
	// FrameAtomicV8InterByteTimeout is the deadline between consecutive
	// bytes within a fixed-length phase (MASTER_HEADER consuming the 5
	// header bytes, MASTER_DATA consuming NN data bytes, MASTER_CRC,
	// SLAVE_LENGTH, SLAVE_DATA, SLAVE_CRC). Per v6 §3: "10 ms
	// (conservative; spec minimum 4.17 ms)."
	FrameAtomicV8InterByteTimeout = 10 * time.Millisecond

	// FrameAtomicV8WaitMasterAckTimeout is the deadline for receiving
	// ACK/NACK after the initiator's CRC. Per v8 §1.5: "200 ms is an
	// engineering choice that accommodates targets up to ~5× the
	// AUTO-SYN slot (~35–45 ms) while still bounding stuck-target
	// detection." Named with the canonical FSM-phase identifier
	// (WAIT_MASTER_ACK) which matches the spec-layer state name; the
	// repo's protocol-layer terminology gate exempts these.
	FrameAtomicV8WaitMasterAckTimeout = 200 * time.Millisecond

	// FrameAtomicV8WaitSlaveAckTimeout symmetrical to
	// WaitMasterAckTimeout for the initiator's ACK/NACK back to the
	// target after the target's response CRC. Per v6 §3, same 200 ms
	// budget.
	FrameAtomicV8WaitSlaveAckTimeout = 200 * time.Millisecond

	// FrameAtomicV8WaitTerminatorSynTimeout is the deadline for the
	// final AUTO-SYN that terminates a successful telegram. Per v6 §3:
	// "100 ms (allows up to two missed AUTO-SYN slots before declaring
	// abandon)."
	FrameAtomicV8WaitTerminatorSynTimeout = 100 * time.Millisecond

	// FrameAtomicV8ArbitratingTimeout is the deadline for grant
	// resolution between the STARTED event and the first echo byte.
	// Per v6 §3: 50 ms.
	FrameAtomicV8ArbitratingTimeout = 50 * time.Millisecond

	// FrameAtomicV8EscapePendingTimeout is the wall-clock cap on the
	// AA-aware escape decoder's ESCAPE_PENDING state. Per v8 §1.3:
	// "explicit 32 ms hard cap." Beyond this, the decoder emits
	// nothing, admin-logs, and re-processes the current byte in
	// NORMAL state. Equivalent constraint to
	// MaxAaAbsorptionsPerEscapePair; whichever fires first.
	FrameAtomicV8EscapePendingTimeout = 32 * time.Millisecond
)

// Count-bounded budgets from the v8 telegram FSM.
const (
	// FrameAtomicV8MaxAaAbsorptionsPerEscapePair is the maximum number
	// of consecutive 0xAA bytes the escape decoder will absorb between
	// the 0xA9 lead and the completion byte (0x00 or 0x01). Per v8 §5
	// and invariant I4: 8 absorptions. Beyond this, the decoder
	// declares escape failure.
	FrameAtomicV8MaxAaAbsorptionsPerEscapePair = 8

	// FrameAtomicV8MaxNackRetxPerPhase is the eBUS V1.3.1
	// single-retransmit cap per phase. Per v6 §3 and invariant I6,
	// master_retx_count and slave_retx_count are independently
	// bounded by this value. The existing 2-attempt inner loop in
	// sendInitiatorTelegram (see bus.go ~line 714) already enforces
	// this for the initiator phase.
	FrameAtomicV8MaxNackRetxPerPhase = 1
)
