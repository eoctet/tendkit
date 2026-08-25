package updater

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

const (
	logEventRunStarted   = "run_started"
	logEventRunFinished  = "run_finished"
	logEventRunCancelled = "run_cancelled"
	logEventRunFailed    = "run_failed"
)

// RunOptions selects applications for one updater request.
type RunOptions struct {
	Names          []string
	CheckOnly      bool
	DownloadAssets map[string]string
}

type batch struct {
	mu       sync.Mutex
	requests []RunOptions
	notify   chan struct{}
	closed   bool
}

func (batch *batch) add(options RunOptions) error {
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if batch.closed {
		return errors.New(i18n.T("app.batch_closed"))
	}
	batch.requests = append(batch.requests, options)
	select {
	case batch.notify <- struct{}{}:
	default:
	}
	return nil
}

func (batch *batch) drain() []RunOptions {
	batch.mu.Lock()
	defer batch.mu.Unlock()
	requests := batch.requests
	batch.requests = nil
	return requests
}

func (batch *batch) closeIfIdle() bool {
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if len(batch.requests) > 0 {
		return false
	}
	batch.closed = true
	return true
}

func (batch *batch) close() {
	batch.mu.Lock()
	batch.closed = true
	batch.requests = nil
	batch.mu.Unlock()
}

// Options supplies presentation callbacks and command streams for one run.
type Options struct {
	LogDir           string
	Logger           *logutil.Logger
	AppStart         func(model.Result)
	Output           func(model.Result)
	UpdateStart      func(model.Result)
	DownloadStart    func(model.Result)
	DownloadProgress func(model.DownloadProgress)
	// DownloadOutput opens stdout and stderr for one application's download command.
	DownloadOutput func(model.Application) (io.WriteCloser, io.WriteCloser)
	CommandOutput  func(model.CommandOutput)
}

type applicationContextKey struct{}
type operationContextKey struct{}

func withApplication(ctx context.Context, app model.Application) context.Context {
	return context.WithValue(ctx, applicationContextKey{}, app)
}

func applicationFromContext(ctx context.Context) (model.Application, bool) {
	app, ok := ctx.Value(applicationContextKey{}).(model.Application)
	return app, ok
}

func withOperation(ctx context.Context, operation string) context.Context {
	return context.WithValue(ctx, operationContextKey{}, operation)
}

func operationFromContext(ctx context.Context) string {
	operation, _ := ctx.Value(operationContextKey{}).(string)
	return operation
}

// Updater is the sole service-facing updater facade. It owns provider, HTTP,
// execution, downloader, and logging collaborators for one run transaction.
type Updater struct {
	engine      engine
	logger      *logutil.Logger
	catalog     model.Config
	targetCount int
	started     time.Time
	batch       *batch
}

// DownloadAssetCandidates preflights downloadable artifacts without starting an update.
// Provider failures are returned by application ID so other targets can proceed;
// request cancellation remains a global error.
func (u *Updater) DownloadAssetCandidates(ctx context.Context, apps []model.Application, progress func(model.DownloadAssetPreflightProgress)) (map[string]model.DownloadAssetChoices, map[string]error, error) {
	choices := make(map[string]model.DownloadAssetChoices)
	failures := make(map[string]error)
	for _, app := range apps {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		reportDownloadAssetPreflight(progress, app, model.DownloadAssetPreflightStarted, model.DownloadAssetChoices{}, nil)
		operationContext := withOperation(withApplication(ctx, app), model.OperationCheck)
		current, err := u.engine.checker.current(operationContext, app, app.StatusManaged.CurrentVersion)
		if err == nil && strings.TrimSpace(current.Version) == "" {
			err = errors.New(i18n.T("engine.current_unknown"))
		}
		var latest string
		if err == nil {
			latest, err = u.engine.checker.latest(operationContext, app, current.Version)
		}
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, nil, contextErr
			}
			reportDownloadAssetPreflight(progress, app, model.DownloadAssetPreflightFailed, model.DownloadAssetChoices{}, err)
			failures[app.ID] = err
			continue
		}
		if !version.IsNewer(latest, current.Version) {
			reportDownloadAssetPreflight(progress, app, model.DownloadAssetPreflightCompleted, model.DownloadAssetChoices{}, nil)
			continue
		}
		values, err := u.engine.checker.downloadAssetCandidates(operationContext, app)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, nil, contextErr
			}
			reportDownloadAssetPreflight(progress, app, model.DownloadAssetPreflightFailed, model.DownloadAssetChoices{}, err)
			failures[app.ID] = err
			continue
		}
		reportDownloadAssetPreflight(progress, app, model.DownloadAssetPreflightCompleted, values, nil)
		if values.Candidates != nil {
			choices[app.ID] = values
		}
	}
	return choices, failures, nil
}

func reportDownloadAssetPreflight(progress func(model.DownloadAssetPreflightProgress), app model.Application, stage model.DownloadAssetPreflightStage, choices model.DownloadAssetChoices, err error) {
	if progress == nil {
		return
	}
	progress(model.DownloadAssetPreflightProgress{AppID: app.ID, AppName: app.Name, Stage: stage, CandidateCount: len(choices.Candidates), Err: err})
}

// PreflightDownloadAssetCandidates is a read-only provider facade. Unlike New,
// it intentionally does not initialize run logging or downloader state.
func PreflightDownloadAssetCandidates(ctx context.Context, catalog model.Config, apps []model.Application, progress func(model.DownloadAssetPreflightProgress)) (map[string]model.DownloadAssetChoices, map[string]error, error) {
	checker, err := newProviderResolver(commandRunner(catalog, Options{}), catalog.Settings.ProviderURLs, catalog.Settings.HTTP)
	if err != nil {
		return nil, nil, err
	}
	return (&Updater{engine: engine{checker: checker}}).DownloadAssetCandidates(ctx, apps, progress)
}

