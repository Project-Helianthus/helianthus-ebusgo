package types

// SelectorInput carries the selector inputs enumerated in 02-l7-types.md
// §"Length-Dependent Selector".
type SelectorInput struct {
	PB                byte
	SB                byte
	Direction         string // "request" / "response"
	Role              string // "initiator" / "responder"
	LengthPrefix      int    // NN
	Payload           []byte
	SelectorDecoderID string
}

// Branch is a catalog branch candidate for a length-dependent selector.
//
// MinLen and MaxLen are inclusive bounds on the declared LengthPrefix (NN).
// Both bounds are enforced for every branch regardless of AllowsRawTail —
// AllowsRawTail only relaxes the PAYLOAD BUFFER overlong check (it allows the
// buffer to carry extra bytes beyond the declared LengthPrefix as a raw tail).
// See rule 5 in 02-l7-types.md §"Length-Dependent Selector".
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
//
// Evaluation order:
//  1. For each branch, check whether the declared LengthPrefix fits the
//     branch's [MinLen, MaxLen] window. The MaxLen upper bound applies to
//     every branch — AllowsRawTail does NOT relax the LengthPrefix bound.
//  2. For each window-fitting branch, evaluate Match (if any), guarding
//     against payload-buffer truncation before invoking Match.
//  3. Zero matches:
//     - If at least one branch's window contains LengthPrefix but Match
//     rejected, return unknown_selector_branch.
//     - If LengthPrefix is shorter than every branch's MinLen, return
//     truncated_payload.
//     - If LengthPrefix exceeds every branch's MaxLen, return
//     overlong_payload.
//     - Otherwise return unknown_selector_branch.
//  4. Multiple matches → ambiguous_selector_branch.
//  5. One match → payload-buffer length sanity check (truncation /
//     overlong) relative to that branch. AllowsRawTail relaxes the buffer
//     upper-bound check only (buffer may exceed LengthPrefix).
func (s LengthSelector) Select(input SelectorInput) SelectorResult {
	var (
		windowFit []Branch
	)
	for _, br := range s.Branches {
		if input.LengthPrefix < br.MinLen {
			continue
		}
		// MaxLen bounds the declared LengthPrefix (NN) for every branch.
		// AllowsRawTail only relaxes the payload-buffer overlong check below,
		// not the declared length itself.
		if input.LengthPrefix > br.MaxLen {
			continue
		}
		windowFit = append(windowFit, br)
	}

	if len(windowFit) == 0 {
		// Disambiguate: shorter than every branch → not enough info; longer
		// than every branch without raw-tail → overlong; else unknown.
		allTooShort := len(s.Branches) > 0
		allTooLong := len(s.Branches) > 0
		for _, br := range s.Branches {
			if input.LengthPrefix >= br.MinLen {
				allTooShort = false
			}
			if input.LengthPrefix <= br.MaxLen {
				allTooLong = false
			}
		}
		if allTooShort {
			return SelectorResult{Err: newDecodeError(ErrCodeTruncatedPayload, "declared length shorter than any branch MinLen")}
		}
		if allTooLong {
			return SelectorResult{Err: newDecodeError(ErrCodeOverlongPayload, "declared length longer than any branch MaxLen")}
		}
		return SelectorResult{Err: newDecodeError(ErrCodeUnknownSelector, "no branch matches selector inputs")}
	}

	// Evaluate Match predicates against window-fitting candidates.
	var matches []Branch
	for _, br := range windowFit {
		if br.Match != nil {
			// Predicate needs at least MinLen bytes available in the buffer.
			if len(input.Payload) < br.MinLen {
				return SelectorResult{Err: newDecodeError(ErrCodeTruncatedPayload, "payload shorter than branch MinLen")}
			}
			// Predicates may read any byte up to the declared LengthPrefix
			// (NN). If the buffer is shorter than the declared prefix, a
			// Match function that indexes beyond len(Payload)-1 would panic.
			// Surface truncated_payload before invoking the predicate.
			if len(input.Payload) < input.LengthPrefix {
				return SelectorResult{Err: newDecodeError(ErrCodeTruncatedPayload, "payload shorter than declared LengthPrefix")}
			}
			if !br.Match(input) {
				continue
			}
		}
		matches = append(matches, br)
	}

	// Payload-buffer truncation check is universal: if the buffer carries
	// fewer bytes than the declared LengthPrefix, the frame is malformed
	// regardless of how many (or which) branches matched. Checking this
	// BEFORE the ambiguity branch prevents a truncated frame from being
	// misclassified as ambiguous_selector_branch when two nil-Match (or
	// predicate-accepting) branches happen to fit by length alone. See
	// Codex r3106398435.
	if len(matches) > 0 && len(input.Payload) < input.LengthPrefix {
		return SelectorResult{Err: newDecodeError(ErrCodeTruncatedPayload, "payload shorter than declared length")}
	}

	switch len(matches) {
	case 0:
		return SelectorResult{Err: newDecodeError(ErrCodeUnknownSelector, "no branch matches selector inputs")}
	case 1:
		winner := matches[0]
		// Buffer upper-bound check relative to winner.
		if !winner.AllowsRawTail && len(input.Payload) > input.LengthPrefix {
			return SelectorResult{Err: newDecodeError(ErrCodeOverlongPayload, "payload buffer longer than declared LengthPrefix")}
		}
		return SelectorResult{Selected: winner.Name}
	default:
		return SelectorResult{Err: newDecodeError(ErrCodeAmbiguousSelector, "multiple branches match the same selector inputs")}
	}
}
