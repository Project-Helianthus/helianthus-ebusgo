package transport

import (
	"testing"
	"time"
)

// Phase 1 Step B1 (frame-atomic-visibility v8 §1.3 / §5, invariant
// I4): unit tests for the v8 AA-aware escape decoder. The legacy
// Push tests in ebus_escape_test.go continue to pin the un-clocked
// (legacy) contract; this file pins the v8 Feed contract — AA
// absorption, wall-clock cap, admin events, data-preserving
// recovery on timeout.

// TestFeed_PlainPassthrough pins the no-escape path under Feed:
// every non-0xA9 wire byte emits unchanged with WasEscaped=false on
// the first call. No admin event.
func TestFeed_PlainPassthrough(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)
	for _, raw := range []byte{0x00, 0x01, 0x08, 0x55, 0x7F, 0x80, 0xA8, 0xAA, 0xFE, 0xFF} {
		got, ok, wasEscaped, admin := d.Feed(raw, t0)
		if !ok {
			t.Fatalf("Feed(0x%02X): ok=false; want true (plain passthrough)", raw)
		}
		if got != raw {
			t.Fatalf("Feed(0x%02X): decoded=0x%02X; want passthrough", raw, got)
		}
		if wasEscaped {
			t.Fatalf("Feed(0x%02X): wasEscaped=true; want false for raw byte", raw)
		}
		if admin.Kind != AdminEventNone {
			t.Fatalf("Feed(0x%02X): admin.Kind=%v; want AdminEventNone", raw, admin.Kind)
		}
	}
}

// TestFeed_A9_01_DecodesToAA pins the canonical
// `wire 0xA9 0x01 → logical 0xAA, wasEscaped=true` rule under Feed.
// No admin event on the happy path.
func TestFeed_A9_01_DecodesToAA(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	_, ok, _, admin := d.Feed(0xA9, t0)
	if ok {
		t.Fatal("Feed(0xA9): ok=true on lead; want false")
	}
	if admin.Kind != AdminEventNone {
		t.Fatalf("Feed(0xA9): admin.Kind=%v; want AdminEventNone", admin.Kind)
	}
	if !d.HasPendingEscape() {
		t.Fatal("HasPendingEscape() = false after lead; want true")
	}

	got, ok, wasEscaped, admin := d.Feed(0x01, t0.Add(1*time.Millisecond))
	if !ok || got != 0xAA || !wasEscaped {
		t.Fatalf("Feed(0x01): got=(0x%02X, ok=%v, wasEscaped=%v); want (0xAA, true, true)", got, ok, wasEscaped)
	}
	if admin.Kind != AdminEventNone {
		t.Fatalf("Feed(0x01) completion: admin.Kind=%v; want AdminEventNone", admin.Kind)
	}
	if d.HasPendingEscape() {
		t.Fatal("HasPendingEscape() = true after completion; want false")
	}
}

// TestFeed_A9_00_DecodesToA9 pins the
// `wire 0xA9 0x00 → logical 0xA9, wasEscaped=true` rule under Feed.
func TestFeed_A9_00_DecodesToA9(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	if _, ok, _, _ := d.Feed(0xA9, t0); ok {
		t.Fatal("Feed(0xA9): ok=true on lead; want false")
	}
	got, ok, wasEscaped, admin := d.Feed(0x00, t0.Add(1*time.Millisecond))
	if !ok || got != 0xA9 || !wasEscaped {
		t.Fatalf("Feed(0x00): got=(0x%02X, ok=%v, wasEscaped=%v); want (0xA9, true, true)", got, ok, wasEscaped)
	}
	if admin.Kind != AdminEventNone {
		t.Fatalf("admin.Kind=%v; want AdminEventNone", admin.Kind)
	}
}

