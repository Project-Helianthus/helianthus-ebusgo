package protocol

import (
	"errors"
	"fmt"
	"sync"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

const (
	defaultCollisionEchoWindow      = 200 * time.Millisecond
	defaultCollisionHistoryCapacity = 128
	defaultCollisionGraceWindow     = 750 * time.Millisecond
)

// ArbitrationFailureReason classifies arbitration failure conditions.
type ArbitrationFailureReason string

const (
	ArbitrationFailureReasonAddressCollision ArbitrationFailureReason = "address_collision"
)

// ErrArbitrationFailed indicates a transmit attempt while collision state is active.
type ErrArbitrationFailed struct {
	Reason ArbitrationFailureReason
}

func (e *ErrArbitrationFailed) Error() string {
	return fmt.Sprintf("ebus: arbitration failed (%s)", e.Reason)
}

func (e *ErrArbitrationFailed) Unwrap() error {
	return ebuserrors.ErrBusCollision
}

// CollisionReason describes why a foreign frame was treated as a collision.
type CollisionReason string

const (
	CollisionReasonForeignSameSource  CollisionReason = "foreign_same_source"
	CollisionReasonObservedWhileMuted CollisionReason = "observed_while_muted"
)

// EventCollisionDetected is emitted when a collision is detected.
type EventCollisionDetected struct {
	Initiator byte
	Frame     Frame
	Timestamp time.Time
	Reason    CollisionReason
}

// CollisionMonitorConfig controls matching windows and history size.
type CollisionMonitorConfig struct {
	EchoWindow       time.Duration
	HistoryCapacity  int
	GraceAfterRejoin time.Duration
}

// CollisionMonitor identifies foreign frames that reuse our initiator source.
type CollisionMonitor struct {
	mu sync.Mutex

	cfg CollisionMonitorConfig
	now func() time.Time

	initiator byte
	muted     bool

	oldInitiator         byte
	oldInitiatorValid    bool
	oldInitiatorGraceEnd time.Time

	history []txRecord

	collisionActive bool
	lastEvent       *EventCollisionDetected
}

type txRecord struct {
	frame     Frame
	timestamp time.Time
}

// NewCollisionMonitor builds a monitor with sensible defaults.
func NewCollisionMonitor(cfg CollisionMonitorConfig) *CollisionMonitor {
	cfg = normalizeCollisionMonitorConfig(cfg)
	return &CollisionMonitor{
		cfg: cfg,
		now: time.Now,
	}
}

// SetInitiator updates the active initiator and starts grace handling for the prior address.
func (m *CollisionMonitor) SetInitiator(initiator byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if m.initiator != 0 && m.initiator != initiator {
		m.oldInitiator = m.initiator
		m.oldInitiatorValid = true
		m.oldInitiatorGraceEnd = now.Add(m.cfg.GraceAfterRejoin)
	}
	m.initiator = initiator
	m.collisionActive = false
	m.lastEvent = nil
}

// SetMuted controls listen-only mode.
func (m *CollisionMonitor) SetMuted(muted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.muted = muted
}

// RecordTX records a locally transmitted frame or fails fast while collision is active.
func (m *CollisionMonitor) RecordTX(frame Frame) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.collisionActive {
		return &ErrArbitrationFailed{Reason: ArbitrationFailureReasonAddressCollision}
	}
	m.appendHistory(txRecord{
		frame:     cloneFrame(frame),
		timestamp: m.now(),
	})
	return nil
}

// ObserveRX checks whether an incoming frame indicates collision.
func (m *CollisionMonitor) ObserveRX(frame Frame) *EventCollisionDetected {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if m.oldInitiatorValid && now.After(m.oldInitiatorGraceEnd) {
		m.oldInitiatorValid = false
	}
	if m.oldInitiatorValid && frame.Source == m.oldInitiator {
		return nil
	}
	if m.initiator == 0 || frame.Source != m.initiator {
		return nil
	}
	if m.muted {
		return m.activateCollision(now, frame, CollisionReasonObservedWhileMuted)
	}
	if m.matchesRecentTX(frame, now) {
		return nil
	}
	return m.activateCollision(now, frame, CollisionReasonForeignSameSource)
}

// CollisionActive reports whether the monitor currently blocks new transmissions.
func (m *CollisionMonitor) CollisionActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.collisionActive
}

// LastEvent returns the most recent detected collision event.
func (m *CollisionMonitor) LastEvent() *EventCollisionDetected {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastEvent == nil {
		return nil
	}
	event := *m.lastEvent
	return &event
}

func (m *CollisionMonitor) activateCollision(now time.Time, frame Frame, reason CollisionReason) *EventCollisionDetected {
	m.collisionActive = true
	event := &EventCollisionDetected{
		Initiator: m.initiator,
		Frame:     cloneFrame(frame),
		Timestamp: now,
		Reason:    reason,
	}
	m.lastEvent = event
	return event
}

func (m *CollisionMonitor) matchesRecentTX(frame Frame, now time.Time) bool {
	for index := len(m.history) - 1; index >= 0; index-- {
		record := m.history[index]
		if now.Sub(record.timestamp) > m.cfg.EchoWindow {
			continue
		}
		if framesEqual(record.frame, frame) {
			return true
		}
	}
	return false
}

func (m *CollisionMonitor) appendHistory(record txRecord) {
	m.history = append(m.history, record)
	if len(m.history) <= m.cfg.HistoryCapacity {
		return
	}
	excess := len(m.history) - m.cfg.HistoryCapacity
	m.history = append([]txRecord(nil), m.history[excess:]...)
}

func normalizeCollisionMonitorConfig(cfg CollisionMonitorConfig) CollisionMonitorConfig {
	if cfg.EchoWindow <= 0 {
		cfg.EchoWindow = defaultCollisionEchoWindow
	}
	if cfg.HistoryCapacity <= 0 {
		cfg.HistoryCapacity = defaultCollisionHistoryCapacity
	}
	if cfg.GraceAfterRejoin <= 0 {
		cfg.GraceAfterRejoin = defaultCollisionGraceWindow
	}
	return cfg
}

func cloneFrame(frame Frame) Frame {
	cloned := frame
	cloned.Data = append([]byte(nil), frame.Data...)
	return cloned
}

func framesEqual(left, right Frame) bool {
	if left.Source != right.Source ||
		left.Target != right.Target ||
		left.Primary != right.Primary ||
		left.Secondary != right.Secondary {
		return false
	}
	if len(left.Data) != len(right.Data) {
		return false
	}
	for index := range left.Data {
		if left.Data[index] != right.Data[index] {
			return false
		}
	}
	return true
}

// IsErrArbitrationFailed reports whether the error is ErrArbitrationFailed.
func IsErrArbitrationFailed(err error) bool {
	var target *ErrArbitrationFailed
	return errors.As(err, &target)
}
