package metadata

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

type metadataRunner struct {
	responses map[string]runtimeutil.Result
	errors    map[string]error
	calls     []string
}

func (runner *metadataRunner) Run(_ context.Context, command string, _ map[string]string) (runtimeutil.Result, error) {
	runner.calls = append(runner.calls, command)
	return runner.responses[command], runner.errors[command]
}

func TestReadMacApplicationMetadata(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Fixture.app")
	infoPath := filepath.Join(appPath, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.example.fixture</string>
<key>CFBundleDisplayName</key><string>Fixture Display</string>
<key>CFBundleShortVersionString</key><string>v2.4.6</string>
<key>SUFeedURL</key><string>https://example.invalid/appcast.xml</string>
<key>SUPublicEDKey</key><string>public-key</string>
<key>SUAllowsAutomaticUpdates</key><true/>
</dict></plist>`
	if err := os.WriteFile(infoPath, []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := ReadMacApplicationMetadata(context.Background(), appPath)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.BundleID != "com.example.fixture" || metadata.Name != "Fixture Display" || metadata.Version != "2.4.6" || metadata.SparkleFeedURL == "" || metadata.SparklePublicEDKey != "public-key" || !metadata.SparkleAllowsAutoUpdates {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestReadJetBrainsProductCodeUsesStrictFixture(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "PyCharm.app")
	productPath := filepath.Join(appPath, "Contents", "Resources", "product-info.json")
	if err := os.MkdirAll(filepath.Dir(productPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(productPath, []byte(`{"productCode":"PY","name":"PyCharm","version":"2026.1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readJetBrainsProductCode(appPath, "com.jetbrains.pycharm"); got != "PY" {
		t.Fatalf("product code = %q", got)
	}
	if got := readJetBrainsProductCode(appPath, "com.example.fixture"); got != "" {
		t.Fatalf("non-JetBrains product code = %q", got)
	}
	if err := os.WriteFile(productPath, []byte(`{"productCode":"Py"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readJetBrainsProductCode(appPath, "com.jetbrains.pycharm"); got != "" {
		t.Fatalf("invalid product code = %q", got)
	}
}

func TestDetectCLIVersionUsesConventionalArgumentsInOrder(t *testing.T) {
	runner := &metadataRunner{responses: map[string]runtimeutil.Result{}, errors: map[string]error{}}
	for _, command := range []string{"/tool --version", "/tool version"} {
		runner.responses[command] = runtimeutil.Result{ExitCode: 1}
	}
	runner.responses["/tool -v"] = runtimeutil.Result{Stdout: "tool v3.2.1"}
	value, err := DetectCLIVersion(context.Background(), runner, "/tool", nil)
	if err != nil || value != "3.2.1" {
		t.Fatalf("DetectCLIVersion() = %q, %v", value, err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"/tool --version", "/tool version", "/tool -v"}) {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestPackageMetadataAndUpdateCommands(t *testing.T) {
	if got := PackageEcosystemFromIdentity("package:uv:ruff"); got != PackageUV {
		t.Fatalf("ecosystem = %q", got)
	}
	runner := &metadataRunner{responses: map[string]runtimeutil.Result{
		"/usr/bin/npm list --global --depth=0 --json @scope/tool": {Stdout: `{"dependencies":{"@scope/tool":{"version":"1.2.3"}}}`},
	}, errors: map[string]error{}}
	value, err := ReadPackageVersion(context.Background(), runner, PackageTarget{Ecosystem: PackageNode, Manager: "/usr/bin/npm", Name: "@scope/tool"})
	if err != nil || value != "1.2.3" {
		t.Fatalf("ReadPackageVersion() = %q, %v", value, err)
	}
	command, err := PackageUpdateCommand(PackageTarget{Ecosystem: PackageGo, Manager: "/usr/bin/go", Name: "example.com/tool/cmd/tool"})
	if err != nil || command != "/usr/bin/go install example.com/tool/cmd/tool@latest" {
		t.Fatalf("PackageUpdateCommand() = %q, %v", command, err)
	}
	rubyVersion, err := PackageVersionCommand(PackageTarget{Ecosystem: PackageRuby, Manager: "/usr/bin/gem", Name: "rubocop"})
	if err != nil || rubyVersion != "/usr/bin/gem list --local --exact rubocop" {
		t.Fatalf("Ruby PackageVersionCommand() = %q, %v", rubyVersion, err)
	}
	rubyUpdate, err := PackageUpdateCommand(PackageTarget{Ecosystem: PackageRuby, Manager: "/usr/bin/gem", Name: "rubocop", UserInstall: true})
	if err != nil || rubyUpdate != "/usr/bin/gem install --user-install --no-document rubocop" {
		t.Fatalf("Ruby PackageUpdateCommand() = %q, %v", rubyUpdate, err)
	}
}

func TestFindManagersAndSparkleCLI(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"python3", "npm", "go", "uv", "gem", "sparkle"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory)
	for _, ecosystem := range []PackageEcosystem{PackagePython, PackageNode, PackageGo, PackageUV, PackageRuby} {
		if path, err := FindPackageManager(ecosystem); err != nil || path == "" {
			t.Fatalf("FindPackageManager(%s) = %q, %v", ecosystem, path, err)
		}
	}
	if path, err := FindSparkleCLI(); err != nil || filepath.Base(path) != "sparkle" {
		t.Fatalf("FindSparkleCLI() = %q, %v", path, err)
	}
	if _, err := FindPackageManager("unknown"); err == nil {
		t.Fatal("unknown package manager was accepted")
	}
}

func TestPackageVersionReadersCoverSupportedEcosystems(t *testing.T) {
	for _, test := range []struct {
		ecosystem PackageEcosystem
		stdout    string
		want      string
	}{
		{PackagePython, "2.3.4\n", "2.3.4"},
		{PackageGo, "v3.4.5\n", "3.4.5"},
		{PackageUV, "v4.5.6\n", "4.5.6"},
		{PackageRuby, "rubocop (5.6.7)\n", "5.6.7"},
	} {
		runner := &metadataRunner{responses: map[string]runtimeutil.Result{}, errors: map[string]error{}}
		target := PackageTarget{Ecosystem: test.ecosystem, Manager: "/manager", Name: "tool", InstallPath: "/tool"}
		command, err := PackageVersionCommand(target)
		if err != nil {
			t.Fatal(err)
		}
		runner.responses[command] = runtimeutil.Result{Stdout: test.stdout}
		got, err := ReadPackageVersion(context.Background(), runner, target)
		if err != nil || got != test.want {
			t.Fatalf("ReadPackageVersion(%s) = %q, %v", test.ecosystem, got, err)
		}
	}
}

func TestGoPackageVersionCommandRendersAndExecutesAwkBraces(t *testing.T) {
	manager := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(manager, []byte("#!/bin/sh\nprintf 'path\\texample.com/tool/cmd/tool\\nmod\\texample.com/tool\\tv1.2.3\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command, err := PackageVersionCommand(PackageTarget{Ecosystem: PackageGo, Manager: manager, Name: "example.com/tool/cmd/tool", InstallPath: "/tool"})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := runtimeutil.Render(command, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("/bin/sh", "-c", rendered).Output()
	if err != nil {
		t.Fatalf("awk command=%q err=%v", rendered, err)
	}
	if got, err := version.Extract(string(output)); err != nil || got != "1.2.3" {
		t.Fatalf("version=%q err=%v", got, err)
	}
}

func TestGoComponentMetadataParsingAndReading(t *testing.T) {
	output := "path\texample.com/tool/cmd/tool\nmod\texample.com/tool\tv1.2.3\n"
	metadata, err := ParseGoComponentMetadata(output)
	if err != nil || metadata.Command != "example.com/tool/cmd/tool" || metadata.Module != "example.com/tool" || metadata.Version != "1.2.3" {
		t.Fatalf("ParseGoComponentMetadata() = %#v, %v", metadata, err)
	}
	command := "/go version -m /tool"
	runner := &metadataRunner{responses: map[string]runtimeutil.Result{command: {Stdout: output}}, errors: map[string]error{}}
	metadata, err = ReadGoComponentMetadata(context.Background(), runner, "/go", "/tool", nil)
	if err != nil || metadata.Module != "example.com/tool" {
		t.Fatalf("ReadGoComponentMetadata() = %#v, %v", metadata, err)
	}
	if _, err := ParseGoComponentMetadata("path example.com/tool"); err == nil {
		t.Fatal("incomplete Go metadata was accepted")
	}
	if _, err := ParseGoComponentMetadata("mod example.com/tool v1.2.3"); err == nil {
		t.Fatal("Go metadata without an installed command path was accepted")
	}
}

func TestPackageUpdateCommandVariants(t *testing.T) {
	for _, target := range []PackageTarget{
		{Ecosystem: PackagePython, Manager: "/python", Name: "tool", UserInstall: true},
		{Ecosystem: PackageNode, Manager: "/npm", Name: "tool"},
		{Ecosystem: PackageUV, Manager: "/uv", Name: "tool"},
		{Ecosystem: PackageRuby, Manager: "/gem", Name: "tool"},
	} {
		if command, err := PackageUpdateCommand(target); err != nil || !strings.Contains(command, "tool") {
			t.Fatalf("PackageUpdateCommand(%s) = %q, %v", target.Ecosystem, command, err)
		}
	}
	if _, err := PackageVersionCommand(PackageTarget{Ecosystem: "unknown", Manager: "/manager", Name: "tool"}); err == nil {
		t.Fatal("unsupported version command was accepted")
	}
	if _, err := PackageUpdateCommand(PackageTarget{Ecosystem: "unknown", Manager: "/manager", Name: "tool"}); err == nil {
		t.Fatal("unsupported update command was accepted")
	}
	home, err := os.UserHomeDir()
	if err == nil && !userPackagePath(filepath.Join(home, "Library", "Python")) {
		t.Fatal("user package path was not recognized")
	}
}

func TestMetadataReadersPreserveCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DetectCLIVersion(ctx, &metadataRunner{}, "/tool", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("DetectCLIVersion() error = %v", err)
	}
	if _, err := ReadMacApplicationMetadata(ctx, "/Applications/Fixture.app"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadMacApplicationMetadata() error = %v", err)
	}
}
