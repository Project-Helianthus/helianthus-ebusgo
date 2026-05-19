package telegram_fsm

import "testing"

// feedStep describes one byte to feed plus the expected outcome.
type feedStep struct {
	b          byte
	wasEscaped bool
	want       Decision
	wantState  State // expected state AFTER this byte is consumed
}

func runFeedSequence(t *testing.T, m *Machine, steps []feedStep) {
	t.Helper()
	for i, step := range steps {
		got := m.Feed(step.b, step.wasEscaped)
		if got != step.want {
			t.Fatalf("step %d (b=0x%02X esc=%v): decision %v, want %v",
				i, step.b, step.wasEscaped, got, step.want)
		}
		if m.State() != step.wantState {
			t.Fatalf("step %d (b=0x%02X esc=%v): state %v, want %v",
				i, step.b, step.wasEscaped, m.State(), step.wantState)
		}
	}
}

// TestNewIsIdle verifies New returns a Machine in StateIdle.
func TestNewIsIdle(t *testing.T) {
	t.Parallel()
	m := New()
	if m.State() != StateIdle {
		t.Fatalf("New().State() = %v, want StateIdle", m.State())
	}
}

// TestIdleForwardsEverything verifies v8 §3 IDLE pass-through: every
// byte in IDLE is forwarded regardless of value or was_escaped.
func TestIdleForwardsEverything(t *testing.T) {
	t.Parallel()
	m := New()
	runFeedSequence(t, m, []feedStep{
		{b: 0xAA, wasEscaped: false, want: DecisionForward, wantState: StateIdle},
		{b: 0xAA, wasEscaped: true, want: DecisionForward, wantState: StateIdle},
		{b: 0x00, wasEscaped: false, want: DecisionForward, wantState: StateIdle},
		{b: 0xFF, wasEscaped: false, want: DecisionForward, wantState: StateIdle},
	})
}

// TestEnterArbitrating transitions IDLE → ARBITRATING on the
// STARTED event (not on a byte feed). The state advance is purely
// event-driven per v6 §3.
func TestEnterArbitrating(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	if m.State() != StateArbitrating {
		t.Fatalf("State after EnterArbitrating = %v, want StateArbitrating", m.State())
	}
}

// TestArbitratingDropsRawSyn verifies v8 §4: raw 0xAA in ARBITRATING
// is adapter-spurious AA-injection. Drop, do not advance.
func TestArbitratingDropsRawSyn(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	got := m.Feed(0xAA, false)
	if got != DecisionDropAaInjection {
		t.Fatalf("decision = %v, want DecisionDropAaInjection", got)
	}
	if m.State() != StateArbitrating {
		t.Fatalf("state = %v, want StateArbitrating (no advance on drop)", m.State())
	}
}

// TestArbitratingFirstByteEntersMasterHeader verifies the
// ARBITRATING → MASTER_HEADER transition on the first non-SYN byte.
// The first byte is QQ; it counts as byte 1 of the 5-byte header.
func TestArbitratingFirstByteEntersMasterHeader(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	got := m.Feed(0x10, false) // QQ
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateMasterHeader {
		t.Fatalf("state = %v, want StateMasterHeader", m.State())
	}
}

// TestMasterHeaderConsumes5BytesThenMasterData verifies the full
// MASTER_HEADER consumption for NN > 0, transitioning to MASTER_DATA.
func TestMasterHeaderConsumes5BytesThenMasterData(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	runFeedSequence(t, m, []feedStep{
		{b: 0x10, want: DecisionForward, wantState: StateMasterHeader}, // QQ
		{b: 0x08, want: DecisionForward, wantState: StateMasterHeader}, // ZZ
		{b: 0xB5, want: DecisionForward, wantState: StateMasterHeader}, // PB
		{b: 0x09, want: DecisionForward, wantState: StateMasterHeader}, // SB
		{b: 0x03, want: DecisionForward, wantState: StateMasterData},   // NN=3
	})
}

