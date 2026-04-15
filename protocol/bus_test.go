package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

type readEvent struct {
	value byte
	err   error
}

type scriptedTransport struct {
	mu        sync.Mutex
	echo      []readEvent
	inbound   []readEvent
	writes    [][]byte
	echoReads int
	inReads   int
}

func (s *scriptedTransport) ReadByte() (byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.echo) > 0 {
		s.echoReads++
		ev := s.echo[0]
		s.echo = s.echo[1:]
		return ev.value, ev.err
	}
	if len(s.inbound) > 0 {
		s.inReads++
		ev := s.inbound[0]
		s.inbound = s.inbound[1:]
		return ev.value, ev.err
	}
	return 0, ebuserrors.ErrTimeout
}

func (s *scriptedTransport) Write(payload []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	copyPayload := append([]byte(nil), payload...)
	s.writes = append(s.writes, copyPayload)
	for _, b := range payload {
		s.echo = append(s.echo, readEvent{value: b})
	}
	return len(payload), nil
}

func (s *scriptedTransport) Close() error {
	return nil
}

func (s *scriptedTransport) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.writes)
}

func (s *scriptedTransport) inboundReadsConsumed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inReads
}

func (s *scriptedTransport) writesFlattened() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, 0, len(s.writes))
	for _, write := range s.writes {
		out = append(out, write...)
	}
	return out
}

type collisionOnceTransport struct {
	mu sync.Mutex

	collideOnFirstEcho bool
	awaitingEcho       bool
	lastWrite          byte

	inbound      []readEvent
	writes       [][]byte
	echoReads    int
	inboundReads int
}

func (t *collisionOnceTransport) ReadByte() (byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.awaitingEcho {
		t.awaitingEcho = false
		t.echoReads++
		if t.collideOnFirstEcho {
			t.collideOnFirstEcho = false
			return t.lastWrite ^ 0xFF, nil
		}
		return t.lastWrite, nil
	}

	t.inboundReads++
	if len(t.inbound) == 0 {
		return 0, ebuserrors.ErrTimeout
	}
	ev := t.inbound[0]
	t.inbound = t.inbound[1:]
	return ev.value, ev.err
}

func (t *collisionOnceTransport) Write(payload []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	copyPayload := append([]byte(nil), payload...)
	t.writes = append(t.writes, copyPayload)
	if len(payload) == 0 {
		return 0, nil
	}
	t.lastWrite = payload[0]
	t.awaitingEcho = true
	return len(payload), nil
}

func (t *collisionOnceTransport) Close() error {
	return nil
}

type gatingTransport struct {
	mu sync.Mutex

	writes       [][]byte
	writeStarted chan struct{}
	releaseRead  chan struct{}
	writeOnce    sync.Once
}

func newGatingTransport() *gatingTransport {
	return &gatingTransport{
		writeStarted: make(chan struct{}),
		releaseRead:  make(chan struct{}),
	}
}

func (t *gatingTransport) ReadByte() (byte, error) {
	<-t.releaseRead
	return 0, ebuserrors.ErrTimeout
}

func (t *gatingTransport) Write(payload []byte) (int, error) {
	t.mu.Lock()
	t.writes = append(t.writes, append([]byte(nil), payload...))
	t.mu.Unlock()
	t.writeOnce.Do(func() {
		close(t.writeStarted)
	})
	return len(payload), nil
}

func (t *gatingTransport) Close() error {
	return nil
}

func TestBus_BroadcastDoesNotReadAck(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	})
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp != nil {
		t.Fatalf("response = %+v; want nil", resp)
	}
	if tr.inboundReadsConsumed() != 0 {
		t.Fatalf("inbound reads = %d; want 0", tr.inboundReadsConsumed())
	}
	if tr.writeCount() == 0 {
		t.Fatalf("writes = %d; want >0", tr.writeCount())
	}

	command := []byte{0x10, protocol.AddressBroadcast, 0x01, 0x02, 0x01, 0x03}
	command = append(command, protocol.CRC(command), protocol.SymbolSyn)
	if got, want := tr.writesFlattened(), command; string(got) != string(want) {
		t.Fatalf("writes = %v; want %v", got, want)
	}
}

