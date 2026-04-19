// M4c1 RED — PR-B: ACK / response / final-ACK FSM.
//
// End-state state machine (per eBUS initiator-responder transaction):
//
//   Idle
//     └─(inbound-frame-for-local-responder, CRC ok)──▶ AckReceived
//     └─(inbound-frame-for-local-responder, CRC bad)─▶ NackReceived ──▶ Idle
//   AckReceived
//     └─(emit ACK)──▶ ResponseSent (after emitting response payload)
//   ResponseSent
//     └─(initiator final ACK)──▶ Idle
//     └─(initiator NACK, retries < N)──▶ AckReceived (re-send)
//     └─(initiator NACK, retries = N)──▶ Idle (aborted, error surfaced)
//
// This RED file locks in the state names. Fails today — FSM type absent.

package responder

import "testing"

// Expected state constant names, in declaration order. PR-B GREEN must
// export these as a named-int type `State` with String() values matching
// these identifiers.
var expectedFSMStates = []string{
	"StateIdle",
	"StateAckReceived",
	"StateNackReceived",
	"StateResponseSent",
}

func TestM4c1_PRB_FSM_States_Declared(t *testing.T) {
	if responderExportRegistry == nil {
		t.Fatalf("M4c1 PR-B: protocol/responder FSM state constants not declared yet")
	}
	for _, name := range expectedFSMStates {
		if _, ok := responderExportRegistry[name]; !ok {
			t.Fatalf("M4c1 PR-B: FSM state %q not declared (end-state requires %v)", name, expectedFSMStates)
		}
	}
}

func TestM4c1_PRB_FSM_Transition_IdleToAckReceived_OnValidInbound(t *testing.T) {
	// End-state: construct FSM in StateIdle, feed a valid for-local-responder
	// frame, assert transition to StateAckReceived.
	fsm := NewFSM()
	if fsm.State() != StateIdle {
		t.Fatalf("M4c1 PR-B: initial FSM state = %s, want StateIdle", fsm.State())
	}
	s, err := fsm.OnInboundFrame(true)
	if err != nil {
		t.Fatalf("M4c1 PR-B: OnInboundFrame(true) from Idle returned err: %v", err)
	}
	if s != StateAckReceived {
		t.Fatalf("M4c1 PR-B: Idle→? on valid inbound = %s, want StateAckReceived", s)
	}
}

func TestM4c1_PRB_FSM_Transition_AckReceivedToResponseSent_OnEmit(t *testing.T) {
	fsm := NewFSM()
	if _, err := fsm.OnInboundFrame(true); err != nil {
		t.Fatalf("M4c1 PR-B: priming to AckReceived failed: %v", err)
	}
	s, err := fsm.OnEmitResponse()
	if err != nil {
		t.Fatalf("M4c1 PR-B: OnEmitResponse from AckReceived returned err: %v", err)
	}
	if s != StateResponseSent {
		t.Fatalf("M4c1 PR-B: AckReceived→? on emit = %s, want StateResponseSent", s)
	}
}

func TestM4c1_PRB_FSM_Transition_ResponseSentToIdle_OnFinalAck(t *testing.T) {
	fsm := NewFSM()
	if _, err := fsm.OnInboundFrame(true); err != nil {
		t.Fatalf("prime OnInboundFrame: %v", err)
	}
	if _, err := fsm.OnEmitResponse(); err != nil {
		t.Fatalf("prime OnEmitResponse: %v", err)
	}
	s, err := fsm.OnInitiatorFinalAck()
	if err != nil {
		t.Fatalf("M4c1 PR-B: OnInitiatorFinalAck from ResponseSent returned err: %v", err)
	}
	if s != StateIdle {
		t.Fatalf("M4c1 PR-B: ResponseSent→? on final ACK = %s, want StateIdle", s)
	}
}

func TestM4c1_PRB_FSM_Transition_ResponseSentToAckReceived_OnInitiatorNack_Retry(t *testing.T) {
	fsm := NewFSM()
	fsm.MaxNackRetries = 3
	if _, err := fsm.OnInboundFrame(true); err != nil {
		t.Fatalf("prime OnInboundFrame: %v", err)
	}
	if _, err := fsm.OnEmitResponse(); err != nil {
		t.Fatalf("prime OnEmitResponse: %v", err)
	}
	s, err := fsm.OnInitiatorNack()
	if err != nil {
		t.Fatalf("M4c1 PR-B: OnInitiatorNack within budget returned err: %v", err)
	}
	if s != StateAckReceived {
		t.Fatalf("M4c1 PR-B: ResponseSent→? on NACK (within budget) = %s, want StateAckReceived", s)
	}
}

func TestM4c1_PRB_FSM_Transition_ResponseSentToIdle_OnInitiatorNack_Exhausted(t *testing.T) {
	fsm := NewFSM()
	fsm.MaxNackRetries = 1
	if _, err := fsm.OnInboundFrame(true); err != nil {
		t.Fatalf("prime OnInboundFrame: %v", err)
	}
	if _, err := fsm.OnEmitResponse(); err != nil {
		t.Fatalf("prime OnEmitResponse: %v", err)
	}
	s, err := fsm.OnInitiatorNack()
	if err == nil {
		t.Fatalf("M4c1 PR-B: OnInitiatorNack with retries=1 should exhaust, got no err")
	}
	if s != StateIdle {
		t.Fatalf("M4c1 PR-B: ResponseSent→? on NACK (exhausted) = %s, want StateIdle", s)
	}
}
