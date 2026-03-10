package protocol_test

import (
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

func TestBusObserverFuncNilIsNoop(t *testing.T) {
	t.Parallel()

	var fn protocol.BusObserverFunc
	if err := fn.OnBusEvent(protocol.BusEvent{Kind: protocol.BusEventTX}); err != nil {
		t.Fatalf("nil BusObserverFunc returned error = %v; want nil", err)
	}
}

func TestDefaultRetryEnvelopeMatchesDefaultBusConfig(t *testing.T) {
	t.Parallel()

	cfg := protocol.DefaultBusConfig()
	got := protocol.DefaultRetryEnvelope()
	if got.InitiatorTarget != cfg.InitiatorTarget {
		t.Fatalf("InitiatorTarget envelope = %+v; want %+v", got.InitiatorTarget, cfg.InitiatorTarget)
	}
	if got.InitiatorInitiator != cfg.InitiatorInitiator {
		t.Fatalf("InitiatorInitiator envelope = %+v; want %+v", got.InitiatorInitiator, cfg.InitiatorInitiator)
	}
	if got.CollisionResyncSYNCount != protocol.CollisionRetryResyncSYNCount {
		t.Fatalf("CollisionResyncSYNCount = %d; want %d", got.CollisionResyncSYNCount, protocol.CollisionRetryResyncSYNCount)
	}
}

func TestBusConfigObserverFieldDefaultsNil(t *testing.T) {
	t.Parallel()

	cfg := protocol.DefaultBusConfig()
	if cfg.Observer != nil {
		t.Fatalf("DefaultBusConfig().Observer = %#v; want nil", cfg.Observer)
	}
}
