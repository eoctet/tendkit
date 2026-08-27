package service

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
	providerpkg "github.com/eoctet/tendkit/internal/updater/provider"
	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestServiceDoesNotImportScannerHandler(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), `internal/scanner/handler`) {
			t.Fatalf("%s imports scanner implementation package", path)
		}
	}
}

func TestClosedBatchUsesLocalizedMessage(t *testing.T) {
	previous := i18n.Current()
	t.Cleanup(func() { i18n.Set(previous) })
	i18n.Set(i18n.Chinese)
	batch := NewBatch(RunOptions{})
	batch.close()
	if err := batch.Add(RunOptions{}); err == nil || err.Error() != "运行队列已关闭" {
		t.Fatalf("closed batch error = %v", err)
	}
}

func saveTestConfig(t *testing.T, store *config.Center, value model.Config) model.Config {
	t.Helper()
	value.Settings.Downloader.CLI = "aria2c"
	for index := range value.Apps {
		if value.Apps[index].StatusManaged.UpdateStatus == "" {
			value.Apps[index].StatusManaged.UpdateStatus = model.StatusUnchecked
		}
	}
	err := store.Initialize()
	if err == nil {
		var snapshot config.Snapshot
		snapshot, err = store.Snapshot()
		if err == nil {
			err = store.Save(snapshot.Revision, value)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func testStatus(value model.Config, id string) model.ManagedStatus {
	for _, application := range value.Apps {
		if application.ID == id {
			return application.StatusManaged
		}
	}
	return model.ManagedStatus{}
}

func runServiceRequest(ctx context.Context, service *Service, options RunOptions) (model.Config, []model.Result, error) {
	return service.Run(ctx, options)
}

type noGitHubResolver struct{}

func (noGitHubResolver) Resolve(context.Context, string) (model.ProviderType, error) { return "", nil }

func TestServiceProductionDependsOnlyOnUpdaterFacade(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate service test")
	}
	assertFacadeImports(t, filepath.Dir(file), "github.com/eoctet/tendkit/internal/updater", map[string]bool{"New": true, "Options": true, "Updater": true, "RunOptions": true, "PreflightDownloadAssetCandidates": true})
}

func TestServicePublicAPIUsesModelScanProgress(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate service test")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "service.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "scanner.Progress") {
		t.Fatal("Service public API leaks scanner.Progress")
	}
}

func TestDownloadAssetCandidatesFiltersTargetsRejectsUnknownAndHasNoRunSideEffects(t *testing.T) {
	if _, err := runtimeutil.DetectSystemInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path == "/go" {
			_, _ = writer.Write([]byte(`[{"version":"go1.2.3","stable":true,"files":[{"filename":"go1.2.3.` + runtime.GOOS + `-` + runtime.GOARCH + `.tar.gz","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `","kind":"archive"},{"filename":"go1.2.3.` + runtime.GOOS + `-` + runtime.GOARCH + `.zip","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `","kind":"archive"}]}]`))
			return
		}
		_, _ = writer.Write([]byte(`{"assets":[{"name":"tool-darwin-arm64.dmg","browser_download_url":"https://github.com/acme/tool.dmg"}]}`))
	}))
	defer server.Close()
	directory := t.TempDir()
	store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "run-logs")
	catalog.Settings.ProviderURLs[string(model.ProviderGitHubRelease)] = server.URL + "/{package}"
	catalog.Settings.ProviderURLs[string(model.ProviderGo)] = server.URL + "/go"
	catalog.Apps = []model.Application{
		{ID: "download", Name: "Download", Type: model.ApplicationTypeCLI, InstallPath: "/tool/download", Enabled: true, UpdateMode: model.ModeDownload, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Package: "acme/download", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
		{ID: "check", Name: "Check", Type: model.ApplicationTypeCLI, InstallPath: "/tool/check", Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Package: "acme/check", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
		{ID: "action", Name: "Action", Type: model.ApplicationTypeCLI, InstallPath: "/tool/action", Enabled: true, UpdateMode: model.ModeDownload, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease, Actions: &model.ProviderActions{Download: &model.Download{URL: "https://example.invalid/fixed.dmg", Filename: "fixed.dmg"}}}, Package: "acme/action", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
		{ID: "go", Name: "Go", Type: model.ApplicationTypeCLI, InstallPath: "/tool/go", Enabled: true, UpdateMode: model.ModeDownload, Provider: providerConfig(model.ProviderGo, "printf '1.0.0\\n'", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
	}
	saveTestConfig(t, store, catalog)
	service := &Service{config: store}
	if _, _, err := service.DownloadAssetCandidates(context.Background(), []string{"missing"}, nil); err == nil || requests != 0 {
		t.Fatalf("unknown target err=%v requests=%d", err, requests)
	}
	choices, failures, err := service.DownloadAssetCandidates(context.Background(), []string{"check", "action"}, nil)
	if err != nil || len(choices) != 0 || len(failures) != 0 || requests != 0 {
		t.Fatalf("filtered choices=%#v failures=%#v err=%v requests=%d", choices, failures, err, requests)
	}
	if _, err := os.Stat(catalog.Settings.LogDir); !os.IsNotExist(err) {
		t.Fatalf("preflight created run log directory: %v", err)
	}
	var progress []model.DownloadAssetPreflightProgress
	choices, failures, err = service.DownloadAssetCandidates(context.Background(), []string{"go"}, func(event model.DownloadAssetPreflightProgress) {
		progress = append(progress, event)
	})
	if err != nil || len(failures) != 0 || len(choices["go"].Candidates) != 2 || requests != 2 {
		t.Fatalf("Go choices=%#v failures=%#v err=%v requests=%d", choices, failures, err, requests)
	}
	if len(progress) != 2 || progress[0].Stage != model.DownloadAssetPreflightStarted || progress[1].Stage != model.DownloadAssetPreflightCompleted || progress[1].AppID != "go" {
		t.Fatalf("Go preflight progress=%#v", progress)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := service.DownloadAssetCandidates(cancelled, []string{"download"}, nil); !errors.Is(err, context.Canceled) || requests != 2 {
		t.Fatalf("cancel err=%v requests=%d", err, requests)
	}
}

func TestMergeCatalogEditAppliesCompleteApplicationEditsAndPreservesExternalApps(t *testing.T) {
	current := model.Config{Settings: model.Settings{Workers: 2}, Apps: []model.Application{
		{ID: "go", Name: "Go", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderGo, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
		{ID: "external", Name: "Externally scanned", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusCurrent}},
	}}
	proposed := model.Config{Settings: model.Settings{Workers: 8}, Apps: []model.Application{{ID: "go", Name: "Go toolchain", Enabled: false, UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderDefault, "", "", "go install example.invalid/go@latest", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}}
	merged := mergeCatalogEdit(current, proposed)
	if merged.Settings.Workers != 8 || merged.Apps[0].Name != "Go toolchain" || merged.Apps[0].Enabled || merged.Apps[0].Provider.UpdateAction() == "" {
		t.Fatalf("catalog edit was not merged: %#v", merged)
	}
	if len(merged.Apps) != 2 || merged.Apps[1].ID != "external" || merged.Apps[1].StatusManaged.UpdateStatus != model.StatusCurrent {
		t.Fatalf("external application was overwritten: %#v", merged.Apps)
	}
}

func assertFacadeImports(t *testing.T, directory, forbiddenImport string, allowed map[string]bool) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		aliases := map[string]bool{}
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, "\"")
			if strings.HasPrefix(path, forbiddenImport+"/") {
				t.Errorf("%s imports updater implementation package %s", filepath.Base(file), path)
				continue
			}
			if path != forbiddenImport {
				continue
			}
			name := "updater"
			if imported.Name != nil {
				name = imported.Name.Name
			}
			aliases[name] = true
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if ok && aliases[ident.Name] && !allowed[selector.Sel.Name] {
				t.Errorf("%s references non-facade updater symbol %s", filepath.Base(file), selector.Sel.Name)
			}
			return true
		})
	}
}

