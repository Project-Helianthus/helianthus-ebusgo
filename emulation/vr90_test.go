package emulation

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

const testVR90Address = byte(0x75)

func defaultVR90TestProfile() VR90Profile {
	profile := DefaultVR90Profile()
	profile.Address = testVR90Address
	return profile
}

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

	_, err := NewVR90Target(VR90Profile{})
	if err == nil {
		t.Fatalf("NewVR90Target() error = nil; want error")
	}
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("NewVR90Target() error = %v; want %v", err, ErrInvalidConfiguration)
	}
	if got, want := err.Error(), "vr90 profile missing address"; !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("NewVR90Target() error = %q; want substring %q", got, want)
	}
}

func TestNewVR90Target_ErrorOnZeroAddress(t *testing.T) {
	t.Parallel()

	_, err := NewVR90Target(VR90Profile{})
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("NewVR90Target() error = %v; want %v", err, ErrInvalidConfiguration)
	}
}

func TestNewVR90Target_PreservesZeroSoftwareHardware(t *testing.T) {
	t.Parallel()

	profile := defaultVR90TestProfile()
	profile.Software = 0x0000
	profile.Hardware = 0x0000

	target, err := NewVR90Target(profile)
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	response, err := target.Emulate(RequestEvent{
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    testVR90Address,
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
		0x00, 0x00,
		0x00, 0x00,
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
				Address:       testVR90Address,
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
				Address:       testVR90Address,
				ResponseDelay: 8 * time.Millisecond,
				Timing: TimingConstraints{
					MinResponseDelay: 10 * time.Millisecond,
					MaxResponseDelay: 5 * time.Millisecond,
				},
			},
			want: ErrInvalidConfiguration,
		},
		{
			name: "mapped command has exact and prefix matchers",
			profile: VR90Profile{
				Address: testVR90Address,
				MappedCommands: []VR90MappedCommand{
					{
						Primary:       0xB5,
						Secondary:     0x06,
						PayloadExact:  []byte{0x01},
						PayloadPrefix: []byte{0x01},
						ResponseData:  []byte{0x00},
					},
				},
			},
			want: ErrInvalidConfiguration,
		},
		{
			name: "mapped command empty response",
			profile: VR90Profile{
				Address: testVR90Address,
				MappedCommands: []VR90MappedCommand{
					{
						Primary:   0xB5,
						Secondary: 0x06,
					},
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

	target, err := NewVR90Target(defaultVR90TestProfile())
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	_, err = target.Emulate(RequestEvent{
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    testVR90Address,
			Primary:   0x01,
			Secondary: 0x02,
		},
	})
	if !errors.Is(err, ErrNoMatchingRule) {
		t.Fatalf("Emulate() error = %v; want %v", err, ErrNoMatchingRule)
	}
}

func TestVR90B509ScanIDChunkEncoding(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		scanID       string
		wantSegments [4]string
	}{
		{
			name:   "default padding",
			scanID: "",
			wantSegments: [4]string{
				"21231600",
				"20260914",
				"09530354",
				"69N6    ",
			},
		},
		{
			name:   "short padded",
			scanID: "ABCD",
			wantSegments: [4]string{
				"ABCD    ",
				"        ",
				"        ",
				"        ",
			},
		},
		{
			name:   "long truncated",
			scanID: "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ",
			wantSegments: [4]string{
				"01234567",
				"89ABCDEF",
				"GHIJKLMN",
				"OPQRSTUV",
			},
		},
	}

	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			for idx := 0; idx < vr90B509ScanIDChunkCount; idx++ {
				selector := vr90B509ScanIDSelectorStart + byte(idx)
				chunk, ok := vr90B509ScanIDChunk(test.scanID, selector)
				if !ok {
					t.Fatalf("vr90B509ScanIDChunk() ok = false for selector 0x%02x", selector)
				}
				want := append([]byte{0x00}, []byte(test.wantSegments[idx])...)
				if !bytes.Equal(chunk, want) {
					t.Fatalf("selector 0x%02x chunk = %x; want %x", selector, chunk, want)
				}
			}
		})
	}
}

