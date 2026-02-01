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
