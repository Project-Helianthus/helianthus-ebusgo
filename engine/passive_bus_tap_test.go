package engine

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

type passiveEventRecorder struct {
	mu     sync.Mutex
	events []PassiveTapEvent
	notify chan struct{}
}

func newPassiveEventRecorder() *passiveEventRecorder {
	return &passiveEventRecorder{
		notify: make(chan struct{}, 32),
	}
}

func (recorder *passiveEventRecorder) OnPassiveTapEvent(event PassiveTapEvent) {
	recorder.mu.Lock()
	recorder.events = append(recorder.events, event)
	recorder.mu.Unlock()
	select {
	case recorder.notify <- struct{}{}:
	default:
	}
}

func (recorder *passiveEventRecorder) snapshot() []PassiveTapEvent {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]PassiveTapEvent(nil), recorder.events...)
}

func TestStartPassiveBusTapRejectsEbusdTCP(t *testing.T) {
	t.Parallel()

	for _, protocolName := range []TransportProtocol{TransportEbusdTCP, TransportProtocol("ebusd")} {
		protocolName := protocolName
		t.Run(string(protocolName), func(t *testing.T) {
			t.Parallel()

			cfg := withDonor(DefaultConfig())
			cfg.TransportConfig = TransportConfig{
				Protocol: protocolName,
				Network:  "tcp",
				Address:  "127.0.0.1:9999",
			}

			if _, err := StartPassiveBusTap(context.Background(), cfg, newPassiveEventRecorder()); err == nil {
				t.Fatal("StartPassiveBusTap error = nil; want unsupported transport error")
			}
		})
	}
}

func TestPassiveBusTap_ReconnectsAfterDisconnect(t *testing.T) {
	t.Parallel()

	recorder := newPassiveEventRecorder()
	var dialMu sync.Mutex
	dialCount := 0

	cfg := withDonor(DefaultConfig())
	cfg.TransportConfig = TransportConfig{
		Protocol:     TransportENH,
		Network:      "tcp",
		Address:      "127.0.0.1:9999",
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
		DialTimeout:  time.Second,
		Dial: func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
			dialMu.Lock()
			defer dialMu.Unlock()
			dialCount++
			client, server := net.Pipe()
			switch dialCount {
			case 1:
				go writePassivePayload(server, enhReceivedBytes([]byte{0x11}))
			case 2:
				go writePassivePayload(server, enhReceivedBytes([]byte{0x22}))
			default:
				_ = server.Close()
				return nil, fmt.Errorf("unexpected extra reconnect")
			}
			return client, nil
		},
	}
	cfg.PassiveAbsenceThreshold = time.Second
	cfg.PassiveReconnectInitialDelay = 5 * time.Millisecond
	cfg.PassiveReconnectMaxDelay = 5 * time.Millisecond

	tap, err := StartPassiveBusTap(context.Background(), cfg, recorder)
	if err != nil {
		t.Fatalf("StartPassiveBusTap error = %v", err)
	}
	defer func() {
		if err := tap.Close(); err != nil {
			t.Fatalf("Close error = %v", err)
		}
	}()

	events := waitForPassiveEvents(t, recorder, 2*time.Second, func(events []PassiveTapEvent) bool {
		return hasPassiveSymbols(events, 0x11, 0x22) &&
			countPassiveEventKind(events, PassiveTapEventConnected) >= 2 &&
			countPassiveEventKind(events, PassiveTapEventDisconnected) >= 1
	})

	if !hasPassiveSymbols(events, 0x11, 0x22) {
		t.Fatalf("symbol events = %v; want reconnect sequence [0x11, 0x22]", passiveSymbols(events))
	}

	snapshot := tap.Snapshot()
	if snapshot.ConnectCount < 2 {
		t.Fatalf("ConnectCount = %d; want >= 2", snapshot.ConnectCount)
	}
	if snapshot.DisconnectCount < 1 {
		t.Fatalf("DisconnectCount = %d; want >= 1", snapshot.DisconnectCount)
	}
}