// TestFeed_A9_AA_Absorbed_Then_01 pins the core v8 AA-absorption
// path: a 0xA9 lead followed by one 0xAA injection then 0x01 must
// (a) absorb the 0xAA without emission, (b) accept the 0x01 as the
// real completion and emit 0xAA wasEscaped=true.
func TestFeed_A9_AA_Absorbed_Then_01(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	if _, ok, _, _ := d.Feed(0xA9, t0); ok {
		t.Fatal("Feed(0xA9): ok=true; want false")
	}
	// AA-injection within budget.
	_, ok, _, admin := d.Feed(0xAA, t0.Add(1*time.Millisecond))
	if ok {
		t.Fatal("Feed(0xAA) after lead: ok=true; want false (absorbed)")
	}
	if admin.Kind != AdminEventNone {
		t.Fatalf("absorption admin.Kind=%v; want AdminEventNone", admin.Kind)
	}
	if d.AbsorbedCount() != 1 {
		t.Fatalf("AbsorbedCount=%d after 1 AA; want 1", d.AbsorbedCount())
	}

	// Now the real completion.
	got, ok, wasEscaped, admin := d.Feed(0x01, t0.Add(2*time.Millisecond))
	if !ok || got != 0xAA || !wasEscaped {
		t.Fatalf("Feed(0x01) post-absorb: got=(0x%02X, ok=%v, wasEscaped=%v); want (0xAA, true, true)", got, ok, wasEscaped)
	}
	if admin.Kind != AdminEventNone {
		t.Fatalf("completion admin.Kind=%v; want AdminEventNone", admin.Kind)
	}
	if d.AbsorbedCount() != 0 {
		t.Fatalf("AbsorbedCount=%d after completion; want 0 (reset)", d.AbsorbedCount())
	}
}

// TestFeed_A9_MaxAA_Then_01 exercises the upper edge of the
// absorption budget: 8 AAs absorbed, then 0x01 must still complete
// cleanly. Verifies MaxAaAbsorptionsPerEscapePair == 8 is the
// "inclusive ceiling" — absorb 8, then complete on the 9th call.
func TestFeed_A9_MaxAA_Then_01(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	if _, ok, _, _ := d.Feed(0xA9, t0); ok {
		t.Fatal("Feed(0xA9): ok=true; want false")
	}
	for i := 0; i < MaxAaAbsorptionsPerEscapePair; i++ {
		_, ok, _, admin := d.Feed(0xAA, t0.Add(time.Duration(i+1)*time.Millisecond))
		if ok {
			t.Fatalf("Feed(0xAA) iter %d: ok=true; want false (absorbed)", i)
		}
		if admin.Kind != AdminEventNone {
			t.Fatalf("Feed(0xAA) iter %d: admin.Kind=%v; want None", i, admin.Kind)
		}
	}
	if d.AbsorbedCount() != MaxAaAbsorptionsPerEscapePair {
		t.Fatalf("AbsorbedCount=%d after %d AAs; want %d",
			d.AbsorbedCount(), MaxAaAbsorptionsPerEscapePair, MaxAaAbsorptionsPerEscapePair)
	}

	// 9th byte is the real completion.
	got, ok, wasEscaped, admin := d.Feed(0x01, t0.Add(20*time.Millisecond))
	if !ok || got != 0xAA || !wasEscaped {
		t.Fatalf("Feed(0x01) post-max-absorb: got=(0x%02X, ok=%v, wasEscaped=%v); want (0xAA, true, true)", got, ok, wasEscaped)
	}
	if admin.Kind != AdminEventNone {
		t.Fatalf("completion admin.Kind=%v; want None", admin.Kind)
	}
}

