// Package protocol — frame-atomic visibility v8 §3 per-phase timeout constants.
//
// These constants are the spec-aligned per-state timeout values for the
// telegram FSM defined in `helianthus-docs-ebus/architecture/adaptermux/
// frame-atomic-visibility-v8.md` §3.
//
// They are DECLARED here so the Step A migration (per v8 §14) can land
// independently as a doc-aligned addition with no behavior change. Actual
// wiring into the active read path happens in Step B, when the telegram
// FSM is extracted into a shared library (per v8 §1.11 and §14 step 1).
//
// Current bus.go does not have per-phase timeouts in the read path; it
// inherits the transport-level read timeout (default 5s on adapter-direct
// transports). Step B will replace per-symbol reads with phase-aware
// `readByteWithDeadline(phase)` calls keyed off these constants.
//
// All values are engineering choices per v8 §1.5 and §3:
//
//   - The eBUS V1.3.1 spec mandates that the target respond within the
//     AUTO-SYN slot (~35–45 ms). The longer values here (e.g., 200 ms for
//     ACK phases) provide tolerance for slow or contended targets without
//     introducing spec violations on our side.
//   - Reducing these values below the documented constants will create
//     spurious aborts on healthy-but-slow targets; increasing them past
//     these constants will mask real stuck-target detection.
//
// The corresponding Prometheus alert (per v8 §1.9) on
// `helianthus_round9_absorb_fired_proxy_mediated_total` must be deployed
// alongside any code path that wires these timeouts in, since the alert
// gates the legacy-fallback verification of v8 §10.
package protocol

import "time"

// FrameAtomicV8 holds the per-phase FSM timeout values from
// frame-atomic-visibility-v8.md §3. They are exported so downstream consumers
// (the proxy in helianthus-ebus-adapter-proxy, the future FSM extraction in
// `protocol/telegram_fsm`) can reference a single source of truth instead of
// duplicating the literals.
//
// Not yet wired into the active bus.go read path. Step B wiring is tracked
// in the migration sequence of frame-atomic-visibility-v8.md §14.
var FrameAtomicV8 = struct {
	// InterByte is the timeout between consecutive bytes within a phase
	// that consumes a known number of bytes (MASTER_HEADER consuming the
	// 5-byte QQ ZZ PB SB NN header, MASTER_DATA consuming NN data bytes,
	// SLAVE_LENGTH consuming the length byte, SLAVE_DATA, SLAVE_CRC). Per
	// v8 §3: "10 ms (conservative; spec minimum 4.17 ms)."
	InterByte time.Duration

	// WaitMasterAck is the deadline for receiving ACK/NACK after the
	// initiator's CRC. Per v8 §1.5: 200 ms is an engineering choice that
	// accommodates targets up to ~5× the AUTO-SYN slot (~35–45 ms).
	WaitMasterAck time.Duration

	// WaitSlaveAck symmetric to WaitMasterAck for the initiator's
	// ACK/NACK back to the target after the target's response CRC.
	WaitSlaveAck time.Duration

	// WaitTerminatorSyn is the deadline for the final AUTO-SYN that
	// terminates a successful telegram. Per v8 §3: "100 ms (allows up
	// to two missed AUTO-SYN slots before declaring abandon)."
	WaitTerminatorSyn time.Duration

	// Arbitrating is the deadline for grant resolution between the
	// `STARTED` event and the first echo byte. Per v8 §3: 50 ms.
	Arbitrating time.Duration

	// EscapePending is the wall-clock cap on the AA-aware escape
	// decoder's ESCAPE_PENDING state. Per v8 §1.3 and I4: 32 ms (= 8 ×
	// τ_wire_byte plus jitter tolerance). Beyond this, the decoder
	// emits nothing, admin-logs, and re-processes the current byte in
	// NORMAL state.
	EscapePending time.Duration

	// MaxAaAbsorptionsPerEscapePair is the count-bounded budget on
	// the AA-aware escape decoder's ESCAPE_PENDING state. Per v8 §5
	// and I4: 8 consecutive 0xAA bytes before declaring escape failure.
	// Equivalent constraint to EscapePending; whichever fires first.
	MaxAaAbsorptionsPerEscapePair int

	// MaxNackRetxPerPhase is the spec V1.3.1 single-retransmit cap
	// per phase. Per v8 §3 and I6, master_retx_count and
	// slave_retx_count are independently bounded by this value. The
	// existing 2-attempt inner loop in sendInitiatorTelegram (see
	// bus.go ~line 714) already enforces this for the master phase.
	MaxNackRetxPerPhase int
}{
	InterByte:                     10 * time.Millisecond,
	WaitMasterAck:                 200 * time.Millisecond,
	WaitSlaveAck:                  200 * time.Millisecond,
	WaitTerminatorSyn:             100 * time.Millisecond,
	Arbitrating:                   50 * time.Millisecond,
	EscapePending:                 32 * time.Millisecond,
	MaxAaAbsorptionsPerEscapePair: 8,
	MaxNackRetxPerPhase:           1,
}
