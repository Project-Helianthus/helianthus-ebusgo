//go:build linux && arm64 && aa29_local_gpio

// Command passive-capture-smoke runs one bounded passive capture over the local GPIO
// backend and writes the decoded engine events as JSON lines.
//
// Status: Hypothesis. The command supplies the local GPIO window and counter
// ports: a read-only shared mapping of an owner-named file and the process
// monotonic clock. It never arms the backend and never offers a prepared
// response, and the window it maps has no write permission, so it cannot drive
// the line. Physical timing and the electrical frontend remain Unknown.
//
// Usage:
//
//	passive-capture-smoke -profile FILE -window FILE [-window-offset N] -responder ADDR
//	           [-duration D] [-max-events N]
//
// The profile is an owner-provisioned local GPIO platform profile in JSON form whose
// counter rate must be the monotonic counter rate. The window file and offset
// are owner-provisioned; the command has no default for either.
//
// Exit status: 0 passive capture completed, 1 runtime failure, 2 usage, 3 a
// passive invariant did not hold.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/cmd/passive-capture-smoke/internal/capture"
	"github.com/Project-Helianthus/helianthus-ebusgo/localworker"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

const (
	exitPassive    = 0
	exitFailure    = 1
	exitUsage      = 2
	exitNotPassive = 3

	defaultDuration  = 10 * time.Second
	defaultMaxEvents = 1 << 16
	closeAttempts    = 2
)

var errUsage = errors.New("aa29-smoke: usage")

type options struct {
	profilePath  string
	windowPath   string
	windowOffset uint64
	responder    byte
	duration     time.Duration
	maxEvents    int
}

// parseOptions requires the profile, window and responder flags; nothing
// that names a resource has a default.
func parseOptions(args []string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("aa29-smoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var result options
	var responder string
	flags.StringVar(&result.profilePath, "profile", "", "owner-provisioned AA29 platform profile (JSON)")
	flags.StringVar(&result.windowPath, "window", "", "owner-provisioned file holding the GPIO control window")
	flags.Uint64Var(&result.windowOffset, "window-offset", 0, "page-aligned offset of the window in that file")
	flags.StringVar(&responder, "responder", "", "target address the engine tags frames for (for example 0x15)")
	flags.DurationVar(&result.duration, "duration", defaultDuration, "capture budget")
	flags.IntVar(&result.maxEvents, "max-events", defaultMaxEvents, "recorder capacity")
	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("%w: unexpected argument %q", errUsage, flags.Arg(0))
	}
	if result.profilePath == "" || result.windowPath == "" || responder == "" {
		return options{}, fmt.Errorf("%w: -profile, -window and -responder are required", errUsage)
	}
	address, err := strconv.ParseUint(responder, 0, 8)
	if err != nil {
		return options{}, fmt.Errorf("%w: -responder: %w", errUsage, err)
	}
	result.responder = byte(address)
	return result, nil
}

func ordering(label string) transport.LocalGPIOOrdering {
	switch label {
	case "relaxed":
		return transport.LocalGPIOOrderingRelaxed
	case "device":
		return transport.LocalGPIOOrderingDevice
	}
	return transport.LocalGPIOOrderingUnknown
}

// profile converts a decoded document field for field. Roles are fixed by
// position. Admission stays with OpenLocalGPIO.
func profile(document capture.ProfileDocument) (transport.LocalGPIOPlatformProfile, error) {
	qualification, err := capture.ParseQualification(document.Qualification)
	if err != nil {
		return transport.LocalGPIOPlatformProfile{}, err
	}
	digest, err := capture.ParseDigest(document.QualificationDigest)
	if err != nil {
		return transport.LocalGPIOPlatformProfile{}, err
	}
	gpio := document.GPIO
	return transport.LocalGPIOPlatformProfile{
		ProfileID:           document.ProfileID,
		Family:              document.Family,
		Qualification:       qualification,
		QualificationDigest: digest,
		GPIO: transport.LocalGPIODescriptor{
			Role:          transport.LocalGPIORoleGPIOControl,
			MapLength:     gpio.MapLength,
			AccessWidth:   gpio.AccessWidth,
			Ordering:      ordering(gpio.Ordering),
			RXLevelOffset: gpio.RXLevelOffset,
			TXSetOffset:   gpio.TXSetOffset,
			TXClearOffset: gpio.TXClearOffset,
			RXMask:        gpio.RXMask,
			TXMask:        gpio.TXMask,
			RXInverted:    gpio.RXInverted,
			TXInverted:    gpio.TXInverted,
		},
		Counter: transport.LocalGPIOCounterDescriptor{
			Role: transport.LocalGPIORoleCounter,
			Hz:   document.Counter.Hz,
		},
		MaxLeaseTicks: document.MaxLeaseTicks,
	}, nil
}

func loadProfile(path string) (transport.LocalGPIOPlatformProfile, error) {
	file, err := os.Open(path)
	if err != nil {
		return transport.LocalGPIOPlatformProfile{}, err
	}
	// The file is read-only; a close error cannot lose profile data.
	defer func() { _ = file.Close() }()
	document, err := capture.DecodeProfile(file)
	if err != nil {
		return transport.LocalGPIOPlatformProfile{}, err
	}
	return profile(document)
}

// readOnlyWindow is the local GPIO window port over a read-only mapping. It reports
// device ordering only for a character device; a regular file never qualifies.
// It has no store path: a store attempt is counted and touches no memory.
type readOnlyWindow struct {
	mapping       *capture.Mapping
	storeAttempts uint64
}

