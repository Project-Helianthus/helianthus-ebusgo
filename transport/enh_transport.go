//go:build !tinygo

package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// ENHTransport wraps a net.Conn and exposes the RawTransport interface using ENH framing.
type ENHTransport struct {
	conn         net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration
	// true for ENH mode: START arbitration already transmits source symbol on wire.
	// false for ENS mode: source symbol must still be written in telegram payload.
	arbitrationSendsSource bool

	readMu  sync.Mutex
	writeMu sync.Mutex

	parser  ENHParser
	pending []byte
	resets  int
	buffer  []byte
}

// NewENHTransport creates a new ENH transport with read/write timeouts.
func NewENHTransport(conn net.Conn, readTimeout, writeTimeout time.Duration) *ENHTransport {
	return &ENHTransport{
		conn:                   conn,
		readTimeout:            readTimeout,
		writeTimeout:           writeTimeout,
		buffer:                 make([]byte, 256),
		arbitrationSendsSource: true,
	}
}

// NewENSTransport creates an ENS transport over ENH framing.
//
// ENS uses START arbitration and the adapter transmits the source byte on the
// wire during arbitration, same as ENH. Callers must NOT include the source
// byte in the outgoing telegram payload.
func NewENSTransport(conn net.Conn, readTimeout, writeTimeout time.Duration) *ENHTransport {
	return NewENHTransport(conn, readTimeout, writeTimeout)
}

// ArbitrationSendsSource reports whether START arbitration already placed the
// source byte on the wire.
func (t *ENHTransport) ArbitrationSendsSource() bool {
	return t.arbitrationSendsSource
}

// Init performs an ENH initialization handshake by sending ENHReqInit(features)
// and waiting for ENHResResetted(features).
//
// Any RESETTED frames observed later will reset the local parser and echo state.
func (t *ENHTransport) Init(features byte) error {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	t.writeMu.Lock()
	seq := EncodeENH(ENHReqInit, features)
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

	maxWait := t.readTimeout
	if maxWait <= 0 {
		maxWait = 2 * time.Second
	}
	start := time.Now()

	for {
		remaining := maxWait - time.Since(start)
		if remaining <= 0 {
			return nil
		}
		if t.readTimeout <= 0 || remaining < t.readTimeout {
			if err := t.conn.SetReadDeadline(time.Now().Add(remaining)); err != nil {
				return t.mapReadError(err)
			}
		} else if err := t.setReadDeadline(); err != nil {
			return t.mapReadError(err)
		}

		n, err := t.conn.Read(t.buffer)
		if err != nil {
			if isTimeout(err) {
				return nil
			}
			return t.mapReadError(err)
		}
		if n == 0 {
			continue
		}

		msgs, err := t.parser.Parse(t.buffer[:n])
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			switch msg.Kind {
			case ENHMessageData:
				t.pending = append(t.pending, msg.Byte)
			case ENHMessageFrame:
				switch msg.Command {
				case ENHResReceived:
					t.pending = append(t.pending, msg.Data)
				case ENHResResetted:
					t.resetStateLocked()
					return nil
				case ENHResInfo:
					// Ignore info responses for now; leave any received bytes queued.
				case ENHResErrorEBUS:
					return fmt.Errorf("enh init ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload)
				case ENHResErrorHost:
					return fmt.Errorf("enh init host error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload)
				}
			}
		}
	}
}

func (t *ENHTransport) ReadByte() (byte, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	for {
		if t.resets > 0 {
			t.resets--
			continue
		}

		if len(t.pending) > 0 {
			value := t.pending[0]
			t.pending = t.pending[1:]
			return value, nil
		}

		if err := t.fillPendingLocked(); err != nil {
			return 0, err
		}
		if t.resets == 0 && len(t.pending) == 0 {
			continue
		}
	}
}

// ReadEvent surfaces optional reset boundaries to passive consumers while
// preserving ReadByte compatibility for active callers.
func (t *ENHTransport) ReadEvent() (StreamEvent, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	for {
		if t.resets > 0 {
			t.resets--
			return StreamEvent{Kind: StreamEventReset}, nil
		}

		if len(t.pending) > 0 {
			value := t.pending[0]
			t.pending = t.pending[1:]
			return StreamEvent{Kind: StreamEventByte, Byte: value}, nil
		}

		if err := t.fillPendingLocked(); err != nil {
			return StreamEvent{}, err
		}
		if t.resets == 0 && len(t.pending) == 0 {
			continue
		}
	}
}

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
			switch msg.Kind {
			case ENHMessageData:
				t.pending = append(t.pending, msg.Byte)
			case ENHMessageFrame:
				switch msg.Command {
				case ENHResReceived:
					// While waiting for STARTED/FAILED, ignore received bus bytes. The protocol
					// state machine will resync after arbitration completes.
				case ENHResResetted:
					t.resetStateLocked()
				case ENHResStarted:
					if msg.Data == initiator {
						arbitrationDone = true
					}
				case ENHResFailed:
					arbitrationDone = true
					arbitrationErr = fmt.Errorf("enh arbitration failed (initiator 0x%02x, winner 0x%02x): %w", initiator, msg.Data, ebuserrors.ErrBusCollision)
				case ENHResErrorEBUS:
					arbitrationDone = true
					arbitrationErr = fmt.Errorf("enh arbitration ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrBusCollision)
				case ENHResErrorHost:
					arbitrationDone = true
					arbitrationErr = fmt.Errorf("enh arbitration host error 0x%02x: %w", msg.Data, ebuserrors.ErrBusCollision)
				}
			}
		}

		if arbitrationDone {
			// Reset parser and pending queue so that ReadByte starts with a
			// clean state. TCP fragmentation can leave the parser with a
			// pending byte1 from a partially-received frame that arrived
			// alongside the STARTED/FAILED response. Without this reset,
			// the stale byte1 combines with the first byte of the next
			// RECEIVED echo to produce a wrong value, causing
			// sendSymbolWithEcho to detect an echo mismatch.
			t.parser.Reset()
			t.pending = t.pending[:0]
			return arbitrationErr
		}
	}
}

