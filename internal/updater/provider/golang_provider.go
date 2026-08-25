package provider

import (
	"context"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type GoProvider struct {
	Source   *HTTPSource
	Endpoint string
	Runner   runtimeutil.Runner
	host     func() runtimeutil.SystemInfo
}

func (p GoProvider) systemInfo() runtimeutil.SystemInfo {
	if p.host != nil {
		return p.host()
	}
	return runtimeutil.HostPlatform()
}

func (p GoProvider) Latest(ctx context.Context, request Request) (string, error) {
	if request.App.Type == model.ApplicationTypePackage {
		manager, err := metadatautil.FindPackageManager(metadatautil.PackageGo)
		if err != nil {
			return "", WrapError("provider.go_component_latest_failed", err, request.App.Name)
		}
		metadata, err := metadatautil.ReadGoComponentMetadata(ctx, p.Runner, manager, request.App.InstallPath, request.App.Environment)
		if err != nil {
			return "", WrapError("provider.go_component_latest_failed", err, request.App.Name)
		}
		result, err := p.Runner.Run(ctx, shellGoLatestCommand(manager, metadata.Module), request.App.Environment)
		if err != nil {
			return "", WrapError("provider.go_component_latest_failed", err, request.App.Name)
		}
		if result.ExitCode != 0 {
			return "", NewError("provider.go_component_latest_exit", request.App.Name, result.ExitCode, strings.TrimSpace(result.Combined()))
		}
		return normalizeAny(result.Combined())
	}
	release, err := p.latestStable(ctx)
	if err != nil {
		return "", err
	}
	return normalizeAny(strings.TrimPrefix(release.Version, "go"))
}

func shellGoLatestCommand(manager, module string) string {
	return runtimeutil.QuoteShell(manager) + " 'list' '-m' '-f' '{{.Version}}' " + runtimeutil.QuoteShell(module+"@latest")
}

func (p GoProvider) Download(ctx context.Context, request Request) (model.Download, error) {
	if request.App.Type == model.ApplicationTypePackage {
		return model.Download{}, CapabilityUnavailable(string(model.ProviderGo), CapabilityDownload)
	}
	release, err := p.latestStable(ctx)
	if err != nil {
		return model.Download{}, err
	}
	info := p.systemInfo()
	files := goHostFiles(release.Files, info)
	if selected := request.SelectedArtifact; selected != "" {
		for _, file := range files {
			if file.Filename == selected {
				return p.downloadForFile(file.Filename)
			}
		}
		return model.Download{}, NewError("provider.go_download_named_unavailable", selected)
	}
	var installers, archives []goFile
	for _, file := range files {
		if file.Kind == "installer" && strings.HasSuffix(file.Filename, ".pkg") {
			installers = append(installers, file)
		} else {
			archives = append(archives, file)
		}
	}
	if len(installers) == 1 {
		return p.downloadForFile(installers[0].Filename)
	}
	if len(installers) > 1 {
		return model.Download{}, NewError("provider.go_download_ambiguous", info.Architecture)
	}
	if len(archives) == 1 {
		return p.downloadForFile(archives[0].Filename)
	}
	if len(archives) > 1 {
		return model.Download{}, NewError("provider.go_download_ambiguous", info.Architecture)
	}
	return model.Download{}, NewError("provider.go_download_unavailable", info.Architecture)
}

// ArtifactCandidates returns every file from the latest stable release that
// is compatible with the current macOS host. Download performs the same
// filtering before accepting a one-run SelectedArtifact value.
func (p GoProvider) ArtifactCandidates(ctx context.Context, request Request) ([]string, error) {
	if request.App.Type == model.ApplicationTypePackage {
		return nil, nil
	}
	release, err := p.latestStable(ctx)
	if err != nil {
		return nil, err
	}
	files := goHostFiles(release.Files, p.systemInfo())
	candidates := make([]string, 0, len(files))
	for _, file := range files {
		candidates = append(candidates, file.Filename)
	}
	sort.Strings(candidates)
	return candidates, nil
}

func goHostFiles(files []goFile, info runtimeutil.SystemInfo) []goFile {
	compatible := make([]goFile, 0, len(files))
	platform, platformSupported := info.GoPlatform()
	architecture, architectureSupported := info.GoArchitecture()
	for _, file := range files {
		if platformSupported && architectureSupported && file.OS == platform && file.Arch == architecture && strings.TrimSpace(file.Filename) != "" {
			compatible = append(compatible, file)
		}
	}
	return compatible
}

func (p GoProvider) downloadForFile(filename string) (model.Download, error) {
	endpoint, err := url.Parse(p.Endpoint)
	if err != nil || endpoint.Host == "" {
		return model.Download{}, NewError("provider.go_download_endpoint_invalid")
	}
	endpoint.Scheme = "https"
	endpoint.Path = path.Join(path.Dir(endpoint.Path), filename)
	endpoint.RawQuery = ""
	downloadURL, err := trustedDownloadURL(p.Endpoint, endpoint.String())
	if err != nil {
		return model.Download{}, err
	}
	return model.Download{URL: downloadURL, Filename: filename}, nil
}

type goFile struct {
	Filename string `json:"filename"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Kind     string `json:"kind"`
}
type goRelease struct {
	Version string   `json:"version"`
	Stable  bool     `json:"stable"`
	Files   []goFile `json:"files"`
}

func (p GoProvider) latestStable(ctx context.Context) (goRelease, error) {
	var data []goRelease
	if err := p.Source.GetJSON(ctx, p.Endpoint, &data); err != nil {
		return goRelease{}, err
	}
	for _, release := range data {
		if release.Stable {
			return release, nil
		}
	}
	return goRelease{}, NewError("provider.go_empty")
}
