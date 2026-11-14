package protocol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// The tests in this file drive each named step of sendRawWithEcho on its own:
// the echo write, the echo read, the SYN-intrusion guards, the payload-0xAA
// AUTO-SYN absorption and the value-mismatch classification. Each row checks
// the returned error, the observer events, the counters and the ordered
// transport-call trace.

// echoStepRead is one scripted read: value, escape flag and error.
type echoStepRead struct {
	value   byte
	escaped bool
	err     error
}

// echoStepTransport records every call and serves scripted reads. It has no
// escape provenance and no structural-write signal.
type echoStepTransport struct {
	calls      []string
	reads      []echoStepRead
	writeN     int
	writeErr   error
	unescaped  bool
	overrideWN bool
}

func (t *echoStepTransport) Write(p []byte) (int, error) {
	t.calls = append(t.calls, fmt.Sprintf("write % X", p))
	if t.writeErr != nil {
		return 0, t.writeErr
	}
	if t.overrideWN {
		return t.writeN, nil
	}
	return len(p), nil
}

func (t *echoStepTransport) next() echoStepRead {
	if len(t.reads) == 0 {
		return echoStepRead{err: ebuserrors.ErrTransportClosed}
	}
	read := t.reads[0]
	t.reads = t.reads[1:]
	return read
}

func (t *echoStepTransport) ReadByte() (byte, error) {
	t.calls = append(t.calls, "read")
	read := t.next()
	return read.value, read.err
}

func (t *echoStepTransport) Close() error            { return nil }
func (t *echoStepTransport) BytesAreUnescaped() bool { return t.unescaped }

// echoStepFlaggedTransport adds escape provenance.
type echoStepFlaggedTransport struct {
	echoStepTransport
}

func (t *echoStepFlaggedTransport) ReadByteWithEscape() (byte, bool, error) {
	t.calls = append(t.calls, "read-esc")
	read := t.next()
	return read.value, read.escaped, read.err
}

// echoStepSignalTransport adds the structural-write signal to a transport
// with escape provenance.
type echoStepSignalTransport struct {
	echoStepFlaggedTransport
}

func (t *echoStepSignalTransport) SignalNextWriteIsStructuralSyn() {
	t.calls = append(t.calls, "signal")
}

var (
	_ transport.RawTransport            = (*echoStepTransport)(nil)
	_ transport.EscapeAware             = (*echoStepTransport)(nil)
	_ transport.EscapeFlaggedReader     = (*echoStepFlaggedTransport)(nil)
	_ transport.StructuralWriteSignaler = (*echoStepSignalTransport)(nil)
)

// echoStepEvent is the part of a BusEvent the echo steps set.
type echoStepEvent struct {
	Kind      BusEventKind
	Outcome   BusOutcomeClass
	Byte      byte
	ByteKnown bool
	Escaped   bool
}

type echoStepRecorder struct {
	events []echoStepEvent
}

func (r *echoStepRecorder) OnBusEvent(event BusEvent) error {
	r.events = append(r.events, echoStepEvent{
		Kind:      event.Kind,
		Outcome:   event.Outcome,
		Byte:      event.Byte,
		ByteKnown: event.ByteKnown,
		Escaped:   event.EchoWasEscaped,
	})
	return nil
}

// echoStepTransportKind selects which optional interfaces the transport has.
type echoStepTransportKind int

const (
	echoStepPlainKind echoStepTransportKind = iota
	echoStepFlaggedKind
	echoStepSignalKind
)

type echoStepFixture struct {
	bus      *Bus
	base     *echoStepTransport
	flagged  transport.EscapeFlaggedReader
	recorder *echoStepRecorder
}

func newEchoStepFixture(kind echoStepTransportKind, unescaped bool, reads []echoStepRead) echoStepFixture {
	recorder := &echoStepRecorder{}
	config := DefaultBusConfig()
	config.Observer = recorder
	var tr transport.RawTransport
	var base *echoStepTransport
	var flagged transport.EscapeFlaggedReader
	switch kind {
	case echoStepPlainKind:
		plain := &echoStepTransport{reads: reads, unescaped: unescaped}
		tr, base = plain, plain
	case echoStepFlaggedKind:
		withFlag := &echoStepFlaggedTransport{echoStepTransport{reads: reads, unescaped: unescaped}}
		tr, base, flagged = withFlag, &withFlag.echoStepTransport, withFlag
	case echoStepSignalKind:
		withSignal := &echoStepSignalTransport{echoStepFlaggedTransport{echoStepTransport{reads: reads, unescaped: unescaped}}}
		tr, base, flagged = withSignal, &withSignal.echoStepTransport, withSignal
	}
	return echoStepFixture{bus: NewBus(tr, config, 1), base: base, flagged: flagged, recorder: recorder}
}

