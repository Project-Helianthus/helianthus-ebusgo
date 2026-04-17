package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// These tests cover the post-STARTED scan timeout shape observed in runtime:
// arbitration succeeds but the bus reports timeouts instead of completing the
// transaction. The tests are deterministic (no real sockets, no timing).

// enhMockTransport is a scriptedTransport that presents as ENS/ENH:
// - BytesAreUnescaped=true (ENH delivers pre-unescaped bytes)
// - StartArbitration succeeds (counted)
// - ArbitrationSendsSource=true (SRC not in wire telegram)
type enhMockTransport struct {
	scriptedTransport
	arbCount        int
	lastArbInitator byte
	arbErr          error
	arbMu           sync.Mutex
}

func (t *enhMockTransport) BytesAreUnescaped() bool      { return true }
func (t *enhMockTransport) ArbitrationSendsSource() bool { return true }

func (t *enhMockTransport) StartArbitration(initiator byte) error {
	t.arbMu.Lock()
	defer t.arbMu.Unlock()
	t.arbCount++
	t.lastArbInitator = initiator
	return t.arbErr
}

func (t *enhMockTransport) arbitrationCount() int {
	t.arbMu.Lock()
	defer t.arbMu.Unlock()
	return t.arbCount
}

// buildITTelegram builds the CRC-bearing initiator telegram for an I-T frame.
// Returns (full telegram including SRC, wire bytes without SRC).
func buildITTelegram(frame protocol.Frame) (full, wire []byte) {
	full = []byte{frame.Source, frame.Target, frame.Primary, frame.Secondary, byte(len(frame.Data))}
	full = append(full, frame.Data...)
	full = append(full, protocol.CRC(full))
	wire = full[1:] // skip SRC — arbitration already sent it
	return full, wire
}

// buildResponseBytes builds the response section {LEN, DATA..., CRC} for the
// target's reply to an I-T transaction.
func buildResponseBytes(data []byte) []byte {
	segment := append([]byte{byte(len(data))}, data...)
	crc := protocol.CRC(segment)
	return append(segment, crc)
}

// TestBus_PostStarted_SuccessAfterArbitration — requirement 1.
// After StartArbitration succeeds, the bus writes the request and receives
// the target's ACK + response + CRC. bus.Send returns success with the
// expected response Data. Control events must not leak into data.
func TestBus_PostStarted_SuccessAfterArbitration(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x31,
		Target:    0x08, // BAI — non-initiator-capable
		Primary:   0x05,
		Secondary: 0x03,
		Data:      []byte{},
	}
	respData := []byte{0x10, 0x20, 0x30}

	// Inbound from target: ACK, LEN, DATA..., CRC, ACK-echo for our ACK,
	// SYN-echo for end-of-message. Writes auto-generate their own echoes.
	inbound := []readEvent{
		{value: protocol.SymbolAck}, // target ACKs request
	}
	resp := buildResponseBytes(respData)
	for _, b := range resp {
		inbound = append(inbound, readEvent{value: b})
	}

	tr := &enhMockTransport{scriptedTransport: scriptedTransport{inbound: inbound}}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus.Run(ctx)

	response, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("bus.Send error = %v; want success after arbitration", err)
	}
	if response == nil {
		t.Fatal("response = nil; want non-nil response")
	}
	if len(response.Data) != len(respData) {
		t.Fatalf("response.Data length = %d; want %d", len(response.Data), len(respData))
	}
	for i, b := range respData {
		if response.Data[i] != b {
			t.Fatalf("response.Data[%d] = 0x%02x; want 0x%02x", i, response.Data[i], b)
		}
	}

	if tr.arbitrationCount() != 1 {
		t.Fatalf("arbitration count = %d; want 1", tr.arbitrationCount())
	}
	if tr.lastArbInitator != frame.Source {
		t.Fatalf("last arbitration initiator = 0x%02x; want 0x%02x (source)", tr.lastArbInitator, frame.Source)
	}

	// Verify wire bytes: SRC must NOT appear — arbitration carried it.
	_, wire := buildITTelegram(frame)
	got := tr.writesFlattened()
	// got = wire bytes (request) + ACK (we sent) + SYN (end-of-message)
	for i, b := range wire {
		if i >= len(got) {
			t.Fatalf("write[%d]: want 0x%02x; got truncated writes", i, b)
		}
		if got[i] != b {
			t.Fatalf("write[%d] = 0x%02x; want 0x%02x (SRC should NOT be on wire)", i, got[i], b)
		}
	}
	// First wire byte is DST — SRC must not be present as a duplicate.
	if len(got) > 0 && got[0] == frame.Source {
		t.Fatalf("write[0] = 0x%02x (source); ArbitrationSendsSource=true should skip SRC on wire", got[0])
	}
}

