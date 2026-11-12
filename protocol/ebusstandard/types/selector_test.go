package types

import "testing"

// The selector tests follow 02-l7-types.md §"Length-Dependent Selector"
// requirement to cover: positive selection, no-match, ambiguous-match,
// truncated-selector, truncated-payload, overlong-payload.

func twoBranchSelector() LengthSelector {
	// Branch "short" accepts NN in [1,2], branch "long" accepts NN in [4,8].
	return LengthSelector{Branches: []Branch{
		{Name: "short", MinLen: 1, MaxLen: 2},
		{Name: "long", MinLen: 4, MaxLen: 8},
	}}
}

func TestLengthSelector_PositiveSelection(t *testing.T) {
	sel := twoBranchSelector()
	res := sel.Select(SelectorInput{LengthPrefix: 2, Payload: []byte{0x01, 0x02}})
	if res.Err != nil {
		t.Fatalf("unexpected err: %+v", res.Err)
	}
	if res.Selected != "short" {
		t.Fatalf("want short, got %q", res.Selected)
	}
	res = sel.Select(SelectorInput{LengthPrefix: 6, Payload: make([]byte, 6)})
	if res.Err != nil || res.Selected != "long" {
		t.Fatalf("want long, got %+v", res)
	}
}

func TestLengthSelector_NoMatch(t *testing.T) {
	sel := twoBranchSelector()
	res := sel.Select(SelectorInput{LengthPrefix: 3, Payload: make([]byte, 3)})
	if res.Err == nil || res.Err.Code != ErrCodeUnknownSelector {
		t.Fatalf("want unknown_selector_branch, got %+v", res.Err)
	}
	if res.Selected != "" {
		t.Fatalf("selected must be empty on error")
	}
}

func TestLengthSelector_AmbiguousMatch(t *testing.T) {
	sel := LengthSelector{Branches: []Branch{
		{Name: "a", MinLen: 2, MaxLen: 4},
		{Name: "b", MinLen: 3, MaxLen: 5},
	}}
	res := sel.Select(SelectorInput{LengthPrefix: 3, Payload: make([]byte, 3)})
	if res.Err == nil || res.Err.Code != ErrCodeAmbiguousSelector {
		t.Fatalf("want ambiguous_selector_branch, got %+v", res.Err)
	}
}

func TestLengthSelector_TruncatedSelector(t *testing.T) {
	// A branch whose Match predicate needs payload[0]; providing no payload
	// while LengthPrefix says 1 must surface truncated_payload.
	sel := LengthSelector{Branches: []Branch{
		{
			Name:   "needs_selector_byte",
			MinLen: 1,
			MaxLen: 1,
			Match: func(in SelectorInput) bool {
				if len(in.Payload) < 1 {
					// Should never be called by selector on short payload;
					// selector must short-circuit.
					return true
				}
				return in.Payload[0] == 0x5A
			},
		},
	}}
	res := sel.Select(SelectorInput{LengthPrefix: 1, Payload: nil})
	if res.Err == nil || res.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated_payload, got %+v", res.Err)
	}
}

func TestLengthSelector_TruncatedPayload(t *testing.T) {
	sel := twoBranchSelector()
	// Branch "long" wants 4..8, but payload only has 2 bytes.
	res := sel.Select(SelectorInput{LengthPrefix: 6, Payload: []byte{0, 0}})
	if res.Err == nil || res.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated_payload, got %+v", res.Err)
	}
}

func TestLengthSelector_OverlongPayload(t *testing.T) {
	sel := twoBranchSelector()
	// LengthPrefix=2 (fits "short"), but payload is 5 bytes and no raw tail
	// is allowed. That is overlong_payload.
	res := sel.Select(SelectorInput{LengthPrefix: 2, Payload: make([]byte, 5)})
	if res.Err == nil || res.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload, got %+v", res.Err)
	}
}

// Regression for Codex r3105815892: the non-raw-tail overlong check MUST
// compare payload buffer length to the declared LengthPrefix (NN), not to
// winner.MaxLen. Otherwise, when LengthPrefix < MaxLen, extra trailing bytes
// leak past the declared prefix without triggering overlong_payload.
func TestLengthSelector_OverlongAgainstLengthPrefixNotMaxLen(t *testing.T) {
	sel := LengthSelector{Branches: []Branch{
		{Name: "wide", MinLen: 1, MaxLen: 8},
	}}
	// LengthPrefix=2 (NN) fits the branch, but buffer carries 3 bytes.
	// Old code checked len(Payload) > MaxLen (3 > 8 = false) and accepted the
	// trailing byte. New code must reject it as overlong_payload.
	res := sel.Select(SelectorInput{LengthPrefix: 2, Payload: []byte{0x01, 0x02, 0x03}})
	if res.Err == nil || res.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload for NN<MaxLen with trailing bytes, got %+v", res)
	}
	// Positive: same branch, payload length equals LengthPrefix → accept.
	res = sel.Select(SelectorInput{LengthPrefix: 2, Payload: []byte{0x01, 0x02}})
	if res.Err != nil || res.Selected != "wide" {
		t.Fatalf("positive case want wide, got %+v", res)
	}
}

