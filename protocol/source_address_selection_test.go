package protocol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedSourceAddressSelectionBus struct {
	mu           sync.Mutex
	listenCalls  int
	inquiryCalls int
	listenFrames [][]Frame
	inquiryErr   error
}

func (b *scriptedSourceAddressSelectionBus) Listen(ctx context.Context, onFrame func(Frame)) error {
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

func (b *scriptedSourceAddressSelectionBus) InquiryExistence(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inquiryCalls++
	return b.inquiryErr
}

func nilContext() context.Context {
	return nil
}

func TestSourceAddressTable_DocsContractConstants(t *testing.T) {
	t.Parallel()

	if SourceAddressTableAnchor != "#ebus-source-address-table-v1" {
		t.Fatalf("SourceAddressTableAnchor = %q", SourceAddressTableAnchor)
	}
	if SourceAddressTableVersion != "ebus-source-address-table/v1" {
		t.Fatalf("SourceAddressTableVersion = %q", SourceAddressTableVersion)
	}
	if SourceAddressTableHash != "e78954445087f63064818ab60a2739b9a6b9bf0ae0147fbe92aac5ac76592103" {
		t.Fatalf("SourceAddressTableHash = %q", SourceAddressTableHash)
	}

	sum := sha256.Sum256([]byte(sourceAddressTableMarkdownForHash()))
	if got := hex.EncodeToString(sum[:]); got != SourceAddressTableHash {
		t.Fatalf("source address table hash = %s; want %s", got, SourceAddressTableHash)
	}
}

func TestSourceAddressTable_OfficialSummariesAndCanonicalDescriptions(t *testing.T) {
	t.Parallel()

	expected := map[byte]struct {
		official  string
		canonical SourceAddressDescription
	}{
		0x00: {"PC/Modem", SourceAddressDescriptionPCModem},
		0x10: {"Heating controller", SourceAddressDescriptionHeatingRegulator},
		0x30: {"Heating circuit controller 1", SourceAddressDescriptionHeatingCircuitRegulator1},
		0x70: {"Heating circuit controller 2", SourceAddressDescriptionHeatingCircuitRegulator2},
		0xF0: {"Heating circuit controller 3", SourceAddressDescriptionHeatingCircuitRegulator3},
		0x01: {"Hand programmer / Remote control", SourceAddressDescriptionHandheldProgrammerRemote},
		0x11: {"Bus interface / Climate controller", SourceAddressDescriptionBusInterfaceClimateRegulator},
		0x31: {"Bus interface", SourceAddressDescriptionBusInterface},
		0x71: {"Heating controller", SourceAddressDescriptionHeatingRegulator},
		0xF1: {"Heating controller", SourceAddressDescriptionHeatingRegulator},
		0x03: {"Burner controller 1", SourceAddressDescriptionCombustionController1},
		0x13: {"Burner controller 2", SourceAddressDescriptionCombustionController2},
		0x33: {"Burner controller 3", SourceAddressDescriptionCombustionController3},
		0x73: {"Burner controller 4", SourceAddressDescriptionCombustionController4},
		0xF3: {"Burner controller 5", SourceAddressDescriptionCombustionController5},
		0x07: {"empty", SourceAddressDescriptionNotPreallocated},
		0x17: {"Heating controller recommendation", SourceAddressDescriptionNotPreallocated},
		0x37: {"Heating controller recommendation", SourceAddressDescriptionNotPreallocated},
		0x77: {"Heating controller recommendation", SourceAddressDescriptionNotPreallocated},
		0xF7: {"Heating controller recommendation", SourceAddressDescriptionNotPreallocated},
		0x0F: {"Clock module / Radio clock module", SourceAddressDescriptionClockRadioClockModule},
		0x1F: {"Burner controller 6 recommendation", SourceAddressDescriptionNotPreallocated},
		0x3F: {"Burner controller 7 recommendation", SourceAddressDescriptionNotPreallocated},
		0x7F: {"Burner controller 8 recommendation", SourceAddressDescriptionNotPreallocated},
		0xFF: {"PC", SourceAddressDescriptionPC},
	}

	rows := SourceAddressTableRows()
	if len(rows) != len(expected) {
		t.Fatalf("source table row count = %d; want %d", len(rows), len(expected))
	}
	for _, row := range rows {
		want, ok := expected[row.Source]
		if !ok {
			t.Fatalf("unexpected source row 0x%02x", row.Source)
		}
		if row.OfficialDescriptionSummary != want.official {
			t.Fatalf("official summary for 0x%02x = %q; want %q", row.Source, row.OfficialDescriptionSummary, want.official)
		}
		if row.CanonicalDescription != want.canonical {
			t.Fatalf("canonical description for 0x%02x = %q; want %q", row.Source, row.CanonicalDescription, want.canonical)
		}
		if row.Companion != CompanionAddressForSource(row.Source) {
			t.Fatalf("companion for 0x%02x = 0x%02x; want modulo companion 0x%02x", row.Source, row.Companion, CompanionAddressForSource(row.Source))
		}
	}
}

func TestSourceAddressSelector_DefaultPolicyOrderAndCompanionWrap(t *testing.T) {
	t.Parallel()

	wantOrder := []byte{0xFF, 0x7F, 0x3F, 0x1F, 0xF7, 0x77, 0x37, 0x17, 0x07, 0x11, 0x31, 0x00}
	if got := HelianthusGatewayDefaultPolicy(); !equalBytes(got, wantOrder) {
		t.Fatalf("HelianthusGatewayDefaultPolicy = %s; want %s", hexBytes(got), hexBytes(wantOrder))
	}
	if got := CompanionAddressForSource(0xFF); got != 0x04 {
		t.Fatalf("CompanionAddressForSource(0xff) = 0x%02x; want 0x04", got)
	}
	if got := CompanionAddressForSource(0xF7); got != 0xFC {
		t.Fatalf("CompanionAddressForSource(0xf7) = 0x%02x; want 0xfc", got)
	}

	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup: 2 * time.Millisecond,
	})
	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x7F {
		t.Fatalf("Source = 0x%02x; want 0x7f because 0xff companion is unknown", result.Source)
	}
	if !equalBytes(result.Metrics.CandidatesConsidered, []byte{0xFF, 0x7F}) {
		t.Fatalf("CandidatesConsidered = %s; want 0xff,0x7f", hexBytes(result.Metrics.CandidatesConsidered))
	}
	if result.Metrics.RejectionReasons[0xFF][0] != "companion-unknown" {
		t.Fatalf("RejectionReasons[0xff] = %v; want companion-unknown", result.Metrics.RejectionReasons[0xFF])
	}
}

