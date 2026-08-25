package ui

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/ui/component"
	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func handleConfigKey(view *tuiModel, key string, actions TUIActions) bool {
	rows := configRows(&view.working)
	if len(rows) == 0 {
		return false
	}
	switch key {
	case "q":
		return true
	case "ctrl+s":
		return saveTUIConfig(view, rows, actions)
	case "r":
		revertTUIConfig(view)
		return false
	}
	if view.configAppFocus {
		return handleApplicationConfigKey(view, key)
	}
	return handleConfigListKey(view, key, rows)
}

func saveTUIConfig(view *tuiModel, rows []configRow, actions TUIActions) bool {
	if !view.dirty {
		view.setMessage(i18n.T("tui.no_changes"), false)
		return false
	}
	if err := validateTUIApplicationCatalog(view.working); err != nil {
		view.setMessage(i18n.ErrorText(err), true)
		return false
	}
	selectedKey := rows[max(0, min(len(rows)-1, view.configIndex))].key
	catalog, err := actions.SaveConfig(view.catalog, view.working)
	if err != nil {
		view.offerReload(err)
		view.setMessage(i18n.ErrorText(err), true)
		return false
	}
	view.catalog, view.working = catalog, cloneConfig(catalog)
	if refreshed := configRows(&view.working); len(refreshed) > 0 {
		view.configIndex = findConfigRowIndex(refreshed, selectedKey)
	}
	view.selected = max(0, min(view.selected, len(catalog.Apps)-1))
	view.dirty = false
	if language, err := i18n.Parse(catalog.Settings.Language); err == nil {
		i18n.Set(language)
	}
	view.setMessage(i18n.T("tui.saved"), false)
	return false
}

func revertTUIConfig(view *tuiModel) {
	view.working = cloneConfig(view.catalog)
	view.dirty = false
	view.setMessage(i18n.T("tui.reverted"), false)
}

func handleApplicationConfigKey(view *tuiModel, key string) bool {
	fields := selectedApplicationConfigRows(view)
	if len(fields) == 0 {
		view.configAppFocus = false
		return false
	}
	switch key {
	case "esc":
		view.configAppFocus = false
		view.appFieldScroll = 0
	case "up", "k":
		view.appFieldIndex = max(0, view.appFieldIndex-1)
	case "down", "j":
		view.appFieldIndex = min(len(fields)-1, view.appFieldIndex+1)
	case "pageup":
		view.appFieldIndex = max(0, view.appFieldIndex-6)
	case "pagedown":
		view.appFieldIndex = min(len(fields)-1, view.appFieldIndex+6)
	case "home":
		view.appFieldIndex = 0
	case "end":
		view.appFieldIndex = len(fields) - 1
	case "left":
		adjustConfig(view, -1)
	case "right":
		adjustConfig(view, 1)
	case "enter":
		beginConfigEdit(view, fields[view.appFieldIndex])
	}
	return false
}

func handleConfigListKey(view *tuiModel, key string, rows []configRow) bool {
	switch key {
	case "esc":
		if view.dirty {
			view.configExitConfirm = true
			view.confirmChoice = tuiConfirmationPrimary
		} else {
			leaveTUIConfig(view)
			view.clearMessage()
		}
	case "up", "k":
		view.configIndex = max(0, view.configIndex-1)
	case "down", "j":
		view.configIndex = min(len(rows)-1, view.configIndex+1)
	case "pageup":
		view.configIndex = max(0, view.configIndex-8)
	case "pagedown":
		view.configIndex = min(len(rows)-1, view.configIndex+8)
	case "home":
		view.configIndex = 0
	case "end":
		view.configIndex = len(rows) - 1
	case "left":
		adjustConfig(view, -1)
	case "right":
		adjustConfig(view, 1)
	case "enter":
		row := rows[view.configIndex]
		if row.rowType == configRowApplication {
			view.configAppFocus = true
			view.appFieldIndex, view.appFieldScroll = 0, 0
		} else {
			beginConfigEdit(view, row)
		}
	}
	return false
}

func beginConfigEdit(view *tuiModel, row configRow) {
	if row.rowType == configRowReadOnly || row.rowType == configRowApplication {
		view.setMessage(i18n.T("tui.readonly"), false)
		return
	}
	if row.rowType == configRowChoice || row.rowType == configRowBoolean || row.rowType == configRowMode {
		view.setMessage(i18n.T("tui.enum_keys_only"), false)
		return
	}
	if row.appID != "" && len(row.value) > tuiMaxEditValueBytes {
		view.setMessage(i18n.T("tui.edit_too_long", tuiMaxEditValueBytes), true)
		return
	}
	view.editing = true
	view.editValue = row.value
	view.editCursor = utf8.RuneCountInString(view.editValue)
}

