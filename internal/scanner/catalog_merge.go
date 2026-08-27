package scanner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
)

// mergeApps preserves user-owned configuration while allowing scan-managed
// entries to absorb newly proven metadata and canonical locations.
func mergeApps(existing, discovered []model.Application) []model.Application {
	byID := make(map[string]model.Application, len(existing)+len(discovered))
	order := make([]string, 0, len(existing)+len(discovered))
	for _, app := range existing {
		byID[app.ID] = cloneApplication(app)
		order = append(order, app.ID)
	}
	for _, found := range discovered {
		if configured, exists := byID[found.ID]; exists {
			if shouldCanonicalizeManagedPackage(configured, found) {
				configured.Name = found.Name
				configured.Type = found.Type
				configured.InstallPath = found.InstallPath
				configured.Identity = found.Identity
				if configured.Description == "" {
					configured.Description = found.Description
				}
				if configured.URL == "" {
					configured.URL = found.URL
				}
			}
			if configured.Type == found.Type || configured.InstallPath == "" {
				configured.InstallPath = found.InstallPath
			}
			if configured.Identity == "" && (configured.Type == found.Type || inferIdentity(configured) == inferIdentity(found)) {
				configured.Identity = found.Identity
			}
			if configured.Provider.VersionAction() == "" && found.Provider.VersionAction() != "" {
				actionConfig(&configured).Version = found.Provider.VersionAction()
			}
			if configured.ScanManaged {
				if configured.Description == "" {
					configured.Description = found.Description
				}
				if configured.URL == "" {
					configured.URL = found.URL
				}
				if configured.Provider.Type == model.ProviderDefault && found.Provider.Type != "" {
					configured.Provider.Type = found.Provider.Type
				}
				if configured.Package == "" {
					configured.Package = found.Package
				}
				if configured.Provider.CheckAction() == "" && found.Provider.CheckAction() != "" {
					actionConfig(&configured).Check = found.Provider.CheckAction()
				}
				if configured.Provider.UpdateAction() == "" && found.Provider.UpdateAction() != "" {
					actionConfig(&configured).Update = found.Provider.UpdateAction()
				}
				if configured.Provider.DownloadAction() == nil && found.Provider.DownloadAction() != nil {
					actionConfig(&configured).Download = cloneApplication(found).Provider.DownloadAction()
				}
				if configured.UpdateMode == model.ModeCheck && found.UpdateMode != model.ModeCheck {
					configured.UpdateMode = found.UpdateMode
				}
			}
			byID[found.ID] = configured
			continue
		}
		byID[found.ID] = cloneApplication(found)
		order = append(order, found.ID)
	}
	apps := make([]model.Application, 0, len(order))
	for _, id := range order {
		apps = append(apps, byID[id])
	}
	return apps
}

func shouldCanonicalizeManagedPackage(configured, found model.Application) bool {
	if !configured.ScanManaged || configured.Type != model.ApplicationTypePackage || found.Type != model.ApplicationTypeCLI {
		return false
	}
	configuredDefinition, configuredOK := matchingBuiltInPathDefinition(configured)
	foundDefinition, foundOK := matchingBuiltInPathDefinition(found)
	return configuredOK && foundOK && configuredDefinition.ID == foundDefinition.ID
}

func installedPath(path string) bool {
	path = installedPathValue(path)
	if filepath.IsAbs(path) || strings.Contains(path, string(filepath.Separator)) {
		_, err := os.Stat(path)
		return err == nil
	}
	_, err := exec.LookPath(path)
	return err == nil
}

func installedPathValue(path string) string {
	path = strings.TrimSpace(os.ExpandEnv(path))
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return path
}

// scanEnvironment copies the application's environment. PATH is the exception:
// an explicit PATH is replaced with the install directory plus the process PATH.
func scanEnvironment(app model.Application) map[string]string {
	environment := make(map[string]string, len(app.Environment)+1)
	for key, value := range app.Environment {
		environment[key] = value
	}
	path := installedPathValue(app.InstallPath)
	if path != "" && (filepath.IsAbs(path) || strings.Contains(path, string(filepath.Separator))) {
		environment["PATH"] = strings.Join(uniqueStrings([]string{filepath.Dir(path), os.Getenv("PATH")}), string(os.PathListSeparator))
	}
	return environment
}

func scanEnabledFor(app model.Application, settings model.ScanSettings) bool {
	if app.Type == model.ApplicationTypeBundle {
		return settings.Application
	}
	if strings.HasPrefix(inferIdentity(app), "package:") || app.Type == model.ApplicationTypePackage {
		identity := inferIdentity(app)
		switch {
		case strings.HasPrefix(identity, "package:python:"):
			return settings.Packages.Python
		case strings.HasPrefix(identity, "package:node:"):
			return settings.Packages.Node
		case strings.HasPrefix(identity, "package:go:"):
			return settings.Packages.Go
		case strings.HasPrefix(identity, "package:uv:"):
			return settings.Packages.UV
		case strings.HasPrefix(identity, "package:ruby:"):
			return settings.Packages.Ruby
		case strings.HasPrefix(identity, "package:homebrew-formula:"):
			return settings.Packages.HomebrewFormula
		case strings.HasPrefix(identity, "package:homebrew-cask:"):
			return settings.Packages.HomebrewCask
		case strings.HasPrefix(identity, "package:cargo:"):
			return settings.Packages.Cargo
		}
	}
	return settings.Path
}

// cloneApplication deep-copies mutable configuration fields used by a scan snapshot.
func cloneApplication(app model.Application) model.Application {
	cloned := app
	if app.Environment != nil {
		cloned.Environment = map[string]string{}
		for k, v := range app.Environment {
			cloned.Environment[k] = v
		}
	}
	if app.Provider.Actions != nil {
		actions := *app.Provider.Actions
		if actions.Download != nil {
			download := *actions.Download
			download.ExtraArgs = append([]string(nil), actions.Download.ExtraArgs...)
			actions.Download = &download
		}
		cloned.Provider.Actions = &actions
	}
	return cloned
}

func cloneApplications(apps []model.Application) []model.Application {
	cloned := make([]model.Application, len(apps))
	for i := range apps {
		cloned[i] = cloneApplication(apps[i])
	}
	return cloned
}

func cloneRuntimeState(state model.RuntimeState) model.RuntimeState {
	cloned := state
	if state.Observations != nil {
		cloned.Observations = map[string]model.ScanObservation{}
		for k, v := range state.Observations {
			cloned.Observations[k] = v
		}
	}
	return cloned
}
