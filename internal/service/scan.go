package service

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"time"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner"
	httpx "github.com/eoctet/tendkit/pkg/http"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// ScanPreview contains unpersisted catalog candidates. Runtime state for apps
// already in the catalog may already be persisted in BaseState.
type ScanPreview struct {
	BaseConfig model.Config
	BaseState  model.RuntimeState
	Config     model.Config
	State      model.RuntimeState
	Changes    []model.ScanApplicationChange
	Added      []model.Application
	Removed    []model.Application
	Excluded   []model.Application
}

// ScanObserver receives progress events for one scan operation.
type ScanObserver struct {
	Progress func(model.ScanProgress)
}

// PreviewScan keeps catalog candidates unpersisted while saving refreshed
// runtime state for applications already present in the catalog.
func (s *Service) PreviewScan(ctx context.Context, observer ScanObserver) (ScanPreview, error) {
	var preview ScanPreview
	err := s.config.WithLock(func() error {
		snapshot, err := s.config.Snapshot()
		if err != nil {
			return err
		}
		catalog := snapshot.Config
		log, _ := s.loggerFor(catalog)
		registerLoggerSensitive(log, catalog)
		started := time.Now()
		_ = log.Info(scanLogEntry("scan_started", model.StatusStarted, "scan lifecycle event", "", "", 0, 0))
		state := model.RuntimeState{}
		current := cloneApplications(catalog.Apps)
		baseCatalog := catalog
		baseCatalog.Apps = cloneApplications(current)
		currentIDs := appIDs(current)
		runner := runtimeutil.Runner{IdleTimeout: time.Duration(catalog.Settings.TimeoutSeconds) * time.Second}
		candidate, candidateState, err := (scanner.Scanner{Runner: runner, Progress: observer.Progress, Diagnostic: scanDiagnosticLogger(log), GitHub: s.githubResolver(catalog)}).Scan(ctx, catalog, state)
		if err != nil {
			logScanFailure(log, ctx, started, "", err)
			return err
		}
		saved, err := s.savePreviewStatuses(baseCatalog, persistExistingStatuses(baseCatalog, candidate))
		if err != nil {
			logScanFailure(log, ctx, started, "", err)
			return err
		}
		preview = ScanPreview{
			BaseConfig: saved, BaseState: candidateState,
			Config: candidate, State: candidateState, Changes: scanApplicationChanges(current, candidate.Apps),
			Added: applicationsOutsideIDs(currentIDs, candidate.Apps), Removed: removedApps(current, candidate.Apps),
			Excluded: scanner.ExcludedConfiguredApps(saved),
		}
		_ = log.Info(scanLogEntry("scan_finished", model.StatusSuccess, "scan lifecycle event", "", "", time.Since(started), len(preview.Added)+len(preview.Removed)))
		return nil
	})
	return preview, err
}

// PreviewApplicationScan keeps the target's catalog candidate unpersisted while
// saving its refreshed runtime state.
func (s *Service) PreviewApplicationScan(ctx context.Context, target model.Application, observer ScanObserver) (ScanPreview, error) {
	var preview ScanPreview
	err := s.config.WithLock(func() error {
		snapshot, err := s.config.Snapshot()
		if err != nil {
			return err
		}
		catalog := snapshot.Config
		log, _ := s.loggerFor(catalog)
		registerLoggerSensitive(log, catalog)
		started := time.Now()
		_ = log.Info(scanLogEntry("scan_started", model.StatusStarted, "scan lifecycle event", "", target.ID, 0, 1))
		state := model.RuntimeState{}
		baseCatalog := catalog
		baseCatalog.Apps = cloneApplications(catalog.Apps)
		candidateCatalog := catalog
		candidateCatalog.Apps = cloneApplications(catalog.Apps)
		candidateState := cloneScanState(state)
		applicationScanner := scanner.New(catalog.Settings.Scan)
		applicationScanner.Runner = runtimeutil.Runner{IdleTimeout: time.Duration(catalog.Settings.TimeoutSeconds) * time.Second}
		applicationScanner.Progress = observer.Progress
		applicationScanner.Diagnostic = scanDiagnosticLogger(log)
		applicationScanner.GitHub = s.githubResolver(catalog)
		applicationScanner.DownloadDir = catalog.Settings.Downloader.StorePath
		candidate, candidateState, err := applicationScanner.ScanApplication(ctx, target, candidateState)
		if err != nil {
			logScanFailure(log, ctx, started, target.ID, err)
			return err
		}
		replaced := false
		for index := range candidateCatalog.Apps {
			if candidateCatalog.Apps[index].ID == candidate.ID {
				candidateCatalog.Apps[index] = candidate
				replaced = true
				break
			}
		}
		if !replaced {
			candidateCatalog.Apps = append(candidateCatalog.Apps, candidate)
		}
		saved, err := s.savePreviewStatuses(baseCatalog, persistExistingStatuses(baseCatalog, candidateCatalog))
		if err != nil {
			logScanFailure(log, ctx, started, target.ID, err)
			return err
		}
		currentIDs := appIDs(catalog.Apps)
		preview = ScanPreview{
			BaseConfig: saved, BaseState: candidateState,
			Config: candidateCatalog, State: candidateState,
			Changes:  scanApplicationChanges(catalog.Apps, candidateCatalog.Apps),
			Added:    applicationsOutsideIDs(currentIDs, candidateCatalog.Apps),
			Excluded: scanner.ExcludedConfiguredApps(baseCatalog),
		}
		_ = log.Info(scanLogEntry("scan_finished", model.StatusSuccess, "scan lifecycle event", "", target.ID, time.Since(started), 1))
		return nil
	})
	return preview, err
}

