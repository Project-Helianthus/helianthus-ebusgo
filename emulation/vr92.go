package emulation

import (
	"fmt"
	"strings"
	"time"
)

// Fixture identity of DefaultVR92Profile and PresetVR92IdentifyOnlyProfile:
// manufacturer byte, five-character device identifier, and software and
// hardware versions sent high byte first. NewVR92Target uses them for
// fields the profile leaves unset, with the same rules as VR90Profile.
const (
	DefaultVR92Manufacturer = byte(0xB5)
	DefaultVR92DeviceID     = "VR_92"
	DefaultVR92Software     = uint16(0x0514)
	DefaultVR92Hardware     = uint16(0x1204)
)

var (
	defaultVR92ResponseDelay = 8 * time.Millisecond
	defaultVR92Timing        = TimingConstraints{
		MinResponseDelay: 5 * time.Millisecond,
		MaxResponseDelay: 30 * time.Millisecond,
	}
)

// VR92MappedCommand is the same type as VR90MappedCommand.
type VR92MappedCommand = VR90MappedCommand

// VR92Profile has the fields and rules of VR90Profile with the DefaultVR92
// identity, except that ScanID has no default: enabling
// EnableB509Discovery with a blank ScanID is invalid.
type VR92Profile struct {
	// Address is the eBUS target address for this device. Required - must be
	// set explicitly. Address is installation-specific; there is no safe default.
	Address             byte
	Manufacturer        byte
	DeviceID            string
	Software            uint16
	Hardware            uint16
	EnableB509Discovery bool
	ScanID              string
	MappedCommands      []VR92MappedCommand
	ResponseDelay       time.Duration
	Timing              TimingConstraints
}

// DefaultVR92Profile returns the fixture identity and timing of the preset
// with no address, no scan identifier, scan-identifier answers disabled and
// no mapped commands. Set Address before passing it to NewVR92Target.
func DefaultVR92Profile() VR92Profile {
	return VR92Profile{
		Manufacturer:  DefaultVR92Manufacturer,
		DeviceID:      DefaultVR92DeviceID,
		Software:      DefaultVR92Software,
		Hardware:      DefaultVR92Hardware,
		ResponseDelay: defaultVR92ResponseDelay,
		Timing:        defaultVR92Timing,
	}
}

// NewVR92Target applies the DefaultVR92 defaults and builds the target
// with NewVR90Target, so the target's name and rules are those of a VR90
// target. It fails like NewVR90Target and also with
// ErrInvalidConfiguration when scan-identifier answers are enabled without
// a ScanID.
func NewVR92Target(profile VR92Profile) (*Target, error) {
	normalized, err := normalizeVR92Profile(profile)
	if err != nil {
		return nil, err
	}

	return NewVR90Target(VR90Profile(normalized))
}

func normalizeVR92Profile(profile VR92Profile) (VR92Profile, error) {
	hasIdentityOverrides := profile.Manufacturer != 0 ||
		strings.TrimSpace(profile.DeviceID) != "" ||
		profile.Software != 0 ||
		profile.Hardware != 0

	if profile.Address == 0 {
		return VR92Profile{}, fmt.Errorf("vr92 profile missing address: %w", ErrInvalidConfiguration)
	}
	if profile.Manufacturer == 0 {
		profile.Manufacturer = DefaultVR92Manufacturer
	}
	if strings.TrimSpace(profile.DeviceID) == "" {
		profile.DeviceID = DefaultVR92DeviceID
	}
	if !hasIdentityOverrides {
		profile.Software = DefaultVR92Software
		profile.Hardware = DefaultVR92Hardware
	}
	if profile.ResponseDelay <= 0 {
		profile.ResponseDelay = defaultVR92ResponseDelay
	}
	if !profile.Timing.active() {
		profile.Timing = defaultVR92Timing
	}
	if err := profile.Timing.validate(profile.ResponseDelay); err != nil {
		return VR92Profile{}, err
	}

	trimmed := strings.TrimSpace(profile.DeviceID)
	if trimmed == "" {
		return VR92Profile{}, fmt.Errorf("vr92 profile empty device id: %w", ErrInvalidConfiguration)
	}
	if len(trimmed) > identifyOnlyDeviceIDLength {
		trimmed = trimmed[:identifyOnlyDeviceIDLength]
	}
	profile.DeviceID = trimmed

	if profile.EnableB509Discovery && strings.TrimSpace(profile.ScanID) == "" {
		return VR92Profile{}, fmt.Errorf("vr92 profile empty scan id with b509 enabled: %w", ErrInvalidConfiguration)
	}

	normalizedCommands, err := normalizeVR90MappedCommands(profile.MappedCommands)
	if err != nil {
		return VR92Profile{}, err
	}
	profile.MappedCommands = normalizedCommands
	return profile, nil
}
