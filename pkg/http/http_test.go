package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type httpRoundTripFunc func(*http.Request) (*http.Response, error)

func (function httpRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
func TestHTTPSourceContract(t *testing.T) {
	t.Run("host-gate-limits-concurrency-and-honors-cancellation", func(t *testing.T) {
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			entered <- struct{}{}
			<-release
			_, _ = w.Write([]byte("ok"))
		}))
		defer server.Close()
		source := NewHTTPSource(server.Client(), HTTPOptions{Retries: 0, MaxConcurrencyPerHost: 1})
		var group sync.WaitGroup
		for range 2 {
			group.Add(1)
			go func() {
				defer group.Done()
				_, _ = source.Get(context.Background(), server.URL, "text/plain", 16, false)
			}()
		}
		<-entered
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if _, err := source.Get(ctx, server.URL, "text/plain", 16, false); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("gate wait error = %v", err)
		}
		select {
		case <-entered:
			t.Fatal("same-host requests ran concurrently")
		default:
		}
		close(release)
		group.Wait()
	})
	t.Run("http-source-retries-transient-network-failure", func(t *testing.T) {
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
	})
	t.Run("http-source-only-sends-github-token-to-api-host", func(t *testing.T) {
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
	})
	t.Run("parse-retry-after-seconds", func(t *testing.T) {
		if delay := parseRetryAfter("2", time.Now()); delay != 2*time.Second {
			t.Fatalf("Retry-After delay = %s", delay)
		}
	})
	t.Run("http-source-rejects-invalid-status-type-and-size", func(t *testing.T) {
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
			server := httptest.NewServer(test.handler)
			_, err := NewHTTPSource(server.Client(), HTTPOptions{Retries: 0, MaxConcurrencyPerHost: 1}).Get(context.Background(), server.URL, "text/plain", test.limit, test.requireText)
			server.Close()
			if err == nil {
				t.Errorf("%s: unsafe response accepted", test.name)
				continue
			}
			switch test.kind {
			case "status":
				var target *HTTPStatusError
				if !errors.As(err, &target) {
					t.Errorf("%s: error = %v", test.name, err)
				}
			case "content":
				var target *HTTPContentTypeError
				if !errors.As(err, &target) {
					t.Errorf("%s: error = %v", test.name, err)
				}
			case "size":
				var target *HTTPResponseTooLargeError
				if !errors.As(err, &target) {
					t.Errorf("%s: error = %v", test.name, err)
				}
			}
		}
		_, err := NewHTTPSource(nil, HTTPOptions{Retries: 0}).Get(context.Background(), "file:///tmp/value", "text/plain", 16, false)
		var invalid *HTTPURLInvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("invalid URL error = %v", err)
		}
	})
}
