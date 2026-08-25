package service

import (
	"context"
	"errors"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner"
	logutil "github.com/eoctet/tendkit/pkg/logger"
)

// Initialize creates the missing unified configuration without replacing an existing file.
func (s *Service) Initialize() error {
	return s.config.Initialize()
}

// Load returns validated catalog and state snapshots.
func (s *Service) Load() (model.Config, model.RuntimeState, error) {
	catalog, err := s.config.Load()
	if err != nil {
		return catalog, model.RuntimeState{}, err
	}
	if log, logErr := s.loggerFor(catalog); logErr == nil {
		_ = log.Debug(logutil.LogEntry{Event: "config_loaded", Operation: "config", Message: "configuration loaded"})
	}
	return catalog, model.RuntimeState{}, nil
}

func (s *Service) Reload() (model.Config, model.RuntimeState, error) {
	catalog, err := s.config.Reload()
	if err == nil {
		if log, logErr := s.loggerFor(catalog); logErr == nil {
			_ = log.Info(logutil.LogEntry{Event: "config_reloaded", Operation: "config", Message: "configuration reloaded"})
		}
	}
	return catalog, model.RuntimeState{}, err
}

func (s *Service) GenerateIdentity(ctx context.Context, application model.Application) (string, error) {
	catalog, err := s.config.Load()
	if err != nil {
		return "", err
	}
	return scanner.New(catalog.Settings.Scan).GenerateIdentity(ctx, application)
}

func (s *Service) SaveConfig(expected, proposed model.Config) (model.Config, error) {
	var saved model.Config
	err := s.config.WithLock(func() error {
		for attempt := 0; attempt < snapshotSaveAttempts; attempt++ {
			snapshot, err := s.config.Snapshot()
			if err != nil {
				return err
			}
			latest := snapshot.Config
			if !model.ConfigEqualExceptRuntime(latest, expected) {
				return config.ErrStaleOperation
			}
			current := cloneConfigForTransaction(latest)
			previous := cloneConfigForTransaction(current)
			current = mergeCatalogEdit(current, proposed)
			model.CopyStatuses(&current, latest)
			current = scanner.ReconcileNewlyManagedBundleIDs(context.Background(), previous, current)
			if hasUnmanageTransition(previous, current) {
				model.ClearScanVersionControlForUnmanagedTransitions(&previous, &current)
			}
			if err := s.config.Save(snapshot.Revision, current); errors.Is(err, config.ErrStaleOperation) {
				continue
			} else if err != nil {
				return err
			}
			saved = current
			return nil
		}
		return config.ErrStaleOperation
	})
	if err != nil {
		if log, logErr := s.loggerFor(expected); logErr == nil {
			registerLoggerSensitive(log, expected)
			_ = log.Error(logutil.LogEntry{Event: "config_save_failed", Operation: "config", Message: "configuration save failed", Detail: err.Error()})
		}
	} else {
		if log, logErr := s.loggerFor(saved); logErr == nil {
			registerLoggerSensitive(log, saved)
			_ = log.Info(logutil.LogEntry{Event: "config_saved", Operation: "config", Message: "configuration saved"})
		}
	}
	return saved, err
}

func cloneConfigForTransaction(value model.Config) model.Config {
	value.Apps = cloneApplications(value.Apps)
	return value
}
func mergeCatalogEdit(current, proposed model.Config) model.Config {
	current.Settings = proposed.Settings
	proposedApps := make(map[string]model.Application, len(proposed.Apps))
	for _, app := range proposed.Apps {
		proposedApps[app.ID] = app
	}
	for index := range current.Apps {
		if app, exists := proposedApps[current.Apps[index].ID]; exists {
			current.Apps[index] = app
		}
	}
	return current
}
func hasUnmanageTransition(previous, current model.Config) bool {
	before := make(map[string]bool, len(previous.Apps))
	for _, app := range previous.Apps {
		before[app.ID] = app.ScanManaged
	}
	for _, app := range current.Apps {
		if before[app.ID] && !app.ScanManaged {
			return true
		}
	}
	return false
}
