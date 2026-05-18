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
	// BusEventCollision is emitted when bus.Send observes evidence of an
	// arbitration-class loss DURING a send sequence — i.e. distinct from
	// the BusEventArbitration emitted at StartArbitration time. The
	// canonical case is first-byte-after-arbitration foreign-initiator
	// echo (round-7 / batch-26): we wrote raw=X expecting echo=X, but
	// the wire echoed a master-class byte (per AddressClassMaster) that
	// is not X. Pre-round-7 this fired BusEventEchoMismatch and was
	// routed by the gateway P10 classifier to the echo_mismatch bucket;
	// post-round-7 it routes through BusOutcomeCollision so retry
	// behavior (which only fires on Is(ErrBusCollision) WITHOUT the
	// "echo mismatch" substring) classifies arbitration losses
	// distinctly from genuine echo mismatches.
	BusEventCollision
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

	// EchoWasEscaped is set on BusEventEchoMismatch events emitted at
	// the sendSymbolWithEcho echo-value check (bus.go ~1054). It
	// records whether the offending echo byte arrived through the
	// transport's escape-decoder (i.e. wire pair `A9 01` → logical
	// 0xAA with the WasEscaped flag set) versus arriving as a raw
	// wire byte. Consumers (gateway P10 echo_mismatch subclass
	// classifier) can use this to split the byte-value-only
	// `pre_echo_syn` label into:
	//
	//   - pre_echo_syn_raw           (EchoWasEscaped=false, real
	//                                 wire SYN — mux SYN-suppression
	//                                 leak class)
	//   - pre_echo_syn_escaped_data  (EchoWasEscaped=true, third-
	//                                 party frame payload 0xAA that
	//                                 was wire-encoded `A9 01` and
	//                                 the transport unescaped it)
	//
	// Added 2026-05-17 batch-23 (echo_mismatch round-4 classifier
	// split). Zero-value on events where the flag is not meaningful
	// (e.g. non-EchoMismatch kinds, or echo events where the byte
	// arrived through a non-escape-flagged transport).
	EchoWasEscaped bool

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
