// Package b503 decodes and encodes the Vaillant B503 error and
// service-message namespace.
//
// Scope and design are locked by decisions AD01..AD05:
//
//   - AD01: this package is a parallel top-level namespace under
//     `protocol/vaillant/b503`; it does not share code with
//     `protocol/ebusstandard/*` and carries no cross-protocol knowledge.
//   - AD02: install-write selectors `02 01` (Clearerrorhistory) and `02 02`
//     (Clearservicehistory) are classified for awareness via [Safety] but no
//     encode helper is provided. Install-writes are not exposed on any
//     surface in v1 — see spec §9.
//   - AD03: requests are modelled as a `(family, selector)` tuple matching
//     the observed wire shape; the 2-byte baseline is implemented. History
//     reads MAY extend beyond the baseline in a device-class-dependent way
//     (spec §2) — only the baseline ships in v1.
//   - AD04: invoke-safety classes — READ, SERVICE_WRITE, INSTALL_WRITE — are
//     exposed via [InvokeSafety] and [Safety]. The live-monitor session
//     model is gateway-side and is not implemented in this package.
//   - AD05: no cross-device F.xxx translation table is provided; decoded
//     error/service slot values are raw LE uint16 with 0xFFFF as the empty
//     sentinel (spec §10).
//
// The normative L7 specification for every decode/encode rule implemented
// here lives in helianthus-docs-ebus:
//
//	protocols/vaillant/ebus-vaillant-b503.md
package b503
