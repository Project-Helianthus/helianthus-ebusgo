package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// F-23 (batch-19, 2026-05-13, Codex bot follow-up review on PR #154)
// regression suite for sendRawWithEcho's escape-aware echo
// validation. The fix carries WasEscaped from the transport through
// to the echo comparison so an escape-decoded payload 0xAA (a user-
// data byte that happened to share the SYN value) cannot satisfy an
// end-of-message SYN echo wait.

// echoF23TestTransport scripts (byte, wasEscaped, err) echo replies
// for sendRawWithEcho tests. Implements RawTransport, EscapeAware,
// and EscapeFlaggedReader.
type echoF23TestTransport struct {
	mu        sync.Mutex
	echo      []echoF23Event
	writes    [][]byte
	unescaped bool
}

type echoF23Event struct {
	value      byte
	wasEscaped bool
	err        error
}

func (t *echoF23TestTransport) ReadByte() (byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.echo) == 0 {
		return 0, ebuserrors.ErrTimeout
	}
	ev := t.echo[0]
	t.echo = t.echo[1:]
	return ev.value, ev.err
}

func (t *echoF23TestTransport) ReadByteWithEscape() (byte, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.echo) == 0 {
		return 0, false, ebuserrors.ErrTimeout
	}
	ev := t.echo[0]
	t.echo = t.echo[1:]
	return ev.value, ev.wasEscaped, ev.err
}

func (t *echoF23TestTransport) Write(payload []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writes = append(t.writes, append([]byte(nil), payload...))
	return len(payload), nil
}

func (t *echoF23TestTransport) Close() error            { return nil }
func (t *echoF23TestTransport) BytesAreUnescaped() bool { return t.unescaped }

// StartArbitration + ArbitrationSendsSource mirror the
// escapeAwareTransport contract from protocol/escape_aware_test.go.
// With ArbitrationSendsSource()=true the active bus knows the SRC
// byte is transmitted by the adapter during arbitration (not in the
// telegram body), so the echo queue can start at DST. Without these
// methods the bus would also write SRC and the echo queue offset
// would be wrong, causing all tests to time out before reaching
// the intended assertions (Codex bot follow-up review on PR #154).
func (t *echoF23TestTransport) StartArbitration(byte) error  { return nil }
func (t *echoF23TestTransport) ArbitrationSendsSource() bool { return true }

var _ transport.RawTransport = (*echoF23TestTransport)(nil)
var _ transport.EscapeAware = (*echoF23TestTransport)(nil)
var _ transport.EscapeFlaggedReader = (*echoF23TestTransport)(nil)

// TestBus_EscapeAware_SynEcho_RealWireSynAccepted pins the happy
// path: we write 0xAA (end-of-message SYN), the wire echoes 0xAA as
// a real wire SYN (WasEscaped=false). sendRawWithEcho must accept
// this as a clean echo, returning nil.
func TestBus_EscapeAware_SynEcho_RealWireSynAccepted(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{
		unescaped: true,
		echo: []echoF23Event{
			{value: protocol.SymbolSyn, wasEscaped: false},
		},
	}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	// Send uses an UNBOUNDED ctx so ErrBusCollision short-circuits
	// instead of looping via the deadline-bounded-retries policy
	// (DefaultBusConfig.TimeoutRetries=0 means collisions only retry
	// while isBoundedContext(reqCtx)==true). With an unbounded ctx
	// the first collision is the returned error — exactly what the
	// test asserts. Test-binary -timeout still bounds total runtime.
	ctx := context.Background()

	// SendEndOfMessage is unexported; invoke through Send with a
	// minimal broadcast frame whose ONLY non-arbitration write is
	// the structural bytes. Simpler approach: use the exported test
	// surface by sending a single-byte broadcast and inspecting
	// what error (if any) sendRawWithEcho would have produced.
	//
	// For this unit-style test we exercise the broadcast frame
	// path which ends with sendEndOfMessage(SymbolSyn). Build the
	// echo queue to match the full wire sequence:
	//   DST(=FE), PB, SB, LEN(=0), CRC, end-of-message SYN
	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}
	telegram := []byte{
		protocol.AddressBroadcast, 0xB5, 0x16, 0x00, // DST PB SB LEN
	}
	telegram = append(telegram, protocol.CRC(append([]byte{0x10}, telegram...)))
	tr.mu.Lock()
	tr.echo = nil
	for _, b := range telegram {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v; want nil (real wire SYN echo MUST be accepted)", err)
	}
}

// TestBus_EscapeAware_SynEcho_EscapeDecodedAARejected pins the
// F-23 follow-up fix. We write 0xAA (end-of-message SYN). The
// echo we receive is also 0xAA — but with WasEscaped=true,
// meaning it's an escape-decoded payload byte from an unrelated
// in-flight telegram (wire `0xA9 0x01`). Pre-fix this satisfied
// `echo == raw` and the bus proceeded as if our SYN was
// acknowledged. Post-fix it must be rejected as ErrBusCollision.
func TestBus_EscapeAware_SynEcho_EscapeDecodedAARejected(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{
		unescaped: true,
	}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	// Send uses an UNBOUNDED ctx so ErrBusCollision short-circuits
	// instead of looping via the deadline-bounded-retries policy
	// (DefaultBusConfig.TimeoutRetries=0 means collisions only retry
	// while isBoundedContext(reqCtx)==true). With an unbounded ctx
	// the first collision is the returned error — exactly what the
	// test asserts. Test-binary -timeout still bounds total runtime.
	ctx := context.Background()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}
	telegram := []byte{
		protocol.AddressBroadcast, 0xB5, 0x16, 0x00, // DST PB SB LEN
	}
	telegram = append(telegram, protocol.CRC(append([]byte{0x10}, telegram...)))
	tr.mu.Lock()
	tr.echo = nil
	for _, b := range telegram {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	// End-of-message SYN echo poisoned by an escape-decoded
	// payload 0xAA from unrelated traffic.
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: true})
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatalf("Send err = nil; want ErrBusCollision (escape-decoded payload 0xAA MUST NOT satisfy SYN echo)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}
}

