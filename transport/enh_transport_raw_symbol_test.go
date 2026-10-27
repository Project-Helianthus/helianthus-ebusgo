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

// Issue helianthus-ebusgateway#1003: a proxy that forwards the ENH symbol
// stream to downstream ENH clients needs every observed RECEIVED symbol in
// order, and an escape lead before its suffix exists. These tests drive a
// real ENHTransport over net.Pipe and write each adapter chunk only after the
// previous item was read, so "before the suffix" is enforced by construction
// rather than by timing.

type rawItem struct {
	ev        transport.StreamEvent
	symbol    byte
	hasSymbol bool
}

func newRawPipeTransport(t *testing.T) (*transport.ENHTransport, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	// The read timeout only bounds a broken test; no assertion depends on it.
	tr := transport.NewENHTransport(client, 2*time.Second, 2*time.Second)
	// A fixed escape clock keeps a lead and its separately written suffix
	// inside the escape-pending cap however slowly the test runs.
	fixed := time.Unix(0, 0)
	transport.SetEscapeClockForTest(tr, func() time.Time { return fixed })
	return tr, server
}

// adapterWrite writes one adapter->host chunk. net.Pipe completes the write
// only after the transport consumed it, and every chunk here fits one read.
func adapterWrite(server net.Conn, chunk []byte) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := server.Write(chunk)
		done <- err
	}()
	return done
}

func receivedFrames(symbols ...byte) []byte {
	out := make([]byte, 0, 2*len(symbols))
	for _, symbol := range symbols {
		seq := transport.EncodeENH(transport.ENHResReceived, symbol)
		out = append(out, seq[0], seq[1])
	}
	return out
}

func readRawItem(t *testing.T, tr *transport.ENHTransport) rawItem {
	t.Helper()
	ev, symbol, hasSymbol, err := tr.ReadEventWithRawSymbol()
	if err != nil {
		t.Fatalf("ReadEventWithRawSymbol: %v", err)
	}
	return rawItem{ev: ev, symbol: symbol, hasSymbol: hasSymbol}
}

func wantRaw(t *testing.T, got rawItem, symbol byte, logical transport.StreamEvent) {
	t.Helper()
	if !got.hasSymbol || got.symbol != symbol || got.ev != logical {
		t.Fatalf("item = {symbol=0x%02X has=%v ev=%+v}; want {symbol=0x%02X has=true ev=%+v}",
			got.symbol, got.hasSymbol, got.ev, symbol, logical)
	}
}

func wantControl(t *testing.T, got rawItem, ev transport.StreamEvent) {
	t.Helper()
	if got.hasSymbol || got.ev != ev {
		t.Fatalf("item = {symbol=0x%02X has=%v ev=%+v}; want control %+v", got.symbol, got.hasSymbol, got.ev, ev)
	}
}

func waitWrite(t *testing.T, done <-chan error) {
	t.Helper()
	if err := <-done; err != nil {
		t.Fatalf("adapter write: %v", err)
	}
}

func logicalByte(b byte, escaped bool) transport.StreamEvent {
	return transport.StreamEvent{Kind: transport.StreamEventByte, Byte: b, WasEscaped: escaped}
}

func TestENHRawSymbol_LeadIsReturnedBeforeSuffixExists(t *testing.T) {
	tr, server := newRawPipeTransport(t)

	done := adapterWrite(server, receivedFrames(0xA9))
	// Only the lead has been written. A logical-only reader would block here
	// until the suffix arrives; the raw reader must return the lead now.
	wantRaw(t, readRawItem(t, tr), 0xA9, transport.StreamEvent{})
	waitWrite(t, done)

	done = adapterWrite(server, receivedFrames(0x00))
	wantRaw(t, readRawItem(t, tr), 0x00, logicalByte(0xA9, true))
	waitWrite(t, done)
}

func TestENHRawSymbol_PayloadSynPairThenStructuralSyn(t *testing.T) {
	tr, server := newRawPipeTransport(t)

	done := adapterWrite(server, receivedFrames(0xA9, 0x01, 0xAA))
	wantRaw(t, readRawItem(t, tr), 0xA9, transport.StreamEvent{})
	wantRaw(t, readRawItem(t, tr), 0x01, logicalByte(0xAA, true))
	wantRaw(t, readRawItem(t, tr), 0xAA, logicalByte(0xAA, false))
	waitWrite(t, done)
}

func TestENHRawSymbol_ControlItemsKeepObservationOrder(t *testing.T) {
	tr, server := newRawPipeTransport(t)

	chunk := receivedFrames(0x10)
	started := transport.EncodeENH(transport.ENHResStarted, 0x31)
	chunk = append(chunk, started[0], started[1])
	chunk = append(chunk, receivedFrames(0xA9, 0x00)...)
	done := adapterWrite(server, chunk)

	wantRaw(t, readRawItem(t, tr), 0x10, logicalByte(0x10, false))
	wantControl(t, readRawItem(t, tr), transport.StreamEvent{Kind: transport.StreamEventStarted, Data: 0x31})
	wantRaw(t, readRawItem(t, tr), 0xA9, transport.StreamEvent{})
	wantRaw(t, readRawItem(t, tr), 0x00, logicalByte(0xA9, true))
	waitWrite(t, done)
}

