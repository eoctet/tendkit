package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/eoctet/tendkit/internal/model"
	downloadutil "github.com/eoctet/tendkit/pkg/downloader"
	"github.com/eoctet/tendkit/pkg/i18n"
	logutil "github.com/eoctet/tendkit/pkg/logger"
)

const packagePlaceholder = "{package}"

var (
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	bundleIDPattern        = regexp.MustCompile(`^[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+$`)
	sha256DigestPattern    = regexp.MustCompile(`(?i)^[0-9a-f]{64}$`)
	requiredProviderURLs   = []model.ProviderType{
		model.ProviderGitHubRelease, model.ProviderGitHubTag, model.ProviderNPM, model.ProviderPyPI,
		model.ProviderJetBrains, model.ProviderGo, model.ProviderNodeLTS,
	}
	packageProviderURLs = []model.ProviderType{
		model.ProviderGitHubRelease, model.ProviderGitHubTag, model.ProviderNPM, model.ProviderPyPI, model.ProviderJetBrains,
	}
)

// ValidateConfig enforces the current strict catalog schema and value limits.
func validateConfig(catalog model.Config) error {
	if catalog.SchemaVersion != model.SchemaVersion {
		return errors.New(i18n.T("config.schema_unsupported", catalog.SchemaVersion))
	}
	downloaderKind, err := validateSettings(catalog.Settings)
	if err != nil {
		return err
	}
	seenIDs := make(map[string]struct{}, len(catalog.Apps))
	seenIdentities := make(map[string]string, len(catalog.Apps))
	for index, app := range catalog.Apps {
		if err := validateApplication(app, index, seenIDs, seenIdentities, downloaderKind); err != nil {
			return err
		}
	}
	return validateScanVersionControl(catalog.ScanVersionControl)
}

func validateSettings(settings model.Settings) (downloadutil.DownloaderKind, error) {
	if settings.Language != "zh" && settings.Language != "en" {
		return "", errors.New(i18n.T("config.language_invalid"))
	}
	if settings.TimeoutSeconds <= 0 || settings.TimeoutSeconds > maxTimeoutSeconds {
		return "", errors.New(i18n.T("config.timeout_invalid"))
	}
	if settings.Workers < minWorkers || settings.Workers > maxWorkers {
		return "", errors.New(i18n.T("config.workers_invalid"))
	}
	if _, err := logutil.NormalizeLevel(settings.LogLevel); err != nil {
		return "", errors.New(i18n.T("config.log_level_invalid", settings.LogLevel))
	}
	if err := validateHTTPSettings(settings.HTTP); err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.Downloader.StorePath) == "" || strings.TrimSpace(settings.LogDir) == "" {
		return "", errors.New(i18n.T("config.paths_empty"))
	}
	cli := strings.TrimSpace(settings.Downloader.CLI)
	downloaderKind, err := downloadutil.DownloaderKindFromCLI(cli)
	if err != nil {
		return "", errors.New(i18n.T("config.downloader_cli_unsupported", cli))
	}
	if err := downloadutil.ValidateDownloaderExtraArgs(downloaderKind, settings.Downloader.ExtraArgs); err != nil {
		return "", fmt.Errorf("settings.downloader.extra_args: %w", err)
	}
	if err := validateProviderURLs(settings.ProviderURLs); err != nil {
		return "", err
	}
	seenBundleIDs := make(map[string]struct{}, len(settings.Scan.BundleID))
	for index, bundleID := range settings.Scan.BundleID {
		bundleID = strings.TrimSpace(bundleID)
		if bundleID == "" {
			return "", errors.New(i18n.T("config.bundle_id_empty", index))
		}
		if !bundleIDPattern.MatchString(bundleID) {
			return "", errors.New(i18n.T("config.bundle_id_invalid", index, bundleID))
		}
		normalized := strings.ToLower(bundleID)
		if _, duplicate := seenBundleIDs[normalized]; duplicate {
			return "", errors.New(i18n.T("config.bundle_id_duplicate", bundleID))
		}
		seenBundleIDs[normalized] = struct{}{}
	}
	for index, pattern := range settings.Scan.Exclude {
		if strings.TrimSpace(pattern) == "" {
			return "", errors.New(i18n.T("config.exclude_empty", index))
		}
	}
	return downloaderKind, nil
}

func validateHTTPSettings(settings *model.HTTPSettings) error {
	if settings == nil {
		return errors.New(i18n.T("config.http_required"))
	}
	if settings.TimeoutSeconds < 1 || settings.TimeoutSeconds > maxHTTPTimeoutSeconds {
		return errors.New(i18n.T("config.http_timeout_invalid"))
	}
	if settings.MaxConcurrencyPerHost < 1 || settings.MaxConcurrencyPerHost > maxHTTPConcurrencyPerHost {
		return errors.New(i18n.T("config.http_concurrency_invalid"))
	}
	if settings.Retries < 0 || settings.Retries > maxHTTPRetries {
		return errors.New(i18n.T("config.http_retries_invalid"))
	}
	return nil
}