func TestRunBatchSharesOneLockAndPersistsDynamicWorkTogether(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.Workers = 2
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Apps = []model.Application{
		{ID: "first", Name: "First", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "sleep 0.2; printf '1.0.0\\n'", "", nil, "")},
		{ID: "second", Name: "Second", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'", "", nil, "")},
	}
	saveTestConfig(t, store, catalog)
	started := make(chan string, 2)
	service := &Service{config: store}
	batch := NewBatch(RunOptions{Names: []string{"first"}, CheckOnly: true})
	type outcome struct {
		config  model.Config
		results []model.Result
		err     error
	}
	finished := make(chan outcome, 1)
	go func() {
		updated, results, err := service.RunBatch(context.Background(), RunOptions{
			Names:     []string{"first"},
			CheckOnly: true,
			Observer:  RunObserver{AppStart: func(result model.Result) { started <- result.AppID }},
		}, batch)
		finished <- outcome{config: updated, results: results, err: err}
	}()
	if id := <-started; id != "first" {
		t.Fatalf("first worker started %q", id)
	}
	contender := testStore(directory)
	if err := contender.WithLock(func() error { return nil }); err == nil {
		t.Fatal("batch released the state lock while work was active")
	}
	if err := batch.Add(RunOptions{Names: []string{"second"}, CheckOnly: true}); err != nil {
		t.Fatal(err)
	}
	if id := <-started; id != "second" {
		t.Fatalf("dynamic worker started %q", id)
	}
	result := <-finished
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.results) != 2 || testStatus(result.config, "first").UpdateStatus != "current" || testStatus(result.config, "second").UpdateStatus != "current" {
		t.Fatalf("batch result = %#v %#v", result.results, result.config.Apps)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if testStatus(saved, "first").UpdateStatus != "current" || testStatus(saved, "second").UpdateStatus != "current" {
		t.Fatalf("dynamic batch was not persisted atomically: %#v", saved.Apps)
	}
	data, err := os.ReadFile(filepath.Join(catalog.Settings.LogDir, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"event":"run_started"`) != 1 || strings.Count(string(data), `"event":"run_finished"`) != 1 {
		t.Fatalf("dynamic work created multiple service transactions: %s", data)
	}
}

func TestBatchAddedBeforeBindRunsWithInitialRequest(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Apps = []model.Application{
		{ID: "first", Name: "First", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'", "", nil, "")},
		{ID: "second", Name: "Second", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'", "", nil, "")},
	}
	saveTestConfig(t, store, catalog)
	batch := NewBatch(RunOptions{Names: []string{"first"}, CheckOnly: true})
	if err := batch.Add(RunOptions{Names: []string{"second"}, CheckOnly: true}); err != nil {
		t.Fatal(err)
	}
	_, results, err := (&Service{config: store}).RunBatch(context.Background(), RunOptions{Names: []string{"first"}, CheckOnly: true}, batch)
	if err != nil || len(results) != 2 {
		t.Fatalf("pre-bound batch results=%#v err=%v", results, err)
	}
}

func TestBatchRejectsAddAfterPrepareFailure(t *testing.T) {
	store := testStore(t.TempDir())
	batch := NewBatch(RunOptions{Names: []string{"missing"}, CheckOnly: true})
	if _, _, err := (&Service{config: store}).RunBatch(context.Background(), RunOptions{Names: []string{"missing"}, CheckOnly: true}, batch); err == nil {
		t.Fatal("expected prepare failure")
	}
	if err := batch.Add(RunOptions{Names: []string{"later"}, CheckOnly: true}); err == nil {
		t.Fatal("closed batch accepted an addition after prepare failure")
	}
}

func TestRunRoutesCommandStdoutAndStderrSeparately(t *testing.T) {
	directory := t.TempDir()
	installedPath := filepath.Join(directory, "installed")
	if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeCheck,
		Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'; printf 'provider warning\\n' >&2", "", nil, ""),
	}}
	saveTestConfig(t, store, catalog)
	var stdout, stderr bytes.Buffer
	if _, _, err := runServiceRequest(context.Background(), &Service{config: store}, RunOptions{CheckOnly: true, CommandOutput: func(output model.CommandOutput) {
		if output.Stream == "stderr" {
			_, _ = stderr.Write(output.Data)
			return
		}
		_, _ = stdout.Write(output.Data)
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "1.0.0") || strings.Contains(stdout.String(), "provider warning") {
		t.Fatalf("stdout was not isolated: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "provider warning") {
		t.Fatalf("stderr was not isolated: %q", stderr.String())
	}
}

func TestPreviewScanPersistsExistingStateWithoutPersistingCandidates(t *testing.T) {
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
}

func TestPreviewScanLogsPersistenceFailure(t *testing.T) {
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
}

func TestPreviewScanLogsCancellation(t *testing.T) {
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
}

func TestPreviewScanKeepsExcludedConfiguredBuiltInPathApplicationAfterRestart(t *testing.T) {
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
}

func TestPreviewApplicationScanDoesNotRunUnrelatedApplications(t *testing.T) {
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
}

func TestSaveScanSnapshotRegistersBundleIDWhenAppBecomesManaged(t *testing.T) {
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
}

func TestSaveScanSnapshotRejectsStaleBase(t *testing.T) {
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
}

func TestSaveScanSnapshotPersistsMatchingBase(t *testing.T) {
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
}

func TestCargoScanCandidateCanBeAcceptedAndSavedDisabledWithCurrentVersion(t *testing.T) {
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
}

func TestSaveScanSnapshotMergesConcurrentStatus(t *testing.T) {
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
}

func TestSaveScanSnapshotRejectsConcurrentApplicationConfiguration(t *testing.T) {
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
}

func TestScanChangesUseProviderConfigFieldPaths(t *testing.T) {
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
}

func TestCloneApplicationDeepCopiesProviderActions(t *testing.T) {
	original := model.Application{
		Provider: providerConfig(model.ProviderDefault, "version", "check", "update", &model.Download{URL: "https://example.test/artifact", ExtraArgs: []string{"--header=X"}}, "install"),
	}
	cloned := cloneApplication(original)
	original.Provider.Actions.Version = "changed-version"
	original.Provider.Actions.Download.URL = "https://example.test/changed"
	original.Provider.Actions.Download.ExtraArgs[0] = "--header=Y"

	if cloned.Provider.Actions == original.Provider.Actions || cloned.Provider.Actions.Download == original.Provider.Actions.Download {
		t.Fatal("provider actions were not copied")
	}
	if got := cloned.Provider.VersionAction(); got != "version" {
		t.Fatalf("cloned version action = %q, want version", got)
	}
	download := cloned.Provider.DownloadAction()
	if download.URL != "https://example.test/artifact" || download.ExtraArgs[0] != "--header=X" {
		t.Fatalf("cloned download action changed with source: %#v", download)
	}
}

func TestRunBatchMergesConcurrentScanKeep(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Apps = []model.Application{{
		ID: "managed", Name: "Managed", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck,
		Provider:      providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "sleep 0.2; printf '1.0.0\\n'", "", nil, ""),
		StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	saveTestConfig(t, store, catalog)
	if _, err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := store.AcquireProcessLock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ReleaseProcessLock() })
	started := make(chan struct{}, 1)
	finished := make(chan error, 1)
	service := &Service{config: store}
	go func() {
		_, _, err := runServiceRequest(context.Background(), service, RunOptions{
			CheckOnly: true,
			Observer:  RunObserver{AppStart: func(model.Result) { started <- struct{}{} }},
		})
		finished <- err
	}()
	<-started
	latest, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	latest.Config.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"managed": {"description": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-17T00:00:00+08:00"}}}
	if err := store.Save(latest.Revision, latest.Config); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("run rejected concurrent scan keep: %v", err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Apps[0].StatusManaged.UpdateStatus != model.StatusCurrent || saved.ScanVersionControl["managed"]["description"].Fingerprint == "" {
		t.Fatalf("run did not preserve runtime state and scan keep: %#v", saved)
	}
}

func TestRunCancellationDoesNotPersistPartialState(t *testing.T) {
	directory := t.TempDir()
	installedPath := filepath.Join(directory, "installed")
	if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Apps = []model.Application{{
		ID: "slow", Name: "Slow", Type: model.ApplicationTypeCLI, InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeCheck,
		Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "sleep 5; printf '2.0.0\\n'", "", nil, ""),
	}}
	catalog.Apps[0].StatusManaged = model.ManagedStatus{CurrentVersion: "0.9.0", UpdateStatus: model.StatusUnchecked}
	saveTestConfig(t, store, catalog)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := runServiceRequest(ctx, &Service{config: store}, RunOptions{CheckOnly: true})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if testStatus(saved, "slow").UpdateStatus != model.StatusUnchecked {
		t.Fatalf("cancellation persisted partial state: %#v", saved)
	}
	runLog, readErr := os.ReadFile(filepath.Join(catalog.Settings.LogDir, "run.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	content := string(runLog)
	for _, expected := range []string{`"event":"run_started"`, `"event":"run_cancelled"`, `"level":"WARN"`, `"run_id":"`} {
		if !strings.Contains(content, expected) {
			t.Fatalf("cancellation log missing %s: %s", expected, content)
		}
	}
}

func TestRunRejectsAnyUnknownName(t *testing.T) {
	directory := t.TempDir()
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Apps = []model.Application{{ID: "go", Name: "Go", Type: model.ApplicationTypeCLI, InstallPath: "go", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, "")}}
	saveTestConfig(t, store, catalog)
	_, _, err := runServiceRequest(context.Background(), &Service{config: store}, RunOptions{Names: []string{"go", "missing"}, CheckOnly: true})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected unknown-name error, got %v", err)
	}
}

