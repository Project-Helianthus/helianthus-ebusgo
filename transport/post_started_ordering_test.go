//go:build !tinygo

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

// Tests covering requirement 4: StreamEventStarted/Failed ordering with
// ReadByte and ReadEvent after STARTED, against the runtime shape where
// arbitration succeeds but scan times out.

// TestENHTransport_ReadByte_SkipsSTARTED_PreservesResponseBytes verifies
// that when STARTED is queued in pendingEvents followed by RECEIVED bytes
// (the response), ReadByte skips STARTED and returns the response bytes
// in order. This is critical for the gateway path: Bus.readSymbol uses
// ReadByte after arbitration, so a stuck STARTED would never return bytes.
func TestENHTransport_ReadByte_SkipsSTARTED_PreservesResponseBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	// Server sends STARTED(0x10) followed by 3 RECEIVED bytes in one batch.
	// Simulates the adapter echoing STARTED alongside the first response
	// bytes — common when the target responds very quickly.
	go func() {
		started := transport.EncodeENH(transport.ENHResStarted, 0x10)
		recv1 := transport.EncodeENH(transport.ENHResReceived, 0xA1)
		recv2 := transport.EncodeENH(transport.ENHResReceived, 0xA2)
		recv3 := transport.EncodeENH(transport.ENHResReceived, 0xA3)
		payload := append(started[:], recv1[:]...)
		payload = append(payload, recv2[:]...)
		payload = append(payload, recv3[:]...)
		_, _ = server.Write(payload)
	}()

	// ReadByte must skip STARTED and deliver 0xA1, 0xA2, 0xA3 in order.
	expected := []byte{0xA1, 0xA2, 0xA3}
	for i, want := range expected {
		got, err := enh.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d] error = %v; want 0x%02x", i, err, want)
		}
		if got != want {
			t.Fatalf("ReadByte[%d] = 0x%02x; want 0x%02x", i, got, want)
		}
	}
}

// TestENHTransport_ReadByte_SkipsFAILED_PreservesSubsequentBytes is the
// symmetric case for FAILED. Bus.waitForSyn may see FAILED followed by
// SYN bytes after arbitration loss — ReadByte must skip FAILED and
// deliver the SYN bytes.
func TestENHTransport_ReadByte_SkipsFAILED_PreservesSubsequentBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	go func() {
		failed := transport.EncodeENH(transport.ENHResFailed, 0x15)
		recv := transport.EncodeENH(transport.ENHResReceived, 0xAA)
		payload := append(failed[:], recv[:]...)
		_, _ = server.Write(payload)
	}()

	got, err := enh.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte after FAILED = %v; want 0xAA (FAILED skipped)", err)
	}
	if got != 0xAA {
		t.Fatalf("ReadByte = 0x%02x; want 0xAA", got)
	}
}

// TestENHTransport_ReadEvent_STARTED_ThenResponseBytes verifies that
// event-aware consumers see STARTED first (arbitration grant) and
// RECEIVED bytes second. No reordering, no starvation.
func TestENHTransport_ReadEvent_STARTED_ThenResponseBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	initiator := byte(0x31)

	// Simulate RequestStart arbitration window: STARTED + RECEIVED(0x42) in
	// one batch. RequestStart (async path) is needed to exercise
	// awaitingStart semantics.
	go func() {
		// Consume the outbound START request.
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)

		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		recv := transport.EncodeENH(transport.ENHResReceived, 0x42)
		payload := append(started[:], recv[:]...)
		_, _ = server.Write(payload)
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	reader := interface{}(enh).(transport.StreamEventReader)

	// First event: STARTED.
	ev, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent[0] error = %v", err)
	}
	if ev.Kind != transport.StreamEventStarted {
		t.Fatalf("ReadEvent[0] = %+v; want StreamEventStarted", ev)
	}
	if ev.Data != initiator {
		t.Fatalf("STARTED.Data = 0x%02x; want 0x%02x", ev.Data, initiator)
	}

	// Second event: post-grant RECEIVED(0x42) as byte.
	ev, err = reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent[1] error = %v", err)
	}
	if ev.Kind != transport.StreamEventByte || ev.Byte != 0x42 {
		t.Fatalf("ReadEvent[1] = %+v; want StreamEventByte(0x42)", ev)
	}
}

