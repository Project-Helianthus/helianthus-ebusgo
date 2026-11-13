//go:build !(linux && arm64 && aa29_local_gpio)

// Command passive-capture-smoke is a passive capture over the local GPIO backend. It
// exists only in linux/arm64 builds with the aa29_local_gpio tag; every other
// build refuses to run.
package main

import "os"

func main() {
	_, _ = os.Stderr.WriteString("aa29-smoke: built without linux/arm64 and the aa29_local_gpio tag; refusing to run\n")
	os.Exit(2)
}
