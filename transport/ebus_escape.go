package transport

import "fmt"

// EbusEscapeDecoder unescapes eBUS byte-stuffing as documented in
// john30/ebusd/docs/enhanced_proto.md:
//
//	wire 0xA9 0x00 → logical 0xA9 (data byte equal to ESC)
//	wire 0xA9 0x01 → logical 0xAA (data byte equal to SYN value)
//	any other byte → passes through unchanged
//
// The decoder is stateful — the `escape` flag carries across calls so a
// pair split between Read() boundaries decodes correctly on the next
// byte. The decoder does NOT own the byte stream: callers invoke Push
// once per wire byte, and Reset() clears in-flight state at transport
// lifecycle boundaries (reconnect, RESETTED frame, surface reset).
//
// Concurrency: the decoder is NOT safe for concurrent use. The ENH
// transport invokes it under readMu, which already serializes byte
// ingest. Adding a mutex here would be redundant for that caller and
// strictly worse for other potential callers; expect external
// serialization.
//
// Background (F-23, batch-19, 2026-05-13): before this decoder existed
// the ENH transport claimed via BytesAreUnescaped() to deliver logical
// bytes but actually forwarded wire bytes unchanged. Consumers (passive
// bus tap, reconstructor) saw bare 0xA9 followed by either 0x00 or
// 0x01 — those two-byte sequences leaked into the request/response
// buffers and produced false unexpected_symbol abandons whenever a
// legitimate frame contained a logical 0xA9 or 0xAA byte (Patterns A
// and B in batch-19's verification report). This decoder makes the
// BytesAreUnescaped() contract honest.
type EbusEscapeDecoder struct {
	escape bool
}

// Push consumes one wire byte and returns the decoded result.
//
// Return values:
//
//	decoded     — the logical byte to emit (valid only when ok=true)
//	ok          — true if a logical byte is ready; false while still
//	              accumulating (the escape lead 0xA9 was just consumed)
//	wasEscaped  — true if `decoded` came from a wire escape pair
//	              (0xA9 0x00 → 0xA9 or 0xA9 0x01 → 0xAA); false for a
//	              raw passthrough byte
//	err         — non-nil iff a 0xA9 lead was followed by an invalid
//	              second byte (anything other than 0x00 or 0x01); the
//	              decoder clears its `escape` state before returning so
//	              the next call resumes cleanly on the next wire byte
//
// On err != nil the caller should count the fault and continue;
// emission of the offending pair is suppressed (ok=false, decoded
// undefined). The next Push resumes clean decoding.
func (d *EbusEscapeDecoder) Push(raw byte) (decoded byte, ok bool, wasEscaped bool, err error) {
	if d.escape {
		// We previously consumed a 0xA9 lead; `raw` is the second byte
		// of the escape pair. Clear the flag first so a malformed pair
		// (returning an error) does not strand us mid-pair on the next
		// call — see error-resume note in the func docstring.
		d.escape = false
		switch raw {
		case 0x00:
			return 0xA9, true, true, nil
		case 0x01:
			return 0xAA, true, true, nil
		default:
			return 0, false, false, fmt.Errorf("ebus escape: 0xA9 0x%02X is not a valid pair (expected 0x00 or 0x01)", raw)
		}
	}
	if raw == 0xA9 {
		// Escape lead. Buffer the state; do not emit yet.
		d.escape = true
		return 0, false, false, nil
	}
	return raw, true, false, nil
}

// Reset clears any in-flight escape state. Call at every transport
// lifecycle boundary that invalidates an in-progress wire pair:
// reconnect, adapter-surfaced RESETTED frame, or any other state
// discard. Do NOT call on read timeout — a 0xA9 lead may have arrived
// just before the timeout and its pair may legitimately arrive on the
// next read.
func (d *EbusEscapeDecoder) Reset() {
	d.escape = false
}

// HasPendingEscape reports whether the decoder is mid-pair (a 0xA9
// lead has been consumed and we are awaiting the second byte). Useful
// for diagnostics and for callers that want to flag mid-frame
// interruption when a transport-level boundary fires while the
// decoder is mid-pair.
func (d *EbusEscapeDecoder) HasPendingEscape() bool {
	return d.escape
}
