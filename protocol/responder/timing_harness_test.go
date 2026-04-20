// M4c1 RED — PR-B: timing harness.
//
// The responder must ACK an inbound for-local-responder frame within the
// eBUS turn-around budget (responder reply window). The exact upper bound will be
// pinned against BASV2 live bench during PR-B GREEN; the RED test uses a
// sentinel placeholder so the assertion is visible and fails today.
//
// What the harness measures end-state:
//   t0 = moment the final CRC byte of the inbound frame is parsed ok
//   t1 = moment the first ACK byte is handed to the transport write path
//   elapsed = t1 - t0
//
// End-state assertion: elapsed <= responderAckBudget.

package responder

import (
	"testing"
	"time"
)

// responderAckBudgetPlaceholder mirrors the production `responderAckBudget`
// constant for the RED→GREEN contract test. PR-B GREEN pinned the value
// to a literature-backed 15 ms placeholder (Vaillant eBUS target-response
// window per `_spike/m4b1-responder-feasibility.md` §4). Per decision
// doc §7.1(1) the operator MUST replace this with a measured BASV2 bench
// value before M4c2 IMPL GREEN (BENCH-REPLACE in `timing_harness.go`).
const responderAckBudgetPlaceholder time.Duration = 15 * time.Millisecond

func TestM4c1_PRB_TimingHarness_Exists(t *testing.T) {
	if responderExportRegistry == nil {
		t.Fatalf("M4c1 PR-B: timing harness not yet present — expected exported TimingHarness type")
	}
	if _, ok := responderExportRegistry["TimingHarness"]; !ok {
		t.Fatalf("M4c1 PR-B: TimingHarness type absent from protocol/responder")
	}
}

func TestM4c1_PRB_TimingHarness_MeasuresCRCToAckElapsed(t *testing.T) {
	// End-state harness shape:
	//   h := responder.NewTimingHarness()
	//   h.MarkInboundCRCOk(t0)
	//   h.MarkAckEmit(t1)
	//   elapsed := h.Elapsed()
	//   if elapsed > responderAckBudget { t.Fatalf(...) }
	t0 := time.Unix(0, 0)
	t1 := t0.Add(2 * time.Millisecond)
	h := NewTimingHarness()
	h.MarkInboundCRCOk(t0)
	h.MarkAckEmit(t1)
	elapsed, ok := h.Elapsed()
	if !ok {
		t.Fatalf("M4c1 PR-B: Elapsed() reported not-ok after both marks recorded")
	}
	if elapsed != 2*time.Millisecond {
		t.Fatalf("M4c1 PR-B: Elapsed() = %v, want 2ms", elapsed)
	}
	if !h.WithinBudget() {
		t.Fatalf("M4c1 PR-B: WithinBudget() = false with elapsed=2ms, budget=%v", h.Budget())
	}
}

// TestM4c1_PRB_TimingHarness_NegativeElapsed_IsInvalid locks the fail-closed
// contract: inverted timestamps (ackEmitAt < inboundCRCAt) must NOT be
// reported as within budget. Regression guard for the silent pass-through
// where a signed raw Sub() result landed under the upper-bound check and
// hid measurement-integrity bugs.
func TestM4c1_PRB_TimingHarness_NegativeElapsed_IsInvalid(t *testing.T) {
	// Inject inverted marks: ackEmitAt is BEFORE inboundCRCAt.
	t0 := time.Unix(0, int64(5*time.Millisecond))
	t1 := time.Unix(0, int64(1*time.Millisecond))
	h := NewTimingHarness()
	h.MarkInboundCRCOk(t0)
	h.MarkAckEmit(t1)
	elapsed, ok := h.Elapsed()
	if !ok {
		t.Fatalf("M4c1 PR-B: Elapsed() reported not-ok after both marks recorded")
	}
	if elapsed >= 0 {
		t.Fatalf("M4c1 PR-B: inverted marks should produce negative elapsed, got %v", elapsed)
	}
	if h.WithinBudget() {
		t.Fatalf("M4c1 PR-B: WithinBudget() = true with negative elapsed=%v — fail-closed invariant violated", elapsed)
	}
}

func TestM4c1_PRB_TimingHarness_BudgetAssertion_Placeholder(t *testing.T) {
	// Budget pinned to zero in RED; PR-B GREEN must replace
	// responderAckBudgetPlaceholder with a real, bench-measured constant
	// and update this assertion accordingly.
	if responderAckBudgetPlaceholder == 0 {
		t.Fatalf("M4c1 PR-B: responder ACK budget still at RED placeholder (0); PR-B GREEN must pin real value from BASV2 bench")
	}
}