func validateProviderURLs(providerURLs map[string]string) error {
	if len(providerURLs) == 0 {
		return errors.New(i18n.T("config.provider_urls_empty"))
	}
	required := make(map[string]struct{}, len(requiredProviderURLs))
	for _, name := range requiredProviderURLs {
		required[string(name)] = struct{}{}
		endpoint := strings.TrimSpace(providerURLs[string(name)])
		if endpoint == "" {
			return errors.New(i18n.T("config.provider_url_empty", name))
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New(i18n.T("config.provider_url_invalid", name))
		}
	}
	for name := range providerURLs {
		if _, found := required[name]; !found {
			return fmt.Errorf("settings.provider_urls.%s is unsupported", name)
		}
	}
	for _, name := range packageProviderURLs {
		if !strings.Contains(providerURLs[string(name)], packagePlaceholder) {
			return errors.New(i18n.T("config.provider_url_package", name))
		}
	}
	return nil
}

func validateApplication(app model.Application, index int, seenIDs map[string]struct{}, seenIdentities map[string]string, downloaderKind downloadutil.DownloaderKind) error {
	prefix := fmt.Sprintf("apps[%d]", index)
	if strings.TrimSpace(app.ID) == "" || strings.TrimSpace(app.Name) == "" {
		return errors.New(i18n.T("config.app_name_empty", prefix))
	}
	if !validApplicationType(app.Type) {
		return errors.New(i18n.T("config.app_type_invalid", prefix, app.Type))
	}
	if strings.TrimSpace(app.InstallPath) == "" {
		return errors.New(i18n.T("config.app_install_path_empty", prefix))
	}
	if _, exists := seenIDs[app.ID]; exists {
		return errors.New(i18n.T("config.app_id_duplicate", app.ID))
	}
	seenIDs[app.ID] = struct{}{}
	identity := strings.ToLower(strings.TrimSpace(app.Identity))
	if existingID, exists := seenIdentities[identity]; identity != "" && exists {
		return errors.New(i18n.T("config.app_identity_duplicate", app.Identity, existingID))
	}
	if identity != "" {
		seenIdentities[identity] = app.ID
	}
	if !app.UpdateMode.Valid() {
		return errors.New(i18n.T("config.update_mode_invalid", prefix))
	}
	if app.Provider.Type == "" {
		return errors.New(i18n.T("config.provider_empty", prefix))
	}
	if !app.Provider.Type.Valid() {
		return errors.New(i18n.T("config.provider_invalid", prefix, app.Provider.Type))
	}
	for key := range app.Environment {
		if !environmentNamePattern.MatchString(key) {
			return errors.New(i18n.T("config.environment_invalid", prefix, key))
		}
	}
	if err := validateUpdateConfiguration(app, prefix, downloaderKind); err != nil {
		return err
	}
	if strings.TrimSpace(app.StatusManaged.UpdateStatus) == "" {
		return errors.New(i18n.T("config.app_status_managed_invalid", prefix))
	}
	if !model.ValidStatus(app.StatusManaged.UpdateStatus) {
		return errors.New(i18n.T("config.app_status_managed_unknown", prefix, app.StatusManaged.UpdateStatus))
	}
	return validateProviderConfiguration(app, prefix)
}

func validApplicationType(applicationType string) bool {
	switch applicationType {
	case model.ApplicationTypeCLI, model.ApplicationTypeBundle, model.ApplicationTypePackage, model.ApplicationTypeSDK:
		return true
	default:
		return false
	}
}

func validateUpdateConfiguration(app model.Application, prefix string, downloaderKind downloadutil.DownloaderKind) error {
	download := app.Provider.DownloadAction()
	if download == nil {
		return nil
	}
	downloadURL := strings.TrimSpace(download.URL)
	if downloadURL == "" {
		return errors.New(i18n.T("config.download_url_empty", prefix))
	}
	parsed, err := url.Parse(downloadURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New(i18n.T("config.download_url_invalid", prefix))
	}
	checksum := strings.TrimSpace(download.ChecksumValue)
	if checksum != "" && !sha256DigestPattern.MatchString(checksum) {
		return errors.New(i18n.T("config.download_checksum_invalid", prefix))
	}
	checksumURL := strings.TrimSpace(download.ChecksumURL)
	if checksumURL != "" {
		parsed, err := url.Parse(checksumURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New(i18n.T("config.download_checksum_url_invalid", prefix))
		}
	}
	if download.ChecksumEnabled && app.Provider.Type != model.ProviderGitHubRelease && checksum == "" && checksumURL == "" {
		return errors.New(i18n.T("config.download_checksum_source_empty", prefix))
	}
	if err := downloadutil.ValidateDownloaderExtraArgs(downloaderKind, download.ExtraArgs); err != nil {
		return fmt.Errorf("%s.provider.actions.download.extra_args: %w", prefix, err)
	}
	return nil
}

