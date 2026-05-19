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

// TestMasterCRCBroadcastReturnsToIdle verifies broadcast (ZZ=0xFE)
// short-circuit: post-CRC the FSM returns to IDLE (skipping ACK).
// Note: WAIT_TERMINATOR_SYN is not yet wired in this iteration; the
// CRC byte itself is forwarded and the FSM resets.
func TestMasterCRCBroadcastReturnsToIdle(t *testing.T) {
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
	if m.State() != StateIdle {
		t.Fatalf("post-CRC broadcast state = %v, want StateIdle", m.State())
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

// TestWaitMasterAckAcceptsAck verifies ACK advances toward the next
// phase. In this initial iteration, ACK returns to IDLE (slave phases
// not yet wired).
func TestWaitMasterAckAcceptsAck(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	got := m.Feed(ACKByte, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateIdle {
		t.Fatalf("post-ACK state = %v, want StateIdle (slave phases not yet wired)", m.State())
	}
}

// TestWaitMasterAckNackTriggersRetx verifies v8 invariant I6:
// first NACK triggers MASTER_RETX (FSM returns to MASTER_HEADER for
// the resend; retx_count increments).
func TestWaitMasterAckNackTriggersRetx(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	got := m.Feed(NACKByte, false)
	if got != DecisionForward {
		t.Fatalf("decision = %v, want DecisionForward", got)
	}
	if m.State() != StateMasterHeader {
		t.Fatalf("state = %v, want StateMasterHeader (retx restarts header)", m.State())
	}
}

// TestWaitMasterAckSecondNackAborts verifies v8 invariant I6: after
// the spec-mandated single retx, a second NACK aborts the telegram.
func TestWaitMasterAckSecondNackAborts(t *testing.T) {
	t.Parallel()
	m := setupAtWaitMasterAck(t)
	// First NACK → MASTER_RETX
	m.Feed(NACKByte, false)
	// Resend full header
	for _, b := range []byte{0x10, 0x08, 0xB5, 0x09, 0x00} {
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
		StateIdle:          "IDLE",
		StateArbitrating:   "ARBITRATING",
		StateMasterHeader:  "MASTER_HEADER",
		StateMasterData:    "MASTER_DATA",
		StateMasterCRC:     "MASTER_CRC",
		StateWaitMasterAck: "WAIT_MASTER_ACK",
		StateAborted:       "ABORTED",
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
	nonTerminal := []State{StateIdle, StateArbitrating, StateMasterHeader, StateMasterData, StateMasterCRC, StateWaitMasterAck}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("%v.IsTerminal() = true, want false", s)
		}
	}
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
