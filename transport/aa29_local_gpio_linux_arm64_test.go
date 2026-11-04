//go:build linux && arm64 && aa29_local_gpio

package transport

import (
	aa29sha256 "crypto/sha256"
	aa29binary "encoding/binary"
	aa29errors "errors"
	aa29ast "go/ast"
	aa29parser "go/parser"
	aa29token "go/token"
	aa29os "os"
	aa29strconv "strconv"
	aa29strings "strings"
	aa29testing "testing"

	aa29localworker "github.com/Project-Helianthus/helianthus-ebusgo/localworker"
)

// Every profile, window and counter below is a synthetic fake. No test maps
// memory, reads a hardware counter or opens a device. Offsets and masks are
// arbitrary synthetic values, not board registers.
const (
	aa29TestProductionFile = "aa29_local_gpio_linux_arm64.go"
	aa29TestTestFile       = "aa29_local_gpio_linux_arm64_test.go"
	aa29TestBuildLine      = "//go:build linux && arm64 && aa29_local_gpio"

	aa29TestHz        uint64 = 1_000_000
	aa29TestNow       uint64 = 10_000
	aa29TestMaxLease  uint64 = 5_000
	aa29TestLength    uint64 = 0x40
	aa29TestRX        uint64 = 0x10
	aa29TestSet       uint64 = 0x20
	aa29TestClear     uint64 = 0x30
	aa29TestRXMask    uint32 = 1 << 3
	aa29TestTXMask    uint32 = 1 << 5
	aa29TestLeaseID   uint64 = 7
	aa29TestResponder byte   = 0x15
)

type aa29FakeStore struct {
	aa29Offset uint64
	aa29Value  uint32
}

type aa29FakeWindow struct {
	aa29Length    uint64
	aa29Width     uint8
	aa29Ordering  AA29Ordering
	aa29Registers map[uint64]uint32
	aa29Loads     []uint64
	aa29Stores    []aa29FakeStore
	aa29Unmaps    int
}

func (aa29Window *aa29FakeWindow) AA29MappedLength() uint64         { return aa29Window.aa29Length }
func (aa29Window *aa29FakeWindow) AA29MappedAccessWidth() uint8     { return aa29Window.aa29Width }
func (aa29Window *aa29FakeWindow) AA29MappedOrdering() AA29Ordering { return aa29Window.aa29Ordering }

func (aa29Window *aa29FakeWindow) AA29Load32(aa29Offset uint64) uint32 {
	aa29Window.aa29Loads = append(aa29Window.aa29Loads, aa29Offset)
	return aa29Window.aa29Registers[aa29Offset]
}

func (aa29Window *aa29FakeWindow) AA29Store32(aa29Offset uint64, aa29Value uint32) {
	aa29Window.aa29Stores = append(aa29Window.aa29Stores, aa29FakeStore{aa29Offset, aa29Value})
}

func (aa29Window *aa29FakeWindow) AA29Unmap() error {
	aa29Window.aa29Unmaps++
	return nil
}

type aa29FakeMapper struct {
	aa29Window *aa29FakeWindow
	aa29Err    error
	aa29Calls  int
	aa29Seen   []AA29GPIODescriptor
}

func (aa29Mapper *aa29FakeMapper) AA29Map(aa29Descriptor AA29GPIODescriptor) (AA29Window, error) {
	aa29Mapper.aa29Calls++
	aa29Mapper.aa29Seen = append(aa29Mapper.aa29Seen, aa29Descriptor)
	if aa29Mapper.aa29Window == nil {
		return nil, aa29Mapper.aa29Err
	}
	return aa29Mapper.aa29Window, aa29Mapper.aa29Err
}

type aa29FakeCounter struct {
	aa29Hz    uint64
	aa29Now   uint64
	aa29Reads int
}

func (aa29Counter *aa29FakeCounter) AA29CounterHz() uint64 { return aa29Counter.aa29Hz }

func (aa29Counter *aa29FakeCounter) AA29ReadCounter() uint64 {
	aa29Counter.aa29Reads++
	return aa29Counter.aa29Now
}

type aa29FakeSink struct {
	aa29Events []aa29localworker.AA25Event
}

func (aa29Sink *aa29FakeSink) AA25TryPublish(aa29Event aa29localworker.AA25Event) bool {
	aa29Sink.aa29Events = append(aa29Sink.aa29Events, aa29Event)
	return true
}

func aa29TestProfile() AA29PlatformProfile {
	aa29Profile := AA29PlatformProfile{
		AA29ProfileID:     "aa29-synthetic-profile",
		AA29Family:        "aa29-synthetic-family",
		AA29Qualification: AA22Proven,
		AA29GPIO: AA29GPIODescriptor{
			AA29Role:          AA29RoleGPIOControl,
			AA29MapLength:     aa29TestLength,
			AA29AccessWidth:   4,
			AA29Ordering:      AA29OrderingDevice,
			AA29RXLevelOffset: aa29TestRX,
			AA29TXSetOffset:   aa29TestSet,
			AA29TXClearOffset: aa29TestClear,
			AA29RXMask:        aa29TestRXMask,
			AA29TXMask:        aa29TestTXMask,
		},
		AA29Counter:       AA29CounterDescriptor{AA29Role: AA29RoleCounter, AA29Hz: aa29TestHz},
		AA29MaxLeaseTicks: aa29TestMaxLease,
	}
	return aa29Seal(aa29Profile)
}

func aa29Seal(aa29Profile AA29PlatformProfile) AA29PlatformProfile {
	aa29Profile.AA29QualificationDigest = AA29ProfileDigest(aa29Profile)
	return aa29Profile
}

func aa29TestWindow() *aa29FakeWindow {
	return &aa29FakeWindow{
		aa29Length:    aa29TestLength,
		aa29Width:     4,
		aa29Ordering:  AA29OrderingDevice,
		aa29Registers: map[uint64]uint32{aa29TestRX: aa29TestRXMask},
	}
}

type aa29Fixture struct {
	aa29Window  *aa29FakeWindow
	aa29Mapper  *aa29FakeMapper
	aa29Counter *aa29FakeCounter
	aa29Backend *AA29Backend
}

