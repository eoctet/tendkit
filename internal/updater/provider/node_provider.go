package provider

import (
	"context"
	"net/url"
	"path"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type NodeLTSProvider struct {
	Source   *HTTPSource
	Endpoint string
	host     func() runtimeutil.SystemInfo
}

func (p NodeLTSProvider) systemInfo() runtimeutil.SystemInfo {
	if p.host != nil {
		return p.host()
	}
	return runtimeutil.HostPlatform()
}

func (p NodeLTSProvider) Latest(ctx context.Context, _ Request) (string, error) {
	release, err := p.latestLTS(ctx)
	if err != nil {
		return "", err
	}
	return normalizeAny(release.Version)
}

func (p NodeLTSProvider) Download(ctx context.Context, _ Request) (model.Download, error) {
	release, err := p.latestLTS(ctx)
	if err != nil {
		return model.Download{}, err
	}
	version, err := normalizeAny(release.Version)
	if err != nil {
		return model.Download{}, err
	}
	info := p.systemInfo()
	if !info.Supported() {
		return model.Download{}, NewError("provider.node_download_unavailable", info.Architecture)
	}
	platform := info.NodeArchivePlatform()
	arch, _ := info.NodeArchiveArchitecture()
	fileKey, _ := info.NodeReleaseFileKey()
	if !nodeReleaseHasFile(release.Files, fileKey) {
		return model.Download{}, NewError("provider.node_download_unavailable", info.Architecture)
	}
	endpoint, err := url.Parse(p.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return model.Download{}, NewError("provider.node_download_unavailable", arch)
	}
	endpoint.Path = path.Join(path.Dir(endpoint.Path), "v"+version, "node-v"+version+"-"+platform+"-"+arch+".tar.gz")
	endpoint.Scheme = "https"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	downloadURL, err := trustedDownloadURL(p.Endpoint, endpoint.String())
	if err != nil {
		return model.Download{}, err
	}
	filename := path.Base(endpoint.Path)
	return model.Download{URL: downloadURL, Filename: filename}, nil
}

type nodeRelease struct {
	Version string   `json:"version"`
	LTS     any      `json:"lts"`
	Files   []string `json:"files"`
}

func (p NodeLTSProvider) latestLTS(ctx context.Context) (nodeRelease, error) {
	var data []nodeRelease
	if err := p.Source.GetJSON(ctx, p.Endpoint, &data); err != nil {
		return nodeRelease{}, err
	}
	for _, release := range data {
		if release.LTS != nil && release.LTS != false && strings.TrimSpace(release.Version) != "" {
			return release, nil
		}
	}
	return nodeRelease{}, NewError("provider.node_empty")
}

func nodeReleaseHasFile(files []string, want string) bool {
	for _, file := range files {
		if file == want {
			return true
		}
	}
	return false
}
