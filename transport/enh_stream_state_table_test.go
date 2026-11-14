//go:build !tinygo

package transport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// The tables in this file pin the observable outcomes of the ENH read path:
// the steady-state fill that ReadByte, ReadEvent and ReadEventWithRawSymbol
// share, the blocking START arbitration and the INFO exchange. They drive the
// transport over a scripted connection and assert the queued items, the window
// and escape-decoder state, the returned error text and the written frames.
// Window deadlines are placed through the atomics; no row waits for a window
// to expire.

const sstInitiator = byte(0x31)

// sstOpenDeadline is a window deadline far in the future: the window stays
// open for the whole row. sstExpiredDeadline is a non-zero deadline in the
// past: the window is open but expired when the next symbol arrives.
const (
	sstOpenDeadline    = int64(1) << 62
	sstExpiredDeadline = int64(1)
)

// Deadline classes reported by sstDeadlineClass.
const (
	sstClosed  = "closed"
	sstOpen    = "open"
	sstExpired = "expired"
	sstRenewed = "renewed"
)

var sstClock = time.Unix(1_000_000, 0)

func sstDeadlineClass(v int64) string {
	switch v {
	case 0:
		return sstClosed
	case sstOpenDeadline:
		return sstOpen
	case sstExpiredDeadline:
		return sstExpired
	default:
		return sstRenewed
	}
}

// sstTimeout is the timeout error the scripted connection returns once its
// script is exhausted.
type sstTimeout struct{}

func (sstTimeout) Error() string   { return "scripted read timeout" }
func (sstTimeout) Timeout() bool   { return true }
func (sstTimeout) Temporary() bool { return true }

var _ net.Error = sstTimeout{}

// sstChunk is one scripted Read result. hook runs at the start of the Read,
// while the transport holds its locks. untilDeadline makes the Read return
// zero bytes only after the read deadline set before it has passed.
type sstChunk struct {
	data          []byte
	err           error
	hook          func()
	untilDeadline bool
}

type sstConn struct {
	mu           sync.Mutex
	chunks       []sstChunk
	reads        int
	written      []byte
	writeErr     error
	shortWrite   bool
	readDeadline time.Time
	closed       bool
	// onWrite, when set, receives every written frame; the dial helper uses
	// it to answer INIT.
	onWrite func(c *sstConn, frame []byte)
}

func newSSTConn(chunks ...sstChunk) *sstConn {
	return &sstConn{chunks: chunks}
}

func (c *sstConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	c.reads++
	if len(c.chunks) == 0 {
		c.mu.Unlock()
		return 0, sstTimeout{}
	}
	chunk := c.chunks[0]
	c.chunks = c.chunks[1:]
	deadline := c.readDeadline
	c.mu.Unlock()
	if chunk.hook != nil {
		chunk.hook()
	}
	if chunk.untilDeadline {
		for !time.Now().After(deadline) {
			time.Sleep(time.Until(deadline) + 100*time.Microsecond)
		}
		return 0, nil
	}
	if len(chunk.data) > len(p) {
		panic(fmt.Sprintf("scripted chunk of %d bytes exceeds the read buffer of %d", len(chunk.data), len(p)))
	}
	return copy(p, chunk.data), chunk.err
}

func (c *sstConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if c.writeErr != nil {
		c.mu.Unlock()
		return 0, c.writeErr
	}
	if c.shortWrite {
		c.mu.Unlock()
		return 0, nil
	}
	c.written = append(c.written, p...)
	hook := c.onWrite
	c.mu.Unlock()
	if hook != nil {
		hook(c, append([]byte(nil), p...))
	}
	return len(p), nil
}

func (c *sstConn) push(chunk sstChunk) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chunks = append(c.chunks, chunk)
}

func (c *sstConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *sstConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *sstConn) LocalAddr() net.Addr              { return nil }
func (c *sstConn) RemoteAddr() net.Addr             { return nil }
func (c *sstConn) SetDeadline(time.Time) error      { return nil }
func (c *sstConn) SetWriteDeadline(time.Time) error { return nil }
func (c *sstConn) SetReadDeadline(d time.Time) error {
	c.mu.Lock()
	c.readDeadline = d
	c.mu.Unlock()
	return nil
}
func (c *sstConn) state() (reads int, written []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads, append([]byte(nil), c.written...)
}

func sstNew(conn net.Conn, readTimeout time.Duration, opts ...ENHTransportOption) *ENHTransport {
	tr := NewENHTransport(conn, readTimeout, time.Second, opts...)
	tr.escapeNow = func() time.Time { return sstClock }
	return tr
}

func sstFrames(frames ...[2]byte) []byte {
	out := make([]byte, 0, 2*len(frames))
	for _, frame := range frames {
		out = append(out, frame[0], frame[1])
	}
	return out
}

