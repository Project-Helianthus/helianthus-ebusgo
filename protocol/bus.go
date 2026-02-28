package protocol

import (
	"context"
	"errors"
	"fmt"
	"sync"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

const defaultQueueCapacity = 64

type RetryPolicy struct {
	TimeoutRetries int
	NACKRetries    int
}

type BusConfig struct {
	InitiatorTarget    RetryPolicy
	InitiatorInitiator RetryPolicy
}

func DefaultBusConfig() BusConfig {
	return BusConfig{
		InitiatorTarget: RetryPolicy{
			TimeoutRetries: 2,
			NACKRetries:    1,
		},
		InitiatorInitiator: RetryPolicy{
			TimeoutRetries: 2,
			NACKRetries:    1,
		},
	}
}

type busRequest struct {
	frame Frame
	ctx   context.Context
	resp  chan busResult
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

	outCap int
}

// NewBus initializes a Bus with transport, config, and optional queue capacity.
func NewBus(tr transport.RawTransport, config BusConfig, queueCapacity int) *Bus {
	if queueCapacity <= 0 {
		queueCapacity = defaultQueueCapacity
	}
	return &Bus{
		transport: tr,
		config:    config,
		queue:     newPriorityQueue(),
		// Capacity 1 to coalesce wake-ups from multiple Send calls.
		notify: make(chan struct{}, 1),
		outCap: queueCapacity,
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
	request := &busRequest{
		frame: frame,
		ctx:   ctx,
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
				return
			case <-b.notify:
				continue
			}
		}

		result := b.handleRequest(ctx, request)
		request.resp <- result
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

	timeoutAttempts := 0
	nackAttempts := 0
	allowUnboundedCollision := isBoundedContext(request.ctx)

	for {
		if err := b.contextError(runCtx, request.ctx); err != nil {
			return nil, err
		}

		if err := b.startArbitration(request.frame.Source); err != nil {
			if errors.Is(err, ebuserrors.ErrBusCollision) {
				// Arbitration can be lost while another initiator owns the bus.
				// ebusd waits for subsequent SYN symbols before retrying; do the
				// same here (bounded by request context deadline).
				if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts, allowUnboundedCollision); retry {
					timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
					if waitErr := b.waitForSyn(runCtx, request.ctx, 2); waitErr != nil {
						return nil, b.wrapRetryError(waitErr)
					}
					continue
				}
				return nil, b.wrapRetryError(err)
			}
			if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts, allowUnboundedCollision); retry {
				timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
				continue
			}
			return nil, b.wrapRetryError(err)
		}

		response, err := b.sendTransaction(runCtx, request.ctx, request.frame)
		if err == nil {
			return response, nil
		}
		if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts, allowUnboundedCollision); retry {
			timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
			if errors.Is(err, ebuserrors.ErrBusCollision) {
				if waitErr := b.waitForSyn(runCtx, request.ctx, 2); waitErr != nil {
					return nil, b.wrapRetryError(waitErr)
				}
			}
			continue
		}
		return nil, b.wrapRetryError(err)
	}
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

func (b *Bus) startArbitration(initiator byte) error {
	tr, ok := b.transport.(arbitrationTransport)
	if !ok {
		return nil
	}
	if err := tr.StartArbitration(initiator); err != nil {
		return fmt.Errorf("bus arbitration failed: %w", err)
	}
	return nil
}

