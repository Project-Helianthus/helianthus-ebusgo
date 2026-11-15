//go:build !tinygo

package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// maxPendingEvents caps the pendingEvents slice to prevent unbounded growth
// when bus traffic floods the transport during INFO or normal read paths.
const maxPendingEvents = 256

// streamEventRawSymbol is a transport-private pendingEvents marker for one
// observed RECEIVED wire symbol. It is queued only after raw capture was
// enabled by ReadEventWithRawSymbol; ReadEvent and ReadByte never return it.
const streamEventRawSymbol StreamEventKind = 0xFF

// rawSymbolCompletesNext in a raw item's Data marks that the immediately
// following pending item is the logical item this wire symbol completed.
const rawSymbolCompletesNext byte = 1

// arbitrationWindowTimeout bounds the async arbitration window opened by
// RequestStart(). If STARTED/FAILED does not arrive within this budget,
// the window is force-closed so subsequent RECEIVED bytes stop being
// dropped as pre-grant traffic. Set generously relative to eBUS wire
// timing (a full arbitration cycle is well under 100ms); 500ms keeps the
// deadline above any legitimate adapter latency while preventing
// permanent starvation on a busy bus where STARTED/FAILED is lost but
// RECEIVED keeps arriving.
const arbitrationWindowTimeout = 500 * time.Millisecond

// postGrantPreEchoTimeout bounds the post-grant pre-echo window opened by
// STARTED. Inside this window, RECEIVED(0xAA) bytes are treated as idle
// SYN from the eBUS wire and suppressed so they don't leak as echoes of
// the bus layer's first write. The window closes on: (a) the first
// non-SYN RECEIVED (the real echo, normal case), or (b) this deadline.
//
// Sizing: 50ms — matches the pre-batch-24 production value. The widening
// to 5s in batch-24 round-5 was reverted in batch-25 hotfix after the live
// Prometheus history (helianthus-ebusgateway PR #634, 2026-05-17) showed:
//
//   - Per-frame echo_mismatch ratio JUMPED from 5-11% to 12-23% on round-5
//     deploy. Absolute echo_mismatch count dropped only because gateway
//     throughput collapsed: passive frame rate fell from 1.0-1.3 c/s to
//     0.0-0.5 c/s (~70% reduction).
//   - Post-revert (binary injected at 05:00 UTC 2026-05-18), per-frame
//     ratio dropped to 2.86%, traffic recovering toward baseline.
//
// Root cause hypothesis: the 5s window's SYN-suppression contract holds
// during the gateway's own bus.Send. When gateway writes a wire SYN (e.g.
// the terminal SYN that ends every M2S frame) the wire echo is a real SYN
// with WasEscaped=false — exactly the predicate that the transport-layer
// suppresses. The window normally closes on the first non-SYN echo so
// subsequent SYN echoes pass, but degenerate paths (FAILED before any
// echo, single-byte broadcasts, idle-grace recovery races) can leave the
// window open across the gateway's own SYN write, blocking its bus.Send
// indefinitely (up to 5s). The 50ms value bounds this to a single
// AUTO-SYN tick and matches the pre-investigation baseline behavior.
//
// Declared as a var (not const) solely to enable a test-only override via
// setPostGrantPreEchoTimeoutForTest. Production code MUST treat this as
// immutable; the override is gated on the _test.go build path and exists
// because two deadline-expiry tests would otherwise need 50ms+ sleeps each.
var postGrantPreEchoTimeout = 50 * time.Millisecond

// ENHTransportOption configures optional ENHTransport behavior.
type ENHTransportOption func(*ENHTransport)

// WithDialFunc provides a dialer callback for mid-session reconnection.
// When set, RESETTED events trigger a full TCP teardown and re-dial+re-INIT
// instead of a parser-only reset. Without it, the transport falls back to
// parser-only reset (backward-compatible).
func WithDialFunc(fn func() (net.Conn, error)) ENHTransportOption {
	return func(t *ENHTransport) { t.dialFunc = fn }
}