type echoStepCounters struct {
	absorbed, recovered, exhausted, entered uint64
}

func (f echoStepFixture) counters() echoStepCounters {
	return echoStepCounters{
		absorbed:  f.bus.PayloadAaAutoSynAbsorbed(),
		recovered: f.bus.PayloadAaAutoSynRecovered(),
		exhausted: f.bus.PayloadAaAutoSynDrainExhausted(),
		entered:   f.bus.Round9AbsorbEntered(),
	}
}

func echoMismatchEvent(value byte, escaped bool) echoStepEvent {
	return echoStepEvent{Kind: BusEventEchoMismatch, Outcome: BusOutcomeEchoMismatch, Byte: value, ByteKnown: true, Escaped: escaped}
}

func rxEvent(value byte) echoStepEvent {
	return echoStepEvent{Kind: BusEventRX, Outcome: BusOutcomeSuccess, Byte: value}
}

const echoStepCollisionText = "ebus: bus collision during arbitration"

// echoStepCheckError compares the error text and, when non-nil, the
// sentinel, the single-emission marker and the outcome class.
func echoStepCheckError(t *testing.T, err error, wantText string, wantSentinel error, wantMarker bool) {
	t.Helper()
	if wantText == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("error = nil, want %q", wantText)
	}
	if err.Error() != wantText {
		t.Fatalf("error text = %q, want %q", err.Error(), wantText)
	}
	if !errors.Is(err, wantSentinel) {
		t.Fatalf("errors.Is(%v, %v) = false", err, wantSentinel)
	}
	if got := echoMismatchAlreadyEmitted(err); got != wantMarker {
		t.Fatalf("single-emission marker = %v, want %v", got, wantMarker)
	}
}

func echoStepCheckTrace(t *testing.T, f echoStepFixture, wantCalls []string, wantEvents []echoStepEvent) {
	t.Helper()
	if !reflect.DeepEqual(f.base.calls, wantCalls) {
		t.Fatalf("transport calls = %q, want %q", f.base.calls, wantCalls)
	}
	if len(f.recorder.events) == 0 && len(wantEvents) == 0 {
		return
	}
	if !reflect.DeepEqual(f.recorder.events, wantEvents) {
		t.Fatalf("events = %+v, want %+v", f.recorder.events, wantEvents)
	}
}

