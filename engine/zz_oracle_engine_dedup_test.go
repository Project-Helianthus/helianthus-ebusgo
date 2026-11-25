package engine

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// TestOracleEngineDedup records the pin's deduplicator stimuli that need no
// watch observer. The input frames, clock and state are those of the pin's
// own tests.
func TestOracleEngineDedup(t *testing.T) {
	oracleDedupBudgets(t)
	oracleDedupMatch(t, "passive-first-then-active", "TestActivePassiveDeduplicator_MatchesPendingPassiveFirstArrival", true)
	oracleDedupMatch(t, "active-first-then-delayed-passive", "TestActivePassiveDeduplicator_MatchesDelayedPassiveWithinRetention", false)
	oracleDedupSharedWatchKey(t)
	oracleDedupDegraded(t)
	oracleDedupDiscontinuity(t)
	oracleDedupOverflow(t)
}

func oracleAdjudicated(event AdjudicatedPassiveEvent) map[string]any {
	key := ""
	if event.Fingerprint.SharedWatchKey != nil {
		key = event.Fingerprint.SharedWatchKey.Canonical()
	}
	return map[string]any{
		"disposition":               string(event.Disposition),
		"suppress_shadow":           event.SuppressShadow,
		"suppress_watch_efficiency": event.SuppressWatchEfficiency,
		"third_party_eligible":      event.ThirdPartyEligible,
		"observability_only":        event.ObservabilityOnly,
		"local_participant_inbound": event.LocalParticipantInbound,
		"matched_active_duplicate":  event.MatchedActiveDuplicate,
		"epoch":                     event.Epoch,
		"fingerprint": map[string]any{
			"source": hex.EncodeToString([]byte{event.Fingerprint.Source}), "target": hex.EncodeToString([]byte{event.Fingerprint.Target}),
			"request": hex.EncodeToString(event.Fingerprint.RequestBytes), "response": hex.EncodeToString(event.Fingerprint.ResponseBytes),
			"transaction_class": string(event.Fingerprint.TransactionClass), "outcome_class": string(event.Fingerprint.OutcomeClass),
			"response_class": string(event.Fingerprint.ResponseClass), "shared_watch_key": key,
		},
		"family_policy":        oracleFamilyPolicy(event.FamilyPolicy),
		"event_kind":           oracleKind(event.Event.Kind),
		"discontinuity_reason": string(event.Event.DiscontinuityReason),
		"event":                oracleClassified(event.Event),
	}
}

// oracleDedupInitialHealth names the starting state the recorder forces before
// the first event: a deduplicator is constructed degraded, and every case that
// calls forceHealthyDedup starts from the healthy state with a cleared
// recovery record. Cases that do not force it start degraded.
const oracleDedupInitialHealth = "healthy: constructed degraded, then forced healthy with an empty recovery record before the first event"

// oraclePassiveSubmission and oracleActiveSubmission describe an event the
// recorder submits, with its frame boundaries and the fields the
// fingerprint is matched on, so that the input alone fixes the operands.
func oraclePassiveSubmission(event PassiveClassifiedEvent, base time.Time) map[string]any {
	return map[string]any{
		"channel": "passive", "kind": oracleKind(event.Kind), "frame_type": oracleFrameType(event.FrameType),
		"has_request": event.HasRequest, "has_response": event.HasResponse,
		"request": oracleFrame(event.Request, event.HasRequest), "response": oracleFrame(event.Response, event.HasResponse),
		"discontinuity_reason": string(event.DiscontinuityReason), "observed_offset_ns": int64(event.ObservedAt.Sub(base)),
	}
}

func oracleActiveSubmission(event protocol.BusEvent, offset time.Duration) map[string]any {
	kind := fmt.Sprintf("kind_%d", event.Kind)
	switch event.Kind {
	case protocol.BusEventAttemptComplete:
		kind = "attempt_complete"
	case protocol.BusEventObserverFault:
		kind = "observer_fault"
	}
	outcome := fmt.Sprintf("outcome_%d", event.Outcome)
	switch event.Outcome {
	case protocol.BusOutcomeSuccess:
		outcome = "success"
	case protocol.BusOutcomeObserverFault:
		outcome = "observer_fault"
	}
	return map[string]any{
		"channel": "active", "kind": kind, "frame_type": oracleFrameType(event.FrameType), "outcome": outcome,
		"has_request": event.HasRequest, "has_response": event.HasResponse,
		"request": oracleFrame(event.Request, event.HasRequest), "response": oracleFrame(event.Response, event.HasResponse),
		"submitted_offset_ns": int64(offset),
	}
}

