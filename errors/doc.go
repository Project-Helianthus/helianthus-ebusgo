// Package errors defines the sentinel errors shared by every layer of the
// eBUS library and maps them to stable codes, categories and source layers.
// It owns the vocabulary that callers use to decide whether to retry, give
// up or report a failure; it does not create or wrap errors itself.
//
// Callers match sentinels with the standard errors.Is, so wrapped errors
// keep their classification. NormalizeErrorCode tests the sentinels in a
// fixed order and returns the first match; an error that wraps none of them
// maps to CodeUnknown and CategoryUnknown. Code, Category and SourceLayer
// values are stable strings intended for logs and API surfaces.
//
// Categories: invalid input (ErrInvalidPayload, ErrQueueFull), definitive
// answers that a retry will not change (ErrNoSuchDevice, ErrNACK,
// ErrAdapterHostError), transient failures (ErrTimeout, ErrBusCollision,
// ErrRetryExhausted, ErrCRCMismatch, ErrAdapterReset) and fatal failures
// (ErrTransportClosed). Mapping.Retriable is true only for the transient
// category.
//
// The predicates IsTransient, IsDefinitive and IsFatal test sentinels
// directly and are not derived from the category table: IsDefinitive also
// reports true for ErrInvalidPayload, and ErrQueueFull satisfies none of
// them.
//
// NormalizeErrorMapping attributes a recognized error to this library when
// no source is given; otherwise NormalizeSourceLayer maps the caller's
// source name and returns SourceLayerUnknown for names it does not know.
package errors
