package protocol

// EscapeBytes escapes eBUS control symbols for wire transmission.
func EscapeBytes(raw []byte) []byte {
	escaped := make([]byte, 0, len(raw))
	for _, b := range raw {
		switch b {
		case SymbolEscape:
			escaped = append(escaped, SymbolEscape, 0x00)
		case SymbolSyn:
			escaped = append(escaped, SymbolEscape, 0x01)
		default:
			escaped = append(escaped, b)
		}
	}
	return escaped
}

// EncodeSlaveResponse builds a wire-ready eBUS response segment.
func EncodeSlaveResponse(data []byte) []byte {
	segment := make([]byte, 0, len(data)+2)
	segment = append(segment, byte(len(data)))
	segment = append(segment, data...)
	segment = append(segment, CRC(segment))

	return EscapeBytes(segment)
}