// TestFeed_BudgetExhausted_AA_Over9 pins the count-bound failure:
// 9 consecutive AAs (one over the budget) yields
// AdminEventEscapeBudgetExhausted; the 9th AA AND all 8 absorbed
// AAs AND the 0xA9 are ALL dropped — emit nothing.
func TestFeed_BudgetExhausted_AA_Over9(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	if _, ok, _, _ := d.Feed(0xA9, t0); ok {
		t.Fatal("Feed(0xA9): ok=true; want false")
	}
	for i := 0; i < MaxAaAbsorptionsPerEscapePair; i++ {
		_, _, _, admin := d.Feed(0xAA, t0.Add(time.Duration(i+1)*time.Millisecond))
		if admin.Kind != AdminEventNone {
			t.Fatalf("Feed(0xAA) iter %d: premature admin event %v", i, admin.Kind)
		}
	}

	// 9th AA — exceeds budget.
	got, ok, wasEscaped, admin := d.Feed(0xAA, t0.Add(20*time.Millisecond))
	if ok {
		t.Fatalf("Feed(over-budget 0xAA): ok=true; want false (drop, emit nothing)")
	}
	if got != 0 {
		t.Errorf("decoded=0x%02X; want 0 (sentinel) on drop", got)
	}
	if wasEscaped {
		t.Error("wasEscaped=true; want false on drop")
	}
	if admin.Kind != AdminEventEscapeBudgetExhausted {
		t.Fatalf("admin.Kind=%v; want AdminEventEscapeBudgetExhausted", admin.Kind)
	}
	if admin.Absorbed != MaxAaAbsorptionsPerEscapePair {
		t.Errorf("admin.Absorbed=%d; want %d", admin.Absorbed, MaxAaAbsorptionsPerEscapePair)
	}
	if d.HasPendingEscape() {
		t.Error("decoder still pending after exhaustion; want cleared")
	}
	if d.AbsorbedCount() != 0 {
		t.Errorf("AbsorbedCount=%d; want 0 after exhaustion", d.AbsorbedCount())
	}

	// Resume: next byte decodes cleanly.
	got, ok, _, admin = d.Feed(0x55, t0.Add(30*time.Millisecond))
	if !ok || got != 0x55 || admin.Kind != AdminEventNone {
		t.Errorf("resume Feed(0x55): got=(0x%02X, ok=%v, admin.Kind=%v); want (0x55, true, None)",
			got, ok, admin.Kind)
	}
}

// TestFeed_InvalidSecondByte pins the malformed-pair drop path: a
// 0xA9 followed by 0xFF (not 0x00/0x01/0xAA) yields
// AdminEventEscapeRecovery; both bytes are dropped.
func TestFeed_InvalidSecondByte(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	if _, ok, _, _ := d.Feed(0xA9, t0); ok {
		t.Fatal("Feed(0xA9): ok=true; want false")
	}
	got, ok, wasEscaped, admin := d.Feed(0xFF, t0.Add(1*time.Millisecond))
	if ok {
		t.Fatal("Feed(0xFF) after lead: ok=true; want false (drop)")
	}
	if got != 0 || wasEscaped {
		t.Errorf("got=(0x%02X, wasEscaped=%v); want (0, false)", got, wasEscaped)
	}
	if admin.Kind != AdminEventEscapeRecovery {
		t.Fatalf("admin.Kind=%v; want AdminEventEscapeRecovery", admin.Kind)
	}
	if d.HasPendingEscape() {
		t.Error("decoder still pending after recovery; want cleared")
	}

	// Resume.
	got, ok, _, admin = d.Feed(0x55, t0.Add(2*time.Millisecond))
	if !ok || got != 0x55 || admin.Kind != AdminEventNone {
		t.Errorf("resume Feed(0x55): got=(0x%02X, ok=%v, admin.Kind=%v); want (0x55, true, None)",
			got, ok, admin.Kind)
	}
}

