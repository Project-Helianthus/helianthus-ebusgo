//go:build !tinygo

package transport

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ebuserrors "github.com/d3vi1/helianthus-ebusgo/errors"
)

type ebusdTCPFixture struct {
	Name    string   `json:"name"`
	Lines   []string `json:"lines"`
	WantHex string   `json:"want_hex"`
	WantErr string   `json:"want_err"`
}

func loadEbusdTCPFixtures(t *testing.T) []ebusdTCPFixture {
	t.Helper()

	path := filepath.Join("testdata", "ebusd_tcp_responses.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}

	var fixtures []ebusdTCPFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("jsonUnmarshal error = %v", err)
	}
	return fixtures
}

func decodeFixtureHex(t *testing.T, hexText string) []byte {
	t.Helper()

	cleaned := strings.NewReplacer(" ", "", "\t", "", "\n", "").Replace(hexText)
	if cleaned == "" {
		return nil
	}
	payload, err := hex.DecodeString(cleaned)
	if err != nil {
		t.Fatalf("DecodeString(%q) error = %v", cleaned, err)
	}
	return payload
}

func TestParseHexResponseLines_Fixtures(t *testing.T) {
	t.Parallel()

	fixtures := loadEbusdTCPFixtures(t)
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()

			got, err := parseHexResponseLines(fixture.Lines)
			switch fixture.WantErr {
			case "":
				if err != nil {
					t.Fatalf("parseHexResponseLines error = %v", err)
				}
			case "timeout":
				if !errors.Is(err, ebuserrors.ErrTimeout) {
					t.Fatalf("parseHexResponseLines error = %v; want ErrTimeout", err)
				}
				return
			case "invalid":
				if !errors.Is(err, ebuserrors.ErrInvalidPayload) {
					t.Fatalf("parseHexResponseLines error = %v; want ErrInvalidPayload", err)
				}
				return
			case "no_hex":
				if !errors.Is(err, errNoHexPayload) {
					t.Fatalf("parseHexResponseLines error = %v; want errNoHexPayload", err)
				}
				return
			default:
				t.Fatalf("unknown WantErr=%q", fixture.WantErr)
			}

			if fixture.WantHex == "" {
				if len(got) != 0 {
					t.Fatalf("parseHexResponseLines = %v; want empty", got)
				}
				return
			}

			want := decodeFixtureHex(t, fixture.WantHex)
			if !bytes.Equal(got, want) {
				t.Fatalf("parseHexResponseLines = %v; want %v", got, want)
			}
		})
	}
}

func TestParseHexResponseLines_FirstHexLine(t *testing.T) {
	t.Parallel()

	lines := []string{
		"  0x03 01 02 03  ",
		"ERR: spurious",
		"ERR: timeout",
	}

	got, err := parseHexResponseLines(lines)
	if err != nil {
		t.Fatalf("parseHexResponseLines error = %v", err)
	}
	want := []byte{0x03, 0x01, 0x02, 0x03}
	if !bytes.Equal(got, want) {
		t.Fatalf("parseHexResponseLines = %v; want %v", got, want)
	}
}

func TestStripLengthPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload []byte
		want    []byte
	}{
		{
			name:    "strip matching prefix",
			payload: []byte{0x03, 0x01, 0x02, 0x03},
			want:    []byte{0x01, 0x02, 0x03},
		},
		{
			name:    "strip single byte payload",
			payload: []byte{0x01, 0x99},
			want:    []byte{0x99},
		},
		{
			name:    "leave mismatched prefix",
			payload: []byte{0x05, 0x01, 0x02},
			want:    []byte{0x05, 0x01, 0x02},
		},
		{
			name:    "leave short payload",
			payload: []byte{0x00},
			want:    []byte{0x00},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := stripLengthPrefix(test.payload)
			if !bytes.Equal(got, test.want) {
				t.Fatalf("stripLengthPrefix(%v) = %v; want %v", test.payload, got, test.want)
			}
		})
	}
}

func TestParseHexResponseLines_DoneBroadcast(t *testing.T) {
	t.Parallel()

	lines := []string{"done broadcast"}
	_, err := parseHexResponseLines(lines)
	if !errors.Is(err, errNoHexPayload) {
		t.Fatalf("parseHexResponseLines error = %v; want errNoHexPayload", err)
	}
}

func TestEbusdTCPTransport_SendHexCommand_StripsLengthPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		src         byte
		payload     []byte
		response    string
		wantCommand string
		wantPayload []byte
	}{
		{
			name:        "strips length prefix",
			src:         0x08,
			payload:     []byte{0x10, 0xB5, 0x24, 0x06},
			response:    "02 11 22\n\n",
			wantCommand: "hex -s 08 10b52406\n",
			wantPayload: []byte{0x11, 0x22},
		},
		{
			name:        "preserves single byte",
			src:         0x15,
			payload:     []byte{0x08, 0xB5, 0x09, 0x00},
			response:    "00\n\n",
			wantCommand: "hex -s 15 08b50900\n",
			wantPayload: []byte{0x00},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			tr := NewEbusdTCPTransport(client)

			serverErr := make(chan error, 1)
			// Goroutine exits after validating the command and writing one response.
			go func() {
				reader := bufio.NewReader(server)
				line, err := reader.ReadString('\n')
				if err != nil {
					serverErr <- err
					return
				}
				if line != test.wantCommand {
					serverErr <- fmt.Errorf("command = %q; want %q", line, test.wantCommand)
					return
				}
				_, err = server.Write([]byte(test.response))
				serverErr <- err
			}()

			got, err := tr.sendHexCommand(test.src, test.payload)
			if err != nil {
				t.Fatalf("sendHexCommand error = %v", err)
			}
			if !bytes.Equal(got, test.wantPayload) {
				t.Fatalf("sendHexCommand = %v; want %v", got, test.wantPayload)
			}
			if err := <-serverErr; err != nil {
				t.Fatalf("server error = %v", err)
			}
		})
	}
}

func TestEbusdTCPTransport_Write_BroadcastDone(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	tr := NewEbusdTCPTransport(client)

	src := byte(0x10)
	telegram := []byte{
		src,
		ebusBroadcast,
		0xB5,
		0x24,
		0x01,
		0x02,
		0x00,
	}
	raw := append(telegram, ebusSymbolSyn)
	wantCommand := "hex -s 10 feb5240102\n"

	serverErr := make(chan error, 1)
	// Goroutine exits after returning the broadcast response line.
	go func() {
		reader := bufio.NewReader(server)
		line, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			return
		}
		if line != wantCommand {
			serverErr <- fmt.Errorf("command = %q; want %q", line, wantCommand)
			return
		}
		_, err = server.Write([]byte("done broadcast\n\n"))
		serverErr <- err
	}()

	written, err := tr.Write(raw)
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if written != len(raw) {
		t.Fatalf("Write = %d; want %d", written, len(raw))
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.pendingErr != nil {
		t.Fatalf("pendingErr = %v; want nil", tr.pendingErr)
	}
	if len(tr.pending) != len(raw) {
		t.Fatalf("pending = %d; want %d", len(tr.pending), len(raw))
	}
}
