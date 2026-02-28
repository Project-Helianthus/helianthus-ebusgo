package types

import ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"

const bitfieldReplacement = byte(0xFF)

// BITFIELD is a packed bitmask across a fixed number of bytes.
type BITFIELD struct {
	SizeBytes int
}

func (b BITFIELD) Size() int {
	return b.SizeBytes
}

func (b BITFIELD) ReplacementValue() []byte {
	if b.SizeBytes <= 0 {
		return nil
	}
	out := make([]byte, b.SizeBytes)
	for i := range out {
		out[i] = bitfieldReplacement
	}
	return out
}

func (b BITFIELD) Decode(payload []byte) (Value, error) {
	if b.SizeBytes <= 0 || len(payload) < b.SizeBytes {
		return Value{}, ebuserrors.ErrInvalidPayload
	}

	segment := payload[:b.SizeBytes]
	if bitfieldIsReplacement(segment) {
		return Value{Valid: false}, nil
	}

	bits := make([]bool, b.SizeBytes*8)
	for i := 0; i < b.SizeBytes; i++ {
		value := segment[i]
		for bit := 0; bit < 8; bit++ {
			bits[i*8+bit] = (value & (1 << bit)) != 0
		}
	}
	return Value{Value: bits, Valid: true}, nil
}

func (b BITFIELD) Encode(value any) ([]byte, error) {
	if b.SizeBytes <= 0 {
		return nil, ebuserrors.ErrInvalidPayload
	}

	switch v := value.(type) {
	case []bool:
		if len(v) != b.SizeBytes*8 {
			return nil, ebuserrors.ErrInvalidPayload
		}
		out := make([]byte, b.SizeBytes)
		for i, flag := range v {
			if flag {
				out[i/8] |= 1 << uint(i%8)
			}
		}
		if bitfieldIsReplacement(out) {
			return nil, ebuserrors.ErrInvalidPayload
		}
		return out, nil
	case []byte:
		if len(v) != b.SizeBytes {
			return nil, ebuserrors.ErrInvalidPayload
		}
		if bitfieldIsReplacement(v) {
			return nil, ebuserrors.ErrInvalidPayload
		}
		out := append([]byte(nil), v...)
		return out, nil
	case uint64:
		return b.encodeFromUint64(v)
	default:
		i, ok := toInt64(value)
		if !ok || i < 0 {
			return nil, ebuserrors.ErrInvalidPayload
		}
		return b.encodeFromUint64(uint64(i))
	}
}

func (b BITFIELD) encodeFromUint64(value uint64) ([]byte, error) {
	if b.SizeBytes <= 0 {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if b.SizeBytes > 8 {
		return nil, ebuserrors.ErrInvalidPayload
	}

	bitCount := uint(b.SizeBytes * 8)
	if bitCount < 64 {
		max := uint64(1)<<bitCount - 1
		if value > max {
			return nil, ebuserrors.ErrInvalidPayload
		}
	}

	out := make([]byte, b.SizeBytes)
	for i := 0; i < b.SizeBytes; i++ {
		out[i] = byte(value >> (8 * i))
	}
	if bitfieldIsReplacement(out) {
		return nil, ebuserrors.ErrInvalidPayload
	}
	return out, nil
}

func bitfieldIsReplacement(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b != bitfieldReplacement {
			return false
		}
	}
	return true
}
