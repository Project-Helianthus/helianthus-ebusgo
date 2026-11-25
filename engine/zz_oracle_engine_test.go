package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// TestOracleEngine records the pin's scheduler, shadow cache, passive tap,
// deduplicator, reconstructor, watch catalog and observe-first policy.
// The stimuli are the pin's own tests.
func TestOracleEngine(t *testing.T) {
	oracleRecordScheduler(t)
	oracleRecordShadow(t)
	oracleRecordTap(t)
	oracleRecordDedup(t)
	oracleRecordReconstructor(t)
	oracleRecordWatchCatalog(t)
	oracleRecordObserveFirst(t)
}

func oracleRecordScheduler(t *testing.T) {
	t.Helper()
	oracleSchedulerCoalesce(t)
	oracleSchedulerCache(t)
	oracleSchedulerRefresh(t)
	oracleSchedulerBreaker(t)
}

func oracleSchedulerCoalesce(t *testing.T) {
	t.Helper()
	scheduler := NewSemanticReadScheduler()
	scheduler.now = func() time.Time { return time.Unix(0, 0) }
	var calls int32
	fetch := func(context.Context) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(25 * time.Millisecond)
		return []byte{0x01, 0x02}, nil
	}
	var wg sync.WaitGroup
	results := make([][]byte, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = scheduler.Get(context.Background(), "k", time.Second, fetch)
		}(i)
	}
	wg.Wait()
	oracleWrite(t, "scheduler/coalesce.json", oracleCase{
		Component: "semantic_read_scheduler",
		Source:    "semantic_read_scheduler.go",
		Stimulus:  "TestSemanticReadScheduler_CoalescesConcurrentGets",
		Input: oracleInput{
			Bytes:      "0102",
			Qualifiers: map[string]any{"key": "k", "max_age_ns": int64(time.Second), "concurrent_gets": 2, "fetch_returns": "0102", "fetch_duration_ns": int64(25 * time.Millisecond)},
			Clock:      map[string]any{"now": time.Unix(0, 0).UTC().Format(time.RFC3339Nano)},
		},
		Output: oracleOutput{
			Decisions: []any{map[string]any{"returned_values": []any{hex.EncodeToString(results[0]), hex.EncodeToString(results[1])}, "errors": []any{oracleErr(errs[0]), oracleErr(errs[1])}}},
			Counters:  map[string]any{"fetch_calls": atomic.LoadInt32(&calls)},
		},
	})
}

func oracleSchedulerCache(t *testing.T) {
	t.Helper()
	now := time.Unix(0, 0)
	scheduler := NewSemanticReadScheduler()
	scheduler.now = func() time.Time { return now }
	var calls int32
	fetch := func(context.Context) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte{0xAA}, nil
	}
	first, err1 := scheduler.Get(context.Background(), "k", time.Second, fetch)
	second, err2 := scheduler.Get(context.Background(), "k", time.Second, fetch)
	oracleWrite(t, "scheduler/cache-within-max-age.json", oracleCase{
		Component: "semantic_read_scheduler",
		Source:    "semantic_read_scheduler.go",
		Stimulus:  "TestSemanticReadScheduler_UsesCacheWithinMaxAge",
		Input: oracleInput{
			Bytes:      "aa",
			Qualifiers: map[string]any{"key": "k", "max_age_ns": int64(time.Second), "gets": 2, "fetch_returns": "aa"},
			Clock:      map[string]any{"now": now.UTC().Format(time.RFC3339Nano)},
		},
		Output: oracleOutput{
			Decisions: []any{map[string]any{"returned_values": []any{hex.EncodeToString(first), hex.EncodeToString(second)}, "errors": []any{oracleErr(err1), oracleErr(err2)}}},
			Cache:     map[string]any{"served_from_cache": atomic.LoadInt32(&calls) == 1},
			Counters:  map[string]any{"fetch_calls": atomic.LoadInt32(&calls)},
		},
	})
}

func oracleSchedulerRefresh(t *testing.T) {
	t.Helper()
	now := time.Unix(0, 0)
	scheduler := NewSemanticReadScheduler()
	scheduler.now = func() time.Time { return now }
	var calls int32
	fetch := func(context.Context) ([]byte, error) {
		call := atomic.AddInt32(&calls, 1)
		return []byte{byte(call)}, nil
	}
	first, err1 := scheduler.Get(context.Background(), "k", 10*time.Millisecond, fetch)
	now = now.Add(25 * time.Millisecond)
	second, err2 := scheduler.Get(context.Background(), "k", 10*time.Millisecond, fetch)
	oracleWrite(t, "scheduler/refresh-after-max-age.json", oracleCase{
		Component: "semantic_read_scheduler",
		Source:    "semantic_read_scheduler.go",
		Stimulus:  "TestSemanticReadScheduler_RefreshesAfterMaxAge",
		Input: oracleInput{
			Qualifiers: map[string]any{"key": "k", "max_age_ns": int64(10 * time.Millisecond), "gets": 2, "fetch_returns": "call number n returns the single byte n"},
			Clock:      map[string]any{"start": time.Unix(0, 0).UTC().Format(time.RFC3339Nano), "advance_ns": int64(25 * time.Millisecond)},
		},
		Output: oracleOutput{
			Decisions: []any{map[string]any{"returned_values": []any{hex.EncodeToString(first), hex.EncodeToString(second)}, "errors": []any{oracleErr(err1), oracleErr(err2)}}},
			Counters:  map[string]any{"fetch_calls": atomic.LoadInt32(&calls)},
		},
	})
}

func oracleSchedulerBreaker(t *testing.T) {
	t.Helper()
	now := time.Unix(0, 0)
	scheduler := NewSemanticReadScheduler()
	scheduler.now = func() time.Time { return now }
	var transitions []SemanticReadCircuitBreakerTransition
	var suppressions []SemanticReadCircuitBreakerSuppression
	scheduler.SetCircuitBreaker(SemanticReadCircuitBreakerOptions{
		FailureBudget:      2,
		OpenCooldown:       15 * time.Second,
		HalfOpenProbeLimit: 1,
		OnTransition: func(event SemanticReadCircuitBreakerTransition) {
			transitions = append(transitions, event)
		},
		OnSuppressed: func(event SemanticReadCircuitBreakerSuppression) {
			suppressions = append(suppressions, event)
		},
	})
	var calls int32
	fetch := func(context.Context) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("read failed")
	}
	steps := make([]any, 0, 5)
	// schedule lists each Get call in call order with the clock offset at
	// which it is made; the fetch behaviour is the same for every call.
	start := now
	schedule := make([]any, 0, 5)
	record := func(name string) {
		schedule = append(schedule, map[string]any{"order": len(schedule) + 1, "key": "k", "max_age_ns": int64(0), "clock_offset_ns": int64(now.Sub(start))})
		_, err := scheduler.Get(context.Background(), "k", 0, fetch)
		steps = append(steps, map[string]any{"step": name, "error": oracleErr(err), "circuit_open": errors.Is(err, ErrSemanticReadCircuitOpen), "fetch_calls": atomic.LoadInt32(&calls)})
	}
	record("failure-1")
	record("failure-2")
	record("open")
	now = now.Add(15 * time.Second)
	record("half-open-probe")
	record("suppressed")
	recordedTransitions := make([]any, 0, len(transitions))
	for _, event := range transitions {
		recordedTransitions = append(recordedTransitions, map[string]any{
			"key": event.Key, "from": string(event.From), "to": string(event.To), "consecutive_failures": event.ConsecutiveFailures,
		})
	}
	recordedSuppressions := make([]any, 0, len(suppressions))
	for _, event := range suppressions {
		recordedSuppressions = append(recordedSuppressions, map[string]any{
			"key": event.Key, "state": string(event.State), "suppressed_total": event.SuppressedTotal, "retry_after_ns": int64(event.RetryAfter),
		})
	}
	oracleWrite(t, "scheduler/circuit-open-and-suppress.json", oracleCase{
		Component: "semantic_read_scheduler",
		Source:    "semantic_read_scheduler.go",
		Stimulus:  "TestSemanticReadScheduler_CircuitBreaker_OpensAndSuppresses",
		Input: oracleInput{
			Qualifiers: map[string]any{"key": "k", "failure_budget": 2, "half_open_probe_limit": 1, "fetch_returns": "no value and the error 'read failed' on every call"},
			Clock:      map[string]any{"start": time.Unix(0, 0).UTC().Format(time.RFC3339Nano), "open_cooldown_ns": int64(15 * time.Second)},
			State:      map[string]any{"gets": schedule},
		},
		Output: oracleOutput{
			Decisions:    steps,
			Observations: []any{map[string]any{"transitions": recordedTransitions, "suppressions": recordedSuppressions}},
			Counters:     map[string]any{"fetch_calls": atomic.LoadInt32(&calls)},
		},
	})
}

func oracleRecordShadow(t *testing.T) {
	t.Helper()
	oracleShadowBudget(t)
	oracleShadowFlags(t)
	oracleShadowEvict(t)
	oracleShadowStale(t)
	oracleShadowPrecedence(t)
}

