package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/handler"
	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func providerConfig(kind model.ProviderType, version, check, update string, download *model.Download) model.ProviderConfig {
	provider := model.ProviderConfig{Type: kind}
	if version != "" || check != "" || update != "" || download != nil {
		provider.Actions = &model.ProviderActions{Version: version, Check: check, Update: update, Download: download}
	}
	return provider
}

func TestScanReportsProgressMilestones(t *testing.T) {
	disabled := false
	catalog := model.Config{Settings: model.Settings{Scan: model.ScanSettings{
		Path: disabled, Application: disabled,
		Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
	}}}
	stages := make([]string, 0)
	scanner := Scanner{Progress: func(progress Progress) { stages = append(stages, progress.Stage) }}
	if _, _, err := scanner.Scan(context.Background(), catalog, model.RuntimeState{}); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(stages, ","), "prepare,finalize"; got != want {
		t.Fatalf("progress stages = %q, want %q", got, want)
	}
}

func TestScanPropagatesCancellationWithoutReturningPartialState(t *testing.T) {
	disabled, enabled := false, true
	t.Setenv("PATH", t.TempDir())
	catalog := model.Config{Apps: []model.Application{{
		ID: "slow", Name: "Slow", Type: model.ApplicationTypeCLI, InstallPath: "/bin/sh",
		Provider: providerConfig(model.ProviderDefault, "sleep 10", "", "", nil),
	}}, Settings: model.Settings{Scan: model.ScanSettings{
		Path: enabled, Application: disabled,
		Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
	}}}
	catalog.Apps[0].StatusManaged = model.ManagedStatus{CurrentVersion: "before"}
	state := model.RuntimeState{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scanner := Scanner{Runner: runtimeutil.Runner{}, Progress: func(progress Progress) {
		if progress.Stage == "application" && progress.Subject == "Slow" {
			cancel()
		}
	}}
	updatedCatalog, _, err := scanner.Scan(ctx, catalog, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan error = %v, want context.Canceled", err)
	}
	if updatedCatalog.Apps[0].StatusManaged.CurrentVersion != "before" {
		t.Fatalf("cancelled scan returned partial state: %#v", updatedCatalog)
	}
}

func TestMergeAppsDoesNotAliasNestedApplicationFields(t *testing.T) {
	existing := applicationWithNestedFields("existing")
	discovered := applicationWithNestedFields("discovered")
	merged := mergeApps([]model.Application{existing}, []model.Application{discovered})
	if len(merged) != 2 {
		t.Fatalf("merged application count = %d, want 2", len(merged))
	}

	merged[0].Provider.Actions.Version = "changed-existing-action"
	merged[0].Provider.Actions.Download.ExtraArgs[0] = "--changed-existing-arg"
	merged[0].Environment["KEEP"] = "changed-existing-environment"
	merged[1].Provider.Actions.Version = "changed-discovered-action"
	merged[1].Provider.Actions.Download.ExtraArgs[0] = "--changed-discovered-arg"
	merged[1].Environment["KEEP"] = "changed-discovered-environment"

	assertNestedApplicationFields(t, existing, "existing")
	assertNestedApplicationFields(t, discovered, "discovered")
}

func TestScanApplicationCancellationDoesNotAliasInputs(t *testing.T) {
	application := applicationWithNestedFields("target")
	state := model.RuntimeState{Observations: map[string]model.ScanObservation{
		application.ID: {Found: true, Path: "/original/path"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	updated, updatedState, err := (Scanner{}).ScanApplication(ctx, application, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanApplication error = %v, want context.Canceled", err)
	}
	updated.Provider.Actions.Version = "changed-action"
	updated.Provider.Actions.Download.ExtraArgs[0] = "--changed-arg"
	updated.Environment["KEEP"] = "changed-environment"
	updatedState.Observations[application.ID] = model.ScanObservation{Found: false, Path: "/changed/path"}

	assertNestedApplicationFields(t, application, "application input")
	if observation := state.Observations[application.ID]; !observation.Found || observation.Path != "/original/path" {
		t.Fatalf("input observations were modified: %#v", state.Observations)
	}
}

func TestDeduplicateCatalogDoesNotAliasInputs(t *testing.T) {
	application := applicationWithNestedFields("target")
	state := model.RuntimeState{Observations: map[string]model.ScanObservation{
		application.ID: {Found: true, Path: "/original/path"},
	}}

	apps, updatedState := deduplicateCatalog([]model.Application{application}, state)
	if len(apps) != 1 {
		t.Fatalf("deduplicated application count = %d, want 1", len(apps))
	}
	apps[0].Provider.Actions.Version = "changed-action"
	apps[0].Provider.Actions.Download.ExtraArgs[0] = "--changed-arg"
	apps[0].Environment["KEEP"] = "changed-environment"
	updatedState.Observations[application.ID] = model.ScanObservation{Found: false, Path: "/changed/path"}

	assertNestedApplicationFields(t, application, "application input")
	if observation := state.Observations[application.ID]; !observation.Found || observation.Path != "/original/path" {
		t.Fatalf("input observations were modified: %#v", state.Observations)
	}
}

func applicationWithNestedFields(id string) model.Application {
	return model.Application{
		ID: id, Name: id, Type: model.ApplicationTypeCLI, InstallPath: "/missing/" + id,
		Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{
			Version:  "original-action",
			Download: &model.Download{ExtraArgs: []string{"--original-arg"}},
		}},
		Environment: map[string]string{"KEEP": "original-environment"},
	}
}

func assertNestedApplicationFields(t *testing.T, application model.Application, name string) {
	t.Helper()
	if application.Provider.Actions.Version != "original-action" || application.Provider.Actions.Download.ExtraArgs[0] != "--original-arg" || application.Environment["KEEP"] != "original-environment" {
		t.Fatalf("%s nested fields were modified: %#v", name, application)
	}
}

func TestScanApplicationRefreshesOnlyTargetAndPreservesDates(t *testing.T) {
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
}

func TestScanApplicationRendersVersionActionPlaceholdersAndRejectsInvalidTemplates(t *testing.T) {
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
}

func TestScanUsesCatalogDownloadDirectoryForVersionAction(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	enabled, disabled := true, false
	downloadDir := filepath.Join(directory, "catalog downloads")
	catalog := model.Config{
		Settings: model.Settings{
			Downloader: model.DownloaderSettings{StorePath: downloadDir},
			Scan: model.ScanSettings{Path: enabled, Application: disabled,
				Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled}},
		},
		Apps: []model.Application{{
			ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: installed,
			Provider: providerConfig(model.ProviderDefault, "test {download_dir} = "+runtimeutil.QuoteShell(downloadDir)+" && printf '1.2.3\\n'", "", "", nil),
		}},
	}
	updated, _, err := (Scanner{Runner: runtimeutil.Runner{}, DownloadDir: "wrong"}).Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil || updated.Apps[0].StatusManaged.CurrentVersion != "1.2.3" {
		t.Fatalf("catalog download directory was not rendered: apps=%#v err=%v", updated.Apps, err)
	}
}

func TestScanApplicationPackageDoesNotEnumerateEcosystem(t *testing.T) {
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
}

func TestScanApplicationPackageNameDoesNotMatchBuiltInPathDefinition(t *testing.T) {
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
}

func TestScanApplicationRediscoversMovedBuiltInPathApplication(t *testing.T) {
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
}

func TestScanApplicationDoesNotRelocateUnmanagedBuiltInPathApplication(t *testing.T) {
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
}

func TestFullScanPreservesExcludedConfiguredBuiltInPathApplication(t *testing.T) {
	directory := t.TempDir()
	gitPath := filepath.Join(directory, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf 'git version 9.9.9\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	disabled, enabled := false, true
	previous := model.ManagedStatus{CurrentVersion: "2.50.0", UpdateStatus: model.StatusUnchecked, FirstDetectedTime: "2026-08-16T10:00:00+08:00"}
	catalog := model.Config{
		Settings: model.Settings{Scan: model.ScanSettings{
			Path: enabled, Application: disabled, Exclude: []string{"git"},
			Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
		}},
		Apps: []model.Application{{
			ID: "git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: gitPath,
			Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, runtimeutil.QuoteShell(gitPath)+" --version", "", "", nil),
			ScanManaged: true, StatusManaged: previous,
		}},
	}

	result, state, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}).Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Apps) != 1 || result.Apps[0].ID != "git" {
		t.Fatalf("excluded configured PATH application was removed: %#v", result.Apps)
	}
	wantStatus := previous
	wantStatus.CurrentVersion = "9.9.9"
	if result.Apps[0].StatusManaged != wantStatus {
		t.Fatalf("excluded configured PATH application was not refreshed: got=%#v want=%#v", result.Apps[0].StatusManaged, wantStatus)
	}
	if observation := state.Observations["git"]; !observation.Found || observation.Path != gitPath {
		t.Fatalf("excluded configured PATH application was marked missing: %#v", observation)
	}
}

