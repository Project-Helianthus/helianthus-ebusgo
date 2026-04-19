// Package transport — responder capability surface (M4c1 PR-A).
//
// This file defines the `ResponderTransport` interface, the transport-level
// capability that a transport must expose to participate in the ebus_standard
// responder lane per decision doc
// `helianthus-execution-plans/ebus-standard-l7-services-w16-26.implementing/
// decisions/m4b2-responder-go-no-go.md` §6.1 (option_go_transport_scoped).
//
// Capability semantics:
//   - ENH / ENS transports GO (they expose the raw byte write path and own
//     arbitration through a separate `StartArbitration` / `RequestStart`
//     surface, so a responder emission path that bypasses arbitration is
//     cleanly expressible).
//   - ebusd-tcp is BLOCKED (perpetual lock per M4b2 §3): the command-bridge
//     protocol does not expose a responder-role emission primitive and
//     forbids the local gateway from synthesising responder bytes.
//
// Design intent: the `SendResponderBytes` method writes a responder-direction
// byte sequence (e.g. ACK, DATA, final ACK) WITHOUT invoking
// `StartArbitration` — the caller has already observed the initiator header
// and is reacting within the eBUS target-response window. Arbitration is
// owned by the remote initiator; the local responder never requests the bus.
package transport

import "reflect"

// ResponderTransport is the capability surface for responder-role emission.
//
// Implementations MUST NOT invoke StartArbitration / RequestStart from
// within SendResponderBytes — this is a reactive send path, not an
// initiator path. Implementations MUST be safe to call concurrently with
// ReadByte / ReadEvent (i.e. follow the established transport mutex
// discipline).
type ResponderTransport interface {
	// SendResponderBytes emits a responder-direction byte sequence on the
	// bus, bypassing arbitration. Returns the number of payload bytes
	// accepted by the underlying wire (not any wire-level encoding
	// expansion such as ENH request/byte pairs) and an error on partial
	// or failed emission.
	SendResponderBytes(payload []byte) (int, error)
}

// responderExportRegistry is a small sentinel registry of exported
// responder-related types. Reflection-based discovery
// (see TestM4c1_ResponderTransport_InterfaceExported) consults this map
// to confirm ResponderTransport is exported and is an interface type.
// The production responder dispatcher consumes ResponderTransport
// directly — the registry exists only for the compile/test contract.
var responderExportRegistry = map[string]reflect.Type{
	"ResponderTransport": reflect.TypeOf((*ResponderTransport)(nil)).Elem(),
}
