package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// batch-26 round-9 (2026-05-18) — bounded AUTO-SYN absorption at the
// payload-0xAA echo site in sendRawWithEcho. The user's architectural
// insight: while the gateway holds the ENS transmit token, AUTO-SYNs
// on the wire are bus-idle interference from the adapter's TCP-buffered
// write timing, NOT real echoes of our payload-0xAA write. The escape
// pair `A9 01` lands on the wire AFTER the wire AUTO-SYN, so the first
// byte the gateway reads is the AUTO-SYN; the real escape-decoded echo
// (0xAA wasEscaped=true) follows immediately.
//
// Round-8 forensic measurement on live HA bus: 100% of residual
// `pre_echo_syn_raw` events fire at the line-1116 guard in bus.go,
// 100% are mid-frame, never at the first-byte-after-arbitration site.
// The fix absorbs up to maxPayloadAaAutoSynDrains (3) intervening
// wire SYNs and retries the echo read; success eliminates the
// echo_mismatch + ErrBusCollision + retry round-trip.

// TestPayloadAaAutoSynAbsorb_SingleAutoSynThenRealEcho pins the most
// common production shape: ONE wire AUTO-SYN absorbed, the very next
// byte is the real escape-decoded payload echo, transaction succeeds
// with no error and no echo_mismatch event emitted.
func TestPayloadAaAutoSynAbsorb_SingleAutoSynThenRealEcho(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		observed = append(observed, ev)
		return nil
	})

	cfg := protocol.DefaultBusConfig()
	cfg.Observer = observer
	bus := protocol.NewBus(tr, cfg, 8)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)
	ctx := context.Background()

	// Broadcast frame with a PAYLOAD byte equal to SymbolSyn (0xAA).
	// Wire transmission: DST PB SB LEN DATA[0]=0xAA CRC, then trailing
	// end-of-message SYN. The DATA[0] echo position will be poisoned by
	// a wire AUTO-SYN (wasEscaped=false), then the real escape-decoded
	// payload echo (wasEscaped=true) follows.
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
	// Bytes 0..3 (DST PB SB LEN) — clean echoes.
	for _, b := range telegram[:4] {
		tr.echo = append(tr.echo, echoF23Event{value: b, wasEscaped: false})
	}
	// Byte 4 (DATA[0]=0xAA) echo SLOT — first byte read is the wire
	// AUTO-SYN (wasEscaped=false), second byte is the real escape-
	// decoded payload echo (wasEscaped=true). Round-9 absorbs the first
	// and accepts the second as the legitimate echo.
	tr.echo = append(tr.echo,
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: true},
	)
	// Byte 5 (CRC) — clean echo.
	tr.echo = append(tr.echo, echoF23Event{value: telegram[5], wasEscaped: false})
	// End-of-message structural SYN — real wire SYN.
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v; want nil (single AUTO-SYN must be absorbed, real echo accepted)", err)
	}

	if got := bus.PayloadAaAutoSynAbsorbed(); got != 1 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 1", got)
	}
	if got := bus.PayloadAaAutoSynRecovered(); got != 1 {
		t.Errorf("PayloadAaAutoSynRecovered() = %d; want 1", got)
	}
	if got := bus.PayloadAaAutoSynDrainExhausted(); got != 0 {
		t.Errorf("PayloadAaAutoSynDrainExhausted() = %d; want 0", got)
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventEchoMismatch {
			t.Errorf("unexpected BusEventEchoMismatch — absorb path must NOT emit echo_mismatch on recovery: %+v", ev)
		}
	}
}

