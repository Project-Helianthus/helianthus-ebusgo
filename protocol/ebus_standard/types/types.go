// Package types implements the L7 primitive types for the cross-vendor
// ebus_standard namespace.
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

const (
	ErrCodeTruncatedPayload   ErrorCode = "truncated_payload"
	ErrCodeOverlongPayload    ErrorCode = "overlong_payload"
	ErrCodeOutOfRange         ErrorCode = "out_of_range"
	ErrCodeInvalidNibble      ErrorCode = "invalid_nibble"
	ErrCodeAmbiguousSelector  ErrorCode = "ambiguous_selector_branch"
	ErrCodeUnknownSelector    ErrorCode = "unknown_selector_branch"
	ErrCodeInvalidRoundTrip   ErrorCode = "invalid_round_trip"
	ErrCodeEncodesReplacement ErrorCode = "encodes_replacement_value"
	ErrCodeFixedWidthExceeded ErrorCode = "fixed_width_exceeded"
	ErrCodeInvalidArgument    ErrorCode = "invalid_argument"
)

// DecodeError is the structured error type for 02-l7-types.md decode failures.
type DecodeError struct {
	Code    ErrorCode
	Message string
}

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

// Value is the decode output for every ebus_standard primitive.
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