func sstRcv(b byte) [2]byte     { return EncodeENH(ENHResReceived, b) }
func sstStarted(b byte) [2]byte { return EncodeENH(ENHResStarted, b) }
func sstFailed(b byte) [2]byte  { return EncodeENH(ENHResFailed, b) }
func sstReset(b byte) [2]byte   { return EncodeENH(ENHResResetted, b) }
func sstInfo(b byte) [2]byte    { return EncodeENH(ENHResInfo, b) }
func sstEBUS(b byte) [2]byte    { return EncodeENH(ENHResErrorEBUS, b) }
func sstHost(b byte) [2]byte    { return EncodeENH(ENHResErrorHost, b) }

func sstByte(b byte) StreamEvent { return StreamEvent{Kind: StreamEventByte, Byte: b} }
func sstEscaped(b byte) StreamEvent {
	return StreamEvent{Kind: StreamEventByte, Byte: b, WasEscaped: true}
}
func sstWireSyn() StreamEvent { return StreamEvent{Kind: StreamEventWireSyn, Byte: ebusSymbolSyn} }
func sstStartedEv(b byte) StreamEvent {
	return StreamEvent{Kind: StreamEventStarted, Data: b}
}
func sstFailedEv(b byte) StreamEvent { return StreamEvent{Kind: StreamEventFailed, Data: b} }

// sstObs is one expected observation: an observed RECEIVED symbol (raw) with
// the logical item it completed (ev), a symbol that completed none (an escape
// lead), or a control item without a symbol.
type sstObs struct {
	raw    byte
	hasRaw bool
	ev     StreamEvent
	hasEv  bool
}

func sstSym(raw byte, ev StreamEvent) sstObs {
	return sstObs{raw: raw, hasRaw: true, ev: ev, hasEv: true}
}
func sstLead(raw byte) sstObs      { return sstObs{raw: raw, hasRaw: true} }
func sstCtl(ev StreamEvent) sstObs { return sstObs{ev: ev, hasEv: true} }

// sstQueue derives the pending queue the observations produce without and
// with raw-symbol capture.
func sstQueue(obs []sstObs, rawCapture bool) []StreamEvent {
	var out []StreamEvent
	for _, o := range obs {
		if rawCapture && o.hasRaw {
			raw := StreamEvent{Kind: streamEventRawSymbol, Byte: o.raw}
			if o.hasEv {
				raw.Data = rawSymbolCompletesNext
			}
			out = append(out, raw)
		}
		if o.hasEv {
			out = append(out, o.ev)
		}
	}
	return out
}

// sstFlags is the window and escape-decoder state after a read.
type sstFlags struct {
	awaiting        bool
	initiator       byte
	startWindow     string
	postGrant       bool
	postGrantWindow string
	escapePending   bool
	escapeReset     bool
	expiredWindows  uint64
}

type sstState struct {
	queue        []StreamEvent
	flags        sstFlags
	resets       int
	deferred     bool
	received     uint64
	parserResets uint64
}

// sstSnapshot reads the transport state. Call it only while no transport
// method runs.
func sstSnapshot(tr *ENHTransport) sstState {
	var queue []StreamEvent
	if len(tr.pendingEvents) > 0 {
		queue = append(queue, tr.pendingEvents...)
	}
	return sstState{
		queue: queue,
		flags: sstFlags{
			awaiting:        tr.awaitingStart.Load(),
			initiator:       byte(tr.awaitingStartInitiator.Load()),
			startWindow:     sstDeadlineClass(tr.arbitrationDeadline.Load()),
			postGrant:       tr.postGrantPreEcho.Load(),
			postGrantWindow: sstDeadlineClass(tr.postGrantPreEchoDeadline.Load()),
			escapePending:   tr.escDecoder.HasPendingEscape(),
			escapeReset:     tr.escapeResetPending.Load(),
			expiredWindows:  tr.postGrantWindowExpiredCount.Load(),
		},
		resets:       tr.resets,
		deferred:     tr.deferredErr != nil,
		received:     tr.receivedSymbolsTotal.Load(),
		parserResets: tr.enhParserResetsTotal.Load(),
	}
}

func sstOpenStartWindow(tr *ENHTransport, deadline int64) {
	tr.arbitrationDeadline.Store(deadline)
	tr.awaitingStartInitiator.Store(uint32(sstInitiator))
	tr.awaitingStart.Store(true)
}

func sstOpenPostGrantWindow(tr *ENHTransport, deadline int64) {
	tr.postGrantPreEchoDeadline.Store(deadline)
	tr.postGrantPreEcho.Store(true)
}

func sstPendEscape(tr *ENHTransport) {
	if _, ok, _, _ := tr.escDecoder.Feed(0xA9, tr.escapeNow()); ok || !tr.escDecoder.HasPendingEscape() {
		panic("escape lead did not leave the decoder pending")
	}
}

func sstCountReceived(stream [][2]byte) uint64 {
	var n uint64
	for _, frame := range stream {
		msg, err := DecodeENH(frame[0], frame[1])
		if err == nil && msg.Command == ENHResReceived {
			n++
		}
	}
	return n
}

