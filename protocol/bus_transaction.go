package protocol

import (
	"context"
	"errors"
	"fmt"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// readByte blocks until a byte is available from the transport or an error
// occurs. The blocking duration is bounded by the transport's read timeout.
// To interrupt, close the transport connection which unblocks the read.
func (b *Bus) readByte(runCtx, reqCtx context.Context) (byte, error) {
	if err := b.contextError(runCtx, reqCtx); err != nil {
		return 0, err
	}
	value, err := b.transport.ReadByte()
	if err != nil {
		return 0, err
	}
	b.emitObserverEvent(BusEvent{
		Kind:    BusEventRX,
		Outcome: BusOutcomeSuccess,
		Byte:    value,
	})
	return value, nil
}

// readByteWithEscape reads one byte and its WasEscaped flag from a
// transport that implements EscapeFlaggedReader. Caller is expected
// to have type-asserted the transport once and passed it in. F-23
// (batch-19, Codex bot review on PR-1): used by waitForSyn so
// escape-decoded payload 0xAA bytes are not falsely counted as real
// wire SYNs in the bus-idle detection path.
func (b *Bus) readByteWithEscape(runCtx, reqCtx context.Context, flagged transport.EscapeFlaggedReader) (byte, bool, error) {
	if err := b.contextError(runCtx, reqCtx); err != nil {
		return 0, false, err
	}
	value, wasEscaped, err := flagged.ReadByteWithEscape()
	if err != nil {
		return 0, false, err
	}
	b.emitObserverEvent(BusEvent{
		Kind:    BusEventRX,
		Outcome: BusOutcomeSuccess,
		Byte:    value,
	})
	return value, wasEscaped, nil
}

type busDecoder struct {
	escape bool
}

func (d *busDecoder) readSymbol(b *Bus, runCtx, reqCtx context.Context) (byte, error) {
	if b.unescapedTransport {
		// ENH transport delivers pre-unescaped bytes; no wire-level escape processing.
		raw, err := b.readByte(runCtx, reqCtx)
		if err != nil {
			return 0, err
		}
		return raw, nil
	}
	// Plain transport: decode eBUS escape sequences (0xA9 + 0x00/0x01).
	for {
		raw, err := b.readByte(runCtx, reqCtx)
		if err != nil {
			if errors.Is(err, ebuserrors.ErrTimeout) {
				d.escape = false
			}
			return 0, err
		}

		if d.escape {
			d.escape = false
			switch raw {
			case 0x00:
				return SymbolEscape, nil
			case 0x01:
				return SymbolSyn, nil
			default:
				return 0, fmt.Errorf("invalid escape sequence 0x%02x: %w", raw, ebuserrors.ErrInvalidPayload)
			}
		}

		if raw == SymbolEscape {
			d.escape = true
			continue
		}
		return raw, nil
	}
}

// transactionAttempt carries what every step of one sendTransaction call
// reads: the request frame, its type, the attempt number, the start time and
// the symbol decoder shared by the command and the response phase.
type transactionAttempt struct {
	frame     Frame
	frameType FrameType
	attempt   uint16
	startedAt time.Time
	decoder   *busDecoder
}

func (b *Bus) sendTransaction(runCtx, reqCtx context.Context, frame Frame, attempt uint16) (*Frame, error) {
	frameType := frame.Type()
	if frameType == FrameTypeUnknown {
		return nil, errUnknownFrameType
	}
	if len(frame.Data) > 255 {
		return nil, fmt.Errorf("frame data length %d exceeds eBUS maximum 255: %w", len(frame.Data), ebuserrors.ErrInvalidPayload)
	}
	tx := &transactionAttempt{frame: frame, frameType: frameType, attempt: attempt, startedAt: time.Now()}

	telegram := buildInitiatorTelegram(frame)
	tx.decoder = &busDecoder{}
	includeSource := b.commandIncludesSource()

	broadcastDone, err := b.sendCommandUntilAck(runCtx, reqCtx, tx, telegram, includeSource)
	if err != nil {
		return nil, err
	}
	if broadcastDone {
		return nil, nil
	}

	if frameType == FrameTypeInitiatorInitiator {
		if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
			return nil, err
		}
		b.emitAttemptComplete(frame, nil, frameType, attempt, tx.startedAt)
		return nil, nil
	}
	if frameType != FrameTypeInitiatorTarget {
		return nil, errUnknownFrameType
	}
	return b.collectResponse(runCtx, reqCtx, tx)
}

