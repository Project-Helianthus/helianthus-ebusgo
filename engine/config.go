package engine

import (
	"context"
	"net"
	"time"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

type TransportProtocol string

const (
	TransportENH           TransportProtocol = "enh"
	TransportENS           TransportProtocol = "ens"
	TransportUDPPlain      TransportProtocol = "udp-plain"
	TransportTCPPlain      TransportProtocol = "tcp-plain"
	TransportEbusdTCP      TransportProtocol = "ebusd-tcp"
	TransportAdapterDirect TransportProtocol = "adapter-direct"
	// transportAdapterDirectENS is the private canonical form of the
	// documented adapter-direct-ens:// URI. It preserves mux wire-family
	// selection after URI stripping without widening the public enum surface.
	transportAdapterDirectENS TransportProtocol = "adapter-direct-ens"
)

type TransportConfig struct {
	Protocol     TransportProtocol
	Network      string
	Address      string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	DialTimeout  time.Duration
	Dial         func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error)
}

type WatchObserver interface {
	Observe(key WatchKey) WatchObservation
}

type Config struct {
	Transport                transport.RawTransport
	PassiveTransport         transport.RawTransport // pre-configured passive transport (adapter-direct mode)
	TransportConfig          TransportConfig
	BusConfig                protocol.BusConfig
	LocalAddressSnapshotter  LocalBusAddressSnapshotter
	WatchObserver            WatchObserver
	ObserveFirstEnabled      bool
	PassiveStateDirectApply  bool
	PassiveConfigDirectApply bool
	ExternalWritePolicy      ObserveFirstExternalWritePolicy
	ObserveFirstFlags        ObserveFirstFeatureFlags

	PassiveAbsenceThreshold                time.Duration
	PassiveTransactionWatchdog             time.Duration
	PassiveReconnectInitialDelay           time.Duration
	PassiveReconnectMaxDelay               time.Duration
	PassiveDedupActivePublishBudget        time.Duration
	PassiveDedupPassiveDeliveryBudget      time.Duration
	PassiveDedupPendingGraceTimeout        time.Duration
	PassiveDedupActiveFingerprintRetention time.Duration
	PassiveDedupPendingCapacity            int
	PassiveDedupRecoveryHysteresis         time.Duration
	PassiveDedupRecoveryEventThreshold     int

	// Collaborator supplies the protocol decisions this package does not own.
	Collaborator EngineCollaborator
}

func DefaultConfig() Config {
	dedupDefaults := defaultPassiveDedupBudgets(protocol.DefaultBusConfig().RetryEnvelope())
	featureFlags := DefaultObserveFirstFeatureFlags()
	return Config{
		TransportConfig: TransportConfig{
			Protocol:     TransportENH,
			Network:      "unix",
			Address:      "/var/run/ebusd/ebusd.socket",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			DialTimeout:  5 * time.Second,
		},
		BusConfig:                              protocol.DefaultBusConfig(),
		PassiveAbsenceThreshold:                10 * time.Second,
		PassiveTransactionWatchdog:             1 * time.Second,
		PassiveReconnectInitialDelay:           1 * time.Second,
		PassiveReconnectMaxDelay:               30 * time.Second,
		PassiveDedupActivePublishBudget:        dedupDefaults.ActivePublishBudget,
		PassiveDedupPassiveDeliveryBudget:      dedupDefaults.PassiveDeliveryBudget,
		PassiveDedupPendingGraceTimeout:        dedupDefaults.PendingGraceTimeout,
		PassiveDedupActiveFingerprintRetention: dedupDefaults.ActiveFingerprintRetention,
		PassiveDedupPendingCapacity:            dedupDefaults.PendingCapacity,
		PassiveDedupRecoveryHysteresis:         dedupDefaults.RecoveryHysteresis,
		PassiveDedupRecoveryEventThreshold:     dedupDefaults.RecoveryEventThreshold,
		ObserveFirstEnabled:                    featureFlags.ObserveFirstEnabled(),
		PassiveStateDirectApply:                true,
		PassiveConfigDirectApply:               featureFlags.PassiveConfigDirectApply(),
		ExternalWritePolicy:                    featureFlags.ExternalWritePolicy(),
		ObserveFirstFlags:                      featureFlags,
	}
}

