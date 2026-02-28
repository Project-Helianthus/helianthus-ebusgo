package determinism

import (
	"errors"
	"reflect"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

func TestNewRetrySchedule(t *testing.T) {
	t.Parallel()

	schedule, err := NewRetrySchedule([]time.Duration{0, 25 * time.Millisecond, time.Second})
	if err != nil {
		t.Fatalf("NewRetrySchedule error = %v", err)
	}
	if got := schedule.Retries(); got != 3 {
		t.Fatalf("Retries = %d; want 3", got)
	}
	if got := schedule.Delays(); !reflect.DeepEqual(got, []time.Duration{0, 25 * time.Millisecond, time.Second}) {
		t.Fatalf("Delays = %v; want exact sequence", got)
	}

	gotCopy := schedule.Delays()
	gotCopy[0] = time.Hour
	if got := schedule.Delays()[0]; got != 0 {
		t.Fatalf("Delays copy mutated original; got %s", got)
	}

	if _, err := NewRetrySchedule([]time.Duration{-1}); !errors.Is(err, ErrInvalidRetryDelay) {
		t.Fatalf("NewRetrySchedule(negative) error = %v; want ErrInvalidRetryDelay", err)
	}
}

func TestFixedRetrySchedule(t *testing.T) {
	t.Parallel()

	schedule, err := FixedRetrySchedule(3, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("FixedRetrySchedule error = %v", err)
	}
	if got := schedule.Delays(); !reflect.DeepEqual(got, []time.Duration{200 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond}) {
		t.Fatalf("Delays = %v; want fixed sequence", got)
	}

	if _, err := FixedRetrySchedule(-1, time.Second); !errors.Is(err, ErrInvalidRetryCount) {
		t.Fatalf("FixedRetrySchedule(negative retries) error = %v; want ErrInvalidRetryCount", err)
	}
	if _, err := FixedRetrySchedule(1, -time.Second); !errors.Is(err, ErrInvalidRetryDelay) {
		t.Fatalf("FixedRetrySchedule(negative delay) error = %v; want ErrInvalidRetryDelay", err)
	}
}

func TestExponentialRetrySchedule(t *testing.T) {
	t.Parallel()

	schedule, err := ExponentialRetrySchedule(4, 100*time.Millisecond, 2, 350*time.Millisecond)
	if err != nil {
		t.Fatalf("ExponentialRetrySchedule error = %v", err)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 350 * time.Millisecond, 350 * time.Millisecond}
	if got := schedule.Delays(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Delays = %v; want %v", got, want)
	}

	if _, err := ExponentialRetrySchedule(-1, time.Millisecond, 2, 0); !errors.Is(err, ErrInvalidRetryCount) {
		t.Fatalf("ExponentialRetrySchedule(negative retries) error = %v; want ErrInvalidRetryCount", err)
	}
	if _, err := ExponentialRetrySchedule(1, -time.Millisecond, 2, 0); !errors.Is(err, ErrInvalidRetryDelay) {
		t.Fatalf("ExponentialRetrySchedule(negative delay) error = %v; want ErrInvalidRetryDelay", err)
	}
	if _, err := ExponentialRetrySchedule(1, time.Millisecond, 0, 0); !errors.Is(err, ErrInvalidRetryFactor) {
		t.Fatalf("ExponentialRetrySchedule(invalid factor) error = %v; want ErrInvalidRetryFactor", err)
	}
}

func TestRetrySchedule_DelayAndNextRetry(t *testing.T) {
	t.Parallel()

	schedule, err := NewRetrySchedule([]time.Duration{10 * time.Millisecond, 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewRetrySchedule error = %v", err)
	}

	if delay, ok := schedule.Delay(-1); ok || delay != 0 {
		t.Fatalf("Delay(-1) = (%s, %v); want (0,false)", delay, ok)
	}
	if delay, ok := schedule.Delay(2); ok || delay != 0 {
		t.Fatalf("Delay(out-of-range) = (%s, %v); want (0,false)", delay, ok)
	}

	if delay, ok := schedule.NextRetry(ebuserrors.ErrTimeout, 0); !ok || delay != 10*time.Millisecond {
		t.Fatalf("NextRetry(timeout,0) = (%s,%v); want (10ms,true)", delay, ok)
	}
	if delay, ok := schedule.NextRetry(ebuserrors.ErrTimeout, 3); ok || delay != 0 {
		t.Fatalf("NextRetry(timeout,exhausted) = (%s,%v); want (0,false)", delay, ok)
	}
	if delay, ok := schedule.NextRetry(ebuserrors.ErrNoSuchDevice, 0); ok || delay != 0 {
		t.Fatalf("NextRetry(definitive,0) = (%s,%v); want (0,false)", delay, ok)
	}
}
