# AGENTS

## Scope

`helianthus-ebusgo` owns eBUS transport, framing, addressing, and protocol-engine behavior. Keep eBUS-specific types and logic at this boundary; do not add registry, universal semantic, consumer, MCP, GraphQL, or Home Assistant policy here.

Public protocol references:

- https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ebus-overview.md
- https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/enh.md
- https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ens.md
- https://github.com/d3vi1/helianthus-docs-ebus/blob/main/protocols/ebusd-tcp.md

## Working rules

- One focused issue and one PR at a time. Branches use `issue/<number>-<slug>` and start from a clean `origin/main` worktree.
- Preserve public API and framing compatibility unless the issue explicitly changes them. Partial failures must not cause callers to discard valid state they still own.
- Run `./scripts/ci_local.sh` before pushing. For a transport or protocol-code change, provide the applicable T01..T88 transport-matrix result; unexpected fail or xpass blocks the PR unless the owner records an override reason.
- Review the exact PR HEAD in a fresh context. Fix valid P0-P2 findings and re-review the new HEAD; P3-P4 are triaged without blocking.
- Use squash merge only after CI and fresh exact-HEAD review are clear. Do not merge or make follow-on changes unless the operator asks.
- Stop for explicit action-time confirmation before credential handling, real installation writes, live-device mutation, or destructive/irreversible operations.

## Documentation

When a change establishes or changes public eBUS transport/framing knowledge, update the public documentation in `helianthus-docs-ebus` in the same delivery cycle. Documentation-only instruction changes do not create a protocol claim.
