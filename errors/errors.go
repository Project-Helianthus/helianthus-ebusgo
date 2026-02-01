package errors

import stderrors "errors"

var (
	ErrBusCollision = stderrors.New("ebus: bus collision during arbitration")
	ErrTimeout      = stderrors.New("ebus: no response within timeout window")
	ErrCRCMismatch  = stderrors.New("ebus: CRC validation failed")
	ErrNACK         = stderrors.New("ebus: slave returned NACK")
	ErrNoSuchDevice = stderrors.New("ebus: no device responded at address")

	ErrRetryExhausted  = stderrors.New("ebus: retries exhausted")
	ErrInvalidPayload  = stderrors.New("ebus: payload does not match expected schema")
	ErrTransportClosed = stderrors.New("ebus: transport connection closed")
)

func IsTransient(err error) bool {
	return stderrors.Is(err, ErrBusCollision) ||
		stderrors.Is(err, ErrTimeout) ||
		stderrors.Is(err, ErrCRCMismatch) ||
		stderrors.Is(err, ErrRetryExhausted)
}

func IsDefinitive(err error) bool {
	return stderrors.Is(err, ErrNoSuchDevice) ||
		stderrors.Is(err, ErrNACK)
}

func IsFatal(err error) bool {
	return stderrors.Is(err, ErrTransportClosed) ||
		stderrors.Is(err, ErrInvalidPayload)
}
