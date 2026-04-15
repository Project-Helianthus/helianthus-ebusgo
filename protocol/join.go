package protocol

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var initiatorAddressAscending = []byte{
	0x00, 0x01, 0x03, 0x07, 0x0F,
	0x10, 0x11, 0x13, 0x17, 0x1F,
	0x30, 0x31, 0x33, 0x37, 0x3F,
	0x70, 0x71, 0x73, 0x77, 0x7F,
	0xF0, 0xF1, 0xF3, 0xF7, 0xFF,
}

var initiatorAddressDescending = []byte{
	0xFF, 0xF7, 0xF3, 0xF1, 0xF0,
	0x7F, 0x77, 0x73, 0x71, 0x70,
	0x3F, 0x37, 0x33, 0x31, 0x30,
	0x1F, 0x17, 0x13, 0x11, 0x10,
	0x0F, 0x07, 0x03, 0x01, 0x00,
}

const (
	defaultJoinListenWarmup            = 5 * time.Second
	defaultInquiryCooldown             = 60 * time.Second
	defaultInquiryMaxAttempts          = 3
	defaultRejoinBackoffBase           = 1 * time.Second
	defaultRejoinBackoffMax            = 30 * time.Second
	defaultGraceAfterRejoin            = 750 * time.Millisecond
	defaultInquiryFollowupListenWindow = 1500 * time.Millisecond
	defaultLikelyTargetSourceMin       = 2
	defaultLikelyTargetDestinationMin  = 2
	defaultTopTalkerCount              = 8
)

// JoinBus captures the minimum bus capabilities needed for address selection.
type JoinBus interface {
	// Listen receives frames until ctx expires or is canceled.
	Listen(ctx context.Context, onFrame func(Frame)) error
	// InquiryExistence triggers an optional active presence probe.
	InquiryExistence(ctx context.Context) error
}

// JoinStateStore persists the last successful initiator address.
type JoinStateStore interface {
	LoadInitiator(ctx context.Context) (byte, error)
	SaveInitiator(ctx context.Context, initiator byte) error
}

// JoinConfig controls initiator address selection behavior.
type JoinConfig struct {
	ListenWarmup          time.Duration
	PreferHighest         bool
	PreferHighestSet      bool
	InquiryEnabled        bool
	InquiryCooldown       time.Duration
	InquiryMaxAttempts    int
	InquiryDisableOnNoNew bool

	RejoinBackoffBase time.Duration
	RejoinBackoffMax  time.Duration

	ForceIfAllOccupied     bool
	PersistLastGood        bool
	PersistLastGoodSet     bool
	GracePeriodAfterRejoin time.Duration
}

// JoinMetrics captures selection telemetry for observability and debugging.
type JoinMetrics struct {
	WarmupDurationActual    time.Duration
	CandidatesConsidered    []byte
	RejectionReasons        map[byte][]string
	ObservedSources         []byte
	ObservedInitiators      []byte
	ObservedProbableTargets []byte
	TopTalkersBySource      []byte
	Forced                  bool
}

// JoinResult contains the selected initiator address and companion target.
type JoinResult struct {
	Initiator       byte
	CompanionTarget byte
	Metrics         JoinMetrics
}

// ErrNoFreeInitiatorAddress is returned when all initiator addresses are occupied
// and force mode is disabled.
type ErrNoFreeInitiatorAddress struct {
	ObservedInitiators []byte
	Metrics            JoinMetrics
}

func (e *ErrNoFreeInitiatorAddress) Error() string {
	return fmt.Sprintf("ebus: no free initiator address (observed: %x)", e.ObservedInitiators)
}

// Joiner selects an initiator address with a low-disturbance strategy.
type Joiner struct {
	bus   JoinBus
	store JoinStateStore
	cfg   JoinConfig
	now   func() time.Time

	mu                sync.Mutex
	inquiryAttempts   int
	inquiryNoNewCount int
	inquiryDisabled   bool
	lastInquiryAt     time.Time
}

// NewJoiner creates a Joiner with defaults applied.
func NewJoiner(bus JoinBus, store JoinStateStore, cfg JoinConfig) *Joiner {
	return &Joiner{
		bus:   bus,
		store: store,
		cfg:   normalizeJoinConfig(cfg),
		now:   time.Now,
	}
}

