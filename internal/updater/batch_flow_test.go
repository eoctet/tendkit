package updater

import (
	"context"

	"github.com/eoctet/tendkit/internal/model"
	"strings"

	providerpkg "github.com/eoctet/tendkit/internal/updater/provider"
	"time"

	logutil "github.com/eoctet/tendkit/pkg/logger"
	"os"

	"io"
	"path/filepath"

	"errors"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"testing"
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

type checkerFunc func(context.Context, model.Application, string) (string, error)

func (function checkerFunc) latest(ctx context.Context, app model.Application, current string) (string, error) {
	return function(ctx, app, current)
}

func (checkerFunc) current(ctx context.Context, app model.Application, fallback string) (currentResolution, error) {
	return testCurrent(ctx, app, fallback)
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
func TestUpdaterBatchFlow(t *testing.T) {
	t.Run("active-batch-deduplicates-pending-and-in-flight-adds-and-closes-atomically", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "installed")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		started := make(chan string, 1)
		release := make(chan struct{})
		worker := engine{Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{
			{ID: "first", Name: "First", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck},
			{ID: "second", Name: "Second", InstallPath: installed, Enabled: true, UpdateMode: model.ModeCheck},
		}}, checker: checkerFunc(func(_ context.Context, app model.Application, _ string) (string, error) {
			started <- app.ID
			<-release
			return "1.0.0", nil
		})}
		batch := newBatch(RunOptions{Names: []string{"first"}, CheckOnly: true})
		finished := make(chan []model.Result, 1)
		go func() { _, results := worker.runBatch(context.Background(), worker.Config, batch); finished <- results }()
		if got := <-started; got != "first" {
			t.Fatalf("first started=%q", got)
		}
		for _, names := range [][]string{{"first"}, {"second"}, {"second"}} {
			if err := batch.add(RunOptions{Names: names, CheckOnly: true}); err != nil {
				t.Fatal(err)
			}
		}
		close(release)
		results := <-finished
		if len(results) != 2 || results[0].AppID != "first" || results[1].AppID != "second" {
			t.Fatalf("pending/in-flight duplicates were scheduled: %#v", results)
		}
		for range 32 {
			idle := newBatch(RunOptions{})
			_ = idle.drain()
			start := make(chan struct{})
			added := make(chan error, 1)
			closed := make(chan bool, 1)
			go func() { <-start; added <- idle.add(RunOptions{Names: []string{"first"}}) }()
			go func() { <-start; closed <- idle.closeIfIdle() }()
			close(start)
			err, didClose := <-added, <-closed
			if didClose && err == nil {
				t.Fatal("add succeeded after idle close")
			}
			if !didClose && err != nil {
				t.Fatalf("add lost before idle close: %v", err)
			}
		}
	})
	t.Run("updater-run-closes-batch-and-writes-one-terminal-event", func(t *testing.T) {
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
	})
	t.Run("engine-batch-adds-work-to-active-worker-pool", func(t *testing.T) {
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
	})
	t.Run("engine-batch-can-reschedule-completed-application", func(t *testing.T) {
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
	})
	t.Run("engine-emits-app-start-before-result", func(t *testing.T) {
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
	})
}
