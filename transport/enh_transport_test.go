package transport_test

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
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

func TestENHTransport_InitSucceedsWithTrailingCorruptByte(t *testing.T) {
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
		// Send valid RESETTED frame followed by a corrupt trailing byte
		// in the same TCP segment. Init must succeed despite the parse error.
		resp := transport.EncodeENH(transport.ENHResResetted, 0x01)
		payload := append(resp[:], 0x85) // 0x85 = orphan ENH byte2 (corrupt)
		_, err := server.Write(payload)
		serverErr <- err
	}()

	features, err := enh.Init(0x00)
	if err != nil {
		t.Fatalf("Init error = %v; want success (RESETTED was valid, trailing byte corrupt)", err)
	}
	if features != 0x01 {
		t.Fatalf("Init features = 0x%02x; want 0x01", features)
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
	// Without dialFunc, RESETTED is surfaced as StreamEventReset boundary,
	// then subsequent bytes (0x11, 0x22) are delivered normally.
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

	// First event: StreamEventReset (boundary surfaced even without dialFunc).
	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent[0] error = %v", err)
	}
	if event.Kind != transport.StreamEventReset {
		t.Fatalf("ReadEvent[0] = %+v; want StreamEventReset", event)
	}

	// Subsequent bytes delivered normally.
	event, err = reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent[1] error = %v", err)
	}
	if event.Kind != transport.StreamEventByte || event.Byte != 0x11 {
		t.Fatalf("ReadEvent[1] = %+v; want StreamEventByte(0x11)", event)
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

		// Send RECEIVED(0x11), STARTED(initiator), RECEIVED(0x22).
		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		payload := []byte{0x11, started[0], started[1], 0x22}
		_, err := server.Write(payload)
		serverErr <- err
	}()

	if err := enh.StartArbitration(initiator); err != nil {
		t.Fatalf("StartArbitration error = %v", err)
	}

	// RECEIVED bytes during arbitration are discarded to prevent echo
	// mismatch in sendRawWithEcho. ReadByte should block (timeout).
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte after arbitration = %v; want ErrTimeout (stale bytes discarded)", err)
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

		// Send RECEIVED(0x33), FAILED(winner), RECEIVED(0x44).
		failed := transport.EncodeENH(transport.ENHResFailed, winner)
		payload := []byte{0x33, failed[0], failed[1], 0x44}
		_, err := server.Write(payload)
		serverErr <- err
	}()

	err := enh.StartArbitration(initiator)
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("StartArbitration error = %v; want ErrBusCollision", err)
	}

	// RECEIVED bytes during arbitration are discarded on failure too.
	// ReadByte should block (timeout).
	_, readErr := enh.ReadByte()
	if !errors.Is(readErr, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte after failed arbitration = %v; want ErrTimeout", readErr)
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
	// Without dialFunc (adapter-direct mode), RESETTED is queued as a
	// StreamEventReset boundary for ReadEvent consumers (see
	// TestENHTransport_XR_ENH_RESETTED_AlwaysSurfacesBoundary). For
	// ReadByte callers, non-byte events are skipped — so ReadByte does
	// not see the reset and delivers post-RESETTED bytes directly without
	// triggering a parser reset or signaling ErrAdapterReset.
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

	// ReadByte skips the StreamEventReset and delivers 0x77 directly.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v; want 0x77 (reset skipped by ReadByte)", err)
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
	// byte1. This triggers ErrInvalidPayload. The transport now surfaces
	// the error (explicit protocol violation) but resets the parser so a
	// subsequent read can recover.
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

	// First ReadEvent: orphan byte surfaces as ErrInvalidPayload.
	_, err := reader.ReadEvent()
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("ReadEvent after orphan byte = %v; want ErrInvalidPayload", err)
	}

	// Second ReadEvent: parser has been reset, next valid byte delivered.
	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent after recovery error = %v; want 0x42", err)
	}
	if event.Kind != transport.StreamEventByte || event.Byte != 0x42 {
		t.Fatalf("ReadEvent after recovery = %+v; want StreamEventByte(0x42)", event)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_ResettedTransparentInReadEvent(t *testing.T) {
	// Send [RESETTED, RECEIVED(0x33)] in one TCP segment. Without dialFunc,
	// RESETTED surfaces as StreamEventReset boundary (not silently absorbed).
	// The trailing RECEIVED(0x33) is delivered as StreamEventByte after.
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

	// First event: StreamEventReset boundary.
	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent[0] error = %v", err)
	}
	if event.Kind != transport.StreamEventReset {
		t.Fatalf("ReadEvent[0] = %+v; want StreamEventReset", event)
	}

	// Second event: RECEIVED(0x33) → StreamEventByte(0x33).
	event, err = reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent[1] error = %v; want StreamEventByte(0x33)", err)
	}
	if event.Kind != transport.StreamEventByte || event.Byte != 0x33 {
		t.Fatalf("ReadEvent[1] = %+v; want StreamEventByte(0x33)", event)
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
		// Valid data byte 0x11, then orphan byte2 (0x80, protocol violation).
		// Any bytes after the violation in this read would be dropped — Parse
		// stops at the first error to create a crisp fault boundary.
		_, err := server.Write([]byte{0x11, 0x80, 0x22})
		serverErr <- err
	}()

	// Valid byte queued before the violation is readable first.
	got1, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte[0] error = %v; want 0x11", err)
	}
	if got1 != 0x11 {
		t.Fatalf("ReadByte[0] = 0x%02x; want 0x11", got1)
	}

	// After queue drained, deferred parse error surfaces explicitly.
	// The 0x22 byte after the violation is NOT delivered — caller must
	// see the explicit protocol error as a fault boundary.
	_, err = enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("ReadByte[1] = %v; want ErrInvalidPayload (explicit protocol violation)", err)
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
		// Wait for the timeout to fire (readTimeout is 50ms).
		time.Sleep(70 * time.Millisecond)
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

