package scanner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	"github.com/eoctet/tendkit/internal/scanner/handler"
	httpx "github.com/eoctet/tendkit/pkg/http"
	"github.com/eoctet/tendkit/pkg/i18n"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

// Progress is retained as the scanner facade spelling for the stable model event.
type Progress = model.ScanProgress

// GitHubResolver is the Scanner-facing capability for classifying a GitHub project.
// Its implementation remains private to the handler package.
type GitHubResolver interface {
	Resolve(context.Context, string) (model.ProviderType, error)
}

// NewGitHubResolver constructs the Scanner facade's GitHub resolver.
func NewGitHubResolver(releaseEndpoint, tagEndpoint string, source *httpx.HTTPSource) GitHubResolver {
	return handler.NewGitHubResolver(releaseEndpoint, tagEndpoint, source)
}

// Scanner discovers configured and unmanaged development tools.
type Scanner struct {
	Runner      runtimeutil.Runner
	Progress    func(Progress)
	DownloadDir string
	GitHub      GitHubResolver
	bundleIDs   []string
}

// New creates a scanner using the configured scan rules.
func New(settings model.ScanSettings) Scanner {
	return Scanner{bundleIDs: append([]string{}, settings.BundleID...)}
}

type discovery struct {
	App   model.Application
	State model.ManagedStatus
}

type scanSession struct {
	scanner    Scanner
	catalog    model.Config
	state      model.RuntimeState
	exclusions exclusionMatcher
	index      existingIndex
	discovered []model.Application
	observed   map[string]model.ManagedStatus
	packages   packageScanResult
}

// Scan reconciles discoveries with catalog and state snapshots.
func (s Scanner) Scan(ctx context.Context, catalog model.Config, state model.RuntimeState) (model.Config, model.RuntimeState, error) {
	catalog.Apps = cloneApplications(catalog.Apps)
	state = cloneRuntimeState(state)
	s.bundleIDs = append([]string{}, catalog.Settings.Scan.BundleID...)
	s.DownloadDir = catalog.Settings.Downloader.StorePath
	session, err := s.prepareScan(ctx, catalog, state)
	if err != nil {
		return catalog, state, err
	}
	if err := session.discoverPath(ctx); err != nil {
		return session.catalog, session.state, err
	}
	if err := session.discoverApplications(ctx); err != nil {
		return session.catalog, session.state, err
	}
	if err := session.discoverPackages(ctx); err != nil {
		return session.catalog, session.state, err
	}
	if err := session.observeConfiguredApplications(ctx); err != nil {
		return session.catalog, session.state, err
	}
	return session.finalize(ctx)
}

func (s Scanner) prepareScan(ctx context.Context, catalog model.Config, state model.RuntimeState) (*scanSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.report(model.ScanStagePrepare, "")
	exclusions := newExclusionMatcher(catalog.Settings.Scan.Exclude)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog.Apps, state = deduplicateCatalog(catalog.Apps, state)
	catalog.Apps = enrichConfiguredMetadata(ctx, catalog.Apps)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &scanSession{
		scanner: s, catalog: catalog, state: state, exclusions: exclusions,
		index: indexApps(catalog.Apps), discovered: make([]model.Application, 0),
		observed: map[string]model.ManagedStatus{},
	}, nil
}

func (session *scanSession) add(found discovery) {
	if existingID := session.index.match(found.App); existingID != "" {
		found.App.ID = existingID
		configured := session.index.apps[existingID]
		if configured.ID != "" && configured.Type != found.App.Type {
			found.App.InstallPath = configured.InstallPath
		}
		if configured.ID != "" && (!configured.ScanManaged || session.exclusions.excluded(configured) || session.exclusions.excluded(found.App)) {
			return
		}
	}
	if session.exclusions.excluded(found.App) {
		return
	}
	session.discovered = append(session.discovered, found.App)
	session.observed[found.App.ID] = found.State
}

func (session *scanSession) discoverPath(ctx context.Context) error {
	if !session.catalog.Settings.Scan.Path {
		return nil
	}
	session.scanner.report(model.ScanStagePath, "")
	result := handler.NewPath(session.scanner.Runner, builtin.PathDefinitions()).Scan(ctx, handler.Request{Report: func(progress handler.Progress) { session.scanner.report(progress.Stage, progress.Subject) }})
	for _, candidate := range result.Candidates {
		resolved, err := session.scanner.resolveGitHub(ctx, candidate.Application)
		if err != nil {
			return err
		}
		candidate.Application = resolved
		session.add(session.scanner.pathDiscovery(candidate))
	}
	return result.Err
}

