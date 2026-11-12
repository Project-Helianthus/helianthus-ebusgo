package types

import (
	"bytes"
	"testing"
)

func TestBYTE_DecodePositive(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want uint8
	}{
		{"zero", []byte{0x00}, 0},
		{"mid", []byte{0x7F}, 127},
		{"high", []byte{0xFF}, 255},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BYTE{}.Decode(tc.in)
			if !got.Valid {
				t.Fatalf("want Valid=true, got %+v", got)
			}
			if got.Replacement {
				t.Fatalf("BYTE primitive has no replacement sentinel; got Replacement=true")
			}
			if got.Err != nil {
				t.Fatalf("unexpected err: %v", got.Err)
			}
			if !bytes.Equal(got.Raw, tc.in) {
				t.Fatalf("raw mismatch: got %v want %v", got.Raw, tc.in)
			}
			v, ok := got.Value.(uint8)
			if !ok {
				t.Fatalf("want uint8, got %T (%v)", got.Value, got.Value)
			}
			if v != tc.want {
				t.Fatalf("value mismatch: got %d want %d", v, tc.want)
			}
		})
	}
}

func TestBYTE_DecodeTruncated(t *testing.T) {
	got := BYTE{}.Decode(nil)
	if got.Valid {
		t.Fatalf("want Valid=false on truncated input")
	}
	if got.Replacement {
		t.Fatalf("truncated must not be replacement")
	}
	if got.Err == nil || got.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated_payload, got %+v", got.Err)
	}
}

func TestBYTE_DecodeOverlong(t *testing.T) {
	got := BYTE{}.Decode([]byte{0x10, 0x20})
	if got.Valid {
		t.Fatalf("want Valid=false on overlong input")
	}
	if got.Err == nil || got.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload, got %+v", got.Err)
	}
}

func TestBYTE_EncodePositive(t *testing.T) {
	cases := []struct {
		in   any
		want []byte
	}{
		{0, []byte{0x00}},
		{255, []byte{0xFF}},
		{uint8(42), []byte{0x2A}},
	}
	for _, tc := range cases {
		got, err := BYTE{}.Encode(tc.in)
		if err != nil {
			t.Fatalf("unexpected err for %v: %v", tc.in, err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Fatalf("encode %v: got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestBYTE_EncodeOutOfRange(t *testing.T) {
	for _, v := range []int{-1, 256, 1000} {
		_, err := BYTE{}.Encode(v)
		if err == nil || err.Code != ErrCodeOutOfRange {
			t.Fatalf("want out_of_range for %d, got %+v", v, err)
		}
	}
}

func TestBYTE_EncodeWrongType(t *testing.T) {
	_, err := BYTE{}.Encode("hello")
	if err == nil || err.Code != ErrCodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %+v", err)
	}
}

func TestBYTE_RawIsDefensiveCopy(t *testing.T) {
	in := []byte{0x42}
	got := BYTE{}.Decode(in)
	if !got.Valid {
		t.Fatalf("unexpected invalid")
	}
	in[0] = 0xAA
	if got.Raw[0] != 0x42 {
		t.Fatalf("Raw must be a defensive copy (got %#x)", got.Raw[0])
	}
}