// TestENHTransport_Race_WriteAndReconnect exercises the data-race fix for
// EG48/EG32: concurrent Write and Reconnect must not race on t.conn.
// Run with -race to verify.
func TestENHTransport_Race_WriteAndReconnect(t *testing.T) {
	t.Parallel()

	client, _ := net.Pipe()
	defer func() { _ = client.Close() }()

	// dialFunc returns a fresh net.Pipe each time.
	dialFunc := func() (net.Conn, error) {
		c, s := net.Pipe()
		// Feed a RESETTED response so initLocked succeeds.
		go func() {
			resp := transport.EncodeENH(transport.ENHResResetted, 0x01)
			_, _ = s.Write(resp[:])
			// Keep server side open; closed when pipe partner closes.
		}()
		return c, nil
	}

	enh := transport.NewENHTransport(client, 100*time.Millisecond, 100*time.Millisecond,
		transport.WithDialFunc(dialFunc))

	done := make(chan struct{})
	// Writer goroutine: hammer Write concurrently with Reconnect.
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_, _ = enh.Write([]byte{0x42})
		}
	}()

	// Reconnect goroutine: runs alongside Write.
	for i := 0; i < 10; i++ {
		_ = enh.Reconnect()
	}

	<-done
}

// EG14/EG39: StartArbitration aborts after 3 wrong-address STARTED responses.
func TestENHTransport_StartArbitrationWrongAddressAborts(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)
	wrongAddr := byte(0x50)

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

		// Send 3 STARTED responses with the wrong address.
		wrongStarted := transport.EncodeENH(transport.ENHResStarted, wrongAddr)
		var payload []byte
		for i := 0; i < 3; i++ {
			payload = append(payload, wrongStarted[0], wrongStarted[1])
		}
		_, err := server.Write(payload)
		serverErr <- err
	}()

	err := enh.StartArbitration(initiator)
	if err == nil {
		t.Fatal("StartArbitration error = nil; want error after 3 mismatches")
	}
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("StartArbitration error = %v; want ErrInvalidPayload", err)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

