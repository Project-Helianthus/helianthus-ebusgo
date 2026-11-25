package engine

import "github.com/Project-Helianthus/helianthus-ebusgo/protocol"

func parseFrame(raw []byte) (protocol.Frame, bool) {
	if len(raw) < 6 {
		return protocol.Frame{}, false
	}
	length := int(raw[4])
	expected := 6 + length
	if len(raw) != expected {
		return protocol.Frame{}, false
	}
	crc := protocol.CRC(raw[:len(raw)-1])
	if crc != raw[len(raw)-1] {
		return protocol.Frame{}, false
	}
	data := append([]byte(nil), raw[5:5+length]...)
	return protocol.Frame{
		Source:    raw[0],
		Target:    raw[1],
		Primary:   raw[2],
		Secondary: raw[3],
		Data:      data,
	}, true
}
