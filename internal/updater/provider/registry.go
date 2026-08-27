package provider

import (
	"sort"
	"strings"
	"sync"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func DetectCapabilities(implementation any) Capabilities {
	return Capabilities{currentCapability(implementation), latestCapability(implementation), updateCapability(implementation), downloadCapability(implementation), installCapability(implementation), checksumCapability(implementation), artifactCapability(implementation)}
}
func currentCapability(v any) CurrentVersioner  { result, _ := v.(CurrentVersioner); return result }
func latestCapability(v any) LatestVersioner    { result, _ := v.(LatestVersioner); return result }
func updateCapability(v any) UpdateExecutor     { result, _ := v.(UpdateExecutor); return result }
func downloadCapability(v any) DownloadResolver { result, _ := v.(DownloadResolver); return result }
func installCapability(v any) InstallExecutor   { result, _ := v.(InstallExecutor); return result }
func checksumCapability(v any) Checksummer      { result, _ := v.(Checksummer); return result }
func artifactCapability(v any) ArtifactProvider { result, _ := v.(ArtifactProvider); return result }

func CapabilityUnavailable(providerName string, capability Capability) error {
	return &Error{Key: "provider.unavailable", Provider: normalizeName(providerName), Capability: capability, Cause: ErrUnavailable}
}

type Registry struct {
	mu        sync.RWMutex
	providers map[string]Capabilities
}

func NewRegistry() *Registry           { return &Registry{providers: map[string]Capabilities{}} }
func normalizeName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }
func (r *Registry) Register(name string, implementation any) error {
	if implementation == nil {
		return NewError("provider.impl_nil", normalizeName(name))
	}
	capabilities := DetectCapabilities(implementation)
	if capabilities == (Capabilities{}) {
		return NewError("provider.impl_nil", normalizeName(name))
	}
	return r.RegisterCapabilities(name, capabilities)
}
func (r *Registry) RegisterCapabilities(name string, capabilities Capabilities) error {
	name = normalizeName(name)
	if name == "" {
		return NewError("provider.name_empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = map[string]Capabilities{}
	}
	if _, exists := r.providers[name]; exists {
		return NewError("provider.duplicate", name)
	}
	r.providers[name] = capabilities
	return nil
}
func (r *Registry) Resolve(name string) (Capabilities, bool) {
	if r == nil {
		return Capabilities{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.providers[normalizeName(name)]
	return value, ok
}
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RegisterBuiltins registers only capabilities backed by existing safe behavior.
func RegisterBuiltins(registry *Registry, source *HTTPSource, endpoints map[string]string, runners ...runtimeutil.Runner) error {
	if registry == nil {
		return NewError("provider.registry_nil")
	}
	if source == nil {
		source = NewHTTPSource(nil)
	}
	runner := runtimeutil.Runner{}
	if len(runners) > 0 {
		runner = runners[0]
	}
	localCurrent := localMetadataProvider{runner: runner}
	packageUpdate := packageUpdateProvider{runner: runner}
	builtins := []struct {
		name           string
		implementation any
	}{
		{string(model.ProviderGitHubRelease), GitHubReleaseProvider{Source: source, Endpoint: endpoints[string(model.ProviderGitHubRelease)]}}, {string(model.ProviderGitHubTag), GitHubTagProvider{Source: source, Endpoint: endpoints[string(model.ProviderGitHubTag)]}}, {string(model.ProviderNPM), NPMProvider{Source: source, Endpoint: endpoints[string(model.ProviderNPM)]}}, {string(model.ProviderPyPI), PyPIProvider{Source: source, Endpoint: endpoints[string(model.ProviderPyPI)]}}, {string(model.ProviderUV), UVProvider{Runner: runner}}, {string(model.ProviderJetBrains), JetBrainsProvider{Source: source, Endpoint: endpoints[string(model.ProviderJetBrains)]}}, {string(model.ProviderGo), GoProvider{Source: source, Endpoint: endpoints[string(model.ProviderGo)], Runner: runner}}, {string(model.ProviderNodeLTS), NodeLTSProvider{Source: source, Endpoint: endpoints[string(model.ProviderNodeLTS)]}}, {string(model.ProviderSparkle), SparkleProvider{Source: source, Runner: runner}}, {string(model.ProviderHomebrew), HomebrewProvider{Runner: runner}}, {string(model.ProviderCargo), CargoProvider{Runner: runner}},
	}
	for _, builtin := range builtins {
		capabilities := DetectCapabilities(builtin.implementation)
		if capabilities.Current == nil {
			capabilities.Current = localCurrent
		}
		switch model.ProviderType(builtin.name) {
		case model.ProviderNPM, model.ProviderPyPI, model.ProviderUV, model.ProviderGo:
			capabilities.Update = packageUpdate
		}
		if err := registry.RegisterCapabilities(builtin.name, capabilities); err != nil {
			return err
		}
	}
	return nil
}