func oracleShadowBudget(t *testing.T) {
	t.Helper()
	keys := []WatchKey{NewB509WatchKey(0x08, 0x0200), NewB509WatchKey(0x08, 0x0201), NewB509WatchKey(0x08, 0x0202)}
	catalog, activations := testShadowCatalogAndActivations(t, keys, WatchActivationSourcePoller)
	cache := NewShadowCache(ShadowCacheOptions{
		Catalog: catalog, Activations: activations, Capacity: 8, PinnedCapacity: 3, WriteConfirmPinnedCap: 1,
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	result := cache.Write(ShadowWrite{
		Key: keys[0], Source: ShadowWriteSourcePassive, Confidence: ShadowConfidenceHigh, Value: []byte{0x01}, ObservedAt: time.Unix(100, 0),
	})
	oracleWrite(t, "shadow-cache/pinned-budget-reject.json", oracleCase{
		Component: "shadow_cache",
		Source:    "shadow_cache.go",
		Stimulus:  "TestShadowCacheDegradesWhenStaticPinnedFootprintExceedsBudget",
		Input: oracleInput{
			Bytes:      "01",
			Qualifiers: map[string]any{"canonical": keys[0].Canonical(), "source": string(ShadowWriteSourcePassive)},
			Clock:      map[string]any{"now": time.Unix(100, 0).UTC().Format(time.RFC3339Nano)},
			State: map[string]any{
				"capacity": 8, "pinned_capacity": 3, "write_confirm_pinned_cap": 1,
				"catalog_keys":      []any{keys[0].Canonical(), keys[1].Canonical(), keys[2].Canonical()},
				"activation_source": string(WatchActivationSourcePoller),
				"activated_keys":    []any{keys[0].Canonical(), keys[1].Canonical(), keys[2].Canonical()},
			},
		},
		Output: oracleOutput{
			Decisions: []any{oracleWriteResult(result)},
			Cache:     oracleShadowSummary(cache.Summary()),
		},
	})
}

func oracleShadowFlags(t *testing.T) {
	t.Helper()
	key := NewB509WatchKey(0x08, 0x0200)
	catalog, activations := testShadowCatalogAndActivations(t, []WatchKey{key}, WatchActivationSourceTooling)
	cache := NewShadowCache(ShadowCacheOptions{
		Catalog: catalog, Activations: activations,
		FeatureFlags:   NormalizeObserveFirstFeatureFlags(true, true, true, ObserveFirstExternalWritePolicyRecordOnly),
		Capacity:       8,
		PinnedCapacity: 4,
		Now:            func() time.Time { return time.Unix(100, 0) },
	})
	flags := cache.FeatureFlags()
	oracleWrite(t, "shadow-cache/normalized-flags.json", oracleCase{
		Component: "shadow_cache",
		Source:    "shadow_cache.go",
		Stimulus:  "TestShadowCacheStoresNormalizedFeatureFlagSeam",
		Input: oracleInput{
			Qualifiers: map[string]any{"enabled": true, "state_direct": true, "config_direct": true, "policy": string(ObserveFirstExternalWritePolicyRecordOnly)},
			Clock:      map[string]any{"now": time.Unix(100, 0).UTC().Format(time.RFC3339Nano)},
		},
		Output: oracleOutput{Decisions: []any{oracleFlags(flags)}, Cache: oracleShadowSummary(cache.Summary())},
	})
}

func oracleShadowEvict(t *testing.T) {
	t.Helper()
	pinned := NewB509WatchKey(0x08, 0x0200)
	older := NewB509WatchKey(0x08, 0x0201)
	newer := NewB509WatchKey(0x08, 0x0202)
	catalog, activations := testShadowCatalogAndActivations(t, []WatchKey{pinned, older, newer}, WatchActivationSourceTooling)
	if err := activations.Activate(WatchActivationSourcePoller, pinned); err != nil {
		t.Fatal(err)
	}
	cache := newTestShadowCache(t, catalog, activations, time.Unix(100, 0), ShadowCacheOptions{Capacity: 2, PinnedCapacity: 2, WriteConfirmPinnedCap: 1})
	writeShadow(t, cache, pinned, ShadowWriteSourcePassive, time.Unix(100, 0), []byte{0x10})
	writeShadow(t, cache, older, ShadowWriteSourcePassive, time.Unix(101, 0), []byte{0x11})
	writeShadow(t, cache, newer, ShadowWriteSourcePassive, time.Unix(102, 0), []byte{0x12})
	oracleWrite(t, "shadow-cache/evict-oldest.json", oracleCase{
		Component: "shadow_cache",
		Source:    "shadow_cache.go",
		Stimulus:  "TestShadowCacheEvictsOldestEvictableEntryFirst",
		Input: oracleInput{
			Bytes:      "101112",
			Qualifiers: map[string]any{"pinned": pinned.Canonical(), "older": older.Canonical(), "newer": newer.Canonical()},
			Clock:      map[string]any{"base": time.Unix(100, 0).UTC().Format(time.RFC3339Nano)},
			State: map[string]any{
				"capacity": 2, "pinned_capacity": 2, "write_confirm_pinned_cap": 1,
				"catalog_keys": []any{pinned.Canonical(), older.Canonical(), newer.Canonical()},
				"activations": []any{
					map[string]any{"source": string(WatchActivationSourceTooling), "keys": []any{pinned.Canonical(), older.Canonical(), newer.Canonical()}},
					map[string]any{"source": string(WatchActivationSourcePoller), "keys": []any{pinned.Canonical()}},
				},
				"writes": []any{
					map[string]any{"order": 1, "role": "pinned", "key": pinned.Canonical(), "source": string(ShadowWriteSourcePassive), "value": "10", "observed_offset_ns": int64(0)},
					map[string]any{"order": 2, "role": "older", "key": older.Canonical(), "source": string(ShadowWriteSourcePassive), "value": "11", "observed_offset_ns": int64(time.Second)},
					map[string]any{"order": 3, "role": "newer", "key": newer.Canonical(), "source": string(ShadowWriteSourcePassive), "value": "12", "observed_offset_ns": int64(2 * time.Second)},
				},
			},
		},
		Output: oracleOutput{Cache: map[string]any{
			"pinned": oracleEntry(cache, pinned),
			"older":  oracleEntry(cache, older),
			"newer":  oracleEntry(cache, newer),
		}},
	})
}

func oracleShadowStale(t *testing.T) {
	t.Helper()
	key := NewB524WatchKey(0x15, 0x06, 0x03, 0x00, 0x001C)
	catalog, activations := testShadowCatalogAndActivations(t, []WatchKey{key}, WatchActivationSourcePoller)
	cache := newTestShadowCache(t, catalog, activations, time.Unix(100, 0), ShadowCacheOptions{})
	writeShadow(t, cache, key, ShadowWriteSourcePassive, time.Unix(120, 0), []byte{0x20})
	result := cache.Write(ShadowWrite{
		Key: key, Source: ShadowWriteSourceActiveConfirmed, Confidence: ShadowConfidenceHigh,
		Value: []byte{0x21}, ObservedAt: time.Unix(119, 0), StartGeneration: cache.CaptureGeneration(key),
	})
	oracleWrite(t, "shadow-cache/stale-active-confirmed.json", oracleCase{
		Component: "shadow_cache",
		Source:    "shadow_cache.go",
		Stimulus:  "TestShadowCachePrecedenceRejectsOlderActiveConfirmedWrite",
		Input: oracleInput{
			Bytes:      "20",
			Qualifiers: map[string]any{"canonical": key.Canonical(), "rejected_value": "21"},
			Clock:      map[string]any{"kept": time.Unix(120, 0).UTC().Format(time.RFC3339Nano), "rejected": time.Unix(119, 0).UTC().Format(time.RFC3339Nano)},
		},
		Output: oracleOutput{Decisions: []any{oracleWriteResult(result)}, Cache: map[string]any{"entry": oracleEntry(cache, key)}},
	})
}

func oracleShadowPrecedence(t *testing.T) {
	t.Helper()
	key := NewB524WatchKey(0x15, 0x06, 0x03, 0x00, 0x001C)
	catalog, activations := testShadowCatalogAndActivations(t, []WatchKey{key}, WatchActivationSourcePoller)
	cache := newTestShadowCache(t, catalog, activations, time.Unix(100, 0), ShadowCacheOptions{})
	writeShadow(t, cache, key, ShadowWriteSourcePassive, time.Unix(120, 0), []byte{0x20})
	start := cache.CaptureGeneration(key)
	writeShadowWithStart(t, cache, key, ShadowWriteSourceActiveConfirmed, time.Unix(120, 0), []byte{0x21}, start)
	oracleWrite(t, "shadow-cache/equal-timestamp-active-confirmed.json", oracleCase{
		Component: "shadow_cache",
		Source:    "shadow_cache.go",
		Stimulus:  "TestShadowCacheEqualTimestampActiveConfirmedOutranksPassive",
		Input: oracleInput{
			Bytes:      "2021",
			Qualifiers: map[string]any{"canonical": key.Canonical()},
			Clock:      map[string]any{"observed_at": time.Unix(120, 0).UTC().Format(time.RFC3339Nano)},
		},
		Output: oracleOutput{Cache: map[string]any{"entry": oracleEntry(cache, key)}},
	})
}

func oracleRecordTap(t *testing.T) {
	t.Helper()
	for _, protocolName := range []TransportProtocol{TransportEbusdTCP, TransportProtocol("ebusd")} {
		cfg := withDonor(DefaultConfig())
		cfg.TransportConfig = TransportConfig{Protocol: protocolName, Network: "tcp", Address: oracleHostPort("loopback-direct")}
		_, err := StartPassiveBusTap(context.Background(), cfg, newPassiveEventRecorder())
		oracleWrite(t, "passive-tap/reject-"+string(protocolName)+".json", oracleCase{
			Component: "passive_bus_tap",
			Source:    "passive_bus_tap.go",
			Stimulus:  "TestStartPassiveBusTapRejectsEbusdTCP/" + string(protocolName),
			Input: oracleInput{
				Qualifiers: map[string]any{"protocol": string(protocolName), "network": "tcp", "address_class": "loopback-direct"},
			},
			Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(err), "invalid_payload": errors.Is(err, ebuserrors.ErrInvalidPayload)}}},
		})
	}
	oracleTapReconnect(t)
	oracleTapProxySilence(t, "proxy-default")
	oracleTapWireEscape(t)
}