// TestFeed_WallClockCap_BeyondTimeout pins the 32 ms timeout
// behavior: a 0xA9 followed by a byte > EscapePendingTimeout later
// yields AdminEventEscapePendingTimeout; the current byte is
// re-processed in NORMAL state (data-preserving recovery).
func TestFeed_WallClockCap_BeyondTimeout(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	if _, ok, _, _ := d.Feed(0xA9, t0); ok {
		t.Fatal("Feed(0xA9): ok=true; want false")
	}
	// Advance past the cap by 1 ms.
	tLate := t0.Add(EscapePendingTimeout + 1*time.Millisecond)

	got, ok, wasEscaped, admin := d.Feed(0x55, tLate)
	if !ok {
		t.Fatal("Feed(0x55) post-timeout: ok=false; want true (re-processed in NORMAL)")
	}
	if got != 0x55 {
		t.Errorf("decoded=0x%02X; want 0x55 (re-processed plain byte)", got)
	}
	if wasEscaped {
		t.Error("wasEscaped=true; want false on plain re-processed byte")
	}
	if admin.Kind != AdminEventEscapePendingTimeout {
		t.Fatalf("admin.Kind=%v; want AdminEventEscapePendingTimeout", admin.Kind)
	}
	if admin.Duration <= EscapePendingTimeout {
		t.Errorf("admin.Duration=%v; want > %v", admin.Duration, EscapePendingTimeout)
	}
	if d.HasPendingEscape() {
		t.Error("decoder still pending after timeout; want cleared")
	}
}

// TestFeed_WallClockCap_BoundaryExact pins the strict-greater-than
// semantics: exactly EscapePendingTimeout elapsed does NOT trigger
// the cap (only `> EscapePendingTimeout` does). Documents the
// boundary behavior.
func TestFeed_WallClockCap_BoundaryExact(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	if _, ok, _, _ := d.Feed(0xA9, t0); ok {
		t.Fatal("Feed(0xA9): ok=true; want false")
	}
	// Advance to EXACTLY the cap.
	tBoundary := t0.Add(EscapePendingTimeout)
	got, ok, wasEscaped, admin := d.Feed(0x01, tBoundary)
	if !ok || got != 0xAA || !wasEscaped {
		t.Fatalf("Feed(0x01) at boundary: got=(0x%02X, ok=%v, wasEscaped=%v); want clean completion", got, ok, wasEscaped)
	}
	if admin.Kind != AdminEventNone {
		t.Fatalf("admin.Kind=%v; want AdminEventNone at boundary", admin.Kind)
	}
}

// TestFeed_WallClockCap_TimeoutBytePreservedAsEscape exercises a
// subtle case: the timeout fires, the current byte is re-processed
// in NORMAL state, and the current byte happens to BE a new 0xA9.
// The byte must start a new pending state (not get dropped).
func TestFeed_WallClockCap_TimeoutBytePreservedAsEscape(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	if _, ok, _, _ := d.Feed(0xA9, t0); ok {
		t.Fatal("Feed(0xA9): ok=true; want false")
	}
	tLate := t0.Add(EscapePendingTimeout + 1*time.Millisecond)
	got, ok, wasEscaped, admin := d.Feed(0xA9, tLate)
	if ok {
		t.Fatal("Feed(0xA9) post-timeout: ok=true; want false (new lead, accumulating)")
	}
	if got != 0 || wasEscaped {
		t.Errorf("got=(0x%02X, wasEscaped=%v); want (0, false) on new lead", got, wasEscaped)
	}
	if admin.Kind != AdminEventEscapePendingTimeout {
		t.Fatalf("admin.Kind=%v; want AdminEventEscapePendingTimeout", admin.Kind)
	}
	if !d.HasPendingEscape() {
		t.Error("HasPendingEscape=false after re-entering on new 0xA9; want true")
	}

	// Now complete the NEW pair cleanly.
	got, ok, wasEscaped, admin = d.Feed(0x01, tLate.Add(1*time.Millisecond))
	if !ok || got != 0xAA || !wasEscaped {
		t.Fatalf("new pair completion: got=(0x%02X, ok=%v, wasEscaped=%v); want (0xAA, true, true)", got, ok, wasEscaped)
	}
	if admin.Kind != AdminEventNone {
		t.Errorf("admin.Kind=%v; want None on completion", admin.Kind)
	}
}

