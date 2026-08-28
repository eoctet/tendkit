package provider

import (
	"errors"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"

	"context"
	"path/filepath"
	"runtime"

	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	"os"
)

func detectedProviderTestHost(

	// These assignments keep the standard Provider contract honest at compile time.
	t *testing.T) runtimeutil.SystemInfo {
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

type fixedProvider struct{ version string }

func (p fixedProvider) Latest(context.Context, Request) (string, error) { return p.version, nil }

type uvLatestRunner struct {
	command string
	env     map[string]string
	result  runtimeutil.Result
	err     error
}

type metadataContractRunner struct {
	responses map[string]runtimeutil.Result
	calls     []string
}

func (r *metadataContractRunner) Run(_ context.Context, command string, _ map[string]string) (runtimeutil.Result, error) {
	r.calls = append(r.calls, command)
	return r.responses[command], nil
}

func (r *uvLatestRunner) Run(_ context.Context, command string, environment map[string]string) (runtimeutil.Result, error) {
	r.command, r.env = command, environment
	return r.result, r.err
}

func requestHost(rawURL string) string { return strings.TrimPrefix(rawURL, "http://") }
func TestProviderRegistryContract(t *testing.T) {
	t.Run("local-metadata-uses-conventional-cli-fallback-and-package-protocols", func(t *testing.T) {
		runner := &metadataContractRunner{responses: map[string]runtimeutil.Result{
			"/tool --version": {ExitCode: 1}, "/tool version": {ExitCode: 1}, "/tool -v": {Stdout: "tool v3.2.1"},
		}}
		value, err := metadatautil.DetectCLIVersion(context.Background(), runner, "/tool", nil)
		if err != nil || value != "3.2.1" || strings.Join(runner.calls, ",") != "/tool --version,/tool version,/tool -v" {
			t.Fatalf("CLI fallback value=%q calls=%q err=%v", value, runner.calls, err)
		}
		for _, test := range []struct {
			ecosystem       metadatautil.PackageEcosystem
			version, update string
			user            bool
		}{
			{metadatautil.PackagePython, "/python -c 'import importlib.metadata as metadata, sys; print(metadata.version(sys.argv[1]))' tool", "/python -m pip install --user --upgrade tool", true},
			{metadatautil.PackageRuby, "/gem list --local --exact tool", "/gem install --user-install --no-document tool", true},
		} {
			target := metadatautil.PackageTarget{Ecosystem: test.ecosystem, Manager: map[bool]string{true: "/python", false: "/gem"}[test.ecosystem == metadatautil.PackagePython], Name: "tool", UserInstall: test.user}
			versionCommand, versionErr := metadatautil.PackageVersionCommand(target)
			updateCommand, updateErr := metadatautil.PackageUpdateCommand(target)
			if versionErr != nil || updateErr != nil || versionCommand != test.version || updateCommand != test.update {
				t.Fatalf("%s commands=%q,%q errors=%v,%v", test.ecosystem, versionCommand, updateCommand, versionErr, updateErr)
			}
		}
	})
	t.Run("cargo-current-uses-one-resolved-manager-and-fails-closed-for-ownership", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "bin", "sample")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		provider := CargoProvider{Runner: &recordingProviderRunner{responses: []providerRunnerResponse{
			{result: runtimeutil.Result{Stdout: "sample v1.2.3:\n    sample\n"}},
			{result: runtimeutil.Result{Stdout: "sample v1.2.3:\n    sample\n"}},
		}}, lookup: func(string, map[string]string) (string, error) { return "/fixture/bin/cargo", nil }}
		request := Request{App: model.Application{Name: "Sample", Package: "sample", InstallPath: path, Environment: map[string]string{"CARGO_INSTALL_ROOT": root}, Provider: model.ProviderConfig{Type: model.ProviderCargo}}}
		current, err := provider.Current(context.Background(), request)
		if err != nil || current != "1.2.3" {
			t.Fatalf("cargo current=%q err=%v", current, err)
		}
		request.App.InstallPath = filepath.Join(root, "other", "sample")
		if err := os.MkdirAll(filepath.Dir(request.App.InstallPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(request.App.InstallPath, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err = provider.Current(context.Background(), request)
		var typed *Error
		if !errors.As(err, &typed) || typed.Key != "provider.target_conflict" || typed.Capability != CapabilityCurrent {
			t.Fatalf("cargo ownership error=%#v", err)
		}
	})
	t.Run("uv-latest-uses-package-context-and-preserves-cancellation", func(t *testing.T) {
		directory := t.TempDir()
		uv := filepath.Join(directory, "uv")
		if err := os.WriteFile(uv, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		runner := &uvLatestRunner{result: runtimeutil.Result{Stdout: "ruff v0.8.0 [latest: 0.9.1]\n"}}
		app := model.Application{Name: "Ruff", Type: model.ApplicationTypePackage, Package: "ruff", Environment: map[string]string{"UV_INDEX": "https://private.invalid/simple"}, Provider: model.ProviderConfig{Type: model.ProviderUV}}
		latest, err := (UVProvider{Runner: runner}).Latest(context.Background(), Request{App: app, CurrentVersion: "0.8.0"})
		if err != nil || latest != "0.9.1" || runner.command != uv+" tool list --outdated --show-version-specifiers --no-progress" || runner.env["UV_INDEX"] == "" {
			t.Fatalf("UV latest=%q command=%q env=%#v err=%v", latest, runner.command, runner.env, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = (UVProvider{Runner: &uvLatestRunner{err: context.Canceled}}).Latest(ctx, Request{App: app, CurrentVersion: "0.8.0"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("UV cancellation=%v", err)
		}
	})
	t.Run("registry-supports-custom-provider-and-rejects-duplicates", func(t *testing.T) {
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
	})
	t.Run("builtin-capability-matrix", func(t *testing.T) {
		registry := NewRegistry()
		if err := RegisterBuiltins(registry, nil, nil); err != nil {
			t.Fatal(err)
		}

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
	})
	t.Run("capability-unavailable-carries-provider-and-capability", func(t *testing.T) {
		err := CapabilityUnavailable(" NPM ", CapabilityArtifact)
		var typed *Error
		if !errors.As(err, &typed) || typed.Provider != "npm" || typed.Capability != CapabilityArtifact {
			t.Fatalf("typed unavailable error = %#v", err)
		}
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unavailable error does not preserve sentinel: %v", err)
		}
	})
	t.Run("package-dependent-providers-reject-missing-package-before-io", func(t *testing.T) {
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
	})
}

var (
	_ Checksummer               = GitHubReleaseProvider{}
	_ ArtifactProvider          = GitHubReleaseProvider{}
	_ DownloadResolver          = GitHubReleaseProvider{}
	_ DownloadResolver          = GitHubTagProvider{}
	_ ArtifactProvider          = GitHubTagProvider{}
	_ DownloadResolver          = NPMProvider{}
	_ ArtifactProvider          = NPMProvider{}
	_ DownloadResolver          = PyPIProvider{}
	_ ArtifactProvider          = PyPIProvider{}
	_ DownloadResolver          = JetBrainsProvider{}
	_ DownloadResolver          = GoProvider{}
	_ ArtifactCandidateProvider = GoProvider{}
	_ DownloadResolver          = NodeLTSProvider{}
	_ DownloadResolver          = SparkleProvider{}
	_ ArtifactProvider          = SparkleProvider{}
	_ UpdateExecutor            = SparkleProvider{}
	_ CurrentVersioner          = localMetadataProvider{}
	_ UpdateExecutor            = packageUpdateProvider{}
	_ CurrentVersioner          = (*actionBackedProvider)(nil)
	_ LatestVersioner           = (*actionBackedProvider)(nil)
	_ UpdateExecutor            = (*actionBackedProvider)(nil)
	_ DownloadResolver          = (*actionBackedProvider)(nil)
	_ InstallExecutor           = (*actionBackedProvider)(nil)
)

func assertPackageRequiredError(t *testing.T, err error, provider model.ProviderType, capability Capability) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Key != "provider.package_required" || typed.Provider != string(provider) || typed.Capability != capability {
		t.Fatalf("package error = %#v", err)
	}
}

func assertProviderCapabilityError(t *testing.T, err error, capability Capability, cancelled bool) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Capability != capability {
		t.Fatalf("typed %s error = %#v", capability, err)
	}
	if cancelled && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled %s error = %v", capability, err)
	}
}
func TestProviderCapabilityContract(t *testing.T) {
	t.Run("capability-contract-separates-download-checksum-and-artifact", func(t *testing.T) {
		capabilities := DetectCapabilities(GitHubReleaseProvider{})
		if capabilities.Download == nil || capabilities.Checksum == nil || capabilities.Artifact == nil {
			t.Fatalf("GitHub Release capability split missing: %#v", capabilities)
		}

		var download model.Download
		var checksum, artifact string
		var err error
		request := Request{}
		download, err = capabilities.Download.Download(context.Background(), request)
		if err == nil && download.URL == "" {
			t.Fatal("Download must return a complete download description")
		}
		checksum, err = capabilities.Checksum.Checksum(context.Background(), request)
		if err == nil && checksum == "" {
			t.Fatal("Checksum must return a SHA256 string")
		}
		artifact, err = capabilities.Artifact.Artifact(context.Background(), request)
		if err == nil && artifact == "" {
			t.Fatal("Artifact must return an identifier string")
		}
	})
	t.Run("shared-local-metadata-and-package-update-capabilities", func(t *testing.T) {
		directory := t.TempDir()
		marker := filepath.Join(directory, "updated")
		tool := filepath.Join(directory, "tool")
		npm := filepath.Join(directory, "npm")
		if err := os.WriteFile(tool, []byte("#!/bin/sh\n[ \"$1\" = \"--version\" ] && printf 'tool v1.2.3\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		npmScript := "#!/bin/sh\nif [ \"$1\" = \"list\" ]; then printf '{\"dependencies\":{\"pkg\":{\"version\":\"2.3.4\"}}}\\n'; else printf '%s' \"$*\" > " + runtimeutil.QuoteShell(marker) + "; fi\n"
		if err := os.WriteFile(npm, []byte(npmScript), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
		runner := runtimeutil.Runner{}
		current := localMetadataProvider{runner: runner}
		cliVersion, err := current.Current(context.Background(), Request{App: model.Application{Name: "Tool", Type: model.ApplicationTypeCLI, InstallPath: tool, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}}})
		if err != nil || cliVersion != "1.2.3" {
			t.Fatalf("CLI Current() = %q, %v", cliVersion, err)
		}
		app := model.Application{Name: "Package", Type: model.ApplicationTypePackage, InstallPath: directory, Package: "pkg", Identity: "package:node:pkg", Provider: model.ProviderConfig{Type: model.ProviderNPM}}
		packageVersion, err := current.Current(context.Background(), Request{App: app})
		if err != nil || packageVersion != "2.3.4" {
			t.Fatalf("package Current() = %q, %v", packageVersion, err)
		}
		if err := (packageUpdateProvider{runner: runner}).Update(context.Background(), Request{App: app}); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(marker)
		if err != nil || !strings.Contains(string(content), "install --global pkg@latest") {
			t.Fatalf("package update marker = %q, %v", content, err)
		}
	})
	t.Run("identity-does-not-grant-default-provider-package-capabilities", func(t *testing.T) {
		app := model.Application{Name: "Rubocop", Type: model.ApplicationTypePackage, Package: "rubocop", Identity: "package:ruby:rubocop", Provider: model.ProviderConfig{Type: model.ProviderDefault}}
		current := localMetadataProvider{runner: runtimeutil.Runner{}}
		if _, err := current.Current(context.Background(), Request{App: app}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("identity granted Current capability: %v", err)
		}
		if err := (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(context.Background(), Request{App: app}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("identity granted Update capability: %v", err)
		}
	})
	t.Run("shared-package-capabilities-reject-missing-package-before-manager-lookup", func(t *testing.T) {
		for _, provider := range []model.ProviderType{model.ProviderNPM, model.ProviderPyPI, model.ProviderUV} {
			t.Run(string(provider), func(t *testing.T) {
				request := Request{App: model.Application{
					Name: "Missing", Type: model.ApplicationTypePackage,
					Provider: model.ProviderConfig{Type: provider},
				}}
				_, currentErr := (localMetadataProvider{runner: runtimeutil.Runner{}}).Current(context.Background(), request)
				assertPackageRequiredError(t, currentErr, provider, CapabilityCurrent)
				updateErr := (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(context.Background(), request)
				assertPackageRequiredError(t, updateErr, provider, CapabilityUpdate)
			})
		}
	})
	t.Run("uv-current-and-update-do-not-require-identity", func(t *testing.T) {
		directory := t.TempDir()
		marker := filepath.Join(directory, "uv-updated")
		uv := filepath.Join(directory, "uv")
		script := "#!/bin/sh\nif [ \"$1\" = \"tool\" ] && [ \"$2\" = \"list\" ]; then printf 'ruff v0.8.0\\n'; else printf '%s' \"$*\" > " + runtimeutil.QuoteShell(marker) + "; fi\n"
		if err := os.WriteFile(uv, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
		request := Request{App: model.Application{
			Name: "Ruff", Type: model.ApplicationTypePackage, Package: "ruff",
			Provider: model.ProviderConfig{Type: model.ProviderUV},
		}}
		current, err := (localMetadataProvider{runner: runtimeutil.Runner{}}).Current(context.Background(), request)
		if err != nil || current != "0.8.0" {
			t.Fatalf("UV Current() = %q, %v", current, err)
		}
		if err := (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(marker)
		if err != nil || !strings.Contains(string(content), "tool upgrade ruff") {
			t.Fatalf("UV update marker = %q, %v", content, err)
		}
	})
	t.Run("shared-metadata-capabilities-preserve-typed-failures-and-cancellation", func(t *testing.T) {
		directory := t.TempDir()
		t.Setenv("PATH", directory)
		current := localMetadataProvider{runner: runtimeutil.Runner{}}
		app := model.Application{Name: "Missing", Type: model.ApplicationTypePackage, Package: "missing", Provider: model.ProviderConfig{Type: model.ProviderNPM}}
		_, err := current.Current(context.Background(), Request{App: app})
		assertProviderCapabilityError(t, err, CapabilityCurrent, false)

		npm := filepath.Join(directory, "npm")
		if err := os.WriteFile(npm, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = current.Current(ctx, Request{App: app})
		assertProviderCapabilityError(t, err, CapabilityCurrent, true)
		err = (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(ctx, Request{App: app})
		assertProviderCapabilityError(t, err, CapabilityUpdate, true)

		if err := os.WriteFile(npm, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		err = (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(context.Background(), Request{App: app})
		assertProviderCapabilityError(t, err, CapabilityUpdate, false)
	})
	t.Run("go-component-latest-uses-go-metadata-instead-of-runtime-feed", func(t *testing.T) {
		directory := t.TempDir()
		goBinary := filepath.Join(directory, "go")
		marker := filepath.Join(directory, "go-update")
		script := `#!/bin/sh
if [ "$1" = "version" ]; then
  printf 'path\texample.com/tool/cmd/tool\nmod\texample.com/tool\tv1.2.3\n'
elif [ "$1" = "install" ]; then
  printf '%s' "$*" > ` + runtimeutil.QuoteShell(marker) + `
else
  printf 'v2.0.0\n'
fi
`
		if err := os.WriteFile(goBinary, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
		app := model.Application{Name: "Go Tool", Type: model.ApplicationTypePackage, InstallPath: filepath.Join(directory, "tool"), Identity: "package:python:wrong", Provider: model.ProviderConfig{Type: model.ProviderGo}}
		current, err := (localMetadataProvider{runner: runtimeutil.Runner{}}).Current(context.Background(), Request{App: app})
		if err != nil || current != "1.2.3" {
			t.Fatalf("Go component Current() = %q, %v", current, err)
		}
		latest, err := (GoProvider{Runner: runtimeutil.Runner{}}).Latest(context.Background(), Request{App: app})
		if err != nil || latest != "2.0.0" {
			t.Fatalf("Go component Latest() = %q, %v", latest, err)
		}
		if err := (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(context.Background(), Request{App: app}); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(marker)
		if err != nil || !strings.Contains(string(content), "install example.com/tool/cmd/tool@latest") {
			t.Fatalf("Go component update = %q, %v", content, err)
		}
	})
	t.Run("go-component-update-rejects-incomplete-metadata-and-preserves-cancellation", func(t *testing.T) {
		directory := t.TempDir()
		goBinary := filepath.Join(directory, "go")
		if err := os.WriteFile(goBinary, []byte("#!/bin/sh\nprintf 'mod\\texample.com/tool\\tv1.2.3\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		app := model.Application{Name: "Go Tool", Type: model.ApplicationTypePackage, InstallPath: filepath.Join(directory, "tool"), Identity: "package:go:tool", Provider: model.ProviderConfig{Type: model.ProviderGo}}
		err := (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(context.Background(), Request{App: app})
		assertProviderCapabilityError(t, err, CapabilityUpdate, false)

		if err := os.WriteFile(goBinary, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err = (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(ctx, Request{App: app})
		assertProviderCapabilityError(t, err, CapabilityUpdate, true)
	})
	t.Run("sparkle-update-uses-official-cli-against-bundle-metadata", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("Sparkle updates are Darwin-specific")
		}
		directory := t.TempDir()
		marker := filepath.Join(directory, "sparkle-args")
		cli := filepath.Join(directory, "sparkle")
		script := "#!/bin/sh\nprintf '%s' \"$*\" > " + runtimeutil.QuoteShell(marker) + "\n"
		if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		appPath := filepath.Join(directory, "Fixture.app")
		infoPath := filepath.Join(appPath, "Contents", "Info.plist")
		if err := os.MkdirAll(filepath.Dir(infoPath), 0o700); err != nil {
			t.Fatal(err)
		}
		plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleShortVersionString</key><string>1.0.0</string><key>SUFeedURL</key><string>https://example.invalid/appcast.xml</string></dict></plist>`
		if err := os.WriteFile(infoPath, []byte(plist), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
		app := model.Application{Name: "Fixture", Type: model.ApplicationTypeBundle, InstallPath: appPath, Provider: model.ProviderConfig{Type: model.ProviderSparkle}}
		if err := (SparkleProvider{Runner: runtimeutil.Runner{}}).Update(context.Background(), Request{App: app}); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(marker)
		if err != nil || !strings.Contains(string(content), "--check-immediately") || !strings.Contains(string(content), appPath) {
			t.Fatalf("sparkle args = %q, %v", content, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err = (SparkleProvider{Runner: runtimeutil.Runner{}}).Update(ctx, Request{App: app})
		assertProviderCapabilityError(t, err, CapabilityUpdate, true)
		invalidPlist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleShortVersionString</key><string>1.0.0</string><key>SUFeedURL</key><string>http://insecure.invalid/appcast.xml</string></dict></plist>`
		if err := os.WriteFile(infoPath, []byte(invalidPlist), 0o600); err != nil {
			t.Fatal(err)
		}
		err = (SparkleProvider{Runner: runtimeutil.Runner{}}).Update(context.Background(), Request{App: app})
		assertProviderCapabilityError(t, err, CapabilityUpdate, false)
	})
	t.Run("action-capabilities-advertise-only-configured-actions", func(t *testing.T) {
		request := Request{App: model.Application{Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{
			Check:    "printf 1.2.3",
			Download: &model.Download{URL: "https://example.invalid/app.zip"},
			Install:  "printf install",
		}}}}
		defaultCapabilities := ActionCapabilities(runtimeutil.Runner{}, request, true)
		if defaultCapabilities.Current != nil || defaultCapabilities.Update != nil || defaultCapabilities.Artifact != nil {
			t.Fatalf("unconfigured actions were advertised: %#v", defaultCapabilities)
		}
		if defaultCapabilities.Latest == nil || defaultCapabilities.Download == nil || defaultCapabilities.Install == nil {
			t.Fatalf("configured default actions were not advertised: %#v", defaultCapabilities)
		}
		nonDefaultCapabilities := ActionCapabilities(runtimeutil.Runner{}, request, false)
		if nonDefaultCapabilities.Install != nil {
			t.Fatalf("non-default action capabilities advertised install: %#v", nonDefaultCapabilities)
		}
	})
	t.Run("action-capabilities-preserve-no-update-and-cancellation", func(t *testing.T) {
		request := Request{CurrentVersion: "1.2.3", App: model.Application{Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{Check: "printf 'already up to date'"}}}}
		latest, err := ActionCapabilities(runtimeutil.Runner{}, request, true).Latest.Latest(context.Background(), request)
		if err != nil || latest != "1.2.3" {
			t.Fatalf("no-update latest = %q, %v", latest, err)
		}
		cancelRequest := request
		cancelRequest.App.Provider.Actions.Check = "sleep 5"
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = ActionCapabilities(runtimeutil.Runner{}, cancelRequest, true).Latest.Latest(ctx, cancelRequest)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled action error = %v", err)
		}
	})
}
