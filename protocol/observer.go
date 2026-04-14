package protocol

// CollisionRetryResyncSYNCount is the number of SYN symbols the bus waits for
// after a collision before retrying. Gateway dedup timing validation uses this
// exported envelope rather than duplicating bus-internal constants.
const CollisionRetryResyncSYNCount = 2

// BusEventKind enumerates the observer event set reserved for observe-first.
//
// The bus emits these events in later milestones. This enum is frozen early so
// downstream consumers can build against one stable event vocabulary.
type BusEventKind uint8

const (
	BusEventArbitration BusEventKind = iota + 1
	BusEventTX
	BusEventRX
	BusEventACK
	BusEventNACK
	BusEventTimeout
	BusEventCRCMismatch
	BusEventEchoMismatch
	BusEventRetry
	BusEventAttemptComplete
	BusEventRequestComplete
	BusEventObserverFault
	BusEventAdapterReset
)

// BusOutcomeClass is the bounded transaction outcome vocabulary exposed to
// observers. It intentionally stays smaller than wrapped Go error values.
type BusOutcomeClass uint8

const (
	BusOutcomeUnknown BusOutcomeClass = iota
	BusOutcomeSuccess
	BusOutcomeTimeout
	BusOutcomeNACK
	BusOutcomeCRCMismatch
	BusOutcomeEchoMismatch
	BusOutcomeCollision
	BusOutcomeObserverFault
	BusOutcomeAdapterReset
)

// BusRetryReason identifies why the bus is retrying a logical request.
type BusRetryReason uint8

const (
	BusRetryReasonUnknown BusRetryReason = iota
	BusRetryReasonTimeout
	BusRetryReasonNACK
	BusRetryReasonCRCMismatch
	BusRetryReasonCollision
	BusRetryReasonAdapterReset
)

// BusEvent is the stable, TinyGo-safe observer payload used by later observe-
// first milestones. Slice fields alias bus-owned memory and are valid only for
// the duration of the callback. Observers that retain payloads must copy them.
//
// No time.Time, maps, or registry/watch descriptors appear here; downstream
// layers must resolve higher-level meaning out-of-band.
type BusEvent struct {
	Kind BusEventKind

	FrameType FrameType
	Outcome   BusOutcomeClass
	Retry     BusRetryReason

	Initiator byte
	Byte      byte

	Attempt        uint16
	TimeoutRetries uint16
	NACKRetries    uint16

	DurationMicros int64

	Request     Frame
	Response    Frame
	HasRequest  bool
	HasResponse bool
}

// BusObserver receives protocol-level observe-first bus events. The bus invokes
// observers synchronously from its own control flow in later milestones.
// Implementations must return quickly and must not call Bus.Send or
// Bus.RawTransportOp -- doing so will deadlock the bus run loop since events
// are dispatched synchronously. Implementations may not retain request/response
// data without copying it first.
//
// Panic containment is an explicit bus policy: once wiring lands, each observer
// invocation is recovered independently so a single panic cannot terminate the
// bus loop or silently disable future observer callbacks.
type BusObserver interface {
	OnBusEvent(BusEvent) error
}

// BusObserverFunc adapts a function to BusObserver. A nil function is a safe
// no-op so zero-value configuration remains allocation-free on the hot path.
type BusObserverFunc func(BusEvent) error

// OnBusEvent dispatches the event to fn when non-nil.
func (fn BusObserverFunc) OnBusEvent(event BusEvent) error {
	if fn == nil {
		return nil
	}
	return fn(event)
}

// BusRetryEnvelope exports the bounded retry/backoff inputs that downstream
// dedup timing validation needs. It is derived from BusConfig plus the stable
// collision resync constant above.
type BusRetryEnvelope struct {
	InitiatorTarget         RetryPolicy
	InitiatorInitiator      RetryPolicy
	CollisionResyncSYNCount int
}

// ObserverFaultSnapshot exposes the bounded observer-fault state accumulated by
// the bus. It is the explicit fallback signal when observer event delivery
// fails or panics, so downstream consumers can enter a conservative degraded
// mode instead of assuming active-match evidence remained healthy.
type ObserverFaultSnapshot struct {
	Count       uint64
	LastKind    BusEventKind
	LastOutcome BusOutcomeClass
	LastPanic   bool
	LastError   string
}

// RetryEnvelope returns the stable retry envelope for this config.
func (c BusConfig) RetryEnvelope() BusRetryEnvelope {
	return BusRetryEnvelope{
		InitiatorTarget:         c.InitiatorTarget,
		InitiatorInitiator:      c.InitiatorInitiator,
		CollisionResyncSYNCount: CollisionRetryResyncSYNCount,
	}
}

// DefaultRetryEnvelope returns the stable retry envelope for DefaultBusConfig.
func DefaultRetryEnvelope() BusRetryEnvelope {
	return DefaultBusConfig().RetryEnvelope()
}
