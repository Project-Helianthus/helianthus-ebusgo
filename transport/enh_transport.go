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

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

// ENHTransport wraps a net.Conn and exposes the RawTransport interface using ENH framing.
type ENHTransport struct {
	conn         net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration

	readMu  sync.Mutex
	writeMu sync.Mutex

	parser  ENHParser
	pending []byte
	buffer  []byte
}

// NewENHTransport creates a new ENH transport with read/write timeouts.
func NewENHTransport(conn net.Conn, readTimeout, writeTimeout time.Duration) *ENHTransport {
	return &ENHTransport{
		conn:         conn,
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
		buffer:       make([]byte, 256),
	}
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
		if len(t.pending) > 0 {
			value := t.pending[0]
			t.pending = t.pending[1:]
			return value, nil
		}

		if err := t.setReadDeadline(); err != nil {
			return 0, t.mapReadError(err)
		}

		bytesRead, err := t.conn.Read(t.buffer)
		if err != nil {
			return 0, t.mapReadError(err)
		}
		if bytesRead == 0 {
			continue
		}

		msgs, err := t.parser.Parse(t.buffer[:bytesRead])
		if err != nil {
			return 0, err
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
				}
			}
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
				}
			}
		}

		if arbitrationDone {
			return arbitrationErr
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