type sstParityRow struct {
	name   string
	setup  func(tr *ENHTransport)
	stream [][2]byte
	want   []sstObs
	flags  sstFlags
}

func sstParityRows() []sstParityRow {
	closed := sstFlags{startWindow: sstClosed, postGrantWindow: sstClosed}
	with := func(f func(*sstFlags)) sstFlags {
		flags := closed
		f(&flags)
		return flags
	}
	return []sstParityRow{
		{
			name:   "no window",
			stream: [][2]byte{sstRcv(0x10), sstRcv(0xAA), sstRcv(0xA9), sstRcv(0x01), sstRcv(0xA9)},
			want: []sstObs{
				sstSym(0x10, sstByte(0x10)),
				sstSym(0xAA, sstByte(0xAA)),
				sstLead(0xA9),
				sstSym(0x01, sstEscaped(0xAA)),
				sstLead(0xA9),
			},
			flags: with(func(f *sstFlags) { f.escapePending = true }),
		},
		{
			name:   "no window, unsolicited STARTED opens the post-grant window",
			stream: [][2]byte{sstStarted(0x10), sstRcv(0xAA), sstRcv(0x20)},
			want:   []sstObs{sstCtl(sstStartedEv(0x10)), sstSym(0x20, sstByte(0x20))},
			flags:  closed,
		},
		{
			name:  "start window open, matching STARTED",
			setup: func(tr *ENHTransport) { sstOpenStartWindow(tr, sstOpenDeadline) },
			stream: [][2]byte{
				sstRcv(0x42), sstRcv(0xAA), sstRcv(0xA9), sstStarted(sstInitiator),
				sstRcv(0xAA), sstRcv(0x05), sstRcv(0xAA),
			},
			want: []sstObs{
				sstSym(0xAA, sstWireSyn()),
				sstCtl(sstStartedEv(sstInitiator)),
				sstSym(0x05, sstByte(0x05)),
				sstSym(0xAA, sstByte(0xAA)),
			},
			flags: with(func(f *sstFlags) { f.initiator = sstInitiator }),
		},
		{
			name:   "start window open, mismatched STARTED",
			setup:  func(tr *ENHTransport) { sstOpenStartWindow(tr, sstOpenDeadline) },
			stream: [][2]byte{sstStarted(0x77), sstRcv(0x42), sstRcv(0xAA), sstRcv(0xA9)},
			want:   []sstObs{sstCtl(sstStartedEv(0x77)), sstSym(0xAA, sstWireSyn())},
			flags: with(func(f *sstFlags) {
				f.awaiting, f.initiator, f.startWindow, f.escapePending = true, sstInitiator, sstOpen, true
			}),
		},
		{
			name: "start window expired",
			setup: func(tr *ENHTransport) {
				sstOpenStartWindow(tr, sstExpiredDeadline)
				sstPendEscape(tr)
			},
			stream: [][2]byte{sstRcv(0x01), sstRcv(0xAA)},
			want:   []sstObs{sstSym(0x01, sstByte(0x01)), sstSym(0xAA, sstByte(0xAA))},
			flags:  with(func(f *sstFlags) { f.initiator = sstInitiator }),
		},
		{
			name: "FAILED closes both windows",
			setup: func(tr *ENHTransport) {
				sstOpenStartWindow(tr, sstOpenDeadline)
				sstOpenPostGrantWindow(tr, sstOpenDeadline)
			},
			stream: [][2]byte{sstRcv(0xA9), sstFailed(0x10), sstRcv(0xAA), sstRcv(0x01)},
			want:   []sstObs{sstCtl(sstFailedEv(0x10)), sstSym(0xAA, sstByte(0xAA)), sstSym(0x01, sstByte(0x01))},
			flags:  with(func(f *sstFlags) { f.initiator = sstInitiator }),
		},
		{
			name:   "post-grant window open, closed by an escaped data byte",
			setup:  func(tr *ENHTransport) { sstOpenPostGrantWindow(tr, sstOpenDeadline) },
			stream: [][2]byte{sstRcv(0xAA), sstRcv(0xA9), sstRcv(0x01), sstRcv(0xAA)},
			want:   []sstObs{sstLead(0xA9), sstSym(0x01, sstEscaped(0xAA)), sstSym(0xAA, sstByte(0xAA))},
			flags:  closed,
		},
		{
			name:   "post-grant window open, idle SYN only",
			setup:  func(tr *ENHTransport) { sstOpenPostGrantWindow(tr, sstOpenDeadline) },
			stream: [][2]byte{sstRcv(0xAA), sstRcv(0xAA)},
			want:   nil,
			flags:  with(func(f *sstFlags) { f.postGrant, f.postGrantWindow = true, sstOpen }),
		},
		{
			name:   "post-grant window expired",
			setup:  func(tr *ENHTransport) { sstOpenPostGrantWindow(tr, sstExpiredDeadline) },
			stream: [][2]byte{sstRcv(0xAA), sstRcv(0xAA)},
			want:   []sstObs{sstSym(0xAA, sstByte(0xAA)), sstSym(0xAA, sstByte(0xAA))},
			flags:  with(func(f *sstFlags) { f.expiredWindows = 1 }),
		},
		{
			name: "aborted START requests an escape reset",
			setup: func(tr *ENHTransport) {
				sstPendEscape(tr)
				tr.escapeResetPending.Store(true)
			},
			stream: [][2]byte{sstRcv(0x01)},
			want:   []sstObs{sstSym(0x01, sstByte(0x01))},
			flags:  closed,
		},
	}
}

