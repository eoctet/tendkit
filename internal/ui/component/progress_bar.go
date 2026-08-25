// Package component contains presentation primitives shared by TUI domains.
package component

import "strings"

// ProgressBar renders a fixed-width, presentation-only progress indicator.
func ProgressBar(percent, width int) string {
	if width < 3 {
		return ""
	}
	percent = max(0, min(100, percent))
	inner := width - 2
	filled := inner * percent / 100
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", inner-filled) + "]"
}
