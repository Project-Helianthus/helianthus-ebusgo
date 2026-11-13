package types

import ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"

const (
	bcdSize        = 1
	bcdReplacement = byte(0xFF)
)

// BCD is a packed binary-coded decimal (00-99) in one byte.
type BCD struct{}

// Size returns 1, the encoded width in bytes.
func (BCD) Size() int {
	return bcdSize
}

// ReplacementValue returns a new slice holding 0xFF, the BCD "no data"
// value.
func (BCD) ReplacementValue() []byte {
	return []byte{bcdReplacement}
}

// Decode reads the first payload byte as two decimal digits, tens in the
// high nibble, and returns the number as a uint8 from 0 to 99. 0xFF decodes
// to Valid false. An empty payload or a nibble above 9 fails with
// errors.ErrInvalidPayload. The wire format is documented at
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/types/primitives.md
func (BCD) Decode(payload []byte) (Value, error) {
	if len(payload) < bcdSize {
		return Value{}, ebuserrors.ErrInvalidPayload
	}

	raw := payload[0]
	if raw == bcdReplacement {
		return Value{Valid: false}, nil
	}

	tens := raw >> 4
	ones := raw & 0x0F
	if tens > 9 || ones > 9 {
		return Value{}, ebuserrors.ErrInvalidPayload
	}

	value := uint8(tens*10 + ones)
	return Value{Value: value, Valid: true}, nil
}

// Encode packs an integer from 0 to 99 into one byte. Non-integer inputs and
// values outside that range fail with errors.ErrInvalidPayload.
func (BCD) Encode(value any) ([]byte, error) {
	i, ok := toInt64(value)
	if !ok {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if i < 0 || i > 99 {
		return nil, ebuserrors.ErrInvalidPayload
	}

	tens := byte(i / 10)
	ones := byte(i % 10)
	return []byte{(tens << 4) | ones}, nil
}