func (session *scanSession) discoverApplications(ctx context.Context) error {
	if !session.catalog.Settings.Scan.Application {
		return nil
	}
	session.scanner.report(model.ScanStageMacOS, "")
	result := handler.NewMacApp(builtin.MacAppDefinitions(), session.scanner.bundleIDs).Scan(ctx, handler.Request{Report: func(progress handler.Progress) { session.scanner.report(progress.Stage, progress.Subject) }})
	for _, candidate := range result.Candidates {
		resolved, err := session.scanner.resolveGitHub(ctx, candidate.Application)
		if err != nil {
			return err
		}
		candidate.Application = resolved
		if session.exclusions.excluded(candidate.Application, candidate.Aliases...) {
			continue
		}
		session.add(session.scanner.pathDiscovery(candidate))
	}
	return result.Err
}

func (s Scanner) resolveGitHub(ctx context.Context, app model.Application) (model.Application, error) {
	if s.GitHub == nil || app.Provider.Type != model.ProviderDefault || app.Provider.CheckAction() != "" || app.Provider.UpdateAction() != "" {
		return app, nil
	}
	project := handler.GitHubProject(app.URL)
	if project == "" {
		return app, nil
	}
	kind, err := s.GitHub.Resolve(ctx, project)
	if err != nil {
		return app, err
	}
	if kind == "" {
		return app, nil
	}
	actions := app.Provider.Actions
	if actions != nil {
		actions = &model.ProviderActions{Version: actions.Version}
	}
	app.Provider = model.ProviderConfig{Type: kind, Actions: actions}
	app.Package = project
	app.UpdateMode = model.ModeDownload
	app.Enabled = true
	return app, nil
}

func (session *scanSession) discoverPackages(ctx context.Context) error {
	settings := session.catalog.Settings.Scan.Packages
	if packageScanningEnabled(settings) {
		session.scanner.report(model.ScanStagePackages, "")
	}
	packageScan := scanPackages(ctx, settings, session.scanner.Runner, session.exclusions, session.catalog.Apps, session.scanner.report)
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, found := range packageScan.Discoveries {
		session.add(found)
	}
	session.packages = packageScan
	return nil
}

func (session *scanSession) observeConfiguredApplications(ctx context.Context) error {
	// Project-specific catalog entries are not necessarily covered by built-in discovery.
	for _, configured := range session.catalog.Apps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if session.retainExistingObservation(configured) || session.retainIncompleteManagedPackage(configured) {
			continue
		}
		session.scanner.report(model.ScanStageApplication, configured.Name)
		observed, err := session.scanner.observeConfiguredApplication(ctx, configured)
		if err != nil {
			return err
		}
		session.observed[configured.ID] = observed
	}
	return nil
}

func (session *scanSession) retainExistingObservation(application model.Application) bool {
	if _, exists := session.observed[application.ID]; exists {
		return true
	}
	if scanEnabledFor(application, session.catalog.Settings.Scan) {
		return false
	}
	session.observed[application.ID] = application.StatusManaged
	return true
}

func (session *scanSession) retainIncompleteManagedPackage(application model.Application) bool {
	if session.exclusions.excluded(application) || !application.ScanManaged || application.Type != model.ApplicationTypePackage {
		return false
	}
	ecosystem := managedPackageEcosystem(application)
	if session.packages.Complete[ecosystem] {
		return false
	}
	old := application.StatusManaged
	if scanErr := session.packages.Errors[ecosystem]; scanErr != nil {
		old.Error = i18n.T("scanner.package_scan_incomplete", ecosystem, packageScanErrorText(scanErr))
	}
	session.observed[application.ID] = old
	return true
}

func packageScanErrorText(err error) string {
	var unavailable *handler.PackageManagerUnavailableError
	if errors.As(err, &unavailable) {
		return i18n.T("scanner.package_manager_unavailable", unavailable.Manager)
	}
	var incomplete *handler.PackageInventoryIncompleteError
	if errors.As(err, &incomplete) {
		return i18n.T("scanner.package_inventory_incomplete", incomplete.Ecosystem)
	}
	return err.Error()
}

func managedPackageEcosystem(application model.Application) string {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(application.Identity)), ":", 3)
	if len(parts) == 3 && parts[0] == "package" {
		return parts[1]
	}
	return ""
}

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