func TestPassiveBusTap_ProxyLikeEndpointDoesNotReconnectOnReadTimeoutSilence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
	}{
		{name: "default proxy port", address: "127.0.0.1:19001"},
		{name: "matrix proxy port", address: "127.0.0.1:19183"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client, server := net.Pipe()
			defer func() { _ = server.Close() }()

			recorder := newPassiveEventRecorder()
			cfg := withDonor(DefaultConfig())
			cfg.TransportConfig = TransportConfig{
				Protocol:     TransportENS,
				Network:      "tcp",
				Address:      test.address,
				ReadTimeout:  20 * time.Millisecond,
				WriteTimeout: 20 * time.Millisecond,
				DialTimeout:  time.Second,
				Dial: func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
					return client, nil
				},
			}
			cfg.PassiveAbsenceThreshold = 60 * time.Millisecond
			cfg.PassiveReconnectInitialDelay = 5 * time.Millisecond
			cfg.PassiveReconnectMaxDelay = 5 * time.Millisecond

			tap, err := StartPassiveBusTap(context.Background(), cfg, recorder)
			if err != nil {
				t.Fatalf("StartPassiveBusTap error = %v", err)
			}
			defer func() {
				if err := tap.Close(); err != nil {
					t.Fatalf("Close error = %v", err)
				}
			}()

			waitForPassiveEvents(t, recorder, 2*time.Second, func(events []PassiveTapEvent) bool {
				return countPassiveEventKind(events, PassiveTapEventReadTimeout) >= 3
			})

			time.Sleep(100 * time.Millisecond)

			snapshot := tap.Snapshot()
			if !snapshot.Connected {
				t.Fatal("Connected = false; want true for proxy-like silent observer session")
			}
			if snapshot.ConnectCount != 1 {
				t.Fatalf("ConnectCount = %d; want 1", snapshot.ConnectCount)
			}
			if snapshot.DisconnectCount != 0 {
				t.Fatalf("DisconnectCount = %d; want 0", snapshot.DisconnectCount)
			}
		})
	}
}

func TestPassiveBusTap_ProxyLikeObserverStreamEmitsLogicalSymbolsWithoutDecodeFault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
	}{
		{name: "default proxy port", address: "127.0.0.1:19001"},
		{name: "matrix proxy port", address: "127.0.0.1:19183"},
	}

	request := protocol.Frame{
		Source:    0xF7,
		Target:    0x08,
		Primary:   0xB5,
		Secondary: 0x09,
		Data:      []byte{protocol.SymbolEscape},
	}
	logicalPayload := proxyObserverTransactionBytes(request, []byte{0x11, 0x22})

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client, server := net.Pipe()
			defer func() { _ = server.Close() }()

			recorder := newPassiveEventRecorder()
			cfg := withDonor(DefaultConfig())
			cfg.TransportConfig = TransportConfig{
				Protocol:     TransportENS,
				Network:      "tcp",
				Address:      test.address,
				ReadTimeout:  20 * time.Millisecond,
				WriteTimeout: 20 * time.Millisecond,
				DialTimeout:  time.Second,
				Dial: func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
					return client, nil
				},
			}
			cfg.PassiveAbsenceThreshold = time.Second
			cfg.PassiveReconnectInitialDelay = time.Second
			cfg.PassiveReconnectMaxDelay = time.Second

			tap, err := StartPassiveBusTap(context.Background(), cfg, recorder)
			if err != nil {
				t.Fatalf("StartPassiveBusTap error = %v", err)
			}
			defer func() {
				if err := tap.Close(); err != nil {
					t.Fatalf("Close error = %v", err)
				}
			}()

			go func() {
				_, _ = server.Write(enhReceivedBytes(wireEscapeSymbols(logicalPayload)))
			}()

			events := waitForPassiveEvents(t, recorder, 2*time.Second, func(events []PassiveTapEvent) bool {
				return hasPassiveSymbols(events, logicalPayload...)
			})

			if got := countPassiveEventKind(events, PassiveTapEventDecodeFault); got != 0 {
				t.Fatalf("decode fault count = %d; want 0 for proxy-like observer payload", got)
			}

			snapshot := tap.Snapshot()
			if got := snapshot.DecodeFaultCount; got != 0 {
				t.Fatalf("DecodeFaultCount = %d; want 0", got)
			}
		})
	}
}

