package protocol

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

const defaultQueueCapacity = 64

// Pre-allocated hot-path errors avoid fmt.Errorf allocations on every call.
// Only errors without dynamic data (hex values, addresses) are pre-allocated.
var (
	errUnknownFrameType        = fmt.Errorf("bus send unknown frame type: %w", ebuserrors.ErrInvalidPayload)
	errCommandAckLoopExhausted = fmt.Errorf("command ack loop exited without ack: %w", ebuserrors.ErrTimeout)
)

type RetryPolicy struct {
	TimeoutRetries int
	NACKRetries    int
}

type BusConfig struct {
	InitiatorTarget    RetryPolicy
	InitiatorInitiator RetryPolicy
	Observer           BusObserver

	// ReconnectRetries is the maximum number of transport reconnection
	// attempts when timeout retries are exhausted. Each successful reconnect
	// resets the per-request timeout retry budget. Set to 0 to disable
	// reconnection (default: 3).
	ReconnectRetries int
	// ReconnectDelay is the stabilization delay before each reconnect
	// attempt, giving the adapter time to finish rebooting (default: 2s).
	ReconnectDelay time.Duration
}

func DefaultBusConfig() BusConfig {
	return BusConfig{
		InitiatorTarget: RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    1,
		},
		InitiatorInitiator: RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    1,
		},
		ReconnectRetries: 3,
		ReconnectDelay:   2 * time.Second,
	}
}

type busRequest struct {
	frame Frame
	ctx   context.Context
	resp  chan busResult

	// transportOp, when non-nil, marks this as a raw transport operation
	// rather than a bus frame send. The run loop calls fn(transport) instead
	// of handleRequest. Used by RawTransportOp for INFO queries.
	transportOp        func(transport.RawTransport) error
	transportOpResp    chan error
	transportOpStarted atomic.Bool
}

type busResult struct {
	frame *Frame
	err   error
}

type arbitrationTransport interface {
	StartArbitration(initiator byte) error
}

type arbitrationSourceBehavior interface {
	ArbitrationSendsSource() bool
}

type collisionRecoveryLifecycle interface {
	transport.CollisionRecoveryLifecycle
}

// Bus orchestrates prioritized frame sending and transaction matching.
type Bus struct {
	transport transport.RawTransport
	config    BusConfig

	queueMu sync.Mutex
	queue   *priorityQueue
	notify  chan struct{}
	closed  bool

	startMu sync.Mutex
	started bool

	observerFaultMu sync.Mutex
	observerFault   ObserverFaultSnapshot

	outCap             int
	unescapedTransport bool // true when transport delivers pre-unescaped bytes (ENH)

	// batch-26 round-9 (2026-05-18): bounded-absorption counters for the
	// payload-0xAA echo path. When the gateway writes a payload byte
	// equal to SymbolSyn (0xAA, wire-escaped as `A9 01`) and a real wire
	// SYN arrives at the echo position with WasEscaped=false, the wire
	// SYN is bus-idle interference that fired BEFORE the adapter's
	// buffered escape pair reached the wire (TCP latency between
	// gateway→adapter→wire). Per the user's ENS-transmit-token invariant,
	// while the gateway holds the transmit token, AUTO-SYNs from the wire
	// must be DROPPED rather than propagated as echo failures. The fix
	// absorbs up to maxPayloadAaAutoSynDrains intervening wire SYNs and
	// retries the echo read; if the real escape-decoded payload echo
	// arrives within the cap, the transaction succeeds normally with no
	// error. These three counters surface drain frequency, recovery
	// success, and cap-exhaustion for downstream observability.
	payloadAaAutoSynAbsorbed       atomic.Uint64
	payloadAaAutoSynRecovered      atomic.Uint64
	payloadAaAutoSynDrainExhausted atomic.Uint64

	// Phase 1 Step B4 (frame-atomic-v8 §1.8 / §1.12, retained per v8 I8):
	// activeEchoWaits is a bus-wide phase predicate counter — it is >0
	// whenever ONE OR MORE goroutines on this Bus are inside the active
	// echo-wait phase of sendRawWithEcho (between a successful raw-byte
	// Write and the resolution of its echo). It is NOT goroutine-local;
	// the predicate inSendRawWithEchoActiveEchoWait() answers "is any
	// goroutine on this Bus currently in echo wait?", not "did THIS
	// caller arrive via sendRawWithEcho?". For the only current call
	// site (round-9 absorb entry, also inside sendRawWithEcho), this
	// distinction is moot — the answer is always true. The predicate
	// exists to document the phase invariant and to give the Prometheus
	// alert a runtime hook; future call sites that try to count round-9
	// fires from outside sendRawWithEcho would need a different
	// per-call mechanism (e.g. an explicit token argument).
	//
	// round9AbsorbEntered counts how many times the round-9 AUTO-SYN
	// absorb predicate fired while the active-echo-wait predicate held.
	// The counter is deliberately named neutrally: this code has no
	// notion of whether a proxy is mediating wire AUTO-SYNs. The
	// Prometheus alert HelianthusRound9FiredUnderProxy (defined in the
	// gateway deploy manifest — see helianthus-docs-ebus →
	// deployment/prometheus-alerts.md, NOT in this repo) is the layer
	// that AND-s this counter with the adaptermux classifier_mode label
	// to derive "round-9 fired while proxy SHOULD have filtered". Per
	// v8 I8 the round-9 absorb code itself is retained as the legacy
	// fallback for direct-adapter mode; expected steady-state under
	// classifier_mode == enforce is counter rate == 0.
	activeEchoWaits     atomic.Int32
	round9AbsorbEntered atomic.Uint64
}

