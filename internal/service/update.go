package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/updater"
	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// RunOptions controls one check, update, download, or dynamic batch transaction.
type RunOptions struct {
	Names     []string
	CheckOnly bool
	// AllRequested preserves the initial all-apps scope after target filtering.
	AllRequested bool
	// DownloadAssets is an ephemeral appID -> downloadable artifact choice. It is never
	// written to catalog configuration.
	DownloadAssets map[string]string
	// DownloadOutput opens stdout and stderr for one application's download command.
	DownloadOutput func(model.Application) (io.WriteCloser, io.WriteCloser)
	CommandOutput  func(model.CommandOutput)
	Observer       RunObserver
}

// RunObserver receives lifecycle events for one batch operation.
type RunObserver struct {
	AppStart           func(model.Result)
	Result             func(model.Result)
	UpdateStart        func(model.Result)
	DownloadStart      func(model.Result)
	DownloadProgress   func(model.DownloadProgress)
	PreprocessProgress func(model.PreprocessProgress)
}

// DownloadAssetCandidates obtains downloadable artifact choices before a run. It does not save
// configuration or start a batch, so a cancelled UI selection is side-effect
// free. The second result contains target-local failures keyed by application ID.
func (s *Service) DownloadAssetCandidates(ctx context.Context, names []string, progress func(model.DownloadAssetPreflightProgress)) (map[string]model.DownloadAssetChoices, map[string]error, error) {
	snapshot, err := s.config.Snapshot()
	if err != nil {
		return nil, nil, err
	}
	if err := validateNames(snapshot.Config.Apps, names); err != nil {
		return nil, nil, err
	}
	selected := selectServiceApps(snapshot.Config.Apps, names)
	selected = downloadSelectionTargets(selected)
	if len(selected) == 0 {
		return map[string]model.DownloadAssetChoices{}, map[string]error{}, nil
	}
	return updater.PreflightDownloadAssetCandidates(ctx, snapshot.Config, selected, progress)
}

func downloadSelectionTargets(apps []model.Application) []model.Application {
	result := make([]model.Application, 0, len(apps))
	for _, app := range apps {
		supportsSelection := app.Provider.Type == model.ProviderGitHubRelease || app.Provider.Type == model.ProviderGo
		if app.Enabled && supportsSelection && app.UpdateMode == model.ModeDownload && app.Provider.DownloadAction() == nil {
			result = append(result, app)
		}
	}
	return result
}

