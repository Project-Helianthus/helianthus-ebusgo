package protocol

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	defaultSourceAddressListenWarmup          = 5 * time.Second
	defaultSourceAddressInquiryCooldown       = 60 * time.Second
	defaultSourceAddressInquiryMaxAttempts    = 3
	defaultSourceAddressInquiryFollowupWindow = 1500 * time.Millisecond
	defaultSourceAddressLikelyTargetSourceMin = 2
	defaultSourceAddressLikelyTargetTargetMin = 2
	defaultSourceAddressTopTalkerCount        = 8
	SourceAddressTableAnchor                  = "#ebus-source-address-table-v1"
	SourceAddressTableVersion                 = "ebus-source-address-table/v1"
	SourceAddressTableHash                    = "e78954445087f63064818ab60a2739b9a6b9bf0ae0147fbe92aac5ac76592103"
)

// SourceAddressPriorityIndex is the official p0..p4 eBUS source-address priority class.
type SourceAddressPriorityIndex string

const (
	SourceAddressPriorityP0 SourceAddressPriorityIndex = "p0"
	SourceAddressPriorityP1 SourceAddressPriorityIndex = "p1"
	SourceAddressPriorityP2 SourceAddressPriorityIndex = "p2"
	SourceAddressPriorityP3 SourceAddressPriorityIndex = "p3"
	SourceAddressPriorityP4 SourceAddressPriorityIndex = "p4"
)

// SourceAddressDescription is the canonical Helianthus description from the
// docs-owned source-address table.
type SourceAddressDescription string

const (
	SourceAddressDescriptionPCModem                      SourceAddressDescription = "PC/Modem"
	SourceAddressDescriptionHeatingRegulator             SourceAddressDescription = "Heating regulator"
	SourceAddressDescriptionHeatingCircuitRegulator1     SourceAddressDescription = "Heating circuit regulator 1"
	SourceAddressDescriptionHeatingCircuitRegulator2     SourceAddressDescription = "Heating circuit regulator 2"
	SourceAddressDescriptionHeatingCircuitRegulator3     SourceAddressDescription = "Heating circuit regulator 3"
	SourceAddressDescriptionHandheldProgrammerRemote     SourceAddressDescription = "Handheld programmer / remote"
	SourceAddressDescriptionBusInterfaceClimateRegulator SourceAddressDescription = "Bus interface / climate regulator"
	SourceAddressDescriptionBusInterface                 SourceAddressDescription = "Bus interface"
	SourceAddressDescriptionCombustionController1        SourceAddressDescription = "Combustion controller 1"
	SourceAddressDescriptionCombustionController2        SourceAddressDescription = "Combustion controller 2"
	SourceAddressDescriptionCombustionController3        SourceAddressDescription = "Combustion controller 3"
	SourceAddressDescriptionCombustionController4        SourceAddressDescription = "Combustion controller 4"
	SourceAddressDescriptionCombustionController5        SourceAddressDescription = "Combustion controller 5"
	SourceAddressDescriptionNotPreallocated              SourceAddressDescription = "Not preallocated"
	SourceAddressDescriptionClockRadioClockModule        SourceAddressDescription = "Clock/radio-clock module"
	SourceAddressDescriptionPC                           SourceAddressDescription = "PC"
)

// SourceAddressTableRow is one normative row from the docs-owned eBUS table.
type SourceAddressTableRow struct {
	Source                     byte
	PriorityIndex              SourceAddressPriorityIndex
	ArbitrationNibble          byte
	OfficialDescriptionSummary string
	CanonicalDescription       SourceAddressDescription
	FreeUse                    bool
	RecommendedFor             string
	Companion                  byte
}

