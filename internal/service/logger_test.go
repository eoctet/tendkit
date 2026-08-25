package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
)

func TestServiceReusesLoggerAndRefreshesOnLoggingSettingsChange(t *testing.T) {
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	service := NewWithConfig(nil)
	first, err := service.loggerFor(catalog)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.loggerFor(catalog)
	if err != nil || first != second {
		t.Fatalf("shared logger = %p, %p, %v", first, second, err)
	}
	catalog.Settings.LogLevel = "WARN"
	third, err := service.loggerFor(catalog)
	if err != nil || third == first {
		t.Fatalf("refreshed logger = %p, %p, %v", first, third, err)
	}
}

func TestServiceOperationLogRedactsCatalogSecretBeforeRunStarts(t *testing.T) {
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	const secret = "pre-run-service-token"
	catalog.Apps = []model.Application{{Environment: map[string]string{"SERVICE_TOKEN": secret}}}
	service := NewWithConfig(nil)
	lines, err := service.OperationLog(catalog, "INFO", "check", secret, "message "+secret)
	if err != nil || strings.Contains(strings.Join(lines, "\n"), secret) {
		t.Fatalf("operation log leaked: %q, %v", lines, err)
	}
}

func TestServiceOperationLogRegistersNewSecretWhenLoggerIsReused(t *testing.T) {
	catalog := config.Default()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	service := NewWithConfig(nil)
	if _, err := service.loggerFor(catalog); err != nil {
		t.Fatal(err)
	}
	const secret = "reused-service-token"
	catalog.Apps = []model.Application{{Environment: map[string]string{"SERVICE_TOKEN": secret}}}
	lines, err := service.OperationLog(catalog, "INFO", "check", secret, "message "+secret)
	if err != nil || strings.Contains(strings.Join(lines, "\n"), secret) {
		t.Fatalf("reused operation log leaked: %q, %v", lines, err)
	}
	data, err := os.ReadFile(filepath.Join(catalog.Settings.LogDir, "run.log"))
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatalf("reused run log leaked: %q, %v", data, err)
	}
}

func TestServiceOperationLogFallsBackToRedactedTextWhenLoggerCannotOpen(t *testing.T) {
	directory := t.TempDir()
	blocked := filepath.Join(directory, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := config.Default()
	catalog.Settings.LogDir = blocked
	const secret = "fallback-service-token"
	catalog.Apps = []model.Application{{Environment: map[string]string{"SERVICE_TOKEN": secret}}}
	lines, err := NewWithConfig(nil).OperationLog(catalog, "INFO", "check", secret, "message "+secret)
	if err != nil || len(lines) == 0 || strings.Contains(strings.Join(lines, "\n"), secret) {
		t.Fatalf("fallback operation log = %q, %v", lines, err)
	}
}
