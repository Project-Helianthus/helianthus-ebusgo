package transport_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// Vectors follow helianthus-docs-ebus/protocols/enh.md: short form,
// 11CCCCDD 10DDDDDD encoding, response IDs, and parser reset invariants.
// Expected messages are independent of EncodeENH, DecodeENH and Feed.
func TestK12ParserPartitions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		wire []byte
		want []transport.ENHMessage
	}{
		{"short_boundaries", []byte{0x00, 0x01, 0x7f}, []transport.ENHMessage{k12Message(1, 0), k12Message(1, 1), k12Message(1, 0x7f)}},
		{"received_payload_quadrants", []byte{0xc4, 0xbf, 0xc5, 0x80, 0xc6, 0x80, 0xc7, 0xbf}, []transport.ENHMessage{k12Message(1, 0x3f), k12Message(1, 0x40), k12Message(1, 0x80), k12Message(1, 0xff)}},
		{"reset_started_info", []byte{0x12, 0xc0, 0x81, 0xca, 0xa5, 0xcf, 0xbf, 0x7f}, []transport.ENHMessage{k12Message(1, 0x12), k12Message(0, 1), k12Message(2, 0xa5), k12Message(3, 0xff), k12Message(1, 0x7f)}},
		{"failure_symbols", []byte{0xeb, 0xbf, 0x00, 0xec, 0x80, 0xf1, 0xbf}, []transport.ENHMessage{k12Message(0xa, 0xff), k12Message(1, 0), k12Message(0xb, 0), k12Message(0xc, 0x7f)}},
		{"pending_tail", []byte{0x00, 0xc6, 0xaa, 0xcf}, []transport.ENHMessage{k12Message(1, 0), k12Message(1, 0xaa)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k12CheckPartitions(t, tc.wire, tc.want, nil, nil)
		})
	}
}

func TestK12ParserMalformedPartitions(t *testing.T) {
	t.Parallel()
	// Every malformed second-byte value; valid suffix bytes must not leak
	// past Parse's first error, regardless of where the chunks end.
	for second := 0; second <= 0xff; second++ {
		if second >= 0x80 && second <= 0xbf {
			continue
		}
		t.Run(fmt.Sprintf("second_%02x", second), func(t *testing.T) {
			k12CheckPartitions(t, []byte{0x21, 0xc6, byte(second), 0x42, 0xc4, 0x81},
				[]transport.ENHMessage{k12Message(1, 0x21)}, ebuserrors.ErrInvalidPayload, nil)
		})
	}
	for _, orphan := range []byte{0x80, 0xbf} {
		t.Run(fmt.Sprintf("orphan_%02x", orphan), func(t *testing.T) {
			k12CheckPartitions(t, []byte{0x21, orphan, 0x42},
				[]transport.ENHMessage{k12Message(1, 0x21)}, ebuserrors.ErrInvalidPayload, nil)
		})
	}
	for _, command := range []byte{4, 5, 6, 7, 8, 9, 0xd, 0xe, 0xf} {
		t.Run(fmt.Sprintf("unknown_%x", command), func(t *testing.T) {
			k12CheckPartitions(t, []byte{0x21, 0xc0 | command<<2, 0x80, 0x42},
				[]transport.ENHMessage{k12Message(1, 0x21)}, ebuserrors.ErrInvalidPayload, nil)
		})
	}
}

func TestK12ParserResetEpochs(t *testing.T) {
	t.Parallel()
	// Exercise every possible pending first byte, including unknown commands:
	// Reset must clear framing state before any command can be completed.
	for first := 0xc0; first <= 0xff; first++ {
		t.Run(fmt.Sprintf("pending_%02x", first), func(t *testing.T) {
			setup := func(p *transport.ENHParser) {
				t.Helper()
				got, err := p.Parse([]byte{0x11, byte(first)})
				if err != nil || !reflect.DeepEqual(got, []transport.ENHMessage{k12Message(1, 0x11)}) {
					t.Fatalf("prior epoch = %v, %v", got, err)
				}
				p.Reset()
			}
			k12CheckPartitions(t, []byte{0x00, 0xc6, 0xaa, 0xc0, 0x81, 0x7f},
				[]transport.ENHMessage{k12Message(1, 0), k12Message(1, 0xaa), k12Message(0, 1), k12Message(1, 0x7f)}, nil, setup)
			// An old continuation must be rejected, then legal bytes in a new
			// Parse call must still be delivered without another reset.
			var p transport.ENHParser
			setup(&p)
			got, err := p.Parse([]byte{0x80})
			if len(got) != 0 || !errors.Is(err, ebuserrors.ErrInvalidPayload) {
				t.Fatalf("old continuation = %v, %v; want no messages, invalid payload", got, err)
			}
			got, err = p.Parse([]byte{0x55, 0xc7, 0xbf})
			want := []transport.ENHMessage{k12Message(1, 0x55), k12Message(1, 0xff)}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("recovery = %v, %v; want %v, nil", got, err, want)
			}
		})
	}
}

func k12Message(command transport.ENHCommand, data byte) transport.ENHMessage {
	return transport.ENHMessage{Kind: transport.ENHMessageFrame, Command: command, Data: data}
}

func k12CheckPartitions(t *testing.T, wire []byte, want []transport.ENHMessage, wantErr error, setup func(*transport.ENHParser)) {
	t.Helper()
	// Each bit selects a cut between adjacent bytes: mask zero is the
	// unpartitioned oracle, all bits set is one byte per Parse call.
	for mask := 0; mask < 1<<(len(wire)-1); mask++ {
		var p transport.ENHParser
		if setup != nil {
			setup(&p)
		}
		var got []transport.ENHMessage
		var err error
		start := 0
		for end := 1; end <= len(wire); end++ {
			if end < len(wire) && mask&(1<<(end-1)) == 0 {
				continue
			}
			// Empty reads are inert even between the halves of a frame.
			if empty, emptyErr := p.Parse(nil); len(empty) != 0 || emptyErr != nil {
				t.Fatalf("mask=%b empty Parse = %v, %v", mask, empty, emptyErr)
			}
			var messages []transport.ENHMessage
			messages, err = p.Parse(wire[start:end])
			got = append(got, messages...)
			if err != nil {
				// Parse exposes no consumed count. Stop the entire stream at
				// the fault rather than guessing how much of this chunk remains.
				break
			}
			start = end
		}
		if !errors.Is(err, wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatalf("wire=% x mask=%b: got %v, %v; want %v, %v", wire, mask, got, err, want, wantErr)
		}
	}
}