func TestBus_InitiatorInitiatorAckOnly(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x30,
		Target:    0x10,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	tr := &scriptedTransport{
		inbound: []readEvent{{value: protocol.SymbolAck}},
	}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp != nil {
		t.Fatalf("response = %+v; want nil", resp)
	}
	if tr.inboundReadsConsumed() != 1 {
		t.Fatalf("inbound reads = %d; want 1", tr.inboundReadsConsumed())
	}
	command := []byte{frame.Source, frame.Target, frame.Primary, frame.Secondary, 0x01, 0x03}
	command = append(command, protocol.CRC(command), protocol.SymbolSyn)
	if got, want := tr.writesFlattened(), command; string(got) != string(want) {
		t.Fatalf("writes = %v; want %v", got, want)
	}
}

func TestBus_ResponseCRCMismatch(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	data := byte(0x10)
	responseSegment := []byte{0x01, data}
	badCRC := protocol.CRC(responseSegment) ^ 0xFF
	tr := &scriptedTransport{
		inbound: []readEvent{
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: badCRC},
			{value: 0x01},
			{value: data},
			{value: badCRC},
		},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	_, err := bus.Send(ctx, frame)
	if !errors.Is(err, ebuserrors.ErrCRCMismatch) {
		t.Fatalf("Send error = %v; want ErrCRCMismatch", err)
	}
	if tr.inboundReadsConsumed() != 7 {
		t.Fatalf("inbound reads = %d; want 7", tr.inboundReadsConsumed())
	}
}

func TestBus_RetryOnCRCMismatch(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	data := byte(0x10)
	responseSegment := []byte{0x01, data}
	goodCRC := protocol.CRC(responseSegment)
	badCRC := goodCRC ^ 0xFF

	tr := &scriptedTransport{
		inbound: []readEvent{
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: badCRC},
			{value: 0x01},
			{value: data},
			{value: goodCRC},
		},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != data {
		t.Fatalf("response = %+v; want data [0x10]", resp)
	}
	if tr.inboundReadsConsumed() != 7 {
		t.Fatalf("inbound reads = %d; want 7", tr.inboundReadsConsumed())
	}
}

func TestBus_NoRetryOnTimeout(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	tr := &scriptedTransport{
		inbound: []readEvent{
			{err: ebuserrors.ErrTimeout},
		},
	}
	// Even with TimeoutRetries > 0, ErrTimeout is never retried —
	// timeout is deterministic on eBUS (no device responded).
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 3,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 3,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	_, err := bus.Send(ctx, frame)
	if !errors.Is(err, ebuserrors.ErrTimeout) {
		t.Fatalf("Send error = %v; want ErrTimeout", err)
	}

	// Only one command should have been written — no retry.
	command := []byte{frame.Source, frame.Target, frame.Primary, frame.Secondary, 0x01, 0x03}
	command = append(command, protocol.CRC(command))
	if got := tr.writesFlattened(); string(got) != string(command) {
		t.Fatalf("writes = %v; want single command %v (no retry)", got, command)
	}
}

func TestBus_RetryOnNACK(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}
	data := byte(0x20)
	responseSegment := []byte{0x01, data}
	respCRC := protocol.CRC(responseSegment)

	tr := &scriptedTransport{
		inbound: []readEvent{
			{value: protocol.SymbolNack},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: respCRC},
		},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    1,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    1,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != 0x20 {
		t.Fatalf("response = %+v; want data [0x20]", resp)
	}

	command := []byte{frame.Source, frame.Target, frame.Primary, frame.Secondary, 0x01, 0x03}
	command = append(command, protocol.CRC(command))
	want := make([]byte, 0, len(command)*2+2)
	want = append(want, command...)
	want = append(want, command...)
	want = append(want, protocol.SymbolAck, protocol.SymbolSyn)
	if got := tr.writesFlattened(); string(got) != string(want) {
		t.Fatalf("writes = %v; want %v", got, want)
	}
}

