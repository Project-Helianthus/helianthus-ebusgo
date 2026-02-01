package types

import (
	"encoding/binary"
	"math"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

const (
	data2cSize        = 2
	data2cDivisor     = 16.0
	data2cEpsilon     = 1e-9
	data2cReplacement = uint16(0x8000)
	data2cReplacementInt = int64(-32768)
	data2cMinInt      = int64(-32767)
	data2cMaxInt      = int64(32767)
)

var (
	data2cMin = float64(data2cMinInt) / data2cDivisor
	data2cMax = float64(data2cMaxInt) / data2cDivisor
)

// DATA2c (D2C) is a 2-byte signed value with divisor 16.
// Encoding rounds to nearest using math.Round (ties away from zero) and requires exact fit.
type DATA2c struct{}

func (DATA2c) Size() int {
	return data2cSize
}

func (DATA2c) ReplacementValue() []byte {
	return []byte{0x00, 0x80}
}

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