var sourceAddressTableV1 = [...]SourceAddressTableRow{
	{Source: 0x00, PriorityIndex: SourceAddressPriorityP0, ArbitrationNibble: 0x0, OfficialDescriptionSummary: "PC/Modem", CanonicalDescription: SourceAddressDescriptionPCModem, RecommendedFor: "none", Companion: 0x05},
	{Source: 0x10, PriorityIndex: SourceAddressPriorityP0, ArbitrationNibble: 0x0, OfficialDescriptionSummary: "Heating controller", CanonicalDescription: SourceAddressDescriptionHeatingRegulator, RecommendedFor: "none", Companion: 0x15},
	{Source: 0x30, PriorityIndex: SourceAddressPriorityP0, ArbitrationNibble: 0x0, OfficialDescriptionSummary: "Heating circuit controller 1", CanonicalDescription: SourceAddressDescriptionHeatingCircuitRegulator1, RecommendedFor: "none", Companion: 0x35},
	{Source: 0x70, PriorityIndex: SourceAddressPriorityP0, ArbitrationNibble: 0x0, OfficialDescriptionSummary: "Heating circuit controller 2", CanonicalDescription: SourceAddressDescriptionHeatingCircuitRegulator2, RecommendedFor: "none", Companion: 0x75},
	{Source: 0xF0, PriorityIndex: SourceAddressPriorityP0, ArbitrationNibble: 0x0, OfficialDescriptionSummary: "Heating circuit controller 3", CanonicalDescription: SourceAddressDescriptionHeatingCircuitRegulator3, RecommendedFor: "none", Companion: 0xF5},
	{Source: 0x01, PriorityIndex: SourceAddressPriorityP1, ArbitrationNibble: 0x1, OfficialDescriptionSummary: "Hand programmer / Remote control", CanonicalDescription: SourceAddressDescriptionHandheldProgrammerRemote, RecommendedFor: "none", Companion: 0x06},
	{Source: 0x11, PriorityIndex: SourceAddressPriorityP1, ArbitrationNibble: 0x1, OfficialDescriptionSummary: "Bus interface / Climate controller", CanonicalDescription: SourceAddressDescriptionBusInterfaceClimateRegulator, RecommendedFor: "none", Companion: 0x16},
	{Source: 0x31, PriorityIndex: SourceAddressPriorityP1, ArbitrationNibble: 0x1, OfficialDescriptionSummary: "Bus interface", CanonicalDescription: SourceAddressDescriptionBusInterface, RecommendedFor: "none", Companion: 0x36},
	{Source: 0x71, PriorityIndex: SourceAddressPriorityP1, ArbitrationNibble: 0x1, OfficialDescriptionSummary: "Heating controller", CanonicalDescription: SourceAddressDescriptionHeatingRegulator, RecommendedFor: "none", Companion: 0x76},
	{Source: 0xF1, PriorityIndex: SourceAddressPriorityP1, ArbitrationNibble: 0x1, OfficialDescriptionSummary: "Heating controller", CanonicalDescription: SourceAddressDescriptionHeatingRegulator, RecommendedFor: "none", Companion: 0xF6},
	{Source: 0x03, PriorityIndex: SourceAddressPriorityP2, ArbitrationNibble: 0x3, OfficialDescriptionSummary: "Burner controller 1", CanonicalDescription: SourceAddressDescriptionCombustionController1, RecommendedFor: "none", Companion: 0x08},
	{Source: 0x13, PriorityIndex: SourceAddressPriorityP2, ArbitrationNibble: 0x3, OfficialDescriptionSummary: "Burner controller 2", CanonicalDescription: SourceAddressDescriptionCombustionController2, RecommendedFor: "none", Companion: 0x18},
	{Source: 0x33, PriorityIndex: SourceAddressPriorityP2, ArbitrationNibble: 0x3, OfficialDescriptionSummary: "Burner controller 3", CanonicalDescription: SourceAddressDescriptionCombustionController3, RecommendedFor: "none", Companion: 0x38},
	{Source: 0x73, PriorityIndex: SourceAddressPriorityP2, ArbitrationNibble: 0x3, OfficialDescriptionSummary: "Burner controller 4", CanonicalDescription: SourceAddressDescriptionCombustionController4, RecommendedFor: "none", Companion: 0x78},
	{Source: 0xF3, PriorityIndex: SourceAddressPriorityP2, ArbitrationNibble: 0x3, OfficialDescriptionSummary: "Burner controller 5", CanonicalDescription: SourceAddressDescriptionCombustionController5, RecommendedFor: "none", Companion: 0xF8},
	{Source: 0x07, PriorityIndex: SourceAddressPriorityP3, ArbitrationNibble: 0x7, OfficialDescriptionSummary: "empty", CanonicalDescription: SourceAddressDescriptionNotPreallocated, FreeUse: true, RecommendedFor: "none", Companion: 0x0C},
	{Source: 0x17, PriorityIndex: SourceAddressPriorityP3, ArbitrationNibble: 0x7, OfficialDescriptionSummary: "Heating controller recommendation", CanonicalDescription: SourceAddressDescriptionNotPreallocated, FreeUse: true, RecommendedFor: "Heating regulator", Companion: 0x1C},
	{Source: 0x37, PriorityIndex: SourceAddressPriorityP3, ArbitrationNibble: 0x7, OfficialDescriptionSummary: "Heating controller recommendation", CanonicalDescription: SourceAddressDescriptionNotPreallocated, FreeUse: true, RecommendedFor: "Heating regulator", Companion: 0x3C},
	{Source: 0x77, PriorityIndex: SourceAddressPriorityP3, ArbitrationNibble: 0x7, OfficialDescriptionSummary: "Heating controller recommendation", CanonicalDescription: SourceAddressDescriptionNotPreallocated, FreeUse: true, RecommendedFor: "Heating regulator", Companion: 0x7C},
	{Source: 0xF7, PriorityIndex: SourceAddressPriorityP3, ArbitrationNibble: 0x7, OfficialDescriptionSummary: "Heating controller recommendation", CanonicalDescription: SourceAddressDescriptionNotPreallocated, FreeUse: true, RecommendedFor: "Heating regulator", Companion: 0xFC},
	{Source: 0x0F, PriorityIndex: SourceAddressPriorityP4, ArbitrationNibble: 0xF, OfficialDescriptionSummary: "Clock module / Radio clock module", CanonicalDescription: SourceAddressDescriptionClockRadioClockModule, RecommendedFor: "none", Companion: 0x14},
	{Source: 0x1F, PriorityIndex: SourceAddressPriorityP4, ArbitrationNibble: 0xF, OfficialDescriptionSummary: "Burner controller 6 recommendation", CanonicalDescription: SourceAddressDescriptionNotPreallocated, FreeUse: true, RecommendedFor: "Combustion controller 6", Companion: 0x24},
	{Source: 0x3F, PriorityIndex: SourceAddressPriorityP4, ArbitrationNibble: 0xF, OfficialDescriptionSummary: "Burner controller 7 recommendation", CanonicalDescription: SourceAddressDescriptionNotPreallocated, FreeUse: true, RecommendedFor: "Combustion controller 7", Companion: 0x44},
	{Source: 0x7F, PriorityIndex: SourceAddressPriorityP4, ArbitrationNibble: 0xF, OfficialDescriptionSummary: "Burner controller 8 recommendation", CanonicalDescription: SourceAddressDescriptionNotPreallocated, FreeUse: true, RecommendedFor: "Combustion controller 8", Companion: 0x84},
	{Source: 0xFF, PriorityIndex: SourceAddressPriorityP4, ArbitrationNibble: 0xF, OfficialDescriptionSummary: "PC", CanonicalDescription: SourceAddressDescriptionPC, RecommendedFor: "none", Companion: 0x04},
}