func scanLogEntry(event, status, message, detail, subject string, duration time.Duration, count int) logutil.LogEntry {
	return logutil.LogEntry{Event: event, Operation: "scan", Status: status, AppID: subject, Message: message, Detail: detail, DurationMS: duration.Milliseconds(), ResultCount: count}
}

func logScanFailure(log *logutil.Logger, ctx context.Context, started time.Time, subject string, err error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		_ = log.Warn(scanLogEntry("scan_cancelled", model.StatusCancelled, "scan operation cancelled", err.Error(), subject, time.Since(started), 0))
		return
	}
	_ = log.Error(scanLogEntry("scan_failed", model.StatusFailed, "scan operation failed", err.Error(), subject, time.Since(started), 0))
}

func scanDiagnosticLogger(log *logutil.Logger) func(scanner.Diagnostic) {
	return func(diagnostic scanner.Diagnostic) {
		detail := diagnostic.Detail
		if detail == "" && diagnostic.Err != nil {
			detail = diagnostic.Err.Error()
		}
		switch diagnostic.Event {
		case "package_incomplete":
			_ = log.Warn(scanLogEntry("scan_package_incomplete", model.StatusFailed, "package scan incomplete", detail, diagnostic.Subject, 0, 0))
		case "path_action_binding_skipped":
			_ = log.Warn(scanLogEntry("scan_path_action_binding_skipped", model.StatusSkipped, "path action binding skipped", detail, diagnostic.Subject, 0, 0))
		}
	}
}

func (s *Service) githubResolver(catalog model.Config) scanner.GitHubResolver {
	if s.GitHubResolver != nil {
		return s.GitHubResolver
	}
	httpSettings := catalog.Settings.HTTP
	source := httpx.NewHTTPSource(nil, httpx.HTTPOptions{
		Timeout:               time.Duration(httpSettings.TimeoutSeconds) * time.Second,
		MaxConcurrencyPerHost: httpSettings.MaxConcurrencyPerHost,
		Retries:               httpSettings.Retries,
	})
	return scanner.NewGitHubResolver(
		catalog.Settings.ProviderURLs[string(model.ProviderGitHubRelease)],
		catalog.Settings.ProviderURLs[string(model.ProviderGitHubTag)],
		source,
	)
}

func cloneScanState(state model.RuntimeState) model.RuntimeState {
	cloned := model.RuntimeState{Observations: make(map[string]model.ScanObservation, len(state.Observations))}
	maps.Copy(cloned.Observations, state.Observations)
	return cloned
}

func persistExistingStatuses(base, candidate model.Config) model.Config {
	base.Apps = cloneApplications(base.Apps)
	statuses := make(map[string]model.ManagedStatus, len(candidate.Apps))
	for _, application := range candidate.Apps {
		statuses[application.ID] = application.StatusManaged
	}
	for index := range base.Apps {
		if status, found := statuses[base.Apps[index].ID]; found {
			base.Apps[index].StatusManaged = status
		}
	}
	return base
}

// savePreviewStatuses retries only the narrow revision window after scanning.
// The scan itself has already completed and is never repeated.
func (s *Service) savePreviewStatuses(base, completed model.Config) (model.Config, error) {
	for attempt := 0; attempt < snapshotSaveAttempts; attempt++ {
		latest, err := s.config.Snapshot()
		if err != nil {
			return model.Config{}, err
		}
		if !model.ConfigEqualExceptRuntime(base, latest.Config) {
			return model.Config{}, config.ErrStaleOperation
		}
		merged, ok := model.MergeRunStatuses(base, latest.Config, completed)
		if !ok {
			return model.Config{}, config.ErrStaleOperation
		}
		err = s.config.Save(latest.Revision, merged)
		if errors.Is(err, config.ErrStaleOperation) {
			continue
		}
		if err != nil {
			return model.Config{}, err
		}
		return merged, nil
	}
	return model.Config{}, config.ErrStaleOperation
}

