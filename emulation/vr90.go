package emulation

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultVR90Address      = byte(0x15)
	DefaultVR90Manufacturer = byte(0xB5)
	DefaultVR90DeviceID     = "B7V00"
	DefaultVR90Software     = uint16(0x0422)
	DefaultVR90Hardware     = uint16(0x5503)
)

var (
	defaultVR90ResponseDelay = 8 * time.Millisecond
	defaultVR90Timing        = TimingConstraints{
		MinResponseDelay: 5 * time.Millisecond,
		MaxResponseDelay: 30 * time.Millisecond,
	}
)

type VR90Profile struct {
	Address       byte
	Manufacturer  byte
	DeviceID      string
	Software      uint16
	Hardware      uint16
	ResponseDelay time.Duration
	Timing        TimingConstraints
}

func DefaultVR90Profile() VR90Profile {
	return VR90Profile{
		Address:       DefaultVR90Address,
		Manufacturer:  DefaultVR90Manufacturer,
		DeviceID:      DefaultVR90DeviceID,
		Software:      DefaultVR90Software,
		Hardware:      DefaultVR90Hardware,
		ResponseDelay: defaultVR90ResponseDelay,
		Timing:        defaultVR90Timing,
	}
}

func NewVR90Target(profile VR90Profile) (*Target, error) {
	normalized, err := normalizeVR90Profile(profile)
	if err != nil {
		return nil, err
	}
	return NewIdentifyOnlyTarget(IdentifyOnlyProfile{
		Name:          fmt.Sprintf("vr90-minimal-0x%02x", normalized.Address),
		Address:       normalized.Address,
		Manufacturer:  normalized.Manufacturer,
		DeviceID:      normalized.DeviceID,
		Software:      normalized.Software,
		Hardware:      normalized.Hardware,
		ResponseDelay: normalized.ResponseDelay,
		Timing:        normalized.Timing,
	})
}

func normalizeVR90Profile(profile VR90Profile) (VR90Profile, error) {
	hasIdentityOverrides := profile.Manufacturer != 0 ||
		strings.TrimSpace(profile.DeviceID) != "" ||
		profile.Software != 0 ||
		profile.Hardware != 0

	if profile.Address == 0 {
		profile.Address = DefaultVR90Address
	}
	if profile.Manufacturer == 0 {
		profile.Manufacturer = DefaultVR90Manufacturer
	}
	if strings.TrimSpace(profile.DeviceID) == "" {
		profile.DeviceID = DefaultVR90DeviceID
	}
	if !hasIdentityOverrides {
		profile.Software = DefaultVR90Software
		profile.Hardware = DefaultVR90Hardware
	}
	if profile.ResponseDelay <= 0 {
		profile.ResponseDelay = defaultVR90ResponseDelay
	}
	if !profile.Timing.active() {
		profile.Timing = defaultVR90Timing
	}
	if err := profile.Timing.validate(profile.ResponseDelay); err != nil {
		return VR90Profile{}, err
	}

	trimmed := strings.TrimSpace(profile.DeviceID)
	if trimmed == "" {
		return VR90Profile{}, fmt.Errorf("vr90 profile empty device id: %w", ErrInvalidConfiguration)
	}
	if len(trimmed) > identifyOnlyDeviceIDLength {
		trimmed = trimmed[:identifyOnlyDeviceIDLength]
	}
	profile.DeviceID = trimmed
	return profile, nil
}
