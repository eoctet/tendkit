package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	httpx "github.com/eoctet/tendkit/pkg/http"
)

func TestGitHubResolverReleaseThenTagFailClosed(t *testing.T) {
	for _, test := range []struct {
		name          string
		releaseStatus int
		releaseBody   string
		tagStatus     int
		tagBody       string
		want          model.ProviderType
		wantErr       bool
	}{
		{"release", 200, `{"tag_name":"v1"}`, 500, ``, model.ProviderGitHubRelease, false},
		{"tag", 404, ``, 200, `[{"name":"v1"}]`, model.ProviderGitHubTag, false},
		{"none", 404, ``, 200, `[]`, "", false},
		{"server error", 500, ``, 200, `[]`, "", true},
		{"malformed", 200, `{`, 200, `[]`, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/release" {
					w.WriteHeader(test.releaseStatus)
					_, _ = w.Write([]byte(test.releaseBody))
					return
				}
				w.WriteHeader(test.tagStatus)
				_, _ = w.Write([]byte(test.tagBody))
			}))
			defer server.Close()
			source := httpx.NewHTTPSource(server.Client(), httpx.HTTPOptions{Retries: 0, MaxConcurrencyPerHost: 1})
			got, err := NewGitHubResolver(server.URL+"/release", server.URL+"/tag", source).Resolve(context.Background(), "owner/repo")
			if got != test.want || (err != nil) != test.wantErr {
				t.Fatalf("Resolve() = %q, %v", got, err)
			}
		})
	}
}

func TestGitHubResolverRejectsOversizedValidPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1"}` + strings.Repeat(" ", (1<<20)+1)))
	}))
	defer server.Close()
	source := httpx.NewHTTPSource(server.Client(), httpx.HTTPOptions{Retries: 0, MaxConcurrencyPerHost: 1})
	_, err := NewGitHubResolver(server.URL, server.URL, source).Resolve(context.Background(), "owner/repo")
	var oversized *httpx.HTTPResponseTooLargeError
	if !errors.As(err, &oversized) {
		t.Fatalf("oversized response error = %v", err)
	}
}
