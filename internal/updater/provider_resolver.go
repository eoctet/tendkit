package updater

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	providerpkg "github.com/eoctet/tendkit/internal/updater/provider"
	httpx "github.com/eoctet/tendkit/pkg/http"
	"github.com/eoctet/tendkit/pkg/i18n"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// providerResolver coordinates registered and action-backed provider capabilities.
type providerResolver struct {
	registry    *providerpkg.Registry
	source      *providerpkg.HTTPSource
	runner      runtimeutil.Runner
	downloadDir string
}

// currentResolution records how the facade determined the current version.
type currentResolution struct {
	Version    string
	Duration   time.Duration
	FromAction bool
}

// downloadResolution is the provider facade's resolved input for downloader.
// ChecksumErr is non-fatal when an explicit artifact URL remains usable.
type downloadResolution struct {
	Spec        model.Download
	Artifact    string
	ChecksumErr error
}

// newProviderResolver constructs a resolver with all built-in providers registered.
func newProviderResolver(commandRunner runtimeutil.Runner, endpoints map[string]string, settings *model.HTTPSettings) (*providerResolver, error) {
	registry := providerpkg.NewRegistry()
	if settings == nil {
		settings = &model.HTTPSettings{}
	}
	if settings.TimeoutSeconds == 0 && settings.MaxConcurrencyPerHost == 0 && settings.Retries == 0 {
		settings = &model.HTTPSettings{TimeoutSeconds: model.DefaultHTTPTimeoutSeconds, MaxConcurrencyPerHost: model.DefaultHTTPMaxConcurrencyPerHost, Retries: model.DefaultHTTPRetries}
	}
	source := providerpkg.NewHTTPSource(nil, httpx.HTTPOptions{Timeout: time.Duration(settings.TimeoutSeconds) * time.Second, MaxConcurrencyPerHost: settings.MaxConcurrencyPerHost, Retries: settings.Retries})
	if err := providerpkg.RegisterBuiltins(registry, source, endpoints, commandRunner); err != nil {
		return nil, err
	}
	return &providerResolver{registry: registry, source: source, runner: commandRunner}, nil
}

// newProviderResolverWithRegistry constructs a resolver around an injected registry.
func newProviderResolverWithRegistry(registry *providerpkg.Registry) *providerResolver {
	if registry == nil {
		registry = providerpkg.NewRegistry()
	}
	return &providerResolver{registry: registry}
}

// httpSource returns the shared provider HTTP source.
func (c *providerResolver) httpSource() *providerpkg.HTTPSource {
	if c == nil {
		return nil
	}
	return c.source
}

// providerNames returns the sorted registered provider names.
func (c *providerResolver) providerNames() []string {
	if c == nil || c.registry == nil {
		return nil
	}
	return c.registry.Names()
}

// downloadAssetCandidates returns provider-approved download choices and their
// explicit-selection requirement for providers that support artifact selection.
func (c *providerResolver) downloadAssetCandidates(ctx context.Context, app model.Application) (model.DownloadAssetChoices, error) {
	if app.Provider.Type != model.ProviderGitHubRelease && app.Provider.Type != model.ProviderGo {
		return model.DownloadAssetChoices{}, nil
	}
	capabilities, ok := c.registry.Resolve(string(app.Provider.Type))
	if !ok {
		return model.DownloadAssetChoices{}, errors.New(i18n.T("provider.unsupported", app.Provider.Type))
	}
	if choices, ok := capabilities.Download.(providerpkg.ArtifactChoiceProvider); ok {
		return choices.ArtifactChoices(ctx, providerpkg.Request{App: app})
	}
	candidates, ok := capabilities.Download.(providerpkg.ArtifactCandidateProvider)
	if !ok {
		return model.DownloadAssetChoices{}, localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), providerpkg.CapabilityArtifact))
	}
	values, err := candidates.ArtifactCandidates(ctx, providerpkg.Request{App: app})
	return model.DownloadAssetChoices{Candidates: values}, err
}

// setDownloadDir configures the download_dir action placeholder.
func (c *providerResolver) setDownloadDir(downloadDir string) {
	if c != nil {
		c.downloadDir = downloadDir
	}
}

