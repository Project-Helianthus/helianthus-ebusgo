// Package ebusgateway is the donor package name of the unsupported-key test
// double. The catalog error formats the key with %T, and that text includes
// this package name.
package ebusgateway

import "github.com/Project-Helianthus/helianthus-ebusgo/engine"

type mutableTestWatchKey struct {
	canonical string
	family    engine.WatchFamily
}

func (key *mutableTestWatchKey) Canonical() string { return key.canonical }

func (key *mutableTestWatchKey) String() string { return key.canonical }

func (key *mutableTestWatchKey) Family() engine.WatchFamily { return key.family }

// MutableUnsupportedWatchKey is the key the donor catalog rejects. Its dynamic
// type is *ebusgateway.mutableTestWatchKey.
func MutableUnsupportedWatchKey() engine.WatchKey {
	return &mutableTestWatchKey{canonical: "custom:mutable", family: engine.WatchFamilyB524}
}
