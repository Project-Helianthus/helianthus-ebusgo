package protocol

import (
	"testing"
	"time"
)

// TestFrameAtomicV8TimeoutsAreSpecAligned guards against silent drift of
// the per-phase timeout values. The doc-of-record is
// helianthus-docs-ebus/architecture/adaptermux/frame-atomic-visibility-v8.md
// reading v6 §3 + v7 §1.5 + v8 §1.3 + v8 §1.5 (v8 is a delta over v7;
// the full phase table is canonically in v6 §3).
//
// If the design doc is updated, this test must be updated to match —
// any failure here means the const and the doc have drifted apart.
func TestFrameAtomicV8TimeoutsAreSpecAligned(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"InterByte (v6 §3)", FrameAtomicV8InterByteTimeout, 10 * time.Millisecond},
		{"WaitMasterAck (v8 §1.5)", FrameAtomicV8WaitMasterAckTimeout, 200 * time.Millisecond},
		{"WaitSlaveAck (v6 §3)", FrameAtomicV8WaitSlaveAckTimeout, 200 * time.Millisecond},
		{"WaitTerminatorSyn (v6 §3)", FrameAtomicV8WaitTerminatorSynTimeout, 100 * time.Millisecond},
		{"Arbitrating (v6 §3)", FrameAtomicV8ArbitratingTimeout, 50 * time.Millisecond},
		{"EscapePending (v8 §1.3)", FrameAtomicV8EscapePendingTimeout, 32 * time.Millisecond},
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
// budgets from v6 §3 / v8 §1.5 (NACK retx cap = 1) and v8 §1.3 / §5 / I4
// (escape AA budget = 8). These are integral invariants from the spec,
// not engineering choices that can drift.
func TestFrameAtomicV8IntegralBudgetsAreSpecAligned(t *testing.T) {
	t.Parallel()
	if FrameAtomicV8MaxNackRetxPerPhase != 1 {
		t.Fatalf("FrameAtomicV8MaxNackRetxPerPhase = %d, want 1 (per spec V1.3.1; v8 I6)",
			FrameAtomicV8MaxNackRetxPerPhase)
	}
	if FrameAtomicV8MaxAaAbsorptionsPerEscapePair != 8 {
		t.Fatalf("FrameAtomicV8MaxAaAbsorptionsPerEscapePair = %d, want 8 (per v8 §5 / I4)",
			FrameAtomicV8MaxAaAbsorptionsPerEscapePair)
	}
}

// TestFrameAtomicV8TimeoutOrdering captures the ordering relationships
// among the per-phase timeouts. These relationships are load-bearing for
// FSM correctness — for example, EscapePending must dominate single
// inter-byte timeouts so the escape decoder's hard cap fires before
// the classifier's inter-byte timer would in the worst case.
func TestFrameAtomicV8TimeoutOrdering(t *testing.T) {
	t.Parallel()

	// Per v8 §1.3, the 32 ms EscapePending bound is the literal v8 cap.
	// The doc rationale says "8 × τ_wire_byte ... extended slightly for
	// jitter tolerance" but the actual literal is 32 ms (with
	// τ_wire_byte = 4.17 ms, 8× = 33.36 ms, so the 32 ms cap fires
	// just before the theoretical absorption window completes — this
	// is a conservative early-cut, not a jitter-extension). The test
	// asserts the literal, not the approximate arithmetic.
	if FrameAtomicV8EscapePendingTimeout != 32*time.Millisecond {
		t.Fatalf("FrameAtomicV8EscapePendingTimeout = %v, want exactly 32 ms (v8 §1.3 literal)",
			FrameAtomicV8EscapePendingTimeout)
	}

	// Per v6 §3 and v8 §1.5: ACK-phase timeouts must dominate single
	// inter-byte timeouts so a slow-but-spec-compliant ACK doesn't
	// abort the phase.
	if FrameAtomicV8WaitMasterAckTimeout <= FrameAtomicV8InterByteTimeout {
		t.Fatalf("WaitMasterAck (%v) must be > InterByte (%v)",
			FrameAtomicV8WaitMasterAckTimeout, FrameAtomicV8InterByteTimeout)
	}
	if FrameAtomicV8WaitSlaveAckTimeout <= FrameAtomicV8InterByteTimeout {
		t.Fatalf("WaitSlaveAck (%v) must be > InterByte (%v)",
			FrameAtomicV8WaitSlaveAckTimeout, FrameAtomicV8InterByteTimeout)
	}

	// Per v6 §3 WAIT_TERMINATOR_SYN rationale ("allows up to two
	// missed AUTO-SYN slots"): WaitTerminatorSyn must cover at least
	// 2 × AUTO-SYN slot. AUTO-SYN cadence is ~35-45 ms per V1.3.1, so
	// 100 ms covers up to two slots comfortably. Assert >= 90 ms as
	// a safety floor.
	if FrameAtomicV8WaitTerminatorSynTimeout < 90*time.Millisecond {
		t.Fatalf("WaitTerminatorSyn = %v, want >= 90 ms (≈ 2 × AUTO-SYN slot)",
			FrameAtomicV8WaitTerminatorSynTimeout)
	}
}