// TestMasterHeaderZeroDataGoesDirectToCRC verifies NN=0 short-circuit
// to MASTER_CRC.
func TestMasterHeaderZeroDataGoesDirectToCRC(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	runFeedSequence(t, m, []feedStep{
		{b: 0x10, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x08, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0xB5, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x09, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x00, want: DecisionForward, wantState: StateMasterCRC}, // NN=0 → skip to CRC
	})
}

// TestMasterHeaderRejectsNNAbove16 verifies v8 §1.7: NN > 16 is a
// spec violation per eBUS V1.3.1, immediately aborting.
func TestMasterHeaderRejectsNNAbove16(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	runFeedSequence(t, m, []feedStep{
		{b: 0x10, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x08, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0xB5, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x09, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x11, want: DecisionProtocolFault, wantState: StateAborted}, // NN=17 spec violation
	})
}

// TestMasterHeaderAcceptsNNExactly16 verifies the boundary case: NN=16
// is the maximum valid data length per eBUS V1.3.1, must NOT abort.
// Pins the `>` comparison in fsm.go against silent regression to `>=`.
func TestMasterHeaderAcceptsNNExactly16(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	runFeedSequence(t, m, []feedStep{
		{b: 0x10, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x08, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0xB5, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x09, want: DecisionForward, wantState: StateMasterHeader},
		{b: 0x10, want: DecisionForward, wantState: StateMasterData}, // NN=16 boundary
	})
}

// TestMasterHeaderDropsRawSyn verifies v8 §4: raw 0xAA in MASTER_HEADER
// (any of the 5 header bytes after QQ) is AA-injection. Drop, stay.
func TestMasterHeaderDropsRawSyn(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	m.Feed(0x10, false) // QQ — now in MASTER_HEADER
	if m.State() != StateMasterHeader {
		t.Fatalf("setup state = %v, want StateMasterHeader", m.State())
	}
	got := m.Feed(0xAA, false)
	if got != DecisionDropAaInjection {
		t.Fatalf("decision = %v, want DecisionDropAaInjection", got)
	}
	if m.State() != StateMasterHeader {
		t.Fatalf("state = %v, want StateMasterHeader (no advance)", m.State())
	}
}

// TestMasterCRCDropsRawSyn verifies v8 §4: raw 0xAA in MASTER_CRC is
// AA-injection (the CRC byte should never be 0xAA raw — payload-0xAA
// encodes as escape-pair, and the actual wire AUTO-SYN only marks
// terminator AFTER WAIT_TERMINATOR_SYN). Drop, stay in MASTER_CRC.
func TestMasterCRCDropsRawSyn(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	for _, b := range []byte{0x10, 0x08, 0xB5, 0x09, 0x00} {
		m.Feed(b, false)
	}
	if m.State() != StateMasterCRC {
		t.Fatalf("setup state = %v, want StateMasterCRC", m.State())
	}
	got := m.Feed(0xAA, false)
	if got != DecisionDropAaInjection {
		t.Fatalf("decision = %v, want DecisionDropAaInjection", got)
	}
	if m.State() != StateMasterCRC {
		t.Fatalf("state = %v, want StateMasterCRC (no advance)", m.State())
	}
}

// TestMasterDataDropsRawSyn verifies v8 §4: raw 0xAA in MASTER_DATA
// is AA-injection. Drop, stay in MASTER_DATA.
func TestMasterDataDropsRawSyn(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	for _, b := range []byte{0x10, 0x08, 0xB5, 0x09, 0x03} {
		m.Feed(b, false)
	}
	if m.State() != StateMasterData {
		t.Fatalf("setup state = %v, want StateMasterData", m.State())
	}
	got := m.Feed(0xAA, false)
	if got != DecisionDropAaInjection {
		t.Fatalf("decision = %v, want DecisionDropAaInjection", got)
	}
	if m.State() != StateMasterData {
		t.Fatalf("state = %v, want StateMasterData (no advance on drop)", m.State())
	}
}