// NewBus initializes a Bus with transport, config, and optional queue capacity.
func NewBus(tr transport.RawTransport, config BusConfig, queueCapacity int) *Bus {
	if queueCapacity <= 0 {
		queueCapacity = defaultQueueCapacity
	}
	unescaped := false
	if ea, ok := tr.(transport.EscapeAware); ok {
		unescaped = ea.BytesAreUnescaped()
	}
	return &Bus{
		transport:          tr,
		config:             config,
		queue:              newPriorityQueue(),
		notify:             make(chan struct{}, 1),
		outCap:             queueCapacity,
		unescapedTransport: unescaped,
	}
}

// Run starts the queue draining loop.
func (b *Bus) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	b.startMu.Lock()
	if b.started {
		b.startMu.Unlock()
		return
	}
	b.started = true
	b.startMu.Unlock()

	// Goroutine exits when ctx.Done() is closed; marks bus closed.
	go b.runLoop(ctx)
}

// Send enqueues a frame for prioritized sending and waits for the response.
func (b *Bus) Send(ctx context.Context, frame Frame) (*Frame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Phase C M-C5: enforce frame-type addressing contract before any
	// wire write. Rejection increments invalidFrameAddressTotal and
	// returns ErrInvalidFrameAddress wrapped with the failing clause.
	// Decision references: AD24 (additive Frame.FrameType + Validate),
	// AD26 (validator contract).
	if err := frame.Validate(); err != nil {
		atomic.AddUint64(&invalidFrameAddressTotal, 1)
		return nil, err
	}

	b.queueMu.Lock()
	if b.closed {
		b.queueMu.Unlock()
		return nil, ebuserrors.ErrTransportClosed
	}
	if b.queue.Len() >= b.outCap {
		b.queueMu.Unlock()
		return nil, ebuserrors.ErrQueueFull
	}
	request := &busRequest{
		frame: Frame{
			Source:    frame.Source,
			Target:    frame.Target,
			Primary:   frame.Primary,
			Secondary: frame.Secondary,
			Data:      append([]byte(nil), frame.Data...),
			FrameType: frame.FrameType,
		},
		ctx: ctx,
		// Capacity 1 to avoid blocking the run loop when delivering results.
		resp: make(chan busResult, 1),
	}
	b.queue.push(request)
	b.queueMu.Unlock()

	select {
	case b.notify <- struct{}{}:
	default:
	}

	select {
	case result := <-request.resp:
		return result.frame, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *Bus) runLoop(ctx context.Context) {
	for {
		request, ok := b.dequeue()
		if !ok {
			select {
			case <-ctx.Done():
				b.markClosed()
				b.drainQueue()
				return
			case <-b.notify:
				continue
			}
		}

		if request.transportOp != nil {
			// Raw transport operation (e.g. INFO query) — execute directly,
			// serialized with bus transactions since runLoop is single-threaded.
			// The callback must not call Bus.Send or RawTransportOp (deadlock).
			if err := b.contextError(ctx, request.ctx); err != nil {
				request.transportOpResp <- err
				continue
			}
			request.transportOpStarted.Store(true)
			err := request.transportOp(b.transport)
			request.transportOpResp <- err
			continue
		}

		result := b.handleRequest(ctx, request)
		request.resp <- result
	}
}

// RawTransportOp enqueues a function to execute directly on the raw transport,
// serialized with bus frame transactions. Use this for transport-level operations
// (e.g., ENH INFO queries) that bypass bus arbitration but must not interleave
// with active bus I/O.
//
// The function receives the Bus's transport and runs on the bus run loop goroutine.
// It must not call Bus.Send or other Bus methods (deadlock).
func (b *Bus) RawTransportOp(ctx context.Context, fn func(transport.RawTransport) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("ebus: raw transport op function is nil")
	}

	b.queueMu.Lock()
	if b.closed {
		b.queueMu.Unlock()
		return ebuserrors.ErrTransportClosed
	}
	if b.queue.Len() >= b.outCap {
		b.queueMu.Unlock()
		return ebuserrors.ErrQueueFull
	}
	op := &busRequest{
		ctx:             ctx,
		transportOp:     fn,
		transportOpResp: make(chan error, 1),
	}
	b.queue.push(op)
	b.queueMu.Unlock()

	select {
	case b.notify <- struct{}{}:
	default:
	}

	select {
	case err := <-op.transportOpResp:
		return err
	case <-ctx.Done():
		if op.transportOpStarted.Load() {
			return <-op.transportOpResp
		}
		return ctx.Err()
	}
}

