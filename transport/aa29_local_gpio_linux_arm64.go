//go:build linux && arm64 && aa29_local_gpio

package transport

import (
	aa29sha256 "crypto/sha256"
	aa29binary "encoding/binary"
	aa29errors "errors"
	aa29fmt "fmt"
	aa29bits "math/bits"
	aa29reflect "reflect"

	aa29localworker "github.com/Project-Helianthus/helianthus-ebusgo/localworker"
)

// This file is the AA29 local GPIO backend boundary for the first local engine
// candidate, a bit-level GPIO poller at a nominal 10x sampling rate. It is only
// built for linux/arm64 with the explicit, non-default aa29_local_gpio tag, so
// the default binary does not contain it.
//
// Status: Hypothesis. The boundary owns only what the engine cannot: admission
// of an owner-provisioned, qualified platform profile, validation of the mapped
// GPIO window and counter it is handed, and translation of AA25 level, edge and
// tick ports onto them. Wire scheduling and state stay in the AA25 engine and
// the AA26 responder.
//
// What this file does not do:
//   - It opens no path, performs no default device lookup, names no physical
//     address and carries no built-in profile. A mapped window and a counter
//     reach it only through the injected AA29MappingPort and AA29CounterPort.
//   - It is not a RawTransport or ResponderTransport and has no Write path. It
//     is not an OS UART, PL011 or PIO backend and makes no ENS claim.
//   - It adds no configuration field, flag or exported arming switch. A backend
//     starts disarmed; its edge port refuses every write until the
//     backend-internal aa29Arm admits a finite lease bound to the admitted
//     profile, and nothing outside this package calls aa29Arm.
//   - It claims no qualified timing. Physical timing, stuck-level containment,
//     platform atomics and the electrical effect of any write remain Unknown.
//
// The backend is serialized like the AA25 engine: one caller drives it.

const (
	// aa29AccessWidth is the only admitted register access width in bytes.
	aa29AccessWidth uint8 = 4
	// aa29MaxMapLength bounds a GPIO control window; it is not a platform value.
	aa29MaxMapLength uint64 = 1 << 16
	// aa29MaxLabel bounds profile identifier and family labels.
	aa29MaxLabel = 64

	aa29DigestDomain = "aa29.profile.1"
)

// AA29ResourceRole names what an owner-provisioned descriptor describes. The
// zero value marks an absent descriptor.
type AA29ResourceRole uint8

const (
	AA29RoleNone AA29ResourceRole = iota
	AA29RoleGPIOControl
	AA29RoleCounter
)

// AA29Ordering is the access ordering a mapping provides. Only device-ordered,
// uncached access is admitted for GPIO control registers.
type AA29Ordering uint8

const (
	AA29OrderingUnknown AA29Ordering = iota
	AA29OrderingRelaxed
	AA29OrderingDevice
)

// AA29GPIODescriptor describes one GPIO control window by size and register
// offsets inside it, never by physical address. Masks select exactly one pin.
// Inversion maps the engine's logic level onto the pin level of the frontend.
type AA29GPIODescriptor struct {
	AA29Role          AA29ResourceRole
	AA29MapLength     uint64
	AA29AccessWidth   uint8
	AA29Ordering      AA29Ordering
	AA29RXLevelOffset uint64
	AA29TXSetOffset   uint64
	AA29TXClearOffset uint64
	AA29RXMask        uint32
	AA29TXMask        uint32
	AA29RXInverted    bool
	AA29TXInverted    bool
}

// AA29CounterDescriptor describes the monotonic counter the engine reads.
type AA29CounterDescriptor struct {
	AA29Role AA29ResourceRole
	AA29Hz   uint64
}

