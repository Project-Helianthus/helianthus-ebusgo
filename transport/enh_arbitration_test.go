//go:build !tinygo

package transport

import (
	"errors"
	"net"
	"reflect"
	"testing"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// The tests in this file cover the steps of the blocking START arbitration
// one by one: the START request write, the pure outcome classifier, the
// per-read batch handling (RECEIVED discard and RESETTED reconnect included)
// and the completion step. They reuse the scripted connection and state
// snapshot of the stream state table.

type earClassifyRow struct {
	name      string
	command   ENHCommand
	data      byte
	initiator byte
	count     int
	done      bool
	wantErr   error
	wantText  string
	wantCount int
}

// earClassifyRows varies every input of the classifier: the command (every
// ENH response ID), the data byte, the initiator and the mismatch count.
func earClassifyRows() []earClassifyRow {
	return []earClassifyRow{
		{name: "matching STARTED", command: ENHResStarted, data: 0x31, initiator: 0x31, done: true},
		{name: "matching STARTED for another initiator", command: ENHResStarted, data: 0x10, initiator: 0x10, done: true},
		{name: "matching STARTED keeps the mismatch count", command: ENHResStarted, data: 0x31, initiator: 0x31, count: 2, done: true, wantCount: 2},
		{name: "first mismatched STARTED", command: ENHResStarted, data: 0x40, initiator: 0x31, wantCount: 1},
		{name: "mismatched STARTED when the data is the other initiator", command: ENHResStarted, data: 0x31, initiator: 0x10, wantCount: 1},
		{name: "second mismatched STARTED", command: ENHResStarted, data: 0x40, initiator: 0x31, count: 1, wantCount: 2},
		{name: "third mismatched STARTED", command: ENHResStarted, data: 0x42, initiator: 0x31, count: 2, done: true, wantCount: 3,
			wantErr: ebuserrors.ErrInvalidPayload, wantText: "enh arbitration started with wrong address 0x42 (expected 0x31, 3 mismatches)"},
		{name: "mismatched STARTED above the limit", command: ENHResStarted, data: 0x77, initiator: 0x10, count: 5, done: true, wantCount: 6,
			wantErr: ebuserrors.ErrInvalidPayload, wantText: "enh arbitration started with wrong address 0x77 (expected 0x10, 6 mismatches)"},
		{name: "FAILED", command: ENHResFailed, data: 0x10, initiator: 0x31, count: 1, done: true, wantCount: 1,
			wantErr: ebuserrors.ErrBusCollision, wantText: "enh arbitration failed (initiator 0x31, winner 0x10)"},
		{name: "FAILED naming the requester", command: ENHResFailed, data: 0x31, initiator: 0x03, done: true,
			wantErr: ebuserrors.ErrBusCollision, wantText: "enh arbitration failed (initiator 0x03, winner 0x31)"},
		{name: "eBUS error", command: ENHResErrorEBUS, data: 0x05, initiator: 0x31, count: 2, done: true, wantCount: 2,
			wantErr: ebuserrors.ErrBusCollision, wantText: "enh arbitration ebus error 0x05"},
		{name: "host error", command: ENHResErrorHost, data: 0x06, initiator: 0x31, done: true,
			wantErr: ebuserrors.ErrAdapterHostError, wantText: "enh arbitration host error 0x06"},
		{name: "INFO is ignored", command: ENHResInfo, data: 0x31, initiator: 0x31, count: 1, wantCount: 1},
		{name: "RECEIVED is not classified", command: ENHResReceived, data: 0x31, initiator: 0x31, count: 2, wantCount: 2},
		{name: "RESETTED is not classified", command: ENHResResetted, data: 0x31, initiator: 0x31},
	}
}

// TestENHArbitrationClassify pins the pure outcome classifier. Field coverage
// and the production change each field catches:
//   - command: the STARTED, FAILED, eBUS-error and host-error rows fail if a
//     case label, sentinel or text is swapped; the INFO, RECEIVED and
//     RESETTED rows fail if the classifier claims a message it must not;
//   - data: the mismatched rows fail if the comparison ignores the data byte,
//     and the texts fail if the winner or wrong address is not the data byte;
//   - initiator: the rows with initiator 0x10 and 0x03 fail if the comparison
//     or the texts use a fixed address instead of the requested initiator;
//   - count: the first, second, third and above-the-limit rows fail if the
//     increment, the limit of three or the >= comparison changes, and the
//     carried counts fail if a non-STARTED message or a grant touches it.
func TestENHArbitrationClassify(t *testing.T) {
	for _, row := range earClassifyRows() {
		t.Run(row.name, func(t *testing.T) {
			msg := ENHMessage{Command: row.command, Data: row.data}
			got, count := classifyArbitrationMessage(msg, row.initiator, row.count)
			if got.done != row.done {
				t.Fatalf("done = %v; want %v", got.done, row.done)
			}
			if count != row.wantCount {
				t.Fatalf("mismatch count = %d; want %d", count, row.wantCount)
			}
			if row.wantErr == nil {
				if got.err != nil {
					t.Fatalf("err = %v; want nil", got.err)
				}
				return
			}
			want := row.wantText + ": " + row.wantErr.Error()
			if got.err == nil || !errors.Is(got.err, row.wantErr) || got.err.Error() != want {
				t.Fatalf("err = %v; want %q wrapping %v", got.err, want, row.wantErr)
			}
		})
	}
}

type earBatchRow struct {
	name      string
	dial      func() (func() (net.Conn, error), *sstConn)
	msgs      []ENHMessage
	count     int
	done      bool
	wantErr   error
	wantText  string
	wantCount int
	// escapePending is the decoder state after the batch; the transport
	// starts with a pending escape lead.
	escapePending bool
}

// TestENHArbitrationBatch pins the per-read handling: every message of a read
// is processed, a later error-setting message wins, a matching STARTED does
// not clear an earlier error, the mismatch count carries across messages,
// RECEIVED only advances the escape decoder and RESETTED reconnects in place.
func TestENHArbitrationBatch(t *testing.T) {
	const resetText = "enh adapter reset during arbitration (features 0x03)"
	rows := []earBatchRow{
		{name: "empty read", escapePending: true},
		{name: "RECEIVED only advances the escape decoder", msgs: []ENHMessage{esoMsg(ENHResReceived, 0x01)}},
		{name: "RECEIVED escape lead stays pending", msgs: []ENHMessage{esoMsg(ENHResReceived, 0x01), esoMsg(ENHResReceived, 0xA9)}, escapePending: true},
		{name: "matching STARTED", msgs: []ENHMessage{esoMsg(ENHResStarted, sstInitiator)}, done: true, escapePending: true},
		{name: "mismatch count carries across messages", count: 1,
			msgs: []ENHMessage{esoMsg(ENHResStarted, 0x40), esoMsg(ENHResStarted, 0x41)}, done: true, wantCount: 3, escapePending: true,
			wantErr: ebuserrors.ErrInvalidPayload, wantText: "enh arbitration started with wrong address 0x41 (expected 0x31, 3 mismatches)"},
		{name: "a later error-setting message wins", msgs: []ENHMessage{esoMsg(ENHResFailed, 0x10), esoMsg(ENHResErrorHost, 0x06)},
			done: true, escapePending: true, wantErr: ebuserrors.ErrAdapterHostError, wantText: "enh arbitration host error 0x06"},
		{name: "a matching STARTED does not clear an earlier error", msgs: []ENHMessage{esoMsg(ENHResFailed, 0x10), esoMsg(ENHResStarted, sstInitiator)},
			done: true, escapePending: true, wantErr: ebuserrors.ErrBusCollision, wantText: "enh arbitration failed (initiator 0x31, winner 0x10)"},
		{name: "RESETTED without a dial function resets state", msgs: []ENHMessage{esoMsg(ENHResResetted, 0x03)},
			done: true, wantErr: ebuserrors.ErrAdapterReset, wantText: resetText},
		{name: "RESETTED with a dial function reconnects", dial: sstDialINIT, msgs: []ENHMessage{esoMsg(ENHResResetted, 0x03)},
			done: true, wantErr: ebuserrors.ErrAdapterReset, wantText: resetText},
		{name: "RESETTED with a failing dial function", dial: sstDialFailing, msgs: []ENHMessage{esoMsg(ENHResResetted, 0x03)},
			done: true, escapePending: true, wantErr: ebuserrors.ErrTransportClosed, wantText: "enh adapter reset during arbitration, reconnect failed"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var opts []ENHTransportOption
			if row.dial != nil {
				dial, _ := row.dial()
				opts = append(opts, WithDialFunc(dial))
			}
			tr := sstNew(newSSTConn(), 0, opts...)
			sstPendEscape(tr)
			tr.readMu.Lock()
			got, count := tr.observeArbitrationBatchLocked(row.msgs, sstInitiator, row.count)
			tr.readMu.Unlock()
			if got.done != row.done {
				t.Fatalf("done = %v; want %v", got.done, row.done)
			}
			if count != row.wantCount {
				t.Fatalf("mismatch count = %d; want %d", count, row.wantCount)
			}
			if tr.pendingEvents != nil {
				t.Fatalf("queue = %+v; want nothing queued", tr.pendingEvents)
			}
			if pending := tr.escDecoder.HasPendingEscape(); pending != row.escapePending {
				t.Fatalf("escape pending = %v; want %v", pending, row.escapePending)
			}
			if row.wantErr == nil {
				if got.err != nil {
					t.Fatalf("err = %v; want nil", got.err)
				}
				return
			}
			sstCheckErr(t, "observeArbitrationBatchLocked", got.err, row.wantErr, row.wantText)
		})
	}
}

