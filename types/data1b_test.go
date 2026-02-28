package types_test

import (
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/types"
)

func TestDATA1b_Decode(t *testing.T) {
	t.Parallel()

	dt := types.DATA1b{}
	cases := []struct {
		name    string
		payload []byte
		want    types.Value
		wantErr bool
	}{
		{
			name:    "Zero",
			payload: []byte{0x00},
			want:    types.Value{Value: int8(0), Valid: true},
		},
		{
			name:    "Max",
			payload: []byte{0x7F},
			want:    types.Value{Value: int8(127), Valid: true},
		},
		{
			name:    "Negative",
			payload: []byte{0x81},
			want:    types.Value{Value: int8(-127), Valid: true},
		},
		{
			name:    "Replacement",
			payload: []byte{0x80},
			want:    types.Value{Valid: false},
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

func TestDATA1b_Encode(t *testing.T) {
	t.Parallel()

	dt := types.DATA1b{}
	cases := []struct {
		name    string
		value   any
		want    []byte
		wantErr bool
	}{
		{
			name:  "Int",
			value: int8(10),
			want:  []byte{0x0A},
		},
		{
			name:  "Negative",
			value: int(-127),
			want:  []byte{0x81},
		},
		{
			name:  "Unsigned",
			value: uint(127),
			want:  []byte{0x7F},
		},
		{
			name:    "TooLow",
			value:   int(-128),
			wantErr: true,
		},
		{
			name:    "TooHigh",
			value:   int(128),
			wantErr: true,
		},
		{
			name:    "WrongType",
			value:   1.5,
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

func TestDATA1b_SizeAndReplacement(t *testing.T) {
	t.Parallel()

	dt := types.DATA1b{}
	if dt.Size() != 1 {
		t.Fatalf("Size = %d; want 1", dt.Size())
	}
	if got := dt.ReplacementValue(); string(got) != string([]byte{0x80}) {
		t.Fatalf("ReplacementValue = %v; want [0x80]", got)
	}
}
