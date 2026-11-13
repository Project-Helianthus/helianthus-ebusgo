package emulation

import (
	"errors"
	"fmt"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

var (
	// ErrNoMatchingRule is returned when no rule of a target accepts the
	// request, or when a builder cannot answer the request it matched.
	ErrNoMatchingRule = errors.New("target emulation no matching rule")
	// ErrTimingConstraint is returned when a planned or recorded response
	// delay is negative or falls outside the applicable bounds.
	ErrTimingConstraint = errors.New("target emulation timing constraint")
	// ErrInvalidConfiguration is returned for an unusable target, rule,
	// profile, harness or bound: a missing matcher, builder or target, a
	// required field left empty, negative bounds or a minimum above the
	// maximum.
	ErrInvalidConfiguration = errors.New("target emulation invalid configuration")
	// ErrRequestTargetMismatch is returned when a request is addressed to
	// another address than the emulated target's.
	ErrRequestTargetMismatch = errors.New("target emulation request target mismatch")
)

// RequestMatcher decides whether a rule answers a request frame. Match
// receives the caller's frame and must not modify its data.
type RequestMatcher interface {
	Match(frame protocol.Frame) bool
}

// MatchFunc adapts a function to RequestMatcher.
type MatchFunc func(frame protocol.Frame) bool

// Match calls fn; a nil MatchFunc matches nothing.
func (fn MatchFunc) Match(frame protocol.Frame) bool {
	if fn == nil {
		return false
	}
	return fn(frame)
}

// ResponseBuilder produces the planned answer to a request that the rule's
// matcher accepted. An error it returns is passed to the caller of
// Target.Emulate unchanged.
type ResponseBuilder interface {
	Build(frame protocol.Frame) (ResponsePlan, error)
}

// BuildFunc adapts a function to ResponseBuilder.
type BuildFunc func(frame protocol.Frame) (ResponsePlan, error)

// Build calls fn; a nil BuildFunc fails with ErrInvalidConfiguration.
func (fn BuildFunc) Build(frame protocol.Frame) (ResponsePlan, error) {
	if fn == nil {
		return ResponsePlan{}, fmt.Errorf("missing response builder: %w", ErrInvalidConfiguration)
	}
	return fn(frame)
}

// ResponsePlan is a builder's answer: the response data bytes and the
// virtual delay after the request at which the response is due. The target
// copies Data into the response, so a builder may reuse its buffer.
type ResponsePlan struct {
	Delay time.Duration
	Data  []byte
}

// TimingConstraints bounds a response delay. Both bounds are inclusive and
// must not be negative; a zero maximum means no upper bound. A value with
// both bounds zero is unset and makes the rule use the target's default
// timing.
type TimingConstraints struct {
	MinResponseDelay time.Duration
	MaxResponseDelay time.Duration
}

func (c TimingConstraints) validate(delay time.Duration) error {
	if c.MinResponseDelay < 0 || c.MaxResponseDelay < 0 {
		return fmt.Errorf("negative timing constraint: %w", ErrInvalidConfiguration)
	}
	if c.MaxResponseDelay > 0 && c.MinResponseDelay > c.MaxResponseDelay {
		return fmt.Errorf("min delay exceeds max delay: %w", ErrInvalidConfiguration)
	}
	if delay < 0 {
		return fmt.Errorf("negative response delay: %w", ErrTimingConstraint)
	}
	if delay < c.MinResponseDelay {
		return fmt.Errorf("response delay %s below min %s: %w", delay, c.MinResponseDelay, ErrTimingConstraint)
	}
	if c.MaxResponseDelay > 0 && delay > c.MaxResponseDelay {
		return fmt.Errorf("response delay %s above max %s: %w", delay, c.MaxResponseDelay, ErrTimingConstraint)
	}
	return nil
}

func (c TimingConstraints) active() bool {
	return c.MinResponseDelay != 0 || c.MaxResponseDelay != 0
}

// Rule answers the requests its Matcher accepts with the plan from its
// Builder, checked against Timing when Timing is set. Name labels the rule
// in errors and in EmulatedResponse.Rule. Both Matcher and Builder are
// required.
type Rule struct {
	Name    string
	Matcher RequestMatcher
	Builder ResponseBuilder
	Timing  TimingConstraints
}

// RequestEvent is one request frame delivered to a target at virtual time
// At, measured from the start of the emulation run.
type RequestEvent struct {
	At    time.Duration
	Frame protocol.Frame
}

// EmulatedResponse is the answer of a target: the name of the rule that
// produced it, the virtual request time, the virtual time the response is
// due (Requested plus the planned delay) and the response frame.
type EmulatedResponse struct {
	Rule      string
	Requested time.Duration
	RespondAt time.Duration
	Frame     protocol.Frame
}

// Target is an in-memory eBUS participant at Address that answers
// requests from Rules, tried in order. DefaultTiming applies to every rule
// whose own Timing is unset. A Target has no internal state, so concurrent
// Emulate calls are safe as long as nobody modifies the Target and its
// matchers and builders are safe themselves.
type Target struct {
	Name          string
	Address       byte
	DefaultTiming TimingConstraints
	Rules         []Rule
}

// Emulate answers one request with the first matching rule. The response
// frame is addressed from the target back to the request's source, keeps
// the request's primary and secondary command bytes and carries a copy of
// the planned data. It fails with ErrInvalidConfiguration for a nil
// target, a rule without matcher or builder (rules are checked in order, a
// missing builder only once its rule matches) or invalid timing bounds;
// ErrRequestTargetMismatch when the request is for another address;
// ErrTimingConstraint when the planned delay is out of bounds;
// ErrNoMatchingRule when no rule matches; and with the builder's own error
// unchanged.
func (t *Target) Emulate(event RequestEvent) (EmulatedResponse, error) {
	if t == nil {
		return EmulatedResponse{}, fmt.Errorf("missing target: %w", ErrInvalidConfiguration)
	}
	if event.Frame.Target != t.Address {
		return EmulatedResponse{}, fmt.Errorf(
			"request target 0x%02x does not match emulated target 0x%02x: %w",
			event.Frame.Target,
			t.Address,
			ErrRequestTargetMismatch,
		)
	}

	for _, rule := range t.Rules {
		if rule.Matcher == nil {
			return EmulatedResponse{}, fmt.Errorf("rule %q missing matcher: %w", rule.Name, ErrInvalidConfiguration)
		}
		if !rule.Matcher.Match(event.Frame) {
			continue
		}
		if rule.Builder == nil {
			return EmulatedResponse{}, fmt.Errorf("rule %q missing builder: %w", rule.Name, ErrInvalidConfiguration)
		}
		plan, err := rule.Builder.Build(event.Frame)
		if err != nil {
			return EmulatedResponse{}, err
		}

		timing := rule.Timing
		if !timing.active() {
			timing = t.DefaultTiming
		}
		if err := timing.validate(plan.Delay); err != nil {
			return EmulatedResponse{}, err
		}

		return EmulatedResponse{
			Rule:      rule.Name,
			Requested: event.At,
			RespondAt: event.At + plan.Delay,
			Frame: protocol.Frame{
				Source:    t.Address,
				Target:    event.Frame.Source,
				Primary:   event.Frame.Primary,
				Secondary: event.Frame.Secondary,
				Data:      append([]byte(nil), plan.Data...),
			},
		}, nil
	}

	return EmulatedResponse{}, fmt.Errorf("request pb=0x%02x sb=0x%02x: %w", event.Frame.Primary, event.Frame.Secondary, ErrNoMatchingRule)
}

// MatchPrimarySecondary matches every request with the given primary and
// secondary command bytes, whatever its data.
func MatchPrimarySecondary(primary, secondary byte) MatchFunc {
	return func(frame protocol.Frame) bool {
		return frame.Primary == primary && frame.Secondary == secondary
	}
}

// MatchPrimarySecondaryWithPrefix matches requests with the given command
// bytes whose data starts with prefix; an empty prefix matches any data. The
// prefix is copied, so later changes to the caller's slice have no effect.
func MatchPrimarySecondaryWithPrefix(primary, secondary byte, prefix []byte) MatchFunc {
	copied := append([]byte(nil), prefix...)
	return func(frame protocol.Frame) bool {
		if frame.Primary != primary || frame.Secondary != secondary {
			return false
		}
		if len(copied) == 0 {
			return true
		}
		if len(frame.Data) < len(copied) {
			return false
		}
		for idx := range copied {
			if frame.Data[idx] != copied[idx] {
				return false
			}
		}
		return true
	}
}
