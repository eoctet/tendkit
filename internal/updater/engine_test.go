package updater

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	providerpkg "github.com/eoctet/tendkit/internal/updater/provider"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

const runLogFile = "run.log"

type testDownloadWriteCloser struct {
	io.Writer
	close func()
}

func (writer testDownloadWriteCloser) Close() error {
	if writer.close != nil {
		writer.close()
	}
	return nil
}

func newLogger(dir string) (*logutil.Logger, error) { return logutil.NewLogger(dir) }

func runFixedRequest(engine *engine, ctx context.Context, config model.Config, options RunOptions) (model.Config, []model.Result) {
	return engine.runBatch(ctx, config, newBatch(options))
}

func newBatch(initial RunOptions) *batch {
	return &batch{requests: []RunOptions{initial}, notify: make(chan struct{}, 1)}
}

func testEngineChecker(t *testing.T, runner runtimeutil.Runner) *providerResolver {
	t.Helper()
	checker, err := newProviderResolver(runner, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return checker
}

func testDownloaderSettings(binary, directory string) model.DownloaderSettings {
	return model.DownloaderSettings{CLI: binary, StorePath: directory}
}

func TestCommandRunnerOutputCarriesApplicationIdentity(t *testing.T) {
	app := model.Application{ID: "sample", Name: "Sample"}
	var outputs []model.CommandOutput
	runner := commandRunner(model.Config{Settings: model.Settings{TimeoutSeconds: 1}}, Options{
		CommandOutput: func(output model.CommandOutput) { outputs = append(outputs, output) },
	})
	ctx := withOperation(withApplication(context.Background(), app), model.OperationCheck)
	if _, err := runner.Run(ctx, "printf stdout; printf stderr >&2", nil); err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 3 || !outputs[2].Done || outputs[0].CommandID == 0 || outputs[0].CommandID != outputs[1].CommandID || outputs[1].CommandID != outputs[2].CommandID {
		t.Fatalf("outputs = %#v, want stdout, stderr, and one matching completion", outputs)
	}
	for _, output := range outputs {
		if output.AppID != app.ID || output.AppName != app.Name || output.Operation != model.OperationCheck {
			t.Fatalf("output identity = %#v, want %q/%q/%q", output, app.ID, app.Name, model.OperationCheck)
		}
	}
}

func TestEngineCheckOnlySetsCheckOperationForCommandOutputContext(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: "codex", Name: "OpenAI Codex CLI", InstallPath: installed, Enabled: true, UpdateMode: model.ModeAuto, StatusManaged: model.ManagedStatus{CurrentVersion: "0.149.1"}}
	checker := checkerFunc(func(ctx context.Context, _ model.Application, _ string) (string, error) {
		if operation := operationFromContext(ctx); operation != model.OperationCheck {
			t.Fatalf("operation context = %q, want %q", operation, model.OperationCheck)
		}
		return "0.149.1", nil
	})
	result := (&engine{checker: checker}).process(context.Background(), app, app.StatusManaged, RunOptions{CheckOnly: true})
	if result.Status != model.StatusCurrent {
		t.Fatalf("result = %#v", result)
	}
}

func TestCheckIgnoresScanExclusionAndOwnership(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	application := model.Application{
		ID: "sample", Name: "Sample", InstallPath: installed, Enabled: true,
		UpdateMode: model.ModeCheck, ScanManaged: false,
		StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"},
	}
	catalog := model.Config{
		Settings: model.Settings{Workers: 1, Scan: model.ScanSettings{Exclude: []string{application.ID}}},
		Apps:     []model.Application{application},
	}
	engine := engine{Config: catalog, checker: checkerFunc(func(context.Context, model.Application, string) (string, error) {
		return "1.0.0", nil
	})}

	updated, results := runFixedRequest(&engine, context.Background(), catalog, RunOptions{Names: []string{application.ID}, CheckOnly: true})
	if len(results) != 1 || results[0].Status != model.StatusCurrent || configStatus(updated, application.ID).UpdateStatus != model.StatusCurrent {
		t.Fatalf("scan exclusion or ownership blocked check: results=%#v status=%#v", results, configStatus(updated, application.ID))
	}
}

