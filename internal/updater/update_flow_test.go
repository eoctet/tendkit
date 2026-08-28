package updater

import (
	"fmt"

	"testing"

	"github.com/eoctet/tendkit/internal/model"
	providerpkg "github.com/eoctet/tendkit/internal/updater/provider"
	"os"

	"strings"

	"time"

	"context"
	"github.com/eoctet/tendkit/pkg/version"

	"errors"

	"path/filepath"

	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestUpdaterUpdateFlow(t *testing.T) {
	t.Run("update-verification-version-ordering-rejects-ambiguous-input", func(t *testing.T) {
		for _, test := range []struct {
			name, latest, current string
			newer                 bool
		}{
			{"prerelease", "1.2.0-rc1", "1.2.0-beta9", true},
			{"release over prerelease", "1.2.0", "1.2.0-rc1", true},
			{"build metadata", "2.0.0+darwin", "2.0.0", false},
			{"release prefix", "release-2.0.0", "1.9.9", true},
		} {
			if got := version.IsNewer(test.latest, test.current); got != test.newer {
				t.Errorf("%s: IsNewer(%q,%q)=%t", test.name, test.latest, test.current, got)
			}
		}
		if _, comparable := version.Compare("nightly-b", "nightly-a"); comparable {
			t.Fatal("unparseable versions were treated as comparable")
		}
		if version.AtLeast("nightly-b", "nightly-a") {
			t.Fatal("different unparseable versions passed update verification")
		}
		if _, err := version.Extract("no version here"); !errors.Is(err, version.ErrExtractFailed) {
			t.Fatalf("Extract error=%v", err)
		}
	})
	t.Run("command-output-carries-identity-and-failure-keeps-localized-cause", func(t *testing.T) {
		app := model.Application{ID: "sample", Name: "Sample"}
		var outputs []model.CommandOutput
		runner := commandRunner(model.Config{Settings: model.Settings{TimeoutSeconds: 1}}, Options{CommandOutput: func(output model.CommandOutput) { outputs = append(outputs, output) }})
		ctx := withOperation(withApplication(context.Background(), app), model.OperationCheck)
		if _, err := runner.Run(ctx, "printf stdout; printf stderr >&2", nil); err != nil {
			t.Fatal(err)
		}
		if len(outputs) != 3 || !outputs[2].Done || outputs[0].CommandID == 0 || outputs[0].CommandID != outputs[2].CommandID {
			t.Fatalf("outputs=%#v", outputs)
		}
		for _, output := range outputs {
			if output.AppID != app.ID || output.AppName != app.Name || output.Operation != model.OperationCheck {
				t.Fatalf("output identity=%#v", output)
			}
		}
		directory := t.TempDir()
		log, err := newLogger(filepath.Join(directory, "logs"))
		if err != nil {
			t.Fatal(err)
		}
		outer := localizedProviderError{text: "cannot read Sample version", cause: &providerpkg.Error{Key: "provider.current_failed", Cause: errors.New("metadata exit 23")}}
		if result := (&engine{logger: log}).fail(app, model.Result{}, model.OperationVersion, outer, 0); result.Message != outer.Error() {
			t.Fatalf("localized result=%#v", result)
		}
		data, err := os.ReadFile(filepath.Join(directory, "logs", runLogFile))
		if err != nil || !strings.Contains(string(data), "cannot read Sample version: metadata exit 23") {
			t.Fatalf("cause log=%q err=%v", data, err)
		}
	})
	t.Run("operation-results-and-logs-redact-application-secrets", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "installed")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		const secret = "version-command-secret"
		app := model.Application{ID: "sample", Name: "Sample", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Environment: map[string]string{"SERVICE_TOKEN": secret}, Provider: providerConfig(model.ProviderDefault, "printf '%s\\n' \"$SERVICE_TOKEN\" >&2; exit 7", "", "", nil)}
		logger, err := newLogger(filepath.Join(directory, "logs"))
		if err != nil {
			t.Fatal(err)
		}
		worker := engine{Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}}, logger: logger, checker: testEngineChecker(t, runtimeutil.Runner{})}
		state, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{CheckOnly: true})
		if len(results) != 1 || results[0].Status != model.StatusFailed || strings.Contains(results[0].Message, secret) || !strings.Contains(configStatus(state, app.ID).Error, "[REDACTED]") {
			t.Fatalf("secret result=%#v state=%#v", results, state)
		}
		data, err := os.ReadFile(filepath.Join(directory, "logs", runLogFile))
		if err != nil || strings.Contains(string(data), secret) {
			t.Fatalf("secret log=%q err=%v", data, err)
		}
	})
	t.Run("check-operation-preserves-command-output-context", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "installed")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := model.Application{ID: "codex", Name: "Codex", InstallPath: installed, Enabled: true, UpdateMode: model.ModeAuto, StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}}
		worker := engine{checker: checkerFunc(func(ctx context.Context, _ model.Application, _ string) (string, error) {
			if got := operationFromContext(ctx); got != model.OperationCheck {
				t.Fatalf("operation context = %q", got)
			}
			return "1.0.0", nil
		})}
		if got := worker.process(context.Background(), app, app.StatusManaged, RunOptions{CheckOnly: true}); got.Status != model.StatusCurrent {
			t.Fatalf("check result = %#v", got)
		}
	})
	t.Run("auto-update-with-multiline-commands", func(t *testing.T) {
		dir := t.TempDir()
		versionFile := filepath.Join(dir, "version.txt")
		if err := os.WriteFile(versionFile, []byte("1.0.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		commandRunner := runtimeutil.Runner{IdleTimeout: time.Second}
		app := model.Application{
			ID: "sample", Name: "Sample", Type: "cli", InstallPath: versionFile, Enabled: true,
			UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderDefault,
				fmt.Sprintf("set -e\ncat %q", versionFile), "printf 'The latest version: 1.1.0\\n'", fmt.Sprintf("set -e\nprintf '1.1.0\\n' > %q", versionFile), nil),
		}
		catalog := model.Config{SchemaVersion: model.SchemaVersion, Settings: model.Settings{Workers: 2, TimeoutSeconds: 1, Downloader: testDownloaderSettings("aria2c", dir)}, Apps: []model.Application{app}}
		worker := engine{Config: catalog, checker: testEngineChecker(t, commandRunner)}
		state, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{})
		if len(results) != 1 || results[0].Status != "updated" {
			t.Fatalf("unexpected results %#v", results)
		}
		if configStatus(state, app.ID).CurrentVersion != "1.1.0" || configStatus(state, app.ID).HasUpdate {
			t.Fatalf("unexpected state %#v", configStatus(state, app.ID))
		}
	})
	t.Run("check-only-does-not-execute-configured-auto-update", func(t *testing.T) {
		dir := t.TempDir()
		versionFile := filepath.Join(dir, "version.txt")
		if err := os.WriteFile(versionFile, []byte("1.0.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		commandRunner := runtimeutil.Runner{IdleTimeout: time.Second}
		app := model.Application{
			ID: "sample", Name: "Sample", Type: "cli", InstallPath: versionFile, Enabled: true,
			UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderDefault,
				fmt.Sprintf("cat %q", versionFile), "printf 'The latest version: 1.1.0\\n'", fmt.Sprintf("printf '1.1.0\\n' > %q", versionFile), nil),
		}
		catalog := model.Config{SchemaVersion: model.SchemaVersion, Settings: model.Settings{Workers: 1, TimeoutSeconds: 1}, Apps: []model.Application{app}}
		worker := engine{Config: catalog, checker: testEngineChecker(t, commandRunner)}
		_, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{CheckOnly: true})
		if len(results) != 1 || results[0].Status != "update_available" || results[0].Mode != model.ModeCheck {
			t.Fatalf("unexpected check-only result %#v", results)
		}
		content, err := os.ReadFile(versionFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "1.0.0\n" {
			t.Fatalf("check-only operation updated the application: %q", content)
		}
	})
	t.Run("install-mode-stops-when-update-fails", func(t *testing.T) {
		dir := t.TempDir()
		versionFile := filepath.Join(dir, "version.txt")
		marker := filepath.Join(dir, "updated")
		if err := os.WriteFile(versionFile, []byte("1.0.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := model.Application{
			ID: "sample", Name: "Sample", InstallPath: versionFile, Enabled: true, UpdateMode: model.ModeInstall,
			Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{Version: fmt.Sprintf("cat %q", versionFile), Check: "printf '1.1.0\\n'", Update: "exit 7", Install: fmt.Sprintf("touch %q", marker)}},
		}
		engine := engine{Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}}, checker: testEngineChecker(t, runtimeutil.Runner{})}
		_, results := runFixedRequest(&engine, context.Background(), engine.Config, RunOptions{})
		if len(results) != 1 || results[0].Status != model.StatusFailed || results[0].Mode != model.ModeInstall {
			t.Fatalf("install result = %#v", results)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("install ran after failed update: %v", err)
		}
	})
	t.Run("install-mode-executes-update-then-install-and-verifies-version", func(t *testing.T) {
		dir := t.TempDir()
		versionFile := filepath.Join(dir, "version.txt")
		updateMarker := filepath.Join(dir, "updated")
		events := filepath.Join(dir, "events")
		if err := os.WriteFile(versionFile, []byte("1.0.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := model.Application{
			ID: "sample", Name: "Sample", InstallPath: versionFile, Enabled: true, UpdateMode: model.ModeInstall,
			Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{
				Version: fmt.Sprintf("cat %q", versionFile),
				Check:   "printf '2.0.0\\n'",
				Update:  fmt.Sprintf("printf update >> %q; touch %q", events, updateMarker),
				Install: fmt.Sprintf("printf install >> %q; printf '2.0.0\\n' > %q", events, versionFile),
			}},
		}
		runner := runtimeutil.Runner{IdleTimeout: time.Second}
		engine := engine{Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}}, checker: testEngineChecker(t, runner)}
		state, results := runFixedRequest(&engine, context.Background(), engine.Config, RunOptions{})
		if len(results) != 1 || results[0].Status != model.StatusUpdated || results[0].Mode != model.ModeInstall {
			t.Fatalf("install result = %#v", results)
		}
		if got := configStatus(state, app.ID).CurrentVersion; got != "2.0.0" {
			t.Fatalf("installed version = %q", got)
		}
		if _, err := os.Stat(updateMarker); err != nil {
			t.Fatalf("install mode did not execute update action: %v", err)
		}
		sequence, err := os.ReadFile(events)
		if err != nil || string(sequence) != "updateinstall" {
			t.Fatalf("install action sequence = %q, %v", sequence, err)
		}
	})
	t.Run("install-failure-retains-successful-update-operation-record", func(t *testing.T) {
		directory := t.TempDir()
		versionFile := filepath.Join(directory, "version.txt")
		if err := os.WriteFile(versionFile, []byte("1.0.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		logger, err := newLogger(filepath.Join(directory, "logs"))
		if err != nil {
			t.Fatal(err)
		}
		app := model.Application{ID: "sample", Name: "Sample", InstallPath: versionFile, Enabled: true, UpdateMode: model.ModeInstall, Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{Version: fmt.Sprintf("cat %q", versionFile), Check: "printf '2.0.0\\n'", Update: "true", Install: "exit 9"}}}
		runner := runtimeutil.Runner{IdleTimeout: time.Second}
		engine := engine{Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}}, checker: testEngineChecker(t, runner), logger: logger}
		_, results := runFixedRequest(&engine, context.Background(), engine.Config, RunOptions{})
		if len(results) != 1 || results[0].Status != model.StatusFailed {
			t.Fatalf("install result=%#v", results)
		}
		runLog, err := os.ReadFile(filepath.Join(directory, "logs", runLogFile))
		if err != nil {
			t.Fatal(err)
		}
		content := string(runLog)
		if strings.Count(content, `"operation":"update"`) != 1 || !strings.Contains(content, `"operation":"install"`) {
			t.Fatalf("missing update/install operation records: %s", content)
		}
	})
	t.Run("engine-version-action-renders-placeholders-and-rejects-invalid-templates", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "installed")
		marker := filepath.Join(directory, "rendered-as-shell")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := model.Application{
			ID: "sample", Name: "Sample; touch " + marker, InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck,
			Provider: providerConfig(model.ProviderDefault, "test {name} = {name} && test {install_path} = '"+installed+"' && printf '1.0.0\\n' # {{.Version}}", "printf '1.0.0\\n'", "", nil),
		}
		engine := engine{Config: model.Config{Settings: model.Settings{Workers: 1, Downloader: model.DownloaderSettings{StorePath: "/tmp/downloads"}}, Apps: []model.Application{app}}, checker: testEngineChecker(t, runtimeutil.Runner{})}
		if version, err := engine.detectVersion(context.Background(), app, ""); err != nil || version != "1.0.0" {
			t.Fatalf("rendered version = %q, %v", version, err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("version placeholder escaped shell quoting: %v", err)
		}
		for _, action := range []string{"printf {unknown}", "printf {install_path"} {
			app.Provider.Actions.Version = action
			if _, err := engine.detectVersion(context.Background(), app, ""); err == nil {
				t.Fatalf("invalid version action %q was accepted", action)
			}
		}
	})
	t.Run("engine-emits-updating-after-check-before-update-result", func(t *testing.T) {
		dir := t.TempDir()
		versionFile := filepath.Join(dir, "version.txt")
		if err := os.WriteFile(versionFile, []byte("1.0.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := model.Application{
			ID: "sample", Name: "Sample", InstallPath: versionFile, Enabled: true,
			UpdateMode: model.ModeAuto, Provider: providerConfig(model.ProviderDefault, fmt.Sprintf("cat %q", versionFile), "printf '2.0.0\\n'", fmt.Sprintf("printf '2.0.0\\n' > %q", versionFile), nil),
		}
		var events []string
		worker := engine{
			Config:      model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}},
			checker:     testEngineChecker(t, runtimeutil.Runner{IdleTimeout: time.Second}),
			AppStart:    func(result model.Result) { events = append(events, result.Status) },
			UpdateStart: func(result model.Result) { events = append(events, result.Status) },
			Output:      func(result model.Result) { events = append(events, result.Status) },
		}
		runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{})
		if strings.Join(events, ",") != "checking,updating,updated" {
			t.Fatalf("events = %#v", events)
		}
	})
	t.Run("placeholders-use-node-distribution-architecture", func(t *testing.T) {
		want := runtimeutil.HostPlatform().Architecture
		if got := placeholders(model.Application{}, model.ManagedStatus{}, "")["arch"]; got != want {
			t.Fatalf("architecture placeholder = %q, want %q", got, want)
		}
	})
	t.Run("auto-update-requires-installed-version-to-reach-latest", func(t *testing.T) {
		for _, test := range []struct {
			name      string
			installed string
		}{
			{name: "partial update", installed: "1.1.0"},
			{name: "downgrade", installed: "0.9.0"},
		} {
			t.Run(test.name, func(t *testing.T) {
				dir := t.TempDir()
				versionFile := filepath.Join(dir, "version.txt")
				if err := os.WriteFile(versionFile, []byte("1.0.0\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				commandRunner := runtimeutil.Runner{IdleTimeout: time.Second}
				app := model.Application{
					ID: "sample", Name: "Sample", InstallPath: versionFile, Enabled: true, UpdateMode: model.ModeAuto,
					Provider: providerConfig(model.ProviderDefault, fmt.Sprintf("cat %q", versionFile), "printf '2.0.0\\n'", fmt.Sprintf("printf '%s\\n' %q > %q", test.installed, test.installed, versionFile), nil),
				}
				catalog := model.Config{SchemaVersion: model.SchemaVersion, Settings: model.Settings{Workers: 1, TimeoutSeconds: 1}, Apps: []model.Application{app}}
				worker := engine{Config: catalog, checker: testEngineChecker(t, commandRunner)}
				state, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{})
				if len(results) != 1 || results[0].Status != "failed" {
					t.Fatalf("unexpected results %#v", results)
				}
				if !configStatus(state, app.ID).HasUpdate || configStatus(state, app.ID).LatestVersion != "2.0.0" {
					t.Fatalf("failed verification cleared update state: %#v", configStatus(state, app.ID))
				}
			})
		}
	})
	t.Run("installed-version-verified-supports-known-and-unknown-latest-versions", func(t *testing.T) {
		for _, test := range []struct {
			name                       string
			installed, current, latest string
			want                       bool
		}{
			{name: "known version reached", installed: "2.0.0", current: "1.0.0", latest: "2.0.0", want: true},
			{name: "known version not reached", installed: "1.1.0", current: "1.0.0", latest: "2.0.0"},
			{name: "unknown latest changed", installed: "nightly-b", current: "nightly-a", latest: version.Available, want: true},
			{name: "unknown latest unchanged", installed: "nightly-a", current: "nightly-a", latest: version.Available},
			{name: "empty installed", current: "nightly-a", latest: version.Available},
		} {
			t.Run(test.name, func(t *testing.T) {
				if got := installedVersionVerified(test.installed, test.current, test.latest); got != test.want {
					t.Fatalf("installedVersionVerified() = %v, want %v", got, test.want)
				}
			})
		}
	})
}
