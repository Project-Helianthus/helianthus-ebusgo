package transport

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// The public frame-atomic-visibility-v8 contract, section 1.3, uses
// elapsed > 32 ms: the exact deadline still admits absorption/completion.
func TestK13AbsorptionDeadlineBoundaries(t *testing.T) {
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		for _, absorbed := range []int{MaxAaAbsorptionsPerEscapePair - 1, MaxAaAbsorptionsPerEscapePair} {
			t.Run(fmt.Sprintf("delta=%s/absorbed=%d", delta, absorbed), func(t *testing.T) {
				var d EbusEscapeDecoder
				s := k13Stream{t: t, decoder: &d, start: time.Unix(1, 0)}
				s.feed(0xA9, 0, AdminEvent{})
				for i := 0; i < absorbed; i++ {
					s.feed(0xAA, time.Duration(i+1)*time.Nanosecond, AdminEvent{})
				}
				s.noBytes()

				at := EscapePendingTimeout + delta
				var event AdminEvent
				var want []k13Byte
				switch {
				case delta > 0:
					event = AdminEvent{Kind: AdminEventEscapePendingTimeout, Duration: at, Absorbed: absorbed}
					want = append(want, k13Byte{value: 0xAA}) // Timeout reprocesses the current AA.
				case absorbed == MaxAaAbsorptionsPerEscapePair:
					event = AdminEvent{Kind: AdminEventEscapeBudgetExhausted, Duration: at, Absorbed: absorbed}
				}
				s.feed(0xAA, at, event)
				if delta <= 0 && absorbed < MaxAaAbsorptionsPerEscapePair {
					s.state(true, MaxAaAbsorptionsPerEscapePair)
					s.noBytes()
					s.feed(0x01, at, AdminEvent{})
					want = append(want, k13Byte{value: 0xAA, escaped: true})
				}
				s.state(false, 0)
				s.prefix(at + time.Nanosecond)
				s.check(append(want, k13PrefixBytes()...))
			})
		}
	}
}

func TestK13CompletionAndMalformedAtBudgetDeadline(t *testing.T) {
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		for _, raw := range []byte{0x00, 0x01, 0xFF, 0xA9} {
			t.Run(fmt.Sprintf("delta=%s/raw=%02X", delta, raw), func(t *testing.T) {
				var d EbusEscapeDecoder
				s := k13Stream{t: t, decoder: &d, start: time.Unix(1, 0)}
				s.feed(0xA9, 0, AdminEvent{})
				for i := 0; i < MaxAaAbsorptionsPerEscapePair; i++ {
					s.feed(0xAA, time.Duration(i+1)*time.Nanosecond, AdminEvent{})
				}
				s.state(true, MaxAaAbsorptionsPerEscapePair)
				s.noBytes()
				at := EscapePendingTimeout + delta
				var event AdminEvent
				var want []k13Byte
				switch {
				case delta > 0:
					event = AdminEvent{Kind: AdminEventEscapePendingTimeout, Duration: at, Absorbed: MaxAaAbsorptionsPerEscapePair}
					if raw != 0xA9 {
						want = append(want, k13Byte{value: raw})
					}
				case raw == 0x00:
					want = append(want, k13Byte{value: 0xA9, escaped: true})
				case raw == 0x01:
					want = append(want, k13Byte{value: 0xAA, escaped: true})
				default:
					event = AdminEvent{Kind: AdminEventEscapeRecovery, Duration: at, Absorbed: MaxAaAbsorptionsPerEscapePair}
				}
				s.feed(raw, at, event)
				if delta > 0 && raw == 0xA9 {
					// The byte that expired the old pair starts a fresh pair.
					s.state(true, 0)
					for i := 0; i < MaxAaAbsorptionsPerEscapePair; i++ {
						s.feed(0xAA, at+time.Duration(i+1)*time.Nanosecond, AdminEvent{})
					}
					s.feed(0x00, at+EscapePendingTimeout, AdminEvent{})
					want = append(want, k13Byte{value: 0xA9, escaped: true})
				}
				s.state(false, 0)
				s.prefix(at + EscapePendingTimeout + time.Nanosecond)
				s.check(append(want, k13PrefixBytes()...))
			})
		}
	}
}

