package handler

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestExpandConfiguredPath(t *testing.T) {
	t.Setenv("PACKAGE_BIN", "bin/tool")
	home := filepath.Join(string(filepath.Separator), "Users", "tester")
	if got := expandConfiguredPath("  ~/$PACKAGE_BIN  ", func() (string, error) { return home, nil }); got != filepath.Join(home, "bin/tool") {
		t.Fatalf("expanded path = %q", got)
	}
	if got := expandConfiguredPath("~/bin/tool", func() (string, error) { return "", errors.New("missing home") }); got != "~/bin/tool" {
		t.Fatalf("path changed after home lookup failure: %q", got)
	}
}