func TestVR90Target_B509SelectorBehavior(t *testing.T) {
	t.Parallel()

	profile := defaultVR90TestProfile()
	profile.EnableB509Discovery = true
	profile.ScanID = "21231600202609140953035469N6"

	target, err := NewVR90Target(profile)
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	cases := []struct {
		name     string
		data     []byte
		wantData []byte
		wantErr  error
	}{
		{
			name:     "selector 0x24",
			data:     []byte{0x24},
			wantData: []byte{0x00, '2', '1', '2', '3', '1', '6', '0', '0'},
		},
		{
			name:     "selector 0x25",
			data:     []byte{0x25},
			wantData: []byte{0x00, '2', '0', '2', '6', '0', '9', '1', '4'},
		},
		{
			name:     "selector 0x26",
			data:     []byte{0x26},
			wantData: []byte{0x00, '0', '9', '5', '3', '0', '3', '5', '4'},
		},
		{
			name:     "selector 0x27",
			data:     []byte{0x27},
			wantData: []byte{0x00, '6', '9', 'N', '6', ' ', ' ', ' ', ' '},
		},
		{
			name:    "unknown selector unmatched",
			data:    []byte{0x28},
			wantErr: ErrNoMatchingRule,
		},
		{
			name:    "known selector with extra data unmatched",
			data:    []byte{0x24, 0x00},
			wantErr: ErrNoMatchingRule,
		},
	}

	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			response, err := target.Emulate(RequestEvent{
				Frame: protocol.Frame{
					Source:    0x10,
					Target:    testVR90Address,
					Primary:   0xB5,
					Secondary: 0x09,
					Data:      append([]byte(nil), test.data...),
				},
			})

			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Emulate() error = %v; want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Emulate() error = %v", err)
			}
			if response.Frame.Primary != 0xB5 || response.Frame.Secondary != 0x09 {
				t.Fatalf("Frame PB/SB = 0x%02x/0x%02x; want 0xB5/0x09", response.Frame.Primary, response.Frame.Secondary)
			}
			if !bytes.Equal(response.Frame.Data, test.wantData) {
				t.Fatalf("Frame data = %x; want %x", response.Frame.Data, test.wantData)
			}
		})
	}
}

func TestVR90Target_B509DiscoveryDisabledByDefault(t *testing.T) {
	t.Parallel()

	target, err := NewVR90Target(defaultVR90TestProfile())
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	_, err = target.Emulate(RequestEvent{
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    testVR90Address,
			Primary:   0xB5,
			Secondary: 0x09,
			Data:      []byte{0x24},
		},
	})
	if !errors.Is(err, ErrNoMatchingRule) {
		t.Fatalf("Emulate() error = %v; want %v", err, ErrNoMatchingRule)
	}
}

