package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

const homebrewNoAutoUpdateKey = "HOMEBREW_NO_AUTO_UPDATE"
const homebrewNoAutoUpdateValue = "1"

type preprocessRunner interface {
	Run(context.Context, string, map[string]string) (runtimeutil.Result, error)
}

type homebrewPreprocessAction struct {
	runner preprocessRunner
	lookup func(string) (string, error)
	setenv func(string, string) error
}

func newHomebrewPreprocessAction() homebrewPreprocessAction {
	return homebrewPreprocessAction{
		lookup: exec.LookPath,
		setenv: os.Setenv,
	}
}

func (homebrewPreprocessAction) ID() string      { return model.PreprocessActionHomebrew }
func (homebrewPreprocessAction) Subject() string { return "Homebrew metadata update" }

func (homebrewPreprocessAction) Enabled(catalog model.Config) bool {
	packages := catalog.Settings.Scan.Packages
	return packages.HomebrewFormula || packages.HomebrewCask
}

func (action homebrewPreprocessAction) Execute(ctx context.Context, catalog model.Config) preprocessOutcome {
	if os.Getenv(homebrewNoAutoUpdateKey) == homebrewNoAutoUpdateValue {
		return preprocessOutcome{
			Status:  model.StatusSkipped,
			Message: "Homebrew batch preprocessing skipped: HOMEBREW_NO_AUTO_UPDATE=1",
		}
	}

	lookup := action.lookup
	if lookup == nil {
		lookup = exec.LookPath
	}
	manager, err := lookup("brew")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return preprocessOutcome{
				Status:  model.StatusSkipped,
				Message: "Homebrew batch preprocessing skipped: brew is not installed",
				Err:     err,
			}
		}
		return homebrewPreprocessFailure(err)
	}

	runner := action.runner
	if runner == nil {
		runner = runtimeutil.Runner{IdleTimeout: homebrewPreprocessTimeout(catalog)}
	}
	commandContext, cancel := context.WithTimeout(ctx, homebrewPreprocessTimeout(catalog))
	result, runErr := runner.Run(commandContext, runtimeutil.QuoteShell(manager)+" update", nil)
	cancel()
	if runErr != nil {
		detail := strings.TrimSpace(result.Combined())
		if detail != "" {
			runErr = fmt.Errorf("%w: %s", runErr, detail)
		}
		return homebrewPreprocessFailure(runErr)
	}
	if result.ExitCode != 0 {
		return homebrewPreprocessFailure(fmt.Errorf("brew update exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Combined())))
	}

	setenv := action.setenv
	if setenv == nil {
		setenv = os.Setenv
	}
	if err := setenv(homebrewNoAutoUpdateKey, homebrewNoAutoUpdateValue); err != nil {
		return homebrewPreprocessFailure(err)
	}
	return preprocessOutcome{
		Status:  model.StatusSuccess,
		Message: "Homebrew metadata updated before batch run",
	}
}

func homebrewPreprocessFailure(err error) preprocessOutcome {
	return preprocessOutcome{
		Status:  model.StatusFailed,
		Message: "Homebrew batch preprocessing failed",
		Err:     err,
	}
}

func homebrewPreprocessTimeout(catalog model.Config) time.Duration {
	seconds := model.DefaultHTTPTimeoutSeconds
	if catalog.Settings.HTTP != nil && catalog.Settings.HTTP.TimeoutSeconds > 0 {
		seconds = catalog.Settings.HTTP.TimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}
