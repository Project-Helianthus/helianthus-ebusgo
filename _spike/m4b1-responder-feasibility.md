# M4b1 — Responder Transport Feasibility Spike

**Meta-run:** Project-Helianthus/helianthus-execution-plans#14
**Plan:** `ebus-standard-l7-services-w16-26.locked` §14
**Role:** DEVELOPER (spike; no mergeable production code)
**Repo:** `helianthus-ebusgo`
**Branch:** `spike/m4b1-responder-feasibility`
**Status:** DONE — findings only

---

## 1. Scope & Question

Can `helianthus-ebusgo`'s transport layer support **local-slave-address
receive + reply** (eBUS responder mode), specifically the four substrate
capabilities required by the locked plan (deprecated NM plan §6B, promoted to
`ebus_standard` §14 / M4b1):

1. **Detect** incoming eBUS frames whose `ZZ` target byte equals the
   gateway's configured local slave address.
2. **Emit ACK** (`0x00`) to the initiator within the bus-timing window.
3. **Emit a slave response** segment (`NN DB1..DBn CRC`).
4. **Receive the final initiator ACK** that closes the transaction.

Transports surveyed: `ENH`, `ENS` (both implemented by `ENHTransport`),
`ebusd-tcp`. `tcp_plain`, `udp_plain`, and `loopback` are also noted for
completeness.

---

## 2. Summary Verdict

| Transport | Verdict | Notes |
| --- | --- | --- |
| **ENH** | **PARTIAL** | Byte-level substrate present; no responder API; no local-slave dispatch; echo/arbitration state machine is initiator-only. Bounded new code. |
| **ENS** | **PARTIAL** | Constructed via `NewENSTransport` → identical `ENHTransport` (`arbitrationSendsSource=true`). Same gaps as ENH. Same bounded fix. |
| **ebusd-tcp** | **BLOCKED** | Semantic-command bridge, not a wire transport. `Write()` maps outbound telegrams to `hex -s SRC <bytes>` commands; inbound traffic is synthesised locally in reply to our own writes. It has no ingress path for third-party telegrams addressed to us, and `ebusd` itself owns the bus timing. Plan hypothesis confirmed. |
| `tcp_plain` | N/A (out of locked scope) | Raw-byte passthrough; would inherit the same upper-layer gap as ENH and additionally lacks adapter-level arbitration. Not a responder target in the plan. |
| `udp_plain` | N/A (out of locked scope) | Same as tcp_plain. |
| `loopback` | N/A | Test-only. |

