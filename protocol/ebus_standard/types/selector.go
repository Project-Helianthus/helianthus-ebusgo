package types

// SelectorInput carries the selector inputs enumerated in 02-l7-types.md
// §"Length-Dependent Selector".
type SelectorInput struct {
	PB                    byte
	SB                    byte
	Direction             string // "request" / "response"
	Role                  string // "initiator" / "responder"
	LengthPrefix          int    // NN
	Payload               []byte
	SelectorDecoderID     string
}

// Branch is a catalog branch candidate for a length-dependent selector.
//
// MinLen and MaxLen are inclusive bounds on the payload length. AllowsRawTail
// indicates whether bytes beyond the consumed portion are permitted without
// triggering OverlongPayload; see rule 5 in 02-l7-types.md §"Length-Dependent
// Selector".
//
// Match is an optional predicate that consumes selected bytes (e.g. a
// selector byte). A nil Match means the length bounds are the only selector.
// Match MUST NOT mutate its argument.
type Branch struct {
	Name          string
	MinLen        int
	MaxLen        int
	AllowsRawTail bool
	Match         func(input SelectorInput) bool
}

// LengthSelector resolves a catalog branch strictly by length / payload.
type LengthSelector struct {
	Branches []Branch
}

// SelectorResult carries the decode outcome. When Err is non-nil, Selected
// is empty.
type SelectorResult struct {
	Selected string
	Err      *DecodeError
}

// Select evaluates the branches against input and returns SelectorResult.
func (s LengthSelector) Select(input SelectorInput) SelectorResult {
	panic("ebus_standard/types.LengthSelector.Select: not implemented")
}
