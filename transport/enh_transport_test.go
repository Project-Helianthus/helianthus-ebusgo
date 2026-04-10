package transport_test

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
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

	features, err := enh.Init(0x00)
	if err != nil {
		t.Fatalf("Init error = %v", err)
	}
	if features != 0x00 {
		t.Fatalf("Init features = 0x%02x; want 0x00", features)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_InitReturnsAdapterFeatures(t *testing.T) {
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

		want := transport.EncodeENH(transport.ENHReqInit, 0x01)
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected init bytes")
			return
		}

		// Adapter confirms with different features byte (e.g. 0x03).
		resp := transport.EncodeENH(transport.ENHResResetted, 0x03)
		_, err := server.Write(resp[:])
		serverErr <- err
	}()

	features, err := enh.Init(0x01)
	if err != nil {
		t.Fatalf("Init error = %v", err)
	}
	if features != 0x03 {
		t.Fatalf("Init features = 0x%02x; want 0x03", features)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_RequestInfo(t *testing.T) {
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

		want := transport.EncodeENH(transport.ENHReqInfo, byte(transport.AdapterInfoVersion))
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected info request")
			return
		}

		length := transport.EncodeENH(transport.ENHResInfo, 0x02)
		first := transport.EncodeENH(transport.ENHResInfo, 0x23)
		second := transport.EncodeENH(transport.ENHResInfo, 0x01)
		response := append(append(length[:], first[:]...), second[:]...)
		_, err := server.Write(response)
		serverErr <- err
	}()

	got, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err != nil {
		t.Fatalf("RequestInfo error = %v", err)
	}
	if len(got) != 2 || got[0] != 0x23 || got[1] != 0x01 {
		t.Fatalf("RequestInfo payload = %v; want [0x23 0x01]", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_RequestInfoTimesOutUnderContinuousReceivedChatter(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENSTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}

		want := transport.EncodeENH(transport.ENHReqInfo, byte(transport.AdapterInfoVersion))
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected info request")
			return
		}

		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()

		received := transport.EncodeENH(transport.ENHResReceived, 0x55)
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			<-ticker.C
			if _, err := server.Write(received[:]); err != nil {
				if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
					serverErr <- nil
					return
				}
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	start := time.Now()
	_, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err == nil {
		t.Fatal("RequestInfo error = nil; want timeout")
	}
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("RequestInfo error = %v; want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed >= 450*time.Millisecond {
		t.Fatalf("RequestInfo elapsed = %s; want bounded timeout before 450ms", elapsed)
	}
	_ = client.Close()

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_RequestInfoStartsTimeoutAfterRequestWriteCompletes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENSTransport(client, 100*time.Millisecond, time.Second)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

		time.Sleep(200 * time.Millisecond)

		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}

		want := transport.EncodeENH(transport.ENHReqInfo, byte(transport.AdapterInfoVersion))
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected info request")
			return
		}

		length := transport.EncodeENH(transport.ENHResInfo, 0x02)
		first := transport.EncodeENH(transport.ENHResInfo, 0x23)
		second := transport.EncodeENH(transport.ENHResInfo, 0x01)
		response := append(append(length[:], first[:]...), second[:]...)
		_, err := server.Write(response)
		serverErr <- err
	}()

	got, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err != nil {
		t.Fatalf("RequestInfo error = %v; want success after delayed write", err)
	}
	if len(got) != 2 || got[0] != 0x23 || got[1] != 0x01 {
		t.Fatalf("RequestInfo payload = %v; want [0x23 0x01]", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_RequestInfoKeepsTrailingReceivedByteInSameBatch(t *testing.T) {
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

		want := transport.EncodeENH(transport.ENHReqInfo, byte(transport.AdapterInfoVersion))
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected info request")
			return
		}

		length := transport.EncodeENH(transport.ENHResInfo, 0x02)
		first := transport.EncodeENH(transport.ENHResInfo, 0x23)
		second := transport.EncodeENH(transport.ENHResInfo, 0x01)
		trailing := transport.EncodeENH(transport.ENHResReceived, 0x99)
		response := append(append(append(length[:], first[:]...), second[:]...), trailing[:]...)
		_, err := server.Write(response)
		serverErr <- err
	}()

	got, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err != nil {
		t.Fatalf("RequestInfo error = %v", err)
	}
	if len(got) != 2 || got[0] != 0x23 || got[1] != 0x01 {
		t.Fatalf("RequestInfo payload = %v; want [0x23 0x01]", got)
	}

	gotByte, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after RequestInfo error = %v", err)
	}
	if gotByte != 0x99 {
		t.Fatalf("ReadByte after RequestInfo = 0x%02x; want 0x99", gotByte)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_RequestInfoKeepsTrailingResetInSameBatch(t *testing.T) {
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

		want := transport.EncodeENH(transport.ENHReqInfo, byte(transport.AdapterInfoVersion))
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected info request")
			return
		}

		length := transport.EncodeENH(transport.ENHResInfo, 0x02)
		first := transport.EncodeENH(transport.ENHResInfo, 0x23)
		second := transport.EncodeENH(transport.ENHResInfo, 0x01)
		trailingReset := transport.EncodeENH(transport.ENHResResetted, 0x00)
		response := append(append(append(length[:], first[:]...), second[:]...), trailingReset[:]...)
		_, err := server.Write(response)
		serverErr <- err
	}()

	got, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err != nil {
		t.Fatalf("RequestInfo error = %v", err)
	}
	if len(got) != 2 || got[0] != 0x23 || got[1] != 0x01 {
		t.Fatalf("RequestInfo payload = %v; want [0x23 0x01]", got)
	}

	reader, ok := interface{}(enh).(transport.StreamEventReader)
	if !ok {
		t.Fatal("ENH transport does not implement StreamEventReader")
	}

	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent after RequestInfo error = %v", err)
	}
	if event.Kind != transport.StreamEventReset {
		t.Fatalf("ReadEvent after RequestInfo kind = %v; want StreamEventReset", event.Kind)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_RequestInfoResetsParserStateAfterTimeoutAndContinues(t *testing.T) {
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

		want := transport.EncodeENH(transport.ENHReqInfo, byte(transport.AdapterInfoVersion))
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected first info request")
			return
		}

		partial := transport.EncodeENH(transport.ENHResInfo, 0x02)
		if _, err := server.Write(partial[:1]); err != nil {
			serverErr <- err
			return
		}

		time.Sleep(300 * time.Millisecond)
		if _, err := server.Write([]byte{0x55}); err != nil {
			serverErr <- err
			return
		}

		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}

		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected second info request")
			return
		}

		length := transport.EncodeENH(transport.ENHResInfo, 0x02)
		first := transport.EncodeENH(transport.ENHResInfo, 0x23)
		second := transport.EncodeENH(transport.ENHResInfo, 0x01)
		response := append(append(length[:], first[:]...), second[:]...)
		_, err := server.Write(response)
		serverErr <- err
	}()

	got, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("RequestInfo error = %v; want ErrTimeout", err)
	}
	if got != nil {
		t.Fatalf("RequestInfo payload = %v; want nil on timeout", got)
	}

	gotByte, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after RequestInfo timeout error = %v", err)
	}
	if gotByte != 0x55 {
		t.Fatalf("ReadByte after RequestInfo timeout = 0x%02x; want 0x55", gotByte)
	}

	got, err = enh.RequestInfo(transport.AdapterInfoVersion)
	if err != nil {
		t.Fatalf("second RequestInfo error = %v", err)
	}
	if len(got) != 2 || got[0] != 0x23 || got[1] != 0x01 {
		t.Fatalf("second RequestInfo payload = %v; want [0x23 0x01]", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_ReadByteReturnsErrAdapterResetOnResetBoundary(t *testing.T) {
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

	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrAdapterReset) {
		t.Fatalf("ReadByte error = %v; want ErrAdapterReset", err)
	}

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after reset error = %v", err)
	}
	if got != 0x11 {
		t.Fatalf("ReadByte after reset = 0x%02x; want 0x11", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_StartArbitrationResettedReturnsErrAdapterReset(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

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

		resetted := transport.EncodeENH(transport.ENHResResetted, 0x01)
		_, err := server.Write(resetted[:])
		serverErr <- err
	}()

	err := enh.StartArbitration(initiator)
	if !errors.Is(err, ebuserrors.ErrAdapterReset) {
		t.Fatalf("StartArbitration error = %v; want ErrAdapterReset", err)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_ReadEventSurfacesResetWithoutCorruptingSubsequentBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

		reset := transport.EncodeENH(transport.ENHResResetted, 0x00)
		payload := []byte{reset[0], reset[1], 0x11, 0x22}
		_, err := server.Write(payload)
		serverErr <- err
	}()

	reader, ok := interface{}(enh).(transport.StreamEventReader)
	if !ok {
		t.Fatal("ENH transport does not implement StreamEventReader")
	}

	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent error = %v", err)
	}
	if event.Kind != transport.StreamEventReset {
		t.Fatalf("ReadEvent kind = %v; want StreamEventReset", event.Kind)
	}

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after reset error = %v", err)
	}
	if got != 0x11 {
		t.Fatalf("ReadByte after reset = 0x%02x; want 0x11", got)
	}

	got, err = enh.ReadByte()
	if err != nil {
		t.Fatalf("second ReadByte after reset error = %v", err)
	}
	if got != 0x22 {
		t.Fatalf("second ReadByte after reset = 0x%02x; want 0x22", got)
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

// --- Reconnection tests (FIX 1, 2) ---

// enhMockDialer creates a mock dialer that returns new net.Pipe connections.
// It also returns a channel of server-side pipe ends for test coordination.
func enhMockDialer() (func() (net.Conn, error), chan net.Conn) {
	servers := make(chan net.Conn, 8)
	dial := func() (net.Conn, error) {
		client, server := net.Pipe()
		servers <- server
		return client, nil
	}
	return dial, servers
}

// enhHandleINIT reads an INIT request from server and responds with RESETTED.
func enhHandleINIT(t *testing.T, server net.Conn) {
	t.Helper()
	buf := make([]byte, 2)
	if _, err := io.ReadFull(server, buf); err != nil {
		t.Fatalf("INIT read error: %v", err)
	}
	want := transport.EncodeENH(transport.ENHReqInit, 0x01)
	if buf[0] != want[0] || buf[1] != want[1] {
		t.Fatalf("expected INIT request, got %02x %02x", buf[0], buf[1])
	}
	resp := transport.EncodeENH(transport.ENHResResetted, 0x01)
	if _, err := server.Write(resp[:]); err != nil {
		t.Fatalf("INIT response write error: %v", err)
	}
}

func TestENHTransport_StartArbitrationResettedReconnects(t *testing.T) {
	t.Parallel()

	// Set up initial connection.
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	dialFunc, newServers := enhMockDialer()
	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond,
		transport.WithDialFunc(dialFunc))

	initiator := byte(0x15)

	// Server goroutine: respond to START with RESETTED on the old connection.
	done := make(chan error, 1)
	go func() {
		defer close(done)
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			done <- err
			return
		}
		resetted := transport.EncodeENH(transport.ENHResResetted, 0x01)
		if _, err := server.Write(resetted[:]); err != nil {
			done <- err
			return
		}
	}()

	// Concurrently handle INIT on the new connection from reconnect.
	go func() {
		newServer := <-newServers
		defer func() { _ = newServer.Close() }()
		enhHandleINIT(t, newServer)
	}()

	err := enh.StartArbitration(initiator)
	if !errors.Is(err, ebuserrors.ErrAdapterReset) {
		t.Fatalf("StartArbitration error = %v; want ErrAdapterReset", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_ReadByteResettedReconnects(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	dialFunc, newServers := enhMockDialer()
	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond,
		transport.WithDialFunc(dialFunc))

	// Concurrently handle INIT on the new connection from reconnect,
	// then send a bus byte on the new connection.
	go func() {
		newServer := <-newServers
		defer func() { _ = newServer.Close() }()
		enhHandleINIT(t, newServer)
		// Send a bus byte on the fresh connection.
		if _, err := newServer.Write([]byte{0x42}); err != nil {
			t.Errorf("new server write error: %v", err)
		}
	}()

	// Send RESETTED on old connection in a goroutine — net.Pipe writes
	// are synchronous and block until the reader (ReadByte) is ready.
	go func() {
		resetted := transport.EncodeENH(transport.ENHResResetted, 0x00)
		_, _ = server.Write(resetted[:])
	}()

	// ReadByte should see ErrAdapterReset first (reconnect happened internally).
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrAdapterReset) {
		t.Fatalf("ReadByte error = %v; want ErrAdapterReset", err)
	}

	// Next ReadByte should succeed on the fresh connection.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after reconnect error = %v", err)
	}
	if got != 0x42 {
		t.Fatalf("ReadByte after reconnect = 0x%02x; want 0x42", got)
	}
}

