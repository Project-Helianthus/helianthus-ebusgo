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
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

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

func TestENHTransport_InitHandshake(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}

		want := transport.EncodeENH(transport.ENHReqInit, 0x00)
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected init bytes")
			return
		}

		resp := transport.EncodeENH(transport.ENHResResetted, 0x00)
		_, err := server.Write(resp[:])
		serverErr <- err
	}()

	if err := enh.Init(0x00); err != nil {
		t.Fatalf("Init error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_ResetClearsEchoSuppression(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}

		reset := transport.EncodeENH(transport.ENHResResetted, 0x00)
		payload := []byte{reset[0], reset[1], 0x11, 0x22}
		_, err := server.Write(payload)
		serverErr <- err
	}()

	if _, err := enh.Write([]byte{0x11}); err != nil {
		t.Fatalf("Write error = %v", err)
	}
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v", err)
	}
	if got != 0x11 {
		t.Fatalf("ReadByte = 0x%02x; want 0x11", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_WriteEncodesFrames(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

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
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 50*time.Millisecond, 200*time.Millisecond)
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte error = %v; want ErrTimeout", err)
	}
}

func TestENHTransport_ReadClosed(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	_ = server.Close()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadByte error = %v; want ErrTransportClosed", err)
	}
}

func TestENHTransport_ForwardsEchoedBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	payload := []byte{0x10, 0x20, 0x30, 0x40, 0x01, 0x55, 0x66}
	serverErr := make(chan error, 1)
	// Goroutine exits after draining frames and writing responses.
	go func() {
		buf := make([]byte, len(payload)*2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		response := make([]byte, 0, len(payload)*2+2)
		for _, value := range payload {
			seqEcho := transport.EncodeENH(transport.ENHResReceived, value)
			response = append(response, seqEcho[0], seqEcho[1])
		}
		seqNonEcho := transport.EncodeENH(transport.ENHResReceived, 0x99)
		response = append(response, seqNonEcho[0], seqNonEcho[1])
		_, err := server.Write(response)
		serverErr <- err
	}()

	if _, err := enh.Write(payload); err != nil {
		t.Fatalf("Write error = %v", err)
	}

	for index, expected := range append(payload, 0x99) {
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
func TestENHTransport_StartArbitrationStartedDiscardsReceivedBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)

	serverErr := make(chan error, 1)
	// Goroutine exits after validating the request and sending responses.
	go func() {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		want := transport.EncodeENH(transport.ENHReqStart, initiator)
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected arbitration request")
			return
		}

		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		payload := []byte{0x11, started[0], started[1], 0x22}
		_, err := server.Write(payload)
		serverErr <- err
	}()

	if err := enh.StartArbitration(initiator); err != nil {
		t.Fatalf("StartArbitration error = %v", err)
	}

	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte error = %v; want ErrTimeout", err)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_StartArbitrationFailedDiscardsReceivedBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)
	winner := byte(0x30)

	serverErr := make(chan error, 1)
	// Goroutine exits after validating the request and sending responses.
	go func() {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		want := transport.EncodeENH(transport.ENHReqStart, initiator)
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected arbitration request")
			return
		}

		failed := transport.EncodeENH(transport.ENHResFailed, winner)
		payload := []byte{0x33, failed[0], failed[1], 0x44}
		_, err := server.Write(payload)
		serverErr <- err
	}()

	err := enh.StartArbitration(initiator)
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("StartArbitration error = %v; want ErrBusCollision", err)
	}

	_, err = enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte error = %v; want ErrTimeout", err)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_StartArbitrationHostErrorReturnsCollision(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)

	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		want := transport.EncodeENH(transport.ENHReqStart, initiator)
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected arbitration request")
			return
		}
		hostErr := transport.EncodeENH(transport.ENHResErrorHost, 0x00)
		_, err := server.Write(hostErr[:])
		serverErr <- err
	}()

	err := enh.StartArbitration(initiator)
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("StartArbitration error = %v; want ErrBusCollision", err)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_StartArbitrationEbusErrorReturnsCollision(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)

	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		want := transport.EncodeENH(transport.ENHReqStart, initiator)
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected arbitration request")
			return
		}
		ebusErr := transport.EncodeENH(transport.ENHResErrorEBUS, 0x01)
		_, err := server.Write(ebusErr[:])
		serverErr <- err
	}()

	err := enh.StartArbitration(initiator)
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("StartArbitration error = %v; want ErrBusCollision", err)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENSTransport_ArbitrationSourceInjectionFlag(t *testing.T) {
	t.Parallel()

	clientEnh, serverEnh := net.Pipe()
	defer func() { _ = clientEnh.Close() }()
	defer func() { _ = serverEnh.Close() }()
	enh := transport.NewENHTransport(clientEnh, 200*time.Millisecond, 200*time.Millisecond)
	if !enh.ArbitrationSendsSource() {
		t.Fatalf("ENH transport ArbitrationSendsSource = false; want true")
	}

	clientEns, serverEns := net.Pipe()
	defer func() { _ = clientEns.Close() }()
	defer func() { _ = serverEns.Close() }()
	ens := transport.NewENSTransport(clientEns, 200*time.Millisecond, 200*time.Millisecond)
	if !ens.ArbitrationSendsSource() {
		t.Fatalf("ENS transport ArbitrationSendsSource = false; want true")
	}
}
