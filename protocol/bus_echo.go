package protocol

import (
	"context"
	"fmt"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

func (b *Bus) sendSymbolWithEcho(runCtx, reqCtx context.Context, symbol byte, escape bool, isFirstByteAfterArbitration bool) error {
	if b.unescapedTransport || !escape || (symbol != SymbolEscape && symbol != SymbolSyn) {
		// F-23 (Codex bot r3 on #154): for ENH-class transports, a
		// `raw == SymbolSyn` write can be either a structural
		// end-of-message SYN (escape=false; adapter sends 0xAA raw
		// on wire) or a telegram payload byte equal to 0xAA
		// (escape=true; adapter wire-encodes to `0xA9 0x01`, echo
		// returns logical 0xAA with WasEscaped=true). The new
		// escape-decoded-SYN rejection in sendRawWithEcho must only
		// fire on the FIRST case — otherwise legitimate payload
		// 0xAA echoes are misclassified as bus collisions.
		expectRawSyn := !escape && symbol == SymbolSyn
		return b.sendRawWithEcho(runCtx, reqCtx, symbol, expectRawSyn, isFirstByteAfterArbitration)
	}

	// Plain transport: wire-level escape for 0xA9/0xAA symbols. The
	// two writes are escape-pair bytes (0xA9 followed by 0x00 or
	// 0x01), neither of which is a structural SYN, so
	// expectRawSyn=false for both. The new ENH guards only fire on
	// unescapedTransport=true anyway, so this is defensive parameter
	// hygiene rather than load-bearing.
	//
	// First-byte-after-arbitration semantics for the escape pair: the
	// FIRST wire byte is the escape lead (0xA9) — that is what the
	// adapter is going to transmit on the wire. So the lead carries
	// the isFirstByte flag; the trailing escape data byte does NOT.
	if err := b.sendRawWithEcho(runCtx, reqCtx, SymbolEscape, false, isFirstByteAfterArbitration); err != nil {
		return err
	}
	esc := byte(0x00)
	if symbol == SymbolSyn {
		esc = 0x01
	}
	return b.sendRawWithEcho(runCtx, reqCtx, esc, false, false)
}

// sendRawWithEcho writes a single byte and validates the wire echo.
// expectRawSyn (F-23, Codex bot r3 on #154) — when true, raw is a
// structural end-of-message SYN that the adapter sends as raw 0xAA
// on wire; the echo MUST come back with WasEscaped=false. When
// false, raw is a payload byte: the adapter may legitimately
// wire-escape values 0xA9/0xAA, and the echo's WasEscaped flag
// reflects that without indicating collision.
//
// isFirstByteAfterArbitration (batch-26 round-7) — when true, this
// write is the first wire byte sent AFTER arbitration completed. A
// mismatch where the echo arrives as a foreign-initiator-class byte
// (AddressClassMaster, value ≠ raw) is unambiguous evidence of
// arbitration loss to a concurrent initiator, NOT a generic echo
// mismatch. Classify as Collision rather than EchoMismatch so the
// retry path (Is(ErrBusCollision) AND NOT contains("echo mismatch"))
// routes through BusOutcomeCollision, and the gateway P10 counter
// surface records the event under the collision class rather than
// inflating the echo_mismatch population.
//
// The steps run in this order: write, echo read, the three SYN-intrusion
// guards, the payload-0xAA AUTO-SYN absorption and the value-mismatch
// classification. The bus is in the active echo-wait phase from the
// successful write until this call returns.
func (b *Bus) sendRawWithEcho(runCtx, reqCtx context.Context, raw byte, expectRawSyn bool, isFirstByteAfterArbitration bool) error {
	if err := b.writeEchoByte(raw, expectRawSyn); err != nil {
		return err
	}

	// Phase 1 Step B4 (frame-atomic-v8 §1.8): mark the bus as being in
	// the active echo-wait phase from here until this call returns.
	// inSendRawWithEchoActiveEchoWait() must hold for any round-9
	// AUTO-SYN absorb fire to count toward the proxy-mediated alert
	// counter — gating the increment on this predicate prevents future
	// callers that mimic the absorb shape from outside the
	// sendRawWithEcho call-stack from tripping the
	// HelianthusRound9FiredUnderProxy alert (v8 §1.12).
	b.activeEchoWaits.Add(1)
	defer b.activeEchoWaits.Add(-1)

	flagged, _ := b.transport.(transport.EscapeFlaggedReader)
	echo, echoWasEscaped, err := b.readEchoByte(runCtx, reqCtx, flagged)
	if err != nil {
		return err
	}
	if err := b.rejectEchoSynIntrusion(raw, echo, echoWasEscaped, flagged, expectRawSyn); err != nil {
		return err
	}
	// Payload 0xAA (expectRawSyn=false) answered by a real wire SYN
	// (WasEscaped=false): see absorbPayloadAaAutoSyn.
	if flagged != nil && !expectRawSyn && raw == SymbolSyn && echo == SymbolSyn && !echoWasEscaped {
		var done bool
		echo, echoWasEscaped, done, err = b.absorbPayloadAaAutoSyn(runCtx, reqCtx, flagged, echo, echoWasEscaped)
		if done {
			return err
		}
	}
	return b.classifyEchoValue(raw, echo, echoWasEscaped, isFirstByteAfterArbitration)
}

// writeEchoByte is the write step of sendRawWithEcho: the structural-SYN
// signal, the one-byte Write, the TX event and the write-error wrapping.
//
// F-NEW-28 (2026-05-21, round-9 layer-correct fix): signal the
// transport when this write is a structural SymbolSyn (terminator)
// vs a payload byte. The gateway adaptermux's echo tracker uses
// this provenance to distinguish a legitimate terminator-SYN
// expected echo from a payload-0xAA expected echo, closing the
// midWriteSyn predicate gap in the gateway mux that let wire
// AUTO-SYNs reach echo position during payload-0xAA writes.
//
// Single one-shot signal per Write — consumed by the next Write
// call and reset to default ("payload") afterwards. Transports
// that don't implement StructuralWriteSignaler are unaffected
// (interface-assertion + nil check); on those transports the
// round-9 absorb in absorbPayloadAaAutoSyn remains the canonical
// filter per v8 §1.8 retention contract.
func (b *Bus) writeEchoByte(raw byte, expectRawSyn bool) error {
	if expectRawSyn {
		if signaler, ok := b.transport.(transport.StructuralWriteSignaler); ok {
			signaler.SignalNextWriteIsStructuralSyn()
		}
	}
	written, err := b.transport.Write([]byte{raw})
	if err != nil {
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		if busOutcomeFromError(err) == BusOutcomeEchoMismatch {
			err = &echoMismatchEmittedError{err: err}
		}
		return err
	}
	if written != 1 {
		err := ebuserrors.ErrInvalidPayload
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	b.emitObserverEvent(BusEvent{
		Kind:    BusEventTX,
		Outcome: BusOutcomeSuccess,
		Byte:    raw,
	})
	return nil
}

// readEchoByte is the echo read step of sendRawWithEcho. A read error is
// emitted as an outcome event and returned.
//
// F-23 (batch-19, Codex bot follow-up review on PR #154): for
// ENH-class transports, read the echo via the EscapeFlaggedReader
// path so we can distinguish a real wire SYN from an escape-
// decoded payload 0xAA. Without this distinction, an unrelated
// in-flight telegram carrying a payload 0xAA (wire-encoded as
// `0xA9 0x01`) can satisfy an end-of-message SYN echo wait and
// the bus would proceed as if our SYN was acknowledged.
func (b *Bus) readEchoByte(runCtx, reqCtx context.Context, flagged transport.EscapeFlaggedReader) (byte, bool, error) {
	var echo byte
	var echoWasEscaped bool
	var err error
	if b.unescapedTransport && flagged != nil {
		echo, echoWasEscaped, err = b.readByteWithEscape(runCtx, reqCtx, flagged)
	} else {
		echo, err = b.readByte(runCtx, reqCtx)
		// Plain transport: wire-level bytes; echoWasEscaped stays
		// false (the plain-transport "unexpected SYN" check in
		// rejectEchoSynIntrusion keys on raw 0xAA only).
	}
	if err != nil {
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return 0, false, err
	}
	return echo, echoWasEscaped, nil
}

// rejectEchoSynIntrusion holds the three SYN-intrusion guards of
// sendRawWithEcho in their order. Each guard emits BusEventEchoMismatch
// with byte provenance BEFORE emitOutcomeEvent (batch-23 round-5, Codex P2
// 2026-05-17) so the downstream gateway P10 classifier sees the echoed
// byte and its escape flag; without the direct emit, the centralized
// re-emit in emitOutcomeEvent surfaces a zero-Byte event that is
// misclassified as `post_grant_ack` at the gateway side.
//
// Guard 1, PLAIN transports: a real wire SYN echoed back while we were
// waiting for a non-SYN echo is unambiguous collision evidence.
//
// Guard 2, F-23: the same collision-detection invariant for ENH-class
// transports, keyed on `wasEscaped=false` so a payload 0xAA
// (escape-decoded with wasEscaped=true) is correctly NOT treated as a real
// wire SYN. Gated on `flagged != nil` (Codex bot r4 on #154): for an
// unescaped transport that does NOT implement EscapeFlaggedReader (e.g.
// legacy test doubles), `echoWasEscaped` defaults to false in the fallback
// ReadByte path. Without provenance we cannot distinguish a real wire SYN
// from an escape-decoded payload — fall back to pre-F-23 behavior (skip
// this guard) rather than false-positive on every legitimate payload 0xAA
// echo. EchoWasEscaped=false here by construction (the guard requires
// it); the explicit forward keeps the emission pattern symmetric.
//
// Guard 3, F-23 (Codex bot r3 on #154): when we DID write a STRUCTURAL
// end-of-message SYN (raw=0xAA, escape=false), the echo must be a real
// wire SYN with WasEscaped=false. An escape-decoded payload 0xAA
// (WasEscaped=true) happens to share the value but is user data from an
// unrelated frame — it MUST NOT satisfy our echo wait. Pre-F-23 the wire
// bytes would have arrived as `0xA9, 0x01` (two bytes, first one fails the
// value check), so the bug was masked by the leak. The expectRawSyn gate
// is critical: a TELEGRAM PAYLOAD byte equal to 0xAA (escape=true at
// sendSymbolWithEcho) is wire-escaped by the adapter, and its legitimate
// echo arrives as logical 0xAA with WasEscaped=true. Rejecting that would
// misclassify every payload 0xAA write as a collision. The expectRawSyn
// flag, passed in by sendSymbolWithEcho, encodes "this 0xAA write IS the
// structural SYN, not a payload byte".
func (b *Bus) rejectEchoSynIntrusion(raw, echo byte, echoWasEscaped bool, flagged transport.EscapeFlaggedReader, expectRawSyn bool) error {
	if !b.unescapedTransport && echo == SymbolSyn && raw != SymbolSyn {
		b.emitObserverEvent(BusEvent{
			Kind:           BusEventEchoMismatch,
			Outcome:        BusOutcomeEchoMismatch,
			Byte:           echo,
			ByteKnown:      true,
			EchoWasEscaped: echoWasEscaped,
		})
		err := fmt.Errorf("unexpected syn while waiting for echo: %w", ebuserrors.ErrBusCollision)
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	if flagged != nil && echo == SymbolSyn && !echoWasEscaped && raw != SymbolSyn {
		b.emitObserverEvent(BusEvent{
			Kind:           BusEventEchoMismatch,
			Outcome:        BusOutcomeEchoMismatch,
			Byte:           echo,
			ByteKnown:      true,
			EchoWasEscaped: echoWasEscaped,
		})
		err := fmt.Errorf("unexpected syn while waiting for echo: %w", ebuserrors.ErrBusCollision)
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	if flagged != nil && expectRawSyn && raw == SymbolSyn && echo == SymbolSyn && echoWasEscaped {
		b.emitObserverEvent(BusEvent{
			Kind:           BusEventEchoMismatch,
			Outcome:        BusOutcomeEchoMismatch,
			Byte:           echo,
			ByteKnown:      true,
			EchoWasEscaped: echoWasEscaped, // 2026-05-17 batch-23: split P10 pre_echo_syn (escape-decoded data byte vs raw SYN)
		})
		err := fmt.Errorf("echo expected real wire SYN (0x%02X) but received escape-decoded payload 0xAA: %w", raw, ebuserrors.ErrBusCollision)
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	return nil
}

// absorbPayloadAaAutoSyn is the round-9 AUTO-SYN absorption of
// sendRawWithEcho. The caller runs it when a TELEGRAM PAYLOAD byte equal to
// 0xAA (expectRawSyn=false), which the adapter wire-escapes as `A9 01` and
// whose legitimate echo arrives WasEscaped=true, is answered first by a
// real wire SYN (WasEscaped=false). That SYN is NOT our payload echo — it
// is an unrelated bus event (idle SYN, or another initiator's structural
// marker).
//
// batch-26 round-9 (2026-05-18, architectural observation): the gateway
// holds the ENS transmit token while sendRawWithEcho is active. A wire SYN
// echo arriving with WasEscaped=false on a payload-0xAA write means an
// AUTO-SYN fired on the bus (the adapter buffers the full escape pair
// before writing it to wire due to TCP latency between
// gateway→adapter→wire — the AUTO-SYN we see is bus-idle interference
// BEFORE our `A9 01` pair reaches the wire). The REAL echo (0xAA
// wasEscaped=true) follows immediately. While holding the transmit token,
// AUTO-SYNs must be dropped, not propagated as failures: absorb the
// wire-SYN and re-read the next byte. The drain is bounded at
// maxPayloadAaAutoSynDrains (3 — typical AUTO-SYN bursts are 1-2 bytes;
// the cap prevents unbounded blocking on a hung adapter).
//
// Round-8 forensic data (100% of residual pre_echo_syn_raw events fire
// here, all mid-frame, never at the plain/F-23 SYN-intrusion guards)
// drove this scoping: the absorb is applied only at this site. The
// SYN-intrusion guards are not modified — round-8 measurements showed they
// have zero production fires, and absorbing wire SYNs there would mask
// legitimate arbitration collisions where we wrote a non-SYN and the bus
// is idle (a real defect, not buffering interference).
//
// v8 LEGACY-FALLBACK BLOCK (Step D label, retained per frame-atomic-v8
// I8). SCOPE: this is the canonical wire-AUTO-SYN filter for
// DIRECT-ADAPTER MODE (gateway → adapter, no proxy mediating the byte
// stream). In PROXY-MEDIATED v8-ENFORCE MODE the adaptermux classifier in
// the gateway filters AUTO-SYNs upstream and this code path stays cold
// (counter rate == 0). Both deployment modes share the same source file;
// the reconciliation lives in the Prometheus alert
// HelianthusRound9FiredUnderProxy, which AND-s the round9AbsorbEntered
// counter with the proxy's classifier_mode label. RETENTION CONTRACT: this
// code is NOT deprecated and NOT scheduled for removal. v8 §1.8 / I8
// explicitly preserves the round-9 absorb mechanism for the direct-adapter
// deployment topology.
//
// done=true means err is the final result of sendRawWithEcho (nil on
// recovery). done=false hands the drained non-SYN byte and its escape flag
// to the value-mismatch classification.
func (b *Bus) absorbPayloadAaAutoSyn(runCtx, reqCtx context.Context, flagged transport.EscapeFlaggedReader, echo byte, echoWasEscaped bool) (byte, bool, bool, error) {
	// Phase 1 Step B4 (frame-atomic-v8 §1.8): the neutrally-named
	// round9AbsorbEntered counter increments here, gated on the runtime
	// phase predicate inSendRawWithEchoActiveEchoWait(). From
	// sendRawWithEcho the predicate is always true by construction; the
	// gate is kept as a phase assertion, not as proof of any cross-repo
	// state (this code has zero proxy-awareness).
	if b.inSendRawWithEchoActiveEchoWait() {
		b.round9AbsorbEntered.Add(1)
	}
	const maxPayloadAaAutoSynDrains = 3
	absorbed := 0
	drainExhausted := true // assume cap-exhausted; flipped to false on any non-SYN break
	for absorbed < maxPayloadAaAutoSynDrains {
		nextEcho, nextWasEscaped, drainErr := b.readByteWithEscape(runCtx, reqCtx, flagged)
		if drainErr != nil {
			// Read error during drain (timeout, transport close, ctx
			// cancel): propagate AS-IS without classifying as echo
			// mismatch — it is a transport-layer fault.
			b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, drainErr)
			return echo, echoWasEscaped, true, drainErr
		}
		absorbed++
		b.payloadAaAutoSynAbsorbed.Add(1)
		if nextEcho == SymbolSyn && nextWasEscaped {
			// The real escape-decoded payload echo arrived: the preceding
			// wire SYN(s) were AUTO-SYN buffering interference. No error
			// and no retry.
			b.payloadAaAutoSynRecovered.Add(1)
			return echo, echoWasEscaped, true, nil
		}
		if nextEcho == SymbolSyn && !nextWasEscaped {
			// Another AUTO-SYN: continue draining within the cap.
			continue
		}
		// Non-SYN drained byte: a real echo mismatch (e.g. adapter
		// corruption or a missed arbitration race). The downstream emit
		// reflects what we actually observed; the exhaustion counter
		// MUST NOT increment for this path.
		echo = nextEcho
		echoWasEscaped = nextWasEscaped
		drainExhausted = false
		break
	}
	if drainExhausted {
		// The cap was reached without the real echo: surface the
		// exhaustion counter, then the payload-0xAA "received real wire
		// SYN" error, which routes through BusOutcomeCollision (the
		// pre-round-9 adapter-genuinely-stuck classification).
		b.payloadAaAutoSynDrainExhausted.Add(1)
		b.emitObserverEvent(BusEvent{
			Kind:           BusEventEchoMismatch,
			Outcome:        BusOutcomeEchoMismatch,
			Byte:           echo,
			ByteKnown:      true,
			EchoWasEscaped: echoWasEscaped,
		})
		err := fmt.Errorf("echo for payload 0xAA expected escape-decoded WasEscaped=true but received real wire SYN: %w", ebuserrors.ErrBusCollision)
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return echo, echoWasEscaped, true, err
	}
	// Non-SYN drained byte: the generic value-mismatch classification
	// fires with the drained byte; its "echo mismatch" text routes to
	// BusOutcomeEchoMismatch (Codex r6 P1, batch-26 round-9: an earlier
	// version returned the "received real wire SYN" error here and
	// misclassified it as Collision).
	return echo, echoWasEscaped, false, nil
}

// classifyEchoValue is the value-mismatch classification of
// sendRawWithEcho: nil when the echo equals raw, otherwise either a
// first-byte arbitration loss or a generic echo mismatch.
//
// batch-26 round-7 — first-byte-after-arbitration foreign-initiator split.
// When the echo of the FIRST byte after arbitration comes back as an
// initiator-class byte that is NOT our raw write AND was NOT
// escape-decoded, that is the classic arbitration-loss shape: a concurrent
// initiator won the bus and its SOF is on the wire instead of our echoed
// byte. Classify as Collision rather than EchoMismatch so
// busOutcomeFromError routes to BusOutcomeCollision (the routing keys off
// Is(ErrBusCollision) AND the "echo mismatch" substring; the arbitration
// loss message omits that substring deliberately so the existing
// classifier discriminates without changing).
//
// Guard layering:
//   - isFirstByteAfterArbitration: only the first wire byte after
//     arbitration can be an arbitration-loss observation. Mid-frame
//     mismatches are echo-stream corruption, not arbitration races.
//   - !echoWasEscaped: an escape-decoded payload byte arriving as the
//     "echo" is not a wire-level foreign-initiator signal — it's
//     escape-stream contamination, which the F-23 / batch-23 round-5
//     guards already classify as EchoMismatch with the right subclass.
//   - AddressClassOf(echo) == AddressClassMaster: only foreign
//     initiator-class bytes are arbitration-loss evidence. A target,
//     broadcast or reserved byte in echo position would be wire
//     corruption or bit-flip — keep generic EchoMismatch for those.
//
// batch-26 round-7 (Codex r2 defect 2 — MEDIUM): no direct BusEvent
// emission for the arbitration-loss branch. The error wraps
// ErrBusCollision WITHOUT the "echo mismatch" substring, so the bus retry
// path emits BusEventRetry with Outcome=BusOutcomeCollision and the
// gateway counts it under the collision class on that retry event; a
// separate BusEventCollision here would be a dead-letter event with no
// downstream consumer. The error wrapping is the load-bearing signal.
func (b *Bus) classifyEchoValue(raw, echo byte, echoWasEscaped bool, isFirstByteAfterArbitration bool) error {
	if echo != raw {
		if isFirstByteAfterArbitration && !echoWasEscaped && AddressClassOf(echo) == AddressClassMaster {
			err := fmt.Errorf("first-byte arbitration loss: wrote 0x%02X, wire echo 0x%02X (foreign initiator): %w", raw, echo, ebuserrors.ErrBusCollision)
			b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
			return err
		}
		b.emitObserverEvent(BusEvent{
			Kind:           BusEventEchoMismatch,
			Outcome:        BusOutcomeEchoMismatch,
			Byte:           echo,
			ByteKnown:      true,
			EchoWasEscaped: echoWasEscaped, // 2026-05-17 batch-23: split P10 pre_echo_syn (escape-decoded data byte vs raw SYN)
		})
		err := &echoMismatchEmittedError{err: fmt.Errorf("echo mismatch (sent 0x%02x, got 0x%02x): %w", raw, echo, ebuserrors.ErrBusCollision)}
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	return nil
}
