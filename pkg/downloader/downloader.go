package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	apperrors "github.com/eoctet/tendkit/pkg/errors"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// DefaultDownloaderCLI is used when settings.downloader.cli is empty.
const DefaultDownloaderCLI = "aria2c"

// Settings selects the aria2c or curl adapter for every download.
type Settings struct {
	CLI       string   `json:"cli"`
	StorePath string   `json:"store_path"`
	ExtraArgs []string `json:"extra_args,omitempty"`
}

// Spec describes an artifact download and its optional integrity check.
type Spec struct {
	URL             string   `json:"url,omitempty"`
	Filename        string   `json:"filename,omitempty"`
	StorePath       string   `json:"store_path,omitempty"`
	ChecksumEnabled bool     `json:"checksum_enabled,omitempty"`
	ChecksumURL     string   `json:"checksum_url,omitempty"`
	ChecksumValue   string   `json:"checksum_value,omitempty"`
	ExtraArgs       []string `json:"extra_args,omitempty"`
}

// Progress is one presentation-independent artifact progress event.
type Progress struct {
	AppID   string `json:"app_id"`
	Name    string `json:"app_name"`
	Percent int    `json:"percent"`
}

type DownloaderKind string

const (
	DownloaderAria2 DownloaderKind = "aria2c"
	DownloaderCurl  DownloaderKind = "curl"
)

var forbiddenDownloaderOptions = map[DownloaderKind]map[string]bool{
	DownloaderAria2: {
		"--allow-overwrite": true, "--auto-file-renaming": true, "--checksum": true,
		"--conf-path": true, "--daemon": true, "--dir": true, "--enable-rpc": true,
		"--input-file": true, "--log": true, "--out": true, "--save-session": true,
	},
}

// curlTransferTuningOptions is intentionally an allowlist. curlAdapter fixes
// the URL, redirects, protocol policy, output path, and progress channel; only
// bounded retry, timeout, rate, and connection-concurrency tuning may be
// supplied by configuration. Unknown curl options fail closed.
var curlTransferTuningOptions = map[string]bool{
	"--connect-timeout": true, "--expect100-timeout": true, "--happy-eyeballs-timeout-ms": true,
	"--keepalive-time": true, "--limit-rate": true, "--max-time": true,
	"--parallel": true, "--parallel-max": true,
	"--retry": true, "--retry-all-errors": true, "--retry-delay": true, "--retry-max-time": true,
	"--speed-limit": true, "--speed-time": true,
}

func DownloaderKindFromCLI(cli string) (DownloaderKind, error) {
	switch filepath.Base(strings.TrimSpace(cli)) {
	case string(DownloaderAria2):
		return DownloaderAria2, nil
	case string(DownloaderCurl):
		return DownloaderCurl, nil
	default:
		return "", fmt.Errorf("unsupported downloader %q", cli)
	}
}

func ValidateDownloaderExtraArgs(kind DownloaderKind, arguments []string) error {
	for index, argument := range arguments {
		argument = strings.TrimSpace(argument)
		if !strings.HasPrefix(argument, "--") || strings.ContainsAny(argument, "\r\n") {
			return &apperrors.ExtraArgumentFormError{Index: index}
		}
		name := downloaderOptionName(argument)
		if !validDownloaderOptionName(name) || (kind != DownloaderCurl && forbiddenDownloaderOptions[kind] == nil) {
			return &apperrors.ExtraArgumentFormError{Index: index}
		}
		unsafe := forbiddenDownloaderOptions[kind][name]
		if kind == DownloaderAria2 {
			unsafe = unsafe || strings.HasPrefix(name, "--on-") || strings.HasPrefix(name, "--rpc-") ||
				strings.HasPrefix(name, "--server-stat-") || name == "--load-cookies" || name == "--save-cookies"
		} else if kind == DownloaderCurl {
			unsafe = !curlTransferTuningOptions[name]
		}
		if unsafe {
			return &apperrors.UnsafeExtraArgumentError{Index: index, Name: name}
		}
	}
	return nil
}

