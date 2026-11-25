package engine

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

type WatchFamily string

const (
	WatchFamilyB509 WatchFamily = "B509"
	WatchFamilyB516 WatchFamily = "B516"
	WatchFamilyB524 WatchFamily = "B524"
	WatchFamilyB555 WatchFamily = "B555"
)

type WatchSemanticClass string

const (
	WatchSemanticClassState     WatchSemanticClass = "state"
	WatchSemanticClassConfig    WatchSemanticClass = "config"
	WatchSemanticClassDiscovery WatchSemanticClass = "discovery"
	WatchSemanticClassDebug     WatchSemanticClass = "debug"
)

type WatchFreshnessProfile string

const (
	WatchFreshnessProfileStateFast WatchFreshnessProfile = "state_fast"
	WatchFreshnessProfileStateSlow WatchFreshnessProfile = "state_slow"
	WatchFreshnessProfileConfig    WatchFreshnessProfile = "config"
	WatchFreshnessProfileDiscovery WatchFreshnessProfile = "discovery"
	WatchFreshnessProfileDebug     WatchFreshnessProfile = "debug"
)

type WatchCorrelationPolicy string

const (
	WatchCorrelationPolicyRequestResponse   WatchCorrelationPolicy = "request_response"
	WatchCorrelationPolicyBroadcastSelector WatchCorrelationPolicy = "broadcast_selector"
	WatchCorrelationPolicyRecordInvalidate  WatchCorrelationPolicy = "record_and_invalidate"
	WatchCorrelationPolicyRecordOnly        WatchCorrelationPolicy = "record_only"
	WatchCorrelationPolicyInvalidateOnly    WatchCorrelationPolicy = "invalidate_only"
)

type WatchDirectApplyPolicy string

const (
	WatchDirectApplyPolicyNever           WatchDirectApplyPolicy = "never"
	WatchDirectApplyPolicyStateDefault    WatchDirectApplyPolicy = "state_default"
	WatchDirectApplyPolicyConfigOptIn     WatchDirectApplyPolicy = "config_opt_in"
	WatchDirectApplyPolicyEnergyMergeOnly WatchDirectApplyPolicy = "energy_merge_only"
)

type WatchActivationSource string

const (
	WatchActivationSourcePoller       WatchActivationSource = "poller"
	WatchActivationSourceWriteConfirm WatchActivationSource = "write_confirm"
	WatchActivationSourceTooling      WatchActivationSource = "tooling"
	WatchActivationSourceOperator     WatchActivationSource = "operator"
)

type WatchObservationState string

const (
	WatchObservationStateActive      WatchObservationState = "active"
	WatchObservationStateInactive    WatchObservationState = "inactive"
	WatchObservationStateCatalogMiss WatchObservationState = "catalog_miss"
)

var watchFreshnessProfileDefaults = map[WatchFreshnessProfile]time.Duration{
	WatchFreshnessProfileStateFast: 30 * time.Second,
	WatchFreshnessProfileStateSlow: 120 * time.Second,
	WatchFreshnessProfileConfig:    5 * time.Minute,
	WatchFreshnessProfileDiscovery: time.Hour,
	WatchFreshnessProfileDebug:     0,
}

var watchActivationSourceOrder = map[WatchActivationSource]int{
	WatchActivationSourcePoller:       0,
	WatchActivationSourceWriteConfirm: 1,
	WatchActivationSourceTooling:      2,
	WatchActivationSourceOperator:     3,
}

type WatchKey interface {
	fmt.Stringer
	Canonical() string
	Family() WatchFamily
}

// WatchCatalog stores each key's canonical string. Admission, cloning and
// selector validation belong to the collaborator that built the catalog.

type B524WatchKey struct {
	Target          byte
	Opcode          byte
	Group           byte
	Instance        byte
	RegisterAddress uint16
}

func NewB524WatchKey(target, opcode, group, instance byte, registerAddress uint16) B524WatchKey {
	return B524WatchKey{
		Target:          target,
		Opcode:          opcode,
		Group:           group,
		Instance:        instance,
		RegisterAddress: registerAddress,
	}
}

func (key B524WatchKey) Canonical() string {
	return fmt.Sprintf("b524:%02x:%02x:%02x:%02x:%04x", key.Target, key.Opcode, key.Group, key.Instance, key.RegisterAddress)
}

func (key B524WatchKey) String() string {
	return key.Canonical()
}

func (key B524WatchKey) Family() WatchFamily {
	return WatchFamilyB524
}

type B509WatchKey struct {
	Target          byte
	RegisterAddress uint16
}