func TestENHRawSymbol_DecodeFaultKeepsObservedSymbols(t *testing.T) {
	tr, server := newRawPipeTransport(t)

	// 0xA9 followed by 0x05 is not a valid pair: the logical stream drops
	// both symbols, the raw view still reports what was observed.
	done := adapterWrite(server, receivedFrames(0xA9, 0x05, 0x11))
	wantRaw(t, readRawItem(t, tr), 0xA9, transport.StreamEvent{})
	wantRaw(t, readRawItem(t, tr), 0x05, transport.StreamEvent{})
	wantRaw(t, readRawItem(t, tr), 0x11, logicalByte(0x11, false))
	waitWrite(t, done)
	if got := tr.DecodeFaultTotal(); got != 1 {
		t.Fatalf("DecodeFaultTotal = %d; want 1", got)
	}
}

func TestENHRawSymbol_LogicalReadersSkipRawItems(t *testing.T) {
	tr, server := newRawPipeTransport(t)

	done := adapterWrite(server, receivedFrames(0xA9))
	wantRaw(t, readRawItem(t, tr), 0xA9, transport.StreamEvent{})
	waitWrite(t, done)

	// Raw capture is now enabled. ReadEvent and ReadByte keep returning only
	// logical items: the raw items for 0x00 and 0x55 are skipped.
	done = adapterWrite(server, receivedFrames(0x00, 0x55))
	ev, err := tr.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent: %v", err)
	}
	if ev != logicalByte(0xA9, true) {
		t.Fatalf("ReadEvent = %+v; want logical 0xA9 escaped", ev)
	}
	b, err := tr.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte: %v", err)
	}
	if b != 0x55 {
		t.Fatalf("ReadByte = 0x%02X; want 0x55", b)
	}
	waitWrite(t, done)
}

func TestENHRawSymbol_ArrivalFiltersApplyToBothViews(t *testing.T) {
	tr, server := newRawPipeTransport(t)

	hostBytes := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(server, buf); err != nil {
			hostBytes <- nil
			return
		}
		hostBytes <- buf
	}()
	if err := tr.RequestStart(0x31); err != nil {
		t.Fatalf("RequestStart: %v", err)
	}
	if got := <-hostBytes; got == nil {
		t.Fatal("START request was not written")
	}

	// Pre-grant window: the foreign byte is dropped from both views, the
	// wire SYN surfaces as a raw symbol with its WireSyn marker.
	done := adapterWrite(server, receivedFrames(0x42, 0xAA))
	wantRaw(t, readRawItem(t, tr), 0xAA, transport.StreamEvent{Kind: transport.StreamEventWireSyn, Byte: 0xAA})
	waitWrite(t, done)

	// The grant opens the post-grant idle-SYN window: the idle 0xAA is
	// suppressed in both views, the first real echo passes.
	chunk := transport.EncodeENH(transport.ENHResStarted, 0x31)
	done = adapterWrite(server, append(chunk[:], receivedFrames(0xAA, 0x10)...))
	wantControl(t, readRawItem(t, tr), transport.StreamEvent{Kind: transport.StreamEventStarted, Data: 0x31})
	wantRaw(t, readRawItem(t, tr), 0x10, logicalByte(0x10, false))
	waitWrite(t, done)
}

func TestENHRawSymbol_FullReadKeepsEverySymbolPair(t *testing.T) {
	tr, server := newRawPipeTransport(t)

	// One 256-byte adapter read of short-form RECEIVED symbols produces 256
	// raw items and 256 logical items. The doubled raw-capture bound keeps
	// all of them; the logical-only bound alone would have dropped half.
	chunk := make([]byte, 256)
	for i := range chunk {
		chunk[i] = byte(i % 0x80)
	}
	done := adapterWrite(server, chunk)
	for i, symbol := range chunk {
		got := readRawItem(t, tr)
		if !got.hasSymbol || got.symbol != symbol || got.ev != logicalByte(symbol, false) {
			t.Fatalf("item %d = {symbol=0x%02X has=%v ev=%+v}; want 0x%02X with its logical byte",
				i, got.symbol, got.hasSymbol, got.ev, symbol)
		}
	}
	waitWrite(t, done)
}

func TestENHRawSymbol_ClosedTransportFailsClosed(t *testing.T) {
	tr, _ := newRawPipeTransport(t)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, _, _, err := tr.ReadEventWithRawSymbol()
	if !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("ReadEventWithRawSymbol after Close err = %v; want ErrTransportClosed", err)
	}
}
