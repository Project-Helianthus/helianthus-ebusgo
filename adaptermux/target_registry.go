package adaptermux

// holder is the one session that answers an address.
// Epoch 0 is empty. Each accepted registration takes a new epoch,
// and that epoch is never reused. A repeated TARGET by the holder
// keeps the epoch it already has.
type holder struct {
	session uint64
	epoch   uint64
}

const (
	regFresh = iota
	regSame
	regDuplicate
)

// targetRegistry is the address table: one holder per destination.
// The index is the address itself (eBUS Spec_Prot_12 V1.3.1 §4.2, §5.3).
// There is no per-session count; a session may hold every admissible address.
type targetRegistry struct {
	by        [256]holder
	nextEpoch uint64
}

func (g *targetRegistry) empty() bool {
	for i := range g.by {
		if g.by[i].session != 0 {
			return false
		}
	}
	return true
}

func (g *targetRegistry) take(session uint64, addr byte) (uint64, int) {
	h := g.by[addr]
	if h.session == 0 {
		g.nextEpoch++
		g.by[addr] = holder{session: session, epoch: g.nextEpoch}
		return g.nextEpoch, regFresh
	}
	if h.session == session {
		return h.epoch, regSame
	}
	return 0, regDuplicate
}

func (g *targetRegistry) clear(session uint64, addr byte) {
	if g.by[addr].session == session {
		g.by[addr] = holder{}
	}
}

func (g *targetRegistry) clearAll() {
	g.by = [256]holder{}
}
