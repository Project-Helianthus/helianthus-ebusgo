package types

// BCD is the single-byte packed BCD primitive, 02-l7-types.md §"Composite BCD"
// single-byte rules. 0xFF is the primitive replacement sentinel.
type BCD struct{}

// Size returns the fixed width.
func (BCD) Size() int { return 1 }

// Decode parses one BCD byte; invalid nibbles surface InvalidNibble.
func (BCD) Decode(payload []byte) Value {
	if len(payload) == 0 {
		return Value{Err: newDecodeError(ErrCodeTruncatedPayload, "BCD requires 1 byte")}
	}
	if len(payload) > 1 {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeOverlongPayload, "BCD consumes exactly 1 byte")}
	}
	return decodeSingleBCD(payload[0], cloneBytes(payload))
}

// decodeSingleBCD decodes exactly one BCD byte with the supplied Raw slice.
// Shared between BCD and BCDComposite.
func decodeSingleBCD(b byte, raw []byte) Value {
	if b == 0xFF {
		return Value{Raw: raw, Replacement: true}
	}
	tens := b >> 4
	ones := b & 0x0F
	if tens > 9 || ones > 9 {
		return Value{Raw: raw, Err: newDecodeError(ErrCodeInvalidNibble, "BCD nibble greater than 9")}
	}
	return Value{
		Raw:   raw,
		Value: int(tens)*10 + int(ones),
		Valid: true,
	}
}

// Encode serialises an integer in [0,99].
func (BCD) Encode(value any) ([]byte, *DecodeError) {
	i, ok := toInt64(value)
	if !ok {
		return nil, newDecodeError(ErrCodeInvalidArgument, "BCD.Encode requires an integer")
	}
	if i < 0 || i > 99 {
		return nil, newDecodeError(ErrCodeOutOfRange, "BCD value must be in [0,99]")
	}
	return []byte{byte(i/10)<<4 | byte(i%10)}, nil
}

// BCDComposite is an ordered sequence of BCD bytes that decode independently.
type BCDComposite struct {
	Components []string // field-level component labels (informational)
}

// Size returns the total byte width.
func (c BCDComposite) Size() int { return len(c.Components) }

// Decode validates each component; any invalid or replacement component
// prevents emitting an aggregate value.
func (c BCDComposite) Decode(payload []byte) Value {
	width := len(c.Components)
	if len(payload) < width {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeTruncatedPayload, "BCDComposite payload shorter than Components")}
	}
	if len(payload) > width {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeOverlongPayload, "BCDComposite payload longer than Components")}
	}
	raw := cloneBytes(payload)
	parts := make([]Value, width)
	var (
		firstErr       *DecodeError
		anyReplacement bool
		anyInvalid     bool
	)
	for i := 0; i < width; i++ {
		partRaw := []byte{raw[i]}
		parts[i] = decodeSingleBCD(raw[i], partRaw)
		if parts[i].Err != nil {
			anyInvalid = true
			if firstErr == nil {
				firstErr = parts[i].Err
			}
		}
		if parts[i].Replacement {
			anyReplacement = true
		}
	}
	out := Value{Raw: raw, Value: parts}
	if anyInvalid {
		out.Err = firstErr
		return out
	}
	if anyReplacement {
		out.Replacement = true
		return out
	}
	out.Valid = true
	return out
}

// Encode serialises a []int ordered per Components.
func (c BCDComposite) Encode(value any) ([]byte, *DecodeError) {
	ints, ok := value.([]int)
	if !ok {
		return nil, newDecodeError(ErrCodeInvalidArgument, "BCDComposite.Encode requires []int")
	}
	if len(ints) != len(c.Components) {
		return nil, newDecodeError(ErrCodeInvalidArgument, "BCDComposite.Encode arity does not match Components")
	}
	out := make([]byte, len(ints))
	for i, v := range ints {
		if v < 0 || v > 99 {
			return nil, newDecodeError(ErrCodeOutOfRange, "BCDComposite component out of [0,99]")
		}
		out[i] = byte(v/10)<<4 | byte(v%10)
	}
	return out, nil
}
