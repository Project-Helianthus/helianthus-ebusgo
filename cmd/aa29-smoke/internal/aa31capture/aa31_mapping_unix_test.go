//go:build linux || darwin

package aa31capture

import (
	aa31binary "encoding/binary"
	aa31errors "errors"
	aa31os "os"
	aa31filepath "path/filepath"
	aa31testing "testing"
	aa31unsafe "unsafe"
)

// The mapped files below are synthetic temporary regular files. No test opens
// a device node.
func aa31WriteWindowFile(aa31T *aa31testing.T, aa31Words []uint32) string {
	aa31T.Helper()
	aa31Source := make([]byte, 4*len(aa31Words))
	for aa31Index, aa31Word := range aa31Words {
		aa31NativePut(aa31Source[4*aa31Index:], aa31Word)
	}
	aa31Path := aa31filepath.Join(aa31T.TempDir(), "aa31-window")
	if aa31Err := aa31os.WriteFile(aa31Path, aa31Source, 0o600); aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
	return aa31Path
}

// aa31NativePut stores a word in host byte order, which is the order an
// aligned 32-bit load reads it back in.
func aa31NativePut(aa31Dst []byte, aa31Word uint32) {
	var aa31Probe uint16 = 1
	if *(*byte)(aa31unsafe.Pointer(&aa31Probe)) == 1 {
		aa31binary.LittleEndian.PutUint32(aa31Dst, aa31Word)
		return
	}
	aa31binary.BigEndian.PutUint32(aa31Dst, aa31Word)
}

// Positive control. Catches: loading the wrong offset or width, reporting a
// regular file as a character device, or an unmap that is not idempotent.
func TestAA31MappingLoadsAlignedWordsReadOnly(aa31T *aa31testing.T) {
	aa31Words := []uint32{0x01020304, 0xA5A5A5A5, 0x00000008, 0xFFFFFFFF}
	aa31Path := aa31WriteWindowFile(aa31T, aa31Words)
	aa31Mapping, aa31Err := AA31MapReadOnly(aa31Path, 0, 16)
	if aa31Err != nil {
		aa31T.Fatalf("aa31 map: %v", aa31Err)
	}
	if aa31Mapping.AA31Length() != 16 || aa31Mapping.AA31CharacterDevice() {
		aa31T.Fatalf("aa31 mapping length %d character device %v", aa31Mapping.AA31Length(), aa31Mapping.AA31CharacterDevice())
	}
	for aa31Index, aa31Want := range aa31Words {
		if aa31Got := aa31Mapping.AA31Load32(uint64(4 * aa31Index)); aa31Got != aa31Want {
			aa31T.Fatalf("aa31 load %d = %#x, want %#x", 4*aa31Index, aa31Got, aa31Want)
		}
	}
	for _, aa31Offset := range []uint64{1, 2, 3, 16, 20, ^uint64(0) - 3} {
		if aa31Got := aa31Mapping.AA31Load32(aa31Offset); aa31Got != 0 {
			aa31T.Fatalf("aa31 invalid load %d = %#x", aa31Offset, aa31Got)
		}
	}
	if aa31Mapping.AA31InvalidLoads() != 6 {
		aa31T.Fatalf("aa31 invalid loads = %d", aa31Mapping.AA31InvalidLoads())
	}
	if aa31Err := aa31Mapping.AA31Unmap(); aa31Err != nil {
		aa31T.Fatalf("aa31 unmap: %v", aa31Err)
	}
	if aa31Err := aa31Mapping.AA31Unmap(); aa31Err != nil || aa31Mapping.AA31Length() != 0 {
		aa31T.Fatalf("aa31 second unmap = %v length %d", aa31Err, aa31Mapping.AA31Length())
	}
	if aa31Got := aa31Mapping.AA31Load32(0); aa31Got != 0 || aa31Mapping.AA31InvalidLoads() != 7 {
		aa31T.Fatalf("aa31 load after unmap = %#x invalid %d", aa31Got, aa31Mapping.AA31InvalidLoads())
	}
}

// Catches: mapping with no owner-named path, a length outside the window
// bounds, an unaligned file offset, or a window past the end of a regular file
// (whose loads would fault).
func TestAA31MappingRefusesInvalidRequests(aa31T *aa31testing.T) {
	aa31Path := aa31WriteWindowFile(aa31T, make([]uint32, 8))
	aa31Page := uint64(aa31os.Getpagesize())
	aa31Cases := map[string]struct {
		aa31Path   string
		aa31Offset uint64
		aa31Length uint64
	}{
		"no_path":         {"", 0, 16},
		"missing_file":    {aa31filepath.Join(aa31T.TempDir(), "aa31-absent"), 0, 16},
		"zero_length":     {aa31Path, 0, 0},
		"short_length":    {aa31Path, 0, 2},
		"unaligned_width": {aa31Path, 0, 6},
		"over_length":     {aa31Path, 0, AA31MaxMapLength + 4},
		"unaligned_page":  {aa31Path, 4, 16},
		"past_file_end":   {aa31Path, 0, 36},
		"offset_past_end": {aa31Path, aa31Page, 16},
		"huge_offset":     {aa31Path, 1<<62 + aa31Page, 16},
	}
	for aa31Name, aa31Case := range aa31Cases {
		if aa31Mapping, aa31Err := AA31MapReadOnly(aa31Case.aa31Path, aa31Case.aa31Offset, aa31Case.aa31Length); !aa31errors.Is(aa31Err, AA31ErrMapping) || aa31Mapping != nil {
			aa31T.Fatalf("aa31 %s map = %v, %v", aa31Name, aa31Mapping, aa31Err)
		}
	}
	aa31Mapping, aa31Err := AA31MapReadOnly(aa31Path, 0, 32)
	if aa31Err != nil {
		aa31T.Fatalf("aa31 whole-file map: %v", aa31Err)
	}
	if aa31Err := aa31Mapping.AA31Unmap(); aa31Err != nil {
		aa31T.Fatal(aa31Err)
	}
}
