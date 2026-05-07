package protocol

import "github.com/Project-Helianthus/helianthus-ebusgo/internal/crc"

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
//
// FrameType is an optional explicit declaration of the frame's
// application-layer semantic kind (M2S / M2M / M2BC). When zero-valued
// (FrameTypeUnknown), the frame's effective type is derived from
// Target via FrameTypeForTarget. The Phase C transport-side validator
// (Bus.Send + Frame.Validate) requires every emitted frame to have an
// explicit non-zero FrameType for new semantic API call sites — see
// Frame.Validate doc + Phase C M-C7 enrichment.
type Frame struct {
	Source    byte
	Target    byte
	Primary   byte
	Secondary byte
	Data      []byte
	FrameType FrameType
}

// Type returns the frame type based on the target address.
//
// Used by parser-side code that always derives from Target. New
// emit-side code should prefer Frame.EffectiveFrameType which respects
// the explicit Frame.FrameType field.
func (f Frame) Type() FrameType {
	return FrameTypeForTarget(f.Target)
}

// EffectiveFrameType returns the explicit Frame.FrameType when set
// (non-Unknown), otherwise FrameTypeForTarget(Target). Used by
// Frame.Validate to assert caller intent matches the destination class.
func (f Frame) EffectiveFrameType() FrameType {
	if f.FrameType != FrameTypeUnknown {
		return f.FrameType
	}
	return FrameTypeForTarget(f.Target)
}

// Validate enforces the Phase C frame-type addressing contract by
// delegating to ValidateFrameAddressing(EffectiveFrameType, Source,
// Target). Returns nil on conformance; ErrInvalidFrameAddress (or a
// wrapped variant) on rejection.
//
// Decision references: AD24 (additive Frame.FrameType field +
// Frame.Validate invoked from Bus.Send), AD26 (validator contract),
// AD28 (per-API-site explicit frame-type declaration).
func (f Frame) Validate() error {
	return ValidateFrameAddressing(f.EffectiveFrameType(), f.Source, f.Target)
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

// CRC calculates the eBUS CRC8. Per spec, logical bytes 0xA9 and 0xAA are
// substituted with their wire-escape sequences (0xA9->{0xA9,0x00} and
// 0xAA->{0xA9,0x01}) before the CRC update. The input is unescaped logical
// bytes; the function applies the substitution internally.
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
// (initiator) address according to the eBUS address table.
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
