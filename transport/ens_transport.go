//go:build !tinygo

package transport

import (
	"fmt"
	"net"
	"sync"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

type ENSTransport struct {
	conn         net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration

	readMu  sync.Mutex
	writeMu sync.Mutex

	parser  ENSParser
	pending []byte
	buffer  []byte
}

func NewENSTransport(conn net.Conn, readTimeout, writeTimeout time.Duration) *ENSTransport {
	return &ENSTransport{
		conn:         conn,
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
		buffer:       make([]byte, 256),
	}
}

func (t *ENSTransport) ReadByte() (byte, error) {
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

		n, err := t.conn.Read(t.buffer)
		if err != nil {
			return 0, t.mapReadError(err)
		}
		if n == 0 {
			continue
		}

		decoded, err := t.parser.Parse(t.buffer[:n])
		if err != nil {
			return 0, err
		}
		if len(decoded) > 0 {
			t.pending = append(t.pending, decoded...)
		}
	}
}

func (t *ENSTransport) Write(payload []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if len(payload) == 0 {
		return 0, nil
	}

	encoded := EncodeENS(payload)
	written := 0
	for written < len(encoded) {
		if err := t.setWriteDeadline(); err != nil {
			return countENSDecoded(encoded[:written]), t.mapWriteError(err)
		}

		n, err := t.conn.Write(encoded[written:])
		written += n
		if err != nil {
			return countENSDecoded(encoded[:written]), t.mapWriteError(err)
		}
		if n == 0 {
			break
		}
	}

	if written != len(encoded) {
		return countENSDecoded(encoded[:written]), ebuserrors.ErrInvalidPayload
	}

	return len(payload), nil
}

func (t *ENSTransport) Close() error {
	return t.conn.Close()
}

func (t *ENSTransport) setReadDeadline() error {
	if t.readTimeout <= 0 {
		return t.conn.SetReadDeadline(time.Time{})
	}
	return t.conn.SetReadDeadline(time.Now().Add(t.readTimeout))
}

func (t *ENSTransport) setWriteDeadline() error {
	if t.writeTimeout <= 0 {
		return t.conn.SetWriteDeadline(time.Time{})
	}
	return t.conn.SetWriteDeadline(time.Now().Add(t.writeTimeout))
}

func (t *ENSTransport) mapReadError(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("ens transport read timeout: %w", ebuserrors.ErrTimeout)
	}
	if isClosed(err) {
		return fmt.Errorf("ens transport read closed: %w", ebuserrors.ErrTransportClosed)
	}
	return fmt.Errorf("ens transport read failed: %v: %w", err, ebuserrors.ErrTransportClosed)
}

func (t *ENSTransport) mapWriteError(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("ens transport write timeout: %w", ebuserrors.ErrTimeout)
	}
	if isClosed(err) {
		return fmt.Errorf("ens transport write closed: %w", ebuserrors.ErrTransportClosed)
	}
	return fmt.Errorf("ens transport write failed: %v: %w", err, ebuserrors.ErrTransportClosed)
}

func countENSDecoded(encoded []byte) int {
	if len(encoded) == 0 {
		return 0
	}
	var parser ENSParser
	decoded, err := parser.Parse(encoded)
	if err != nil {
		return 0
	}
	return len(decoded)
}

var _ RawTransport = (*ENSTransport)(nil)