func TestSourceAddressSelector_ZeroFFRequiresObservedFreeCompanion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		evidence   KnownAddressEvidence
		wantSource byte
		wantErr    bool
		wantReason string
	}{
		{
			name: "observed free companion validates",
			evidence: KnownAddressEvidence{
				CompanionProvenance: []KnownAddressOccupancy{{Address: 0x04, State: SourceAddressOccupancyObservedFree}},
			},
			wantSource: 0xFF,
		},
		{
			name:       "unknown companion withheld",
			wantSource: 0x7F,
		},
		{
			name: "occupied companion withheld",
			evidence: KnownAddressEvidence{
				CurrentObservation: []KnownAddressOccupancy{{Address: 0x04, State: SourceAddressOccupancyObservedOccupied}},
			},
			wantSource: 0x7F,
		},
		{
			name: "stale companion withheld",
			evidence: KnownAddressEvidence{
				StaleKnownDevices: []byte{0x04},
			},
			wantSource: 0x7F,
		},
		{
			name: "explicit unknown companion rejects",
			evidence: KnownAddressEvidence{
				Cache: []KnownAddressOccupancy{{Address: 0xF7, State: SourceAddressOccupancyObservedFree}},
			},
			wantErr:    true,
			wantReason: "companion-unknown",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := SourceAddressSelectionConfig{
				ListenWarmup: 2 * time.Millisecond,
				Evidence:     tc.evidence,
			}
			if tc.wantErr {
				cfg.ExplicitSource = 0xFF
				cfg.ExplicitSourceSet = true
			}
			selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, cfg)
			result, err := selector.Select(context.Background())
			if tc.wantErr {
				var validationErr *ErrSourceAddressValidation
				if !errors.As(err, &validationErr) {
					t.Fatalf("Select error = %v; want ErrSourceAddressValidation", err)
				}
				if validationErr.Reason != tc.wantReason {
					t.Fatalf("validation reason = %q; want %q", validationErr.Reason, tc.wantReason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Select error = %v", err)
			}
			if result.Source != tc.wantSource {
				t.Fatalf("Source = 0x%02x; want 0x%02x", result.Source, tc.wantSource)
			}
		})
	}
}