// ENHTransport wraps a net.Conn and exposes the RawTransport interface using ENH framing.
//
// Lock ordering: readMu before writeMu. connMu is independent and protects
// only the conn pointer swap — never held during blocking I/O (conn.Close
// is called after connMu.Unlock).
type ENHTransport struct {
	connMu       sync.Mutex // protects conn pointer swap only, not I/O operations
	conn         net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration
	// true for ENH mode: START arbitration already transmits source symbol on wire.
	// false for ENS mode: source symbol must still be written in telegram payload.
	arbitrationSendsSource bool

	// closed is set atomically by Close() to signal terminal state. All public
	// methods check this before acquiring locks to prevent post-Close hangs.
	closed atomic.Bool

	// dialFunc, when non-nil, enables mid-session reconnection on RESETTED.
	// The function should return a fresh net.Conn to the adapter.
	dialFunc func() (net.Conn, error)

	readMu  sync.Mutex
	writeMu sync.Mutex

	parser        ENHParser
	pendingEvents []StreamEvent
	// deferredErr holds a parse error that occurred while valid messages
	// were also produced. The error is surfaced on the next Read*()
	// call after pendingEvents is fully drained, so valid messages are
	// not lost. Accessed only under readMu.
	deferredErr error
	resets      int
	buffer      []byte
	// awaitingStart is set atomically by RequestStart() and cleared when a
	// STARTED/FAILED arrives (or parser state is reset). While set, the
	// async arbitration window is open: RECEIVED bytes received in this
	// window are bus traffic observed before grant, not our echoes — they
	// must not leak into pendingEvents where the protocol layer would
	// consume them as if they were post-grant echoes. Blocking
	// StartArbitration clears pendingEvents after grant, but the async
	// path needs this in-window drop to achieve the same invariant.
	awaitingStart atomic.Bool
	// awaitingStartInitiator holds the initiator byte RequestStart is
	// waiting a grant for. STARTED frames with a different Data are
	// NOT our grant (another device won or a stale response); the async
	// path must keep awaitingStart open until a matching STARTED, FAILED,
	// or the arbitration deadline expires. Stored as uint32 so we can
	// use atomic.Uint32 (byte in low bits). Only valid when
	// awaitingStart.Load() is true.
	awaitingStartInitiator atomic.Uint32
	// arbitrationDeadline is the absolute time after which awaitingStart
	// is force-closed even if no STARTED/FAILED arrives. Prevents permanent
	// starvation on a busy bus where RECEIVED keeps arriving (no read
	// timeout) but the arbitration response is lost. Stored as Unix nanos
	// for lock-free access; 0 = no deadline (window closed).
	arbitrationDeadline atomic.Int64
	// postGrantPreEcho is set to true when STARTED/FAILED closes the
	// arbitration window. While set, fillPendingLocked suppresses any
	// RECEIVED(0xAA) idle SYN bytes that arrive on the wire between the
	// arbitration grant and the first real echo from our next write.
	// Cleared on: (a) the first non-SYN RECEIVED byte (the real echo,
	// normal case), (b) postGrantPreEchoDeadline expiry, (c) parser
	// reset / reconnect / error lifecycle boundaries.
	//
	// Root cause: eBUS wire goes idle (0xAA SYN) after STARTED is sent
	// by the adapter; our bus layer then writes the first byte (DST),
	// but the adapter may emit RECEIVED(0xAA) for idle ticks before
	// echoing our write. sendRawWithEcho reads the queued 0xAA thinking
	// it's the echo of DST → echo mismatch error. Suppressing idle SYN
	// during this brief window eliminates the mismatch.
	//
	// Deadline covers the degenerate case where the first legit echo
	// happens to be 0xAA — without it, that echo would be suppressed,
	// the window would never close, and reads would stall. See
	// postGrantPreEchoTimeout for timing rationale.
	postGrantPreEcho         atomic.Bool
	postGrantPreEchoDeadline atomic.Int64

	// postGrantWindowExpiredCount counts the number of times the
	// postGrantPreEcho window closed via DEADLINE EXPIRY (case b in
	// the field doc above), as opposed to closure via first-real-
	// echo arrival (case a) or lifecycle reset (case c). Surfaced
	// via PostGrantWindowExpiredCount() for downstream consumers
	// (gateway mux) to correlate post-window-close SYNs with
	// echo_mismatch events.
	//
	// F-XX (batch-22 Attack 2 instrumentation, 2026-05-15;
	// batch-24 round-5 timeout widening, 2026-05-17):
	// postGrantPreEchoTimeout intentionally bounds the window so a
	// degenerate first-echo-is-0xAA case doesn't stall the read path.
	// After expiry the SYN-suppression contract ends — any subsequent
	// idle 0xAA passes through to the pendingEvents queue and may leak
	// as the next ReadByte caller's echo (the pre_echo_syn root cause
	// Attack 2 targets). This counter measures how often that window
	// closes by expiry, so the gateway can correlate the rate with
	// mux-side echo_mismatch events. Round-5 widened the timeout from
	// 50ms to 5s so the window now covers the full gateway transaction
	// duration; expiry-path closures should become rare and indicate a
	// stuck/long txn rather than ordinary post-grant settling.
	postGrantWindowExpiredCount atomic.Uint64
	// escDecoder unescapes eBUS byte-stuffing on the steady-state byte
	// stream so the BytesAreUnescaped() contract is honest. Accessed
	// only under readMu (every byte-emission site that feeds into it
	// is locked). F-23, batch-19, 2026-05-13. See transport/ebus_escape.go.
	escDecoder EbusEscapeDecoder
	// escapeNow supplies observation time only to escDecoder.Feed.
	// Set at construction, or before reading through the test-only bridge.
	escapeNow func() time.Time
	// rawSymbolItems is set by the first ReadEventWithRawSymbol call. While
	// set, each RECEIVED wire symbol that passes this transport's arrival
	// filters is queued as a private raw item immediately ahead of the
	// logical item it completed, if any. Accessed only under readMu.
	rawSymbolItems bool
	// escapeResetPending is set by writer-side async arbitration aborts that
	// cannot reset escDecoder directly because they do not hold readMu. The
	// next read-side non-dropped byte consumes this flag and resets before
	// feeding that byte, preventing stale pre-grant escape state from crossing
	// the abort boundary.
	escapeResetPending atomic.Bool
	// decodeFaultTotal counts wire 0xA9 leads followed by a byte
	// other than 0x00 or 0x01 (invalid escape pair). Non-fatal: the
	// decoder drops the offending pair and resumes on the next wire
	// byte. Exposed via DecodeFaultTotal() for transport-level
	// observability. Atomic because external readers (gateway metrics
	// snapshot) read it without holding readMu.
	//
	// Phase 1 Step B1 (frame-atomic-visibility v8 §1.3 / §5,
	// invariant I4) — the counter is now incremented by BOTH legacy
	// "0xA9 + invalid second byte" faults (AdminEventEscapeRecovery)
	// AND v8 AA-injection budget exhaustion
	// (AdminEventEscapeBudgetExhausted). The latter is a new fault
	// mode introduced by v8's bounded AA absorption; semantically
	// both indicate "the escape decoder gave up on a pending pair",
	// so they share this single fault counter. The decomposed
	// counters below (escapeBudgetExhaustedTotal,
	// escapeRecoveryTotal) give granular visibility for operators
	// who want to attribute decodeFaultTotal to a specific cause.
	decodeFaultTotal atomic.Uint64

	// escapePendingTimeoutTotal counts wire 0xA9 leads that spent
	// more than EscapePendingTimeout (32 ms) in pending state without
	// resolution, per v8 §1.3. Non-fatal: the decoder drops the
	// orphaned 0xA9 plus any absorbed AAs and re-processes the
	// current byte in NORMAL state to preserve data for next-byte
	// resync. Distinct from decodeFaultTotal because timeout is a
	// different failure mode (wire/adapter pathologically slow vs.
	// malformed escape pair). Exposed via EscapePendingTimeoutTotal()
	// for transport-level observability.
	escapePendingTimeoutTotal atomic.Uint64

	// escapeRecoveryTotal counts AdminEventEscapeRecovery emissions —
	// a wire 0xA9 followed by a byte that is neither a valid
	// completion (0x00 / 0x01) nor an absorbable 0xAA-within-budget.
	// The decoder drops the 0xA9, absorbed AAs, AND the offending
	// byte. Subset of decodeFaultTotal; exposed for granular
	// operator visibility.
	escapeRecoveryTotal atomic.Uint64

	// escapeBudgetExhaustedTotal counts AdminEventEscapeBudgetExhausted
	// emissions — a wire 0xA9 followed by more than
	// MaxAaAbsorptionsPerEscapePair (8) consecutive 0xAA bytes. The
	// decoder drops the 0xA9, all 8 absorbed AAs, and the
	// over-budget AA. Subset of decodeFaultTotal; exposed for
	// granular operator visibility.
	escapeBudgetExhaustedTotal atomic.Uint64

	// escapeAaAbsorbedTotal counts the cumulative number of 0xAA
	// bytes the decoder absorbed mid-escape-pair across all
	// transactions. Each absorption is a successful in-budget event
	// (NOT a fault). Tracks the AA-injection load the v8 absorber is
	// catching. Note: this is per-byte, not per-pair — a single pair
	// that absorbed 3 AAs increments this counter by 3.
	escapeAaAbsorbedTotal atomic.Uint64

	// Transport-instance observations; never cleared by lifecycle/parser resets.
	// Atomic loads keep metrics readers independent of blocking readMu/I/O.
	receivedSymbolsTotal       atomic.Uint64
	enhFramingErrorsTotal      atomic.Uint64
	enhParserResetsTotal       atomic.Uint64
	adapterResetResponsesTotal atomic.Uint64
}

