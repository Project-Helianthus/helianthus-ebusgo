//go:build linux || darwin

package capture

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

// The mapped files below are synthetic temporary regular files. No test opens
// a device node.
func writeWindowFile(t *testing.T, words []uint32) string {
	t.Helper()
	source := make([]byte, 4*len(words))
	for index, word := range words {
		nativePut(source[4*index:], word)
	}
	path := filepath.Join(t.TempDir(), "aa31-window")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// nativePut stores a word in host byte order, which is the order an
// aligned 32-bit load reads it back in.
func nativePut(dst []byte, word uint32) {
	var probe uint16 = 1
	if *(*byte)(unsafe.Pointer(&probe)) == 1 {
		binary.LittleEndian.PutUint32(dst, word)
		return
	}
	binary.BigEndian.PutUint32(dst, word)
}

// Positive control. Catches: loading the wrong offset or width, reporting a
// regular file as a character device, or an unmap that is not idempotent.
func TestAA31MappingLoadsAlignedWordsReadOnly(t *testing.T) {
	words := []uint32{0x01020304, 0xA5A5A5A5, 0x00000008, 0xFFFFFFFF}
	path := writeWindowFile(t, words)
	mapping, err := MapReadOnly(path, 0, 16)
	if err != nil {
		t.Fatalf("aa31 map: %v", err)
	}
	if mapping.Length() != 16 || mapping.CharacterDevice() {
		t.Fatalf("aa31 mapping length %d character device %v", mapping.Length(), mapping.CharacterDevice())
	}
	for index, want := range words {
		if got := mapping.Load32(uint64(4 * index)); got != want {
			t.Fatalf("aa31 load %d = %#x, want %#x", 4*index, got, want)
		}
	}
	for _, offset := range []uint64{1, 2, 3, 16, 20, ^uint64(0) - 3} {
		if got := mapping.Load32(offset); got != 0 {
			t.Fatalf("aa31 invalid load %d = %#x", offset, got)
		}
	}
	if mapping.InvalidLoads() != 6 {
		t.Fatalf("aa31 invalid loads = %d", mapping.InvalidLoads())
	}
	if err := mapping.Unmap(); err != nil {
		t.Fatalf("aa31 unmap: %v", err)
	}
	if err := mapping.Unmap(); err != nil || mapping.Length() != 0 {
		t.Fatalf("aa31 second unmap = %v length %d", err, mapping.Length())
	}
	if got := mapping.Load32(0); got != 0 || mapping.InvalidLoads() != 7 {
		t.Fatalf("aa31 load after unmap = %#x invalid %d", got, mapping.InvalidLoads())
	}
}

// Catches: mapping with no owner-named path, a length outside the window
// bounds, an unaligned file offset, or a window past the end of a regular file
// (whose loads would fault).
func TestAA31MappingRefusesInvalidRequests(t *testing.T) {
	path := writeWindowFile(t, make([]uint32, 8))
	page := uint64(os.Getpagesize())
	cases := map[string]struct {
		path   string
		offset uint64
		length uint64
	}{
		"no_path":         {"", 0, 16},
		"missing_file":    {filepath.Join(t.TempDir(), "aa31-absent"), 0, 16},
		"zero_length":     {path, 0, 0},
		"short_length":    {path, 0, 2},
		"unaligned_width": {path, 0, 6},
		"over_length":     {path, 0, MaxMapLength + 4},
		"unaligned_page":  {path, 4, 16},
		"past_file_end":   {path, 0, 36},
		"offset_past_end": {path, page, 16},
		"huge_offset":     {path, 1<<62 + page, 16},
	}
	for name, tc := range cases {
		if mapping, err := MapReadOnly(tc.path, tc.offset, tc.length); !errors.Is(err, ErrMapping) || mapping != nil {
			t.Fatalf("aa31 %s map = %v, %v", name, mapping, err)
		}
	}
	mapping, err := MapReadOnly(path, 0, 32)
	if err != nil {
		t.Fatalf("aa31 whole-file map: %v", err)
	}
	if err := mapping.Unmap(); err != nil {
		t.Fatal(err)
	}
}
