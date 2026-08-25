package updater

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	providerpkg "github.com/eoctet/tendkit/internal/updater/provider"
	downloadutil "github.com/eoctet/tendkit/pkg/downloader"
	"github.com/eoctet/tendkit/pkg/i18n"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

// engine executes application operations with a bounded worker pool.
type engine struct {
	Config           model.Config
	checker          versionChecker
	logger           *logutil.Logger
	AppStart         func(model.Result)
	Output           func(model.Result)
	UpdateStart      func(model.Result)
	DownloadStart    func(model.Result)
	DownloadProgress func(model.DownloadProgress)
	DownloadOutput   func(model.Application) (io.WriteCloser, io.WriteCloser)
}

// versionChecker is the provider boundary used by the execution engine.
type versionChecker interface {
	current(context.Context, model.Application, string) (currentResolution, error)
	latest(context.Context, model.Application, string) (string, error)
	executeUpdate(context.Context, model.Application, model.ManagedStatus) (runtimeutil.Result, error)
	executeInstall(context.Context, model.Application, model.ManagedStatus) (runtimeutil.Result, error)
	resolveDownload(context.Context, model.Application, model.ManagedStatus, ...string) (downloadResolution, error)
	downloadAssetCandidates(context.Context, model.Application) (model.DownloadAssetChoices, error)
	httpSource() *providerpkg.HTTPSource
}

// RunBatch executes requests appended before the batch becomes idle. It rejects
// duplicate IDs only while the same application is pending or in flight; a
// completed application may be scheduled again in the same transaction.
func (e *engine) runBatch(ctx context.Context, config model.Config, batch *batch) (model.Config, []model.Result) {
	workers := e.Config.Settings.Workers
	if workers < 1 {
		workers = 1
	}
	type job struct {
		index   int
		app     model.Application
		old     model.ManagedStatus
		options RunOptions
	}
	type finished struct {
		index  int
		result model.Result
	}
	jobs := make(chan job)
	done := make(chan finished, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for item := range jobs {
				if e.AppStart != nil {
					mode := item.app.UpdateMode
					if item.options.CheckOnly {
						mode = model.ModeCheck
					}
					e.AppStart(model.Result{AppID: item.app.ID, Name: item.app.Name, Mode: mode, Status: model.StatusChecking, State: item.old})
				}
				result := e.process(ctx, item.app, item.old, item.options)
				done <- finished{index: item.index, result: result}
			}
		}()
	}
	pending := make([]job, 0)
	completed := make(map[int]model.Result)
	scheduled := make(map[string]bool)
	nextIndex := 0
	inFlight := 0
	jobsClosed := false
	ctxDone := ctx.Done()
	cancelled := false

	for {
		if !cancelled {
			for _, request := range batch.drain() {
				for _, app := range selectApps(e.Config.Apps, request.Names) {
					if scheduled[app.ID] {
						continue
					}
					scheduled[app.ID] = true
					pending = append(pending, job{index: nextIndex, app: app, old: app.StatusManaged, options: request})
					nextIndex++
				}
			}
		}
		if len(pending) == 0 && inFlight == 0 {
			if cancelled || batch.closeIfIdle() {
				break
			}
			continue
		}

		var readyJobs chan job
		var next job
		if !cancelled && len(pending) > 0 {
			readyJobs = jobs
			next = pending[0]
		}
		select {
		case readyJobs <- next:
			pending = pending[1:]
			inFlight++
		case item := <-done:
			inFlight--
			delete(scheduled, item.result.AppID)
			completed[item.index] = item.result
			setApplicationStatus(&config, item.result.AppID, item.result.State)
			if e.Output != nil && ctx.Err() == nil {
				e.Output(item.result)
			}
		case <-batch.notify:
		case <-ctxDone:
			batch.close()
			pending = nil
			cancelled = true
			ctxDone = nil
			close(jobs)
			jobsClosed = true
		}
	}
	batch.close()
	if !jobsClosed {
		close(jobs)
	}
	group.Wait()

	results := make([]model.Result, nextIndex)
	for index, result := range completed {
		results[index] = result
	}
	return config, results
}

func setApplicationStatus(config *model.Config, id string, status model.ManagedStatus) {
	for index := range config.Apps {
		if config.Apps[index].ID == id {
			config.Apps[index].StatusManaged = status
			return
		}
	}
}