func TestVR90Target_B509DiscoveryPreservesIdentify(t *testing.T) {
	t.Parallel()

	profile := defaultVR90TestProfile()
	profile.EnableB509Discovery = true
	target, err := NewVR90Target(profile)
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	response, err := target.Emulate(RequestEvent{
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    testVR90Address,
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

func TestVR90Target_MappedCommandResponse(t *testing.T) {
	t.Parallel()

	profile := defaultVR90TestProfile()
	profile.MappedCommands = []VR90MappedCommand{
		{
			Name:          "mapped-room-temp",
			Primary:       0xB5,
			Secondary:     0x06,
			PayloadPrefix: []byte{0x01, 0x00},
			ResponseData:  []byte{0x00, 0x2A, 0x10},
		},
	}
	target, err := NewVR90Target(profile)
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	response, err := target.Emulate(RequestEvent{
		At: 30 * time.Millisecond,
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    testVR90Address,
			Primary:   0xB5,
			Secondary: 0x06,
			Data:      []byte{0x01, 0x00, 0x7F},
		},
	})
	if err != nil {
		t.Fatalf("Emulate() error = %v", err)
	}
	if response.Rule != "mapped-room-temp" {
		t.Fatalf("Rule = %q; want mapped-room-temp", response.Rule)
	}
	if response.RespondAt != 38*time.Millisecond {
		t.Fatalf("RespondAt = %s; want %s", response.RespondAt, 38*time.Millisecond)
	}
	wantData := []byte{0x00, 0x2A, 0x10}
	if !bytes.Equal(response.Frame.Data, wantData) {
		t.Fatalf("Frame data = %x; want %x", response.Frame.Data, wantData)
	}

	response.Frame.Data[0] = 0xFF
	next, err := target.Emulate(RequestEvent{
		At: 50 * time.Millisecond,
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    testVR90Address,
			Primary:   0xB5,
			Secondary: 0x06,
			Data:      []byte{0x01, 0x00, 0x7F},
		},
	})
	if err != nil {
		t.Fatalf("Emulate() second call error = %v", err)
	}
	if !bytes.Equal(next.Frame.Data, wantData) {
		t.Fatalf("second Frame data = %x; want %x", next.Frame.Data, wantData)
	}
}

func TestVR90Target_MappedCommandPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("built-in rules win over mapped collisions", func(t *testing.T) {
		t.Parallel()

		profile := defaultVR90TestProfile()
		profile.EnableB509Discovery = true
		profile.MappedCommands = []VR90MappedCommand{
			{
				Name:         "mapped-identify-collision",
				Primary:      0x07,
				Secondary:    0x04,
				ResponseData: []byte{0xAA},
			},
			{
				Name:         "mapped-b509-collision",
				Primary:      0xB5,
				Secondary:    0x09,
				PayloadExact: []byte{0x24},
				ResponseData: []byte{0xBB},
			},
		}
		target, err := NewVR90Target(profile)
		if err != nil {
			t.Fatalf("NewVR90Target() error = %v", err)
		}

		identify, err := target.Emulate(RequestEvent{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0x07,
				Secondary: 0x04,
			},
		})
		if err != nil {
			t.Fatalf("identify Emulate() error = %v", err)
		}
		if identify.Rule != "identify" {
			t.Fatalf("identify Rule = %q; want identify", identify.Rule)
		}

		b509, err := target.Emulate(RequestEvent{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0xB5,
				Secondary: 0x09,
				Data:      []byte{0x24},
			},
		})
		if err != nil {
			t.Fatalf("b509 Emulate() error = %v", err)
		}
		if b509.Rule != "vaillant-b509-scanid" {
			t.Fatalf("b509 Rule = %q; want vaillant-b509-scanid", b509.Rule)
		}
		wantChunk, ok := vr90B509ScanIDChunk(profile.ScanID, 0x24)
		if !ok {
			t.Fatalf("vr90B509ScanIDChunk() ok = false")
		}
		if !bytes.Equal(b509.Frame.Data, wantChunk) {
			t.Fatalf("b509 Frame data = %x; want %x", b509.Frame.Data, wantChunk)
		}
	})

	t.Run("mapped commands follow declaration order", func(t *testing.T) {
		t.Parallel()

		profile := defaultVR90TestProfile()
		profile.MappedCommands = []VR90MappedCommand{
			{
				Name:          "first-prefix",
				Primary:       0xB5,
				Secondary:     0x06,
				PayloadPrefix: []byte{0x01},
				ResponseData:  []byte{0x10},
			},
			{
				Name:         "second-exact",
				Primary:      0xB5,
				Secondary:    0x06,
				PayloadExact: []byte{0x01, 0x02},
				ResponseData: []byte{0x20},
			},
		}
		target, err := NewVR90Target(profile)
		if err != nil {
			t.Fatalf("NewVR90Target() error = %v", err)
		}

		response, err := target.Emulate(RequestEvent{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0xB5,
				Secondary: 0x06,
				Data:      []byte{0x01, 0x02},
			},
		})
		if err != nil {
			t.Fatalf("Emulate() error = %v", err)
		}
		if response.Rule != "first-prefix" {
			t.Fatalf("Rule = %q; want first-prefix", response.Rule)
		}
		if !bytes.Equal(response.Frame.Data, []byte{0x10}) {
			t.Fatalf("Frame data = %x; want %x", response.Frame.Data, []byte{0x10})
		}
	})
}

func TestVR90Target_MappedCommandUnknownFallback(t *testing.T) {
	t.Parallel()

	profile := defaultVR90TestProfile()
	profile.MappedCommands = []VR90MappedCommand{
		{
			Name:          "mapped-room-temp",
			Primary:       0xB5,
			Secondary:     0x06,
			PayloadPrefix: []byte{0x01, 0x00},
			ResponseData:  []byte{0x00, 0x2A, 0x10},
		},
	}
	target, err := NewVR90Target(profile)
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	_, err = target.Emulate(RequestEvent{
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    testVR90Address,
			Primary:   0xB5,
			Secondary: 0x06,
			Data:      []byte{0x01, 0x01},
		},
	})
	if !errors.Is(err, ErrNoMatchingRule) {
		t.Fatalf("Emulate() error = %v; want %v", err, ErrNoMatchingRule)
	}
}

