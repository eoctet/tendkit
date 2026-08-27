package scanner

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	"github.com/eoctet/tendkit/internal/scanner/handler"
)

// exclusionMatcher applies configured glob-like rules to every stable spelling
// by which a discovery may be identified.
type exclusionMatcher struct{ patterns []*regexp.Regexp }

func newExclusionMatcher(values []string) exclusionMatcher {
	matcher := exclusionMatcher{}
	for _, value := range values {
		expression := regexp.QuoteMeta(strings.ToLower(strings.TrimSpace(value)))
		expression = strings.ReplaceAll(expression, `\*`, `.*`)
		expression = strings.ReplaceAll(expression, `\?`, `.`)
		if compiled, err := regexp.Compile("^" + expression + "$"); err == nil {
			matcher.patterns = append(matcher.patterns, compiled)
		}
	}
	return matcher
}

func (m exclusionMatcher) excluded(app model.Application, aliases ...string) bool {
	identity := inferIdentity(app)
	candidates := []string{app.ID, app.Name, app.InstallPath, app.Identity, identity, app.Package}
	identity = strings.ToLower(identity)
	if strings.HasPrefix(identity, "app:") {
		candidates = append(candidates, "bundle:"+strings.TrimPrefix(identity, "app:"))
	}
	if strings.HasPrefix(identity, "package:") {
		parts := strings.SplitN(identity, ":", 3)
		if len(parts) == 3 {
			candidates = append(candidates, parts[1]+":"+parts[2])
		}
	}
	candidates = append(candidates, aliases...)
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		for _, pattern := range m.patterns {
			if candidate != "" && pattern.MatchString(candidate) {
				return true
			}
		}
	}
	return false
}

// ExcludedConfiguredApps returns existing catalog entries matched by the
// configured exclusion rules. The caller decides whether removal is approved.
func ExcludedConfiguredApps(catalog model.Config) []model.Application {
	matcher := newExclusionMatcher(catalog.Settings.Scan.Exclude)
	matched := make([]model.Application, 0)
	for _, app := range catalog.Apps {
		if matcher.excluded(app) {
			matched = append(matched, app)
		}
	}
	return matched
}

// canonicalPath produces a comparable identity path while tolerating a missing
// target; strict ownership evidence uses canonicalEvidencePath instead.
func canonicalPath(value string) string {
	value = installedPathValue(value)
	if absolute, err := filepath.Abs(value); err == nil {
		value = absolute
	}
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = resolved
	}
	return canonicalComparablePath(value)
}

func normalizePackage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}

// builtInPathDefinitionIndex links configured package metadata back to the
// canonical built-in CLI definition without fuzzy name matching.
type builtInPathDefinitionIndex struct {
	byID      map[string]builtin.PathDefinition
	byPackage map[string]builtin.PathDefinition
}

var canonicalBuiltInPathDefinitions = func() builtInPathDefinitionIndex {
	index := builtInPathDefinitionIndex{byID: make(map[string]builtin.PathDefinition), byPackage: make(map[string]builtin.PathDefinition)}
	for _, item := range builtin.PathDefinitions() {
		index.byID[strings.ToLower(item.ID)] = item
		if key := builtInPackageKey(item.Provider, item.Package); key != "" {
			index.byPackage[key] = item
		}
	}
	return index
}()

func builtInPackageKey(provider model.ProviderType, packageName string) string {
	packageName = normalizePackage(packageName)
	if provider == "" || packageName == "" {
		return ""
	}
	return strings.ToLower(string(provider)) + ":" + packageName
}

// inferIdentity uses only stable provider, package, bundle, and CLI facts; it
// never guesses across package ecosystems.
func inferIdentity(app model.Application) string {
	if app.Identity != "" {
		return strings.ToLower(app.Identity)
	}
	if app.Type == model.ApplicationTypeBundle {
		return "app-path:" + canonicalPath(app.InstallPath)
	}
	if app.Package != "" {
		switch app.Provider.Type {
		case model.ProviderPyPI:
			return model.PackageIdentity("python", app.Package)
		case model.ProviderNPM:
			return model.PackageIdentity("node", app.Package)
		case model.ProviderUV:
			return model.PackageIdentity("uv", app.Package)
		case model.ProviderGo:
			if strings.Contains(app.Package, ".") || strings.Contains(app.Package, "/") {
				return model.PackageIdentity("go", app.Package)
			}
		case model.ProviderHomebrew:
			ecosystem, name := "homebrew-formula", strings.TrimPrefix(app.Package, "formula/")
			if strings.HasPrefix(app.Package, "cask/") {
				ecosystem, name = "homebrew-cask", strings.TrimPrefix(app.Package, "cask/")
			}
			return model.PackageIdentity(ecosystem, name)
		case model.ProviderCargo:
			return model.PackageIdentity("cargo", app.Package)
		}
	}
	return "cli:" + model.NormalizeIdentityName(app.Name)
}

func matchingBuiltInPathDefinition(app model.Application) (builtin.PathDefinition, bool) {
	if app.Type != model.ApplicationTypeCLI && app.Type != model.ApplicationTypePackage {
		return builtin.PathDefinition{}, false
	}
	if app.Type == model.ApplicationTypeCLI {
		if item, ok := canonicalBuiltInPathDefinitions.byID[strings.ToLower(app.ID)]; ok {
			return item, true
		}
	}
	if item, ok := canonicalBuiltInPathDefinitions.byPackage[builtInPackageKey(app.Provider.Type, app.Package)]; ok {
		return item, true
	}
	return builtin.PathDefinition{}, false
}

func deduplicationKey(app model.Application, activeBuiltInCLIs map[string]bool) string {
	if item, ok := matchingBuiltInPathDefinition(app); ok && activeBuiltInCLIs[item.ID] {
		return "builtin-path:" + item.ID
	}
	key := inferIdentity(app)
	if app.InstallPath != "" && !strings.HasPrefix(key, "package:") {
		return "path:" + canonicalPath(app.InstallPath)
	}
	return key
}

// GenerateIdentity returns the stable identity produced by this scanner's
// discovery rules, falling back to the inferred stable identity when unmatched.
func (s Scanner) GenerateIdentity(ctx context.Context, app model.Application) (string, error) {
	app.Identity = ""
	if app.Type == model.ApplicationTypeBundle {
		candidate, matched, err := handler.NewMacApp(builtin.MacAppDefinitions(), s.bundleIDs).ScanApplication(ctx, app, handler.Request{})
		if err != nil {
			return "", err
		}
		if matched {
			return candidate.Application.Identity, nil
		}
	}
	return inferIdentity(app), nil
}