func TestPassiveTapDecodesWireEscapes_DifferentiatesRemoteDirectAdapterLogicalStreams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  TransportConfig
		want bool
	}{
		{
			name: "remote direct adapter ip skips local decode",
			cfg: TransportConfig{
				Protocol: TransportENH,
				Network:  "tcp",
				Address:  "192.168.100.2:9999",
			},
			want: false,
		},
		{
			name: "remote direct adapter hostname skips local decode",
			cfg: TransportConfig{
				Protocol: TransportENS,
				Network:  "tcp",
				Address:  "adapter.local:9999",
			},
			want: false,
		},
		{
			name: "loopback direct adapter still decodes raw wire bytes",
			cfg: TransportConfig{
				Protocol: TransportENH,
				Network:  "tcp",
				Address:  "127.0.0.1:9999",
			},
			want: true,
		},
		{
			name: "proxy-like endpoint skips local decode",
			cfg: TransportConfig{
				Protocol: TransportENS,
				Network:  "tcp",
				Address:  "127.0.0.1:19001",
			},
			want: false,
		},
		{
			name: "custom remote direct port still decodes raw wire bytes",
			cfg: TransportConfig{
				Protocol: TransportENH,
				Network:  "tcp",
				Address:  "192.168.100.2:19183",
			},
			want: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cfg := withDonor(DefaultConfig())
			cfg.TransportConfig = test.cfg

			if got := passiveTapDecodesWireEscapes(cfg); got != test.want {
				t.Fatalf("passiveTapDecodesWireEscapes() = %v; want %v", got, test.want)
			}
		})
	}
}

func TestPassiveBusTap_RemoteDirectAdapterEndpointAcceptsLogicalObserverPayloadWithoutDecodeFault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		protocol TransportProtocol
		address  string
	}{
		{name: "remote direct ip", protocol: TransportENH, address: "192.168.100.2:9999"},
		{name: "remote direct hostname", protocol: TransportENS, address: "adapter.local:9999"},
	}

	request := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0xB5,
		Secondary: 0x09,
		Data:      []byte{protocol.SymbolEscape},
	}
	logicalPayload := proxyObserverTransactionBytes(request, []byte{0x11, 0x22})

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client, server := net.Pipe()
			defer func() { _ = server.Close() }()

			recorder := newPassiveEventRecorder()
			cfg := withDonor(DefaultConfig())
			cfg.TransportConfig = TransportConfig{
				Protocol:     test.protocol,
				Network:      "tcp",
				Address:      test.address,
				ReadTimeout:  20 * time.Millisecond,
				WriteTimeout: 20 * time.Millisecond,
				DialTimeout:  time.Second,
				Dial: func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
					return client, nil
				},
			}
			cfg.PassiveAbsenceThreshold = time.Second
			cfg.PassiveReconnectInitialDelay = time.Second
			cfg.PassiveReconnectMaxDelay = time.Second

			tap, err := StartPassiveBusTap(context.Background(), cfg, recorder)
			if err != nil {
				t.Fatalf("StartPassiveBusTap error = %v", err)
			}
			defer func() {
				if err := tap.Close(); err != nil {
					t.Fatalf("Close error = %v", err)
				}
			}()

			go func() {
				_, _ = server.Write(enhReceivedBytes(wireEscapeSymbols(logicalPayload)))
			}()

			events := waitForPassiveEvents(t, recorder, 2*time.Second, func(events []PassiveTapEvent) bool {
				return hasPassiveSymbols(events, logicalPayload...)
			})

			if got := countPassiveEventKind(events, PassiveTapEventDecodeFault); got != 0 {
				t.Fatalf("decode fault count = %d; want 0 for direct remote ENH observer payload", got)
			}

			snapshot := tap.Snapshot()
			if got := snapshot.DecodeFaultCount; got != 0 {
				t.Fatalf("DecodeFaultCount = %d; want 0", got)
			}
		})
	}
}

