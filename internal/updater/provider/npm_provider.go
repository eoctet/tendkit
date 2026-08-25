package provider

import (
	"context"
	"net/url"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
)

type NPMProvider struct {
	Source   *HTTPSource
	Endpoint string
}

func (p NPMProvider) Latest(ctx context.Context, request Request) (string, error) {
	data, err := p.metadata(ctx, request, CapabilityLatest)
	if err != nil {
		return "", err
	}
	return normalizeAny(data.Version)
}

func (p NPMProvider) Download(ctx context.Context, request Request) (model.Download, error) {
	data, err := p.metadata(ctx, request, CapabilityDownload)
	if err != nil {
		return model.Download{}, err
	}
	if strings.TrimSpace(data.Dist.Tarball) == "" {
		return model.Download{}, NewError("provider.npm_tarball_unavailable")
	}
	endpoint := expandEndpoint(p.Endpoint, url.PathEscape(request.App.Package))
	downloadURL, err := trustedDownloadURL(endpoint, data.Dist.Tarball)
	if err != nil {
		return model.Download{}, err
	}
	filename := downloadFilename(downloadURL, "npm-"+data.Version+".tgz")
	if filename == "" {
		return model.Download{}, NewError("provider.npm_tarball_unavailable")
	}
	return model.Download{URL: downloadURL, Filename: filename}, nil
}

func (p NPMProvider) Artifact(ctx context.Context, request Request) (string, error) {
	data, err := p.metadata(ctx, request, CapabilityArtifact)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(data.Name) == "" || strings.TrimSpace(data.Version) == "" {
		return "", NewError("provider.npm_artifact_unavailable")
	}
	return data.Name + "@" + data.Version, nil
}

type npmMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Dist    struct {
		Tarball string `json:"tarball"`
	} `json:"dist"`
}

func (p NPMProvider) metadata(ctx context.Context, request Request, capability Capability) (npmMetadata, error) {
	var data npmMetadata
	name, err := requiredPackage(request, capability)
	if err != nil {
		return data, err
	}
	if err := p.Source.GetJSON(ctx, expandEndpoint(p.Endpoint, url.PathEscape(name)), &data); err != nil {
		return npmMetadata{}, err
	}
	return data, nil
}
