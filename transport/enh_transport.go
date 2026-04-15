//go:build !tinygo

package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

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
// Lock ordering: readMu before writeMu. Both must be held when replacing t.conn.
type ENHTransport struct {
	conn         net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration
	// true for ENH mode: START arbitration already transmits source symbol on wire.
	// false for ENS mode: source symbol must still be written in telegram payload.
	arbitrationSendsSource bool

	// dialFunc, when non-nil, enables mid-session reconnection on RESETTED.
	// The function should return a fresh net.Conn to the adapter.
	dialFunc func() (net.Conn, error)

	readMu  sync.Mutex
	writeMu sync.Mutex

	parser        ENHParser
	pendingEvents []StreamEvent
	resets        int
	buffer        []byte
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

// Init performs an ENH initialization handshake by sending ENHReqInit(features)
// and waiting for ENHResResetted(features).
//
// Returns the adapter's confirmed features byte from the RESETTED response.
// Any RESETTED frames observed later will reset the local parser and echo state.
func (t *ENHTransport) Init(features byte) (byte, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()
	return t.initLocked(features)
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
	return t.initRecvLocked()
}

// initRecvLocked reads the INIT response. Caller must hold readMu.
func (t *ENHTransport) initRecvLocked() (byte, error) {
	maxWait := t.readTimeout
	if maxWait <= 0 {
		maxWait = 2 * time.Second
	}
	start := time.Now()

	for {
		remaining := maxWait - time.Since(start)
		if remaining <= 0 {
			return 0, nil
		}
		if t.readTimeout <= 0 || remaining < t.readTimeout {
			if err := t.conn.SetReadDeadline(time.Now().Add(remaining)); err != nil {
				return 0, t.mapReadError(err)
			}
		} else if err := t.setReadDeadline(); err != nil {
			return 0, t.mapReadError(err)
		}

		n, err := t.conn.Read(t.buffer)
		if err != nil {
			if isTimeout(err) {
				return 0, nil
			}
			return 0, t.mapReadError(err)
		}
		if n == 0 {
			continue
		}

		msgs, err := t.parser.Parse(t.buffer[:n])
		if err != nil {
			return 0, err
		}
		for _, msg := range msgs {
			switch msg.Command {
			case ENHResReceived:
				t.pendingEvents = append(t.pendingEvents, StreamEvent{Kind: StreamEventByte, Byte: msg.Data})
			case ENHResResetted:
				t.resetStateLocked()
				return msg.Data, nil
			case ENHResInfo:
				// Ignore info responses for now; leave any received bytes queued.
			case ENHResErrorEBUS:
				return 0, fmt.Errorf("enh init ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload)
			case ENHResErrorHost:
				return 0, fmt.Errorf("enh init host error 0x%02x: %w", msg.Data, ebuserrors.ErrAdapterHostError)
			}
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
	// Hold writeMu for the entire swap+init-send sequence to prevent
	// concurrent Write() from sending application bytes on the new
	// connection before INIT completes. Lock ordering: readMu (held by
	// caller) before writeMu.
	t.writeMu.Lock()
	_ = t.conn.Close()
	t.conn = newConn
	t.resetStateLocked()
	sendErr := t.initSendLocked(0x01)
	if sendErr != nil {
		_ = t.conn.Close()
		t.writeMu.Unlock()
		return fmt.Errorf("enh reconnect init send: %v: %w", sendErr, ebuserrors.ErrTransportClosed)
	}
	t.writeMu.Unlock()

	// Read INIT response (readMu held by caller).
	if _, err := t.initRecvLocked(); err != nil {
		t.writeMu.Lock()
		_ = t.conn.Close()
		t.writeMu.Unlock()
		return fmt.Errorf("enh reconnect init recv: %v: %w", err, ebuserrors.ErrTransportClosed)
	}
	return nil
}

// Reconnect tears down and re-establishes the underlying TCP connection.
// This is the Reconnectable interface implementation used by the protocol
// layer to recover from dead TCP connections (timeout exhaustion).
//
// Returns ErrTransportClosed if no DialFunc was configured.
func (t *ENHTransport) Reconnect() error {
	t.readMu.Lock()
	defer t.readMu.Unlock()
	if t.dialFunc == nil {
		return fmt.Errorf("enh transport reconnect: no dial function configured: %w", ebuserrors.ErrTransportClosed)
	}
	return t.reconnectLocked()
}

func (t *ENHTransport) ReadByte() (byte, error) {
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
			// Skip non-byte events (StreamEventStarted, StreamEventFailed).
			// These are intentionally dropped in ReadByte — event-aware
			// consumers should use ReadEvent instead, which returns all
			// event types.
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

// StartArbitration requests bus ownership for the given initiator address.
// It sends ENHReqStart(initiator) and blocks until ENHResStarted(initiator) or ENHResFailed(winner).
//
// Any received ENHResReceived bytes observed while waiting are queued so that subsequent ReadByte
// calls can consume them.
func (t *ENHTransport) StartArbitration(initiator byte) error {
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
			return t.mapReadError(err)
		}
		if n == 0 {
			continue
		}

		msgs, err := t.parser.Parse(t.buffer[:n])
		if err != nil {
			return err
		}

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
			t.pendingEvents = t.pendingEvents[:0]
			return arbitrationErr
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
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	seq := EncodeENH(ENHReqStart, initiator)
	if err := t.setWriteDeadline(); err != nil {
		return t.mapWriteError(err)
	}
	_, err := t.conn.Write(seq[:])
	if err != nil {
		return t.mapWriteError(err)
	}
	return nil
}

// RequestInfo sends an INFO request for the given ID and returns the raw
// response payload. The exchange is transport-exclusive: both readMu and writeMu
// are held for the duration to prevent interleaving with bus operations.
//
// Returns ErrTimeout if the response does not arrive within readTimeout.
// Returns ErrAdapterReset if a RESETTED frame is received mid-exchange.
func (t *ENHTransport) RequestInfo(id AdapterInfoID) ([]byte, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	var err error
	readDeadline := time.Time{}
	defer func() {
		if err != nil {
			t.parser.Reset()
			// Preserve buffered events on timeout/error so they are not
			// silently dropped. Clear pending on fatal transport errors and
			// adapter resets where the parser state is unrecoverable or events
			// from the same TCP segment after RESETTED would be stale.
			if errors.Is(err, ebuserrors.ErrTransportClosed) || errors.Is(err, ebuserrors.ErrAdapterReset) {
				t.pendingEvents = nil
			}
		}
	}()

	// Send the INFO request.
	t.writeMu.Lock()
	seq := EncodeENH(ENHReqInfo, byte(id))
	written := 0
	for written < len(seq) {
		if err = t.setWriteDeadline(); err != nil {
			t.writeMu.Unlock()
			err = t.mapWriteError(err)
			return nil, err
		}
		var n int
		n, err = t.conn.Write(seq[written:])
		written += n
		if err != nil {
			t.writeMu.Unlock()
			err = t.mapWriteError(err)
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	t.writeMu.Unlock()
	if written != len(seq) {
		err = fmt.Errorf("enh info request write incomplete: %w", ebuserrors.ErrInvalidPayload)
		return nil, err
	}
	if t.readTimeout > 0 {
		readDeadline = time.Now().Add(t.readTimeout)
	}

	// Read response: first INFO frame has length, then N data frames.
	payloadLen := -1
	var payload []byte
	payloadComplete := false
	resetBeforeCompletion := false

	for {
		if !readDeadline.IsZero() && time.Now().After(readDeadline) {
			err = fmt.Errorf("enh info exchange deadline exceeded: %w", ebuserrors.ErrTimeout)
			return nil, err
		}
		if readDeadline.IsZero() {
			err = t.conn.SetReadDeadline(time.Time{})
		} else {
			err = t.conn.SetReadDeadline(readDeadline)
		}
		if err != nil {
			err = t.mapReadError(err)
			return nil, err
		}

		var n int
		n, err = t.conn.Read(t.buffer)
		if err != nil {
			err = t.mapReadError(err)
			return nil, err
		}
		if n == 0 {
			continue
		}

		msgs, parseErr := t.parser.Parse(t.buffer[:n])
		if parseErr != nil {
			err = parseErr
			return nil, err
		}

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
				// Bus byte received during INFO — queue it.
				t.pendingEvents = append(t.pendingEvents, StreamEvent{Kind: StreamEventByte, Byte: msg.Data})
			case ENHResResetted:
				t.surfaceResetLocked()
				if !payloadComplete {
					resetBeforeCompletion = true
				}
			case ENHResErrorEBUS:
				return nil, fmt.Errorf("enh info ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload)
			case ENHResErrorHost:
				return nil, fmt.Errorf("enh info host error 0x%02x: %w", msg.Data, ebuserrors.ErrAdapterHostError)
			}
		}

		if resetBeforeCompletion {
			err = fmt.Errorf("enh adapter resetted during info request: %w", ebuserrors.ErrAdapterReset)
			return nil, err
		}
		if payloadComplete {
			return payload, nil
		}
	}
}

func (t *ENHTransport) Close() error {
	return t.conn.Close()
}

func (t *ENHTransport) resetStateLocked() {
	t.parser.Reset()
	t.pendingEvents = nil
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
			t.pendingEvents = append(t.pendingEvents, StreamEvent{Kind: StreamEventByte, Byte: msg.Data})
		case ENHResStarted:
			t.pendingEvents = append(t.pendingEvents, StreamEvent{Kind: StreamEventStarted, Data: msg.Data})
		case ENHResFailed:
			t.pendingEvents = append(t.pendingEvents, StreamEvent{Kind: StreamEventFailed, Data: msg.Data})
		case ENHResResetted:
			if t.dialFunc != nil {
				if reconnErr := t.reconnectLocked(); reconnErr != nil {
					return fmt.Errorf("enh adapter reset during read, reconnect failed: %w", ebuserrors.ErrTransportClosed)
				}
				t.resets++
				// Reconnected to fresh TCP — remaining msgs were
				// parsed from the old stream and are stale.
				return nil
			}
			// Adapter-direct mode (no dialFunc): the adapter periodically
			// sends RESETTED as a bus-level event. Do NOT signal this as
			// ErrAdapterReset — that causes handleReset() which drains the
			// active channel, cancels pending arbitrations, and disrupts
			// the mux state machine. In adapter-direct mode the mux owns
			// the TCP connection lifecycle and RESETTED is informational
			// only — continue processing remaining msgs from the same batch.
		}
	}
	if parseErr != nil {
		if errors.Is(parseErr, ebuserrors.ErrInvalidPayload) {
			// Parser desync: orphan byte2, missing byte2, or invalid command.
			// Reset the parser to re-synchronize on the next valid byte1.
			// Valid messages before the desync point have already been queued above.
			t.parser.Reset()
			return nil
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
