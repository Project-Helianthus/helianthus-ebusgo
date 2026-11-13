package emulation

import (
	"fmt"
	"strings"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

const (
	identifyOnlyDeviceIDLength = 5

	// DefaultVR71Address is the fixture target address of
	// PresetVR71IdentifyOnlyProfile.
	DefaultVR71Address = byte(0x26)
	// DefaultVR71Manufacturer is the fixture manufacturer byte of
	// PresetVR71IdentifyOnlyProfile.
	DefaultVR71Manufacturer = byte(0xB5)
	// DefaultVR71DeviceID is the fixture five-character device identifier of
	// PresetVR71IdentifyOnlyProfile.
	DefaultVR71DeviceID = "VR_71"
	// DefaultVR71Software is the fixture software version of
	// PresetVR71IdentifyOnlyProfile, sent high byte first.
	DefaultVR71Software = uint16(0x0100)
	// DefaultVR71Hardware is the fixture hardware version of
	// PresetVR71IdentifyOnlyProfile, sent high byte first.
	DefaultVR71Hardware = uint16(0x5904)
)

var (
	defaultIdentifyOnlyResponseDelay = 8 * time.Millisecond
	defaultIdentifyOnlyTiming        = TimingConstraints{
		MinResponseDelay: 5 * time.Millisecond,
		MaxResponseDelay: 30 * time.Millisecond,
	}
)

// IdentifyOnlyProfile describes a target that answers only the
// identification request (primary 0x07, secondary 0x04). Address and
// Manufacturer must not be zero and DeviceID must not be blank. Name
// defaults to one derived from the address. DeviceID is trimmed and cut to
// its first five bytes, then space-padded to five bytes in the answer.
// Software and Hardware each fill two version bytes of the answer, high
// byte first; their encoding is not checked. ResponseDelay defaults to 8 ms
// when not positive and Timing to 5 ms to 30 ms when unset. The answer
// layout is documented at
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/architecture/ebus_standard/03-identification.md
type IdentifyOnlyProfile struct {
	Name          string
	Address       byte
	Manufacturer  byte
	DeviceID      string
	Software      uint16
	Hardware      uint16
	ResponseDelay time.Duration
	Timing        TimingConstraints
}

// PresetVR90IdentifyOnlyProfile returns an identify-only profile with the
// DefaultVR90 fixture identity at address 0x15, an 8 ms response delay and
// 5 ms to 30 ms timing. Each call returns a new value.
func PresetVR90IdentifyOnlyProfile() IdentifyOnlyProfile {
	return IdentifyOnlyProfile{
		Name:          fmt.Sprintf("vr90-minimal-0x%02x", 0x15),
		Address:       0x15,
		Manufacturer:  DefaultVR90Manufacturer,
		DeviceID:      DefaultVR90DeviceID,
		Software:      DefaultVR90Software,
		Hardware:      DefaultVR90Hardware,
		ResponseDelay: defaultVR90ResponseDelay,
		Timing:        defaultVR90Timing,
	}
}

// PresetVR92IdentifyOnlyProfile returns an identify-only profile with the
// DefaultVR92 fixture identity at address 0x30, an 8 ms response delay and
// 5 ms to 30 ms timing. Each call returns a new value.
func PresetVR92IdentifyOnlyProfile() IdentifyOnlyProfile {
	return IdentifyOnlyProfile{
		Name:          fmt.Sprintf("vr92-minimal-0x%02x", 0x30),
		Address:       0x30,
		Manufacturer:  DefaultVR92Manufacturer,
		DeviceID:      DefaultVR92DeviceID,
		Software:      DefaultVR92Software,
		Hardware:      DefaultVR92Hardware,
		ResponseDelay: defaultVR92ResponseDelay,
		Timing:        defaultVR92Timing,
	}
}

// PresetVR71IdentifyOnlyProfile returns an identify-only profile with the
// DefaultVR71 fixture identity at DefaultVR71Address, an 8 ms response
// delay and 5 ms to 30 ms timing. Each call returns a new value.
func PresetVR71IdentifyOnlyProfile() IdentifyOnlyProfile {
	return IdentifyOnlyProfile{
		Name:          fmt.Sprintf("vr71-minimal-0x%02x", DefaultVR71Address),
		Address:       DefaultVR71Address,
		Manufacturer:  DefaultVR71Manufacturer,
		DeviceID:      DefaultVR71DeviceID,
		Software:      DefaultVR71Software,
		Hardware:      DefaultVR71Hardware,
		ResponseDelay: defaultIdentifyOnlyResponseDelay,
		Timing:        defaultIdentifyOnlyTiming,
	}
}

// NewIdentifyOnlyTarget normalizes profile as IdentifyOnlyProfile
// describes and returns a target with one rule, "identify", that answers
// every identification request, whatever its data, with the 10-byte
// identification answer after the profile's response delay. It fails with
// ErrInvalidConfiguration for a missing address, manufacturer or device
// identifier or invalid timing bounds, and with ErrTimingConstraint when
// the response delay is outside the timing.
func NewIdentifyOnlyTarget(profile IdentifyOnlyProfile) (*Target, error) {
	normalized, err := normalizeIdentifyOnlyProfile(profile)
	if err != nil {
		return nil, err
	}

	return &Target{
		Name:          normalized.Name,
		Address:       normalized.Address,
		DefaultTiming: normalized.Timing,
		Rules: []Rule{
			{
				Name:    "identify",
				Matcher: MatchPrimarySecondary(0x07, 0x04),
				Builder: BuildFunc(func(_ protocol.Frame) (ResponsePlan, error) {
					return ResponsePlan{
						Delay: normalized.ResponseDelay,
						Data:  normalized.identificationPayload(),
					}, nil
				}),
			},
		},
	}, nil
}

func normalizeIdentifyOnlyProfile(profile IdentifyOnlyProfile) (IdentifyOnlyProfile, error) {
	profile.Name = strings.TrimSpace(profile.Name)
	if profile.Name == "" {
		profile.Name = fmt.Sprintf("identify-only-0x%02x", profile.Address)
	}
	if profile.Address == 0 {
		return IdentifyOnlyProfile{}, fmt.Errorf("identify-only profile empty address: %w", ErrInvalidConfiguration)
	}
	if profile.Manufacturer == 0 {
		return IdentifyOnlyProfile{}, fmt.Errorf("identify-only profile empty manufacturer: %w", ErrInvalidConfiguration)
	}

	trimmed := strings.TrimSpace(profile.DeviceID)
	if trimmed == "" {
		return IdentifyOnlyProfile{}, fmt.Errorf("identify-only profile empty device id: %w", ErrInvalidConfiguration)
	}
	if len(trimmed) > identifyOnlyDeviceIDLength {
		trimmed = trimmed[:identifyOnlyDeviceIDLength]
	}
	profile.DeviceID = trimmed

	if profile.ResponseDelay <= 0 {
		profile.ResponseDelay = defaultIdentifyOnlyResponseDelay
	}
	if !profile.Timing.active() {
		profile.Timing = defaultIdentifyOnlyTiming
	}
	if err := profile.Timing.validate(profile.ResponseDelay); err != nil {
		return IdentifyOnlyProfile{}, err
	}

	return profile, nil
}

func (profile IdentifyOnlyProfile) identificationPayload() []byte {
	deviceID := fmt.Sprintf("%-*s", identifyOnlyDeviceIDLength, profile.DeviceID)
	return []byte{
		profile.Manufacturer,
		deviceID[0],
		deviceID[1],
		deviceID[2],
		deviceID[3],
		deviceID[4],
		byte(profile.Software >> 8),
		byte(profile.Software & 0xFF),
		byte(profile.Hardware >> 8),
		byte(profile.Hardware & 0xFF),
	}
}