// buildInitiatorTelegram returns the initiator telegram (unescaped symbols):
// SRC DST PB SB LEN DATA... CRC
func buildInitiatorTelegram(frame Frame) []byte {
	telegram := make([]byte, 0, 6+len(frame.Data))
	telegram = append(telegram, frame.Source, frame.Target, frame.Primary, frame.Secondary, byte(len(frame.Data)))
	telegram = append(telegram, frame.Data...)
	telegram = append(telegram, CRC(telegram))
	return telegram
}

// commandIncludesSource tells whether the first command send writes SRC:
// for enhanced transports, SRC is already sent during arbitration.
func (b *Bus) commandIncludesSource() bool {
	includeSource := true
	if _, ok := b.transport.(arbitrationTransport); ok {
		includeSource = false
		if behavior, ok := b.transport.(arbitrationSourceBehavior); ok {
			includeSource = !behavior.ArbitrationSendsSource()
		}
	}
	return includeSource
}

// sendCommandUntilAck sends the command telegram until the target
// acknowledges it. It reports broadcastDone when a broadcast telegram has
// been completed (broadcasts are never acknowledged).
//
// Inner 2-attempt loop: per eBUS spec, the initiator retransmits the
// command telegram once after a NACK without re-arbitrating (commandAttempt
// 0 = first send, 1 = spec-mandated retry). The outer sendWithRetries
// applies policy-based retries that involve re-arbitration. With default
// NACKRetries=1 this yields at most: 2 inner sends + re-arbitrate + 2 inner
// sends = 4 total transmissions. This is correct per spec.
func (b *Bus) sendCommandUntilAck(runCtx, reqCtx context.Context, tx *transactionAttempt, telegram []byte, includeSource bool) (bool, error) {
	acked := false
	for commandAttempt := 0; commandAttempt < 2; commandAttempt++ {
		if err := b.sendInitiatorTelegram(runCtx, reqCtx, telegram, includeSource); err != nil {
			return false, err
		}

		if tx.frameType == FrameTypeBroadcast {
			if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
				return false, err
			}
			b.emitAttemptComplete(tx.frame, nil, tx.frameType, tx.attempt, tx.startedAt)
			return true, nil
		}

		ack, err := tx.decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
			return false, err
		}
		acked, err = b.handleCommandAck(runCtx, reqCtx, tx, ack, commandAttempt)
		if err != nil {
			return false, err
		}
		if !acked {
			// Repeat command once without arbitration; SRC must be sent explicitly.
			includeSource = true
			continue
		}
		break
	}
	if !acked {
		return false, errCommandAckLoopExhausted
	}
	return false, nil
}

// handleCommandAck classifies the symbol read after a command telegram. It
// returns true on ACK, false without an error when a first NACK asks for one
// retransmission, and the error the transaction ends with otherwise.
func (b *Bus) handleCommandAck(runCtx, reqCtx context.Context, tx *transactionAttempt, ack byte, commandAttempt int) (bool, error) {
	switch ack {
	case SymbolAck:
		b.emitObserverEvent(BusEvent{
			Kind:       BusEventACK,
			FrameType:  tx.frameType,
			Outcome:    BusOutcomeSuccess,
			Byte:       SymbolAck,
			Attempt:    tx.attempt,
			Request:    tx.frame,
			HasRequest: true,
		})
		return true, nil
	case SymbolNack:
		b.emitObserverEvent(BusEvent{
			Kind:       BusEventNACK,
			FrameType:  tx.frameType,
			Outcome:    BusOutcomeNACK,
			Byte:       SymbolNack,
			Attempt:    tx.attempt,
			Request:    tx.frame,
			HasRequest: true,
		})
		if commandAttempt == 0 {
			return false, nil
		}
		_ = b.sendEndOfMessage(runCtx, reqCtx)
		err := fmt.Errorf("nack received: %w", ebuserrors.ErrNACK)
		b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
		return false, err
	case SymbolSyn:
		// On plain transports, SYN during ACK wait means the bus went
		// idle without a response (timeout). On ENH transports, 0xAA is
		// a valid data byte — the adapter does not send idle SYN.
		if !b.unescapedTransport {
			err := fmt.Errorf("syn while waiting for command ack: %w", ebuserrors.ErrTimeout)
			b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
			return false, err
		}
		// ENH: treat 0xAA as unexpected symbol, not SYN timeout.
		err := fmt.Errorf("unexpected symbol 0x%02x while waiting for command ack: %w", ack, ebuserrors.ErrTimeout)
		b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
		return false, err
	default:
		err := fmt.Errorf("unexpected symbol 0x%02x while waiting for command ack: %w", ack, ebuserrors.ErrTimeout)
		b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
		return false, err
	}
}

