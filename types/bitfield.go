package types

import ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"

const bitfieldReplacement = byte(0xFF)

// BITFIELD is a packed bitmask across a fixed number of bytes.
type BITFIELD struct {
	SizeBytes int
}

// Size returns SizeBytes unchecked; a value that is not positive makes
// Decode and Encode fail.
func (b BITFIELD) Size() int {
	return b.SizeBytes
}

// ReplacementValue returns a new slice of SizeBytes bytes of 0xFF, the
// "no data" value, or nil when SizeBytes is not positive.
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

// Decode reads the first SizeBytes bytes and returns a []bool of
// SizeBytes*8 flags, where index i is bit i%8 (least significant first) of
// byte i/8. All bytes 0xFF decode to Valid false. A SizeBytes that is not
// positive or a shorter payload fails with errors.ErrInvalidPayload. The
// layout is documented at
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/types/composite.md
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

// Encode accepts a []bool of exactly SizeBytes*8 flags in Decode's order, a
// []byte of exactly SizeBytes bytes (copied), or a non-negative integer
// packed little-endian, which needs SizeBytes of at most 8 and a value that
// fits. A uint is accepted only up to the largest int64 value (it goes through
// the signed helper), while a uint64 is accepted over its full range. It fails with errors.ErrInvalidPayload for other inputs, for a
// SizeBytes that is not positive and for a result that would be all 0xFF.
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
