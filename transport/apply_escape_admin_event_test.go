package transport

import (
	"sync/atomic"
	"testing"
	"time"
)

// Phase 1 Step B1 (frame-atomic-visibility v8 §1.3 / §5, invariant
// I4): deterministic unit tests for applyEscapeAdminEvent — the
// admin-event-to-counter routing extracted from
// feedEscapeDecoderLocked per Codex round-3 review on PR #165.
//
// The integration tests in enh_transport_v8_counters_test.go cover
// the wiring for Recovery, BudgetExhausted, and AaAbsorbed via the
// end-to-end ENH stream path. The wall-clock timeout case cannot be
// integration-tested deterministically (see the NOTE in that file
// + Codex round-2 finding) because net.Pipe.Write returning does
// not prove the decoder has been fed yet. These pure-function tests
// close that gap: they feed each AdminEvent kind into
// applyEscapeAdminEvent directly and assert the counter side-effects
// PLUS the dropEmission return value, without depending on
// scheduler timing or net.Pipe semantics.

// pinPendingTimeout asserts that AdminEventEscapePendingTimeout
// increments ONLY the pending-timeout counter and returns
// dropEmission=false (data-preserving recovery).
func TestApplyEscapeAdminEvent_PendingTimeout(t *testing.T) {
	t.Parallel()

	var pendingTimeout, recovery, budgetExhausted, decodeFault atomic.Uint64

	admin := AdminEvent{
		Kind:     AdminEventEscapePendingTimeout,
		Duration: 33 * time.Millisecond,
		Absorbed: 0,
	}
	drop := applyEscapeAdminEvent(admin, &pendingTimeout, &recovery, &budgetExhausted, &decodeFault)

	if drop {
		t.Error("dropEmission=true for PendingTimeout; want false (data-preserving)")
	}
	if got := pendingTimeout.Load(); got != 1 {
		t.Errorf("pendingTimeout=%d; want 1", got)
	}
	if got := recovery.Load(); got != 0 {
		t.Errorf("recovery=%d; want 0 (only pendingTimeout should increment)", got)
	}
	if got := budgetExhausted.Load(); got != 0 {
		t.Errorf("budgetExhausted=%d; want 0", got)
	}
	if got := decodeFault.Load(); got != 0 {
		t.Errorf("decodeFault=%d; want 0 (timeout is NOT a fault — data-preserving)", got)
	}
}

func TestApplyEscapeAdminEvent_Recovery(t *testing.T) {
	t.Parallel()

	var pendingTimeout, recovery, budgetExhausted, decodeFault atomic.Uint64

	admin := AdminEvent{Kind: AdminEventEscapeRecovery, Absorbed: 0}
	drop := applyEscapeAdminEvent(admin, &pendingTimeout, &recovery, &budgetExhausted, &decodeFault)

	if !drop {
		t.Error("dropEmission=false for Recovery; want true (fault — drop the byte)")
	}
	if got := recovery.Load(); got != 1 {
		t.Errorf("recovery=%d; want 1", got)
	}
	if got := decodeFault.Load(); got != 1 {
		t.Errorf("decodeFault=%d; want 1 (Recovery is a fault)", got)
	}
	if got := pendingTimeout.Load(); got != 0 {
		t.Errorf("pendingTimeout=%d; want 0", got)
	}
	if got := budgetExhausted.Load(); got != 0 {
		t.Errorf("budgetExhausted=%d; want 0", got)
	}
}

func TestApplyEscapeAdminEvent_BudgetExhausted(t *testing.T) {
	t.Parallel()

	var pendingTimeout, recovery, budgetExhausted, decodeFault atomic.Uint64

	admin := AdminEvent{Kind: AdminEventEscapeBudgetExhausted, Absorbed: MaxAaAbsorptionsPerEscapePair}
	drop := applyEscapeAdminEvent(admin, &pendingTimeout, &recovery, &budgetExhausted, &decodeFault)

	if !drop {
		t.Error("dropEmission=false for BudgetExhausted; want true (fault — drop)")
	}
	if got := budgetExhausted.Load(); got != 1 {
		t.Errorf("budgetExhausted=%d; want 1", got)
	}
	if got := decodeFault.Load(); got != 1 {
		t.Errorf("decodeFault=%d; want 1 (BudgetExhausted is a fault)", got)
	}
	if got := pendingTimeout.Load(); got != 0 {
		t.Errorf("pendingTimeout=%d; want 0", got)
	}
	if got := recovery.Load(); got != 0 {
		t.Errorf("recovery=%d; want 0", got)
	}
}

