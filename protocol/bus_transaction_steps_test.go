package protocol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// The tests in this file drive each named step of sendTransaction and of the
// retry path on its own: the telegram build, the source-inclusion decision,
// the command phase, the response phase, the collision and adapter-reset
// backoffs, the retry budgets and the decisions after a failed arbitration
// or transaction. Each row checks the returned values and one ordered trace
// that interleaves the transport calls with the observer events.

// txStepRead is one scripted reply: value or error.
type txStepRead struct {
	value byte
	err   error
}

// txStepLog is the ordered trace shared by the transport and the observer.
type txStepLog struct {
	entries []string
}

func (l *txStepLog) add(format string, args ...any) {
	l.entries = append(l.entries, fmt.Sprintf(format, args...))
}

// txStepTransport echoes every write and serves the scripted replies once
// the echoes are read. Only writes and replies enter the trace. It has no
// escape provenance, no arbitration and no collision-recovery lifecycle.
type txStepTransport struct {
	log       *txStepLog
	echoes    []byte
	replies   []txStepRead
	unescaped bool
}

func (t *txStepTransport) Write(p []byte) (int, error) {
	t.log.add("write % X", p)
	t.echoes = append(t.echoes, p...)
	return len(p), nil
}

func (t *txStepTransport) ReadByte() (byte, error) {
	if len(t.echoes) > 0 {
		value := t.echoes[0]
		t.echoes = t.echoes[1:]
		return value, nil
	}
	if len(t.replies) == 0 {
		t.log.add("read closed")
		return 0, ebuserrors.ErrTransportClosed
	}
	reply := t.replies[0]
	t.replies = t.replies[1:]
	if reply.err != nil {
		t.log.add("read error")
		return 0, reply.err
	}
	t.log.add("read %02X", reply.value)
	return reply.value, nil
}

func (t *txStepTransport) Close() error            { return nil }
func (t *txStepTransport) BytesAreUnescaped() bool { return t.unescaped }

// txStepArbitrationTransport arbitrates; arbitration places the source byte.
type txStepArbitrationTransport struct {
	txStepTransport
}

func (t *txStepArbitrationTransport) StartArbitration(initiator byte) error {
	t.log.add("arbitrate %02X", initiator)
	return nil
}

// txStepSourceTransport arbitrates and says whether arbitration places the
// source byte.
type txStepSourceTransport struct {
	txStepArbitrationTransport
	sendsSource bool
}

func (t *txStepSourceTransport) ArbitrationSendsSource() bool { return t.sendsSource }

// txStepRecoveryTransport exposes the collision-recovery lifecycle and
// reconnects.
type txStepRecoveryTransport struct {
	txStepTransport
	token uint64
}

func (t *txStepRecoveryTransport) CollisionRecoveryToken() uint64 {
	t.log.add("token %d", t.token)
	return t.token
}

func (t *txStepRecoveryTransport) CompleteCollisionRecovery(token uint64, decision transport.CollisionRecoveryDecision) {
	name := "unknown"
	switch decision {
	case transport.CollisionRecoveryRetry:
		name = "retry"
	case transport.CollisionRecoveryAbandon:
		name = "abandon"
	}
	t.log.add("complete %d %s", token, name)
}

func (t *txStepRecoveryTransport) Reconnect() error {
	t.log.add("reconnect")
	return nil
}

var (
	_ transport.RawTransport               = (*txStepTransport)(nil)
	_ transport.EscapeAware                = (*txStepTransport)(nil)
	_ arbitrationTransport                 = (*txStepArbitrationTransport)(nil)
	_ arbitrationSourceBehavior            = (*txStepSourceTransport)(nil)
	_ transport.CollisionRecoveryLifecycle = (*txStepRecoveryTransport)(nil)
	_ transport.Reconnectable              = (*txStepRecoveryTransport)(nil)
)

// txStepObserver adds every event except the per-byte TX and RX events to
// the trace.
type txStepObserver struct {
	log *txStepLog
}

var txStepKindNames = map[BusEventKind]string{
	BusEventArbitration:     "Arbitration",
	BusEventACK:             "ACK",
	BusEventNACK:            "NACK",
	BusEventTimeout:         "Timeout",
	BusEventCRCMismatch:     "CRCMismatch",
	BusEventEchoMismatch:    "EchoMismatch",
	BusEventRetry:           "Retry",
	BusEventAttemptComplete: "AttemptComplete",
	BusEventRequestComplete: "RequestComplete",
	BusEventObserverFault:   "ObserverFault",
	BusEventAdapterReset:    "AdapterReset",
}

