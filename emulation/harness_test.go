package emulation

import (
	"errors"
	"testing"
	"time"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

func TestHarnessRunSequence_Deterministic(t *testing.T) {
	t.Parallel()

	target := &Target{
		Name:    "deterministic",
		Address: 0x15,
		Rules: []Rule{
			{
				Name:    "identify",
				Matcher: MatchPrimarySecondary(0x07, 0x04),
				Builder: BuildFunc(func(protocol.Frame) (ResponsePlan, error) {
					return ResponsePlan{
						Delay: 8 * time.Millisecond,
						Data:  []byte{0xB5},
					}, nil
				}),
			},
		},
	}

	harness := NewHarness(target)
	responses, err := harness.RunSequence([]QueryStep{
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    0x15,
				Primary:   0x07,
				Secondary: 0x04,
			},
		},
		{
			Advance: 12 * time.Millisecond,
			Frame: protocol.Frame{
				Source:    0x11,
				Target:    0x15,
				Primary:   0x07,
				Secondary: 0x04,
			},
		},
		{
			Advance: 3 * time.Millisecond,
			Frame: protocol.Frame{
				Source:    0x12,
				Target:    0x15,
				Primary:   0x07,
				Secondary: 0x04,
			},
		},
	})
	if err != nil {
		t.Fatalf("RunSequence error = %v", err)
	}
	if len(responses) != 3 {
		t.Fatalf("len(responses) = %d; want 3", len(responses))
	}

	wantRequested := []time.Duration{0, 12 * time.Millisecond, 15 * time.Millisecond}
	wantRespondAt := []time.Duration{8 * time.Millisecond, 20 * time.Millisecond, 23 * time.Millisecond}
	wantSources := []byte{0x10, 0x11, 0x12}
	for idx := range responses {
		response := responses[idx]
		if response.Requested != wantRequested[idx] {
			t.Fatalf("responses[%d].Requested = %s; want %s", idx, response.Requested, wantRequested[idx])
		}
		if response.RespondAt != wantRespondAt[idx] {
			t.Fatalf("responses[%d].RespondAt = %s; want %s", idx, response.RespondAt, wantRespondAt[idx])
		}
		if response.Frame.Source != 0x15 || response.Frame.Target != wantSources[idx] {
			t.Fatalf("responses[%d].Frame source/target = 0x%02x/0x%02x; want 0x15/0x%02x", idx, response.Frame.Source, response.Frame.Target, wantSources[idx])
		}
	}
	if harness.Now() != 15*time.Millisecond {
		t.Fatalf("Now() = %s; want %s", harness.Now(), 15*time.Millisecond)
	}

	history := harness.History()
	if len(history) != len(responses) {
		t.Fatalf("len(history) = %d; want %d", len(history), len(responses))
	}
	for idx := range responses {
		if history[idx].Requested != responses[idx].Requested || history[idx].RespondAt != responses[idx].RespondAt {
			t.Fatalf("history[%d] does not match response[%d]", idx, idx)
		}
	}
}

func TestHarnessRunSequence_Errors(t *testing.T) {
	t.Parallel()

	target := &Target{
		Address: 0x15,
		Rules: []Rule{
			{
				Name:    "identify",
				Matcher: MatchPrimarySecondary(0x07, 0x04),
				Builder: BuildFunc(func(protocol.Frame) (ResponsePlan, error) {
					return ResponsePlan{
						Delay: 8 * time.Millisecond,
						Data:  []byte{0xB5},
					}, nil
				}),
			},
		},
	}

	cases := []struct {
		name    string
		harness *Harness
		steps   []QueryStep
		want    error
	}{
		{
			name:    "missing target",
			harness: NewHarness(nil),
			steps: []QueryStep{
				{
					Frame: protocol.Frame{},
				},
			},
			want: ErrInvalidConfiguration,
		},
		{
			name:    "negative advance",
			harness: NewHarness(target),
			steps: []QueryStep{
				{
					Advance: -time.Millisecond,
					Frame:   protocol.Frame{},
				},
			},
			want: ErrInvalidConfiguration,
		},
		{
			name:    "query mismatch",
			harness: NewHarness(target),
			steps: []QueryStep{
				{
					Frame: protocol.Frame{
						Source:    0x10,
						Target:    0x08,
						Primary:   0x07,
						Secondary: 0x04,
					},
				},
			},
			want: ErrRequestTargetMismatch,
		},
	}

	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := test.harness.RunSequence(test.steps)
			if !errors.Is(err, test.want) {
				t.Fatalf("RunSequence error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestValidateResponseEnvelope(t *testing.T) {
	t.Parallel()

	responses := []EmulatedResponse{
		{
			Requested: 0,
			RespondAt: 8 * time.Millisecond,
		},
		{
			Requested: 10 * time.Millisecond,
			RespondAt: 21 * time.Millisecond,
		},
	}

	if err := ValidateResponseEnvelope(responses, ResponseEnvelope{
		MinDelay: 5 * time.Millisecond,
		MaxDelay: 15 * time.Millisecond,
	}); err != nil {
		t.Fatalf("ValidateResponseEnvelope() error = %v", err)
	}

	err := ValidateResponseEnvelope(responses, ResponseEnvelope{
		MinDelay: 9 * time.Millisecond,
		MaxDelay: 15 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimingConstraint) {
		t.Fatalf("ValidateResponseEnvelope() error = %v; want %v", err, ErrTimingConstraint)
	}

	err = ValidateResponseEnvelope(responses, ResponseEnvelope{
		MinDelay: -time.Millisecond,
	})
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("ValidateResponseEnvelope() error = %v; want %v", err, ErrInvalidConfiguration)
	}
}
