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

func TestENHTransport_ReadByteIgnoresResettedWithoutDialFunc(t *testing.T) {
	// Without dialFunc (adapter-direct mode), RESETTED is informational
	// and must NOT surface as ErrAdapterReset. Subsequent bytes are
	// delivered transparently.
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

	// RESETTED is silently absorbed; first ReadByte returns 0x11.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v; want 0x11 (RESETTED absorbed)", err)
	}
	if got != 0x11 {
		t.Fatalf("ReadByte = 0x%02x; want 0x11", got)
	}

	got, err = enh.ReadByte()
	if err != nil {
		t.Fatalf("second ReadByte error = %v", err)
	}
	if got != 0x22 {
		t.Fatalf("second ReadByte = 0x%02x; want 0x22", got)
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

func TestENHTransport_ReadEventAbsorbsResettedWithoutCorruptingSubsequentBytes(t *testing.T) {
	// Without dialFunc, RESETTED is silently absorbed. Subsequent bytes
	// (0x11, 0x22) are delivered as normal StreamEventByte events.
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

	// RESETTED absorbed; first event is 0x11.
	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent error = %v", err)
	}
	if event.Kind != transport.StreamEventByte || event.Byte != 0x11 {
		t.Fatalf("ReadEvent = %+v; want StreamEventByte(0x11)", event)
	}

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v", err)
	}
	if got != 0x22 {
		t.Fatalf("ReadByte = 0x%02x; want 0x22", got)
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

func TestENHTransport_StartArbitrationHostErrorReturnsAdapterHostError(t *testing.T) {
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
	if !errors.Is(err, ebuserrors.ErrAdapterHostError) {
		t.Fatalf("StartArbitration error = %v; want ErrAdapterHostError", err)
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

func TestENHTransport_ResettedTransparentWithoutDialFunc(t *testing.T) {
	// Without dialFunc (adapter-direct mode), RESETTED is silently
	// absorbed — no ErrAdapterReset, no parser reset, no state disruption.
	// Post-RESETTED bytes are delivered directly.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	resetted := transport.EncodeENH(transport.ENHResResetted, 0x00)
	postReset := []byte{0x77}
	payload := append(resetted[:], postReset...)
	go func() {
		_, _ = server.Write(payload)
	}()

	// RESETTED absorbed; ReadByte delivers 0x77 directly.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v; want 0x77 (RESETTED absorbed)", err)
	}
	if got != 0x77 {
		t.Fatalf("ReadByte = 0x%02x; want 0x77", got)
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

func TestENHTransport_RequestStartSurfacesStarted(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

		// Read the START request from the client.
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		want := transport.EncodeENH(transport.ENHReqStart, initiator)
		if buf[0] != want[0] || buf[1] != want[1] {
			serverErr <- errors.New("unexpected start request")
			return
		}

		// Respond with STARTED.
		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		_, err := server.Write(started[:])
		serverErr <- err
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	reader, ok := interface{}(enh).(transport.StreamEventReader)
	if !ok {
		t.Fatal("ENH transport does not implement StreamEventReader")
	}

	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent error = %v", err)
	}
	if event.Kind != transport.StreamEventStarted {
		t.Fatalf("ReadEvent kind = %v; want StreamEventStarted", event.Kind)
	}
	if event.Data != initiator {
		t.Fatalf("ReadEvent data = 0x%02x; want 0x%02x", event.Data, initiator)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_RequestStartSurfacesFailed(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)
	winner := byte(0x30)

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
			serverErr <- errors.New("unexpected start request")
			return
		}

		// Respond with FAILED.
		failed := transport.EncodeENH(transport.ENHResFailed, winner)
		_, err := server.Write(failed[:])
		serverErr <- err
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	reader, ok := interface{}(enh).(transport.StreamEventReader)
	if !ok {
		t.Fatal("ENH transport does not implement StreamEventReader")
	}

	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent error = %v", err)
	}
	if event.Kind != transport.StreamEventFailed {
		t.Fatalf("ReadEvent kind = %v; want StreamEventFailed", event.Kind)
	}
	if event.Data != winner {
		t.Fatalf("ReadEvent data = 0x%02x; want 0x%02x", event.Data, winner)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_ReadByteIgnoresStartedFailed(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

		// Read the START request.
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}

		// Send STARTED then a RECEIVED byte. ReadByte must skip STARTED
		// and return only the RECEIVED byte.
		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		received := transport.EncodeENH(transport.ENHResReceived, 0x42)
		payload := append(started[:], received[:]...)
		_, err := server.Write(payload)
		serverErr <- err
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v", err)
	}
	if got != 0x42 {
		t.Fatalf("ReadByte = 0x%02x; want 0x42 (should skip STARTED)", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_ReadEventRecoverFromParserDesync(t *testing.T) {
	// Send an orphan byte2 (0x85, in range 0x80-0xBF) without a preceding
	// byte1. This triggers ErrInvalidPayload in the parser. The transport
	// should recover by resetting the parser, not propagate a fatal error.
	// Subsequent bytes should be readable.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		// First write: orphan byte2 (triggers desync)
		_, err := server.Write([]byte{0x85})
		if err != nil {
			serverErr <- err
			return
		}
		// Second write: valid raw data byte (should be readable after recovery)
		_, err = server.Write([]byte{0x42})
		serverErr <- err
	}()

	reader, ok := interface{}(enh).(transport.StreamEventReader)
	if !ok {
		t.Fatal("ENH transport does not implement StreamEventReader")
	}

	// First ReadEvent: the orphan byte should be absorbed by recovery,
	// not returned as an error.
	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent after orphan byte error = %v; want recovery (no error)", err)
	}
	// The recovered read should deliver the 0x42 byte.
	if event.Kind != transport.StreamEventByte || event.Byte != 0x42 {
		t.Fatalf("ReadEvent after recovery = %+v; want StreamEventByte(0x42)", event)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_ResettedTransparentInReadEvent(t *testing.T) {
	// Send [RESETTED, RECEIVED(0x33)] in one TCP segment. Without dialFunc
	// (adapter-direct mode), RESETTED is silently absorbed. The trailing
	// RECEIVED(0x33) must be delivered as the first event.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		reset := transport.EncodeENH(transport.ENHResResetted, 0x00)
		received := transport.EncodeENH(transport.ENHResReceived, 0x33)
		payload := append(reset[:], received[:]...)
		_, err := server.Write(payload)
		serverErr <- err
	}()

	reader, ok := interface{}(enh).(transport.StreamEventReader)
	if !ok {
		t.Fatal("ENH transport does not implement StreamEventReader")
	}

	// RESETTED absorbed; first event is RECEIVED(0x33) → StreamEventByte(0x33).
	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent error = %v; want StreamEventByte(0x33)", err)
	}
	if event.Kind != transport.StreamEventByte || event.Byte != 0x33 {
		t.Fatalf("ReadEvent = %+v; want StreamEventByte(0x33)", event)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_StartArbitration_HostErrorWrapsAdapterHostError(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		// Read the START request (2 bytes).
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		// Respond with ERROR_HOST(0x03).
		resp := transport.EncodeENH(transport.ENHResErrorHost, 0x03)
		_, writeErr := server.Write(resp[:])
		serverErr <- writeErr
	}()

	err := enh.StartArbitration(0x71)
	if err == nil {
		t.Fatal("StartArbitration returned nil; want error wrapping ErrAdapterHostError")
	}
	if !errors.Is(err, ebuserrors.ErrAdapterHostError) {
		t.Fatalf("StartArbitration error = %v; want wrapping ErrAdapterHostError", err)
	}
	// Must NOT wrap ErrBusCollision (the old, buggy behavior).
	if errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatal("StartArbitration error wraps ErrBusCollision; should wrap ErrAdapterHostError instead")
	}

	if sErr := <-serverErr; sErr != nil {
		t.Fatalf("server error = %v", sErr)
	}
}

func TestENHTransport_Init_HostErrorWrapsAdapterHostError(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		// Read the INIT request (2 bytes).
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		// Respond with ERROR_HOST(0x05).
		resp := transport.EncodeENH(transport.ENHResErrorHost, 0x05)
		_, writeErr := server.Write(resp[:])
		serverErr <- writeErr
	}()

	_, err := enh.Init(0x01)
	if err == nil {
		t.Fatal("Init returned nil error; want error wrapping ErrAdapterHostError")
	}
	if !errors.Is(err, ebuserrors.ErrAdapterHostError) {
		t.Fatalf("Init error = %v; want wrapping ErrAdapterHostError", err)
	}
	// Must NOT wrap ErrInvalidPayload (the old, buggy behavior).
	if errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatal("Init error wraps ErrInvalidPayload; should wrap ErrAdapterHostError instead")
	}

	if sErr := <-serverErr; sErr != nil {
		t.Fatalf("server error = %v", sErr)
	}
}

func TestENHTransport_FillPendingProcessesValidMsgsBeforeError(t *testing.T) {
	// EG8: When Parse returns valid messages alongside an error,
	// fillPendingLocked must queue the valid messages before handling the error.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		// Valid data byte 0x11, then orphan byte2 (0x80, corrupt), then valid data byte 0x22.
		_, err := server.Write([]byte{0x11, 0x80, 0x22})
		serverErr <- err
	}()

	// Both valid bytes should be readable despite the corrupt byte in between.
	got1, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte[0] error = %v; want 0x11", err)
	}
	if got1 != 0x11 {
		t.Fatalf("ReadByte[0] = 0x%02x; want 0x11", got1)
	}

	got2, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte[1] error = %v; want 0x22", err)
	}
	if got2 != 0x22 {
		t.Fatalf("ReadByte[1] = 0x%02x; want 0x22", got2)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_TimeoutResetsParserState(t *testing.T) {
	// EG9: A read timeout mid-parse (byte1 received, timeout before byte2)
	// must reset the parser so that the next read starts clean.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 50*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		// Send only byte1 of an ENH frame, then let timeout expire.
		if _, err := server.Write([]byte{0xC0}); err != nil {
			serverErr <- err
			return
		}
		// Wait for the timeout to fire.
		time.Sleep(100 * time.Millisecond)
		// Now send a valid raw data byte.
		_, err := server.Write([]byte{0x55})
		serverErr <- err
	}()

	// First ReadByte should timeout.
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte error = %v; want ErrTimeout", err)
	}

	// After timeout, parser state should be reset. Next ReadByte should
	// return 0x55 as a plain data byte, NOT combine it with stale byte1.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after timeout error = %v; want 0x55", err)
	}
	if got != 0x55 {
		t.Fatalf("ReadByte after timeout = 0x%02x; want 0x55", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_AdapterDirectResettedNoError(t *testing.T) {
	// EG17: In adapter-direct mode (no dialFunc), RESETTED should not
	// surface as an error -- it is informational only.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	// No WithDialFunc option -- adapter-direct mode.
	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		reset := transport.EncodeENH(transport.ENHResResetted, 0x00)
		after := transport.EncodeENH(transport.ENHResReceived, 0xAB)
		payload := append(reset[:], after[:]...)
		_, err := server.Write(payload)
		serverErr <- err
	}()

	// ReadByte should return 0xAB transparently -- RESETTED absorbed.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v; want 0xAB (RESETTED absorbed)", err)
	}
	if got != 0xAB {
		t.Fatalf("ReadByte = 0x%02x; want 0xAB", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}