var txStepOutcomeNames = map[BusOutcomeClass]string{
	BusOutcomeUnknown:       "Unknown",
	BusOutcomeSuccess:       "Success",
	BusOutcomeTimeout:       "Timeout",
	BusOutcomeNACK:          "NACK",
	BusOutcomeCRCMismatch:   "CRCMismatch",
	BusOutcomeEchoMismatch:  "EchoMismatch",
	BusOutcomeCollision:     "Collision",
	BusOutcomeObserverFault: "ObserverFault",
	BusOutcomeAdapterReset:  "AdapterReset",
}

func (o *txStepObserver) OnBusEvent(event BusEvent) error {
	if event.Kind == BusEventTX || event.Kind == BusEventRX {
		return nil
	}
	entry := fmt.Sprintf("event %s %s", txStepKindNames[event.Kind], txStepOutcomeNames[event.Outcome])
	if event.Kind == BusEventRetry || event.Kind == BusEventRequestComplete {
		entry += fmt.Sprintf(" attempt=%d timeout=%d nack=%d", event.Attempt, event.TimeoutRetries, event.NACKRetries)
	}
	if event.HasResponse {
		entry += fmt.Sprintf(" response=% X", event.Response.Data)
	}
	o.log.add("%s", entry)
	return nil
}

func newTxStepBus(tr transport.RawTransport, log *txStepLog, config BusConfig) *Bus {
	config.Observer = &txStepObserver{log: log}
	return NewBus(tr, config, 1)
}

// txStepWrites returns the trace of writing the given logical symbols one by
// one, with the wire escape of a plain transport when escaped is set.
func txStepWrites(escaped bool, symbols ...byte) []string {
	var out []string
	for _, symbol := range symbols {
		switch {
		case escaped && symbol == SymbolEscape:
			out = append(out, "write A9", "write 00")
		case escaped && symbol == SymbolSyn:
			out = append(out, "write A9", "write 01")
		default:
			out = append(out, fmt.Sprintf("write %02X", symbol))
		}
	}
	return out
}

func txStepTrace(parts ...any) []string {
	var out []string
	for _, part := range parts {
		switch value := part.(type) {
		case string:
			out = append(out, value)
		case []string:
			out = append(out, value...)
		}
	}
	return out
}

func txStepCheckError(t *testing.T, err error, wantText string, wantSentinel error) {
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
	if wantSentinel != nil && !errors.Is(err, wantSentinel) {
		t.Fatalf("errors.Is(%v, %v) = false", err, wantSentinel)
	}
}

func txStepCheckTrace(t *testing.T, log *txStepLog, want []string) {
	t.Helper()
	if !reflect.DeepEqual(log.entries, want) {
		t.Fatalf("trace mismatch\n got: %q\nwant: %q", log.entries, want)
	}
}

func newTxStepAttempt(frame Frame) *transactionAttempt {
	return &transactionAttempt{frame: frame, frameType: frame.Type(), attempt: 3, startedAt: time.Now(), decoder: &busDecoder{}}
}

// TestTransactionStepBuildInitiatorTelegram varies every field of the frame
// and checks its position in the telegram; the CRC covers every byte before
// it.
func TestTransactionStepBuildInitiatorTelegram(t *testing.T) {
	base := Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x2A}}
	rows := []struct {
		name  string
		frame Frame
		head  []byte
	}{
		{"base", base, []byte{0x10, 0x08, 0xB5, 0x09, 0x01, 0x2A}},
		{"source", Frame{Source: 0x31, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x2A}}, []byte{0x31, 0x08, 0xB5, 0x09, 0x01, 0x2A}},
		{"target", Frame{Source: 0x10, Target: 0x15, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x2A}}, []byte{0x10, 0x15, 0xB5, 0x09, 0x01, 0x2A}},
		{"primary", Frame{Source: 0x10, Target: 0x08, Primary: 0x07, Secondary: 0x09, Data: []byte{0x2A}}, []byte{0x10, 0x08, 0x07, 0x09, 0x01, 0x2A}},
		{"secondary", Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x04, Data: []byte{0x2A}}, []byte{0x10, 0x08, 0xB5, 0x04, 0x01, 0x2A}},
		{"no data", Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09}, []byte{0x10, 0x08, 0xB5, 0x09, 0x00}},
		{"escape-valued data stays logical", Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0xA9, 0xAA, 0x01}}, []byte{0x10, 0x08, 0xB5, 0x09, 0x03, 0xA9, 0xAA, 0x01}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := buildInitiatorTelegram(row.frame)
			want := append(append([]byte{}, row.head...), CRC(row.head))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("telegram = % X, want % X", got, want)
			}
			if cap(got) != 6+len(row.frame.Data) {
				t.Fatalf("capacity = %d, want %d", cap(got), 6+len(row.frame.Data))
			}
		})
	}
}

