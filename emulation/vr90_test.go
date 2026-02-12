package emulation

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

func TestNewVR90Target_IdentifyResponseUsesProfile(t *testing.T) {
	t.Parallel()

	target, err := NewVR90Target(VR90Profile{
		Address:       0x30,
		Manufacturer:  0xAA,
		DeviceID:      "R90A1",
		Software:      0x1234,
		Hardware:      0x5678,
		ResponseDelay: 9 * time.Millisecond,
		Timing: TimingConstraints{
			MinResponseDelay: 5 * time.Millisecond,
			MaxResponseDelay: 15 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	response, err := target.Emulate(RequestEvent{
		At: 40 * time.Millisecond,
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    0x30,
			Primary:   0x07,
			Secondary: 0x04,
		},
	})
	if err != nil {
		t.Fatalf("Emulate() error = %v", err)
	}

	if response.Rule != "identify" {
		t.Fatalf("Rule = %q; want identify", response.Rule)
	}
	if response.RespondAt != 49*time.Millisecond {
		t.Fatalf("RespondAt = %s; want %s", response.RespondAt, 49*time.Millisecond)
	}
	if response.Frame.Source != 0x30 || response.Frame.Target != 0x10 {
		t.Fatalf("Frame source/target = 0x%02x/0x%02x; want 0x30/0x10", response.Frame.Source, response.Frame.Target)
	}
	wantData := []byte{
		0xAA, 'R', '9', '0', 'A', '1',
		0x12, 0x34,
		0x56, 0x78,
	}
	if !bytes.Equal(response.Frame.Data, wantData) {
		t.Fatalf("Frame data = %x; want %x", response.Frame.Data, wantData)
	}
}

func TestNewVR90Target_NormalizesDefaults(t *testing.T) {
	t.Parallel()

	target, err := NewVR90Target(VR90Profile{})
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}
	if target.Address != DefaultVR90Address {
		t.Fatalf("Address = 0x%02x; want 0x%02x", target.Address, DefaultVR90Address)
	}

	response, err := target.Emulate(RequestEvent{
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    DefaultVR90Address,
			Primary:   0x07,
			Secondary: 0x04,
		},
	})
	if err != nil {
		t.Fatalf("Emulate() error = %v", err)
	}

	wantData := []byte{
		DefaultVR90Manufacturer,
		'B', '7', 'V', '0', '0',
		0x04, 0x22,
		0x55, 0x03,
	}
	if !bytes.Equal(response.Frame.Data, wantData) {
		t.Fatalf("Frame data = %x; want %x", response.Frame.Data, wantData)
	}
}

func TestNewVR90Target_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		profile VR90Profile
		want    error
	}{
		{
			name: "timing violation",
			profile: VR90Profile{
				ResponseDelay: 2 * time.Millisecond,
				Timing: TimingConstraints{
					MinResponseDelay: 5 * time.Millisecond,
					MaxResponseDelay: 10 * time.Millisecond,
				},
			},
			want: ErrTimingConstraint,
		},
		{
			name: "invalid timing range",
			profile: VR90Profile{
				ResponseDelay: 8 * time.Millisecond,
				Timing: TimingConstraints{
					MinResponseDelay: 10 * time.Millisecond,
					MaxResponseDelay: 5 * time.Millisecond,
				},
			},
			want: ErrInvalidConfiguration,
		},
	}

	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewVR90Target(test.profile)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewVR90Target() error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestVR90Target_NoMatchForOtherQueries(t *testing.T) {
	t.Parallel()

	target, err := NewVR90Target(DefaultVR90Profile())
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	_, err = target.Emulate(RequestEvent{
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    DefaultVR90Address,
			Primary:   0x01,
			Secondary: 0x02,
		},
	})
	if !errors.Is(err, ErrNoMatchingRule) {
		t.Fatalf("Emulate() error = %v; want %v", err, ErrNoMatchingRule)
	}
}

func TestSmokeVR90MinimalQuerySet(t *testing.T) {
	target, err := NewVR90Target(DefaultVR90Profile())
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	harness := NewHarness(target)
	responses, err := harness.RunSequence([]QueryStep{
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    DefaultVR90Address,
				Primary:   0x07,
				Secondary: 0x04,
			},
		},
	})
	if err != nil {
		t.Fatalf("RunSequence() error = %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("len(responses) = %d; want 1", len(responses))
	}

	response := responses[0]
	if response.Frame.Source != DefaultVR90Address || response.Frame.Target != 0x10 {
		t.Fatalf("Frame source/target = 0x%02x/0x%02x; want 0x%02x/0x10", response.Frame.Source, response.Frame.Target, DefaultVR90Address)
	}
	if response.Frame.Primary != 0x07 || response.Frame.Secondary != 0x04 {
		t.Fatalf("Frame PB/SB = 0x%02x/0x%02x; want 0x07/0x04", response.Frame.Primary, response.Frame.Secondary)
	}
	if gotID := string(response.Frame.Data[1:6]); gotID != DefaultVR90DeviceID {
		t.Fatalf("DeviceID = %q; want %q", gotID, DefaultVR90DeviceID)
	}

	if err := ValidateResponseEnvelope(responses, ResponseEnvelope{
		MinDelay: 5 * time.Millisecond,
		MaxDelay: 30 * time.Millisecond,
	}); err != nil {
		t.Fatalf("ValidateResponseEnvelope() error = %v", err)
	}
}
