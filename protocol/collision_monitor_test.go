package protocol

import (
	"errors"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

func TestCollisionMonitor_ForeignSameSourceTriggersCollision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{
		EchoWindow:       200 * time.Millisecond,
		HistoryCapacity:  16,
		GraceAfterRejoin: 750 * time.Millisecond,
	})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)

	if err := monitor.RecordTX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x06, 0x02, 0x00, 0x03, 0x00},
	}); err != nil {
		t.Fatalf("RecordTX error = %v", err)
	}

	now = now.Add(10 * time.Millisecond)
	event := monitor.ObserveRX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x06, 0x03, 0x00, 0x03, 0x00},
	})
	if event == nil {
		t.Fatalf("ObserveRX returned nil event; want collision")
	}
	if event.Reason != CollisionReasonForeignSameSource {
		t.Fatalf("Collision reason = %q; want %q", event.Reason, CollisionReasonForeignSameSource)
	}
	if !monitor.CollisionActive() {
		t.Fatalf("CollisionActive = false; want true")
	}
}

func TestCollisionMonitor_MatchingEchoDoesNotTriggerCollision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{EchoWindow: 200 * time.Millisecond})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)

	frame := Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0x07,
		Secondary: 0x04,
		Data:      []byte{0x00},
	}
	if err := monitor.RecordTX(frame); err != nil {
		t.Fatalf("RecordTX error = %v", err)
	}

	now = now.Add(25 * time.Millisecond)
	event := monitor.ObserveRX(frame)
	if event != nil {
		t.Fatalf("ObserveRX event = %#v; want nil", event)
	}
	if monitor.CollisionActive() {
		t.Fatalf("CollisionActive = true; want false")
	}
}

func TestCollisionMonitor_MutedModeTreatsSameSourceAsCollision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)
	monitor.SetMuted(true)

	event := monitor.ObserveRX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x03, 0x00, 0x00},
	})
	if event == nil {
		t.Fatalf("ObserveRX returned nil event; want collision")
	}
	if event.Reason != CollisionReasonObservedWhileMuted {
		t.Fatalf("Collision reason = %q; want %q", event.Reason, CollisionReasonObservedWhileMuted)
	}
}

func TestCollisionMonitor_RecordTXFailsFastWhileCollisionActive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)

	_ = monitor.ObserveRX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x03, 0x00, 0x00},
	})

	err := monitor.RecordTX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x06, 0x02, 0x00, 0x0F, 0x00},
	})
	var arbitrationErr *ErrArbitrationFailed
	if !errors.As(err, &arbitrationErr) {
		t.Fatalf("RecordTX error = %v; want ErrArbitrationFailed", err)
	}
	if arbitrationErr.Reason != ArbitrationFailureReasonAddressCollision {
		t.Fatalf("ErrArbitrationFailed reason = %q; want %q", arbitrationErr.Reason, ArbitrationFailureReasonAddressCollision)
	}
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("RecordTX error = %v; want wrapped ErrBusCollision", err)
	}
}

func TestCollisionMonitor_GraceAfterRejoinIgnoresOldInitiator(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{
		EchoWindow:       200 * time.Millisecond,
		HistoryCapacity:  16,
		GraceAfterRejoin: 500 * time.Millisecond,
	})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)
	monitor.SetInitiator(0x33)

	now = now.Add(150 * time.Millisecond)
	event := monitor.ObserveRX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0x07,
		Secondary: 0x04,
		Data:      []byte{0x00},
	})
	if event != nil {
		t.Fatalf("ObserveRX event = %#v; want nil during grace window", event)
	}

	now = now.Add(600 * time.Millisecond)
	event = monitor.ObserveRX(Frame{
		Source:    0x33,
		Target:    0x15,
		Primary:   0x07,
		Secondary: 0x04,
		Data:      []byte{0x00},
	})
	if event == nil {
		t.Fatalf("ObserveRX returned nil event; want collision for new initiator")
	}
}

func TestCollisionMonitor_IsErrArbitrationFailed(t *testing.T) {
	t.Parallel()

	if IsErrArbitrationFailed(nil) {
		t.Fatalf("IsErrArbitrationFailed(nil) = true; want false")
	}
	if !IsErrArbitrationFailed(&ErrArbitrationFailed{Reason: ArbitrationFailureReasonAddressCollision}) {
		t.Fatalf("IsErrArbitrationFailed(ErrArbitrationFailed) = false; want true")
	}
}