// AA29PlatformProfile is owner-provisioned. AA29Qualification must be
// AA22Proven and AA29QualificationDigest must equal AA29ProfileDigest of the
// same profile, so a qualification record covers exactly these contents. The
// profile qualifies the resource description only; it is not a timing claim.
type AA29PlatformProfile struct {
	AA29ProfileID           string
	AA29Family              string
	AA29Qualification       AA22Evidence
	AA29QualificationDigest [aa29sha256.Size]byte
	AA29GPIO                AA29GPIODescriptor
	AA29Counter             AA29CounterDescriptor
	AA29MaxLeaseTicks       uint64
}

// AA29Window is a mapped GPIO control window. Loads and stores must not block.
type AA29Window interface {
	AA29MappedLength() uint64
	AA29MappedAccessWidth() uint8
	AA29MappedOrdering() AA29Ordering
	AA29Load32(aa29Offset uint64) uint32
	AA29Store32(aa29Offset uint64, aa29Value uint32)
	AA29Unmap() error
}

// AA29MappingPort maps a window for an already validated descriptor from a
// resource its owner provisioned. The backend never chooses what to map.
type AA29MappingPort interface {
	AA29Map(aa29Descriptor AA29GPIODescriptor) (AA29Window, error)
}

// AA29CounterPort reads the monotonic counter. Neither method may block.
type AA29CounterPort interface {
	AA29CounterHz() uint64
	AA29ReadCounter() uint64
}

// AA29Lease bounds one arming. It names the admitted profile by digest and
// expires at AA29NotAfterTick of the backend counter.
type AA29Lease struct {
	AA29LeaseID       uint64
	AA29ProfileDigest [aa29sha256.Size]byte
	AA29NotAfterTick  uint64
}

// AA29Stats is a snapshot of bounded backend counters.
type AA29Stats struct {
	AA29StoredEdges   uint64
	AA29RefusedEdges  uint64
	AA29Releases      uint64
	AA29LeaseExpiries uint64
}

// The AA29 prefix is mandatory for every identifier in this ticket (enforced by
// TestAA29EveryDeclaredIdentifierIsPrefixed), which conflicts with the ErrFoo
// naming form that ST1012 expects.
var (
	AA29ErrProfileMissing  = aa29errors.New("transport: aa29 platform profile missing")            //nolint:staticcheck // ST1012: AA29 prefix is mandatory
	AA29ErrUnqualified     = aa29errors.New("transport: aa29 platform profile not qualified")      //nolint:staticcheck // ST1012: AA29 prefix is mandatory
	AA29ErrDescriptor      = aa29errors.New("transport: aa29 resource descriptor invalid")         //nolint:staticcheck // ST1012: AA29 prefix is mandatory
	AA29ErrResourceMissing = aa29errors.New("transport: aa29 control resource missing")            //nolint:staticcheck // ST1012: AA29 prefix is mandatory
	AA29ErrResourceMatch   = aa29errors.New("transport: aa29 control resource does not match")     //nolint:staticcheck // ST1012: AA29 prefix is mandatory
	AA29ErrLease           = aa29errors.New("transport: aa29 lease not admitted")                  //nolint:staticcheck // ST1012: AA29 prefix is mandatory
	AA29ErrClosed          = aa29errors.New("transport: aa29 backend closed")                      //nolint:staticcheck // ST1012: AA29 prefix is mandatory
	AA29ErrMapping         = aa29errors.New("transport: aa29 mapping port refused the descriptor") //nolint:staticcheck // ST1012: AA29 prefix is mandatory
)

