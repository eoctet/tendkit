//go:build linux

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
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&state)))
	if errno != 0 {
		t.Fatalf("TCGETS: %v", errno)
	}
	return state
}

func TestLinuxFDRangeRejectsUpperBound(t *testing.T) {
	upperBound := len((syscall.FdSet{}).Bits) * 64
	if linuxFDInRange(upperBound) {
		t.Fatalf("fd %d was accepted", upperBound)
	}
	if !linuxFDInRange(upperBound - 1) {
		t.Fatalf("fd %d was rejected", upperBound-1)
	}
}
