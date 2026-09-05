package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

var uvAppLine = regexp.MustCompile(`^([^\s]+)\s+v?(\d+(?:\.\d+)+(?:[-+._][0-9A-Za-z.-]+)?)(?:\s+\(.+\))?\s*$`)
var uvPathLine = regexp.MustCompile(`^\s*-\s+[^\s]+\s+\((.+)\)\s*$`)

// UVHandler discovers applications installed with uv tool.
type UVHandler struct {
	runner   Runner
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	homeDir  func() (string, error)
}

func NewUV(r Runner) *UVHandler {
	return &UVHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, homeDir: os.UserHomeDir}
}

func (*UVHandler) Domain() Domain { return UV }

func (h *UVHandler) Scan(ctx context.Context, request Request) Result {
	uv := h.manager(request.Configured)
	if uv == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "uv"}}
	}
	reportPackageProgress(request, model.ScanStagePackageList, "uv")
	listing, runErr := h.runner.Run(ctx, runtimeutil.QuoteShell(uv)+" tool list --show-paths", nil)
	if err := ctx.Err(); err != nil {
		return Result{Complete: false, Err: err}
	}
	if runErr != nil && strings.TrimSpace(listing.Stdout) == "" {
		return Result{Complete: false, Err: runErr}
	}

	toolDirectory := ""
	toolDirOK := false
	if directory, err := h.runner.Run(ctx, runtimeutil.QuoteShell(uv)+" tool dir", nil); err == nil && directory.ExitCode == 0 {
		toolDirectory = strings.TrimSpace(directory.Stdout)
		if filepath.IsAbs(toolDirectory) {
			if info, statErr := h.stat(toolDirectory); statErr == nil && info.IsDir() {
				toolDirOK = true
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{Complete: false, Err: err}
	}

	result := Result{Complete: runErr == nil && listing.ExitCode == 0}
	tools, parseOK := parseUVTools(listing.Stdout)
	if len(tools) > 0 && !toolDirOK {
		result.Complete = false
	}
	matched := len(tools)
	if !parseOK {
		result.Complete = false
	}
	for _, tool := range tools {
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Err = err
			return result
		}
		name, current := tool.name, tool.version
		reportPackageProgress(request, model.ScanStageApplication, name)
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Err = err
			return result
		}
		paths, valid := executableEvidencePaths(tool.paths, h.stat)
		if !valid {
			result.Complete = false
			continue
		}
		installPath := paths[0]
		metadata := h.metadata(ctx, toolDirectory, name, request)
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Err = err
			return result
		}
		app := model.Application{
			ID:          "pkg-uv-" + packageSlug(name),
			Name:        name,
			Type:        model.ApplicationTypePackage,
			Description: metadata.Description,
			URL:         metadata.URL,
			InstallPath: installPath,
			Enabled:     true,
			UpdateMode:  model.ModeAuto,
			Provider:    model.ProviderConfig{Type: model.ProviderUV},
			Package:     name,
			Identity:    model.PackageIdentity(string(h.Domain()), name),
			ScanManaged: true,
		}
		candidate := packageCandidate(app, current, "uv:"+name)
		candidate.Evidence = &InstallationEvidence{Source: string(h.Domain()), Package: name, ExecutablePaths: paths, InstallRoot: toolDirectory}
		result.Candidates = append(result.Candidates, candidate)
	}
	if strings.TrimSpace(listing.Stdout) != "" && matched == 0 {
		result.Complete = false
	}
	if !result.Complete && result.Err == nil {
		result.Err = &PackageInventoryIncompleteError{Ecosystem: "uv", Message: "incomplete or invalid uv tool inventory"}
	}
	return result
}

type uvTool struct {
	name, version string
	paths         []string
}

// parseUVTools accepts only complete records emitted by `uv tool list --show-paths`.
func parseUVTools(output string) ([]uvTool, bool) {
	if strings.TrimSpace(output) == "" {
		return nil, true
	}
	var tools []uvTool
	var current *uvTool
	names := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if match := uvAppLine.FindStringSubmatch(strings.TrimSpace(line)); len(match) > 0 {
			if current != nil && len(current.paths) == 0 {
				return nil, false
			}
			if names[match[1]] {
				return nil, false
			}
			names[match[1]] = true
			tools = append(tools, uvTool{name: match[1], version: match[2]})
			current = &tools[len(tools)-1]
			continue
		}
		if match := uvPathLine.FindStringSubmatch(line); len(match) > 1 && current != nil {
			for _, value := range current.paths {
				if value == strings.TrimSpace(match[1]) {
					return nil, false
				}
			}
			current.paths = append(current.paths, strings.TrimSpace(match[1]))
			continue
		}
		return nil, false
	}
	return tools, current != nil && len(current.paths) > 0
}

func (h *UVHandler) manager(configured []model.Application) string {
	return managerPath("uv", configured, h.lookPath, h.stat, h.homeDir)
}

func (h *UVHandler) metadata(ctx context.Context, toolDirectory, name string, request Request) packageMetadata {
	if toolDirectory == "" {
		return packageMetadata{}
	}
	for _, executable := range []string{"python3", "python"} {
		python := filepath.Join(toolDirectory, name, "bin", executable)
		if info, err := h.stat(python); err == nil && !info.IsDir() {
			return NewPython(h.runner).metadata(ctx, python, []string{name}, request)[packageKey(name)]
		}
	}
	return packageMetadata{}
}
