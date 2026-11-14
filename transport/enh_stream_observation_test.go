//go:build !tinygo

package transport

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// The tests in this file cover the units of the ENH stream observation and
// the INFO exchange one by one: the shared RECEIVED, STARTED and FAILED
// handlers (and that the steady-state read and the INFO batch both reach
// them), the RESETTED and parse-error handling of the steady-state read, the
// INFO request write, the INFO batch, the INFO payload collector and the
// fail-then-unlock step of RequestInfo. They reuse the scripted connection
// and state snapshot of the stream state table.

func esoMsg(command ENHCommand, data byte) ENHMessage {
	return ENHMessage{Command: command, Data: data}
}

// esoView is the part of the transport state the shared handlers change.
type esoView struct {
	queue []StreamEvent
	flags sstFlags
}

func esoViewOf(tr *ENHTransport) esoView {
	s := sstSnapshot(tr)
	return esoView{queue: s.queue, flags: s.flags}
}

type esoHandlerRow struct {
	name  string
	setup func(tr *ENHTransport)
	msgs  []ENHMessage
	want  esoView
}

func esoHandlerRows() []esoHandlerRow {
	closed := sstFlags{startWindow: sstClosed, postGrantWindow: sstClosed}
	with := func(f func(*sstFlags)) sstFlags {
		out := closed
		f(&out)
		return out
	}
	awaiting := with(func(f *sstFlags) { f.awaiting, f.initiator, f.startWindow = true, sstInitiator, sstOpen })
	granted := with(func(f *sstFlags) { f.initiator, f.postGrant, f.postGrantWindow = sstInitiator, true, sstRenewed })
	openStart := func(tr *ENHTransport) { sstOpenStartWindow(tr, sstOpenDeadline) }
	return []esoHandlerRow{
		{
			name: "received data without windows",
			msgs: []ENHMessage{esoMsg(ENHResReceived, 0x10)},
			want: esoView{queue: []StreamEvent{sstByte(0x10)}, flags: closed},
		},
		{
			name: "received SYN without windows",
			msgs: []ENHMessage{esoMsg(ENHResReceived, ebusSymbolSyn)},
			want: esoView{queue: []StreamEvent{sstByte(ebusSymbolSyn)}, flags: closed},
		},
		{
			name:  "received data inside the START window is dropped",
			setup: openStart,
			msgs:  []ENHMessage{esoMsg(ENHResReceived, 0x10)},
			want:  esoView{flags: awaiting},
		},
		{
			name:  "received SYN inside the START window is a wire SYN",
			setup: openStart,
			msgs:  []ENHMessage{esoMsg(ENHResReceived, ebusSymbolSyn)},
			want:  esoView{queue: []StreamEvent{sstWireSyn()}, flags: awaiting},
		},
		{
			name: "expired START window closes and resets a dropped escape lead",
			setup: func(tr *ENHTransport) {
				sstOpenStartWindow(tr, sstExpiredDeadline)
				sstPendEscape(tr)
			},
			msgs: []ENHMessage{esoMsg(ENHResReceived, 0x01)},
			want: esoView{queue: []StreamEvent{sstByte(0x01)}, flags: with(func(f *sstFlags) { f.initiator = sstInitiator })},
		},
		{
			name:  "post-grant window suppresses an idle SYN",
			setup: func(tr *ENHTransport) { sstOpenPostGrantWindow(tr, sstOpenDeadline) },
			msgs:  []ENHMessage{esoMsg(ENHResReceived, ebusSymbolSyn)},
			want:  esoView{flags: with(func(f *sstFlags) { f.postGrant, f.postGrantWindow = true, sstOpen })},
		},
		{
			name:  "post-grant window closes on the first data echo",
			setup: func(tr *ENHTransport) { sstOpenPostGrantWindow(tr, sstOpenDeadline) },
			msgs:  []ENHMessage{esoMsg(ENHResReceived, 0x10)},
			want:  esoView{queue: []StreamEvent{sstByte(0x10)}, flags: closed},
		},
		{
			name:  "expired post-grant window delivers the SYN and counts the expiry",
			setup: func(tr *ENHTransport) { sstOpenPostGrantWindow(tr, sstExpiredDeadline) },
			msgs:  []ENHMessage{esoMsg(ENHResReceived, ebusSymbolSyn)},
			want: esoView{
				queue: []StreamEvent{sstByte(ebusSymbolSyn)},
				flags: with(func(f *sstFlags) { f.expiredWindows = 1 }),
			},
		},
		{
			name:  "post-grant window passes an escape-decoded 0xAA",
			setup: func(tr *ENHTransport) { sstOpenPostGrantWindow(tr, sstOpenDeadline) },
			msgs:  []ENHMessage{esoMsg(ENHResReceived, 0xA9), esoMsg(ENHResReceived, 0x01)},
			want:  esoView{queue: []StreamEvent{sstEscaped(ebusSymbolSyn)}, flags: closed},
		},
		{
			name: "escape lead alone queues nothing and stays pending",
			msgs: []ENHMessage{esoMsg(ENHResReceived, 0xA9)},
			want: esoView{flags: with(func(f *sstFlags) { f.escapePending = true })},
		},
		{
			name: "requested escape reset is consumed before the next emission",
			setup: func(tr *ENHTransport) {
				sstPendEscape(tr)
				tr.escapeResetPending.Store(true)
			},
			msgs: []ENHMessage{esoMsg(ENHResReceived, 0x01)},
			want: esoView{queue: []StreamEvent{sstByte(0x01)}, flags: closed},
		},
		{
			name: "matching STARTED closes the START window and opens the post-grant window",
			setup: func(tr *ENHTransport) {
				openStart(tr)
				sstPendEscape(tr)
			},
			msgs: []ENHMessage{esoMsg(ENHResStarted, sstInitiator)},
			want: esoView{queue: []StreamEvent{sstStartedEv(sstInitiator)}, flags: granted},
		},
		{
			name: "mismatched STARTED keeps the START window and the escape state",
			setup: func(tr *ENHTransport) {
				openStart(tr)
				sstPendEscape(tr)
			},
			msgs: []ENHMessage{esoMsg(ENHResStarted, sstInitiator+1)},
			want: esoView{
				queue: []StreamEvent{sstStartedEv(sstInitiator + 1)},
				flags: with(func(f *sstFlags) {
					*f = awaiting
					f.escapePending = true
				}),
			},
		},
		{
			name: "STARTED without a START window opens the post-grant window",
			msgs: []ENHMessage{esoMsg(ENHResStarted, 0x71)},
			want: esoView{
				queue: []StreamEvent{sstStartedEv(0x71)},
				flags: with(func(f *sstFlags) { f.postGrant, f.postGrantWindow = true, sstRenewed }),
			},
		},
		{
			name: "FAILED closes both windows and the escape state",
			setup: func(tr *ENHTransport) {
				openStart(tr)
				sstOpenPostGrantWindow(tr, sstOpenDeadline)
				sstPendEscape(tr)
			},
			msgs: []ENHMessage{esoMsg(ENHResFailed, sstInitiator)},
			want: esoView{
				queue: []StreamEvent{sstFailedEv(sstInitiator)},
				flags: with(func(f *sstFlags) { f.initiator = sstInitiator }),
			},
		},
	}
}