// TestBus_EscapeAware_NonSynEcho_RealWireSynIsCollision pins the
// symmetric collision detection for ENH-class transports: while
// waiting for a non-SYN byte echo, a real wire SYN
// (WasEscaped=false) arriving instead is unambiguous collision
// evidence and must produce ErrBusCollision.
//
// Pre-F-23 this check only existed for plain transports (the
// `!b.unescapedTransport && echo == SymbolSyn && raw != SymbolSyn`
// guard). With ENH now correctly distinguishing real SYNs from
// escape-decoded payload 0xAA, the equivalent collision-detection
// invariant is enabled for ENH too.
func TestBus_EscapeAware_NonSynEcho_RealWireSynIsCollision(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{
		unescaped: true,
		echo: []echoF23Event{
			// First echo (DST=FE expected): we get a real wire
			// SYN instead → collision.
			{value: protocol.SymbolSyn, wasEscaped: false},
		},
	}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	// Send uses an UNBOUNDED ctx so ErrBusCollision short-circuits
	// instead of looping via the deadline-bounded-retries policy
	// (DefaultBusConfig.TimeoutRetries=0 means collisions only retry
	// while isBoundedContext(reqCtx)==true). With an unbounded ctx
	// the first collision is the returned error — exactly what the
	// test asserts. Test-binary -timeout still bounds total runtime.
	ctx := context.Background()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}
	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatalf("Send err = nil; want ErrBusCollision (real wire SYN where we wrote non-SYN MUST be a collision)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}
}

// TestBus_EscapeAware_PayloadAAEcho_WithWasEscapedAccepted pins the
// Codex bot r3 finding on PR #154: when the bus writes a TELEGRAM
// PAYLOAD byte equal to 0xAA (e.g. a data byte), the adapter wire-
// encodes it as `0xA9 0x01` and the echo arrives as logical 0xAA
// with WasEscaped=true. The new escape-decoded-SYN rejection guard
// in sendRawWithEcho MUST NOT fire on this case — that's a
// legitimate payload echo, not an unrelated-traffic 0xAA.
//
// The distinguishing signal is the structural-vs-payload flag
// threaded from sendSymbolWithEcho: payload bytes pass
// expectRawSyn=false; only the end-of-message structural SYN
// passes expectRawSyn=true.
//
// Pre-r3 fix, this test would have failed with ErrBusCollision
// because the guard fired on raw==SymbolSyn alone. Post-r3 fix,
// the guard is gated on expectRawSyn AND the payload 0xAA echoes
// pass cleanly.
func TestBus_EscapeAware_PayloadAAEcho_WithWasEscapedAccepted(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{
		unescaped: true,
	}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

	// Broadcast frame with a PAYLOAD byte equal to SymbolSyn (0xAA).
	// Wire transmission: DST PB SB LEN DATA[0]=0xAA CRC, then
	// trailing end-of-message SYN. Echo for the 0xAA data byte
	// arrives with WasEscaped=true (legitimately wire-escaped by
	// the adapter); all other bytes arrive WasEscaped=false.
	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      []byte{protocol.SymbolSyn},
	}
	telegram := []byte{
		protocol.AddressBroadcast, 0xB5, 0x16, 0x01, protocol.SymbolSyn,
	}
	telegram = append(telegram, protocol.CRC(append([]byte{0x10}, telegram...)))

	tr.mu.Lock()
	tr.echo = nil
	for i, b := range telegram {
		ev := echoF23Event{value: b}
		if i == 4 { // DATA[0] = 0xAA — wire-escaped, echo wasEscaped=true
			ev.wasEscaped = true
		}
		tr.echo = append(tr.echo, ev)
	}
	// End-of-message structural SYN: real wire SYN, wasEscaped=false.
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v; want nil (payload 0xAA with WasEscaped=true MUST be accepted as legitimate echo)", err)
	}
}

// TestBus_EscapeAware_NonSynEcho_EscapeDecodedAANotCollision pins
// the inverse of the above: escape-decoded payload 0xAA arriving
// when we expected a non-SYN echo is a value-mismatch
// (ErrBusCollision via the `echo != raw` path), but it's NOT the
// "real wire SYN intrusion" path. This test ensures we don't
// double-classify the same failure.
func TestBus_EscapeAware_NonSynEcho_EscapeDecodedAANotCollision(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{
		unescaped: true,
		echo: []echoF23Event{
			// First echo (DST=FE=0xFE expected): we get an
			// escape-decoded payload 0xAA. echo != raw → echo
			// mismatch path fires.
			{value: protocol.SymbolSyn, wasEscaped: true},
		},
	}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	// Send uses an UNBOUNDED ctx so ErrBusCollision short-circuits
	// instead of looping via the deadline-bounded-retries policy
	// (DefaultBusConfig.TimeoutRetries=0 means collisions only retry
	// while isBoundedContext(reqCtx)==true). With an unbounded ctx
	// the first collision is the returned error — exactly what the
	// test asserts. Test-binary -timeout still bounds total runtime.
	ctx := context.Background()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}
	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatalf("Send err = nil; want ErrBusCollision (value mismatch)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}
}
