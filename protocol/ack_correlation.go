package protocol

// ACKPosition names where in a transaction an acknowledgement symbol was
// observed.
type ACKPosition string

const (
	// ACKPositionRequestACK is the acknowledgement that answers the
	// initiator's request, as opposed to one sent for a response.
	ACKPositionRequestACK ACKPosition = "request_ack"
)

// ACKCorrelator names the rule that tied an acknowledgement to its request.
type ACKCorrelator string

const (
	// ACKCorrelatorM2A tags a correlation whose acknowledgement symbol was
	// matched to the complete request immediately before it.
	ACKCorrelatorM2A ACKCorrelator = "M2A"
)

// ACKCorrelation records which acknowledgement symbol was observed for a
// request, where it was observed and by which correlation rule. The zero value
// means no acknowledgement was correlated.
//
// Byte holds the observed symbol as received (compare it with SymbolAck and
// SymbolNack). CompleteRequest reports whether the producer saw the whole
// request before the symbol. The protocol package defines this type for
// consumers that reconstruct transactions; it does not populate it itself.
type ACKCorrelation struct {
	Byte            byte
	Position        ACKPosition
	CompleteRequest bool
	Correlator      ACKCorrelator
}
