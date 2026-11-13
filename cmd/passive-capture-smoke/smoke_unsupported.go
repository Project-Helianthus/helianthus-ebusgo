//go:build !(linux && arm64 && local_gpio)

// Command passive-capture-smoke is a passive capture over the local GPIO backend. It
// exists only in linux/arm64 builds with the local_gpio tag; every other
// build refuses to run.
package main

import "os"

func main() {
	_, _ = os.Stderr.WriteString("passive-capture-smoke: built without linux/arm64 and the local_gpio tag; refusing to run\n")
	os.Exit(2)
}
