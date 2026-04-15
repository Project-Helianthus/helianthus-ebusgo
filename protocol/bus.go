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

// adapterResetRetryDelay is the stabilization delay before retrying after
// ErrAdapterReset. Unlike collision (waitForSyn), adapter reset does not
// guarantee bus traffic will resume, so we use a fixed delay.
const adapterResetRetryDelay = 200 * time.Millisecond

// collisionBackoffFloor is the minimum delay between an arbitration FAILED
// and the next START request. The PIC16F firmware has a race in
// protocol_state_dispatch where rapid START floods bypass the 60-tick scan
// deadline and cause transient eBUS signal loss. 50ms lets the firmware
// flush its FAILED response, apply the deadline, and reset the UART state.
const collisionBackoffFloor = 50 * time.Millisecond

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

func (b *Bus) sendWithRetries(runCtx context.Context, request *busRequest) (*Frame, error) {
	frameType := request.frame.Type()
	policy := b.retryPolicy(frameType)
	startedAt := time.Now()

	timeoutAttempts := 0
	nackAttempts := 0
	reconnectAttempts := 0
	// A deadline-bounded context can tolerate unbounded collision retries
	// because the deadline will eventually stop the loop. Without a deadline,
	// collision retries must be bounded by the retry policy.
	deadlineBoundsRetries := isBoundedContext(request.ctx)
	attemptCount := uint16(0)

	for {
		attemptCount++
		if err := b.contextError(runCtx, request.ctx); err != nil {
			b.emitRequestComplete(request.frame, nil, frameType, attemptCount-1, uint16(timeoutAttempts), uint16(nackAttempts), err, time.Since(startedAt))
			return nil, err
		}

		if err := b.startArbitration(request.frame.Source, frameType, attemptCount); err != nil {
			if errors.Is(err, ebuserrors.ErrBusCollision) {
				// Arbitration can be lost while another initiator owns the bus.
				// ebusd waits for subsequent SYN symbols before retrying; do the
				// same here (bounded by request context deadline).
				if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts, deadlineBoundsRetries); retry {
					timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
					b.emitRetryEvent(request.frame, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), err)
					// 50ms backoff floor for PIC16F firmware race: the firmware
					// needs time to flush FAILED, apply its scan deadline, and
					// reset UART state before accepting a new START.
					select {
					case <-time.After(collisionBackoffFloor):
					case <-runCtx.Done():
						return nil, b.wrapRetryError(runCtx.Err())
					case <-request.ctx.Done():
						return nil, b.wrapRetryError(request.ctx.Err())
					}
					if waitErr := b.waitForSyn(runCtx, request.ctx, 2); waitErr != nil {
						b.emitRequestComplete(request.frame, nil, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), waitErr, time.Since(startedAt))
						return nil, b.wrapRetryError(waitErr)
					}
					continue
				}
				b.emitRequestComplete(request.frame, nil, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), err, time.Since(startedAt))
				return nil, b.wrapRetryError(err)
			}
			if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts, deadlineBoundsRetries); retry {
				timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
				b.emitRetryEvent(request.frame, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), err)
				if errors.Is(err, ebuserrors.ErrAdapterReset) {
					// Short stabilization delay — unlike collision, adapter
					// reset does not guarantee bus traffic so waitForSyn
					// could hang on a silent bus.
					select {
					case <-time.After(adapterResetRetryDelay):
					case <-runCtx.Done():
						return nil, b.wrapRetryError(runCtx.Err())
					case <-request.ctx.Done():
						return nil, b.wrapRetryError(request.ctx.Err())
					}
				}
				continue
			}
			// Retry budget exhausted — try transport reconnect for
			// timeout-class errors before giving up.
			if b.tryTransportReconnect(runCtx, request, err, &timeoutAttempts, &nackAttempts, &reconnectAttempts) {
				continue
			}
			b.emitRequestComplete(request.frame, nil, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), err, time.Since(startedAt))
			return nil, b.wrapRetryError(err)
		}

		response, err := b.sendTransaction(runCtx, request.ctx, request.frame, attemptCount)
		if err == nil {
			b.emitRequestComplete(request.frame, response, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), nil, time.Since(startedAt))
			return response, nil
		}
		if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts, deadlineBoundsRetries); retry {
			timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
			b.emitRetryEvent(request.frame, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), err)
			if errors.Is(err, ebuserrors.ErrBusCollision) {
				// EG36: This branch only fires for genuine bus collisions
				// (ErrBusCollision). Timeout wraps ErrTimeout and HOST errors
				// wrap ErrAdapterHostError, neither of which satisfies Is(ErrBusCollision).
				// 50ms backoff floor for PIC16F firmware race (post-transaction).
				select {
				case <-time.After(collisionBackoffFloor):
				case <-runCtx.Done():
					return nil, b.wrapRetryError(runCtx.Err())
				case <-request.ctx.Done():
					return nil, b.wrapRetryError(request.ctx.Err())
				}
				if waitErr := b.waitForSyn(runCtx, request.ctx, 2); waitErr != nil {
					b.emitRequestComplete(request.frame, nil, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), waitErr, time.Since(startedAt))
					return nil, b.wrapRetryError(waitErr)
				}
			}
			if errors.Is(err, ebuserrors.ErrAdapterReset) {
				select {
				case <-time.After(adapterResetRetryDelay):
				case <-runCtx.Done():
					return nil, b.wrapRetryError(runCtx.Err())
				case <-request.ctx.Done():
					return nil, b.wrapRetryError(request.ctx.Err())
				}
			}
			continue
		}
		// Retry budget exhausted — try transport reconnect for
		// timeout-class errors before giving up.
		if b.tryTransportReconnect(runCtx, request, err, &timeoutAttempts, &nackAttempts, &reconnectAttempts) {
			continue
		}
		b.emitRequestComplete(request.frame, nil, frameType, attemptCount, uint16(timeoutAttempts), uint16(nackAttempts), err, time.Since(startedAt))
		return nil, b.wrapRetryError(err)
	}
}