func TestTransactionStepCommandIncludesSource(t *testing.T) {
	log := &txStepLog{}
	rows := []struct {
		name string
		tr   transport.RawTransport
		want bool
	}{
		{"no arbitration", &txStepTransport{log: log}, true},
		{"arbitration places the source", &txStepArbitrationTransport{txStepTransport{log: log}}, false},
		{"arbitration says it places the source", &txStepSourceTransport{txStepArbitrationTransport{txStepTransport{log: log}}, true}, false},
		{"arbitration says it does not place the source", &txStepSourceTransport{txStepArbitrationTransport{txStepTransport{log: log}}, false}, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			bus := NewBus(row.tr, DefaultBusConfig(), 1)
			if got := bus.commandIncludesSource(); got != row.want {
				t.Fatalf("commandIncludesSource = %v, want %v", got, row.want)
			}
		})
	}
	if len(log.entries) != 0 {
		t.Fatalf("the decision called the transport: %q", log.entries)
	}
}

func TestTransactionStepSendCommandUntilAck(t *testing.T) {
	targetFrame := Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x2A}}
	broadcastFrame := Frame{Source: 0x10, Target: AddressBroadcast, Primary: 0xB5, Secondary: 0x16, Data: []byte{0x01}}
	tel := buildInitiatorTelegram(targetFrame)
	btel := buildInitiatorTelegram(broadcastFrame)
	nackText := "nack received: " + ebuserrors.ErrNACK.Error()
	timeoutSuffix := ": " + ebuserrors.ErrTimeout.Error()

	rows := []struct {
		name          string
		frame         Frame
		unescaped     bool
		includeSource bool
		replies       []txStepRead
		wantBroadcast bool
		wantText      string
		wantSentinel  error
		wantTrace     []string
	}{
		{
			name: "ack on the first send", frame: targetFrame, includeSource: true,
			replies:   []txStepRead{{value: SymbolAck}},
			wantTrace: txStepTrace(txStepWrites(true, tel...), "read 00", "event ACK Success"),
		},
		{
			name: "nack then ack sends the source on the retransmission", frame: targetFrame,
			replies: []txStepRead{{value: SymbolNack}, {value: SymbolAck}},
			wantTrace: txStepTrace(txStepWrites(true, tel[1:]...), "read FF", "event NACK NACK",
				txStepWrites(true, tel...), "read 00", "event ACK Success"),
		},
		{
			name: "nack twice ends the message", frame: targetFrame,
			replies:  []txStepRead{{value: SymbolNack}, {value: SymbolNack}},
			wantText: nackText, wantSentinel: ebuserrors.ErrNACK,
			wantTrace: txStepTrace(txStepWrites(true, tel[1:]...), "read FF", "event NACK NACK",
				txStepWrites(true, tel...), "read FF", "event NACK NACK", "write AA", "event NACK NACK"),
		},
		{
			name: "syn at the ack wait on a plain transport", frame: targetFrame, includeSource: true,
			replies:  []txStepRead{{value: SymbolSyn}},
			wantText: "syn while waiting for command ack" + timeoutSuffix, wantSentinel: ebuserrors.ErrTimeout,
			wantTrace: txStepTrace(txStepWrites(true, tel...), "read AA", "event Timeout Timeout"),
		},
		{
			name: "0xAA at the ack wait on an unescaped transport", frame: targetFrame, unescaped: true, includeSource: true,
			replies:  []txStepRead{{value: SymbolSyn}},
			wantText: "unexpected symbol 0xaa while waiting for command ack" + timeoutSuffix, wantSentinel: ebuserrors.ErrTimeout,
			wantTrace: txStepTrace(txStepWrites(false, tel...), "read AA", "event Timeout Timeout"),
		},
		{
			name: "another symbol at the ack wait", frame: targetFrame, includeSource: true,
			replies:  []txStepRead{{value: 0x42}},
			wantText: "unexpected symbol 0x42 while waiting for command ack" + timeoutSuffix, wantSentinel: ebuserrors.ErrTimeout,
			wantTrace: txStepTrace(txStepWrites(true, tel...), "read 42", "event Timeout Timeout"),
		},
		{
			name: "read error at the ack wait", frame: targetFrame, includeSource: true,
			replies:  []txStepRead{{err: ebuserrors.ErrAdapterReset}},
			wantText: ebuserrors.ErrAdapterReset.Error(), wantSentinel: ebuserrors.ErrAdapterReset,
			wantTrace: txStepTrace(txStepWrites(true, tel...), "read error", "event AdapterReset AdapterReset"),
		},
		{
			name: "broadcast ends the message without an ack", frame: broadcastFrame, includeSource: true,
			wantBroadcast: true,
			wantTrace:     txStepTrace(txStepWrites(true, btel...), "write AA", "event AttemptComplete Success"),
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			log := &txStepLog{}
			tr := &txStepTransport{log: log, replies: row.replies, unescaped: row.unescaped}
			bus := newTxStepBus(tr, log, DefaultBusConfig())
			tx := newTxStepAttempt(row.frame)
			telegram := buildInitiatorTelegram(row.frame)
			broadcastDone, err := bus.sendCommandUntilAck(context.Background(), context.Background(), tx, telegram, row.includeSource)
			txStepCheckError(t, err, row.wantText, row.wantSentinel)
			if broadcastDone != row.wantBroadcast {
				t.Fatalf("broadcastDone = %v, want %v", broadcastDone, row.wantBroadcast)
			}
			txStepCheckTrace(t, log, row.wantTrace)
		})
	}
}

