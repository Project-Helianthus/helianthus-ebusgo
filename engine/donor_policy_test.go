package engine

import (
	"fmt"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// Donor decisions copied from the gateway so replay injects the same keys,
// classifications, clones, validation errors and scan reasons.

type B555WatchKey struct {
	Target byte
	Opcode byte
	Zone   byte
	HC     byte

	Weekday    byte
	Slot       byte
	HasWeekday bool
	HasSlot    bool
}

func NewB555ProgramWatchKey(target, opcode, zone, hc byte) B555WatchKey {
	return B555WatchKey{
		Target: target,
		Opcode: opcode,
		Zone:   zone,
		HC:     hc,
	}
}

func NewB555WatchKey(target, opcode, zone, hc, weekday, slot byte) B555WatchKey {
	return B555WatchKey{
		Target:     target,
		Opcode:     opcode,
		Zone:       zone,
		HC:         hc,
		Weekday:    weekday,
		Slot:       slot,
		HasWeekday: true,
		HasSlot:    true,
	}
}

func (key B555WatchKey) Canonical() string {
	canonical := fmt.Sprintf(
		"b555:%02x:%02x:%02x:%02x:%02x:%02x",
		key.Target,
		key.Opcode,
		key.Zone,
		key.HC,
		b555SelectorByte(key.HasWeekday, key.Weekday),
		b555SelectorByte(key.HasSlot, key.Slot),
	)
	if b555WatchKeySelectorsValid(key) {
		return canonical
	}
	return fmt.Sprintf(
		"%s:raw:%d:%02x:%d:%02x",
		canonical,
		b555BoolByte(key.HasWeekday),
		key.Weekday,
		b555BoolByte(key.HasSlot),
		key.Slot,
	)
}

func (key B555WatchKey) String() string {
	return key.Canonical()
}

func (key B555WatchKey) Family() WatchFamily {
	return WatchFamilyB555
}

func PassiveWatchKeyFromEvent(event PassiveClassifiedEvent) (WatchKey, bool) {
	if !event.HasRequest {
		return nil, false
	}
	return PassiveWatchKeyFromFrame(event.Request)
}

func PassiveWatchKeyFromFrame(frame protocol.Frame) (WatchKey, bool) {
	switch {
	case frame.Primary == 0xB5 && frame.Secondary == 0x24:
		return parsePassiveB524WatchKey(frame)
	case frame.Primary == 0xB5 && frame.Secondary == 0x09:
		return parsePassiveB509WatchKey(frame)
	default:
		return nil, false
	}
}

func parsePassiveB524WatchKey(frame protocol.Frame) (WatchKey, bool) {
	if len(frame.Data) < 6 {
		return nil, false
	}
	opcode := frame.Data[0]
	if opcode != 0x02 && opcode != 0x06 {
		return nil, false
	}
	addr := uint16(frame.Data[4]) | uint16(frame.Data[5])<<8
	return NewB524WatchKey(frame.Target, opcode, frame.Data[2], frame.Data[3], addr), true
}

func parsePassiveB509WatchKey(frame protocol.Frame) (WatchKey, bool) {
	if len(frame.Data) < 3 {
		return nil, false
	}
	switch frame.Data[0] {
	case 0x0D, 0x29, 0x0E:
	default:
		return nil, false
	}
	addr := uint16(frame.Data[1])<<8 | uint16(frame.Data[2])
	return NewB509WatchKey(frame.Target, addr), true
}

func b555SelectorByte(set bool, value byte) byte {
	if !set {
		return 0xFF
	}
	return value
}

func b555BoolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}

func b555WatchKeySelectorsValid(key B555WatchKey) bool {
	if key.HasWeekday != key.HasSlot {
		return false
	}
	if !key.HasWeekday {
		return key.Weekday == 0x00 && key.Slot == 0x00
	}
	return key.Weekday != 0xFF && key.Slot != 0xFF
}

