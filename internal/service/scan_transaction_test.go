package service

import (
	"encoding/json"
	"os"

	"strings"

	"path/filepath"
	"testing"

	"context"
	"sync"
	"time"

	"runtime"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
	providerpkg "github.com/eoctet/tendkit/internal/updater/provider"

	"errors"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestLogScanFailureContract(t *testing.T) {
	for _, test := range []struct {
		name, subject, event, status, message, level string
		cancelled                                    bool
	}{
		{"full-failure", "", "scan_failed", model.StatusFailed, "scan operation failed", "ERROR", false},
		{"full-cancelled", "", "scan_cancelled", model.StatusCancelled, "scan operation cancelled", "WARN", true},
		{"target-failure", "app-id", "scan_failed", model.StatusFailed, "scan operation failed", "ERROR", false},
		{"target-cancelled", "app-id", "scan_cancelled", model.StatusCancelled, "scan operation cancelled", "WARN", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			log, err := logutil.NewLogger(directory)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if test.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			logScanFailure(log, ctx, time.Now().Add(-time.Second), test.subject, errors.New("failure detail"))
			data, err := os.ReadFile(filepath.Join(directory, "run.log"))
			if err != nil {
				t.Fatal(err)
			}
			var entry logutil.LogEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Level != test.level || entry.Event != test.event || entry.Operation != "scan" || entry.Status != test.status || entry.Message != test.message || entry.Detail != "failure detail" || entry.AppID != test.subject || entry.ResultCount != 0 || entry.DurationMS <= 0 || entry.DurationMS > 10_000 {
				t.Fatalf("entry = %#v", entry)
			}
		})
	}
}

