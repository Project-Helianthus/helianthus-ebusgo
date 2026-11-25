package adaptermux

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// respUp is a synthetic upstream. SendResponderBytes never calls RequestStart.
type respUp struct {
	mu         sync.Mutex
	writes     [][]byte
	symbols    []byte
	starts     []byte
	blockN     int
	blockStart chan struct{}
	entered    chan struct{}
	echo       func(byte)
}

func (u *respUp) ReadByte() (byte, error) { return 0, io.EOF }
func (u *respUp) Close() error            { return nil }

func (u *respUp) Write(p []byte) (int, error) {
	u.mu.Lock()
	u.writes = append(u.writes, append([]byte(nil), p...))
	u.mu.Unlock()
	return len(p), nil
}

func (u *respUp) SendResponderBytes(p []byte) (int, error) {
	for _, b := range p {
		u.mu.Lock()
		n := len(u.symbols)
		u.symbols = append(u.symbols, b)
		block := u.blockN > 0 && n < u.blockN
		u.mu.Unlock()
		if block && u.blockStart != nil {
			if u.entered != nil {
				select {
				case u.entered <- struct{}{}:
				default:
				}
			}
			<-u.blockStart
		}
		if u.echo != nil {
			u.echo(b)
		}
	}
	return len(p), nil
}

func (u *respUp) RequestStart(initiator byte) error {
	u.mu.Lock()
	u.starts = append(u.starts, initiator)
	block := u.blockStart
	u.mu.Unlock()
	if u.entered != nil {
		select {
		case u.entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	return nil
}

func (u *respUp) snapshot() (symbols []byte, writes []byte, starts []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, w := range u.writes {
		writes = append(writes, w...)
	}
	return append([]byte(nil), u.symbols...), writes, append([]byte(nil), u.starts...)
}

// rawOnly is a RawTransport with RequestStart and no responder emission.
type rawOnly struct{}

func (rawOnly) ReadByte() (byte, error)     { return 0, io.EOF }
func (rawOnly) Write(p []byte) (int, error) { return len(p), nil }
func (rawOnly) Close() error                { return nil }
func (rawOnly) RequestStart(byte) error     { return nil }

func newRespMux(t *testing.T, up transport.RawTransport) *Mux {
	t.Helper()
	m := New(Config{
		Protocol:      "enh",
		Logger:        log.New(io.Discard, "", 0),
		StartDeadline: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel
	m.upstream = up
	t.Cleanup(cancel)
	return m
}

type respSess struct {
	id     uint64
	client net.Conn
	mu     sync.Mutex
	buf    bytes.Buffer
}

func addRespSession(t *testing.T, m *Mux) *respSess {
	t.Helper()
	client, server := net.Pipe()
	id := m.AddSession(server)
	if id == 0 {
		t.Fatal("AddSession rejected")
	}
	s := &respSess{id: id, client: client}
	go func() {
		b := make([]byte, 512)
		for {
			n, err := client.Read(b)
			if n > 0 {
				s.mu.Lock()
				s.buf.Write(b[:n])
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = client.Close()
		m.RemoveSession(id)
	})
	return s
}

func (s *respSess) write(t *testing.T, p []byte) {
	t.Helper()
	if _, err := s.client.Write(p); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (s *respSess) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

func enhCmd(cmd, data byte) []byte {
	f := transport.EncodeENH(transport.ENHCommand(cmd), data)
	return append([]byte(nil), f[:]...)
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition not met before the failure deadline")
		}
		runtime.Gosched()
	}
}

func waitSuffix(t *testing.T, s *respSess, suffix []byte) []byte {
	t.Helper()
	var got []byte
	waitUntil(t, func() bool {
		got = s.bytes()
		return bytes.HasSuffix(got, suffix)
	})
	return got
}

type trafficVec struct {
	AdapterSymbols string `json:"adapter_symbols"`
	AdapterWrites  string `json:"adapter_writes"`
	RequestStarts  string `json:"request_starts"`
	Session        string `json:"session"`
	SessionB       string `json:"session_b"`
}

func loadVec(t *testing.T, name string) trafficVec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "responder", name))
	if err != nil {
		t.Fatal(err)
	}
	var v trafficVec
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	if s == "" {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func trimHexPrefix(t *testing.T, got []byte, prefix string) []byte {
	t.Helper()
	p := mustHex(t, prefix)
	if !bytes.HasPrefix(got, p) {
		t.Fatalf("session %x does not start with %s", got, prefix)
	}
	return got[len(p):]
}

func compareTraffic(t *testing.T, v trafficVec, session, symbols, writes, starts []byte) {
	t.Helper()
	if !bytes.Equal(session, mustHex(t, v.Session)) {
		t.Fatalf("session %x want %s", session, v.Session)
	}
	if !bytes.Equal(symbols, mustHex(t, v.AdapterSymbols)) {
		t.Fatalf("adapter symbols %x want %s", symbols, v.AdapterSymbols)
	}
	if !bytes.Equal(writes, mustHex(t, v.AdapterWrites)) {
		t.Fatalf("adapter writes %x want %s", writes, v.AdapterWrites)
	}
	if !bytes.Equal(starts, mustHex(t, v.RequestStarts)) {
		t.Fatalf("RequestStart %x want %s", starts, v.RequestStarts)
	}
}

// targetAndWait registers addr and waits for TARGETED.
func targetAndWait(t *testing.T, s *respSess, addr byte) {
	t.Helper()
	before := len(s.bytes())
	s.write(t, enhCmd(extNibbleTarget, addr))
	want := enhCmd(extNibbleTarget, addr)
	waitUntil(t, func() bool {
		got := s.bytes()
		return len(got) >= before+len(want) && bytes.Equal(got[before:before+len(want)], want)
	})
}

// commitSlot writes BEGIN, STAGE and COMMIT for the open slot and waits
// until the committed buffer has n bytes.
func commitSlot(t *testing.T, m *Mux, s *respSess, payload []byte) {
	t.Helper()
	var tag byte
	waitUntil(t, func() bool {
		m.responder.mu.Lock()
		defer m.responder.mu.Unlock()
		if m.responder.slot == nil {
			return false
		}
		tag = m.responder.slot.key.tag
		return true
	})
	var buf []byte
	buf = append(buf, enhCmd(extNibbleBegin, tag)...)
	for _, b := range payload {
		buf = append(buf, enhCmd(extNibbleStage, b)...)
	}
	buf = append(buf, enhCmd(extNibbleCommit, byte(len(payload)))...)
	s.write(t, buf)
	waitUntil(t, func() bool {
		m.responder.mu.Lock()
		defer m.responder.mu.Unlock()
		return m.responder.slot != nil && len(m.responder.slot.committed) == len(payload)
	})
}

func feedRaw(m *Mux, p []byte) {
	for _, b := range p {
		m.deliverRawToSessions(b)
	}
}

func waitSymbols(t *testing.T, up *respUp, m *Mux, n int) {
	t.Helper()
	waitUntil(t, func() bool {
		sym, _, _ := up.snapshot()
		return len(sym) >= n && !m.responderWindowOpen()
	})
}