func NewB509WatchKey(target byte, registerAddress uint16) B509WatchKey {
	return B509WatchKey{
		Target:          target,
		RegisterAddress: registerAddress,
	}
}

func (key B509WatchKey) Canonical() string {
	return fmt.Sprintf("b509:%02x:%04x", key.Target, key.RegisterAddress)
}

func (key B509WatchKey) String() string {
	return key.Canonical()
}

func (key B509WatchKey) Family() WatchFamily {
	return WatchFamilyB509
}

type B516WatchKey struct {
	Target byte
	Period byte
	Source byte
	Usage  byte
}

func NewB516WatchKey(target, period, source, usage byte) B516WatchKey {
	return B516WatchKey{
		Target: target,
		Period: period,
		Source: source,
		Usage:  usage,
	}
}

func (key B516WatchKey) Canonical() string {
	return fmt.Sprintf("b516:%02x:%02x:%02x:%02x", key.Target, key.Period, key.Source, key.Usage)
}

func (key B516WatchKey) String() string {
	return key.Canonical()
}

func (key B516WatchKey) Family() WatchFamily {
	return WatchFamilyB516
}

type WatchDescriptor struct {
	Key               WatchKey
	SemanticClass     WatchSemanticClass
	FreshnessProfile  WatchFreshnessProfile
	DecoderID         string
	CorrelationPolicy WatchCorrelationPolicy
	DirectApplyPolicy WatchDirectApplyPolicy
	FreshnessTTL      *time.Duration
}

func (descriptor WatchDescriptor) CanonicalKey() string {
	if descriptor.Key == nil {
		return ""
	}
	return descriptor.Key.Canonical()
}

func (descriptor WatchDescriptor) Family() WatchFamily {
	if descriptor.Key == nil {
		return ""
	}
	return descriptor.Key.Family()
}

func (descriptor WatchDescriptor) EffectiveFreshnessTTL(collaborator EngineCollaborator) (time.Duration, error) {
	if err := validateWatchDescriptor(collaborator, descriptor); err != nil {
		return 0, err
	}
	defaultTTL := watchFreshnessProfileDefaults[descriptor.FreshnessProfile]
	if descriptor.FreshnessTTL == nil {
		return defaultTTL, nil
	}
	return *descriptor.FreshnessTTL, nil
}

func validateWatchDescriptor(collaborator EngineCollaborator, descriptor WatchDescriptor) error {
	if descriptor.Key == nil {
		return fmt.Errorf("watch descriptor missing key")
	}
	normalizedKey, err := cloneThroughCollaborator(collaborator, descriptor.Key)
	if err != nil {
		return err
	}
	canonical := normalizedKey.Canonical()
	if collaborator != nil {
		if err := collaborator.ValidateB555WatchKey(normalizedKey); err != nil {
			return fmt.Errorf("watch descriptor %q %w", canonical, err)
		}
	}
	if descriptor.SemanticClass == "" {
		return fmt.Errorf("watch descriptor %q missing semantic class", canonical)
	}
	if descriptor.FreshnessProfile == "" {
		return fmt.Errorf("watch descriptor %q missing freshness profile", canonical)
	}
	if !watchSemanticClassAllowsProfile(descriptor.SemanticClass, descriptor.FreshnessProfile) {
		return fmt.Errorf(
			"watch descriptor %q invalid semantic_class=%s freshness_profile=%s",
			canonical,
			descriptor.SemanticClass,
			descriptor.FreshnessProfile,
		)
	}
	if descriptor.DecoderID == "" {
		return fmt.Errorf("watch descriptor %q missing decoder id", canonical)
	}
	if descriptor.CorrelationPolicy == "" {
		return fmt.Errorf("watch descriptor %q missing correlation policy", canonical)
	}
	if descriptor.DirectApplyPolicy == "" {
		return fmt.Errorf("watch descriptor %q missing direct-apply policy", canonical)
	}
	defaultTTL, ok := watchFreshnessProfileDefaults[descriptor.FreshnessProfile]
	if !ok {
		return fmt.Errorf("watch descriptor %q has unknown freshness profile %s", canonical, descriptor.FreshnessProfile)
	}
	if descriptor.FreshnessTTL == nil {
		return nil
	}
	if *descriptor.FreshnessTTL < 0 {
		return fmt.Errorf("watch descriptor %q has negative freshness ttl", canonical)
	}
	if defaultTTL == 0 && *descriptor.FreshnessTTL != 0 {
		return fmt.Errorf("watch descriptor %q cannot widen zero-default freshness ttl", canonical)
	}
	if defaultTTL > 0 && *descriptor.FreshnessTTL > defaultTTL {
		return fmt.Errorf(
			"watch descriptor %q freshness ttl %s widens default %s",
			canonical,
			descriptor.FreshnessTTL.Round(time.Millisecond),
			defaultTTL.Round(time.Millisecond),
		)
	}
	return nil
}

