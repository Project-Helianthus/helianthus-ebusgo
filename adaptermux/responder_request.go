package adaptermux

import "github.com/Project-Helianthus/helianthus-ebusgo/protocol"

// protocolResponseMax is the longest logical target answer the length
// octet can name: ACK, the length byte, 255 data bytes and the CRC.
const protocolResponseMax = 258

func (m *Mux) observeResponderSymbol(symbol byte, physical, logicalWireSYN bool) {
	r := m.responder
	if r == nil {
		return
	}
	r.mu.Lock()
	plan := r.feedLocked(m, symbol, physical, logicalWireSYN)
	r.mu.Unlock()
	if plan != nil {
		r.dispatch(m, plan)
	}
}

func (r *responder) busyLocked() bool {
	return r.phase != phIdle || r.slot != nil || r.window || r.foreignAckWatch || r.crcSeen || r.escPending
}

func (r *responder) feedLocked(m *Mux, symbol byte, physical, logicalWireSYN bool) *sendPlan {
	wireSYN := logicalWireSYN
	if physical && !r.escPending && symbol == protocol.SymbolSyn {
		wireSYN = true
	}
	if !r.busyLocked() && r.reg.empty() {
		return nil
	}
	if wireSYN {
		return r.onWireSYNLocked(m)
	}
	switch r.phase {
	case phEcho:
		select {
		case r.echoCh <- symbol:
		default:
		}
		return nil
	case phReply:
		return r.onReplyLocked(m, symbol)
	case phSkip:
		if r.foreignAckWatch {
			addr := r.foreignAddr
			sess := uint64(0)
			if h := r.reg.by[addr]; h.session != 0 {
				sess = h.session
			}
			r.foreignAckWatch = false
			if sess != 0 {
				r.counters.competing++
				r.revokeAddressLocked(m, sess, addr, reasonConflict)
			}
		}
		return nil
	case phIdle:
		r.startRequestLocked(symbol)
		return nil
	case phQQ:
		return r.onDestinationLocked(m, symbol, physical)
	case phBody:
		return r.onBodyLocked(m, symbol, physical)
	default:
		return nil
	}
}

func (r *responder) onWireSYNLocked(m *Mux) *sendPlan {
	r.afterSYN = true
	r.escPending = false
	r.crcSeen = false
	r.buf = nil
	r.foreignAckWatch = false
	switch r.phase {
	case phBody:
		r.endSlotLocked(m, endMalformed)
	case phEcho, phReply:
		r.stopPaceLocked()
		r.endSlotLocked(m, endBoundary)
	default:
		r.slot = nil
		r.window = false
	}
	r.phase = phIdle
	return nil
}

func (r *responder) startRequestLocked(symbol byte) {
	if r.afterSYN || r.txn == 0 {
		r.txn++
		r.afterSYN = false
	}
	r.crcSeen = false
	r.buf = []byte{symbol}
	r.phase = phQQ
	r.escPending = false
}

func (r *responder) onDestinationLocked(m *Mux, symbol byte, physical bool) *sendPlan {
	if physical && symbol == protocol.SymbolEscape {
		r.phase = phSkip
		r.buf = nil
		return nil
	}
	r.buf = append(r.buf, symbol)
	if inadmissibleAddress(symbol) || r.reg.by[symbol].session == 0 {
		r.phase = phSkip
		return nil
	}
	h := r.reg.by[symbol]
	tag, ok := r.allocTagLocked(h.session)
	if !ok {
		r.phase = phSkip
		r.revokeAllLocked(m, h.session, reasonTagExhausted)
		return nil
	}
	r.slot = &respSlot{
		key: slotKey{
			session:     h.session,
			epoch:       h.epoch,
			generation:  r.generation,
			transaction: r.txn,
			tag:         tag,
		},
		addr: symbol,
		i2i:  protocol.IsInitiatorCapableAddress(symbol),
	}
	r.phase = phBody
	r.queueLocked(m, h.session, sessionFrameSlot, tag)
	return nil
}

func (r *responder) onBodyLocked(m *Mux, symbol byte, physical bool) *sendPlan {
	if physical && r.escPending {
		r.escPending = false
		switch symbol {
		case 0x00:
			symbol = protocol.SymbolEscape
		case 0x01:
			symbol = protocol.SymbolSyn
		default:
			r.endSlotLocked(m, endMalformed)
			return nil
		}
		return r.appendRequestLocked(m, symbol)
	}
	if physical && symbol == protocol.SymbolEscape {
		r.escPending = true
		return nil
	}
	return r.appendRequestLocked(m, symbol)
}

func (r *responder) appendRequestLocked(m *Mux, b byte) *sendPlan {
	r.buf = append(r.buf, b)
	if len(r.buf) < 5 {
		return nil
	}
	total := 6 + int(r.buf[4])
	if len(r.buf) < total {
		return nil
	}
	return r.finishRequestLocked(m)
}