func TestENHTransport_ReconnectFallsBackWithoutDialFunc(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	// No WithDialFunc — backward-compatible parser-only reset.
	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	resetted := transport.EncodeENH(transport.ENHResResetted, 0x00)
	postReset := []byte{0x77}
	payload := append(resetted[:], postReset...)
	go func() {
		_, _ = server.Write(payload)
	}()

	// Should still get ErrAdapterReset (parser reset path).
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrAdapterReset) {
		t.Fatalf("ReadByte error = %v; want ErrAdapterReset", err)
	}

	// Without reconnect, bytes after RESETTED on the SAME connection
	// are still available (parser reset only, same TCP stream).
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after parser-only reset error = %v", err)
	}
	if got != 0x77 {
		t.Fatalf("ReadByte after parser-only reset = 0x%02x; want 0x77", got)
	}
}

func TestENHTransport_ReconnectImplementsReconnectable(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	dialFunc, newServers := enhMockDialer()
	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond,
		transport.WithDialFunc(dialFunc))

	// Verify the interface is implemented.
	reconn, ok := interface{}(enh).(transport.Reconnectable)
	if !ok {
		t.Fatal("ENHTransport does not implement Reconnectable")
	}

	// Handle INIT on new connection.
	go func() {
		newServer := <-newServers
		defer func() { _ = newServer.Close() }()
		enhHandleINIT(t, newServer)
	}()

	if err := reconn.Reconnect(); err != nil {
		t.Fatalf("Reconnect error = %v", err)
	}
}