func TestFullScanDoesNotMergeDiscoveryIntoUnmanagedBuiltInPathApplication(t *testing.T) {
	directory := t.TempDir()
	gitPath := filepath.Join(directory, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf 'git version 2.51.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	disabled, enabled := false, true
	configuredPath := filepath.Join(directory, "configured-git-is-missing")
	application := model.Application{
		ID: "git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: configuredPath,
		Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: false,
		StatusManaged: model.ManagedStatus{CurrentVersion: "2.50.0", UpdateStatus: model.StatusCurrent},
	}
	catalog := model.Config{Apps: []model.Application{application}, Settings: model.Settings{Scan: model.ScanSettings{
		Path: enabled, Application: disabled,
		Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
	}}}

	result, state, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}).Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Apps) != 1 || result.Apps[0].InstallPath != configuredPath || result.Apps[0].Provider != application.Provider {
		t.Fatalf("unmanaged application accepted discovered configuration: %#v", result.Apps)
	}
	if result.Apps[0].StatusManaged.UpdateStatus != model.StatusMissing || state.Observations["git"].Found {
		t.Fatalf("unmanaged application was not observed from its configured path: app=%#v state=%#v", result.Apps[0].StatusManaged, state)
	}
}

func TestFullScanMarksExcludedConfiguredApplicationMissing(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	disabled, enabled := false, true
	application := model.Application{
		ID: "git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: filepath.Join(directory, "missing-git"),
		Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: true,
		StatusManaged: model.ManagedStatus{CurrentVersion: "2.50.0", UpdateStatus: model.StatusCurrent},
	}
	catalog := model.Config{Apps: []model.Application{application}, Settings: model.Settings{Scan: model.ScanSettings{
		Path: enabled, Application: disabled, Exclude: []string{"git"},
		Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
	}}}

	result, state, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}).Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Apps) != 1 || result.Apps[0].ID != application.ID {
		t.Fatalf("excluded configured application was removed: %#v", result.Apps)
	}
	if result.Apps[0].StatusManaged.UpdateStatus != model.StatusMissing || state.Observations[application.ID].Found {
		t.Fatalf("excluded configured application missing state was not refreshed: app=%#v state=%#v", result.Apps[0].StatusManaged, state)
	}
}

func TestPackageScanReportsManagerProgressBeforeDiscovery(t *testing.T) {
	disabled, enabled := false, true
	t.Setenv("PATH", t.TempDir())
	settings := model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled}
	events := make([]string, 0)
	scanPackages(context.Background(), settings, runtimeutil.Runner{}, exclusionMatcher{}, nil, func(stage, subject string) {
		events = append(events, stage+":"+subject)
	})
	if len(events) == 0 || events[0] != "package_manager:Node.js" {
		t.Fatalf("package progress events = %v", events)
	}
}

func TestMergeAppsPreservesConfiguredPolicy(t *testing.T) {
	existing := model.Application{ID: "node-js", Name: "Node.js", UpdateMode: model.ModeAuto, Provider: providerConfig("node_lts", "", "", "custom update", nil)}
	found := model.Application{ID: "node-js", Name: "Node.js", InstallPath: "/new/node", UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderNodeLTS}}
	merged := mergeApps([]model.Application{existing}, []model.Application{found})
	if len(merged) != 1 || merged[0].UpdateMode != model.ModeAuto || merged[0].Provider.UpdateAction() != "custom update" || merged[0].InstallPath != "/new/node" {
		t.Fatalf("unexpected merge %#v", merged)
	}
}

