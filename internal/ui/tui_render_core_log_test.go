package ui

import (
	"strings"
	"testing"
	"time"
)

func TestFormatLogLinesIncludesStandardContextOnEveryLine(t *testing.T) {
	at := time.Date(2026, 8, 14, 10, 20, 30, 456000000, time.Local)
	lines := FormatLogLines(at, LogError, "update", "pypdf", "first line\nsecond line")
	if len(lines) != 2 {
		t.Fatalf("lines = %#v", lines)
	}
	for _, line := range lines {
		for _, expected := range []string{"2026-08-14 10:20:30.456", "[ERROR]", "[UPDATE  ]", "[pypdf]"} {
			if !strings.Contains(line, expected) {
				t.Fatalf("formatted line missing %q: %q", expected, line)
			}
		}
	}
	if !strings.HasSuffix(lines[1], "second line") {
		t.Fatalf("continuation message = %q", lines[1])
	}
}

func TestLogLevelFromLine(t *testing.T) {
	for _, expected := range []LogLevel{LogInfo, LogDebug, LogWarn, LogError} {
		line := FormatLogLines(time.Now(), expected, "check", "app", "message")[0]
		if actual := LogLevelFromLine(line); actual != expected {
			t.Fatalf("parsed level = %q, want %q from %q", actual, expected, line)
		}
	}
}