func TestSourceAddressSelector_ConstrainedDescriptionCannotEscape(t *testing.T) {
	t.Parallel()

	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup:      2 * time.Millisecond,
		SourceDescription: SourceAddressDescriptionHeatingRegulator,
		Evidence: KnownAddressEvidence{
			CurrentObservation: []KnownAddressOccupancy{
				{Address: 0x10, State: SourceAddressOccupancyObservedOccupied},
				{Address: 0x71, State: SourceAddressOccupancyObservedOccupied},
				{Address: 0xF1, State: SourceAddressOccupancyObservedOccupied},
			},
		},
	})

	_, err := selector.Select(context.Background())
	var noAvailableErr *ErrNoAvailableSourceAddress
	if !errors.As(err, &noAvailableErr) {
		t.Fatalf("Select error = %v; want ErrNoAvailableSourceAddress", err)
	}
	wantConsidered := []byte{0x10, 0x71, 0xF1}
	if !equalBytes(noAvailableErr.Metrics.CandidatesConsidered, wantConsidered) {
		t.Fatalf("CandidatesConsidered = %s; want %s", hexBytes(noAvailableErr.Metrics.CandidatesConsidered), hexBytes(wantConsidered))
	}
	for _, freeUseRecommendation := range []byte{0x17, 0x37, 0x77, 0xF7} {
		if _, ok := noAvailableErr.Metrics.RejectionReasons[freeUseRecommendation]; ok {
			t.Fatalf("free-use recommendation 0x%02x was considered as heating regulator", freeUseRecommendation)
		}
	}
}

func TestSourceAddressSelector_ExplicitAddressBypassesCandidateSearchButValidates(t *testing.T) {
	t.Parallel()

	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup:      2 * time.Millisecond,
		ExplicitSource:    0x71,
		ExplicitSourceSet: true,
		SourceDescription: "",
		Evidence: KnownAddressEvidence{
			Cache: []KnownAddressOccupancy{{Address: 0xF7, State: SourceAddressOccupancyObservedFree}},
		},
	})

	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x71 {
		t.Fatalf("Source = 0x%02x; want explicit 0x71", result.Source)
	}
	if result.Mode != SourceAddressSelectionModeExplicitValidateOnly {
		t.Fatalf("Mode = %q; want %q", result.Mode, SourceAddressSelectionModeExplicitValidateOnly)
	}
	if !equalBytes(result.Metrics.CandidatesConsidered, []byte{0x71}) {
		t.Fatalf("CandidatesConsidered = %s; want 0x71", hexBytes(result.Metrics.CandidatesConsidered))
	}
}

func TestSourceAddressSelector_SelectionModeContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  SourceAddressSelectionConfig
		want SourceAddressSelectionMode
	}{
		{
			name: "default",
			cfg:  SourceAddressSelectionConfig{},
			want: SourceAddressSelectionModeDefaultPolicy,
		},
		{
			name: "source description constrained",
			cfg: SourceAddressSelectionConfig{
				SourceDescription: SourceAddressDescriptionBusInterface,
			},
			want: SourceAddressSelectionModeSourceDescriptionConstrainedPolicy,
		},
		{
			name: "priority filtered default",
			cfg: SourceAddressSelectionConfig{
				PriorityIndex: SourceAddressPriorityP3,
			},
			want: SourceAddressSelectionModePriorityFilteredDefaultPolicy,
		},
		{
			name: "explicit validate only",
			cfg: SourceAddressSelectionConfig{
				ExplicitSource:    0x71,
				ExplicitSourceSet: true,
			},
			want: SourceAddressSelectionModeExplicitValidateOnly,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.cfg
			cfg.ListenWarmup = 2 * time.Millisecond
			selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, cfg)
			result, err := selector.Select(context.Background())
			if err != nil {
				t.Fatalf("Select error = %v", err)
			}
			if result.Mode != tc.want {
				t.Fatalf("Mode = %q; want %q", result.Mode, tc.want)
			}
		})
	}
}