func TestPassiveBusTap_LoopbackENHStillReconnectsOnReadTimeoutSilence(t *testing.T) {
	t.Parallel()

	recorder := newPassiveEventRecorder()
	keepStreaming := make(chan struct{})
	var serversMu sync.Mutex
	var servers []net.Conn
	var dialMu sync.Mutex
	dialCount := 0

	cfg := withDonor(DefaultConfig())
	cfg.TransportConfig = TransportConfig{
		Protocol:     TransportENH,
		Network:      "tcp",
		Address:      "127.0.0.1:9999",
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
		DialTimeout:  time.Second,
		Dial: func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
			dialMu.Lock()
			defer dialMu.Unlock()
			dialCount++

			client, server := net.Pipe()
			serversMu.Lock()
			servers = append(servers, server)
			serversMu.Unlock()

			switch dialCount {
			case 1:
				// Stay silent so the absence threshold forces a reconnect.
			case 2:
				go func() {
					ticker := time.NewTicker(15 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-keepStreaming:
							return
						case <-ticker.C:
							if _, err := server.Write(enhReceivedBytes([]byte{0x44})); err != nil {
								return
							}
						}
					}
				}()
			default:
				_ = server.Close()
				return nil, fmt.Errorf("unexpected extra reconnect")
			}

			return client, nil
		},
	}
	cfg.PassiveAbsenceThreshold = 60 * time.Millisecond
	cfg.PassiveReconnectInitialDelay = 5 * time.Millisecond
	cfg.PassiveReconnectMaxDelay = 5 * time.Millisecond

	t.Cleanup(func() {
		close(keepStreaming)
		serversMu.Lock()
		defer serversMu.Unlock()
		for _, server := range servers {
			_ = server.Close()
		}
	})

	tap, err := StartPassiveBusTap(context.Background(), cfg, recorder)
	if err != nil {
		t.Fatalf("StartPassiveBusTap error = %v", err)
	}
	defer func() {
		if err := tap.Close(); err != nil {
			t.Fatalf("Close error = %v", err)
		}
	}()

	events := waitForPassiveEvents(t, recorder, 2*time.Second, func(events []PassiveTapEvent) bool {
		return countPassiveEventKind(events, PassiveTapEventDisconnected) >= 1 &&
			countPassiveEventKind(events, PassiveTapEventConnected) >= 2 &&
			hasPassiveSymbols(events, 0x44)
	})

	if !hasPassiveSymbols(events, 0x44) {
		t.Fatalf("symbol events = %v; want reconnect recovery symbol [0x44]", passiveSymbols(events))
	}

	snapshot := tap.Snapshot()
	if !snapshot.Connected {
		t.Fatal("Connected = false; want true after loopback ENH reconnect")
	}
	if snapshot.ConnectCount < 2 {
		t.Fatalf("ConnectCount = %d; want >= 2", snapshot.ConnectCount)
	}
	if snapshot.DisconnectCount < 1 {
		t.Fatalf("DisconnectCount = %d; want >= 1", snapshot.DisconnectCount)
	}
}

