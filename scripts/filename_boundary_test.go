package scripts

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copyRepositoryForGate copies the repository working tree, without VCS metadata or scratch output.
func copyRepositoryForGate(t *testing.T, dst string) {
	t.Helper()
	src, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if top == ".git" || top == ".tmp" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func runPublicSymbolGate(t *testing.T, root string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "scripts/public_symbol_gate.sh")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("public symbol gate timed out: %v; output: %s", ctx.Err(), output)
	}
	if err == nil {
		return string(output), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("public symbol gate: %v; output: %s", err, output)
	}
	return string(output), exitErr.ExitCode()
}

func TestPublicSymbolGateVendorNeutralImports(t *testing.T) {
	const module = "github.com/Project-Helianthus/helianthus-ebusgo"
	const forbidden = module + "/protocol/vaillant/b503"
	const probe = module + "/engine/probe"
	want := "public symbol gate: vendor-neutral path imports " + forbidden + " (" + probe + ")"

	clean := t.TempDir()
	copyRepositoryForGate(t, clean)
	if output, status := runPublicSymbolGate(t, clean); status != 0 {
		t.Fatalf("unmodified copy: exit %d, want 0; output: %s", status, output)
	}

	probed := t.TempDir()
	copyRepositoryForGate(t, probed)
	if err := os.MkdirAll(filepath.Join(probed, "engine", "probe"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := "package probe\n\nimport _ \"" + forbidden + "\"\n"
	if err := os.WriteFile(filepath.Join(probed, "engine", "probe", "probe.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	output, status := runPublicSymbolGate(t, probed)
	if status != 1 {
		t.Fatalf("probed copy: exit %d, want 1; output: %s", status, output)
	}
	if !strings.Contains(output, want) {
		t.Fatalf("probed copy output %q does not contain %q", output, want)
	}

	// A production file excluded by build constraints must still be checked: go list omits its imports.
	constrained := t.TempDir()
	copyRepositoryForGate(t, constrained)
	const constrainedSource = "//go:build local_gpio\n\npackage transport\n\nimport _ \"" + forbidden + "\"\n"
	if err := os.WriteFile(filepath.Join(constrained, "transport", "probe_local_gpio.go"), []byte(constrainedSource), 0o644); err != nil {
		t.Fatal(err)
	}
	output, status = runPublicSymbolGate(t, constrained)
	wantConstrained := "public symbol gate: vendor-neutral path imports " + forbidden + " (" + module + "/transport)"
	if status != 1 {
		t.Fatalf("build-constrained probe: exit %d, want 1; output: %s", status, output)
	}
	if !strings.Contains(output, wantConstrained) {
		t.Fatalf("build-constrained probe output %q does not contain %q", output, wantConstrained)
	}

	// A package whose every production file is excluded by build constraints is omitted by go list entirely.
	allConstrained := t.TempDir()
	copyRepositoryForGate(t, allConstrained)
	if err := os.MkdirAll(filepath.Join(allConstrained, "engine", "gpioprobe"), 0o755); err != nil {
		t.Fatal(err)
	}
	const gpioSource = "//go:build local_gpio\n\npackage gpioprobe\n\nimport _ \"" + forbidden + "\"\n"
	if err := os.WriteFile(filepath.Join(allConstrained, "engine", "gpioprobe", "probe.go"), []byte(gpioSource), 0o644); err != nil {
		t.Fatal(err)
	}
	output, status = runPublicSymbolGate(t, allConstrained)
	wantAllConstrained := "public symbol gate: vendor-neutral path imports " + forbidden + " (" + module + "/engine/gpioprobe)"
	if status != 1 {
		t.Fatalf("fully build-constrained probe: exit %d, want 1; output: %s", status, output)
	}
	if !strings.Contains(output, wantAllConstrained) {
		t.Fatalf("fully build-constrained probe output %q does not contain %q", output, wantAllConstrained)
	}
}

func q03ClassificationBlock(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("transport_gate.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	const start = "\nrequires_gate=0\n"
	const end = "\ndone <<< \"${changed_files}\"\n"
	if strings.Count(text, start) != 1 || strings.Count(text, end) != 1 {
		t.Fatal("classification extraction anchors must each occur exactly once")
	}
	first, last := strings.Index(text, start), strings.Index(text, end)
	if last <= first {
		t.Fatal("classification extraction anchors are out of order")
	}
	return text[first : last+len(end)]
}

func q03Classify(t *testing.T, block, files string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Supply fixtures as data, and execute only the extracted classification block.
	cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-c",
		"set -euo pipefail\nchanged_files=$1\n"+block+"\nprintf '%s' \"${requires_gate}\"\n",
		"q03", files)
	// Do not inherit shell startup hooks or exported functions from the caller.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("classification subprocess timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("classification subprocess: %v; output: %s", err, output)
	}
	return string(output)
}

func TestQ03FilenameBoundary(t *testing.T) {
	block := q03ClassificationBlock(t)
	// Mutate the real production predicate, never a copied classifier.
	const predicate = "*_test.go)"
	if strings.Count(block, predicate) != 1 {
		t.Fatal("expected exactly one nonempty test-file predicate mutation target")
	}
	mutant := strings.Replace(block, predicate, "*test.go)", 1)
	cases := []struct {
		name, files, want string
	}{
		{"transport_latest", "transport/latest.go", "1"},
		{"protocol_contest", "protocol/contest.go", "1"},
		{"readme_before_latest", "README.md\ntransport/latest.go", "1"},
		{"contest_before_readme", "protocol/contest.go\nREADME.md", "1"},
		{"readme_before_contest", "README.md\nprotocol/contest.go", "1"},
		{"latest_before_readme", "transport/latest.go\nREADME.md", "1"},
		{"both_with_readme", "README.md\ntransport/latest.go\nprotocol/contest.go", "1"},
		{"unrelated_only", "README.md\nscripts/unrelated.sh", "0"},
		{"transport_engine_cache", "transport/engine_cache.go", "0"},
		{"transport_responder_timing", "transport/responder_timing.go", "0"},
		{"protocol_responder_frame", "protocol/responder/frame.go", "0"},
		{"adaptermux_mux", "adaptermux/mux.go", "0"},
		{"engine_scheduler", "engine/scheduler.go", "0"},
		{"engine_cache_with_latest", "transport/engine_cache.go\ntransport/latest.go", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := q03Classify(t, block, tc.files); got != tc.want {
				t.Errorf("production classification = %q, want %q", got, tc.want)
			}
		})
		t.Run("broad_suffix_mutant/"+tc.name, func(t *testing.T) {
			got := q03Classify(t, mutant, tc.files)
			if got != "0" {
				t.Fatalf("broad-suffix mutant classification = %q, want 0", got)
			}
			if tc.want == "1" && got == tc.want {
				t.Fatal("production-like filename did not expose the broad-suffix mutant")
			}
		})
	}
}
