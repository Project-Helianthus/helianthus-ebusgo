//go:build linux || darwin

package adaptermux

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/adaptermux/v8classifier"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// TestOracleAdapterMuxUnits records the pin's arbitration selection, escape
// symbol writes, classifier event sequences and echo pacer. The stimuli are
// those of the pin's own tests.
func TestOracleAdapterMuxUnits(t *testing.T) {
	oracleRecordArbitration(t)
	oracleRecordSendSymbols(t)
	oracleRecordSendPath(t)
	oracleRecordClassifierSequences(t)
	oracleRecordPacer(t)
}

func oracleRecordArbitration(t *testing.T) {
	t.Helper()
	addresses := []byte{0x10, 0x01, 0x11}
	for state := 0; state < 4; state++ {
		hasOwner, busIdle := state&1 != 0, state&2 == 0
		rows := make([]any, 0)
		for count := 0; count <= 2; count++ {
			combinations := 1
			for i := 0; i < count; i++ {
				combinations *= len(addresses) * 2
			}
			for code := 0; code < combinations; code++ {
				refs := make([]*startRequest, count)
				n := code
				for i := range refs {
					refs[i] = &startRequest{sessionID: uint64(1 + n%2), initiator: addresses[(n/2)%3], notify: make(chan startResult, 1)}
					n /= 6
				}
				for gateway := 0; gateway < 2; gateway++ {
					var gw *startRequest
					if gateway == 1 {
						gw = &startRequest{sessionID: 0, initiator: 0x11, notify: make(chan startResult, 1)}
					}
					for mask := -1; mask < (1 << (count + gateway)); mask++ {
						var bids map[*startRequest]chan startResult
						if mask >= 0 {
							bids = make(map[*startRequest]chan startResult)
							for i, r := range refs {
								if mask&(1<<i) != 0 {
									bids[r] = r.notify
								}
							}
							if gw != nil && mask&(1<<count) != 0 {
								bids[gw] = gw.notify
							}
						}
						a := &arbitrator{pendingGateway: gw, pendingExternal: append([]*startRequest(nil), refs...), hasOwner: hasOwner}
						winner, losers := a.selectBidsAtSYN(busIdle, bids)
						pending := make([]string, 0, len(a.pendingExternal))
						pendingID := make([]string, 0, len(a.pendingExternal))
						for _, r := range a.pendingExternal {
							pending = append(pending, oracleBid(r))
							pendingID = append(pendingID, oracleBidID(r, refs, gw))
						}
						loserNames := make([]string, 0, len(losers))
						loserID := make([]string, 0, len(losers))
						for _, r := range losers {
							loserNames = append(loserNames, oracleBid(r))
							loserID = append(loserID, oracleBidID(r, refs, gw))
						}
						external := make([]string, 0, len(refs))
						for _, r := range refs {
							external = append(external, oracleBid(r))
						}
						rows = append(rows, fmt.Sprintf("ext=[%s] gw=%t mask=%d | win=%s lose=[%s] pend_gw=%t pend_ext=[%s] deferred_gw=%t | win_id=%s lose_id=[%s] pend_gw_id=%s pend_ext_id=[%s] deferred_gw_id=%s",
							strings.Join(external, ","), gw != nil, mask, oracleBid(winner), strings.Join(loserNames, ","),
							a.pendingGateway != nil, strings.Join(pending, ","), a.deferredGatewayLoser != nil,
							oracleBidID(winner, refs, gw), strings.Join(loserID, ","), oracleBidID(a.pendingGateway, refs, gw),
							strings.Join(pendingID, ","), oracleBidID(a.deferredGatewayLoser, refs, gw)))
					}
				}
			}
		}
		oracleWrite(t, fmt.Sprintf("arbitration/selection-owner%t-idle%t.json", hasOwner, busIdle), oracleCase{
			Component: "adaptermux",
			Source:    "internal/adaptermux/arbitration.go",
			Stimulus:  "TestArbitrationDecisionExhaustive",
			Input: oracleInput{
				Qualifiers: map[string]any{"has_owner": hasOwner, "bus_idle": busIdle, "initiators": "10 01 11", "sessions": "1 2", "gateway_initiator": "11", "max_external": 2,
					"request_identity": "x<n> is the n-th external request of the row in queue order starting at 0, gw is the gateway request; *_id fields name requests by that identity"},
				State: map[string]any{"snapshot_mask": "-1 selects every pending bid; otherwise a bit per external bid then the gateway bid"},
			},
			Output: oracleOutput{Decisions: rows, Counters: map[string]any{"rows": len(rows)}},
		})
	}
}

