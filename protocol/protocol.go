package protocol

import "github.com/d3vi1/helianthus-ebusgo/internal/crc"

const (
	AddressBroadcast = byte(0xFE)
	SymbolEscape     = byte(0xA9)
	SymbolSyn        = byte(0xAA)
	SymbolAck        = byte(0x00)
	SymbolNack       = byte(0xFF)
)

type FrameType uint8

const (
	FrameTypeUnknown FrameType = iota
	FrameTypeBroadcast
	FrameTypeInitiatorTarget
	FrameTypeInitiatorInitiator
)

// Frame represents a parsed eBUS frame.
type Frame struct {
	Source    byte
	Target    byte
	Primary   byte
	Secondary byte
	Data      []byte
}

// Type returns the frame type based on the target address.
func (f Frame) Type() FrameType {
	return FrameTypeForTarget(f.Target)
}

// FrameTypeForTarget determines the frame type based on the destination address.
func FrameTypeForTarget(target byte) FrameType {
	if target == AddressBroadcast {
		return FrameTypeBroadcast
	}
	if !isValidAddress(target) {
		return FrameTypeUnknown
	}
	if IsInitiatorCapableAddress(target) {
		return FrameTypeInitiatorInitiator
	}
	return FrameTypeInitiatorTarget
}

// CRC calculates the eBUS CRC8 over unescaped symbols.
func CRC(data []byte) byte {
	value := byte(0)
	for _, b := range data {
		switch b {
		case SymbolEscape:
			value = crc.Update(value, SymbolEscape)
			value = crc.Update(value, 0x00)
		case SymbolSyn:
			value = crc.Update(value, SymbolEscape)
			value = crc.Update(value, 0x01)
		default:
			value = crc.Update(value, b)
		}
	}
	return value
}

func isValidAddress(addr byte) bool {
	return addr != SymbolEscape && addr != SymbolSyn
}

// IsInitiatorCapableAddress reports whether addr is a valid eBUS initiator
// (master) address according to the eBUS address table.
func IsInitiatorCapableAddress(addr byte) bool {
	return initiatorPartIndex(addr&0x0F) > 0 && initiatorPartIndex((addr&0xF0)>>4) > 0
}

func initiatorPartIndex(bits byte) byte {
	switch bits {
	case 0x0:
		return 1
	case 0x1:
		return 2
	case 0x3:
		return 3
	case 0x7:
		return 4
	case 0xF:
		return 5
	default:
		return 0
	}
}