func oracleTapReconnect(t *testing.T) {
	t.Helper()
	recorder := newPassiveEventRecorder()
	var dialMu sync.Mutex
	dialCount := 0
	cfg := withDonor(DefaultConfig())
	cfg.TransportConfig = TransportConfig{
		Protocol: TransportENH, Network: "tcp", Address: oracleHostPort("loopback-direct"),
		ReadTimeout: 50 * time.Millisecond, WriteTimeout: 50 * time.Millisecond, DialTimeout: time.Second,
		Dial: func(context.Context, string, string, time.Duration) (net.Conn, error) {
			dialMu.Lock()
			defer dialMu.Unlock()
			dialCount++
			client, server := net.Pipe()
			switch dialCount {
			case 1:
				go writePassivePayload(server, enhReceivedBytes([]byte{0x11}))
			case 2:
				go writePassivePayload(server, enhReceivedBytes([]byte{0x22}))
			default:
				_ = server.Close()
				return nil, errors.New("unexpected extra reconnect")
			}
			return client, nil
		},
	}
	cfg.PassiveAbsenceThreshold = time.Second
	cfg.PassiveReconnectInitialDelay = 5 * time.Millisecond
	cfg.PassiveReconnectMaxDelay = 5 * time.Millisecond
	tap, err := StartPassiveBusTap(context.Background(), cfg, recorder)
	if err != nil {
		t.Fatal(err)
	}
	events := waitForPassiveEvents(t, recorder, 2*time.Second, func(events []PassiveTapEvent) bool {
		return hasPassiveSymbols(events, 0x11, 0x22) &&
			countPassiveEventKind(events, PassiveTapEventConnected) >= 2 &&
			countPassiveEventKind(events, PassiveTapEventDisconnected) >= 1
	})
	snapshot := tap.Snapshot()
	_ = tap.Close()
	oracleWrite(t, "passive-tap/reconnect.json", oracleCase{
		Component: "passive_bus_tap",
		Source:    "passive_bus_tap.go",
		Stimulus:  "TestPassiveBusTap_ReconnectsAfterDisconnect",
		Input: oracleInput{
			Bytes:      "1122",
			Qualifiers: map[string]any{"protocol": string(TransportENH), "network": "tcp", "address_class": "loopback-direct", "observations_exclude": []any{"read_timeout"}},
			Clock: map[string]any{
				"reconnect_delay_ns": int64(5 * time.Millisecond), "read_timeout_ns": int64(50 * time.Millisecond),
				"absence_threshold_ns": int64(time.Second),
			},
		},
		Output: oracleOutput{
			Decisions: []any{map[string]any{
				"saw_reconnect_symbols": hasPassiveSymbols(events, 0x11, 0x22),
			}},
			Observations: oracleTapEventsThrough(events, 0x22),
			Counters: map[string]any{
				"connect_count_at_least_2":    snapshot.ConnectCount >= 2,
				"disconnect_count_at_least_1": snapshot.DisconnectCount >= 1,
				"observed_symbol_count":       snapshot.ObservedSymbolCount,
			},
		},
	})
}

// oracleTapEventsThrough lists the emitted events in order, up to and
// including the first symbol event equal to last; events after it race with
// the connection teardown and are not part of what the pin's test waits for.
// Read-timeout events depend on when the writer goroutine runs against the
// read deadline, so they are left out of the list and the input names that.
func oracleTapEventsThrough(events []PassiveTapEvent, last byte) []any {
	names := map[PassiveTapEventKind]string{
		PassiveTapEventConnected: "connected", PassiveTapEventDisconnected: "disconnected", PassiveTapEventSymbol: "symbol",
		PassiveTapEventReset: "reset", PassiveTapEventDecodeFault: "decode_fault", PassiveTapEventReadTimeout: "read_timeout",
	}
	out := make([]any, 0, len(events))
	for _, event := range events {
		if event.Kind == PassiveTapEventReadTimeout {
			continue
		}
		entry := map[string]any{"kind": names[event.Kind]}
		if event.Kind == PassiveTapEventSymbol {
			entry["symbol"] = hex.EncodeToString([]byte{event.Symbol})
			entry["was_escaped"] = event.WasEscaped
		}
		out = append(out, entry)
		if event.Kind == PassiveTapEventSymbol && event.Symbol == last {
			break
		}
	}
	return out
}

func oracleTapProxySilence(t *testing.T, class string) {
	t.Helper()
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	recorder := newPassiveEventRecorder()
	cfg := withDonor(DefaultConfig())
	cfg.TransportConfig = TransportConfig{
		Protocol: TransportENS, Network: "tcp", Address: oracleHostPort(class),
		ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond, DialTimeout: time.Second,
		Dial: func(context.Context, string, string, time.Duration) (net.Conn, error) { return client, nil },
	}
	cfg.PassiveAbsenceThreshold = 60 * time.Millisecond
	cfg.PassiveReconnectInitialDelay = 5 * time.Millisecond
	cfg.PassiveReconnectMaxDelay = 5 * time.Millisecond
	tap, err := StartPassiveBusTap(context.Background(), cfg, recorder)
	if err != nil {
		t.Fatal(err)
	}
	waitForPassiveEvents(t, recorder, 2*time.Second, func(events []PassiveTapEvent) bool {
		return countPassiveEventKind(events, PassiveTapEventReadTimeout) >= 3
	})
	time.Sleep(100 * time.Millisecond)
	snapshot := tap.Snapshot()
	_ = tap.Close()
	oracleWrite(t, "passive-tap/proxy-silence-"+class+".json", oracleCase{
		Component: "passive_bus_tap",
		Source:    "passive_bus_tap.go",
		Stimulus:  "TestPassiveBusTap_ProxyLikeEndpointDoesNotReconnectOnReadTimeoutSilence/" + class,
		Input: oracleInput{
			Qualifiers: map[string]any{"protocol": string(TransportENS), "network": "tcp", "address_class": class},
			Clock:      map[string]any{"read_timeout_ns": int64(20 * time.Millisecond), "absence_ns": int64(60 * time.Millisecond)},
		},
		Output: oracleOutput{Decisions: []any{map[string]any{
			"connected":        snapshot.Connected,
			"endpoint_state":   string(snapshot.EndpointState),
			"connect_count":    snapshot.ConnectCount,
			"disconnect_count": snapshot.DisconnectCount,
		}}},
	})
}

func oracleTapWireEscape(t *testing.T) {
	t.Helper()
	cases := []struct {
		name     string
		class    string
		protocol TransportProtocol
		network  string
	}{
		{"remote-direct-ip", "remote-numeric-direct", TransportENH, "tcp"},
		{"remote-direct-name", "remote-name", TransportENS, "tcp"},
		{"loopback-direct", "loopback-direct", TransportENH, "tcp"},
		{"proxy-like", "proxy-default", TransportENS, "tcp"},
		{"custom-remote-port", "remote-numeric-custom-port", TransportENH, "tcp"},
	}
	for _, tc := range cases {
		cfg := withDonor(DefaultConfig())
		cfg.TransportConfig = TransportConfig{Protocol: tc.protocol, Network: tc.network, Address: oracleHostPort(tc.class)}
		oracleWrite(t, "passive-tap/wire-escape-"+tc.name+".json", oracleCase{
			Component: "passive_bus_tap",
			Source:    "passive_bus_tap.go",
			Stimulus:  "TestPassiveTapDecodesWireEscapes_DifferentiatesRemoteDirectAdapterLogicalStreams/" + tc.name,
			Input:     oracleInput{Qualifiers: map[string]any{"protocol": string(tc.protocol), "network": tc.network, "address_class": tc.class}},
			Output:    oracleOutput{Decisions: []any{map[string]any{"decodes_wire_escapes": passiveTapDecodesWireEscapes(cfg)}}},
		})
	}
}

func oracleHostPort(class string) string {
	loop := strings.Join([]string{"127", "0", "0", "1"}, ".")
	remote := strings.Join([]string{"192", "168", "100", "2"}, ".")
	switch class {
	case "remote-numeric-direct":
		return net.JoinHostPort(remote, "9999")
	case "remote-name":
		return net.JoinHostPort("adapter.local", "9999")
	case "loopback-direct":
		return net.JoinHostPort(loop, "9999")
	case "proxy-default":
		return net.JoinHostPort(loop, "19001")
	case "proxy-matrix":
		return net.JoinHostPort(loop, "19183")
	case "remote-numeric-custom-port":
		return net.JoinHostPort(remote, "19183")
	default:
		return ""
	}
}

func oracleRecordDedup(t *testing.T) {
	t.Helper()
	deduplicator := newTestDeduplicator(t)
	subscription, err := deduplicator.Subscribe("test", DedupSubscriberCritical, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	forceHealthyDedup(deduplicator)
	stateKey := NewB524WatchKey(0x15, 0x06, 0x03, 0x01, 0x001C)
	deduplicator.cfg.WatchObserver = staticWatchObserver{byCanonical: map[string]WatchObservation{
		stateKey.Canonical(): {
			State: WatchObservationStateActive,
			Descriptor: WatchDescriptor{
				Key: stateKey, SemanticClass: WatchSemanticClassState,
				CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault,
			},
			HasDescriptor: true,
		},
	}}
	base := time.Unix(0, 0)
	deduplicator.nowFunc = func() time.Time { return base }
	activeRequest := protocol.Frame{Source: 0x31, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x06, 0x00, 0x03, 0x01, 0x1C, 0x00}}
	activeResponse := protocol.Frame{Source: activeRequest.Target, Target: activeRequest.Source, Primary: activeRequest.Primary, Secondary: activeRequest.Secondary, Data: []byte{0x42, 0x01, 0x03, 0x1C, 0x00, 0x22}}
	activeEvent := activeAttemptEvent(activeRequest, activeResponse)
	if err := deduplicator.OnBusEvent(activeEvent); err != nil {
		t.Fatal(err)
	}
	observation := deduplicator.Observe(stateKey)
	passiveRequest := activeRequest
	passiveRequest.Source = 0x10
	passiveResponse := activeResponse
	passiveResponse.Target = passiveRequest.Source
	passiveEvent := passiveTransactionEvent(base.Add(100*time.Millisecond), passiveRequest, passiveResponse)
	deduplicator.OnPassiveClassifiedEvent(passiveEvent)
	assertNoAdjudicatedEvent(t, subscription, 25*time.Millisecond)
	pending := len(deduplicator.pending)
	deduplicator.nowFunc = func() time.Time {
		return base.Add(100*time.Millisecond + deduplicator.budgets.PendingGraceTimeout + time.Millisecond)
	}
	outputs := deduplicator.releaseExpiredPending(deduplicator.now())
	deduplicator.publishAll(outputs)
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	var event AdjudicatedPassiveEvent
	select {
	case out, ok := <-subscription.Events():
		if !ok {
			t.Fatal("subscription closed")
		}
		event = out
	case <-timeout.C:
		t.Fatal("timeout waiting for adjudicated event")
	}
	oracleWrite(t, "deduplicator/b524-runtime-observer-fallback.json", oracleCase{
		Component: "active_passive_deduplicator",
		Source:    "active_passive_deduplicator.go",
		Stimulus:  "TestActivePassiveDeduplicator_B524CorrelatedReadUsesRuntimeObserverFallback",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(append(append([]byte{}, activeRequest.Data...), activeResponse.Data...)),
			Qualifiers: map[string]any{"canonical": stateKey.Canonical(), "passive_source": "10"},
			Clock: map[string]any{
				"base": base.UTC().Format(time.RFC3339Nano), "passive_offset_ns": int64(100 * time.Millisecond),
				"release_offset_ns": int64(100*time.Millisecond + deduplicator.budgets.PendingGraceTimeout + time.Millisecond),
			},
			State: map[string]any{
				"initial_health":    oracleDedupInitialHealth,
				"submissions":       []any{oracleActiveSubmission(activeEvent, 0), oraclePassiveSubmission(passiveEvent, base)},
				"observation_state": string(observation.State),
				"watch_observer": map[string]any{
					"canonical": stateKey.Canonical(), "state": string(WatchObservationStateActive), "has_descriptor": true,
					"descriptor": map[string]any{
						"key": oracleKeyCanonical(stateKey), "semantic_class": string(WatchSemanticClassState),
						"correlation_policy": string(WatchCorrelationPolicyRequestResponse), "direct_apply_policy": string(WatchDirectApplyPolicyStateDefault),
					},
				},
			},
		},
		Output: oracleOutput{
			Decisions: []any{map[string]any{
				"disposition":                        string(event.Disposition),
				"third_party_eligible":               event.ThirdPartyEligible,
				"direct_apply_policy":                string(event.FamilyPolicy.DirectApplyPolicy),
				"family":                             string(event.FamilyPolicy.Family),
				"request_intent":                     string(event.FamilyPolicy.RequestIntent),
				"response_class":                     string(event.FamilyPolicy.ResponseClass),
				"correlation_policy":                 string(event.FamilyPolicy.CorrelationPolicy),
				"uses_runtime_external_write_policy": event.FamilyPolicy.UsesRuntimeExternalWritePolicy,
				"effective_external_write_policy":    string(event.FamilyPolicy.EffectiveExternalWritePolicy),
			}},
			Counters: map[string]any{"pending_before_release": pending, "released": len(outputs)},
		},
	})
}

