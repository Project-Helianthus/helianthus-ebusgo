package protocol

// Companion returns the eBUS companion address (initiator/target pair) for addr,
// resolved through the docs-owned canonical address table
// (architecture/ebus_standard/12-source-address-table.md, canonical hash
// e78954445087f63064818ab60a2739b9a6b9bf0ae0147fbe92aac5ac76592103).
// Returns (companion, true) when addr is a canonical source OR canonical
// companion, else (0, false).
//
// Decision references: AD03, AD04 (frame-position disambiguation for 0xFF
// is the caller's responsibility; this function operates on already-
// disambiguated address bytes).
//
// Pre-A.7 implementation used math (+5/-5 with IsInitiatorCapableAddress
// nibble check). For the 25 canonical pairs the result is identical, but
// the table-backed lookup is the single authoritative source — it also
// exposes priority tier and free-use metadata via SlaveOfMaster /
// MasterOfSlave / MasterTier / IsFreeUseMaster.
func Companion(addr byte) (byte, bool) {
	if companion, ok := CompanionOfSource(addr); ok {
		return companion, true
	}
	if source, ok := SourceOfCompanion(addr); ok {
		return source, true
	}
	return 0, false
}