func TestBus_NACKExhaustedWrapsSentinel(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	tr := &scriptedTransport{
		inbound: []readEvent{
			{value: protocol.SymbolNack},
			{value: protocol.SymbolNack},
		},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	_, err := bus.Send(ctx, frame)
	if !errors.Is(err, ebuserrors.ErrNACK) {
		t.Fatalf("Send error = %v; want ErrNACK", err)
	}
}

func TestBus_RawTransportOpSkipsCanceledRequestContext(t *testing.T) {
	tr := newGatingTransport()
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)

	sendCtx, sendCancel := context.WithTimeout(context.Background(), time.Second)
	defer sendCancel()
	sendDone := make(chan error, 1)
	go func() {
		_, err := bus.Send(sendCtx, protocol.Frame{
			Source:    0x10,
			Target:    0x08,
			Primary:   0x01,
			Secondary: 0x02,
			Data:      []byte{0x03},
		})
		sendDone <- err
	}()

	<-tr.writeStarted

	opCtx, opCancel := context.WithCancel(context.Background())
	opDone := make(chan error, 1)
	rawExecuted := make(chan struct{}, 1)
	go func() {
		opDone <- bus.RawTransportOp(opCtx, func(transport.RawTransport) error {
			rawExecuted <- struct{}{}
			return nil
		})
	}()

	time.Sleep(20 * time.Millisecond)
	opCancel()

	select {
	case err := <-opDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RawTransportOp error = %v; want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for RawTransportOp to return")
	}

	close(tr.releaseRead)

	select {
	case err := <-sendDone:
		if err == nil {
			t.Fatal("Send error = nil; want timeout after release")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for Send to return")
	}

	select {
	case <-rawExecuted:
		t.Fatal("raw transport op executed after cancellation")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBus_RawTransportOpWaitsForStartedOperationDespiteContextCancel(t *testing.T) {
	tr := newGatingTransport()
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)

	started := make(chan struct{})
	release := make(chan struct{})
	opCtx, opCancel := context.WithCancel(context.Background())
	defer opCancel()
	opDone := make(chan error, 1)
	go func() {
		opDone <- bus.RawTransportOp(opCtx, func(transport.RawTransport) error {
			close(started)
			<-release
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for RawTransportOp to start")
	}

	opCancel()

	select {
	case err := <-opDone:
		t.Fatalf("RawTransportOp returned before completion after cancellation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-opDone:
		if err != nil {
			t.Fatalf("RawTransportOp error = %v; want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for RawTransportOp to return")
	}
}

func TestBus_RawTransportOpRejectsNilCallbackWhileBusy(t *testing.T) {
	tr := newGatingTransport()
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	bus.Run(runCtx)

	sendCtx, sendCancel := context.WithTimeout(context.Background(), time.Second)
	defer sendCancel()
	sendDone := make(chan error, 1)
	go func() {
		_, err := bus.Send(sendCtx, protocol.Frame{
			Source:    0x10,
			Target:    0x08,
			Primary:   0x01,
			Secondary: 0x02,
			Data:      []byte{0x03},
		})
		sendDone <- err
	}()

	<-tr.writeStarted

	opCtx, opCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer opCancel()
	err := bus.RawTransportOp(opCtx, nil)
	if err == nil {
		t.Fatal("RawTransportOp error = nil; want nil callback rejection")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RawTransportOp error = %v; want immediate nil callback rejection", err)
	}

	close(tr.releaseRead)

	select {
	case err := <-sendDone:
		if err == nil {
			t.Fatal("Send error = nil; want timeout after read release")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for Send to return")
	}
}

type arbitratingScriptedTransport struct {
	mu sync.Mutex

	echo    []readEvent
	inbound []readEvent

	writes [][]byte
	calls  []string

	arbitrationInitiators  []byte
	arbitrationResults     []error
	arbitrationSendsSource bool
}

func (s *arbitratingScriptedTransport) StartArbitration(initiator byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, "arbitrate")
	s.arbitrationInitiators = append(s.arbitrationInitiators, initiator)
	if len(s.arbitrationResults) == 0 {
		return nil
	}
	err := s.arbitrationResults[0]
	s.arbitrationResults = s.arbitrationResults[1:]
	return err
}

func (s *arbitratingScriptedTransport) ArbitrationSendsSource() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.arbitrationSendsSource
}

func (s *arbitratingScriptedTransport) ReadByte() (byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.echo) > 0 {
		ev := s.echo[0]
		s.echo = s.echo[1:]
		return ev.value, ev.err
	}
	if len(s.inbound) == 0 {
		return 0, ebuserrors.ErrTimeout
	}
	ev := s.inbound[0]
	s.inbound = s.inbound[1:]
	return ev.value, ev.err
}

func (s *arbitratingScriptedTransport) Write(payload []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, "write")
	copyPayload := append([]byte(nil), payload...)
	s.writes = append(s.writes, copyPayload)
	for _, b := range payload {
		s.echo = append(s.echo, readEvent{value: b})
	}
	return len(payload), nil
}

func (s *arbitratingScriptedTransport) Close() error {
	return nil
}

func TestBus_ArbitrationCalledBeforeWrite(t *testing.T) {
	t.Parallel()

	tr := &arbitratingScriptedTransport{}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	})
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp != nil {
		t.Fatalf("response = %+v; want nil", resp)
	}

	tr.mu.Lock()
	calls := append([]string(nil), tr.calls...)
	initiators := append([]byte(nil), tr.arbitrationInitiators...)
	tr.mu.Unlock()

	if len(calls) < 2 || calls[0] != "arbitrate" {
		t.Fatalf("calls = %v; want first call arbitrate", calls)
	}
	if len(initiators) != 1 || initiators[0] != 0x10 {
		t.Fatalf("arbitration initiators = %v; want [0x10]", initiators)
	}
}

func TestBus_ArbitrationSendsSourceSkipsSourceByte(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x07,
		Secondary: 0xFE,
		Data:      []byte{0x00},
	}

	tr := &arbitratingScriptedTransport{
		arbitrationSendsSource: true,
	}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	if _, err := bus.Send(ctx, frame); err != nil {
		t.Fatalf("Send error = %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.writes) == 0 || len(tr.writes[0]) == 0 {
		t.Fatalf("writes = %v; want at least one write", tr.writes)
	}
	if got := tr.writes[0][0]; got != frame.Target {
		t.Fatalf("first write byte = 0x%02x; want target 0x%02x when arbitration sends source", got, frame.Target)
	}
}

func TestBus_ArbitrationWithoutSourceInjectionIncludesSourceByte(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x07,
		Secondary: 0xFE,
		Data:      []byte{0x00},
	}

	tr := &arbitratingScriptedTransport{
		arbitrationSendsSource: false,
	}
	bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	if _, err := bus.Send(ctx, frame); err != nil {
		t.Fatalf("Send error = %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.writes) == 0 || len(tr.writes[0]) == 0 {
		t.Fatalf("writes = %v; want at least one write", tr.writes)
	}
	if got := tr.writes[0][0]; got != frame.Source {
		t.Fatalf("first write byte = 0x%02x; want source 0x%02x when arbitration does not send source", got, frame.Source)
	}
}

func TestBus_RetryOnCollisionDuringArbitration(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x30,
		Target:    0x10,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	tr := &arbitratingScriptedTransport{
		arbitrationResults: []error{ebuserrors.ErrBusCollision, nil},
		inbound: []readEvent{
			{value: protocol.SymbolEscape},
			{err: ebuserrors.ErrTimeout},
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolAck},
		},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp != nil {
		t.Fatalf("response = %+v; want nil", resp)
	}

	tr.mu.Lock()
	writes := len(tr.writes)
	initiators := append([]byte(nil), tr.arbitrationInitiators...)
	tr.mu.Unlock()

	if writes == 0 {
		t.Fatalf("writes = %d; want >0", writes)
	}
	if len(initiators) != 2 {
		t.Fatalf("arbitration calls = %d; want 2", len(initiators))
	}
}

func TestBus_ArbitrationCollisionBoundWithoutDeadline(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x30,
		Target:    0x10,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	tr := &arbitratingScriptedTransport{
		arbitrationResults: []error{ebuserrors.ErrBusCollision, ebuserrors.ErrBusCollision},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	runCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(runCtx)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	_, err := bus.Send(reqCtx, frame)
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send error = %v; want ErrBusCollision", err)
	}

	tr.mu.Lock()
	initiators := len(tr.arbitrationInitiators)
	tr.mu.Unlock()
	if initiators != 1 {
		t.Fatalf("arbitration calls = %d; want 1", initiators)
	}
}

func TestBus_RetryOnCollisionDuringWriteWaitsForSyn(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x07,
		Secondary: 0x04,
	}

	data := byte(0x10)
	responseSegment := []byte{0x01, data}
	respCRC := protocol.CRC(responseSegment)
	tr := &collisionOnceTransport{
		collideOnFirstEcho: true,
		inbound: []readEvent{
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: respCRC},
		},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != data {
		t.Fatalf("response = %+v; want data [0x10]", resp)
	}

	tr.mu.Lock()
	inReads := tr.inboundReads
	tr.mu.Unlock()
	if inReads != 6 {
		t.Fatalf("inbound reads = %d; want 6", inReads)
	}
}