// TestFeed_Reset_ClearsAbsorbedAndLeadTime pins that Reset() wipes
// the v8 fields (absorbedCount, leadObservedAt) in addition to the
// legacy `escape` flag.
func TestFeed_Reset_ClearsAbsorbedAndLeadTime(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}
	t0 := time.Unix(0, 0)

	_, _, _, _ = d.Feed(0xA9, t0)
	_, _, _, _ = d.Feed(0xAA, t0.Add(1*time.Millisecond))
	if !d.HasPendingEscape() || d.AbsorbedCount() != 1 {
		t.Fatalf("precondition: pending=%v absorbed=%d; want true, 1",
			d.HasPendingEscape(), d.AbsorbedCount())
	}

	d.Reset()
	if d.HasPendingEscape() {
		t.Error("HasPendingEscape() = true after Reset(); want false")
	}
	if d.AbsorbedCount() != 0 {
		t.Errorf("AbsorbedCount=%d after Reset(); want 0", d.AbsorbedCount())
	}

	// Verify the reset cleared the wall-clock anchor: a fresh lead
	// followed by an immediate AAa across what WOULD have been a
	// timeout from the OLD anchor must NOT trigger the cap.
	_, ok, _, _ := d.Feed(0xA9, t0.Add(100*time.Millisecond))
	if ok {
		t.Fatal("fresh lead post-reset: ok=true; want false")
	}
	got, ok, wasEscaped, admin := d.Feed(0x01, t0.Add(101*time.Millisecond))
	if !ok || got != 0xAA || !wasEscaped {
		t.Fatalf("completion post-reset: got=(0x%02X, ok=%v, wasEscaped=%v); want (0xAA, true, true)", got, ok, wasEscaped)
	}
	if admin.Kind != AdminEventNone {
		t.Errorf("admin.Kind=%v; want None (reset cleared the timeout anchor)", admin.Kind)
	}
}

// TestFeed_Push_LegacyContract_WithAbsorption pins the Push wrapper:
// Push silently absorbs AAs (it does NOT report admin events) and
// completes cleanly when a valid 0x00/0x01 follows. This is a v8
// BEHAVIOR CHANGE relative to pre-v8 Push, which would have
// returned an err on Push(0xA9)+Push(0xAA). Documented intentional
// change — the AA absorption is now a SUCCESS path.
func TestFeed_Push_LegacyContract_WithAbsorption(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}

	if _, ok, _, err := d.Push(0xA9); err != nil || ok {
		t.Fatalf("Push(0xA9): err=%v ok=%v; want nil false", err, ok)
	}
	// First AA — absorbed silently. Pre-v8 this would have errored.
	_, ok, _, err := d.Push(0xAA)
	if err != nil {
		t.Errorf("Push(0xAA) after lead: err=%v; want nil (v8 absorbs)", err)
	}
	if ok {
		t.Error("Push(0xAA) after lead: ok=true; want false (absorbed)")
	}
	// Then real completion.
	got, ok, wasEscaped, err := d.Push(0x01)
	if err != nil || !ok || got != 0xAA || !wasEscaped {
		t.Fatalf("Push(0x01) completion: got=(0x%02X, ok=%v, wasEscaped=%v, err=%v); want (0xAA, true, true, nil)",
			got, ok, wasEscaped, err)
	}
}

// TestFeed_Push_BudgetExhausted_StillReturnsErr pins that the
// legacy Push wrapper still maps budget-exhaustion to err for
// callers that depend on the err signal.
func TestFeed_Push_BudgetExhausted_StillReturnsErr(t *testing.T) {
	t.Parallel()

	d := &EbusEscapeDecoder{}

	_, _, _, _ = d.Push(0xA9)
	for i := 0; i < MaxAaAbsorptionsPerEscapePair; i++ {
		if _, _, _, err := d.Push(0xAA); err != nil {
			t.Fatalf("Push(0xAA) iter %d: err=%v; want nil", i, err)
		}
	}
	// Over-budget.
	_, ok, _, err := d.Push(0xAA)
	if err == nil {
		t.Fatal("Push(over-budget 0xAA): err=nil; want non-nil")
	}
	if ok {
		t.Error("Push(over-budget 0xAA): ok=true; want false")
	}
	if d.HasPendingEscape() {
		t.Error("decoder still pending after exhaustion via Push; want cleared")
	}
}