// oracleBid names a bid by its session and initiator.
func oracleBid(r *startRequest) string {
	if r == nil {
		return "none"
	}
	return fmt.Sprintf("s%d:%02x", r.sessionID, r.initiator)
}

// oracleBidID names a bid by the identity of its request object: "x<n>" is the
// n-th external request of the row (queue order, zero based) and "gw" the
// gateway request, so equal-looking bids stay distinguishable.
func oracleBidID(r *startRequest, refs []*startRequest, gw *startRequest) string {
	if r == nil {
		return "none"
	}
	if r == gw {
		return "gw"
	}
	for i, x := range refs {
		if x == r {
			return fmt.Sprintf("x%d", i)
		}
	}
	return "unknown"
}

func oracleRecordSendSymbols(t *testing.T) {
	t.Helper()
	rows := make([]any, 0, 512)
	for _, structural := range []bool{false, true} {
		for value := 0; value < 256; value++ {
			symbols := gatewaySendSymbols(byte(value), structural)
			rows = append(rows, map[string]any{"data": fmt.Sprintf("%02x", value), "structural": structural, "symbols": hex.EncodeToString(symbols)})
		}
	}
	oracleWrite(t, "send-symbols/gateway-send-symbols.json", oracleCase{
		Component: "adaptermux",
		Source:    "internal/adaptermux/raw_symbols.go",
		Stimulus:  "TestRawContinuity_GatewaySendSymbols",
		Input:     oracleInput{Bytes: "00..ff", Qualifiers: map[string]any{"structural": "false and true for every data byte"}},
		Output:    oracleOutput{Decisions: rows, Counters: map[string]any{"rows": len(rows)}},
	})
}