func selectedApplicationConfigRows(view *tuiModel) []configRow {
	rows := configRows(&view.working)
	if len(rows) == 0 {
		return nil
	}
	view.configIndex = max(0, min(len(rows)-1, view.configIndex))
	if rows[view.configIndex].rowType != configRowApplication {
		return nil
	}
	fields := applicationConfigRows(&view.working, rows[view.configIndex].appID)
	if len(fields) > 0 {
		view.appFieldIndex = max(0, min(len(fields)-1, view.appFieldIndex))
	}
	return fields
}

func selectedConfigEditRow(view *tuiModel) (configRow, bool) {
	if view.configAppFocus {
		fields := selectedApplicationConfigRows(view)
		if len(fields) == 0 {
			return configRow{}, false
		}
		return fields[view.appFieldIndex], true
	}
	rows := configRows(&view.working)
	if len(rows) == 0 {
		return configRow{}, false
	}
	view.configIndex = max(0, min(len(rows)-1, view.configIndex))
	return rows[view.configIndex], true
}

func findConfigRowIndex(rows []configRow, key string) int {
	for index, row := range rows {
		if row.key == key {
			return index
		}
	}
	return max(0, len(rows)-1)
}

type configRowType string

const (
	configRowApplication configRowType = "app"
	configRowBoolean     configRowType = "bool"
	configRowChoice      configRowType = "choice"
	configRowInteger     configRowType = "int"
	configRowLanguage    configRowType = "language"
	configRowList        configRowType = "list"
	configRowMode        configRowType = "mode"
	configRowReadOnly    configRowType = "readonly"
	configRowString      configRowType = "string"
)

type configRow struct {
	key     string
	label   string
	value   string
	rowType configRowType
	min     int
	max     int
	appID   string
	field   string
	choices []string
	section configSection
}

type configSection string

const (
	configSectionBasic       configSection = "basic"
	configSectionHTTP        configSection = "http"
	configSectionDownload    configSection = "download"
	configSectionProvider    configSection = "provider"
	configSectionScan        configSection = "scan"
	configSectionApplication configSection = "application"
)

func configSectionTitle(section configSection, appCount int) string {
	if section == configSectionApplication {
		return i18n.T("tui.config.section.applications", appCount)
	}
	return i18n.T("tui.config.section." + string(section))
}