func aa29OpenFixture(aa29T *aa29testing.T, aa29Profile AA29PlatformProfile) *aa29Fixture {
	aa29T.Helper()
	aa29Window := aa29TestWindow()
	aa29Fix := &aa29Fixture{
		aa29Window:  aa29Window,
		aa29Mapper:  &aa29FakeMapper{aa29Window: aa29Window},
		aa29Counter: &aa29FakeCounter{aa29Hz: aa29TestHz, aa29Now: aa29TestNow},
	}
	aa29Backend, aa29Err := AA29Open(&aa29Profile, aa29Fix.aa29Mapper, aa29Fix.aa29Counter)
	if aa29Err != nil {
		aa29T.Fatalf("aa29 open of a valid profile: %v", aa29Err)
	}
	aa29Fix.aa29Backend = aa29Backend
	aa29T.Cleanup(func() {
		if aa29CloseErr := aa29Backend.AA29Close(); aa29CloseErr != nil {
			aa29T.Errorf("aa29 close: %v", aa29CloseErr)
		}
		if aa29Window.aa29Unmaps != 1 {
			aa29T.Errorf("aa29 unmaps after close = %d, want 1", aa29Window.aa29Unmaps)
		}
	})
	return aa29Fix
}

func aa29TestLease(aa29Profile AA29PlatformProfile) AA29Lease {
	return AA29Lease{
		AA29LeaseID:       aa29TestLeaseID,
		AA29ProfileDigest: aa29Profile.AA29QualificationDigest,
		AA29NotAfterTick:  aa29TestNow + 1_000,
	}
}

// aa29ExpectRefusedWithoutMapping opens a profile and requires the error and
// that no port was asked to map anything.
func aa29ExpectRefusedWithoutMapping(aa29T *aa29testing.T, aa29Profile *AA29PlatformProfile, aa29Want error) {
	aa29T.Helper()
	aa29Mapper := &aa29FakeMapper{aa29Window: aa29TestWindow()}
	aa29Counter := &aa29FakeCounter{aa29Hz: aa29TestHz, aa29Now: aa29TestNow}
	aa29Backend, aa29Err := AA29Open(aa29Profile, aa29Mapper, aa29Counter)
	if !aa29errors.Is(aa29Err, aa29Want) || aa29Backend != nil {
		aa29T.Fatalf("aa29 open = (%v, %v), want (nil, %v)", aa29Backend, aa29Err, aa29Want)
	}
	if aa29Mapper.aa29Calls != 0 || aa29Counter.aa29Reads != 0 {
		aa29T.Fatalf("aa29 refused open touched ports: maps=%d reads=%d", aa29Mapper.aa29Calls, aa29Counter.aa29Reads)
	}
}

// Catches: deleting or weakening the build line, which would put the backend in
// the default build or on another platform.
func TestAA29BuildConstraintIsNonDefault(aa29T *aa29testing.T) {
	for _, aa29Path := range []string{aa29TestProductionFile, aa29TestTestFile} {
		aa29Source, aa29Err := aa29os.ReadFile(aa29Path)
		if aa29Err != nil {
			aa29T.Fatalf("aa29 read %s: %v", aa29Path, aa29Err)
		}
		aa29First, _, _ := aa29strings.Cut(string(aa29Source), "\n")
		if aa29First != aa29TestBuildLine {
			aa29T.Fatalf("aa29 %s build line = %q, want %q", aa29Path, aa29First, aa29TestBuildLine)
		}
	}
}

// Catches: adding an OS, syscall or device-path dependency to the boundary,
// which would let it open or look up a resource itself.
func TestAA29ProductionOpensNoResourceItself(aa29T *aa29testing.T) {
	aa29File := aa29ParseSource(aa29T, aa29TestProductionFile)
	aa29Allowed := map[string]bool{
		"crypto/sha256":   true,
		"encoding/binary": true,
		"errors":          true,
		"fmt":             true,
		"math/bits":       true,
		"reflect":         true,
		"github.com/Project-Helianthus/helianthus-ebusgo/localworker": true,
	}
	for _, aa29Import := range aa29File.Imports {
		aa29Path, aa29Err := aa29strconv.Unquote(aa29Import.Path.Value)
		if aa29Err != nil || !aa29Allowed[aa29Path] {
			aa29T.Fatalf("aa29 production import %s is outside the boundary", aa29Import.Path.Value)
		}
	}
	aa29ast.Inspect(aa29File, func(aa29Node aa29ast.Node) bool {
		if aa29Literal, aa29OK := aa29Node.(*aa29ast.BasicLit); aa29OK && aa29strings.Contains(aa29Literal.Value, "/dev") {
			aa29T.Fatalf("aa29 production literal %s names a device path", aa29Literal.Value)
		}
		return true
	})
}

// Positive control. Catches: writing TX at open, starting armed, claiming
// Proven arbitration or timing, or reporting a non-raw kind.
func TestAA29OpenAdmitsQualifiedProfileAndStartsPassive(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	aa29Fix := aa29OpenFixture(aa29T, aa29Profile)
	if aa29Fix.aa29Mapper.aa29Calls != 1 || aa29Fix.aa29Mapper.aa29Seen[0] != aa29Profile.AA29GPIO {
		aa29T.Fatalf("aa29 mapping calls = %d %+v, want the profile descriptor once", aa29Fix.aa29Mapper.aa29Calls, aa29Fix.aa29Mapper.aa29Seen)
	}
	if len(aa29Fix.aa29Window.aa29Stores) != 0 || len(aa29Fix.aa29Window.aa29Loads) != 0 {
		aa29T.Fatalf("aa29 open accessed the window: stores=%v loads=%v", aa29Fix.aa29Window.aa29Stores, aa29Fix.aa29Window.aa29Loads)
	}
	if aa29Fix.aa29Backend.aa29Armed {
		aa29T.Fatal("aa29 backend starts armed")
	}
	aa29Want := AA22Capabilities{
		AA22Kind:                       AA22KindLocalRaw,
		AA22Encoding:                   AA22EncodingRaw,
		AA22PhysicalArbitration:        AA22Unknown,
		AA22AutonomousPreparedResponse: AA22Hypothesis,
		AA22ObservedEcho:               AA22Hypothesis,
		AA22CaptureTimestamps:          AA22Hypothesis,
		AA22QualifiedTiming:            AA22Unknown,
	}
	aa29Got := AA22QueryCapabilities(aa29Fix.aa29Backend)
	if aa29Got != aa29Want {
		aa29T.Fatalf("aa29 capabilities = %+v, want %+v", aa29Got, aa29Want)
	}
	if aa29Got.AA22DeadlineSuitable() {
		aa29T.Fatal("aa29 backend claims deadline suitability")
	}
}

// Catches: treating an absent profile as a default profile.
func TestAA29OpenRejectsMissingProfileWithoutMapping(aa29T *aa29testing.T) {
	aa29ExpectRefusedWithoutMapping(aa29T, nil, AA29ErrProfileMissing)
}

