package provider

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

// UVProvider delegates resolution to uv so its saved constraints, indexes and
// authentication remain the sole authority for installed tools.
type UVProvider struct{ Runner metadatautil.CommandRunner }

const uvComparableVersion = `\d+(?:\.\d+)+(?:[-+._][0-9A-Za-z][0-9A-Za-z.-]*)?`

var uvVersion = regexp.MustCompile(`^v?` + uvComparableVersion + `$`)
var uvOutdatedLine = regexp.MustCompile(`^([^\s]+)\s+v(` + uvComparableVersion + `)(?:\s+\[required:\s+[^\]]+\])?\s+\[latest:\s+v?(` + uvComparableVersion + `)\]$`)

func (p UVProvider) Latest(ctx context.Context, request Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", WrapError("provider.uv_latest_failed", err, request.App.Name)
	}
	app := request.App
	name, err := requiredPackage(request, CapabilityLatest)
	if err != nil {
		return "", err
	}
	normalizedName := model.NormalizeIdentityName(name)
	if app.Type != model.ApplicationTypePackage || normalizedName == "" {
		return "", NewError("provider.uv_invalid_target", app.Name)
	}
	current, valid := normalizeUVVersion(request.CurrentVersion)
	if !valid {
		return "", NewError("provider.uv_current_missing", app.Name)
	}
	manager, err := metadatautil.FindPackageManager(metadatautil.PackageUV)
	if err != nil {
		return "", WrapError("provider.uv_manager_unavailable", err, app.Name)
	}
	command := runtimeutil.QuoteShell(manager) + " tool list --outdated --show-version-specifiers --no-progress"
	result, err := p.Runner.Run(ctx, command, app.Environment)
	if err != nil {
		return "", WrapError("provider.uv_latest_failed", err, app.Name)
	}
	if result.ExitCode != 0 {
		return "", NewError("provider.uv_latest_exit", app.Name, result.ExitCode)
	}
	latest, err := parseUVOutdated(request.App.Package, result.Stdout)
	if err != nil {
		return "", WrapError("provider.uv_parse_failed", err, app.Name)
	}
	if latest == "" {
		return current, nil
	}
	return latest, nil
}

func parseUVOutdated(name, output string) (string, error) {
	target := model.NormalizeIdentityName(name)
	if target == "" {
		return "", errors.New("target package is empty")
	}
	var latest string
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		match := uvOutdatedLine.FindStringSubmatch(line)
		if len(match) == 0 {
			return "", errors.New("unrecognized uv outdated output")
		}
		if _, valid := normalizeUVVersion(match[2]); !valid {
			return "", errors.New("invalid uv outdated current version")
		}
		if model.NormalizeIdentityName(match[1]) != target {
			continue
		}
		value, valid := normalizeUVVersion(match[3])
		if !valid || latest != "" {
			return "", errors.New("invalid or duplicate target version")
		}
		latest = value
	}
	return latest, nil
}

func normalizeUVVersion(value string) (string, bool) {
	if !uvVersion.MatchString(strings.TrimSpace(value)) {
		return "", false
	}
	return version.Normalize(value), true
}