// tryTransportReconnect attempts a transport-level reconnect when timeout
// retries are exhausted. Returns true if reconnect was attempted and the
// caller should continue the retry loop.
//
// Python equivalent: _send_with_policy reconnect_retries tier.
func (b *Bus) tryTransportReconnect(runCtx context.Context, request *busRequest, err error, timeoutAttempts, nackAttempts, reconnectAttempts *int) bool {
	if b.config.ReconnectRetries <= 0 {
		return false
	}
	// Only reconnect for timeout-class errors, not NACK or collision.
	if !errors.Is(err, ebuserrors.ErrTimeout) &&
		!errors.Is(err, ebuserrors.ErrAdapterReset) &&
		!errors.Is(err, ebuserrors.ErrCRCMismatch) {
		return false
	}
	reconn, ok := b.transport.(transport.Reconnectable)
	if !ok {
		return false
	}
	if *reconnectAttempts >= b.config.ReconnectRetries {
		return false
	}
	*reconnectAttempts++

	// Wait before reconnecting — gives the adapter time to finish rebooting.
	select {
	case <-time.After(b.config.ReconnectDelay):
	case <-runCtx.Done():
		return false
	case <-request.ctx.Done():
		return false
	}

	if err := reconn.Reconnect(); err != nil {
		// Reconnect failed — don't reset timeout budget. The caller
		// will re-enter tryTransportReconnect on the next timeout-class
		// error and use the next reconnect attempt.
		return true
	}
	*timeoutAttempts = 0 // reset timeout budget for the fresh session
	*nackAttempts = 0    // reset NACK budget for the fresh session
	return true
}

func (b *Bus) retryPolicy(frameType FrameType) RetryPolicy {
	switch frameType {
	case FrameTypeInitiatorInitiator:
		return b.config.InitiatorInitiator
	case FrameTypeInitiatorTarget:
		return b.config.InitiatorTarget
	default:
		return RetryPolicy{}
	}
}

func (b *Bus) startArbitration(initiator byte, frameType FrameType, attempt uint16) error {
	tr, ok := b.transport.(arbitrationTransport)
	if !ok {
		return nil
	}
	startedAt := time.Now()
	if err := tr.StartArbitration(initiator); err != nil {
		b.emitObserverEvent(BusEvent{
			Kind:           BusEventArbitration,
			FrameType:      frameType,
			Outcome:        busOutcomeFromError(err),
			Initiator:      initiator,
			Attempt:        attempt,
			DurationMicros: durationMicros(time.Since(startedAt)),
		})
		return fmt.Errorf("bus arbitration failed: %w", err)
	}
	b.emitObserverEvent(BusEvent{
		Kind:           BusEventArbitration,
		FrameType:      frameType,
		Outcome:        BusOutcomeSuccess,
		Initiator:      initiator,
		Attempt:        attempt,
		DurationMicros: durationMicros(time.Since(startedAt)),
	})
	return nil
}

