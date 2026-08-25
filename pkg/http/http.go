package http

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultHTTPTimeout = 60 * time.Second
	defaultHTTPRetries = 2
	defaultHostLimit   = 1
	defaultRetryDelay  = 250 * time.Millisecond
	maximumRetryDelay  = 30 * time.Second
	githubAPIHost      = "api.github.com"
)

type HTTPOptions struct {
	Timeout               time.Duration
	MaxConcurrencyPerHost int
	Retries               int
	RetryDelay            time.Duration
}

// HTTPSource is the shared bounded GET transport for Scanner and Provider code.
type HTTPSource struct {
	client                *http.Client
	retries               int
	maxConcurrencyPerHost int
	retryDelay            time.Duration
	gatesMu               sync.Mutex
	gates                 map[string]chan struct{}
}

func NewHTTPSource(client *http.Client, configured ...HTTPOptions) *HTTPSource {
	options := HTTPOptions{Timeout: defaultHTTPTimeout, MaxConcurrencyPerHost: defaultHostLimit, Retries: defaultHTTPRetries, RetryDelay: defaultRetryDelay}
	if len(configured) > 0 {
		options = configured[0]
		if options.Timeout <= 0 {
			options.Timeout = defaultHTTPTimeout
		}
		if options.MaxConcurrencyPerHost < 0 {
			options.MaxConcurrencyPerHost = 0
		}
		if options.Retries < 0 {
			options.Retries = 0
		}
		if options.RetryDelay <= 0 {
			options.RetryDelay = defaultRetryDelay
		}
	}
	if client == nil {
		client = &http.Client{Timeout: options.Timeout}
	} else if len(configured) > 0 {
		cloned := *client
		cloned.Timeout = options.Timeout
		client = &cloned
	}
	return &HTTPSource{client: client, retries: options.Retries, maxConcurrencyPerHost: options.MaxConcurrencyPerHost, retryDelay: options.RetryDelay, gates: make(map[string]chan struct{})}
}

type HTTPStatusError struct {
	Endpoint   string
	StatusCode int
	retryAfter time.Duration
}

func (err *HTTPStatusError) Error() string { return http.StatusText(err.StatusCode) }

type HTTPResponseTooLargeError struct {
	Endpoint string
	Limit    int64
}

func (err *HTTPResponseTooLargeError) Error() string { return "HTTP response exceeds configured limit" }

type HTTPContentTypeError struct{ Endpoint, MediaType string }

func (err *HTTPContentTypeError) Error() string { return "HTTP response has an invalid content type" }

type HTTPURLInvalidError struct{ Endpoint string }

func (err *HTTPURLInvalidError) Error() string { return "invalid HTTP URL: " + err.Endpoint }

// Get returns a fully read response and rejects non-2xx, oversized, or invalid text responses.
func (s *HTTPSource) Get(ctx context.Context, endpoint, accept string, limit int64, requireText bool) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.Join(&HTTPURLInvalidError{Endpoint: endpoint}, err)
	}
	if (request.URL.Scheme != "http" && request.URL.Scheme != "https") || request.URL.Host == "" {
		return nil, &HTTPURLInvalidError{Endpoint: endpoint}
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "tendkit-go/1.0")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" && strings.EqualFold(request.URL.Hostname(), githubAPIHost) {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if s == nil {
		s = NewHTTPSource(nil)
	}
	var lastErr error
	for attempt := 0; attempt <= s.retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := s.acquire(ctx, strings.ToLower(request.URL.Host))
		if err != nil {
			return nil, err
		}
		body, err := s.getOnce(request, limit, requireText)
		release()
		if err == nil {
			return body, nil
		}
		lastErr = err
		if attempt == s.retries || !retryableHTTPError(err) {
			break
		}
		if err := waitForRetry(ctx, s.retryDelay, attempt, err); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (s *HTTPSource) getOnce(request *http.Request, limit int64, requireText bool) ([]byte, error) {
	response, err := s.client.Do(request.Clone(request.Context()))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		closeErr := response.Body.Close()
		return nil, errors.Join(&HTTPStatusError{Endpoint: request.URL.String(), StatusCode: response.StatusCode, retryAfter: retryAfter}, closeErr)
	}
	if requireText {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
		if mediaType != "" && !strings.HasPrefix(mediaType, "text/") {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			closeErr := response.Body.Close()
			return nil, errors.Join(&HTTPContentTypeError{Endpoint: request.URL.String(), MediaType: mediaType}, closeErr)
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	closeErr := response.Body.Close()
	if err != nil {
		return nil, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(body)) > limit {
		return nil, &HTTPResponseTooLargeError{Endpoint: request.URL.String(), Limit: limit}
	}
	return body, nil
}

func (s *HTTPSource) acquire(ctx context.Context, host string) (func(), error) {
	if s.maxConcurrencyPerHost <= 0 || host == "" {
		return func() {}, nil
	}
	s.gatesMu.Lock()
	gate := s.gates[host]
	if gate == nil {
		gate = make(chan struct{}, s.maxConcurrencyPerHost)
		s.gates[host] = gate
	}
	s.gatesMu.Unlock()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Acquire reserves one configured per-host request slot.
func (s *HTTPSource) Acquire(ctx context.Context, host string) (func(), error) {
	return s.acquire(ctx, strings.ToLower(host))
}

// IsRetryableHTTPError reports whether the shared transport retries err.
func IsRetryableHTTPError(err error) bool { return retryableHTTPError(err) }

func retryableHTTPError(err error) bool {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.EPIPE)
}

func waitForRetry(ctx context.Context, base time.Duration, attempt int, err error) error {
	delay := base
	for range attempt {
		if delay >= maximumRetryDelay/2 {
			delay = maximumRetryDelay
			break
		}
		delay *= 2
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) && statusErr.retryAfter > delay {
		delay = statusErr.retryAfter
	}
	if delay > maximumRetryDelay {
		delay = maximumRetryDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}
