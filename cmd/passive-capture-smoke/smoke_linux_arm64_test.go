//go:build linux && arm64 && aa29_local_gpio

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/cmd/passive-capture-smoke/internal/capture"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

// Every profile, window and counter below is synthetic. Offsets and masks are
// arbitrary values, not board registers, and no test opens a device node.
const (
	testProductionFile  = "smoke_linux_arm64.go"
	testTestFile        = "smoke_linux_arm64_test.go"
	testUnsupportedFile = "smoke_unsupported.go"
	testBuildLine       = "//go:build linux && arm64 && aa29_local_gpio"
	testUnsupportedLine = "//go:build !(linux && arm64 && aa29_local_gpio)"

	testHz        uint64 = 1_000_000
	testLength    uint64 = 0x40
	testRX        uint64 = 0x10
	testRXMask    uint32 = 1 << 3
	testResponder byte   = 0x15
	testTickStep  uint64 = 7
	testBaud      uint64 = 2400
	testSymbol    byte   = 0xAA
)

func testDocument() capture.ProfileDocument {
	return capture.ProfileDocument{
		ProfileID:     "aa31-synthetic-profile",
		Family:        "aa31-synthetic-family",
		Qualification: "proven",
		GPIO: capture.GPIODocument{
			MapLength:     testLength,
			AccessWidth:   4,
			Ordering:      "device",
			RXLevelOffset: testRX,
			TXSetOffset:   0x20,
			TXClearOffset: 0x30,
			RXMask:        testRXMask,
			TXMask:        1 << 5,
		},
		Counter:       capture.CounterDocument{Hz: testHz},
		MaxLeaseTicks: 5_000,
	}
}

// sealed converts a document with a placeholder digest, then records the
// digest of the converted profile, as an owner's qualification record would.
func sealed(t *testing.T, document capture.ProfileDocument) transport.LocalGPIOPlatformProfile {
	t.Helper()
	document.QualificationDigest = strings.Repeat("0", 64)
	converted, err := profile(document)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	converted.QualificationDigest = transport.LocalGPIOProfileDigest(converted)
	return converted
}

// waveWindow is a synthetic window whose RX bit follows a UART waveform of
// repeated testSymbol characters, clocked by the shared fake counter. It
// records every store attempt.
type waveWindow struct {
	counter *stepCounter
	loads   int
	stores  int
	unmaps  int
}

func (current *waveWindow) MappedLength() uint64     { return testLength }
func (current *waveWindow) MappedAccessWidth() uint8 { return 4 }
func (current *waveWindow) MappedOrdering() transport.LocalGPIOOrdering {
	return transport.LocalGPIOOrderingDevice
}

// Load32 frames each character as 20 idle bits, a start bit, eight data
// bits LSB first and a stop bit.
func (current *waveWindow) Load32(offset uint64) uint32 {
	current.loads++
	if offset != testRX {
		return 0
	}
	bit := current.counter.now * testBaud / testHz % 30
	high := true
	switch {
	case bit == 20:
		high = false
	case bit > 20 && bit < 29:
		high = testSymbol>>(bit-21)&1 == 1
	}
	if high {
		return testRXMask
	}
	return 0
}

func (current *waveWindow) Store32(uint64, uint32) { current.stores++ }
func (current *waveWindow) Unmap() error {
	current.unmaps++
	return nil
}

type waveMapper struct {
	window *waveWindow
	calls  int
}

func (current *waveMapper) Map(transport.LocalGPIODescriptor) (transport.LocalGPIOWindow, error) {
	current.calls++
	return current.window, nil
}

func (current *waveMapper) storeAttempts() uint64 {
	return uint64(current.window.stores)
}

// stepCounter advances by a fixed step on every read.
type stepCounter struct {
	now uint64
}

func (current *stepCounter) CounterHz() uint64 { return testHz }
func (current *stepCounter) ReadCounter() uint64 {
	current.now += testTickStep
	return current.now
}

func testRequest(t *testing.T) request {
	return request{
		profile:   sealed(t, testDocument()),
		responder: testResponder,
		duration:  100 * time.Millisecond,
		maxEvents: 4_096,
	}
}

func parseReport(t *testing.T, out string) ([]capture.EventRecord, capture.Summary) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	var events []capture.EventRecord
	for _, line := range lines[:len(lines)-1] {
		var event capture.EventRecord
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Record != "event" {
			t.Fatalf("event line %q: %v", line, err)
		}
		events = append(events, event)
	}
	var summary capture.Summary
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil || summary.Record != "summary" {
		t.Fatalf("summary line: %v", err)
	}
	return events, summary
}