// Catches: accepting Unknown, Hypothesis or an out-of-range evidence value as
// qualification, even with an internally consistent digest.
func TestAA29OpenRejectsUnqualifiedProfileWithoutMapping(aa29T *aa29testing.T) {
	for _, aa29Evidence := range []AA22Evidence{AA22Unknown, AA22Hypothesis, AA22Proven + 1} {
		aa29Profile := aa29TestProfile()
		aa29Profile.AA29Qualification = aa29Evidence
		aa29Profile = aa29Seal(aa29Profile)
		aa29ExpectRefusedWithoutMapping(aa29T, &aa29Profile, AA29ErrUnqualified)
	}
	aa29Unsealed := aa29TestProfile()
	aa29Unsealed.AA29QualificationDigest = [aa29sha256.Size]byte{}
	aa29ExpectRefusedWithoutMapping(aa29T, &aa29Unsealed, AA29ErrUnqualified)
}

// aa29IndependentDigest restates the documented field order without calling
// production encoding helpers.
func aa29IndependentDigest(aa29Profile AA29PlatformProfile) [aa29sha256.Size]byte {
	var aa29Buffer []byte
	aa29Buffer = append(aa29Buffer, []byte("aa29.profile.1\x00")...)
	for _, aa29Label := range []string{aa29Profile.AA29ProfileID, aa29Profile.AA29Family} {
		aa29Buffer = append(aa29Buffer, byte(len(aa29Label)>>8), byte(len(aa29Label)))
		aa29Buffer = append(aa29Buffer, aa29Label...)
	}
	aa29G := aa29Profile.AA29GPIO
	aa29Buffer = append(aa29Buffer, byte(aa29Profile.AA29Qualification), byte(aa29G.AA29Role))
	aa29Word := make([]byte, 8)
	aa29Put := func(aa29Value uint64) {
		aa29binary.BigEndian.PutUint64(aa29Word, aa29Value)
		aa29Buffer = append(aa29Buffer, aa29Word...)
	}
	aa29Put(aa29G.AA29MapLength)
	aa29Buffer = append(aa29Buffer, aa29G.AA29AccessWidth, byte(aa29G.AA29Ordering))
	aa29Put(aa29G.AA29RXLevelOffset)
	aa29Put(aa29G.AA29TXSetOffset)
	aa29Put(aa29G.AA29TXClearOffset)
	aa29Buffer = append(aa29Buffer, byte(aa29G.AA29RXMask>>24), byte(aa29G.AA29RXMask>>16), byte(aa29G.AA29RXMask>>8), byte(aa29G.AA29RXMask))
	aa29Buffer = append(aa29Buffer, byte(aa29G.AA29TXMask>>24), byte(aa29G.AA29TXMask>>16), byte(aa29G.AA29TXMask>>8), byte(aa29G.AA29TXMask))
	aa29Flag := func(aa29Value bool) byte {
		if aa29Value {
			return 1
		}
		return 0
	}
	aa29Buffer = append(aa29Buffer, aa29Flag(aa29G.AA29RXInverted), aa29Flag(aa29G.AA29TXInverted), byte(aa29Profile.AA29Counter.AA29Role))
	aa29Put(aa29Profile.AA29Counter.AA29Hz)
	aa29Put(aa29Profile.AA29MaxLeaseTicks)
	return aa29sha256.Sum256(aa29Buffer)
}

// Catches: reordering, dropping or re-encoding a digest field, and folding the
// stored digest into its own input.
func TestAA29ProfileDigestMatchesIndependentEncoding(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	aa29Profile.AA29GPIO.AA29RXInverted = true
	if aa29Got, aa29Want := AA29ProfileDigest(aa29Profile), aa29IndependentDigest(aa29Profile); aa29Got != aa29Want {
		aa29T.Fatalf("aa29 digest = %x, want %x", aa29Got, aa29Want)
	}
	aa29Other := aa29Profile
	aa29Other.AA29QualificationDigest[0] ^= 0xFF
	if AA29ProfileDigest(aa29Other) != AA29ProfileDigest(aa29Profile) {
		aa29T.Fatal("aa29 digest covers the stored digest")
	}
}

// Every profile field is varied alone after sealing. Catches, per field:
// omitting that field from AA29ProfileDigest (the digest would not change), or
// skipping the digest comparison in admission (the open would succeed).
func TestAA29QualificationDigestCoversEveryProfileField(aa29T *aa29testing.T) {
	aa29Cases := []struct {
		aa29Name   string
		aa29Valid  bool
		aa29Mutate func(*AA29PlatformProfile)
	}{
		{"AA29ProfileID", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29ProfileID += "x" }},
		{"AA29Family", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29Family += "x" }},
		{"AA29Qualification", false, func(aa29P *AA29PlatformProfile) { aa29P.AA29Qualification = AA22Hypothesis }},
		{"AA29GPIO.AA29Role", false, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29Role = AA29RoleCounter }},
		{"AA29GPIO.AA29MapLength", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29MapLength += 4 }},
		{"AA29GPIO.AA29AccessWidth", false, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29AccessWidth = 8 }},
		{"AA29GPIO.AA29Ordering", false, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29Ordering = AA29OrderingRelaxed }},
		{"AA29GPIO.AA29RXLevelOffset", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXLevelOffset += 4 }},
		{"AA29GPIO.AA29TXSetOffset", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXSetOffset += 4 }},
		{"AA29GPIO.AA29TXClearOffset", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXClearOffset += 4 }},
		{"AA29GPIO.AA29RXMask", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXMask <<= 1 }},
		{"AA29GPIO.AA29TXMask", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXMask <<= 1 }},
		{"AA29GPIO.AA29RXInverted", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXInverted = true }},
		{"AA29GPIO.AA29TXInverted", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXInverted = true }},
		{"AA29Counter.AA29Role", false, func(aa29P *AA29PlatformProfile) { aa29P.AA29Counter.AA29Role = AA29RoleGPIOControl }},
		{"AA29Counter.AA29Hz", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29Counter.AA29Hz++ }},
		{"AA29MaxLeaseTicks", true, func(aa29P *AA29PlatformProfile) { aa29P.AA29MaxLeaseTicks++ }},
	}
	aa29Sealed := aa29TestProfile()
	for _, aa29Case := range aa29Cases {
		aa29T.Run(aa29Case.aa29Name, func(aa29T *aa29testing.T) {
			aa29Mutated := aa29Sealed
			aa29Case.aa29Mutate(&aa29Mutated)
			if AA29ProfileDigest(aa29Mutated) == aa29Sealed.AA29QualificationDigest {
				aa29T.Fatal("aa29 digest ignores the field")
			}
			aa29ExpectRefusedWithoutMapping(aa29T, &aa29Mutated, AA29ErrUnqualified)
			if !aa29Case.aa29Valid {
				return
			}
			aa29Resealed := aa29Seal(aa29Mutated)
			aa29Counter := &aa29FakeCounter{aa29Hz: aa29Resealed.AA29Counter.AA29Hz}
			aa29Window := aa29TestWindow()
			aa29Window.aa29Length = aa29Resealed.AA29GPIO.AA29MapLength
			aa29Backend, aa29Err := AA29Open(&aa29Resealed, &aa29FakeMapper{aa29Window: aa29Window}, aa29Counter)
			if aa29Err != nil {
				aa29T.Fatalf("aa29 resealed control refused: %v", aa29Err)
			}
			if aa29CloseErr := aa29Backend.AA29Close(); aa29CloseErr != nil {
				aa29T.Fatalf("aa29 close: %v", aa29CloseErr)
			}
		})
	}
}

