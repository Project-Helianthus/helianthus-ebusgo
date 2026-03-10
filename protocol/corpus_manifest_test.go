package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type observerCorpus struct {
	Name            string `json:"name"`
	ValidationScope string `json:"validation_scope"`
	Cases           []struct {
		Name       string `json:"name"`
		SourceTest string `json:"source_test"`
		FrameType  string `json:"frame_type"`
		ScriptedIn []struct {
			Type string `json:"type"`
		} `json:"scripted_inbound"`
		Expected struct {
			Outcome string `json:"outcome"`
		} `json:"expected"`
	} `json:"cases"`
}

type mirroredCorpus struct {
	Name                  string `json:"name"`
	ValidationScope       string `json:"validation_scope"`
	MirrorSourceWorkspace string `json:"mirror_source_workspace_path"`
	Cases                 []struct {
		Name string `json:"name"`
		File string `json:"file"`
	} `json:"cases"`
}

func TestProtocol_ObserverHookCorpusManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join("testdata", "observer_hook_cases.json")
	var corpus observerCorpus
	readJSONFixture(t, path, &corpus)

	if corpus.Name != "observer_hook_core_cases_v1" {
		t.Fatalf("manifest name = %q; want observer_hook_core_cases_v1", corpus.Name)
	}
	if corpus.ValidationScope != "fixture_presence_and_shape_only" {
		t.Fatalf("validation_scope = %q; want fixture_presence_and_shape_only", corpus.ValidationScope)
	}

	required := map[string]bool{
		"broadcast_no_ack":                                  false,
		"initiator_initiator_ack_only":                      false,
		"initiator_target_timeout_then_success":             false,
		"initiator_target_nack_then_success":                false,
		"initiator_target_crc_mismatch_exhausted":           false,
		"arbitration_collision_retry_then_ack_only_success": false,
		"write_echo_collision_retry_then_response_success":  false,
	}

	for _, tc := range corpus.Cases {
		if tc.Name == "" || tc.SourceTest == "" || tc.FrameType == "" || tc.Expected.Outcome == "" {
			t.Fatalf("case %+v is missing required metadata", tc)
		}
		if _, ok := required[tc.Name]; ok {
			required[tc.Name] = true
		}
	}

	for name, seen := range required {
		if !seen {
			t.Fatalf("required observer corpus case %q missing from %s", name, path)
		}
	}
}

func TestProtocol_B524ProofMirrorManifest(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("testdata", "b524_proof_2026_03_05")
	manifestPath := filepath.Join(dir, "manifest.json")
	var manifest mirroredCorpus
	readJSONFixture(t, manifestPath, &manifest)

	if manifest.Name != "b524_proof_2026_03_05" {
		t.Fatalf("manifest name = %q; want b524_proof_2026_03_05", manifest.Name)
	}
	if manifest.ValidationScope != "corpus_presence_only" {
		t.Fatalf("validation_scope = %q; want corpus_presence_only", manifest.ValidationScope)
	}
	if manifest.MirrorSourceWorkspace != "_work_register_mapping/B524/proof_2026-03-05/" {
		t.Fatalf("mirror_source_workspace_path = %q; want _work_register_mapping/B524/proof_2026-03-05/", manifest.MirrorSourceWorkspace)
	}
	if len(manifest.Cases) < 17 {
		t.Fatalf("cases = %d; want at least 17 mirrored proof entries", len(manifest.Cases))
	}

	for _, tc := range manifest.Cases {
		if tc.Name == "" || tc.File == "" {
			t.Fatalf("mirrored case %+v is missing required metadata", tc)
		}
		if _, err := os.Stat(filepath.Join(dir, tc.File)); err != nil {
			t.Fatalf("mirrored proof file %s missing: %v", tc.File, err)
		}
	}
}

func readJSONFixture(t *testing.T, path string, target any) {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", path, err)
	}
}
