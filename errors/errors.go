package errors

import (
	stderrors "errors"
	"strings"
)

var (
	// ErrBusCollision reports that this side lost arbitration or detected a
	// collision with another participant's traffic. It is transient: a later
	// attempt may succeed.
	ErrBusCollision = stderrors.New("ebus: bus collision during arbitration")
	// ErrTimeout reports that an expected byte, acknowledgement or response
	// did not arrive in time. It is transient.
	ErrTimeout = stderrors.New("ebus: no response within timeout window")
	// ErrCRCMismatch reports a frame whose checksum does not match its
	// bytes. It is transient.
	ErrCRCMismatch = stderrors.New("ebus: CRC validation failed")
	// ErrNACK reports that the addressed participant answered with a
	// negative acknowledgement. It is definitive: repeating the same request
	// is not expected to change the answer.
	ErrNACK = stderrors.New("ebus: target returned NACK")
	// ErrNoSuchDevice reports that no participant answered at the target
	// address. It is definitive.
	ErrNoSuchDevice = stderrors.New("ebus: no device responded at address")

	// ErrRetryExhausted reports that every permitted attempt failed. It is
	// classified as transient.
	ErrRetryExhausted = stderrors.New("ebus: retries exhausted")
	// ErrInvalidPayload reports input or received data that does not have
	// the expected length, range or shape, including values that a codec
	// cannot encode. It is in the invalid category.
	ErrInvalidPayload = stderrors.New("ebus: payload does not match expected schema")
	// ErrTransportClosed reports that the transport, or the bus running on
	// it, has been closed. It is fatal: requests keep failing until a new one
	// is opened.
	ErrTransportClosed = stderrors.New("ebus: transport connection closed")
	// ErrAdapterReset reports that the bus adapter reset while an operation
	// was in progress. It is transient.
	ErrAdapterReset = stderrors.New("ebus: adapter reset during operation")
	// ErrAdapterHostError reports an error that the adapter signalled about
	// the host side of the adapter protocol. It is definitive.
	ErrAdapterHostError = stderrors.New("ebus: adapter host-side protocol error")
	// ErrQueueFull reports that a request was refused because the send queue
	// is at capacity; nothing was sent. It is in the invalid category and is
	// not retriable by NormalizeErrorMapping.
	ErrQueueFull = stderrors.New("ebus: send queue at capacity")
)

// Code is the stable string name of a recognized sentinel, as returned by
// NormalizeErrorCode.
type Code string

// Error codes, one per sentinel of this package, plus CodeUnknown for an
// error that wraps none of them.
const (
	CodeUnknown          Code = "UNKNOWN"
	CodeInvalidPayload   Code = "INVALID_PAYLOAD"
	CodeNoSuchDevice     Code = "NO_SUCH_DEVICE"
	CodeNACK             Code = "NACK"
	CodeTimeout          Code = "TIMEOUT"
	CodeBusCollision     Code = "BUS_COLLISION"
	CodeRetryExhausted   Code = "RETRY_EXHAUSTED"
	CodeCRCMismatch      Code = "CRC_MISMATCH"
	CodeTransportClosed  Code = "TRANSPORT_CLOSED"
	CodeAdapterReset     Code = "ADAPTER_RESET"
	CodeAdapterHostError Code = "ADAPTER_HOST_ERROR"
	CodeQueueFull        Code = "QUEUE_FULL"
)

// Category groups error codes by what a caller should do next; see
// NormalizeErrorCategory for the table.
type Category string

// Error categories. CategoryInvalid means the request itself must change,
// CategoryTransient that the same request may succeed later,
// CategoryDefinitive that the answer will not change, CategoryFatal that
// the transport is unusable and CategoryUnknown that the error is not
// recognized.
const (
	CategoryUnknown    Category = "UNKNOWN"
	CategoryInvalid    Category = "INVALID"
	CategoryTransient  Category = "TRANSIENT"
	CategoryDefinitive Category = "DEFINITIVE"
	CategoryFatal      Category = "FATAL"
)

// SourceLayer names the software layer an error is attributed to.
type SourceLayer string

// Source layers: this eBUS library, the eBUS registry, the gateway and its
// API surfaces, or unknown.
const (
	SourceLayerUnknown SourceLayer = "unknown"
	SourceLayerEbusgo  SourceLayer = "ebusgo"
	SourceLayerEbusreg SourceLayer = "ebusreg"
	SourceLayerGateway SourceLayer = "gateway"
)

// Mapping is the full classification of one error, as returned by
// NormalizeErrorMapping. Retriable is true only for CategoryTransient.
type Mapping struct {
	Code        Code
	Category    Category
	Retriable   bool
	SourceLayer SourceLayer
}

