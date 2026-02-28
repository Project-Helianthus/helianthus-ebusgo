package types_test

import (
	"math"
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/types"
)

func TestDATA2b_Decode(t *testing.T) {
	t.Parallel()

	dt := types.DATA2b{}
	cases := []struct {
		name    string
		payload []byte
		want    types.Value
		wantErr bool
	}{
		{
			name:    "Zero",
			payload: []byte{0x00, 0x00},
			want:    types.Value{Value: float64(0), Valid: true},
		},
		{
			name:    "One",
			payload: []byte{0x00, 0x01},
			want:    types.Value{Value: float64(1), Valid: true},
		},
		{
			name:    "NegativeOne",
			payload: []byte{0x00, 0xFF},
			want:    types.Value{Value: float64(-1), Valid: true},
		},
		{
			name:    "Replacement",
			payload: []byte{0x00, 0x80},
			want:    types.Value{Valid: false},
		},
		{
			name:    "Short",
			payload: []byte{0x00},
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

func TestDATA2b_Encode(t *testing.T) {
	t.Parallel()

	dt := types.DATA2b{}
	cases := []struct {
		name    string
		value   any
		want    []byte
		wantErr bool
	}{
		{
			name:  "Float64",
			value: float64(1),
			want:  []byte{0x00, 0x01},
		},
		{
			name:  "Float32",
			value: float32(0.5),
			want:  []byte{0x80, 0x00},
		},
		{
			name:  "Int",
			value: int(-1),
			want:  []byte{0x00, 0xFF},
		},
		{
			name:    "NotExact",
			value:   0.1,
			wantErr: true,
		},
		{
			name:    "NaN",
			value:   math.NaN(),
			wantErr: true,
		},
		{
			name:    "TooHigh",
			value:   128.0,
			wantErr: true,
		},
		{
			name:    "WrongType",
			value:   "1",
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

func TestDATA2b_SizeAndReplacement(t *testing.T) {
	t.Parallel()

	dt := types.DATA2b{}
	if dt.Size() != 2 {
		t.Fatalf("Size = %d; want 2", dt.Size())
	}
	if got := dt.ReplacementValue(); string(got) != string([]byte{0x00, 0x80}) {
		t.Fatalf("ReplacementValue = %v; want [0x00 0x80]", got)
	}
}
