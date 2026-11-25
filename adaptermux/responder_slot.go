package adaptermux

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// slotKey is the identity of one reserved responder slot.
type slotKey struct {
	session     uint64
	epoch       uint64
	generation  uint64
	transaction uint64
	tag         byte
}

// slotKeyMatches reports whether every field equals the live slot.
// Dropping one compare accepts a commit that field was meant to reject.
func slotKeyMatches(slot *respSlot, session, epoch, generation, transaction uint64, tag byte) bool {
	if slot == nil {
		return false
	}
	k := slot.key
	return k.session == session &&
		k.epoch == epoch &&
		k.generation == generation &&
		k.transaction == transaction &&
		k.tag == tag
}

// respSlot is the reservation made at the destination symbol.
type respSlot struct {
	key         slotKey
	addr        byte
	i2i         bool
	stagingOpen bool
	staged      []byte
	committed   []byte
	declined    bool
	inFlight    bool
	retried     bool
}

type respPhase uint8

const (
	phIdle respPhase = iota
	phQQ
	phBody
	phSkip
	phEcho
	phReply
)

// responderCounters are the responder-path diagnostics.
type responderCounters struct {
	late         uint64
	stageDiscard uint64
	stale        uint64
	windowSend   uint64
	nack         uint64
	retransmit   uint64
	competing    uint64
}

// responder is the local-target registration and answer path.
// Notices and pending output use the session send channel already
// sized by defaultSessionSendBuffer. The response window is the
// AUTO-SYN slot, measured with now.
type responder struct {
	mu sync.Mutex

	reg        targetRegistry
	generation uint64
	txn        uint64
	afterSYN   bool

	phase           respPhase
	buf             []byte
	escPending      bool
	crcSeen         bool
	slot            *respSlot
	window          bool
	foreignAckWatch bool
	foreignAddr     byte
	refused         [256]bool
	nextTag         map[uint64]byte
	tagDead         map[uint64]bool

	echoCh  chan byte
	ctx     context.Context
	cancel  context.CancelFunc
	liveSeq uint64
	now     func() time.Time

	counters responderCounters
}

func newResponder() *responder {
	ctx, cancel := context.WithCancel(context.Background())
	return &responder{
		generation: 1,
		nextTag:    make(map[uint64]byte),
		tagDead:    make(map[uint64]bool),
		echoCh:     make(chan byte, 8),
		ctx:        ctx,
		cancel:     cancel,
		now:        time.Now,
	}
}

func (m *Mux) responderTransport() (transport.ResponderTransport, bool) {
	if m == nil || m.upstream == nil {
		return nil, false
	}
	rt, ok := m.upstream.(transport.ResponderTransport)
	return rt, ok
}

func (r *responder) sessionHolds(id uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.reg.by {
		if r.reg.by[i].session == id {
			return true
		}
	}
	return false
}

func (r *responder) command(s *session, cmd, data byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch cmd {
	case extNibbleTarget:
		r.targetLocked(s.mux, s, data)
	case extNibbleUntarget:
		r.untargetLocked(s.mux, s, data)
	case extNibbleBegin:
		r.beginLocked(s.mux, s, data)
	case extNibbleStage:
		r.stageLocked(s.mux, s, data)
	case extNibbleCommit:
		r.commitLocked(s.mux, s, data)
	default:
		s.deliverErrorHost()
	}
}

func inadmissibleAddress(addr byte) bool {
	return addr == protocol.AddressBroadcast || addr == protocol.SymbolEscape || addr == protocol.SymbolSyn
}

func (r *responder) targetLocked(m *Mux, s *session, addr byte) {
	if inadmissibleAddress(addr) {
		r.refuseLocked(m, s.id, addr, reasonInadmissible)
		return
	}
	if _, ok := m.responderTransport(); !ok || m.ctx == nil || m.ctx.Err() != nil {
		r.refuseLocked(m, s.id, addr, reasonUnavailable)
		return
	}
	if r.tagDead[s.id] {
		r.refuseLocked(m, s.id, addr, reasonTagExhausted)
		return
	}
	if r.refused[addr] {
		r.refuseLocked(m, s.id, addr, reasonConflict)
		return
	}
	_, kind := r.reg.take(s.id, addr)
	switch kind {
	case regSame:
		if !m.enqueueExt(s.id, sessionFrameTargeted, addr) {
			r.refuseLocked(m, s.id, addr, reasonCapacity)
		}
	case regFresh:
		if !m.enqueueExt(s.id, sessionFrameTargeted, addr) {
			r.reg.clear(s.id, addr)
			r.refuseLocked(m, s.id, addr, reasonCapacity)
		}
	default:
		r.refuseLocked(m, s.id, addr, reasonDuplicate)
	}
}

func (r *responder) refuseLocked(m *Mux, session uint64, addr, reason byte) {
	if !m.enqueueExt(session, sessionFrameTargetError, reason) || !m.enqueueExt(session, sessionFrameUntargeted, addr) {
		go m.RemoveSession(session)
	}
}

func (r *responder) untargetLocked(m *Mux, s *session, addr byte) {
	if r.reg.by[addr].session == s.id {
		r.reg.by[addr] = holder{}
	}
	revokedAll := false
	if r.slot != nil && r.slot.addr == addr && r.slot.key.session == s.id && !r.slot.inFlight {
		r.endSlotLocked(m, endRevoked)
		revokedAll = r.tagDead[s.id]
	}
	if !revokedAll {
		r.queueLocked(m, s.id, sessionFrameUntargeted, addr)
	}
}

