package updater

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type stubPreprocessAction struct {
	id      string
	subject string
	enabled bool
	execute func(context.Context, model.Config) preprocessOutcome
}

func (action stubPreprocessAction) ID() string                { return action.id }
func (action stubPreprocessAction) Subject() string           { return action.subject }
func (action stubPreprocessAction) Enabled(model.Config) bool { return action.enabled }
func (action stubPreprocessAction) Execute(ctx context.Context, catalog model.Config) preprocessOutcome {
	return action.execute(ctx, catalog)
}

func TestUpdaterPreprocessFlow(t *testing.T) {
	t.Run("default-actions-register-homebrew-once", func(t *testing.T) {
		actions := defaultPreprocessActions()
		if len(actions) != 1 || actions[0].ID() != model.PreprocessActionHomebrew {
			t.Fatalf("actions=%#v", actions)
		}
	})

	t.Run("log-event-composes-action-and-preprocess-suffix", func(t *testing.T) {
		if event := preprocessLogEntry(model.PreprocessActionHomebrew, preprocessOutcome{Status: model.StatusSuccess}).Event; event != "Homebrew_preprocess" {
			t.Fatalf("event=%q", event)
		}
	})

	t.Run("already-cancelled-context-does-not-start-an-action", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		var progress []model.PreprocessProgress
		preprocessor := batchPreprocessor{
			actions: []preprocessAction{stubPreprocessAction{
				id: "first", subject: "First", enabled: true,
				execute: func(context.Context, model.Config) preprocessOutcome {
					called = true
					return preprocessOutcome{Status: model.StatusSuccess}
				},
			}},
			report: func(event model.PreprocessProgress) { progress = append(progress, event) },
		}

		preprocessor.run(ctx, model.Config{})

		if called || len(progress) != 0 {
			t.Fatalf("called=%t progress=%#v", called, progress)
		}
	})

	t.Run("runs-enabled-actions-in-order-and-continues-after-terminal-outcomes", func(t *testing.T) {
		var calls []string
		var progress []model.PreprocessProgress
		action := func(id, status string, enabled bool) preprocessAction {
			return stubPreprocessAction{id: id, subject: id + " subject", enabled: enabled, execute: func(context.Context, model.Config) preprocessOutcome {
				calls = append(calls, id)
				return preprocessOutcome{Status: status, Message: id + " finished"}
			}}
		}
		preprocessor := batchPreprocessor{
			actions: []preprocessAction{
				action("disabled", model.StatusSuccess, false),
				action("first", model.StatusSuccess, true),
				action("second", model.StatusFailed, true),
				action("third", model.StatusSkipped, true),
			},
			report: func(event model.PreprocessProgress) { progress = append(progress, event) },
		}

		preprocessor.run(context.Background(), model.Config{})

		if !reflect.DeepEqual(calls, []string{"first", "second", "third"}) {
			t.Fatalf("calls=%#v", calls)
		}
		want := []model.PreprocessProgress{
			{Action: "first", Subject: "first subject", Status: model.StatusStarted},
			{Action: "first", Subject: "first subject", Status: model.StatusSuccess},
			{Action: "second", Subject: "second subject", Status: model.StatusStarted},
			{Action: "second", Subject: "second subject", Status: model.StatusFailed},
			{Action: "third", Subject: "third subject", Status: model.StatusStarted},
			{Action: "third", Subject: "third subject", Status: model.StatusSkipped},
		}
		if !reflect.DeepEqual(progress, want) {
			t.Fatalf("progress=%#v want=%#v", progress, want)
		}
	})

	t.Run("parent-cancellation-stops-remaining-actions", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var secondCalled bool
		var progress []model.PreprocessProgress
		preprocessor := batchPreprocessor{
			actions: []preprocessAction{
				stubPreprocessAction{id: "first", subject: "First", enabled: true, execute: func(context.Context, model.Config) preprocessOutcome {
					cancel()
					return preprocessOutcome{Status: model.StatusFailed}
				}},
				stubPreprocessAction{id: "second", subject: "Second", enabled: true, execute: func(context.Context, model.Config) preprocessOutcome {
					secondCalled = true
					return preprocessOutcome{Status: model.StatusSuccess}
				}},
			},
			report: func(event model.PreprocessProgress) { progress = append(progress, event) },
		}

		preprocessor.run(ctx, model.Config{})

		if secondCalled {
			t.Fatal("action after cancellation was executed")
		}
		want := []model.PreprocessProgress{
			{Action: "first", Subject: "First", Status: model.StatusStarted},
			{Action: "first", Subject: "First", Status: model.StatusCancelled},
		}
		if !reflect.DeepEqual(progress, want) {
			t.Fatalf("progress=%#v want=%#v", progress, want)
		}
	})

	t.Run("invalid-terminal-status-is-reported-as-failed", func(t *testing.T) {
		directory := t.TempDir()
		logger, err := logutil.NewLogger(directory)
		if err != nil {
			t.Fatal(err)
		}
		var progress []model.PreprocessProgress
		preprocessor := batchPreprocessor{
			actions: []preprocessAction{stubPreprocessAction{
				id: "invalid", subject: "Invalid", enabled: true,
				execute: func(context.Context, model.Config) preprocessOutcome {
					return preprocessOutcome{Status: model.StatusStarted}
				},
			}},
			logger: logger,
			report: func(event model.PreprocessProgress) { progress = append(progress, event) },
		}

		preprocessor.run(context.Background(), model.Config{})

		if len(progress) != 2 || progress[1].Status != model.StatusFailed {
			t.Fatalf("progress=%#v", progress)
		}
		data := readPreprocessLog(t, directory)
		if !strings.Contains(data, `"event":"invalid_preprocess"`) || !strings.Contains(data, `"level":"ERROR"`) || !strings.Contains(data, "invalid status") {
			t.Fatalf("log=%s", data)
		}
	})

	t.Run("initial-request-controls-batch-preprocess", testInitialRequestControlsBatchPreprocess)
	t.Run("homebrew-deadline-failure-does-not-block-workers", testHomebrewDeadlineFailureDoesNotBlockWorkers)
}