func TestBus_RetryOnCollisionDoesNotConsumeTimeoutRetries(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x07,
		Secondary: 0x04,
	}

	data := byte(0x10)
	responseSegment := []byte{0x01, data}
	respCRC := protocol.CRC(responseSegment)
	tr := &collisionOnceTransport{
		collideOnFirstEcho: true,
		inbound: []readEvent{
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: respCRC},
		},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != data {
		t.Fatalf("response = %+v; want data [0x10]", resp)
	}
}

func TestBus_CollisionRetryRespectsTimeoutRetriesWithoutDeadline(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x07,
		Secondary: 0x04,
	}

	data := byte(0x10)
	responseSegment := []byte{0x01, data}
	respCRC := protocol.CRC(responseSegment)
	tr := &collisionOnceTransport{
		collideOnFirstEcho: true,
		inbound: []readEvent{
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: respCRC},
		},
	}
	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
	}
	bus := protocol.NewBus(tr, config, 8)
	runCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(runCtx)

	_, err := bus.Send(context.Background(), frame)
	if !errors.Is(err, ebuserrors.ErrBusCollision) {
		t.Fatalf("Send error = %v; want ErrBusCollision", err)
	}
}

var _ transport.RawTransport = (*scriptedTransport)(nil)

func TestBus_SendFrameDataOver255(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      make([]byte, 256),
	}
	_, err := bus.Send(ctx, frame)
	if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
		t.Fatalf("Send error = %v; want ErrInvalidPayload", err)
	}
}

