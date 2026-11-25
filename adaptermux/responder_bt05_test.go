package adaptermux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/adaptermux/v8classifier"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

func openUpstream(t *testing.T) (*Mux, *respUp, *respSess) {
	t.Helper()
	up := &respUp{}
	m := newRespMux(t, up)
	up.echo = func(b byte) { m.responder.pushEcho(b) }
	return m, up, addRespSession(t, m)
}

func TestSessionProtocolRoundTrip(t *testing.T) {
	frames, rest, hold, holding, bad := peelSessionExtension([]byte{0xD0, 0x88, 0x01}, 0, false)
	if bad || holding || hold != 0 || len(rest) != 1 || rest[0] != 0x01 {
		t.Fatalf("peel rest=%x hold=%d bad=%v", rest, hold, bad)
	}
	if len(frames) != 1 || frames[0].cmd != extNibbleTarget || frames[0].data != 0x08 {
		t.Fatalf("frame %+v", frames)
	}
	_, _, _, _, bad = peelSessionExtension([]byte{0x00}, 0xD0, true)
	if !bad {
		t.Fatal("second byte outside the encoded form was accepted")
	}
	got := enhCmd(extNibbleTargetError, reasonDuplicate)
	if !bytes.Equal(got, []byte{0xF4, 0x81}) {
		t.Fatalf("TARGET_ERROR duplicate %x", got)
	}
}

