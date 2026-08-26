package main

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/service"
	"github.com/eoctet/tendkit/internal/ui"
	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestBuildMetadataDefaults(t *testing.T) {
	if programVersion != "dev" {
		t.Fatalf("programVersion = %q, want dev", programVersion)
	}
	if commitSHA != "unknown" {
		t.Fatalf("commitSHA = %q, want unknown", commitSHA)
	}
	if buildDate != "unknown" {
		t.Fatalf("buildDate = %q, want unknown", buildDate)
	}
}

func saveCommandTestConfig(t *testing.T, center *config.Center, catalog model.Config) model.Config {
	t.Helper()
	if err := center.Initialize(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := center.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := center.Save(snapshot.Revision, catalog); err != nil {
		t.Fatal(err)
	}
	saved, err := center.Load()
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestRunInteractiveTUIBuildsServiceCallbacks(t *testing.T) {
	directory := t.TempDir()
	store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
	saveCommandTestConfig(t, store, config.Default())
	previous := executeTUI
	t.Cleanup(func() { executeTUI = previous })
	called := false
	executeTUI = func(_ context.Context, _ *os.File, _ *os.File, actions ui.TUIActions, color ui.Mode) error {
		called = true
		if color != ui.ModeNever {
			t.Fatalf("color = %q", color)
		}
		catalog, _, err := actions.Load()
		if err != nil || catalog.SchemaVersion != model.SchemaVersion {
			t.Fatalf("Load() = %#v, %v", catalog, err)
		}
		if _, _, err := actions.Reload(); err != nil {
			t.Fatalf("Reload() = %v", err)
		}
		if actions.DownloadAssetCandidates == nil || actions.StartRun == nil {
			t.Fatal("download asset preflight or TUI run callback is not wired")
		}
		// An invalid target exercises the Service-owned preflight boundary
		// without making an HTTP request.
		if _, _, err := actions.DownloadAssetCandidates(context.Background(), ui.TUIRunRequest{Names: []string{"missing"}}, ui.TUIDownloadAssetObserver{}); err == nil {
			t.Fatal("download asset preflight accepted an unknown application")
		}
		return context.Canceled
	}
	if err := runInteractiveTUI(context.Background(), service.NewWithConfig(store), ui.ModeNever); err != nil {
		t.Fatalf("interactive TUI cancellation = %v", err)
	}
	if !called {
		t.Fatal("interactive TUI did not invoke the UI executor")
	}
}

func TestRequireSupportedHostRejectsUnsupportedSystem(t *testing.T) {
	previous := detectHostSystem
	detectHostSystem = func(context.Context) (runtimeutil.SystemInfo, error) {
		return runtimeutil.SystemInfo{OS: "windows", Architecture: "x86_64", FullName: "windows_unknown_unknown_x86_64"}, nil
	}
	t.Cleanup(func() { detectHostSystem = previous })
	if err := requireSupportedHost(context.Background()); err == nil {
		t.Fatal("unsupported host was accepted")
	}
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

func TestTUIDependsOnServiceNotUpdater(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate main test")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(file), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range files {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), source, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, "\"")
			if path == "github.com/eoctet/tendkit/internal/config" || strings.HasPrefix(path, "github.com/eoctet/tendkit/internal/config/") {
				t.Fatalf("%s imports config directly", filepath.Base(source))
			}
			if path == "github.com/eoctet/tendkit/internal/updater" || strings.HasPrefix(path, "github.com/eoctet/tendkit/internal/updater/") {
				t.Fatalf("%s imports updater directly", filepath.Base(source))
			}
			if path == "github.com/eoctet/tendkit/internal/scanner" || strings.HasPrefix(path, "github.com/eoctet/tendkit/internal/scanner/") {
				t.Fatalf("%s imports scanner directly", filepath.Base(source))
			}
		}
	}
}

