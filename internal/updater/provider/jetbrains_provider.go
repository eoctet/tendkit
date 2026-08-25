package provider

import (
	"context"
	"net/url"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type JetBrainsProvider struct {
	Source   *HTTPSource
	Endpoint string
	host     func() runtimeutil.SystemInfo
}

func (p JetBrainsProvider) systemInfo() runtimeutil.SystemInfo {
	if p.host != nil {
		return p.host()
	}
	return runtimeutil.HostPlatform()
}

type jetBrainsRelease struct {
	Version   string `json:"version"`
	Downloads map[string]struct {
		Link string `json:"link"`
	} `json:"downloads"`
}

func (p JetBrainsProvider) Download(ctx context.Context, request Request) (model.Download, error) {
	if _, err := requiredPackage(request, CapabilityDownload); err != nil {
		return model.Download{}, err
	}
	var data map[string][]jetBrainsRelease
	if err := p.Source.GetJSON(ctx, expandEndpoint(p.Endpoint, url.QueryEscape(request.App.Package)), &data); err != nil {
		return model.Download{}, err
	}
	releases, err := jetBrainsReleases(data, request.App.Package)
	if err != nil {
		return model.Download{}, err
	}
	info := p.systemInfo()
	key, supported := info.JetBrainsPlatformKey()
	if !supported {
		return model.Download{}, NewError("provider.jetbrains_download_unavailable", info.FullName, 0)
	}
	var links []string
	for _, release := range releases {
		if link := strings.TrimSpace(release.Downloads[key].Link); link != "" {
			links = append(links, link)
		}
	}
	if len(links) != 1 {
		return model.Download{}, NewError("provider.jetbrains_download_unavailable", key, len(links))
	}
	downloadURL, err := trustedDownloadURL(expandEndpoint(p.Endpoint, url.QueryEscape(request.App.Package)), links[0])
	if err != nil {
		return model.Download{}, err
	}
	filename := downloadFilename(downloadURL, "jetbrains-"+releases[0].Version+".dmg")
	if filename == "" {
		return model.Download{}, NewError("provider.jetbrains_download_unavailable", key, len(links))
	}
	return model.Download{URL: downloadURL, Filename: filename}, nil
}

func (p JetBrainsProvider) Latest(ctx context.Context, request Request) (string, error) {
	if _, err := requiredPackage(request, CapabilityLatest); err != nil {
		return "", err
	}
	var data map[string][]jetBrainsRelease
	endpoint := expandEndpoint(p.Endpoint, url.QueryEscape(request.App.Package))
	if err := p.Source.GetJSON(ctx, endpoint, &data); err != nil {
		return "", err
	}
	return jetBrainsVersion(data, request.App.Package)
}
func jetBrainsVersion(data map[string][]jetBrainsRelease, productCode string) (string, error) {
	releases, err := jetBrainsReleases(data, productCode)
	if err != nil {
		return "", err
	}
	return normalizeAny(releases[0].Version)
}
func jetBrainsReleases(data map[string][]jetBrainsRelease, productCode string) ([]jetBrainsRelease, error) {
	releases := data[productCode]
	if len(releases) == 0 {
		for code, candidates := range data {
			if strings.EqualFold(code, productCode) && len(candidates) > 0 {
				releases = candidates
				break
			}
		}
	}
	if len(releases) == 0 {
		return nil, NewError("provider.jetbrains_empty", productCode)
	}
	return releases, nil
}
