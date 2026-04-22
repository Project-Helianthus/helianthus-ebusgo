package b503

// EncodeRequest builds the 2-byte `(family, selector)` request payload that
// prefixes every B503 invocation (spec §2). The returned slice is owned by
// the caller.
//
// History reads (family=0x01) MAY extend beyond 2 bytes in a device-class
// dependent way — callers wanting to select a specific history index MUST
// consult the per-target TypeSpec/LOCAL_CAPTURE evidence and append any
// extra bytes themselves. The v1 decoder surface (spec §3) ships only the
// 2-byte baseline encoder.
func EncodeRequest(family, selector byte) []byte {
	return []byte{family, selector}
}

// EncodeCurrentError returns the request payload for selector 00 01
// (Currenterror, spec §3). Invoke-safety: READ.
func EncodeCurrentError() []byte { return EncodeRequest(0x00, 0x01) }

// EncodeErrorHistory returns the request payload for selector 01 01
// (Errorhistory, spec §3). Invoke-safety: READ. See spec §2 for the
// device-class-dependent history-index extension caveat.
func EncodeErrorHistory() []byte { return EncodeRequest(0x01, 0x01) }

// EncodeCurrentService returns the request payload for selector 00 02
// (Currentservice, spec §3). Invoke-safety: READ.
func EncodeCurrentService() []byte { return EncodeRequest(0x00, 0x02) }

// EncodeServiceHistory returns the request payload for selector 01 02
// (Servicehistory, spec §3). Invoke-safety: READ.
func EncodeServiceHistory() []byte { return EncodeRequest(0x01, 0x02) }

// EncodeLiveMonitorMain returns the request payload for selector 00 03
// (HMU LiveMonitorMain, spec §3). Invoke-safety: SERVICE_WRITE — the
// gateway MUST acquire liveMonitorMu and respect the session FSM (spec §6)
// before emitting this frame.
func EncodeLiveMonitorMain() []byte { return EncodeRequest(0x00, 0x03) }

// Intentionally absent (plan AD02, spec §9):
//   - EncodeClearErrorHistory (selector 02 01)
//   - EncodeClearServiceHistory (selector 02 02)
//
// Install-writes are NOT exposed on any public surface in v1; adding an
// encode helper here would undermine the invariant audited by
// TestNoInstallWriteHelpers.
