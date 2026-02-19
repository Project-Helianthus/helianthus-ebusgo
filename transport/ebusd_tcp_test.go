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
	"time"

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

func TestParseHexResponseLines_WhitespaceHex(t *testing.T) {
	t.Parallel()

	lines := []string{" \t0x02 \t11  22\t "}
	got, err := parseHexResponseLines(lines)
	if err != nil {
		t.Fatalf("parseHexResponseLines error = %v", err)
	}
	want := []byte{0x02, 0x11, 0x22}
	if !bytes.Equal(got, want) {
		t.Fatalf("parseHexResponseLines = %v; want %v", got, want)
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
			defer func() { _ = client.Close() }()
			defer func() { _ = server.Close() }()

			tr := NewEbusdTCPTransport(client, 0, 0)

			serverErr := make(chan error, 1)
			// Goroutine exits after validating the command and writing one response.
			go func() {
				reader := bufio.NewReader(server)
				line, err := reader.ReadString('\n')
				if err != nil {
					serverErr <- err
					_ = server.Close()
					return
				}
				if line != test.wantCommand {
					serverErr <- fmt.Errorf("command = %q; want %q", line, test.wantCommand)
					_ = server.Close()
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

func TestEbusdTCPTransport_SendHexCommand_CommandFormatting(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	tr := NewEbusdTCPTransport(client, 0, 0)

	src := byte(0x03)
	payload := []byte{0x00, 0x0A, 0xF0}
	wantCommand := "hex -s 03 000af0\n"

	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		line, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			_ = server.Close()
			return
		}
		if line != wantCommand {
			serverErr <- fmt.Errorf("command = %q; want %q", line, wantCommand)
			_ = server.Close()
			return
		}
		_, err = server.Write([]byte("01 00\n\n"))
		serverErr <- err
	}()

	got, err := tr.sendHexCommand(src, payload)
	if err != nil {
		t.Fatalf("sendHexCommand error = %v", err)
	}
	wantPayload := []byte{0x00}
	if !bytes.Equal(got, wantPayload) {
		t.Fatalf("sendHexCommand = %v; want %v", got, wantPayload)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestEbusdTCPTransport_SendHexCommand_ErrLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		wantErr  error
	}{
		{
			name:     "timeout",
			response: "ERR: timeout waiting for response\n\n",
			wantErr:  ebuserrors.ErrTimeout,
		},
		{
			name:     "no answer",
			response: "ERR: no answer\n\n",
			wantErr:  ebuserrors.ErrTimeout,
		},
		{
			name:     "access denied",
			response: "ERR: access denied\n\n",
			wantErr:  ebuserrors.ErrInvalidPayload,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			defer func() { _ = server.Close() }()

			tr := NewEbusdTCPTransport(client, 0, 0)
			wantCommand := "hex -s 08 01\n"

			serverErr := make(chan error, 1)
			go func() {
				reader := bufio.NewReader(server)
				line, err := reader.ReadString('\n')
				if err != nil {
					serverErr <- err
					_ = server.Close()
					return
				}
				if line != wantCommand {
					serverErr <- fmt.Errorf("command = %q; want %q", line, wantCommand)
					_ = server.Close()
					return
				}
				_, err = server.Write([]byte(test.response))
				serverErr <- err
			}()

			_, err := tr.sendHexCommand(0x08, []byte{0x01})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("sendHexCommand error = %v; want %v", err, test.wantErr)
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
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	tr := NewEbusdTCPTransport(client, 0, 0)

	src := byte(0x10)
	telegram := []byte{src, ebusBroadcast, 0xB5, 0x24, 0x01, 0x02}
	telegram = append(telegram, crcValue(telegram))
	wantCommand := "hex -s 10 feb5240102\n"

	serverErr := make(chan error, 1)
	// Goroutine exits after returning the broadcast response line.
	go func() {
		reader := bufio.NewReader(server)
		line, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			_ = server.Close()
			return
		}
		if line != wantCommand {
			serverErr <- fmt.Errorf("command = %q; want %q", line, wantCommand)
			_ = server.Close()
			return
		}
		_, err = server.Write([]byte("done broadcast\n\n"))
		serverErr <- err
	}()

	written, err := tr.Write(telegram)
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if written != len(telegram) {
		t.Fatalf("Write = %d; want %d", written, len(telegram))
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.pendingErr != nil {
		t.Fatalf("pendingErr = %v; want nil", tr.pendingErr)
	}
	if len(tr.pending) != len(telegram) {
		t.Fatalf("pending = %d; want %d", len(tr.pending), len(telegram))
	}
}

func TestEbusdTCPTransport_Write_CommandInjectsAckAndResponse(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	tr := NewEbusdTCPTransport(client, 0, 0)

	src := byte(0x31)
	dst := byte(0x15)
	pb := byte(0x07)
	sb := byte(0x04)

	telegram := []byte{src, dst, pb, sb, 0x00}
	telegram = append(telegram, crcValue(telegram))

	wantCommand := "hex -s 31 15070400\n"

	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		line, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			_ = server.Close()
			return
		}
		if line != wantCommand {
			serverErr <- fmt.Errorf("command = %q; want %q", line, wantCommand)
			_ = server.Close()
			return
		}
		_, err = server.Write([]byte("03 11 22 33\n\n"))
		serverErr <- err
	}()

	for _, b := range telegram {
		if _, err := tr.Write([]byte{b}); err != nil {
			t.Fatalf("Write error = %v", err)
		}
		echo, err := tr.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte (echo) error = %v", err)
		}
		if echo != b {
			t.Fatalf("echo = 0x%02x; want 0x%02x", echo, b)
		}
	}

	ack, err := tr.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte (ack) error = %v", err)
	}
	if ack != ebusSymbolAck {
		t.Fatalf("ack = 0x%02x; want 0x%02x", ack, ebusSymbolAck)
	}

	respData := []byte{0x11, 0x22, 0x33}
	respTelegram := []byte{dst, src, pb, sb, byte(len(respData))}
	respTelegram = append(respTelegram, respData...)
	respTelegram = append(respTelegram, crcValue(respTelegram))

	gotResp := make([]byte, len(respTelegram))
	for i := range gotResp {
		value, err := tr.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte (response) error = %v", err)
		}
		gotResp[i] = value
	}
	if !bytes.Equal(gotResp, respTelegram) {
		t.Fatalf("response = %x; want %x", gotResp, respTelegram)
	}

	if _, err := tr.Write([]byte{ebusSymbolAck}); err != nil {
		t.Fatalf("Write (response ack) error = %v", err)
	}
	echo, err := tr.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte (response ack echo) error = %v", err)
	}
	if echo != ebusSymbolAck {
		t.Fatalf("response ack echo = 0x%02x; want 0x%02x", echo, ebusSymbolAck)
	}

	if _, err := tr.Write([]byte{ebusSymbolSyn}); err != nil {
		t.Fatalf("Write (syn) error = %v", err)
	}
	echo, err = tr.ReadByte()
	if err != nil {
		t.Fatalf("ReadByte (syn echo) error = %v", err)
	}
	if echo != ebusSymbolSyn {
		t.Fatalf("syn echo = 0x%02x; want 0x%02x", echo, ebusSymbolSyn)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestEbusdTCPTransport_Write_AllowsEchoDrainWhileWaitingForEbusd(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	tr := NewEbusdTCPTransport(client, 0, 0)

	src := byte(0x31)
	dst := byte(0x15)
	pb := byte(0x07)
	sb := byte(0x04)

	telegram := []byte{src, dst, pb, sb, 0x00}
	telegram = append(telegram, crcValue(telegram))

	wantCommand := "hex -s 31 15070400\n"

	commandRead := make(chan struct{})
	allowResponse := make(chan struct{})
	serverErr := make(chan error, 1)
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
		close(commandRead)
		<-allowResponse
		_, err = server.Write([]byte("0a b5 41 53 56 32 05 07 17 04\n\n"))
		serverErr <- err
	}()

	writeErr := make(chan error, 1)
	go func() {
		_, err := tr.Write(telegram)
		writeErr <- err
	}()

	select {
	case <-commandRead:
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("server did not receive command line within timeout")
	}

	// While ebusd is blocked waiting to respond, we should still be able to drain
	// the echoed request bytes from the transport. If Write() holds the internal
	// lock during sendHexCommand, ReadByte() would block here.
	for _, b := range telegram {
		valueCh := make(chan struct {
			value byte
			err   error
		}, 1)
		go func() {
			value, err := tr.ReadByte()
			valueCh <- struct {
				value byte
				err   error
			}{value: value, err: err}
		}()
		select {
		case got := <-valueCh:
			if got.err != nil {
				t.Fatalf("ReadByte (echo) error = %v", got.err)
			}
			if got.value != b {
				t.Fatalf("echo = 0x%02x; want 0x%02x", got.value, b)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("ReadByte (echo) timed out; likely blocked on internal mutex")
		}
	}

	close(allowResponse)

	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("Write error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Write did not complete after server response")
	}

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not complete within timeout")
	}
}