// Each case is resealed, so only descriptor validation can refuse it. Catches:
// accepting an absent descriptor, a wrong role, an access width other than
// four bytes, non-device ordering, an empty, oversized or unaligned window, a
// register outside the window or unaligned, aliased registers, a mask that is
// not exactly one pin, a counter below the sample rate, an unbounded lease or
// an invalid label.
func TestAA29OpenRejectsInvalidDescriptorsWithoutMapping(aa29T *aa29testing.T) {
	aa29Cases := []struct {
		aa29Name   string
		aa29Mutate func(*AA29PlatformProfile)
	}{
		{"absent GPIO descriptor", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO = AA29GPIODescriptor{} }},
		{"absent counter descriptor", func(aa29P *AA29PlatformProfile) { aa29P.AA29Counter = AA29CounterDescriptor{} }},
		{"GPIO role counter", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29Role = AA29RoleCounter }},
		{"counter role GPIO", func(aa29P *AA29PlatformProfile) { aa29P.AA29Counter.AA29Role = AA29RoleGPIOControl }},
		{"width 1", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29AccessWidth = 1 }},
		{"width 2", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29AccessWidth = 2 }},
		{"width 8", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29AccessWidth = 8 }},
		{"ordering unknown", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29Ordering = AA29OrderingUnknown }},
		{"ordering relaxed", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29Ordering = AA29OrderingRelaxed }},
		{"length zero", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29MapLength = 0 }},
		{"length above bound", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29MapLength = 1<<16 + 4 }},
		{"length unaligned", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29MapLength = aa29TestLength + 2 }},
		{"RX outside", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXLevelOffset = aa29TestLength }},
		{"set outside", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXSetOffset = aa29TestLength }},
		{"clear outside", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXClearOffset = aa29TestLength }},
		{"RX wraps", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXLevelOffset = ^uint64(0) - 3 }},
		{"RX unaligned", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXLevelOffset = aa29TestRX + 1 }},
		{"set unaligned", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXSetOffset = aa29TestSet + 2 }},
		{"clear unaligned", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXClearOffset = aa29TestClear + 3 }},
		{"set aliases clear", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXClearOffset = aa29TestSet }},
		{"RX aliases set", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXLevelOffset = aa29TestSet }},
		{"RX aliases clear", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXLevelOffset = aa29TestClear }},
		{"RX mask empty", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXMask = 0 }},
		{"RX mask two pins", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29RXMask = 0b11 }},
		{"TX mask empty", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXMask = 0 }},
		{"TX mask two pins", func(aa29P *AA29PlatformProfile) { aa29P.AA29GPIO.AA29TXMask = 0b101 }},
		{"counter below sample rate", func(aa29P *AA29PlatformProfile) {
			aa29P.AA29Counter.AA29Hz = aa29localworker.AA25NominalSampleRate - 1
		}},
		{"lease unbounded", func(aa29P *AA29PlatformProfile) { aa29P.AA29MaxLeaseTicks = 0 }},
		{"profile ID empty", func(aa29P *AA29PlatformProfile) { aa29P.AA29ProfileID = "" }},
		{"profile ID long", func(aa29P *AA29PlatformProfile) { aa29P.AA29ProfileID = aa29strings.Repeat("p", 65) }},
		{"profile ID control byte", func(aa29P *AA29PlatformProfile) { aa29P.AA29ProfileID = "aa29\n" }},
		{"family empty", func(aa29P *AA29PlatformProfile) { aa29P.AA29Family = "" }},
		{"family non-ASCII", func(aa29P *AA29PlatformProfile) { aa29P.AA29Family = "aa29\x7F" }},
	}
	for _, aa29Case := range aa29Cases {
		aa29T.Run(aa29Case.aa29Name, func(aa29T *aa29testing.T) {
			aa29Profile := aa29TestProfile()
			aa29Case.aa29Mutate(&aa29Profile)
			aa29Profile = aa29Seal(aa29Profile)
			aa29ExpectRefusedWithoutMapping(aa29T, &aa29Profile, AA29ErrDescriptor)
		})
	}
}

// Positive boundary control: registers in the last word, a window at the bound
// and 64-character labels are admitted, so the refusals above are not caused by
// an over-tight bound.
func TestAA29OpenAdmitsDescriptorBoundaries(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	aa29Profile.AA29GPIO.AA29MapLength = 1 << 16
	aa29Profile.AA29GPIO.AA29RXLevelOffset = 1<<16 - 4
	aa29Profile.AA29GPIO.AA29TXSetOffset = 0
	aa29Profile.AA29GPIO.AA29TXClearOffset = 1<<16 - 8
	aa29Profile.AA29GPIO.AA29RXMask = 1 << 31
	aa29Profile.AA29GPIO.AA29TXMask = 1
	aa29Profile.AA29Counter.AA29Hz = aa29localworker.AA25NominalSampleRate
	aa29Profile.AA29ProfileID = aa29strings.Repeat("p", 64)
	aa29Profile.AA29Family = aa29strings.Repeat("~", 64)
	aa29Profile = aa29Seal(aa29Profile)
	aa29Window := aa29TestWindow()
	aa29Window.aa29Length = 1 << 16
	aa29Backend, aa29Err := AA29Open(&aa29Profile, &aa29FakeMapper{aa29Window: aa29Window},
		&aa29FakeCounter{aa29Hz: aa29localworker.AA25NominalSampleRate})
	if aa29Err != nil {
		aa29T.Fatalf("aa29 boundary profile refused: %v", aa29Err)
	}
	if aa29CloseErr := aa29Backend.AA29Close(); aa29CloseErr != nil {
		aa29T.Fatalf("aa29 close: %v", aa29CloseErr)
	}
}

type aa29NilMapper struct{}

// AA29Map returns a typed nil window with no error.
func (*aa29NilMapper) AA29Map(AA29GPIODescriptor) (AA29Window, error) {
	var aa29Window *aa29FakeWindow
	return aa29Window, nil
}

