package determinism

import (
	"errors"
	"reflect"
	"testing"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

func TestCanonicalJSON_StableAcrossMapOrder(t *testing.T) {
	t.Parallel()

	first := map[string]any{
		"z": 1,
		"a": map[string]any{
			"k2": "v2",
			"k1": "v1",
		},
		"l": []any{"b", 3, true},
	}
	second := map[string]any{
		"l": []any{"b", 3, true},
		"a": map[string]any{
			"k1": "v1",
			"k2": "v2",
		},
		"z": 1,
	}

	canonicalFirst, err := CanonicalJSON(first)
	if err != nil {
		t.Fatalf("CanonicalJSON(first) error = %v", err)
	}
	canonicalSecond, err := CanonicalJSON(second)
	if err != nil {
		t.Fatalf("CanonicalJSON(second) error = %v", err)
	}

	if string(canonicalFirst) != string(canonicalSecond) {
		t.Fatalf("canonical mismatch:\nfirst=%s\nsecond=%s", canonicalFirst, canonicalSecond)
	}

	hashFirst, err := CanonicalHash(first)
	if err != nil {
		t.Fatalf("CanonicalHash(first) error = %v", err)
	}
	hashSecond, err := CanonicalHash(second)
	if err != nil {
		t.Fatalf("CanonicalHash(second) error = %v", err)
	}
	if hashFirst != hashSecond {
		t.Fatalf("CanonicalHash mismatch: first=%s second=%s", hashFirst, hashSecond)
	}
}

func TestCanonicalClone_DeepCopy(t *testing.T) {
	t.Parallel()

	source := map[string]any{
		"obj": map[string]any{"v": "x"},
		"arr": []any{1, "a"},
	}
	clonedAny, err := CanonicalClone(source)
	if err != nil {
		t.Fatalf("CanonicalClone error = %v", err)
	}
	cloned, ok := clonedAny.(map[string]any)
	if !ok {
		t.Fatalf("CanonicalClone type = %T; want map[string]any", clonedAny)
	}

	inner := cloned["obj"].(map[string]any)
	inner["v"] = "changed"
	arr := cloned["arr"].([]any)
	arr[0] = "changed"

	canonicalSource, err := CanonicalJSON(source)
	if err != nil {
		t.Fatalf("CanonicalJSON(source) error = %v", err)
	}
	canonicalCloned, err := CanonicalJSON(cloned)
	if err != nil {
		t.Fatalf("CanonicalJSON(cloned) error = %v", err)
	}
	if string(canonicalSource) == string(canonicalCloned) {
		t.Fatalf("source mutated via clone; canonical source=%s cloned=%s", canonicalSource, canonicalCloned)
	}

	freshCloneAny, err := CanonicalClone(source)
	if err != nil {
		t.Fatalf("CanonicalClone(fresh) error = %v", err)
	}
	freshCanonical, err := CanonicalJSON(freshCloneAny)
	if err != nil {
		t.Fatalf("CanonicalJSON(fresh clone) error = %v", err)
	}
	if string(canonicalSource) != string(freshCanonical) {
		t.Fatalf("fresh clone canonical mismatch: source=%s fresh=%s", canonicalSource, freshCanonical)
	}
}

func TestCanonicalJSON_ErrorsWrapInvalidPayload(t *testing.T) {
	t.Parallel()

	invalid := map[string]any{"fn": func() {}}
	if _, err := CanonicalJSON(invalid); !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("CanonicalJSON(invalid) error = %v; want ErrInvalidPayload", err)
	}
	if _, err := CanonicalClone(invalid); !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("CanonicalClone(invalid) error = %v; want ErrInvalidPayload", err)
	}
	if _, err := CanonicalHash(invalid); !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("CanonicalHash(invalid) error = %v; want ErrInvalidPayload", err)
	}
}

func TestCanonicalClone_PreservesEquivalentContent(t *testing.T) {
	t.Parallel()

	in := map[string]any{
		"n": 1,
		"b": true,
		"s": "x",
		"a": []any{1, 2, 3},
	}
	clone, err := CanonicalClone(in)
	if err != nil {
		t.Fatalf("CanonicalClone error = %v", err)
	}

	inCanonical, err := CanonicalClone(in)
	if err != nil {
		t.Fatalf("CanonicalClone(in 2) error = %v", err)
	}
	if !reflect.DeepEqual(clone, inCanonical) {
		t.Fatalf("CanonicalClone output mismatch: got=%#v want=%#v", clone, inCanonical)
	}
}