func selectServiceApps(apps []model.Application, names []string) []model.Application {
	if len(names) == 0 {
		return append([]model.Application(nil), apps...)
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	result := make([]model.Application, 0, len(names))
	for _, app := range apps {
		if wanted[app.ID] {
			result = append(result, app)
		}
	}
	return result
}

// Batch owns requests submitted before a run is bound, then forwards additions
// to the facade without exposing updater batch internals.
type Batch struct {
	mu      sync.Mutex
	pending []RunOptions
	updater *updater.Updater
	closed  bool
}

// NewBatch creates a service-level dynamic batch.
func NewBatch(options RunOptions) *Batch {
	return &Batch{pending: []RunOptions{options}}
}

// Add appends work to the active service transaction.
func (batch *Batch) Add(options RunOptions) error {
	if batch == nil {
		return errors.New(i18n.T("app.batch_required"))
	}
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if batch.closed {
		return errors.New(i18n.T("app.batch_closed"))
	}
	if batch.updater == nil {
		batch.pending = append(batch.pending, options)
		return nil
	}
	return batch.updater.Add(updater.RunOptions{Names: options.Names, CheckOnly: options.CheckOnly, AllRequested: options.AllRequested, DownloadAssets: options.DownloadAssets})
}

// Run executes a fixed request through a service-owned batch.
func (s *Service) Run(ctx context.Context, options RunOptions) (model.Config, []model.Result, error) {
	return s.RunBatch(ctx, options, NewBatch(options))
}

// RunBatch executes a dynamically extensible set of application operations
// under one lock and persists the combined state exactly once.
func (s *Service) RunBatch(ctx context.Context, options RunOptions, batch *Batch) (model.Config, []model.Result, error) {
	if batch == nil {
		return model.Config{}, nil, errors.New(i18n.T("app.batch_required"))
	}
	defer batch.close()
	var execution *serviceRunExecution
	err := s.config.WithLock(func() error {
		var err error
		execution, err = s.prepareRunExecution(options, batch)
		if err != nil {
			return err
		}
		return execution.execute(ctx)
	})
	if execution == nil {
		return model.Config{}, nil, err
	}
	return execution.catalog, execution.results, err
}

type serviceRunExecution struct {
	service  *Service
	batch    *Batch
	catalog  model.Config
	baseline model.Config
	results  []model.Result
	updater  *updater.Updater
}

func (s *Service) prepareRunExecution(options RunOptions, batch *Batch) (*serviceRunExecution, error) {
	snapshot, err := s.config.Snapshot()
	if err != nil {
		return nil, err
	}
	if err := s.config.ValidateExecutionSecurity(); err != nil {
		return nil, err
	}
	catalog := snapshot.Config
	baseline := catalog
	baseline.Apps = append([]model.Application(nil), catalog.Apps...)
	execution := &serviceRunExecution{service: s, batch: batch, catalog: catalog, baseline: baseline}
	if err := validateNames(catalog.Apps, options.Names); err != nil {
		return execution, err
	}
	sharedLogger, _ := s.loggerFor(catalog)
	execution.updater, err = updater.New(catalog, updater.Options{
		LogDir:   runtimeutil.ExpandPath(catalog.Settings.LogDir),
		Logger:   sharedLogger,
		AppStart: options.Observer.AppStart, Output: options.Observer.Result, UpdateStart: options.Observer.UpdateStart,
		DownloadStart: options.Observer.DownloadStart, DownloadProgress: options.Observer.DownloadProgress,
		PreprocessProgress: options.Observer.PreprocessProgress,
		DownloadOutput:     options.DownloadOutput,
		CommandOutput:      options.CommandOutput,
	})
	if err != nil {
		return execution, err
	}
	return execution, nil
}

func (execution *serviceRunExecution) execute(ctx context.Context) error {
	if err := execution.batch.bind(execution.updater); err != nil {
		return err
	}
	var err error
	execution.catalog, execution.results, err = execution.updater.Run(ctx, execution.persist)
	return err
}

func (batch *Batch) bind(facade *updater.Updater) error {
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if batch.closed || batch.updater != nil {
		return errors.New(i18n.T("app.batch_required"))
	}
	for _, request := range batch.pending {
		if err := facade.Add(updater.RunOptions{Names: request.Names, CheckOnly: request.CheckOnly, AllRequested: request.AllRequested, DownloadAssets: request.DownloadAssets}); err != nil {
			return err
		}
	}
	batch.pending = nil
	batch.updater = facade
	return nil
}

func (batch *Batch) close() {
	batch.mu.Lock()
	batch.closed = true
	batch.pending = nil
	batch.mu.Unlock()
}

func (execution *serviceRunExecution) persist(catalog model.Config, results []model.Result) (model.Config, error) {
	execution.catalog, execution.results = catalog, results
	for attempt := 0; attempt < snapshotSaveAttempts; attempt++ {
		latest, err := execution.service.config.Snapshot()
		if err != nil {
			return model.Config{}, err
		}
		merged, ok := model.MergeRunStatuses(execution.baseline, latest.Config, execution.catalog)
		if !ok {
			return model.Config{}, config.ErrStaleOperation
		}
		err = execution.service.config.Save(latest.Revision, merged)
		if errors.Is(err, config.ErrStaleOperation) {
			continue
		}
		if err != nil {
			return model.Config{}, err
		}
		execution.catalog = merged
		return execution.catalog, nil
	}
	return model.Config{}, config.ErrStaleOperation
}

// ValidateNames verifies that every requested name resolves to a catalog entry.
func ValidateNames(apps []model.Application, names []string) error { return validateNames(apps, names) }

func validateNames(apps []model.Application, names []string) error {
	known := make(map[string]struct{}, len(apps)*2)
	for _, app := range apps {
		known[strings.ToLower(app.ID)] = struct{}{}
		known[strings.ToLower(app.Name)] = struct{}{}
	}
	for _, name := range names {
		if _, exists := known[strings.ToLower(name)]; !exists {
			return errors.New(i18n.T("app.app_not_found", name))
		}
	}
	return nil
}