func TestSourceAddressSelector_ExplicitAddressRejectsDescriptionOrPriority(t *testing.T) {
	t.Parallel()

	tests := []SourceAddressSelectionConfig{
		{
			ListenWarmup:      2 * time.Millisecond,
			ExplicitSource:    0x71,
			ExplicitSourceSet: true,
			SourceDescription: SourceAddressDescriptionHeatingRegulator,
		},
		{
			ListenWarmup:      2 * time.Millisecond,
			ExplicitSource:    0x71,
			ExplicitSourceSet: true,
			PriorityIndex:     SourceAddressPriorityP1,
		},
	}

	for _, cfg := range tests {
		cfg := cfg
		t.Run(string(cfg.SourceDescription)+string(cfg.PriorityIndex), func(t *testing.T) {
			t.Parallel()
			selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, cfg)
			_, err := selector.Select(context.Background())
			var configErr *ErrSourceAddressConfig
			if !errors.As(err, &configErr) {
				t.Fatalf("Select error = %v; want ErrSourceAddressConfig", err)
			}
		})
	}
}

func TestSourceAddressSelector_PriorityFilteringUsesDefaultPolicyOnly(t *testing.T) {
	t.Parallel()

	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup:  2 * time.Millisecond,
		PriorityIndex: SourceAddressPriorityP3,
		Evidence: KnownAddressEvidence{
			CurrentObservation: []KnownAddressOccupancy{
				{Address: 0xF7, State: SourceAddressOccupancyObservedOccupied},
				{Address: 0x77, State: SourceAddressOccupancyObservedOccupied},
				{Address: 0x37, State: SourceAddressOccupancyObservedOccupied},
				{Address: 0x17, State: SourceAddressOccupancyObservedOccupied},
			},
		},
	})

	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x07 {
		t.Fatalf("Source = 0x%02x; want remaining p3 default-policy source 0x07", result.Source)
	}
	wantConsidered := []byte{0xF7, 0x77, 0x37, 0x17, 0x07}
	if !equalBytes(result.Metrics.CandidatesConsidered, wantConsidered) {
		t.Fatalf("CandidatesConsidered = %s; want %s", hexBytes(result.Metrics.CandidatesConsidered), hexBytes(wantConsidered))
	}
}

func TestSourceAddressSelector_OccupancyStates(t *testing.T) {
	t.Parallel()

	evidence := KnownAddressEvidence{
		CurrentObservation: []KnownAddressOccupancy{{Address: 0x10, State: SourceAddressOccupancyObservedFree}},
		Topology:           []KnownAddressOccupancy{{Address: 0x71, State: SourceAddressOccupancyObservedOccupied}},
		Cache:              []KnownAddressOccupancy{{Address: 0xF1, State: SourceAddressOccupancyObservedFree}},
		StaleKnownDevices:  []byte{0xF1},
		CompanionProvenance: []KnownAddressOccupancy{
			{Address: 0x04, State: SourceAddressOccupancyObservedFree},
		},
	}

	tests := []struct {
		address byte
		want    SourceAddressOccupancyState
	}{
		{0x10, SourceAddressOccupancyObservedFree},
		{0x71, SourceAddressOccupancyObservedOccupied},
		{0xF1, SourceAddressOccupancyStaleKnownDevice},
		{0x04, SourceAddressOccupancyObservedFree},
		{0x31, SourceAddressOccupancyUnknown},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("0x%02x", tc.address), func(t *testing.T) {
			t.Parallel()
			if got := ResolveKnownAddressOccupancy(evidence, tc.address); got != tc.want {
				t.Fatalf("ResolveKnownAddressOccupancy(0x%02x) = %q; want %q", tc.address, got, tc.want)
			}
		})
	}
}

