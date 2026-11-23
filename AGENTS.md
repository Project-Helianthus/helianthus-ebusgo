# AGENTS

## Scope

`helianthus-ebusgo` owns eBUS transport, framing, addressing, and protocol-engine behavior. Keep eBUS-specific types and logic at this boundary; do not add registry, universal semantic, consumer, MCP, GraphQL, or Home Assistant policy here.

Public protocol references:

- https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/protocols/ebus-services/ebus-overview.md
- https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/protocols/enh.md
- https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/protocols/ens.md
- https://github.com/Project-Helianthus/helianthus-docs-ebus/blob/main/protocols/ebusd-tcp.md

## Working rules

- One focused issue and one PR at a time. Branches use `issue/<number>-<slug>` and start from a clean `origin/main` worktree.
- Preserve public API and framing compatibility unless the issue explicitly changes them. Partial failures must not cause callers to discard valid state they still own.
- Run `./scripts/ci_local.sh` before pushing. For a transport or protocol-code change, record the applicable T01..T88 adapter-matrix result, including not-run, failed or unexpected outcomes. Board decision D23 (2026-11-21, [issue #247](https://github.com/Project-Helianthus/helianthus-ebusgo/issues/247)) says "the adapter matrix is a recorded level, never a merge condition." Unrelated CI validation failures still block.
- Review the exact PR HEAD in a fresh context. Fix valid P0-P2 findings and re-review the new HEAD; P3-P4 are triaged without blocking.
- Use squash merge only after CI and fresh exact-HEAD review are clear. Verify remote `main` and stop at the requested boundary.
- Stop for explicit action-time confirmation before credential handling, real installation writes, live-device mutation, or destructive/irreversible operations.

## Documentation

When a change establishes or changes public eBUS transport/framing knowledge, update the public documentation in `helianthus-docs-ebus` in the same delivery cycle. Documentation-only instruction changes do not create a protocol claim.