func esoDispatch(tr *ENHTransport, msg ENHMessage) {
	switch msg.Command {
	case ENHResReceived:
		tr.observeReceivedLocked(msg)
	case ENHResStarted:
		tr.observeStartedLocked(msg)
	case ENHResFailed:
		tr.observeFailedLocked(msg)
	default:
		panic(fmt.Sprintf("no shared handler for command %v", msg.Command))
	}
}

func esoFrames(msgs []ENHMessage) []byte {
	var out []byte
	for _, msg := range msgs {
		frame := EncodeENH(msg.Command, msg.Data)
		out = append(out, frame[0], frame[1])
	}
	return out
}

// TestENHStreamObservationSharedHandlers drives each row through the shared
// handlers directly, through the steady-state read and through the INFO
// batch, and requires the same queue and window state from all three.
func TestENHStreamObservationSharedHandlers(t *testing.T) {
	for _, row := range esoHandlerRows() {
		row := row
		t.Run(row.name, func(t *testing.T) {
			paths := map[string]func(tr *ENHTransport){
				"handlers": func(tr *ENHTransport) {
					for _, msg := range row.msgs {
						esoDispatch(tr, msg)
					}
				},
				"steady-state read": func(tr *ENHTransport) {
					if err := tr.fillPendingLocked(); err != nil {
						t.Fatalf("fillPendingLocked = %v", err)
					}
				},
				"info batch": func(tr *ENHTransport) {
					c := newInfoPayloadCollector()
					reset, err := tr.observeInfoBatchLocked(row.msgs, &c)
					if reset || err != nil {
						t.Fatalf("observeInfoBatchLocked = %v, %v; want false, nil", reset, err)
					}
				},
			}
			for _, name := range []string{"handlers", "steady-state read", "info batch"} {
				conn := newSSTConn(sstChunk{data: esoFrames(row.msgs)})
				tr := sstNew(conn, 0)
				if row.setup != nil {
					row.setup(tr)
				}
				paths[name](tr)
				if got := esoViewOf(tr); !reflect.DeepEqual(got, row.want) {
					t.Fatalf("%s: state = %+v; want %+v", name, got, row.want)
				}
			}
		})
	}
}

