package types

import (
	"encoding/binary"
	"math"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

const (
	data2cSize           = 2
	data2cDivisor        = 16.0
	data2cEpsilon        = 1e-9
	data2cReplacement    = uint16(0x8000)
	data2cReplacementInt = int64(-32768)
	data2cMinInt         = int64(-32767)
	data2cMaxInt         = int64(32767)
)

var (
	data2cMin = float64(data2cMinInt) / data2cDivisor
	data2cMax = float64(data2cMaxInt) / data2cDivisor
)

// DATA2c (D2C) is a 2-byte signed value with divisor 16.
// Encoding rounds to nearest using math.Round (ties away from zero) and requires exact fit.
type DATA2c struct{}

// Size returns 2, the encoded width in bytes.
func (DATA2c) Size() int {
	return data2cSize
}

// ReplacementValue returns a new slice holding 0x00 0x80, the DATA2c
// "no data" value 0x8000 in little-endian order.
func (DATA2c) ReplacementValue() []byte {
	return []byte{0x00, 0x80}
}

// Decode reads the first two bytes as a little-endian int16 and returns it
// divided by 16 as a float64. 0x8000 decodes to Valid false; a payload
// shorter than two bytes fails with errors.ErrInvalidPayload. The wire
// format is documented at
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/types/primitives.md
func (DATA2c) Decode(payload []byte) (Value, error) {
	if len(payload) < data2cSize {
		return Value{}, ebuserrors.ErrInvalidPayload
	}

	raw := binary.LittleEndian.Uint16(payload)
	if raw == data2cReplacement {
		return Value{Valid: false}, nil
	}

	value := float64(int16(raw)) / data2cDivisor
	return Value{Value: value, Valid: true}, nil
}

// Encode multiplies an integer or float by 16 and writes the result as a
// little-endian int16. The value must be finite, lie from -32767/16 to
// 32767/16 and be a multiple of 1/16 within a tolerance of 1e-9 after
// scaling; anything else fails with errors.ErrInvalidPayload rather than
// being rounded.
func (DATA2c) Encode(value any) ([]byte, error) {
	f, ok := toFloat64(value)
	if !ok {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if f < data2cMin || f > data2cMax {
		return nil, ebuserrors.ErrInvalidPayload
	}

	scaled := f * data2cDivisor
	rounded := math.Round(scaled)
	if math.Abs(scaled-rounded) > data2cEpsilon {
		return nil, ebuserrors.ErrInvalidPayload
	}

	intValue := int64(rounded)
	if intValue < data2cMinInt || intValue > data2cMaxInt {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if intValue == data2cReplacementInt {
		return nil, ebuserrors.ErrInvalidPayload
	}

	u := uint16(int16(intValue))
	return []byte{byte(u), byte(u >> 8)}, nil
}
