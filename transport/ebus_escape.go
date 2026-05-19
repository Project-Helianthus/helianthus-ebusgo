package transport

import (
	"fmt"
	"time"
)

// EbusEscapeDecoder unescapes eBUS byte-stuffing as documented in
// john30/ebusd/docs/enhanced_proto.md:
//
//	wire 0xA9 0x00 → logical 0xA9 (data byte equal to ESC)
//	wire 0xA9 0x01 → logical 0xAA (data byte equal to SYN value)
//	any other byte → passes through unchanged
//
// Phase 1 Step B1 (frame-atomic-visibility v8 §1.3, §5, invariant I4)
// extends the decoder with AA-injection absorption and a wall-clock
// cap on the pending state. The two motivating defects:
//
//   - **AA-injection mid-escape-pair.** When a 0xA9 lead is followed
//     by spurious 0xAA bytes (the adapter buffer leaking AUTO-SYN
//     mid-escape-pair, per the round-9 motivating bug), the decoder
//     silently absorbs up to MaxAaAbsorptionsPerEscapePair (8) of them
//     and continues waiting for the real second byte. Without this,
//     every AA-injection mid-escape would corrupt the decoded byte
//     stream and surface as a phantom PROTOCOL_FAULT downstream.
//
//   - **Hard 32 ms wall-clock cap on ESCAPE_PENDING.** Per v8 §1.3:
//     "Beyond 32 ms, the wire is genuinely broken; better to abandon
//     and let downstream FSM detect protocol fault." When the cap is
//     reached, the decoder drops the orphaned 0xA9 plus any absorbed
//     AAs and re-processes the current byte in NORMAL state to
//     preserve data for next-byte resync.
//
// Per v8 §1.3, a 0xA9 followed by anything OTHER than 0x00 / 0x01 /
// 0xAA-within-budget is treated as protocol corruption. The decoder
// emits NOTHING for the failed escape pair (drops both the 0xA9 and
// the offending byte) and returns to NORMAL state. v6's earlier
// emit-0xA9-as-raw recovery was rejected because the 0xA9 may have
// been pure injection noise; emitting it would invent a byte the
// wire never carried.
//
// The decoder is stateful — fields carry across calls so an escape
// pair split across Read() boundaries decodes correctly. The decoder
// does NOT own the byte stream: callers invoke Feed (or the legacy
// Push) once per wire byte, and Reset() clears in-flight state at
// transport lifecycle boundaries (reconnect, RESETTED frame, surface
// reset).
//
// Concurrency: the decoder is NOT safe for concurrent use. The ENH
// transport invokes it under readMu, which already serializes byte
// ingest. Adding a mutex here would be redundant for that caller and
// strictly worse for other potential callers; expect external
// serialization.
//
// Background (F-23, batch-19, 2026-05-13): before this decoder existed
// the ENH transport claimed via BytesAreUnescaped() to deliver logical
// bytes but actually forwarded wire bytes unchanged. Consumers (passive
// bus tap, reconstructor) saw bare 0xA9 followed by either 0x00 or
// 0x01 — those two-byte sequences leaked into the request/response
// buffers and produced false unexpected_symbol abandons whenever a
// legitimate frame contained a logical 0xA9 or 0xAA byte (Patterns A
// and B in batch-19's verification report). This decoder makes the
// BytesAreUnescaped() contract honest. The v8 additions (AA absorption
// + wall-clock cap) layer on top of that without changing the
// underlying contract.
type EbusEscapeDecoder struct {
	escape         bool
	leadObservedAt time.Time
	absorbedCount  int
}

// MaxAaAbsorptionsPerEscapePair is the maximum number of consecutive
// 0xAA bytes the decoder will absorb between the 0xA9 lead and the
// completion byte (0x00 or 0x01). Per frame-atomic-visibility v8 §5
// and invariant I4. Beyond this count the decoder declares escape
// failure (AdminEventEscapeBudgetExhausted).
//
// This constant MUST match
// `protocol.FrameAtomicV8MaxAaAbsorptionsPerEscapePair` in
// `protocol/frame_atomic_v8_timeouts.go`. The transport package
// cannot import the protocol package (circular dependency), so the
// constant is mirrored here with an explicit cross-reference comment.
const MaxAaAbsorptionsPerEscapePair = 8