func TestRunUsesBuiltinUpdaterFacade(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Apps = []model.Application{{
		ID: "custom", Name: "Custom", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck,
		Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'", "", nil, ""),
	}}
	saveTestConfig(t, store, catalog)
	service := &Service{config: store}
	if _, results, err := runServiceRequest(context.Background(), service, RunOptions{CheckOnly: true}); err != nil || len(results) != 1 || results[0].Status != model.StatusCurrent {
		t.Fatalf("built-in updater facade did not run: results=%#v err=%v", results, err)
	}
}

func TestRunConfiguresCheckerDownloadDirectory(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Settings.Downloader.StorePath = filepath.Join(directory, "downloads with spaces")
	catalog.Apps = []model.Application{{
		ID: "custom", Name: "Custom", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck,
		Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "test {download_dir} = "+runtimeutil.QuoteShell(catalog.Settings.Downloader.StorePath)+" && printf '1.0.0\\n'", "", nil, ""),
	}}
	saveTestConfig(t, store, catalog)
	service := &Service{config: store}
	_, results, err := runServiceRequest(context.Background(), service, RunOptions{CheckOnly: true})
	if err != nil || len(results) != 1 || results[0].Status != model.StatusCurrent {
		t.Fatalf("check did not receive configured download directory: results=%#v err=%v", results, err)
	}
}

