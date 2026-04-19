package types

// RawPayload decodes a variable-length raw byte slice as defined in
// 02-l7-types.md §"Variable-Length Raw Payload".
//
// The catalog branch supplies MinLen and MaxLen bounds. Both bounds are
// always enforced; MaxLen == 0 is a legitimate strict zero-length cap
// (payload must be exactly zero bytes). Zero-length input is permitted
// only when MinLen == 0.
type RawPayload struct {
	MinLen int
	MaxLen int
}

// Decode copies payload[:len(payload)] into Raw after enforcing bounds.
func (r RawPayload) Decode(payload []byte) Value {
	n := len(payload)
	if n < r.MinLen {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeTruncatedPayload, "raw payload shorter than MinLen")}
	}
	if n > r.MaxLen {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeOverlongPayload, "raw payload longer than MaxLen")}
	}
	raw := cloneBytes(payload)
	// Value carries a separate defensive copy so that consumers mutating it
	// cannot disturb Value.Raw.
	var val []byte
	if raw != nil {
		val = append([]byte(nil), raw...)
	}
	return Value{
		Raw:   raw,
		Value: val,
		Valid: true,
	}
}

// Encode validates length bounds and returns a defensive copy.
func (r RawPayload) Encode(value any) ([]byte, *DecodeError) {
	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return nil, newDecodeError(ErrCodeInvalidArgument, "RawPayload.Encode requires []byte or string")
	}
	if len(data) < r.MinLen {
		return nil, newDecodeError(ErrCodeTruncatedPayload, "raw payload shorter than MinLen")
	}
	if len(data) > r.MaxLen {
		return nil, newDecodeError(ErrCodeOverlongPayload, "raw payload longer than MaxLen")
	}
	return cloneBytes(data), nil
}
