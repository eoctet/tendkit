package updater

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	providerpkg "github.com/eoctet/tendkit/internal/updater/provider"
	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func testChecker(t *testing.T, runner runtimeutil.Runner) *providerResolver {
	t.Helper()
	checker, err := newProviderResolver(runner, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return checker
}

func detectedTestHost(t *testing.T) runtimeutil.SystemInfo {
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

func testGitHubAssetPlatform(info runtimeutil.SystemInfo) string {
	if info.Kernel == "darwin" {
		return "macos"
	}
	return info.Kernel
}

func testGitHubAssetArchitecture(info runtimeutil.SystemInfo) string {
	if info.Architecture == "x86_64" {
		return "x64"
	}
	return "arm64"
}

func TestCheckerCheckActionOverridesBuiltInProvider(t *testing.T) {
	checker := testChecker(t, runtimeutil.Runner{})
	latest, err := checker.latest(context.Background(), model.Application{Provider: model.ProviderConfig{Type: model.ProviderNPM, Actions: &model.ProviderActions{Check: "printf '2.4.1\\n'"}}}, "1.0.0")
	if err != nil || latest != "2.4.1" {
		t.Fatalf("latest=%q err=%v", latest, err)
	}
}

func TestCheckerRegistersBuiltins(t *testing.T) {
	got := testChecker(t, runtimeutil.Runner{}).providerNames()
	want := []string{"github_release", "github_tag", "go", "jetbrains", "node_lts", "npm", "pypi", "sparkle", "uv"}
	if len(got) != len(want) {
		t.Fatalf("providers=%v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("providers=%v want=%v", got, want)
		}
	}
}

func TestCheckerReturnsGoDownloadCandidates(t *testing.T) {
	info := detectedTestHost(t)
	goArch, _ := info.GoArchitecture()
	files := []string{
		"go1.2.3." + info.Kernel + "-" + goArch + ".tar.gz",
		"go1.2.3." + info.Kernel + "-" + goArch + ".zip",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"version":"go1.2.3","stable":true,"files":[{"filename":%q,"os":%q,"arch":%q,"kind":"archive"},{"filename":%q,"os":%q,"arch":%q,"kind":"archive"}]}]`, files[0], info.Kernel, goArch, files[1], info.Kernel, goArch)
	}))
	defer server.Close()
	checker, err := newProviderResolver(runtimeutil.Runner{}, map[string]string{string(model.ProviderGo): server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := checker.downloadAssetCandidates(context.Background(), model.Application{Provider: model.ProviderConfig{Type: model.ProviderGo}})
	if err != nil || candidates.SelectionRequired || !reflect.DeepEqual(candidates.Candidates, files) {
		t.Fatalf("Go candidates = %#v, %v", candidates, err)
	}
}

func TestCheckerLocalizesUnavailableProviderWithoutLosingCause(t *testing.T) {
	_, err := testChecker(t, runtimeutil.Runner{}).latest(context.Background(), model.Application{Provider: model.ProviderConfig{Type: model.ProviderDefault}}, "")
	if !errors.Is(err, providerpkg.ErrUnavailable) {
		t.Fatalf("default provider error does not preserve ErrUnavailable: %v", err)
	}
	var typed *providerpkg.Error
	if !errors.As(err, &typed) || typed.Provider != "default" || typed.Capability != providerpkg.CapabilityLatest {
		t.Fatalf("default provider error does not preserve typed cause: %#v", err)
	}
	if err == nil || err.Error() != i18n.T("provider.unavailable") {
		t.Fatalf("default provider error is not localized: %v", err)
	}
}

func TestCheckerRejectsUnregisteredProvider(t *testing.T) {
	_, err := newProviderResolverWithRegistry(providerpkg.NewRegistry()).latest(context.Background(), model.Application{Provider: model.ProviderConfig{Type: model.ProviderType("missing")}}, "")
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestCheckerActionOverlayDoesNotMaskOtherCapabilities(t *testing.T) {
	registry := providerpkg.NewRegistry()
	if err := registry.Register("fixture", artifactProvider{}); err != nil {
		t.Fatal(err)
	}
	checker := newProviderResolverWithRegistry(registry)
	app := model.Application{Provider: model.ProviderConfig{
		Type: "fixture", Actions: &model.ProviderActions{Check: "printf '9.9.9\\n'"},
	}}
	latest, err := checker.latest(context.Background(), app, "1.0.0")
	if err != nil || latest != "9.9.9" {
		t.Fatalf("check action latest=%q err=%v", latest, err)
	}
	resolved, err := checker.resolveDownload(context.Background(), app, model.ManagedStatus{LatestVersion: latest})
	if err != nil || resolved.Spec.URL != "https://example.invalid/artifact.zip" {
		t.Fatalf("check action masked artifact capability: %#v, %v", resolved, err)
	}
}

func TestCheckerDownloadActionReplacesBuiltinDownload(t *testing.T) {
	registry := providerpkg.NewRegistry()
	if err := registry.Register("fixture", artifactProvider{}); err != nil {
		t.Fatal(err)
	}
	resolved, err := newProviderResolverWithRegistry(registry).resolveDownload(context.Background(), model.Application{Provider: model.ProviderConfig{Type: "fixture", Actions: &model.ProviderActions{Download: &model.Download{URL: "https://example.invalid/action.zip"}}}}, model.ManagedStatus{})
	if err != nil || resolved.Spec.URL != "https://example.invalid/action.zip" || resolved.Spec.Filename != "" || resolved.Artifact != "" {
		t.Fatalf("download action did not replace builtin download: %#v, %v", resolved, err)
	}
}

func TestUVDownloadRequiresAction(t *testing.T) {
	registry := providerpkg.NewRegistry()
	if err := registry.Register(string(model.ProviderUV), providerpkg.UVProvider{}); err != nil {
		t.Fatal(err)
	}
	checker := newProviderResolverWithRegistry(registry)
	app := model.Application{Provider: model.ProviderConfig{Type: model.ProviderUV}, Package: "ruff", Identity: "package:uv:ruff"}
	if _, err := checker.resolveDownload(context.Background(), app, model.ManagedStatus{}); !errors.Is(err, providerpkg.ErrUnavailable) {
		t.Fatalf("UV builtin download error = %v", err)
	}
	app.Provider.Actions = &model.ProviderActions{Download: &model.Download{URL: "https://example.invalid/ruff.whl"}}
	resolved, err := checker.resolveDownload(context.Background(), app, model.ManagedStatus{})
	if err != nil || resolved.Spec.URL != "https://example.invalid/ruff.whl" {
		t.Fatalf("UV action download = %#v, %v", resolved, err)
	}
}

func TestCheckerLocalizesParameterizedDownloadErrorsInBothLanguages(t *testing.T) {
	original := i18n.Current()
	t.Cleanup(func() { i18n.Set(original) })
	for _, language := range []i18n.Language{i18n.English, i18n.Chinese} {
		i18n.Set(language)
		for _, test := range []struct {
			name string
			key  string
			args []any
		}{
			{"untrusted URL", "provider.download_url_untrusted", nil},
			{"pypi candidates", "provider.pypi_sdist_unavailable", []any{2}},
			{"jetbrains link", "provider.jetbrains_download_unavailable", []any{"macM1", 0}},
			{"go arch", "provider.go_download_unavailable", []any{"arm64"}},
			{"node arch", "provider.node_download_unavailable", []any{"arm64"}},
			{"http status", "http.status", []any{"https://example.invalid", 503}},
			{"http request", "http.request_failed", []any{"https://example.invalid"}},
			{"sparkle parse", "provider.sparkle_parse_failed", []any{"https://example.invalid/appcast.xml"}},
			{"current", "provider.current_failed", []any{"Fixture"}},
			{"package update", "provider.package_update_exit", []any{"Fixture", 1, "failed"}},
		} {
			t.Run(string(language)+"/"+test.name, func(t *testing.T) {
				registry := providerpkg.NewRegistry()
				if err := registry.Register("fixture", errorDownloadProvider{err: providerpkg.NewError(test.key, test.args...)}); err != nil {
					t.Fatal(err)
				}
				_, err := newProviderResolverWithRegistry(registry).resolveDownload(context.Background(), model.Application{Provider: model.ProviderConfig{Type: "fixture"}}, model.ManagedStatus{})
				var typed *providerpkg.Error
				if !errors.As(err, &typed) || typed.Key != test.key {
					t.Fatalf("typed error = %#v", err)
				}
				if got := err.Error(); got == test.key || strings.Contains(got, "%!") {
					t.Fatalf("localized error leaked key/format: %q", got)
				}
			})
		}
	}
}

func TestCheckerResolvesBuiltinGitHubDownloadWithoutAction(t *testing.T) {
	info := detectedTestHost(t)
	arch := testGitHubAssetArchitecture(info)
	platform := testGitHubAssetPlatform(info)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"assets":[{"name":"sample-%s-%s.zip","browser_download_url":"https://%s/sample.zip","digest":"sha256:%s"}]}`, platform, arch, request.Host, strings.Repeat("a", 64))
	}))
	defer server.Close()
	checker, err := newProviderResolver(runtimeutil.Runner{}, map[string]string{string(model.ProviderGitHubRelease): server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := checker.resolveDownload(context.Background(), model.Application{Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Package: "owner/repo"}, model.ManagedStatus{LatestVersion: "1.0.0"})
	if err != nil || resolved.Spec.URL != "https://"+strings.TrimPrefix(server.URL, "http://")+"/sample.zip" || !resolved.Spec.ChecksumEnabled || resolved.Spec.ChecksumValue != strings.Repeat("a", 64) {
		t.Fatalf("builtin GitHub download = %#v, %v", resolved, err)
	}
}

func TestCheckerSelectedGitHubArtifactFlowsToDownloadChecksumAndArtifact(t *testing.T) {
	digest := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprintf(w, `{"assets":[{"name":"first.dmg","browser_download_url":"https://%s/first.dmg","digest":"sha256:%s"},{"name":"second.dmg","browser_download_url":"https://%s/second.dmg","digest":"sha256:%s"}]}`, request.Host, strings.Repeat("a", 64), request.Host, digest)
	}))
	defer server.Close()
	checker, err := newProviderResolver(runtimeutil.Runner{}, map[string]string{string(model.ProviderGitHubRelease): server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := model.Application{Package: "owner/repo", Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}}
	before := app
	resolved, err := checker.resolveDownload(context.Background(), app, model.ManagedStatus{LatestVersion: "2.0.0"}, "second.dmg")
	if err != nil || resolved.Spec.URL != "https://"+strings.TrimPrefix(server.URL, "http://")+"/second.dmg" || resolved.Spec.Filename != "second.dmg" || resolved.Spec.ChecksumValue != digest || resolved.Artifact != "second.dmg" {
		t.Fatalf("selected artifact resolution = %#v, %v", resolved, err)
	}
	if !reflect.DeepEqual(app, before) {
		t.Fatalf("selection mutated input application: before=%#v after=%#v", before, app)
	}

	actionApp := app
	actionApp.Provider.Actions = &model.ProviderActions{Download: &model.Download{URL: "https://example.invalid/configured.dmg", Filename: "configured.dmg"}}
	resolved, err = checker.resolveDownload(context.Background(), actionApp, model.ManagedStatus{LatestVersion: "2.0.0"}, "second.dmg")
	if err != nil || resolved.Spec.URL != "https://example.invalid/configured.dmg" {
		t.Fatalf("configured download action did not take precedence: %#v, %v", resolved, err)
	}
}