// TestENHStreamStateTableParity feeds one RECEIVED/STARTED/FAILED stream,
// followed by an INFO response, through the steady-state fill and through
// RequestInfo for every window state, without and with raw-symbol capture.
// Both paths must leave the same queue, windows and decoder state, and both
// must equal the row's fixed expectation.
func TestENHStreamStateTableParity(t *testing.T) {
	const infoData = byte(0x5A)
	for _, row := range sstParityRows() {
		for _, rawCapture := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/raw=%t", row.name, rawCapture), func(t *testing.T) {
				chunk := sstFrames(append(append([][2]byte{}, row.stream...), sstInfo(1), sstInfo(infoData))...)
				prepare := func() (*ENHTransport, *sstConn) {
					conn := newSSTConn(sstChunk{data: chunk})
					tr := sstNew(conn, time.Second)
					tr.rawSymbolItems = rawCapture
					if row.setup != nil {
						row.setup(tr)
					}
					return tr, conn
				}

				steady, steadyConn := prepare()
				steady.readMu.Lock()
				err := steady.fillPendingLocked()
				steady.readMu.Unlock()
				if err != nil {
					t.Fatalf("steady-state fill: %v", err)
				}
				steadyState := sstSnapshot(steady)
				if reads, written := steadyConn.state(); reads != 1 || len(written) != 0 {
					t.Fatalf("steady-state fill: reads=%d written=% X; want 1 read, no write", reads, written)
				}

				info, infoConn := prepare()
				payload, err := info.RequestInfo(AdapterInfoTemperature)
				if err != nil {
					t.Fatalf("RequestInfo: %v", err)
				}
				if !bytes.Equal(payload, []byte{infoData}) {
					t.Fatalf("RequestInfo payload = % X; want %02X", payload, infoData)
				}
				infoState := sstSnapshot(info)
				request := EncodeENH(ENHReqInfo, byte(AdapterInfoTemperature))
				if reads, written := infoConn.state(); reads != 1 || !bytes.Equal(written, request[:]) {
					t.Fatalf("RequestInfo: reads=%d written=% X; want 1 read, % X", reads, written, request)
				}

				want := sstState{
					queue:    sstQueue(row.want, rawCapture),
					flags:    row.flags,
					received: sstCountReceived(row.stream),
				}
				if !reflect.DeepEqual(steadyState, infoState) {
					t.Fatalf("paths differ\nsteady: %+v\n  info: %+v", steadyState, infoState)
				}
				if !reflect.DeepEqual(steadyState, want) {
					t.Fatalf("state\n got: %+v\nwant: %+v", steadyState, want)
				}
			})
		}
	}
}

type sstArbRow struct {
	name       string
	chunks     []sstChunk
	dial       func() (func() (net.Conn, error), *sstConn)
	writeErr   error
	shortWrite bool
	wantErr    error
	wantPrefix string
	reads      int
	// outcome: "granted" and "refused" end the arbitration (queue cleared,
	// post-grant window opened or closed); "aborted" is a read timeout or a
	// parse error (queue and post-grant window kept); "unsent" stops before
	// any read; "read-failed" returns a read error that is not a timeout.
	outcome string
	// stateReset marks rows whose RESETTED handling reset the transport
	// state (parser-only reset, or a completed reconnect).
	stateReset bool
}

// sstCheckErr asserts the exact error text: the row's prefix followed by the
// text of the wrapped sentinel, or the bare sentinel when the prefix is empty.
func sstCheckErr(t *testing.T, call string, err, sentinel error, prefix string) {
	t.Helper()
	if sentinel == nil {
		if err != nil {
			t.Fatalf("%s = %v; want nil", call, err)
		}
		return
	}
	want := sentinel.Error()
	if prefix != "" {
		want = prefix + ": " + want
	}
	if err == nil || !errors.Is(err, sentinel) || err.Error() != want {
		t.Fatalf("%s = %v; want %q wrapping %v", call, err, want, sentinel)
	}
}

func sstDialINIT() (func() (net.Conn, error), *sstConn) {
	fresh := newSSTConn()
	fresh.onWrite = func(c *sstConn, frame []byte) {
		if init := EncodeENH(ENHReqInit, 0x01); bytes.Equal(frame, init[:]) {
			c.push(sstChunk{data: sstFrames(sstReset(0x01))})
		}
	}
	return func() (net.Conn, error) { return fresh, nil }, fresh
}

