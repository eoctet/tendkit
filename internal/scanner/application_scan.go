package scanner

import (
	"context"
	"errors"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	"github.com/eoctet/tendkit/internal/scanner/handler"
	"github.com/eoctet/tendkit/pkg/i18n"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

// observeConfiguredApplication records only locally provable version state;
// explicit version actions take precedence over bundle metadata inspection.
func (s Scanner) observeConfiguredApplication(ctx context.Context, application model.Application) (model.ManagedStatus, error) {
	observed := model.ManagedStatus{UpdateStatus: model.StatusMissing}
	if !installedPath(application.InstallPath) {
		return observed, nil
	}
	observed.UpdateStatus = model.StatusUnchecked
	if strings.TrimSpace(application.Provider.VersionAction()) != "" {
		result, err := s.runVersionAction(ctx, application, application.StatusManaged)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return observed, ctxErr
			}
			observed.Error, observed.UpdateStatus = i18n.ErrorText(err), model.StatusFailed
		} else if result.ExitCode != 0 {
			observed.Error, observed.UpdateStatus = i18n.T("scanner.command_exit", result.ExitCode), model.StatusFailed
		} else if value, versionErr := version.Extract(result.Combined()); versionErr == nil {
			observed.CurrentVersion = value
		} else {
			observed.Error, observed.UpdateStatus = versionErr.Error(), model.StatusFailed
		}
	} else if strings.HasSuffix(strings.ToLower(application.InstallPath), strings.ToLower(metadatautil.ApplicationExtension)) {
		observed.CurrentVersion = inspectApplication(ctx, application.InstallPath).Version
	}
	return observed, nil
}

// ScanApplication refreshes one application against a cloned runtime state.
// Scan-managed built-in PATH applications and bundles may be relocated by
// targeted discovery; all other applications are observed at their configured
// location without enumerating the surrounding domain.
func (s Scanner) ScanApplication(ctx context.Context, application model.Application, state model.RuntimeState) (model.Application, model.RuntimeState, error) {
	application = cloneApplication(application)
	state = cloneRuntimeState(state)
	s.report(model.ScanStagePrepare, "")
	s.report(model.ScanStageApplication, application.Name)
	var discovered discovery
	found := false
	if application.ScanManaged {
		var err error
		discovered, found, err = s.discoverTargetApplication(ctx, application)
		if err != nil {
			return application, state, err
		}
	}
	var observed model.ManagedStatus
	var err error
	if found && application.ScanManaged {
		application = mergeApps([]model.Application{application}, []model.Application{discovered.App})[0]
		observed = discovered.State
	} else {
		if application.ScanManaged {
			application = enrichConfiguredMetadata(ctx, []model.Application{application})[0]
		}
		observed, err = s.observeKnownApplication(ctx, application)
		if err != nil {
			return application, state, err
		}
	}
	if err := ctx.Err(); err != nil {
		return application, state, err
	}
	observed = preserveScannedApplicationState(observed, application.StatusManaged)
	now := model.Now()
	if observed.UpdateStatus != model.StatusMissing && observed.FirstDetectedTime == "" {
		observed.FirstDetectedTime = now
	}
	application.StatusManaged = observed
	if state.Observations == nil {
		state.Observations = map[string]model.ScanObservation{}
	}
	state.Observations[application.ID] = model.ScanObservation{Found: observed.UpdateStatus != model.StatusMissing, Path: application.InstallPath}
	s.report(model.ScanStageFinalize, "")
	return application, state, nil
}

func (s Scanner) observeKnownApplication(ctx context.Context, application model.Application) (model.ManagedStatus, error) {
	observed := model.ManagedStatus{UpdateStatus: model.StatusMissing}
	if installedPath(application.InstallPath) {
		observed.UpdateStatus = model.StatusUnchecked
		if strings.TrimSpace(application.Provider.VersionAction()) != "" {
			result, err := s.runVersionAction(ctx, application, application.StatusManaged)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return observed, ctxErr
				}
				observed.Error = i18n.ErrorText(err)
				observed.UpdateStatus = model.StatusFailed
			} else if result.ExitCode != 0 {
				observed.Error = i18n.T("scanner.command_exit", result.ExitCode)
				observed.UpdateStatus = model.StatusFailed
			} else if value, err := version.Extract(result.Combined()); err == nil {
				observed.CurrentVersion = value
			} else {
				observed.Error = i18n.ErrorText(err)
				observed.UpdateStatus = model.StatusFailed
			}
		} else if strings.HasSuffix(strings.ToLower(application.InstallPath), strings.ToLower(metadatautil.ApplicationExtension)) {
			observed.CurrentVersion = inspectApplication(ctx, application.InstallPath).Version
		}
	}
	return observed, nil
}