func TestENHStreamObservationFillPendingResetted(t *testing.T) {
	t.Run("adapter-direct queues the boundary and keeps processing", func(t *testing.T) {
		conn := newSSTConn(sstChunk{data: sstFrames(sstRcv(0x10), sstReset(0x02), sstRcv(0x11))})
		tr := sstNew(conn, 0)
		if err := tr.fillPendingLocked(); err != nil {
			t.Fatalf("fillPendingLocked = %v", err)
		}
		want := []StreamEvent{sstByte(0x10), {Kind: StreamEventReset, Data: 0x02}, sstByte(0x11)}
		if got := sstSnapshot(tr); !reflect.DeepEqual(got.queue, want) || got.resets != 0 {
			t.Fatalf("queue = %v, resets = %d; want %v, 0", got.queue, got.resets, want)
		}
	})
	t.Run("adapter-direct closes the post-grant window and the escape state", func(t *testing.T) {
		tr := sstNew(newSSTConn(), 0)
		sstOpenPostGrantWindow(tr, sstOpenDeadline)
		sstPendEscape(tr)
		stop, err := tr.fillPendingResettedLocked(esoMsg(ENHResResetted, 0x05))
		if stop || err != nil {
			t.Fatalf("fillPendingResettedLocked = %v, %v; want false, nil", stop, err)
		}
		got := esoViewOf(tr)
		want := esoView{
			queue: []StreamEvent{{Kind: StreamEventReset, Data: 0x05}},
			flags: sstFlags{startWindow: sstClosed, postGrantWindow: sstClosed},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("state = %+v; want %+v", got, want)
		}
	})
	t.Run("reconnect stops the batch and counts one reset", func(t *testing.T) {
		dial, _ := sstDialINIT()
		conn := newSSTConn(sstChunk{data: sstFrames(sstReset(0x02), sstRcv(0x11))})
		tr := sstNew(conn, 0, WithDialFunc(dial))
		if err := tr.fillPendingLocked(); err != nil {
			t.Fatalf("fillPendingLocked = %v", err)
		}
		if got := sstSnapshot(tr); len(got.queue) != 0 || got.resets != 1 {
			t.Fatalf("queue = %v, resets = %d; want empty, 1", got.queue, got.resets)
		}
	})
	t.Run("failed reconnect stops the batch with the reconnect error", func(t *testing.T) {
		dial, _ := sstDialFailing()
		tr := sstNew(newSSTConn(), 0, WithDialFunc(dial))
		stop, err := tr.fillPendingResettedLocked(esoMsg(ENHResResetted, 0x02))
		if !stop {
			t.Fatalf("fillPendingResettedLocked stop = false; want true")
		}
		sstCheckErr(t, "fillPendingResettedLocked", err, ebuserrors.ErrTransportClosed, "enh adapter reset during read, reconnect failed")
	})
}