var helianthusGatewayDefaultPolicyV1 = [...]byte{
	0xFF, 0x7F, 0x3F, 0x1F,
	0xF7, 0x77, 0x37, 0x17, 0x07,
	0x11, 0x31,
	0x00,
}

// SourceAddressTableRows returns a copy of the docs-owned source-address table.
func SourceAddressTableRows() []SourceAddressTableRow {
	rows := make([]SourceAddressTableRow, len(sourceAddressTableV1))
	copy(rows, sourceAddressTableV1[:])
	return rows
}

// HelianthusGatewayDefaultPolicy returns the Helianthus startup candidate order.
func HelianthusGatewayDefaultPolicy() []byte {
	policy := make([]byte, len(helianthusGatewayDefaultPolicyV1))
	copy(policy, helianthusGatewayDefaultPolicyV1[:])
	return policy
}

// CompanionAddressForSource returns the responder-side companion address using byte modulo arithmetic.
func CompanionAddressForSource(source byte) byte {
	return source + 0x05
}

// LookupSourceAddress returns the standard table row for source.
func LookupSourceAddress(source byte) (SourceAddressTableRow, bool) {
	for _, row := range sourceAddressTableV1 {
		if row.Source == source {
			return row, true
		}
	}
	return SourceAddressTableRow{}, false
}

// OfficialSourceAddressArbitrationRank returns the official arbitration class
// rank. Lower rank wins; this is intentionally separate from the Helianthus
// default candidate policy.
func OfficialSourceAddressArbitrationRank(source byte) (int, bool) {
	row, ok := LookupSourceAddress(source)
	if !ok {
		return 0, false
	}
	return sourceAddressPriorityRank(row.PriorityIndex), true
}

