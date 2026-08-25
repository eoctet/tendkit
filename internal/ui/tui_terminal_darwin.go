//go:build darwin

package ui

import (
	"errors"
	"fmt"
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

func waitTUIInput(file *os.File, timeout time.Duration) (bool, error) {
	if file == nil {
		return false, errors.New(i18n.T("tui.terminal_required"))
	}
	fd := int(file.Fd())
	if fd < 0 || fd/32 >= len((syscall.FdSet{}).Bits) {
		return false, fmt.Errorf("terminal input fd %d exceeds select fd set", fd)
	}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		var readSet syscall.FdSet
		readSet.Bits[fd/32] |= 1 << uint(fd%32)
		value := syscall.NsecToTimeval(remaining.Nanoseconds())
		err := syscall.Select(fd+1, &readSet, nil, nil, &value)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		return readSet.Bits[fd/32]&(1<<uint(fd%32)) != 0, nil
	}
}

type terminalWindowSize struct {
	Rows    uint16
	Columns uint16
	XPixel  uint16
	YPixel  uint16
}

// IsTerminal reports whether file is attached to a terminal device.
func IsTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	var termios syscall.Termios
	// #nosec G103 -- Darwin terminal ioctl requires an unsafe pointer to the syscall structure.
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}

func enterRawMode(file *os.File) (*terminalState, error) {
	if file == nil {
		return nil, errors.New(i18n.T("tui.terminal_required"))
	}
	var original syscall.Termios
	// #nosec G103 -- Darwin terminal ioctl requires an unsafe pointer to the syscall structure.
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&original)), 0, 0, 0); errno != 0 {
		return nil, errors.New(i18n.T("tui.terminal_required"))
	}
	raw := original
	makeRaw(&raw)
	// #nosec G103 -- Darwin terminal ioctl requires an unsafe pointer to the syscall structure.
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCSETA, uintptr(unsafe.Pointer(&raw)), 0, 0, 0); errno != 0 {
		return nil, errno
	}
	return &terminalState{file: file, original: original}, nil
}

func makeRaw(raw *syscall.Termios) {
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	// A TUI input reader must block until at least one byte is available. With
	// VMIN=0/VTIME>0, an idle terminal periodically returns a zero-byte read;
	// os.File reports that as EOF and the event loop exits immediately.
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
}

func (state *terminalState) restore() {
	if state == nil || state.file == nil {
		return
	}
	// #nosec G103 -- Darwin terminal ioctl requires an unsafe pointer to the syscall structure.
	_, _, _ = syscall.Syscall6(syscall.SYS_IOCTL, state.file.Fd(), syscall.TIOCSETA, uintptr(unsafe.Pointer(&state.original)), 0, 0, 0)
}

func terminalSize(file *os.File) (int, int) {
	if file != nil {
		var size terminalWindowSize
		// #nosec G103 -- Darwin terminal ioctl requires an unsafe pointer to the syscall structure.
		if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)), 0, 0, 0); errno == 0 && size.Columns > 0 && size.Rows > 0 {
			return int(size.Columns), int(size.Rows)
		}
	}
	return 120, 36
}
