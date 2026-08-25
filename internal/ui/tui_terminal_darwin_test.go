//go:build darwin

package ui

import (
	"syscall"
	"testing"
)

func TestMakeRawBlocksUntilInput(t *testing.T) {
	termios := syscall.Termios{}
	termios.Cc[syscall.VMIN] = 0
	termios.Cc[syscall.VTIME] = 1

	makeRaw(&termios)

	if termios.Cc[syscall.VMIN] != 1 || termios.Cc[syscall.VTIME] != 0 {
		t.Fatalf("raw input timing = VMIN %d, VTIME %d; want 1, 0", termios.Cc[syscall.VMIN], termios.Cc[syscall.VTIME])
	}
}