func (current *readOnlyWindow) MappedLength() uint64 {
	return current.mapping.Length()
}

func (current *readOnlyWindow) MappedAccessWidth() uint8 {
	return uint8(capture.AccessWidth)
}

func (current *readOnlyWindow) MappedOrdering() transport.LocalGPIOOrdering {
	if current.mapping.CharacterDevice() {
		return transport.LocalGPIOOrderingDevice
	}
	return transport.LocalGPIOOrderingRelaxed
}

func (current *readOnlyWindow) Load32(offset uint64) uint32 {
	return current.mapping.Load32(offset)
}

func (current *readOnlyWindow) Store32(uint64, uint32) {
	current.storeAttempts++
}

func (current *readOnlyWindow) Unmap() error {
	return current.mapping.Unmap()
}

// countingMapper is a local GPIO mapping port that also reports store attempts
// on every window it has handed out.
type countingMapper interface {
	transport.LocalGPIOMappingPort
	storeAttempts() uint64
}

// fileMapper maps the owner-named window file. It maps only the descriptor
// length the backend has already validated.
type fileMapper struct {
	path    string
	offset  uint64
	windows []*readOnlyWindow
}

func (current *fileMapper) Map(descriptor transport.LocalGPIODescriptor) (transport.LocalGPIOWindow, error) {
	mapping, err := capture.MapReadOnly(current.path, current.offset, descriptor.MapLength)
	if err != nil {
		return nil, err
	}
	result := &readOnlyWindow{mapping: mapping}
	current.windows = append(current.windows, result)
	return result, nil
}

func (current *fileMapper) storeAttempts() uint64 {
	var total uint64
	for _, each := range current.windows {
		total += each.storeAttempts
	}
	return total
}

type request struct {
	profile   transport.LocalGPIOPlatformProfile
	responder byte
	duration  time.Duration
	maxEvents int
}

func closeBackend(backend *transport.LocalGPIOBackend) error {
	var err error
	for attempt := 0; attempt < closeAttempts; attempt++ {
		if err = backend.Close(); err == nil {
			return nil
		}
	}
	return err
}

// runCapture opens the backend, runs one bounded passive capture, closes the
// backend and writes the report. The report is written only for a capture that
// ran; it states whether every passive invariant held.
func runCapture(req request, mapping countingMapper, counter transport.LocalGPIOCounterPort, stop *atomic.Bool, out io.Writer) (int, error) {
	budget, err := capture.BudgetTicks(req.duration, req.profile.Counter.Hz)
	if err != nil {
		return exitUsage, err
	}
	recorder, err := capture.NewRecorder(req.maxEvents)
	if err != nil {
		return exitUsage, err
	}
	backend, err := transport.OpenLocalGPIO(&req.profile, mapping, counter)
	if err != nil {
		if backend != nil {
			err = errors.Join(err, closeBackend(backend))
		}
		return exitFailure, err
	}
	engine, err := backend.NewEngine(localworker.EngineConfig{
		TickHz:           req.profile.Counter.Hz,
		ResponderAddress: req.responder,
	}, recorder)
	if err != nil {
		code := exitFailure
		if errors.Is(err, localworker.ErrEngineInvalidConfig) {
			code = exitUsage
		}
		return code, errors.Join(err, closeBackend(backend))
	}
	result := capture.Run(engine, backend, recorder, budget, stop)
	closeErr := closeBackend(backend)
	stats := backend.Stats()
	summary := capture.Seal(capture.Summary{
		ProfileID:      req.profile.ProfileID,
		Family:         req.profile.Family,
		CounterHz:      req.profile.Counter.Hz,
		BudgetTicks:    budget,
		ElapsedTicks:   result.ElapsedTicks,
		Steps:          result.Steps,
		Stop:           capture.StopName(result.Stop),
		EventsRecorded: uint64(len(recorder.Events())),
		EventsRefused:  recorder.Refused(),
		Engine:         capture.EngineCountersFrom(engine.Stats()),
		Backend: capture.BackendCounters{
			StoredEdges:   stats.StoredEdges,
			RefusedEdges:  stats.RefusedEdges,
			Releases:      stats.Releases,
			LeaseExpiries: stats.LeaseExpiries,
		},
		WindowStores: mapping.storeAttempts(),
	})
	if err := capture.WriteReport(out, recorder.Events(), summary); err != nil {
		return exitFailure, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return exitFailure, closeErr
	}
	if !summary.Passive {
		return exitNotPassive, errors.New("aa29-smoke: a passive invariant did not hold")
	}
	return exitPassive, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitUsage
	}
	platformProfile, err := loadProfile(opts.profilePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "aa29-smoke: profile:", err)
		return exitUsage
	}
	var stop atomic.Bool
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		if _, ok := <-signals; ok {
			stop.Store(true)
		}
	}()
	buffered := bufio.NewWriter(stdout)
	code, err := runCapture(request{
		profile:   platformProfile,
		responder: opts.responder,
		duration:  opts.duration,
		maxEvents: opts.maxEvents,
	}, &fileMapper{path: opts.windowPath, offset: opts.windowOffset},
		capture.NewMonotonicCounter(), &stop, buffered)
	if flushErr := buffered.Flush(); flushErr != nil && code == exitPassive {
		code, err = exitFailure, flushErr
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
	}
	return code
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
