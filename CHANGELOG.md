# Changelog

## Unreleased

- Add package documentation in new comment-only `doc.go` files for the root
  package `ebusgo` and the packages `determinism`, `emulation`, `errors`,
  `internal/crc` and `types`. Each file holds only the package comment and
  the package clause; no code, API or protocol behavior changes.

- Rename the passive-capture smoke command internals under `cmd/aa29-smoke`
  to behavior names. The internal package `internal/aa31capture` becomes
  `internal/capture`. Files: `aa31_smoke_linux_arm64.go` →
  `smoke_linux_arm64.go`, `aa31_smoke_linux_arm64_test.go` →
  `smoke_linux_arm64_test.go`, `aa31_smoke_unsupported.go` →
  `smoke_unsupported.go`, and in the package `aa31_capture.go` →
  `capture.go`, `aa31_capture_test.go` → `capture_test.go`,
  `aa31_mapping_unix.go` → `mapping_unix.go`, `aa31_mapping_unix_test.go` →
  `mapping_unix_test.go`. Identifiers drop the `AA31`/`aa31` prefix (for
  example `AA31DecodeProfile` → `DecodeProfile`, `AA31ErrProfile` →
  `ErrProfile`, `aa31ParseOptions` → `parseOptions`), import aliases that only
  carried the prefix are removed, and the exceptions are `aa31Capture` →
  `runCapture`, `aa31Main` → `run`, the window type `aa31Window` →
  `readOnlyWindow`, the mapper type `aa31Mapper` → `fileMapper` and
  `aa31Case` → `tc`. Test function names are unchanged; the two tests that
  required the prefix on every identifier are removed. The command directory,
  the `aa29_local_gpio` build tag, the flags, the report JSON keys and every
  usage and error text are unchanged.
  `scripts/rename_smoke_capture_names.py` reproduces the change.

- Rename `protocol/telegram_fsm` to `protocol/telegramfsm` and
  `protocol/ebus_standard` to `protocol/ebusstandard`. The `types` package is
  retained. Consumers must update imports; the old paths have no forwarding
  packages. Protocol bytes and native data are unchanged.
