package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
)

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

func TestGitHubResolverUsesCurrentCatalogSettings(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"tag_name":"v2"}`)) }))
	defer second.Close()

	catalog := config.Default()
	catalog.Settings.HTTP.Retries = 0
	catalog.Settings.ProviderURLs[string(model.ProviderGitHubRelease)] = first.URL + "/{package}"
	catalog.Settings.ProviderURLs[string(model.ProviderGitHubTag)] = first.URL + "/{package}"
	resolved, err := (&Service{}).githubResolver(catalog).Resolve(context.Background(), "owner/repo")
	if err != nil || resolved != "" {
		t.Fatalf("first resolver = %q, %v", resolved, err)
	}

	catalog.Settings.ProviderURLs[string(model.ProviderGitHubRelease)] = second.URL + "/{package}"
	resolved, err = (&Service{}).githubResolver(catalog).Resolve(context.Background(), "owner/repo")
	if err != nil || resolved != model.ProviderGitHubRelease {
		t.Fatalf("updated resolver = %q, %v", resolved, err)
	}
}