// TestPayloadAaAutoSynAbsorb_TwoAutoSynsThenRealEcho exercises the
// drain loop with TWO intervening wire AUTO-SYNs before the real echo.
// Verifies absorbed counter increments per drain (not per recovery) and
// recovery still succeeds within the cap.
func TestPayloadAaAutoSynAbsorb_TwoAutoSynsThenRealEcho(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		observed = append(observed, ev)
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
	// Byte 4 echo slot: two wire AUTO-SYNs (wasEscaped=false), then real
	// escape-decoded echo (wasEscaped=true).
	tr.echo = append(tr.echo,
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: true},
	)
	tr.echo = append(tr.echo, echoF23Event{value: telegram[5], wasEscaped: false})
	tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v; want nil (two AUTO-SYNs within cap MUST be absorbed)", err)
	}

	if got := bus.PayloadAaAutoSynAbsorbed(); got != 2 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 2", got)
	}
	if got := bus.PayloadAaAutoSynRecovered(); got != 1 {
		t.Errorf("PayloadAaAutoSynRecovered() = %d; want 1", got)
	}
	if got := bus.PayloadAaAutoSynDrainExhausted(); got != 0 {
		t.Errorf("PayloadAaAutoSynDrainExhausted() = %d; want 0", got)
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventEchoMismatch {
			t.Errorf("unexpected BusEventEchoMismatch — absorb path must NOT emit on recovery: %+v", ev)
		}
	}
}

// TestPayloadAaAutoSynAbsorb_DrainCapExhausted_StillEchoMismatch pins
// the safety boundary: when the drain cap (3) is reached without
// finding the real echo, the original payload-0xAA echo_mismatch path
// fires (ErrBusCollision returned, BusEventEchoMismatch emitted,
// PayloadAaAutoSynDrainExhausted increments).
func TestPayloadAaAutoSynAbsorb_DrainCapExhausted_StillEchoMismatch(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		observed = append(observed, ev)
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
	// Byte 4 echo slot poisoned with 4 wire AUTO-SYNs in a row (cap+1):
	// the first absorbs into the initial echo, three more are drained,
	// the third drain reaches the cap → fall through to echo_mismatch
	// with the third drained byte's provenance (wire SYN, !wasEscaped).
	for i := 0; i < 4; i++ {
		tr.echo = append(tr.echo, echoF23Event{value: protocol.SymbolSyn, wasEscaped: false})
	}
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("Send err = nil; want ErrBusCollision (cap exhausted)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}

	if got := bus.PayloadAaAutoSynAbsorbed(); got != 3 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 3 (cap)", got)
	}
	if got := bus.PayloadAaAutoSynRecovered(); got != 0 {
		t.Errorf("PayloadAaAutoSynRecovered() = %d; want 0 (no recovery)", got)
	}
	if got := bus.PayloadAaAutoSynDrainExhausted(); got != 1 {
		t.Errorf("PayloadAaAutoSynDrainExhausted() = %d; want 1", got)
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	var sawEchoMismatch bool
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventEchoMismatch {
			sawEchoMismatch = true
			// The emitted event provenance must be the drained byte
			// (wire SYN, wasEscaped=false) NOT the original echo.
			if ev.Byte != protocol.SymbolSyn {
				t.Errorf("emit Byte = 0x%02X; want 0xAA (drained wire SYN)", ev.Byte)
			}
			if ev.EchoWasEscaped {
				t.Errorf("emit EchoWasEscaped = true; want false (drained wire SYN has wasEscaped=false)")
			}
			break
		}
	}
	if !sawEchoMismatch {
		t.Error("no BusEventEchoMismatch emitted on drain exhaustion; want emit (caller observability)")
	}
}