// EG14/EG39: StartArbitration succeeds if correct address arrives before 3 mismatches.
func TestENHTransport_StartArbitrationWrongThenCorrectSucceeds(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
	initiator := byte(0x10)
	wrongAddr := byte(0x50)

	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}

		// Send 2 wrong STARTEDs then the correct one.
		wrongStarted := transport.EncodeENH(transport.ENHResStarted, wrongAddr)
		correctStarted := transport.EncodeENH(transport.ENHResStarted, initiator)
		var payload []byte
		payload = append(payload, wrongStarted[0], wrongStarted[1])
		payload = append(payload, wrongStarted[0], wrongStarted[1])
		payload = append(payload, correctStarted[0], correctStarted[1])
		_, err := server.Write(payload)
		serverErr <- err
	}()

	if err := enh.StartArbitration(initiator); err != nil {
		t.Fatalf("StartArbitration error = %v; want nil", err)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

// EG40: isClosed uses errors.Is/errors.As, not substring matching.
func TestIsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"io.EOF", io.EOF, true},
		{"net.ErrClosed", net.ErrClosed, true},
		{"io.ErrClosedPipe", io.ErrClosedPipe, true},
		{"os.ErrClosed", os.ErrClosed, true},
		{"wrapped io.EOF", fmt.Errorf("read: %w", io.EOF), true},
		{"wrapped net.ErrClosed", fmt.Errorf("conn: %w", net.ErrClosed), true},
		// The old bug: substring match on "closed" caused false positives.
		{"unrelated closed string", fmt.Errorf("operation closed by user"), false},
		{"random error", fmt.Errorf("something went wrong"), false},
		// net.OpError wrapping os.ErrClosed.
		{"OpError wrapping os.ErrClosed", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrClosed}, true},
		{"OpError wrapping random error", &net.OpError{Op: "read", Net: "tcp", Err: fmt.Errorf("timeout")}, false},
		// syscall connection-reset errors (Linux ECONNRESET/ECONNABORTED).
		{"ECONNRESET", syscall.ECONNRESET, true},
		{"ECONNABORTED", syscall.ECONNABORTED, true},
		{"OpError wrapping SyscallError with ECONNRESET", &net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := transport.IsClosed(tc.err)
			if got != tc.want {
				t.Errorf("IsClosed(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

// R6-REG-01: When reconnectLocked's initRecvLocked fails with an error
// (e.g. eBUS error), the new conn must remain in place (not closed) and
// the error must wrap ErrAdapterReset (transient), not ErrTransportClosed (fatal).
func TestENHTransport_ReconnectInitRecvError_ConnStillUsable(t *testing.T) {
	t.Parallel()

	// First connection: the "old" one that will be replaced.
	oldClient, oldServer := net.Pipe()
	defer func() { _ = oldServer.Close() }()

	// New connection: dial succeeds, INIT send succeeds, but adapter responds
	// with an eBUS error instead of RESETTED. The error should preserve the
	// original error class (ErrInvalidPayload), not wrap as ErrTransportClosed.
	newClient, newServer := net.Pipe()
	defer func() { _ = newClient.Close() }()
	defer func() { _ = newServer.Close() }()

	dialCalled := make(chan struct{}, 1)
	dialFunc := func() (net.Conn, error) {
		dialCalled <- struct{}{}
		return newClient, nil
	}

	enh := transport.NewENHTransport(oldClient, 200*time.Millisecond, 200*time.Millisecond,
		transport.WithDialFunc(dialFunc))

	// Perform Init on the old connection to set up initial state.
	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(oldServer, buf); err != nil {
			serverErr <- err
			return
		}
		resp := transport.EncodeENH(transport.ENHResResetted, 0x01)
		_, err := oldServer.Write(resp[:])
		serverErr <- err
	}()

	if _, err := enh.Init(0x00); err != nil {
		t.Fatalf("Init error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("old server init error = %v", err)
	}

	// Trigger reconnect via Reconnect(). The new server will consume the
	// INIT request and respond with an eBUS error, causing initRecvLocked
	// to return an error.
	go func() {
		<-dialCalled
		buf := make([]byte, 2)
		// Consume the INIT request on the new conn.
		_, _ = io.ReadFull(newServer, buf)
		// Send eBUS error instead of RESETTED.
		ebusErr := transport.EncodeENH(transport.ENHResErrorEBUS, 0x42)
		_, _ = newServer.Write(ebusErr[:])
	}()

	err := enh.Reconnect()
	if err == nil {
		t.Fatal("Reconnect() should have returned an error on initRecvLocked eBUS error")
	}
	// The error should preserve the original error class from initRecvLocked
	// (ErrInvalidPayload for eBUS errors), NOT wrap as ErrTransportClosed (fatal).
	if errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("Reconnect() error wraps ErrTransportClosed; want original error class preserved")
	}
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("Reconnect() error = %v; want ErrInvalidPayload (from eBUS error response)", err)
	}

	// The conn should still be usable — Write should not fail with ErrTransportClosed.
	writeDone := make(chan error, 1)
	go func() {
		_, err := enh.Write([]byte{0x42})
		writeDone <- err
	}()
	// Consume the write on new server side so it doesn't block.
	go func() {
		buf := make([]byte, 2)
		_, _ = io.ReadFull(newServer, buf)
	}()

	select {
	case writeErr := <-writeDone:
		if errors.Is(writeErr, ebuserrors.ErrTransportClosed) {
			t.Fatalf("Write after failed reconnect returned ErrTransportClosed; conn should still be usable")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Write after failed reconnect timed out unexpectedly")
	}
}

// R6-EG-Codex-04: Close() must be safe to call concurrently with Write().
func TestENHTransport_CloseConcurrentWithWrite(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 100*time.Millisecond, 100*time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, err := enh.Write([]byte{byte(i)})
			if err != nil {
				return
			}
		}
	}()

	// Closer goroutine — give writer a small head start.
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		_ = enh.Close()
	}()

	// Drain writes on server side to prevent blocking.
	go func() {
		buf := make([]byte, 256)
		for {
			_, err := server.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	wg.Wait()
	// Test passes if there is no data race (run with -race).
}

// R6-REG-03: Unsolicited INFO frames in fillPendingLocked are safely ignored
// and do not corrupt the event stream.
func TestENHTransport_FillPendingIgnoresInfoFrames(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)

		// Send INFO(0x05), then RECEIVED(0xAB).
		info := transport.EncodeENH(transport.ENHResInfo, 0x05)
		recv := transport.EncodeENH(transport.ENHResReceived, 0xAB)
		payload := append(info[:], recv[:]...)
		_, err := server.Write(payload)
		serverErr <- err
	}()

	// ReadByte should skip the INFO frame and return 0xAB.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v", err)
	}
	if got != 0xAB {
		t.Fatalf("ReadByte = 0x%02x; want 0xAB (INFO frame should be ignored)", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

// TestENHTransport_XR_INIT_TimeoutFailOpen_Bounded verifies that when the
// adapter does not respond with RESETTED during INIT, the transport fails
// open: Init returns features=0 with nil error, and InitWithResult returns
// Confirmed=false so the caller can distinguish timeout from confirmed 0x00.
func TestENHTransport_XR_INIT_TimeoutFailOpen_Bounded(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	// Short timeout to keep the test fast.
	enh := transport.NewENHTransport(client, 100*time.Millisecond, 100*time.Millisecond)

	// Server reads the INIT request but never sends RESETTED.
	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		buf := make([]byte, 2)
		_, err := io.ReadFull(server, buf)
		serverErr <- err
	}()

	// Subtest 1: Init backward compat — returns 0, nil on timeout.
	features, err := enh.Init(0x00)
	if err != nil {
		t.Fatalf("Init error = %v; want nil (fail-open on timeout)", err)
	}
	if features != 0x00 {
		t.Fatalf("Init features = 0x%02x; want 0x00", features)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}

	// Subtest 2: InitWithResult — returns Confirmed=false on timeout.
	// Need a fresh connection since Init already consumed the first one.
	client2, server2 := net.Pipe()
	defer func() { _ = client2.Close() }()
	defer func() { _ = server2.Close() }()

	enh2 := transport.NewENHTransport(client2, 100*time.Millisecond, 100*time.Millisecond)

	serverErr2 := make(chan error, 1)
	go func() {
		defer close(serverErr2)
		buf := make([]byte, 2)
		_, err := io.ReadFull(server2, buf)
		serverErr2 <- err
	}()

	result, err := enh2.InitWithResult(0x01)
	if err != nil {
		t.Fatalf("InitWithResult error = %v; want nil (fail-open on timeout)", err)
	}
	if result.Confirmed {
		t.Fatal("InitWithResult Confirmed = true; want false (no RESETTED received)")
	}
	if result.Features != 0x00 {
		t.Fatalf("InitWithResult Features = 0x%02x; want 0x00", result.Features)
	}

	if err := <-serverErr2; err != nil {
		t.Fatalf("server2 error = %v", err)
	}
}

// TestENHTransport_InitWithResult_Confirmed verifies that when the adapter
// responds with RESETTED, InitWithResult returns Confirmed=true and the
// adapter's features byte.
func TestENHTransport_InitWithResult_Confirmed(t *testing.T) {
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
		resp := transport.EncodeENH(transport.ENHResResetted, 0x03)
		_, err := server.Write(resp[:])
		serverErr <- err
	}()

	result, err := enh.InitWithResult(0x01)
	if err != nil {
		t.Fatalf("InitWithResult error = %v", err)
	}
	if !result.Confirmed {
		t.Fatal("InitWithResult Confirmed = false; want true")
	}
	if result.Features != 0x03 {
		t.Fatalf("InitWithResult Features = 0x%02x; want 0x03", result.Features)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_InitWithResult_ErrorHost(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	go func() {
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)
		resp := transport.EncodeENH(transport.ENHResErrorHost, 0x42)
		_, _ = server.Write(resp[:])
	}()

	_, err := enh.InitWithResult(0x01)
	if err == nil {
		t.Fatal("InitWithResult should return error on ErrorHost")
	}
	if !errors.Is(err, ebuserrors.ErrAdapterHostError) {
		t.Fatalf("InitWithResult error = %v; want ErrAdapterHostError", err)
	}
}

func TestENHTransport_InitWithResult_ErrorEBUS(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	go func() {
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)
		resp := transport.EncodeENH(transport.ENHResErrorEBUS, 0x99)
		_, _ = server.Write(resp[:])
	}()

	_, err := enh.InitWithResult(0x01)
	if err == nil {
		t.Fatal("InitWithResult should return error on ErrorEBUS")
	}
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("InitWithResult error = %v; want ErrInvalidPayload", err)
	}
}

func TestENHTransport_InitWithResult_CorruptTrailingByte(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

	go func() {
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)
		// Valid RESETTED + corrupt trailing byte in same segment
		resp := transport.EncodeENH(transport.ENHResResetted, 0x01)
		_, _ = server.Write(append(resp[:], 0x85))
	}()

	result, err := enh.InitWithResult(0x01)
	if err != nil {
		t.Fatalf("InitWithResult error = %v; want success (RESETTED valid)", err)
	}
	if !result.Confirmed {
		t.Fatal("InitWithResult Confirmed = false; want true")
	}
	if result.Features != 0x01 {
		t.Fatalf("InitWithResult Features = 0x%02x; want 0x01", result.Features)
	}
}

