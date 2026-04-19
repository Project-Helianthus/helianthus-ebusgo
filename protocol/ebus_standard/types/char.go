package types

import (
	"fmt"
	"strings"
)

// CHAR is the single-octet CHAR primitive defined in 02-l7-types.md §"CHAR".
//
// Signedness, replacement bytes, and fixed-width text semantics are
// catalog-field metadata. The primitive itself decodes one byte as unsigned
// uint8 by default; callers that want signed/text semantics use CHARSigned or
// CHARText.
type CHAR struct{}

// Size returns the fixed width of a CHAR primitive.
func (CHAR) Size() int { return 1 }

// Decode parses a single byte as unsigned uint8.
func (CHAR) Decode(payload []byte) Value {
	if len(payload) == 0 {
		return Value{Err: newDecodeError(ErrCodeTruncatedPayload, "CHAR requires 1 byte")}
	}
	if len(payload) > 1 {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeOverlongPayload, "CHAR consumes exactly 1 byte")}
	}
	return Value{
		Raw:   cloneBytes(payload),
		Value: payload[0],
		Valid: true,
	}
}

// Encode serialises an unsigned byte.
func (CHAR) Encode(value any) ([]byte, *DecodeError) {
	i, ok := toInt64(value)
	if !ok {
		return nil, newDecodeError(ErrCodeInvalidArgument, "CHAR.Encode requires an integer")
	}
	if i < 0 || i > 255 {
		return nil, newDecodeError(ErrCodeOutOfRange, "CHAR value must be in [0,255]")
	}
	return []byte{byte(i)}, nil
}

// CHARSigned is a CHAR field declared as signed int8 per catalog metadata.
type CHARSigned struct{}

// Size returns the fixed width of a signed CHAR primitive.
func (CHARSigned) Size() int { return 1 }

// Decode parses a single byte as two's-complement int8.
func (CHARSigned) Decode(payload []byte) Value {
	if len(payload) == 0 {
		return Value{Err: newDecodeError(ErrCodeTruncatedPayload, "CHARSigned requires 1 byte")}
	}
	if len(payload) > 1 {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeOverlongPayload, "CHARSigned consumes exactly 1 byte")}
	}
	return Value{
		Raw:   cloneBytes(payload),
		Value: int8(payload[0]),
		Valid: true,
	}
}

// Encode serialises a signed int in [-128,127] as one byte.
func (CHARSigned) Encode(value any) ([]byte, *DecodeError) {
	i, ok := toInt64(value)
	if !ok {
		return nil, newDecodeError(ErrCodeInvalidArgument, "CHARSigned.Encode requires an integer")
	}
	if i < -128 || i > 127 {
		return nil, newDecodeError(ErrCodeOutOfRange, "CHARSigned value must be in [-128,127]")
	}
	return []byte{byte(int8(i))}, nil
}

// CHARText decodes a fixed-width CHAR[n] text field. The Width MUST be > 0.
// Pad defaults to 0x20 (ASCII space) when nil; callers override by setting
// Pad to the literal pad byte the catalog field declares (0x00, 0xFF, ...).
type CHARText struct {
	Width int
	Pad   *byte
}

// Size returns the fixed width in bytes.
func (c CHARText) Size() int { return c.Width }

// Decode copies a Width-byte slice into Value.Raw and exposes a display
// string (trailing 0x00 and 0x20 stripped, non-printable bytes escaped).
func (c CHARText) Decode(payload []byte) Value {
	if c.Width <= 0 {
		return Value{Err: newDecodeError(ErrCodeInvalidArgument, "CHARText.Width must be > 0")}
	}
	if len(payload) < c.Width {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeTruncatedPayload, "CHARText requires exactly Width bytes")}
	}
	if len(payload) > c.Width {
		return Value{Raw: cloneBytes(payload), Err: newDecodeError(ErrCodeOverlongPayload, "CHARText consumes exactly Width bytes")}
	}
	raw := cloneBytes(payload)

	// Display: strip trailing pad bytes for display only; raw bytes remain
	// authoritative. Per 02-l7-types.md §CHAR rule 6, default padding is
	// 0x00 / 0x20 when the catalog field does not declare a different pad.
	// When c.Pad is explicitly set (e.g. 0xFF), the catalog field's declared
	// pad byte is an OVERRIDE of the default — only that byte is treated as
	// trailing padding, so genuine trailing 0x00 / 0x20 bytes remain visible
	// in the display string. Non-printable bytes not stripped as padding are
	// escaped as \xHH so consumers can tell they were present.
	end := len(raw)
	for end > 0 {
		b := raw[end-1]
		if c.Pad != nil {
			if b == *c.Pad {
				end--
				continue
			}
			break
		}
		if b == 0x00 || b == 0x20 {
			end--
			continue
		}
		break
	}
	var b strings.Builder
	for _, by := range raw[:end] {
		if by >= 0x20 && by <= 0x7E {
			b.WriteByte(by)
		} else {
			fmt.Fprintf(&b, "\\x%02X", by)
		}
	}
	return Value{
		Raw:   raw,
		Value: b.String(),
		Valid: true,
	}
}

// Encode pads the supplied string/bytes to exactly Width bytes using Pad.
func (c CHARText) Encode(value any) ([]byte, *DecodeError) {
	if c.Width <= 0 {
		return nil, newDecodeError(ErrCodeInvalidArgument, "CHARText.Width must be > 0")
	}
	var data []byte
	switch v := value.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = append([]byte(nil), v...)
	default:
		return nil, newDecodeError(ErrCodeInvalidArgument, "CHARText.Encode requires string or []byte")
	}
	if len(data) > c.Width {
		return nil, newDecodeError(ErrCodeFixedWidthExceeded, "CHARText input longer than Width")
	}
	var pad byte = 0x20
	if c.Pad != nil {
		pad = *c.Pad
	}
	out := make([]byte, c.Width)
	copy(out, data)
	for i := len(data); i < c.Width; i++ {
		out[i] = pad
	}
	return out, nil
}
