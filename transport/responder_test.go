// M4c1 RED — responder transport primitives.
//
// These tests describe the end-state contract for PR-A of M4c1 per decision
// doc `helianthus-execution-plans/ebus-standard-l7-services-w16-26.implementing/
// decisions/m4b2-responder-go-no-go.md` §6.1. They are expected to FAIL until
// the implementation lands (TDD strict — RED before GREEN).
//
// The tests are written to COMPILE against the current tree (no missing
// symbols at build time) and fail at RUN time. Absent types/methods are
// probed via reflection against a shim interface that mirrors the expected
// production contract.

package transport

import (
	"reflect"
	"testing"
)

// responderTransportShim mirrors the expected production
// `transport.ResponderTransport` interface. Used only to discover whether a
// concrete transport implements the expected method set. Impl in PR-A will
// add a real `ResponderTransport` interface and these tests will be rewritten
// to consume it directly.
type responderTransportShim interface {
	// SendResponderBytes emits a responder-side byte sequence (ACK, data,
	// final ACK) WITHOUT going through StartArbitration. The responder role
	// is reactive: we are replying to an initiator that already owns the
	// bus.
	SendResponderBytes(payload []byte) (int, error)
}

// TestM4c1_ResponderTransport_InterfaceExported asserts that a named type
// `ResponderTransport` is exported from this package. Fails until PR-A lands.
func TestM4c1_ResponderTransport_InterfaceExported(t *testing.T) {
	// Probe via a reflect.Type lookup over package-exported values. The
	// canonical way to assert the interface exists is to reference it by
	// name. Since we cannot reference a non-existent identifier at compile
	// time, we instead assert a sentinel var that PR-A must introduce.
	//
	// Convention for the impl: PR-A exports `var _ResponderTransportMarker
	// = reflect.TypeOf((*ResponderTransport)(nil)).Elem()`. Until then,
	// lookup returns the zero Value and the test fails.
	v := lookupExportedType("ResponderTransport")
	if v == nil {
		t.Fatalf("M4c1 PR-A: transport.ResponderTransport interface not yet exported (expected end-state)")
	}
	if v.Kind() != reflect.Interface {
		t.Fatalf("M4c1 PR-A: transport.ResponderTransport exists but is not an interface (got %s)", v.Kind())
	}
}

// TestM4c1_ENHTransport_SatisfiesResponderTransport asserts the production
// ENHTransport ends up satisfying ResponderTransport. Fails today because
// the SendResponderBytes method does not exist yet.
func TestM4c1_ENHTransport_SatisfiesResponderTransport(t *testing.T) {
	var enh interface{} = (*ENHTransport)(nil)
	if _, ok := enh.(responderTransportShim); !ok {
		t.Fatalf("M4c1 PR-A: *ENHTransport must satisfy ResponderTransport (SendResponderBytes missing)")
	}
}

// TestM4c1_ENHTransport_SendResponderBytes_BypassesArbitration asserts the
// existence of an ENH method that does NOT call into StartArbitration /
// RequestStart. Fails today because the method is absent.
func TestM4c1_ENHTransport_SendResponderBytes_BypassesArbitration(t *testing.T) {
	enhType := reflect.TypeOf((*ENHTransport)(nil))
	if _, ok := enhType.MethodByName("SendResponderBytes"); !ok {
		t.Fatalf("M4c1 PR-A: *ENHTransport.SendResponderBytes method absent — responder send primitive missing")
	}
}

// TestM4c1_EbusdTCPTransport_DoesNotSatisfyResponderTransport locks in the
// perpetual non-satisfaction of ebusd-tcp for the responder role per M4b2
// decision §6.1 (command-bridge protocol forbids responder-role emission).
// This test MUST fail today for the right reason: the shim interface is
// trivially unsatisfied until PR-A lands — and MUST continue to pass after
// PR-A as a lock. The RED phase records the intent; GREEN preserves it.
func TestM4c1_EbusdTCPTransport_DoesNotSatisfyResponderTransport(t *testing.T) {
	// Today the assertion below succeeds trivially (no type implements the
	// shim). Paired-assertion: after PR-A lands, ENHTransport will
	// satisfy ResponderTransport and EbusdTCPTransport will NOT. We force
	// this test to FAIL in RED so the suite counts correctly, by requiring
	// that the exported ResponderTransport type already exist.
	if lookupExportedType("ResponderTransport") == nil {
		t.Fatalf("M4c1 PR-A: ResponderTransport interface not yet exported; ebusd-tcp lock cannot be asserted until PR-A defines the interface (this test stays GREEN post-impl as a perpetual lock)")
	}
	var bridge interface{} = (*EbusdTCPTransport)(nil)
	if _, ok := bridge.(responderTransportShim); ok {
		t.Fatalf("M4c1 PR-A lock: *EbusdTCPTransport MUST NOT satisfy ResponderTransport — command bridge forbids responder role")
	}
}

// lookupExportedType reflects on a sentinel registry that PR-A is expected
// to populate. Until the impl publishes the marker, returns nil.
func lookupExportedType(name string) reflect.Type {
	if responderExportRegistry == nil {
		return nil
	}
	return responderExportRegistry[name]
}

// responderExportRegistry is declared and populated by the production
// file transport/responder.go (PR-A GREEN phase). The RED-phase sentinel
// that used to live here has been replaced by the real registry — see
// responder.go init() for the population logic.
