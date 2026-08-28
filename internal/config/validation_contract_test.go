package config

import (
	"github.com/eoctet/tendkit/internal/model"

	"encoding/json"
	"github.com/eoctet/tendkit/pkg/i18n"
	"path/filepath"

	"os"
	"strings"
	"testing"

	"fmt"
)

func TestConfigValidationContract(t *testing.T) {
	t.Run("target-provider-and-downloader-schema", func(t *testing.T) {
		valid := func(t *testing.T) []byte {
			t.Helper()
			data, err := json.Marshal(defaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			document["apps"] = []any{map[string]any{
				"id": "sample", "name": "Sample", "type": model.ApplicationTypeCLI,
				"install_path": "/tmp/sample", "enabled": true, "update_mode": "check",
				"provider":       map[string]any{"type": "default", "actions": map[string]any{"version": "printf 1.0.0", "check": "printf 1.0.0"}},
				"status_managed": map[string]any{"update_status": model.StatusUnchecked},
			}}
			return mustMarshalJSON(t, document)
		}

		t.Run("accepts target object form", func(t *testing.T) {
			store := testUnifiedStore(t)
			if err := os.WriteFile(store.ConfigPath, valid(t), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.load(); err != nil {
				t.Fatalf("target schema rejected: %v", err)
			}
		})

		for name, mutate := range map[string]func(map[string]any){
			"provider string": func(document map[string]any) { document["apps"].([]any)[0].(map[string]any)["provider"] = "none" },
			"operation command": func(document map[string]any) {
				document["apps"].([]any)[0].(map[string]any)["operation_command"] = map[string]any{}
			},
			"downloader binary": func(document map[string]any) {
				document["settings"].(map[string]any)["downloader"].(map[string]any)["binary"] = "aria2c"
			},
			"continue download": func(document map[string]any) {
				document["settings"].(map[string]any)["downloader"].(map[string]any)["continue_download"] = true
			},
			"split number": func(document map[string]any) {
				document["settings"].(map[string]any)["downloader"].(map[string]any)["split_num"] = 64
			},
			"max connection number": func(document map[string]any) {
				document["settings"].(map[string]any)["downloader"].(map[string]any)["max_connection_num"] = 10
			},
			"empty actions": func(document map[string]any) {
				document["apps"].([]any)[0].(map[string]any)["provider"].(map[string]any)["actions"] = map[string]any{}
			},
		} {
			t.Run("rejects "+name, func(t *testing.T) {
				var document map[string]any
				if err := json.Unmarshal(valid(t), &document); err != nil {
					t.Fatal(err)
				}
				mutate(document)
				store := testUnifiedStore(t)
				if err := os.WriteFile(store.ConfigPath, mustMarshalJSON(t, document), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := store.load(); err == nil {
					t.Fatal("legacy or noncanonical schema accepted")
				}
			})
		}
	})
	t.Run("store-rejects-install-without-update-action", func(t *testing.T) {
		store := testUnifiedStore(t)
		catalog := defaultConfig()
		catalog.Apps = []model.Application{{
			ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: "/tmp/sample", Enabled: true, UpdateMode: model.ModeInstall,
			Provider: providerConfig(model.ProviderDefault, "", "", "", nil, "installer"), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
		}}
		data, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.ConfigPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.load(); err == nil {
			t.Fatal("install without update action was accepted")
		}
	})
	t.Run("load-config-rejects-invalid-scan-version-control", func(t *testing.T) {
		store := testUnifiedStore(t)
		value := testUnifiedConfig()
		value.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"sample": {"x": {Fingerprint: "bad", RecordedAt: "bad"}}}
		if err := store.createIfMissing(value); err == nil {
			t.Fatal("accepted invalid scan_version_control")
		}
	})
	t.Run("load-config-rejects-missing-bundle-id-list", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "config.json")
		data, err := json.Marshal(defaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		missing := strings.Replace(string(data), `"bundle_id":[],`, "", 1)
		if missing == string(data) {
			t.Fatalf("default catalog did not encode settings.scan.bundle_id: %s", data)
		}
		if err := os.WriteFile(path, []byte(missing), 0o600); err != nil {
			t.Fatal(err)
		}
		store := newStore(path, path+".lock")
		if _, err := store.load(); err == nil || !strings.Contains(err.Error(), "settings.scan.bundle_id") {
			t.Fatalf("missing settings.scan.bundle_id was accepted: %v", err)
		}
	})
	t.Run("load-config-requires-every-scan-switch", func(t *testing.T) {
		for _, key := range []string{"path", "application", "python", "node", "go", "uv", "ruby"} {
			data, err := json.Marshal(defaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			scan := document["settings"].(map[string]any)["scan"].(map[string]any)
			if key == "path" || key == "application" {
				delete(scan, key)
			} else {
				delete(scan["packages"].(map[string]any), key)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, mustMarshalJSON(t, document), 0o600); err != nil {
				t.Fatal(err)
			}
			store := newStore(path, path+".lock")
			if _, err := store.load(); err == nil {
				t.Errorf("missing scan switch %q was accepted", key)
			}
		}
	})
	t.Run("settings-and-identity-validation-matrix", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*model.Config)
			valid  bool
			field  string
		}{
			{name: "zero global timeout", mutate: func(c *model.Config) { c.Settings.TimeoutSeconds = 0 }, field: "timeout_seconds"},
			{name: "excessive global timeout", mutate: func(c *model.Config) { c.Settings.TimeoutSeconds = maxTimeoutSeconds + 1 }, field: "timeout_seconds"},
			{name: "unsupported language", mutate: func(c *model.Config) { c.Settings.Language = "fr" }, field: "settings.language"},
			{name: "missing language", mutate: func(c *model.Config) { c.Settings.Language = "" }},
			{name: "custom bundle IDs", mutate: func(c *model.Config) {
				c.Settings.Scan.BundleID = []string{"md.obsidian", "com.example.Editor-Preview"}
			}, valid: true},
			{name: "empty bundle ID", mutate: func(c *model.Config) { c.Settings.Scan.BundleID = []string{""} }, field: "settings.scan.bundle_id"},
			{name: "malformed bundle ID", mutate: func(c *model.Config) { c.Settings.Scan.BundleID = []string{"obsidian"} }, field: "settings.scan.bundle_id"},
			{name: "duplicate bundle ID", mutate: func(c *model.Config) { c.Settings.Scan.BundleID = []string{"md.obsidian", "MD.OBSIDIAN"} }, field: "settings.scan.bundle_id"},
			{name: "duplicate normalized identity", mutate: func(c *model.Config) {
				c.Apps = []model.Application{
					{ID: "one", Name: "One", Type: model.ApplicationTypeCLI, InstallPath: "one", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), Identity: "cli:tool", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
					{ID: "two", Name: "Two", Type: model.ApplicationTypeCLI, InstallPath: "two", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), Identity: " CLI:TOOL ", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
				}
			}, field: "cli:tool"},
			{name: "aria2 absolute path", mutate: func(c *model.Config) { c.Settings.Downloader.CLI = "/opt/homebrew/bin/aria2c" }, valid: true},
			{name: "curl absolute path and arguments", mutate: func(c *model.Config) {
				c.Settings.Downloader.CLI = "/usr/bin/curl"
				c.Settings.Downloader.ExtraArgs = []string{"--retry=3", "--connect-timeout=10"}
			}, valid: true},
			{name: "aria2 argument for curl", mutate: func(c *model.Config) {
				c.Settings.Downloader.CLI = "curl"
				c.Settings.Downloader.ExtraArgs = []string{"--split=64"}
			}, field: "settings.downloader.extra_args"},
			{name: "unsafe global downloader argument", mutate: func(c *model.Config) { c.Settings.Downloader.ExtraArgs = []string{"--enable-rpc=true"} }, field: "settings.downloader.extra_args"},
		}
		for _, test := range tests {
			catalog := defaultConfig()
			test.mutate(&catalog)
			err := validateConfig(catalog)
			if (err == nil) != test.valid || err != nil && test.field != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.field)) {
				t.Errorf("%s: validation error = %v, valid=%t, field=%q", test.name, err, test.valid, test.field)
			}
		}
	})
	t.Run("validate-config-rejects-aria2-application-arguments-for-curl", func(t *testing.T) {
		catalog := defaultConfig()
		catalog.Settings.Downloader.CLI = "curl"
		catalog.Settings.Downloader.ExtraArgs = []string{"--retry=3"}
		catalog.Apps = []model.Application{{
			ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload,
			Provider: providerConfig(model.ProviderDefault, "", "printf latest", "", &model.Download{URL: "https://example.invalid/file", ExtraArgs: []string{"--split=64"}}, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
		}}
		if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "apps[0].provider.actions.download.extra_args") {
			t.Fatalf("expected curl application extra_args validation error, got %v", err)
		}
	})
	t.Run("validate-config-requires-mode-configuration", func(t *testing.T) {
		for _, test := range []struct {
			mode  model.UpdateMode
			field string
			check string
		}{
			{mode: model.ModeAuto, field: "update", check: "check"},
			{mode: model.ModeDownload, field: "download.url", check: "check"},
		} {
			catalog := defaultConfig()
			catalog.Apps = []model.Application{{
				ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: test.mode,
				Provider: providerConfig(model.ProviderDefault, "", test.check, "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
			}}
			if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %s validation, got %v", test.field, err)
			}
		}
	})
	t.Run("validate-config-rejects-invalid-application-type-and-install-path", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			app   model.Application
			field string
		}{
			{name: "unknown type", app: model.Application{ID: "sample", Name: "Sample", Type: "desktop", InstallPath: "sample", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}, field: "type"},
			{name: "empty type", app: model.Application{ID: "sample", Name: "Sample", InstallPath: "sample", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}, field: "type"},
			{name: "empty install path", app: model.Application{ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}, field: "install_path"},
		} {
			catalog := defaultConfig()
			catalog.Apps = []model.Application{test.app}
			if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Errorf("%s: expected %s validation error, got %v", test.name, test.field, err)
			}
		}
	})
	t.Run("download-action-validation-matrix", func(t *testing.T) {
		tests := []struct {
			name     string
			provider model.ProviderType
			download *model.Download
			field    string
		}{
			{name: "non-HTTP URL", provider: model.ProviderDefault, download: &model.Download{URL: "file:///tmp/application.zip"}, field: "download.url"},
			{name: "invalid checksum", provider: model.ProviderDefault, download: &model.Download{URL: "https://example.invalid/file", ChecksumValue: "not-a-digest"}, field: "download.checksum_value"},
			{name: "missing checksum source", provider: model.ProviderDefault, download: &model.Download{URL: "https://example.invalid/file", ChecksumEnabled: true}, field: "checksum_url"},
			{name: "non-HTTP checksum URL", provider: model.ProviderDefault, download: &model.Download{URL: "https://example.invalid/file", ChecksumEnabled: true, ChecksumURL: "file:///tmp/checksum"}, field: "checksum_url"},
			{name: "incomplete GitHub Release override", provider: model.ProviderGitHubRelease, download: &model.Download{Filename: "sample-{last_version}.zip", ChecksumEnabled: true}},
			{name: "empty Sparkle override", provider: model.ProviderSparkle, download: &model.Download{}},
			{name: "unsafe application downloader argument", provider: model.ProviderDefault, download: &model.Download{URL: "https://example.invalid/file", ExtraArgs: []string{"--out=elsewhere"}}, field: "extra_args"},
		}
		for _, test := range tests {
			app := model.Application{ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload, Provider: providerConfig(test.provider, "", "", "", test.download, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}
			switch test.provider {
			case model.ProviderGitHubRelease:
				app.Package = "owner/repo"
			case model.ProviderSparkle:
				app.Type, app.InstallPath, app.Package = model.ApplicationTypeBundle, "/Applications/App.app", "https://example.invalid/appcast.xml"
			}
			catalog := defaultConfig()
			catalog.Apps = []model.Application{app}
			err := validateConfig(catalog)
			if err == nil || test.field != "" && !strings.Contains(err.Error(), test.field) {
				t.Errorf("%s: validation error = %v, field=%q", test.name, err, test.field)
			}
		}
	})
	t.Run("validate-config-rejects-unsupported-provider", func(t *testing.T) {
		catalog := defaultConfig()
		catalog.Apps = []model.Application{{
			ID: "custom-application", Name: "Custom Application", Type: model.ApplicationTypeCLI, InstallPath: "custom-application",
			UpdateMode: model.ModeCheck, Provider: providerConfig("company_release_feed", "", "", "", nil, ""),
			StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
		}}
		if err := validateConfig(catalog); err == nil {
			t.Fatal("unsupported provider was accepted")
		}
	})
	t.Run("provider-actions-omit-when-absent", func(t *testing.T) {
		encoded, err := json.Marshal(providerConfig(model.ProviderDefault, "", "", "", nil, ""))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "actions") {
			t.Fatalf("empty provider actions were encoded: %s", encoded)
		}
	})
	t.Run("validate-config-rejects-missing-provider-url", func(t *testing.T) {
		catalog := defaultConfig()
		delete(catalog.Settings.ProviderURLs, "github_release")
		if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "provider_urls.github_release") {
			t.Fatalf("expected provider URL validation, got %v", err)
		}
	})
	t.Run("validate-config-rejects-non-target-provider-url", func(t *testing.T) {
		catalog := defaultConfig()
		catalog.Settings.ProviderURLs["vscode"] = "https://example.invalid/vscode"
		if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "provider_urls.vscode") {
			t.Fatalf("non-target provider URL was accepted: %v", err)
		}
	})
	t.Run("load-config-rejects-unsupported-schema", func(t *testing.T) {
		for _, version := range []int{2, 3} {
			directory := t.TempDir()
			path := filepath.Join(directory, "config.json")
			content := fmt.Sprintf(`{"schema_version":%d,"settings":{},"apps":[],"scan_version_control":{}}`, version)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			store := newStore(path, path+".lock")
			_, err := store.load()
			if err == nil || !strings.Contains(err.Error(), "schema_version") {
				t.Fatalf("expected schema validation error for %s, got %v", content, err)
			}
		}
	})
	t.Run("load-config-requires-unified-structure-fields", func(t *testing.T) {
		valid, err := json.Marshal(testUnifiedConfig())
		if err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			"scan version control missing": strings.Replace(string(valid), `,"scan_version_control":{}`, "", 1),
		} {
			t.Run(name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				store := newStore(path, path+".lock")
				if _, err := store.load(); err == nil {
					t.Fatal("missing required unified field was accepted")
				}
			})
		}
	})
	t.Run("load-config-rejects-unknown-managed-status", func(t *testing.T) {
		value := testUnifiedConfig()
		value.Apps[0].StatusManaged.UpdateStatus = "future_status"
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := newStore(path, path+".lock")
		if _, err := store.load(); err == nil || !strings.Contains(err.Error(), "future_status") {
			t.Fatalf("unknown update_status was accepted: %v", err)
		}
	})
	t.Run("validate-config-rejects-invalid-http-settings", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			change func(*model.HTTPSettings)
			field  string
		}{
			{name: "timeout", change: func(settings *model.HTTPSettings) { settings.TimeoutSeconds = 0 }, field: "http.timeout_seconds"},
			{name: "host concurrency", change: func(settings *model.HTTPSettings) { settings.MaxConcurrencyPerHost = 0 }, field: "max_concurrency_per_host"},
			{name: "retries", change: func(settings *model.HTTPSettings) { settings.Retries = maxHTTPRetries + 1 }, field: "http.retries"},
		} {
			catalog := defaultConfig()
			test.change(catalog.Settings.HTTP)
			if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Errorf("%s: expected %s validation, got %v", test.name, test.field, err)
			}
		}
	})
}
func TestConfigInstallValidationContract(t *testing.T) {
	t.Run("install-mode-requires-complete-default-action-sequence", func(t *testing.T) {
		complete := model.ProviderActions{Version: "version", Check: "check", Update: "update", Install: "install"}
		for _, test := range []struct {
			name     string
			actions  model.ProviderActions
			provider model.ProviderType
			valid    bool
		}{
			{"complete default", complete, model.ProviderDefault, true},
			{"missing version", model.ProviderActions{Check: "check", Update: "update", Install: "install"}, model.ProviderDefault, false},
			{"missing check", model.ProviderActions{Version: "version", Update: "update", Install: "install"}, model.ProviderDefault, false},
			{"missing update", model.ProviderActions{Version: "version", Check: "check", Install: "install"}, model.ProviderDefault, false},
			{"missing install", model.ProviderActions{Version: "version", Check: "check", Update: "update"}, model.ProviderDefault, false},
			{"non-default", complete, model.ProviderNPM, false},
		} {
			catalog := defaultConfig()
			catalog.Apps = []model.Application{{
				ID: "install", Name: "Install", Type: model.ApplicationTypeCLI, InstallPath: "/usr/local/bin/install", Enabled: true,
				UpdateMode: model.ModeInstall, Provider: model.ProviderConfig{Type: test.provider, Actions: &test.actions},
				StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
			}}
			err := validateConfig(catalog)
			if (err == nil) != test.valid {
				t.Errorf("%s: ValidateConfig() error = %v, valid=%t", test.name, err, test.valid)
			}
		}
	})
	t.Run("install-mode-rejects-uv-provider", func(t *testing.T) {
		catalog := defaultConfig()
		catalog.Apps = []model.Application{{
			ID: "uv-install", Name: "Ruff", Type: model.ApplicationTypePackage, InstallPath: "/usr/local/bin/ruff", Enabled: true,
			UpdateMode: model.ModeInstall, Provider: model.ProviderConfig{Type: model.ProviderUV}, Package: "ruff", Identity: "package:uv:ruff",
			StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
		}}
		if err := validateConfig(catalog); err == nil {
			t.Fatal("UV install mode was accepted")
		}
	})
	t.Run("config-provider-validation-errors-are-localized", func(t *testing.T) {
		original := i18n.Current()
		t.Cleanup(func() { i18n.Set(original) })
		for _, language := range []i18n.Language{i18n.English, i18n.Chinese} {
			i18n.Set(language)
			for _, test := range []struct {
				actions *model.ProviderActions
			}{
				{actions: &model.ProviderActions{}},
				{actions: &model.ProviderActions{Check: "check", Update: "update", Install: "install"}},
				{actions: nil},
			} {
				catalog := defaultConfig()
				catalog.Apps = []model.Application{{
					ID: "install", Name: "Install", Type: model.ApplicationTypeCLI, InstallPath: "/usr/local/bin/install", Enabled: true,
					UpdateMode: model.ModeInstall, Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: test.actions},
					StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
				}}
				err := validateConfig(catalog)
				if err == nil || strings.Contains(err.Error(), "config.") || strings.Contains(err.Error(), "%!") {
					t.Fatalf("%s localized validation error = %v", language, err)
				}
			}
		}
	})
}
func reachableModeApp(provider model.ProviderType, mode model.UpdateMode, enabled bool) model.Application {
	actions := &model.ProviderActions{Check: "check", Update: "update", Download: &model.Download{URL: "https://example.invalid/file"}, Version: "version", Install: "install"}
	if provider != model.ProviderDefault {
		actions = &model.ProviderActions{Update: "update"}
	}
	if mode == model.ModeCheck {
		actions.Update, actions.Install = "", ""
		actions.Download = nil
		if provider != model.ProviderDefault {
			actions = nil
		}
	}
	if mode == model.ModeAuto {
		actions.Install = ""
		actions.Download = nil
	}
	if mode == model.ModeDownload {
		actions.Update, actions.Install = "", ""
		if provider != model.ProviderDefault {
			actions = nil
		}
	}
	app := model.Application{ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: "sample", Enabled: enabled, UpdateMode: mode, Provider: model.ProviderConfig{Type: provider, Actions: actions}, StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}
	if testProviderUsesPackage(provider) {
		app.Package = "owner/package"
	}
	if provider == model.ProviderSparkle {
		app.Package = "https://example.invalid/appcast.xml"
	}
	return app
}

