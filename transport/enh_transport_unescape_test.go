package transport_test

import (
	"errors"
	"net"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// F-23 (batch-19, 2026-05-13): integration tests verifying that the
// ENH transport now honestly unescapes eBUS byte-stuffing at every
// StreamEventByte emission, matching the BytesAreUnescaped() contract.
//
// These tests synthesize wire bytes by ENH-encoding each byte
// individually as an ENHResReceived frame and writing the encoded
// stream to the server side of a net.Pipe(). The transport reads via
// ReadEvent() and exposes the decoded byte plus the new WasEscaped
// flag for inspection.
//
// References:
//   - _work_adaptermux_audit/EBUSD-VERIFICATION-2026-05-13-batch19.md
//   - john30/ebusd/docs/enhanced_proto.md (escape rules)

// feedENHReceivedBytes pumps the ENHResReceived encoding of each
// raw wire byte to the server side of a net.Pipe. The transport's
// read path will pull these one frame at a time and present them to
// the F-23 escape decoder before the StreamEventByte emission. The
// helper closes the server side after the final byte so a blocking
// ReadEvent() unblocks deterministically.
func feedENHReceivedBytes(t *testing.T, server net.Conn, wire []byte) {
	t.Helper()
	encoded := make([]byte, 0, len(wire)*2)
	for _, b := range wire {
		seq := transport.EncodeENH(transport.ENHResReceived, b)
		encoded = append(encoded, seq[0], seq[1])
	}
	go func() {
		_, _ = server.Write(encoded)
	}()
}

// drainBytes pulls up to n StreamEventByte events via ReadEvent,
// failing the test if it cannot collect them within a generous
// timeout window. Non-byte events (Started/Failed/Reset) are skipped.
func drainBytes(t *testing.T, enh *transport.ENHTransport, n int) []transport.StreamEvent {
	t.Helper()
	out := make([]transport.StreamEvent, 0, n)
	deadline := time.Now().Add(2 * time.Second)
	for len(out) < n {
		if time.Now().After(deadline) {
			t.Fatalf("drainBytes: collected %d/%d events before timeout", len(out), n)
		}
		ev, err := enh.ReadEvent()
		if err != nil {
			t.Fatalf("ReadEvent: err=%v after %d events", err, len(out))
		}
		if ev.Kind == transport.StreamEventByte {
			out = append(out, ev)
		}
	}
	return out
}

// TestENH_Transport_UnescapesA9_00_ToSingleLogicalByte pins the
// `wire A9 00 → logical A9` rule at the transport boundary. Pre-F-23
// the consumer would have seen two separate StreamEventByte events
// (0xA9 then 0x00). Post-F-23 the consumer sees ONE event with
// Byte=0xA9 and WasEscaped=true.
func TestENH_Transport_UnescapesA9_00_ToSingleLogicalByte(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	feedENHReceivedBytes(t, server, []byte{0xA9, 0x00, 0x55})

	events := drainBytes(t, enh, 2)
	if events[0].Byte != 0xA9 || !events[0].WasEscaped {
		t.Fatalf("event[0] = {Byte=0x%02X WasEscaped=%v}; want {0xA9, true}",
			events[0].Byte, events[0].WasEscaped)
	}
	if events[1].Byte != 0x55 || events[1].WasEscaped {
		t.Fatalf("event[1] = {Byte=0x%02X WasEscaped=%v}; want {0x55, false}",
			events[1].Byte, events[1].WasEscaped)
	}
}

// TestENH_Transport_UnescapesA9_01_ToSyn pins the
// `wire A9 01 → logical AA` rule. The decoded byte is the SYN value
// carried as user payload; the consumer must be able to distinguish
// this from a real wire SYN (which arrives as raw 0xAA with
// WasEscaped=false) via the WasEscaped flag.
func TestENH_Transport_UnescapesA9_01_ToSyn(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	feedENHReceivedBytes(t, server, []byte{0xA9, 0x01, 0xAA})

	events := drainBytes(t, enh, 2)
	if events[0].Byte != 0xAA || !events[0].WasEscaped {
		t.Fatalf("event[0] = {Byte=0x%02X WasEscaped=%v}; want {0xAA, true} (escape-decoded data 0xAA)",
			events[0].Byte, events[0].WasEscaped)
	}
	if events[1].Byte != 0xAA || events[1].WasEscaped {
		t.Fatalf("event[1] = {Byte=0x%02X WasEscaped=%v}; want {0xAA, false} (raw wire SYN)",
			events[1].Byte, events[1].WasEscaped)
	}
}

// TestENH_Transport_PassthroughPlainBytes confirms that an unrelated
// stream (no 0xA9 leads) emits every byte unchanged with
// WasEscaped=false. Guards against any regression where the decoder
// over-eagerly flags raw bytes.
func TestENH_Transport_PassthroughPlainBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	wire := []byte{0x10, 0x08, 0xB5, 0x16, 0x01, 0x42, 0x77}
	feedENHReceivedBytes(t, server, wire)

	events := drainBytes(t, enh, len(wire))
	for i, ev := range events {
		if ev.Byte != wire[i] {
			t.Fatalf("event[%d] = 0x%02X; want 0x%02X", i, ev.Byte, wire[i])
		}
		if ev.WasEscaped {
			t.Fatalf("event[%d]: WasEscaped=true on raw byte 0x%02X", i, ev.Byte)
		}
	}
}

