package logger

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

const (
	runLogFile              = "run.log"
	maxLogSize              = 128 << 20
	maxLogFiles             = 5
	commandOutputBufferSize = 256 << 10
	commandFileBufferSize   = commandOutputBufferSize / 2
	commandRedactorMaxBytes = commandOutputBufferSize - commandFileBufferSize
	jsonEncodingChunkSize   = 32 << 10
)

var archiveName = regexp.MustCompile(`^run-\d{4}-\d{2}-\d{2}\.\d+\.log$`)
var commandOutputTempName = regexp.MustCompile(`^\.run-command-[0-9]+$`)

// NormalizeLevel accepts the five stable logging levels and returns uppercase.
func NormalizeLevel(value string) (string, error) {
	switch normalized := strings.ToUpper(strings.TrimSpace(value)); normalized {
	case "TRACE", "DEBUG", "INFO", "WARN", "ERROR":
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid log level %q", value)
	}
}

func levelRank(level string) int {
	switch level {
	case "TRACE":
		return 0
	case "DEBUG":
		return 1
	case "INFO":
		return 2
	case "WARN":
		return 3
	case "ERROR":
		return 4
	default:
		return 2
	}
}

type LogEntry struct {
	Time        string `json:"time"`
	RunID       string `json:"run_id"`
	Level       string `json:"level,omitempty"`
	Event       string `json:"event"`
	AppID       string `json:"app_id,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	Operation   string `json:"operation,omitempty"`
	Status      string `json:"status,omitempty"`
	Message     string `json:"message,omitempty"`
	Artifact    string `json:"artifact,omitempty"`
	Detail      string `json:"detail,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	TargetCount int    `json:"target_count,omitempty"`
	ResultCount int    `json:"result_count,omitempty"`
	FailedCount int    `json:"failed_count,omitempty"`
	WorkerCount int    `json:"worker_count,omitempty"`
}

// Logger is the sole file logging boundary. Its optional threshold defaults to DEBUG.
type Logger struct {
	dir, runID, threshold string
	mu                    sync.Mutex
	sensitive             map[string]struct{}
	activeDate            time.Time
}

func NewLogger(dir string, levels ...string) (*Logger, error) {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		return nil, fmt.Errorf("%s", i18n.T("log.directory_unsafe", dir))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := validateLogDirectory(dir); err != nil {
		return nil, err
	}
	if err := cleanupCommandOutputTemps(dir); err != nil {
		return nil, err
	}
	threshold := "DEBUG"
	if len(levels) > 0 && strings.TrimSpace(levels[0]) != "" {
		var err error
		threshold, err = NormalizeLevel(levels[0])
		if err != nil {
			return nil, err
		}
	}
	identifier := make([]byte, 8)
	if _, err := rand.Read(identifier); err != nil {
		return nil, err
	}
	l := &Logger{dir: dir, runID: hex.EncodeToString(identifier), threshold: threshold, sensitive: map[string]struct{}{}}
	if info, err := os.Stat(filepath.Join(dir, runLogFile)); err == nil {
		l.activeDate = info.ModTime().In(time.Local)
	}
	if err := l.cleanupLocked(); err != nil {
		return nil, err
	}
	l.AddSensitiveEnvironment(environmentMap(os.Environ()))
	return l, nil
}

func (l *Logger) AddSensitiveEnvironment(environment map[string]string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, value := range environment {
		if value != "" && runtimeutil.IsSensitiveEnvironmentKey(key) {
			l.sensitive[value] = struct{}{}
		}
	}
}

// Log writes a structured run-log entry at the requested level.
func (l *Logger) Log(level string, entry LogEntry) error {
	if l == nil {
		return nil
	}
	entry.Level = level
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writeLocked(entry)
}

// Trace writes a structured TRACE run-log entry.
func (l *Logger) Trace(entry LogEntry) error { return l.Log("TRACE", entry) }

// Debug writes a structured DEBUG run-log entry.
func (l *Logger) Debug(entry LogEntry) error { return l.Log("DEBUG", entry) }

