package scanner

import (
	"os"
	"reflect"

	"strings"

	"time"

	"github.com/eoctet/tendkit/internal/scanner/handler"

	"context"
	"errors"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"

	"path/filepath"

	"testing"

	"github.com/eoctet/tendkit/internal/model"

	"github.com/eoctet/tendkit/pkg/i18n"
)

// The scanner flows intentionally share only these small catalog builders.  They
// describe stable input ownership, rather than recreating catalog fixtures per
// flow or preserving the retired catalog test matrix.
func providerConfig(kind model.ProviderType, version, check, update string, download *model.Download) model.ProviderConfig {
	provider := model.ProviderConfig{Type: kind}
	if version != "" || check != "" || update != "" || download != nil {
		provider.Actions = &model.ProviderActions{Version: version, Check: check, Update: update, Download: download}
	}
	return provider
}

func applicationByIdentity(apps []model.Application, identity string) model.Application {
	for _, application := range apps {
		if application.Identity == identity {
			return application
		}
	}
	return model.Application{}
}

func writeScannerFixture(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func managedOwner(id, name string, provider model.ProviderType, pkg, identity, ecosystem, path string, mode model.UpdateMode, current string) discovery {
	return discovery{App: model.Application{ID: id, Name: name, Type: model.ApplicationTypePackage, InstallPath: path, Enabled: true, ScanManaged: true, Provider: model.ProviderConfig{Type: provider}, Package: pkg, Identity: identity, UpdateMode: mode}, State: model.ManagedStatus{CurrentVersion: current, UpdateStatus: model.StatusUnchecked}, Evidence: &handler.InstallationEvidence{Source: ecosystem, Package: pkg, ExecutablePaths: []string{path}}}
}

func applicationByID(t *testing.T, apps []model.Application, id string) model.Application {
	t.Helper()
	for _, app := range apps {
		if app.ID == id {
			return app
		}
	}
	t.Fatalf("application %q not found in %#v", id, apps)
	return model.Application{}
}

func TestScannerFullScanFlow(t *testing.T) {
	t.Run("scan-reports-progress-milestones", func(t *testing.T) {
		disabled := false
		catalog := model.Config{Settings: model.Settings{Scan: model.ScanSettings{
			Path: disabled, Application: disabled,
			Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
		}}}
		stages := make([]string, 0)
		scanner := Scanner{Progress: func(progress Progress) { stages = append(stages, progress.Stage) }}
		if _, _, err := scanner.Scan(context.Background(), catalog, model.RuntimeState{}); err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Join(stages, ","), "prepare,finalize"; got != want {
			t.Fatalf("progress stages = %q, want %q", got, want)
		}
	})
	t.Run("scan-uses-catalog-download-directory-for-version-action", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "installed")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", t.TempDir())
		enabled, disabled := true, false
		downloadDir := filepath.Join(directory, "catalog downloads")
		catalog := model.Config{
			Settings: model.Settings{
				Downloader: model.DownloaderSettings{StorePath: downloadDir},
				Scan: model.ScanSettings{Path: enabled, Application: disabled,
					Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled}},
			},
			Apps: []model.Application{{
				ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: installed,
				Provider: providerConfig(model.ProviderDefault, "test {download_dir} = "+runtimeutil.QuoteShell(downloadDir)+" && printf '1.2.3\\n'", "", "", nil),
			}},
		}
		updated, _, err := (Scanner{Runner: runtimeutil.Runner{}, DownloadDir: "wrong"}).Scan(context.Background(), catalog, model.RuntimeState{})
		if err != nil || updated.Apps[0].StatusManaged.CurrentVersion != "1.2.3" {
			t.Fatalf("catalog download directory was not rendered: apps=%#v err=%v", updated.Apps, err)
		}
	})
	t.Run("full-scan-does-not-merge-discovery-into-unmanaged-built-in-path-application", func(t *testing.T) {
		directory := t.TempDir()
		gitPath := filepath.Join(directory, "git")
		if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf 'git version 2.51.0\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		disabled, enabled := false, true
		configuredPath := filepath.Join(directory, "configured-git-is-missing")
		application := model.Application{
			ID: "git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: configuredPath,
			Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: false,
			StatusManaged: model.ManagedStatus{CurrentVersion: "2.50.0", UpdateStatus: model.StatusCurrent},
		}
		catalog := model.Config{Apps: []model.Application{application}, Settings: model.Settings{Scan: model.ScanSettings{
			Path: enabled, Application: disabled,
			Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
		}}}

		result, state, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}).Scan(context.Background(), catalog, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Apps) != 1 || result.Apps[0].InstallPath != configuredPath || result.Apps[0].Provider != application.Provider {
			t.Fatalf("unmanaged application accepted discovered configuration: %#v", result.Apps)
		}
		if result.Apps[0].StatusManaged.UpdateStatus != model.StatusMissing || state.Observations["git"].Found {
			t.Fatalf("unmanaged application was not observed from its configured path: app=%#v state=%#v", result.Apps[0].StatusManaged, state)
		}
	})
	t.Run("package-scan-reports-manager-progress-before-discovery", func(t *testing.T) {
		disabled, enabled := false, true
		t.Setenv("PATH", t.TempDir())
		settings := model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled}
		events := make([]string, 0)
		scanPackages(context.Background(), settings, runtimeutil.Runner{}, exclusionMatcher{}, nil, func(stage, subject string) {
			events = append(events, stage+":"+subject)
		})
		if len(events) == 0 || events[0] != "package_manager:Node.js" {
			t.Fatalf("package progress events = %v", events)
		}
	})
	t.Run("repeated-full-scan-does-not-introduce-duplicate-candidate-metadata", func(t *testing.T) {
		directory := t.TempDir()
		npm := `#!/bin/sh
case "$*" in
  "--version") printf '10.0.0\n' ;;
  "install --help") exit 1 ;;
  "list -g --depth=0 --json") printf '{"dependencies":{"@anthropic-ai/claude-code":{"version":"2.1.233"}}}\n' ;;
  "root -g") printf '%s\n' "` + directory + `" ;;
  "view @anthropic-ai/claude-code description homepage repository.url --json") printf '{}\n' ;;
  *) exit 1 ;;
esac
`
		for name, body := range map[string]string{
			"npm":    npm,
			"claude": "#!/bin/sh\nprintf '2.1.233\\n'\n",
		} {
			if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", directory)
		enabled, disabled := true, false
		catalog := model.Config{Settings: model.Settings{Scan: model.ScanSettings{
			Path: enabled, Application: disabled,
			Packages: model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled},
		}}}
		scanner := Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}
		first, state, err := scanner.Scan(context.Background(), catalog, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		firstTarget := applicationByID(t, first.Apps, "cli-claude")
		if firstTarget.Description == "" || firstTarget.URL == "" {
			t.Fatalf("first full scan lost duplicate metadata: %#v", firstTarget)
		}
		if firstTarget.Type != model.ApplicationTypeCLI || firstTarget.Provider.VersionAction() != filepath.Join(directory, "claude")+" --version" {
			t.Fatalf("first full scan did not keep the built-in CLI canonical: %#v", firstTarget)
		}

		second, _, err := scanner.Scan(context.Background(), first, state)
		if err != nil {
			t.Fatal(err)
		}
		secondTarget := applicationByID(t, second.Apps, firstTarget.ID)
		if secondTarget.Description != firstTarget.Description || secondTarget.URL != firstTarget.URL {
			t.Fatalf("second full scan introduced metadata differences: first=%#v second=%#v", firstTarget, secondTarget)
		}
	})
	t.Run("full-scan-prefers-built-in-cli-and-enriches-from-node-package", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.MkdirAll(filepath.Join(directory, "@google", "gemini-cli"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "@google", "gemini-cli", "package.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		npm := `#!/bin/sh
case "$*" in
  "--version") printf '10.0.0\n' ;;
  "install --help") exit 1 ;;
  "list -g --depth=0 --json") printf '{"dependencies":{"@google/gemini-cli":{"version":"0.55.1"}}}\n' ;;
  "root -g") printf '%s\n' "` + directory + `" ;;
  "view @google/gemini-cli description homepage repository.url --json") printf '{}\n' ;;
  *) exit 1 ;;
