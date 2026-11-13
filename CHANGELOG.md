# Changelog

## Unreleased

- Rename the externally visible work-item-coded strings of the local worker,
  the local GPIO transport and the passive-capture smoke command directly,
  with no alias, no dual acceptance and no migration step:
  - build tag: `aa29_local_gpio` → `local_gpio` (transport and smoke command;
    build with `go build -tags local_gpio ./cmd/passive-capture-smoke`);
  - local GPIO platform-profile digest domain: `aa29.profile.1` →
    `local_gpio.profile.1`. Every profile digest changes, so every
    owner-provisioned platform profile must be digested again before the next
    RAW trial;
  - smoke command name, flag-set name and message prefix: `aa29-smoke` →
    `passive-capture-smoke` (`aa29-smoke: usage`, `aa29-smoke: profile:`,
    `aa29-smoke: a passive invariant did not hold`, `aa29-smoke: profile
    document invalid`, `aa29-smoke: capture limits invalid`, `aa29-smoke:
    read-only mapping refused` and `aa29-smoke: built without linux/arm64 and
    the aa29_local_gpio tag; refusing to run` take the new prefix and tag);
  - `-profile` flag help: `owner-provisioned AA29 platform profile (JSON)` →
    `owner-provisioned local GPIO platform profile (JSON)`;
  - engine errors: `localworker: aa25 invalid config` → `localworker: engine
    invalid config`, `localworker: aa25 missing port` → `localworker: engine
    missing port`, `localworker: aa25 invalid prepared response` →
    `localworker: engine invalid prepared response`;
  - responder errors: `localworker: aa26 invalid responder configuration` →
    `localworker: responder invalid configuration`, and `localworker: aa26 …`
    → `localworker: responder …` for `worker, stream or clock domain
    mismatch`, `plan reference is not the adopted plan`, `wire authority is
    held`, `no matching initiator hold`, `wire port refused`, `clock
    regression` and `address not admitted`;
  - local GPIO transport errors: `transport: aa29 …` → `transport: local
    GPIO …` for `platform profile missing`, `platform profile not qualified`,
    `resource descriptor invalid`, `control resource missing`, `control
    resource does not match`, `lease not admitted`, `backend closed` and
    `mapping port refused the descriptor`.
- Rename the work-item-coded local worker, local GPIO transport and smoke
  command files and identifiers to behavior names. The command directory
  `cmd/aa29-smoke` becomes `cmd/passive-capture-smoke` (its internal `capture`
  package moves with it; the file names inside are unchanged) and the ignored
  built binary is `/passive-capture-smoke`. Files in `localworker` and
  `transport`: `aa23_plans.go` → `plans.go`, `aa23_plans_test.go` →
  `plans_test.go`, `aa24_ipc.go` → `ipc.go`, `aa24_ipc_test.go` →
  `ipc_test.go`, `aa25_engine.go` → `engine.go`, `aa25_engine_test.go` →
  `engine_test.go`, `aa26_responder.go` → `responder.go`,
  `aa26_responder_test.go` → `responder_test.go`,
  `aa29_local_gpio_linux_arm64.go` → `local_gpio_linux_arm64.go`,
  `aa29_local_gpio_linux_arm64_test.go` → `local_gpio_linux_arm64_test.go`.
  Identifiers drop the `AA23`, `AA24`, `AA25`, `AA26` and `AA29` codes (for
  example `AA23Registry` → `Registry`, `ErrAA24Full` → `ErrFull`,
  `AA25NewEngine` → `NewEngine`, `AA26ErrWireBusy` → `ErrWireBusy`, `aa24Lane`
  → `lane`). Package-level names of the local GPIO backend take `LocalGPIO`
  instead of the code (`AA29Backend` → `LocalGPIOBackend`,
  `AA29PlatformProfile` → `LocalGPIOPlatformProfile`, `AA29GPIODescriptor` →
  `LocalGPIODescriptor`, `AA29ErrClosed` → `ErrLocalGPIOClosed`, `AA29Open` →
  `OpenLocalGPIO`), while their fields and methods drop it (`AA29Stats()` →
  `Stats()`, `AA29Lease.AA29ProfileDigest` → `LocalGPIOLease.ProfileDigest`).
  Exported package-level names that would collide inside `localworker` take
  the area of their file (Plan, IPC, Engine, Responder): `AA23Limits` →
  `PlanLimits`, `AA24Config` → `IPCConfig`, `AA24Limits` → `IPCLimits`,
  `AA25Config` → `EngineConfig`, `AA25ErrInvalidConfig` →
  `ErrEngineInvalidConfig`, `AA25Stats` → `EngineStats`, `AA26Config` →
  `ResponderConfig`, `AA26ErrClock` → `ErrResponderClock`, `AA26ErrIdentity` →
  `ErrResponderIdentity`, `AA26ErrInvalidConfig` →
  `ErrResponderInvalidConfig`, `AA26Stats` → `ResponderStats`,
  `ErrAA23Capacity` → `ErrPlanCapacity`, `ErrAA23Expired` → `ErrPlanExpired`,
  `ErrAA23InvalidLimits` → `ErrPlanInvalidLimits`, `ErrAA23Oversize` →
  `ErrPlanOversize`, `ErrAA23Revoked` → `ErrPlanRevoked`, `ErrAA24Capacity` →
  `ErrIPCCapacity`, `ErrAA24Clock` → `ErrIPCClock`, `ErrAA24Expired` →
  `ErrIPCExpired`, `ErrAA24Identity` → `ErrIPCIdentity`,
  `ErrAA24InvalidLimits` → `ErrIPCInvalidLimits`, `ErrAA24Oversize` →
  `ErrIPCOversize`, `ErrAA24Revoked` → `ErrIPCRevoked`; the fields and methods
  that share these tokens drop the code, and unexported collisions follow the
  same rule. Import aliases that only carried the code are removed, comments
  name the area instead of the code, and test messages lose their leading code
  tag; test function names are unchanged. The `aa29_local_gpio` build tag, the
  `aa29.profile.1` digest domain, the flags, the report JSON keys and every
  usage and error text are unchanged. `scripts/rename_local_worker_names.py`
  reproduces the change and prints the full identifier map with `--print-map`.

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
