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
// Deadline prevents a degenerate case where the first legitimate echo
// happens to be 0xAA (for example, a frame whose first post-arbitration
// byte is 0xAA) — without a deadline, that echo would be suppressed,
// the window would never close, and the write path would stall. 50ms is
// well above TCP latency between STARTED and our first Write returning
// from conn.Write, but short enough to not materially affect protocol
// timing (a full eBUS transaction is bounded by larger timeouts).
const postGrantPreEchoTimeout = 50 * time.Millisecond

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
			t.parser.Reset() // Clear stale byte1 from partial frame
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
				t.parser.Reset() // Clear stale byte1 from partial frame
				t.deferredErr = nil
				t.awaitingStart.Store(false)
				return InitResult{}, nil
			}
			return InitResult{}, t.mapReadError(err)
		}
		if n == 0 {
			continue
		}

		msgs, parseErr := t.parser.Parse(t.buffer[:n])

		// Process valid messages before handling parse error — a valid
		// RESETTED followed by a corrupt trailing byte should succeed.
		for _, msg := range msgs {
			switch msg.Command {
			case ENHResReceived:
				if len(t.pendingEvents) < maxPendingEvents {
					t.pendingEvents = append(t.pendingEvents, StreamEvent{Kind: StreamEventByte, Byte: msg.Data})
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
			t.parser.Reset()
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

func (t *ENHTransport) ReadByte() (byte, error) {
	if t.closed.Load() {
		return 0, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()

	for {
		if t.resets > 0 {
			t.resets--
			return 0, ebuserrors.ErrAdapterReset
		}

		// Drain pendingEvents, returning only Byte events. Non-byte events
		// (STARTED, FAILED) are silently skipped so ReadByte callers never
		// see them.
		for len(t.pendingEvents) > 0 {
			ev := t.pendingEvents[0]
			t.pendingEvents = t.pendingEvents[1:]
			if ev.Kind == StreamEventByte {
				return ev.Byte, nil
			}
			// Skip non-byte events (StreamEventStarted, StreamEventFailed,
			// StreamEventReset) in ReadByte — event-aware consumers should
			// use ReadEvent instead.
		}

		// Surface deferred parse error after pendingEvents is fully drained.
		if t.deferredErr != nil {
			err := t.deferredErr
			t.deferredErr = nil
			return 0, err
		}

		if err := t.fillPendingLocked(); err != nil {
			return 0, err
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

// StartArbitration requests bus ownership for the given initiator address.
// It sends ENHReqStart(initiator) and blocks until ENHResStarted(initiator) or ENHResFailed(winner).
//
// Any received ENHResReceived bytes observed while waiting are queued so that subsequent ReadByte
// calls can consume them.
func (t *ENHTransport) StartArbitration(initiator byte) error {
	if t.closed.Load() {
		return fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()

	t.writeMu.Lock()
	seq := EncodeENH(ENHReqStart, initiator)
	written := 0
	for written < len(seq) {
		if err := t.setWriteDeadline(); err != nil {
			t.writeMu.Unlock()
			return t.mapWriteError(err)
		}
		n, err := t.conn.Write(seq[written:])
		written += n
		if err != nil {
			t.writeMu.Unlock()
			return t.mapWriteError(err)
		}
		if n == 0 {
			break
		}
	}
	t.writeMu.Unlock()
	if written != len(seq) {
		return ebuserrors.ErrInvalidPayload
	}

	mismatchCount := 0

	for {
		if err := t.setReadDeadline(); err != nil {
			return t.mapReadError(err)
		}

		n, err := t.conn.Read(t.buffer)
		if err != nil {
			if isTimeout(err) {
				t.parser.Reset() // Clear any pending byte1 from partial frame
				t.deferredErr = nil
				t.awaitingStart.Store(false)
			}
			return t.mapReadError(err)
		}
		if n == 0 {
			continue
		}

		msgs, parseErr := t.parser.Parse(t.buffer[:n])

		// Process valid messages before handling parse error — a buffer
		// with a valid STARTED followed by a corrupt trailing byte should
		// succeed, not abort.
		var arbitrationDone bool
		var arbitrationErr error
		for _, msg := range msgs {
			switch msg.Command {
			case ENHResReceived:
				// Discard RECEIVED bytes during arbitration. These are other
				// devices' traffic, not our echoes. Buffering them would cause
				// echo mismatch in sendRawWithEcho (ReadByte drains
				// pendingEvents first, returning stale bytes as "echoes").
			case ENHResResetted:
				if reconnErr := t.reconnectLocked(); reconnErr != nil {
					arbitrationDone = true
					arbitrationErr = fmt.Errorf("enh adapter reset during arbitration, reconnect failed: %w", ebuserrors.ErrTransportClosed)
				} else {
					arbitrationDone = true
					arbitrationErr = fmt.Errorf("enh adapter reset during arbitration (features 0x%02x): %w", msg.Data, ebuserrors.ErrAdapterReset)
				}
			case ENHResStarted:
				if msg.Data == initiator {
					arbitrationDone = true
				} else {
					mismatchCount++
					if mismatchCount >= 3 {
						arbitrationDone = true
						arbitrationErr = fmt.Errorf("enh arbitration started with wrong address 0x%02x (expected 0x%02x, %d mismatches): %w",
							msg.Data, initiator, mismatchCount, ebuserrors.ErrInvalidPayload)
					}
				}
			case ENHResFailed:
				arbitrationDone = true
				arbitrationErr = fmt.Errorf("enh arbitration failed (initiator 0x%02x, winner 0x%02x): %w", initiator, msg.Data, ebuserrors.ErrBusCollision)
			case ENHResErrorEBUS:
				arbitrationDone = true
				arbitrationErr = fmt.Errorf("enh arbitration ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrBusCollision)
			case ENHResErrorHost:
				arbitrationDone = true
				arbitrationErr = fmt.Errorf("enh arbitration host error 0x%02x: %w", msg.Data, ebuserrors.ErrAdapterHostError)
			}
		}

		if arbitrationDone {
			// Reset parser and pending events so that ReadByte starts with
			// a clean state. TCP fragmentation can leave the parser with a
			// pending byte1 from a partially-received frame that arrived
			// alongside the STARTED/FAILED response. Stale RECEIVED bytes
			// in pendingEvents would be consumed as echoes by
			// sendRawWithEcho, causing echo mismatch errors.
			t.parser.Reset()
			t.pendingEvents = nil
			t.deferredErr = nil
			t.awaitingStart.Store(false)
			// On successful grant, open the post-grant pre-echo window
			// so idle SYN bytes arriving between STARTED and our first
			// write's echo are suppressed. On failure/error, clear it
			// defensively.
			if arbitrationErr == nil {
				t.openPostGrantPreEchoWindow()
			} else {
				t.closePostGrantPreEchoWindow()
			}
			return arbitrationErr
		}

		// Handle parse error only after processing valid messages — a
		// corrupt trailing byte should not mask a valid STARTED/FAILED.
		if parseErr != nil {
			t.parser.Reset()
			t.deferredErr = nil
			t.awaitingStart.Store(false)
			return parseErr
		}
	}
}

// RequestStart sends a non-blocking START arbitration request for the given
// initiator address. It writes the ENH START frame and returns immediately
// without waiting for the adapter response.
//
// The adapter's STARTED or FAILED response will arrive asynchronously and
// can be consumed via ReadEvent (as StreamEventStarted or StreamEventFailed).
// ReadByte automatically skips these events.
//
// Only writeMu is held; readMu is NOT acquired so a concurrent ReadEvent
// loop can consume the response without deadlock.
func (t *ENHTransport) RequestStart(initiator byte) error {
	if t.closed.Load() {
		return fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	seq := EncodeENH(ENHReqStart, initiator)
	if err := t.setWriteDeadline(); err != nil {
		return t.mapWriteError(err)
	}
	// Open the arbitration window BEFORE conn.Write so STARTED emitted by
	// a fast adapter (between write-completion and our store) is correctly
	// handled by fillPendingLocked's STARTED case — which clears the flag.
	// Setting AFTER Write creates a race: STARTED arrives while flag=false,
	// gets processed normally, then our late Store(true) opens a FALSE
	// window causing post-grant RECEIVED bytes to be dropped until deadline.
	// On Write failure we roll back with Store(false) so no false window
	// persists after a blocked/failed write.
	t.arbitrationDeadline.Store(time.Now().Add(arbitrationWindowTimeout).UnixNano())
	t.awaitingStartInitiator.Store(uint32(initiator))
	t.awaitingStart.Store(true)
	n, err := t.conn.Write(seq[:])
	if err != nil {
		t.awaitingStart.Store(false)
		t.arbitrationDeadline.Store(0)
		return t.mapWriteError(err)
	}
	if n != len(seq) {
		t.awaitingStart.Store(false)
		t.arbitrationDeadline.Store(0)
		return fmt.Errorf("enh request start short write (%d/%d): %w", n, len(seq), ebuserrors.ErrInvalidPayload)
	}
	return nil
}

// arbitrationWindowExpired checks if the async arbitration window's
// deadline has passed. Returns true when the window must be force-closed
// even though no STARTED/FAILED has arrived.
func (t *ENHTransport) arbitrationWindowExpired() bool {
	deadline := t.arbitrationDeadline.Load()
	if deadline == 0 {
		return false
	}
	return time.Now().UnixNano() >= deadline
}

// openPostGrantPreEchoWindow sets the postGrantPreEcho flag + deadline.
// Called from STARTED handling on all code paths (blocking StartArbitration,
// fillPendingLocked, RequestInfo) so the SYN-suppression window is opened
// consistently with the deadline that bounds it.
func (t *ENHTransport) openPostGrantPreEchoWindow() {
	t.postGrantPreEchoDeadline.Store(time.Now().Add(postGrantPreEchoTimeout).UnixNano())
	t.postGrantPreEcho.Store(true)
}

// closePostGrantPreEchoWindow clears the flag + deadline atomically.
// Called when the real echo arrives (first non-SYN), on FAILED, or on any
// lifecycle reset boundary.
func (t *ENHTransport) closePostGrantPreEchoWindow() {
	t.postGrantPreEcho.Store(false)
	t.postGrantPreEchoDeadline.Store(0)
}

// postGrantPreEchoExpired reports whether the post-grant pre-echo window
// deadline has passed. Returns true even if the flag is still set — the
// caller should close the window and deliver the byte rather than
// suppress it.
func (t *ENHTransport) postGrantPreEchoExpired() bool {
	deadline := t.postGrantPreEchoDeadline.Load()
	if deadline == 0 {
		return false
	}
	return time.Now().UnixNano() >= deadline
}

// RequestInfo sends an INFO request for the given ID and returns the raw
// response payload. The exchange is transport-exclusive: both readMu and writeMu
// are held for the duration to prevent interleaving with bus operations.
//
// Returns ErrTimeout if the response does not arrive within readTimeout.
// Returns ErrAdapterReset if a RESETTED frame is received mid-exchange.
// requestInfoFail performs the on-error cleanup for RequestInfo while both
// readMu and writeMu are still held by the caller. This is intentionally
// NOT a deferred function — deferred execution orders with writeMu.Unlock
// create a race window where a blocked RequestStart can acquire writeMu,
// set awaitingStart=true, and have that state stomped by the cleanup.
// Keeping this synchronous and explicit, called before each error return
// and BEFORE releasing writeMu, eliminates the race entirely.
func (t *ENHTransport) requestInfoFail(err error) error {
	t.parser.Reset()
	t.deferredErr = nil
	t.awaitingStart.Store(false)
	t.arbitrationDeadline.Store(0)
	t.closePostGrantPreEchoWindow()
	// Preserve buffered events on timeout/error so they are not silently
	// dropped. Clear pending on fatal transport errors and adapter resets
	// where the parser state is unrecoverable or events from the same TCP
	// segment after RESETTED would be stale.
	if errors.Is(err, ebuserrors.ErrTransportClosed) || errors.Is(err, ebuserrors.ErrAdapterReset) {
		t.pendingEvents = nil
	}
	return err
}

func (t *ENHTransport) RequestInfo(id AdapterInfoID) ([]byte, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	t.writeMu.Lock()

	// Send the INFO request.
	seq := EncodeENH(ENHReqInfo, byte(id))
	written := 0
	for written < len(seq) {
		if err := t.setWriteDeadline(); err != nil {
			wrapped := t.requestInfoFail(t.mapWriteError(err))
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return nil, wrapped
		}
		n, err := t.conn.Write(seq[written:])
		written += n
		if err != nil {
			wrapped := t.requestInfoFail(t.mapWriteError(err))
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return nil, wrapped
		}
		if n == 0 {
			break
		}
	}
	if written != len(seq) {
		err := t.requestInfoFail(fmt.Errorf("enh info request write incomplete: %w", ebuserrors.ErrInvalidPayload))
		t.writeMu.Unlock()
		t.readMu.Unlock()
		return nil, err
	}

	infoTimeout := t.readTimeout
	if infoTimeout <= 0 {
		infoTimeout = 2 * time.Second // Fallback matches Init default
	}
	readDeadline := time.Now().Add(infoTimeout)

	// Read response: first INFO frame has length, then N data frames.
	payloadLen := -1
	var payload []byte
	payloadComplete := false
	resetBeforeCompletion := false

	for {
		if time.Now().After(readDeadline) {
			err := t.requestInfoFail(fmt.Errorf("enh info exchange deadline exceeded: %w", ebuserrors.ErrTimeout))
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return nil, err
		}
		if err := t.conn.SetReadDeadline(readDeadline); err != nil {
			wrapped := t.requestInfoFail(t.mapReadError(err))
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return nil, wrapped
		}

		n, err := t.conn.Read(t.buffer)
		if err != nil {
			wrapped := t.requestInfoFail(t.mapReadError(err))
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return nil, wrapped
		}
		if n == 0 {
			continue
		}

		msgs, parseErr := t.parser.Parse(t.buffer[:n])

		// Process valid messages before handling parse error — a valid
		// INFO response followed by a corrupt trailing byte should succeed.
		for _, msg := range msgs {
			switch msg.Command {
			case ENHResInfo:
				if payloadLen < 0 {
					// First INFO response: length byte.
					payloadLen = int(msg.Data)
					if payloadLen == 0 {
						payloadComplete = true
						payload = []byte{}
						continue
					}
					payload = make([]byte, 0, payloadLen)
				} else {
					// Subsequent INFO responses: data bytes.
					if len(payload) < payloadLen {
						payload = append(payload, msg.Data)
						if len(payload) >= payloadLen {
							payloadComplete = true
						}
					}
				}
			case ENHResReceived:
				// Bus byte received during INFO. If an async arbitration
				// window is open, drop pre-grant bytes (same semantics as
				// fillPendingLocked awaitingStart gate). Honor the window
				// deadline so a lost STARTED/FAILED does not starve bytes
				// for the full INFO timeout — only for the 500ms arbitration
				// budget. After expiry, bytes fall through to normal queue.
				if t.awaitingStart.Load() {
					if t.arbitrationWindowExpired() {
						t.awaitingStart.Store(false)
						t.arbitrationDeadline.Store(0)
						// Fall through: deliver this byte to pendingEvents.
					} else {
						continue
					}
				}
				// Post-grant pre-echo window: suppress idle SYN bytes that
				// arrive between arbitration grant and first real echo.
				// Same pattern as fillPendingLocked — parity required so
				// INFO-interleave path does not leak idle 0xAA as echo.
				// Honor the deadline so a degenerate first-echo-is-0xAA
				// case does not stall the read path indefinitely.
				if t.postGrantPreEcho.Load() {
					if t.postGrantPreEchoExpired() {
						t.closePostGrantPreEchoWindow()
						// Fall through: deliver this byte.
					} else if msg.Data == ebusSymbolSyn {
						continue
					} else {
						t.closePostGrantPreEchoWindow()
					}
				}
				if len(t.pendingEvents) < maxPendingEvents {
					t.pendingEvents = append(t.pendingEvents, StreamEvent{Kind: StreamEventByte, Byte: msg.Data})
				}
				// Drop when at cap — INFO is bounded by deadline anyway.
			case ENHResStarted:
				// Only a STARTED matching the expected initiator is OUR
				// grant. Mismatched STARTED (another device won) keeps
				// awaitingStart open — same policy as fillPendingLocked.
				if t.awaitingStart.Load() && byte(t.awaitingStartInitiator.Load()) != msg.Data {
					t.appendControlEventLocked(StreamEvent{Kind: StreamEventStarted, Data: msg.Data})
					break
				}
				// Matching STARTED — close arbitration window, open
				// post-grant pre-echo window, queue event.
				t.awaitingStart.Store(false)
				t.arbitrationDeadline.Store(0)
				t.openPostGrantPreEchoWindow()
				t.appendControlEventLocked(StreamEvent{Kind: StreamEventStarted, Data: msg.Data})
			case ENHResFailed:
				// FAILED = arbitration lost — no echo will follow, so
				// don't open the pre-echo window (would suppress legit
				// idle SYNs from the bus during collision backoff).
				t.awaitingStart.Store(false)
				t.arbitrationDeadline.Store(0)
				t.closePostGrantPreEchoWindow()
				t.appendControlEventLocked(StreamEvent{Kind: StreamEventFailed, Data: msg.Data})
			case ENHResResetted:
				t.surfaceResetLocked()
				if !payloadComplete {
					resetBeforeCompletion = true
				}
			case ENHResErrorEBUS:
				err := t.requestInfoFail(fmt.Errorf("enh info ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload))
				t.writeMu.Unlock()
				t.readMu.Unlock()
				return nil, err
			case ENHResErrorHost:
				err := t.requestInfoFail(fmt.Errorf("enh info host error 0x%02x: %w", msg.Data, ebuserrors.ErrAdapterHostError))
				t.writeMu.Unlock()
				t.readMu.Unlock()
				return nil, err
			}
		}

		if resetBeforeCompletion {
			err := t.requestInfoFail(fmt.Errorf("enh adapter resetted during info request: %w", ebuserrors.ErrAdapterReset))
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return nil, err
		}
		if payloadComplete {
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return payload, nil
		}
		// Handle parse error only after processing valid messages.
		if parseErr != nil {
			err := t.requestInfoFail(parseErr)
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return nil, err
		}
	}
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

// appendControlEventLocked queues a control event (STARTED/FAILED/Reset)
// that MUST NOT be silently dropped. If the pendingEvents cap is reached,
// the oldest StreamEventByte is evicted to make room. Control events
// preserve ordering relative to each other; byte ordering is preserved
// among non-evicted bytes. Caller must hold readMu.
func (t *ENHTransport) appendControlEventLocked(ev StreamEvent) {
	if len(t.pendingEvents) >= maxPendingEvents {
		// Evict oldest byte event first (data is recoverable; gateway
		// re-reads bus state). If no byte event exists (queue is all
		// control events — pathological, e.g. repeated STARTED/FAILED/
		// RESETTED under adapter fault), evict the oldest control event.
		// This preserves the bounded-backpressure guarantee strictly
		// while keeping the most recent control event (always more
		// relevant than a stale one from an earlier arbitration cycle).
		evicted := false
		for i, existing := range t.pendingEvents {
			if existing.Kind == StreamEventByte {
				t.pendingEvents = append(t.pendingEvents[:i], t.pendingEvents[i+1:]...)
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

func (t *ENHTransport) resetStateLocked() {
	t.parser.Reset()
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
}

func (t *ENHTransport) surfaceResetLocked() {
	t.resetStateLocked()
	t.resets++
}

func (t *ENHTransport) fillPendingLocked() error {
	if err := t.setReadDeadline(); err != nil {
		return t.mapReadError(err)
	}

	bytesRead, err := t.conn.Read(t.buffer)
	if err != nil {
		if isTimeout(err) {
			t.parser.Reset() // EG9: clear any pending byte1 from partial frame
			// Close async arbitration window on read timeout — if STARTED/
			// FAILED didn't arrive, the RequestStart caller should treat
			// this as a timed-out arbitration. Leaving awaitingStart set
			// would silently drop RECEIVED bytes on subsequent reads.
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
	msgs, parseErr := t.parser.Parse(t.buffer[:bytesRead])
	for _, msg := range msgs {
		switch msg.Command {
		case ENHResReceived:
			// Drop RECEIVED bytes that arrive inside the async arbitration
			// window — they are pre-grant bus traffic, not our echoes. The
			// blocking StartArbitration clears pendingEvents after grant;
			// this is the async-path equivalent (see awaitingStart doc).
			// Force-close the window if its deadline expired (STARTED/
			// FAILED was lost on a busy bus); remaining RECEIVED bytes
			// from that point are delivered normally.
			if t.awaitingStart.Load() {
				if t.arbitrationWindowExpired() {
					t.awaitingStart.Store(false)
					t.arbitrationDeadline.Store(0)
					// Fall through: deliver this byte to pendingEvents.
				} else {
					continue
				}
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
					// Fall through: deliver this byte.
				} else if msg.Data == ebusSymbolSyn {
					continue
				} else {
					t.closePostGrantPreEchoWindow()
				}
			}
			if len(t.pendingEvents) < maxPendingEvents {
				t.pendingEvents = append(t.pendingEvents, StreamEvent{Kind: StreamEventByte, Byte: msg.Data})
			}
		case ENHResStarted:
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
				break
			}
			// Matching STARTED (or no window open) — close arbitration
			// window, open post-grant pre-echo window, queue event.
			t.awaitingStart.Store(false)
			t.arbitrationDeadline.Store(0)
			t.openPostGrantPreEchoWindow()
			// Control events MUST NEVER be dropped — gateway/adaptermux
			// wait for exactly one STARTED or FAILED per arbitration cycle.
			// If the queue is full of byte events, evict the oldest byte
			// events to make room. Control event ordering is preserved.
			t.appendControlEventLocked(StreamEvent{Kind: StreamEventStarted, Data: msg.Data})
		case ENHResFailed:
			// FAILED = arbitration lost; no echo will follow. Clear
			// postGrantPreEcho (defensively) and do not open the window.
			t.awaitingStart.Store(false)
			t.arbitrationDeadline.Store(0)
			t.closePostGrantPreEchoWindow()
			t.appendControlEventLocked(StreamEvent{Kind: StreamEventFailed, Data: msg.Data})
		case ENHResInfo:
			// INFO responses are consumed by RequestInfo's dedicated read path.
			// Unsolicited INFO frames in the steady-state read are safely ignored.
		case ENHResResetted:
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
					return fmt.Errorf("enh adapter reset during read, reconnect failed: %w", ebuserrors.ErrTransportClosed)
				}
				t.resets++
				// Reconnected to fresh TCP — remaining msgs in this batch
				// were parsed from the old stream and are stale.
				return nil
			}
			// Adapter-direct mode: queue one StreamEventReset for ReadEvent
			// consumers using the control-event path (never dropped under
			// byte flood). Remaining msgs in the same batch continue to be
			// processed — bus-level data after RESETTED is still valid.
			// Clear post-grant pre-echo window: any in-flight arbitration
			// correlation is invalidated by the adapter reset.
			t.closePostGrantPreEchoWindow()
			t.appendControlEventLocked(StreamEvent{Kind: StreamEventReset, Data: msg.Data})
		}
	}
	if parseErr != nil {
		if errors.Is(parseErr, ebuserrors.ErrInvalidPayload) {
			// Parser desync: orphan byte2, missing byte2, or unknown command.
			// Reset parser to re-synchronize on the next valid byte1.
			t.parser.Reset()
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
	return nil
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
func (t *ENHTransport) BytesAreUnescaped() bool { return true }

var _ RawTransport = (*ENHTransport)(nil)
var _ StreamEventReader = (*ENHTransport)(nil)
var _ InfoRequester = (*ENHTransport)(nil)
var _ Reconnectable = (*ENHTransport)(nil)
var _ EscapeAware = (*ENHTransport)(nil)
