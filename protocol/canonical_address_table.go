package protocol

// Canonical address table API. All lookups in this file are O(1) via maps
// derived once from sourceAddressTableV1 at package init. The
// sourceAddressTableV1 itself is the docs-owned authoritative table from
// architecture/ebus_standard/12-source-address-table.md (canonical hash
// e78954445087f63064818ab60a2739b9a6b9bf0ae0147fbe92aac5ac76592103).
//
// These lookups replace ad-hoc math+nibble derivations. Math (+5/-5 with
// IsInitiatorCapableAddress) happens to produce identical results for the 25
// canonical pairs, but it does not expose tier (priority class) or free-use
// metadata, and it cannot reject by-construction non-canonical addresses
// (e.g. real devices like 0x26 / 0xEC that are not in the eBUS standard
// pair table).
//
// Vocabulary: the eBUS standard table uses "Source" (initiator-capable,
// arbitration-eligible address) and "Companion" (the responder-side
// peer at Source+0x05 mod 0x100). These match the doc terms.

var (
	canonicalSourceToCompanion = map[byte]byte{}
	canonicalCompanionToSource = map[byte]byte{}
	canonicalSourceTier        = map[byte]SourceAddressPriorityIndex{}
	canonicalSourceFreeUse     = map[byte]bool{}
)

func init() {
	for _, row := range sourceAddressTableV1 {
		canonicalSourceToCompanion[row.Source] = row.Companion
		canonicalCompanionToSource[row.Companion] = row.Source
		canonicalSourceTier[row.Source] = row.PriorityIndex
		canonicalSourceFreeUse[row.Source] = row.FreeUse
	}
}

// CompanionOfSource returns the canonical companion of source per the eBUS
// standard table. Returns (0, false) for any non-canonical source.
func CompanionOfSource(source byte) (byte, bool) {
	companion, ok := canonicalSourceToCompanion[source]
	return companion, ok
}

// SourceOfCompanion returns the canonical source of companion per the eBUS
// standard table. Returns (0, false) for any non-canonical companion.
func SourceOfCompanion(companion byte) (byte, bool) {
	source, ok := canonicalCompanionToSource[companion]
	return source, ok
}

// IsCanonicalSource reports whether addr is one of the 25 canonical source
// addresses. This is stricter than IsInitiatorCapableAddress because it
// asserts presence in the docs-owned table, not just nibble pattern
// validity.
func IsCanonicalSource(addr byte) bool {
	_, ok := canonicalSourceToCompanion[addr]
	return ok
}

// IsCanonicalCompanion reports whether addr is one of the 25 canonical
// companion addresses per the eBUS standard table.
func IsCanonicalCompanion(addr byte) bool {
	_, ok := canonicalCompanionToSource[addr]
	return ok
}

// SourceTier returns the priority class (p0..p4) of a canonical source.
// Returns ("", false) for non-canonical sources.
func SourceTier(source byte) (SourceAddressPriorityIndex, bool) {
	tier, ok := canonicalSourceTier[source]
	return tier, ok
}

// IsFreeUseSource reports whether a canonical source is marked free-use in
// the eBUS standard table (used by Helianthus startup-source-selection
// candidate ranking). Returns (false, false) for non-canonical sources.
func IsFreeUseSource(source byte) (bool, bool) {
	free, ok := canonicalSourceFreeUse[source]
	return free, ok
}