func watchSemanticClassAllowsProfile(class WatchSemanticClass, profile WatchFreshnessProfile) bool {
	switch class {
	case WatchSemanticClassState:
		return profile == WatchFreshnessProfileStateFast || profile == WatchFreshnessProfileStateSlow
	case WatchSemanticClassConfig:
		return profile == WatchFreshnessProfileConfig
	case WatchSemanticClassDiscovery:
		return profile == WatchFreshnessProfileDiscovery
	case WatchSemanticClassDebug:
		return profile == WatchFreshnessProfileDebug
	default:
		return false
	}
}

type WatchCatalog struct {
	descriptors  map[string]WatchDescriptor
	ordered      []WatchDescriptor
	collaborator EngineCollaborator
}

func NewWatchCatalog(descriptors []WatchDescriptor, collaborator EngineCollaborator) (*WatchCatalog, error) {
	catalog := &WatchCatalog{
		descriptors:  make(map[string]WatchDescriptor, len(descriptors)),
		ordered:      make([]WatchDescriptor, 0, len(descriptors)),
		collaborator: collaborator,
	}
	for _, descriptor := range descriptors {
		normalized, err := normalizeWatchDescriptor(collaborator, descriptor)
		if err != nil {
			return nil, err
		}
		key := normalized.CanonicalKey()
		if _, exists := catalog.descriptors[key]; exists {
			return nil, fmt.Errorf("duplicate watch descriptor for %q", key)
		}
		catalog.descriptors[key] = normalized
		catalog.ordered = append(catalog.ordered, normalized)
	}
	sort.Slice(catalog.ordered, func(i, j int) bool {
		return catalog.ordered[i].CanonicalKey() < catalog.ordered[j].CanonicalKey()
	})
	return catalog, nil
}

func (catalog *WatchCatalog) Len() int {
	if catalog == nil {
		return 0
	}
	return len(catalog.ordered)
}

func (catalog *WatchCatalog) Descriptor(key WatchKey) (WatchDescriptor, bool) {
	if catalog == nil || key == nil {
		return WatchDescriptor{}, false
	}
	return catalog.DescriptorByCanonical(key.Canonical())
}

func (catalog *WatchCatalog) DescriptorByCanonical(key string) (WatchDescriptor, bool) {
	if catalog == nil {
		return WatchDescriptor{}, false
	}
	descriptor, ok := catalog.descriptors[key]
	if !ok {
		return WatchDescriptor{}, false
	}
	return cloneWatchDescriptor(catalog.collaborator, descriptor), true
}

func (catalog *WatchCatalog) Descriptors() []WatchDescriptor {
	if catalog == nil {
		return nil
	}
	out := make([]WatchDescriptor, 0, len(catalog.ordered))
	for _, descriptor := range catalog.ordered {
		out = append(out, cloneWatchDescriptor(catalog.collaborator, descriptor))
	}
	return out
}

type WatchObservationSummary struct {
	ActiveTotal      uint64
	InactiveTotal    uint64
	CatalogMissTotal uint64
}

type WatchObservation struct {
	State         WatchObservationState
	Descriptor    WatchDescriptor
	HasDescriptor bool
	Sources       []WatchActivationSource
}

type WatchActivationSet struct {
	catalog *WatchCatalog

	mu      sync.RWMutex
	active  map[string]map[WatchActivationSource]struct{}
	summary WatchObservationSummary
}

func NewWatchActivationSet(catalog *WatchCatalog) *WatchActivationSet {
	if catalog == nil {
		catalog = &WatchCatalog{}
	}
	return &WatchActivationSet{
		catalog: catalog,
		active:  make(map[string]map[WatchActivationSource]struct{}),
	}
}

// Activate validates the full batch before mutating the immutable catalog-backed activation set.
func (set *WatchActivationSet) Activate(source WatchActivationSource, keys ...WatchKey) error {
	if set == nil {
		return nil
	}
	if source == "" {
		return fmt.Errorf("watch activation missing source")
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	canonicals := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == nil {
			return fmt.Errorf("watch activation contains nil key")
		}
		canonical := key.Canonical()
		if _, ok := set.catalog.DescriptorByCanonical(canonical); !ok {
			return fmt.Errorf("watch activation key %q missing from catalog", canonical)
		}
		canonicals = append(canonicals, canonical)
	}
	for _, canonical := range canonicals {
		sources := set.active[canonical]
		if sources == nil {
			sources = make(map[WatchActivationSource]struct{}, 1)
			set.active[canonical] = sources
		}
		sources[source] = struct{}{}
	}
	return nil
}

