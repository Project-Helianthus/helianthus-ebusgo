package types

import ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"

const (
	bcdSize        = 1
	bcdReplacement = byte(0xFF)
)

// BCD is a packed binary-coded decimal (00-99) in one byte.
type BCD struct{}

func (BCD) Size() int {
	return bcdSize
}

func (BCD) ReplacementValue() []byte {
	return []byte{bcdReplacement}
}

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