func TestPassiveBusTap_CustomDirectAdapterPortsStillDecodeWireEscapedObserverTraffic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
	}{
		{name: "loopback custom direct port", address: "127.0.0.1:10001"},
		{name: "remote custom direct port", address: "192.168.100.2:19183"},
	}

	request := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0xB5,
		Secondary: 0x09,
		Data:      []byte{protocol.SymbolEscape},
	}
	logicalPayload := proxyObserverTransactionBytes(request, []byte{0x11, 0x22})

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client, server := net.Pipe()
			defer func() { _ = server.Close() }()

			recorder := newPassiveEventRecorder()
			cfg := withDonor(DefaultConfig())
			cfg.TransportConfig = TransportConfig{
				Protocol:     TransportENH,
				Network:      "tcp",
				Address:      test.address,
				ReadTimeout:  20 * time.Millisecond,
				WriteTimeout: 20 * time.Millisecond,
				DialTimeout:  time.Second,
				Dial: func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
					return client, nil
				},
			}
			cfg.PassiveAbsenceThreshold = time.Second
			cfg.PassiveReconnectInitialDelay = time.Second
			cfg.PassiveReconnectMaxDelay = time.Second

			tap, err := StartPassiveBusTap(context.Background(), cfg, recorder)
			if err != nil {
				t.Fatalf("StartPassiveBusTap error = %v", err)
			}
			defer func() {
				if err := tap.Close(); err != nil {
					t.Fatalf("Close error = %v", err)
				}
			}()

			go func() {
				_, _ = server.Write(enhReceivedBytes(wireEscapeSymbols(logicalPayload)))
			}()

			events := waitForPassiveEvents(t, recorder, 2*time.Second, func(events []PassiveTapEvent) bool {
				return hasPassiveSymbols(events, logicalPayload...)
			})

			if got := countPassiveEventKind(events, PassiveTapEventDecodeFault); got != 0 {
				t.Fatalf("decode fault count = %d; want 0 for direct raw-wire observer payload", got)
			}

			snapshot := tap.Snapshot()
			if got := snapshot.DecodeFaultCount; got != 0 {
				t.Fatalf("DecodeFaultCount = %d; want 0", got)
			}
		})
	}
}

func TestPassiveTransactionReconstructor_ClassifiesProxyLikeObserverTraffic(t *testing.T) {
	t.Parallel()

	request := protocol.Frame{
		Source:    0xF7,
		Target:    0x08,
		Primary:   0xB5,
		Secondary: 0x09,
		Data:      []byte{protocol.SymbolEscape},
	}
	result := runProxyENSObserverHarness(t, []proxyObserverWrite{
		{delay: 25 * time.Millisecond, logicalSymbols: proxyObserverTransactionBytes(request, []byte{0x11, 0x22})},
	}, false, 1)

	event := result.requireEvent(t, PassiveClassifiedEventTransaction)
	if got := event.Request.Data; len(got) != 1 || got[0] != protocol.SymbolEscape {
		t.Fatalf("request data = %v; want [0x%02X]", got, protocol.SymbolEscape)
	}
	if !event.HasResponse {
		t.Fatal("transaction event missing response")
	}

	if got := result.snapshot.TapStatus.DecodeFaultCount; got != 0 {
		t.Fatalf("DecodeFaultCount = %d; want 0", got)
	}
	if got := result.snapshot.TapStatus.ObservedSymbolCount; got == 0 {
		t.Fatal("ObservedSymbolCount = 0; want proxy-like observer traffic to produce symbols")
	}
	if got := result.countEvents(PassiveClassifiedEventTransaction); got == 0 {
		t.Fatal("transaction event count = 0; want > 0 for reconstructible observer traffic")
	}
}
func waitForPassiveEvents(t *testing.T, recorder *passiveEventRecorder, timeout time.Duration, ready func([]PassiveTapEvent) bool) []PassiveTapEvent {
	t.Helper()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		events := recorder.snapshot()
		if ready(events) {
			return events
		}

		select {
		case <-recorder.notify:
		case <-deadline.C:
			t.Fatalf("timeout waiting for passive events; got %#v", events)
		}
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for condition")
}
func countPassiveEventKind(events []PassiveTapEvent, kind PassiveTapEventKind) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func passiveSymbols(events []PassiveTapEvent) []byte {
	out := make([]byte, 0, len(events))
	for _, event := range events {
		if event.Kind == PassiveTapEventSymbol {
			out = append(out, event.Symbol)
		}
	}
	return out
}

func hasPassiveSymbols(events []PassiveTapEvent, want ...byte) bool {
	got := passiveSymbols(events)
	if len(got) < len(want) {
		return false
	}
	index := 0
	for _, symbol := range got {
		if symbol != want[index] {
			continue
		}
		index++
		if index == len(want) {
			return true
		}
	}
	return false
}

