//go:build !tinygo

package transport

import (
	"fmt"
	"net"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// InitResult contains the outcome of an ENH INIT handshake.
type InitResult struct {
	Features  byte // Adapter's confirmed features byte from RESETTED response.
	Confirmed bool // True if adapter sent RESETTED; false on timeout (unconfirmed).
}

// Init performs an ENH initialization handshake by sending ENHReqInit(features)
// and waiting for ENHResResetted(features).
//
// Returns the adapter's confirmed features byte from the RESETTED response.
// Any RESETTED frames observed later will reset the local parser and echo state.
func (t *ENHTransport) Init(features byte) (byte, error) {
	if t.closed.Load() {
		return 0, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()
	return t.initLocked(features)
}

// InitWithResult performs an ENH INIT handshake and returns detailed result.
// When Confirmed is false, the adapter did not respond with RESETTED within
// the timeout window — the connection may still be usable but optional
// features (INFO queries, etc.) should not be assumed available.
func (t *ENHTransport) InitWithResult(features byte) (InitResult, error) {
	if t.closed.Load() {
		return InitResult{}, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()
	return t.initWithResultLocked(features)
}

// initWithResultLocked performs the INIT handshake returning a rich result.
// Caller must hold readMu.
func (t *ENHTransport) initWithResultLocked(features byte) (InitResult, error) {
	t.writeMu.Lock()
	err := t.initSendLocked(features)
	t.writeMu.Unlock()
	if err != nil {
		return InitResult{}, err
	}
	return t.initRecvResultLocked()
}

// initSendLocked writes the INIT request frame. Caller must hold writeMu.
func (t *ENHTransport) initSendLocked(features byte) error {
	seq := EncodeENH(ENHReqInit, features)
	written := 0
	for written < len(seq) {
		if err := t.setWriteDeadline(); err != nil {
			return t.mapWriteError(err)
		}
		n, err := t.conn.Write(seq[written:])
		written += n
		if err != nil {
			return t.mapWriteError(err)
		}
		if n == 0 {
			break
		}
	}
	if written != len(seq) {
		return ebuserrors.ErrInvalidPayload
	}
	return nil
}

// initLocked performs the INIT handshake. Caller must hold readMu.
// Returns the features byte from the adapter's RESETTED response.
func (t *ENHTransport) initLocked(features byte) (byte, error) {
	t.writeMu.Lock()
	err := t.initSendLocked(features)
	t.writeMu.Unlock()
	if err != nil {
		return 0, err
	}
	result, err := t.initRecvResultLocked()
	return result.Features, err
}

// initRecvResultLocked reads the INIT response and returns a rich result.
// Caller must hold readMu. When RESETTED is received, Confirmed is true and
// Features contains the adapter's features byte. On timeout without RESETTED,
// Confirmed is false and Features is 0 (fail-open: no error returned).
func (t *ENHTransport) initRecvResultLocked() (InitResult, error) {
	maxWait := t.readTimeout
	if maxWait <= 0 {
		maxWait = 2 * time.Second
	}
	start := time.Now()

	for {
		remaining := maxWait - time.Since(start)
		if remaining <= 0 {
			t.resetENHParserLocked() // Clear stale byte1 from partial frame
			t.deferredErr = nil
			t.awaitingStart.Store(false)
			return InitResult{}, nil
		}
		if t.readTimeout <= 0 || remaining < t.readTimeout {
			if err := t.conn.SetReadDeadline(time.Now().Add(remaining)); err != nil {
				return InitResult{}, t.mapReadError(err)
			}
		} else if err := t.setReadDeadline(); err != nil {
			return InitResult{}, t.mapReadError(err)
		}

		n, err := t.conn.Read(t.buffer)
		if err != nil {
			if isTimeout(err) {
				t.resetENHParserLocked() // Clear stale byte1 from partial frame
				t.deferredErr = nil
				t.awaitingStart.Store(false)
				return InitResult{}, nil
			}
			return InitResult{}, t.mapReadError(err)
		}
		if n == 0 {
			continue
		}

		msgs, parseErr := t.parseObservedLocked(t.buffer[:n])

		// Process valid messages before handling parse error — a valid
		// RESETTED followed by a corrupt trailing byte should succeed.
		for _, msg := range msgs {
			switch msg.Command {
			case ENHResReceived:
				// F-23 (batch-19): feed the escape decoder on every
				// wire byte so logical (unescaped) bytes reach
				// consumers. Init-recv has no application-layer
				// suppression, so we append immediately when the
				// decoder produces a logical byte.
				if decoded, ok, wasEscaped := t.feedEscapeDecoderLocked(msg.Data); ok {
					t.queueObservedSymbolLocked(msg.Data, StreamEvent{Kind: StreamEventByte, Byte: decoded, WasEscaped: wasEscaped}, true)
				} else {
					t.queueObservedSymbolLocked(msg.Data, StreamEvent{}, false)
				}
			case ENHResResetted:
				t.resetStateLocked()
				return InitResult{Features: msg.Data, Confirmed: true}, nil
			case ENHResInfo:
				// Ignore info responses for now; leave any received bytes queued.
			case ENHResErrorEBUS:
				return InitResult{}, fmt.Errorf("enh init ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload)
			case ENHResErrorHost:
				return InitResult{}, fmt.Errorf("enh init host error 0x%02x: %w", msg.Data, ebuserrors.ErrAdapterHostError)
			}
		}
		if parseErr != nil {
			t.resetENHParserLocked()
			// F-23 (batch-19, Codex bot P-r3): protocol fault on the
			// ENH frame layer — orphan byte2 or unknown command. The
			// wire-stream interpretation cannot be trusted across
			// the resync boundary, so wipe any in-flight escape
			// state too. (Timeouts are NOT faults and stay
			// parser-only.)
			t.escDecoder.Reset()
			return InitResult{}, parseErr
		}
	}
}

// reconnectLocked tears down the current TCP connection, dials a fresh one
// via dialFunc, sets TCP_NODELAY, resets the parser, and performs an INIT
// handshake on the new connection. Caller must hold readMu.
//
// If dialFunc is nil (no reconnect capability), falls back to parser-only
// reset for backward compatibility.
func (t *ENHTransport) reconnectLocked() error {
	if t.closed.Load() {
		return fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	if t.dialFunc == nil {
		t.resetStateLocked()
		return nil
	}
	// Dial new connection BEFORE closing the old one. If dial fails,
	// the old conn stays in place so subsequent operations produce
	// timeout errors (retryable) rather than ErrTransportClosed (fatal).
	// If dial succeeds but INIT fails, the new conn is closed and the
	// transport is effectively dead (ErrTransportClosed on all paths).
	newConn, err := t.dialFunc()
	if err != nil {
		return fmt.Errorf("enh reconnect dial: %v: %w", err, ebuserrors.ErrTransportClosed)
	}
	if tcpConn, ok := newConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}
	// Re-check closed after dial — Close() may have run while we were
	// blocked in dialFunc. If so, close the freshly dialed conn and bail.
	if t.closed.Load() {
		_ = newConn.Close()
		return fmt.Errorf("enh transport closed during reconnect: %w", ebuserrors.ErrTransportClosed)
	}
	// Hold writeMu for the entire swap+init-send sequence to prevent
	// concurrent Write() from sending application bytes on the new
	// connection before INIT completes. Lock ordering: readMu (held by
	// caller) before writeMu.
	t.writeMu.Lock()
	t.connMu.Lock()
	// Final closed check under locks — eliminates TOCTOU between the
	// post-dial check and the actual swap. If Close() ran after our
	// earlier check, abort and close the freshly dialed conn.
	if t.closed.Load() {
		t.connMu.Unlock()
		t.writeMu.Unlock()
		_ = newConn.Close()
		return fmt.Errorf("enh transport closed during reconnect: %w", ebuserrors.ErrTransportClosed)
	}
	oldConn := t.conn
	t.conn = newConn
	t.connMu.Unlock()
	_ = oldConn.Close()
	t.resetStateLocked()
	sendErr := t.initSendLocked(0x01)
	if sendErr != nil {
		_ = t.conn.Close()
		t.writeMu.Unlock()
		return fmt.Errorf("enh reconnect init send: %v: %w", sendErr, ebuserrors.ErrTransportClosed)
	}
	t.writeMu.Unlock()

	// Read INIT response (readMu held by caller).
	if _, err := t.initRecvResultLocked(); err != nil {
		// INIT recv failed but newConn is still a valid TCP connection —
		// we successfully sent the INIT request. Keep newConn in place so
		// subsequent operations get timeout errors (retryable) instead of
		// ErrTransportClosed (fatal). Preserve the original error class so
		// shouldRetry handles it correctly (e.g. ErrAdapterHostError is
		// non-retryable, ErrTimeout is transient).
		return fmt.Errorf("enh reconnect init recv: %w", err)
	}
	return nil
}

// Reconnect tears down and re-establishes the underlying TCP connection.
// This is the Reconnectable interface implementation used by the protocol
// layer to recover from dead TCP connections (timeout exhaustion).
//
// Returns ErrTransportClosed if no DialFunc was configured.
func (t *ENHTransport) Reconnect() error {
	if t.closed.Load() {
		return fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()
	if t.dialFunc == nil {
		return fmt.Errorf("enh transport reconnect: no dial function configured: %w", ebuserrors.ErrTransportClosed)
	}
	return t.reconnectLocked()
}

func (t *ENHTransport) resetStateLocked() {
	t.resetENHParserLocked()
	t.pendingEvents = nil
	// Clear any deferred parse error — state discard means previously
	// deferred errors from before this boundary are stale and must not
	// leak into subsequent operations.
	t.deferredErr = nil
	// Clear async arbitration window — any pending RequestStart is
	// invalidated by the reset (connection swap or lifecycle boundary).
	t.awaitingStart.Store(false)
	t.arbitrationDeadline.Store(0)
	// Clear post-grant pre-echo window — lifecycle reset means any
	// in-flight arbitration-echo correlation is broken; next write will
	// open a fresh window if applicable.
	t.closePostGrantPreEchoWindow()
	// F-23 (batch-19): clear any in-flight eBUS escape pair. A mid-pair
	// 0xA9 lead from before the lifecycle boundary is stale; pairing it
	// with whatever byte arrives next from the fresh connection would
	// silently corrupt the post-reset stream.
	t.escapeResetPending.Store(false)
	t.escDecoder.Reset()
}

// DecodeFaultTotal returns the cumulative count of invalid eBUS
// escape sequences observed on the wire. Non-fatal — the decoder
// drops the offending pair (and any absorbed AAs) and resumes on
// the next byte. Exposed for transport-level observability surfaces
// (gateway metrics snapshot, admin endpoints). Safe to call without
// holding readMu.
//
// Phase 1 Step B1 (frame-atomic-visibility v8) widened the
// definition to include BOTH:
//
//   - "0xA9 + invalid second byte" (the legacy fault, now
//     AdminEventEscapeRecovery) — see EscapeRecoveryTotal.
//   - "0xA9 + 8 AAs + over-budget AA" (a new v8 fault,
//     AdminEventEscapeBudgetExhausted) — see
//     EscapeBudgetExhaustedTotal.
//
// The 32 ms wall-clock timeout (AdminEventEscapePendingTimeout) is
// NOT counted here because it is a data-preserving recovery (the
// current byte is re-processed in NORMAL state and may emit
// normally). See EscapePendingTimeoutTotal for that counter.
func (t *ENHTransport) DecodeFaultTotal() uint64 {
	return t.decodeFaultTotal.Load()
}

// EscapePendingTimeoutTotal returns the cumulative count of v8 32 ms
// wall-clock cap hits on the escape decoder's pending state. Per v8
// §1.3: beyond 32 ms the wire is considered genuinely broken, so the
// decoder drops the orphaned 0xA9 plus any absorbed AAs and
// re-processes the current byte in NORMAL state to preserve data
// for next-byte resync. Non-fatal, data-preserving — distinct from
// DecodeFaultTotal which counts drop-everything faults. Safe to call
// without holding readMu.
func (t *ENHTransport) EscapePendingTimeoutTotal() uint64 {
	return t.escapePendingTimeoutTotal.Load()
}

// EscapeRecoveryTotal returns the cumulative count of v8
// AdminEventEscapeRecovery events: a 0xA9 lead followed by a byte
// that is neither 0x00/0x01 nor an absorbable 0xAA-within-budget.
// Subset of DecodeFaultTotal. Exposed for granular operator
// visibility into the fault mix (legacy invalid-pair vs. v8
// budget-exhausted). Safe to call without holding readMu.
func (t *ENHTransport) EscapeRecoveryTotal() uint64 {
	return t.escapeRecoveryTotal.Load()
}

// EscapeBudgetExhaustedTotal returns the cumulative count of v8
// AdminEventEscapeBudgetExhausted events: a 0xA9 lead followed by
// more than MaxAaAbsorptionsPerEscapePair (8) consecutive 0xAA
// bytes. Subset of DecodeFaultTotal. Exposed for granular operator
// visibility — a non-zero rate here suggests adapter buffer
// pathology (sustained AA-injection mid-escape) and may correlate
// with round-9 absorb fires. Safe to call without holding readMu.
func (t *ENHTransport) EscapeBudgetExhaustedTotal() uint64 {
	return t.escapeBudgetExhaustedTotal.Load()
}

// EscapeAaAbsorbedTotal returns the cumulative number of 0xAA bytes
// absorbed mid-escape-pair across all transactions. Each absorption
// is a successful in-budget event (NOT a fault). Per-byte, not
// per-pair — a single pair that absorbed 3 AAs increments this
// counter by 3. Useful for measuring the AA-injection load the v8
// absorber is catching; pair with EscapeBudgetExhaustedTotal to
// detect a budget that is too small. Safe to call without holding
// readMu.
func (t *ENHTransport) EscapeAaAbsorbedTotal() uint64 {
	return t.escapeAaAbsorbedTotal.Load()
}

// PostGrantWindowExpiredCount returns the cumulative count of
// postGrantPreEcho window-close-by-deadline-expiry events (as opposed
// to closure via the first non-SYN echo arrival or lifecycle reset).
// Used by downstream consumers (gateway adaptermux) to correlate
// post-window-close idle 0xAA SYN arrivals with echo_mismatch events
// in the next gateway transaction. Forensic-only signal — no behavior
// implication on its own. Safe to call without holding readMu.
//
// F-XX (batch-22 Attack 2 instrumentation, 2026-05-15): see
// postGrantWindowExpiredCount field doc for full rationale.
func (t *ENHTransport) PostGrantWindowExpiredCount() uint64 {
	return t.postGrantWindowExpiredCount.Load()
}
