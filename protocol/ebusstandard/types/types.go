// Package types implements the L7 primitive types for the cross-vendor
// ebusstandard namespace.
//
// This package is namespace-isolated: it MUST NOT depend on Vaillant 0xB5
// provider logic, and the Vaillant types package (github.com/Project-Helianthus/
// helianthus-ebusgo/types) MUST NOT depend on this one.
//
// The normative specification for every decode/encode rule implemented here
// lives in helianthus-docs-ebus:
//
//	architecture/ebus_standard/02-l7-types.md
//
// Canonical plan SHA-256:
//
//	9e0a29bb76d99f551904b05749e322aafd3972621858aa6d1acbe49b9ef37305
package types

// ErrorCode enumerates structured decoder error codes defined in
// 02-l7-types.md. These values surface to consumers via Value.Err.
type ErrorCode string

// DecodeError.Code carries one of these codes; DecodeError.Message carries
// free-text detail. For the decode and encode rules see
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/architecture/ebus_standard/02-l7-types.md
const (
	// ErrCodeTruncatedPayload means a payload has fewer bytes than its length
	// contract requires (fixed width, minimum length or declared length). It
	// is returned by both Decode and Encode paths.
	ErrCodeTruncatedPayload ErrorCode = "truncated_payload"
	// ErrCodeOverlongPayload means a payload has more bytes than its length
	// contract allows (fixed width, maximum length or declared length); the
	// extra bytes are not ignored. It is returned by both Decode and Encode
	// paths.
	ErrCodeOverlongPayload ErrorCode = "overlong_payload"
	// ErrCodeOutOfRange means Encode received a value outside the primitive's
	// range, or a non-finite number.
	ErrCodeOutOfRange ErrorCode = "out_of_range"
	// ErrCodeInvalidNibble means a BCD byte has a nibble greater than 9.
	ErrCodeInvalidNibble ErrorCode = "invalid_nibble"
	// ErrCodeAmbiguousSelector means more than one selector branch matches the
	// same inputs.
	ErrCodeAmbiguousSelector ErrorCode = "ambiguous_selector_branch"
	// ErrCodeUnknownSelector means no selector branch matches the inputs.
	ErrCodeUnknownSelector ErrorCode = "unknown_selector_branch"
	// ErrCodeInvalidRoundTrip means Encode received a value that the wire format
	// cannot represent exactly, so decoding would not return it.
	ErrCodeInvalidRoundTrip ErrorCode = "invalid_round_trip"
	// ErrCodeEncodesReplacement means Encode would produce the primitive's
	// replacement sentinel, which Decode reports with Replacement set and
	// Valid false.
	ErrCodeEncodesReplacement ErrorCode = "encodes_replacement_value"
	// ErrCodeFixedWidthExceeded means Encode received text longer than the
	// primitive's fixed width.
	ErrCodeFixedWidthExceeded ErrorCode = "fixed_width_exceeded"
	// ErrCodeInvalidArgument means a Go value has the wrong type or shape for
	// the primitive, or a primitive parameter (such as a text width) is invalid;
	// returned by Encode and, for invalid parameters, by Decode.
	ErrCodeInvalidArgument ErrorCode = "invalid_argument"
)

// DecodeError is the structured error type for 02-l7-types.md decode failures.
type DecodeError struct {
	Code    ErrorCode
	Message string
}

// Error returns the code, followed by ": " and Message when Message is set.
// It is safe on a nil receiver.
func (e *DecodeError) Error() string {
	if e == nil {
		return "<nil decode error>"
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// newDecodeError returns a pointer to a DecodeError. Using a pointer makes
// errors.Is / errors.As chains straightforward for consumers that later wrap
// this error.
func newDecodeError(code ErrorCode, message string) *DecodeError {
	return &DecodeError{Code: code, Message: message}
}

// Value is the decode output for every ebusstandard primitive.
//
// Contract (02-l7-types.md §"Decode Output Validity"):
//
//   - Raw always holds the exact source bytes in wire order, even when
//     Valid=false.
//   - Valid is true only when the decode produced a usable Value.
//   - Replacement is true when the payload matched a replacement sentinel
//     (Valid is also false in that case). Replacement and Err are mutually
//     exclusive: replacement sentinels never set Err.
//   - Value is zero / unset when Valid=false.
//   - Err is non-nil when the decode failed for a syntax/range/length/selector
//     reason rather than because of a replacement sentinel.
type Value struct {
	Raw         []byte
	Value       any
	Valid       bool
	Replacement bool
	Err         *DecodeError
}

// IsError reports whether Value holds a structured decode error.
func (v Value) IsError() bool { return v.Err != nil }

// Codec is the encode/decode contract for an L7 primitive.
type Codec interface {
	// Decode parses payload. It MUST always populate Raw when it consumes
	// exactly one primitive's worth of bytes. Payload slices MUST be treated
	// as read-only; decoders make defensive copies when retaining bytes.
	Decode(payload []byte) Value

	// Encode serialises value into the primitive's wire format. The returned
	// byte slice is owned by the caller.
	Encode(value any) ([]byte, *DecodeError)
}

// cloneBytes returns a defensive copy of b. Decoders store the copy in
// Value.Raw so that consumers mutating the slice cannot corrupt cached decode
// output.
func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
