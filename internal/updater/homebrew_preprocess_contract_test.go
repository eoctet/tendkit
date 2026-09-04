package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type preprocessRunnerFunc func(context.Context, string, map[string]string) (runtimeutil.Result, error)

func (run preprocessRunnerFunc) Run(ctx context.Context, command string, environment map[string]string) (runtimeutil.Result, error) {
	return run(ctx, command, environment)
}

func preprocessCatalog(formula, cask bool, timeout int) model.Config {
	return model.Config{Settings: model.Settings{
		HTTP: &model.HTTPSettings{TimeoutSeconds: timeout},
		Scan: model.ScanSettings{Packages: model.PackageScanSettings{
			HomebrewFormula: formula,
			HomebrewCask:    cask,
		}},
	}}
}

func TestHomebrewPreprocessContract(t *testing.T) {
	t.Setenv(homebrewNoAutoUpdateKey, "")

	t.Run("existing-no-auto-update-environment-skips-before-lookup", func(t *testing.T) {
		t.Setenv(homebrewNoAutoUpdateKey, "1")
		lookupCalled := false
		runnerCalled := false
		setenvCalled := false
		var progress []model.PreprocessProgress
		action := homebrewPreprocessAction{
			lookup: func(string) (string, error) {
				lookupCalled = true
				return "/fixture/bin/brew", nil
			},
			runner: preprocessRunnerFunc(func(context.Context, string, map[string]string) (runtimeutil.Result, error) {
				runnerCalled = true
				return runtimeutil.Result{}, nil
			}),
			setenv: func(string, string) error {
				setenvCalled = true
				return nil
			},
		}
		newBatchPreprocessor(nil, func(event model.PreprocessProgress) { progress = append(progress, event) }, action).
			run(context.Background(), preprocessCatalog(true, false, 7))
		if lookupCalled || runnerCalled || setenvCalled {
			t.Fatalf("lookup=%t runner=%t setenv=%t", lookupCalled, runnerCalled, setenvCalled)
		}
		if len(progress) != 2 || progress[0].Status != model.StatusStarted || progress[1].Status != model.StatusSkipped {
			t.Fatalf("progress=%#v", progress)
		}
	})

	t.Run("enabled-domain-updates-once-with-http-deadline-and-sets-process-environment", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			catalog model.Config
		}{
			{name: "formula", catalog: preprocessCatalog(true, false, 7)},
			{name: "cask", catalog: preprocessCatalog(false, true, 7)},
			{name: "formula-and-cask", catalog: preprocessCatalog(true, true, 7)},
		} {
			t.Run(test.name, func(t *testing.T) {
				calls := 0
				setCalls := 0
				var progress []model.PreprocessProgress
				action := homebrewPreprocessAction{
					lookup: func(name string) (string, error) {
						if name != "brew" {
							t.Fatalf("lookup name=%q", name)
						}
						return "/fixture/bin/brew", nil
					},
					runner: preprocessRunnerFunc(func(ctx context.Context, command string, environment map[string]string) (runtimeutil.Result, error) {
						calls++
						if command != "/fixture/bin/brew update" || environment != nil {
							t.Fatalf("command=%q environment=%#v", command, environment)
						}
						deadline, ok := ctx.Deadline()
						if !ok || time.Until(deadline) > 7*time.Second || time.Until(deadline) < 6*time.Second {
							t.Fatalf("deadline=%v ok=%t", deadline, ok)
						}
						return runtimeutil.Result{}, nil
					}),
					setenv: func(key, value string) error {
						setCalls++
						if key != "HOMEBREW_NO_AUTO_UPDATE" || value != "1" {
							t.Fatalf("environment=%s=%s", key, value)
						}
						return nil
					},
				}
				preprocessor := newBatchPreprocessor(nil, func(event model.PreprocessProgress) { progress = append(progress, event) }, action)
				preprocessor.run(context.Background(), test.catalog)
				if calls != 1 || setCalls != 1 {
					t.Fatalf("calls=%d setCalls=%d", calls, setCalls)
				}
				if len(progress) != 2 || progress[0] != (model.PreprocessProgress{Action: model.PreprocessActionHomebrew, Subject: "Homebrew metadata update", Status: model.StatusStarted}) || progress[1].Status != model.StatusSuccess {
					t.Fatalf("progress=%#v", progress)
				}
			})
		}
	})

	t.Run("disabled-domains-skip-lookup-and-command", func(t *testing.T) {
		called := false
		action := homebrewPreprocessAction{lookup: func(string) (string, error) {
			called = true
			return "", nil
		}}
		newBatchPreprocessor(nil, nil, action).run(context.Background(), preprocessCatalog(false, false, 7))
		if called {
			t.Fatal("disabled Homebrew domains ran preprocessing")
		}
	})

	t.Run("missing-homebrew-warns-and-does-not-set-environment", func(t *testing.T) {
		directory := t.TempDir()
		logger, err := logutil.NewLogger(directory)
		if err != nil {
			t.Fatal(err)
		}
		setCalled := false
		var progress []model.PreprocessProgress
		action := homebrewPreprocessAction{
			lookup: func(string) (string, error) { return "", exec.ErrNotFound },
			setenv: func(string, string) error { setCalled = true; return nil },
		}
		preprocessor := newBatchPreprocessor(logger, func(event model.PreprocessProgress) { progress = append(progress, event) }, action)
		preprocessor.run(context.Background(), preprocessCatalog(true, false, 7))
		if setCalled {
			t.Fatal("missing Homebrew set process environment")
		}
		if len(progress) != 2 || progress[0].Status != model.StatusStarted || progress[1].Status != model.StatusSkipped {
			t.Fatalf("progress=%#v", progress)
		}
		data := readPreprocessLog(t, directory)
		if !strings.Contains(data, `"event":"homebrew_preprocess"`) || !strings.Contains(data, `"level":"WARN"`) {
			t.Fatalf("log=%s", data)
		}
	})

	t.Run("command-failure-errors-and-does-not-set-environment", func(t *testing.T) {
		directory := t.TempDir()
		logger, err := logutil.NewLogger(directory)
		if err != nil {
			t.Fatal(err)
		}
		setCalled := false
		var progress []model.PreprocessProgress
		action := homebrewPreprocessAction{
			lookup: func(string) (string, error) { return "/fixture/bin/brew", nil },
			runner: preprocessRunnerFunc(func(context.Context, string, map[string]string) (runtimeutil.Result, error) {
				return runtimeutil.Result{ExitCode: 1, Stderr: "network failed"}, nil
			}),
			setenv: func(string, string) error { setCalled = true; return nil },
		}
		preprocessor := newBatchPreprocessor(logger, func(event model.PreprocessProgress) { progress = append(progress, event) }, action)
		preprocessor.run(context.Background(), preprocessCatalog(false, true, 7))
		if setCalled {
			t.Fatal("failed update set process environment")
		}
		if len(progress) != 2 || progress[0].Status != model.StatusStarted || progress[1].Status != model.StatusFailed {
			t.Fatalf("progress=%#v", progress)
		}
		data := readPreprocessLog(t, directory)
		if !strings.Contains(data, `"level":"ERROR"`) || !strings.Contains(data, "network failed") {
			t.Fatalf("log=%s", data)
		}
	})

	t.Run("lookup-failure-errors-without-running-or-setting-environment", func(t *testing.T) {
		directory := t.TempDir()
		logger, err := logutil.NewLogger(directory)
		if err != nil {
			t.Fatal(err)
		}
		runnerCalled := false
		setenvCalled := false
		var progress []model.PreprocessProgress
		action := homebrewPreprocessAction{
			lookup: func(string) (string, error) { return "", errors.New("lookup failure") },
			runner: preprocessRunnerFunc(func(context.Context, string, map[string]string) (runtimeutil.Result, error) {
				runnerCalled = true
				return runtimeutil.Result{}, nil
			}),
			setenv: func(string, string) error { setenvCalled = true; return nil },
		}
		newBatchPreprocessor(logger, func(event model.PreprocessProgress) { progress = append(progress, event) }, action).
			run(context.Background(), preprocessCatalog(true, false, 7))
		if runnerCalled || setenvCalled {
			t.Fatalf("runnerCalled=%t setenvCalled=%t", runnerCalled, setenvCalled)
		}
		if len(progress) != 2 || progress[1].Status != model.StatusFailed {
			t.Fatalf("progress=%#v", progress)
		}
		if data := readPreprocessLog(t, directory); !strings.Contains(data, `"level":"ERROR"`) || !strings.Contains(data, "lookup failure") {
			t.Fatalf("log=%s", data)
		}
	})

	t.Run("runner-or-environment-failure-errors-with-default-http-timeout", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			result runtimeutil.Result
			runErr error
			setErr error
		}{
			{name: "runner", runErr: errors.New("I/O failure")},
			{name: "environment", setErr: errors.New("setenv failure")},
		} {
			t.Run(test.name, func(t *testing.T) {
				directory := t.TempDir()
				logger, err := logutil.NewLogger(directory)
				if err != nil {
					t.Fatal(err)
				}
				action := homebrewPreprocessAction{
					lookup: func(string) (string, error) { return "/fixture/bin/brew", nil },
					runner: preprocessRunnerFunc(func(ctx context.Context, _ string, _ map[string]string) (runtimeutil.Result, error) {
						deadline, ok := ctx.Deadline()
						if !ok {
							t.Fatal("default deadline is not set")
						}
						expected := time.Duration(model.DefaultHTTPTimeoutSeconds) * time.Second
						remaining := time.Until(deadline)
						if remaining > expected || remaining < expected-time.Second {
							t.Fatalf("default deadline remaining=%s, want within [%s, %s]", remaining, expected-time.Second, expected)
						}
						return test.result, test.runErr
					}),
					setenv: func(string, string) error { return test.setErr },
				}
				newBatchPreprocessor(logger, nil, action).run(context.Background(), preprocessCatalog(true, false, 0))
				if data := readPreprocessLog(t, directory); !strings.Contains(data, `"level":"ERROR"`) {
					t.Fatalf("log=%s", data)
				}
			})
		}
	})
}

func readPreprocessLog(t *testing.T, directory string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