// TestENHTransport_ControlEvents_NoReorderVsResponseBytes verifies the
// strict invariant: if the adapter emits STARTED then N response bytes,
// the consumer observes STARTED first and then all bytes in order, with
// no reordering caused by the control-event eviction policy.
func TestENHTransport_ControlEvents_NoReorderVsResponseBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	initiator := byte(0x31)

	// Single batch: STARTED, then 10 RECEIVED bytes in sequence.
	go func() {
		buf := make([]byte, 2)
		_, _ = io.ReadFull(server, buf)

		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		payload := append([]byte(nil), started[:]...)
		for i := 0; i < 10; i++ {
			recv := transport.EncodeENH(transport.ENHResReceived, byte(0xB0+i))
			payload = append(payload, recv[:]...)
		}
		_, _ = server.Write(payload)
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	reader := interface{}(enh).(transport.StreamEventReader)

	// STARTED first.
	ev, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent STARTED = %v", err)
	}
	if ev.Kind != transport.StreamEventStarted {
		t.Fatalf("first event = %+v; want StreamEventStarted", ev)
	}

	// Then 10 bytes in order 0xB0..0xB9.
	for i := 0; i < 10; i++ {
		ev, err := reader.ReadEvent()
		if err != nil {
			t.Fatalf("ReadEvent byte[%d] = %v", i, err)
		}
		want := byte(0xB0 + i)
		if ev.Kind != transport.StreamEventByte || ev.Byte != want {
			t.Fatalf("event[%d] = %+v; want StreamEventByte(0x%02x)", i, ev, want)
		}
	}
}

// TestENHTransport_XR_ENH_0xAA_ReadByte_PassThrough verifies the content-
// neutrality invariant: logical 0xAA is delivered as a data byte via
// ReadByte. The transport does NOT absorb 0xAA as a SYN boundary. This
// is essential so the bus layer can receive payloads containing 0xAA
// (after EG47 fix, CRC values and data bytes can legitimately be 0xAA).
func TestENHTransport_XR_ENH_0xAA_ReadByte_PassThrough(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	// Server sends a sequence where 0xAA appears as data — every byte must
	// be delivered, including 0xAA.
	payload := []byte{0x01, 0xAA, 0x02, 0xAA, 0xAA, 0x03}
	go func() {
		for _, b := range payload {
			recv := transport.EncodeENH(transport.ENHResReceived, b)
			_, _ = server.Write(recv[:])
		}
	}()

	for i, want := range payload {
		got, err := enh.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d] error = %v; want 0x%02x", i, err, want)
		}
		if got != want {
			t.Fatalf("ReadByte[%d] = 0x%02x; want 0x%02x (0xAA must pass through)", i, got, want)
		}
	}
}

// TestENHTransport_XR_ENH_0xAA_ReadEvent_PassThrough is the symmetric test
// for ReadEvent — 0xAA must arrive as StreamEventByte, not as an implicit
// SYN boundary.
func TestENHTransport_XR_ENH_0xAA_ReadEvent_PassThrough(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	go func() {
		recv := transport.EncodeENH(transport.ENHResReceived, 0xAA)
		_, _ = server.Write(recv[:])
	}()

	reader := interface{}(enh).(transport.StreamEventReader)
	ev, err := reader.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent error = %v; want StreamEventByte(0xAA)", err)
	}
	if ev.Kind != transport.StreamEventByte || ev.Byte != 0xAA {
		t.Fatalf("ReadEvent = %+v; want StreamEventByte(0xAA) (no SYN translation)", ev)
	}
}

// TestENHTransport_TimeoutIsErrTimeout_NotMisclassified verifies the
// transport returns ebuserrors.ErrTimeout on read deadline expiry, and
// not some other error class that the bus layer might miscategorize.
// This is the transport-layer counterpart to
// TestBus_PostStarted_TimeoutAfterArbitration.
func TestENHTransport_TimeoutIsErrTimeout_NotMisclassified(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 50*time.Millisecond, 50*time.Millisecond)

	_, err := enh.ReadByte()
	if err == nil {
		t.Fatal("ReadByte with no data = nil error; want timeout")
	}
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("ReadByte timeout error = %v; want ErrTimeout", err)
	}
	if errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatal("timeout misclassified as ErrBusCollision")
	}
	if errors.Is(err, ebuserrors.ErrNACK) {
		t.Fatal("timeout misclassified as ErrNACK")
	}
	if errors.Is(err, ebuserrors.ErrCRCMismatch) {
		t.Fatal("timeout misclassified as ErrCRCMismatch")
	}
	if errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatal("timeout misclassified as ErrInvalidPayload")
	}
}
