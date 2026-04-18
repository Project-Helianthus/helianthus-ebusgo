package types

import (
	"bytes"
	"testing"
)

func TestCHAR_DecodeUnsigned(t *testing.T) {
	got := CHAR{}.Decode([]byte{0xC8})
	if !got.Valid {
		t.Fatalf("want Valid=true, got %+v", got)
	}
	v, ok := got.Value.(uint8)
	if !ok || v != 200 {
		t.Fatalf("want uint8(200), got %T(%v)", got.Value, got.Value)
	}
}

func TestCHAR_DecodeTruncated(t *testing.T) {
	got := CHAR{}.Decode(nil)
	if got.Err == nil || got.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated_payload, got %+v", got.Err)
	}
}

func TestCHAR_DecodeOverlong(t *testing.T) {
	got := CHAR{}.Decode([]byte{0x01, 0x02})
	if got.Err == nil || got.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload, got %+v", got.Err)
	}
}

func TestCHARSigned_DecodePositive(t *testing.T) {
	got := CHARSigned{}.Decode([]byte{0x7F})
	if !got.Valid {
		t.Fatalf("unexpected invalid")
	}
	if v, _ := got.Value.(int8); v != 127 {
		t.Fatalf("want 127, got %v", got.Value)
	}
}

func TestCHARSigned_DecodeNegative(t *testing.T) {
	got := CHARSigned{}.Decode([]byte{0xFF})
	if !got.Valid {
		t.Fatalf("unexpected invalid for -1")
	}
	if v, _ := got.Value.(int8); v != -1 {
		t.Fatalf("want -1, got %v", got.Value)
	}
}

func TestCHARSigned_EncodeRange(t *testing.T) {
	b, err := CHARSigned{}.Encode(-128)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.Equal(b, []byte{0x80}) {
		t.Fatalf("got %v", b)
	}
	codec := CHARSigned{}
	if _, err := codec.Encode(128); err == nil || err.Code != ErrCodeOutOfRange {
		t.Fatalf("want out_of_range, got %+v", err)
	}
	if _, err := codec.Encode(-129); err == nil || err.Code != ErrCodeOutOfRange {
		t.Fatalf("want out_of_range, got %+v", err)
	}
}

func TestCHARText_DecodePreservesRawAndTrimsDisplay(t *testing.T) {
	// 6-byte slot "AB\x00 Z " -> display "AB" "Z" wise: trailing 0x00 and 0x20 stripped
	raw := []byte{'A', 'B', 0x00, 'Z', 0x20, 0x20}
	got := CHARText{Width: 6}.Decode(raw)
	if !got.Valid {
		t.Fatalf("unexpected invalid, err=%v", got.Err)
	}
	if !bytes.Equal(got.Raw, raw) {
		t.Fatalf("raw mismatch: %v vs %v", got.Raw, raw)
	}
	// Display text lives under Value.
	s, ok := got.Value.(string)
	if !ok {
		t.Fatalf("want string display, got %T", got.Value)
	}
	// Trailing 0x00/0x20 stripped; embedded 0x00 must be escaped, not dropped.
	if s == "" {
		t.Fatalf("display empty; raw %v", raw)
	}
	// Must NOT simply equal "ABZ" by silently dropping the embedded 0x00:
	// the display MUST escape non-printable bytes so consumers can tell.
	if s == "ABZ" {
		t.Fatalf("display dropped the embedded 0x00 silently: %q", s)
	}
}

func TestCHARText_DecodeWrongLength(t *testing.T) {
	c := CHARText{Width: 4}
	if got := c.Decode([]byte{1, 2, 3}); got.Err == nil || got.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated, got %+v", got.Err)
	}
	if got := c.Decode([]byte{1, 2, 3, 4, 5}); got.Err == nil || got.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong, got %+v", got.Err)
	}
}

func TestCHARText_EncodePadsRight(t *testing.T) {
	c := CHARText{Width: 4}
	got, err := c.Encode("Hi")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.Equal(got, []byte{'H', 'i', 0x20, 0x20}) {
		t.Fatalf("got %v", got)
	}
}

func TestCHARText_EncodeCustomPad(t *testing.T) {
	c := CHARText{Width: 3, Pad: 0x00}
	got, err := c.Encode("A")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.Equal(got, []byte{'A', 0x00, 0x00}) {
		t.Fatalf("got %v", got)
	}
}

func TestCHARText_EncodeOverflow(t *testing.T) {
	c := CHARText{Width: 2}
	if _, err := c.Encode("ABC"); err == nil || err.Code != ErrCodeFixedWidthExceeded {
		t.Fatalf("want fixed_width_exceeded, got %+v", err)
	}
}