func enhReceivedBytes(values []byte) []byte {
	out := make([]byte, 0, len(values)*2)
	for _, value := range values {
		seq := transport.EncodeENH(transport.ENHResReceived, value)
		out = append(out, seq[0], seq[1])
	}
	return out
}

// requestFrameBytes returns the wire bytes for a single eBUS request
// frame (`SRC DST PB SB LEN [data×LEN] CRC`) WITHOUT a trailing
// SymbolSyn. This matches the real-wire shape for M2I (initiator/
// initiator) and M2T (initiator/target) transactions, where the
// transaction continues with target ACK + (optional) response phase
// before the trailing SYN — there is NO SYN between the command CRC
// and the target's ACK byte (Spec_Prot_7 §3, P7).
//
// Use frameBytes (with trailing SYN) for broadcast wire tests where
// the SYN immediately after CRC matches actual broadcast wire shape.
func requestFrameBytes(frame protocol.Frame) []byte {
	raw := make([]byte, 0, 6+len(frame.Data))
	raw = append(raw, frame.Source, frame.Target, frame.Primary, frame.Secondary, byte(len(frame.Data)))
	raw = append(raw, frame.Data...)
	raw = append(raw, protocol.CRC(raw))
	return raw
}

// frameBytes returns a request frame with a trailing SymbolSyn — the
// real-wire shape for broadcast transactions (which have no ACK or
// response phase, so SYN immediately follows CRC). Most M2I/M2T tests
// should use requestFrameBytes instead.
func frameBytes(frame protocol.Frame) []byte {
	return append(requestFrameBytes(frame), protocol.SymbolSyn)
}

func proxyObserverTransactionBytes(request protocol.Frame, responseData []byte) []byte {
	// Real eBUS wire (Spec_Prot_7 §3) for an M2T transaction:
	//
	//     [SYN]  (bus-idle marker between frames — P6 Layer 1)
	//     SRC DST PB SB LEN data CRC
	//     ACK    (target acks command — NO SYN before this byte, P7)
	//     RESP_LEN resp_data RESP_CRC
	//     ACK    (initiator acks response)
	//     SYN    (transaction terminator)
	//
	// Pre-P6 fixture lacked the leading SYN; pre-P7 fixture (legacy
	// frameBytes with trailing SYN) injected a spurious SYN between
	// command CRC and target ACK that does not exist on real wire.
	// requestFrameBytes returns SRC..CRC without a trailing SYN, so
	// the wire shape now matches Spec_Prot_7 exactly.
	payload := []byte{protocol.SymbolSyn}
	payload = append(payload, requestFrameBytes(request)...)
	payload = append(payload, protocol.SymbolAck)
	payload = append(payload, responseSegmentBytes(responseData)...)
	payload = append(payload, protocol.SymbolAck, protocol.SymbolSyn)
	return payload
}

type proxyObserverWrite struct {
	delay          time.Duration
	logicalSymbols []byte
}

type proxyObserverHarnessResult struct {
	snapshot   PassiveReconstructorSnapshot
	classified []PassiveClassifiedEvent
}

func (result proxyObserverHarnessResult) requireEvent(t *testing.T, kind PassiveClassifiedEventKind) PassiveClassifiedEvent {
	t.Helper()
	for _, event := range result.classified {
		if event.Kind == kind {
			return event
		}
	}
	t.Fatalf("missing classified event kind %d in %#v", kind, result.classified)
	return PassiveClassifiedEvent{}
}