func MergeDownloaderExtraArgs(kind DownloaderKind, defaults, application []string) ([]string, error) {
	if err := ValidateDownloaderExtraArgs(kind, defaults); err != nil {
		return nil, err
	}
	if err := ValidateDownloaderExtraArgs(kind, application); err != nil {
		return nil, err
	}
	merged := make([]string, 0, len(defaults)+len(application))
	positions := make(map[string]int, len(defaults)+len(application))
	for _, argument := range append(append([]string(nil), defaults...), application...) {
		name := downloaderOptionName(argument)
		if position, exists := positions[name]; exists {
			merged[position] = strings.TrimSpace(argument)
			continue
		}
		positions[name] = len(merged)
		merged = append(merged, strings.TrimSpace(argument))
	}
	return merged, nil
}

func downloaderOptionName(argument string) string {
	name, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(argument)), "=")
	return name
}

func validDownloaderOptionName(name string) bool {
	if len(name) <= 2 || !strings.HasPrefix(name, "--") {
		return false
	}
	for _, character := range name[2:] {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

// Downloader executes the configured command adapter and optional SHA-256 verification.
type Downloader struct {
	Settings         Settings
	Output           io.Writer
	ErrorOutput      io.Writer
	FetchText        func(context.Context, string) ([]byte, error)
	Progress         func(Progress)
	ChecksumError    error
	terminationGrace time.Duration
}

// DownloaderError carries a stable presentation key without introducing a
// dependency from the public downloader package back to updater or UI packages.
type DownloaderError struct {
	Key   string
	Args  []any
	Cause error
}

func (err *DownloaderError) Error() string { return fmt.Sprintf("%s: %v", err.Key, err.Args) }
func (err *DownloaderError) Unwrap() error { return err.Cause }
func newDownloaderError(key string, args ...any) error {
	return &DownloaderError{Key: key, Args: args}
}
func wrapDownloaderError(key string, cause error, args ...any) error {
	return &DownloaderError{Key: key, Args: args, Cause: cause}
}

// ChecksumStatus describes the integrity-check outcome independently from the
// artifact download itself.
type ChecksumStatus string

const (
	ChecksumDisabled ChecksumStatus = "disabled"
	ChecksumVerified ChecksumStatus = "verified"
	ChecksumFailed   ChecksumStatus = "failed"
	ChecksumIgnored  ChecksumStatus = "ignored"
)

// DownloadResult records the local artifact and integrity outcome.
type DownloadResult struct {
	Path           string
	SHA256         string
	ExpectedSHA256 string
	Checksum       ChecksumStatus
	ChecksumError  error
}

var (
	aria2ANSISequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	aria2LogPrefix    = regexp.MustCompile(`^(?:\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}\s+)?\[(?:NOTICE|INFO|WARN|WARNING|ERROR|DEBUG)\]\s*`)
	aria2SummaryRule  = regexp.MustCompile(`^[=-]{8,}$`)
)

type aria2LogWriter struct {
	target   io.Writer
	buffer   string
	progress *progressReporter
}

const aria2MaxLogLineBytes = 64 * 1024

func newAria2ProgressWriter(target io.Writer, progress *progressReporter) *aria2LogWriter {
	if target == nil {
		target = io.Discard
	}
	return &aria2LogWriter{target: target, progress: progress}
}

func (writer *aria2LogWriter) Write(data []byte) (int, error) {
	return writeLogBuffer(&writer.buffer, data, writer.emit)
}

func (writer *aria2LogWriter) Flush() error {
	return flushLogBuffer(&writer.buffer, writer.emit)
}

// writeLogBuffer consumes buffered bytes before emitting, including on failure.
func writeLogBuffer(buffer *string, data []byte, emit func(string) error) (int, error) {
	written := len(data)
	*buffer += string(data)
	for {
		index := strings.IndexAny(*buffer, "\r\n")
		if index < 0 {
			break
		}
		line := (*buffer)[:index]
		next := index + 1
		if next < len(*buffer) && (*buffer)[index] == '\r' && (*buffer)[next] == '\n' {
			next++
		}
		*buffer = (*buffer)[next:]
		if err := emit(line); err != nil {
			return 0, err
		}
	}
	for len(*buffer) > aria2MaxLogLineBytes {
		chunk := strings.ToValidUTF8((*buffer)[:aria2MaxLogLineBytes], "�") + "…"
		*buffer = (*buffer)[aria2MaxLogLineBytes:]
		if err := emit(chunk); err != nil {
			return 0, err
		}
	}
	return written, nil
}

func flushLogBuffer(buffer *string, emit func(string) error) error {
	if *buffer == "" {
		return nil
	}
	line := *buffer
	*buffer = ""
	return emit(line)
}

func (writer *aria2LogWriter) emit(line string) error {
	line = aria2ANSISequence.ReplaceAllString(line, "")
	line = strings.TrimSpace(aria2LogPrefix.ReplaceAllString(strings.TrimSpace(line), ""))
	if line == "" || strings.HasPrefix(line, "*** Download Progress Summary") || strings.HasPrefix(line, "FILE:") || aria2SummaryRule.MatchString(line) {
		return nil
	}
	if len(line) > aria2MaxLogLineBytes {
		line = strings.ToValidUTF8(line[:aria2MaxLogLineBytes], "�") + "…"
	}
	if strings.HasPrefix(line, "[#") {
		if percent, ok := aria2ProgressPercent(line); ok {
			writer.progress.report(percent)
		}
		return nil
	}
	_, err := io.WriteString(writer.target, line+"\n")
	return err
}

var aria2ProgressPercentPattern = regexp.MustCompile(`\(([0-9]{1,3})%\)`)

func aria2ProgressPercent(line string) (int, bool) {
	if !strings.HasPrefix(line, "[#") {
		return 0, false
	}
	match := aria2ProgressPercentPattern.FindStringSubmatch(line)
	if len(match) != 2 {
		return 0, false
	}
	percent, err := strconv.Atoi(match[1])
	return percent, err == nil && percent >= 0 && percent <= 100
}

type progressReporter struct {
	mu       sync.Mutex
	last     int
	reported bool
	callback func(Progress)
}

func (reporter *progressReporter) report(percent int) {
	if reporter == nil || reporter.callback == nil || percent < 0 || percent > 100 {
		return
	}
	reporter.mu.Lock()
	if reporter.reported && reporter.last == percent {
		reporter.mu.Unlock()
		return
	}
	reporter.last, reporter.reported = percent, true
	callback := reporter.callback
	reporter.mu.Unlock()
	callback(Progress{Percent: percent})
}

type curlProgressWriter struct {
	target   io.Writer
	buffer   string
	progress *progressReporter
}

func newCurlProgressWriter(target io.Writer, progress *progressReporter) *curlProgressWriter {
	if target == nil {
		target = io.Discard
	}
	return &curlProgressWriter{target: target, progress: progress}
}

func (writer *curlProgressWriter) Write(data []byte) (int, error) {
	return writeLogBuffer(&writer.buffer, data, writer.emit)
}

func (writer *curlProgressWriter) Flush() error {
	return flushLogBuffer(&writer.buffer, writer.emit)
}

func (writer *curlProgressWriter) emit(line string) error {
	line = strings.TrimSpace(line)
	if line == "" || strings.Contains(line, "% Total") || strings.HasPrefix(line, "Dload  Upload") {
		return nil
	}
	if percent, ok := curlProgressPercent(line); ok {
		writer.progress.report(percent)
		return nil
	}
	if len(line) > aria2MaxLogLineBytes {
		line = strings.ToValidUTF8(line[:aria2MaxLogLineBytes], "�") + "…"
	}
	_, err := io.WriteString(writer.target, line+"\n")
	return err
}

func curlProgressPercent(line string) (int, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return 0, false
	}
	percent, err := strconv.Atoi(fields[2])
	if err != nil || percent < 0 || percent > 100 {
		return 0, false
	}
	if totalPercent, err := strconv.Atoi(fields[0]); err != nil || totalPercent < 0 || totalPercent > 100 {
		return 0, false
	}
	return percent, true
}

type downloadAdapter interface {
	arguments(directory, filename, endpoint string, extraArgs []string) []string
	streams(output, errorOutput io.Writer, progress *progressReporter) (io.Writer, io.Writer, func() error)
}

type aria2Adapter struct{}

func (aria2Adapter) arguments(directory, filename, endpoint string, extraArgs []string) []string {
	args := append([]string(nil), extraArgs...)
	return append(args, "--allow-overwrite=true", "-d", directory, "-o", filename, "--", endpoint)
}

func (aria2Adapter) streams(output, errorOutput io.Writer, progress *progressReporter) (io.Writer, io.Writer, func() error) {
	stdout := newAria2ProgressWriter(output, progress)
	stderr := newAria2ProgressWriter(errorOutput, progress)
	return stdout, stderr, func() error { return errors.Join(stdout.Flush(), stderr.Flush()) }
}

type curlAdapter struct{}

func (curlAdapter) arguments(directory, filename, endpoint string, extraArgs []string) []string {
	// --disable must be the first argument so curl cannot load an ambient curlrc.
	args := append([]string{"--disable"}, extraArgs...)
	path := filepath.Join(directory, filename)
	return append(args, "--fail", "--location", "--proto", "=http,https", "--proto-redir", "=http,https", "--output", path, "--url", endpoint)
}

func (curlAdapter) streams(output, errorOutput io.Writer, progress *progressReporter) (io.Writer, io.Writer, func() error) {
	if output == nil {
		output = io.Discard
	}
	stderr := newCurlProgressWriter(errorOutput, progress)
	return output, stderr, stderr.Flush
}

func adapterFor(kind DownloaderKind) downloadAdapter {
	if kind == DownloaderCurl {
		return curlAdapter{}
	}
	return aria2Adapter{}
}

// Download renders and downloads one artifact while respecting cancellation.
func (d Downloader) Download(ctx context.Context, spec Spec, values map[string]string) (DownloadResult, error) {
	endpoint, err := runtimeutil.Render(spec.URL, values, false)
	if err != nil {
		return DownloadResult{}, err
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || (parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") || parsedEndpoint.Host == "" {
		return DownloadResult{}, newDownloaderError("download.url_invalid")
	}
	filename, err := runtimeutil.Render(spec.Filename, values, false)
	if err != nil {
		return DownloadResult{}, err
	}
	if filename == "" {
		parts := strings.Split(strings.TrimRight(endpoint, "/"), "/")
		filename = parts[len(parts)-1]
		if query := strings.IndexByte(filename, '?'); query >= 0 {
			filename = filename[:query]
		}
	}
	filename = filepath.Base(filename)
	if filename == "." || filename == "" {
		return DownloadResult{}, newDownloaderError("download.filename_unknown")
	}
	directory := d.Settings.StorePath
	if strings.TrimSpace(spec.StorePath) != "" {
		directory = spec.StorePath
	}
	directory = runtimeutil.ExpandPath(directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return DownloadResult{}, err
	}
	binary := strings.TrimSpace(d.Settings.CLI)
	if binary == "" {
		binary = DefaultDownloaderCLI
	}
	kind, err := DownloaderKindFromCLI(binary)
	if err != nil {
		return DownloadResult{}, err
	}
	extraArgs, err := MergeDownloaderExtraArgs(kind, d.Settings.ExtraArgs, spec.ExtraArgs)
	if err != nil {
		return DownloadResult{}, err
	}
	adapter := adapterFor(kind)
	args := adapter.arguments(directory, filename, endpoint, extraArgs)
	// #nosec G204 -- The executable path is trusted configuration whose basename must be aria2c or curl; arguments bypass a shell and are adapter-validated.
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	terminationGrace := d.terminationGrace
	if terminationGrace <= 0 {
		terminationGrace = runtimeutil.ProcessTerminationGracePeriod
	}
	cmd.Cancel = func() error {
		runtimeutil.TerminateProcessGroup(cmd, terminationGrace)
		return nil
	}
	cmd.WaitDelay = terminationGrace
	reporter := &progressReporter{callback: d.Progress}
	stdout, stderr, flush := adapter.streams(d.Output, d.ErrorOutput, reporter)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	flushErr := flush()
	if runErr != nil && ctx.Err() != nil {
		return DownloadResult{}, ctx.Err()
	}
	if err := errors.Join(runErr, flushErr); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return DownloadResult{}, contextErr
		}
		return DownloadResult{}, wrapDownloaderError("download.failed", err, binary)
	}
	path := filepath.Join(directory, filename)
	result := DownloadResult{Path: path, Checksum: ChecksumDisabled}
	if !spec.ChecksumEnabled {
		return result, nil
	}
	expected, checksumErr := d.expectedChecksum(ctx, spec, values, filename)
	if checksumErr != nil {
		result.Checksum = ChecksumIgnored
		result.ChecksumError = checksumErr
		return result, nil
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return DownloadResult{}, wrapDownloaderError("download.hash_failed", err, path)
	}
	result.SHA256 = digest
	result.ExpectedSHA256 = expected
	if digest == expected {
		result.Checksum = ChecksumVerified
		return result, nil
	}
	result.Checksum = ChecksumFailed
	if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return DownloadResult{}, errors.Join(
			newDownloaderError("download.checksum_mismatch", expected, digest),
			wrapDownloaderError("download.remove_unverified", removeErr, path),
		)
	}
	return result, nil
}

