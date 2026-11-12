// Package types holds the codecs for the fixed-width eBUS data types (BCD,
// DATA1b, DATA2b, DATA2c, WORD, EXP and BITFIELD) and a helper that decodes
// a sequence of named fields from one payload. It converts between payload
// bytes and Go values only; it does not assign names, units or meaning to a
// register.
//
// Every codec implements DataType. Multi-byte types are little-endian.
// Scaled types keep their native scale: DATA2b divides by 256 and DATA2c by
// 16, and their encoders accept only values that fit the scale exactly
// after rounding to nearest (ties away from zero).
//
// Decode reads the first Size bytes and ignores any bytes after them; a
// shorter payload or a malformed value fails with errors.ErrInvalidPayload
// from this module. The type's replacement value (and, for EXP, any NaN or
// infinity) is not an error: it decodes to a Value with Valid false and no
// Value, so callers can tell "no data" from a bad frame.
//
// Encode accepts the concrete types int, int8, int16, int32, int64,
// uint, uint8, uint16, uint32, uint64 for the integer codecs. DATA2b and
// DATA2c also accept float32 and float64; EXP accepts float32 and float64.
// BITFIELD accepts []bool, []byte or a non-negative value of one of those
// integer types, subject to the encoded width and replacement-value restrictions.
// Numeric inputs require SizeBytes <= 8; wider fields require []bool or []byte.
// For BITFIELD, uint inputs above MaxInt64 are rejected even when they fit
// in eight bytes; uint64 inputs can use the full unsigned range subject to
// those restrictions. Defined numeric types and uintptr are not accepted.
// Other inputs and out-of-range input fail with errors.ErrInvalidPayload, and Encode never
// emits the replacement value; use ReplacementValue to send "no data"
// explicitly. ReplacementValue returns a new slice on each call.
//
// DecodeFields decodes fields back to back from the start of the payload
// and fails on the first missing type, non-positive size, short payload or
// decode error, returning no partial map. TotalSize skips fields without a
// type or with a non-positive size.
//
// The codecs are stateless values and safe for concurrent use. The types of
// the standard service catalog live in protocol/ebusstandard/types; this
// package does not import it.
package types