func validateProviderConfiguration(app model.Application, prefix string) error {
	if app.Provider.Actions != nil && !app.Provider.HasActions() {
		return errors.New(i18n.T("config.provider_actions_empty", prefix))
	}
	if !app.Enabled {
		return nil
	}
	if app.Provider.Type == model.ProviderDefault && strings.TrimSpace(app.Provider.VersionAction()) == "" {
		return errors.New(i18n.T("config.current_unavailable", prefix))
	}
	if !hasLatestCapability(app.Provider) {
		return errors.New(i18n.T("config.check_unavailable", prefix))
	}
	if app.UpdateMode == model.ModeAuto && strings.TrimSpace(app.Provider.UpdateAction()) == "" && !hasBuiltinUpdate(app) {
		return errors.New(i18n.T("config.auto_update_empty", prefix))
	}
	if app.UpdateMode == model.ModeDownload && app.Provider.DownloadAction() == nil && !hasBuiltinDownload(app) {
		return errors.New(i18n.T("config.download_url_empty", prefix))
	}
	if app.UpdateMode == model.ModeInstall && (app.Provider.Type != model.ProviderDefault ||
		strings.TrimSpace(app.Provider.VersionAction()) == "" ||
		strings.TrimSpace(app.Provider.CheckAction()) == "" ||
		strings.TrimSpace(app.Provider.UpdateAction()) == "" ||
		strings.TrimSpace(app.Provider.InstallAction()) == "") {
		return errors.New(i18n.T("config.install_unavailable", prefix))
	}
	return nil
}

func hasBuiltinUpdate(app model.Application) bool {
	if app.Provider.Type == model.ProviderSparkle {
		return app.Type == model.ApplicationTypeBundle
	}
	switch app.Provider.Type {
	case model.ProviderNPM, model.ProviderPyPI, model.ProviderUV:
		return app.Type == model.ApplicationTypePackage
	case model.ProviderHomebrew:
		return strings.TrimSpace(app.Package) != "" && (app.Type == model.ApplicationTypePackage || app.Type == model.ApplicationTypeCLI || app.Type == model.ApplicationTypeBundle)
	}
	if app.Type != model.ApplicationTypePackage {
		return false
	}
	switch app.Provider.Type {
	case model.ProviderGo:
		return validGoComponent(app)
	default:
		return false
	}
}

func hasLatestCapability(provider model.ProviderConfig) bool {
	return strings.TrimSpace(provider.CheckAction()) != "" || provider.Type != model.ProviderDefault && provider.Type != model.ProviderCargo
}

func hasBuiltinDownload(app model.Application) bool {
	if app.Provider.Type == model.ProviderGo {
		// go.dev archives are Go runtime artifacts. Components are upgraded by
		// go install and require an explicit download action for download mode.
		return app.Type != model.ApplicationTypePackage
	}
	switch app.Provider.Type {
	case model.ProviderGitHubRelease, model.ProviderGitHubTag, model.ProviderNPM,
		model.ProviderPyPI, model.ProviderJetBrains, model.ProviderGo,
		model.ProviderNodeLTS, model.ProviderSparkle:
		return true
	default:
		return false
	}
}

func validGoComponent(app model.Application) bool {
	return app.Type == model.ApplicationTypePackage &&
		strings.TrimSpace(app.InstallPath) != ""
}

// ValidateConfigExecutionSecurity verifies the ownership and write boundary
// before provider actions are executed with the current user's rights.
func (s store) validateExecutionSecurity() error {
	path, err := filepath.Abs(s.ConfigPath)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return unsafeConfig(path, i18n.T("config.reason_regular"))
	}
	uid, ok := fileOwner(info)
	if !ok || uid != os.Geteuid() {
		return unsafeConfig(path, i18n.T("config.reason_owner"))
	}
	if info.Mode().Perm()&0o022 != 0 {
		return unsafeConfig(path, i18n.T("config.reason_writable"))
	}
	directory, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	for {
		info, statErr := os.Lstat(directory)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return unsafeConfig(directory, i18n.T("config.reason_directory"))
		}
		owner, ownerOK := fileOwner(info)
		if !ownerOK || (owner != os.Geteuid() && owner != 0) {
			return unsafeConfig(directory, i18n.T("config.reason_directory_owner"))
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return unsafeConfig(directory, i18n.T("config.reason_directory_writable"))
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return nil
}

func fileOwner(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

func unsafeConfig(path, reason string) error {
	return &UnsafeConfigError{Path: path, Reason: fmt.Sprintf("%s; %s", reason, i18n.T("config.repair"))}
}
