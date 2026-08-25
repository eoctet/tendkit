package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	apperrors "github.com/eoctet/tendkit/pkg/errors"
)

func testDownloaderSettings(binary, directory string) Settings {
	return Settings{
		CLI: binary, StorePath: directory,
	}
}

func testDownloader(binary, directory string) Downloader {
	return Downloader{Settings: testDownloaderSettings(binary, directory)}
}

func TestDownloadCancellationKillsAriaProcessGroup(t *testing.T) {
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
}

func TestDownloadBuildsAriaArguments(t *testing.T) {
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
}

func TestDownloadBuildsCurlArgumentsAndReportsProgress(t *testing.T) {
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
}

func TestDownloadReportsAriaProgressWithoutLoggingProgressLines(t *testing.T) {
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
}

func TestDownloadRejectsSecuritySensitiveExtraArgs(t *testing.T) {
	for _, argument := range []string{"--dir=/tmp/override", "--out=other", "--on-download-complete=/tmp/hook", "--enable-rpc=true", "https://example.invalid/second"} {
		t.Run(argument, func(t *testing.T) {
			_, err := testDownloader("aria2c", t.TempDir()).Download(context.Background(), Spec{
				URL: "https://example.invalid/file", Filename: "file", ExtraArgs: []string{argument},
			}, nil)
			var form *apperrors.ExtraArgumentFormError
			var unsafe *apperrors.UnsafeExtraArgumentError
			if err == nil || !errors.As(err, &form) && !errors.As(err, &unsafe) {
				t.Fatalf("unsafe argument %q was accepted: %v", argument, err)
			}
		})
	}
}

func TestDownloaderRejectsNonHTTPSource(t *testing.T) {
	_, err := testDownloader("/missing", t.TempDir()).Download(
		context.Background(), Spec{URL: "file:///etc/passwd", Filename: "passwd"}, nil,
	)
	var typed *DownloaderError
	if !errors.As(err, &typed) || typed.Key != "download.url_invalid" {
		t.Fatalf("expected non-HTTP URL rejection, got %v", err)
	}
}

func TestDownloadVerifiesConfiguredSHA256(t *testing.T) {
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
}

func TestExpectedChecksumUsesExplicitValueBeforeProviderError(t *testing.T) {
	digest := strings.Repeat("a", 64)
	Downloader := Downloader{ChecksumError: errors.New("provider digest unavailable")}
	got, err := Downloader.expectedChecksum(context.Background(), Spec{ChecksumValue: digest}, nil, "file")
	if err != nil || got != digest {
		t.Fatalf("expectedChecksum() = %q, %v", got, err)
	}
}

func TestDownloadRejectsMismatchedSHA256(t *testing.T) {
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
}

func TestDownloadVerifiesChecksumURLWithRetry(t *testing.T) {
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
}

func TestDownloadIgnoresUnreadableChecksum(t *testing.T) {
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
}

func TestAria2LogWriterNormalizesConsoleOutput(t *testing.T) {
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
}

func TestAria2LogWriterBoundsUnterminatedOutput(t *testing.T) {
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
}