func (set *WatchActivationSet) Deactivate(source WatchActivationSource, keys ...WatchKey) {
	if set == nil || source == "" {
		return
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	for _, key := range keys {
		if key == nil {
			continue
		}
		canonical := key.Canonical()
		sources := set.active[canonical]
		if len(sources) == 0 {
			continue
		}
		delete(sources, source)
		if len(sources) == 0 {
			delete(set.active, canonical)
		}
	}
}

func (set *WatchActivationSet) ActiveSources(key WatchKey) []WatchActivationSource {
	if set == nil || key == nil {
		return nil
	}
	set.mu.RLock()
	defer set.mu.RUnlock()
	return sortedWatchActivationSources(set.active[key.Canonical()])
}

func (set *WatchActivationSet) Observe(key WatchKey) WatchObservation {
	if set == nil || key == nil {
		return WatchObservation{State: WatchObservationStateCatalogMiss}
	}
	set.mu.Lock()
	defer set.mu.Unlock()

	descriptor, ok := set.catalog.DescriptorByCanonical(key.Canonical())
	if !ok {
		set.summary.CatalogMissTotal++
		return WatchObservation{State: WatchObservationStateCatalogMiss}
	}
	sources := sortedWatchActivationSources(set.active[key.Canonical()])
	if len(sources) == 0 {
		set.summary.InactiveTotal++
		return WatchObservation{
			State:         WatchObservationStateInactive,
			Descriptor:    descriptor,
			HasDescriptor: true,
		}
	}
	set.summary.ActiveTotal++
	return WatchObservation{
		State:         WatchObservationStateActive,
		Descriptor:    descriptor,
		HasDescriptor: true,
		Sources:       sources,
	}
}

func (set *WatchActivationSet) Summary() WatchObservationSummary {
	if set == nil {
		return WatchObservationSummary{}
	}
	set.mu.RLock()
	defer set.mu.RUnlock()
	return set.summary
}

func cloneWatchDescriptor(collaborator EngineCollaborator, descriptor WatchDescriptor) WatchDescriptor {
	cloned := descriptor
	if descriptor.Key != nil {
		cloned.Key = mustCloneWatchKey(collaborator, descriptor.Key)
	}
	if descriptor.FreshnessTTL != nil {
		value := *descriptor.FreshnessTTL
		cloned.FreshnessTTL = &value
	}
	return cloned
}

func sortedWatchActivationSources(sources map[WatchActivationSource]struct{}) []WatchActivationSource {
	if len(sources) == 0 {
		return nil
	}
	out := make([]WatchActivationSource, 0, len(sources))
	for source := range sources {
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool {
		left, lok := watchActivationSourceOrder[out[i]]
		right, rok := watchActivationSourceOrder[out[j]]
		if lok && rok && left != right {
			return left < right
		}
		if lok != rok {
			return lok
		}
		return out[i] < out[j]
	})
	return out
}

func normalizeWatchDescriptor(collaborator EngineCollaborator, descriptor WatchDescriptor) (WatchDescriptor, error) {
	normalized := descriptor
	if descriptor.Key != nil {
		key, err := cloneThroughCollaborator(collaborator, descriptor.Key)
		if err != nil {
			return WatchDescriptor{}, err
		}
		normalized.Key = key
	}
	if descriptor.FreshnessTTL != nil {
		value := *descriptor.FreshnessTTL
		normalized.FreshnessTTL = &value
	}
	if err := validateWatchDescriptor(collaborator, normalized); err != nil {
		return WatchDescriptor{}, err
	}
	return normalized, nil
}

func cloneThroughCollaborator(collaborator EngineCollaborator, key WatchKey) (WatchKey, error) {
	if collaborator == nil {
		return nil, fmt.Errorf("watch descriptor key type %T is not supported for immutable catalog storage", key)
	}
	return collaborator.CloneWatchKey(key)
}

func mustCloneWatchKey(collaborator EngineCollaborator, key WatchKey) WatchKey {
	cloned, err := cloneThroughCollaborator(collaborator, key)
	if err != nil {
		panic(err)
	}
	return cloned
}
