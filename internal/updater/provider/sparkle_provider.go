package provider

import (
	"context"
	"encoding/xml"
	"net/url"
	"path"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

type SparkleProvider struct {
	Source *HTTPSource
	Runner runtimeutil.Runner
}
type sparkleRelease struct {
	Version string
	URL     string
}

func (p SparkleProvider) Latest(ctx context.Context, request Request) (string, error) {
	release, err := p.release(ctx, request)
	return release.Version, err
}
func (p SparkleProvider) Download(ctx context.Context, request Request) (model.Download, error) {
	release, err := p.release(ctx, request)
	if err != nil {
		return model.Download{}, err
	}
	if release.URL == "" {
		return model.Download{}, NewError("provider.sparkle_artifact_empty")
	}
	return model.Download{URL: release.URL}, nil
}
func (p SparkleProvider) Artifact(ctx context.Context, request Request) (string, error) {
	release, err := p.release(ctx, request)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(release.URL)
	if err != nil || parsed.Path == "" {
		return "", NewError("provider.sparkle_artifact_empty")
	}
	artifact := path.Base(parsed.Path)
	if artifact == "." || artifact == "/" || artifact == "" {
		return "", NewError("provider.sparkle_artifact_empty")
	}
	return artifact, nil
}
func (p SparkleProvider) Update(ctx context.Context, request Request) error {
	if request.App.Type != model.ApplicationTypeBundle {
		return CapabilityUnavailable(string(request.App.Provider.Type), CapabilityUpdate)
	}
	metadata, err := metadatautil.ReadMacApplicationMetadata(ctx, request.App.InstallPath)
	if err != nil || strings.TrimSpace(metadata.SparkleFeedURL) == "" {
		if err == nil {
			err = NewError("provider.sparkle_feed_unavailable")
		}
		return &Error{Key: "provider.sparkle_update_unavailable", Args: []any{request.App.Name}, Provider: string(request.App.Provider.Type), Capability: CapabilityUpdate, Cause: err}
	}
	feed, err := url.Parse(metadata.SparkleFeedURL)
	if err != nil || feed.Scheme != "https" || feed.Host == "" {
		return &Error{Key: "provider.sparkle_update_unavailable", Args: []any{request.App.Name}, Provider: string(request.App.Provider.Type), Capability: CapabilityUpdate, Cause: NewError("http.url_invalid", metadata.SparkleFeedURL)}
	}
	cli, err := metadatautil.FindSparkleCLI()
	if err != nil {
		return &Error{Key: "provider.sparkle_update_unavailable", Args: []any{request.App.Name}, Provider: string(request.App.Provider.Type), Capability: CapabilityUpdate, Cause: err}
	}
	command := runtimeutil.QuoteShell(cli) + " '--check-immediately' '--feed-url' " + runtimeutil.QuoteShell(metadata.SparkleFeedURL) + " '--user-agent-name' " + runtimeutil.QuoteShell(request.App.Name) + " " + runtimeutil.QuoteShell(request.App.InstallPath)
	result, err := p.Runner.Run(ctx, command, request.App.Environment)
	if err != nil {
		return &Error{Key: "provider.sparkle_update_failed", Args: []any{request.App.Name}, Provider: string(request.App.Provider.Type), Capability: CapabilityUpdate, Cause: err}
	}
	if result.ExitCode != 0 {
		return &Error{Key: "provider.sparkle_update_exit", Args: []any{request.App.Name, result.ExitCode, strings.TrimSpace(result.Combined())}, Provider: string(request.App.Provider.Type), Capability: CapabilityUpdate}
	}
	return nil
}
func (p SparkleProvider) release(ctx context.Context, request Request) (sparkleRelease, error) {
	endpoint := strings.TrimSpace(request.App.Package)
	if endpoint == "" {
		endpoint, _ = sparkleFeedURL(ctx, request.App.InstallPath)
	}
	if endpoint == "" {
		endpoint = request.App.URL
	}
	body, err := p.Source.Get(ctx, endpoint, "application/xml")
	if err != nil {
		return sparkleRelease{}, err
	}
	var feed struct {
		Channel struct {
			Items []struct {
				Enclosure struct {
					Short   string `xml:"shortVersionString,attr"`
					Version string `xml:"version,attr"`
					URL     string `xml:"url,attr"`
				} `xml:"enclosure"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return sparkleRelease{}, WrapError("provider.sparkle_parse_failed", err, endpoint)
	}
	if len(feed.Channel.Items) == 0 {
		return sparkleRelease{}, NewError("provider.sparkle_empty")
	}
	latest := sparkleRelease{}
	for _, item := range feed.Channel.Items {
		value := item.Enclosure.Short
		if value == "" {
			value = item.Enclosure.Version
		}
		normalized, normalizeErr := normalizeAny(value)
		if normalizeErr != nil {
			continue
		}
		comparison, comparable := version.Compare(normalized, latest.Version)
		if latest.Version == "" || comparable && comparison > 0 {
			latest = sparkleRelease{Version: normalized, URL: strings.TrimSpace(item.Enclosure.URL)}
		}
	}
	if latest.Version == "" {
		return sparkleRelease{}, NewError("provider.sparkle_empty")
	}
	return latest, nil
}
func sparkleFeedURL(parent context.Context, appPath string) (string, error) {
	metadata, err := metadatautil.ReadMacApplicationMetadata(parent, appPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(metadata.SparkleFeedURL), nil
}