func TestTargetRegistrationLifecycle(t *testing.T) {
	t.Run("inadmissible", func(t *testing.T) {
		for _, tc := range []struct {
			addr byte
			file string
		}{
			{protocol.AddressBroadcast, "register-fe.json"},
			{protocol.SymbolEscape, "register-a9.json"},
			{protocol.SymbolSyn, "register-aa.json"},
		} {
			m, up, s := openUpstream(t)
			s.write(t, enhCmd(extNibbleTarget, tc.addr))
			v := loadVec(t, tc.file)
			got := waitSuffix(t, s, mustHex(t, v.Session))
			sym, wr, st := up.snapshot()
			compareTraffic(t, v, got, sym, wr, st)
			m.responder.mu.Lock()
			held := m.responder.reg.by[tc.addr].session != 0
			m.responder.mu.Unlock()
			if held {
				t.Fatalf("address 0x%02X was registered", tc.addr)
			}
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		m, up, holder := openUpstream(t)
		other := addRespSession(t, m)
		targetAndWait(t, holder, 0x08)
		m.responder.mu.Lock()
		epoch := m.responder.reg.by[0x08].epoch
		owner := m.responder.reg.by[0x08].session
		m.responder.mu.Unlock()
		other.write(t, enhCmd(extNibbleTarget, 0x08))
		v := loadVec(t, "register-duplicate.json")
		ob := waitSuffix(t, other, mustHex(t, v.SessionB))
		if !bytes.Equal(ob, mustHex(t, v.SessionB)) || !bytes.Equal(holder.bytes(), mustHex(t, v.Session)) {
			t.Fatalf("holder %x other %x", holder.bytes(), ob)
		}
		m.responder.mu.Lock()
		same := m.responder.reg.by[0x08].session == owner && m.responder.reg.by[0x08].epoch == epoch
		m.responder.mu.Unlock()
		if !same {
			t.Fatal("duplicate TARGET replaced the holder")
		}
		sym, wr, st := up.snapshot()
		if len(sym) != 0 || len(wr) != 0 || len(st) != 0 {
			t.Fatalf("duplicate moved the bus %x %x %x", sym, wr, st)
		}
	})

	t.Run("retarget", func(t *testing.T) {
		m, up, s := openUpstream(t)
		targetAndWait(t, s, 0x08)
		m.responder.mu.Lock()
		epoch := m.responder.reg.by[0x08].epoch
		m.responder.mu.Unlock()
		targetAndWait(t, s, 0x08)
		m.responder.mu.Lock()
		same := m.responder.reg.by[0x08].epoch == epoch && m.responder.reg.by[0x08].session == s.id
		m.responder.mu.Unlock()
		if !same {
			t.Fatal("holder re-TARGET changed the epoch")
		}
		v := loadVec(t, "register-retarget.json")
		sym, wr, st := up.snapshot()
		compareTraffic(t, v, s.bytes(), sym, wr, st)
	})

	t.Run("initiator", func(t *testing.T) {
		m, up, s := openUpstream(t)
		if !protocol.IsInitiatorCapableAddress(0x10) {
			t.Fatal("0x10 is the initiator address this test admits")
		}
		targetAndWait(t, s, 0x10)
		v := loadVec(t, "register-initiator.json")
		sym, wr, st := up.snapshot()
		compareTraffic(t, v, s.bytes(), sym, wr, st)
		m.responder.mu.Lock()
		ok := m.responder.reg.by[0x10].session == s.id
		m.responder.mu.Unlock()
		if !ok {
			t.Fatal("initiator address was not admitted")
		}
	})

	t.Run("i2i", func(t *testing.T) {
		m, up, s := openUpstream(t)
		targetAndWait(t, s, 0x10)
		feedRaw(m, []byte{0x03, 0x10})
		commitSlot(t, m, s, []byte{protocol.SymbolAck})
		feedRaw(m, []byte{0x07, 0x04, 0x00, 0x13})
		waitSymbols(t, up, m, 1)
		raw := waitSuffix(t, s, []byte{0xDC, 0x80})
		body := trimHexPrefix(t, raw, "d090")
		v := loadVec(t, "i2i-ack.json")
		sym, wr, st := up.snapshot()
		compareTraffic(t, v, body, sym, wr, st)
		feedRaw(m, []byte{protocol.SymbolNack})
		sym, _, _ = up.snapshot()
		if !bytes.Equal(sym, []byte{protocol.SymbolAck}) || m.ResponderDiagnostics().Retransmit != 0 {
			t.Fatalf("initiator destination repeated a response %x diag %+v", sym, m.ResponderDiagnostics())
		}
	})

	t.Run("reset", func(t *testing.T) {
		m, _, s := openUpstream(t)
		targetAndWait(t, s, 0x08)
		m.handleReset()
		got := waitSuffix(t, s, []byte{0xF4, 0x87, 0xD4, 0x88, 0xC0, 0x80})
		if !bytes.HasSuffix(trimHexPrefix(t, got, "d088"), []byte{0xF4, 0x87, 0xD4, 0x88, 0xC0, 0x80}) {
			t.Fatalf("reset notices %x", got)
		}
		m.responder.mu.Lock()
		empty := m.responder.reg.empty()
		m.responder.mu.Unlock()
		if !empty {
			t.Fatal("reset left a registration")
		}
		before := len(s.bytes())
		m.retireGatewayLeadWait(io.EOF)
		if len(s.bytes()) != before {
			t.Fatal("a second stream end emitted notices")
		}
	})

	t.Run("reconnect", func(t *testing.T) {
		m, _, s := openUpstream(t)
		targetAndWait(t, s, 0x08)
		m.retireGatewayLeadWait(io.EOF)
		got := waitSuffix(t, s, []byte{0xF4, 0x87, 0xD4, 0x88})
		if bytes.Contains(got, []byte{0xC0, 0x80}) {
			t.Fatalf("reconnect hook emitted RESETTED %x", got)
		}
		m.responder.mu.Lock()
		empty := m.responder.reg.empty()
		m.responder.mu.Unlock()
		if !empty {
			t.Fatal("reconnect left a registration")
		}
	})

	t.Run("close", func(t *testing.T) {
		m, _, s := openUpstream(t)
		targetAndWait(t, s, 0x08)
		m.RemoveSession(s.id)
		waitUntil(t, func() bool {
			m.responder.mu.Lock()
			defer m.responder.mu.Unlock()
			return m.responder.reg.empty()
		})
	})

	t.Run("epoch", func(t *testing.T) {
		m, _, s := openUpstream(t)
		targetAndWait(t, s, 0x08)
		m.responder.mu.Lock()
		first := m.responder.reg.by[0x08].epoch
		m.responder.mu.Unlock()
		s.write(t, enhCmd(extNibbleUntarget, 0x08))
		waitSuffix(t, s, enhCmd(extNibbleUntarget, 0x08))
		targetAndWait(t, s, 0x08)
		m.responder.mu.Lock()
		second := m.responder.reg.by[0x08].epoch
		m.responder.mu.Unlock()
		if second == first || second == 0 {
			t.Fatalf("epochs %d then %d", first, second)
		}
		// Removing the epoch compare accepts a commit after unregister and re-register.
		old := &respSlot{key: slotKey{session: s.id, epoch: first, generation: 1, transaction: 1, tag: 1}}
		if slotKeyMatches(old, s.id, second, 1, 1, 1) {
			t.Fatal("registration epoch: a commit after unregister and re-register binds to the old slot")
		}
		if !slotKeyMatches(old, s.id, first, 1, 1, 1) {
			t.Fatal("epoch positive control")
		}
	})

	t.Run("capacity", func(t *testing.T) {
		m := newRespMux(t, &respUp{})
		client, server := net.Pipe()
		t.Cleanup(func() { _ = client.Close() })
		id := m.AddSession(server)
		if id == 0 {
			t.Fatal("AddSession rejected")
		}
		t.Cleanup(func() { m.RemoveSession(id) })
		m.sessionsMu.Lock()
		sess := m.sessions[id]
		m.sessionsMu.Unlock()
		sess.sendCh <- sessionFrame{kind: sessionFrameInfo}
		waitUntil(t, func() bool { return len(sess.sendCh) == 0 })
		for {
			select {
			case sess.sendCh <- sessionFrame{kind: sessionFrameInfo}:
			default:
				goto full
			}
		}
	full:
		m.responder.mu.Lock()
		m.responder.targetLocked(m, sess, 0x08)
		if m.responder.reg.by[0x08].session != 0 {
			m.responder.mu.Unlock()
			t.Fatal("a fresh TARGET stayed registered when TARGETED could not be queued")
		}
		m.responder.reg.by[0x08] = holder{session: id, epoch: 1}
		m.responder.targetLocked(m, sess, 0x08)
		held := m.responder.reg.by[0x08].session == id
		m.responder.mu.Unlock()
		if !held {
			t.Fatal("holder re-TARGET cleared the registration when TARGETED could not be queued")
		}
	})
}

func TestDestinationSymbolReservesSlotWithOrderedPrefix(t *testing.T) {
	m, up, s := openUpstream(t)
	targetAndWait(t, s, 0x08)
	feedRaw(m, []byte{0x10, 0x08, 0x07})
	raw := waitSuffix(t, s, mustHex(t, "1008d88107"))
	body := trimHexPrefix(t, raw, "d088")
	v := loadVec(t, "slot-prefix.json")
	sym, wr, st := up.snapshot()
	compareTraffic(t, v, body, sym, wr, st)

	m2, up2, s2 := openUpstream(t)
	targetAndWait(t, s2, 0x08)
	feedRaw(m2, []byte{0x10, 0x08, 0x07, 0x04, 0x02, 0xA9, 0x00, 0x11, 0xF2})
	raw = waitSuffix(t, s2, []byte{0xDC, 0x83})
	body = trimHexPrefix(t, raw, "d088")
	v = loadVec(t, "escape-once.json")
	sym, wr, st = up2.snapshot()
	compareTraffic(t, v, body, sym, wr, st)
	if bytes.Count(body, []byte{0xC6, 0xA9}) != 1 {
		t.Fatalf("escape pair delivered %d times in %x", bytes.Count(body, []byte{0xC6, 0xA9}), body)
	}
}

func TestProvisionalSlotEmitsNothingBeforeValidatedRequest(t *testing.T) {
	m, up, s := openUpstream(t)
	targetAndWait(t, s, 0x08)
	feedRaw(m, []byte{0x10, 0x08})
	commitSlot(t, m, s, []byte{0x00, 0x01, 0x11, 0x8A})
	sym, _, _ := up.snapshot()
	if len(sym) != 0 {
		t.Fatalf("commit before CRC wrote %x", sym)
	}

	mNack, upNack, sNack := openUpstream(t)
	targetAndWait(t, sNack, 0x08)
	feedRaw(mNack, []byte{0x10, 0x08, 0x07, 0x04, 0x00, 0x00})
	waitSymbols(t, upNack, mNack, 1)
	raw := waitSuffix(t, sNack, []byte{0xDC, 0x81})
	body := trimHexPrefix(t, raw, "d088")
	v := loadVec(t, "crc-nack.json")
	sym, wr, st := upNack.snapshot()
	compareTraffic(t, v, body, sym, wr, st)
	if mNack.ResponderDiagnostics().Nack != 1 {
		t.Fatalf("nack counter %d", mNack.ResponderDiagnostics().Nack)
	}

	mBad, upBad, sBad := openUpstream(t)
	targetAndWait(t, sBad, 0x08)
	feedRaw(mBad, []byte{0x10, 0x08, 0x07, 0x04, 0x01, 0xA9, 0xFF})
	raw = waitSuffix(t, sBad, []byte{0xDC, 0x82})
	body = trimHexPrefix(t, raw, "d088")
	v = loadVec(t, "malformed.json")
	sym, wr, st = upBad.snapshot()
	compareTraffic(t, v, body, sym, wr, st)

	mSyn, upSyn, sSyn := openUpstream(t)
	targetAndWait(t, sSyn, 0x08)
	feedRaw(mSyn, []byte{0x10, 0x08, 0x07, protocol.SymbolSyn})
	raw = waitSuffix(t, sSyn, []byte{0xDC, 0x82})
	body = trimHexPrefix(t, raw, "d088")
	v = loadVec(t, "syn-in-request.json")
	sym, wr, st = upSyn.snapshot()
	compareTraffic(t, v, body, sym, wr, st)

	mIdle, upIdle, sIdle := openUpstream(t)
	targetAndWait(t, sIdle, 0x08)
	feedRaw(mIdle, []byte{0x10, 0x08, 0x07, 0x04, 0x00, 0x17})
	raw = waitSuffix(t, sIdle, []byte{0xDC, 0x83})
	body = trimHexPrefix(t, raw, "d088")
	v = loadVec(t, "not-ready.json")
	sym, wr, st = upIdle.snapshot()
	compareTraffic(t, v, body, sym, wr, st)
}

func TestResponderEmissionWithoutStartFencesLocalTraffic(t *testing.T) {
	up := &respUp{blockN: 1, blockStart: make(chan struct{}), entered: make(chan struct{}, 1)}
	m := newRespMux(t, up)
	up.echo = func(b byte) { m.responder.pushEcho(b) }
	m.wg.Add(1)
	go m.sendLoop()
	s := addRespSession(t, m)
	other := addRespSession(t, m)
	m.arb.confirmOwnership(s.id, 0x10)
	targetAndWait(t, s, 0x08)
	feedRaw(m, []byte{0x10, 0x08})
	commitSlot(t, m, s, []byte{0x00, 0x01, 0x11, 0x8A})
	m.responder.mu.Lock()
	staged := append([]byte(nil), m.responder.slot.committed...)
	m.responder.mu.Unlock()
	other.write(t, enhCmd(extNibbleBegin, 1))
	waitSuffix(t, other, []byte{0xF4, 0x88})
	m.responder.mu.Lock()
	same := bytes.Equal(m.responder.slot.committed, staged)
	m.responder.mu.Unlock()
	if !same {
		t.Fatal("another session's BEGIN changed the committed buffer")
	}

	feedDone := make(chan struct{})
	go func() {
		feedRaw(m, []byte{0x07, 0x04, 0x00, 0x17})
		close(feedDone)
	}()
	select {
	case <-up.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first response symbol was not submitted")
	}
	if !m.responderWindowOpen() {
		t.Fatal("window was closed at the first symbol")
	}
	m.stateMu.Lock()
	armed := len(m.gatewayEcho.expectedEchoes) != 0 || m.gatewayTxnActive
	m.stateMu.Unlock()
	res := make(chan error, 1)
	m.activeSendCh <- sendRequest{sessionID: gatewaySessionID, data: 0x55, result: res}
	if err := <-res; !errors.Is(err, errResponderWindow) {
		t.Fatalf("queued write %v", err)
	}
	s.write(t, []byte{0x02})
	waitSuffix(t, s, []byte{0xF0, 0x80})
	sym, wr, st := up.snapshot()
	if len(wr) != 0 || len(st) != 0 || !bytes.Equal(sym, []byte{0x00}) {
		t.Fatalf("window wrote %x starts %x symbols %x", wr, st, sym)
	}
	m.stateMu.Lock()
	armed = armed || len(m.gatewayEcho.expectedEchoes) != 0 || m.gatewayTxnActive
	m.stateMu.Unlock()
	if armed {
		t.Fatal("queued write armed a gateway echo")
	}
	if m.ResponderDiagnostics().WindowSend < 2 {
		t.Fatalf("window counter %d", m.ResponderDiagnostics().WindowSend)
	}
	up.blockStart <- struct{}{}
	<-feedDone
	waitSymbols(t, up, m, 4)

	feedRaw(m, []byte{protocol.SymbolNack})
	waitSymbols(t, up, m, 7)
	feedRaw(m, []byte{protocol.SymbolAck})
	raw := waitSuffix(t, s, []byte{0xDC, 0x80})
	if !bytes.Contains(raw, []byte{0xF0, 0x80}) {
		t.Fatal("owner SEND was not answered with ERROR_HOST")
	}
	sym, _, st = up.snapshot()
	if !bytes.Equal(sym, mustHex(t, "0001118a01118a")) || len(st) != 0 {
		t.Fatalf("symbols %x starts %x", sym, st)
	}
	if m.ResponderDiagnostics().Retransmit != 1 {
		t.Fatalf("retransmit %d", m.ResponderDiagnostics().Retransmit)
	}
	// The tail is one emission of length, data and CRC. It has no command ACK.
	if !bytes.Equal(sym[4:], []byte{0x01, 0x11, 0x8A}) {
		t.Fatalf("retransmission %x", sym[4:])
	}
	s.write(t, []byte{0x22})
	waitUntil(t, func() bool {
		_, w, _ := up.snapshot()
		return bytes.Equal(w, []byte{0x22})
	})

	t.Run("unavailable", func(t *testing.T) {
		m := newRespMux(t, rawOnly{})
		s := addRespSession(t, m)
		s.write(t, enhCmd(extNibbleTarget, 0x08))
		got := waitSuffix(t, s, []byte{0xF4, 0x85, 0xD4, 0x88})
		if !bytes.Equal(got, []byte{0xF4, 0x85, 0xD4, 0x88}) {
			t.Fatalf("unavailable %x", got)
		}
		m.responder.mu.Lock()
		held := m.responder.reg.by[0x08].session != 0
		m.responder.mu.Unlock()
		if held {
			t.Fatal("unavailable TARGET registered")
		}
	})
}

func TestResponderSynWhileWaiting(t *testing.T) {
	up := &respUp{blockN: 1, blockStart: make(chan struct{}), entered: make(chan struct{}, 2)}
	m := newRespMux(t, up)
	s := addRespSession(t, m)
	targetAndWait(t, s, 0x08)
	feedRaw(m, []byte{0x10, 0x08})
	commitSlot(t, m, s, []byte{0x00, 0x01, 0x11, 0x8A})
	feedDone := make(chan struct{})
	go func() {
		feedRaw(m, []byte{0x07, 0x04, 0x00, 0x17})
		close(feedDone)
	}()
	select {
	case <-up.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("response symbol was not submitted")
	}
	feedRaw(m, []byte{protocol.SymbolSyn})
	up.blockStart <- struct{}{}
	<-feedDone
	raw := waitSuffix(t, s, []byte{0xDC, 0x87})
	body := trimHexPrefix(t, raw, "d088")
	sym, wr, st := up.snapshot()
	compareTraffic(t, loadVec(t, "syn-wait.json"), body, sym, wr, st)
}

func TestResponderRepeatAfterNack(t *testing.T) {
	m, up, s := openUpstream(t)
	targetAndWait(t, s, 0x08)
	feedRaw(m, []byte{0x10, 0x08})
	commitSlot(t, m, s, []byte{0x00, 0x01, 0x11, 0x8A})
	feedRaw(m, []byte{0x07, 0x04, 0x00, 0x17})
	waitSymbols(t, up, m, 4)
	feedRaw(m, []byte{protocol.SymbolNack})
	waitSymbols(t, up, m, 7)
	feedRaw(m, []byte{protocol.SymbolAck})
	raw := waitSuffix(t, s, []byte{0xDC, 0x80})
	body := trimHexPrefix(t, raw, "d088")
	v := loadVec(t, "emit-retransmit.json")
	sym, wr, st := up.snapshot()
	compareTraffic(t, v, body, sym, wr, st)
	if m.ResponderDiagnostics().Retransmit != 1 {
		t.Fatalf("retransmit counted %d", m.ResponderDiagnostics().Retransmit)
	}
}

func TestResponderPathIsUnpacedAndBounded(t *testing.T) {
	up := &respUp{}
	m := New(Config{
		Protocol:         "enh",
		Logger:           log.New(io.Discard, "", 0),
		StartDeadline:    time.Hour,
		V8ClassifierMode: v8classifier.ModeShadow,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.ctx = ctx
	m.cancel = cancel
	m.upstream = up
	up.echo = func(b byte) { m.responder.pushEcho(b) }
	reg := addRespSession(t, m)
	other := addRespSession(t, m)
	targetAndWait(t, reg, 0x08)
	feedRaw(m, []byte{0x31})
	waitUntil(t, func() bool { return len(reg.bytes()) >= 3 && len(other.bytes()) >= 1 })
	waitUntil(t, func() bool { return !m.SessionPacer(other.id).LastScheduledEmit().IsZero() })
	if !m.SessionPacer(reg.id).LastScheduledEmit().IsZero() {
		t.Fatal("registered session was paced")
	}
	m.sessionsMu.Lock()
	sess := m.sessions[reg.id]
	m.sessionsMu.Unlock()
	sess.deliverReceived(0x32)
	sess.deliverReceived(0x33)
	waitUntil(t, func() bool { return len(reg.bytes()) >= 5 })
	if !m.SessionPacer(reg.id).LastScheduledEmit().IsZero() {
		t.Fatal("registered session was paced by later symbols")
	}

	t.Run("fixed_clock_emits", func(t *testing.T) {
		base := time.Unix(0, 0)
		session, sym, wr, st := emitAckWithClock(t, func() time.Time { return base }, false)
		compareTraffic(t, loadVec(t, "emit-ack.json"), session, sym, wr, st)
	})
	t.Run("deadline_emits_nothing", func(t *testing.T) {
		base := time.Unix(0, 0)
		var n int
		session, sym, wr, st := emitAckWithClock(t, func() time.Time {
			n++
			if n >= 2 {
				return base.Add(autoSYNResponseWindow)
			}
			return base
		}, true)
		compareTraffic(t, loadVec(t, "late.json"), session, sym, wr, st)
	})
}

func emitAckWithClock(t *testing.T, now func() time.Time, late bool) (session, symbols, writes, starts []byte) {
	t.Helper()
	m, up, s := openUpstream(t)
	m.responder.now = now
	targetAndWait(t, s, 0x08)
	feedRaw(m, []byte{0x10, 0x08})
	commitSlot(t, m, s, []byte{0x00, 0x01, 0x11, 0x8A})
	feedRaw(m, []byte{0x07, 0x04, 0x00, 0x17})
	if late {
		if m.ResponderDiagnostics().Late != 1 {
			t.Fatalf("late counter %d", m.ResponderDiagnostics().Late)
		}
		raw := waitSuffix(t, s, []byte{0xDC, 0x85})
		session = trimHexPrefix(t, raw, "d088")
	} else {
		waitSymbols(t, up, m, 4)
		feedRaw(m, []byte{protocol.SymbolAck})
		raw := waitSuffix(t, s, []byte{0xDC, 0x80})
		session = trimHexPrefix(t, raw, "d088")
	}
	symbols, writes, starts = up.snapshot()
	return session, symbols, writes, starts
}

func TestResponderIsolationNoTarget(t *testing.T) {
	m, up, s := openUpstream(t)
	feedRaw(m, []byte{0x10, 0x08, 0x07, 0x04, 0x00, 0x17})
	waitUntil(t, func() bool { return len(s.bytes()) >= 6 })
	v := loadVec(t, "no-target.json")
	sym, wr, st := up.snapshot()
	compareTraffic(t, v, s.bytes(), sym, wr, st)
}

func TestResponderSlotInvalidation(t *testing.T) {
	// Each row flips one key field. The positive control uses the same slot.
	slot := &respSlot{key: slotKey{session: 7, epoch: 3, generation: 5, transaction: 9, tag: 4}}
	if !slotKeyMatches(slot, 7, 3, 5, 9, 4) {
		t.Fatal("positive control")
	}
	// session: a commit from another session is accepted
	if slotKeyMatches(slot, 8, 3, 5, 9, 4) {
		t.Fatal("session: a commit from another session is accepted")
	}
	// registration epoch: a commit after unregister and re-register binds to the old slot
	if slotKeyMatches(slot, 7, 4, 5, 9, 4) {
		t.Fatal("registration epoch: a commit after unregister and re-register binds to the old slot")
	}
	// stream generation: a slot reserved before a reset emits after it
	if slotKeyMatches(slot, 7, 3, 6, 9, 4) {
		t.Fatal("stream generation: a slot reserved before a reset emits after it")
	}
	// transaction: a commit for an ended transaction is used for the next one
	if slotKeyMatches(slot, 7, 3, 5, 10, 4) {
		t.Fatal("transaction: a commit for an ended transaction is used for the next one")
	}
	// slot tag: a stale BEGIN or COMMIT is accepted, or a tag is reused within a session
	if slotKeyMatches(slot, 7, 3, 5, 9, 5) {
		t.Fatal("slot tag: a stale BEGIN or COMMIT is accepted, or a tag is reused within a session")
	}

	t.Run("stale", func(t *testing.T) {
		m, _, s := openUpstream(t)
		targetAndWait(t, s, 0x08)
		feedRaw(m, []byte{0x10, 0x08})
		s.write(t, append(enhCmd(extNibbleBegin, 1), enhCmd(extNibbleStage, 0x00)...))
		waitUntil(t, func() bool {
			m.responder.mu.Lock()
			defer m.responder.mu.Unlock()
			return m.responder.slot != nil && len(m.responder.slot.staged) == 1
		})
		s.write(t, enhCmd(extNibbleBegin, 2))
		waitSuffix(t, s, []byte{0xF4, 0x88})
		m.responder.mu.Lock()
		kept := m.responder.slot != nil && len(m.responder.slot.staged) == 1 && m.responder.slot.stagingOpen
		m.responder.mu.Unlock()
		if !kept {
			t.Fatal("stale BEGIN changed staging")
		}
		if m.ResponderDiagnostics().Stale == 0 {
			t.Fatal("stale was not counted")
		}
	})

	t.Run("unregister_finishes_lease", func(t *testing.T) {
		up := &respUp{blockN: 1, blockStart: make(chan struct{}), entered: make(chan struct{}, 1)}
		m := newRespMux(t, up)
		up.echo = func(b byte) { m.responder.pushEcho(b) }
		s := addRespSession(t, m)
		targetAndWait(t, s, 0x08)
		feedRaw(m, []byte{0x10, 0x08})
		commitSlot(t, m, s, []byte{0x00, 0x01, 0x11, 0x8A})
		go feedRaw(m, []byte{0x07, 0x04, 0x00, 0x17})
		select {
		case <-up.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("emission did not start")
		}
		s.write(t, enhCmd(extNibbleUntarget, 0x08))
		waitSuffix(t, s, enhCmd(extNibbleUntarget, 0x08))
		up.blockStart <- struct{}{}
		waitSymbols(t, up, m, 4)
		sym, _, _ := up.snapshot()
		if !bytes.Equal(sym, []byte{0x00, 0x01, 0x11, 0x8A}) {
			t.Fatalf("lease wrote %x", sym)
		}
	})

	t.Run("reset_stops_emission", func(t *testing.T) {
		m, up, _ := openUpstream(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		m.responder.mu.Lock()
		plan := m.responder.makePlanLocked(m, []byte{0x00, 0x01, 0x11, 0x8A}, false, false)
		plan.ctx = ctx
		m.responder.generation++
		m.responder.mu.Unlock()
		m.responder.pace(m, plan, 0x00, []byte{0x01, 0x11, 0x8A})
		sym, _, _ := up.snapshot()
		if len(sym) != 0 {
			t.Fatalf("reset wrote %x", sym)
		}
	})

	t.Run("tag_255", func(t *testing.T) {
		m, up, s := openUpstream(t)
		targetAndWait(t, s, 0x08)
		m.responder.mu.Lock()
		m.responder.nextTag[s.id] = 254
		m.responder.mu.Unlock()
		feedRaw(m, []byte{0x10, 0x08})
		commitSlot(t, m, s, []byte{0x00, 0x01, 0x11, 0x8A})
		feedRaw(m, []byte{0x07, 0x04, 0x00, 0x17})
		waitSymbols(t, up, m, 4)
		feedRaw(m, []byte{protocol.SymbolAck})
		raw := waitSuffix(t, s, []byte{0xF4, 0x8B, 0xD4, 0x88})
		if !bytes.Contains(raw, []byte{0xDB, 0xBF}) || !bytes.Contains(raw, []byte{0xDC, 0x80}) {
			t.Fatalf("tag 255 stream %x", raw)
		}
		sym, _, _ := up.snapshot()
		if !bytes.Equal(sym, []byte{0x00, 0x01, 0x11, 0x8A}) {
			t.Fatalf("tag 255 symbols %x", sym)
		}
		m.responder.mu.Lock()
		empty := m.responder.reg.empty()
		dead := m.responder.tagDead[s.id]
		m.responder.mu.Unlock()
		if !empty || !dead {
			t.Fatal("tag 255 did not revoke after SLOT_END")
		}
	})
}

func TestResponderSlotReplayIsDeterministic(t *testing.T) {
	for _, name := range []string{"emit-ack", "crc-nack", "malformed", "no-target"} {
		t.Run(name, func(t *testing.T) {
			a := replayScenario(t, name)
			b := replayScenario(t, name)
			if !bytes.Equal(a.session, b.session) || !bytes.Equal(a.symbols, b.symbols) || !bytes.Equal(a.writes, b.writes) || a.diag != b.diag {
				t.Fatalf("replay %s\n%x\n%x", name, a.session, b.session)
			}
			compareTraffic(t, loadVec(t, name+".json"), a.session, a.symbols, a.writes, a.starts)
		})
	}
}

type replayResult struct {
	session []byte
	symbols []byte
	writes  []byte
	starts  []byte
	diag    ResponderDiag
}

func replayScenario(t *testing.T, name string) replayResult {
	t.Helper()
	m, up, s := openUpstream(t)
	switch name {
	case "emit-ack":
		targetAndWait(t, s, 0x08)
		feedRaw(m, []byte{0x10, 0x08})
		commitSlot(t, m, s, []byte{0x00, 0x01, 0x11, 0x8A})
		feedRaw(m, []byte{0x07, 0x04, 0x00, 0x17})
		waitSymbols(t, up, m, 4)
		feedRaw(m, []byte{protocol.SymbolAck})
		waitSuffix(t, s, []byte{0xDC, 0x80})
		sbytes := trimHexPrefix(t, s.bytes(), "d088")
		sym, wr, st := up.snapshot()
		return replayResult{sbytes, sym, wr, st, m.ResponderDiagnostics()}
	case "crc-nack":
		targetAndWait(t, s, 0x08)
		feedRaw(m, []byte{0x10, 0x08, 0x07, 0x04, 0x00, 0x00})
		waitSymbols(t, up, m, 1)
		raw := waitSuffix(t, s, []byte{0xDC, 0x81})
		sym, wr, st := up.snapshot()
		return replayResult{trimHexPrefix(t, raw, "d088"), sym, wr, st, m.ResponderDiagnostics()}
	case "malformed":
		targetAndWait(t, s, 0x08)
		feedRaw(m, []byte{0x10, 0x08, 0x07, 0x04, 0x01, 0xA9, 0xFF})
		raw := waitSuffix(t, s, []byte{0xDC, 0x82})
		sym, wr, st := up.snapshot()
		return replayResult{trimHexPrefix(t, raw, "d088"), sym, wr, st, m.ResponderDiagnostics()}
	case "no-target":
		feedRaw(m, []byte{0x10, 0x08, 0x07, 0x04, 0x00, 0x17})
		waitUntil(t, func() bool { return len(s.bytes()) >= 6 })
		sym, wr, st := up.snapshot()
		return replayResult{s.bytes(), sym, wr, st, m.ResponderDiagnostics()}
	default:
		t.Fatalf("unknown %s", name)
	}
	return replayResult{}
}

func TestResponderGrantHandoff(t *testing.T) {
	t.Run("late_start_stays_false", func(t *testing.T) {
		m, up, a, b := grantPair(t)
		a.write(t, enhCmd(transportStart(), 0x10))
		waitBids(t, m, 1)
		m.stateMu.Lock()
		bids := m.arb.snapshotBids()
		m.stateMu.Unlock()
		b.write(t, enhCmd(transportStart(), 0x11))
		waitBids(t, m, 2)
		go m.tryGrantAndStartForBids(bids)
		select {
		case <-up.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("RequestStart was not entered")
		}
		if m.BidHandedToGrant(a.id) || m.BidHandedToGrant(b.id) {
			t.Fatal("hand-off was true before RequestStart returned")
		}
		acted := false
		if actAfterHandoff(m, a.id, func() { acted = true }) || acted {
			t.Fatal("stimulus acted before RequestStart returned")
		}
		up.blockStart <- struct{}{}
		waitUntil(t, func() bool { return m.BidHandedToGrant(a.id) })
		if m.BidHandedToGrant(b.id) {
			t.Fatal("a START queued after the snapshot was handed to the grant step")
		}
		if actAfterHandoff(m, b.id, func() { acted = true }) || acted {
			t.Fatal("stimulus acted on a bid that has not reached RequestStart")
		}
	})

	t.Run("replaced", func(t *testing.T) {
		m, up, a, _ := grantPair(t)
		driveGrant(t, m, up, a, nil)
		waitUntil(t, func() bool { return m.BidHandedToGrant(a.id) })
		if !actAfterHandoff(m, a.id, func() {
			_ = m.requestStartForSession(a.id, 0x11)
		}) {
			t.Fatal("replacement did not wait for the hand-off")
		}
		sym, wr, st := up.snapshot()
		v := loadVec(t, "contention-replaced.json")
		compareTraffic(t, v, a.bytes(), sym, wr, st)
	})

	t.Run("closed", func(t *testing.T) {
		m, up, a, _ := grantPair(t)
		driveGrant(t, m, up, a, nil)
		waitUntil(t, func() bool { return m.BidHandedToGrant(a.id) })
		if !actAfterHandoff(m, a.id, func() { m.RemoveSession(a.id) }) {
			t.Fatal("close did not wait for the hand-off")
		}
		sym, wr, st := up.snapshot()
		v := loadVec(t, "contention-closed.json")
		compareTraffic(t, v, nil, sym, wr, st)
	})
}

func actAfterHandoff(m *Mux, id uint64, act func()) bool {
	if !m.BidHandedToGrant(id) {
		return false
	}
	act()
	return true
}

func transportStart() byte { return byte(0x2) }

func grantPair(t *testing.T) (*Mux, *respUp, *respSess, *respSess) {
	t.Helper()
	up := &respUp{blockStart: make(chan struct{}), entered: make(chan struct{}, 1)}
	m := newRespMux(t, up)
	return m, up, addRespSession(t, m), addRespSession(t, m)
}

func waitBids(t *testing.T, m *Mux, n int) {
	t.Helper()
	waitUntil(t, func() bool {
		m.arb.mu.Lock()
		defer m.arb.mu.Unlock()
		return len(m.arb.pendingExternal) >= n
	})
}

func driveGrant(t *testing.T, m *Mux, up *respUp, a, b *respSess) {
	t.Helper()
	a.write(t, enhCmd(transportStart(), 0x10))
	waitBids(t, m, 1)
	m.stateMu.Lock()
	bids := m.arb.snapshotBids()
	m.stateMu.Unlock()
	if b != nil {
		b.write(t, enhCmd(transportStart(), 0x11))
		waitBids(t, m, 2)
	}
	go m.tryGrantAndStartForBids(bids)
	select {
	case <-up.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestStart was not entered")
	}
	if m.BidHandedToGrant(a.id) {
		t.Fatal("hand-off was true before RequestStart returned")
	}
	up.blockStart <- struct{}{}
}