// CompareOfficialSourceAddressArbitration compares two source addresses for
// official bus arbitration. It returns -1 when a wins, 1 when b wins, 0 when
// equal, and false when either source is outside the standard table.
func CompareOfficialSourceAddressArbitration(a, b byte) (int, bool) {
	aRank, ok := OfficialSourceAddressArbitrationRank(a)
	if !ok {
		return 0, false
	}
	bRank, ok := OfficialSourceAddressArbitrationRank(b)
	if !ok {
		return 0, false
	}
	if aRank < bRank {
		return -1, true
	}
	if aRank > bRank {
		return 1, true
	}
	if a < b {
		return -1, true
	}
	if a > b {
		return 1, true
	}
	return 0, true
}

// SourceAddressSelectionBus captures optional bus observation capabilities used
// before selecting a source address.
type SourceAddressSelectionBus interface {
	// Listen receives frames until ctx expires or is canceled.
	Listen(ctx context.Context, onFrame func(Frame)) error
	// InquiryExistence triggers an optional active presence probe.
	InquiryExistence(ctx context.Context) error
}

// SourceAddressSelectionMode classifies how candidate rows were produced.
type SourceAddressSelectionMode string

const (
	SourceAddressSelectionModeDefaultPolicy                      SourceAddressSelectionMode = "default_policy"
	SourceAddressSelectionModeSourceDescriptionConstrainedPolicy SourceAddressSelectionMode = "source_description_constrained_policy"
	SourceAddressSelectionModePriorityFilteredDefaultPolicy      SourceAddressSelectionMode = "priority_filtered_default_policy"
	SourceAddressSelectionModeExplicitValidateOnly               SourceAddressSelectionMode = "explicit_validate_only"
)

// SourceAddressOccupancyState is the selector's known state for a source or companion address.
type SourceAddressOccupancyState string

const (
	SourceAddressOccupancyUnknown          SourceAddressOccupancyState = "unknown"
	SourceAddressOccupancyObservedFree     SourceAddressOccupancyState = "observed_free"
	SourceAddressOccupancyObservedOccupied SourceAddressOccupancyState = "observed_occupied"
	SourceAddressOccupancyStaleKnownDevice SourceAddressOccupancyState = "stale_known_device"
)

// KnownAddressOccupancy records an address state learned outside the selector.
type KnownAddressOccupancy struct {
	Address byte
	State   SourceAddressOccupancyState
}

// KnownAddressEvidence supplies admission-adjacent address evidence without
// giving the selector authority to perform gateway startup admission.
type KnownAddressEvidence struct {
	CurrentObservation  []KnownAddressOccupancy
	Topology            []KnownAddressOccupancy
	Cache               []KnownAddressOccupancy
	StaleKnownDevices   []byte
	CompanionProvenance []KnownAddressOccupancy
}

// ResolveKnownAddressOccupancy resolves explicit known-address evidence for address.
func ResolveKnownAddressOccupancy(evidence KnownAddressEvidence, address byte) SourceAddressOccupancyState {
	return resolveKnownAddressOccupancy(evidence, address, nil)
}

// SourceAddressSelectionConfig controls source-address candidate selection.
type SourceAddressSelectionConfig struct {
	ListenWarmup          time.Duration
	InquiryEnabled        bool
	InquiryCooldown       time.Duration
	InquiryMaxAttempts    int
	InquiryDisableOnNoNew bool

	SourceDescription SourceAddressDescription
	PriorityIndex     SourceAddressPriorityIndex
	ExplicitSource    byte
	ExplicitSourceSet bool

	Evidence KnownAddressEvidence
}

// SourceAddressSelectionMetrics captures selection telemetry for observability and debugging.
type SourceAddressSelectionMetrics struct {
	Mode                    SourceAddressSelectionMode
	WarmupDurationActual    time.Duration
	CandidatesConsidered    []byte
	RejectionReasons        map[byte][]string
	Occupancy               map[byte]SourceAddressOccupancyState
	ObservedSources         []byte
	ObservedSourceAddresses []byte
	ObservedProbableTargets []byte
	TopTalkersBySource      []byte
}

// SourceAddressSelection contains the selected source and companion address.
type SourceAddressSelection struct {
	Source       byte
	Companion    byte
	TableAnchor  string
	TableVersion string
	TableHash    string
	Mode         SourceAddressSelectionMode
	Metrics      SourceAddressSelectionMetrics
}

