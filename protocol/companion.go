package protocol

// Companion returns the eBUS companion address (master/slave pair) for addr,
// per the eBUS standard (master+5 / slave-5) with bit-pattern validity check.
// Returns (companion, true) when a valid pair exists, else (0, false).
//
// Decision references: AD03, AD04 (frame-position disambiguation for 0xFF
// is the caller's responsibility; this function operates on already-
// disambiguated address bytes).
func Companion(addr byte) (byte, bool) {
	if IsInitiatorCapableAddress(addr) {
		return addr + 0x05, true
	}
	candidate := byte((int(addr) - 0x05) & 0xFF)
	if IsInitiatorCapableAddress(candidate) {
		return candidate, true
	}
	return 0, false
}