func (session *scanSession) finalize(ctx context.Context) (model.Config, model.RuntimeState, error) {
	if err := ctx.Err(); err != nil {
		return session.catalog, session.state, err
	}
	session.catalog.Apps = mergeApps(session.catalog.Apps, session.discovered)
	session.scanner.report(model.ScanStageFinalize, "")
	for _, application := range session.catalog.Apps {
		if _, exists := session.observed[application.ID]; !exists {
			old := application.StatusManaged
			old.UpdateStatus = model.StatusMissing
			session.observed[application.ID] = old
		}
	}
	now := model.Now()
	for index := range session.catalog.Apps {
		id := session.catalog.Apps[index].ID
		value := session.observed[id]
		value = preserveScannedApplicationState(value, session.catalog.Apps[index].StatusManaged)
		if value.UpdateStatus != model.StatusMissing && value.FirstDetectedTime == "" {
			value.FirstDetectedTime = now
		}
		session.catalog.Apps[index].StatusManaged = value
	}
	session.catalog.Apps, session.state = deduplicateCatalog(session.catalog.Apps, session.state)
	if session.state.Observations == nil {
		session.state.Observations = map[string]model.ScanObservation{}
	}
	for _, application := range session.catalog.Apps {
		status := application.StatusManaged
		session.state.Observations[application.ID] = model.ScanObservation{Found: status.UpdateStatus != model.StatusMissing, Path: application.InstallPath}
	}
	return session.catalog, session.state, nil
}

// ScanApplication refreshes only one application. Built-in PATH applications may
// be relocated by their single definition; all other applications use their
// configured path and version command without enumerating the surrounding domain.
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
	candidate, found, err := handler.NewPath(s.Runner, builtin.PathDefinitions()).ScanApplication(ctx, target, handler.Request{})
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

func (s Scanner) report(stage, subject string) {
	if s.Progress != nil {
		s.Progress(Progress{Stage: stage, Subject: subject})
	}
}