func TestENHStreamObservationFillPendingParseError(t *testing.T) {
	invalid := fmt.Errorf("scripted desync: %w", ebuserrors.ErrInvalidPayload)
	other := errors.New("scripted other parse failure")
	rows := []struct {
		name         string
		awaiting     bool
		msgs         []ENHMessage
		err          error
		wantErr      error
		wantDeferred bool
		wantAwaiting bool
		wantEscape   bool
		parserResets uint64
	}{
		{name: "desync without queued messages returns the error", err: invalid, wantErr: invalid, parserResets: 1},
		{name: "desync after queued messages defers the error", msgs: []ENHMessage{esoMsg(ENHResReceived, 0x10)}, err: invalid, wantDeferred: true, parserResets: 1},
		{name: "desync inside the START window returns the error", awaiting: true, msgs: []ENHMessage{esoMsg(ENHResReceived, 0x10)}, err: invalid, wantErr: invalid, parserResets: 1},
		{name: "other parse error leaves the state", awaiting: true, msgs: []ENHMessage{esoMsg(ENHResReceived, 0x10)}, err: other, wantErr: other, wantAwaiting: true, wantEscape: true},
	}
	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			tr := sstNew(newSSTConn(), 0)
			if row.awaiting {
				sstOpenStartWindow(tr, sstOpenDeadline)
			}
			sstPendEscape(tr)
			before := tr.enhParserResetsTotal.Load()
			if got := tr.fillPendingParseErrorLocked(row.err, row.msgs); got != row.wantErr {
				t.Fatalf("fillPendingParseErrorLocked = %v; want %v", got, row.wantErr)
			}
			if got := tr.deferredErr != nil; got != row.wantDeferred {
				t.Fatalf("deferred = %v; want %v", got, row.wantDeferred)
			}
			if row.wantDeferred && tr.deferredErr != row.err {
				t.Fatalf("deferredErr = %v; want %v", tr.deferredErr, row.err)
			}
			if got := tr.awaitingStart.Load(); got != row.wantAwaiting {
				t.Fatalf("awaitingStart = %v; want %v", got, row.wantAwaiting)
			}
			if got := tr.escDecoder.HasPendingEscape(); got != row.wantEscape {
				t.Fatalf("escape pending = %v; want %v", got, row.wantEscape)
			}
			if got := tr.enhParserResetsTotal.Load() - before; got != row.parserResets {
				t.Fatalf("parser resets = %d; want %d", got, row.parserResets)
			}
		})
	}
}

func TestENHStreamObservationInfoCollector(t *testing.T) {
	rows := []struct {
		name         string
		frames       []byte
		wantComplete bool
		wantPayload  []byte
		wantLen      int
	}{
		{name: "no frame", wantLen: -1},
		{name: "zero length completes with an empty payload", frames: []byte{0x00}, wantComplete: true, wantPayload: []byte{}, wantLen: 0},
		{name: "frames after a zero length are ignored", frames: []byte{0x00, 0x41}, wantComplete: true, wantPayload: []byte{}, wantLen: 0},
		{name: "length only", frames: []byte{0x03}, wantPayload: []byte{}, wantLen: 3},
		{name: "partial data", frames: []byte{0x03, 0x41, 0x42}, wantPayload: []byte{0x41, 0x42}, wantLen: 3},
		{name: "complete data", frames: []byte{0x02, 0x41, 0x42}, wantComplete: true, wantPayload: []byte{0x41, 0x42}, wantLen: 2},
		{name: "frames beyond the length are ignored", frames: []byte{0x02, 0x41, 0x42, 0x43}, wantComplete: true, wantPayload: []byte{0x41, 0x42}, wantLen: 2},
	}
	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			c := newInfoPayloadCollector()
			for _, b := range row.frames {
				c.add(b)
			}
			if c.complete != row.wantComplete || c.payloadLen != row.wantLen {
				t.Fatalf("complete, length = %v, %d; want %v, %d", c.complete, c.payloadLen, row.wantComplete, row.wantLen)
			}
			if !reflect.DeepEqual(c.payload, row.wantPayload) {
				t.Fatalf("payload = %#v; want %#v", c.payload, row.wantPayload)
			}
		})
	}
}

