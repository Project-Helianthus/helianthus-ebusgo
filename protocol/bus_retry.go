package protocol

import (
	"context"
	"errors"
	"fmt"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

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

// retryState carries the budgets and counters of one request across the
// attempts of sendWithRetries.
type retryState struct {
	request   *busRequest
	frameType FrameType
	policy    RetryPolicy
	startedAt time.Time

	timeoutAttempts   int
	nackAttempts      int
	reconnectAttempts int
	// A deadline-bounded context can tolerate unbounded collision retries
	// because the deadline will eventually stop the loop. Without a deadline,
	// collision retries must be bounded by the retry policy.
	deadlineBoundsRetries bool
	attemptCount          uint16
}

func (b *Bus) newRetryState(request *busRequest) *retryState {
	frameType := request.frame.Type()
	return &retryState{
		request:               request,
		frameType:             frameType,
		policy:                b.retryPolicy(frameType),
		startedAt:             time.Now(),
		deadlineBoundsRetries: isBoundedContext(request.ctx),
	}
}

// retry applies shouldRetry to the state and takes the new budgets only when
// the error is retried.
func (s *retryState) retry(err error) bool {
	retry, timeoutAttempts, nackAttempts := shouldRetry(err, s.policy, s.timeoutAttempts, s.nackAttempts, s.deadlineBoundsRetries)
	if retry {
		s.timeoutAttempts, s.nackAttempts = timeoutAttempts, nackAttempts
	}
	return retry
}

func (b *Bus) emitRetryStateEvent(state *retryState, err error) {
	b.emitRetryEvent(state.request.frame, state.frameType, state.attemptCount, uint16(state.timeoutAttempts), uint16(state.nackAttempts), err)
}

func (b *Bus) emitRetryStateComplete(state *retryState, response *Frame, attempt uint16, err error) {
	b.emitRequestComplete(state.request.frame, response, state.frameType, attempt, uint16(state.timeoutAttempts), uint16(state.nackAttempts), err, time.Since(state.startedAt))
}

func (b *Bus) sendWithRetries(runCtx context.Context, request *busRequest) (*Frame, error) {
	state := b.newRetryState(request)
	for {
		state.attemptCount++
		if err := b.contextError(runCtx, request.ctx); err != nil {
			b.emitRetryStateComplete(state, nil, state.attemptCount-1, err)
			return nil, err
		}

		if err := b.startArbitration(request.frame.Source, state.frameType, state.attemptCount); err != nil {
			if stopErr := b.afterArbitrationError(runCtx, state, err); stopErr != nil {
				return nil, stopErr
			}
			continue
		}

		response, err := b.sendTransaction(runCtx, request.ctx, request.frame, state.attemptCount)
		if err == nil {
			b.emitRetryStateComplete(state, response, state.attemptCount, nil)
			return response, nil
		}
		if stopErr := b.afterTransactionError(runCtx, state, err); stopErr != nil {
			return nil, stopErr
		}
	}
}

// afterArbitrationError decides what follows a failed arbitration. It returns
// nil when the loop starts the next attempt, or the error the request ends
// with.
func (b *Bus) afterArbitrationError(runCtx context.Context, state *retryState, err error) error {
	if errors.Is(err, ebuserrors.ErrBusCollision) {
		lifecycle, token := b.collisionRecoveryLifecycle()
		// Arbitration can be lost while another initiator owns the bus.
		// ebusd waits for subsequent SYN symbols before retrying; do the
		// same here (bounded by request context deadline).
		if state.retry(err) {
			b.emitRetryStateEvent(state, err)
			return b.collisionBackoff(runCtx, state, lifecycle, token)
		}
		b.completeCollisionRecovery(lifecycle, token, transport.CollisionRecoveryAbandon)
		b.emitRetryStateComplete(state, nil, state.attemptCount, err)
		return b.wrapRetryError(err)
	}
	if state.retry(err) {
		b.emitRetryStateEvent(state, err)
		if errors.Is(err, ebuserrors.ErrAdapterReset) {
			// Short stabilization delay — unlike collision, adapter
			// reset does not guarantee bus traffic so waitForSyn
			// could hang on a silent bus.
			return b.adapterResetBackoff(runCtx, state.request.ctx)
		}
		return nil
	}
	return b.reconnectOrGiveUp(runCtx, state, err)
}

// afterTransactionError decides what follows a failed transaction. It
// returns nil when the loop starts the next attempt, or the error the
// request ends with.
func (b *Bus) afterTransactionError(runCtx context.Context, state *retryState, err error) error {
	if state.retry(err) {
		b.emitRetryStateEvent(state, err)
		if errors.Is(err, ebuserrors.ErrBusCollision) {
			lifecycle, token := b.collisionRecoveryLifecycle()
			// EG36: This branch only fires for genuine bus collisions
			// (ErrBusCollision). Timeout wraps ErrTimeout and HOST errors
			// wrap ErrAdapterHostError, neither of which satisfies Is(ErrBusCollision).
			// 50ms backoff floor for PIC16F firmware race (post-transaction).
			if stopErr := b.collisionBackoff(runCtx, state, lifecycle, token); stopErr != nil {
				return stopErr
			}
		}
		if errors.Is(err, ebuserrors.ErrAdapterReset) {
			if stopErr := b.adapterResetBackoff(runCtx, state.request.ctx); stopErr != nil {
				return stopErr
			}
		}
		return nil
	}
	if errors.Is(err, ebuserrors.ErrBusCollision) {
		lifecycle, token := b.collisionRecoveryLifecycle()
		b.completeCollisionRecovery(lifecycle, token, transport.CollisionRecoveryAbandon)
	}
	return b.reconnectOrGiveUp(runCtx, state, err)
}

// reconnectOrGiveUp runs once the retry budget is exhausted: it tries a
// transport reconnect for timeout-class errors before giving up. It returns
// nil when the loop starts the next attempt.
func (b *Bus) reconnectOrGiveUp(runCtx context.Context, state *retryState, err error) error {
	if b.tryTransportReconnect(runCtx, state.request, err, &state.timeoutAttempts, &state.nackAttempts, &state.reconnectAttempts) {
		return nil
	}
	b.emitRetryStateComplete(state, nil, state.attemptCount, err)
	return b.wrapRetryError(err)
}

// collisionBackoff waits out a bus collision before the next attempt: the
// 50ms backoff floor for the PIC16F firmware race (the firmware needs time
// to flush FAILED, apply its scan deadline, and reset UART state before
// accepting a new START), then two SYN symbols. It completes the collision
// recovery with Retry when the loop goes on and with Abandon otherwise. It
// returns nil when the loop starts the next attempt.
func (b *Bus) collisionBackoff(runCtx context.Context, state *retryState, lifecycle collisionRecoveryLifecycle, token uint64) error {
	select {
	case <-time.After(collisionBackoffFloor):
	case <-runCtx.Done():
		b.completeCollisionRecovery(lifecycle, token, transport.CollisionRecoveryAbandon)
		return b.wrapRetryError(runCtx.Err())
	case <-state.request.ctx.Done():
		b.completeCollisionRecovery(lifecycle, token, transport.CollisionRecoveryAbandon)
		return b.wrapRetryError(state.request.ctx.Err())
	}
	if waitErr := b.waitForSyn(runCtx, state.request.ctx, 2); waitErr != nil {
		b.completeCollisionRecovery(lifecycle, token, transport.CollisionRecoveryAbandon)
		b.emitRetryStateComplete(state, nil, state.attemptCount, waitErr)
		return b.wrapRetryError(waitErr)
	}
	b.completeCollisionRecovery(lifecycle, token, transport.CollisionRecoveryRetry)
	return nil
}

// adapterResetBackoff waits the fixed adapter-reset stabilization delay. It
// returns nil when the loop starts the next attempt.
func (b *Bus) adapterResetBackoff(runCtx, reqCtx context.Context) error {
	select {
	case <-time.After(adapterResetRetryDelay):
		return nil
	case <-runCtx.Done():
		return b.wrapRetryError(runCtx.Err())
	case <-reqCtx.Done():
		return b.wrapRetryError(reqCtx.Err())
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

func (b *Bus) collisionRecoveryLifecycle() (collisionRecoveryLifecycle, uint64) {
	lifecycle, ok := b.transport.(collisionRecoveryLifecycle)
	if !ok {
		return nil, 0
	}
	return lifecycle, lifecycle.CollisionRecoveryToken()
}

func (b *Bus) completeCollisionRecovery(lifecycle collisionRecoveryLifecycle, token uint64, decision transport.CollisionRecoveryDecision) {
	if lifecycle != nil && token != 0 {
		lifecycle.CompleteCollisionRecovery(token, decision)
	}
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

func (b *Bus) waitForSyn(runCtx, reqCtx context.Context, count int) error {
	if count <= 0 {
		return nil
	}
	if b.unescapedTransport {
		return b.waitForSynUnescaped(runCtx, reqCtx, count)
	}
	return b.waitForSynPlain(runCtx, reqCtx, count)
}

// waitForSynUnescaped is waitForSyn on a transport that delivers unescaped
// bytes.
func (b *Bus) waitForSynUnescaped(runCtx, reqCtx context.Context, count int) error {
	// ENH: bytes are pre-unescaped at the transport layer. Post-
	// F-23 (batch-19, 2026-05-13), this includes user-payload
	// 0xAA bytes that were wire-encoded as `0xA9 0x01`. The
	// transport surfaces those with the WasEscaped flag set so
	// SYN-counting logic can distinguish them from real wire
	// SYNs. Without this distinction (per Codex bot review on
	// Project-Helianthus/helianthus-ebusgo#154), an unrelated
	// in-progress telegram carrying an escaped 0xAA could
	// falsely satisfy waitForSyn during collision retry/backoff
	// and let the bus try to re-arbitrate while traffic is
	// still flowing.
	//
	// Use the EscapeFlaggedReader interface when the transport
	// implements it. Transports that don't (none today; future
	// "ENH-class" transports that claim BytesAreUnescaped() but
	// emit escape-decoded SYNs as bare bytes) would be the
	// callers responsible for implementing the interface — this
	// branch is the gate.
	flagged, _ := b.transport.(transport.EscapeFlaggedReader)
	seen := 0
	for seen < count {
		if err := b.contextError(runCtx, reqCtx); err != nil {
			return err
		}
		var raw byte
		var wasEscaped bool
		var err error
		if flagged != nil {
			raw, wasEscaped, err = b.readByteWithEscape(runCtx, reqCtx, flagged)
		} else {
			raw, err = b.readByte(runCtx, reqCtx)
			wasEscaped = false
		}
		if err != nil {
			if errors.Is(err, ebuserrors.ErrTimeout) ||
				errors.Is(err, ebuserrors.ErrAdapterReset) ||
				errors.Is(err, ebuserrors.ErrInvalidPayload) {
				continue
			}
			return err
		}
		if raw == SymbolSyn && !wasEscaped {
			seen++
		}
	}
	return nil
}

// waitForSynPlain is waitForSyn on a transport that delivers wire bytes.
func (b *Bus) waitForSynPlain(runCtx, reqCtx context.Context, count int) error {
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