func TestSourceAddressSelector_LiveObservationMarksOccupiedAfterTwoFrames(t *testing.T) {
	t.Parallel()

	bus := &scriptedSourceAddressSelectionBus{
		listenFrames: [][]Frame{{
			{Source: 0x7F, Target: 0x15},
			{Source: 0x7F, Target: 0xFE},
		}},
	}
	selector := NewSourceAddressSelector(bus, SourceAddressSelectionConfig{
		ListenWarmup: 2 * time.Millisecond,
	})

	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x3F {
		t.Fatalf("Source = 0x%02x; want 0x3f because 0xff companion is unknown and 0x7f is occupied", result.Source)
	}
	if result.Metrics.Occupancy[0x7F] != SourceAddressOccupancyObservedOccupied {
		t.Fatalf("0x7f occupancy = %q; want observed_occupied", result.Metrics.Occupancy[0x7F])
	}
}

func TestSourceAddressSelector_CompanionObservedAsTargetRejectsCandidate(t *testing.T) {
	t.Parallel()

	bus := &scriptedSourceAddressSelectionBus{
		listenFrames: [][]Frame{{
			{Source: 0x31, Target: 0x04},
			{Source: 0x1F, Target: 0x04},
		}},
	}
	selector := NewSourceAddressSelector(bus, SourceAddressSelectionConfig{
		ListenWarmup: 2 * time.Millisecond,
	})

	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x7F {
		t.Fatalf("Source = 0x%02x; want 0x7f", result.Source)
	}
	if got := result.Metrics.RejectionReasons[0xFF][0]; got != "companion-observed-occupied" {
		t.Fatalf("RejectionReasons[0xff][0] = %q; want companion-observed-occupied", got)
	}
}

func TestSourceAddressSelector_PropagatesInquiryCancellation(t *testing.T) {
	t.Parallel()

	bus := &scriptedSourceAddressSelectionBus{inquiryErr: context.Canceled}
	selector := NewSourceAddressSelector(bus, SourceAddressSelectionConfig{
		ListenWarmup:       2 * time.Millisecond,
		InquiryEnabled:     true,
		InquiryMaxAttempts: 1,
	})

	_, err := selector.Select(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Select error = %v; want context.Canceled", err)
	}
}

func TestSourceAddressSelector_IgnoresOptionalInquiryFailure(t *testing.T) {
	t.Parallel()

	bus := &scriptedSourceAddressSelectionBus{inquiryErr: errors.New("inquiry unsupported")}
	selector := NewSourceAddressSelector(bus, SourceAddressSelectionConfig{
		ListenWarmup:       2 * time.Millisecond,
		InquiryEnabled:     true,
		InquiryMaxAttempts: 1,
	})

	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v; want optional inquiry failure to be non-fatal", err)
	}
	if result.Source != 0x7F {
		t.Fatalf("Source = 0x%02x; want 0x7f from candidate evaluation after optional inquiry failure", result.Source)
	}
	if bus.inquiryCalls != 1 {
		t.Fatalf("inquiry calls = %d; want 1", bus.inquiryCalls)
	}
}

func TestSourceAddressSelector_NilContextDefaultsToBackground(t *testing.T) {
	t.Parallel()

	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup: 2 * time.Millisecond,
	})

	result, err := selector.Select(nilContext())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x7F {
		t.Fatalf("Source = 0x%02x; want 0x7f", result.Source)
	}
}

func TestSourceAddressSelector_OfficialArbitrationRankSeparateFromDefaultPolicy(t *testing.T) {
	t.Parallel()

	pairs := []struct {
		a byte
		b byte
	}{
		{0x00, 0x01},
		{0x01, 0x03},
		{0x03, 0x07},
		{0x07, 0x0F},
		{0x17, 0x37},
	}
	for _, pair := range pairs {
		pair := pair
		t.Run(fmt.Sprintf("0x%02x_before_0x%02x", pair.a, pair.b), func(t *testing.T) {
			t.Parallel()
			got, ok := CompareOfficialSourceAddressArbitration(pair.a, pair.b)
			if !ok {
				t.Fatalf("CompareOfficialSourceAddressArbitration returned ok=false")
			}
			if got >= 0 {
				t.Fatalf("CompareOfficialSourceAddressArbitration(0x%02x,0x%02x) = %d; want 0x%02x to win", pair.a, pair.b, got, pair.a)
			}
		})
	}

	defaultPolicy := HelianthusGatewayDefaultPolicy()
	if defaultPolicy[0] != 0xFF {
		t.Fatalf("default policy first source = 0x%02x; want 0xff", defaultPolicy[0])
	}
	cmp, ok := CompareOfficialSourceAddressArbitration(0x00, 0xFF)
	if !ok || cmp >= 0 {
		t.Fatalf("official arbitration compare 0x00 vs 0xff = %d, %v; want 0x00 to win", cmp, ok)
	}
}

