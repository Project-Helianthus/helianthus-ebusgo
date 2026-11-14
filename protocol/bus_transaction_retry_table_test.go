package protocol_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// TestBusTransactionRetryTable pins how Bus.Send drives the command phase,
// the response phase and the retry loop today. It is a characterization
// test: each row records one ordered trace that interleaves the transport
// calls (arbitration, writes, reads, collision-recovery token calls and
// reconnects) with the observer events, plus the returned error and
// response, exactly as the current implementation produces them.

type txRetryFlavour int

const (
	// txRetryPlain carries wire bytes, reconnects and exposes the
	// collision-recovery lifecycle.
	txRetryPlain txRetryFlavour = iota
	// txRetryPlainBare carries wire bytes and implements nothing else.
	txRetryPlainBare
	// txRetryEnh delivers unescaped bytes with escape provenance, owns
	// arbitration (which places the source byte), accepts the structural
	// SYN signal, reconnects and exposes the collision-recovery lifecycle.
	txRetryEnh
	// txRetryEnhSource is txRetryEnh whose arbitration does not place the
	// source byte.
	txRetryEnhSource
)

func (f txRetryFlavour) enhClass() bool { return f == txRetryEnh || f == txRetryEnhSource }

// txRetryRead is one scripted read: value, escape flag and error.
type txRetryRead struct {
	value   byte
	escaped bool
	err     error
}

var errTxRetryScriptExhausted = errors.New("txretry: read script exhausted")

// txRetryCore records every transport call and observer event in one
// ordered trace. Writes are echoed unless the write index carries an echo
// override; every other read is served from the reply script in order.
type txRetryCore struct {
	mu         sync.Mutex
	escapeEcho bool
	frame      protocol.Frame
	trace      []string
	faults     []string
	writes     int
	structural bool
	pending    []txRetryRead

	echoAt        map[int]txRetryRead
	replies       []txRetryRead
	arbErrs       []error
	tokens        []uint64
	reconnectErrs []error
}

func (c *txRetryCore) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace = append(c.trace, fmt.Sprintf("write % X", p))
	index := c.writes
	c.writes++
	structural := c.structural
	c.structural = false
	if read, ok := c.echoAt[index]; ok {
		c.pending = append(c.pending, read)
		return len(p), nil
	}
	for _, value := range p {
		escaped := c.escapeEcho && !structural &&
			(value == protocol.SymbolSyn || value == protocol.SymbolEscape)
		c.pending = append(c.pending, txRetryRead{value: value, escaped: escaped})
	}
	return len(p), nil
}

func (c *txRetryCore) next(call string) (byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace = append(c.trace, call)
	var read txRetryRead
	switch {
	case len(c.pending) > 0:
		read, c.pending = c.pending[0], c.pending[1:]
	case len(c.replies) > 0:
		read, c.replies = c.replies[0], c.replies[1:]
	default:
		return 0, false, errTxRetryScriptExhausted
	}
	return read.value, read.escaped, read.err
}

func (c *txRetryCore) ReadByte() (byte, error) {
	value, _, err := c.next("read")
	return value, err
}

func (c *txRetryCore) Close() error { return nil }

// popErr records call and returns the next scripted error, nil once the
// script is used up.
func (c *txRetryCore) popErr(call string, script *[]error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace = append(c.trace, call)
	if len(*script) == 0 {
		return nil
	}
	err := (*script)[0]
	*script = (*script)[1:]
	return err
}

// txRetryLifecycle adds reconnect and the collision-recovery lifecycle.
type txRetryLifecycle struct{ *txRetryCore }

func (t txRetryLifecycle) Reconnect() error { return t.popErr("reconnect", &t.reconnectErrs) }

func (t txRetryLifecycle) CollisionRecoveryToken() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var token uint64
	if len(t.tokens) > 0 {
		token, t.tokens = t.tokens[0], t.tokens[1:]
	}
	t.trace = append(t.trace, fmt.Sprintf("token %d", token))
	return token
}

var txRetryDecisionNames = map[transport.CollisionRecoveryDecision]string{
	transport.CollisionRecoveryRetry:   "retry",
	transport.CollisionRecoveryAbandon: "abandon",
}

func (t txRetryLifecycle) CompleteCollisionRecovery(token uint64, decision transport.CollisionRecoveryDecision) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.trace = append(t.trace, fmt.Sprintf("complete %d %s", token, txRetryName(txRetryDecisionNames, decision)))
}

// txRetryPlainBareTransport implements only the raw transport contract.
type txRetryPlainBareTransport struct{ *txRetryCore }

// txRetryPlainTransport adds reconnect and the collision-recovery lifecycle.
type txRetryPlainTransport struct{ txRetryLifecycle }

// txRetryEnhTransport is the ENH-class flavour.
type txRetryEnhTransport struct{ txRetryLifecycle }

func (txRetryEnhTransport) BytesAreUnescaped() bool { return true }

func (t txRetryEnhTransport) ReadByteWithEscape() (byte, bool, error) { return t.next("read_esc") }

func (t txRetryEnhTransport) StartArbitration(initiator byte) error {
	return t.popErr(fmt.Sprintf("arb %02X", initiator), &t.arbErrs)
}

func (txRetryEnhTransport) ArbitrationSendsSource() bool { return true }

func (t txRetryEnhTransport) SignalNextWriteIsStructuralSyn() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.trace = append(t.trace, "signal")
	t.structural = true
}

// txRetryEnhSourceTransport is txRetryEnhTransport whose arbitration does
// not place the source byte.
type txRetryEnhSourceTransport struct{ txRetryEnhTransport }

func (txRetryEnhSourceTransport) ArbitrationSendsSource() bool { return false }

// txRetryObserver appends every event to the shared trace and records a
// fault when an event carries a request or response frame other than the
// one the row sent or received.
type txRetryObserver struct{ core *txRetryCore }