// Info writes a structured INFO run-log entry.
func (l *Logger) Info(entry LogEntry) error { return l.Log("INFO", entry) }

// Warn writes a structured WARN run-log entry.
func (l *Logger) Warn(entry LogEntry) error { return l.Log("WARN", entry) }

// Error writes a structured ERROR run-log entry.
func (l *Logger) Error(entry LogEntry) error { return l.Log("ERROR", entry) }

// Operation formats a localized TEXT event for the TUI and stores one matching JSONL event.
func (l *Logger) Operation(level, operation, subject, message string) ([]string, error) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lines, normalized, operation, subject, message, err := l.operationTextLocked(level, operation, subject, message)
	if err != nil || lines == nil {
		return lines, err
	}
	err = l.writeLocked(LogEntry{Level: normalized, Event: "operation_log", Operation: operation, AppName: subject, Message: message})
	return lines, err
}

// OperationText applies the shared level filter, redaction, and TEXT formatting without persisting it.
func (l *Logger) OperationText(level, operation, subject, message string) ([]string, error) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lines, _, _, _, _, err := l.operationTextLocked(level, operation, subject, message)
	return lines, err
}

// FormatOperation formats and redacts an operation event without opening a log file.
func FormatOperation(level, threshold, operation, subject, message string, environments ...map[string]string) ([]string, error) {
	normalized, err := NormalizeLevel(level)
	if err != nil {
		return nil, err
	}
	minimum, err := NormalizeLevel(threshold)
	if err != nil {
		return nil, err
	}
	if levelRank(normalized) < levelRank(minimum) {
		return nil, nil
	}
	sensitive := map[string]struct{}{}
	for key, value := range environmentMap(os.Environ()) {
		if value != "" && runtimeutil.IsSensitiveEnvironmentKey(key) {
			sensitive[value] = struct{}{}
		}
	}
	for _, environment := range environments {
		for key, value := range environment {
			if value != "" && runtimeutil.IsSensitiveEnvironmentKey(key) {
				sensitive[value] = struct{}{}
			}
		}
	}
	return FormatOperationLines(time.Now(), normalized, RedactSensitiveValues(operation, sensitive), RedactSensitiveValues(subject, sensitive), RedactSensitiveValues(message, sensitive)), nil
}

// OperationOutputWriter streams one complete command into a single JSONL operation event.
func (l *Logger) OperationOutputWriter(level, operation, appID, appName string) (io.WriteCloser, error) {
	if l == nil {
		return discardWriteCloser{Writer: io.Discard}, nil
	}
	l.mu.Lock()
	normalized, operation, appName, _, err := l.operationFieldsLocked(level, operation, appName, "")
	if err != nil || normalized == "" {
		l.mu.Unlock()
		return discardWriteCloser{Writer: io.Discard}, err
	}
	entry := LogEntry{
		Time: time.Now().UTC().Format(time.RFC3339Nano), RunID: l.runID, Level: normalized,
		Event: "operation_log", Operation: operation, AppID: RedactSensitiveValues(appID, l.sensitive), AppName: appName,
	}
	sensitive := make([]string, 0, len(l.sensitive))
	for value := range l.sensitive {
		if len(value) > commandRedactorMaxBytes/2 {
			l.mu.Unlock()
			return discardWriteCloser{Writer: io.Discard}, nil
		}
		sensitive = append(sensitive, value)
	}
	l.mu.Unlock()
	return newOperationOutputWriter(l, entry, sensitive)
}

func (l *Logger) operationTextLocked(level, operation, subject, message string) ([]string, string, string, string, string, error) {
	normalized, operation, subject, message, err := l.operationFieldsLocked(level, operation, subject, message)
	if err != nil || normalized == "" {
		return nil, normalized, operation, subject, message, err
	}
	return FormatOperationLines(time.Now(), normalized, operation, subject, message), normalized, operation, subject, message, nil
}