esac
`
		for name, body := range map[string]string{
			"npm":    npm,
			"gemini": "#!/bin/sh\nprintf '0.55.1\\n'\n",
		} {
			if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", directory)
		enabled, disabled := true, false
		catalog := model.Config{Settings: model.Settings{Scan: model.ScanSettings{
			Path: enabled, Application: disabled,
			Packages: model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled},
		}}}

		result, _, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}).Scan(context.Background(), catalog, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		target := applicationByID(t, result.Apps, "cli-gemini")
		if target.ID != "cli-gemini" || target.Type != model.ApplicationTypeCLI || target.InstallPath != filepath.Join(directory, "gemini") {
			t.Fatalf("full scan did not choose built-in CLI: %#v", target)
		}
		if target.Provider.VersionAction() != filepath.Join(directory, "gemini")+" --version" || target.Provider.UpdateAction() != "" || target.UpdateMode != model.ModeCheck {
			t.Fatalf("PATH candidate did not remain independently scoped: %#v", target)
		}
		packageTarget := applicationByIdentity(result.Apps, "package:node:google-gemini-cli")
		if packageTarget.ID == "" || packageTarget.Provider.UpdateAction() == "" {
			t.Fatalf("package candidate was not preserved independently: %#v", result.Apps)
		}
	})
	t.Run("complete-package-inventory-omission-directly-verifies-configured-application", func(t *testing.T) {
		directory := t.TempDir()
		python := filepath.Join(directory, "python3")
		if err := os.WriteFile(python, []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		installed := filepath.Join(directory, "sample-package")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		disabled, enabled := false, true
		application := model.Application{
			ID: "pkg-python-sample", Name: "sample", Type: model.ApplicationTypePackage,
			Identity: "package:python:sample", Package: "sample", InstallPath: installed,
			ScanManaged: true, Provider: providerConfig(model.ProviderDefault, "printf 'sample 1.2.3'", "", "", nil),
			StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusCurrent, CurrentVersion: "1.2.2"},
		}
		catalog := model.Config{Apps: []model.Application{application}, Settings: model.Settings{Scan: model.ScanSettings{
			Path: disabled, Application: disabled,
			Packages: model.PackageScanSettings{Python: enabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled},
		}}}

		updated, state, err := (Scanner{Runner: runtimeutil.Runner{}}).Scan(context.Background(), catalog, model.RuntimeState{})
		if err != nil {
			t.Fatal(err)
		}
		if len(updated.Apps) != 1 || updated.Apps[0].ID != application.ID {
			t.Fatalf("complete inventory omission removed configured application: %#v", updated.Apps)
		}
		if updated.Apps[0].StatusManaged.CurrentVersion != "1.2.3" || updated.Apps[0].StatusManaged.UpdateStatus == model.StatusMissing {
			t.Fatalf("configured application was not directly verified: %#v", updated.Apps[0].StatusManaged)
		}
		if observation := state.Observations[application.ID]; !observation.Found {
			t.Fatalf("directly verified application observation = %#v", observation)
		}
	})
	t.Run("incomplete-package-scan-preserves-managed-package-state", func(t *testing.T) {
		for _, ecosystem := range []string{string(handler.Python), string(handler.Node), string(handler.Go), string(handler.UV), string(handler.Ruby)} {
			application := model.Application{ID: "managed", Type: model.ApplicationTypePackage, Identity: "package:" + ecosystem + ":sample", ScanManaged: true}
			previous := model.ManagedStatus{UpdateStatus: model.StatusCurrent, CurrentVersion: "1.2.3"}
			application.StatusManaged = previous
			session := scanSession{
				state:    model.RuntimeState{},
				observed: map[string]model.ManagedStatus{},
				packages: packageScanResult{Complete: map[string]bool{ecosystem: false}, Errors: map[string]error{ecosystem: errors.New("inventory failed")}},
			}
			if !session.retainIncompleteManagedPackage(application) {
				t.Errorf("%s managed package was not handled", ecosystem)
				continue
			}
			actual := session.observed[application.ID]
			if actual.UpdateStatus != previous.UpdateStatus || actual.CurrentVersion != previous.CurrentVersion || !strings.Contains(actual.Error, "inventory failed") {
				t.Errorf("%s previous state was not preserved: %#v", ecosystem, actual)
			}
		}
	})
	t.Run("go-metadata-execution-failure-preserves-managed-package-state", func(t *testing.T) {
		directory := t.TempDir()
		goPath := filepath.Join(directory, "gopath")
		binDirectory := filepath.Join(goPath, "bin")
		if err := os.MkdirAll(binDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binDirectory, "sample"), []byte("not relevant"), 0o755); err != nil {
			t.Fatal(err)
		}
		managerDirectory := filepath.Join(directory, "manager")
		if err := os.Mkdir(managerDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		goScript := `#!/bin/sh