// NormalizeErrorCode returns the code of the first sentinel that err wraps,
// tested with errors.Is in a fixed order: ErrInvalidPayload, ErrNoSuchDevice,
// ErrNACK, ErrTimeout, ErrBusCollision, ErrRetryExhausted, ErrCRCMismatch,
// ErrTransportClosed, ErrAdapterReset, ErrAdapterHostError, ErrQueueFull.
// An error that wraps several sentinels gets the earliest one's code; nil
// and unrecognized errors give CodeUnknown.
func NormalizeErrorCode(err error) Code {
	switch {
	case stderrors.Is(err, ErrInvalidPayload):
		return CodeInvalidPayload
	case stderrors.Is(err, ErrNoSuchDevice):
		return CodeNoSuchDevice
	case stderrors.Is(err, ErrNACK):
		return CodeNACK
	case stderrors.Is(err, ErrTimeout):
		return CodeTimeout
	case stderrors.Is(err, ErrBusCollision):
		return CodeBusCollision
	case stderrors.Is(err, ErrRetryExhausted):
		return CodeRetryExhausted
	case stderrors.Is(err, ErrCRCMismatch):
		return CodeCRCMismatch
	case stderrors.Is(err, ErrTransportClosed):
		return CodeTransportClosed
	case stderrors.Is(err, ErrAdapterReset):
		return CodeAdapterReset
	case stderrors.Is(err, ErrAdapterHostError):
		return CodeAdapterHostError
	case stderrors.Is(err, ErrQueueFull):
		return CodeQueueFull
	default:
		return CodeUnknown
	}
}

// NormalizeErrorCategory returns the category of NormalizeErrorCode(err):
// invalid for invalid payload and full queue; definitive for no such device,
// NACK and adapter host error; transient for timeout, bus collision,
// exhausted retries, CRC mismatch and adapter reset; fatal for a closed
// transport; unknown otherwise.
func NormalizeErrorCategory(err error) Category {
	return categoryForCode(NormalizeErrorCode(err))
}

// NormalizeSourceLayer maps a caller-supplied source name, compared after
// trimming and lower-casing, to a layer: "ebusgo", "ebus-go" and "ebus_go"
// to SourceLayerEbusgo; "ebusreg", "ebus-reg", "ebus_reg" and "registry" to
// SourceLayerEbusreg; "gateway", "api", "graphql" and "mcp" to
// SourceLayerGateway; anything else, including the empty string, to
// SourceLayerUnknown.
func NormalizeSourceLayer(source string) SourceLayer {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "ebusgo", "ebus-go", "ebus_go":
		return SourceLayerEbusgo
	case "ebusreg", "ebus-reg", "ebus_reg", "registry":
		return SourceLayerEbusreg
	case "gateway", "api", "graphql", "mcp":
		return SourceLayerGateway
	default:
		return SourceLayerUnknown
	}
}

// NormalizeErrorMapping classifies err and attributes it to source. When
// source is blank and err wraps a known sentinel, the layer is
// SourceLayerEbusgo, because the sentinels belong to this library; a blank
// source with an unrecognized error, or a source name that
// NormalizeSourceLayer does not know, gives SourceLayerUnknown.
func NormalizeErrorMapping(err error, source string) Mapping {
	code := NormalizeErrorCode(err)
	category := categoryForCode(code)
	normalizedSource := strings.TrimSpace(source)
	layer := NormalizeSourceLayer(normalizedSource)
	if code != CodeUnknown && normalizedSource == "" {
		layer = SourceLayerEbusgo
	}

	return Mapping{
		Code:        code,
		Category:    category,
		Retriable:   category == CategoryTransient,
		SourceLayer: layer,
	}
}

func categoryForCode(code Code) Category {
	switch code {
	case CodeInvalidPayload, CodeQueueFull:
		return CategoryInvalid
	case CodeNoSuchDevice, CodeNACK, CodeAdapterHostError:
		return CategoryDefinitive
	case CodeTimeout, CodeBusCollision, CodeRetryExhausted, CodeCRCMismatch, CodeAdapterReset:
		return CategoryTransient
	case CodeTransportClosed:
		return CategoryFatal
	default:
		return CategoryUnknown
	}
}

// IsTransient reports whether err wraps ErrBusCollision, ErrTimeout,
// ErrCRCMismatch, ErrRetryExhausted or ErrAdapterReset. It matches the
// transient category.
func IsTransient(err error) bool {
	return stderrors.Is(err, ErrBusCollision) ||
		stderrors.Is(err, ErrTimeout) ||
		stderrors.Is(err, ErrCRCMismatch) ||
		stderrors.Is(err, ErrRetryExhausted) ||
		stderrors.Is(err, ErrAdapterReset)
}

// IsDefinitive reports whether err wraps ErrNoSuchDevice, ErrNACK,
// ErrAdapterHostError or ErrInvalidPayload. Unlike the category table it
// also counts ErrInvalidPayload, whose category is invalid.
func IsDefinitive(err error) bool {
	return stderrors.Is(err, ErrNoSuchDevice) ||
		stderrors.Is(err, ErrNACK) ||
		stderrors.Is(err, ErrAdapterHostError) ||
		stderrors.Is(err, ErrInvalidPayload)
}

// IsFatal reports whether err wraps ErrTransportClosed.
func IsFatal(err error) bool {
	return stderrors.Is(err, ErrTransportClosed)
}
