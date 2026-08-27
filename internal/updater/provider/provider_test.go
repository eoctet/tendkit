package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	httpx "github.com/eoctet/tendkit/pkg/http"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func detectedProviderTestHost(t *testing.T) runtimeutil.SystemInfo {
	t.Helper()
	info, err := runtimeutil.DetectSystemInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Supported() {
		t.Fatalf("unsupported test host: %#v", info)
	}
	return info
}

func providerTestAssetPlatform(info runtimeutil.SystemInfo) string {
	if info.Kernel == "darwin" {
		return "macos"
	}
	return info.Kernel
}

func providerTestAssetArchitecture(info runtimeutil.SystemInfo) string {
	if info.Architecture == "x86_64" {
		return "x64"
	}
	return "arm64"
}

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type fixedProvider struct{ version string }

func (p fixedProvider) Latest(context.Context, Request) (string, error) { return p.version, nil }

func TestRegistrySupportsCustomProviderAndRejectsDuplicates(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("custom_feed", fixedProvider{version: "3.2.1"}); err != nil {
		t.Fatal(err)
	}
	capabilities, ok := registry.Resolve("CUSTOM_FEED")
	if !ok || capabilities.Latest == nil {
		t.Fatal("custom provider missing")
	}
	if latest, err := capabilities.Latest.Latest(context.Background(), Request{}); err != nil || latest != "3.2.1" {
		t.Fatalf("latest=%q err=%v", latest, err)
	}
	if err := registry.Register(" CUSTOM_FEED ", fixedProvider{}); err == nil {
		t.Fatal("expected duplicate rejection")
	}
}

func TestBuiltinCapabilityMatrix(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry, nil, nil); err != nil {
		t.Fatal(err)
	}
	// This records the implemented subset of the authoritative capability matrix.
	want := map[string]struct{ current, latest, update, download, install, checksum, artifact bool }{
		"github_release": {current: true, latest: true, download: true, checksum: true, artifact: true},
		"github_tag":     {current: true, latest: true, download: true, artifact: true},
		"npm":            {current: true, latest: true, update: true, download: true, artifact: true},
		"pypi":           {current: true, latest: true, update: true, download: true, artifact: true},
		"jetbrains":      {current: true, latest: true, download: true},
		"go":             {current: true, latest: true, update: true, download: true},
		"node_lts":       {current: true, latest: true, download: true},
		"sparkle":        {current: true, latest: true, update: true, download: true, artifact: true},
		"uv":             {current: true, latest: true, update: true},
		"homebrew":       {current: true, latest: true, update: true},
		"cargo":          {current: true},
	}
	for name, expected := range want {
		capabilities, ok := registry.Resolve(name)
		if !ok {
			t.Fatalf("%s was not registered", name)
		}
		got := struct{ current, latest, update, download, install, checksum, artifact bool }{
			capabilities.Current != nil, capabilities.Latest != nil, capabilities.Update != nil,
			capabilities.Download != nil, capabilities.Install != nil, capabilities.Checksum != nil, capabilities.Artifact != nil,
		}
		if got != expected {
			t.Fatalf("%s capabilities = %#v, want %#v", name, got, expected)
		}
	}
}

func TestBuiltinProvidersResolveLatestFromOfflineFixtures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/github-release/owner/repo":
			_, _ = w.Write([]byte(`{"tag_name":"v1.2.3"}`))
		case "/github-tag/owner/repo":
			_, _ = w.Write([]byte(`[{"name":"v2.3.4"}]`))
		case "/npm/example-package":
			_, _ = w.Write([]byte(`{"version":"3.4.5"}`))
		case "/pypi/example-package":
			_, _ = w.Write([]byte(`{"info":{"version":"4.5.6"}}`))
		case "/jetbrains":
			if request.URL.Query().Get("code") != "IIU" {
				t.Errorf("JetBrains product code = %q", request.URL.Query().Get("code"))
			}
			_, _ = w.Write([]byte(`{"IIU":[{"version":"2026.2.1"}]}`))
		case "/go":
			_, _ = w.Write([]byte(`[{"version":"go1.25.0rc1","stable":false},{"version":"go1.24.3","stable":true}]`))
		case "/node":
			_, _ = w.Write([]byte(`[{"version":"v24.0.0","lts":false},{"version":"v22.15.0","lts":"Jod"}]`))
		default:
			t.Errorf("unexpected request path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	registry := NewRegistry()
	endpoints := map[string]string{
		"github_release": server.URL + "/github-release/{package}",
		"github_tag":     server.URL + "/github-tag/{package}",
		"npm":            server.URL + "/npm/{package}",
		"pypi":           server.URL + "/pypi/{package}",
		"jetbrains":      server.URL + "/jetbrains?code={package}",
		"go":             server.URL + "/go",
		"node_lts":       server.URL + "/node",
	}
	if err := RegisterBuiltins(registry, NewHTTPSource(server.Client()), endpoints); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		provider string
		app      model.Application
		want     string
	}{
		{provider: "github_release", app: model.Application{Package: "owner/repo"}, want: "1.2.3"},
		{provider: "github_tag", app: model.Application{Package: "owner/repo"}, want: "2.3.4"},
		{provider: "npm", app: model.Application{Package: "example-package"}, want: "3.4.5"},
		{provider: "pypi", app: model.Application{Package: "example-package"}, want: "4.5.6"},
		{provider: "jetbrains", app: model.Application{Package: "IIU"}, want: "2026.2.1"},
		{provider: "go", want: "1.24.3"},
		{provider: "node_lts", want: "22.15.0"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			capabilities, ok := registry.Resolve(test.provider)
			if !ok || capabilities.Latest == nil {
				t.Fatalf("latest capability missing for %s", test.provider)
			}
			got, err := capabilities.Latest.Latest(context.Background(), Request{App: test.app})
			if err != nil || got != test.want {
				t.Fatalf("Latest() = %q, %v; want %q, nil", got, err, test.want)
			}
		})
	}
}

