package config

import (
	"testing"

	"github.com/eoctet/tendkit/pkg/i18n"
)

func TestLogLevelIsStrictAndNormalized(t *testing.T) {
	catalog := defaultConfig()
	catalog.Settings.LogLevel = "warn"
	normalizeConfig(&catalog)
	if catalog.Settings.LogLevel != "WARN" {
		t.Fatalf("normalized level = %q", catalog.Settings.LogLevel)
	}
	if err := validateConfig(catalog); err != nil {
		t.Fatal(err)
	}
	catalog.Settings.LogLevel = "verbose"
	if err := validateConfig(catalog); err == nil {
		t.Fatal("invalid log level accepted")
	}
}

func TestInvalidLogLevelErrorIsLocalized(t *testing.T) {
	original := i18n.Current()
	t.Cleanup(func() { i18n.Set(original) })
	messages := make([]string, 0, 2)
	for _, language := range []i18n.Language{i18n.English, i18n.Chinese} {
		i18n.Set(language)
		catalog := defaultConfig()
		catalog.Settings.LogLevel = "verbose"
		err := validateConfig(catalog)
		want := i18n.T("config.log_level_invalid", "verbose")
		if err == nil || err.Error() != want {
			t.Fatalf("%s localized log-level error = %v", language, err)
		}
		messages = append(messages, want)
	}
	if messages[0] == messages[1] {
		t.Fatalf("log-level errors are not localized: %q", messages[0])
	}
}