func (b *Bus) handleRequest(runCtx context.Context, request *busRequest) busResult {
	if err := b.contextError(runCtx, request.ctx); err != nil {
		return busResult{err: err}
	}

	frame, err := b.sendWithRetries(runCtx, request)
	return busResult{frame: frame, err: err}
}

func (b *Bus) contextError(runCtx, reqCtx context.Context) error {
	if reqCtx != nil {
		if err := reqCtx.Err(); err != nil {
			return err
		}
	}
	if runCtx != nil {
		if err := runCtx.Err(); err != nil {
			return ebuserrors.ErrTransportClosed
		}
	}
	return nil
}

func (b *Bus) dequeue() (*busRequest, bool) {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()

	return b.queue.pop()
}

func (b *Bus) markClosed() {
	b.queueMu.Lock()
	b.closed = true
	b.queueMu.Unlock()
}

// drainQueue sends ErrTransportClosed to all pending requests and transport ops.
// Must be called after markClosed to ensure no new items are enqueued.
func (b *Bus) drainQueue() {
	for {
		request, ok := b.dequeue()
		if !ok {
			return
		}
		if request.transportOp != nil {
			request.transportOpResp <- ebuserrors.ErrTransportClosed
		} else {
			request.resp <- busResult{err: ebuserrors.ErrTransportClosed}
		}
	}
}

// ObserverFaultSnapshot returns the current bounded observer-fault state.
func (b *Bus) ObserverFaultSnapshot() ObserverFaultSnapshot {
	b.observerFaultMu.Lock()
	defer b.observerFaultMu.Unlock()
	return b.observerFault
}

// PayloadAaAutoSynAbsorbed returns the cumulative count of wire SYN bytes
// absorbed by the round-9 AUTO-SYN drain at the payload-0xAA echo site in
// sendRawWithEcho. Each increment represents one ReadByteWithEscape call
// whose result was an unexpected wire SYN (WasEscaped=false) and was
// dropped per the ENS-transmit-token invariant. Surface as a Prometheus
// gauge/counter for observability.
//
// v8 LEGACY-FALLBACK label (Step D, retained per frame-atomic-v8 I8):
// the round-9 absorb mechanism is intentionally retained as the legacy
// fallback for direct-adapter mode (no adaptermux proxy mediating wire
// AUTO-SYNs). Under v8 enforce mode (classifier filtering on the gateway
// side) the proxy filters AUTO-SYNs before they reach this code path —
// expected steady-state increment rate is zero. Non-zero growth in
// enforce mode is a v8 invariant violation surfaced via the
// HelianthusRound9FiredUnderProxy alert. The code is NOT marked
// deprecated and is NOT scheduled for removal: it remains the canonical
// filter for direct-adapter deployments.
func (b *Bus) PayloadAaAutoSynAbsorbed() uint64 {
	return b.payloadAaAutoSynAbsorbed.Load()
}