// RequestInfo sends an INFO request for the given ID and returns the raw
// response payload. The exchange is transport-exclusive: both readMu and writeMu
// are held for the duration to prevent interleaving with bus operations.
//
// Returns ErrTimeout if the response does not arrive within readTimeout.
// Returns ErrTransportClosed if a RESETTED frame is received mid-exchange.
func (t *ENHTransport) RequestInfo(id AdapterInfoID) ([]byte, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	var err error
	readDeadline := time.Time{}
	defer func() {
		if err != nil {
			t.parser.Reset()
			t.pending = nil
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
			switch msg.Kind {
			case ENHMessageData:
				// Bus byte received during INFO exchange — queue it.
				t.pending = append(t.pending, msg.Byte)
			case ENHMessageFrame:
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
					t.pending = append(t.pending, msg.Data)
				case ENHResResetted:
					t.surfaceResetLocked()
					if !payloadComplete {
						resetBeforeCompletion = true
					}
				case ENHResErrorEBUS:
					return nil, fmt.Errorf("enh info ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload)
				case ENHResErrorHost:
					return nil, fmt.Errorf("enh info host error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload)
				}
			}
		}

		if resetBeforeCompletion {
			err = fmt.Errorf("enh adapter resetted during info request: %w", ebuserrors.ErrTransportClosed)
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
	t.pending = nil
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
		return t.mapReadError(err)
	}
	if bytesRead == 0 {
		return nil
	}

	msgs, err := t.parser.Parse(t.buffer[:bytesRead])
	if err != nil {
		return err
	}
	for _, msg := range msgs {
		switch msg.Kind {
		case ENHMessageData:
			t.pending = append(t.pending, msg.Byte)
		case ENHMessageFrame:
			switch msg.Command {
			case ENHResReceived:
				t.pending = append(t.pending, msg.Data)
			case ENHResResetted:
				t.surfaceResetLocked()
			}
		}
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
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) ||
		strings.Contains(err.Error(), "closed pipe") ||
		strings.Contains(err.Error(), "closed network connection") ||
		strings.Contains(strings.ToLower(err.Error()), "closed")
}

var _ RawTransport = (*ENHTransport)(nil)
var _ StreamEventReader = (*ENHTransport)(nil)
var _ InfoRequester = (*ENHTransport)(nil)
