package types

import "math"

// DATA1c is the unsigned 0.5-resolution primitive with 0xFF replacement
// sentinel, defined in 02-l7-types.md §"DATA1c".
//
// DATA1c is NOT two's-complement signed. Raw values 0x80..0xFE are positive
// half-unit values.
type DATA1c struct{}

// Size returns the fixed width.
func (DATA1c) Size() int { return 1 }

// Decode parses one byte; 0xFF is the replacement sentinel.
func (DATA1c) Decode(payload []byte) Value {
	if len(payload) == 0 {
		return Value{Err: newDecodeError(ErrCodeTruncatedPayload, "DATA1c requires 1 byte")}
	}
	if len(payload) > 1 {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeOverlongPayload, "DATA1c consumes exactly 1 byte")}
	}
	raw := payload[0]
	if raw == 0xFF {
		return Value{
			Raw:         cloneBytes(payload),
			Replacement: true,
		}
	}
	return Value{
		Raw:   cloneBytes(payload),
		Value: float64(raw) / 2.0,
		Valid: true,
	}
}

// Encode serialises a half-unit value; see 02-l7-types.md for accepted ranges.
func (DATA1c) Encode(value any) ([]byte, *DecodeError) {
	f, ok := toFloat64(value)
	if !ok {
		return nil, newDecodeError(ErrCodeInvalidArgument, "DATA1c.Encode requires a numeric value")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, newDecodeError(ErrCodeOutOfRange, "DATA1c value must be finite (NaN/Inf rejected)")
	}
	if f < 0 {
		return nil, newDecodeError(ErrCodeOutOfRange, "DATA1c value must be non-negative")
	}
	// Maximum representable non-replacement physical value is 0xFE/2 = 127.
	if f > 127.0 {
		// 127.5 would encode to 0xFF (replacement) — catch that specifically,
		// everything strictly above is out of range.
		if math.Abs(f-127.5) < 1e-9 {
			return nil, newDecodeError(ErrCodeEncodesReplacement, "DATA1c 127.5 would encode to 0xFF replacement")
		}
		return nil, newDecodeError(ErrCodeOutOfRange, "DATA1c value must be <= 127 (or exactly 127.5 rejected as replacement)")
	}
	// Must round-trip exactly to a half-unit.
	raw := f * 2.0
	rounded := math.Round(raw)
	if math.Abs(raw-rounded) > 1e-9 {
		return nil, newDecodeError(ErrCodeInvalidRoundTrip, "DATA1c value does not align to a 0.5 half-unit")
	}
	if rounded == 0xFF {
		return nil, newDecodeError(ErrCodeEncodesReplacement, "DATA1c encoded byte would be 0xFF replacement")
	}
	return []byte{byte(rounded)}, nil
}