func TestMergeAppsEnrichesManagedApplicationCapabilities(t *testing.T) {
	existing := model.Application{ID: "tool", Name: "Tool", Provider: model.ProviderConfig{Type: model.ProviderDefault}, UpdateMode: model.ModeCheck, ScanManaged: true}
	found := model.Application{
		ID: "tool", Name: "Tool", Description: "Useful tool", URL: "https://github.com/example/tool", Provider: providerConfig("pypi", "tool --version", "", "upgrade tool", nil), Package: "tool",
		UpdateMode: model.ModeAuto}
	merged := mergeApps([]model.Application{existing}, []model.Application{found})
	if len(merged) != 1 || merged[0].Description != "Useful tool" || merged[0].URL != found.URL ||
		merged[0].UpdateMode != model.ModeAuto || merged[0].Provider.UpdateAction() != "upgrade tool" {
		t.Fatalf("managed application was not enriched: %#v", merged)
	}
}

func TestDeduplicateCatalogPrefersConfiguredApplication(t *testing.T) {
	configured := model.Application{ID: "dbeaver-community", Name: "DBeaver Community", Type: "application", InstallPath: "/Applications/DBeaver.app", UpdateMode: model.ModeDownload, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Package: "dbeaver/dbeaver"}
	generated := model.Application{ID: "app-dbeaver", Name: "DBeaver", Type: "application", InstallPath: "/Applications/DBeaver.app", UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}}
	configured.StatusManaged = model.ManagedStatus{CurrentVersion: "1.0"}
	state := model.RuntimeState{Observations: map[string]model.ScanObservation{"app-dbeaver": {Found: true, Path: generated.InstallPath}}}
	apps, updated := deduplicateCatalog([]model.Application{configured, generated}, state)
	if len(apps) != 1 || apps[0].ID != "dbeaver-community" {
		t.Fatalf("unexpected apps %#v", apps)
	}
	if !updated.Observations["dbeaver-community"].Found || apps[0].StatusManaged.CurrentVersion != "1.0" {
		t.Fatalf("unexpected state %#v %#v", updated, apps[0])
	}
}

func TestExclusionMatchesIdentityAndWildcard(t *testing.T) {
	matcher := newExclusionMatcher([]string{"package:python:pip", "bundle:com.example.*"})
	if !matcher.excluded(model.Application{Identity: "package:python:pip"}) {
		t.Fatal("expected package exclusion")
	}
	if !matcher.excluded(model.Application{}, "bundle:com.example.Editor") {
		t.Fatal("expected wildcard bundle exclusion")
	}
}

func TestExcludedConfiguredAppsIncludesManualEntriesAndInferredIdentity(t *testing.T) {
	catalog := model.Config{Settings: model.Settings{Scan: model.ScanSettings{Exclude: []string{"cli:gradle", "bundle:com.example.*"}}}, Apps: []model.Application{
		{ID: "gradle", Name: "Gradle", Type: "cli", InstallPath: "/opt/gradle", ScanManaged: false},
		{ID: "editor", Name: "Editor", Type: "application", Identity: "app:com.example.editor", ScanManaged: false},
		{ID: "kept", Name: "Kept", Type: "cli", ScanManaged: true},
	}}
	matched := ExcludedConfiguredApps(catalog)
	if len(matched) != 2 || matched[0].ID != "gradle" || matched[1].ID != "editor" {
		t.Fatalf("unexpected excluded apps %#v", matched)
	}
}

func TestDevelopmentApplicationClassification(t *testing.T) {
	if !(Scanner{}).isDevelopmentApplication(appInfo{Category: "public.app-category.developer-tools"}) {
		t.Fatal("developer category should be included")
	}
	if !(Scanner{}).isDevelopmentApplication(appInfo{Name: "DBeaver"}) {
		t.Fatal("known developer application should be included")
	}
	if (Scanner{}).isDevelopmentApplication(appInfo{Name: "GarageBand", Category: "public.app-category.music"}) {
		t.Fatal("non-development app should be excluded")
	}
}

func TestDeduplicateCatalogPreservesMetadataFromDuplicateDiscovery(t *testing.T) {
	identity := "package:node:@anthropic-ai/claude-code"
	pathCandidate := model.Application{
		ID: "claude", Name: "Claude Code", Type: model.ApplicationTypeCLI,
		Description: "Coding agent CLI", URL: "https://docs.anthropic.com/en/docs/claude-code",
		Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderNPM, "claude --version", "", "", nil),
		Package: "@anthropic-ai/claude-code", Identity: identity, ScanManaged: true,
	}
	packageCandidate := model.Application{
		ID: "pkg-node-anthropic-ai-claude-code", Name: "@anthropic-ai/claude-code", Type: model.ApplicationTypePackage,
		Provider: providerConfig(model.ProviderNPM, "npm list", "", "npm install", nil), Package: "@anthropic-ai/claude-code", Identity: identity, ScanManaged: true,
		UpdateMode: model.ModeAuto}

	apps, _ := deduplicateCatalog([]model.Application{pathCandidate, packageCandidate}, model.RuntimeState{})
	if len(apps) != 1 || apps[0].ID != pathCandidate.ID || apps[0].Type != model.ApplicationTypeCLI {
		t.Fatalf("unexpected duplicate winner: %#v", apps)
	}
	if apps[0].Description != pathCandidate.Description || apps[0].URL != pathCandidate.URL {
		t.Fatalf("duplicate metadata was lost: %#v", apps[0])
	}
	if apps[0].Provider.VersionAction() != pathCandidate.Provider.VersionAction() || apps[0].Provider.UpdateAction() != packageCandidate.Provider.UpdateAction() || apps[0].UpdateMode != model.ModeAuto {
		t.Fatalf("duplicate capabilities were not merged: %#v", apps[0])
	}
}

