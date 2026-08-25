package config

import (
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

func TestInstallModeRequiresCompleteDefaultActionSequence(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			catalog := defaultConfig()
			catalog.Apps = []model.Application{{
				ID: "install", Name: "Install", Type: model.ApplicationTypeCLI, InstallPath: "/usr/local/bin/install", Enabled: true,
				UpdateMode: model.ModeInstall, Provider: model.ProviderConfig{Type: test.provider, Actions: &test.actions},
				StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
			}}
			err := validateConfig(catalog)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateConfig() error = %v, valid=%t", err, test.valid)
			}
		})
	}
}

func TestInstallModeRejectsUVProvider(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "uv-install", Name: "Ruff", Type: model.ApplicationTypePackage, InstallPath: "/usr/local/bin/ruff", Enabled: true,
		UpdateMode: model.ModeInstall, Provider: model.ProviderConfig{Type: model.ProviderUV}, Package: "ruff", Identity: "package:uv:ruff",
		StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil {
		t.Fatal("UV install mode was accepted")
	}
}

func TestConfigProviderValidationErrorsAreLocalized(t *testing.T) {
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
}