func TestCapabilityUnavailableCarriesProviderAndCapability(t *testing.T) {
	err := CapabilityUnavailable(" NPM ", CapabilityArtifact)
	var typed *Error
	if !errors.As(err, &typed) || typed.Provider != "npm" || typed.Capability != CapabilityArtifact {
		t.Fatalf("typed unavailable error = %#v", err)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable error does not preserve sentinel: %v", err)
	}
}

func TestPackageDependentProvidersRejectMissingPackageBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider model.ProviderType
		latest   LatestVersioner
	}{
		{"github release", model.ProviderGitHubRelease, GitHubReleaseProvider{}},
		{"github tag", model.ProviderGitHubTag, GitHubTagProvider{}},
		{"npm", model.ProviderNPM, NPMProvider{}},
		{"pypi", model.ProviderPyPI, PyPIProvider{}},
		{"uv", model.ProviderUV, UVProvider{}},
		{"jetbrains", model.ProviderJetBrains, JetBrainsProvider{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.latest.Latest(context.Background(), Request{App: model.Application{
				Name: "Missing", Type: model.ApplicationTypePackage,
				Provider: model.ProviderConfig{Type: test.provider},
			}})
			var typed *Error
			if !errors.As(err, &typed) || typed.Key != "provider.package_required" || typed.Provider != string(test.provider) || typed.Capability != CapabilityLatest {
				t.Fatalf("missing package error = %#v", err)
			}
		})
	}
}

func TestFindVersionUsesDeterministicObjectOrder(t *testing.T) {
	value := map[string]any{
		"z-release": map[string]any{"version": "9.0.0"},
		"a-release": map[string]any{"version": "1.0.0"},
	}
	for range 50 {
		version, err := findVersion(value)
		if err != nil || version != "1.0.0" {
			t.Fatalf("findVersion returned %q, %v", version, err)
		}
	}
}

func TestSparkleProviderSelectsNewestItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel>
<item><enclosure sparkle:shortVersionString="1.9.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"/></item>
<item><enclosure sparkle:shortVersionString="2.0.0-beta1" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"/></item>
<item><enclosure url="https://example.invalid/app-2.zip" sparkle:shortVersionString="2.0.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"/></item>
</channel></rss>`))
	}))
	defer server.Close()
	implementation := SparkleProvider{Source: NewHTTPSource(server.Client())}
	latest, err := implementation.Latest(context.Background(), Request{App: model.Application{Package: server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if latest != "2.0.0" {
		t.Fatalf("latest = %q", latest)
	}
	download, err := implementation.Download(context.Background(), Request{App: model.Application{Package: server.URL}})
	if err != nil || download.URL != "https://example.invalid/app-2.zip" {
		t.Fatalf("unexpected Sparkle artifact %#v, %v", download, err)
	}
}

func TestGitHubReleaseProviderResolvesAssetDigest(t *testing.T) {
	digest := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/owner/repo/releases/latest" {
			t.Fatalf("unexpected request path %q", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"tag_name":"v2.0.0","assets":[{"name":"app-2.0.0.dmg","browser_download_url":"https://%s/app-2.0.0.dmg","digest":"sha256:%s"}]}`, request.Host, digest)
	}))
	defer server.Close()
	provider := GitHubReleaseProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL + "/repos/{package}/releases/latest"}
	for _, test := range []struct {
		name     string
		download model.Download
	}{
		{
			name: "URL filename takes priority over custom local filename",
			download: model.Download{
				URL: "https://example.invalid/app-{last_version}.dmg?source=release", Filename: "custom-name.dmg",
			},
		},
		{
			name: "filename is used when URL has no filename",
			download: model.Download{
				URL: "https://example.invalid/releases/", Filename: "app-{last_version}.dmg",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			download, err := provider.Download(context.Background(), Request{
				App:    model.Application{Package: "owner/repo", Provider: model.ProviderConfig{Actions: &model.ProviderActions{Download: &test.download}}},
				Values: map[string]string{"last_version": "2.0.0"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if download.Filename != "app-2.0.0.dmg" || download.ChecksumValue != "" {
				t.Fatalf("unexpected GitHub download %#v", download)
			}
			checksum, err := provider.Checksum(context.Background(), Request{
				App:    model.Application{Package: "owner/repo", Provider: model.ProviderConfig{Actions: &model.ProviderActions{Download: &test.download}}},
				Values: map[string]string{"last_version": "2.0.0"},
			})
			if err != nil || checksum != digest {
				t.Fatalf("unexpected GitHub checksum %q, %v", checksum, err)
			}
			artifact, err := provider.Artifact(context.Background(), Request{
				App:    model.Application{Package: "owner/repo", Provider: model.ProviderConfig{Actions: &model.ProviderActions{Download: &test.download}}},
				Values: map[string]string{"last_version": "2.0.0"},
			})
			if err != nil || artifact != "app-2.0.0.dmg" {
				t.Fatalf("unexpected GitHub artifact %q, %v", artifact, err)
			}
		})
	}
}

func TestGitHubReleaseProviderSelectsOnlyOneHostAssetWithoutAction(t *testing.T) {
	info := detectedProviderTestHost(t)
	arch := providerTestAssetArchitecture(info)
	platform := providerTestAssetPlatform(info)
	hostAsset := "sample-" + platform + "-" + arch + ".tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"assets":[{"name":%q,"browser_download_url":"https://%s/sample.tar.gz"},{"name":"sample-windows-%s.zip","browser_download_url":"https://%s/sample.exe"}]}`, hostAsset, request.Host, arch, request.Host)
	}))
	defer server.Close()
	implementation := GitHubReleaseProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL, host: func() runtimeutil.SystemInfo { return info }}
	request := Request{App: model.Application{Package: "owner/repo"}}
	candidates, err := implementation.ArtifactChoices(context.Background(), request)
	if err != nil || candidates.SelectionRequired || !slices.Equal(candidates.Candidates, []string{hostAsset}) {
		t.Fatalf("host candidates = %#v, %v", candidates, err)
	}
	download, err := implementation.Download(context.Background(), request)
	if err != nil || download.URL != "https://"+requestHost(server.URL)+"/sample.tar.gz" {
		t.Fatalf("automatic GitHub asset selection = %#v, %v", download, err)
	}
	request.SelectedArtifact = "sample-windows-" + arch + ".zip"
	if _, err := implementation.Download(context.Background(), request); err == nil {
		t.Fatal("non-host artifact bypassed inferred candidate boundary")
	}
}

func TestGitHubReleaseProviderSelectedArtifactKeepsURLDigestAndArtifactTogether(t *testing.T) {
	digest := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprintf(w, `{"assets":[{"name":"first.dmg","browser_download_url":"https://%s/first.dmg","digest":"sha256:%s"},{"name":"second.dmg","browser_download_url":"https://%s/second.dmg","digest":"sha256:%s"}]}`, request.Host, strings.Repeat("a", 64), request.Host, digest)
	}))
	defer server.Close()
	provider := GitHubReleaseProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL}
	request := Request{App: model.Application{Package: "owner/repo"}, SelectedArtifact: "second.dmg"}
	download, err := provider.Download(context.Background(), request)
	if err != nil || download.URL != "https://"+requestHost(server.URL)+"/second.dmg" || download.Filename != "second.dmg" {
		t.Fatalf("download=%#v err=%v", download, err)
	}
	checksum, err := provider.Checksum(context.Background(), request)
	if err != nil || checksum != digest {
		t.Fatalf("checksum=%q err=%v", checksum, err)
	}
	artifact, err := provider.Artifact(context.Background(), request)
	if err != nil || artifact != "second.dmg" {
		t.Fatalf("artifact=%q err=%v", artifact, err)
	}
	if _, err := provider.Download(context.Background(), Request{App: request.App, SelectedArtifact: "missing.dmg"}); err == nil {
		t.Fatal("invalid selected artifact accepted")
	}
}

func TestGitHubReleaseProviderFallsBackToAllNamedAssetsWhenHostCannotBeInferred(t *testing.T) {
	digest := strings.Repeat("c", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprintf(w, `{"assets":[{"name":"opaque-package-b.payload","browser_download_url":"https://%s/b.payload","digest":"sha256:%s"},{"name":"","browser_download_url":"https://%s/unnamed"},{"name":"opaque-package-a.payload","browser_download_url":"https://%s/a.payload"}]}`, request.Host, digest, request.Host, request.Host)
	}))
	defer server.Close()
	provider := GitHubReleaseProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL}
	request := Request{App: model.Application{Package: "owner/repo"}}
	candidates, err := provider.ArtifactChoices(context.Background(), request)
	if err != nil || !candidates.SelectionRequired || !slices.Equal(candidates.Candidates, []string{"opaque-package-a.payload", "opaque-package-b.payload"}) {
		t.Fatalf("fallback candidates = %#v, %v", candidates, err)
	}
	request.SelectedArtifact = "opaque-package-b.payload"
	download, err := provider.Download(context.Background(), request)
	if err != nil || download.URL != "https://"+requestHost(server.URL)+"/b.payload" || download.Filename != "b.payload" {
		t.Fatalf("fallback download = %#v, %v", download, err)
	}
	checksum, err := provider.Checksum(context.Background(), request)
	if err != nil || checksum != digest {
		t.Fatalf("fallback checksum = %q, %v", checksum, err)
	}
	artifact, err := provider.Artifact(context.Background(), request)
	if err != nil || artifact != "opaque-package-b.payload" {
		t.Fatalf("fallback artifact = %q, %v", artifact, err)
	}
}