func oracleRecordReconstructor(t *testing.T) {
	t.Helper()
	oracleReconstructorCase(t, "reconstructor/broadcast.json", "TestPassiveTransactionReconstructor_ClassifiesBroadcast", oracleReconSpec{}, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
		reconstructor, subscription := oracleReconstructor()
		frame := protocol.Frame{Source: 0x10, Target: protocol.AddressBroadcast, Primary: 0xB5, Secondary: 0x16, Data: []byte{0x01, 0x02}}
		payload := frameBytes(frame)
		feedPassiveSymbols(reconstructor, time.Unix(0, 0), payload)
		event := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventBroadcastFrame)
		return payload, []PassiveClassifiedEvent{event}, reconstructor
	})
	oracleReconstructorCase(t, "reconstructor/initiator-initiator.json", "TestPassiveTransactionReconstructor_ClassifiesMasterMaster", oracleReconSpec{}, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
		reconstructor, subscription := oracleReconstructor()
		frame := protocol.Frame{Source: 0x30, Target: 0x10, Primary: 0x01, Secondary: 0x02, Data: []byte{0x03}}
		payload := append(requestFrameBytes(frame), protocol.SymbolAck, protocol.SymbolSyn)
		feedPassiveSymbols(reconstructor, time.Unix(0, 0), payload)
		event := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventMasterFrame)
		return payload, []PassiveClassifiedEvent{event}, reconstructor
	})
	oracleReconstructorCase(t, "reconstructor/direct.json", "TestPassiveTransactionReconstructor_ClassifiesDirectTransaction", oracleReconSpec{}, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
		reconstructor, subscription := oracleReconstructor()
		request := protocol.Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x01}}
		payload := append(requestFrameBytes(request), protocol.SymbolAck)
		payload = append(payload, responseSegmentBytes([]byte{0x11, 0x55})...)
		payload = append(payload, protocol.SymbolAck, protocol.SymbolSyn)
		feedPassiveSymbols(reconstructor, time.Unix(0, 0), payload)
		event := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventTransaction)
		return payload, []PassiveClassifiedEvent{event}, reconstructor
	})
	oracleReconstructorCase(t, "reconstructor/no-response.json", "TestPassiveTransactionReconstructor_NoResponseBecomesAbandoned", oracleReconSpec{}, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
		reconstructor, subscription := oracleReconstructor()
		request := protocol.Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x01}}
		payload := append(requestFrameBytes(request), protocol.SymbolAck, protocol.SymbolSyn)
		feedPassiveSymbols(reconstructor, time.Unix(0, 0), payload)
		event := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventAbandonedTransaction)
		return payload, []PassiveClassifiedEvent{event}, reconstructor
	})
	oracleReconstructorCase(t, "reconstructor/reset.json", "TestPassiveTransactionReconstructor_ResetAbandonsInFlight", oracleReconSpec{TapEvents: []oracleTapEvent{{Kind: "reset", OffsetNS: int64(100 * time.Millisecond)}}}, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
		reconstructor, subscription := oracleReconstructor()
		request := protocol.Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x01}}
		payload := append(requestFrameBytes(request), protocol.SymbolAck)
		base := time.Unix(0, 0)
		feedPassiveSymbols(reconstructor, base, payload)
		reconstructor.OnPassiveTapEvent(PassiveTapEvent{Kind: PassiveTapEventReset, ObservedAt: base.Add(100 * time.Millisecond)})
		abandoned := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventAbandonedTransaction)
		discontinuity := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventDiscontinuity)
		return payload, []PassiveClassifiedEvent{abandoned, discontinuity}, reconstructor
	})
	oracleReconstructorCase(t, "reconstructor/scan-collision.json", "TestPassiveReconstructor_F19a_PreservesClassification_ScanCollision", oracleReconSpec{Raw: true}, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
		reconstructor, subscription := oracleReconstructor()
		payload := []byte{protocol.SymbolSyn, 0x10, 0xFE, 0x07, 0x04, 0x01, 0xAA, 0xAA}
		feedPassiveSymbolsRaw(reconstructor, time.Unix(0, 0), payload)
		event := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventAbandonedTransaction)
		return payload, []PassiveClassifiedEvent{event}, reconstructor
	})
	oracleReconstructorCase(t, "reconstructor/arbitration-fragment.json", "TestPassiveReconstructor_F19b_FourByteFragment_ClassifiesAsArbitrationFragment", oracleReconSpec{Raw: true}, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
		reconstructor, subscription := oracleReconstructor()
		payload := []byte{protocol.SymbolSyn, 0xF1, 0x15, 0xB5, 0x24, protocol.SymbolSyn}
		feedPassiveSymbolsRaw(reconstructor, time.Unix(0, 0), payload)
		event := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventAbandonedTransaction)
		return payload, []PassiveClassifiedEvent{event}, reconstructor
	})
	oracleRecordF19c(t)
}

func oracleReconstructor() (*PassiveTransactionReconstructor, *PassiveClassifiedSubscription) {
	reconstructor := newPassiveTransactionReconstructorCore(withDonor(DefaultConfig()))
	subscription, err := reconstructor.Subscribe("test", PassiveSubscriberCritical, 8)
	if err != nil {
		panic(err)
	}
	return reconstructor, subscription
}

// oracleReconSpec names the inputs of one reconstructor case that the byte
// payload alone does not carry: whether the pin's raw feeder (no prepended SYN)
// or the SYN-prepending feeder was used, the local-address snapshotter, the
// configured watchdog and the non-symbol tap events that follow the stream.
type oracleReconSpec struct {
	Raw         bool
	Watchdog    time.Duration
	Snapshotter *LocalAddressSnapshot
	TapEvents   []oracleTapEvent
}

type oracleTapEvent struct {
	Kind     string
	OffsetNS int64
	Error    string
}

const oracleSymbolSpacing = 10 * time.Millisecond

// oracleFedStream returns the symbols the pin's feeder passes to the
// reconstructor and the per-symbol escape flags it derives, exactly as the
// pin's own helpers compute them.
func oracleFedStream(payload []byte, raw bool) ([]byte, []bool) {
	fed := payload
	if !raw && len(payload) != 0 && payload[0] != protocol.SymbolSyn {
		fed = append([]byte{protocol.SymbolSyn}, payload...)
	}
	return fed, autoEscapeMaskForLegacyTests(fed)
}

func oracleReconstructorInput(payload []byte, spec oracleReconSpec) oracleInput {
	fed, mask := oracleFedStream(payload, spec.Raw)
	escaped := make([]any, 0)
	for index, flag := range mask {
		if flag {
			escaped = append(escaped, index)
		}
	}
	feeder := "feedPassiveSymbols"
	if spec.Raw {
		feeder = "feedPassiveSymbolsRaw"
	}
	watchdog := spec.Watchdog
	if watchdog == 0 {
		watchdog = withDonor(DefaultConfig()).PassiveTransactionWatchdog
	}
	snapshotter := map[string]any{"installed": false}
	if spec.Snapshotter != nil {
		snapshotter = map[string]any{"installed": true, "address": hex.EncodeToString([]byte{spec.Snapshotter.Address}), "known": spec.Snapshotter.Known}
	}
	events := make([]any, 0, len(spec.TapEvents))
	for _, event := range spec.TapEvents {
		entry := map[string]any{"kind": event.Kind, "offset_from_start_ns": event.OffsetNS}
		if event.Error != "" {
			entry["error"] = event.Error
		}
		events = append(events, entry)
	}
	return oracleInput{
		Bytes: hex.EncodeToString(payload),
		Qualifiers: map[string]any{
			"feeder":                  feeder,
			"fed_symbols":             hex.EncodeToString(fed),
			"escaped_symbol_indices":  escaped,
			"tap_events_after_stream": events,
		},
		Clock: map[string]any{
			"start":             time.Unix(0, 0).UTC().Format(time.RFC3339Nano),
			"symbol_spacing_ns": int64(oracleSymbolSpacing),
		},
		State: map[string]any{
			"passive_transaction_watchdog_ns": int64(watchdog),
			"local_address_snapshotter":       snapshotter,
		},
	}
}

