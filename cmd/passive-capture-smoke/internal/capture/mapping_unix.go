//go:build linux || darwin

package capture

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	// AccessWidth is the only load width the mapping serves.
	AccessWidth uint64 = 4
	// MaxMapLength matches the local GPIO window bound; it is not a platform value.
	MaxMapLength uint64 = 1 << 16
)

// ErrMapping reports a refused or failed read-only mapping.
var ErrMapping = errors.New("passive-capture-smoke: read-only mapping refused")

// Mapping is a read-only shared mapping of an owner-named file. The pages
// are mapped without write permission, so no store can reach the resource
// through it; a store fault is the kernel's enforcement, not this type's.
type Mapping struct {
	memory          []byte
	characterDevice bool
	invalidLoads    uint64
}

// MapReadOnly opens the named file read-only and synchronous, maps length
// bytes at the page-aligned file offset, and closes the descriptor.
// The path and offset come only from the caller; there is no default.
func MapReadOnly(path string, offset, length uint64) (*Mapping, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: no path", ErrMapping)
	}
	if length < AccessWidth || length > MaxMapLength || length%AccessWidth != 0 {
		return nil, fmt.Errorf("%w: length %d", ErrMapping, length)
	}
	page := uint64(os.Getpagesize())
	if offset%page != 0 || offset > uint64(1<<62) {
		return nil, fmt.Errorf("%w: offset is not page aligned", ErrMapping)
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_SYNC, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMapping, err)
	}
	// The mapping outlives the descriptor; a close error cannot unmap it.
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMapping, err)
	}
	// A regular file shorter than the window would fault on a load past its end.
	if info.Mode().IsRegular() && uint64(info.Size()) < offset+length {
		return nil, fmt.Errorf("%w: file shorter than the window", ErrMapping)
	}
	memory, err := syscall.Mmap(int(file.Fd()), int64(offset), int(length), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMapping, err)
	}
	return &Mapping{
		memory:          memory,
		characterDevice: info.Mode()&os.ModeCharDevice != 0,
	}, nil
}

// Length returns the mapped length, or zero once unmapped.
func (mapping *Mapping) Length() uint64 {
	return uint64(len(mapping.memory))
}

// CharacterDevice reports whether the mapped file is a character device.
// A regular file is page-cache memory and is never device-ordered; whether a
// character device mapping is device-ordered is defined by its driver and is
// a Hypothesis here.
func (mapping *Mapping) CharacterDevice() bool {
	return mapping.characterDevice
}

// Load32 performs one aligned 32-bit load in native byte order. An
// unaligned, out-of-range or post-unmap offset loads nothing, returns zero and
// is counted.
func (mapping *Mapping) Load32(offset uint64) uint32 {
	length := uint64(len(mapping.memory))
	if length < AccessWidth || offset%AccessWidth != 0 || offset > length-AccessWidth {
		mapping.invalidLoads++
		return 0
	}
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(&mapping.memory[offset])))
}

// InvalidLoads returns the number of refused loads.
func (mapping *Mapping) InvalidLoads() uint64 {
	return mapping.invalidLoads
}

// Unmap releases the mapping once. A failed unmap keeps it owned so a
// later call retries; calls after a successful unmap return nil.
func (mapping *Mapping) Unmap() error {
	if mapping.memory == nil {
		return nil
	}
	if err := syscall.Munmap(mapping.memory); err != nil {
		return fmt.Errorf("%w: %w", ErrMapping, err)
	}
	mapping.memory = nil
	return nil
}
