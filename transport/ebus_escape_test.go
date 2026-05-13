package transport

import (
	"strings"
	"testing"
)

// F-23 (batch-19, 2026-05-13): unit tests for EbusEscapeDecoder. The
// decoder is the per-byte primitive that the ENH transport runs on
// every wire byte before emission so consumers receive logical
// (unescaped) symbols and the BytesAreUnescaped() contract becomes
// honest.

// TestEbusEscape_PlainPassthrough pins the no-escape path: any non-
// 0xA9 wire byte must emit unchanged with WasEscaped=false on the
// very first Push call.
func TestEbusEscape_PlainPassthrough(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	for _, raw := range []byte{0x00, 0x01, 0x08, 0x55, 0x7F, 0x80, 0xA8, 0xAA, 0xFE, 0xFF} {
		got, ok, wasEscaped, err := d.Push(raw)
		if err != nil {
			t.Fatalf("Push(0x%02X): unexpected err=%v", raw, err)
		}
		if !ok {
			t.Fatalf("Push(0x%02X): ok=false; want ok=true for plain passthrough", raw)
		}
		if got != raw {
			t.Fatalf("Push(0x%02X): decoded=0x%02X; want passthrough", raw, got)
		}
		if wasEscaped {
			t.Fatalf("Push(0x%02X): wasEscaped=true; want false for raw byte", raw)
		}
		if d.HasPendingEscape() {
			t.Fatalf("Push(0x%02X): decoder mid-pair after plain byte", raw)
		}
	}
}

// TestEbusEscape_A9_00_DecodesToA9_WasEscapedTrue pins the
// `wire 0xA9 0x00 → logical 0xA9` rule. The first Push consumes the
// lead and emits nothing (ok=false); the second Push emits 0xA9 with
// WasEscaped=true.
func TestEbusEscape_A9_00_DecodesToA9_WasEscapedTrue(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}

	got, ok, wasEscaped, err := d.Push(0xA9)
	if err != nil {
		t.Fatalf("Push(0xA9) lead: unexpected err=%v", err)
	}
	if ok {
		t.Fatal("Push(0xA9) lead: ok=true; want false (still accumulating)")
	}
	if wasEscaped {
		t.Fatal("Push(0xA9) lead: wasEscaped=true; want false on incomplete pair")
	}
	if !d.HasPendingEscape() {
		t.Fatal("HasPendingEscape() = false after 0xA9 lead; want true")
	}

	got, ok, wasEscaped, err = d.Push(0x00)
	if err != nil {
		t.Fatalf("Push(0x00) pair-complete: unexpected err=%v", err)
	}
	if !ok {
		t.Fatal("Push(0x00) pair-complete: ok=false; want true")
	}
	if got != 0xA9 {
		t.Fatalf("Push(0x00) pair-complete: decoded=0x%02X; want 0xA9", got)
	}
	if !wasEscaped {
		t.Fatal("Push(0x00) pair-complete: wasEscaped=false; want true")
	}
	if d.HasPendingEscape() {
		t.Fatal("HasPendingEscape() = true after completed pair; want false")
	}
}

// TestEbusEscape_A9_01_DecodesToAA_WasEscapedTrue pins the
// `wire 0xA9 0x01 → logical 0xAA` rule. The decoded byte must equal
// the SYN-value (0xAA) carried as user data, distinct from a real
// wire SYN which arrives unescaped.
func TestEbusEscape_A9_01_DecodesToAA_WasEscapedTrue(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}

	if _, ok, _, _ := d.Push(0xA9); ok {
		t.Fatal("Push(0xA9): ok=true on lead; want false")
	}

	got, ok, wasEscaped, err := d.Push(0x01)
	if err != nil {
		t.Fatalf("Push(0x01) pair-complete: unexpected err=%v", err)
	}
	if !ok {
		t.Fatal("Push(0x01) pair-complete: ok=false; want true")
	}
	if got != 0xAA {
		t.Fatalf("Push(0x01) pair-complete: decoded=0x%02X; want 0xAA", got)
	}
	if !wasEscaped {
		t.Fatal("Push(0x01) pair-complete: wasEscaped=false; want true")
	}
	if d.HasPendingEscape() {
		t.Fatal("HasPendingEscape() = true after completed pair; want false")
	}
}