func (r *responder) finishRequestLocked(m *Mux) *sendPlan {
	buf := r.buf
	valid := len(buf) >= 6 && protocol.CRC(buf[:len(buf)-1]) == buf[len(buf)-1]
	r.crcSeen = true
	r.buf = nil
	// Last symbol of the request. The AUTO-SYN slot starts at this
	// injected-clock reading; dispatch compares the first-symbol
	// submission against it.
	r.windowStart = r.now()
	if r.slot == nil {
		r.phase = phSkip
		return nil
	}
	if !valid {
		r.window = true
		r.phase = phEcho
		r.slot.inFlight = true
		return r.makePlanLocked(m, []byte{protocol.SymbolNack}, true, false)
	}
	if r.slot.declined {
		addr := r.slot.addr
		r.endSlotLocked(m, endDeclined)
		r.armForeignLocked(addr)
		return nil
	}
	if len(r.slot.committed) == 0 {
		addr := r.slot.addr
		r.endSlotLocked(m, endNotReady)
		r.armForeignLocked(addr)
		return nil
	}
	if !r.registrationLiveLocked(r.slot) {
		r.endSlotLocked(m, endRevoked)
		return nil
	}
	logical := append([]byte{}, r.slot.committed...)
	i2i := r.slot.i2i
	r.window = true
	r.phase = phEcho
	r.slot.inFlight = true
	return r.makePlanLocked(m, logical, false, i2i)
}

func (r *responder) armForeignLocked(addr byte) {
	r.foreignAckWatch = true
	r.foreignAddr = addr
	r.phase = phSkip
}

func (r *responder) onReplyLocked(m *Mux, symbol byte) *sendPlan {
	if r.slot == nil {
		r.phase = phSkip
		return nil
	}
	switch symbol {
	case protocol.SymbolAck:
		r.endSlotLocked(m, endCompleted)
	case protocol.SymbolNack:
		if r.slot.retried || r.slot.i2i || len(r.slot.committed) < 2 {
			r.endSlotLocked(m, endResponseRejected)
			return nil
		}
		r.slot.retried = true
		tail := append([]byte{}, r.slot.committed[1:]...)
		r.counters.retransmit++
		r.window = true
		r.phase = phEcho
		// The NACK reopens the slot. The first repeated symbol is
		// measured from this symbol, not from the original request.
		r.windowStart = r.now()
		return r.makePlanLocked(m, tail, false, false)
	default:
		r.endSlotLocked(m, endBoundary)
	}
	return nil
}

func (r *responder) beginLocked(m *Mux, s *session, tag byte) {
	if r.crcSeen || !r.matchesLocked(s.id, tag) || (r.slot != nil && r.slot.committed != nil) {
		r.counters.stale++
		r.queueLocked(m, s.id, sessionFrameTargetError, reasonStale)
		return
	}
	r.slot.staged = nil
	r.slot.stagingOpen = true
}

func (r *responder) stageLocked(m *Mux, s *session, b byte) {
	if r.crcSeen || r.slot == nil || !r.slot.stagingOpen || r.slot.key.session != s.id {
		r.counters.stageDiscard++
		return
	}
	// The length octet cannot name a response longer than protocolResponseMax.
	if len(r.slot.staged) >= protocolResponseMax {
		r.slot.staged = nil
		r.slot.stagingOpen = false
		r.queueLocked(m, s.id, sessionFrameTargetError, reasonInvalidResponse)
		return
	}
	r.slot.staged = append(r.slot.staged, b)
}

func (r *responder) commitLocked(m *Mux, s *session, count byte) {
	if r.crcSeen {
		r.counters.late++
		r.queueLocked(m, s.id, sessionFrameTargetError, reasonLate)
		return
	}
	if r.slot == nil || !r.slot.stagingOpen || !r.matchesLocked(s.id, r.slot.key.tag) {
		r.counters.stale++
		r.queueLocked(m, s.id, sessionFrameTargetError, reasonStale)
		return
	}
	if count == 0 && len(r.slot.staged) == 0 {
		r.slot.declined = true
		r.slot.stagingOpen = false
		return
	}
	if int(count) != len(r.slot.staged) || !validCommit(r.slot.i2i, r.slot.staged) {
		r.slot.staged = nil
		r.slot.stagingOpen = false
		r.slot.committed = nil
		r.queueLocked(m, s.id, sessionFrameTargetError, reasonInvalidResponse)
		return
	}
	r.slot.committed = append([]byte{}, r.slot.staged...)
	r.slot.stagingOpen = false
}

func validCommit(i2i bool, b []byte) bool {
	if i2i {
		return len(b) == 1 && b[0] == protocol.SymbolAck
	}
	if len(b) < 3 || b[0] != protocol.SymbolAck {
		return false
	}
	nn := int(b[1])
	if len(b) != 2+nn+1 {
		return false
	}
	return protocol.CRC(b[1:len(b)-1]) == b[len(b)-1]
}