// TestBus_PostStarted_TimeoutAfterArbitration — requirement 2.
// Arbitration succeeds, request bytes are written, but no response bytes
// arrive. bus.Send must return a timeout error — not collision/nack/crc/
// invalid-payload. This matches the runtime shape: timeouts=164, others=0.
func TestBus_PostStarted_TimeoutAfterArbitration(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x31,
		Target:    0x08,
		Primary:   0x05,
		Secondary: 0x03,
		Data:      []byte{},
	}

	// Inbound is empty — scriptedTransport.ReadByte returns ErrTimeout when
	// both echo and inbound are drained. Writes are auto-echoed, so the
	// request goes out fine; ACK read will return ErrTimeout.
	tr := &enhMockTransport{}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus.Run(ctx)

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("bus.Send returned nil; want timeout error")
	}
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("bus.Send error = %v; want ErrTimeout (not collision/nack/crc/invalid)", err)
	}
	// Explicitly reject misclassification.
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

	if tr.arbitrationCount() < 1 {
		t.Fatal("arbitration count = 0; expected at least one StartArbitration call")
	}
}

// TestBus_PostStarted_SourceExcludedFromWireWithArbitrationSendsSource —
// requirement 3a. Verifies SRC is NOT written to the wire when the transport
// reports ArbitrationSendsSource=true.
func TestBus_PostStarted_SourceExcludedFromWireWithArbitrationSendsSource(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x31,
		Target:    0x08,
		Primary:   0x05,
		Secondary: 0x03,
		Data:      []byte{0xAA, 0xBB}, // non-trivial payload
	}

	// Provide an ACK so Send gets past the request-write phase.
	// After ACK, no response data → timeout on response length read, which
	// is fine for this test (we only inspect writes).
	inbound := []readEvent{
		{value: protocol.SymbolAck},
	}
	tr := &enhMockTransport{scriptedTransport: scriptedTransport{inbound: inbound}}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus.Run(ctx)

	_, _ = bus.Send(ctx, frame) // error is expected (response timeout)

	_, wire := buildITTelegram(frame)
	got := tr.writesFlattened()

	// The initial portion of writes must match the wire telegram (no SRC).
	if len(got) < len(wire) {
		t.Fatalf("writes truncated: got %d bytes, need at least %d", len(got), len(wire))
	}
	for i, b := range wire {
		if got[i] != b {
			t.Fatalf("write[%d] = 0x%02x; want 0x%02x", i, got[i], b)
		}
	}
	// First byte on wire must be DST, not SRC.
	if got[0] != frame.Target {
		t.Fatalf("first wire byte = 0x%02x; want DST=0x%02x (SRC must be omitted)", got[0], frame.Target)
	}
}

// plainMockTransport is like scriptedTransport but does NOT implement
// EscapeAware or arbitrationTransport — the plain-TCP path. includeSource=true.
type plainMockTransport struct {
	scriptedTransport
}

// TestBus_PostStarted_SourceIncludedOnPlainTransport — requirement 3b.
// On a plain transport (no StartArbitration, no BytesAreUnescaped), SRC
// must be written to the wire as the first byte.
func TestBus_PostStarted_SourceIncludedOnPlainTransport(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x31,
		Target:    0x08,
		Primary:   0x05,
		Secondary: 0x03,
		Data:      []byte{0x42},
	}

	inbound := []readEvent{
		{value: protocol.SymbolAck},
	}
	tr := &plainMockTransport{scriptedTransport: scriptedTransport{inbound: inbound}}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus.Run(ctx)

	_, _ = bus.Send(ctx, frame)

	full, _ := buildITTelegram(frame)
	got := tr.writesFlattened()
	if len(got) < len(full) {
		t.Fatalf("writes truncated: got %d, need %d", len(got), len(full))
	}
	if got[0] != frame.Source {
		t.Fatalf("first wire byte = 0x%02x; want SRC=0x%02x on plain transport", got[0], frame.Source)
	}
}