// TestENH_Transport_PreservesWasEscaped feeds a mixed stream where
// escape pairs and raw bytes alternate; every emission must carry
// the correct flag. This pins the per-byte propagation contract
// described in the StreamEvent docstring.
func TestENH_Transport_PreservesWasEscaped(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	// wire: A9 00 | 55 | A9 01 | AA | 33  → logical: A9* | 55 | AA* | AA | 33
	// (where * marks WasEscaped=true)
	feedENHReceivedBytes(t, server, []byte{0xA9, 0x00, 0x55, 0xA9, 0x01, 0xAA, 0x33})

	events := drainBytes(t, enh, 5)
	cases := []struct {
		Byte       byte
		WasEscaped bool
	}{
		{0xA9, true},
		{0x55, false},
		{0xAA, true},
		{0xAA, false},
		{0x33, false},
	}
	for i, want := range cases {
		got := events[i]
		if got.Byte != want.Byte || got.WasEscaped != want.WasEscaped {
			t.Fatalf("event[%d] = {Byte=0x%02X WasEscaped=%v}; want {0x%02X, %v}",
				i, got.Byte, got.WasEscaped, want.Byte, want.WasEscaped)
		}
	}
}

// TestENH_Transport_PatternA_CRC_A9_Roundtrip synthesizes the wire
// fragment from batch-19's Pattern A: a target response whose CRC=0xA9
// is wire-encoded as `0xA9 0x00`, followed by M_ACK (0x00) and SYN
// (0xAA). The transport MUST deliver three logical bytes — CRC,
// M_ACK, SYN — not four wire bytes.
//
// Pre-F-23 the consumer would have received [0xA9, 0x00, 0x00, 0xAA]
// and classified the second 0x00 as a spurious extra byte after CRC,
// producing the unexpected_symbol abandon flagged in batch-19.
func TestENH_Transport_PatternA_CRC_A9_Roundtrip(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	// Trailing wire fragment of a Pattern A frame:
	//   CRC(=0xA9, wire-encoded as 0xA9 0x00), M_ACK(=0x00), SYN(=0xAA).
	feedENHReceivedBytes(t, server, []byte{0xA9, 0x00, 0x00, 0xAA})

	events := drainBytes(t, enh, 3)
	want := []struct {
		Byte       byte
		WasEscaped bool
		note       string
	}{
		{0xA9, true, "CRC (escape-decoded from wire A9 00)"},
		{0x00, false, "M_ACK"},
		{0xAA, false, "trailing SYN"},
	}
	for i, w := range want {
		got := events[i]
		if got.Byte != w.Byte || got.WasEscaped != w.WasEscaped {
			t.Fatalf("event[%d] (%s) = {Byte=0x%02X WasEscaped=%v}; want {0x%02X, %v}",
				i, w.note, got.Byte, got.WasEscaped, w.Byte, w.WasEscaped)
		}
	}
}