func (o txRetryObserver) OnBusEvent(event protocol.BusEvent) error {
	line := txRetryEventLine(event)
	o.core.mu.Lock()
	defer o.core.mu.Unlock()
	o.core.trace = append(o.core.trace, line)
	frame := o.core.frame
	if event.HasRequest != reflect.DeepEqual(event.Request, frame) ||
		(!event.HasRequest && !reflect.DeepEqual(event.Request, protocol.Frame{})) {
		o.core.faults = append(o.core.faults, fmt.Sprintf("%s: request %+v", line, event.Request))
	}
	header := protocol.Frame{Source: frame.Target, Target: frame.Source, Primary: frame.Primary, Secondary: frame.Secondary}
	got := event.Response
	got.Data = nil
	if (event.HasResponse && !reflect.DeepEqual(got, header)) ||
		(!event.HasResponse && !reflect.DeepEqual(event.Response, protocol.Frame{})) {
		o.core.faults = append(o.core.faults, fmt.Sprintf("%s: response %+v", line, event.Response))
	}
	return nil
}

var (
	txRetryKindNames = map[protocol.BusEventKind]string{
		protocol.BusEventArbitration:     "arbitration",
		protocol.BusEventTX:              "tx",
		protocol.BusEventRX:              "rx",
		protocol.BusEventACK:             "ack",
		protocol.BusEventNACK:            "nack",
		protocol.BusEventTimeout:         "timeout",
		protocol.BusEventCRCMismatch:     "crc_mismatch",
		protocol.BusEventEchoMismatch:    "echo_mismatch",
		protocol.BusEventRetry:           "retry",
		protocol.BusEventAttemptComplete: "attempt_complete",
		protocol.BusEventRequestComplete: "request_complete",
		protocol.BusEventObserverFault:   "observer_fault",
		protocol.BusEventAdapterReset:    "adapter_reset",
	}
	txRetryOutcomeNames = map[protocol.BusOutcomeClass]string{
		protocol.BusOutcomeUnknown:       "unknown",
		protocol.BusOutcomeSuccess:       "success",
		protocol.BusOutcomeTimeout:       "timeout",
		protocol.BusOutcomeNACK:          "nack",
		protocol.BusOutcomeCRCMismatch:   "crc_mismatch",
		protocol.BusOutcomeEchoMismatch:  "echo_mismatch",
		protocol.BusOutcomeCollision:     "collision",
		protocol.BusOutcomeObserverFault: "observer_fault",
		protocol.BusOutcomeAdapterReset:  "adapter_reset",
	}
	txRetryReasonNames = map[protocol.BusRetryReason]string{
		protocol.BusRetryReasonTimeout:      "timeout",
		protocol.BusRetryReasonNACK:         "nack",
		protocol.BusRetryReasonCRCMismatch:  "crc_mismatch",
		protocol.BusRetryReasonCollision:    "collision",
		protocol.BusRetryReasonAdapterReset: "adapter_reset",
	}
	txRetryFrameTypeNames = map[protocol.FrameType]string{
		protocol.FrameTypeUnknown:            "-",
		protocol.FrameTypeBroadcast:          "bc",
		protocol.FrameTypeInitiatorTarget:    "it",
		protocol.FrameTypeInitiatorInitiator: "ii",
	}
)

// txRetryName returns the table name of value, or a numbered fallback that
// no expectation uses.
func txRetryName[K comparable](names map[K]string, value K) string {
	if name, ok := names[value]; ok {
		return name
	}
	return fmt.Sprintf("?%v", value)
}

// txRetryEventLine renders an event as "ev <kind> <outcome> <frame type>
// a<attempt>" followed by every non-zero field. A plain TX or RX event
// collapses to "ev tx XX" or "ev rx XX".
func txRetryEventLine(event protocol.BusEvent) string {
	kind := txRetryName(txRetryKindNames, event.Kind)
	bare := event
	bare.Kind, bare.Byte = 0, 0
	if (event.Kind == protocol.BusEventTX || event.Kind == protocol.BusEventRX) &&
		reflect.DeepEqual(bare, protocol.BusEvent{Outcome: protocol.BusOutcomeSuccess}) {
		return fmt.Sprintf("ev %s %02X", kind, event.Byte)
	}
	var line strings.Builder
	fmt.Fprintf(&line, "ev %s %s %s a%d", kind, txRetryName(txRetryOutcomeNames, event.Outcome),
		txRetryName(txRetryFrameTypeNames, event.FrameType), event.Attempt)
	for _, part := range []struct {
		on   bool
		text string
	}{
		{event.Retry != protocol.BusRetryReasonUnknown, " r:" + txRetryName(txRetryReasonNames, event.Retry)},
		{event.Initiator != 0, fmt.Sprintf(" i%02X", event.Initiator)},
		{event.Byte != 0, fmt.Sprintf(" b%02X", event.Byte)},
		{event.ByteKnown, " known"},
		{event.EchoWasEscaped, " esc"},
		{event.TimeoutRetries != 0, fmt.Sprintf(" t%d", event.TimeoutRetries)},
		{event.NACKRetries != 0, fmt.Sprintf(" n%d", event.NACKRetries)},
		{event.HasRequest, " req"},
		{event.HasResponse, txRetryResp(event.Response.Data)},
	} {
		if part.on {
			line.WriteString(part.text)
		}
	}
	return line.String()
}

func txRetryResp(data []byte) string { return fmt.Sprintf(" resp=% X", data) }

const (
	txRetrySource    = byte(0x10)
	txRetryTarget    = byte(0x15) // target-class address
	txRetryPeer      = byte(0x30) // initiator-class address, not ours
	txRetryPrimary   = byte(0xB5)
	txRetrySecondary = byte(0x09)
	txRetryData      = byte(0x0D)
)

// txRetryReconnectDelay is the small reconnect delay every row configures.
const txRetryReconnectDelay = 20 * time.Millisecond

var (
	// txRetryResponse is the default response payload of the target.
	txRetryResponse = []byte{0x01, 0x02}
	// txRetryEscData and txRetrySynData need escape pairs on a plain wire.
	txRetryEscData = []byte{protocol.SymbolEscape, 0x02}
	txRetrySynData = []byte{protocol.SymbolSyn, 0x02}
)

func txRetryFrame(target byte) protocol.Frame {
	return protocol.Frame{
		Source: txRetrySource, Target: target, Primary: txRetryPrimary, Secondary: txRetrySecondary,
		Data: []byte{txRetryData},
	}
}

// txRetryTelegram is the logical command telegram including its CRC.
func txRetryTelegram(frame protocol.Frame) []byte {
	telegram := append([]byte{frame.Source, frame.Target, frame.Primary, frame.Secondary, byte(len(frame.Data))}, frame.Data...)
	return append(telegram, protocol.CRC(telegram))
}