// PayloadAaAutoSynRecovered returns the cumulative count of transactions
// rescued by the round-9 AUTO-SYN drain — i.e. cases where the absorb
// loop found the real escape-decoded payload echo within the cap and the
// transaction succeeded without emitting an echo_mismatch or returning
// ErrBusCollision. The ratio Recovered / (Absorbed events) approximates
// the round-9 retry-rate reduction.
//
// v8 LEGACY-FALLBACK label (Step D, retained per frame-atomic-v8 I8):
// see PayloadAaAutoSynAbsorbed docstring. The recovery counter measures
// effectiveness of the legacy-fallback drain — useful in direct-adapter
// mode, zero in proxy-mediated v8-enforce mode.
func (b *Bus) PayloadAaAutoSynRecovered() uint64 {
	return b.payloadAaAutoSynRecovered.Load()
}

// PayloadAaAutoSynDrainExhausted returns the cumulative count of
// transactions where the round-9 absorb loop reached
// maxPayloadAaAutoSynDrains intervening wire SYNs without finding the
// real payload echo. These fall through to the original payload-0xAA
// echo_mismatch + ErrBusCollision retry path, preserving the pre-round-9
// behavior for adapter-genuinely-stuck cases.
//
// v8 LEGACY-FALLBACK label (Step D, retained per frame-atomic-v8 I8):
// see PayloadAaAutoSynAbsorbed docstring. The drain-exhausted counter
// signals "absorb cap reached without recovery" — i.e. the
// legacy-fallback couldn't rescue the transaction and the pre-round-9
// echo-mismatch path is taking over. In proxy-mediated v8-enforce mode
// this stays at zero because the upstream proxy filters wire AUTO-SYNs
// before they reach this code.
func (b *Bus) PayloadAaAutoSynDrainExhausted() uint64 {
	return b.payloadAaAutoSynDrainExhausted.Load()
}

// inSendRawWithEchoActiveEchoWait reports whether at least one
// goroutine on this Bus is currently inside the active echo-wait
// phase of sendRawWithEcho — i.e. between a successful raw-byte
// Write and the resolution of its echo. The predicate is bus-wide,
// not goroutine-local; it answers "is any goroutine in echo wait?",
// not "did the caller arrive via sendRawWithEcho?". For the only
// current call site (the round-9 absorb predicate, itself inside
// sendRawWithEcho) the predicate is always true and the gate is a
// no-op assertion. The hook exists for Prometheus / future call
// sites and documents the phase invariant explicitly.
func (b *Bus) inSendRawWithEchoActiveEchoWait() bool {
	return b.activeEchoWaits.Load() > 0
}

// Round9AbsorbEntered returns the cumulative count of round-9
// AUTO-SYN absorb predicate fires that occurred while the bus was in
// the active echo-wait phase of sendRawWithEcho. The counter is
// deliberately named neutrally: this code has no notion of whether a
// proxy is mediating wire AUTO-SYNs, so it counts every entry
// regardless of deployment mode. The Prometheus alert that AND-s
// this counter with the adaptermux classifier-mode label
// (HelianthusRound9FiredUnderProxy — see helianthus-docs-ebus →
// deployment/prometheus-alerts.md) is what makes the round-9-under-
// proxy interpretation.
//
// v8 LEGACY-FALLBACK label (Step D, retained per frame-atomic-v8 I8):
// the round-9 absorb code itself is RETAINED as the legacy fallback
// for DIRECT-ADAPTER MODE (no adaptermux proxy in the byte path).
// In that deployment, round-9 is the canonical filter — operators
// expect non-zero counter growth under bus contention. In
// PROXY-MEDIATED v8-ENFORCE MODE the upstream proxy filters AUTO-SYNs
// before they reach this code, so the steady-state counter rate is 0.
// The two interpretations are reconciled at the Prometheus alert
// layer, not in this counter. The code is NOT marked deprecated and
// is NOT scheduled for removal — it stays as the production filter
// for direct-adapter deployments.
//
// Suggested Prometheus name: helianthus_round9_absorb_entered_total.
// Pair with PayloadAaAutoSynAbsorbed / PayloadAaAutoSynRecovered /
// PayloadAaAutoSynDrainExhausted for severity dashboards (per-byte
// drained, recovery rate, cap exhaustion).
func (b *Bus) Round9AbsorbEntered() uint64 {
	return b.round9AbsorbEntered.Load()
}

