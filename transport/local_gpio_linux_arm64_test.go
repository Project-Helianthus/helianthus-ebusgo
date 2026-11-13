//go:build linux && arm64 && aa29_local_gpio

package transport

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/localworker"
)

// Every profile, window and counter below is a synthetic fake. No test maps
// memory, reads a hardware counter or opens a device. Offsets and masks are
// arbitrary synthetic values, not board registers.
const (
	localGPIOTestProductionFile = "local_gpio_linux_arm64.go"
	localGPIOTestTestFile       = "local_gpio_linux_arm64_test.go"
	localGPIOTestBuildLine      = "//go:build linux && arm64 && aa29_local_gpio"

	localGPIOTestHz        uint64 = 1_000_000
	localGPIOTestNow       uint64 = 10_000
	localGPIOTestMaxLease  uint64 = 5_000
	localGPIOTestLength    uint64 = 0x40
	localGPIOTestRX        uint64 = 0x10
	localGPIOTestSet       uint64 = 0x20
	localGPIOTestClear     uint64 = 0x30
	localGPIOTestRXMask    uint32 = 1 << 3
	localGPIOTestTXMask    uint32 = 1 << 5
	localGPIOTestLeaseID   uint64 = 7
	localGPIOTestResponder byte   = 0x15
)

type localGPIOFakeStore struct {
	offset uint64
	value  uint32
}

type localGPIOFakeWindow struct {
	length    uint64
	width     uint8
	ordering  LocalGPIOOrdering
	registers map[uint64]uint32
	loads     []uint64
	stores    []localGPIOFakeStore
	unmaps    int
	unmapErr  error
}

func (window *localGPIOFakeWindow) MappedLength() uint64              { return window.length }
func (window *localGPIOFakeWindow) MappedAccessWidth() uint8          { return window.width }
func (window *localGPIOFakeWindow) MappedOrdering() LocalGPIOOrdering { return window.ordering }

func (window *localGPIOFakeWindow) Load32(offset uint64) uint32 {
	window.loads = append(window.loads, offset)
	return window.registers[offset]
}

func (window *localGPIOFakeWindow) Store32(offset uint64, value uint32) {
	window.stores = append(window.stores, localGPIOFakeStore{offset, value})
}

func (window *localGPIOFakeWindow) Unmap() error {
	window.unmaps++
	return window.unmapErr
}

type localGPIOFakeMapper struct {
	window *localGPIOFakeWindow
	err    error
	calls  int
	seen   []LocalGPIODescriptor
}

func (mapper *localGPIOFakeMapper) Map(descriptor LocalGPIODescriptor) (LocalGPIOWindow, error) {
	mapper.calls++
	mapper.seen = append(mapper.seen, descriptor)
	if mapper.window == nil {
		return nil, mapper.err
	}
	return mapper.window, mapper.err
}

type localGPIOFakeCounter struct {
	hz    uint64
	now   uint64
	reads int
}

func (counter *localGPIOFakeCounter) CounterHz() uint64 { return counter.hz }

func (counter *localGPIOFakeCounter) ReadCounter() uint64 {
	counter.reads++
	return counter.now
}

type localGPIOFakeSink struct {
	events []localworker.Event
}

func (sink *localGPIOFakeSink) TryPublish(event localworker.Event) bool {
	sink.events = append(sink.events, event)
	return true
}

func localGPIOTestProfile() LocalGPIOPlatformProfile {
	profile := LocalGPIOPlatformProfile{
		ProfileID:     "aa29-synthetic-profile",
		Family:        "aa29-synthetic-family",
		Qualification: AA22Proven,
		GPIO: LocalGPIODescriptor{
			Role:          LocalGPIORoleGPIOControl,
			MapLength:     localGPIOTestLength,
			AccessWidth:   4,
			Ordering:      LocalGPIOOrderingDevice,
			RXLevelOffset: localGPIOTestRX,
			TXSetOffset:   localGPIOTestSet,
			TXClearOffset: localGPIOTestClear,
			RXMask:        localGPIOTestRXMask,
			TXMask:        localGPIOTestTXMask,
		},
		Counter:       LocalGPIOCounterDescriptor{Role: LocalGPIORoleCounter, Hz: localGPIOTestHz},
		MaxLeaseTicks: localGPIOTestMaxLease,
	}
	return localGPIOSeal(profile)
}

func localGPIOSeal(profile LocalGPIOPlatformProfile) LocalGPIOPlatformProfile {
	profile.QualificationDigest = LocalGPIOProfileDigest(profile)
	return profile
}

func localGPIOTestWindow() *localGPIOFakeWindow {
	return &localGPIOFakeWindow{
		length:    localGPIOTestLength,
		width:     4,
		ordering:  LocalGPIOOrderingDevice,
		registers: map[uint64]uint32{localGPIOTestRX: localGPIOTestRXMask},
	}
}

type localGPIOFixture struct {
	window  *localGPIOFakeWindow
	mapper  *localGPIOFakeMapper
	counter *localGPIOFakeCounter
	backend *LocalGPIOBackend
}

func localGPIOOpenFixture(t *testing.T, profile LocalGPIOPlatformProfile) *localGPIOFixture {
	t.Helper()
	window := localGPIOTestWindow()
	fix := &localGPIOFixture{
		window:  window,
		mapper:  &localGPIOFakeMapper{window: window},
		counter: &localGPIOFakeCounter{hz: localGPIOTestHz, now: localGPIOTestNow},
	}
	backend, err := OpenLocalGPIO(&profile, fix.mapper, fix.counter)
	if err != nil {
		t.Fatalf("open of a valid profile: %v", err)
	}
	fix.backend = backend
	t.Cleanup(func() {
		if closeErr := backend.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
		if window.unmaps != 1 {
			t.Errorf("unmaps after close = %d, want 1", window.unmaps)
		}
	})
	return fix
}

