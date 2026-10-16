//go:build !tinygo

package transport_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

func baudFrames(frames ...transport.ENHFrame) []byte {
	var out []byte
	for _, frame := range frames {
		pair := transport.EncodeENH(frame.Command, frame.Data)
		out = append(out, pair[:]...)
	}
	return out
}

func TestBaudObservability_ReadPreservesFaultBoundary(t *testing.T) {
	t.Parallel()
	// The byte after the orphan ENH byte2 must remain unobserved: Parse
	// stops at the first error, even though the connection read it.
	conn := &stubConn{readData: []byte{0x55, 0x80, 0x66}}
	enh := transport.NewENHTransport(conn, time.Second, time.Second)
	var provider transport.BaudObservability = enh
	if got := provider.BaudObservabilitySnapshot(); got != (transport.BaudObservabilitySnapshot{}) {
		t.Fatalf("new session snapshot = %+v; want zero/unavailable", got)
	}
	if b, err := enh.ReadByte(); err != nil || b != 0x55 {
		t.Fatalf("valid prefix = %x, %v", b, err)
	}
	want := transport.BaudObservabilitySnapshot{
		ReceivedSymbolsTotal: 1, ENHFramingErrorsTotal: 1, ENHParserResetsTotal: 1,
	}
	if got := provider.BaudObservabilitySnapshot(); got != want {
		t.Fatalf("snapshot = %+v; want %+v", got, want)
	}
	if _, err := enh.ReadByte(); !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("deferred error = %v", err)
	}
	if got := provider.BaudObservabilitySnapshot(); got != want {
		t.Fatalf("draining error counted twice: %+v", got)
	}
	conn.readData = append(conn.readData, 0x77)
	if b, err := enh.ReadByte(); err != nil || b != 0x77 {
		t.Fatalf("post-recovery byte = %x, %v", b, err)
	}
	want.ReceivedSymbolsTotal++
	if got := provider.BaudObservabilitySnapshot(); got != want {
		t.Fatalf("recovered snapshot = %+v; want %+v", got, want)
	}
	if err := enh.Close(); err != nil {
		t.Fatal(err)
	}
	if got := provider.BaudObservabilitySnapshot(); got != want {
		t.Fatalf("Close erased session counters: %+v", got)
	}
}

func TestBaudObservability_AllENHReadPaths(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"init", "arbitration", "info"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			frames := []transport.ENHFrame{{Command: transport.ENHResReceived, Data: 0x55}}
			want := transport.BaudObservabilitySnapshot{ReceivedSymbolsTotal: 1, ENHFramingErrorsTotal: 1}
			switch path {
			case "init":
				frames = append(frames, transport.ENHFrame{Command: transport.ENHResResetted, Data: 0x01})
				want.AdapterResetResponsesTotal = 1
				want.ENHParserResetsTotal = 1
			case "arbitration":
				frames = append(frames, transport.ENHFrame{Command: transport.ENHResStarted, Data: 0x31})
				want.ENHParserResetsTotal = 1
			case "info":
				frames = append(frames, transport.ENHFrame{Command: transport.ENHResInfo, Data: 1}, transport.ENHFrame{Command: transport.ENHResInfo, Data: 0x42})
			}
			// A trailing parse fault is observed even when a valid response
			// takes precedence and the existing operation succeeds.
			conn := &stubConn{readData: append(baudFrames(frames...), 0x80)}
			enh := transport.NewENHTransport(conn, time.Second, time.Second)
			defer func() { _ = enh.Close() }()
			switch path {
			case "init":
				result, err := enh.InitWithResult(1)
				if err != nil || !result.Confirmed || result.Features != 1 {
					t.Fatalf("InitWithResult = %+v, %v", result, err)
				}
			case "arbitration":
				if err := enh.StartArbitration(0x31); err != nil {
					t.Fatal(err)
				}
				// Pre-grant symbols are counted even though arbitration drops them.
			case "info":
				data, err := enh.RequestInfo(transport.AdapterInfoVersion)
				if err != nil || len(data) != 1 || data[0] != 0x42 {
					t.Fatalf("RequestInfo = %x, %v", data, err)
				}
			}
			if got := enh.BaudObservabilitySnapshot(); got != want {
				t.Fatalf("snapshot = %+v; want %+v", got, want)
			}
		})
	}
}

