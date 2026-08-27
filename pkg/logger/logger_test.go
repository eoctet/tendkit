package logger

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingOutputWriter struct{}

func (failingOutputWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestLoggerFiltersLevelsAndNeverCreatesAuditLog(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory, "INFO")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "catalog-super-secret"
	log.AddSensitiveEnvironment(map[string]string{"SERVICE_API_KEY": secret})
	if err := log.Debug(LogEntry{Event: "debug", Message: secret}); err != nil {
		t.Fatal(err)
	}
	if err := log.Info(LogEntry{Event: " RUN_STARTED ", Message: secret}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, runLogFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry LogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Time == "" || entry.RunID == "" || entry.Event != "run_started" || entry.Level != "INFO" {
		t.Fatalf("entry: %#v", entry)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("secret leaked")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "audit.log")); !os.IsNotExist(err) {
		t.Fatalf("audit.log exists: %v", err)
	}
}

func TestLoggerStandardLevelInterfacesOwnLevelDispatch(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory, "TRACE")
	if err != nil {
		t.Fatal(err)
	}
	writes := []func(LogEntry) error{log.Trace, log.Debug, log.Info, log.Warn, log.Error}
	levels := []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR"}
	for index, write := range writes {
		if err := write(LogEntry{Event: "standard_" + strings.ToLower(levels[index])}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Log("INFO", LogEntry{Event: "generic"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Log("", LogEntry{Event: "invalid"}); err == nil {
		t.Fatal("empty generic level accepted")
	}
	data, err := os.ReadFile(filepath.Join(directory, runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(levels)+1 {
		t.Fatalf("line count = %d, want %d", len(lines), len(levels)+1)
	}
	for index, level := range append(levels, "INFO") {
		var entry LogEntry
		if err := json.Unmarshal([]byte(lines[index]), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Level != level {
			t.Fatalf("entry %d level = %q, want %q", index, entry.Level, level)
		}
	}
}

func TestLoggerLeavesHistoricalAuditLogUntouched(t *testing.T) {
	directory := t.TempDir()
	audit := filepath.Join(directory, "audit.log")
	if err := os.WriteFile(audit, []byte("historical"), 0o600); err != nil {
		t.Fatal(err)
	}
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Info(LogEntry{Event: "run"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(audit)
	if err != nil || string(data) != "historical" {
		t.Fatalf("audit history = %q, %v", data, err)
	}
}

func TestLoggerOperationUsesSameFilterAndWritesJSONL(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory, "WARN")
	if err != nil {
		t.Fatal(err)
	}
	if lines, err := log.Operation("INFO", "scan", "sample", "中文消息"); err != nil || len(lines) != 0 {
		t.Fatalf("filtered operation = %q, %v", lines, err)
	}
	if lines, err := log.Operation("WARN", "scan", "sample", "中文消息"); err != nil || len(lines) != 1 || !strings.Contains(lines[0], "[WARN ") {
		t.Fatalf("operation = %q, %v", lines, err)
	}
	data, err := os.ReadFile(filepath.Join(directory, runLogFile))
	if err != nil || !strings.Contains(string(data), `"event":"app_operation"`) {
		t.Fatalf("operation JSONL = %q, %v", data, err)
	}
}

func TestLoggerRedactsOperationTextAndExternalStructuredFields(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "all-fields-secret"
	log.AddSensitiveEnvironment(map[string]string{"API_TOKEN": secret})
	lines, err := log.Operation("INFO", "op-"+secret, "subject-"+secret, "message-"+secret)
	if err != nil || strings.Contains(strings.Join(lines, "\n"), secret) {
		t.Fatalf("operation redaction = %q, %v", lines, err)
	}
	if err := log.Error(LogEntry{Event: "application_event", AppID: secret, AppName: secret, Operation: secret, Status: secret, Message: secret, Artifact: secret, Detail: secret}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("structured secret leaked: %s", data)
	}
}

func TestLoggerNeverRedactsFixedProtocolFields(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	log.AddSensitiveEnvironment(map[string]string{"API_TOKEN": "INFO"})
	if err := log.Info(LogEntry{Event: "fixed_event", Message: "INFO"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	var entry LogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Level != "INFO" || entry.Event != "fixed_event" || entry.Time == "" || entry.RunID == "" {
		t.Fatalf("fixed fields were changed: %#v", entry)
	}
	if entry.Message != "[REDACTED]" {
		t.Fatalf("message was not redacted: %#v", entry)
	}
}

func TestLoggerRotatesByLocalDateAndKeepsFiveFiles(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	log.activeDate = time.Now().AddDate(0, 0, -1)
	if err := os.WriteFile(filepath.Join(directory, runLogFile), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := log.Info(LogEntry{Event: "rotation"}); err != nil {
		t.Fatal(err)
	}
	archives, err := filepath.Glob(filepath.Join(directory, "run-*.log"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives = %v, %v", archives, err)
	}
	for index := 0; index < 6; index++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("run-2026-01-01.%d.log", index)), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log.mu.Lock()
	err = log.cleanupLocked()
	log.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "run*.log"))
	if err != nil || len(files) > maxLogFiles {
		t.Fatalf("files = %v, %v", files, err)
	}
}

func TestLoggerRotatesAtSizeLimitAndRetainsAtMostFiveFiles(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 4; index++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("run-2026-01-01.%d.log", index)), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	active := filepath.Join(directory, runLogFile)
	if err := os.WriteFile(active, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(active, maxLogSize); err != nil {
		t.Fatal(err)
	}
	if err := log.Info(LogEntry{Event: "size_rotation"}); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "run*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != maxLogFiles {
		t.Fatalf("file count after size rotation = %d, files=%v", len(files), files)
	}
	data, err := os.ReadFile(active)
	if err != nil || !strings.Contains(string(data), `"event":"size_rotation"`) {
		t.Fatalf("active log = %q, %v", data, err)
	}
}

func TestLoggerRejectsUnsafeActiveTargetBeforeDateRotation(t *testing.T) {
	directory := t.TempDir()
	for _, setup := range []struct {
		name string
		make func(string) error
	}{
		{"symlink", func(path string) error { return os.Symlink(filepath.Join(directory, "target"), path) }},
		{"directory", func(path string) error { return os.Mkdir(path, 0o700) }},
	} {
		t.Run(setup.name, func(t *testing.T) {
			path := filepath.Join(directory, runLogFile)
			if err := setup.make(path); err != nil {
				t.Fatal(err)
			}
			log, err := NewLogger(directory)
			if err != nil {
				t.Fatal(err)
			}
			log.activeDate = time.Now().AddDate(0, 0, -1)
			if err := log.Info(LogEntry{Event: "rotate"}); err == nil {
				t.Fatal("unsafe active log accepted")
			}
			if _, err := filepath.Glob(filepath.Join(directory, "run-*.log")); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(path)
		})
	}
}

func TestLoggerRotationMakesLegacyActiveAndArchivePrivate(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, runLogFile)
	if err := os.WriteFile(path, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	log.activeDate = time.Now().AddDate(0, 0, -1)
	if err := log.Info(LogEntry{Event: "rotate"}); err != nil {
		t.Fatal(err)
	}
	archives, _ := filepath.Glob(filepath.Join(directory, "run-*.log"))
	if len(archives) != 1 {
		t.Fatalf("archives = %v", archives)
	}
	if info, err := os.Stat(archives[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode=%v, %v", info, err)
	}
}
func TestLoggerRejectsUnsafeDirectoryAndDestination(t *testing.T) {
	if _, err := NewLogger("~/.config/tendkit/logs"); err == nil {
		t.Fatal("unresolved home-relative log directory accepted")
	}

	unsafeDirectory := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(unsafeDirectory, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeDirectory, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLogger(unsafeDirectory); err == nil {
		t.Fatal("group-writable directory accepted")
	}

	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, runLogFile), 0o700); err != nil {
		t.Fatal(err)
	}
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Info(LogEntry{Event: "test"}); err == nil {
		t.Fatal("directory log destination accepted")
	}
}

func TestLoggerHelpersHandleNilAndEnvironment(t *testing.T) {
	var log *Logger
	log.AddSensitiveEnvironment(map[string]string{"TOKEN": "ignored"})
	for _, write := range []func(LogEntry) error{log.Trace, log.Debug, log.Info, log.Warn, log.Error} {
		if err := write(LogEntry{Event: "ignored"}); err != nil {
			t.Fatalf("nil logger returned error: %v", err)
		}
	}
	environment := environmentMap([]string{"A=1", "BROKEN", "B=two=parts"})
	if environment["A"] != "1" || environment["B"] != "two=parts" {
		t.Fatalf("environment map = %#v", environment)
	}
	if got := RedactSensitiveValues("long-secret short", map[string]struct{}{"short": {}, "long-secret": {}}); got != "[REDACTED] [REDACTED]" {
		t.Fatalf("redacted = %q", got)
	}
}

func TestOperationOutputWriterPersistsOneCompleteCommandRecord(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := log.OperationOutputWriter("INFO", "check", "sample", "Sample")
	if err != nil {
		t.Fatal(err)
	}
	stream, ok := writer.(*operationOutputWriter)
	if !ok || stream.buffer.Size() != commandFileBufferSize || stream.buffer.Size()+commandRedactorMaxBytes != commandOutputBufferSize {
		t.Fatalf("command output buffer = %#v", writer)
	}
	large := strings.Repeat("x", (1<<20)+17)
	for _, part := range []string{"one\n", large, "\ntwo\n"} {
		if _, err := writer.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	var entry LogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	want := "one\n" + large + "\ntwo\n"
	if entry.Event != "app_operation" || entry.AppID != "sample" || entry.AppName != "Sample" || entry.Message != want {
		t.Fatalf("entry = %#v", entry)
	}
	if matches, err := filepath.Glob(filepath.Join(directory, ".run-command-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary command files = %v, %v", matches, err)
	}
}

func TestOperationOutputWriterRedactsSecretAcrossWriteBoundaries(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "cross-boundary-secret"
	log.AddSensitiveEnvironment(map[string]string{"SERVICE_TOKEN": secret})
	writer, err := log.OperationOutputWriter("INFO", "check", "sample", "Sample")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"prefix cross-", "boundary-", "secret suffix"} {
		if _, err := io.WriteString(writer, part); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), "prefix [REDACTED] suffix") {
		t.Fatalf("stream redaction = %s", data)
	}
}

func TestOperationOutputWriterStreamsJSONEscapingAcrossUTF8Boundaries(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := log.OperationOutputWriter("INFO", "check", "sample", "Sample")
	if err != nil {
		t.Fatal(err)
	}
	wide := []byte("界")
	parts := [][]byte{[]byte("quote \" slash \\ control \x01 "), wide[:1], wide[1:], {0xff}, []byte(" tail")}
	for _, part := range parts {
		if _, err := writer.Write(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, runLogFile))
	if err != nil {
		t.Fatal(err)
	}
	var entry LogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	want := "quote \" slash \\ control \x01 界� tail"
	if entry.Message != want {
		t.Fatalf("message = %q, want %q", entry.Message, want)
	}
}

func TestOperationOutputWriterBoundsPendingDataAfterWriteFailure(t *testing.T) {
	log, err := NewLogger(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	output, err := log.OperationOutputWriter("INFO", "check", "sample", "Sample")
	if err != nil {
		t.Fatal(err)
	}
	writer := output.(*operationOutputWriter)
	writer.escaper.destination = failingOutputWriter{}
	payload := []byte(strings.Repeat("x", (1<<20)+17))
	if _, err := writer.Write(payload); err == nil {
		t.Fatal("stream write unexpectedly succeeded")
	}
	if len(writer.redactor.pending) != 0 || writer.failed == nil {
		t.Fatalf("failed writer retained %d bytes, error=%v", len(writer.redactor.pending), writer.failed)
	}
	if count, err := writer.Write(payload); err != nil || count != len(payload) || len(writer.redactor.pending) != 0 {
		t.Fatalf("discard after failure = %d, %v, pending=%d", count, err, len(writer.redactor.pending))
	}
	if err := writer.Close(); err == nil {
		t.Fatal("Close did not report the ignored persistence failure")
	}
}

func TestNewLoggerRemovesStaleCommandOutputFiles(t *testing.T) {
	directory := t.TempDir()
	stale := filepath.Join(directory, ".run-command-123456789")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(directory, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(directory, ".run-command-notes")
	if err := os.WriteFile(notes, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLogger(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale command output file still exists: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
	if _, err := os.Stat(notes); err != nil {
		t.Fatalf("similarly named user file was removed: %v", err)
	}
}

func TestOperationOutputWriterDiscardsWhenSensitiveCarryWouldExceedBound(t *testing.T) {
	directory := t.TempDir()
	log, err := NewLogger(directory)
	if err != nil {
		t.Fatal(err)
	}
	log.AddSensitiveEnvironment(map[string]string{"SERVICE_TOKEN": strings.Repeat("s", commandRedactorMaxBytes/2+1)})
	writer, err := log.OperationOutputWriter("INFO", "check", "sample", "Sample")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := writer.(discardWriteCloser); !ok {
		t.Fatalf("writer = %T, want safe discard", writer)
	}
	if _, err := writer.Write([]byte("output")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, runLogFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded output created run.log: %v", err)
	}
}
