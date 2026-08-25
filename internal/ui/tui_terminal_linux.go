//go:build linux

package ui

import (
	"errors"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/eoctet/tendkit/pkg/i18n"
)

type terminalState struct {
	file     *os.File
	original syscall.Termios
}

func IsTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
func enterRawMode(file *os.File) (*terminalState, error) {
	if !IsTerminal(file) {
		return nil, errors.New(i18n.T("tui.terminal_required"))
	}
	var original syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&original))); errno != 0 {
		return nil, errno
	}
	raw := original
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&raw))); errno != 0 {
		return nil, errno
	}
	return &terminalState{file: file, original: original}, nil
}
func (state *terminalState) restore() {
	if state != nil && state.file != nil {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, state.file.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&state.original)))
	}
}
func waitTUIInput(file *os.File, timeout time.Duration) (bool, error) {
	if file == nil {
		return false, errors.New(i18n.T("tui.terminal_required"))
	}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		fd := int(file.Fd())
		if !linuxFDInRange(fd) {
			return false, errors.New(i18n.T("tui.terminal_required"))
		}
		var set syscall.FdSet
		set.Bits[fd/64] |= 1 << uint(fd%64)
		value := syscall.NsecToTimeval(remaining.Nanoseconds())
		_, err := syscall.Select(fd+1, &set, nil, nil, &value)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		return set.Bits[fd/64]&(1<<uint(fd%64)) != 0, nil
	}
}

func linuxFDInRange(fd int) bool {
	return fd >= 0 && fd < len((syscall.FdSet{}).Bits)*64
}

type terminalWindowSize struct{ Rows, Columns, XPixel, YPixel uint16 }

func terminalSize(file *os.File) (int, int) {
	if file != nil {
		var size terminalWindowSize
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size))); errno == 0 && size.Columns > 0 && size.Rows > 0 {
			return int(size.Columns), int(size.Rows)
		}
	}
	return 120, 36
}
