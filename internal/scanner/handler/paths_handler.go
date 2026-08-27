package handler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

type lookPathsFunc func(string) ([]string, error)

// PathInstanceFingerprintLength is the stable hexadecimal suffix length used by Scanner.
const PathInstanceFingerprintLength = 16

type PathHandler struct {
	runner      Runner
	definitions []builtin.PathDefinition
	lookPaths   lookPathsFunc
}

func NewPath(r Runner, definitions []builtin.PathDefinition) *PathHandler {
	return &PathHandler{runner: r, definitions: append([]builtin.PathDefinition(nil), definitions...), lookPaths: defaultLookPaths}
}
func (h *PathHandler) Domain() Domain { return Path }
func (h *PathHandler) Scan(ctx context.Context, request Request) Result {
	result := Result{Complete: true}
	for _, d := range h.definitions {
		if ctx.Err() != nil {
			return Result{Candidates: result.Candidates, Complete: false, Err: ctx.Err()}
		}
		if request.Report != nil {
			request.Report(Progress{Stage: model.ScanStageApplication, Subject: d.Name})
		}
		paths, err := h.lookPaths(d.Binary)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) || errors.Is(err, ErrNotFound) {
				continue
			}
			return Result{Candidates: result.Candidates, Complete: false, Err: err}
		}
		paths = stableExecutablePaths(paths)
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return Result{Candidates: result.Candidates, Complete: false, Err: err}
			}
			candidate, err := h.discover(ctx, d, path, request.Diagnostic)
			if err != nil {
				return Result{Candidates: result.Candidates, Complete: false, Err: err}
			}
			result.Candidates = append(result.Candidates, candidate)
		}
	}
	return result
}
func (h *PathHandler) ScanApplication(ctx context.Context, app model.Application, request Request) (Candidate, bool, error) {
	if app.Type != model.ApplicationTypeCLI && app.Type != model.ApplicationTypeSDK {
		return Candidate{}, false, nil
	}
	for _, d := range h.definitions {
		if pathDefinitionMatchesApplication(d, app) {
			if path, ok := configuredExecutablePath(app.InstallPath); ok {
				candidate, err := h.discover(ctx, d, path, request.Diagnostic)
				if err == nil {
					candidate.Application.ID = app.ID
					if app.Identity != "" {
						candidate.Application.Identity = app.Identity
					}
				}
				return candidate, err == nil, err
			}
			if isExtendedPathApplicationID(d, app.ID) {
				return Candidate{}, false, ErrNotFound
			}
			paths, err := h.lookPaths(d.Binary)
			if err != nil {
				if errors.Is(err, exec.ErrNotFound) || errors.Is(err, ErrNotFound) {
					return Candidate{}, false, ErrNotFound
				}
				return Candidate{}, false, err
			}
			paths = stableExecutablePaths(paths)
			if len(paths) == 0 {
				return Candidate{}, false, ErrNotFound
			}
			candidate, err := h.discover(ctx, d, paths[0], request.Diagnostic)
			return candidate, err == nil, err
		}
	}
	return Candidate{}, false, ErrNotFound
}

func (h *PathHandler) discover(ctx context.Context, d builtin.PathDefinition, path string, diagnostic func(Diagnostic)) (Candidate, error) {
	versionCommand, err := bindExecutable(d.VersionCommand, d.Binary, path)
	if err != nil {
		return Candidate{}, fmt.Errorf("bind %s version command: %w", d.ID, err)
	}
	desc := d.Description
	actions := &model.ProviderActions{Version: versionCommand}
	actions.Check = h.bindOptionalAction(d, "check", d.CheckCommand, path, diagnostic)
	actions.Update = h.bindOptionalAction(d, "update", d.UpdateCommand, path, diagnostic)
	updateProbe := h.bindOptionalAction(d, "update_probe", d.UpdateProbe, path, diagnostic)
	if d.DownloadURL != "" {
		actions.Download = &model.Download{URL: d.DownloadURL, Filename: d.DownloadFilename}
	}
	enabled := d.Provider != model.ProviderDefault || strings.TrimSpace(d.CheckCommand) != ""
	app := model.Application{ID: PathApplicationID(d.ID), Name: d.Name, Type: model.ApplicationTypeCLI, Description: desc, URL: d.URL, InstallPath: path, Enabled: enabled, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: d.Provider, Actions: actions}, Package: d.Package, ScanManaged: true}
	if d.DownloadURL != "" {
		app.UpdateMode = model.ModeDownload
	} else if actions.Update != "" && !h.probe(ctx, updateProbe) {
		actions.Update = ""
	} else if actions.Update != "" {
		app.UpdateMode = model.ModeAuto
	}
	app.Identity = identity(app)
	c := Candidate{Application: app}
	r, e := h.runner.Run(ctx, versionCommand, nil)
	if e != nil {
		c.ObservationErr = e
	} else if r.ExitCode != 0 {
		c.ObservationErr = CommandExitError{ExitCode: r.ExitCode}
	} else {
		c.CurrentVersion, e = version.Extract(r.Combined())
		c.ObservationErr = e
	}
	return c, nil
}

func (h *PathHandler) bindOptionalAction(definition builtin.PathDefinition, action, command, path string, diagnostic func(Diagnostic)) string {
	bound, err := bindOptionalExecutable(command, definition.Binary, path)
	if err != nil && diagnostic != nil {
		diagnostic(Diagnostic{
			Event:   "path_action_binding_skipped",
			Subject: definition.ID,
			Detail:  fmt.Sprintf("definition=%s action=%s path=%s error=%v", definition.ID, action, path, err),
			Err:     err,
		})
	}
	return bound
}

