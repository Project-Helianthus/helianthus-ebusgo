package transport_test

import (
	"errors"
	"testing"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/transport"
)

func TestLoopback_ReadWrite(t *testing.T) {
	t.Parallel()

	lb := transport.NewLoopback()
	defer lb.Close()

	payload := []byte{0x01, 0x02, 0x03}
	n, err := lb.Write(payload)
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write = %d; want %d", n, len(payload))
	}

	for i, want := range payload {
		got, err := lb.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d] error = %v", i, err)
		}
		if got != want {
			t.Fatalf("ReadByte[%d] = 0x%02x; want 0x%02x", i, got, want)
		}
	}
}

func TestLoopback_BlockingReadUnblocksOnWrite(t *testing.T) {
	t.Parallel()

	lb := transport.NewLoopback()
	defer lb.Close()

	readCh := make(chan byte, 1)
	errCh := make(chan error, 1)

	// Goroutine exits after ReadByte returns with a byte or error.
	go func() {
		b, err := lb.ReadByte()
		if err != nil {
			errCh <- err
			return
		}
		readCh <- b
	}()

	select {
	case b := <-readCh:
		t.Fatalf("ReadByte returned early with 0x%02x", b)
	case err := <-errCh:
		t.Fatalf("ReadByte returned early with error %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	if _, err := lb.Write([]byte{0xAA}); err != nil {
		t.Fatalf("Write error = %v", err)
	}

	select {
	case b := <-readCh:
		if b != 0xAA {
			t.Fatalf("ReadByte = 0x%02x; want 0xAA", b)
		}
	case err := <-errCh:
		t.Fatalf("ReadByte error = %v", err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout waiting for ReadByte")
	}
}

func TestLoopback_CloseUnblocksReadAndRejectsWrite(t *testing.T) {
	t.Parallel()

	lb := transport.NewLoopback()

	readErr := make(chan error, 1)
	// Goroutine exits after ReadByte returns with a byte or error.
	go func() {
		_, err := lb.ReadByte()
		readErr <- err
	}()

	if err := lb.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	select {
	case err := <-readErr:
		if !errors.Is(err, ebuserrors.ErrTransportClosed) {
			t.Fatalf("ReadByte error = %v; want ErrTransportClosed", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout waiting for ReadByte after close")
	}

	if _, err := lb.Write([]byte{0x01}); !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("Write error = %v; want ErrTransportClosed", err)
	}
}

func TestLoopback_DrainsBufferedBytesAfterClose(t *testing.T) {
	t.Parallel()

	lb := transport.NewLoopback()

	if _, err := lb.Write([]byte{0x11}); err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if err := lb.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	got, err := lb.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte error = %v", err)
	}
	if got != 0x11 {
		t.Fatalf("ReadByte = 0x%02x; want 0x11", got)
	}

	if _, err := lb.ReadByte(); !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadByte error = %v; want ErrTransportClosed", err)
	}
}
