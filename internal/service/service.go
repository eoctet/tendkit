package service

import (
	"io"
	"sync"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// Bootstrap contains command startup paths while keeping config access behind Service.
type Bootstrap = config.Bootstrap

func DefaultBootstrap() Bootstrap { return config.DefaultBootstrap() }

func LoadEnvironment(path string, required bool, defaultPath string) error {
	_, err := config.LoadEnvFile(path, required, defaultPath)
	return err
}

// Service coordinates persistent configuration transactions for the TUI.
type Service struct {
	config         *config.Center
	GitHubResolver scanner.GitHubResolver
	loggerMu       sync.Mutex
	logger         *logutil.Logger
	loggerDir      string
	loggerLevel    string
}

// New constructs the process service and its private configuration center.
func New(configPath, lockPath string) *Service {
	return &Service{config: config.New(configPath, lockPath)}
}

// NewWithConfig injects an existing configuration center for integration tests
// and embedders while preserving Service as the operation boundary.
func NewWithConfig(center *config.Center) *Service { return &Service{config: center} }

// Start acquires the process-lifetime lock, initializes the configuration, and
// returns the validated startup snapshot used by the command layer.
func (s *Service) Start() (model.Config, error) {
	if err := s.config.AcquireProcessLock(); err != nil {
		return model.Config{}, err
	}
	if err := s.config.Initialize(); err != nil {
		_ = s.config.ReleaseProcessLock()
		return model.Config{}, err
	}
	catalog, err := s.config.Load()
	if err != nil {
		_ = s.config.ReleaseProcessLock()
		return model.Config{}, err
	}
	if log, logErr := s.loggerFor(catalog); logErr == nil {
		_ = log.Info(logutil.LogEntry{Event: "config_started", Operation: "config", Message: "configuration initialized"})
	}
	return catalog, nil
}

// Close releases the process-lifetime configuration lock.
func (s *Service) Close() error { return s.config.ReleaseProcessLock() }

// OperationLog persists a localized TUI event through the shared logger.
func (s *Service) OperationLog(catalog model.Config, level, operation, subject, message string) ([]string, error) {
	log, err := s.loggerFor(catalog)
	if err != nil {
		return formatOperationWithoutFile(catalog, level, operation, subject, message)
	}
	lines, _ := log.Operation(level, operation, subject, message)
	return lines, nil
}

// OperationText formats a localized TUI event through the shared logger without persisting it.
func (s *Service) OperationText(catalog model.Config, level, operation, subject, message string) ([]string, error) {
	log, err := s.loggerFor(catalog)
	if err != nil {
		return formatOperationWithoutFile(catalog, level, operation, subject, message)
	}
	lines, _ := log.OperationText(level, operation, subject, message)
	return lines, nil
}

// CommandOutputWriter streams one command-output event through the shared logger.
func (s *Service) CommandOutputWriter(catalog model.Config, level, operation, appID, appName string) (io.WriteCloser, error) {
	log, err := s.loggerFor(catalog)
	if err != nil {
		return discardServiceWriter{Writer: io.Discard}, nil
	}
	writer, err := log.OperationOutputWriter(level, operation, appID, appName)
	if err != nil {
		return discardServiceWriter{Writer: io.Discard}, nil
	}
	return writer, nil
}

func (s *Service) loggerFor(catalog model.Config) (*logutil.Logger, error) {
	dir := runtimeutil.ExpandPath(catalog.Settings.LogDir)
	s.loggerMu.Lock()
	defer s.loggerMu.Unlock()
	if s.logger != nil && s.loggerDir == dir && s.loggerLevel == catalog.Settings.LogLevel {
		registerLoggerSensitive(s.logger, catalog)
		return s.logger, nil
	}
	log, err := logutil.NewLogger(dir, catalog.Settings.LogLevel)
	if err != nil {
		return nil, err
	}
	s.logger, s.loggerDir, s.loggerLevel = log, dir, catalog.Settings.LogLevel
	registerLoggerSensitive(log, catalog)
	return log, nil
}

func formatOperationWithoutFile(catalog model.Config, level, operation, subject, message string) ([]string, error) {
	environments := make([]map[string]string, 0, len(catalog.Apps))
	for _, app := range catalog.Apps {
		environments = append(environments, app.Environment)
	}
	return logutil.FormatOperation(level, catalog.Settings.LogLevel, operation, subject, message, environments...)
}

type discardServiceWriter struct{ io.Writer }

func (discardServiceWriter) Close() error { return nil }

func registerLoggerSensitive(log *logutil.Logger, catalog model.Config) {
	for _, app := range catalog.Apps {
		log.AddSensitiveEnvironment(app.Environment)
	}
}

// snapshotSaveAttempts bounds retries for a revision race between LoadSnapshot
// and SaveSnapshot. Only ErrStaleOperation is retried after a fresh baseline check.
const snapshotSaveAttempts = 3
