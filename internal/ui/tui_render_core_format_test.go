package ui

import (
	"bytes"
	"testing"

	"github.com/eoctet/tendkit/pkg/i18n"
)

func useLanguage(t *testing.T, language i18n.Language) {
	t.Helper()
	previous := i18n.Current()
	i18n.Set(language)
	t.Cleanup(func() { i18n.Set(previous) })
}

func TestStatusLabelUsesUpdateOrientedLabel(t *testing.T) {
	useLanguage(t, i18n.English)
	if label := StatusLabel("unchecked"); label != "UNCHECKED" {
		t.Fatalf("unexpected unchecked label: %q", label)
	}
	if label := StatusLabel("updating"); label != "UPDATING" {
		t.Fatalf("unexpected updating label: %q", label)
	}
}

func TestColorEnabledHonorsExplicitModes(t *testing.T) {
	var output bytes.Buffer
	if !colorEnabled(&output, ModeAlways) {
		t.Fatal("always mode did not enable color")
	}
	if colorEnabled(&output, ModeNever) {
		t.Fatal("never mode enabled color")
	}
}

func TestColumnAccountsForWideCharacters(t *testing.T) {
	if got := Column("应用", 6); got != "应用  " || DisplayWidth(got) != 6 {
		t.Fatalf("unexpected wide column %q width=%d", got, DisplayWidth(got))
	}
}