// TestEbusEscape_InvalidA9_FF_ReturnsErrorAndResumes pins the
// invalid-pair error path. A 0xA9 lead followed by anything other
// than 0x00 or 0x01 returns an error AND clears the in-flight
// escape state, so the very next Push resumes cleanly.
func TestEbusEscape_InvalidA9_FF_ReturnsErrorAndResumes(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}

	if _, ok, _, _ := d.Push(0xA9); ok {
		t.Fatal("Push(0xA9): ok=true on lead; want false")
	}

	got, ok, wasEscaped, err := d.Push(0xFF)
	if err == nil {
		t.Fatal("Push(0xFF) after lead: err=nil; want invalid-pair error")
	}
	if ok {
		t.Fatal("Push(0xFF) after lead: ok=true; want false")
	}
	if wasEscaped {
		t.Fatal("Push(0xFF) after lead: wasEscaped=true; want false")
	}
	if got != 0 {
		t.Fatalf("Push(0xFF) after lead: decoded=0x%02X; want zero (sentinel)", got)
	}
	if !strings.Contains(err.Error(), "0xA9 0xFF") {
		t.Fatalf("error message lacks the offending pair: %q", err.Error())
	}
	if d.HasPendingEscape() {
		t.Fatal("HasPendingEscape() = true after invalid-pair error; want false (decoder must clear state to resume cleanly)")
	}

	// Resume verification: the next byte must decode cleanly.
	got, ok, wasEscaped, err = d.Push(0x55)
	if err != nil {
		t.Fatalf("resume Push(0x55): unexpected err=%v", err)
	}
	if !ok || got != 0x55 || wasEscaped {
		t.Fatalf("resume Push(0x55): decoded=0x%02X ok=%v wasEscaped=%v; want 0x55 true false",
			got, ok, wasEscaped)
	}
}

// TestEbusEscape_PendingEscapeReportedCorrectly cross-checks that
// HasPendingEscape() tracks the internal `escape` flag at every
// transition: false initially, true after a lead, false after pair
// completion, true again on a fresh lead.
func TestEbusEscape_PendingEscapeReportedCorrectly(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	if d.HasPendingEscape() {
		t.Fatal("HasPendingEscape() = true at zero value; want false")
	}

	_, _, _, _ = d.Push(0xA9)
	if !d.HasPendingEscape() {
		t.Fatal("after lead: HasPendingEscape() = false; want true")
	}

	_, _, _, _ = d.Push(0x00)
	if d.HasPendingEscape() {
		t.Fatal("after valid pair: HasPendingEscape() = true; want false")
	}

	// Second lead — verify the flag transitions on each cycle.
	_, _, _, _ = d.Push(0xA9)
	if !d.HasPendingEscape() {
		t.Fatal("after second lead: HasPendingEscape() = false; want true")
	}

	_, _, _, _ = d.Push(0x01)
	if d.HasPendingEscape() {
		t.Fatal("after second valid pair: HasPendingEscape() = true; want false")
	}
}

// TestEbusEscape_ResetClearsPendingState pins the Reset() contract:
// an in-flight `escape=true` is wiped so the next Push starts fresh.
// This is the contract that the ENH transport relies on at every
// lifecycle boundary (reconnect, RESETTED, surface reset).
func TestEbusEscape_ResetClearsPendingState(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	_, _, _, _ = d.Push(0xA9)
	if !d.HasPendingEscape() {
		t.Fatal("precondition: decoder must be mid-pair before Reset()")
	}

	d.Reset()
	if d.HasPendingEscape() {
		t.Fatal("after Reset(): HasPendingEscape() = true; want false")
	}

	// Next Push must NOT pair the cleared lead with whatever byte
	// arrives. 0x00 alone is plain passthrough — confirming the
	// stranded-pair hazard is gone.
	got, ok, wasEscaped, err := d.Push(0x00)
	if err != nil {
		t.Fatalf("post-Reset Push(0x00): unexpected err=%v", err)
	}
	if !ok {
		t.Fatal("post-Reset Push(0x00): ok=false; want true (plain passthrough)")
	}
	if got != 0x00 {
		t.Fatalf("post-Reset Push(0x00): decoded=0x%02X; want 0x00 (NOT paired with stale lead)", got)
	}
	if wasEscaped {
		t.Fatal("post-Reset Push(0x00): wasEscaped=true; want false (stale pair was cleared)")
	}
}
