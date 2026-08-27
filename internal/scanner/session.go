package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	"github.com/eoctet/tendkit/internal/scanner/handler"
	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// add applies stable catalog identity and exclusion policy before recording a
// discovery in the session snapshot.
func (session *scanSession) add(found discovery) {
	if existingID := session.index.match(found.App); existingID != "" && !found.StableID {
		found.App.ID = existingID
		configured := session.index.apps[existingID]
		if configured.ID != "" && configured.Type != found.App.Type {
			found.App.InstallPath = configured.InstallPath
		}
		if configured.ID != "" && (!configured.ScanManaged || session.exclusions.excluded(configured) || session.exclusions.excluded(found.App)) {
			return
		}
	}
	if found.StableID {
		configured := session.index.apps[found.App.ID]
		if configured.ID != "" && (!configured.ScanManaged || session.exclusions.excluded(configured) || session.exclusions.excluded(found.App)) {
			return
		}
		if legacyID := session.index.match(found.App); legacyID != "" && legacyID != found.App.ID {
			legacy := session.index.apps[legacyID]
			if !legacy.ScanManaged || session.exclusions.excluded(legacy) {
				return
			}
		}
	}
	if session.exclusions.excluded(found.App) {
		return
	}
	session.discovered = append(session.discovered, found.App)
	session.observed[found.App.ID] = found.State
}

// canonicalEvidencePath fails closed when ownership evidence cannot be reduced
// to an existing canonical filesystem path.
func canonicalEvidencePath(value string) (string, bool) {
	value = installedPathValue(value)
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	return canonicalComparablePath(real), true
}

func canonicalComparablePath(value string) string {
	value = filepath.Clean(value)
	// Windows filesystems commonly use case-insensitive path semantics. Do not
	// lower-case POSIX paths: that would collapse distinct installations.
	if runtimeutil.HostPlatform().Kernel == "windows" {
		return strings.ToLower(value)
	}
	return value
}

func (session *scanSession) discoverPath(ctx context.Context) error {
	if !session.catalog.Settings.Scan.Path {
		return nil
	}
	session.scanner.report(model.ScanStagePath, "")
	result := handler.NewPath(session.scanner.Runner, builtin.PathDefinitions()).Scan(ctx, handler.Request{
		Report:     func(progress handler.Progress) { session.scanner.report(progress.Stage, progress.Subject) },
		Diagnostic: func(diagnostic handler.Diagnostic) { session.scanner.reportDiagnostic(Diagnostic(diagnostic)) },
	})
	grouped := make(map[string][]handler.Candidate)
	for _, candidate := range result.Candidates {
		grouped[candidate.Application.ID] = append(grouped[candidate.Application.ID], candidate)
	}
	assigned := make([]handler.Candidate, 0, len(result.Candidates))
	for _, definition := range builtin.PathDefinitions() {
		candidates := grouped[handler.PathApplicationID(definition.ID)]
		if len(candidates) == 0 {
			continue
		}
		assignment, err := assignPathInstances(definition.ID, candidates, session.catalog.Apps)
		if err != nil {
			return err
		}
		session.applyPathInstanceMigrations(assignment)
		assigned = append(assigned, assignment.Candidates...)
	}
	session.index = indexApps(session.catalog.Apps)
	for _, candidate := range assigned {
		resolved, err := session.scanner.resolveGitHub(ctx, candidate.Application)
		if err != nil {
			return err
		}
		candidate.Application = resolved
		found := session.scanner.pathDiscovery(candidate)
		found.StableID = true
		session.add(found)
	}
	return result.Err
}

func (session *scanSession) applyPathInstanceMigrations(assignment pathInstanceAssignment) {
	if len(assignment.Migrations) == 0 && len(assignment.IdentityMigrations) == 0 {
		return
	}
	for index := range session.catalog.Apps {
		oldID := session.catalog.Apps[index].ID
		_, identityMigrated := assignment.IdentityMigrations[oldID]
		if identity, ok := assignment.IdentityMigrations[oldID]; ok {
			session.catalog.Apps[index].Identity = identity
		}
		newID, ok := assignment.Migrations[oldID]
		if !ok {
			continue
		}
		session.catalog.Apps[index].ID = newID
		if observation, exists := session.state.Observations[oldID]; exists {
			delete(session.state.Observations, oldID)
			session.state.Observations[newID] = observation
		}
		if keep, exists := session.catalog.ScanVersionControl[oldID]; exists {
			delete(session.catalog.ScanVersionControl, oldID)
			session.catalog.ScanVersionControl[newID] = keep
		}
		if !identityMigrated && !installedPath(session.catalog.Apps[index].InstallPath) {
			if session.observed == nil {
				session.observed = map[string]model.ManagedStatus{}
			}
			missing := session.catalog.Apps[index].StatusManaged
			missing.UpdateStatus = model.StatusMissing
			session.observed[newID] = missing
		}
	}
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

// discoverPackages defers ownership-backed candidates until every ecosystem's
// completeness is known and reconciliation can evaluate them atomically.
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
		if found.Evidence != nil {
			session.installationDiscoveries = append(session.installationDiscoveries, found)
		} else {
			session.add(found)
		}
	}
	ecosystems := make([]string, 0, len(packageScan.Errors))
	for ecosystem := range packageScan.Errors {
		ecosystems = append(ecosystems, ecosystem)
	}
	sort.Strings(ecosystems)
	for _, ecosystem := range ecosystems {
		scanErr := packageScan.Errors[ecosystem]
		session.scanner.reportDiagnostic(Diagnostic{Event: "package_incomplete", Subject: ecosystem, Detail: scanErr.Error(), Err: scanErr})
	}
	session.packages = packageScan
	return nil
}

// observeConfiguredApplications reconciles ownership first, then observes only
// catalog entries not already covered by discovery or retention rules.
func (session *scanSession) observeConfiguredApplications(ctx context.Context) error {
	session.reconcileManagedInstallations()
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

func (session *scanSession) addIndependentPackage(found discovery) {
	if session.exclusions.excluded(found.App) {
		return
	}
	session.discovered = append(session.discovered, found.App)
	session.observed[found.App.ID] = found.State
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

// retainIncompleteManagedPackage preserves the last stable observation when an
// ecosystem cannot provide a complete inventory; absence is not evidence of removal.
func (session *scanSession) retainIncompleteManagedPackage(application model.Application) bool {
	if session.exclusions.excluded(application) || !application.ScanManaged {
		return false
	}
	ecosystem := managedPackageEcosystem(application)
	if ecosystem == "" {
		return false
	}
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

// finalize publishes merged applications and observations as one coherent snapshot.
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
	// A reconciliation can absorb a transient package ID into a stable PATH or
	// bundle ID. Without cleanup, keep records and observations for removed IDs
	// would survive as orphaned scan state.
	known := make(map[string]bool, len(session.catalog.Apps))
	for _, application := range session.catalog.Apps {
		known[application.ID] = true
	}
	for id := range session.state.Observations {
		if !known[id] {
			delete(session.state.Observations, id)
		}
	}
	for id := range session.catalog.ScanVersionControl {
		if !known[id] {
			delete(session.catalog.ScanVersionControl, id)
		}
	}
	return session.catalog, session.state, nil
}
