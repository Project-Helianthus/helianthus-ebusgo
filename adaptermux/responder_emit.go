package adaptermux

import (
	"context"
	"errors"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// autoSYNResponseWindow is the AUTO-SYN response slot
// (eBUS Spec_Prot_12 V1.3.1 §7.5). The protocol pages do not state one
// duration. The frame-atomic notes describe that slot as about 35–45 ms
// and treat a longer acknowledgement wait as an engineering tolerance,
// not the slot. Hardware does not report a duration, and the session
// buffer is not a time bound, so the first symbol is late at the lower
// end of that range. An observed SYN still ends the slot sooner.
const autoSYNResponseWindow = 35 * time.Millisecond

// errResponderWindow refuses a SEND while a local target answer is on
// the wire. Callers map it to ERROR_HOST.
var errResponderWindow = errors.New("adaptermux: responder window")

type sendPlan struct {
	symbols []byte
	nack    bool
	i2i     bool
	gen     uint64
	seq     uint64
	epoch   uint64
	ctx     context.Context
}

func (m *Mux) responderWindowOpen() bool {
	if m == nil || m.responder == nil {
		return false
	}
	r := m.responder
	r.mu.Lock()
	open := r.window
	r.mu.Unlock()
	return open
}

func (m *Mux) noteResponderWindowSend() {
	if m == nil || m.responder == nil {
		return
	}
	r := m.responder
	r.mu.Lock()
	r.counters.windowSend++
	r.mu.Unlock()
}

// lateLocked reads the injected clock twice. The second reading is the
// decision: a time that is not strictly before the opened slot is late,
// and nothing is written.
func (r *responder) lateLocked() bool {
	opened := r.now()
	return !r.now().Before(opened.Add(autoSYNResponseWindow))
}

func (r *responder) makePlanLocked(m *Mux, logical []byte, nack, i2i bool) *sendPlan {
	var symbols []byte
	if nack || !m.upstreamSendIsRawSymbol() {
		symbols = append([]byte{}, logical...)
	} else {
		symbols = expandPhysical(logical)
	}
	r.liveSeq++
	return &sendPlan{
		symbols: symbols,
		nack:    nack,
		i2i:     i2i,
		gen:     r.generation,
		seq:     r.liveSeq,
		epoch:   m.arb.lifecycleEpoch.Load(),
		ctx:     r.ctx,
	}
}

func expandPhysical(logical []byte) []byte {
	out := make([]byte, 0, len(logical))
	for _, b := range logical {
		switch b {
		case protocol.SymbolEscape:
			out = append(out, protocol.SymbolEscape, 0x00)
		case protocol.SymbolSyn:
			out = append(out, protocol.SymbolEscape, 0x01)
		default:
			out = append(out, b)
		}
	}
	return out
}

func (r *responder) dispatch(m *Mux, plan *sendPlan) {
	if plan == nil || len(plan.symbols) == 0 {
		return
	}
	if !r.writeSymbol(m, plan.symbols[0]) {
		r.failPlan(m, plan)
		return
	}
	rest := append([]byte{}, plan.symbols[1:]...)
	go r.pace(m, plan, plan.symbols[0], rest)
}

func (r *responder) writeSymbol(m *Mux, b byte) bool {
	rt, ok := m.responderTransport()
	if !ok {
		return false
	}
	_, err := rt.SendResponderBytes([]byte{b})
	return err == nil
}

func (r *responder) pace(m *Mux, plan *sendPlan, sent byte, rest []byte) {
	expect := sent
	for {
		echo, timedOut, stopped := r.waitEcho(plan.ctx)
		if stopped || !r.leaseCurrent(m, plan) {
			return
		}
		if !timedOut && echo != expect {
			r.compete(m, plan)
			return
		}
		if len(rest) == 0 {
			r.finishPlan(m, plan)
			return
		}
		next := rest[0]
		rest = rest[1:]
		if !r.leaseCurrent(m, plan) || !r.writeSymbol(m, next) {
			r.failPlan(m, plan)
			return
		}
		expect = next
	}
}

func (r *responder) waitEcho(ctx context.Context) (byte, bool, bool) {
	select {
	case b := <-r.echoCh:
		return b, false, false
	case <-ctx.Done():
		return 0, false, true
	default:
	}
	timer := time.NewTimer(gatewaySuffixPaceBound)
	defer timer.Stop()
	select {
	case b := <-r.echoCh:
		return b, false, false
	case <-timer.C:
		return 0, true, false
	case <-ctx.Done():
		return 0, false, true
	}
}

func (r *responder) leaseCurrent(m *Mux, plan *sendPlan) bool {
	if m.arb.lifecycleEpoch.Load() != plan.epoch {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return plan.gen == r.generation && plan.seq == r.liveSeq
}

func (r *responder) finishPlan(m *Mux, plan *sendPlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if plan.gen != r.generation || plan.seq != r.liveSeq {
		return
	}
	r.window = false
	if plan.nack {
		r.endSlotLocked(m, endCRC)
		return
	}
	if plan.i2i {
		r.endSlotLocked(m, endCompleted)
		return
	}
	if r.slot != nil {
		r.phase = phReply
	}
}

func (r *responder) failPlan(m *Mux, plan *sendPlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if plan.gen != r.generation || plan.seq != r.liveSeq {
		return
	}
	r.endSlotLocked(m, endBoundary)
}

func (r *responder) compete(m *Mux, plan *sendPlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if plan.gen != r.generation || plan.seq != r.liveSeq {
		return
	}
	r.counters.competing++
	var addr byte
	var sess uint64
	if r.slot != nil {
		addr = r.slot.addr
		sess = r.slot.key.session
		r.refused[addr] = true
	}
	r.endSlotLocked(m, endCompeting)
	if sess != 0 && !r.tagDead[sess] {
		r.revokeAddressLocked(m, sess, addr, reasonConflict)
	}
}

func (r *responder) pushEcho(b byte) {
	select {
	case r.echoCh <- b:
	default:
	}
}