func TestDeduplicateCatalogDoesNotOverwriteWinnerMetadata(t *testing.T) {
	identity := "package:node:example"
	secondary := model.Application{
		ID: "example-cli", Type: model.ApplicationTypeCLI, Description: "Secondary", URL: "https://example.invalid/secondary",
		Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderNPM, "example --version", "", "", nil), Package: "example", Identity: identity,
	}
	primary := model.Application{
		ID: "pkg-node-example", Type: model.ApplicationTypePackage, Description: "Primary", URL: "https://example.invalid/primary",
		Enabled: true, UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderNPM, "npm list", "", "npm install", nil), Package: "example", Identity: identity,
	}

	apps, _ := deduplicateCatalog([]model.Application{secondary, primary}, model.RuntimeState{})
	if len(apps) != 1 || apps[0].ID != primary.ID || apps[0].Description != primary.Description || apps[0].URL != primary.URL {
		t.Fatalf("winner metadata was overwritten: %#v", apps)
	}
}

func TestDeduplicateCatalogPrefersBuiltInCLIAndMergesPackageCapabilities(t *testing.T) {
	identity := "package:node:@google/gemini-cli"
	cli := model.Application{
		ID: "gemini", Name: "Gemini CLI", Type: model.ApplicationTypeCLI,
		Description: "Google's terminal AI agent.", URL: "https://github.com/google-gemini/gemini-cli", InstallPath: "/usr/local/bin/gemini",
		Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderNPM, "gemini --version", "", "", nil), Package: "@google/gemini-cli", Identity: identity, ScanManaged: true,
	}
	packageApp := model.Application{
		ID: "pkg-node-google-gemini-cli", Name: "@google/gemini-cli", Type: model.ApplicationTypePackage,
		InstallPath: "/usr/local/lib/node_modules/@google/gemini-cli", Enabled: true, UpdateMode: model.ModeAuto,
		Provider: providerConfig(model.ProviderNPM, "npm list @google/gemini-cli", "", "npm install --global @google/gemini-cli@latest", nil), Package: "@google/gemini-cli", Identity: identity, ScanManaged: true,
	}

	apps, _ := deduplicateCatalog([]model.Application{packageApp, cli}, model.RuntimeState{})
	if len(apps) != 1 || apps[0].ID != cli.ID || apps[0].Type != model.ApplicationTypeCLI {
		t.Fatalf("built-in CLI was not canonical: %#v", apps)
	}
	if apps[0].Provider.VersionAction() != cli.Provider.VersionAction() || apps[0].Provider.UpdateAction() != packageApp.Provider.UpdateAction() || apps[0].UpdateMode != model.ModeAuto {
		t.Fatalf("package capabilities were not safely merged: %#v", apps[0])
	}
}

func TestDeduplicateCatalogMatchesUVToolToBuiltInPyPICLI(t *testing.T) {
	cli := model.Application{
		ID: "llm", Name: "LLM", Type: model.ApplicationTypeCLI, InstallPath: "/Users/example/.local/bin/llm",
		Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderPyPI, "llm --version", "", "", nil), Package: "llm", ScanManaged: true,
	}
	uvTool := model.Application{
		ID: "pkg-uv-llm", Name: "llm", Type: model.ApplicationTypePackage, InstallPath: "/Users/example/.local/bin/llm",
		Enabled: true, UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderPyPI, "uv tool run llm", "", "uv tool upgrade llm", nil), Package: "llm", Identity: "package:uv:llm", ScanManaged: true,
	}

	apps, _ := deduplicateCatalog([]model.Application{uvTool, cli}, model.RuntimeState{})
	if len(apps) != 1 || apps[0].ID != cli.ID || apps[0].Provider.UpdateAction() != uvTool.Provider.UpdateAction() {
		t.Fatalf("UV tool and built-in CLI were not merged: %#v", apps)
	}
}

func TestDeduplicateCatalogKeepsBuiltInUpdateCommand(t *testing.T) {
	identity := "package:node:@anthropic-ai/claude-code"
	cli := model.Application{
		ID: "claude", Name: "Claude Code", Type: model.ApplicationTypeCLI, InstallPath: "/usr/local/bin/claude",
		Enabled: true, UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderNPM, "claude --version", "", "claude update", nil), Package: "@anthropic-ai/claude-code", Identity: identity, ScanManaged: true,
	}
	packageApp := model.Application{
		ID: "pkg-node-anthropic-ai-claude-code", Name: "@anthropic-ai/claude-code", Type: model.ApplicationTypePackage,
		Enabled: true, UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderNPM, "npm list @anthropic-ai/claude-code", "", "npm install --global @anthropic-ai/claude-code@latest", nil), Package: "@anthropic-ai/claude-code", Identity: identity, ScanManaged: true,
	}

	apps, _ := deduplicateCatalog([]model.Application{packageApp, cli}, model.RuntimeState{})
	if len(apps) != 1 || apps[0].Provider.UpdateAction() != cli.Provider.UpdateAction() {
		t.Fatalf("built-in update command was overwritten: %#v", apps)
	}
}

func TestDeduplicateCatalogDoesNotMergeUnknownPackagesAcrossEcosystems(t *testing.T) {
	pythonPackage := model.Application{
		ID: "pkg-python-example", Name: "example", Type: model.ApplicationTypePackage,
		Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Package: "example", Identity: "package:python:example", ScanManaged: true,
	}
	uvPackage := model.Application{
		ID: "pkg-uv-example", Name: "example", Type: model.ApplicationTypePackage,
		Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Package: "example", Identity: "package:uv:example", ScanManaged: true,
	}

	apps, _ := deduplicateCatalog([]model.Application{pythonPackage, uvPackage}, model.RuntimeState{})
	if len(apps) != 2 {
		t.Fatalf("unknown cross-ecosystem packages were merged: %#v", apps)
	}
}

func TestDeduplicateCatalogDoesNotMergeKnownPackagesWithoutPATHCLI(t *testing.T) {
	pythonPackage := model.Application{
		ID: "pkg-python-llm", Name: "llm", Type: model.ApplicationTypePackage,
		Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Package: "llm", Identity: "package:python:llm", ScanManaged: true,
	}
	uvPackage := model.Application{
		ID: "pkg-uv-llm", Name: "llm", Type: model.ApplicationTypePackage,
		Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Package: "llm", Identity: "package:uv:llm", ScanManaged: true,
	}

	apps, _ := deduplicateCatalog([]model.Application{pythonPackage, uvPackage}, model.RuntimeState{})
	if len(apps) != 2 {
		t.Fatalf("packages were merged without an active built-in PATH CLI: %#v", apps)
	}
}