func TestTransactionStepHandleCommandAck(t *testing.T) {
	frame := Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09}
	rows := []struct {
		name           string
		ack            byte
		commandAttempt int
		wantAcked      bool
		wantText       string
		wantTrace      []string
	}{
		{"ack on the first send", SymbolAck, 0, true, "", []string{"event ACK Success"}},
		{"ack on the retransmission", SymbolAck, 1, true, "", []string{"event ACK Success"}},
		{"nack on the first send asks for a retransmission", SymbolNack, 0, false, "", []string{"event NACK NACK"}},
		{"nack on the retransmission", SymbolNack, 1, false, "nack received: " + ebuserrors.ErrNACK.Error(), []string{"event NACK NACK", "write AA", "event NACK NACK"}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			log := &txStepLog{}
			bus := newTxStepBus(&txStepTransport{log: log}, log, DefaultBusConfig())
			acked, err := bus.handleCommandAck(context.Background(), context.Background(), newTxStepAttempt(frame), row.ack, row.commandAttempt)
			txStepCheckError(t, err, row.wantText, nil)
			if acked != row.wantAcked {
				t.Fatalf("acked = %v, want %v", acked, row.wantAcked)
			}
			txStepCheckTrace(t, log, row.wantTrace)
		})
	}
}

func TestTransactionStepReadResponse(t *testing.T) {
	frame := Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09}
	timeoutSuffix := ": " + ebuserrors.ErrTimeout.Error()
	rows := []struct {
		name      string
		unescaped bool
		replies   []txStepRead
		wantLen   byte
		wantData  []byte
		wantCRC   byte
		wantText  string
		wantTrace []string
	}{
		{
			name:    "length, data and crc",
			replies: []txStepRead{{value: 0x02}, {value: 0x11}, {value: 0x22}, {value: 0x5C}},
			wantLen: 0x02, wantData: []byte{0x11, 0x22}, wantCRC: 0x5C,
			wantTrace: []string{"read 02", "read 11", "read 22", "read 5C"},
		},
		{
			name:    "escape pair decodes to 0xA9 on a plain transport",
			replies: []txStepRead{{value: 0x01}, {value: SymbolEscape}, {value: 0x00}, {value: 0x07}},
			wantLen: 0x01, wantData: []byte{SymbolEscape}, wantCRC: 0x07,
			wantTrace: []string{"read 01", "read A9", "read 00", "read 07"},
		},
		{
			name:      "escape pair for 0xAA in the data counts as syn on a plain transport",
			replies:   []txStepRead{{value: 0x01}, {value: SymbolEscape}, {value: 0x01}},
			wantText:  "syn while reading response data" + timeoutSuffix,
			wantTrace: []string{"read 01", "read A9", "read 01", "event Timeout Timeout"},
		},
		{
			name:      "syn as the length on a plain transport",
			replies:   []txStepRead{{value: SymbolSyn}},
			wantText:  "syn while waiting for response length" + timeoutSuffix,
			wantTrace: []string{"read AA", "event Timeout Timeout"},
		},
		{
			name:      "syn in the data on a plain transport",
			replies:   []txStepRead{{value: 0x02}, {value: SymbolSyn}},
			wantText:  "syn while reading response data" + timeoutSuffix,
			wantTrace: []string{"read 02", "read AA", "event Timeout Timeout"},
		},
		{
			name:      "syn as the crc on a plain transport",
			replies:   []txStepRead{{value: 0x01}, {value: 0x11}, {value: SymbolSyn}},
			wantText:  "syn while waiting for response crc" + timeoutSuffix,
			wantTrace: []string{"read 01", "read 11", "read AA", "event Timeout Timeout"},
		},
		{
			name: "0xAA in the data and as the crc on an unescaped transport", unescaped: true,
			replies: []txStepRead{{value: 0x01}, {value: SymbolSyn}, {value: SymbolSyn}},
			wantLen: 0x01, wantData: []byte{SymbolSyn}, wantCRC: SymbolSyn,
			wantTrace: []string{"read 01", "read AA", "read AA"},
		},
		{
			name:      "read error in the data",
			replies:   []txStepRead{{value: 0x02}, {value: 0x11}, {err: ebuserrors.ErrTimeout}},
			wantText:  ebuserrors.ErrTimeout.Error(),
			wantTrace: []string{"read 02", "read 11", "read error", "event Timeout Timeout"},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			log := &txStepLog{}
			tr := &txStepTransport{log: log, replies: row.replies, unescaped: row.unescaped}
			bus := newTxStepBus(tr, log, DefaultBusConfig())
			length, data, crc, err := bus.readResponse(context.Background(), context.Background(), newTxStepAttempt(frame))
			txStepCheckError(t, err, row.wantText, nil)
			if err == nil && (length != row.wantLen || !reflect.DeepEqual(data, row.wantData) || crc != row.wantCRC) {
				t.Fatalf("response = %02X % X %02X, want %02X % X %02X", length, data, crc, row.wantLen, row.wantData, row.wantCRC)
			}
			txStepCheckTrace(t, log, row.wantTrace)
		})
	}
}

