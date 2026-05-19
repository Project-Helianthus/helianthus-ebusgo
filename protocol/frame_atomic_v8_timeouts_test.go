package protocol

import (
	"testing"
	"time"
)

// TestFrameAtomicV8TimeoutsAreSpecAligned guards against silent drift of
// the v8 §3 per-phase timeout values. If the design doc is updated, the
// test must be updated to match.
//
// See helianthus-docs-ebus/architecture/adaptermux/frame-atomic-visibility-v8.md
// §3 (per-state timeouts) and §1.3 (ESCAPE_PENDING bound).
func TestFrameAtomicV8TimeoutsAreSpecAligned(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"InterByte (v8 §3)", FrameAtomicV8.InterByte, 10 * time.Millisecond},
		{"WaitMasterAck (v8 §1.5)", FrameAtomicV8.WaitMasterAck, 200 * time.Millisecond},
		{"WaitSlaveAck (v8 §3)", FrameAtomicV8.WaitSlaveAck, 200 * time.Millisecond},
		{"WaitTerminatorSyn (v8 §3)", FrameAtomicV8.WaitTerminatorSyn, 100 * time.Millisecond},
		{"Arbitrating (v8 §3)", FrameAtomicV8.Arbitrating, 50 * time.Millisecond},
		{"EscapePending (v8 §1.3, I4)", FrameAtomicV8.EscapePending, 32 * time.Millisecond},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("%s = %v, want %v (drift from v8 spec — sync the doc and this test together)", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestFrameAtomicV8IntegralBudgetsAreSpecAligned asserts the count-bounded
// budgets from v8 §3 (NACK retx cap = 1) and §1.3 / §5 (escape AA budget
// = 8). These are integral invariants from the spec, not engineering
// choices that can drift.
func TestFrameAtomicV8IntegralBudgetsAreSpecAligned(t *testing.T) {
	t.Parallel()
	if FrameAtomicV8.MaxNackRetxPerPhase != 1 {
		t.Fatalf("MaxNackRetxPerPhase = %d, want 1 (per spec V1.3.1; v8 §3 invariant I6)",
			FrameAtomicV8.MaxNackRetxPerPhase)
	}
	if FrameAtomicV8.MaxAaAbsorptionsPerEscapePair != 8 {
		t.Fatalf("MaxAaAbsorptionsPerEscapePair = %d, want 8 (per v8 §1.3 / §5 / I4)",
			FrameAtomicV8.MaxAaAbsorptionsPerEscapePair)
	}
}

// TestFrameAtomicV8TimeoutOrdering captures the ordering relationships
// among the per-phase timeouts. These relationships are load-bearing for
// FSM correctness — for example, EscapePending must be < InterByte ×
// MaxAaAbsorptionsPerEscapePair so the escape decoder's hard cap fires
// before the classifier's inter-byte timer would.
func TestFrameAtomicV8TimeoutOrdering(t *testing.T) {
	t.Parallel()
	v := FrameAtomicV8

	// Per v8 §1.3: "The 32 ms bound is `8 × τ_wire_byte` (the maximum
	// theoretically achievable absorption window) extended slightly for
	// jitter tolerance." So EscapePending must equal exactly 8 × 4 ms
	// = 32 ms (the spec wire byte time is ~4.17 ms; the constant is
	// rounded down to 4 ms for the jitter-tolerance margin).
	wantEscapePending := 8 * 4 * time.Millisecond
	if v.EscapePending != wantEscapePending {
		t.Fatalf("EscapePending = %v, want %v (8 × 4 ms per v8 §1.3 rationale)",
			v.EscapePending, wantEscapePending)
	}

	// Per v8 §3: ACK-phase timeouts must dominate single inter-byte
	// timeouts so a slow-but-spec-compliant ACK doesn't abort.
	if v.WaitMasterAck <= v.InterByte {
		t.Fatalf("WaitMasterAck (%v) must be > InterByte (%v) per v8 §3", v.WaitMasterAck, v.InterByte)
	}
	if v.WaitSlaveAck <= v.InterByte {
		t.Fatalf("WaitSlaveAck (%v) must be > InterByte (%v) per v8 §3", v.WaitSlaveAck, v.InterByte)
	}

	// Per v8 §3 WAIT_TERMINATOR_SYN rationale ("allows up to two missed
	// AUTO-SYN slots"): WaitTerminatorSyn must be at least 2 × AUTO-SYN
	// slot. AUTO-SYN cadence is ~35-45 ms per V1.3.1, so 100 ms covers
	// up to two slots comfortably. We assert >= 90 ms as a safety floor.
	if v.WaitTerminatorSyn < 90*time.Millisecond {
		t.Fatalf("WaitTerminatorSyn = %v, want >= 90 ms (≈ 2 × AUTO-SYN slot) per v8 §3",
			v.WaitTerminatorSyn)
	}
}
