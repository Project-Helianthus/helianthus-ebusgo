//go:build !tinygo

package transport

import (
	"errors"
	"fmt"
	"sync/atomic"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

func (t *ENHTransport) fillPendingLocked() error {
	if err := t.setReadDeadline(); err != nil {
		return t.mapReadError(err)
	}

	bytesRead, err := t.conn.Read(t.buffer)
	if err != nil {
		if isTimeout(err) {
			t.resetENHParserLocked() // EG9: clear any pending byte1 from partial frame
			// Close async arbitration window on read timeout — if STARTED/
			// FAILED didn't arrive, the RequestStart caller should treat
			// this as a timed-out arbitration. Leaving awaitingStart set
			// would silently drop RECEIVED bytes on subsequent reads.
			//
			// F-23 (Codex bot r5 on #154): if awaitingStart was true
			// at the moment of timeout, the decoder may have a stale
			// 0xA9 lead from a discarded pre-grant byte. Arbitration
			// abort is a Layer-1 boundary; reset the decoder so the
			// next read does not pair its first byte with the stale
			// lead. Pure passive timeouts (awaitingStart=false)
			// preserve decoder state — a passive listener may
			// legitimately await pair completion on the next read.
			if t.awaitingStart.Load() {
				t.escDecoder.Reset()
			}
			t.awaitingStart.Store(false)
		}
		return t.mapReadError(err)
	}
	if bytesRead == 0 {
		return nil
	}

	// EG8/EG34: Parse now returns valid messages alongside the first error.
	// Process all returned messages BEFORE checking the error so that valid
	// bytes parsed before a corrupt byte are not lost.
	msgs, parseErr := t.parseObservedLocked(t.buffer[:bytesRead])
	for _, msg := range msgs {
		switch msg.Command {
		case ENHResReceived:
			t.observeReceivedLocked(msg)
		case ENHResStarted:
			t.observeStartedLocked(msg)
		case ENHResFailed:
			t.observeFailedLocked(msg)
		case ENHResInfo:
			// INFO responses are consumed by RequestInfo's dedicated read path.
			// Unsolicited INFO frames in the steady-state read are safely ignored.
		case ENHResResetted:
			if stop, resetErr := t.fillPendingResettedLocked(msg); stop {
				return resetErr
			}
		}
	}
	if parseErr != nil {
		return t.fillPendingParseErrorLocked(parseErr, msgs)
	}
	return nil
}

// fillPendingResettedLocked handles one RESETTED in the steady-state read.
// stop reports that the rest of the batch is not processed; err is then the
// value fillPendingLocked returns. Caller must hold readMu.
func (t *ENHTransport) fillPendingResettedLocked(msg ENHMessage) (stop bool, err error) {
	// Always surface the reset boundary exactly once, regardless of
	// whether the transport performs a TCP reconnect
	// (XR_ENH_RESETTED_AlwaysSurfacesBoundary). The delivery channel
	// differs between modes:
	//   dialFunc != nil: reconnectLocked clears pendingEvents, so
	//     the boundary is signaled via t.resets++ (ReadEvent converts
	//     this into one StreamEventReset).
	//   dialFunc == nil: no reconnect, pendingEvents is preserved,
	//     so we queue one StreamEventReset directly.
	if t.dialFunc != nil {
		if reconnErr := t.reconnectLocked(); reconnErr != nil {
			return true, fmt.Errorf("enh adapter reset during read, reconnect failed: %w", ebuserrors.ErrTransportClosed)
		}
		t.resets++
		// Reconnected to fresh TCP — remaining msgs in this batch
		// were parsed from the old stream and are stale.
		return true, nil
	}
	// Adapter-direct mode: queue one StreamEventReset for ReadEvent
	// consumers using the control-event path (never dropped under
	// byte flood). Remaining msgs in the same batch continue to be
	// processed — bus-level data after RESETTED is still valid.
	// Clear post-grant pre-echo window: any in-flight arbitration
	// correlation is invalidated by the adapter reset.
	t.closePostGrantPreEchoWindow()
	// F-23 (batch-19, Codex bot P1 on PR-1): the adapter-
	// direct RESETTED path does NOT call resetStateLocked()
	// (pendingEvents must survive to deliver the boundary
	// event, and the parser stays alive because bus-level
	// data after RESETTED is valid). But the escape decoder
	// MUST be reset here: a 0xA9 lead consumed from the pre-
	// RESETTED stream would otherwise pair with the first
	// post-RESETTED byte. Layer-1 boundary at the adapter
	// invalidates in-flight wire-stream state.
	t.escDecoder.Reset()
	t.appendControlEventLocked(StreamEvent{Kind: StreamEventReset, Data: msg.Data})
	return false, nil
}

// fillPendingParseErrorLocked handles the parse error of one steady-state
// read after every valid message of the batch (msgs) was processed, and
// returns the value fillPendingLocked returns. Caller must hold readMu.
func (t *ENHTransport) fillPendingParseErrorLocked(parseErr error, msgs []ENHMessage) error {
	if errors.Is(parseErr, ebuserrors.ErrInvalidPayload) {
		// Parser desync: orphan byte2, missing byte2, or unknown command.
		// Reset parser to re-synchronize on the next valid byte1.
		t.resetENHParserLocked()
		// F-23 (batch-19, Codex bot P-r3): protocol fault on the
		// ENH frame layer invalidates wire-stream interpretation.
		// Wipe in-flight escape state alongside the parser so a
		// stranded pre-fault 0xA9 lead can't falsely pair with
		// the first post-resync byte.
		t.escDecoder.Reset()
		// Always close the async arbitration window on a parse error —
		// a malformed frame before STARTED/FAILED arrives means the
		// START response is compromised and would never be recognized.
		// Leaving awaitingStart=true would silently drop all subsequent
		// RECEIVED bytes. Clear unconditionally, not only when observed
		// set at this specific moment.
		wasAwaiting := t.awaitingStart.Load()
		t.awaitingStart.Store(false)
		// During async arbitration, surface the error immediately — the
		// RequestStart caller is waiting for a crisp STARTED/FAILED
		// outcome, not a parse error delayed behind a byte backlog.
		if wasAwaiting {
			return parseErr
		}
		// Steady-state read: if valid messages were queued in this batch,
		// defer the error so callers drain the queue first (no lost
		// data), then surface the explicit protocol error on a subsequent
		// call (XR_ENH_UnknownCommand_LivePath_ExplicitError).
		if len(msgs) > 0 {
			t.deferredErr = parseErr
			return nil
		}
		return parseErr
	}
	return parseErr
}

// observeReceivedLocked handles one RECEIVED symbol: the arrival filters
// (async START window and its expiry, pre-grant wire SYN forwarding, escape
// decode, post-grant pre-echo window and its expiry), then the queueing of
// what passes them. The steady-state read (fillPendingLocked) and the INFO
// exchange (RequestInfo) share it. Caller must hold readMu.
func (t *ENHTransport) observeReceivedLocked(msg ENHMessage) {
	// F-23 (batch-19, Codex bot r6 on #154): handle the
	// awaitingStart deadline-expiry boundary BEFORE feeding
	// the escape decoder. Otherwise a pre-grant 0xA9 lead
	// that was dropped while the window was active would
	// pair with the FIRST post-expiry byte inside the
	// decoder, emitting a false escape-decoded byte (or
	// faulting on an invalid pair) instead of letting the
	// boundary byte through cleanly.
	//
	// Two paths through awaitingStart:
	//
	//   (a) Window still active: drop emission AND feed
	//       decoder so state stays coherent across the
	//       discarded-traffic stream (round-2 invariant).
	//
	//   (b) Window expired at the moment THIS byte arrived:
	//       Layer-1 abort — wipe arbitration state INCLUDING
	//       the escape decoder, then fall through to feed
	//       the byte fresh as the start of post-arbitration
	//       traffic.
	if t.awaitingStart.Load() {
		if t.arbitrationWindowExpired() {
			t.escDecoder.Reset()
			t.awaitingStart.Store(false)
			t.arbitrationDeadline.Store(0)
			// Fall through to the normal decode path below.
		} else {
			// F-38 (batch-33, iter16, 2026-05-15): forward wire
			// SYN bytes to upstream consumers even during
			// awaitingStart. Suppress non-SYN bytes only.
			//
			// ebusd's bus state machine has SYN_TIMEOUT = 51 ms
			// (upstream `src/lib/ebus/protocol.h`). When wire SYNs
			// are suppressed for >51 ms during a gateway-pending
			// START, ebusd's bs_ready→bs_skip transition fires from
			// its receive-timeout. When ENH_RES_STARTED finally
			// reaches ebusd, its m_state is bs_skip → "arbitration
			// won in invalid state skip", and the grant is dropped
			// silently.
			//
			// Wire SYNs are bus-idle markers — they're safe and
			// even essential to forward to ENH clients during
			// pending. Other wire bytes (data of unrelated foreign
			// initiator frames) are still suppressed so the
			// consumer doesn't see them as gateway-pending
			// arbitration results.
			decoded, ok, wasEscaped := t.feedEscapeDecoderLocked(msg.Data)
			if ok && decoded == ebusSymbolSyn && !wasEscaped {
				// PR #155 P1 fix: emit as StreamEventWireSyn so
				// the active sender's ReadByte echo-wait skips it
				// instead of consuming the pre-grant SYN as the
				// first echo (which sendRawWithEcho's collision
				// guard would reject as unexpected wire SYN).
				t.queueObservedSymbolLocked(msg.Data, StreamEvent{Kind: StreamEventWireSyn, Byte: ebusSymbolSyn}, true)
			}
			return
		}
	}
	t.resetPendingEscapeBeforeEmissionLocked()
	// F-23 (batch-19): feed the escape decoder so logical
	// (unescaped) bytes reach consumers. The SYN test on
	// the decoded value uses the LOGICAL byte + WasEscaped
	// flag so escape-decoded data 0xAA (WasEscaped=true) is
	// never mistaken for an idle bus SYN.
	decoded, ok, wasEscaped := t.feedEscapeDecoderLocked(msg.Data)
	if !ok {
		// Escape lead, absorbed 0xAA or decode-fault symbol: no
		// logical item, but the observed symbol stays visible to
		// raw-capture readers immediately (a raw ENH client waits
		// for the lead's echo before it sends the suffix).
		t.queueObservedSymbolLocked(msg.Data, StreamEvent{}, false)
		return
	}
	// Post-grant pre-echo window: suppress idle SYN bytes that
	// arrive between arbitration grant and the first real echo
	// of our write. The first non-SYN byte closes the window so
	// later legitimate 0xAA data bytes are delivered normally.
	// The deadline closes the window if the first real echo
	// happens to be 0xAA (would otherwise be suppressed forever).
	// See postGrantPreEcho field doc.
	if t.postGrantPreEcho.Load() {
		if t.postGrantPreEchoExpired() {
			t.closePostGrantPreEchoWindow()
			// F-XX (batch-22 Attack 2 instrumentation):
			// expiry-path close — distinct from first-echo
			// close. See postGrantWindowExpiredCount field doc.
			t.postGrantWindowExpiredCount.Add(1)
			// Fall through: deliver this byte.
		} else if decoded == ebusSymbolSyn && !wasEscaped {
			// Idle bus SYN — suppress. An escape-decoded
			// logical 0xAA (WasEscaped=true) is user payload
			// from a frame, not an idle bus marker, and
			// MUST pass through.
			return
		} else {
			t.closePostGrantPreEchoWindow()
		}
	}
	t.queueObservedSymbolLocked(msg.Data, StreamEvent{Kind: StreamEventByte, Byte: decoded, WasEscaped: wasEscaped}, true)
}

// observeStartedLocked handles one STARTED. The steady-state read and the
// INFO exchange share it. Caller must hold readMu.
func (t *ENHTransport) observeStartedLocked(msg ENHMessage) {
	// If an async arbitration window is open, only a STARTED that
	// matches the expected initiator is OUR grant. STARTED with a
	// different initiator means another device won arbitration (or
	// a stale response from a prior session); keep the window open
	// and let the arbitration deadline / real STARTED handle closure.
	// Without this check, a mismatched STARTED would drop the
	// pre-grant filter and subsequent RECEIVED bytes would be
	// queued as if arbitration was granted — false echoes.
	//
	// Control events MUST NEVER be dropped — observers still see
	// the STARTED via the never-drop path so they can correlate.
	if t.awaitingStart.Load() && byte(t.awaitingStartInitiator.Load()) != msg.Data {
		// Mismatch — keep awaitingStart open, queue event only.
		t.appendControlEventLocked(StreamEvent{Kind: StreamEventStarted, Data: msg.Data})
		return
	}
	// Matching STARTED (or no window open) — close arbitration
	// window, open post-grant pre-echo window, queue event.
	t.awaitingStart.Store(false)
	t.arbitrationDeadline.Store(0)
	// F-23 (Codex bot r7 on #154): pre-grant RECEIVED bytes
	// were fed through the escape decoder during the
	// awaitingStart window. If a 0xA9 lead arrived and the
	// pair-completion byte did NOT arrive before STARTED,
	// the decoder retains escape=true. The first post-grant
	// echo (our own write echoing back) would pair with
	// that stale lead, mis-decoding it as a payload
	// 0xA9/0xAA or faulting as an invalid pair. Arbitration
	// completion is a Layer-1 boundary — the discarded
	// traffic's escape state is gone, no completion will
	// arrive from it. Reset BEFORE the post-grant window
	// opens so our echo path starts with a clean decoder.
	t.escDecoder.Reset()
	t.openPostGrantPreEchoWindow()
	// Control events MUST NEVER be dropped — gateway/adaptermux
	// wait for exactly one STARTED or FAILED per arbitration cycle.
	// If the queue is full of byte events, evict the oldest byte
	// events to make room. Control event ordering is preserved.
	t.appendControlEventLocked(StreamEvent{Kind: StreamEventStarted, Data: msg.Data})
}

// observeFailedLocked handles one FAILED. The steady-state read and the
// INFO exchange share it. Caller must hold readMu.
func (t *ENHTransport) observeFailedLocked(msg ENHMessage) {
	// FAILED = arbitration lost; no echo will follow. Clear
	// postGrantPreEcho (defensively) and do not open the window
	// (it would suppress legit idle SYNs from the bus during
	// collision backoff).
	t.awaitingStart.Store(false)
	t.arbitrationDeadline.Store(0)
	t.closePostGrantPreEchoWindow()
	// F-23 (Codex bot r7 on #154): symmetric to STARTED —
	// arbitration completion (whether grant or loss) closes
	// the discarded-traffic window. Any stranded 0xA9 lead
	// from a dropped pre-grant byte must be cleared so
	// subsequent reads (passive listen, or another
	// arbitration attempt) start with a clean decoder.
	t.escDecoder.Reset()
	t.appendControlEventLocked(StreamEvent{Kind: StreamEventFailed, Data: msg.Data})
}

// appendControlEventLocked queues a control event (STARTED/FAILED/Reset)
// that MUST NOT be silently dropped. If the pendingEvents cap is reached,
// the oldest evictable entry (StreamEventByte or StreamEventWireSyn) is
// dropped to make room. Control events preserve ordering relative to
// each other; byte ordering is preserved among non-evicted bytes.
// Caller must hold readMu.
//
// PR #155 P2 (Codex review): StreamEventWireSyn is explicitly lossy/
// capped backlog (idle bus markers fed during the awaitingStart window
// for downstream bus-reconstructor consumers). Under a SYN flood that
// fills pendingEvents alongside an earlier STARTED/FAILED/RESETTED,
// the next real control event must NOT evict that earlier control
// boundary — instead it must drop a passive SYN marker. Including
// WireSyn in the evictable class makes that ordering invariant
// strict.
func (t *ENHTransport) appendControlEventLocked(ev StreamEvent) {
	if len(t.pendingEvents) >= t.pendingCapLocked() {
		// Evict oldest evictable entry first — byte events (data is
		// recoverable; gateway re-reads bus state) or passive wire-SYN
		// markers (explicitly lossy backlog per the kind's contract).
		// If no evictable entry exists (queue is all control events —
		// pathological, e.g. repeated STARTED/FAILED/RESETTED under
		// adapter fault), evict the oldest control event. This
		// preserves the bounded-backpressure guarantee strictly while
		// keeping the most recent control event (always more relevant
		// than a stale one from an earlier arbitration cycle).
		//
		// A raw-capture item is evictable data as well. It always precedes
		// the logical item it completed, so the scan meets it first and
		// evicts both together: the raw and logical views never disagree
		// about which observed symbol was lost.
		evicted := false
		for i, existing := range t.pendingEvents {
			if existing.Kind == StreamEventByte || existing.Kind == StreamEventWireSyn || existing.Kind == streamEventRawSymbol {
				n := 1
				if existing.Kind == streamEventRawSymbol && existing.Data == rawSymbolCompletesNext && i+1 < len(t.pendingEvents) {
					n = 2
				}
				t.pendingEvents = append(t.pendingEvents[:i], t.pendingEvents[i+n:]...)
				evicted = true
				break
			}
		}
		if !evicted {
			// All control events — drop the oldest to bound memory.
			t.pendingEvents = t.pendingEvents[1:]
		}
	}
	t.pendingEvents = append(t.pendingEvents, ev)
}

// resetPendingEscapeBeforeEmissionLocked consumes any writer-side request to
// reset the escape decoder before the next non-dropped byte is fed. This is
// needed for RequestStart write-failure rollback: RequestStart opens the async
// arbitration window before conn.Write, but it cannot safely reset escDecoder
// directly on write failure because it does not hold readMu.
func (t *ENHTransport) resetPendingEscapeBeforeEmissionLocked() {
	if t.escapeResetPending.Swap(false) {
		t.escDecoder.Reset()
	}
}

// feedEscapeDecoderLocked advances the eBUS escape decoder by one
// wire byte WITHOUT making an emission decision. Returns the decoded
// logical byte and its WasEscaped flag when a logical symbol is
// ready (ok=true), or ok=false while accumulating an escape pair,
// absorbing AA-injection mid-pair, or dropping a fault. Caller must
// hold readMu.
//
// This primitive MUST be called on every wire byte the transport
// observes, including bytes that are dropped by application-layer
// suppression (awaitingStart pre-grant traffic, post-grant pre-echo
// SYN). Skipping the decoder on a dropped byte would leave a prior
// 0xA9 lead stranded, and the next non-dropped byte would falsely
// complete the stale pair (Codex bot P2 on PR-1: discarded-byte
// discontinuity preserving pending escape).
//
// Phase 1 Step B1 (frame-atomic-visibility v8 §1.3 / §5, invariant
// I4): the underlying decoder is now the v8 AA-aware version with
// bounded AA-injection absorption (up to
// MaxAaAbsorptionsPerEscapePair) and a wall-clock cap on the pending
// state (EscapePendingTimeout). The StreamEvent contract is
// preserved — callers see exactly the same `(decoded, ok,
// wasEscaped)` triple. The v8 additions are surfaced via
// transport-level admin counters:
//
//   - escapeAaAbsorbedTotal — every absorbed mid-pair 0xAA
//     increments this (per-byte).
//   - escapePendingTimeoutTotal — every 32 ms wall-clock cap hit.
//     The orphaned 0xA9 is dropped; the current byte is
//     re-processed in NORMAL state and emitted if it produces a
//     decoded byte (data-preserving recovery).
//   - escapeRecoveryTotal — 0xA9 followed by a byte that is neither
//     0x00/0x01 nor an absorbable 0xAA. The 0xA9, absorbed AAs,
//     AND the offending byte are all dropped. Increments
//     decodeFaultTotal too (subset).
//   - escapeBudgetExhaustedTotal — over-budget AA after 8
//     absorptions. The 0xA9, 8 absorbed AAs, AND the over-budget
//     AA are all dropped. Increments decodeFaultTotal too (subset).
func (t *ENHTransport) feedEscapeDecoderLocked(raw byte) (decoded byte, ok bool, wasEscaped bool) {
	// Snapshot the in-budget AA count BEFORE Feed so we can
	// distinguish "AA absorbed this call" from "AA absorbed earlier".
	absorbedBefore := t.escDecoder.AbsorbedCount()

	var admin AdminEvent
	decoded, ok, wasEscaped, admin = t.escDecoder.Feed(raw, t.escapeNow())

	// Account for any AA absorbed by this specific Feed call. The
	// absorber increments AbsorbedCount when a 0xAA arrives mid-pair
	// within budget, and resets to 0 on any exit from pending. So
	// the per-call delta is +1 if we absorbed; otherwise 0 or
	// negative-on-exit (which we map to 0).
	if delta := t.escDecoder.AbsorbedCount() - absorbedBefore; delta > 0 {
		t.escapeAaAbsorbedTotal.Add(uint64(delta))
	}

	if applyEscapeAdminEvent(
		admin,
		&t.escapePendingTimeoutTotal,
		&t.escapeRecoveryTotal,
		&t.escapeBudgetExhaustedTotal,
		&t.decodeFaultTotal,
	) {
		// Drop-everything fault path (Recovery or BudgetExhausted) —
		// emission suppressed regardless of what the decoder returned
		// for this byte. The decoder cleared its in-flight state
		// before returning, so subsequent bytes resume cleanly.
		return 0, false, false
	}
	// AdminEventNone or AdminEventEscapePendingTimeout:
	// data-preserving paths. For Timeout, the current byte was
	// re-processed in NORMAL state by the decoder and may have
	// produced a decoded byte (`decoded`, `ok`, `wasEscaped` reflect
	// that re-processing); the counter was already bumped inside
	// applyEscapeAdminEvent.
	return decoded, ok, wasEscaped
}

// applyEscapeAdminEvent routes a v8 escape-decoder admin event to
// the four transport-level counters and reports whether the caller
// must drop the current byte's emission. Extracted from
// feedEscapeDecoderLocked so the routing logic is directly unit-
// testable without standing up a full ENHTransport (per Codex
// round-3 review on PR #165 — "visually parallel wiring is not
// executable coverage").
//
// Returns dropEmission=true when admin.Kind indicates a drop-
// everything fault (AdminEventEscapeRecovery or
// AdminEventEscapeBudgetExhausted). Returns false for AdminEventNone
// (no event) and AdminEventEscapePendingTimeout (data-preserving —
// the current byte was re-processed in NORMAL state by the decoder
// and the caller should emit whatever the decoder returned).
//
// The function is allocation-free and operates only on the four
// atomic counter pointers passed in; it does not depend on any
// ENHTransport state, making it pure-function testable.
func applyEscapeAdminEvent(
	admin AdminEvent,
	pendingTimeoutCtr, recoveryCtr, budgetExhaustedCtr, decodeFaultCtr *atomic.Uint64,
) (dropEmission bool) {
	switch admin.Kind {
	case AdminEventEscapePendingTimeout:
		// 32 ms cap hit. The decoder already re-processed the
		// current byte in NORMAL state; the caller emits whatever
		// the decoder returned. Surface the timeout via its own
		// counter — NOT a fault (decodeFaultCtr stays put).
		pendingTimeoutCtr.Add(1)
		return false
	case AdminEventEscapeRecovery:
		// Invalid second byte. Drop the 0xA9, any absorbed AAs, AND
		// the offending byte. Fault.
		recoveryCtr.Add(1)
		decodeFaultCtr.Add(1)
		return true
	case AdminEventEscapeBudgetExhausted:
		// AA-injection budget exhausted. Drop the 0xA9, the 8
		// absorbed AAs, AND the over-budget AA. Fault.
		budgetExhaustedCtr.Add(1)
		decodeFaultCtr.Add(1)
		return true
	}
	// AdminEventNone — no admin event, nothing to do.
	return false
}

// pendingCapLocked is the bound on pendingEvents. Raw capture queues up to
// two items per observed symbol (the raw item and the logical item it
// completed), so its bound is doubled to keep the per-symbol capacity of the
// logical-only stream. Caller must hold readMu.
func (t *ENHTransport) pendingCapLocked() int {
	if t.rawSymbolItems {
		return 2 * maxPendingEvents
	}
	return maxPendingEvents
}

// queueObservedSymbolLocked records one RECEIVED wire symbol that passed the
// caller's arrival filters together with the logical item it completed
// (hasLogical). Without raw capture this is exactly the logical emission the
// read paths performed before raw capture existed: a StreamEventByte or
// StreamEventWireSyn subject to maxPendingEvents, and nothing for a symbol
// that completed no logical item. With raw capture the raw item and its
// logical item are queued as one unit; when the bounded queue cannot hold
// both, both are dropped, so the two views stay consistent about every
// observed symbol. Caller must hold readMu and must already have advanced
// the escape decoder for this symbol.
func (t *ENHTransport) queueObservedSymbolLocked(symbol byte, logical StreamEvent, hasLogical bool) {
	if !t.rawSymbolItems {
		if !hasLogical {
			return
		}
		if logical.Kind == StreamEventWireSyn {
			t.appendWireSynLocked()
			return
		}
		t.appendDecodedByteLocked(logical.Byte, logical.WasEscaped)
		return
	}
	// A symbol that completes no logical item is an escape lead. Its suffix
	// will need two slots (raw plus logical), so the lead is only accepted
	// when the queue can also hold that completion.
	need := 3
	if hasLogical {
		need = 2
	}
	if len(t.pendingEvents)+need > t.pendingCapLocked() {
		return
	}
	raw := StreamEvent{Kind: streamEventRawSymbol, Byte: symbol}
	if hasLogical {
		raw.Data = rawSymbolCompletesNext
	}
	t.pendingEvents = append(t.pendingEvents, raw)
	if hasLogical {
		t.pendingEvents = append(t.pendingEvents, logical)
	}
}

// appendDecodedByteLocked emits a fully-decoded logical byte to
// pendingEvents subject to the maxPendingEvents cap. Caller must hold
// readMu and is responsible for having advanced the escape decoder
// (via feedEscapeDecoderLocked) for THIS byte. Control events still
// bypass the cap via appendControlEventLocked.
func (t *ENHTransport) appendDecodedByteLocked(decoded byte, wasEscaped bool) {
	if len(t.pendingEvents) < maxPendingEvents {
		t.pendingEvents = append(t.pendingEvents, StreamEvent{
			Kind:       StreamEventByte,
			Byte:       decoded,
			WasEscaped: wasEscaped,
		})
	}
}

// appendWireSynLocked emits a passive wire-SYN marker (StreamEventWireSyn)
// during the awaitingStart window so ReadEvent consumers downstream
// (gateway adaptermux → ebusd) observe bus-idle cadence and avoid
// triggering their receive-state-machine timeouts. Critically, the
// non-Byte kind makes ReadByte SKIP this event — the active sender's
// echo-wait drain never sees pre-grant SYNs and so cannot mistake them
// for the first echo. Subject to maxPendingEvents cap to prevent
// unbounded growth on a sustained pre-grant window.
//
// F-38-fix (PR #155 P1, 2026-05-15): replaces the original F-38 use of
// appendDecodedByteLocked which pollutes the echo-drain path.
func (t *ENHTransport) appendWireSynLocked() {
	if len(t.pendingEvents) < maxPendingEvents {
		t.pendingEvents = append(t.pendingEvents, StreamEvent{
			Kind: StreamEventWireSyn,
			Byte: ebusSymbolSyn,
		})
	}
}