func TestMergeAppsCanonicalizesManagedPackageAsBuiltInCLI(t *testing.T) {
	status := model.ManagedStatus{CurrentVersion: "0.1.0", UpdateStatus: model.StatusCurrent}
	configured := model.Application{
		ID: "pkg-node-openai-codex", Name: "@openai/codex", Type: model.ApplicationTypePackage,
		InstallPath: "/usr/local/lib/node_modules/@openai/codex", Enabled: false, UpdateMode: model.ModeAuto,
		Provider: providerConfig(model.ProviderNPM, "npm list @openai/codex", "", "npm install --global @openai/codex@latest", nil),
		Package:  "@openai/codex", Identity: "package:node:@openai/codex", ScanManaged: true,
		Environment: map[string]string{"EXAMPLE": "preserved"}, StatusManaged: status,
	}
	found := model.Application{
		ID: configured.ID, Name: "OpenAI Codex CLI", Type: model.ApplicationTypeCLI, Description: "Terminal coding agent from OpenAI.",
		InstallPath: "/usr/local/bin/codex", Enabled: true, UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderNPM, "codex --version", "", "codex update", nil),
		Package: "@openai/codex", Identity: "package:node:@openai/codex", ScanManaged: true,
	}

	apps := mergeApps([]model.Application{configured}, []model.Application{found})
	if len(apps) != 1 || apps[0].ID != configured.ID || apps[0].Type != model.ApplicationTypeCLI || apps[0].InstallPath != found.InstallPath {
		t.Fatalf("managed package was not canonicalized: %#v", apps)
	}
	if apps[0].Provider.VersionAction() != configured.Provider.VersionAction() || apps[0].Provider.UpdateAction() != configured.Provider.UpdateAction() || apps[0].StatusManaged != status || apps[0].Enabled {
		t.Fatalf("canonicalization did not preserve policy and state: %#v", apps[0])
	}
	if apps[0].Environment["EXAMPLE"] != "preserved" {
		t.Fatalf("environment was not preserved: %#v", apps[0].Environment)
	}
}

func TestRepeatedFullScanDoesNotIntroduceDuplicateCandidateMetadata(t *testing.T) {
	directory := t.TempDir()
	npm := `#!/bin/sh
case "$*" in
  "--version") printf '10.0.0\n' ;;
  "install --help") exit 1 ;;
  "list -g --depth=0 --json") printf '{"dependencies":{"@anthropic-ai/claude-code":{"version":"2.1.233"}}}\n' ;;
  "root -g") printf '%s\n' "` + directory + `" ;;
  "view @anthropic-ai/claude-code description homepage repository.url --json") printf '{}\n' ;;
  *) exit 1 ;;
esac
`
	for name, body := range map[string]string{
		"npm":    npm,
		"claude": "#!/bin/sh\nprintf '2.1.233\\n'\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)
	enabled, disabled := true, false
	catalog := model.Config{Settings: model.Settings{Scan: model.ScanSettings{
		Path: enabled, Application: disabled,
		Packages: model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled},
	}}}
	scanner := Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}
	first, state, err := scanner.Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	firstTarget := applicationByIdentity(first.Apps, "package:node:anthropic-ai-claude-code")
	if firstTarget.Description == "" || firstTarget.URL == "" {
		t.Fatalf("first full scan lost duplicate metadata: %#v", firstTarget)
	}
	if firstTarget.Type != model.ApplicationTypeCLI || firstTarget.Provider.VersionAction() != "claude --version" {
		t.Fatalf("first full scan did not keep the built-in CLI canonical: %#v", firstTarget)
	}

	second, _, err := scanner.Scan(context.Background(), first, state)
	if err != nil {
		t.Fatal(err)
	}
	secondTarget := applicationByIdentity(second.Apps, firstTarget.Identity)
	if secondTarget.Description != firstTarget.Description || secondTarget.URL != firstTarget.URL {
		t.Fatalf("second full scan introduced metadata differences: first=%#v second=%#v", firstTarget, secondTarget)
	}
}

func TestFullScanPrefersBuiltInCLIAndEnrichesFromNodePackage(t *testing.T) {
	directory := t.TempDir()
	npm := `#!/bin/sh
case "$*" in
  "--version") printf '10.0.0\n' ;;
  "install --help") exit 1 ;;
  "list -g --depth=0 --json") printf '{"dependencies":{"@google/gemini-cli":{"version":"0.55.1"}}}\n' ;;
  "root -g") printf '%s\n' "` + directory + `" ;;
  "view @google/gemini-cli description homepage repository.url --json") printf '{}\n' ;;
  *) exit 1 ;;
esac
`
	for name, body := range map[string]string{
		"npm":    npm,
		"gemini": "#!/bin/sh\nprintf '0.55.1\\n'\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)
	enabled, disabled := true, false
	catalog := model.Config{Settings: model.Settings{Scan: model.ScanSettings{
		Path: enabled, Application: disabled,
		Packages: model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled},
	}}}

	result, _, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}).Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	target := applicationByIdentity(result.Apps, "package:node:google-gemini-cli")
	if target.ID != "cli-gemini" || target.Type != model.ApplicationTypeCLI || target.InstallPath != filepath.Join(directory, "gemini") {
		t.Fatalf("full scan did not choose built-in CLI: %#v", target)
	}
	if target.Provider.VersionAction() != "gemini --version" || target.Provider.UpdateAction() == "" || target.UpdateMode != model.ModeAuto {
		t.Fatalf("full scan did not enrich package capabilities: %#v", target)
	}
}

func applicationByIdentity(apps []model.Application, identity string) model.Application {
	for _, application := range apps {
		if application.Identity == identity {
			return application
		}
	}
	return model.Application{}
}

func TestObsidianBundleIsRecognizedAsDevelopmentApplication(t *testing.T) {
	info := appInfo{
		Name:     "Obsidian",
		BundleID: "md.obsidian",
		Category: "public.app-category.productivity",
	}
	if (Scanner{}).isDevelopmentApplication(info) {
		t.Fatal("Obsidian unexpectedly matched the built-in development application rules")
	}
	if !New(model.ScanSettings{BundleID: []string{"MD.OBSIDIAN"}}).isDevelopmentApplication(info) {
		t.Fatal("configured Obsidian Bundle ID was filtered from the macOS application scan")
	}
}