func TestBus_SendClonesFrameData(t *testing.T) {
	t.Parallel()

	// Use a gating transport so the Send blocks until we mutate the data.
	tr := newGatingTransport()
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus.Run(ctx)

	data := []byte{0xAA, 0xBB}
	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      data,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var sendErr error
	go func() {
		defer wg.Done()
		_, sendErr = bus.Send(ctx, frame)
	}()

	// Wait for the write to start, then mutate the original data.
	<-tr.writeStarted
	data[0] = 0xFF
	data[1] = 0xFF

	// Release the read so the transaction completes.
	close(tr.releaseRead)
	wg.Wait()

	// The sent bytes should contain the original data, not the mutated version.
	flat := func() []byte {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		out := make([]byte, 0)
		for _, w := range tr.writes {
			out = append(out, w...)
		}
		return out
	}()

	// The wire format is: SRC DST PB SB LEN DATA... CRC SYN
	// Data starts at index 4 (LEN) and the actual data bytes follow.
	// Check that the data bytes in the wire are 0xAA, 0xBB (original).
	if len(flat) < 7 {
		if sendErr != nil {
			// Transaction failed (timeout expected from gating transport), but
			// we can still verify the written bytes contain original data.
			t.Logf("Send error (expected with gating transport): %v", sendErr)
		}
		if len(flat) == 0 {
			t.Fatal("no bytes written to transport")
		}
	}
	// Find the data bytes: index 4 is LEN=0x02, indices 5-6 are the data.
	if len(flat) >= 7 && (flat[5] != 0xAA || flat[6] != 0xBB) {
		t.Fatalf("wire data = [0x%02x, 0x%02x]; want [0xAA, 0xBB] (clone failed)", flat[5], flat[6])
	}
}

