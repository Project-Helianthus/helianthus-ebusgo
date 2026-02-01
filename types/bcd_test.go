package types_test

import (
	"testing"

	"github.com/d3vi1/helianthus-ebusgo/types"
)

func TestBCD_Decode(t *testing.T) {
	t.Parallel()

	dt := types.BCD{}
	cases := []struct {
		name    string
		payload []byte
		want    types.Value
		wantErr bool
	}{
		{
			name:    "Value",
			payload: []byte{0x42},
			want:    types.Value{Value: uint8(42), Valid: true},
		},
		{
			name:    "SingleDigit",
			payload: []byte{0x09},
			want:    types.Value{Value: uint8(9), Valid: true},
		},
		{
			name:    "Replacement",
			payload: []byte{0xFF},
			want:    types.Value{Valid: false},
		},
		{
			name:    "InvalidOnes",
			payload: []byte{0x1A},
			wantErr: true,
		},
		{
			name:    "InvalidTens",
			payload: []byte{0xA1},
			wantErr: true,
		},
		{
			name:    "Short",
			payload: []byte{},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := dt.Decode(tc.payload)
			if tc.wantErr {
				assertInvalidPayload(t, err)
				return
			}
			if err != nil {
				t.Fatalf("Decode error = %v", err)
			}
			assertValue(t, got, tc.want)
		})
	}
}

func TestBCD_Encode(t *testing.T) {
	t.Parallel()

	dt := types.BCD{}
	cases := []struct {
		name    string
		value   any
		want    []byte
		wantErr bool
	}{
		{
			name:  "Value",
			value: int(42),
			want:  []byte{0x42},
		},
		{
			name:  "SingleDigit",
			value: uint(9),
			want:  []byte{0x09},
		},
		{
			name:  "Max",
			value: int(99),
			want:  []byte{0x99},
		},
		{
			name:    "TooHigh",
			value:   int(100),
			wantErr: true,
		},
		{
			name:    "Negative",
			value:   int(-1),
			wantErr: true,
		},
		{
			name:    "WrongType",
			value:   1.2,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := dt.Encode(tc.value)
			if tc.wantErr {
				assertInvalidPayload(t, err)
				return
			}
			if err != nil {
				t.Fatalf("Encode error = %v", err)
			}
			if string(got) != string(tc.want) {
				t.Fatalf("Encode = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestBCD_SizeAndReplacement(t *testing.T) {
	t.Parallel()

	dt := types.BCD{}
	if dt.Size() != 1 {
		t.Fatalf("Size = %d; want 1", dt.Size())
	}
	if got := dt.ReplacementValue(); string(got) != string([]byte{0xFF}) {
		t.Fatalf("ReplacementValue = %v; want [0xFF]", got)
	}
}
