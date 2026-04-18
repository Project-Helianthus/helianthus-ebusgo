package types

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
	panic("ebus_standard/types.DATA1c.Decode: not implemented")
}

// Encode serialises a half-unit value; see 02-l7-types.md for accepted ranges.
func (DATA1c) Encode(value any) ([]byte, *DecodeError) {
	panic("ebus_standard/types.DATA1c.Encode: not implemented")
}