func localGPIOTestLease(profile LocalGPIOPlatformProfile) LocalGPIOLease {
	return LocalGPIOLease{
		LeaseID:       localGPIOTestLeaseID,
		ProfileDigest: profile.QualificationDigest,
		NotAfterTick:  localGPIOTestNow + 1_000,
	}
}

// localGPIOExpectRefusedWithoutMapping opens a profile and requires the error and
// that no port was asked to map anything.
func localGPIOExpectRefusedWithoutMapping(t *testing.T, profile *LocalGPIOPlatformProfile, want error) {
	t.Helper()
	mapper := &localGPIOFakeMapper{window: localGPIOTestWindow()}
	counter := &localGPIOFakeCounter{hz: localGPIOTestHz, now: localGPIOTestNow}
	backend, err := OpenLocalGPIO(profile, mapper, counter)
	if !errors.Is(err, want) || backend != nil {
		t.Fatalf("open = (%v, %v), want (nil, %v)", backend, err, want)
	}
	if mapper.calls != 0 || counter.reads != 0 {
		t.Fatalf("refused open touched ports: maps=%d reads=%d", mapper.calls, counter.reads)
	}
}

// Catches: deleting or weakening the build line, which would put the backend in
// the default build or on another platform.
func TestAA29BuildConstraintIsNonDefault(t *testing.T) {
	for _, path := range []string{localGPIOTestProductionFile, localGPIOTestTestFile} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		first, _, _ := strings.Cut(string(source), "\n")
		if first != localGPIOTestBuildLine {
			t.Fatalf("%s build line = %q, want %q", path, first, localGPIOTestBuildLine)
		}
	}
}

// Catches: adding an OS, syscall or device-path dependency to the boundary,
// which would let it open or look up a resource itself.
func TestAA29ProductionOpensNoResourceItself(t *testing.T) {
	file := localGPIOParseSource(t, localGPIOTestProductionFile)
	allowed := map[string]bool{
		"crypto/sha256":   true,
		"encoding/binary": true,
		"errors":          true,
		"fmt":             true,
		"math/bits":       true,
		"reflect":         true,
		"sync/atomic":     true,
		"github.com/Project-Helianthus/helianthus-ebusgo/localworker": true,
	}
	for _, importSpec := range file.Imports {
		path, err := strconv.Unquote(importSpec.Path.Value)
		if err != nil || !allowed[path] {
			t.Fatalf("production import %s is outside the boundary", importSpec.Path.Value)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if literal, ok := node.(*ast.BasicLit); ok && strings.Contains(literal.Value, "/dev") {
			t.Fatalf("production literal %s names a device path", literal.Value)
		}
		return true
	})
}

// Positive control. Catches: writing TX at open, starting armed, claiming
// Proven arbitration or timing, or reporting a non-raw kind.
func TestAA29OpenAdmitsQualifiedProfileAndStartsPassive(t *testing.T) {
	profile := localGPIOTestProfile()
	fix := localGPIOOpenFixture(t, profile)
	if fix.mapper.calls != 1 || fix.mapper.seen[0] != profile.GPIO {
		t.Fatalf("mapping calls = %d %+v, want the profile descriptor once", fix.mapper.calls, fix.mapper.seen)
	}
	if len(fix.window.stores) != 0 || len(fix.window.loads) != 0 {
		t.Fatalf("open accessed the window: stores=%v loads=%v", fix.window.stores, fix.window.loads)
	}
	if fix.backend.armed {
		t.Fatal("backend starts armed")
	}
	want := AA22Capabilities{
		AA22Kind:                       AA22KindLocalRaw,
		AA22Encoding:                   AA22EncodingRaw,
		AA22PhysicalArbitration:        AA22Unknown,
		AA22AutonomousPreparedResponse: AA22Hypothesis,
		AA22ObservedEcho:               AA22Hypothesis,
		AA22CaptureTimestamps:          AA22Hypothesis,
		AA22QualifiedTiming:            AA22Unknown,
	}
	got := AA22QueryCapabilities(fix.backend)
	if got != want {
		t.Fatalf("capabilities = %+v, want %+v", got, want)
	}
	if got.AA22DeadlineSuitable() {
		t.Fatal("backend claims deadline suitability")
	}
}

// Catches: treating an absent profile as a default profile.
func TestAA29OpenRejectsMissingProfileWithoutMapping(t *testing.T) {
	localGPIOExpectRefusedWithoutMapping(t, nil, ErrLocalGPIOProfileMissing)
}

// Catches: accepting Unknown, Hypothesis or an out-of-range evidence value as
// qualification, even with an internally consistent digest.
func TestAA29OpenRejectsUnqualifiedProfileWithoutMapping(t *testing.T) {
	for _, evidence := range []AA22Evidence{AA22Unknown, AA22Hypothesis, AA22Proven + 1} {
		profile := localGPIOTestProfile()
		profile.Qualification = evidence
		profile = localGPIOSeal(profile)
		localGPIOExpectRefusedWithoutMapping(t, &profile, ErrLocalGPIOUnqualified)
	}
	unsealed := localGPIOTestProfile()
	unsealed.QualificationDigest = [sha256.Size]byte{}
	localGPIOExpectRefusedWithoutMapping(t, &unsealed, ErrLocalGPIOUnqualified)
}

