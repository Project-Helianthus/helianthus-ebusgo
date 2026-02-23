package determinism

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestIdempotencyStore_LookupStoreAndConflict(t *testing.T) {
	t.Parallel()

	store := NewIdempotencyStore(time.Minute)
	if err := store.Store("req-1", "sig-1", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("Store error = %v", err)
	}

	got, ok, err := store.Lookup("req-1", "sig-1")
	if err != nil {
		t.Fatalf("Lookup error = %v", err)
	}
	if !ok {
		t.Fatalf("Lookup ok = false; want true")
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("Lookup value = %q; want payload", got)
	}

	got[0] = '{'
	again, ok, err := store.Lookup("req-1", "sig-1")
	if err != nil || !ok {
		t.Fatalf("Lookup(second) = (%v, %v); want hit,nil", ok, err)
	}
	if string(again) != `{"ok":true}` {
		t.Fatalf("Lookup(second) value = %q; want immutable copy", again)
	}

	_, _, err = store.Lookup("req-1", "sig-other")
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("Lookup(conflict) error = %v; want ErrIdempotencyConflict", err)
	}
}

func TestIdempotencyStore_Validation(t *testing.T) {
	t.Parallel()

	store := NewIdempotencyStore(time.Minute)
	if err := store.Store("  ", "sig", []byte("x")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Store(empty key) error = %v; want ErrInvalidKey", err)
	}
	if err := store.Store("k", "  ", []byte("x")); !errors.Is(err, ErrInvalidFingerprint) {
		t.Fatalf("Store(empty fingerprint) error = %v; want ErrInvalidFingerprint", err)
	}

	if _, _, err := store.Lookup("", "sig"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Lookup(empty key) error = %v; want ErrInvalidKey", err)
	}
	if _, _, err := store.Lookup("k", ""); !errors.Is(err, ErrInvalidFingerprint) {
		t.Fatalf("Lookup(empty fingerprint) error = %v; want ErrInvalidFingerprint", err)
	}
}

func TestIdempotencyStore_ExpiryAndLen(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 23, 22, 0, 0, 0, time.UTC)
	store := NewIdempotencyStore(10 * time.Second)
	store.now = func() time.Time { return now }

	if err := store.Store("req-1", "sig-1", []byte("a")); err != nil {
		t.Fatalf("Store(req-1) error = %v", err)
	}
	if err := store.Store("req-2", "sig-2", []byte("b")); err != nil {
		t.Fatalf("Store(req-2) error = %v", err)
	}
	if got := store.Len(); got != 2 {
		t.Fatalf("Len(before expiry) = %d; want 2", got)
	}

	now = now.Add(11 * time.Second)
	if got := store.Len(); got != 0 {
		t.Fatalf("Len(after expiry) = %d; want 0", got)
	}

	if _, ok, err := store.Lookup("req-1", "sig-1"); err != nil || ok {
		t.Fatalf("Lookup(expired) = (%v, %v); want false,nil", ok, err)
	}
}

func TestIdempotencyStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	store := NewIdempotencyStore(time.Minute)

	const workers = 16
	const iterations = 40
	var wg sync.WaitGroup
	wg.Add(workers)

	for worker := 0; worker < workers; worker++ {
		worker := worker
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := "k-" + string(rune('a'+worker%4))
				if err := store.Store(key, "sig", []byte("ok")); err != nil {
					t.Errorf("Store error = %v", err)
					return
				}
				if _, _, err := store.Lookup(key, "sig"); err != nil {
					t.Errorf("Lookup error = %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
}
