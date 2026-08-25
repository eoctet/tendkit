package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/eoctet/tendkit/pkg/i18n"
)

func resolvePath(path, defaultPath string) (string, error) {
	if path == "" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("env.cwd_failed"), err)
		}
		path = filepath.Join(workingDirectory, defaultPath)
	} else if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("env.home_failed", path), err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("env.resolve_failed", path), err)
	}
	return filepath.Clean(absolute), nil
}

func loadEnvFile(path string, required bool, defaultPath string) (EnvLoadResult, error) {
	resolved, err := resolvePath(path, defaultPath)
	if err != nil {
		return EnvLoadResult{}, err
	}
	result := EnvLoadResult{Path: resolved}
	info, err := os.Stat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		if required {
			return result, fmt.Errorf("%s", i18n.T("env.missing", resolved))
		}
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("%s: %w", i18n.T("env.access_failed", resolved), err)
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("%s", i18n.T("env.not_regular", resolved))
	}
	result.Exists = true
	// #nosec G304 -- The user-selected env file path is read only after confirming it resolves to a regular file.
	content, err := os.ReadFile(resolved)
	if err != nil {
		return result, fmt.Errorf("%s: %w", i18n.T("env.read_failed", resolved), err)
	}
	content = []byte(strings.TrimPrefix(string(content), "\ufeff"))
	for index, original := range strings.Split(string(content), "\n") {
		lineNumber := index + 1
		line := strings.TrimSpace(strings.TrimSuffix(original, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		separator := strings.IndexByte(line, '=')
		if separator < 0 {
			return result, fmt.Errorf("%s", i18n.T("env.missing_equal", resolved, lineNumber))
		}
		key := strings.TrimSpace(line[:separator])
		if !environmentNamePattern.MatchString(key) {
			return result, fmt.Errorf("%s", i18n.T("env.invalid_name", resolved, lineNumber, key))
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value, parseErr := parseValue(line[separator+1:])
		if parseErr != nil {
			return result, fmt.Errorf("%s: %w", i18n.T("env.invalid_value", resolved, lineNumber), parseErr)
		}
		if setErr := os.Setenv(key, value); setErr != nil {
			return result, fmt.Errorf("%s: %w", i18n.T("env.set_failed", key), setErr)
		}
		result.Loaded++
	}
	return result, nil
}

func parseValue(raw string) (string, error) {
	var tokens []string
	var token strings.Builder
	started := false
	quote := rune(0)
	escaped := false

	flush := func() {
		if started {
			tokens = append(tokens, token.String())
			token.Reset()
			started = false
		}
	}

	for _, character := range raw {
		if escaped {
			token.WriteRune(character)
			started = true
			escaped = false
			continue
		}
		if quote != 0 {
			switch {
			case character == quote:
				quote = 0
			case character == '\\' && quote == '"':
				escaped = true
			default:
				token.WriteRune(character)
			}
			started = true
			continue
		}
		switch {
		case character == '#':
			flush()
			return strings.Join(tokens, " "), nil
		case character == '\\':
			escaped = true
			started = true
		case character == '\'' || character == '"':
			quote = character
			started = true
		case unicode.IsSpace(character):
			flush()
		default:
			token.WriteRune(character)
			started = true
		}
	}
	if escaped {
		return "", errors.New(i18n.T("env.trailing_escape"))
	}
	if quote != 0 {
		return "", errors.New(i18n.T("env.unclosed_quote"))
	}
	flush()
	return strings.Join(tokens, " "), nil
}