func sourceAddressTableMarkdownForHash() string {
	var builder strings.Builder
	builder.WriteString("| Source | Priority index | Arbitration nibble | Official description summary | Canonical description | Free-use | Recommended for | Companion |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, row := range sourceAddressTableV1 {
		freeUse := "no"
		if row.FreeUse {
			freeUse = "yes"
		}
		fmt.Fprintf(
			&builder,
			"| `0x%02X` | %s | `0x%X` | %s | %s | %s | %s | `0x%02X` |\n",
			row.Source,
			row.PriorityIndex,
			row.ArbitrationNibble,
			row.OfficialDescriptionSummary,
			row.CanonicalDescription,
			freeUse,
			row.RecommendedFor,
			row.Companion,
		)
	}
	return builder.String()
}

// -----------------------------------------------------------------------------
// M4_SOURCE_SELECTION_HINT (runtime-state-w19-26.locked):
// HintCandidate biases candidate ordering toward a cached preferred source
// from a prior admission cycle. Validation is unchanged: a hint that fails
// validation falls through to the regular candidate order.
// -----------------------------------------------------------------------------

func TestSourceAddressSelector_HintCandidateAcceptedFirst(t *testing.T) {
	t.Parallel()

	// Default policy normally returns 0x7F first (0xFF companion unknown).
	// Pass a hint of 0x77 — a row LATER in the default policy — and verify
	// the selector tries it first and admits it.
	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup:     2 * time.Millisecond,
		HintCandidate:    0x77,
		HintCandidateSet: true,
	})
	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x77 {
		t.Fatalf("Source = 0x%02x; want 0x77 (hint biased to head of candidate order)", result.Source)
	}
	if !result.Metrics.HintCandidateSet || result.Metrics.HintCandidate != 0x77 {
		t.Fatalf("HintCandidate=%v set=%v; want 0x77 set=true", result.Metrics.HintCandidate, result.Metrics.HintCandidateSet)
	}
	if !result.Metrics.HintAccepted {
		t.Fatalf("HintAccepted = false; want true when hint succeeded")
	}
	if result.Metrics.HintRejectionReason != "" {
		t.Fatalf("HintRejectionReason = %q; want empty when hint accepted", result.Metrics.HintRejectionReason)
	}
	if len(result.Metrics.CandidatesConsidered) == 0 || result.Metrics.CandidatesConsidered[0] != 0x77 {
		t.Fatalf("CandidatesConsidered[0] = 0x%02x; want 0x77 first", firstByteOrZero(result.Metrics.CandidatesConsidered))
	}
}

func TestSourceAddressSelector_HintCandidateRejectionFallsBackToDefaultPolicy(t *testing.T) {
	t.Parallel()

	// Hint = 0xFF — default policy's first candidate, but its companion
	// 0x04 has unknown occupancy → validator rejects with "companion-unknown".
	// Selector must fall through to 0x7F (next default-policy row) without
	// erroring or mutating the hint state — and expose the rejection reason.
	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup:     2 * time.Millisecond,
		HintCandidate:    0xFF,
		HintCandidateSet: true,
	})
	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x7F {
		t.Fatalf("Source = 0x%02x; want 0x7f after hint=0xff rejection", result.Source)
	}
	if !result.Metrics.HintCandidateSet || result.Metrics.HintCandidate != 0xFF {
		t.Fatalf("HintCandidate=%v set=%v; want 0xff set=true", result.Metrics.HintCandidate, result.Metrics.HintCandidateSet)
	}
	if result.Metrics.HintAccepted {
		t.Fatalf("HintAccepted = true; want false when hint rejected")
	}
	if result.Metrics.HintRejectionReason != "companion-unknown" {
		t.Fatalf("HintRejectionReason = %q; want companion-unknown", result.Metrics.HintRejectionReason)
	}
	// Default-policy fall-through must keep its order intact.
	if len(result.Metrics.CandidatesConsidered) < 2 ||
		result.Metrics.CandidatesConsidered[0] != 0xFF ||
		result.Metrics.CandidatesConsidered[1] != 0x7F {
		t.Fatalf("CandidatesConsidered = %s; want 0xff,0x7f,...", hexBytes(result.Metrics.CandidatesConsidered))
	}
}

