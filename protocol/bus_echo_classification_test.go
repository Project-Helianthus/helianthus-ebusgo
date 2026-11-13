package protocol_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// TestBusEchoClassification pins how Bus.Send classifies the echo of every
// written byte today: the first byte after arbitration, a mid-telegram
// payload byte and the end-of-message SYN, over three transport flavours.
// It is a characterization test: each row records the returned error, the
// observer events, the AUTO-SYN drain counters and the transport-call trace
// exactly as the current implementation produces them.

type echoClassFlavour int

const (
	// echoClassPlain carries wire bytes and has no escape provenance.
	echoClassPlain echoClassFlavour = iota
	// echoClassEnh delivers unescaped bytes with escape provenance, owns
	// arbitration and accepts the structural-SYN write signal.
	echoClassEnh
	// echoClassUnescaped delivers unescaped bytes without escape provenance.
	echoClassUnescaped
)

func (f echoClassFlavour) String() string {
	switch f {
	case echoClassPlain:
		return "plain"
	case echoClassEnh:
		return "enh"
	case echoClassUnescaped:
		return "unescaped"
	default:
		return "unknown"
	}
}

// echoClassRead is one scripted echo read: value, escape flag and error.
type echoClassRead struct {
	value   byte
	escaped bool
	err     error
}

// echoClassCore records every transport call and serves echo reads. It
// echoes every write unless the write position carries a script.
type echoClassCore struct {
	mu         sync.Mutex
	escapeEcho bool
	calls      []string
	writes     int
	structural bool
	pending    []echoClassRead

	scriptAt    int
	script      []echoClassRead
	writeErr    error
	shortWrite  bool
	hasScripted bool
}

func (c *echoClassCore) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(p) != 1 {
		c.calls = append(c.calls, fmt.Sprintf("write % X", p))
	} else {
		c.calls = append(c.calls, fmt.Sprintf("write %02X", p[0]))
	}
	index := c.writes
	c.writes++
	structural := c.structural
	c.structural = false
	if c.hasScripted && index == c.scriptAt {
		if c.writeErr != nil {
			return 0, c.writeErr
		}
		if c.shortWrite {
			return 0, nil
		}
		if c.script != nil {
			c.pending = append(c.pending, c.script...)
			return len(p), nil
		}
	}
	for _, value := range p {
		escaped := c.escapeEcho && !structural &&
			(value == protocol.SymbolSyn || value == protocol.SymbolEscape)
		c.pending = append(c.pending, echoClassRead{value: value, escaped: escaped})
	}
	return len(p), nil
}

func (c *echoClassCore) next(call string) (byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	if len(c.pending) == 0 {
		return 0, false, ebuserrors.ErrTimeout
	}
	read := c.pending[0]
	c.pending = c.pending[1:]
	if read.err != nil {
		return 0, false, read.err
	}
	return read.value, read.escaped, nil
}

func (c *echoClassCore) ReadByte() (byte, error) {
	value, _, err := c.next("read")
	return value, err
}

func (c *echoClassCore) Close() error { return nil }

func (c *echoClassCore) trace() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// echoClassPlainTransport implements only the raw transport contract.
type echoClassPlainTransport struct{ *echoClassCore }

// echoClassUnescapedTransport reports unescaped bytes but has no escape
// provenance, no arbitration and no structural-SYN signal.
type echoClassUnescapedTransport struct{ *echoClassCore }

func (echoClassUnescapedTransport) BytesAreUnescaped() bool { return true }

// echoClassEnhTransport is the ENH-class flavour.
type echoClassEnhTransport struct{ *echoClassCore }

func (echoClassEnhTransport) BytesAreUnescaped() bool { return true }

func (t echoClassEnhTransport) ReadByteWithEscape() (byte, bool, error) {
	return t.next("read_esc")
}

func (t echoClassEnhTransport) StartArbitration(initiator byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, fmt.Sprintf("arb %02X", initiator))
	return nil
}

func (t echoClassEnhTransport) SignalNextWriteIsStructuralSyn() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, "signal")
	t.structural = true
}

