package types

import (
	"bytes"
	"math"
	"testing"
)

func TestDATA1c_DecodePositive(t *testing.T) {
	cases := []struct {
		in   byte
		want float64
	}{
		{0x00, 0.0},
		{0x01, 0.5},
		{0x02, 1.0},
		{0x80, 64.0},
		{0xC8, 100.0}, // raw 200 / 2 = 100
		{0xFE, 127.0},
	}
	for _, tc := range cases {
		got := DATA1c{}.Decode([]byte{tc.in})
		if !got.Valid {
			t.Fatalf("want Valid=true for 0x%02X, got %+v", tc.in, got)
		}
		if got.Replacement {
			t.Fatalf("0x%02X must not be replacement", tc.in)
		}
		v, ok := got.Value.(float64)
		if !ok {
			t.Fatalf("want float64, got %T", got.Value)
		}
		if math.Abs(v-tc.want) > 1e-9 {
			t.Fatalf("0x%02X: got %v want %v", tc.in, v, tc.want)
		}
	}
}

func TestDATA1c_DecodeReplacement(t *testing.T) {
	got := DATA1c{}.Decode([]byte{0xFF})
	if got.Valid {
		t.Fatalf("replacement must set Valid=false")
	}
	if !got.Replacement {
		t.Fatalf("Replacement must be true for 0xFF")
	}
	if got.Err != nil {
		t.Fatalf("replacement must not produce err: got %+v", got.Err)
	}
	if !bytes.Equal(got.Raw, []byte{0xFF}) {
		t.Fatalf("raw mismatch: %v", got.Raw)
	}
	if got.Value != nil {
		t.Fatalf("value must be nil on replacement, got %v", got.Value)
	}
}

func TestDATA1c_DecodeTruncated(t *testing.T) {
	got := DATA1c{}.Decode(nil)
	if got.Err == nil || got.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated_payload, got %+v", got.Err)
	}
}

func TestDATA1c_DecodeOverlong(t *testing.T) {
	got := DATA1c{}.Decode([]byte{0x10, 0x20})
	if got.Err == nil || got.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload, got %+v", got.Err)
	}
}

func TestDATA1c_EncodeRoundTrip(t *testing.T) {
	cases := []struct {
		in   float64
		want byte
	}{
		{0.0, 0x00},
		{0.5, 0x01},
		{100.0, 0xC8},
		{127.0, 0xFE},
	}
	for _, tc := range cases {
		got, err := DATA1c{}.Encode(tc.in)
		if err != nil {
			t.Fatalf("%v: %v", tc.in, err)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%v: got %v want 0x%02X", tc.in, got, tc.want)
		}
	}
}

func TestDATA1c_EncodeRejectsNonHalfUnit(t *testing.T) {
	_, err := DATA1c{}.Encode(0.3) // does not round-trip to a half-unit
	if err == nil || err.Code != ErrCodeInvalidRoundTrip {
		t.Fatalf("want invalid_round_trip, got %+v", err)
	}
}

func TestDATA1c_EncodeRejectsReplacementByte(t *testing.T) {
	// 127.5 would encode to raw 0xFF which is the replacement sentinel; reject.
	_, err := DATA1c{}.Encode(127.5)
	if err == nil || err.Code != ErrCodeEncodesReplacement {
		t.Fatalf("want encodes_replacement_value, got %+v", err)
	}
}

func TestDATA1c_EncodeOutOfRange(t *testing.T) {
	codec := DATA1c{}
	if _, err := codec.Encode(-0.5); err == nil || err.Code != ErrCodeOutOfRange {
		t.Fatalf("want out_of_range for -0.5, got %+v", err)
	}
	if _, err := codec.Encode(200.0); err == nil || err.Code != ErrCodeOutOfRange {
		t.Fatalf("want out_of_range for 200, got %+v", err)
	}
}
