// Package determinism provides the building blocks that make repeated
// requests produce the same result: canonical hashing of request payloads,
// an idempotency store for mutation results and fixed retry-delay schedules.
// It performs no bus I/O.
//
// Canonical hashing: CanonicalClone, CanonicalJSON and CanonicalHash accept
// only JSON-encodable values. Values are normalized through a JSON round
// trip, so map key order does not matter and numbers keep their decimal
// text as json.Number; equivalent payloads therefore hash to the same
// lowercase hex SHA-256 digest. Values that cannot be encoded fail with an
// error that wraps errors.ErrInvalidPayload from this module.
//
// Idempotency: IdempotencyStore is safe for concurrent use. Keys and
// fingerprints are trimmed and must not be empty. A key reused with a
// different fingerprint returns ErrIdempotencyConflict rather than the stored
// result. Entries expire a fixed TTL after Store (DefaultIdempotencyTTL when
// the configured TTL is not positive) and are evicted lazily on the next
// access; Store replaces any entry under the same key. Stored and returned
// byte slices are copies, so callers keep ownership of their buffers.
//
// Retry schedules: RetrySchedule is an immutable list of delays. Retry index
// 0 is the delay before the second attempt. Constructors reject negative
// counts and delays and factors below 1; exponential schedules cap each
// delay at the maximum when one is set and saturate instead of overflowing.
// NextRetry retries only errors that this module's errors package classifies
// as transient.
package determinism
