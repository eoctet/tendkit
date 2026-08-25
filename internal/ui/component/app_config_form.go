package component

import "strings"

// NormalizeApplicationValue applies shared edit-size, trimming, and required-field rules.
func NormalizeApplicationValue(value string, trim, required bool, maxBytes int) (string, bool) {
	if len(value) > maxBytes {
		return "", false
	}
	if trim {
		value = strings.TrimSpace(value)
	}
	if required && value == "" {
		return "", false
	}
	return value, true
}
