package protocol

import (
	"fmt"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// EscapeBytes escapes eBUS control symbols for wire transmission.
func EscapeBytes(raw []byte) []byte {
	escaped := make([]byte, 0, len(raw))
	for _, b := range raw {
		switch b {
		case SymbolEscape:
			escaped = append(escaped, SymbolEscape, 0x00)
		case SymbolSyn:
			escaped = append(escaped, SymbolEscape, 0x01)
		default:
			escaped = append(escaped, b)
		}
	}
	return escaped
}

// EncodeSlaveResponse builds a wire-ready eBUS response segment.
// Data must be 0–255 bytes (NN is a single byte).
func EncodeSlaveResponse(data []byte) ([]byte, error) {
	if len(data) > 255 {
		return nil, fmt.Errorf("target response data length %d exceeds 255: %w", len(data), ebuserrors.ErrInvalidPayload)
	}
	segment := make([]byte, 0, len(data)+2)
	segment = append(segment, byte(len(data)))
	segment = append(segment, data...)
	segment = append(segment, CRC(segment))

	return EscapeBytes(segment), nil
}
