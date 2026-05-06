package protocol

import "testing"

// Tests for the canonical-address-table-derived companion API. These RED tests
// verify that companion derivation, master/slave lookup, tier classification,
// and free-use status are sourced from the docs-owned sourceAddressTableV1
// (helianthus-docs-ebus/architecture/ebus_standard/12-source-address-table.md)
// rather than from byte-arithmetic guesses with nibble-pattern checks.

// canonicalPairs is the operator-pinned ground truth for the 25 canonical
// master/slave pairs from the eBUS standard table. Tests assert each direction.
var canonicalPairs = []struct {
	master byte
	slave  byte
	tier   SourceAddressPriorityIndex
	free   bool
}{
	{0x00, 0x05, SourceAddressPriorityP0, false},
	{0x10, 0x15, SourceAddressPriorityP0, false},
	{0x30, 0x35, SourceAddressPriorityP0, false},
	{0x70, 0x75, SourceAddressPriorityP0, false},
	{0xF0, 0xF5, SourceAddressPriorityP0, false},
	{0x01, 0x06, SourceAddressPriorityP1, false},
	{0x11, 0x16, SourceAddressPriorityP1, false},
	{0x31, 0x36, SourceAddressPriorityP1, false},
	{0x71, 0x76, SourceAddressPriorityP1, false},
	{0xF1, 0xF6, SourceAddressPriorityP1, false},
	{0x03, 0x08, SourceAddressPriorityP2, false},
	{0x13, 0x18, SourceAddressPriorityP2, false},
	{0x33, 0x38, SourceAddressPriorityP2, false},
	{0x73, 0x78, SourceAddressPriorityP2, false},
	{0xF3, 0xF8, SourceAddressPriorityP2, false},
	{0x07, 0x0C, SourceAddressPriorityP3, true},
	{0x17, 0x1C, SourceAddressPriorityP3, true},
	{0x37, 0x3C, SourceAddressPriorityP3, true},
	{0x77, 0x7C, SourceAddressPriorityP3, true},
	{0xF7, 0xFC, SourceAddressPriorityP3, true},
	{0x0F, 0x14, SourceAddressPriorityP4, false},
	{0x1F, 0x24, SourceAddressPriorityP4, true},
	{0x3F, 0x44, SourceAddressPriorityP4, true},
	{0x7F, 0x84, SourceAddressPriorityP4, true},
	{0xFF, 0x04, SourceAddressPriorityP4, false},
}

func TestCanonical_SlaveOfMaster_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("master_0x"+hex2(pair.master), func(t *testing.T) {
			t.Parallel()
			got, ok := SlaveOfMaster(pair.master)
			if !ok || got != pair.slave {
				t.Fatalf("SlaveOfMaster(0x%02X) = (0x%02X, %v); want (0x%02X, true)", pair.master, got, ok, pair.slave)
			}
		})
	}
}

func TestCanonical_MasterOfSlave_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("slave_0x"+hex2(pair.slave), func(t *testing.T) {
			t.Parallel()
			got, ok := MasterOfSlave(pair.slave)
			if !ok || got != pair.master {
				t.Fatalf("MasterOfSlave(0x%02X) = (0x%02X, %v); want (0x%02X, true)", pair.slave, got, ok, pair.master)
			}
		})
	}
}

func TestCanonical_IsCanonicalMaster_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("master_0x"+hex2(pair.master), func(t *testing.T) {
			t.Parallel()
			if !IsCanonicalMaster(pair.master) {
				t.Fatalf("IsCanonicalMaster(0x%02X) = false; want true", pair.master)
			}
		})
	}
}

func TestCanonical_IsCanonicalSlave_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("slave_0x"+hex2(pair.slave), func(t *testing.T) {
			t.Parallel()
			if !IsCanonicalSlave(pair.slave) {
				t.Fatalf("IsCanonicalSlave(0x%02X) = false; want true", pair.slave)
			}
		})
	}
}