// EscapePendingTimeout is the wall-clock cap on the decoder's
// pending state. Beyond this, the decoder declares ESCAPE_PENDING
// timeout (AdminEventEscapePendingTimeout). Per v8 §1.3.
//
// This constant MUST match `protocol.FrameAtomicV8EscapePendingTimeout`
// in `protocol/frame_atomic_v8_timeouts.go`. Same mirroring rationale
// as MaxAaAbsorptionsPerEscapePair.
const EscapePendingTimeout = 32 * time.Millisecond

// AdminEventKind classifies admin-channel events the decoder emits
// when it detects fault or recovery conditions per v8 §1.3 / §5.
// These events live strictly on the proxy admin channel; per v8
// invariant I1 they are NEVER injected into client byte streams.
type AdminEventKind int

const (
	// AdminEventNone means the Feed call produced no admin event.
	AdminEventNone AdminEventKind = iota

	// AdminEventEscapePendingTimeout means the decoder spent more
	// than EscapePendingTimeout in pending state without resolving.
	// The orphaned 0xA9 and any absorbed AAs are dropped; the
	// current byte is re-processed in NORMAL state. Per v8 §1.3.
	AdminEventEscapePendingTimeout

	// AdminEventEscapeRecovery means the decoder saw a byte in
	// pending state that is neither a valid completion byte
	// (0x00 / 0x01) nor an absorbable 0xAA-within-budget. The
	// orphaned 0xA9 and any absorbed AAs are dropped; the current
	// byte is also dropped (treated as continuation of the
	// corruption). Per v8 §5.
	AdminEventEscapeRecovery

	// AdminEventEscapeBudgetExhausted means the decoder saw the
	// (MaxAaAbsorptionsPerEscapePair + 1)-th 0xAA in pending state,
	// exhausting the count-bounded absorption budget. The orphaned
	// 0xA9, the 8 absorbed AAs, AND the over-budget 0xAA are ALL
	// dropped — emit nothing. Per v8 §5 / I4: only the timeout path
	// re-processes the current byte; the count-bounded path drops
	// everything so a raw AUTO-SYN cannot leak into the downstream
	// classifier after declared escape failure.
	AdminEventEscapeBudgetExhausted
)

// String returns a human-readable label for the admin event kind.
// Used in admin-channel logs.
func (k AdminEventKind) String() string {
	switch k {
	case AdminEventNone:
		return "none"
	case AdminEventEscapePendingTimeout:
		return "escape_pending_timeout"
	case AdminEventEscapeRecovery:
		return "escape_decoder_recovery"
	case AdminEventEscapeBudgetExhausted:
		return "escape_budget_exhausted"
	default:
		return "unknown"
	}
}

// AdminEvent captures the per-Feed diagnostic that the decoder
// wishes to surface to the admin channel. Empty (Kind ==
// AdminEventNone) means no event.
type AdminEvent struct {
	Kind     AdminEventKind
	Duration time.Duration // wall-clock duration in pending state, if relevant
	Absorbed int           // number of AAs absorbed before the event, if relevant
}

// Feed consumes one wire byte and returns:
//   - decoded: the decoded logical byte (valid only when ok=true).
//   - ok: true if a logical byte is ready; false while accumulating
//     an escape pair, absorbing an AA-injection, or dropping a
//     fault.
//   - wasEscaped: true if `decoded` came from a wire escape pair
//     (0xA9 0x00 → 0xA9 or 0xA9 0x01 → 0xAA); false for a raw
//     passthrough byte.
//   - admin: a diagnostic event for the admin channel, or
//     AdminEventNone.
//
// Callers MUST pass `now` as the monotonic-clock observation time
// for `raw` (per v8 invariant I0). Tests pass synthetic times. The
// `now` value is used ONLY for the EscapePendingTimeout wall-clock
// cap; any caller that doesn't care about admin-channel observability
// may use the legacy Push method instead.
//
// Feed is allocation-free on the hot path.
func (d *EbusEscapeDecoder) Feed(raw byte, now time.Time) (decoded byte, ok bool, wasEscaped bool, admin AdminEvent) {
	if d.escape {
		return d.feedPending(raw, now)
	}
	return d.feedNormal(raw, now)
}

