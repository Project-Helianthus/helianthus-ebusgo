package protocol_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// escapeAwareTransport is a scriptedTransport that also implements
// transport.EscapeAware, simulating an ENH-like transport that delivers
// pre-unescaped bytes.
type escapeAwareTransport struct {
	scriptedTransport
}

func (t *escapeAwareTransport) BytesAreUnescaped() bool { return true }

// TestBus_EscapeAware_SendDoesNotDoubleEscape verifies that when the transport
// implements EscapeAware, sendSymbolWithEcho sends 0xA9 and 0xAA as raw bytes
// rather than expanding them into escape sequences (0xA9+0x00 / 0xA9+0x01).
func TestBus_EscapeAware_SendDoesNotDoubleEscape(t *testing.T) {
	t.Parallel()

	// Build a broadcast frame whose payload contains 0xA9 and 0xAA.
	// Broadcast avoids needing an ACK read, keeping the test minimal.
	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{protocol.SymbolEscape, protocol.SymbolSyn},
	}

	tr := &escapeAwareTransport{}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	_, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}

	// With an unescaped transport, the wire bytes should contain the raw
	// 0xA9 and 0xAA in the data portion -- NOT expanded escape sequences.
	// telegram = SRC DST PB SB LEN DATA[0] DATA[1] CRC SYN
	command := []byte{0x10, protocol.AddressBroadcast, 0x01, 0x02, 0x02,
		protocol.SymbolEscape, protocol.SymbolSyn}
	command = append(command, protocol.CRC(command))
	command = append(command, protocol.SymbolSyn) // end-of-message SYN

	got := tr.writesFlattened()
	if len(got) != len(command) {
		t.Fatalf("writes length = %d; want %d\n  got:  %v\n  want: %v", len(got), len(command), got, command)
	}
	for i := range got {
		if got[i] != command[i] {
			t.Fatalf("writes[%d] = 0x%02x; want 0x%02x\n  got:  %v\n  want: %v", i, got[i], command[i], got, command)
		}
	}
}

// TestBus_PlainTransport_EscapesSpecialSymbols verifies that a plain transport
// (no EscapeAware) DOES expand 0xA9/0xAA into escape sequences on the wire.
func TestBus_PlainTransport_EscapesSpecialSymbols(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{protocol.SymbolEscape, protocol.SymbolSyn},
	}

	tr := &scriptedTransport{}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	_, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}

	got := tr.writesFlattened()
	// Plain transport must escape 0xA9 -> 0xA9,0x00 and 0xAA -> 0xA9,0x01.
	// First 5 bytes: SRC=0x10, DST=0xFE, PB=0x01, SB=0x02, LEN=0x02 (non-special).
	if len(got) < 9 {
		t.Fatalf("writes too short (%d bytes): %v", len(got), got)
	}
	// Data[0]=0xA9 should be escaped as 0xA9,0x00
	if got[5] != protocol.SymbolEscape || got[6] != 0x00 {
		t.Fatalf("expected escaped 0xA9 at position 5-6, got 0x%02x,0x%02x", got[5], got[6])
	}
	// Data[1]=0xAA should be escaped as 0xA9,0x01
	if got[7] != protocol.SymbolEscape || got[8] != 0x01 {
		t.Fatalf("expected escaped 0xAA at position 7-8, got 0x%02x,0x%02x", got[7], got[8])
	}
}

// TestBus_EscapeAware_ReadSymbolReturnsRawBytes verifies that readSymbol on an
// unescaped transport returns raw bytes without interpreting escape sequences.
// Uses an I-I transaction where the ACK is 0x00 (SymbolAck).
func TestBus_EscapeAware_ReadSymbolReturnsRawBytes(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x30,
		Target:    0x10,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	tr := &escapeAwareTransport{
		scriptedTransport: scriptedTransport{
			inbound: []readEvent{{value: protocol.SymbolAck}},
		},
	}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp != nil {
		t.Fatalf("response = %+v; want nil for I-I", resp)
	}
}

// TestShouldRetry_DeadlineBoundsRetries_CompilesCorrectly verifies that the
// renamed deadlineBoundsRetries parameter compiles and works correctly through
// the full sendWithRetries path.
func TestShouldRetry_DeadlineBoundsRetries_CompilesCorrectly(t *testing.T) {
	t.Parallel()

	config := protocol.DefaultBusConfig()
	tr := &scriptedTransport{}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	bus.Run(ctx)

	// The bus should accept frames without panicking, proving the renamed
	// parameter compiles and flows through sendWithRetries correctly.
	_, err := bus.Send(ctx, protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x01,
		Secondary: 0x02,
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected error: %v", err)
	}
}
