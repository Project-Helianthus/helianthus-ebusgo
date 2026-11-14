//go:build !tinygo

package transport

import (
	"errors"
	"fmt"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// requestInfoFail performs the on-error cleanup for RequestInfo while both
// readMu and writeMu are still held by the caller. This is intentionally
// NOT a deferred function — deferred execution orders with writeMu.Unlock
// create a race window where a blocked RequestStart can acquire writeMu,
// set awaitingStart=true, and have that state stomped by the cleanup.
// Keeping this synchronous and explicit, called before each error return
// and BEFORE releasing writeMu, eliminates the race entirely.
func (t *ENHTransport) requestInfoFail(err error) error {
	wasAwaitingStart := t.awaitingStart.Load()
	t.resetENHParserLocked()
	t.deferredErr = nil
	t.awaitingStart.Store(false)
	t.arbitrationDeadline.Store(0)
	t.closePostGrantPreEchoWindow()
	// F-23 (batch-19, Codex bot P-r3 + P-r4): RequestInfo fault
	// recovery is a wire-stream boundary for PROTOCOL errors
	// (RESETTED, ebus/host error, parse-error resync). Wipe in-
	// flight escape state in those cases so a stranded 0xA9 lead
	// from before the fault can't pair with the first post-recovery
	// byte. Pure passive timeouts are NOT protocol faults — the bus simply
	// went quiet, and a legitimate pair-completion byte may arrive on the
	// next read after the caller retries. But if RequestInfo timed out while
	// an async arbitration window was open, RECEIVED bytes may have been fed
	// through the decoder and then discarded as pre-grant traffic. Closing
	// that arbitration window is a Layer-1 abort boundary, so any in-flight
	// discarded escape lead is stale and must be reset.
	if !errors.Is(err, ebuserrors.ErrTimeout) || wasAwaitingStart {
		t.escDecoder.Reset()
	}
	// Preserve buffered events on timeout/error so they are not silently
	// dropped. Clear pending on fatal transport errors and adapter resets
	// where the parser state is unrecoverable or events from the same TCP
	// segment after RESETTED would be stale.
	if errors.Is(err, ebuserrors.ErrTransportClosed) || errors.Is(err, ebuserrors.ErrAdapterReset) {
		t.pendingEvents = nil
	}
	return err
}

// requestInfoFailUnlock runs requestInfoFail, then releases writeMu, then
// readMu, and returns the error requestInfoFail returned.
func (t *ENHTransport) requestInfoFailUnlock(err error) error {
	wrapped := t.requestInfoFail(err)
	t.writeMu.Unlock()
	t.readMu.Unlock()
	return wrapped
}

// RequestInfo sends an INFO request for the given ID and returns the raw
// response payload. The exchange is transport-exclusive: both readMu and writeMu
// are held for the duration to prevent interleaving with bus operations.
//
// Returns ErrTimeout if the response does not arrive within readTimeout.
// Returns ErrAdapterReset if a RESETTED frame is received mid-exchange.
func (t *ENHTransport) RequestInfo(id AdapterInfoID) ([]byte, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("enh transport closed: %w", ebuserrors.ErrTransportClosed)
	}
	t.readMu.Lock()
	t.writeMu.Lock()

	// Send the INFO request.
	if err := t.writeInfoRequestLocked(id); err != nil {
		return nil, t.requestInfoFailUnlock(err)
	}

	infoTimeout := t.readTimeout
	if infoTimeout <= 0 {
		infoTimeout = 2 * time.Second // Fallback matches Init default
	}
	readDeadline := time.Now().Add(infoTimeout)

	// Read response: first INFO frame has length, then N data frames.
	collector := newInfoPayloadCollector()
	resetBeforeCompletion := false

	for {
		if time.Now().After(readDeadline) {
			return nil, t.requestInfoFailUnlock(fmt.Errorf("enh info exchange deadline exceeded: %w", ebuserrors.ErrTimeout))
		}
		if err := t.conn.SetReadDeadline(readDeadline); err != nil {
			return nil, t.requestInfoFailUnlock(t.mapReadError(err))
		}

		n, err := t.conn.Read(t.buffer)
		if err != nil {
			return nil, t.requestInfoFailUnlock(t.mapReadError(err))
		}
		if n == 0 {
			continue
		}

		msgs, parseErr := t.parseObservedLocked(t.buffer[:n])

		// Process valid messages before handling parse error — a valid
		// INFO response followed by a corrupt trailing byte should succeed.
		resetSeen, err := t.observeInfoBatchLocked(msgs, &collector)
		if err != nil {
			return nil, t.requestInfoFailUnlock(err)
		}
		if resetSeen {
			resetBeforeCompletion = true
		}

		if resetBeforeCompletion {
			return nil, t.requestInfoFailUnlock(fmt.Errorf("enh adapter resetted during info request: %w", ebuserrors.ErrAdapterReset))
		}
		if collector.complete {
			t.writeMu.Unlock()
			t.readMu.Unlock()
			return collector.payload, nil
		}
		// Handle parse error only after processing valid messages.
		if parseErr != nil {
			return nil, t.requestInfoFailUnlock(parseErr)
		}
	}
}

