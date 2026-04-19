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

// responderAckBudgetPlaceholder is the RED sentinel. PR-B GREEN replaces
// this with an empirically measured budget (BASV2 live bench, per §6.1 of
// the M4b2 decision doc). The zero value guarantees the RED test fails
// regardless of any accidental fast path.
const responderAckBudgetPlaceholder time.Duration = 0

func TestM4c1_PRB_TimingHarness_Exists(t *testing.T) {
	if responderExportRegistry == nil {
		t.Fatalf("M4c1 PR-B: timing harness not yet present — expected exported TimingHarness type")
	}
	if _, ok := responderExportRegistry["TimingHarness"]; !ok {
		t.Fatalf("M4c1 PR-B: TimingHarness type absent from protocol/responder")
	}
}

func TestM4c1_PRB_TimingHarness_MeasuresCRCToAckElapsed(t *testing.T) {
	// End-state harness shape (pseudo):
	//   h := responder.NewTimingHarness()
	//   h.MarkInboundCRCOk(t0)
	//   h.MarkAckEmit(t1)
	//   elapsed := h.Elapsed()
	//   if elapsed > responderAckBudget { t.Fatalf(...) }
	t.Fatalf("M4c1 PR-B: TimingHarness.Elapsed() not implemented — CRC→ACK measurement unavailable")
}

func TestM4c1_PRB_TimingHarness_BudgetAssertion_Placeholder(t *testing.T) {
	// Budget pinned to zero in RED; PR-B GREEN must replace
	// responderAckBudgetPlaceholder with a real, bench-measured constant
	// and update this assertion accordingly.
	if responderAckBudgetPlaceholder == 0 {
		t.Fatalf("M4c1 PR-B: responder ACK budget still at RED placeholder (0); PR-B GREEN must pin real value from BASV2 bench")
	}
}