func (e *engine) process(ctx context.Context, app model.Application, old model.ManagedStatus, options RunOptions) model.Result {
	ctx = withApplication(ctx, app)
	if e.logger != nil {
		e.logger.AddSensitiveEnvironment(app.Environment)
	}
	mode := app.UpdateMode
	if options.CheckOnly {
		mode = model.ModeCheck
	}
	ctx = withOperation(ctx, operationForMode(mode))
	result := model.Result{AppID: app.ID, Name: app.Name, Mode: mode, State: old}
	result.State.LastCheckTime = model.Now()
	result.State.Error = ""
	if !app.Enabled {
		result.Status, result.Message = model.StatusSkipped, i18n.T("engine.disabled")
		result.State.UpdateStatus = result.Status
		_ = e.logger.Warn(operationLogEntry(app, operationForMode(mode), result.Status, "application operation completed with warning", result.Message, 0, ""))
		return result
	}
	if !installed(app.InstallPath) {
		result.Status, result.Message = model.StatusMissing, i18n.T("engine.missing")
		result.State.HasUpdate = false
		result.State.UpdateStatus = result.Status
		_ = e.logger.Warn(operationLogEntry(app, operationForMode(mode), result.Status, "application operation completed with warning", result.Message, 0, ""))
		return result
	}
	current, err := e.detectVersion(ctx, app, old.CurrentVersion)
	if err != nil {
		return e.fail(app, result, model.OperationVersion, err, 0)
	}
	if current != "" {
		result.State.CurrentVersion = current
	}
	started := time.Now()
	latest, err := e.checker.latest(ctx, app, result.State.CurrentVersion)
	if errors.Is(err, providerpkg.ErrUnavailable) {
		result.Status, result.Message = model.StatusSkipped, i18n.ErrorText(err)
		result.State.HasUpdate = false
		result.State.UpdateStatus = result.Status
		_ = e.logger.Warn(operationLogEntry(app, model.OperationCheck, result.Status, "application operation completed with warning", result.Message, time.Since(started), ""))
		return result
	}
	if err != nil {
		return e.fail(app, result, model.OperationCheck, err, time.Since(started))
	}
	result.State.LatestVersion = latest
	if result.State.CurrentVersion == "" {
		return e.fail(app, result, model.OperationCheck, errors.New(i18n.T("engine.current_unknown")), time.Since(started))
	}
	result.State.HasUpdate = version.IsNewer(latest, result.State.CurrentVersion)
	if !result.State.HasUpdate {
		result.Status, result.Message = model.StatusCurrent, result.State.CurrentVersion
		result.State.UpdateStatus = result.Status
		return result
	}
	if mode == model.ModeCheck {
		result.Status, result.Message = model.StatusUpdateAvailable, result.State.CurrentVersion+" -> "+latest
		result.State.UpdateStatus = result.Status
		return result
	}
	if mode == model.ModeDownload {
		return e.download(ctx, app, result, options.DownloadAssets[app.ID])
	}
	if e.UpdateStart != nil {
		pending := result
		pending.Status = model.StatusUpdating
		pending.Message = ""
		e.UpdateStart(pending)
	}
	if mode == model.ModeInstall {
		return e.install(ctx, app, result)
	}
	return e.update(ctx, app, result)
}

func (e *engine) detectVersion(ctx context.Context, app model.Application, fallback string) (string, error) {
	started := time.Now()
	resolved, err := e.checker.current(ctx, app, fallback)
	if !resolved.FromAction {
		return resolved.Version, err
	}
	duration := resolved.Duration
	if duration == 0 {
		duration = time.Since(started)
	}
	if err != nil {
		_ = e.logger.Error(operationLogEntry(app, model.OperationVersion, model.StatusFailed, "application operation failed", message(err, resolved.Version), duration, ""))
	} else {
		_ = e.logger.Debug(operationLogEntry(app, model.OperationVersion, model.StatusSuccess, "application operation completed", resolved.Version, duration, ""))
	}
	return resolved.Version, err
}

