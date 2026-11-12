package types

// BYTE is the single-octet unsigned primitive defined in
// 02-l7-types.md §"BYTE".
//
// The primitive itself has no replacement sentinel. Fields that want a
// replacement byte MUST declare it at the catalog-field layer.
type BYTE struct{}

// Size returns the fixed width of a BYTE primitive.
func (BYTE) Size() int { return 1 }

// Decode parses a single unsigned byte.
func (BYTE) Decode(payload []byte) Value {
	if len(payload) == 0 {
		return Value{Raw: nil, Err: newDecodeError(ErrCodeTruncatedPayload, "BYTE requires 1 byte")}
	}
	if len(payload) > 1 {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeOverlongPayload, "BYTE consumes exactly 1 byte")}
	}
	return Value{
		Raw:   cloneBytes(payload),
		Value: payload[0],
		Valid: true,
	}
}

// Encode serialises an integer in [0,255] as a single byte.
func (BYTE) Encode(value any) ([]byte, *DecodeError) {
	i, ok := toInt64(value)
	if !ok {
		return nil, newDecodeError(ErrCodeInvalidArgument, "BYTE.Encode requires an integer")
	}
	if i < 0 || i > 255 {
		return nil, newDecodeError(ErrCodeOutOfRange, "BYTE value must be in [0,255]")
	}
	return []byte{byte(i)}, nil
}
