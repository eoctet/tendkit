package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	httpx "github.com/eoctet/tendkit/pkg/http"
)

const (
	maxResponseSize     = 8 << 20
	maxTextResponseSize = 64 << 10
	githubAPIHost       = "api.github.com"
)

// HTTPSource adds Provider typed errors to the shared bounded HTTP transport.
type HTTPSource struct{ source *httpx.HTTPSource }

func NewHTTPSource(client *http.Client, configured ...httpx.HTTPOptions) *HTTPSource {
	return &HTTPSource{source: httpx.NewHTTPSource(client, configured...)}
}

func (s *HTTPSource) GetJSON(ctx context.Context, endpoint string, target any) error {
	body, err := s.Get(ctx, endpoint, "application/json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return WrapError("http.parse_failed", err, endpoint)
	}
	return nil
}

func (s *HTTPSource) JSONField(ctx context.Context, endpoint, field string) (string, error) {
	var data map[string]any
	if err := s.GetJSON(ctx, endpoint, &data); err != nil {
		return "", err
	}
	return normalizeAny(data[field])
}

func (s *HTTPSource) Get(ctx context.Context, endpoint, accept string) ([]byte, error) {
	return s.get(ctx, endpoint, accept, maxResponseSize, false)
}

func (s *HTTPSource) GetText(ctx context.Context, endpoint string) ([]byte, error) {
	return s.get(ctx, endpoint, "text/plain", maxTextResponseSize, true)
}

func (s *HTTPSource) get(ctx context.Context, endpoint, accept string, limit int64, requireText bool) ([]byte, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, NewError("http.empty_url")
	}
	if s == nil || s.source == nil {
		s = NewHTTPSource(nil)
	}
	body, err := s.source.Get(ctx, endpoint, accept, limit, requireText)
	if err == nil {
		return body, nil
	}
	var statusErr *httpx.HTTPStatusError
	if errors.As(err, &statusErr) {
		return nil, WrapError("http.status", err, endpoint, statusErr.StatusCode)
	}
	var tooLarge *httpx.HTTPResponseTooLargeError
	if errors.As(err, &tooLarge) {
		return nil, WrapError("http.response_too_large", err, endpoint, tooLarge.Limit)
	}
	var contentType *httpx.HTTPContentTypeError
	if errors.As(err, &contentType) {
		return nil, WrapError("http.content_type_invalid", err, endpoint, contentType.MediaType)
	}
	var invalidURL *httpx.HTTPURLInvalidError
	if errors.As(err, &invalidURL) {
		return nil, WrapError("http.url_invalid", err, endpoint)
	}
	if ctx.Err() != nil {
		return nil, WrapError("http.request_failed", ctx.Err(), endpoint)
	}
	return nil, WrapError("http.request_failed", err, endpoint)
}