func testInitialRequestControlsBatchPreprocess(t *testing.T) {
	for _, test := range []struct {
		name    string
		options RunOptions
		want    int
	}{
		{name: "all-check", options: RunOptions{CheckOnly: true, AllRequested: true}, want: 1},
		{name: "all-update", options: RunOptions{AllRequested: true}, want: 1},
		{name: "filtered-all-update", options: RunOptions{Names: []string{"sample"}, AllRequested: true}, want: 1},
		{name: "empty-names-without-all-request", options: RunOptions{}},
		{name: "selected-check", options: RunOptions{Names: []string{"sample"}, CheckOnly: true}},
		{name: "selected-update", options: RunOptions{Names: []string{"sample"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			catalog := model.Config{Settings: model.Settings{Workers: 1}}
			updater := &Updater{
				catalog:    catalog,
				engine:     engine{Config: catalog},
				batch:      newBatch(test.options),
				preprocess: func(context.Context) { calls++ },
			}
			_, _, err := updater.Run(context.Background(), func(updated model.Config, _ []model.Result) (model.Config, error) {
				return updated, nil
			})
			if err != nil || calls != test.want {
				t.Fatalf("err=%v calls=%d want=%d", err, calls, test.want)
			}
		})
	}

	for _, test := range []struct {
		name  string
		first RunOptions
		later RunOptions
		want  int
	}{
		{name: "later-all-request-does-not-change-selected-batch", first: RunOptions{Names: []string{"first"}}, later: RunOptions{AllRequested: true}},
		{name: "later-selected-request-does-not-change-all-batch", first: RunOptions{AllRequested: true}, later: RunOptions{Names: []string{"later"}}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			catalog := model.Config{Settings: model.Settings{Workers: 1}}
			batch := newBatch(test.first)
			if err := batch.add(test.later); err != nil {
				t.Fatal(err)
			}
			updater := &Updater{
				catalog:    catalog,
				engine:     engine{Config: catalog},
				batch:      batch,
				preprocess: func(context.Context) { calls++ },
			}
			_, _, err := updater.Run(context.Background(), func(updated model.Config, _ []model.Result) (model.Config, error) {
				return updated, nil
			})
			if err != nil || calls != test.want {
				t.Fatalf("err=%v calls=%d want=%d", err, calls, test.want)
			}
		})
	}
}

func testHomebrewDeadlineFailureDoesNotBlockWorkers(t *testing.T) {
	directory := t.TempDir()
	installed := filepath.Join(directory, "installed")
	if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, err := logutil.NewLogger(filepath.Join(directory, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := preprocessCatalog(true, false, 1)
	catalog.Settings.Workers = 1
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", InstallPath: installed, Enabled: true,
		UpdateMode: model.ModeCheck, StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"},
	}}
	setCalled := false
	action := homebrewPreprocessAction{
		lookup: func(string) (string, error) { return "/fixture/bin/brew", nil },
		runner: preprocessRunnerFunc(func(ctx context.Context, _ string, _ map[string]string) (runtimeutil.Result, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("Homebrew runner context has no deadline")
			}
			return runtimeutil.Result{}, context.DeadlineExceeded
		}),
		setenv: func(string, string) error { setCalled = true; return nil },
	}
	preprocessor := newBatchPreprocessor(logger, nil, action)
	facade := &Updater{
		catalog: catalog,
		logger:  logger,
		engine: engine{Config: catalog, logger: logger, checker: checkerFunc(func(context.Context, model.Application, string) (string, error) {
			return "1.0.0", nil
		})},
		batch: newBatch(RunOptions{CheckOnly: true, AllRequested: true}),
		preprocess: func(ctx context.Context) {
			preprocessor.run(ctx, catalog)
		},
	}
	_, results, err := facade.Run(context.Background(), func(updated model.Config, _ []model.Result) (model.Config, error) {
		return updated, nil
	})
	if err != nil || len(results) != 1 || results[0].Status != model.StatusCurrent {
		t.Fatalf("err=%v results=%#v", err, results)
	}
	if setCalled {
		t.Fatal("deadline failure set process environment")
	}
	data := readPreprocessLog(t, filepath.Join(directory, "logs"))
	if !strings.Contains(data, `"event":"homebrew_preprocess"`) ||
		!strings.Contains(data, `"level":"ERROR"`) ||
		!strings.Contains(data, context.DeadlineExceeded.Error()) ||
		!strings.Contains(data, `"event":"run_finished"`) {
		t.Fatalf("log=%s", data)
	}
}
