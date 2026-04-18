package types

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
	panic("ebus_standard/types.CHAR.Decode: not implemented")
}

// Encode serialises an unsigned byte.
func (CHAR) Encode(value any) ([]byte, *DecodeError) {
	panic("ebus_standard/types.CHAR.Encode: not implemented")
}

// CHARSigned is a CHAR field declared as signed int8 per catalog metadata.
type CHARSigned struct{}

// Size returns the fixed width of a signed CHAR primitive.
func (CHARSigned) Size() int { return 1 }

// Decode parses a single byte as two's-complement int8.
func (CHARSigned) Decode(payload []byte) Value {
	panic("ebus_standard/types.CHARSigned.Decode: not implemented")
}

// Encode serialises a signed int in [-128,127] as one byte.
func (CHARSigned) Encode(value any) ([]byte, *DecodeError) {
	panic("ebus_standard/types.CHARSigned.Encode: not implemented")
}

// CHARText decodes a fixed-width CHAR[n] text field. The Width MUST be > 0.
// Pad defaults to 0x20 (ASCII space) when zero.
type CHARText struct {
	Width int
	Pad   byte
}

// Size returns the fixed width in bytes.
func (c CHARText) Size() int { return c.Width }

// Decode copies a Width-byte slice into Value.Raw and exposes a display
// string (trailing 0x00 and 0x20 stripped, non-printable bytes escaped).
func (c CHARText) Decode(payload []byte) Value {
	panic("ebus_standard/types.CHARText.Decode: not implemented")
}

// Encode pads the supplied string/bytes to exactly Width bytes using Pad.
func (c CHARText) Encode(value any) ([]byte, *DecodeError) {
	panic("ebus_standard/types.CHARText.Encode: not implemented")
}