func (e *engine) update(ctx context.Context, app model.Application, result model.Result) model.Result {
	started := time.Now()
	commandResult, err := e.checker.executeUpdate(ctx, app, result.State)
	if err != nil {
		return e.fail(app, result, model.OperationUpdate, err, time.Since(started))
	}
	installedVersion, err := e.verifyInstalledVersion(ctx, app, result.State)
	if err != nil {
		return e.fail(app, result, model.OperationUpdate, err, commandResult.Duration)
	}
	return e.completedUpdateResult(app, result, model.OperationUpdate, installedVersion, commandResult.Duration)
}

func (e *engine) install(ctx context.Context, app model.Application, result model.Result) model.Result {
	started := time.Now()
	updateResult, err := e.checker.executeUpdate(ctx, app, result.State)
	if err != nil {
		return e.fail(app, result, model.OperationUpdate, err, time.Since(started))
	}
	_ = e.logger.Info(operationLogEntry(app, model.OperationUpdate, model.StatusSuccess, "application operation completed", i18n.T("engine.updated", result.State.LatestVersion), updateResult.Duration, ""))
	commandResult, err := e.checker.executeInstall(ctx, app, result.State)
	if err != nil {
		return e.fail(app, result, model.OperationInstall, err, time.Since(started))
	}
	installedVersion, err := e.verifyInstalledVersion(ctx, app, result.State)
	if err != nil {
		return e.fail(app, result, model.OperationInstall, err, commandResult.Duration)
	}
	return e.completedUpdateResult(app, result, model.OperationInstall, installedVersion, commandResult.Duration)
}

func (e *engine) verifyInstalledVersion(ctx context.Context, app model.Application, state model.ManagedStatus) (string, error) {
	installedVersion, err := e.detectVersion(ctx, app, "")
	if err != nil {
		return installedVersion, err
	}
	if !installedVersionVerified(installedVersion, state.CurrentVersion, state.LatestVersion) {
		return installedVersion, errors.New(i18n.T("engine.verify_failed", installedVersion, state.LatestVersion))
	}
	return installedVersion, nil
}

func installedVersionVerified(installed, current, latest string) bool {
	if installed == "" {
		return false
	}
	if latest == version.Available {
		return installed != current
	}
	return version.AtLeast(installed, latest)
}

func (e *engine) completedUpdateResult(app model.Application, result model.Result, operation, installedVersion string, duration time.Duration) model.Result {
	result.State.CurrentVersion = installedVersion
	result.State.LatestVersion = installedVersion
	result.State.HasUpdate = false
	result.State.LastUpdateTime = model.Now()
	result.State.UpdateStatus = model.StatusUpdated
	result.Status, result.Message = model.StatusUpdated, i18n.T("engine.updated", installedVersion)
	_ = e.logger.Info(operationLogEntry(app, operation, model.StatusSuccess, "application operation completed", result.Message, duration, ""))
	return result
}

func (e *engine) download(ctx context.Context, app model.Application, result model.Result, selectedArtifact string) model.Result {
	if e.DownloadStart != nil {
		pending := result
		pending.Status = model.StatusDownloading
		pending.Message = ""
		e.DownloadStart(pending)
	}
	started := time.Now()
	resolved, err := e.checker.resolveDownload(ctx, app, result.State, selectedArtifact)
	if err != nil {
		return e.fail(app, result, model.OperationDownload, err, time.Since(started))
	}
	output := io.Writer(io.Discard)
	errorOutput := io.Writer(io.Discard)
	if e.DownloadOutput != nil {
		stdout, stderr := e.DownloadOutput(app)
		if stdout != nil {
			output = stdout
			defer func() { _ = stdout.Close() }()
		}
		if stderr != nil {
			errorOutput = stderr
			defer func() { _ = stderr.Close() }()
		}
	}
	d := downloadutil.Downloader{Settings: e.Config.Settings.Downloader, Output: output, ErrorOutput: errorOutput, ChecksumError: resolved.ChecksumErr}
	if source := e.checker.httpSource(); source != nil {
		d.FetchText = source.GetText
	}
	if e.DownloadProgress != nil {
		d.Progress = func(progress model.DownloadProgress) {
			progress.AppID, progress.Name = app.ID, app.Name
			e.DownloadProgress(progress)
		}
	}
	download, err := d.Download(ctx, resolved.Spec, placeholders(app, result.State, e.Config.Settings.Downloader.StorePath))
	if err != nil {
		return e.fail(app, result, model.OperationDownload, localizeDownloaderError(err), time.Since(started))
	}
	switch download.Checksum {
	case downloadutil.ChecksumVerified:
		result.Status, result.Message = model.StatusDownloaded, i18n.T("engine.downloaded_verified", download.Path, download.SHA256)
	case downloadutil.ChecksumFailed:
		result.Status, result.Message = model.StatusDownloadedUnverified, i18n.T("engine.download_checksum_failed", download.Path, download.ExpectedSHA256, download.SHA256)
	case downloadutil.ChecksumIgnored:
		result.Status, result.Message = model.StatusDownloadedUnverified, i18n.T("engine.download_checksum_ignored", download.Path, download.ChecksumError)
	default:
		result.Status, result.Message = model.StatusDownloaded, i18n.T("engine.downloaded", download.Path)
	}
	result.State.UpdateStatus = result.Status
	if download.Checksum == downloadutil.ChecksumFailed {
		result.State.DownloadPath = ""
	} else {
		result.State.DownloadPath = download.Path
	}
	result.State.LastUpdateTime = model.Now()
	if download.Checksum == downloadutil.ChecksumFailed || download.Checksum == downloadutil.ChecksumIgnored {
		_ = e.logger.Warn(operationLogEntry(app, model.OperationDownload, model.StatusDownloadedUnverified, "application operation completed with warning", result.Message, time.Since(started), resolved.Artifact))
	} else {
		_ = e.logger.Info(operationLogEntry(app, model.OperationDownload, model.StatusSuccess, "application operation completed", result.Message, time.Since(started), resolved.Artifact))
	}
	return result
}

