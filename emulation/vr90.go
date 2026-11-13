package emulation

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

const (
	// DefaultVR90Manufacturer is the fixture manufacturer byte that
	// DefaultVR90Profile and NewVR90Target use.
	DefaultVR90Manufacturer = byte(0xB5)
	// DefaultVR90DeviceID is the fixture five-character device identifier
	// that DefaultVR90Profile and NewVR90Target use.
	DefaultVR90DeviceID = "B7V00"
	// DefaultVR90Software is the fixture software version, sent high byte
	// first, used when a VR90Profile sets no identity field.
	DefaultVR90Software = uint16(0x0422)
	// DefaultVR90Hardware is the fixture hardware version, sent high byte
	// first, used when a VR90Profile sets no identity field.
	DefaultVR90Hardware = uint16(0x5503)
	// DefaultVR90ScanID is the fixture scan identifier that NewVR90Target
	// uses when VR90Profile.ScanID is blank.
	DefaultVR90ScanID = "21231600202609140953035469N6"

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

// VR90Profile describes a preset target that answers the identification
// request like an IdentifyOnlyProfile and, optionally, scan-identifier
// reads and caller-mapped commands. Address is required. A zero
// Manufacturer or blank DeviceID takes the DefaultVR90 value; Software and
// Hardware take theirs only when none of Manufacturer, DeviceID, Software
// and Hardware is set, so a profile that overrides any identity field keeps
// zero versions as zero. ResponseDelay and Timing default as for
// IdentifyOnlyProfile, and the response delay applies to every rule.
//
// With EnableB509Discovery the target also answers primary 0xB5, secondary
// 0x09 requests with one data byte from 0x24 to 0x27. It trims ScanID
// (DefaultVR90ScanID when blank), cuts or space-pads it to 32 bytes and
// answers selector 0x24+n with a 0x00 byte followed by bytes 8n to 8n+7.
// That window layout belongs to this fixture and is not evidence of
// the wire layout, which is documented with its open questions at
// https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/protocols/vaillant/ebus-vaillant-b509.md
type VR90Profile struct {
	// Address is the eBUS target address for this device. Required - must be
	// set explicitly. Address is installation-specific; there is no safe default.
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

// VR90MappedCommand adds a fixed answer to a preset target. It matches
// requests with Primary and Secondary and, when set, an exact payload or a
// payload prefix; setting both is invalid. ResponseData must not be empty
// and is the answer's data, unchanged. A blank Name defaults to one derived
// from the command bytes and the command's index. Mapped commands are
// tried after the identification and scan-identifier rules, so they cannot
// override them. The constructors copy every slice.
type VR90MappedCommand struct {
	Name      string
	Primary   byte
	Secondary byte
	// PayloadExact is unset when nil. A non-nil empty slice matches only an
	// empty payload.
	PayloadExact []byte
	// PayloadPrefix is unset when nil. A non-nil empty slice matches every
	// payload and is therefore equivalent to matching only Primary/Secondary.
	PayloadPrefix []byte
	ResponseData  []byte
}

// DefaultVR90Profile returns the fixture identity and timing of the preset
// with no address, scan-identifier answers disabled and no mapped commands.
// Set Address before passing it to NewVR90Target.
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

// NewVR90Target normalizes profile as VR90Profile describes and returns a
// target named after its address with the rules "identify", then the
// scan-identifier rule when enabled, then one rule per mapped command in
// order. It fails with ErrInvalidConfiguration for a zero address, invalid
// timing bounds or an invalid mapped command, and with ErrTimingConstraint
// when the response delay is outside the timing.
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
		if command.PayloadExact != nil && command.PayloadPrefix != nil {
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
		command.PayloadExact = cloneOptionalBytes(command.PayloadExact)
		command.PayloadPrefix = cloneOptionalBytes(command.PayloadPrefix)
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
	if command.PayloadExact != nil {
		payload := cloneOptionalBytes(command.PayloadExact)
		return func(frame protocol.Frame) bool {
			return frame.Primary == command.Primary &&
				frame.Secondary == command.Secondary &&
				bytes.Equal(frame.Data, payload)
		}
	}
	if command.PayloadPrefix != nil {
		return MatchPrimarySecondaryWithPrefix(command.Primary, command.Secondary, command.PayloadPrefix)
	}
	return MatchPrimarySecondary(command.Primary, command.Secondary)
}

func cloneOptionalBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
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
