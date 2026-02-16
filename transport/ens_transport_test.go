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

func TestENSTransport_ReadByteDecodesEscapes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	ens := transport.NewENSTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	raw := []byte{0x10, 0xA9, 0xAA, 0x20}
	encoded := transport.EncodeENS(raw)

	writeErr := make(chan error, 1)
	go func() {
		_, err := server.Write(encoded)
		writeErr <- err
	}()

	for i, expected := range raw {
		got, err := ens.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d] error = %v", i, err)
		}
		if got != expected {
			t.Fatalf("ReadByte[%d] = 0x%02x; want 0x%02x", i, got, expected)
		}
	}

	if err := <-writeErr; err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

func TestENSTransport_WriteEncodesEscapes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	ens := transport.NewENSTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	raw := []byte{0x10, 0xA9, 0xAA, 0x20}
	encoded := transport.EncodeENS(raw)

	readCh := make(chan []byte, 1)
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, len(encoded))
		_, err := io.ReadFull(server, buf)
		if err != nil {
			readErr <- err
			return
		}
		readCh <- buf
	}()

	n, err := ens.Write(raw)
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if n != len(raw) {
		t.Fatalf("Write = %d; want %d", n, len(raw))
	}

	var got []byte
	select {
	case got = <-readCh:
	case err := <-readErr:
		t.Fatalf("reader error = %v", err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout waiting for reader")
	}

	if string(got) != string(encoded) {
		t.Fatalf("encoded bytes = %v; want %v", got, encoded)
	}
}

func TestENSTransport_ReadTimeout(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	ens := transport.NewENSTransport(client, 50*time.Millisecond, 200*time.Millisecond)
	_, err := ens.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte error = %v; want ErrTimeout", err)
	}
}

func TestENSTransport_ReadClosed(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	_ = server.Close()

	ens := transport.NewENSTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	_, err := ens.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadByte error = %v; want ErrTransportClosed", err)
	}
}
