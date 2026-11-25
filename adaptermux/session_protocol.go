package adaptermux

// Session extension nibbles. ENH already uses 0x0–0x3 and 0xA–0xC.
// Each frame is the encoded two-byte form only: 0xC0|cmd<<2|data>>6,
// 0x80|data&0x3F. The short form is never used for these commands.
const (
	extNibbleTarget      = 0x4 // session TARGET, mux TARGETED
	extNibbleUntarget    = 0x5 // session UNTARGET, mux UNTARGETED
	extNibbleBegin       = 0x6 // session BEGIN, mux SLOT
	extNibbleStage       = 0x7 // session STAGE, mux SLOT_END
	extNibbleCommit      = 0x8
	extNibbleTargetError = 0xD
)

// TARGET_ERROR reason codes.
const (
	reasonDuplicate         = 0x01
	reasonInadmissible      = 0x02
	reasonCapacity          = 0x03
	reasonConflict          = 0x04
	reasonUnavailable       = 0x05
	reasonSubscriberFailure = 0x06
	reasonReset             = 0x07
	reasonStale             = 0x08
	reasonLate              = 0x09
	reasonInvalidResponse   = 0x0A
	reasonTagExhausted      = 0x0B
)

// SLOT_END codes.
const (
	endCompleted        = 0x00
	endCRC              = 0x01
	endMalformed        = 0x02
	endNotReady         = 0x03
	endDeclined         = 0x04
	endLate             = 0x05
	endCompeting        = 0x06
	endBoundary         = 0x07
	endRevoked          = 0x08
	endStreamEnded      = 0x09
	endResponseRejected = 0x0A
)

// extFrame is one decoded session-extension command.
type extFrame struct {
	cmd  byte
	data byte
}

// peelSessionExtension splits a client read into extension commands and the
// bytes that still belong to the ENH parser. A 0xC0-form first byte whose
// command nibble is 0x4–0x8 is held until its second byte; every other byte
// is left for the parser, so unknown commands stay on the existing path.
func peelSessionExtension(buf []byte, hold byte, holding bool) (frames []extFrame, rest []byte, newHold byte, stillHolding bool, bad bool) {
	if holding {
		if len(buf) == 0 {
			return nil, nil, hold, true, false
		}
		fr, ok := finishExt(hold, buf[0])
		if !ok {
			return nil, nil, 0, false, true
		}
		frames = append(frames, fr)
		buf = buf[1:]
	}
	for len(buf) > 0 {
		b := buf[0]
		if !isExtFirst(b) {
			rest = append(rest, b)
			buf = buf[1:]
			continue
		}
		if len(buf) == 1 {
			return frames, rest, b, true, false
		}
		fr, ok := finishExt(b, buf[1])
		if !ok {
			return nil, nil, 0, false, true
		}
		frames = append(frames, fr)
		buf = buf[2:]
	}
	return frames, rest, 0, false, false
}

func isExtFirst(b byte) bool {
	if b&0xC0 != 0xC0 {
		return false
	}
	cmd := (b >> 2) & 0x0F
	return cmd >= extNibbleTarget && cmd <= extNibbleCommit
}

func finishExt(b1, b2 byte) (extFrame, bool) {
	if b2&0xC0 != 0x80 {
		return extFrame{}, false
	}
	cmd := (b1 >> 2) & 0x0F
	data := ((b1 & 0x03) << 6) | (b2 & 0x3F)
	return extFrame{cmd: cmd, data: data}, true
}
