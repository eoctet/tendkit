package updater

import (
	"context"
	"fmt"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	logutil "github.com/eoctet/tendkit/pkg/logger"
)

const batchPreprocessEventSuffix = "_preprocess"

type preprocessAction interface {
	ID() string
	Subject() string
	Enabled(model.Config) bool
	Execute(context.Context, model.Config) preprocessOutcome
}

type preprocessOutcome struct {
	Status  string
	Message string
	Err     error
}

type batchPreprocessor struct {
	actions []preprocessAction
	logger  *logutil.Logger
	report  func(model.PreprocessProgress)
}

func newBatchPreprocessor(logger *logutil.Logger, report func(model.PreprocessProgress), actions ...preprocessAction) batchPreprocessor {
	return batchPreprocessor{actions: actions, logger: logger, report: report}
}

// defaultPreprocessActions is the single registration point for built-in
// preprocessing actions. Slice order is execution order.
func defaultPreprocessActions() []preprocessAction {
	return []preprocessAction{
		newHomebrewPreprocessAction(),
	}
}

// run executes enabled global actions in registration order before application
// workers start. Action failures are isolated; parent cancellation stops the
// remaining actions and is handled by the owning batch.
func (preprocessor batchPreprocessor) run(ctx context.Context, catalog model.Config) {
	for _, action := range preprocessor.actions {
		if ctx.Err() != nil {
			return
		}
		if action == nil || !action.Enabled(catalog) {
			continue
		}

		preprocessor.emit(action, preprocessOutcome{
			Status:  model.StatusStarted,
			Message: action.Subject() + " batch preprocessing started",
		})
		outcome := action.Execute(ctx, catalog)
		if err := ctx.Err(); err != nil {
			outcome = preprocessOutcome{
				Status:  model.StatusCancelled,
				Message: action.Subject() + " batch preprocessing cancelled",
				Err:     err,
			}
		} else if !validPreprocessTerminalStatus(outcome.Status) {
			outcome = preprocessOutcome{
				Status:  model.StatusFailed,
				Message: action.Subject() + " batch preprocessing failed",
				Err:     fmt.Errorf("preprocess action %q returned invalid status %q", action.ID(), outcome.Status),
			}
		}
		preprocessor.emit(action, outcome)
		if outcome.Status == model.StatusCancelled {
			return
		}
	}
}

func validPreprocessTerminalStatus(status string) bool {
	switch status {
	case model.StatusSuccess, model.StatusSkipped, model.StatusFailed:
		return true
	default:
		return false
	}
}

func (preprocessor batchPreprocessor) emit(action preprocessAction, outcome preprocessOutcome) {
	if preprocessor.logger != nil {
		entry := preprocessLogEntry(action.ID(), outcome)
		switch outcome.Status {
		case model.StatusStarted, model.StatusSuccess:
			_ = preprocessor.logger.Info(entry)
		case model.StatusSkipped, model.StatusCancelled:
			_ = preprocessor.logger.Warn(entry)
		default:
			_ = preprocessor.logger.Error(entry)
		}
	}
	if preprocessor.report != nil {
		preprocessor.report(model.PreprocessProgress{
			Action:  action.ID(),
			Subject: action.Subject(),
			Status:  outcome.Status,
		})
	}
}

func preprocessLogEntry(action string, outcome preprocessOutcome) logutil.LogEntry {
	detail := ""
	if outcome.Err != nil {
		detail = outcome.Err.Error()
	}
	message := strings.TrimSpace(outcome.Message)
	if message == "" {
		message = fmt.Sprintf("%s batch preprocessing %s", action, outcome.Status)
	}
	return logutil.LogEntry{
		Event:     action + batchPreprocessEventSuffix,
		Operation: model.OperationBatch,
		Status:    outcome.Status,
		Message:   message,
		Detail:    detail,
	}
}
