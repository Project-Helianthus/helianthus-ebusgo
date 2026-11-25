package engine

import (
	"encoding/hex"
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// TestOracleEnginePolicy records the pin's remaining observe-first family
// policy and flag normalisation stimuli.
func TestOracleEnginePolicy(t *testing.T) {
	oracleB509DirectApply(t)
	oracleB509Descriptors(t)
	oracleB555(t)
	oracleB524StateDefault(t)
	oracleFlagsStateDisabled(t)
}

func oracleB509DirectApply(t *testing.T) {
	t.Helper()
	read := protocol.Frame{Source: 0x31, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x29, 0x02, 0x00}}
	write := protocol.Frame{Source: 0x31, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x0E, 0x02, 0x00, 0x55}}
	key := NewB509WatchKey(0x08, 0x0200)
	headerOnly := observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, read, DedupResponseHeaderOnly, testActiveStateObservation(key), DefaultObserveFirstFeatureFlags())
	valueBearing := observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, read, DedupResponseValueBearing, testActiveStateObservation(key), DefaultObserveFirstFeatureFlags())
	written := observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, write, DedupResponseHeaderOnly, testActiveStateObservation(key), NormalizeObserveFirstFeatureFlags(true, true, false, ObserveFirstExternalWritePolicyRecordAndInvalidate))
	oracleWrite(t, "observe-first-family-policy/b509-direct-apply-payload-read.json", oracleCase{
		Component: "observe_first_family_policy", Source: "observe_first_family_policy.go", Stimulus: "TestObserveFirstFamilyPolicy_B509DirectApplyRequiresPayloadBearingRead",
		Input: oracleInput{Bytes: hex.EncodeToString(read.Data) + hex.EncodeToString(write.Data), Qualifiers: map[string]any{"target": "08", "key": oracleKeyCanonical(key)}},
		Output: oracleOutput{Decisions: []any{
			map[string]any{"name": "header-only", "policy": oracleFamilyPolicy(headerOnly)},
			map[string]any{"name": "value-bearing", "policy": oracleFamilyPolicy(valueBearing)},
			map[string]any{"name": "write", "policy": oracleFamilyPolicy(written)},
		}},
	})
}

func oracleB509Descriptors(t *testing.T) {
	t.Helper()
	request := protocol.Frame{Source: 0x31, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x29, 0x02, 0x00}}
	key := NewB509WatchKey(0x08, 0x0200)
	cases := []struct {
		name       string
		descriptor WatchDescriptor
	}{
		{"state-default-admitted", WatchDescriptor{Key: key, SemanticClass: WatchSemanticClassState, CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault}},
		{"never-refused", WatchDescriptor{Key: key, SemanticClass: WatchSemanticClassState, CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyNever}},
		{"energy-merge-only-refused", WatchDescriptor{Key: key, SemanticClass: WatchSemanticClassState, CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyEnergyMergeOnly}},
		{"unknown-policy-refused", WatchDescriptor{Key: key, SemanticClass: WatchSemanticClassState, CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicy("unknown")}},
		{"class-policy-mismatch-refused", WatchDescriptor{Key: key, SemanticClass: WatchSemanticClassConfig, CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyConfigOptIn}},
		{"key-mismatch-refused", WatchDescriptor{Key: NewB509WatchKey(0x08, 0x0201), SemanticClass: WatchSemanticClassState, CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault}},
	}
	rows := make([]any, 0, len(cases))
	operands := make([]any, 0, len(cases))
	for _, tc := range cases {
		operands = append(operands, map[string]any{
			"name": tc.name, "observation_state": string(WatchObservationStateActive), "has_descriptor": true,
			"descriptor": map[string]any{
				"key": oracleKeyCanonical(tc.descriptor.Key), "semantic_class": string(tc.descriptor.SemanticClass),
				"correlation_policy": string(tc.descriptor.CorrelationPolicy), "direct_apply_policy": string(tc.descriptor.DirectApplyPolicy),
			},
		})
		policy := observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, request, DedupResponseValueBearing,
			WatchObservation{State: WatchObservationStateActive, Descriptor: tc.descriptor, HasDescriptor: true},
			NormalizeObserveFirstFeatureFlags(true, true, true, ObserveFirstExternalWritePolicyRecordOnly))
		rows = append(rows, map[string]any{
			"name": tc.name, "descriptor_key": oracleKeyCanonical(tc.descriptor.Key), "semantic_class": tc.descriptor.SemanticClass,
			"descriptor_direct_apply": tc.descriptor.DirectApplyPolicy, "policy": oracleFamilyPolicy(policy),
		})
	}
	oracleWrite(t, "observe-first-family-policy/b509-descriptor-admission.json", oracleCase{
		Component: "observe_first_family_policy", Source: "observe_first_family_policy.go", Stimulus: "TestObserveFirstFamilyPolicy_B509DirectApplyRequiresMatchingActiveStateDescriptor",
		Input:  oracleInput{Bytes: hex.EncodeToString(request.Data), Qualifiers: map[string]any{"request_key": oracleKeyCanonical(key), "flags": "enabled state_direct config_direct record_only", "scope": string(ObserveFirstTrafficScopePassive), "response_class": "value-bearing", "descriptors": operands}},
		Output: oracleOutput{Decisions: rows},
	})
}

