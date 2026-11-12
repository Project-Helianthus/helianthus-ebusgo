package types

import (
	"bytes"
	"testing"
)

func TestRawPayload_DecodePositive(t *testing.T) {
	r := RawPayload{MinLen: 1, MaxLen: 8}
	in := []byte{0x01, 0x02, 0x03}
	got := r.Decode(in)
	if !got.Valid {
		t.Fatalf("unexpected invalid: %+v", got)
	}
	if !bytes.Equal(got.Raw, in) {
		t.Fatalf("raw mismatch: %v vs %v", got.Raw, in)
	}
	v, ok := got.Value.([]byte)
	if !ok {
		t.Fatalf("want []byte, got %T", got.Value)
	}
	if !bytes.Equal(v, in) {
		t.Fatalf("value mismatch: %v", v)
	}
}

func TestRawPayload_DecodeZeroLengthPermitted(t *testing.T) {
	r := RawPayload{MinLen: 0, MaxLen: 4}
	got := r.Decode(nil)
	if !got.Valid {
		t.Fatalf("zero-length must decode: %+v", got)
	}
	if len(got.Raw) != 0 {
		t.Fatalf("raw must be empty, got %v", got.Raw)
	}
}

func TestRawPayload_DecodeZeroLengthForbidden(t *testing.T) {
	r := RawPayload{MinLen: 1, MaxLen: 4}
	got := r.Decode(nil)
	if got.Err == nil || got.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated_payload, got %+v", got.Err)
	}
}

func TestRawPayload_DecodeOverlong(t *testing.T) {
	r := RawPayload{MinLen: 1, MaxLen: 3}
	got := r.Decode([]byte{1, 2, 3, 4})
	if got.Err == nil || got.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload, got %+v", got.Err)
	}
}

func TestRawPayload_DecodeRawIsDefensiveCopy(t *testing.T) {
	r := RawPayload{MinLen: 1, MaxLen: 4}
	in := []byte{0x10, 0x20}
	got := r.Decode(in)
	if !got.Valid {
		t.Fatalf("unexpected invalid")
	}
	in[0] = 0xFF
	if got.Raw[0] != 0x10 {
		t.Fatalf("Raw must be defensive copy, got %#x", got.Raw[0])
	}
}

func TestRawPayload_EncodePositive(t *testing.T) {
	r := RawPayload{MinLen: 1, MaxLen: 4}
	out, err := r.Encode([]byte{1, 2, 3})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.Equal(out, []byte{1, 2, 3}) {
		t.Fatalf("got %v", out)
	}
}

func TestRawPayload_EncodeTooShort(t *testing.T) {
	r := RawPayload{MinLen: 2, MaxLen: 4}
	if _, err := r.Encode([]byte{1}); err == nil || err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated, got %+v", err)
	}
}

func TestRawPayload_EncodeTooLong(t *testing.T) {
	r := RawPayload{MinLen: 1, MaxLen: 2}
	if _, err := r.Encode([]byte{1, 2, 3}); err == nil || err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong, got %+v", err)
	}
}

// Regression for Codex r3106271525: MaxLen=0 must enforce a strict
// zero-length cap (payload must be exactly zero bytes), not be treated as
// "unbounded".
func TestRawPayload_DecodeMaxLenZeroRejectsNonEmpty(t *testing.T) {
	r := RawPayload{MinLen: 0, MaxLen: 0}
	got := r.Decode([]byte{0x01})
	if got.Err == nil || got.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload for non-empty input against MaxLen=0, got %+v", got.Err)
	}
	if got.Valid {
		t.Fatalf("value must be invalid when Err is set")
	}
	// Zero-length payload against MaxLen=0 must still decode.
	ok := r.Decode(nil)
	if !ok.Valid || ok.Err != nil {
		t.Fatalf("zero-length must decode against MaxLen=0, got %+v", ok)
	}
}

func TestRawPayload_EncodeMaxLenZeroRejectsNonEmpty(t *testing.T) {
	r := RawPayload{MinLen: 0, MaxLen: 0}
	if _, err := r.Encode([]byte{0x01}); err == nil || err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload for non-empty encode against MaxLen=0, got %+v", err)
	}
	// Zero-length encode against MaxLen=0 must succeed.
	out, err := r.Encode([]byte{})
	if err != nil {
		t.Fatalf("zero-length encode against MaxLen=0 must succeed, got %+v", err)
	}
	if len(out) != 0 {
		t.Fatalf("want empty output, got %v", out)
	}
}

func TestRawPayload_EncodeWrongType(t *testing.T) {
	r := RawPayload{MinLen: 0, MaxLen: 4}
	if _, err := r.Encode(42); err == nil || err.Code != ErrCodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %+v", err)
	}
}