func TestGitHubReleaseProviderRejectsAmbiguousHostAssets(t *testing.T) {
	info := detectedProviderTestHost(t)
	arch := providerTestAssetArchitecture(info)
	platform := providerTestAssetPlatform(info)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"assets":[{"name":"sample-%s-%s.zip","browser_download_url":"https://example.invalid/one.zip"},{"name":"other-%s-%s.tar.gz","browser_download_url":"https://example.invalid/two.tar.gz"}]}`, platform, arch, platform, arch)
	}))
	defer server.Close()
	_, err := (GitHubReleaseProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL, host: func() runtimeutil.SystemInfo { return info }}).Download(context.Background(), Request{App: model.Application{Package: "owner/repo"}})
	var typed *Error
	if !errors.As(err, &typed) || typed.Key != "provider.github_asset_ambiguous" {
		t.Fatalf("ambiguous automatic selection error = %#v", err)
	}
}

func TestGoProviderListsAndDownloadsSelectedHostFile(t *testing.T) {
	info := detectedProviderTestHost(t)
	goArch, _ := info.GoArchitecture()
	files := []string{
		"go1.2.3." + info.Kernel + "-" + goArch + ".tar.gz",
		"go1.2.3." + info.Kernel + "-" + goArch + ".zip",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"version":"go1.2.3","stable":true,"files":[
		{"filename":%q,"os":%q,"arch":%q,"kind":"archive"},
		{"filename":%q,"os":%q,"arch":%q,"kind":"archive"}
		]}]`, files[0], info.Kernel, goArch, files[1], info.Kernel, goArch)
	}))
	defer server.Close()
	implementation := GoProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL + "/?mode=json", host: func() runtimeutil.SystemInfo { return info }}

	candidates, err := implementation.ArtifactCandidates(context.Background(), Request{})
	if err != nil || !slices.Equal(candidates, files) {
		t.Fatalf("ArtifactCandidates() = %#v, %v", candidates, err)
	}
	download, err := implementation.Download(context.Background(), Request{SelectedArtifact: files[1]})
	wantURL := strings.Replace(server.URL, "http://", "https://", 1) + "/" + files[1]
	if err != nil || download.URL != wantURL || download.Filename != files[1] {
		t.Fatalf("Download(selected) = %#v, %v; want URL %q", download, err, wantURL)
	}
}

func TestGoProviderRejectsInvalidSelectedFile(t *testing.T) {
	info := detectedProviderTestHost(t)
	goArch, _ := info.GoArchitecture()
	otherKernel := "linux"
	if info.Kernel == "linux" {
		otherKernel = "darwin"
	}
	hostFilename := "go1.2.3." + info.Kernel + "-" + goArch + ".tar.gz"
	otherFilename := "go1.2.3." + otherKernel + "-" + goArch + ".tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"version":"go1.2.3","stable":true,"files":[{"filename":%q,"os":%q,"arch":%q,"kind":"archive"},{"filename":%q,"os":%q,"arch":%q,"kind":"archive"}]}]`, hostFilename, info.Kernel, goArch, otherFilename, otherKernel, goArch)
	}))
	defer server.Close()
	implementation := GoProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL, host: func() runtimeutil.SystemInfo { return info }}
	for _, selected := range []string{"missing.pkg", "   ", otherFilename, " " + hostFilename + " "} {
		if _, err := implementation.Download(context.Background(), Request{SelectedArtifact: selected}); err == nil {
			t.Fatalf("selected file %q was accepted", selected)
		}
	}
}

func TestGoProviderPackageHasNoDownloadCandidates(t *testing.T) {
	request := Request{App: model.Application{Type: model.ApplicationTypePackage}}
	implementation := GoProvider{}
	candidates, err := implementation.ArtifactCandidates(context.Background(), request)
	if err != nil || candidates != nil {
		t.Fatalf("package candidates = %#v, %v", candidates, err)
	}
	if _, err := implementation.Download(context.Background(), request); err == nil {
		t.Fatal("package download capability was accepted")
	}
}

func TestHTTPSourceRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseSize+1)))
	}))
	defer server.Close()
	_, err := NewHTTPSource(server.Client()).Get(context.Background(), server.URL, "text/plain")
	if err == nil || !strings.Contains(err.Error(), "8388608") {
		t.Fatalf("expected response size error, got %v", err)
	}
}