func oracleB555(t *testing.T) {
	t.Helper()
	request := protocol.Frame{Source: 0x10, Target: 0x15, Primary: 0xB5, Secondary: 0x55, Data: []byte{0xA3, 0x01}}
	policy := observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, request, DedupResponseHeaderOnly, WatchObservation{State: WatchObservationStateCatalogMiss}, DefaultObserveFirstFeatureFlags())
	oracleWrite(t, "observe-first-family-policy/b555-record-invalidate.json", oracleCase{
		Component: "observe_first_family_policy", Source: "observe_first_family_policy.go", Stimulus: "TestObserveFirstFamilyPolicy_B555UsesConservativeRecordInvalidate",
		Input:  oracleInput{Bytes: hex.EncodeToString(request.Data)},
		Output: oracleOutput{Decisions: []any{oracleFamilyPolicy(policy)}},
	})
}

func oracleB524StateDefault(t *testing.T) {
	t.Helper()
	read := protocol.Frame{Source: 0x31, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x06, 0x00, 0x03, 0x01, 0x1C, 0x00}}
	key := NewB524WatchKey(0x15, 0x06, 0x03, 0x01, 0x001C)
	descriptor := func(class WatchSemanticClass, k WatchKey) WatchDescriptor {
		return WatchDescriptor{Key: k, SemanticClass: class, CorrelationPolicy: WatchCorrelationPolicyRequestResponse, DirectApplyPolicy: WatchDirectApplyPolicyStateDefault}
	}
	cases := []struct {
		name        string
		request     protocol.Frame
		observation WatchObservation
	}{
		{"catalog-miss", read, WatchObservation{State: WatchObservationStateCatalogMiss}},
		{"inactive-descriptor", read, WatchObservation{State: WatchObservationStateInactive, Descriptor: descriptor(WatchSemanticClassState, key), HasDescriptor: true}},
		{"config-descriptor", read, WatchObservation{State: WatchObservationStateActive, Descriptor: descriptor(WatchSemanticClassConfig, key), HasDescriptor: true}},
		{"timer-read", protocol.Frame{Source: 0x31, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x03, 0x01, 0x00}}, WatchObservation{State: WatchObservationStateActive, Descriptor: descriptor(WatchSemanticClassState, NewB524WatchKey(0x15, 0x03, 0x03, 0x01, 0x0000)), HasDescriptor: true}},
		{"write", protocol.Frame{Source: 0x10, Target: 0x15, Primary: 0xB5, Secondary: 0x24, Data: []byte{0x06, 0x01, 0x03, 0x01, 0x1C, 0x00}}, WatchObservation{State: WatchObservationStateActive, Descriptor: descriptor(WatchSemanticClassState, key), HasDescriptor: true}},
		{"active-state-read", read, testActiveStateObservation(key)},
	}
	rows := make([]any, 0, len(cases))
	operands := make([]any, 0, len(cases))
	fed := make([]byte, 0, 32)
	for _, tc := range cases {
		policy := observeFirstFamilyPolicy(ObserveFirstTrafficScopePassive, tc.request, DedupResponseValueBearing, tc.observation, DefaultObserveFirstFeatureFlags())
		rows = append(rows, map[string]any{"name": tc.name, "request": hex.EncodeToString(tc.request.Data), "observation_state": tc.observation.State, "policy": oracleFamilyPolicy(policy)})
		operand := map[string]any{
			"name": tc.name, "request": oracleFrame(tc.request, true), "response_class": "value-bearing",
			"observation": map[string]any{"state": tc.observation.State, "has_descriptor": tc.observation.HasDescriptor},
		}
		if tc.observation.HasDescriptor {
			operand["observation"].(map[string]any)["descriptor"] = map[string]any{
				"key": oracleKeyCanonical(tc.observation.Descriptor.Key), "semantic_class": tc.observation.Descriptor.SemanticClass,
				"correlation_policy": tc.observation.Descriptor.CorrelationPolicy, "direct_apply_policy": tc.observation.Descriptor.DirectApplyPolicy,
			}
		}
		operands = append(operands, operand)
		fed = append(fed, tc.request.Data...)
	}
	oracleWrite(t, "observe-first-family-policy/b524-state-default-requires-active-state-read.json", oracleCase{
		Component: "observe_first_family_policy", Source: "observe_first_family_policy.go", Stimulus: "TestObserveFirstFamilyPolicy_B524StateDefaultRequiresActiveDescriptorBackedStateRead",
		Input:  oracleInput{Bytes: hex.EncodeToString(fed), Qualifiers: map[string]any{"key": oracleKeyCanonical(key), "response": "value-bearing", "flags": "default", "traffic_scope": "passive", "cases": operands}},
		Output: oracleOutput{Decisions: rows},
	})
}

func oracleFlagsStateDisabled(t *testing.T) {
	t.Helper()
	rows := make([]any, 0, 2)
	for _, policy := range []ObserveFirstExternalWritePolicy{ObserveFirstExternalWritePolicyInvalidateOnly, ObserveFirstExternalWritePolicyRecordAndInvalidate} {
		rows = append(rows, map[string]any{"policy": string(policy), "flags": oracleFlags(NormalizeObserveFirstFeatureFlags(true, false, false, policy))})
	}
	oracleWrite(t, "observe-first-flags/state-disabled-keeps-valid-policy.json", oracleCase{
		Component: "observe_first_flags", Source: "observe_first_flags.go", Stimulus: "TestNormalizeObserveFirstFeatureFlags_StateDisabledKeepsValidExternalWritePolicy",
		Input:  oracleInput{Qualifiers: map[string]any{"enabled": true, "state_direct": false, "config_direct": false}},
		Output: oracleOutput{Decisions: rows},
	})
}
