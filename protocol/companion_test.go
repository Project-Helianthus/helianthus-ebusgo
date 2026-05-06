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
		{"master 0x04 to slave 0xFF", 0x04, 0xFF, true},
		{"slave 0xFF to master 0x04", 0xFF, 0x04, true},
		{"master 0xF1 to slave 0xF6", 0xF1, 0xF6, true},
		{"slave 0xF6 to master 0xF1", 0xF6, 0xF1, true},
		{"master 0x08 to slave 0x03", 0x08, 0x03, true},
		{"slave 0x03 to master 0x08", 0x03, 0x08, true},
		{"master 0x10 to slave 0x15", 0x10, 0x15, true},
		{"slave 0x15 to master 0x10", 0x15, 0x10, true},
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
		{"0x26 has no initiator-capable 0x21 master", 0x26},
		{"0xEC has no initiator-capable 0xE7 master", 0xEC},
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

		slave, found := Companion(addr)
		if !found {
			t.Fatalf("Companion(0x%02X) found = false; want true", addr)
		}
		wantSlave := addr + 5
		if slave != wantSlave {
			t.Fatalf("Companion(0x%02X) = (0x%02X, true); want (0x%02X, true)", addr, slave, wantSlave)
		}
		if gotMaster := slave - 5; gotMaster != addr {
			t.Fatalf("Companion(0x%02X) round-trip master = 0x%02X; want 0x%02X", addr, gotMaster, addr)
		}
	}
}