// TestENHArbitrationComplete pins the completion step: it clears the parser,
// queue, deferred error, async window and escape decoder, opens the post-grant
// window only on success and returns the outcome error unchanged.
func TestENHArbitrationComplete(t *testing.T) {
	refused := errors.New("scripted refusal")
	for _, row := range []struct {
		name      string
		in        error
		postGrant bool
		window    string
	}{
		{name: "granted opens the post-grant window", postGrant: true, window: sstRenewed},
		{name: "refused closes the post-grant window", in: refused, window: sstClosed},
	} {
		t.Run(row.name, func(t *testing.T) {
			tr := sstNew(newSSTConn(), 0)
			tr.pendingEvents = []StreamEvent{sstByte(0x99)}
			tr.deferredErr = errors.New("stale deferred error")
			sstOpenStartWindow(tr, sstOpenDeadline)
			sstOpenPostGrantWindow(tr, sstExpiredDeadline)
			sstPendEscape(tr)
			before := sstSnapshot(tr)

			tr.readMu.Lock()
			got := tr.completeArbitrationLocked(row.in)
			tr.readMu.Unlock()
			if got != row.in {
				t.Fatalf("completeArbitrationLocked = %v; want %v", got, row.in)
			}

			want := before
			want.queue, want.deferred = nil, false
			want.flags.awaiting, want.flags.escapePending = false, false
			want.flags.postGrant, want.flags.postGrantWindow = row.postGrant, row.window
			want.parserResets++
			if state := sstSnapshot(tr); !reflect.DeepEqual(state, want) {
				t.Fatalf("state\n got: %+v\nwant: %+v", state, want)
			}
		})
	}
}