func TestENHStreamObservationInfoBatch(t *testing.T) {
	rows := []struct {
		name         string
		msgs         []ENHMessage
		wantReset    bool
		wantErr      error
		wantPrefix   string
		wantQueue    []StreamEvent
		wantComplete bool
	}{
		{
			name:         "INFO frames complete the payload and RECEIVED is queued",
			msgs:         []ENHMessage{esoMsg(ENHResInfo, 0x01), esoMsg(ENHResReceived, 0x10), esoMsg(ENHResInfo, 0x41)},
			wantQueue:    []StreamEvent{sstByte(0x10)},
			wantComplete: true,
		},
		{
			name:      "RESETTED before completion is reported",
			msgs:      []ENHMessage{esoMsg(ENHResInfo, 0x02), esoMsg(ENHResResetted, 0x01), esoMsg(ENHResReceived, 0x10)},
			wantReset: true,
			wantQueue: []StreamEvent{sstByte(0x10)},
		},
		{
			name:         "RESETTED after completion is not reported",
			msgs:         []ENHMessage{esoMsg(ENHResInfo, 0x00), esoMsg(ENHResResetted, 0x01)},
			wantComplete: true,
		},
		{
			name:       "eBUS error stops the batch at that message",
			msgs:       []ENHMessage{esoMsg(ENHResReceived, 0x10), esoMsg(ENHResErrorEBUS, 0x07), esoMsg(ENHResReceived, 0x11)},
			wantErr:    ebuserrors.ErrInvalidPayload,
			wantPrefix: "enh info ebus error 0x07",
			wantQueue:  []StreamEvent{sstByte(0x10)},
		},
		{
			name:       "host error stops the batch and keeps an earlier reset flag",
			msgs:       []ENHMessage{esoMsg(ENHResResetted, 0x01), esoMsg(ENHResErrorHost, 0x08), esoMsg(ENHResReceived, 0x11)},
			wantReset:  true,
			wantErr:    ebuserrors.ErrAdapterHostError,
			wantPrefix: "enh info host error 0x08",
		},
	}
	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			tr := sstNew(newSSTConn(), 0)
			c := newInfoPayloadCollector()
			reset, err := tr.observeInfoBatchLocked(row.msgs, &c)
			if reset != row.wantReset {
				t.Fatalf("resetBeforeCompletion = %v; want %v", reset, row.wantReset)
			}
			sstCheckErr(t, "observeInfoBatchLocked", err, row.wantErr, row.wantPrefix)
			if got := sstSnapshot(tr).queue; !reflect.DeepEqual(got, row.wantQueue) {
				t.Fatalf("queue = %v; want %v", got, row.wantQueue)
			}
			if c.complete != row.wantComplete {
				t.Fatalf("complete = %v; want %v", c.complete, row.wantComplete)
			}
		})
	}
}

func TestENHStreamObservationInfoRequestWrite(t *testing.T) {
	t.Run("writes one INFO request frame", func(t *testing.T) {
		conn := newSSTConn()
		tr := sstNew(conn, 0)
		if err := tr.writeInfoRequestLocked(AdapterInfoID(0x02)); err != nil {
			t.Fatalf("writeInfoRequestLocked = %v", err)
		}
		want := EncodeENH(ENHReqInfo, 0x02)
		if _, written := conn.state(); !reflect.DeepEqual(written, want[:]) {
			t.Fatalf("written = % x; want % x", written, want)
		}
	})
	t.Run("short write is incomplete", func(t *testing.T) {
		conn := newSSTConn()
		conn.shortWrite = true
		err := sstNew(conn, 0).writeInfoRequestLocked(AdapterInfoID(0x02))
		sstCheckErr(t, "writeInfoRequestLocked", err, ebuserrors.ErrInvalidPayload, "enh info request write incomplete")
	})
	t.Run("write error is mapped", func(t *testing.T) {
		conn := newSSTConn()
		conn.writeErr = errors.New("scripted write refused")
		err := sstNew(conn, 0).writeInfoRequestLocked(AdapterInfoID(0x02))
		sstCheckErr(t, "writeInfoRequestLocked", err, ebuserrors.ErrTransportClosed, "enh transport write failed: scripted write refused")
	})
}

func TestENHStreamObservationInfoFailUnlock(t *testing.T) {
	tr := sstNew(newSSTConn(), 0)
	tr.readMu.Lock()
	tr.writeMu.Lock()
	sstOpenStartWindow(tr, sstOpenDeadline)
	sstOpenPostGrantWindow(tr, sstOpenDeadline)
	sstPendEscape(tr)
	tr.pendingEvents = []StreamEvent{sstByte(0x10)}
	in := fmt.Errorf("scripted reset: %w", ebuserrors.ErrAdapterReset)
	if got := tr.requestInfoFailUnlock(in); got != in {
		t.Fatalf("requestInfoFailUnlock = %v; want %v", got, in)
	}
	if !tr.writeMu.TryLock() {
		t.Fatalf("writeMu still held")
	}
	tr.writeMu.Unlock()
	if !tr.readMu.TryLock() {
		t.Fatalf("readMu still held")
	}
	tr.readMu.Unlock()
	got := esoViewOf(tr)
	want := esoView{flags: sstFlags{initiator: sstInitiator, startWindow: sstClosed, postGrantWindow: sstClosed}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %+v; want %+v", got, want)
	}
}