if [ "$1" = "env" ]; then
    printf '%s\n\n' "$FAKE_GOPATH"
    exit 0
fi
/bin/sleep 1
`
		if err := os.WriteFile(filepath.Join(managerDirectory, "go"), []byte(goScript), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", managerDirectory)
		t.Setenv("FAKE_GOPATH", goPath)
		disabled, enabled := false, true
		application := model.Application{
			ID: "pkg-go-sample", Name: "sample", Type: model.ApplicationTypePackage, InstallPath: filepath.Join(binDirectory, "sample"),
			Identity: "package:go:example.invalid/sample", ScanManaged: true,
		}
		previous := model.ManagedStatus{UpdateStatus: model.StatusCurrent, CurrentVersion: "1.2.3"}
		catalog := model.Config{Apps: []model.Application{application}, Settings: model.Settings{Scan: model.ScanSettings{
			Path: disabled, Application: disabled,
			Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: enabled, UV: disabled, Ruby: disabled},
		}}}
		catalog.Apps[0].StatusManaged = previous
		state := model.RuntimeState{}

		updatedCatalog, _, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: 20 * time.Millisecond}}).Scan(context.Background(), catalog, state)
		if err != nil {
			t.Fatal(err)
		}
		actual := updatedCatalog.Apps[0].StatusManaged
		if len(updatedCatalog.Apps) != 1 || actual.UpdateStatus != previous.UpdateStatus || actual.CurrentVersion != previous.CurrentVersion || actual.Error == "" {
			t.Fatalf("incomplete Go metadata scan did not preserve state: apps=%#v state=%#v", updatedCatalog.Apps, actual)
		}
	})
	t.Run("package-scanners-report-missing-managers-as-incomplete", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		disabled, enabled := false, true
		cases := []struct {
			name      string
			settings  model.PackageScanSettings
			ecosystem string
		}{
			{"python", model.PackageScanSettings{Python: enabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled}, string(handler.Python)},
			{"node", model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled}, string(handler.Node)},
			{"go", model.PackageScanSettings{Python: disabled, Node: disabled, Go: enabled, UV: disabled, Ruby: disabled}, string(handler.Go)},
			{"uv", model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: enabled, Ruby: disabled}, string(handler.UV)},
			{"ruby", model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: enabled}, string(handler.Ruby)},
		}
		for _, test := range cases {
			result := scanPackages(context.Background(), test.settings, runtimeutil.Runner{IdleTimeout: time.Second}, exclusionMatcher{}, nil, nil)
			if result.Complete[test.ecosystem] || result.Errors[test.ecosystem] == nil {
				t.Errorf("%s missing manager was not reported: %#v", test.name, result)
			}
		}
	})
	t.Run("package-scan-errors-use-localized-stable-messages", func(t *testing.T) {
		previous := i18n.Current()
		t.Cleanup(func() { i18n.Set(previous) })
		i18n.Set(i18n.Chinese)

		for _, err := range []error{
			&handler.PackageManagerUnavailableError{Manager: "npm"},
			&handler.PackageInventoryIncompleteError{Ecosystem: "Node.js", Message: "incomplete Node.js package inventory"},
		} {
			message := i18n.T("scanner.package_scan_incomplete", "node", packageScanErrorText(err))
			if strings.Contains(message, "package manager") || strings.Contains(message, "incomplete Node.js") {
				t.Fatalf("localized package scan message retained a known English error: %q", message)
			}
		}
	})
	t.Run("package-scanners-reject-failed-or-invalid-inventories", func(t *testing.T) {
		disabled, enabled := false, true
		cases := []struct {
			name      string
			binaries  map[string]string
			settings  model.PackageScanSettings
			ecosystem string
		}{
			{"python invalid JSON", map[string]string{"python3": "printf 'not-json\\n'; exit 0"}, model.PackageScanSettings{Python: enabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled}, string(handler.Python)},
			{"node partial nonzero", map[string]string{"npm": "printf '{\"dependencies\":{}}\\n'; exit 1"}, model.PackageScanSettings{Python: disabled, Node: enabled, Go: disabled, UV: disabled, Ruby: disabled}, string(handler.Node)},
			{"go nonzero", map[string]string{"go": "exit 1"}, model.PackageScanSettings{Python: disabled, Node: disabled, Go: enabled, UV: disabled, Ruby: disabled}, string(handler.Go)},
			{"uv invalid output", map[string]string{"uv": "printf 'unexpected output\\n'; exit 0"}, model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: enabled, Ruby: disabled}, string(handler.UV)},
			{"ruby invalid JSON", map[string]string{"ruby": "printf 'not-json\\n'; exit 0", "gem": "exit 0"}, model.PackageScanSettings{Python: disabled, Node: disabled, Go: disabled, UV: disabled, Ruby: enabled}, string(handler.Ruby)},
		}
		for _, test := range cases {
			directory := t.TempDir()
			for name, body := range test.binaries {
				path := filepath.Join(directory, name)
				if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", directory)
			result := scanPackages(context.Background(), test.settings, runtimeutil.Runner{IdleTimeout: time.Second}, exclusionMatcher{}, nil, nil)
			if result.Complete[test.ecosystem] || result.Errors[test.ecosystem] == nil {
				t.Errorf("%s failed inventory was accepted: %#v", test.name, result)
			}
		}
	})
	t.Run("python-install-info-protocol-violations-mark-ecosystem-incomplete", func(t *testing.T) {
		disabled, enabled := false, true
		for _, installInfo := range []string{
			`{"one":{"path":"/fixture/one","scope":"system","complete":true,"unknown":true}}`,
			`{"one":{"path":"/fixture/one","scope":"system","complete":true}} {}`,
		} {
			t.Run(installInfo, func(t *testing.T) {
				directory := t.TempDir()
				python := `#!/bin/sh
