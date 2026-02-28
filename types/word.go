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

func (WORD) Size() int {
	return wordSize
}

func (WORD) ReplacementValue() []byte {
	return []byte{0xFF, 0xFF}
}

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
