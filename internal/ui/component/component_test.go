package component

import (
	"strings"
	"testing"

	logutil "github.com/eoctet/tendkit/pkg/logger"
	"time"
)

func TestMessageBubbleLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	var bubble MessageBubble
	bubble.Set("saved", false, now, time.Second, 2*time.Second)
	if bubble.Message != "saved" || bubble.Error || bubble.Until != now.Add(time.Second) || bubble.Expire(now) {
		t.Fatalf("normal bubble = %#v", bubble)
	}
	bubble.Set("failed", true, now, time.Second, 2*time.Second)
	if !bubble.Error || bubble.Until != now.Add(2*time.Second) || !bubble.Expire(now.Add(3*time.Second)) || bubble.Message != "" {
		t.Fatalf("error bubble did not expire: %#v", bubble)
	}
	bubble.Set("", false, now, time.Second, 2*time.Second)
	if !bubble.Until.IsZero() {
		t.Fatalf("empty bubble retained deadline: %#v", bubble)
	}
}

func TestLogOutputNormalizesMultilineRecords(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	lines := logutil.FormatOperationLines(at, "unknown", " update] ", "app\nname", "first\r\nsecond")
	if len(lines) != 2 || !strings.Contains(lines[0], "[INFO ") || strings.Contains(lines[0], "update]") {
		t.Fatalf("formatted lines = %#v", lines)
	}
	if LevelFromLine(lines[0]) != LogInfo || LevelFromLine(logutil.FormatOperationLines(at, "ERROR", "x", "y", "z")[0]) != LogError || LevelFromLine("plain") != LogInfo {
		t.Fatal("log severity parsing failed")
	}
	if got := logutil.FormatOperationLines(time.Time{}, "WARN", "", "", ""); len(got) != 1 || !strings.Contains(got[0], "SYSTEM") {
		t.Fatalf("default log context = %#v", got)
	}
}

func TestNormalizeApplicationValue(t *testing.T) {
	if got, ok := NormalizeApplicationValue("  Obsidian  ", true, true, 32); !ok || got != "Obsidian" {
		t.Fatalf("normalized value = %q, %v", got, ok)
	}
	if _, ok := NormalizeApplicationValue("   ", true, true, 32); ok {
		t.Fatal("required blank value was accepted")
	}
	if _, ok := NormalizeApplicationValue("toolong", false, false, 3); ok {
		t.Fatal("oversized value was accepted")
	}
}
