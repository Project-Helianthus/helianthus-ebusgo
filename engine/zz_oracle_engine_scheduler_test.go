package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestOracleEngineScheduler records the pin's remaining scheduler stimuli:
// half-open recovery, a disabled breaker, the probe limit and the shadow
// served watch read. The clock and fetch schedule are those of the pin's own
// tests.
func TestOracleEngineScheduler(t *testing.T) {
	oracleSchedulerHalfOpenRecovery(t)
	oracleSchedulerDisabledBreaker(t)
	oracleSchedulerProbeLimit(t)
	oracleSchedulerShadowHit(t)
}

func oracleTransitionRows(events []SemanticReadCircuitBreakerTransition) []any {
	rows := make([]any, 0, len(events))
	for _, event := range events {
		rows = append(rows, map[string]any{"key": event.Key, "from": string(event.From), "to": string(event.To), "consecutive_failures": event.ConsecutiveFailures})
	}
	return rows
}

func oracleSchedulerHalfOpenRecovery(t *testing.T) {
	t.Helper()
	now := time.Unix(0, 0)
	scheduler := NewSemanticReadScheduler()
	scheduler.now = func() time.Time { return now }
	var transitions []SemanticReadCircuitBreakerTransition
	scheduler.SetCircuitBreaker(SemanticReadCircuitBreakerOptions{
		FailureBudget: 2, OpenCooldown: 15 * time.Second, HalfOpenProbeLimit: 1,
		OnTransition: func(event SemanticReadCircuitBreakerTransition) { transitions = append(transitions, event) },
	})
	var calls int32
	fetch := func(context.Context) ([]byte, error) {
		call := atomic.AddInt32(&calls, 1)
		switch call {
		case 1, 2:
			return nil, errors.New("fail-closed")
		case 3:
			return []byte{0x7A}, nil
		default:
			return nil, errors.New("fail-after-recover")
		}
	}
	steps := make([]any, 0, 7)
	get := func(name string) {
		value, err := scheduler.Get(context.Background(), "k", 0, fetch)
		steps = append(steps, map[string]any{"step": name, "value": hex.EncodeToString(value), "error": oracleErr(err), "circuit_open": errors.Is(err, ErrSemanticReadCircuitOpen), "fetch_calls": atomic.LoadInt32(&calls)})
	}
	get("failure-1")
	get("failure-2")
	get("open")
	now = now.Add(15 * time.Second)
	get("half-open-recovery")
	get("post-recovery-failure-1")
	get("post-recovery-failure-2")
	get("post-recovery-open")
	oracleWrite(t, "scheduler/half-open-success-resets-budget.json", oracleCase{
		Component: "semantic_read_scheduler", Source: "semantic_read_scheduler.go", Stimulus: "TestSemanticReadScheduler_CircuitBreaker_HalfOpenSuccessResetsBudget",
		Input: oracleInput{
			Bytes:      "7a",
			Qualifiers: map[string]any{"key": "k", "failure_budget": 2, "half_open_probe_limit": 1, "fetch_schedule": "fail fail ok fail fail"},
			Clock:      map[string]any{"start": time.Unix(0, 0).UTC().Format(time.RFC3339Nano), "open_cooldown_ns": int64(15 * time.Second), "advance_before_recovery_ns": int64(15 * time.Second)},
		},
		Output: oracleOutput{Decisions: steps, Observations: oracleTransitionRows(transitions), Counters: map[string]any{"fetch_calls": atomic.LoadInt32(&calls), "transitions": len(transitions)}},
	})
}

func oracleSchedulerDisabledBreaker(t *testing.T) {
	t.Helper()
	scheduler := NewSemanticReadScheduler()
	scheduler.SetCircuitBreaker(SemanticReadCircuitBreakerOptions{FailureBudget: 0})
	var calls int32
	fetch := func(context.Context) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("read failed")
	}
	steps := make([]any, 0, 3)
	for attempt := 1; attempt <= 3; attempt++ {
		_, err := scheduler.Get(context.Background(), "k", 0, fetch)
		steps = append(steps, map[string]any{"attempt": attempt, "error": oracleErr(err), "circuit_open": errors.Is(err, ErrSemanticReadCircuitOpen), "fetch_calls": atomic.LoadInt32(&calls)})
	}
	oracleWrite(t, "scheduler/breaker-disabled-at-zero-budget.json", oracleCase{
		Component: "semantic_read_scheduler", Source: "semantic_read_scheduler.go", Stimulus: "TestSemanticReadScheduler_CircuitBreaker_DisabledWhenFailureBudgetZero",
		Input:  oracleInput{Qualifiers: map[string]any{"key": "k", "failure_budget": 0, "attempts": 3}},
		Output: oracleOutput{Decisions: steps, Counters: map[string]any{"fetch_calls": atomic.LoadInt32(&calls)}},
	})
}

