//go:build linux && arm64 && aa29_local_gpio

package main

import (
	aa31bytes "bytes"
	aa31json "encoding/json"
	aa31errors "errors"
	aa31ast "go/ast"
	aa31parser "go/parser"
	aa31token "go/token"
	aa31io "io"
	aa31os "os"
	aa31filepath "path/filepath"
	aa31strconv "strconv"
	aa31strings "strings"
	aa31testing "testing"
	aa31time "time"

	aa31capture "github.com/Project-Helianthus/helianthus-ebusgo/cmd/aa29-smoke/internal/aa31capture"
	aa31transport "github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// Every profile, window and counter below is synthetic. Offsets and masks are
// arbitrary values, not board registers, and no test opens a device node.
const (
	aa31TestProductionFile  = "aa31_smoke_linux_arm64.go"
	aa31TestTestFile        = "aa31_smoke_linux_arm64_test.go"
	aa31TestUnsupportedFile = "aa31_smoke_unsupported.go"
	aa31TestBuildLine       = "//go:build linux && arm64 && aa29_local_gpio"
	aa31TestUnsupportedLine = "//go:build !(linux && arm64 && aa29_local_gpio)"

	aa31TestHz        uint64 = 1_000_000
	aa31TestLength    uint64 = 0x40
	aa31TestRX        uint64 = 0x10
	aa31TestRXMask    uint32 = 1 << 3
	aa31TestResponder byte   = 0x15
	aa31TestTickStep  uint64 = 7
	aa31TestBaud      uint64 = 2400
	aa31TestSymbol    byte   = 0xAA
)

func aa31TestDocument() aa31capture.AA31ProfileDocument {
	return aa31capture.AA31ProfileDocument{
		AA31ProfileID:     "aa31-synthetic-profile",
		AA31Family:        "aa31-synthetic-family",
		AA31Qualification: "proven",
		AA31GPIO: aa31capture.AA31GPIODocument{
			AA31MapLength:     aa31TestLength,
			AA31AccessWidth:   4,
			AA31Ordering:      "device",
			AA31RXLevelOffset: aa31TestRX,
			AA31TXSetOffset:   0x20,
			AA31TXClearOffset: 0x30,
			AA31RXMask:        aa31TestRXMask,
			AA31TXMask:        1 << 5,
		},
		AA31Counter:       aa31capture.AA31CounterDocument{AA31Hz: aa31TestHz},
		AA31MaxLeaseTicks: 5_000,
	}
}

// aa31Sealed converts a document with a placeholder digest, then records the
// digest of the converted profile, as an owner's qualification record would.
func aa31Sealed(aa31T *aa31testing.T, aa31Document aa31capture.AA31ProfileDocument) aa31transport.AA29PlatformProfile {
	aa31T.Helper()
	aa31Document.AA31QualificationDigest = aa31strings.Repeat("0", 64)
	aa31Converted, aa31Err := aa31Profile(aa31Document)
	if aa31Err != nil {
		aa31T.Fatalf("aa31 convert: %v", aa31Err)
	}
	aa31Converted.AA29QualificationDigest = aa31transport.AA29ProfileDigest(aa31Converted)
	return aa31Converted
}

// aa31WaveWindow is a synthetic window whose RX bit follows a UART waveform of
// repeated aa31TestSymbol characters, clocked by the shared fake counter. It
// records every store attempt.
type aa31WaveWindow struct {
	aa31Counter *aa31StepCounter
	aa31Loads   int
	aa31Stores  int
	aa31Unmaps  int
}

func (aa31Current *aa31WaveWindow) AA29MappedLength() uint64     { return aa31TestLength }
func (aa31Current *aa31WaveWindow) AA29MappedAccessWidth() uint8 { return 4 }
func (aa31Current *aa31WaveWindow) AA29MappedOrdering() aa31transport.AA29Ordering {
	return aa31transport.AA29OrderingDevice
}

// AA29Load32 frames each character as 20 idle bits, a start bit, eight data
// bits LSB first and a stop bit.
func (aa31Current *aa31WaveWindow) AA29Load32(aa31Offset uint64) uint32 {
	aa31Current.aa31Loads++
	if aa31Offset != aa31TestRX {
		return 0
	}
	aa31Bit := aa31Current.aa31Counter.aa31Now * aa31TestBaud / aa31TestHz % 30
	aa31High := true
	switch {
	case aa31Bit == 20:
		aa31High = false
	case aa31Bit > 20 && aa31Bit < 29:
		aa31High = aa31TestSymbol>>(aa31Bit-21)&1 == 1
	}
	if aa31High {
		return aa31TestRXMask
	}
	return 0
}

func (aa31Current *aa31WaveWindow) AA29Store32(uint64, uint32) { aa31Current.aa31Stores++ }
func (aa31Current *aa31WaveWindow) AA29Unmap() error {
	aa31Current.aa31Unmaps++
	return nil
}

type aa31WaveMapper struct {
	aa31Window *aa31WaveWindow
	aa31Calls  int
}

func (aa31Current *aa31WaveMapper) AA29Map(aa31transport.AA29GPIODescriptor) (aa31transport.AA29Window, error) {
	aa31Current.aa31Calls++
	return aa31Current.aa31Window, nil
}

func (aa31Current *aa31WaveMapper) aa31StoreAttempts() uint64 {
	return uint64(aa31Current.aa31Window.aa31Stores)
}

// aa31StepCounter advances by a fixed step on every read.
type aa31StepCounter struct {
	aa31Now uint64
}

func (aa31Current *aa31StepCounter) AA29CounterHz() uint64 { return aa31TestHz }
func (aa31Current *aa31StepCounter) AA29ReadCounter() uint64 {
	aa31Current.aa31Now += aa31TestTickStep
	return aa31Current.aa31Now
}

func aa31TestRequest(aa31T *aa31testing.T) aa31Request {
	return aa31Request{
		aa31Profile:   aa31Sealed(aa31T, aa31TestDocument()),
		aa31Responder: aa31TestResponder,
		aa31Duration:  100 * aa31time.Millisecond,
		aa31MaxEvents: 4_096,
	}
}

func aa31ParseReport(aa31T *aa31testing.T, aa31Out string) ([]aa31capture.AA31EventRecord, aa31capture.AA31Summary) {
	aa31T.Helper()
	aa31Lines := aa31strings.Split(aa31strings.TrimSuffix(aa31Out, "\n"), "\n")
	var aa31Events []aa31capture.AA31EventRecord
	for _, aa31Line := range aa31Lines[:len(aa31Lines)-1] {
		var aa31Event aa31capture.AA31EventRecord
		if aa31Err := aa31json.Unmarshal([]byte(aa31Line), &aa31Event); aa31Err != nil || aa31Event.AA31Record != "event" {
			aa31T.Fatalf("aa31 event line %q: %v", aa31Line, aa31Err)
		}
		aa31Events = append(aa31Events, aa31Event)
	}
	var aa31Summary aa31capture.AA31Summary
	if aa31Err := aa31json.Unmarshal([]byte(aa31Lines[len(aa31Lines)-1]), &aa31Summary); aa31Err != nil || aa31Summary.AA31Record != "summary" {
		aa31T.Fatalf("aa31 summary line: %v", aa31Err)
	}
	return aa31Events, aa31Summary
}

// Catches: a non-default build line drifting, so the command or its tests
// would enter the default binary, or the refusing stub covering the tagged
// build.
func TestAA31BuildConstraintsPartitionBuilds(aa31T *aa31testing.T) {
	for aa31Path, aa31Want := range map[string]string{
		aa31TestProductionFile:  aa31TestBuildLine,
		aa31TestTestFile:        aa31TestBuildLine,
		aa31TestUnsupportedFile: aa31TestUnsupportedLine,
	} {
		aa31Source, aa31Err := aa31os.ReadFile(aa31Path)
		if aa31Err != nil {
			aa31T.Fatalf("aa31 read %s: %v", aa31Path, aa31Err)
		}
		if aa31First, _, _ := aa31strings.Cut(string(aa31Source), "\n"); aa31First != aa31Want {
			aa31T.Fatalf("aa31 %s build line = %q, want %q", aa31Path, aa31First, aa31Want)
		}
	}
}

// Catches: a production literal naming a device path, which would give the
// command a default resource.
func TestAA31ProductionNamesNoDevicePath(aa31T *aa31testing.T) {
	aa31File, aa31Err := aa31parser.ParseFile(aa31token.NewFileSet(), aa31TestProductionFile, nil, aa31parser.SkipObjectResolution)
	if aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	aa31ast.Inspect(aa31File, func(aa31Node aa31ast.Node) bool {
		if aa31Literal, aa31OK := aa31Node.(*aa31ast.BasicLit); aa31OK && aa31strings.Contains(aa31Literal.Value, "/dev") {
			aa31T.Fatalf("aa31 production literal %s names a device path", aa31Literal.Value)
		}
		return true
	})
}

// Catches, per field, a conversion that drops or swaps one document field:
// profile id, family, qualification, qualification digest, map length, access
// width, ordering, RX level offset, TX set offset, TX clear offset, RX mask, TX
// mask, RX inversion, TX inversion, counter rate and maximum lease ticks. Each
// mutation must change the converted profile and, except the digest itself,
// its profile digest.
func TestAA31ProfileConvertsEveryField(aa31T *aa31testing.T) {
	aa31Base := aa31TestDocument()
	aa31Base.AA31QualificationDigest = aa31strings.Repeat("0", 64)
	aa31BaseProfile, aa31Err := aa31Profile(aa31Base)
	if aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	aa31WantBase := aa31transport.AA29PlatformProfile{
		AA29ProfileID:     "aa31-synthetic-profile",
		AA29Family:        "aa31-synthetic-family",
		AA29Qualification: aa31transport.AA22Proven,
		AA29GPIO: aa31transport.AA29GPIODescriptor{
			AA29Role: aa31transport.AA29RoleGPIOControl, AA29MapLength: aa31TestLength, AA29AccessWidth: 4,
			AA29Ordering: aa31transport.AA29OrderingDevice, AA29RXLevelOffset: aa31TestRX, AA29TXSetOffset: 0x20,
			AA29TXClearOffset: 0x30, AA29RXMask: aa31TestRXMask, AA29TXMask: 1 << 5,
		},
		AA29Counter:       aa31transport.AA29CounterDescriptor{AA29Role: aa31transport.AA29RoleCounter, AA29Hz: aa31TestHz},
		AA29MaxLeaseTicks: 5_000,
	}
	if aa31BaseProfile != aa31WantBase {
		aa31T.Fatalf("aa31 base profile = %+v", aa31BaseProfile)
	}
	aa31Mutations := map[string]func(*aa31capture.AA31ProfileDocument){
		"profile_id":      func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31ProfileID = "aa31-other-profile" },
		"family":          func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31Family = "aa31-other-family" },
		"qualification":   func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31Qualification = "hypothesis" },
		"map_length":      func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31MapLength = 0x80 },
		"access_width":    func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31AccessWidth = 8 },
		"ordering":        func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31Ordering = "relaxed" },
		"rx_level_offset": func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31RXLevelOffset = 0x14 },
		"tx_set_offset":   func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31TXSetOffset = 0x24 },
		"tx_clear_offset": func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31TXClearOffset = 0x34 },
		"rx_mask":         func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31RXMask = 1 << 4 },
		"tx_mask":         func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31TXMask = 1 << 6 },
		"rx_inverted":     func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31RXInverted = true },
		"tx_inverted":     func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31GPIO.AA31TXInverted = true },
		"counter_hz":      func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31Counter.AA31Hz = 2 * aa31TestHz },
		"max_lease_ticks": func(aa31D *aa31capture.AA31ProfileDocument) { aa31D.AA31MaxLeaseTicks = 6_000 },
	}
	aa31BaseDigest := aa31transport.AA29ProfileDigest(aa31BaseProfile)
	for aa31Name, aa31Mutate := range aa31Mutations {
		aa31Document := aa31Base
		aa31Mutate(&aa31Document)
		aa31Got, aa31Err := aa31Profile(aa31Document)
		if aa31Err != nil {
			aa31T.Fatalf("aa31 convert with %s: %v", aa31Name, aa31Err)
		}
		if aa31Got == aa31BaseProfile || aa31transport.AA29ProfileDigest(aa31Got) == aa31BaseDigest {
			aa31T.Fatalf("aa31 conversion ignores %s", aa31Name)
		}
	}
	aa31Document := aa31Base
	aa31Document.AA31QualificationDigest = "01" + aa31strings.Repeat("0", 62)
	aa31Got, aa31Err := aa31Profile(aa31Document)
	if aa31Err != nil || aa31Got.AA29QualificationDigest[0] != 0x01 || aa31Got.AA29QualificationDigest == aa31BaseProfile.AA29QualificationDigest {
		aa31T.Fatalf("aa31 conversion ignores qualification_digest: %+v, %v", aa31Got, aa31Err)
	}
	for _, aa31Label := range []string{"unknown", "relaxed", "device"} {
		aa31Document := aa31Base
		aa31Document.AA31GPIO.AA31Ordering = aa31Label
		aa31Got, _ := aa31Profile(aa31Document)
		if aa31Want := map[string]aa31transport.AA29Ordering{
			"unknown": aa31transport.AA29OrderingUnknown, "relaxed": aa31transport.AA29OrderingRelaxed, "device": aa31transport.AA29OrderingDevice,
		}[aa31Label]; aa31Got.AA29GPIO.AA29Ordering != aa31Want {
			aa31T.Fatalf("aa31 ordering %q = %v", aa31Label, aa31Got.AA29GPIO.AA29Ordering)
		}
	}
}