var (
	_ transport.RawTransport            = echoClassPlainTransport{}
	_ transport.EscapeAware             = echoClassUnescapedTransport{}
	_ transport.EscapeAware             = echoClassEnhTransport{}
	_ transport.EscapeFlaggedReader     = echoClassEnhTransport{}
	_ transport.StructuralWriteSignaler = echoClassEnhTransport{}
)

// echoClassEvent is the projection of a BusEvent the rows assert on.
type echoClassEvent struct {
	kind    protocol.BusEventKind
	outcome protocol.BusOutcomeClass
	value   byte
	known   bool
	escaped bool
}

type echoClassObserver struct {
	mu     sync.Mutex
	events []echoClassEvent
}

func (o *echoClassObserver) OnBusEvent(event protocol.BusEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, echoClassEvent{
		kind:    event.Kind,
		outcome: event.Outcome,
		value:   event.Byte,
		known:   event.ByteKnown,
		escaped: event.EchoWasEscaped,
	})
	return nil
}

func (o *echoClassObserver) snapshot() []echoClassEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]echoClassEvent(nil), o.events...)
}

type echoClassCounters struct {
	absorbed  uint64
	recovered uint64
	exhausted uint64
	round9    uint64
}

func echoClassReadCounters(bus *protocol.Bus) echoClassCounters {
	return echoClassCounters{
		absorbed:  bus.PayloadAaAutoSynAbsorbed(),
		recovered: bus.PayloadAaAutoSynRecovered(),
		exhausted: bus.PayloadAaAutoSynDrainExhausted(),
		round9:    bus.Round9AbsorbEntered(),
	}
}

func echoClassTX(value byte) echoClassEvent {
	return echoClassEvent{kind: protocol.BusEventTX, outcome: protocol.BusOutcomeSuccess, value: value}
}

func echoClassRX(value byte) echoClassEvent {
	return echoClassEvent{kind: protocol.BusEventRX, outcome: protocol.BusOutcomeSuccess, value: value}
}

func echoClassMismatch(value byte, escaped bool) echoClassEvent {
	return echoClassEvent{
		kind:    protocol.BusEventEchoMismatch,
		outcome: protocol.BusOutcomeEchoMismatch,
		value:   value,
		known:   true,
		escaped: escaped,
	}
}

func echoClassRequestComplete(outcome protocol.BusOutcomeClass) echoClassEvent {
	return echoClassEvent{kind: protocol.BusEventRequestComplete, outcome: outcome}
}

var (
	echoClassArbitration = echoClassEvent{kind: protocol.BusEventArbitration, outcome: protocol.BusOutcomeSuccess}
	echoClassAttemptDone = echoClassEvent{kind: protocol.BusEventAttemptComplete, outcome: protocol.BusOutcomeSuccess}
	echoClassTimeoutEv   = echoClassEvent{kind: protocol.BusEventTimeout, outcome: protocol.BusOutcomeTimeout}
	echoClassUnknownEcho = echoClassEvent{kind: protocol.BusEventEchoMismatch, outcome: protocol.BusOutcomeEchoMismatch}
)

const (
	echoClassSource    = byte(0x10)
	echoClassPrimary   = byte(0xB5)
	echoClassSecondary = byte(0x16)
	echoClassForeign   = byte(0x30) // initiator-class address, not ours
	echoClassTargetCls = byte(0x15) // target-class address
)

func echoClassFrame(data byte) protocol.Frame {
	return protocol.Frame{
		Source:    echoClassSource,
		Target:    protocol.AddressBroadcast,
		Primary:   echoClassPrimary,
		Secondary: echoClassSecondary,
		Data:      []byte{data},
	}
}

func echoClassCRC(data byte) byte {
	return protocol.CRC([]byte{
		echoClassSource, protocol.AddressBroadcast, echoClassPrimary, echoClassSecondary, 0x01, data,
	})
}

