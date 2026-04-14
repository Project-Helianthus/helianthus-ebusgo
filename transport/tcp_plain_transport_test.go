//go:build !tinygo

package transport_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

func TestTCPPlainTransport_ReadByte(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverAccepted := make(chan net.Conn, 1)
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		serverAccepted <- conn
	}()

	clientConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial error = %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	var serverConn net.Conn
	select {
	case serverConn = <-serverAccepted:
	case acceptErr := <-serverErr:
		t.Fatalf("Accept error = %v", acceptErr)
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Accept timed out")
	}
	t.Cleanup(func() { _ = serverConn.Close() })

	tr := transport.NewTCPPlainTransport(clientConn, 200*time.Millisecond, 200*time.Millisecond)

	want := []byte{0x10, 0x20, 0x30, 0x40}
	if _, err := serverConn.Write(want); err != nil {
		t.Fatalf("server write error = %v", err)
	}

	got := make([]byte, 0, len(want))
	for i := 0; i < len(want); i++ {
		value, readErr := tr.ReadByte()
		if readErr != nil {
			t.Fatalf("ReadByte[%d] error = %v", i, readErr)
		}
		got = append(got, value)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read bytes = %x; want %x", got, want)
	}
}

func TestTCPPlainTransport_Write(t *testing.T) {
	t.Parallel()

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })
	t.Cleanup(func() { _ = clientConn.Close() })

	tr := transport.NewTCPPlainTransport(clientConn, 200*time.Millisecond, 200*time.Millisecond)

	want := []byte{0x01, 0x02, 0x03}
	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, len(want))
		_, _ = io.ReadFull(serverConn, buf)
		done <- buf
	}()

	written, err := tr.Write(want)
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if written != len(want) {
		t.Fatalf("written = %d; want %d", written, len(want))
	}

	select {
	case got := <-done:
		if !bytes.Equal(got, want) {
			t.Fatalf("server bytes = %x; want %x", got, want)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("server did not receive bytes")
	}
}

func TestTCPPlainTransport_ReadTimeout(t *testing.T) {
	t.Parallel()

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })
	t.Cleanup(func() { _ = clientConn.Close() })

	tr := transport.NewTCPPlainTransport(clientConn, 20*time.Millisecond, 200*time.Millisecond)
	_, err := tr.ReadByte()
	if err == nil {
		t.Fatalf("ReadByte error = nil; want timeout")
	}
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte error = %v; want ErrTimeout", err)
	}
}

func TestTCPPlainTransport_Close(t *testing.T) {
	t.Parallel()

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })

	tr := transport.NewTCPPlainTransport(clientConn, 200*time.Millisecond, 200*time.Millisecond)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	if _, err := tr.Write([]byte{0x01}); !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("Write error = %v; want ErrTransportClosed", err)
	}
	if _, err := tr.ReadByte(); !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadByte error = %v; want ErrTransportClosed", err)
	}
}

func TestTCPPlainTransport_ReadByteAfterCloseReturnsClosed(t *testing.T) {
	t.Parallel()

	// EG46: After Close(), ReadByte must return ErrTransportClosed even if
	// the bufio.Reader still holds buffered bytes from before Close.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverAccepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		serverAccepted <- conn
	}()

	clientConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial error = %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	var serverConn net.Conn
	select {
	case serverConn = <-serverAccepted:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Accept timed out")
	}
	t.Cleanup(func() { _ = serverConn.Close() })

	tr := transport.NewTCPPlainTransport(clientConn, 200*time.Millisecond, 200*time.Millisecond)

	// Send data so the bufio.Reader has buffered bytes.
	if _, err := serverConn.Write([]byte{0x10, 0x20, 0x30, 0x40}); err != nil {
		t.Fatalf("server write error = %v", err)
	}

	// Read one byte to confirm data is available.
	b, err := tr.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v", err)
	}
	if b != 0x10 {
		t.Fatalf("ReadByte = 0x%02x; want 0x10", b)
	}

	// Close the transport while buffered bytes remain.
	if err := tr.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	// Subsequent ReadByte must return ErrTransportClosed, not buffered data.
	_, err = tr.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadByte after Close error = %v; want ErrTransportClosed (EG46)", err)
	}
}
