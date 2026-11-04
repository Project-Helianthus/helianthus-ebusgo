//go:build linux && arm64 && aa29_local_gpio

// Command aa29-smoke runs one bounded passive capture over the AA29 local GPIO
// backend and writes the decoded engine events as JSON lines.
//
// Status: Hypothesis. The command supplies the AA29 GPIO window and counter
// ports: a read-only shared mapping of an owner-named file and the process
// monotonic clock. It never arms the backend and never offers a prepared
// response, and the window it maps has no write permission, so it cannot drive
// the line. Physical timing and the electrical frontend remain Unknown.
//
// Usage:
//
//	aa29-smoke -profile FILE -window FILE [-window-offset N] -responder ADDR
//	           [-duration D] [-max-events N]
//
// The profile is an owner-provisioned AA29 platform profile in JSON form whose
// counter rate must be the monotonic counter rate. The window file and offset
// are owner-provisioned; the command has no default for either.
//
// Exit status: 0 passive capture completed, 1 runtime failure, 2 usage, 3 a
// passive invariant did not hold.
package main

import (
	aa31bufio "bufio"
	aa31errors "errors"
	aa31flag "flag"
	aa31fmt "fmt"
	aa31io "io"
	aa31os "os"
	aa31signal "os/signal"
	aa31strconv "strconv"
	aa31atomic "sync/atomic"
	aa31syscall "syscall"
	aa31time "time"

	aa31capture "github.com/Project-Helianthus/helianthus-ebusgo/cmd/aa29-smoke/internal/aa31capture"
	aa31localworker "github.com/Project-Helianthus/helianthus-ebusgo/localworker"
	aa31transport "github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

const (
	aa31ExitPassive    = 0
	aa31ExitFailure    = 1
	aa31ExitUsage      = 2
	aa31ExitNotPassive = 3

	aa31DefaultDuration  = 10 * aa31time.Second
	aa31DefaultMaxEvents = 1 << 16
	aa31CloseAttempts    = 2
)

var aa31ErrUsage = aa31errors.New("aa29-smoke: usage") //nolint:staticcheck // ST1012: AA31 prefix is mandatory

type aa31Options struct {
	aa31ProfilePath  string
	aa31WindowPath   string
	aa31WindowOffset uint64
	aa31Responder    byte
	aa31Duration     aa31time.Duration
	aa31MaxEvents    int
}

// aa31ParseOptions requires the profile, window and responder flags; nothing
// that names a resource has a default.
func aa31ParseOptions(aa31Args []string, aa31Stderr aa31io.Writer) (aa31Options, error) {
	aa31Flags := aa31flag.NewFlagSet("aa29-smoke", aa31flag.ContinueOnError)
	aa31Flags.SetOutput(aa31Stderr)
	var aa31Result aa31Options
	var aa31Responder string
	aa31Flags.StringVar(&aa31Result.aa31ProfilePath, "profile", "", "owner-provisioned AA29 platform profile (JSON)")
	aa31Flags.StringVar(&aa31Result.aa31WindowPath, "window", "", "owner-provisioned file holding the GPIO control window")
	aa31Flags.Uint64Var(&aa31Result.aa31WindowOffset, "window-offset", 0, "page-aligned offset of the window in that file")
	aa31Flags.StringVar(&aa31Responder, "responder", "", "target address the engine tags frames for (for example 0x15)")
	aa31Flags.DurationVar(&aa31Result.aa31Duration, "duration", aa31DefaultDuration, "capture budget")
	aa31Flags.IntVar(&aa31Result.aa31MaxEvents, "max-events", aa31DefaultMaxEvents, "recorder capacity")
	if aa31Err := aa31Flags.Parse(aa31Args); aa31Err != nil {
		return aa31Options{}, aa31fmt.Errorf("%w: %w", aa31ErrUsage, aa31Err)
	}
	if aa31Flags.NArg() != 0 {
		return aa31Options{}, aa31fmt.Errorf("%w: unexpected argument %q", aa31ErrUsage, aa31Flags.Arg(0))
	}
	if aa31Result.aa31ProfilePath == "" || aa31Result.aa31WindowPath == "" || aa31Responder == "" {
		return aa31Options{}, aa31fmt.Errorf("%w: -profile, -window and -responder are required", aa31ErrUsage)
	}
	aa31Address, aa31Err := aa31strconv.ParseUint(aa31Responder, 0, 8)
	if aa31Err != nil {
		return aa31Options{}, aa31fmt.Errorf("%w: -responder: %w", aa31ErrUsage, aa31Err)
	}
	aa31Result.aa31Responder = byte(aa31Address)
	return aa31Result, nil
}

func aa31Ordering(aa31Label string) aa31transport.AA29Ordering {
	switch aa31Label {
	case "relaxed":
		return aa31transport.AA29OrderingRelaxed
	case "device":
		return aa31transport.AA29OrderingDevice
	}
	return aa31transport.AA29OrderingUnknown
}

// aa31Profile converts a decoded document field for field. Roles are fixed by
// position. Admission stays with AA29Open.
func aa31Profile(aa31Document aa31capture.AA31ProfileDocument) (aa31transport.AA29PlatformProfile, error) {
	aa31Qualification, aa31Err := aa31capture.AA31ParseQualification(aa31Document.AA31Qualification)
	if aa31Err != nil {
		return aa31transport.AA29PlatformProfile{}, aa31Err
	}
	aa31Digest, aa31Err := aa31capture.AA31ParseDigest(aa31Document.AA31QualificationDigest)
	if aa31Err != nil {
		return aa31transport.AA29PlatformProfile{}, aa31Err
	}
	aa31GPIO := aa31Document.AA31GPIO
	return aa31transport.AA29PlatformProfile{
		AA29ProfileID:           aa31Document.AA31ProfileID,
		AA29Family:              aa31Document.AA31Family,
		AA29Qualification:       aa31Qualification,
		AA29QualificationDigest: aa31Digest,
		AA29GPIO: aa31transport.AA29GPIODescriptor{
			AA29Role:          aa31transport.AA29RoleGPIOControl,
			AA29MapLength:     aa31GPIO.AA31MapLength,
			AA29AccessWidth:   aa31GPIO.AA31AccessWidth,
			AA29Ordering:      aa31Ordering(aa31GPIO.AA31Ordering),
			AA29RXLevelOffset: aa31GPIO.AA31RXLevelOffset,
			AA29TXSetOffset:   aa31GPIO.AA31TXSetOffset,
			AA29TXClearOffset: aa31GPIO.AA31TXClearOffset,
			AA29RXMask:        aa31GPIO.AA31RXMask,
			AA29TXMask:        aa31GPIO.AA31TXMask,
			AA29RXInverted:    aa31GPIO.AA31RXInverted,
			AA29TXInverted:    aa31GPIO.AA31TXInverted,
		},
		AA29Counter: aa31transport.AA29CounterDescriptor{
			AA29Role: aa31transport.AA29RoleCounter,
			AA29Hz:   aa31Document.AA31Counter.AA31Hz,
		},
		AA29MaxLeaseTicks: aa31Document.AA31MaxLeaseTicks,
	}, nil
}

func aa31LoadProfile(aa31Path string) (aa31transport.AA29PlatformProfile, error) {
	aa31File, aa31Err := aa31os.Open(aa31Path)
	if aa31Err != nil {
		return aa31transport.AA29PlatformProfile{}, aa31Err
	}
	// The file is read-only; a close error cannot lose profile data.
	defer func() { _ = aa31File.Close() }()
	aa31Document, aa31Err := aa31capture.AA31DecodeProfile(aa31File)
	if aa31Err != nil {
		return aa31transport.AA29PlatformProfile{}, aa31Err
	}
	return aa31Profile(aa31Document)
}

// aa31Window is the AA29 window port over a read-only mapping. It reports
// device ordering only for a character device; a regular file never qualifies.
// It has no store path: a store attempt is counted and touches no memory.
type aa31Window struct {
	aa31Mapping       *aa31capture.AA31Mapping
	aa31StoreAttempts uint64
}

func (aa31Current *aa31Window) AA29MappedLength() uint64 {
	return aa31Current.aa31Mapping.AA31Length()
}

func (aa31Current *aa31Window) AA29MappedAccessWidth() uint8 {
	return uint8(aa31capture.AA31AccessWidth)
}

func (aa31Current *aa31Window) AA29MappedOrdering() aa31transport.AA29Ordering {
	if aa31Current.aa31Mapping.AA31CharacterDevice() {
		return aa31transport.AA29OrderingDevice
	}
	return aa31transport.AA29OrderingRelaxed
}

func (aa31Current *aa31Window) AA29Load32(aa31Offset uint64) uint32 {
	return aa31Current.aa31Mapping.AA31Load32(aa31Offset)
}

func (aa31Current *aa31Window) AA29Store32(uint64, uint32) {
	aa31Current.aa31StoreAttempts++
}

func (aa31Current *aa31Window) AA29Unmap() error {
	return aa31Current.aa31Mapping.AA31Unmap()
}

// aa31CountingMapper is an AA29 mapping port that also reports store attempts
// on every window it has handed out.
type aa31CountingMapper interface {
	aa31transport.AA29MappingPort
	aa31StoreAttempts() uint64
}

// aa31Mapper maps the owner-named window file. It maps only the descriptor
// length the backend has already validated.
type aa31Mapper struct {
	aa31Path    string
	aa31Offset  uint64
	aa31Windows []*aa31Window
}

func (aa31Current *aa31Mapper) AA29Map(aa31Descriptor aa31transport.AA29GPIODescriptor) (aa31transport.AA29Window, error) {
	aa31Mapping, aa31Err := aa31capture.AA31MapReadOnly(aa31Current.aa31Path, aa31Current.aa31Offset, aa31Descriptor.AA29MapLength)
	if aa31Err != nil {
		return nil, aa31Err
	}
	aa31Result := &aa31Window{aa31Mapping: aa31Mapping}
	aa31Current.aa31Windows = append(aa31Current.aa31Windows, aa31Result)
	return aa31Result, nil
}

func (aa31Current *aa31Mapper) aa31StoreAttempts() uint64 {
	var aa31Total uint64
	for _, aa31Each := range aa31Current.aa31Windows {
		aa31Total += aa31Each.aa31StoreAttempts
	}
	return aa31Total
}

type aa31Request struct {
	aa31Profile   aa31transport.AA29PlatformProfile
	aa31Responder byte
	aa31Duration  aa31time.Duration
	aa31MaxEvents int
}

func aa31CloseBackend(aa31Backend *aa31transport.AA29Backend) error {
	var aa31Err error
	for aa31Attempt := 0; aa31Attempt < aa31CloseAttempts; aa31Attempt++ {
		if aa31Err = aa31Backend.AA29Close(); aa31Err == nil {
			return nil
		}
	}
	return aa31Err
}

// aa31Capture opens the backend, runs one bounded passive capture, closes the
// backend and writes the report. The report is written only for a capture that
// ran; it states whether every passive invariant held.
func aa31Capture(aa31Req aa31Request, aa31Mapping aa31CountingMapper, aa31Counter aa31transport.AA29CounterPort, aa31Stop *aa31atomic.Bool, aa31Out aa31io.Writer) (int, error) {
	aa31Budget, aa31Err := aa31capture.AA31BudgetTicks(aa31Req.aa31Duration, aa31Req.aa31Profile.AA29Counter.AA29Hz)
	if aa31Err != nil {
		return aa31ExitUsage, aa31Err
	}
	aa31Recorder, aa31Err := aa31capture.AA31NewRecorder(aa31Req.aa31MaxEvents)
	if aa31Err != nil {
		return aa31ExitUsage, aa31Err
	}
	aa31Backend, aa31Err := aa31transport.AA29Open(&aa31Req.aa31Profile, aa31Mapping, aa31Counter)
	if aa31Err != nil {
		if aa31Backend != nil {
			aa31Err = aa31errors.Join(aa31Err, aa31CloseBackend(aa31Backend))
		}
		return aa31ExitFailure, aa31Err
	}
	aa31Engine, aa31Err := aa31Backend.AA29NewEngine(aa31localworker.AA25Config{
		AA25TickHz:           aa31Req.aa31Profile.AA29Counter.AA29Hz,
		AA25ResponderAddress: aa31Req.aa31Responder,
	}, aa31Recorder)
	if aa31Err != nil {
		aa31Code := aa31ExitFailure
		if aa31errors.Is(aa31Err, aa31localworker.AA25ErrInvalidConfig) {
			aa31Code = aa31ExitUsage
		}
		return aa31Code, aa31errors.Join(aa31Err, aa31CloseBackend(aa31Backend))
	}
	aa31Result := aa31capture.AA31Run(aa31Engine, aa31Backend, aa31Recorder, aa31Budget, aa31Stop)
	aa31CloseErr := aa31CloseBackend(aa31Backend)
	aa31Stats := aa31Backend.AA29Stats()
	aa31Summary := aa31capture.AA31Seal(aa31capture.AA31Summary{
		AA31ProfileID:      aa31Req.aa31Profile.AA29ProfileID,
		AA31Family:         aa31Req.aa31Profile.AA29Family,
		AA31CounterHz:      aa31Req.aa31Profile.AA29Counter.AA29Hz,
		AA31BudgetTicks:    aa31Budget,
		AA31ElapsedTicks:   aa31Result.AA31ElapsedTicks,
		AA31Steps:          aa31Result.AA31Steps,
		AA31Stop:           aa31capture.AA31StopName(aa31Result.AA31Stop),
		AA31EventsRecorded: uint64(len(aa31Recorder.AA31Events())),
		AA31EventsRefused:  aa31Recorder.AA31Refused(),
		AA31Engine:         aa31capture.AA31EngineCountersFrom(aa31Engine.AA25Stats()),
		AA31Backend: aa31capture.AA31BackendCounters{
			AA31StoredEdges:   aa31Stats.AA29StoredEdges,
			AA31RefusedEdges:  aa31Stats.AA29RefusedEdges,
			AA31Releases:      aa31Stats.AA29Releases,
			AA31LeaseExpiries: aa31Stats.AA29LeaseExpiries,
		},
		AA31WindowStores: aa31Mapping.aa31StoreAttempts(),
	})
	if aa31Err := aa31capture.AA31WriteReport(aa31Out, aa31Recorder.AA31Events(), aa31Summary); aa31Err != nil {
		return aa31ExitFailure, aa31errors.Join(aa31Err, aa31CloseErr)
	}
	if aa31CloseErr != nil {
		return aa31ExitFailure, aa31CloseErr
	}
	if !aa31Summary.AA31Passive {
		return aa31ExitNotPassive, aa31errors.New("aa29-smoke: a passive invariant did not hold")
	}
	return aa31ExitPassive, nil
}

func aa31Main(aa31Args []string, aa31Stdout, aa31Stderr aa31io.Writer) int {
	aa31Opts, aa31Err := aa31ParseOptions(aa31Args, aa31Stderr)
	if aa31Err != nil {
		_, _ = aa31fmt.Fprintln(aa31Stderr, aa31Err)
		return aa31ExitUsage
	}
	aa31PlatformProfile, aa31Err := aa31LoadProfile(aa31Opts.aa31ProfilePath)
	if aa31Err != nil {
		_, _ = aa31fmt.Fprintln(aa31Stderr, "aa29-smoke: profile:", aa31Err)
		return aa31ExitUsage
	}
	var aa31Stop aa31atomic.Bool
	aa31Signals := make(chan aa31os.Signal, 1)
	aa31signal.Notify(aa31Signals, aa31os.Interrupt, aa31syscall.SIGTERM)
	defer aa31signal.Stop(aa31Signals)
	go func() {
		if _, aa31OK := <-aa31Signals; aa31OK {
			aa31Stop.Store(true)
		}
	}()
	aa31Buffered := aa31bufio.NewWriter(aa31Stdout)
	aa31Code, aa31Err := aa31Capture(aa31Request{
		aa31Profile:   aa31PlatformProfile,
		aa31Responder: aa31Opts.aa31Responder,
		aa31Duration:  aa31Opts.aa31Duration,
		aa31MaxEvents: aa31Opts.aa31MaxEvents,
	}, &aa31Mapper{aa31Path: aa31Opts.aa31WindowPath, aa31Offset: aa31Opts.aa31WindowOffset},
		aa31capture.AA31NewMonotonicCounter(), &aa31Stop, aa31Buffered)
	if aa31FlushErr := aa31Buffered.Flush(); aa31FlushErr != nil && aa31Code == aa31ExitPassive {
		aa31Code, aa31Err = aa31ExitFailure, aa31FlushErr
	}
	if aa31Err != nil {
		_, _ = aa31fmt.Fprintln(aa31Stderr, aa31Err)
	}
	return aa31Code
}

func main() {
	aa31os.Exit(aa31Main(aa31os.Args[1:], aa31os.Stdout, aa31os.Stderr))
}
