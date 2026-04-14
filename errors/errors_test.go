package errors_test

import (
	stderrors "errors"
	"fmt"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
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
			name:       "ErrAdapterHostError",
			err:        ebuserrors.ErrAdapterHostError,
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

func TestNormalizeErrorCodeAndCategory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		err      error
		code     ebuserrors.Code
		category ebuserrors.Category
	}{
		{
			name:     "invalid payload",
			err:      ebuserrors.ErrInvalidPayload,
			code:     ebuserrors.CodeInvalidPayload,
			category: ebuserrors.CategoryInvalid,
		},
		{
			name:     "no such device",
			err:      ebuserrors.ErrNoSuchDevice,
			code:     ebuserrors.CodeNoSuchDevice,
			category: ebuserrors.CategoryDefinitive,
		},
		{
			name:     "nack",
			err:      ebuserrors.ErrNACK,
			code:     ebuserrors.CodeNACK,
			category: ebuserrors.CategoryDefinitive,
		},
		{
			name:     "timeout",
			err:      ebuserrors.ErrTimeout,
			code:     ebuserrors.CodeTimeout,
			category: ebuserrors.CategoryTransient,
		},
		{
			name:     "bus collision",
			err:      ebuserrors.ErrBusCollision,
			code:     ebuserrors.CodeBusCollision,
			category: ebuserrors.CategoryTransient,
		},
		{
			name:     "retry exhausted",
			err:      ebuserrors.ErrRetryExhausted,
			code:     ebuserrors.CodeRetryExhausted,
			category: ebuserrors.CategoryTransient,
		},
		{
			name:     "crc mismatch",
			err:      ebuserrors.ErrCRCMismatch,
			code:     ebuserrors.CodeCRCMismatch,
			category: ebuserrors.CategoryTransient,
		},
		{
			name:     "transport closed",
			err:      ebuserrors.ErrTransportClosed,
			code:     ebuserrors.CodeTransportClosed,
			category: ebuserrors.CategoryFatal,
		},
		{
			name:     "adapter host error",
			err:      ebuserrors.ErrAdapterHostError,
			code:     ebuserrors.CodeAdapterHostError,
			category: ebuserrors.CategoryDefinitive,
		},
		{
			name:     "queue full",
			err:      ebuserrors.ErrQueueFull,
			code:     ebuserrors.CodeQueueFull,
			category: ebuserrors.CategoryInvalid,
		},
		{
			name:     "unknown",
			err:      stderrors.New("unknown"),
			code:     ebuserrors.CodeUnknown,
			category: ebuserrors.CategoryUnknown,
		},
		{
			name:     "nil",
			err:      nil,
			code:     ebuserrors.CodeUnknown,
			category: ebuserrors.CategoryUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wrapped := tc.err
			if tc.err != nil {
				wrapped = fmt.Errorf("wrapped: %w", tc.err)
			}

			if got := ebuserrors.NormalizeErrorCode(wrapped); got != tc.code {
				t.Fatalf("NormalizeErrorCode(%s) = %q; want %q", tc.name, got, tc.code)
			}
			if got := ebuserrors.NormalizeErrorCategory(wrapped); got != tc.category {
				t.Fatalf("NormalizeErrorCategory(%s) = %q; want %q", tc.name, got, tc.category)
			}
		})
	}
}

func TestNormalizeSourceLayer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want ebuserrors.SourceLayer
	}{
		{in: "ebusgo", want: ebuserrors.SourceLayerEbusgo},
		{in: " EBUS-GO ", want: ebuserrors.SourceLayerEbusgo},
		{in: "ebus_reg", want: ebuserrors.SourceLayerEbusreg},
		{in: "registry", want: ebuserrors.SourceLayerEbusreg},
		{in: "graphql", want: ebuserrors.SourceLayerGateway},
		{in: "MCP", want: ebuserrors.SourceLayerGateway},
		{in: "", want: ebuserrors.SourceLayerUnknown},
		{in: "other", want: ebuserrors.SourceLayerUnknown},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := ebuserrors.NormalizeSourceLayer(tc.in); got != tc.want {
				t.Fatalf("NormalizeSourceLayer(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("known sentinel defaults to ebusgo source", func(t *testing.T) {
		t.Parallel()

		got := ebuserrors.NormalizeErrorMapping(fmt.Errorf("wrapped: %w", ebuserrors.ErrTimeout), "")
		want := ebuserrors.Mapping{
			Code:        ebuserrors.CodeTimeout,
			Category:    ebuserrors.CategoryTransient,
			Retriable:   true,
			SourceLayer: ebuserrors.SourceLayerEbusgo,
		}

		if got != want {
			t.Fatalf("NormalizeErrorMapping(timeout) = %#v; want %#v", got, want)
		}
	})

	t.Run("explicit source is normalized and preserved", func(t *testing.T) {
		t.Parallel()

		got := ebuserrors.NormalizeErrorMapping(ebuserrors.ErrNACK, " Registry ")
		want := ebuserrors.Mapping{
			Code:        ebuserrors.CodeNACK,
			Category:    ebuserrors.CategoryDefinitive,
			Retriable:   false,
			SourceLayer: ebuserrors.SourceLayerEbusreg,
		}
		if got != want {
			t.Fatalf("NormalizeErrorMapping(nack, registry) = %#v; want %#v", got, want)
		}
	})

	t.Run("explicit unknown source stays unknown", func(t *testing.T) {
		t.Parallel()

		got := ebuserrors.NormalizeErrorMapping(ebuserrors.ErrTimeout, "future-layer")
		want := ebuserrors.Mapping{
			Code:        ebuserrors.CodeTimeout,
			Category:    ebuserrors.CategoryTransient,
			Retriable:   true,
			SourceLayer: ebuserrors.SourceLayerUnknown,
		}
		if got != want {
			t.Fatalf("NormalizeErrorMapping(timeout, future-layer) = %#v; want %#v", got, want)
		}
	})

	t.Run("unknown error keeps unknown classification", func(t *testing.T) {
		t.Parallel()

		got := ebuserrors.NormalizeErrorMapping(stderrors.New("boom"), "gateway")
		want := ebuserrors.Mapping{
			Code:        ebuserrors.CodeUnknown,
			Category:    ebuserrors.CategoryUnknown,
			Retriable:   false,
			SourceLayer: ebuserrors.SourceLayerGateway,
		}
		if got != want {
			t.Fatalf("NormalizeErrorMapping(unknown, gateway) = %#v; want %#v", got, want)
		}
	})
}
