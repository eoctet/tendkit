package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type httpRoundTripFunc func(*http.Request) (*http.Response, error)

func (function httpRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestHTTPSourceRetriesTransientNetworkFailure(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: httpRoundTripFunc(func(*http.Request) (*http.Response, error) {
		if attempts.Add(1) < 3 {
			return nil, syscall.ECONNRESET
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}
	source := NewHTTPSource(client, HTTPOptions{Retries: 2, RetryDelay: time.Millisecond, MaxConcurrencyPerHost: 1})
	body, err := source.Get(context.Background(), "https://example.invalid", "text/plain", 16, false)
	if err != nil || string(body) != "ok" || attempts.Load() != 3 {
		t.Fatalf("Get() = %q, %v after %d attempts", body, err, attempts.Load())
	}
}

func TestHTTPSourceOnlySendsGitHubTokenToAPIHost(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	client := &http.Client{Transport: httpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if value := request.Header.Get("Authorization"); value != "" {
			t.Errorf("token leaked to %s: %q", request.URL.Host, value)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})}
	_, err := NewHTTPSource(client, HTTPOptions{Retries: 0, MaxConcurrencyPerHost: 1}).Get(context.Background(), "https://example.com/test", "application/json", 16, false)
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	if delay := parseRetryAfter("2", time.Now()); delay != 2*time.Second {
		t.Fatalf("Retry-After delay = %s", delay)
	}
}

func TestHTTPSourceRejectsInvalidStatusTypeAndSize(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		limit       int64
		requireText bool
		kind        string
	}{
		{"status", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }, 16, false, "status"},
		{"content type", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		}, 16, true, "content"},
		{"oversized", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("too-large")) }, 3, false, "size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			_, err := NewHTTPSource(server.Client(), HTTPOptions{Retries: 0, MaxConcurrencyPerHost: 1}).Get(context.Background(), server.URL, "text/plain", test.limit, test.requireText)
			if err == nil {
				t.Fatal("unsafe response accepted")
			}
			switch test.kind {
			case "status":
				var target *HTTPStatusError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v", err)
				}
			case "content":
				var target *HTTPContentTypeError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v", err)
				}
			case "size":
				var target *HTTPResponseTooLargeError
				if !errors.As(err, &target) {
					t.Fatalf("error = %v", err)
				}
			}
		})
	}
	_, err := NewHTTPSource(nil, HTTPOptions{Retries: 0}).Get(context.Background(), "file:///tmp/value", "text/plain", 16, false)
	var invalid *HTTPURLInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid URL error = %v", err)
	}
}
