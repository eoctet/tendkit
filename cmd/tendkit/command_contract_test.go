package main

import (
	"os"

	"context"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/service"
	"github.com/eoctet/tendkit/internal/ui"
	"github.com/eoctet/tendkit/pkg/i18n"

	"path/filepath"

	"strings"

	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"testing"

	"github.com/eoctet/tendkit/internal/config"
)

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
func TestCommandContract(t *testing.T) {
	t.Run("build-metadata-defaults", func(t *testing.T) {
		if programVersion != "dev" {
			t.Fatalf("programVersion = %q, want dev", programVersion)
		}
		if commitSHA != "unknown" {
			t.Fatalf("commitSHA = %q, want unknown", commitSHA)
		}
		if buildDate != "unknown" {
			t.Fatalf("buildDate = %q, want unknown", buildDate)
		}
	})
	t.Run("require-supported-host-rejects-unsupported-system", func(t *testing.T) {
		previous := detectHostSystem
		detectHostSystem = func(context.Context) (runtimeutil.SystemInfo, error) {
			return runtimeutil.SystemInfo{OS: "windows", Architecture: "x86_64", FullName: "windows_unknown_unknown_x86_64"}, nil
		}
		t.Cleanup(func() { detectHostSystem = previous })
		if err := requireSupportedHost(context.Background()); err == nil {
			t.Fatal("unsupported host was accepted")
		}
	})
	t.Run("run-loads-only-explicit-env-file", func(t *testing.T) {
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
	})
	t.Run("run-selects-startup-env-file-before-user-env-file", func(t *testing.T) {
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
	})
	t.Run("run-falls-back-to-user-env-file", func(t *testing.T) {
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
	})
	t.Run("run-no-env-file-disables-default-load", func(t *testing.T) {
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
	})
	t.Run("run-rejects-conflicting-env-options", func(t *testing.T) {
		if code := run([]string{"version", "--env-file", "custom.env", "--no-env-file"}); code != 2 {
			t.Fatalf("run returned %d, want 2", code)
		}
	})
	t.Run("version-ignores-color-validation", func(t *testing.T) {
		if code := run([]string{"version", "--color", "unsupported", "--no-env-file"}); code != 0 {
			t.Fatalf("run returned %d, want 0", code)
		}
	})
	t.Run("run-rejects-removed-execution-commands", func(t *testing.T) {
		for _, command := range []string{"migrate", "download", "run", "list", "show", "check", "update"} {
			if code := run([]string{command}); code != 2 {
				t.Fatalf("run(%q) returned %d, want 2", command, code)
			}
		}
	})
	t.Run("resolve-command-uses-default-action-without-synthetic-subcommand", func(t *testing.T) {
		action, arguments, code, done := resolveCommand(nil, service.DefaultBootstrap())
		if action != "default" || len(arguments) != 0 || code != 0 || done {
			t.Fatalf("resolveCommand(nil) = %q, %#v, %d, %v", action, arguments, code, done)
		}
	})
	t.Run("usage-text-starts-with-tend-kit-banner", func(t *testing.T) {
		previous := i18n.Current()
		t.Cleanup(func() { i18n.Set(previous) })
		i18n.Set(i18n.English)
		text := usageText(service.DefaultBootstrap())
		if !strings.HasPrefix(strings.TrimLeft(text, "\r\n"), i18n.Banner()+"\n\nTendKit (Go)\n") {
			t.Fatalf("help does not start with the TendKit banner:\n%s", text)
		}
	})
	t.Run("parse-command-options-derives-lock-path-from-config", func(t *testing.T) {
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
			options, code, done := parseCommandOptions("default", test.arguments, bootstrap)
			if done || code != 0 {
				t.Errorf("%s: parseCommandOptions() done=%t code=%d", test.name, done, code)
				continue
			}
			if options.configPath != test.config || options.lockPath != test.lock {
				t.Errorf("%s: paths = (%q, %q), want (%q, %q)", test.name, options.configPath, options.lockPath, test.config, test.lock)
			}
		}
	})
	t.Run("resolve-command-paths-rejects-missing-user-home", func(t *testing.T) {
		t.Setenv("HOME", "")
		bootstrap := service.DefaultBootstrap()
		options, code, done := parseCommandOptions("default", nil, bootstrap)
		if done || code != 0 {
			t.Fatalf("parseCommandOptions() done=%t code=%d", done, code)
		}
		if _, err := resolveCommandPaths(options); err == nil {
			t.Fatal("default paths were accepted without a resolvable user home")
		}
	})
	t.Run("run-rejects-removed-execution-options", func(t *testing.T) {
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
	})
	t.Run("version-does-not-initialize-default-configuration", func(t *testing.T) {
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
	})
	t.Run("language-argument", func(t *testing.T) {
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
	})
	t.Run("configured-language-and-clioverride-precedence", func(t *testing.T) {
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
	})
}

func TestTUIConfigSaveTransaction(t *testing.T) {
	newStore := func(t *testing.T) (*config.Center, model.Config) {
		t.Helper()
		directory := t.TempDir()
		store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
		catalog := config.Default()
		catalog.Apps = []model.Application{{ID: "managed", Name: "Managed", Type: model.ApplicationTypeCLI, InstallPath: "managed", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "printf '1.0.0'", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
		return store, saveCommandTestConfig(t, store, catalog)
	}
	t.Run("merges-concurrent-runtime-and-keep-state", func(t *testing.T) {
		store, baseline := newStore(t)
		concurrent := baseline
		concurrent.Apps = append([]model.Application(nil), baseline.Apps...)
		concurrent.Apps[0].StatusManaged = model.ManagedStatus{CurrentVersion: "1.0.0", UpdateStatus: model.StatusCurrent}
		concurrent.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"managed": {"description": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-17T00:00:00+08:00"}}}
		snapshot, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(snapshot.Revision, concurrent); err != nil {
			t.Fatal(err)
		}
		proposed := baseline
		proposed.Apps = append([]model.Application(nil), baseline.Apps...)
		changedLanguage := "en"
		if baseline.Settings.Language == changedLanguage {
			changedLanguage = "zh"
		}
		proposed.Settings.Language = changedLanguage
		if _, err := service.NewWithConfig(store).SaveConfig(baseline, proposed); err != nil {
			t.Fatalf("non-conflicting save: %v", err)
		}
		saved, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Settings.Language != changedLanguage || saved.Apps[0].StatusManaged.CurrentVersion != "1.0.0" || saved.ScanVersionControl["managed"]["description"].Fingerprint == "" {
			t.Fatalf("save lost concurrent state: %#v", saved)
		}
	})
	t.Run("rejects-concurrent-configuration-without-overwrite", func(t *testing.T) {
		store, baseline := newStore(t)
		concurrent := baseline
		concurrent.Apps = append([]model.Application(nil), baseline.Apps...)
		changedLanguage := "en"
		if baseline.Settings.Language == changedLanguage {
			changedLanguage = "zh"
		}
		concurrent.Settings.Language = changedLanguage
		snapshot, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(snapshot.Revision, concurrent); err != nil {
			t.Fatal(err)
		}
		proposed := baseline
		proposed.Apps = append([]model.Application(nil), baseline.Apps...)
		proposed.Apps[0].Description = "editor change"
		if _, err := service.NewWithConfig(store).SaveConfig(baseline, proposed); err == nil {
			t.Fatal("configuration save accepted concurrent change")
		}
		saved, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Settings.Language != changedLanguage || saved.Apps[0].Description != "" {
			t.Fatalf("save overwrote concurrent configuration: %#v", saved)
		}
	})
}
