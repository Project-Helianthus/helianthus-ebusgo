package protocol

import "testing"

// Phase C M-C1: AddressClass enum + AddressClassOf classifier per the
// docs-owned 256-byte taxonomy in
// helianthus-docs-ebus/architecture/ebus_standard/12-address-table.md
// (frozen hash 316baf20ab0d0a64b36613bb8c7604d7570fecc01071daca94931029ae82ebec).
//
// The enum has exactly 4 classes:
//   - AddressClassReserved (zero value, fail-closed)
//   - AddressClassMaster
//   - AddressClassSlave
//   - AddressClassBroadcast
//
// Reserved is the zero value so an uninitialized AddressClass variable
// cannot be confused with a "valid" classification — any code path that
// fails to populate an AddressClass falls through to Reserved (which the
// Phase C validator treats as a hard reject).

func TestAddressClass_ZeroValueIsReserved(t *testing.T) {
	t.Parallel()
	var zero AddressClass
	if zero != AddressClassReserved {
		t.Fatalf("zero AddressClass = %v; want AddressClassReserved (zero-value-fail-closed invariant)", zero)
	}
}

func TestAddressClassOf_AllCanonicalSourcesAreMaster(t *testing.T) {
	t.Parallel()
	for _, row := range sourceAddressTableV1 {
		row := row
		t.Run("source_0x"+hex2(row.Source), func(t *testing.T) {
			t.Parallel()
			if got := AddressClassOf(row.Source); got != AddressClassMaster {
				t.Fatalf("AddressClassOf(0x%02X) = %v; want Master", row.Source, got)
			}
		})
	}
}

func TestAddressClassOf_AllCanonicalCompanionsAreSlave(t *testing.T) {
	t.Parallel()
	for _, row := range sourceAddressTableV1 {
		row := row
		t.Run("companion_0x"+hex2(row.Companion), func(t *testing.T) {
			t.Parallel()
			if got := AddressClassOf(row.Companion); got != AddressClassSlave {
				t.Fatalf("AddressClassOf(0x%02X) = %v; want Slave (canonical companion of 0x%02X)", row.Companion, got, row.Source)
			}
		})
	}
}

func TestAddressClassOf_BroadcastAndReserved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr byte
		want AddressClass
		desc string
	}{
		{0xFE, AddressClassBroadcast, "broadcast (Anhang line 305 + Spec_Prot_12 §4.3)"},
		{0xAA, AddressClassReserved, "SYN symbol (Anhang line 225 + Spec_Prot_12 §5.1)"},
		{0xA9, AddressClassReserved, "ESCAPE symbol (Anhang line 224 + Spec_Prot_12 §5.1)"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("addr_0x"+hex2(tc.addr), func(t *testing.T) {
			t.Parallel()
			if got := AddressClassOf(tc.addr); got != tc.want {
				t.Fatalf("AddressClassOf(0x%02X) = %v; want %v (%s)", tc.addr, got, tc.want, tc.desc)
			}
		})
	}
}

// TestAddressClassOf_ImplicitSlavesAreSlave asserts every byte that is
// neither a canonical Master nor Broadcast nor Reserved is classified as
// Slave (Anhang lines 75-304 enumerate 203 Slave NNN labels, plus 25
// reserved-companion entries that share the Slave class for protocol
// purposes).
func TestAddressClassOf_ImplicitSlavesAreSlave(t *testing.T) {
	t.Parallel()
	masterSet := make(map[byte]bool, 25)
	companionSet := make(map[byte]bool, 25)
	for _, row := range sourceAddressTableV1 {
		masterSet[row.Source] = true
		companionSet[row.Companion] = true
	}
	for i := 0; i <= 0xFF; i++ {
		addr := byte(i)
		if masterSet[addr] {
			continue // already covered
		}
		if addr == 0xFE || addr == 0xAA || addr == 0xA9 {
			continue
		}
		got := AddressClassOf(addr)
		if got != AddressClassSlave {
			t.Errorf("AddressClassOf(0x%02X) = %v; want Slave (implicit slave per spec)", addr, got)
		}
	}
}

// TestAddressClassOf_Cardinality256 asserts the partition is exhaustive.
func TestAddressClassOf_Cardinality256(t *testing.T) {
	t.Parallel()
	counts := map[AddressClass]int{}
	for i := 0; i <= 0xFF; i++ {
		counts[AddressClassOf(byte(i))]++
	}
	want := map[AddressClass]int{
		AddressClassMaster:    25,
		AddressClassSlave:     228,
		AddressClassBroadcast: 1,
		AddressClassReserved:  2,
	}
	for class, wantCount := range want {
		if counts[class] != wantCount {
			t.Errorf("class %v count = %d; want %d", class, counts[class], wantCount)
		}
	}
	total := counts[AddressClassMaster] + counts[AddressClassSlave] + counts[AddressClassBroadcast] + counts[AddressClassReserved]
	if total != 256 {
		t.Errorf("total partition = %d; want 256", total)
	}
}

// TestAddressClass_StringStable asserts the String() method returns
// stable lower-case names for diagnostics + log lines.
func TestAddressClass_StringStable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		c    AddressClass
		want string
	}{
		{AddressClassReserved, "reserved"},
		{AddressClassMaster, "master"},
		{AddressClassSlave, "slave"},
		{AddressClassBroadcast, "broadcast"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.c.String(); got != tc.want {
				t.Fatalf("(%v).String() = %q; want %q", tc.c, got, tc.want)
			}
		})
	}
}