// NewENHTransport creates a new ENH transport with read/write timeouts.
//
// If conn is a *net.TCPConn, TCP_NODELAY is enabled to avoid Nagle-induced
// latency on the 2-byte ENH frames (~40ms per operation without it).
//
// Optional ENHTransportOption values configure reconnection behavior; see
// WithDialFunc.
func NewENHTransport(conn net.Conn, readTimeout, writeTimeout time.Duration, opts ...ENHTransportOption) *ENHTransport {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}
	t := &ENHTransport{
		conn:                   conn,
		readTimeout:            readTimeout,
		writeTimeout:           writeTimeout,
		buffer:                 make([]byte, 256),
		escapeNow:              time.Now,
		arbitrationSendsSource: true,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// NewENSTransport creates an ENS transport over ENH framing.
//
// ENS uses START arbitration and the adapter transmits the source byte on the
// wire during arbitration, same as ENH. Callers must NOT include the source
// byte in the outgoing telegram payload.
func NewENSTransport(conn net.Conn, readTimeout, writeTimeout time.Duration, opts ...ENHTransportOption) *ENHTransport {
	return NewENHTransport(conn, readTimeout, writeTimeout, opts...)
}

// ArbitrationSendsSource reports whether START arbitration already placed the
// source byte on the wire.
func (t *ENHTransport) ArbitrationSendsSource() bool {
	return t.arbitrationSendsSource
}

// ReadByte returns the next logical bus byte with escaping already removed.
// Non-byte stream events (arbitration started or failed) are discarded; use
// ReadEvent to observe them. Each adapter reset is reported once as
// ErrAdapterReset, a read that exceeds the read timeout wraps ErrTimeout, and
// a closed or failed connection wraps ErrTransportClosed. Concurrent calls are
// serialized.
func (t *ENHTransport) ReadByte() (byte, error) {
	b, _, err := t.readByteAndFlag()
	return b, err
}

// ReadByteWithEscape behaves like ReadByte but additionally returns
// the WasEscaped flag for the emitted byte. F-23 (batch-19,
// 2026-05-13, Codex bot review on PR-1): the protocol layer's
// waitForSyn (idle-detect) MUST use this method for ENH-class
// transports so a logical 0xAA carrying user payload
// (WasEscaped=true, originally wire 0xA9 0x01) is not falsely
// counted as a real wire SYN. See transport.EscapeFlaggedReader for
// the interface contract.
func (t *ENHTransport) ReadByteWithEscape() (byte, bool, error) {
	return t.readByteAndFlag()
}

// readByteAndFlag is the shared implementation backing ReadByte
// (which discards the flag) and ReadByteWithEscape (which surfaces
// it). Acquires readMu, drains pendingEvents, and returns the next
// logical byte plus its WasEscaped provenance.
func (t *ENHTransport) readByteAndFlag() (byte, bool, error) {
	if t.closed.Load() {
		return 0, false, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()

	for {
		if t.resets > 0 {
			t.resets--
			return 0, false, ebuserrors.ErrAdapterReset
		}

		// Drain pendingEvents, returning only Byte events. Non-byte events
		// (STARTED, FAILED) are silently skipped so ReadByte callers never
		// see them.
		for len(t.pendingEvents) > 0 {
			ev := t.pendingEvents[0]
			t.pendingEvents = t.pendingEvents[1:]
			if ev.Kind == StreamEventByte {
				return ev.Byte, ev.WasEscaped, nil
			}
			// Skip non-byte events (StreamEventStarted, StreamEventFailed,
			// StreamEventReset) in ReadByte — event-aware consumers should
			// use ReadEvent instead.
		}

		// Surface deferred parse error after pendingEvents is fully drained.
		if t.deferredErr != nil {
			err := t.deferredErr
			t.deferredErr = nil
			return 0, false, err
		}

		if err := t.fillPendingLocked(); err != nil {
			return 0, false, err
		}
		if t.resets == 0 && len(t.pendingEvents) == 0 {
			continue
		}
	}
}

// ReadEvent surfaces non-byte transport events (reset, arbitration
// started/failed) to passive consumers while preserving ReadByte
// compatibility for active callers.
func (t *ENHTransport) ReadEvent() (StreamEvent, error) {
	if t.closed.Load() {
		return StreamEvent{}, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()

	for {
		if t.resets > 0 {
			t.resets--
			return StreamEvent{Kind: StreamEventReset}, nil
		}

		if len(t.pendingEvents) > 0 {
			ev := t.pendingEvents[0]
			t.pendingEvents = t.pendingEvents[1:]
			if ev.Kind == streamEventRawSymbol {
				// Raw items exist only after ReadEventWithRawSymbol enabled
				// capture; ReadEvent keeps its logical-only contract.
				continue
			}
			return ev, nil
		}

		// Surface deferred parse error after pendingEvents is fully drained.
		if t.deferredErr != nil {
			err := t.deferredErr
			t.deferredErr = nil
			return StreamEvent{}, err
		}

		if err := t.fillPendingLocked(); err != nil {
			return StreamEvent{}, err
		}
		if t.resets == 0 && len(t.pendingEvents) == 0 {
			continue
		}
	}
}

// ReadEventWithRawSymbol implements RawSymbolStreamReader. The first call
// enables raw capture for the rest of this transport's lifetime. Each call
// returns one stream item in observation order: an observed RECEIVED wire
// symbol together with the logical item it completed (if any), or a control
// item. Raw symbols are the adapter-reported values; they are never
// regenerated from logical bytes.
func (t *ENHTransport) ReadEventWithRawSymbol() (StreamEvent, byte, bool, error) {
	if t.closed.Load() {
		return StreamEvent{}, 0, false, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()
	t.rawSymbolItems = true

	for {
		if t.resets > 0 {
			t.resets--
			return StreamEvent{Kind: StreamEventReset}, 0, false, nil
		}

		if len(t.pendingEvents) > 0 {
			ev := t.pendingEvents[0]
			t.pendingEvents = t.pendingEvents[1:]
			if ev.Kind != streamEventRawSymbol {
				// Control item, or a logical item queued before raw
				// capture was enabled.
				return ev, 0, false, nil
			}
			if ev.Data == rawSymbolCompletesNext && len(t.pendingEvents) > 0 {
				logical := t.pendingEvents[0]
				t.pendingEvents = t.pendingEvents[1:]
				return logical, ev.Byte, true, nil
			}
			return StreamEvent{}, ev.Byte, true, nil
		}

		// Surface deferred parse error after pendingEvents is fully drained.
		if t.deferredErr != nil {
			err := t.deferredErr
			t.deferredErr = nil
			return StreamEvent{}, 0, false, err
		}

		if err := t.fillPendingLocked(); err != nil {
			return StreamEvent{}, 0, false, err
		}
	}
}

// Write sends payload bytes over the ENH transport. Each payload byte is
// encoded as a 2-byte ENH pair. The full encoded buffer is written in a
// single conn.Write call for atomicity — TCP may fragment, but the retry
// loop ensures the complete buffer is delivered.
func (t *ENHTransport) Write(payload []byte) (int, error) {
	if t.closed.Load() {
		return 0, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if len(payload) == 0 {
		return 0, nil
	}

	encoded := make([]byte, 0, len(payload)*2)
	for _, payloadByte := range payload {
		seq := EncodeENH(ENHReqSend, payloadByte)
		encoded = append(encoded, seq[0], seq[1])
	}

	written := 0
	for written < len(encoded) {
		if err := t.setWriteDeadline(); err != nil {
			return written / 2, t.mapWriteError(err)
		}

		n, err := t.conn.Write(encoded[written:])
		written += n
		if err != nil {
			return written / 2, t.mapWriteError(err)
		}
		if n == 0 {
			break
		}
	}

	if written != len(encoded) {
		return written / 2, ebuserrors.ErrInvalidPayload
	}

	return len(payload), nil
}

// SendResponderBytes emits a responder-direction byte sequence on the bus,
// bypassing arbitration. This is the ENH transport's implementation of
// ResponderTransport (M4c1 PR-A, decision doc m4b2-responder-go-no-go §6.1).
//
// Semantics:
//   - The caller has already observed the initiator's telegram header via
//     ReadByte / ReadEvent and determined that the telegram is addressed to
//     the local responder slot. SendResponderBytes emits the reactive reply
//     within the eBUS target-response window (budget pinned in PR-B).
//   - Arbitration is owned by the remote initiator; this path MUST NOT
//     call StartArbitration / RequestStart. The underlying wire send is
//     the same raw-byte path used by Write (ENH request-byte pairs); no
//     arbitration helper is touched.
//   - Thread-safety matches Write: serialised under writeMu.
//
// The method is a thin delegation to Write so that responder emission
// reuses the existing ENH encoding, write-deadline, and error-mapping
// substrate — there is exactly one ENH byte-send implementation in the
// tree, and the responder path shares it.
func (t *ENHTransport) SendResponderBytes(payload []byte) (int, error) {
	return t.Write(payload)
}

// Close closes the underlying connection. We set the closed flag first so
// all public methods bail out immediately, then snapshot the conn pointer
// under connMu (not writeMu — a stalled Write holds writeMu and we must
// not block behind it). net.Conn.Close is concurrent-safe and unblocks
// any pending Read or Write.
func (t *ENHTransport) Close() error {
	t.closed.Store(true)
	// Clear transport-state flags defensively (atomic — no lock needed).
	// Any post-Close read is rejected by the closed flag; clearing these
	// avoids stale state leaking if the transport is somehow reused.
	t.awaitingStart.Store(false)
	t.arbitrationDeadline.Store(0)
	t.closePostGrantPreEchoWindow()
	t.connMu.Lock()
	conn := t.conn
	t.connMu.Unlock()
	return conn.Close()
}

func (t *ENHTransport) surfaceResetLocked() {
	t.resetStateLocked()
	t.resets++
}

func (t *ENHTransport) setReadDeadline() error {
	if t.readTimeout <= 0 {
		return t.conn.SetReadDeadline(time.Time{})
	}
	return t.conn.SetReadDeadline(time.Now().Add(t.readTimeout))
}

func (t *ENHTransport) setWriteDeadline() error {
	if t.writeTimeout <= 0 {
		return t.conn.SetWriteDeadline(time.Time{})
	}
	return t.conn.SetWriteDeadline(time.Now().Add(t.writeTimeout))
}

func (t *ENHTransport) mapReadError(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("enh transport read timeout: %w", ebuserrors.ErrTimeout)
	}
	if isClosed(err) {
		return fmt.Errorf("enh transport read closed: %w", ebuserrors.ErrTransportClosed)
	}
	return fmt.Errorf("enh transport read failed: %v: %w", err, ebuserrors.ErrTransportClosed)
}

func (t *ENHTransport) mapWriteError(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("enh transport write timeout: %w", ebuserrors.ErrTimeout)
	}
	if isClosed(err) {
		return fmt.Errorf("enh transport write closed: %w", ebuserrors.ErrTransportClosed)
	}
	return fmt.Errorf("enh transport write failed: %v: %w", err, ebuserrors.ErrTransportClosed)
}

func isTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func isClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) {
		return true
	}
	// Check wrapped net.OpError for closed-connection indicators.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return isClosed(opErr.Err)
	}
	return false
}

