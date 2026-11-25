package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// neutralAdmittedWatchKey is a key shape outside the donor's four built-in
// shapes. Kind and Address are the identity fields Canonical reports.
//
// Kind: a production mutation that drops Kind from Canonical makes
// NewWatchCatalog reject the second descriptor as a duplicate, or makes
// DescriptorByCanonical return the other descriptor.
// Address: a production mutation that drops Address from Canonical makes
// NewWatchCatalog reject the third descriptor as a duplicate, or makes
// DescriptorByCanonical return the other descriptor.
// The deduplicator half catches mustCloneWatchKey panicking on this shape,
// or buildActiveFingerprint leaving SharedWatchKey empty or identical for
// frames that differ in Kind or in Address.
type neutralAdmittedWatchKey struct {
	Kind    byte
	Address uint16
}

func (key neutralAdmittedWatchKey) String() string { return key.Canonical() }

func (key neutralAdmittedWatchKey) Canonical() string {
	return fmt.Sprintf("neutral:%02x:%04x", key.Kind, key.Address)
}

func (key neutralAdmittedWatchKey) Family() WatchFamily { return WatchFamily("neutral") }

type admittingCollaborator struct{ donorCollaborator }

func (admittingCollaborator) CloneWatchKey(key WatchKey) (WatchKey, error) {
	switch typed := key.(type) {
	case neutralAdmittedWatchKey:
		return typed, nil
	case *neutralAdmittedWatchKey:
		if typed == nil {
			return nil, fmt.Errorf("watch descriptor contains nil *neutralAdmittedWatchKey")
		}
		return *typed, nil
	default:
		return donorCollaborator{}.CloneWatchKey(key)
	}
}

func (admittingCollaborator) ValidateB555WatchKey(key WatchKey) error {
	if _, ok := key.(neutralAdmittedWatchKey); ok {
		return nil
	}
	return donorCollaborator{}.ValidateB555WatchKey(key)
}

func (collaborator admittingCollaborator) PassiveWatchKeyFromFrame(frame protocol.Frame) (WatchKey, bool) {
	if frame.Primary == 0xEE && len(frame.Data) >= 3 {
		return neutralAdmittedWatchKey{
			Kind:    frame.Data[0],
			Address: uint16(frame.Data[1])<<8 | uint16(frame.Data[2]),
		}, true
	}
	return donorCollaborator{}.PassiveWatchKeyFromFrame(frame)
}

func (collaborator admittingCollaborator) PassiveWatchKeyFromEvent(event PassiveClassifiedEvent) (WatchKey, bool) {
	if !event.HasRequest {
		return nil, false
	}
	return collaborator.PassiveWatchKeyFromFrame(event.Request)
}

func TestCatalogAndDeduplicatorAdmitCollaboratorKeyType(t *testing.T) {
	kindAndAddress := neutralAdmittedWatchKey{Kind: 0x11, Address: 0x0102}
	addressDiffers := neutralAdmittedWatchKey{Kind: 0x11, Address: 0x0103}
	kindDiffers := neutralAdmittedWatchKey{Kind: 0x12, Address: 0x0102}

	catalog, err := NewWatchCatalog([]WatchDescriptor{
		neutralDescriptor(kindAndAddress),
		neutralDescriptor(addressDiffers),
		neutralDescriptor(kindDiffers),
	}, admittingCollaborator{})
	if err != nil {
		t.Fatalf("NewWatchCatalog() error = %v", err)
	}
	if catalog.Len() != 3 {
		t.Fatalf("catalog.Len() = %d; want 3", catalog.Len())
	}
	assertNeutralDescriptor(t, catalog, kindAndAddress)
	assertNeutralDescriptor(t, catalog, addressDiffers)
	assertNeutralDescriptor(t, catalog, kindDiffers)

	for _, key := range []neutralAdmittedWatchKey{kindAndAddress, addressDiffers, kindDiffers} {
		request := protocol.Frame{
			Source:    0x10,
			Target:    0x08,
			Primary:   0xEE,
			Secondary: 0x01,
			Data:      []byte{key.Kind, byte(key.Address >> 8), byte(key.Address)},
		}
		response := protocol.Frame{
			Source:    request.Target,
			Target:    request.Source,
			Primary:   request.Primary,
			Secondary: request.Secondary,
			Data:      []byte{key.Kind, byte(key.Address >> 8), byte(key.Address), 0x01},
		}
		fingerprint, ok := buildActiveFingerprint(
			1,
			activeAttemptEvent(request, response),
			time.Unix(0, 0),
			DefaultObserveFirstFeatureFlags(),
			nil,
			admittingCollaborator{},
		)
		if !ok {
			t.Fatalf("buildActiveFingerprint ok = false for %s", key.Canonical())
		}
		if fingerprint.SharedWatchKey == nil {
			t.Fatalf("SharedWatchKey = nil for %s", key.Canonical())
		}
		if got := fingerprint.SharedWatchKey.Canonical(); got != key.Canonical() {
			t.Fatalf("SharedWatchKey.Canonical() = %q; want %q", got, key.Canonical())
		}
	}
}

func neutralDescriptor(key neutralAdmittedWatchKey) WatchDescriptor {
	return WatchDescriptor{
		Key:               key,
		SemanticClass:     WatchSemanticClassState,
		FreshnessProfile:  WatchFreshnessProfileStateFast,
		DecoderID:         key.Canonical(),
		CorrelationPolicy: WatchCorrelationPolicyRequestResponse,
		DirectApplyPolicy: WatchDirectApplyPolicyStateDefault,
	}
}

func assertNeutralDescriptor(t *testing.T, catalog *WatchCatalog, key neutralAdmittedWatchKey) {
	t.Helper()
	stored, ok := catalog.DescriptorByCanonical(key.Canonical())
	if !ok {
		t.Fatalf("DescriptorByCanonical(%q) = !ok", key.Canonical())
	}
	if stored.DecoderID != key.Canonical() {
		t.Fatalf("DescriptorByCanonical(%q).DecoderID = %q", key.Canonical(), stored.DecoderID)
	}
	storedKey, ok := stored.Key.(neutralAdmittedWatchKey)
	if !ok {
		t.Fatalf("stored key type = %T; want neutralAdmittedWatchKey", stored.Key)
	}
	if storedKey.Kind != key.Kind || storedKey.Address != key.Address {
		t.Fatalf("stored key = %+v; want %+v", storedKey, key)
	}
}