// txRetryWire returns the bytes a plain transport carries for the logical
// symbols (escape pairs for 0xA9 and 0xAA); ENH-class transports carry
// the symbols unchanged.
func txRetryWire(flavour txRetryFlavour, symbols []byte) []byte {
	if flavour.enhClass() {
		return append([]byte(nil), symbols...)
	}
	var wire []byte
	for _, symbol := range symbols {
		switch symbol {
		case protocol.SymbolEscape:
			wire = append(wire, protocol.SymbolEscape, 0x00)
		case protocol.SymbolSyn:
			wire = append(wire, protocol.SymbolEscape, 0x01)
		default:
			wire = append(wire, symbol)
		}
	}
	return wire
}

// txRetrySegment is the logical response segment: length, data and CRC;
// a non-zero crcXor corrupts the CRC.
func txRetrySegment(data []byte, crcXor byte) []byte {
	segment := append([]byte{byte(len(data))}, data...)
	return append(segment, protocol.CRC(segment)^crcXor)
}

func txRetryReads(values ...byte) []txRetryRead {
	reads := make([]txRetryRead, 0, len(values))
	for _, value := range values {
		reads = append(reads, txRetryRead{value: value})
	}
	return reads
}

func txRetryErr(err error) []txRetryRead { return []txRetryRead{{err: err}} }

// txRetryReplies concatenates reply groups.
func txRetryReplies(groups ...[]txRetryRead) []txRetryRead {
	var replies []txRetryRead
	for _, group := range groups {
		replies = append(replies, group...)
	}
	return replies
}

// txRetryResponseReads is the wire form of a response segment.
func txRetryResponseReads(flavour txRetryFlavour, data []byte, crcXor byte) []txRetryRead {
	return txRetryReads(txRetryWire(flavour, txRetrySegment(data, crcXor))...)
}

// txRetryWant builds the expected trace of one row; every step returns
// the builder so a row reads as one chain.
type txRetryWant struct {
	flavour txRetryFlavour
	frame   protocol.Frame
	lines   []string
}

func (w *txRetryWant) add(lines ...string) *txRetryWant {
	w.lines = append(w.lines, lines...)
	return w
}

// ev appends an event line carrying the row's frame type.
func (w *txRetryWant) ev(kind, outcome string, attempt int, extra string) *txRetryWant {
	ft := txRetryName(txRetryFrameTypeNames, w.frame.Type())
	return w.add(fmt.Sprintf("ev %s %s %s a%d%s", kind, outcome, ft, attempt, extra))
}

func (w *txRetryWant) echoRead() string {
	if w.flavour.enhClass() {
		return "read_esc"
	}
	return "read"
}

// put appends single-byte writes that are echoed unchanged.
func (w *txRetryWant) put(values ...byte) *txRetryWant {
	for _, value := range values {
		w.add(fmt.Sprintf("write %02X", value), fmt.Sprintf("ev tx %02X", value), w.echoRead(), fmt.Sprintf("ev rx %02X", value))
	}
	return w
}

// syn appends the structural end-of-message SYN.
func (w *txRetryWant) syn() *txRetryWant {
	if w.flavour.enhClass() {
		w.add("signal")
	}
	return w.put(protocol.SymbolSyn)
}

// command appends the command telegram writes, from the source byte or
// from the target byte.
func (w *txRetryWant) command(includeSource bool) *txRetryWant {
	telegram := txRetryTelegram(w.frame)
	if !includeSource {
		telegram = telegram[1:]
	}
	return w.put(txRetryWire(w.flavour, telegram)...)
}

// recv appends plain reads (command ACK and response symbols).
func (w *txRetryWant) recv(values ...byte) *txRetryWant {
	for _, value := range values {
		w.add("read", fmt.Sprintf("ev rx %02X", value))
	}
	return w
}

// waitRead appends the reads of the SYN wait before a collision retry.
func (w *txRetryWant) waitRead(values ...byte) *txRetryWant {
	for _, value := range values {
		w.add(w.echoRead(), fmt.Sprintf("ev rx %02X", value))
	}
	return w
}

// arb appends one arbitration with its event outcome.
func (w *txRetryWant) arb(attempt int, outcome string) *txRetryWant {
	w.add(fmt.Sprintf("arb %02X", txRetrySource))
	return w.ev("arbitration", outcome, attempt, fmt.Sprintf(" i%02X", txRetrySource))
}

// started appends a granted arbitration and a command sent from the target
// byte, the first send on an ENH-class transport.
func (w *txRetryWant) started(attempt int) *txRetryWant {
	return w.arb(attempt, "success").command(false)
}

func (w *txRetryWant) ack(attempt int) *txRetryWant {
	return w.recv(protocol.SymbolAck).ev("ack", "success", attempt, " req")
}

func (w *txRetryWant) nack(attempt int) *txRetryWant {
	return w.recv(protocol.SymbolNack).ev("nack", "nack", attempt, " bFF req")
}

// fail appends the outcome event of a failed attempt.
func (w *txRetryWant) fail(outcome string, attempt int) *txRetryWant {
	return w.ev(outcome, outcome, attempt, " req")
}

// doubleNack appends a command answered by NACK twice, from the first
// send to the outcome event.
func (w *txRetryWant) doubleNack(attempt int, includeSource bool) *txRetryWant {
	return w.command(includeSource).nack(attempt).command(true).nack(attempt).syn().fail("nack", attempt)
}

// respond appends a well-formed response, the ACK sent back and the end
// of the attempt.
func (w *txRetryWant) respond(attempt int, data []byte) *txRetryWant {
	w.recv(txRetryWire(w.flavour, txRetrySegment(data, 0))...).put(protocol.SymbolAck)
	w.ev("ack", "success", attempt, " req"+txRetryResp(data)).syn()
	return w.ev("attempt_complete", "success", attempt, " req"+txRetryResp(data))
}

// answered appends a well-formed response and the successful end of the
// request; retries carries the retry counters of the final event.
func (w *txRetryWant) answered(attempt int, data []byte, retries string) {
	w.respond(attempt, data).done("success", attempt, retries+" req"+txRetryResp(data))
}

// timedOut appends the timeout event and the end of the request.
func (w *txRetryWant) timedOut(attempt int) {
	w.fail("timeout", attempt).done("timeout", attempt, " req")
}