func (l *Logger) operationFieldsLocked(level, operation, subject, message string) (string, string, string, string, error) {
	normalized, err := NormalizeLevel(level)
	if err != nil {
		return "", "", "", "", err
	}
	if levelRank(normalized) < levelRank(l.threshold) {
		return "", "", "", "", nil
	}
	return normalized, RedactSensitiveValues(operation, l.sensitive), RedactSensitiveValues(subject, l.sensitive), RedactSensitiveValues(message, l.sensitive), nil
}

func (l *Logger) writeLocked(entry LogEntry) error {
	level, err := NormalizeLevel(entry.Level)
	if err != nil {
		return err
	}
	if levelRank(level) < levelRank(l.threshold) {
		return nil
	}
	entry.Time, entry.RunID, entry.Level = time.Now().UTC().Format(time.RFC3339Nano), l.runID, level
	entry.Event = strings.ToLower(strings.TrimSpace(entry.Event))
	entry = redactExternalFields(entry, l.sensitive)
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := l.rotateLocked(time.Now(), int64(len(data)))
	if err != nil {
		return err
	}
	return writeAndClose(f, bytes.NewReader(data))
}

func redactExternalFields(entry LogEntry, sensitive map[string]struct{}) LogEntry {
	entry.AppID = RedactSensitiveValues(entry.AppID, sensitive)
	entry.AppName = RedactSensitiveValues(entry.AppName, sensitive)
	entry.Operation = RedactSensitiveValues(entry.Operation, sensitive)
	entry.Status = RedactSensitiveValues(entry.Status, sensitive)
	entry.Message = RedactSensitiveValues(entry.Message, sensitive)
	entry.Artifact = RedactSensitiveValues(entry.Artifact, sensitive)
	entry.Detail = RedactSensitiveValues(entry.Detail, sensitive)
	return entry
}

func (l *Logger) rotateLocked(now time.Time, nextSize int64) (*os.File, error) {
	path := filepath.Join(l.dir, runLogFile)
	f, err := openLogFile(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if l.activeDate.IsZero() {
		l.activeDate = info.ModTime().In(time.Local)
	}
	if l.activeDate.Format("2006-01-02") == now.In(time.Local).Format("2006-01-02") && (info.Size() == 0 || info.Size()+nextSize <= maxLogSize) {
		return f, nil
	}
	day := l.activeDate.In(time.Local).Format("2006-01-02")
	for sequence := 1; ; sequence++ {
		archive := filepath.Join(l.dir, fmt.Sprintf("run-%s.%d.log", day, sequence))
		if _, statErr := os.Lstat(archive); errors.Is(statErr, os.ErrNotExist) {
			current, pathErr := os.Lstat(path)
			if pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
				return nil, errors.Join(fmt.Errorf("%s", i18n.T("log.file_unsafe", path)), pathErr, f.Close())
			}
			if err := os.Rename(path, archive); err != nil {
				return nil, errors.Join(err, f.Close())
			}
			if err := f.Close(); err != nil {
				return nil, err
			}
			break
		} else if statErr != nil {
			return nil, errors.Join(statErr, f.Close())
		}
	}
	l.activeDate = now.In(time.Local)
	f, err = openLogFile(path)
	if err != nil {
		return nil, err
	}
	if err := l.cleanupLocked(); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func writeAndClose(file *os.File, source io.Reader) error {
	if _, err := io.Copy(file, source); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

type discardWriteCloser struct{ io.Writer }

func (discardWriteCloser) Close() error { return nil }

type operationOutputWriter struct {
	logger   *Logger
	file     *os.File
	path     string
	buffer   *bufio.Writer
	redactor *streamRedactor
	escaper  *jsonEscapeWriter
	mu       sync.Mutex
	closed   bool
	failed   error
}

func newOperationOutputWriter(log *Logger, entry LogEntry, sensitive []string) (*operationOutputWriter, error) {
	file, err := os.CreateTemp(log.dir, ".run-command-*")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	failed := func(err error) (*operationOutputWriter, error) {
		return nil, errors.Join(err, file.Close(), os.Remove(path))
	}
	if err := file.Chmod(0o600); err != nil {
		return failed(err)
	}
	entry.Message = ""
	prefix, err := json.Marshal(entry)
	if err != nil {
		return failed(err)
	}
	prefix = append(prefix[:len(prefix)-1], []byte(`,"message":"`)...)
	buffer := bufio.NewWriterSize(file, commandFileBufferSize)
	if _, err := buffer.Write(prefix); err != nil {
		return failed(err)
	}
	escaper := &jsonEscapeWriter{destination: buffer}
	return &operationOutputWriter{logger: log, file: file, path: path, buffer: buffer, escaper: escaper, redactor: newStreamRedactor(escaper, sensitive)}, nil
}

func (writer *operationOutputWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return 0, os.ErrClosed
	}
	if writer.failed != nil {
		return len(data), nil
	}
	written := 0
	for len(data) > 0 {
		available := commandRedactorMaxBytes - len(writer.redactor.pending)
		if available < 1 {
			available = 1
		}
		size := min(len(data), available)
		count, err := writer.redactor.Write(data[:size])
		written += count
		data = data[size:]
		if err != nil {
			writer.failed = err
			writer.redactor.pending = nil
			return written, err
		}
	}
	return written, nil
}

func (writer *operationOutputWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return nil
	}
	writer.closed = true
	defer os.Remove(writer.path)
	if writer.failed != nil {
		return errors.Join(writer.failed, writer.file.Close())
	}
	if err := writer.redactor.Close(); err != nil {
		return errors.Join(err, writer.file.Close())
	}
	if err := writer.escaper.Close(); err != nil {
		return errors.Join(err, writer.file.Close())
	}
	if _, err := writer.buffer.WriteString(`"}` + "\n"); err != nil {
		return errors.Join(err, writer.file.Close())
	}
	if err := writer.buffer.Flush(); err != nil {
		return errors.Join(err, writer.file.Close())
	}
	info, err := writer.file.Stat()
	if err != nil {
		return errors.Join(err, writer.file.Close())
	}
	if _, err := writer.file.Seek(0, io.SeekStart); err != nil {
		return errors.Join(err, writer.file.Close())
	}
	writer.logger.mu.Lock()
	destination, err := writer.logger.rotateLocked(time.Now(), info.Size())
	if err == nil {
		err = writeAndClose(destination, writer.file)
	}
	writer.logger.mu.Unlock()
	return errors.Join(err, writer.file.Close())
}