func shouldRetry(err error, policy RetryPolicy, timeoutAttempts, nackAttempts int, allowUnboundedCollision bool) (bool, int, int) {
	// Collisions are transient bus-ownership events; retry until ctx deadline/cancel.
	if errors.Is(err, ebuserrors.ErrBusCollision) {
		if allowUnboundedCollision {
			return true, timeoutAttempts, nackAttempts
		}
		if timeoutAttempts < policy.TimeoutRetries {
			return true, timeoutAttempts + 1, nackAttempts
		}
		return false, timeoutAttempts, nackAttempts
	}
	if errors.Is(err, ebuserrors.ErrTimeout) || errors.Is(err, ebuserrors.ErrCRCMismatch) {
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
	if errors.Is(err, ebuserrors.ErrTransportClosed) {
		return fmt.Errorf("bus transport closed: %w", err)
	}
	return fmt.Errorf("bus send failed: %w", err)
}

func (b *Bus) readByte(runCtx, reqCtx context.Context) (byte, error) {
	if err := b.contextError(runCtx, reqCtx); err != nil {
		return 0, err
	}
	value, err := b.transport.ReadByte()
	if err != nil {
		return 0, err
	}
	return value, nil
}

type busDecoder struct {
	escape bool
}

func (d *busDecoder) readSymbol(b *Bus, runCtx, reqCtx context.Context) (byte, error) {
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

func (b *Bus) sendTransaction(runCtx, reqCtx context.Context, frame Frame) (*Frame, error) {
	frameType := frame.Type()
	if frameType == FrameTypeUnknown {
		return nil, fmt.Errorf("bus send unknown frame type: %w", ebuserrors.ErrInvalidPayload)
	}

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

	for attempt := 0; attempt < 2; attempt++ {
		if err := b.sendInitiatorTelegram(runCtx, reqCtx, telegram, includeSource); err != nil {
			return nil, err
		}

		if frameType == FrameTypeBroadcast {
			if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
				return nil, err
			}
			return nil, nil
		}

		ack, err := decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			return nil, err
		}
		switch ack {
		case SymbolAck:
			attempt = 2
		case SymbolNack:
			if attempt == 0 {
				// Repeat command once without arbitration; SRC must be sent explicitly.
				includeSource = true
				continue
			}
			_ = b.sendEndOfMessage(runCtx, reqCtx)
			return nil, fmt.Errorf("nack received: %w", ebuserrors.ErrNACK)
		case SymbolSyn:
			return nil, fmt.Errorf("syn while waiting for command ack: %w", ebuserrors.ErrTimeout)
		default:
			return nil, fmt.Errorf("unexpected symbol 0x%02x while waiting for command ack: %w", ack, ebuserrors.ErrTimeout)
		}
	}

	if frameType == FrameTypeInitiatorInitiator {
		if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if frameType != FrameTypeInitiatorTarget {
		return nil, fmt.Errorf("bus send unknown frame type: %w", ebuserrors.ErrInvalidPayload)
	}

	// eBUS slave response: NN DB1..DBn CRC (no header — QQ/ZZ/PB/SB are
	// inferred from the initiator telegram).
	var data []byte
	for respAttempt := 0; respAttempt < 2; respAttempt++ {
		lengthSym, err := decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			return nil, err
		}
		if lengthSym == SymbolSyn {
			return nil, fmt.Errorf("syn while waiting for response length: %w", ebuserrors.ErrTimeout)
		}

		length := int(lengthSym)
		data = make([]byte, length)
		for i := 0; i < length; i++ {
			value, err := decoder.readSymbol(b, runCtx, reqCtx)
			if err != nil {
				return nil, err
			}
			if value == SymbolSyn {
				return nil, fmt.Errorf("syn while reading response data: %w", ebuserrors.ErrTimeout)
			}
			data[i] = value
		}

		crcValue, err := decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			return nil, err
		}
		if crcValue == SymbolSyn {
			return nil, fmt.Errorf("syn while waiting for response crc: %w", ebuserrors.ErrTimeout)
		}

		segment := make([]byte, 0, 1+len(data))
		segment = append(segment, lengthSym)
		segment = append(segment, data...)
		if CRC(segment) != crcValue {
			if err := b.sendSymbolWithEcho(runCtx, reqCtx, SymbolNack, true); err != nil {
				return nil, err
			}
			if respAttempt == 0 {
				continue
			}
			_ = b.sendEndOfMessage(runCtx, reqCtx)
			return nil, fmt.Errorf("crc mismatch: %w", ebuserrors.ErrCRCMismatch)
		}

		if err := b.sendSymbolWithEcho(runCtx, reqCtx, SymbolAck, true); err != nil {
			return nil, err
		}
		if err := b.sendEndOfMessage(runCtx, reqCtx); err != nil {
			return nil, err
		}

		return &Frame{
			Source:    frame.Target,
			Target:    frame.Source,
			Primary:   frame.Primary,
			Secondary: frame.Secondary,
			Data:      data,
		}, nil
	}

	return nil, fmt.Errorf("unreachable response loop: %w", ebuserrors.ErrTimeout)
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
	if !escape || (symbol != SymbolEscape && symbol != SymbolSyn) {
		return b.sendRawWithEcho(runCtx, reqCtx, symbol)
	}

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
		return err
	}
	if written != 1 {
		return ebuserrors.ErrInvalidPayload
	}

	echo, err := b.readByte(runCtx, reqCtx)
	if err != nil {
		return err
	}
	if echo == SymbolSyn && raw != SymbolSyn {
		return fmt.Errorf("unexpected syn while waiting for echo: %w", ebuserrors.ErrBusCollision)
	}
	if echo != raw {
		return fmt.Errorf("echo mismatch (sent 0x%02x, got 0x%02x): %w", raw, echo, ebuserrors.ErrBusCollision)
	}
	return nil
}

func (b *Bus) waitForSyn(runCtx, reqCtx context.Context, count int) error {
	if count <= 0 {
		return nil
	}
	var decoder busDecoder
	seen := 0
	for seen < count {
		if err := b.contextError(runCtx, reqCtx); err != nil {
			return err
		}
		value, err := decoder.readSymbol(b, runCtx, reqCtx)
		if err != nil {
			if errors.Is(err, ebuserrors.ErrTimeout) {
				continue
			}
			return err
		}
		if value == SymbolSyn {
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
