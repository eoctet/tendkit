package handler

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type RubyHandler struct {
	runner   Runner
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	homeDir  func() (string, error)
}

func NewRuby(r Runner) *RubyHandler {
	return &RubyHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, homeDir: os.UserHomeDir}
}

func (*RubyHandler) Domain() Domain { return Ruby }

func (h *RubyHandler) Scan(ctx context.Context, request Request) Result {
	ruby := h.manager(request.Configured)
	if ruby == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "ruby or RubyGems"}}
	}
	reportPackageProgress(request, model.ScanStagePackageList, "Ruby")
	listing, runErr := h.runner.Run(ctx, rubyGemListCommand(ruby), nil)
	if err := ctx.Err(); err != nil {
		return Result{Complete: false, Err: err}
	}
	if runErr != nil && strings.TrimSpace(listing.Stdout) == "" {
		return Result{Complete: false, Err: runErr}
	}
	var gems []rubyGem
	if err := json.Unmarshal([]byte(listing.Stdout), &gems); err != nil {
		return Result{Complete: false, Err: err}
	}

	result := Result{Complete: runErr == nil && listing.ExitCode == 0}
	for _, item := range gems {
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Err = err
			return result
		}
		reportPackageProgress(request, model.ScanStageApplication, item.Name)
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Err = err
			return result
		}
		url := githubProjectURL(item.Source)
		if url == "" {
			url = githubProjectURL(item.Homepage)
		}
		update, mode := rubyGemUpdateCommand(ruby, item.Name, item.InstallScope)
		versionCommand := rubyGemCommand(ruby, "list", "--local", "--exact", item.Name)
		app := model.Application{
			ID:          "pkg-ruby-" + packageSlug(item.Name),
			Name:        item.Name,
			Type:        model.ApplicationTypePackage,
			Description: strings.TrimSpace(item.Summary),
			URL:         url,
			InstallPath: item.InstallPath,
			Enabled:     true,
			UpdateMode:  mode,
			Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{
				Version: versionCommand,
				Check:   rubyGemCommand(ruby, "search", "--remote", "--exact", item.Name),
				Update:  update,
			}},
			Package:     item.Name,
			Identity:    model.PackageIdentity(string(h.Domain()), item.Name),
			ScanManaged: true,
		}
		candidate := packageCandidate(app, item.Version, "ruby:"+item.Name)
		invalidName := false
		seenNames := map[string]bool{}
		for index, name := range item.ExecutableNames {
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) || seenNames[name] || index >= len(item.Executables) || filepath.Clean(item.Executables[index]) != filepath.Clean(filepath.Join(item.Bindir, name)) {
				invalidName = true
			}
			seenNames[name] = true
		}
		hasClaims := len(item.ExecutableNames) > 0 || len(item.Executables) > 0
		if hasClaims && (invalidName || item.InstallScope == packageInstallScopeUnknown || !filepath.IsAbs(item.Bindir) || len(item.ExecutableNames) == 0 || len(item.Executables) == 0 || len(item.ExecutableNames) != len(item.Executables)) {
			result.Complete = false
			continue
		}
		if len(item.Executables) > 0 {
			paths, valid := executableEvidencePaths(item.Executables, h.stat)
			if !valid {
				result.Complete = false
				continue
			}
			candidate.Evidence = &InstallationEvidence{Source: string(h.Domain()), Package: item.Name, ExecutablePaths: paths, InstallRoot: item.InstallPath}
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	if !result.Complete && result.Err == nil {
		result.Err = &PackageInventoryIncompleteError{Ecosystem: "Ruby", Message: "incomplete RubyGems package inventory"}
	}
	return result
}

func rubyGemCommand(ruby string, args ...string) string {
	quotedArgs := make([]string, 0, len(args))
	for _, arg := range args {
		quotedArgs = append(quotedArgs, runtimeutil.QuoteShell(arg))
	}
	return runtimeutil.QuoteShell(ruby) + " -rrubygems/gem_runner -e " + runtimeutil.QuoteShell(`Gem::GemRunner.new.run(ARGV)`) + " -- " + strings.Join(quotedArgs, " ")
}

type rubyGem struct {
	Name            string              `json:"name"`
	Version         string              `json:"version"`
	Summary         string              `json:"summary"`
	Homepage        string              `json:"homepage"`
	Source          string              `json:"source"`
	InstallPath     string              `json:"install_path"`
	InstallScope    packageInstallScope `json:"install_scope"`
	Executables     []string            `json:"executables"`
	ExecutableNames []string            `json:"executable_names"`
	Bindir          string              `json:"bindir"`
}

func (h *RubyHandler) manager(configured []model.Application) string {
	return managerPath("ruby", configured, h.lookPath, h.stat, h.homeDir)
}

func rubyGemListCommand(ruby string) string {
	script := `canonical = ->(path) { begin File.realpath(path) rescue File.expand_path(path) end }; within = ->(path, root) { path = canonical.call(path); root = canonical.call(root); path == root || path.start_with?(root + File::SEPARATOR) }; user_dir = Gem.user_dir; gem_paths = Gem.path; latest = {}; Gem::Specification.each { |s| next if s.default_gem?; old = latest[s.name]; latest[s.name] = s if old.nil? || s.version > old.version || (s.version == old.version && s.full_gem_path < old.full_gem_path) }; puts JSON.generate(latest.values.sort_by { |s| [s.name.downcase, s.name, s.full_gem_path] }.map { |s| path = s.full_gem_path; scope = within.call(path, user_dir) ? "user" : (gem_paths.any? { |root| within.call(path, root) } ? "system" : "unknown"); bindir = scope == "user" ? Gem.bindir(user_dir) : Gem.bindir; names = s.executables; executables = names.map { |name| File.join(bindir, name) }; {name: s.name, version: s.version.to_s, summary: s.summary.to_s, homepage: s.homepage.to_s, source: s.metadata.fetch("source_code_uri", ""), install_path: path, install_scope: scope, bindir: bindir, executable_names: names, executables: executables} })`
	return runtimeutil.QuoteShell(ruby) + " -rjson -rrubygems -e " + runtimeutil.QuoteShell(script)
}

func rubyGemUpdateCommand(ruby, name string, scope packageInstallScope) (string, model.UpdateMode) {
	switch scope {
	case packageInstallScopeUser, packageInstallScopeSystem:
		args := []string{"install", "--no-document", name}
		if scope == packageInstallScopeUser {
			args = []string{"install", "--user-install", "--no-document", name}
		}
		return rubyGemCommand(ruby, args...), model.ModeAuto
	default:
		return "", model.ModeCheck
	}
}