// AA29ProfileDigest is SHA-256 over a fixed-order encoding of every profile
// field except AA29QualificationDigest itself.
func AA29ProfileDigest(aa29Profile AA29PlatformProfile) [aa29sha256.Size]byte {
	aa29Buffer := make([]byte, 0, 160)
	aa29Buffer = append(aa29Buffer, aa29DigestDomain...)
	aa29Buffer = append(aa29Buffer, 0)
	aa29Buffer = aa29AppendLabel(aa29Buffer, aa29Profile.AA29ProfileID)
	aa29Buffer = aa29AppendLabel(aa29Buffer, aa29Profile.AA29Family)
	aa29Buffer = append(aa29Buffer, byte(aa29Profile.AA29Qualification))
	aa29GPIO := aa29Profile.AA29GPIO
	aa29Buffer = append(aa29Buffer, byte(aa29GPIO.AA29Role))
	aa29Buffer = aa29binary.BigEndian.AppendUint64(aa29Buffer, aa29GPIO.AA29MapLength)
	aa29Buffer = append(aa29Buffer, aa29GPIO.AA29AccessWidth, byte(aa29GPIO.AA29Ordering))
	aa29Buffer = aa29binary.BigEndian.AppendUint64(aa29Buffer, aa29GPIO.AA29RXLevelOffset)
	aa29Buffer = aa29binary.BigEndian.AppendUint64(aa29Buffer, aa29GPIO.AA29TXSetOffset)
	aa29Buffer = aa29binary.BigEndian.AppendUint64(aa29Buffer, aa29GPIO.AA29TXClearOffset)
	aa29Buffer = aa29binary.BigEndian.AppendUint32(aa29Buffer, aa29GPIO.AA29RXMask)
	aa29Buffer = aa29binary.BigEndian.AppendUint32(aa29Buffer, aa29GPIO.AA29TXMask)
	aa29Buffer = append(aa29Buffer, aa29Bool(aa29GPIO.AA29RXInverted), aa29Bool(aa29GPIO.AA29TXInverted))
	aa29Buffer = append(aa29Buffer, byte(aa29Profile.AA29Counter.AA29Role))
	aa29Buffer = aa29binary.BigEndian.AppendUint64(aa29Buffer, aa29Profile.AA29Counter.AA29Hz)
	aa29Buffer = aa29binary.BigEndian.AppendUint64(aa29Buffer, aa29Profile.AA29MaxLeaseTicks)
	return aa29sha256.Sum256(aa29Buffer)
}

func aa29AppendLabel(aa29Buffer []byte, aa29Label string) []byte {
	aa29Buffer = aa29binary.BigEndian.AppendUint16(aa29Buffer, uint16(len(aa29Label)))
	return append(aa29Buffer, aa29Label...)
}

func aa29Bool(aa29Value bool) byte {
	if aa29Value {
		return 1
	}
	return 0
}

func aa29ValidLabel(aa29Label string) bool {
	if len(aa29Label) == 0 || len(aa29Label) > aa29MaxLabel {
		return false
	}
	for aa29Index := 0; aa29Index < len(aa29Label); aa29Index++ {
		if aa29Label[aa29Index] < 0x20 || aa29Label[aa29Index] > 0x7E {
			return false
		}
	}
	return true
}

// aa29RegisterFits reports whether one aligned access at aa29Offset lies wholly
// inside a window of aa29Length bytes.
func aa29RegisterFits(aa29Offset, aa29Length uint64) bool {
	return aa29Offset%uint64(aa29AccessWidth) == 0 && aa29Offset <= aa29Length-uint64(aa29AccessWidth)
}

func aa29ValidGPIO(aa29GPIO AA29GPIODescriptor) bool {
	if aa29GPIO.AA29Role != AA29RoleGPIOControl ||
		aa29GPIO.AA29AccessWidth != aa29AccessWidth ||
		aa29GPIO.AA29Ordering != AA29OrderingDevice ||
		aa29GPIO.AA29MapLength < uint64(aa29AccessWidth) ||
		aa29GPIO.AA29MapLength > aa29MaxMapLength ||
		aa29GPIO.AA29MapLength%uint64(aa29AccessWidth) != 0 {
		return false
	}
	if !aa29RegisterFits(aa29GPIO.AA29RXLevelOffset, aa29GPIO.AA29MapLength) ||
		!aa29RegisterFits(aa29GPIO.AA29TXSetOffset, aa29GPIO.AA29MapLength) ||
		!aa29RegisterFits(aa29GPIO.AA29TXClearOffset, aa29GPIO.AA29MapLength) {
		return false
	}
	if aa29GPIO.AA29TXSetOffset == aa29GPIO.AA29TXClearOffset ||
		aa29GPIO.AA29RXLevelOffset == aa29GPIO.AA29TXSetOffset ||
		aa29GPIO.AA29RXLevelOffset == aa29GPIO.AA29TXClearOffset {
		return false
	}
	return aa29bits.OnesCount32(aa29GPIO.AA29RXMask) == 1 && aa29bits.OnesCount32(aa29GPIO.AA29TXMask) == 1
}

