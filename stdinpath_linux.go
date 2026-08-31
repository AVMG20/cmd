package main

import (
	"os"
	"strconv"
)

// stdinPathRaw asks the kernel which file a descriptor refers to.
//
// On Linux every open descriptor is a symlink under /proc/self/fd.
func stdinPathRaw(f *os.File) (string, bool) {
	target, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(f.Fd()), 10))
	if err != nil {
		return "", false
	}
	return target, true
}
