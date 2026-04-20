// M4c1 RED — PR-B: FrameDecoder / LocalResponderDispatcher absence.
//
// See `helianthus-execution-plans/ebus-standard-l7-services-w16-26.implementing/
// decisions/m4b2-responder-go-no-go.md` §6.1.

package responder

import (
	"errors"
	"testing"
)

// TestM4c1_PRB_FrameDecoder_Exported asserts a FrameDecoder type is
// published by the responder package. Fails today — type absent.
func TestM4c1_PRB_FrameDecoder_Exported(t *testing.T) {
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
	// End-state: call dispatcher.Handle(frame{ZZ: 0x10}) with local=0x71 and
	// assert the FSM stayed in Idle and no outbound byte was queued.
	fsm := NewFSM()
	disp := NewLocalResponderDispatcher(0x71, fsm)
	frame := DecodedFrame{Source: 0x03, Target: 0x10, Primary: 0x07, Secondary: 0x04}
	accepted, err := disp.Handle(frame)
	if err != nil {
		t.Fatalf("M4c1 PR-B: non-local ZZ surfaced error = %v, want nil (silent drop)", err)
	}
	if accepted {
		t.Fatalf("M4c1 PR-B: LocalResponderDispatcher accepted non-local ZZ (want drop)")
	}
	if fsm.State() != StateIdle {
		t.Fatalf("M4c1 PR-B: FSM advanced on non-local ZZ (state=%s, want StateIdle)", fsm.State())
	}
	if got := disp.Dropped(); got != 1 {
		t.Fatalf("M4c1 PR-B: dropped counter = %d, want 1", got)
	}
}

// TestM4c1_PRB_Dispatcher_Handle_PropagatesInvalidTransition asserts that
// when a local-addressed frame arrives while the FSM is not in StateIdle
// (e.g. duplicate/new inbound request before the previous exchange has
// completed), Handle surfaces ErrInvalidTransition instead of silently
// reporting `accepted=true`. Regression guard for Codex P1 finding on PR
// #140 (handle discarded the FSM error).
func TestM4c1_PRB_Dispatcher_Handle_PropagatesInvalidTransition(t *testing.T) {
	fsm := NewFSM()
	// Drive FSM out of StateIdle: Idle → AckReceived → ResponseSent.
	if _, err := fsm.OnInboundFrame(true); err != nil {
		t.Fatalf("setup: OnInboundFrame: %v", err)
	}
	if _, err := fsm.OnEmitResponse(); err != nil {
		t.Fatalf("setup: OnEmitResponse: %v", err)
	}
	if got := fsm.State(); got != StateResponseSent {
		t.Fatalf("setup: FSM state = %s, want StateResponseSent", got)
	}

	disp := NewLocalResponderDispatcher(0x71, fsm)
	frame := DecodedFrame{Source: 0x03, Target: 0x71, Primary: 0x07, Secondary: 0x04}
	accepted, err := disp.Handle(frame)
	if accepted {
		t.Fatalf("Handle accepted=true while FSM was StateResponseSent (want accepted=false)")
	}
	if err == nil {
		t.Fatalf("Handle err = nil, want ErrInvalidTransition surfaced to caller")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Handle err = %v, want ErrInvalidTransition", err)
	}
	// Dropped counter MUST NOT increment on FSM rejection — the frame was
	// local (ZZ matched), not filtered by the bus-hygiene drop path.
	if got := disp.Dropped(); got != 0 {
		t.Fatalf("dropped counter = %d, want 0 (FSM reject is not a ZZ-filter drop)", got)
	}
}