// latest resolves the latest available application version.
func (c *providerResolver) latest(ctx context.Context, app model.Application, current string) (string, error) {
	if c == nil || c.registry == nil {
		return "", errors.New(i18n.T("provider.registry_uninitialized"))
	}
	state := app.StatusManaged
	state.CurrentVersion = current
	request := providerpkg.Request{App: app, CurrentVersion: current, Values: placeholders(app, state, c.downloadDir)}
	if actions := providerpkg.ActionCapabilities(c.runner, request, app.Provider.Type == model.ProviderDefault); actions.Latest != nil {
		latest, err := actions.Latest.Latest(ctx, request)
		return latest, localizeProviderError(err)
	}
	capabilities, ok := c.registry.Resolve(string(app.Provider.Type))
	if !ok {
		if app.Provider.Type == model.ProviderDefault {
			return "", localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), providerpkg.CapabilityLatest))
		}
		return "", errors.New(i18n.T("provider.unsupported", app.Provider.Type))
	}
	if capabilities.Latest == nil {
		return "", localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), providerpkg.CapabilityLatest))
	}
	latest, err := capabilities.Latest.Latest(ctx, providerpkg.Request{App: app, CurrentVersion: current})
	return latest, localizeProviderError(err)
}

// current resolves the installed application version and its action timing.
func (c *providerResolver) current(ctx context.Context, app model.Application, fallback string) (currentResolution, error) {
	if c == nil || c.registry == nil {
		return currentResolution{}, errors.New(i18n.T("provider.registry_uninitialized"))
	}
	if strings.TrimSpace(app.Provider.VersionAction()) != "" {
		state := app.StatusManaged
		state.CurrentVersion = fallback
		request := providerpkg.Request{App: app, CurrentVersion: fallback, Values: placeholders(app, state, c.downloadDir)}
		started := time.Now()
		version, err := providerpkg.ActionCapabilities(c.runner, request, app.Provider.Type == model.ProviderDefault).Current.Current(ctx, request)
		return currentResolution{Version: version, Duration: time.Since(started), FromAction: true}, localizeProviderError(err)
	}
	if app.Provider.Type == model.ProviderDefault {
		return currentResolution{Version: fallback}, localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), providerpkg.CapabilityCurrent))
	}
	capabilities, ok := c.registry.Resolve(string(app.Provider.Type))
	if !ok {
		if app.Provider.Type != model.ProviderDefault {
			return currentResolution{}, errors.New(i18n.T("provider.unsupported", app.Provider.Type))
		}
		capabilities = providerpkg.Capabilities{}
	}
	var currentErr error
	if capabilities.Current != nil {
		version, err := capabilities.Current.Current(ctx, providerpkg.Request{App: app, CurrentVersion: fallback})
		if err == nil {
			return currentResolution{Version: version}, nil
		}
		currentErr = err
	}
	if strings.HasSuffix(strings.ToLower(app.InstallPath), strings.ToLower(metadatautil.ApplicationExtension)) {
		if version, err := applicationBundleVersion(ctx, app.InstallPath); err == nil {
			return currentResolution{Version: version}, nil
		}
	}
	if currentErr != nil {
		return currentResolution{Version: fallback}, localizeProviderError(currentErr)
	}
	return currentResolution{Version: fallback}, nil
}

// executeUpdate invokes the configured or registered update capability.
func (c *providerResolver) executeUpdate(ctx context.Context, app model.Application, state model.ManagedStatus) (runtimeutil.Result, error) {
	return c.execute(ctx, app, state, providerpkg.CapabilityUpdate)
}

// executeInstall invokes the default provider's install capability.
func (c *providerResolver) executeInstall(ctx context.Context, app model.Application, state model.ManagedStatus) (runtimeutil.Result, error) {
	if app.Provider.Type != model.ProviderDefault {
		return runtimeutil.Result{}, localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), providerpkg.CapabilityInstall))
	}
	return c.execute(ctx, app, state, providerpkg.CapabilityInstall)
}
func (c *providerResolver) execute(ctx context.Context, app model.Application, state model.ManagedStatus, capability providerpkg.Capability) (runtimeutil.Result, error) {
	if c == nil || c.registry == nil {
		return runtimeutil.Result{}, errors.New(i18n.T("provider.registry_uninitialized"))
	}
	values := placeholders(app, state, c.downloadDir)
	request := providerpkg.Request{App: app, CurrentVersion: state.CurrentVersion, Values: values}
	actions := providerpkg.ActionCapabilities(c.runner, request, app.Provider.Type == model.ProviderDefault)
	started := time.Now()
	switch capability {
	case providerpkg.CapabilityUpdate:
		if actions.Update != nil {
			err := actions.Update.Update(ctx, request)
			return runtimeutil.Result{Duration: time.Since(started)}, localizeProviderError(err)
		}
	case providerpkg.CapabilityInstall:
		if actions.Install != nil {
			err := actions.Install.Install(ctx, request)
			return runtimeutil.Result{Duration: time.Since(started)}, localizeProviderError(err)
		}
	}
	capabilities, ok := c.registry.Resolve(string(app.Provider.Type))
	if !ok {
		if app.Provider.Type == model.ProviderDefault {
			return runtimeutil.Result{}, localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), capability))
		}
		return runtimeutil.Result{}, errors.New(i18n.T("provider.unsupported", app.Provider.Type))
	}
	switch capability {
	case providerpkg.CapabilityUpdate:
		if capabilities.Update != nil {
			return runtimeutil.Result{}, localizeProviderError(capabilities.Update.Update(ctx, request))
		}
	case providerpkg.CapabilityInstall:
		if capabilities.Install != nil {
			return runtimeutil.Result{}, localizeProviderError(capabilities.Install.Install(ctx, request))
		}
	}
	return runtimeutil.Result{}, localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), capability))
}

