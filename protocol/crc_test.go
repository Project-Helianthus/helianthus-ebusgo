package protocol_test

import (
	"testing"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

func TestCRC_Vectors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data []byte
		want byte
	}{
		{
			name: "Empty",
			data: []byte{},
			want: 0x00,
		},
		{
			name: "Simple",
			data: []byte{0x01, 0x02},
			want: 0x99,
		},
		{
			name: "EscapedSymbols",
			data: []byte{0x10, 0xFE, 0xB5, 0x05, 0x04, 0x27, 0xA9, 0x15, 0xAA},
			want: 0x77,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := protocol.CRC(tc.data); got != tc.want {
				t.Fatalf("CRC = 0x%02x; want 0x%02x", got, tc.want)
			}
		})
	}
}
