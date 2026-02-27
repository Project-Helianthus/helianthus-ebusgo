package protocol

import "testing"

func TestIsInitiatorCapableAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr byte
		want bool
	}{
		{"0x00", 0x00, true},
		{"0x01", 0x01, true},
		{"0x03", 0x03, true},
		{"0x07", 0x07, true},
		{"0x0F", 0x0F, true},
		{"0x10", 0x10, true},
		{"0x70", 0x70, true},
		{"0xF0", 0xF0, true},
		{"0xF7", 0xF7, true},
		{"0xFF", 0xFF, true},
		{"0x37", 0x37, true},
		{"0x15 (slave)", 0x15, false},
		{"0x1C (slave)", 0x1C, false},
		{"0x75 (slave)", 0x75, false},
		{"0x50 (not in table)", 0x50, false},
		{"0xFE (not in table)", 0xFE, false},
		{"0xA9 (SymbolEscape)", SymbolEscape, false},
		{"0xAA (SymbolSyn)", SymbolSyn, false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsInitiatorCapableAddress(tc.addr); got != tc.want {
				t.Fatalf("IsInitiatorCapableAddress(0x%02x) = %v; want %v", tc.addr, got, tc.want)
			}
		})
	}
}
