package protocol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type scriptedJoinBus struct {
	mu           sync.Mutex
	listenCalls  int
	inquiryCalls int
	listenFrames [][]Frame
	inquiryErr   error
}

func (b *scriptedJoinBus) Listen(ctx context.Context, onFrame func(Frame)) error {
	b.mu.Lock()
	index := b.listenCalls
	b.listenCalls++
	var frames []Frame
	if index < len(b.listenFrames) {
		frames = append(frames, b.listenFrames[index]...)
	}
	b.mu.Unlock()

	for _, frame := range frames {
		onFrame(frame)
	}

	<-ctx.Done()
	return ctx.Err()
}

func (b *scriptedJoinBus) InquiryExistence(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inquiryCalls++
	return b.inquiryErr
}

type memoryJoinStateStore struct {
	loadValue byte
	loadErr   error
	saveErr   error
	saves     []byte
}

func nilContext() context.Context {
	return nil
}

func (s *memoryJoinStateStore) LoadInitiator(context.Context) (byte, error) {
	return s.loadValue, s.loadErr
}

func (s *memoryJoinStateStore) SaveInitiator(_ context.Context, initiator byte) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saves = append(s.saves, initiator)
	return nil
}

func TestJoiner_NoTrafficSelectsHighestInitiator(t *testing.T) {
	t.Parallel()

	joiner := NewJoiner(&scriptedJoinBus{}, nil, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(context.Background())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}

	if result.Initiator != 0xFF {
		t.Fatalf("Initiator = 0x%02x; want 0xff", result.Initiator)
	}
	if result.CompanionTarget != 0x04 {
		t.Fatalf("CompanionTarget = 0x%02x; want 0x04", result.CompanionTarget)
	}
}

func TestJoiner_ObservedInitiatorsAreSkipped(t *testing.T) {
	t.Parallel()

	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{
			{
				{Source: 0xFF, Target: 0xFE},
				{Source: 0x31, Target: 0xFE},
				{Source: 0x7F, Target: 0xFE},
			},
		},
	}
	joiner := NewJoiner(bus, nil, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(context.Background())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}
	if result.Initiator != 0xF7 {
		t.Fatalf("Initiator = 0x%02x; want 0xf7", result.Initiator)
	}
}

func TestJoiner_RejectsCandidateWhenCompanionTargetLooksOccupiedBySource(t *testing.T) {
	t.Parallel()

	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{
			{
				{Source: 0x04, Target: 0x15},
				{Source: 0x04, Target: 0x26},
			},
		},
	}
	joiner := NewJoiner(bus, nil, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(context.Background())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}
	if result.Initiator != 0xF7 {
		t.Fatalf("Initiator = 0x%02x; want 0xf7", result.Initiator)
	}
	reasons := result.Metrics.RejectionReasons[0xFF]
	if len(reasons) == 0 {
		t.Fatalf("RejectionReasons[0xff] is empty; want companion-target reason")
	}
}

func TestJoiner_RejectsCandidateWhenCompanionTargetIsFrequentlyAddressed(t *testing.T) {
	t.Parallel()

	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{
			{
				{Source: 0x31, Target: 0x04},
				{Source: 0x1F, Target: 0x04},
			},
		},
	}
	joiner := NewJoiner(bus, nil, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(context.Background())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}
	if result.Initiator != 0xF7 {
		t.Fatalf("Initiator = 0x%02x; want 0xf7", result.Initiator)
	}
	reasons := result.Metrics.RejectionReasons[0xFF]
	if len(reasons) == 0 {
		t.Fatalf("RejectionReasons[0xff] is empty; want companion-target reason")
	}
}

func TestJoiner_AllInitiatorsObservedReturnsError(t *testing.T) {
	t.Parallel()

	frames := make([]Frame, 0, len(initiatorAddressAscending))
	for _, address := range initiatorAddressAscending {
		frames = append(frames, Frame{Source: address, Target: 0xFE})
	}
	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{frames},
	}
	joiner := NewJoiner(bus, nil, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	_, err := joiner.Join(context.Background())
	var noFreeErr *ErrNoFreeInitiatorAddress
	if !errors.As(err, &noFreeErr) {
		t.Fatalf("Join error = %v; want ErrNoFreeInitiatorAddress", err)
	}
	if len(noFreeErr.ObservedInitiators) != len(initiatorAddressAscending) {
		t.Fatalf("ObservedInitiators = %d; want %d", len(noFreeErr.ObservedInitiators), len(initiatorAddressAscending))
	}
}

func TestJoiner_PersistedInitiatorIsPreferredWhenSafe(t *testing.T) {
	t.Parallel()

	store := &memoryJoinStateStore{loadValue: 0xF1}
	joiner := NewJoiner(&scriptedJoinBus{}, store, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(context.Background())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}
	if result.Initiator != 0xF1 {
		t.Fatalf("Initiator = 0x%02x; want 0xf1", result.Initiator)
	}
	if len(store.saves) != 1 || store.saves[0] != 0xF1 {
		t.Fatalf("saved initiators = %v; want [0xf1]", store.saves)
	}
}

func TestJoiner_PersistedInitiatorFallsBackWhenOccupied(t *testing.T) {
	t.Parallel()

	store := &memoryJoinStateStore{loadValue: 0xF1}
	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{
			{
				{Source: 0xF1, Target: 0xFE},
			},
		},
	}
	joiner := NewJoiner(bus, store, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(context.Background())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}
	if result.Initiator != 0xFF {
		t.Fatalf("Initiator = 0x%02x; want 0xff", result.Initiator)
	}
}

func TestJoiner_NilContextDefaultsToBackground(t *testing.T) {
	t.Parallel()

	joiner := NewJoiner(&scriptedJoinBus{}, nil, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(nilContext())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}
	if result.Initiator != 0xFF {
		t.Fatalf("Initiator = 0x%02x; want 0xff", result.Initiator)
	}
}

func TestJoiner_PropagatesInquiryCancellation(t *testing.T) {
	t.Parallel()

	bus := &scriptedJoinBus{inquiryErr: context.Canceled}
	joiner := NewJoiner(bus, nil, JoinConfig{
		ListenWarmup:       2 * time.Millisecond,
		PreferHighest:      true,
		InquiryEnabled:     true,
		InquiryMaxAttempts: 1,
	})

	_, err := joiner.Join(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Join error = %v; want context.Canceled", err)
	}
}