func sstDialFailing() (func() (net.Conn, error), *sstConn) {
	return func() (net.Conn, error) { return nil, errors.New("scripted dial refused") }, nil
}

func sstOne(frames ...[2]byte) []sstChunk { return []sstChunk{{data: sstFrames(frames...)}} }

func sstArbRows() []sstArbRow {
	const resetText = "enh adapter reset during arbitration (features 0x03)"
	return []sstArbRow{
		{name: "matching STARTED", chunks: sstOne(sstStarted(sstInitiator)), reads: 1, outcome: "granted"},
		{name: "three mismatched STARTED", chunks: sstOne(sstStarted(0x40), sstStarted(0x41), sstStarted(0x42)), reads: 1, outcome: "refused",
			wantErr: ebuserrors.ErrInvalidPayload, wantPrefix: "enh arbitration started with wrong address 0x42 (expected 0x31, 3 mismatches)"},
		{name: "mismatch count spans reads", chunks: append(sstOne(sstStarted(0x40), sstStarted(0x41)), sstOne(sstStarted(0x43))...), reads: 2, outcome: "refused",
			wantErr: ebuserrors.ErrInvalidPayload, wantPrefix: "enh arbitration started with wrong address 0x43 (expected 0x31, 3 mismatches)"},
		{name: "two mismatched STARTED then the matching one", chunks: sstOne(sstStarted(0x40), sstStarted(0x41), sstStarted(sstInitiator)), reads: 1, outcome: "granted"},
		{name: "FAILED", chunks: sstOne(sstFailed(0x10)), reads: 1, outcome: "refused",
			wantErr: ebuserrors.ErrBusCollision, wantPrefix: "enh arbitration failed (initiator 0x31, winner 0x10)"},
		{name: "eBUS error", chunks: sstOne(sstEBUS(0x05)), reads: 1, outcome: "refused",
			wantErr: ebuserrors.ErrBusCollision, wantPrefix: "enh arbitration ebus error 0x05"},
		{name: "host error", chunks: sstOne(sstHost(0x06)), reads: 1, outcome: "refused",
			wantErr: ebuserrors.ErrAdapterHostError, wantPrefix: "enh arbitration host error 0x06"},
		{name: "RESETTED without a dial function", chunks: sstOne(sstReset(0x03)), reads: 1, outcome: "refused", stateReset: true,
			wantErr: ebuserrors.ErrAdapterReset, wantPrefix: resetText},
		{name: "RESETTED with a dial function", chunks: sstOne(sstReset(0x03)), dial: sstDialINIT, reads: 1, outcome: "refused", stateReset: true,
			wantErr: ebuserrors.ErrAdapterReset, wantPrefix: resetText},
		{name: "RESETTED with a failing dial function", chunks: sstOne(sstReset(0x03)), dial: sstDialFailing, reads: 1, outcome: "refused",
			wantErr: ebuserrors.ErrTransportClosed, wantPrefix: "enh adapter reset during arbitration, reconnect failed"},
		{name: "read timeout", chunks: sstOne(sstRcv(0xA9)), reads: 2, outcome: "aborted",
			wantErr: ebuserrors.ErrTimeout, wantPrefix: "enh transport read timeout"},
		{name: "read closed", chunks: []sstChunk{{err: io.EOF}}, reads: 1, outcome: "read-failed",
			wantErr: ebuserrors.ErrTransportClosed, wantPrefix: "enh transport read closed"},
		{name: "parse error after a valid STARTED", chunks: []sstChunk{{data: append(sstFrames(sstStarted(sstInitiator)), 0x80)}}, reads: 1, outcome: "granted"},
		{name: "parse error after a mismatched STARTED", chunks: []sstChunk{{data: append(sstFrames(sstStarted(0x40)), 0x80)}}, reads: 1, outcome: "aborted",
			wantErr: ebuserrors.ErrInvalidPayload},
		{name: "a later error-setting message wins", chunks: sstOne(sstFailed(0x10), sstHost(0x06)), reads: 1, outcome: "refused",
			wantErr: ebuserrors.ErrAdapterHostError, wantPrefix: "enh arbitration host error 0x06"},
		{name: "a matching STARTED does not clear an earlier error", chunks: sstOne(sstFailed(0x10), sstStarted(sstInitiator)), reads: 1, outcome: "refused",
			wantErr: ebuserrors.ErrBusCollision, wantPrefix: "enh arbitration failed (initiator 0x31, winner 0x10)"},
		{name: "a matching STARTED after RESETTED keeps the reset error", chunks: sstOne(sstReset(0x03), sstStarted(sstInitiator)), reads: 1, outcome: "refused", stateReset: true,
			wantErr: ebuserrors.ErrAdapterReset, wantPrefix: resetText},
		{name: "RECEIVED around a matching STARTED in one read is discarded", chunks: sstOne(sstRcv(0x42), sstRcv(0xAA), sstStarted(sstInitiator), sstRcv(0x43)), reads: 1, outcome: "granted"},
		{name: "write error", writeErr: errors.New("scripted write refused"), outcome: "unsent",
			wantErr: ebuserrors.ErrTransportClosed, wantPrefix: "enh transport write failed: scripted write refused"},
		{name: "short write", shortWrite: true, outcome: "unsent", wantErr: ebuserrors.ErrInvalidPayload},
	}
}

