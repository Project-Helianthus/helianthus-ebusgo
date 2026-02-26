package protocol

import (
	"bytes"
	"testing"
)

func TestEscapeBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  []byte
		want []byte
	}{
		{
			name: "empty",
			raw:  []byte{},
			want: []byte{},
		},
		{
			name: "normal bytes",
			raw:  []byte{0x10, 0x22, 0x7F},
			want: []byte{0x10, 0x22, 0x7F},
		},
		{
			name: "single SymbolEscape",
			raw:  []byte{SymbolEscape},
			want: []byte{SymbolEscape, 0x00},
		},
		{
			name: "single SymbolSyn",
			raw:  []byte{SymbolSyn},
			want: []byte{SymbolEscape, 0x01},
		},
		{
			name: "mixed",
			raw:  []byte{0x01, SymbolEscape, 0x02, SymbolSyn, 0x03},
			want: []byte{0x01, SymbolEscape, 0x00, 0x02, SymbolEscape, 0x01, 0x03},
		},
		{
			name: "consecutive escaped symbols",
			raw:  []byte{SymbolEscape, SymbolSyn, SymbolEscape},
			want: []byte{SymbolEscape, 0x00, SymbolEscape, 0x01, SymbolEscape, 0x00},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := EscapeBytes(tc.raw); !bytes.Equal(got, tc.want) {
				t.Fatalf("EscapeBytes(%v) = %v; want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestEncodeSlaveResponse(t *testing.T) {
	t.Parallel()

	t.Run("empty data", func(t *testing.T) {
		t.Parallel()

		// CRC([0x00]) == 0x00
		want := []byte{0x00, 0x00}
		if got := EncodeSlaveResponse(nil); !bytes.Equal(got, want) {
			t.Fatalf("EncodeSlaveResponse(nil) = %v; want %v", got, want)
		}
	})

	t.Run("normal data single byte", func(t *testing.T) {
		t.Parallel()

		data := []byte{0x10}
		raw := []byte{byte(len(data)), 0x10}
		want := append([]byte{}, raw...)
		want = append(want, CRC(raw))

		if got := EncodeSlaveResponse(data); !bytes.Equal(got, want) {
			t.Fatalf("EncodeSlaveResponse(%v) = %v; want %v", data, got, want)
		}
	})

	t.Run("data escapes SymbolEscape", func(t *testing.T) {
		t.Parallel()

		data := []byte{SymbolEscape}
		got := EncodeSlaveResponse(data)
		wantPrefix := []byte{0x01, SymbolEscape, 0x00}
		if !bytes.HasPrefix(got, wantPrefix) {
			t.Fatalf("EncodeSlaveResponse(%v) = %v; missing escaped data prefix %v", data, got, wantPrefix)
		}
	})

	t.Run("data escapes SymbolSyn", func(t *testing.T) {
		t.Parallel()

		data := []byte{SymbolSyn}
		got := EncodeSlaveResponse(data)
		wantPrefix := []byte{0x01, SymbolEscape, 0x01}
		if !bytes.HasPrefix(got, wantPrefix) {
			t.Fatalf("EncodeSlaveResponse(%v) = %v; missing escaped data prefix %v", data, got, wantPrefix)
		}
	})

	t.Run("CRC uses unescaped NN+data", func(t *testing.T) {
		t.Parallel()

		data := []byte{SymbolEscape, 0x42, SymbolSyn}
		got := EncodeSlaveResponse(data)
		segment := decodeEscapedSegment(t, got)
		if len(segment) != len(data)+2 {
			t.Fatalf("decoded segment len = %d; want %d", len(segment), len(data)+2)
		}

		nn := segment[0]
		if nn != byte(len(data)) {
			t.Fatalf("decoded NN = 0x%02x; want 0x%02x", nn, byte(len(data)))
		}
		if !bytes.Equal(segment[1:1+len(data)], data) {
			t.Fatalf("decoded data = %v; want %v", segment[1:1+len(data)], data)
		}

		wantCRC := CRC(append([]byte{nn}, data...))
		gotCRC := segment[len(segment)-1]
		if gotCRC != wantCRC {
			t.Fatalf("CRC byte = 0x%02x; want 0x%02x", gotCRC, wantCRC)
		}
	})

	t.Run("known vector with escaped CRC", func(t *testing.T) {
		t.Parallel()

		// Hand-calculated CRC for raw [0x01, 0x31]:
		// crc0=0x00 -> Update(0x00,0x01)=0x01 -> Update(0x01,0x31)=0xAA.
		data := []byte{0x31}
		want := []byte{0x01, 0x31, SymbolEscape, 0x01}

		if got := EncodeSlaveResponse(data); !bytes.Equal(got, want) {
			t.Fatalf("EncodeSlaveResponse(%v) = %v; want %v", data, got, want)
		}
	})

	t.Run("NN is escaped on wire when length is SymbolEscape", func(t *testing.T) {
		t.Parallel()

		data := make([]byte, int(SymbolEscape))
		got := EncodeSlaveResponse(data)
		if len(got) < 2 {
			t.Fatalf("EncodeSlaveResponse len = %d; want >=2", len(got))
		}
		if got[0] != SymbolEscape || got[1] != 0x00 {
			t.Fatalf("encoded prefix = %v; want escaped NN [0x%02x 0x00]", got[:2], SymbolEscape)
		}

		decoded := decodeEscapedSegment(t, got)
		if decoded[0] != SymbolEscape {
			t.Fatalf("decoded NN = 0x%02x; want 0x%02x", decoded[0], SymbolEscape)
		}
	})
}

func decodeEscapedSegment(t *testing.T, escaped []byte) []byte {
	t.Helper()

	decoded := make([]byte, 0, len(escaped))
	for i := 0; i < len(escaped); i++ {
		b := escaped[i]
		if b != SymbolEscape {
			decoded = append(decoded, b)
			continue
		}
		if i+1 >= len(escaped) {
			t.Fatalf("invalid escaped segment %v: trailing escape byte", escaped)
		}
		next := escaped[i+1]
		switch next {
		case 0x00:
			decoded = append(decoded, SymbolEscape)
		case 0x01:
			decoded = append(decoded, SymbolSyn)
		default:
			t.Fatalf("invalid escaped segment %v: unknown escape code 0x%02x", escaped, next)
		}
		i++
	}
	return decoded
}