// --- XR alignment tests ---

func TestENHTransport_XR_UpstreamLoss_GracefulShutdown_NoHang(t *testing.T) {
	// Fix 1: Close() sets terminal state; Reconnect() after Close() returns
	// ErrTransportClosed immediately without hanging on lock acquisition.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	dialFunc, _ := enhMockDialer()
	enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond,
		transport.WithDialFunc(dialFunc))

	if err := enh.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	// Reconnect after Close must return ErrTransportClosed, not hang.
	done := make(chan error, 1)
	go func() {
		done <- enh.Reconnect()
	}()

	var err error
	select {
	case err = <-done:
		if !errors.Is(err, ebuserrors.ErrTransportClosed) {
			t.Fatalf("Reconnect after Close error = %v; want ErrTransportClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Reconnect after Close hung for >1s")
	}

	// ReadByte after Close must also return ErrTransportClosed.
	_, err = enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadByte after Close error = %v; want ErrTransportClosed", err)
	}

	// ReadEvent after Close must return ErrTransportClosed.
	reader, ok := interface{}(enh).(transport.StreamEventReader)
	if !ok {
		t.Fatal("ENH transport does not implement StreamEventReader")
	}
	_, err = reader.ReadEvent()
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadEvent after Close error = %v; want ErrTransportClosed", err)
	}

	// Write after Close must return ErrTransportClosed.
	_, err = enh.Write([]byte{0x11})
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("Write after Close error = %v; want ErrTransportClosed", err)
	}

	// StartArbitration after Close must return ErrTransportClosed.
	err = enh.StartArbitration(0x10)
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("StartArbitration after Close error = %v; want ErrTransportClosed", err)
	}

	// RequestStart after Close must return ErrTransportClosed.
	err = enh.RequestStart(0x10)
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("RequestStart after Close error = %v; want ErrTransportClosed", err)
	}

	// RequestInfo after Close must return ErrTransportClosed.
	infoReq, ok := interface{}(enh).(transport.InfoRequester)
	if !ok {
		t.Fatal("ENH transport does not implement InfoRequester")
	}
	_, err = infoReq.RequestInfo(transport.AdapterInfoVersion)
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("RequestInfo after Close error = %v; want ErrTransportClosed", err)
	}
}

func TestENHTransport_XR_INFO_FrameLength_AndSerialAccess(t *testing.T) {
	t.Parallel()

	t.Run("timeout_fallback", func(t *testing.T) {
		t.Parallel()
		// Fix 2: RequestInfo with readTimeout=0 uses a fallback timeout and
		// returns within bounded time instead of blocking forever.
		client, server := net.Pipe()
		defer func() { _ = client.Close() }()
		defer func() { _ = server.Close() }()

		// readTimeout=0 means "no timeout configured" -- RequestInfo must use fallback.
		enh := transport.NewENHTransport(client, 0, 200*time.Millisecond)

		serverErr := make(chan error, 1)
		go func() {
			defer close(serverErr)
			// Read the INFO request but never respond.
			buf := make([]byte, 2)
			_, err := io.ReadFull(server, buf)
			serverErr <- err
		}()

		start := time.Now()
		_, err := enh.RequestInfo(transport.AdapterInfoVersion)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("RequestInfo with zero timeout returned nil error; want timeout")
		}
		if !errors.Is(err, ebuserrors.ErrTimeout) {
			t.Fatalf("RequestInfo error = %v; want ErrTimeout", err)
		}
		// Fallback is 2s; should complete within 3s even with scheduling jitter.
		if elapsed > 3*time.Second {
			t.Fatalf("RequestInfo took %s; want bounded by ~2s fallback timeout", elapsed)
		}

		if err := <-serverErr; err != nil {
			t.Fatalf("server error = %v", err)
		}
	})

	t.Run("serial_access", func(t *testing.T) {
		t.Parallel()
		// Two concurrent RequestInfo calls must serialize (both hold readMu+writeMu)
		// and not corrupt each other's results.
		client, server := net.Pipe()
		defer func() { _ = client.Close() }()
		defer func() { _ = server.Close() }()

		enh := transport.NewENHTransport(client, 500*time.Millisecond, 200*time.Millisecond)

		// Server side: respond to each INFO request with a 1-byte payload.
		// First request gets 0xAA, second gets 0xBB.
		go func() {
			buf := make([]byte, 2)
			responses := []byte{0xAA, 0xBB}
			for _, resp := range responses {
				if _, err := io.ReadFull(server, buf); err != nil {
					return
				}
				// Send INFO length=1, then INFO data=resp.
				lenFrame := transport.EncodeENH(transport.ENHResInfo, 0x01)
				dataFrame := transport.EncodeENH(transport.ENHResInfo, resp)
				payload := append(lenFrame[:], dataFrame[:]...)
				if _, err := server.Write(payload); err != nil {
					return
				}
			}
		}()

		var wg sync.WaitGroup
		results := make([][]byte, 2)
		errs := make([]error, 2)

		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				results[idx], errs[idx] = enh.RequestInfo(transport.AdapterInfoVersion)
			}(i)
		}
		wg.Wait()

		for i := 0; i < 2; i++ {
			if errs[i] != nil {
				t.Fatalf("RequestInfo[%d] error = %v", i, errs[i])
			}
			if len(results[i]) != 1 {
				t.Fatalf("RequestInfo[%d] payload len = %d; want 1", i, len(results[i]))
			}
		}
		// Due to serialization, one gets 0xAA and the other gets 0xBB.
		got := map[byte]bool{results[0][0]: true, results[1][0]: true}
		if !got[0xAA] || !got[0xBB] {
			t.Fatalf("RequestInfo results = {0x%02x, 0x%02x}; want {0xAA, 0xBB}", results[0][0], results[1][0])
		}
	})
}