func TestLoadDoesNotCreateMissingCatalog(t *testing.T) {
	directory := t.TempDir()
	store := testStore(directory)
	_, _, err := (&Service{config: store}).Load()
	if err == nil {
		t.Fatal("expected missing catalog error")
	}
	if _, statErr := os.Stat(testConfigPath(directory)); !os.IsNotExist(statErr) {
		t.Fatalf("load unexpectedly created config: %v", statErr)
	}
}

func TestInitializeRejectsInvalidExistingFiles(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		directory := t.TempDir()
		store := testStore(directory)
		if err := os.WriteFile(testConfigPath(directory), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := (&Service{config: store}).Initialize(); err == nil {
			t.Fatal("expected invalid existing config error")
		}
	})
}

func TestInitializeHoldsStoreLock(t *testing.T) {
	directory := t.TempDir()
	store := testStore(directory)
	contender := testStore(directory)
	err := store.WithLock(func() error {
		return (&Service{config: contender}).Initialize()
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "already") && !strings.Contains(err.Error(), "运行") {
		t.Fatalf("initialize did not contend for the store lock: %v", err)
	}
	if _, statErr := os.Stat(testConfigPath(directory)); !os.IsNotExist(statErr) {
		t.Fatalf("contended initialize created config: %v", statErr)
	}
}

func TestInitializePreservesExistingUnifiedConfig(t *testing.T) {
	directory := t.TempDir()
	store := testStore(directory)
	originalCatalog := config.Default()
	originalCatalog.Settings.Language = "en"
	saveTestConfig(t, store, originalCatalog)
	configData, err := os.ReadFile(testConfigPath(directory))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Service{config: store}).Initialize(); err != nil {
		t.Fatal(err)
	}
	recoveredConfig, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recoveredConfig.Settings.Language != "en" {
		t.Fatalf("initialize overwrote existing config: %#v", recoveredConfig.Settings)
	}
	if after, err := os.ReadFile(testConfigPath(directory)); err != nil || !bytes.Equal(after, configData) {
		t.Fatalf("initialize changed existing config: %v", err)
	}
}