// Positive control. Catches: a capture that arms, stores or offers a response,
// a window that is never sampled, or a report that does not decode the
// synthetic waveform.
func TestAA31CaptureDecodesWaveformPassively(aa31T *aa31testing.T) {
	aa31Counter := &aa31StepCounter{}
	aa31Window := &aa31WaveWindow{aa31Counter: aa31Counter}
	aa31Mapper := &aa31WaveMapper{aa31Window: aa31Window}
	var aa31Out aa31bytes.Buffer
	aa31Code, aa31Err := aa31Capture(aa31TestRequest(aa31T), aa31Mapper, aa31Counter, nil, &aa31Out)
	if aa31Code != aa31ExitPassive || aa31Err != nil {
		aa31T.Fatalf("aa31 capture = %d, %v", aa31Code, aa31Err)
	}
	aa31Events, aa31Summary := aa31ParseReport(aa31T, aa31Out.String())
	aa31Symbols := 0
	for _, aa31Event := range aa31Events {
		if aa31Event.AA31Kind == "raw_symbol" && aa31Event.AA31Raw == aa31TestSymbol {
			aa31Symbols++
		}
	}
	if aa31Symbols == 0 {
		aa31T.Fatalf("aa31 capture decoded no symbol: %+v", aa31Events)
	}
	if !aa31Summary.AA31Passive || aa31Summary.AA31Stop != "budget" || aa31Summary.AA31Engine.AA31TXEdges != 0 ||
		aa31Summary.AA31Backend != (aa31capture.AA31BackendCounters{}) || aa31Summary.AA31WindowStores != 0 ||
		aa31Summary.AA31EventsRecorded != uint64(len(aa31Events)) || aa31Summary.AA31CounterHz != aa31TestHz ||
		aa31Summary.AA31BudgetTicks != 100_000 || aa31Summary.AA31ElapsedTicks < aa31Summary.AA31BudgetTicks {
		aa31T.Fatalf("aa31 summary = %+v", aa31Summary)
	}
	if aa31Mapper.aa31Calls != 1 || aa31Window.aa31Loads == 0 || aa31Window.aa31Stores != 0 || aa31Window.aa31Unmaps != 1 {
		aa31T.Fatalf("aa31 window calls=%d loads=%d stores=%d unmaps=%d", aa31Mapper.aa31Calls, aa31Window.aa31Loads, aa31Window.aa31Stores, aa31Window.aa31Unmaps)
	}
}