func shouldRetry(err error, policy RetryPolicy, timeoutAttempts, nackAttempts int, deadlineBoundsRetries bool) (bool, int, int) {
	// HOST errors are non-retryable protocol violations — never retry.
	if errors.Is(err, ebuserrors.ErrAdapterHostError) {
		return false, timeoutAttempts, nackAttempts
	}
	// Collisions are transient bus-ownership events; retry until ctx deadline/cancel.
	if errors.Is(err, ebuserrors.ErrBusCollision) {
		if deadlineBoundsRetries {
			return true, timeoutAttempts, nackAttempts
		}
		if timeoutAttempts < policy.TimeoutRetries {
			return true, timeoutAttempts + 1, nackAttempts
		}
		return false, timeoutAttempts, nackAttempts
	}
	if errors.Is(err, ebuserrors.ErrAdapterReset) {
		if timeoutAttempts < policy.TimeoutRetries {
			return true, timeoutAttempts + 1, nackAttempts
		}
		return false, timeoutAttempts, nackAttempts
	}
	// Timeout is deterministic on eBUS — no device answered, retrying is
	// pointless. Fall through to return false.
	if errors.Is(err, ebuserrors.ErrTimeout) {
		return false, timeoutAttempts, nackAttempts
	}
	if errors.Is(err, ebuserrors.ErrCRCMismatch) {
		if timeoutAttempts < policy.TimeoutRetries {
			return true, timeoutAttempts + 1, nackAttempts
		}
	}
	if errors.Is(err, ebuserrors.ErrNACK) {
		if nackAttempts < policy.NACKRetries {
			return true, timeoutAttempts, nackAttempts + 1
		}
	}
	return false, timeoutAttempts, nackAttempts
}

func isBoundedContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if _, ok := ctx.Deadline(); ok {
		return true
	}
	return false
}

func (b *Bus) wrapRetryError(err error) error {
	if errors.Is(err, ebuserrors.ErrBusCollision) {
		return fmt.Errorf("bus send collision: %w", err)
	}
	if errors.Is(err, ebuserrors.ErrTimeout) {
		return fmt.Errorf("bus send timeout: %w", err)
	}
	if errors.Is(err, ebuserrors.ErrNACK) {
		return fmt.Errorf("bus send nack: %w", err)
	}
	if errors.Is(err, ebuserrors.ErrCRCMismatch) {
		return fmt.Errorf("bus send crc mismatch: %w", err)
	}
	if errors.Is(err, ebuserrors.ErrAdapterReset) {
		return fmt.Errorf("bus send adapter reset: %w", err)
	}
	if errors.Is(err, ebuserrors.ErrTransportClosed) {
		return fmt.Errorf("bus transport closed: %w", err)
	}
	return fmt.Errorf("bus send failed: %w", err)
}

// readByte blocks until a byte is available from the transport or an error
// occurs. The blocking duration is bounded by the transport's read timeout.
// To interrupt, close the transport connection which unblocks the read.
func (b *Bus) readByte(runCtx, reqCtx context.Context) (byte, error) {
	if err := b.contextError(runCtx, reqCtx); err != nil {
		return 0, err
	}
	value, err := b.transport.ReadByte()
	if err != nil {
		return 0, err
	}
	b.emitObserverEvent(BusEvent{
		Kind:    BusEventRX,
		Outcome: BusOutcomeSuccess,
		Byte:    value,
	})
	return value, nil
}

type busDecoder struct {
	escape bool
}

func (d *busDecoder) readSymbol(b *Bus, runCtx, reqCtx context.Context) (byte, error) {
	if b.unescapedTransport {
		// ENH transport delivers pre-unescaped bytes; no wire-level escape processing.
		raw, err := b.readByte(runCtx, reqCtx)
		if err != nil {
			return 0, err
		}
		return raw, nil
	}
	// Plain transport: decode eBUS escape sequences (0xA9 + 0x00/0x01).
	for {
		raw, err := b.readByte(runCtx, reqCtx)
		if err != nil {
			if errors.Is(err, ebuserrors.ErrTimeout) {
				d.escape = false
			}
			return 0, err
		}

		if d.escape {
			d.escape = false
			switch raw {
			case 0x00:
				return SymbolEscape, nil
			case 0x01:
				return SymbolSyn, nil
			default:
				return 0, fmt.Errorf("invalid escape sequence 0x%02x: %w", raw, ebuserrors.ErrInvalidPayload)
			}
		}

		if raw == SymbolEscape {
			d.escape = true
			continue
		}
		return raw, nil
	}
}

