// Package crc computes the eBUS 8-bit CRC for polynomial 0x9B through a
// lookup table. The checksum starts at 0x00 and each byte is folded in by
// Update; no final transformation is applied.
//
// The package only folds bytes into the checksum; it does not know the
// telegram layout or the escape rules. Callers decide which bytes to feed
// and in which form: the eBUS CRC covers the symbols as they appear on the
// wire, so a caller that holds unescaped data must feed the escape sequence
// for each escaped byte, not the byte itself.
//
// Update is the incremental form and Sum the one-shot form over a slice
// starting from zero. Both are pure functions and safe for concurrent use.
package crc