// Catches: a report written for a capture that never ran, or a mapping made
// for a profile that admission refuses.
func TestAA31CaptureRefusesUnadmittedProfileWithoutMapping(aa31T *aa31testing.T) {
	aa31Cases := map[string]func(*aa31Request){
		"hypothesis": func(aa31R *aa31Request) {
			aa31R.aa31Profile.AA29Qualification = aa31transport.AA22Hypothesis
			aa31R.aa31Profile.AA29QualificationDigest = aa31transport.AA29ProfileDigest(aa31R.aa31Profile)
		},
		"stale_digest": func(aa31R *aa31Request) { aa31R.aa31Profile.AA29MaxLeaseTicks++ },
	}
	for aa31Name, aa31Mutate := range aa31Cases {
		aa31Counter := &aa31StepCounter{}
		aa31Mapper := &aa31WaveMapper{aa31Window: &aa31WaveWindow{aa31Counter: aa31Counter}}
		aa31Req := aa31TestRequest(aa31T)
		aa31Mutate(&aa31Req)
		var aa31Out aa31bytes.Buffer
		aa31Code, aa31Err := aa31Capture(aa31Req, aa31Mapper, aa31Counter, nil, &aa31Out)
		if aa31Code != aa31ExitFailure || !aa31errors.Is(aa31Err, aa31transport.AA29ErrUnqualified) || aa31Mapper.aa31Calls != 0 || aa31Out.Len() != 0 {
			aa31T.Fatalf("aa31 %s capture = %d, %v, maps %d, out %q", aa31Name, aa31Code, aa31Err, aa31Mapper.aa31Calls, aa31Out.String())
		}
	}
}