// BytesAreUnescaped reports that ENH transport delivers pre-unescaped bytes.
// The adapter handles eBUS wire escaping internally.
//
// F-23 (batch-19, 2026-05-13) — this contract is now honest. Prior to
// F-23 the function returned true but the transport actually forwarded
// raw wire bytes (the eBUS escape pairs 0xA9 0x00 → 0xA9 and 0xA9 0x01
// → 0xAA were leaked as two-byte sequences). Consumers (passive bus
// tap, reconstructor) saw bare 0xA9 followed by 0x00 or 0x01 and
// classified the trailing byte as a spurious symbol, producing false
// unexpected_symbol abandons on every frame whose CRC or data byte was
// 0xA9 or 0xAA. The CRC verification documented in the batch-19 audit
// confirmed three production frames (CRC=0xA9 ×2, CRC=0x1B with a data
// byte 0xA9 at position 13) all matched their wire fingerprints under
// the escape-leak hypothesis. The fix added an EbusEscapeDecoder
// (transport/ebus_escape.go) that runs on every StreamEventByte
// emission inside this transport — the contract is now what the
// docstring always claimed.
func (t *ENHTransport) BytesAreUnescaped() bool { return true }

var _ RawTransport = (*ENHTransport)(nil)
var _ StreamEventReader = (*ENHTransport)(nil)
var _ InfoRequester = (*ENHTransport)(nil)
var _ Reconnectable = (*ENHTransport)(nil)
var _ EscapeAware = (*ENHTransport)(nil)
var _ EscapeFlaggedReader = (*ENHTransport)(nil)
var _ RawSymbolStreamReader = (*ENHTransport)(nil)

