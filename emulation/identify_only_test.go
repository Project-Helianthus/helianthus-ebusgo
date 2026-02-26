package emulation

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

func TestNewIdentifyOnlyTarget_ConstructorAndPayload(t *testing.T) {
	t.Parallel()

	target, err := NewIdentifyOnlyTarget(IdentifyOnlyProfile{
		Name:          "  custom-identify  ",
		Address:       0x30,
		Manufacturer:  0xAA,
		DeviceID:      "R71LONG",
		Software:      0x1234,
		Hardware:      0x5678,
		ResponseDelay: 9 * time.Millisecond,
		Timing: TimingConstraints{
			MinResponseDelay: 5 * time.Millisecond,
			MaxResponseDelay: 15 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("NewIdentifyOnlyTarget() error = %v", err)
	}

	if target.Name != "custom-identify" {
		t.Fatalf("Name = %q; want %q", target.Name, "custom-identify")
	}
	if target.Address != 0x30 {
		t.Fatalf("Address = 0x%02x; want 0x30", target.Address)
	}

	response, err := target.Emulate(RequestEvent{
		At: 20 * time.Millisecond,
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
	if response.RespondAt != 29*time.Millisecond {
		t.Fatalf("RespondAt = %s; want %s", response.RespondAt, 29*time.Millisecond)
	}

	wantData := []byte{
		0xAA, 'R', '7', '1', 'L', 'O',
		0x12, 0x34,
		0x56, 0x78,
	}
	if !bytes.Equal(response.Frame.Data, wantData) {
		t.Fatalf("Frame data = %x; want %x", response.Frame.Data, wantData)
	}
}

func TestNewIdentifyOnlyTarget_Errors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		profile IdentifyOnlyProfile
		want    error
	}{
		{
			name: "empty address",
			profile: IdentifyOnlyProfile{
				Manufacturer: 0xB5,
				DeviceID:     "VR_71",
			},
			want: ErrInvalidConfiguration,
		},
		{
			name: "empty manufacturer",
			profile: IdentifyOnlyProfile{
				Address:  0x26,
				DeviceID: "VR_71",
			},
			want: ErrInvalidConfiguration,
		},
		{
			name: "empty device id",
			profile: IdentifyOnlyProfile{
				Address:      0x26,
				Manufacturer: 0xB5,
				DeviceID:     "   ",
			},
			want: ErrInvalidConfiguration,
		},
		{
			name: "timing violation",
			profile: IdentifyOnlyProfile{
				Address:       0x26,
				Manufacturer:  0xB5,
				DeviceID:      "VR_71",
				ResponseDelay: 2 * time.Millisecond,
				Timing: TimingConstraints{
					MinResponseDelay: 5 * time.Millisecond,
					MaxResponseDelay: 15 * time.Millisecond,
				},
			},
			want: ErrTimingConstraint,
		},
	}

	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewIdentifyOnlyTarget(test.profile)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewIdentifyOnlyTarget() error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestIdentifyOnlyProfile_Presets(t *testing.T) {
	t.Parallel()

	vr90 := PresetVR90IdentifyOnlyProfile()
	if vr90.Address != 0x15 ||
		vr90.Manufacturer != DefaultVR90Manufacturer ||
		vr90.DeviceID != DefaultVR90DeviceID ||
		vr90.Software != DefaultVR90Software ||
		vr90.Hardware != DefaultVR90Hardware {
		t.Fatalf("VR90 preset mismatch: %+v", vr90)
	}

	vr92 := PresetVR92IdentifyOnlyProfile()
	if vr92.Address != 0x30 ||
		vr92.Manufacturer != DefaultVR92Manufacturer ||
		vr92.DeviceID != DefaultVR92DeviceID ||
		vr92.Software != DefaultVR92Software ||
		vr92.Hardware != DefaultVR92Hardware {
		t.Fatalf("VR92 preset mismatch: %+v", vr92)
	}

	vr71 := PresetVR71IdentifyOnlyProfile()
	if vr71.Address != DefaultVR71Address ||
		vr71.Manufacturer != DefaultVR71Manufacturer ||
		vr71.DeviceID != DefaultVR71DeviceID ||
		vr71.Software != DefaultVR71Software ||
		vr71.Hardware != DefaultVR71Hardware {
		t.Fatalf("VR_71 preset mismatch: %+v", vr71)
	}
}

func TestIdentifyOnlyTarget_RepeatedQueryBehavior(t *testing.T) {
	t.Parallel()

	profile := PresetVR71IdentifyOnlyProfile()
	target, err := NewIdentifyOnlyTarget(profile)
	if err != nil {
		t.Fatalf("NewIdentifyOnlyTarget() error = %v", err)
	}

	harness := NewHarness(target)
	frame := protocol.Frame{
		Source:    0x10,
		Target:    profile.Address,
		Primary:   0x07,
		Secondary: 0x04,
	}

	first, err := harness.Query(frame)
	if err != nil {
		t.Fatalf("Query(first) error = %v", err)
	}
	first.Frame.Data[0] = 0x00

	harness.Advance(3 * time.Millisecond)
	second, err := harness.Query(frame)
	if err != nil {
		t.Fatalf("Query(second) error = %v", err)
	}

	if second.Requested != 3*time.Millisecond {
		t.Fatalf("second.Requested = %s; want %s", second.Requested, 3*time.Millisecond)
	}
	if second.RespondAt != 11*time.Millisecond {
		t.Fatalf("second.RespondAt = %s; want %s", second.RespondAt, 11*time.Millisecond)
	}

	wantData := profile.identificationPayload()
	if !bytes.Equal(second.Frame.Data, wantData) {
		t.Fatalf("second.Frame.Data = %x; want %x", second.Frame.Data, wantData)
	}
}

func TestSmokeIdentifyOnlyProfileValidation(t *testing.T) {
	cases := []struct {
		name    string
		profile IdentifyOnlyProfile
	}{
		{
			name:    "vr90",
			profile: PresetVR90IdentifyOnlyProfile(),
		},
		{
			name:    "vr92",
			profile: PresetVR92IdentifyOnlyProfile(),
		},
		{
			name:    "vr_71",
			profile: PresetVR71IdentifyOnlyProfile(),
		},
	}

	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			target, err := NewIdentifyOnlyTarget(test.profile)
			if err != nil {
				t.Fatalf("NewIdentifyOnlyTarget() error = %v", err)
			}

			harness := NewHarness(target)
			responses, err := harness.RunSequence([]QueryStep{
				{
					Frame: protocol.Frame{
						Source:    0x10,
						Target:    test.profile.Address,
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
			if !bytes.Equal(responses[0].Frame.Data, test.profile.identificationPayload()) {
				t.Fatalf("Frame data = %x; want %x", responses[0].Frame.Data, test.profile.identificationPayload())
			}

			if err := ValidateResponseEnvelope(responses, ResponseEnvelope{
				MinDelay: test.profile.Timing.MinResponseDelay,
				MaxDelay: test.profile.Timing.MaxResponseDelay,
			}); err != nil {
				t.Fatalf("ValidateResponseEnvelope() error = %v", err)
			}
		})
	}
}