func TestK13ResetPendingAtBudgetDeadline(t *testing.T) {
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(delta.String(), func(t *testing.T) {
			var d EbusEscapeDecoder
			s := k13Stream{t: t, decoder: &d, start: time.Unix(1, 0)}
			s.feed(0xA9, 0, AdminEvent{})
			for i := 0; i < MaxAaAbsorptionsPerEscapePair; i++ {
				s.feed(0xAA, time.Duration(i+1)*time.Nanosecond, AdminEvent{})
			}
			s.state(true, MaxAaAbsorptionsPerEscapePair)
			s.noBytes()
			at := EscapePendingTimeout + delta
			d.Reset()
			s.state(false, 0)
			// A former completion byte is now raw, even after the old deadline.
			s.feed(0x00, at, AdminEvent{})
			s.prefix(at)
			// A fresh pair gets both a full count budget and its own clock anchor.
			s.feed(0xA9, at, AdminEvent{})
			for i := 0; i < MaxAaAbsorptionsPerEscapePair; i++ {
				s.feed(0xAA, at+time.Duration(i+1)*time.Nanosecond, AdminEvent{})
			}
			s.state(true, MaxAaAbsorptionsPerEscapePair)
			s.feed(0x01, at+EscapePendingTimeout, AdminEvent{})
			s.state(false, 0)
			want := append([]k13Byte{{value: 0x00}}, k13PrefixBytes()...)
			s.check(append(want, k13Byte{value: 0xAA, escaped: true}))
		})
	}
}

type k13Byte struct {
	value   byte
	escaped bool
}

type k13Stream struct {
	t         *testing.T
	decoder   *EbusEscapeDecoder
	start     time.Time
	bytes     []k13Byte
	admin     []AdminEvent
	wantAdmin []AdminEvent
}

func (s *k13Stream) feed(raw byte, elapsed time.Duration, want AdminEvent) {
	s.t.Helper()
	value, ok, escaped, event := s.decoder.Feed(raw, s.start.Add(elapsed))
	if ok {
		s.bytes = append(s.bytes, k13Byte{value: value, escaped: escaped})
	} else if escaped {
		s.t.Errorf("Feed(%02X) at %s: escaped without a logical byte", raw, elapsed)
	}
	s.admin = append(s.admin, event)
	s.wantAdmin = append(s.wantAdmin, want)
}

func (s *k13Stream) state(pending bool, absorbed int) {
	s.t.Helper()
	if s.decoder.HasPendingEscape() != pending || s.decoder.AbsorbedCount() != absorbed {
		s.t.Errorf("state: pending=%v absorbed=%d; want pending=%v absorbed=%d",
			s.decoder.HasPendingEscape(), s.decoder.AbsorbedCount(), pending, absorbed)
	}
}

func (s *k13Stream) noBytes() {
	s.t.Helper()
	if len(s.bytes) != 0 {
		s.t.Errorf("pending escape fabricated logical bytes: %v", s.bytes)
	}
}

func (s *k13Stream) prefix(at time.Duration) {
	s.t.Helper()
	for _, raw := range []byte{0x55, 0xA9, 0x00, 0xA9, 0x01, 0x42} {
		s.feed(raw, at, AdminEvent{})
	}
	s.state(false, 0)
}

func k13PrefixBytes() []k13Byte {
	return []k13Byte{{value: 0x55}, {value: 0xA9, escaped: true}, {value: 0xAA, escaped: true}, {value: 0x42}}
}

func (s *k13Stream) check(want []k13Byte) {
	s.t.Helper()
	// Bytes and all per-Feed admin events have independent oracles. In
	// particular, a diagnostic must never stand in for suppressed data.
	if !reflect.DeepEqual(s.bytes, want) {
		s.t.Errorf("logical bytes/provenance: got %v; want %v", s.bytes, want)
	}
	if !reflect.DeepEqual(s.admin, s.wantAdmin) {
		s.t.Errorf("admin sequence: got %+v; want %+v", s.admin, s.wantAdmin)
	}
}