// TestENH_Transport_PatternB_DataByte_A9_Roundtrip synthesizes the
// batch-19 Pattern B case: a 16-byte response payload whose 13th
// data byte equals 0xA9 (wire-encoded as 0xA9 0x00). The transport
// MUST deliver 16 logical bytes, with WasEscaped=true exactly at
// position 13.
//
// Pre-F-23 the consumer received 17 wire bytes and the response
// overrun guard fired with `unexpected_symbol`.
func TestENH_Transport_PatternB_DataByte_A9_Roundtrip(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	// Build a 16-byte logical payload with 0xA9 at index 13. Encode
	// to wire bytes per the F-23 spec: 0xA9 → 0xA9 0x00; other bytes
	// passthrough.
	logical := []byte{
		0x10, 0x30, 0x01, 0xFF, 0x00, 0x2C, 0x01, 0x00,
		0x80, 0x76, 0x02, 0x60, 0x02, 0xA9, 0x02, 0x40,
	}
	if logical[13] != 0xA9 {
		t.Fatalf("test fixture invariant violated: logical[13]=0x%02X; want 0xA9", logical[13])
	}
	wire := make([]byte, 0, len(logical)+1)
	for _, b := range logical {
		if b == 0xA9 {
			wire = append(wire, 0xA9, 0x00)
		} else {
			wire = append(wire, b)
		}
	}
	feedENHReceivedBytes(t, server, wire)

	events := drainBytes(t, enh, len(logical))
	for i, ev := range events {
		wantByte := logical[i]
		wantEscaped := wantByte == 0xA9
		if ev.Byte != wantByte || ev.WasEscaped != wantEscaped {
			t.Fatalf("event[%d] = {Byte=0x%02X WasEscaped=%v}; want {0x%02X, %v}",
				i, ev.Byte, ev.WasEscaped, wantByte, wantEscaped)
		}
	}
}

// TestENH_Transport_BytesAreUnescaped_HonestContract pins that the
// BytesAreUnescaped() boolean now matches reality. The function has
// always returned true; F-23 made the behavior actually conform. If
// the integration of the decoder regresses, the unescape tests above
// will fail — but pin the contract explicitly too.
func TestENH_Transport_BytesAreUnescaped_HonestContract(t *testing.T) {
	t.Parallel()

	client, _ := net.Pipe()
	defer func() { _ = client.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	if !enh.BytesAreUnescaped() {
		t.Fatal("BytesAreUnescaped() = false; want true (per ENH transport contract)")
	}
}

// TestENH_Transport_ResetClearsEscapeOnReconnect feeds a 0xA9 lead,
// triggers a transport reset (RESETTED frame in the steady-state
// read path), then feeds an unrelated byte. The decoder MUST NOT
// pair the pre-reset lead with the post-reset byte — if it did,
// the post-reset stream would silently corrupt every escape-decode
// across the reset boundary.
func TestENH_Transport_ResetClearsEscapeOnReconnect(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	// Feed: 0xA9 (lead, no emission), then a wire RESETTED control
	// frame, then a raw 0x55 byte.
	leadSeq := transport.EncodeENH(transport.ENHResReceived, 0xA9)
	resetSeq := transport.EncodeENH(transport.ENHResResetted, 0x01)
	zeroSeq := transport.EncodeENH(transport.ENHResReceived, 0x00)
	tailSeq := transport.EncodeENH(transport.ENHResReceived, 0x55)

	go func() {
		_, _ = server.Write([]byte{
			leadSeq[0], leadSeq[1],
			resetSeq[0], resetSeq[1],
			zeroSeq[0], zeroSeq[1],
			tailSeq[0], tailSeq[1],
		})
	}()

	// Drain events. Without F-23's Reset() in resetStateLocked, the
	// 0x00 immediately after the reset would falsely complete the
	// pre-reset lead pair and emit a phantom 0xA9 with
	// WasEscaped=true. Post-fix the 0x00 emits as plain passthrough.
	deadline := time.Now().Add(2 * time.Second)
	gotReset := false
	bytes := make([]transport.StreamEvent, 0, 3)
	for !gotReset || len(bytes) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: gotReset=%v bytes=%v", gotReset, bytes)
		}
		ev, err := enh.ReadEvent()
		if err != nil {
			t.Fatalf("ReadEvent: err=%v", err)
		}
		switch ev.Kind {
		case transport.StreamEventReset:
			gotReset = true
		case transport.StreamEventByte:
			bytes = append(bytes, ev)
		}
	}

	if !gotReset {
		t.Fatal("did not observe StreamEventReset; reconnect path did not surface boundary")
	}
	// Post-reset bytes must be plain passthrough.
	if bytes[0].Byte != 0x00 || bytes[0].WasEscaped {
		t.Fatalf("post-reset event[0] = {Byte=0x%02X WasEscaped=%v}; want {0x00, false} (pre-reset lead must have been cleared)",
			bytes[0].Byte, bytes[0].WasEscaped)
	}
	if bytes[1].Byte != 0x55 || bytes[1].WasEscaped {
		t.Fatalf("post-reset event[1] = {Byte=0x%02X WasEscaped=%v}; want {0x55, false}",
			bytes[1].Byte, bytes[1].WasEscaped)
	}
}