// Catches: mapping without a counter or mapping port, proceeding with a counter
// whose rate differs from the profile, ignoring a mapping error, or accepting a
// nil window.
func TestAA29OpenRejectsAbsentControlResources(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	var aa29TypedNilMapper *aa29FakeMapper
	var aa29TypedNilCounter *aa29FakeCounter
	aa29Mapper := &aa29FakeMapper{aa29Window: aa29TestWindow()}
	aa29Counter := &aa29FakeCounter{aa29Hz: aa29TestHz}
	for _, aa29Case := range []struct {
		aa29Name    string
		aa29Mapping AA29MappingPort
		aa29Counter AA29CounterPort
	}{
		{"nil mapping", nil, aa29Counter},
		{"typed nil mapping", aa29TypedNilMapper, aa29Counter},
		{"nil counter", aa29Mapper, nil},
		{"typed nil counter", aa29Mapper, aa29TypedNilCounter},
	} {
		if aa29Backend, aa29Err := AA29Open(&aa29Profile, aa29Case.aa29Mapping, aa29Case.aa29Counter); !aa29errors.Is(aa29Err, AA29ErrResourceMissing) || aa29Backend != nil {
			aa29T.Fatalf("aa29 %s open = (%v, %v)", aa29Case.aa29Name, aa29Backend, aa29Err)
		}
	}
	if aa29Mapper.aa29Calls != 0 {
		aa29T.Fatalf("aa29 mapped %d times without both resources", aa29Mapper.aa29Calls)
	}

	aa29Mismatch := &aa29FakeCounter{aa29Hz: aa29TestHz + 1}
	if _, aa29Err := AA29Open(&aa29Profile, aa29Mapper, aa29Mismatch); !aa29errors.Is(aa29Err, AA29ErrResourceMatch) || aa29Mapper.aa29Calls != 0 || aa29Mismatch.aa29Reads != 0 {
		aa29T.Fatalf("aa29 counter rate mismatch = %v, maps=%d reads=%d", aa29Err, aa29Mapper.aa29Calls, aa29Mismatch.aa29Reads)
	}

	aa29Cause := aa29errors.New("aa29 synthetic refusal")
	aa29Failing := &aa29FakeMapper{aa29Window: aa29TestWindow(), aa29Err: aa29Cause}
	if aa29Backend, aa29Err := AA29Open(&aa29Profile, aa29Failing, aa29Counter); !aa29errors.Is(aa29Err, AA29ErrMapping) || !aa29errors.Is(aa29Err, aa29Cause) || aa29Backend != nil {
		aa29T.Fatalf("aa29 mapping error = (%v, %v)", aa29Backend, aa29Err)
	}
	if aa29Failing.aa29Window.aa29Unmaps != 1 {
		aa29T.Fatalf("aa29 window returned with an error was unmapped %d times, want 1", aa29Failing.aa29Window.aa29Unmaps)
	}

	if aa29Backend, aa29Err := AA29Open(&aa29Profile, &aa29NilMapper{}, aa29Counter); !aa29errors.Is(aa29Err, AA29ErrResourceMissing) || aa29Backend != nil {
		aa29T.Fatalf("aa29 typed nil window = (%v, %v)", aa29Backend, aa29Err)
	}
	if aa29Backend, aa29Err := AA29Open(&aa29Profile, &aa29FakeMapper{}, aa29Counter); !aa29errors.Is(aa29Err, AA29ErrResourceMissing) || aa29Backend != nil {
		aa29T.Fatalf("aa29 nil window = (%v, %v)", aa29Backend, aa29Err)
	}
}

// Each window precondition is varied alone. Catches: ignoring the mapped size,
// the access width or the ordering the mapping actually provides, or leaking
// the refused mapping.
func TestAA29OpenRejectsWindowPreconditionsAndUnmaps(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	for _, aa29Case := range []struct {
		aa29Name   string
		aa29Mutate func(*aa29FakeWindow)
	}{
		{"length short", func(aa29W *aa29FakeWindow) { aa29W.aa29Length = aa29TestLength - 4 }},
		{"length long", func(aa29W *aa29FakeWindow) { aa29W.aa29Length = aa29TestLength + 4 }},
		{"width 2", func(aa29W *aa29FakeWindow) { aa29W.aa29Width = 2 }},
		{"width 8", func(aa29W *aa29FakeWindow) { aa29W.aa29Width = 8 }},
		{"ordering relaxed", func(aa29W *aa29FakeWindow) { aa29W.aa29Ordering = AA29OrderingRelaxed }},
		{"ordering unknown", func(aa29W *aa29FakeWindow) { aa29W.aa29Ordering = AA29OrderingUnknown }},
	} {
		aa29T.Run(aa29Case.aa29Name, func(aa29T *aa29testing.T) {
			aa29Window := aa29TestWindow()
			aa29Case.aa29Mutate(aa29Window)
			aa29Backend, aa29Err := AA29Open(&aa29Profile, &aa29FakeMapper{aa29Window: aa29Window}, &aa29FakeCounter{aa29Hz: aa29TestHz})
			if !aa29errors.Is(aa29Err, AA29ErrResourceMatch) || aa29Backend != nil {
				aa29T.Fatalf("aa29 open = (%v, %v), want resource mismatch", aa29Backend, aa29Err)
			}
			if aa29Window.aa29Unmaps != 1 || len(aa29Window.aa29Stores) != 0 || len(aa29Window.aa29Loads) != 0 {
				aa29T.Fatalf("aa29 refused window: unmaps=%d stores=%v loads=%v", aa29Window.aa29Unmaps, aa29Window.aa29Stores, aa29Window.aa29Loads)
			}
		})
	}
}

// Catches: a backend that stores an edge without an admitted lease, i.e. a
// default transmit path.
func TestAA29DefaultTransmitIsRefused(aa29T *aa29testing.T) {
	aa29Fix := aa29OpenFixture(aa29T, aa29TestProfile())
	aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
	aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelHigh)
	if len(aa29Fix.aa29Window.aa29Stores) != 0 {
		aa29T.Fatalf("aa29 disarmed backend stored %v", aa29Fix.aa29Window.aa29Stores)
	}
	if aa29Stats := aa29Fix.aa29Backend.AA29Stats(); aa29Stats.AA29RefusedEdges != 2 || aa29Stats.AA29StoredEdges != 0 {
		aa29T.Fatalf("aa29 stats = %+v", aa29Stats)
	}
}

