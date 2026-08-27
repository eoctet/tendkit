package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestHomebrewRealScannerKeepsStandaloneRipgrepSeparate(t *testing.T) {
	if os.Getenv("TENDKIT_REAL_HOMEBREW") != "1" {
		t.Skip("set TENDKIT_REAL_HOMEBREW=1 to run against the local Homebrew installation")
	}
	standalone := os.Getenv("TENDKIT_REAL_STANDALONE_RG")
	if standalone == "" {
		t.Skip("set TENDKIT_REAL_STANDALONE_RG to an independently installed rg executable")
	}
	standalone, err := filepath.Abs(standalone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(standalone); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(standalone) != "rg" {
		t.Fatalf("standalone executable must be named rg: %s", standalone)
	}
	originalPath := os.Getenv("PATH")
	standaloneDir := filepath.Dir(standalone)
	t.Setenv("PATH", standaloneDir+string(os.PathListSeparator)+originalPath)
	catalog := model.Config{
		SchemaVersion: model.SchemaVersion,
		Settings: model.Settings{Scan: model.ScanSettings{
			Path: true, Packages: model.PackageScanSettings{HomebrewFormula: true, HomebrewCask: true},
		}},
		ScanVersionControl: map[string]map[string]model.ScanKeepResolution{},
	}
	scanner := Scanner{Runner: runtimeutil.Runner{IdleTimeout: 2 * time.Minute}}
	updated, state, err := scanner.Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	ripgrepApps := make([]model.Application, 0, 2)
	for _, app := range updated.Apps {
		if app.Name == "ripgrep" {
			ripgrepApps = append(ripgrepApps, app)
		}
	}
	if len(ripgrepApps) < 2 {
		t.Fatalf("real scan found %d ripgrep installations: %#v", len(ripgrepApps), ripgrepApps)
	}
	var standaloneApp, homebrewApp model.Application
	identities := map[string]bool{}
	standaloneExpected, err := filepath.EvalSymlinks(standalone)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range ripgrepApps {
		real, realErr := filepath.EvalSymlinks(app.InstallPath)
		if realErr != nil {
			t.Fatal(realErr)
		}
		if real == standaloneExpected {
			standaloneApp = app
		} else if app.Provider.Type == model.ProviderHomebrew {
			homebrewApp = app
		}
		if len(app.ID) <= len("cli-ripgrep-") || app.ID[:len("cli-ripgrep-")] != "cli-ripgrep-" {
			t.Fatalf("multi-instance ripgrep retained non-fingerprinted ID: %#v", app)
		}
		if identities[app.Identity] {
			t.Fatalf("multi-instance ripgrep reused identity %q: %#v", app.Identity, ripgrepApps)
		}
		identities[app.Identity] = true
	}
	if standaloneApp.ID == "" || homebrewApp.ID == "" || standaloneApp.Identity == homebrewApp.Identity {
		t.Fatalf("real ripgrep identity assignment is incomplete: %#v", ripgrepApps)
	}
	standaloneReal, err := filepath.EvalSymlinks(standaloneApp.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	homebrewReal, err := filepath.EvalSymlinks(homebrewApp.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	if standaloneReal == homebrewReal || standaloneApp.Provider.Type != model.ProviderGitHubRelease || homebrewApp.Provider.Type != model.ProviderHomebrew {
		t.Fatalf("real installations were merged: standalone=%#v Homebrew=%#v", standaloneApp, homebrewApp)
	}
	t.Setenv("PATH", originalPath+string(os.PathListSeparator)+standaloneDir)
	reordered, _, err := scanner.Scan(context.Background(), updated, state)
	if err != nil {
		t.Fatal(err)
	}
	for _, before := range ripgrepApps {
		beforeReal, realErr := filepath.EvalSymlinks(before.InstallPath)
		if realErr != nil {
			t.Fatal(realErr)
		}
		matched := false
		for _, after := range reordered.Apps {
			afterReal, afterErr := filepath.EvalSymlinks(after.InstallPath)
			if afterErr == nil && beforeReal == afterReal {
				matched = true
				if before.ID != after.ID || before.Identity != after.Identity {
					t.Fatalf("PATH reorder changed identity: before=%#v after=%#v", before, after)
				}
			}
		}
		if !matched {
			t.Fatalf("PATH reorder lost %#v", before)
		}
	}
	if token, application := os.Getenv("TENDKIT_REAL_HOMEBREW_CASK"), os.Getenv("TENDKIT_REAL_HOMEBREW_CASK_APP"); token != "" && application != "" {
		cask := applicationByID(t, reordered.Apps, "pkg-homebrew-cask-"+token)
		caskReal, err := filepath.EvalSymlinks(cask.InstallPath)
		if err != nil {
			t.Fatal(err)
		}
		applicationReal, err := filepath.EvalSymlinks(application)
		if err != nil {
			t.Fatal(err)
		}
		if caskReal != applicationReal || cask.Provider.Type != model.ProviderHomebrew || cask.Package != "cask/"+token || cask.UpdateMode != model.ModeAuto {
			t.Fatalf("real Cask was not preserved: %#v", cask)
		}
		t.Logf("real Homebrew Cask: token=%s app=%s", token, caskReal)
	}
	t.Logf("real ripgrep installations: standalone=%s Homebrew=%s", standaloneReal, homebrewReal)
}