func configRows(catalog *model.Config) []configRow {
	http := catalog.Settings.HTTP
	if http == nil {
		http = &model.HTTPSettings{
			TimeoutSeconds:        model.DefaultHTTPTimeoutSeconds,
			MaxConcurrencyPerHost: model.DefaultHTTPMaxConcurrencyPerHost,
			Retries:               model.DefaultHTTPRetries,
		}
		catalog.Settings.HTTP = http
	}
	rows := []configRow{
		{key: "language", label: i18n.T("tui.config.language"), value: catalog.Settings.Language, rowType: configRowLanguage, section: configSectionBasic},
		{key: "timeout", label: i18n.T("tui.config.timeout"), value: strconv.Itoa(catalog.Settings.TimeoutSeconds), rowType: configRowInteger, min: 1, max: model.MaxTimeoutSeconds, section: configSectionBasic},
		{key: "workers", label: i18n.T("tui.config.workers"), value: strconv.Itoa(catalog.Settings.Workers), rowType: configRowInteger, min: model.MinWorkers, max: model.MaxWorkers, section: configSectionBasic},
		{key: "log_dir", label: i18n.T("tui.config.log_dir"), value: catalog.Settings.LogDir, rowType: configRowString, section: configSectionBasic},
		{key: "log_level", label: i18n.T("tui.config.log_level"), value: catalog.Settings.LogLevel, rowType: configRowChoice, choices: []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR"}, section: configSectionBasic},
		{key: "http_timeout", label: i18n.T("tui.config.http_timeout"), value: strconv.Itoa(http.TimeoutSeconds), rowType: configRowInteger, min: 1, max: model.MaxHTTPTimeoutSeconds, section: configSectionHTTP},
		{key: "http_concurrency", label: i18n.T("tui.config.http_concurrency"), value: strconv.Itoa(http.MaxConcurrencyPerHost), rowType: configRowInteger, min: 1, max: model.MaxHTTPConcurrencyPerHost, section: configSectionHTTP},
		{key: "http_retries", label: i18n.T("tui.config.http_retries"), value: strconv.Itoa(http.Retries), rowType: configRowInteger, min: 0, max: model.MaxHTTPRetries, section: configSectionHTTP},
		{key: "downloader_cli", label: i18n.T("tui.config.downloader_cli"), value: catalog.Settings.Downloader.CLI, rowType: configRowString, section: configSectionDownload},
		{key: "downloader_store_path", label: i18n.T("tui.config.downloader_store_path"), value: catalog.Settings.Downloader.StorePath, rowType: configRowString, section: configSectionDownload},
		{key: "downloader_extra_args", label: i18n.T("tui.config.downloader_extra_args"), value: strings.Join(catalog.Settings.Downloader.ExtraArgs, ", "), rowType: configRowList, section: configSectionDownload},
	}
	keys := make([]string, 0, len(catalog.Settings.ProviderURLs))
	for key := range catalog.Settings.ProviderURLs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rows = append(rows, configRow{key: "provider:" + key, label: i18n.T("tui.config.provider", key), value: catalog.Settings.ProviderURLs[key], rowType: configRowString, section: configSectionProvider})
	}
	rows = append(rows,
		configRow{key: "scan_path", label: i18n.T("tui.config.scan_path"), value: boolText(catalog.Settings.Scan.Path), rowType: configRowBoolean, section: configSectionScan},
		configRow{key: "scan_application", label: i18n.T("tui.config.scan_application"), value: boolText(catalog.Settings.Scan.Application), rowType: scanApplicationRowType(), section: configSectionScan},
		configRow{key: "scan_python", label: i18n.T("tui.config.scan_python"), value: boolText(catalog.Settings.Scan.Packages.Python), rowType: configRowBoolean, section: configSectionScan},
		configRow{key: "scan_node", label: i18n.T("tui.config.scan_node"), value: boolText(catalog.Settings.Scan.Packages.Node), rowType: configRowBoolean, section: configSectionScan},
		configRow{key: "scan_go", label: i18n.T("tui.config.scan_go"), value: boolText(catalog.Settings.Scan.Packages.Go), rowType: configRowBoolean, section: configSectionScan},
		configRow{key: "scan_uv", label: i18n.T("tui.config.scan_uv"), value: boolText(catalog.Settings.Scan.Packages.UV), rowType: configRowBoolean, section: configSectionScan},
		configRow{key: "scan_ruby", label: i18n.T("tui.config.scan_ruby"), value: boolText(catalog.Settings.Scan.Packages.Ruby), rowType: configRowBoolean, section: configSectionScan},
		configRow{key: "scan_bundle_id", label: i18n.T("tui.config.scan_bundle_id"), value: strings.Join(catalog.Settings.Scan.BundleID, ", "), rowType: configRowList, section: configSectionScan},
		configRow{key: "scan_exclude", label: i18n.T("tui.config.scan_exclude"), value: strings.Join(catalog.Settings.Scan.Exclude, ", "), rowType: configRowList, section: configSectionScan},
	)
	for _, app := range catalog.Apps {
		rows = append(rows, configRow{
			key: "app:" + app.ID, label: i18n.T("tui.config.app", app.Name),
			value: UpdateModeLabel(app.UpdateMode) + " · " + string(app.Provider.Type), rowType: configRowApplication, appID: app.ID, section: configSectionApplication,
		})
	}
	return rows
}

func scanApplicationRowType() configRowType {
	if !tuiSettingsSupportApplicationBundles() {
		return configRowReadOnly
	}
	return configRowBoolean
}

var tuiSettingsSupportApplicationBundles = func() bool { return runtimeutil.HostPlatform().SupportsApplicationBundles() }

