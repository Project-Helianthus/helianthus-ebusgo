//go:build !tinygo

package transport

import (
	"fmt"
	"net"
	"sync"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// UDPPlainTransport exposes RawTransport over UDP datagrams that contain raw eBUS bytes.
//
// Each UDP datagram is treated as a contiguous chunk of the byte stream; ReadByte returns
// bytes sequentially from received datagrams.
//
// This transport does not implement ENH framing or any ESC/SYN handling; that is handled
// by the protocol layer.
type UDPPlainTransport struct {
	conn *net.UDPConn

	readTimeout  time.Duration
	writeTimeout time.Duration

	readMu  sync.Mutex
	writeMu sync.Mutex

	closedMu sync.Mutex
	closed   bool

	pending []byte
	buffer  []byte
}

const udpReadBufferSize = 65535

// maxUDPPendingBytes bounds the pending byte buffer to prevent unbounded
// memory growth from rapid datagram arrival (EG42/EG53).
const maxUDPPendingBytes = 65536

func NewUDPPlainTransport(conn *net.UDPConn, readTimeout, writeTimeout time.Duration) *UDPPlainTransport {
	return &UDPPlainTransport{
		conn:         conn,
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
		buffer:       make([]byte, udpReadBufferSize),
	}
}

func (t *UDPPlainTransport) ReadByte() (byte, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	for {
		if len(t.pending) > 0 {
			value := t.pending[0]
			t.pending = t.pending[1:]
			// Release backing array when fully drained.
			if len(t.pending) == 0 {
				t.pending = nil
			}
			return value, nil
		}

		if t.isClosed() {
			return 0, ebuserrors.ErrTransportClosed
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
		t.pending = append(t.pending, t.buffer[:n]...)
		// Enforce upper bound: copy kept tail into a new slice to release
		// the old backing array and strictly bound memory.
		if len(t.pending) > maxUDPPendingBytes {
			kept := t.pending[len(t.pending)-maxUDPPendingBytes:]
			t.pending = make([]byte, len(kept))
			copy(t.pending, kept)
		}
	}
}

func (t *UDPPlainTransport) Write(payload []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if len(payload) == 0 {
		return 0, nil
	}
	if t.isClosed() {
		return 0, ebuserrors.ErrTransportClosed
	}

	if err := t.setWriteDeadline(); err != nil {
		return 0, t.mapWriteError(err)
	}

	n, err := t.conn.Write(payload)
	if err != nil {
		return n, t.mapWriteError(err)
	}
	if n != len(payload) {
		return n, ebuserrors.ErrInvalidPayload
	}
	return n, nil
}

func (t *UDPPlainTransport) Close() error {
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

func (t *UDPPlainTransport) isClosed() bool {
	t.closedMu.Lock()
	defer t.closedMu.Unlock()
	return t.closed
}

func (t *UDPPlainTransport) setReadDeadline() error {
	if t.readTimeout <= 0 {
		return t.conn.SetReadDeadline(time.Time{})
	}
	return t.conn.SetReadDeadline(time.Now().Add(t.readTimeout))
}

func (t *UDPPlainTransport) setWriteDeadline() error {
	if t.writeTimeout <= 0 {
		return t.conn.SetWriteDeadline(time.Time{})
	}
	return t.conn.SetWriteDeadline(time.Now().Add(t.writeTimeout))
}

func (t *UDPPlainTransport) mapReadError(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("udp-plain transport read timeout: %w", ebuserrors.ErrTimeout)
	}
	if isClosed(err) || t.isClosed() {
		return fmt.Errorf("udp-plain transport read closed: %w", ebuserrors.ErrTransportClosed)
	}
	return fmt.Errorf("udp-plain transport read failed: %v: %w", err, ebuserrors.ErrTransportClosed)
}

func (t *UDPPlainTransport) mapWriteError(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("udp-plain transport write timeout: %w", ebuserrors.ErrTimeout)
	}
	if isClosed(err) || t.isClosed() {
		return fmt.Errorf("udp-plain transport write closed: %w", ebuserrors.ErrTransportClosed)
	}
	return fmt.Errorf("udp-plain transport write failed: %v: %w", err, ebuserrors.ErrTransportClosed)
}

var _ RawTransport = (*UDPPlainTransport)(nil)