func TestENHTransport_CloseDoesNotBlockBehindStalledWrite(t *testing.T) {
	// Close() no longer acquires writeMu — it closes conn directly, which
	// unblocks a stalled Write. Verify Close returns promptly even when
	// Write holds writeMu blocked on conn.Write.
	t.Parallel()

	// stallConn blocks Write on a channel so we can deterministically
	// know when Write is holding writeMu inside conn.Write.
	clientPipe, server := net.Pipe()
	defer func() { _ = server.Close() }()

	stallCh := make(chan struct{})
	stalled := &stallableConn{Conn: clientPipe, stallCh: stallCh}
	enh := transport.NewENHTransport(stalled, time.Second, time.Second)

	// Start a Write that will stall inside conn.Write (holding writeMu).
	writeErr := make(chan error, 1)
	go func() {
		_, err := enh.Write([]byte{0x42})
		writeErr <- err
	}()

	// Wait until the goroutine is blocked inside stallableConn.Write.
	<-stallCh

	// Close must complete promptly — it no longer waits for writeMu.
	done := make(chan error, 1)
	go func() {
		done <- enh.Close()
	}()

	select {
	case err := <-done:
		_ = err // Close may return an error from already-closed pipe, that is fine.
	case <-time.After(time.Second):
		t.Fatal("Close() blocked for >1s behind stalled Write")
	}

	// Unblock the stalled Write so the goroutine can exit.
	close(stallCh)
	<-writeErr
}

// stallableConn blocks the first Write on a channel to simulate a stalled
// conn.Write. The channel is signaled (receives) when Write enters, and
// the Write blocks until the channel is closed.
type stallableConn struct {
	net.Conn
	stallCh chan struct{}
	once    sync.Once
}

func (c *stallableConn) Write(b []byte) (int, error) {
	stalled := false
	c.once.Do(func() {
		// Signal that we are inside Write (caller knows writeMu is held).
		c.stallCh <- struct{}{}
		// Block until stallCh is closed.
		<-c.stallCh
		stalled = true
	})
	if stalled {
		// After unstalling, the conn is likely closed — propagate the error.
		return c.Conn.Write(b)
	}
	return c.Conn.Write(b)
}

// shortWriteConn wraps a net.Conn and returns short writes on demand.
type shortWriteConn struct {
	net.Conn
	shortWriteOnce atomic.Bool
}

func (c *shortWriteConn) Write(b []byte) (int, error) {
	if c.shortWriteOnce.CompareAndSwap(false, true) && len(b) > 1 {
		// Write only 1 byte out of the full buffer.
		return c.Conn.Write(b[:1])
	}
	return c.Conn.Write(b)
}

func TestENHTransport_XR_START_RequestStart_WriteAll_NoDoubleSend(t *testing.T) {
	// Fix 4: RequestStart checks n against len(seq) and returns an error
	// on short write instead of silently succeeding.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	wrapped := &shortWriteConn{Conn: client}
	enh := transport.NewENHTransport(wrapped, 200*time.Millisecond, 200*time.Millisecond)

	// Drain the server side so the write does not block.
	go func() {
		buf := make([]byte, 256)
		for {
			_, err := server.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	err := enh.RequestStart(0x10)
	if err == nil {
		t.Fatal("RequestStart returned nil on short write; want error")
	}
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("RequestStart error = %v; want ErrInvalidPayload", err)
	}
}

func TestENHTransport_XR_ENH_ParserReset_AfterReadTimeout(t *testing.T) {
	// Fix 5: StartArbitration resets parser on read timeout so that a
	// partial ENH frame (byte1 only) does not combine with the next byte.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 80*time.Millisecond, 200*time.Millisecond)
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
		// Send only byte1 of an ENH frame, then let timeout expire.
		if _, err := server.Write([]byte{0xC0}); err != nil {
			serverErr <- err
			return
		}
		// Wait for the timeout to fire.
		time.Sleep(120 * time.Millisecond)
		// Now send a raw data byte (0x42) on its own.
		// If parser was NOT reset, 0x42 would be combined with stale 0xC0
		// as byte2 and decoded as a frame. If parser WAS reset, 0x42 is
		// a plain raw byte (< 0x80) delivered as RECEIVED.
		if _, err := server.Write([]byte{0x42}); err != nil {
			serverErr <- err
			return
		}
		// Wait for ReadByte to consume it before closing.
		time.Sleep(50 * time.Millisecond)
		serverErr <- nil
	}()

	// StartArbitration should timeout (no STARTED/FAILED received).
	err := enh.StartArbitration(initiator)
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("StartArbitration error = %v; want ErrTimeout", err)
	}

	// After timeout, parser should be reset. ReadByte should return 0x42
	// as a plain data byte, not combined with stale byte1.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after timeout error = %v; want 0x42", err)
	}
	if got != 0x42 {
		t.Fatalf("ReadByte after timeout = 0x%02x; want 0x42 (parser should have been reset)", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_RequestInfoPendingEventsFloodBounded(t *testing.T) {
	// Fix 6: pendingEvents is capped at 256 during RequestInfo. Flooding
	// with RECEIVED frames must not grow the slice unboundedly.
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 200*time.Millisecond)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		// Read the INFO request.
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			serverErr <- err
			return
		}

		// Flood 1000 RECEIVED frames, then send a valid INFO response.
		received := transport.EncodeENH(transport.ENHResReceived, 0x55)
		for i := 0; i < 1000; i++ {
			if _, err := server.Write(received[:]); err != nil {
				serverErr <- err
				return
			}
		}

		// Complete the INFO response: length=1, data=0xAA.
		length := transport.EncodeENH(transport.ENHResInfo, 0x01)
		data := transport.EncodeENH(transport.ENHResInfo, 0xAA)
		response := append(length[:], data[:]...)
		_, err := server.Write(response)
		serverErr <- err
	}()

	got, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err != nil {
		t.Fatalf("RequestInfo error = %v", err)
	}
	if len(got) != 1 || got[0] != 0xAA {
		t.Fatalf("RequestInfo payload = %v; want [0xAA]", got)
	}

	// Count how many RECEIVED bytes are buffered. Must not exceed 256.
	count := 0
	for {
		_, readErr := enh.ReadByte()
		if readErr != nil {
			if errors.Is(readErr, ebuserrors.ErrTimeout) {
				break
			}
			t.Fatalf("ReadByte error = %v (after %d bytes)", readErr, count)
		}
		count++
		if count > 300 {
			t.Fatalf("pendingEvents exceeded 300; cap is not enforced")
		}
	}
	// maxPendingEvents = 256 (internal constant).
	if count > 256 {
		t.Fatalf("drained %d pending bytes; want <= 256 (maxPendingEvents)", count)
	}
	if count == 0 {
		t.Fatal("drained 0 pending bytes; expected some buffered RECEIVED events")
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestENHTransport_XR_ENH_UnknownCommand_ExplicitError(t *testing.T) {
	// Fix 7: DecodeENH rejects unknown command nibbles with ErrInvalidPayload.
	t.Parallel()

	t.Run("decode_level", func(t *testing.T) {
		// Command nibble 0x04 is not defined in the ENH protocol.
		// 0xD1 = 1101_0001: byte1 marker 0xC0 | (cmd=0x04 << 2) | data_hi=0x01
		// 0x80 = 1000_0000: byte2 marker 0x80 | data_lo=0x00
		_, err := transport.DecodeENH(0xD1, 0x80)
		if err == nil {
			t.Fatal("DecodeENH(0xD1, 0x80) returned nil error; want ErrInvalidPayload")
		}
		if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
			t.Fatalf("DecodeENH(0xD1, 0x80) error = %v; want ErrInvalidPayload", err)
		}
	})

	t.Run("live_path_explicit_error", func(t *testing.T) {
		// XR_ENH_UnknownCommand_LivePath_ExplicitError: feed unknown ENH
		// command through live transport and verify ReadByte surfaces
		// ErrInvalidPayload explicitly as the first result. Bytes appearing
		// AFTER the protocol violation in the same read are NOT delivered
		// before the error — crisp fault boundary.
		client, server := net.Pipe()
		defer func() { _ = client.Close() }()
		defer func() { _ = server.Close() }()

		enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)

		go func() {
			// Unknown command (0xD1, 0x80) followed by what would be a valid
			// RECEIVED(0x42) — the 0x42 byte must NOT leak before the error.
			recv := transport.EncodeENH(transport.ENHResReceived, 0x42)
			payload := append([]byte{0xD1, 0x80}, recv[:]...)
			_, _ = server.Write(payload)
		}()

		// First ReadByte surfaces the explicit protocol violation.
		// No valid bytes precede it in this buffer (unknown command is the first frame).
		_, err := enh.ReadByte()
		if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
			t.Fatalf("ReadByte[0] on unknown command = %v; want ErrInvalidPayload (explicit, first)", err)
		}
	})
}