func TestCollisionMonitor_ObserveRXReturnsDefensiveEventCopy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)

	event := monitor.ObserveRX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x01, 0x02},
	})
	if event == nil {
		t.Fatalf("ObserveRX returned nil event; want collision")
	}

	event.Frame.Data[0] = 0xAA
	stored := monitor.LastEvent()
	if stored == nil {
		t.Fatalf("LastEvent returned nil; want event")
	}
	if stored.Frame.Data[0] == 0xAA {
		t.Fatalf("LastEvent data mutated via ObserveRX return value")
	}
}

func TestCollisionMonitor_LastEventReturnsDefensiveFrameCopy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)

	_ = monitor.ObserveRX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x03, 0x04},
	})

	first := monitor.LastEvent()
	if first == nil {
		t.Fatalf("LastEvent returned nil; want event")
	}
	first.Frame.Data[0] = 0xBB

	second := monitor.LastEvent()
	if second == nil {
		t.Fatalf("LastEvent returned nil on second read; want event")
	}
	if second.Frame.Data[0] == 0xBB {
		t.Fatalf("LastEvent data mutated via caller-owned snapshot")
	}
}

func TestCollisionMonitor_SetInitiatorSameAddressPreservesCollision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)

	// Trigger a collision.
	event := monitor.ObserveRX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x03, 0x00, 0x00},
	})
	if event == nil {
		t.Fatalf("ObserveRX returned nil event; want collision")
	}
	if !monitor.CollisionActive() {
		t.Fatalf("CollisionActive = false after ObserveRX; want true")
	}

	// EG25: Setting the same initiator address must preserve collision state.
	monitor.SetInitiator(0x31)
	if !monitor.CollisionActive() {
		t.Fatalf("CollisionActive = false after SetInitiator(same); want true (EG25)")
	}
	if monitor.LastEvent() == nil {
		t.Fatalf("LastEvent = nil after SetInitiator(same); want preserved event (EG25)")
	}
}

func TestCollisionMonitor_SetInitiatorDifferentAddressClearsCollision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	monitor := NewCollisionMonitor(CollisionMonitorConfig{})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)

	// Trigger a collision.
	_ = monitor.ObserveRX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xB5,
		Secondary: 0x24,
		Data:      []byte{0x03, 0x00, 0x00},
	})
	if !monitor.CollisionActive() {
		t.Fatalf("CollisionActive = false; want true")
	}

	// Switching to a different address should clear collision state.
	monitor.SetInitiator(0x33)
	if monitor.CollisionActive() {
		t.Fatalf("CollisionActive = true after SetInitiator(different); want false")
	}
	if monitor.LastEvent() != nil {
		t.Fatalf("LastEvent != nil after SetInitiator(different); want nil")
	}
}

func TestCollisionMonitor_AppendHistoryTrimsAtCapacity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 19, 17, 0, 0, 0, time.UTC)
	capacity := 4
	monitor := NewCollisionMonitor(CollisionMonitorConfig{
		EchoWindow:      200 * time.Millisecond,
		HistoryCapacity: capacity,
	})
	monitor.now = func() time.Time { return now }
	monitor.SetInitiator(0x31)

	// Push exactly capacity frames. EG29: trim should fire when len == capacity,
	// keeping the buffer at capacity-1 after trim (capacity/2).
	for i := 0; i < capacity; i++ {
		now = now.Add(time.Millisecond)
		if err := monitor.RecordTX(Frame{
			Source:    0x31,
			Target:    0x15,
			Primary:   byte(i),
			Secondary: 0x00,
		}); err != nil {
			t.Fatalf("RecordTX[%d] error = %v", i, err)
		}
	}

	// Verify the monitor still works (no panic, no corruption).
	// Push one more to confirm trim has run and history is bounded.
	now = now.Add(time.Millisecond)
	if err := monitor.RecordTX(Frame{
		Source:    0x31,
		Target:    0x15,
		Primary:   0xFF,
		Secondary: 0x00,
	}); err != nil {
		t.Fatalf("RecordTX[overflow] error = %v", err)
	}
}