func TestEchoStepWriteEchoByte(t *testing.T) {
	t.Parallel()

	mismatchErr := fmt.Errorf("adapter echo mismatch: %w", ebuserrors.ErrBusCollision)
	collisionErr := fmt.Errorf("adapter lost arbitration: %w", ebuserrors.ErrBusCollision)
	tx := func(value byte) echoStepEvent {
		return echoStepEvent{Kind: BusEventTX, Outcome: BusOutcomeSuccess, Byte: value}
	}

	tests := []struct {
		name         string
		kind         echoStepTransportKind
		raw          byte
		expectRawSyn bool
		writeErr     error
		shortWrite   bool
		wantCalls    []string
		wantEvents   []echoStepEvent
		wantText     string
		wantSentinel error
		wantMarker   bool
	}{
		{
			name:       "payload byte on a signalling transport is not signalled",
			kind:       echoStepSignalKind,
			raw:        0x42,
			wantCalls:  []string{"write 42"},
			wantEvents: []echoStepEvent{tx(0x42)},
		},
		{
			name:       "payload 0xAA without the structural flag is not signalled",
			kind:       echoStepSignalKind,
			raw:        SymbolSyn,
			wantCalls:  []string{"write AA"},
			wantEvents: []echoStepEvent{tx(SymbolSyn)},
		},
		{
			name:         "structural SYN is signalled before the write",
			kind:         echoStepSignalKind,
			raw:          SymbolSyn,
			expectRawSyn: true,
			wantCalls:    []string{"signal", "write AA"},
			wantEvents:   []echoStepEvent{tx(SymbolSyn)},
		},
		{
			name:         "structural SYN on a transport without the signal",
			kind:         echoStepFlaggedKind,
			raw:          SymbolSyn,
			expectRawSyn: true,
			wantCalls:    []string{"write AA"},
			wantEvents:   []echoStepEvent{tx(SymbolSyn)},
		},
		{
			name:         "write error carrying the echo-mismatch text is emitted once and marked",
			kind:         echoStepPlainKind,
			raw:          0x10,
			writeErr:     mismatchErr,
			wantCalls:    []string{"write 10"},
			wantEvents:   []echoStepEvent{{Kind: BusEventEchoMismatch, Outcome: BusOutcomeEchoMismatch}},
			wantText:     mismatchErr.Error(),
			wantSentinel: ebuserrors.ErrBusCollision,
			wantMarker:   true,
		},
		{
			name:         "write error of the collision class is returned unmarked",
			kind:         echoStepSignalKind,
			raw:          SymbolSyn,
			expectRawSyn: true,
			writeErr:     collisionErr,
			wantCalls:    []string{"signal", "write AA"},
			wantText:     collisionErr.Error(),
			wantSentinel: ebuserrors.ErrBusCollision,
		},
		{
			name:         "write timeout emits a timeout event",
			kind:         echoStepFlaggedKind,
			raw:          0x08,
			writeErr:     ebuserrors.ErrTimeout,
			wantCalls:    []string{"write 08"},
			wantEvents:   []echoStepEvent{{Kind: BusEventTimeout, Outcome: BusOutcomeTimeout}},
			wantText:     ebuserrors.ErrTimeout.Error(),
			wantSentinel: ebuserrors.ErrTimeout,
		},
		{
			name:         "short write is an invalid payload without a TX event",
			kind:         echoStepPlainKind,
			raw:          0x31,
			shortWrite:   true,
			wantCalls:    []string{"write 31"},
			wantText:     ebuserrors.ErrInvalidPayload.Error(),
			wantSentinel: ebuserrors.ErrInvalidPayload,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEchoStepFixture(tc.kind, true, nil)
			f.base.writeErr = tc.writeErr
			f.base.overrideWN = tc.shortWrite
			err := f.bus.writeEchoByte(tc.raw, tc.expectRawSyn)
			echoStepCheckError(t, err, tc.wantText, tc.wantSentinel, tc.wantMarker)
			echoStepCheckTrace(t, f, tc.wantCalls, tc.wantEvents)
		})
	}
}

func TestEchoStepReadEchoByte(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		kind        echoStepTransportKind
		unescaped   bool
		reads       []echoStepRead
		cancel      bool
		wantEcho    byte
		wantEscaped bool
		wantCalls   []string
		wantEvents  []echoStepEvent
		wantText    string
		wantErr     error
	}{
		{
			name:        "unescaped transport with provenance reads the escape flag",
			kind:        echoStepFlaggedKind,
			unescaped:   true,
			reads:       []echoStepRead{{value: SymbolSyn, escaped: true}},
			wantEcho:    SymbolSyn,
			wantEscaped: true,
			wantCalls:   []string{"read-esc"},
			wantEvents:  []echoStepEvent{rxEvent(SymbolSyn)},
		},
		{
			name:       "unescaped transport without provenance reads a plain byte",
			kind:       echoStepPlainKind,
			unescaped:  true,
			reads:      []echoStepRead{{value: 0x52, escaped: true}},
			wantEcho:   0x52,
			wantCalls:  []string{"read"},
			wantEvents: []echoStepEvent{rxEvent(0x52)},
		},
		{
			name:       "plain transport ignores provenance it offers",
			kind:       echoStepFlaggedKind,
			unescaped:  false,
			reads:      []echoStepRead{{value: 0x07, escaped: true}},
			wantEcho:   0x07,
			wantCalls:  []string{"read"},
			wantEvents: []echoStepEvent{rxEvent(0x07)},
		},
		{
			name:       "read timeout emits a timeout event",
			kind:       echoStepFlaggedKind,
			unescaped:  true,
			reads:      []echoStepRead{{err: ebuserrors.ErrTimeout}},
			wantCalls:  []string{"read-esc"},
			wantEvents: []echoStepEvent{{Kind: BusEventTimeout, Outcome: BusOutcomeTimeout}},
			wantText:   ebuserrors.ErrTimeout.Error(),
			wantErr:    ebuserrors.ErrTimeout,
		},
		{
			name:      "cancelled request context reads nothing",
			kind:      echoStepPlainKind,
			reads:     []echoStepRead{{value: 0x07}},
			cancel:    true,
			wantCalls: nil,
			wantText:  context.Canceled.Error(),
			wantErr:   context.Canceled,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEchoStepFixture(tc.kind, tc.unescaped, tc.reads)
			reqCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			flagged, _ := f.bus.transport.(transport.EscapeFlaggedReader)
			echo, escaped, err := f.bus.readEchoByte(context.Background(), reqCtx, flagged)
			echoStepCheckError(t, err, tc.wantText, tc.wantErr, false)
			if err == nil && (echo != tc.wantEcho || escaped != tc.wantEscaped) {
				t.Fatalf("echo = 0x%02X escaped=%v, want 0x%02X escaped=%v", echo, escaped, tc.wantEcho, tc.wantEscaped)
			}
			echoStepCheckTrace(t, f, tc.wantCalls, tc.wantEvents)
		})
	}
}