// TestENH_Transport_AwaitingStartDropDoesNotStrandEscape pins the
// fix for Codex bot P2 on PR-1: a 0xA9 lead emitted BEFORE
// awaitingStart=true, followed by its pair-completion byte 0x00
// that arrives WHILE awaitingStart=true (and is dropped from
// pendingEvents emission), MUST still advance the escape decoder.
// Without this guarantee, the next non-dropped post-grant byte
// would falsely complete the stale pair.
//
// Sequence:
//
//  1. Send wire 0xA9 outside any suppression window — decoder
//     captures `escape=true`, no event emitted yet.
//  2. RequestStart() opens awaitingStart=true.
//  3. Send wire 0x00 during awaitingStart — pair completes inside
//     the decoder (clearing `escape`), but pendingEvents emission
//     is suppressed (pre-grant traffic discard).
//  4. STARTED arrives, closing awaitingStart.
//  5. Send wire 0x42 — must emit plain 0x42 (WasEscaped=false),
//     NOT pair-with-stale-lead.
//
// Pre-P2-fix: step 3 bypasses the decoder, leaving `escape=true`.
// Step 5 then pairs 0xA9+0x42 → invalid pair → decodeFaultTotal++
// AND 0x42 is silently dropped — the test would observe no
// StreamEventByte event and timeout, or observe an unexpected
// decodeFaultTotal increment.
func TestENH_Transport_AwaitingStartDropDoesNotStrandEscape(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	initiator := byte(0x10)

	// Single-consumer pattern: one goroutine drives ReadEvent and
	// forwards everything to a channel. Avoids two competing
	// ReadEvent callers (which would deadlock on readMu).
	events := make(chan transport.StreamEvent, 16)
	readErrCh := make(chan error, 1)
	go func() {
		for {
			ev, err := enh.ReadEvent()
			if err != nil {
				readErrCh <- err
				return
			}
			events <- ev
		}
	}()

	// Step 1: emit the lone 0xA9 lead from the server. The reader
	// goroutine will pull it into the decoder (no event surfaces
	// because the decoder is mid-pair).
	preLead := transport.EncodeENH(transport.ENHResReceived, 0xA9)
	if _, err := server.Write(preLead[:]); err != nil {
		t.Fatalf("write preLead: %v", err)
	}

	// Server goroutine: from this point forward, the server side
	// is driven by the test. We need to read the START request
	// from the client, then send the dropped pair-completion byte
	// 0x00 (within the awaitingStart window), then STARTED, then a
	// plain post-grant byte 0x42.
	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		buf := make([]byte, 2)
		if _, err := readFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		// Drop byte: 0x00 wire. Arrives while awaitingStart=true.
		dropPair := transport.EncodeENH(transport.ENHResReceived, 0x00)
		if _, err := server.Write(dropPair[:]); err != nil {
			serverErr <- err
			return
		}
		// STARTED closes the awaitingStart window.
		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		if _, err := server.Write(started[:]); err != nil {
			serverErr <- err
			return
		}
		// Post-grant first byte: 0x42. Must emit plain
		// (WasEscaped=false). Closing the post-grant pre-echo
		// window with a non-SYN byte is the natural path.
		plain := transport.EncodeENH(transport.ENHResReceived, 0x42)
		_, err := server.Write(plain[:])
		serverErr <- err
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	// Collect events from the channel: STARTED + 0x42.
	deadline := time.After(2 * time.Second)
	gotStarted := false
	var firstByte *transport.StreamEvent
	for !gotStarted || firstByte == nil {
		select {
		case ev := <-events:
			switch ev.Kind {
			case transport.StreamEventStarted:
				gotStarted = true
			case transport.StreamEventByte:
				captured := ev
				firstByte = &captured
			}
		case err := <-readErrCh:
			t.Fatalf("reader goroutine err=%v decodeFaultTotal=%d", err, enh.DecodeFaultTotal())
		case <-deadline:
			t.Fatalf("timeout: gotStarted=%v firstByte=%v decodeFaultTotal=%d (pre-fix: 0x42 paired with stale 0xA9 lead, faulted, no event)",
				gotStarted, firstByte, enh.DecodeFaultTotal())
		}
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}

	if firstByte.Byte != 0x42 || firstByte.WasEscaped {
		t.Fatalf("post-grant event = {Byte=0x%02X WasEscaped=%v}; want {0x42, false} (P2 fix: dropped 0x00 advanced the decoder so the lead was consumed; 0x42 emits plain)",
			firstByte.Byte, firstByte.WasEscaped)
	}
	if got := enh.DecodeFaultTotal(); got != 0 {
		t.Fatalf("DecodeFaultTotal = %d; want 0 (P2 fix: dropped byte advanced decoder cleanly, no invalid-pair fault)", got)
	}
}

