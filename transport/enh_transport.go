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
	echoMu  sync.Mutex

	parser      ENHParser
	pending     []byte
	buffer      []byte
	echoPending []byte
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
				if msg.Command == ENHResReceived {
					if !t.shouldSuppressEcho(msg.Data) {
						t.pending = append(t.pending, msg.Data)
					}
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

	framed := make([]byte, 0, len(payload)*2)
	for _, payloadByte := range payload {
		seq := EncodeENH(ENHReqSend, payloadByte)
		framed = append(framed, seq[0], seq[1])
	}

	bytesWritten := 0
	queuedFrames := 0
	for bytesWritten < len(framed) {
		if err := t.setWriteDeadline(); err != nil {
			return queuedFrames, t.mapWriteError(err)
		}

		chunkWritten, err := t.conn.Write(framed[bytesWritten:])
		bytesWritten += chunkWritten
		framesWritten := bytesWritten / 2
		if framesWritten > queuedFrames {
			t.enqueueEcho(payload[queuedFrames:framesWritten])
			queuedFrames = framesWritten
		}
		if err != nil {
			return queuedFrames, t.mapWriteError(err)
		}
		if chunkWritten == 0 {
			break
		}
	}

	if bytesWritten%2 != 0 {
		return queuedFrames, ebuserrors.ErrInvalidPayload
	}

	return queuedFrames, nil
}

func (t *ENHTransport) Close() error {
	return t.conn.Close()
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

func (t *ENHTransport) shouldSuppressEcho(value byte) bool {
	t.echoMu.Lock()
	defer t.echoMu.Unlock()
	if len(t.echoPending) == 0 {
		return false
	}
	if t.echoPending[0] != value {
		return false
	}
	t.echoPending = t.echoPending[1:]
	return true
}

func (t *ENHTransport) enqueueEcho(payload []byte) {
	if len(payload) == 0 {
		return
	}
	t.echoMu.Lock()
	t.echoPending = append(t.echoPending, payload...)
	t.echoMu.Unlock()
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
