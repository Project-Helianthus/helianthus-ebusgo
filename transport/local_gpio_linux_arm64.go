//go:build linux && arm64 && aa29_local_gpio

package transport

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"reflect"
	"sync/atomic"

	"github.com/Project-Helianthus/helianthus-ebusgo/localworker"
)

// This file is the local GPIO backend boundary for the first local engine
// candidate, a bit-level GPIO poller at a nominal 10x sampling rate. It is only
// built for linux/arm64 with the explicit, non-default aa29_local_gpio tag, so
// the default binary does not contain it.
//
// Status: Hypothesis. The boundary owns only what the engine cannot: admission
// of an owner-provisioned, qualified platform profile, validation of the mapped
// GPIO window and counter it is handed, and translation of wire engine level, edge and
// tick ports onto them. Wire scheduling and state stay in the wire engine and
// the responder.
//
// What this file does not do:
//   - It opens no path, performs no default device lookup, names no physical
//     address and carries no built-in profile. A mapped window and a counter
//     reach it only through the injected LocalGPIOMappingPort and LocalGPIOCounterPort.
//   - It is not a RawTransport or ResponderTransport and has no Write path. It
//     is not an OS UART, PL011 or PIO backend and makes no ENS claim.
//   - It adds no configuration field, flag or exported arming switch. A backend
//     starts disarmed; its edge port refuses every write until the
//     backend-internal arm admits a finite lease bound to the admitted
//     profile, and nothing outside this package calls arm.
//   - It claims no qualified timing. Physical timing, stuck-level containment,
//     platform atomics and the electrical effect of any write remain Unknown.
//
// The backend is serialized like the wire engine: one caller drives it.

const (
	// localGPIOAccessWidth is the only admitted register access width in bytes.
	localGPIOAccessWidth uint8 = 4
	// localGPIOMaxMapLength bounds a GPIO control window; it is not a platform value.
	localGPIOMaxMapLength uint64 = 1 << 16
	// localGPIOMaxLabel bounds profile identifier and family labels.
	localGPIOMaxLabel = 64

	localGPIODigestDomain = "aa29.profile.1"
)

// LocalGPIOResourceRole names what an owner-provisioned descriptor describes. The
// zero value marks an absent descriptor.
type LocalGPIOResourceRole uint8

const (
	LocalGPIORoleNone LocalGPIOResourceRole = iota
	LocalGPIORoleGPIOControl
	LocalGPIORoleCounter
)

// LocalGPIOOrdering is the access ordering a mapping provides. Only device-ordered,
// uncached access is admitted for GPIO control registers.
type LocalGPIOOrdering uint8

const (
	LocalGPIOOrderingUnknown LocalGPIOOrdering = iota
	LocalGPIOOrderingRelaxed
	LocalGPIOOrderingDevice
)

// LocalGPIODescriptor describes one GPIO control window by size and register
// offsets inside it, never by physical address. Masks select exactly one pin.
// Inversion maps the engine's logic level onto the pin level of the frontend.
type LocalGPIODescriptor struct {
	Role          LocalGPIOResourceRole
	MapLength     uint64
	AccessWidth   uint8
	Ordering      LocalGPIOOrdering
	RXLevelOffset uint64
	TXSetOffset   uint64
	TXClearOffset uint64
	RXMask        uint32
	TXMask        uint32
	RXInverted    bool
	TXInverted    bool
}

// LocalGPIOCounterDescriptor describes the monotonic counter the engine reads.
type LocalGPIOCounterDescriptor struct {
	Role LocalGPIOResourceRole
	Hz   uint64
}

// LocalGPIOPlatformProfile is owner-provisioned. Qualification must be
// AA22Proven and QualificationDigest must equal LocalGPIOProfileDigest of the
// same profile, so a qualification record covers exactly these contents. The
// profile qualifies the resource description only; it is not a timing claim.
type LocalGPIOPlatformProfile struct {
	ProfileID           string
	Family              string
	Qualification       AA22Evidence
	QualificationDigest [sha256.Size]byte
	GPIO                LocalGPIODescriptor
	Counter             LocalGPIOCounterDescriptor
	MaxLeaseTicks       uint64
}