func TestUpdaterRunClosesBatchAndWritesOneTerminalEvent(t *testing.T) {
	for _, test := range []struct {
		name       string
		persistErr error
		event      string
	}{
		{name: "success", event: logEventRunFinished},
		{name: "persist failure", persistErr: errors.New("persist failed"), event: logEventRunFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			installed := filepath.Join(directory, "installed")
			if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
				t.Fatal(err)
			}
			catalog := model.Config{Settings: model.Settings{Workers: 1, LogDir: filepath.Join(directory, "logs")}, Apps: []model.Application{{ID: "sample", Name: "Sample", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{Version: "printf '1.0.0\\n'", Check: "printf '1.0.0\\n'"}}}}}
			facade, err := New(catalog, Options{LogDir: catalog.Settings.LogDir})
			if err != nil {
				t.Fatal(err)
			}
			if err := facade.Add(RunOptions{CheckOnly: true}); err != nil {
				t.Fatal(err)
			}
			_, _, err = facade.Run(context.Background(), func(updated model.Config, results []model.Result) (model.Config, error) {
				return updated, test.persistErr
			})
			if (err != nil) != (test.persistErr != nil) {
				t.Fatalf("Run error=%v", err)
			}
			if err := facade.Add(RunOptions{CheckOnly: true}); err == nil {
				t.Fatal("closed updater batch accepted an addition")
			}
			data, err := os.ReadFile(filepath.Join(catalog.Settings.LogDir, runLogFile))
			if err != nil {
				t.Fatal(err)
			}
			content := string(data)
			if strings.Count(content, `"event":"run_started"`) != 1 || strings.Count(content, `"event":"`+test.event+`"`) != 1 {
				t.Fatalf("run log = %s", content)
			}
			other := logEventRunFailed
			if test.event == logEventRunFailed {
				other = logEventRunFinished
			}
			if strings.Contains(content, `"event":"`+other+`"`) {
				t.Fatalf("unexpected terminal event: %s", content)
			}
		})
	}
}

func TestEngineBatchAddsWorkToActiveWorkerPool(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	apps := []model.Application{
		{ID: "first", Name: "First", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck},
		{ID: "second", Name: "Second", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck},
	}
	started := make(chan string, len(apps))
	release := make(chan struct{})
	worker := engine{
		Config: model.Config{Settings: model.Settings{Workers: 2}, Apps: apps},
		checker: checkerFunc(func(_ context.Context, app model.Application, _ string) (string, error) {
			started <- app.ID
			<-release
			return "1.0.0", nil
		}),
	}
	state := configWithStatuses(worker.Config, map[string]model.ManagedStatus{
		"first":  {CurrentVersion: "1.0.0"},
		"second": {CurrentVersion: "1.0.0"},
	})
	batch := newBatch(RunOptions{Names: []string{"first"}, CheckOnly: true})
	type outcome struct {
		state   model.Config
		results []model.Result
	}
	finished := make(chan outcome, 1)
	go func() {
		updated, results := worker.runBatch(context.Background(), state, batch)
		finished <- outcome{state: updated, results: results}
	}()

	if id := <-started; id != "first" {
		t.Fatalf("first worker started %q", id)
	}
	if err := batch.add(RunOptions{Names: []string{"second"}, CheckOnly: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-started:
		if id != "second" {
			t.Fatalf("dynamic worker started %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("second application did not join the active worker pool")
	}
	close(release)
	result := <-finished
	if len(result.results) != 2 || result.results[0].AppID != "first" || result.results[1].AppID != "second" {
		t.Fatalf("batch results = %#v", result.results)
	}
	if configStatus(result.state, "first").UpdateStatus != "current" || configStatus(result.state, "second").UpdateStatus != "current" {
		t.Fatalf("batch state = %#v", result.state.Apps)
	}
	if err := batch.add(RunOptions{Names: []string{"first"}}); err == nil {
		t.Fatalf("closed batch accepted work: %v", err)
	}
}

func TestEngineBatchCanRescheduleCompletedApplication(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	apps := []model.Application{
		{ID: "repeat", Name: "Repeat", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck},
		{ID: "blocker", Name: "Blocker", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck},
	}
	started := make(chan string, 3)
	completed := make(chan string, 3)
	releaseBlocker := make(chan struct{})
	worker := engine{
		Config: model.Config{Settings: model.Settings{Workers: 2}, Apps: apps},
		checker: checkerFunc(func(_ context.Context, app model.Application, _ string) (string, error) {
			started <- app.ID
			if app.ID == "blocker" {
				<-releaseBlocker
			}
			return "1.0.0", nil
		}),
		Output: func(result model.Result) { completed <- result.AppID },
	}
	state := configWithStatuses(worker.Config, map[string]model.ManagedStatus{
		"repeat":  {CurrentVersion: "1.0.0"},
		"blocker": {CurrentVersion: "1.0.0"},
	})
	batch := newBatch(RunOptions{Names: []string{"repeat", "blocker"}, CheckOnly: true})
	results := make(chan []model.Result, 1)
	go func() {
		_, items := worker.runBatch(context.Background(), state, batch)
		results <- items
	}()

	seenStarted := map[string]bool{}
	for len(seenStarted) < 2 {
		seenStarted[<-started] = true
	}
	if id := <-completed; id != "repeat" {
		t.Fatalf("first completed application = %q", id)
	}
	if err := batch.add(RunOptions{Names: []string{"repeat"}, CheckOnly: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-started:
		if id != "repeat" {
			t.Fatalf("rescheduled application = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("completed application did not rejoin the active worker pool")
	}
	close(releaseBlocker)
	items := <-results
	if len(items) != 3 || items[0].AppID != "repeat" || items[1].AppID != "blocker" || items[2].AppID != "repeat" {
		t.Fatalf("rescheduled results = %#v", items)
	}
}

type checkerFunc func(context.Context, model.Application, string) (string, error)

type currentErrorChecker struct{ checkerFunc }

func (currentErrorChecker) current(context.Context, model.Application, string) (currentResolution, error) {
	return currentResolution{Version: "1.0.0"}, providerpkg.CapabilityUnavailable("default", providerpkg.CapabilityCurrent)
}

func (function checkerFunc) latest(ctx context.Context, app model.Application, current string) (string, error) {
	return function(ctx, app, current)
}

func (checkerFunc) current(ctx context.Context, app model.Application, fallback string) (currentResolution, error) {
	return testCurrent(ctx, app, fallback)
}

func TestEngineDoesNotTreatPersistedVersionAsSuccessfulCurrentDetection(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: "sample", Name: "Sample", Enabled: true, InstallPath: installed, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}}
	catalog := model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}}
	checker := currentErrorChecker{checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil })}
	_, results := runFixedRequest(&engine{Config: catalog, checker: checker}, context.Background(), catalog, RunOptions{CheckOnly: true})
	if len(results) != 1 || results[0].Status != model.StatusFailed || results[0].State.CurrentVersion != "1.0.0" || !strings.Contains(results[0].Message, "current") {
		t.Fatalf("persisted current fallback result = %#v", results)
	}
}

