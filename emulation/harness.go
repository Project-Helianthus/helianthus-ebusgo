package emulation

import (
	"fmt"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// ResponseEnvelope is an inclusive bound on the delay between request and
// response that ValidateResponseEnvelope applies to recorded responses. A
// zero MaxDelay means no upper bound; negative bounds are invalid.
type ResponseEnvelope struct {
	MinDelay time.Duration
	MaxDelay time.Duration
}

func (e ResponseEnvelope) validate() error {
	if e.MinDelay < 0 || e.MaxDelay < 0 {
		return fmt.Errorf("negative response envelope: %w", ErrInvalidConfiguration)
	}
	if e.MaxDelay > 0 && e.MinDelay > e.MaxDelay {
		return fmt.Errorf("envelope min delay exceeds max delay: %w", ErrInvalidConfiguration)
	}
	return nil
}

// ValidateResponseEnvelope checks that every response's delay, RespondAt
// minus Requested, lies inside envelope. It fails with
// ErrInvalidConfiguration for an invalid envelope and with
// ErrTimingConstraint, naming its index, for the first response outside it.
// An empty list passes.
func ValidateResponseEnvelope(responses []EmulatedResponse, envelope ResponseEnvelope) error {
	if err := envelope.validate(); err != nil {
		return err
	}
	for idx, response := range responses {
		delay := response.RespondAt - response.Requested
		if delay < envelope.MinDelay {
			return fmt.Errorf(
				"response[%d] delay %s below envelope min %s: %w",
				idx,
				delay,
				envelope.MinDelay,
				ErrTimingConstraint,
			)
		}
		if envelope.MaxDelay > 0 && delay > envelope.MaxDelay {
			return fmt.Errorf(
				"response[%d] delay %s above envelope max %s: %w",
				idx,
				delay,
				envelope.MaxDelay,
				ErrTimingConstraint,
			)
		}
	}
	return nil
}

// QueryStep is one step of Harness.RunSequence: advance the virtual clock
// by Advance, which must not be negative, then send Frame.
type QueryStep struct {
	Advance time.Duration
	Frame   protocol.Frame
}

// Harness drives one Target on a virtual clock that starts at zero and
// records every successful response. It is not safe for concurrent use.
type Harness struct {
	target  *Target
	now     time.Duration
	history []EmulatedResponse
}

// NewHarness returns a harness for target with the clock at zero and an
// empty history. The harness keeps the pointer, so later changes to the
// target apply to later queries. A nil target is accepted here, and every
// query then fails with ErrInvalidConfiguration.
func NewHarness(target *Target) *Harness {
	return &Harness{
		target: target,
	}
}

// Now returns the virtual time elapsed since the harness started; it is
// zero for a nil harness.
func (h *Harness) Now() time.Duration {
	if h == nil {
		return 0
	}
	return h.now
}

// Advance moves the virtual clock forward by delta. A delta that is zero or
// negative, or a nil harness, leaves it unchanged.
func (h *Harness) Advance(delta time.Duration) {
	if h == nil || delta <= 0 {
		return
	}
	h.now += delta
}

// Query sends frame to the target at the current virtual time, without
// advancing the clock, and records the response in the history. It returns
// the error of Target.Emulate unchanged and records nothing on failure; a
// nil harness or target fails with ErrInvalidConfiguration.
func (h *Harness) Query(frame protocol.Frame) (EmulatedResponse, error) {
	if h == nil || h.target == nil {
		return EmulatedResponse{}, fmt.Errorf("missing harness target: %w", ErrInvalidConfiguration)
	}
	response, err := h.target.Emulate(RequestEvent{
		At:    h.now,
		Frame: frame,
	})
	if err != nil {
		return EmulatedResponse{}, err
	}
	h.history = append(h.history, response)
	return response, nil
}

// RunSequence performs the steps in order and returns their responses. It
// stops at the first failing step and returns only an error that names the
// step index: ErrInvalidConfiguration for a negative advance, otherwise the
// query error. The clock keeps every advance already applied, including
// that of a step whose query failed, and successful responses stay in
// History.
func (h *Harness) RunSequence(steps []QueryStep) ([]EmulatedResponse, error) {
	if h == nil || h.target == nil {
		return nil, fmt.Errorf("missing harness target: %w", ErrInvalidConfiguration)
	}
	out := make([]EmulatedResponse, 0, len(steps))
	for idx, step := range steps {
		if step.Advance < 0 {
			return nil, fmt.Errorf("step[%d] negative advance: %w", idx, ErrInvalidConfiguration)
		}
		h.Advance(step.Advance)
		response, err := h.Query(step.Frame)
		if err != nil {
			return nil, fmt.Errorf("step[%d]: %w", idx, err)
		}
		out = append(out, response)
	}
	return out, nil
}

// History returns a copy of the recorded responses in query order, with
// copied frame data, or nil when there are none.
func (h *Harness) History() []EmulatedResponse {
	if h == nil || len(h.history) == 0 {
		return nil
	}
	out := make([]EmulatedResponse, len(h.history))
	for idx := range h.history {
		out[idx] = h.history[idx]
		out[idx].Frame.Data = append([]byte(nil), h.history[idx].Frame.Data...)
	}
	return out
}