// LocalGPIOWindow is a mapped GPIO control window. Loads and stores must not block.
type LocalGPIOWindow interface {
	MappedLength() uint64
	MappedAccessWidth() uint8
	MappedOrdering() LocalGPIOOrdering
	Load32(offset uint64) uint32
	Store32(offset uint64, value uint32)
	Unmap() error
}

// LocalGPIOMappingPort maps a window for an already validated descriptor from a
// resource its owner provisioned. The backend never chooses what to map.
type LocalGPIOMappingPort interface {
	Map(descriptor LocalGPIODescriptor) (LocalGPIOWindow, error)
}

// LocalGPIOCounterPort reads the monotonic counter. Neither method may block.
type LocalGPIOCounterPort interface {
	CounterHz() uint64
	ReadCounter() uint64
}

// LocalGPIOLease bounds one arming. It names the admitted profile by digest and
// expires at NotAfterTick of the backend counter.
type LocalGPIOLease struct {
	LeaseID       uint64
	ProfileDigest [sha256.Size]byte
	NotAfterTick  uint64
}

// LocalGPIOStats is a snapshot of bounded backend counters.
type LocalGPIOStats struct {
	StoredEdges   uint64
	RefusedEdges  uint64
	Releases      uint64
	LeaseExpiries uint64
}

// The local GPIO backend refuses profiles, resources, leases and mappings
// with these errors.
var (
	ErrLocalGPIOProfileMissing  = errors.New("transport: aa29 platform profile missing")
	ErrLocalGPIOUnqualified     = errors.New("transport: aa29 platform profile not qualified")
	ErrLocalGPIODescriptor      = errors.New("transport: aa29 resource descriptor invalid")
	ErrLocalGPIOResourceMissing = errors.New("transport: aa29 control resource missing")
	ErrLocalGPIOResourceMatch   = errors.New("transport: aa29 control resource does not match")
	ErrLocalGPIOLease           = errors.New("transport: aa29 lease not admitted")
	ErrLocalGPIOClosed          = errors.New("transport: aa29 backend closed")
	ErrLocalGPIOMapping         = errors.New("transport: aa29 mapping port refused the descriptor")
)

// LocalGPIOProfileDigest is SHA-256 over a fixed-order encoding of every profile
// field except QualificationDigest itself.
func LocalGPIOProfileDigest(profile LocalGPIOPlatformProfile) [sha256.Size]byte {
	buffer := make([]byte, 0, 160)
	buffer = append(buffer, localGPIODigestDomain...)
	buffer = append(buffer, 0)
	buffer = localGPIOAppendLabel(buffer, profile.ProfileID)
	buffer = localGPIOAppendLabel(buffer, profile.Family)
	buffer = append(buffer, byte(profile.Qualification))
	gpio := profile.GPIO
	buffer = append(buffer, byte(gpio.Role))
	buffer = binary.BigEndian.AppendUint64(buffer, gpio.MapLength)
	buffer = append(buffer, gpio.AccessWidth, byte(gpio.Ordering))
	buffer = binary.BigEndian.AppendUint64(buffer, gpio.RXLevelOffset)
	buffer = binary.BigEndian.AppendUint64(buffer, gpio.TXSetOffset)
	buffer = binary.BigEndian.AppendUint64(buffer, gpio.TXClearOffset)
	buffer = binary.BigEndian.AppendUint32(buffer, gpio.RXMask)
	buffer = binary.BigEndian.AppendUint32(buffer, gpio.TXMask)
	buffer = append(buffer, localGPIOBool(gpio.RXInverted), localGPIOBool(gpio.TXInverted))
	buffer = append(buffer, byte(profile.Counter.Role))
	buffer = binary.BigEndian.AppendUint64(buffer, profile.Counter.Hz)
	buffer = binary.BigEndian.AppendUint64(buffer, profile.MaxLeaseTicks)
	return sha256.Sum256(buffer)
}

