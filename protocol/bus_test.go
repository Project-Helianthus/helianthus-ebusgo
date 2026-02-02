package protocol_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
	"github.com/d3vi1/helianthus-ebusgo/protocol"
	"github.com/d3vi1/helianthus-ebusgo/transport"
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

func TestBus_MasterMasterAckOnly(t *testing.T) {
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
	length := byte(0x01)
	badCRC := protocol.CRC([]byte{length, data}) ^ 0xFF
	tr := &scriptedTransport{
		inbound: []readEvent{
			{value: protocol.SymbolAck},
			{value: length},
			{value: data},
			{value: badCRC},
			{value: length},
			{value: data},
			{value: badCRC},
		},
	}
	config := protocol.BusConfig{
		MasterSlave: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		MasterMaster: protocol.RetryPolicy{
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
	goodCRC := protocol.CRC([]byte{0x01, data})
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
		MasterSlave: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
		MasterMaster: protocol.RetryPolicy{
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

func TestBus_RetryOnTimeout(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}
	data := byte(0x10)
	respCRC := protocol.CRC([]byte{0x01, data})

	tr := &scriptedTransport{
		inbound: []readEvent{
			{err: ebuserrors.ErrTimeout},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: respCRC},
		},
	}
	config := protocol.BusConfig{
		MasterSlave: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
		MasterMaster: protocol.RetryPolicy{
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
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != 0x10 {
		t.Fatalf("response = %+v; want data [0x10]", resp)
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
	respCRC := protocol.CRC([]byte{0x01, data})

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
		MasterSlave: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    1,
		},
		MasterMaster: protocol.RetryPolicy{
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
		MasterSlave: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		MasterMaster: protocol.RetryPolicy{
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

type arbitratingScriptedTransport struct {
	mu sync.Mutex

	echo    []readEvent
	inbound []readEvent

	writes [][]byte
	calls  []string

	arbitrationMasters []byte
	arbitrationResults []error
}

func (s *arbitratingScriptedTransport) StartArbitration(master byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, "arbitrate")
	s.arbitrationMasters = append(s.arbitrationMasters, master)
	if len(s.arbitrationResults) == 0 {
		return nil
	}
	err := s.arbitrationResults[0]
	s.arbitrationResults = s.arbitrationResults[1:]
	return err
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
	masters := append([]byte(nil), tr.arbitrationMasters...)
	tr.mu.Unlock()

	if len(calls) < 2 || calls[0] != "arbitrate" {
		t.Fatalf("calls = %v; want first call arbitrate", calls)
	}
	if len(masters) != 1 || masters[0] != 0x10 {
		t.Fatalf("arbitration masters = %v; want [0x10]", masters)
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
		inbound:           []readEvent{{value: protocol.SymbolAck}},
	}
	config := protocol.BusConfig{
		MasterSlave: protocol.RetryPolicy{
			TimeoutRetries: 0,
			NACKRetries:    0,
		},
		MasterMaster: protocol.RetryPolicy{
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
	masters := append([]byte(nil), tr.arbitrationMasters...)
	tr.mu.Unlock()

	if writes == 0 {
		t.Fatalf("writes = %d; want >0", writes)
	}
	if len(masters) != 2 {
		t.Fatalf("arbitration calls = %d; want 2", len(masters))
	}
}

var _ transport.RawTransport = (*scriptedTransport)(nil)
