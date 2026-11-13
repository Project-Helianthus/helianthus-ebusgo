package transport

import aa22reflect "reflect"

// AA22Evidence describes a capability claim, independently of transport kind.
// Only AA22Proven is affirmative; a hypothesis is not qualification.
type AA22Evidence uint8

// AA22QueryCapabilities clears any value above AA22Proven to AA22Unknown.
const (
	// AA22Unknown is the zero value: no claim is made.
	AA22Unknown AA22Evidence = iota
	// AA22Hypothesis is a claim not yet proven. It is not affirmative:
	// AA22DeadlineSuitable, for one, requires AA22Proven.
	AA22Hypothesis
	// AA22Proven is the only affirmative claim.
	AA22Proven
)

// AA22TransportKind identifies a backend without inferring its capabilities.
// A local raw backend is distinct from ENS adapter framing.
type AA22TransportKind uint8

// AA22QueryCapabilities clears any value above AA22KindEBUSDCommand to
// AA22KindUnknown. The adapter protocols are described in
// https://github.com/Project-Helianthus/helianthus-docs-ebus/tree/main/protocols
const (
	// AA22KindUnknown is the zero value: the backend is not identified.
	AA22KindUnknown AA22TransportKind = iota
	// AA22KindLocalRaw is a backend attached to the bus on this host,
	// without adapter framing.
	AA22KindLocalRaw
	// AA22KindENH is an adapter spoken to with ENH framing.
	AA22KindENH
	// AA22KindENS is an adapter spoken to with ENS framing.
	AA22KindENS
	// AA22KindTCP is a plain byte stream over TCP, the kind
	// TCPPlainTransport implements.
	AA22KindTCP
	// AA22KindUDP is plain bytes over UDP datagrams, the kind
	// UDPPlainTransport implements.
	AA22KindUDP
	// AA22KindEBUSDCommand is the ebusd command port, the kind
	// EbusdTCPTransport implements.
	AA22KindEBUSDCommand
)

// AA22Encoding describes the byte stream exposed to the caller, rather than
// host-to-adapter framing. Raw symbols and escape-decoded logical bytes differ.
type AA22Encoding uint8

// AA22QueryCapabilities clears any value above AA22EncodingLogical to
// AA22EncodingUnknown.
const (
	// AA22EncodingUnknown is the zero value: the encoding is not claimed.
	AA22EncodingUnknown AA22Encoding = iota
	// AA22EncodingRaw delivers wire symbols; escape pairs are still encoded.
	AA22EncodingRaw
	// AA22EncodingLogical delivers escape-decoded logical bytes.
	AA22EncodingLogical
)

// AA22Capabilities is an opt-in snapshot, not an opener or an arming switch.
// Existing transports make no claims unless they implement AA22CapabilityProvider.
// Observations never imply qualified timing, and SendResponderBytes alone
// establishes neither autonomous prepared response nor deadline suitability.
type AA22Capabilities struct {
	AA22Kind                       AA22TransportKind
	AA22Encoding                   AA22Encoding
	AA22PhysicalArbitration        AA22Evidence
	AA22AutonomousPreparedResponse AA22Evidence
	AA22ObservedEcho               AA22Evidence
	AA22CaptureTimestamps          AA22Evidence
	AA22QualifiedTiming            AA22Evidence
}

// AA22CapabilityProvider supplies current claims without performing I/O.
// Implementations must return a coherent snapshot safe for concurrent queries.
// AA22QualifiedTiming must refer to retained qualification for the backend's
// current operating conditions; this seam does not perform that qualification.
type AA22CapabilityProvider interface {
	AA22TransportCapabilities() AA22Capabilities
}

// AA22QueryCapabilities returns Unknown for absent, nil, or invalid claims.
// Invalid fields are cleared independently, preserving other valid evidence.
// No existing transport interface, name, or framing selector implies a claim.
func AA22QueryCapabilities(aa22Transport any) AA22Capabilities {
	aa22Provider, aa22OK := aa22Transport.(AA22CapabilityProvider)
	if !aa22OK {
		return AA22Capabilities{}
	}
	aa22Value := aa22reflect.ValueOf(aa22Provider)
	switch aa22Value.Kind() {
	case aa22reflect.Chan, aa22reflect.Func, aa22reflect.Interface, aa22reflect.Map, aa22reflect.Pointer, aa22reflect.Slice:
		if aa22Value.IsNil() {
			return AA22Capabilities{}
		}
	}
	aa22Result := aa22Provider.AA22TransportCapabilities()
	if aa22Result.AA22Kind > AA22KindEBUSDCommand {
		aa22Result.AA22Kind = AA22KindUnknown
	}
	if aa22Result.AA22Encoding > AA22EncodingLogical {
		aa22Result.AA22Encoding = AA22EncodingUnknown
	}
	for _, aa22Claim := range []*AA22Evidence{
		&aa22Result.AA22PhysicalArbitration,
		&aa22Result.AA22AutonomousPreparedResponse,
		&aa22Result.AA22ObservedEcho,
		&aa22Result.AA22CaptureTimestamps,
		&aa22Result.AA22QualifiedTiming,
	} {
		if *aa22Claim > AA22Proven {
			*aa22Claim = AA22Unknown
		}
	}
	return aa22Result
}

// AA22DeadlineSuitable reports explicit autonomous-response timing claims only.
// Arbitration, echo and timestamp observations remain independent. This is
// descriptive metadata, not permission to transmit or physical qualification.
func (aa22Capabilities AA22Capabilities) AA22DeadlineSuitable() bool {
	return aa22Capabilities.AA22Kind >= AA22KindLocalRaw &&
		aa22Capabilities.AA22Kind <= AA22KindEBUSDCommand &&
		(aa22Capabilities.AA22Encoding == AA22EncodingRaw || aa22Capabilities.AA22Encoding == AA22EncodingLogical) &&
		aa22Capabilities.AA22AutonomousPreparedResponse == AA22Proven &&
		aa22Capabilities.AA22QualifiedTiming == AA22Proven
}