func packageScanningEnabled(settings model.PackageScanSettings) bool {
	return settings.Python || settings.Node || settings.Go ||
		settings.UV || settings.Ruby
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

func mergeApps(existing, discovered []model.Application) []model.Application {
	byID := make(map[string]model.Application, len(existing)+len(discovered))
	order := make([]string, 0, len(existing)+len(discovered))
	for _, app := range existing {
		byID[app.ID] = cloneApplication(app)
		order = append(order, app.ID)
	}
	for _, found := range discovered {
		if configured, exists := byID[found.ID]; exists {
			if shouldCanonicalizeManagedPackage(configured, found) {
				configured.Name = found.Name
				configured.Type = found.Type
				configured.InstallPath = found.InstallPath
				configured.Identity = found.Identity
				if configured.Description == "" {
					configured.Description = found.Description
				}
				if configured.URL == "" {
					configured.URL = found.URL
				}
			}
			if configured.Type == found.Type || configured.InstallPath == "" {
				configured.InstallPath = found.InstallPath
			}
			if configured.Identity == "" && (configured.Type == found.Type || inferIdentity(configured) == inferIdentity(found)) {
				configured.Identity = found.Identity
			}
			if configured.Provider.VersionAction() == "" && found.Provider.VersionAction() != "" {
				actionConfig(&configured).Version = found.Provider.VersionAction()
			}
			if configured.ScanManaged {
				if configured.Description == "" {
					configured.Description = found.Description
				}
				if configured.URL == "" {
					configured.URL = found.URL
				}
				if configured.Provider.Type == model.ProviderDefault && found.Provider.Type != "" {
					configured.Provider.Type = found.Provider.Type
				}
				if configured.Package == "" {
					configured.Package = found.Package
				}
				if configured.Provider.CheckAction() == "" && found.Provider.CheckAction() != "" {
					actionConfig(&configured).Check = found.Provider.CheckAction()
				}
				if configured.Provider.UpdateAction() == "" && found.Provider.UpdateAction() != "" {
					actionConfig(&configured).Update = found.Provider.UpdateAction()
				}
				if configured.Provider.DownloadAction() == nil && found.Provider.DownloadAction() != nil {
					actionConfig(&configured).Download = cloneApplication(found).Provider.DownloadAction()
				}
				if configured.UpdateMode == model.ModeCheck && found.UpdateMode != model.ModeCheck {
					configured.UpdateMode = found.UpdateMode
				}
			}
			byID[found.ID] = configured
			continue
		}
		byID[found.ID] = cloneApplication(found)
		order = append(order, found.ID)
	}
	apps := make([]model.Application, 0, len(order))
	for _, id := range order {
		apps = append(apps, byID[id])
	}
	return apps
}

func shouldCanonicalizeManagedPackage(configured, found model.Application) bool {
	if !configured.ScanManaged || configured.Type != model.ApplicationTypePackage || found.Type != model.ApplicationTypeCLI {
		return false
	}
	configuredDefinition, configuredOK := matchingBuiltInPathDefinition(configured)
	foundDefinition, foundOK := matchingBuiltInPathDefinition(found)
	return configuredOK && foundOK && configuredDefinition.ID == foundDefinition.ID
}

func installedPath(path string) bool {
	path = installedPathValue(path)
	if filepath.IsAbs(path) || strings.Contains(path, string(filepath.Separator)) {
		_, err := os.Stat(path)
		return err == nil
	}
	_, err := exec.LookPath(path)
	return err == nil
}

func installedPathValue(path string) string {
	path = strings.TrimSpace(os.ExpandEnv(path))
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return path
}

func scanEnvironment(app model.Application) map[string]string {
	environment := make(map[string]string, len(app.Environment)+1)
	for key, value := range app.Environment {
		environment[key] = value
	}
	path := installedPathValue(app.InstallPath)
	if path != "" && (filepath.IsAbs(path) || strings.Contains(path, string(filepath.Separator))) {
		environment["PATH"] = strings.Join(uniqueStrings([]string{filepath.Dir(path), os.Getenv("PATH")}), string(os.PathListSeparator))
	}
	return environment
}

func scanEnabledFor(app model.Application, settings model.ScanSettings) bool {
	if app.Type == model.ApplicationTypeBundle {
		return settings.Application
	}
	if strings.HasPrefix(inferIdentity(app), "package:") || app.Type == model.ApplicationTypePackage {
		identity := inferIdentity(app)
		switch {
		case strings.HasPrefix(identity, "package:python:"):
			return settings.Packages.Python
		case strings.HasPrefix(identity, "package:node:"):
			return settings.Packages.Node
		case strings.HasPrefix(identity, "package:go:"):
			return settings.Packages.Go
		case strings.HasPrefix(identity, "package:uv:"):
			return settings.Packages.UV
		case strings.HasPrefix(identity, "package:ruby:"):
			return settings.Packages.Ruby
		}
	}
	return settings.Path
}

func cloneApplication(app model.Application) model.Application {
	cloned := app
	if app.Environment != nil {
		cloned.Environment = map[string]string{}
		for k, v := range app.Environment {
			cloned.Environment[k] = v
		}
	}
	if app.Provider.Actions != nil {
		actions := *app.Provider.Actions
		if actions.Download != nil {
			download := *actions.Download
			download.ExtraArgs = append([]string(nil), actions.Download.ExtraArgs...)
			actions.Download = &download
		}
		cloned.Provider.Actions = &actions
	}
	return cloned
}

func cloneApplications(apps []model.Application) []model.Application {
	cloned := make([]model.Application, len(apps))
	for i := range apps {
		cloned[i] = cloneApplication(apps[i])
	}
	return cloned
}

func cloneRuntimeState(state model.RuntimeState) model.RuntimeState {
	cloned := state
	if state.Observations != nil {
		cloned.Observations = map[string]model.ScanObservation{}
		for k, v := range state.Observations {
			cloned.Observations[k] = v
		}
	}
	return cloned
}

type appInfo struct {
	Path        string
	Name        string
	Description string
	BundleID    string
	Category    string
	Version     string
	FeedURL     string
}

func inspectApplication(parent context.Context, path string) appInfo {
	metadata, _ := handler.NewMacApp(nil, nil).Inspect(parent, path)
	return appInfo{
		Path: metadata.Path, Name: metadata.Name, BundleID: metadata.BundleID,
		Category: metadata.Category, Description: metadata.Description,
		Version: metadata.Version, FeedURL: metadata.FeedURL,
	}
}

func (s Scanner) isDevelopmentApplication(info appInfo) bool {
	if strings.EqualFold(info.Category, "public.app-category.developer-tools") {
		return true
	}
	bundleID := strings.ToLower(info.BundleID)
	if builtin.MatchesMacAppCatalog(bundleID, info.Name) {
		return true
	}
	for _, configured := range s.bundleIDs {
		if bundleID != "" && bundleID == strings.ToLower(strings.TrimSpace(configured)) {
			return true
		}
	}
	return false
}

// ReconcileNewlyManagedBundleIDs registers custom Bundle IDs at the moment a
// bundle transitions into scan-managed ownership. Keeping this in the same
// save transaction prevents the following scan from filtering the app first.
func ReconcileNewlyManagedBundleIDs(ctx context.Context, previous, proposed model.Config) model.Config {
	previousApps := make(map[string]model.Application, len(previous.Apps))
	for _, app := range previous.Apps {
		previousApps[app.ID] = app
	}
	scanner := New(proposed.Settings.Scan)
	for _, app := range proposed.Apps {
		if err := ctx.Err(); err != nil {
			break
		}
		old, existed := previousApps[app.ID]
		if !app.ScanManaged || app.Type != model.ApplicationTypeBundle || (existed && old.ScanManaged) {
			continue
		}
		scanner.registerManagedBundleID(ctx, app)
	}
	proposed.Settings.Scan.BundleID = scanner.bundleIDs
	return proposed
}

func (s *Scanner) registerManagedBundleID(ctx context.Context, app model.Application) {
	info := inspectApplication(ctx, app.InstallPath)
	bundleID := strings.ToLower(strings.TrimSpace(info.BundleID))
	if bundleID == "" || (Scanner{}).isDevelopmentApplication(info) || containsFold(s.bundleIDs, bundleID) {
		return
	}
	s.bundleIDs = append(s.bundleIDs, bundleID)
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func actionConfig(application *model.Application) *model.ProviderActions {
	if application.Provider.Actions == nil {
		application.Provider.Actions = &model.ProviderActions{}
	}
	return application.Provider.Actions
}

type packageScanResult struct {
	Discoveries []discovery
	Complete    map[string]bool
	Errors      map[string]error
}

type ecosystemScanResult struct {
	Discoveries []discovery
	Complete    bool
	Err         error
}

const (
	packageEcosystemPython = "python"
	packageEcosystemNode   = "node"
	packageEcosystemGo     = "go"
	packageEcosystemUV     = "uv"
	packageEcosystemRuby   = "ruby"
)

func scanPackages(ctx context.Context, settings model.PackageScanSettings, runner runtimeutil.Runner, exclusions exclusionMatcher, configured []model.Application, progress func(string, string)) packageScanResult {
	result := packageScanResult{Complete: make(map[string]bool), Errors: make(map[string]error)}
	appendResult := func(ecosystem string, scanned ecosystemScanResult) {
		result.Discoveries = append(result.Discoveries, scanned.Discoveries...)
		result.Complete[ecosystem] = scanned.Complete
		if scanned.Err != nil {
			result.Errors[ecosystem] = scanned.Err
		}
	}
	type configuredHandler struct {
		enabled   bool
		ecosystem string
		label     string
		handler   handler.Handler
	}
	handlers := []configuredHandler{
		{settings.Python, packageEcosystemPython, "Python", handler.NewPython(runner)},
		{settings.Node, packageEcosystemNode, "Node.js", handler.NewNode(runner)},
		{settings.Go, packageEcosystemGo, "Go", handler.NewGo(runner)},
		{settings.UV, packageEcosystemUV, "uv", handler.NewUV(runner)},
		{settings.Ruby, packageEcosystemRuby, "Ruby", handler.NewRuby(runner)},
	}
	for _, item := range handlers {
		if !item.enabled || ctx.Err() != nil {
			continue
		}
		reportPackageManager(progress, item.label)
		appendResult(item.ecosystem, packageHandlerResult(
			item.handler.Scan(ctx, packageHandlerRequest(configured, progress)), exclusions,
		))
	}
	return result
}

func packageHandlerRequest(configured []model.Application, progress func(string, string)) handler.Request {
	return handler.Request{Configured: configured, Report: func(value handler.Progress) { reportPackageStep(progress, value.Stage, value.Subject) }}
}

func packageHandlerResult(scanned handler.Result, exclusions exclusionMatcher) ecosystemScanResult {
	converted := ecosystemScanResult{Complete: scanned.Complete, Err: scanned.Err}
	for _, candidate := range scanned.Candidates {
		if exclusions.excluded(candidate.Application, candidate.Aliases...) {
			continue
		}
		converted.Discoveries = append(converted.Discoveries, packageDiscovery(candidate.Application, candidate.CurrentVersion))
	}
	return converted
}

func reportPackageManager(progress func(string, string), manager string) {
	if progress != nil {
		progress(model.ScanStagePackageManager, manager)
	}
}

func reportPackageStep(progress func(string, string), stage, subject string) {
	if progress != nil {
		progress(stage, subject)
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func packageDiscovery(app model.Application, current string) discovery {
	return discovery{App: app, State: model.ManagedStatus{CurrentVersion: version.Normalize(current), UpdateStatus: model.StatusUnchecked}}
}

type exclusionMatcher struct{ patterns []*regexp.Regexp }

func newExclusionMatcher(values []string) exclusionMatcher {
	matcher := exclusionMatcher{}
	for _, value := range values {
		expression := regexp.QuoteMeta(strings.ToLower(strings.TrimSpace(value)))
		expression = strings.ReplaceAll(expression, `\*`, `.*`)
		expression = strings.ReplaceAll(expression, `\?`, `.`)
		if compiled, err := regexp.Compile("^" + expression + "$"); err == nil {
			matcher.patterns = append(matcher.patterns, compiled)
		}
	}
	return matcher
}

func (m exclusionMatcher) excluded(app model.Application, aliases ...string) bool {
	identity := inferIdentity(app)
	candidates := []string{app.ID, app.Name, app.InstallPath, app.Identity, identity, app.Package}
	identity = strings.ToLower(identity)
	if strings.HasPrefix(identity, "app:") {
		candidates = append(candidates, "bundle:"+strings.TrimPrefix(identity, "app:"))
	}
	if strings.HasPrefix(identity, "package:") {
		parts := strings.SplitN(identity, ":", 3)
		if len(parts) == 3 {
			candidates = append(candidates, parts[1]+":"+parts[2])
		}
	}
	candidates = append(candidates, aliases...)
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		for _, pattern := range m.patterns {
			if candidate != "" && pattern.MatchString(candidate) {
				return true
			}
		}
	}
	return false
}

// ExcludedConfiguredApps returns existing catalog entries matched by the
// configured exclusion rules. The caller decides whether removal is approved.
func ExcludedConfiguredApps(catalog model.Config) []model.Application {
	matcher := newExclusionMatcher(catalog.Settings.Scan.Exclude)
	matched := make([]model.Application, 0)
	for _, app := range catalog.Apps {
		if matcher.excluded(app) {
			matched = append(matched, app)
		}
	}
	return matched
}

func canonicalPath(value string) string {
	value = installedPathValue(value)
	if absolute, err := filepath.Abs(value); err == nil {
		value = absolute
	}
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = resolved
	}
	return strings.ToLower(filepath.Clean(value))
}

func normalizePackage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}

type builtInPathDefinitionIndex struct {
	byID      map[string]builtin.PathDefinition
	byPackage map[string]builtin.PathDefinition
}

var canonicalBuiltInPathDefinitions = func() builtInPathDefinitionIndex {
	index := builtInPathDefinitionIndex{byID: make(map[string]builtin.PathDefinition), byPackage: make(map[string]builtin.PathDefinition)}
	for _, item := range builtin.PathDefinitions() {
		index.byID[strings.ToLower(item.ID)] = item
		if key := builtInPackageKey(item.Provider, item.Package); key != "" {
			index.byPackage[key] = item
		}
	}
	return index
}()

func builtInPackageKey(provider model.ProviderType, packageName string) string {
	packageName = normalizePackage(packageName)
	if provider == "" || packageName == "" {
		return ""
	}
	return strings.ToLower(string(provider)) + ":" + packageName
}

func inferIdentity(app model.Application) string {
	if app.Identity != "" {
		return strings.ToLower(app.Identity)
	}
	if app.Type == model.ApplicationTypeBundle {
		return "app-path:" + canonicalPath(app.InstallPath)
	}
	if app.Package != "" {
		switch app.Provider.Type {
		case model.ProviderPyPI:
			return model.PackageIdentity("python", app.Package)
		case model.ProviderNPM:
			return model.PackageIdentity("node", app.Package)
		case model.ProviderUV:
			return model.PackageIdentity("uv", app.Package)
		case model.ProviderGo:
			if strings.Contains(app.Package, ".") || strings.Contains(app.Package, "/") {
				return model.PackageIdentity("go", app.Package)
			}
		}
	}
	return "cli:" + model.NormalizeIdentityName(app.Name)
}

func matchingBuiltInPathDefinition(app model.Application) (builtin.PathDefinition, bool) {
	if app.Type != model.ApplicationTypeCLI && app.Type != model.ApplicationTypePackage {
		return builtin.PathDefinition{}, false
	}
	if app.Type == model.ApplicationTypeCLI {
		if item, ok := canonicalBuiltInPathDefinitions.byID[strings.ToLower(app.ID)]; ok {
			return item, true
		}
	}
	if item, ok := canonicalBuiltInPathDefinitions.byPackage[builtInPackageKey(app.Provider.Type, app.Package)]; ok {
		return item, true
	}
	return builtin.PathDefinition{}, false
}

func deduplicationKey(app model.Application, activeBuiltInCLIs map[string]bool) string {
	if item, ok := matchingBuiltInPathDefinition(app); ok && activeBuiltInCLIs[item.ID] {
		return "builtin-path:" + item.ID
	}
	key := inferIdentity(app)
	if app.InstallPath != "" && !strings.HasPrefix(key, "package:") {
		return "path:" + canonicalPath(app.InstallPath)
	}
	return key
}

// GenerateIdentity returns the stable identity produced by this scanner's
// discovery rules, falling back to the inferred path identity when unmatched.
func (s Scanner) GenerateIdentity(ctx context.Context, app model.Application) (string, error) {
	app.Identity = ""
	if app.Type == model.ApplicationTypeBundle {
		candidate, matched, err := handler.NewMacApp(builtin.MacAppDefinitions(), s.bundleIDs).ScanApplication(ctx, app, handler.Request{})
		if err != nil {
			return "", err
		}
		if matched {
			return candidate.Application.Identity, nil
		}
	}
	return inferIdentity(app), nil
}

type existingIndex struct {
	byIdentity map[string]string
	byPath     map[string]string
	byName     map[string]string
	apps       map[string]model.Application
}

func indexApps(apps []model.Application) existingIndex {
	index := existingIndex{byIdentity: map[string]string{}, byPath: map[string]string{}, byName: map[string]string{}, apps: map[string]model.Application{}}
	for _, app := range apps {
		index.apps[app.ID] = app
		identity := inferIdentity(app)
		if previous, exists := index.byIdentity[identity]; exists && previous != app.ID {
			// Normalization collisions (for example foo.bar and foobar) are
			// ambiguous package identities. Never silently pick the later app.
			index.byIdentity[identity] = ""
		} else if !exists {
			index.byIdentity[identity] = app.ID
		}
		if app.InstallPath != "" {
			index.byPath[canonicalPath(app.InstallPath)] = app.ID
		}
		index.byName[strings.ToLower(strings.TrimSpace(app.Name))] = app.ID
	}
	return index
}

func (i existingIndex) match(app model.Application) string {
	identity := inferIdentity(app)
	if id, exists := i.byIdentity[identity]; exists {
		if id != "" {
			return id
		}
		if strings.HasPrefix(identity, "package:") {
			return ""
		}
	}
	if app.InstallPath != "" && !strings.HasPrefix(identity, "package:") {
		if id := i.byPath[canonicalPath(app.InstallPath)]; id != "" {
			return id
		}
	}
	id := i.byName[strings.ToLower(strings.TrimSpace(app.Name))]
	configured := i.apps[id]
	sameType := configured.Type == app.Type
	if sameType && app.Type == model.ApplicationTypePackage && configured.Provider.Type != app.Provider.Type {
		sameType = false
	}
	if configured.ID != "" && (sameType ||
		(configured.Provider.Type == app.Provider.Type && configured.Package != "" && configured.Package == app.Package)) {
		return id
	}
	return ""
}

func deduplicateCatalog(apps []model.Application, state model.RuntimeState) ([]model.Application, model.RuntimeState) {
	apps = cloneApplications(apps)
	state = cloneRuntimeState(state)
	type candidate struct {
		app   model.Application
		order int
	}
	groups := map[string][]candidate{}
	keys := make([]string, 0)
	activeBuiltInCLIs := make(map[string]bool)
	for _, app := range apps {
		if app.Type == model.ApplicationTypeCLI {
			if item, ok := matchingBuiltInPathDefinition(app); ok {
				activeBuiltInCLIs[item.ID] = true
			}
		}
	}
	for index, app := range apps {
		key := deduplicationKey(app, activeBuiltInCLIs)
		if strings.HasPrefix(key, "package:") {
			if existing := groups[key]; len(existing) > 0 && packageIdentityCollision(existing[0].app, app) {
				// A lossy normalized package identity is ambiguous. Keep the
				// candidate visible, but clear its identity so it cannot be
				// silently merged or later persisted as an invalid duplicate.
				app.Identity = ""
				key = "ambiguous-package:" + app.ID
			}
		}
		if _, exists := groups[key]; !exists {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], candidate{app: app, order: index})
	}
	sort.Slice(keys, func(i, j int) bool { return groups[keys[i]][0].order < groups[keys[j]][0].order })
	result := make([]model.Application, 0, len(groups))
	if state.Observations == nil {
		state.Observations = map[string]model.ScanObservation{}
	}
	for _, key := range keys {
		items := groups[key]
		winner := items[0]
		for _, item := range items[1:] {
			if candidatePreferred(item.app, winner.app) {
				winner = item
			}
		}
		winnerObservation := state.Observations[winner.app.ID]
		for _, item := range items {
			if item.app.ID == winner.app.ID {
				continue
			}
			winner.app = mergeDuplicateApplication(winner.app, item.app)
			winner.app.StatusManaged = mergeApplicationState(winner.app.StatusManaged, item.app.StatusManaged)
			loserObservation := state.Observations[item.app.ID]
			winnerObservation.Found = winnerObservation.Found || loserObservation.Found
			if winnerObservation.Path == "" {
				winnerObservation.Path = loserObservation.Path
			}
			delete(state.Observations, item.app.ID)
		}
		state.Observations[winner.app.ID] = winnerObservation
		result = append(result, winner.app)
	}
	return result, state
}