func (b *Bus) emitAttemptComplete(request Frame, response *Frame, frameType FrameType, attempt uint16, startedAt time.Time) {
	event := BusEvent{
		Kind:           BusEventAttemptComplete,
		FrameType:      frameType,
		Outcome:        BusOutcomeSuccess,
		Attempt:        attempt,
		DurationMicros: durationMicros(time.Since(startedAt)),
		Request:        request,
		HasRequest:     true,
	}
	if response != nil {
		event.Response = *response
		event.HasResponse = true
	}
	b.emitObserverEvent(event)
}

func (b *Bus) emitRequestComplete(request Frame, response *Frame, frameType FrameType, attempt, timeoutRetries, nackRetries uint16, err error, elapsed time.Duration) {
	event := BusEvent{
		Kind:           BusEventRequestComplete,
		FrameType:      frameType,
		Outcome:        busOutcomeFromError(err),
		Attempt:        attempt,
		TimeoutRetries: timeoutRetries,
		NACKRetries:    nackRetries,
		DurationMicros: durationMicros(elapsed),
		Request:        request,
		HasRequest:     true,
	}
	if response != nil {
		event.Response = *response
		event.HasResponse = true
	}
	b.emitObserverEvent(event)
}

func (b *Bus) emitRetryEvent(request Frame, frameType FrameType, attempt, timeoutRetries, nackRetries uint16, err error) {
	b.emitObserverEvent(BusEvent{
		Kind:           BusEventRetry,
		FrameType:      frameType,
		Outcome:        busOutcomeFromError(err),
		Retry:          busRetryReasonFromError(err),
		Attempt:        attempt,
		TimeoutRetries: timeoutRetries,
		NACKRetries:    nackRetries,
		Request:        request,
		HasRequest:     true,
	})
}

// echoMismatchEmittedError marks an error whose echo-mismatch observation
// already produced a bus event, so outcome emission does not repeat it.
type echoMismatchEmittedError struct {
	err error
}

func (e *echoMismatchEmittedError) Error() string { return e.err.Error() }

func (e *echoMismatchEmittedError) Unwrap() error { return e.err }

func echoMismatchAlreadyEmitted(err error) bool {
	var marker *echoMismatchEmittedError
	return errors.As(err, &marker)
}

func (b *Bus) emitOutcomeEvent(request Frame, frameType FrameType, attempt uint16, err error) {
	if echoMismatchAlreadyEmitted(err) && busOutcomeFromError(err) == BusOutcomeEchoMismatch {
		return
	}
	switch busOutcomeFromError(err) {
	case BusOutcomeTimeout:
		b.emitObserverEvent(BusEvent{
			Kind:       BusEventTimeout,
			FrameType:  frameType,
			Outcome:    BusOutcomeTimeout,
			Attempt:    attempt,
			Request:    request,
			HasRequest: frameType != FrameTypeUnknown,
		})
	case BusOutcomeNACK:
		b.emitObserverEvent(BusEvent{
			Kind:       BusEventNACK,
			FrameType:  frameType,
			Outcome:    BusOutcomeNACK,
			Attempt:    attempt,
			Request:    request,
			HasRequest: frameType != FrameTypeUnknown,
		})
	case BusOutcomeCRCMismatch:
		b.emitObserverEvent(BusEvent{
			Kind:       BusEventCRCMismatch,
			FrameType:  frameType,
			Outcome:    BusOutcomeCRCMismatch,
			Attempt:    attempt,
			Request:    request,
			HasRequest: frameType != FrameTypeUnknown,
		})
	case BusOutcomeEchoMismatch:
		b.emitObserverEvent(BusEvent{
			Kind:       BusEventEchoMismatch,
			FrameType:  frameType,
			Outcome:    BusOutcomeEchoMismatch,
			Attempt:    attempt,
			Request:    request,
			HasRequest: frameType != FrameTypeUnknown,
		})
	case BusOutcomeAdapterReset:
		b.emitObserverEvent(BusEvent{
			Kind:       BusEventAdapterReset,
			FrameType:  frameType,
			Outcome:    BusOutcomeAdapterReset,
			Attempt:    attempt,
			Request:    request,
			HasRequest: frameType != FrameTypeUnknown,
		})
	}
}

