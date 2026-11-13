package determinism

import (
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	// ErrIdempotencyConflict is returned by IdempotencyStore.Lookup when a
	// live entry exists for the key but was stored under a different
	// fingerprint; the stored result is not returned.
	ErrIdempotencyConflict = errors.New("determinism: idempotency key reused with different fingerprint")
	// ErrInvalidKey is returned when a key is empty after trimming spaces.
	ErrInvalidKey = errors.New("determinism: idempotency key must not be empty")
	// ErrInvalidFingerprint is returned when a fingerprint is empty after
	// trimming spaces.
	ErrInvalidFingerprint = errors.New("determinism: idempotency fingerprint must not be empty")
)

// DefaultIdempotencyTTL is the entry lifetime NewIdempotencyStore uses when
// it is given a TTL that is zero or negative.
const DefaultIdempotencyTTL = 30 * time.Second

type idempotencyEntry struct {
	fingerprint string
	value       []byte
	expiresAt   time.Time
}

// IdempotencyStore keeps mutate results keyed by idempotency key and payload fingerprint.
// The store is concurrency-safe and evicts expired entries lazily on access.
type IdempotencyStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]idempotencyEntry
}

// NewIdempotencyStore returns an empty store whose entries expire ttl after
// they are stored; a ttl that is zero or negative selects
// DefaultIdempotencyTTL. The store reads the wall clock.
func NewIdempotencyStore(ttl time.Duration) *IdempotencyStore {
	if ttl <= 0 {
		ttl = DefaultIdempotencyTTL
	}
	return &IdempotencyStore{
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]idempotencyEntry),
	}
}

// Lookup returns a copy of the result stored under key and true when a live
// entry exists with the same fingerprint, and nil, false, nil when there is
// none. Key and fingerprint are trimmed first; an empty one fails with
// ErrInvalidKey or ErrInvalidFingerprint, and a live entry with another
// fingerprint fails with ErrIdempotencyConflict. A stored empty result comes
// back as nil with true. Lookup first evicts every expired entry.
func (s *IdempotencyStore) Lookup(key string, fingerprint string) ([]byte, bool, error) {
	key, fingerprint, err := normalizeLookupInputs(key, fingerprint)
	if err != nil {
		return nil, false, err
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)

	entry, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	if entry.fingerprint != fingerprint {
		return nil, false, ErrIdempotencyConflict
	}
	return cloneBytes(entry.value), true, nil
}

// Store records a copy of value under the trimmed key and fingerprint and
// restarts the entry's TTL. It replaces any entry under the same key, even
// one with another fingerprint, so callers that must not overwrite a
// conflicting result check Lookup first. It fails only with ErrInvalidKey or
// ErrInvalidFingerprint, and then stores nothing.
func (s *IdempotencyStore) Store(key string, fingerprint string, value []byte) error {
	key, fingerprint, err := normalizeLookupInputs(key, fingerprint)
	if err != nil {
		return err
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)

	s.entries[key] = idempotencyEntry{
		fingerprint: fingerprint,
		value:       cloneBytes(value),
		expiresAt:   now.Add(s.ttl),
	}
	return nil
}

// Delete removes the entry under the trimmed key, whatever its fingerprint.
// An empty or unknown key is a no-op.
func (s *IdempotencyStore) Delete(key string) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, trimmed)
	s.mu.Unlock()
}

// Len evicts expired entries and returns the number of live ones.
func (s *IdempotencyStore) Len() int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)
	return len(s.entries)
}

func (s *IdempotencyStore) purgeExpiredLocked(now time.Time) {
	for key, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, key)
		}
	}
}

func normalizeLookupInputs(key string, fingerprint string) (string, string, error) {
	normalizedKey := strings.TrimSpace(key)
	if normalizedKey == "" {
		return "", "", ErrInvalidKey
	}
	normalizedFingerprint := strings.TrimSpace(fingerprint)
	if normalizedFingerprint == "" {
		return "", "", ErrInvalidFingerprint
	}
	return normalizedKey, normalizedFingerprint, nil
}

func cloneBytes(in []byte) []byte {
	if len(in) == 0 {
		return nil
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}
