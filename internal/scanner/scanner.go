package scanner

import (
	"context"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/handler"
	httpx "github.com/eoctet/tendkit/pkg/http"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
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

// New creates a scanner with a copy of the configured bundle-ID scan rules.
func New(settings model.ScanSettings) Scanner {
	return Scanner{bundleIDs: append([]string{}, settings.BundleID...)}
}

// discovery couples a catalog candidate with its transient observation and
// optional package-manager ownership evidence.
type discovery struct {
	App      model.Application
	State    model.ManagedStatus
	Evidence *handler.InstallationEvidence
}

// scanSession aggregates the catalog, runtime state, discoveries, and
// observations used while one scan is in progress.
type scanSession struct {
	scanner                 Scanner
	catalog                 model.Config
	state                   model.RuntimeState
	exclusions              exclusionMatcher
	index                   existingIndex
	discovered              []model.Application
	observed                map[string]model.ManagedStatus
	packages                packageScanResult
	installationDiscoveries []discovery
}

// Scan reconciles discoveries with catalog and state snapshots.
func (s Scanner) Scan(ctx context.Context, catalog model.Config, state model.RuntimeState) (model.Config, model.RuntimeState, error) {
	// Applications and runtime state are cloned for scan-time mutation. Other
	// Config reference fields retain their existing copy semantics.
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

// prepareScan normalizes the isolated baseline before any external discovery.
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

func (s Scanner) report(stage, subject string) {
	if s.Progress != nil {
		s.Progress(Progress{Stage: stage, Subject: subject})
	}
}

func packageScanningEnabled(settings model.PackageScanSettings) bool {
	return settings.Python || settings.Node || settings.Go ||
		settings.UV || settings.Ruby || settings.HomebrewFormula || settings.HomebrewCask || settings.Cargo
}