// TestENH_Transport_EscapePersistsAcrossConnReadBoundary pins the
// Codex bot P3 follow-up: the escape decoder lives on the transport
// struct, so an A9 lead arriving in one conn.Read must correctly
// pair with the 0x00/0x01 in the NEXT conn.Read. net.Pipe is
// synchronous, so writing the lead frame on its own forces a
// separate transport-side conn.Read; the pair-completion frame on
// the next Write happens in a distinct Read call.
func TestENH_Transport_EscapePersistsAcrossConnReadBoundary(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)

	leadSeq := transport.EncodeENH(transport.ENHResReceived, 0xA9)
	pairSeq := transport.EncodeENH(transport.ENHResReceived, 0x01)

	// Server writes the lead frame, waits, then writes the pair-
	// completion frame in a separate Write. Each Write blocks on
	// its counterpart Read (net.Pipe synchronous semantics), which
	// forces the transport to do TWO distinct conn.Read calls.
	go func() {
		_, _ = server.Write(leadSeq[:])
		// Brief sleep guarantees the transport has begun a fresh
		// fillPendingLocked Read call before the pair arrives.
		time.Sleep(20 * time.Millisecond)
		_, _ = server.Write(pairSeq[:])
	}()

	events := drainBytes(t, enh, 1)
	if events[0].Byte != 0xAA || !events[0].WasEscaped {
		t.Fatalf("event = {Byte=0x%02X WasEscaped=%v}; want {0xAA, true} (escape pair must complete across conn.Read boundary)",
			events[0].Byte, events[0].WasEscaped)
	}
}

