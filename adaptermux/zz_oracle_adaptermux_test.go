//go:build linux || darwin

package adaptermux

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
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/adaptermux/ptytest"
	"github.com/Project-Helianthus/helianthus-ebusgo/adaptermux/v8classifier"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
	"golang.org/x/sys/unix"
)

// TestOracleAdapterMux records the pin's multiplexer, classifier and
// pseudo-terminal cases. The stimuli are the pin's own fixtures.
func TestOracleAdapterMux(t *testing.T) {
	oracleRecordTransitions(t)
	oracleRecordClassifier(t)
	oracleRecordPty(t)
}

func oracleRecordTransitions(t *testing.T) {
	t.Helper()
	for _, row := range transitionRows {
		row := row
		fixture := transitionFixtures[row.fixture]
		apply := transitionInputs[row.input]
		h := newTransitionHarness(t, fixture.mode)
		fixture.build(h)
		h.discardEffects()
		before := h.state()
		fed := oracleTransitionBytes(row.input, h.source)
		apply(h)
		after := h.state()
		effects := append([]string(nil), h.effects()...)
		h.stop()
		symbols, observations, classes := oracleClassifyEffects(effects)
		oracleWrite(t, "transition/"+row.fixture+"--"+row.input+".json", oracleCase{
			Component: "adaptermux",
			Source:    "internal/adaptermux/mux.go",
			Stimulus:  "TestMuxTransitionCharacterization/" + row.fixture + "/" + row.input,
			Input: oracleInput{
				Bytes:      fed,
				Qualifiers: map[string]any{"fixture": row.fixture, "input": row.input, "upstream": oracleUpstream(fixture.mode)},
				Clock:      map[string]any{"timers": "held", "deadline": "fires only when the input is start_deadline"},
				State:      oracleTransitionState(before),
			},
			Output: oracleOutput{
				SymbolsWritten: symbols,
				Decisions:      []any{map[string]any{"state": oracleTransitionState(after), "effects": effects, "effect_classes": classes}},
				Observations:   observations,
			},
		})
	}
}

// oracleClassifyEffects splits the harness effect log with the pin's own effect
// classifier. Only the payload of an adapter write is a symbol written;
// consumer-facing passive, active and session entries are observations; adapter
// start requests, results and errors stay in the effect list and its classes.
func oracleClassifyEffects(effects []string) (written, observations, classes []any) {
	written, observations, classes = []any{}, []any{}, []any{}
	for _, effect := range effects {
		kind, err := muxModelEffectKind(effect)
		if err != nil {
			classes = append(classes, "unclassified")
			continue
		}
		classes = append(classes, kind.String())
		switch kind {
		case muxModelEffectAdapterWrite:
			written = append(written, strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(effect, "adapter: Write "), " ", "")))
		case muxModelEffectPassiveSymbol, muxModelEffectPassiveReset, muxModelEffectPassiveLifecycle, muxModelEffectActiveByte,
			muxModelEffectSessionReceived, muxModelEffectSessionStarted, muxModelEffectSessionFailed, muxModelEffectSessionReset,
			muxModelEffectSessionError, muxModelEffectSessionInfo:
			observations = append(observations, effect)
		}
	}
	return written, observations, classes
}

func oracleUpstream(mode transitionUpstreamMode) string {
	switch mode {
	case transitionUpstreamAsync:
		return "async"
	case transitionUpstreamBlocking:
		return "blocking"
	case transitionUpstreamRawView:
		return "raw_view"
	default:
		return "unknown"
	}
}

// oracleTransitionBytes is the byte the input hands to the handler: the source
// byte of a STARTED event is the one the fixture selected (h.source), which the
// pin's harness feeds as is.
func oracleTransitionBytes(input string, selected byte) string {
	switch input {
	case "started_selected_source":
		return hex.EncodeToString([]byte{selected})
	case "wire_syn", "stream_wire_syn":
		return "aa"
	case "received_symbol":
		return "52"
	case "received_initiator_symbol", "failed", "started_other_source":
		return "10"
	case "raw_escape_lead":
		return "a9"
	case "bid":
		return "71"
	default:
		return ""
	}
}

func oracleTransitionState(state transitionState) map[string]any {
	return map[string]any{
		"owner":             state.owner,
		"gateway_txn":       state.gatewayTxn,
		"start":             state.start,
		"start_response":    state.startResponse,
		"start_blocking":    state.startBlocking,
		"blocking_call":     state.blockingCall,
		"absorb":            state.absorb,
		"regrant_fence":     state.regrantFence,
		"retired_selection": state.retiredSelection,
		"recovery":          state.recovery,
		"resync_owed":       state.resyncOwed,
		"phase":             state.phase,
		"local_losers":      state.localLosers,
		"bids":              state.bids,
	}
}