// TestMasterDataAcceptsEscapedAA verifies that an escape-decoded
// logical 0xAA (was_escaped=true) is a legitimate payload byte and
// advances MASTER_DATA's byte counter.
func TestMasterDataAcceptsEscapedAA(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	// QQ ZZ PB SB NN=1 (single data byte)
	for _, b := range []byte{0x10, 0x08, 0xB5, 0x09, 0x01} {
		m.Feed(b, false)
	}
	// Single data byte is a logical 0xAA (escape-decoded).
	got := m.Feed(0xAA, true)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward (escape-decoded AA is legit payload)", got)
	}
	if m.State() != StateMasterCRC {
		t.Fatalf("state = %v, want StateMasterCRC (NN=1 satisfied)", m.State())
	}
}

// TestMasterCRCBroadcastEntersWaitTerminator verifies broadcast
// (ZZ=0xFE) short-circuit: post-CRC the FSM enters WAIT_TERMINATOR_SYN
// (skipping ACK). The final SYN then returns to IDLE.
func TestMasterCRCBroadcastEntersWaitTerminator(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	// QQ ZZ=0xFE PB SB NN=1
	for _, b := range []byte{0x10, 0xFE, 0xB5, 0x09, 0x01} {
		m.Feed(b, false)
	}
	m.Feed(0xCC, false) // data byte
	if m.State() != StateMasterCRC {
		t.Fatalf("pre-CRC state = %v, want StateMasterCRC", m.State())
	}
	got := m.Feed(0x55, false) // CRC byte
	if got != DecisionForward {
		t.Fatalf("CRC decision = %v, want DecisionForward", got)
	}
	if m.State() != StateWaitTerminatorSyn {
		t.Fatalf("post-CRC broadcast state = %v, want StateWaitTerminatorSyn", m.State())
	}
	// Final terminator SYN returns to IDLE.
	got = m.Feed(0xAA, false)
	if got != DecisionForward {
		t.Fatalf("terminator decision = %v, want DecisionForward", got)
	}
	if m.State() != StateIdle {
		t.Fatalf("post-terminator state = %v, want StateIdle", m.State())
	}
}

// TestMasterCRCNonBroadcastGoesToWaitAck verifies non-broadcast frames
// transition MASTER_CRC → WAIT_MASTER_ACK.
func TestMasterCRCNonBroadcastGoesToWaitAck(t *testing.T) {
	t.Parallel()
	m := New()
	m.EnterArbitrating()
	for _, b := range []byte{0x10, 0x08, 0xB5, 0x09, 0x00} { // NN=0
		m.Feed(b, false)
	}
	if m.State() != StateMasterCRC {
		t.Fatalf("pre-CRC state = %v, want StateMasterCRC", m.State())
	}
	got := m.Feed(0x42, false) // CRC byte
	if got != DecisionForward {
		t.Fatalf("CRC decision = %v, want DecisionForward", got)
	}
	if m.State() != StateWaitMasterAck {
		t.Fatalf("post-CRC state = %v, want StateWaitMasterAck", m.State())
	}
}

// TestWaitMasterAckDropsRawSyn verifies v8 §4: raw 0xAA in
// WAIT_MASTER_ACK is AA-injection. Drop, stay.
func TestWaitMasterAckDropsRawSyn(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	got := m.Feed(0xAA, false)
	if got != DecisionDropAaInjection {
		t.Fatalf("decision = %v, want DecisionDropAaInjection", got)
	}
	if m.State() != StateWaitMasterAck {
		t.Fatalf("state = %v, want StateWaitMasterAck (no advance)", m.State())
	}
}

