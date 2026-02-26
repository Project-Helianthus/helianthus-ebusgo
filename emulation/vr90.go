package emulation

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

const (
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
	MappedCommands      []VR90MappedCommand
	ResponseDelay       time.Duration
	Timing              TimingConstraints
}

type VR90MappedCommand struct {
	Name          string
	Primary       byte
	Secondary     byte
	PayloadExact  []byte
	PayloadPrefix []byte
	ResponseData  []byte
}

func DefaultVR90Profile() VR90Profile {
	return VR90Profile{
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
	if normalized.EnableB509Discovery {
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
	}
	for idx := range normalized.MappedCommands {
		target.Rules = append(target.Rules, vr90MappedRule(normalized.MappedCommands[idx], normalized.ResponseDelay))
	}
	return target, nil
}

func normalizeVR90Profile(profile VR90Profile) (VR90Profile, error) {
	hasIdentityOverrides := profile.Manufacturer != 0 ||
		strings.TrimSpace(profile.DeviceID) != "" ||
		profile.Software != 0 ||
		profile.Hardware != 0

	if profile.Address == 0 {
		return VR90Profile{}, fmt.Errorf("vr90 profile missing address: %w", ErrInvalidConfiguration)
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

	normalizedCommands, err := normalizeVR90MappedCommands(profile.MappedCommands)
	if err != nil {
		return VR90Profile{}, err
	}
	profile.MappedCommands = normalizedCommands
	return profile, nil
}

func normalizeVR90MappedCommands(commands []VR90MappedCommand) ([]VR90MappedCommand, error) {
	if len(commands) == 0 {
		return nil, nil
	}

	normalized := make([]VR90MappedCommand, 0, len(commands))
	for idx := range commands {
		command := commands[idx]
		command.Name = strings.TrimSpace(command.Name)
		if command.Name == "" {
			command.Name = fmt.Sprintf("mapped-pb-0x%02x-sb-0x%02x-%d", command.Primary, command.Secondary, idx)
		}
		if len(command.PayloadExact) > 0 && len(command.PayloadPrefix) > 0 {
			return nil, fmt.Errorf(
				"vr90 mapped command[%d] has both exact and prefix payload matchers: %w",
				idx,
				ErrInvalidConfiguration,
			)
		}
		if len(command.ResponseData) == 0 {
			return nil, fmt.Errorf(
				"vr90 mapped command[%d] empty response payload: %w",
				idx,
				ErrInvalidConfiguration,
			)
		}
		command.PayloadExact = append([]byte(nil), command.PayloadExact...)
		command.PayloadPrefix = append([]byte(nil), command.PayloadPrefix...)
		command.ResponseData = append([]byte(nil), command.ResponseData...)
		normalized = append(normalized, command)
	}

	return normalized, nil
}

func vr90MappedRule(command VR90MappedCommand, delay time.Duration) Rule {
	responsePayload := append([]byte(nil), command.ResponseData...)
	return Rule{
		Name:    command.Name,
		Matcher: vr90MappedCommandMatcher(command),
		Builder: BuildFunc(func(protocol.Frame) (ResponsePlan, error) {
			return ResponsePlan{
				Delay: delay,
				Data:  append([]byte(nil), responsePayload...),
			}, nil
		}),
	}
}

func vr90MappedCommandMatcher(command VR90MappedCommand) MatchFunc {
	if len(command.PayloadExact) > 0 {
		payload := append([]byte(nil), command.PayloadExact...)
		return func(frame protocol.Frame) bool {
			return frame.Primary == command.Primary &&
				frame.Secondary == command.Secondary &&
				bytes.Equal(frame.Data, payload)
		}
	}
	if len(command.PayloadPrefix) > 0 {
		return MatchPrimarySecondaryWithPrefix(command.Primary, command.Secondary, command.PayloadPrefix)
	}
	return MatchPrimarySecondary(command.Primary, command.Secondary)
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