// ErrSourceAddressConfig reports invalid selector configuration.
type ErrSourceAddressConfig struct {
	Reason string
}

func (e *ErrSourceAddressConfig) Error() string {
	return fmt.Sprintf("ebus: source address selection config: %s", e.Reason)
}

// ErrSourceAddressValidation reports that a selected or candidate source cannot be used.
type ErrSourceAddressValidation struct {
	Source    byte
	Companion byte
	Reason    string
}

func (e *ErrSourceAddressValidation) Error() string {
	return fmt.Sprintf("ebus: source address validation failed for 0x%02x/0x%02x: %s", e.Source, e.Companion, e.Reason)
}

// ErrNoAvailableSourceAddress is returned when no candidate passes validation.
type ErrNoAvailableSourceAddress struct {
	Metrics SourceAddressSelectionMetrics
}

func (e *ErrNoAvailableSourceAddress) Error() string {
	return "ebus: no available source address"
}

// ErrSourceAddressBusObservation wraps non-context bus observation failures.
type ErrSourceAddressBusObservation struct {
	Operation string
	Err       error
}

func (e *ErrSourceAddressBusObservation) Error() string {
	return fmt.Sprintf("ebus: source address bus observation %s failed: %v", e.Operation, e.Err)
}

func (e *ErrSourceAddressBusObservation) Unwrap() error {
	return e.Err
}

// SourceAddressSelector selects a source address with a low-disturbance strategy.
type SourceAddressSelector struct {
	bus SourceAddressSelectionBus
	cfg SourceAddressSelectionConfig
	now func() time.Time

	mu                sync.Mutex
	inquiryAttempts   int
	inquiryNoNewCount int
	inquiryDisabled   bool
	lastInquiryAt     time.Time
}

// NewSourceAddressSelector creates a SourceAddressSelector with defaults applied.
func NewSourceAddressSelector(bus SourceAddressSelectionBus, cfg SourceAddressSelectionConfig) *SourceAddressSelector {
	return &SourceAddressSelector{
		bus: bus,
		cfg: normalizeSourceAddressSelectionConfig(cfg),
		now: time.Now,
	}
}

