package runtime

import "testing"

func TestQuoteShellLeavesSafeTokensBareAndQuotesWhitespace(t *testing.T) {
	for _, test := range []struct{ value, want string }{
		{"/usr/bin/gem", "/usr/bin/gem"}, {"atomos", "atomos"}, {"arg-value", "arg-value"},
		{"/path with space/gem", "'/path with space/gem'"}, {"arg value", "'arg value'"}, {"a;rm", "'a;rm'"},
	} {
		if got := QuoteShell(test.value); got != test.want {
			t.Fatalf("QuoteShell(%q)=%q, want %q", test.value, got, test.want)
		}
	}
}

func TestRenderQuotesShellValues(t *testing.T) {
	got, err := Render("application --name {name}", map[string]string{"name": "O'Brien; echo unsafe"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "application --name 'O'\"'\"'Brien; echo unsafe'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderRejectsUnknownPlaceholder(t *testing.T) {
	if _, err := Render("{secret}", map[string]string{}, true); err == nil {
		t.Fatal("expected error")
	}
}

func TestRenderPreservesAwkBracesButRejectsLegalUnknownPlaceholder(t *testing.T) {
	got, err := Render(`awk '$1 == "mod" {print $3; found=1}'`, nil, false)
	if err != nil || got != `awk '$1 == "mod" {print $3; found=1}'` {
		t.Fatalf("awk render=%q err=%v", got, err)
	}
	if _, err := Render(`echo {missing_key}`, nil, false); err == nil {
		t.Fatal("legal unknown placeholder was accepted")
	}
}

func TestRenderPreservesDoubleBraceTemplateBlocks(t *testing.T) {
	got, err := Render("printf '{{.Version}} {name}'", map[string]string{"name": "1.2.3"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "printf '{{.Version}} 1.2.3'"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderRejectsUnclosedSinglePlaceholder(t *testing.T) {
	if _, err := Render("printf {name", map[string]string{"name": "sample"}, true); err == nil {
		t.Fatal("expected error")
	}
}
