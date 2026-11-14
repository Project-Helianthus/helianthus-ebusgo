package transport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workItemCode matches a two-letter work-item code followed by its number at
// the start of a name or of a word inside it, for example XY12Name, ErrXY12 or
// xy12_name.go, while plain counts such as Uint32 or arm64 stay accepted.
var workItemCode = regexp.MustCompile(`(^|[a-z0-9_])[A-Z]{2}[0-9]{2}|(^|_)[a-z]{2}[0-9]{2}`)

// TestNoWorkItemCodeInExportedNamesOrFiles lists every Go file name of the
// package and every exported name its production files declare (types,
// functions, methods, constants, variables, struct fields and interface
// methods, under every build constraint) and fails on a work-item code.
func TestNoWorkItemCodeInExportedNamesOrFiles(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		if workItemCode.MatchString(name) {
			t.Errorf("file name %s carries a work-item code", name)
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			var idents []*ast.Ident
			switch n := node.(type) {
			case *ast.FuncDecl:
				idents = append(idents, n.Name)
			case *ast.TypeSpec:
				idents = append(idents, n.Name)
			case *ast.ValueSpec:
				idents = append(idents, n.Names...)
			case *ast.Field:
				idents = append(idents, n.Names...)
			}
			for _, ident := range idents {
				if !ident.IsExported() {
					continue
				}
				checked++
				if workItemCode.MatchString(ident.Name) {
					t.Errorf("exported name %s in %s carries a work-item code", ident.Name, name)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no exported name was checked")
	}
	for _, accepted := range []string{"Uint32", "IsV31", "local_gpio_linux_arm64.go", "Sum256"} {
		if workItemCode.MatchString(accepted) {
			t.Fatalf("%s is not a work-item code", accepted)
		}
	}
	for _, rejected := range []string{"XY12Name", "ErrXY12", "xy12_name.go", "name_xy12.go"} {
		if !workItemCode.MatchString(rejected) {
			t.Fatalf("%s carries a work-item code", rejected)
		}
	}
}

type fakeProvider struct {
	capabilities Capabilities
}

func (provider *fakeProvider) TransportCapabilities() Capabilities {
	return provider.capabilities
}

type responderOnly struct{}

// This existing interface method must never be invoked by the query seam.
func (responderOnly) SendResponderBytes(payload []byte) (int, error) {
	panic("query attempted emission")
}

func positiveCapabilities() Capabilities {
	return Capabilities{
		Kind: KindLocalRaw, Encoding: EncodingRaw,
		PhysicalArbitration: EvidenceProven, AutonomousPreparedResponse: EvidenceProven,
		ObservedEcho: EvidenceProven, CaptureTimestamps: EvidenceProven,
		QualifiedTiming: EvidenceProven,
	}
}

func TestAbsentCapabilities(t *testing.T) {
	var nilProvider *fakeProvider
	for _, backend := range []any{nil, struct{}{}, responderOnly{}, nilProvider} {
		got := QueryCapabilities(backend)
		if got != (Capabilities{}) || got.DeadlineSuitable() {
			t.Fatalf("absent provider granted capabilities: %+v", got)
		}
	}
	if (Capabilities{}).DeadlineSuitable() {
		t.Fatal("zero descriptor grants a deadline claim")
	}
}

func TestIndependentClaims(t *testing.T) {
	// Each axis is varied while all other claims retain their positive control.
	// Invalid values normalize only that field, preserving last-known evidence.
	for field := 0; field < 5; field++ {
		for _, evidence := range []CapabilityEvidence{EvidenceUnknown, EvidenceHypothesis, EvidenceProven, 255} {
			input := positiveCapabilities()
			expected := positiveCapabilities()
			inputFields := []*CapabilityEvidence{&input.PhysicalArbitration, &input.AutonomousPreparedResponse, &input.ObservedEcho, &input.CaptureTimestamps, &input.QualifiedTiming}
			expectedFields := []*CapabilityEvidence{&expected.PhysicalArbitration, &expected.AutonomousPreparedResponse, &expected.ObservedEcho, &expected.CaptureTimestamps, &expected.QualifiedTiming}
			*inputFields[field] = evidence
			if evidence == 255 {
				*expectedFields[field] = EvidenceUnknown
			} else {
				*expectedFields[field] = evidence
			}
			got := QueryCapabilities(&fakeProvider{capabilities: input})
			if got != expected {
				t.Fatalf("field %d evidence %d: got %+v want %+v", field, evidence, got, expected)
			}
			wantDeadline := evidence == EvidenceProven || (field != 1 && field != 4)
			if got.DeadlineSuitable() != wantDeadline {
				t.Fatalf("field %d evidence %d changed independent deadline expectation", field, evidence)
			}
		}
	}
}

func TestClaimCombinations(t *testing.T) {
	// Exhaust every combination, including malformed claims. Expected admission
	// uses base-four input digits, independently of descriptor normalization.
	for combination := 0; combination < 1024; combination++ {
		input := positiveCapabilities()
		fields := []*CapabilityEvidence{&input.PhysicalArbitration, &input.AutonomousPreparedResponse, &input.ObservedEcho, &input.CaptureTimestamps, &input.QualifiedTiming}
		digits := combination
		for _, field := range fields {
			*field = []CapabilityEvidence{EvidenceUnknown, EvidenceHypothesis, EvidenceProven, 255}[digits%4]
			digits /= 4
		}
		want := (combination/4)%4 == 2 && (combination/256)%4 == 2
		got := QueryCapabilities(&fakeProvider{capabilities: input})
		if got.DeadlineSuitable() != want || input.DeadlineSuitable() != want {
			t.Fatalf("combination %d: deadline want %t", combination, want)
		}
	}
}

func TestTransportKinds(t *testing.T) {
	for _, tc := range []struct {
		kind     BackendKind
		expected BackendKind
		deadline bool
	}{
		{KindUnknown, KindUnknown, false},
		{KindLocalRaw, KindLocalRaw, true},
		{KindENH, KindENH, true},
		{KindENS, KindENS, true},
		{KindTCP, KindTCP, true},
		{KindUDP, KindUDP, true},
		{KindEBUSDCommand, KindEBUSDCommand, true},
		{255, KindUnknown, false},
	} {
		input := positiveCapabilities()
		input.Kind = tc.kind
		expected := positiveCapabilities()
		expected.Kind = tc.expected
		got := QueryCapabilities(&fakeProvider{capabilities: input})
		if got != expected || got.DeadlineSuitable() != tc.deadline || input.DeadlineSuitable() != tc.deadline {
			t.Fatalf("kind %d: got %+v want %+v", tc.kind, got, expected)
		}
	}
	if KindLocalRaw == KindENS {
		t.Fatal("local raw must not masquerade as ENS")
	}
}

func TestEncodings(t *testing.T) {
	for _, tc := range []struct {
		encoding StreamEncoding
		expected StreamEncoding
		deadline bool
	}{
		{EncodingUnknown, EncodingUnknown, false},
		{EncodingRaw, EncodingRaw, true},
		{EncodingLogical, EncodingLogical, true},
		{255, EncodingUnknown, false},
	} {
		input := positiveCapabilities()
		input.Encoding = tc.encoding
		expected := positiveCapabilities()
		expected.Encoding = tc.expected
		got := QueryCapabilities(&fakeProvider{capabilities: input})
		if got != expected || got.DeadlineSuitable() != tc.deadline || input.DeadlineSuitable() != tc.deadline {
			t.Fatalf("encoding %d: got %+v want %+v", tc.encoding, got, expected)
		}
	}
	if EncodingRaw == EncodingLogical {
		t.Fatal("raw symbols and logical bytes must differ")
	}
}
