package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// These assignments keep the standard Provider contract honest at compile time.
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

func TestCapabilityContractSeparatesDownloadChecksumAndArtifact(t *testing.T) {
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
}

func TestSharedLocalMetadataAndPackageUpdateCapabilities(t *testing.T) {
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
}

func TestIdentityDoesNotGrantDefaultProviderPackageCapabilities(t *testing.T) {
	app := model.Application{Name: "Rubocop", Type: model.ApplicationTypePackage, Package: "rubocop", Identity: "package:ruby:rubocop", Provider: model.ProviderConfig{Type: model.ProviderDefault}}
	current := localMetadataProvider{runner: runtimeutil.Runner{}}
	if _, err := current.Current(context.Background(), Request{App: app}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("identity granted Current capability: %v", err)
	}
	if err := (packageUpdateProvider{runner: runtimeutil.Runner{}}).Update(context.Background(), Request{App: app}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("identity granted Update capability: %v", err)
	}
}

func TestSharedPackageCapabilitiesRejectMissingPackageBeforeManagerLookup(t *testing.T) {
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
}

func TestUVCurrentAndUpdateDoNotRequireIdentity(t *testing.T) {
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
}

func assertPackageRequiredError(t *testing.T, err error, provider model.ProviderType, capability Capability) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Key != "provider.package_required" || typed.Provider != string(provider) || typed.Capability != capability {
		t.Fatalf("package error = %#v", err)
	}
}

func TestSharedMetadataCapabilitiesPreserveTypedFailuresAndCancellation(t *testing.T) {
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

func TestGoComponentLatestUsesGoMetadataInsteadOfRuntimeFeed(t *testing.T) {
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
}

func TestGoComponentUpdateRejectsIncompleteMetadataAndPreservesCancellation(t *testing.T) {
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
}

func TestSparkleUpdateUsesOfficialCLIAgainstBundleMetadata(t *testing.T) {
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
}

func TestActionCapabilitiesAdvertiseOnlyConfiguredActions(t *testing.T) {
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
}

func TestActionCapabilitiesPreserveNoUpdateAndCancellation(t *testing.T) {
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
}
