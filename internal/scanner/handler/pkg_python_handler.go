package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/script"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type PythonHandler struct {
	runner   Runner
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	homeDir  func() (string, error)
}

type packageMetadata struct {
	Description string
	URL         string
}

type packageInstallScope string

const (
	packageInstallScopeUnknown packageInstallScope = "unknown"
	packageInstallScopeUser    packageInstallScope = "user"
	packageInstallScopeSystem  packageInstallScope = "system"
)

type pythonPackageInstallInfo struct {
	Path        string              `json:"path"`
	Scope       packageInstallScope `json:"scope"`
	Executables []string            `json:"executables"`
	Complete    *bool               `json:"complete"`
}

func NewPython(r Runner) *PythonHandler {
	return &PythonHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, homeDir: os.UserHomeDir}
}
func (*PythonHandler) Domain() Domain { return Python }

func (h *PythonHandler) Scan(ctx context.Context, request Request) Result {
	python := h.findManager(request.Configured)
	if python == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "python3"}}
	}
	reportPackageProgress(request, model.ScanStagePackageList, "Python")
	listing, runErr := h.runner.Run(ctx, pythonPackageListCommand(python), nil)
	if err := ctx.Err(); err != nil {
		return Result{Complete: false, Err: err}
	}
	if runErr != nil && strings.TrimSpace(listing.Stdout) == "" {
		return Result{Complete: false, Err: runErr}
	}
	var packages []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(listing.Stdout), &packages); err != nil {
		return Result{Complete: false, Err: err}
	}
	names := make([]string, 0, len(packages))
	for _, item := range packages {
		if err := ctx.Err(); err != nil {
			return Result{Complete: false, Err: err}
		}
		names = append(names, item.Name)
	}
	metadata := h.metadata(ctx, python, names, request)
	installed := h.installInfo(ctx, python, names, request)
	result := Result{Complete: runErr == nil && listing.ExitCode == 0}
	for _, item := range packages {
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
		key := packageKey(item.Name)
		installation := installed[key]
		if strings.TrimSpace(installation.Path) == "" || installation.Complete == nil || !*installation.Complete {
			result.Complete = false
			continue
		}
		update, mode := pythonPackageUpdateCommand(python, item.Name, installation.Path, installation.Scope)
		info := metadata[key]
		app := model.Application{
			ID: "pkg-python-" + packageSlug(item.Name), Name: item.Name,
			Type: model.ApplicationTypePackage, Description: info.Description, URL: info.URL,
			InstallPath: installation.Path, Enabled: true, UpdateMode: mode,
			Provider: packageProvider(model.ProviderPyPI, pipVersionCommand(python, item.Name), update),
			Package:  item.Name, Identity: model.PackageIdentity(string(h.Domain()), item.Name), ScanManaged: true,
		}
		candidate := packageCandidate(app, item.Version, "python:"+item.Name)
		if len(installation.Executables) > 0 {
			paths, valid := executableEvidencePaths(installation.Executables, h.stat)
			if !valid {
				result.Complete = false
				continue
			}
			candidate.Evidence = &InstallationEvidence{Source: string(h.Domain()), Package: item.Name, ExecutablePaths: paths, InstallRoot: installation.Path}
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	if !result.Complete && result.Err == nil {
		result.Err = &PackageInventoryIncompleteError{Ecosystem: "Python", Message: "incomplete Python package inventory"}
	}
	return result
}

func (h *PythonHandler) findManager(configured []model.Application) string {
	if path, err := h.lookPath("python3"); err == nil {
		if canonical, err := CanonicalExecutablePath(path); err == nil {
			matches := make([]string, 0)
			for _, app := range configured {
				if !pythonManagerApplication(app) {
					continue
				}
				configuredPath := expandConfiguredPath(app.InstallPath, h.homeDir)
				visible, err := filepath.Abs(configuredPath)
				if err != nil {
					continue
				}
				if configuredCanonical, err := CanonicalExecutablePath(visible); err == nil && configuredCanonical == canonical {
					matches = append(matches, filepath.Clean(visible))
				}
			}
			if len(matches) > 0 {
				sort.Strings(matches)
				return matches[0]
			}
		}
		return path
	}
	for _, app := range configured {
		if !pythonManagerApplication(app) {
			continue
		}
		path := expandConfiguredPath(app.InstallPath, h.homeDir)
		if info, err := h.stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func pythonManagerApplication(app model.Application) bool {
	if app.Type != model.ApplicationTypeCLI {
		return false
	}
	id := strings.TrimSpace(app.ID)
	return id == PathApplicationID("python3") || isExtendedPathApplicationIDValue("python3", id)
}

func pipVersionCommand(python, name string) string {
	command, _ := metadatautil.PackageVersionCommand(metadatautil.PackageTarget{Ecosystem: metadatautil.PackagePython, Manager: python, Name: name})
	return command
}

func pythonPackageListCommand(python string) string {
	return runtimeutil.QuoteShell(python) + " -m pip list --not-required --format=json"
}

func pythonPackageUpdateCommand(python, name, installPath string, scope packageInstallScope) (string, model.UpdateMode) {
	switch scope {
	case packageInstallScopeUser, packageInstallScopeSystem:
		command, err := metadatautil.PackageUpdateCommand(metadatautil.PackageTarget{Ecosystem: metadatautil.PackagePython, Manager: python, Name: name, InstallPath: installPath, UserInstall: scope == packageInstallScopeUser})
		if err == nil {
			return command, model.ModeAuto
		}
		return "", model.ModeCheck
	default:
		return "", model.ModeCheck
	}
}

func (h *PythonHandler) metadata(ctx context.Context, python string, names []string, request Request) map[string]packageMetadata {
	metadata := make(map[string]packageMetadata)
	const chunkSize = 50
	for start := 0; start < len(names); start += chunkSize {
		if ctx.Err() != nil {
			return metadata
		}
		end := min(len(names), start+chunkSize)
		reportPackageProgress(request, model.ScanStagePackageMetadata, fmt.Sprintf("Python %d/%d", end, len(names)))
		command := runtimeutil.QuoteShell(python) + " -m pip show"
		for _, name := range names[start:end] {
			command += " " + runtimeutil.QuoteShell(name)
		}
		result, err := h.runner.Run(ctx, command, nil)
		if err != nil && strings.TrimSpace(result.Stdout) == "" {
			continue
		}
		for name, value := range parsePipShow(result.Stdout) {
			metadata[name] = value
		}
	}
	return metadata
}

func parsePipShow(output string) map[string]packageMetadata {
	result := make(map[string]packageMetadata)
	fields := map[string][]string{}
	flush := func() {
		name := packageKey(firstField(fields, "Name"))
		if name == "" {
			fields = map[string][]string{}
			return
		}
		url := githubProjectURL(firstField(fields, "Home-page"))
		for _, projectURL := range fields["Project-URL"] {
			if _, candidate, ok := strings.Cut(projectURL, ","); ok {
				projectURL = candidate
			}
			if candidate := githubProjectURL(strings.TrimSpace(projectURL)); candidate != "" {
				url = candidate
				break
			}
		}
		result[name] = packageMetadata{Description: firstField(fields, "Summary"), URL: url}
		fields = map[string][]string{}
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "---" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			key = strings.TrimSpace(key)
			fields[key] = append(fields[key], strings.TrimSpace(value))
		}
	}
	flush()
	return result
}

func firstField(fields map[string][]string, key string) string {
	if len(fields[key]) == 0 {
		return ""
	}
	return strings.TrimSpace(fields[key][0])
}

func (h *PythonHandler) installInfo(ctx context.Context, python string, names []string, request Request) map[string]pythonPackageInstallInfo {
	installed := make(map[string]pythonPackageInstallInfo)
	const chunkSize = 50
	for start := 0; start < len(names); start += chunkSize {
		if ctx.Err() != nil {
			return installed
		}
		end := min(len(names), start+chunkSize)
		reportPackageProgress(request, model.ScanStagePackagePaths, fmt.Sprintf("Python %d/%d", end, len(names)))
		command := runtimeutil.QuoteShell(python) + " -c " + runtimeutil.QuoteShell(script.PythonPackageInfo)
		for _, name := range names[start:end] {
			command += " " + runtimeutil.QuoteShell(name)
		}
		result, err := h.runner.Run(ctx, command, nil)
		if err != nil && strings.TrimSpace(result.Stdout) == "" {
			continue
		}
		var chunk map[string]pythonPackageInstallInfo
		decoder := json.NewDecoder(bytes.NewBufferString(result.Stdout))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(&chunk) != nil || decoder.Decode(&extra) != io.EOF {
			continue
		}
		for name, info := range chunk {
			info.Path = strings.TrimSpace(info.Path)
			if info.Path == "" {
				continue
			}
			if info.Scope != packageInstallScopeUser && info.Scope != packageInstallScopeSystem {
				info.Scope = packageInstallScopeUnknown
			}
			for index := range info.Executables {
				info.Executables[index] = strings.TrimSpace(info.Executables[index])
			}
			installed[packageKey(name)] = info
		}
	}
	return installed
}
