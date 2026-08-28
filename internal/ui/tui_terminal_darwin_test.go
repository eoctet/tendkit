//go:build darwin

package ui

import (
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func termiosForTUIPTY(t *testing.T, file *os.File) syscall.Termios {
	t.Helper()
	var state syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&state)), 0, 0, 0)
	if errno != 0 {
		t.Fatalf("TIOCGETA: %v", errno)
	}
	return state
}

func TestMakeRawBlocksUntilInput(t *testing.T) {
	termios := syscall.Termios{}
	termios.Cc[syscall.VMIN] = 0
	termios.Cc[syscall.VTIME] = 1

	makeRaw(&termios)

	if termios.Cc[syscall.VMIN] != 1 || termios.Cc[syscall.VTIME] != 0 {
		t.Fatalf("raw input timing = VMIN %d, VTIME %d; want 1, 0", termios.Cc[syscall.VMIN], termios.Cc[syscall.VTIME])
	}
}
