package types

// RawPayload decodes a variable-length raw byte slice as defined in
// 02-l7-types.md §"Variable-Length Raw Payload".
//
// The catalog branch supplies MinLen and MaxLen bounds. Zero-length is
// permitted only when MinLen == 0.
type RawPayload struct {
	MinLen int
	MaxLen int
}

// Decode copies payload[:len(payload)] into Raw after enforcing bounds.
func (r RawPayload) Decode(payload []byte) Value {
	panic("ebus_standard/types.RawPayload.Decode: not implemented")
}

// Encode validates length bounds and returns a defensive copy.
func (r RawPayload) Encode(value any) ([]byte, *DecodeError) {
	panic("ebus_standard/types.RawPayload.Encode: not implemented")
}