// TestBus_PostStarted_RequestBytesMatchTelegramFormat — requirement 3c.
// The encoded request bytes written after arbitration must match the eBUS
// telegram format: DST, PB, SB, LEN, DATA..., CRC.
func TestBus_PostStarted_RequestBytesMatchTelegramFormat(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0x07,
		Secondary: 0x04,
		Data:      []byte{0x01, 0x02, 0x03},
	}

	inbound := []readEvent{
		{value: protocol.SymbolAck},
	}
	tr := &enhMockTransport{scriptedTransport: scriptedTransport{inbound: inbound}}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus.Run(ctx)

	_, _ = bus.Send(ctx, frame)

	_, wire := buildITTelegram(frame)
	// wire = [DST, PB, SB, LEN=3, 0x01, 0x02, 0x03, CRC]
	got := tr.writesFlattened()
	if len(got) < len(wire) {
		t.Fatalf("writes truncated: got %d, need %d", len(got), len(wire))
	}
	for i, b := range wire {
		if got[i] != b {
			t.Fatalf("wire[%d] = 0x%02x; want 0x%02x", i, got[i], b)
		}
	}
	// Structural assertions.
	if got[0] != frame.Target {
		t.Fatalf("wire[0] (DST) = 0x%02x; want 0x%02x", got[0], frame.Target)
	}
	if got[1] != frame.Primary {
		t.Fatalf("wire[1] (PB) = 0x%02x; want 0x%02x", got[1], frame.Primary)
	}
	if got[2] != frame.Secondary {
		t.Fatalf("wire[2] (SB) = 0x%02x; want 0x%02x", got[2], frame.Secondary)
	}
	if got[3] != byte(len(frame.Data)) {
		t.Fatalf("wire[3] (LEN) = 0x%02x; want 0x%02x", got[3], len(frame.Data))
	}
}

// TestBus_PostStarted_RegressionScanTimeoutShape — requirement 6.
// Model the runtime shape: many sequential transactions, arbitration succeeds
// for each, request bytes written, no response → every transaction returns
// timeout (not parse error/collision/nack). No pending control-event backlog
// leaks into later transactions.
func TestBus_PostStarted_RegressionScanTimeoutShape(t *testing.T) {
	t.Parallel()

	const txCount = 20
	tr := &enhMockTransport{}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus.Run(ctx)

	var timeouts, collisions, nacks, crcs, invalids, others int
	for i := 0; i < txCount; i++ {
		frame := protocol.Frame{
			Source:    0x31,
			Target:    0x08,
			Primary:   0x05,
			Secondary: byte(i & 0xFF),
			Data:      []byte{},
		}
		txCtx, txCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, err := bus.Send(txCtx, frame)
		txCancel()
		if err == nil {
			t.Fatalf("tx %d: bus.Send returned nil; want timeout", i)
		}
		switch {
		case errors.Is(err, ebuserrors.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
			timeouts++
		case errors.Is(err, ebuserrors.ErrBusCollision):
			collisions++
		case errors.Is(err, ebuserrors.ErrNACK):
			nacks++
		case errors.Is(err, ebuserrors.ErrCRCMismatch):
			crcs++
		case errors.Is(err, ebuserrors.ErrInvalidPayload):
			invalids++
		default:
			others++
			t.Logf("tx %d: unexpected error class: %v", i, err)
		}
	}

	// Runtime shape: all timeouts, zero of anything else.
	if timeouts != txCount {
		t.Errorf("timeouts = %d; want %d (all transactions should time out)", timeouts, txCount)
	}
	if collisions != 0 {
		t.Errorf("collisions = %d; want 0 (arbitration succeeded for each)", collisions)
	}
	if nacks != 0 {
		t.Errorf("nacks = %d; want 0", nacks)
	}
	if crcs != 0 {
		t.Errorf("crcs = %d; want 0", crcs)
	}
	if invalids != 0 {
		t.Errorf("invalids = %d; want 0 (no parse errors)", invalids)
	}
	if others != 0 {
		t.Errorf("others = %d; want 0 (no unknown error classes)", others)
	}

	// Arbitration was attempted at least once per transaction.
	if tr.arbitrationCount() < txCount {
		t.Errorf("arbitration count = %d; want >= %d", tr.arbitrationCount(), txCount)
	}
}