// TestENH_Transport_ReadByteWithEscape_ExposesProvenanceFlag pins
// the F-23 (Codex bot review on PR-1) fix that adds the
// EscapeFlaggedReader interface. The transport must surface
// WasEscaped to SYN-comparison consumers (waitForSyn) so an escape-
// decoded payload 0xAA can be distinguished from a real wire SYN.
//
// Feeds: A9 00 (-> logical A9, WasEscaped=true)
//
//	A9 01 (-> logical AA, WasEscaped=true)
//	AA    (-> logical AA, WasEscaped=false — real wire SYN)
func TestENH_Transport_ReadByteWithEscape_ExposesProvenanceFlag(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	feedENHReceivedBytes(t, server, []byte{0xA9, 0x00, 0xA9, 0x01, 0xAA})

	cases := []struct {
		Byte       byte
		WasEscaped bool
		note       string
	}{
		{0xA9, true, "escape-decoded data byte 0xA9"},
		{0xAA, true, "escape-decoded data byte 0xAA (NOT a SYN)"},
		{0xAA, false, "raw wire SYN"},
	}
	for i, w := range cases {
		got, wasEscaped, err := enh.ReadByteWithEscape()
		if err != nil {
			t.Fatalf("ReadByteWithEscape[%d]: err=%v", i, err)
		}
		if got != w.Byte || wasEscaped != w.WasEscaped {
			t.Fatalf("ReadByteWithEscape[%d] (%s) = {Byte=0x%02X WasEscaped=%v}; want {0x%02X, %v}",
				i, w.note, got, wasEscaped, w.Byte, w.WasEscaped)
		}
	}
}

// TestENH_Transport_ReadByte_DiscardsEscapeFlag confirms the legacy
// ReadByte() shape is preserved: same bytes as ReadByteWithEscape,
// minus the flag. Backward compatible for callers that don't care.
func TestENH_Transport_ReadByte_DiscardsEscapeFlag(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	feedENHReceivedBytes(t, server, []byte{0xA9, 0x00, 0xA9, 0x01, 0xAA})

	want := []byte{0xA9, 0xAA, 0xAA}
	for i, wantByte := range want {
		got, err := enh.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d]: err=%v", i, err)
		}
		if got != wantByte {
			t.Fatalf("ReadByte[%d] = 0x%02X; want 0x%02X", i, got, wantByte)
		}
	}
}

