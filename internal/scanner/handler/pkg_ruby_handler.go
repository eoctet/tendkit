package handler

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
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
	ruby := h.manager("ruby", request.Configured, "ruby", "Ruby")
	gem := h.manager("gem", request.Configured, "gem", "RubyGems")
	if ruby == "" || gem == "" {
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
		update, mode := rubyGemUpdateCommand(gem, item.Name, item.InstallScope)
		versionCommand, _ := metadatautil.PackageVersionCommand(metadatautil.PackageTarget{Ecosystem: metadatautil.PackageRuby, Manager: gem, Name: item.Name, InstallPath: item.InstallPath})
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
				Check:   runtimeutil.QuoteShell(gem) + " search --remote --exact " + runtimeutil.QuoteShell(item.Name),
				Update:  update,
			}},
			Package:     item.Name,
			Identity:    model.PackageIdentity("ruby", item.Name),
			ScanManaged: true,
		}
		result.Candidates = append(result.Candidates, packageCandidate(app, item.Version, "ruby:"+item.Name))
	}
	if !result.Complete && result.Err == nil {
		result.Err = &PackageInventoryIncompleteError{Ecosystem: "Ruby", Message: "incomplete RubyGems package inventory"}
	}
	return result
}

type rubyGem struct {
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Summary      string              `json:"summary"`
	Homepage     string              `json:"homepage"`
	Source       string              `json:"source"`
	InstallPath  string              `json:"install_path"`
	InstallScope packageInstallScope `json:"install_scope"`
}

func (h *RubyHandler) manager(binary string, configured []model.Application, names ...string) string {
	if path, err := h.lookPath(binary); err == nil {
		return path
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[strings.ToLower(name)] = true
	}
	for _, app := range configured {
		if !wanted[strings.ToLower(app.ID)] && !wanted[strings.ToLower(app.Name)] {
			continue
		}
		path := expandConfiguredPath(app.InstallPath, h.homeDir)
		if info, err := h.stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func rubyGemListCommand(ruby string) string {
	script := `canonical = ->(path) { begin File.realpath(path) rescue File.expand_path(path) end }; within = ->(path, root) { path = canonical.call(path); root = canonical.call(root); path == root || path.start_with?(root + File::SEPARATOR) }; user_dir = Gem.user_dir; gem_paths = Gem.path; latest = {}; Gem::Specification.each { |s| next if s.default_gem?; old = latest[s.name]; latest[s.name] = s if old.nil? || s.version > old.version }; puts JSON.generate(latest.values.map { |s| path = s.full_gem_path; scope = within.call(path, user_dir) ? "user" : (gem_paths.any? { |root| within.call(path, root) } ? "system" : "unknown"); {name: s.name, version: s.version.to_s, summary: s.summary.to_s, homepage: s.homepage.to_s, source: s.metadata.fetch("source_code_uri", ""), install_path: path, install_scope: scope} })`
	return runtimeutil.QuoteShell(ruby) + " -rjson -rrubygems -e " + runtimeutil.QuoteShell(script)
}

func rubyGemUpdateCommand(gem, name string, scope packageInstallScope) (string, model.UpdateMode) {
	switch scope {
	case packageInstallScopeUser, packageInstallScopeSystem:
		command, err := metadatautil.PackageUpdateCommand(metadatautil.PackageTarget{Ecosystem: metadatautil.PackageRuby, Manager: gem, Name: name, UserInstall: scope == packageInstallScopeUser})
		if err == nil {
			return command, model.ModeAuto
		}
		return "", model.ModeCheck
	default:
		return "", model.ModeCheck
	}
}
