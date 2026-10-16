package transport

// BaudObservability is an optional transport-owned observability surface.
// Absence means unavailable, not zero traffic or zero faults. RawTransport
// implementations and on-wire behavior are unchanged. DriverRuntime callers
// can sample it inside an admitted invocation, alongside that lease's generation.
type BaudObservability interface {
	BaudObservabilitySnapshot() BaudObservabilitySnapshot
}

// BaudObservabilitySnapshot contains cumulative counters for one transport
// instance. A fresh instance starts at zero; reconnect, INIT, adapter reset and
// Close do not erase its counters. Samples use independent atomic loads: each
// counter is race-safe, but the collection is not a transactional snapshot.
// None of these counters establishes physical baud or attributes faults to it.
type BaudObservabilitySnapshot struct {
	// ConfiguredBaud is in bits per second, valid only when ConfiguredBaudKnown.
	// ENH/ENS over net.Conn do not configure or observe UART baud and leave
	// these fields zero/false. Consumers must omit the unavailable gauge.
	ConfiguredBaud      uint64
	ConfiguredBaudKnown bool

	// ReceivedSymbolsTotal counts successfully parsed ENH RECEIVED messages,
	// including compact raw bytes, before eBUS unescaping, arbitration/SYN
	// suppression or queue eviction. Escape leads/completions and SYNs each
	// count as one symbol. ENH control/INFO messages and unparsed bytes do not.
	// A rate of this counter is host-observed ingress throughput in symbols/s,
	// including idle traffic and echoes; it is not measured baud.
	ReceivedSymbolsTotal uint64

	// ENHFramingErrorsTotal counts ENHParser.Parse errors, once per failing
	// parse, even if a valid response takes precedence over a trailing error.
	// This describes the host-adapter framing layer, not UART framing errors.
	ENHFramingErrorsTotal uint64

	// ENHParserResetsTotal counts actual parser reset actions (resynchronization
	// attempts), including ordinary INIT/arbitration/reconnect boundaries and
	// timeout cleanup. It does not prove recovery or indicate a baud fault.
	ENHParserResetsTotal uint64

	// AdapterResetResponsesTotal counts parsed RESETTED responses, including
	// expected INIT confirmations. It is not an unexpected-device-reboot count.
	AdapterResetResponsesTotal uint64

	// EscapeDecodeFaultsTotal mirrors ENHTransport.DecodeFaultTotal.
	// EscapeRecoveriesTotal and EscapeBudgetExhaustedTotal decompose that
	// counter; EscapePendingTimeoutsTotal is separate. Units are decoder events,
	// not bytes. These describe escape decoding, with no baud attribution.
	EscapeDecodeFaultsTotal    uint64
	EscapeRecoveriesTotal      uint64
	EscapeBudgetExhaustedTotal uint64
	EscapePendingTimeoutsTotal uint64
}