// oracleRecordSendPath drives the production send path (doSendNormalized) with
// the fixture of the pin's write-normalization test and records the symbols the
// upstream received, for the gateway session and for an external session.
func oracleRecordSendPath(t *testing.T) {
	t.Helper()
	type send struct {
		session    uint64
		data       byte
		structural bool
	}
	scenarios := []struct {
		name         string
		protocol     string
		readsLogical bool
		sends        []send
	}{
		{"enh-logical-both-ways", "enh", true, []send{{gatewaySessionID, 0xA9, false}, {gatewaySessionID, 0xAA, false}, {gatewaySessionID, 0xAA, true}, {7, 0xA9, false}}},
		{"ens-logical-both-ways", "ens", true, []send{{gatewaySessionID, 0xA9, false}}},
		{"raw-receive-side", "enh", false, []send{{gatewaySessionID, 0xA9, false}}},
		{"send-side-not-raw-symbol", "plain", true, []send{{gatewaySessionID, 0xA9, false}, {gatewaySessionID, 0xAA, false}}},
	}
	rows := make([]any, 0, len(scenarios))
	written := make([]any, 0, 8)
	fed := make([]byte, 0, 8)
	operands := make([]any, 0, len(scenarios))
	for _, scenario := range scenarios {
		planned := make([]any, 0, len(scenario.sends))
		for _, step := range scenario.sends {
			planned = append(planned, map[string]any{"session": step.session, "data": fmt.Sprintf("%02x", step.data), "structural": step.structural})
		}
		operands = append(operands, map[string]any{"scenario": scenario.name, "protocol": scenario.protocol, "upstream_reads_logical": scenario.readsLogical, "sends": planned})
	}
	for _, scenario := range scenarios {
		mux := New(Config{Protocol: scenario.protocol, Logger: log.New(io.Discard, "", 0)})
		ctx, cancel := context.WithCancel(context.Background())
		mux.ctx, mux.cancel = ctx, cancel
		up := &writeRecorderUpstream{unescaped: scenario.readsLogical}
		mux.upstream = up
		activeLogical := mux.activeReadsAreLogical()
		callerUnescaped := mux.ActiveTransport().(transport.EscapeAware).BytesAreUnescaped()
		sends := make([]any, 0, len(scenario.sends))
		for _, step := range scenario.sends {
			mux.arb.forceRelease()
			mux.arb.confirmOwnership(step.session, 0x10)
			up.mu.Lock()
			up.writes = nil
			up.mu.Unlock()
			err := mux.doSendNormalized(step.session, step.data, step.structural)
			up.mu.Lock()
			got := hex.EncodeToString(up.writes)
			up.mu.Unlock()
			sends = append(sends, map[string]any{
				"session": step.session, "data": fmt.Sprintf("%02x", step.data), "structural": step.structural,
				"upstream_writes": got, "error": oracleErrText(err),
			})
			written = append(written, got)
			fed = append(fed, step.data)
		}
		cancel()
		rows = append(rows, map[string]any{
			"scenario": scenario.name, "protocol": scenario.protocol, "upstream_reads_logical": scenario.readsLogical,
			"active_reads_are_logical": activeLogical, "active_transport_bytes_are_unescaped": callerUnescaped, "sends": sends,
		})
	}
	oracleWrite(t, "send-path/write-normalization.json", oracleCase{
		Component: "adaptermux",
		Source:    "internal/adaptermux/raw_symbols.go",
		Stimulus:  "TestRawContinuity_WriteNormalizationIsIndependentOfReceiveFlag",
		Input: oracleInput{
			Bytes:      hex.EncodeToString(fed),
			Qualifiers: map[string]any{"scenarios": operands, "gateway_session": gatewaySessionID, "external_session": 7, "owner_initiator": "10"},
			State:      map[string]any{"upstream": "write recorder without a raw view; ownership confirmed for the sending session before each send"},
		},
		Output: oracleOutput{SymbolsWritten: written, Decisions: rows, Counters: map[string]any{"scenarios": len(scenarios), "sends": len(written)}},
	})
}

type oracleStep struct {
	kind    string
	value   byte
	escaped bool
}

func oracleStreamEvent(step oracleStep) transport.StreamEvent {
	switch step.kind {
	case "started":
		return transport.StreamEvent{Kind: transport.StreamEventStarted, Data: step.value}
	case "failed":
		return transport.StreamEvent{Kind: transport.StreamEventFailed, Data: step.value}
	case "reset":
		return transport.StreamEvent{Kind: transport.StreamEventReset}
	case "wire_syn":
		return transport.StreamEvent{Kind: transport.StreamEventWireSyn, Byte: step.value}
	default:
		return transport.StreamEvent{Kind: transport.StreamEventByte, Byte: step.value, WasEscaped: step.escaped}
	}
}