// sstArbWant derives the state StartArbitration leaves from the state before
// it and the row's outcome. Symbol and parser-reset counters are not compared.
func sstArbWant(before sstState, row sstArbRow) sstState {
	want := before
	want.received, want.parserResets = 0, 0
	switch row.outcome {
	case "granted", "refused":
		want.queue = nil
		want.deferred = false
		want.flags.awaiting = false
		want.flags.escapePending = false
		if row.outcome == "granted" {
			want.flags.postGrant, want.flags.postGrantWindow = true, sstRenewed
		} else {
			want.flags.postGrant, want.flags.postGrantWindow = false, sstClosed
		}
	case "aborted":
		want.deferred = false
		want.flags.awaiting = false
		want.flags.escapePending = false
	}
	if row.stateReset {
		// The RESETTED handling reset the transport state, which also
		// clears the async window deadline that arbitration keeps.
		want.flags.startWindow = sstClosed
	}
	return want
}

// TestENHStreamStateTableStartArbitration pins every outcome of the blocking
// START arbitration, including multi-message reads, against a transport that
// starts with a queued byte, an open async window, an open post-grant window,
// a pending escape lead and a deferred error.
func TestENHStreamStateTableStartArbitration(t *testing.T) {
	stale := errors.New("stale deferred error")
	for _, row := range sstArbRows() {
		t.Run(row.name, func(t *testing.T) {
			conn := newSSTConn(row.chunks...)
			conn.writeErr = row.writeErr
			conn.shortWrite = row.shortWrite
			var opts []ENHTransportOption
			var fresh *sstConn
			if row.dial != nil {
				var dial func() (net.Conn, error)
				dial, fresh = row.dial()
				opts = append(opts, WithDialFunc(dial))
			}
			tr := sstNew(conn, time.Second, opts...)
			tr.pendingEvents = []StreamEvent{sstByte(0x99)}
			sstOpenStartWindow(tr, sstOpenDeadline)
			sstOpenPostGrantWindow(tr, sstOpenDeadline)
			sstPendEscape(tr)
			tr.deferredErr = stale
			before := sstSnapshot(tr)

			sstCheckErr(t, "StartArbitration", tr.StartArbitration(sstInitiator), row.wantErr, row.wantPrefix)

			reads, written := conn.state()
			if reads != row.reads {
				t.Fatalf("reads = %d; want %d", reads, row.reads)
			}
			start := EncodeENH(ENHReqStart, sstInitiator)
			wantWritten := start[:]
			if row.outcome == "unsent" {
				wantWritten = nil
			}
			if !bytes.Equal(written, wantWritten) {
				t.Fatalf("written = % X; want % X", written, wantWritten)
			}
			if !tr.readMu.TryLock() || !tr.writeMu.TryLock() {
				t.Fatal("a transport lock is still held")
			}
			tr.writeMu.Unlock()
			tr.readMu.Unlock()

			got, want := sstSnapshot(tr), sstArbWant(before, row)
			got.received, got.parserResets = 0, 0
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("state\n got: %+v\nwant: %+v", got, want)
			}

			if fresh != nil {
				if !conn.isClosed() {
					t.Fatal("the replaced connection was not closed")
				}
				if tr.conn != fresh {
					t.Fatal("the transport does not use the dialled connection")
				}
				init := EncodeENH(ENHReqInit, 0x01)
				if _, freshWritten := fresh.state(); !bytes.Equal(freshWritten, init[:]) {
					t.Fatalf("dialled connection written = % X; want INIT % X", freshWritten, init)
				}
			}
		})
	}
}

type sstInfoRow struct {
	name        string
	readTimeout time.Duration
	setup       func(tr *ENHTransport)
	chunks      []sstChunk
	writeErr    error
	shortWrite  bool
	payload     []byte
	wantErr     error
	wantPrefix  string
	reads       int
	queue       []StreamEvent
	resets      int
	// parserResets counts ENH parser resets: one for the failure cleanup,
	// one more for every RESETTED state reset.
	parserResets uint64
	// escapePending is the decoder state after the exchange; a failure
	// resets the decoder unless it is a timeout with no async window open.
	escapePending bool
}

