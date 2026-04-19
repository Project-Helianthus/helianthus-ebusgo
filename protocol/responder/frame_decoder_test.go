// M4c1 RED — PR-B: FrameDecoder / LocalResponderDispatcher absence.
//
// See `helianthus-execution-plans/ebus-standard-l7-services-w16-26.implementing/
// decisions/m4b2-responder-go-no-go.md` §6.1.

package responder

import (
	"testing"
)

// TestM4c1_PRB_FrameDecoder_Exported asserts a FrameDecoder type is
// published by the responder package. Fails today — type absent.
func TestM4c1_PRB_FrameDecoder_Exported(t *testing.T) {
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	if responderExportRegistry == nil {
		t.Fatalf("M4c1 PR-B: protocol/responder package has no exported FrameDecoder yet")
	}
	if _, ok := responderExportRegistry["FrameDecoder"]; !ok {
		t.Fatalf("M4c1 PR-B: FrameDecoder type absent from protocol/responder")
	}
}

// TestM4c1_PRB_LocalResponderDispatcher_Exported asserts a
// LocalResponderDispatcher is published. Fails today.
func TestM4c1_PRB_LocalResponderDispatcher_Exported(t *testing.T) {
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	if responderExportRegistry == nil {
		t.Fatalf("M4c1 PR-B: protocol/responder package has no exported LocalResponderDispatcher yet")
	}
	if _, ok := responderExportRegistry["LocalResponderDispatcher"]; !ok {
		t.Fatalf("M4c1 PR-B: LocalResponderDispatcher type absent from protocol/responder")
	}
}

// TestM4c1_PRB_FSM_Exported asserts the ACK/response/final-ACK FSM is
// published. Fails today.
func TestM4c1_PRB_FSM_Exported(t *testing.T) {
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	if responderExportRegistry == nil {
		t.Fatalf("M4c1 PR-B: protocol/responder package has no exported FSM yet")
	}
	if _, ok := responderExportRegistry["FSM"]; !ok {
		t.Fatalf("M4c1 PR-B: FSM type absent from protocol/responder")
	}
}

// TestM4c1_PRB_LocalResponderAddressFilter_DropsNonLocalZZ asserts the
// inbound frame filter semantics: any frame whose ZZ (destination responder
// address) differs from the configured local responder address MUST be
// dropped without emitting an ACK or advancing the FSM. Stubbed — fails
// today because the harness is absent.
func TestM4c1_PRB_LocalResponderAddressFilter_DropsNonLocalZZ(t *testing.T) {
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	// End-state: call dispatcher.Handle(frame{ZZ: 0x10}) with local=0x71 and
	// assert the FSM stayed in Idle and no outbound byte was queued.
	// Until PR-B lands there is no dispatcher to call.
	t.Fatalf("M4c1 PR-B: LocalResponderDispatcher ZZ filter harness absent — end-state requires drop+no-ACK when ZZ != local responder addr")
}