// Catches: a mapping port that reports device ordering for a regular file, so
// a page-cache mapping would be admitted as a GPIO control window; and a
// refused window left mapped.
func TestAA31RegularFileWindowIsRefusedAndUnmapped(aa31T *aa31testing.T) {
	aa31Path := aa31filepath.Join(aa31T.TempDir(), "aa31-window")
	if aa31Err := aa31os.WriteFile(aa31Path, make([]byte, aa31TestLength), 0o600); aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	aa31Req := aa31TestRequest(aa31T)
	aa31Mapper := &aa31Mapper{aa31Path: aa31Path}
	var aa31Out aa31bytes.Buffer
	aa31Code, aa31Err := aa31Capture(aa31Req, aa31Mapper, &aa31StepCounter{}, nil, &aa31Out)
	if aa31Code != aa31ExitFailure || !aa31errors.Is(aa31Err, aa31transport.AA29ErrResourceMatch) || aa31Out.Len() != 0 {
		aa31T.Fatalf("aa31 regular-file capture = %d, %v, out %q", aa31Code, aa31Err, aa31Out.String())
	}
	if len(aa31Mapper.aa31Windows) != 1 || aa31Mapper.aa31Windows[0].AA29MappedLength() != 0 {
		aa31T.Fatalf("aa31 refused window still mapped: %d windows", len(aa31Mapper.aa31Windows))
	}
}