func applicationConfigRows(catalog *model.Config, appID string) []configRow {
	app, exists := findApplication(catalog, appID)
	if !exists {
		return nil
	}
	row := func(field, label, value string, rowType configRowType) configRow {
		return configRow{key: "app:" + appID + ":" + field, label: label, value: value, rowType: rowType, appID: appID, field: field}
	}
	download := app.Provider.DownloadAction()
	if download == nil {
		download = &model.Download{}
	}
	return []configRow{
		row("id", "ID", app.ID, configRowReadOnly),
		row("name", i18n.T("label.name"), app.Name, configRowString),
		{key: "app:" + appID + ":type", label: i18n.T("label.type"), value: app.Type, rowType: configRowChoice, appID: appID, field: "type", choices: applicationTypeChoices(app.Type)},
		row("description", i18n.T("tui.config.app_description"), app.Description, configRowString),
		row("url", "URL", app.URL, configRowString),
		row("install_path", i18n.T("label.install_path"), app.InstallPath, configRowString),
		row("enabled", i18n.T("label.enabled"), boolText(app.Enabled), configRowBoolean),
		row("update_mode", i18n.T("label.update_mode"), UpdateModeLabel(app.UpdateMode), configRowMode),
		{key: "app:" + appID + ":provider", label: i18n.T("label.provider"), value: string(app.Provider.Type), rowType: configRowChoice, appID: appID, field: "provider", choices: providerChoices(string(app.Provider.Type))},
		row("package", i18n.T("tui.config.app_package"), app.Package, configRowString),
		row("action_version", i18n.T("tui.config.app_command_version"), app.Provider.VersionAction(), configRowString),
		row("action_check", i18n.T("tui.config.app_command_check"), app.Provider.CheckAction(), configRowString),
		row("action_update", i18n.T("tui.config.app_command_update"), app.Provider.UpdateAction(), configRowString),
		row("action_install", i18n.T("tui.config.app_action_install"), app.Provider.InstallAction(), configRowString),
		row("download_url", i18n.T("tui.config.app_download_url"), download.URL, configRowString),
		row("download_filename", i18n.T("tui.config.app_download_filename"), download.Filename, configRowString),
		row("download_store_path", i18n.T("tui.config.app_download_store_path"), download.StorePath, configRowString),
		row("download_checksum_enabled", i18n.T("tui.config.app_download_checksum_enabled"), boolText(download.ChecksumEnabled), configRowBoolean),
		row("download_checksum_url", i18n.T("tui.config.app_download_checksum_url"), download.ChecksumURL, configRowString),
		row("download_checksum_value", i18n.T("tui.config.app_download_checksum_value"), download.ChecksumValue, configRowString),
		row("download_extra_args", i18n.T("tui.config.app_download_extra_args"), strings.Join(download.ExtraArgs, ", "), configRowList),
		row("environment", i18n.T("tui.config.app_environment"), formatTUIEnvironment(app.Environment), configRowString),
		row("identity", i18n.T("tui.config.app_identity"), app.Identity, configRowString),
		row("scan_managed", i18n.T("tui.scan.managed"), boolText(app.ScanManaged), configRowBoolean),
	}
}

func configRowModified(current configRow, working, baseline *model.Config) bool {
	if current.rowType == configRowApplication {
		return configApplicationModified(working, baseline, current.appID)
	}
	for _, row := range configRows(baseline) {
		if row.key == current.key {
			return row.value != current.value || row.label != current.label
		}
	}
	return true
}

func configApplicationModified(working, baseline *model.Config, appID string) bool {
	current := applicationConfigRows(working, appID)
	saved := applicationConfigRows(baseline, appID)
	if len(current) != len(saved) {
		return true
	}
	savedValues := make(map[string]string, len(saved))
	for _, row := range saved {
		savedValues[row.key] = row.value
	}
	for _, row := range current {
		if value, exists := savedValues[row.key]; !exists || value != row.value {
			return true
		}
	}
	return false
}

func applicationTypeChoices(current string) []string {
	return includeCurrentChoice([]string{
		model.ApplicationTypeCLI,
		model.ApplicationTypeBundle,
		model.ApplicationTypePackage,
		model.ApplicationTypeSDK,
	}, current)
}

func providerChoices(current string) []string {
	return includeCurrentChoice([]string{
		string(model.ProviderDefault), string(model.ProviderGitHubRelease), string(model.ProviderGitHubTag), string(model.ProviderNPM), string(model.ProviderPyPI), string(model.ProviderUV), string(model.ProviderJetBrains), string(model.ProviderGo), string(model.ProviderNodeLTS), string(model.ProviderSparkle),
	}, current)
}

func includeCurrentChoice(choices []string, current string) []string {
	current = strings.TrimSpace(current)
	for _, choice := range choices {
		if choice == current {
			return choices
		}
	}
	if current != "" {
		return append(choices, current)
	}
	return choices
}

