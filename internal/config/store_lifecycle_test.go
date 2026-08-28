package config

import (
	"encoding/json"
	"github.com/eoctet/tendkit/internal/model"
	"os"
	"slices"

	"path/filepath"
	"strings"

	"testing"
)

func testUnifiedStore(t *testing.T) store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	return newStore(path, path+".lock")
}
func testUnifiedConfig() model.Config {
	value := defaultConfig()
	value.Apps = []model.Application{{ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: "/tmp/sample", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "check", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
	return value
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

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestConfigStoreLifecycle(t *testing.T) {
	t.Run("execution-security-rejects-writable-catalogs-and-symlinks", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "config.json")
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := newStore(path, path+".lock")
		if err := store.validateExecutionSecurity(); err != nil {
			t.Fatalf("secure catalog rejected: %v", err)
		}
		if err := os.Chmod(path, 0o620); err != nil {
			t.Fatal(err)
		}
		if err := store.validateExecutionSecurity(); err == nil {
			t.Fatal("group-writable catalog accepted")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(directory, "linked.json")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if err := newStore(link, link+".lock").validateExecutionSecurity(); err == nil {
			t.Fatal("symlink catalog accepted")
		}
	})
	t.Run("unified-config-persists-schema-v1-apps-and-status", func(t *testing.T) {
		store := testUnifiedStore(t)
		value := testUnifiedConfig()
		value.Apps[0].StatusManaged.CurrentVersion = "1.2.3"
		value.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"sample": {"name": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00+08:00"}}}
		if err := store.createIfMissing(value); err != nil {
			t.Fatal(err)
		}
		got, err := store.load()
		if err != nil || got.Apps[0].StatusManaged.CurrentVersion != "1.2.3" || got.ScanVersionControl["sample"]["name"].Fingerprint == "" {
			t.Fatalf("%#v %v", got, err)
		}
	})
	t.Run("load-config-persists-v1-scan-version-control", func(t *testing.T) {
		store := testUnifiedStore(t)
		value := testUnifiedConfig()
		value.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"sample": {"description": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00+08:00"}}}
		if err := store.createIfMissing(value); err != nil {
			t.Fatal(err)
		}
		got, err := store.load()
		if err != nil || got.ScanVersionControl["sample"]["description"].Fingerprint == "" {
			t.Fatalf("%#v %v", got, err)
		}
	})
	t.Run("load-config-uses-application-type-field", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "config.json")
		catalog := defaultConfig()
		catalog.Apps = []model.Application{{
			ID: "sample", Name: "Sample", Type: "cli", InstallPath: "sample", Enabled: true,
			UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "check", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
		}}
		data, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		encoded := string(data)
		obsoleteField := "ki" + "nd"
		if !strings.Contains(encoded, `"type":"cli"`) || strings.Contains(encoded, `"`+obsoleteField+`"`) {
			t.Fatalf("unexpected application encoding: %s", encoded)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := newStore(path, path+".lock")
		if _, err := store.load(); err != nil {
			t.Fatalf("apps.type was not accepted: %v", err)
		}
		legacy := strings.Replace(encoded, `"type":"cli"`, `"`+obsoleteField+`":"cli"`, 1)
		if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = store.reload()
		if err == nil || !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), obsoleteField) {
			t.Fatalf("obsolete application type field was not rejected: %v", err)
		}
	})
	t.Run("load-config-uses-scan-application-field", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "config.json")
		catalog := defaultConfig()
		data, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		encoded := string(data)
		if !strings.Contains(encoded, `"application":true`) || strings.Contains(encoded, `"scan":{"path":true,"apps":true`) {
			t.Fatalf("unexpected scan settings encoding: %s", encoded)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := newStore(path, path+".lock")
		if _, err := store.load(); err != nil {
			t.Fatalf("settings.scan.application was not accepted: %v", err)
		}
		legacy := strings.Replace(encoded, `"application":true`, `"apps":true`, 1)
		if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = store.reload()
		if err == nil || !strings.Contains(err.Error(), "settings.scan.application") {
			t.Fatalf("legacy settings.scan.apps was not rejected: %v", err)
		}
	})
	t.Run("load-config-package-manager-scan-switch-compatibility", func(t *testing.T) {
		const (
			homebrewFormula = "homebrew-formula"
			homebrewCask    = "homebrew-cask"
			cargo           = "cargo"
		)
		newDocument := func(t *testing.T) map[string]any {
			t.Helper()
			data, err := json.Marshal(defaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			return document
		}
		load := func(t *testing.T, document map[string]any) (model.Config, error) {
			t.Helper()
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, mustMarshalJSON(t, document), 0o600); err != nil {
				t.Fatal(err)
			}
			store := newStore(path, path+".lock")
			return store.load()
		}

		t.Run("default template enables all new ecosystems", func(t *testing.T) {
			packages := defaultConfig().Settings.Scan.Packages
			if !packages.HomebrewFormula || !packages.HomebrewCask || !packages.Cargo {
				t.Fatalf("default package scan settings=%#v", packages)
			}
		})

		t.Run("legacy JSON omits new switches", func(t *testing.T) {
			document := newDocument(t)
			packages := document["settings"].(map[string]any)["scan"].(map[string]any)["packages"].(map[string]any)
			delete(packages, homebrewFormula)
			delete(packages, homebrewCask)
			delete(packages, cargo)
			catalog, err := load(t, document)
			if err != nil {
				t.Fatalf("legacy config was rejected: %v", err)
			}
			loaded := catalog.Settings.Scan.Packages
			if loaded.HomebrewFormula || loaded.HomebrewCask || loaded.Cargo {
				t.Fatalf("legacy config silently enabled new scans: %#v", loaded)
			}
		})

		t.Run("new switches parse normally", func(t *testing.T) {
			document := newDocument(t)
			packages := document["settings"].(map[string]any)["scan"].(map[string]any)["packages"].(map[string]any)
			packages[homebrewFormula] = false
			packages[homebrewCask] = true
			packages[cargo] = false
			catalog, err := load(t, document)
			if err != nil {
				t.Fatalf("new scan switches were rejected: %v", err)
			}
			loaded := catalog.Settings.Scan.Packages
			if loaded.HomebrewFormula || !loaded.HomebrewCask || loaded.Cargo {
				t.Fatalf("new scan switches parsed incorrectly: %#v", loaded)
			}
		})

		t.Run("unknown package switch remains rejected", func(t *testing.T) {
			document := newDocument(t)
			packages := document["settings"].(map[string]any)["scan"].(map[string]any)["packages"].(map[string]any)
			packages["future-manager"] = true
			_, err := load(t, document)
			if err == nil || !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), "future-manager") {
				t.Fatalf("unknown scan switch was not strictly rejected: %v", err)
			}
		})
	})
	t.Run("embedded-defaults-are-valid-and-independent", func(t *testing.T) {
		first := defaultConfig()
		if err := validateConfig(first); err != nil {
			t.Fatal(err)
		}
		if len(first.Settings.ProviderURLs) == 0 {
			t.Fatal("embedded defaults did not load configurable URLs")
		}
		if first.Settings.Downloader.CLI != "aria2c" || len(first.Settings.ProviderURLs) != 7 {
			t.Fatalf("embedded defaults do not match target downloader/provider schema: %#v %#v", first.Settings.Downloader, first.Settings.ProviderURLs)
		}
		for _, argument := range []string{"--continue=true", "--split=64", "--max-connection-per-server=10"} {
			if !slices.Contains(first.Settings.Downloader.ExtraArgs, argument) {
				t.Fatalf("embedded defaults missing downloader argument %q: %#v", argument, first.Settings.Downloader.ExtraArgs)
			}
		}
		first.Settings.ProviderURLs["go"] = "changed"
		first.Settings.HTTP.TimeoutSeconds = 1
		second := defaultConfig()
		if second.Settings.ProviderURLs["go"] == "changed" {
			t.Fatal("default catalog instances share mutable configuration")
		}
		if second.Settings.HTTP.TimeoutSeconds == 1 {
			t.Fatal("default catalog instances share HTTP configuration")
		}
		if second.Settings.LogDir != "~/.config/tendkit/logs" {
			t.Fatalf("default log directory = %q", second.Settings.LogDir)
		}
		bootstrap := DefaultBootstrap()
		if bootstrap.ConfigPath == "" || bootstrap.LockPath == "" || bootstrap.EnvFile == "" || bootstrap.UserEnvFile == "" {
			t.Fatalf("invalid bootstrap defaults: %+v", bootstrap)
		}
		if bootstrap.ConfigPath != "~/.config/tendkit/config.json" || bootstrap.LockPath != "~/.config/tendkit/config.json.lock" {
			t.Fatalf("default bootstrap paths are not under the user config directory: %+v", bootstrap)
		}
		if bootstrap.EnvFile != ".env" || bootstrap.UserEnvFile != "~/.config/tendkit/.env" {
			t.Fatalf("default environment paths are invalid: %+v", bootstrap)
		}
	})
	t.Run("load-config-allows-empty-status-managed", func(t *testing.T) {
		data, err := json.Marshal(testUnifiedConfig())
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), `"status_managed":{"has_update":false,"update_status":"unchecked"}`, `"status_managed":{}`, 1))
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := newStore(path, path+".lock")
		loaded, err := store.load()
		if err != nil || loaded.Apps[0].StatusManaged.UpdateStatus != model.StatusUnchecked {
			t.Fatalf("empty status_managed was not normalized: %#v %v", loaded.Apps[0].StatusManaged, err)
		}
	})
	t.Run("load-config-allows-missing-status-managed", func(t *testing.T) {
		data, err := json.Marshal(testUnifiedConfig())
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), `,"status_managed":{"has_update":false,"update_status":"unchecked"}`, "", 1))
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := newStore(path, path+".lock")
		loaded, err := store.load()
		if err != nil || loaded.Apps[0].StatusManaged.UpdateStatus != model.StatusUnchecked {
			t.Fatalf("missing status_managed was not normalized: %#v %v", loaded.Apps[0].StatusManaged, err)
		}
	})
}
func TestEnvironmentFileContract(t *testing.T) {
	t.Run("load-parses-supported-dotenv-syntax", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		content := "\ufeff# comment\n" +
			"ENVFILE_TEST_PLAIN=value\n" +
			"export ENVFILE_TEST_EXPORTED = 'quoted value'\n" +
			"ENVFILE_TEST_DOUBLE=\"value # retained\"\n" +
			"ENVFILE_TEST_COMMENT=value # removed\n" +
			"ENVFILE_TEST_EMPTY=\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		keys := []string{"ENVFILE_TEST_PLAIN", "ENVFILE_TEST_EXPORTED", "ENVFILE_TEST_DOUBLE", "ENVFILE_TEST_COMMENT", "ENVFILE_TEST_EMPTY"}
		for _, key := range keys {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Unsetenv(key) })
		}

		result, err := LoadEnvFile(path, true, ".env")
		if err != nil {
			t.Fatal(err)
		}
		if !result.Exists || result.Loaded != len(keys) {
			t.Fatalf("unexpected result: %+v", result)
		}
		want := map[string]string{
			"ENVFILE_TEST_PLAIN": "value", "ENVFILE_TEST_EXPORTED": "quoted value",
			"ENVFILE_TEST_DOUBLE": "value # retained", "ENVFILE_TEST_COMMENT": "value", "ENVFILE_TEST_EMPTY": "",
		}
		for key, expected := range want {
			if actual, exists := os.LookupEnv(key); !exists || actual != expected {
				t.Errorf("%s=%q, exists=%v; want %q", key, actual, exists, expected)
			}
		}
	})
	t.Run("load-does-not-override-process-environment", func(t *testing.T) {
		const key = "ENVFILE_TEST_PRECEDENCE"
		t.Setenv(key, "process-value")
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(key+"='file-value'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := LoadEnvFile(path, true, ".env")
		if err != nil {
			t.Fatal(err)
		}
		if result.Loaded != 0 || os.Getenv(key) != "process-value" {
			t.Fatalf("process environment was overridden: result=%+v value=%q", result, os.Getenv(key))
		}
	})
	t.Run("load-missing-file-required-policy", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.env")
		result, err := LoadEnvFile(path, false, ".env")
		if err != nil || result.Exists || result.Loaded != 0 {
			t.Fatalf("optional missing file: result=%+v err=%v", result, err)
		}
		if _, err := LoadEnvFile(path, true, ".env"); err == nil || !strings.Contains(err.Error(), "不存在") {
			t.Fatalf("expected required-file error, got %v", err)
		}
	})
	t.Run("load-rejects-invalid-content", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte("INVALID LINE\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEnvFile(path, true, ".env"); err == nil || !strings.Contains(err.Error(), "缺少 '='") {
			t.Fatalf("expected syntax error, got %v", err)
		}
	})
}