func TestEchoStepRejectEchoSynIntrusion(t *testing.T) {
	t.Parallel()

	unexpectedSyn := "unexpected syn while waiting for echo: " + echoStepCollisionText
	escapeDecoded := "echo expected real wire SYN (0xAA) but received escape-decoded payload 0xAA: " + echoStepCollisionText

	tests := []struct {
		name         string
		kind         echoStepTransportKind
		unescaped    bool
		raw          byte
		echo         byte
		escaped      bool
		expectRawSyn bool
		wantText     string
		wantEvents   []echoStepEvent
	}{
		{
			name:       "plain transport: SYN in place of a non-SYN echo",
			kind:       echoStepPlainKind,
			raw:        0x15,
			echo:       SymbolSyn,
			wantText:   unexpectedSyn,
			wantEvents: []echoStepEvent{echoMismatchEvent(SymbolSyn, false)},
		},
		{
			name:       "plain transport with provenance: the plain guard fires first and alone",
			kind:       echoStepFlaggedKind,
			raw:        0x15,
			echo:       SymbolSyn,
			wantText:   unexpectedSyn,
			wantEvents: []echoStepEvent{echoMismatchEvent(SymbolSyn, false)},
		},
		{
			name: "plain transport: SYN echo of a SYN write passes",
			kind: echoStepPlainKind,
			raw:  SymbolSyn,
			echo: SymbolSyn,
		},
		{
			name: "plain transport: non-SYN echo passes to value classification",
			kind: echoStepPlainKind,
			raw:  0x15,
			echo: 0x16,
		},
		{
			name:       "unescaped transport with provenance: wire SYN in place of a non-SYN echo",
			kind:       echoStepFlaggedKind,
			unescaped:  true,
			raw:        0x52,
			echo:       SymbolSyn,
			wantText:   unexpectedSyn,
			wantEvents: []echoStepEvent{echoMismatchEvent(SymbolSyn, false)},
		},
		{
			name:      "unescaped transport with provenance: escape-decoded 0xAA is not a wire SYN",
			kind:      echoStepFlaggedKind,
			unescaped: true,
			raw:       0x52,
			echo:      SymbolSyn,
			escaped:   true,
		},
		{
			name:      "unescaped transport without provenance skips the provenance guards",
			kind:      echoStepPlainKind,
			unescaped: true,
			raw:       0x52,
			echo:      SymbolSyn,
		},
		{
			name:         "structural SYN answered by an escape-decoded 0xAA",
			kind:         echoStepSignalKind,
			unescaped:    true,
			raw:          SymbolSyn,
			echo:         SymbolSyn,
			escaped:      true,
			expectRawSyn: true,
			wantText:     escapeDecoded,
			wantEvents:   []echoStepEvent{echoMismatchEvent(SymbolSyn, true)},
		},
		{
			name:         "structural SYN answered by a wire SYN passes",
			kind:         echoStepSignalKind,
			unescaped:    true,
			raw:          SymbolSyn,
			echo:         SymbolSyn,
			expectRawSyn: true,
		},
		{
			name:      "payload 0xAA answered by an escape-decoded 0xAA passes",
			kind:      echoStepFlaggedKind,
			unescaped: true,
			raw:       SymbolSyn,
			echo:      SymbolSyn,
			escaped:   true,
		},
		{
			name:      "payload 0xAA answered by a wire SYN is left to the AUTO-SYN absorption",
			kind:      echoStepFlaggedKind,
			unescaped: true,
			raw:       SymbolSyn,
			echo:      SymbolSyn,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEchoStepFixture(tc.kind, tc.unescaped, nil)
			err := f.bus.rejectEchoSynIntrusion(tc.raw, tc.echo, tc.escaped, f.flagged, tc.expectRawSyn)
			echoStepCheckError(t, err, tc.wantText, ebuserrors.ErrBusCollision, false)
			if err != nil && busOutcomeFromError(err) != BusOutcomeCollision {
				t.Fatalf("outcome = %v, want collision", busOutcomeFromError(err))
			}
			echoStepCheckTrace(t, f, nil, tc.wantEvents)
		})
	}
}