// TestENH_Transport_StartArbitrationDiscardDoesNotStrandEscape pins
// the second-pass Codex P2 fix: blocking StartArbitration was also
// dropping ENHResReceived bytes without feeding the decoder. This
// test repeats the awaitingStart-drop scenario but via the blocking
// arbitration path (StartArbitration) rather than the async path
// (RequestStart).
//
// The blocking path resets the escape decoder unconditionally at
// arbitration completion, so even a stranded lead is cleared. We
// verify the post-grant byte is plain and no fault is recorded.
func TestENH_Transport_StartArbitrationDiscardDoesNotStrandEscape(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 500*time.Millisecond, 500*time.Millisecond)
	initiator := byte(0x10)

	// Server: read START request, write a discarded RECEIVED byte
	// (0xA9 lead) DURING arbitration, then STARTED.
	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		buf := make([]byte, 2)
		if _, err := readFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		// Discarded byte during arbitration: a wire 0xA9 lead. With
		// the P2 fix, this advances the decoder (escape=true) before
		// the byte is discarded.
		leadSeq := transport.EncodeENH(transport.ENHResReceived, 0xA9)
		if _, err := server.Write(leadSeq[:]); err != nil {
			serverErr <- err
			return
		}
		// STARTED — arbitration completion resets the decoder
		// unconditionally (defense-in-depth), wiping any in-flight
		// state.
		started := transport.EncodeENH(transport.ENHResStarted, initiator)
		_, err := server.Write(started[:])
		serverErr <- err
	}()

	if err := enh.StartArbitration(initiator); err != nil {
		t.Fatalf("StartArbitration error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}

	// Post-arbitration: feed a plain 0x42 and confirm it emits as
	// raw passthrough (decoder state was reset).
	plain := transport.EncodeENH(transport.ENHResReceived, 0x42)
	go func() {
		_, _ = server.Write(plain[:])
	}()
	events := drainBytes(t, enh, 1)
	if events[0].Byte != 0x42 || events[0].WasEscaped {
		t.Fatalf("post-arbitration event = {Byte=0x%02X WasEscaped=%v}; want {0x42, false} (decoder reset at arbitration completion)",
			events[0].Byte, events[0].WasEscaped)
	}
	if got := enh.DecodeFaultTotal(); got != 0 {
		t.Fatalf("DecodeFaultTotal = %d; want 0 (no invalid-pair fault)", got)
	}
}

// TestENH_Transport_StartArbitrationTimeoutResetsEscape pins the
// F-23 Codex bot r5 finding on PR #154: if the blocking
// StartArbitration discard path consumes a `0xA9` lead and then
// the conn.Read times out before the second pair byte arrives,
// the escape decoder MUST be reset. Otherwise the stale lead
// would pair with the first byte of any future read.
//
// Test flow:
//  1. Issue StartArbitration; server reads the START request.
//  2. Server writes a wire `0xA9` (an isolated lead, no follower).
//     This is the discarded pre-grant byte that feeds the decoder.
//  3. Server stops writing. Read times out. Bus returns the timeout.
//  4. Then send a wire 0x55 (plain byte). Verify it emits as logical
//     0x55 with WasEscaped=false — NOT paired with the stale lead.
//     If decoder.escape were stale, 0x55 would either fault (invalid
//     pair) or be wrongly paired.
func TestENH_Transport_StartArbitrationTimeoutResetsEscape(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 300*time.Millisecond, 300*time.Millisecond)
	initiator := byte(0x10)

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		// Read the START request from the client.
		buf := make([]byte, 2)
		if _, err := readFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		// Send a wire 0xA9 (isolated lead, no second byte).
		// Decoder will buffer escape=true; awaitingStart=true so
		// the byte is dropped from emission. Then we wait — the
		// conn.Read on the client side will time out, exercising
		// the F-23 r5 reset path.
		leadFrame := transport.EncodeENH(transport.ENHResReceived, 0xA9)
		_, _ = server.Write(leadFrame[:])
		// Don't send anything else for 600ms — long enough for
		// StartArbitration's read deadline (300ms) to fire twice.
		// The bus returns the timeout error after the first deadline.
	}()

	err := enh.StartArbitration(initiator)
	if err == nil {
		t.Fatal("StartArbitration err = nil; want timeout (server never sent STARTED/FAILED)")
	}
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("StartArbitration err = %v; want wrapped ErrTimeout", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}

	// Decoder state check: now feed a plain 0x55. The legacy stale
	// lead from step 2 should have been wiped by the timeout
	// reset. Expected: 0x55 emits as plain passthrough.
	plain := transport.EncodeENH(transport.ENHResReceived, 0x55)
	go func() {
		_, _ = server.Write(plain[:])
	}()
	got, wasEscaped, readErr := enh.ReadByteWithEscape()
	if readErr != nil {
		t.Fatalf("ReadByteWithEscape after timeout: err=%v", readErr)
	}
	if got != 0x55 || wasEscaped {
		t.Fatalf("after timeout: {Byte=0x%02X WasEscaped=%v}; want {0x55, false} (decoder.escape MUST have been reset on arbitration timeout)",
			got, wasEscaped)
	}
	if got := enh.DecodeFaultTotal(); got != 0 {
		t.Fatalf("DecodeFaultTotal = %d; want 0 (clean decode after reset)", got)
	}
}

// TestENH_Transport_RequestInfoAwaitingStartTimeoutResetsEscape pins the
// RequestInfo sibling of the blocking-arbitration timeout bug: if an async
// RequestStart window is open, RequestInfo feeds RECEIVED bytes through the
// decoder before dropping them as pre-grant traffic. A timeout that closes
// that window must reset a stale escape lead from discarded traffic.
func TestENH_Transport_RequestInfoAwaitingStartTimeoutResetsEscape(t *testing.T) {
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
		if _, err := readFull(server, buf); err != nil { // START
			serverErr <- err
			return
		}
		if _, err := readFull(server, buf); err != nil { // INFO
			serverErr <- err
			return
		}
		leadFrame := transport.EncodeENH(transport.ENHResReceived, 0xA9)
		_, _ = server.Write(leadFrame[:])
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}
	_, err := enh.RequestInfo(transport.AdapterInfoVersion)
	if err == nil {
		t.Fatal("RequestInfo err = nil; want timeout")
	}
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("RequestInfo err = %v; want wrapped ErrTimeout", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}

	plain := transport.EncodeENH(transport.ENHResReceived, 0x55)
	go func() {
		_, _ = server.Write(plain[:])
	}()
	got, wasEscaped, readErr := enh.ReadByteWithEscape()
	if readErr != nil {
		t.Fatalf("ReadByteWithEscape after RequestInfo timeout: err=%v", readErr)
	}
	if got != 0x55 || wasEscaped {
		t.Fatalf("after RequestInfo timeout: {Byte=0x%02X WasEscaped=%v}; want {0x55, false}",
			got, wasEscaped)
	}
	if got := enh.DecodeFaultTotal(); got != 0 {
		t.Fatalf("DecodeFaultTotal = %d; want 0 (clean decode after RequestInfo awaitingStart timeout reset)", got)
	}
}

