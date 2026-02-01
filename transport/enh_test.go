package transport_test

import (
	"errors"
	"testing"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/transport"
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
		transport.ENHCommand(0xF),
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
	if msg.Kind != transport.ENHMessageData || msg.Byte != 0x7F {
		t.Fatalf("message = %+v; want data 0x7F", msg)
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
	if msgs[0].Kind != transport.ENHMessageData || msgs[0].Byte != 0x10 {
		t.Fatalf("msg0 = %+v; want data 0x10", msgs[0])
	}
	if msgs[1].Kind != transport.ENHMessageFrame || msgs[1].Command != transport.ENHReqSend || msgs[1].Data != 0x55 {
		t.Fatalf("msg1 = %+v; want cmd=%v data=0x55", msgs[1], transport.ENHReqSend)
	}
	if msgs[2].Kind != transport.ENHMessageData || msgs[2].Byte != 0x20 {
		t.Fatalf("msg2 = %+v; want data 0x20", msgs[2])
	}
}
