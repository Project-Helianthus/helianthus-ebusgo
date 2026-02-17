//go:build !tinygo

package transport

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/internal/crc"
)

const (
	ebusSymbolEscape = byte(0xA9)
	ebusSymbolSyn    = byte(0xAA)
	ebusSymbolAck    = byte(0x00)
	ebusBroadcast    = byte(0xFE)
	ebusdDrainWindow = 10 * time.Millisecond
)

var errNoHexPayload = errors.New("ebusd: no hex payload")

// EbusdTCPTransport provides RawTransport over the ebusd TCP command port.
// It sends "hex" commands on end-of-message and feeds echoed/response bytes
// back to the bus in raw (escaped) form.
type EbusdTCPTransport struct {
	conn   net.Conn
	reader *bufio.Reader

	mu         sync.Mutex
	cond       *sync.Cond
	closed     bool
	pending    []byte
	pendingErr error

	readTimeout  time.Duration
	writeTimeout time.Duration

	ignoreUntilSyn bool

	escape   bool
	telegram []byte
}

// NewEbusdTCPTransport wraps an established ebusd command connection.
func NewEbusdTCPTransport(conn net.Conn, readTimeout, writeTimeout time.Duration) *EbusdTCPTransport {
	tr := &EbusdTCPTransport{
		conn:         conn,
		reader:       bufio.NewReader(conn),
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
	}
	tr.cond = sync.NewCond(&tr.mu)
	return tr
}

func (t *EbusdTCPTransport) ReadByte() (byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for len(t.pending) == 0 && t.pendingErr == nil && !t.closed {
		t.cond.Wait()
	}
	if len(t.pending) == 0 {
		if t.pendingErr != nil {
			err := t.pendingErr
			t.pendingErr = nil
			return 0, err
		}
		if t.closed {
			return 0, ebuserrors.ErrTransportClosed
		}
		return 0, ebuserrors.ErrTimeout
	}
	value := t.pending[0]
	t.pending = t.pending[1:]
	return value, nil
}

func (t *EbusdTCPTransport) Write(payload []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return 0, ebuserrors.ErrTransportClosed
	}
	if len(payload) == 0 {
		return 0, nil
	}

	for _, raw := range payload {
		t.pending = append(t.pending, raw)
		if err := t.consumeRawByte(raw); err != nil {
			t.pendingErr = err
			t.cond.Broadcast()
			return len(payload), err
		}
	}
	t.cond.Broadcast()
	return len(payload), nil
}

func (t *EbusdTCPTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true
	if t.conn != nil {
		_ = t.conn.Close()
	}
	t.cond.Broadcast()
	return nil
}

func (t *EbusdTCPTransport) consumeRawByte(raw byte) error {
	if t.ignoreUntilSyn {
		switch {
		case t.escape:
			t.escape = false
		case raw == ebusSymbolEscape:
			t.escape = true
		case raw == ebusSymbolSyn:
			t.ignoreUntilSyn = false
			t.escape = false
			t.telegram = t.telegram[:0]
		}
		return nil
	}

	if t.escape {
		t.escape = false
		switch raw {
		case 0x00:
			t.telegram = append(t.telegram, ebusSymbolEscape)
		case 0x01:
			t.telegram = append(t.telegram, ebusSymbolSyn)
		default:
			t.telegram = t.telegram[:0]
			return ebuserrors.ErrInvalidPayload
		}
		return t.maybeDispatchTelegram()
	}

	if raw == ebusSymbolEscape {
		t.escape = true
		return nil
	}
	if raw == ebusSymbolSyn {
		// End-of-message boundary (sent by the bus state machine after a full transaction).
		t.telegram = t.telegram[:0]
		return nil
	}

	t.telegram = append(t.telegram, raw)
	return t.maybeDispatchTelegram()
}

