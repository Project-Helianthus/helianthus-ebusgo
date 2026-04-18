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
	panic("ebus_standard/types.BYTE.Decode: not implemented")
}

// Encode serialises an integer in [0,255] as a single byte.
func (BYTE) Encode(value any) ([]byte, *DecodeError) {
	panic("ebus_standard/types.BYTE.Encode: not implemented")
}
