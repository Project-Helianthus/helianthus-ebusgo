package protocol

import (
	"errors"
	"testing"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
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