**Overall:** M4b2 is expected to return **GO-WITH-WORK** scoped to
`ENH`/`ENS` only. `ebusd-tcp` is explicitly out of the responder matrix —
this aligns with the plan's hypothesis and with the locked transport-gate
(META memory: "gateway must not query ebusd when transport is not
ebusd-tcp" / ebusd isolation policy).

---

## 3. Survey of Existing Transport Code

Source tree (prod only, no tests):

- `transport/transport.go` — `RawTransport` interface (`ReadByte`, `Write`,
  `Close`), plus optional extensions: `StreamEventReader`, `InfoRequester`,
  `EscapeAware`, `Reconnectable`. No "receive-addressed-to-me" or
  "send-slave-response" extension exists.
- `transport/enh.go` — `ENHCommand` enum including request IDs
  `ENHReqInit/Send/Start/Info` and response IDs
  `ENHResResetted/Received/Started/Info/Failed/ErrorEBUS/ErrorHost`. No
  slave-role command.
- `transport/enh_transport.go` — ~1290 LOC. Implements:
  - **INIT handshake** (`Init(features)` → `ENHReqInit`, waits for
    `ENHResResetted`).
  - **StartArbitration(initiator byte)** — the adapter's arbitration
    grant. Confirmed for initiator role only.
  - **Write path** — escapes bytes and drives echo verification of our own
    transmissions.
  - **Read path** — exposes `ReadByte()` and `ReadEvent()` with the post-
    STARTED pre-echo window (0xAA suppression for 50 ms). Surfaces raw
    wire bytes to the protocol layer.
  - No API receives "we observed a frame addressed to us" events. All
    inbound bytes flow through the same `ReadByte()` as passive traffic.
- `transport/ebusd_tcp.go` — wraps the `ebusd` TCP command socket. Critical
  behaviour:
  - `Write(payload)` parses outbound raw telegram, extracts `SRC/DST/PB/SB`,
    issues `hex -s SRC <bytes>\n` to ebusd, and synthesises the expected
    echo + slave-response locally from ebusd's JSON reply.
  - `ebusd` itself owns the wire. This transport has **no real-time
    ingress**: bytes appear in `pending` only as a side-effect of our own
    `Write`. Third-party telegrams directed at our local slave address
    never traverse this code path.
- `transport/tcp_plain_transport.go`, `udp_plain_transport.go` — direct
  byte conduits; no adapter semantics at all.
- `transport/loopback.go` — in-process test fake.
- `emulation/` package — **logical** target emulator
  (`Target{Name, Address, Rules}`, `EmulatedResponse{RespondAt, Frame}`).
  This is a **simulation/test harness**, NOT a transport runtime: it
  computes "what would a target say" in virtual time; it does not wire
  into `RawTransport`, does not ACK, does not write a response to the
  bus. Valuable as the **response-builder abstraction** we can reuse for
  M4c1/M4c2 — absolutely not a responder implementation.

### Protocol layer (for completeness)

- `protocol/bus.go` — `Bus` orchestrates **initiator-side transactions
  only**. The response-reading block (lines ~745–862) reads `NN
  DB..CRC`, checks CRC, and sends `ACK`/`NACK` **as the initiator
  acknowledging a target's response**. There is no symmetric path for
  "we are the target; initiator sent us a BB telegram; we must ACK then
  reply".
- `protocol/slave_encode.go` — `EncodeSlaveResponse(data)` already builds
  a wire-ready `NN DB..CRC` segment with escape handling. This is a
  usable building block for responder TX.
- `protocol/join.go` — `Joiner` selects an **initiator** address from the
  25 valid initiator addresses. No local slave address selection
  exists. The derived slave address (initiator + 5, modulo the rules) is
  not computed in code today outside the NM plan's unwritten runtime.

---

## 4. Per-Capability Matrix (ENH / ENS)

| Capability | Present? | Evidence / Gap |
| --- | --- | --- |
| Byte-level wire ingress | YES | `ENHTransport.ReadByte()` + `ENHResReceived` frames. |
| Parse inbound telegram header (QQ ZZ PB SB NN DB.. CRC) | NO in transport; **YES in `protocol` layer** but only when we initiated. A responder-side decoder is missing. |
| Match `ZZ` against configured local slave address | NO | No config field, no dispatch. |
| Emit ACK symbol in-window | PARTIAL | `Write()` can send a byte, but the bus mutex is initiator-oriented. An ACK from responder role must be scheduled **without** `StartArbitration` (we did not win the bus; we are replying in the slave window). No code path writes without arbitration today. |
| Emit escaped `NN DB.. CRC` segment | YES (encoder) + PARTIAL (TX path) | `EncodeSlaveResponse` exists; `Write()` can transmit bytes, but again the current state machine assumes we hold the bus via arbitration. |
| Receive initiator's final ACK | YES (bytes arrive via ReadByte) | No state tracking to recognise "this byte is the final ACK for my slave reply". |
| Timing budget (< ~15 ms target-response window) | UNKNOWN | ENH INIT + STARTED path have sub-100 ms budgets; raw wire latency to ebusd-like adapter is proven, but TCP-NODELAY + OS jitter has never been measured in a responder role. Spike open question. |

### Arbitration-source semantics

`ArbitrationSendsSource() bool` reports `true` for both ENH and ENS —
meaning during initiator arbitration the **adapter** emits the SRC byte.
In responder role the gateway does NOT arbitrate (the initiator already
holds the bus). The ENH protocol supports this: `ENHReqSend(data)`
simply writes `data` onto the wire. So the byte-emission substrate
exists — but there is no API today that exposes "send a byte(s) without
first calling StartArbitration". `Write()` is gated implicitly by the
bus-layer state machine that only calls it post-STARTED.

### Concretely what's missing in `helianthus-ebusgo`

1. **Transport extension** — a new optional interface, e.g.
   `ResponderTransport`:
   ```go
   type ResponderTransport interface {
       // SendResponderBytes writes bytes onto the wire WITHOUT
       // arbitration (the initiator already owns the bus).
       SendResponderBytes([]byte) error
   }
   ```
   ENH/ENS can implement this by calling the existing `Write()` path
   bypassing the arbitration precondition. Small, bounded.
2. **Telegram decoder in transport or new `protocol/responder` package** —
   reusing frame-parse logic already present in `protocol/bus.go` decode
   inner loop, refactored for both initiator and passive/responder use.
3. **Local-slave-address configuration** — a single byte plumbed from
   join authority (see deprecated NM plan `ISSUE-GW-JOIN-01`). In-scope
   for M4b1 at the **type primitive** level only; actual population is
   M4c1/M4c2.
4. **State-machine extension** for the ACK↔respond↔ACK protocol when
   we are the addressed target. This is the main new logic and lands in
   M4c1 per the locked dependency chain.

**Classification:** PARTIAL. Substrate exists; bounded additions required.
Estimated effort tier: **bounded** (no new kernel-level or adapter-level
primitive; all changes are Go code inside `helianthus-ebusgo`). Likely
1–2 focused PRs: (a) `ResponderTransport` interface + ENH impl + tests,
(b) responder-side telegram decoder + response-dispatch scaffolding.

---

## 5. `ebusd-tcp` Analysis (Hypothesis: BLOCKED)

Evidence from `transport/ebusd_tcp.go`:

- L30–71: transport wraps an **ebusd command connection**, not a raw wire.
- L102–177 (`Write`): outbound eBUS telegrams are parsed into
  `ebusdHexDispatch{src, dst, pb, sb, payload}` and dispatched via
  `hex -s SRC <bytes>\n` (L290). The transport then **synthesises** the
  expected echo (pushed into `pending`) and, for addressed telegrams
  where the DST is target-capable (`isInitiatorCapableAddress(dispatch.dst)`
  branch — L158), appends a fabricated ACK + response segment that came
  from ebusd's JSON reply.
- There is no goroutine reading the ebusd socket for unsolicited bus
  traffic. All reads are correlated to a pending command. The `pending`
  buffer only grows when (a) we `Write` something, or (b) the follow-up
  reader inside `sendHexCommand` returns data for our command.
- ebusd itself is the only bus participant from the gateway's
  perspective — it arbitrates, ACKs, responds, and re-transmits. It
  offers no API (`hex` has no passive-listen semantic) for "tell me when
  somebody sends me a telegram addressed to ZZ=0x76".

**Conclusion — BLOCKED.** `ebusd-tcp` is architecturally a request/response
command bridge. Responder-grade behaviour would require either:

- A new ebusd command and a passive-subscription API — out-of-scope,
  upstream project, and counter to our existing ebusd-isolation policy.
- Replacing `ebusd-tcp` with raw adapter access where responder mode is
  required — which is precisely what the ENH/ENS path already provides.

The locked plan's hypothesis is **confirmed**. `ebusd-tcp` MUST be listed
in the M4b2 transport matrix as "responder: not supported" and the NM /
`ebus_standard` runtime MUST disable responder lane when the active
transport is `ebusd-tcp`.

**Effort tier for `ebusd-tcp`:** **out-of-scope**.

---

## 6. Cross-Repo Dependencies

Findings that cross repo boundaries (recommendations only — do NOT open
these issues from inside this spike; `M4b2` in `helianthus-ebusgateway`
owns the go/no-go signal and is the proper place to spawn conditional
issues per §11 / §14 of the locked plan):

### 6.1 `helianthus-ebus-adapter-proxy` — conditional, NOT REQUIRED YET

The adapter-proxy currently multiplexes the single physical adapter to
multiple clients. For responder mode to work end-to-end, the proxy must
either:

- Let one client declare ownership of a local slave address, and route
  incoming frames matching that ZZ to only that client, OR
- Be transparent to responder traffic (the typical case: the adapter
  delivers all RECEIVED bytes to every client, and each client filters
  locally).

Empirical answer depends on the proxy's current fan-out semantics (not
examined in this spike — out of scope for `helianthus-ebusgo`). If the
proxy swallows or dedupes RECEIVED bytes in a way that breaks timing,
that is a proxy-side blocker.