// badResponse appends a response with a corrupted CRC and the NACK sent
// back.
func (w *txRetryWant) badResponse(attempt int, data []byte) *txRetryWant {
	w.recv(txRetryWire(w.flavour, txRetrySegment(data, 0x01))...).put(protocol.SymbolNack)
	return w.ev("nack", "crc_mismatch", attempt, " bFF req"+txRetryResp(data))
}

// crcEvent appends the CRC-mismatch event of the first bad response.
func (w *txRetryWant) crcEvent(attempt int, data []byte) *txRetryWant {
	return w.ev("crc_mismatch", "crc_mismatch", attempt, " req"+txRetryResp(data))
}

// noResponse appends the end of an attempt without a response phase.
func (w *txRetryWant) noResponse(attempt int) *txRetryWant {
	return w.syn().ev("attempt_complete", "success", attempt, " req")
}

func (w *txRetryWant) retry(reason string, attempt int, extra string) *txRetryWant {
	return w.ev("retry", reason, attempt, " r:"+reason+extra+" req")
}

func (w *txRetryWant) done(outcome string, attempt int, extra string) {
	w.ev("request_complete", outcome, attempt, extra)
}

// echoSyn appends a payload write whose echo is a wire SYN.
func (w *txRetryWant) echoSyn(value byte) *txRetryWant {
	return w.add(fmt.Sprintf("write %02X", value), fmt.Sprintf("ev tx %02X", value), w.echoRead(),
		"ev rx AA", "ev echo_mismatch echo_mismatch - a0 bAA known")
}

type txRetryRow struct {
	name    string
	flavour txRetryFlavour
	target  byte
	// deadline gives the request context a deadline, which makes
	// collision-class errors retry without consuming the policy budget.
	deadline         bool
	policy           *protocol.RetryPolicy
	reconnectRetries int

	echoAt        map[int]txRetryRead
	replies       []txRetryRead
	arbErrs       []error
	tokens        []uint64
	reconnectErrs []error

	wantResponse []byte
	wantSentinel error
	wantText     string
	want         func(w *txRetryWant)
}