// echoClassWireWrites lists the single-byte writes Bus.Send issues for the
// broadcast frame carrying one data byte on the given flavour.
func echoClassWireWrites(t *testing.T, flavour echoClassFlavour, data byte) []byte {
	t.Helper()
	crc := echoClassCRC(data)
	if crc == protocol.SymbolSyn || crc == protocol.SymbolEscape {
		t.Fatalf("fixture CRC 0x%02X needs escaping; choose another data byte", crc)
	}
	var writes []byte
	if flavour != echoClassEnh {
		writes = append(writes, echoClassSource)
	}
	writes = append(writes, protocol.AddressBroadcast, echoClassPrimary, echoClassSecondary, 0x01)
	if flavour == echoClassPlain && (data == protocol.SymbolSyn || data == protocol.SymbolEscape) {
		escaped := byte(0x00)
		if data == protocol.SymbolSyn {
			escaped = 0x01
		}
		writes = append(writes, protocol.SymbolEscape, escaped)
	} else {
		writes = append(writes, data)
	}
	return append(writes, crc, protocol.SymbolSyn)
}

func echoClassReadCall(flavour echoClassFlavour) string {
	if flavour == echoClassEnh {
		return "read_esc"
	}
	return "read"
}

// echoClassCleanSpan returns the events and calls produced when every write
// in writes[from:to] is echoed unchanged. The last write is the structural
// end-of-message SYN.
func echoClassCleanSpan(flavour echoClassFlavour, writes []byte, from, to int) ([]echoClassEvent, []string) {
	var events []echoClassEvent
	var calls []string
	for i := from; i < to; i++ {
		if flavour == echoClassEnh && i == len(writes)-1 {
			calls = append(calls, "signal")
		}
		calls = append(calls, fmt.Sprintf("write %02X", writes[i]), echoClassReadCall(flavour))
		events = append(events, echoClassTX(writes[i]), echoClassRX(writes[i]))
	}
	return events, calls
}

type echoClassRow struct {
	name    string
	flavour echoClassFlavour
	data    byte
	// at is the write position whose echo is scripted; -1 echoes everything.
	at         int
	script     []echoClassRead
	writeErr   error
	shortWrite bool

	wantSentinel error
	wantText     string
	// wantOutcome is the RequestComplete outcome for failing rows.
	wantOutcome protocol.BusOutcomeClass
	// atEvents and atCalls describe the scripted position only; clean
	// positions before it (and after it on success) are derived.
	atEvents     []echoClassEvent
	atCalls      []string
	wantCounters echoClassCounters
}

