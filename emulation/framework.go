package emulation

import (
	"errors"
	"fmt"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

var (
	ErrNoMatchingRule        = errors.New("target emulation no matching rule")
	ErrTimingConstraint      = errors.New("target emulation timing constraint")
	ErrInvalidConfiguration  = errors.New("target emulation invalid configuration")
	ErrRequestTargetMismatch = errors.New("target emulation request target mismatch")
)

type RequestMatcher interface {
	Match(frame protocol.Frame) bool
}

type MatchFunc func(frame protocol.Frame) bool

func (fn MatchFunc) Match(frame protocol.Frame) bool {
	if fn == nil {
		return false
	}
	return fn(frame)
}

type ResponseBuilder interface {
	Build(frame protocol.Frame) (ResponsePlan, error)
}

type BuildFunc func(frame protocol.Frame) (ResponsePlan, error)

func (fn BuildFunc) Build(frame protocol.Frame) (ResponsePlan, error) {
	if fn == nil {
		return ResponsePlan{}, fmt.Errorf("missing response builder: %w", ErrInvalidConfiguration)
	}
	return fn(frame)
}

type ResponsePlan struct {
	Delay time.Duration
	Data  []byte
}

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

type Rule struct {
	Name    string
	Matcher RequestMatcher
	Builder ResponseBuilder
	Timing  TimingConstraints
}

type RequestEvent struct {
	At    time.Duration
	Frame protocol.Frame
}

type EmulatedResponse struct {
	Rule      string
	Requested time.Duration
	RespondAt time.Duration
	Frame     protocol.Frame
}

type Target struct {
	Name          string
	Address       byte
	DefaultTiming TimingConstraints
	Rules         []Rule
}

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

func MatchPrimarySecondary(primary, secondary byte) MatchFunc {
	return func(frame protocol.Frame) bool {
		return frame.Primary == primary && frame.Secondary == secondary
	}
}

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
