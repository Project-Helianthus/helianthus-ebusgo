package protocol_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/transport"
)

type fixtureExpectation struct {
	Name       string `json:"name"`
	SymbolsHex string `json:"symbols_hex"`
}

func TestProtocol_FixtureReplay(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("testdata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, err)
	}

	var binFiles []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".bin") {
			continue
		}
		binFiles = append(binFiles, filepath.Join(dir, entry.Name()))
	}
	if len(binFiles) == 0 {
		t.Fatalf("no fixtures found in %s", dir)
	}

	for _, binPath := range binFiles {
		binPath := binPath
		t.Run(filepath.Base(binPath), func(t *testing.T) {
			t.Parallel()

			payload, err := os.ReadFile(binPath)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v", binPath, err)
			}

			jsonPath := strings.TrimSuffix(binPath, ".bin") + ".json"
			rawJSON, err := os.ReadFile(jsonPath)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v", jsonPath, err)
			}

			var expect fixtureExpectation
			if err := json.Unmarshal(rawJSON, &expect); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", jsonPath, err)
			}
			hexStr := strings.ReplaceAll(expect.SymbolsHex, " ", "")
			hexStr = strings.ReplaceAll(hexStr, "\n", "")
			want, err := hex.DecodeString(hexStr)
			if err != nil {
				t.Fatalf("Decode symbols_hex error = %v", err)
			}

			var parser transport.ENHParser
			msgs, err := parser.Parse(payload)
			if err != nil {
				t.Fatalf("Parse fixture %s error = %v", binPath, err)
			}

			var got []byte
			for _, msg := range msgs {
				if msg.Command == transport.ENHResReceived {
					got = append(got, msg.Data)
				}
			}

			if !bytes.Equal(got, want) {
				t.Fatalf("decoded symbols = %x; want %x", got, want)
			}
		})
	}
}
