package service

import (
	"strings"

	"errors"
	"time"

	"bytes"
	"github.com/eoctet/tendkit/internal/model"

	"github.com/eoctet/tendkit/pkg/i18n"
	"testing"

	"context"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"path/filepath"

	"github.com/eoctet/tendkit/internal/config"

	"os"
)

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

func providerConfig(kind model.ProviderType, version, check, update string, download *model.Download, install string) model.ProviderConfig {
	if kind == model.ProviderDefault && version == "" && (check != "" || update != "" || download != nil || install != "") {
		version = "version"
	}
	provider := model.ProviderConfig{Type: kind}
	if version != "" || check != "" || update != "" || download != nil || install != "" {
		provider.Actions = &model.ProviderActions{Version: version, Check: check, Update: update, Download: download, Install: install}
	}
	return provider
}

type noGitHubResolver struct{}

func (noGitHubResolver) Resolve(context.Context, string) (model.ProviderType, error) { return "", nil }

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
func TestServiceBatchTransaction(t *testing.T) {
	t.Run("download-preflight-filters-non-download-actions-without-run-side-effects", func(t *testing.T) {
		directory := t.TempDir()
		store := testStore(directory)
		catalog := config.Default()
		catalog.Settings.LogDir = filepath.Join(directory, "run-logs")
		catalog.Apps = []model.Application{
			{ID: "check", Name: "Check", Type: model.ApplicationTypeCLI, InstallPath: "/tool/check", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "printf '1.0.0'", "printf '1.0.0'", "", nil, "")},
			{ID: "action", Name: "Action", Type: model.ApplicationTypeCLI, InstallPath: "/tool/action", Enabled: true, UpdateMode: model.ModeDownload, Provider: providerConfig(model.ProviderDefault, "printf '1.0.0'", "printf '1.0.0'", "", &model.Download{URL: "https://example.invalid/fixed.dmg", Filename: "fixed.dmg"}, "")},
		}
		saveTestConfig(t, store, catalog)
		service := &Service{config: store}
		if _, _, err := service.DownloadAssetCandidates(context.Background(), []string{"missing"}, nil); err == nil {
			t.Fatal("unknown preflight target accepted")
		}
		choices, failures, err := service.DownloadAssetCandidates(context.Background(), []string{"check", "action"}, nil)
		if err != nil || len(choices) != 0 || len(failures) != 0 {
			t.Fatalf("filtered preflight choices=%#v failures=%#v err=%v", choices, failures, err)
		}
		if _, err := os.Stat(catalog.Settings.LogDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preflight created run log directory: %v", err)
		}
	})
	t.Run("configuration-edits-merge-with-external-state-and-initialization-holds-lock", func(t *testing.T) {
		current := model.Config{Settings: model.Settings{Workers: 2}, Apps: []model.Application{
			{ID: "go", Name: "Go", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderGo, "", "", "", nil, "")},
			{ID: "external", Name: "Externally scanned", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusCurrent}},
		}}
		proposed := model.Config{Settings: model.Settings{Workers: 8}, Apps: []model.Application{{ID: "go", Name: "Go toolchain", Enabled: false, UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderDefault, "", "", "go install example.invalid/go@latest", nil, "")}}}
		merged := mergeCatalogEdit(current, proposed)
		if merged.Settings.Workers != 8 || len(merged.Apps) != 2 || merged.Apps[0].Provider.UpdateAction() == "" || merged.Apps[1].StatusManaged.UpdateStatus != model.StatusCurrent {
			t.Fatalf("catalog edit merge=%#v", merged)
		}
		directory := t.TempDir()
		store, contender := testStore(directory), testStore(directory)
		err := store.WithLock(func() error { return (&Service{config: contender}).Initialize() })
		if err == nil || (!strings.Contains(strings.ToLower(err.Error()), "already") && !strings.Contains(err.Error(), "运行")) {
			t.Fatalf("initialize lock error=%v", err)
		}
		if _, err := os.Stat(testConfigPath(directory)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("contended initialization wrote config: %v", err)
		}
	})
	t.Run("closed-batch-uses-localized-message", func(t *testing.T) {
		previous := i18n.Current()
		t.Cleanup(func() { i18n.Set(previous) })
		i18n.Set(i18n.Chinese)
		batch := NewBatch(RunOptions{})
		batch.close()
		if err := batch.Add(RunOptions{}); err == nil || err.Error() != "运行队列已关闭" {
			t.Fatalf("closed batch error = %v", err)
		}
	})
	t.Run("run-batch-shares-one-lock-and-persists-dynamic-work-together", func(t *testing.T) {
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
	})
	t.Run("batch-added-before-bind-runs-with-initial-request", func(t *testing.T) {
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
	})
	t.Run("batch-rejects-add-after-prepare-failure", func(t *testing.T) {
		store := testStore(t.TempDir())
		batch := NewBatch(RunOptions{Names: []string{"missing"}, CheckOnly: true})
		if _, _, err := (&Service{config: store}).RunBatch(context.Background(), RunOptions{Names: []string{"missing"}, CheckOnly: true}, batch); err == nil {
			t.Fatal("expected prepare failure")
		}
		if err := batch.Add(RunOptions{Names: []string{"later"}, CheckOnly: true}); err == nil {
			t.Fatal("closed batch accepted an addition after prepare failure")
		}
	})
	t.Run("run-routes-command-stdout-and-stderr-separately", func(t *testing.T) {
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
	})
	t.Run("run-batch-merges-concurrent-scan-keep", func(t *testing.T) {
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
	})
	t.Run("run-cancellation-does-not-persist-partial-state", func(t *testing.T) {
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
	})
	t.Run("run-rejects-any-unknown-name", func(t *testing.T) {
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
	})
	t.Run("run-uses-builtin-updater-facade", func(t *testing.T) {
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
	})
	t.Run("run-configures-checker-download-directory", func(t *testing.T) {
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
	})
	t.Run("run-log-write-failure-does-not-interrupt-or-discard-operation-state", func(t *testing.T) {
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
	})
	t.Run("run-logs-failure-instead-of-finished-when-config-persistence-fails", func(t *testing.T) {
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
	})
}
