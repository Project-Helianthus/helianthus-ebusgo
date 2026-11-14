package transport

import "reflect"

// CapabilityEvidence describes a capability claim, independently of transport kind.
// Only EvidenceProven is affirmative; a hypothesis is not qualification.
type CapabilityEvidence uint8

// Values above EvidenceProven are cleared to EvidenceUnknown by QueryCapabilities.
const (
	// EvidenceUnknown is the zero value: no claim is made.
	EvidenceUnknown CapabilityEvidence = iota
	// EvidenceHypothesis is a claim not yet proven. It is not affirmative:
	// DeadlineSuitable, for one, requires EvidenceProven.
	EvidenceHypothesis
	// EvidenceProven is the only affirmative claim.
	EvidenceProven
)

// BackendKind identifies a backend without inferring its capabilities.
// A local raw backend is distinct from ENS adapter framing.
type BackendKind uint8

// Values above KindEBUSDCommand are cleared to KindUnknown by
// QueryCapabilities. A kind is reported only by a CapabilityProvider;
// TCPPlainTransport, UDPPlainTransport and EbusdTCPTransport do not implement
// it, so QueryCapabilities returns KindUnknown for them. For the
// adapter protocols see
// https://github.com/Project-Helianthus/helianthus-docs-ebus/tree/main/protocols
const (
	// KindUnknown is the zero value: the backend is not identified.
	KindUnknown BackendKind = iota
	// KindLocalRaw labels a local raw backend, without adapter framing.
	KindLocalRaw
	// KindENH labels an adapter spoken to with ENH framing.
	KindENH
	// KindENS labels an adapter spoken to with ENS framing.
	KindENS
	// KindTCP labels a plain byte stream over TCP.
	KindTCP
	// KindUDP labels plain bytes over UDP datagrams.
	KindUDP
	// KindEBUSDCommand labels the ebusd command port.
	KindEBUSDCommand
)

// StreamEncoding describes the byte stream exposed to the caller, rather than
// host-to-adapter framing. Raw symbols and escape-decoded logical bytes differ.
type StreamEncoding uint8

// Values above EncodingLogical are cleared to EncodingUnknown by
// QueryCapabilities.
const (
	// EncodingUnknown is the zero value: the encoding is not claimed.
	EncodingUnknown StreamEncoding = iota
	// EncodingRaw labels a stream of raw bus symbols, not escape-decoded.
	EncodingRaw
	// EncodingLogical labels a stream of escape-decoded logical bytes.
	EncodingLogical
)

// Capabilities is an opt-in snapshot, not an opener or an arming switch.
// Existing transports make no claims unless they implement CapabilityProvider.
// Observations never imply qualified timing, and SendResponderBytes alone
// establishes neither autonomous prepared response nor deadline suitability.
type Capabilities struct {
	Kind                       BackendKind
	Encoding                   StreamEncoding
	PhysicalArbitration        CapabilityEvidence
	AutonomousPreparedResponse CapabilityEvidence
	ObservedEcho               CapabilityEvidence
	CaptureTimestamps          CapabilityEvidence
	QualifiedTiming            CapabilityEvidence
}

// CapabilityProvider supplies current claims without performing I/O.
// Implementations must return a coherent snapshot safe for concurrent queries.
// QualifiedTiming must refer to retained qualification for the backend's
// current operating conditions; this seam does not perform that qualification.
type CapabilityProvider interface {
	TransportCapabilities() Capabilities
}

// QueryCapabilities returns Unknown for absent, nil, or invalid claims.
// Invalid fields are cleared independently, preserving other valid evidence.
// No existing transport interface, name, or framing selector implies a claim.
func QueryCapabilities(backend any) Capabilities {
	provider, ok := backend.(CapabilityProvider)
	if !ok {
		return Capabilities{}
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return Capabilities{}
		}
	}
	result := provider.TransportCapabilities()
	if result.Kind > KindEBUSDCommand {
		result.Kind = KindUnknown
	}
	if result.Encoding > EncodingLogical {
		result.Encoding = EncodingUnknown
	}
	for _, claim := range []*CapabilityEvidence{
		&result.PhysicalArbitration,
		&result.AutonomousPreparedResponse,
		&result.ObservedEcho,
		&result.CaptureTimestamps,
		&result.QualifiedTiming,
	} {
		if *claim > EvidenceProven {
			*claim = EvidenceUnknown
		}
	}
	return result
}

// DeadlineSuitable reports explicit autonomous-response timing claims only.
// Arbitration, echo and timestamp observations remain independent. This is
// descriptive metadata, not permission to transmit or physical qualification.
func (capabilities Capabilities) DeadlineSuitable() bool {
	return capabilities.Kind >= KindLocalRaw &&
		capabilities.Kind <= KindEBUSDCommand &&
		(capabilities.Encoding == EncodingRaw || capabilities.Encoding == EncodingLogical) &&
		capabilities.AutonomousPreparedResponse == EvidenceProven &&
		capabilities.QualifiedTiming == EvidenceProven
}