func oracleSchedulerProbeLimit(t *testing.T) {
	t.Helper()
	now := time.Unix(0, 0)
	scheduler := NewSemanticReadScheduler()
	scheduler.now = func() time.Time { return now }
	scheduler.SetCircuitBreaker(SemanticReadCircuitBreakerOptions{FailureBudget: 1, OpenCooldown: 10 * time.Second, HalfOpenProbeLimit: 2})
	var calls int32
	fetch := func(context.Context) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("read failed")
	}
	steps := make([]any, 0, 5)
	get := func(name string) {
		_, err := scheduler.Get(context.Background(), "k", 0, fetch)
		steps = append(steps, map[string]any{"step": name, "error": oracleErr(err), "circuit_open": errors.Is(err, ErrSemanticReadCircuitOpen), "fetch_calls": atomic.LoadInt32(&calls)})
	}
	get("initial-failure")
	get("open")
	now = now.Add(10 * time.Second)
	get("half-open-probe-1")
	get("half-open-probe-2")
	get("post-probe")
	oracleWrite(t, "scheduler/half-open-probe-limit.json", oracleCase{
		Component: "semantic_read_scheduler", Source: "semantic_read_scheduler.go", Stimulus: "TestSemanticReadScheduler_CircuitBreaker_HalfOpenProbeLimitRespected",
		Input: oracleInput{
			Qualifiers: map[string]any{"key": "k", "failure_budget": 1, "half_open_probe_limit": 2},
			Clock:      map[string]any{"start": time.Unix(0, 0).UTC().Format(time.RFC3339Nano), "open_cooldown_ns": int64(10 * time.Second), "advance_before_probes_ns": int64(10 * time.Second)},
		},
		Output: oracleOutput{Decisions: steps, Counters: map[string]any{"fetch_calls": atomic.LoadInt32(&calls)}},
	})
}

func oracleSchedulerShadowHit(t *testing.T) {
	t.Helper()
	key := NewB509WatchKey(0x08, 0x0200)
	now := time.Unix(100, 0)
	catalog, activations := testShadowCatalogAndActivations(t, []WatchKey{key}, WatchActivationSourcePoller)
	cache := newTestShadowCache(t, catalog, activations, now, ShadowCacheOptions{})
	writeShadow(t, cache, key, ShadowWriteSourcePassive, now, []byte{0xAB})
	scheduler := NewSemanticReadScheduler()
	scheduler.now = func() time.Time { return now }
	scheduler.SetShadowCache(cache)
	var fetchCalls int32
	value, stats, err := scheduler.GetWatchWithStats(context.Background(), key, time.Second, func(context.Context) ([]byte, error) {
		atomic.AddInt32(&fetchCalls, 1)
		return []byte{0xCD}, nil
	})
	oracleWrite(t, "scheduler/watch-read-served-from-passive-shadow.json", oracleCase{
		Component: "semantic_read_scheduler", Source: "semantic_read_scheduler.go", Stimulus: "TestSemanticReadScheduler_GetWatchReturnsShadowHitWithoutFetch/GetWatchWithStats_TracksShadowHit",
		Input: oracleInput{
			Bytes:      "ab",
			Qualifiers: map[string]any{"key": oracleKeyCanonical(key), "max_age_ns": int64(time.Second), "write_source": "passive", "fetch_would_return": "cd"},
			Clock:      map[string]any{"now": now.UTC().Format(time.RFC3339Nano)},
		},
		Output: oracleOutput{
			Decisions: []any{map[string]any{
				"returned_value": hex.EncodeToString(value), "error": oracleErr(err), "served_from_shadow": stats.ServedFromShadow, "served_from_passive_shadow": stats.ServedFromPassiveShadow,
				"active_fetch_attempted": stats.ActiveFetchAttempted, "active_fetch_succeeded": stats.ActiveFetchSucceeded, "active_fetch_duration_ns": int64(stats.ActiveFetchDuration),
			}},
			Counters: map[string]any{"fetch_calls": atomic.LoadInt32(&fetchCalls)},
		},
	})
}