func TestCheckerResolvesBuiltinSparkleDownloadWithoutAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel><item><enclosure url="https://example.invalid/Sample.dmg" sparkle:shortVersionString="2.0.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"/></item></channel></rss>`))
	}))
	defer server.Close()
	resolved, err := testChecker(t, runtimeutil.Runner{}).resolveDownload(context.Background(), model.Application{Provider: model.ProviderConfig{Type: model.ProviderSparkle}, Package: server.URL}, model.ManagedStatus{LatestVersion: "2.0.0"})
	if err != nil || resolved.Spec.URL != "https://example.invalid/Sample.dmg" {
		t.Fatalf("builtin Sparkle download = %#v, %v", resolved, err)
	}
}

func TestCheckerResolvesOfficialBuiltinDownloadsWithoutActions(t *testing.T) {
	info := detectedTestHost(t)
	jetBrainsKey, _ := info.JetBrainsPlatformKey()
	goArch, _ := info.GoArchitecture()
	nodeFileKey, _ := info.NodeReleaseFileKey()
	nodePlatform := info.NodeArchivePlatform()
	nodeArch, _ := info.NodeArchiveArchitecture()
	goFilename := "go1.2.3." + info.Kernel + "-" + goArch + ".tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/tag/owner/repo":
			_, _ = fmt.Fprintf(w, `[{"name":"v1.2.3","tarball_url":"https://%s/source.tar.gz"}]`, request.Host)
		case "/npm/pkg":
			_, _ = fmt.Fprintf(w, `{"name":"pkg","version":"1.2.3","dist":{"tarball":"https://%s/pkg.tgz"}}`, request.Host)
		case "/pypi/pkg":
			_, _ = fmt.Fprintf(w, `{"info":{"name":"pkg","version":"1.2.3"},"urls":[{"packagetype":"sdist","url":"https://%s/pkg.tar.gz","filename":"pkg.tar.gz"}]}`, request.Host)
		case "/jetbrains":
			_, _ = fmt.Fprintf(w, `{"IIU":[{"version":"1.2.3","downloads":{"%s":{"link":"https://%s/idea.tar.gz"}}}]}`, jetBrainsKey, request.Host)
		case "/go":
			_, _ = fmt.Fprintf(w, `[{"version":"go1.2.3","stable":true,"files":[{"filename":%q,"os":%q,"arch":%q,"kind":"archive"}]}]`, goFilename, info.Kernel, goArch)
		case "/node/index.json":
			_, _ = fmt.Fprintf(w, `[{"version":"v1.2.3","lts":"LTS","files":[%q]}]`, nodeFileKey)
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	}))
	defer server.Close()
	checker, err := newProviderResolver(runtimeutil.Runner{}, map[string]string{
		string(model.ProviderGitHubTag): server.URL + "/tag/{package}", string(model.ProviderNPM): server.URL + "/npm/{package}",
		string(model.ProviderPyPI): server.URL + "/pypi/{package}", string(model.ProviderJetBrains): server.URL + "/jetbrains?code={package}",
		string(model.ProviderGo): server.URL + "/go", string(model.ProviderNodeLTS): server.URL + "/node/index.json",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		provider          model.ProviderType
		packageName, want string
	}{
		{model.ProviderGitHubTag, "owner/repo", "https://" + strings.TrimPrefix(server.URL, "http://") + "/source.tar.gz"}, {model.ProviderNPM, "pkg", "https://" + strings.TrimPrefix(server.URL, "http://") + "/pkg.tgz"},
		{model.ProviderPyPI, "pkg", "https://" + strings.TrimPrefix(server.URL, "http://") + "/pkg.tar.gz"}, {model.ProviderJetBrains, "IIU", "https://" + strings.TrimPrefix(server.URL, "http://") + "/idea.tar.gz"},
		{model.ProviderGo, "", "https://" + strings.TrimPrefix(server.URL, "http://") + "/" + goFilename},
		{model.ProviderNodeLTS, "", "https://" + strings.TrimPrefix(server.URL, "http://") + "/node/v1.2.3/node-v1.2.3-" + nodePlatform + "-" + nodeArch + ".tar.gz"},
	} {
		app := model.Application{Provider: model.ProviderConfig{Type: test.provider}, Package: test.packageName}
		resolved, err := checker.resolveDownload(context.Background(), app, model.ManagedStatus{LatestVersion: "1.2.3"})
		if err != nil || resolved.Spec.URL != test.want {
			t.Fatalf("%s ResolveDownload() = %#v, %v", test.provider, resolved, err)
		}
	}
}

func TestCheckerLocalizesActionFailureWithoutLosingTypedCause(t *testing.T) {
	_, err := testChecker(t, runtimeutil.Runner{}).latest(context.Background(), model.Application{Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{Check: "exit 7"}}}, "1.0.0")
	var typed *providerpkg.Error
	if !errors.As(err, &typed) || typed.Key != "provider.command_exit" || err.Error() != i18n.T("provider.command_exit", 7, "") {
		t.Fatalf("localized action error = %#v", err)
	}
}

func TestCheckerLocalizesGitHubAssetSelectionErrorsWithoutFormatLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[]}`))
	}))
	defer server.Close()
	checker, err := newProviderResolver(runtimeutil.Runner{}, map[string]string{string(model.ProviderGitHubRelease): server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		app  model.Application
		key  string
		args []any
	}{
		{"automatic", model.Application{Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Package: "owner/repo"}, "provider.github_asset_unavailable", []any{runtimeutil.HostPlatform().OS, runtimeutil.HostPlatform().Architecture}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := checker.resolveDownload(context.Background(), test.app, model.ManagedStatus{})
			_ = resolved
			var typed *providerpkg.Error
			if !errors.As(err, &typed) || typed.Key != test.key {
				t.Fatalf("typed asset error = %#v", err)
			}
			if got, want := err.Error(), i18n.T(test.key, test.args...); got != want || strings.Contains(got, "%!MISSING") {
				t.Fatalf("localized asset error = %q, want %q", got, want)
			}
		})
	}
}

