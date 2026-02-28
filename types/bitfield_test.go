package types_test

import (
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/types"
)

func TestBITFIELD_Decode(t *testing.T) {
	t.Parallel()

	dt := types.BITFIELD{SizeBytes: 1}
	cases := []struct {
		name    string
		payload []byte
		want    types.Value
		wantErr bool
	}{
		{
			name:    "Value",
			payload: []byte{0x12},
			want: types.Value{
				Value: []bool{false, true, false, false, true, false, false, false},
				Valid: true,
			},
		},
		{
			name:    "Replacement",
			payload: []byte{0xFF},
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

func TestBITFIELD_DecodeMultiByte(t *testing.T) {
	t.Parallel()

	dt := types.BITFIELD{SizeBytes: 2}
	got, err := dt.Decode([]byte{0x34, 0x12})
	if err != nil {
		t.Fatalf("Decode error = %v", err)
	}
	want := types.Value{
		Value: []bool{
			false, false, true, false, true, true, false, false,
			false, true, false, false, true, false, false, false,
		},
		Valid: true,
	}
	assertValue(t, got, want)
}

func TestBITFIELD_Encode(t *testing.T) {
	t.Parallel()

	dt1 := types.BITFIELD{SizeBytes: 1}
	dt2 := types.BITFIELD{SizeBytes: 2}
	cases := []struct {
		name    string
		dt      types.BITFIELD
		value   any
		want    []byte
		wantErr bool
	}{
		{
			name:  "BoolSlice",
			dt:    dt1,
			value: []bool{true, false, true, false, false, false, false, false},
			want:  []byte{0x05},
		},
		{
			name:  "ByteSlice",
			dt:    dt1,
			value: []byte{0x12},
			want:  []byte{0x12},
		},
		{
			name:  "Uint16",
			dt:    dt2,
			value: uint16(0x1234),
			want:  []byte{0x34, 0x12},
		},
		{
			name:    "BoolSliceWrongLength",
			dt:      dt1,
			value:   []bool{true},
			wantErr: true,
		},
		{
			name:    "Negative",
			dt:      dt1,
			value:   int(-1),
			wantErr: true,
		},
		{
			name:    "TooLarge",
			dt:      dt1,
			value:   uint16(0x1FF),
			wantErr: true,
		},
		{
			name:    "Replacement",
			dt:      dt1,
			value:   uint8(0xFF),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.dt.Encode(tc.value)
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

func TestBITFIELD_SizeAndReplacement(t *testing.T) {
	t.Parallel()

	dt := types.BITFIELD{SizeBytes: 2}
	if dt.Size() != 2 {
		t.Fatalf("Size = %d; want 2", dt.Size())
	}
	if got := dt.ReplacementValue(); string(got) != string([]byte{0xFF, 0xFF}) {
		t.Fatalf("ReplacementValue = %v; want [0xFF 0xFF]", got)
	}
}