func packageIdentityCollision(first, second model.Application) bool {
	return inferIdentity(first) == inferIdentity(second) && !strings.EqualFold(strings.TrimSpace(first.Package), strings.TrimSpace(second.Package))
}

func mergeDuplicateMetadata(primary, secondary model.Application) model.Application {
	if strings.TrimSpace(primary.Description) == "" {
		primary.Description = secondary.Description
	}
	if strings.TrimSpace(primary.URL) == "" {
		primary.URL = secondary.URL
	}
	return primary
}

type candidatePriority struct {
	protected       bool
	sourceRank      int
	capabilityScore int
}

func priorityForCandidate(app model.Application) candidatePriority {
	priority := candidatePriority{protected: !app.ScanManaged}
	if _, ok := matchingBuiltInPathDefinition(app); ok && app.Type == model.ApplicationTypeCLI {
		priority.sourceRank = 30
	} else {
		switch app.Type {
		case model.ApplicationTypeBundle:
			priority.sourceRank = 20
		case model.ApplicationTypePackage:
			priority.sourceRank = 10
		}
	}
	priority.capabilityScore = appCapabilityScore(app)
	return priority
}

func candidatePreferred(candidate, current model.Application) bool {
	left, right := priorityForCandidate(candidate), priorityForCandidate(current)
	if left.protected != right.protected {
		return left.protected
	}
	if left.sourceRank != right.sourceRank {
		return left.sourceRank > right.sourceRank
	}
	return left.capabilityScore > right.capabilityScore
}

