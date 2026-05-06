package protocol

import "testing"

// Tests for the canonical-address-table-derived companion API. These RED
// tests verify that companion derivation, source/companion lookup, tier
// classification, and free-use status are sourced from the docs-owned
// sourceAddressTableV1
// (helianthus-docs-ebus/architecture/ebus_standard/12-source-address-table.md)
// rather than from byte-arithmetic guesses with nibble-pattern checks.
//
// Vocabulary follows the docs-owned table: "Source" (initiator-capable,
// arbitration-eligible address) and "Companion" (the responder-side peer
// at Source+0x05 mod 0x100).

// canonicalPairs is the operator-pinned ground truth for the 25 canonical
// source/companion pairs from the eBUS standard table. Tests assert each
// direction.
var canonicalPairs = []struct {
	source    byte
	companion byte
	tier      SourceAddressPriorityIndex
	free      bool
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

func TestCanonical_CompanionOfSource_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("source_0x"+hex2(pair.source), func(t *testing.T) {
			t.Parallel()
			got, ok := CompanionOfSource(pair.source)
			if !ok || got != pair.companion {
				t.Fatalf("CompanionOfSource(0x%02X) = (0x%02X, %v); want (0x%02X, true)", pair.source, got, ok, pair.companion)
			}
		})
	}
}

func TestCanonical_SourceOfCompanion_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("companion_0x"+hex2(pair.companion), func(t *testing.T) {
			t.Parallel()
			got, ok := SourceOfCompanion(pair.companion)
			if !ok || got != pair.source {
				t.Fatalf("SourceOfCompanion(0x%02X) = (0x%02X, %v); want (0x%02X, true)", pair.companion, got, ok, pair.source)
			}
		})
	}
}

func TestCanonical_IsCanonicalSource_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("source_0x"+hex2(pair.source), func(t *testing.T) {
			t.Parallel()
			if !IsCanonicalSource(pair.source) {
				t.Fatalf("IsCanonicalSource(0x%02X) = false; want true", pair.source)
			}
		})
	}
}

func TestCanonical_IsCanonicalCompanion_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("companion_0x"+hex2(pair.companion), func(t *testing.T) {
			t.Parallel()
			if !IsCanonicalCompanion(pair.companion) {
				t.Fatalf("IsCanonicalCompanion(0x%02X) = false; want true", pair.companion)
			}
		})
	}
}

func TestCanonical_SourceTier_AllPairs(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("source_0x"+hex2(pair.source), func(t *testing.T) {
			t.Parallel()
			got, ok := SourceTier(pair.source)
			if !ok || got != pair.tier {
				t.Fatalf("SourceTier(0x%02X) = (%q, %v); want (%q, true)", pair.source, got, ok, pair.tier)
			}
		})
	}
}

func TestCanonical_NonCanonical_RejectsSource(t *testing.T) {
	t.Parallel()
	// Addresses that pass IsInitiatorCapableAddress nibble check but are
	// not in the canonical table — none exist for the +5 math because
	// IsInitiatorCapableAddress matches exactly the 25 canonical sources.
	// However, any other byte (e.g. 0x02, 0x26, 0x38 acting as src) MUST
	// NOT be classified as a canonical source.
	nonSources := []byte{0x02, 0x04, 0x05, 0x09, 0x14, 0x21, 0x26, 0x38, 0x44, 0x84, 0xEC}
	for _, addr := range nonSources {
		addr := addr
		t.Run("non_source_0x"+hex2(addr), func(t *testing.T) {
			t.Parallel()
			if IsCanonicalSource(addr) {
				t.Fatalf("IsCanonicalSource(0x%02X) = true; want false (not in canonical table)", addr)
			}
			if _, ok := SourceTier(addr); ok {
				t.Fatalf("SourceTier(0x%02X) ok = true; want false", addr)
			}
			if _, ok := CompanionOfSource(addr); ok {
				t.Fatalf("CompanionOfSource(0x%02X) ok = true; want false (not a source)", addr)
			}
		})
	}
}

