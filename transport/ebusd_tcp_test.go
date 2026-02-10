//go:build !tinygo

package transport

import (
	"bytes"
	"testing"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

func TestStripLengthPrefix(t *testing.T) {
	input := []byte{0x09, 0x03, 0x03, 0x16, 0x00, 0x45, 0x74, 0x61, 0x6a, 0x00}
	want := []byte{0x03, 0x03, 0x16, 0x00, 0x45, 0x74, 0x61, 0x6a, 0x00}
	if got := stripLengthPrefix(input); !bytes.Equal(got, want) {
		t.Fatalf("stripLengthPrefix = %x; want %x", got, want)
	}

	if got := stripLengthPrefix([]byte{0x00}); len(got) != 0 {
		t.Fatalf("stripLengthPrefix(00) = %x; want empty", got)
	}
}

func TestParseHexResponseLines(t *testing.T) {
	t.Run("ignores err lines before hex", func(t *testing.T) {
		lines := []string{"", "ERR: invalid numeric argument", "040000803f"}
		got, err := parseHexResponseLines(lines)
		if err != nil {
			t.Fatalf("parseHexResponseLines returned error: %v", err)
		}
		want := []byte{0x04, 0x00, 0x00, 0x80, 0x3f}
		if !bytes.Equal(got, want) {
			t.Fatalf("parseHexResponseLines = %x; want %x", got, want)
		}
	})

	t.Run("handles spaced hex", func(t *testing.T) {
		lines := []string{"09 03 03 16 00 45 74 61 6a 00"}
		got, err := parseHexResponseLines(lines)
		if err != nil {
			t.Fatalf("parseHexResponseLines returned error: %v", err)
		}
		want := []byte{0x09, 0x03, 0x03, 0x16, 0x00, 0x45, 0x74, 0x61, 0x6a, 0x00}
		if !bytes.Equal(got, want) {
			t.Fatalf("parseHexResponseLines = %x; want %x", got, want)
		}
	})

	t.Run("done broadcast yields no payload", func(t *testing.T) {
		_, err := parseHexResponseLines([]string{"done broadcast"})
		if err == nil || err != errNoHexPayload {
			t.Fatalf("expected errNoHexPayload, got %v", err)
		}
	})

	t.Run("usage returns invalid payload", func(t *testing.T) {
		_, err := parseHexResponseLines([]string{"usage: hex"})
		if err == nil || err != ebuserrors.ErrInvalidPayload {
			t.Fatalf("expected ErrInvalidPayload, got %v", err)
		}
	})
}
