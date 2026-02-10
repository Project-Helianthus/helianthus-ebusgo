//go:build !tinygo

package transport

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseHexResponseLines_FirstHexLine(t *testing.T) {
	t.Parallel()

	lines := []string{
		"  0x03 01 02 03  ",
		"ERR: spurious",
		"ERR: timeout",
	}

	got, err := parseHexResponseLines(lines)
	if err != nil {
		t.Fatalf("parseHexResponseLines error = %v", err)
	}
	want := []byte{0x03, 0x01, 0x02, 0x03}
	if !bytes.Equal(got, want) {
		t.Fatalf("parseHexResponseLines = %v; want %v", got, want)
	}
}

func TestStripLengthPrefix(t *testing.T) {
	t.Parallel()

	payload := []byte{0x03, 0x01, 0x02, 0x03}
	got := stripLengthPrefix(payload)
	want := []byte{0x01, 0x02, 0x03}
	if !bytes.Equal(got, want) {
		t.Fatalf("stripLengthPrefix = %v; want %v", got, want)
	}
}

func TestParseHexResponseLines_DoneBroadcast(t *testing.T) {
	t.Parallel()

	lines := []string{"done broadcast"}
	_, err := parseHexResponseLines(lines)
	if !errors.Is(err, errNoHexPayload) {
		t.Fatalf("parseHexResponseLines error = %v; want errNoHexPayload", err)
	}
}