func (b *Bus) sendTransaction(runCtx, reqCtx context.Context, frame Frame, attempt uint16) (*Frame, error) {
	frameType := frame.Type()
	if frameType == FrameTypeUnknown {
		return nil, errUnknownFrameType
	}
	if len(frame.Data) > 255 {
		return nil, fmt.Errorf("frame data length %d exceeds eBUS maximum 255: %w", len(frame.Data), ebuserrors.ErrInvalidPayload)
	}
	startedAt := time.Now()

	// Initiator telegram (unescaped symbols): SRC DST PB SB LEN DATA... CRC
	telegram := make([]byte, 0, 6+len(frame.Data))
	telegram = append(telegram, frame.Source, frame.Target, frame.Primary, frame.Secondary, byte(len(frame.Data)))
	telegram = append(telegram, frame.Data...)
	telegram = append(telegram, CRC(telegram))

	decoder := &busDecoder{}

	// First attempt: for enhanced transports, SRC is already sent during arbitration.
	includeSource := true
	if _, ok := b.transport.(arbitrationTransport); ok {
		includeSource = false
		if behavior, ok := b.transport.(arbitrationSourceBehavior); ok {
			includeSource = !behavior.ArbitrationSendsSource()
		}
	}

	// Inner 2-attempt loop: per eBUS spec, the initiator retransmits the
	// command telegram once after a NACK without re-arbitrating (commandAttempt
	// 0 = first send, 1 = spec-mandated retry). The outer sendWithRetries
	// applies policy-based retries that involve re-arbitration. With default
	// NACKRetries=1 this yields at most: 2 inner sends + re-arbitrate + 2 inner
	// sends = 4 total transmissions. This is correct per spec.
	acked := false
	for commandAttempt := 0; commandAttempt < 2; commandAttempt++ {
		if err := b.sendInitiatorTelegram(runCtx, reqCtx, telegram, includeSource); err != nil {
			return nil, err
		}

		if frameType == FrameTypeBroadcast {
			if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
				return nil, err
			}
			b.emitAttemptComplete(frame, nil, frameType, attempt, startedAt)
			return nil, nil
		}

		ack, err := decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		}
		switch ack {
		case SymbolAck:
			b.emitObserverEvent(BusEvent{
				Kind:       BusEventACK,
				FrameType:  frameType,
				Outcome:    BusOutcomeSuccess,
				Byte:       SymbolAck,
				Attempt:    attempt,
				Request:    frame,
				HasRequest: true,
			})
			acked = true
		case SymbolNack:
			b.emitObserverEvent(BusEvent{
				Kind:       BusEventNACK,
				FrameType:  frameType,
				Outcome:    BusOutcomeNACK,
				Byte:       SymbolNack,
				Attempt:    attempt,
				Request:    frame,
				HasRequest: true,
			})
			if commandAttempt == 0 {
				// Repeat command once without arbitration; SRC must be sent explicitly.
				includeSource = true
				continue
			}
			_ = b.sendEndOfMessage(runCtx, reqCtx)
			err := fmt.Errorf("nack received: %w", ebuserrors.ErrNACK)
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		case SymbolSyn:
			// On plain transports, SYN during ACK wait means the bus went
			// idle without a response (timeout). On ENH transports, 0xAA is
			// a valid data byte — the adapter does not send idle SYN.
			if !b.unescapedTransport {
				err := fmt.Errorf("syn while waiting for command ack: %w", ebuserrors.ErrTimeout)
				b.emitOutcomeEvent(frame, frameType, attempt, err)
				return nil, err
			}
			// ENH: treat 0xAA as unexpected symbol, not SYN timeout.
			err := fmt.Errorf("unexpected symbol 0x%02x while waiting for command ack: %w", ack, ebuserrors.ErrTimeout)
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		default:
			err := fmt.Errorf("unexpected symbol 0x%02x while waiting for command ack: %w", ack, ebuserrors.ErrTimeout)
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		}
		if acked {
			break
		}
	}
	if !acked {
		return nil, errCommandAckLoopExhausted
	}

	if frameType == FrameTypeInitiatorInitiator {
		if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
			return nil, err
		}
		b.emitAttemptComplete(frame, nil, frameType, attempt, startedAt)
		return nil, nil
	}
	if frameType != FrameTypeInitiatorTarget {
		return nil, errUnknownFrameType
	}

	// eBUS target response: NN DB1..DBn CRC (no header - QQ/ZZ/PB/SB are
	// inferred from the initiator telegram).
	var data []byte
	for respAttempt := 0; respAttempt < 2; respAttempt++ {
		lengthSym, err := decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		}
		// On plain transports, SYN (0xAA) during response means bus went idle
		// (timeout). On ENH transports, 0xAA is a valid data byte — the
		// adapter does not inject idle SYN into the byte stream.
		if !b.unescapedTransport && lengthSym == SymbolSyn {
			err := fmt.Errorf("syn while waiting for response length: %w", ebuserrors.ErrTimeout)
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		}

		length := int(lengthSym)
		data = make([]byte, length)
		for i := 0; i < length; i++ {
			value, err := decoder.readSymbol(b, runCtx, reqCtx)
			if err != nil {
				b.emitOutcomeEvent(frame, frameType, attempt, err)
				return nil, err
			}
			if !b.unescapedTransport && value == SymbolSyn {
				err := fmt.Errorf("syn while reading response data: %w", ebuserrors.ErrTimeout)
				b.emitOutcomeEvent(frame, frameType, attempt, err)
				return nil, err
			}
			data[i] = value
		}

		crcValue, err := decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		}
		if !b.unescapedTransport && crcValue == SymbolSyn {
			err := fmt.Errorf("syn while waiting for response crc: %w", ebuserrors.ErrTimeout)
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		}

		segment := make([]byte, 0, 1+len(data))
		segment = append(segment, lengthSym)
		segment = append(segment, data...)
		if CRC(segment) != crcValue {
			if err := b.sendSymbolWithEcho(runCtx, reqCtx, SymbolNack, true); err != nil {
				return nil, err
			}
			b.emitObserverEvent(BusEvent{
				Kind:        BusEventNACK,
				FrameType:   frameType,
				Outcome:     BusOutcomeCRCMismatch,
				Byte:        SymbolNack,
				Attempt:     attempt,
				Request:     frame,
				Response:    Frame{Source: frame.Target, Target: frame.Source, Primary: frame.Primary, Secondary: frame.Secondary, Data: data},
				HasRequest:  true,
				HasResponse: true,
			})
			if respAttempt == 0 {
				b.emitObserverEvent(BusEvent{
					Kind:        BusEventCRCMismatch,
					FrameType:   frameType,
					Outcome:     BusOutcomeCRCMismatch,
					Attempt:     attempt,
					Request:     frame,
					Response:    Frame{Source: frame.Target, Target: frame.Source, Primary: frame.Primary, Secondary: frame.Secondary, Data: data},
					HasRequest:  true,
					HasResponse: true,
				})
				continue
			}
			_ = b.sendEndOfMessage(runCtx, reqCtx)
			err := fmt.Errorf("crc mismatch: %w", ebuserrors.ErrCRCMismatch)
			b.emitOutcomeEvent(frame, frameType, attempt, err)
			return nil, err
		}

		if err := b.sendSymbolWithEcho(runCtx, reqCtx, SymbolAck, true); err != nil {
			return nil, err
		}
		response := &Frame{
			Source:    frame.Target,
			Target:    frame.Source,
			Primary:   frame.Primary,
			Secondary: frame.Secondary,
			Data:      data,
		}
		b.emitObserverEvent(BusEvent{
			Kind:        BusEventACK,
			FrameType:   frameType,
			Outcome:     BusOutcomeSuccess,
			Byte:        SymbolAck,
			Attempt:     attempt,
			Request:     frame,
			Response:    *response,
			HasRequest:  true,
			HasResponse: true,
		})
		if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
			return nil, err
		}
		b.emitAttemptComplete(frame, response, frameType, attempt, startedAt)
		return response, nil
	}

	err := fmt.Errorf("unreachable response loop: %w", ebuserrors.ErrTimeout)
	b.emitOutcomeEvent(frame, frameType, attempt, err)
	return nil, err
}

