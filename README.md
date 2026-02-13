# helianthus-ebusgo

`helianthus-ebusgo` is the low-level eBUS core for the Helianthus stack: byte transports, frame encode/decode, bus arbitration + retries, and reusable eBUS data-type codecs.

## Scope

- In scope: `transport`, `protocol`, `types`, `emulation`, `errors`.
- Out of scope: gateway APIs/projections/smoke orchestration (`helianthus-ebusgateway`, `helianthus-ebusreg`).

## Role / Use-Case Entry Paths

| Role / use case | Start path(s) | First command |
|---|---|---|
| Integrate bus send/receive in a Go process | `transport/`, `protocol/bus.go`, `protocol/protocol.go` | `go test ./protocol -count=1` |
| Extend transport behavior (ENH/ENS/ebusd-tcp/loopback) | `transport/` | `go test ./transport -count=1` |
| Add/adjust eBUS value codecs | `types/` | `go test ./types -count=1` |
| Build deterministic target emulation tests | `emulation/` | `go test ./emulation -count=1` |
| Check embedded compile compatibility | `cmd/tinygo-check/` | `tinygo build -target esp32-coreboard-v2 ./cmd/tinygo-check` |

## Transport Decision Matrix

| Transport | Choose it when | Constraints / notes | Entry constructor |
|---|---|---|---|
| `ENH` | Adapter/socket speaks ENH command framing and you want explicit init/arbitration semantics | Non-TinyGo build (`transport/enh_transport.go` has `//go:build !tinygo`); call `Init(features)` before normal traffic | `transport.NewENHTransport(conn, readTimeout, writeTimeout)` |
| `ENS` | Endpoint is ENS-escaped raw byte stream (no ENH control channel) | Non-TinyGo build (`transport/ens_transport.go` has `//go:build !tinygo`) | `transport.NewENSTransport(conn, readTimeout, writeTimeout)` |
| `ebusd-tcp` | You only have ebusd command TCP access and need functional request/response bridging | Non-TinyGo build (`transport/ebusd_tcp.go` has `//go:build !tinygo`); uses ebusd `hex` command flow; good for functional checks, not cycle-accurate timing/emulation | `transport.NewEbusdTCPTransport(conn)` |
| `loopback` | Unit tests, local simulation, deterministic in-memory transport | No external endpoint/hardware; not a real bus adapter | `transport.NewLoopback()` |

Protocol references:

- https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/enh.md
- https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ens.md
- https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ebusd-tcp.md

## Minimal Quick Start (first successful run)

```bash
git clone https://github.com/d3vi1/helianthus-ebusgo.git
cd helianthus-ebusgo
go test ./...
./scripts/smoke-identify-only.sh
./scripts/smoke-vr90-minimal.sh
```

Optional CI-parity checks:

```bash
go vet ./...
go test -race -count=1 ./...
```

Hardware smoke is intentionally out of scope in this repo. For live-bus smoke flow, use `helianthus-ebusgateway` (`cmd/smoke`) and:

- https://github.com/d3vi1/helianthus-docs-ebus/blob/main/development/smoke-test.md

## Change Area -> Test Matrix

| Change area | Focused test command(s) |
|---|---|
| `transport/enh*.go` | `go test ./transport -run ENH -count=1` |
| `transport/ens*.go` | `go test ./transport -run ENS -count=1` |
| `transport/ebusd_tcp*.go` | `go test ./transport -run EbusdTCP -count=1` |
| `protocol/` | `go test ./protocol -count=1` |
| `types/` | `go test ./types -count=1` |
| `emulation/` | `go test ./emulation -count=1` + `./scripts/smoke-identify-only.sh` + `./scripts/smoke-vr90-minimal.sh` |
| Cross-package behavior | `go test ./... -count=1` |

## Troubleshooting Pointers

| Symptom / error | Likely cause | Practical pointer |
|---|---|---|
| `ErrTimeout` | No response or timeout too aggressive | Validate target address/query and increase transport read timeout or request context timeout |
| `ErrBusCollision` | Arbitration lost to another initiator | Retry with bounded context; verify initiator address and bus contention |
| `ErrCRCMismatch` / `ErrInvalidPayload` | Wrong transport expectation or malformed escaped payload | Re-check chosen transport against matrix above (`ENH` vs `ENS` vs `ebusd-tcp`) and inspect fixture/raw bytes |
| `ErrTransportClosed` | Connection dropped/closed | Reconnect, recreate transport, then recreate/restart `protocol.Bus` |

Error policy helpers:

- `errors.IsTransient(err)` → retry/backoff candidate
- `errors.IsDefinitive(err)` → protocol/target negative outcome
- `errors.IsFatal(err)` → setup/transport/payload issue

## Docs / CI / Issues

- CI workflow: `.github/workflows/ci.yml`
- eBUS overview: https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ebus-overview.md
- Roadmap / bugs / support: https://github.com/d3vi1/helianthus-ebusgo/issues