func (checkerFunc) executeUpdate(context.Context, model.Application, model.ManagedStatus) (runtimeutil.Result, error) {
	return runtimeutil.Result{}, providerpkg.ErrUnavailable
}

func (checkerFunc) executeInstall(context.Context, model.Application, model.ManagedStatus) (runtimeutil.Result, error) {
	return runtimeutil.Result{}, providerpkg.ErrUnavailable
}

func (checkerFunc) resolveDownload(_ context.Context, app model.Application, _ model.ManagedStatus, _ ...string) (downloadResolution, error) {
	spec := app.Provider.DownloadAction()
	if spec == nil {
		return downloadResolution{}, providerpkg.ErrUnavailable
	}
	return downloadResolution{Spec: *spec}, nil
}

func (checkerFunc) downloadAssetCandidates(context.Context, model.Application) (model.DownloadAssetChoices, error) {
	return model.DownloadAssetChoices{}, nil
}

func (checkerFunc) httpSource() *providerpkg.HTTPSource { return nil }

type assetCandidateChecker struct {
	checkerFunc
	choices  map[string]model.DownloadAssetChoices
	failures map[string]error
	calls    map[string]int
}

func (checker assetCandidateChecker) downloadAssetCandidates(_ context.Context, app model.Application) (model.DownloadAssetChoices, error) {
	if checker.calls != nil {
		checker.calls[app.ID]++
	}
	if err := checker.failures[app.ID]; err != nil {
		return model.DownloadAssetChoices{}, err
	}
	return checker.choices[app.ID], nil
}