func TestHTTPSourceLimitsConcurrencyPerHost(t *testing.T) {
	const requests = 4
	entered := make(chan struct{}, requests)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	source := NewHTTPSource(server.Client(), httpx.HTTPOptions{
		Timeout:               time.Second,
		MaxConcurrencyPerHost: 1,
		Retries:               0,
	})
	var group sync.WaitGroup
	errorsByRequest := make(chan error, requests)
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := source.Get(context.Background(), server.URL, "text/plain")
			errorsByRequest <- err
		}()
	}
	<-entered
	select {
	case <-entered:
		close(release)
		group.Wait()
		t.Fatal("multiple requests to the same host ran concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	group.Wait()
	close(errorsByRequest)
	for err := range errorsByRequest {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestHTTPSourceRetriesTransientStatus(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	source := NewHTTPSource(server.Client(), httpx.HTTPOptions{
		Timeout:               time.Second,
		MaxConcurrencyPerHost: 1,
		Retries:               2,
		RetryDelay:            time.Millisecond,
	})
	body, err := source.Get(context.Background(), server.URL, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" || attempts.Load() != 3 {
		t.Fatalf("body = %q, attempts = %d", body, attempts.Load())
	}
}

func TestHTTPSourceDoesNotRetryPermanentStatus(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	source := NewHTTPSource(server.Client(), httpx.HTTPOptions{
		Timeout:               time.Second,
		MaxConcurrencyPerHost: 1,
		Retries:               2,
	})
	_, err := source.Get(context.Background(), server.URL, "text/plain")
	if err == nil || attempts.Load() != 1 {
		t.Fatalf("error = %v, attempts = %d", err, attempts.Load())
	}
}

func TestRetryableHTTPErrorIncludesClientTimeout(t *testing.T) {
	err := &url.Error{Op: http.MethodGet, URL: "https://example.invalid", Err: context.DeadlineExceeded}
	if !httpx.IsRetryableHTTPError(err) {
		t.Fatalf("client timeout should be retryable: %v", err)
	}
}

func TestHTTPSourceHostLimitWaitHonorsCancellation(t *testing.T) {
	source := NewHTTPSource(nil, httpx.HTTPOptions{Timeout: time.Second, MaxConcurrencyPerHost: 1})
	release, err := source.source.Acquire(context.Background(), "example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := source.source.Acquire(ctx, "example.invalid"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline while waiting for host gate, got %v", err)
	}
}

func TestGitHubReleaseProviderUsesConfiguredEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/owner/project/latest" {
			t.Errorf("unexpected configured endpoint path %q", request.URL.Path)
		}
		_, _ = w.Write([]byte(`{"tag_name":"v4.5.6"}`))
	}))
	defer server.Close()

	implementation := GitHubReleaseProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL + "/{package}/latest"}
	latest, err := implementation.Latest(context.Background(), Request{App: model.Application{Package: "owner/project"}})
	if err != nil {
		t.Fatal(err)
	}
	if latest != "4.5.6" {
		t.Fatalf("unexpected version %q", latest)
	}
}

func TestGitHubRequestUsesTokenFromProcessEnvironment(t *testing.T) {
	const token = "test-token-not-a-secret"
	t.Setenv("GITHUB_TOKEN", token)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer "+token {
			t.Errorf("Authorization = %q", authorization)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	source := NewHTTPSource(client)
	if _, err := source.Get(context.Background(), "https://"+githubAPIHost+"/test", "application/json"); err != nil {
		t.Fatal(err)
	}
}

func TestJetBrainsResponseUsesProductCodeAndRequiresRelease(t *testing.T) {
	for _, test := range []struct {
		name    string
		data    map[string][]jetBrainsRelease
		want    string
		wantErr bool
	}{
		{name: "matching product", data: map[string][]jetBrainsRelease{"IIU": {{Version: "2026.2.1"}}}, want: "2026.2.1"},
		{name: "missing release", data: map[string][]jetBrainsRelease{"IIU": {}}, wantErr: true},
		{name: "wrong product code", data: map[string][]jetBrainsRelease{"WRONG": {{Version: "1.2.3"}}}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			latest, err := jetBrainsVersion(test.data, "IIU")
			if (err != nil) != test.wantErr || latest != test.want {
				t.Fatalf("jetBrainsVersion() = %q, %v; want %q, error=%t", latest, err, test.want, test.wantErr)
			}
		})
	}
}