func localGPIOAppendLabel(buffer []byte, label string) []byte {
	buffer = binary.BigEndian.AppendUint16(buffer, uint16(len(label)))
	return append(buffer, label...)
}

func localGPIOBool(value bool) byte {
	if value {
		return 1
	}
	return 0
}

func localGPIOValidLabel(label string) bool {
	if len(label) == 0 || len(label) > localGPIOMaxLabel {
		return false
	}
	for index := 0; index < len(label); index++ {
		if label[index] < 0x20 || label[index] > 0x7E {
			return false
		}
	}
	return true
}

// localGPIORegisterFits reports whether one aligned access at offset lies wholly
// inside a window of length bytes.
func localGPIORegisterFits(offset, length uint64) bool {
	return offset%uint64(localGPIOAccessWidth) == 0 && offset <= length-uint64(localGPIOAccessWidth)
}

func localGPIOValidGPIO(gpio LocalGPIODescriptor) bool {
	if gpio.Role != LocalGPIORoleGPIOControl ||
		gpio.AccessWidth != localGPIOAccessWidth ||
		gpio.Ordering != LocalGPIOOrderingDevice ||
		gpio.MapLength < uint64(localGPIOAccessWidth) ||
		gpio.MapLength > localGPIOMaxMapLength ||
		gpio.MapLength%uint64(localGPIOAccessWidth) != 0 {
		return false
	}
	if !localGPIORegisterFits(gpio.RXLevelOffset, gpio.MapLength) ||
		!localGPIORegisterFits(gpio.TXSetOffset, gpio.MapLength) ||
		!localGPIORegisterFits(gpio.TXClearOffset, gpio.MapLength) {
		return false
	}
	if gpio.TXSetOffset == gpio.TXClearOffset ||
		gpio.RXLevelOffset == gpio.TXSetOffset ||
		gpio.RXLevelOffset == gpio.TXClearOffset {
		return false
	}
	return bits.OnesCount32(gpio.RXMask) == 1 && bits.OnesCount32(gpio.TXMask) == 1
}

// localGPIOAdmit checks the profile alone. It touches no port.
func localGPIOAdmit(profile *LocalGPIOPlatformProfile) ([sha256.Size]byte, error) {
	if profile == nil {
		return [sha256.Size]byte{}, ErrLocalGPIOProfileMissing
	}
	if profile.Qualification != AA22Proven {
		return [sha256.Size]byte{}, ErrLocalGPIOUnqualified
	}
	digest := LocalGPIOProfileDigest(*profile)
	if digest != profile.QualificationDigest {
		return [sha256.Size]byte{}, ErrLocalGPIOUnqualified
	}
	if !localGPIOValidLabel(profile.ProfileID) || !localGPIOValidLabel(profile.Family) ||
		!localGPIOValidGPIO(profile.GPIO) ||
		profile.Counter.Role != LocalGPIORoleCounter ||
		profile.Counter.Hz < localworker.NominalSampleRate ||
		profile.MaxLeaseTicks == 0 {
		return [sha256.Size]byte{}, ErrLocalGPIODescriptor
	}
	return digest, nil
}

