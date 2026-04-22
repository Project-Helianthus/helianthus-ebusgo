package b503

// InvokeSafety classifies the gateway invoke boundary behaviour required for
// a given B503 (family, selector) tuple. See spec §4 and plan AD04.
type InvokeSafety int

const (
	// SafetyRead is passive-safe, idempotent; no session state required.
	// Gateway MAY invoke directly via ebus.v1.rpc.invoke.
	SafetyRead InvokeSafety = iota

	// SafetyServiceWrite is side-effectful and stateful; gateway MUST gate
	// via the live-monitor session FSM (spec §6).
	SafetyServiceWrite

	// SafetyInstallWrite requires installer authority. MUST NOT be exposed
	// on any public surface in v1 (spec §9, plan AD02). Classified here only
	// so gateway/invoke layers can reject such tuples defensively.
	SafetyInstallWrite
)

// String implements fmt.Stringer.
func (s InvokeSafety) String() string {
	switch s {
	case SafetyRead:
		return "READ"
	case SafetyServiceWrite:
		return "SERVICE_WRITE"
	case SafetyInstallWrite:
		return "INSTALL_WRITE"
	default:
		return "UNKNOWN"
	}
}

// selectorKey packs (family, selector) into a single uint16 for map lookup.
func selectorKey(family, selector byte) uint16 {
	return uint16(family)<<8 | uint16(selector)
}

// catalog enumerates the 7 locked v1 (family, selector) tuples and their
// invoke-safety classes. Derived from spec §3 / plan AD01..AD04.
var catalog = map[uint16]InvokeSafety{
	selectorKey(0x00, 0x01): SafetyRead,         // Currenterror
	selectorKey(0x01, 0x01): SafetyRead,         // Errorhistory
	selectorKey(0x02, 0x01): SafetyInstallWrite, // Clearerrorhistory
	selectorKey(0x00, 0x02): SafetyRead,         // Currentservice
	selectorKey(0x01, 0x02): SafetyRead,         // Servicehistory
	selectorKey(0x02, 0x02): SafetyInstallWrite, // Clearservicehistory
	selectorKey(0x00, 0x03): SafetyServiceWrite, // LiveMonitorMain
}

// Safety classifies (family, selector) per spec §3 / §4.
//
// Returns (class, true) for any tuple enumerated in the spec §3 catalog.
// Returns (_, false) for unknown tuples — callers MUST treat an unknown
// tuple as not-invokable and NOT attempt a default classification.
func Safety(family, selector byte) (InvokeSafety, bool) {
	s, ok := catalog[selectorKey(family, selector)]
	return s, ok
}