// Catches: a non-default build line drifting, so the command or its tests
// would enter the default binary, or the refusing stub covering the tagged
// build.
func TestAA31BuildConstraintsPartitionBuilds(t *testing.T) {
	for path, want := range map[string]string{
		testProductionFile:  testBuildLine,
		testTestFile:        testBuildLine,
		testUnsupportedFile: testUnsupportedLine,
	} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if first, _, _ := strings.Cut(string(source), "\n"); first != want {
			t.Fatalf("%s build line = %q, want %q", path, first, want)
		}
	}
}

// Catches: a production literal naming a device path, which would give the
// command a default resource.
func TestAA31ProductionNamesNoDevicePath(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), testProductionFile, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if literal, ok := node.(*ast.BasicLit); ok && strings.Contains(literal.Value, "/dev") {
			t.Fatalf("production literal %s names a device path", literal.Value)
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
func TestAA31ProfileConvertsEveryField(t *testing.T) {
	base := testDocument()
	base.QualificationDigest = strings.Repeat("0", 64)
	baseProfile, err := profile(base)
	if err != nil {
		t.Fatal(err)
	}
	wantBase := transport.LocalGPIOPlatformProfile{
		ProfileID:     "aa31-synthetic-profile",
		Family:        "aa31-synthetic-family",
		Qualification: transport.AA22Proven,
		GPIO: transport.LocalGPIODescriptor{
			Role: transport.LocalGPIORoleGPIOControl, MapLength: testLength, AccessWidth: 4,
			Ordering: transport.LocalGPIOOrderingDevice, RXLevelOffset: testRX, TXSetOffset: 0x20,
			TXClearOffset: 0x30, RXMask: testRXMask, TXMask: 1 << 5,
		},
		Counter:       transport.LocalGPIOCounterDescriptor{Role: transport.LocalGPIORoleCounter, Hz: testHz},
		MaxLeaseTicks: 5_000,
	}
	if baseProfile != wantBase {
		t.Fatalf("base profile = %+v", baseProfile)
	}
	mutations := map[string]func(*capture.ProfileDocument){
		"profile_id":      func(d *capture.ProfileDocument) { d.ProfileID = "aa31-other-profile" },
		"family":          func(d *capture.ProfileDocument) { d.Family = "aa31-other-family" },
		"qualification":   func(d *capture.ProfileDocument) { d.Qualification = "hypothesis" },
		"map_length":      func(d *capture.ProfileDocument) { d.GPIO.MapLength = 0x80 },
		"access_width":    func(d *capture.ProfileDocument) { d.GPIO.AccessWidth = 8 },
		"ordering":        func(d *capture.ProfileDocument) { d.GPIO.Ordering = "relaxed" },
		"rx_level_offset": func(d *capture.ProfileDocument) { d.GPIO.RXLevelOffset = 0x14 },
		"tx_set_offset":   func(d *capture.ProfileDocument) { d.GPIO.TXSetOffset = 0x24 },
		"tx_clear_offset": func(d *capture.ProfileDocument) { d.GPIO.TXClearOffset = 0x34 },
		"rx_mask":         func(d *capture.ProfileDocument) { d.GPIO.RXMask = 1 << 4 },
		"tx_mask":         func(d *capture.ProfileDocument) { d.GPIO.TXMask = 1 << 6 },
		"rx_inverted":     func(d *capture.ProfileDocument) { d.GPIO.RXInverted = true },
		"tx_inverted":     func(d *capture.ProfileDocument) { d.GPIO.TXInverted = true },
		"counter_hz":      func(d *capture.ProfileDocument) { d.Counter.Hz = 2 * testHz },
		"max_lease_ticks": func(d *capture.ProfileDocument) { d.MaxLeaseTicks = 6_000 },
	}
	baseDigest := transport.LocalGPIOProfileDigest(baseProfile)
	for name, mutate := range mutations {
		document := base
		mutate(&document)
		got, err := profile(document)
		if err != nil {
			t.Fatalf("convert with %s: %v", name, err)
		}
		if got == baseProfile || transport.LocalGPIOProfileDigest(got) == baseDigest {
			t.Fatalf("conversion ignores %s", name)
		}
	}
	document := base
	document.QualificationDigest = "01" + strings.Repeat("0", 62)
	got, err := profile(document)
	if err != nil || got.QualificationDigest[0] != 0x01 || got.QualificationDigest == baseProfile.QualificationDigest {
		t.Fatalf("conversion ignores qualification_digest: %+v, %v", got, err)
	}
	for _, label := range []string{"unknown", "relaxed", "device"} {
		document := base
		document.GPIO.Ordering = label
		got, _ := profile(document)
		if want := map[string]transport.LocalGPIOOrdering{
			"unknown": transport.LocalGPIOOrderingUnknown, "relaxed": transport.LocalGPIOOrderingRelaxed, "device": transport.LocalGPIOOrderingDevice,
		}[label]; got.GPIO.Ordering != want {
			t.Fatalf("ordering %q = %v", label, got.GPIO.Ordering)
		}
	}
}

// Positive control. Catches: a capture that arms, stores or offers a response,
// a window that is never sampled, or a report that does not decode the
// synthetic waveform.
func TestAA31CaptureDecodesWaveformPassively(t *testing.T) {
	counter := &stepCounter{}
	window := &waveWindow{counter: counter}
	mapper := &waveMapper{window: window}
	var out bytes.Buffer
	code, err := runCapture(testRequest(t), mapper, counter, nil, &out)
	if code != exitPassive || err != nil {
		t.Fatalf("capture = %d, %v", code, err)
	}
	events, summary := parseReport(t, out.String())
	symbols := 0
	for _, event := range events {
		if event.Kind == "raw_symbol" && event.Raw == testSymbol {
			symbols++
		}
	}
	if symbols == 0 {
		t.Fatalf("capture decoded no symbol: %+v", events)
	}
	if !summary.Passive || summary.Stop != "budget" || summary.Engine.TXEdges != 0 ||
		summary.Backend != (capture.BackendCounters{}) || summary.WindowStores != 0 ||
		summary.EventsRecorded != uint64(len(events)) || summary.CounterHz != testHz ||
		summary.BudgetTicks != 100_000 || summary.ElapsedTicks < summary.BudgetTicks {
		t.Fatalf("summary = %+v", summary)
	}
	if mapper.calls != 1 || window.loads == 0 || window.stores != 0 || window.unmaps != 1 {
		t.Fatalf("window calls=%d loads=%d stores=%d unmaps=%d", mapper.calls, window.loads, window.stores, window.unmaps)
	}
}

// Catches: a report written for a capture that never ran, or a mapping made
// for a profile that admission refuses.
func TestAA31CaptureRefusesUnadmittedProfileWithoutMapping(t *testing.T) {
	cases := map[string]func(*request){
		"hypothesis": func(r *request) {
			r.profile.Qualification = transport.AA22Hypothesis
			r.profile.QualificationDigest = transport.LocalGPIOProfileDigest(r.profile)
		},
		"stale_digest": func(r *request) { r.profile.MaxLeaseTicks++ },
	}
	for name, mutate := range cases {
		counter := &stepCounter{}
		mapper := &waveMapper{window: &waveWindow{counter: counter}}
		req := testRequest(t)
		mutate(&req)
		var out bytes.Buffer
		code, err := runCapture(req, mapper, counter, nil, &out)
		if code != exitFailure || !errors.Is(err, transport.ErrLocalGPIOUnqualified) || mapper.calls != 0 || out.Len() != 0 {
			t.Fatalf("%s capture = %d, %v, maps %d, out %q", name, code, err, mapper.calls, out.String())
		}
	}
}

// Catches: a mapping port that reports device ordering for a regular file, so
// a page-cache mapping would be admitted as a GPIO control window; and a
// refused window left mapped.
func TestAA31RegularFileWindowIsRefusedAndUnmapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aa31-window")
	if err := os.WriteFile(path, make([]byte, testLength), 0o600); err != nil {
		t.Fatal(err)
	}
	req := testRequest(t)
	mapper := &fileMapper{path: path}
	var out bytes.Buffer
	code, err := runCapture(req, mapper, &stepCounter{}, nil, &out)
	if code != exitFailure || !errors.Is(err, transport.ErrLocalGPIOResourceMatch) || out.Len() != 0 {
		t.Fatalf("regular-file capture = %d, %v, out %q", code, err, out.String())
	}
	if len(mapper.windows) != 1 || mapper.windows[0].MappedLength() != 0 {
		t.Fatalf("refused window still mapped: %d windows", len(mapper.windows))
	}
}