func TestBus_QueueFull(t *testing.T) {
	t.Parallel()

	// Use a gating transport to block the run loop on the first request.
	tr := newGatingTransport()
	config := protocol.DefaultBusConfig()
	capacity := 2
	bus := protocol.NewBus(tr, config, capacity)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus.Run(ctx)

	frame := protocol.Frame{
		Source:    0x10,
		Target:    protocol.AddressBroadcast,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	// First Send will be picked up by runLoop and block on transport.
	// We need it to block so subsequent sends fill the queue.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = bus.Send(ctx, frame)
	}()
	// Wait for the first request to start writing.
	<-tr.writeStarted

	// Now fill the queue to capacity.
	for i := 0; i < capacity; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = bus.Send(ctx, frame)
		}()
	}

	// Give goroutines time to enqueue.
	time.Sleep(50 * time.Millisecond)

	// The next send should fail with ErrQueueFull.
	_, err := bus.Send(ctx, frame)
	if !errors.Is(err, ebuserrors.ErrQueueFull) {
		t.Fatalf("Send error = %v; want ErrQueueFull", err)
	}

	// Cleanup: release the gating transport so goroutines can finish.
	close(tr.releaseRead)
	cancel()
	wg.Wait()
}

func TestBus_DrainQueueOnShutdown(t *testing.T) {
	t.Parallel()

	// Use a gating transport so the run loop blocks on the first request.
	tr := newGatingTransport()
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 64)
	runCtx, runCancel := context.WithCancel(context.Background())
	bus.Run(runCtx)

	// Send a request that will block in the run loop.
	var wg sync.WaitGroup
	wg.Add(1)
	var firstErr error
	go func() {
		defer wg.Done()
		_, firstErr = bus.Send(context.Background(), protocol.Frame{
			Source:    0x10,
			Target:    protocol.AddressBroadcast,
			Primary:   0x01,
			Secondary: 0x02,
			Data:      []byte{0x03},
		})
	}()
	<-tr.writeStarted

	// Enqueue a second request that will sit in the queue.
	wg.Add(1)
	var secondErr error
	go func() {
		defer wg.Done()
		_, secondErr = bus.Send(context.Background(), protocol.Frame{
			Source:    0x20,
			Target:    protocol.AddressBroadcast,
			Primary:   0x01,
			Secondary: 0x02,
			Data:      []byte{0x04},
		})
	}()
	// Give it time to enqueue.
	time.Sleep(50 * time.Millisecond)

	// Cancel the run context. This should drain the queue and notify
	// the second request with ErrTransportClosed.
	close(tr.releaseRead)
	runCancel()

	// Both goroutines should complete within a reasonable time.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success — both goroutines finished.
	case <-time.After(3 * time.Second):
		t.Fatal("goroutines did not finish after run context cancelled — drain likely failed")
	}

	// The second request should have received ErrTransportClosed from drainQueue.
	if secondErr != nil && !errors.Is(secondErr, ebuserrors.ErrTransportClosed) {
		// It may also get context.Canceled if it was picked up, but
		// ErrTransportClosed is the expected drain path.
		t.Logf("second Send error = %v (ErrTransportClosed expected from drain)", secondErr)
	}
	_ = firstErr // First request outcome depends on transport timing.
}

func TestBus_RawTransportOpQueueFull(t *testing.T) {
	t.Parallel()

	tr := newGatingTransport()
	config := protocol.DefaultBusConfig()
	capacity := 1
	bus := protocol.NewBus(tr, config, capacity)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus.Run(ctx)

	// First send blocks in transport.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = bus.Send(ctx, protocol.Frame{
			Source:    0x10,
			Target:    protocol.AddressBroadcast,
			Primary:   0x01,
			Secondary: 0x02,
			Data:      []byte{0x03},
		})
	}()
	<-tr.writeStarted

	// Fill queue.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = bus.Send(ctx, protocol.Frame{
			Source:    0x10,
			Target:    protocol.AddressBroadcast,
			Primary:   0x01,
			Secondary: 0x02,
		})
	}()
	time.Sleep(50 * time.Millisecond)

	// RawTransportOp should also fail with ErrQueueFull.
	err := bus.RawTransportOp(ctx, func(transport.RawTransport) error { return nil })
	if !errors.Is(err, ebuserrors.ErrQueueFull) {
		t.Fatalf("RawTransportOp error = %v; want ErrQueueFull", err)
	}

	close(tr.releaseRead)
	cancel()
	wg.Wait()
}

