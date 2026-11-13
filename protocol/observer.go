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

// Numbering starts at 1: the zero BusEventKind matches no constant.
const (
	// BusEventArbitration reports the result of one arbitration attempt on
	// a transport that arbitrates; Outcome tells success from failure.
	BusEventArbitration BusEventKind = iota + 1
	// BusEventTX reports one byte written by the bus.
	BusEventTX
	// BusEventRX reports one byte read by the bus.
	BusEventRX
	// BusEventACK reports a positive acknowledgement. A successful
	// initiator-to-target attempt emits it for the target's acknowledgement
	// of the request (HasResponse false) and for the bus's acknowledgement
	// of the response (HasResponse true).
	BusEventACK
	// BusEventNACK reports a negative acknowledgement received or sent by
	// the bus, or a failure matching ErrNACK.
	BusEventNACK
	// BusEventTimeout reports a failure matching ErrTimeout.
	BusEventTimeout
	// BusEventCRCMismatch reports a CRC check that failed.
	BusEventCRCMismatch
	// BusEventEchoMismatch reports a written byte whose echo differed from
	// what was sent. The byte values can be equal: an encoding mismatch (a
	// structural byte echoed as a decoded logical byte, or a payload byte
	// echoed as a raw SYN) also counts. Byte holds the echo only when
	// ByteKnown is set, and EchoWasEscaped tells whether it arrived
	// escape-decoded.
	BusEventEchoMismatch
	// BusEventRetry is emitted when the retry policy allows another
	// attempt, before any wait that precedes it; the request can still end
	// during that wait. Retry carries the reason; TimeoutRetries and
	// NACKRetries carry the retry counters at that point.
	BusEventRetry
	// BusEventAttemptComplete reports one successful attempt, with the
	// response when the frame type has one.
	BusEventAttemptComplete
	// BusEventRequestComplete reports the end of a logical request; Outcome
	// is its overall result. It is not emitted on every exit: a request
	// that ends because its context or the bus's run context is done during
	// the fixed delay before a collision or adapter-reset retry returns
	// without it.
	BusEventRequestComplete
	// BusEventObserverFault is emitted to the observer right after one of
	// its callbacks returned an error or panicked; that failure is recorded
	// in Bus.ObserverFaultSnapshot. A failure of the callback that receives
	// this event is discarded: it is neither recorded nor reported again.
	BusEventObserverFault
	// BusEventAdapterReset reports a failure matching ErrAdapterReset.
	BusEventAdapterReset
)

// BusOutcomeClass is the bounded transaction outcome vocabulary exposed to
// observers. It intentionally stays smaller than wrapped Go error values.
type BusOutcomeClass uint8

// The classes map from the sentinel errors of this module's errors package
// with errors.Is; an error that matches none of them is BusOutcomeUnknown.
const (
	// BusOutcomeUnknown is the zero value and the class of any error that
	// matches no other class.
	BusOutcomeUnknown BusOutcomeClass = iota
	// BusOutcomeSuccess is the class of a nil error.
	BusOutcomeSuccess
	// BusOutcomeTimeout corresponds to ErrTimeout.
	BusOutcomeTimeout
	// BusOutcomeNACK corresponds to ErrNACK.
	BusOutcomeNACK
	// BusOutcomeCRCMismatch corresponds to ErrCRCMismatch.
	BusOutcomeCRCMismatch
	// BusOutcomeEchoMismatch is the class of BusEventEchoMismatch events
	// and of an ErrBusCollision error whose message reports an echo
	// mismatch.
	BusOutcomeEchoMismatch
	// BusOutcomeCollision is any other bus collision (ErrBusCollision).
	BusOutcomeCollision
	// BusOutcomeObserverFault marks BusEventObserverFault events.
	BusOutcomeObserverFault
	// BusOutcomeAdapterReset corresponds to ErrAdapterReset.
	BusOutcomeAdapterReset
)

// BusRetryReason identifies why the bus is retrying a logical request.
type BusRetryReason uint8

// Each reason follows the BusOutcomeClass of the error that caused the retry.
const (
	// BusRetryReasonUnknown is the zero value and the reason for a cause in
	// any class without its own reason.
	BusRetryReasonUnknown BusRetryReason = iota
	// BusRetryReasonTimeout corresponds to BusOutcomeTimeout.
	BusRetryReasonTimeout
	// BusRetryReasonNACK corresponds to BusOutcomeNACK.
	BusRetryReasonNACK
	// BusRetryReasonCRCMismatch corresponds to BusOutcomeCRCMismatch.
	BusRetryReasonCRCMismatch
	// BusRetryReasonCollision corresponds to BusOutcomeCollision and
	// BusOutcomeEchoMismatch.
	BusRetryReasonCollision
	// BusRetryReasonAdapterReset corresponds to BusOutcomeAdapterReset.
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

	// ByteKnown is true only when Byte carries a byte actually observed on
	// the wire. Events without an observed byte leave it false, so a
	// consumer can tell a missing byte from a real 0x00.
	ByteKnown bool

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