func TestCanonical_MasterTier_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("master_0x"+hex2(pair.master), func(t *testing.T) {
			t.Parallel()
			got, ok := MasterTier(pair.master)
			if !ok || got != pair.tier {
				t.Fatalf("MasterTier(0x%02X) = (%q, %v); want (%q, true)", pair.master, got, ok, pair.tier)
			}
		})
	}
}

func TestCanonical_NonCanonical_RejectsMaster(t *testing.T) {
	t.Parallel()
	// Addresses that pass IsInitiatorCapableAddress nibble check but are
	// not in the canonical table — none exist for the +5 math because
	// IsInitiatorCapableAddress matches exactly the 25 canonical masters.
	// However, any other byte (e.g. 0x02, 0x26, 0x38 acting as src) MUST
	// NOT be classified as a canonical master.
	nonMasters := []byte{0x02, 0x04, 0x05, 0x09, 0x14, 0x21, 0x26, 0x38, 0x44, 0x84, 0xEC}
	for _, addr := range nonMasters {
		addr := addr
		t.Run("non_master_0x"+hex2(addr), func(t *testing.T) {
			t.Parallel()
			if IsCanonicalMaster(addr) {
				t.Fatalf("IsCanonicalMaster(0x%02X) = true; want false (not in canonical table)", addr)
			}
			if _, ok := MasterTier(addr); ok {
				t.Fatalf("MasterTier(0x%02X) ok = true; want false", addr)
			}
			if _, ok := SlaveOfMaster(addr); ok {
				t.Fatalf("SlaveOfMaster(0x%02X) ok = true; want false (not a master)", addr)
			}
		})
	}
}

func TestCanonical_NonCanonical_RejectsSlave(t *testing.T) {
	t.Parallel()
	// 0x26 (VR_71 slave) and 0xEC (SOL00 slave) are real devices but NOT
	// in the canonical pair table — they have no math companion.
	nonSlaves := []byte{0x02, 0x07, 0x10, 0x21, 0x26, 0x33, 0xEC, 0xFE, 0xFF}
	for _, addr := range nonSlaves {
		addr := addr
		t.Run("non_slave_0x"+hex2(addr), func(t *testing.T) {
			t.Parallel()
			if IsCanonicalSlave(addr) {
				t.Fatalf("IsCanonicalSlave(0x%02X) = true; want false (not in canonical table)", addr)
			}
			if _, ok := MasterOfSlave(addr); ok {
				t.Fatalf("MasterOfSlave(0x%02X) ok = true; want false (not a slave)", addr)
			}
		})
	}
}

func TestCanonical_FreeUseFlag(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("master_0x"+hex2(pair.master), func(t *testing.T) {
			t.Parallel()
			got, ok := IsFreeUseMaster(pair.master)
			if !ok {
				t.Fatalf("IsFreeUseMaster(0x%02X) ok = false; want true (canonical master)", pair.master)
			}
			if got != pair.free {
				t.Fatalf("IsFreeUseMaster(0x%02X) = %v; want %v", pair.master, got, pair.free)
			}
		})
	}
}

func TestCanonical_Companion_BackedByTable(t *testing.T) {
	t.Parallel()
	// After A.7a, Companion MUST resolve via the canonical table — not via
	// math + nibble check. The 25 canonical pairs are returned in both
	// directions; everything else is (0, false).
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("master_0x"+hex2(pair.master), func(t *testing.T) {
			t.Parallel()
			got, ok := Companion(pair.master)
			if !ok || got != pair.slave {
				t.Fatalf("Companion(0x%02X master) = (0x%02X, %v); want (0x%02X, true)", pair.master, got, ok, pair.slave)
			}
		})
		t.Run("slave_0x"+hex2(pair.slave), func(t *testing.T) {
			t.Parallel()
			got, ok := Companion(pair.slave)
			if !ok || got != pair.master {
				t.Fatalf("Companion(0x%02X slave) = (0x%02X, %v); want (0x%02X, true)", pair.slave, got, ok, pair.master)
			}
		})
	}
}

func hex2(b byte) string {
	const hexDigits = "0123456789ABCDEF"
	return string([]byte{hexDigits[b>>4], hexDigits[b&0x0F]})
}