func oracleReconstructorCase(t *testing.T, name, stimulus string, spec oracleReconSpec, run func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor)) {
	t.Helper()
	payload, events, reconstructor := run()
	observations := make([]any, 0, len(events))
	for _, event := range events {
		observations = append(observations, oracleClassified(event))
	}
	snapshot := reconstructor.Snapshot()
	oracleWrite(t, name, oracleCase{
		Component: "passive_transaction_reconstructor",
		Source:    "passive_transaction_reconstructor.go",
		Stimulus:  stimulus,
		Input:     oracleReconstructorInput(payload, spec),
		Output: oracleOutput{
			Observations: observations,
			Counters: map[string]any{
				"invalid_src_class_skipped_total": snapshot.InvalidSrcClassSkippedTotal,
				"prefix_resync_skipped_total":     snapshot.PrefixResyncSkippedTotal,
				"abandons_by_reason":              oracleUint64Map(snapshot.AbandonsByReason),
			},
		},
	})
}

func oracleRecordF19c(t *testing.T) {
	t.Helper()
	trueBytes := []byte{0x00, 0x10, 0x30, 0x70, 0xF0, 0x01, 0x11, 0x31, 0x71, 0xF1, 0x03, 0x13, 0x33, 0x73, 0xF3, 0x07, 0x17, 0x37, 0x77, 0xF7, 0x0F, 0x1F, 0x3F, 0x7F, 0xFF}
	falseBytes := []byte{0x12, 0x52, 0x88, 0x99, 0x4C, 0xCD}
	decisions := make([]any, 0, len(trueBytes)+len(falseBytes))
	for _, value := range trueBytes {
		decisions = append(decisions, map[string]any{"byte": hex.EncodeToString([]byte{value}), "initiator": isInitiatorAddr(value)})
	}
	for _, value := range falseBytes {
		decisions = append(decisions, map[string]any{"byte": hex.EncodeToString([]byte{value}), "initiator": isInitiatorAddr(value)})
	}
	targets := make([]any, 0, 5)
	targetBytes := []byte{0x08, 0xAA, 0xA9, 0xFE, 0x10}
	for _, value := range targetBytes {
		targets = append(targets, map[string]any{"byte": hex.EncodeToString([]byte{value}), "valid_target": isValidTargetAddr(value)})
	}
	oracleWrite(t, "reconstructor/f19c-initiator-and-target.json", oracleCase{
		Component: "passive_reconstructor_f19c_helpers",
		Source:    "passive_reconstructor_f19c_helpers.go",
		Stimulus:  "TestF19c_InvalidQQ_NotMaster/TestF19c_F18_RegressionGuard",
		Input: oracleInput{
			Bytes: hex.EncodeToString(append(append([]byte{}, trueBytes...), falseBytes...)),
			State: map[string]any{"initiator_operands": hex.EncodeToString(append(append([]byte{}, trueBytes...), falseBytes...)), "target_operands": hex.EncodeToString(targetBytes)},
		},
		Output: oracleOutput{Decisions: decisions, Observations: targets},
	})
	reconstructor, subscription := oracleReconstructor()
	payload := []byte{protocol.SymbolSyn, 0x12}
	feedPassiveSymbolsRaw(reconstructor, time.Unix(0, 0), payload)
	requireNoFurtherPassiveEvent(t, subscription)
	snapshot := reconstructor.Snapshot()
	oracleWrite(t, "reconstructor/f19c-invalid-qq-silent-drop.json", oracleCase{
		Component: "passive_reconstructor_f19c_helpers",
		Source:    "passive_reconstructor_f19c_helpers.go",
		Stimulus:  "TestF19c_InvalidQQ_NotMaster",
		Input:     oracleF19cSilentDropInput(payload),
		Output: oracleOutput{Counters: map[string]any{
			"invalid_src_class_skipped_total": snapshot.InvalidSrcClassSkippedTotal,
			"abandons_by_reason":              oracleUint64Map(snapshot.AbandonsByReason),
		}},
	})
}

func oracleRecordWatchCatalog(t *testing.T) {
	t.Helper()
	standard := WatchDescriptor{
		Key: NewB524WatchKey(0x15, 0x06, 0x01, 0x00, 0x0005), SemanticClass: WatchSemanticClassState,
		FreshnessProfile: WatchFreshnessProfileStateFast, DecoderID: "vaillant.f32le",
		CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault,
	}
	ttl, err := standard.EffectiveFreshnessTTL(donorCollaborator{})
	oracleWrite(t, "watch-catalog/freshness-ttl.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestWatchDescriptorEffectiveFreshnessTTLUsesProfileDefault",
		Input:  oracleInput{Qualifiers: map[string]any{"canonical": standard.Key.Canonical(), "profile": string(standard.FreshnessProfile)}},
		Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(err), "ttl_ns": int64(ttl)}}},
	})
	invalid := standard
	invalid.FreshnessProfile = WatchFreshnessProfileConfig
	_, invalidErr := invalid.EffectiveFreshnessTTL(donorCollaborator{})
	oracleWrite(t, "watch-catalog/invalid-pairing.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestWatchDescriptorRejectsInvalidSemanticPairing",
		Input:  oracleInput{Qualifiers: map[string]any{"semantic_class": string(invalid.SemanticClass), "profile": string(invalid.FreshnessProfile)}},
		Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(invalidErr)}}},
	})
	widened := standard
	widened.FreshnessProfile = WatchFreshnessProfileStateSlow
	wide := 121 * time.Second
	widened.FreshnessTTL = &wide
	_, wideErr := widened.EffectiveFreshnessTTL(donorCollaborator{})
	oracleWrite(t, "watch-catalog/widened-ttl.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestWatchDescriptorRejectsWidenedTTL",
		Input: oracleInput{
			Qualifiers: map[string]any{"ttl_ns": int64(wide)},
			State: map[string]any{"descriptor": map[string]any{
				"key": oracleKeyCanonical(widened.Key), "semantic_class": string(widened.SemanticClass), "freshness_profile": string(widened.FreshnessProfile),
				"decoder_id": widened.DecoderID, "correlation_policy": string(widened.CorrelationPolicy), "direct_apply_policy": string(widened.DirectApplyPolicy),
				"freshness_ttl_ns": int64(*widened.FreshnessTTL),
			}},
		},
		Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(wideErr)}}},
	})
	duplicate := WatchDescriptor{
		Key: NewB509WatchKey(0x08, 0x0200), SemanticClass: WatchSemanticClassState, FreshnessProfile: WatchFreshnessProfileStateSlow,
		DecoderID: "vaillant.data2b", CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault,
	}
	_, dupErr := NewWatchCatalog([]WatchDescriptor{duplicate, duplicate}, donorCollaborator{})
	oracleWrite(t, "watch-catalog/duplicate.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestNewWatchCatalogRejectsDuplicateKeys",
		Input:  oracleInput{Qualifiers: map[string]any{"canonical": duplicate.Key.Canonical()}},
		Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(dupErr)}}},
	})
	oracleWatchActivation(t)
	oracleWatchKeys(t)
	oracleWatchClone(t)
}

func oracleWatchActivation(t *testing.T) {
	t.Helper()
	activeKey := NewB524WatchKey(0x15, 0x06, 0x03, 0x00, 0x000F)
	inactiveKey := NewB509WatchKey(0x08, 0x0200)
	catalog, err := NewWatchCatalog([]WatchDescriptor{
		{Key: activeKey, SemanticClass: WatchSemanticClassState, FreshnessProfile: WatchFreshnessProfileStateFast, DecoderID: "vaillant.f32le", CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault},
		{Key: inactiveKey, SemanticClass: WatchSemanticClassState, FreshnessProfile: WatchFreshnessProfileStateSlow, DecoderID: "vaillant.data2b", CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault},
	}, donorCollaborator{})
	if err != nil {
		t.Fatal(err)
	}
	activations := NewWatchActivationSet(catalog)
	if err := activations.Activate(WatchActivationSourcePoller, activeKey); err != nil {
		t.Fatal(err)
	}
	if err := activations.Activate(WatchActivationSourceTooling, activeKey); err != nil {
		t.Fatal(err)
	}
	active := activations.Observe(activeKey)
	inactive := activations.Observe(inactiveKey)
	missKey := NewB524WatchKey(0x15, 0x06, 0x03, 0x00, 0x0022)
	miss := activations.Observe(missKey)
	summary := activations.Summary()
	activations.Deactivate(WatchActivationSourcePoller, activeKey)
	remaining := activations.ActiveSources(activeKey)
	sources := make([]any, 0, len(active.Sources))
	for _, source := range active.Sources {
		sources = append(sources, string(source))
	}
	left := make([]any, 0, len(remaining))
	for _, source := range remaining {
		left = append(left, string(source))
	}
	oracleWrite(t, "watch-catalog/activation-union.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestWatchActivationSetUnionSemanticsAndObservationSummary",
		Input: oracleInput{Qualifiers: map[string]any{"active": activeKey.Canonical(), "inactive": inactiveKey.Canonical(), "miss": missKey.Canonical()}},
		Output: oracleOutput{
			Decisions: []any{map[string]any{
				"active_state": string(active.State), "active_sources": sources,
				"inactive_state": string(inactive.State), "miss_state": string(miss.State), "remaining_sources": left,
			}},
			Counters: map[string]any{"active_total": summary.ActiveTotal, "inactive_total": summary.InactiveTotal, "catalog_miss_total": summary.CatalogMissTotal},
		},
	})
	emptyCatalog := NewWatchActivationSet(catalog)
	missErr := emptyCatalog.Activate(WatchActivationSourcePoller, missKey)
	oracleWrite(t, "watch-catalog/catalog-miss-activation.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestWatchActivationSetRejectsCatalogMissActivation",
		Input:  oracleInput{Qualifiers: map[string]any{"canonical": missKey.Canonical()}},
		Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(missErr)}}},
	})
}