func (b *Bus) sendInitiatorTelegram(runCtx, reqCtx context.Context, telegram []byte, includeSource bool) error {
	start := 0
	if !includeSource {
		start = 1
	}
	for i := start; i < len(telegram); i++ {
		if err := b.sendSymbolWithEcho(runCtx, reqCtx, telegram[i], true); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bus) sendEndOfMessage(runCtx, reqCtx context.Context) error {
	return b.sendSymbolWithEcho(runCtx, reqCtx, SymbolSyn, false)
}

func (b *Bus) sendSymbolWithEcho(runCtx, reqCtx context.Context, symbol byte, escape bool) error {
	if b.unescapedTransport || !escape || (symbol != SymbolEscape && symbol != SymbolSyn) {
		return b.sendRawWithEcho(runCtx, reqCtx, symbol)
	}

	// Plain transport: wire-level escape for 0xA9/0xAA symbols.
	if err := b.sendRawWithEcho(runCtx, reqCtx, SymbolEscape); err != nil {
		return err
	}
	esc := byte(0x00)
	if symbol == SymbolSyn {
		esc = 0x01
	}
	return b.sendRawWithEcho(runCtx, reqCtx, esc)
}

func (b *Bus) sendRawWithEcho(runCtx, reqCtx context.Context, raw byte) error {
	written, err := b.transport.Write([]byte{raw})
	if err != nil {
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	if written != 1 {
		err := ebuserrors.ErrInvalidPayload
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	b.emitObserverEvent(BusEvent{
		Kind:    BusEventTX,
		Outcome: BusOutcomeSuccess,
		Byte:    raw,
	})

	echo, err := b.readByte(runCtx, reqCtx)
	if err != nil {
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	if !b.unescapedTransport && echo == SymbolSyn && raw != SymbolSyn {
		err := fmt.Errorf("unexpected syn while waiting for echo: %w", ebuserrors.ErrBusCollision)
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	if echo != raw {
		b.emitObserverEvent(BusEvent{
			Kind:    BusEventEchoMismatch,
			Outcome: BusOutcomeEchoMismatch,
			Byte:    echo,
		})
		err := fmt.Errorf("echo mismatch (sent 0x%02x, got 0x%02x): %w", raw, echo, ebuserrors.ErrBusCollision)
		b.emitOutcomeEvent(Frame{}, FrameTypeUnknown, 0, err)
		return err
	}
	return nil
}

func (b *Bus) waitForSyn(runCtx, reqCtx context.Context, count int) error {
	if count <= 0 {
		return nil
	}
	if b.unescapedTransport {
		// ENH: bytes are pre-unescaped, 0xAA is always a real SYN.
		seen := 0
		for seen < count {
			if err := b.contextError(runCtx, reqCtx); err != nil {
				return err
			}
			raw, err := b.readByte(runCtx, reqCtx)
			if err != nil {
				if errors.Is(err, ebuserrors.ErrTimeout) ||
					errors.Is(err, ebuserrors.ErrAdapterReset) ||
					errors.Is(err, ebuserrors.ErrInvalidPayload) {
					continue
				}
				return err
			}
			if raw == SymbolSyn {
				seen++
			}
		}
		return nil
	}
	// Plain transport: track escape state to distinguish real SYN from escaped data.
	seen := 0
	prevWasEscape := false
	for seen < count {
		if err := b.contextError(runCtx, reqCtx); err != nil {
			return err
		}
		raw, err := b.readByte(runCtx, reqCtx)
		if err != nil {
			if errors.Is(err, ebuserrors.ErrTimeout) ||
				errors.Is(err, ebuserrors.ErrAdapterReset) ||
				errors.Is(err, ebuserrors.ErrInvalidPayload) {
				prevWasEscape = false
				continue
			}
			return err
		}
		if prevWasEscape {
			prevWasEscape = false
			continue // Escaped byte, not a real SYN
		}
		if raw == SymbolEscape {
			prevWasEscape = true
			continue
		}
		if raw == SymbolSyn {
			seen++
		}
	}
	return nil
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

func (b *Bus) emitOutcomeEvent(request Frame, frameType FrameType, attempt uint16, err error) {
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
