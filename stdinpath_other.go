//go:build !linux && !darwin

package main

import "os"

// stdinPathRaw has no portable implementation. Where the path cannot be
// recovered, a redirect is treated like any other stream: the data is sampled
// and the command reads stdin, exactly as it did before.
func stdinPathRaw(f *os.File) (string, bool) { return "", false }
