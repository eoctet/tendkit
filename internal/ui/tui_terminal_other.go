//go:build !darwin && !linux

package ui

import (
	"errors"
	"os"
	"time"

	"github.com/eoctet/tendkit/pkg/i18n"
)

type terminalState struct{}

// IsTerminal reports false because the TUI is only supported on macOS.
func IsTerminal(*os.File) bool { return false }

func enterRawMode(*os.File) (*terminalState, error) {
	return nil, errors.New(i18n.T("tui.terminal_required"))
}

func (*terminalState) restore() {}

func terminalSize(*os.File) (int, int) { return 120, 36 }

func waitTUIInput(*os.File, time.Duration) (bool, error) {
	return false, errors.New(i18n.T("tui.terminal_required"))
}