func adjacentChoice(current string, choices []string, delta int) (string, bool) {
	if len(choices) == 0 {
		return "", false
	}
	index := 0
	for candidate, choice := range choices {
		if choice == current {
			index = candidate
			break
		}
	}
	return choices[(index+delta+len(choices))%len(choices)], true
}

func findApplication(catalog *model.Config, appID string) (*model.Application, bool) {
	for index := range catalog.Apps {
		if catalog.Apps[index].ID == appID {
			return &catalog.Apps[index], true
		}
	}
	return nil, false
}

func boolText(value bool) string {
	if value {
		return i18n.T("value.yes")
	}
	return i18n.T("value.no")
}

func adjustConfig(view *tuiModel, delta int) {
	row, exists := selectedConfigEditRow(view)
	if !exists {
		return
	}
	switch row.rowType {
	case configRowInteger:
		value, _ := strconv.Atoi(row.value)
		value = max(row.min, min(row.max, value+delta))
		_ = setConfigValue(&view.working, row, strconv.Itoa(value))
		view.dirty = true
	case configRowBoolean:
		_ = setConfigValue(&view.working, row, boolText(row.value != i18n.T("value.yes")))
		view.dirty = true
	case configRowChoice:
		value, exists := adjacentChoice(row.value, row.choices, delta)
		if !exists {
			return
		}
		_ = setConfigValue(&view.working, row, value)
		view.dirty = true
	case configRowLanguage:
		value := "zh"
		if row.value == "zh" {
			value = "en"
		}
		_ = setConfigValue(&view.working, row, value)
		view.dirty = true
	case configRowMode:
		modes := []model.UpdateMode{model.ModeAuto, model.ModeDownload, model.ModeCheck, model.ModeInstall}
		current, _ := parseUpdateMode(row.value)
		index := 0
		for candidate := range modes {
			if modes[candidate] == current {
				index = candidate
				break
			}
		}
		index = (index + delta + len(modes)) % len(modes)
		_ = setConfigValue(&view.working, row, string(modes[index]))
		view.dirty = true
	}
	if view.dirty {
		view.setMessage(i18n.T("tui.unsaved"), false)
	}
}

func applyConfigEdit(view *tuiModel, value string) error {
	row, exists := selectedConfigEditRow(view)
	if !exists {
		return errors.New(i18n.T("tui.config_selection_missing"))
	}
	if row.rowType == configRowInteger {
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || number < row.min || number > row.max {
			return errors.New(i18n.T("tui.invalid_range", row.min, row.max))
		}
	}
	if row.rowType == configRowBoolean {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized != strings.ToLower(i18n.T("value.yes")) && normalized != strings.ToLower(i18n.T("value.no")) && normalized != "true" && normalized != "false" && normalized != "1" && normalized != "0" {
			return errors.New(i18n.T("tui.invalid_bool"))
		}
	}
	if row.rowType == configRowMode {
		if _, exists := parseUpdateMode(value); !exists {
			return errors.New(i18n.T("tui.invalid_mode"))
		}
	}
	if row.appID != "" {
		normalized, err := normalizeTUIApplicationField(row, value)
		if err != nil {
			return err
		}
		value = normalized
	}
	return setConfigValue(&view.working, row, value)
}

func applyActiveTUIEdit(view *tuiModel, value string) error {
	if view.page == tuiScan && view.scanEditFocus {
		return applyScanCandidateConfigEdit(view, value)
	}
	return applyConfigEdit(view, value)
}

func setConfigValue(catalog *model.Config, row configRow, value string) error {
	if row.appID != "" && row.field != "" {
		return setApplicationConfigValue(catalog, row, value)
	}
	return setSettingsConfigValue(catalog, row, value)
}

