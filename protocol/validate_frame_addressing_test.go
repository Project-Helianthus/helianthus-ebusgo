package protocol

import (
	"errors"
	"testing"
)

// Phase C M-C2 + M-C3 + M-C4: ValidateFrameAddressing + Frame.Validate
// + Frame.FrameType field + Frame.EffectiveFrameType().

func TestValidateFrameAddressing_M2S_OK(t *testing.T) {
	t.Parallel()
	// 0x10 master → 0x15 slave canonical pair.
	if err := ValidateFrameAddressing(FrameTypeInitiatorTarget, 0x10, 0x15); err != nil {
		t.Fatalf("ValidateFrameAddressing(M2S, 0x10, 0x15) = %v; want nil", err)
	}
}

func TestValidateFrameAddressing_M2M_OK(t *testing.T) {
	t.Parallel()
	// 0x71 master → 0x10 master.
	if err := ValidateFrameAddressing(FrameTypeInitiatorInitiator, 0x71, 0x10); err != nil {
		t.Fatalf("ValidateFrameAddressing(M2M, 0x71, 0x10) = %v; want nil", err)
	}
}

func TestValidateFrameAddressing_M2BC_OK(t *testing.T) {
	t.Parallel()
	if err := ValidateFrameAddressing(FrameTypeBroadcast, 0x71, 0xFE); err != nil {
		t.Fatalf("ValidateFrameAddressing(M2BC, 0x71, 0xFE) = %v; want nil", err)
	}
}

func TestValidateFrameAddressing_RejectsM2SWithSlaveSrc(t *testing.T) {
	t.Parallel()
	err := ValidateFrameAddressing(FrameTypeInitiatorTarget, 0x15, 0x08)
	if !errors.Is(err, ErrInvalidFrameAddress) {
		t.Fatalf("ValidateFrameAddressing(M2S, 0x15-slave, 0x08) = %v; want ErrInvalidFrameAddress", err)
	}
}

func TestValidateFrameAddressing_RejectsM2SWithMasterDst(t *testing.T) {
	t.Parallel()
	err := ValidateFrameAddressing(FrameTypeInitiatorTarget, 0x71, 0x10)
	if !errors.Is(err, ErrInvalidFrameAddress) {
		t.Fatalf("ValidateFrameAddressing(M2S, 0x71, 0x10-master) = %v; want ErrInvalidFrameAddress", err)
	}
}

func TestValidateFrameAddressing_RejectsM2BCWithNonFEDst(t *testing.T) {
	t.Parallel()
	err := ValidateFrameAddressing(FrameTypeBroadcast, 0x71, 0x15)
	if !errors.Is(err, ErrInvalidFrameAddress) {
		t.Fatalf("ValidateFrameAddressing(M2BC, 0x71, 0x15) = %v; want ErrInvalidFrameAddress", err)
	}
}

func TestValidateFrameAddressing_RejectsUnknownFrameType(t *testing.T) {
	t.Parallel()
	err := ValidateFrameAddressing(FrameTypeUnknown, 0x71, 0x15)
	if !errors.Is(err, ErrInvalidFrameAddress) {
		t.Fatalf("ValidateFrameAddressing(Unknown, ...) = %v; want ErrInvalidFrameAddress", err)
	}
}

func TestValidateFrameAddressing_RejectsSelfAddressing(t *testing.T) {
	t.Parallel()
	err := ValidateFrameAddressing(FrameTypeInitiatorTarget, 0x71, 0x71)
	if !errors.Is(err, ErrInvalidFrameAddress) {
		t.Fatalf("ValidateFrameAddressing(M2S, 0x71, 0x71) = %v; want ErrInvalidFrameAddress (self)", err)
	}
}

func TestValidateFrameAddressing_RejectsReservedDst(t *testing.T) {
	t.Parallel()
	for _, dst := range []byte{0xAA, 0xA9} {
		dst := dst
		t.Run("dst_reserved", func(t *testing.T) {
			err := ValidateFrameAddressing(FrameTypeInitiatorTarget, 0x71, dst)
			if !errors.Is(err, ErrInvalidFrameAddress) {
				t.Fatalf("ValidateFrameAddressing(M2S, 0x71, 0x%02X-reserved) = %v; want ErrInvalidFrameAddress", dst, err)
			}
		})
	}
}

func TestFrame_FrameTypeField_ZeroDerivesFromTarget(t *testing.T) {
	t.Parallel()
	// Zero-value FrameType means "derive from Target via FrameTypeForTarget".
	f := Frame{Source: 0x71, Target: 0x15}
	if got := f.EffectiveFrameType(); got != FrameTypeInitiatorTarget {
		t.Fatalf("Frame{...}.EffectiveFrameType() = %v; want FrameTypeInitiatorTarget (derived from Target=0x15)", got)
	}
}

func TestFrame_FrameTypeField_ExplicitOverride(t *testing.T) {
	t.Parallel()
	// Explicit FrameType wins over Target-derived.
	f := Frame{
		Source:    0x71,
		Target:    0x15, // would derive M2S
		FrameType: FrameTypeInitiatorInitiator,
	}
	if got := f.EffectiveFrameType(); got != FrameTypeInitiatorInitiator {
		t.Fatalf("Frame{FrameType:M2M}.EffectiveFrameType() = %v; want M2M", got)
	}
}

func TestFrame_Validate_HappyPath(t *testing.T) {
	t.Parallel()
	f := Frame{Source: 0x71, Target: 0x15} // implicit M2S via Target
	if err := f.Validate(); err != nil {
		t.Fatalf("Frame.Validate() = %v; want nil", err)
	}
}

func TestFrame_Validate_RejectsExplicitMismatch(t *testing.T) {
	t.Parallel()
	// Caller declares M2S but Target is broadcast 0xFE: clause-6
	// FrameTypeForTarget(0xFE) == FrameTypeBroadcast; declared M2S
	// disagrees → reject.
	f := Frame{
		Source:    0x71,
		Target:    0xFE,
		FrameType: FrameTypeInitiatorTarget,
	}
	err := f.Validate()
	if !errors.Is(err, ErrInvalidFrameAddress) {
		t.Fatalf("Frame{M2S, dst=0xFE}.Validate() = %v; want ErrInvalidFrameAddress (clause-6)", err)
	}
}