func oracleRecordClassifier(t *testing.T) {
	t.Helper()
	now := time.Unix(0, 0).UTC()
	// The pinned tests feed consecutive bytes starting at zero: 100 in ModeOff,
	// 50 in ModeShadow and 25 in ModeEnforce.
	for _, idle := range []struct {
		mode     v8classifier.Mode
		count    int
		stimulus string
	}{
		{v8classifier.ModeOff, 100, "TestClassifier_Observe_OffMode_IsNoOp"},
		{v8classifier.ModeShadow, 50, "TestClassifier_Observe_ShadowMode_CountsButDoesNotDrop"},
		{v8classifier.ModeEnforce, 25, "TestClassifier_Observe_EnforceMode_IdleStateBytesNotDropped"},
	} {
		mode := idle.mode
		classifier := v8classifier.New(mode)
		drops := make([]bool, 0, idle.count)
		forwarded := make([]any, 0, idle.count)
		fed := make([]byte, 0, idle.count)
		for i := 0; i < idle.count; i++ {
			value := byte(i)
			drop := classifier.Observe(transport.StreamEvent{Kind: transport.StreamEventByte, Byte: value}, now)
			drops = append(drops, drop)
			forwarded = append(forwarded, oracleForwarded(fmt.Sprintf("%02x", value), drop)...)
			fed = append(fed, value)
		}
		oracleWrite(t, "v8classifier/observe-idle-"+mode.String()+".json", oracleCase{
			Component: "v8classifier",
			Source:    "internal/adaptermux/v8classifier/classifier.go",
			Stimulus:  idle.stimulus,
			Input: oracleInput{
				Bytes:      hex.EncodeToString(fed),
				Qualifiers: map[string]any{"mode": mode.String(), "kind": "byte", "count": idle.count},
				Clock:      map[string]any{"at": now.Format(time.RFC3339Nano)},
				State:      map[string]any{"fsm": "idle"},
			},
			Output: oracleOutput{
				SymbolsWritten: forwarded,
				Decisions:      []any{map[string]any{"drop": drops}},
				Counters:       oracleClassifierCounters(classifier),
			},
		})
	}
	for _, mode := range []v8classifier.Mode{v8classifier.ModeOff, v8classifier.ModeShadow, v8classifier.ModeEnforce} {
		at := time.Unix(42, 0).UTC()
		if mode == v8classifier.ModeEnforce {
			at = time.Unix(7, 0).UTC()
		}
		classifier := v8classifier.New(mode)
		classifier.Observe(transport.StreamEvent{Kind: transport.StreamEventStarted, Data: 0x71}, at)
		drop := classifier.Observe(transport.StreamEvent{Kind: transport.StreamEventByte, Byte: 0xAA}, at)
		events, dropped := classifier.DrainAdminEvents()
		recorded := make([]any, 0, len(events))
		for _, event := range events {
			recorded = append(recorded, map[string]any{
				"kind": event.Kind.String(), "byte": hex.EncodeToString([]byte{event.Byte}),
				"was_escaped": event.WasEscaped, "at": event.At.UTC().Format(time.RFC3339Nano),
				"fsm_state": fmt.Sprint(event.FSMState),
			})
		}
		oracleWrite(t, "v8classifier/aa-injection-"+mode.String()+".json", oracleCase{
			Component: "v8classifier",
			Source:    "internal/adaptermux/v8classifier/classifier.go",
			Stimulus:  "TestClassifier_DropAaInjection/" + mode.String(),
			Input: oracleInput{
				Bytes:      "aa",
				Qualifiers: map[string]any{"mode": mode.String(), "started": "71"},
				Clock:      map[string]any{"at": at.Format(time.RFC3339Nano)},
				State:      map[string]any{"after_started": true},
			},
			Output: oracleOutput{
				SymbolsWritten: oracleForwarded("aa", drop),
				Decisions:      []any{map[string]any{"drop": drop, "admin_events": recorded, "admin_dropped": dropped}},
				Observations:   recorded,
				Counters:       oracleClassifierCounters(classifier),
			},
		})
	}
}

// oracleForwarded lists the symbol the classifier lets through: a symbol that
// Observe drops is not forwarded and is not listed as written.
func oracleForwarded(symbol string, drop bool) []any {
	if drop {
		return []any{}
	}
	return []any{symbol}
}

