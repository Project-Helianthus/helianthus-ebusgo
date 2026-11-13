package types

import ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"

const (
	data1bSize        = 1
	data1bReplacement = byte(0x80)
)

// DATA1b (D1B) is a 1-byte signed integer with 0x80 as replacement value.
type DATA1b struct{}

// Size returns 1, the encoded width in bytes.
func (DATA1b) Size() int {
	return data1bSize
}

// ReplacementValue returns a new slice holding 0x80, the DATA1b "no data"
// value.
func (DATA1b) ReplacementValue() []byte {
	return []byte{data1bReplacement}
}

// Decode reads the first payload byte as a two's-complement int8. 0x80
// decodes to Valid false; an empty payload fails with
// errors.ErrInvalidPayload. The wire format is documented at
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/types/primitives.md
func (DATA1b) Decode(payload []byte) (Value, error) {
	if len(payload) < data1bSize {
		return Value{}, ebuserrors.ErrInvalidPayload
	}

	b := payload[0]
	if b == data1bReplacement {
		return Value{Valid: false}, nil
	}

	return Value{Value: int8(b), Valid: true}, nil
}

// Encode writes an integer from -127 to 127 as one byte. Non-integer
// inputs and values outside that range, -128 included, fail with
// errors.ErrInvalidPayload.
func (DATA1b) Encode(value any) ([]byte, error) {
	i, ok := toInt64(value)
	if !ok {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if i < -127 || i > 127 {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if i == -128 {
		return nil, ebuserrors.ErrInvalidPayload
	}

	return []byte{byte(int8(i))}, nil
}