type observerDispatchResult struct {
	err      error
	panicked bool
}

func (b *Bus) emitObserverEvent(event BusEvent) {
	observer := b.config.Observer
	if observer == nil {
		return
	}

	// Clone slice fields so observer callbacks cannot alias caller memory.
	if event.HasRequest && len(event.Request.Data) > 0 {
		event.Request.Data = append([]byte(nil), event.Request.Data...)
	}
	if event.HasResponse && len(event.Response.Data) > 0 {
		event.Response.Data = append([]byte(nil), event.Response.Data...)
	}

	result := callObserver(observer, event)
	if result.err == nil {
		return
	}

	b.recordObserverFault(event, result)
	if event.Kind == BusEventObserverFault {
		return
	}

	callObserver(observer, BusEvent{
		Kind:        BusEventObserverFault,
		FrameType:   event.FrameType,
		Outcome:     BusOutcomeObserverFault,
		Attempt:     event.Attempt,
		Initiator:   event.Initiator,
		Request:     event.Request,
		Response:    event.Response,
		HasRequest:  event.HasRequest,
		HasResponse: event.HasResponse,
	})
}

func callObserver(observer BusObserver, event BusEvent) (result observerDispatchResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result.err = fmt.Errorf("observer panic: %v", recovered)
			result.panicked = true
		}
	}()

	result.err = observer.OnBusEvent(event)
	return result
}

func (b *Bus) recordObserverFault(event BusEvent, result observerDispatchResult) {
	b.observerFaultMu.Lock()
	defer b.observerFaultMu.Unlock()

	b.observerFault.Count++
	b.observerFault.LastKind = event.Kind
	b.observerFault.LastOutcome = event.Outcome
	b.observerFault.LastPanic = result.panicked
	if result.err != nil {
		b.observerFault.LastError = result.err.Error()
	} else {
		b.observerFault.LastError = ""
	}
}

func busOutcomeFromError(err error) BusOutcomeClass {
	switch {
	case err == nil:
		return BusOutcomeSuccess
	case errors.Is(err, ebuserrors.ErrTimeout):
		return BusOutcomeTimeout
	case errors.Is(err, ebuserrors.ErrNACK):
		return BusOutcomeNACK
	case errors.Is(err, ebuserrors.ErrCRCMismatch):
		return BusOutcomeCRCMismatch
	case errors.Is(err, ebuserrors.ErrBusCollision):
		if containsEchoMismatch(err) {
			return BusOutcomeEchoMismatch
		}
		return BusOutcomeCollision
	case errors.Is(err, ebuserrors.ErrAdapterReset):
		return BusOutcomeAdapterReset
	default:
		return BusOutcomeUnknown
	}
}

func busRetryReasonFromError(err error) BusRetryReason {
	switch busOutcomeFromError(err) {
	case BusOutcomeTimeout:
		return BusRetryReasonTimeout
	case BusOutcomeNACK:
		return BusRetryReasonNACK
	case BusOutcomeCRCMismatch:
		return BusRetryReasonCRCMismatch
	case BusOutcomeCollision, BusOutcomeEchoMismatch:
		return BusRetryReasonCollision
	case BusOutcomeAdapterReset:
		return BusRetryReasonAdapterReset
	default:
		return BusRetryReasonUnknown
	}
}

func containsEchoMismatch(err error) bool {
	if err == nil {
		return false
	}
	return stringsContains(err.Error(), "echo mismatch")
}

func stringsContains(value, needle string) bool {
	return len(needle) > 0 && len(value) >= len(needle) && (value == needle || containsSubstring(value, needle))
}

func containsSubstring(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func durationMicros(duration time.Duration) int64 {
	return duration.Microseconds()
}
