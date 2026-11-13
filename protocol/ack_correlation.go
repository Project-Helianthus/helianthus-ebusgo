package protocol

// ACKPosition is a string label for the position of an acknowledgement
// within a transaction.
type ACKPosition string

const (
	// ACKPositionRequestACK is the only position label this package
	// defines.
	ACKPositionRequestACK ACKPosition = "request_ack"
)

// ACKCorrelator is a string label for how an acknowledgement was correlated.
type ACKCorrelator string

const (
	// ACKCorrelatorM2A is the only correlator label this package defines.
	ACKCorrelatorM2A ACKCorrelator = "M2A"
)

// ACKCorrelation holds an acknowledgement byte, its position label, a
// CompleteRequest flag and a correlator label. No code in this module
// produces or consumes it. The zero value is not distinguishable by Byte
// alone: Byte 0x00 equals SymbolAck.
type ACKCorrelation struct {
	Byte            byte
	Position        ACKPosition
	CompleteRequest bool
	Correlator      ACKCorrelator
}
