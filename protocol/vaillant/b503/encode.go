package b503

import (
	"errors"
	"fmt"
)

// ErrInstallWriteForbidden is returned by EncodeRequest when a caller supplies
// a `(family, selector)` tuple classified as INSTALL_WRITE (spec §4, plan
// AD02). v1 MUST NOT expose any install-write encoding path on public
// surfaces; this error enforces the invariant at the earliest call site.
var ErrInstallWriteForbidden = errors.New("b503: install-write selector forbidden on public surface (plan AD02, spec §9)")

// ErrUnknownSelector is returned by EncodeRequest when a caller supplies a
// tuple that is not in the §3 catalog. Unknown selectors are refused at the
// encoder boundary so contract violations surface early rather than as
// silently-serialized nonsense frames on the bus.
var ErrUnknownSelector = errors.New("b503: unknown (family, selector) — not in spec §3 catalog")

// EncodeRequest builds the 2-byte `(family, selector)` request payload that
// prefixes every B503 invocation (spec §2).
//
// The encoder refuses:
//   - install-write tuples (`02 01`, `02 02`) with ErrInstallWriteForbidden —
//     v1 invariant per plan AD02 / spec §9;
//   - tuples absent from the §3 catalog with ErrUnknownSelector.
//
// History reads (family=0x01) MAY extend beyond 2 bytes in a device-class
// dependent way — callers wanting to select a specific history index MUST
// consult the per-target TypeSpec/LOCAL_CAPTURE evidence and append any
// extra bytes themselves. The v1 encoder surface (spec §3) ships only the
// 2-byte baseline.
func EncodeRequest(family, selector byte) ([]byte, error) {
	safety, ok := Safety(family, selector)
	if !ok {
		return nil, fmt.Errorf("%w: (%#x,%#x)", ErrUnknownSelector, family, selector)
	}
	if safety == SafetyInstallWrite {
		return nil, fmt.Errorf("%w: (%#x,%#x)", ErrInstallWriteForbidden, family, selector)
	}
	return []byte{family, selector}, nil
}

// mustEncode is an internal helper for the per-selector convenience functions
// below; it panics on error because all known-safe call sites pass hard-coded
// READ/SERVICE_WRITE tuples from the §3 catalog. A panic here would indicate
// a programming error in this package, not caller misuse.
func mustEncode(family, selector byte) []byte {
	b, err := EncodeRequest(family, selector)
	if err != nil {
		panic(fmt.Sprintf("b503: internal: mustEncode(%#x,%#x) failed: %v", family, selector, err))
	}
	return b
}

// EncodeCurrentError returns the request payload for selector 00 01
// (Currenterror, spec §3). Invoke-safety: READ.
func EncodeCurrentError() []byte { return mustEncode(0x00, 0x01) }

// EncodeErrorHistory returns the request payload for selector 01 01
// (Errorhistory, spec §3). Invoke-safety: READ. See spec §2 for the
// device-class-dependent history-index extension caveat.
func EncodeErrorHistory() []byte { return mustEncode(0x01, 0x01) }

// EncodeCurrentService returns the request payload for selector 00 02
// (Currentservice, spec §3). Invoke-safety: READ.
func EncodeCurrentService() []byte { return mustEncode(0x00, 0x02) }

// EncodeServiceHistory returns the request payload for selector 01 02
// (Servicehistory, spec §3). Invoke-safety: READ.
func EncodeServiceHistory() []byte { return mustEncode(0x01, 0x02) }

// EncodeLiveMonitorMain returns the request payload for selector 00 03
// (HMU LiveMonitorMain, spec §3). Invoke-safety: SERVICE_WRITE — the
// gateway MUST acquire liveMonitorMu and respect the session FSM (spec §6)
// before emitting this frame.
func EncodeLiveMonitorMain() []byte { return mustEncode(0x00, 0x03) }

// Intentionally absent (plan AD02, spec §9):
//   - EncodeClearErrorHistory (selector 02 01)
//   - EncodeClearServiceHistory (selector 02 02)
//
// The generic EncodeRequest also refuses these tuples at runtime; the dual
// guard (source-file absence + runtime check) is audited by
// TestNoInstallWriteHelpers and TestEncodeRequest_RejectsInstallWrite.