// TestENHTransport_XR_ENH_RESETTED_AlwaysSurfacesBoundary verifies that
// RESETTED frames surface as StreamEventReset to ReadEvent consumers
// regardless of whether the transport was constructed with a reconnect
// dialFunc. Reconnect may be optional; boundary notification is not.
func TestENHTransport_XR_ENH_RESETTED_AlwaysSurfacesBoundary(t *testing.T) {
	t.Parallel()

	t.Run("no_dialFunc", func(t *testing.T) {
		client, server := net.Pipe()
		defer func() { _ = client.Close() }()
		defer func() { _ = server.Close() }()

		enh := transport.NewENHTransport(client, 200*time.Millisecond, 200*time.Millisecond)
		reader := interface{}(enh).(transport.StreamEventReader)

		go func() {
			reset := transport.EncodeENH(transport.ENHResResetted, 0x00)
			_, _ = server.Write(reset[:])
		}()

		event, err := reader.ReadEvent()
		if err != nil {
			t.Fatalf("ReadEvent error = %v; want StreamEventReset", err)
		}
		if event.Kind != transport.StreamEventReset {
			t.Fatalf("ReadEvent = %+v; want StreamEventReset (boundary surfaced without dialFunc)", event)
		}
	})

	// The XR invariant (boundary not optional) is proven by no_dialFunc:
	// boundary surfaces as StreamEventReset even when the transport is not
	// reconnect-capable. The reconnect-capable path is covered by existing
	// reconnect tests and is orthogonal to boundary notification.
}

// TestENHTransport_XR_Burst_BoundedBackpressure verifies that RECEIVED data
// events are capped by maxPendingEvents=256 — unbounded flood is not
// possible. The cap triggers when a caller holds the read path exclusively
// (RequestInfo pattern) while bus data floods the parser; ReadByte alone
// always drains as it reads, so the queue can't grow above 1 in steady state.
//
// The authoritative cap-enforcement proof is in
// TestENHTransport_RequestInfoPendingEventsFloodBounded, which floods 1000
// RECEIVED frames during a RequestInfo exchange (readMu held, no drain)
// and asserts drained count <= 256.
//
// This test verifies the lightweight invariant: during ReadByte steady-state
// consumption, a burst of RECEIVED bytes is delivered in order without loss
// below the cap.
func TestENHTransport_XR_Burst_BoundedBackpressure(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	// 200 RECEIVED events — below cap, all must be delivered in order.
	const burstSize = 200
	recv := transport.EncodeENH(transport.ENHResReceived, 0xAA)
	burst := make([]byte, 0, burstSize*2)
	for i := 0; i < burstSize; i++ {
		burst = append(burst, recv[:]...)
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := server.Write(burst)
		writeDone <- err
	}()

	timeout := time.After(3 * time.Second)
	for drained := 0; drained < burstSize; drained++ {
		select {
		case <-timeout:
			t.Fatalf("timed out after %d bytes", drained)
		default:
		}
		b, err := enh.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d] = %v", drained, err)
		}
		if b != 0xAA {
			t.Fatalf("ReadByte[%d] = 0x%02x; want 0xAA", drained, b)
		}
	}

	if err := <-writeDone; err != nil {
		t.Fatalf("writer error = %v", err)
	}
}

// TestENHTransport_DeferredErrClearedOnArbitrationTimeout verifies that a
// deferred parse error does not leak across arbitration lifecycle boundaries.
// If ReadByte buffered a valid byte + deferred ErrInvalidPayload, and then
// StartArbitration hits a timeout, subsequent ReadByte must not surface the
// stale payload error.
func TestENHTransport_DeferredErrClearedOnArbitrationTimeout(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 100*time.Millisecond, 100*time.Millisecond)

	// Server sends valid byte 0x11 + orphan byte 0x80 (parse error).
	// Then never responds to StartArbitration — forces arbitration timeout.
	go func() {
		_, _ = server.Write([]byte{0x11, 0x80})
		// Discard the StartArbitration write.
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)
		// No response — StartArbitration will time out.
	}()

	// Consume the valid byte — this triggers fillPendingLocked which queues
	// 0x11 and sets deferredErr = ErrInvalidPayload.
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte = %v; want 0x11", err)
	}
	if got != 0x11 {
		t.Fatalf("ReadByte = 0x%02x; want 0x11", got)
	}

	// StartArbitration hits a read timeout. This reset path must clear
	// deferredErr so it doesn't leak into the next ReadByte.
	err = enh.StartArbitration(0x10)
	if err == nil {
		t.Fatal("StartArbitration should timeout; got nil")
	}

	// Now read — the conn is effectively done; we expect either timeout
	// or EOF, but NOT the stale ErrInvalidPayload from before.
	_, err = enh.ReadByte()
	if errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("ReadByte after arbitration timeout = %v; deferred error leaked across boundary", err)
	}
}