func TestBusTransactionRetryTable(t *testing.T) {
	t.Parallel()
	txRetryCheckFixtures(t)

	const (
		syn  = protocol.SymbolSyn
		ack  = protocol.SymbolAck
		nack = protocol.SymbolNack
		esc  = protocol.SymbolEscape
	)
	var (
		it, ii, bc    = txRetryTarget, txRetryPeer, protocol.AddressBroadcast
		resp          = txRetryResponse
		timeoutText   = ebuserrors.ErrTimeout.Error()
		collisionText = ebuserrors.ErrBusCollision.Error()
		timeoutBudget = &protocol.RetryPolicy{TimeoutRetries: 1, NACKRetries: 1}
		noBudget      = &protocol.RetryPolicy{}
		closed        = ebuserrors.ErrTransportClosed
		readTimeout   = txRetryErr(ebuserrors.ErrTimeout)
		synEcho5      = map[int]txRetryRead{5: {value: syn}}
		ackResp       = func(f txRetryFlavour) []txRetryRead {
			return txRetryReplies(txRetryReads(ack), txRetryResponseReads(f, resp, 0))
		}
		badResp = func(f txRetryFlavour) []txRetryRead { return txRetryResponseReads(f, resp, 0x01) }
	)

	rows := []txRetryRow{
		// Command phase: ACK, then the response.
		{name: "plain/ack_then_response", flavour: txRetryPlain, target: it, replies: ackResp(txRetryPlain), wantResponse: resp,
			want: func(w *txRetryWant) { w.command(true).ack(1).answered(1, resp, "") }},
		{name: "enh/ack_then_response", flavour: txRetryEnh, target: it, replies: ackResp(txRetryEnh), wantResponse: resp,
			want: func(w *txRetryWant) { w.started(1).ack(1).answered(1, resp, "") }},
		{name: "enh_source/arbitration_without_source_sends_source", flavour: txRetryEnhSource, target: it,
			replies: ackResp(txRetryEnhSource), wantResponse: resp,
			want: func(w *txRetryWant) { w.arb(1, "success").command(true).ack(1).answered(1, resp, "") }},

		// NACK: the command is sent once more without arbitration, with the source.
		{name: "plain/nack_then_ack", flavour: txRetryPlain, target: it,
			replies: txRetryReplies(txRetryReads(nack), ackResp(txRetryPlain)), wantResponse: resp,
			want: func(w *txRetryWant) { w.command(true).nack(1).command(true).ack(1).answered(1, resp, "") }},
		{name: "enh/nack_then_ack_resends_with_source", flavour: txRetryEnh, target: it,
			replies: txRetryReplies(txRetryReads(nack), ackResp(txRetryEnh)), wantResponse: resp,
			want: func(w *txRetryWant) { w.started(1).nack(1).command(true).ack(1).answered(1, resp, "") }},
		// Double NACK: one policy retry with a new arbitration.
		{name: "enh/double_nack_then_policy_retry", flavour: txRetryEnh, target: it,
			replies: txRetryReplies(txRetryReads(nack, nack), ackResp(txRetryEnh)), wantResponse: resp,
			want: func(w *txRetryWant) {
				w.arb(1, "success").doubleNack(1, false).retry("nack", 1, " n1")
				w.started(2).ack(2).answered(2, resp, " n1")
			}},
		// NACK budget: a second double NACK ends the request; NACK never reconnects.
		{name: "enh/double_nack_exhausts_nack_budget", flavour: txRetryEnh, target: it, reconnectRetries: 3,
			replies:      txRetryReads(nack, nack, nack, nack),
			wantSentinel: ebuserrors.ErrNACK, wantText: "bus send nack: nack received: " + ebuserrors.ErrNACK.Error(),
			want: func(w *txRetryWant) {
				w.arb(1, "success").doubleNack(1, false).retry("nack", 1, " n1")
				w.arb(2, "success").doubleNack(2, false).done("nack", 2, " n1 req")
			}},
		{name: "plain/double_nack_without_nack_budget", flavour: txRetryPlain, target: it, policy: noBudget,
			replies:      txRetryReads(nack, nack),
			wantSentinel: ebuserrors.ErrNACK, wantText: "bus send nack: nack received: " + ebuserrors.ErrNACK.Error(),
			want: func(w *txRetryWant) { w.doubleNack(1, true).done("nack", 1, " req") }},

		// SYN or another symbol at the ACK wait.
		{name: "plain/syn_at_ack_wait", flavour: txRetryPlain, target: it, replies: txRetryReads(syn),
			wantSentinel: ebuserrors.ErrTimeout, wantText: "bus send timeout: syn while waiting for command ack: " + timeoutText,
			want: func(w *txRetryWant) { w.command(true).recv(syn).timedOut(1) }},
		{name: "plain/escaped_aa_at_ack_wait", flavour: txRetryPlain, target: it, replies: txRetryReads(esc, 0x01),
			wantSentinel: ebuserrors.ErrTimeout, wantText: "bus send timeout: syn while waiting for command ack: " + timeoutText,
			want: func(w *txRetryWant) { w.command(true).recv(esc, 0x01).timedOut(1) }},
		{name: "enh/aa_at_ack_wait", flavour: txRetryEnh, target: it, replies: txRetryReads(syn),
			wantSentinel: ebuserrors.ErrTimeout,
			wantText:     "bus send timeout: unexpected symbol 0xaa while waiting for command ack: " + timeoutText,
			want:         func(w *txRetryWant) { w.started(1).recv(syn).timedOut(1) }},
		{name: "plain/other_symbol_at_ack_wait", flavour: txRetryPlain, target: it, replies: txRetryReads(0x55),
			wantSentinel: ebuserrors.ErrTimeout,
			wantText:     "bus send timeout: unexpected symbol 0x55 while waiting for command ack: " + timeoutText,
			want:         func(w *txRetryWant) { w.command(true).recv(0x55).timedOut(1) }},
		{name: "enh/other_symbol_at_ack_wait", flavour: txRetryEnh, target: it, replies: txRetryReads(0x55),
			wantSentinel: ebuserrors.ErrTimeout,
			wantText:     "bus send timeout: unexpected symbol 0x55 while waiting for command ack: " + timeoutText,
			want:         func(w *txRetryWant) { w.started(1).recv(0x55).timedOut(1) }},

		// Timeout and host errors are not retried.
		{name: "enh/timeout_at_ack_wait_without_retry", flavour: txRetryEnh, target: it,
			replies:      readTimeout,
			wantSentinel: ebuserrors.ErrTimeout, wantText: "bus send timeout: " + timeoutText,
			want: func(w *txRetryWant) { w.started(1).add("read").timedOut(1) }},
		{name: "plain_bare/timeout_without_reconnect_capability", flavour: txRetryPlainBare, target: it, reconnectRetries: 3,
			replies:      readTimeout,
			wantSentinel: ebuserrors.ErrTimeout, wantText: "bus send timeout: " + timeoutText,
			want: func(w *txRetryWant) { w.command(true).add("read").timedOut(1) }},
		{name: "enh/host_error_at_arbitration_without_retry", flavour: txRetryEnh, target: it,
			policy: timeoutBudget, reconnectRetries: 3, arbErrs: []error{ebuserrors.ErrAdapterHostError},
			wantSentinel: ebuserrors.ErrAdapterHostError,
			wantText:     "bus send failed: bus arbitration failed: " + ebuserrors.ErrAdapterHostError.Error(),
			want:         func(w *txRetryWant) { w.arb(1, "unknown").done("unknown", 1, " req") }},
		{name: "enh/host_error_at_ack_wait_without_retry", flavour: txRetryEnh, target: it,
			policy: timeoutBudget, reconnectRetries: 3, replies: txRetryErr(ebuserrors.ErrAdapterHostError),
			wantSentinel: ebuserrors.ErrAdapterHostError, wantText: "bus send failed: " + ebuserrors.ErrAdapterHostError.Error(),
			want: func(w *txRetryWant) { w.started(1).add("read").done("unknown", 1, " req") }},

		// Broadcast and initiator-to-initiator: no response phase.
		{name: "plain/broadcast", flavour: txRetryPlain, target: bc,
			want: func(w *txRetryWant) { w.command(true).noResponse(1).done("success", 1, " req") }},
		{name: "enh/broadcast", flavour: txRetryEnh, target: bc,
			want: func(w *txRetryWant) { w.started(1).noResponse(1).done("success", 1, " req") }},
		{name: "plain/initiator_to_initiator", flavour: txRetryPlain, target: ii, replies: txRetryReads(ack),
			want: func(w *txRetryWant) { w.command(true).ack(1).noResponse(1).done("success", 1, " req") }},
		{name: "enh/initiator_to_initiator", flavour: txRetryEnh, target: ii, replies: txRetryReads(ack),
			want: func(w *txRetryWant) { w.started(1).ack(1).noResponse(1).done("success", 1, " req") }},

		// Response phase: SYN checks on plain transports, decoding, CRC.
		{name: "plain/syn_as_response_length", flavour: txRetryPlain, target: it, replies: txRetryReads(ack, syn),
			wantSentinel: ebuserrors.ErrTimeout, wantText: "bus send timeout: syn while waiting for response length: " + timeoutText,
			want: func(w *txRetryWant) { w.command(true).ack(1).recv(syn).timedOut(1) }},
		{name: "plain/syn_in_response_data", flavour: txRetryPlain, target: it, replies: txRetryReads(ack, 0x02, 0x01, syn),
			wantSentinel: ebuserrors.ErrTimeout, wantText: "bus send timeout: syn while reading response data: " + timeoutText,
			want: func(w *txRetryWant) { w.command(true).ack(1).recv(0x02, 0x01, syn).timedOut(1) }},
		{name: "plain/syn_as_response_crc", flavour: txRetryPlain, target: it, replies: txRetryReads(ack, 0x02, 0x01, 0x02, syn),
			wantSentinel: ebuserrors.ErrTimeout, wantText: "bus send timeout: syn while waiting for response crc: " + timeoutText,
			want: func(w *txRetryWant) { w.command(true).ack(1).recv(0x02, 0x01, 0x02, syn).timedOut(1) }},
		// A plain transport decodes an escaped 0xAA data byte to SYN and
		// then treats it as the bus going idle.
		{name: "plain/escaped_aa_in_response_data", flavour: txRetryPlain, target: it, replies: txRetryReads(ack, 0x02, esc, 0x01),
			wantSentinel: ebuserrors.ErrTimeout, wantText: "bus send timeout: syn while reading response data: " + timeoutText,
			want: func(w *txRetryWant) { w.command(true).ack(1).recv(0x02, esc, 0x01).timedOut(1) }},
		{name: "plain/escaped_a9_in_response_data", flavour: txRetryPlain, target: it,
			replies: txRetryReplies(txRetryReads(ack), txRetryResponseReads(txRetryPlain, txRetryEscData, 0)), wantResponse: txRetryEscData,
			want: func(w *txRetryWant) { w.command(true).ack(1).answered(1, txRetryEscData, "") }},
		{name: "enh/aa_in_response_data", flavour: txRetryEnh, target: it,
			replies: txRetryReplies(txRetryReads(ack), txRetryResponseReads(txRetryEnh, txRetrySynData, 0)), wantResponse: txRetrySynData,
			want: func(w *txRetryWant) { w.started(1).ack(1).answered(1, txRetrySynData, "") }},
		{name: "plain/invalid_escape_in_response", flavour: txRetryPlain, target: it, reconnectRetries: 3,
			replies:      txRetryReads(ack, 0x02, esc, 0x05),
			wantSentinel: ebuserrors.ErrInvalidPayload,
			wantText:     "bus send failed: invalid escape sequence 0x05: " + ebuserrors.ErrInvalidPayload.Error(),
			want:         func(w *txRetryWant) { w.command(true).ack(1).recv(0x02, esc, 0x05).done("unknown", 1, " req") }},
		{name: "enh/crc_mismatch_then_retry", flavour: txRetryEnh, target: it,
			replies: txRetryReplies(txRetryReads(ack), badResp(txRetryEnh), txRetryResponseReads(txRetryEnh, resp, 0)), wantResponse: resp,
			want: func(w *txRetryWant) { w.started(1).ack(1).badResponse(1, resp).crcEvent(1, resp).answered(1, resp, "") }},
		{name: "plain/crc_mismatch_twice", flavour: txRetryPlain, target: it,
			replies:      txRetryReplies(txRetryReads(ack), badResp(txRetryPlain), badResp(txRetryPlain)),
			wantSentinel: ebuserrors.ErrCRCMismatch, wantText: "bus send crc mismatch: crc mismatch: " + ebuserrors.ErrCRCMismatch.Error(),
			want: func(w *txRetryWant) {
				w.command(true).ack(1).badResponse(1, resp).crcEvent(1, resp).badResponse(1, resp).syn()
				w.fail("crc_mismatch", 1).done("crc_mismatch", 1, " req")
			}},
		{name: "plain/crc_mismatch_twice_retried_on_timeout_budget", flavour: txRetryPlain, target: it, policy: timeoutBudget,
			replies:      txRetryReplies(txRetryReads(ack), badResp(txRetryPlain), badResp(txRetryPlain), ackResp(txRetryPlain)),
			wantResponse: resp,
			want: func(w *txRetryWant) {
				w.command(true).ack(1).badResponse(1, resp).crcEvent(1, resp).badResponse(1, resp).syn()
				w.fail("crc_mismatch", 1).retry("crc_mismatch", 1, " t1")
				w.command(true).ack(2).answered(2, resp, " t1")
			}},

		// Collision at arbitration: the token is fetched before the retry
		// decision; without a deadline the default policy abandons.
		{name: "enh/arbitration_collision_without_deadline", flavour: txRetryEnh, target: it,
			arbErrs: []error{ebuserrors.ErrBusCollision}, tokens: []uint64{7},
			wantSentinel: ebuserrors.ErrBusCollision, wantText: "bus send collision: bus arbitration failed: " + collisionText,
			want: func(w *txRetryWant) {
				w.arb(1, "collision").add("token 7", "complete 7 abandon").done("collision", 1, " req")
			}},
		{name: "enh/arbitration_collision_with_deadline", flavour: txRetryEnh, target: it, deadline: true,
			arbErrs: []error{ebuserrors.ErrBusCollision}, tokens: []uint64{7},
			replies: txRetryReplies([]txRetryRead{{value: syn, escaped: true}, {value: syn}},
				readTimeout, txRetryReads(syn), ackResp(txRetryEnh)),
			wantResponse: resp,
			want: func(w *txRetryWant) {
				w.arb(1, "collision").add("token 7").retry("collision", 1, "")
				w.waitRead(syn, syn).add("read_esc").waitRead(syn).add("complete 7 retry")
				w.started(2).ack(2).answered(2, resp, "")
			}},
		{name: "enh/arbitration_collision_with_deadline_without_live_token", flavour: txRetryEnh, target: bc, deadline: true,
			arbErrs: []error{ebuserrors.ErrBusCollision}, replies: txRetryReads(syn, syn),
			want: func(w *txRetryWant) {
				w.arb(1, "collision").add("token 0").retry("collision", 1, "").waitRead(syn, syn)
				w.started(2).noResponse(2).done("success", 2, " req")
			}},
		{name: "enh/arbitration_collision_syn_wait_fails", flavour: txRetryEnh, target: it, deadline: true,
			arbErrs: []error{ebuserrors.ErrBusCollision}, tokens: []uint64{7}, replies: txRetryErr(closed),
			wantSentinel: closed, wantText: "bus transport closed: " + closed.Error(),
			want: func(w *txRetryWant) {
				w.arb(1, "collision").add("token 7").retry("collision", 1, "")
				w.add("read_esc", "complete 7 abandon").done("unknown", 1, " req")
			}},
		{name: "enh/arbitration_collision_on_timeout_budget", flavour: txRetryEnh, target: it, policy: timeoutBudget,
			arbErrs: []error{ebuserrors.ErrBusCollision, ebuserrors.ErrBusCollision}, tokens: []uint64{7, 8},
			replies:      txRetryReads(syn, syn),
			wantSentinel: ebuserrors.ErrBusCollision, wantText: "bus send collision: bus arbitration failed: " + collisionText,
			want: func(w *txRetryWant) {
				w.arb(1, "collision").add("token 7").retry("collision", 1, " t1").waitRead(syn, syn).add("complete 7 retry")
				w.arb(2, "collision").add("token 8", "complete 8 abandon").done("collision", 2, " t1 req")
			}},

		// Collision after the telegram: the token is fetched after the
		// retry event.
		{name: "plain/collision_after_telegram_without_deadline", flavour: txRetryPlain, target: it,
			echoAt: synEcho5, tokens: []uint64{7},
			wantSentinel: ebuserrors.ErrBusCollision, wantText: "bus send collision: unexpected syn while waiting for echo: " + collisionText,
			want: func(w *txRetryWant) {
				w.put(txRetryTelegram(w.frame)[:5]...).echoSyn(txRetryData)
				w.add("token 7", "complete 7 abandon").done("collision", 1, " req")
			}},
		{name: "plain_bare/collision_after_telegram_without_lifecycle", flavour: txRetryPlainBare, target: it, echoAt: synEcho5,
			wantSentinel: ebuserrors.ErrBusCollision, wantText: "bus send collision: unexpected syn while waiting for echo: " + collisionText,
			want: func(w *txRetryWant) {
				w.put(txRetryTelegram(w.frame)[:5]...).echoSyn(txRetryData).done("collision", 1, " req")
			}},
		{name: "plain/collision_after_telegram_with_deadline", flavour: txRetryPlain, target: it, deadline: true,
			echoAt: synEcho5, tokens: []uint64{7},
			replies: txRetryReplies(txRetryReads(esc, 0x01, syn, syn), ackResp(txRetryPlain)), wantResponse: resp,
			want: func(w *txRetryWant) {
				w.put(txRetryTelegram(w.frame)[:5]...).echoSyn(txRetryData).retry("collision", 1, "").add("token 7")
				w.waitRead(esc, 0x01, syn, syn).add("complete 7 retry")
				w.command(true).ack(2).answered(2, resp, "")
			}},
		{name: "enh/first_byte_collision_with_deadline", flavour: txRetryEnh, target: it, deadline: true,
			echoAt: map[int]txRetryRead{0: {value: txRetryPeer}}, tokens: []uint64{7},
			replies: txRetryReplies(txRetryReads(syn, syn), ackResp(txRetryEnh)), wantResponse: resp,
			want: func(w *txRetryWant) {
				w.arb(1, "success").add("write 15", "ev tx 15", "read_esc", "ev rx 30").retry("collision", 1, "")
				w.add("token 7").waitRead(syn, syn).add("complete 7 retry")
				w.started(2).ack(2).answered(2, resp, "")
			}},

		// Adapter reset: retried on the timeout budget after the fixed
		// delay; broadcast has no retry policy.
		{name: "enh/arbitration_adapter_reset_retry", flavour: txRetryEnh, target: it, policy: timeoutBudget,
			arbErrs: []error{ebuserrors.ErrAdapterReset}, replies: ackResp(txRetryEnh), wantResponse: resp,
			want: func(w *txRetryWant) {
				w.arb(1, "adapter_reset").retry("adapter_reset", 1, " t1")
				w.started(2).ack(2).answered(2, resp, " t1")
			}},
		{name: "enh/ack_wait_adapter_reset_retry", flavour: txRetryEnh, target: it, policy: timeoutBudget,
			replies: txRetryReplies(txRetryErr(ebuserrors.ErrAdapterReset), ackResp(txRetryEnh)), wantResponse: resp,
			want: func(w *txRetryWant) {
				w.started(1).add("read").fail("adapter_reset", 1).retry("adapter_reset", 1, " t1")
				w.started(2).ack(2).answered(2, resp, " t1")
			}},
		{name: "enh/broadcast_adapter_reset_without_policy", flavour: txRetryEnh, target: bc, policy: timeoutBudget,
			arbErrs:      []error{ebuserrors.ErrAdapterReset},
			wantSentinel: ebuserrors.ErrAdapterReset,
			wantText:     "bus send adapter reset: bus arbitration failed: " + ebuserrors.ErrAdapterReset.Error(),
			want:         func(w *txRetryWant) { w.arb(1, "adapter_reset").done("adapter_reset", 1, " req") }},

		// Reconnect tier: a successful reconnect resets both budgets.
		{name: "enh/adapter_reset_without_budget_reconnects", flavour: txRetryEnh, target: bc, reconnectRetries: 1,
			arbErrs: []error{ebuserrors.ErrAdapterReset},
			want: func(w *txRetryWant) {
				w.arb(1, "adapter_reset").add("reconnect")
				w.started(2).noResponse(2).done("success", 2, " req")
			}},
		{name: "enh/reconnect_resets_timeout_budget", flavour: txRetryEnh, target: it, policy: timeoutBudget, reconnectRetries: 1,
			arbErrs: []error{ebuserrors.ErrAdapterReset, ebuserrors.ErrAdapterReset, ebuserrors.ErrAdapterReset},
			replies: ackResp(txRetryEnh), wantResponse: resp,
			want: func(w *txRetryWant) {
				w.arb(1, "adapter_reset").retry("adapter_reset", 1, " t1").arb(2, "adapter_reset").add("reconnect")
				w.arb(3, "adapter_reset").retry("adapter_reset", 3, " t1").started(4).ack(4).answered(4, resp, " t1")
			}},
		{name: "enh/reconnect_resets_nack_budget", flavour: txRetryEnh, target: it, reconnectRetries: 1, wantResponse: resp,
			replies: txRetryReplies(txRetryReads(nack, nack), readTimeout, txRetryReads(nack, nack), ackResp(txRetryEnh)),
			want: func(w *txRetryWant) {
				w.arb(1, "success").doubleNack(1, false).retry("nack", 1, " n1")
				w.started(2).add("read").fail("timeout", 2).add("reconnect")
				w.arb(3, "success").doubleNack(3, false).retry("nack", 3, " n1")
				w.started(4).ack(4).answered(4, resp, " n1")
			}},
		{name: "plain/reconnect_failure_then_reconnect_cap", flavour: txRetryPlain, target: it, reconnectRetries: 1,
			reconnectErrs: []error{closed},
			replies:       txRetryReplies(readTimeout, readTimeout),
			wantSentinel:  ebuserrors.ErrTimeout, wantText: "bus send timeout: " + timeoutText,
			want: func(w *txRetryWant) {
				w.command(true).add("read").fail("timeout", 1).add("reconnect")
				w.command(true).add("read").timedOut(2)
			}},
	}

	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			txRetryRunRow(t, row)
		})
	}
}

