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

// writeFailTransport returns an error from Write after a configured
// number of successful writes. Used to assert that a Write-phase
// error in sendRawWithEcho does NOT increment Round9AbsorbEntered
// and leaves no lingering active-echo-wait state on the Bus.
type writeFailTransport struct {
	mu          sync.Mutex
	writes      int
	failOnWrite int // 1 = fail on the FIRST write (no successful writes), 2 = fail on the 2nd, ...
}

var (
	_ transport.RawTransport        = (*writeFailTransport)(nil)
	_ transport.EscapeAware         = (*writeFailTransport)(nil)
	_ transport.EscapeFlaggedReader = (*writeFailTransport)(nil)
)

var errSyntheticWriteFailure = errors.New("synthetic write failure")

func (t *writeFailTransport) ReadByte() (byte, error) {
	// Never expected to be invoked — Write errors before any echo wait.
	return 0, ebuserrors.ErrTimeout
}

func (t *writeFailTransport) ReadByteWithEscape() (byte, bool, error) {
	return 0, false, ebuserrors.ErrTimeout
}

func (t *writeFailTransport) Write(payload []byte) (int, error) {
	t.mu.Lock()
	t.writes++
	w := t.writes
	t.mu.Unlock()
	if t.failOnWrite > 0 && w >= t.failOnWrite {
		return 0, errSyntheticWriteFailure
	}
	return len(payload), nil
}

func (t *writeFailTransport) Close() error                  { return nil }
func (t *writeFailTransport) BytesAreUnescaped() bool       { return true }
func (t *writeFailTransport) StartArbitration(byte) error   { return nil }
func (t *writeFailTransport) ArbitrationSendsSource() bool  { return true }

// Phase 1 Step B4 (frame-atomic-v8 §1.8, §1.12, retained per v8 I8):
// the round-9 AUTO-SYN absorb code at the payload-0xAA echo site in
// sendRawWithEcho stays as a legacy fallback for direct-adapter mode.
// In adaptermux's `enforce` mode, the proxy MUST filter wire AUTO-SYNs
// before they reach the gateway's echo position; if round-9 still fires
// under that mode, the Prometheus alert HelianthusRound9FiredUnderProxy
// trips. The Round9AbsorbEntered counter exposes the fires
// from the gateway's perspective so the alert can be evaluated against
// it AND classifier-mode label (the gating is in the alert rule, not in
// the Go code — the counter just increments any time the round-9
// absorb predicate fires within sendRawWithEcho's active echo-wait).
//
// These tests assert:
//   - the new counter increments by exactly 1 per round-9 entry;
//   - it stays at zero when round-9 never fires (clean echo path);
//   - it tracks the existing PayloadAaAutoSynAbsorbed counter shape
//     (per-fire, not per-byte-drained);
//   - the inSendRawWithEchoActiveEchoWait() predicate is true at the
//     absorb site by construction.

// TestRound9AbsorbEntered_SingleEntry asserts that one
// round-9 absorb entry (regardless of how many bytes are drained
// inside the loop) increments the round9AbsorbEntered counter exactly once.
func TestRound9AbsorbEntered_SingleEntry(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

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
	for _, b := range telegram[:4] {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	// Byte 4 echo slot: one wire AUTO-SYN, then real escape-decoded
	// payload echo. Round-9 absorb predicate fires once.
	tr.echo = append(tr.echo,
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: true},
	)
	tr.echo = append(tr.echo, echoF23Event{value: telegram[5], wasEscaped: false})
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	if _, err := bus.Send(ctx, frame); err != nil {
		t.Fatalf("Send error = %v; want nil", err)
	}

	if got := bus.Round9AbsorbEntered(); got != 1 {
		t.Errorf("Round9AbsorbEntered() = %d; want 1 (single round-9 entry)", got)
	}
	if got := bus.PayloadAaAutoSynAbsorbed(); got != 1 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 1", got)
	}
}

// TestRound9AbsorbEntered_PerEntryNotPerByte asserts that
// when the absorb loop drains multiple bytes within a single entry,
// the round9AbsorbEntered counter still increments by ONE (per-fire
// semantics, not per-byte). This distinguishes it from
// PayloadAaAutoSynAbsorbed which counts per drained byte.
func TestRound9AbsorbEntered_PerEntryNotPerByte(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

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
	for _, b := range telegram[:4] {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	// Byte 4 echo slot: two wire AUTO-SYNs (one entry + one drain
	// iteration), then real escape-decoded echo. The absorb predicate
	// is checked once on entry — the loop drains the second AUTO-SYN
	// internally without re-firing the predicate.
	tr.echo = append(tr.echo,
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: true},
	)
	tr.echo = append(tr.echo, echoF23Event{value: telegram[5], wasEscaped: false})
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	if _, err := bus.Send(ctx, frame); err != nil {
		t.Fatalf("Send error = %v; want nil", err)
	}

	if got := bus.Round9AbsorbEntered(); got != 1 {
		t.Errorf("Round9AbsorbEntered() = %d; want 1 (per-entry, not per-byte)", got)
	}
	// Per-byte counter should report 2 — the predicate fires once on
	// entry, but two AUTO-SYN bytes are drained.
	if got := bus.PayloadAaAutoSynAbsorbed(); got != 2 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 2 (per-byte)", got)
	}
}

