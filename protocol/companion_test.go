package protocol

import "testing"

// RED tests for cruise plan address-table-registry-w19-26 M0B/ebusgo — references M2 Companion func that intentionally doesn't exist yet.

func TestCompanion_OperatorPinnedCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		addr      byte
		want      byte
		wantFound bool
	}{
		{"initiator 0x04 to target 0xFF", 0x04, 0xFF, true},
		{"target 0xFF to initiator 0x04", 0xFF, 0x04, true},
		{"initiator 0xF1 to target 0xF6", 0xF1, 0xF6, true},
		{"target 0xF6 to initiator 0xF1", 0xF6, 0xF1, true},
		{"initiator 0x08 to target 0x03", 0x08, 0x03, true},
		{"target 0x03 to initiator 0x08", 0x03, 0x08, true},
		{"initiator 0x10 to target 0x15", 0x10, 0x15, true},
		{"target 0x15 to initiator 0x10", 0x15, 0x10, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, found := Companion(tc.addr)
			if got != tc.want || found != tc.wantFound {
				t.Fatalf("Companion(0x%02X) = (0x%02X, %v); want (0x%02X, %v)", tc.addr, got, found, tc.want, tc.wantFound)
			}
		})
	}
}

func TestCompanion_NoMasterPair(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr byte
	}{
		{"0x26 has no initiator-capable 0x21 initiator-capable byte", 0x26},
		{"0xEC has no initiator-capable 0xE7 initiator-capable byte", 0xEC},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, found := Companion(tc.addr)
			if got != 0 || found {
				t.Fatalf("Companion(0x%02X) = (0x%02X, %v); want (0x00, false)", tc.addr, got, found)
			}
		})
	}
}

func TestCompanion_MasterToSlave_OffsetFive(t *testing.T) {
	t.Parallel()

	for i := 0; i <= 0xFF; i++ {
		addr := byte(i)
		if !IsInitiatorCapableAddress(addr) {
			continue
		}

		target, found := Companion(addr)
		if !found {
			t.Fatalf("Companion(0x%02X) found = false; want true", addr)
		}
		wantTarget := addr + 5
		if target != wantTarget {
			t.Fatalf("Companion(0x%02X) = (0x%02X, true); want (0x%02X, true)", addr, target, wantTarget)
		}
		if gotInitiator := target - 5; gotInitiator != addr {
			t.Fatalf("Companion(0x%02X) round-trip initiator = 0x%02X; want 0x%02X", addr, gotInitiator, addr)
		}
	}
}
