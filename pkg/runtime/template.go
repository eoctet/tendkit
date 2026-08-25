package runtime

import (
	"strings"

	apperrors "github.com/eoctet/tendkit/pkg/errors"
)

// Render expands known placeholders and optionally shell-quotes their values.
func Render(text string, values map[string]string, shellQuote bool) (string, error) {
	var output strings.Builder
	for index := 0; index < len(text); {
		if text[index] != '{' {
			output.WriteByte(text[index])
			index++
			continue
		}
		if index+1 < len(text) && text[index+1] == '{' {
			close := strings.Index(text[index+2:], "}}")
			if close < 0 {
				return "", &apperrors.UnclosedPlaceholderError{}
			}
			close += index + 2
			// Go-template blocks are literal command text, not application
			// placeholders; preserve both braces exactly.
			output.WriteString(text[index : close+2])
			index = close + 2
			continue
		}
		end := strings.IndexByte(text[index+1:], '}')
		if end < 0 {
			return "", &apperrors.UnclosedPlaceholderError{}
		}
		end += index + 1
		key := text[index+1 : end]
		if !placeholderIdentifier(key) {
			output.WriteString(text[index : end+1])
			index = end + 1
			continue
		}
		value, ok := values[key]
		if !ok {
			return "", &apperrors.UnknownPlaceholderError{Key: key}
		}
		if shellQuote {
			value = QuoteShell(value)
		}
		output.WriteString(value)
		index = end + 1
	}
	return output.String(), nil
}

func placeholderIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || character == '_' {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

// QuoteShell returns one safely single-quoted shell token.
func QuoteShell(value string) string {
	if value == "" {
		return "''"
	}
	if shellSafeToken(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellSafeToken(value string) bool {
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("_@%+=:,./-", character) {
			continue
		}
		return false
	}
	return true
}
