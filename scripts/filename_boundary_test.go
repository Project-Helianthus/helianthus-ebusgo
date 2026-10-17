package scripts

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

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