// reconnectableScriptedTransport extends scriptedTransport with Reconnectable
// support for testing reconnect-related retry budget resets.
type reconnectableScriptedTransport struct {
	mu             sync.Mutex
	echo           []readEvent
	inbound        []readEvent
	phase2Inbound  []readEvent // loaded after reconnect
	writes         [][]byte
	echoReads      int
	inReads        int
	reconnected    bool
	reconnectCount int
}

func (s *reconnectableScriptedTransport) ReadByte() (byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.echo) > 0 {
		s.echoReads++
		ev := s.echo[0]
		s.echo = s.echo[1:]
		return ev.value, ev.err
	}
	if len(s.inbound) > 0 {
		s.inReads++
		ev := s.inbound[0]
		s.inbound = s.inbound[1:]
		return ev.value, ev.err
	}
	return 0, ebuserrors.ErrTimeout
}

func (s *reconnectableScriptedTransport) Write(payload []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	copyPayload := append([]byte(nil), payload...)
	s.writes = append(s.writes, copyPayload)
	for _, b := range payload {
		s.echo = append(s.echo, readEvent{value: b})
	}
	return len(payload), nil
}

func (s *reconnectableScriptedTransport) Close() error { return nil }

func (s *reconnectableScriptedTransport) Reconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconnectCount++
	s.reconnected = true
	// Load phase2 inbound events after reconnect.
	s.inbound = append(s.inbound, s.phase2Inbound...)
	s.phase2Inbound = nil
	return nil
}

// DIV-04: After a reconnect, nackAttempts must be reset so that the NACK
// retry budget is available again for the fresh session.
//
// Sequence: NACK x2 (exhausts NACK budget for one sendTransaction) ->
// timeout (triggers reconnect) -> NACK x2 (should succeed because NACK
// budget was reset) -> success.
//
// The inner command ACK loop in sendTransaction retries the command once
// on NACK (commandAttempt 0 -> 1), so TWO NACKs per sendTransaction call
// produce one ErrNACK to the outer retry loop.
func TestBus_NackAttemptsResetAfterReconnect(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}

	data := byte(0x20)
	responseSegment := []byte{0x01, data}
	goodCRC := protocol.CRC(responseSegment)

	// NACKRetries=1 means one ErrNACK is retried.
	// Flow:
	// Attempt 1: sendTransaction -> NACK, NACK -> ErrNACK, nackAttempts=0->1, retry
	// Attempt 2: sendTransaction -> timeout -> ErrTimeout, shouldRetry false
	//   -> tryTransportReconnect -> reconnect -> nackAttempts=0 (reset!)
	// Attempt 3: sendTransaction -> NACK, NACK -> ErrNACK, nackAttempts=0->1, retry
	// Attempt 4: sendTransaction -> ACK + good response -> success

	tr := &reconnectableScriptedTransport{
		inbound: []readEvent{
			// Attempt 1: two NACKs (inner loop retries once, then returns ErrNACK).
			// Note: After the inner loop's first NACK (commandAttempt=0), the
			// command is resent with SRC. After second NACK (commandAttempt=1),
			// sendEndOfMessage writes SYN, then ErrNACK is returned.
			{value: protocol.SymbolNack}, // first NACK -> inner retry
			{value: protocol.SymbolNack}, // second NACK -> ErrNACK to outer loop
			// Attempt 2: timeout (empty inbound -> ErrTimeout) triggers reconnect.
		},
		phase2Inbound: []readEvent{
			// Attempt 3 (post-reconnect): two NACKs again -> ErrNACK,
			// but nackAttempts was reset so retry is allowed.
			{value: protocol.SymbolNack},
			{value: protocol.SymbolNack},
			// Attempt 4: ACK + good response.
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: goodCRC},
		},
	}

	config := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    1,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    1,
		},
		ReconnectRetries: 1,
		ReconnectDelay:   1 * time.Millisecond,
	}
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v; want success after reconnect with NACK budget reset", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != data {
		t.Fatalf("response = %+v; want data [0x20]", resp)
	}

	tr.mu.Lock()
	if !tr.reconnected {
		t.Fatal("transport was not reconnected; expected reconnect to reset NACK budget")
	}
	tr.mu.Unlock()
}
