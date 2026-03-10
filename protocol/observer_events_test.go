package protocol_test

import (
	"context"
	"sync"
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

type recordingObserver struct {
	mu           sync.Mutex
	events       []protocol.BusEvent
	failKind     protocol.BusEventKind
	failOnce     bool
	panickedOnce bool
}

func (o *recordingObserver) OnBusEvent(event protocol.BusEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.events = append(o.events, event)
	if event.Kind == o.failKind && o.failOnce {
		o.failOnce = false
		return ebuserrors.ErrInvalidPayload
	}
	if event.Kind == o.failKind && o.panickedOnce {
		o.panickedOnce = false
		panic("observer boom")
	}
	return nil
}

func (o *recordingObserver) snapshot() []protocol.BusEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]protocol.BusEvent(nil), o.events...)
}

func TestBus_ObserverErrorSurfacesFaultEvent(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}
	responseSegment := []byte{0x01, 0x10}
	observer := &recordingObserver{
		failKind: protocol.BusEventAttemptComplete,
		failOnce: true,
	}
	tr := &scriptedTransport{
		inbound: []readEvent{
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: 0x10},
			{value: protocol.CRC(responseSegment)},
		},
	}
	cfg := protocol.DefaultBusConfig()
	cfg.Observer = observer
	bus := protocol.NewBus(tr, cfg, 8)
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

	fault := bus.ObserverFaultSnapshot()
	if fault.Count == 0 {
		t.Fatalf("ObserverFaultSnapshot.Count = 0; want >0")
	}
	if fault.LastKind != protocol.BusEventAttemptComplete {
		t.Fatalf("ObserverFaultSnapshot.LastKind = %v; want %v", fault.LastKind, protocol.BusEventAttemptComplete)
	}

	events := observer.snapshot()
	if !containsEventKind(events, protocol.BusEventObserverFault) {
		t.Fatalf("observer events do not include BusEventObserverFault: %#v", events)
	}
}

func TestBus_ObserverPanicDoesNotKillBusLoop(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}
	observer := &recordingObserver{
		failKind:     protocol.BusEventRequestComplete,
		panickedOnce: true,
	}
	tr := &scriptedTransport{
		inbound: []readEvent{
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: 0x10},
			{value: protocol.CRC([]byte{0x01, 0x10})},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: 0x20},
			{value: protocol.CRC([]byte{0x01, 0x20})},
		},
	}
	cfg := protocol.DefaultBusConfig()
	cfg.Observer = observer
	bus := protocol.NewBus(tr, cfg, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	first, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("first Send error = %v", err)
	}
	if first == nil || first.Data[0] != 0x10 {
		t.Fatalf("first response = %+v; want data [0x10]", first)
	}

	second, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("second Send error = %v", err)
	}
	if second == nil || second.Data[0] != 0x20 {
		t.Fatalf("second response = %+v; want data [0x20]", second)
	}

	fault := bus.ObserverFaultSnapshot()
	if !fault.LastPanic {
		t.Fatalf("ObserverFaultSnapshot.LastPanic = false; want true")
	}
	if fault.Count == 0 {
		t.Fatalf("ObserverFaultSnapshot.Count = 0; want >0")
	}
}

func TestBus_AttemptAndRequestCompleteDurationsAcrossRetry(t *testing.T) {
	t.Parallel()

	frame := protocol.Frame{
		Source:    0x10,
		Target:    0x08,
		Primary:   0x01,
		Secondary: 0x02,
		Data:      []byte{0x03},
	}
	observer := &recordingObserver{}
	tr := &scriptedTransport{
		inbound: []readEvent{
			{err: ebuserrors.ErrTimeout},
			{value: protocol.SymbolAck},
			{value: 0x01},
			{value: 0x10},
			{value: protocol.CRC([]byte{0x01, 0x10})},
		},
	}
	cfg := protocol.BusConfig{
		InitiatorTarget: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
		InitiatorInitiator: protocol.RetryPolicy{
			TimeoutRetries: 1,
			NACKRetries:    0,
		},
		Observer: observer,
	}
	bus := protocol.NewBus(tr, cfg, 8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus.Run(ctx)

	resp, err := bus.Send(ctx, frame)
	if err != nil {
		t.Fatalf("Send error = %v", err)
	}
	if resp == nil || resp.Data[0] != 0x10 {
		t.Fatalf("response = %+v; want data [0x10]", resp)
	}

	events := observer.snapshot()
	attemptEvent := findEvent(events, protocol.BusEventAttemptComplete)
	requestEvent := findEvent(events, protocol.BusEventRequestComplete)
	retryEvent := findEvent(events, protocol.BusEventRetry)
	if attemptEvent == nil {
		t.Fatal("missing BusEventAttemptComplete")
	}
	if requestEvent == nil {
		t.Fatal("missing BusEventRequestComplete")
	}
	if retryEvent == nil {
		t.Fatal("missing BusEventRetry")
	}
	if attemptEvent.Attempt != 2 {
		t.Fatalf("attempt-complete Attempt = %d; want 2", attemptEvent.Attempt)
	}
	if requestEvent.Attempt != 2 {
		t.Fatalf("request-complete Attempt = %d; want 2", requestEvent.Attempt)
	}
	if requestEvent.TimeoutRetries != 1 {
		t.Fatalf("request-complete TimeoutRetries = %d; want 1", requestEvent.TimeoutRetries)
	}
	if retryEvent.Retry != protocol.BusRetryReasonTimeout {
		t.Fatalf("retry reason = %v; want %v", retryEvent.Retry, protocol.BusRetryReasonTimeout)
	}
	if requestEvent.DurationMicros < attemptEvent.DurationMicros {
		t.Fatalf("request-complete duration = %d; want >= attempt-complete duration %d", requestEvent.DurationMicros, attemptEvent.DurationMicros)
	}
}

func containsEventKind(events []protocol.BusEvent, kind protocol.BusEventKind) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func findEvent(events []protocol.BusEvent, kind protocol.BusEventKind) *protocol.BusEvent {
	for i := range events {
		if events[i].Kind == kind {
			return &events[i]
		}
	}
	return nil
}