func TestServiceScanTransaction(t *testing.T) {
	t.Run("preview-scan-persists-existing-state-without-persisting-candidates", func(t *testing.T) {
		directory := t.TempDir()
		binDirectory := filepath.Join(directory, "bin")
		if err := os.Mkdir(binDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binDirectory, "git"), []byte("#!/bin/sh\necho 'git version 2.50.0'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDirectory)
		store := testStore(directory)
		catalog := scanTestCatalog()
		catalog.Settings.LogDir = filepath.Join(directory, "logs")
		saveTestConfig(t, store, catalog)

		pathScans := 0
		preview, err := (&Service{config: store, GitHubResolver: noGitHubResolver{}}).PreviewScan(context.Background(), ScanObserver{Progress: func(progress model.ScanProgress) {
			if progress.Stage == model.ScanStagePath {
				pathScans++
			}
		}})
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Added) != 1 || preview.Added[0].ID != "cli-git" || preview.Added[0].StatusManaged.FirstDetectedTime == "" {
			t.Fatalf("unexpected preview %#v %#v", preview.Added, preview.Config.Apps)
		}
		savedCatalog, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(savedCatalog.Apps) != 0 {
			t.Fatalf("preview persistence boundary is incorrect: %#v", savedCatalog)
		}
		if pathScans != 1 {
			t.Fatalf("full scan ran %d times, want 1", pathScans)
		}
		data, err := os.ReadFile(filepath.Join(directory, "logs", "run.log"))
		if err != nil {
			t.Fatal(err)
		}
		if content := string(data); !strings.Contains(content, `"event":"scan_started"`) || !strings.Contains(content, `"event":"scan_finished"`) {
			t.Fatalf("persistent scan lifecycle log is incomplete: %s", content)
		}
	})
	t.Run("preview-scan-logs-incomplete-package-ecosystem-without-existing-application", func(t *testing.T) {
		directory := t.TempDir()
		binDirectory := filepath.Join(directory, "bin")
		if err := os.Mkdir(binDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		goBinary := filepath.Join(binDirectory, "go")
		if err := os.WriteFile(goBinary, []byte("#!/bin/sh\nif [ \"$1\" = env ]; then exit 7; fi\necho 'go version go1.25.0 darwin/arm64'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDirectory)
		store := testStore(directory)
		catalog := scanTestCatalog()
		catalog.Settings.Scan.Packages.Go = true
		catalog.Settings.LogDir = filepath.Join(directory, "logs")
		saveTestConfig(t, store, catalog)

		if _, err := (&Service{config: store, GitHubResolver: noGitHubResolver{}}).PreviewScan(context.Background(), ScanObserver{}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(directory, "logs", "run.log"))
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, `"event":"scan_package_incomplete"`) ||
			!strings.Contains(content, `"app_id":"go"`) ||
			!strings.Contains(content, `go env exited with code 7`) {
			t.Fatalf("package diagnostic log is missing: %s", content)
		}
	})
	t.Run("preview-scan-logs-skipped-path-action-binding", func(t *testing.T) {
		directory := t.TempDir()
		firstDirectory := filepath.Join(directory, "first")
		secondDirectory := filepath.Join(directory, "second")
		for _, binDirectory := range []string{firstDirectory, secondDirectory} {
			if err := os.Mkdir(binDirectory, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(binDirectory, "pip3"), []byte("#!/bin/sh\necho 'pip 25.0 from fixture'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", strings.Join([]string{firstDirectory, secondDirectory}, string(os.PathListSeparator)))
		store := testStore(directory)
		catalog := scanTestCatalog()
		catalog.Settings.LogDir = filepath.Join(directory, "logs")
		saveTestConfig(t, store, catalog)

		if _, err := (&Service{config: store, GitHubResolver: noGitHubResolver{}}).PreviewScan(context.Background(), ScanObserver{}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(directory, "logs", "run.log"))
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, `"event":"scan_path_action_binding_skipped"`) ||
			!strings.Contains(content, `"app_id":"pip3"`) ||
			!strings.Contains(content, `action=update`) ||
			!strings.Contains(content, `command does not start with executable`) {
			t.Fatalf("Path action diagnostic log is missing: %s", content)
		}
	})
	t.Run("preview-scan-logs-persistence-failure", func(t *testing.T) {
		directory := t.TempDir()
		binDirectory := filepath.Join(directory, "bin")
		if err := os.Mkdir(binDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binDirectory, "git"), []byte("#!/bin/sh\necho 'git version 2.50.0'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDirectory)
		store := testStore(directory)
		catalog := scanTestCatalog()
		catalog.Settings.LogDir = filepath.Join(directory, "logs")
		saveTestConfig(t, store, catalog)
		configPath := testConfigPath(directory)
		var changed sync.Once
		service := &Service{config: store}
		observer := ScanObserver{Progress: func(progress model.ScanProgress) {
			if progress.Stage != model.ScanStagePath {
				return
			}
			changed.Do(func() {
				content, err := os.ReadFile(configPath)
				if err == nil {
					err = os.WriteFile(configPath, append(content, '\n'), 0o600)
				}
				if err != nil {
					t.Errorf("mutate config during scan: %v", err)
				}
			})
		}}
		if _, err := service.PreviewScan(context.Background(), observer); err == nil {
			t.Fatal("external persistence conflict was accepted")
		}
		data, err := os.ReadFile(filepath.Join(directory, "logs", "run.log"))
		if err != nil {
			t.Fatal(err)
		}
		if content := string(data); !strings.Contains(content, `"event":"scan_failed"`) {
			t.Fatalf("persistence failure terminal event is missing: %s", content)
		}
	})
	t.Run("preview-scan-logs-cancellation", func(t *testing.T) {
		directory := t.TempDir()
		store := testStore(directory)
		catalog := scanTestCatalog()
		catalog.Settings.LogDir = filepath.Join(directory, "logs")
		saveTestConfig(t, store, catalog)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (&Service{config: store}).PreviewScan(ctx, ScanObserver{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled scan error = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(directory, "logs", "run.log"))
		if err != nil {
			t.Fatal(err)
		}
		if content := string(data); !strings.Contains(content, `"event":"scan_cancelled"`) {
			t.Fatalf("cancellation terminal event is missing: %s", content)
		}
	})
	t.Run("preview-scan-keeps-excluded-configured-built-in-path-application-after-restart", func(t *testing.T) {
		directory := t.TempDir()
		binDirectory := filepath.Join(directory, "bin")
		if err := os.Mkdir(binDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		gitPath := filepath.Join(binDirectory, "git")
		if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf 'git version 9.9.9\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDirectory)
		store := testStore(directory)
		catalog := scanTestCatalog()
		catalog.Settings.LogDir = filepath.Join(directory, "logs")
		catalog.Settings.Scan.Exclude = []string{"git"}
		previous := model.ManagedStatus{CurrentVersion: "2.49.0", UpdateStatus: model.StatusUnchecked, FirstDetectedTime: "2026-08-16T10:00:00+08:00"}
		catalog.Apps = []model.Application{{
			ID: "git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: gitPath,
			Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, runtimeutil.QuoteShell(gitPath)+" --version", "printf '9.9.9'", "", nil, ""), ScanManaged: true, StatusManaged: previous,
		}}
		saveTestConfig(t, store, catalog)

		preview, err := (&Service{config: store, GitHubResolver: noGitHubResolver{}}).PreviewScan(context.Background(), ScanObserver{})
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Config.Apps) != 1 || preview.Config.Apps[0].ID != "git" || len(preview.Removed) != 0 {
			t.Fatalf("excluded configured PATH application became removed: apps=%#v removed=%#v", preview.Config.Apps, preview.Removed)
		}
		if len(preview.Excluded) != 1 || preview.Excluded[0].ID != "git" {
			t.Fatalf("excluded configured PATH application was not classified as excluded: %#v", preview.Excluded)
		}
		if observation := preview.State.Observations["git"]; !observation.Found || observation.Path != gitPath {
			t.Fatalf("excluded configured PATH application was marked missing: %#v", observation)
		}
		freshStore := config.New(testConfigPath(directory), filepath.Join(directory, "config.lock"))
		saved, err := freshStore.Load()
		if err != nil {
			t.Fatal(err)
		}
		wantStatus := previous
		wantStatus.CurrentVersion = "9.9.9"
		if len(saved.Apps) != 1 || saved.Apps[0].ID != "git" || saved.Apps[0].StatusManaged != wantStatus {
			t.Fatalf("excluded configured PATH application health was not refreshed on disk: %#v", saved.Apps)
		}
	})
	t.Run("preview-application-scan-does-not-run-unrelated-applications", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "installed")
		marker := filepath.Join(directory, "unrelated-ran")
		targetMarker := filepath.Join(directory, "target-runs")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := testStore(directory)
		catalog := config.Default()
		catalog.Settings.LogDir = filepath.Join(directory, "logs")
		catalog.Apps = []model.Application{
			{ID: "target", Name: "Target", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "printf x >> "+runtimeutil.QuoteShell(targetMarker)+"; printf 'target 1.2.3'", "printf '1.2.3'", "", nil, "")},
			{ID: "other", Name: "Other", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "touch "+runtimeutil.QuoteShell(marker)+"; printf 'other 9.9.9'", "printf '9.9.9'", "", nil, "")},
		}
		catalog.Apps[0].StatusManaged = model.ManagedStatus{FirstDetectedTime: "2026-08-15T10:00:00+08:00"}
		catalog.Apps[1].StatusManaged = model.ManagedStatus{CurrentVersion: "8.8.8", FirstDetectedTime: "2026-08-14T10:00:00+08:00"}
		saveTestConfig(t, store, catalog)
		preview, err := (&Service{config: store}).PreviewApplicationScan(context.Background(), catalog.Apps[0], ScanObserver{})
		if err != nil {
			t.Fatal(err)
		}
		if testStatus(preview.Config, "target").CurrentVersion != "1.2.3" || testStatus(preview.Config, "target").FirstDetectedTime != "2026-08-15T10:00:00+08:00" {
			t.Fatalf("target was not refreshed correctly: %#v", testStatus(preview.Config, "target"))
		}
		if testStatus(preview.Config, "other") != catalog.Apps[1].StatusManaged {
			t.Fatalf("unrelated state changed: %#v", testStatus(preview.Config, "other"))
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("unrelated version command ran: %v", err)
		}
		runs, err := os.ReadFile(targetMarker)
		if err != nil || string(runs) != "x" {
			t.Fatalf("single scan ran more than once: %q %v", runs, err)
		}
		savedConfig, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if testStatus(savedConfig, "target").CurrentVersion != "1.2.3" || testStatus(savedConfig, "other") != catalog.Apps[1].StatusManaged {
			t.Fatalf("single scan state was not persisted selectively: %#v", savedConfig)
		}
	})
	t.Run("save-scan-snapshot-registers-bundle-id-when-app-becomes-managed", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("macOS bundle persistence is Darwin-specific")
		}
		directory := t.TempDir()
		appPath := filepath.Join(directory, "Knowledge.app")
		plistPath := filepath.Join(appPath, filepath.FromSlash("Contents/Info.plist"))
		if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
			t.Fatal(err)
		}
		plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.example.knowledge</string>