func TestDownloadAssetCandidatesCollectsApplicationFailures(t *testing.T) {
	apps := []model.Application{
		{ID: "failed", Name: "Failed", StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}},
		{ID: "empty", Name: "Empty", StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}},
		{ID: "ready", Name: "Ready", StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}},
	}
	updater := &Updater{engine: engine{checker: assetCandidateChecker{
		checkerFunc: checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil }),
		choices: map[string]model.DownloadAssetChoices{
			"empty": {Candidates: []string{}},
			"ready": {Candidates: []string{"ready.dmg"}, SelectionRequired: true},
		},
		failures: map[string]error{"failed": errors.New("provider failed")},
	}}}

	var progress []model.DownloadAssetPreflightProgress
	choices, failures, err := updater.DownloadAssetCandidates(context.Background(), apps, func(event model.DownloadAssetPreflightProgress) {
		progress = append(progress, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(choices["empty"].Candidates) != 0 || len(choices["ready"].Candidates) != 1 || choices["ready"].Candidates[0] != "ready.dmg" || !choices["ready"].SelectionRequired {
		t.Fatalf("choices = %#v", choices)
	}
	if len(failures) != 1 || failures["failed"] == nil {
		t.Fatalf("failures = %#v", failures)
	}
	wantStages := []model.DownloadAssetPreflightStage{
		model.DownloadAssetPreflightStarted, model.DownloadAssetPreflightFailed,
		model.DownloadAssetPreflightStarted, model.DownloadAssetPreflightCompleted,
		model.DownloadAssetPreflightStarted, model.DownloadAssetPreflightCompleted,
	}
	if len(progress) != len(wantStages) {
		t.Fatalf("progress = %#v", progress)
	}
	for index, stage := range wantStages {
		if progress[index].Stage != stage {
			t.Fatalf("progress[%d] stage=%q want=%q", index, progress[index].Stage, stage)
		}
	}
	if progress[5].AppID != "ready" || progress[5].AppName != "Ready" || progress[5].CandidateCount != 1 {
		t.Fatalf("ready progress = %#v", progress[5])
	}
}

func TestDownloadAssetCandidatesSkipsSelectionWhenVersionIsCurrent(t *testing.T) {
	calls := map[string]int{}
	app := model.Application{ID: "current", Name: "Current", StatusManaged: model.ManagedStatus{CurrentVersion: "2.0.0"}}
	updater := &Updater{engine: engine{checker: assetCandidateChecker{
		checkerFunc: checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil }),
		choices:     map[string]model.DownloadAssetChoices{"current": {Candidates: []string{"current.dmg"}, SelectionRequired: true}},
		calls:       calls,
	}}}

	choices, failures, err := updater.DownloadAssetCandidates(context.Background(), []model.Application{app}, nil)
	if err != nil || len(choices) != 0 || len(failures) != 0 || calls[app.ID] != 0 {
		t.Fatalf("current preflight choices=%#v failures=%#v calls=%d err=%v", choices, failures, calls[app.ID], err)
	}
}

func TestDownloadAssetCandidatesReturnsCancellationAsGlobalError(t *testing.T) {
	updater := &Updater{engine: engine{checker: assetCandidateChecker{failures: map[string]error{"cancelled": context.Canceled}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	choices, failures, err := updater.DownloadAssetCandidates(ctx, []model.Application{{ID: "cancelled"}, {ID: "later"}}, nil)
	if !errors.Is(err, context.Canceled) || choices != nil || failures != nil {
		t.Fatalf("cancelled preflight choices=%#v failures=%#v err=%v", choices, failures, err)
	}
}

type artifactChecker struct {
	download model.Download
	artifact string
}

type selectedArtifactCaptureChecker struct {
	artifactChecker
	selected string
}

func (checker *selectedArtifactCaptureChecker) resolveDownload(_ context.Context, _ model.Application, _ model.ManagedStatus, selected ...string) (downloadResolution, error) {
	if len(selected) > 0 {
		checker.selected = selected[0]
	}
	return downloadResolution{}, errors.New("stop after selected artifact capture")
}

func TestEnginePassesRunOptionDownloadAssetToResolver(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: "github", Name: "GitHub", Enabled: true, InstallPath: installed, UpdateMode: model.ModeDownload, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}}
	checker := &selectedArtifactCaptureChecker{}
	worker := engine{Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}}, checker: checker}
	_, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{DownloadAssets: map[string]string{app.ID: "second.dmg"}})
	if checker.selected != "second.dmg" || len(results) != 1 || results[0].Status != model.StatusFailed {
		t.Fatalf("engine resolver selection=%q results=%#v", checker.selected, results)
	}
}

func (checker artifactChecker) latest(context.Context, model.Application, string) (string, error) {
	return "2.0.0", nil
}

func (checker artifactChecker) current(ctx context.Context, app model.Application, fallback string) (currentResolution, error) {
	return testCurrent(ctx, app, fallback)
}

func (artifactChecker) executeUpdate(context.Context, model.Application, model.ManagedStatus) (runtimeutil.Result, error) {
	return runtimeutil.Result{}, providerpkg.ErrUnavailable
}

