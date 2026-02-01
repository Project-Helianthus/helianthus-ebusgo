package protocol

import "github.com/d3vi1/helianthus-ebusgo/internal/crc"

const (
	AddressBroadcast = byte(0xFE)
	SymbolEscape     = byte(0xA9)
	SymbolSyn        = byte(0xAA)
)

type FrameType uint8

const (
	FrameTypeUnknown FrameType = iota
	FrameTypeBroadcast
	FrameTypeMasterSlave
	FrameTypeMasterMaster
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
	if isMasterAddress(target) {
		return FrameTypeMasterMaster
	}
	return FrameTypeMasterSlave
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

func isMasterAddress(addr byte) bool {
	return masterPartIndex(addr&0x0F) > 0 && masterPartIndex((addr&0xF0)>>4) > 0
}

func masterPartIndex(bits byte) byte {
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