func oracleWatchKeys(t *testing.T) {
	t.Helper()
	builders := []any{
		map[string]any{"builder": "b516", "canonical": NewB516WatchKey(0x15, 0x01, 0x02, 0x03).Canonical()},
		map[string]any{"builder": "b555-program", "canonical": NewB555ProgramWatchKey(0x15, 0xA4, 0x02, 0x00).Canonical()},
		map[string]any{"builder": "b555", "canonical": NewB555WatchKey(0x15, 0xA5, 0x02, 0x00, 0x06, 0x03).Canonical()},
	}
	oracleWrite(t, "watch-catalog/builders.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestWatchKeyBuildersRemainStable",
		Input: oracleInput{Qualifiers: map[string]any{"calls": []any{
			map[string]any{"builder": "b516", "function": "NewB516WatchKey", "target": "15", "period": "01", "source": "02", "usage": "03"},
			map[string]any{"builder": "b555-program", "function": "NewB555ProgramWatchKey", "target": "15", "opcode": "a4", "zone": "02", "hc": "00"},
			map[string]any{"builder": "b555", "function": "NewB555WatchKey", "target": "15", "opcode": "a5", "zone": "02", "hc": "00", "weekday": "06", "slot": "03"},
		}}},
		Output: oracleOutput{Decisions: builders},
	})
	keys := []B555WatchKey{
		NewB555ProgramWatchKey(0x15, 0xA4, 0x02, 0x00),
		NewB555ProgramWatchKey(0x15, 0xA4, 0x02, 0x01),
		NewB555WatchKey(0x15, 0xA5, 0x02, 0x00, 0x06, 0x03),
		NewB555WatchKey(0x15, 0xA5, 0x01, 0x00, 0x06, 0x03),
		NewB555WatchKey(0x15, 0xA5, 0x02, 0x01, 0x06, 0x03),
		NewB555WatchKey(0x15, 0xA5, 0x02, 0x00, 0x05, 0x03),
		NewB555WatchKey(0x15, 0xA5, 0x02, 0x00, 0x06, 0x02),
	}
	seen := map[string]int{}
	canonicals := make([]any, 0, len(keys))
	operands := make([]any, 0, len(keys))
	for _, key := range keys {
		canonical := key.Canonical()
		seen[canonical]++
		canonicals = append(canonicals, canonical)
		operands = append(operands, oracleB555Selector(key))
	}
	unique := true
	for _, count := range seen {
		if count != 1 {
			unique = false
		}
	}
	oracleWrite(t, "watch-catalog/b555-axes.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestB555WatchKeyCanonicalPreservesSelectorAxes",
		Input:  oracleInput{Qualifiers: map[string]any{"keys": operands}},
		Output: oracleOutput{Decisions: canonicals, Counters: map[string]any{"unique": unique}},
	})
	validProgram := NewB555ProgramWatchKey(0x15, 0xA4, 0x02, 0x00)
	malformed := B555WatchKey{Target: 0x15, Opcode: 0xA4, Zone: 0x02, HC: 0x00, Weekday: 0x06, Slot: 0x03}
	catalog, err := NewWatchCatalog([]WatchDescriptor{{
		Key: validProgram, SemanticClass: WatchSemanticClassConfig, FreshnessProfile: WatchFreshnessProfileConfig,
		DecoderID: "vaillant.b555", CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyConfigOptIn,
	}}, donorCollaborator{})
	if err != nil {
		t.Fatal(err)
	}
	_, found := catalog.DescriptorByCanonical(malformed.Canonical())
	oracleWrite(t, "watch-catalog/b555-malformed-program.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestB555WatchKeyCanonicalDisambiguatesMalformedProgramSelectors",
		Input: oracleInput{Qualifiers: map[string]any{"malformed_selector": oracleB555Selector(malformed), "valid_selector": oracleB555Selector(validProgram)}, State: map[string]any{"valid": validProgram.Canonical()}},
		Output: oracleOutput{Decisions: []any{map[string]any{
			"malformed": malformed.Canonical(), "aliases_valid": malformed.Canonical() == validProgram.Canonical(), "catalog_hit": found,
		}}},
	})
	validTimer := NewB555ProgramWatchKey(0x15, 0xA5, 0x02, 0x00)
	sentinel := B555WatchKey{Target: 0x15, Opcode: 0xA5, Zone: 0x02, HC: 0x00, Weekday: 0xFF, Slot: 0x03, HasWeekday: true, HasSlot: true}
	timerCatalog, err := NewWatchCatalog([]WatchDescriptor{{
		Key: validTimer, SemanticClass: WatchSemanticClassConfig, FreshnessProfile: WatchFreshnessProfileConfig,
		DecoderID: "vaillant.b555", CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyConfigOptIn,
	}}, donorCollaborator{})
	if err != nil {
		t.Fatal(err)
	}
	_, timerFound := timerCatalog.DescriptorByCanonical(sentinel.Canonical())
	oracleWrite(t, "watch-catalog/b555-sentinel-timer.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestB555WatchKeyCanonicalDisambiguatesSentinelTimerSelectors",
		Input: oracleInput{Qualifiers: map[string]any{"sentinel_selector": oracleB555Selector(sentinel), "valid_selector": oracleB555Selector(validTimer)}, State: map[string]any{"valid": validTimer.Canonical()}},
		Output: oracleOutput{Decisions: []any{map[string]any{
			"sentinel": sentinel.Canonical(), "aliases_valid": sentinel.Canonical() == validTimer.Canonical(), "catalog_hit": timerFound,
		}}},
	})
	for _, tc := range []struct {
		name       string
		stimulus   string
		descriptor WatchDescriptor
	}{
		{"partial", "TestWatchDescriptorRejectsPartialB555Selector", WatchDescriptor{
			Key:           B555WatchKey{Target: 0x15, Opcode: 0xA5, Zone: 0x02, HC: 0x00, Weekday: 0x06, HasWeekday: true},
			SemanticClass: WatchSemanticClassConfig, FreshnessProfile: WatchFreshnessProfileConfig, DecoderID: "vaillant.b555",
			CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyConfigOptIn,
		}},
		{"non-canonical", "TestWatchDescriptorRejectsNonCanonicalProgramB555SelectorBytes", WatchDescriptor{
			Key: malformed, SemanticClass: WatchSemanticClassConfig, FreshnessProfile: WatchFreshnessProfileConfig, DecoderID: "vaillant.b555",
			CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyConfigOptIn,
		}},
		{"sentinel-descriptor", "TestWatchDescriptorRejectsSentinelTimerB555Selector", WatchDescriptor{
			Key: sentinel, SemanticClass: WatchSemanticClassConfig, FreshnessProfile: WatchFreshnessProfileConfig, DecoderID: "vaillant.b555",
			CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyConfigOptIn,
		}},
	} {
		_, ttlErr := tc.descriptor.EffectiveFreshnessTTL(donorCollaborator{})
		oracleWrite(t, "watch-catalog/b555-reject-"+tc.name+".json", oracleCase{
			Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: tc.stimulus,
			Input:  oracleInput{Qualifiers: map[string]any{"canonical": tc.descriptor.Key.Canonical(), "selector": oracleB555Selector(tc.descriptor.Key.(B555WatchKey))}},
			Output: oracleOutput{Decisions: []any{map[string]any{"error": oracleErr(ttlErr)}}},
		})
	}
	readEvent := PassiveClassifiedEvent{HasRequest: true, Request: protocol.Frame{Source: 0x10, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x06, 0x00, 0x03, 0x01, 0x1C, 0x00}}}
	readKey, readOK := PassiveWatchKeyFromEvent(readEvent)
	writeEvent := PassiveClassifiedEvent{HasRequest: true, Request: protocol.Frame{Source: 0x10, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x02, 0x02, 0x03, 0x01, 0x1C, 0x00, 0xAA, 0x55}}}
	writeKey, writeOK := PassiveWatchKeyFromEvent(writeEvent)
	oracleWrite(t, "watch-catalog/passive-b524-key.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestPassiveWatchKeyFromEventMatchesB524SchedulerKey",
		Input: oracleInput{
			Bytes: "060003011c00020203011c00aa55",
			State: map[string]any{"events": []any{
				map[string]any{"name": "read", "has_request": readEvent.HasRequest, "request": oracleFrame(readEvent.Request, true)},
				map[string]any{"name": "write", "has_request": writeEvent.HasRequest, "request": oracleFrame(writeEvent.Request, true)},
			}},
		},
		Output: oracleOutput{Decisions: []any{
			map[string]any{"name": "read", "ok": readOK, "canonical": oracleKeyCanonical(readKey)},
			map[string]any{"name": "write", "ok": writeOK, "canonical": oracleKeyCanonical(writeKey)},
		}},
	})
	b509 := make([]any, 0, 3)
	b509Events := make([]any, 0, 3)
	for _, opcode := range []byte{0x0D, 0x29, 0x0E} {
		event := PassiveClassifiedEvent{HasRequest: true, Request: protocol.Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{opcode, 0x02, 0x00}}}
		key, ok := PassiveWatchKeyFromEvent(event)
		b509Events = append(b509Events, map[string]any{"opcode": hex.EncodeToString([]byte{opcode}), "has_request": event.HasRequest, "request": oracleFrame(event.Request, true)})
		b509 = append(b509, map[string]any{"opcode": hex.EncodeToString([]byte{opcode}), "ok": ok, "canonical": oracleKeyCanonical(key)})
	}
	oracleWrite(t, "watch-catalog/passive-b509-key.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestPassiveWatchKeyFromEventMatchesB509SchedulerKey",
		Input:  oracleInput{Bytes: "0d02002902000e0200", State: map[string]any{"events": b509Events}},
		Output: oracleOutput{Decisions: b509},
	})
}

func oracleWatchClone(t *testing.T) {
	t.Helper()
	original := &B524WatchKey{Target: 0x15, Opcode: 0x06, Group: 0x01, Instance: 0x00, RegisterAddress: 0x0005}
	originalCanonical := original.Canonical()
	catalog, err := NewWatchCatalog([]WatchDescriptor{{
		Key: original, SemanticClass: WatchSemanticClassState, FreshnessProfile: WatchFreshnessProfileStateFast,
		DecoderID: "vaillant.f32le", CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault,
	}}, donorCollaborator{})
	if err != nil {
		t.Fatal(err)
	}
	original.RegisterAddress = 0x0022
	stored, ok := catalog.DescriptorByCanonical(originalCanonical)
	storedCanonical := ""
	if storedKey, isKey := stored.Key.(B524WatchKey); isKey {
		storedCanonical = storedKey.Canonical()
	}
	oracleWrite(t, "watch-catalog/clone-pointer-key.json", oracleCase{
		Component: "watch_catalog", Source: "watch_catalog.go", Stimulus: "TestNewWatchCatalogClonesSupportedPointerWatchKeys",
		Input:  oracleInput{State: map[string]any{"original": originalCanonical, "mutated_register": "0022"}},
		Output: oracleOutput{Decisions: []any{map[string]any{"found": ok, "stored": storedCanonical, "unchanged": storedCanonical == originalCanonical}}},
	})
}