func (d *EbusEscapeDecoder) feedNormal(raw byte, now time.Time) (byte, bool, bool, AdminEvent) {
	if raw == 0xA9 {
		// Escape lead — enter pending state, remember when the lead
		// arrived for the wall-clock cap. Do not emit yet.
		d.escape = true
		d.leadObservedAt = now
		d.absorbedCount = 0
		return 0, false, false, AdminEvent{}
	}
	// Plain wire byte. WasEscaped=false because no escape pair was
	// used. A wire AUTO-SYN (0xAA) flows through here as
	// (0xAA, wasEscaped=false) — the classifier in v8 §4 uses this
	// provenance to distinguish wire AUTO-SYN from escape-decoded
	// payload 0xAA.
	return raw, true, false, AdminEvent{}
}

func (d *EbusEscapeDecoder) feedPending(raw byte, now time.Time) (byte, bool, bool, AdminEvent) {
	// Wall-clock cap fires before the byte-value branches. Per v8
	// §1.3: "Beyond 32 ms, the wire is genuinely broken; better to
	// abandon and let downstream FSM detect protocol fault." Only
	// honor the cap when `leadObservedAt` is non-zero — a zero time
	// is a legacy signal from the Push wrapper meaning "no clock
	// available, skip wall-clock cap" (preserves Push's
	// no-time-dependence behavior).
	if !d.leadObservedAt.IsZero() {
		elapsed := now.Sub(d.leadObservedAt)
		if elapsed > EscapePendingTimeout {
			absorbed := d.absorbedCount
			d.escape = false
			d.leadObservedAt = time.Time{}
			d.absorbedCount = 0
			// Re-process the current byte in NORMAL state to preserve
			// it for next-byte resync (v8 §1.3 polish commit).
			decoded, ok, wasEscaped, _ := d.feedNormal(raw, now)
			return decoded, ok, wasEscaped, AdminEvent{
				Kind:     AdminEventEscapePendingTimeout,
				Duration: elapsed,
				Absorbed: absorbed,
			}
		}
	}

	switch raw {
	case 0x01:
		// 0xA9 0x01 → logical 0xAA, wasEscaped=true.
		d.escape = false
		d.leadObservedAt = time.Time{}
		d.absorbedCount = 0
		return 0xAA, true, true, AdminEvent{}

	case 0x00:
		// 0xA9 0x00 → logical 0xA9, wasEscaped=true.
		d.escape = false
		d.leadObservedAt = time.Time{}
		d.absorbedCount = 0
		return 0xA9, true, true, AdminEvent{}

	case 0xAA:
		// AA-injection mid-escape-pair. Absorb if budget allows.
		if d.absorbedCount < MaxAaAbsorptionsPerEscapePair {
			d.absorbedCount++
			return 0, false, false, AdminEvent{}
		}
		// Budget exhausted. Per v8 §5 / I4: drop the 0xA9, all
		// absorbed AAs, AND this over-budget AA. Emit NOTHING. Only
		// the timeout path (wall-clock cap above) re-processes the
		// current byte; the count-exhausted path drops everything so
		// an over-budget raw AUTO-SYN cannot leak into the
		// downstream classifier after declared escape failure.
		absorbed := d.absorbedCount
		elapsed := time.Duration(0)
		if !d.leadObservedAt.IsZero() {
			elapsed = now.Sub(d.leadObservedAt)
		}
		d.escape = false
		d.leadObservedAt = time.Time{}
		d.absorbedCount = 0
		return 0, false, false, AdminEvent{
			Kind:     AdminEventEscapeBudgetExhausted,
			Duration: elapsed,
			Absorbed: absorbed,
		}

	default:
		// Malformed escape: byte is neither a valid completion
		// (0x00 / 0x01) nor an absorbable 0xAA-within-budget. Drop
		// the 0xA9 plus absorbed AAs AND drop this byte (treat as
		// continuation of corruption). Emit admin event.
		absorbed := d.absorbedCount
		elapsed := time.Duration(0)
		if !d.leadObservedAt.IsZero() {
			elapsed = now.Sub(d.leadObservedAt)
		}
		d.escape = false
		d.leadObservedAt = time.Time{}
		d.absorbedCount = 0
		return 0, false, false, AdminEvent{
			Kind:     AdminEventEscapeRecovery,
			Duration: elapsed,
			Absorbed: absorbed,
		}
	}
}

