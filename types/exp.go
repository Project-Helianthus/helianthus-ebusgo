package types

import (
	"encoding/binary"
	"math"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

const (
	expSize        = 4
	expReplacement = uint32(0x7FC00000)
)

// EXP is a 4-byte IEEE754 float32 (little-endian).
type EXP struct{}

// Size returns 4, the encoded width in bytes.
func (EXP) Size() int {
	return expSize
}

// ReplacementValue returns a new slice holding 0x00 0x00 0xC0 0x7F, the
// EXP "no data" value: the float32 quiet NaN 0x7FC00000 in little-endian
// order.
func (EXP) ReplacementValue() []byte {
	return []byte{0x00, 0x00, 0xC0, 0x7F}
}

// Decode reads the first four bytes as a little-endian IEEE 754 float32 and
// returns it as a float32. The replacement value and every other NaN or
// infinity decode to Valid false; a payload shorter than four bytes fails
// with errors.ErrInvalidPayload. The wire format is documented at
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/types/primitives.md
func (EXP) Decode(payload []byte) (Value, error) {
	if len(payload) < expSize {
		return Value{}, ebuserrors.ErrInvalidPayload
	}

	raw := binary.LittleEndian.Uint32(payload)
	if raw == expReplacement {
		return Value{Valid: false}, nil
	}

	value := math.Float32frombits(raw)
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
		return Value{Valid: false}, nil
	}

	return Value{Value: value, Valid: true}, nil
}

// Encode writes a float32 or float64 as a little-endian float32. A float64
// is converted to the nearest float32, so precision can be lost; one that
// overflows float32 fails. NaN, infinities and every other input type fail
// with errors.ErrInvalidPayload.
func (EXP) Encode(value any) ([]byte, error) {
	var f32 float32

	switch v := value.(type) {
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, ebuserrors.ErrInvalidPayload
		}
		f32 = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, ebuserrors.ErrInvalidPayload
		}
		f32 = float32(v)
		if math.IsInf(float64(f32), 0) {
			return nil, ebuserrors.ErrInvalidPayload
		}
	default:
		return nil, ebuserrors.ErrInvalidPayload
	}

	if math.IsNaN(float64(f32)) || math.IsInf(float64(f32), 0) {
		return nil, ebuserrors.ErrInvalidPayload
	}

	raw := math.Float32bits(f32)
	if raw == expReplacement {
		return nil, ebuserrors.ErrInvalidPayload
	}

	buf := make([]byte, expSize)
	binary.LittleEndian.PutUint32(buf, raw)
	return buf, nil
}
