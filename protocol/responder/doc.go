// Package responder implements the eBUS responder (ZZ-addressed reply) role:
// inbound frame decoding, local-responder dispatch, ACK / response /
// final-ACK FSM, and a timing harness measuring received-frame-CRC →
// responder-ACK-emit latency.
//
// M4c1: implementation stub, tests fail until impl. PR-B ships the runtime.
package responder

// responderExportRegistry is the RED-phase sentinel. PR-B impl must populate
// it (via an init() in frame_decoder.go / fsm.go / dispatcher.go) with the
// expected type names so the RED tests can detect the end-state.
//
// Left nil today so all M4c1 PR-B tests fail.
var responderExportRegistry map[string]any