func setApplicationConfigValue(catalog *model.Config, row configRow, value string) error {
	app, exists := findApplication(catalog, row.appID)
	if !exists {
		return errors.New(i18n.T("tui.config_selection_missing"))
	}
	boolean := value == i18n.T("value.yes") || strings.EqualFold(value, "true") || value == "1"
	switch row.field {
	case "id":
		return errors.New(i18n.T("tui.readonly"))
	case "name":
		app.Name = value
	case "type":
		app.Type = value
	case "description":
		app.Description = value
	case "url":
		app.URL = value
	case "install_path":
		app.InstallPath = value
	case "enabled":
		app.Enabled = boolean
	case "update_mode":
		mode, valid := parseUpdateMode(value)
		if !valid {
			return errors.New(i18n.T("tui.invalid_mode"))
		}
		app.UpdateMode = mode
	case "provider":
		app.Provider.Type = model.ProviderType(value)
	case "package":
		app.Package = value
	case "action_version", "action_check", "action_update", "action_install":
		if app.Provider.Actions == nil {
			app.Provider.Actions = &model.ProviderActions{}
		}
		switch row.field {
		case "action_version":
			app.Provider.Actions.Version = value
		case "action_check":
			app.Provider.Actions.Check = value
		case "action_update":
			app.Provider.Actions.Update = value
		case "action_install":
			app.Provider.Actions.Install = value
		}
		if !app.Provider.HasActions() {
			app.Provider.Actions = nil
		}
	case "download_url", "download_filename", "download_store_path", "download_checksum_enabled", "download_checksum_url", "download_checksum_value", "download_extra_args":
		return setApplicationDownloadField(app, row.field, value, boolean)
	case "environment":
		environment, err := parseTUIEnvironment(value)
		if err != nil {
			return err
		}
		app.Environment = environment
	case "identity":
		app.Identity = value
	case "scan_managed":
		app.ScanManaged = boolean
	default:
		return errors.New(i18n.T("tui.config_selection_missing"))
	}
	return nil
}

func setApplicationDownloadField(app *model.Application, field, value string, boolean bool) error {
	download := model.Download{}
	if current := app.Provider.DownloadAction(); current != nil {
		download = *current
		download.ExtraArgs = append([]string(nil), current.ExtraArgs...)
	}
	switch field {
	case "download_url":
		download.URL = value
	case "download_filename":
		download.Filename = value
	case "download_store_path":
		download.StorePath = value
	case "download_checksum_enabled":
		download.ChecksumEnabled = boolean
	case "download_checksum_url":
		download.ChecksumURL = value
	case "download_checksum_value":
		download.ChecksumValue = value
	case "download_extra_args":
		download.ExtraArgs = parseTUIList(value)
	}
	if download.URL == "" && download.Filename == "" && download.StorePath == "" && !download.ChecksumEnabled && download.ChecksumURL == "" && download.ChecksumValue == "" && len(download.ExtraArgs) == 0 {
		if app.Provider.Actions != nil {
			app.Provider.Actions.Download = nil
			if !app.Provider.HasActions() {
				app.Provider.Actions = nil
			}
		}
		return nil
	}
	if app.Provider.Actions == nil {
		app.Provider.Actions = &model.ProviderActions{}
	}
	app.Provider.Actions.Download = &download
	return nil
}

func setSettingsConfigValue(catalog *model.Config, row configRow, value string) error {
	http := catalog.Settings.HTTP
	if http == nil {
		http = &model.HTTPSettings{
			TimeoutSeconds:        model.DefaultHTTPTimeoutSeconds,
			MaxConcurrencyPerHost: model.DefaultHTTPMaxConcurrencyPerHost,
			Retries:               model.DefaultHTTPRetries,
		}
		catalog.Settings.HTTP = http
	}
	integer, _ := strconv.Atoi(strings.TrimSpace(value))
	boolean := value == i18n.T("value.yes") || strings.EqualFold(value, "true") || value == "1"
	switch row.key {
	case "language":
		if value != "zh" && value != "en" {
			return errors.New(i18n.T("config.language_invalid"))
		}
		catalog.Settings.Language = value
	case "timeout":
		catalog.Settings.TimeoutSeconds = integer
	case "workers":
		catalog.Settings.Workers = integer
	case "http_timeout":
		http.TimeoutSeconds = integer
	case "http_concurrency":
		http.MaxConcurrencyPerHost = integer
	case "http_retries":
		http.Retries = integer
	case "downloader_cli":
		catalog.Settings.Downloader.CLI = value
	case "downloader_store_path":
		catalog.Settings.Downloader.StorePath = value
	case "downloader_extra_args":
		catalog.Settings.Downloader.ExtraArgs = parseTUIList(value)
	case "log_dir":
		catalog.Settings.LogDir = value
	case "log_level":
		catalog.Settings.LogLevel = strings.ToUpper(strings.TrimSpace(value))
	case "scan_path":
		catalog.Settings.Scan.Path = boolean
	case "scan_application":
		catalog.Settings.Scan.Application = boolean
	case "scan_python":
		catalog.Settings.Scan.Packages.Python = boolean
	case "scan_node":
		catalog.Settings.Scan.Packages.Node = boolean
	case "scan_go":
		catalog.Settings.Scan.Packages.Go = boolean
	case "scan_uv":
		catalog.Settings.Scan.Packages.UV = boolean
	case "scan_ruby":
		catalog.Settings.Scan.Packages.Ruby = boolean
	case "scan_bundle_id":
		catalog.Settings.Scan.BundleID = parseTUIList(value)
	case "scan_exclude":
		catalog.Settings.Scan.Exclude = parseTUIList(value)
	default:
		if strings.HasPrefix(row.key, "provider:") {
			catalog.Settings.ProviderURLs[strings.TrimPrefix(row.key, "provider:")] = value
		}
	}
	return nil
}