func TestEchoStepAbsorbPayloadAaAutoSyn(t *testing.T) {
	t.Parallel()

	wireSyn := echoStepRead{value: SymbolSyn}
	payloadEcho := echoStepRead{value: SymbolSyn, escaped: true}
	exhaustedText := "echo for payload 0xAA expected escape-decoded WasEscaped=true but received real wire SYN: " + echoStepCollisionText

	tests := []struct {
		name         string
		reads        []echoStepRead
		inEchoWait   bool
		wantDone     bool
		wantEcho     byte
		wantEscaped  bool
		wantText     string
		wantSentinel error
		wantCalls    []string
		wantEvents   []echoStepEvent
		wantCounters echoStepCounters
	}{
		{
			name:         "recovered after one drained SYN",
			reads:        []echoStepRead{payloadEcho},
			inEchoWait:   true,
			wantDone:     true,
			wantCalls:    []string{"read-esc"},
			wantEvents:   []echoStepEvent{rxEvent(SymbolSyn)},
			wantCounters: echoStepCounters{absorbed: 1, recovered: 1, entered: 1},
		},
		{
			name:         "recovered after three drained SYNs",
			reads:        []echoStepRead{wireSyn, wireSyn, payloadEcho},
			inEchoWait:   true,
			wantDone:     true,
			wantCalls:    []string{"read-esc", "read-esc", "read-esc"},
			wantEvents:   []echoStepEvent{rxEvent(SymbolSyn), rxEvent(SymbolSyn), rxEvent(SymbolSyn)},
			wantCounters: echoStepCounters{absorbed: 3, recovered: 1, entered: 1},
		},
		{
			name:         "drain cap reached",
			reads:        []echoStepRead{wireSyn, wireSyn, wireSyn, payloadEcho},
			inEchoWait:   true,
			wantDone:     true,
			wantText:     exhaustedText,
			wantSentinel: ebuserrors.ErrBusCollision,
			wantCalls:    []string{"read-esc", "read-esc", "read-esc"},
			wantEvents: []echoStepEvent{
				rxEvent(SymbolSyn), rxEvent(SymbolSyn), rxEvent(SymbolSyn),
				echoMismatchEvent(SymbolSyn, false),
			},
			wantCounters: echoStepCounters{absorbed: 3, exhausted: 1, entered: 1},
		},
		{
			name:         "non-SYN drained byte is handed to value classification",
			reads:        []echoStepRead{wireSyn, {value: 0x42, escaped: true}},
			inEchoWait:   true,
			wantEcho:     0x42,
			wantEscaped:  true,
			wantCalls:    []string{"read-esc", "read-esc"},
			wantEvents:   []echoStepEvent{rxEvent(SymbolSyn), rxEvent(0x42)},
			wantCounters: echoStepCounters{absorbed: 2, entered: 1},
		},
		{
			name:         "read error while draining is returned as is",
			reads:        []echoStepRead{wireSyn, {err: ebuserrors.ErrTimeout}},
			inEchoWait:   true,
			wantDone:     true,
			wantText:     ebuserrors.ErrTimeout.Error(),
			wantSentinel: ebuserrors.ErrTimeout,
			wantCalls:    []string{"read-esc", "read-esc"},
			wantEvents: []echoStepEvent{
				rxEvent(SymbolSyn),
				{Kind: BusEventTimeout, Outcome: BusOutcomeTimeout},
			},
			wantCounters: echoStepCounters{absorbed: 1, entered: 1},
		},
		{
			name:         "outside the echo-wait phase the entry counter stays put",
			reads:        []echoStepRead{payloadEcho},
			wantDone:     true,
			wantCalls:    []string{"read-esc"},
			wantEvents:   []echoStepEvent{rxEvent(SymbolSyn)},
			wantCounters: echoStepCounters{absorbed: 1, recovered: 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEchoStepFixture(echoStepFlaggedKind, true, tc.reads)
			if tc.inEchoWait {
				f.bus.activeEchoWaits.Add(1)
			}
			echo, escaped, done, err := f.bus.absorbPayloadAaAutoSyn(context.Background(), context.Background(), f.flagged, SymbolSyn, false)
			if done != tc.wantDone {
				t.Fatalf("done = %v, want %v", done, tc.wantDone)
			}
			echoStepCheckError(t, err, tc.wantText, tc.wantSentinel, false)
			if !done && (echo != tc.wantEcho || escaped != tc.wantEscaped) {
				t.Fatalf("echo = 0x%02X escaped=%v, want 0x%02X escaped=%v", echo, escaped, tc.wantEcho, tc.wantEscaped)
			}
			if got := f.counters(); got != tc.wantCounters {
				t.Fatalf("counters = %+v, want %+v", got, tc.wantCounters)
			}
			echoStepCheckTrace(t, f, tc.wantCalls, tc.wantEvents)
		})
	}
}