func oracleDedupBudgets(t *testing.T) {
	t.Helper()
	deduplicator, err := NewActivePassiveDeduplicator(withDonor(DefaultConfig()))
	if err != nil {
		t.Fatal(err)
	}
	budgets := deduplicator.Budgets()
	oracleWrite(t, "deduplicator/default-budgets.json", oracleCase{
		Component: "active_passive_deduplicator", Source: "active_passive_deduplicator.go", Stimulus: "TestActivePassiveDeduplicator_DefaultBudgetsStayFrozenToM1Envelope",
		Input: oracleInput{Qualifiers: map[string]any{"config": "DefaultConfig"}},
		Output: oracleOutput{Counters: map[string]any{
			"logical_request_envelope_ns": int64(budgets.LogicalRequestEnvelope), "active_publish_budget_ns": int64(budgets.ActivePublishBudget),
			"pending_grace_timeout_ns": int64(budgets.PendingGraceTimeout), "active_fingerprint_retention_ns": int64(budgets.ActiveFingerprintRetention),
			"pending_capacity": budgets.PendingCapacity,
		}},
	})
}

func oracleDedupMatch(t *testing.T, name, stimulus string, passiveFirst bool) {
	t.Helper()
	deduplicator := newTestDeduplicator(t)
	subscription, err := deduplicator.Subscribe("test", DedupSubscriberCritical, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	base := time.Unix(0, 0)
	deduplicator.nowFunc = func() time.Time { return base }
	request, response := directTransactionPair()
	setKnownLocalAddress(deduplicator, request.Source)
	offset := 500 * time.Millisecond
	var submissions []any
	if passiveFirst {
		passive := passiveTransactionEvent(base, request, response)
		active := activeAttemptEvent(request, response)
		submissions = []any{oraclePassiveSubmission(passive, base), oracleActiveSubmission(active, offset)}
		deduplicator.OnPassiveClassifiedEvent(passive)
		deduplicator.nowFunc = func() time.Time { return base.Add(offset) }
		if err := deduplicator.OnBusEvent(active); err != nil {
			t.Fatal(err)
		}
	} else {
		offset = 1500 * time.Millisecond
		active := activeAttemptEvent(request, response)
		passive := passiveTransactionEvent(base.Add(offset), request, response)
		submissions = []any{oracleActiveSubmission(active, 0), oraclePassiveSubmission(passive, base)}
		if err := deduplicator.OnBusEvent(active); err != nil {
			t.Fatal(err)
		}
		deduplicator.nowFunc = func() time.Time { return base.Add(offset) }
		deduplicator.OnPassiveClassifiedEvent(passive)
	}
	event := requireAdjudicatedEvent(t, subscription, DedupDispositionMatchedActiveCopy)
	oracleWrite(t, "deduplicator/"+name+".json", oracleCase{
		Component: "active_passive_deduplicator", Source: "active_passive_deduplicator.go", Stimulus: stimulus,
		Input: oracleInput{
			Bytes:      hex.EncodeToString(append(append([]byte{}, request.Data...), response.Data...)),
			Qualifiers: map[string]any{"passive_first": passiveFirst, "local_address": "31"},
			Clock:      map[string]any{"base": base.UTC().Format(time.RFC3339Nano), "second_arrival_offset_ns": int64(offset)},
			State:      map[string]any{"submissions": submissions},
		},
		Output: oracleOutput{Decisions: []any{oracleAdjudicated(event)}, Counters: map[string]any{"pending": len(deduplicator.pending), "active": len(deduplicator.active)}},
	})
}

func oracleDedupSharedWatchKey(t *testing.T) {
	t.Helper()
	deduplicator := newTestDeduplicator(t)
	subscription, err := deduplicator.Subscribe("test", DedupSubscriberCritical, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	forceHealthyDedup(deduplicator)
	base := time.Unix(0, 0)
	deduplicator.nowFunc = func() time.Time { return base }
	request := protocol.Frame{Source: 0x31, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x06, 0x00, 0x03, 0x01, 0x1C, 0x00}}
	response := protocol.Frame{Source: request.Target, Target: request.Source, Primary: request.Primary, Secondary: request.Secondary, Data: []byte{0x42, 0x01, 0x03, 0x1C, 0x00, 0x22}}
	passiveEvent := passiveTransactionEvent(base, request, response)
	deduplicator.OnPassiveClassifiedEvent(passiveEvent)
	assertNoAdjudicatedEvent(t, subscription, 25*time.Millisecond)
	pending := len(deduplicator.pending)
	deduplicator.nowFunc = func() time.Time {
		return base.Add(100*time.Millisecond + deduplicator.budgets.PendingGraceTimeout + time.Millisecond)
	}
	deduplicator.publishAll(deduplicator.releaseExpiredPending(deduplicator.now()))
	event := requireAdjudicatedEvent(t, subscription, DedupDispositionObservabilityOnly)
	oracleWrite(t, "deduplicator/passive-fingerprint-shared-watch-key.json", oracleCase{
		Component: "active_passive_deduplicator", Source: "active_passive_deduplicator.go", Stimulus: "TestActivePassiveDeduplicator_PassiveFingerprintCarriesSharedWatchKey",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(append(append([]byte{}, request.Data...), response.Data...)),
			Qualifiers: map[string]any{"watch_observer": "none"},
			Clock:      map[string]any{"base": base.UTC().Format(time.RFC3339Nano), "release_offset_ns": int64(100*time.Millisecond + deduplicator.budgets.PendingGraceTimeout + time.Millisecond)},
			State:      map[string]any{"initial_health": oracleDedupInitialHealth, "submissions": []any{oraclePassiveSubmission(passiveEvent, base)}},
		},
		Output: oracleOutput{Decisions: []any{oracleAdjudicated(event)}, Counters: map[string]any{"pending_before_release": pending}},
	})
}

