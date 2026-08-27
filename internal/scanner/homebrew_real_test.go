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
	catalog := model.Config{
		SchemaVersion: model.SchemaVersion,
		Settings: model.Settings{Scan: model.ScanSettings{
			Path: true, Packages: model.PackageScanSettings{HomebrewFormula: true, HomebrewCask: true},
		}},
		Apps: []model.Application{{
			ID: "ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI,
			InstallPath: standalone, Enabled: true, UpdateMode: model.ModeAuto,
			Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease, Actions: &model.ProviderActions{Version: "rg --version"}},
			Package:  "BurntSushi/ripgrep", Identity: "cli:ripgrep", ScanManaged: true,
		}},
		ScanVersionControl: map[string]map[string]model.ScanKeepResolution{},
	}
	updated, _, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: 2 * time.Minute}}).Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	standaloneApp := applicationByID(t, updated.Apps, "ripgrep")
	homebrewApp := applicationByID(t, updated.Apps, "pkg-homebrew-formula-ripgrep")
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
	if token, application := os.Getenv("TENDKIT_REAL_HOMEBREW_CASK"), os.Getenv("TENDKIT_REAL_HOMEBREW_CASK_APP"); token != "" && application != "" {
		cask := applicationByID(t, updated.Apps, "pkg-homebrew-cask-"+token)
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