func cloneWatchKey(key WatchKey) (WatchKey, error) {
	switch typed := key.(type) {
	case B509WatchKey:
		return typed, nil
	case *B509WatchKey:
		if typed == nil {
			return nil, fmt.Errorf("watch descriptor contains nil *B509WatchKey")
		}
		return *typed, nil
	case B516WatchKey:
		return typed, nil
	case *B516WatchKey:
		if typed == nil {
			return nil, fmt.Errorf("watch descriptor contains nil *B516WatchKey")
		}
		return *typed, nil
	case B524WatchKey:
		return typed, nil
	case *B524WatchKey:
		if typed == nil {
			return nil, fmt.Errorf("watch descriptor contains nil *B524WatchKey")
		}
		return *typed, nil
	case B555WatchKey:
		return typed, nil
	case *B555WatchKey:
		if typed == nil {
			return nil, fmt.Errorf("watch descriptor contains nil *B555WatchKey")
		}
		return *typed, nil
	default:
		return nil, fmt.Errorf("watch descriptor key type %T is not supported for immutable catalog storage", key)
	}
}

func validateB555WatchKey(key WatchKey) error {
	b555Key, ok := key.(B555WatchKey)
	if !ok {
		return nil
	}
	if b555Key.HasWeekday != b555Key.HasSlot {
		return fmt.Errorf("must set both weekday and slot selectors together")
	}
	if !b555Key.HasWeekday {
		if b555Key.Weekday != 0x00 || b555Key.Slot != 0x00 {
			return fmt.Errorf("program selectors must keep raw weekday/slot zero when unset")
		}
		return nil
	}
	if b555Key.Weekday == 0xFF || b555Key.Slot == 0xFF {
		return fmt.Errorf("timer selectors may not use 0xff sentinel")
	}
	return nil
}

func observeFirstResponseClassForPassiveEvent(event PassiveClassifiedEvent, outcome DedupOutcomeClass) DedupResponseClass {
	return observeFirstResponseClass(
		event.Request,
		event.FrameType,
		outcome,
		event.Response,
		event.HasResponse,
	)
}

func observeFirstResponseClassForActiveEvent(event protocol.BusEvent) DedupResponseClass {
	return observeFirstResponseClass(
		event.Request,
		event.FrameType,
		DedupOutcomeSuccess,
		event.Response,
		event.HasResponse,
	)
}

func observeFirstResponseClass(request protocol.Frame, frameType protocol.FrameType, outcome DedupOutcomeClass, response protocol.Frame, hasResponse bool) DedupResponseClass {
	if outcome != DedupOutcomeSuccess {
		return DedupResponseErrorOrAmbiguous
	}
	if !hasResponse {
		if frameType == protocol.FrameTypeInitiatorInitiator {
			return DedupResponseACKOnly
		}
		return DedupResponseErrorOrAmbiguous
	}

	family := observeFirstFamilyFromFrame(request)
	intent := observeFirstRequestIntentFromFrame(request)

	switch family {
	case ObserveFirstFamilyB509:
		return observeFirstB509ResponseClass(request, intent, response.Data)
	case ObserveFirstFamilyB524:
		return observeFirstB524ResponseClass(request, intent, response.Data)
	default:
		return observeFirstGenericResponseClass(response.Data)
	}
}