func defaultLookPaths(binary string) ([]string, error) {
	byCanonical := map[string]string{}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(directory) == "" || !filepath.IsAbs(directory) {
			continue
		}
		visible, err := exec.LookPath(filepath.Join(directory, binary))
		if err != nil {
			continue
		}
		visible, err = filepath.Abs(visible)
		if err != nil {
			continue
		}
		visible = filepath.Clean(visible)
		canonical, err := CanonicalExecutablePath(visible)
		if err != nil {
			continue
		}
		if current, exists := byCanonical[canonical]; !exists || canonicalComparableExecutablePath(visible) < canonicalComparableExecutablePath(current) {
			byCanonical[canonical] = visible
		}
	}
	if len(byCanonical) == 0 {
		return nil, exec.ErrNotFound
	}
	canonicals := make([]string, 0, len(byCanonical))
	for canonical := range byCanonical {
		canonicals = append(canonicals, canonical)
	}
	sort.Strings(canonicals)
	paths := make([]string, 0, len(canonicals))
	for _, canonical := range canonicals {
		paths = append(paths, byCanonical[canonical])
	}
	return paths, nil
}

func stableExecutablePaths(paths []string) []string {
	type executablePath struct{ visible, canonical string }
	byCanonical := map[string]executablePath{}
	for _, value := range paths {
		visible, err := filepath.Abs(strings.TrimSpace(value))
		if err != nil || strings.TrimSpace(value) == "" {
			continue
		}
		visible = filepath.Clean(visible)
		canonical, err := CanonicalExecutablePath(visible)
		if err != nil {
			canonical = canonicalComparableExecutablePath(visible)
		}
		if current, exists := byCanonical[canonical]; !exists || canonicalComparableExecutablePath(visible) < canonicalComparableExecutablePath(current.visible) {
			byCanonical[canonical] = executablePath{visible: visible, canonical: canonical}
		}
	}
	values := make([]executablePath, 0, len(byCanonical))
	for _, value := range byCanonical {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].canonical < values[j].canonical })
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.visible)
	}
	return result
}

// CanonicalExecutablePath returns the stable real path used to compare PATH installations.
func CanonicalExecutablePath(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return canonicalComparableExecutablePath(real), nil
}

func canonicalComparableExecutablePath(value string) string {
	value = filepath.Clean(value)
	if runtimeutil.HostPlatform().Kernel == "windows" {
		return strings.ToLower(value)
	}
	return value
}

func bindExecutable(command, binary, absolutePath string) (string, error) {
	command = strings.TrimSpace(command)
	binary = strings.TrimSpace(binary)
	if command == "" || binary == "" || !filepath.IsAbs(absolutePath) {
		return "", errors.New("command, binary, and absolute executable path are required")
	}
	if command == binary {
		return runtimeutil.QuoteShell(absolutePath), nil
	}
	if strings.HasPrefix(command, binary) {
		rest := command[len(binary):]
		if rest != "" && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r') {
			return runtimeutil.QuoteShell(absolutePath) + rest, nil
		}
	}
	return "", fmt.Errorf("command does not start with executable %q", binary)
}

func bindOptionalExecutable(command, binary, absolutePath string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", nil
	}
	return bindExecutable(command, binary, absolutePath)
}

// RebindPathCandidateActions moves one discovered candidate to an equivalent
// historical visible path without changing the definition's capability scope.
func RebindPathCandidateActions(app model.Application, definition builtin.PathDefinition, absolutePath string) (model.Application, error) {
	versionCommand, err := bindExecutable(definition.VersionCommand, definition.Binary, absolutePath)
	if err != nil {
		return model.Application{}, err
	}
	actions := model.ProviderActions{Version: versionCommand}
	if app.Provider.Actions != nil {
		actions = *app.Provider.Actions
		actions.Version = versionCommand
	}
	if actions.Check != "" {
		actions.Check, err = bindOptionalExecutable(definition.CheckCommand, definition.Binary, absolutePath)
		if err != nil {
			return model.Application{}, err
		}
	}
	if actions.Update != "" {
		actions.Update, err = bindOptionalExecutable(definition.UpdateCommand, definition.Binary, absolutePath)
		if err != nil {
			return model.Application{}, err
		}
	}
	app.InstallPath = filepath.Clean(absolutePath)
	app.Provider.Actions = &actions
	return app, nil
}

func configuredExecutablePath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !filepath.IsAbs(value) {
		return "", false
	}
	info, err := os.Stat(value)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	return filepath.Clean(value), true
}

func pathDefinitionMatchesApplication(d builtin.PathDefinition, app model.Application) bool {
	return PathApplicationID(d.ID) == app.ID || isExtendedPathApplicationID(d, app.ID) || strings.EqualFold(d.Name, app.Name) || (d.Package != "" && strings.EqualFold(d.Package, app.Package))
}

func isExtendedPathApplicationID(d builtin.PathDefinition, id string) bool {
	return isExtendedPathApplicationIDValue(d.ID, id)
}

func isExtendedPathApplicationIDValue(definitionID, id string) bool {
	prefix := PathApplicationID(definitionID) + "-"
	fingerprint := strings.TrimPrefix(id, prefix)
	if len(fingerprint) != PathInstanceFingerprintLength || !strings.HasPrefix(id, prefix) {
		return false
	}
	for _, character := range fingerprint {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// PathApplicationID returns the canonical base ID for one built-in PATH definition.
func PathApplicationID(id string) string {
	id = strings.TrimPrefix(strings.TrimSpace(id), "cli-")
	return "cli-" + slug(id)
}
func (h *PathHandler) probe(ctx context.Context, s string) bool {
	if s == "" {
		return false
	}
	r, e := h.runner.Run(ctx, s, nil)
	return e == nil && r.ExitCode == 0
}
func identity(app model.Application) string {
	return "cli:" + model.NormalizeIdentityName(app.Name)
}