// runVersionAction renders the explicit action with the prior observation and
// executes it in the application's scan environment.
func (s Scanner) runVersionAction(ctx context.Context, application model.Application, state model.ManagedStatus) (runtimeutil.Result, error) {
	action, err := runtimeutil.Render(application.Provider.VersionAction(), versionActionValues(application, state, s.DownloadDir), true)
	if err != nil {
		return runtimeutil.Result{}, err
	}
	return s.Runner.Run(ctx, action, scanEnvironment(application))
}

func versionActionValues(application model.Application, state model.ManagedStatus, downloadDir string) map[string]string {
	architecture := runtimeutil.ActionArchitecture()
	return map[string]string{
		"id":              application.ID,
		"name":            application.Name,
		"app_name":        application.Name,
		"install_path":    application.InstallPath,
		"current_version": state.CurrentVersion,
		"latest_version":  state.LatestVersion,
		"last_version":    state.LatestVersion,
		"download_dir":    downloadDir,
		"arch":            architecture,
	}
}

func (s Scanner) discoverTargetApplication(ctx context.Context, target model.Application) (discovery, bool, error) {
	if target.Type == model.ApplicationTypeBundle {
		candidate, found, err := handler.NewMacApp(builtin.MacAppDefinitions(), s.bundleIDs).ScanApplication(ctx, target, handler.Request{})
		if err != nil || !found {
			return discovery{}, found, err
		}
		candidate.Application, err = s.resolveGitHub(ctx, candidate.Application)
		if err != nil {
			return discovery{}, false, err
		}
		return s.pathDiscovery(candidate), true, nil
	}
	if target.Type != model.ApplicationTypeCLI && target.Type != model.ApplicationTypeSDK {
		return discovery{}, false, nil
	}
	candidate, found, err := handler.NewPath(s.Runner, builtin.PathDefinitions()).ScanApplication(ctx, target, handler.Request{
		Diagnostic: func(diagnostic handler.Diagnostic) { s.reportDiagnostic(Diagnostic(diagnostic)) },
	})
	if err != nil || !found {
		return discovery{}, found, err
	}
	candidate.Application, err = s.resolveGitHub(ctx, candidate.Application)
	if err != nil {
		return discovery{}, false, err
	}
	return s.pathDiscovery(candidate), true, nil
}

func (s Scanner) pathDiscovery(candidate handler.Candidate) discovery {
	state := model.ManagedStatus{CurrentVersion: candidate.CurrentVersion, UpdateStatus: model.StatusUnchecked}
	if candidate.ObservationErr != nil {
		state.UpdateStatus = model.StatusFailed
		var exit handler.CommandExitError
		if errors.As(candidate.ObservationErr, &exit) {
			state.Error = i18n.T("scanner.command_exit", exit.ExitCode)
		} else {
			state.Error = i18n.ErrorText(candidate.ObservationErr)
		}
	}
	return discovery{App: candidate.Application, State: state}
}

// preserveScannedApplicationState carries forward update workflow fields that a
// version observation is not authoritative to erase.
func preserveScannedApplicationState(observed, previous model.ManagedStatus) model.ManagedStatus {
	if strings.TrimSpace(observed.UpdateStatus) == "" {
		observed.UpdateStatus = model.StatusUnchecked
	}
	if observed.UpdateStatus == model.StatusUnchecked && preservesUpdateStatus(previous.UpdateStatus) {
		observed.UpdateStatus = previous.UpdateStatus
		if observed.Error == "" {
			observed.Error = previous.Error
		}
	}
	if observed.CurrentVersion == "" {
		observed.CurrentVersion = previous.CurrentVersion
	}
	if observed.FirstDetectedTime == "" {
		observed.FirstDetectedTime = previous.FirstDetectedTime
	}
	observed.LatestVersion = previous.LatestVersion
	observed.HasUpdate = previous.HasUpdate
	observed.LastCheckTime = previous.LastCheckTime
	observed.LastUpdateTime = previous.LastUpdateTime
	observed.DownloadPath = previous.DownloadPath
	return observed
}

func enrichConfiguredMetadata(ctx context.Context, apps []model.Application) []model.Application {
	for index := range apps {
		app := &apps[index]
		if !app.ScanManaged || app.Type != model.ApplicationTypeBundle || strings.TrimSpace(app.Description) != "" || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(app.InstallPath)), metadatautil.ApplicationExtension) {
			continue
		}
		app.Description = inspectApplication(ctx, app.InstallPath).Description
	}
	return apps
}

func preservesUpdateStatus(status string) bool {
	switch status {
	case model.StatusSkipped, model.StatusFailed, model.StatusCurrent, model.StatusUpdateAvailable,
		model.StatusDownloaded, model.StatusDownloadedUnverified, model.StatusUpdated:
		return true
	default:
		return false
	}
}
