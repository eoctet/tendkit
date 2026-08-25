package ui

import (
	"bytes"
	"testing"
	"time"
)

func TestWaitForTUIScanHasFiniteGracePeriod(t *testing.T) {
	view := tuiModel{scanPageState: scanPageState{scanRunning: true}, viewportState: viewportState{width: 80, height: 24}}
	started := time.Now()
	if waitForTUIScan(&view, TUIActions{}, make(chan tuiEvent), &bytes.Buffer{}, 20*time.Millisecond) {
		t.Fatal("stuck scanner was reported as stopped")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("scanner shutdown wait was not bounded: %s", elapsed)
	}
}
