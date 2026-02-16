package emulation

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/d3vi1/helianthus-ebusgo/protocol"
)

func TestDefaultVR92Profile(t *testing.T) {
	t.Parallel()

	profile := DefaultVR92Profile()
	if profile.Address != DefaultVR92Address ||
		profile.Manufacturer != DefaultVR92Manufacturer ||
		profile.DeviceID != DefaultVR92DeviceID ||
		profile.Software != DefaultVR92Software ||
		profile.Hardware != DefaultVR92Hardware {
		t.Fatalf("DefaultVR92Profile() mismatch: %+v", profile)
	}
}

func TestNewVR92Target_IdentifyResponseUsesObservedIdentity(t *testing.T) {
	t.Parallel()

	target, err := NewVR92Target(VR92Profile{})
	if err != nil {
		t.Fatalf("NewVR92Target() error = %v", err)
	}

	response, err := target.Emulate(RequestEvent{
		At: 12 * time.Millisecond,
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    DefaultVR92Address,
			Primary:   0x07,
			Secondary: 0x04,
		},
	})
	if err != nil {
		t.Fatalf("Emulate() error = %v", err)
	}

	wantData := []byte{
		DefaultVR92Manufacturer,
		'V', 'R', '_', '9', '2',
		0x05, 0x14,
		0x12, 0x04,
	}
	if !bytes.Equal(response.Frame.Data, wantData) {
		t.Fatalf("Frame data = %x; want %x", response.Frame.Data, wantData)
	}
}

func TestNewVR92Target_B509RequiresScanID(t *testing.T) {
	t.Parallel()

	_, err := NewVR92Target(VR92Profile{
		EnableB509Discovery: true,
	})
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("NewVR92Target() error = %v; want %v", err, ErrInvalidConfiguration)
	}
}

func TestVR92Target_B509SelectorBehavior(t *testing.T) {
	t.Parallel()

	profile := DefaultVR92Profile()
	profile.EnableB509Discovery = true
	profile.ScanID = "21223400202621480082014267N7"

	target, err := NewVR92Target(profile)
	if err != nil {
		t.Fatalf("NewVR92Target() error = %v", err)
	}

	response, err := target.Emulate(RequestEvent{
		Frame: protocol.Frame{
			Source:    0x10,
			Target:    DefaultVR92Address,
			Primary:   0xB5,
			Secondary: 0x09,
			Data:      []byte{0x24},
		},
	})
	if err != nil {
		t.Fatalf("Emulate() error = %v", err)
	}
	wantChunk, ok := vr90B509ScanIDChunk(profile.ScanID, 0x24)
	if !ok {
		t.Fatalf("vr90B509ScanIDChunk() ok = false")
	}
	if !bytes.Equal(response.Frame.Data, wantChunk) {
		t.Fatalf("Frame data = %x; want %x", response.Frame.Data, wantChunk)
	}
}