// Catches: a window port with a store path, so an armed backend could drive
// the line through this command.
func TestAA31WindowStoreTouchesNothing(aa31T *aa31testing.T) {
	aa31Path := aa31filepath.Join(aa31T.TempDir(), "aa31-window")
	if aa31Err := aa31os.WriteFile(aa31Path, make([]byte, aa31TestLength), 0o600); aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	aa31Mapper := &aa31Mapper{aa31Path: aa31Path}
	aa31Port, aa31Err := aa31Mapper.AA29Map(aa31transport.AA29GPIODescriptor{AA29MapLength: aa31TestLength})
	if aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	aa31Port.AA29Store32(aa31TestRX, ^uint32(0))
	if aa31Port.AA29Load32(aa31TestRX) != 0 || aa31Mapper.aa31StoreAttempts() != 1 || aa31Port.AA29MappedOrdering() != aa31transport.AA29OrderingRelaxed || aa31Port.AA29MappedAccessWidth() != 4 {
		aa31T.Fatal("aa31 window store reached memory or misreported the mapping")
	}
	if aa31Err := aa31Port.AA29Unmap(); aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	aa31Source, aa31Err := aa31os.ReadFile(aa31Path)
	if aa31Err != nil || !aa31bytes.Equal(aa31Source, make([]byte, aa31TestLength)) {
		aa31T.Fatalf("aa31 window file changed: %v", aa31Err)
	}
}

