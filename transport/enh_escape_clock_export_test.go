//go:build !tinygo

package transport

import "time"

// SetEscapeClockForTest must be called before any reads begin.
func SetEscapeClockForTest(t *ENHTransport, now func() time.Time) {
	t.escapeNow = now
}
