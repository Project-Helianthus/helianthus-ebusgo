package types

import (
	"encoding/binary"
	"math"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

const (
	data2bSize           = 2
	data2bDivisor        = 256.0
	data2bEpsilon        = 1e-9
	data2bReplacement    = uint16(0x8000)
	data2bReplacementInt = int64(-32768)
	data2bMinInt         = int64(-32767)
	data2bMaxInt         = int64(32767)
)

var (
	data2bMin = float64(data2bMinInt) / data2bDivisor
	data2bMax = float64(data2bMaxInt) / data2bDivisor
)

// DATA2b (D2B) is a 2-byte signed value with divisor 256.
// Encoding rounds to nearest using math.Round (ties away from zero) and requires exact fit.
type DATA2b struct{}

func (DATA2b) Size() int {
	return data2bSize
}

func (DATA2b) ReplacementValue() []byte {
	return []byte{0x00, 0x80}
}

func (DATA2b) Decode(payload []byte) (Value, error) {
	if len(payload) < data2bSize {
		return Value{}, ebuserrors.ErrInvalidPayload
	}

	raw := binary.LittleEndian.Uint16(payload)
	if raw == data2bReplacement {
		return Value{Valid: false}, nil
	}

	value := float64(int16(raw)) / data2bDivisor
	return Value{Value: value, Valid: true}, nil
}

func (DATA2b) Encode(value any) ([]byte, error) {
	f, ok := toFloat64(value)
	if !ok {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if f < data2bMin || f > data2bMax {
		return nil, ebuserrors.ErrInvalidPayload
	}

	scaled := f * data2bDivisor
	rounded := math.Round(scaled)
	if math.Abs(scaled-rounded) > data2bEpsilon {
		return nil, ebuserrors.ErrInvalidPayload
	}

	intValue := int64(rounded)
	if intValue < data2bMinInt || intValue > data2bMaxInt {
		return nil, ebuserrors.ErrInvalidPayload
	}
	if intValue == data2bReplacementInt {
		return nil, ebuserrors.ErrInvalidPayload
	}

	u := uint16(int16(intValue))
	return []byte{byte(u), byte(u >> 8)}, nil
}
