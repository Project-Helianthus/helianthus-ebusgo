package emulation

import (
	"fmt"
	"strings"
	"time"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

const (
	identifyOnlyDeviceIDLength = 5

	DefaultVR71Address      = byte(0x26)
	DefaultVR71Manufacturer = byte(0xB5)
	DefaultVR71DeviceID     = "VR_71"
	DefaultVR71Software     = uint16(0x0100)
	DefaultVR71Hardware     = uint16(0x5904)
)

var (
	defaultIdentifyOnlyResponseDelay = 8 * time.Millisecond
	defaultIdentifyOnlyTiming        = TimingConstraints{
		MinResponseDelay: 5 * time.Millisecond,
		MaxResponseDelay: 30 * time.Millisecond,
	}
)

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

func PresetVR90IdentifyOnlyProfile() IdentifyOnlyProfile {
	return IdentifyOnlyProfile{
		Name:          fmt.Sprintf("vr90-minimal-0x%02x", DefaultVR90Address),
		Address:       DefaultVR90Address,
		Manufacturer:  DefaultVR90Manufacturer,
		DeviceID:      DefaultVR90DeviceID,
		Software:      DefaultVR90Software,
		Hardware:      DefaultVR90Hardware,
		ResponseDelay: defaultVR90ResponseDelay,
		Timing:        defaultVR90Timing,
	}
}

func PresetVR92IdentifyOnlyProfile() IdentifyOnlyProfile {
	return IdentifyOnlyProfile{
		Name:          fmt.Sprintf("vr92-minimal-0x%02x", DefaultVR92Address),
		Address:       DefaultVR92Address,
		Manufacturer:  DefaultVR92Manufacturer,
		DeviceID:      DefaultVR92DeviceID,
		Software:      DefaultVR92Software,
		Hardware:      DefaultVR92Hardware,
		ResponseDelay: defaultVR92ResponseDelay,
		Timing:        defaultVR92Timing,
	}
}

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