type streamRedactor struct {
	destination io.Writer
	values      [][]byte
	pending     []byte
	maxLength   int
}

func cleanupCommandOutputTemps(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !commandOutputTempName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func newStreamRedactor(destination io.Writer, sensitive []string) *streamRedactor {
	redactor := &streamRedactor{destination: destination}
	for _, value := range sensitive {
		if value == "" {
			continue
		}
		redactor.values = append(redactor.values, []byte(value))
		redactor.maxLength = max(redactor.maxLength, len(value))
	}
	sort.Slice(redactor.values, func(i, j int) bool { return len(redactor.values[i]) > len(redactor.values[j]) })
	return redactor
}

func (writer *streamRedactor) Write(data []byte) (int, error) {
	written := len(data)
	writer.pending = append(writer.pending, data...)
	return written, writer.flush(false)
}

func (writer *streamRedactor) Close() error { return writer.flush(true) }

func (writer *streamRedactor) flush(final bool) error {
	limit := len(writer.pending)
	if !final && writer.maxLength > 1 {
		limit -= writer.maxLength - 1
	}
	if limit <= 0 {
		return nil
	}
	position := 0
	for position < limit {
		matchAt, matchLength := -1, 0
		for _, value := range writer.values {
			if index := bytes.Index(writer.pending[position:], value); index >= 0 && (matchAt < 0 || index < matchAt) {
				matchAt, matchLength = index, len(value)
			}
		}
		if matchAt < 0 || position+matchAt >= limit {
			if _, err := writer.destination.Write(writer.pending[position:limit]); err != nil {
				return err
			}
			position = limit
			break
		}
		absolute := position + matchAt
		if _, err := writer.destination.Write(writer.pending[position:absolute]); err != nil {
			return err
		}
		if _, err := io.WriteString(writer.destination, "[REDACTED]"); err != nil {
			return err
		}
		position = absolute + matchLength
	}
	writer.pending = append(writer.pending[:0], writer.pending[position:]...)
	return nil
}

type jsonEscapeWriter struct {
	destination io.Writer
	pending     []byte
}

func (writer *jsonEscapeWriter) Write(data []byte) (int, error) {
	written := len(data)
	data = append(writer.pending, data...)
	writer.pending = writer.pending[:0]
	for len(data) > 0 {
		end := min(len(data), jsonEncodingChunkSize)
		end = completeUTF8Boundary(data, end)
		if end == 0 {
			writer.pending = append(writer.pending, data...)
			break
		}
		encoded, err := json.Marshal(string(data[:end]))
		if err != nil {
			return 0, err
		}
		if _, err := writer.destination.Write(encoded[1 : len(encoded)-1]); err != nil {
			return 0, err
		}
		data = data[end:]
	}
	return written, nil
}

func (writer *jsonEscapeWriter) Close() error {
	if len(writer.pending) == 0 {
		return nil
	}
	encoded, err := json.Marshal(string(writer.pending))
	if err == nil {
		_, err = writer.destination.Write(encoded[1 : len(encoded)-1])
	}
	writer.pending = nil
	return err
}

func completeUTF8Boundary(data []byte, end int) int {
	if end == 0 {
		return 0
	}
	start := end - 1
	limit := max(0, end-(utf8.UTFMax-1))
	for start >= limit && !utf8.RuneStart(data[start]) {
		start--
	}
	if start < limit {
		return end
	}
	if end < len(data) {
		_, size := utf8.DecodeRune(data[start:])
		if start+size > end {
			return start
		}
	} else if !utf8.FullRune(data[start:end]) {
		return start
	}
	return end
}

func (l *Logger) cleanupLocked() error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	files := []candidate{}
	for _, entry := range entries {
		if entry.Name() != runLogFile && !archiveName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, candidate{filepath.Join(l.dir, entry.Name()), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) <= maxLogFiles {
		return nil
	}
	for _, file := range files[maxLogFiles:] {
		if err := os.Remove(file.path); err != nil {
			return err
		}
	}
	return nil
}

// FormatOperationLines formats a pre-sanitized operation event for display.
func FormatOperationLines(at time.Time, level, operation, subject, message string) []string {
	if at.IsZero() {
		at = time.Now()
	}
	if normalized, err := NormalizeLevel(level); err == nil {
		level = normalized
	} else {
		level = "INFO"
	}
	operation = strings.ToUpper(normalizeContext(operation, "SYSTEM"))
	subject = normalizeContext(subject, "-")
	message = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(message)
	parts := strings.Split(message, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, fmt.Sprintf("%s [%-5s] [%-8s] [%s] %s", at.Local().Format("2006-01-02 15:04:05.000"), level, operation, subject, part))
	}
	return lines
}
func normalizeContext(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return strings.NewReplacer("\r", " ", "\n", " ", "]", ")").Replace(value)
}
func validateLogDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s", i18n.T("log.directory_unsafe", path))
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s", i18n.T("log.directory_unsafe", path))
	}
	return nil
}
func environmentMap(items []string) map[string]string {
	result := make(map[string]string, len(items))
	for _, item := range items {
		if key, value, ok := strings.Cut(item, "="); ok {
			result[key] = value
		}
	}
	return result
}
func RedactSensitiveValues(text string, values map[string]struct{}) string {
	ordered := make([]string, 0, len(values))
	for value := range values {
		if value != "" {
			ordered = append(ordered, value)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, value := range ordered {
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	return text
}
func openLogFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil && !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s", i18n.T("log.file_unsafe", path))
	}
	// #nosec G304 -- The validated log path is opened with mode 0600 and verified as the same regular file immediately after opening.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	opened, statErr := f.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return nil, errors.Join(fmt.Errorf("%s", i18n.T("log.file_unsafe", path)), statErr, pathErr, f.Close())
	}
	return f, nil
}