func TestRunLogWriteFailureDoesNotInterruptOrDiscardOperationState(t *testing.T) {
	directory := t.TempDir()
	installedPath := filepath.Join(directory, "installed")
	if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	logDirectory := filepath.Join(directory, "logs")
	if err := os.MkdirAll(filepath.Join(logDirectory, "run.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := testStore(directory)
	catalog := config.Default()
	catalog.Settings.LogDir = logDirectory
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeCheck,
		Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'", "", nil, ""),
	}}
	catalog.Apps[0].StatusManaged = model.ManagedStatus{UpdateStatus: "unchecked"}
	saveTestConfig(t, store, catalog)
	_, _, err := runServiceRequest(context.Background(), &Service{config: store}, RunOptions{CheckOnly: true})
	if err != nil {
		t.Fatalf("run was interrupted by logging failure: %v", err)
	}
	saved, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if testStatus(saved, "sample").UpdateStatus == "unchecked" {
		t.Fatalf("operation state was discarded after logging failure: %#v", saved)
	}
}

func TestRunLogsFailureInsteadOfFinishedWhenConfigPersistenceFails(t *testing.T) {
	directory := t.TempDir()
	configDirectory := filepath.Join(directory, "config")
	if err := os.Mkdir(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	store := config.New(filepath.Join(configDirectory, "config.json"), filepath.Join(directory, "config.lock"))
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(directory, "logs")
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck,
		Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'", "", nil, ""),
	}}
	saveTestConfig(t, store, catalog)
	if err := os.Chmod(configDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(configDirectory, 0o700) }()
	if _, _, err := runServiceRequest(context.Background(), &Service{config: store}, RunOptions{CheckOnly: true}); err == nil {
		t.Fatal("expected config persistence failure")
	}
	data, err := os.ReadFile(filepath.Join(catalog.Settings.LogDir, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, `"event":"run_finished"`) || !strings.Contains(content, `"event":"run_failed"`) {
		t.Fatalf("persistence operation order is inconsistent: %s", content)
	}
}

func scanTestCatalog() model.Config {
	catalog := config.Default()
	disabled := false
	catalog.Settings.Scan.Application = disabled
	catalog.Settings.Scan.Packages = model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled}
	catalog.Settings.Scan.Exclude = nil
	return catalog
}

func testStore(directory string) *config.Center {
	return config.New(testConfigPath(directory), filepath.Join(directory, "config.lock"))
}

func testConfigPath(directory string) string { return filepath.Join(directory, "config.json") }