// TestENHTransport_RequestStart_AsyncDropsPreGrantBytes verifies that
// RECEIVED bytes arriving in the async arbitration window are not leaked
// to pendingEvents before STARTED/FAILED. Blocking StartArbitration clears
// pendingEvents after grant; RequestStart achieves the same by using
// awaitingStart to filter the pre-grant window.
func TestENHTransport_RequestStart_AsyncDropsPreGrantBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	initiator := byte(0x10)

	go func() {
		// Consume the START request.
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)

		// Send pre-grant RECEIVED bytes (bus traffic observed before STARTED),
		// then STARTED, then post-grant RECEIVED byte (valid echo).
		preGrant1 := transport.EncodeENH(transport.ENHResReceived, 0x11)
		preGrant2 := transport.EncodeENH(transport.ENHResReceived, 0x22)
		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		postGrant := transport.EncodeENH(transport.ENHResReceived, 0x33)

		payload := append(preGrant1[:], preGrant2[:]...)
		payload = append(payload, started[:]...)
		payload = append(payload, postGrant[:]...)
		_, _ = server.Write(payload)
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	reader := interface{}(enh).(transport.StreamEventReader)

	// First event must be STARTED — pre-grant RECEIVED bytes (0x11, 0x22)
	// must NOT appear before it.
	event, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent[0] error = %v", err)
	}
	if event.Kind != transport.StreamEventStarted {
		t.Fatalf("ReadEvent[0] = %+v; want StreamEventStarted (pre-grant bytes must be dropped)", event)
	}
	if event.Data != initiator {
		t.Fatalf("ReadEvent[0].Data = 0x%02x; want 0x%02x", event.Data, initiator)
	}

	// Next event: post-grant RECEIVED(0x33) delivered as byte.
	event, err = reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent[1] error = %v", err)
	}
	if event.Kind != transport.StreamEventByte || event.Byte != 0x33 {
		t.Fatalf("ReadEvent[1] = %+v; want StreamEventByte(0x33)", event)
	}
}

// TestENHTransport_XR_ENH_0xAA_DataNotSYN verifies that the ENH transport
// delivers 0xAA as a data byte through ReadByte — it does NOT absorb or
// translate 0xAA into a SYN boundary event. SYN semantics are the
// protocol layer's concern; the transport is content-neutral.
func TestENHTransport_XR_ENH_0xAA_DataNotSYN(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	go func() {
		recv := transport.EncodeENH(transport.ENHResReceived, 0xAA)
		_, _ = server.Write(recv[:])
	}()

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v; want 0xAA as data byte", err)
	}
	if got != 0xAA {
		t.Fatalf("ReadByte = 0x%02x; want 0xAA (data byte, not SYN-translated)", got)
	}
}

// TestENHTransport_ControlEventNotDroppedUnderByteFlood verifies that
// STARTED is never silently dropped when pendingEvents is full of byte
// events. The queue must evict an oldest byte to make room for the
// control event. Matches the invariant: gateway waits exactly once per
// arbitration cycle, missing the STARTED means stuck arbitration.
func TestENHTransport_ControlEventNotDroppedUnderByteFlood(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 2*time.Second, 2*time.Second)

	// Flood pendingEvents via RequestInfo (holds readMu, no drain), then
	// send STARTED after the cap is exceeded. STARTED must still be
	// observable via ReadEvent after RequestInfo completes.
	go func() {
		buf := make([]byte, 2)
		// Consume INFO request.
		_, _ = io.ReadFull(server, buf)

		// Flood 300 RECEIVED bytes (> maxPendingEvents=256) + INFO response
		// + STARTED at the end — STARTED must survive byte flood.
		received := transport.EncodeENH(transport.ENHResReceived, 0x55)
		for i := 0; i < 300; i++ {
			_, _ = server.Write(received[:])
		}
		length := transport.EncodeENH(transport.ENHResInfo, 0x01)
		data := transport.EncodeENH(transport.ENHResInfo, 0x42)
		_, _ = server.Write(append(length[:], data[:]...))

		// Now flood more bytes then STARTED.
		for i := 0; i < 300; i++ {
			_, _ = server.Write(received[:])
		}
		started := transport.EncodeENH(transport.ENHResStarted, 0x10)
		_, _ = server.Write(started[:])
	}()

	_, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err != nil {
		t.Fatalf("RequestInfo error = %v", err)
	}

	// Drain byte events + control event. STARTED must appear eventually.
	reader := interface{}(enh).(transport.StreamEventReader)
	deadline := time.After(3 * time.Second)
	sawStarted := false
	for !sawStarted {
		select {
		case <-deadline:
			t.Fatal("STARTED was dropped under byte flood")
		default:
		}
		ev, err := reader.ReadEvent()
		if err != nil {
			if errors.Is(err, ebuserrors.ErrTimeout) {
				t.Fatal("STARTED was dropped — ReadEvent timed out")
			}
			t.Fatalf("ReadEvent error = %v", err)
		}
		if ev.Kind == transport.StreamEventStarted {
			sawStarted = true
			if ev.Data != 0x10 {
				t.Fatalf("StreamEventStarted.Data = 0x%02x; want 0x10", ev.Data)
			}
		}
	}
}

// TestENHTransport_AwaitingStartClearedOnParseError verifies that a
// parse error during the async arbitration window closes awaitingStart
// and surfaces the error immediately (not deferred behind byte backlog).
func TestENHTransport_AwaitingStartClearedOnParseError(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	go func() {
		// Consume START request.
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)
		// Send orphan byte2 (invalid) instead of STARTED.
		_, _ = server.Write([]byte{0x80})
	}()

	if err := enh.RequestStart(0x10); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	// ReadByte must surface ErrInvalidPayload immediately (not defer).
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("ReadByte = %v; want ErrInvalidPayload (bounded during awaitingStart)", err)
	}

	// Post-error: send valid byte, verify normal delivery (awaitingStart cleared).
	done := make(chan struct{})
	go func() {
		recv := transport.EncodeENH(transport.ENHResReceived, 0x42)
		_, _ = server.Write(recv[:])
		close(done)
	}()

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after parse error = %v; want 0x42 (awaitingStart cleared)", err)
	}
	if got != 0x42 {
		t.Fatalf("ReadByte = 0x%02x; want 0x42", got)
	}
	<-done
}

