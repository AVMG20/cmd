package main

import (
	"os"
	"syscall"
	"unsafe"
)

// fGetPath is darwin's fcntl command for "tell me the path of this
// descriptor". It is not exported by the syscall package, so it is named here.
const fGetPath = 50

// maxPathLen is darwin's MAXPATHLEN; F_GETPATH requires a buffer of at least
// this size and will not write more.
const maxPathLen = 1024

// stdinPathRaw asks the kernel which file a descriptor refers to.
//
// macOS has no /proc, but fcntl(F_GETPATH) answers the same question. It fails
// for a pipe, which is exactly the case that must not resolve to a name.
func stdinPathRaw(f *os.File) (string, bool) {
	buf := make([]byte, maxPathLen)
	_, _, errno := syscall.Syscall(
		syscall.SYS_FCNTL,
		f.Fd(),
		uintptr(fGetPath),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if errno != 0 {
		return "", false
	}
	// The kernel writes a NUL-terminated string into the buffer.
	end := 0
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	if end == 0 {
		return "", false
	}
	return string(buf[:end]), true
}
