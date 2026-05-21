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

	// StreamEventWireSyn is a passive bus-idle marker (wire 0xAA SYN)
	// surfaced during an in-flight async RequestStart's `awaitingStart`
	// window. Distinct from StreamEventByte so that ReadByte (the
	// active sender's echo-wait drain) NEVER returns it — only ReadEvent
	// consumers (e.g. a downstream bus reconstructor that needs to
	// observe SYN cadence to keep its receive-state-machine in
	// `bs_ready`) see these markers.
	//
	// F-38-fix (PR #155 P1, 2026-05-15): the original F-38 emitted
	// pre-grant SYNs as StreamEventByte, which polluted the
	// pendingEvents queue drained by ReadByte. After the eventual
	// STARTED arrived, the active sender's first echo wait would
	// return the stale 0xAA as the first echo and `sendRawWithEcho`'s
	// collision guard would reject it as an unexpected wire SYN.
	// Routing through this dedicated kind keeps the F-38 contract
	// (downstream consumers see SYN cadence) without breaking the
	// active-sender echo invariant.
	StreamEventWireSyn
)

// StreamEvent is a transport-stream item. Byte is valid for
// StreamEventByte. Data is valid for StreamEventStarted (confirmed
// initiator address) and StreamEventFailed (winner address).
//
// WasEscaped (F-23, batch-19, 2026-05-13) is valid for StreamEventByte
// only. It carries the wire-side truth flag for the emitted logical
// byte: true means the byte was decoded from an eBUS escape pair
// (wire 0xA9 0x00 → logical 0xA9 with WasEscaped=true; wire 0xA9 0x01
// → logical 0xAA with WasEscaped=true); false means the byte passed
// through unchanged as a raw wire byte. Transports that already
// deliver logical bytes (i.e. BytesAreUnescaped() returns true) MUST
// populate this field on every StreamEventByte emission so consumers
// can distinguish a real wire SYN (0xAA, WasEscaped=false) from an
// escape-decoded logical 0xAA carrying user payload (WasEscaped=true).
// Transports that deliver raw wire bytes leave the field at its
// zero value (false).
type StreamEvent struct {
	Kind       StreamEventKind
	Byte       byte // valid for StreamEventByte
	Data       byte // valid for StreamEventStarted, StreamEventFailed
	WasEscaped bool // valid for StreamEventByte; see F-23 docstring above
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

// EscapeFlaggedReader is an optional extension (F-23, batch-19, 2026-05-13)
// implemented by transports whose ReadByte stream may legitimately contain
// the SYN value (0xAA) as user payload — distinguishable only by the
// upstream WasEscaped flag.
//
// Callers that interpret raw 0xAA as a structural marker (e.g. the
// protocol layer's waitForSyn idle-detection) MUST use this method on
// any transport that implements it; otherwise an escape-decoded payload
// 0xAA (originally wire `0xA9 0x01`) would falsely satisfy SYN-counting
// logic and let the bus arbitration retry path proceed while traffic is
// still in progress (Codex bot review on Project-Helianthus/helianthus-ebusgo#154).
//
// Transports that already track escape state internally and never
// surface a logical 0xAA without provenance (e.g. plain TCP / UDP, the
// ebusd_tcp adapter) do NOT need to implement this — the protocol
// layer's plain-transport path handles those correctly via the
// `prevWasEscape` heuristic inside waitForSyn.
type EscapeFlaggedReader interface {
	// ReadByteWithEscape behaves like ReadByte but additionally
	// returns the WasEscaped flag for the emitted byte: true means
	// the byte was decoded from an eBUS escape pair (wire 0xA9 0x00
	// → logical 0xA9 or 0xA9 0x01 → logical 0xAA) and is therefore
	// user payload, not a wire-layer structural marker.
	ReadByteWithEscape() (byte, bool, error)
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

// PostGrantWindowExpiredReporter is an optional extension implemented
// by transports that maintain a post-grant pre-echo SYN-suppression
// window (currently: ENH transport). Returns the cumulative count of
// times the window closed via DEADLINE EXPIRY (as opposed to first-
// real-echo arrival or lifecycle reset).
//
// Consumers (gateway adaptermux) correlate the post-window-close
// idle 0xAA SYN arrivals with `echo_mismatch` events in the next
// gateway transaction. F-XX (batch-22 Attack 2 instrumentation,
// 2026-05-15). Forensic-only — no behavior implication.
//
// Transports without a post-grant window (plain TCP/UDP, ebusd_tcp)
// do NOT need to implement this; the gateway falls back to leaving
// the diagnostic counter at zero.
type PostGrantWindowExpiredReporter interface {
	PostGrantWindowExpiredCount() uint64
}

// StructuralWriteSignaler is an optional extension (F-NEW-28,
// 2026-05-21) implemented by transports that need to distinguish, on
// the write side, a STRUCTURAL `SymbolSyn` (raw end-of-message
// terminator, wire-form 0xAA with no escape) from a PAYLOAD byte that
// happens to equal `SymbolSyn` (wire-form 0xA9 0x01, expected echo is
// logical 0xAA with WasEscaped=true).
//
// Why this matters — round-9 leak at the gateway adaptermux:
//
//	The gateway's per-byte echo tracker (echo_tracker.go) records
//	the expected echo BYTE for each write, but NOT the expected
//	WasEscaped flag. When the next-pending byte is `SymbolSyn`,
//	the mux cannot tell whether bus.Send is waiting for a
//	structural terminator (legitimate raw 0xAA echo) or for a
//	payload-0xAA echo (logical 0xAA, WasEscaped=true).
//
//	Under v8-enforce mode the mux suppresses wire AUTO-SYNs that
//	arrive mid-payload-write (`midWriteSyn` predicate at
//	`mux.go:2487`), but only when the next-pending byte is
//	NON-SYN. For payload-0xAA writes the next-pending byte IS
//	SymbolSyn, the suppressor doesn't fire, and a wire AUTO-SYN
//	can land in echo position — tripping the round-9 absorb path
//	at `bus.go:1204` and failing the transaction with a terminal
//	read timeout.
//
//	The fix: bus.go signals "this next single-byte Write is a
//	structural SYN" before calling `Write([]byte{SymbolSyn})` on
//	transports that implement this interface. The transport
//	propagates the flag through to the gateway's echo tracker,
//	which uses it in the SYN-suppression predicate.
//
// Contract:
//
//   - `SignalNextWriteIsStructuralSyn` MUST be called at most ONCE
//     per Write call, IMMEDIATELY before the Write. The signaled
//     state is consumed by the next Write and reset to the default
//     ("payload") afterwards.
//   - bus.go calls this ONLY when issuing a structural SymbolSyn
//     write (i.e. `sendRawWithEcho(... raw=SymbolSyn,
//     expectRawSyn=true)`). All other writes (payload bytes, including
//     payload-0xAA writes that go through the escape pair on plain
//     transports) leave the signaler at its default state.
//   - Transports that do NOT implement this interface are not affected;
//     callers MUST gracefully handle missing implementation
//     (interface-assertion + nil check). On such transports the
//     round-9 leak remains the canonical filter, per v8 §1.8.
//
// This is a WRITE-side mirror of the READ-side `WasEscaped` flag
// surfaced by `StreamEvent` and `EscapeFlaggedReader`. The pair
// closes the F-NEW-28 round-9 enforce-mode invariant: under v8
// enforce, `helianthus_round9_absorb_entered_total` is expected to
// stay flat per `HelianthusRound9FiredUnderProxy`.
type StructuralWriteSignaler interface {
	// SignalNextWriteIsStructuralSyn marks the next Write call on
	// this transport as a structural SymbolSyn (end-of-message
	// terminator). The flag is one-shot — consumed by the next
	// Write and reset to default ("payload") afterwards.
	//
	// Safe to call from the goroutine that subsequently issues
	// Write. Concurrent callers MUST serialize per the transport
	// implementation's own concurrency contract; in practice
	// bus.Send.sendRawWithEcho is single-threaded for the lifetime
	// of one telegram, so no extra synchronization is required at
	// the caller.
	SignalNextWriteIsStructuralSyn()
}
