package errors_test

import (
	stderrors "errors"
	"fmt"
	"testing"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

func TestClassifiers_NilAndUnknown(t *testing.T) {
	t.Parallel()

	if ebuserrors.IsTransient(nil) {
		t.Fatal("IsTransient(nil) = true; want false")
	}
	if ebuserrors.IsDefinitive(nil) {
		t.Fatal("IsDefinitive(nil) = true; want false")
	}
	if ebuserrors.IsFatal(nil) {
		t.Fatal("IsFatal(nil) = true; want false")
	}

	unknown := stderrors.New("unknown")
	if ebuserrors.IsTransient(unknown) {
		t.Fatal("IsTransient(unknown) = true; want false")
	}
	if ebuserrors.IsDefinitive(unknown) {
		t.Fatal("IsDefinitive(unknown) = true; want false")
	}
	if ebuserrors.IsFatal(unknown) {
		t.Fatal("IsFatal(unknown) = true; want false")
	}
}

func TestClassifiers_CoverAllSentinels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		transient  bool
		definitive bool
		fatal      bool
	}{
		{
			name:      "ErrBusCollision",
			err:       ebuserrors.ErrBusCollision,
			transient: true,
		},
		{
			name:      "ErrTimeout",
			err:       ebuserrors.ErrTimeout,
			transient: true,
		},
		{
			name:      "ErrCRCMismatch",
			err:       ebuserrors.ErrCRCMismatch,
			transient: true,
		},
		{
			name:      "ErrRetryExhausted",
			err:       ebuserrors.ErrRetryExhausted,
			transient: true,
		},
		{
			name:       "ErrNoSuchDevice",
			err:        ebuserrors.ErrNoSuchDevice,
			definitive: true,
		},
		{
			name:       "ErrNACK",
			err:        ebuserrors.ErrNACK,
			definitive: true,
		},
		{
			name:  "ErrTransportClosed",
			err:   ebuserrors.ErrTransportClosed,
			fatal: true,
		},
		{
			name:  "ErrInvalidPayload",
			err:   ebuserrors.ErrInvalidPayload,
			fatal: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("wrapped: %w", tc.err)

			if got := ebuserrors.IsTransient(wrapped); got != tc.transient {
				t.Fatalf("IsTransient(%s) = %v; want %v", tc.name, got, tc.transient)
			}
			if got := ebuserrors.IsDefinitive(wrapped); got != tc.definitive {
				t.Fatalf("IsDefinitive(%s) = %v; want %v", tc.name, got, tc.definitive)
			}
			if got := ebuserrors.IsFatal(wrapped); got != tc.fatal {
				t.Fatalf("IsFatal(%s) = %v; want %v", tc.name, got, tc.fatal)
			}

			matches := 0
			if tc.transient {
				matches++
			}
			if tc.definitive {
				matches++
			}
			if tc.fatal {
				matches++
			}
			if matches != 1 {
				t.Fatalf("test case %s classifies into %d categories; want exactly 1", tc.name, matches)
			}
		})
	}
}