func oracleRecordObserveFirst(t *testing.T) {
	t.Helper()
	oracleWrite(t, "observe-first-flags/default.json", oracleCase{
		Component: "observe_first_flags", Source: "observe_first_flags.go", Stimulus: "TestDefaultObserveFirstFeatureFlagsAreConservative",
		Output: oracleOutput{Decisions: []any{oracleFlags(DefaultObserveFirstFeatureFlags())}},
	})
	clamped := NormalizeObserveFirstFeatureFlags(false, true, true, ObserveFirstExternalWritePolicyInvalidateOnly)
	oracleWrite(t, "observe-first-flags/enabled-off.json", oracleCase{
		Component: "observe_first_flags", Source: "observe_first_flags.go", Stimulus: "TestNormalizeObserveFirstFeatureFlags_MasterOffClampsUnsafeSubFlags",
		Input:  oracleInput{Qualifiers: map[string]any{"enabled": false, "state_direct": true, "config_direct": true, "policy": string(ObserveFirstExternalWritePolicyInvalidateOnly)}},
		Output: oracleOutput{Decisions: []any{oracleFlags(clamped)}},
	})
	stateOff := NormalizeObserveFirstFeatureFlags(true, false, true, ObserveFirstExternalWritePolicyInvalidateOnly)
	oracleWrite(t, "observe-first-flags/state-disabled.json", oracleCase{
		Component: "observe_first_flags", Source: "observe_first_flags.go", Stimulus: "TestNormalizeObserveFirstFeatureFlags_StateDisabledForcesConfigOffOnly",
		Input:  oracleInput{Qualifiers: map[string]any{"enabled": true, "state_direct": false, "config_direct": true, "policy": string(ObserveFirstExternalWritePolicyInvalidateOnly)}},
		Output: oracleOutput{Decisions: []any{oracleFlags(stateOff)}},
	})
	configDirect := NormalizeObserveFirstFeatureFlags(true, true, true, ObserveFirstExternalWritePolicyRecordOnly)
	oracleWrite(t, "observe-first-flags/config-requires-invalidation.json", oracleCase{
		Component: "observe_first_flags", Source: "observe_first_flags.go", Stimulus: "TestNormalizeObserveFirstFeatureFlags_ConfigDirectRequiresInvalidatingPolicy",
		Input:  oracleInput{Qualifiers: map[string]any{"enabled": true, "state_direct": true, "config_direct": true, "policy": string(ObserveFirstExternalWritePolicyRecordOnly)}},
		Output: oracleOutput{Decisions: []any{oracleFlags(configDirect)}},
	})
	stamped := NormalizeObserveFirstFeatureFlags(true, true, false, ObserveFirstExternalWritePolicyRecordOnly)
	updatedAt := time.Date(2026, time.March, 12, 12, 34, 56, 0, time.UTC)
	state := stamped.StateAt(updatedAt)
	zero := stamped.State()
	stamp := ""
	if state.LastUpdatedAt != nil {
		stamp = state.LastUpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	oracleWrite(t, "observe-first-flags/state-at.json", oracleCase{
		Component: "observe_first_flags", Source: "observe_first_flags.go", Stimulus: "TestObserveFirstFeatureFlagsStateAtCarriesMutationTimestamp",
		Input:  oracleInput{Clock: map[string]any{"at": updatedAt.Format(time.RFC3339Nano)}},
		Output: oracleOutput{Decisions: []any{map[string]any{"state_at": stamp, "state_has_timestamp": zero.LastUpdatedAt != nil}}},
	})
	cfg := applyDefaults(Config{ObserveFirstEnabled: true, PassiveStateDirectApply: false, PassiveConfigDirectApply: true, ExternalWritePolicy: ObserveFirstExternalWritePolicyInvalidateOnly})
	oracleWrite(t, "observe-first-flags/apply-defaults.json", oracleCase{
		Component: "observe_first_flags", Source: "observe_first_flags.go", Stimulus: "TestApplyDefaultsNormalizesObserveFirstFlagsIntoConfig",
		Input: oracleInput{Qualifiers: map[string]any{"enabled": true, "state_direct": false, "config_direct": true, "policy": string(ObserveFirstExternalWritePolicyInvalidateOnly)}},
		Output: oracleOutput{Decisions: []any{map[string]any{
			"config_direct": cfg.PassiveConfigDirectApply, "policy": string(cfg.ExternalWritePolicy), "flags": oracleFlags(cfg.ObserveFirstFlags),
		}}},
	})
	parsed := make([]any, 0, 5)
	for _, input := range []string{"invalidate_only", "record_only", "record_and_invalidate", "record-and-invalidate", "unsafe"} {
		policy, parseErr := ParseObserveFirstExternalWritePolicy(input)
		parsed = append(parsed, map[string]any{"input": input, "policy": string(policy), "error": oracleErr(parseErr)})
	}
	oracleWrite(t, "observe-first-flags/parse-policy.json", oracleCase{
		Component: "observe_first_flags", Source: "observe_first_flags.go", Stimulus: "TestParseObserveFirstExternalWritePolicyAcceptsBoundedValues",
		Output: oracleOutput{Decisions: parsed},
	})

	type responseCase struct {
		name     string
		response protocol.Frame
	}
	classify := func(file, stimulus string, request protocol.Frame, cases []responseCase) {
		decisions := make([]any, 0, len(cases))
		operands := make([]any, 0, len(cases))
		for _, item := range cases {
			class := observeFirstResponseClass(request, protocol.FrameTypeInitiatorTarget, DedupOutcomeSuccess, item.response, true)
			decisions = append(decisions, map[string]any{"name": item.name, "class": string(class)})
			operands = append(operands, map[string]any{"name": item.name, "frame_type": oracleFrameType(protocol.FrameTypeInitiatorTarget), "outcome": string(DedupOutcomeSuccess), "has_response": true, "response": oracleFrame(item.response, true)})
		}
		oracleWrite(t, file, oracleCase{
			Component: "observe_first_family_policy", Source: "observe_first_family_policy.go", Stimulus: stimulus,
			Input: oracleInput{
				Bytes:      hex.EncodeToString(request.Data),
				Qualifiers: map[string]any{"request": oracleFrame(request, true)},
				State:      map[string]any{"responses": operands},
			},
			Output: oracleOutput{Decisions: decisions},
		})
	}
	b509Request := protocol.Frame{Source: 0x31, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x29, 0x02, 0x00}}
	classify("observe-first-family-policy/b509-response-class.json", "TestObserveFirstResponseClass_B509ReadMapsHeaderOnlyAndValueBearing", b509Request, []responseCase{
		{"header", protocol.Frame{Data: []byte{0x29, 0x02, 0x00}}},
		{"value", protocol.Frame{Data: []byte{0x29, 0x02, 0x00, 0x7F}}},
		{"single", protocol.Frame{Data: []byte{0x7F}}},
		{"empty", protocol.Frame{}},
	})
	b524Request := protocol.Frame{Source: 0x10, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x06, 0x00, 0x03, 0x01, 0x1C, 0x00}}
	classify("observe-first-family-policy/b524-response-class.json", "TestObserveFirstResponseClass_B524ReadMapsHeaderValueAndAmbiguous", b524Request, []responseCase{
		{"header", protocol.Frame{Data: []byte{0x42, 0x01, 0x03, 0x1C, 0x00}}},
		{"value", protocol.Frame{Data: []byte{0x42, 0x01, 0x03, 0x1C, 0x00, 0x55}}},
		{"ambiguous", protocol.Frame{Data: []byte{0x42, 0x01, 0x04, 0x1C, 0x00, 0x55}}},
	})
	b516 := protocol.Frame{Source: 0x15, Target: 0xFE, Primary: 0xB5, Secondary: 0x16, Data: []byte{0x01, 0x02}}
	b516Observation := WatchObservation{State: WatchObservationStateCatalogMiss}
	b516Policy := observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, b516, DedupResponseValueBearing, b516Observation, DefaultObserveFirstFeatureFlags())
	oracleWrite(t, "observe-first-family-policy/b516-energy-merge.json", oracleCase{
		Component: "observe_first_family_policy", Source: "observe_first_family_policy.go", Stimulus: "TestObserveFirstFamilyPolicy_B516UsesEnergyMergeOnly",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(b516.Data),
			Qualifiers: map[string]any{"request": oracleFrame(b516, true)},
			State: map[string]any{"calls": []any{map[string]any{
				"order": 1, "scope": string(ObserveFirstTrafficScopePassive), "request": oracleFrame(b516, true), "response_class": string(DedupResponseValueBearing),
				"observation": map[string]any{"state": string(b516Observation.State), "has_descriptor": b516Observation.HasDescriptor},
				"flags":       map[string]any{"constructor": "DefaultObserveFirstFeatureFlags"},
			}}},
		},
		Output: oracleOutput{Decisions: []any{oracleFamilyPolicy(b516Policy)}},
	})
	readRequest := protocol.Frame{Source: 0x31, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x06, 0x00, 0x03, 0x01, 0x1C, 0x00}}
	policyKey := NewB524WatchKey(0x15, 0x06, 0x03, 0x01, 0x001C)
	observation := testActiveStateObservation(policyKey)
	observationOperand := map[string]any{
		"state": string(observation.State), "has_descriptor": observation.HasDescriptor, "key": policyKey.Canonical(),
		"semantic_class": string(observation.Descriptor.SemanticClass), "correlation_policy": string(observation.Descriptor.CorrelationPolicy),
		"direct_apply_policy": string(observation.Descriptor.DirectApplyPolicy),
	}
	readPolicy := observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, readRequest, DedupResponseValueBearing, observation, DefaultObserveFirstFeatureFlags())
	writeRequest := protocol.Frame{Source: 0x10, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x06, 0x01, 0x03, 0x01, 0x1C, 0x00}}
	calls := []any{map[string]any{
		"order": 1, "scope": string(ObserveFirstTrafficScopePassive), "request": oracleFrame(readRequest, true), "response_class": string(DedupResponseValueBearing),
		"observation": observationOperand, "flags": map[string]any{"constructor": "DefaultObserveFirstFeatureFlags"},
	}}
	decisions := []any{oracleFamilyPolicy(readPolicy)}
	for _, policyValue := range []ObserveFirstExternalWritePolicy{ObserveFirstExternalWritePolicyInvalidateOnly, ObserveFirstExternalWritePolicyRecordOnly, ObserveFirstExternalWritePolicyRecordAndInvalidate} {
		flags := NormalizeObserveFirstFeatureFlags(true, true, false, policyValue)
		calls = append(calls, map[string]any{
			"order": len(calls) + 1, "scope": string(ObserveFirstTrafficScopePassive), "request": oracleFrame(writeRequest, true), "response_class": string(DedupResponseHeaderOnly),
			"observation": observationOperand,
			"flags":       map[string]any{"constructor": "NormalizeObserveFirstFeatureFlags", "enabled": true, "state_direct": true, "config_direct": false, "external_write_policy": string(policyValue)},
		})
		decisions = append(decisions, oracleFamilyPolicy(observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, writeRequest, DedupResponseHeaderOnly, observation, flags)))
	}
	oracleWrite(t, "observe-first-family-policy/b524-read-write.json", oracleCase{
		Component: "observe_first_family_policy", Source: "observe_first_family_policy.go", Stimulus: "TestObserveFirstFamilyPolicy_B524ReadAndObservedWriteStaySeparated",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(append(append([]byte{}, readRequest.Data...), writeRequest.Data...)),
			Qualifiers: map[string]any{"read_request": oracleFrame(readRequest, true), "write_request": oracleFrame(writeRequest, true)},
			State:      map[string]any{"calls": calls},
		},
		Output: oracleOutput{Decisions: decisions},
	})
}

