package types_test

import (
	"testing"

	"github.com/d3vi1/helianthus-ebusgo/types"
)

func TestWORD_Decode(t *testing.T) {
	t.Parallel()

	dt := types.WORD{}
	cases := []struct {
		name    string
		payload []byte
		want    types.Value
		wantErr bool
	}{
		{
			name:    "Value",
			payload: []byte{0x34, 0x12},
			want:    types.Value{Value: uint16(0x1234), Valid: true},
		},
		{
			name:    "Replacement",
			payload: []byte{0xFF, 0xFF},
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

func TestWORD_Encode(t *testing.T) {
	t.Parallel()

	dt := types.WORD{}
	cases := []struct {
		name    string
		value   any
		want    []byte
		wantErr bool
	}{
		{
			name:  "Uint16",
			value: uint16(0x1234),
			want:  []byte{0x34, 0x12},
		},
		{
			name:  "IntZero",
			value: int(0),
			want:  []byte{0x00, 0x00},
		},
		{
			name:    "Negative",
			value:   -1,
			wantErr: true,
		},
		{
			name:    "Replacement",
			value:   uint(65535),
			wantErr: true,
		},
		{
			name:    "TooHigh",
			value:   int(65535),
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

func TestWORD_SizeAndReplacement(t *testing.T) {
	t.Parallel()

	dt := types.WORD{}
	if dt.Size() != 2 {
		t.Fatalf("Size = %d; want 2", dt.Size())
	}
	if got := dt.ReplacementValue(); string(got) != string([]byte{0xFF, 0xFF}) {
		t.Fatalf("ReplacementValue = %v; want [0xFF 0xFF]", got)
	}
}
