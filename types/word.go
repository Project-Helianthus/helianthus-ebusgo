package types

import (
	"encoding/binary"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

const (
	wordSize        = 2
	wordReplacement = uint16(0xFFFF)
)

// WORD is an unsigned 16-bit little-endian value.
type WORD struct{}

// Size returns 2, the encoded width in bytes.
func (WORD) Size() int {
	return wordSize
}

// ReplacementValue returns a new slice holding 0xFF 0xFF, the WORD
// "no data" value.
func (WORD) ReplacementValue() []byte {
	return []byte{0xFF, 0xFF}
}

// Decode reads the first two bytes as a little-endian uint16. 0xFFFF
// decodes to Valid false; a payload shorter than two bytes fails with
// errors.ErrInvalidPayload. The wire format is documented at
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/types/primitives.md
func (WORD) Decode(payload []byte) (Value, error) {
	if len(payload) < wordSize {
		return Value{}, ebuserrors.ErrInvalidPayload
	}

	raw := binary.LittleEndian.Uint16(payload)
	if raw == wordReplacement {
		return Value{Valid: false}, nil
	}

	return Value{Value: raw, Valid: true}, nil
}

// Encode writes an integer from 0 to 65534 as a little-endian uint16.
// Non-integer inputs and values outside that range, 65535 included, fail
// with errors.ErrInvalidPayload.
func (WORD) Encode(value any) ([]byte, error) {
	i, ok := toInt64(value)
	if !ok {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if i < 0 || i > 65534 {
		return nil, ebuserrors.ErrInvalidPayload
	}

	u := uint16(i)
	buf := make([]byte, wordSize)
	binary.LittleEndian.PutUint16(buf, u)
	return buf, nil
}