func TestEnrichConfiguredMetadataAddsOnlyMissingManagedBundleDescription(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS application metadata is Darwin-specific")
	}
	appPath := filepath.Join(t.TempDir(), "Fixture.app")
	plistPath := filepath.Join(appPath, filepath.FromSlash("Contents/Info.plist"))
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>
<key>CFBundleName</key><string>Fixture</string>
<key>CFBundleGetInfoString</key><string>Metadata description</string>
</dict></plist>`
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	nonAppPath := filepath.Join(t.TempDir(), "Fixture")
	nonAppPlistPath := filepath.Join(nonAppPath, filepath.FromSlash("Contents/Info.plist"))
	if err := os.MkdirAll(filepath.Dir(nonAppPlistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nonAppPlistPath, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	apps := enrichConfiguredMetadata(context.Background(), []model.Application{
		{ID: "bundle-empty", Type: model.ApplicationTypeBundle, InstallPath: appPath, ScanManaged: true},
		{ID: "bundle-present", Type: model.ApplicationTypeBundle, InstallPath: appPath, Description: "User description", ScanManaged: true},
		{ID: "bundle-unmanaged", Type: model.ApplicationTypeBundle, InstallPath: appPath},
		{ID: "bundle-non-app", Type: model.ApplicationTypeBundle, InstallPath: nonAppPath, ScanManaged: true},
		{ID: "cli", Type: model.ApplicationTypeCLI, InstallPath: appPath, ScanManaged: true},
		{ID: "sdk", Type: model.ApplicationTypePackage, InstallPath: appPath, ScanManaged: true},
	})
	if apps[0].Description != "Metadata description" {
		t.Fatalf("missing bundle description = %q", apps[0].Description)
	}
	if apps[1].Description != "User description" {
		t.Fatalf("existing bundle description overwritten: %q", apps[1].Description)
	}
	if apps[2].Description != "" || apps[3].Description != "" || apps[4].Description != "" || apps[5].Description != "" {
		t.Fatalf("unsupported metadata was enriched: %#v", apps[2:])
	}
}

func TestManagedObsidianIdentityAndObservationRemainStable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS bundle identity is Darwin-specific")
	}
	appPath := filepath.Join(t.TempDir(), "Obsidian.app")
	plistPath := filepath.Join(appPath, filepath.FromSlash("Contents/Info.plist"))
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>md.obsidian</string>
<key>CFBundleName</key><string>Obsidian</string>
<key>CFBundleShortVersionString</key><string>1.13.6</string>
<key>LSApplicationCategoryType</key><string>public.app-category.productivity</string>
</dict></plist>`
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	app := model.Application{
		ID: "obsidian", Name: "Obsidian", Type: model.ApplicationTypeBundle,
		InstallPath: appPath, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Package: "obsidianmd/obsidian-releases",
		UpdateMode: model.ModeDownload, ScanManaged: true,
	}
	previous := model.Config{Apps: []model.Application{app}}
	previous.Apps[0].ScanManaged = false
	catalog := ReconcileNewlyManagedBundleIDs(context.Background(), previous, model.Config{Apps: []model.Application{app}})
	if len(catalog.Settings.Scan.BundleID) != 1 || catalog.Settings.Scan.BundleID[0] != "md.obsidian" {
		t.Fatalf("managed Obsidian Bundle ID was not registered: %#v", catalog.Settings.Scan.BundleID)
	}
	identity, err := New(catalog.Settings.Scan).GenerateIdentity(context.Background(), app)
	if err != nil {
		t.Fatal(err)
	}
	if identity != "app:md.obsidian" {
		t.Fatalf("configured Bundle ID generated identity %q, want app:md.obsidian", identity)
	}
	fallback, err := New(model.ScanSettings{}).GenerateIdentity(context.Background(), app)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fallback, "app-path:") {
		t.Fatalf("unmatched bundle identity = %q, want app-path fallback", fallback)
	}
	excludedApps := catalog.Apps
	previousStatus := model.ManagedStatus{CurrentVersion: "1.0.0", UpdateStatus: model.StatusCurrent, FirstDetectedTime: "2026-08-16T10:00:00+08:00"}
	excludedApps[0].StatusManaged = previousStatus
	session := scanSession{
		scanner:    New(catalog.Settings.Scan),
		catalog:    model.Config{Apps: excludedApps, Settings: model.Settings{Scan: model.ScanSettings{Application: true}}},
		exclusions: newExclusionMatcher([]string{app.ID}),
		index:      indexApps(excludedApps),
		observed:   map[string]model.ManagedStatus{},
	}
	if err := session.observeConfiguredApplications(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshed, refreshedState, err := session.finalize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observed := refreshed.Apps[0].StatusManaged
	if observed.CurrentVersion != "1.13.6" || observed.UpdateStatus == model.StatusMissing || observed.FirstDetectedTime != previousStatus.FirstDetectedTime || !refreshedState.Observations[app.ID].Found {
		t.Fatalf("excluded managed application was not observed from its configured bundle: app=%#v state=%#v", observed, refreshedState)
	}
}

func TestNormalizePackage(t *testing.T) {
	if got := normalizePackage("Scikit_Learn"); got != "scikit-learn" || strings.Contains(got, "_") {
		t.Fatalf("unexpected normalized name %q", got)
	}
}

func TestCompletePackageInventoryOmissionDirectlyVerifiesConfiguredApplication(t *testing.T) {
	directory := t.TempDir()
	python := filepath.Join(directory, "python3")
	if err := os.WriteFile(python, []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(directory, "sample-package")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	disabled, enabled := false, true
	application := model.Application{
		ID: "pkg-python-sample", Name: "sample", Type: model.ApplicationTypePackage,
		Identity: "package:python:sample", Package: "sample", InstallPath: installed,
		ScanManaged: true, Provider: providerConfig(model.ProviderDefault, "printf 'sample 1.2.3'", "", "", nil),
		StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusCurrent, CurrentVersion: "1.2.2"},
	}
	catalog := model.Config{Apps: []model.Application{application}, Settings: model.Settings{Scan: model.ScanSettings{
		Path: disabled, Application: disabled,
		Packages: model.PackageScanSettings{Python: enabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
	}}}

	updated, state, err := (Scanner{Runner: runtimeutil.Runner{}}).Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Apps) != 1 || updated.Apps[0].ID != application.ID {
		t.Fatalf("complete inventory omission removed configured application: %#v", updated.Apps)
	}
	if updated.Apps[0].StatusManaged.CurrentVersion != "1.2.3" || updated.Apps[0].StatusManaged.UpdateStatus == model.StatusMissing {
		t.Fatalf("configured application was not directly verified: %#v", updated.Apps[0].StatusManaged)
	}
	if observation := state.Observations[application.ID]; !observation.Found {
		t.Fatalf("directly verified application observation = %#v", observation)
	}
}