func TestLengthSelector_RawTailBranchAllowsExtra(t *testing.T) {
	// AllowsRawTail permits the PAYLOAD BUFFER to carry extra bytes beyond
	// the declared LengthPrefix (NN). It does NOT relax the MaxLen bound on
	// LengthPrefix itself.
	sel := LengthSelector{Branches: []Branch{
		{Name: "with_tail", MinLen: 1, MaxLen: 1, AllowsRawTail: true},
	}}
	// LengthPrefix=1 fits MaxLen=1; buffer carries 3 extra trailing bytes
	// which AllowsRawTail permits.
	res := sel.Select(SelectorInput{LengthPrefix: 1, Payload: []byte{1, 2, 3, 4}})
	if res.Err != nil || res.Selected != "with_tail" {
		t.Fatalf("raw-tail branch must accept extra buffer bytes, got %+v", res)
	}
}

// Regression for Codex r3106271521: AllowsRawTail must not let LengthPrefix
// exceed the declared MaxLen. A branch like {MinLen:1, MaxLen:4,
// AllowsRawTail:true} must reject LengthPrefix=100.
func TestLengthSelector_RawTailDoesNotBypassMaxLenOnLengthPrefix(t *testing.T) {
	sel := LengthSelector{Branches: []Branch{
		{Name: "bounded_tail", MinLen: 1, MaxLen: 4, AllowsRawTail: true},
	}}
	res := sel.Select(SelectorInput{LengthPrefix: 100, Payload: make([]byte, 100)})
	if res.Err == nil || res.Err.Code != ErrCodeOverlongPayload {
		t.Fatalf("want overlong_payload for LengthPrefix beyond MaxLen even with AllowsRawTail, got %+v", res)
	}
	if res.Selected != "" {
		t.Fatalf("selected must be empty on error, got %q", res.Selected)
	}
}

// Regression for Codex r3106369840: Select must guard against the declared
// LengthPrefix exceeding the payload buffer BEFORE invoking a branch's Match
// predicate. Otherwise a Match function that reads any byte up to
// LengthPrefix-1 can panic on a short buffer instead of surfacing a proper
// truncated_payload error.
func TestLengthSelector_MatchGuardsAgainstLengthPrefixTruncation(t *testing.T) {
	sel := LengthSelector{Branches: []Branch{
		{
			Name:   "reads_index_one",
			MinLen: 1,
			MaxLen: 2,
			Match: func(in SelectorInput) bool {
				// Declared LengthPrefix tells us byte[1] is valid; selector
				// must ensure that holds before calling us. If it does not,
				// this indexes out of range and panics.
				return in.Payload[1] == 0x5A
			},
		},
	}}
	// LengthPrefix=2 declares two bytes of payload, but buffer holds 1 byte.
	// Branch MinLen=1 passes the old guard; the declared-prefix guard must
	// catch it and return truncated_payload instead of panicking.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Select panicked on short buffer: %v", r)
		}
	}()
	res := sel.Select(SelectorInput{LengthPrefix: 2, Payload: []byte{0x5A}})
	if res.Err == nil || res.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated_payload, got %+v", res)
	}
	if res.Selected != "" {
		t.Fatalf("selected must be empty on error, got %q", res.Selected)
	}
}

// Regression for Codex r3106398435: payload-buffer truncation must be
// reported as truncated_payload even when two branches both match the
// selector inputs by length/predicate. Otherwise a malformed/truncated
// frame (declared LengthPrefix exceeds buffer) is misclassified as
// ambiguous_selector_branch, driving incorrect downstream error handling.
func TestLengthSelector_TruncatedPayloadBeatsAmbiguity(t *testing.T) {
	// Two nil-Match branches both match by length alone; LengthPrefix=4 but
	// the buffer only carries 1 byte. Expect truncated_payload, NOT
	// ambiguous_selector_branch.
	sel := LengthSelector{Branches: []Branch{
		{Name: "a", MinLen: 1, MaxLen: 4},
		{Name: "b", MinLen: 1, MaxLen: 4},
	}}
	res := sel.Select(SelectorInput{LengthPrefix: 4, Payload: []byte{0x01}})
	if res.Err == nil || res.Err.Code != ErrCodeTruncatedPayload {
		t.Fatalf("want truncated_payload, got %+v", res)
	}
	if res.Selected != "" {
		t.Fatalf("selected must be empty on error, got %q", res.Selected)
	}
}

func TestLengthSelector_MatchPredicateDiscriminates(t *testing.T) {
	sel := LengthSelector{Branches: []Branch{
		{
			Name: "block1", MinLen: 2, MaxLen: 2,
			Match: func(in SelectorInput) bool { return in.Payload[0] == 0x01 },
		},
		{
			Name: "block2", MinLen: 2, MaxLen: 2,
			Match: func(in SelectorInput) bool { return in.Payload[0] == 0x02 },
		},
	}}
	res := sel.Select(SelectorInput{LengthPrefix: 2, Payload: []byte{0x02, 0xAA}})
	if res.Err != nil || res.Selected != "block2" {
		t.Fatalf("want block2, got %+v", res)
	}
	res = sel.Select(SelectorInput{LengthPrefix: 2, Payload: []byte{0x01, 0xAA}})
	if res.Err != nil || res.Selected != "block1" {
		t.Fatalf("want block1, got %+v", res)
	}
	res = sel.Select(SelectorInput{LengthPrefix: 2, Payload: []byte{0x99, 0xAA}})
	if res.Err == nil || res.Err.Code != ErrCodeUnknownSelector {
		t.Fatalf("want unknown_selector_branch, got %+v", res)
	}
}
