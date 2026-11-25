package engine_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/engine"
	"github.com/Project-Helianthus/helianthus-ebusgo/engine/donoridentity"
)

// TestOracleUnsupportedMutableKey replays the donor rejection of a key type the
// catalog does not clone. The key's package name is the donor package name
// because the recorded error formats the key with %T.
func TestOracleUnsupportedMutableKey(t *testing.T) {
	_, mutableErr := engine.NewWatchCatalog([]engine.WatchDescriptor{{
		Key:               ebusgateway.MutableUnsupportedWatchKey(),
		SemanticClass:     engine.WatchSemanticClassState,
		FreshnessProfile:  engine.WatchFreshnessProfileStateFast,
		DecoderID:         "custom.mutable",
		CorrelationPolicy: engine.WatchCorrelationPolicyRequestResponse,
		DirectApplyPolicy: engine.WatchDirectApplyPolicyStateDefault,
	}}, engine.DonorCollaborator())
	errText := ""
	if mutableErr != nil {
		errText = mutableErr.Error()
	}
	document := mutableOracleCase{
		Format:    "helianthus-oracle-bt01-case/1",
		Component: "watch_catalog",
		Source:    "watch_catalog.go",
		Stimulus:  "TestNewWatchCatalogRejectsUnsupportedMutableKeyType",
	}
	document.Input.Bytes = ""
	document.Input.Qualifiers = map[string]any{}
	document.Input.Clock = map[string]any{}
	document.Input.State = map[string]any{}
	document.Output.SymbolsWritten = []any{}
	document.Output.Decisions = []any{map[string]any{"error": errText}}
	document.Output.Cache = map[string]any{}
	document.Output.Observations = []any{}
	document.Output.Counters = map[string]any{}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := append(encoded, '\n')
	want, err := os.ReadFile(filepath.Join("testdata", "oracle", "watch-catalog", "unsupported-mutable-key.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("oracle vector watch-catalog/unsupported-mutable-key.json differs from the recorded case")
	}
}

type mutableOracleCase struct {
	Format    string             `json:"format"`
	Component string             `json:"component"`
	Source    string             `json:"source"`
	Stimulus  string             `json:"stimulus"`
	Input     mutableOracleInput `json:"input"`
	Output    mutableOracleOut   `json:"output"`
}

type mutableOracleInput struct {
	Bytes      string         `json:"bytes"`
	Qualifiers map[string]any `json:"qualifiers"`
	Clock      map[string]any `json:"clock"`
	State      map[string]any `json:"state"`
}

type mutableOracleOut struct {
	SymbolsWritten []any          `json:"symbols_written"`
	Decisions      []any          `json:"decisions"`
	Cache          map[string]any `json:"cache"`
	Observations   []any          `json:"observations"`
	Counters       map[string]any `json:"counters"`
}