// Catches: a window port with a store path, so an armed backend could drive
// the line through this command.
func TestAA31WindowStoreTouchesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aa31-window")
	if err := os.WriteFile(path, make([]byte, testLength), 0o600); err != nil {
		t.Fatal(err)
	}
	mapper := &fileMapper{path: path}
	port, err := mapper.Map(transport.LocalGPIODescriptor{MapLength: testLength})
	if err != nil {
		t.Fatal(err)
	}
	port.Store32(testRX, ^uint32(0))
	if port.Load32(testRX) != 0 || mapper.storeAttempts() != 1 || port.MappedOrdering() != transport.LocalGPIOOrderingRelaxed || port.MappedAccessWidth() != 4 {
		t.Fatal("window store reached memory or misreported the mapping")
	}
	if err := port.Unmap(); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source, make([]byte, testLength)) {
		t.Fatalf("window file changed: %v", err)
	}
}

// Catches: a missing resource flag falling back to a default, a malformed
// responder being truncated, or stray arguments being ignored.
func TestAA31ParseOptionsRequiresEveryResource(t *testing.T) {
	full := []string{"-profile", "aa31-p", "-window", "aa31-w", "-responder", "0x15"}
	opts, err := parseOptions(full, io.Discard)
	if err != nil || opts != (options{
		profilePath: "aa31-p", windowPath: "aa31-w", responder: 0x15,
		duration: defaultDuration, maxEvents: defaultMaxEvents,
	}) {
		t.Fatalf("options = %+v, %v", opts, err)
	}
	opts, err = parseOptions(append(full, "-window-offset", "4096", "-duration", "2s", "-max-events", "9"), io.Discard)
	if err != nil || opts.windowOffset != 4096 || opts.duration != 2*time.Second || opts.maxEvents != 9 {
		t.Fatalf("explicit options = %+v, %v", opts, err)
	}
	for index := 0; index < len(full); index += 2 {
		partial := append(append([]string{}, full[:index]...), full[index+2:]...)
		if _, err := parseOptions(partial, io.Discard); !errors.Is(err, errUsage) {
			t.Fatalf("options without %s = %v", full[index], err)
		}
	}
	for _, responder := range []string{"0x115", "x", "-1", "256"} {
		args := append(append([]string{}, full[:4]...), "-responder", responder)
		if _, err := parseOptions(args, io.Discard); !errors.Is(err, errUsage) {
			t.Fatalf("responder %q = %v", responder, err)
		}
	}
	if _, err := parseOptions(append(full, "stray"), io.Discard); !errors.Is(err, errUsage) {
		t.Fatalf("stray argument = %v", err)
	}
}

