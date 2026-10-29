package transport

import aa22testing "testing"

type aa22Provider struct {
	aa22Capabilities AA22Capabilities
}

func (aa22ProviderValue *aa22Provider) AA22TransportCapabilities() AA22Capabilities {
	return aa22ProviderValue.aa22Capabilities
}

type aa22ResponderOnly struct{}

// This existing interface method must never be invoked by the query seam.
func (aa22ResponderOnly) SendResponderBytes(aa22Payload []byte) (int, error) {
	panic("aa22 query attempted emission")
}

func aa22PositiveCapabilities() AA22Capabilities {
	return AA22Capabilities{
		AA22Kind: AA22KindLocalRaw, AA22Encoding: AA22EncodingRaw,
		AA22PhysicalArbitration: AA22Proven, AA22AutonomousPreparedResponse: AA22Proven,
		AA22ObservedEcho: AA22Proven, AA22CaptureTimestamps: AA22Proven,
		AA22QualifiedTiming: AA22Proven,
	}
}

func TestAA22AbsentCapabilities(aa22T *aa22testing.T) {
	var aa22NilProvider *aa22Provider
	for _, aa22Transport := range []any{nil, struct{}{}, aa22ResponderOnly{}, aa22NilProvider} {
		aa22Got := AA22QueryCapabilities(aa22Transport)
		if aa22Got != (AA22Capabilities{}) || aa22Got.AA22DeadlineSuitable() {
			aa22T.Fatalf("aa22 absent provider granted capabilities: %+v", aa22Got)
		}
	}
	if (AA22Capabilities{}).AA22DeadlineSuitable() {
		aa22T.Fatal("aa22 zero descriptor grants a deadline claim")
	}
}

func TestAA22IndependentClaims(aa22T *aa22testing.T) {
	// Each axis is varied while all other claims retain their positive control.
	// Invalid values normalize only that field, preserving last-known evidence.
	for aa22Field := 0; aa22Field < 5; aa22Field++ {
		for _, aa22Evidence := range []AA22Evidence{AA22Unknown, AA22Hypothesis, AA22Proven, 255} {
			aa22Input := aa22PositiveCapabilities()
			aa22Expected := aa22PositiveCapabilities()
			aa22InputFields := []*AA22Evidence{&aa22Input.AA22PhysicalArbitration, &aa22Input.AA22AutonomousPreparedResponse, &aa22Input.AA22ObservedEcho, &aa22Input.AA22CaptureTimestamps, &aa22Input.AA22QualifiedTiming}
			aa22ExpectedFields := []*AA22Evidence{&aa22Expected.AA22PhysicalArbitration, &aa22Expected.AA22AutonomousPreparedResponse, &aa22Expected.AA22ObservedEcho, &aa22Expected.AA22CaptureTimestamps, &aa22Expected.AA22QualifiedTiming}
			*aa22InputFields[aa22Field] = aa22Evidence
			if aa22Evidence == 255 {
				*aa22ExpectedFields[aa22Field] = AA22Unknown
			} else {
				*aa22ExpectedFields[aa22Field] = aa22Evidence
			}
			aa22Got := AA22QueryCapabilities(&aa22Provider{aa22Capabilities: aa22Input})
			if aa22Got != aa22Expected {
				aa22T.Fatalf("aa22 field %d evidence %d: got %+v want %+v", aa22Field, aa22Evidence, aa22Got, aa22Expected)
			}
			aa22WantDeadline := aa22Evidence == AA22Proven || (aa22Field != 1 && aa22Field != 4)
			if aa22Got.AA22DeadlineSuitable() != aa22WantDeadline {
				aa22T.Fatalf("aa22 field %d evidence %d changed independent deadline expectation", aa22Field, aa22Evidence)
			}
		}
	}
}