// resolveDownload resolves the executable download, checksum and artifact.
func (c *providerResolver) resolveDownload(ctx context.Context, app model.Application, state model.ManagedStatus, selected ...string) (downloadResolution, error) {
	if c == nil || c.registry == nil {
		return downloadResolution{}, errors.New(i18n.T("provider.registry_uninitialized"))
	}
	values := placeholders(app, state, c.downloadDir)
	selectedArtifact := ""
	if len(selected) > 0 {
		selectedArtifact = selected[0]
	}
	request := providerpkg.Request{App: app, CurrentVersion: state.LatestVersion, Values: values, SelectedArtifact: selectedArtifact}
	capabilities := providerpkg.Capabilities{}
	registered, ok := c.registry.Resolve(string(app.Provider.Type))
	if ok {
		capabilities = registered
	} else if app.Provider.Type != model.ProviderDefault {
		return downloadResolution{}, errors.New(i18n.T("provider.unsupported", app.Provider.Type))
	}
	var spec model.Download
	usedConfiguredAction := false
	if actionCapabilities := providerpkg.ActionCapabilities(c.runner, request, app.Provider.Type == model.ProviderDefault); actionCapabilities.Download != nil {
		// A configured download action replaces the builtin Download capability.
		var err error
		spec, err = actionCapabilities.Download.Download(ctx, request)
		if err != nil {
			return downloadResolution{}, localizeProviderError(err)
		}
		usedConfiguredAction = true
	} else if capabilities.Download != nil {
		var err error
		spec, err = capabilities.Download.Download(ctx, request)
		if err != nil {
			return downloadResolution{}, localizeProviderError(err)
		}
	} else {
		return downloadResolution{}, localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), providerpkg.CapabilityDownload))
	}
	if !usedConfiguredAction && spec.ChecksumEnabled && strings.TrimSpace(spec.ChecksumValue) == "" && strings.TrimSpace(spec.ChecksumURL) == "" {
		if capabilities.Checksum == nil {
			return downloadResolution{Spec: spec, ChecksumErr: localizeProviderError(providerpkg.CapabilityUnavailable(string(app.Provider.Type), providerpkg.CapabilityChecksum))}, nil
		}
		checksum, err := capabilities.Checksum.Checksum(ctx, request)
		if err != nil {
			return downloadResolution{Spec: spec, ChecksumErr: localizeProviderError(err)}, nil
		}
		spec.ChecksumValue = checksum
	}
	if !usedConfiguredAction && capabilities.Artifact != nil {
		artifact, err := capabilities.Artifact.Artifact(ctx, request)
		if err != nil {
			return downloadResolution{}, localizeProviderError(err)
		}
		return downloadResolution{Spec: spec, Artifact: artifact}, nil
	}
	return downloadResolution{Spec: spec}, nil
}
func localizeProviderError(err error) error {
	var typed *providerpkg.Error
	if errors.As(err, &typed) {
		return localizedProviderError{text: i18n.T(typed.Key, typed.Args...), cause: err}
	}
	return err
}

type localizedProviderError struct {
	text  string
	cause error
}

func (err localizedProviderError) Error() string { return err.text }
func (err localizedProviderError) Unwrap() error { return err.cause }

// placeholders returns the stable values exposed to configured provider actions.
func placeholders(app model.Application, state model.ManagedStatus, downloadDir string) map[string]string {
	return map[string]string{
		"id": app.ID, "name": app.Name, "app_name": app.Name, "install_path": app.InstallPath,
		"current_version": state.CurrentVersion, "latest_version": state.LatestVersion,
		"last_version": state.LatestVersion, "download_dir": downloadDir, "arch": runtimeutil.ActionArchitecture(),
	}
}

func applicationBundleVersion(ctx context.Context, appPath string) (string, error) {
	metadata, err := metadatautil.ReadMacApplicationMetadata(ctx, appPath)
	if err != nil {
		return "", err
	}
	if metadata.Version == "" {
		return "", errors.New("application bundle version is empty")
	}
	return metadata.Version, nil
}