// localGPIOIndependentDigest restates the documented field order without calling
// production encoding helpers.
func localGPIOIndependentDigest(profile LocalGPIOPlatformProfile) [sha256.Size]byte {
	var buffer []byte
	buffer = append(buffer, []byte("aa29.profile.1\x00")...)
	for _, label := range []string{profile.ProfileID, profile.Family} {
		buffer = append(buffer, byte(len(label)>>8), byte(len(label)))
		buffer = append(buffer, label...)
	}
	g := profile.GPIO
	buffer = append(buffer, byte(profile.Qualification), byte(g.Role))
	word := make([]byte, 8)
	put := func(value uint64) {
		binary.BigEndian.PutUint64(word, value)
		buffer = append(buffer, word...)
	}
	put(g.MapLength)
	buffer = append(buffer, g.AccessWidth, byte(g.Ordering))
	put(g.RXLevelOffset)
	put(g.TXSetOffset)
	put(g.TXClearOffset)
	buffer = append(buffer, byte(g.RXMask>>24), byte(g.RXMask>>16), byte(g.RXMask>>8), byte(g.RXMask))
	buffer = append(buffer, byte(g.TXMask>>24), byte(g.TXMask>>16), byte(g.TXMask>>8), byte(g.TXMask))
	flag := func(value bool) byte {
		if value {
			return 1
		}
		return 0
	}
	buffer = append(buffer, flag(g.RXInverted), flag(g.TXInverted), byte(profile.Counter.Role))
	put(profile.Counter.Hz)
	put(profile.MaxLeaseTicks)
	return sha256.Sum256(buffer)
}

// Catches: reordering, dropping or re-encoding a digest field, and folding the
// stored digest into its own input.
func TestAA29ProfileDigestMatchesIndependentEncoding(t *testing.T) {
	profile := localGPIOTestProfile()
	profile.GPIO.RXInverted = true
	if got, want := LocalGPIOProfileDigest(profile), localGPIOIndependentDigest(profile); got != want {
		t.Fatalf("digest = %x, want %x", got, want)
	}
	other := profile
	other.QualificationDigest[0] ^= 0xFF
	if LocalGPIOProfileDigest(other) != LocalGPIOProfileDigest(profile) {
		t.Fatal("digest covers the stored digest")
	}
}

