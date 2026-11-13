package determinism

import (
	"errors"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

var (
	// ErrInvalidRetryCount is returned when a schedule is asked for a
	// negative number of retries.
	ErrInvalidRetryCount = errors.New("determinism: retry count must be >= 0")
	// ErrInvalidRetryDelay is returned when a delay, base delay or maximum
	// delay is negative.
	ErrInvalidRetryDelay = errors.New("determinism: retry delay must be >= 0")
	// ErrInvalidRetryFactor is returned when an exponential factor is below 1.
	ErrInvalidRetryFactor = errors.New("determinism: retry factor must be >= 1")
)

// RetrySchedule is an immutable, deterministic retry-delay sequence.
type RetrySchedule struct {
	delays []time.Duration
}

// NewRetrySchedule returns a schedule with one retry per element of delays,
// in order, and keeps its own copy of the slice. An empty slice gives a
// schedule without retries; a negative delay fails with
// ErrInvalidRetryDelay.
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

// FixedRetrySchedule returns retries retries that all wait delay. A negative
// count fails with ErrInvalidRetryCount and a negative delay with
// ErrInvalidRetryDelay.
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

// ExponentialRetrySchedule returns retries delays that start at baseDelay
// and are multiplied by factor after each retry. When maxDelay is positive
// it caps every delay; zero means no cap. Growth saturates at the largest
// time.Duration instead of overflowing, and factor 1 gives a fixed
// schedule. It fails with ErrInvalidRetryCount, ErrInvalidRetryDelay for a
// negative baseDelay or maxDelay, or ErrInvalidRetryFactor for a factor
// below 1.
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

// Retries returns the number of retries after the first attempt, so a
// caller makes at most Retries()+1 attempts.
func (schedule RetrySchedule) Retries() int {
	return len(schedule.delays)
}

// Delays returns a copy of the delays in retry order; changing it does not
// change the schedule.
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
