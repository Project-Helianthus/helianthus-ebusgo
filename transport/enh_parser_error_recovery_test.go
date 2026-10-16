package transport_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

func TestM53MalformedSecondByteRecovery(t *testing.T) {
	t.Parallel()
	for second := 0; second <= 0xff; second++ {
		if second >= 0x80 && second <= 0xbf {
			continue
		}
		t.Run(fmt.Sprintf("second_%02x", second), func(t *testing.T) {
			var parser transport.ENHParser
			if msg, ok, err := parser.Feed(0xc7); err != nil || ok || msg != (transport.ENHMessage{}) {
				t.Fatalf("first byte = %+v, %v, %v; want no message, nil", msg, ok, err)
			}
			msg, ok, err := parser.Feed(byte(second))
			if !errors.Is(err, ebuserrors.ErrInvalidPayload) || ok || msg != (transport.ENHMessage{}) {
				t.Fatalf("malformed second byte = %+v, %v, %v; want no message, ErrInvalidPayload", msg, ok, err)
			}
			// Independent wire vector: RECEIVED with all payload high bits clear.
			// The failed frame's high payload bits must not survive.
			if msg, ok, err := parser.Feed(0xc4); err != nil || ok || msg != (transport.ENHMessage{}) {
				t.Fatalf("fresh first byte = %+v, %v, %v; want no message, nil", msg, ok, err)
			}
			msg, ok, err = parser.Feed(0x81)
			want := transport.ENHMessage{Kind: transport.ENHMessageFrame, Command: transport.ENHResReceived, Data: 1}
			if err != nil || !ok || msg != want {
				t.Fatalf("recovered frame = %+v, %v, %v; want %+v, true, nil", msg, ok, err, want)
			}
		})
	}
}

func TestM53UnknownCommandIdentityAndRecovery(t *testing.T) {
	t.Parallel()
	for _, command := range []byte{4, 5, 6, 7, 8, 9, 0xd, 0xe, 0xf} {
		t.Run(fmt.Sprintf("command_%x", command), func(t *testing.T) {
			first := byte(0xc3) | command<<2
			frame, err := transport.DecodeENH(first, 0xbf)
			if frame != (transport.ENHFrame{}) {
				t.Fatalf("unknown command decoded as %+v", frame)
			}
			m53UnknownError(t, err)

			var parser transport.ENHParser
			messages, err := parser.Parse([]byte{0x21, first, 0xbf, 0x42})
			m53UnknownError(t, err)
			want := []transport.ENHMessage{{Kind: transport.ENHMessageFrame, Command: transport.ENHResReceived, Data: 0x21}}
			if !reflect.DeepEqual(messages, want) {
				t.Fatalf("messages before fault = %+v; want %+v", messages, want)
			}
			messages, err = parser.Parse([]byte{0xc4, 0x81})
			want[0].Data = 1
			if err != nil || !reflect.DeepEqual(messages, want) {
				t.Fatalf("recovery = %+v, %v; want %+v, nil", messages, err, want)
			}
		})
	}
}

func m53UnknownError(t *testing.T, err error) {
	t.Helper()
	for _, candidate := range []error{err, fmt.Errorf("parse frame: %w", err)} {
		if !errors.Is(candidate, transport.ErrUnknownENHCommand) || !errors.Is(candidate, ebuserrors.ErrInvalidPayload) {
			t.Fatalf("error = %v; want ErrUnknownENHCommand and compatible ErrInvalidPayload identity", candidate)
		}
	}
}
