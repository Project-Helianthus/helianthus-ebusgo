package transport_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/transport"
)

type stubAddr string

func (a stubAddr) Network() string { return string(a) }
func (a stubAddr) String() string  { return string(a) }

type stubConn struct {
	mu sync.Mutex

	writes     [][]byte
	writeLimit int
	writeErr   error

	readMu   sync.Mutex
	readData []byte
	readPos  int

	closed bool
}

func (c *stubConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if c.closed {
		return 0, net.ErrClosed
	}
	if c.readPos >= len(c.readData) {
		return 0, io.EOF
	}

	n := copy(p, c.readData[c.readPos:])
	c.readPos += n
	return n, nil
}

func (c *stubConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	writeLimit := c.writeLimit
	writeErr := c.writeErr
	closed := c.closed
	c.mu.Unlock()

	if closed {
		return 0, net.ErrClosed
	}

	n := len(p)
	if writeLimit > 0 && writeLimit < n {
		n = writeLimit
	}
	if writeErr != nil {
		return n, writeErr
	}
	return n, nil
}

func (c *stubConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *stubConn) LocalAddr() net.Addr  { return stubAddr("local") }
func (c *stubConn) RemoteAddr() net.Addr { return stubAddr("remote") }

func (c *stubConn) SetDeadline(time.Time) error      { return nil }
func (c *stubConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stubConn) SetWriteDeadline(time.Time) error { return nil }

func (c *stubConn) writeCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}

func (c *stubConn) writesFlattened() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out bytes.Buffer
	for _, write := range c.writes {
		out.Write(write)
	}
	return out.Bytes()
}

func TestENHTransport_WriteBatchesToSingleConnWrite(t *testing.T) {
	t.Parallel()

	conn := &stubConn{}
	enh := transport.NewENHTransport(conn, 200*time.Millisecond, 200*time.Millisecond)

	payload := []byte{0x10, 0x20, 0x30}
	n, err := enh.Write(payload)
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write = %d; want %d", n, len(payload))
	}
	if conn.writeCalls() != 1 {
		t.Fatalf("conn write calls = %d; want 1", conn.writeCalls())
	}

	want := make([]byte, 0, len(payload)*2)
	for _, value := range payload {
		seq := transport.EncodeENH(transport.ENHReqSend, value)
		want = append(want, seq[0], seq[1])
	}
	got := conn.writesFlattened()
	if string(got) != string(want) {
		t.Fatalf("encoded bytes = %v; want %v", got, want)
	}
}

func TestENHTransport_WriteReturnsPartialCountOnPartialWrite(t *testing.T) {
	t.Parallel()

	seqEcho1 := transport.EncodeENH(transport.ENHResReceived, 0x11)
	seqEcho2 := transport.EncodeENH(transport.ENHResReceived, 0x22)
	conn := &stubConn{
		writeLimit: 3,
		writeErr:   io.ErrClosedPipe,
		readData:   []byte{seqEcho1[0], seqEcho1[1], seqEcho2[0], seqEcho2[1]},
	}
	enh := transport.NewENHTransport(conn, 200*time.Millisecond, 200*time.Millisecond)

	n, err := enh.Write([]byte{0x11, 0x22})
	if n != 1 {
		t.Fatalf("Write = %d; want 1", n)
	}
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("Write error = %v; want ErrTransportClosed", err)
	}

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v", err)
	}
	if got != 0x11 {
		t.Fatalf("ReadByte = 0x%02x; want 0x11", got)
	}

	got2, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte[2] error = %v", err)
	}
	if got2 != 0x22 {
		t.Fatalf("ReadByte[2] = 0x%02x; want 0x22", got2)
	}
}