// localGPIOAbsent treats a nil interface and a typed nil reference alike.
func localGPIOAbsent(port any) bool {
	if port == nil {
		return true
	}
	value := reflect.ValueOf(port)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

// LocalGPIOBackend binds one admitted profile to one mapped window and counter.
type LocalGPIOBackend struct {
	profile LocalGPIOPlatformProfile
	digest  [sha256.Size]byte
	window  LocalGPIOWindow
	counter LocalGPIOCounterPort
	stats   LocalGPIOStats

	// closed is atomic so capability queries stay coherent against close.
	closed   atomic.Bool
	unmapped bool
	armed    bool
	driving  bool
	lease    LocalGPIOLease
}

// OpenLocalGPIO admits the profile and checks both control resources before asking
// the mapping port for anything, then verifies the returned window against the
// descriptor. Every failure leaves no mapping held. The backend starts
// disarmed and writes nothing at open.
func OpenLocalGPIO(profile *LocalGPIOPlatformProfile, mapping LocalGPIOMappingPort, counter LocalGPIOCounterPort) (*LocalGPIOBackend, error) {
	digest, err := localGPIOAdmit(profile)
	if err != nil {
		return nil, err
	}
	if localGPIOAbsent(mapping) || localGPIOAbsent(counter) {
		return nil, ErrLocalGPIOResourceMissing
	}
	if counter.CounterHz() != profile.Counter.Hz {
		return nil, ErrLocalGPIOResourceMatch
	}
	gpio := profile.GPIO
	window, err := mapping.Map(gpio)
	if err != nil {
		if !localGPIOAbsent(window) {
			if unmapErr := window.Unmap(); unmapErr != nil {
				return localGPIORetainWindow(window, counter), fmt.Errorf("%w: %w: %w", ErrLocalGPIOMapping, err, unmapErr)
			}
		}
		return nil, fmt.Errorf("%w: %w", ErrLocalGPIOMapping, err)
	}
	if localGPIOAbsent(window) {
		return nil, ErrLocalGPIOResourceMissing
	}
	if window.MappedLength() != gpio.MapLength ||
		window.MappedAccessWidth() != localGPIOAccessWidth ||
		window.MappedOrdering() != LocalGPIOOrderingDevice {
		if unmapErr := window.Unmap(); unmapErr != nil {
			return localGPIORetainWindow(window, counter), fmt.Errorf("%w: %w", ErrLocalGPIOResourceMatch, unmapErr)
		}
		return nil, ErrLocalGPIOResourceMatch
	}
	return &LocalGPIOBackend{
		profile: *profile,
		digest:  digest,
		window:  window,
		counter: counter,
	}, nil
}

// localGPIORetainWindow keeps a window whose rollback unmap failed in a closed,
// unusable backend so the caller can retry Close until the unmap succeeds.
func localGPIORetainWindow(window LocalGPIOWindow, counter LocalGPIOCounterPort) *LocalGPIOBackend {
	backend := &LocalGPIOBackend{window: window, counter: counter}
	backend.closed.Store(true)
	return backend
}

// LocalGPIOStats returns a snapshot of the bounded counters.
func (backend *LocalGPIOBackend) Stats() LocalGPIOStats {
	return backend.stats
}

// AA22TransportCapabilities reports a local raw backend. Arbitration and
// qualified timing stay Unknown; the remaining claims are Hypothesis, because
// profile admission qualifies resources, not wire behavior. A closed backend
// claims nothing.
func (backend *LocalGPIOBackend) AA22TransportCapabilities() AA22Capabilities {
	if backend.closed.Load() {
		return AA22Capabilities{}
	}
	return AA22Capabilities{
		AA22Kind:                       AA22KindLocalRaw,
		AA22Encoding:                   AA22EncodingRaw,
		AA22PhysicalArbitration:        AA22Unknown,
		AA22AutonomousPreparedResponse: AA22Hypothesis,
		AA22ObservedEcho:               AA22Hypothesis,
		AA22CaptureTimestamps:          AA22Hypothesis,
		AA22QualifiedTiming:            AA22Unknown,
	}
}

// SampleLevel reads the RX register. The level always comes from the
// window, never from the last TX level, so an echo is observed, not
// synthesized. A closed backend reads the idle level without a load.
func (backend *LocalGPIOBackend) SampleLevel() localworker.Level {
	if backend.closed.Load() {
		return localworker.LevelHigh
	}
	gpio := backend.profile.GPIO
	pinHigh := backend.window.Load32(gpio.RXLevelOffset)&gpio.RXMask != 0
	if pinHigh != gpio.RXInverted {
		return localworker.LevelHigh
	}
	return localworker.LevelLow
}

// NowTicks reads the counter. A read at or past the lease expiry disarms
// and releases a driven line, so expiry holds without another edge request. A
// closed backend returns zero without a read, which the engine treats as a tick
// regression and a withdrawal.
func (backend *LocalGPIOBackend) NowTicks() uint64 {
	if backend.closed.Load() {
		return 0
	}
	now := backend.counter.ReadCounter()
	if backend.armed && now >= backend.lease.NotAfterTick {
		backend.stats.LeaseExpiries++
		backend.disarm()
	}
	return now
}

// WriteEdge stores an edge only while a live lease arms the backend. Any
// other write is refused and counted. An expired lease disarms and releases.
func (backend *LocalGPIOBackend) WriteEdge(level localworker.Level) {
	if backend.closed.Load() || !backend.armed {
		backend.stats.RefusedEdges++
		return
	}
	if backend.counter.ReadCounter() >= backend.lease.NotAfterTick {
		backend.stats.LeaseExpiries++
		backend.disarm()
		backend.stats.RefusedEdges++
		return
	}
	backend.store(level)
	backend.stats.StoredEdges++
}

func (backend *LocalGPIOBackend) store(level localworker.Level) {
	gpio := backend.profile.GPIO
	offset := gpio.TXClearOffset
	if (level == localworker.LevelHigh) != gpio.TXInverted {
		offset = gpio.TXSetOffset
	}
	backend.window.Store32(offset, gpio.TXMask)
	backend.driving = level == localworker.LevelLow
}

// disarm releases a driven line to the idle level once, then disarms. The
// release is a software store; it bounds nothing while the caller is not
// running.
func (backend *LocalGPIOBackend) disarm() {
	if backend.driving {
		backend.store(localworker.LevelHigh)
		backend.stats.Releases++
	}
	backend.armed = false
	backend.lease = LocalGPIOLease{}
}

// arm is backend-internal arm admission. It requires a nonzero lease bound
// to the admitted profile digest whose expiry is in the future and no further
// away than the profile's MaxLeaseTicks. An armed backend must be revoked
// or expire before another lease is admitted.
func (backend *LocalGPIOBackend) arm(lease LocalGPIOLease) error {
	if backend.closed.Load() {
		return ErrLocalGPIOClosed
	}
	if backend.armed || lease.LeaseID == 0 || lease.ProfileDigest != backend.digest {
		return ErrLocalGPIOLease
	}
	now := backend.counter.ReadCounter()
	if lease.NotAfterTick <= now || lease.NotAfterTick-now > backend.profile.MaxLeaseTicks {
		return ErrLocalGPIOLease
	}
	backend.lease = lease
	backend.armed = true
	return nil
}

// revoke disarms only for the lease that armed the backend.
func (backend *LocalGPIOBackend) revoke(leaseID uint64) bool {
	if !backend.armed || leaseID == 0 || leaseID != backend.lease.LeaseID {
		return false
	}
	backend.disarm()
	return true
}

// NewEngine binds this backend's level, edge and tick ports to a wire engine
// engine. The engine's tick rate must be the admitted counter rate. Engine
// construction admits no lease: the engine still transmits only an offered
// prepared response, and this backend still refuses edges while disarmed.
func (backend *LocalGPIOBackend) NewEngine(config localworker.EngineConfig, sink localworker.EventSink) (*localworker.Engine, error) {
	if backend.closed.Load() {
		return nil, ErrLocalGPIOClosed
	}
	if config.TickHz != backend.profile.Counter.Hz {
		return nil, ErrLocalGPIOResourceMatch
	}
	return localworker.NewEngine(config, localworker.Ports{
		Levels: backend,
		Edges:  backend,
		Ticks:  backend,
		Sink:   sink,
	})
}

// Close disarms, releasing a driven line, and unmaps the window once.
// A failed unmap keeps the window owned, so a later call retries it; calls
// after a successful unmap return nil.
func (backend *LocalGPIOBackend) Close() error {
	if backend.unmapped {
		return nil
	}
	if !backend.closed.Load() {
		backend.disarm()
		backend.closed.Store(true)
	}
	err := backend.window.Unmap()
	if err == nil {
		backend.unmapped = true
	}
	return err
}