// Push consumes one wire byte and returns the decoded result. This
// is the legacy entry point — it forwards to Feed with a zero time,
// which suppresses the wall-clock cap (the AA absorption budget
// still applies). Use Feed directly when you need the wall-clock cap
// and admin-channel observability.
//
// Return values:
//
//	decoded     — the logical byte to emit (valid only when ok=true)
//	ok          — true if a logical byte is ready; false while still
//	              accumulating (the escape lead 0xA9 was just
//	              consumed, an AA-injection was absorbed, or a fault
//	              dropped the current byte)
//	wasEscaped  — true if `decoded` came from a wire escape pair
//	              (0xA9 0x00 → 0xA9 or 0xA9 0x01 → 0xAA); false for a
//	              raw passthrough byte
//	err         — non-nil iff Feed emitted AdminEventEscapeRecovery
//	              or AdminEventEscapeBudgetExhausted (mapped to err
//	              for legacy callers that expect the pre-v8 fault
//	              contract). The decoder clears its escape state
//	              before returning, so the next call resumes cleanly.
//
// On err != nil the caller should count the fault and continue;
// emission of the offending pair/byte is suppressed (ok=false,
// decoded undefined). The next Push resumes clean decoding.
func (d *EbusEscapeDecoder) Push(raw byte) (decoded byte, ok bool, wasEscaped bool, err error) {
	// Pass time.Time{} (zero time) to disable the wall-clock cap.
	// Push has no clock injection point so it cannot enforce the cap
	// honestly — Feed is the v8 entry point that does.
	decoded, ok, wasEscaped, admin := d.Feed(raw, time.Time{})
	switch admin.Kind {
	case AdminEventEscapeRecovery:
		return 0, false, false, fmt.Errorf("ebus escape: 0xA9 0x%02X is not a valid pair (expected 0x00 or 0x01)", raw)
	case AdminEventEscapeBudgetExhausted:
		return 0, false, false, fmt.Errorf("ebus escape: AA-injection budget (%d) exhausted in pending state", MaxAaAbsorptionsPerEscapePair)
	}
	return decoded, ok, wasEscaped, nil
}

// Reset clears any in-flight escape state. Call at every transport
// lifecycle boundary that invalidates an in-progress wire pair:
// reconnect, adapter-surfaced RESETTED frame, or any other state
// discard. Do NOT call on read timeout — a 0xA9 lead may have
// arrived just before the timeout and its pair may legitimately
// arrive on the next read.
func (d *EbusEscapeDecoder) Reset() {
	d.escape = false
	d.leadObservedAt = time.Time{}
	d.absorbedCount = 0
}

// HasPendingEscape reports whether the decoder is mid-pair (a 0xA9
// lead has been consumed and we are awaiting the second byte or
// absorbing AA-injection). Useful for diagnostics and for callers
// that want to flag mid-frame interruption when a transport-level
// boundary fires while the decoder is mid-pair.
func (d *EbusEscapeDecoder) HasPendingEscape() bool {
	return d.escape
}

// AbsorbedCount returns the number of 0xAA bytes absorbed in the
// current pending sequence. Returns 0 when not pending. Exposed for
// testing and admin-channel observability.
func (d *EbusEscapeDecoder) AbsorbedCount() int {
	return d.absorbedCount
}
