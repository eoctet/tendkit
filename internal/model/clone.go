package model

// CloneConfig returns a configuration copy whose mutable maps, slices, and
// pointers can be changed without affecting the source configuration.
func CloneConfig(value Config) Config {
	copied := value
	if value.Settings.HTTP != nil {
		httpSettings := *value.Settings.HTTP
		copied.Settings.HTTP = &httpSettings
	}
	copied.Settings.ProviderURLs = cloneStringMap(value.Settings.ProviderURLs)
	copied.Settings.Downloader.ExtraArgs = cloneOmitEmptyStrings(value.Settings.Downloader.ExtraArgs)
	copied.Settings.Scan.BundleID = cloneStrings(value.Settings.Scan.BundleID)
	copied.Settings.Scan.Exclude = cloneStrings(value.Settings.Scan.Exclude)

	if value.Apps != nil {
		copied.Apps = make([]Application, len(value.Apps))
		for index, application := range value.Apps {
			copied.Apps[index] = cloneApplication(application)
		}
	}
	if value.ScanVersionControl != nil {
		copied.ScanVersionControl = make(map[string]map[string]ScanKeepResolution, len(value.ScanVersionControl))
		for applicationID, fields := range value.ScanVersionControl {
			if fields == nil {
				copied.ScanVersionControl[applicationID] = nil
				continue
			}
			fieldCopy := make(map[string]ScanKeepResolution, len(fields))
			for field, resolution := range fields {
				fieldCopy[field] = resolution
			}
			copied.ScanVersionControl[applicationID] = fieldCopy
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

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	copied := make(map[string]string, len(value))
	for key, item := range value {
		copied[key] = item
	}
	return copied
}

func cloneStrings(value []string) []string {
	if value == nil {
		return nil
	}
	copied := make([]string, len(value))
	copy(copied, value)
	return copied
}

func cloneOmitEmptyStrings(value []string) []string {
	if len(value) == 0 {
		return nil
	}
	return cloneStrings(value)
}

func cloneOmitEmptyStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	return cloneStringMap(value)
}