func localizeDownloaderError(err error) error {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		items := joined.Unwrap()
		localized := make([]error, 0, len(items))
		for _, item := range items {
			localized = append(localized, localizeDownloaderError(item))
		}
		return errors.Join(localized...)
	}
	var typed *downloadutil.DownloaderError
	if errors.As(err, &typed) {
		return localizedProviderError{text: i18n.T(typed.Key, typed.Args...), cause: err}
	}
	return err
}

func (e *engine) fail(app model.Application, result model.Result, operation string, err error, duration time.Duration) model.Result {
	cleaned := redact(i18n.ErrorText(err), app.Environment)
	result.Status, result.Message = model.StatusFailed, cleaned
	result.State.UpdateStatus = result.Status
	result.State.Error = cleaned
	_ = e.logger.Error(operationLogEntry(app, operation, model.StatusFailed, "application operation failed", cleaned, duration, ""))
	return result
}

func operationLogEntry(app model.Application, operation, status, summary, detail string, duration time.Duration, artifact string) logutil.LogEntry {
	return logutil.LogEntry{Event: "app_operation", AppID: app.ID, AppName: app.Name, Operation: operation, Status: status, Message: summary, Detail: detail, Artifact: artifact, DurationMS: duration.Milliseconds()}
}

func operationForMode(mode model.UpdateMode) string {
	switch mode {
	case model.ModeCheck:
		return model.OperationCheck
	case model.ModeDownload:
		return model.OperationDownload
	case model.ModeInstall:
		return model.OperationInstall
	default:
		return model.OperationUpdate
	}
}

func installed(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if strings.Contains(path, string(filepath.Separator)) {
		_, err := os.Stat(path)
		return err == nil
	}
	_, err := exec.LookPath(path)
	return err == nil
}

func selectApps(apps []model.Application, names []string) []model.Application {
	if len(names) == 0 {
		return append([]model.Application(nil), apps...)
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[strings.ToLower(name)] = true
	}
	selected := make([]model.Application, 0)
	for _, app := range apps {
		if wanted[strings.ToLower(app.ID)] || wanted[strings.ToLower(app.Name)] {
			selected = append(selected, app)
		}
	}
	return selected
}

func message(err error, success string) string {
	if err != nil {
		return i18n.ErrorText(err)
	}
	return success
}

func redact(text string, configured map[string]string) string {
	values := make(map[string]struct{}, len(configured)+4)
	for key, value := range configured {
		if value != "" && runtimeutil.IsSensitiveEnvironmentKey(key) {
			values[value] = struct{}{}
		}
	}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok && value != "" && runtimeutil.IsSensitiveEnvironmentKey(key) {
			values[value] = struct{}{}
		}
	}
	return logutil.RedactSensitiveValues(text, values)
}
