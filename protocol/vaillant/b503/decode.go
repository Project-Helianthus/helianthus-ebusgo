package b503

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// EmptySlot is the per-slot sentinel for the five-slot Currenterror /
// Currentservice / history payloads (spec §5.1). A slot value of 0xFFFF
// denotes "no error / no service message occupying that position".
const EmptySlot uint16 = 0xFFFF

// slotCount is the fixed number of uint16 slots in every error / service
// five-slot payload (spec §3 / §5).
const slotCount = 5

// slotPayloadLen is the byte length of the raw slot array (5 × uint16 LE).
const slotPayloadLen = slotCount * 2

// ErrShortPayload is returned by decoders when the payload is shorter than
// the minimum required for the selector.
var ErrShortPayload = errors.New("b503: payload shorter than required for selector")

// ErrorSlots is the decoded five-slot composite payload returned by the
// Currenterror (00 01) and Currentservice (00 02) selectors.
//
// Each slot carries the raw LE uint16 reported by the device, or EmptySlot
// (0xFFFF) when the slot is empty. Per AD05 / spec §10, no F.xxx translation
// is applied — callers receive the decimal as-is.
type ErrorSlots struct {
	Slots [slotCount]uint16
}

// FirstActive returns the first non-EmptySlot value, scanning from index 0.
// present is false iff all five slots equal EmptySlot. Definition per spec
// §5.2.
func (e ErrorSlots) FirstActive() (value uint16, present bool) {
	for _, s := range e.Slots {
		if s != EmptySlot {
			return s, true
		}
	}
	return 0, false
}

// ErrorHistoryRecord is the decoded payload for the Errorhistory (01 01)
// and Servicehistory (01 02) selectors. Index is the history-entry index
// echoed by the device; Slots carries the same five-slot shape as
// ErrorSlots.
type ErrorHistoryRecord struct {
	Index uint8
	Slots [slotCount]uint16
}

// FirstActive mirrors ErrorSlots.FirstActive over the history record's
// slot array.
func (r ErrorHistoryRecord) FirstActive() (value uint16, present bool) {
	for _, s := range r.Slots {
		if s != EmptySlot {
			return s, true
		}
	}
	return 0, false
}

// LiveMonitorStatus is the decoded payload for the HMU LiveMonitorMain
// (00 03) selector. Status and Function are the first two payload bytes;
// Reserved carries any trailing bytes verbatim (spec §3).
type LiveMonitorStatus struct {
	Status   byte
	Function byte
	Reserved []byte
}

// decodeSlots parses a contiguous 10-byte payload (5 × uint16 LE) into an
// ErrorSlots value. The caller is responsible for verifying payload length
// before invoking.
func decodeSlots(payload []byte) [slotCount]uint16 {
	var out [slotCount]uint16
	for i := 0; i < slotCount; i++ {
		out[i] = binary.LittleEndian.Uint16(payload[i*2 : i*2+2])
	}
	return out
}

// DecodeCurrentError decodes the response payload for selector 00 01
// (Currenterror, spec §3 / §5). payload is the raw five-slot byte block
// (10 bytes), i.e. the response body AFTER the leading length byte has been
// stripped by the transport layer.
func DecodeCurrentError(payload []byte) (ErrorSlots, error) {
	if len(payload) < slotPayloadLen {
		return ErrorSlots{}, fmt.Errorf("%w: Currenterror wants %d bytes, got %d", ErrShortPayload, slotPayloadLen, len(payload))
	}
	return ErrorSlots{Slots: decodeSlots(payload[:slotPayloadLen])}, nil
}

// DecodeCurrentService decodes the response payload for selector 00 02
// (Currentservice, spec §3 / §5). Wire shape is identical to Currenterror.
func DecodeCurrentService(payload []byte) (ErrorSlots, error) {
	if len(payload) < slotPayloadLen {
		return ErrorSlots{}, fmt.Errorf("%w: Currentservice wants %d bytes, got %d", ErrShortPayload, slotPayloadLen, len(payload))
	}
	return ErrorSlots{Slots: decodeSlots(payload[:slotPayloadLen])}, nil
}

// DecodeErrorHistory decodes the response payload for selector 01 01
// (Errorhistory, spec §3). The first byte is the echoed history index,
// followed by the five LE uint16 slots.
func DecodeErrorHistory(payload []byte) (ErrorHistoryRecord, error) {
	if len(payload) < 1+slotPayloadLen {
		return ErrorHistoryRecord{}, fmt.Errorf("%w: Errorhistory wants %d bytes, got %d", ErrShortPayload, 1+slotPayloadLen, len(payload))
	}
	return ErrorHistoryRecord{
		Index: payload[0],
		Slots: decodeSlots(payload[1 : 1+slotPayloadLen]),
	}, nil
}

// DecodeServiceHistory decodes the response payload for selector 01 02
// (Servicehistory, spec §3). Wire shape is identical to Errorhistory.
func DecodeServiceHistory(payload []byte) (ErrorHistoryRecord, error) {
	if len(payload) < 1+slotPayloadLen {
		return ErrorHistoryRecord{}, fmt.Errorf("%w: Servicehistory wants %d bytes, got %d", ErrShortPayload, 1+slotPayloadLen, len(payload))
	}
	return ErrorHistoryRecord{
		Index: payload[0],
		Slots: decodeSlots(payload[1 : 1+slotPayloadLen]),
	}, nil
}

// DecodeLiveMonitorMain decodes the response payload for selector 00 03
// (HMU LiveMonitorMain, spec §1 / §3). Status and Function are the first
// two bytes; Reserved is a defensive copy of the trailing bytes.
func DecodeLiveMonitorMain(payload []byte) (LiveMonitorStatus, error) {
	if len(payload) < 2 {
		return LiveMonitorStatus{}, fmt.Errorf("%w: LiveMonitorMain wants >=2 bytes, got %d", ErrShortPayload, len(payload))
	}
	reserved := make([]byte, len(payload)-2)
	copy(reserved, payload[2:])
	return LiveMonitorStatus{
		Status:   payload[0],
		Function: payload[1],
		Reserved: reserved,
	}, nil
}