func (d Downloader) expectedChecksum(ctx context.Context, spec Spec, values map[string]string, filename string) (string, error) {
	configured, err := runtimeutil.Render(spec.ChecksumValue, values, false)
	if err != nil {
		return "", err
	}
	if configured != "" {
		return normalizeSHA256(configured)
	}
	endpoint, err := runtimeutil.Render(spec.ChecksumURL, values, false)
	if err != nil {
		return "", err
	}
	if endpoint == "" {
		if d.ChecksumError != nil {
			return "", d.ChecksumError
		}
		return "", newDownloaderError("download.checksum_unavailable")
	}
	if d.FetchText == nil {
		return "", newDownloaderError("download.checksum_unavailable")
	}
	body, err := d.FetchText(ctx, endpoint)
	if err != nil {
		return "", err
	}
	return checksumFromText(body, filename)
}

func checksumFromText(body []byte, filename string) (string, error) {
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return "", newDownloaderError("download.checksum_text_invalid")
	}
	var fallback string
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		digest, err := normalizeSHA256(fields[0])
		if err != nil {
			continue
		}
		if len(fields) == 1 {
			if fallback == "" {
				fallback = digest
			}
			continue
		}
		listedName := filepath.Base(strings.TrimPrefix(fields[len(fields)-1], "*"))
		if listedName == filename {
			return digest, nil
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", newDownloaderError("download.checksum_text_missing", filename)
}

func normalizeSHA256(value string) (string, error) {
	digest := strings.ToLower(strings.TrimSpace(value))
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", newDownloaderError("download.checksum_value_invalid")
	}
	return digest, nil
}

func fileSHA256(path string) (string, error) {
	// #nosec G304 -- The path is the validated destination file produced by the downloader before checksum verification.
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