**Recommendation to M4b2:** verify proxy fan-out by dual-attaching two
clients and checking RECEIVED-byte parity + wall-clock skew. If the
proxy serialises or reorders bytes, open `ISSUE-PROXY-EBS-01`
(already reserved in §12 of the locked plan) with an explicit
dependency edge from M4b2.

### 6.2 Adapter firmware (`knx2ebus` / PIC16F) — UNLIKELY BLOCKER

ENH `ENHReqSend` writes raw bytes; the adapter does not differentiate
initiator vs responder framing. The tight target-response window
(~15 ms after final initiator byte) is firmware-independent as long as
our `Write()` returns fast enough. The PIC16F firmware has the known
collision-retry race (see `bus.go` L22–27) but that is an initiator-path
concern. No firmware work is anticipated.

**Recommendation:** gather one live-bus trace during M4b2 where the
gateway (as responder) ACKs within-window; if that passes, firmware is
not on the critical path.

### 6.3 `helianthus-ebusgateway` — owns M4b2 and the runtime

All runtime wiring (local-slave-address from Joiner, NM interrogation
responder, `07 04` responder) lives in the gateway. This spike does
not add work there; M4b2 is the declared consumer of the M4b1 verdict.

### 6.4 Summary of recommendations

| Repo | Recommendation | Dependency edge |
| --- | --- | --- |
| `helianthus-ebus-adapter-proxy` | Measure fan-out under dual-attach during M4b2; open `ISSUE-PROXY-EBS-01` only if timing or byte-parity regresses. | M4b2 → proxy (conditional) |
| PIC16F / `knx2ebus` firmware | Verify by trace; no speculative work. | M4b2 → firmware (contingent, LOW) |
| `helianthus-ebusgateway` | M4b2 consumes this finding. No new recommendation from spike. | M4b1 → M4b2 (explicit) |