func TestBaudObservability_EscapeAndAdapterReset(t *testing.T) {
	t.Parallel()
	conn := &stubConn{readData: baudFrames(
		transport.ENHFrame{Command: transport.ENHResReceived, Data: 0xA9},
		transport.ENHFrame{Command: transport.ENHResReceived, Data: 0xFF},
		transport.ENHFrame{Command: transport.ENHResResetted, Data: 1},
		transport.ENHFrame{Command: transport.ENHResReceived, Data: 0x55},
	)}
	enh := transport.NewENSTransport(conn, time.Second, time.Second)
	defer func() { _ = enh.Close() }()
	if ev, err := enh.ReadEvent(); err != nil || ev.Kind != transport.StreamEventReset {
		t.Fatalf("ReadEvent = %+v, %v", ev, err)
	}
	if b, err := enh.ReadByte(); err != nil || b != 0x55 {
		t.Fatalf("ReadByte = %x, %v", b, err)
	}
	want := transport.BaudObservabilitySnapshot{
		ReceivedSymbolsTotal: 3, AdapterResetResponsesTotal: 1,
		EscapeDecodeFaultsTotal: 1, EscapeRecoveriesTotal: 1,
	}
	if got := enh.BaudObservabilitySnapshot(); got != want {
		t.Fatalf("snapshot = %+v; want %+v", got, want)
	}
}

func TestBaudObservability_ParseErrorResetsInAllReadPaths(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"init", "arbitration", "info"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			enh := transport.NewENHTransport(&stubConn{readData: []byte{0x80}}, time.Second, time.Second)
			defer func() { _ = enh.Close() }()
			var err error
			switch path {
			case "init":
				_, err = enh.Init(1)
			case "arbitration":
				err = enh.StartArbitration(0x31)
			case "info":
				_, err = enh.RequestInfo(transport.AdapterInfoVersion)
			}
			if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
				t.Fatalf("parse error = %v", err)
			}
			want := transport.BaudObservabilitySnapshot{ENHFramingErrorsTotal: 1, ENHParserResetsTotal: 1}
			if got := enh.BaudObservabilitySnapshot(); got != want {
				t.Fatalf("snapshot = %+v; want %+v", got, want)
			}
		})
	}
}

func TestBaudObservability_ReconnectRetainsCounters(t *testing.T) {
	t.Parallel()
	freshConn := &stubConn{readData: baudFrames(transport.ENHFrame{Command: transport.ENHResResetted, Data: 1})}
	enh := transport.NewENHTransport(&stubConn{readData: []byte{0x55}}, time.Second, time.Second,
		transport.WithDialFunc(func() (net.Conn, error) { return freshConn, nil }))
	defer func() { _ = enh.Close() }()
	if b, err := enh.ReadByte(); err != nil || b != 0x55 {
		t.Fatalf("initial read = %x, %v", b, err)
	}
	if err := enh.Reconnect(); err != nil {
		t.Fatal(err)
	}
	freshConn.readData = append(freshConn.readData, 0x66)
	if b, err := enh.ReadByte(); err != nil || b != 0x66 {
		t.Fatalf("reconnected read = %x, %v", b, err)
	}
	want := transport.BaudObservabilitySnapshot{ReceivedSymbolsTotal: 2, AdapterResetResponsesTotal: 1, ENHParserResetsTotal: 2}
	if got := enh.BaudObservabilitySnapshot(); got != want {
		t.Fatalf("snapshot = %+v; want %+v", got, want)
	}
}

type baudTimeoutConn struct{ stubConn }

func (*baudTimeoutConn) Read([]byte) (int, error) { return 0, baudTimeoutError{} }

type baudTimeoutError struct{}

func (baudTimeoutError) Error() string   { return "test read timeout" }
func (baudTimeoutError) Timeout() bool   { return true }
func (baudTimeoutError) Temporary() bool { return true }