func TestTransactionStepCollectResponse(t *testing.T) {
	frame := Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09}
	good := []byte{0x02, 0x11, 0x22}
	goodCRC := CRC(good)
	badCRC := goodCRC ^ 0x01
	if badCRC == SymbolSyn || badCRC == SymbolEscape || goodCRC == SymbolSyn || goodCRC == SymbolEscape {
		t.Fatalf("fixture CRC %02X needs an escape; pick another response", goodCRC)
	}
	reply := func(crc byte) []txStepRead {
		return []txStepRead{{value: good[0]}, {value: good[1]}, {value: good[2]}, {value: crc}}
	}
	reads := func(crc byte) []string {
		return []string{"read 02", "read 11", "read 22", fmt.Sprintf("read %02X", crc)}
	}
	accepted := []string{"write 00", "event ACK Success response=11 22", "write AA", "event AttemptComplete Success response=11 22"}
	rejected := []string{"write FF", "event NACK CRCMismatch response=11 22"}
	rows := []struct {
		name      string
		replies   []txStepRead
		wantText  string
		wantTrace []string
	}{
		{
			name:      "crc matches",
			replies:   reply(goodCRC),
			wantTrace: txStepTrace(reads(goodCRC), accepted),
		},
		{
			name:    "crc mismatch then match",
			replies: append(reply(badCRC), reply(goodCRC)...),
			wantTrace: txStepTrace(reads(badCRC), rejected, "event CRCMismatch CRCMismatch response=11 22",
				reads(goodCRC), accepted),
		},
		{
			name:     "crc mismatch twice",
			replies:  append(reply(badCRC), reply(badCRC)...),
			wantText: "crc mismatch: " + ebuserrors.ErrCRCMismatch.Error(),
			wantTrace: txStepTrace(reads(badCRC), rejected, "event CRCMismatch CRCMismatch response=11 22",
				reads(badCRC), rejected, "write AA", "event CRCMismatch CRCMismatch"),
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			log := &txStepLog{}
			tr := &txStepTransport{log: log, replies: row.replies}
			bus := newTxStepBus(tr, log, DefaultBusConfig())
			response, err := bus.collectResponse(context.Background(), context.Background(), newTxStepAttempt(frame))
			txStepCheckError(t, err, row.wantText, nil)
			if err == nil {
				want := &Frame{Source: 0x08, Target: 0x10, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x11, 0x22}}
				if !reflect.DeepEqual(response, want) {
					t.Fatalf("response = %+v, want %+v", response, want)
				}
			}
			txStepCheckTrace(t, log, row.wantTrace)
		})
	}
}