// The lease identity is (AA29LeaseID, AA29ProfileDigest, AA29NotAfterTick);
// each is varied alone. Catches: admitting a zero lease ID, a lease for another
// profile digest, an expired or already-due lease, a lease beyond
// AA29MaxLeaseTicks, or a second lease over a live one.
func TestAA29ArmRequiresLeaseBoundToProfile(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	for _, aa29Case := range []struct {
		aa29Name   string
		aa29Mutate func(*AA29Lease)
		aa29Admit  bool
	}{
		{"valid", func(*AA29Lease) {}, true},
		{"ID zero", func(aa29L *AA29Lease) { aa29L.AA29LeaseID = 0 }, false},
		{"ID other", func(aa29L *AA29Lease) { aa29L.AA29LeaseID = aa29TestLeaseID + 1 }, true},
		{"digest first byte", func(aa29L *AA29Lease) { aa29L.AA29ProfileDigest[0] ^= 1 }, false},
		{"digest last byte", func(aa29L *AA29Lease) { aa29L.AA29ProfileDigest[aa29sha256.Size-1] ^= 0x80 }, false},
		{"digest zero", func(aa29L *AA29Lease) { aa29L.AA29ProfileDigest = [aa29sha256.Size]byte{} }, false},
		{"expiry now", func(aa29L *AA29Lease) { aa29L.AA29NotAfterTick = aa29TestNow }, false},
		{"expiry past", func(aa29L *AA29Lease) { aa29L.AA29NotAfterTick = aa29TestNow - 1 }, false},
		{"expiry next tick", func(aa29L *AA29Lease) { aa29L.AA29NotAfterTick = aa29TestNow + 1 }, true},
		{"expiry at bound", func(aa29L *AA29Lease) { aa29L.AA29NotAfterTick = aa29TestNow + aa29TestMaxLease }, true},
		{"expiry beyond bound", func(aa29L *AA29Lease) { aa29L.AA29NotAfterTick = aa29TestNow + aa29TestMaxLease + 1 }, false},
	} {
		aa29T.Run(aa29Case.aa29Name, func(aa29T *aa29testing.T) {
			aa29Fix := aa29OpenFixture(aa29T, aa29Profile)
			aa29Lease := aa29TestLease(aa29Profile)
			aa29Case.aa29Mutate(&aa29Lease)
			aa29Err := aa29Fix.aa29Backend.aa29Arm(aa29Lease)
			if aa29Case.aa29Admit != (aa29Err == nil) || aa29Fix.aa29Backend.aa29Armed != aa29Case.aa29Admit {
				aa29T.Fatalf("aa29 arm = %v armed=%v, want admit %v", aa29Err, aa29Fix.aa29Backend.aa29Armed, aa29Case.aa29Admit)
			}
			if !aa29Case.aa29Admit {
				if !aa29errors.Is(aa29Err, AA29ErrLease) {
					aa29T.Fatalf("aa29 arm error = %v, want lease refusal", aa29Err)
				}
				aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
				if len(aa29Fix.aa29Window.aa29Stores) != 0 {
					aa29T.Fatalf("aa29 refused lease still stored %v", aa29Fix.aa29Window.aa29Stores)
				}
				return
			}
			if aa29Second := aa29Fix.aa29Backend.aa29Arm(aa29TestLease(aa29Profile)); !aa29errors.Is(aa29Second, AA29ErrLease) {
				aa29T.Fatalf("aa29 second arm over a live lease = %v", aa29Second)
			}
		})
	}
}

// Catches: storing to the wrong register, the wrong mask, ignoring TX inversion
// or writing a level instead of a set/clear pin store.
func TestAA29ArmedEdgesUseDescriptorRegistersAndPolarity(aa29T *aa29testing.T) {
	for _, aa29Inverted := range []bool{false, true} {
		aa29Profile := aa29TestProfile()
		aa29Profile.AA29GPIO.AA29TXInverted = aa29Inverted
		aa29Profile = aa29Seal(aa29Profile)
		aa29Fix := aa29OpenFixture(aa29T, aa29Profile)
		if aa29Err := aa29Fix.aa29Backend.aa29Arm(aa29TestLease(aa29Profile)); aa29Err != nil {
			aa29T.Fatalf("aa29 arm: %v", aa29Err)
		}
		aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
		aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelHigh)
		aa29LowOffset, aa29HighOffset := aa29TestClear, aa29TestSet
		if aa29Inverted {
			aa29LowOffset, aa29HighOffset = aa29TestSet, aa29TestClear
		}
		aa29Want := []aa29FakeStore{{aa29LowOffset, aa29TestTXMask}, {aa29HighOffset, aa29TestTXMask}}
		if len(aa29Fix.aa29Window.aa29Stores) != 2 || aa29Fix.aa29Window.aa29Stores[0] != aa29Want[0] || aa29Fix.aa29Window.aa29Stores[1] != aa29Want[1] {
			aa29T.Fatalf("aa29 inverted=%v stores = %v, want %v", aa29Inverted, aa29Fix.aa29Window.aa29Stores, aa29Want)
		}
		if aa29Stats := aa29Fix.aa29Backend.AA29Stats(); aa29Stats.AA29StoredEdges != 2 || aa29Stats.AA29RefusedEdges != 0 {
			aa29T.Fatalf("aa29 stats = %+v", aa29Stats)
		}
	}
}

// Catches: ignoring lease expiry on the edge path, leaving a driven line low on
// expiry, or staying armed after expiry.
func TestAA29LeaseExpiryDisarmsAndReleases(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	aa29Fix := aa29OpenFixture(aa29T, aa29Profile)
	aa29Lease := aa29TestLease(aa29Profile)
	if aa29Err := aa29Fix.aa29Backend.aa29Arm(aa29Lease); aa29Err != nil {
		aa29T.Fatalf("aa29 arm: %v", aa29Err)
	}
	aa29Fix.aa29Counter.aa29Now = aa29Lease.AA29NotAfterTick - 1
	aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
	aa29Fix.aa29Counter.aa29Now = aa29Lease.AA29NotAfterTick
	aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
	aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
	aa29Want := []aa29FakeStore{{aa29TestClear, aa29TestTXMask}, {aa29TestSet, aa29TestTXMask}}
	if len(aa29Fix.aa29Window.aa29Stores) != 2 || aa29Fix.aa29Window.aa29Stores[0] != aa29Want[0] || aa29Fix.aa29Window.aa29Stores[1] != aa29Want[1] {
		aa29T.Fatalf("aa29 stores = %v, want drive then release %v", aa29Fix.aa29Window.aa29Stores, aa29Want)
	}
	aa29Stats := aa29Fix.aa29Backend.AA29Stats()
	if aa29Fix.aa29Backend.aa29Armed || aa29Stats.AA29LeaseExpiries != 1 || aa29Stats.AA29Releases != 1 || aa29Stats.AA29RefusedEdges != 2 || aa29Stats.AA29StoredEdges != 1 {
		aa29T.Fatalf("aa29 after expiry armed=%v stats=%+v", aa29Fix.aa29Backend.aa29Armed, aa29Stats)
	}
}

