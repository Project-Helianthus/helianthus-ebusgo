package transport_test

import (
	"errors"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

func assertInvalidPayload(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("error = %v; want ErrInvalidPayload", err)
	}
}

func TestENHEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	cmds := []transport.ENHCommand{
		transport.ENHReqInit,
		transport.ENHReqSend,
		transport.ENHReqStart,
		transport.ENHReqInfo,
		transport.ENHResFailed,
		transport.ENHResErrorEBUS,
		transport.ENHResErrorHost,
	}
	dataValues := []byte{0x00, 0x01, 0x7F, 0x80, 0xA5, 0xFF}

	for _, cmd := range cmds {
		for _, data := range dataValues {
			seq := transport.EncodeENH(cmd, data)
			frame, err := transport.DecodeENH(seq[0], seq[1])
			if err != nil {
				t.Fatalf("DecodeENH error = %v", err)
			}
			if frame.Command != cmd || frame.Data != data {
				t.Fatalf("round-trip = (%v,0x%02x); want (%v,0x%02x)", frame.Command, frame.Data, cmd, data)
			}
		}
	}
}

func TestENHEncodeKnownSequence(t *testing.T) {
	t.Parallel()

	seq := transport.EncodeENH(transport.ENHReqStart, 0xA5)
	want := [2]byte{0xCA, 0xA5}
	if seq != want {
		t.Fatalf("EncodeENH = %v; want %v", seq, want)
	}
}

func TestENHDecode_InvalidByte1(t *testing.T) {
	t.Parallel()

	_, err := transport.DecodeENH(0x7F, 0x80)
	assertInvalidPayload(t, err)
}

func TestENHDecode_InvalidByte2(t *testing.T) {
	t.Parallel()

	_, err := transport.DecodeENH(0xC0, 0x40)
	assertInvalidPayload(t, err)
}

func TestENHParser_DataByte(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	msg, ok, err := parser.Feed(0x7F)
	if err != nil {
		t.Fatalf("Feed error = %v", err)
	}
	if !ok {
		t.Fatal("expected message")
	}
	if msg.Kind != transport.ENHMessageFrame || msg.Command != transport.ENHResReceived || msg.Data != 0x7F {
		t.Fatalf("message = %+v; want cmd=%v data=0x7F", msg, transport.ENHResReceived)
	}
}

func TestENHParser_Frame(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	if _, ok, err := parser.Feed(0xCA); err != nil || ok {
		t.Fatalf("Feed byte1 ok=%v err=%v; want ok=false err=nil", ok, err)
	}
	msg, ok, err := parser.Feed(0xA5)
	if err != nil {
		t.Fatalf("Feed byte2 error = %v", err)
	}
	if !ok {
		t.Fatal("expected message")
	}
	if msg.Kind != transport.ENHMessageFrame || msg.Command != transport.ENHReqStart || msg.Data != 0xA5 {
		t.Fatalf("message = %+v; want cmd=%v data=0xA5", msg, transport.ENHReqStart)
	}
}

func TestENHParser_PartialThenParse(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	if _, ok, err := parser.Feed(0xC0); err != nil || ok {
		t.Fatalf("Feed byte1 ok=%v err=%v; want ok=false err=nil", ok, err)
	}
	msgs, err := parser.Parse([]byte{0x80})
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("Parse messages = %d; want 1", len(msgs))
	}
	if msgs[0].Kind != transport.ENHMessageFrame || msgs[0].Command != transport.ENHReqInit || msgs[0].Data != 0x00 {
		t.Fatalf("message = %+v; want cmd=%v data=0x00", msgs[0], transport.ENHReqInit)
	}
}

func TestENHParser_UnexpectedByte2(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	_, _, err := parser.Feed(0x80)
	assertInvalidPayload(t, err)
}

func TestENHParser_MissingByte2(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	if _, ok, err := parser.Feed(0xC0); err != nil || ok {
		t.Fatalf("Feed byte1 ok=%v err=%v; want ok=false err=nil", ok, err)
	}
	_, _, err := parser.Feed(0x01)
	assertInvalidPayload(t, err)
}