func TestBuiltinDownloadsUseOfficialMetadataWithoutActions(t *testing.T) {
	info := detectedProviderTestHost(t)
	jetBrainsKey, _ := info.JetBrainsPlatformKey()
	goArch, _ := info.GoArchitecture()
	nodeFileKey, _ := info.NodeReleaseFileKey()
	nodePlatform := info.NodeArchivePlatform()
	nodeArch, _ := info.NodeArchiveArchitecture()
	goFilename := "go1.2.3." + info.Kernel + "-" + goArch + ".tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tag/owner/repo":
			_, _ = fmt.Fprintf(w, `[{"name":"v1.2.3","tarball_url":"https://%s/repo-v1.2.3.tar.gz"}]`, r.Host)
		case "/npm/pkg":
			_, _ = fmt.Fprintf(w, `{"name":"pkg","version":"1.2.3","dist":{"tarball":"https://%s/pkg-1.2.3.tgz"}}`, r.Host)
		case "/npm/%40scope%2Fpkg":
			_, _ = fmt.Fprintf(w, `{"name":"@scope/pkg","version":"1.2.3","dist":{"tarball":"https://%s/pkg-1.2.3.tgz"}}`, r.Host)
		case "/pypi/pkg":
			_, _ = fmt.Fprintf(w, `{"info":{"name":"Py__.-Pkg","version":"1.2.3"},"urls":[{"packagetype":"sdist","url":"https://%s/pkg-1.2.3.tar.gz","filename":"pkg-1.2.3.tar.gz"}]}`, r.Host)
		case "/jetbrains":
			_, _ = fmt.Fprintf(w, `{"IIU":[{"version":"1.2.3","downloads":{"%s":{"link":"https://%s/idea.tar.gz"}}}]}`, jetBrainsKey, r.Host)
		case "/go":
			_, _ = fmt.Fprintf(w, `[{"version":"go1.2.3","stable":true,"files":[{"filename":%q,"os":%q,"arch":%q,"kind":"archive"}]}]`, goFilename, info.Kernel, goArch)
		case "/node/index.json":
			_, _ = fmt.Fprintf(w, `[{"version":"v1.2.3","lts":"LTS","files":[%q]}]`, nodeFileKey)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	source := NewHTTPSource(server.Client())
	for _, test := range []struct {
		name                  string
		resolver              DownloadResolver
		artifact              ArtifactProvider
		request               Request
		wantURL, wantArtifact string
	}{
		{"github tag", GitHubTagProvider{source, server.URL + "/tag/{package}"}, GitHubTagProvider{source, server.URL + "/tag/{package}"}, Request{App: model.Application{Package: "owner/repo"}}, strings.Replace(server.URL, "http://", "https://", 1) + "/repo-v1.2.3.tar.gz", "v1.2.3"},
		{"npm", NPMProvider{source, server.URL + "/npm/{package}"}, NPMProvider{source, server.URL + "/npm/{package}"}, Request{App: model.Application{Package: "pkg"}}, strings.Replace(server.URL, "http://", "https://", 1) + "/pkg-1.2.3.tgz", "pkg@1.2.3"},
		{"pypi", PyPIProvider{source, server.URL + "/pypi/{package}"}, PyPIProvider{source, server.URL + "/pypi/{package}"}, Request{App: model.Application{Package: "pkg"}}, strings.Replace(server.URL, "http://", "https://", 1) + "/pkg-1.2.3.tar.gz", "py-pkg"},
		{"jetbrains", JetBrainsProvider{Source: source, Endpoint: server.URL + "/jetbrains?code={package}", host: func() runtimeutil.SystemInfo { return info }}, nil, Request{App: model.Application{Package: "IIU"}}, strings.Replace(server.URL, "http://", "https://", 1) + "/idea.tar.gz", ""},
		{"go", GoProvider{Source: source, Endpoint: server.URL + "/go", host: func() runtimeutil.SystemInfo { return info }}, nil, Request{}, strings.Replace(server.URL, "http://", "https://", 1) + "/" + goFilename, ""},
		{"node", NodeLTSProvider{Source: source, Endpoint: server.URL + "/node/index.json", host: func() runtimeutil.SystemInfo { return info }}, nil, Request{}, strings.Replace(server.URL, "http://", "https://", 1) + "/node/v1.2.3/node-v1.2.3-" + nodePlatform + "-" + nodeArch + ".tar.gz", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			download, err := test.resolver.Download(context.Background(), test.request)
			if err != nil || download.URL != test.wantURL {
				t.Fatalf("Download() = %#v, %v", download, err)
			}
			if test.name == "node" {
				wantFilename := "node-v1.2.3-" + nodePlatform + "-" + nodeArch + ".tar.gz"
				if download.Filename != wantFilename {
					t.Fatalf("node filename = %q, want %q", download.Filename, wantFilename)
				}
			}
			if test.name == "github tag" || test.name == "npm" {
				if download.Filename == "" {
					t.Fatalf("%s Download returned no filename", test.name)
				}
			}
			if test.artifact != nil {
				artifact, err := test.artifact.Artifact(context.Background(), test.request)
				if err != nil || artifact != test.wantArtifact {
					t.Fatalf("Artifact() = %q, %v", artifact, err)
				}
			}
		})
	}
}

func TestNPMProviderEscapesScopedPackagePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.RawPath != "/npm/@scope%2Fpkg" {
			t.Fatalf("raw npm path = %q", request.URL.RawPath)
		}
		_, _ = fmt.Fprintf(w, `{"name":"@scope/pkg","version":"1.2.3","dist":{"tarball":"https://%s/pkg.tgz"}}`, request.Host)
	}))
	defer server.Close()
	_, err := (NPMProvider{NewHTTPSource(server.Client()), server.URL + "/npm/{package}"}).Download(context.Background(), Request{App: model.Application{Package: "@scope/pkg"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTrustedDownloadURLFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, candidate string
		valid                     bool
	}{
		{"same host", "https://registry.npmjs.org/pkg", "https://registry.npmjs.org/pkg.tgz", true},
		{"pypi official cdn", "https://pypi.org/pypi/pkg/json", "https://files.pythonhosted.org/pkg.tgz", true},
		{"jetbrains official cdn", "https://data.services.jetbrains.com/products/releases", "https://download.jetbrains.com/idea.dmg", true},
		{"github official host", "https://api.github.com/repos/o/r/tags", "https://github.com/o/r/tarball/v1", true},
		{"http", "https://registry.npmjs.org/pkg", "http://registry.npmjs.org/pkg.tgz", false},
		{"untrusted host", "https://registry.npmjs.org/pkg", "https://example.invalid/pkg.tgz", false},
		{"custom endpoint", "https://packages.example.test/pkg", "https://cdn.example.test/pkg.tgz", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := trustedDownloadURL(test.endpoint, test.candidate)
			if (err == nil) != test.valid {
				t.Fatalf("trustedDownloadURL(%q, %q) error = %v", test.endpoint, test.candidate, err)
			}
		})
	}
}

func requestHost(rawURL string) string { return strings.TrimPrefix(rawURL, "http://") }

func TestAutomaticArtifactSelectionUsesNormalizedLinuxSystemInfo(t *testing.T) {
	ubuntu := runtimeutil.SystemInfo{OS: "linux", Product: "Ubuntu", Architecture: "x86_64"}
	rhel := runtimeutil.SystemInfo{OS: "linux", Product: "Red Hat", Architecture: "arm64"}
	if !githubAssetMatchesHost("tool-linux-x86_64.deb", ubuntu) || githubAssetMatchesHost("tool-linux-x86_64.rpm", ubuntu) {
		t.Fatal("Ubuntu package matching did not enforce deb distribution boundary")
	}
	if !githubAssetMatchesHost("tool-linux-arm64.rpm", rhel) || githubAssetMatchesHost("tool-linux-arm64.deb", rhel) {
		t.Fatal("Red Hat package matching did not enforce rpm distribution boundary")
	}
	if githubAssetMatchesHost("tool-macos-arm64.zip", rhel) || githubAssetMatchesHost("tool-windows-arm64.zip", rhel) {
		t.Fatal("Linux selection accepted a non-Linux artifact")
	}
	if githubAssetMatchesHost("tool-linux-x86_64.tar.gz", runtimeutil.SystemInfo{OS: "linux", Product: "Fedora", Architecture: "x86_64"}) {
		t.Fatal("unsupported Linux distribution selected an artifact")
	}
}

func TestLinuxProviderArtifactSelectionUsesInjectedSystemInfo(t *testing.T) {
	for _, test := range []struct {
		name, architecture, goArch, nodeArch, nodeKey, jetKey string
	}{
		{"x86_64", "x86_64", "amd64", "x64", "linux-x64-tar", "linux"},
		{"arm64", "arm64", "arm64", "arm64", "linux-arm64-tar", "linuxARM64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := runtimeutil.SystemInfo{OS: "linux", Product: "Ubuntu", Architecture: test.architecture}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/node/index.json":
					_, _ = fmt.Fprintf(w, `[{"version":"v1.2.3","lts":"LTS","files":[%q]}]`, test.nodeKey)
				case "/jetbrains":
					_, _ = fmt.Fprintf(w, `{"IIU":[{"version":"2026.1","downloads":{%q:{"link":"https://%s/idea.tar.gz"}}}]}`, test.jetKey, request.Host)
				}
			}))
			defer server.Close()
			host := func() runtimeutil.SystemInfo { return info }
			node, err := (NodeLTSProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL + "/node/index.json", host: host}).Download(context.Background(), Request{})
			if err != nil || !strings.HasSuffix(node.URL, "node-v1.2.3-linux-"+test.nodeArch+".tar.gz") {
				t.Fatalf("Node Linux download = %#v, %v", node, err)
			}
			jetbrains, err := (JetBrainsProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL + "/jetbrains?code={package}", host: host}).Download(context.Background(), Request{App: model.Application{Package: "IIU"}})
			if err != nil || !strings.HasSuffix(jetbrains.URL, "/idea.tar.gz") {
				t.Fatalf("JetBrains Linux download = %#v, %v", jetbrains, err)
			}
			files := goHostFiles([]goFile{{Filename: "go.linux", OS: "linux", Arch: test.goArch}}, info)
			if len(files) != 1 {
				t.Fatalf("Go Linux files = %#v", files)
			}
		})
	}
}

