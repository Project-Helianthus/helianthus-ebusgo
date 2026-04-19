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
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
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
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	// End-state: construct FSM in StateIdle, feed a valid for-local-responder
	// frame, assert transition to StateAckReceived.
	t.Fatalf("M4c1 PR-B: FSM transition harness absent — Idle→AckReceived on valid inbound")
}

func TestM4c1_PRB_FSM_Transition_AckReceivedToResponseSent_OnEmit(t *testing.T) {
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	t.Fatalf("M4c1 PR-B: FSM transition harness absent — AckReceived→ResponseSent on payload emit")
}

func TestM4c1_PRB_FSM_Transition_ResponseSentToIdle_OnFinalAck(t *testing.T) {
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	t.Fatalf("M4c1 PR-B: FSM transition harness absent — ResponseSent→Idle on initiator final ACK")
}

func TestM4c1_PRB_FSM_Transition_ResponseSentToAckReceived_OnInitiatorNack_Retry(t *testing.T) {
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	t.Fatalf("M4c1 PR-B: FSM retry transition harness absent — ResponseSent→AckReceived on NACK within retry budget")
}

func TestM4c1_PRB_FSM_Transition_ResponseSentToIdle_OnInitiatorNack_Exhausted(t *testing.T) {
	t.Skip("M4c1 PR-B impl pending — see issue/138 PR-B dispatch")
	t.Fatalf("M4c1 PR-B: FSM abort transition harness absent — ResponseSent→Idle on NACK with retries exhausted")
}
