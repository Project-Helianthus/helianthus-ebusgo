package engine

import "github.com/Project-Helianthus/helianthus-ebusgo/protocol"

// EngineCollaborator is the protocol decision seam. The transport stores and
// matches what the collaborator returns and does not classify register
// operands, admit key types, or recognize an identity scan on its own.
type EngineCollaborator interface {
	ResponseClassForPassive(event PassiveClassifiedEvent, outcome DedupOutcomeClass) DedupResponseClass
	ResponseClassForActive(event protocol.BusEvent) DedupResponseClass
	FamilyPolicy(scope ObserveFirstTrafficScope, request protocol.Frame, responseClass DedupResponseClass, observation WatchObservation, flags ObserveFirstFeatureFlagView) ObserveFirstFamilyPolicy
	PassiveWatchKeyFromEvent(event PassiveClassifiedEvent) (WatchKey, bool)
	PassiveWatchKeyFromFrame(frame protocol.Frame) (WatchKey, bool)
	RequestIntentFromFrame(frame protocol.Frame) ObserveFirstRequestIntent
	IsScanProbeRaw(raw []byte) bool
	IsScanTimeout(request protocol.Frame) bool
	CloneWatchKey(key WatchKey) (WatchKey, error)
	ValidateB555WatchKey(key WatchKey) error
}
