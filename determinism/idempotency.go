package determinism

import (
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrIdempotencyConflict = errors.New("determinism: idempotency key reused with different fingerprint")
	ErrInvalidKey          = errors.New("determinism: idempotency key must not be empty")
	ErrInvalidFingerprint  = errors.New("determinism: idempotency fingerprint must not be empty")
)

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

func (s *IdempotencyStore) Delete(key string) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, trimmed)
	s.mu.Unlock()
}

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
