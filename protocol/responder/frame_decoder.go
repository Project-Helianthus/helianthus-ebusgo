// M4c1 PR-B: inbound frame decoder for the responder lane.
//
// Parses an eBUS initiator-to-responder frame:
//
//	QQ ZZ PB SB NN DB1..DBn CRC
//
// All bytes are expected to be already unescaped (the caller is responsible
// for the wire-level 0xA9 / 0xAA de-escape, per the established transport
// discipline in `protocol/bus.go`). The decoder validates the CRC across
// the header+payload span and returns a `DecodedFrame` on success, or an
// error classifying the failure (short frame, length mismatch, CRC bad).

package responder

import (
	"errors"
	"reflect"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// DecodedFrame is the parsed, CRC-validated form of an inbound initiator
// -to-responder eBUS frame.
type DecodedFrame struct {
	// Source (QQ) is the initiator address.
	Source byte
	// Target (ZZ) is the destination address; matched against the
	// configured local responder address by LocalResponderDispatcher.
	Target byte
	// Primary (PB) and Secondary (SB) are the eBUS service-bank opcodes.
	Primary   byte
	Secondary byte
	// Payload is the DB1..DBn bytes (length == NN).
	Payload []byte
	// CRC is the validated frame CRC byte.
	CRC byte
}

// FrameDecoder parses inbound responder-direction frames. It is stateless
// and safe for concurrent use.
type FrameDecoder struct{}

// NewFrameDecoder constructs a FrameDecoder.
func NewFrameDecoder() *FrameDecoder {
	return &FrameDecoder{}
}

// Errors returned by FrameDecoder.Decode.
var (
	// ErrFrameTooShort is returned when the input is shorter than the
	// minimum possible frame (QQ ZZ PB SB NN CRC == 6 bytes).
	ErrFrameTooShort = errors.New("responder: frame too short")
	// ErrLengthMismatch is returned when NN declares a payload size that
	// does not fit in the supplied byte slice.
	ErrLengthMismatch = errors.New("responder: NN length mismatch")
	// ErrBadCRC is returned when the CRC byte does not match the
	// computed CRC over QQ..DBn.
	ErrBadCRC = errors.New("responder: bad CRC")
)

// Decode parses a single unescaped frame. `frame` must be exactly
// `5 + NN + 1` bytes (header + payload + CRC).
func (d *FrameDecoder) Decode(frame []byte) (DecodedFrame, error) {
	if len(frame) < 6 {
		return DecodedFrame{}, ErrFrameTooShort
	}
	nn := int(frame[4])
	want := 5 + nn + 1
	if len(frame) != want {
		return DecodedFrame{}, ErrLengthMismatch
	}
	crcByte := frame[want-1]
	body := frame[:want-1]
	if protocol.CRC(body) != crcByte {
		return DecodedFrame{}, ErrBadCRC
	}
	payload := make([]byte, nn)
	copy(payload, frame[5:5+nn])
	return DecodedFrame{
		Source:    frame[0],
		Target:    frame[1],
		Primary:   frame[2],
		Secondary: frame[3],
		Payload:   payload,
		CRC:       crcByte,
	}, nil
}

func init() {
	registerExport("FrameDecoder", reflect.TypeOf((*FrameDecoder)(nil)))
}