func TestTransactionStepSendTransactionRejectsBeforeWriting(t *testing.T) {
	rows := []struct {
		name     string
		frame    Frame
		wantText string
	}{
		{"unknown frame type", Frame{Source: 0x10, Target: SymbolSyn, Primary: 0xB5, Secondary: 0x09}, errUnknownFrameType.Error()},
		{"data above 255 bytes", Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: make([]byte, 256)},
			"frame data length 256 exceeds eBUS maximum 255: " + ebuserrors.ErrInvalidPayload.Error()},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			log := &txStepLog{}
			bus := newTxStepBus(&txStepTransport{log: log}, log, DefaultBusConfig())
			response, err := bus.sendTransaction(context.Background(), context.Background(), row.frame, 1)
			txStepCheckError(t, err, row.wantText, ebuserrors.ErrInvalidPayload)
			if response != nil {
				t.Fatalf("response = %+v, want nil", response)
			}
			txStepCheckTrace(t, log, nil)
		})
	}
}

func TestTransactionStepSendTransactionInitiatorInitiator(t *testing.T) {
	frame := Frame{Source: 0x10, Target: 0x30, Primary: 0xB5, Secondary: 0x09}
	if frame.Type() != FrameTypeInitiatorInitiator {
		t.Fatalf("fixture frame type = %v, want initiator-to-initiator", frame.Type())
	}
	log := &txStepLog{}
	bus := newTxStepBus(&txStepTransport{log: log, replies: []txStepRead{{value: SymbolAck}}}, log, DefaultBusConfig())
	response, err := bus.sendTransaction(context.Background(), context.Background(), frame, 1)
	txStepCheckError(t, err, "", nil)
	if response != nil {
		t.Fatalf("response = %+v, want nil", response)
	}
	txStepCheckTrace(t, log, txStepTrace(txStepWrites(true, buildInitiatorTelegram(frame)...), "read 00",
		"event ACK Success", "write AA", "event AttemptComplete Success"))
}

func newTxStepRequest(ctx context.Context, frame Frame) *busRequest {
	return &busRequest{frame: frame, ctx: ctx}
}

func TestRetryStepNewRetryState(t *testing.T) {
	config := DefaultBusConfig()
	config.InitiatorTarget = RetryPolicy{TimeoutRetries: 2, NACKRetries: 3}
	config.InitiatorInitiator = RetryPolicy{TimeoutRetries: 4, NACKRetries: 5}
	bus := NewBus(&txStepTransport{log: &txStepLog{}}, config, 1)
	deadline, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	rows := []struct {
		name       string
		ctx        context.Context
		target     byte
		wantType   FrameType
		wantPolicy RetryPolicy
		wantBound  bool
	}{
		{"initiator-to-target", context.Background(), 0x08, FrameTypeInitiatorTarget, config.InitiatorTarget, false},
		{"initiator-to-initiator", context.Background(), 0x30, FrameTypeInitiatorInitiator, config.InitiatorInitiator, false},
		{"broadcast", context.Background(), AddressBroadcast, FrameTypeBroadcast, RetryPolicy{}, false},
		{"deadline-bounded", deadline, 0x08, FrameTypeInitiatorTarget, config.InitiatorTarget, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			request := newTxStepRequest(row.ctx, Frame{Source: 0x10, Target: row.target, Primary: 0xB5, Secondary: 0x09})
			state := bus.newRetryState(request)
			if state.request != request || state.frameType != row.wantType || state.policy != row.wantPolicy || state.deadlineBoundsRetries != row.wantBound {
				t.Fatalf("state = %+v, want type %v policy %+v bounded %v", state, row.wantType, row.wantPolicy, row.wantBound)
			}
			if state.timeoutAttempts != 0 || state.nackAttempts != 0 || state.reconnectAttempts != 0 || state.attemptCount != 0 || state.startedAt.IsZero() {
				t.Fatalf("state counters = %+v, want zero counters and a start time", state)
			}
		})
	}
}