// collectResponse reads the target response, checks its CRC and answers it
// with ACK or NACK; a first CRC mismatch is answered with NACK and the
// response is read once more.
//
// eBUS target response: NN DB1..DBn CRC (no header - QQ/ZZ/PB/SB are
// inferred from the initiator telegram).
func (b *Bus) collectResponse(runCtx, reqCtx context.Context, tx *transactionAttempt) (*Frame, error) {
	for respAttempt := 0; respAttempt < 2; respAttempt++ {
		lengthSym, data, crcValue, err := b.readResponse(runCtx, reqCtx, tx)
		if err != nil {
			return nil, err
		}

		segment := make([]byte, 0, 1+len(data))
		segment = append(segment, lengthSym)
		segment = append(segment, data...)
		if CRC(segment) != crcValue {
			if err := b.rejectResponse(runCtx, reqCtx, tx, data, respAttempt); err != nil {
				return nil, err
			}
			continue
		}
		return b.acceptResponse(runCtx, reqCtx, tx, data)
	}

	err := fmt.Errorf("unreachable response loop: %w", ebuserrors.ErrTimeout)
	b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
	return nil, err
}

// readResponse reads the length, the data and the CRC of one response.
func (b *Bus) readResponse(runCtx, reqCtx context.Context, tx *transactionAttempt) (byte, []byte, byte, error) {
	lengthSym, err := tx.decoder.readSymbol(b, runCtx, reqCtx)
	if err != nil {
		b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
		return 0, nil, 0, err
	}
	// On plain transports, SYN (0xAA) during response means bus went idle
	// (timeout). On ENH transports, 0xAA is a valid data byte — the
	// adapter does not inject idle SYN into the byte stream.
	if !b.unescapedTransport && lengthSym == SymbolSyn {
		err := fmt.Errorf("syn while waiting for response length: %w", ebuserrors.ErrTimeout)
		b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
		return 0, nil, 0, err
	}

	length := int(lengthSym)
	data := make([]byte, length)
	for i := 0; i < length; i++ {
		value, err := tx.decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
			return 0, nil, 0, err
		}
		if !b.unescapedTransport && value == SymbolSyn {
			err := fmt.Errorf("syn while reading response data: %w", ebuserrors.ErrTimeout)
			b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
			return 0, nil, 0, err
		}
		data[i] = value
	}

	crcValue, err := tx.decoder.readSymbol(b, runCtx, reqCtx)
	if err != nil {
		b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
		return 0, nil, 0, err
	}
	if !b.unescapedTransport && crcValue == SymbolSyn {
		err := fmt.Errorf("syn while waiting for response crc: %w", ebuserrors.ErrTimeout)
		b.emitOutcomeEvent(tx.frame, tx.frameType, tx.attempt, err)
		return 0, nil, 0, err
	}
	return lengthSym, data, crcValue, nil
}

