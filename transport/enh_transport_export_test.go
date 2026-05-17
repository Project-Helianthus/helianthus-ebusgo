//go:build !tinygo

package transport

import "time"

// SetPostGrantPreEchoTimeoutForTest temporarily overrides
// postGrantPreEchoTimeout for the duration of a test and returns a
// cleanup function that restores the original value.
//
// Tests MUST invoke the cleanup function (usually via defer) to avoid
// cross-test contamination. Exported (Capital) so callers in the
// external transport_test package can reach it; lives in an _test.go
// file so it never ships in the production build.
//
// Rationale: the production constant was widened to 5s in batch-24
// round-5 to cover the entire gateway transaction duration. Two
// deadline-expiry tests (TestENHTransport_PostGrantWindow_Deadline-
// ExpiresWithSYNEcho and TestPostGrantWindowExpired_IncrementedOn-
// Deadline) need a much shorter timeout to assert the expiry branch
// without adding 5s+ of sleep to each. This helper lets them set
// e.g. 50ms locally without altering the production value.
//
// Concurrency: NOT safe for parallel tests. The override mutates a
// package-level var that other tests (notably
// TestPostGrantPreEchoTimeout_CoversTransactionDuration) read at
// runtime. Any test that calls this helper MUST NOT call t.Parallel,
// and the falsifiability test that asserts the production value must
// also be serial — otherwise the override leaks across the parallel
// scheduling boundary and the asserted constant value races.
func SetPostGrantPreEchoTimeoutForTest(d time.Duration) (cleanup func()) {
	prev := postGrantPreEchoTimeout
	postGrantPreEchoTimeout = d
	return func() { postGrantPreEchoTimeout = prev }
}
