//go:build !tinygo

package transport

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

// TCPPlainTransport exposes RawTransport over a plain TCP byte stream.
//
// This transport does not apply ENH/ENS framing. Bytes are exchanged as-is,
// and protocol-layer framing/arbitration is handled above this layer.
type TCPPlainTransport struct {
	conn   net.Conn
	reader *bufio.Reader

	readTimeout  time.Duration
	writeTimeout time.Duration

	readMu  sync.Mutex
	writeMu sync.Mutex

	closedMu sync.Mutex
	closed   bool
}

func NewTCPPlainTransport(conn net.Conn, readTimeout, writeTimeout time.Duration) *TCPPlainTransport {
	return &TCPPlainTransport{
		conn:         conn,
		reader:       bufio.NewReaderSize(conn, 4096),
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
	}
}

func (t *TCPPlainTransport) ReadByte() (byte, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	if t.isClosed() {
		return 0, ebuserrors.ErrTransportClosed
	}
	if err := t.setReadDeadline(); err != nil {
		return 0, t.mapReadError(err)
	}

	value, err := t.reader.ReadByte()
	if err != nil {
		return 0, t.mapReadError(err)
	}
	return value, nil
}

func (t *TCPPlainTransport) Write(payload []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if len(payload) == 0 {
		return 0, nil
	}
	if t.isClosed() {
		return 0, ebuserrors.ErrTransportClosed
	}

	written := 0
	for written < len(payload) {
		if err := t.setWriteDeadline(); err != nil {
			return written, t.mapWriteError(err)
		}
		n, err := t.conn.Write(payload[written:])
		written += n
		if err != nil {
			return written, t.mapWriteError(err)
		}
		if n == 0 {
			break
		}
	}
	if written != len(payload) {
		return written, ebuserrors.ErrInvalidPayload
	}
	return written, nil
}

func (t *TCPPlainTransport) Close() error {
	t.closedMu.Lock()
	defer t.closedMu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}

func (t *TCPPlainTransport) isClosed() bool {
	t.closedMu.Lock()
	defer t.closedMu.Unlock()
	return t.closed
}

func (t *TCPPlainTransport) setReadDeadline() error {
	if t.readTimeout <= 0 {
		return t.conn.SetReadDeadline(time.Time{})
	}
	return t.conn.SetReadDeadline(time.Now().Add(t.readTimeout))
}

func (t *TCPPlainTransport) setWriteDeadline() error {
	if t.writeTimeout <= 0 {
		return t.conn.SetWriteDeadline(time.Time{})
	}
	return t.conn.SetWriteDeadline(time.Now().Add(t.writeTimeout))
}

func (t *TCPPlainTransport) mapReadError(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("tcp-plain transport read timeout: %w", ebuserrors.ErrTimeout)
	}
	if isClosed(err) || t.isClosed() {
		return fmt.Errorf("tcp-plain transport read closed: %w", ebuserrors.ErrTransportClosed)
	}
	return fmt.Errorf("tcp-plain transport read failed: %v: %w", err, ebuserrors.ErrTransportClosed)
}

func (t *TCPPlainTransport) mapWriteError(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("tcp-plain transport write timeout: %w", ebuserrors.ErrTimeout)
	}
	if isClosed(err) || t.isClosed() {
		return fmt.Errorf("tcp-plain transport write closed: %w", ebuserrors.ErrTransportClosed)
	}
	return fmt.Errorf("tcp-plain transport write failed: %v: %w", err, ebuserrors.ErrTransportClosed)
}

var _ RawTransport = (*TCPPlainTransport)(nil)
