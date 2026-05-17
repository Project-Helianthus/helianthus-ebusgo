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

// TestBus_EscapeAware_PayloadAAEcho_RealWireSynIsCollision pins the
// Codex bot r4 finding on PR #154: symmetric to the round-3
// PayloadAAEcho_WithWasEscapedAccepted test. We write a TELEGRAM
// PAYLOAD byte equal to 0xAA (expectRawSyn=false). The legitimate
// echo would arrive WasEscaped=true (adapter wire-escaped). Instead,
// a real wire SYN (WasEscaped=false) arrives first — this is an
// unrelated bus event, NOT our payload echo, and MUST be rejected
// as ErrBusCollision.
//
// Pre-r4 fix, all four existing guards skipped: the ENH SYN-intrusion
// guard requires raw != SymbolSyn; the structural-SYN rejection
// requires expectRawSyn=true; the value-mismatch check has echo ==
// raw == 0xAA. Result: false accept. The new guard plugs that hole.
func TestBus_EscapeAware_PayloadAAEcho_RealWireSynIsCollision(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{
		unescaped: true,
	}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

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
	// First four bytes echo normally (DST PB SB LEN). The fifth
	// echo (DATA[0]=0xAA) is poisoned: a real wire SYN arrives
	// where the escape-decoded payload echo should have been. Bus
	// must reject as collision before reaching the value check.
	for i, b := range telegram[:4] {
		_ = i
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatalf("Send err = nil; want ErrBusCollision (real wire SYN where escape-decoded payload 0xAA was expected MUST be a collision)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}
}

// TestBus_EchoMismatchEvent_PropagatesEchoWasEscaped_True pins the
// batch-23 (2026-05-17) round-4 plumbing: the BusEventEchoMismatch
// emitted by sendRawWithEcho's escape-aware guards MUST carry
// EchoWasEscaped from the transport. Downstream consumers (gateway
// P10 echo_mismatch subclass classifier) rely on this to split the
// byte-value-only `pre_echo_syn` label into raw-SYN vs escape-
// decoded-data subclasses.
//
// Scenario: we write a TELEGRAM PAYLOAD byte 0xAA (expectRawSyn=
// false). Echo arrives as a REAL wire SYN (WasEscaped=false) —
// this is the round-4 round-4 guard at bus.go ~1054, which emits
// BusEventEchoMismatch with EchoWasEscaped=false (forwarded from
// the echoWasEscaped local).
func TestBus_EchoMismatchEvent_PropagatesEchoWasEscaped_False(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		if ev.Kind == protocol.BusEventEchoMismatch {
			observed = append(observed, ev)
		}
		return nil
	})

	cfg := protocol.DefaultBusConfig()
	cfg.Observer = observer
	bus := protocol.NewBus(tr, cfg, 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

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
	for _, b := range telegram[:4] {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	// Payload 0xAA position poisoned by real wire SYN.
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("setup: expected ErrBusCollision")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("setup: want wrapped ErrBusCollision, got %v", err)
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	if len(observed) == 0 {
		t.Fatal("no BusEventEchoMismatch observed")
	}
	// The FIRST echo-mismatch event is the per-byte emission from
	// sendRawWithEcho's r4 guard. Subsequent ones may come from
	// emitOutcomeEvent's centralized re-emit; we assert on the
	// first one (the per-byte direct emission).
	first := observed[0]
	if first.Byte != protocol.SymbolSyn {
		t.Fatalf("first event Byte = 0x%02X, want 0xAA", first.Byte)
	}
	if first.EchoWasEscaped {
		t.Fatalf("first event EchoWasEscaped = true, want false (a real wire SYN intrusion has WasEscaped=false on the wire)")
	}
}

// TestBus_EchoMismatchEvent_PropagatesEchoWasEscaped_True pins the
// other half: when we write a STRUCTURAL end-of-message SYN
// (expectRawSyn=true) and the echo arrives 0xAA with WasEscaped=
// true (escape-decoded payload 0xAA from unrelated traffic — the
// round-3 guard at bus.go ~1032), the emitted BusEventEchoMismatch
// MUST carry EchoWasEscaped=true so the downstream P10 classifier
// can attribute this to the "third-party frame payload" subclass
// rather than to a gateway-internal SYN-suppression leak.
func TestBus_EchoMismatchEvent_PropagatesEchoWasEscaped_True(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		if ev.Kind == protocol.BusEventEchoMismatch {
			observed = append(observed, ev)
		}
		return nil
	})

	cfg := protocol.DefaultBusConfig()
	cfg.Observer = observer
	bus := protocol.NewBus(tr, cfg, 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}
	telegram := []byte{
		protocol.AddressBroadcast, 0xB5, 0x16, 0x00,
	}
	telegram = append(telegram, protocol.CRC(append([]byte{0x10}, telegram...)))
	tr.mu.Lock()
	tr.echo = nil
	for _, b := range telegram {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	// End-of-message structural SYN slot poisoned by escape-decoded
	// payload 0xAA from unrelated traffic.
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: true})
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("setup: expected ErrBusCollision")
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	if len(observed) == 0 {
		t.Fatal("no BusEventEchoMismatch observed")
	}
	first := observed[0]
	if first.Byte != protocol.SymbolSyn {
		t.Fatalf("first event Byte = 0x%02X, want 0xAA", first.Byte)
	}
	if !first.EchoWasEscaped {
		t.Fatal("first event EchoWasEscaped = false, want true (escape-decoded payload 0xAA from third-party frame has WasEscaped=true)")
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

// TestBus_EchoMismatchEvent_RawSynIntrusion_CarriesProvenance pins
// the batch-23 round-5 (Codex P2 2026-05-17) fix on PR #155. Pre-fix,
// the ENH "unexpected SYN while waiting for non-SYN echo" guard at
// bus.go ~1011 returned after only emitOutcomeEvent, which surfaces
// a BusEventEchoMismatch with zero Byte. Downstream the gateway P10
// classifier saw Byte=0x00 and recorded "post_grant_ack" instead of
// "pre_echo_syn_raw" — the very subclass the operator needs to
// distinguish raw-SYN mux leaks from third-party-escaped 0xAA frames.
//
// Scenario: raw=0xFE (the DST byte of a broadcast frame), echo=0xAA
// (real wire SYN, WasEscaped=false). Post-fix the bus emits a direct
// BusEventEchoMismatch{Byte: 0xAA, EchoWasEscaped: false} BEFORE the
// emitOutcomeEvent re-emit; the gateway classifier sees the per-byte
// event first and tags it `pre_echo_syn_raw`.
func TestBus_EchoMismatchEvent_RawSynIntrusion_CarriesProvenance(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		if ev.Kind == protocol.BusEventEchoMismatch {
			observed = append(observed, ev)
		}
		return nil
	})

	cfg := protocol.DefaultBusConfig()
	cfg.Observer = observer
	bus := protocol.NewBus(tr, cfg, 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

	// Broadcast frame: first telegram byte after arbitration is DST=
	// 0xFE (AddressBroadcast). We poison its echo slot with a real
	// wire SYN (0xAA, WasEscaped=false) so the bus hits the
	// `flagged != nil && echo == SymbolSyn && !echoWasEscaped &&
	// raw != SymbolSyn` guard at bus.go ~1011.
	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0xB5,
		Secondary: 0x16,
		Data:      nil,
	}
	tr.mu.Lock()
	tr.echo = []echoF23Event{
		{value: protocol.SymbolSyn, wasEscaped: false},
	}
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("setup: expected ErrBusCollision (raw SYN intrusion)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("setup: want wrapped ErrBusCollision, got %v", err)
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	if len(observed) == 0 {
		t.Fatal("no BusEventEchoMismatch observed (provenance lost — Codex P2 batch-23 regression)")
	}
	// The first event is the per-byte direct emission from the
	// r5 guard; the centralized emitOutcomeEvent re-emit follows.
	first := observed[0]
	if first.Byte != protocol.SymbolSyn {
		t.Fatalf("first event Byte = 0x%02X, want 0xAA (raw SYN intrusion byte)", first.Byte)
	}
	if first.EchoWasEscaped {
		t.Fatalf("first event EchoWasEscaped = true, want false (real wire SYN has WasEscaped=false)")
	}
}