func sstInfoRows() []sstInfoRow {
	const timeoutText = "enh transport read timeout"
	queued := func(b byte) []StreamEvent { return []StreamEvent{sstByte(b)} }
	staged := func(tr *ENHTransport) { tr.pendingEvents = queued(0x99); sstPendEscape(tr) }
	return []sstInfoRow{
		{name: "zero length", chunks: sstOne(sstInfo(0)), payload: []byte{}, reads: 1},
		{name: "length then data in one read", chunks: sstOne(sstInfo(2), sstInfo(0x11), sstInfo(0x22)), payload: []byte{0x11, 0x22}, reads: 1},
		{name: "length then data across reads", chunks: append(sstOne(sstInfo(2), sstInfo(0x11)), sstOne(sstInfo(0x22))...), payload: []byte{0x11, 0x22}, reads: 2},
		{name: "trailing RECEIVED in the completing read is queued", chunks: sstOne(sstInfo(1), sstInfo(0x11), sstRcv(0x42)),
			payload: []byte{0x11}, reads: 1, queue: queued(0x42)},
		{name: "trailing RESETTED after completion surfaces the reset", chunks: sstOne(sstRcv(0x42), sstInfo(1), sstInfo(0x11), sstReset(0x01)),
			payload: []byte{0x11}, reads: 1, resets: 1, parserResets: 1},
		{name: "RESETTED before completion", chunks: sstOne(sstRcv(0x42), sstInfo(2), sstInfo(0x11), sstReset(0x01)), reads: 1, resets: 1, parserResets: 2,
			wantErr: ebuserrors.ErrAdapterReset, wantPrefix: "enh adapter resetted during info request"},
		{name: "eBUS error keeps the queued bytes", chunks: sstOne(sstRcv(0x42), sstRcv(0xA9), sstEBUS(0x05)), reads: 1, queue: queued(0x42),
			wantErr: ebuserrors.ErrInvalidPayload, wantPrefix: "enh info ebus error 0x05"},
		{name: "host error", chunks: sstOne(sstRcv(0xA9), sstHost(0x06)), reads: 1,
			wantErr: ebuserrors.ErrAdapterHostError, wantPrefix: "enh info host error 0x06"},
		{name: "read timeout keeps a pending escape lead", chunks: sstOne(sstRcv(0x42), sstRcv(0xA9)), reads: 2, queue: queued(0x42), escapePending: true,
			wantErr: ebuserrors.ErrTimeout, wantPrefix: timeoutText},
		{name: "read timeout with the async window open resets the escape decoder", setup: func(tr *ENHTransport) { sstOpenStartWindow(tr, sstOpenDeadline) },
			chunks: sstOne(sstRcv(0xA9)), reads: 2, wantErr: ebuserrors.ErrTimeout, wantPrefix: timeoutText},
		{name: "exchange deadline", readTimeout: 2 * time.Millisecond, chunks: append(sstOne(sstRcv(0xA9)), sstChunk{untilDeadline: true}), reads: 2, escapePending: true,
			wantErr: ebuserrors.ErrTimeout, wantPrefix: "enh info exchange deadline exceeded"},
		{name: "read closed drops the queue", chunks: append(sstOne(sstRcv(0x42)), sstChunk{err: io.EOF}), reads: 2,
			wantErr: ebuserrors.ErrTransportClosed, wantPrefix: "enh transport read closed"},
		{name: "parse error", chunks: []sstChunk{{data: append(sstFrames(sstRcv(0x42), sstRcv(0xA9)), 0x80)}}, reads: 1, queue: queued(0x42),
			wantErr: ebuserrors.ErrInvalidPayload},
		{name: "write error drops the queue", setup: staged, writeErr: errors.New("scripted write refused"),
			wantErr: ebuserrors.ErrTransportClosed, wantPrefix: "enh transport write failed: scripted write refused"},
		{name: "short write keeps the queue", setup: staged, shortWrite: true, queue: queued(0x99),
			wantErr: ebuserrors.ErrInvalidPayload, wantPrefix: "enh info request write incomplete"},
	}
}

