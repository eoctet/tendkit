package config

import (
	"testing"

	"github.com/eoctet/tendkit/internal/model"
)

func TestEnabledProviderModeReachabilityMatrix(t *testing.T) {
	providers := []model.ProviderType{
		model.ProviderDefault, model.ProviderGitHubRelease, model.ProviderGitHubTag,
		model.ProviderNPM, model.ProviderPyPI, model.ProviderJetBrains,
		model.ProviderGo, model.ProviderNodeLTS, model.ProviderSparkle,
		model.ProviderHomebrew, model.ProviderCargo,
	}
	for _, provider := range providers {
		for _, mode := range []model.UpdateMode{model.ModeCheck, model.ModeAuto, model.ModeDownload, model.ModeInstall} {
			t.Run(string(provider)+"/"+string(mode), func(t *testing.T) {
				app := reachableModeApp(provider, mode, true)
				wantValid := mode != model.ModeInstall || provider == model.ProviderDefault
				if mode == model.ModeDownload && (provider == model.ProviderHomebrew || provider == model.ProviderCargo) {
					wantValid = false
				}
				if provider == model.ProviderCargo {
					wantValid = false
				}
				if err := validateConfig(configWithApp(app)); (err == nil) != wantValid {
					t.Fatalf("ValidateConfig(%s/%s) error = %v, valid=%t", provider, mode, err, wantValid)
				}
			})
		}
	}
}

func TestEnabledDefaultRequiresLatestAndDisabledDefaultMayBeUnreachable(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			app := reachableModeApp(model.ProviderDefault, test.mode, test.enabled)
			app.Provider.Actions = nil
			if err := validateConfig(configWithApp(app)); (err == nil) != test.valid {
				t.Fatalf("ValidateConfig() error = %v, valid=%t", err, test.valid)
			}
		})
	}
}

func TestCargoRequiresExplicitCheckAndUpdateActions(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			app := reachableModeApp(model.ProviderCargo, model.ModeCheck, enabled)
			app.Provider.Actions = nil
			err := validateConfig(configWithApp(app))
			if enabled && err == nil {
				t.Fatal("enabled Cargo check without explicit action was accepted")
			}
			if !enabled && err != nil {
				t.Fatalf("disabled Cargo inventory candidate was rejected: %v", err)
			}
		})
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
}

func TestDisabledAppsStillValidateProvidedActionStructure(t *testing.T) {
	app := reachableModeApp(model.ProviderDefault, model.ModeCheck, false)
	app.Provider.Actions = &model.ProviderActions{Download: &model.Download{URL: "file:///invalid"}}
	if err := validateConfig(configWithApp(app)); err == nil {
		t.Fatal("disabled app accepted malformed provided download action")
	}
}

func TestBuiltinUpdateReachabilityDependsOnApplicationType(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
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
				t.Fatalf("ValidateConfig() error = %v, valid=%t", err, test.valid)
			}
		})
	}
}

func TestProviderValidationAllowsMissingPackageAndAnyIdentity(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			app := reachableModeApp(test.provider, model.ModeAuto, true)
			app.Type, app.Package, app.Identity = test.kind, test.package_, test.identity
			app.Provider.Actions = nil
			if err := validateConfig(configWithApp(app)); err != nil {
				t.Fatalf("ValidateConfig() error = %v", err)
			}
		})
	}
}

func TestGoDownloadReachabilitySeparatesRuntimeAndComponents(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			if err := validateConfig(configWithApp(test.app)); (err == nil) != test.valid {
				t.Fatalf("ValidateConfig() error = %v, valid=%t", err, test.valid)
			}
		})
	}
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