// writeInfoRequestLocked writes the INFO request frame for id. It returns the
// error RequestInfo hands to requestInfoFail. Caller holds readMu and writeMu.
func (t *ENHTransport) writeInfoRequestLocked(id AdapterInfoID) error {
	seq := EncodeENH(ENHReqInfo, byte(id))
	written := 0
	for written < len(seq) {
		if err := t.setWriteDeadline(); err != nil {
			return t.mapWriteError(err)
		}
		n, err := t.conn.Write(seq[written:])
		written += n
		if err != nil {
			return t.mapWriteError(err)
		}
		if n == 0 {
			break
		}
	}
	if written != len(seq) {
		return fmt.Errorf("enh info request write incomplete: %w", ebuserrors.ErrInvalidPayload)
	}
	return nil
}

// observeInfoBatchLocked processes the messages of one INFO-exchange read in
// order. RECEIVED, STARTED and FAILED take the steady-state read's handling;
// INFO frames feed c. resetBeforeCompletion reports a RESETTED seen while the
// payload was still incomplete. An adapter error stops the batch at that
// message and is returned as err. Caller holds readMu and writeMu.
func (t *ENHTransport) observeInfoBatchLocked(msgs []ENHMessage, c *infoPayloadCollector) (resetBeforeCompletion bool, err error) {
	for _, msg := range msgs {
		switch msg.Command {
		case ENHResInfo:
			c.add(msg.Data)
		case ENHResReceived:
			t.observeReceivedLocked(msg)
		case ENHResStarted:
			t.observeStartedLocked(msg)
		case ENHResFailed:
			t.observeFailedLocked(msg)
		case ENHResResetted:
			t.surfaceResetLocked()
			if !c.complete {
				resetBeforeCompletion = true
			}
		case ENHResErrorEBUS:
			return resetBeforeCompletion, fmt.Errorf("enh info ebus error 0x%02x: %w", msg.Data, ebuserrors.ErrInvalidPayload)
		case ENHResErrorHost:
			return resetBeforeCompletion, fmt.Errorf("enh info host error 0x%02x: %w", msg.Data, ebuserrors.ErrAdapterHostError)
		}
	}
	return resetBeforeCompletion, nil
}

// infoPayloadCollector assembles an INFO response: the first INFO frame
// carries the payload length, the following INFO frames carry one data byte
// each. Frames beyond the announced length are ignored.
type infoPayloadCollector struct {
	payloadLen int // -1 until the length frame arrived
	payload    []byte
	complete   bool
}

func newInfoPayloadCollector() infoPayloadCollector {
	return infoPayloadCollector{payloadLen: -1}
}

// add consumes the data byte of one INFO frame.
func (c *infoPayloadCollector) add(data byte) {
	if c.payloadLen < 0 {
		// First INFO response: length byte.
		c.payloadLen = int(data)
		if c.payloadLen == 0 {
			c.complete = true
			c.payload = []byte{}
			return
		}
		c.payload = make([]byte, 0, c.payloadLen)
	} else {
		// Subsequent INFO responses: data bytes.
		if len(c.payload) < c.payloadLen {
			c.payload = append(c.payload, data)
			if len(c.payload) >= c.payloadLen {
				c.complete = true
			}
		}
	}
}
