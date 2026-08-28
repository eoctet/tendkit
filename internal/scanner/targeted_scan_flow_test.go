package scanner

import (
	"errors"
	"os"
	"path/filepath"

	"strings"

	"github.com/eoctet/tendkit/internal/model"

	"context"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"

	"testing"
)

func TestScannerTargetedScanFlow(t *testing.T) {
	t.Run("scan-application-refreshes-only-target-and-preserves-dates", func(t *testing.T) {
		application := model.Application{
			ID: "shell", Name: "Shell", Type: model.ApplicationTypeCLI, InstallPath: "/bin/sh",
			Provider: providerConfig(model.ProviderDefault, "printf 'shell 1.2.3'", "", "", nil),
		}
		application.StatusManaged = model.ManagedStatus{FirstDetectedTime: "2026-08-15T10:00:00+08:00", LatestVersion: "1.2.4"}
		stages := make([]string, 0)
		updated, _, err := (Scanner{Runner: runtimeutil.Runner{}, Progress: func(progress Progress) {
			stages = append(stages, progress.Stage+":"+progress.Subject)
		}}).ScanApplication(context.Background(), application, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		if got := updated.StatusManaged; got.CurrentVersion != "1.2.3" || got.FirstDetectedTime != "2026-08-15T10:00:00+08:00" || got.LatestVersion != "1.2.4" {
			t.Fatalf("target state = %#v", got)
		}
		if strings.Join(stages, ",") != "prepare:,application:Shell,finalize:" {
			t.Fatalf("single scan stages = %v", stages)
		}
	})
	t.Run("scan-application-renders-version-action-placeholders-and-rejects-invalid-templates", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "installed")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		downloadDir := filepath.Join(directory, "downloads with spaces")
		marker := filepath.Join(directory, "rendered-as-shell")
		application := model.Application{
			ID: "sample", Name: "Sample; touch " + marker, Type: model.ApplicationTypeCLI, InstallPath: installed,
			Provider:      providerConfig(model.ProviderDefault, "test {name} = "+runtimeutil.QuoteShell("Sample; touch "+marker)+" && test {current_version} = '1.0.0' && test {download_dir} = "+runtimeutil.QuoteShell(downloadDir)+" && printf '1.2.3\\n' # {{.Version}}", "", "", nil),
			StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"},
		}
		scanner := Scanner{Runner: runtimeutil.Runner{}, DownloadDir: downloadDir}
		updated, _, err := scanner.ScanApplication(context.Background(), application, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		if updated.StatusManaged.CurrentVersion != "1.2.3" {
			t.Fatalf("rendered version action = %#v", updated.StatusManaged)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("version placeholder escaped shell quoting: %v", err)
		}
		for _, action := range []string{"touch " + runtimeutil.QuoteShell(marker) + " {unknown}", "touch " + runtimeutil.QuoteShell(marker) + " {name"} {
			application.Provider.Actions.Version = action
			updated, _, err := scanner.ScanApplication(context.Background(), application, model.RuntimeState{})
			if err != nil {
				t.Fatalf("invalid version action %q returned %v", action, err)
			}
			if updated.StatusManaged.UpdateStatus != model.StatusFailed || updated.StatusManaged.Error == "" {
				t.Fatalf("invalid version action %q state = %#v", action, updated.StatusManaged)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid version action %q executed runner: %v", action, err)
			}
			observed, err := scanner.observeConfiguredApplication(context.Background(), application)
			if err != nil || observed.UpdateStatus != model.StatusFailed || observed.Error == "" {
				t.Fatalf("configured invalid version action %q state = %#v err=%v", action, observed, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("configured invalid version action %q executed runner: %v", action, err)
			}
		}
	})
	t.Run("scan-application-package-does-not-enumerate-ecosystem", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "target-package")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		application := model.Application{
			ID: "pkg-python-target", Name: "target", Type: model.ApplicationTypePackage,
			Identity: "package:python:target", Package: "target", InstallPath: installed,
			Provider: providerConfig(model.ProviderDefault, "printf 'target 1.2.3'", "", "", nil),
		}
		stages := make([]string, 0)
		updated, _, err := (Scanner{Runner: runtimeutil.Runner{}, Progress: func(progress Progress) {
			stages = append(stages, progress.Stage+":"+progress.Subject)
		}}).ScanApplication(context.Background(), application, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		if updated.StatusManaged.CurrentVersion != "1.2.3" {
			t.Fatalf("target package version = %q", updated.StatusManaged.CurrentVersion)
		}
		if strings.Join(stages, ",") != "prepare:,application:target,finalize:" {
			t.Fatalf("single package scan enumerated its ecosystem: %v", stages)
		}
	})
	t.Run("scan-application-package-name-does-not-match-built-in-path-definition", func(t *testing.T) {
		directory := t.TempDir()
		git := filepath.Join(directory, "git")
		if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf 'git version 9.9.9\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		installed := filepath.Join(directory, "python-git-package")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		application := model.Application{
			ID: "pkg-python-git", Name: "Git", Type: model.ApplicationTypePackage,
			Identity: "package:python:git", Package: "git", InstallPath: installed,
			Provider: providerConfig(model.ProviderDefault, "printf 'package 1.2.3'", "", "", nil),
		}

		updated, _, err := (Scanner{Runner: runtimeutil.Runner{}}).ScanApplication(context.Background(), application, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		if updated.StatusManaged.CurrentVersion != "1.2.3" || updated.Type != model.ApplicationTypePackage {
			t.Fatalf("package name collision used built-in PATH definition: %#v", updated)
		}
	})
	t.Run("scan-application-rediscovers-moved-built-in-path-application", func(t *testing.T) {
		directory := t.TempDir()
		gitPath := filepath.Join(directory, "git")
		if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf 'git version 9.9.9\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		target := model.Application{
			ID: "cli-git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: "/old/location/git",
			Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: true,
		}
		application, state, err := (Scanner{Runner: runtimeutil.Runner{}}).ScanApplication(context.Background(), target, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		if application.InstallPath != gitPath || state.Observations["cli-git"].Path != gitPath || application.StatusManaged.CurrentVersion == "" {
			t.Fatalf("moved built-in application was not rediscovered: app=%#v", application)
		}
	})
	t.Run("scan-application-does-not-relocate-unmanaged-built-in-path-application", func(t *testing.T) {
		directory := t.TempDir()
		gitPath := filepath.Join(directory, "git")
		if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf 'git version 2.51.0\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		target := model.Application{
			ID: "git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: "/old/location/git",
			Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: false,
			StatusManaged: model.ManagedStatus{CurrentVersion: "2.50.0", UpdateStatus: model.StatusCurrent},
		}

		application, state, err := (Scanner{Runner: runtimeutil.Runner{}}).ScanApplication(context.Background(), target, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		if application.InstallPath != target.InstallPath || application.Provider != target.Provider {
			t.Fatalf("unmanaged application accepted discovered configuration: %#v", application)
		}
		if application.StatusManaged.UpdateStatus != model.StatusMissing || state.Observations["git"].Found {
			t.Fatalf("unmanaged application was not observed from its configured path: app=%#v state=%#v", application.StatusManaged, state)
		}
	})
}