// New initializes the built-in provider registry, HTTP source, command runner,
// engine, and logger. Initialization failures are returned to the caller.
func New(catalog model.Config, options Options) (*Updater, error) {
	runner := commandRunner(catalog, options)
	checker, err := newProviderResolver(runner, catalog.Settings.ProviderURLs, catalog.Settings.HTTP)
	if err != nil {
		return nil, err
	}
	checker.setDownloadDir(catalog.Settings.Downloader.StorePath)
	if err := validateProviders(catalog, checker.providerNames()); err != nil {
		return nil, err
	}
	logger := options.Logger
	if logger == nil {
		logger, _ = logutil.NewLogger(options.LogDir, catalog.Settings.LogLevel)
	}
	if logger != nil {
		for _, app := range catalog.Apps {
			logger.AddSensitiveEnvironment(app.Environment)
		}
	}
	return &Updater{
		catalog: catalog,
		logger:  logger,
		engine: engine{Config: catalog, checker: checker, logger: logger,
			AppStart: options.AppStart, Output: options.Output, UpdateStart: options.UpdateStart, DownloadStart: options.DownloadStart, DownloadProgress: options.DownloadProgress,
			DownloadOutput: options.DownloadOutput},
		batch: &batch{notify: make(chan struct{}, 1)},
	}, nil
}

// Add appends a request to this facade's private dynamic batch.
func (u *Updater) Add(options RunOptions) error {
	if u == nil || u.batch == nil {
		return errors.New(i18n.T("app.batch_required"))
	}
	return u.batch.add(options)
}

// Run executes the private dynamic batch and commits its result through persist.
// It records exactly one terminal run event for every started execution.
func (u *Updater) Run(ctx context.Context, persist func(model.Config, []model.Result) (model.Config, error)) (model.Config, []model.Result, error) {
	if u == nil || u.batch == nil || persist == nil {
		return model.Config{}, nil, errors.New(i18n.T("app.batch_required"))
	}
	defer u.batch.close()
	u.targetCount = batchTargetCount(u.catalog, u.batch)
	u.started = time.Now()
	_ = u.logger.Info(logutil.LogEntry{Event: logEventRunStarted, Operation: model.OperationBatch, Status: model.StatusStarted, Message: "batch run started", TargetCount: u.targetCount, WorkerCount: u.catalog.Settings.Workers})
	catalog, results := u.engine.runBatch(ctx, u.catalog, u.batch)
	if err := ctx.Err(); err != nil {
		_ = u.logger.Warn(logutil.LogEntry{Event: logEventRunCancelled, Operation: model.OperationBatch, Status: model.StatusCancelled, Message: "batch run cancelled", DurationMS: time.Since(u.started).Milliseconds()})
		return catalog, results, err
	}
	persisted, err := persist(catalog, results)
	if err != nil {
		_ = u.logger.Error(logutil.LogEntry{Event: logEventRunFailed, Operation: model.OperationBatch, Status: model.StatusFailed, Message: "batch run failed", Detail: err.Error(), DurationMS: time.Since(u.started).Milliseconds()})
		return catalog, results, err
	}
	failed := failedResultCount(results)
	entry := logutil.LogEntry{Event: logEventRunFinished, Operation: model.OperationBatch, Status: model.StatusSuccess, Message: "batch run finished", DurationMS: time.Since(u.started).Milliseconds(), TargetCount: len(results), ResultCount: len(results), FailedCount: failed, WorkerCount: u.catalog.Settings.Workers}
	if failed > 0 {
		entry.Status = model.StatusCompletedWithErrors
		_ = u.logger.Warn(entry)
	} else {
		_ = u.logger.Info(entry)
	}
	return persisted, results, nil
}

func commandRunner(catalog model.Config, options Options) runtimeutil.Runner {
	runner := runtimeutil.Runner{IdleTimeout: time.Duration(catalog.Settings.TimeoutSeconds) * time.Second}
	if options.CommandOutput == nil {
		return runner
	}
	runner.OnOutput = func(ctx context.Context, output runtimeutil.OutputEvent) {
		app, ok := applicationFromContext(ctx)
		if !ok {
			return
		}
		options.CommandOutput(model.CommandOutput{CommandID: output.CommandID, AppID: app.ID, AppName: app.Name, Operation: operationFromContext(ctx), Stream: output.Stream, Data: append([]byte(nil), output.Data...), Done: output.Done})
	}
	return runner
}

func validateProviders(catalog model.Config, names []string) error {
	available := make(map[string]struct{}, len(names))
	for _, name := range names {
		available[strings.ToLower(name)] = struct{}{}
	}
	for _, app := range catalog.Apps {
		if app.Provider.Type == model.ProviderDefault {
			continue
		}
		if _, ok := available[strings.ToLower(string(app.Provider.Type))]; !ok {
			return errors.New(i18n.T("app.provider_unknown", app.Provider.Type, app.ID, strings.Join(names, ", ")))
		}
	}
	return nil
}

func batchTargetCount(catalog model.Config, batch *batch) int {
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if len(batch.requests) == 0 || len(batch.requests[0].Names) == 0 {
		return len(catalog.Apps)
	}
	return len(batch.requests[0].Names)
}

func failedResultCount(results []model.Result) int {
	failed := 0
	for _, result := range results {
		if result.Status == model.StatusFailed {
			failed++
		}
	}
	return failed
}
