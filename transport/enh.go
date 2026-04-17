package transport

import ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"

// ENHCommand represents an enhanced protocol command or response ID.
type ENHCommand byte

const (
	ENHReqInit  ENHCommand = 0x0
	ENHReqSend  ENHCommand = 0x1
	ENHReqStart ENHCommand = 0x2
	ENHReqInfo  ENHCommand = 0x3

	ENHResResetted  ENHCommand = 0x0
	ENHResReceived  ENHCommand = 0x1
	ENHResStarted   ENHCommand = 0x2
	ENHResInfo      ENHCommand = 0x3
	ENHResFailed    ENHCommand = 0xA
	ENHResErrorEBUS ENHCommand = 0xB
	ENHResErrorHost ENHCommand = 0xC
)

const (
	enhByteFlag = byte(0x80)
	enhByteMask = byte(0xC0)
	enhByte1    = byte(0xC0)
	enhByte2    = byte(0x80)
)

// ENHFrame is a decoded enhanced protocol frame.
type ENHFrame struct {
	Command ENHCommand
	Data    byte
}

// EncodeENH encodes a command and data byte into an enhanced protocol sequence.
func EncodeENH(command ENHCommand, data byte) [2]byte {
	byte1 := enhByte1 | (byte(command) << 2) | ((data & 0xC0) >> 6)
	byte2 := enhByte2 | (data & 0x3F)
	return [2]byte{byte1, byte2}
}

// DecodeENH decodes a two-byte enhanced protocol sequence into a frame.
func DecodeENH(byte1, byte2 byte) (ENHFrame, error) {
	if byte1&enhByteMask != enhByte1 {
		return ENHFrame{}, ebuserrors.ErrInvalidPayload
	}
	if byte2&enhByteMask != enhByte2 {
		return ENHFrame{}, ebuserrors.ErrInvalidPayload
	}
	cmd := ENHCommand((byte1 >> 2) & 0x0F)
	// Validate command nibble — only 0x0..0x3, 0xA, 0xB, 0xC are defined.
	switch cmd {
	case ENHReqInit, ENHReqSend, ENHReqStart, ENHReqInfo,
		ENHResFailed, ENHResErrorEBUS, ENHResErrorHost:
		// Valid (note: 0x0..0x3 overlap request/response by direction)
	default:
		return ENHFrame{}, ebuserrors.ErrInvalidPayload
	}
	data := byte(((byte1 & 0x03) << 6) | (byte2 & 0x3F))
	return ENHFrame{Command: cmd, Data: data}, nil
}

// ENHMessageKind describes a parsed enhanced stream item.
type ENHMessageKind uint8

const (
	ENHMessageFrame ENHMessageKind = iota
)

// ENHMessage represents either a raw data byte or an enhanced frame.
type ENHMessage struct {
	Kind    ENHMessageKind
	Byte    byte
	Command ENHCommand
	Data    byte
}

// ENHParser incrementally parses enhanced protocol data streams.
type ENHParser struct {
	pending bool
	byte1   byte
}

// Reset clears any pending state.
func (p *ENHParser) Reset() {
	p.pending = false
	p.byte1 = 0
}

// Feed consumes a single byte and returns a parsed message when complete.
func (p *ENHParser) Feed(b byte) (ENHMessage, bool, error) {
	if !p.pending {
		if b&enhByteFlag == 0 {
			return ENHMessage{Kind: ENHMessageFrame, Command: ENHResReceived, Data: b}, true, nil
		}
		if b&enhByteMask == enhByte2 {
			return ENHMessage{}, false, ebuserrors.ErrInvalidPayload
		}
		p.pending = true
		p.byte1 = b
		return ENHMessage{}, false, nil
	}

	if b&enhByteMask != enhByte2 {
		p.pending = false
		return ENHMessage{}, false, ebuserrors.ErrInvalidPayload
	}

	frame, err := DecodeENH(p.byte1, b)
	p.pending = false
	if err != nil {
		return ENHMessage{}, false, err
	}

	return ENHMessage{Kind: ENHMessageFrame, Command: frame.Command, Data: frame.Data}, true, nil
}

// Parse consumes a byte slice and returns messages parsed up to (but not
// including) the first error. Bytes after the first error are NOT
// processed — the caller must surface the error before reading further
// data, so protocol violations create a crisp fault boundary instead of
// leaking post-violation bytes to upper layers.
func (p *ENHParser) Parse(data []byte) ([]ENHMessage, error) {
	if len(data) == 0 {
		return nil, nil
	}

	out := make([]ENHMessage, 0, len(data))
	for _, b := range data {
		msg, ok, err := p.Feed(b)
		if err != nil {
			return out, err
		}
		if ok {
			out = append(out, msg)
		}
	}
	return out, nil
}
