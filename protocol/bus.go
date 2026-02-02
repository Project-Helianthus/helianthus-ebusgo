package protocol

import (
	"context"
	"errors"
	"fmt"
	"sync"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/transport"
)

const defaultQueueCapacity = 64

type RetryPolicy struct {
	TimeoutRetries int
	NACKRetries    int
}

type BusConfig struct {
	MasterSlave  RetryPolicy
	MasterMaster RetryPolicy
}

func DefaultBusConfig() BusConfig {
	return BusConfig{
		MasterSlave: RetryPolicy{
			TimeoutRetries: 2,
			NACKRetries:    1,
		},
		MasterMaster: RetryPolicy{
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
		outCap:    queueCapacity,
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

	for {
		if err := b.contextError(runCtx, request.ctx); err != nil {
			return nil, err
		}

		if err := b.writeFrame(request.frame); err != nil {
			return nil, fmt.Errorf("bus send write: %w", err)
		}

		if frameType == FrameTypeBroadcast {
			return nil, nil
		}

		if frameType == FrameTypeMasterMaster {
			err := b.readAck(runCtx, request.ctx)
			if err == nil {
				return nil, nil
			}
			if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts); retry {
				timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
				continue
			}
			return nil, b.wrapRetryError(err)
		}

		if frameType != FrameTypeMasterSlave {
			return nil, fmt.Errorf("bus send unknown frame type: %w", ebuserrors.ErrInvalidPayload)
		}

		if err := b.readAck(runCtx, request.ctx); err != nil {
			if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts); retry {
				timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
				continue
			}
			return nil, b.wrapRetryError(err)
		}

		response, err := b.readResponse(runCtx, request.ctx, request.frame)
		if err == nil {
			return response, nil
		}
		if retry, timeoutAttempts2, nackAttempts2 := shouldRetry(err, policy, timeoutAttempts, nackAttempts); retry {
			timeoutAttempts, nackAttempts = timeoutAttempts2, nackAttempts2
			continue
		}
		return nil, b.wrapRetryError(err)
	}
}

func (b *Bus) retryPolicy(frameType FrameType) RetryPolicy {
	switch frameType {
	case FrameTypeMasterMaster:
		return b.config.MasterMaster
	case FrameTypeMasterSlave:
		return b.config.MasterSlave
	default:
		return RetryPolicy{}
	}
}

func shouldRetry(err error, policy RetryPolicy, timeoutAttempts, nackAttempts int) (bool, int, int) {
	if errors.Is(err, ebuserrors.ErrTimeout) {
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

func (b *Bus) wrapRetryError(err error) error {
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

func (b *Bus) writeFrame(frame Frame) error {
	command := make([]byte, 0, 6+len(frame.Data))
	command = append(command, frame.Source, frame.Target, frame.Primary, frame.Secondary, byte(len(frame.Data)))
	command = append(command, frame.Data...)
	command = append(command, CRC(command))

	written, err := b.transport.Write(command)
	if err != nil {
		return err
	}
	if written != len(command) {
		return ebuserrors.ErrInvalidPayload
	}
	return nil
}

func (b *Bus) readAck(runCtx, reqCtx context.Context) error {
	for {
		value, err := b.readByte(runCtx, reqCtx)
		if err != nil {
			return err
		}
		switch value {
		case SymbolSyn:
			continue
		case SymbolAck:
			return nil
		case SymbolNack:
			return fmt.Errorf("nack received: %w", ebuserrors.ErrNACK)
		default:
			return fmt.Errorf("unexpected ack symbol 0x%02x: %w", value, ebuserrors.ErrInvalidPayload)
		}
	}
}

func (b *Bus) readResponse(runCtx, reqCtx context.Context, request Frame) (*Frame, error) {
	length, err := b.readByte(runCtx, reqCtx)
	if err != nil {
		return nil, err
	}

	data := make([]byte, int(length))
	for i := 0; i < int(length); i++ {
		b, err := b.readByte(runCtx, reqCtx)
		if err != nil {
			return nil, err
		}
		data[i] = b
	}

	crcValue, err := b.readByte(runCtx, reqCtx)
	if err != nil {
		return nil, err
	}

	segment := append([]byte{length}, data...)
	if CRC(segment) != crcValue {
		return nil, fmt.Errorf("crc mismatch: %w", ebuserrors.ErrCRCMismatch)
	}

	resp := &Frame{
		Source:    request.Target,
		Target:    request.Source,
		Primary:   request.Primary,
		Secondary: request.Secondary,
		Data:      data,
	}
	return resp, nil
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
