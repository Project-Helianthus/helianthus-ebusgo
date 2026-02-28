package types_test

import (
	"errors"
	"reflect"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/types"
)

func assertValue(t *testing.T, got, want types.Value) {
	t.Helper()

	if got.Valid != want.Valid {
		t.Fatalf("Valid = %v; want %v", got.Valid, want.Valid)
	}
	if !got.Valid {
		return
	}
	if !reflect.DeepEqual(got.Value, want.Value) {
		t.Fatalf("Value = %#v (%T); want %#v (%T)", got.Value, got.Value, want.Value, want.Value)
	}
}

func assertInvalidPayload(t *testing.T, err error) {
	t.Helper()

	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("error = %v; want ErrInvalidPayload", err)
	}
}