func (t *EbusdTCPTransport) maybeDispatchTelegram() error {
	if len(t.telegram) == 0 {
		return nil
	}

	if len(t.telegram) < 5 {
		return nil
	}

	dataLen := int(t.telegram[4])
	expected := 6 + dataLen
	if len(t.telegram) < expected {
		return nil
	}
	if len(t.telegram) != expected {
		t.telegram = t.telegram[:0]
		return ebuserrors.ErrInvalidPayload
	}

	if len(t.telegram) < 6 {
		t.telegram = t.telegram[:0]
		return ebuserrors.ErrInvalidPayload
	}

	if crcValue(t.telegram[:len(t.telegram)-1]) != t.telegram[len(t.telegram)-1] {
		t.telegram = t.telegram[:0]
		return ebuserrors.ErrCRCMismatch
	}

	src := t.telegram[0]
	dst := t.telegram[1]
	pb := t.telegram[2]
	sb := t.telegram[3]
	payload := append([]byte(nil), t.telegram[1:len(t.telegram)-1]...)
	t.telegram = t.telegram[:0]

	respPayload, err := t.sendHexCommand(src, payload)
	if err != nil {
		if errors.Is(err, errNoHexPayload) && dst == ebusBroadcast {
			return nil
		}
		if errors.Is(err, ebuserrors.ErrTransportClosed) {
			t.closed = true
			t.pendingErr = err
			t.cond.Broadcast()
			return err
		}
		t.pendingErr = err
		t.cond.Broadcast()
		return nil
	}

	if dst == ebusBroadcast {
		return nil
	}

	t.pending = append(t.pending, ebusSymbolAck)
	if isInitiatorCapableAddress(dst) {
		t.ignoreUntilSyn = true
		t.cond.Broadcast()
		return nil
	}

	response := make([]byte, 0, 6+len(respPayload))
	response = append(response, dst, src, pb, sb, byte(len(respPayload)))
	response = append(response, respPayload...)
	response = append(response, crcValue(response))

	t.pending = append(t.pending, escapeBytes(response)...)
	t.cond.Broadcast()
	t.ignoreUntilSyn = true
	return nil
}

func (t *EbusdTCPTransport) sendHexCommand(src byte, payload []byte) ([]byte, error) {
	if t.closed {
		return nil, ebuserrors.ErrTransportClosed
	}

	command := fmt.Sprintf("hex -s %02X %s\n", src, hex.EncodeToString(payload))
	if t.conn != nil && t.writeTimeout > 0 {
		_ = t.conn.SetWriteDeadline(time.Now().Add(t.writeTimeout))
	}
	if _, err := io.WriteString(t.conn, command); err != nil {
		return nil, ebuserrors.ErrTransportClosed
	}
	if t.conn != nil {
		_ = t.conn.SetWriteDeadline(time.Time{})
	}

	if t.conn != nil && t.readTimeout > 0 {
		_ = t.conn.SetReadDeadline(time.Now().Add(t.readTimeout))
	}
	lines, err := readResponseLines(t.conn, t.reader)
	if err != nil {
		if t.conn != nil {
			_ = t.conn.SetReadDeadline(time.Time{})
		}
		return nil, err
	}
	if t.conn != nil {
		_ = t.conn.SetReadDeadline(time.Time{})
	}
	resp, err := parseHexResponseLines(lines)
	if err != nil {
		return nil, err
	}
	return stripLengthPrefix(resp), nil
}

func readResponseLines(conn net.Conn, reader *bufio.Reader) ([]string, error) {
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			if isTimeout(err) {
				return lines, ebuserrors.ErrTimeout
			}
			if isClosed(err) {
				return lines, ebuserrors.ErrTransportClosed
			}
			return lines, ebuserrors.ErrTransportClosed
		}

		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			if len(lines) > 0 {
				break
			}
			if errors.Is(err, io.EOF) {
				return lines, ebuserrors.ErrTransportClosed
			}
			continue
		}
		lines = append(lines, line)
		if errors.Is(err, io.EOF) {
			return lines, nil
		}
		break
	}

	if len(lines) == 0 {
		return lines, ebuserrors.ErrTimeout
	}

	if conn != nil {
		_ = conn.SetReadDeadline(time.Now().Add(ebusdDrainWindow))
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if isTimeout(err) || errors.Is(err, io.EOF) || isClosed(err) {
				break
			}
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if conn != nil {
		_ = conn.SetReadDeadline(time.Time{})
	}

	return lines, nil
}