// TestWaitMasterAckAcceptsAck verifies ACK advances to SLAVE_LENGTH
// for the target response phase.
func TestWaitMasterAckAcceptsAck(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	got := m.Feed(ACKByte, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveLength {
		t.Fatalf("post-ACK state = %v, want StateSlaveLength", m.State())
	}
}

// TestWaitMasterAckNackTriggersRetx verifies v8 invariant I6:
// first NACK triggers MASTER_RETX state; the next byte then
// re-enters MASTER_HEADER for the resend.
func TestWaitMasterAckNackTriggersRetx(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	got := m.Feed(NACKByte, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateMasterRetx {
		t.Fatalf("state = %v, want StateMasterRetx (explicit retx state)", m.State())
	}
	// Next byte is the resent QQ; transitions into MASTER_HEADER.
	got = m.Feed(0x10, false)
	if got != DecisionForward {
		t.Fatalf("post-retx QQ decision = %v, want DecisionForward", got)
	}
	if m.State() != StateMasterHeader {
		t.Fatalf("post-retx QQ state = %v, want StateMasterHeader", m.State())
	}
}

// TestWaitMasterAckSecondNackAborts verifies v8 invariant I6: after
// the spec-mandated single retx, a second NACK aborts the telegram.
func TestWaitMasterAckSecondNackAborts(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	// First NACK → MASTER_RETX
	m.Feed(NACKByte, false)
	// Next byte is resent QQ — enters MASTER_HEADER fresh
	m.Feed(0x10, false)
	// Resend remaining header bytes (ZZ PB SB NN=0)
	for _, b := range []byte{0x08, 0xB5, 0x09, 0x00} {
		m.Feed(b, false)
	}
	// CRC
	m.Feed(0x42, false)
	if m.State() != StateWaitMasterAck {
		t.Fatalf("post-retx state = %v, want StateWaitMasterAck", m.State())
	}
	// Second NACK
	got := m.Feed(NACKByte, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateAborted {
		t.Fatalf("state = %v, want StateAborted (retx budget exhausted)", m.State())
	}
}

// TestWaitMasterAckMalformedByteAborts verifies any byte other than
// ACK / NACK / AA in WAIT_MASTER_ACK is a protocol fault.
func TestWaitMasterAckMalformedByteAborts(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	got := m.Feed(0x42, false)
	if got != DecisionProtocolFault {
		t.Fatalf("decision = %v, want DecisionProtocolFault", got)
	}
	if m.State() != StateAborted {
		t.Fatalf("state = %v, want StateAborted", m.State())
	}
}

// TestAbortedFedReportsFault verifies that feeding bytes while in
// ABORTED returns DecisionProtocolFault (caller bug to feed without
// ResetToIdle first).
func TestAbortedFedReportsFault(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	m.Feed(0x42, false) // → ABORTED
	if m.State() != StateAborted {
		t.Fatalf("setup state = %v, want StateAborted", m.State())
	}
	got := m.Feed(0x10, false)
	if got != DecisionProtocolFault {
		t.Fatalf("decision = %v, want DecisionProtocolFault (feed while ABORTED)", got)
	}
}

// TestResetToIdleClearsState verifies ResetToIdle returns to IDLE
// and clears all per-telegram state, allowing the same Machine to
// be reused for the next telegram.
func TestResetToIdleClearsState(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	m.ResetToIdle()
	if m.State() != StateIdle {
		t.Fatalf("post-reset state = %v, want StateIdle", m.State())
	}
	// Machine should work normally for a fresh telegram.
	m.EnterArbitrating()
	got := m.Feed(0x10, false)
	if got != DecisionForward {
		t.Fatalf("post-reset arbitration → header: decision = %v, want DecisionForward", got)
	}
	if m.State() != StateMasterHeader {
		t.Fatalf("post-reset arbitration → header: state = %v, want StateMasterHeader", m.State())
	}
}

// TestRetxCapMatchesSpec drift-guards the MaxRetxPerPhase constant
// against the v8 I6 spec value.
func TestRetxCapMatchesSpec(t *testing.T) {
	t.Parallel()
	if MaxRetxPerPhase != 1 {
		t.Fatalf("MaxRetxPerPhase = %d, want 1 (per spec V1.3.1, v8 I6)", MaxRetxPerPhase)
	}
}

// TestStateAndDecisionStringsAreReadable verifies the String()
// methods produce labels suitable for admin logs.
func TestStateAndDecisionStringsAreReadable(t *testing.T) {
	t.Parallel()
	stateCases := map[State]string{
		StateIdle:              "IDLE",
		StateArbitrating:       "ARBITRATING",
		StateMasterHeader:      "MASTER_HEADER",
		StateMasterData:        "MASTER_DATA",
		StateMasterCRC:         "MASTER_CRC",
		StateWaitMasterAck:     "WAIT_MASTER_ACK",
		StateMasterRetx:        "MASTER_RETX",
		StateSlaveLength:       "SLAVE_LENGTH",
		StateSlaveData:         "SLAVE_DATA",
		StateSlaveCRC:          "SLAVE_CRC",
		StateWaitSlaveAck:      "WAIT_SLAVE_ACK",
		StateSlaveRetx:         "SLAVE_RETX",
		StateWaitTerminatorSyn: "WAIT_TERMINATOR_SYN",
		StateAborted:           "ABORTED",
	}
	for s, want := range stateCases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
	decisionCases := map[Decision]string{
		DecisionForward:         "forward",
		DecisionDropAaInjection: "drop_aa_injection",
		DecisionProtocolFault:   "protocol_fault",
	}
	for d, want := range decisionCases {
		if got := d.String(); got != want {
			t.Errorf("Decision(%d).String() = %q, want %q", d, got, want)
		}
	}
}

// TestStateIsTerminal verifies IsTerminal reports ABORTED as
// terminal and all other states as non-terminal.
func TestStateIsTerminal(t *testing.T) {
	t.Parallel()
	if !StateAborted.IsTerminal() {
		t.Error("StateAborted.IsTerminal() = false, want true")
	}
	nonTerminal := []State{
		StateIdle, StateArbitrating,
		StateMasterHeader, StateMasterData, StateMasterCRC,
		StateWaitMasterAck, StateMasterRetx,
		StateSlaveLength, StateSlaveData, StateSlaveCRC,
		StateWaitSlaveAck, StateSlaveRetx,
		StateWaitTerminatorSyn,
	}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("%v.IsTerminal() = true, want false", s)
		}
	}
}

// setupAtSlaveLength advances the Machine through a complete initiator
// half (QQ ZZ PB SB NN=0 CRC ACK) so the FSM lands in SLAVE_LENGTH
// ready for the target's response.
func setupAtSlaveLength(t *testing.T) *Machine {
	t.Helper()
	m := setupAtWaitMasterAck(t)
	if got := m.Feed(ACKByte, false); got != DecisionForward {
		t.Fatalf("setup ACK decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveLength {
		t.Fatalf("setup state = %v, want StateSlaveLength", m.State())
	}
	return m
}

// TestSlaveLengthConsumesNNAndEntersSlaveData covers normal target
// response: NN' > 0 goes to SLAVE_DATA.
func TestSlaveLengthConsumesNNAndEntersSlaveData(t *testing.T) {
	t.Parallel()
	m := setupAtSlaveLength(t)
	got := m.Feed(0x03, false) // NN' = 3
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveData {
		t.Fatalf("state = %v, want StateSlaveData", m.State())
	}
}

// TestSlaveLengthZeroGoesDirectToSlaveCRC verifies NN'=0 short-circuit
// to SLAVE_CRC. Used by frames where the target has no response payload
// (e.g., initiator-initiator semantics).
func TestSlaveLengthZeroGoesDirectToSlaveCRC(t *testing.T) {
	t.Parallel()
	m := setupAtSlaveLength(t)
	got := m.Feed(0x00, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveCRC {
		t.Fatalf("state = %v, want StateSlaveCRC", m.State())
	}
}

// TestSlaveLengthRejectsNNAbove16 verifies v8 §1.7 + V1.3.1 spec:
// target response > 16 data bytes is illegal.
func TestSlaveLengthRejectsNNAbove16(t *testing.T) {
	t.Parallel()
	m := setupAtSlaveLength(t)
	got := m.Feed(0x11, false) // NN' = 17
	if got != DecisionProtocolFault {
		t.Fatalf("decision = %v, want DecisionProtocolFault", got)
	}
	if m.State() != StateAborted {
		t.Fatalf("state = %v, want StateAborted", m.State())
	}
}

// TestSlaveLengthAcceptsNNExactly16 verifies the NN'=16 boundary
// (max valid).
func TestSlaveLengthAcceptsNNExactly16(t *testing.T) {
	t.Parallel()
	m := setupAtSlaveLength(t)
	got := m.Feed(0x10, false) // NN' = 16
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveData {
		t.Fatalf("state = %v, want StateSlaveData", m.State())
	}
}

// TestSlaveDataConsumesNNBytesThenCRC walks the full SLAVE_DATA
// counter for NN'=2.
func TestSlaveDataConsumesNNBytesThenCRC(t *testing.T) {
	t.Parallel()
	m := setupAtSlaveLength(t)
	m.Feed(0x02, false) // NN' = 2 → SLAVE_DATA
	runFeedSequence(t, m, []feedStep{
		{b: 0xC0, want: DecisionForward, wantState: StateSlaveData},
		{b: 0xC1, want: DecisionForward, wantState: StateSlaveCRC},
	})
}

// TestSlavePhaseDropsRawSyn verifies AA-injection filter active
// throughout the target-response half (SLAVE_LENGTH, SLAVE_DATA, SLAVE_CRC,
// WAIT_SLAVE_ACK). Each phase drops raw 0xAA without advancing.
func TestSlavePhaseDropsRawSyn(t *testing.T) {
	t.Parallel()
	// SLAVE_LENGTH
	m := setupAtSlaveLength(t)
	if got := m.Feed(0xAA, false); got != DecisionDropAaInjection {
		t.Fatalf("SLAVE_LENGTH: decision = %v, want DropAaInjection", got)
	}
	if m.State() != StateSlaveLength {
		t.Fatalf("SLAVE_LENGTH stays after drop: state = %v", m.State())
	}
	// SLAVE_DATA
	m = setupAtSlaveLength(t)
	m.Feed(0x02, false) // → SLAVE_DATA
	if got := m.Feed(0xAA, false); got != DecisionDropAaInjection {
		t.Fatalf("SLAVE_DATA: decision = %v, want DropAaInjection", got)
	}
	if m.State() != StateSlaveData {
		t.Fatalf("SLAVE_DATA stays after drop: state = %v", m.State())
	}
	// SLAVE_CRC
	m = setupAtSlaveLength(t)
	m.Feed(0x00, false) // NN'=0 → SLAVE_CRC
	if got := m.Feed(0xAA, false); got != DecisionDropAaInjection {
		t.Fatalf("SLAVE_CRC: decision = %v, want DropAaInjection", got)
	}
	if m.State() != StateSlaveCRC {
		t.Fatalf("SLAVE_CRC stays after drop: state = %v", m.State())
	}
	// WAIT_SLAVE_ACK
	m = setupAtSlaveLength(t)
	m.Feed(0x00, false) // → SLAVE_CRC
	m.Feed(0x42, false) // CRC → WAIT_SLAVE_ACK
	if got := m.Feed(0xAA, false); got != DecisionDropAaInjection {
		t.Fatalf("WAIT_SLAVE_ACK: decision = %v, want DropAaInjection", got)
	}
	if m.State() != StateWaitSlaveAck {
		t.Fatalf("WAIT_SLAVE_ACK stays after drop: state = %v", m.State())
	}
}

// TestMasterRetxDropsRawSyn verifies v8 §4: raw 0xAA in MASTER_RETX
// is AA-injection (no real wire bytes should arrive between the NACK
// and the resent QQ). Drop, stay.
func TestMasterRetxDropsRawSyn(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	m.Feed(NACKByte, false) // → MASTER_RETX
	if m.State() != StateMasterRetx {
		t.Fatalf("setup state = %v, want StateMasterRetx", m.State())
	}
	got := m.Feed(0xAA, false)
	if got != DecisionDropAaInjection {
		t.Fatalf("decision = %v, want DecisionDropAaInjection", got)
	}
	if m.State() != StateMasterRetx {
		t.Fatalf("state = %v, want StateMasterRetx (no advance)", m.State())
	}
}

// TestSlaveRetxDropsRawSyn verifies v8 §4: raw 0xAA in SLAVE_RETX
// is AA-injection (no real wire bytes should arrive between the NACK
// and the resent NN'). Drop, stay.
func TestSlaveRetxDropsRawSyn(t *testing.T) {
	t.Parallel()
	m := setupAtWaitSlaveAck(t)
	m.Feed(NACKByte, false) // → SLAVE_RETX
	if m.State() != StateSlaveRetx {
		t.Fatalf("setup state = %v, want StateSlaveRetx", m.State())
	}
	got := m.Feed(0xAA, false)
	if got != DecisionDropAaInjection {
		t.Fatalf("decision = %v, want DecisionDropAaInjection", got)
	}
	if m.State() != StateSlaveRetx {
		t.Fatalf("state = %v, want StateSlaveRetx (no advance)", m.State())
	}
}

// TestRetxCountersAreIndependent verifies masterRetxCount and
// slaveRetxCount are independent per v8 I6. A master phase that
// completed without NACK leaves the master budget at 0; a subsequent
// slave NACK must still get SLAVE_RETX (not be blocked by some
// fictitious shared cap). And vice-versa.
func TestRetxCountersAreIndependent(t *testing.T) {
	t.Parallel()
	// Forward direction: master phase succeeds, slave NACK works.
	m := setupAtWaitSlaveAck(t) // master ACKed cleanly, no master NACK
	got := m.Feed(NACKByte, false)
	if got != DecisionForward {
		t.Fatalf("forward slave NACK decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveRetx {
		t.Fatalf("forward slave NACK state = %v, want StateSlaveRetx (master budget must not block)", m.State())
	}

	// Inverse direction: master NACK + retry succeeds, slave still has
	// its full budget. To exercise: do master NACK, complete the retx,
	// then do a slave NACK and verify SLAVE_RETX (not abort).
	m = setupAtWaitMasterAck(t)
	m.Feed(NACKByte, false) // → MASTER_RETX
	// Retx header (QQ ZZ PB SB NN=0)
	for _, b := range []byte{0x10, 0x08, 0xB5, 0x09, 0x00} {
		m.Feed(b, false)
	}
	m.Feed(0x42, false)    // CRC → WAIT_MASTER_ACK
	m.Feed(ACKByte, false) // → SLAVE_LENGTH
	m.Feed(0x00, false)    // NN'=0 → SLAVE_CRC
	m.Feed(0x42, false)    // CRC → WAIT_SLAVE_ACK
	if m.State() != StateWaitSlaveAck {
		t.Fatalf("setup post-master-retx state = %v, want StateWaitSlaveAck", m.State())
	}
	// Now slave NACK — must enter SLAVE_RETX, not ABORT.
	got = m.Feed(NACKByte, false)
	if got != DecisionForward {
		t.Fatalf("inverse slave NACK decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveRetx {
		t.Fatalf("inverse slave NACK state = %v, want StateSlaveRetx (master retx must not exhaust slave budget)", m.State())
	}
}

// TestWaitSlaveAckAcceptsAck verifies ACK → WAIT_TERMINATOR_SYN.
func TestWaitSlaveAckAcceptsAck(t *testing.T) {
	t.Parallel()
	m := setupAtWaitSlaveAck(t)
	got := m.Feed(ACKByte, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateWaitTerminatorSyn {
		t.Fatalf("state = %v, want StateWaitTerminatorSyn", m.State())
	}
	// Final SYN returns to IDLE.
	got = m.Feed(SynByte, false)
	if got != DecisionForward {
		t.Fatalf("terminator decision = %v, want DecisionForward", got)
	}
	if m.State() != StateIdle {
		t.Fatalf("post-terminator state = %v, want StateIdle", m.State())
	}
}

// TestWaitSlaveAckNackTriggersSlaveRetx verifies first NACK →
// SLAVE_RETX; next byte is the resent NN'.
func TestWaitSlaveAckNackTriggersSlaveRetx(t *testing.T) {
	t.Parallel()
	m := setupAtWaitSlaveAck(t)
	got := m.Feed(NACKByte, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveRetx {
		t.Fatalf("state = %v, want StateSlaveRetx", m.State())
	}
	// Next byte is the resent NN'. Direct entry to SLAVE_DATA / SLAVE_CRC.
	got = m.Feed(0x00, false) // NN' = 0 → straight to SLAVE_CRC
	if got != DecisionForward {
		t.Fatalf("retx NN' decision = %v, want DecisionForward", got)
	}
	if m.State() != StateSlaveCRC {
		t.Fatalf("retx NN'=0 state = %v, want StateSlaveCRC", m.State())
	}
}

// TestWaitSlaveAckSecondNackAborts verifies v8 I6: slave_retx_count
// caps at 1; second NACK aborts.
func TestWaitSlaveAckSecondNackAborts(t *testing.T) {
	t.Parallel()
	m := setupAtWaitSlaveAck(t)
	// First NACK → SLAVE_RETX
	m.Feed(NACKByte, false)
	// Resent NN'=0 → SLAVE_CRC
	m.Feed(0x00, false)
	// Resent CRC → WAIT_SLAVE_ACK
	m.Feed(0x42, false)
	if m.State() != StateWaitSlaveAck {
		t.Fatalf("post-target-retx state = %v, want StateWaitSlaveAck", m.State())
	}
	// Second NACK
	got := m.Feed(NACKByte, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateAborted {
		t.Fatalf("state = %v, want StateAborted", m.State())
	}
}

// TestWaitTerminatorSynForwardsRawSynUnlikeOtherPhases is the v8 §4
// carve-out: WAIT_TERMINATOR_SYN is the ONE phase where raw 0xAA is
// legitimate (it's the terminator). Forward, transition to IDLE.
func TestWaitTerminatorSynForwardsRawSynUnlikeOtherPhases(t *testing.T) {
	t.Parallel()
	m := setupAtWaitSlaveAck(t)
	m.Feed(ACKByte, false) // → WAIT_TERMINATOR_SYN
	got := m.Feed(0xAA, false)
	if got != DecisionForward {
		t.Fatalf("terminator decision = %v, want DecisionForward", got)
	}
	if m.State() != StateIdle {
		t.Fatalf("post-terminator state = %v, want StateIdle", m.State())
	}
}

// TestWaitTerminatorSynRejectsNonSyn verifies non-SYN bytes during
// WAIT_TERMINATOR_SYN are protocol faults.
func TestWaitTerminatorSynRejectsNonSyn(t *testing.T) {
	t.Parallel()
	m := setupAtWaitSlaveAck(t)
	m.Feed(ACKByte, false) // → WAIT_TERMINATOR_SYN
	got := m.Feed(0x42, false)
	if got != DecisionProtocolFault {
		t.Fatalf("decision = %v, want DecisionProtocolFault", got)
	}
	if m.State() != StateAborted {
		t.Fatalf("state = %v, want StateAborted", m.State())
	}
}

// setupAtWaitSlaveAck advances through initiator+target halves so the
// FSM lands in WAIT_SLAVE_ACK ready for the initiator's ACK/NACK.
// Uses NN=0 / NN'=0 for brevity.
func setupAtWaitSlaveAck(t *testing.T) *Machine {
	t.Helper()
	m := setupAtSlaveLength(t)
	m.Feed(0x00, false) // NN' = 0 → SLAVE_CRC
	m.Feed(0x42, false) // CRC → WAIT_SLAVE_ACK
	if m.State() != StateWaitSlaveAck {
		t.Fatalf("setup state = %v, want StateWaitSlaveAck", m.State())
	}
	return m
}

// setupAtWaitMasterAck constructs a Machine that has progressed
// through IDLE → ARBITRATING → MASTER_HEADER (NN=0) → MASTER_CRC →
// WAIT_MASTER_ACK with a non-broadcast frame.
func setupAtWaitMasterAck(t *testing.T) *Machine {
	t.Helper()
	m := New()
	m.EnterArbitrating()
	// QQ ZZ PB SB NN=0 then CRC. ZZ != 0xFE so the FSM goes to
	// WAIT_MASTER_ACK after CRC.
	for _, b := range []byte{0x10, 0x08, 0xB5, 0x09, 0x00} {
		m.Feed(b, false)
	}
	m.Feed(0x42, false) // CRC
	if m.State() != StateWaitMasterAck {
		t.Fatalf("setup pre-condition: state = %v, want StateWaitMasterAck", m.State())
	}
	return m
}