func (result proxyObserverHarnessResult) countEvents(kind PassiveClassifiedEventKind) int {
	count := 0
	for _, event := range result.classified {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func collectPassiveClassifiedEvents(subscription *PassiveClassifiedSubscription, quietWindow time.Duration) []PassiveClassifiedEvent {
	events := make([]PassiveClassifiedEvent, 0, 8)
	timer := time.NewTimer(quietWindow)
	defer timer.Stop()

	for {
		select {
		case event, ok := <-subscription.Events():
			if !ok {
				return events
			}
			events = append(events, event)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quietWindow)
		case <-timer.C:
			return events
		}
	}
}

func wireEscapeSymbols(symbols []byte) []byte {
	out := make([]byte, 0, len(symbols)+4)
	for _, symbol := range symbols {
		switch symbol {
		case protocol.SymbolEscape:
			out = append(out, protocol.SymbolEscape, 0x00)
		case protocol.SymbolSyn:
			out = append(out, protocol.SymbolEscape, 0x01)
		default:
			out = append(out, symbol)
		}
	}
	return out
}

func writePassivePayload(conn net.Conn, payload []byte) {
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write(payload)
}

func TestProxyENSObserverReplayHarness_CombinedArtifactFixtureRoutesBlameToStreamShape(t *testing.T) {
	t.Parallel()

	// Synthetic incomplete observer symbols. The tap ingests them and the
	// reconstructor does not classify a transaction from this shape.
	currentProxyModeledObserverSymbols := []byte{0x10, 0x03, 0x07, 0x04}

	result := runProxyENSObserverHarness(t, []proxyObserverWrite{
		{delay: 25 * time.Millisecond, logicalSymbols: currentProxyModeledObserverSymbols},
	}, false, 1)

	if got := result.countEvents(PassiveClassifiedEventTransaction); got != 0 {
		t.Fatalf("transaction event count = %d; want 0 for current proxy-modeled observer stream", got)
	}
	if got := result.snapshot.TapStatus.ObservedSymbolCount; got == 0 {
		t.Fatal("ObservedSymbolCount = 0; want harness to prove the stream was ingested before routing blame")
	}
}

func runProxyENSObserverHarness(t *testing.T, writes []proxyObserverWrite, waitCompleted bool, requiredTransactions int) proxyObserverHarnessResult {
	t.Helper()
	_ = waitCompleted
	_ = requiredTransactions

	client, server := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { _ = server.Close() }()

	cfg := withDonor(DefaultConfig())
	cfg.TransportConfig = TransportConfig{
		Protocol:     TransportENS,
		Network:      "tcp",
		Address:      "127.0.0.1:19183",
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
		DialTimeout:  time.Second,
		Dial: func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
			return client, nil
		},
	}
	cfg.PassiveAbsenceThreshold = time.Second
	cfg.PassiveReconnectInitialDelay = time.Second
	cfg.PassiveReconnectMaxDelay = time.Second

	reconstructor, err := StartPassiveTransactionReconstructor(ctx, cfg)
	if err != nil {
		t.Fatalf("StartPassiveTransactionReconstructor error = %v", err)
	}
	defer func() {
		if err := reconstructor.Close(); err != nil {
			t.Fatalf("reconstructor.Close error = %v", err)
		}
	}()

	waitForCondition(t, 2*time.Second, func() bool {
		return reconstructor.Snapshot().TapStatus.Connected
	})

	subscription, err := reconstructor.Subscribe("harness", PassiveSubscriberCritical, 16)
	if err != nil {
		t.Fatalf("Subscribe error = %v", err)
	}
	defer subscription.Close()

	totalSymbols := 0
	for _, write := range writes {
		totalSymbols += len(write.logicalSymbols)
	}
	go func() {
		for _, write := range writes {
			if write.delay > 0 {
				time.Sleep(write.delay)
			}
			_, _ = server.Write(enhReceivedBytes(wireEscapeSymbols(write.logicalSymbols)))
		}
	}()

	waitForCondition(t, 2*time.Second, func() bool {
		return reconstructor.Snapshot().TapStatus.ObservedSymbolCount >= uint64(totalSymbols)
	})

	classified := collectPassiveClassifiedEvents(subscription, 200*time.Millisecond)
	return proxyObserverHarnessResult{
		snapshot:   reconstructor.Snapshot(),
		classified: classified,
	}
}
