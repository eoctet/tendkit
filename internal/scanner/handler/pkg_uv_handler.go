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
	if directory, err := h.runner.Run(ctx, runtimeutil.QuoteShell(uv)+" tool dir", nil); err == nil && directory.ExitCode == 0 {
		toolDirectory = strings.TrimSpace(directory.Stdout)
	}
	if err := ctx.Err(); err != nil {
		return Result{Complete: false, Err: err}
	}

	result := Result{Complete: runErr == nil && listing.ExitCode == 0}
	lines := strings.Split(listing.Stdout, "\n")
	matched := 0
	for index, line := range lines {
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Err = err
			return result
		}
		match := uvAppLine.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) == 0 {
			continue
		}
		name, current := match[1], match[2]
		matched++
		reportPackageProgress(request, model.ScanStageApplication, name)
		if err := ctx.Err(); err != nil {
			result.Complete = false
			result.Err = err
			return result
		}
		installPath := uv
		if index+1 < len(lines) {
			if path := uvPathLine.FindStringSubmatch(lines[index+1]); len(path) > 1 {
				installPath = path[1]
			}
		}
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
			Identity:    model.PackageIdentity("uv", name),
			ScanManaged: true,
		}
		result.Candidates = append(result.Candidates, packageCandidate(app, current, "uv:"+name))
	}
	if strings.TrimSpace(listing.Stdout) != "" && matched == 0 {
		result.Complete = false
	}
	if !result.Complete && result.Err == nil {
		result.Err = &PackageInventoryIncompleteError{Ecosystem: "uv", Message: "incomplete or invalid uv tool inventory"}
	}
	return result
}

func (h *UVHandler) manager(configured []model.Application) string {
	if path, err := h.lookPath("uv"); err == nil {
		return path
	}
	for _, app := range configured {
		if !strings.EqualFold(app.ID, "uv") && !strings.EqualFold(app.Name, "uv") {
			continue
		}
		path := expandConfiguredPath(app.InstallPath, h.homeDir)
		if info, err := h.stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
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