func observeFirstFamilyPolicy(scope ObserveFirstTrafficScope, request protocol.Frame, responseClass DedupResponseClass, observation WatchObservation, flags ObserveFirstFeatureFlagView) ObserveFirstFamilyPolicy {
	family := observeFirstFamilyFromFrame(request)
	intent := observeFirstRequestIntentFromFrame(request)

	policy := ObserveFirstFamilyPolicy{
		Family:            family,
		RequestIntent:     intent,
		ResponseClass:     responseClass,
		CorrelationPolicy: WatchCorrelationPolicyRecordOnly,
		DirectApplyPolicy: ObserveFirstDirectApplyPolicyNever,
	}

	if scope == ObserveFirstTrafficScopePassive && intent == ObserveFirstRequestIntentWrite {
		policy.UsesRuntimeExternalWritePolicy = true
		policy.EffectiveExternalWritePolicy = NormalizeObserveFirstFeatureFlagsFromView(flags).ExternalWritePolicy()
	}

	switch family {
	case ObserveFirstFamilyB516:
		policy.CorrelationPolicy = WatchCorrelationPolicyBroadcastSelector
		if responseClass == DedupResponseValueBearing {
			policy.DirectApplyPolicy = ObserveFirstDirectApplyPolicyEnergyMergeOnly
		}
	case ObserveFirstFamilyB524:
		policy.CorrelationPolicy = WatchCorrelationPolicyRequestResponse
		if observeFirstB524CorrelatedStateRead(request, observation) && responseClass == DedupResponseValueBearing {
			policy.DirectApplyPolicy = ObserveFirstDirectApplyPolicyStateDefault
		}
	case ObserveFirstFamilyB509:
		policy.CorrelationPolicy = WatchCorrelationPolicyRequestResponse
		if observeFirstB509CorrelatedStateRead(request, observation) && responseClass == DedupResponseValueBearing {
			policy.DirectApplyPolicy = ObserveFirstDirectApplyPolicyStateDefault
		}
	case ObserveFirstFamilyB555:
		policy.CorrelationPolicy = WatchCorrelationPolicyRecordInvalidate
	default:
		if scope == ObserveFirstTrafficScopePassive && intent == ObserveFirstRequestIntentBroadcast {
			policy.CorrelationPolicy = WatchCorrelationPolicyBroadcastSelector
		}
	}

	return policy
}

func observeFirstFamilyFromFrame(frame protocol.Frame) ObserveFirstFamily {
	if frame.Primary != 0xB5 {
		return ObserveFirstFamilyOther
	}
	switch frame.Secondary {
	case 0x09:
		return ObserveFirstFamilyB509
	case 0x16:
		return ObserveFirstFamilyB516
	case 0x24:
		return ObserveFirstFamilyB524
	case 0x55:
		return ObserveFirstFamilyB555
	default:
		return ObserveFirstFamilyOther
	}
}

func observeFirstRequestIntentFromFrame(frame protocol.Frame) ObserveFirstRequestIntent {
	switch observeFirstFamilyFromFrame(frame) {
	case ObserveFirstFamilyB509:
		if len(frame.Data) == 0 {
			return ObserveFirstRequestIntentUnknown
		}
		switch frame.Data[0] {
		case 0x0D, 0x29:
			return ObserveFirstRequestIntentRead
		case 0x0E:
			return ObserveFirstRequestIntentWrite
		default:
			return ObserveFirstRequestIntentUnknown
		}
	case ObserveFirstFamilyB516:
		return ObserveFirstRequestIntentBroadcast
	case ObserveFirstFamilyB524:
		if len(frame.Data) == 0 {
			return ObserveFirstRequestIntentUnknown
		}
		switch frame.Data[0] {
		case 0x02, 0x06:
			if len(frame.Data) >= 2 && frame.Data[1] == 0x00 {
				return ObserveFirstRequestIntentRead
			}
			if len(frame.Data) >= 2 {
				return ObserveFirstRequestIntentWrite
			}
			return ObserveFirstRequestIntentUnknown
		case 0x03:
			return ObserveFirstRequestIntentRead
		default:
			return ObserveFirstRequestIntentUnknown
		}
	case ObserveFirstFamilyB555:
		if len(frame.Data) == 0 {
			return ObserveFirstRequestIntentUnknown
		}
		switch frame.Data[0] {
		case 0xA3, 0xA4, 0xA5:
			return ObserveFirstRequestIntentRead
		case 0xA6:
			return ObserveFirstRequestIntentWrite
		default:
			return ObserveFirstRequestIntentUnknown
		}
	default:
		return ObserveFirstRequestIntentUnknown
	}
}

func observeFirstGenericResponseClass(payload []byte) DedupResponseClass {
	if len(payload) == 0 {
		return DedupResponseHeaderOnly
	}
	return DedupResponseValueBearing
}