<key>CFBundleName</key><string>Knowledge</string>
<key>LSApplicationCategoryType</key><string>public.app-category.productivity</string>
</dict></plist>`
		if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
		catalog := scanTestCatalog()
		catalog.Apps = []model.Application{{
			ID: "knowledge", Name: "Knowledge", Type: model.ApplicationTypeBundle,
			InstallPath: appPath, Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""),
		}}
		store := testStore(directory)
		saveTestConfig(t, store, catalog)
		expected, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		proposed := expected
		proposed.Apps = cloneApplications(expected.Apps)
		proposed.Apps[0].ScanManaged = true
		if err := (&Service{config: store}).SaveScanSnapshot(expected, proposed); err != nil {
			t.Fatal(err)
		}
		saved, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if !saved.Apps[0].ScanManaged || len(saved.Settings.Scan.BundleID) != 1 || saved.Settings.Scan.BundleID[0] != "com.example.knowledge" {
			t.Fatalf("managed transition and Bundle ID were not saved together: %#v", saved)
		}
	})
	t.Run("save-scan-snapshot-rejects-stale-base", func(t *testing.T) {
		directory := t.TempDir()
		store := testStore(directory)
		expectedCatalog := scanTestCatalog()
		expectedCatalog.Settings.Downloader.CLI = "aria2c"
		expectedCatalog = saveTestConfig(t, store, expectedCatalog)
		changedLanguage := "en"
		if expectedCatalog.Settings.Language == changedLanguage {
			changedLanguage = "zh"
		}
		current := expectedCatalog
		current.Settings.Language = changedLanguage
		current = saveTestConfig(t, store, current)
		proposed := expectedCatalog
		err := (&Service{config: store}).SaveScanSnapshot(expectedCatalog, proposed)
		if err == nil {
			t.Fatal("stale scan snapshot was saved")
		}
		saved, loadErr := store.Load()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if saved.Settings.Language != changedLanguage {
			t.Fatalf("stale save overwrote current catalog: %#v", saved.Settings)
		}
	})
	t.Run("save-scan-snapshot-persists-matching-base", func(t *testing.T) {
		directory := t.TempDir()
		store := testStore(directory)
		expectedCatalog := scanTestCatalog()
		expectedCatalog.Settings.Downloader.CLI = "aria2c"
		expectedCatalog = saveTestConfig(t, store, expectedCatalog)
		changedLanguage := "en"
		if expectedCatalog.Settings.Language == changedLanguage {
			changedLanguage = "zh"
		}
		proposedCatalog := expectedCatalog
		proposedCatalog.Settings.Language = changedLanguage
		if err := (&Service{config: store}).SaveScanSnapshot(expectedCatalog, proposedCatalog); err != nil {
			t.Fatal(err)
		}
		savedCatalog, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if savedCatalog.Settings.Language != changedLanguage {
			t.Fatalf("scan snapshot was not persisted: %#v", savedCatalog.Settings)
		}
	})
	t.Run("cargoscan-candidate-can-be-accepted-and-saved-disabled-with-current-version", func(t *testing.T) {
		directory := t.TempDir()
		bin := filepath.Join(directory, "bin")
		root := filepath.Join(directory, "cargo-root")
		binary := filepath.Join(root, "bin", "sample")
		realBinary := filepath.Join(root, "bin", "sample-real")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realBinary, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realBinary, binary); err != nil {
			t.Fatal(err)
		}
		cargo := filepath.Join(bin, "cargo")
		if err := os.WriteFile(cargo, []byte("#!/bin/sh\nif [ \"$1 $2\" = \"install --list\" ]; then printf 'sample v1.2.3:\\n    sample\\n'; exit 0; fi\nexit 91\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		t.Setenv("CARGO_INSTALL_ROOT", root)
		t.Setenv("CARGO_HOME", filepath.Join(directory, "cargo-home"))
		store := testStore(directory)
		catalog := scanTestCatalog()
		catalog.Settings.LogDir = filepath.Join(directory, "logs")
		catalog.Settings.Scan.Path = false
		catalog.Settings.Scan.Application = false
		catalog.Settings.Scan.Packages = model.PackageScanSettings{Cargo: true}
		catalog.Apps = nil
		saveTestConfig(t, store, catalog)
		service := &Service{config: store, GitHubResolver: noGitHubResolver{}}
		preview, err := service.PreviewScan(context.Background(), ScanObserver{})
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Config.Apps) != 1 {
			t.Fatalf("Cargo preview apps=%#v", preview.Config.Apps)
		}
		candidate := preview.Config.Apps[0]
		if candidate.Provider.Type != model.ProviderCargo || candidate.Enabled || candidate.UpdateMode != model.ModeCheck || candidate.StatusManaged.CurrentVersion != "1.2.3" {
			t.Fatalf("Cargo candidate=%#v", candidate)
		}
		if err := service.SaveScanSnapshot(preview.BaseConfig, preview.Config); err != nil {
			t.Fatalf("SaveScanSnapshot() error=%v", err)
		}
		saved, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(saved.Apps) != 1 || saved.Apps[0].Enabled || saved.Apps[0].StatusManaged.CurrentVersion != "1.2.3" {
			t.Fatalf("saved Cargo candidate=%#v", saved.Apps)
		}
		savedApp := saved.Apps[0]
		canonicalBinary, err := filepath.EvalSymlinks(realBinary)
		if err != nil {
			t.Fatal(err)
		}
		if savedApp.InstallPath != canonicalBinary {
			t.Fatalf("saved Cargo install path=%q want=%q", savedApp.InstallPath, canonicalBinary)
		}
		if savedApp.Environment["CARGO_INSTALL_ROOT"] != root || !strings.HasPrefix(savedApp.Environment["PATH"], bin) || len(savedApp.Environment) != 2 {
			t.Fatalf("saved Cargo environment=%#v", savedApp.Environment)
		}
		registry := providerpkg.NewRegistry()
		if err := providerpkg.RegisterBuiltins(registry, nil, nil, runtimeutil.Runner{}); err != nil {
			t.Fatal(err)
		}
		capabilities, ok := registry.Resolve(string(model.ProviderCargo))
		if !ok || capabilities.Current == nil {
			t.Fatalf("Cargo capabilities=%#v", capabilities)
		}
		current, err := capabilities.Current.Current(context.Background(), providerpkg.Request{App: savedApp})
		if err != nil || current != "1.2.3" {
			t.Fatalf("saved Cargo Current=%q error=%v", current, err)
		}
	})
	t.Run("save-scan-snapshot-merges-concurrent-status", func(t *testing.T) {
		directory := t.TempDir()
		store := testStore(directory)
		expected := config.Default()
		expected.Settings.Downloader.CLI = "aria2c"
		expected.Apps = []model.Application{{ID: "managed", Name: "Managed", Type: model.ApplicationTypeCLI, InstallPath: "managed", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), ScanManaged: true, StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
		expected = saveTestConfig(t, store, expected)
		concurrent := expected
		concurrent.Apps = cloneApplications(expected.Apps)
		concurrent.Apps[0].StatusManaged = model.ManagedStatus{CurrentVersion: "1.0.0", UpdateStatus: model.StatusCurrent}
		saveTestConfig(t, store, concurrent)
		proposed := expected
		proposed.Apps = cloneApplications(expected.Apps)
		proposed.Apps[0].Description = "scan candidate"
		if err := (&Service{config: store}).SaveScanSnapshot(expected, proposed); err != nil {
			t.Fatalf("scan save rejected non-conflicting status refresh: %v", err)
		}
		saved, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Apps[0].Description != "scan candidate" || saved.Apps[0].StatusManaged.CurrentVersion != "1.0.0" {
			t.Fatalf("scan save did not preserve candidate and status: %#v", saved.Apps[0])
		}
	})
	t.Run("save-scan-snapshot-rejects-concurrent-application-configuration", func(t *testing.T) {
		directory := t.TempDir()
		store := testStore(directory)
		expected := config.Default()
		expected.Apps = []model.Application{{ID: "managed", Name: "Managed", Type: model.ApplicationTypeCLI, InstallPath: "managed", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), ScanManaged: true, StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
		expected = saveTestConfig(t, store, expected)
		concurrent := expected
		concurrent.Apps = cloneApplications(expected.Apps)
		concurrent.Apps[0].Description = "concurrent edit"
		saveTestConfig(t, store, concurrent)
		proposed := expected
		proposed.Apps = cloneApplications(expected.Apps)
		proposed.Apps[0].Description = "scan candidate"
		if err := (&Service{config: store}).SaveScanSnapshot(expected, proposed); err == nil {
			t.Fatal("scan save accepted concurrent application configuration")
		}
		saved, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Apps[0].Description != "concurrent edit" {
			t.Fatalf("scan save overwrote concurrent application configuration: %#v", saved.Apps[0])
		}
	})
	t.Run("scan-changes-use-provider-config-field-paths", func(t *testing.T) {
		current := model.Application{Provider: providerConfig(model.ProviderDefault, "current-version", "current-check", "current-update", &model.Download{URL: "https://example.test/current"}, "current-install")}
		proposed := model.Application{Provider: providerConfig(model.ProviderGitHubRelease, "proposed-version", "proposed-check", "proposed-update", &model.Download{URL: "https://example.test/proposed"}, "proposed-install")}

		changes := changedApplicationFields(current, proposed)
		byField := make(map[string]model.ScanFieldChange, len(changes))
		for _, change := range changes {
			byField[change.Field] = change
		}
		for _, field := range []string{
			"provider.type", "provider.actions.version", "provider.actions.check", "provider.actions.update", "provider.actions.download", "provider.actions.install",
		} {
			if _, found := byField[field]; !found {
				t.Fatalf("provider change field %q missing from %#v", field, changes)
			}
		}
	})
}