// TestENHArbitrationStartRequest pins the START request write: one START
// frame for the requested initiator, the write lock released on every exit,
// the short-write sentinel and the mapped write error.
func TestENHArbitrationStartRequest(t *testing.T) {
	for _, row := range []struct {
		name       string
		initiator  byte
		writeErr   error
		shortWrite bool
		wantErr    error
		wantText   string
		written    bool
	}{
		{name: "writes the START frame", initiator: sstInitiator, written: true},
		{name: "writes the START frame for another initiator", initiator: 0x10, written: true},
		{name: "short write", initiator: sstInitiator, shortWrite: true, wantErr: ebuserrors.ErrInvalidPayload},
		{name: "write error is mapped", initiator: sstInitiator, writeErr: errors.New("scripted write refused"),
			wantErr: ebuserrors.ErrTransportClosed, wantText: "enh transport write failed: scripted write refused"},
	} {
		t.Run(row.name, func(t *testing.T) {
			conn := newSSTConn()
			conn.writeErr = row.writeErr
			conn.shortWrite = row.shortWrite
			tr := sstNew(conn, 0)
			sstCheckErr(t, "writeStartRequest", tr.writeStartRequest(row.initiator), row.wantErr, row.wantText)
			if !tr.writeMu.TryLock() {
				t.Fatal("writeMu still held")
			}
			tr.writeMu.Unlock()
			var want []byte
			if row.written {
				frame := EncodeENH(ENHReqStart, row.initiator)
				want = frame[:]
			}
			if _, written := conn.state(); !reflect.DeepEqual(written, want) {
				t.Fatalf("written = % X; want % X", written, want)
			}
		})
	}
}