// Catches: revoking for a zero or foreign lease ID, or revoking without
// releasing a driven line.
func TestAA29RevokeDisarmsOnlyItsLeaseAndReleases(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	aa29Fix := aa29OpenFixture(aa29T, aa29Profile)
	if aa29Err := aa29Fix.aa29Backend.aa29Arm(aa29TestLease(aa29Profile)); aa29Err != nil {
		aa29T.Fatalf("aa29 arm: %v", aa29Err)
	}
	aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
	if aa29Fix.aa29Backend.aa29Revoke(0) || aa29Fix.aa29Backend.aa29Revoke(aa29TestLeaseID+1) || !aa29Fix.aa29Backend.aa29Armed {
		aa29T.Fatal("aa29 foreign revoke disarmed the backend")
	}
	if !aa29Fix.aa29Backend.aa29Revoke(aa29TestLeaseID) || aa29Fix.aa29Backend.aa29Armed {
		aa29T.Fatal("aa29 matching revoke did not disarm")
	}
	aa29Last := aa29Fix.aa29Window.aa29Stores[len(aa29Fix.aa29Window.aa29Stores)-1]
	if len(aa29Fix.aa29Window.aa29Stores) != 2 || aa29Last != (aa29FakeStore{aa29TestSet, aa29TestTXMask}) {
		aa29T.Fatalf("aa29 stores after revoke = %v", aa29Fix.aa29Window.aa29Stores)
	}
	aa29Fix.aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
	if len(aa29Fix.aa29Window.aa29Stores) != 2 {
		aa29T.Fatal("aa29 revoked backend stored an edge")
	}
}

// The RX register is independent of TX here, as on a line another device can
// hold. Catches: synthesizing the echo from the last TX level, reading the
// wrong register, ignoring the RX mask or ignoring RX inversion.
func TestAA29SampleLevelReadsWindowNeverSynthesizesEcho(aa29T *aa29testing.T) {
	for _, aa29Inverted := range []bool{false, true} {
		aa29Profile := aa29TestProfile()
		aa29Profile.AA29GPIO.AA29RXInverted = aa29Inverted
		aa29Profile = aa29Seal(aa29Profile)
		aa29Fix := aa29OpenFixture(aa29T, aa29Profile)
		if aa29Err := aa29Fix.aa29Backend.aa29Arm(aa29TestLease(aa29Profile)); aa29Err != nil {
			aa29T.Fatalf("aa29 arm: %v", aa29Err)
		}
		aa29PinHigh, aa29PinLow := aa29localworker.AA25LevelHigh, aa29localworker.AA25LevelLow
		if aa29Inverted {
			aa29PinHigh, aa29PinLow = aa29PinLow, aa29PinHigh
		}
		for _, aa29Step := range []struct {
			aa29TX       aa29localworker.AA25Level
			aa29Register uint32
			aa29Want     aa29localworker.AA25Level
		}{
			{aa29localworker.AA25LevelLow, aa29TestRXMask, aa29PinHigh},
			{aa29localworker.AA25LevelHigh, 0, aa29PinLow},
			{aa29localworker.AA25LevelLow, ^aa29TestRXMask, aa29PinLow},
			{aa29localworker.AA25LevelHigh, aa29TestRXMask | aa29TestTXMask, aa29PinHigh},
		} {
			aa29Fix.aa29Backend.AA25WriteEdge(aa29Step.aa29TX)
			aa29Fix.aa29Window.aa29Registers[aa29TestRX] = aa29Step.aa29Register
			aa29Fix.aa29Window.aa29Registers[aa29TestSet] = ^aa29Step.aa29Register
			aa29Fix.aa29Window.aa29Registers[aa29TestClear] = ^aa29Step.aa29Register
			if aa29Got := aa29Fix.aa29Backend.AA25SampleLevel(); aa29Got != aa29Step.aa29Want {
				aa29T.Fatalf("aa29 inverted=%v TX=%v RX=%#x sample = %v, want %v", aa29Inverted, aa29Step.aa29TX, aa29Step.aa29Register, aa29Got, aa29Step.aa29Want)
			}
		}
		for _, aa29Offset := range aa29Fix.aa29Window.aa29Loads {
			if aa29Offset != aa29TestRX {
				aa29T.Fatalf("aa29 sample loaded offset %#x", aa29Offset)
			}
		}
	}
}

// Catches: a close that leaves the line driven, does not unmap, unmaps twice,
// keeps claiming capabilities or keeps touching the window or counter.
func TestAA29CloseDisarmsUnmapsAndStaysClosed(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	aa29Fix := aa29OpenFixture(aa29T, aa29Profile)
	aa29Backend := aa29Fix.aa29Backend
	if aa29Err := aa29Backend.aa29Arm(aa29TestLease(aa29Profile)); aa29Err != nil {
		aa29T.Fatalf("aa29 arm: %v", aa29Err)
	}
	aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
	if aa29Err := aa29Backend.AA29Close(); aa29Err != nil {
		aa29T.Fatalf("aa29 close: %v", aa29Err)
	}
	aa29Want := []aa29FakeStore{{aa29TestClear, aa29TestTXMask}, {aa29TestSet, aa29TestTXMask}}
	if len(aa29Fix.aa29Window.aa29Stores) != 2 || aa29Fix.aa29Window.aa29Stores[1] != aa29Want[1] || aa29Fix.aa29Window.aa29Unmaps != 1 {
		aa29T.Fatalf("aa29 close stores=%v unmaps=%d", aa29Fix.aa29Window.aa29Stores, aa29Fix.aa29Window.aa29Unmaps)
	}
	aa29Loads, aa29Reads := len(aa29Fix.aa29Window.aa29Loads), aa29Fix.aa29Counter.aa29Reads
	if aa29Backend.AA25SampleLevel() != aa29localworker.AA25LevelHigh || aa29Backend.AA25NowTicks() != 0 {
		aa29T.Fatal("aa29 closed ports report activity")
	}
	aa29Backend.AA25WriteEdge(aa29localworker.AA25LevelLow)
	if len(aa29Fix.aa29Window.aa29Loads) != aa29Loads || aa29Fix.aa29Counter.aa29Reads != aa29Reads || len(aa29Fix.aa29Window.aa29Stores) != 2 {
		aa29T.Fatal("aa29 closed backend touched the window or counter")
	}
	if aa29Caps := AA22QueryCapabilities(aa29Backend); aa29Caps != (AA22Capabilities{}) {
		aa29T.Fatalf("aa29 closed capabilities = %+v", aa29Caps)
	}
	if aa29Err := aa29Backend.aa29Arm(aa29TestLease(aa29Profile)); !aa29errors.Is(aa29Err, AA29ErrClosed) {
		aa29T.Fatalf("aa29 arm after close = %v", aa29Err)
	}
	if _, aa29Err := aa29Backend.AA29NewEngine(aa29TestEngineConfig(), &aa29FakeSink{}); !aa29errors.Is(aa29Err, AA29ErrClosed) {
		aa29T.Fatalf("aa29 engine after close = %v", aa29Err)
	}
}