func TestEchoStepClassifyEchoValue(t *testing.T) {
	t.Parallel()

	const initiatorByte, targetByte = 0x10, 0x15
	if AddressClassOf(initiatorByte) != AddressClassMaster {
		t.Fatalf("fixture byte 0x%02X is not initiator-class", initiatorByte)
	}
	if AddressClassOf(targetByte) == AddressClassMaster {
		t.Fatalf("fixture byte 0x%02X is initiator-class", targetByte)
	}
	mismatch := func(raw, echo byte) string {
		return fmt.Sprintf("echo mismatch (sent 0x%02x, got 0x%02x): %s", raw, echo, echoStepCollisionText)
	}

	tests := []struct {
		name        string
		raw         byte
		echo        byte
		escaped     bool
		firstByte   bool
		wantText    string
		wantMarker  bool
		wantOutcome BusOutcomeClass
		wantEvents  []echoStepEvent
	}{
		{
			name:      "matching echo of the first byte",
			raw:       0x08,
			echo:      0x08,
			firstByte: true,
		},
		{
			name:    "matching escape-decoded echo mid-telegram",
			raw:     SymbolSyn,
			echo:    SymbolSyn,
			escaped: true,
		},
		{
			name:        "first byte answered by a foreign initiator-class byte",
			raw:         0x08,
			echo:        initiatorByte,
			firstByte:   true,
			wantText:    "first-byte arbitration loss: wrote 0x08, wire echo 0x10 (foreign initiator): " + echoStepCollisionText,
			wantOutcome: BusOutcomeCollision,
		},
		{
			name:        "first byte answered by an escape-decoded initiator-class byte",
			raw:         0x08,
			echo:        initiatorByte,
			escaped:     true,
			firstByte:   true,
			wantText:    mismatch(0x08, initiatorByte),
			wantMarker:  true,
			wantOutcome: BusOutcomeEchoMismatch,
			wantEvents:  []echoStepEvent{echoMismatchEvent(initiatorByte, true)},
		},
		{
			name:        "first byte answered by a non-initiator-class byte",
			raw:         0x08,
			echo:        targetByte,
			firstByte:   true,
			wantText:    mismatch(0x08, targetByte),
			wantMarker:  true,
			wantOutcome: BusOutcomeEchoMismatch,
			wantEvents:  []echoStepEvent{echoMismatchEvent(targetByte, false)},
		},
		{
			name:        "mid-telegram byte answered by an initiator-class byte",
			raw:         0x52,
			echo:        initiatorByte,
			wantText:    mismatch(0x52, initiatorByte),
			wantMarker:  true,
			wantOutcome: BusOutcomeEchoMismatch,
			wantEvents:  []echoStepEvent{echoMismatchEvent(initiatorByte, false)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEchoStepFixture(echoStepFlaggedKind, true, nil)
			err := f.bus.classifyEchoValue(tc.raw, tc.echo, tc.escaped, tc.firstByte)
			echoStepCheckError(t, err, tc.wantText, ebuserrors.ErrBusCollision, tc.wantMarker)
			if err != nil && busOutcomeFromError(err) != tc.wantOutcome {
				t.Fatalf("outcome = %v, want %v", busOutcomeFromError(err), tc.wantOutcome)
			}
			echoStepCheckTrace(t, f, nil, tc.wantEvents)
		})
	}
}