func (artifactChecker) executeInstall(context.Context, model.Application, model.ManagedStatus) (runtimeutil.Result, error) {
	return runtimeutil.Result{}, providerpkg.ErrUnavailable
}

func (checker artifactChecker) resolveDownload(_ context.Context, app model.Application, _ model.ManagedStatus, _ ...string) (downloadResolution, error) {
	spec := app.Provider.DownloadAction()
	if spec == nil {
		return downloadResolution{}, providerpkg.ErrUnavailable
	}
	resolved := *spec
	if resolved.URL == "" {
		resolved.URL = checker.download.URL
		if resolved.Filename == "" {
			resolved.Filename = checker.download.Filename
		}
	}
	if resolved.ChecksumEnabled && resolved.ChecksumValue == "" && resolved.ChecksumURL == "" && app.Provider.Type == model.ProviderGitHubRelease {
		resolved.ChecksumValue = checker.download.ChecksumValue
	}
	return downloadResolution{Spec: resolved, Artifact: checker.artifact}, nil
}

func (artifactChecker) httpSource() *providerpkg.HTTPSource { return nil }
func (artifactChecker) downloadAssetCandidates(context.Context, model.Application) (model.DownloadAssetChoices, error) {
	return model.DownloadAssetChoices{}, nil
}

func testCurrent(ctx context.Context, app model.Application, fallback string) (currentResolution, error) {
	if strings.TrimSpace(app.Provider.VersionAction()) == "" {
		return currentResolution{Version: fallback}, nil
	}
	state := app.StatusManaged
	state.CurrentVersion = fallback
	request := providerpkg.Request{App: app, CurrentVersion: fallback, Values: placeholders(app, state, "")}
	started := time.Now()
	version, err := providerpkg.ActionCapabilities(runtimeutil.Runner{IdleTimeout: time.Second}, request, app.Provider.Type == model.ProviderDefault).Current.Current(ctx, request)
	return currentResolution{Version: version, Duration: time.Since(started), FromAction: true}, err
}

func TestAutoUpdateWithMultilineCommands(t *testing.T) {
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
}

func TestRedactDoesNotTreatOrdinaryEnvironmentValuesAsSecrets(t *testing.T) {
	const pipError = `Update command exited with code 1: Could not find pypdf==6.16.0 (from versions: 1.4, 3.1.0, 6.15.0)`
	actual := redact(pipError, map[string]string{
		"PIP_DISABLE_PIP_VERSION_CHECK": "1",
		"GOBIN":                         "/tmp/go/bin",
	})
	if actual != pipError {
		t.Fatalf("ordinary environment values corrupted output:\n%s", actual)
	}
}

func TestRedactMasksOnlySensitiveEnvironmentValues(t *testing.T) {
	const secret = "sample-secret-token"
	actual := redact("request failed with token "+secret, map[string]string{
		"SERVICE_TOKEN": secret,
		"FEATURE_FLAG":  "request",
	})
	if strings.Contains(actual, secret) || actual != "request failed with token [REDACTED]" {
		t.Fatalf("unexpected redaction %q", actual)
	}
}

func TestEngineRegistersApplicationSecretsBeforeOperationCommandOutput(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	const secret = "early-command-token"
	log, err := newLogger(filepath.Join(directory, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: "sample", Name: "Sample", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Environment: map[string]string{"SERVICE_TOKEN": secret}}
	worker := engine{Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}}, logger: log, checker: checkerFunc(func(context.Context, model.Application, string) (string, error) {
		lines, err := log.Operation("INFO", "check", "sample", "stdout "+secret)
		if err != nil || strings.Contains(strings.Join(lines, "\n"), secret) {
			t.Fatalf("early output leaked: %q, %v", lines, err)
		}
		return "1.0.0", nil
	})}
	worker.process(context.Background(), app, model.ManagedStatus{}, RunOptions{CheckOnly: true})
}

