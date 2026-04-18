package types

// BCD is the single-byte packed BCD primitive, 02-l7-types.md §"Composite BCD"
// single-byte rules. 0xFF is the primitive replacement sentinel.
type BCD struct{}

// Size returns the fixed width.
func (BCD) Size() int { return 1 }

// Decode parses one BCD byte; invalid nibbles surface InvalidNibble.
func (BCD) Decode(payload []byte) Value {
	panic("ebus_standard/types.BCD.Decode: not implemented")
}

// Encode serialises an integer in [0,99].
func (BCD) Encode(value any) ([]byte, *DecodeError) {
	panic("ebus_standard/types.BCD.Encode: not implemented")
}

// BCDComposite is an ordered sequence of BCD bytes that decode independently.
// Components is informational and intended for composite date/time or
// base-100-chunk counters (see 02-l7-types.md §"Composite BCD" composite
// rules).
type BCDComposite struct {
	Components []string // field-level component labels (informational)
}

// Size returns the total byte width.
func (c BCDComposite) Size() int { return len(c.Components) }

// Decode validates each component; any invalid or replacement component
// prevents emitting an aggregate value. The per-component diagnostics live in
// the returned Value under the Value.Value field as a []Value.
func (c BCDComposite) Decode(payload []byte) Value {
	panic("ebus_standard/types.BCDComposite.Decode: not implemented")
}

// Encode serialises a []int ordered per Components.
func (c BCDComposite) Encode(value any) ([]byte, *DecodeError) {
	panic("ebus_standard/types.BCDComposite.Encode: not implemented")
}