// Catches: a missing resource flag falling back to a default, a malformed
// responder being truncated, or stray arguments being ignored.
func TestAA31ParseOptionsRequiresEveryResource(aa31T *aa31testing.T) {
	aa31Full := []string{"-profile", "aa31-p", "-window", "aa31-w", "-responder", "0x15"}
	aa31Opts, aa31Err := aa31ParseOptions(aa31Full, aa31io.Discard)
	if aa31Err != nil || aa31Opts != (aa31Options{
		aa31ProfilePath: "aa31-p", aa31WindowPath: "aa31-w", aa31Responder: 0x15,
		aa31Duration: aa31DefaultDuration, aa31MaxEvents: aa31DefaultMaxEvents,
	}) {
		aa31T.Fatalf("aa31 options = %+v, %v", aa31Opts, aa31Err)
	}
	aa31Opts, aa31Err = aa31ParseOptions(append(aa31Full, "-window-offset", "4096", "-duration", "2s", "-max-events", "9"), aa31io.Discard)
	if aa31Err != nil || aa31Opts.aa31WindowOffset != 4096 || aa31Opts.aa31Duration != 2*aa31time.Second || aa31Opts.aa31MaxEvents != 9 {
		aa31T.Fatalf("aa31 explicit options = %+v, %v", aa31Opts, aa31Err)
	}
	for aa31Index := 0; aa31Index < len(aa31Full); aa31Index += 2 {
		aa31Partial := append(append([]string{}, aa31Full[:aa31Index]...), aa31Full[aa31Index+2:]...)
		if _, aa31Err := aa31ParseOptions(aa31Partial, aa31io.Discard); !aa31errors.Is(aa31Err, aa31ErrUsage) {
			aa31T.Fatalf("aa31 options without %s = %v", aa31Full[aa31Index], aa31Err)
		}
	}
	for _, aa31Responder := range []string{"0x115", "x", "-1", "256"} {
		aa31Args := append(append([]string{}, aa31Full[:4]...), "-responder", aa31Responder)
		if _, aa31Err := aa31ParseOptions(aa31Args, aa31io.Discard); !aa31errors.Is(aa31Err, aa31ErrUsage) {
			aa31T.Fatalf("aa31 responder %q = %v", aa31Responder, aa31Err)
		}
	}
	if _, aa31Err := aa31ParseOptions(append(aa31Full, "stray"), aa31io.Discard); !aa31errors.Is(aa31Err, aa31ErrUsage) {
		aa31T.Fatalf("aa31 stray argument = %v", aa31Err)
	}
}

// Catches: an initiator-class responder or an out-of-range budget reaching the
// engine instead of being refused before capture.
func TestAA31CaptureRefusesInvalidRequests(aa31T *aa31testing.T) {
	aa31Cases := map[string]func(*aa31Request){
		"initiator_responder": func(aa31R *aa31Request) { aa31R.aa31Responder = 0x10 },
		"zero_duration":       func(aa31R *aa31Request) { aa31R.aa31Duration = 0 },
		"over_duration":       func(aa31R *aa31Request) { aa31R.aa31Duration = aa31capture.AA31MaxDuration + 1 },
		"zero_events":         func(aa31R *aa31Request) { aa31R.aa31MaxEvents = 0 },
	}
	for aa31Name, aa31Mutate := range aa31Cases {
		aa31Counter := &aa31StepCounter{}
		aa31Window := &aa31WaveWindow{aa31Counter: aa31Counter}
		aa31Req := aa31TestRequest(aa31T)
		aa31Mutate(&aa31Req)
		var aa31Out aa31bytes.Buffer
		aa31Code, aa31Err := aa31Capture(aa31Req, &aa31WaveMapper{aa31Window: aa31Window}, aa31Counter, nil, &aa31Out)
		if aa31Code != aa31ExitUsage || aa31Err == nil || aa31Out.Len() != 0 || aa31Window.aa31Stores != 0 {
			aa31T.Fatalf("aa31 %s capture = %d, %v", aa31Name, aa31Code, aa31Err)
		}
		if aa31Name == "initiator_responder" && aa31Window.aa31Unmaps != 1 {
			aa31T.Fatalf("aa31 refused engine left the window mapped (%d unmaps)", aa31Window.aa31Unmaps)
		}
	}
}