// TestPayloadAaAutoSynAbsorb_DrainHitsNonSynMismatch_RoutesToValueMismatch
// pins the inner-loop break path: when a drained byte is NOT a wire SYN,
// it is treated as the (mismatched) real echo and falls through to the
// emit + ErrBusCollision return, with the BusEventEchoMismatch carrying
// the DRAINED byte's value/provenance (not the original 0xAA).
func TestPayloadAaAutoSynAbsorb_DrainHitsNonSynMismatch_RoutesToValueMismatch(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	var observed []protocol.BusEvent
	var obsMu sync.Mutex
	observer := protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
		obsMu.Lock()
		defer obsMu.Unlock()
		observed = append(observed, ev)
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
	// Byte 4 echo: AUTO-SYN absorbed, then drained byte is 0x55 (a
	// non-SYN, non-master-class byte). break path runs; emit uses the
	// drained byte's provenance.
	tr.echo = append(tr.echo,
		echoF23Event{value: protocol.SymbolSyn, wasEscaped: false},
		echoF23Event{value: 0x55, wasEscaped: false},
	)
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("Send err = nil; want ErrBusCollision (non-SYN drained byte is a real mismatch)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}

	if got := bus.PayloadAaAutoSynAbsorbed(); got != 1 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 1", got)
	}
	if got := bus.PayloadAaAutoSynRecovered(); got != 0 {
		t.Errorf("PayloadAaAutoSynRecovered() = %d; want 0", got)
	}
	if got := bus.PayloadAaAutoSynDrainExhausted(); got != 0 {
		t.Errorf("PayloadAaAutoSynDrainExhausted() = %d; want 0 (break path, not cap)", got)
	}

	obsMu.Lock()
	defer obsMu.Unlock()
	var sawEchoMismatch bool
	for _, ev := range observed {
		if ev.Kind == protocol.BusEventEchoMismatch {
			sawEchoMismatch = true
			// The emit MUST reflect the DRAINED byte (0x55), not the
			// original 0xAA. This is load-bearing for downstream P10
			// classification: the classifier sees the byte that
			// actually mismatched, not the AUTO-SYN that we absorbed.
			if ev.Byte != 0x55 {
				t.Errorf("emit Byte = 0x%02X; want 0x55 (drained byte, NOT the absorbed AUTO-SYN)", ev.Byte)
			}
			if ev.EchoWasEscaped {
				t.Errorf("emit EchoWasEscaped = true; want false (drained 0x55 has wasEscaped=false)")
			}
			break
		}
	}
	if !sawEchoMismatch {
		t.Error("no BusEventEchoMismatch emitted on non-SYN drain break; want emit")
	}
}

// TestPayloadAaAutoSynAbsorb_NoEffect_OnNonPayloadAaPath is the negative
// control: the round-9 absorb is gated on `!expectRawSyn && raw ==
// SymbolSyn`. On a NON-payload-0xAA path (the line-1066 plain/F-23 SYN-
// intrusion guard, where raw != SymbolSyn and echo == wire-SYN), the
// drain MUST NOT run — payloadAaAutoSyn* counters stay zero.
//
// Setup: broadcast frame with NO payload 0xAA (Data=nil). First echo
// (DST=0xFE) poisoned by real wire SYN. This hits the line-1066 guard,
// not the line-1163 guard. Counters must remain zero.
func TestPayloadAaAutoSynAbsorb_NoEffect_OnNonPayloadAaPath(t *testing.T) {
	t.Parallel()

	tr := &echoF23TestTransport{unescaped: true}

	cfg := protocol.DefaultBusConfig()
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
		Data:      nil, // no payload 0xAA — round-9 path inactive
	}

	tr.mu.Lock()
	// First echo (DST=AddressBroadcast=0xFE expected): we get a real wire
	// SYN instead → line-1066 guard fires, raw != SymbolSyn so round-9
	// absorb is NOT entered.
	tr.echo = []echoF23Event{
		{value: protocol.SymbolSyn, wasEscaped: false},
	}
	tr.mu.Unlock()

	_, err := bus.Send(ctx, frame)
	if err == nil {
		t.Fatal("Send err = nil; want ErrBusCollision (line-1066 SYN intrusion path)")
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
	}

	if got := bus.PayloadAaAutoSynAbsorbed(); got != 0 {
		t.Errorf("PayloadAaAutoSynAbsorbed() = %d; want 0 (non-payload-0xAA path MUST NOT trigger absorb)", got)
	}
	if got := bus.PayloadAaAutoSynRecovered(); got != 0 {
		t.Errorf("PayloadAaAutoSynRecovered() = %d; want 0", got)
	}
	if got := bus.PayloadAaAutoSynDrainExhausted(); got != 0 {
		t.Errorf("PayloadAaAutoSynDrainExhausted() = %d; want 0", got)
	}
}