func observeFirstB509ResponseClass(request protocol.Frame, intent ObserveFirstRequestIntent, payload []byte) DedupResponseClass {
	if len(payload) == 0 {
		return DedupResponseHeaderOnly
	}
	if intent == ObserveFirstRequestIntentWrite {
		return DedupResponseHeaderOnly
	}
	if intent != ObserveFirstRequestIntentRead {
		return observeFirstGenericResponseClass(payload)
	}

	addr, ok := observeFirstExpectedB509Address(request)
	if !ok {
		return observeFirstGenericResponseClass(payload)
	}

	addrHi := byte(addr >> 8)
	addrLo := byte(addr)

	if len(payload) >= 3 && (payload[0] == 0x0D || payload[0] == 0x29) && payload[1] == addrHi && payload[2] == addrLo {
		if len(payload) == 3 {
			return DedupResponseHeaderOnly
		}
		return DedupResponseValueBearing
	}
	if len(payload) >= 2 && payload[0] == addrHi && payload[1] == addrLo {
		if len(payload) == 2 {
			return DedupResponseHeaderOnly
		}
		return DedupResponseValueBearing
	}
	return observeFirstGenericResponseClass(payload)
}

func observeFirstB524ResponseClass(request protocol.Frame, intent ObserveFirstRequestIntent, payload []byte) DedupResponseClass {
	if len(payload) == 0 {
		return DedupResponseHeaderOnly
	}
	if intent != ObserveFirstRequestIntentRead {
		if intent == ObserveFirstRequestIntentWrite {
			return DedupResponseHeaderOnly
		}
		return observeFirstGenericResponseClass(payload)
	}
	if observeFirstIsB524TimerRead(request) {
		return observeFirstGenericResponseClass(payload)
	}

	opcode, group, instance, addr, ok := observeFirstExpectedB524ReadSelector(request)
	if !ok {
		return observeFirstGenericResponseClass(payload)
	}
	if len(payload) == 1 && payload[0] == 0x00 {
		return DedupResponseErrorOrAmbiguous
	}
	if len(payload) < 4 {
		return DedupResponseErrorOrAmbiguous
	}
	if len(payload) >= 5 {
		replyInstance := payload[1]
		replyGroup := payload[2]
		replyAddr := uint16(payload[3]) | uint16(payload[4])<<8
		if replyGroup == group && replyAddr == addr {
			if !observeFirstMatchesB524ReplyInstance(replyInstance, instance) {
				return DedupResponseErrorOrAmbiguous
			}
			if len(payload) == 5 {
				return DedupResponseHeaderOnly
			}
			return DedupResponseValueBearing
		}
	}

	replyGroup := payload[1]
	replyAddr := uint16(payload[2]) | uint16(payload[3])<<8
	if replyGroup != group || replyAddr != addr {
		_ = opcode
		return DedupResponseErrorOrAmbiguous
	}
	if len(payload) == 4 {
		return DedupResponseHeaderOnly
	}
	return DedupResponseValueBearing
}

func observeFirstExpectedB509Address(frame protocol.Frame) (uint16, bool) {
	if len(frame.Data) < 3 {
		return 0, false
	}
	switch frame.Data[0] {
	case 0x0D, 0x29, 0x0E:
		return uint16(frame.Data[1])<<8 | uint16(frame.Data[2]), true
	default:
		return 0, false
	}
}

func observeFirstExpectedB524ReadSelector(frame protocol.Frame) (opcode, group, instance byte, addr uint16, ok bool) {
	if len(frame.Data) < 6 {
		return 0, 0, 0, 0, false
	}
	if frame.Data[0] != 0x02 && frame.Data[0] != 0x06 {
		return 0, 0, 0, 0, false
	}
	if frame.Data[1] != 0x00 {
		return 0, 0, 0, 0, false
	}
	return frame.Data[0], frame.Data[2], frame.Data[3], uint16(frame.Data[4]) | uint16(frame.Data[5])<<8, true
}

func observeFirstB524CorrelatedStateRead(frame protocol.Frame, observation WatchObservation) bool {
	opcode, group, instance, addr, ok := observeFirstExpectedB524ReadSelector(frame)
	if !ok {
		return false
	}
	key := NewB524WatchKey(frame.Target, opcode, group, instance, addr)
	return observeFirstMatchesActiveStateDefaultDescriptor(key, observation)
}

