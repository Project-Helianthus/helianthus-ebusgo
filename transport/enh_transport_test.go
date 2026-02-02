package transport_test

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/transport"
)

func TestENHTransport_ReadByteDecodesFrames(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	seqReceived := transport.EncodeENH(transport.ENHResReceived, 0x22)
	seqFailed := transport.EncodeENH(transport.ENHResFailed, 0x33)

	payload := []byte{0x11, seqReceived[0], seqReceived[1], seqFailed[0], seqFailed[1], 0x44}
	writeErr := make(chan error, 1)
	// Goroutine exits after the payload write completes.
	go func() {
		_, err := server.Write(payload)
		writeErr <- err
	}()

	want := []byte{0x11, 0x22, 0x44}
	for index, expected := range want {
		got, err := enh.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d] error = %v", index, err)
		}
		if got != expected {
			t.Fatalf("ReadByte[%d] = 0x%02x; want 0x%02x", index, got, expected)
		}
	}

	if err := <-writeErr; err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestENHTransport_WriteEncodesFrames(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	readCh := make(chan []byte, 1)
	readErr := make(chan error, 1)
	// Goroutine exits after reading the expected number of bytes or error.
	go func() {
		buf := make([]byte, 4)
		_, err := io.ReadFull(server, buf)
		if err != nil {
			readErr <- err
			return
		}
		readCh <- buf
	}()

	bytesWritten, err := enh.Write([]byte{0x10, 0x20})
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if bytesWritten != 2 {
		t.Fatalf("Write = %d; want 2", bytesWritten)
	}

	var got []byte
	select {
	case got = <-readCh:
	case err := <-readErr:
		t.Fatalf("reader error = %v", err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout waiting for reader")
	}

	seq1 := transport.EncodeENH(transport.ENHReqSend, 0x10)
	seq2 := transport.EncodeENH(transport.ENHReqSend, 0x20)
	want := []byte{seq1[0], seq1[1], seq2[0], seq2[1]}
	if string(got) != string(want) {
		t.Fatalf("framed bytes = %v; want %v", got, want)
	}
}

func TestENHTransport_ReadTimeout(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	enh := transport.NewENHTransport(client, 50*time.Millisecond, 200*time.Millisecond)
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte error = %v; want ErrTimeout", err)
	}
}

func TestENHTransport_ReadClosed(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	_ = server.Close()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadByte error = %v; want ErrTransportClosed", err)
	}
}

func TestENHTransport_SuppressesEchoedBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	outbound := []byte{0x11, 0x22, 0x33}
	// Goroutine exits after draining frames and writing responses.
	go func() {
		buf := make([]byte, len(outbound)*2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		seqEcho1 := transport.EncodeENH(transport.ENHResReceived, 0x11)
		seqNonEcho := transport.EncodeENH(transport.ENHResReceived, 0x88)
		seqEcho3 := transport.EncodeENH(transport.ENHResReceived, 0x33)
		response := []byte{
			seqEcho1[0], seqEcho1[1],
			0x55,
			0x22,
			seqNonEcho[0], seqNonEcho[1],
			seqEcho3[0], seqEcho3[1],
			0x44,
		}
		_, err := server.Write(response)
		serverErr <- err
	}()

	if _, err := enh.Write(outbound); err != nil {
		t.Fatalf("Write error = %v", err)
	}

	want := []byte{0x55, 0x88, 0x44}
	for index, expected := range want {
		got, err := enh.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d] error = %v", index, err)
		}
		if got != expected {
			t.Fatalf("ReadByte[%d] = 0x%02x; want 0x%02x", index, got, expected)
		}
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}