func (r *responder) dropSession(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.reg.by {
		if r.reg.by[i].session == id {
			r.reg.by[i] = holder{}
		}
	}
	delete(r.nextTag, id)
	delete(r.tagDead, id)
	if r.slot != nil && r.slot.key.session == id && !r.slot.inFlight {
		r.slot = nil
		if r.phase == phQQ || r.phase == phBody {
			r.phase = phSkip
		}
		r.window = false
	}
}

func (r *responder) endStream(m *Mux) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopPaceLocked()
	if r.slot != nil {
		r.queueLocked(m, r.slot.key.session, sessionFrameSlotEnd, endStreamEnded)
		r.slot = nil
	}
	type pair struct {
		session uint64
		addr    byte
	}
	var pairs []pair
	for i := 0; i < 256; i++ {
		h := r.reg.by[i]
		if h.session != 0 {
			pairs = append(pairs, pair{h.session, byte(i)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].session != pairs[j].session {
			return pairs[i].session < pairs[j].session
		}
		return pairs[i].addr < pairs[j].addr
	})
	for _, p := range pairs {
		r.queueLocked(m, p.session, sessionFrameTargetError, reasonReset)
		r.queueLocked(m, p.session, sessionFrameUntargeted, p.addr)
	}
	r.reg.clearAll()
	r.refused = [256]bool{}
	r.phase = phIdle
	r.window = false
	r.crcSeen = false
	r.foreignAckWatch = false
	r.buf = nil
	r.escPending = false
	r.generation++
}

func (r *responder) allocTagLocked(session uint64) (byte, bool) {
	if r.tagDead[session] {
		return 0, false
	}
	n := r.nextTag[session]
	// nextTag is one byte, so 255 is the last tag it can hold.
	if n == 255 {
		return 0, false
	}
	n++
	r.nextTag[session] = n
	return n, true
}

func (r *responder) endSlotLocked(m *Mux, code byte) {
	slot := r.slot
	r.slot = nil
	r.phase = phSkip
	r.window = false
	r.foreignAckWatch = false
	r.escPending = false
	if slot == nil {
		return
	}
	r.queueLocked(m, slot.key.session, sessionFrameSlotEnd, code)
	if slot.key.tag == 255 {
		r.revokeAllLocked(m, slot.key.session, reasonTagExhausted)
	}
}

func (r *responder) revokeAllLocked(m *Mux, session uint64, reason byte) {
	r.tagDead[session] = true
	var addrs []byte
	for i := 0; i < 256; i++ {
		if r.reg.by[i].session == session {
			addrs = append(addrs, byte(i))
			r.reg.by[i] = holder{}
		}
	}
	for _, addr := range addrs {
		r.queueLocked(m, session, sessionFrameTargetError, reason)
		r.queueLocked(m, session, sessionFrameUntargeted, addr)
	}
}

func (r *responder) revokeAddressLocked(m *Mux, session uint64, addr, reason byte) {
	if r.reg.by[addr].session == session {
		r.reg.by[addr] = holder{}
	}
	r.refused[addr] = true
	r.queueLocked(m, session, sessionFrameTargetError, reason)
	r.queueLocked(m, session, sessionFrameUntargeted, addr)
}

func (r *responder) queueLocked(m *Mux, session uint64, kind sessionFrameKind, payload byte) {
	if !m.enqueueExt(session, kind, payload) {
		go m.RemoveSession(session)
	}
}

func (m *Mux) enqueueExt(id uint64, kind sessionFrameKind, payload byte) bool {
	m.sessionsMu.Lock()
	s := m.sessions[id]
	m.sessionsMu.Unlock()
	if s == nil || s.closed.Load() {
		return false
	}
	select {
	case s.sendCh <- sessionFrame{kind: kind, payload: payload, enqueuedAtNano: time.Since(monoAnchor).Nanoseconds()}:
		return true
	default:
		return false
	}
}

func (r *responder) registrationLiveLocked(slot *respSlot) bool {
	if slot == nil {
		return false
	}
	h := r.reg.by[slot.addr]
	return h.session == slot.key.session && h.epoch == slot.key.epoch
}

func (r *responder) matchesLocked(session uint64, tag byte) bool {
	epoch := uint64(0)
	if r.slot != nil {
		if h := r.reg.by[r.slot.addr]; h.session == session {
			epoch = h.epoch
		}
	}
	return slotKeyMatches(r.slot, session, epoch, r.generation, r.txn, tag)
}

func (r *responder) stopPaceLocked() {
	if r.cancel != nil {
		r.cancel()
	}
	r.liveSeq++
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx = ctx
	r.cancel = cancel
	for {
		select {
		case <-r.echoCh:
		default:
			return
		}
	}
}

func (r *responder) diagnostics() ResponderDiag {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ResponderDiag{
		Late:         r.counters.late,
		StageDiscard: r.counters.stageDiscard,
		Stale:        r.counters.stale,
		WindowSend:   r.counters.windowSend,
		Nack:         r.counters.nack,
		Retransmit:   r.counters.retransmit,
		Competing:    r.counters.competing,
	}
}