// Every profile field is varied alone after sealing. Catches, per field:
// omitting that field from LocalGPIOProfileDigest (the digest would not change), or
// skipping the digest comparison in admission (the open would succeed).
func TestAA29QualificationDigestCoversEveryProfileField(t *testing.T) {
	cases := []struct {
		name   string
		valid  bool
		mutate func(*LocalGPIOPlatformProfile)
	}{
		{"ProfileID", true, func(p *LocalGPIOPlatformProfile) { p.ProfileID += "x" }},
		{"Family", true, func(p *LocalGPIOPlatformProfile) { p.Family += "x" }},
		{"Qualification", false, func(p *LocalGPIOPlatformProfile) { p.Qualification = AA22Hypothesis }},
		{"GPIO.Role", false, func(p *LocalGPIOPlatformProfile) { p.GPIO.Role = LocalGPIORoleCounter }},
		{"GPIO.MapLength", true, func(p *LocalGPIOPlatformProfile) { p.GPIO.MapLength += 4 }},
		{"GPIO.AccessWidth", false, func(p *LocalGPIOPlatformProfile) { p.GPIO.AccessWidth = 8 }},
		{"GPIO.Ordering", false, func(p *LocalGPIOPlatformProfile) { p.GPIO.Ordering = LocalGPIOOrderingRelaxed }},
		{"GPIO.RXLevelOffset", true, func(p *LocalGPIOPlatformProfile) { p.GPIO.RXLevelOffset += 4 }},
		{"GPIO.TXSetOffset", true, func(p *LocalGPIOPlatformProfile) { p.GPIO.TXSetOffset += 4 }},
		{"GPIO.TXClearOffset", true, func(p *LocalGPIOPlatformProfile) { p.GPIO.TXClearOffset += 4 }},
		{"GPIO.RXMask", true, func(p *LocalGPIOPlatformProfile) { p.GPIO.RXMask <<= 1 }},
		{"GPIO.TXMask", true, func(p *LocalGPIOPlatformProfile) { p.GPIO.TXMask <<= 1 }},
		{"GPIO.RXInverted", true, func(p *LocalGPIOPlatformProfile) { p.GPIO.RXInverted = true }},
		{"GPIO.TXInverted", true, func(p *LocalGPIOPlatformProfile) { p.GPIO.TXInverted = true }},
		{"Counter.Role", false, func(p *LocalGPIOPlatformProfile) { p.Counter.Role = LocalGPIORoleGPIOControl }},
		{"Counter.Hz", true, func(p *LocalGPIOPlatformProfile) { p.Counter.Hz++ }},
		{"MaxLeaseTicks", true, func(p *LocalGPIOPlatformProfile) { p.MaxLeaseTicks++ }},
	}
	sealed := localGPIOTestProfile()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := sealed
			tc.mutate(&mutated)
			if LocalGPIOProfileDigest(mutated) == sealed.QualificationDigest {
				t.Fatal("digest ignores the field")
			}
			localGPIOExpectRefusedWithoutMapping(t, &mutated, ErrLocalGPIOUnqualified)
			if !tc.valid {
				return
			}
			resealed := localGPIOSeal(mutated)
			counter := &localGPIOFakeCounter{hz: resealed.Counter.Hz}
			window := localGPIOTestWindow()
			window.length = resealed.GPIO.MapLength
			backend, err := OpenLocalGPIO(&resealed, &localGPIOFakeMapper{window: window}, counter)
			if err != nil {
				t.Fatalf("resealed control refused: %v", err)
			}
			if closeErr := backend.Close(); closeErr != nil {
				t.Fatalf("close: %v", closeErr)
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
func TestAA29OpenRejectsInvalidDescriptorsWithoutMapping(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*LocalGPIOPlatformProfile)
	}{
		{"absent GPIO descriptor", func(p *LocalGPIOPlatformProfile) { p.GPIO = LocalGPIODescriptor{} }},
		{"absent counter descriptor", func(p *LocalGPIOPlatformProfile) { p.Counter = LocalGPIOCounterDescriptor{} }},
		{"GPIO role counter", func(p *LocalGPIOPlatformProfile) { p.GPIO.Role = LocalGPIORoleCounter }},
		{"counter role GPIO", func(p *LocalGPIOPlatformProfile) { p.Counter.Role = LocalGPIORoleGPIOControl }},
		{"width 1", func(p *LocalGPIOPlatformProfile) { p.GPIO.AccessWidth = 1 }},
		{"width 2", func(p *LocalGPIOPlatformProfile) { p.GPIO.AccessWidth = 2 }},
		{"width 8", func(p *LocalGPIOPlatformProfile) { p.GPIO.AccessWidth = 8 }},
		{"ordering unknown", func(p *LocalGPIOPlatformProfile) { p.GPIO.Ordering = LocalGPIOOrderingUnknown }},
		{"ordering relaxed", func(p *LocalGPIOPlatformProfile) { p.GPIO.Ordering = LocalGPIOOrderingRelaxed }},
		{"length zero", func(p *LocalGPIOPlatformProfile) { p.GPIO.MapLength = 0 }},
		{"length above bound", func(p *LocalGPIOPlatformProfile) { p.GPIO.MapLength = 1<<16 + 4 }},
		{"length unaligned", func(p *LocalGPIOPlatformProfile) { p.GPIO.MapLength = localGPIOTestLength + 2 }},
		{"RX outside", func(p *LocalGPIOPlatformProfile) { p.GPIO.RXLevelOffset = localGPIOTestLength }},
		{"set outside", func(p *LocalGPIOPlatformProfile) { p.GPIO.TXSetOffset = localGPIOTestLength }},
		{"clear outside", func(p *LocalGPIOPlatformProfile) { p.GPIO.TXClearOffset = localGPIOTestLength }},
		{"RX wraps", func(p *LocalGPIOPlatformProfile) { p.GPIO.RXLevelOffset = ^uint64(0) - 3 }},
		{"RX unaligned", func(p *LocalGPIOPlatformProfile) { p.GPIO.RXLevelOffset = localGPIOTestRX + 1 }},
		{"set unaligned", func(p *LocalGPIOPlatformProfile) { p.GPIO.TXSetOffset = localGPIOTestSet + 2 }},
		{"clear unaligned", func(p *LocalGPIOPlatformProfile) { p.GPIO.TXClearOffset = localGPIOTestClear + 3 }},
		{"set aliases clear", func(p *LocalGPIOPlatformProfile) { p.GPIO.TXClearOffset = localGPIOTestSet }},
		{"RX aliases set", func(p *LocalGPIOPlatformProfile) { p.GPIO.RXLevelOffset = localGPIOTestSet }},
		{"RX aliases clear", func(p *LocalGPIOPlatformProfile) { p.GPIO.RXLevelOffset = localGPIOTestClear }},
		{"RX mask empty", func(p *LocalGPIOPlatformProfile) { p.GPIO.RXMask = 0 }},
		{"RX mask two pins", func(p *LocalGPIOPlatformProfile) { p.GPIO.RXMask = 0b11 }},
		{"TX mask empty", func(p *LocalGPIOPlatformProfile) { p.GPIO.TXMask = 0 }},
		{"TX mask two pins", func(p *LocalGPIOPlatformProfile) { p.GPIO.TXMask = 0b101 }},
		{"counter below sample rate", func(p *LocalGPIOPlatformProfile) {
			p.Counter.Hz = localworker.NominalSampleRate - 1
		}},
		{"lease unbounded", func(p *LocalGPIOPlatformProfile) { p.MaxLeaseTicks = 0 }},
		{"profile ID empty", func(p *LocalGPIOPlatformProfile) { p.ProfileID = "" }},
		{"profile ID long", func(p *LocalGPIOPlatformProfile) { p.ProfileID = strings.Repeat("p", 65) }},
		{"profile ID control byte", func(p *LocalGPIOPlatformProfile) { p.ProfileID = "aa29\n" }},
		{"family empty", func(p *LocalGPIOPlatformProfile) { p.Family = "" }},
		{"family non-ASCII", func(p *LocalGPIOPlatformProfile) { p.Family = "aa29\x7F" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := localGPIOTestProfile()
			tc.mutate(&profile)
			profile = localGPIOSeal(profile)
			localGPIOExpectRefusedWithoutMapping(t, &profile, ErrLocalGPIODescriptor)
		})
	}
}

// Positive boundary control: registers in the last word, a window at the bound
// and 64-character labels are admitted, so the refusals above are not caused by
// an over-tight bound.
func TestAA29OpenAdmitsDescriptorBoundaries(t *testing.T) {
	profile := localGPIOTestProfile()
	profile.GPIO.MapLength = 1 << 16
	profile.GPIO.RXLevelOffset = 1<<16 - 4
	profile.GPIO.TXSetOffset = 0
	profile.GPIO.TXClearOffset = 1<<16 - 8
	profile.GPIO.RXMask = 1 << 31
	profile.GPIO.TXMask = 1
	profile.Counter.Hz = localworker.NominalSampleRate
	profile.ProfileID = strings.Repeat("p", 64)
	profile.Family = strings.Repeat("~", 64)
	profile = localGPIOSeal(profile)
	window := localGPIOTestWindow()
	window.length = 1 << 16
	backend, err := OpenLocalGPIO(&profile, &localGPIOFakeMapper{window: window},
		&localGPIOFakeCounter{hz: localworker.NominalSampleRate})
	if err != nil {
		t.Fatalf("boundary profile refused: %v", err)
	}
	if closeErr := backend.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
}

type localGPIONilMapper struct{}