func oracleRecordClassifierSequences(t *testing.T) {
	t.Helper()
	sequences := []struct {
		name     string
		stimulus string
		steps    []oracleStep
	}{
		{"telegram-lifecycle", "TestFSM_MultipleTelegrams_StateLifecycle", []oracleStep{{"started", 0x71, false}, {"byte", 0x10, false}, {"reset", 0, false}, {"started", 0x10, false}}},
		{"protocol-fault-cascade", "TestFSM_StateAborted_ContinuationCountsFaults", []oracleStep{{"started", 0x71, false}, {"byte", 0x10, false}, {"byte", 0xB5, false}, {"byte", 0x16, false}, {"byte", 0xFF, false}, {"byte", 0x00, false}, {"byte", 0x01, false}, {"byte", 0x02, false}, {"byte", 0x03, false}, {"byte", 0x04, false}, {"reset", 0, false}, {"byte", 0x55, false}}},
		{"legitimate-bytes", "TestFSM_EnforceMode_LegitimateBytesNotDropped", []oracleStep{{"started", 0x71, false}, {"byte", 0x10, false}, {"byte", 0xB5, false}, {"byte", 0x16, false}}},
		{"wire-syn-mid-frame", "TestStreamEventWireSyn_EnforceMode_Drops", []oracleStep{{"started", 0x71, false}, {"wire_syn", 0xAA, false}}},
		{"failed-enters-passive", "TestFSM_StreamEventFailed_EntersPassiveTracking", []oracleStep{{"failed", 0x31, false}, {"byte", 0x10, false}}},
		{"provenance-escaped-aa", "TestProvenance_EscapedPayloadAa", []oracleStep{{"byte", 0xAA, true}, {"byte", 0xAA, true}, {"byte", 0xAA, true}, {"byte", 0xAA, true}, {"byte", 0xAA, true}}},
		{"provenance-escaped-esc", "TestProvenance_EscapedPayloadEsc", []oracleStep{{"byte", 0xA9, true}, {"byte", 0xA9, true}}},
		{"provenance-mixed", "TestProvenance_EscapedPayloadAa", []oracleStep{{"byte", 0x31, false}, {"byte", 0xAA, true}, {"byte", 0xA9, true}, {"wire_syn", 0xAA, false}, {"byte", 0x52, false}}},
	}
	at := time.Unix(0, 0).UTC()
	for _, mode := range []v8classifier.Mode{v8classifier.ModeOff, v8classifier.ModeShadow, v8classifier.ModeEnforce} {
		for _, sequence := range sequences {
			classifier := v8classifier.New(mode)
			trace := make([]any, 0, len(sequence.steps))
			replayed := make([]any, 0, len(sequence.steps))
			symbols := make([]any, 0, len(sequence.steps))
			bytes := make([]string, 0, len(sequence.steps))
			for _, step := range sequence.steps {
				replayed = append(replayed, map[string]any{"event": step.kind, "value": fmt.Sprintf("%02x", step.value), "escaped": step.escaped})
				drop := classifier.Observe(oracleStreamEvent(step), at)
				trace = append(trace, map[string]any{
					"event": step.kind, "value": fmt.Sprintf("%02x", step.value), "escaped": step.escaped,
					"drop": drop, "fsm_state": fmt.Sprint(classifier.FSMState()), "passive": classifier.FSMIsPassive(),
				})
				if step.kind == "byte" || step.kind == "wire_syn" {
					bytes = append(bytes, fmt.Sprintf("%02x", step.value))
					if !drop {
						symbols = append(symbols, fmt.Sprintf("%02x", step.value))
					}
				}
			}
			events, dropped := classifier.DrainAdminEvents()
			recorded := make([]any, 0, len(events))
			for _, event := range events {
				recorded = append(recorded, map[string]any{
					"kind": event.Kind.String(), "byte": hex.EncodeToString([]byte{event.Byte}),
					"was_escaped": event.WasEscaped, "at": event.At.UTC().Format(time.RFC3339Nano), "fsm_state": fmt.Sprint(event.FSMState),
				})
			}
			counters := oracleClassifierCounters(classifier)
			counters["fsm_forward_total"] = classifier.FsmForwardTotal()
			counters["fsm_protocol_fault_total"] = classifier.FsmProtocolFaultTotal()
			counters["fsm_enter_passive_total"] = classifier.FsmEnterPassiveTotal()
			counters["fsm_reset_total"] = classifier.FsmResetTotal()
			counters["escaped_payload_aa_total"] = classifier.EscapedPayloadAaTotal()
			counters["escaped_payload_esc_total"] = classifier.EscapedPayloadEscTotal()
			counters["wire_auto_syn_total"] = classifier.WireAutoSynTotal()
			counters["plain_byte_total"] = classifier.PlainByteTotal()
			counters["admin_dropped"] = dropped
			oracleWrite(t, "v8classifier/sequence-"+sequence.name+"-"+mode.String()+".json", oracleCase{
				Component: "v8classifier",
				Source:    "internal/adaptermux/v8classifier/classifier.go",
				Stimulus:  sequence.stimulus + "/" + mode.String(),
				Input: oracleInput{
					Bytes:      strings.Join(bytes, ""),
					Qualifiers: map[string]any{"mode": mode.String(), "sequence": sequence.name},
					Clock:      map[string]any{"at": at.Format(time.RFC3339Nano)},
					State:      map[string]any{"fsm": "idle", "events": replayed},
				},
				Output: oracleOutput{SymbolsWritten: symbols, Decisions: trace, Observations: recorded, Counters: counters},
			})
		}
	}
	for _, tc := range []struct{ name, text string }{{"off", "off"}, {"shadow", "shadow"}, {"enforce", "enforce"}, {"empty", ""}, {"capitalised", "Shadow"}, {"unknown", "bogus"}} {
		mode, err := v8classifier.ParseMode(tc.text)
		oracleWrite(t, "v8classifier/parse-mode-"+tc.name+".json", oracleCase{
			Component: "v8classifier",
			Source:    "internal/adaptermux/v8classifier/mode.go",
			Stimulus:  "TestParseMode/" + tc.name,
			Input:     oracleInput{Qualifiers: map[string]any{"text": tc.text}},
			Output:    oracleOutput{Decisions: []any{map[string]any{"mode": mode.String(), "error": oracleErrText(err)}}},
		})
	}
}

func oracleErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func oracleRecordPacer(t *testing.T) {
	t.Helper()
	t0 := time.Unix(1_000_000_000, 0)
	offset := func(value time.Time) int64 { return int64(value.Sub(t0)) }
	oracleWrite(t, "pacer/schedule-cadence.json", func() oracleCase {
		p := v8classifier.NewPacer()
		first := p.Schedule(t0)
		early := p.Schedule(t0.Add(time.Millisecond))
		late := p.Schedule(t0.Add(100 * time.Millisecond))
		burst := v8classifier.NewPacer()
		burstSlots := make([]any, 0, 6)
		for i := 0; i < 6; i++ {
			burstSlots = append(burstSlots, offset(burst.Schedule(t0)))
		}
		return oracleCase{
			Component: "v8classifier",
			Source:    "internal/adaptermux/v8classifier/pacer.go",
			Stimulus:  "TestPacer_Schedule_SubsequentCallEnforcesCadence/CallerLateAdvancesCadence/BurstSequence",
			Input:     oracleInput{Qualifiers: map[string]any{"calls_ns": []any{0, int64(time.Millisecond), int64(100 * time.Millisecond)}, "burst": 6}, Clock: map[string]any{"t0_unix": t0.Unix()}},
			Output: oracleOutput{
				Decisions: []any{map[string]any{"emit_ns": []any{offset(first), offset(early), offset(late)}, "last_scheduled_ns": offset(p.LastScheduledEmit()), "burst_emit_ns": burstSlots}},
				Counters:  map[string]any{"tau_ns": int64(v8classifier.TauWireByte)},
			},
		}
	}())
	oracleWrite(t, "pacer/set-tau.json", func() oracleCase {
		p := v8classifier.NewPacer()
		accepted := []any{p.SetTau(5 * time.Millisecond), p.SetTau(0), p.SetTau(-time.Second)}
		p.Schedule(t0)
		second := p.Schedule(t0)
		return oracleCase{
			Component: "v8classifier",
			Source:    "internal/adaptermux/v8classifier/pacer.go",
			Stimulus:  "TestPacer_SetTau_RejectsInvalid",
			Input:     oracleInput{Qualifiers: map[string]any{"tau_ns": []any{int64(5 * time.Millisecond), 0, int64(-time.Second)}}, Clock: map[string]any{"t0_unix": t0.Unix()}},
			Output:    oracleOutput{Decisions: []any{map[string]any{"accepted": accepted, "second_emit_ns": offset(second)}}},
		}
	}())
	oracleWrite(t, "pacer/echo-samples.json", func() oracleCase {
		p := v8classifier.NewPacer()
		initial := p.Lrtt()
		p.RecordEcho(-time.Millisecond, t0)
		p.RecordEcho(0, t0)
		rejected := p.RecordedSamples()
		p.RecordEcho(50*time.Millisecond, t0)
		afterFirst := p.Lrtt()
		for i := 1; i < 100; i++ {
			p.RecordEcho(50*time.Millisecond, t0.Add(time.Duration(i)*100*time.Millisecond))
		}
		return oracleCase{
			Component: "v8classifier",
			Source:    "internal/adaptermux/v8classifier/pacer.go",
			Stimulus:  "TestPacer_RecordEcho_FirstSampleUpdatesEMA/EMAConvergence/RejectsInvalidRtt",
			Input:     oracleInput{Qualifiers: map[string]any{"rtt_ns": []any{int64(-time.Millisecond), 0, int64(50 * time.Millisecond)}, "valid_samples": 100}, Clock: map[string]any{"t0_unix": t0.Unix(), "spacing_ns": int64(100 * time.Millisecond)}},
			Output: oracleOutput{
				Decisions: []any{map[string]any{"lrtt_initial_ns": int64(initial), "samples_after_invalid": rejected, "lrtt_after_first_ns": int64(afterFirst), "lrtt_after_100_ns": int64(p.Lrtt())}},
				Counters:  map[string]any{"recorded_samples": p.RecordedSamples()},
			},
		}
	}())
	oracleWrite(t, "pacer/grace-and-deadlines.json", func() oracleCase {
		take := func(p *v8classifier.Pacer) map[string]any {
			soft, hard, enabled := p.EchoDeadlines()
			return map[string]any{"soft_ns": int64(soft), "hard_ns": int64(hard), "hard_enabled": enabled, "grace": p.InGraceBootstrap()}
		}
		fresh := v8classifier.NewPacer()
		normal := take(fresh)
		p := v8classifier.NewPacer()
		p.RecordEcho(50*time.Millisecond, t0)
		p.BeforeActiveWrite(t0.Add(29 * time.Second))
		short := take(p)
		p.BeforeActiveWrite(t0.Add(v8classifier.GraceIdleThreshold))
		boundary := take(p)
		steps := []any{}
		for i := 0; i < 3; i++ {
			p.RecordEcho(50*time.Millisecond, t0.Add(31*time.Second).Add(time.Duration(i)*100*time.Millisecond))
			steps = append(steps, take(p))
		}
		empty := v8classifier.NewPacer()
		empty.BeforeActiveWrite(t0)
		return oracleCase{
			Component: "v8classifier",
			Source:    "internal/adaptermux/v8classifier/pacer.go",
			Stimulus:  "TestPacer_EchoDeadlines_NormalMode/GraceBootstrap_NotTriggeredByShortIdle/BoundaryExactly30s/NoPriorHistoryIsNoOp",
			Input:     oracleInput{Qualifiers: map[string]any{"idle_ns": []any{int64(29 * time.Second), int64(v8classifier.GraceIdleThreshold)}}, Clock: map[string]any{"t0_unix": t0.Unix()}},
			Output: oracleOutput{
				Decisions: []any{map[string]any{"fresh": normal, "after_29s_idle": short, "at_30s_idle": boundary, "grace_echoes": steps, "no_history_grace": empty.InGraceBootstrap()}},
				Counters:  map[string]any{"before_active_write_total": p.BeforeActiveWriteTotal(), "grace_idle_threshold_ns": int64(v8classifier.GraceIdleThreshold)},
			},
		}
	}())
}
