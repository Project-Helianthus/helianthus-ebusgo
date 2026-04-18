package types

import (
	"bytes"
	"testing"
)

func TestBCD_DecodePositive(t *testing.T) {
	cases := []struct {
		in   byte
		want int
	}{
		{0x00, 0},
		{0x09, 9},
		{0x42, 42},
		{0x99, 99},
	}
	for _, tc := range cases {
		got := BCD{}.Decode([]byte{tc.in})
		if !got.Valid {
			t.Fatalf("0x%02X invalid: %+v", tc.in, got)
		}
		v, ok := got.Value.(int)
		if !ok {
			t.Fatalf("want int, got %T", got.Value)
		}
		if v != tc.want {
			t.Fatalf("0x%02X: got %d want %d", tc.in, v, tc.want)
		}
	}
}

func TestBCD_DecodeReplacement(t *testing.T) {
	got := BCD{}.Decode([]byte{0xFF})
	if got.Valid {
		t.Fatalf("replacement must not be Valid")
	}
	if !got.Replacement {
		t.Fatalf("want Replacement=true")
	}
	if got.Err != nil {
		t.Fatalf("replacement must not produce err, got %+v", got.Err)
	}
}

func TestBCD_DecodeInvalidNibble(t *testing.T) {
	// High nibble invalid
	got := BCD{}.Decode([]byte{0xA0})
	if got.Err == nil || got.Err.Code != ErrCodeInvalidNibble {
		t.Fatalf("want invalid_nibble, got %+v", got.Err)
	}
	// Low nibble invalid
	got = BCD{}.Decode([]byte{0x0B})
	if got.Err == nil || got.Err.Code != ErrCodeInvalidNibble {
		t.Fatalf("want invalid_nibble, got %+v", got.Err)
	}
}

func TestBCD_DecodeTruncated(t *testing.T) {
	got := BCD{}.Decode(nil)
	if got.Err == nil || got.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated, got %+v", got.Err)
	}
}

func TestBCD_DecodeOverlong(t *testing.T) {
	got := BCD{}.Decode([]byte{0x01, 0x02})
	if got.Err == nil || got.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong, got %+v", got.Err)
	}
}

func TestBCD_Encode(t *testing.T) {
	got, err := BCD{}.Encode(42)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.Equal(got, []byte{0x42}) {
		t.Fatalf("got %v", got)
	}
	codec := BCD{}
	if _, err := codec.Encode(100); err == nil || err.Code != ErrCodeOutOfRange {
		t.Fatalf("want out_of_range, got %+v", err)
	}
	if _, err := codec.Encode(-1); err == nil || err.Code != ErrCodeOutOfRange {
		t.Fatalf("want out_of_range, got %+v", err)
	}
}

func TestBCDComposite_DecodeAllValid(t *testing.T) {
	c := BCDComposite{Components: []string{"sec", "min", "hour"}}
	got := c.Decode([]byte{0x30, 0x45, 0x12}) // 30 seconds, 45 minutes, 12 hours
	if !got.Valid {
		t.Fatalf("want valid: %+v", got)
	}
	parts, ok := got.Value.([]Value)
	if !ok {
		t.Fatalf("want []Value for composite, got %T", got.Value)
	}
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d", len(parts))
	}
	want := []int{30, 45, 12}
	for i, p := range parts {
		if !p.Valid {
			t.Fatalf("part %d invalid: %+v", i, p)
		}
		if v := p.Value.(int); v != want[i] {
			t.Fatalf("part %d: got %d want %d", i, v, want[i])
		}
	}
}

func TestBCDComposite_DecodeComponentInvalidNibble(t *testing.T) {
	c := BCDComposite{Components: []string{"sec", "min"}}
	got := c.Decode([]byte{0xA0, 0x12}) // invalid first component
	if got.Valid {
		t.Fatalf("want composite invalid")
	}
	if got.Err == nil || got.Err.Code != ErrCodeInvalidNibble {
		t.Fatalf("want invalid_nibble, got %+v", got.Err)
	}
	// Diagnostics should still expose both parts.
	parts, ok := got.Value.([]Value)
	if !ok {
		t.Fatalf("want diagnostic []Value, got %T", got.Value)
	}
	if len(parts) != 2 {
		t.Fatalf("want 2 diag parts, got %d", len(parts))
	}
	if parts[0].Valid {
		t.Fatalf("part 0 must be invalid")
	}
	if !parts[1].Valid {
		t.Fatalf("part 1 must remain valid in diagnostics")
	}
}

func TestBCDComposite_DecodeReplacementPropagates(t *testing.T) {
	c := BCDComposite{Components: []string{"sec", "min"}}
	got := c.Decode([]byte{0x30, 0xFF})
	if got.Valid {
		t.Fatalf("any replacement must make composite invalid")
	}
	if !got.Replacement {
		t.Fatalf("want Replacement=true")
	}
	if got.Err != nil {
		t.Fatalf("replacement-caused invalidity must not set err, got %+v", got.Err)
	}
}

func TestBCDComposite_DecodeTruncated(t *testing.T) {
	c := BCDComposite{Components: []string{"a", "b", "c"}}
	got := c.Decode([]byte{0x01, 0x02})
	if got.Err == nil || got.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated, got %+v", got.Err)
	}
}

func TestBCDComposite_DecodeOverlong(t *testing.T) {
	c := BCDComposite{Components: []string{"a", "b"}}
	got := c.Decode([]byte{0x01, 0x02, 0x03})
	if got.Err == nil || got.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong, got %+v", got.Err)
	}
}

func TestBCDComposite_Encode(t *testing.T) {
	c := BCDComposite{Components: []string{"a", "b"}}
	got, err := c.Encode([]int{12, 34})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.Equal(got, []byte{0x12, 0x34}) {
		t.Fatalf("got %v", got)
	}
	if _, err := c.Encode([]int{1}); err == nil || err.Code != ErrCodeInvalidArgument {
		t.Fatalf("want invalid_argument (arity), got %+v", err)
	}
	if _, err := c.Encode([]int{100, 0}); err == nil || err.Code != ErrCodeOutOfRange {
		t.Fatalf("want out_of_range (component), got %+v", err)
	}
}