func TestAA22ClaimCombinations(aa22T *aa22testing.T) {
	// Exhaust every combination, including malformed claims. Expected admission
	// uses base-four input digits, independently of descriptor normalization.
	for aa22Combination := 0; aa22Combination < 1024; aa22Combination++ {
		aa22Input := aa22PositiveCapabilities()
		aa22Fields := []*AA22Evidence{&aa22Input.AA22PhysicalArbitration, &aa22Input.AA22AutonomousPreparedResponse, &aa22Input.AA22ObservedEcho, &aa22Input.AA22CaptureTimestamps, &aa22Input.AA22QualifiedTiming}
		aa22Digits := aa22Combination
		for _, aa22Field := range aa22Fields {
			*aa22Field = []AA22Evidence{AA22Unknown, AA22Hypothesis, AA22Proven, 255}[aa22Digits%4]
			aa22Digits /= 4
		}
		aa22Want := (aa22Combination/4)%4 == 2 && (aa22Combination/256)%4 == 2
		aa22Got := AA22QueryCapabilities(&aa22Provider{aa22Capabilities: aa22Input})
		if aa22Got.AA22DeadlineSuitable() != aa22Want || aa22Input.AA22DeadlineSuitable() != aa22Want {
			aa22T.Fatalf("aa22 combination %d: deadline want %t", aa22Combination, aa22Want)
		}
	}
}

func TestAA22TransportKinds(aa22T *aa22testing.T) {
	for _, aa22Case := range []struct {
		aa22Kind     AA22TransportKind
		aa22Expected AA22TransportKind
		aa22Deadline bool
	}{
		{AA22KindUnknown, AA22KindUnknown, false},
		{AA22KindLocalRaw, AA22KindLocalRaw, true},
		{AA22KindENH, AA22KindENH, true},
		{AA22KindENS, AA22KindENS, true},
		{AA22KindTCP, AA22KindTCP, true},
		{AA22KindUDP, AA22KindUDP, true},
		{AA22KindEBUSDCommand, AA22KindEBUSDCommand, true},
		{255, AA22KindUnknown, false},
	} {
		aa22Input := aa22PositiveCapabilities()
		aa22Input.AA22Kind = aa22Case.aa22Kind
		aa22Expected := aa22PositiveCapabilities()
		aa22Expected.AA22Kind = aa22Case.aa22Expected
		aa22Got := AA22QueryCapabilities(&aa22Provider{aa22Capabilities: aa22Input})
		if aa22Got != aa22Expected || aa22Got.AA22DeadlineSuitable() != aa22Case.aa22Deadline || aa22Input.AA22DeadlineSuitable() != aa22Case.aa22Deadline {
			aa22T.Fatalf("aa22 kind %d: got %+v want %+v", aa22Case.aa22Kind, aa22Got, aa22Expected)
		}
	}
	if AA22KindLocalRaw == AA22KindENS {
		aa22T.Fatal("aa22 local raw must not masquerade as ENS")
	}
}

func TestAA22Encodings(aa22T *aa22testing.T) {
	for _, aa22Case := range []struct {
		aa22Encoding AA22Encoding
		aa22Expected AA22Encoding
		aa22Deadline bool
	}{
		{AA22EncodingUnknown, AA22EncodingUnknown, false},
		{AA22EncodingRaw, AA22EncodingRaw, true},
		{AA22EncodingLogical, AA22EncodingLogical, true},
		{255, AA22EncodingUnknown, false},
	} {
		aa22Input := aa22PositiveCapabilities()
		aa22Input.AA22Encoding = aa22Case.aa22Encoding
		aa22Expected := aa22PositiveCapabilities()
		aa22Expected.AA22Encoding = aa22Case.aa22Expected
		aa22Got := AA22QueryCapabilities(&aa22Provider{aa22Capabilities: aa22Input})
		if aa22Got != aa22Expected || aa22Got.AA22DeadlineSuitable() != aa22Case.aa22Deadline || aa22Input.AA22DeadlineSuitable() != aa22Case.aa22Deadline {
			aa22T.Fatalf("aa22 encoding %d: got %+v want %+v", aa22Case.aa22Encoding, aa22Got, aa22Expected)
		}
	}
	if AA22EncodingRaw == AA22EncodingLogical {
		aa22T.Fatal("aa22 raw symbols and logical bytes must differ")
	}
}