// TestRound9AbsorbEntered_StaysZeroOnCleanEcho asserts the
// expected steady-state under the v8 enforce contract: when the proxy
// correctly filters wire AUTO-SYNs (no round-9 entry is needed), the
// counter stays at zero and the HelianthusRound9FiredUnderProxy alert
// remains silent.
func TestRound9AbsorbEntered_StaysZeroOnCleanEcho(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

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
	for _, b := range telegram[:4] {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	// Byte 4 echo slot: real escape-decoded echo arrives immediately,
	// no wire AUTO-SYN interference. Round-9 absorb predicate stays
	// false — counter must remain at zero.
	tr.echo = append(tr.echo,
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: true},
	)
	tr.echo = append(tr.echo, echoF23Event{value: telegram[5], wasEscaped: false})
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	if _, err := bus.Send(ctx, frame); err != nil {
		t.Fatalf("Send error = %v; want nil", err)
	}

	if got := bus.Round9AbsorbEntered(); got != 0 {
		t.Errorf("Round9AbsorbEntered() = %d; want 0 (no round-9 entry under clean echo)", got)
	}
	if got := bus.PayloadAaAutoSynAbsorbed(); got != 0 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 0", got)
	}
}

// TestRound9AbsorbEntered_CapExhaustedStillCounts asserts
// that even when the absorb loop reaches its cap without recovery
// (drainExhausted path), the counter still increments — the predicate
// fires regardless of the absorb outcome. This is critical for the
// alert: a failing round-9 entry is still a v8 invariant violation
// when the alert that gates on classifier_mode == enforce fires.
func TestRound9AbsorbEntered_CapExhaustedStillCounts(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

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
	for _, b := range telegram[:4] {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	// Byte 4 echo slot: cap+1 wire AUTO-SYNs in a row. Round-9
	// predicate fires on entry, drain loop exhausts the cap (3) without
	// finding the real echo, falls through to echo_mismatch /
	// ErrBusCollision.
	for i := 0; i < 4; i++ {
		tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	}
	tr.mu.Unlock()

	if _, err := bus.Send(ctx, frame); err == nil {
		t.Fatalf("Send error = nil; want non-nil (cap exhausted should error)")
	}

	// Counter must increment EVEN when the absorb attempt fails — the
	// fire happened, the alert should observe it.
	if got := bus.Round9AbsorbEntered(); got != 1 {
		t.Errorf("Round9AbsorbEntered() = %d; want 1 (predicate fired even on exhaustion)", got)
	}
	if got := bus.PayloadAaAutoSynDrainExhausted(); got != 1 {
		t.Errorf("PayloadAaAutoSynDrainExhausted() = %d; want 1", got)
	}
}

// TestRound9AbsorbEntered_AccumulatesAcrossSends asserts
// monotonic accumulation across multiple Send() calls — each round-9
// entry adds 1, and the counter never decrements.
func TestRound9AbsorbEntered_AccumulatesAcrossSends(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

	makeFrame := func() protocol.Frame {
		return protocol.Frame{
			Source:    0x10,
			Target:    protocol.AddressBroadcast,
			Primary:   0xB5,
			Secondary: 0x16,
			Data:      []byte{protocol.SymbolSyn},
		}
	}
	makeTelegram := func() []byte {
		t := []byte{
			protocol.AddressBroadcast, 0xB5, 0x16, 0x01, protocol.SymbolSyn,
		}
		return append(t, protocol.CRC(append([]byte{0x10}, t...)))
	}

	for i := 0; i < 3; i++ {
		telegram := makeTelegram()
		tr.mu.Lock()
		tr.echo = nil
		for _, b := range telegram[:4] {
			tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
		}
		tr.echo = append(tr.echo,
			echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
			echoF23Event{value: protocol.SymbolSyn, wasEscaped: true},
		)
		tr.echo = append(tr.echo, echoF23Event{value: telegram[5], wasEscaped: false})
		tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
		tr.mu.Unlock()

		if _, err := bus.Send(ctx, makeFrame()); err != nil {
			t.Fatalf("Send #%d error = %v; want nil", i, err)
		}
	}

	if got := bus.Round9AbsorbEntered(); got != 3 {
		t.Errorf("Round9AbsorbEntered() = %d; want 3 (one fire per Send)", got)
	}
}

// TestRound9AbsorbEntered_ConcurrentSendsRaceFree exercises
// the atomic counter under concurrent senders. We don't claim
// strict ordering — only that the final count equals the number of
// expected round-9 entries with no torn reads/writes.
func TestRound9AbsorbEntered_ConcurrentSendsRaceFree(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	// Pre-seed enough echo slots for 5 sequential Send() calls. The
	// transport mutex serializes Write→Read pairs at the test harness
	// level, so even with concurrent goroutines the sequencing is
	// well-defined; we only verify the counter accumulates correctly.
	telegramBase := []byte{
		protocol.AddressBroadcast, 0xB5, 0x16, 0x01, protocol.SymbolSyn,
	}
	telegramBase = append(telegramBase, protocol.CRC(append([]byte{0x10}, telegramBase...)))

	const n = 5
	tr.mu.Lock()
	tr.echo = nil
	for i := 0; i < n; i++ {
		for _, b := range telegramBase[:4] {
			tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
		}
		tr.echo = append(tr.echo,
			echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
			echoF23Event{value: protocol.SymbolSyn, wasEscaped: true},
		)
		tr.echo = append(tr.echo, echoF23Event{value: telegramBase[5], wasEscaped: false})
		tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	}
	tr.mu.Unlock()

	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 16)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			frame := protocol.Frame{
				Source:    0x10,
				Target:    protocol.AddressBroadcast,
				Primary:   0xB5,
				Secondary: 0x16,
				Data:      []byte{protocol.SymbolSyn},
			}
			_, _ = bus.Send(ctx, frame)
		}()
	}
	wg.Wait()

	if got := bus.Round9AbsorbEntered(); got != n {
		t.Errorf("Round9AbsorbEntered() = %d; want %d (concurrent monotonic)", got, n)
	}
}

