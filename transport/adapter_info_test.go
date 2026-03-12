package transport

import (
	"testing"
)

func TestParseAdapterVersion_2Byte(t *testing.T) {
	data := []byte{0x23, 0x01} // version=0x23, features=0x01 (INFO supported)
	v, err := ParseAdapterVersion(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Version != 0x23 {
		t.Errorf("Version = 0x%02x; want 0x23", v.Version)
	}
	if !v.SupportsInfo {
		t.Error("SupportsInfo should be true")
	}
	if v.HasChecksum {
		t.Error("HasChecksum should be false for 2-byte response")
	}
	if v.HasBootloader {
		t.Error("HasBootloader should be false for 2-byte response")
	}
	if v.VersionResponseLen() != 2 {
		t.Errorf("VersionResponseLen() = %d; want 2", v.VersionResponseLen())
	}
}

func TestParseAdapterVersion_5Byte(t *testing.T) {
	data := []byte{0x23, 0x01, 0xAB, 0xCD, 0x19} // jumpers: enhanced+wifi+v3.1
	v, err := ParseAdapterVersion(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.HasChecksum {
		t.Error("HasChecksum should be true for 5-byte response")
	}
	if v.Checksum != 0xABCD {
		t.Errorf("Checksum = 0x%04x; want 0xABCD", v.Checksum)
	}
	if v.Jumpers != 0x19 {
		t.Errorf("Jumpers = 0x%02x; want 0x19", v.Jumpers)
	}
	if !v.IsWiFi {
		t.Error("IsWiFi should be true (jumpers & 0x08)")
	}
	if v.IsEthernet {
		t.Error("IsEthernet should be false")
	}
	if !v.IsV31 {
		t.Error("IsV31 should be true (jumpers & 0x10)")
	}
	if v.HasBootloader {
		t.Error("HasBootloader should be false for 5-byte response")
	}
	if v.VersionResponseLen() != 5 {
		t.Errorf("VersionResponseLen() = %d; want 5", v.VersionResponseLen())
	}
}

func TestParseAdapterVersion_8Byte(t *testing.T) {
	data := []byte{0x23, 0x01, 0xAB, 0xCD, 0x19, 0x10, 0xEF, 0x01}
	v, err := ParseAdapterVersion(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.HasBootloader {
		t.Error("HasBootloader should be true for 8-byte response")
	}
	if v.BootloaderVersion != 0x10 {
		t.Errorf("BootloaderVersion = 0x%02x; want 0x10", v.BootloaderVersion)
	}
	if v.BootloaderChecksum != 0xEF01 {
		t.Errorf("BootloaderChecksum = 0x%04x; want 0xEF01", v.BootloaderChecksum)
	}
	if v.VersionResponseLen() != 8 {
		t.Errorf("VersionResponseLen() = %d; want 8", v.VersionResponseLen())
	}
}

func TestParseAdapterVersion_TooShort(t *testing.T) {
	_, err := ParseAdapterVersion([]byte{0x23})
	if err == nil {
		t.Fatal("expected error for 1-byte response")
	}
}

func TestParseAdapterVersion_NoInfoSupport(t *testing.T) {
	data := []byte{0x23, 0x00} // features=0x00 (no INFO)
	v, err := ParseAdapterVersion(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.SupportsInfo {
		t.Error("SupportsInfo should be false when features bit 0 is clear")
	}
}

func TestSupportsInfoID_VersionGating(t *testing.T) {
	tests := []struct {
		name     string
		version  AdapterVersion
		id       AdapterInfoID
		expected bool
	}{
		{
			name:     "no INFO support",
			version:  AdapterVersion{SupportsInfo: false},
			id:       AdapterInfoTemperature,
			expected: false,
		},
		{
			name:     "basic ID always supported",
			version:  AdapterVersion{SupportsInfo: true},
			id:       AdapterInfoTemperature,
			expected: true,
		},
		{
			name:     "version ID always supported",
			version:  AdapterVersion{SupportsInfo: true},
			id:       AdapterInfoVersion,
			expected: true,
		},
		{
			name:     "reset info requires bootloader",
			version:  AdapterVersion{SupportsInfo: true, HasChecksum: true},
			id:       AdapterInfoResetInfo,
			expected: false,
		},
		{
			name:     "reset info with bootloader",
			version:  AdapterVersion{SupportsInfo: true, HasBootloader: true},
			id:       AdapterInfoResetInfo,
			expected: true,
		},
		{
			name:     "WiFi RSSI requires checksum+wifi",
			version:  AdapterVersion{SupportsInfo: true, HasChecksum: true, IsWiFi: false},
			id:       AdapterInfoWiFiRSSI,
			expected: false,
		},
		{
			name:     "WiFi RSSI with checksum+wifi",
			version:  AdapterVersion{SupportsInfo: true, HasChecksum: true, IsWiFi: true},
			id:       AdapterInfoWiFiRSSI,
			expected: true,
		},
		{
			name:     "WiFi RSSI without checksum",
			version:  AdapterVersion{SupportsInfo: true, HasChecksum: false, IsWiFi: true},
			id:       AdapterInfoWiFiRSSI,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.version.SupportsInfoID(tt.id)
			if got != tt.expected {
				t.Errorf("SupportsInfoID(%s) = %v; want %v", tt.id, got, tt.expected)
			}
		})
	}
}

func TestParseAdapterResetInfo(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		wantCause  string
		wantCode   byte
		wantCount  byte
		wantErr    bool
	}{
		{
			name:      "power on",
			data:      []byte{1, 5},
			wantCause: "power_on",
			wantCode:  1,
			wantCount: 5,
		},
		{
			name:      "watchdog",
			data:      []byte{3, 0},
			wantCause: "watchdog",
			wantCode:  3,
			wantCount: 0,
		},
		{
			name:      "unknown cause",
			data:      []byte{0xFF, 10},
			wantCause: "unknown",
			wantCode:  0xFF,
			wantCount: 10,
		},
		{
			name:    "too short",
			data:    []byte{1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseAdapterResetInfo(tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Cause != tt.wantCause {
				t.Errorf("Cause = %q; want %q", info.Cause, tt.wantCause)
			}
			if info.CauseCode != tt.wantCode {
				t.Errorf("CauseCode = %d; want %d", info.CauseCode, tt.wantCode)
			}
			if info.RestartCount != tt.wantCount {
				t.Errorf("RestartCount = %d; want %d", info.RestartCount, tt.wantCount)
			}
		})
	}
}

func TestAdapterInfoID_String(t *testing.T) {
	if s := AdapterInfoVersion.String(); s != "version" {
		t.Errorf("String() = %q; want %q", s, "version")
	}
	if s := AdapterInfoWiFiRSSI.String(); s != "wifi_rssi" {
		t.Errorf("String() = %q; want %q", s, "wifi_rssi")
	}
	if s := AdapterInfoID(0xFF).String(); s != "unknown(0xff)" {
		t.Errorf("String() = %q; want %q", s, "unknown(0xff)")
	}
}
