package protocol_test

import (
	"testing"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

func TestFrameTypeForTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		target byte
		want   protocol.FrameType
	}{
		{
			name:   "Broadcast",
			target: protocol.AddressBroadcast,
			want:   protocol.FrameTypeBroadcast,
		},
		{
			name:   "InitiatorInitiator",
			target: 0x10,
			want:   protocol.FrameTypeInitiatorInitiator,
		},
		{
			name:   "InitiatorTarget",
			target: 0x08,
			want:   protocol.FrameTypeInitiatorTarget,
		},
		{
			name:   "InvalidAddress",
			target: protocol.SymbolEscape,
			want:   protocol.FrameTypeUnknown,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := protocol.FrameTypeForTarget(tc.target); got != tc.want {
				t.Fatalf("FrameTypeForTarget = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestFrame_Type(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{Target: 0x10}
	if got := frame.Type(); got != protocol.FrameTypeInitiatorInitiator {
		t.Fatalf("Frame.Type = %v; want %v", got, protocol.FrameTypeInitiatorInitiator)
	}
}