// TestRound9AbsorbEntered_WriteErrorLeavesCounterAtZero asserts the
// write-phase error boundary: when transport.Write fails before
// sendRawWithEcho enters the active-echo-wait phase, the
// Round9AbsorbEntered and PayloadAaAutoSyn* counters MUST NOT
// increment. This is the boundary the test actually proves — with
// failOnWrite=1 the function returns before activeEchoWaits.Add(1),
// so by construction the predicate is never bumped. A "predicate
// leak" test would need a separate scenario where Write succeeds,
// the increment-and-defer registers, and the function returns
// abnormally (panic, runtime.Goexit, etc.); this test is not that
// scenario. Two sequential failed sends are used purely to confirm
// the counters stay at zero across multiple attempts (no monotonic
// surprise).
func TestRound9AbsorbEntered_WriteErrorLeavesCounterAtZero(t *testing.T) {
	t.Parallel()

	// Fail on the FIRST Write — sendRawWithEcho returns immediately
	// without ever incrementing activeEchoWaits or reaching the
	// round-9 absorb block.
	tr := &writeFailTransport{failOnWrite: 1}

	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)

	// Bounded context so the Bus does not retry on transport-error
	// classifications that route through the retry policy.
	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()

	for i := 0; i < 2; i++ {
		frame := protocol.Frame{
			Source:    0x10,
			Target:    protocol.AddressBroadcast,
			Primary:   0xB5,
			Secondary: 0x16,
			Data:      []byte{0x01},
		}
		_, err := bus.Send(sendCtx, frame)
		if err == nil {
			t.Fatalf("Send #%d returned nil; want error (transport.Write must fail)", i)
		}
	}

	if got := bus.Round9AbsorbEntered(); got != 0 {
		t.Errorf("Round9AbsorbEntered() = %d; want 0 (Write error must not enter absorb block)", got)
	}
	// PayloadAaAutoSynAbsorbed/Recovered/DrainExhausted must also be
	// 0 — Write error is a transport-layer fault, not an absorb event.
	if got := bus.PayloadAaAutoSynAbsorbed(); got != 0 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 0", got)
	}
	if got := bus.PayloadAaAutoSynRecovered(); got != 0 {
		t.Errorf("PayloadAaAutoSynRecovered() = %d; want 0", got)
	}
	if got := bus.PayloadAaAutoSynDrainExhausted(); got != 0 {
		t.Errorf("PayloadAaAutoSynDrainExhausted() = %d; want 0", got)
	}
}