func oracleClassifierCounters(classifier *v8classifier.Classifier) map[string]any {
	return map[string]any{
		"observed_bytes_total":        classifier.ObservedBytesTotal(),
		"fsm_drop_aa_injection_total": classifier.FsmDropAaInjectionTotal(),
		"shadow_would_have_dropped":   classifier.ShadowWouldHaveDroppedTotal(),
		"enforce_drops_applied_total": classifier.EnforceDropsAppliedTotal(),
		"observed_admin_events_total": classifier.ObservedAdminEventsTotal(),
	}
}

func oracleRecordPty(t *testing.T) {
	t.Helper()
	t.Run("pair-roundtrip", func(t *testing.T) {
		oracleRecordSerialRoundTrip(t)
	})
	for _, test := range []struct {
		protocol string
		baud     uint32
	}{{"ens", 115200}, {"enh", 9600}} {
		test := test
		t.Run("mux-open-"+test.protocol, func(t *testing.T) {
			opened := ptytest.Open(t)
			adapter := ptytest.ServeAdapter(opened)
			mux := newSerialTestMux(test.protocol, opened.DevicePath, nil)
			if err := mux.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			adapter.WaitInit(t, serialTestGuard)
			got := opened.Baud(t)
			mux.connMu.Lock()
			_, isSerial := mux.conn.(*serialConn)
			mux.connMu.Unlock()
			_ = mux.Close()
			oracleWrite(t, "ptytest/mux-open-"+test.protocol+".json", oracleCase{
				Component: "ptytest",
				Source:    "internal/adaptermux/serial.go",
				Stimulus:  "TestMuxSerial_StartOpensDeviceAtTheProtocolSpeed/" + test.protocol,
				Input:     oracleInput{Bytes: "", Qualifiers: map[string]any{"protocol": test.protocol, "device": "synthetic-pty"}, Clock: map[string]any{}, State: map[string]any{}},
				Output: oracleOutput{
					Decisions: []any{map[string]any{"baud": got, "serial_connection": isSerial, "baud_matches_protocol": got == test.baud}},
					Counters:  map[string]any{"baud": got},
				},
			})
		})
	}
	t.Run("mux-absent", func(t *testing.T) {
		absent := newSerialTestMux("ens", filepath.Join(t.TempDir(), "absent"), nil)
		err := absent.Start(context.Background())
		_ = absent.Close()
		oracleWrite(t, "ptytest/mux-absent-device.json", oracleCase{
			Component: "ptytest",
			Source:    "internal/adaptermux/serial.go",
			Stimulus:  "TestMuxSerial_StartReportsAnAbsentDevice",
			Input:     oracleInput{Qualifiers: map[string]any{"protocol": "ens", "device": "absent"}, Clock: map[string]any{}, State: map[string]any{}},
			Output:    oracleOutput{Decisions: []any{map[string]any{"enoent": errors.Is(err, unix.ENOENT)}}},
		})
	})
	t.Run("mux-reopen", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "adapter")
		first := ptytest.Open(t)
		if err := os.Symlink(first.DevicePath, link); err != nil {
			t.Fatal(err)
		}
		firstAdapter := ptytest.ServeAdapter(first)
		logs := make(serialLogLines, 256)
		mux := newSerialTestMux("ens", link, logs)
		if err := mux.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		firstAdapter.WaitInit(t, serialTestGuard)
		second := ptytest.Open(t)
		distinct := second.DevicePath != first.DevicePath
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		first.Close()
		firstFailure := logs.wait(t, "no such file or directory")
		secondFailure := logs.wait(t, "no such file or directory")
		gap := secondFailure.at.Sub(firstFailure.at)
		secondAdapter := ptytest.ServeAdapter(second)
		if err := os.Symlink(second.DevicePath, link); err != nil {
			t.Fatal(err)
		}
		secondAdapter.WaitInit(t, serialTestGuard)
		reconnected := logs.wait(t, "reconnected to "+NetworkSerial+"/")
		baud := second.Baud(t)
		_ = mux.Close()
		oracleWrite(t, "ptytest/mux-reopen.json", oracleCase{
			Component: "ptytest",
			Source:    "internal/adaptermux/serial.go",
			Stimulus:  "TestMuxSerial_ReopensThePathAfterTheDeviceWentAway",
			Input:     oracleInput{Qualifiers: map[string]any{"protocol": "ens", "device": "synthetic-pty-link"}, Clock: map[string]any{"reconnect_initial": "40ms", "reconnect_max": "80ms"}, State: map[string]any{"distinct_replacement": distinct}},
			Output: oracleOutput{
				Decisions: []any{map[string]any{
					"first_failure_is_reconnect": strings.Contains(firstFailure.text, "reconnect failed"),
					"backoff_doubled":            gap >= 80*time.Millisecond,
					"reconnected":                strings.Contains(reconnected.text, "reconnected"),
					"baud":                       baud,
				}},
				Counters: map[string]any{"baud": baud},
			},
		})
	})
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