// TestENHTransport_AwaitingStartClearedOnTimeout verifies that a read
// timeout during the async arbitration window closes awaitingStart so
// subsequent reads do not silently drop RECEIVED bytes as pre-grant noise.
func TestENHTransport_AwaitingStartClearedOnTimeout(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 50*time.Millisecond, 500*time.Millisecond)

	go func() {
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)
		// No STARTED response — force timeout.
	}()

	if err := enh.RequestStart(0x10); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	// ReadByte triggers fillPendingLocked → timeout → should clear awaitingStart.
	_, _ = enh.ReadByte() // expect timeout

	// Now send a RECEIVED byte — it must be delivered (not dropped as
	// pre-grant noise, because awaitingStart was cleared on timeout).
	done := make(chan struct{})
	go func() {
		recv := transport.EncodeENH(transport.ENHResReceived, 0x77)
		_, _ = server.Write(recv[:])
		close(done)
	}()

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after RequestStart timeout = %v; want 0x77 (awaitingStart cleared)", err)
	}
	if got != 0x77 {
		t.Fatalf("ReadByte = 0x%02x; want 0x77", got)
	}
	<-done
}

// TestENHTransport_RequestInfo_ClosesArbitrationWindowOnSTARTED verifies
// that when RequestStart() has opened the async arbitration window and
// RequestInfo() consumes the STARTED response, the window is closed so
// subsequent RECEIVED bytes are delivered (not dropped as pre-grant).
// Copilot P1 on PR #135.
func TestENHTransport_RequestInfo_ClosesArbitrationWindowOnSTARTED(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	initiator := byte(0x31)

	go func() {
		// Consume the START request (RequestStart).
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)
		// Consume the INFO request.
		_, _ = io.ReadFull(server, buf)

		// Adapter sequence: STARTED (for our async arbitration) arrives
		// interleaved with the INFO response. RequestInfo must handle
		// STARTED and close the arbitration window.
		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		length := transport.EncodeENH(transport.ENHResInfo, 0x01)
		data := transport.EncodeENH(transport.ENHResInfo, 0x77)
		payload := append(started[:], length[:]...)
		payload = append(payload, data[:]...)
		_, _ = server.Write(payload)
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	got, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err != nil {
		t.Fatalf("RequestInfo error = %v", err)
	}
	if len(got) != 1 || got[0] != 0x77 {
		t.Fatalf("RequestInfo payload = %v; want [0x77]", got)
	}

	// Arbitration window must now be closed — next RECEIVED byte delivered.
	done := make(chan struct{})
	go func() {
		recv := transport.EncodeENH(transport.ENHResReceived, 0x42)
		_, _ = server.Write(recv[:])
		close(done)
	}()

	b, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after RequestInfo+STARTED = %v; want 0x42 (arbitration window closed)", err)
	}
	if b != 0x42 {
		t.Fatalf("ReadByte = 0x%02x; want 0x42", b)
	}
	<-done
}

// TestENHTransport_AwaitingStartClearedOnParseErrorNoDefer verifies that
// a parse error in fillPendingLocked clears awaitingStart even when the
// deferred-error path is taken (msgs queued before the error). Without
// this, a malformed frame before STARTED/FAILED would leave awaitingStart
// set and drop subsequent RECEIVED bytes. Copilot P2 on PR #135.
func TestENHTransport_AwaitingStartClearedOnParseErrorNoDefer(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	initiator := byte(0x10)

	// RequestStart opens the window. After it returns, we simulate a
	// malformed frame arriving (no STARTED). The window must close.
	go func() {
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)
		// Send orphan byte2 (0x80) — triggers ErrInvalidPayload in parser.
		// No valid msgs precede, so the "defer" branch is NOT taken.
		_, _ = server.Write([]byte{0x80})
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	// Surface the parse error.
	_, err := enh.ReadByte()
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("ReadByte = %v; want ErrInvalidPayload", err)
	}

	// Now send a legit RECEIVED byte. It must be delivered (awaitingStart cleared).
	done := make(chan struct{})
	go func() {
		recv := transport.EncodeENH(transport.ENHResReceived, 0x55)
		_, _ = server.Write(recv[:])
		close(done)
	}()

	b, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after parse error = %v; want 0x55 (awaitingStart cleared)", err)
	}
	if b != 0x55 {
		t.Fatalf("ReadByte = 0x%02x; want 0x55", b)
	}
	<-done
}

// TestENHTransport_ArbitrationWindowExpiresOnBusyBus verifies that the
// async arbitration window is force-closed by its deadline. After the
// deadline expires, the first RECEIVED byte that arrives is delivered
// (not dropped as pre-grant). Copilot P2 on PR #135 — permanent
// starvation scenario on a busy bus where STARTED/FAILED is lost.
func TestENHTransport_ArbitrationWindowExpiresOnBusyBus(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 2*time.Second, 2*time.Second)
	initiator := byte(0x10)

	// Server:
	// 1. Consumes the START request.
	// 2. Waits past the arbitration window deadline (500ms + margin).
	// 3. Sends one post-deadline RECEIVED byte.
	//    The reader should deliver this byte — proving the window was
	//    force-closed by its deadline (STARTED/FAILED never arrived).
	go func() {
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)

		// Sleep past the window deadline (arbitrationWindowTimeout=500ms).
		time.Sleep(700 * time.Millisecond)

		postExpiry := transport.EncodeENH(transport.ENHResReceived, 0xC3)
		_, _ = server.Write(postExpiry[:])
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	// ReadByte should return 0xC3 — the byte that arrived after the
	// arbitration window expired. If the deadline mechanism didn't work,
	// this byte would be dropped as pre-grant and ReadByte would block
	// (until the read deadline hits).
	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte = %v; want 0xC3 (window should expire, post-byte delivered)", err)
	}
	if got != 0xC3 {
		t.Fatalf("ReadByte = 0x%02x; want 0xC3 (post-expiry byte)", got)
	}
}