// Catches: a profile file path that is not decoded strictly before capture.
func TestAA31MainRefusesMalformedProfile(aa31T *aa31testing.T) {
	aa31Path := aa31filepath.Join(aa31T.TempDir(), "aa31-profile.json")
	if aa31Err := aa31os.WriteFile(aa31Path, []byte(`{"profile_id":"aa31","extra":1}`), 0o600); aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	var aa31Stdout, aa31Stderr aa31bytes.Buffer
	aa31Code := aa31Main([]string{"-profile", aa31Path, "-window", aa31Path, "-responder", aa31strconv.Itoa(int(aa31TestResponder))}, &aa31Stdout, &aa31Stderr)
	if aa31Code != aa31ExitUsage || aa31Stdout.Len() != 0 || !aa31strings.Contains(aa31Stderr.String(), "profile") {
		aa31T.Fatalf("aa31 main = %d, stdout %q, stderr %q", aa31Code, aa31Stdout.String(), aa31Stderr.String())
	}
}

func aa31CheckPrefix(aa31T *aa31testing.T, aa31Name string) {
	aa31T.Helper()
	if aa31Name == "_" || aa31Name == "main" || aa31strings.HasPrefix(aa31Name, "aa31") || aa31strings.HasPrefix(aa31Name, "AA31") || aa31strings.HasPrefix(aa31Name, "TestAA31") {
		return
	}
	aa31T.Fatalf("aa31 identifier %q lacks the AA31 prefix", aa31Name)
}

// Method names that satisfy the AA29 window, mapping and counter ports carry
// that ticket's prefix by contract and are the only exemptions besides main.
func TestAA31EveryDeclaredIdentifierIsPrefixed(aa31T *aa31testing.T) {
	aa31Ported := map[string]bool{
		"AA29MappedLength":      true,
		"AA29MappedAccessWidth": true,
		"AA29MappedOrdering":    true,
		"AA29Load32":            true,
		"AA29Store32":           true,
		"AA29Unmap":             true,
		"AA29Map":               true,
		"AA29CounterHz":         true,
		"AA29ReadCounter":       true,
	}
	for _, aa31Path := range []string{aa31TestProductionFile, aa31TestTestFile, aa31TestUnsupportedFile} {
		aa31File, aa31Err := aa31parser.ParseFile(aa31token.NewFileSet(), aa31Path, nil, aa31parser.SkipObjectResolution)
		if aa31Err != nil {
			aa31T.Fatalf("aa31 parse %s: %v", aa31Path, aa31Err)
		}
		for _, aa31Import := range aa31File.Imports {
			if aa31Import.Name == nil {
				aa31T.Fatalf("aa31 import %s has no prefixed alias", aa31Import.Path.Value)
			}
			aa31CheckPrefix(aa31T, aa31Import.Name.Name)
		}
		aa31ast.Inspect(aa31File, func(aa31Node aa31ast.Node) bool {
			switch aa31Value := aa31Node.(type) {
			case *aa31ast.FuncDecl:
				if !aa31Ported[aa31Value.Name.Name] {
					aa31CheckPrefix(aa31T, aa31Value.Name.Name)
				}
			case *aa31ast.TypeSpec:
				aa31CheckPrefix(aa31T, aa31Value.Name.Name)
			case *aa31ast.ValueSpec:
				for _, aa31Name := range aa31Value.Names {
					aa31CheckPrefix(aa31T, aa31Name.Name)
				}
			case *aa31ast.Field:
				for _, aa31Name := range aa31Value.Names {
					aa31CheckPrefix(aa31T, aa31Name.Name)
				}
			case *aa31ast.AssignStmt:
				if aa31Value.Tok == aa31token.DEFINE {
					for _, aa31Left := range aa31Value.Lhs {
						if aa31Ident, aa31OK := aa31Left.(*aa31ast.Ident); aa31OK {
							aa31CheckPrefix(aa31T, aa31Ident.Name)
						}
					}
				}
			case *aa31ast.RangeStmt:
				if aa31Value.Tok == aa31token.DEFINE {
					for _, aa31Left := range []aa31ast.Expr{aa31Value.Key, aa31Value.Value} {
						if aa31Ident, aa31OK := aa31Left.(*aa31ast.Ident); aa31OK {
							aa31CheckPrefix(aa31T, aa31Ident.Name)
						}
					}
				}
			}
			return true
		})
	}
}