func TestUpdaterRegistersSecretsBeforeAppStartOperationLog(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	const secret = "app-start-token"
	log, err := newLogger(filepath.Join(directory, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: "sample", Name: "Sample", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck, Environment: map[string]string{"SERVICE_TOKEN": secret}, Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'", "", nil)}
	updater, err := New(model.Config{Settings: model.Settings{Workers: 1, LogDir: filepath.Join(directory, "logs")}, Apps: []model.Application{app}}, Options{LogDir: filepath.Join(directory, "logs"), Logger: log, AppStart: func(model.Result) {
		lines, operationErr := log.Operation("INFO", "check", secret, "started "+secret)
		if operationErr != nil || strings.Contains(strings.Join(lines, "\n"), secret) {
			t.Fatalf("AppStart operation leak: %q, %v", lines, operationErr)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.Add(RunOptions{CheckOnly: true}); err != nil {
		t.Fatal(err)
	}
	_, _, err = updater.Run(context.Background(), func(catalog model.Config, _ []model.Result) (model.Config, error) { return catalog, nil })
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "logs", runLogFile))
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatalf("AppStart JSONL leaked: %q, %v", data, err)
	}
}

func TestVersionCommandFailureDoesNotPersistSecret(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	const secret = "version-command-secret"
	app := model.Application{
		ID: "sample", Name: "Sample", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck,
		Environment: map[string]string{"SERVICE_TOKEN": secret},
		Provider:    providerConfig(model.ProviderDefault, "printf '%s\\n' \"$SERVICE_TOKEN\" >&2; exit 7", "", "", nil),
	}
	logger, err := newLogger(filepath.Join(directory, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	engine := engine{
		Config:  model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}},
		logger:  logger,
		checker: checkerFunc(func(context.Context, model.Application, string) (string, error) { return "", nil }),
	}
	state, results := runFixedRequest(&engine, context.Background(), engine.Config, RunOptions{CheckOnly: true})
	if len(results) != 1 || results[0].Status != model.StatusFailed {
		t.Fatalf("unexpected results: %#v", results)
	}
	for _, value := range []string{results[0].Message, results[0].State.Error, configStatus(state, app.ID).Error} {
		if strings.Contains(value, secret) || !strings.Contains(value, "[REDACTED]") {
			t.Fatalf("result leaked secret: %q", value)
		}
	}
	runLog, err := os.ReadFile(filepath.Join(directory, "logs", runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(runLog), secret) {
		t.Fatalf("run log leaked secret: %s", runLog)
	}
}

func TestSensitiveEnvironmentKeyRecognizesCredentialNames(t *testing.T) {
	for _, key := range []string{"GITHUB_TOKEN", "DB_PASSWORD", "CLIENT_SECRET", "SERVICE_API_KEY", "AWS_ACCESS_KEY_ID", "PRIVATE_KEY", "CREDENTIAL_FILE"} {
		if !runtimeutil.IsSensitiveEnvironmentKey(key) {
			t.Errorf("expected %q to be sensitive", key)
		}
	}
	for _, key := range []string{"PIP_DISABLE_PIP_VERSION_CHECK", "GOBIN", "GOPROXY", "DISABLE_TELEMETRY"} {
		if runtimeutil.IsSensitiveEnvironmentKey(key) {
			t.Errorf("expected %q to be non-sensitive", key)
		}
	}
}

func TestCheckOnlyDoesNotExecuteConfiguredAutoUpdate(t *testing.T) {
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
}

func TestInstallModeStopsWhenUpdateFails(t *testing.T) {
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
}

func TestInstallModeExecutesUpdateThenInstallAndVerifiesVersion(t *testing.T) {
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
}

func TestInstallFailureRetainsSuccessfulUpdateOperationRecord(t *testing.T) {
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
}

func TestEngineVersionActionRendersPlaceholdersAndRejectsInvalidTemplates(t *testing.T) {
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
}

func TestEngineEmitsAppStartBeforeResult(t *testing.T) {
	dir := t.TempDir()
	installedPath := filepath.Join(dir, "installed")
	if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := model.Application{
		ID: "sample", Name: "Sample", InstallPath: installedPath, Enabled: true,
		UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.0.0\\n'", "", nil),
	}
	var events []string
	worker := engine{
		Config:   model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}},
		checker:  testEngineChecker(t, runtimeutil.Runner{IdleTimeout: time.Second}),
		AppStart: func(result model.Result) { events = append(events, result.Status+":"+result.AppID) },
		Output:   func(result model.Result) { events = append(events, result.Status+":"+result.AppID) },
	}
	runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{CheckOnly: true})
	if strings.Join(events, ",") != "checking:sample,current:sample" {
		t.Fatalf("events = %#v", events)
	}
}

func TestEngineEmitsUpdatingAfterCheckBeforeUpdateResult(t *testing.T) {
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
}

func TestEngineLogsEarlySkippedAndMissingResults(t *testing.T) {
	directory := t.TempDir()
	logger, err := newLogger(filepath.Join(directory, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	apps := []model.Application{
		{ID: "disabled", Name: "Disabled", InstallPath: "disabled", Enabled: false, UpdateMode: model.ModeInstall},
		{ID: "missing", Name: "Missing", InstallPath: filepath.Join(directory, "not-installed"), Enabled: true, UpdateMode: model.ModeInstall},
	}
	worker := engine{
		Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: apps},
		logger: logger,
		checker: checkerFunc(func(context.Context, model.Application, string) (string, error) {
			t.Fatal("checker must not run for disabled or missing apps")
			return "", nil
		}),
	}
	_, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{})
	if len(results) != 2 || results[0].Status != "skipped" || results[1].Status != "missing" {
		t.Fatalf("results = %#v", results)
	}
	data, err := os.ReadFile(filepath.Join(directory, "logs", runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, expected := range []string{`"app_id":"disabled"`, `"status":"skipped"`, `"operation":"install"`, `"app_id":"missing"`, `"status":"missing"`, `"level":"WARN"`} {
		if !strings.Contains(content, expected) {
			t.Fatalf("run log missing %s: %s", expected, content)
		}
	}
}

func TestDownloadIntegrityControlsResultStatus(t *testing.T) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("payload")))
	for _, test := range []struct {
		name       string
		enabled    bool
		expected   string
		wantStatus string
		wantPath   bool
	}{
		{name: "verified", enabled: true, expected: digest, wantStatus: "downloaded", wantPath: true},
		{name: "disabled", wantStatus: "downloaded", wantPath: true},
		{name: "mismatch", enabled: true, expected: strings.Repeat("0", 64), wantStatus: "downloaded_unverified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			installedPath := filepath.Join(dir, "installed")
			if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
				t.Fatal(err)
			}
			downloaderPath := filepath.Join(dir, "aria2c")
			script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
			if err := os.WriteFile(downloaderPath, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			app := model.Application{
				ID: "sample", Name: "Sample", InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeDownload,
				Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "", "", &model.Download{URL: "https://example.invalid/file", Filename: "file", ChecksumEnabled: test.enabled, ChecksumValue: test.expected}),
			}
			worker := engine{
				Config:  model.Config{Settings: model.Settings{Workers: 1, Downloader: testDownloaderSettings(downloaderPath, dir)}, Apps: []model.Application{app}},
				checker: checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil }),
			}
			state, results := runFixedRequest(&worker, context.Background(), configWithStatuses(worker.Config, map[string]model.ManagedStatus{"sample": {DownloadPath: "old-path"}}), RunOptions{})
			if len(results) != 1 || results[0].Status != test.wantStatus || configStatus(state, app.ID).UpdateStatus != test.wantStatus {
				t.Fatalf("unexpected download result: results=%#v state=%#v", results, configStatus(state, app.ID))
			}
			if got := configStatus(state, app.ID).DownloadPath; (got != "") != test.wantPath {
				t.Fatalf("download path = %q, wantPath=%v", got, test.wantPath)
			}
		})
	}
}

