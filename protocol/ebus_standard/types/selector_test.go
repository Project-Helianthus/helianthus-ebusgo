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

func TestLengthSelector_RawTailBranchAllowsExtra(t *testing.T) {
	sel := LengthSelector{Branches: []Branch{
		{Name: "with_tail", MinLen: 1, MaxLen: 1, AllowsRawTail: true},
	}}
	res := sel.Select(SelectorInput{LengthPrefix: 4, Payload: []byte{1, 2, 3, 4}})
	if res.Err != nil || res.Selected != "with_tail" {
		t.Fatalf("raw-tail branch must accept extra bytes, got %+v", res)
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