// TestENHStreamStateTableRequestInfo pins the INFO payload collection and
// every failure exit: the error text, the kept or dropped queue, the escape
// decoder rule and the cleared windows.
func TestENHStreamStateTableRequestInfo(t *testing.T) {
	for _, row := range sstInfoRows() {
		t.Run(row.name, func(t *testing.T) {
			conn := newSSTConn(row.chunks...)
			conn.writeErr = row.writeErr
			conn.shortWrite = row.shortWrite
			readTimeout := row.readTimeout
			if readTimeout == 0 {
				readTimeout = time.Second
			}
			tr := sstNew(conn, readTimeout)
			if row.setup != nil {
				row.setup(tr)
			}
			if row.wantErr != nil {
				// Every failure must close windows that were open before.
				sstOpenPostGrantWindow(tr, sstOpenDeadline)
				tr.deferredErr = errors.New("stale deferred error")
			}
			before := sstSnapshot(tr)

			payload, err := tr.RequestInfo(AdapterInfoTemperature)
			sstCheckErr(t, "RequestInfo", err, row.wantErr, row.wantPrefix)
			if (payload == nil) != (row.wantErr != nil) || !bytes.Equal(payload, row.payload) {
				t.Fatalf("payload = % X (nil=%t); want % X", payload, payload == nil, row.payload)
			}

			reads, written := conn.state()
			if reads != row.reads {
				t.Fatalf("reads = %d; want %d", reads, row.reads)
			}
			request := EncodeENH(ENHReqInfo, byte(AdapterInfoTemperature))
			wantWritten := request[:]
			if row.writeErr != nil || row.shortWrite {
				wantWritten = nil
			}
			if !bytes.Equal(written, wantWritten) {
				t.Fatalf("written = % X; want % X", written, wantWritten)
			}
			if !tr.readMu.TryLock() || !tr.writeMu.TryLock() {
				t.Fatal("a transport lock is still held")
			}
			tr.writeMu.Unlock()
			tr.readMu.Unlock()

			got := sstSnapshot(tr)
			if !reflect.DeepEqual(got.queue, row.queue) {
				t.Fatalf("queue = %+v; want %+v", got.queue, row.queue)
			}
			if got.resets != row.resets {
				t.Fatalf("resets = %d; want %d", got.resets, row.resets)
			}
			if got.flags.escapePending != row.escapePending {
				t.Fatalf("escape pending = %t; want %t", got.flags.escapePending, row.escapePending)
			}
			if row.wantErr != nil {
				wantFlags := sstFlags{
					initiator:       before.flags.initiator,
					startWindow:     sstClosed,
					postGrantWindow: sstClosed,
					escapePending:   row.escapePending,
				}
				if got.flags != wantFlags || got.deferred {
					t.Fatalf("after failure flags = %+v deferred=%t; want %+v, no deferred error", got.flags, got.deferred, wantFlags)
				}
			}
			wantParserResets := row.parserResets
			if row.wantErr != nil && wantParserResets == 0 {
				wantParserResets = 1
			}
			if got.parserResets != before.parserResets+wantParserResets {
				t.Fatalf("parser resets = %d; want %d", got.parserResets-before.parserResets, wantParserResets)
			}
		})
	}
}

// TestENHStreamStateTableInfoCleanupPrecedesWriteUnlock pins that a failing
// INFO exchange cleans up before it releases the write lock: the first writer
// that acquires the write lock after the failure already sees the closed
// windows and the parser reset, and a START window it opens is not undone.
func TestENHStreamStateTableInfoCleanupPrecedesWriteUnlock(t *testing.T) {
	rows := []struct {
		name         string
		chunk        sstChunk
		want         error
		parserResets uint64
	}{
		{name: "host error", chunk: sstChunk{data: sstFrames(sstHost(0x06))}, want: ebuserrors.ErrAdapterHostError, parserResets: 1},
		{name: "RESETTED before completion", chunk: sstChunk{data: sstFrames(sstInfo(2), sstReset(0x01))}, want: ebuserrors.ErrAdapterReset, parserResets: 2},
		{name: "read timeout", chunk: sstChunk{err: sstTimeout{}}, want: ebuserrors.ErrTimeout, parserResets: 1},
		{name: "parse error", chunk: sstChunk{data: []byte{0x80}}, want: ebuserrors.ErrInvalidPayload, parserResets: 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			type seen struct {
				awaiting     bool
				startWindow  int64
				postGrant    bool
				parserResets uint64
			}
			conn := newSSTConn()
			tr := sstNew(conn, time.Second)
			sstOpenStartWindow(tr, sstOpenDeadline)
			sstOpenPostGrantWindow(tr, sstOpenDeadline)
			parserResets := tr.enhParserResetsTotal.Load()

			var observed seen
			var heldDuringRead bool
			waiting := make(chan struct{})
			done := make(chan struct{})
			chunk := row.chunk
			chunk.hook = func() {
				heldDuringRead = !tr.writeMu.TryLock() && !tr.readMu.TryLock()
				go func() {
					close(waiting)
					tr.writeMu.Lock()
					observed = seen{
						awaiting:     tr.awaitingStart.Load(),
						startWindow:  tr.arbitrationDeadline.Load(),
						postGrant:    tr.postGrantPreEcho.Load(),
						parserResets: tr.enhParserResetsTotal.Load(),
					}
					tr.writeMu.Unlock()
					close(done)
				}()
				<-waiting
			}
			conn.push(chunk)

			if _, err := tr.RequestInfo(AdapterInfoTemperature); !errors.Is(err, row.want) {
				t.Fatalf("RequestInfo = %v; want %v", err, row.want)
			}
			<-done
			if !heldDuringRead {
				t.Fatal("RequestInfo did not hold both locks while reading")
			}
			want := seen{parserResets: parserResets + row.parserResets}
			if observed != want {
				t.Fatalf("first writer after the failure saw %+v; want %+v", observed, want)
			}

			if err := tr.RequestStart(sstInitiator); err != nil {
				t.Fatalf("RequestStart = %v", err)
			}
			if !tr.awaitingStart.Load() || sstDeadlineClass(tr.arbitrationDeadline.Load()) != sstRenewed {
				t.Fatal("the START window opened after the failed exchange was undone")
			}
		})
	}
}
