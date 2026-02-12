package emulation

import (
	"fmt"
	"time"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

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

type QueryStep struct {
	Advance time.Duration
	Frame   protocol.Frame
}

type Harness struct {
	target  *Target
	now     time.Duration
	history []EmulatedResponse
}

func NewHarness(target *Target) *Harness {
	return &Harness{
		target: target,
	}
}

func (h *Harness) Now() time.Duration {
	if h == nil {
		return 0
	}
	return h.now
}

func (h *Harness) Advance(delta time.Duration) {
	if h == nil || delta <= 0 {
		return
	}
	h.now += delta
}

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
