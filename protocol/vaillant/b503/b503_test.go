package b503_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-ebusgo/protocol/vaillant/b503"
)

// readFixture reads a testdata file or fails the test.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func TestEncodeRequest_AllSelectors(t *testing.T) {
	cases := []struct {
		name            string
		family          byte
		selector        byte
		fixture         string
		helper          func() []byte
	}{
		{"Currenterror", 0x00, 0x01, "req_current_error.bin", b503.EncodeCurrentError},
		{"Errorhistory", 0x01, 0x01, "req_error_history.bin", b503.EncodeErrorHistory},
		{"Currentservice", 0x00, 0x02, "req_current_service.bin", b503.EncodeCurrentService},
		{"Servicehistory", 0x01, 0x02, "req_service_history.bin", b503.EncodeServiceHistory},
		{"LiveMonitorMain", 0x00, 0x03, "req_live_monitor_main.bin", b503.EncodeLiveMonitorMain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := readFixture(t, tc.fixture)
			got := b503.EncodeRequest(tc.family, tc.selector)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("EncodeRequest(%#x,%#x) = %x; want %x", tc.family, tc.selector, got, want)
			}
			if tc.helper != nil {
				h := tc.helper()
				if !reflect.DeepEqual(h, want) {
					t.Errorf("helper for %s = %x; want %x", tc.name, h, want)
				}
			}
		})
	}
}

func TestSafety_Classification(t *testing.T) {
	cases := []struct {
		family, selector byte
		want             b503.InvokeSafety
	}{
		{0x00, 0x01, b503.SafetyRead},          // Currenterror
		{0x01, 0x01, b503.SafetyRead},          // Errorhistory
		{0x02, 0x01, b503.SafetyInstallWrite},  // Clearerrorhistory
		{0x00, 0x02, b503.SafetyRead},          // Currentservice
		{0x01, 0x02, b503.SafetyRead},          // Servicehistory
		{0x02, 0x02, b503.SafetyInstallWrite},  // Clearservicehistory
		{0x00, 0x03, b503.SafetyServiceWrite},  // LiveMonitorMain
	}
	for _, tc := range cases {
		got, ok := b503.Safety(tc.family, tc.selector)
		if !ok {
			t.Errorf("Safety(%#x,%#x): ok=false, expected known entry", tc.family, tc.selector)
			continue
		}
		if got != tc.want {
			t.Errorf("Safety(%#x,%#x) = %v; want %v", tc.family, tc.selector, got, tc.want)
		}
	}

	// Unknown tuples
	if _, ok := b503.Safety(0x03, 0x01); ok {
		t.Errorf("Safety(0x03,0x01) unexpectedly classified as known")
	}
	if _, ok := b503.Safety(0x00, 0x09); ok {
		t.Errorf("Safety(0x00,0x09) unexpectedly classified as known")
	}
}

func TestInvokeSafety_String(t *testing.T) {
	if b503.SafetyRead.String() == "" || b503.SafetyServiceWrite.String() == "" || b503.SafetyInstallWrite.String() == "" {
		t.Fatalf("InvokeSafety.String() must return non-empty for all constants")
	}
}

func TestDecodeCurrentError_SpecFixture(t *testing.T) {
	// The golden.bin includes the 0x0a length byte per the spec §5.3 wire dump.
	// DecodeCurrentError accepts the 10-byte slot payload (length-byte already stripped).
	payload := readFixture(t, "current_error_bai00.payload.bin")
	rec, err := b503.DecodeCurrentError(payload)
	if err != nil {
		t.Fatalf("DecodeCurrentError: unexpected error: %v", err)
	}
	if rec.Slots[0] != 0x0119 {
		t.Errorf("slot[0] = %#x; want 0x0119", rec.Slots[0])
	}
	for i := 1; i < 5; i++ {
		if rec.Slots[i] != b503.EmptySlot {
			t.Errorf("slot[%d] = %#x; want EmptySlot (0xFFFF)", i, rec.Slots[i])
		}
	}
	v, present := rec.FirstActive()
	if !present || v != 0x0119 {
		t.Errorf("FirstActive = (%#x,%v); want (0x0119,true)", v, present)
	}
}

func TestDecodeCurrentError_AllSentinel(t *testing.T) {
	payload := readFixture(t, "current_error_all_empty.payload.bin")
	rec, err := b503.DecodeCurrentError(payload)
	if err != nil {
		t.Fatalf("DecodeCurrentError: %v", err)
	}
	for i, s := range rec.Slots {
		if s != b503.EmptySlot {
			t.Errorf("slot[%d] = %#x; want 0xFFFF", i, s)
		}
	}
	if _, present := rec.FirstActive(); present {
		t.Errorf("FirstActive present = true; want false on all-sentinel")
	}
}

func TestDecodeCurrentError_MixedSentinel(t *testing.T) {
	payload := readFixture(t, "current_error_mixed.payload.bin")
	rec, err := b503.DecodeCurrentError(payload)
	if err != nil {
		t.Fatalf("DecodeCurrentError: %v", err)
	}
	want := [5]uint16{0xFFFF, 0x0042, 0xFFFF, 0x0099, 0xFFFF}
	if rec.Slots != want {
		t.Errorf("Slots = %#v; want %#v", rec.Slots, want)
	}
	v, present := rec.FirstActive()
	if !present || v != 0x0042 {
		t.Errorf("FirstActive = (%#x,%v); want (0x0042,true)", v, present)
	}
}

