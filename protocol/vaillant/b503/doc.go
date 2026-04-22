// Package b503 is the M1_DECODER deliverable for cruise-run
// Project-Helianthus/helianthus-execution-plans#19 (plan
// vaillant-b503-namespace-w17-26, canonical SHA 896a82e7).
//
// Scope and design are locked by decisions AD01..AD05 in
// `helianthus-execution-plans/vaillant-b503-namespace-w17-26.locked/10-scope-decisions.md`:
//
//   - AD01: this package is a parallel top-level namespace under
//     `protocol/vaillant/b503`; it does not share code with
//     `protocol/ebus_standard/*` and carries no cross-protocol knowledge.
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
//	protocols/vaillant/ebus-vaillant-B503.md
//
// Canonical plan SHA-256:
//
//	896a82e720b33eefb449ea532570e0a962bfa76504519996825f13d92ec9bb28
package b503
