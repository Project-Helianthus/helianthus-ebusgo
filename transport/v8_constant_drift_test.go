package transport_test

import (
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// Phase 1 Step B1 (frame-atomic-visibility v8 §1.3 / §5, invariant
// I4): the transport package mirrors two v8 constants from the
// protocol package because production-code in transport cannot
// import protocol (circular dependency). External test code is the
// ONLY layer that can hold both packages and prove the mirror has
// not drifted.
//
// These tests are the drift guard. If they fail, either:
//   - protocol/frame_atomic_v8_timeouts.go changed and transport
//     was not updated, or
//   - transport/ebus_escape.go changed the mirror to a different
//     literal without updating the protocol source of truth.
//
// Either way, the mismatch is a bug: protocol is the source of
// truth (v8 design constants live there) and transport must
// faithfully mirror.

// TestV8DriftGuard_MaxAaAbsorptionsPerEscapePair asserts the
// AA-injection absorption budget matches across packages.
func TestV8DriftGuard_MaxAaAbsorptionsPerEscapePair(t *testing.T) {
	t.Parallel()

	if transport.MaxAaAbsorptionsPerEscapePair != protocol.FrameAtomicV8MaxAaAbsorptionsPerEscapePair {
		t.Fatalf("v8 constant drift: transport.MaxAaAbsorptionsPerEscapePair=%d, protocol.FrameAtomicV8MaxAaAbsorptionsPerEscapePair=%d; both must match (v8 §5 / I4)",
			transport.MaxAaAbsorptionsPerEscapePair,
			protocol.FrameAtomicV8MaxAaAbsorptionsPerEscapePair)
	}
}

// TestV8DriftGuard_EscapePendingTimeout asserts the 32 ms
// wall-clock cap matches across packages.
func TestV8DriftGuard_EscapePendingTimeout(t *testing.T) {
	t.Parallel()

	if transport.EscapePendingTimeout != protocol.FrameAtomicV8EscapePendingTimeout {
		t.Fatalf("v8 constant drift: transport.EscapePendingTimeout=%v, protocol.FrameAtomicV8EscapePendingTimeout=%v; both must match (v8 §1.3)",
			transport.EscapePendingTimeout,
			protocol.FrameAtomicV8EscapePendingTimeout)
	}
}