// Map returns a typed nil window with no error.
func (*localGPIONilMapper) Map(LocalGPIODescriptor) (LocalGPIOWindow, error) {
	var window *localGPIOFakeWindow
	return window, nil
}

// Catches: mapping without a counter or mapping port, proceeding with a counter
// whose rate differs from the profile, ignoring a mapping error, or accepting a
// nil window.
func TestAA29OpenRejectsAbsentControlResources(t *testing.T) {
	profile := localGPIOTestProfile()
	var typedNilMapper *localGPIOFakeMapper
	var typedNilCounter *localGPIOFakeCounter
	mapper := &localGPIOFakeMapper{window: localGPIOTestWindow()}
	counter := &localGPIOFakeCounter{hz: localGPIOTestHz}
	for _, tc := range []struct {
		name    string
		mapping LocalGPIOMappingPort
		counter LocalGPIOCounterPort
	}{
		{"nil mapping", nil, counter},
		{"typed nil mapping", typedNilMapper, counter},
		{"nil counter", mapper, nil},
		{"typed nil counter", mapper, typedNilCounter},
	} {
		if backend, err := OpenLocalGPIO(&profile, tc.mapping, tc.counter); !errors.Is(err, ErrLocalGPIOResourceMissing) || backend != nil {
			t.Fatalf("%s open = (%v, %v)", tc.name, backend, err)
		}
	}
	if mapper.calls != 0 {
		t.Fatalf("mapped %d times without both resources", mapper.calls)
	}

	mismatch := &localGPIOFakeCounter{hz: localGPIOTestHz + 1}
	if _, err := OpenLocalGPIO(&profile, mapper, mismatch); !errors.Is(err, ErrLocalGPIOResourceMatch) || mapper.calls != 0 || mismatch.reads != 0 {
		t.Fatalf("counter rate mismatch = %v, maps=%d reads=%d", err, mapper.calls, mismatch.reads)
	}

	cause := errors.New("synthetic refusal")
	failing := &localGPIOFakeMapper{window: localGPIOTestWindow(), err: cause}
	if backend, err := OpenLocalGPIO(&profile, failing, counter); !errors.Is(err, ErrLocalGPIOMapping) || !errors.Is(err, cause) || backend != nil {
		t.Fatalf("mapping error = (%v, %v)", backend, err)
	}
	if failing.window.unmaps != 1 {
		t.Fatalf("window returned with an error was unmapped %d times, want 1", failing.window.unmaps)
	}

	if backend, err := OpenLocalGPIO(&profile, &localGPIONilMapper{}, counter); !errors.Is(err, ErrLocalGPIOResourceMissing) || backend != nil {
		t.Fatalf("typed nil window = (%v, %v)", backend, err)
	}
	if backend, err := OpenLocalGPIO(&profile, &localGPIOFakeMapper{}, counter); !errors.Is(err, ErrLocalGPIOResourceMissing) || backend != nil {
		t.Fatalf("nil window = (%v, %v)", backend, err)
	}
}

// Each window precondition is varied alone. Catches: ignoring the mapped size,
// the access width or the ordering the mapping actually provides, or leaking
// the refused mapping.
func TestAA29OpenRejectsWindowPreconditionsAndUnmaps(t *testing.T) {
	profile := localGPIOTestProfile()
	for _, tc := range []struct {
		name   string
		mutate func(*localGPIOFakeWindow)
	}{
		{"length short", func(w *localGPIOFakeWindow) { w.length = localGPIOTestLength - 4 }},
		{"length long", func(w *localGPIOFakeWindow) { w.length = localGPIOTestLength + 4 }},
		{"width 2", func(w *localGPIOFakeWindow) { w.width = 2 }},
		{"width 8", func(w *localGPIOFakeWindow) { w.width = 8 }},
		{"ordering relaxed", func(w *localGPIOFakeWindow) { w.ordering = LocalGPIOOrderingRelaxed }},
		{"ordering unknown", func(w *localGPIOFakeWindow) { w.ordering = LocalGPIOOrderingUnknown }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window := localGPIOTestWindow()
			tc.mutate(window)
			backend, err := OpenLocalGPIO(&profile, &localGPIOFakeMapper{window: window}, &localGPIOFakeCounter{hz: localGPIOTestHz})
			if !errors.Is(err, ErrLocalGPIOResourceMatch) || backend != nil {
				t.Fatalf("open = (%v, %v), want resource mismatch", backend, err)
			}
			if window.unmaps != 1 || len(window.stores) != 0 || len(window.loads) != 0 {
				t.Fatalf("refused window: unmaps=%d stores=%v loads=%v", window.unmaps, window.stores, window.loads)
			}
		})
	}
}

