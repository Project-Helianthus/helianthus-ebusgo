// Package emulation provides deterministic, in-memory eBUS targets for
// tests. A Target answers request frames from an ordered list of rules; it
// opens no device, touches no transport and never transmits on a real bus.
// Emulated answers are test fixtures, not evidence of how a real device
// behaves.
//
// Time is virtual: callers pass the request time in RequestEvent, and
// Harness keeps its own clock that moves only through Advance or
// RunSequence. Nothing sleeps or reads the wall clock, so the same inputs
// always give the same responses.
//
// Target.Emulate requires the request's target address to equal the
// emulated address (ErrRequestTargetMismatch otherwise) and uses the first
// rule whose matcher accepts the frame (ErrNoMatchingRule when none does).
// A rule without a matcher or builder fails with ErrInvalidConfiguration.
// The planned delay is checked against the rule's TimingConstraints, or the
// target's defaults when the rule sets none, and fails with
// ErrTimingConstraint when it falls outside them; a zero maximum means no
// upper bound. The response swaps source and target, keeps the primary and
// secondary command bytes and carries a copy of the planned data.
//
// The constructors build targets from profiles: identify-only targets that
// answer the standard identification request, and preset profiles that can
// also answer a scan-identification request and caller-mapped commands.
// Constructors normalize the profile (trimmed names, truncated device
// identifiers, default delays and timing) and reject incomplete profiles
// with ErrInvalidConfiguration.
//
// Harness is not safe for concurrent use. History returns a deep copy of
// the recorded responses, so callers may modify it freely.
// ValidateResponseEnvelope checks recorded responses against an outer delay
// envelope.
package emulation
