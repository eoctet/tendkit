package downloader

import (
	"crypto/sha256"

	"bytes"
	"fmt"
	"io"

	"net/http"

	"context"
	"time"

	"net/http/httptest"
	"os"

	"errors"
	"strconv"
	"strings"

	"path/filepath"
	"slices"
	"testing"

	apperrors "github.com/eoctet/tendkit/pkg/errors"
	"syscall"
)

func newAria2LogWriter(target io.Writer) *aria2LogWriter {
	return newAria2ProgressWriter(target, nil)
}

type logBufferTestSink func([]byte) (int, error)

func (sink logBufferTestSink) Write(data []byte) (int, error) { return sink(data) }

func testDownloaderSettings(binary, directory string) Settings {
	return Settings{
		CLI: binary, StorePath: directory,
	}
}

func testDownloader(binary, directory string) Downloader {
	return Downloader{Settings: testDownloaderSettings(binary, directory)}
}
func TestDownloaderFlow(t *testing.T) {
	t.Run("download-cancellation-kills-aria-process-group", func(t *testing.T) {
		directory := t.TempDir()
		binary := filepath.Join(directory, "aria2c")
		pidFile := filepath.Join(directory, "child.pid")
		script := "#!/bin/sh\ntrap '' TERM\n( trap '' TERM; while :; do sleep 1; done ) &\nchild=$!\nprintf '%s' \"$child\" > \"$ARIA_CHILD_PID\"\nwait\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ARIA_CHILD_PID", pidFile)
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() {
			Downloader := testDownloader(binary, directory)
			Downloader.terminationGrace = 100 * time.Millisecond
			_, err := Downloader.Download(
				ctx, Spec{URL: "https://example.invalid/application.zip", Filename: "application.zip"}, nil,
			)
			finished <- err
		}()
		deadline := time.Now().Add(time.Second)
		var childPID int
		for childPID == 0 && time.Now().Before(deadline) {
			data, err := os.ReadFile(pidFile)
			if err == nil {
				childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			}
			if childPID == 0 {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if childPID == 0 {
			cancel()
			t.Fatal("fake aria2c child did not start")
		}
		cancel()
		if err := <-finished; err != context.Canceled {
			t.Fatalf("download cancellation error = %v", err)
		}
		for syscall.Kill(childPID, 0) == nil && time.Now().Before(deadline.Add(time.Second)) {
			time.Sleep(10 * time.Millisecond)
		}
		if syscall.Kill(childPID, 0) == nil {
			t.Fatal("aria2c descendant survived cancellation")
		}
	})
	t.Run("download-builds-aria-arguments", func(t *testing.T) {
		dir := t.TempDir()
		binary := filepath.Join(dir, "aria2c")
		capture := filepath.Join(dir, "args.txt")
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CAPTURE_ARGS", capture)
		downloadDir := filepath.Join(dir, "downloads")
		d := testDownloader(binary, downloadDir)
		d.Settings.ExtraArgs = []string{"--summary-interval=1", "--console-log-level=notice", "--download-result=hide", "--enable-color=false"}
		result, err := d.Download(context.Background(), Spec{
			URL: "https://example.com/v{latest_version}/application.zip", Filename: "application-{latest_version}.zip",
			ExtraArgs: []string{"--summary-interval=5", "--retry-wait=2"},
		}, map[string]string{"latest_version": "2.0.0"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Path != filepath.Join(downloadDir, "application-2.0.0.zip") || result.Checksum != ChecksumDisabled {
			t.Fatalf("unexpected download result %#v", result)
		}
		data, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		args := string(data)
		for _, expected := range []string{"--summary-interval=5\n", "--console-log-level=notice\n", "--download-result=hide\n", "--enable-color=false\n", "--retry-wait=2\n", downloadDir + "\n", "application-2.0.0.zip\n", "https://example.com/v2.0.0/application.zip\n"} {
			if !strings.Contains(args, expected) {
				t.Fatalf("missing %q in %q", expected, args)
			}
		}
		if strings.Contains(args, "--summary-interval=1\n") {
			t.Fatalf("application option did not override global default: %q", args)
		}
	})
	t.Run("download-builds-curl-arguments-and-reports-progress", func(t *testing.T) {
		dir := t.TempDir()
		binary := filepath.Join(dir, "curl")
		capture := filepath.Join(dir, "args.txt")
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    --output) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nprintf '  %% Total    %% Received %% Xferd  Average Speed   Time    Time     Time  Current\\r' >&2\nprintf ' 50 10 50 5 0 0 5 0 0:00:02 0:00:01 0:00:01 5\\r' >&2\nprintf '100 10 100 10 0 0 10 0 0:00:01 0:00:01 --:--:-- 10\\n' >&2\nprintf payload > \"$out\"\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CAPTURE_ARGS", capture)
		downloadDir := filepath.Join(dir, "downloads")
		d := testDownloader(binary, downloadDir)
		d.Settings.ExtraArgs = []string{"--retry=1", "--connect-timeout=10"}
		var progress []int
		d.Progress = func(event Progress) { progress = append(progress, event.Percent) }
		result, err := d.Download(context.Background(), Spec{
			URL: "https://example.com/application.zip", Filename: "application.zip",
			ExtraArgs: []string{"--retry=3"},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Path != filepath.Join(downloadDir, "application.zip") {
			t.Fatalf("unexpected download result %#v", result)
		}
		data, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		args := string(data)
		for _, expected := range []string{"--disable\n", "--retry=3\n", "--connect-timeout=10\n", "--fail\n", "--location\n", "--proto\n", "=http,https\n", "--proto-redir\n", "--output\n", result.Path + "\n", "--url\n", "https://example.com/application.zip\n"} {
			if !strings.Contains(args, expected) {
				t.Fatalf("missing %q in %q", expected, args)
			}
		}
		if strings.Contains(args, "--retry=1\n") {
			t.Fatalf("application option did not override global default: %q", args)
		}
		if !slices.Equal(progress, []int{50, 100}) {
			t.Fatalf("curl progress = %v, want [50 100]", progress)
		}
	})
	t.Run("download-reports-aria-progress-without-logging-progress-lines", func(t *testing.T) {
		dir := t.TempDir()
		binary := filepath.Join(dir, "aria2c")
		script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nprintf '[#abc 5MiB/10MiB(50%%) CN:1 DL:1MiB]\\r'\nprintf '[#abc 10MiB/10MiB(100%%) CN:1 DL:1MiB]\\n'\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		var progress []int
		d := testDownloader(binary, dir)
		d.Output = &output
		d.Progress = func(event Progress) { progress = append(progress, event.Percent) }
		if _, err := d.Download(context.Background(), Spec{URL: "https://example.invalid/file", Filename: "file"}, nil); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(progress, []int{50, 100}) {
			t.Fatalf("aria2 progress = %v, want [50 100]", progress)
		}
		if strings.Contains(output.String(), "[#abc") {
			t.Fatalf("progress leaked into ordinary command output: %q", output.String())
		}
	})
	t.Run("download-rejects-security-sensitive-extra-args", func(t *testing.T) {
		for _, argument := range []string{"--dir=/tmp/override", "--out=other", "--on-download-complete=/tmp/hook", "--enable-rpc=true", "https://example.invalid/second"} {
			_, err := testDownloader("aria2c", t.TempDir()).Download(context.Background(), Spec{
				URL: "https://example.invalid/file", Filename: "file", ExtraArgs: []string{argument},
			}, nil)
			var form *apperrors.ExtraArgumentFormError
			var unsafe *apperrors.UnsafeExtraArgumentError
			if err == nil || !errors.As(err, &form) && !errors.As(err, &unsafe) {
				t.Errorf("unsafe argument %q was accepted: %v", argument, err)
			}
		}
	})
	t.Run("downloader-rejects-non-http-source", func(t *testing.T) {
		_, err := testDownloader("/missing", t.TempDir()).Download(
			context.Background(), Spec{URL: "file:///etc/passwd", Filename: "passwd"}, nil,
		)
		var typed *DownloaderError
		if !errors.As(err, &typed) || typed.Key != "download.url_invalid" {
			t.Fatalf("expected non-HTTP URL rejection, got %v", err)
		}
	})
	t.Run("download-verifies-configured-sha256", func(t *testing.T) {
		dir := t.TempDir()
		binary := filepath.Join(dir, "aria2c")
		script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte("payload")))
		result, err := testDownloader(binary, dir).Download(context.Background(), Spec{
			URL: "https://example.invalid/file", Filename: "file", ChecksumEnabled: true, ChecksumValue: strings.ToUpper(digest),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Checksum != ChecksumVerified || result.SHA256 != digest {
			t.Fatalf("download was not verified: %#v", result)
		}
	})
	t.Run("expected-checksum-uses-explicit-value-before-provider-error", func(t *testing.T) {
		digest := strings.Repeat("a", 64)
		Downloader := Downloader{ChecksumError: errors.New("provider digest unavailable")}
		got, err := Downloader.expectedChecksum(context.Background(), Spec{ChecksumValue: digest}, nil, "file")
		if err != nil || got != digest {
			t.Fatalf("expectedChecksum() = %q, %v", got, err)
		}
	})
	t.Run("download-rejects-mismatched-sha256", func(t *testing.T) {
		dir := t.TempDir()
		binary := filepath.Join(dir, "aria2c")
		script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		result, err := testDownloader(binary, dir).Download(context.Background(), Spec{
			URL: "https://example.invalid/file", Filename: "file", ChecksumEnabled: true, ChecksumValue: strings.Repeat("0", 64),
		}, nil)
		if err != nil || result.Checksum != ChecksumFailed {
			t.Fatalf("expected checksum mismatch, got %#v, %v", result, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "file")); !os.IsNotExist(statErr) {
			t.Fatalf("mismatched download was not removed: %v", statErr)
		}
	})
	t.Run("download-verifies-checksum-url-with-retry", func(t *testing.T) {
		dir := t.TempDir()
		binary := filepath.Join(dir, "aria2c")
		script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte("payload")))
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests++
			if requests == 1 {
				http.Error(w, "retry", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = fmt.Fprintf(w, "%s  file\n", digest)
		}))
		defer server.Close()
		Downloader := testDownloader(binary, dir)
		Downloader.FetchText = func(ctx context.Context, endpoint string) ([]byte, error) {
			var lastErr error
			for attempt := 0; attempt < 2; attempt++ {
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
				response, err := server.Client().Do(request)
				if err != nil {
					lastErr = err
					continue
				}
				body, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if response.StatusCode == http.StatusOK && readErr == nil && closeErr == nil {
					return body, nil
				}
				lastErr = errors.Join(readErr, closeErr, fmt.Errorf("status %d", response.StatusCode))
			}
			return nil, lastErr
		}
		result, err := Downloader.Download(context.Background(), Spec{
			URL: "https://example.invalid/file", Filename: "file", ChecksumEnabled: true, ChecksumURL: server.URL,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if requests != 2 || result.Checksum != ChecksumVerified || result.SHA256 != digest {
			t.Fatalf("unexpected checksum result %#v after %d requests", result, requests)
		}
	})
	t.Run("download-ignores-unreadable-checksum", func(t *testing.T) {
		dir := t.TempDir()
		binary := filepath.Join(dir, "aria2c")
		script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		Downloader := testDownloader(binary, dir)
		Downloader.FetchText = func(context.Context, string) ([]byte, error) {
			return nil, errors.New("not plain text")
		}
		result, err := Downloader.Download(
			context.Background(), Spec{URL: "https://example.invalid/file", Filename: "file", ChecksumEnabled: true, ChecksumURL: "https://example.invalid/checksum"}, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Checksum != ChecksumIgnored || result.ChecksumError == nil {
			t.Fatalf("checksum read failure was not ignored: %#v", result)
		}
		if _, err := os.Stat(filepath.Join(dir, "file")); err != nil {
			t.Fatalf("ignored checksum removed the downloaded file: %v", err)
		}
	})
	t.Run("aria2-log-writer-normalizes-console-output", func(t *testing.T) {
		var output bytes.Buffer
		writer := newAria2LogWriter(&output)
		chunks := []string{
			"08/14 12:40:37 \x1b[32m[NOTICE]\x1b[0m Downloading 1 item(s)\r[#aa02c0 0B/0B",
			" CN:1 DL:0B]\r[#aa02c0 0B/0B CN:1 DL:0B]\r",
			"*** Download Progress Summary as of Thu Aug 14 12:40:38 2026 ***\n========================\nFILE: /tmp/application.zip\n------------------------\n",
			"08/14 12:40:39 [NOTICE] Allocating disk space. Use --file-allocation=none to disable it.\n",
			"download completed",
		}
		for _, chunk := range chunks {
			if _, err := writer.Write([]byte(chunk)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{
			"Downloading 1 item(s)",
			"Allocating disk space. Use --file-allocation=none to disable it.",
			"download completed",
			"",
		}, "\n")
		if output.String() != want {
			t.Fatalf("normalized output = %q, want %q", output.String(), want)
		}
	})
	t.Run("aria2-log-writer-bounds-unterminated-output", func(t *testing.T) {
		var output bytes.Buffer
		writer := newAria2LogWriter(&output)
		if _, err := writer.Write([]byte(strings.Repeat("x", aria2MaxLogLineBytes+10))); err != nil {
			t.Fatal(err)
		}
		if len(writer.buffer) > aria2MaxLogLineBytes {
			t.Fatalf("buffer grew to %d bytes", len(writer.buffer))
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "…") {
			t.Fatalf("bounded output lacks truncation marker: %q", output.String())
		}
	})
}
func TestDownloaderLogBufferContract(t *testing.T) {
	type logWriter interface {
		io.Writer
		Flush() error
	}
	for _, adapter := range []struct {
		name      string
		newWriter func(io.Writer) logWriter
	}{
		{"aria2", func(target io.Writer) logWriter { return newAria2ProgressWriter(target, nil) }},
		{"curl", func(target io.Writer) logWriter { return newCurlProgressWriter(target, nil) }},
	} {
		t.Run(adapter.name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				chunks []string
				want   string
			}{
				{"empty", []string{""}, ""},
				{"mixed-delimiters", []string{"one\rtwo\nthree\r\nfour"}, "one\ntwo\nthree\nfour\n"},
				{"split-crlf", []string{"one\r", "\ntw", "o\n", "tail"}, "one\ntwo\ntail\n"},
				{"exact-limit", []string{strings.Repeat("x", aria2MaxLogLineBytes)}, strings.Repeat("x", aria2MaxLogLineBytes) + "\n"},
				{"over-limit", []string{strings.Repeat("x", aria2MaxLogLineBytes) + "tail"}, strings.Repeat("x", aria2MaxLogLineBytes) + "…\ntail\n"},
				{"utf8-byte-boundary", []string{strings.Repeat("x", aria2MaxLogLineBytes-1) + "中tail"}, strings.Repeat("x", aria2MaxLogLineBytes-1) + "�…\n\xb8\xadtail\n"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var output bytes.Buffer
					writer := adapter.newWriter(&output)
					for _, chunk := range tc.chunks {
						if n, err := writer.Write([]byte(chunk)); err != nil || n != len(chunk) {
							t.Fatalf("Write = %d, %v", n, err)
						}
					}
					for range 2 {
						if err := writer.Flush(); err != nil {
							t.Fatal(err)
						}
					}
					if output.String() != tc.want {
						t.Fatalf("output = %q, want %q", output.String(), tc.want)
					}
				})
			}
			for _, phase := range []string{"line", "chunk", "flush"} {
				t.Run("failure/"+phase, func(t *testing.T) {
					failure := errors.New("sink failure")
					var output bytes.Buffer
					fail := true
					writer := adapter.newWriter(logBufferTestSink(func(data []byte) (int, error) {
						if fail {
							return 0, failure
						}
						return output.Write(data)
					}))
					input := "lost\nkept"
					if phase == "chunk" {
						input = strings.Repeat("x", aria2MaxLogLineBytes) + "kept"
					}
					if phase == "flush" {
						input = "lost"
					}
					n, err := writer.Write([]byte(input))
					if phase == "flush" {
						if err != nil || n != len(input) {
							t.Fatalf("Write = %d, %v", n, err)
						}
						err = writer.Flush()
					} else if n != 0 {
						t.Fatalf("failed Write count = %d", n)
					}
					if !errors.Is(err, failure) {
						t.Fatalf("error = %v", err)
					}
					fail = false
					if err := writer.Flush(); err != nil {
						t.Fatal(err)
					}
					want := "kept\n"
					if phase == "flush" {
						want = ""
					}
					if output.String() != want {
						t.Fatalf("remaining output = %q, want %q", output.String(), want)
					}
				})
			}
		})
	}
}

func TestDownloaderArgumentContract(t *testing.T) {
	t.Run("merge-downloader-extra-args-overrides-and-appends-application-options", func(t *testing.T) {
		for _, test := range []struct {
			name        string
			defaults    []string
			application []string
			want        []string
		}{
			{
				name: "override and append", defaults: []string{"--summary-interval=1", "--console-log-level=notice"},
				application: []string{"--summary-interval=5", "--retry-wait=2"},
				want:        []string{"--summary-interval=5", "--console-log-level=notice", "--retry-wait=2"},
			},
			{
				name: "override transfer options", defaults: []string{"--continue=true", "--split=16", "--max-connection-per-server=4"},
				application: []string{"--continue=false", "--split=1", "--max-connection-per-server=1"},
				want:        []string{"--continue=false", "--split=1", "--max-connection-per-server=1"},
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				got, err := MergeDownloaderExtraArgs(DownloaderAria2, test.defaults, test.application)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, test.want) {
					t.Fatalf("merged arguments = %v, want %v", got, test.want)
				}
			})
		}
	})
	t.Run("validate-downloader-extra-args-rejects-program-controlled-options", func(t *testing.T) {
		tests := map[DownloaderKind][]string{
			DownloaderAria2: {
				"--dir=/tmp", "--out=artifact", "--allow-overwrite=true", "--auto-file-renaming=false", "--checksum=sha-256=deadbeef",
				"--enable-rpc=true", "--on-download-complete=hook", "--conf-path=config", "--input-file=queue", "--log=aria2.log", "--save-session=session",
			},
			DownloaderCurl: {
				"--output=/tmp/file", "--output-dir=/tmp", "--remote-name=true", "--config=/tmp/curlrc", "--url=https://example.invalid",
				"--write-out=%{json}", "--progress-bar=true", "--no-progress-meter=true", "--stderr=/tmp/log", "--upload-file=/tmp/file",
			},
		}
		for kind, arguments := range tests {
			for _, argument := range arguments {
				if err := ValidateDownloaderExtraArgs(kind, []string{argument}); err == nil {
					t.Fatalf("%s program-controlled option %q was accepted", kind, argument)
				}
			}
		}
	})
	t.Run("validate-downloader-extra-args-rejects-curl-options-by-safety-category", func(t *testing.T) {
		for category, arguments := range map[string][]string{
			"request body or upload":  {"--json={\"name\":\"value\"}", "--data-urlencode=name=value", "--form-string=name=value", "--upload-file=file"},
			"fixed request target":    {"--url-query=name=value", "--request-target=/other", "--path-as-is"},
			"ambient credentials":     {"--netrc", "--netrc-file=/tmp/netrc", "--netrc-optional"},
			"redirect authentication": {"--location-trusted"},
			"side output":             {"--output=/tmp/file", "--dump-header=/tmp/headers", "--trace=/tmp/trace"},
			"connection rewrite":      {"--unix-socket=/tmp/socket", "--abstract-unix-socket=name", "--resolve=example.invalid:443:127.0.0.1", "--connect-to=example.invalid:443:127.0.0.1:443"},
		} {
			for _, argument := range arguments {
				if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err == nil {
					t.Errorf("curl %s option %q was accepted", category, argument)
				}
			}
		}
		if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{"--connect-timeout=15", "--retry=2"}); err != nil {
			t.Fatalf("safe curl options were rejected: %v", err)
		}
	})
	t.Run("validate-downloader-extra-args-uses-curl-transfer-tuning-allowlist", func(t *testing.T) {
		for _, argument := range []string{
			"--retry=3", "--retry-all-errors", "--retry-delay=2", "--retry-max-time=60",
			"--connect-timeout=10", "--max-time=120", "--speed-limit=1024", "--speed-time=30",
			"--limit-rate=1M", "--parallel", "--parallel-max=4", "--keepalive-time=30",
		} {
			if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err != nil {
				t.Fatalf("safe curl transfer tuning option %q was rejected: %v", argument, err)
			}
		}
		for category, arguments := range map[string][]string{
			"output and metadata":               {"--no-clobber", "--remote-name-all", "--create-file-mode=0600", "--xattr"},
			"credentials and request injection": {"--cookie=/tmp/cookie", "--header=@file", "--proxy-header=@file"},
			"unknown options":                   {"--future-curl-option=value", "--unexpected"},
		} {
			for _, argument := range arguments {
				if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err == nil {
					t.Errorf("curl %s option %q was accepted", category, argument)
				}
			}
		}
	})
	t.Run("downloader-kind-from-cli-uses-basename", func(t *testing.T) {
		for cli, want := range map[string]DownloaderKind{
			"aria2c": DownloaderAria2, "/opt/homebrew/bin/aria2c": DownloaderAria2,
			"curl": DownloaderCurl, "/usr/bin/curl": DownloaderCurl,
		} {
			got, err := DownloaderKindFromCLI(cli)
			if err != nil || got != want {
				t.Fatalf("DownloaderKindFromCLI(%q) = %q, %v; want %q", cli, got, err, want)
			}
		}
		if _, err := DownloaderKindFromCLI("wget"); err == nil {
			t.Fatal("unsupported downloader was accepted")
		}
	})
	t.Run("validate-downloader-extra-args-rejects-arguments-for-another-adapter", func(t *testing.T) {
		for _, argument := range []string{
			"--split=16", "--max-connection-per-server=4",
		} {
			if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err == nil {
				t.Fatalf("aria2 option %q was accepted for curl", argument)
			}
		}
	})
	t.Run("validate-downloader-extra-args-rejects-split-option-tokens", func(t *testing.T) {
		for _, argument := range []string{"--header X-Test: value", "--retry\n3", "https://example.invalid"} {
			if err := ValidateDownloaderExtraArgs(DownloaderCurl, []string{argument}); err == nil {
				t.Fatalf("malformed argument %q was accepted", argument)
			}
		}
	})
}
