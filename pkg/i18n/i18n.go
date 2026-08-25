package i18n

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	apperrors "github.com/eoctet/tendkit/pkg/errors"
	"github.com/eoctet/tendkit/pkg/version"
)

// Language identifies an embedded user-interface message catalog.
type Language string

const (
	Chinese Language = "zh"
	English Language = "en"
)

// ErrUnsupportedLanguage indicates that a language selector is not supported.
var ErrUnsupportedLanguage = errors.New("unsupported language")

//go:embed locales/*.json
var localeFiles embed.FS

//go:embed locales/banner.txt
var bannerText string

var (
	catalogs        = loadCatalogs()
	reverseCatalogs = loadReverseCatalogs()
	patterns        = loadPatterns()
	current         atomic.Value
)

type messagePattern struct {
	key         string
	expression  *regexp.Regexp
	conversions []byte
}

func init() {
	current.Store(Chinese)
}

// Parse converts a locale or language selector into a supported language.
func Parse(value string) (Language, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch {
	case normalized == "zh" || strings.HasPrefix(normalized, "zh-"):
		return Chinese, nil
	case normalized == "en" || strings.HasPrefix(normalized, "en-"):
		return English, nil
	default:
		return "", ErrUnsupportedLanguage
	}
}

// Detect resolves the preferred language from the process locale.
func Detect() Language {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			continue
		}
		if language, err := Parse(strings.SplitN(value, ".", 2)[0]); err == nil {
			return language
		}
	}
	return Chinese
}

// Set changes the process-wide language used by T.
func Set(language Language) {
	if language != Chinese && language != English {
		language = Chinese
	}
	current.Store(language)
}

// Current returns the process-wide language used by T.
func Current() Language {
	if language, ok := current.Load().(Language); ok {
		return language
	}
	return Chinese
}

// Banner returns the shared TendKit terminal logo without trailing line breaks.
func Banner() string { return strings.TrimRight(bannerText, "\r\n") }

// T formats one localized message by key.
func T(key string, values ...any) string {
	language := Current()
	template := catalogs[language][key]
	if template == "" {
		template = catalogs[Chinese][key]
	}
	if template == "" {
		template = key
	}
	if len(values) == 0 {
		return template
	}
	return fmt.Sprintf(template, values...)
}

// Localize translates a previously rendered built-in message into the active
// language. It leaves external command output and unknown text unchanged.
func Localize(message string) string {
	if strings.TrimSpace(message) == "" {
		return message
	}
	target := Current()
	for source := range catalogs {
		if source == target {
			continue
		}
		if key := reverseCatalogs[source][message]; key != "" {
			return catalogs[target][key]
		}
		for _, pattern := range patterns[source] {
			matches := pattern.expression.FindStringSubmatch(message)
			if matches == nil {
				continue
			}
			values, ok := patternValues(matches[1:], pattern.conversions)
			if ok {
				return T(pattern.key, values...)
			}
		}
	}
	return message
}

// loadReverseCatalogs indexes only unambiguous, already-rendered messages. A
// rendered message cannot safely recover its source key when multiple keys
// share its text but have different translations.
func loadReverseCatalogs() map[Language]map[string]string {
	result := make(map[Language]map[string]string, len(catalogs))
	for source, messages := range catalogs {
		index := make(map[string]string, len(messages))
		for key, text := range messages {
			if _, conversions := formatPattern(text); len(conversions) != 0 {
				continue
			}
			if previous, found := index[text]; found {
				if catalogs[English][key] != catalogs[English][previous] || catalogs[Chinese][key] != catalogs[Chinese][previous] {
					index[text] = ""
				}
				continue
			}
			index[text] = key
		}
		result[source] = index
	}
	return result
}

func loadCatalogs() map[Language]map[string]string {
	result := make(map[Language]map[string]string, 2)
	for _, language := range []Language{Chinese, English} {
		content, err := localeFiles.ReadFile("locales/" + string(language) + ".json")
		if err != nil {
			panic(err)
		}
		messages := map[string]string{}
		if err := json.Unmarshal(content, &messages); err != nil {
			panic(err)
		}
		result[language] = messages
	}
	return result
}

func loadPatterns() map[Language][]messagePattern {
	result := make(map[Language][]messagePattern, len(catalogs))
	for language, messages := range catalogs {
		keys := make([]string, 0, len(messages))
		for key := range messages {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			expression, conversions := formatPattern(messages[key])
			if len(conversions) == 0 {
				continue
			}
			result[language] = append(result[language], messagePattern{key: key, expression: expression, conversions: conversions})
		}
	}
	return result
}

func formatPattern(format string) (*regexp.Regexp, []byte) {
	var expression strings.Builder
	expression.WriteString("^")
	conversions := make([]byte, 0, 4)
	for index := 0; index < len(format); {
		if format[index] != '%' {
			start := index
			for index < len(format) && format[index] != '%' {
				index++
			}
			expression.WriteString(regexp.QuoteMeta(format[start:index]))
			continue
		}
		if index+1 < len(format) && format[index+1] == '%' {
			expression.WriteString("%")
			index += 2
			continue
		}
		conversionIndex := index + 1
		for conversionIndex < len(format) && !strings.ContainsRune("vTtbcdoOqxXUeEfgGsxp", rune(format[conversionIndex])) {
			conversionIndex++
		}
		if conversionIndex >= len(format) {
			expression.WriteString(regexp.QuoteMeta(format[index:]))
			break
		}
		conversion := format[conversionIndex]
		conversions = append(conversions, conversion)
		switch conversion {
		case 'd':
			expression.WriteString(`(-?[0-9]+)`)
		case 'q':
			expression.WriteString(`("(?:\\.|[^"\\])*")`)
		default:
			expression.WriteString(`(.*?)`)
		}
		index = conversionIndex + 1
	}
	expression.WriteString("$")
	return regexp.MustCompile(expression.String()), conversions
}

func patternValues(matches []string, conversions []byte) ([]any, bool) {
	if len(matches) != len(conversions) {
		return nil, false
	}
	values := make([]any, len(matches))
	for index, match := range matches {
		switch conversions[index] {
		case 'd':
			value, err := strconv.Atoi(match)
			if err != nil {
				return nil, false
			}
			values[index] = value
		case 'q':
			value, err := strconv.Unquote(match)
			if err != nil {
				return nil, false
			}
			values[index] = value
		default:
			values[index] = match
		}
	}
	return values, true
}

// ErrorText translates stable lower-layer errors at the presentation boundary.
func ErrorText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, version.ErrExtractFailed) {
		return T("version.extract_failed")
	}
	var start *apperrors.StartError
	if errors.As(err, &start) {
		return T("runner.start_failed") + ": " + start.Err.Error()
	}
	var idle *apperrors.IdleTimeoutError
	if errors.As(err, &idle) {
		return T("runner.idle_timeout", idle.Duration)
	}
	var unclosed *apperrors.UnclosedPlaceholderError
	if errors.As(err, &unclosed) {
		return T("template.unclosed")
	}
	var unknown *apperrors.UnknownPlaceholderError
	if errors.As(err, &unknown) {
		return T("template.unknown", unknown.Key)
	}
	var form *apperrors.ExtraArgumentFormError
	if errors.As(err, &form) {
		return T("download.extra_arg_form", form.Index)
	}
	var unsafe *apperrors.UnsafeExtraArgumentError
	if errors.As(err, &unsafe) {
		return T("download.extra_arg_unsafe", unsafe.Index, unsafe.Name)
	}
	return err.Error()
}
