package version

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Available marks a provider result that can confirm an update exists without
// reporting a comparable target version.
const Available = "available"

// ErrExtractFailed is returned when text contains no comparable dotted version.
var ErrExtractFailed = errors.New("version extraction failed")

var (
	defaultPattern    = regexp.MustCompile(`(?i)(?:version\s*)?v?(\d+(?:\.\d+){1,5}(?:[-+._][0-9A-Za-z.-]+)?)`)
	numberPattern     = regexp.MustCompile(`\d+(?:\.\d+)+`)
	identifierPattern = regexp.MustCompile(`[A-Za-z]+|\d+`)
)

type parsedVersion struct {
	core       []int
	prerelease []string
}

// Extract returns the first recognizable dotted version in text.
func Extract(text string) (string, error) {
	match := defaultPattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return "", ErrExtractFailed
	}
	return Normalize(match[1]), nil
}

// Normalize removes common release and version prefixes.
func Normalize(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	for _, prefix := range []string{"release-", "release/", "version-", "version/"} {
		if strings.HasPrefix(lower, prefix) {
			value = value[len(prefix):]
			break
		}
	}
	if len(value) > 1 && (value[0] == 'v' || value[0] == 'V') && value[1] >= '0' && value[1] <= '9' {
		value = value[1:]
	}
	return strings.TrimSpace(value)
}

// Compare compares versions with numeric cores and common SemVer-style
// prerelease suffixes. The boolean is false only when either value has no
// comparable dotted numeric core.
func Compare(left, right string) (int, bool) {
	l, lok := parse(left)
	r, rok := parse(right)
	if !lok || !rok {
		return 0, false
	}
	width := len(l.core)
	if len(r.core) > width {
		width = len(r.core)
	}
	for i := 0; i < width; i++ {
		var lv, rv int
		if i < len(l.core) {
			lv = l.core[i]
		}
		if i < len(r.core) {
			rv = r.core[i]
		}
		if lv < rv {
			return -1, true
		}
		if lv > rv {
			return 1, true
		}
	}
	return comparePrerelease(l.prerelease, r.prerelease), true
}

// IsNewer reports whether latest should replace current.
func IsNewer(latest, current string) bool {
	if latest == Available {
		return true
	}
	if comparison, ok := Compare(latest, current); ok {
		return comparison > 0
	}
	return latest != "" && current != "" && latest != current
}

// AtLeast reports whether installed satisfies the expected version.
func AtLeast(installed, expected string) bool {
	if comparison, ok := Compare(installed, expected); ok {
		return comparison >= 0
	}
	return installed == expected
}

func parse(value string) (parsedVersion, bool) {
	value = Normalize(value)
	location := numberPattern.FindStringIndex(value)
	if location == nil {
		return parsedVersion{}, false
	}
	parts := strings.Split(value[location[0]:location[1]], ".")
	parsed := parsedVersion{core: make([]int, 0, len(parts))}
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return parsedVersion{}, false
		}
		parsed.core = append(parsed.core, number)
	}
	suffix := value[location[1]:]
	if suffix == "" || strings.HasPrefix(suffix, "+") || strings.TrimSpace(suffix) != suffix {
		return parsed, true
	}
	suffix = strings.TrimLeft(suffix, "-_.")
	if metadata := strings.IndexByte(suffix, '+'); metadata >= 0 {
		suffix = suffix[:metadata]
	}
	identifiers := identifierPattern.FindAllString(strings.ToLower(suffix), -1)
	if len(identifiers) == 1 && isFinalIdentifier(identifiers[0]) {
		return parsed, true
	}
	parsed.prerelease = identifiers
	return parsed, true
}

func comparePrerelease(left, right []string) int {
	if len(left) == 0 && len(right) == 0 {
		return 0
	}
	if len(left) == 0 {
		return 1
	}
	if len(right) == 0 {
		return -1
	}
	width := len(left)
	if len(right) > width {
		width = len(right)
	}
	for i := 0; i < width; i++ {
		if i >= len(left) {
			return -1
		}
		if i >= len(right) {
			return 1
		}
		comparison := compareIdentifier(left[i], right[i])
		if comparison != 0 {
			return comparison
		}
	}
	return 0
}

func compareIdentifier(left, right string) int {
	leftNumber, leftErr := strconv.Atoi(left)
	rightNumber, rightErr := strconv.Atoi(right)
	if leftErr == nil && rightErr == nil {
		if leftNumber < rightNumber {
			return -1
		}
		if leftNumber > rightNumber {
			return 1
		}
		return 0
	}
	if leftErr == nil {
		return -1
	}
	if rightErr == nil {
		return 1
	}
	leftRank, leftKnown := qualifierRank(left)
	rightRank, rightKnown := qualifierRank(right)
	if leftKnown && rightKnown && leftRank != rightRank {
		if leftRank < rightRank {
			return -1
		}
		return 1
	}
	return strings.Compare(left, right)
}

func qualifierRank(value string) (int, bool) {
	switch value {
	case "dev", "snapshot":
		return 0, true
	case "a", "alpha":
		return 1, true
	case "b", "beta":
		return 2, true
	case "pre", "preview":
		return 3, true
	case "rc":
		return 4, true
	default:
		return 0, false
	}
}

func isFinalIdentifier(value string) bool {
	return value == "final" || value == "release" || value == "stable"
}