func parseTUIList(value string) []string {
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func formatTUIEnvironment(environment map[string]string) string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]string, 0, len(keys))
	for _, key := range keys {
		items = append(items, key+"="+environment[key])
	}
	return strings.Join(items, ",")
}

func parseTUIEnvironment(value string) (map[string]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	environment := make(map[string]string)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		key, itemValue, exists := strings.Cut(item, "=")
		key, itemValue = strings.TrimSpace(key), strings.TrimSpace(itemValue)
		if !exists || key == "" {
			return nil, errors.New(i18n.T("tui.environment_format_invalid"))
		}
		if !validTUIEnvironmentName(key) {
			return nil, errors.New(i18n.T("tui.environment_name_invalid", key))
		}
		if _, duplicate := environment[key]; duplicate {
			return nil, errors.New(i18n.T("tui.environment_duplicate", key))
		}
		environment[key] = itemValue
	}
	return environment, nil
}

func validTUIEnvironmentName(value string) bool {
	for index, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return value != ""
}

func parseUpdateMode(value string) (model.UpdateMode, bool) {
	normalized := strings.TrimSpace(value)
	for _, mode := range []model.UpdateMode{model.ModeAuto, model.ModeDownload, model.ModeCheck, model.ModeInstall} {
		if strings.EqualFold(normalized, string(mode)) || strings.EqualFold(normalized, UpdateModeLabel(mode)) {
			return mode, true
		}
	}
	return "", false
}

func normalizeTUIApplicationField(row configRow, value string) (string, error) {
	trim := row.rowType == configRowString || row.rowType == configRowList
	required := applicationConfigFieldRequired(row.field)
	normalized, valid := component.NormalizeApplicationValue(value, trim, required, tuiMaxEditValueBytes)
	if !valid && len(value) > tuiMaxEditValueBytes {
		return "", errors.New(i18n.T("tui.edit_too_long", tuiMaxEditValueBytes))
	}
	if !valid {
		return "", errors.New(i18n.T("tui.scan.required_field", row.label))
	}
	return normalized, nil
}

func validateTUIApplication(application model.Application) error {
	required := []struct {
		label string
		value string
	}{
		{i18n.T("label.name"), application.Name},
		{i18n.T("label.type"), application.Type},
		{i18n.T("label.provider"), string(application.Provider.Type)},
		{i18n.T("label.install_path"), application.InstallPath},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return errors.New(i18n.T("tui.scan.required_field", field.label))
		}
	}
	for _, row := range applicationConfigRows(&model.Config{Apps: []model.Application{application}}, application.ID) {
		if len(row.value) > tuiMaxEditValueBytes {
			return errors.New(i18n.T("tui.edit_too_long", tuiMaxEditValueBytes))
		}
	}
	if len(formatTUIEnvironment(application.Environment)) > tuiMaxEditValueBytes {
		return errors.New(i18n.T("tui.edit_too_long", tuiMaxEditValueBytes))
	}
	return nil
}

func validateTUIApplicationCatalog(catalog model.Config) error {
	identities := make(map[string]string, len(catalog.Apps))
	for _, application := range catalog.Apps {
		if err := validateTUIApplication(application); err != nil {
			return err
		}
		identity := strings.ToLower(strings.TrimSpace(application.Identity))
		if identity == "" {
			continue
		}
		if existingID, exists := identities[identity]; exists {
			return errors.New(i18n.T("config.app_identity_duplicate", application.Identity, existingID))
		}
		identities[identity] = application.ID
	}
	return nil
}

func applicationConfigFieldRequired(field string) bool {
	switch field {
	case "name", "type", "provider", "install_path":
		return true
	default:
		return false
	}
}