func appCapabilityScore(app model.Application) int {
	score := 0
	if app.Provider.Type != model.ProviderDefault {
		score += 20
	}
	if app.UpdateMode != model.ModeCheck {
		score += 10
	}
	if app.Package != "" {
		score += 5
	}
	if app.Provider.VersionAction() != "" || app.Provider.CheckAction() != "" || app.Provider.UpdateAction() != "" {
		score += 5
	}
	return score
}

func mergeDuplicateApplication(primary, secondary model.Application) model.Application {
	if !primary.ScanManaged {
		return primary
	}
	primary = mergeDuplicateMetadata(primary, secondary)
	if !compatibleBuiltInCapabilities(primary, secondary) {
		return primary
	}
	if primary.Provider.CheckAction() == "" && secondary.Provider.CheckAction() != "" {
		actionConfig(&primary).Check = secondary.Provider.CheckAction()
	}
	if primary.Provider.UpdateAction() == "" && secondary.Provider.UpdateAction() != "" {
		actionConfig(&primary).Update = secondary.Provider.UpdateAction()
	}
	if primary.Provider.DownloadAction() == nil && secondary.Provider.DownloadAction() != nil {
		actionConfig(&primary).Download = cloneApplication(secondary).Provider.DownloadAction()
	}
	if primary.UpdateMode == model.ModeCheck {
		if primary.Provider.UpdateAction() != "" && secondary.UpdateMode == model.ModeAuto {
			primary.UpdateMode = model.ModeAuto
		} else if primary.Provider.DownloadAction() != nil && secondary.UpdateMode == model.ModeDownload {
			primary.UpdateMode = model.ModeDownload
		}
	}
	return primary
}

func compatibleBuiltInCapabilities(primary, secondary model.Application) bool {
	primaryDefinition, primaryOK := matchingBuiltInPathDefinition(primary)
	secondaryDefinition, secondaryOK := matchingBuiltInPathDefinition(secondary)
	return primaryOK && secondaryOK && primaryDefinition.ID == secondaryDefinition.ID && primary.Provider.Type == secondary.Provider.Type && normalizePackage(primary.Package) == normalizePackage(secondary.Package)
}

func mergeApplicationState(primary, secondary model.ManagedStatus) model.ManagedStatus {
	if primary.CurrentVersion == "" {
		primary.CurrentVersion = secondary.CurrentVersion
	}
	if primary.LatestVersion == "" {
		primary.LatestVersion = secondary.LatestVersion
	}
	if secondary.FirstDetectedTime != "" && (primary.FirstDetectedTime == "" || secondary.FirstDetectedTime < primary.FirstDetectedTime) {
		primary.FirstDetectedTime = secondary.FirstDetectedTime
	}
	if primary.Error == "" {
		primary.Error = secondary.Error
	}
	primary.HasUpdate = primary.HasUpdate || secondary.HasUpdate
	return primary
}