func TestRetryStepRetryStateBudgets(t *testing.T) {
	policy := RetryPolicy{TimeoutRetries: 1, NACKRetries: 1}
	rows := []struct {
		name        string
		err         error
		bounded     bool
		timeout     int
		nack        int
		wantRetry   bool
		wantTimeout int
		wantNack    int
	}{
		{"nack within budget", ebuserrors.ErrNACK, false, 0, 0, true, 0, 1},
		{"nack budget spent", ebuserrors.ErrNACK, false, 0, 1, false, 0, 1},
		{"crc within budget", ebuserrors.ErrCRCMismatch, false, 0, 0, true, 1, 0},
		{"collision without a deadline", ebuserrors.ErrBusCollision, false, 0, 0, true, 1, 0},
		{"collision with a deadline", ebuserrors.ErrBusCollision, true, 1, 0, true, 1, 0},
		{"timeout", ebuserrors.ErrTimeout, false, 0, 0, false, 0, 0},
		{"host error", ebuserrors.ErrAdapterHostError, false, 0, 0, false, 0, 0},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			state := &retryState{policy: policy, timeoutAttempts: row.timeout, nackAttempts: row.nack, deadlineBoundsRetries: row.bounded}
			if got := state.retry(row.err); got != row.wantRetry {
				t.Fatalf("retry = %v, want %v", got, row.wantRetry)
			}
			if state.timeoutAttempts != row.wantTimeout || state.nackAttempts != row.wantNack {
				t.Fatalf("budgets = %d/%d, want %d/%d", state.timeoutAttempts, state.nackAttempts, row.wantTimeout, row.wantNack)
			}
		})
	}
}

func TestRetryStepCollisionBackoff(t *testing.T) {
	frame := Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	closedText := "bus transport closed: " + ebuserrors.ErrTransportClosed.Error()
	rows := []struct {
		name      string
		token     uint64
		runCtx    context.Context
		reqCtx    context.Context
		replies   []txStepRead
		wantText  string
		wantTrace []string
	}{
		{
			name: "two syn symbols complete with retry", token: 7, runCtx: context.Background(), reqCtx: context.Background(),
			replies:   []txStepRead{{value: 0x10}, {value: SymbolSyn}, {value: SymbolSyn}},
			wantTrace: []string{"read 10", "read AA", "read AA", "complete 7 retry"},
		},
		{
			name: "zero token skips the completion", token: 0, runCtx: context.Background(), reqCtx: context.Background(),
			replies:   []txStepRead{{value: SymbolSyn}, {value: SymbolSyn}},
			wantTrace: []string{"read AA", "read AA"},
		},
		{
			name: "syn wait error completes with abandon", token: 7, runCtx: context.Background(), reqCtx: context.Background(),
			replies:  []txStepRead{{value: SymbolSyn}},
			wantText: closedText,
			wantTrace: []string{"read AA", "read closed", "complete 7 abandon",
				"event RequestComplete Unknown attempt=2 timeout=1 nack=0"},
		},
		{
			name: "request context done", token: 7, runCtx: context.Background(), reqCtx: canceled,
			wantText:  "bus send failed: " + context.Canceled.Error(),
			wantTrace: []string{"complete 7 abandon"},
		},
		{
			name: "run context done", token: 7, runCtx: canceled, reqCtx: context.Background(),
			wantText:  "bus send failed: " + context.Canceled.Error(),
			wantTrace: []string{"complete 7 abandon"},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			log := &txStepLog{}
			tr := &txStepRecoveryTransport{txStepTransport: txStepTransport{log: log, replies: row.replies}, token: row.token}
			bus := newTxStepBus(tr, log, DefaultBusConfig())
			state := bus.newRetryState(newTxStepRequest(row.reqCtx, frame))
			state.attemptCount, state.timeoutAttempts = 2, 1
			err := bus.collisionBackoff(row.runCtx, state, tr, row.token)
			txStepCheckError(t, err, row.wantText, nil)
			txStepCheckTrace(t, log, row.wantTrace)
		})
	}
}

func TestRetryStepAdapterResetBackoff(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	bus := NewBus(&txStepTransport{log: &txStepLog{}}, DefaultBusConfig(), 1)
	rows := []struct {
		name     string
		runCtx   context.Context
		reqCtx   context.Context
		wantText string
	}{
		{"delay elapses", context.Background(), context.Background(), ""},
		{"request context done", context.Background(), canceled, "bus send failed: " + context.Canceled.Error()},
		{"run context done", canceled, context.Background(), "bus send failed: " + context.Canceled.Error()},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			started := time.Now()
			err := bus.adapterResetBackoff(row.runCtx, row.reqCtx)
			txStepCheckError(t, err, row.wantText, nil)
			if row.wantText == "" && time.Since(started) < adapterResetRetryDelay {
				t.Fatalf("returned after %v, before the %v delay", time.Since(started), adapterResetRetryDelay)
			}
		})
	}
}