// Catches: a backend that stores an edge without an admitted lease, i.e. a
// default transmit path.
func TestAA29DefaultTransmitIsRefused(t *testing.T) {
	fix := localGPIOOpenFixture(t, localGPIOTestProfile())
	fix.backend.WriteEdge(localworker.LevelLow)
	fix.backend.WriteEdge(localworker.LevelHigh)
	if len(fix.window.stores) != 0 {
		t.Fatalf("disarmed backend stored %v", fix.window.stores)
	}
	if stats := fix.backend.Stats(); stats.RefusedEdges != 2 || stats.StoredEdges != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

// The lease identity is (LeaseID, LocalGPIOProfileDigest, NotAfterTick);
// each is varied alone. Catches: admitting a zero lease ID, a lease for another
// profile digest, an expired or already-due lease, a lease beyond
// MaxLeaseTicks, or a second lease over a live one.
func TestAA29ArmRequiresLeaseBoundToProfile(t *testing.T) {
	profile := localGPIOTestProfile()
	for _, tc := range []struct {
		name   string
		mutate func(*LocalGPIOLease)
		admit  bool
	}{
		{"valid", func(*LocalGPIOLease) {}, true},
		{"ID zero", func(l *LocalGPIOLease) { l.LeaseID = 0 }, false},
		{"ID other", func(l *LocalGPIOLease) { l.LeaseID = localGPIOTestLeaseID + 1 }, true},
		{"digest first byte", func(l *LocalGPIOLease) { l.ProfileDigest[0] ^= 1 }, false},
		{"digest last byte", func(l *LocalGPIOLease) { l.ProfileDigest[sha256.Size-1] ^= 0x80 }, false},
		{"digest zero", func(l *LocalGPIOLease) { l.ProfileDigest = [sha256.Size]byte{} }, false},
		{"expiry now", func(l *LocalGPIOLease) { l.NotAfterTick = localGPIOTestNow }, false},
		{"expiry past", func(l *LocalGPIOLease) { l.NotAfterTick = localGPIOTestNow - 1 }, false},
		{"expiry next tick", func(l *LocalGPIOLease) { l.NotAfterTick = localGPIOTestNow + 1 }, true},
		{"expiry at bound", func(l *LocalGPIOLease) { l.NotAfterTick = localGPIOTestNow + localGPIOTestMaxLease }, true},
		{"expiry beyond bound", func(l *LocalGPIOLease) { l.NotAfterTick = localGPIOTestNow + localGPIOTestMaxLease + 1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fix := localGPIOOpenFixture(t, profile)
			lease := localGPIOTestLease(profile)
			tc.mutate(&lease)
			err := fix.backend.arm(lease)
			if tc.admit != (err == nil) || fix.backend.armed != tc.admit {
				t.Fatalf("arm = %v armed=%v, want admit %v", err, fix.backend.armed, tc.admit)
			}
			if !tc.admit {
				if !errors.Is(err, ErrLocalGPIOLease) {
					t.Fatalf("arm error = %v, want lease refusal", err)
				}
				fix.backend.WriteEdge(localworker.LevelLow)
				if len(fix.window.stores) != 0 {
					t.Fatalf("refused lease still stored %v", fix.window.stores)
				}
				return
			}
			if second := fix.backend.arm(localGPIOTestLease(profile)); !errors.Is(second, ErrLocalGPIOLease) {
				t.Fatalf("second arm over a live lease = %v", second)
			}
		})
	}
}

// Catches: storing to the wrong register, the wrong mask, ignoring TX inversion
// or writing a level instead of a set/clear pin store.
func TestAA29ArmedEdgesUseDescriptorRegistersAndPolarity(t *testing.T) {
	for _, inverted := range []bool{false, true} {
		profile := localGPIOTestProfile()
		profile.GPIO.TXInverted = inverted
		profile = localGPIOSeal(profile)
		fix := localGPIOOpenFixture(t, profile)
		if err := fix.backend.arm(localGPIOTestLease(profile)); err != nil {
			t.Fatalf("arm: %v", err)
		}
		fix.backend.WriteEdge(localworker.LevelLow)
		fix.backend.WriteEdge(localworker.LevelHigh)
		lowOffset, highOffset := localGPIOTestClear, localGPIOTestSet
		if inverted {
			lowOffset, highOffset = localGPIOTestSet, localGPIOTestClear
		}
		want := []localGPIOFakeStore{{lowOffset, localGPIOTestTXMask}, {highOffset, localGPIOTestTXMask}}
		if len(fix.window.stores) != 2 || fix.window.stores[0] != want[0] || fix.window.stores[1] != want[1] {
			t.Fatalf("inverted=%v stores = %v, want %v", inverted, fix.window.stores, want)
		}
		if stats := fix.backend.Stats(); stats.StoredEdges != 2 || stats.RefusedEdges != 0 {
			t.Fatalf("stats = %+v", stats)
		}
	}
}

// Catches: ignoring lease expiry on the edge path, leaving a driven line low on
// expiry, or staying armed after expiry.
func TestAA29LeaseExpiryDisarmsAndReleases(t *testing.T) {
	profile := localGPIOTestProfile()
	fix := localGPIOOpenFixture(t, profile)
	lease := localGPIOTestLease(profile)
	if err := fix.backend.arm(lease); err != nil {
		t.Fatalf("arm: %v", err)
	}
	fix.counter.now = lease.NotAfterTick - 1
	fix.backend.WriteEdge(localworker.LevelLow)
	fix.counter.now = lease.NotAfterTick
	fix.backend.WriteEdge(localworker.LevelLow)
	fix.backend.WriteEdge(localworker.LevelLow)
	want := []localGPIOFakeStore{{localGPIOTestClear, localGPIOTestTXMask}, {localGPIOTestSet, localGPIOTestTXMask}}
	if len(fix.window.stores) != 2 || fix.window.stores[0] != want[0] || fix.window.stores[1] != want[1] {
		t.Fatalf("stores = %v, want drive then release %v", fix.window.stores, want)
	}
	stats := fix.backend.Stats()
	if fix.backend.armed || stats.LeaseExpiries != 1 || stats.Releases != 1 || stats.RefusedEdges != 2 || stats.StoredEdges != 1 {
		t.Fatalf("after expiry armed=%v stats=%+v", fix.backend.armed, stats)
	}
}

// Catches: revoking for a zero or foreign lease ID, or revoking without
// releasing a driven line.
func TestAA29RevokeDisarmsOnlyItsLeaseAndReleases(t *testing.T) {
	profile := localGPIOTestProfile()
	fix := localGPIOOpenFixture(t, profile)
	if err := fix.backend.arm(localGPIOTestLease(profile)); err != nil {
		t.Fatalf("arm: %v", err)
	}
	fix.backend.WriteEdge(localworker.LevelLow)
	if fix.backend.revoke(0) || fix.backend.revoke(localGPIOTestLeaseID+1) || !fix.backend.armed {
		t.Fatal("foreign revoke disarmed the backend")
	}
	if !fix.backend.revoke(localGPIOTestLeaseID) || fix.backend.armed {
		t.Fatal("matching revoke did not disarm")
	}
	last := fix.window.stores[len(fix.window.stores)-1]
	if len(fix.window.stores) != 2 || last != (localGPIOFakeStore{localGPIOTestSet, localGPIOTestTXMask}) {
		t.Fatalf("stores after revoke = %v", fix.window.stores)
	}
	fix.backend.WriteEdge(localworker.LevelLow)
	if len(fix.window.stores) != 2 {
		t.Fatal("revoked backend stored an edge")
	}
}

// The RX register is independent of TX here, as on a line another device can
// hold. Catches: synthesizing the echo from the last TX level, reading the
// wrong register, ignoring the RX mask or ignoring RX inversion.
func TestAA29SampleLevelReadsWindowNeverSynthesizesEcho(t *testing.T) {
	for _, inverted := range []bool{false, true} {
		profile := localGPIOTestProfile()
		profile.GPIO.RXInverted = inverted
		profile = localGPIOSeal(profile)
		fix := localGPIOOpenFixture(t, profile)
		if err := fix.backend.arm(localGPIOTestLease(profile)); err != nil {
			t.Fatalf("arm: %v", err)
		}
		pinHigh, pinLow := localworker.LevelHigh, localworker.LevelLow
		if inverted {
			pinHigh, pinLow = pinLow, pinHigh
		}
		for _, step := range []struct {
			tx       localworker.Level
			register uint32
			want     localworker.Level
		}{
			{localworker.LevelLow, localGPIOTestRXMask, pinHigh},
			{localworker.LevelHigh, 0, pinLow},
			{localworker.LevelLow, ^localGPIOTestRXMask, pinLow},
			{localworker.LevelHigh, localGPIOTestRXMask | localGPIOTestTXMask, pinHigh},
		} {
			fix.backend.WriteEdge(step.tx)
			fix.window.registers[localGPIOTestRX] = step.register
			fix.window.registers[localGPIOTestSet] = ^step.register
			fix.window.registers[localGPIOTestClear] = ^step.register
			if got := fix.backend.SampleLevel(); got != step.want {
				t.Fatalf("inverted=%v TX=%v RX=%#x sample = %v, want %v", inverted, step.tx, step.register, got, step.want)
			}
		}
		for _, offset := range fix.window.loads {
			if offset != localGPIOTestRX {
				t.Fatalf("sample loaded offset %#x", offset)
			}
		}
	}
}

// Catches: a close that leaves the line driven, does not unmap, unmaps twice,
// keeps claiming capabilities or keeps touching the window or counter.
func TestAA29CloseDisarmsUnmapsAndStaysClosed(t *testing.T) {
	profile := localGPIOTestProfile()
	fix := localGPIOOpenFixture(t, profile)
	backend := fix.backend
	if err := backend.arm(localGPIOTestLease(profile)); err != nil {
		t.Fatalf("arm: %v", err)
	}
	backend.WriteEdge(localworker.LevelLow)
	if err := backend.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	want := []localGPIOFakeStore{{localGPIOTestClear, localGPIOTestTXMask}, {localGPIOTestSet, localGPIOTestTXMask}}
	if len(fix.window.stores) != 2 || fix.window.stores[1] != want[1] || fix.window.unmaps != 1 {
		t.Fatalf("close stores=%v unmaps=%d", fix.window.stores, fix.window.unmaps)
	}
	loads, reads := len(fix.window.loads), fix.counter.reads
	if backend.SampleLevel() != localworker.LevelHigh || backend.NowTicks() != 0 {
		t.Fatal("closed ports report activity")
	}
	backend.WriteEdge(localworker.LevelLow)
	if len(fix.window.loads) != loads || fix.counter.reads != reads || len(fix.window.stores) != 2 {
		t.Fatal("closed backend touched the window or counter")
	}
	if caps := AA22QueryCapabilities(backend); caps != (AA22Capabilities{}) {
		t.Fatalf("closed capabilities = %+v", caps)
	}
	if err := backend.arm(localGPIOTestLease(profile)); !errors.Is(err, ErrLocalGPIOClosed) {
		t.Fatalf("arm after close = %v", err)
	}
	if _, err := backend.NewEngine(localGPIOTestEngineConfig(), &localGPIOFakeSink{}); !errors.Is(err, ErrLocalGPIOClosed) {
		t.Fatalf("engine after close = %v", err)
	}
}

func localGPIOTestEngineConfig() localworker.EngineConfig {
	return localworker.EngineConfig{
		TickHz:               localGPIOTestHz,
		ResponderAddress:     localGPIOTestResponder,
		TXLateToleranceTicks: 10,
		EchoAllowanceTicks:   100,
	}
}

// Catches: binding an engine whose tick rate differs from the admitted counter,
// dropping a required port, or an engine binding that transmits without an
// offered prepared response even while a lease is live.
func TestAA29NewEngineBindsBackendPortsWithoutTransmitting(t *testing.T) {
	profile := localGPIOTestProfile()
	fix := localGPIOOpenFixture(t, profile)
	mismatch := localGPIOTestEngineConfig()
	mismatch.TickHz = localGPIOTestHz * 2
	if _, err := fix.backend.NewEngine(mismatch, &localGPIOFakeSink{}); !errors.Is(err, ErrLocalGPIOResourceMatch) {
		t.Fatalf("engine rate mismatch = %v", err)
	}
	if _, err := fix.backend.NewEngine(localGPIOTestEngineConfig(), nil); !errors.Is(err, localworker.ErrMissingPort) {
		t.Fatalf("engine without sink = %v", err)
	}
	for _, armed := range []bool{false, true} {
		if armed {
			lease := localGPIOTestLease(profile)
			lease.NotAfterTick = fix.counter.now + localGPIOTestMaxLease
			if err := fix.backend.arm(lease); err != nil {
				t.Fatalf("arm: %v", err)
			}
		}
		sink := &localGPIOFakeSink{}
		engine, err := fix.backend.NewEngine(localGPIOTestEngineConfig(), sink)
		if err != nil {
			t.Fatalf("engine: %v", err)
		}
		loadsBefore := len(fix.window.loads)
		for index := 0; index < 200; index++ {
			fix.counter.now = engine.NextDeadlineTick()
			engine.Step()
		}
		if len(fix.window.loads) <= loadsBefore {
			t.Fatal("engine never sampled the window")
		}
		if len(fix.window.stores) != 0 || engine.Stats().TXEdges != 0 {
			t.Fatalf("armed=%v engine without a prepared response stored %v", armed, fix.window.stores)
		}
	}
}

func localGPIOParseSource(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

// Catches: enforcing lease expiry only on an edge request, so a polling engine
// leaves a driven line held past the deadline.
func TestAA29TickReadPastLeaseExpiryDisarmsAndReleases(t *testing.T) {
	profile := localGPIOTestProfile()
	fix := localGPIOOpenFixture(t, profile)
	lease := localGPIOTestLease(profile)
	if err := fix.backend.arm(lease); err != nil {
		t.Fatalf("arm: %v", err)
	}
	fix.backend.WriteEdge(localworker.LevelLow)
	fix.counter.now = lease.NotAfterTick - 1
	if fix.backend.NowTicks() != lease.NotAfterTick-1 || !fix.backend.armed {
		t.Fatal("tick read before expiry disarmed")
	}
	fix.counter.now = lease.NotAfterTick
	if fix.backend.NowTicks() != lease.NotAfterTick {
		t.Fatal("tick read changed the reported tick")
	}
	want := []localGPIOFakeStore{{localGPIOTestClear, localGPIOTestTXMask}, {localGPIOTestSet, localGPIOTestTXMask}}
	if len(fix.window.stores) != 2 || fix.window.stores[1] != want[1] {
		t.Fatalf("stores = %v, want drive then release", fix.window.stores)
	}
	stats := fix.backend.Stats()
	if fix.backend.armed || stats.LeaseExpiries != 1 || stats.Releases != 1 {
		t.Fatalf("after tick expiry armed=%v stats=%+v", fix.backend.armed, stats)
	}
	fix.backend.WriteEdge(localworker.LevelLow)
	if len(fix.window.stores) != 2 {
		t.Fatal("edge stored after tick expiry")
	}
}

// Catches: discarding an unmap failure while refusing an open, which hides a
// mapping that is still held.
func TestAA29OpenPreservesUnmapFailures(t *testing.T) {
	profile := localGPIOTestProfile()
	unmapCause := errors.New("synthetic unmap failure")
	mapCause := errors.New("synthetic map failure")

	mismatched := localGPIOTestWindow()
	mismatched.length++
	mismatched.unmapErr = unmapCause
	mapper := &localGPIOFakeMapper{window: mismatched}
	counter := &localGPIOFakeCounter{hz: localGPIOTestHz}
	mismatchBackend, err := OpenLocalGPIO(&profile, mapper, counter)
	if mismatchBackend == nil || mismatchBackend.AA22TransportCapabilities() != (AA22Capabilities{}) ||
		!errors.Is(err, ErrLocalGPIOResourceMatch) || !errors.Is(err, unmapCause) || mismatched.unmaps != 1 {
		t.Fatalf("mismatched window with failing unmap = (%v, %v) unmaps=%d", mismatchBackend, err, mismatched.unmaps)
	}
	if err := mismatchBackend.Close(); !errors.Is(err, unmapCause) || mismatched.unmaps != 2 {
		t.Fatalf("retained close = %v unmaps=%d, want retry failure and 2", err, mismatched.unmaps)
	}
	mismatched.unmapErr = nil
	if err := mismatchBackend.Close(); err != nil || mismatched.unmaps != 3 {
		t.Fatalf("retained close after recovery = %v unmaps=%d, want nil and 3", err, mismatched.unmaps)
	}
	if err := mismatchBackend.Close(); err != nil || mismatched.unmaps != 3 {
		t.Fatalf("close after successful unmap = %v unmaps=%d, want nil and 3", err, mismatched.unmaps)
	}

	errored := localGPIOTestWindow()
	errored.unmapErr = unmapCause
	failing := &localGPIOFakeMapper{window: errored, err: mapCause}
	if backend, err := OpenLocalGPIO(&profile, failing, counter); backend == nil ||
		!errors.Is(err, ErrLocalGPIOMapping) || !errors.Is(err, mapCause) || !errors.Is(err, unmapCause) || errored.unmaps != 1 {
		t.Fatalf("mapping error with failing unmap = (%v, %v) unmaps=%d", backend, err, errored.unmaps)
	}
}

// Catches: a plain bool for the closed state, which races between capability
// queries and Close (reported by the race detector).
func TestAA29CapabilitiesQueryRacesCloseSafely(t *testing.T) {
	fix := localGPIOOpenFixture(t, localGPIOTestProfile())
	backend := fix.backend
	if backend.AA22TransportCapabilities().AA22Kind != AA22KindLocalRaw {
		t.Fatalf("open backend must report LocalRaw")
	}
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for index := 0; index < 64; index++ {
			_ = backend.AA22TransportCapabilities()
		}
	}()
	if err := backend.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	group.Wait()
	if backend.AA22TransportCapabilities() != (AA22Capabilities{}) {
		t.Fatalf("closed backend must claim nothing")
	}
}