// Select observes the bus, applies configured evidence, and selects a source address.
func (s *SourceAddressSelector) Select(ctx context.Context) (SourceAddressSelection, error) {
	if s == nil {
		return SourceAddressSelection{}, &ErrSourceAddressConfig{Reason: "selector is nil"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := normalizeSourceAddressSelectionConfig(s.cfg)
	if err := validateSourceAddressSelectionConfig(cfg); err != nil {
		return SourceAddressSelection{}, err
	}

	observation := newSourceAddressObservation()
	warmupDuration, err := s.observeWindow(ctx, cfg.ListenWarmup, observation)
	if err != nil {
		return SourceAddressSelection{}, err
	}

	if shouldInquiry := s.shouldRunInquiry(cfg); shouldInquiry {
		s.markInquiryAttempt()
		if err := s.bus.InquiryExistence(ctx); err == nil {
			before := len(observation.observedSourceAddresses)
			_, listenErr := s.observeWindow(ctx, defaultSourceAddressInquiryFollowupWindow, observation)
			if listenErr != nil && !errors.Is(listenErr, context.Canceled) && !errors.Is(listenErr, context.DeadlineExceeded) {
				return SourceAddressSelection{}, listenErr
			}
			after := len(observation.observedSourceAddresses)
			s.markInquiryResult(cfg, after > before)
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return SourceAddressSelection{}, err
		} else if ctx.Err() != nil {
			return SourceAddressSelection{}, ctx.Err()
		} else {
			s.markInquiryResult(cfg, false)
		}
	}

	metrics := SourceAddressSelectionMetrics{
		Mode:                    cfg.selectionMode(),
		WarmupDurationActual:    warmupDuration,
		RejectionReasons:        make(map[byte][]string),
		Occupancy:               make(map[byte]SourceAddressOccupancyState),
		ObservedSources:         sortedKeys(observation.observedSources),
		ObservedSourceAddresses: sortedKeys(observation.observedSourceAddresses),
		ObservedProbableTargets: sortedKeys(observation.observedProbableTargets),
		TopTalkersBySource:      observation.topTalkers(defaultSourceAddressTopTalkerCount),
	}

	rows, err := candidateRowsForSourceAddressSelection(cfg)
	if err != nil {
		return SourceAddressSelection{}, err
	}

	for _, row := range rows {
		appendUniqueByte(&metrics.CandidatesConsidered, row.Source)
		if validationErr := validateSourceAddressCandidate(row, cfg.Evidence, observation, &metrics); validationErr != nil {
			metrics.RejectionReasons[row.Source] = append(metrics.RejectionReasons[row.Source], validationErr.Reason)
			if cfg.ExplicitSourceSet {
				return SourceAddressSelection{}, validationErr
			}
			continue
		}
		return SourceAddressSelection{
			Source:       row.Source,
			Companion:    row.Companion,
			TableAnchor:  SourceAddressTableAnchor,
			TableVersion: SourceAddressTableVersion,
			TableHash:    SourceAddressTableHash,
			Mode:         metrics.Mode,
			Metrics:      metrics,
		}, nil
	}

	return SourceAddressSelection{}, &ErrNoAvailableSourceAddress{Metrics: metrics}
}

func (s *SourceAddressSelector) observeWindow(ctx context.Context, duration time.Duration, observation *sourceAddressObservation) (time.Duration, error) {
	if duration <= 0 {
		return 0, nil
	}
	if s.bus == nil {
		return 0, &ErrSourceAddressConfig{Reason: "selection bus is nil"}
	}

	started := s.now()
	windowCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	err := s.bus.Listen(windowCtx, observation.addFrame)
	elapsed := s.now().Sub(started)
	if err == nil {
		return elapsed, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			return elapsed, ctx.Err()
		}
		return elapsed, nil
	}
	return elapsed, &ErrSourceAddressBusObservation{Operation: "listen", Err: err}
}

func (s *SourceAddressSelector) shouldRunInquiry(cfg SourceAddressSelectionConfig) bool {
	if !cfg.InquiryEnabled || cfg.InquiryMaxAttempts <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inquiryDisabled {
		return false
	}
	if s.inquiryAttempts >= cfg.InquiryMaxAttempts {
		return false
	}
	if cfg.InquiryCooldown > 0 && !s.lastInquiryAt.IsZero() {
		if s.now().Sub(s.lastInquiryAt) < cfg.InquiryCooldown {
			return false
		}
	}
	return true
}

func (s *SourceAddressSelector) markInquiryAttempt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inquiryAttempts++
	s.lastInquiryAt = s.now()
}

func (s *SourceAddressSelector) markInquiryResult(cfg SourceAddressSelectionConfig, foundNew bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if foundNew {
		s.inquiryNoNewCount = 0
		return
	}
	s.inquiryNoNewCount++
	if cfg.InquiryDisableOnNoNew && s.inquiryNoNewCount >= cfg.InquiryMaxAttempts {
		s.inquiryDisabled = true
	}
}

func validateSourceAddressCandidate(
	row SourceAddressTableRow,
	evidence KnownAddressEvidence,
	observation *sourceAddressObservation,
	metrics *SourceAddressSelectionMetrics,
) *ErrSourceAddressValidation {
	sourceState := resolveKnownAddressOccupancy(evidence, row.Source, observation)
	recordSourceAddressOccupancy(metrics, row.Source, sourceState)
	switch sourceState {
	case SourceAddressOccupancyObservedOccupied:
		return &ErrSourceAddressValidation{Source: row.Source, Companion: row.Companion, Reason: "source-observed-occupied"}
	case SourceAddressOccupancyStaleKnownDevice:
		return &ErrSourceAddressValidation{Source: row.Source, Companion: row.Companion, Reason: "source-stale-known-device"}
	}

	companionState := resolveKnownAddressOccupancy(evidence, row.Companion, observation)
	recordSourceAddressOccupancy(metrics, row.Companion, companionState)
	if row.Source == 0xFF && companionState != SourceAddressOccupancyObservedFree {
		return &ErrSourceAddressValidation{Source: row.Source, Companion: row.Companion, Reason: companionRejectionReason(companionState)}
	}
	switch companionState {
	case SourceAddressOccupancyObservedOccupied:
		return &ErrSourceAddressValidation{Source: row.Source, Companion: row.Companion, Reason: "companion-observed-occupied"}
	case SourceAddressOccupancyStaleKnownDevice:
		return &ErrSourceAddressValidation{Source: row.Source, Companion: row.Companion, Reason: "companion-stale-known-device"}
	}
	return nil
}