func TestBaudObservability_TimeoutResetIsNotFramingError(t *testing.T) {
	t.Parallel()
	enh := transport.NewENHTransport(&baudTimeoutConn{}, time.Second, time.Second)
	defer func() { _ = enh.Close() }()
	if _, err := enh.ReadByte(); !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("read error = %v", err)
	}
	want := transport.BaudObservabilitySnapshot{ENHParserResetsTotal: 1}
	if got := enh.BaudObservabilitySnapshot(); got != want {
		t.Fatalf("snapshot = %+v; want %+v", got, want)
	}
}

func TestBaudObservability_DriverRuntimeSessions(t *testing.T) {
	t.Parallel()
	runtime := transport.NewDriverRuntime(func(context.Context) (*transport.ManagedRawTransport, error) {
		enh := transport.NewENHTransport(&stubConn{readData: []byte{0x55}}, time.Second, time.Second)
		request, closed := make(chan struct{}), make(chan struct{})
		go func() {
			<-request
			_ = enh.Close()
			close(closed)
		}()
		return &transport.ManagedRawTransport{Transport: enh, Lifecycle: transport.DriverLifecycleHandle{CloseRequest: request, Closed: closed}}, nil
	}, transport.DriverRuntimeConfig{DrainTimeout: time.Second})
	defer func() {
		if err := runtime.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if _, err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var saved transport.BaudObservabilitySnapshot
	if err := runtime.Invoke(context.Background(), func(raw transport.RawTransport) error {
		_, err := raw.ReadByte()
		saved = raw.(transport.BaudObservability).BaudObservabilitySnapshot()
		return err
	}); err != nil || saved.ReceivedSymbolsTotal != 1 {
		t.Fatalf("first session = %+v, %v", saved, err)
	}
	if generation, err := runtime.Replace(context.Background()); err != nil || generation != 2 {
		t.Fatalf("Replace = %d, %v", generation, err)
	}
	if err := runtime.Invoke(context.Background(), func(raw transport.RawTransport) error {
		if got := raw.(transport.BaudObservability).BaudObservabilitySnapshot(); got != (transport.BaudObservabilitySnapshot{}) {
			t.Fatalf("new session retained old counters: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if saved.ReceivedSymbolsTotal != 1 {
		t.Fatalf("detached snapshot changed: %+v", saved)
	}
	for _, raw := range []transport.RawTransport{
		transport.NewLoopback(),
		transport.NewTCPPlainTransport(&stubConn{}, time.Second, time.Second),
		transport.NewUDPPlainTransport(nil, time.Second, time.Second),
		transport.NewEbusdTCPTransport(&stubConn{}, time.Second, time.Second),
	} {
		if _, ok := raw.(transport.BaudObservability); ok {
			t.Errorf("unsupported transport %T claims baud observations", raw)
		}
	}
}

func TestBaudObservability_ConcurrentSnapshot(t *testing.T) {
	t.Parallel()
	data := make([]byte, 1024)
	for i := range data {
		data[i] = 0x55
	}
	enh := transport.NewENHTransport(&stubConn{readData: data}, time.Second, time.Second)
	defer func() { _ = enh.Close() }()
	var readers sync.WaitGroup
	stop := make(chan struct{})
	readers.Add(1)
	go func() {
		defer readers.Done()
		var previous uint64
		for {
			select {
			case <-stop:
				return
			default:
				got := enh.BaudObservabilitySnapshot()
				if got.ReceivedSymbolsTotal < previous || got.ConfiguredBaudKnown || got.ConfiguredBaud != 0 {
					t.Errorf("invalid concurrent snapshot: %+v", got)
					return
				}
				previous = got.ReceivedSymbolsTotal
			}
		}
	}()
	for range data {
		if _, err := enh.ReadByte(); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	readers.Wait()
	if got := enh.BaudObservabilitySnapshot(); got.ReceivedSymbolsTotal != uint64(len(data)) {
		t.Fatalf("symbols = %d; want %d", got.ReceivedSymbolsTotal, len(data))
	}
}