// aa29Admit checks the profile alone. It touches no port.
func aa29Admit(aa29Profile *AA29PlatformProfile) ([aa29sha256.Size]byte, error) {
	if aa29Profile == nil {
		return [aa29sha256.Size]byte{}, AA29ErrProfileMissing
	}
	if aa29Profile.AA29Qualification != AA22Proven {
		return [aa29sha256.Size]byte{}, AA29ErrUnqualified
	}
	aa29Digest := AA29ProfileDigest(*aa29Profile)
	if aa29Digest != aa29Profile.AA29QualificationDigest {
		return [aa29sha256.Size]byte{}, AA29ErrUnqualified
	}
	if !aa29ValidLabel(aa29Profile.AA29ProfileID) || !aa29ValidLabel(aa29Profile.AA29Family) ||
		!aa29ValidGPIO(aa29Profile.AA29GPIO) ||
		aa29Profile.AA29Counter.AA29Role != AA29RoleCounter ||
		aa29Profile.AA29Counter.AA29Hz < aa29localworker.AA25NominalSampleRate ||
		aa29Profile.AA29MaxLeaseTicks == 0 {
		return [aa29sha256.Size]byte{}, AA29ErrDescriptor
	}
	return aa29Digest, nil
}

// aa29Absent treats a nil interface and a typed nil reference alike.
func aa29Absent(aa29Port any) bool {
	if aa29Port == nil {
		return true
	}
	aa29Value := aa29reflect.ValueOf(aa29Port)
	switch aa29Value.Kind() {
	case aa29reflect.Chan, aa29reflect.Func, aa29reflect.Interface, aa29reflect.Map, aa29reflect.Pointer, aa29reflect.Slice:
		return aa29Value.IsNil()
	}
	return false
}

// AA29Backend binds one admitted profile to one mapped window and counter.
type AA29Backend struct {
	aa29Profile AA29PlatformProfile
	aa29Digest  [aa29sha256.Size]byte
	aa29Window  AA29Window
	aa29Counter AA29CounterPort
	aa29Stats   AA29Stats

	aa29Closed  bool
	aa29Armed   bool
	aa29Driving bool
	aa29Lease   AA29Lease
}

// AA29Open admits the profile and checks both control resources before asking
// the mapping port for anything, then verifies the returned window against the
// descriptor. Every failure leaves no mapping held. The backend starts
// disarmed and writes nothing at open.
func AA29Open(aa29Profile *AA29PlatformProfile, aa29Mapping AA29MappingPort, aa29Counter AA29CounterPort) (*AA29Backend, error) {
	aa29Digest, aa29Err := aa29Admit(aa29Profile)
	if aa29Err != nil {
		return nil, aa29Err
	}
	if aa29Absent(aa29Mapping) || aa29Absent(aa29Counter) {
		return nil, AA29ErrResourceMissing
	}
	if aa29Counter.AA29CounterHz() != aa29Profile.AA29Counter.AA29Hz {
		return nil, AA29ErrResourceMatch
	}
	aa29GPIO := aa29Profile.AA29GPIO
	aa29Window, aa29Err := aa29Mapping.AA29Map(aa29GPIO)
	if aa29Err != nil {
		if !aa29Absent(aa29Window) {
			if aa29UnmapErr := aa29Window.AA29Unmap(); aa29UnmapErr != nil {
				return nil, aa29fmt.Errorf("%w: %w: %w", AA29ErrMapping, aa29Err, aa29UnmapErr)
			}
		}
		return nil, aa29fmt.Errorf("%w: %w", AA29ErrMapping, aa29Err)
	}
	if aa29Absent(aa29Window) {
		return nil, AA29ErrResourceMissing
	}
	if aa29Window.AA29MappedLength() != aa29GPIO.AA29MapLength ||
		aa29Window.AA29MappedAccessWidth() != aa29AccessWidth ||
		aa29Window.AA29MappedOrdering() != AA29OrderingDevice {
		if aa29UnmapErr := aa29Window.AA29Unmap(); aa29UnmapErr != nil {
			return nil, aa29fmt.Errorf("%w: %w", AA29ErrResourceMatch, aa29UnmapErr)
		}
		return nil, AA29ErrResourceMatch
	}
	return &AA29Backend{
		aa29Profile: *aa29Profile,
		aa29Digest:  aa29Digest,
		aa29Window:  aa29Window,
		aa29Counter: aa29Counter,
	}, nil
}

