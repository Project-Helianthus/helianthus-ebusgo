package transport

// RawTransport is the low-level byte transport for eBUS communication.
// Implementations provide blocking reads for single bytes, buffered writes,
// and a Close method to release the underlying resource.
//
// ReadByte should return ebuserrors.ErrTransportClosed (wrapped) when the
// transport has been closed by the peer or locally.
type RawTransport interface {
	// ReadByte blocks until a byte is available or an error occurs.
	ReadByte() (byte, error)
	// Write sends raw bytes to the bus.
	Write([]byte) (int, error)
	// Close releases the underlying transport resources.
	Close() error
}

// StreamEventKind identifies optional non-byte transport events surfaced to
// passive consumers that need reset/lifecycle boundaries.
type StreamEventKind uint8

const (
	StreamEventByte StreamEventKind = iota + 1
	StreamEventReset
	StreamEventStarted // adapter confirmed START arbitration
	StreamEventFailed  // adapter rejected START arbitration
)

// StreamEvent is a transport-stream item. Byte is valid for
// StreamEventByte. Data is valid for StreamEventStarted (confirmed
// initiator address) and StreamEventFailed (winner address).
type StreamEvent struct {
	Kind StreamEventKind
	Byte byte // valid for StreamEventByte
	Data byte // valid for StreamEventStarted, StreamEventFailed
}

// StreamEventReader is an optional extension implemented by transports that
// can surface non-byte stream boundaries, such as adapter RESETTED frames,
// without changing RawTransport ReadByte compatibility for active callers.
type StreamEventReader interface {
	ReadEvent() (StreamEvent, error)
}

// InfoRequester is an optional extension implemented by transports that support
// enhanced protocol INFO queries for adapter hardware telemetry and identity.
// Plain TCP and UDP transports do not implement this interface.
type InfoRequester interface {
	RequestInfo(id AdapterInfoID) ([]byte, error)
}

// EscapeAware is implemented by transports that deliver already-unescaped
// bytes. When BytesAreUnescaped returns true, the protocol layer must skip
// wire-level escape decoding (0xA9 sequences) on both read and write paths.
type EscapeAware interface {
	BytesAreUnescaped() bool
}

// Reconnectable is an optional extension implemented by transports that can
// tear down and re-establish their underlying connection mid-session. This is
// used by the protocol layer to recover from dead TCP connections (timeout
// exhaustion) without restarting the entire bus lifecycle.
//
// Reconnect closes the current connection, dials a new one, and performs any
// required handshake (e.g. ENH INIT). It returns ErrTransportClosed (wrapped)
// if reconnection fails.
type Reconnectable interface {
	Reconnect() error
}