func TestCheckerActionOverlaysExecuteConfiguredCapabilities(t *testing.T) {
	directory := t.TempDir()
	updateMarker := filepath.Join(directory, "updated")
	installMarker := filepath.Join(directory, "installed")
	app := model.Application{
		ID: "sample", Name: "Sample", Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{
			Version:  "printf '1.0.0\\n'",
			Check:    "printf 'The latest version: 2.0.0\\n'",
			Update:   "touch " + runtimeutil.QuoteShell(updateMarker),
			Install:  "touch " + runtimeutil.QuoteShell(installMarker),
			Download: &model.Download{URL: "https://example.invalid/sample.zip", Filename: "sample.zip"},
		}},
	}
	checker := testChecker(t, runtimeutil.Runner{IdleTimeout: time.Second})
	current, err := checker.current(context.Background(), app, "")
	if err != nil || !current.FromAction || current.Version != "1.0.0" {
		t.Fatalf("Current() = %#v, %v", current, err)
	}
	latest, err := checker.latest(context.Background(), app, current.Version)
	if err != nil || latest != "2.0.0" {
		t.Fatalf("Latest() = %q, %v", latest, err)
	}
	state := model.ManagedStatus{CurrentVersion: current.Version, LatestVersion: latest}
	if _, err := checker.executeUpdate(context.Background(), app, state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(updateMarker); err != nil {
		t.Fatalf("update action was not executed: %v", err)
	}
	if _, err := checker.executeInstall(context.Background(), app, state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installMarker); err != nil {
		t.Fatalf("install action was not executed: %v", err)
	}
	resolved, err := checker.resolveDownload(context.Background(), app, state)
	if err != nil || resolved.Spec.URL != "https://example.invalid/sample.zip" || resolved.Spec.Filename != "sample.zip" {
		t.Fatalf("ResolveDownload() = %#v, %v", resolved, err)
	}
}