// Join performs warmup observation, optional inquiry, then selects an initiator.
func (j *Joiner) Join(ctx context.Context) (JoinResult, error) {
	if j == nil || j.bus == nil {
		return JoinResult{}, fmt.Errorf("ebus: joiner bus is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := normalizeJoinConfig(j.cfg)

	observation := newJoinObservation()
	warmupDuration, err := j.observeWindow(ctx, cfg.ListenWarmup, observation)
	if err != nil {
		return JoinResult{}, err
	}

	if shouldInquiry := j.shouldRunInquiry(cfg); shouldInquiry {
		j.markInquiryAttempt()
		if err := j.bus.InquiryExistence(ctx); err == nil {
			before := len(observation.observedInitiators)
			_, listenErr := j.observeWindow(ctx, defaultInquiryFollowupListenWindow, observation)
			if listenErr != nil && !errors.Is(listenErr, context.Canceled) && !errors.Is(listenErr, context.DeadlineExceeded) {
				return JoinResult{}, listenErr
			}
			after := len(observation.observedInitiators)
			j.markInquiryResult(cfg, after > before)
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return JoinResult{}, err
		} else if ctx.Err() != nil {
			return JoinResult{}, ctx.Err()
		}
	}

	metrics := JoinMetrics{
		WarmupDurationActual:    warmupDuration,
		RejectionReasons:        make(map[byte][]string),
		ObservedSources:         sortedKeys(observation.observedSources),
		ObservedInitiators:      sortedKeys(observation.observedInitiators),
		ObservedProbableTargets: sortedKeys(observation.observedProbableTargets),
		TopTalkersBySource:      observation.topTalkers(defaultTopTalkerCount),
	}

	preferredOrder := initiatorAddressDescending
	if !cfg.PreferHighest {
		preferredOrder = initiatorAddressAscending
	}

	var persistedCandidate *byte
	if cfg.PersistLastGood && j.store != nil {
		if persisted, loadErr := j.store.LoadInitiator(ctx); loadErr == nil && IsInitiatorCapableAddress(persisted) {
			persistedCandidate = &persisted
		}
	}

	chosen, selectedFromOccupied, err := j.selectInitiator(preferredOrder, observation, persistedCandidate, &metrics, cfg.ForceIfAllOccupied)
	if err != nil {
		return JoinResult{}, err
	}
	if selectedFromOccupied {
		metrics.Forced = true
	}

	if cfg.PersistLastGood && j.store != nil {
		_ = j.store.SaveInitiator(ctx, chosen)
	}

	return JoinResult{
		Initiator:       chosen,
		CompanionTarget: chosen + 0x05,
		Metrics:         metrics,
	}, nil
}

func (j *Joiner) observeWindow(ctx context.Context, duration time.Duration, observation *joinObservation) (time.Duration, error) {
	if duration <= 0 {
		return 0, nil
	}

	started := j.now()
	windowCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	err := j.bus.Listen(windowCtx, observation.addFrame)
	elapsed := j.now().Sub(started)
	if err == nil {
		return elapsed, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			return elapsed, ctx.Err()
		}
		return elapsed, nil
	}
	return elapsed, err
}

func (j *Joiner) shouldRunInquiry(cfg JoinConfig) bool {
	if !cfg.InquiryEnabled || cfg.InquiryMaxAttempts <= 0 {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.inquiryDisabled {
		return false
	}
	if j.inquiryAttempts >= cfg.InquiryMaxAttempts {
		return false
	}
	if cfg.InquiryCooldown > 0 && !j.lastInquiryAt.IsZero() {
		if j.now().Sub(j.lastInquiryAt) < cfg.InquiryCooldown {
			return false
		}
	}
	return true
}

func (j *Joiner) markInquiryAttempt() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.inquiryAttempts++
	j.lastInquiryAt = j.now()
}

func (j *Joiner) markInquiryResult(cfg JoinConfig, foundNew bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if foundNew {
		j.inquiryNoNewCount = 0
		return
	}
	j.inquiryNoNewCount++
	if cfg.InquiryDisableOnNoNew && j.inquiryNoNewCount >= cfg.InquiryMaxAttempts {
		j.inquiryDisabled = true
	}
}

func (j *Joiner) selectInitiator(
	preferredOrder []byte,
	observation *joinObservation,
	persistedCandidate *byte,
	metrics *JoinMetrics,
	forceIfAllOccupied bool,
) (byte, bool, error) {
	occupied := observation.observedInitiators
	freeCount := 0
	for _, candidate := range preferredOrder {
		if _, isOccupied := occupied[candidate]; !isOccupied {
			freeCount++
		}
	}

	if persistedCandidate != nil {
		candidate := *persistedCandidate
		appendUniqueByte(&metrics.CandidatesConsidered, candidate)
		if _, isOccupied := occupied[candidate]; isOccupied {
			metrics.RejectionReasons[candidate] = append(metrics.RejectionReasons[candidate], "persisted-occupied")
		} else {
			reasons := companionTargetRejectionReasons(observation, candidate)
			if len(reasons) == 0 {
				return candidate, false, nil
			}
			metrics.RejectionReasons[candidate] = append(metrics.RejectionReasons[candidate], reasons...)
		}
	}

	if freeCount == 0 {
		if !forceIfAllOccupied {
			return 0, false, &ErrNoFreeInitiatorAddress{
				ObservedInitiators: append([]byte(nil), metrics.ObservedInitiators...),
				Metrics:            *metrics,
			}
		}
		if len(preferredOrder) == 0 {
			return 0, false, &ErrNoFreeInitiatorAddress{
				ObservedInitiators: append([]byte(nil), metrics.ObservedInitiators...),
				Metrics:            *metrics,
			}
		}
		chosen := preferredOrder[0]
		appendUniqueByte(&metrics.CandidatesConsidered, chosen)
		metrics.RejectionReasons[chosen] = append(metrics.RejectionReasons[chosen], "force-if-all-occupied")
		return chosen, true, nil
	}

	var firstFree byte
	firstFreeSet := false
	for _, candidate := range preferredOrder {
		if _, isOccupied := occupied[candidate]; isOccupied {
			continue
		}
		appendUniqueByte(&metrics.CandidatesConsidered, candidate)
		if !firstFreeSet {
			firstFree = candidate
			firstFreeSet = true
		}
		reasons := companionTargetRejectionReasons(observation, candidate)
		if len(reasons) == 0 {
			return candidate, false, nil
		}
		metrics.RejectionReasons[candidate] = append(metrics.RejectionReasons[candidate], reasons...)
	}

	metrics.RejectionReasons[firstFree] = append(metrics.RejectionReasons[firstFree], "heuristic-overridden")
	return firstFree, false, nil
}

func companionTargetRejectionReasons(observation *joinObservation, initiator byte) []string {
	// Guard against byte overflow: initiator + 0x05 wraps around when
	// initiator > 0xFA, producing a low address that could collide with
	// well-known device addresses (e.g. 0x00-0x04). Skip companion
	// target heuristics entirely for these cases (EG18).
	if int(initiator)+0x05 > 0xFF {
		return []string{"companion-target-overflows-byte"}
	}
	companionTarget := initiator + 0x05
	reasons := make([]string, 0, 2)

	if !IsInitiatorCapableAddress(companionTarget) && observation.sourceCount[companionTarget] >= defaultLikelyTargetSourceMin {
		reasons = append(reasons, "companion-target-seen-as-probable-target-source")
	}
	if observation.targetCount[companionTarget] >= defaultLikelyTargetDestinationMin {
		reasons = append(reasons, "companion-target-frequently-addressed")
	}

	return reasons
}

func normalizeJoinConfig(cfg JoinConfig) JoinConfig {
	if cfg.ListenWarmup <= 0 {
		cfg.ListenWarmup = defaultJoinListenWarmup
	}
	if !cfg.PreferHighestSet {
		cfg.PreferHighest = true
	}
	if cfg.InquiryCooldown <= 0 {
		cfg.InquiryCooldown = defaultInquiryCooldown
	}
	if cfg.InquiryMaxAttempts <= 0 {
		cfg.InquiryMaxAttempts = defaultInquiryMaxAttempts
	}
	if cfg.RejoinBackoffBase <= 0 {
		cfg.RejoinBackoffBase = defaultRejoinBackoffBase
	}
	if cfg.RejoinBackoffMax <= 0 {
		cfg.RejoinBackoffMax = defaultRejoinBackoffMax
	}
	if cfg.GracePeriodAfterRejoin <= 0 {
		cfg.GracePeriodAfterRejoin = defaultGraceAfterRejoin
	}
	if !cfg.PersistLastGoodSet {
		cfg.PersistLastGood = true
	}
	return cfg
}

type joinObservation struct {
	sourceCount             map[byte]int
	targetCount             map[byte]int
	observedSources         map[byte]struct{}
	observedInitiators      map[byte]struct{}
	observedProbableTargets map[byte]struct{}
}

func newJoinObservation() *joinObservation {
	return &joinObservation{
		sourceCount:             make(map[byte]int),
		targetCount:             make(map[byte]int),
		observedSources:         make(map[byte]struct{}),
		observedInitiators:      make(map[byte]struct{}),
		observedProbableTargets: make(map[byte]struct{}),
	}
}

func (o *joinObservation) addFrame(frame Frame) {
	o.sourceCount[frame.Source]++
	o.targetCount[frame.Target]++
	o.observedSources[frame.Source] = struct{}{}
	if IsInitiatorCapableAddress(frame.Source) {
		// Require at least 2 observations before marking an address as
		// occupied. A single frame could be noise or a spoofed source;
		// two independent sightings provide stronger evidence (EG26).
		if o.sourceCount[frame.Source] >= 2 {
			o.observedInitiators[frame.Source] = struct{}{}
		}
		return
	}
	o.observedProbableTargets[frame.Source] = struct{}{}
}

func (o *joinObservation) topTalkers(limit int) []byte {
	type counter struct {
		address byte
		count   int
	}
	list := make([]counter, 0, len(o.sourceCount))
	for address, count := range o.sourceCount {
		list = append(list, counter{address: address, count: count})
	}
	sort.Slice(list, func(i, k int) bool {
		if list[i].count == list[k].count {
			return list[i].address < list[k].address
		}
		return list[i].count > list[k].count
	})
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	result := make([]byte, 0, len(list))
	for _, item := range list {
		result = append(result, item.address)
	}
	return result
}

func sortedKeys(set map[byte]struct{}) []byte {
	keys := make([]byte, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, k int) bool { return keys[i] < keys[k] })
	return keys
}

func appendUniqueByte(values *[]byte, value byte) {
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}