func observeFirstB509CorrelatedStateRead(frame protocol.Frame, observation WatchObservation) bool {
	addr, ok := observeFirstExpectedB509Address(frame)
	if !ok || observeFirstRequestIntentFromFrame(frame) != ObserveFirstRequestIntentRead {
		return false
	}
	key := NewB509WatchKey(frame.Target, addr)
	return observeFirstMatchesActiveStateDefaultDescriptor(key, observation)
}

func observeFirstMatchesActiveStateDefaultDescriptor(key WatchKey, observation WatchObservation) bool {
	if key == nil {
		return false
	}
	if !observation.HasDescriptor || observation.State != WatchObservationStateActive {
		return false
	}
	descriptor := observation.Descriptor
	if descriptor.Key == nil || descriptor.Key.Canonical() != key.Canonical() {
		return false
	}
	if descriptor.SemanticClass != WatchSemanticClassState {
		return false
	}
	if descriptor.CorrelationPolicy != WatchCorrelationPolicyRequestResponse {
		return false
	}
	return descriptor.DirectApplyPolicy == WatchDirectApplyPolicyStateDefault
}

func observeFirstIsB524TimerRead(frame protocol.Frame) bool {
	return len(frame.Data) > 0 && frame.Data[0] == 0x03
}

func observeFirstMatchesB524ReplyInstance(replyInstance, requestedInstance byte) bool {
	if replyInstance == requestedInstance {
		return true
	}
	if requestedInstance < 0xFF && replyInstance == requestedInstance+1 {
		return true
	}
	return false
}

// isScanProbeRaw checks raw request bytes for a 0704 device identity query
// without requiring a full frame parse.  Used to reclassify collision-garbled
// scan probes as scan_collision instead of corrupted_request.
func isScanProbeRaw(raw []byte) bool {
	return len(raw) >= 4 && raw[2] == 0x07 && raw[3] == 0x04
}

type donorCollaborator struct{}

func withDonor(cfg Config) Config {
	if cfg.Collaborator == nil {
		cfg.Collaborator = donorCollaborator{}
	}
	return cfg
}

func (donorCollaborator) ResponseClassForPassive(event PassiveClassifiedEvent, outcome DedupOutcomeClass) DedupResponseClass {
	return observeFirstResponseClassForPassiveEvent(event, outcome)
}

func (donorCollaborator) ResponseClassForActive(event protocol.BusEvent) DedupResponseClass {
	return observeFirstResponseClassForActiveEvent(event)
}

func (donorCollaborator) FamilyPolicy(scope ObserveFirstTrafficScope, request protocol.Frame, responseClass DedupResponseClass, observation WatchObservation, flags ObserveFirstFeatureFlagView) ObserveFirstFamilyPolicy {
	return observeFirstFamilyPolicy(scope, request, responseClass, observation, flags)
}

func (donorCollaborator) PassiveWatchKeyFromEvent(event PassiveClassifiedEvent) (WatchKey, bool) {
	return PassiveWatchKeyFromEvent(event)
}

func (donorCollaborator) PassiveWatchKeyFromFrame(frame protocol.Frame) (WatchKey, bool) {
	return PassiveWatchKeyFromFrame(frame)
}

func (donorCollaborator) RequestIntentFromFrame(frame protocol.Frame) ObserveFirstRequestIntent {
	return observeFirstRequestIntentFromFrame(frame)
}

func (donorCollaborator) IsScanProbeRaw(raw []byte) bool {
	return isScanProbeRaw(raw)
}

// IsScanTimeout reports the donor pending-probe test: a parsed request whose
// primary and secondary bytes are 0x07 and 0x04.
func (donorCollaborator) IsScanTimeout(request protocol.Frame) bool {
	return request.Primary == 0x07 && request.Secondary == 0x04
}

func (donorCollaborator) CloneWatchKey(key WatchKey) (WatchKey, error) {
	return cloneWatchKey(key)
}

func (donorCollaborator) ValidateB555WatchKey(key WatchKey) error {
	return validateB555WatchKey(key)
}
