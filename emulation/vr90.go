package emulation

import (
	"fmt"
	"strings"
	"time"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

const (
	DefaultVR90Address      = byte(0x15)
	DefaultVR90Manufacturer = byte(0xB5)
	DefaultVR90DeviceID     = "B7V00"
	DefaultVR90Software     = uint16(0x0422)
	DefaultVR90Hardware     = uint16(0x5503)
	DefaultVR90ScanID       = "21231600202609140953035469N6"

	vr90B509ScanIDSelectorStart = byte(0x24)
	vr90B509ScanIDSelectorEnd   = byte(0x27)
	vr90B509ScanIDChunkSize     = 8
	vr90B509ScanIDChunkCount    = 4
	vr90B509ScanIDLength        = vr90B509ScanIDChunkSize * vr90B509ScanIDChunkCount
)

var (
	defaultVR90ResponseDelay = 8 * time.Millisecond
	defaultVR90Timing        = TimingConstraints{
		MinResponseDelay: 5 * time.Millisecond,
		MaxResponseDelay: 30 * time.Millisecond,
	}
)

type VR90Profile struct {
	Address             byte
	Manufacturer        byte
	DeviceID            string
	Software            uint16
	Hardware            uint16
	EnableB509Discovery bool
	ScanID              string
	ResponseDelay       time.Duration
	Timing              TimingConstraints
}

func DefaultVR90Profile() VR90Profile {
	return VR90Profile{
		Address:       DefaultVR90Address,
		Manufacturer:  DefaultVR90Manufacturer,
		DeviceID:      DefaultVR90DeviceID,
		Software:      DefaultVR90Software,
		Hardware:      DefaultVR90Hardware,
		ScanID:        DefaultVR90ScanID,
		ResponseDelay: defaultVR90ResponseDelay,
		Timing:        defaultVR90Timing,
	}
}

func NewVR90Target(profile VR90Profile) (*Target, error) {
	normalized, err := normalizeVR90Profile(profile)
	if err != nil {
		return nil, err
	}
	target, err := NewIdentifyOnlyTarget(IdentifyOnlyProfile{
		Name:          fmt.Sprintf("vr90-minimal-0x%02x", normalized.Address),
		Address:       normalized.Address,
		Manufacturer:  normalized.Manufacturer,
		DeviceID:      normalized.DeviceID,
		Software:      normalized.Software,
		Hardware:      normalized.Hardware,
		ResponseDelay: normalized.ResponseDelay,
		Timing:        normalized.Timing,
	})
	if err != nil {
		return nil, err
	}
	if !normalized.EnableB509Discovery {
		return target, nil
	}

	scanID := normalized.ScanID
	target.Rules = append(target.Rules, Rule{
		Name: "vaillant-b509-scanid",
		Matcher: MatchFunc(func(frame protocol.Frame) bool {
			return frame.Primary == 0xB5 &&
				frame.Secondary == 0x09 &&
				len(frame.Data) == 1 &&
				isVR90B509ScanIDSelector(frame.Data[0])
		}),
		Builder: BuildFunc(func(frame protocol.Frame) (ResponsePlan, error) {
			chunk, ok := vr90B509ScanIDChunk(scanID, frame.Data[0])
			if !ok {
				return ResponsePlan{}, fmt.Errorf("vr90 unsupported b509 selector 0x%02x: %w", frame.Data[0], ErrNoMatchingRule)
			}
			return ResponsePlan{
				Delay: normalized.ResponseDelay,
				Data:  chunk,
			}, nil
		}),
	})
	return target, nil
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
	profile.ScanID = normalizeVR90ScanID(profile.ScanID)
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

func normalizeVR90ScanID(scanID string) string {
	trimmed := strings.TrimSpace(scanID)
	if trimmed == "" {
		trimmed = DefaultVR90ScanID
	}
	if len(trimmed) > vr90B509ScanIDLength {
		trimmed = trimmed[:vr90B509ScanIDLength]
	}
	if len(trimmed) == vr90B509ScanIDLength {
		return trimmed
	}
	padded := make([]byte, vr90B509ScanIDLength)
	copy(padded, trimmed)
	for idx := len(trimmed); idx < len(padded); idx++ {
		padded[idx] = ' '
	}
	return string(padded)
}

func isVR90B509ScanIDSelector(selector byte) bool {
	return selector >= vr90B509ScanIDSelectorStart && selector <= vr90B509ScanIDSelectorEnd
}

func vr90B509ScanIDChunk(scanID string, selector byte) ([]byte, bool) {
	if !isVR90B509ScanIDSelector(selector) {
		return nil, false
	}
	normalized := normalizeVR90ScanID(scanID)
	offset := int(selector-vr90B509ScanIDSelectorStart) * vr90B509ScanIDChunkSize
	chunk := make([]byte, 1, 1+vr90B509ScanIDChunkSize)
	chunk[0] = 0x00
	chunk = append(chunk, normalized[offset:offset+vr90B509ScanIDChunkSize]...)
	return chunk, true
}