func TestIncompletePackageScanPreservesManagedPackageState(t *testing.T) {
	for _, ecosystem := range []string{packageEcosystemPython, packageEcosystemNode, packageEcosystemGo, packageEcosystemUV, packageEcosystemRuby} {
		t.Run(ecosystem, func(t *testing.T) {
			application := model.Application{ID: "managed", Type: model.ApplicationTypePackage, Identity: "package:" + ecosystem + ":sample", ScanManaged: true}
			previous := model.ManagedStatus{UpdateStatus: model.StatusCurrent, CurrentVersion: "1.2.3"}
			application.StatusManaged = previous
			session := scanSession{
				state:    model.RuntimeState{},
				observed: map[string]model.ManagedStatus{},
				packages: packageScanResult{Complete: map[string]bool{ecosystem: false}, Errors: map[string]error{ecosystem: errors.New("inventory failed")}},
			}
			if !session.retainIncompleteManagedPackage(application) {
				t.Fatal("managed package was not handled")
			}
			actual := session.observed[application.ID]
			if actual.UpdateStatus != previous.UpdateStatus || actual.CurrentVersion != previous.CurrentVersion || !strings.Contains(actual.Error, "inventory failed") {
				t.Fatalf("previous state was not preserved: %#v", actual)
			}
		})
	}
}

func TestGoMetadataExecutionFailurePreservesManagedPackageState(t *testing.T) {
	directory := t.TempDir()
	goPath := filepath.Join(directory, "gopath")
	binDirectory := filepath.Join(goPath, "bin")
	if err := os.MkdirAll(binDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDirectory, "sample"), []byte("not relevant"), 0o755); err != nil {
		t.Fatal(err)
	}
	managerDirectory := filepath.Join(directory, "manager")
	if err := os.Mkdir(managerDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	goScript := `#!/bin/sh
if [ "$1" = "env" ]; then
    printf '%s\n\n' "$FAKE_GOPATH"
    exit 0
fi
/bin/sleep 1
`
	if err := os.WriteFile(filepath.Join(managerDirectory, "go"), []byte(goScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", managerDirectory)
	t.Setenv("FAKE_GOPATH", goPath)
	disabled, enabled := false, true
	application := model.Application{
		ID: "pkg-go-sample", Name: "sample", Type: model.ApplicationTypePackage, InstallPath: filepath.Join(binDirectory, "sample"),
		Identity: "package:go:example.invalid/sample", ScanManaged: true,
	}
	previous := model.ManagedStatus{UpdateStatus: model.StatusCurrent, CurrentVersion: "1.2.3"}
	catalog := model.Config{Apps: []model.Application{application}, Settings: model.Settings{Scan: model.ScanSettings{
		Path: disabled, Application: disabled,
		Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: enabled, UV: disabled, Ruby: disabled},
	}}}
	catalog.Apps[0].StatusManaged = previous
	state := model.RuntimeState{}

	updatedCatalog, _, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: 20 * time.Millisecond}}).Scan(context.Background(), catalog, state)
	if err != nil {
		t.Fatal(err)
	}
	actual := updatedCatalog.Apps[0].StatusManaged
	if len(updatedCatalog.Apps) != 1 || actual.UpdateStatus != previous.UpdateStatus || actual.CurrentVersion != previous.CurrentVersion || actual.Error == "" {
		t.Fatalf("incomplete Go metadata scan did not preserve state: apps=%#v state=%#v", updatedCatalog.Apps, actual)
	}
}

func TestPackageScannersReportMissingManagersAsIncomplete(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	disabled, enabled := false, true
	cases := []struct {
		name      string
		settings  model.PackageScanSettings
		ecosystem string
	}{
		{"python", model.PackageScanSettings{Python: enabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled}, packageEcosystemPython},
		{"node", model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled}, packageEcosystemNode},
		{"go", model.PackageScanSettings{Python: disabled, Node: disabled, Go: enabled, UV: disabled, Ruby: disabled}, packageEcosystemGo},
		{"uv", model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: enabled, Ruby: disabled}, packageEcosystemUV},
		{"ruby", model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: enabled}, packageEcosystemRuby},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := scanPackages(context.Background(), test.settings, runtimeutil.Runner{IdleTimeout: time.Second}, exclusionMatcher{}, nil, nil)
			if result.Complete[test.ecosystem] || result.Errors[test.ecosystem] == nil {
				t.Fatalf("missing manager was not reported: %#v", result)
			}
		})
	}
}

func TestPackageScanErrorsUseLocalizedStableMessages(t *testing.T) {
	previous := i18n.Current()
	t.Cleanup(func() { i18n.Set(previous) })
	i18n.Set(i18n.Chinese)

	for _, err := range []error{
		&handler.PackageManagerUnavailableError{Manager: "npm"},
		&handler.PackageInventoryIncompleteError{Ecosystem: "Node.js", Message: "incomplete Node.js package inventory"},
	} {
		message := i18n.T("scanner.package_scan_incomplete", "node", packageScanErrorText(err))
		if strings.Contains(message, "package manager") || strings.Contains(message, "incomplete Node.js") {
			t.Fatalf("localized package scan message retained a known English error: %q", message)
		}
	}
}