func aa29TestEngineConfig() aa29localworker.AA25Config {
	return aa29localworker.AA25Config{
		AA25TickHz:               aa29TestHz,
		AA25ResponderAddress:     aa29TestResponder,
		AA25TXLateToleranceTicks: 10,
		AA25EchoAllowanceTicks:   100,
	}
}

// Catches: binding an engine whose tick rate differs from the admitted counter,
// dropping a required port, or an engine binding that transmits without an
// offered prepared response even while a lease is live.
func TestAA29NewEngineBindsBackendPortsWithoutTransmitting(aa29T *aa29testing.T) {
	aa29Profile := aa29TestProfile()
	aa29Fix := aa29OpenFixture(aa29T, aa29Profile)
	aa29Mismatch := aa29TestEngineConfig()
	aa29Mismatch.AA25TickHz = aa29TestHz * 2
	if _, aa29Err := aa29Fix.aa29Backend.AA29NewEngine(aa29Mismatch, &aa29FakeSink{}); !aa29errors.Is(aa29Err, AA29ErrResourceMatch) {
		aa29T.Fatalf("aa29 engine rate mismatch = %v", aa29Err)
	}
	if _, aa29Err := aa29Fix.aa29Backend.AA29NewEngine(aa29TestEngineConfig(), nil); !aa29errors.Is(aa29Err, aa29localworker.AA25ErrMissingPort) {
		aa29T.Fatalf("aa29 engine without sink = %v", aa29Err)
	}
	for _, aa29Armed := range []bool{false, true} {
		if aa29Armed {
			aa29Lease := aa29TestLease(aa29Profile)
			aa29Lease.AA29NotAfterTick = aa29Fix.aa29Counter.aa29Now + aa29TestMaxLease
			if aa29Err := aa29Fix.aa29Backend.aa29Arm(aa29Lease); aa29Err != nil {
				aa29T.Fatalf("aa29 arm: %v", aa29Err)
			}
		}
		aa29Sink := &aa29FakeSink{}
		aa29Engine, aa29Err := aa29Fix.aa29Backend.AA29NewEngine(aa29TestEngineConfig(), aa29Sink)
		if aa29Err != nil {
			aa29T.Fatalf("aa29 engine: %v", aa29Err)
		}
		aa29LoadsBefore := len(aa29Fix.aa29Window.aa29Loads)
		for aa29Index := 0; aa29Index < 200; aa29Index++ {
			aa29Fix.aa29Counter.aa29Now = aa29Engine.AA25NextDeadlineTick()
			aa29Engine.AA25Step()
		}
		if len(aa29Fix.aa29Window.aa29Loads) <= aa29LoadsBefore {
			aa29T.Fatal("aa29 engine never sampled the window")
		}
		if len(aa29Fix.aa29Window.aa29Stores) != 0 || aa29Engine.AA25Stats().AA25TXEdges != 0 {
			aa29T.Fatalf("aa29 armed=%v engine without a prepared response stored %v", aa29Armed, aa29Fix.aa29Window.aa29Stores)
		}
	}
}

func aa29ParseSource(aa29T *aa29testing.T, aa29Path string) *aa29ast.File {
	aa29T.Helper()
	aa29File, aa29Err := aa29parser.ParseFile(aa29token.NewFileSet(), aa29Path, nil, aa29parser.SkipObjectResolution)
	if aa29Err != nil {
		aa29T.Fatalf("aa29 parse %s: %v", aa29Path, aa29Err)
	}
	return aa29File
}

func aa29CheckPrefix(aa29T *aa29testing.T, aa29Name string) {
	aa29T.Helper()
	if aa29Name == "_" || aa29strings.HasPrefix(aa29Name, "aa29") || aa29strings.HasPrefix(aa29Name, "AA29") || aa29strings.HasPrefix(aa29Name, "TestAA29") {
		return
	}
	aa29T.Fatalf("aa29 identifier %q lacks the AA29 prefix", aa29Name)
}

// Method names that satisfy the AA22 and AA25 port interfaces carry those
// tickets' prefixes by contract and are the only exemptions.
func TestAA29EveryDeclaredIdentifierIsPrefixed(aa29T *aa29testing.T) {
	aa29Ported := map[string]bool{
		"AA22TransportCapabilities": true,
		"AA25SampleLevel":           true,
		"AA25NowTicks":              true,
		"AA25WriteEdge":             true,
		"AA25TryPublish":            true,
	}
	for _, aa29Path := range []string{aa29TestProductionFile, aa29TestTestFile} {
		aa29File := aa29ParseSource(aa29T, aa29Path)
		for _, aa29Import := range aa29File.Imports {
			if aa29Import.Name == nil {
				aa29T.Fatalf("aa29 import %s has no prefixed alias", aa29Import.Path.Value)
			}
			aa29CheckPrefix(aa29T, aa29Import.Name.Name)
		}
		aa29ast.Inspect(aa29File, func(aa29Node aa29ast.Node) bool {
			switch aa29Value := aa29Node.(type) {
			case *aa29ast.FuncDecl:
				if !aa29Ported[aa29Value.Name.Name] {
					aa29CheckPrefix(aa29T, aa29Value.Name.Name)
				}
			case *aa29ast.TypeSpec:
				aa29CheckPrefix(aa29T, aa29Value.Name.Name)
			case *aa29ast.ValueSpec:
				for _, aa29Name := range aa29Value.Names {
					aa29CheckPrefix(aa29T, aa29Name.Name)
				}
			case *aa29ast.Field:
				for _, aa29Name := range aa29Value.Names {
					aa29CheckPrefix(aa29T, aa29Name.Name)
				}
			case *aa29ast.AssignStmt:
				if aa29Value.Tok == aa29token.DEFINE {
					for _, aa29Left := range aa29Value.Lhs {
						if aa29Ident, aa29OK := aa29Left.(*aa29ast.Ident); aa29OK {
							aa29CheckPrefix(aa29T, aa29Ident.Name)
						}
					}
				}
			case *aa29ast.RangeStmt:
				if aa29Value.Tok == aa29token.DEFINE {
					for _, aa29Left := range []aa29ast.Expr{aa29Value.Key, aa29Value.Value} {
						if aa29Ident, aa29OK := aa29Left.(*aa29ast.Ident); aa29OK {
							aa29CheckPrefix(aa29T, aa29Ident.Name)
						}
					}
				}
			}
			return true
		})
	}
}