// rejectResponse answers a response whose CRC does not match with NACK. It
// returns nil after the first mismatch (the response is read once more) and
// the error the transaction ends with after the second.
func (b *Bus) rejectResponse(runCtx, reqCtx context.Context, tx *transactionAttempt, data []byte, respAttempt int) error {
	frame := tx.frame
	// ACK/NACK are response-phase sends — never the first wire byte
	// after arbitration, so isFirstByteAfterArbitration=false.
	if err := b.sendSymbolWithEcho(runCtx, reqCtx, SymbolNack, true, false); err != nil {
		return err
	}
	b.emitObserverEvent(BusEvent{
		Kind:        BusEventNACK,
		FrameType:   tx.frameType,
		Outcome:     BusOutcomeCRCMismatch,
		Byte:        SymbolNack,
		Attempt:     tx.attempt,
		Request:     frame,
		Response:    Frame{Source: frame.Target, Target: frame.Source, Primary: frame.Primary, Secondary: frame.Secondary, Data: data},
		HasRequest:  true,
		HasResponse: true,
	})
	if respAttempt == 0 {
		b.emitObserverEvent(BusEvent{
			Kind:        BusEventCRCMismatch,
			FrameType:   tx.frameType,
			Outcome:     BusOutcomeCRCMismatch,
			Attempt:     tx.attempt,
			Request:     frame,
			Response:    Frame{Source: frame.Target, Target: frame.Source, Primary: frame.Primary, Secondary: frame.Secondary, Data: data},
			HasRequest:  true,
			HasResponse: true,
		})
		return nil
	}
	_ = b.sendEndOfMessage(runCtx, reqCtx)
	err := fmt.Errorf("crc mismatch: %w", ebuserrors.ErrCRCMismatch)
	b.emitOutcomeEvent(frame, tx.frameType, tx.attempt, err)
	return err
}

// acceptResponse answers a response whose CRC matches with ACK, ends the
// message and completes the attempt.
func (b *Bus) acceptResponse(runCtx, reqCtx context.Context, tx *transactionAttempt, data []byte) (*Frame, error) {
	frame := tx.frame
	// ACK/NACK are response-phase sends — never the first wire byte
	// after arbitration, so isFirstByteAfterArbitration=false.
	if err := b.sendSymbolWithEcho(runCtx, reqCtx, SymbolAck, true, false); err != nil {
		return nil, err
	}
	response := &Frame{
		Source:    frame.Target,
		Target:    frame.Source,
		Primary:   frame.Primary,
		Secondary: frame.Secondary,
		Data:      data,
	}
	b.emitObserverEvent(BusEvent{
		Kind:        BusEventACK,
		FrameType:   tx.frameType,
		Outcome:     BusOutcomeSuccess,
		Byte:        SymbolAck,
		Attempt:     tx.attempt,
		Request:     frame,
		Response:    *response,
		HasRequest:  true,
		HasResponse: true,
	})
	if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
		return nil, err
	}
	b.emitAttemptComplete(frame, response, tx.frameType, tx.attempt, tx.startedAt)
	return response, nil
}

func (b *Bus) sendInitiatorTelegram(runCtx, reqCtx context.Context, telegram []byte, includeSource bool) error {
	start := 0
	if !includeSource {
		start = 1
	}
	for i := start; i < len(telegram); i++ {
		// batch-26 round-7 — first-byte arbitration-loss split. The
		// first iteration of this loop writes the first wire byte
		// AFTER arbitration: with includeSource=false (ENH path) it's
		// telegram[1] (DST); with includeSource=true (plain-path
		// NACK-retry) it's telegram[0] (SRC re-sent without
		// re-arbitrating). In both cases i==start identifies the
		// position. sendSymbolWithEcho receives the flag and the
		// downstream sendRawWithEcho classifies a mismatch from a
		// foreign initiator-class byte as ErrBusCollision rather than
		// ErrBusCollision-via-"echo mismatch" string (which routes
		// through BusOutcomeEchoMismatch).
		isFirstByte := i == start
		if err := b.sendSymbolWithEcho(runCtx, reqCtx, telegram[i], true, isFirstByte); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bus) sendEndOfMessage(runCtx, reqCtx context.Context) error {
	// End-of-message SYN is never the first byte after arbitration —
	// it terminates a frame whose first byte fired far earlier in
	// sendInitiatorTelegram. Pass isFirstByteAfterArbitration=false.
	return b.sendSymbolWithEcho(runCtx, reqCtx, SymbolSyn, false, false)
}
