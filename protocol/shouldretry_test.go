package protocol

import (
	"errors"
	"fmt"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

func TestShouldRetry_AdapterHostError_NeverRetries(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{TimeoutRetries: 5, NACKRetries: 3}

	// Direct sentinel.
	retry, _, _ := shouldRetry(ebuserrors.ErrAdapterHostError, policy, 0, 0, true)
	if retry {
		t.Fatal("shouldRetry(ErrAdapterHostError) = true; want false")
	}

	// Wrapped sentinel (as produced by enh_transport).
	wrapped := fmt.Errorf("enh arbitration host error 0x03: %w", ebuserrors.ErrAdapterHostError)
	retry, _, _ = shouldRetry(wrapped, policy, 0, 0, true)
	if retry {
		t.Fatal("shouldRetry(wrapped ErrAdapterHostError) = true; want false")
	}

	// Even with unbounded collision enabled, HOST errors must not retry.
	retry, _, _ = shouldRetry(ebuserrors.ErrAdapterHostError, policy, 0, 0, true)
	if retry {
		t.Fatal("shouldRetry(ErrAdapterHostError, unbounded) = true; want false")
	}
}

func TestShouldRetry_AdapterHostError_PreservesCounters(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{TimeoutRetries: 5, NACKRetries: 3}
	retry, to, nack := shouldRetry(ebuserrors.ErrAdapterHostError, policy, 2, 1, false)
	if retry {
		t.Fatal("shouldRetry returned true for ErrAdapterHostError")
	}
	if to != 2 {
		t.Fatalf("timeout attempts = %d; want 2 (unchanged)", to)
	}
	if nack != 1 {
		t.Fatalf("nack attempts = %d; want 1 (unchanged)", nack)
	}
}

func TestShouldRetry_CollisionRetries(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{TimeoutRetries: 2, NACKRetries: 1}

	// Collision with unbounded should always retry.
	retry, _, _ := shouldRetry(ebuserrors.ErrBusCollision, policy, 0, 0, true)
	if !retry {
		t.Fatal("shouldRetry(ErrBusCollision, unbounded) = false; want true")
	}

	// Collision bounded within budget.
	retry, to, _ := shouldRetry(ebuserrors.ErrBusCollision, policy, 0, 0, false)
	if !retry {
		t.Fatal("shouldRetry(ErrBusCollision, bounded, 0) = false; want true")
	}
	if to != 1 {
		t.Fatalf("timeout attempts = %d; want 1", to)
	}

	// Collision bounded exhausted.
	retry, _, _ = shouldRetry(ebuserrors.ErrBusCollision, policy, 2, 0, false)
	if retry {
		t.Fatal("shouldRetry(ErrBusCollision, bounded, exhausted) = true; want false")
	}
}

func TestPreAllocatedErrors_WrapCorrectSentinels(t *testing.T) {
	t.Parallel()

	if !errors.Is(errUnknownFrameType, ebuserrors.ErrInvalidPayload) {
		t.Fatal("errUnknownFrameType does not wrap ErrInvalidPayload")
	}
	if !errors.Is(errCommandAckLoopExhausted, ebuserrors.ErrTimeout) {
		t.Fatal("errCommandAckLoopExhausted does not wrap ErrTimeout")
	}
}
