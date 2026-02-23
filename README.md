# helianthus-ebusgo

`helianthus-ebusgo` is the low-level eBUS core library for Helianthus. It provides transport adapters, wire framing, bus transaction flow, and reusable data-type codecs used by higher-level repositories.

## Purpose and scope

### What belongs in this repo

- eBUS transports (`transport/`) including ENH, ENS, ebusd-tcp, and loopback test transport.
- Wire/protocol primitives (`protocol/`) including framing, CRC handling, and bus transaction behavior.
- Reusable type codecs (`types/`) and error taxonomy/normalization primitives (`errors/`).
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