func TestBusEchoClassification(t *testing.T) {
	t.Parallel()

	if protocol.AddressClassOf(echoClassForeign) != protocol.AddressClassOf(echoClassSource) {
		t.Fatalf("fixture 0x%02X must share the class of source 0x%02X", echoClassForeign, echoClassSource)
	}
	if protocol.AddressClassOf(echoClassTargetCls) == protocol.AddressClassOf(echoClassSource) {
		t.Fatalf("fixture 0x%02X must not share the class of source 0x%02X", echoClassTargetCls, echoClassSource)
	}

	collision := ebuserrors.ErrBusCollision.Error()
	writeFailure := fmt.Errorf("adapter write echo mismatch: %w", ebuserrors.ErrBusCollision)
	const syn = protocol.SymbolSyn

	rows := []echoClassRow{
		// Clean echo at every position (first byte, payload, end SYN).
		{name: "plain/clean", flavour: echoClassPlain, data: 0x01, at: -1},
		{name: "plain/clean_payload_aa_escape_pair", flavour: echoClassPlain, data: syn, at: -1},
		{name: "plain/clean_payload_a9_escape_pair", flavour: echoClassPlain, data: protocol.SymbolEscape, at: -1},
		{name: "enh/clean", flavour: echoClassEnh, data: 0x01, at: -1},
		{name: "enh/clean_payload_aa_escape_decoded", flavour: echoClassEnh, data: syn, at: -1},
		{name: "unescaped/clean", flavour: echoClassUnescaped, data: 0x01, at: -1},

		// Plain SYN intrusion at a non-SYN payload position.
		{
			name: "plain/syn_intrusion_payload", flavour: echoClassPlain, data: 0x01, at: 5,
			script:       []echoClassRead{{value: syn}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: unexpected syn while waiting for echo: " + collision,
			wantOutcome:  protocol.BusOutcomeCollision,
			atEvents:     []echoClassEvent{echoClassTX(0x01), echoClassRX(syn), echoClassMismatch(syn, false)},
			atCalls:      []string{"write 01", "read"},
		},
		{
			name: "enh/syn_intrusion_payload", flavour: echoClassEnh, data: 0x01, at: 4,
			script:       []echoClassRead{{value: syn}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: unexpected syn while waiting for echo: " + collision,
			wantOutcome:  protocol.BusOutcomeCollision,
			atEvents:     []echoClassEvent{echoClassTX(0x01), echoClassRX(syn), echoClassMismatch(syn, false)},
			atCalls:      []string{"write 01", "read_esc"},
		},
		// Escape-flagged SYN intrusion: not a wire SYN, falls to the value mismatch.
		{
			name: "enh/escape_flagged_syn_intrusion_payload", flavour: echoClassEnh, data: 0x01, at: 4,
			script:       []echoClassRead{{value: syn, escaped: true}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0x01, got 0xaa): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents:     []echoClassEvent{echoClassTX(0x01), echoClassRX(syn), echoClassMismatch(syn, true)},
			atCalls:      []string{"write 01", "read_esc"},
		},
		// Structural end-of-message SYN answered by an escape-decoded 0xAA.
		{
			name: "enh/structural_syn_escape_decoded_echo", flavour: echoClassEnh, data: 0x01, at: 6,
			script:       []echoClassRead{{value: syn, escaped: true}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo expected real wire SYN (0xAA) but received escape-decoded payload 0xAA: " + collision,
			wantOutcome:  protocol.BusOutcomeCollision,
			atEvents:     []echoClassEvent{echoClassTX(syn), echoClassRX(syn), echoClassMismatch(syn, true)},
			atCalls:      []string{"signal", "write AA", "read_esc"},
		},
		// Payload 0xAA with AUTO-SYN interference.
		{
			name: "enh/payload_aa_auto_syn_recovered_after_one", flavour: echoClassEnh, data: syn, at: 4,
			script:       []echoClassRead{{value: syn}, {value: syn, escaped: true}},
			atEvents:     []echoClassEvent{echoClassTX(syn), echoClassRX(syn), echoClassRX(syn)},
			atCalls:      []string{"write AA", "read_esc", "read_esc"},
			wantCounters: echoClassCounters{absorbed: 1, recovered: 1, round9: 1},
		},
		{
			name: "enh/payload_aa_auto_syn_recovered_after_three", flavour: echoClassEnh, data: syn, at: 4,
			script: []echoClassRead{{value: syn}, {value: syn}, {value: syn}, {value: syn, escaped: true}},
			atEvents: []echoClassEvent{
				echoClassTX(syn), echoClassRX(syn), echoClassRX(syn), echoClassRX(syn), echoClassRX(syn),
			},
			atCalls:      []string{"write AA", "read_esc", "read_esc", "read_esc", "read_esc"},
			wantCounters: echoClassCounters{absorbed: 3, recovered: 1, round9: 1},
		},
		{
			name: "enh/payload_aa_auto_syn_drain_cap", flavour: echoClassEnh, data: syn, at: 4,
			script:       []echoClassRead{{value: syn}, {value: syn}, {value: syn}, {value: syn}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo for payload 0xAA expected escape-decoded WasEscaped=true but received real wire SYN: " + collision,
			wantOutcome:  protocol.BusOutcomeCollision,
			atEvents: []echoClassEvent{
				echoClassTX(syn), echoClassRX(syn), echoClassRX(syn), echoClassRX(syn), echoClassRX(syn),
				echoClassMismatch(syn, false),
			},
			atCalls:      []string{"write AA", "read_esc", "read_esc", "read_esc", "read_esc"},
			wantCounters: echoClassCounters{absorbed: 3, exhausted: 1, round9: 1},
		},
		{
			name: "enh/payload_aa_auto_syn_non_syn_drained", flavour: echoClassEnh, data: syn, at: 4,
			script:       []echoClassRead{{value: syn}, {value: 0x05}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0xaa, got 0x05): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents: []echoClassEvent{
				echoClassTX(syn), echoClassRX(syn), echoClassRX(0x05), echoClassMismatch(0x05, false),
			},
			atCalls:      []string{"write AA", "read_esc", "read_esc"},
			wantCounters: echoClassCounters{absorbed: 1, round9: 1},
		},
		{
			name: "enh/payload_aa_auto_syn_read_error_while_draining", flavour: echoClassEnh, data: syn, at: 4,
			script:       []echoClassRead{{value: syn}, {err: ebuserrors.ErrTimeout}},
			wantSentinel: ebuserrors.ErrTimeout,
			wantText:     "bus send timeout: " + ebuserrors.ErrTimeout.Error(),
			wantOutcome:  protocol.BusOutcomeTimeout,
			atEvents:     []echoClassEvent{echoClassTX(syn), echoClassRX(syn), echoClassTimeoutEv},
			atCalls:      []string{"write AA", "read_esc", "read_esc"},
			wantCounters: echoClassCounters{round9: 1},
		},

		// First byte after arbitration.
		{
			name: "plain/first_byte_foreign_initiator", flavour: echoClassPlain, data: 0x01, at: 0,
			script:       []echoClassRead{{value: echoClassForeign}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: first-byte arbitration loss: wrote 0x10, wire echo 0x30 (foreign initiator): " + collision,
			wantOutcome:  protocol.BusOutcomeCollision,
			atEvents:     []echoClassEvent{echoClassTX(echoClassSource), echoClassRX(echoClassForeign)},
			atCalls:      []string{"write 10", "read"},
		},
		{
			name: "enh/first_byte_foreign_initiator", flavour: echoClassEnh, data: 0x01, at: 0,
			script:       []echoClassRead{{value: echoClassForeign}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: first-byte arbitration loss: wrote 0xFE, wire echo 0x30 (foreign initiator): " + collision,
			wantOutcome:  protocol.BusOutcomeCollision,
			atEvents:     []echoClassEvent{echoClassTX(protocol.AddressBroadcast), echoClassRX(echoClassForeign)},
			atCalls:      []string{"write FE", "read_esc"},
		},
		{
			name: "enh/first_byte_foreign_initiator_escape_flagged", flavour: echoClassEnh, data: 0x01, at: 0,
			script:       []echoClassRead{{value: echoClassForeign, escaped: true}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0xfe, got 0x30): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents: []echoClassEvent{
				echoClassTX(protocol.AddressBroadcast), echoClassRX(echoClassForeign), echoClassMismatch(echoClassForeign, true),
			},
			atCalls: []string{"write FE", "read_esc"},
		},
		{
			name: "plain/first_byte_target_class", flavour: echoClassPlain, data: 0x01, at: 0,
			script:       []echoClassRead{{value: echoClassTargetCls}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0x10, got 0x15): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents: []echoClassEvent{
				echoClassTX(echoClassSource), echoClassRX(echoClassTargetCls), echoClassMismatch(echoClassTargetCls, false),
			},
			atCalls: []string{"write 10", "read"},
		},
		{
			name: "enh/first_byte_target_class", flavour: echoClassEnh, data: 0x01, at: 0,
			script:       []echoClassRead{{value: echoClassTargetCls}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0xfe, got 0x15): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents: []echoClassEvent{
				echoClassTX(protocol.AddressBroadcast), echoClassRX(echoClassTargetCls), echoClassMismatch(echoClassTargetCls, false),
			},
			atCalls: []string{"write FE", "read_esc"},
		},

		// Mid-telegram mismatch.
		{
			name: "plain/payload_mismatch", flavour: echoClassPlain, data: 0x01, at: 5,
			script:       []echoClassRead{{value: 0x02}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0x01, got 0x02): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents:     []echoClassEvent{echoClassTX(0x01), echoClassRX(0x02), echoClassMismatch(0x02, false)},
			atCalls:      []string{"write 01", "read"},
		},
		{
			name: "enh/payload_mismatch", flavour: echoClassEnh, data: 0x01, at: 4,
			script:       []echoClassRead{{value: 0x02}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0x01, got 0x02): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents:     []echoClassEvent{echoClassTX(0x01), echoClassRX(0x02), echoClassMismatch(0x02, false)},
			atCalls:      []string{"write 01", "read_esc"},
		},
		{
			name: "plain/payload_aa_escape_pair_trailer_mismatch", flavour: echoClassPlain, data: syn, at: 6,
			script:       []echoClassRead{{value: 0x00}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0x01, got 0x00): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents:     []echoClassEvent{echoClassTX(0x01), echoClassRX(0x00), echoClassMismatch(0x00, false)},
			atCalls:      []string{"write 01", "read"},
		},
		{
			name: "plain/end_of_message_syn_mismatch", flavour: echoClassPlain, data: 0x01, at: 7,
			script:       []echoClassRead{{value: 0x01}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0xaa, got 0x01): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents:     []echoClassEvent{echoClassTX(syn), echoClassRX(0x01), echoClassMismatch(0x01, false)},
			atCalls:      []string{"write AA", "read"},
		},
		{
			name: "plain/payload_read_error", flavour: echoClassPlain, data: 0x01, at: 5,
			script:       []echoClassRead{{err: ebuserrors.ErrTimeout}},
			wantSentinel: ebuserrors.ErrTimeout,
			wantText:     "bus send timeout: " + ebuserrors.ErrTimeout.Error(),
			wantOutcome:  protocol.BusOutcomeTimeout,
			atEvents:     []echoClassEvent{echoClassTX(0x01), echoClassTimeoutEv},
			atCalls:      []string{"write 01", "read"},
		},

		// Write error: exactly one echo-mismatch emission, byte unknown.
		{
			name: "plain/write_error", flavour: echoClassPlain, data: 0x01, at: 2,
			writeErr:     writeFailure,
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: " + writeFailure.Error(),
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents:     []echoClassEvent{echoClassUnknownEcho},
			atCalls:      []string{"write B5"},
		},
		{
			name: "enh/write_error", flavour: echoClassEnh, data: 0x01, at: 1,
			writeErr:     writeFailure,
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: " + writeFailure.Error(),
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents:     []echoClassEvent{echoClassUnknownEcho},
			atCalls:      []string{"write B5"},
		},
		// Short write.
		{
			name: "plain/short_write", flavour: echoClassPlain, data: 0x01, at: 2,
			shortWrite:   true,
			wantSentinel: ebuserrors.ErrInvalidPayload,
			wantText:     "bus send failed: " + ebuserrors.ErrInvalidPayload.Error(),
			wantOutcome:  protocol.BusOutcomeUnknown,
			atCalls:      []string{"write B5"},
		},
		{
			name: "enh/short_write", flavour: echoClassEnh, data: 0x01, at: 1,
			shortWrite:   true,
			wantSentinel: ebuserrors.ErrInvalidPayload,
			wantText:     "bus send failed: " + ebuserrors.ErrInvalidPayload.Error(),
			wantOutcome:  protocol.BusOutcomeUnknown,
			atCalls:      []string{"write B5"},
		},

		// Unescaped transport without provenance: no escape-flagged guard.
		{
			name: "unescaped/payload_aa_echo_without_provenance", flavour: echoClassUnescaped, data: syn, at: 5,
			script:   []echoClassRead{{value: syn}},
			atEvents: []echoClassEvent{echoClassTX(syn), echoClassRX(syn)},
			atCalls:  []string{"write AA", "read"},
		},
		{
			name: "unescaped/syn_echo_on_non_syn_payload", flavour: echoClassUnescaped, data: 0x01, at: 5,
			script:       []echoClassRead{{value: syn}},
			wantSentinel: ebuserrors.ErrBusCollision,
			wantText:     "bus send collision: echo mismatch (sent 0x01, got 0xaa): " + collision,
			wantOutcome:  protocol.BusOutcomeEchoMismatch,
			atEvents:     []echoClassEvent{echoClassTX(0x01), echoClassRX(syn), echoClassMismatch(syn, false)},
			atCalls:      []string{"write 01", "read"},
		},
		{
			name: "unescaped/structural_syn_echo_without_provenance", flavour: echoClassUnescaped, data: 0x01, at: 7,
			script:   []echoClassRead{{value: syn}},
			atEvents: []echoClassEvent{echoClassTX(syn), echoClassRX(syn)},
			atCalls:  []string{"write AA", "read"},
		},
	}

	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			echoClassRunRow(t, row)
		})
	}
}

func echoClassRunRow(t *testing.T, row echoClassRow) {
	t.Helper()

	core := &echoClassCore{escapeEcho: row.flavour == echoClassEnh}
	if row.at >= 0 {
		core.hasScripted = true
		core.scriptAt = row.at
		core.script = row.script
		core.writeErr = row.writeErr
		core.shortWrite = row.shortWrite
	}
	var tr transport.RawTransport
	switch row.flavour {
	case echoClassPlain:
		tr = echoClassPlainTransport{core}
	case echoClassEnh:
		tr = echoClassEnhTransport{core}
	case echoClassUnescaped:
		tr = echoClassUnescapedTransport{core}
	default:
		t.Fatalf("unknown flavour %v", row.flavour)
	}

	observer := &echoClassObserver{}
	config := protocol.DefaultBusConfig()
	config.Observer = observer
	config.ReconnectRetries = 0
	bus := protocol.NewBus(tr, config, 4)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)

	before := echoClassReadCounters(bus)

	// A cancel timer rather than a deadline: a deadline would make
	// collision-class errors retry until it expires.
	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()
	guard := time.AfterFunc(5*time.Second, sendCancel)
	defer guard.Stop()

	response, err := bus.Send(sendCtx, echoClassFrame(row.data))
	if response != nil {
		t.Fatalf("broadcast response = %+v, want nil", response)
	}

	if row.wantSentinel == nil {
		if err != nil {
			t.Fatalf("Send error = %v, want nil", err)
		}
	} else {
		if !errors.Is(err, row.wantSentinel) {
			t.Fatalf("Send error = %v, want errors.Is %v", err, row.wantSentinel)
		}
		if err.Error() != row.wantText {
			t.Fatalf("Send error text\n got: %q\nwant: %q", err.Error(), row.wantText)
		}
	}

	writes := echoClassWireWrites(t, row.flavour, row.data)
	at := row.at
	if at < 0 {
		at = len(writes)
	}
	var wantEvents []echoClassEvent
	var wantCalls []string
	if row.flavour == echoClassEnh {
		wantEvents = append(wantEvents, echoClassArbitration)
		wantCalls = append(wantCalls, fmt.Sprintf("arb %02X", echoClassSource))
	}
	prefixEvents, prefixCalls := echoClassCleanSpan(row.flavour, writes, 0, at)
	wantEvents = append(wantEvents, prefixEvents...)
	wantCalls = append(wantCalls, prefixCalls...)
	wantEvents = append(wantEvents, row.atEvents...)
	wantCalls = append(wantCalls, row.atCalls...)
	if row.wantSentinel == nil {
		if row.at >= 0 {
			restEvents, restCalls := echoClassCleanSpan(row.flavour, writes, at+1, len(writes))
			wantEvents = append(wantEvents, restEvents...)
			wantCalls = append(wantCalls, restCalls...)
		}
		wantEvents = append(wantEvents, echoClassAttemptDone, echoClassRequestComplete(protocol.BusOutcomeSuccess))
	} else {
		wantEvents = append(wantEvents, echoClassRequestComplete(row.wantOutcome))
	}

	if got := observer.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("observer events\n got: %+v\nwant: %+v", got, wantEvents)
	}
	if got := core.trace(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("transport calls\n got: %q\nwant: %q", got, wantCalls)
	}

	after := echoClassReadCounters(bus)
	delta := echoClassCounters{
		absorbed:  after.absorbed - before.absorbed,
		recovered: after.recovered - before.recovered,
		exhausted: after.exhausted - before.exhausted,
		round9:    after.round9 - before.round9,
	}
	if delta != row.wantCounters {
		t.Fatalf("counter deltas = %+v, want %+v", delta, row.wantCounters)
	}
}