func TestDecodeCurrentService_Parity(t *testing.T) {
	// Same shape as CurrentError: reuse the mixed fixture as a proxy.
	payload := readFixture(t, "current_error_mixed.payload.bin")
	rec, err := b503.DecodeCurrentService(payload)
	if err != nil {
		t.Fatalf("DecodeCurrentService: %v", err)
	}
	if rec.Slots[1] != 0x0042 || rec.Slots[3] != 0x0099 {
		t.Errorf("CurrentService slots = %#v; want slot[1]=0x0042 slot[3]=0x0099", rec.Slots)
	}
}

func TestDecodeErrorHistory_IndexEcho(t *testing.T) {
	// Fixture: first byte = index (0x07), then five LE uint16 slots.
	payload := readFixture(t, "error_history_index7.payload.bin")
	rec, err := b503.DecodeErrorHistory(payload)
	if err != nil {
		t.Fatalf("DecodeErrorHistory: %v", err)
	}
	if rec.Index != 0x07 {
		t.Errorf("Index = %#x; want 0x07", rec.Index)
	}
	if rec.Slots[1] != 0x0042 || rec.Slots[3] != 0x0099 {
		t.Errorf("history slots = %#v; want slot[1]=0x0042 slot[3]=0x0099", rec.Slots)
	}
}

func TestDecodeServiceHistory_IndexEcho(t *testing.T) {
	payload := readFixture(t, "error_history_index7.payload.bin")
	rec, err := b503.DecodeServiceHistory(payload)
	if err != nil {
		t.Fatalf("DecodeServiceHistory: %v", err)
	}
	if rec.Index != 0x07 {
		t.Errorf("Index = %#x; want 0x07", rec.Index)
	}
}

func TestDecodeLiveMonitorMain_SpecFixture(t *testing.T) {
	// Per spec §1: REQ 31 08 b5 03 02 00 03 → RESP 0a f4 01 ff ff ff ff ff ff ff ff.
	// After stripping the 0x0a length byte, payload starts with status=0xf4, function=0x01.
	payload := readFixture(t, "live_monitor_main.payload.bin")
	st, err := b503.DecodeLiveMonitorMain(payload)
	if err != nil {
		t.Fatalf("DecodeLiveMonitorMain: %v", err)
	}
	if st.Status != 0xf4 {
		t.Errorf("Status = %#x; want 0xf4", st.Status)
	}
	if st.Function != 0x01 {
		t.Errorf("Function = %#x; want 0x01", st.Function)
	}
	if len(st.Reserved) != len(payload)-2 {
		t.Errorf("Reserved len = %d; want %d", len(st.Reserved), len(payload)-2)
	}
}

func TestDecoders_ShortPayload_Error(t *testing.T) {
	short := []byte{0x00}
	if _, err := b503.DecodeCurrentError(short); err == nil {
		t.Errorf("DecodeCurrentError(short): expected error")
	}
	if _, err := b503.DecodeCurrentService(short); err == nil {
		t.Errorf("DecodeCurrentService(short): expected error")
	}
	if _, err := b503.DecodeErrorHistory(short); err == nil {
		t.Errorf("DecodeErrorHistory(short): expected error")
	}
	if _, err := b503.DecodeServiceHistory(short); err == nil {
		t.Errorf("DecodeServiceHistory(short): expected error")
	}
	if _, err := b503.DecodeLiveMonitorMain(short); err == nil {
		t.Errorf("DecodeLiveMonitorMain(short): expected error")
	}
}

// TestNoInstallWriteHelpers asserts the v1 invariant (plan AD02, spec §9):
// the package MUST NOT export any helper that constructs a clear / install-write
// request frame.
func TestNoInstallWriteHelpers(t *testing.T) {
	// Walk package-level exports by using Go's reflect on a sentinel value from
	// the package. We cannot enumerate package symbols from reflect alone, so
	// instead we inspect the go source files. The simplest in-package check:
	// compile the canary file. Here we rely on the fact that any Encode*Clear*
	// symbol would be both exported and referenced in the package's exported API.
	// We ship a companion canary via package-source scan.
	forbidden := []string{"EncodeClearErrorHistory", "EncodeClearServiceHistory", "EncodeClear"}
	root := "."
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(data)
		for _, f := range forbidden {
			// Match "func <Forbidden>(" to catch exports.
			needle := "func " + f
			if strings.Contains(src, needle) {
				t.Errorf("package exports forbidden install-write helper %q (spec §9, plan AD02) in %s", f, e.Name())
			}
		}
	}
}

// TestNoFxxxLookupTable asserts plan AD05 / spec §10: no cross-device F.xxx
// translation table lives in this package.
func TestNoFxxxLookupTable(t *testing.T) {
	forbidden := []string{"errorCodeToFString", "fCodeTable", "FxxxLookup", "FCodeMap"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		src := string(data)
		for _, f := range forbidden {
			if strings.Contains(src, f) {
				t.Errorf("package contains forbidden F.xxx lookup symbol %q (spec §10, plan AD05) in %s", f, e.Name())
			}
		}
	}
}