// txRetryCheckFixtures fails when a fixture would need wire escaping where
// the rows assume none.
func txRetryCheckFixtures(t *testing.T) {
	t.Helper()
	for _, target := range []byte{txRetryTarget, txRetryPeer, protocol.AddressBroadcast} {
		for _, symbol := range txRetryTelegram(txRetryFrame(target)) {
			if symbol == protocol.SymbolSyn || symbol == protocol.SymbolEscape {
				t.Fatalf("fixture telegram for target 0x%02X needs escaping; choose other bytes", target)
			}
		}
	}
	for _, data := range [][]byte{txRetryResponse, txRetryEscData, txRetrySynData} {
		for _, crcXor := range []byte{0, 0x01} {
			segment := txRetrySegment(data, crcXor)
			if crc := segment[len(segment)-1]; crc == protocol.SymbolSyn || crc == protocol.SymbolEscape {
				t.Fatalf("fixture response CRC 0x%02X for % X needs escaping; choose other bytes", crc, data)
			}
		}
	}
	if protocol.FrameTypeForTarget(txRetryTarget) != protocol.FrameTypeInitiatorTarget ||
		protocol.FrameTypeForTarget(txRetryPeer) != protocol.FrameTypeInitiatorInitiator {
		t.Fatalf("fixture targets 0x%02X and 0x%02X have the wrong frame types", txRetryTarget, txRetryPeer)
	}
}