// AA29Stats returns a snapshot of the bounded counters.
func (aa29Backend *AA29Backend) AA29Stats() AA29Stats {
	return aa29Backend.aa29Stats
}

// AA22TransportCapabilities reports a local raw backend. Arbitration and
// qualified timing stay Unknown; the remaining claims are Hypothesis, because
// profile admission qualifies resources, not wire behavior. A closed backend
// claims nothing.
func (aa29Backend *AA29Backend) AA22TransportCapabilities() AA22Capabilities {
	if aa29Backend.aa29Closed {
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

// AA25SampleLevel reads the RX register. The level always comes from the
// window, never from the last TX level, so an echo is observed, not
// synthesized. A closed backend reads the idle level without a load.
func (aa29Backend *AA29Backend) AA25SampleLevel() aa29localworker.AA25Level {
	if aa29Backend.aa29Closed {
		return aa29localworker.AA25LevelHigh
	}
	aa29GPIO := aa29Backend.aa29Profile.AA29GPIO
	aa29PinHigh := aa29Backend.aa29Window.AA29Load32(aa29GPIO.AA29RXLevelOffset)&aa29GPIO.AA29RXMask != 0
	if aa29PinHigh != aa29GPIO.AA29RXInverted {
		return aa29localworker.AA25LevelHigh
	}
	return aa29localworker.AA25LevelLow
}

// AA25NowTicks reads the counter. A read at or past the lease expiry disarms
// and releases a driven line, so expiry holds without another edge request. A
// closed backend returns zero without a read, which the engine treats as a tick
// regression and a withdrawal.
func (aa29Backend *AA29Backend) AA25NowTicks() uint64 {
	if aa29Backend.aa29Closed {
		return 0
	}
	aa29Now := aa29Backend.aa29Counter.AA29ReadCounter()
	if aa29Backend.aa29Armed && aa29Now >= aa29Backend.aa29Lease.AA29NotAfterTick {
		aa29Backend.aa29Stats.AA29LeaseExpiries++
		aa29Backend.aa29Disarm()
	}
	return aa29Now
}

// AA25WriteEdge stores an edge only while a live lease arms the backend. Any
// other write is refused and counted. An expired lease disarms and releases.
func (aa29Backend *AA29Backend) AA25WriteEdge(aa29Level aa29localworker.AA25Level) {
	if aa29Backend.aa29Closed || !aa29Backend.aa29Armed {
		aa29Backend.aa29Stats.AA29RefusedEdges++
		return
	}
	if aa29Backend.aa29Counter.AA29ReadCounter() >= aa29Backend.aa29Lease.AA29NotAfterTick {
		aa29Backend.aa29Stats.AA29LeaseExpiries++
		aa29Backend.aa29Disarm()
		aa29Backend.aa29Stats.AA29RefusedEdges++
		return
	}
	aa29Backend.aa29Store(aa29Level)
	aa29Backend.aa29Stats.AA29StoredEdges++
}

func (aa29Backend *AA29Backend) aa29Store(aa29Level aa29localworker.AA25Level) {
	aa29GPIO := aa29Backend.aa29Profile.AA29GPIO
	aa29Offset := aa29GPIO.AA29TXClearOffset
	if (aa29Level == aa29localworker.AA25LevelHigh) != aa29GPIO.AA29TXInverted {
		aa29Offset = aa29GPIO.AA29TXSetOffset
	}
	aa29Backend.aa29Window.AA29Store32(aa29Offset, aa29GPIO.AA29TXMask)
	aa29Backend.aa29Driving = aa29Level == aa29localworker.AA25LevelLow
}

// aa29Disarm releases a driven line to the idle level once, then disarms. The
// release is a software store; it bounds nothing while the caller is not
// running.
func (aa29Backend *AA29Backend) aa29Disarm() {
	if aa29Backend.aa29Driving {
		aa29Backend.aa29Store(aa29localworker.AA25LevelHigh)
		aa29Backend.aa29Stats.AA29Releases++
	}
	aa29Backend.aa29Armed = false
	aa29Backend.aa29Lease = AA29Lease{}
}

// aa29Arm is backend-internal arm admission. It requires a nonzero lease bound
// to the admitted profile digest whose expiry is in the future and no further
// away than the profile's AA29MaxLeaseTicks. An armed backend must be revoked
// or expire before another lease is admitted.
func (aa29Backend *AA29Backend) aa29Arm(aa29Lease AA29Lease) error {
	if aa29Backend.aa29Closed {
		return AA29ErrClosed
	}
	if aa29Backend.aa29Armed || aa29Lease.AA29LeaseID == 0 || aa29Lease.AA29ProfileDigest != aa29Backend.aa29Digest {
		return AA29ErrLease
	}
	aa29Now := aa29Backend.aa29Counter.AA29ReadCounter()
	if aa29Lease.AA29NotAfterTick <= aa29Now || aa29Lease.AA29NotAfterTick-aa29Now > aa29Backend.aa29Profile.AA29MaxLeaseTicks {
		return AA29ErrLease
	}
	aa29Backend.aa29Lease = aa29Lease
	aa29Backend.aa29Armed = true
	return nil
}

// aa29Revoke disarms only for the lease that armed the backend.
func (aa29Backend *AA29Backend) aa29Revoke(aa29LeaseID uint64) bool {
	if !aa29Backend.aa29Armed || aa29LeaseID == 0 || aa29LeaseID != aa29Backend.aa29Lease.AA29LeaseID {
		return false
	}
	aa29Backend.aa29Disarm()
	return true
}

// AA29NewEngine binds this backend's level, edge and tick ports to an AA25
// engine. The engine's tick rate must be the admitted counter rate. Engine
// construction admits no lease: the engine still transmits only an offered
// prepared response, and this backend still refuses edges while disarmed.
func (aa29Backend *AA29Backend) AA29NewEngine(aa29Config aa29localworker.AA25Config, aa29Sink aa29localworker.AA25EventSink) (*aa29localworker.AA25Engine, error) {
	if aa29Backend.aa29Closed {
		return nil, AA29ErrClosed
	}
	if aa29Config.AA25TickHz != aa29Backend.aa29Profile.AA29Counter.AA29Hz {
		return nil, AA29ErrResourceMatch
	}
	return aa29localworker.AA25NewEngine(aa29Config, aa29localworker.AA25Ports{
		AA25Levels: aa29Backend,
		AA25Edges:  aa29Backend,
		AA25Ticks:  aa29Backend,
		AA25Sink:   aa29Sink,
	})
}

// AA29Close disarms, releasing a driven line, and unmaps the window once.
// Later calls return nil.
func (aa29Backend *AA29Backend) AA29Close() error {
	if aa29Backend.aa29Closed {
		return nil
	}
	aa29Backend.aa29Disarm()
	aa29Backend.aa29Closed = true
	return aa29Backend.aa29Window.AA29Unmap()
}