func oracleDedupDegraded(t *testing.T) {
	t.Helper()
	deduplicator := newTestDeduplicator(t)
	subscription, err := deduplicator.Subscribe("test", DedupSubscriberCritical, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	forceHealthyDedup(deduplicator)
	base := time.Unix(0, 0)
	deduplicator.nowFunc = func() time.Time { return base }
	request, response := directTransactionPair()
	setKnownLocalAddress(deduplicator, request.Source)
	activeEvent := activeAttemptEvent(request, response)
	faultEvent := protocol.BusEvent{Kind: protocol.BusEventObserverFault, Outcome: protocol.BusOutcomeObserverFault}
	passiveEvent := passiveTransactionEvent(base.Add(200*time.Millisecond), request, response)
	if err := deduplicator.OnBusEvent(activeEvent); err != nil {
		t.Fatal(err)
	}
	assertNoAdjudicatedEvent(t, subscription, 25*time.Millisecond)
	deduplicator.nowFunc = func() time.Time { return base.Add(100 * time.Millisecond) }
	if err := deduplicator.OnBusEvent(faultEvent); err != nil {
		t.Fatal(err)
	}
	deduplicator.nowFunc = func() time.Time { return base.Add(200 * time.Millisecond) }
	deduplicator.OnPassiveClassifiedEvent(passiveEvent)
	deduplicator.nowFunc = func() time.Time { return base.Add(deduplicator.budgets.PendingGraceTimeout + time.Second) }
	deduplicator.publishAll(deduplicator.releaseExpiredPending(deduplicator.now()))
	event := requireAdjudicatedEvent(t, subscription, DedupDispositionObservabilityOnly)
	oracleWrite(t, "deduplicator/degraded-skips-active-suppression.json", oracleCase{
		Component: "active_passive_deduplicator", Source: "active_passive_deduplicator.go", Stimulus: "TestActivePassiveDeduplicator_DegradedSkipsActiveFingerprintSuppression",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(append(append([]byte{}, request.Data...), response.Data...)),
			Qualifiers: map[string]any{"bus_events": "attempt_complete then observer_fault"},
			Clock:      map[string]any{"base": base.UTC().Format(time.RFC3339Nano), "observer_fault_offset_ns": int64(100 * time.Millisecond), "passive_offset_ns": int64(200 * time.Millisecond)},
			State: map[string]any{"initial_health": oracleDedupInitialHealth, "local_address": hex.EncodeToString([]byte{request.Source}), "submissions": []any{
				oracleActiveSubmission(activeEvent, 0), oracleActiveSubmission(faultEvent, 100*time.Millisecond), oraclePassiveSubmission(passiveEvent, base),
			}},
		},
		Output: oracleOutput{Decisions: []any{oracleAdjudicated(event)}, Counters: map[string]any{"active_fingerprints": len(deduplicator.active)}},
	})
}

