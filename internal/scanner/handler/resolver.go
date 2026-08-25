package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	httpx "github.com/eoctet/tendkit/pkg/http"
)

// GitHubResolver identifies the supported provider type for a GitHub project.
type GitHubResolver interface {
	Resolve(context.Context, string) (model.ProviderType, error)
}

type githubResolver struct {
	releaseEndpoint, tagEndpoint string
	source                       *httpx.HTTPSource
}

// NewGitHubResolver constructs a resolver using the configured release and tag endpoints.
func NewGitHubResolver(releaseEndpoint, tagEndpoint string, source *httpx.HTTPSource) GitHubResolver {
	if source == nil {
		source = httpx.NewHTTPSource(nil)
	}
	return &githubResolver{releaseEndpoint: releaseEndpoint, tagEndpoint: tagEndpoint, source: source}
}

func (r *githubResolver) Resolve(ctx context.Context, project string) (model.ProviderType, error) {
	project = strings.Trim(strings.TrimSpace(project), "/")
	if strings.Count(project, "/") != 1 {
		return "", fmt.Errorf("invalid GitHub project %q", project)
	}
	status, populated, err := r.fetch(ctx, r.releaseEndpoint, project, true)
	if err != nil {
		return "", err
	}
	if status == 200 && populated {
		return model.ProviderGitHubRelease, nil
	}
	if status != 404 && status != 200 {
		return "", fmt.Errorf("GitHub release probe returned %d", status)
	}
	status, populated, err = r.fetch(ctx, r.tagEndpoint, project, false)
	if err != nil {
		return "", err
	}
	if status == 200 && populated {
		return model.ProviderGitHubTag, nil
	}
	if status == 404 || status == 200 {
		return "", nil
	}
	return "", fmt.Errorf("GitHub tag probe returned %d", status)
}

func (r *githubResolver) fetch(ctx context.Context, endpoint, project string, release bool) (int, bool, error) {
	endpoint = strings.ReplaceAll(endpoint, "{package}", project)
	body, err := r.source.Get(ctx, endpoint, "application/vnd.github+json", 1<<20, false)
	if err != nil {
		var statusErr *httpx.HTTPStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == 404 {
			return 404, false, nil
		}
		return 0, false, err
	}
	if release {
		var value struct {
			Tag string `json:"tag_name"`
		}
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, false, err
		}
		return 200, strings.TrimSpace(value.Tag) != "", nil
	}
	var values []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &values); err != nil {
		return 0, false, err
	}
	return 200, len(values) > 0 && strings.TrimSpace(values[0].Name) != "", nil
}

// GitHubProject extracts a canonical owner/repository identifier from a GitHub URL.
func GitHubProject(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Host, "github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
