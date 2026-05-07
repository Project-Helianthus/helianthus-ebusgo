package protocol

// AddressClass is the eBUS protocol-layer classification for one of the
// 256 byte values of an eBUS source/destination field, per the docs-owned
// taxonomy in
// helianthus-docs-ebus/architecture/ebus_standard/12-address-table.md
// (canonical-block hash
// 316baf20ab0d0a64b36613bb8c7604d7570fecc01071daca94931029ae82ebec).
//
// The enum has exactly 4 classes; tier/role labels (PC, free-use,
// recommended-for) belong to the upper layer (`SourceAddressTableRow`).
//
// Decision references: Phase C AD25 (4-class taxonomy + zero-value-fail-
// closed invariant), AD26 (validator contract).
type AddressClass uint8

const (
	// AddressClassReserved is the zero value. SYN (0xAA), ESCAPE (0xA9),
	// and any future reserved bytes per spec land here. Phase C
	// validator rejects M2S/M2M/M2BC frames whose src or dst falls in
	// Reserved. The zero value is Reserved (not Master) so an
	// uninitialized AddressClass cannot be confused with a valid
	// classification — fail-closed by construction.
	AddressClassReserved AddressClass = iota
	// AddressClassMaster covers the 25 canonical source addresses from
	// `sourceAddressTableV1` (the eBUS Spec_Prot_7 V1.6.1 Anhang lines
	// 34-58 source-capable rows).
	AddressClassMaster
	// AddressClassSlave covers the 228 byte values that are neither
	// Master nor Broadcast nor Reserved. This is the protocol-layer
	// classification used by the Phase C validator. The 203 Anhang-
	// enumerated Slave NNN rows (lines 75-304) plus the 25 reserved-
	// companion rows (companion of each canonical source) all share
	// this class for src/dst validation purposes.
	AddressClassSlave
	// AddressClassBroadcast is the single-byte 0xFE address per
	// Anhang line 305 + Spec_Prot_12 §4.3 lines 220-244.
	AddressClassBroadcast
)

// String returns a stable lower-case name for diagnostics, log lines,
// and JSON serialization. Stable across releases.
func (c AddressClass) String() string {
	switch c {
	case AddressClassMaster:
		return "master"
	case AddressClassSlave:
		return "slave"
	case AddressClassBroadcast:
		return "broadcast"
	case AddressClassReserved:
		return "reserved"
	default:
		return "reserved"
	}
}

// addressClassTable maps every byte value to its AddressClass at package
// init from sourceAddressTableV1 + the spec-defined broadcast/reserved
// constants. Lookup is O(1) at the cost of 256 bytes of memory.
var addressClassTable [256]AddressClass

func init() {
	// Default everything to Slave. Then override Master, Broadcast,
	// Reserved. This matches the spec's "implicit slave" rule:
	// every byte not Master/Broadcast/Reserved is Slave.
	for i := range addressClassTable {
		addressClassTable[i] = AddressClassSlave
	}
	for _, row := range sourceAddressTableV1 {
		addressClassTable[row.Source] = AddressClassMaster
	}
	addressClassTable[0xFE] = AddressClassBroadcast
	addressClassTable[0xAA] = AddressClassReserved // SYN
	addressClassTable[0xA9] = AddressClassReserved // ESCAPE
}

// AddressClassOf returns the AddressClass of an eBUS address byte.
// Always returns one of the 4 enum values (never panics). A byte that
// the spec doesn't explicitly classify falls into AddressClassSlave per
// the implicit-slave rule.
func AddressClassOf(addr byte) AddressClass {
	return addressClassTable[addr]
}
