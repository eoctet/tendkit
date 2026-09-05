package model

import (
	"maps"
	"slices"
)

// CloneConfig returns a configuration copy whose mutable maps, slices, and
// pointers can be changed without affecting the source configuration.
func CloneConfig(value Config) Config {
	copied := value
	if value.Settings.HTTP != nil {
		httpSettings := *value.Settings.HTTP
		copied.Settings.HTTP = &httpSettings
	}
	copied.Settings.ProviderURLs = maps.Clone(value.Settings.ProviderURLs)
	copied.Settings.Downloader.ExtraArgs = cloneOmitEmptyStrings(value.Settings.Downloader.ExtraArgs)
	copied.Settings.Scan.BundleID = slices.Clone(value.Settings.Scan.BundleID)
	copied.Settings.Scan.Exclude = slices.Clone(value.Settings.Scan.Exclude)

	if value.Apps != nil {
		copied.Apps = make([]Application, len(value.Apps))
		for index, application := range value.Apps {
			copied.Apps[index] = cloneApplication(application)
		}
	}
	if value.ScanVersionControl != nil {
		copied.ScanVersionControl = make(map[string]map[string]ScanKeepResolution, len(value.ScanVersionControl))
		for applicationID, fields := range value.ScanVersionControl {
			copied.ScanVersionControl[applicationID] = maps.Clone(fields)
		}
	}
	return copied
}

func cloneApplication(value Application) Application {
	copied := value
	copied.Environment = cloneOmitEmptyStringMap(value.Environment)
	if value.Provider.Actions != nil {
		actions := *value.Provider.Actions
		if value.Provider.Actions.Download != nil {
			download := *value.Provider.Actions.Download
			download.ExtraArgs = cloneOmitEmptyStrings(value.Provider.Actions.Download.ExtraArgs)
			actions.Download = &download
		}
		copied.Provider.Actions = &actions
	}
	return copied
}

func cloneOmitEmptyStrings(value []string) []string {
	if len(value) == 0 {
		return nil
	}
	return slices.Clone(value)
}

func cloneOmitEmptyStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	return maps.Clone(value)
}