func TestENHDecode_InvalidCommand0x0F(t *testing.T) {
	t.Parallel()

	// 0xFF encodes command nibble 0x0F which is not a valid ENH command.
	_, err := transport.DecodeENH(0xFF, 0x80)
	assertInvalidPayload(t, err)
}

func TestENHDecode_AllInvalidCommandNibbles(t *testing.T) {
	t.Parallel()

	// Commands 0x4..0x9, 0xD, 0xE, 0xF are undefined.
	invalid := []transport.ENHCommand{0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xD, 0xE, 0xF}
	for _, cmd := range invalid {
		seq := transport.EncodeENH(cmd, 0x00)
		_, err := transport.DecodeENH(seq[0], seq[1])
		if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
			t.Fatalf("DecodeENH(cmd=0x%X) error = %v; want ErrInvalidPayload", cmd, err)
		}
	}
}

func TestENHParser_ParseMixedValidAndCorrupt(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	// Stream: valid 0x10, orphan byte2 0x80 (corrupt), valid 0x20.
	// Parse stops at first error — 0x20 after the violation is NOT delivered.
	// Caller surfaces the error and re-parses the remaining buffer if needed.
	data := []byte{0x10, 0x80, 0x20}

	msgs, err := parser.Parse(data)
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("Parse error = %v; want ErrInvalidPayload", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("Parse messages = %d; want 1 (only bytes before violation)", len(msgs))
	}
	if msgs[0].Data != 0x10 {
		t.Fatalf("msg[0].Data = 0x%02x; want 0x10", msgs[0].Data)
	}
}

func TestENHParser_ParseCorruptThenValid(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	// byte1 (0xC0) followed by invalid byte2 (0x01) — corrupt frame.
	// 0x42 after the violation is NOT delivered (Parse stops at first error).
	data := []byte{0xC0, 0x01, 0x42}

	msgs, err := parser.Parse(data)
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("Parse error = %v; want ErrInvalidPayload", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("Parse messages = %d; want 0 (no bytes before violation)", len(msgs))
	}
}

func TestENHParser_ResetClearsPending(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	// Feed byte1, putting parser in pending state.
	_, ok, err := parser.Feed(0xC0)
	if err != nil || ok {
		t.Fatalf("Feed byte1 ok=%v err=%v", ok, err)
	}
	// Simulate timeout: reset parser.
	parser.Reset()
	// After reset, a fresh data byte should parse cleanly (not combine with stale byte1).
	msg, ok, err := parser.Feed(0x55)
	if err != nil {
		t.Fatalf("Feed after reset error = %v", err)
	}
	if !ok {
		t.Fatal("expected message after reset")
	}
	if msg.Command != transport.ENHResReceived || msg.Data != 0x55 {
		t.Fatalf("message = %+v; want Received 0x55", msg)
	}
}

func TestENHParser_ParseMultiple(t *testing.T) {
	t.Parallel()

	var parser transport.ENHParser
	seq := transport.EncodeENH(transport.ENHReqSend, 0x55)
	data := []byte{0x10, seq[0], seq[1], 0x20}

	msgs, err := parser.Parse(data)
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("Parse messages = %d; want 3", len(msgs))
	}
	if msgs[0].Kind != transport.ENHMessageFrame || msgs[0].Command != transport.ENHResReceived || msgs[0].Data != 0x10 {
		t.Fatalf("msg0 = %+v; want cmd=%v data=0x10", msgs[0], transport.ENHResReceived)
	}
	if msgs[1].Kind != transport.ENHMessageFrame || msgs[1].Command != transport.ENHReqSend || msgs[1].Data != 0x55 {
		t.Fatalf("msg1 = %+v; want cmd=%v data=0x55", msgs[1], transport.ENHReqSend)
	}
	if msgs[2].Kind != transport.ENHMessageFrame || msgs[2].Command != transport.ENHResReceived || msgs[2].Data != 0x20 {
		t.Fatalf("msg2 = %+v; want cmd=%v data=0x20", msgs[2], transport.ENHResReceived)
	}
}
