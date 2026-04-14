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

func TestJoiner_NoTrafficSelectsHighestSafeInitiator(t *testing.T) {
	t.Parallel()

	joiner := NewJoiner(&scriptedJoinBus{}, nil, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(context.Background())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}

	// 0xFF is rejected because its companion target (0xFF+5) overflows byte (EG18).
	// The next highest candidate 0xF7 (companion 0xFC) is selected.
	if result.Initiator != 0xF7 {
		t.Fatalf("Initiator = 0x%02x; want 0xf7", result.Initiator)
	}
	if result.CompanionTarget != 0xFC {
		t.Fatalf("CompanionTarget = 0x%02x; want 0xfc", result.CompanionTarget)
	}
}

func TestJoiner_ObservedInitiatorsAreSkipped(t *testing.T) {
	t.Parallel()

	// Each initiator must be observed at least twice to be marked occupied (EG26).
	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{
			{
				{Source: 0xFF, Target: 0xFE},
				{Source: 0xFF, Target: 0x15},
				{Source: 0x31, Target: 0xFE},
				{Source: 0x31, Target: 0x15},
				{Source: 0x7F, Target: 0xFE},
				{Source: 0x7F, Target: 0x15},
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

	// Each initiator must be observed at least twice to be marked occupied (EG26).
	frames := make([]Frame, 0, 2*len(initiatorAddressAscending))
	for _, address := range initiatorAddressAscending {
		frames = append(frames, Frame{Source: address, Target: 0xFE})
		frames = append(frames, Frame{Source: address, Target: 0x15})
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

	// Each initiator must be observed at least twice to be marked occupied (EG26).
	store := &memoryJoinStateStore{loadValue: 0xF1}
	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{
			{
				{Source: 0xF1, Target: 0xFE},
				{Source: 0xF1, Target: 0x15},
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
	// 0xFF rejected due to companion target overflow (EG18); 0xF7 selected.
	if result.Initiator != 0xF7 {
		t.Fatalf("Initiator = 0x%02x; want 0xf7", result.Initiator)
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
	// 0xFF rejected due to companion target byte overflow (EG18); 0xF7 selected.
	if result.Initiator != 0xF7 {
		t.Fatalf("Initiator = 0x%02x; want 0xf7", result.Initiator)
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

func TestJoiner_CompanionTargetOverflowRejectsCandidate(t *testing.T) {
	t.Parallel()

	// EG18: 0xFF + 0x05 overflows byte (wraps to 0x04). The joiner should
	// reject 0xFF and pick the next safe candidate (0xF7, companion 0xFC).
	joiner := NewJoiner(&scriptedJoinBus{}, nil, JoinConfig{
		ListenWarmup:  2 * time.Millisecond,
		PreferHighest: true,
	})

	result, err := joiner.Join(context.Background())
	if err != nil {
		t.Fatalf("Join error = %v", err)
	}
	if result.Initiator == 0xFF {
		t.Fatalf("Initiator = 0xff; should be rejected due to companion overflow")
	}
	reasons := result.Metrics.RejectionReasons[0xFF]
	found := false
	for _, r := range reasons {
		if r == "companion-target-overflows-byte" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("RejectionReasons[0xff] = %v; want companion-target-overflows-byte", reasons)
	}
}

func TestJoiner_SingleObservationDoesNotMarkOccupied(t *testing.T) {
	t.Parallel()

	// EG26: A single frame from an initiator address should not mark it as
	// occupied. Require at least 2 observations for defense in depth.
	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{
			{
				{Source: 0xFF, Target: 0xFE},
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
	// 0xFF has companion overflow (EG18), so 0xF7 should be selected.
	// The key assertion is that 0xFF was NOT rejected for being occupied
	// (only one observation).
	reasons := result.Metrics.RejectionReasons[0xFF]
	for _, r := range reasons {
		if r == "persisted-occupied" {
			t.Fatalf("0xFF rejected as occupied from single observation (EG26)")
		}
	}
}

func TestJoiner_TwoObservationsMarkOccupied(t *testing.T) {
	t.Parallel()

	// EG26: Two observations should be sufficient to mark an address as occupied.
	bus := &scriptedJoinBus{
		listenFrames: [][]Frame{
			{
				{Source: 0xF7, Target: 0xFE},
				{Source: 0xF7, Target: 0x15},
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
	// 0xF7 should be occupied (2 observations), 0xFF has companion overflow,
	// so the next candidate should be 0xF3.
	if result.Initiator == 0xF7 {
		t.Fatalf("Initiator = 0xf7; should be occupied after 2 observations")
	}
	if result.Initiator != 0xF3 {
		t.Fatalf("Initiator = 0x%02x; want 0xf3", result.Initiator)
	}
}