func TestApplyEscapeAdminEvent_None(t *testing.T) {
	t.Parallel()

	var pendingTimeout, recovery, budgetExhausted, decodeFault atomic.Uint64

	admin := AdminEvent{Kind: AdminEventNone}
	drop := applyEscapeAdminEvent(admin, &pendingTimeout, &recovery, &budgetExhausted, &decodeFault)

	if drop {
		t.Error("dropEmission=true for None; want false (no event, emission proceeds)")
	}
	if got := pendingTimeout.Load(); got != 0 {
		t.Errorf("pendingTimeout=%d; want 0", got)
	}
	if got := recovery.Load(); got != 0 {
		t.Errorf("recovery=%d; want 0", got)
	}
	if got := budgetExhausted.Load(); got != 0 {
		t.Errorf("budgetExhausted=%d; want 0", got)
	}
	if got := decodeFault.Load(); got != 0 {
		t.Errorf("decodeFault=%d; want 0", got)
	}
}

// TestApplyEscapeAdminEvent_TimeoutDoesNotIncrementDecodeFault is a
// targeted regression guard against the most likely refactor mistake:
// someone treating PendingTimeout as a "fault" and adding it to
// DecodeFaultTotal. Per v8 §1.3 the timeout path is data-preserving
// (the current byte is re-emitted), so it MUST NOT be a fault.
func TestApplyEscapeAdminEvent_TimeoutDoesNotIncrementDecodeFault(t *testing.T) {
	t.Parallel()

	var pendingTimeout, recovery, budgetExhausted, decodeFault atomic.Uint64

	// Fire the timeout admin event many times — none should
	// increment decodeFault.
	for i := 0; i < 100; i++ {
		_ = applyEscapeAdminEvent(
			AdminEvent{Kind: AdminEventEscapePendingTimeout},
			&pendingTimeout, &recovery, &budgetExhausted, &decodeFault,
		)
	}

	if got := pendingTimeout.Load(); got != 100 {
		t.Errorf("pendingTimeout=%d; want 100", got)
	}
	if got := decodeFault.Load(); got != 0 {
		t.Errorf("decodeFault=%d; want 0 (timeout MUST NOT count toward fault — v8 §1.3 data-preserving invariant)", got)
	}
}

// TestApplyEscapeAdminEvent_FaultsBothIncrementDecodeFault is the
// dual regression guard: Recovery and BudgetExhausted MUST both
// increment decodeFault (the legacy fault counter operators are
// already watching). If a refactor decouples them, this test fires.
func TestApplyEscapeAdminEvent_FaultsBothIncrementDecodeFault(t *testing.T) {
	t.Parallel()

	var pendingTimeout, recovery, budgetExhausted, decodeFault atomic.Uint64

	_ = applyEscapeAdminEvent(
		AdminEvent{Kind: AdminEventEscapeRecovery},
		&pendingTimeout, &recovery, &budgetExhausted, &decodeFault,
	)
	_ = applyEscapeAdminEvent(
		AdminEvent{Kind: AdminEventEscapeBudgetExhausted},
		&pendingTimeout, &recovery, &budgetExhausted, &decodeFault,
	)

	if got := recovery.Load(); got != 1 {
		t.Errorf("recovery=%d; want 1", got)
	}
	if got := budgetExhausted.Load(); got != 1 {
		t.Errorf("budgetExhausted=%d; want 1", got)
	}
	// DecodeFault should be 2 (Recovery + BudgetExhausted).
	if got := decodeFault.Load(); got != 2 {
		t.Errorf("decodeFault=%d; want 2 (both faults must increment the legacy counter)", got)
	}
}