func applyDefaults(cfg Config) Config {
	if cfg.BusConfig == (protocol.BusConfig{}) {
		cfg.BusConfig = protocol.DefaultBusConfig()
	}
	if cfg.PassiveAbsenceThreshold <= 0 {
		cfg.PassiveAbsenceThreshold = 10 * time.Second
	}
	if cfg.PassiveTransactionWatchdog <= 0 {
		cfg.PassiveTransactionWatchdog = 1 * time.Second
	}
	if cfg.PassiveReconnectInitialDelay <= 0 {
		cfg.PassiveReconnectInitialDelay = 1 * time.Second
	}
	if cfg.PassiveReconnectMaxDelay <= 0 {
		cfg.PassiveReconnectMaxDelay = 30 * time.Second
	}
	dedupDefaults := defaultPassiveDedupBudgets(cfg.BusConfig.RetryEnvelope())
	if cfg.PassiveDedupActivePublishBudget <= 0 {
		cfg.PassiveDedupActivePublishBudget = dedupDefaults.ActivePublishBudget
	}
	if cfg.PassiveDedupPassiveDeliveryBudget <= 0 {
		cfg.PassiveDedupPassiveDeliveryBudget = dedupDefaults.PassiveDeliveryBudget
	}
	if cfg.PassiveDedupPendingGraceTimeout <= 0 {
		cfg.PassiveDedupPendingGraceTimeout = dedupDefaults.PendingGraceTimeout
	}
	if cfg.PassiveDedupActiveFingerprintRetention <= 0 {
		cfg.PassiveDedupActiveFingerprintRetention = dedupDefaults.ActiveFingerprintRetention
	}
	if cfg.PassiveDedupPendingCapacity <= 0 {
		cfg.PassiveDedupPendingCapacity = dedupDefaults.PendingCapacity
	}
	if cfg.PassiveDedupRecoveryHysteresis <= 0 {
		cfg.PassiveDedupRecoveryHysteresis = dedupDefaults.RecoveryHysteresis
	}
	if cfg.PassiveDedupRecoveryEventThreshold <= 0 {
		cfg.PassiveDedupRecoveryEventThreshold = dedupDefaults.RecoveryEventThreshold
	}
	if cfg.Transport == nil {
		if cfg.TransportConfig.Protocol == "" {
			cfg.TransportConfig.Protocol = TransportENH
		}
		if cfg.TransportConfig.Network == "" {
			cfg.TransportConfig.Network = "unix"
		}
		if cfg.TransportConfig.Address == "" {
			cfg.TransportConfig.Address = "/var/run/ebusd/ebusd.socket"
		}
		if cfg.TransportConfig.ReadTimeout == 0 {
			cfg.TransportConfig.ReadTimeout = 5 * time.Second
		}
		if cfg.TransportConfig.WriteTimeout == 0 {
			cfg.TransportConfig.WriteTimeout = 5 * time.Second
		}
		if cfg.TransportConfig.DialTimeout == 0 {
			cfg.TransportConfig.DialTimeout = 5 * time.Second
		}
	}
	flags := NormalizeObserveFirstFeatureFlags(cfg.ObserveFirstEnabled, cfg.PassiveStateDirectApply, cfg.PassiveConfigDirectApply, cfg.ExternalWritePolicy)
	if cfg.ExternalWritePolicy == "" && !cfg.ObserveFirstEnabled && !cfg.PassiveStateDirectApply && !cfg.PassiveConfigDirectApply && cfg.ObserveFirstFlags.configured() {
		flags = NormalizeObserveFirstFeatureFlagsFromView(cfg.ObserveFirstFlags)
	}
	cfg.ObserveFirstFlags = flags
	cfg.ObserveFirstEnabled = flags.ObserveFirstEnabled()
	cfg.PassiveStateDirectApply = flags.PassiveStateDirectApply()
	cfg.PassiveConfigDirectApply = flags.PassiveConfigDirectApply()
	cfg.ExternalWritePolicy = flags.ExternalWritePolicy()
	return cfg
}