func oracleClassified(event PassiveClassifiedEvent) map[string]any {
	return map[string]any{
		"kind":                  oracleKind(event.Kind),
		"frame_type":            oracleFrameType(event.FrameType),
		"has_request":           event.HasRequest,
		"has_response":          event.HasResponse,
		"request":               oracleFrame(event.Request, event.HasRequest),
		"response":              oracleFrame(event.Response, event.HasResponse),
		"abandon_reason":        string(event.AbandonReason),
		"discontinuity_reason":  string(event.DiscontinuityReason),
		"offending_symbol":      hex.EncodeToString([]byte{event.OffendingSymbol}),
		"offending_was_escaped": event.OffendingWasEscaped,
		"observed_at":           event.ObservedAt.UTC().Format(time.RFC3339Nano),
		"timing":                oracleTiming(event.Timing),
		"ack_correlation": map[string]any{
			"byte": hex.EncodeToString([]byte{event.ACKCorrelation.Byte}), "position": string(event.ACKCorrelation.Position),
			"complete_request": event.ACKCorrelation.CompleteRequest, "correlator": string(event.ACKCorrelation.Correlator),
		},
		"error": oracleErr(event.Err),
	}
}

// oracleTiming lists the five timing markers of an event; a marker the
// reconstructor did not set is the empty string.
func oracleTiming(timing PassiveTimingMarkers) map[string]any {
	marker := func(at time.Time) string {
		if at.IsZero() {
			return ""
		}
		return at.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"request_start": marker(timing.RequestStart), "request_end": marker(timing.RequestEnd),
		"response_start": marker(timing.ResponseStart), "response_end": marker(timing.ResponseEnd), "terminal": marker(timing.Terminal),
	}
}

func oracleKind(kind PassiveClassifiedEventKind) string {
	switch kind {
	case PassiveClassifiedEventBroadcastFrame:
		return "broadcast_frame"
	case PassiveClassifiedEventMasterFrame:
		return "master_frame"
	case PassiveClassifiedEventTransaction:
		return "transaction"
	case PassiveClassifiedEventAbandonedTransaction:
		return "abandoned_transaction"
	case PassiveClassifiedEventDiscontinuity:
		return "discontinuity"
	default:
		return fmt.Sprintf("kind_%d", kind)
	}
}

func oracleFrameType(kind protocol.FrameType) string {
	switch kind {
	case protocol.FrameTypeUnknown:
		return "unknown"
	case protocol.FrameTypeBroadcast:
		return "broadcast"
	case protocol.FrameTypeInitiatorTarget:
		return "initiator_target"
	case protocol.FrameTypeInitiatorInitiator:
		return "initiator_initiator"
	default:
		return fmt.Sprintf("frame_%d", kind)
	}
}

func oracleFrame(frame protocol.Frame, present bool) map[string]any {
	if !present {
		return map[string]any{}
	}
	return map[string]any{
		"source": hex.EncodeToString([]byte{frame.Source}), "target": hex.EncodeToString([]byte{frame.Target}),
		"primary": hex.EncodeToString([]byte{frame.Primary}), "secondary": hex.EncodeToString([]byte{frame.Secondary}),
		"data": hex.EncodeToString(frame.Data),
	}
}

func oracleFlags(flags ObserveFirstFeatureFlags) map[string]any {
	reasons := make([]any, 0)
	for _, reason := range flags.NormalizationReasons() {
		reasons = append(reasons, string(reason))
	}
	return map[string]any{
		"observe_first_enabled": flags.ObserveFirstEnabled(),
		"passive_state_direct":  flags.PassiveStateDirectApply(),
		"passive_config_direct": flags.PassiveConfigDirectApply(),
		"external_write_policy": string(flags.ExternalWritePolicy()),
		"normalization_reasons": reasons,
	}
}

func oracleFamilyPolicy(policy ObserveFirstFamilyPolicy) map[string]any {
	return map[string]any{
		"family": policy.Family, "request_intent": policy.RequestIntent, "response_class": policy.ResponseClass,
		"correlation_policy": policy.CorrelationPolicy, "direct_apply_policy": policy.DirectApplyPolicy,
		"uses_runtime_external_write_policy": policy.UsesRuntimeExternalWritePolicy,
		"effective_external_write_policy":    policy.EffectiveExternalWritePolicy,
	}
}

func oracleWriteResult(result ShadowWriteResult) map[string]any {
	return map[string]any{"accepted": result.Accepted, "reason": string(result.Reason), "generation": result.Generation}
}

func oracleShadowSummary(summary ShadowCacheSummary) map[string]any {
	return map[string]any{
		"enabled": summary.Enabled, "pinned_budget_degraded": summary.PinnedBudgetDegraded,
		"total_entries": summary.TotalEntries, "pinned_entries": summary.PinnedEntries, "evictable_entries": summary.EvictableEntries,
	}
}

func oracleEntry(cache *ShadowCache, key WatchKey) map[string]any {
	entry, ok := cache.Entry(key)
	if !ok {
		return map[string]any{"present": false}
	}
	stamp := func(at time.Time) string {
		if at.IsZero() {
			return ""
		}
		return at.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"present": true, "value": hex.EncodeToString(entry.Value), "source": string(entry.Source),
		"confidence": string(entry.Confidence), "state": string(entry.State), "observed_at": stamp(entry.ObservedAt), "expires_at": stamp(entry.ExpiresAt),
		"generation": entry.Generation, "last_write_generation": entry.LastWriteGeneration, "invalidation_generation": entry.InvalidationGeneration,
		"invalidation_reason": string(entry.InvalidationReason), "invalidation_source": string(entry.InvalidationSource), "invalidated_at": stamp(entry.InvalidatedAt),
		"pinned": entry.Pinned,
	}
}

func oracleKeyCanonical(key WatchKey) string {
	if key == nil {
		return ""
	}
	return key.Canonical()
}

func oracleUint64Map(values map[string]uint64) map[string]any {
	out := map[string]any{}
	for key, value := range values {
		out[key] = value
	}
	return out
}

func oracleErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type oracleCase struct {
	Format    string       `json:"format"`
	Component string       `json:"component"`
	Source    string       `json:"source"`
	Stimulus  string       `json:"stimulus"`
	Input     oracleInput  `json:"input"`
	Output    oracleOutput `json:"output"`
}

type oracleInput struct {
	Bytes      string         `json:"bytes"`
	Qualifiers map[string]any `json:"qualifiers"`
	Clock      map[string]any `json:"clock"`
	State      map[string]any `json:"state"`
}

type oracleOutput struct {
	SymbolsWritten []any          `json:"symbols_written"`
	Decisions      []any          `json:"decisions"`
	Cache          map[string]any `json:"cache"`
	Observations   []any          `json:"observations"`
	Counters       map[string]any `json:"counters"`
}

func oracleWrite(t *testing.T, name string, document oracleCase) {
	t.Helper()
	if document.Format == "" {
		document.Format = "helianthus-oracle-bt01-case/1"
	}
	if document.Input.Qualifiers == nil {
		document.Input.Qualifiers = map[string]any{}
	}
	if document.Input.Clock == nil {
		document.Input.Clock = map[string]any{}
	}
	if document.Input.State == nil {
		document.Input.State = map[string]any{}
	}
	if document.Output.SymbolsWritten == nil {
		document.Output.SymbolsWritten = []any{}
	}
	if document.Output.Decisions == nil {
		document.Output.Decisions = []any{}
	}
	if document.Output.Cache == nil {
		document.Output.Cache = map[string]any{}
	}
	if document.Output.Observations == nil {
		document.Output.Observations = []any{}
	}
	if document.Output.Counters == nil {
		document.Output.Counters = map[string]any{}
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := append(encoded, '\n')
	path := filepath.Join("testdata", "oracle", filepath.FromSlash(name))
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("oracle vector %s: %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("oracle vector %s differs from the recorded case", name)
	}

}

func oracleF19cSilentDropInput(payload []byte) oracleInput {
	input := oracleReconstructorInput(payload, oracleReconSpec{Raw: true})
	input.Clock["silence_ns"] = int64(50 * time.Millisecond)
	return input
}

func oracleB555Selector(key B555WatchKey) map[string]any {
	return map[string]any{
		"target": hex.EncodeToString([]byte{key.Target}), "opcode": hex.EncodeToString([]byte{key.Opcode}),
		"zone": hex.EncodeToString([]byte{key.Zone}), "hc": hex.EncodeToString([]byte{key.HC}),
		"weekday": hex.EncodeToString([]byte{key.Weekday}), "slot": hex.EncodeToString([]byte{key.Slot}),
		"has_weekday": key.HasWeekday, "has_slot": key.HasSlot,
	}
}