---

## 7. Recommended M4b2 Scope

M4b2 lives in `helianthus-ebusgateway` (per plan §14 / chunk 11). Based
on this spike, M4b2 should:

1. Accept this finding: ENH/ENS are **PARTIAL/GO-WITH-WORK**, `ebusd-tcp`
   is **BLOCKED**.
2. Emit the go/no-go signal as `responder_lane=GO` for ENH/ENS,
   `responder_lane=NOT_SUPPORTED` for ebusd-tcp.
3. Expose the active transport's responder-capability to the NM runtime
   so the responder lane is auto-disabled when transport = ebusd-tcp.
4. Trigger the conditional proxy measurement (see §6.1). Issue creation
   is M4b2's call, not this spike's.
5. Approve M4c1 (ebusgo transport-substrate PR) with the bounded scope
   described in §4.

---

## 8. Open Questions / Unknowns

1. **Target-response wire-time budget** — eBUS spec target-response window
   is typically within ~15 ms of the initiator's final CRC byte. ENH
   adds TCP round-trip. Real-world measurement is a PASS/FAIL criterion
   for M4c1 but can't be answered in a static code spike. **Action:**
   defer to M4c1 bench test.
2. **Adapter-proxy fan-out semantics** — see §6.1.
3. **Concurrent initiator + responder role on the same gateway** — if
   the gateway is polling semantics as initiator when an incoming frame
   arrives addressed to our slave, does the bus layer yield? Current
   `Bus` holds `readMu` for the whole transaction. This is a design
   question for M4c2 (gateway runtime), not for M4b1.
4. **ENS-vs-ENH behavioural differences** — current code treats them
   identically (`NewENSTransport` just calls `NewENHTransport`). If ENS
   has real differences on-wire, responder substrate must be re-verified.
   Not blocking M4b2 go/no-go.

---

## 9. Verdict Restated

- ENH: **PARTIAL** → bounded work → GO after M4c1.
- ENS: **PARTIAL** → bounded work → GO after M4c1 (inherits ENH impl).
- ebusd-tcp: **BLOCKED** → do not promise responder support on this
  transport; document in transport matrix; runtime must gate on
  capability flag.

No M4b2-blocker issue required inside `helianthus-ebusgo`. All follow-up
routes through M4b2 in `helianthus-ebusgateway`.
