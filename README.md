# helianthus-ebusgo

`helianthus-ebusgo` is the low-level eBUS core for the Helianthus stack. It provides byte transports, frame encoding/decoding, bus arbitration + retries, and reusable eBUS data-type codecs.

## Purpose and Audience

This repository is for:

- engineers implementing eBUS integrations in Go,
- maintainers extending transport/protocol behavior, and
- operators validating transport-level behavior before wiring higher-level services.

This repository is **not** a full gateway daemon. API endpoints, registry/projections, and smoke orchestration live in sibling repos (`helianthus-ebusgateway`, `helianthus-ebusreg`).

## Architecture Summary

Core layering in this repo:

1. `transport` — `RawTransport` implementations (`ENH`, `ENS`, `ebusd-tcp`, loopback).
2. `protocol` — frame model, CRC handling, and prioritized `Bus` send/receive state machine.
3. `types` — eBUS codecs (`EXP`, `BCD`, `DATA1b`, `DATA2b`, `DATA2c`, `WORD`, structured types) with replacement-value semantics.
4. `errors` — sentinel errors + helpers (`IsTransient`, `IsDefinitive`, `IsFatal`) for policy decisions.

## Prerequisites

- Go `1.22+`
- Git
- Optional (for TinyGo compatibility checks): TinyGo `0.40.1` (matches CI)
- Optional (for live transport validation): access to an ENH/ENS endpoint or ebusd TCP command port

## Quick Start (Clean Machine)

```bash
git clone https://github.com/d3vi1/helianthus-ebusgo.git
cd helianthus-ebusgo

# Build all packages
go build ./...

# Run unit tests
go test ./...
```

CI-parity local checks:

```bash
go vet ./...
go test -race -count=1 ./...
```

Optional TinyGo check (same target pattern as CI):

```bash
tinygo build -target esp32-coreboard-v2 ./cmd/tinygo-check
```

## Smoke-Test Context and Limits

- This repo focuses on deterministic library/unit behavior; it does **not** include a hardware smoke runner.
- Real-bus smoke/integration flow is run from `helianthus-ebusgateway` (`cmd/smoke`) and documented here:
  - https://github.com/d3vi1/helianthus-docs-ebus/blob/main/development/smoke-test.md
- `transport/ebusd_tcp.go` is built only on non-TinyGo targets (`//go:build !tinygo`).

## Package Map

| Package | Role | Typical use |
|---|---|---|
| `errors` | Sentinel error taxonomy + classifiers | Retry/backoff/failure policy |
| `transport` | Byte-level transport adapters | Connect ENH/ENS/ebusd-tcp endpoints |
| `protocol` | eBUS frame model + `Bus` queue/state machine | Send frames with typed retry semantics |
| `types` | eBUS value codecs | Decode/encode payload fields |
| `cmd/tinygo-check` | TinyGo compile probe | Validate package compatibility on embedded targets |
| `internal/crc` | CRC implementation details | Internal-only support package |

## Common Workflows

### 1) Work on protocol retry/arbitration logic

```bash
go test ./protocol -count=1
```

### 2) Work on transport parsers/backends (including ebusd-tcp fixtures)

```bash
go test ./transport -count=1
go test ./transport -run EbusdTCP -count=1
```

### 3) Integrate bus send/receive in your own process

```go
conn, err := net.Dial("unix", "/var/run/ebusd/ebusd.socket")
if err != nil { /* handle */ }
defer conn.Close()

tr := transport.NewENHTransport(conn, 5*time.Second, 5*time.Second)
if err := tr.Init(0x00); err != nil { /* handle */ }

bus := protocol.NewBus(tr, protocol.DefaultBusConfig(), 0)
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
bus.Run(ctx)

resp, err := bus.Send(ctx, protocol.Frame{
    Source: 0x31, Target: 0x08, Primary: 0x07, Secondary: 0x04, Data: nil,
})
_ = resp
_ = err
```

Tip: use a bounded context (`context.WithTimeout`) for `Send` calls on multi-master buses.

## Troubleshooting

| Symptom / error | Likely cause | Practical fix |
|---|---|---|
| `ErrTimeout` | target did not answer, or timeout too short | verify address/telegram and increase read timeout / request context |
| `ErrBusCollision` | arbitration lost to another master | retry with bounded context; verify master address and bus contention |
| `ErrCRCMismatch` | framing/escaping mismatch or corrupted response | ensure transport matches endpoint (`enh` vs `ens` vs `ebusd-tcp`) |
| `ErrInvalidPayload` | malformed adapter data or wrong backend assumption | confirm backend protocol and command endpoint; inspect raw response fixture |
| `ErrTransportClosed` | socket/connection dropped | reconnect, recreate transport, then recreate/restart `Bus` |

If you need policy-based behavior:

- `errors.IsTransient(err)` → safe to retry
- `errors.IsDefinitive(err)` → target/protocol-level negative outcome
- `errors.IsFatal(err)` → transport/payload setup issue

## Docs / CI / Issues

- CI workflow: `.github/workflows/ci.yml`
- Protocol references:
  - https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ebus-overview.md
  - https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/enh.md
  - https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ens.md
  - https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ebusd-tcp.md
- Roadmap / bugs / support: https://github.com/d3vi1/helianthus-ebusgo/issues
- GitHub Releases: none published at the moment (consume by module commit/tag as needed).