// BaudObservabilitySnapshot returns transport-owned observations without
// acquiring I/O locks or performing any bus/adapter request. Physical baud,
// symbol timing and rate-attributable UART faults are unavailable over net.Conn.
func (t *ENHTransport) BaudObservabilitySnapshot() BaudObservabilitySnapshot {
	return BaudObservabilitySnapshot{
		ReceivedSymbolsTotal:       t.receivedSymbolsTotal.Load(),
		ENHFramingErrorsTotal:      t.enhFramingErrorsTotal.Load(),
		ENHParserResetsTotal:       t.enhParserResetsTotal.Load(),
		AdapterResetResponsesTotal: t.adapterResetResponsesTotal.Load(),
		EscapeDecodeFaultsTotal:    t.DecodeFaultTotal(),
		EscapeRecoveriesTotal:      t.EscapeRecoveryTotal(),
		EscapeBudgetExhaustedTotal: t.EscapeBudgetExhaustedTotal(),
		EscapePendingTimeoutsTotal: t.EscapePendingTimeoutTotal(),
	}
}

// parseObservedLocked covers INIT, blocking arbitration, INFO and steady-state
// reads at their common parse boundary. Count before any path-specific filtering
// or early return so decoded observations are independent of consumer delivery.
// Caller holds readMu. Parse's existing first-error boundary is unchanged.
func (t *ENHTransport) parseObservedLocked(data []byte) ([]ENHMessage, error) {
	msgs, err := t.parser.Parse(data)
	for _, msg := range msgs {
		switch msg.Command {
		case ENHResReceived:
			t.receivedSymbolsTotal.Add(1)
		case ENHResResetted:
			t.adapterResetResponsesTotal.Add(1)
		}
	}
	if err != nil {
		t.enhFramingErrorsTotal.Add(1)
	}
	return msgs, err
}

// resetENHParserLocked records the existing reset action without changing its
// cause, timing, error precedence or stream handling. Caller holds readMu.
func (t *ENHTransport) resetENHParserLocked() {
	t.parser.Reset()
	t.enhParserResetsTotal.Add(1)
}

var _ BaudObservability = (*ENHTransport)(nil)