func TestPackageScannersRejectFailedOrInvalidInventories(t *testing.T) {
	disabled, enabled := false, true
	cases := []struct {
		name      string
		binaries  map[string]string
		settings  model.PackageScanSettings
		ecosystem string
	}{
		{"python invalid JSON", map[string]string{"python3": "printf 'not-json\\n'; exit 0"}, model.PackageScanSettings{Python: enabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled}, packageEcosystemPython},
		{"node partial nonzero", map[string]string{"npm": "printf '{\"dependencies\":{}}\\n'; exit 1"}, model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled}, packageEcosystemNode},
		{"go nonzero", map[string]string{"go": "exit 1"}, model.PackageScanSettings{Python: disabled, Node: disabled, Go: enabled, UV: disabled, Ruby: disabled}, packageEcosystemGo},
		{"uv invalid output", map[string]string{"uv": "printf 'unexpected output\\n'; exit 0"}, model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: enabled, Ruby: disabled}, packageEcosystemUV},
		{"ruby invalid JSON", map[string]string{"ruby": "printf 'not-json\\n'; exit 0", "gem": "exit 0"}, model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: enabled}, packageEcosystemRuby},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			for name, body := range test.binaries {
				path := filepath.Join(directory, name)
				if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", directory)
			result := scanPackages(context.Background(), test.settings, runtimeutil.Runner{IdleTimeout: time.Second}, exclusionMatcher{}, nil, nil)
			if result.Complete[test.ecosystem] || result.Errors[test.ecosystem] == nil {
				t.Fatalf("failed inventory was accepted: %#v", result)
			}
		})
	}
}

func TestScanPreservesOnlyExistingUpdateStatuses(t *testing.T) {
	for _, status := range []string{"skipped", "failed", "current", "update_available", "downloaded", "updated"} {
		if !preservesUpdateStatus(status) {
			t.Errorf("expected %q to be preserved", status)
		}
	}
	for _, status := range []string{"", "unchecked", "missing"} {
		if preservesUpdateStatus(status) {
			t.Errorf("did not expect %q to be preserved", status)
		}
	}
}

func TestPreserveScannedApplicationStateDefaultsEmptyUpdateStatus(t *testing.T) {
	previous := model.ManagedStatus{UpdateStatus: model.StatusCurrent, CurrentVersion: "1.0.0"}
	merged := preserveScannedApplicationState(model.ManagedStatus{CurrentVersion: "1.0.1"}, previous)
	if merged.UpdateStatus != model.StatusCurrent {
		t.Fatalf("empty observed status replaced existing status: %#v", merged)
	}
	created := preserveScannedApplicationState(model.ManagedStatus{CurrentVersion: "1.0.0"}, model.ManagedStatus{})
	if created.UpdateStatus != model.StatusUnchecked {
		t.Fatalf("new observed status was not normalized: %#v", created)
	}
}

func TestMergeApplicationStatePreservesFirstDetectedTime(t *testing.T) {
	primary := model.ManagedStatus{}
	secondary := model.ManagedStatus{FirstDetectedTime: "2026-08-15T10:00:00+08:00"}
	merged := mergeApplicationState(primary, secondary)
	if merged.FirstDetectedTime != secondary.FirstDetectedTime {
		t.Fatalf("first detected time = %q", merged.FirstDetectedTime)
	}
}

func TestGenerateIdentityUsesUVScannerEcosystem(t *testing.T) {
	identity, err := New(model.ScanSettings{}).GenerateIdentity(context.Background(), model.Application{
		Name: "Ruff", Type: model.ApplicationTypePackage, Package: "ruff",
		Provider: model.ProviderConfig{Type: model.ProviderUV},
	})
	if err != nil || identity != "package:uv:ruff" {
		t.Fatalf("GenerateIdentity() = %q, %v", identity, err)
	}
}

func TestExistingIndexDoesNotMatchPackageAcrossProvidersByName(t *testing.T) {
	index := indexApps([]model.Application{{
		ID: "pkg-python-tavily-cli", Name: "tavily-cli", Type: model.ApplicationTypePackage,
		Package: "tavily-cli", Provider: model.ProviderConfig{Type: model.ProviderPyPI},
	}})
	uvPackage := model.Application{
		ID: "pkg-uv-tavily-cli", Name: "tavily-cli", Type: model.ApplicationTypePackage,
		Package: "tavily-cli", Provider: model.ProviderConfig{Type: model.ProviderUV},
	}
	if got := index.match(uvPackage); got != "" {
		t.Fatalf("UV package matched existing PyPI package %q", got)
	}

	index = indexApps([]model.Application{{
		ID: "manual-uv-tavily-cli", Name: "tavily-cli", Type: model.ApplicationTypePackage,
		Provider: model.ProviderConfig{Type: model.ProviderUV},
	}})
	if got := index.match(uvPackage); got != "manual-uv-tavily-cli" {
		t.Fatalf("UV package did not match same-provider configuration without package: %q", got)
	}
}

func TestExistingIndexFailsClosedOnNormalizedPackageIdentityCollision(t *testing.T) {
	for _, values := range [][]string{{"foo.bar", "foobar"}} {
		apps := []model.Application{
			{ID: "first", Name: values[0], Type: model.ApplicationTypePackage, Package: values[0], Provider: model.ProviderConfig{Type: model.ProviderPyPI}},
			{ID: "second", Name: values[1], Type: model.ApplicationTypePackage, Package: values[1], Provider: model.ProviderConfig{Type: model.ProviderPyPI}},
		}
		index := indexApps(apps)
		if got := index.match(apps[0]); got != "" {
			t.Fatalf("%q/%q collision matched %q", values[0], values[1], got)
		}
	}
	if model.PackageIdentity("python", "foo_bar") == model.PackageIdentity("python", "foo-bar") {
		t.Fatal("underscore deletion and an existing hyphen unexpectedly collided")
	}
}

func TestDeduplicateCatalogKeepsNormalizedPackageCollisionButClearsAmbiguousIdentity(t *testing.T) {
	apps, _ := deduplicateCatalog([]model.Application{
		{ID: "one", Name: "foo.bar", Type: model.ApplicationTypePackage, Package: "foo.bar", Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Identity: "package:python:foobar"},
		{ID: "two", Name: "foobar", Type: model.ApplicationTypePackage, Package: "foobar", Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Identity: "package:python:foobar"},
	}, model.RuntimeState{})
	if len(apps) != 2 || apps[0].Identity == "" || apps[1].Identity != "" {
		t.Fatalf("collision catalog = %#v", apps)
	}
}
