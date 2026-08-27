package handler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestHomebrewRealHandlers(t *testing.T) {
	if os.Getenv("TENDKIT_REAL_HOMEBREW") != "1" {
		t.Skip("set TENDKIT_REAL_HOMEBREW=1 to run against the local Homebrew installation")
	}
	runner := runtimeutil.Runner{IdleTimeout: 2 * time.Minute}
	formula := NewHomebrewFormula(runner).Scan(context.Background(), Request{})
	if !formula.Complete || formula.Err != nil {
		t.Fatalf("formula scan=%#v", formula)
	}
	foundRipgrep := false
	for _, candidate := range formula.Candidates {
		if candidate.Application.Package == "formula/ripgrep" {
			foundRipgrep = candidate.CurrentVersion != "" && candidate.Evidence != nil && len(candidate.Evidence.ExecutablePaths) > 0
		}
	}
	if !foundRipgrep {
		t.Fatalf("formula candidates do not contain a complete ripgrep candidate: %#v", formula.Candidates)
	}
	cask := NewHomebrewCask(runner).Scan(context.Background(), Request{})
	if !cask.Complete || cask.Err != nil {
		t.Fatalf("cask scan=%#v", cask)
	}
	if token := os.Getenv("TENDKIT_REAL_HOMEBREW_CASK"); token != "" {
		found := false
		for _, candidate := range cask.Candidates {
			if candidate.Application.Package == "cask/"+token && candidate.Application.UpdateMode == model.ModeAuto && candidate.Evidence != nil && len(candidate.Evidence.ApplicationPaths) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatalf("cask candidates do not contain %q: %#v", token, cask.Candidates)
		}
	}
	t.Logf("real Homebrew handlers: formulae=%d casks=%d", len(formula.Candidates), len(cask.Candidates))
}
