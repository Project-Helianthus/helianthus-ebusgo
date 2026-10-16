# helianthus-ebusgo

`helianthus-ebusgo` is the low-level eBUS core library for Helianthus. It provides transport adapters, wire framing, bus transaction flow, and reusable data-type codecs used by higher-level repositories.

## Purpose and scope

### What belongs in this repo

- eBUS transports (`transport/`) including ENH, ENS, ebusd-tcp, and loopback test transport.
- Wire/protocol primitives (`protocol/`) including framing, CRC handling, and bus transaction behavior.
- Reusable type codecs (`types/`), error taxonomy/normalization primitives (`errors/`), and deterministic invoke helpers (`determinism/`).
- Deterministic target emulation helpers and smoke tests (`emulation/`, `scripts/`).

### What does not belong in this repo

- GraphQL/API surfaces, registry/provider composition, or integration orchestration.
- Live deployment or production gateway runtime wiring.

Those belong in:
- `helianthus-ebusreg` (registry/provider layer),
- `helianthus-ebusgateway` (gateway runtime and live-bus smoke path).

## Status and maturity

- **Maturity:** active foundation library with stable core test coverage and ongoing incremental feature work.
- **Current posture:** suitable for contributor onboarding, protocol experimentation, and CI-validated low-level changes.
- **Operational note:** this repo focuses on deterministic library/runtime behavior; live-bus rollout validation is performed in `helianthus-ebusgateway`.

## How this fits in the Helianthus chain

```text
helianthus-ebusgo  ->  helianthus-ebusreg  ->  helianthus-ebusgateway  ->  integrations/add-ons
 (transport+bus)       (registry+schemas)      (runtime/API/smoke)
```

## Quickstart (copy/paste)

### 1) Clone and baseline checks

```bash
git clone https://github.com/d3vi1/helianthus-ebusgo.git
cd helianthus-ebusgo
go test ./...
go vet ./...
go build ./...
```

### 2) CI-parity checks

```bash
./scripts/ci_local.sh
go test -race -count=1 ./...
```

### 3) Local smoke tests (deterministic, no hardware required)

```bash
./scripts/smoke-identify-only.sh
./scripts/smoke-vr90-minimal.sh
```

### 4) TinyGo compatibility check (optional)

```bash
tinygo build -target esp32-coreboard-v2 ./cmd/tinygo-check
```

## Local smoke-test configuration examples

Use these when extending `emulation/` behavior.

### Identify-only preset example

```go
profile := emulation.PresetVR90IdentifyOnlyProfile()
target, err := emulation.NewIdentifyOnlyTarget(profile)
```

```go
profile := emulation.PresetVR92IdentifyOnlyProfile()
target, err := emulation.NewIdentifyOnlyTarget(profile)
```

### VR90 extended discovery + mapped-command example

```go
profile := emulation.DefaultVR90Profile()
profile.Address = 0x75 // Set to your installation's RCC target address
profile.EnableB509Discovery = true
profile.MappedCommands = []emulation.VR90MappedCommand{
	{
		Primary:      0xB5,
		Secondary:    0x09,
		PayloadExact: []byte{0x24},
		ResponseData: []byte{0x00, 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H'},
	},
}
target, err := emulation.NewVR90Target(profile)
```

`PayloadExact` and `PayloadPrefix` use nil to mean "no payload matcher." An
explicit non-nil empty `PayloadExact` matches only an empty request payload;
leave both fields nil for PB/SB-only matching. Existing non-empty exact and
prefix matchers keep their behavior. An explicit empty prefix matches every
payload and is equivalent to PB/SB-only matching. Configure at most one payload
matcher on each mapped command.

## Transport observations for baud-series consumers

ENH and ENS implement the optional `transport.BaudObservability` interface.
`BaudObservabilitySnapshot()` only loads instance-local counters: it takes no
I/O lock and sends no adapter request. `RawTransport` and on-wire behavior are
unchanged. Plain TCP/UDP, ebusd-tcp and loopback do not implement this surface;
consumers must treat its absence as unavailable and omit those series.