func oracleDedupDiscontinuity(t *testing.T) {
	t.Helper()
	deduplicator := newTestDeduplicator(t)
	subscription, err := deduplicator.Subscribe("test", DedupSubscriberCritical, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	forceHealthyDedup(deduplicator)
	base := time.Unix(0, 0)
	deduplicator.nowFunc = func() time.Time { return base }
	request, response := directTransactionPair()
	setKnownLocalAddress(deduplicator, request.Source)
	passiveEvent := passiveTransactionEvent(base, request, response)
	deduplicator.OnPassiveClassifiedEvent(passiveEvent)
	pendingBefore := len(deduplicator.pending)
	resetsBefore := expvarIntValue(observeFirstDedupEpochResetTotal)
	flushBefore := expvarMapInt64(observeFirstDedupPendingFlushTotal, "epoch_reset")
	resetEvent := PassiveClassifiedEvent{Kind: PassiveClassifiedEventDiscontinuity, ObservedAt: base.Add(100 * time.Millisecond), DiscontinuityReason: PassiveDiscontinuityTransportReset}
	deduplicator.OnPassiveClassifiedEvent(resetEvent)
	event := requireAdjudicatedEvent(t, subscription, DedupDispositionDiscontinuity)
	assertNoAdjudicatedEvent(t, subscription, 25*time.Millisecond)
	oracleWrite(t, "deduplicator/discontinuity-flushes-pending.json", oracleCase{
		Component: "active_passive_deduplicator", Source: "active_passive_deduplicator.go", Stimulus: "TestActivePassiveDeduplicator_DiscontinuityResetsEpochAndFlushesPending",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(append(append([]byte{}, request.Data...), response.Data...)),
			Qualifiers: map[string]any{"discontinuity_reason": string(PassiveDiscontinuityTransportReset)},
			Clock:      map[string]any{"base": base.UTC().Format(time.RFC3339Nano), "discontinuity_offset_ns": int64(100 * time.Millisecond)},
			State:      map[string]any{"initial_health": oracleDedupInitialHealth, "local_address": hex.EncodeToString([]byte{request.Source}), "submissions": []any{oraclePassiveSubmission(passiveEvent, base), oraclePassiveSubmission(resetEvent, base)}},
		},
		Output: oracleOutput{
			Decisions: []any{oracleAdjudicated(event)},
			Counters: map[string]any{
				"pending_before": pendingBefore, "pending_after": len(deduplicator.pending),
				"epoch_reset_total_delta":         expvarIntValue(observeFirstDedupEpochResetTotal) - resetsBefore,
				"pending_epoch_reset_flush_delta": expvarMapInt64(observeFirstDedupPendingFlushTotal, "epoch_reset") - flushBefore,
			},
		},
	})
}

func oracleDedupOverflow(t *testing.T) {
	t.Helper()
	deduplicator := newTestDeduplicator(t)
	subscription, err := deduplicator.Subscribe("overflow", DedupSubscriberCritical, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	forceHealthyDedup(deduplicator)
	request, _ := directTransactionPair()
	setKnownLocalAddress(deduplicator, request.Source)
	base := time.Unix(0, 0)
	initiatorFrame := PassiveClassifiedEvent{Kind: PassiveClassifiedEventMasterFrame, FrameType: protocol.FrameTypeInitiatorInitiator, Request: protocol.Frame{Source: 0x10, Target: request.Source, Primary: 0x01, Secondary: 0x02, Data: []byte{0x03}}, HasRequest: true, ObservedAt: base}
	resetsBefore := expvarIntValue(observeFirstDedupEpochResetTotal)
	deduplicator.OnPassiveClassifiedEvent(initiatorFrame)
	second := initiatorFrame
	second.ObservedAt = base.Add(10 * time.Millisecond)
	deduplicator.OnPassiveClassifiedEvent(second)
	event := requireAdjudicatedEvent(t, subscription, DedupDispositionDiscontinuity)
	oracleWrite(t, "deduplicator/critical-subscriber-overflow.json", oracleCase{
		Component: "active_passive_deduplicator", Source: "active_passive_deduplicator.go", Stimulus: "TestActivePassiveDeduplicator_CriticalSubscriberOverflowAdvancesEpoch",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(initiatorFrame.Request.Data),
			Qualifiers: map[string]any{"subscriber_capacity": 1, "frames": 2, "frame_type": "initiator_initiator"},
			Clock:      map[string]any{"base": base.UTC().Format(time.RFC3339Nano), "second_offset_ns": int64(10 * time.Millisecond)},
			State:      map[string]any{"initial_health": oracleDedupInitialHealth, "local_address": hex.EncodeToString([]byte{request.Source}), "submissions": []any{oraclePassiveSubmission(initiatorFrame, base), oraclePassiveSubmission(second, base)}},
		},
		Output: oracleOutput{Decisions: []any{oracleAdjudicated(event)}, Counters: map[string]any{"epoch_reset_total_delta": expvarIntValue(observeFirstDedupEpochResetTotal) - resetsBefore}},
	})
}
