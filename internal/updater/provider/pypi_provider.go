package provider

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
)

type PyPIProvider struct {
	Source   *HTTPSource
	Endpoint string
}

func (p PyPIProvider) Latest(ctx context.Context, request Request) (string, error) {
	data, err := p.metadata(ctx, request, CapabilityLatest)
	if err != nil {
		return "", err
	}
	return normalizeAny(data.Info.Version)
}

func (p PyPIProvider) Download(ctx context.Context, request Request) (model.Download, error) {
	data, err := p.metadata(ctx, request, CapabilityDownload)
	if err != nil {
		return model.Download{}, err
	}
	var candidates []pypiFile
	for _, file := range data.URLs {
		if file.PackageType == "sdist" && strings.TrimSpace(file.URL) != "" {
			candidates = append(candidates, file)
		}
	}
	if len(candidates) != 1 {
		return model.Download{}, NewError("provider.pypi_sdist_unavailable", len(candidates))
	}
	downloadURL, err := trustedDownloadURL(expandEndpoint(p.Endpoint, url.PathEscape(request.App.Package)), candidates[0].URL)
	if err != nil {
		return model.Download{}, err
	}
	filename := downloadFilename(downloadURL, candidates[0].Filename)
	if filename == "" {
		return model.Download{}, NewError("provider.pypi_sdist_unavailable", len(candidates))
	}
	return model.Download{URL: downloadURL, Filename: filename}, nil
}

func (p PyPIProvider) Artifact(ctx context.Context, request Request) (string, error) {
	data, err := p.metadata(ctx, request, CapabilityArtifact)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(data.Info.Name)
	if name == "" {
		return "", NewError("provider.pypi_artifact_unavailable")
	}
	return pep503Name(name), nil
}

var pep503Separators = regexp.MustCompile(`[-_.]+`)

func pep503Name(name string) string {
	return pep503Separators.ReplaceAllString(strings.ToLower(name), "-")
}

type pypiFile struct {
	PackageType string `json:"packagetype"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
}
type pypiMetadata struct {
	Info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"info"`
	URLs []pypiFile `json:"urls"`
}

func (p PyPIProvider) metadata(ctx context.Context, request Request, capability Capability) (pypiMetadata, error) {
	var data pypiMetadata
	name, err := requiredPackage(request, capability)
	if err != nil {
		return data, err
	}
	if err := p.Source.GetJSON(ctx, expandEndpoint(p.Endpoint, url.PathEscape(name)), &data); err != nil {
		return pypiMetadata{}, err
	}
	return data, nil
}
