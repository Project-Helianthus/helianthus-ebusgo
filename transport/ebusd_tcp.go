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
	ebusSymbolEscape    = byte(0xA9)
	ebusSymbolSyn       = byte(0xAA)
	ebusSymbolAck       = byte(0x00)
	ebusBroadcast       = byte(0xFE)
	ebusdFollowupWindow = 150 * time.Millisecond
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
	writeMu    sync.Mutex
	closed     bool
	pending    []byte
	pendingErr error

	readTimeout  time.Duration
	writeTimeout time.Duration

	ignoreUntilSyn bool

	escape   bool
	telegram []byte
}

type ebusdHexDispatch struct {
	src     byte
	dst     byte
	pb      byte
	sb      byte
	payload []byte
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
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()
		return 0, ebuserrors.ErrTransportClosed
	}
	if len(payload) == 0 {
		t.mu.Unlock()
		return 0, nil
	}

	for _, raw := range payload {
		t.pending = append(t.pending, raw)
		dispatch, err := t.consumeRawByte(raw)
		if err != nil {
			t.pendingErr = err
			t.cond.Broadcast()
			t.mu.Unlock()
			return len(payload), err
		}
		if dispatch == nil {
			continue
		}

		// Allow the bus reader to drain the echoed request bytes while we block on ebusd.
		t.cond.Broadcast()
		t.mu.Unlock()

		respPayload, sendErr := t.sendHexCommand(dispatch.src, dispatch.payload)

		t.mu.Lock()
		if sendErr != nil {
			if errors.Is(sendErr, errNoHexPayload) && dispatch.dst == ebusBroadcast {
				continue
			}
			if errors.Is(sendErr, ebuserrors.ErrTransportClosed) {
				t.closed = true
				t.pendingErr = sendErr
				t.cond.Broadcast()
				t.mu.Unlock()
				return len(payload), sendErr
			}
			t.pendingErr = sendErr
			t.cond.Broadcast()
			continue
		}

		if dispatch.dst == ebusBroadcast {
			continue
		}

		t.pending = append(t.pending, ebusSymbolAck)
		if isInitiatorCapableAddress(dispatch.dst) {
			t.ignoreUntilSyn = true
			t.cond.Broadcast()
			continue
		}

		response := make([]byte, 0, 6+len(respPayload))
		response = append(response, dispatch.dst, dispatch.src, dispatch.pb, dispatch.sb, byte(len(respPayload)))
		response = append(response, respPayload...)
		response = append(response, crcValue(response))

		t.pending = append(t.pending, escapeBytes(response)...)
		t.ignoreUntilSyn = true
	}
	t.cond.Broadcast()
	t.mu.Unlock()
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

func (t *EbusdTCPTransport) consumeRawByte(raw byte) (*ebusdHexDispatch, error) {
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
		return nil, nil
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
			return nil, ebuserrors.ErrInvalidPayload
		}
		return t.maybeDispatchTelegram()
	}

	if raw == ebusSymbolEscape {
		t.escape = true
		return nil, nil
	}
	if raw == ebusSymbolSyn {
		// End-of-message boundary (sent by the bus state machine after a full transaction).
		t.telegram = t.telegram[:0]
		return nil, nil
	}

	t.telegram = append(t.telegram, raw)
	return t.maybeDispatchTelegram()
}

func (t *EbusdTCPTransport) maybeDispatchTelegram() (*ebusdHexDispatch, error) {
	if len(t.telegram) == 0 {
		return nil, nil
	}

	if len(t.telegram) < 5 {
		return nil, nil
	}

	dataLen := int(t.telegram[4])
	expected := 6 + dataLen
	if len(t.telegram) < expected {
		return nil, nil
	}
	if len(t.telegram) != expected {
		t.telegram = t.telegram[:0]
		return nil, ebuserrors.ErrInvalidPayload
	}

	if len(t.telegram) < 6 {
		t.telegram = t.telegram[:0]
		return nil, ebuserrors.ErrInvalidPayload
	}

	if crcValue(t.telegram[:len(t.telegram)-1]) != t.telegram[len(t.telegram)-1] {
		t.telegram = t.telegram[:0]
		return nil, ebuserrors.ErrCRCMismatch
	}

	src := t.telegram[0]
	dst := t.telegram[1]
	pb := t.telegram[2]
	sb := t.telegram[3]
	payload := append([]byte(nil), t.telegram[1:len(t.telegram)-1]...)
	t.telegram = t.telegram[:0]

	return &ebusdHexDispatch{
		src:     src,
		dst:     dst,
		pb:      pb,
		sb:      sb,
		payload: payload,
	}, nil
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
	lines, err := readResponseLines(t.conn, t.reader, t.readTimeout)
	if err != nil {
		return nil, err
	}
	resp, err := parseHexResponseLines(lines)
	if err != nil {
		return nil, err
	}
	return stripLengthPrefix(resp), nil
}

func readResponseLines(conn net.Conn, reader *bufio.Reader, readTimeout time.Duration) ([]string, error) {
	var lines []string

	if conn != nil && readTimeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	}

	followupDeadlineSet := false

	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			if isTimeout(err) {
				if conn != nil {
					_ = conn.SetReadDeadline(time.Time{})
				}
				if len(lines) > 0 {
					return lines, nil
				}
				return lines, ebuserrors.ErrTimeout
			}
			if isClosed(err) {
				if conn != nil {
					_ = conn.SetReadDeadline(time.Time{})
				}
				if len(lines) > 0 {
					return lines, nil
				}
				return lines, ebuserrors.ErrTransportClosed
			}
			if conn != nil {
				_ = conn.SetReadDeadline(time.Time{})
			}
			return lines, ebuserrors.ErrTransportClosed
		}

		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		if trimmed == "" {
			if errors.Is(err, io.EOF) {
				if conn != nil {
					_ = conn.SetReadDeadline(time.Time{})
				}
				if len(lines) > 0 {
					return lines, nil
				}
				return lines, ebuserrors.ErrTransportClosed
			}
			continue
		}

		lower := strings.ToLower(trimmed)
		if isIgnorableResponseLine(lower) {
			if errors.Is(err, io.EOF) {
				if conn != nil {
					_ = conn.SetReadDeadline(time.Time{})
				}
				if len(lines) > 0 {
					return lines, nil
				}
				return lines, ebuserrors.ErrTransportClosed
			}
			continue
		}

		lines = append(lines, trimmed)

		if looksLikeHexResponseLine(trimmed) || strings.HasPrefix(lower, "done") {
			break
		}

		if conn != nil && !followupDeadlineSet {
			followup := ebusdFollowupWindow
			if readTimeout > 0 && readTimeout < followup {
				followup = readTimeout
			}
			if followup > 0 {
				_ = conn.SetReadDeadline(time.Now().Add(followup))
				followupDeadlineSet = true
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
	}

	if conn != nil {
		_ = conn.SetReadDeadline(time.Time{})
	}
	if len(lines) == 0 {
		return lines, ebuserrors.ErrTimeout
	}
	return lines, nil
}

func isIgnorableResponseLine(lower string) bool {
	return strings.Contains(lower, "dump enabled") ||
		strings.Contains(lower, "dump disabled")
}

func looksLikeHexResponseLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(strings.ToLower(trimmed), "0x") {
		trimmed = strings.TrimSpace(trimmed[2:])
	}
	hexText := stripHexSpaces(trimmed)
	if hexText == "" || len(hexText)%2 != 0 {
		return false
	}
	for _, r := range hexText {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
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
