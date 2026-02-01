package transport

import ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"

const (
	ensEscape    = byte(0xA9)
	ensSyn       = byte(0xAA)
	ensEscEscape = byte(0x00)
	ensEscSyn    = byte(0x01)
)

// EncodeENS encodes a byte slice using ENS escaping rules.
func EncodeENS(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}

	out := make([]byte, 0, len(data))
	for _, b := range data {
		switch b {
		case ensEscape:
			out = append(out, ensEscape, ensEscEscape)
		case ensSyn:
			out = append(out, ensEscape, ensEscSyn)
		default:
			out = append(out, b)
		}
	}
	return out
}

// DecodeENS decodes an ENS escaped byte slice.
func DecodeENS(data []byte) ([]byte, error) {
	var parser ENSParser
	out, err := parser.Parse(data)
	if err != nil {
		return nil, err
	}
	if err := parser.Finish(); err != nil {
		return nil, err
	}
	return out, nil
}

// ENSParser incrementally decodes ENS escaped streams.
type ENSParser struct {
	escape bool
}

// Reset clears the parser state.
func (p *ENSParser) Reset() {
	p.escape = false
}

// Finish validates that no pending escape remains.
func (p *ENSParser) Finish() error {
	if p.escape {
		p.escape = false
		return ebuserrors.ErrInvalidPayload
	}
	return nil
}

// Feed consumes one byte and returns a decoded byte when available.
func (p *ENSParser) Feed(b byte) (byte, bool, error) {
	if p.escape {
		p.escape = false
		switch b {
		case ensEscEscape:
			return ensEscape, true, nil
		case ensEscSyn:
			return ensSyn, true, nil
		default:
			return 0, false, ebuserrors.ErrInvalidPayload
		}
	}

	if b == ensEscape {
		p.escape = true
		return 0, false, nil
	}
	if b == ensSyn {
		return 0, false, ebuserrors.ErrInvalidPayload
	}
	return b, true, nil
}

// Parse consumes a byte slice and returns decoded bytes.
func (p *ENSParser) Parse(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}

	out := make([]byte, 0, len(data))
	for _, b := range data {
		value, ok, err := p.Feed(b)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, value)
		}
	}
	return out, nil
}