func txRetryRunRow(t *testing.T, row txRetryRow) {
	t.Helper()

	frame := txRetryFrame(row.target)
	core := &txRetryCore{
		escapeEcho:    row.flavour.enhClass(),
		frame:         frame,
		echoAt:        row.echoAt,
		replies:       append([]txRetryRead(nil), row.replies...),
		arbErrs:       append([]error(nil), row.arbErrs...),
		tokens:        append([]uint64(nil), row.tokens...),
		reconnectErrs: append([]error(nil), row.reconnectErrs...),
	}
	var tr transport.RawTransport
	switch row.flavour {
	case txRetryPlain:
		tr = txRetryPlainTransport{txRetryLifecycle{core}}
	case txRetryPlainBare:
		tr = txRetryPlainBareTransport{core}
	case txRetryEnh:
		tr = txRetryEnhTransport{txRetryLifecycle{core}}
	case txRetryEnhSource:
		tr = txRetryEnhSourceTransport{txRetryEnhTransport{txRetryLifecycle{core}}}
	default:
		t.Fatalf("unknown flavour %d", row.flavour)
	}

	config := protocol.DefaultBusConfig()
	if row.policy != nil {
		config.InitiatorTarget = *row.policy
		config.InitiatorInitiator = *row.policy
	}
	config.Observer = txRetryObserver{core: core}
	config.ReconnectRetries = row.reconnectRetries
	config.ReconnectDelay = txRetryReconnectDelay
	bus := protocol.NewBus(tr, config, 4)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)

	// Without a deadline, a cancel timer bounds a broken row; a deadline
	// would make collision-class errors retry until it expires.
	var sendCtx context.Context
	var sendCancel context.CancelFunc
	if row.deadline {
		sendCtx, sendCancel = context.WithTimeout(context.Background(), 5*time.Second)
	} else {
		sendCtx, sendCancel = context.WithCancel(context.Background())
		guard := time.AfterFunc(5*time.Second, sendCancel)
		defer guard.Stop()
	}
	defer sendCancel()

	startedAt := time.Now()
	response, err := bus.Send(sendCtx, frame)
	elapsed := time.Since(startedAt)

	if row.wantSentinel == nil && err != nil {
		t.Fatalf("Send error = %v, want nil", err)
	}
	if row.wantSentinel != nil {
		if !errors.Is(err, row.wantSentinel) {
			t.Fatalf("Send error = %v, want errors.Is %v", err, row.wantSentinel)
		}
		if err.Error() != row.wantText {
			t.Fatalf("Send error text\n got: %q\nwant: %q", err.Error(), row.wantText)
		}
	}
	var wantResponse *protocol.Frame
	if row.wantResponse != nil {
		wantResponse = &protocol.Frame{
			Source: frame.Target, Target: frame.Source, Primary: frame.Primary, Secondary: frame.Secondary,
			Data: row.wantResponse,
		}
	}
	if !reflect.DeepEqual(response, wantResponse) {
		t.Fatalf("Send response = %+v, want %+v", response, wantResponse)
	}

	expected := &txRetryWant{flavour: row.flavour, frame: frame}
	row.want(expected)

	core.mu.Lock()
	defer core.mu.Unlock()
	if !reflect.DeepEqual(core.trace, expected.lines) {
		t.Fatalf("trace\n got:%s\nwant:%s", txRetryJoin(core.trace), txRetryJoin(expected.lines))
	}
	if len(core.faults) != 0 {
		t.Fatalf("observer faults: %q", core.faults)
	}
	if floor := txRetryMinElapsed(expected.lines); elapsed < floor {
		t.Fatalf("Send took %v, want at least %v from the retry delays", elapsed, floor)
	}
	if len(core.pending)+len(core.replies)+len(core.arbErrs)+len(core.tokens)+len(core.reconnectErrs) != 0 {
		t.Fatalf("unused script: echoes %d, replies %d, arbitration %d, tokens %d, reconnects %d",
			len(core.pending), len(core.replies), len(core.arbErrs), len(core.tokens), len(core.reconnectErrs))
	}
}

// txRetryMinElapsed is the least time the expected trace takes: the 50 ms
// collision floor per collision retry, the 200 ms delay per adapter-reset
// retry and the reconnect delay per reconnect. Only lower bounds are
// checked, so a slow machine cannot fail a row.
func txRetryMinElapsed(lines []string) time.Duration {
	var floor time.Duration
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "ev retry ") && strings.Contains(line, " r:collision"):
			floor += 50 * time.Millisecond
		case strings.HasPrefix(line, "ev retry ") && strings.Contains(line, " r:adapter_reset"):
			floor += 200 * time.Millisecond
		case line == "reconnect":
			floor += txRetryReconnectDelay
		}
	}
	return floor
}

func txRetryJoin(lines []string) string {
	return "\n\t" + strings.Join(lines, "\n\t")
}
