package errors

import (
	stderrors "errors"
	"strings"
)

var (
	ErrBusCollision = stderrors.New("ebus: bus collision during arbitration")
	ErrTimeout      = stderrors.New("ebus: no response within timeout window")
	ErrCRCMismatch  = stderrors.New("ebus: CRC validation failed")
	ErrNACK         = stderrors.New("ebus: target returned NACK")
	ErrNoSuchDevice = stderrors.New("ebus: no device responded at address")

	ErrRetryExhausted    = stderrors.New("ebus: retries exhausted")
	ErrInvalidPayload    = stderrors.New("ebus: payload does not match expected schema")
	ErrTransportClosed   = stderrors.New("ebus: transport connection closed")
	ErrAdapterReset      = stderrors.New("ebus: adapter reset during operation")
	ErrAdapterHostError  = stderrors.New("ebus: adapter host-side protocol error")
	ErrQueueFull         = stderrors.New("ebus: send queue at capacity")
)

type Code string

const (
	CodeUnknown         Code = "UNKNOWN"
	CodeInvalidPayload  Code = "INVALID_PAYLOAD"
	CodeNoSuchDevice    Code = "NO_SUCH_DEVICE"
	CodeNACK            Code = "NACK"
	CodeTimeout         Code = "TIMEOUT"
	CodeBusCollision    Code = "BUS_COLLISION"
	CodeRetryExhausted  Code = "RETRY_EXHAUSTED"
	CodeCRCMismatch     Code = "CRC_MISMATCH"
	CodeTransportClosed   Code = "TRANSPORT_CLOSED"
	CodeAdapterReset      Code = "ADAPTER_RESET"
	CodeAdapterHostError  Code = "ADAPTER_HOST_ERROR"
	CodeQueueFull         Code = "QUEUE_FULL"
)

type Category string

const (
	CategoryUnknown    Category = "UNKNOWN"
	CategoryInvalid    Category = "INVALID"
	CategoryTransient  Category = "TRANSIENT"
	CategoryDefinitive Category = "DEFINITIVE"
	CategoryFatal      Category = "FATAL"
)

type SourceLayer string

const (
	SourceLayerUnknown SourceLayer = "unknown"
	SourceLayerEbusgo  SourceLayer = "ebusgo"
	SourceLayerEbusreg SourceLayer = "ebusreg"
	SourceLayerGateway SourceLayer = "gateway"
)

type Mapping struct {
	Code        Code
	Category    Category
	Retriable   bool
	SourceLayer SourceLayer
}

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

func NormalizeErrorCategory(err error) Category {
	return categoryForCode(NormalizeErrorCode(err))
}

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

func IsTransient(err error) bool {
	return stderrors.Is(err, ErrBusCollision) ||
		stderrors.Is(err, ErrTimeout) ||
		stderrors.Is(err, ErrCRCMismatch) ||
		stderrors.Is(err, ErrRetryExhausted) ||
		stderrors.Is(err, ErrAdapterReset)
}

func IsDefinitive(err error) bool {
	return stderrors.Is(err, ErrNoSuchDevice) ||
		stderrors.Is(err, ErrNACK) ||
		stderrors.Is(err, ErrAdapterHostError) ||
		stderrors.Is(err, ErrInvalidPayload)
}

func IsFatal(err error) bool {
	return stderrors.Is(err, ErrTransportClosed)
}
