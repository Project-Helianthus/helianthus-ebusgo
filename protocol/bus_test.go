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
	reads     []readEvent
	writes    [][]byte
	readCount int
}

func (s *scriptedTransport) ReadByte() (byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.readCount++
	if len(s.reads) == 0 {
		return 0, ebuserrors.ErrTimeout
	}
	ev := s.reads[0]
	s.reads = s.reads[1:]
	return ev.value, ev.err
}

func (s *scriptedTransport) Write(payload []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	copyPayload := append([]byte(nil), payload...)
	s.writes = append(s.writes, copyPayload)
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

func (s *scriptedTransport) readsConsumed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readCount
}

func TestBus_BroadcastDoesNotReadAck(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{
		reads: []readEvent{
			{err: ebuserrors.ErrTimeout},
		},
	}
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
	if tr.readsConsumed() != 0 {
		t.Fatalf("reads = %d; want 0", tr.readsConsumed())
	}
	if tr.writeCount() != 1 {
		t.Fatalf("writes = %d; want 1", tr.writeCount())
	}
}

func TestBus_MasterMasterAckOnly(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{
		reads: []readEvent{
			{value: protocol.SymbolAck},
		},
	}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x30,
		Target:    0x10,
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
	if tr.readsConsumed() != 1 {
		t.Fatalf("reads = %d; want 1", tr.readsConsumed())
	}
	if tr.writeCount() != 1 {
		t.Fatalf("writes = %d; want 1", tr.writeCount())
	}
}

func TestBus_ReadAckSkipsSyn(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{
		reads: []readEvent{
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolAck},
		},
	}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x30,
		Target:    0x10,
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
	if tr.readsConsumed() != 3 {
		t.Fatalf("reads = %d; want 3", tr.readsConsumed())
	}
	if tr.writeCount() != 1 {
		t.Fatalf("writes = %d; want 1", tr.writeCount())
	}
}

func TestBus_ReadAckSkipsNoiseBytes(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{
		reads: []readEvent{
			{value: 0x10},
			{value: 0x55},
			{value: protocol.SymbolSyn},
			{value: protocol.SymbolAck},
		},
	}
	config := protocol.DefaultBusConfig()
	bus := protocol.NewBus(tr, config, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x30,
		Target:    0x10,
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
	if tr.readsConsumed() != 4 {
		t.Fatalf("reads = %d; want 4", tr.readsConsumed())
	}
	if tr.writeCount() != 1 {
		t.Fatalf("writes = %d; want 1", tr.writeCount())
	}
}

func TestBus_ResponseCRCMismatch(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{
		reads: []readEvent{
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: 0x10},
			{value: 0x00},
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

	_, err := bus.Send(ctx, protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	})
	if !errors.Is(err, ebuserrors.ErrCRCMismatch) {
		t.Fatalf("Send error = %v; want ErrCRCMismatch", err)
	}
	if tr.writeCount() != 1 {
		t.Fatalf("writes = %d; want 1", tr.writeCount())
	}
}

func TestBus_RetryOnCRCMismatch(t *testing.T) {
	t.Parallel()

	data := byte(0x10)
	goodCRC := protocol.CRC([]byte{0x01, data})
	badCRC := goodCRC ^ 0xFF

	tr := &scriptedTransport{
		reads: []readEvent{
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: data},
			{value: badCRC},
			{value: protocol.SymbolAck},
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

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	})
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != data {
		t.Fatalf("response = %+v; want data [0x10]", resp)
	}
	if tr.writeCount() != 2 {
		t.Fatalf("writes = %d; want 2", tr.writeCount())
	}
}

func TestBus_RetryOnTimeout(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{
		reads: []readEvent{
			{err: ebuserrors.ErrTimeout},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: 0x10},
			{value: protocol.CRC([]byte{0x01, 0x10})},
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

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	})
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != 0x10 {
		t.Fatalf("response = %+v; want data [0x10]", resp)
	}
	if tr.writeCount() != 2 {
		t.Fatalf("writes = %d; want 2", tr.writeCount())
	}
}

func TestBus_RetryOnNACK(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{
		reads: []readEvent{
			{value: protocol.SymbolNack},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: 0x20},
			{value: protocol.CRC([]byte{0x01, 0x20})},
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

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	})
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0] != 0x20 {
		t.Fatalf("response = %+v; want data [0x20]", resp)
	}
	if tr.writeCount() != 2 {
		t.Fatalf("writes = %d; want 2", tr.writeCount())
	}
}

func TestBus_NACKExhaustedWrapsSentinel(t *testing.T) {
	t.Parallel()

	tr := &scriptedTransport{
		reads: []readEvent{
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

	_, err := bus.Send(ctx, protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	})
	if !errors.Is(err, ebuserrors.ErrNACK) {
		t.Fatalf("Send error = %v; want ErrNACK", err)
	}
}

type arbitratingScriptedTransport struct {
	mu sync.Mutex

	reads []readEvent

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

	if len(s.reads) == 0 {
		return 0, ebuserrors.ErrTimeout
	}
	ev := s.reads[0]
	s.reads = s.reads[1:]
	return ev.value, ev.err
}

func (s *arbitratingScriptedTransport) Write(payload []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, "write")
	copyPayload := append([]byte(nil), payload...)
	s.writes = append(s.writes, copyPayload)
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

	if len(calls) != 2 || calls[0] != "arbitrate" || calls[1] != "write" {
		t.Fatalf("calls = %v; want [arbitrate write]", calls)
	}
	if len(masters) != 1 || masters[0] != 0x10 {
		t.Fatalf("arbitration masters = %v; want [0x10]", masters)
	}
}

func TestBus_RetryOnCollisionDuringArbitration(t *testing.T) {
	t.Parallel()

	tr := &arbitratingScriptedTransport{
		arbitrationResults: []error{ebuserrors.ErrBusCollision, nil},
		reads: []readEvent{
			{value: protocol.SymbolAck},
		},
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

	resp, err := bus.Send(ctx, protocol.Frame{
		Source:    0x30,
		Target:    0x10,
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
	writes := len(tr.writes)
	masters := append([]byte(nil), tr.arbitrationMasters...)
	tr.mu.Unlock()

	if writes != 1 {
		t.Fatalf("writes = %d; want 1", writes)
	}
	if len(masters) != 2 {
		t.Fatalf("arbitration calls = %d; want 2", len(masters))
	}
}

var _ transport.RawTransport = (*scriptedTransport)(nil)
