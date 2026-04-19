// M4c1 PR-B: responder ACK timing harness.
//
// Measures the elapsed wall-clock time between the moment the inbound
// frame's final CRC byte was parsed OK (t0) and the moment the first ACK
// byte is handed to the transport write path (t1). Compares elapsed
// against responderAckBudget.
//
// Clock abstraction: tests inject a deterministic Clock; production uses
// realClock (time.Now). This keeps the harness unit-testable without
// relying on real wall-clock latency.
//
// BENCH-REPLACE obligation: the 15 ms budget below is a literature-backed
// placeholder from the Vaillant eBUS target-response window cited in the
// M4b1 spike artifact (§4 timing-budget row) and the eBUS Adapter 3.1
// documentation. Per decision doc §7.1 no-go criterion (1), the operator
// MUST run this harness on live BASV2 hardware before M4c2 IMPL GREEN and
// replace the placeholder with the measured value. This PR ships the
// measurement infrastructure; the measured constant is a separate
// follow-up commit.

package responder

import (
	"reflect"
	"sync"
	"time"
)

// responderAckBudget is the upper bound on the CRC-to-ACK elapsed window.
//
// BENCH-REPLACE: measured value from BASV2 bench pending per decision
// §7.1 no-go criterion (1). Literature-backed placeholder derived from
// the Vaillant eBUS target-response window (~15 ms after the initiator's
// final CRC byte) cited in `_spike/m4b1-responder-feasibility.md` §4 and
// the eBUS Adapter 3.1 documentation. Community cross-reference:
// ebusd-esp32#14 on arbitration/turnaround timing.
const responderAckBudget time.Duration = 15 * time.Millisecond

// Clock is the time source used by the timing harness. `time.Now` is the
// production implementation; tests inject a fake for determinism.
type Clock interface {
	Now() time.Time
}

// realClock is the production Clock (time.Now).
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// TimingHarness measures the CRC-to-ACK elapsed window for a single
// responder transaction. Each transaction should use a fresh harness (or
// call Reset between uses). Safe for concurrent use; in practice one
// transaction runs at a time.
type TimingHarness struct {
	mu            sync.Mutex
	clock         Clock
	inboundCRCAt  time.Time
	ackEmitAt     time.Time
	haveInboundAt bool
	haveAckAt     bool
}

// NewTimingHarness constructs a harness backed by the real clock.
func NewTimingHarness() *TimingHarness {
	return &TimingHarness{clock: realClock{}}
}

// NewTimingHarnessWithClock constructs a harness with an injected clock.
func NewTimingHarnessWithClock(c Clock) *TimingHarness {
	if c == nil {
		c = realClock{}
	}
	return &TimingHarness{clock: c}
}

// MarkInboundCRCOk records t0 — the moment the inbound frame's final CRC
// byte was parsed OK. If `at` is the zero time, the harness's clock is
// consulted.
func (h *TimingHarness) MarkInboundCRCOk(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if at.IsZero() {
		at = h.clock.Now()
	}
	h.inboundCRCAt = at
	h.haveInboundAt = true
}

// MarkAckEmit records t1 — the moment the first ACK byte was handed to
// the transport write path. If `at` is the zero time, the harness's clock
// is consulted.
func (h *TimingHarness) MarkAckEmit(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if at.IsZero() {
		at = h.clock.Now()
	}
	h.ackEmitAt = at
	h.haveAckAt = true
}

// Elapsed returns t1 - t0. Returns 0 and false if either mark is missing.
func (h *TimingHarness) Elapsed() (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.haveInboundAt || !h.haveAckAt {
		return 0, false
	}
	return h.ackEmitAt.Sub(h.inboundCRCAt), true
}

// WithinBudget returns true iff Elapsed() is present and <=
// responderAckBudget.
func (h *TimingHarness) WithinBudget() bool {
	elapsed, ok := h.Elapsed()
	if !ok {
		return false
	}
	return elapsed <= responderAckBudget
}

// Budget returns the currently configured ACK budget.
func (h *TimingHarness) Budget() time.Duration {
	return responderAckBudget
}

// Reset clears the recorded marks so the harness can be reused.
func (h *TimingHarness) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inboundCRCAt = time.Time{}
	h.ackEmitAt = time.Time{}
	h.haveInboundAt = false
	h.haveAckAt = false
}

func init() {
	registerExport("TimingHarness", reflect.TypeOf((*TimingHarness)(nil)))
}
