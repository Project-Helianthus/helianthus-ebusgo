// Package responder implements the eBUS responder (ZZ-addressed reply) role:
// inbound frame decoding, local-responder dispatch, ACK / response /
// final-ACK FSM, and a timing harness measuring received-frame-CRC →
// responder-ACK-emit latency.
//
// M4c1 PR-B: GREEN-phase implementation per decision doc
// `helianthus-execution-plans/ebus-standard-l7-services-w16-26.implementing/
// decisions/m4b2-responder-go-no-go.md` §6.1.
//
// Scope (PR-B):
//   - FrameDecoder       — inbound frame parser (header + CRC validation)
//   - LocalResponderDispatcher — routes frames with matching ZZ; drops
//     non-local frames silently.
//   - FSM                — Idle → AckReceived → ResponseSent → {Idle |
//     retry | aborted} state machine.
//   - TimingHarness      — clock-injected CRC-to-ACK latency meter.
//
// The package consumes the PR-A-landed `transport.ResponderTransport`
// capability for outbound byte emission but does NOT reach into transport
// internals — all transport-specific logic stays at the transport boundary.
package responder

// responderExportRegistry publishes the PR-B exported surface so the RED
// contract tests (which consult the registry by name) can observe the
// end-state. The registry is populated from init() functions in the files
// below; the map itself is allocated lazily on first registration to keep
// declaration order independent of file compilation order.
var responderExportRegistry map[string]any

func registerExport(name string, value any) {
	if responderExportRegistry == nil {
		responderExportRegistry = make(map[string]any)
	}
	responderExportRegistry[name] = value
}