func TestSmokeVR90MinimalQuerySet(t *testing.T) {
	target, err := NewVR90Target(defaultVR90TestProfile())
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	harness := NewHarness(target)
	responses, err := harness.RunSequence([]QueryStep{
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
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
	if response.Frame.Source != testVR90Address || response.Frame.Target != 0x10 {
		t.Fatalf("Frame source/target = 0x%02x/0x%02x; want 0x%02x/0x10", response.Frame.Source, response.Frame.Target, testVR90Address)
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

func TestSmokeVR90B509DiscoveryQuerySet(t *testing.T) {
	profile := defaultVR90TestProfile()
	profile.EnableB509Discovery = true

	target, err := NewVR90Target(profile)
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	harness := NewHarness(target)
	responses, err := harness.RunSequence([]QueryStep{
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0x07,
				Secondary: 0x04,
			},
		},
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0xB5,
				Secondary: 0x09,
				Data:      []byte{0x24},
			},
		},
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0xB5,
				Secondary: 0x09,
				Data:      []byte{0x25},
			},
		},
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0xB5,
				Secondary: 0x09,
				Data:      []byte{0x26},
			},
		},
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0xB5,
				Secondary: 0x09,
				Data:      []byte{0x27},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunSequence() error = %v", err)
	}
	if len(responses) != 5 {
		t.Fatalf("len(responses) = %d; want 5", len(responses))
	}

	wantChunks := [][]byte{
		{0x00, '2', '1', '2', '3', '1', '6', '0', '0'},
		{0x00, '2', '0', '2', '6', '0', '9', '1', '4'},
		{0x00, '0', '9', '5', '3', '0', '3', '5', '4'},
		{0x00, '6', '9', 'N', '6', ' ', ' ', ' ', ' '},
	}
	for idx := range wantChunks {
		response := responses[idx+1]
		if response.Frame.Primary != 0xB5 || response.Frame.Secondary != 0x09 {
			t.Fatalf("response[%d] PB/SB = 0x%02x/0x%02x; want 0xB5/0x09", idx+1, response.Frame.Primary, response.Frame.Secondary)
		}
		if !bytes.Equal(response.Frame.Data, wantChunks[idx]) {
			t.Fatalf("response[%d] data = %x; want %x", idx+1, response.Frame.Data, wantChunks[idx])
		}
	}

	if err := ValidateResponseEnvelope(responses, ResponseEnvelope{
		MinDelay: 5 * time.Millisecond,
		MaxDelay: 30 * time.Millisecond,
	}); err != nil {
		t.Fatalf("ValidateResponseEnvelope() error = %v", err)
	}
}

func TestSmokeVR90MappedCommandQuerySet(t *testing.T) {
	profile := defaultVR90TestProfile()
	profile.EnableB509Discovery = true
	profile.MappedCommands = []VR90MappedCommand{
		{
			Name:          "mapped-room-temp",
			Primary:       0xB5,
			Secondary:     0x06,
			PayloadPrefix: []byte{0x01, 0x00},
			ResponseData:  []byte{0x00, 0x2A, 0x10},
		},
	}

	target, err := NewVR90Target(profile)
	if err != nil {
		t.Fatalf("NewVR90Target() error = %v", err)
	}

	harness := NewHarness(target)
	responses, err := harness.RunSequence([]QueryStep{
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0x07,
				Secondary: 0x04,
			},
		},
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0xB5,
				Secondary: 0x09,
				Data:      []byte{0x24},
			},
		},
		{
			Frame: protocol.Frame{
				Source:    0x10,
				Target:    testVR90Address,
				Primary:   0xB5,
				Secondary: 0x06,
				Data:      []byte{0x01, 0x00, 0x33},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunSequence() error = %v", err)
	}
	if len(responses) != 3 {
		t.Fatalf("len(responses) = %d; want 3", len(responses))
	}

	identify := responses[0]
	if identify.Rule != "identify" {
		t.Fatalf("responses[0].Rule = %q; want identify", identify.Rule)
	}
	if gotID := string(identify.Frame.Data[1:6]); gotID != DefaultVR90DeviceID {
		t.Fatalf("responses[0] DeviceID = %q; want %q", gotID, DefaultVR90DeviceID)
	}

	b509 := responses[1]
	if b509.Rule != "vaillant-b509-scanid" {
		t.Fatalf("responses[1].Rule = %q; want vaillant-b509-scanid", b509.Rule)
	}
	wantChunk, ok := vr90B509ScanIDChunk(profile.ScanID, 0x24)
	if !ok {
		t.Fatalf("vr90B509ScanIDChunk() ok = false")
	}
	if !bytes.Equal(b509.Frame.Data, wantChunk) {
		t.Fatalf("responses[1] data = %x; want %x", b509.Frame.Data, wantChunk)
	}

	mapped := responses[2]
	if mapped.Rule != "mapped-room-temp" {
		t.Fatalf("responses[2].Rule = %q; want mapped-room-temp", mapped.Rule)
	}
	if mapped.Frame.Primary != 0xB5 || mapped.Frame.Secondary != 0x06 {
		t.Fatalf("responses[2] PB/SB = 0x%02x/0x%02x; want 0xB5/0x06", mapped.Frame.Primary, mapped.Frame.Secondary)
	}
	wantMapped := []byte{0x00, 0x2A, 0x10}
	if !bytes.Equal(mapped.Frame.Data, wantMapped) {
		t.Fatalf("responses[2] data = %x; want %x", mapped.Frame.Data, wantMapped)
	}

	if err := ValidateResponseEnvelope(responses, ResponseEnvelope{
		MinDelay: 5 * time.Millisecond,
		MaxDelay: 30 * time.Millisecond,
	}); err != nil {
		t.Fatalf("ValidateResponseEnvelope() error = %v", err)
	}
}