case "$*" in
  "-m pip list --not-required --format=json") printf '%s\n' '[{"name":"one","version":"1"}]' ;;
  "-m pip show one") exit 0 ;;
  "-c "*) printf '%s\n' '` + installInfo + `' ;;
  *) exit 1 ;;
esac
`
				if err := os.WriteFile(filepath.Join(directory, "python3"), []byte(python), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", directory)
				result := scanPackages(context.Background(), model.PackageScanSettings{Python: enabled, Node: disabled, Go: disabled, UV: disabled, Ruby: disabled}, runtimeutil.Runner{IdleTimeout: time.Second}, exclusionMatcher{}, nil, nil)
				if result.Complete[string(handler.Python)] || result.Errors[string(handler.Python)] == nil || len(result.Discoveries) != 0 {
					t.Fatalf("non-strict Python install-info was accepted: %#v", result)
				}
			})
		}
	})
	t.Run("cargo-unsafe-binary-inventory-produces-no-discovery", func(t *testing.T) {
		directory := t.TempDir()
		root := filepath.Join(directory, "cargo-root")
		cargo := filepath.Join(directory, "cargo")
		if err := os.WriteFile(cargo, []byte("#!/bin/sh\nprintf 'sample v1.2.3:\\n    ../escape\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", directory)
		t.Setenv("CARGO_INSTALL_ROOT", root)
		result := scanPackages(context.Background(), model.PackageScanSettings{Cargo: true}, runtimeutil.Runner{IdleTimeout: time.Second}, exclusionMatcher{}, nil, nil)
		if result.Complete[string(handler.Cargo)] || result.Errors[string(handler.Cargo)] == nil || len(result.Discoveries) != 0 {
			t.Fatalf("unsafe Cargo inventory result=%#v", result)
		}
	})
}

func TestScannerCancellationFlow(t *testing.T) {
	t.Run("cancels-after-package-command-start-without-returning-partial-discoveries", func(t *testing.T) {
		directory := t.TempDir()
		goPath := filepath.Join(directory, "gopath")
		binary := writeScannerFixture(t, filepath.Join(goPath, "bin", "sample"))
		managerDirectory := filepath.Join(directory, "manager")
		if err := os.Mkdir(managerDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(directory, "version-command-started")
		goScript := `#!/bin/sh