func companionRejectionReason(state SourceAddressOccupancyState) string {
	switch state {
	case SourceAddressOccupancyObservedOccupied:
		return "companion-observed-occupied"
	case SourceAddressOccupancyStaleKnownDevice:
		return "companion-stale-known-device"
	case SourceAddressOccupancyObservedFree:
		return ""
	default:
		return "companion-unknown"
	}
}

func recordSourceAddressOccupancy(metrics *SourceAddressSelectionMetrics, address byte, state SourceAddressOccupancyState) {
	if metrics.Occupancy == nil {
		metrics.Occupancy = make(map[byte]SourceAddressOccupancyState)
	}
	metrics.Occupancy[address] = state
}

func candidateRowsForSourceAddressSelection(cfg SourceAddressSelectionConfig) ([]SourceAddressTableRow, error) {
	if cfg.ExplicitSourceSet {
		row, ok := LookupSourceAddress(cfg.ExplicitSource)
		if !ok {
			return nil, &ErrSourceAddressValidation{Source: cfg.ExplicitSource, Companion: CompanionAddressForSource(cfg.ExplicitSource), Reason: "source-not-in-standard-table"}
		}
		return []SourceAddressTableRow{row}, nil
	}

	if cfg.SourceDescription != "" {
		rows := make([]SourceAddressTableRow, 0, len(sourceAddressTableV1))
		for _, row := range sourceAddressTableV1 {
			if row.CanonicalDescription != cfg.SourceDescription {
				continue
			}
			if cfg.PriorityIndex != "" && row.PriorityIndex != cfg.PriorityIndex {
				continue
			}
			rows = append(rows, row)
		}
		return rows, nil
	}

	rows := make([]SourceAddressTableRow, 0, len(helianthusGatewayDefaultPolicyV1))
	for _, source := range helianthusGatewayDefaultPolicyV1 {
		row, ok := LookupSourceAddress(source)
		if !ok {
			return nil, &ErrSourceAddressConfig{Reason: fmt.Sprintf("default policy source 0x%02x missing from table", source)}
		}
		if cfg.PriorityIndex != "" && row.PriorityIndex != cfg.PriorityIndex {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func validateSourceAddressSelectionConfig(cfg SourceAddressSelectionConfig) error {
	if cfg.ExplicitSourceSet && cfg.SourceDescription != "" {
		return &ErrSourceAddressConfig{Reason: "explicit source cannot be combined with source description"}
	}
	if cfg.ExplicitSourceSet && cfg.PriorityIndex != "" {
		return &ErrSourceAddressConfig{Reason: "explicit source cannot be combined with priority index"}
	}
	if cfg.SourceDescription != "" && !isKnownSourceAddressDescription(cfg.SourceDescription) {
		return &ErrSourceAddressConfig{Reason: fmt.Sprintf("unknown source description %q", cfg.SourceDescription)}
	}
	if cfg.PriorityIndex != "" && !isKnownSourceAddressPriority(cfg.PriorityIndex) {
		return &ErrSourceAddressConfig{Reason: fmt.Sprintf("unknown source priority index %q", cfg.PriorityIndex)}
	}
	return nil
}

func normalizeSourceAddressSelectionConfig(cfg SourceAddressSelectionConfig) SourceAddressSelectionConfig {
	if cfg.ListenWarmup <= 0 {
		cfg.ListenWarmup = defaultSourceAddressListenWarmup
	}
	if cfg.InquiryCooldown <= 0 {
		cfg.InquiryCooldown = defaultSourceAddressInquiryCooldown
	}
	if cfg.InquiryMaxAttempts <= 0 {
		cfg.InquiryMaxAttempts = defaultSourceAddressInquiryMaxAttempts
	}
	return cfg
}

func (cfg SourceAddressSelectionConfig) selectionMode() SourceAddressSelectionMode {
	if cfg.ExplicitSourceSet {
		return SourceAddressSelectionModeExplicitValidateOnly
	}
	if cfg.SourceDescription != "" {
		return SourceAddressSelectionModeSourceDescriptionConstrainedPolicy
	}
	if cfg.PriorityIndex != "" {
		return SourceAddressSelectionModePriorityFilteredDefaultPolicy
	}
	return SourceAddressSelectionModeDefaultPolicy
}

func isKnownSourceAddressPriority(priority SourceAddressPriorityIndex) bool {
	switch priority {
	case SourceAddressPriorityP0, SourceAddressPriorityP1, SourceAddressPriorityP2, SourceAddressPriorityP3, SourceAddressPriorityP4:
		return true
	default:
		return false
	}
}

func isKnownSourceAddressDescription(description SourceAddressDescription) bool {
	for _, row := range sourceAddressTableV1 {
		if row.CanonicalDescription == description {
			return true
		}
	}
	return false
}

func sourceAddressPriorityRank(priority SourceAddressPriorityIndex) int {
	switch priority {
	case SourceAddressPriorityP0:
		return 0
	case SourceAddressPriorityP1:
		return 1
	case SourceAddressPriorityP2:
		return 2
	case SourceAddressPriorityP3:
		return 3
	case SourceAddressPriorityP4:
		return 4
	default:
		return 255
	}
}

type sourceAddressObservation struct {
	sourceCount             map[byte]int
	targetCount             map[byte]int
	observedSources         map[byte]struct{}
	observedSourceAddresses map[byte]struct{}
	observedProbableTargets map[byte]struct{}
}

func newSourceAddressObservation() *sourceAddressObservation {
	return &sourceAddressObservation{
		sourceCount:             make(map[byte]int),
		targetCount:             make(map[byte]int),
		observedSources:         make(map[byte]struct{}),
		observedSourceAddresses: make(map[byte]struct{}),
		observedProbableTargets: make(map[byte]struct{}),
	}
}

func (o *sourceAddressObservation) addFrame(frame Frame) {
	o.sourceCount[frame.Source]++
	o.targetCount[frame.Target]++
	o.observedSources[frame.Source] = struct{}{}
	if IsInitiatorCapableAddress(frame.Source) {
		if o.sourceCount[frame.Source] >= defaultSourceAddressLikelyTargetSourceMin {
			o.observedSourceAddresses[frame.Source] = struct{}{}
		}
	} else {
		o.observedProbableTargets[frame.Source] = struct{}{}
	}
	if o.targetCount[frame.Target] >= defaultSourceAddressLikelyTargetTargetMin {
		o.observedProbableTargets[frame.Target] = struct{}{}
	}
}

func (o *sourceAddressObservation) topTalkers(limit int) []byte {
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

func resolveKnownAddressOccupancy(
	evidence KnownAddressEvidence,
	address byte,
	observation *sourceAddressObservation,
) SourceAddressOccupancyState {
	state := SourceAddressOccupancyUnknown
	for _, record := range evidence.CurrentObservation {
		if record.Address == address {
			state = mergeSourceAddressOccupancy(state, record.State)
		}
	}
	for _, record := range evidence.Topology {
		if record.Address == address {
			state = mergeSourceAddressOccupancy(state, record.State)
		}
	}
	for _, record := range evidence.Cache {
		if record.Address == address {
			state = mergeSourceAddressOccupancy(state, record.State)
		}
	}
	for _, staleAddress := range evidence.StaleKnownDevices {
		if staleAddress == address {
			state = mergeSourceAddressOccupancy(state, SourceAddressOccupancyStaleKnownDevice)
		}
	}
	for _, record := range evidence.CompanionProvenance {
		if record.Address == address {
			state = mergeSourceAddressOccupancy(state, record.State)
		}
	}
	if observation != nil {
		if _, ok := observation.observedSourceAddresses[address]; ok {
			state = mergeSourceAddressOccupancy(state, SourceAddressOccupancyObservedOccupied)
		}
		if observation.sourceCount[address] >= defaultSourceAddressLikelyTargetSourceMin {
			state = mergeSourceAddressOccupancy(state, SourceAddressOccupancyObservedOccupied)
		}
		if observation.targetCount[address] >= defaultSourceAddressLikelyTargetTargetMin {
			state = mergeSourceAddressOccupancy(state, SourceAddressOccupancyObservedOccupied)
		}
	}
	return state
}

func mergeSourceAddressOccupancy(current, next SourceAddressOccupancyState) SourceAddressOccupancyState {
	switch next {
	case SourceAddressOccupancyObservedOccupied:
		return SourceAddressOccupancyObservedOccupied
	case SourceAddressOccupancyStaleKnownDevice:
		if current != SourceAddressOccupancyObservedOccupied {
			return SourceAddressOccupancyStaleKnownDevice
		}
	case SourceAddressOccupancyObservedFree:
		if current == SourceAddressOccupancyUnknown {
			return SourceAddressOccupancyObservedFree
		}
	}
	return current
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