func TestCheckerUnavailableActionCapabilitiesRemainTyped(t *testing.T) {
	checker := testChecker(t, runtimeutil.Runner{})
	app := model.Application{Provider: model.ProviderConfig{Type: model.ProviderDefault}}
	state := model.ManagedStatus{}
	for name, operation := range map[string]func() error{
		"update":   func() error { _, err := checker.executeUpdate(context.Background(), app, state); return err },
		"install":  func() error { _, err := checker.executeInstall(context.Background(), app, state); return err },
		"download": func() error { _, err := checker.resolveDownload(context.Background(), app, state); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, providerpkg.ErrUnavailable) {
				t.Fatalf("unavailable %s error does not preserve ErrUnavailable: %v", name, err)
			}
		})
	}
}

func TestCheckerRejectsInstallActionsForNonDefaultProvider(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "installed")
	checker := testChecker(t, runtimeutil.Runner{IdleTimeout: time.Second})
	app := model.Application{Provider: model.ProviderConfig{Type: model.ProviderNPM, Actions: &model.ProviderActions{Install: "touch " + runtimeutil.QuoteShell(marker)}}}
	_, err := checker.executeInstall(context.Background(), app, model.ManagedStatus{})
	var typed *providerpkg.Error
	if !errors.As(err, &typed) || typed.Provider != "npm" || typed.Capability != providerpkg.CapabilityInstall || !errors.Is(err, providerpkg.ErrUnavailable) {
		t.Fatalf("install error = %#v", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("non-default install action ran: %v", statErr)
	}
}