func TestEbusdTCPTransport_SendHexCommand_HandlesNoiseBeforeHex(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	tr := NewEbusdTCPTransport(client, 500*time.Millisecond, 0)
	wantCommand := "hex -s 31 feb5240100\n"

	serverErr := make(chan error, 1)
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

		if _, err := server.Write([]byte("\n")); err != nil {
			serverErr <- err
			return
		}
		if _, err := server.Write([]byte("dump enabled\n")); err != nil {
			serverErr <- err
			return
		}
		if _, err := server.Write([]byte("ERR: invalid numeric argument\n")); err != nil {
			serverErr <- err
			return
		}
		time.Sleep(40 * time.Millisecond)
		if _, err := server.Write([]byte("0400004040\n\n")); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	got, err := tr.sendHexCommand(0x31, []byte{0xFE, 0xB5, 0x24, 0x01, 0x00})
	if err != nil {
		t.Fatalf("sendHexCommand error = %v", err)
	}
	want := []byte{0x00, 0x00, 0x40, 0x40}
	if !bytes.Equal(got, want) {
		t.Fatalf("sendHexCommand = %x; want %x", got, want)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestEbusdTCPTransport_SendHexCommand_DrainsTrailingLinesBeforeNextCommand(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	tr := NewEbusdTCPTransport(client, 500*time.Millisecond, 0)

	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)

		first, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			return
		}
		if first != "hex -s 31 15070400\n" {
			serverErr <- fmt.Errorf("first command = %q; want %q", first, "hex -s 31 15070400\n")
			return
		}
		if _, err := server.Write([]byte("01aa\n")); err != nil {
			serverErr <- err
			return
		}
		if _, err := server.Write([]byte("ERR: invalid numeric argument\n\n")); err != nil {
			serverErr <- err
			return
		}

		second, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			return
		}
		if second != "hex -s 31 15070401\n" {
			serverErr <- fmt.Errorf("second command = %q; want %q", second, "hex -s 31 15070401\n")
			return
		}
		if _, err := server.Write([]byte("01bb\n\n")); err != nil {
			serverErr <- err
			return
		}

		serverErr <- nil
	}()

	gotFirst, err := tr.sendHexCommand(0x31, []byte{0x15, 0x07, 0x04, 0x00})
	if err != nil {
		t.Fatalf("first sendHexCommand error = %v", err)
	}
	if !bytes.Equal(gotFirst, []byte{0xAA}) {
		t.Fatalf("first sendHexCommand = %x; want aa", gotFirst)
	}

	gotSecond, err := tr.sendHexCommand(0x31, []byte{0x15, 0x07, 0x04, 0x01})
	if err != nil {
		t.Fatalf("second sendHexCommand error = %v", err)
	}
	if !bytes.Equal(gotSecond, []byte{0xBB}) {
		t.Fatalf("second sendHexCommand = %x; want bb", gotSecond)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestEbusdTCPTransport_Write_SerializesConcurrentHexCommands(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	tr := NewEbusdTCPTransport(client, 500*time.Millisecond, 0)

	telegramA := []byte{0x31, 0xFE, 0x07, 0xFE, 0x00}
	telegramA = append(telegramA, crcValue(telegramA))
	commandA := "hex -s 31 fe07fe00\n"

	telegramB := []byte{0x10, 0xFE, 0xB5, 0x16, 0x01, 0x01}
	telegramB = append(telegramB, crcValue(telegramB))
	commandB := "hex -s 10 feb5160101\n"

	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		first, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			return
		}

		_ = server.SetReadDeadline(time.Now().Add(60 * time.Millisecond))
		secondEarly, err := reader.ReadString('\n')
		if err == nil {
			serverErr <- fmt.Errorf("second command arrived before first response: %q", secondEarly)
			return
		}
		if !isTimeout(err) {
			serverErr <- fmt.Errorf("unexpected pre-response read error: %v", err)
			return
		}
		_ = server.SetReadDeadline(time.Time{})

		if _, err := server.Write([]byte("done broadcast\n\n")); err != nil {
			serverErr <- err
			return
		}

		second, err := reader.ReadString('\n')
		if err != nil {
			serverErr <- err
			return
		}
		if _, err := server.Write([]byte("done broadcast\n\n")); err != nil {
			serverErr <- err
			return
		}

		if (first != commandA || second != commandB) && (first != commandB || second != commandA) {
			serverErr <- fmt.Errorf("commands = [%q, %q]; want permutations of [%q, %q]", first, second, commandA, commandB)
			return
		}

		serverErr <- nil
	}()

	writeErr := make(chan error, 2)
	go func() {
		_, err := tr.Write(telegramA)
		writeErr <- err
	}()
	go func() {
		_, err := tr.Write(telegramB)
		writeErr <- err
	}()

	for index := 0; index < 2; index++ {
		if err := <-writeErr; err != nil {
			t.Fatalf("Write #%d error = %v", index+1, err)
		}
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server error = %v", err)
	}
}
