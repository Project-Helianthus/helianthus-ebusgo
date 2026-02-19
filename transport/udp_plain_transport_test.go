//go:build !tinygo

package transport_test

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/transport"
)

func TestUDPPlainTransport_ReadByteHandlesLargeDatagram(t *testing.T) {
	t.Parallel()

	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	clientConn, err := net.DialUDP("udp", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP error = %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	tr := transport.NewUDPPlainTransport(clientConn, 200*time.Millisecond, 200*time.Millisecond)

	payload := bytes.Repeat([]byte{0x5A}, 4096)
	if _, err := server.WriteToUDP(payload, clientConn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("server WriteToUDP error = %v", err)
	}

	for index, want := range payload {
		got, err := tr.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d] error = %v", index, err)
		}
		if got != want {
			t.Fatalf("ReadByte[%d] = 0x%02x; want 0x%02x", index, got, want)
		}
	}
}

func TestUDPPlainTransport_WriteSendsDatagram(t *testing.T) {
	t.Parallel()

	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	clientConn, err := net.DialUDP("udp", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP error = %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	tr := transport.NewUDPPlainTransport(clientConn, 200*time.Millisecond, 200*time.Millisecond)

	want := []byte{0x10, 0x20, 0x30, 0x40}
	written, err := tr.Write(want)
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if written != len(want) {
		t.Fatalf("Write = %d; want %d", written, len(want))
	}

	buf := make([]byte, 64)
	_ = server.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	n, _, err := server.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("server ReadFromUDP error = %v", err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("server datagram = %x; want %x", buf[:n], want)
	}
}

func TestUDPPlainTransport_ReadByteStreamsDatagrams(t *testing.T) {
	t.Parallel()

	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	clientConn, err := net.DialUDP("udp", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP error = %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	tr := transport.NewUDPPlainTransport(clientConn, 200*time.Millisecond, 200*time.Millisecond)

	clientAddr := clientConn.LocalAddr().(*net.UDPAddr)
	if _, err := server.WriteToUDP([]byte{0x01, 0x02, 0x03}, clientAddr); err != nil {
		t.Fatalf("server WriteToUDP error = %v", err)
	}
	if _, err := server.WriteToUDP([]byte{0x04}, clientAddr); err != nil {
		t.Fatalf("server WriteToUDP error = %v", err)
	}

	var got []byte
	for i := 0; i < 4; i++ {
		b, err := tr.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte error = %v", err)
		}
		got = append(got, b)
	}

	want := []byte{0x01, 0x02, 0x03, 0x04}
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadByte stream = %x; want %x", got, want)
	}
}

func TestUDPPlainTransport_ReadTimeout(t *testing.T) {
	t.Parallel()

	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	clientConn, err := net.DialUDP("udp", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP error = %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	tr := transport.NewUDPPlainTransport(clientConn, 20*time.Millisecond, 200*time.Millisecond)

	_, err = tr.ReadByte()
	if err == nil {
		t.Fatalf("ReadByte error = nil; want timeout")
	}
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte error = %v; want ErrTimeout", err)
	}
}

func TestUDPPlainTransport_Close(t *testing.T) {
	t.Parallel()

	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	clientConn, err := net.DialUDP("udp", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP error = %v", err)
	}

	tr := transport.NewUDPPlainTransport(clientConn, 200*time.Millisecond, 200*time.Millisecond)
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