func TestDownloadExplicitChecksumOverridesGitHubArtifactDigest(t *testing.T) {
	dir := t.TempDir()
	installedPath := filepath.Join(dir, "installed")
	if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	downloaderPath := filepath.Join(dir, "aria2c")
	script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
	if err := os.WriteFile(downloaderPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("payload")))
	app := model.Application{
		ID: "sample", Name: "Sample", InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderGitHubRelease, "printf '1.0.0\\n'", "", "", &model.Download{
			URL: "https://example.invalid/file", Filename: "file", ChecksumEnabled: true, ChecksumValue: digest,
		}), Package: "owner/repo",
	}
	worker := engine{
		Config:  model.Config{Settings: model.Settings{Workers: 1, Downloader: testDownloaderSettings(downloaderPath, dir)}, Apps: []model.Application{app}},
		checker: artifactChecker{download: model.Download{ChecksumValue: strings.Repeat("0", 64)}},
	}
	_, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{})
	if len(results) != 1 || results[0].Status != model.StatusDownloaded {
		t.Fatalf("explicit checksum was overridden by provider digest: %#v", results)
	}
}

func TestDownloadResolvesDynamicProviderArtifact(t *testing.T) {
	dir := t.TempDir()
	installedPath := filepath.Join(dir, "installed")
	if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	downloaderPath := filepath.Join(dir, "aria2c")
	script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
	if err := os.WriteFile(downloaderPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	app := model.Application{
		ID: "sparkle", Name: "Sparkle", InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderSparkle, "printf '1.0.0\\n'", "", "", &model.Download{URL: "https://example.invalid/app.zip", Filename: "app.zip"}),
	}
	log, err := newLogger(filepath.Join(dir, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	worker := engine{
		Config:  model.Config{Settings: model.Settings{Workers: 1, Downloader: testDownloaderSettings(downloaderPath, dir)}, Apps: []model.Application{app}},
		checker: artifactChecker{download: model.Download{URL: "https://example.invalid/app.zip", Filename: "app.zip"}, artifact: "app@2.0.0"}, logger: log,
	}
	state, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{})
	if len(results) != 1 || results[0].Status != "downloaded" || configStatus(state, app.ID).DownloadPath != filepath.Join(dir, "app.zip") {
		t.Fatalf("unexpected dynamic download result: results=%#v state=%#v", results, configStatus(state, app.ID))
	}
	runLog, err := os.ReadFile(filepath.Join(dir, "logs", runLogFile))
	if err != nil || !strings.Contains(string(runLog), `"artifact":"app@2.0.0"`) {
		t.Fatalf("download artifact was not consumed by run log: %q, %v", runLog, err)
	}
}

func TestPlaceholdersUseNodeDistributionArchitecture(t *testing.T) {
	want := runtimeutil.HostPlatform().Architecture
	if got := placeholders(model.Application{}, model.ManagedStatus{}, "")["arch"]; got != want {
		t.Fatalf("architecture placeholder = %q, want %q", got, want)
	}
}

func TestCancelledDownloadClosesLiveOutputWithoutFinalResult(t *testing.T) {
	dir := t.TempDir()
	installedPath := filepath.Join(dir, "installed")
	if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	downloaderPath := filepath.Join(dir, "aria2c")
	if err := os.WriteFile(downloaderPath, []byte("#!/bin/sh\nprintf '[#abc 5MiB/10MiB(50%%) CN:1 DL:1MiB]\\r'\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	app := model.Application{
		ID: "obsidian", Name: "Obsidian", InstallPath: installedPath, Enabled: true,
		UpdateMode: model.ModeDownload, Provider: providerConfig(model.ProviderDefault, "", "", "", &model.Download{URL: "https://example.invalid/app.dmg", Filename: "app.dmg"}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cancelAfterProgress sync.Once
	var events []string
	var outputAppID string
	worker := engine{
		Config:        model.Config{Settings: model.Settings{Workers: 1, Downloader: testDownloaderSettings(downloaderPath, dir)}, Apps: []model.Application{app}},
		checker:       checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil }),
		DownloadStart: func(result model.Result) { events = append(events, result.Status) },
		DownloadProgress: func(progress model.DownloadProgress) {
			events = append(events, fmt.Sprintf("%s:%s:%d", progress.AppID, progress.Name, progress.Percent))
			cancelAfterProgress.Do(cancel)
		},
		DownloadOutput: func(app model.Application) (io.WriteCloser, io.WriteCloser) {
			outputAppID = app.ID
			return testDownloadWriteCloser{Writer: io.Discard, close: func() { events = append(events, "finished") }}, testDownloadWriteCloser{Writer: io.Discard}
		},
		Output: func(model.Result) { events = append(events, "result") },
	}
	_, results := runFixedRequest(&worker, ctx, configWithStatuses(worker.Config, map[string]model.ManagedStatus{"obsidian": {CurrentVersion: "1.0.0"}}), RunOptions{})
	if len(results) != 1 || results[0].Status != "failed" {
		t.Fatalf("unexpected results: %#v", results)
	}
	if strings.Join(events, ",") != "downloading,obsidian:Obsidian:50,finished" || outputAppID != "obsidian" {
		t.Fatalf("unexpected live output events: %#v app=%q", events, outputAppID)
	}
}

func TestAutoUpdateRequiresInstalledVersionToReachLatest(t *testing.T) {
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
}

func TestInstalledVersionVerifiedSupportsKnownAndUnknownLatestVersions(t *testing.T) {
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
}

func configWithStatuses(config model.Config, statuses map[string]model.ManagedStatus) model.Config {
	for index := range config.Apps {
		if status, found := statuses[config.Apps[index].ID]; found {
			config.Apps[index].StatusManaged = status
		}
	}
	return config
}

func configStatus(config model.Config, id string) model.ManagedStatus {
	for _, application := range config.Apps {
		if application.ID == id {
			return application.StatusManaged
		}
	}
	return model.ManagedStatus{}
}

func providerConfig(kind model.ProviderType, version, check, update string, download *model.Download) model.ProviderConfig {
	provider := model.ProviderConfig{Type: kind}
	if version != "" || check != "" || update != "" || download != nil {
		provider.Actions = &model.ProviderActions{Version: version, Check: check, Update: update, Download: download}
	}
	return provider
}
