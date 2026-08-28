package i18n

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	apperrors "github.com/eoctet/tendkit/pkg/errors"
	"github.com/eoctet/tendkit/pkg/version"
)

func TestI18NContract(t *testing.T) {
	t.Run("parse-language-aliases", func(t *testing.T) {
		for value, expected := range map[string]Language{
			"zh": Chinese, "zh-CN": Chinese, "zh_CN": Chinese,
			"en": English, "en-US": English, "en_GB": English,
		} {
			actual, err := Parse(value)
			if err != nil || actual != expected {
				t.Errorf("Parse(%q) = %q, %v; want %q", value, actual, err, expected)
			}
		}
		if _, err := Parse("fr"); err == nil {
			t.Fatal("Parse(fr) should reject an unsupported language")
		}
	})
	t.Run("error-text-maps-shared-stable-errors", func(t *testing.T) {
		previous := Current()
		t.Cleanup(func() { Set(previous) })
		Set(English)
		for _, err := range []error{
			version.ErrExtractFailed,
			&apperrors.StartError{Err: errors.New("boom")},
			&apperrors.IdleTimeoutError{Duration: time.Second},
			&apperrors.UnclosedPlaceholderError{},
			&apperrors.UnknownPlaceholderError{Key: "name"},
			&apperrors.ExtraArgumentFormError{Index: 2},
			&apperrors.UnsafeExtraArgumentError{Index: 3, Name: "--output"},
		} {
			if text := ErrorText(err); text == "" || text == err.Error() {
				t.Errorf("ErrorText(%T) = %q", err, text)
			}
		}
		if ErrorText(nil) != "" {
			t.Fatal("nil error was not empty")
		}
		cause := errors.New("start cause")
		start := &apperrors.StartError{Err: cause}
		if !errors.Is(start, cause) || ErrorText(start) == "" {
			t.Fatalf("StartError lost cause/localization: %v", start)
		}
	})
	t.Run("detect-uses-locale-precedence", func(t *testing.T) {
		t.Setenv("LANG", "zh_CN.UTF-8")
		t.Setenv("LC_MESSAGES", "en_US.UTF-8")
		t.Setenv("LC_ALL", "")
		if actual := Detect(); actual != English {
			t.Fatalf("Detect() = %q, want en", actual)
		}
		t.Setenv("LC_ALL", "zh_CN.UTF-8")
		if actual := Detect(); actual != Chinese {
			t.Fatalf("Detect() = %q, want zh", actual)
		}
	})
	t.Run("catalogs-have-matching-keys", func(t *testing.T) {
		for key := range catalogs[Chinese] {
			if _, exists := catalogs[English][key]; !exists {
				t.Errorf("English catalog is missing %q", key)
			}
		}
		for key := range catalogs[English] {
			if _, exists := catalogs[Chinese][key]; !exists {
				t.Errorf("Chinese catalog is missing %q", key)
			}
		}
	})
	t.Run("banner-is-embedded-without-trailing-line-break", func(t *testing.T) {
		banner := Banner()
		lines := strings.Split(banner, "\n")
		if len(lines) != 6 {
			t.Fatalf("banner has %d lines, want 6", len(lines))
		}
		if strings.TrimSpace(banner) == "" || strings.HasSuffix(banner, "\n") || strings.HasSuffix(banner, "\r") {
			t.Fatalf("embedded banner has invalid boundaries: %q", banner)
		}
	})
	t.Run("catalog-formats-have-matching-arguments", func(t *testing.T) {
		for key, chinese := range catalogs[Chinese] {
			_, chineseConversions := formatPattern(chinese)
			_, englishConversions := formatPattern(catalogs[English][key])
			if !slices.Equal(chineseConversions, englishConversions) {
				t.Errorf("format arguments differ for %q: zh=%q en=%q", key, chineseConversions, englishConversions)
			}
		}
	})
	t.Run("configuration-terminology-is-consistent-across-languages", func(t *testing.T) {
		expected := map[string][2]string{
			"tui.config.downloader_cli":          {"下载工具", "Download tool"},
			"tui.config.downloader_extra_args":   {"下载扩展参数", "Download options"},
			"tui.config.app_download_extra_args": {"下载扩展参数", "Download options"},
			"tui.config.timeout":                 {"更新超时时间", "Update timeout"},
			"tui.config.workers":                 {"批量更新并发数", "Batch update concurrency"},
			"tui.config.http_concurrency":        {"HTTP 最大并发", "HTTP max concurrency"},
			"tui.config.scan_bundle_id":          {"macOS 应用白名单", "macOS application allowlist"},
			"tui.config.scan_go":                 {"扫描 Go 组件", "Scan Go components"},
			"tui.config.scan_uv":                 {"扫描 uv 包", "Scan uv packages"},
			"tui.config.scan_ruby":               {"扫描 Ruby 包", "Scan Ruby packages"},
		}
		for key, values := range expected {
			if actual := catalogs[Chinese][key]; actual != values[0] {
				t.Errorf("Chinese %s = %q, want %q", key, actual, values[0])
			}
			if actual := catalogs[English][key]; actual != values[1] {
				t.Errorf("English %s = %q, want %q", key, actual, values[1])
			}
		}
		for language, forbidden := range map[Language][]string{
			Chinese: {"Workers", "下载器"},
			English: {"WORKERS", "Worker pool", "Downloader"},
		} {
			for key, message := range catalogs[language] {
				if !strings.HasPrefix(key, "tui.") && key != "download.failed" && key != "log.run_started" {
					continue
				}
				for _, term := range forbidden {
					if strings.Contains(message, term) {
						t.Errorf("%s %s contains stale term %q: %q", language, key, term, message)
					}
				}
			}
		}
	})
	t.Run("tui-message-shortcut-names-use-uppercase-presentation", func(t *testing.T) {
		patterns := map[Language]*regexp.Regexp{
			Chinese: regexp.MustCompile(`按 [a-z](?:[ ，。；、/]|$)`),
			English: regexp.MustCompile(`(?i:press) [a-z](?:[ ,.;:/]|$)`),
		}
		for language, messages := range catalogs {
			for key, message := range messages {
				if !strings.HasPrefix(key, "tui.") {
					continue
				}
				if patterns[language].MatchString(message) {
					t.Errorf("%s %q contains a lowercase shortcut name: %q", language, key, message)
				}
			}
		}
	})
	t.Run("translation-uses-selected-language", func(t *testing.T) {
		previous := Current()
		t.Cleanup(func() { Set(previous) })
		Set(Chinese)
		if message := T("cli.help", "config", "lock", ".env", "user/.env"); !strings.Contains(message, "默认行为") {
			t.Fatalf("unexpected Chinese message %q", message)
		}
		Set(English)
		if message := T("cli.help", "config", "lock", ".env", "user/.env"); !strings.Contains(message, "Default behavior") {
			t.Fatalf("unexpected English message %q", message)
		}
	})
	t.Run("localize-previously-rendered-message", func(t *testing.T) {
		previous := Current()
		t.Cleanup(func() { Set(previous) })
		Set(English)
		message := Localize("检查命令退出码 1: Docker Desktop is not running")
		if message != "Check command exited with code 1: Docker Desktop is not running" {
			t.Fatalf("unexpected localized message %q", message)
		}
		Set(Chinese)
		message = Localize(`Installed version "1.0" did not reach expected version "2.0"`)
		if message != "更新后版本 \"1.0\" 未达到预期 \"2.0\"" {
			t.Fatalf("unexpected localized quoted message %q", message)
		}
	})
	t.Run("localize-leaves-unrecognized-text-unchanged", func(t *testing.T) {
		previous := Current()
		t.Cleanup(func() { Set(previous) })
		Set(English)
		for _, message := range []string{
			"upstream application returned a custom error",
			"扫描管理",
		} {
			if actual := Localize(message); actual != message {
				t.Errorf("Localize(%q) = %q, want original text", message, actual)
			}
		}
	})
}
