package ui

import (
	"strings"
	"testing"
)

func TestTUIKeymapPermissionsAndFooterWrapping(t *testing.T) {
	keymap := newTUIKeymap(
		tuiKey("tui.key.select", "up", "down"),
		tuiDynamicKey([]string{"enter"}, func() string { return "ENTER apply" }),
		tuiTextKeyMatching("tui.key.type", func(value string) bool { return value != "!" }),
	)
	if !keymap.Permits("up") || !keymap.Permits("中") || keymap.Permits("!") || keymap.Permits("esc") {
		t.Fatal("keymap permissions do not match bindings")
	}
	lines := keymap.FooterLines(20)
	if len(lines) < 2 || !strings.Contains(strings.Join(lines, " "), "ENTER apply") {
		t.Fatalf("footer lines = %#v", lines)
	}
	if !newTUIKeymap(tuiTextKey("tui.key.type")).Permits("x") {
		t.Fatal("unfiltered text binding rejected text")
	}
}