func TestCheckerCurrentUsesRegisteredCapabilityBeforeFallback(t *testing.T) {
	registry := providerpkg.NewRegistry()
	if err := registry.Register("custom", currentProvider{version: "3.2.1"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := newProviderResolverWithRegistry(registry).current(context.Background(), model.Application{Provider: model.ProviderConfig{Type: "custom"}}, "1.0.0")
	if err != nil || resolved.Version != "3.2.1" || resolved.FromAction {
		t.Fatalf("Current() = %#v, %v", resolved, err)
	}
	resolved, err = testChecker(t, runtimeutil.Runner{}).current(context.Background(), model.Application{Provider: model.ProviderConfig{Type: model.ProviderDefault}}, "1.0.0")
	if !errors.Is(err, providerpkg.ErrUnavailable) || resolved.Version != "1.0.0" || resolved.FromAction {
		t.Fatalf("fallback Current() = %#v, %v", resolved, err)
	}
}

func TestCheckerCurrentReadsTemporaryBundleInfoPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("app bundle version lookup requires macOS plutil")
	}
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "short version", body: `<key>CFBundleShortVersionString</key><string>2.4.6</string><key>CFBundleVersion</key><string>99</string>`, want: "2.4.6"},
		{name: "bundle version fallback", body: `<key>CFBundleVersion</key><string>7.8.9</string>`, want: "7.8.9"},
	} {
		t.Run(test.name, func(t *testing.T) {
			appPath := filepath.Join(t.TempDir(), "Sample.app")
			plistPath := filepath.Join(appPath, "Contents", "Info.plist")
			if err := os.MkdirAll(filepath.Dir(plistPath), 0o700); err != nil {
				t.Fatal(err)
			}
			plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>` + test.body + `</dict></plist>`
			if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
				t.Fatal(err)
			}
			resolved, err := testChecker(t, runtimeutil.Runner{}).current(context.Background(), model.Application{InstallPath: appPath, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}}, "1.0.0")
			if err != nil || resolved.FromAction || resolved.Version != test.want {
				t.Fatalf("Current() = %#v, %v", resolved, err)
			}
		})
	}
}

type currentProvider struct{ version string }

func (provider currentProvider) Current(context.Context, providerpkg.Request) (string, error) {
	return provider.version, nil
}

type artifactProvider struct{}

func (artifactProvider) Latest(context.Context, providerpkg.Request) (string, error) {
	return "1.0.0", nil
}
func (artifactProvider) Download(context.Context, providerpkg.Request) (model.Download, error) {
	return model.Download{URL: "https://example.invalid/artifact.zip", Filename: "artifact.zip"}, nil
}
func (artifactProvider) Artifact(context.Context, providerpkg.Request) (string, error) {
	return "artifact.zip", nil
}

type errorDownloadProvider struct{ err error }

func (p errorDownloadProvider) Download(context.Context, providerpkg.Request) (model.Download, error) {
	return model.Download{}, p.err
}
