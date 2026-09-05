package handler

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/cargoroot"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// CargoHandler uses cargo's public install list and deliberately emits Check
// mode: the public listing cannot prove original installation options.
type CargoHandler struct {
	runner   Runner
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	homeDir  func() (string, error)
	cwd      func() (string, error)
	readFile func(string) ([]byte, error)
	getenv   func(string) string
}

// NewCargo creates a handler for binaries installed by the selected Cargo instance.
func NewCargo(r Runner) *CargoHandler {
	return &CargoHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, homeDir: os.UserHomeDir, cwd: os.Getwd, readFile: os.ReadFile, getenv: os.Getenv}
}
func (*CargoHandler) Domain() Domain { return Cargo }

var cargoInventoryHeader = regexp.MustCompile(`(?m)^([^\s]+)\s+v([^\s:]+):\s*$`)

func (h *CargoHandler) Scan(ctx context.Context, request Request) Result {
	ecosystem := string(h.Domain())
	cargo := h.manager(request.Configured)
	if cargo == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "cargo"}}
	}
	cargo = filepath.Clean(cargo)
	root := h.cargoInstallRoot(request.Configured)
	if root == "" {
		return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: ecosystem, Message: "Cargo install root unavailable"}}
	}
	environment := h.executionEnvironment(cargo, root)
	reportPackageProgress(request, model.ScanStagePackageList, "Cargo")
	r, err := h.runner.Run(ctx, runtimeutil.QuoteShell(cargo)+" install --list --root "+runtimeutil.QuoteShell(root), environment)
	if err != nil || r.ExitCode != 0 {
		return Result{Complete: false, Err: inventoryError(ecosystem, err)}
	}
	entries, parseErr := parseCargoInstallList(r.Stdout)
	if parseErr != nil {
		return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: ecosystem, Message: "invalid Cargo install inventory"}}
	}
	canonicalBinaryRoot := ""
	if len(entries) > 0 {
		canonicalBinaryRoot, err = filepath.EvalSymlinks(filepath.Join(root, "bin"))
		if err != nil {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: ecosystem, Message: "Cargo binary root is missing"}}
		}
	}
	result := Result{Complete: true}
	for _, entry := range entries {
		name, current := entry.name, entry.version
		reportPackageProgress(request, model.ScanStageApplication, name)
		if len(entry.binaries) == 0 {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: ecosystem, Message: "Cargo crate has no binary evidence"}}
		}
		sort.Strings(entry.binaries)
		path := filepath.Join(root, "bin", entry.binaries[0])
		app := model.Application{ID: "pkg-cargo-" + packageSlug(name), Name: name, Type: model.ApplicationTypePackage, InstallPath: path, Enabled: false, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderCargo}, Package: name, Identity: model.PackageIdentity(string(h.Domain()), name), ScanManaged: true, Environment: environment}
		paths := make([]string, 0, len(entry.binaries))
		for _, binary := range entry.binaries {
			owned, err := filepath.EvalSymlinks(filepath.Join(root, "bin", binary))
			if err != nil || !pathWithinRoot(owned, canonicalBinaryRoot) {
				return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: ecosystem, Message: "Cargo binary path is outside install root"}}
			}
			paths = append(paths, owned)
		}
		path = paths[0]
		app.InstallPath = path
		candidate := packageCandidate(app, current, ecosystem+":"+name)
		candidate.Evidence = &InstallationEvidence{Source: ecosystem, Package: name, ExecutablePaths: paths, InstallRoot: root}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result
}

func (h *CargoHandler) executionEnvironment(cargo, root string) map[string]string {
	managerDir := filepath.Dir(cargo)
	path := managerDir
	getenv := h.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if existing := strings.TrimSpace(getenv("PATH")); existing != "" {
		entries := []string{managerDir}
		for _, entry := range filepath.SplitList(existing) {
			if filepath.Clean(entry) == filepath.Clean(managerDir) {
				continue
			}
			entries = append(entries, entry)
		}
		path = strings.Join(entries, string(os.PathListSeparator))
	}
	return map[string]string{"CARGO_INSTALL_ROOT": root, "PATH": path}
}

type cargoInstallEntry struct {
	name, version string
	binaries      []string
}

func parseCargoInstallList(raw string) ([]cargoInstallEntry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var result []cargoInstallEntry
	current := -1
	for _, line := range strings.Split(raw, "\n") {
		if match := cargoInventoryHeader.FindStringSubmatch(line); len(match) == 3 {
			result = append(result, cargoInstallEntry{name: match[1], version: match[2]})
			current = len(result) - 1
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if current < 0 || !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			return nil, errors.New("invalid cargo install list")
		}
		binary := strings.TrimSpace(line)
		if strings.ContainsAny(binary, " \t") || !validCargoBinaryName(binary) {
			return nil, errors.New("invalid cargo binary")
		}
		result[current].binaries = append(result[current].binaries, binary)
	}
	return result, nil
}

func validCargoBinaryName(value string) bool {
	return value != "" && value != "." && value != ".." && !filepath.IsAbs(value) && filepath.Base(value) == value && !strings.ContainsAny(value, `/\`)
}
func (h *CargoHandler) manager(configured []model.Application) string {
	return managerPath("cargo", configured, h.lookPath, h.stat, h.homeDir)
}
func (h *CargoHandler) cargoInstallRoot(configured []model.Application) string {
	environment := map[string]string{}
	for _, app := range configured {
		if (app.Provider.Type != model.ProviderCargo && !strings.EqualFold(app.ID, "cargo") && !strings.EqualFold(app.Name, "cargo")) || app.Environment == nil {
			continue
		}
		for _, key := range []string{"CARGO_INSTALL_ROOT", "CARGO_HOME"} {
			value := expandConfiguredPath(app.Environment[key], h.homeDir)
			if value == "" {
				continue
			}
			if existing := environment[key]; existing != "" && filepath.Clean(existing) != filepath.Clean(value) {
				return ""
			}
			environment[key] = value
		}
	}
	if len(environment) > 0 {
		root, err := cargoroot.InstallRoot(environment, cargoroot.Dependencies{Getwd: h.cwd, ReadFile: h.readFile, UserHomeDir: h.homeDir})
		if err == nil {
			return root
		}
		return ""
	}
	root, err := cargoroot.InstallRoot(nil, cargoroot.Dependencies{Getwd: h.cwd, ReadFile: h.readFile, UserHomeDir: h.homeDir})
	if err != nil {
		return ""
	}
	return root
}
