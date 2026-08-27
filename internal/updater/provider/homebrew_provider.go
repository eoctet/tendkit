package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

// HomebrewProvider delegates all package-manager semantics to the locally selected brew.
// It deliberately accepts only a uniquely identified installed target.
type HomebrewProvider struct {
	Runner commandRunner
	host   func() runtimeutil.SystemInfo
	lookup func(string, map[string]string) (string, error)
}

func managerPath(name string, environment map[string]string, lookup func(string, map[string]string) (string, error)) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", errors.New("invalid manager executable name")
	}
	if lookup != nil {
		return lookup(name, environment)
	}
	path := os.Getenv("PATH")
	if environment != nil && environment["PATH"] != "" {
		path = environment["PATH"]
	}
	for _, directory := range filepath.SplitList(path) {
		candidate := filepath.Join(directory, name)
		// #nosec G703 -- directory is intentionally sourced from PATH; name is restricted to a single basename above.
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return filepath.Abs(candidate)
		}
	}
	return exec.LookPath(name)
}

func (p HomebrewProvider) caskSupported() bool {
	if p.host != nil {
		return p.host().Kernel == "darwin"
	}
	return runtimeutil.HostPlatform().Kernel == "darwin"
}

func parseHomebrewPackage(value string) (kind, name string, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", errors.New("package is empty")
	}
	kind, name = "formula", value
	if strings.HasPrefix(value, "formula/") {
		name = strings.TrimPrefix(value, "formula/")
	} else if strings.HasPrefix(value, "cask/") {
		kind, name = "cask", strings.TrimPrefix(value, "cask/")
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "//") {
		return "", "", errors.New("invalid Homebrew package")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || filepath.Base(part) != part || strings.Contains(part, `\`) {
			return "", "", errors.New("invalid Homebrew package")
		}
	}
	return kind, name, nil
}

func (p HomebrewProvider) Current(ctx context.Context, request Request) (string, error) {
	kind, _, err := parseHomebrewPackage(request.App.Package)
	if err != nil {
		return "", p.error(request, CapabilityCurrent, "provider.homebrew_invalid_package", err)
	}
	if kind == "cask" && !p.caskSupported() {
		return "", CapabilityUnavailable(string(model.ProviderHomebrew), CapabilityCurrent)
	}
	manager, err := managerPath("brew", request.App.Environment, p.lookup)
	if err != nil {
		return "", p.error(request, CapabilityCurrent, "provider.homebrew_current_failed", err)
	}
	target, err := p.target(ctx, request, manager, CapabilityCurrent)
	if err != nil {
		return "", err
	}
	return target.current, nil
}
func (p HomebrewProvider) Latest(ctx context.Context, request Request) (string, error) {
	kind, name, err := parseHomebrewPackage(request.App.Package)
	if err != nil {
		return "", p.error(request, CapabilityLatest, "provider.homebrew_invalid_package", err)
	}
	if kind == "cask" && !p.caskSupported() {
		return "", CapabilityUnavailable(string(model.ProviderHomebrew), CapabilityLatest)
	}
	manager, err := managerPath("brew", request.App.Environment, p.lookup)
	if err != nil {
		return "", p.error(request, CapabilityLatest, "provider.homebrew_latest_failed", err)
	}
	base, err := p.target(ctx, request, manager, CapabilityLatest)
	if err != nil {
		return "", err
	}
	result, runErr := p.Runner.Run(ctx, runtimeutil.QuoteShell(manager)+" outdated --json=v2 --"+kind+" "+runtimeutil.QuoteShell(name), request.App.Environment)
	if runErr != nil {
		return "", p.error(request, CapabilityLatest, "provider.homebrew_latest_failed", runErr)
	}
	latest, found, parseErr := parseHomebrewLatest(result.Stdout, kind, base.name)
	if parseErr != nil {
		return "", p.error(request, CapabilityLatest, "provider.homebrew_parse_failed", parseErr)
	}
	if result.ExitCode != 0 && !found {
		return "", p.error(request, CapabilityLatest, "provider.homebrew_latest_exit", fmt.Errorf("exit %d", result.ExitCode), result.ExitCode)
	}
	if !found {
		return base.current, nil
	}
	if result.ExitCode != 0 && latest == "" {
		return "", p.error(request, CapabilityLatest, "provider.homebrew_latest_exit", fmt.Errorf("exit %d", result.ExitCode), result.ExitCode)
	}
	if latest == "" {
		return "", p.error(request, CapabilityLatest, "provider.homebrew_parse_failed", errors.New("latest version is empty"))
	}
	return latest, nil
}
func (p HomebrewProvider) Update(ctx context.Context, request Request) error {
	kind, name, err := parseHomebrewPackage(request.App.Package)
	if err != nil {
		return p.error(request, CapabilityUpdate, "provider.homebrew_invalid_package", err)
	}
	if kind == "cask" && !p.caskSupported() {
		return CapabilityUnavailable(string(model.ProviderHomebrew), CapabilityUpdate)
	}
	manager, err := managerPath("brew", request.App.Environment, p.lookup)
	if err != nil {
		return p.error(request, CapabilityUpdate, "provider.homebrew_update_failed", err)
	}
	if _, err := p.target(ctx, request, manager, CapabilityUpdate); err != nil {
		return err
	}
	result, err := p.Runner.Run(ctx, runtimeutil.QuoteShell(manager)+" upgrade --"+kind+" "+runtimeutil.QuoteShell(name), request.App.Environment)
	if err != nil {
		return p.error(request, CapabilityUpdate, "provider.homebrew_update_failed", err)
	}
	if result.ExitCode != 0 {
		output := strings.TrimSpace(result.Combined())
		return p.error(request, CapabilityUpdate, "provider.homebrew_update_exit", fmt.Errorf("exit %d: %s", result.ExitCode, output), result.ExitCode, output)
	}
	return nil
}

type homebrewTarget struct{ name, current, prefix, installed string }

func (p HomebrewProvider) target(ctx context.Context, request Request, manager string, capability Capability) (homebrewTarget, error) {
	kind, name, err := parseHomebrewPackage(request.App.Package)
	if err != nil {
		return homebrewTarget{}, p.error(request, capability, "provider.homebrew_invalid_package", err)
	}
	if kind == "cask" && !p.caskSupported() {
		return homebrewTarget{}, CapabilityUnavailable(string(model.ProviderHomebrew), capability)
	}
	result, err := p.Runner.Run(ctx, runtimeutil.QuoteShell(manager)+" list --"+kind+" --versions --json", request.App.Environment)
	if err != nil {
		return homebrewTarget{}, p.error(request, capability, "provider.homebrew_current_failed", err)
	}
	if result.ExitCode != 0 {
		return homebrewTarget{}, p.error(request, capability, "provider.homebrew_current_exit", fmt.Errorf("exit %d", result.ExitCode), result.ExitCode)
	}
	if kind == "cask" {
		target, token, err := parseHomebrewFastCask(result.Stdout, name)
		if err != nil {
			return homebrewTarget{}, p.error(request, capability, "provider.homebrew_parse_failed", err)
		}
		rootResult, rootErr := p.Runner.Run(ctx, runtimeutil.QuoteShell(manager)+" --caskroom", request.App.Environment)
		if rootErr != nil {
			return homebrewTarget{}, p.error(request, capability, "provider.homebrew_current_failed", rootErr)
		}
		if rootResult.ExitCode != 0 {
			return homebrewTarget{}, p.error(request, capability, "provider.homebrew_current_exit", fmt.Errorf("exit %d", rootResult.ExitCode), rootResult.ExitCode)
		}
		root, err := parseHomebrewPrefix(rootResult.Stdout)
		if err != nil {
			return homebrewTarget{}, p.error(request, capability, "provider.homebrew_parse_failed", err)
		}
		target.prefix = filepath.Join(root, token, target.installed)
		if err := verifyCaskOwnershipPath(request.App.InstallPath, target.prefix); err != nil {
			return homebrewTarget{}, p.error(request, capability, "provider.target_conflict", err)
		}
		return target, nil
	}
	target, rack, err := parseHomebrewFastFormula(result.Stdout, name)
	if err != nil {
		return homebrewTarget{}, p.error(request, capability, "provider.homebrew_parse_failed", err)
	}
	prefixResult, prefixErr := p.Runner.Run(ctx, runtimeutil.QuoteShell(manager)+" --cellar", request.App.Environment)
	if prefixErr != nil {
		return homebrewTarget{}, p.error(request, capability, "provider.homebrew_current_failed", prefixErr)
	}
	if prefixResult.ExitCode != 0 {
		return homebrewTarget{}, p.error(request, capability, "provider.homebrew_current_exit", fmt.Errorf("exit %d", prefixResult.ExitCode), prefixResult.ExitCode)
	}
	prefix, prefixParseErr := parseHomebrewPrefix(prefixResult.Stdout)
	if prefixParseErr != nil {
		return homebrewTarget{}, p.error(request, capability, "provider.homebrew_parse_failed", prefixParseErr)
	}
	target.prefix = filepath.Join(prefix, rack, target.installed)
	if err := verifyInstallOwnership(request.App.InstallPath, target.prefix); err != nil {
		return homebrewTarget{}, p.error(request, capability, "provider.target_conflict", err)
	}
	return target, nil
}
func (p HomebrewProvider) error(request Request, capability Capability, key string, cause error, details ...any) error {
	args := append([]any{request.App.Name}, details...)
	return &Error{Key: key, Args: args, Provider: string(model.ProviderHomebrew), Capability: capability, Cause: cause}
}

type brewInfo struct {
	Formulae []brewItem `json:"formulae"`
}

type brewFastInventory struct {
	Formulae []brewFastFormula `json:"formulae"`
	Casks    []brewFastCask    `json:"casks"`
}

type brewFastFormula struct {
	Name             string   `json:"name"`
	Versions         []string `json:"versions"`
	LinkedVersion    string   `json:"linked_version"`
	OptlinkedVersion string   `json:"optlinked_version"`
}

type brewFastCask struct {
	Token    string   `json:"token"`
	Versions []string `json:"versions"`
}

func requestedHomebrewToken(name string) string {
	parts := strings.Split(name, "/")
	return parts[len(parts)-1]
}

func parseHomebrewFastFormula(raw, requested string) (homebrewTarget, string, error) {
	var inventory brewFastInventory
	if err := json.Unmarshal([]byte(raw), &inventory); err != nil {
		return homebrewTarget{}, "", err
	}
	token := requestedHomebrewToken(requested)
	var matched *brewFastFormula
	for i := range inventory.Formulae {
		if inventory.Formulae[i].Name == token {
			if matched != nil {
				return homebrewTarget{}, "", errors.New("installed target is not unique")
			}
			matched = &inventory.Formulae[i]
		}
	}
	if matched == nil {
		return homebrewTarget{}, "", errors.New("installed target was not found")
	}
	current := matched.LinkedVersion
	if current == "" {
		current = matched.OptlinkedVersion
	}
	if current == "" && len(matched.Versions) == 1 {
		current = matched.Versions[0]
	}
	if current == "" || !slicesContains(matched.Versions, current) {
		return homebrewTarget{}, "", errors.New("installed version is not unique")
	}
	if _, err := version.Extract(current); err != nil {
		return homebrewTarget{}, "", err
	}
	return homebrewTarget{name: requested, current: version.Normalize(current), installed: current}, matched.Name, nil
}

func parseHomebrewFastCask(raw, requested string) (homebrewTarget, string, error) {
	var inventory brewFastInventory
	if err := json.Unmarshal([]byte(raw), &inventory); err != nil {
		return homebrewTarget{}, "", err
	}
	token := requestedHomebrewToken(requested)
	var matched *brewFastCask
	for i := range inventory.Casks {
		if inventory.Casks[i].Token == token {
			if matched != nil {
				return homebrewTarget{}, "", errors.New("installed cask is not unique")
			}
			matched = &inventory.Casks[i]
		}
	}
	if matched == nil || len(matched.Versions) != 1 {
		return homebrewTarget{}, "", errors.New("installed cask version is not unique")
	}
	current := matched.Versions[0]
	if _, err := version.Extract(current); err != nil {
		return homebrewTarget{}, "", err
	}
	return homebrewTarget{name: requested, current: version.Normalize(current), installed: current}, matched.Token, nil
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type brewItem struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Versions struct {
		Stable string `json:"stable"`
	} `json:"versions"`
	CurrentVersion string `json:"current_version"`
}

func parseHomebrewPrefix(raw string) (string, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return "", errors.New("installed prefix is not unique")
	}
	return strings.TrimSpace(lines[0]), nil
}
func verifyCaskOwnershipPath(installPath, prefix string) error {
	installPath, err := filepath.EvalSymlinks(installPath)
	if err != nil {
		return err
	}
	found := false
	err = filepath.WalkDir(prefix, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == prefix || !strings.HasSuffix(strings.ToLower(entry.Name()), ".app") {
			return nil
		}
		candidate, err := filepath.EvalSymlinks(path)
		if err == nil && candidate == installPath {
			found = true
		}
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return errors.New("application is not owned by cask")
	}
	return nil
}
func parseHomebrewLatest(raw, kind, target string) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return "", false, nil
	}
	if kind == "cask" {
		var value brewCaskOutdatedInfo
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return "", false, err
		}
		return parseHomebrewCaskOutdated(value.Casks, target)
	}
	var value brewInfo
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", false, err
	}
	items := value.Formulae
	if len(items) == 0 {
		return "", false, nil
	}
	if len(items) != 1 {
		return "", false, errors.New("outdated target is not unique")
	}
	item := items[0]
	name := item.FullName
	if name == "" {
		name = item.Name
	}
	if name != target && item.Name != target {
		return "", false, errors.New("outdated target mismatch")
	}
	latest := item.Versions.Stable
	if latest == "" {
		latest = item.CurrentVersion
	}
	if _, err := version.Extract(latest); err != nil {
		return "", false, err
	}
	return version.Normalize(latest), true, nil
}

type brewCaskOutdatedInfo struct {
	Casks []brewCaskOutdated `json:"casks"`
}

type brewCaskOutdated struct {
	Name              string   `json:"name"`
	InstalledVersions []string `json:"installed_versions"`
	CurrentVersion    string   `json:"current_version"`
}

func parseHomebrewCaskOutdated(items []brewCaskOutdated, target string) (string, bool, error) {
	if len(items) == 0 {
		return "", false, nil
	}
	if len(items) != 1 {
		return "", false, errors.New("outdated cask is not unique")
	}
	item := items[0]
	if item.Name != target && !strings.HasSuffix(target, "/"+item.Name) {
		return "", false, errors.New("outdated cask mismatch")
	}
	if len(item.InstalledVersions) != 1 || strings.TrimSpace(item.InstalledVersions[0]) == "" {
		return "", false, errors.New("outdated installed cask version is not unique")
	}
	if _, err := version.Extract(item.CurrentVersion); err != nil {
		return "", false, err
	}
	return version.Normalize(item.CurrentVersion), true, nil
}
