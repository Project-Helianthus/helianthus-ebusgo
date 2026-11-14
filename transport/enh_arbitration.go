//go:build !tinygo

package transport

import (
	"fmt"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// StartArbitration requests bus ownership for the given initiator address.
// It sends ENHReqStart(initiator) and blocks until ENHResStarted(initiator) or ENHResFailed(winner).
//
// Any received ENHResReceived bytes observed while waiting are queued so that subsequent ReadByte
// calls can consume them.
func (t *ENHTransport) StartArbitration(initiator byte) error {
	if t.closed.Load() {
		return fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()

	if err := t.writeStartRequest(initiator); err != nil {
		return err
	}

	mismatchCount := 0

	for {
		if err := t.setReadDeadline(); err != nil {
			return t.mapReadError(err)
		}

		n, err := t.conn.Read(t.buffer)
		if err != nil {
			if isTimeout(err) {
				t.resetENHParserLocked() // Clear any pending byte1 from partial frame
				t.deferredErr = nil
				t.awaitingStart.Store(false)
				// F-23 (Codex bot r5 on #154): blocking arbitration
				// timeout. The discard path during arbitration feeds
				// every received byte through escDecoder so a stale
				// 0xA9 lead cannot strand across a non-dropped byte
				// boundary — but if arbitration TIMES OUT before the
				// second byte of the pair arrives, the lead persists
				// and the next read (after the caller retries or moves
				// on) would pair its first byte with that stale lead.
				// Arbitration abort is a Layer-1 boundary that
				// invalidates discarded-traffic state; the bytes we
				// fed during the aborted window are gone, no
				// completion is expected. Reset.
				t.escDecoder.Reset()
			}
			return t.mapReadError(err)
		}
		if n == 0 {
			continue
		}

		msgs, parseErr := t.parseObservedLocked(t.buffer[:n])

		// Process valid messages before handling parse error — a buffer
		// with a valid STARTED followed by a corrupt trailing byte should
		// succeed, not abort.
		var outcome arbitrationOutcome
		outcome, mismatchCount = t.observeArbitrationBatchLocked(msgs, initiator, mismatchCount)

		if outcome.done {
			return t.completeArbitrationLocked(outcome.err)
		}

		// Handle parse error only after processing valid messages — a
		// corrupt trailing byte should not mask a valid STARTED/FAILED.
		if parseErr != nil {
			t.resetENHParserLocked()
			t.deferredErr = nil
			t.awaitingStart.Store(false)
			// F-23 (batch-19, Codex bot P-r3): parse-error resync is
			// a protocol fault boundary; reset the escape decoder
			// alongside the parser so a stranded pre-fault 0xA9
			// lead can't pair with the first post-resync byte.
			t.escDecoder.Reset()
			return parseErr
		}
	}
}

// writeStartRequest writes the START request frame for initiator. It takes
// writeMu for the write only and releases it before returning, so the wait
// for the adapter's answer runs under readMu alone. Caller holds readMu.
func (t *ENHTransport) writeStartRequest(initiator byte) error {
	t.writeMu.Lock()
	seq := EncodeENH(ENHReqStart, initiator)
	written := 0
	for written < len(seq) {
		if err := t.setWriteDeadline(); err != nil {
			t.writeMu.Unlock()
			return t.mapWriteError(err)
		}
		n, err := t.conn.Write(seq[written:])
		written += n
		if err != nil {
			t.writeMu.Unlock()
			return t.mapWriteError(err)
		}
		if n == 0 {
			break
		}
	}
	t.writeMu.Unlock()
	if written != len(seq) {
		return ebuserrors.ErrInvalidPayload
	}
	return nil
}

// arbitrationOutcome is what one adapter message, or one read of them, means
// for a blocking START request: done ends the wait, and a non-nil err
// replaces any error an earlier message of the same read set.
type arbitrationOutcome struct {
	done bool
	err  error
}

// observeArbitrationBatchLocked processes every message of one read while a
// blocking START request waits for its answer and returns the combined
// outcome with the updated mismatch count. RECEIVED only advances the escape
// decoder, RESETTED reconnects in place, and every other message goes
// through classifyArbitrationMessage. Caller holds readMu.
func (t *ENHTransport) observeArbitrationBatchLocked(msgs []ENHMessage, initiator byte, mismatchCount int) (arbitrationOutcome, int) {
	var arbitrationDone bool
	var arbitrationErr error
	for _, msg := range msgs {
		switch msg.Command {
		case ENHResReceived:
			// Discard RECEIVED bytes during arbitration. These are other
			// devices' traffic, not our echoes. Buffering them would cause
			// echo mismatch in sendRawWithEcho (ReadByte drains
			// pendingEvents first, returning stale bytes as "echoes").
			//
			// F-23 (batch-19, Codex bot P2 round 2 on PR-1): the byte
			// is intentionally dropped from pendingEvents, but we MUST
			// still advance the escape decoder so a 0xA9 lead from
			// before this arbitration cycle does not strand. The
			// decoded value is then discarded — only the side effect
			// on escDecoder state matters.
			_, _, _ = t.feedEscapeDecoderLocked(msg.Data)
		case ENHResResetted:
			if reconnErr := t.reconnectLocked(); reconnErr != nil {
				arbitrationDone = true
				arbitrationErr = fmt.Errorf("enh adapter reset during arbitration, reconnect failed: %w", ebuserrors.ErrTransportClosed)
			} else {
				arbitrationDone = true
				arbitrationErr = fmt.Errorf("enh adapter reset during arbitration (features 0x%02x): %w", msg.Data, ebuserrors.ErrAdapterReset)
			}
		default:
			var outcome arbitrationOutcome
			outcome, mismatchCount = classifyArbitrationMessage(msg, initiator, mismatchCount)
			if outcome.done {
				arbitrationDone = true
			}
			if outcome.err != nil {
				arbitrationErr = outcome.err
			}
		}
	}
	return arbitrationOutcome{done: arbitrationDone, err: arbitrationErr}, mismatchCount
}

// classifyArbitrationMessage maps one STARTED, FAILED, eBUS-error or
// host-error message to its outcome for a blocking START request by
// initiator, given the number of mismatched STARTED messages seen so far,
// and returns the updated count. A STARTED for another address is tolerated
// until the third one. Any other message has no outcome. It has no side
// effects.
func classifyArbitrationMessage(msg ENHMessage, initiator byte, mismatchCount int) (arbitrationOutcome, int) {
	switch msg.Command {
	case ENHResStarted:
		if msg.Data == initiator {
			return arbitrationOutcome{done: true}, mismatchCount
		}
		mismatchCount++
		if mismatchCount >= 3 {
			return arbitrationOutcome{done: true, err: fmt.Errorf("enh arbitration started with wrong address 0x%02x (expected 0x%02x, %d mismatches): %w",
				msg.Data, initiator, mismatchCount, ebuserrors.ErrInvalidPayload)}, mismatchCount
		}
	case ENHResFailed:
		return arbitrationOutcome{done: true, err: fmt.Errorf("enh arbitration failed (initiator 0x%02x, winner 0x%02x): %w", initiator, msg.Data, ebuserrors.ErrBusCollision)}, mismatchCount
	case ENHResErrorEBUS:
		return arbitrationOutcome{done: true, err: fmt.Errorf("enh arbitration ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrBusCollision)}, mismatchCount
	case ENHResErrorHost:
		return arbitrationOutcome{done: true, err: fmt.Errorf("enh arbitration host error 0x%02x: %w", msg.Data, ebuserrors.ErrAdapterHostError)}, mismatchCount
	}
	return arbitrationOutcome{}, mismatchCount
}

// completeArbitrationLocked ends a blocking START request with the outcome
// error arbitrationErr (nil on a grant) and returns it unchanged. Caller holds
// readMu.
func (t *ENHTransport) completeArbitrationLocked(arbitrationErr error) error {
	// Reset parser and pending events so that ReadByte starts with
	// a clean state. TCP fragmentation can leave the parser with a
	// pending byte1 from a partially-received frame that arrived
	// alongside the STARTED/FAILED response. Stale RECEIVED bytes
	// in pendingEvents would be consumed as echoes by
	// sendRawWithEcho, causing echo mismatch errors.
	t.resetENHParserLocked()
	t.pendingEvents = nil
	t.deferredErr = nil
	t.awaitingStart.Store(false)
	// F-23 (batch-19, Codex bot P2 round 2 on PR-1): defense-
	// in-depth — even though discarded RECEIVED bytes during
	// arbitration now advance the decoder (in
	// observeArbitrationBatchLocked), a stranded
	// 0xA9 lead from JUST BEFORE arbitration (e.g. across a
	// read timeout that doesn't reset the decoder) could
	// survive if no byte arrived during arbitration to
	// complete it. Treat arbitration completion as a Layer-1
	// boundary that invalidates in-flight wire-stream state,
	// same as the steady-state RESETTED branch.
	t.escDecoder.Reset()
	// On successful grant, open the post-grant pre-echo window
	// so idle SYN bytes arriving between STARTED and our first
	// write's echo are suppressed. On failure/error, clear it
	// defensively.
	if arbitrationErr == nil {
		t.openPostGrantPreEchoWindow()
	} else {
		t.closePostGrantPreEchoWindow()
	}
	return arbitrationErr
}

// RequestStart sends a non-blocking START arbitration request for the given
// initiator address. It writes the ENH START frame and returns immediately
// without waiting for the adapter response.
//
// The adapter's STARTED or FAILED response will arrive asynchronously and
// can be consumed via ReadEvent (as StreamEventStarted or StreamEventFailed).
// ReadByte automatically skips these events.
//
// Only writeMu is held; readMu is NOT acquired so a concurrent ReadEvent
// loop can consume the response without deadlock.
func (t *ENHTransport) RequestStart(initiator byte) error {
	if t.closed.Load() {
		return fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	seq := EncodeENH(ENHReqStart, initiator)
	if err := t.setWriteDeadline(); err != nil {
		return t.mapWriteError(err)
	}
	// Open the arbitration window BEFORE conn.Write so STARTED emitted by
	// a fast adapter (between write-completion and our store) is correctly
	// handled by fillPendingLocked's STARTED case — which clears the flag.
	// Setting AFTER Write creates a race: STARTED arrives while flag=false,
	// gets processed normally, then our late Store(true) opens a FALSE
	// window causing post-grant RECEIVED bytes to be dropped until deadline.
	// On Write failure we roll back with Store(false) so no false window
	// persists after a blocked/failed write.
	t.arbitrationDeadline.Store(time.Now().Add(arbitrationWindowTimeout).UnixNano())
	t.awaitingStartInitiator.Store(uint32(initiator))
	t.awaitingStart.Store(true)
	n, err := t.conn.Write(seq[:])
	if err != nil {
		t.escapeResetPending.Store(true)
		t.awaitingStart.Store(false)
		t.arbitrationDeadline.Store(0)
		return t.mapWriteError(err)
	}
	if n != len(seq) {
		t.escapeResetPending.Store(true)
		t.awaitingStart.Store(false)
		t.arbitrationDeadline.Store(0)
		return fmt.Errorf("enh request start short write (%d/%d): %w", n, len(seq), ebuserrors.ErrInvalidPayload)
	}
	return nil
}

// arbitrationWindowExpired checks if the async arbitration window's
// deadline has passed. Returns true when the window must be force-closed
// even though no STARTED/FAILED has arrived.
func (t *ENHTransport) arbitrationWindowExpired() bool {
	deadline := t.arbitrationDeadline.Load()
	if deadline == 0 {
		return false
	}
	return time.Now().UnixNano() >= deadline
}

// openPostGrantPreEchoWindow sets the postGrantPreEcho flag + deadline.
// Called from STARTED handling on all code paths (blocking StartArbitration,
// fillPendingLocked, RequestInfo) so the SYN-suppression window is opened
// consistently with the deadline that bounds it.
func (t *ENHTransport) openPostGrantPreEchoWindow() {
	t.postGrantPreEchoDeadline.Store(time.Now().Add(postGrantPreEchoTimeout).UnixNano())
	t.postGrantPreEcho.Store(true)
}

// closePostGrantPreEchoWindow clears the flag + deadline atomically.
// Called when the real echo arrives (first non-SYN), on FAILED, or on any
// lifecycle reset boundary.
func (t *ENHTransport) closePostGrantPreEchoWindow() {
	t.postGrantPreEcho.Store(false)
	t.postGrantPreEchoDeadline.Store(0)
}

// postGrantPreEchoExpired reports whether the post-grant pre-echo window
// deadline has passed. Returns true even if the flag is still set — the
// caller should close the window and deliver the byte rather than
// suppress it.
func (t *ENHTransport) postGrantPreEchoExpired() bool {
	deadline := t.postGrantPreEchoDeadline.Load()
	if deadline == 0 {
		return false
	}
	return time.Now().UnixNano() >= deadline
}