func TestSourceAddressSelector_HintCandidateNotInTableIgnored(t *testing.T) {
	t.Parallel()

	// 0x42 is not a row in sourceAddressTableV1. Selector must silently
	// ignore the hint and proceed with default policy. Metrics should
	// still reflect HintCandidateSet=true so observability can attribute
	// the absence to a missing row, not "no hint passed".
	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup:     2 * time.Millisecond,
		HintCandidate:    0x42,
		HintCandidateSet: true,
	})
	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x7F {
		t.Fatalf("Source = 0x%02x; want 0x7f (default policy unaffected)", result.Source)
	}
	if result.Metrics.HintAccepted {
		t.Fatalf("HintAccepted = true; want false when hint not in table")
	}
	if result.Metrics.HintRejectionReason != "" {
		t.Fatalf("HintRejectionReason = %q; want empty (hint never tried)", result.Metrics.HintRejectionReason)
	}
}

func TestSourceAddressSelector_HintCandidateIgnoredWhenExplicitSourceSet(t *testing.T) {
	t.Parallel()

	// Operator pinned ExplicitSource — hint must be silently ignored to
	// avoid biasing the operator-supplied candidate set.
	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup:      2 * time.Millisecond,
		ExplicitSource:    0x71,
		ExplicitSourceSet: true,
		HintCandidate:     0x77,
		HintCandidateSet:  true,
	})
	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x71 {
		t.Fatalf("Source = 0x%02x; want 0x71 (explicit source pinned, hint ignored)", result.Source)
	}
	// HintCandidateSet metric must report false because explicit source
	// suppressed the hint — observability shouldn't claim the hint was used.
	if result.Metrics.HintCandidateSet || result.Metrics.HintAccepted {
		t.Fatalf("Hint metrics should be cleared when explicit source set; got set=%v accepted=%v",
			result.Metrics.HintCandidateSet, result.Metrics.HintAccepted)
	}
}

func TestSourceAddressSelector_HintCandidateSetWithZeroIsHonored(t *testing.T) {
	t.Parallel()

	// HintCandidate=0x00 + HintCandidateSet=true means "hint = 0x00", not
	// "no hint" — the byte-zero ambiguity is resolved by the explicit
	// HintCandidateSet flag. 0x00 is in the default policy table (last
	// entry), and without occupancy evidence its companion (0x05) is
	// unknown but still allowed by the validator. Selector must promote
	// 0x00 to head of the candidate sequence and admit it.
	selector := NewSourceAddressSelector(&scriptedSourceAddressSelectionBus{}, SourceAddressSelectionConfig{
		ListenWarmup:     2 * time.Millisecond,
		HintCandidate:    0x00,
		HintCandidateSet: true,
	})
	result, err := selector.Select(context.Background())
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if result.Source != 0x00 {
		t.Fatalf("Source = 0x%02x; want 0x00 (hint biased to head of candidate order)", result.Source)
	}
	if !result.Metrics.HintCandidateSet || result.Metrics.HintCandidate != 0x00 {
		t.Fatalf("HintCandidate=%v set=%v; want 0x00 set=true", result.Metrics.HintCandidate, result.Metrics.HintCandidateSet)
	}
	if !result.Metrics.HintAccepted {
		t.Fatalf("HintAccepted = false; want true (hint=0x00 honored, not treated as unset)")
	}
	if len(result.Metrics.CandidatesConsidered) == 0 || result.Metrics.CandidatesConsidered[0] != 0x00 {
		t.Fatalf("CandidatesConsidered[0] = 0x%02x; want 0x00 first", firstByteOrZero(result.Metrics.CandidatesConsidered))
	}
}

func firstByteOrZero(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hexBytes(values []byte) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("0x%02x", value))
	}
	return strings.Join(parts, ",")
}
