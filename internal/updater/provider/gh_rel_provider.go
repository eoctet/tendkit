package provider

import (
	"context"
	"encoding/hex"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// GitHubReleaseProvider resolves GitHub release versions and configured assets.
type GitHubReleaseProvider struct {
	Source   *HTTPSource
	Endpoint string
	host     func() runtimeutil.SystemInfo
}

func (p GitHubReleaseProvider) systemInfo() runtimeutil.SystemInfo {
	if p.host != nil {
		return p.host()
	}
	return runtimeutil.HostPlatform()
}

func (p GitHubReleaseProvider) Latest(ctx context.Context, request Request) (string, error) {
	name, err := requiredPackage(request, CapabilityLatest)
	if err != nil {
		return "", err
	}
	return p.Source.JSONField(ctx, expandEndpoint(p.Endpoint, name), "tag_name")
}
func (p GitHubReleaseProvider) Download(ctx context.Context, request Request) (model.Download, error) {
	if _, err := requiredPackage(request, CapabilityDownload); err != nil {
		return model.Download{}, err
	}
	asset, err := p.asset(ctx, request)
	if err != nil {
		return model.Download{}, err
	}
	downloadURL, err := trustedDownloadURL(expandEndpoint(p.Endpoint, request.App.Package), asset.URL)
	if err != nil {
		return model.Download{}, err
	}
	filename := downloadFilename(downloadURL, asset.Name)
	if filename == "" {
		return model.Download{}, NewError("provider.github_asset_filename_unavailable")
	}
	return model.Download{URL: downloadURL, Filename: filename, ChecksumEnabled: true}, nil
}
func (p GitHubReleaseProvider) Checksum(ctx context.Context, request Request) (string, error) {
	if _, err := requiredPackage(request, CapabilityChecksum); err != nil {
		return "", err
	}
	asset, err := p.asset(ctx, request)
	if err != nil {
		return "", err
	}
	digest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(asset.Digest)), "sha256:")
	if !ok {
		return "", NewError("provider.github_digest_unavailable", asset.Name)
	}
	digest, err = normalizeSHA256(digest)
	if err != nil {
		return "", NewError("provider.github_digest_unavailable", asset.Name)
	}
	return digest, nil
}
func (p GitHubReleaseProvider) Artifact(ctx context.Context, request Request) (string, error) {
	if _, err := requiredPackage(request, CapabilityArtifact); err != nil {
		return "", err
	}
	asset, err := p.asset(ctx, request)
	return asset.Name, err
}

// ArtifactCandidates returns host-compatible release asset names when the
// filename heuristic finds any. Otherwise it returns every named asset so the
// caller can ask the user to choose explicitly.
func (p GitHubReleaseProvider) ArtifactChoices(ctx context.Context, request Request) (model.DownloadAssetChoices, error) {
	if _, err := requiredPackage(request, CapabilityArtifact); err != nil {
		return model.DownloadAssetChoices{}, err
	}
	assets, err := p.assets(ctx, request)
	if err != nil {
		return model.DownloadAssetChoices{}, err
	}
	choices, fallback := githubReleaseAssetCandidates(assets, p.systemInfo())
	return model.DownloadAssetChoices{Candidates: choices, SelectionRequired: fallback}, nil
}

func (p GitHubReleaseProvider) ArtifactCandidates(ctx context.Context, request Request) ([]string, error) {
	choices, err := p.ArtifactChoices(ctx, request)
	return choices.Candidates, err
}

type githubReleaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

func (p GitHubReleaseProvider) asset(ctx context.Context, request Request) (githubReleaseAsset, error) {
	assets, err := p.assets(ctx, request)
	if err != nil {
		return githubReleaseAsset{}, err
	}
	if configured := request.App.Provider.DownloadAction(); configured != nil {
		targetName, err := githubReleaseAssetName(*configured, request.Values)
		if err != nil {
			return githubReleaseAsset{}, err
		}
		if targetName == "" {
			return githubReleaseAsset{}, NewError("provider.github_asset_name_unavailable")
		}
		for _, asset := range assets {
			if asset.Name == targetName {
				return asset, nil
			}
		}
		return githubReleaseAsset{}, NewError("provider.github_asset_named_unavailable", targetName)
	}
	if selected := strings.TrimSpace(request.SelectedArtifact); selected != "" {
		info := p.systemInfo()
		choices, _ := githubReleaseAssetCandidates(assets, info)
		for _, asset := range assets {
			if asset.Name == selected {
				if !containsExact(choices, selected) {
					return githubReleaseAsset{}, NewError("provider.github_asset_named_unavailable", selected)
				}
				return asset, nil
			}
		}
		return githubReleaseAsset{}, NewError("provider.github_asset_named_unavailable", selected)
	}
	info := p.systemInfo()
	return selectGitHubReleaseAsset(assets, info)
}

func (p GitHubReleaseProvider) assets(ctx context.Context, request Request) ([]githubReleaseAsset, error) {
	var release struct {
		Assets []githubReleaseAsset `json:"assets"`
	}
	if err := p.Source.GetJSON(ctx, expandEndpoint(p.Endpoint, request.App.Package), &release); err != nil {
		return nil, err
	}
	return release.Assets, nil
}

func selectGitHubReleaseAsset(assets []githubReleaseAsset, info runtimeutil.SystemInfo) (githubReleaseAsset, error) {
	var matches []githubReleaseAsset
	for _, asset := range assets {
		if githubAssetMatchesHost(asset.Name, info) {
			matches = append(matches, asset)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return githubReleaseAsset{}, NewError("provider.github_asset_unavailable", info.OS, info.Architecture)
	}
	return githubReleaseAsset{}, NewError("provider.github_asset_ambiguous", info.OS, info.Architecture)
}
func githubAssetMatchesHost(name string, info runtimeutil.SystemInfo) bool {
	return info.MatchesGitHubArtifact(name)
}
func githubReleaseAssetCandidates(assets []githubReleaseAsset, info runtimeutil.SystemInfo) ([]string, bool) {
	matches := make([]string, 0, len(assets))
	fallback := make([]string, 0, len(assets))
	for _, asset := range assets {
		if strings.TrimSpace(asset.Name) == "" {
			continue
		}
		name := asset.Name
		fallback = append(fallback, name)
		if githubAssetMatchesHost(name, info) {
			matches = append(matches, name)
		}
	}
	if len(matches) > 0 {
		sort.Strings(matches)
		return matches, false
	}
	sort.Strings(fallback)
	return fallback, len(fallback) > 0
}
func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func githubReleaseAssetName(download model.Download, values map[string]string) (string, error) {
	renderedURL, err := runtimeutil.Render(download.URL, values, false)
	if err != nil {
		return "", err
	}
	parsed, parseErr := url.Parse(renderedURL)
	if parseErr == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && !strings.HasSuffix(parsed.Path, "/") {
		name := path.Base(parsed.Path)
		if name != "" && name != "." && name != "/" {
			return name, nil
		}
	}
	return runtimeutil.Render(download.Filename, values, false)
}
func normalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return "", NewError("provider.github_digest_unavailable")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", NewError("provider.github_digest_unavailable")
	}
	return value, nil
}
