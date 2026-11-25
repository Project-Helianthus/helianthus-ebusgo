package engine

import (
	"testing"
	"time"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
	"github.com/Project-Helianthus/helianthus-ebusgo/protocol"
)

// TestOracleEngineReconstructor records the pin's reconstructor stimuli for
// the read-timeout watchdog and for self-echo classification.
func TestOracleEngineReconstructor(t *testing.T) {
	oracleReconstructorCase(t, "reconstructor/read-timeout-watchdog.json", "TestPassiveTransactionReconstructor_ReadTimeoutHonorsWatchdog", oracleReconSpec{
		Watchdog: 250 * time.Millisecond,
		TapEvents: []oracleTapEvent{
			{Kind: "read_timeout", OffsetNS: int64(100 * time.Millisecond), Error: ebuserrors.ErrTimeout.Error()},
			{Kind: "read_timeout", OffsetNS: int64(400 * time.Millisecond), Error: ebuserrors.ErrTimeout.Error()},
		},
	}, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
		cfg := withDonor(DefaultConfig())
		cfg.PassiveTransactionWatchdog = 250 * time.Millisecond
		reconstructor := newPassiveTransactionReconstructorCore(cfg)
		subscription, err := reconstructor.Subscribe("test", PassiveSubscriberCritical, 8)
		if err != nil {
			t.Fatal(err)
		}
		request := protocol.Frame{Source: 0x10, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x01}}
		base := time.Unix(0, 0)
		payload := append(requestFrameBytes(request), protocol.SymbolAck)
		feedPassiveSymbols(reconstructor, base, payload)
		reconstructor.OnPassiveTapEvent(PassiveTapEvent{Kind: PassiveTapEventReadTimeout, ObservedAt: base.Add(100 * time.Millisecond), Err: ebuserrors.ErrTimeout})
		assertNoPassiveClassifiedEvent(t, subscription, 25*time.Millisecond)
		reconstructor.OnPassiveTapEvent(PassiveTapEvent{Kind: PassiveTapEventReadTimeout, ObservedAt: base.Add(400 * time.Millisecond), Err: ebuserrors.ErrTimeout})
		event := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventAbandonedTransaction)
		return payload, []PassiveClassifiedEvent{event}, reconstructor
	})
	badCRC := []byte{0x08, 0xB5, 0x09, 0x01, 0x01, 0xFF}
	selfEchoCases := []struct {
		name, stimulus string
		snapshotter    bool
		payload        []byte
	}{
		{"self-echo-parse-failure", "TestReconstructorSelfEchoOnParseFailure", true, append(append([]byte{0x71}, badCRC...), protocol.SymbolSyn)},
		{"no-self-echo-third-party", "TestReconstructorNoSelfEchoForThirdParty", true, append(append([]byte{0x31}, badCRC...), protocol.SymbolSyn)},
		{"self-echo-without-snapshotter", "TestReconstructorSelfEchoNotTriggeredWithoutSnapshotter", false, append(append([]byte{0x71}, badCRC...), protocol.SymbolSyn)},
		{"self-echo-ack-phase", "TestReconstructorSelfEchoACKPhase", true, append(requestFrameBytes(protocol.Frame{Source: 0x71, Target: 0x08, Primary: 0xB5, Secondary: 0x09, Data: []byte{0x01}}), protocol.SymbolSyn)},
	}
	for _, tc := range selfEchoCases {
		tc := tc
		spec := oracleReconSpec{}
		if tc.snapshotter {
			spec.Snapshotter = &LocalAddressSnapshot{Address: 0x71, Known: true}
		}
		oracleReconstructorCase(t, "reconstructor/"+tc.name+".json", tc.stimulus, spec, func() ([]byte, []PassiveClassifiedEvent, *PassiveTransactionReconstructor) {
			reconstructor := newPassiveTransactionReconstructorCore(withDonor(DefaultConfig()))
			if tc.snapshotter {
				reconstructor.SetLocalAddressSnapshotter(testLocalSnapshotter{address: 0x71, known: true})
			}
			subscription, err := reconstructor.Subscribe("test", PassiveSubscriberCritical, 8)
			if err != nil {
				t.Fatal(err)
			}
			feedPassiveSymbols(reconstructor, time.Unix(0, 0), tc.payload)
			event := requirePassiveClassifiedEvent(t, subscription, PassiveClassifiedEventAbandonedTransaction)
			return tc.payload, []PassiveClassifiedEvent{event}, reconstructor
		})
	}
}
