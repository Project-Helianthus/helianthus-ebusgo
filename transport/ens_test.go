package transport_test

import (
	"errors"
	"testing"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/transport"
)

func assertENSInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("error = %v; want ErrInvalidPayload", err)
	}
}

func TestENSEncodeEscapes(t *testing.T) {
	t.Parallel()

	input := []byte{0x10, 0xA9, 0xAA, 0xFF}
	encoded := transport.EncodeENS(input)
	want := []byte{0x10, 0xA9, 0x00, 0xA9, 0x01, 0xFF}
	if string(encoded) != string(want) {
		t.Fatalf("EncodeENS = %v; want %v", encoded, want)
	}
}

func TestENSEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	input := []byte{0x00, 0x10, 0xA9, 0xAA, 0xFE, 0xFF}
	encoded := transport.EncodeENS(input)
	decoded, err := transport.DecodeENS(encoded)
	if err != nil {
		t.Fatalf("DecodeENS error = %v", err)
	}
	if string(decoded) != string(input) {
		t.Fatalf("DecodeENS = %v; want %v", decoded, input)
	}
}

func TestENSDecode_UnescapedSYN(t *testing.T) {
	t.Parallel()

	_, err := transport.DecodeENS([]byte{0xAA})
	assertENSInvalid(t, err)
}

func TestENSDecode_InvalidEscape(t *testing.T) {
	t.Parallel()

	_, err := transport.DecodeENS([]byte{0xA9, 0x02})
	assertENSInvalid(t, err)
}

func TestENSParser_StreamedEscape(t *testing.T) {
	t.Parallel()

	var parser transport.ENSParser
	first, err := parser.Parse([]byte{0x10, 0xA9})
	if err != nil {
		t.Fatalf("Parse first error = %v", err)
	}
	if string(first) != string([]byte{0x10}) {
		t.Fatalf("Parse first = %v; want [0x10]", first)
	}

	second, err := parser.Parse([]byte{0x00, 0x20, 0xA9, 0x01})
	if err != nil {
		t.Fatalf("Parse second error = %v", err)
	}
	wantSecond := []byte{0xA9, 0x20, 0xAA}
	if string(second) != string(wantSecond) {
		t.Fatalf("Parse second = %v; want %v", second, wantSecond)
	}

	if err := parser.Finish(); err != nil {
		t.Fatalf("Finish error = %v", err)
	}
}

func TestENSParser_FinishWithPendingEscape(t *testing.T) {
	t.Parallel()

	var parser transport.ENSParser
	if _, _, err := parser.Feed(0xA9); err != nil {
		t.Fatalf("Feed error = %v", err)
	}
	err := parser.Finish()
	assertENSInvalid(t, err)
}