// TestENH_Transport_AwaitingStartExpiryBoundaryResetsEscape pins the
// F-23 Codex bot r6 finding on PR #154: an async RequestStart window
// can be force-closed at the moment of a fresh byte arrival when
// the deadline has expired. If the decoder retains a stale 0xA9
// lead from a dropped pre-grant byte, feeding the boundary byte
// through the decoder BEFORE clearing the arbitration state would
// pair it with the stale lead — emitting a false 0xA9/0xAA or
// faulting on an invalid pair.
//
// Test flow:
//  1. RequestStart opens awaitingStart with a short deadline.
//  2. Server sends a wire 0xA9 (lead). Decoder buffers escape=true,
//     awaitingStart drops emission per the round-2 invariant.
//  3. Test sleeps past the arbitration deadline (no second byte
//     arrives during the window).
//  4. Server sends a wire 0x55 (the boundary byte that triggers
//     the expired-window fall-through).
//  5. Test reads via ReadByteWithEscape and asserts the byte
//     emerges as plain 0x55 (WasEscaped=false), NOT paired with
//     the stale lead.
func TestENH_Transport_AwaitingStartExpiryBoundaryResetsEscape(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	enh := transport.NewENHTransport(client, 2*time.Second, 500*time.Millisecond)
	initiator := byte(0x10)

	// Drive RequestStart on a goroutine: it writes the START frame
	// and returns immediately (non-blocking). awaitingStart is set
	// asynchronously by the transport.
	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		// Server reads the START request from the client to
		// unblock RequestStart's writer.
		buf := make([]byte, 2)
		if _, err := readFull(server, buf); err != nil {
			serverErr <- err
			return
		}
		// Send a wire 0xA9 lead during the awaitingStart window.
		// Decoder buffers escape=true; awaitingStart drops the
		// emission per the round-2 invariant.
		lead := transport.EncodeENH(transport.ENHResReceived, 0xA9)
		if _, err := server.Write(lead[:]); err != nil {
			serverErr <- err
			return
		}
		// Wait past the arbitration deadline (default 500ms per
		// arbitrationWindowTimeout). 700ms is comfortable margin.
		time.Sleep(700 * time.Millisecond)
		// Now send the boundary byte. The transport's read loop
		// will see awaitingStart=true AND deadline expired → wipe
		// arbitration state (including escDecoder) BEFORE feeding
		// the byte → 0x55 emits plain.
		boundary := transport.EncodeENH(transport.ENHResReceived, 0x55)
		_, err := server.Write(boundary[:])
		serverErr <- err
	}()

	if err := enh.RequestStart(initiator); err != nil {
		t.Fatalf("RequestStart error = %v", err)
	}

	got, wasEscaped, err := enh.ReadByteWithEscape()
	if err != nil {
		t.Fatalf("ReadByteWithEscape: err=%v", err)
	}
	if got != 0x55 || wasEscaped {
		t.Fatalf("post-expiry boundary byte = {Byte=0x%02X WasEscaped=%v}; want {0x55, false} (decoder MUST be reset BEFORE feeding the expired-window boundary byte)",
			got, wasEscaped)
	}
	if got := enh.DecodeFaultTotal(); got != 0 {
		t.Fatalf("DecodeFaultTotal = %d; want 0 (clean decode after expiry boundary reset)", got)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

// readFull is a small io.ReadFull-equivalent for the test helpers
// above without dragging the full io package into our import list.
func readFull(r net.Conn, buf []byte) (int, error) {
	read := 0
	for read < len(buf) {
		n, err := r.Read(buf[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}