// SaveScanSnapshot persists a confirmed TUI scan snapshot when its base is still current.
func (s *Service) SaveScanSnapshot(expectedCatalog, catalog model.Config) error {
	for attempt := 0; attempt < snapshotSaveAttempts; attempt++ {
		snapshot, err := s.config.Snapshot()
		if err != nil {
			return err
		}
		if !model.ConfigEqualExceptStatuses(snapshot.Config, expectedCatalog) {
			return config.ErrStaleOperation
		}
		candidate := catalog
		candidate.Apps = append([]model.Application(nil), catalog.Apps...)
		candidate = scanner.ReconcileNewlyManagedBundleIDs(context.Background(), expectedCatalog, candidate)
		model.CopyStatuses(&candidate, snapshot.Config)
		err = s.config.Save(snapshot.Revision, candidate)
		if errors.Is(err, config.ErrStaleOperation) {
			continue
		}
		return err
	}
	return config.ErrStaleOperation
}

func appIDs(apps []model.Application) map[string]struct{} {
	ids := make(map[string]struct{}, len(apps))
	for _, app := range apps {
		ids[app.ID] = struct{}{}
	}
	return ids
}

func applicationsOutsideIDs(excludedIDs map[string]struct{}, applications []model.Application) []model.Application {
	result := make([]model.Application, 0)
	for _, app := range applications {
		if _, exists := excludedIDs[app.ID]; !exists {
			result = append(result, app)
		}
	}
	return result
}

func removedApps(existing, proposed []model.Application) []model.Application {
	return applicationsOutsideIDs(appIDs(proposed), existing)
}

func scanApplicationChanges(current, proposed []model.Application) []model.ScanApplicationChange {
	currentByID := make(map[string]model.Application, len(current))
	for _, application := range current {
		currentByID[application.ID] = application
	}

	changes := make([]model.ScanApplicationChange, 0)
	for _, candidate := range proposed {
		existing, found := currentByID[candidate.ID]
		if !found {
			continue
		}
		fields := changedApplicationFields(existing, candidate)
		if len(fields) == 0 {
			continue
		}
		changes = append(changes, model.ScanApplicationChange{Current: existing, Proposed: candidate, Fields: fields})
	}
	return changes
}

func changedApplicationFields(current, proposed model.Application) []model.ScanFieldChange {
	changes := make([]model.ScanFieldChange, 0)
	add := func(field, currentValue, proposedValue string) {
		if currentValue != proposedValue {
			changes = append(changes, model.ScanFieldChange{Field: field, Current: currentValue, Proposed: proposedValue})
		}
	}

	add(model.ApplicationFieldName, current.Name, proposed.Name)
	add(model.ApplicationFieldType, current.Type, proposed.Type)
	add(model.ApplicationFieldDescription, current.Description, proposed.Description)
	add(model.ApplicationFieldURL, current.URL, proposed.URL)
	add(model.ApplicationFieldInstallPath, current.InstallPath, proposed.InstallPath)
	add(model.ApplicationFieldEnabled, strconv.FormatBool(current.Enabled), strconv.FormatBool(proposed.Enabled))
	add(model.ApplicationFieldUpdateMode, string(current.UpdateMode), string(proposed.UpdateMode))
	add(model.ApplicationFieldProviderType, string(current.Provider.Type), string(proposed.Provider.Type))
	add(model.ApplicationFieldPackage, current.Package, proposed.Package)
	add(model.ApplicationFieldActionVersion, current.Provider.VersionAction(), proposed.Provider.VersionAction())
	add(model.ApplicationFieldActionCheck, current.Provider.CheckAction(), proposed.Provider.CheckAction())
	add(model.ApplicationFieldActionUpdate, current.Provider.UpdateAction(), proposed.Provider.UpdateAction())
	add(model.ApplicationFieldActionDownload, downloadChangeValue(current.Provider.DownloadAction()), downloadChangeValue(proposed.Provider.DownloadAction()))
	add(model.ApplicationFieldActionInstall, current.Provider.InstallAction(), proposed.Provider.InstallAction())
	add(model.ApplicationFieldIdentity, current.Identity, proposed.Identity)
	add(model.ApplicationFieldScanManaged, strconv.FormatBool(current.ScanManaged), strconv.FormatBool(proposed.ScanManaged))
	return changes
}

func downloadChangeValue(download *model.Download) string {
	if download == nil {
		return ""
	}
	value, err := json.Marshal(download)
	if err != nil {
		return ""
	}
	return string(value)
}

func cloneApplications(applications []model.Application) []model.Application {
	cloned := make([]model.Application, len(applications))
	for index, application := range applications {
		cloned[index] = cloneApplication(application)
	}
	return cloned
}

func cloneApplication(application model.Application) model.Application {
	cloned := application
	cloned.Environment = maps.Clone(application.Environment)
	if application.Provider.Actions != nil {
		actions := *application.Provider.Actions
		actions.Download = cloneDownload(application.Provider.DownloadAction())
		cloned.Provider.Actions = &actions
	}
	return cloned
}

func cloneDownload(source *model.Download) *model.Download {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.ExtraArgs = append([]string(nil), source.ExtraArgs...)
	return &cloned
}
