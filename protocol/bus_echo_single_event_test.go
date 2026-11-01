package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

type echoWriteErrTransport struct {
	*echoF23TestTransport
	writeErr error
}

func (t *echoWriteErrTransport) Write(payload []byte) (int, error) {
	if t.writeErr != nil {
		return 0, t.writeErr
	}
	return t.echoF23TestTransport.Write(payload)
}

func TestBus_EchoMismatch_SingleEventWithByteProvenance(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		echo      []echoF23Event
		writeErr  error
		wantByte  byte
		wantKnown bool
	}{
		{
			name: "generic value mismatch carries the observed byte",
			echo: []echoF23Event{
				{value: 0x08},
				{value: 0x31},
			},
			wantByte:  0x31,
			wantKnown: true,
		},
		{
			name: "generic value mismatch with a real zero byte",
			echo: []echoF23Event{
				{value: 0x08},
				{value: 0x00},
			},
			wantByte:  0x00,
			wantKnown: true,
		},
		{
			name:      "transport write error with mismatch wording has no byte",
			writeErr:  errors.New("echo mismatch: escape pair rejected: " + ebuserrors.ErrBusCollision.Error()),
			wantKnown: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			base := &echoF23TestTransport{unescaped: true, echo: tc.echo}
			var transportUnderTest = &echoWriteErrTransport{echoF23TestTransport: base}
			if tc.writeErr != nil {
				transportUnderTest.writeErr = errors.Join(ebuserrors.ErrBusCollision, tc.writeErr)
			}

			var mu sync.Mutex
			var events []protocol.BusEvent
			cfg := protocol.DefaultBusConfig()
			cfg.Observer = protocol.BusObserverFunc(func(ev protocol.BusEvent) error {
				mu.Lock()
				defer mu.Unlock()
				if ev.Kind == protocol.BusEventEchoMismatch {
					events = append(events, ev)
				}
				return nil
			})
			bus := protocol.NewBus(transportUnderTest, cfg, 8)
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			bus.Run(runCtx)

			_, err := bus.Send(context.Background(), protocol.Frame{
				Source:    0x10,
				Target:    0x08,
				Primary:   0xB5,
				Secondary: 0x16,
			})
			if !errors.Is(err, ebuserrors.ErrBusCollision) {
				t.Fatalf("Send err = %v; want wrapped ErrBusCollision", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(events) != 1 {
				t.Fatalf("echo-mismatch events = %d (%+v); want exactly 1", len(events), events)
			}
			if events[0].ByteKnown != tc.wantKnown {
				t.Errorf("ByteKnown = %v; want %v", events[0].ByteKnown, tc.wantKnown)
			}
			if events[0].Byte != tc.wantByte {
				t.Errorf("Byte = 0x%02X; want 0x%02X", events[0].Byte, tc.wantByte)
			}
		})
	}
}