if [ "$1" = "env" ]; then
  printf '%s\n\n' "$FAKE_GOPATH"
  exit 0
fi
: > "$FAKE_SCAN_MARKER"
while :; do sleep 1; done
`
		if err := os.WriteFile(filepath.Join(managerDirectory, "go"), []byte(goScript), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", managerDirectory)
		t.Setenv("FAKE_GOPATH", goPath)
		t.Setenv("FAKE_SCAN_MARKER", marker)
		enabled, disabled := true, false
		baseline := model.Application{ID: "configured", Name: "Configured", Type: model.ApplicationTypeCLI, InstallPath: binary, ScanManaged: false, StatusManaged: model.ManagedStatus{CurrentVersion: "baseline", UpdateStatus: model.StatusCurrent}}
		catalog := model.Config{Apps: []model.Application{baseline}, Settings: model.Settings{Scan: model.ScanSettings{Path: disabled, Application: disabled, Packages: model.PackageScanSettings{Python: disabled, Node: disabled, Go: enabled, UV: disabled, Ruby: disabled}}}}
		state := model.RuntimeState{Observations: map[string]model.ScanObservation{"configured": {Found: true}}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cancelled := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(cancelled)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(marker); err == nil {
					cancel()
					return
				}
				select {
				case <-done:
					return
				case <-ticker.C:
				}
			}
		}()
		updated, updatedState, err := (Scanner{Runner: runtimeutil.Runner{IdleTimeout: time.Second}}).Scan(ctx, catalog, state)
		close(done)
		<-cancelled
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("scan error=%v, want context cancellation", err)
		}
		if len(updated.Apps) != 1 || !reflect.DeepEqual(updated.Apps[0], baseline) || !reflect.DeepEqual(updatedState, state) {
			t.Fatalf("cancelled scan returned partial mutation: apps=%#v state=%#v", updated.Apps, updatedState)
		}
	})
}