func TestRunLoadsOnlyExplicitEnvFile(t *testing.T) {
	const (
		sharedKey   = "TENDKIT_CLI_ENV_SHARED_TEST"
		explicitKey = "TENDKIT_CLI_ENV_EXPLICIT_TEST"
		startupKey  = "TENDKIT_CLI_ENV_STARTUP_TEST"
		userKey     = "TENDKIT_CLI_ENV_USER_TEST"
		processKey  = "TENDKIT_CLI_ENV_PROCESS_TEST"
	)
	for _, key := range []string{sharedKey, explicitKey, startupKey, userKey, processKey} {
		_ = os.Unsetenv(key)
		key := key
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}
	t.Setenv(processKey, "process")

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	root := t.TempDir()
	startupDirectory := filepath.Join(root, "startup")
	userEnvDirectory := filepath.Join(root, ".config", "tendkit")
	if err := os.MkdirAll(startupDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userEnvDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	if err := os.Chdir(startupDirectory); err != nil {
		t.Fatal(err)
	}
	explicitPath := filepath.Join(root, "explicit.env")
	if err := os.WriteFile(explicitPath, []byte(sharedKey+"=explicit\n"+explicitKey+"=explicit\n"+processKey+"=explicit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(startupDirectory, ".env"), []byte(sharedKey+"=startup\n"+startupKey+"=startup\n"+processKey+"=startup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userEnvDirectory, ".env"), []byte(sharedKey+"=user\n"+userKey+"=user\n"+processKey+"=user\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"version", "--env-file", explicitPath}); code != 0 {
		t.Fatalf("run returned %d", code)
	}
	for key, want := range map[string]string{
		sharedKey: "explicit", explicitKey: "explicit", startupKey: "", userKey: "", processKey: "process",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

func TestRunSelectsStartupEnvFileBeforeUserEnvFile(t *testing.T) {
	const startupKey = "TENDKIT_CLI_DEFAULT_ENV_TEST"
	const userKey = "TENDKIT_CLI_USER_ENV_TEST"
	for _, key := range []string{startupKey, userKey} {
		_ = os.Unsetenv(key)
		key := key
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	temporaryDirectory := t.TempDir()
	userEnvDirectory := filepath.Join(temporaryDirectory, ".config", "tendkit")
	if err := os.MkdirAll(userEnvDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", temporaryDirectory)
	if err := os.WriteFile(filepath.Join(temporaryDirectory, ".env"), []byte(startupKey+"=loaded-by-startup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userEnvDirectory, ".env"), []byte(userKey+"=loaded-by-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"version"}); code != 0 {
		t.Fatalf("run returned %d", code)
	}
	if value := os.Getenv(startupKey); value != "loaded-by-startup" {
		t.Fatalf("startup value = %q", value)
	}
	if value := os.Getenv(userKey); value != "" {
		t.Fatalf("lower-priority user file was also loaded: %q", value)
	}
}

func TestRunFallsBackToUserEnvFile(t *testing.T) {
	const key = "TENDKIT_CLI_USER_ENV_FALLBACK_TEST"
	_ = os.Unsetenv(key)
	t.Cleanup(func() { _ = os.Unsetenv(key) })
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	home := t.TempDir()
	startupDirectory := filepath.Join(home, "startup")
	userEnvDirectory := filepath.Join(home, ".config", "tendkit")
	if err := os.MkdirAll(startupDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userEnvDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := os.Chdir(startupDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userEnvDirectory, ".env"), []byte(key+"=loaded-by-user-fallback\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"version"}); code != 0 {
		t.Fatalf("run returned %d", code)
	}
	if value := os.Getenv(key); value != "loaded-by-user-fallback" {
		t.Fatalf("fallback value = %q", value)
	}
}

func TestRunNoEnvFileDisablesDefaultLoad(t *testing.T) {
	const key = "TENDKIT_CLI_NO_ENV_TEST"
	_ = os.Unsetenv(key)
	t.Cleanup(func() { _ = os.Unsetenv(key) })
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	temporaryDirectory := t.TempDir()
	userEnvDirectory := filepath.Join(temporaryDirectory, ".config", "tendkit")
	if err := os.MkdirAll(userEnvDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", temporaryDirectory)
	if err := os.WriteFile(filepath.Join(temporaryDirectory, ".env"), []byte(key+"=unexpected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userEnvDirectory, ".env"), []byte(key+"=also-unexpected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"version", "--no-env-file"}); code != 0 {
		t.Fatalf("run returned %d", code)
	}
	if _, exists := os.LookupEnv(key); exists {
		t.Fatal("--no-env-file unexpectedly loaded the default file")
	}
}

func TestSaveTUICatalogClearsKeepsOnlyOnUnmanage(t *testing.T) {
	directory := t.TempDir()
	store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
	catalog := config.Default()
	catalog.Apps = []model.Application{{ID: "managed", Name: "Managed", Type: model.ApplicationTypeCLI, InstallPath: "managed", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), ScanManaged: true, StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}, {ID: "other", Name: "Other", Type: model.ApplicationTypeCLI, InstallPath: "other", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
	resolution := model.ScanKeepResolution{Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00+08:00"}
	catalog.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"managed": {"description": resolution}, "other": {"description": resolution}}
	catalog = saveCommandTestConfig(t, store, catalog)
	proposed := catalog
	proposed.Apps = append([]model.Application(nil), catalog.Apps...)
	proposed.Apps[0].ScanManaged = false
	if _, err := service.NewWithConfig(store).SaveConfig(catalog, proposed); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.ScanVersionControl["managed"] != nil || saved.ScanVersionControl["other"] == nil {
		t.Fatalf("unexpected keeps: %#v", saved.ScanVersionControl)
	}
	saved.ScanVersionControl["managed"] = map[string]model.ScanKeepResolution{"description": resolution}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(snapshot.Revision, saved); err != nil {
		t.Fatal(err)
	}
	proposed.Apps[0].ScanManaged = true
	if _, err := service.NewWithConfig(store).SaveConfig(saved, proposed); err != nil {
		t.Fatal(err)
	}
	saved, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.ScanVersionControl["managed"] == nil {
		t.Fatalf("manage changed keeps: %#v", saved.ScanVersionControl)
	}
}

func TestSaveTUICatalogStateBoundaries(t *testing.T) {
	newStore := func(t *testing.T, managed bool) (*config.Center, model.Config) {
		t.Helper()
		directory := t.TempDir()
		store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
		catalog := config.Default()
		catalog.Apps = []model.Application{{ID: "managed", Name: "Managed", Type: model.ApplicationTypeCLI, InstallPath: "managed", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), ScanManaged: managed, StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
		catalog = saveCommandTestConfig(t, store, catalog)
		return store, catalog
	}
	t.Run("manage preserves existing version control", func(t *testing.T) {
		store, catalog := newStore(t, false)
		catalog.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"managed": {"description": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00+08:00"}}}
		snapshot, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(snapshot.Revision, catalog); err != nil {
			t.Fatal(err)
		}
		proposed := catalog
		proposed.Apps = append([]model.Application(nil), catalog.Apps...)
		proposed.Apps[0].ScanManaged = true
		if _, err := service.NewWithConfig(store).SaveConfig(catalog, proposed); err != nil {
			t.Fatal(err)
		}
		saved, err := store.Load()
		if err != nil || !saved.Apps[0].ScanManaged {
			t.Fatalf("manage save failed: %#v %v", saved, err)
		}
		if saved.ScanVersionControl["managed"] == nil {
			t.Fatalf("manage unexpectedly cleared version control: %#v", saved.ScanVersionControl)
		}
	})
	t.Run("unmanage clears only its version control", func(t *testing.T) {
		store, catalog := newStore(t, true)
		catalog.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"managed": {"description": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00+08:00"}}}
		snapshot, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(snapshot.Revision, catalog); err != nil {
			t.Fatal(err)
		}
		proposed := catalog
		proposed.Apps = append([]model.Application(nil), catalog.Apps...)
		proposed.Apps[0].ScanManaged = false
		if _, err := service.NewWithConfig(store).SaveConfig(catalog, proposed); err != nil {
			t.Fatal(err)
		}
		saved, err := store.Load()
		if err != nil || saved.Apps[0].ScanManaged || saved.ScanVersionControl["managed"] != nil {
			t.Fatalf("unmanage did not clear config boundary: %#v %v", saved, err)
		}
	})
	t.Run("unmanage without keep saves catalog only", func(t *testing.T) {
		store, catalog := newStore(t, true)
		proposed := catalog
		proposed.Apps = append([]model.Application(nil), catalog.Apps...)
		proposed.Apps[0].ScanManaged = false
		if _, err := service.NewWithConfig(store).SaveConfig(catalog, proposed); err != nil {
			t.Fatal(err)
		}
		saved, err := store.Load()
		if err != nil || saved.Apps[0].ScanManaged {
			t.Fatalf("unmanage without keep failed: %#v %v", saved, err)
		}
	})
}

func TestSaveTUICatalogMergesConcurrentStatusAndScanKeep(t *testing.T) {
	directory := t.TempDir()
	store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
	expected := config.Default()
	expected.Apps = []model.Application{{ID: "managed", Name: "Managed", Type: model.ApplicationTypeCLI, InstallPath: "managed", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
	expected = saveCommandTestConfig(t, store, expected)
	concurrent := expected
	concurrent.Apps = append([]model.Application(nil), expected.Apps...)
	concurrent.Apps[0].StatusManaged = model.ManagedStatus{CurrentVersion: "1.0.0", UpdateStatus: model.StatusCurrent}
	concurrent.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"managed": {"description": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-17T00:00:00+08:00"}}}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(snapshot.Revision, concurrent); err != nil {
		t.Fatal(err)
	}
	changedLanguage := "en"
	if expected.Settings.Language == changedLanguage {
		changedLanguage = "zh"
	}
	proposed := expected
	proposed.Settings.Language = changedLanguage
	if _, err := service.NewWithConfig(store).SaveConfig(expected, proposed); err != nil {
		t.Fatalf("configuration save rejected non-conflicting state changes: %v", err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Settings.Language != changedLanguage || saved.Apps[0].StatusManaged.CurrentVersion != "1.0.0" || saved.ScanVersionControl["managed"]["description"].Fingerprint == "" {
		t.Fatalf("configuration save did not preserve all changes: %#v", saved)
	}
}

func TestSaveTUICatalogRejectsConcurrentConfiguration(t *testing.T) {
	directory := t.TempDir()
	store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
	expected := config.Default()
	expected.Apps = []model.Application{{ID: "managed", Name: "Managed", Type: model.ApplicationTypeCLI, InstallPath: "managed", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
	expected = saveCommandTestConfig(t, store, expected)
	changedLanguage := "en"
	if expected.Settings.Language == changedLanguage {
		changedLanguage = "zh"
	}
	concurrent := expected
	concurrent.Settings.Language = changedLanguage
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(snapshot.Revision, concurrent); err != nil {
		t.Fatal(err)
	}
	proposed := expected
	proposed.Apps = append([]model.Application(nil), expected.Apps...)
	proposed.Apps[0].Description = "editor change"
	if _, err := service.NewWithConfig(store).SaveConfig(expected, proposed); err == nil {
		t.Fatal("configuration save accepted concurrent settings change")
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Settings.Language != changedLanguage || saved.Apps[0].Description != "" {
		t.Fatalf("configuration save overwrote concurrent configuration: %#v", saved)
	}
}

func TestRunRejectsConflictingEnvOptions(t *testing.T) {
	if code := run([]string{"version", "--env-file", "custom.env", "--no-env-file"}); code != 2 {
		t.Fatalf("run returned %d, want 2", code)
	}
}

func TestVersionIgnoresColorValidation(t *testing.T) {
	if code := run([]string{"version", "--color", "unsupported", "--no-env-file"}); code != 0 {
		t.Fatalf("run returned %d, want 0", code)
	}
}

func TestRunRejectsRemovedExecutionCommands(t *testing.T) {
	for _, command := range []string{"migrate", "download", "run", "list", "show", "check", "update"} {
		if code := run([]string{command}); code != 2 {
			t.Fatalf("run(%q) returned %d, want 2", command, code)
		}
	}
}

func TestResolveCommandRejectsRemovedTUIInitAndScanCommands(t *testing.T) {
	bootstrap := service.DefaultBootstrap()
	for _, command := range []string{"tui", "init", "scan"} {
		if _, _, code, done := resolveCommand([]string{command}, bootstrap); !done || code != 2 {
			t.Fatalf("resolveCommand(%q) = done %v, code %d; want rejected with code 2", command, done, code)
		}
	}
}

func TestResolveCommandUsesDefaultActionWithoutSyntheticSubcommand(t *testing.T) {
	action, arguments, code, done := resolveCommand(nil, service.DefaultBootstrap())
	if action != "default" || len(arguments) != 0 || code != 0 || done {
		t.Fatalf("resolveCommand(nil) = %q, %#v, %d, %v", action, arguments, code, done)
	}
}

func TestUsageTextStartsWithTendKitBanner(t *testing.T) {
	previous := i18n.Current()
	t.Cleanup(func() { i18n.Set(previous) })
	i18n.Set(i18n.English)
	text := usageText(service.DefaultBootstrap())
	if !strings.HasPrefix(strings.TrimLeft(text, "\r\n"), i18n.Banner()+"\n\nTendKit (Go)\n") {
		t.Fatalf("help does not start with the TendKit banner:\n%s", text)
	}
}

func TestParseCommandOptionsDerivesLockPathFromConfig(t *testing.T) {
	bootstrap := service.DefaultBootstrap()
	tests := []struct {
		name      string
		arguments []string
		config    string
		lock      string
	}{
		{name: "defaults", config: bootstrap.ConfigPath, lock: bootstrap.ConfigPath + ".lock"},
		{name: "custom config", arguments: []string{"--config", "custom/catalog.json"}, config: "custom/catalog.json", lock: "custom/catalog.json.lock"},
		{name: "explicit lock", arguments: []string{"--config", "custom/catalog.json", "--lock", "locks/tendkit.lock"}, config: "custom/catalog.json", lock: "locks/tendkit.lock"},
		{name: "explicit lock before config", arguments: []string{"--lock", "locks/tendkit.lock", "--config", "custom/catalog.json"}, config: "custom/catalog.json", lock: "locks/tendkit.lock"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, code, done := parseCommandOptions("default", test.arguments, bootstrap)
			if done || code != 0 {
				t.Fatalf("parseCommandOptions() done=%t code=%d", done, code)
			}
			if options.configPath != test.config || options.lockPath != test.lock {
				t.Fatalf("paths = (%q, %q), want (%q, %q)", options.configPath, options.lockPath, test.config, test.lock)
			}
		})
	}
}

func TestResolveCommandPathsRejectsMissingUserHome(t *testing.T) {
	t.Setenv("HOME", "")
	bootstrap := service.DefaultBootstrap()
	options, code, done := parseCommandOptions("default", nil, bootstrap)
	if done || code != 0 {
		t.Fatalf("parseCommandOptions() done=%t code=%d", done, code)
	}
	if _, err := resolveCommandPaths(options); err == nil {
		t.Fatal("default paths were accepted without a resolvable user home")
	}
}

func TestRunRejectsRemovedExecutionOptions(t *testing.T) {
	for _, arguments := range [][]string{
		{"version", "--workers", "1"},
		{"version", "--timeout", "30"},
		{"version", "--dry-run"},
		{"version", "--json"},
		{"version", "--name", "go"},
		{"version", "--catalog", "applications.json"},
		{"version", "--state", "state.json"},
	} {
		if code := run(arguments); code != 2 {
			t.Fatalf("run(%v) returned %d, want 2", arguments, code)
		}
	}
}

func TestDefaultTUIAutoInitializesMissingConfigurationAndAcceptsGlobalOptions(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "custom", "config.json")
	lockPath := filepath.Join(directory, "config.lock")
	envPath := filepath.Join(directory, "options.env")
	_ = os.Unsetenv("TENDKIT_MAIN_TEST")
	t.Cleanup(func() { _ = os.Unsetenv("TENDKIT_MAIN_TEST") })
	if err := os.WriteFile(envPath, []byte("TENDKIT_MAIN_TEST=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	code := runWithTUI([]string{
		"--config", configPath, "--lock", lockPath, "--color", "never", "--lang", "en", "--env-file", envPath,
	}, func(_ context.Context, store *service.Service, color ui.Mode) error {
		called = true
		if color != ui.ModeNever {
			t.Fatalf("color mode = %q, want %q", color, ui.ModeNever)
		}
		if _, _, err := store.Load(); err != nil {
			t.Fatalf("TUI received uninitialized config: %v", err)
		}
		return nil
	})
	if code != 0 || !called {
		t.Fatalf("runWithTUI returned %d, called=%v", code, called)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("automatic initialization did not create %s: %v", configPath, err)
	}
}

func TestDefaultTUICreatesDerivedLockBesideCustomConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	configPath := filepath.Join(directory, "custom", "catalog.json")
	lockPath := configPath + ".lock"
	code := runWithTUI([]string{"--config", configPath, "--no-env-file", "--color", "never"}, func(_ context.Context, _ *service.Service, _ ui.Mode) error {
		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("derived lock was not created at %s: %v", lockPath, err)
		}
		return nil
	})
	if code != 0 {
		t.Fatalf("runWithTUI returned %d", code)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("configuration was not created at %s: %v", configPath, err)
	}
}

func TestDefaultTUIUsesAndCreatesUserConfigDirectory(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}

	code := runWithTUI([]string{"--no-env-file"}, func(_ context.Context, store *service.Service, _ ui.Mode) error {
		if _, _, err := store.Load(); err != nil {
			t.Fatalf("TUI received uninitialized default config: %v", err)
		}
		return nil
	})
	if code != 0 {
		t.Fatalf("runWithTUI returned %d", code)
	}
	for _, path := range []string{
		filepath.Join(directory, ".config", "tendkit", "config.json"),
		filepath.Join(directory, ".config", "tendkit", "config.json.lock"),
		filepath.Join(directory, ".config", "tendkit", "logs", "run.log"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("default initialization did not create %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "conf")); !os.IsNotExist(err) {
		t.Fatalf("legacy startup-directory config was created: %v", err)
	}
}

func TestDefaultTUIDoesNotOverwriteInvalidExistingCatalog(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	lockPath := filepath.Join(directory, "config.lock")
	invalid := []byte("{invalid json\n")
	if err := os.WriteFile(configPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	code := runWithTUI([]string{
		"--config", configPath, "--lock", lockPath, "--no-env-file",
	}, func(context.Context, *service.Service, ui.Mode) error {
		called = true
		return nil
	})
	if code != 2 || called {
		t.Fatalf("runWithTUI returned %d, called=%v; want invalid configuration rejection", code, called)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(invalid) {
		t.Fatalf("invalid config was overwritten: %q", content)
	}
}

func TestVersionDoesNotInitializeDefaultConfiguration(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	directory := t.TempDir()
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"version", "--no-env-file"}); code != 0 {
		t.Fatalf("run returned %d", code)
	}
	if _, err := os.Stat(filepath.Join(directory, "conf")); !os.IsNotExist(err) {
		t.Fatalf("version unexpectedly initialized configuration: %v", err)
	}
}

func TestLanguageArgument(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		want      i18n.Language
		wantError bool
	}{
		{arguments: []string{"--lang", "en"}, want: i18n.English},
		{arguments: []string{"help", "--lang=zh-CN"}, want: i18n.Chinese},
		{arguments: []string{"--lang", "en", "--lang=fr"}, wantError: true},
	} {
		got, exists, err := languageArgument(test.arguments)
		if test.wantError {
			if err == nil {
				t.Fatalf("languageArgument(%v) accepted invalid language", test.arguments)
			}
			continue
		}
		if err != nil || !exists || got != test.want {
			t.Fatalf("languageArgument(%v) = %q, %v, %v", test.arguments, got, exists, err)
		}
	}
}

func TestConfiguredLanguageAndCLIOverridePrecedence(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
	catalog := config.Default()
	catalog.Settings.Language = "en"
	catalog.Apps = nil
	saveCommandTestConfig(t, store, catalog)
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "zh_CN.UTF-8")

	configPath := filepath.Join(directory, "config.json")
	lockPath := filepath.Join(directory, "config.lock")
	arguments := []string{"--config", configPath, "--lock", lockPath, "--color", "never", "--no-env-file"}
	observed := i18n.Language("")
	code := runWithTUI(arguments, func(context.Context, *service.Service, ui.Mode) error {
		observed = i18n.Current()
		return nil
	})
	if code != 0 || observed != i18n.English {
		t.Fatalf("configured English was not applied: code=%d language=%q", code, observed)
	}

	code = runWithTUI(append(arguments, "--lang", "zh"), func(context.Context, *service.Service, ui.Mode) error {
		observed = i18n.Current()
		return nil
	})
	if code != 0 || observed != i18n.Chinese {
		t.Fatalf("explicit Chinese did not override catalog: code=%d language=%q", code, observed)
	}
}

func TestRunInteractiveTUIRequiresTerminalBeforeUsingStore(t *testing.T) {
	i18n.Set(i18n.English)
	err := runInteractiveTUI(context.Background(), &service.Service{}, ui.ModeNever)
	if err == nil || err.Error() != i18n.T("tui.terminal_required") {
		t.Fatalf("runInteractiveTUI() error = %v, want terminal-required error", err)
	}
}

func TestRunWithTUIRejectsInvalidOptionsBeforeInitializingConfiguration(t *testing.T) {
	missingEnv := filepath.Join(t.TempDir(), "missing.env")
	for _, arguments := range [][]string{
		{"--color", "invalid", "--no-env-file"},
		{"--lang", "fr", "--no-env-file"},
		{"--env-file", missingEnv},
		{"version", "--help"},
	} {
		called := false
		if code := runWithTUI(arguments, func(context.Context, *service.Service, ui.Mode) error {
			called = true
			return nil
		}); code != 2 && !(len(arguments) == 2 && arguments[0] == "version" && code == 0) {
			t.Fatalf("runWithTUI(%v) = %d", arguments, code)
		} else if called {
			t.Fatalf("runWithTUI(%v) unexpectedly started TUI", arguments)
		}
	}
}
