package model

import (
	"reflect"
	"time"

	downloadutil "github.com/eoctet/tendkit/pkg/downloader"
)

// UpdateMode controls whether an application is checked, downloaded, or updated.
type UpdateMode string

const (
	ModeAuto     UpdateMode = "auto"
	ModeDownload UpdateMode = "download"
	ModeCheck    UpdateMode = "check"
	ModeInstall  UpdateMode = "install"
)

// Valid reports whether the update mode is supported.
func (m UpdateMode) Valid() bool {
	return m == ModeAuto || m == ModeDownload || m == ModeCheck || m == ModeInstall
}

// CommandOutput identifies one stdout or stderr chunk emitted by an application command.
type CommandOutput struct {
	CommandID uint64
	AppID     string
	AppName   string
	Operation string
	Stream    string
	Data      []byte
	Done      bool
}

// Application describes one catalog-managed development tool.
type Application struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	Description   string            `json:"description,omitempty"`
	URL           string            `json:"url,omitempty"`
	InstallPath   string            `json:"install_path"`
	Enabled       bool              `json:"enabled"`
	UpdateMode    UpdateMode        `json:"update_mode"`
	Provider      ProviderConfig    `json:"provider"`
	Package       string            `json:"package,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
	Identity      string            `json:"identity,omitempty"`
	ScanManaged   bool              `json:"scan_managed,omitempty"`
	StatusManaged ManagedStatus     `json:"status_managed"`
}

// ProviderType identifies one of the supported version providers.
type ProviderType string

const (
	ProviderDefault       ProviderType = "default"
	ProviderGitHubRelease ProviderType = "github_release"
	ProviderGitHubTag     ProviderType = "github_tag"
	ProviderNPM           ProviderType = "npm"
	ProviderPyPI          ProviderType = "pypi"
	ProviderUV            ProviderType = "uv"
	ProviderJetBrains     ProviderType = "jetbrains"
	ProviderGo            ProviderType = "go"
	ProviderNodeLTS       ProviderType = "node_lts"
	ProviderSparkle       ProviderType = "sparkle"
)

func (p ProviderType) Valid() bool {
	switch p {
	case ProviderDefault, ProviderGitHubRelease, ProviderGitHubTag, ProviderNPM, ProviderPyPI, ProviderUV, ProviderJetBrains, ProviderGo, ProviderNodeLTS, ProviderSparkle:
		return true
	default:
		return false
	}
}

// ProviderConfig configures a provider and optional action overrides.
type ProviderConfig struct {
	Type    ProviderType     `json:"type"`
	Actions *ProviderActions `json:"actions,omitempty"`
}

// ProviderActions contains optional executable capabilities for a provider.
type ProviderActions struct {
	Version  string    `json:"version,omitempty"`
	Check    string    `json:"check,omitempty"`
	Update   string    `json:"update,omitempty"`
	Download *Download `json:"download,omitempty"`
	Install  string    `json:"install,omitempty"`
}

func (p ProviderConfig) VersionAction() string {
	if p.Actions == nil {
		return ""
	}
	return p.Actions.Version
}
func (p ProviderConfig) CheckAction() string {
	if p.Actions == nil {
		return ""
	}
	return p.Actions.Check
}
func (p ProviderConfig) UpdateAction() string {
	if p.Actions == nil {
		return ""
	}
	return p.Actions.Update
}
func (p ProviderConfig) DownloadAction() *Download {
	if p.Actions == nil {
		return nil
	}
	return p.Actions.Download
}
func (p ProviderConfig) InstallAction() string {
	if p.Actions == nil {
		return ""
	}
	return p.Actions.Install
}
func (p ProviderConfig) HasActions() bool {
	return p.Actions != nil && (p.Actions.Version != "" || p.Actions.Check != "" || p.Actions.Update != "" || p.Actions.Download != nil || p.Actions.Install != "")
}

// Download describes an artifact download and its optional integrity check.
type Download = downloadutil.Spec

// DownloadProgress is a stable, presentation-independent progress event for
// one artifact. Percent is always in the inclusive range 0..100.
type DownloadProgress = downloadutil.Progress

// ManagedStatus records the persisted update information for one application.
// Discovery facts are represented only by ScanObservation during scanning.
type ManagedStatus struct {
	FirstDetectedTime string `json:"first_detected_time,omitempty"`
	CurrentVersion    string `json:"current_version,omitempty"`
	LatestVersion     string `json:"latest_version,omitempty"`
	HasUpdate         bool   `json:"has_update"`
	UpdateStatus      string `json:"update_status,omitempty"`
	Error             string `json:"error,omitempty"`
	LastCheckTime     string `json:"last_check_time,omitempty"`
	LastUpdateTime    string `json:"last_update_time,omitempty"`
	DownloadPath      string `json:"download_path,omitempty"`
}

// ConfigEqualExceptStatuses compares every persisted configuration field except
// per-application runtime status. Scan keep records remain part of the comparison.
func ConfigEqualExceptStatuses(left, right Config) bool {
	clearStatuses(&left)
	clearStatuses(&right)
	return reflect.DeepEqual(left, right)
}

// ConfigEqualExceptRuntime compares application configuration and settings while
// excluding independently persisted runtime status and scan keep decisions.
func ConfigEqualExceptRuntime(left, right Config) bool {
	clearStatuses(&left)
	clearStatuses(&right)
	left.ScanVersionControl = nil
	right.ScanVersionControl = nil
	return reflect.DeepEqual(left, right)
}

// CopyStatuses replaces destination statuses with the latest statuses for apps
// that exist in source. It never creates or removes applications.
func CopyStatuses(destination *Config, source Config) {
	if destination == nil {
		return
	}
	statuses := make(map[string]ManagedStatus, len(source.Apps))
	for _, application := range source.Apps {
		statuses[application.ID] = application.StatusManaged
	}
	for index := range destination.Apps {
		if status, found := statuses[destination.Apps[index].ID]; found {
			destination.Apps[index].StatusManaged = status
		}
	}
}

// MergeRunStatuses applies runtime results to the latest config only if each
// affected application's configuration and prior runtime status still match the
// operation baseline and global settings remain unchanged. Unrelated application
// changes and scan keep decisions are retained.
func MergeRunStatuses(base, latest, completed Config) (Config, bool) {
	if base.SchemaVersion != latest.SchemaVersion || !reflect.DeepEqual(base.Settings, latest.Settings) {
		return Config{}, false
	}
	baseApps := applicationsByID(base.Apps)
	latestApps := applicationsByID(latest.Apps)
	completedApps := applicationsByID(completed.Apps)
	merged := latest
	merged.Apps = append([]Application(nil), latest.Apps...)
	for id, completedApp := range completedApps {
		baseApp, found := baseApps[id]
		if !found || baseApp.StatusManaged == completedApp.StatusManaged {
			continue
		}
		latestApp, found := latestApps[id]
		if !found || !applicationEqualExceptStatus(baseApp, latestApp) || latestApp.StatusManaged != baseApp.StatusManaged {
			return Config{}, false
		}
		for index := range merged.Apps {
			if merged.Apps[index].ID == id {
				merged.Apps[index].StatusManaged = completedApp.StatusManaged
				break
			}
		}
	}
	return merged, true
}

func clearStatuses(value *Config) {
	value.Apps = append([]Application(nil), value.Apps...)
	for index := range value.Apps {
		value.Apps[index].StatusManaged = ManagedStatus{}
	}
}

func applicationsByID(applications []Application) map[string]Application {
	values := make(map[string]Application, len(applications))
	for _, application := range applications {
		values[application.ID] = application
	}
	return values
}

func applicationEqualExceptStatus(left, right Application) bool {
	left.StatusManaged = ManagedStatus{}
	right.StatusManaged = ManagedStatus{}
	return reflect.DeepEqual(left, right)
}

// Result is the language-independent outcome of one application operation.
type Result struct {
	AppID   string        `json:"app_id"`
	Name    string        `json:"app_name"`
	Mode    UpdateMode    `json:"update_mode"`
	Status  string        `json:"status"`
	Message string        `json:"message,omitempty"`
	State   ManagedStatus `json:"state"`
}

// Now returns the current time in the persisted RFC 3339 representation.
func Now() string { return time.Now().Format(time.RFC3339) }