| Snapshot field | Meaning and unit |
|---|---|
| `ConfiguredBaud`, `ConfiguredBaudKnown` | Configured UART bits/s, valid only when known. ENH/ENS over `net.Conn` leave these at zero/false; omit the gauge. No default baud is assumed. |
| `ReceivedSymbolsTotal` | Count of parsed RECEIVED symbols, including compact raw bytes, escape leads/completions, idle SYNs and echoes. Counted before unescaping, arbitration/SYN suppression and queue eviction. Control/INFO frames and bytes beyond the parser's first-error boundary are excluded. |
| `ENHFramingErrorsTotal` | Number of ENH parse errors, including a trailing error when an earlier valid response lets INIT, arbitration or INFO succeed. These are host-adapter framing faults, with no UART/baud attribution. |
| `ENHParserResetsTotal` | Number of actual parser reset actions: resynchronization attempts, including normal INIT/arbitration/reconnect boundaries and timeout cleanup. This is neither confirmed recovery nor an error-only counter. |
| `AdapterResetResponsesTotal` | Number of parsed RESETTED responses, including expected INIT confirmations. This does not count only unexpected adapter reboots. |
| `EscapeDecodeFaultsTotal` | Existing `DecodeFaultTotal()` counter, in decoder fault events. |
| `EscapeRecoveriesTotal`, `EscapeBudgetExhaustedTotal` | Existing escape recovery/budget-exhaustion event counters; subsets of `EscapeDecodeFaultsTotal`. |
| `EscapePendingTimeoutsTotal` | Existing escape pending-timeout events; separate from decode faults. |

A transport session here is the lifetime of one constructed transport instance.
Counters survive INIT, adapter reset, reconnect and Close; a fresh transport
instance (including a fresh DriverRuntime generation) starts at zero. Keep the
generation with the sample so consumers can handle counter resets. Snapshot
fields are independently loaded atomics, not a transactional collection; a
concurrent scrape can briefly see a fault/subset pair at different instants.

Sample through the existing DriverRuntime admission surface to associate the
observations with the exact generation:

```go
lease, err := runtime.Admit(ctx)
if err != nil {
    return err
}
defer lease.Release()
generation := lease.Generation()
var snapshot transport.BaudObservabilitySnapshot
var available bool
err = lease.Invoke(func(raw transport.RawTransport) error {
    if provider, ok := raw.(transport.BaudObservability); ok {
        snapshot = provider.BaudObservabilitySnapshot()
        available = true
    }
    return nil
})
// Publish generation/snapshot only if err == nil && available.
// Wrappers must forward the optional interface to retain its availability.
```

A delta of `ReceivedSymbolsTotal` over scrape elapsed seconds measures
host-observed ingress throughput in symbols/s. TCP batching, adapter buffering,
idle traffic and echoes prevent interpreting it as physical baud or precise
inter-symbol timing. No hardware symbol timestamps or baud-attributable UART
framing errors are available in these transports, so this surface exposes no
such measurements. The gateway owns Prometheus names, rendering and wiring.

## Change area -> focused validation

| Change area | Focused command(s) |
|---|---|
| `transport/` | `go test ./transport -count=1` |
| `protocol/` | `go test ./protocol -count=1` |
| `types/` | `go test ./types -count=1` |
| `emulation/` | `go test ./emulation -count=1` |
| smoke scripts | `./scripts/smoke-identify-only.sh` and `./scripts/smoke-vr90-minimal.sh` |
| cross-package | `go test ./... -count=1` |

## Link map

### Key protocol docs

- eBUS overview: https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ebus-overview.md
- ENH framing: https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/enh.md
- ENS framing: https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ens.md
- ebusd TCP behavior: https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ebusd-tcp.md

### Local project pointers

- CI workflow: `.github/workflows/ci.yml`
- smoke scripts: `scripts/smoke-identify-only.sh`, `scripts/smoke-vr90-minimal.sh`
- TinyGo check entrypoint: `cmd/tinygo-check/main.go`

### Issue workflow conventions

- Use one issue-focused branch per change (example: `issue-72-readme-refresh`).
- Keep PR scope aligned to issue acceptance criteria.
- Include closing keyword in PR body (example: `Fixes #72`).
- Track roadmap, bugs, and enhancements in GitHub Issues:
  - https://github.com/d3vi1/helianthus-ebusgo/issues
