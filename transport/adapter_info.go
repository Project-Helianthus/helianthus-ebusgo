package transport

import (
	"encoding/binary"
	"fmt"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// AdapterInfoID represents an enhanced protocol INFO query identifier (0x00–0x07).
type AdapterInfoID byte

const (
	AdapterInfoVersion      AdapterInfoID = 0x00
	AdapterInfoHardwareID   AdapterInfoID = 0x01
	AdapterInfoHardwareConf AdapterInfoID = 0x02
	AdapterInfoTemperature  AdapterInfoID = 0x03
	AdapterInfoSupplyVolt   AdapterInfoID = 0x04
	AdapterInfoBusVoltage   AdapterInfoID = 0x05
	AdapterInfoResetInfo    AdapterInfoID = 0x06
	AdapterInfoWiFiRSSI     AdapterInfoID = 0x07
)

// adapterInfoIDNames maps INFO IDs to human-readable names.
var adapterInfoIDNames = [8]string{
	"version", "hw_id", "hw_config", "temperature",
	"supply_voltage", "bus_voltage", "reset_info", "wifi_rssi",
}

// String returns the human-readable name of an INFO ID.
func (id AdapterInfoID) String() string {
	if int(id) < len(adapterInfoIDNames) {
		return adapterInfoIDNames[id]
	}
	return fmt.Sprintf("unknown(0x%02x)", byte(id))
}

// AdapterVersion holds parsed INFO ID 0x00 response data.
// Fields beyond Version/Features are populated only when the response is long
// enough (wire-observable gating via response length).
type AdapterVersion struct {
	Version  byte
	Features byte

	Checksum uint16 // firmware checksum; valid when HasChecksum
	Jumpers  byte   // jumper/capability flags; valid when HasChecksum

	BootloaderVersion  byte   // valid when HasBootloader
	BootloaderChecksum uint16 // valid when HasBootloader

	// Derived capability flags (wire-observable, no magic dates).
	HasChecksum   bool // version_len >= 5
	HasBootloader bool // version_len == 8
	SupportsInfo  bool // features & 0x01
	IsWiFi        bool // jumpers & 0x08
	IsEthernet    bool // jumpers & 0x04
	IsHighSpeed   bool // jumpers & 0x02
	IsV31         bool // jumpers & 0x10
}

// VersionResponseLen returns the original wire response length category.
func (v AdapterVersion) VersionResponseLen() int {
	if v.HasBootloader {
		return 8
	}
	if v.HasChecksum {
		return 5
	}
	return 2
}

// SupportsInfoID returns whether the adapter supports a given INFO ID based on
// wire-observable evidence from the version response.
func (v AdapterVersion) SupportsInfoID(id AdapterInfoID) bool {
	if !v.SupportsInfo {
		return false
	}
	switch id {
	case AdapterInfoVersion, AdapterInfoHardwareID, AdapterInfoHardwareConf,
		AdapterInfoTemperature, AdapterInfoSupplyVolt, AdapterInfoBusVoltage:
		return true
	case AdapterInfoResetInfo:
		return v.HasBootloader // version_len == 8
	case AdapterInfoWiFiRSSI:
		return v.HasChecksum && v.IsWiFi // version_len >= 5 AND WiFi
	default:
		return false
	}
}

// ParseAdapterVersion parses an INFO ID 0x00 response.
// Accepts 2, 5, or 8 byte payloads.
func ParseAdapterVersion(data []byte) (AdapterVersion, error) {
	switch len(data) {
	case 2, 5, 8:
	default:
		return AdapterVersion{}, fmt.Errorf("adapter version response has invalid length (%d bytes): %w", len(data), ebuserrors.ErrInvalidPayload)
	}

	v := AdapterVersion{
		Version:  data[0],
		Features: data[1],
	}
	v.SupportsInfo = v.Features&0x01 != 0

	switch len(data) {
	case 8:
		v.HasBootloader = true
		v.BootloaderVersion = data[5]
		v.BootloaderChecksum = binary.BigEndian.Uint16(data[6:8])
		fallthrough
	case 5:
		v.HasChecksum = true
		v.Checksum = binary.BigEndian.Uint16(data[2:4])
		v.Jumpers = data[4]
		v.IsWiFi = v.Jumpers&0x08 != 0
		v.IsEthernet = v.Jumpers&0x04 != 0
		v.IsHighSpeed = v.Jumpers&0x02 != 0
		v.IsV31 = v.Jumpers&0x10 != 0
	}

	return v, nil
}

// AdapterResetInfo holds parsed INFO ID 0x06 response data.
type AdapterResetInfo struct {
	Cause        string
	CauseCode    byte
	RestartCount byte
}

// resetCauseNames maps reset cause codes to human-readable strings.
var resetCauseNames = map[byte]string{
	1: "power_on",
	2: "brown_out",
	3: "watchdog",
	4: "clear",
	5: "external_reset",
	6: "stack_overflow",
	7: "memory_failure",
}

// ParseAdapterResetInfo parses an INFO ID 0x06 response (2 bytes).
func ParseAdapterResetInfo(data []byte) (AdapterResetInfo, error) {
	if len(data) < 2 {
		return AdapterResetInfo{}, fmt.Errorf("adapter reset info too short (%d bytes): %w", len(data), ebuserrors.ErrInvalidPayload)
	}

	cause := "unknown"
	if name, ok := resetCauseNames[data[0]]; ok {
		cause = name
	}

	return AdapterResetInfo{
		Cause:        cause,
		CauseCode:    data[0],
		RestartCount: data[1],
	}, nil
}