func TestCanonical_NonCanonical_RejectsCompanion(t *testing.T) {
	t.Parallel()
	// 0x26 (VR_71 companion-like physical address) and 0xEC (SOL00
	// companion-like physical address) are real devices but NOT in the
	// canonical pair table — they have no math companion.
	nonCompanions := []byte{0x02, 0x07, 0x10, 0x21, 0x26, 0x33, 0xEC, 0xFE, 0xFF}
	for _, addr := range nonCompanions {
		addr := addr
		t.Run("non_companion_0x"+hex2(addr), func(t *testing.T) {
			t.Parallel()
			if IsCanonicalCompanion(addr) {
				t.Fatalf("IsCanonicalCompanion(0x%02X) = true; want false (not in canonical table)", addr)
			}
			if _, ok := SourceOfCompanion(addr); ok {
				t.Fatalf("SourceOfCompanion(0x%02X) ok = true; want false (not a companion)", addr)
			}
		})
	}
}

func TestCanonical_FreeUseFlag(t *testing.T) {
	t.Parallel()
	for _, pair := range canonicalPairs {
		pair := pair
		t.Run("source_0x"+hex2(pair.source), func(t *testing.T) {
			t.Parallel()
			got, ok := IsFreeUseSource(pair.source)
			if !ok {
				t.Fatalf("IsFreeUseSource(0x%02X) ok = false; want true (canonical source)", pair.source)
			}
			if got != pair.free {
				t.Fatalf("IsFreeUseSource(0x%02X) = %v; want %v", pair.source, got, pair.free)
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
		t.Run("source_0x"+hex2(pair.source), func(t *testing.T) {
			t.Parallel()
			got, ok := Companion(pair.source)
			if !ok || got != pair.companion {
				t.Fatalf("Companion(0x%02X source) = (0x%02X, %v); want (0x%02X, true)", pair.source, got, ok, pair.companion)
			}
		})
		t.Run("companion_0x"+hex2(pair.companion), func(t *testing.T) {
			t.Parallel()
			got, ok := Companion(pair.companion)
			if !ok || got != pair.source {
				t.Fatalf("Companion(0x%02X companion) = (0x%02X, %v); want (0x%02X, true)", pair.companion, got, ok, pair.source)
			}
		})
	}
}

// TestCanonical_TableInvariants asserts the docs-owned source-address table
// has exactly 25 unique source/companion pairs. Catches accidental future
// table expansion or silent overwrite from duplicate keys (Codex review nit
// from PR #149).
func TestCanonical_TableInvariants(t *testing.T) {
	t.Parallel()

	const wantRows = 25
	if got := len(sourceAddressTableV1); got != wantRows {
		t.Fatalf("sourceAddressTableV1 has %d rows; want %d", got, wantRows)
	}

	uniqueSources := make(map[byte]bool, wantRows)
	uniqueCompanions := make(map[byte]bool, wantRows)
	for _, row := range sourceAddressTableV1 {
		if uniqueSources[row.Source] {
			t.Fatalf("duplicate Source 0x%02X in sourceAddressTableV1", row.Source)
		}
		uniqueSources[row.Source] = true
		if uniqueCompanions[row.Companion] {
			t.Fatalf("duplicate Companion 0x%02X in sourceAddressTableV1", row.Companion)
		}
		uniqueCompanions[row.Companion] = true
	}

	// Map coverage parity: the 4 derived maps must each have exactly
	// wantRows entries, matching the table 1:1.
	if got := len(canonicalSourceToCompanion); got != wantRows {
		t.Fatalf("canonicalSourceToCompanion has %d entries; want %d", got, wantRows)
	}
	if got := len(canonicalCompanionToSource); got != wantRows {
		t.Fatalf("canonicalCompanionToSource has %d entries; want %d", got, wantRows)
	}
	if got := len(canonicalSourceTier); got != wantRows {
		t.Fatalf("canonicalSourceTier has %d entries; want %d", got, wantRows)
	}
	if got := len(canonicalSourceFreeUse); got != wantRows {
		t.Fatalf("canonicalSourceFreeUse has %d entries; want %d", got, wantRows)
	}
}

func hex2(b byte) string {
	const hexDigits = "0123456789ABCDEF"
	return string([]byte{hexDigits[b>>4], hexDigits[b&0x0F]})
}
