package protocol

type ACKPosition string

const (
	ACKPositionRequestACK ACKPosition = "request_ack"
)

type ACKCorrelator string

const (
	ACKCorrelatorM2A ACKCorrelator = "M2A"
)

type ACKCorrelation struct {
	Byte            byte
	Position        ACKPosition
	CompleteRequest bool
	Correlator      ACKCorrelator
}
