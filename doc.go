// Package ebusgo is the root of the eBUS library module. It owns eBUS
// transport, framing, addressing and protocol-engine behavior; registry,
// semantic, consumer and API policy belong to the modules that import it.
//
// The root package declares no API and exists only to anchor the module
// path. Callers import the subpackages:
//
//   - transport: adapter and network connections that carry eBUS symbols.
//   - protocol: frames, addressing, escaping, CRC framing and the bus engine,
//     with protocol-specific codecs in its subpackages.
//   - types: codecs for the fixed-width eBUS data types.
//   - errors: the shared sentinel errors and their code and category mapping.
//   - determinism: canonical hashing, idempotency and retry schedules.
//   - emulation: deterministic simulated targets for tests.
//   - localworker: offline, deterministic models of a local responder
//     worker; it opens no device and cannot transmit.
package ebusgo