func testProviderUsesPackage(provider model.ProviderType) bool {
	switch provider {
	case model.ProviderGitHubRelease, model.ProviderGitHubTag, model.ProviderNPM, model.ProviderPyPI, model.ProviderUV, model.ProviderJetBrains:
		return true
	default:
		return false
	}
}

func configWithApp(app model.Application) model.Config {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{app}
	return catalog
}
func TestConfigProviderReachabilityContract(t *testing.T) {
	t.Run("enabled-provider-mode-reachability-matrix", func(t *testing.T) {
		providers := []model.ProviderType{
			model.ProviderDefault, model.ProviderGitHubRelease, model.ProviderGitHubTag,
			model.ProviderNPM, model.ProviderPyPI, model.ProviderJetBrains,
			model.ProviderGo, model.ProviderNodeLTS, model.ProviderSparkle,
			model.ProviderHomebrew, model.ProviderCargo,
		}
		for _, provider := range providers {
			for _, mode := range []model.UpdateMode{model.ModeCheck, model.ModeAuto, model.ModeDownload, model.ModeInstall} {
				app := reachableModeApp(provider, mode, true)
				wantValid := mode != model.ModeInstall || provider == model.ProviderDefault
				if mode == model.ModeDownload && (provider == model.ProviderHomebrew || provider == model.ProviderCargo) {
					wantValid = false
				}
				if provider == model.ProviderCargo {
					wantValid = false
				}
				if err := validateConfig(configWithApp(app)); (err == nil) != wantValid {
					t.Errorf("ValidateConfig(%s/%s) error = %v, valid=%t", provider, mode, err, wantValid)
				}
			}
		}
	})
	t.Run("enabled-default-requires-latest-and-disabled-default-may-be-unreachable", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			mode    model.UpdateMode
			enabled bool
			valid   bool
		}{
			{"enabled check", model.ModeCheck, true, false},
			{"enabled auto", model.ModeAuto, true, false},
			{"enabled download", model.ModeDownload, true, false},
			{"enabled install", model.ModeInstall, true, false},
			{"disabled check", model.ModeCheck, false, true},
			{"disabled auto", model.ModeAuto, false, true},
			{"disabled download", model.ModeDownload, false, true},
			{"disabled install", model.ModeInstall, false, true},
		} {
			app := reachableModeApp(model.ProviderDefault, test.mode, test.enabled)
			app.Provider.Actions = nil
			if err := validateConfig(configWithApp(app)); (err == nil) != test.valid {
				t.Errorf("%s: ValidateConfig() error = %v, valid=%t", test.name, err, test.valid)
			}
		}
	})
	t.Run("cargo-requires-explicit-check-and-update-actions", func(t *testing.T) {
		for _, enabled := range []bool{false, true} {
			app := reachableModeApp(model.ProviderCargo, model.ModeCheck, enabled)
			app.Provider.Actions = nil
			err := validateConfig(configWithApp(app))
			if enabled && err == nil {
				t.Error("enabled Cargo check without explicit action was accepted")
			}
			if !enabled && err != nil {
				t.Errorf("disabled Cargo inventory candidate was rejected: %v", err)
			}
		}
		auto := reachableModeApp(model.ProviderCargo, model.ModeAuto, true)
		auto.Provider.Actions = nil
		if err := validateConfig(configWithApp(auto)); err == nil {
			t.Fatal("Cargo auto without explicit update action was accepted")
		}
		auto.Provider.Actions = &model.ProviderActions{Check: "cargo check sample", Update: "cargo install sample"}
		if err := validateConfig(configWithApp(auto)); err != nil {
			t.Fatalf("Cargo auto with explicit update action was rejected: %v", err)
		}
	})
	t.Run("disabled-apps-still-validate-provided-action-structure", func(t *testing.T) {
		app := reachableModeApp(model.ProviderDefault, model.ModeCheck, false)
		app.Provider.Actions = &model.ProviderActions{Download: &model.Download{URL: "file:///invalid"}}
		if err := validateConfig(configWithApp(app)); err == nil {
			t.Fatal("disabled app accepted malformed provided download action")
		}
	})
	t.Run("builtin-update-reachability-depends-on-application-type", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			provider model.ProviderType
			kind     string
			identity string
			valid    bool
		}{
			{"npm package", model.ProviderNPM, model.ApplicationTypePackage, "package:node:tool", true},
			{"pypi package", model.ProviderPyPI, model.ApplicationTypePackage, "package:python:tool", true},
			{"uv package", model.ProviderUV, model.ApplicationTypePackage, "package:uv:tool", true},
			{"go component without identity", model.ProviderGo, model.ApplicationTypePackage, "", true},
			{"ruby package", model.ProviderDefault, model.ApplicationTypePackage, "package:ruby:tool", false},
			{"sparkle application", model.ProviderSparkle, model.ApplicationTypeBundle, "app:example", true},
			{"go runtime", model.ProviderGo, model.ApplicationTypeCLI, "cli:go", false},
			{"npm cli", model.ProviderNPM, model.ApplicationTypeCLI, "cli:tool", false},
		} {
			app := reachableModeApp(test.provider, model.ModeAuto, true)
			app.Type, app.Identity = test.kind, test.identity
			if test.provider == model.ProviderUV {
				app.Package = "tool"
			}
			app.Provider.Actions = nil
			if test.provider == model.ProviderDefault {
				app.Provider.Actions = &model.ProviderActions{Version: "version", Check: "check"}
			}
			if err := validateConfig(configWithApp(app)); (err == nil) != test.valid {
				t.Errorf("%s: ValidateConfig() error = %v, valid=%t", test.name, err, test.valid)
			}
		}
	})
	t.Run("provider-validation-allows-missing-package-and-any-identity", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			provider model.ProviderType
			kind     string
			package_ string
			identity string
		}{
			{"uv identity omitted", model.ProviderUV, model.ApplicationTypePackage, "ruff", ""},
			{"uv package omitted", model.ProviderUV, model.ApplicationTypePackage, "", ""},
			{"uv unrelated identity", model.ProviderUV, model.ApplicationTypePackage, "ruff", "package:python:other"},
			{"pypi uv identity", model.ProviderPyPI, model.ApplicationTypePackage, "ruff", "package:uv:ruff"},
			{"npm package omitted", model.ProviderNPM, model.ApplicationTypePackage, "", ""},
		} {
			app := reachableModeApp(test.provider, model.ModeAuto, true)
			app.Type, app.Package, app.Identity = test.kind, test.package_, test.identity
			app.Provider.Actions = nil
			if err := validateConfig(configWithApp(app)); err != nil {
				t.Errorf("%s: ValidateConfig() error = %v", test.name, err)
			}
		}
	})
	t.Run("go-download-reachability-separates-runtime-and-components", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			app   model.Application
			valid bool
		}{
			{"runtime", reachableModeApp(model.ProviderGo, model.ModeDownload, true), true},
			{"component identity without action", func() model.Application {
				app := reachableModeApp(model.ProviderGo, model.ModeDownload, true)
				app.Type, app.Identity = model.ApplicationTypePackage, "package:go:example.com/tool/cmd/tool"
				app.InstallPath = "/usr/local/bin/tool"
				return app
			}(), false},
			{"component explicit action", func() model.Application {
				app := reachableModeApp(model.ProviderGo, model.ModeDownload, true)
				app.Type, app.Identity = model.ApplicationTypePackage, "package:go:example.com/tool/cmd/tool"
				app.InstallPath = "/usr/local/bin/tool"
				app.Provider.Actions = &model.ProviderActions{Download: &model.Download{URL: "https://example.invalid/tool.tar.gz"}}
				return app
			}(), true},
			{"component missing identity", func() model.Application {
				app := reachableModeApp(model.ProviderGo, model.ModeDownload, true)
				app.Type, app.Identity = model.ApplicationTypePackage, "package:node:tool"
				app.InstallPath = "/usr/local/bin/tool"
				return app
			}(), false},
		} {
			if err := validateConfig(configWithApp(test.app)); (err == nil) != test.valid {
				t.Errorf("%s: ValidateConfig() error = %v, valid=%t", test.name, err, test.valid)
			}
		}
	})
}
func TestLogLevelContract(t *testing.T) {
	t.Run("log-level-is-strict-and-normalized", func(t *testing.T) {
		catalog := defaultConfig()
		catalog.Settings.LogLevel = "warn"
		normalizeConfig(&catalog)
		if catalog.Settings.LogLevel != "WARN" {
			t.Fatalf("normalized level = %q", catalog.Settings.LogLevel)
		}
		if err := validateConfig(catalog); err != nil {
			t.Fatal(err)
		}
		catalog.Settings.LogLevel = "verbose"
		if err := validateConfig(catalog); err == nil {
			t.Fatal("invalid log level accepted")
		}
	})
	t.Run("invalid-log-level-error-is-localized", func(t *testing.T) {
		original := i18n.Current()
		t.Cleanup(func() { i18n.Set(original) })
		messages := make([]string, 0, 2)
		for _, language := range []i18n.Language{i18n.English, i18n.Chinese} {
			i18n.Set(language)
			catalog := defaultConfig()
			catalog.Settings.LogLevel = "verbose"
			err := validateConfig(catalog)
			want := i18n.T("config.log_level_invalid", "verbose")
			if err == nil || err.Error() != want {
				t.Fatalf("%s localized log-level error = %v", language, err)
			}
			messages = append(messages, want)
		}
		if messages[0] == messages[1] {
			t.Fatalf("log-level errors are not localized: %q", messages[0])
		}
	})
}
