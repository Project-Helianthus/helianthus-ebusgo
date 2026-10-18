package transport

import (
	"testing"
	"time"
)

func TestTransportEscapeTimeoutLiteral(t *testing.T) {
	if EscapePendingTimeout != 32*time.Millisecond {
		t.Fatalf("EscapePendingTimeout = %v; want 32ms", EscapePendingTimeout)
	}
}

func TestTransportEscapeTimeoutLiteralBoundary(t *testing.T) {
	t.Parallel()

	// The clock and expectations deliberately do not use EscapePendingTimeout.
	start := time.Unix(0, 0)
	boundary := start.Add(32 * time.Millisecond)
	late := boundary.Add(time.Nanosecond)
	var d EbusEscapeDecoder
	assertFeed := func(raw byte, now time.Time, want byte, wantOK, wantEscaped bool, wantAdmin AdminEvent, wantPending bool, wantAbsorbed int) {
		t.Helper()
		got, ok, escaped, admin := d.Feed(raw, now)
		if got != want || ok != wantOK || escaped != wantEscaped || admin != wantAdmin {
			t.Fatalf("Feed(0x%02X, +%v) = (0x%02X, %v, %v, %+v); want (0x%02X, %v, %v, %+v)",
				raw, now.Sub(start), got, ok, escaped, admin, want, wantOK, wantEscaped, wantAdmin)
		}
		if d.HasPendingEscape() != wantPending || d.AbsorbedCount() != wantAbsorbed {
			t.Fatalf("after Feed(0x%02X, +%v): pending=%v absorbed=%d; want %v, %d",
				raw, now.Sub(start), d.HasPendingEscape(), d.AbsorbedCount(), wantPending, wantAbsorbed)
		}
	}

	assertFeed(0xA9, start, 0, false, false, AdminEvent{}, true, 0)
	// Absorption at exactly 32ms must leave the original escape pending.
	assertFeed(0xAA, boundary, 0, false, false, AdminEvent{}, true, 1)
	// One nanosecond later, discard the orphan and reprocess this raw byte.
	assertFeed(0x55, late, 0x55, true, false, AdminEvent{
		Kind:     AdminEventEscapePendingTimeout,
		Duration: 32*time.Millisecond + time.Nanosecond,
		Absorbed: 1,
	}, false, 0)
	assertFeed(0x7F, late.Add(time.Nanosecond), 0x7F, true, false, AdminEvent{}, false, 0)
	// A detached fresh pair confirms recovery without the expired clock anchor.
	assertFeed(0xA9, start.Add(time.Second), 0, false, false, AdminEvent{}, true, 0)
	assertFeed(0x01, start.Add(time.Second+time.Millisecond), 0xAA, true, true, AdminEvent{}, false, 0)
}
