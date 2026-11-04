//go:build linux || darwin

package aa31capture

import (
	aa31errors "errors"
	aa31fmt "fmt"
	aa31os "os"
	aa31atomic "sync/atomic"
	aa31syscall "syscall"
	aa31unsafe "unsafe"
)

const (
	// AA31AccessWidth is the only load width the mapping serves.
	AA31AccessWidth uint64 = 4
	// AA31MaxMapLength matches the AA29 window bound; it is not a platform value.
	AA31MaxMapLength uint64 = 1 << 16
)

// AA31ErrMapping reports a refused or failed read-only mapping.
var AA31ErrMapping = aa31errors.New("aa29-smoke: read-only mapping refused") //nolint:staticcheck // ST1012: AA31 prefix is mandatory

// AA31Mapping is a read-only shared mapping of an owner-named file. The pages
// are mapped without write permission, so no store can reach the resource
// through it; a store fault is the kernel's enforcement, not this type's.
type AA31Mapping struct {
	aa31Memory          []byte
	aa31CharacterDevice bool
	aa31InvalidLoads    uint64
}

// AA31MapReadOnly opens aa31Path read-only and synchronous, maps aa31Length
// bytes at the page-aligned file offset aa31Offset, and closes the descriptor.
// The path and offset come only from the caller; there is no default.
func AA31MapReadOnly(aa31Path string, aa31Offset, aa31Length uint64) (*AA31Mapping, error) {
	if aa31Path == "" {
		return nil, aa31fmt.Errorf("%w: no path", AA31ErrMapping)
	}
	if aa31Length < AA31AccessWidth || aa31Length > AA31MaxMapLength || aa31Length%AA31AccessWidth != 0 {
		return nil, aa31fmt.Errorf("%w: length %d", AA31ErrMapping, aa31Length)
	}
	aa31Page := uint64(aa31os.Getpagesize())
	if aa31Offset%aa31Page != 0 || aa31Offset > uint64(1<<62) {
		return nil, aa31fmt.Errorf("%w: offset is not page aligned", AA31ErrMapping)
	}
	aa31File, aa31Err := aa31os.OpenFile(aa31Path, aa31os.O_RDONLY|aa31syscall.O_SYNC, 0)
	if aa31Err != nil {
		return nil, aa31fmt.Errorf("%w: %w", AA31ErrMapping, aa31Err)
	}
	// The mapping outlives the descriptor; a close error cannot unmap it.
	defer func() { _ = aa31File.Close() }()
	aa31Info, aa31Err := aa31File.Stat()
	if aa31Err != nil {
		return nil, aa31fmt.Errorf("%w: %w", AA31ErrMapping, aa31Err)
	}
	// A regular file shorter than the window would fault on a load past its end.
	if aa31Info.Mode().IsRegular() && uint64(aa31Info.Size()) < aa31Offset+aa31Length {
		return nil, aa31fmt.Errorf("%w: file shorter than the window", AA31ErrMapping)
	}
	aa31Memory, aa31Err := aa31syscall.Mmap(int(aa31File.Fd()), int64(aa31Offset), int(aa31Length), aa31syscall.PROT_READ, aa31syscall.MAP_SHARED)
	if aa31Err != nil {
		return nil, aa31fmt.Errorf("%w: %w", AA31ErrMapping, aa31Err)
	}
	return &AA31Mapping{
		aa31Memory:          aa31Memory,
		aa31CharacterDevice: aa31Info.Mode()&aa31os.ModeCharDevice != 0,
	}, nil
}

// AA31Length returns the mapped length, or zero once unmapped.
func (aa31Mapping *AA31Mapping) AA31Length() uint64 {
	return uint64(len(aa31Mapping.aa31Memory))
}

// AA31CharacterDevice reports whether the mapped file is a character device.
// A regular file is page-cache memory and is never device-ordered; whether a
// character device mapping is device-ordered is defined by its driver and is
// a Hypothesis here.
func (aa31Mapping *AA31Mapping) AA31CharacterDevice() bool {
	return aa31Mapping.aa31CharacterDevice
}

// AA31Load32 performs one aligned 32-bit load in native byte order. An
// unaligned, out-of-range or post-unmap offset loads nothing, returns zero and
// is counted.
func (aa31Mapping *AA31Mapping) AA31Load32(aa31Offset uint64) uint32 {
	aa31Length := uint64(len(aa31Mapping.aa31Memory))
	if aa31Length < AA31AccessWidth || aa31Offset%AA31AccessWidth != 0 || aa31Offset > aa31Length-AA31AccessWidth {
		aa31Mapping.aa31InvalidLoads++
		return 0
	}
	return aa31atomic.LoadUint32((*uint32)(aa31unsafe.Pointer(&aa31Mapping.aa31Memory[aa31Offset])))
}

// AA31InvalidLoads returns the number of refused loads.
func (aa31Mapping *AA31Mapping) AA31InvalidLoads() uint64 {
	return aa31Mapping.aa31InvalidLoads
}

// AA31Unmap releases the mapping once. A failed unmap keeps it owned so a
// later call retries; calls after a successful unmap return nil.
func (aa31Mapping *AA31Mapping) AA31Unmap() error {
	if aa31Mapping.aa31Memory == nil {
		return nil
	}
	if aa31Err := aa31syscall.Munmap(aa31Mapping.aa31Memory); aa31Err != nil {
		return aa31fmt.Errorf("%w: %w", AA31ErrMapping, aa31Err)
	}
	aa31Mapping.aa31Memory = nil
	return nil
}
