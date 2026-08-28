package provider

import (
	"context"

	"errors"
	"fmt"

	"strings"

	"net/http"
	"testing"

	"slices"

	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"net/http/httptest"

	"runtime"

	"github.com/eoctet/tendkit/internal/model"
)

func TestPackageProviderContract(t *testing.T) {
	t.Run("github-release-provider-resolves-asset-digest", func(t *testing.T) {
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
	})
	t.Run("github-release-provider-selects-only-one-host-asset-without-action", func(t *testing.T) {
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
	})
	t.Run("github-release-provider-selected-artifact-keeps-url-digest-and-artifact-together", func(t *testing.T) {
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
	})
	t.Run("github-release-provider-falls-back-to-all-named-assets-when-host-cannot-be-inferred", func(t *testing.T) {
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
	})
	t.Run("github-release-provider-rejects-ambiguous-host-assets", func(t *testing.T) {
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
	})
	t.Run("go-provider-lists-and-downloads-selected-host-file", func(t *testing.T) {
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
	})
	t.Run("go-provider-rejects-invalid-selected-file", func(t *testing.T) {
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
	})
	t.Run("go-provider-package-has-no-download-candidates", func(t *testing.T) {
		request := Request{App: model.Application{Type: model.ApplicationTypePackage}}
		implementation := GoProvider{}
		candidates, err := implementation.ArtifactCandidates(context.Background(), request)
		if err != nil || candidates != nil {
			t.Fatalf("package candidates = %#v, %v", candidates, err)
		}
		if _, err := implementation.Download(context.Background(), request); err == nil {
			t.Fatal("package download capability was accepted")
		}
	})
	t.Run("github-release-provider-uses-configured-endpoint", func(t *testing.T) {
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
	})
	t.Run("builtin-downloads-use-official-metadata-without-actions", func(t *testing.T) {
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
	})
	t.Run("trusted-download-url-fails-closed", func(t *testing.T) {
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
	})
	t.Run("automatic-artifact-selection-uses-normalized-linux-system-info", func(t *testing.T) {
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
	})
	t.Run("linux-provider-artifact-selection-uses-injected-system-info", func(t *testing.T) {
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
	})
	t.Run("node-lts-provider-rejects-undeclared-darwin-archive", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"version":"v1.2.3","lts":"LTS","files":["osx-other-tar"]}]`))
		}))
		defer server.Close()
		_, err := (NodeLTSProvider{Source: NewHTTPSource(server.Client()), Endpoint: server.URL}).Download(context.Background(), Request{})
		var typed *Error
		if !errors.As(err, &typed) || typed.Key != "provider.node_download_unavailable" || len(typed.Args) != 1 {
			t.Fatalf("undeclared Node archive error = %#v", err)
		}
	})
	t.Run("builtin-download-rejects-malformed-and-cancelled-responses", func(t *testing.T) {
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
	})
	t.Run("builtin-download-rejects-empty-and-ambiguous-artifacts", func(t *testing.T) {
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
	})
}
