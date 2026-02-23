package determinism

import (
	"errors"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

var (
	ErrInvalidRetryCount  = errors.New("determinism: retry count must be >= 0")
	ErrInvalidRetryDelay  = errors.New("determinism: retry delay must be >= 0")
	ErrInvalidRetryFactor = errors.New("determinism: retry factor must be >= 1")
)

// RetrySchedule is an immutable, deterministic retry-delay sequence.
type RetrySchedule struct {
	delays []time.Duration
}

func NewRetrySchedule(delays []time.Duration) (RetrySchedule, error) {
	out := make([]time.Duration, len(delays))
	for index, delay := range delays {
		if delay < 0 {
			return RetrySchedule{}, ErrInvalidRetryDelay
		}
		out[index] = delay
	}
	return RetrySchedule{delays: out}, nil
}

func FixedRetrySchedule(retries int, delay time.Duration) (RetrySchedule, error) {
	if retries < 0 {
		return RetrySchedule{}, ErrInvalidRetryCount
	}
	if delay < 0 {
		return RetrySchedule{}, ErrInvalidRetryDelay
	}
	delays := make([]time.Duration, retries)
	for index := range delays {
		delays[index] = delay
	}
	return RetrySchedule{delays: delays}, nil
}

func ExponentialRetrySchedule(retries int, baseDelay time.Duration, factor int, maxDelay time.Duration) (RetrySchedule, error) {
	if retries < 0 {
		return RetrySchedule{}, ErrInvalidRetryCount
	}
	if baseDelay < 0 || maxDelay < 0 {
		return RetrySchedule{}, ErrInvalidRetryDelay
	}
	if factor < 1 {
		return RetrySchedule{}, ErrInvalidRetryFactor
	}

	delays := make([]time.Duration, retries)
	current := baseDelay
	for index := 0; index < retries; index++ {
		if maxDelay > 0 && current > maxDelay {
			delays[index] = maxDelay
		} else {
			delays[index] = current
		}

		if factor == 1 {
			continue
		}
		if current > 0 && current > time.Duration((1<<63-1)/factor) {
			current = time.Duration(1<<63 - 1)
			continue
		}
		current *= time.Duration(factor)
	}

	return RetrySchedule{delays: delays}, nil
}

func (schedule RetrySchedule) Retries() int {
	return len(schedule.delays)
}

func (schedule RetrySchedule) Delays() []time.Duration {
	out := make([]time.Duration, len(schedule.delays))
	copy(out, schedule.delays)
	return out
}

// Delay returns the delay for the zero-based retry index.
// retryIndex 0 is the delay before the second attempt.
func (schedule RetrySchedule) Delay(retryIndex int) (time.Duration, bool) {
	if retryIndex < 0 || retryIndex >= len(schedule.delays) {
		return 0, false
	}
	return schedule.delays[retryIndex], true
}

// NextRetry returns whether an error is retriable and, if it is, which delay
// should be applied before the next attempt.
func (schedule RetrySchedule) NextRetry(err error, retryIndex int) (time.Duration, bool) {
	if !ebuserrors.NormalizeErrorMapping(err, "").Retriable {
		return 0, false
	}
	return schedule.Delay(retryIndex)
}
