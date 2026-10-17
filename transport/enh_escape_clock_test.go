//go:build !tinygo

package transport_test

import (
	"errors"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

func escapeClockConn(t *testing.T, continuation byte) *u14SplitReadConn {
	t.Helper()
	conn := &u14SplitReadConn{frames: [][2]byte{
		transport.EncodeENH(transport.ENHResReceived, 0xA9),
		transport.EncodeENH(transport.ENHResReceived, continuation),
	}}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func assertEscapeClockResult(t *testing.T, enh *transport.ENHTransport, conn *u14SplitReadConn, want byte, escaped bool, timeouts uint64) {
	t.Helper()
	ev, err := enh.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent: %v", err)
	}
	if ev.Kind != transport.StreamEventByte || ev.Byte != want || ev.WasEscaped != escaped {
		t.Fatalf("event = %+v; want byte 0x%02X escaped=%v", ev, want, escaped)
	}
	if conn.reads != 2 || len(conn.frames) != 0 {
		t.Fatalf("read boundary: calls=%d remaining=%d; want 2, 0", conn.reads, len(conn.frames))
	}
	if got := enh.EscapePendingTimeoutTotal(); got != timeouts {
		t.Fatalf("EscapePendingTimeoutTotal = %d; want %d", got, timeouts)
	}
	if enh.DecodeFaultTotal() != 0 || enh.EscapeRecoveryTotal() != 0 || enh.EscapeBudgetExhaustedTotal() != 0 || enh.EscapeAaAbsorbedTotal() != 0 {
		t.Fatal("unexpected escape fault, recovery, budget exhaustion or absorption")
	}
	if extra, err := enh.ReadEvent(); !errors.Is(err, ebuserrors.ErrTransportClosed) {
		t.Fatalf("after pair: event=%+v err=%v; want stream exhausted", extra, err)
	}
	if conn.reads != 3 {
		t.Fatalf("read calls after EOF = %d; want 3", conn.reads)
	}
}

func TestEscapeClockPendingTimeoutBoundary(t *testing.T) {
	t.Parallel()
	for _, pair := range []struct {
		name         string
		continuation byte
		decoded      byte
	}{
		{"escape", 0x00, 0xA9},
		{"syn", 0x01, 0xAA},
	} {
		t.Run(pair.name, func(t *testing.T) {
			for _, boundary := range []struct {
				name    string
				elapsed time.Duration
				expired bool
			}{
				{"before", transport.EscapePendingTimeout - time.Nanosecond, false},
				{"at", transport.EscapePendingTimeout, false},
				{"after", transport.EscapePendingTimeout + time.Nanosecond, true},
			} {
				t.Run(boundary.name, func(t *testing.T) {
					now := time.Unix(1, 0)
					conn := escapeClockConn(t, pair.continuation)
					conn.beforeRead = func(read int) {
						if read == 2 {
							now = now.Add(boundary.elapsed)
						}
					}
					enh := transport.NewENHTransport(conn, time.Second, time.Second)
					transport.SetEscapeClockForTest(enh, func() time.Time { return now })
					want, escaped, timeouts := pair.decoded, true, uint64(0)
					if boundary.expired {
						want, escaped, timeouts = pair.continuation, false, 1
					}
					assertEscapeClockResult(t, enh, conn, want, escaped, timeouts)
				})
			}
		})
	}
}

func TestEscapeClockInstancesIndependent(t *testing.T) {
	t.Parallel()
	firstNow, secondNow := time.Unix(1, 0), time.Unix(100, 0)
	firstConn := escapeClockConn(t, 0x00)
	secondConn := escapeClockConn(t, 0x01)
	first := transport.NewENHTransport(firstConn, time.Second, time.Second)
	second := transport.NewENHTransport(secondConn, time.Second, time.Second)
	transport.SetEscapeClockForTest(first, func() time.Time { return firstNow })
	transport.SetEscapeClockForTest(second, func() time.Time { return secondNow })

	secondConn.beforeRead = func(read int) {
		if read == 2 {
			secondNow = secondNow.Add(transport.EscapePendingTimeout + time.Nanosecond)
		}
	}
	firstConn.beforeRead = func(read int) {
		if read == 2 {
			// The first lead is already pending while the second instance
			// advances beyond its own deadline and reprocesses its suffix.
			assertEscapeClockResult(t, second, secondConn, 0x01, false, 1)
			firstNow = firstNow.Add(transport.EscapePendingTimeout - time.Nanosecond)
		}
	}
	assertEscapeClockResult(t, first, firstConn, 0xA9, true, 0)
}