// TestRetryStepAfterError pins the decision after a failed arbitration and
// after a failed transaction, including where the collision-recovery token
// is fetched relative to the retry event.
func TestRetryStepAfterError(t *testing.T) {
	frame := Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09}
	arbCollision := fmt.Errorf("bus arbitration failed: %w", ebuserrors.ErrBusCollision)
	syns := []txStepRead{{value: SymbolSyn}, {value: SymbolSyn}}
	rows := []struct {
		name          string
		arbitration   bool
		err           error
		policy        RetryPolicy
		reconnect     int
		replies       []txStepRead
		wantText      string
		wantTrace     []string
		wantTimeout   int
		wantNack      int
		wantReconnect int
	}{
		{
			name: "arbitration collision retried fetches the token before the retry event", arbitration: true,
			err: arbCollision, policy: RetryPolicy{TimeoutRetries: 1}, replies: syns,
			wantTrace:   []string{"token 7", "event Retry Collision attempt=1 timeout=1 nack=0", "read AA", "read AA", "complete 7 retry"},
			wantTimeout: 1,
		},
		{
			name: "arbitration collision with a spent budget abandons", arbitration: true,
			err:       arbCollision,
			wantText:  "bus send collision: " + arbCollision.Error(),
			wantTrace: []string{"token 7", "complete 7 abandon", "event RequestComplete Collision attempt=1 timeout=0 nack=0"},
		},
		{
			name: "arbitration timeout without reconnect ends the request", arbitration: true,
			err:       ebuserrors.ErrTimeout,
			wantText:  "bus send timeout: " + ebuserrors.ErrTimeout.Error(),
			wantTrace: []string{"event RequestComplete Timeout attempt=1 timeout=0 nack=0"},
		},
		{
			name: "transaction collision retried fetches the token after the retry event",
			err:  ebuserrors.ErrBusCollision, policy: RetryPolicy{TimeoutRetries: 1}, replies: syns,
			wantTrace:   []string{"event Retry Collision attempt=1 timeout=1 nack=0", "token 7", "read AA", "read AA", "complete 7 retry"},
			wantTimeout: 1,
		},
		{
			name:      "transaction collision with a spent budget abandons",
			err:       ebuserrors.ErrBusCollision,
			wantText:  "bus send collision: " + ebuserrors.ErrBusCollision.Error(),
			wantTrace: []string{"token 7", "complete 7 abandon", "event RequestComplete Collision attempt=1 timeout=0 nack=0"},
		},
		{
			name: "transaction nack retried",
			err:  ebuserrors.ErrNACK, policy: RetryPolicy{NACKRetries: 1},
			wantTrace: []string{"event Retry NACK attempt=1 timeout=0 nack=1"},
			wantNack:  1,
		},
		{
			name: "transaction timeout reconnects and resets the budgets",
			err:  ebuserrors.ErrTimeout, reconnect: 1,
			wantTrace:     []string{"reconnect"},
			wantReconnect: 1,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			log := &txStepLog{}
			tr := &txStepRecoveryTransport{txStepTransport: txStepTransport{log: log, replies: row.replies}, token: 7}
			config := DefaultBusConfig()
			config.InitiatorTarget = row.policy
			config.ReconnectRetries = row.reconnect
			config.ReconnectDelay = time.Millisecond
			bus := newTxStepBus(tr, log, config)
			state := bus.newRetryState(newTxStepRequest(context.Background(), frame))
			state.attemptCount = 1
			if row.reconnect > 0 {
				state.timeoutAttempts, state.nackAttempts = 3, 2
			}
			var err error
			if row.arbitration {
				err = bus.afterArbitrationError(context.Background(), state, row.err)
			} else {
				err = bus.afterTransactionError(context.Background(), state, row.err)
			}
			txStepCheckError(t, err, row.wantText, nil)
			txStepCheckTrace(t, log, row.wantTrace)
			if state.timeoutAttempts != row.wantTimeout || state.nackAttempts != row.wantNack || state.reconnectAttempts != row.wantReconnect {
				t.Fatalf("budgets = %d/%d/%d, want %d/%d/%d", state.timeoutAttempts, state.nackAttempts, state.reconnectAttempts,
					row.wantTimeout, row.wantNack, row.wantReconnect)
			}
		})
	}
}
