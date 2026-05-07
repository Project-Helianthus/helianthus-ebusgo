package protocol

import (
	"errors"
	"fmt"
)

// ErrInvalidFrameAddress is the sentinel error returned by
// ValidateFrameAddressing when a frame's (FrameType, src, dst) tuple
// does not satisfy the Phase C frame-type addressing contract documented
// in helianthus-docs-ebus/architecture/ebus_standard/12-address-table.md.
//
// Callers can match via errors.Is(err, protocol.ErrInvalidFrameAddress).
// The wrapper error always carries a descriptive message naming the
// specific clause that rejected the frame.
//
// Decision references: AD26 (validator contract).
var ErrInvalidFrameAddress = errors.New("eBUS: invalid frame address")

// ValidateFrameAddressing enforces the 6-clause Phase C contract:
//
//  1. ft != FrameTypeUnknown.
//  2. src != dst (no self-addressing — gateway-side discipline).
//  3. AddressClassOf(src) and AddressClassOf(dst) must not be Reserved.
//  4. ft == M2S → AddressClassOf(src) == Master AND
//     AddressClassOf(dst) == Slave.
//  5. ft == M2M → AddressClassOf(src) == Master AND
//     AddressClassOf(dst) == Master.
//  6. ft == M2BC → AddressClassOf(src) == Master AND dst == 0xFE
//     (AddressClassOf(dst) == Broadcast).
//
// The function returns nil on conformance; otherwise an error wrapping
// ErrInvalidFrameAddress.
//
// Doc reference: 12-address-table.md "Validator Contract" section
// (taxonomy + frame-type contract block, hash
// 316baf20ab0d0a64b36613bb8c7604d7570fecc01071daca94931029ae82ebec).
func ValidateFrameAddressing(ft FrameType, src, dst byte) error {
	// Clause 1: reject zero/unknown frame type.
	if ft == FrameTypeUnknown {
		return fmt.Errorf("%w: frame_type=unknown (caller must declare M2S/M2M/M2BC explicitly)", ErrInvalidFrameAddress)
	}
	// Clause 2: no self-addressing.
	if src == dst {
		return fmt.Errorf("%w: src == dst == 0x%02X (self-addressing forbidden)", ErrInvalidFrameAddress, src)
	}

	srcClass := AddressClassOf(src)
	dstClass := AddressClassOf(dst)

	// Clause 3: reserved bytes (SYN, ESCAPE) cannot appear as src or dst.
	if srcClass == AddressClassReserved {
		return fmt.Errorf("%w: src=0x%02X is reserved (SYN/ESCAPE)", ErrInvalidFrameAddress, src)
	}
	if dstClass == AddressClassReserved {
		return fmt.Errorf("%w: dst=0x%02X is reserved (SYN/ESCAPE)", ErrInvalidFrameAddress, dst)
	}

	// Clauses 4-6: per-frame-type src/dst class match.
	switch ft {
	case FrameTypeInitiatorTarget:
		if srcClass != AddressClassMaster {
			return fmt.Errorf("%w: M2S requires src=Master, got src=0x%02X (%s)", ErrInvalidFrameAddress, src, srcClass)
		}
		if dstClass != AddressClassSlave {
			return fmt.Errorf("%w: M2S requires dst=Slave, got dst=0x%02X (%s)", ErrInvalidFrameAddress, dst, dstClass)
		}
	case FrameTypeInitiatorInitiator:
		if srcClass != AddressClassMaster {
			return fmt.Errorf("%w: M2M requires src=Master, got src=0x%02X (%s)", ErrInvalidFrameAddress, src, srcClass)
		}
		if dstClass != AddressClassMaster {
			return fmt.Errorf("%w: M2M requires dst=Master, got dst=0x%02X (%s)", ErrInvalidFrameAddress, dst, dstClass)
		}
	case FrameTypeBroadcast:
		if srcClass != AddressClassMaster {
			return fmt.Errorf("%w: M2BC requires src=Master, got src=0x%02X (%s)", ErrInvalidFrameAddress, src, srcClass)
		}
		if dstClass != AddressClassBroadcast {
			return fmt.Errorf("%w: M2BC requires dst=0xFE, got dst=0x%02X (%s)", ErrInvalidFrameAddress, dst, dstClass)
		}
	default:
		return fmt.Errorf("%w: unsupported FrameType %d", ErrInvalidFrameAddress, ft)
	}

	return nil
}