func TestNodeLTSProviderRejectsUndeclaredDarwinArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"version":"v1.2.3","lts":"LTS","files":["osx-other-tar"]}]`))
	}))
	defer server.Close()
	_, err := (NodeLTSProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL}).Download(context.Background(), Request{})
	var typed *Error
	if !errors.As(err, &typed) || typed.Key != "provider.node_download_unavailable" || len(typed.Args) != 1 {
		t.Fatalf("undeclared Node archive error = %#v", err)
	}
}

func TestBuiltinDownloadRejectsMalformedAndCancelledResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{`)) }))
	defer server.Close()
	source := NewHTTPSource(server.Client())
	resolvers := []struct {
		name     string
		resolver DownloadResolver
		request  Request
	}{
		{"github_tag", GitHubTagProvider{source, server.URL}, Request{App: model.Application{Package: "owner/repo"}}}, {"npm", NPMProvider{source, server.URL}, Request{App: model.Application{Package: "pkg"}}}, {"pypi", PyPIProvider{source, server.URL}, Request{App: model.Application{Package: "pkg"}}}, {"jetbrains", JetBrainsProvider{Source: source, Endpoint: server.URL}, Request{App: model.Application{Package: "IIU"}}}, {"go", GoProvider{Source: source, Endpoint: server.URL}, Request{}}, {"node", NodeLTSProvider{Source: source, Endpoint: server.URL}, Request{}},
	}
	for _, test := range resolvers {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.resolver.Download(context.Background(), test.request); err == nil {
				t.Fatal("malformed response was accepted")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := test.resolver.Download(ctx, test.request); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled Download error = %v", err)
			}
		})
	}
}

func TestHTTPAndSparkleFailuresAreTypedAndPreserveCauses(t *testing.T) {
	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{`))
	}))
	defer malformed.Close()
	var target map[string]any
	err := NewHTTPSource(malformed.Client()).GetJSON(context.Background(), malformed.URL, &target)
	var typed *Error
	var syntax *json.SyntaxError
	if !errors.As(err, &typed) || typed.Key != "http.parse_failed" || !errors.As(err, &syntax) {
		t.Fatalf("malformed JSON error = %#v", err)
	}

	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer status.Close()
	source := NewHTTPSource(status.Client(), httpx.HTTPOptions{Retries: 0})
	_, err = source.Get(context.Background(), status.URL, "application/json")
	var statusCause *httpx.HTTPStatusError
	if !errors.As(err, &typed) || typed.Key != "http.status" || !errors.As(err, &statusCause) {
		t.Fatalf("HTTP status error = %#v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = source.Get(ctx, status.URL, "application/json")
	if !errors.As(err, &typed) || typed.Key != "http.request_failed" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled HTTP error = %#v", err)
	}

	_, err = (SparkleProvider{Source: NewHTTPSource(malformed.Client())}).Latest(context.Background(), Request{App: model.Application{Package: malformed.URL}})
	if !errors.As(err, &typed) || typed.Key != "provider.sparkle_parse_failed" {
		t.Fatalf("malformed Sparkle error = %#v", err)
	}
}

func TestBuiltinDownloadRejectsEmptyAndAmbiguousArtifacts(t *testing.T) {
	for _, test := range []struct {
		name, body string
		resolver   func(*HTTPSource, string) DownloadResolver
		request    Request
	}{
		{"github tag", `[]`, func(s *HTTPSource, endpoint string) DownloadResolver { return GitHubTagProvider{s, endpoint} }, Request{App: model.Application{Package: "owner/repo"}}},
		{"npm", `{}`, func(s *HTTPSource, endpoint string) DownloadResolver { return NPMProvider{s, endpoint} }, Request{App: model.Application{Package: "pkg"}}},
		{"pypi", `{"info":{"name":"pkg"},"urls":[{"packagetype":"sdist","url":"https://example.invalid/a"},{"packagetype":"sdist","url":"https://example.invalid/b"}]}`, func(s *HTTPSource, endpoint string) DownloadResolver { return PyPIProvider{s, endpoint} }, Request{App: model.Application{Package: "pkg"}}},
		{"jetbrains", `{"IIU":[{"downloads":{"mac":{"link":"https://example.invalid/a"}}},{"downloads":{"mac":{"link":"https://example.invalid/b"}}}]}`, func(s *HTTPSource, endpoint string) DownloadResolver {
			return JetBrainsProvider{Source: s, Endpoint: endpoint}
		}, Request{App: model.Application{Package: "IIU"}}},
		{"go", `[{"stable":true,"files":[{"filename":"a.pkg","os":"darwin","arch":"` + runtime.GOARCH + `","kind":"installer"},{"filename":"b.pkg","os":"darwin","arch":"` + runtime.GOARCH + `","kind":"installer"}]}]`, func(s *HTTPSource, endpoint string) DownloadResolver {
			return GoProvider{Source: s, Endpoint: endpoint}
		}, Request{}},
		{"node", `[]`, func(s *HTTPSource, endpoint string) DownloadResolver {
			return NodeLTSProvider{Source: s, Endpoint: endpoint}
		}, Request{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			if _, err := test.resolver(NewHTTPSource(server.Client()), server.URL).Download(context.Background(), test.request); err == nil {
				t.Fatal("empty or ambiguous artifact was accepted")
			}
		})
	}
}
