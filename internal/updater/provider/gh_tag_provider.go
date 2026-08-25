package provider

import (
	"context"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
)

type GitHubTagProvider struct {
	Source   *HTTPSource
	Endpoint string
}

func (p GitHubTagProvider) Latest(ctx context.Context, request Request) (string, error) {
	if _, err := requiredPackage(request, CapabilityLatest); err != nil {
		return "", err
	}
	tag, err := p.firstTag(ctx, request)
	if err != nil {
		return "", err
	}
	return normalizeAny(tag.Name)
}

func (p GitHubTagProvider) Download(ctx context.Context, request Request) (model.Download, error) {
	if _, err := requiredPackage(request, CapabilityDownload); err != nil {
		return model.Download{}, err
	}
	tag, err := p.firstTag(ctx, request)
	if err != nil {
		return model.Download{}, err
	}
	downloadURL := strings.TrimSpace(tag.TarballURL)
	if downloadURL == "" {
		downloadURL = strings.TrimSpace(tag.ZipballURL)
	}
	if downloadURL == "" {
		return model.Download{}, NewError("provider.github_tag_download_unavailable")
	}
	downloadURL, err = trustedDownloadURL(expandEndpoint(p.Endpoint, request.App.Package), downloadURL)
	if err != nil {
		return model.Download{}, err
	}
	fallback := "github-source.tar.gz"
	if strings.TrimSpace(tag.TarballURL) == "" {
		fallback = "github-source.zip"
	}
	filename := downloadFilename(downloadURL, fallback)
	if filename == "" {
		return model.Download{}, NewError("provider.github_tag_download_unavailable")
	}
	return model.Download{URL: downloadURL, Filename: filename}, nil
}

func (p GitHubTagProvider) Artifact(ctx context.Context, request Request) (string, error) {
	if _, err := requiredPackage(request, CapabilityArtifact); err != nil {
		return "", err
	}
	tag, err := p.firstTag(ctx, request)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tag.Name), nil
}

type githubTag struct {
	Name       string `json:"name"`
	TarballURL string `json:"tarball_url"`
	ZipballURL string `json:"zipball_url"`
}

func (p GitHubTagProvider) firstTag(ctx context.Context, request Request) (githubTag, error) {
	var tags []githubTag
	if err := p.Source.GetJSON(ctx, expandEndpoint(p.Endpoint, request.App.Package), &tags); err != nil {
		return githubTag{}, err
	}
	if len(tags) == 0 {
		return githubTag{}, NewError("provider.github_no_tag")
	}
	if strings.TrimSpace(tags[0].Name) == "" {
		return githubTag{}, NewError("provider.github_no_tag")
	}
	return tags[0], nil
}
