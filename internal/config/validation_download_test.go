package config

import (
	"testing"

	"github.com/eoctet/tendkit/internal/model"
)

func TestDownloadModeAcceptsBuiltinProviderWithoutDownloadAction(t *testing.T) {
	for _, provider := range []model.ProviderType{
		model.ProviderGitHubRelease, model.ProviderGitHubTag, model.ProviderNPM,
		model.ProviderPyPI, model.ProviderJetBrains, model.ProviderGo,
		model.ProviderNodeLTS, model.ProviderSparkle,
	} {
		t.Run(string(provider), func(t *testing.T) {
			catalog := defaultConfig()
			app := model.Application{
				ID: "download-" + string(provider), Name: "Download " + string(provider),
				Type: model.ApplicationTypeCLI, InstallPath: "/usr/local/bin/sample", Enabled: true,
				UpdateMode: model.ModeDownload, Provider: model.ProviderConfig{Type: provider},
				StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
			}
			switch provider {
			case model.ProviderGitHubRelease, model.ProviderGitHubTag, model.ProviderNPM, model.ProviderPyPI, model.ProviderJetBrains:
				app.Package = "owner/package"
			}
			catalog.Apps = []model.Application{app}
			if err := validateConfig(catalog); err != nil {
				t.Fatalf("download mode rejected builtin %s without action: %v", provider, err)
			}
		})
	}
}