func parseHexResponseLines(lines []string) ([]byte, error) {
	sawDone := false
	sawTimeout := false
	sawCollision := false
	sawErr := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "done") {
			sawDone = true
			continue
		}
		if strings.HasPrefix(lower, "err") {
			sawErr = true
			if strings.Contains(lower, "arbitration lost") ||
				strings.Contains(lower, "arbitration") && strings.Contains(lower, "lost") ||
				strings.Contains(lower, "collision") {
				sawCollision = true
				continue
			}
			if strings.Contains(lower, "timeout") ||
				strings.Contains(lower, "timed out") ||
				strings.Contains(lower, "no answer") {
				sawTimeout = true
			}
			if strings.Contains(lower, "no signal") ||
				strings.Contains(lower, "signal lost") ||
				strings.Contains(lower, "device invalid") ||
				strings.Contains(lower, "generic i/o error") {
				sawTimeout = true
			}
			continue
		}
		if strings.HasPrefix(lower, "usage:") {
			sawErr = true
			continue
		}
		if strings.HasPrefix(lower, "0x") {
			trimmed = strings.TrimSpace(trimmed[2:])
		}

		hexText := stripHexSpaces(trimmed)
		isHex := true
		for _, r := range hexText {
			switch {
			case r >= '0' && r <= '9':
			case r >= 'a' && r <= 'f':
			case r >= 'A' && r <= 'F':
			default:
				isHex = false
			}
			if !isHex {
				break
			}
		}
		if !isHex || hexText == "" {
			continue
		}
		if len(hexText)%2 != 0 {
			sawErr = true
			continue
		}
		payload, err := hex.DecodeString(hexText)
		if err != nil {
			sawErr = true
			continue
		}
		return payload, nil
	}
	if sawDone {
		return nil, errNoHexPayload
	}
	if sawTimeout {
		return nil, ebuserrors.ErrTimeout
	}
	if sawCollision {
		return nil, ebuserrors.ErrBusCollision
	}
	if sawErr {
		return nil, ebuserrors.ErrInvalidPayload
	}
	return nil, ebuserrors.ErrTimeout
}

func stripHexSpaces(value string) string {
	return strings.NewReplacer(" ", "", "\t", "").Replace(value)
}

func stripLengthPrefix(payload []byte) []byte {
	if len(payload) < 2 {
		return payload
	}
	if int(payload[0]) == len(payload)-1 {
		return payload[1:]
	}
	return payload
}

func escapeBytes(payload []byte) []byte {
	out := make([]byte, 0, len(payload)*2)
	for _, value := range payload {
		switch value {
		case ebusSymbolEscape:
			out = append(out, ebusSymbolEscape, 0x00)
		case ebusSymbolSyn:
			out = append(out, ebusSymbolEscape, 0x01)
		default:
			out = append(out, value)
		}
	}
	return out
}

func crcValue(payload []byte) byte {
	value := byte(0)
	for _, b := range payload {
		switch b {
		case ebusSymbolEscape:
			value = crc.Update(value, ebusSymbolEscape)
			value = crc.Update(value, 0x00)
		case ebusSymbolSyn:
			value = crc.Update(value, ebusSymbolEscape)
			value = crc.Update(value, 0x01)
		default:
			value = crc.Update(value, b)
		}
	}
	return value
}

func isInitiatorCapableAddress(addr byte) bool {
	return initiatorPartIndex(addr&0x0F) > 0 && initiatorPartIndex((addr&0xF0)>>4) > 0
}

func initiatorPartIndex(bits byte) byte {
	switch bits {
	case 0x0:
		return 1
	case 0x1:
		return 2
	case 0x3:
		return 3
	case 0x7:
		return 4
	case 0xF:
		return 5
	default:
		return 0
	}
}

var _ RawTransport = (*EbusdTCPTransport)(nil)