// Catches: an initiator-class responder or an out-of-range budget reaching the
// engine instead of being refused before capture.
func TestAA31CaptureRefusesInvalidRequests(t *testing.T) {
	cases := map[string]func(*request){
		"initiator_responder": func(r *request) { r.responder = 0x10 },
		"zero_duration":       func(r *request) { r.duration = 0 },
		"over_duration":       func(r *request) { r.duration = capture.MaxDuration + 1 },
		"zero_events":         func(r *request) { r.maxEvents = 0 },
	}
	for name, mutate := range cases {
		counter := &stepCounter{}
		window := &waveWindow{counter: counter}
		req := testRequest(t)
		mutate(&req)
		var out bytes.Buffer
		code, err := runCapture(req, &waveMapper{window: window}, counter, nil, &out)
		if code != exitUsage || err == nil || out.Len() != 0 || window.stores != 0 {
			t.Fatalf("%s capture = %d, %v", name, code, err)
		}
		if name == "initiator_responder" && window.unmaps != 1 {
			t.Fatalf("refused engine left the window mapped (%d unmaps)", window.unmaps)
		}
	}
}

// Catches: a profile file path that is not decoded strictly before capture.
func TestAA31MainRefusesMalformedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aa31-profile.json")
	if err := os.WriteFile(path, []byte(`{"profile_id":"aa31","extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-profile", path, "-window", path, "-responder", strconv.Itoa(int(testResponder))}, &stdout, &stderr)
	if code != exitUsage || stdout.Len() != 0 || !strings.Contains(stderr.String(), "profile") {
		t.Fatalf("main = %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}
