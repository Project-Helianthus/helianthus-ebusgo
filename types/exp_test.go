package types_test

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/types"
)

func TestEXP_Decode(t *testing.T) {
	t.Parallel()

	dt := types.EXP{}
	valueBits := math.Float32bits(1.5)
	valuePayload := make([]byte, 4)
	binary.LittleEndian.PutUint32(valuePayload, valueBits)

	nanPayload := make([]byte, 4)
	binary.LittleEndian.PutUint32(nanPayload, 0x7FC00001)

	infPayload := make([]byte, 4)
	binary.LittleEndian.PutUint32(infPayload, 0x7F800000)

	cases := []struct {
		name    string
		payload []byte
		want    types.Value
		wantErr bool
	}{
		{
			name:    "Value",
			payload: valuePayload,
			want:    types.Value{Value: float32(1.5), Valid: true},
		},
		{
			name:    "Replacement",
			payload: []byte{0x00, 0x00, 0xC0, 0x7F},
			want:    types.Value{Valid: false},
		},
		{
			name:    "NaN",
			payload: nanPayload,
			want:    types.Value{Valid: false},
		},
		{
			name:    "Inf",
			payload: infPayload,
			want:    types.Value{Valid: false},
		},
		{
			name:    "Short",
			payload: []byte{0x00, 0x00},
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

func TestEXP_Encode(t *testing.T) {
	t.Parallel()

	dt := types.EXP{}
	valueBits := math.Float32bits(2.5)
	valuePayload := make([]byte, 4)
	binary.LittleEndian.PutUint32(valuePayload, valueBits)

	cases := []struct {
		name    string
		value   any
		want    []byte
		wantErr bool
	}{
		{
			name:  "Float32",
			value: float32(2.5),
			want:  valuePayload,
		},
		{
			name:  "Float64",
			value: float64(1.25),
			want:  []byte{0x00, 0x00, 0xA0, 0x3F},
		},
		{
			name:    "NaN",
			value:   math.Float32frombits(0x7FC00000),
			wantErr: true,
		},
		{
			name:    "Inf",
			value:   float64(math.Inf(1)),
			wantErr: true,
		},
		{
			name:    "TooLarge",
			value:   math.MaxFloat64,
			wantErr: true,
		},
		{
			name:    "WrongType",
			value:   12,
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

func TestEXP_SizeAndReplacement(t *testing.T) {
	t.Parallel()

	dt := types.EXP{}
	if dt.Size() != 4 {
		t.Fatalf("Size = %d; want 4", dt.Size())
	}
	if got := dt.ReplacementValue(); string(got) != string([]byte{0x00, 0x00, 0xC0, 0x7F}) {
		t.Fatalf("ReplacementValue = %v; want [0x00 0x00 0xC0 0x7F]", got)
	}
}