// oracleRecordSerialRoundTrip runs the pin's two serial-path fixtures on a
// pseudo-terminal pair through the configured serial connection (the poller
// port and the retry port): every symbol value crosses the receive path inside
// ENS RECEIVED frames, and a payload of control characters crosses the send path
// as ENS SEND frames.
func oracleRecordSerialRoundTrip(t *testing.T) {
	t.Helper()
	var want, receiveWire []byte
	for symbol := 0; symbol <= 0xff; symbol++ {
		want = append(want, byte(symbol))
		frame := transport.EncodeENH(transport.ENHResReceived, byte(symbol))
		receiveWire = append(receiveWire, frame[:]...)
	}
	payload := []byte{0x0a, 0x0d, 0x11, 0x13, 0x03, 0x04, 0x7f}
	var sendWire []byte
	for _, b := range payload {
		frame := transport.EncodeENH(transport.ENHReqSend, b)
		sendWire = append(sendWire, frame[:]...)
	}
	ports := []struct {
		name string
		open func(*testing.T, string, string) net.Conn
	}{{"poller", openSerialForTest}, {"retry", openRetrySerialForTest}}
	received := make([]any, 0, len(ports))
	sent := make([]any, 0, len(ports))
	peerReads := make([]any, 0, len(ports))
	receivedSymbols := map[string]any{}
	sentFrames := map[string]any{}
	for _, port := range ports {
		port := port
		t.Run("receive-"+port.name, func(t *testing.T) {
			pair := ptytest.Open(t)
			conn := port.open(t, pair.DevicePath, "ens")
			tr := transport.NewENSTransport(conn, serialTestGuard, serialTestGuard)
			pair.WritePeer(t, receiveWire)
			got := make([]byte, 0, len(want))
			for len(got) < len(want) {
				_, symbol, hasSymbol, err := tr.ReadEventWithRawSymbol()
				if err != nil {
					t.Fatalf("after %d symbols: %v", len(got), err)
				}
				if hasSymbol {
					got = append(got, symbol)
				}
			}
			receivedSymbols[port.name] = len(got)
			received = append(received, map[string]any{"port": port.name, "symbols": hex.EncodeToString(got), "matches_stimulus": string(got) == string(want)})
		})
		t.Run("send-"+port.name, func(t *testing.T) {
			pair := ptytest.Open(t)
			conn := port.open(t, pair.DevicePath, "ens")
			tr := transport.NewENSTransport(conn, serialTestGuard, serialTestGuard)
			if _, err := tr.Write(payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			got := pair.ReadPeer(t, len(sendWire), serialTestGuard)
			peerReads = append(peerReads, hex.EncodeToString(got))
			frame := transport.EncodeENH(transport.ENHReqSend, 0)
			sentFrames[port.name] = len(got) / len(frame[:])
			sent = append(sent, map[string]any{"port": port.name, "peer_read": hex.EncodeToString(got), "matches_stimulus": string(got) == string(sendWire)})
		})
	}
	if t.Failed() {
		return
	}
	oracleWrite(t, "ptytest/pair-roundtrip.json", oracleCase{
		Component: "ptytest",
		Source:    "internal/adaptermux/serial.go",
		Stimulus:  "TestSerialConn_ENSFramingPassesThrough and TestSerialConn_WritePassesThroughUnchanged",
		Input: oracleInput{
			Bytes: hex.EncodeToString(receiveWire),
			Qualifiers: map[string]any{
				"protocol": "ens", "device": "synthetic-pty", "ports": "poller, retry",
				"receive":   "every symbol 00..ff, each in one ENS RECEIVED frame written at the peer, read through the raw view",
				"send":      "payload " + hex.EncodeToString(payload) + " written through the ENS transport; peer reads the ENS SEND frames",
				"send_wire": hex.EncodeToString(sendWire),
			},
			Clock: map[string]any{"io_guard": serialTestGuard.String()},
			State: map[string]any{"serial_setup": "openSerialConn (poller) and the retry port fixture of the pin; raw 8N1, no flow control"},
		},
		Output: oracleOutput{
			SymbolsWritten: peerReads,
			Decisions:      []any{map[string]any{"receive": received, "send": sent}},
			Counters:       map[string]any{"receive_symbols": receivedSymbols, "send_frames": sentFrames},
		},
	})
}
