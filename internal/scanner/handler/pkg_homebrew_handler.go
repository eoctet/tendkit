package handler

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// HomebrewFormulaHandler inventories only installed formulae. Casks are kept
// separate because their application ownership requires the macOS bundle scan.
type HomebrewFormulaHandler struct {
	runner   Runner
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	homeDir  func() (string, error)
	host     func() string
}

// HomebrewCaskHandler is macOS-only. On other platforms it is explicitly
// not-applicable and therefore complete without requiring brew to exist.
type HomebrewCaskHandler struct {
	runner   Runner
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	homeDir  func() (string, error)
	host     func() string
}

func NewHomebrewCask(r Runner) *HomebrewCaskHandler {
	return &HomebrewCaskHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, homeDir: os.UserHomeDir, host: func() string { return runtimeutil.HostPlatform().Kernel }}
}
func (*HomebrewCaskHandler) Domain() Domain { return HomebrewCask }
func (h *HomebrewCaskHandler) Scan(ctx context.Context, request Request) Result {
	if h.host() != "darwin" {
		return Result{Complete: true}
	}
	brew := managerPath("brew", request.Configured, h.lookPath, h.stat, h.homeDir)
	if brew == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "brew"}}
	}
	reportPackageProgress(request, model.ScanStagePackageList, "Homebrew cask")
	r, err := h.runner.Run(ctx, runtimeutil.QuoteShell(brew)+" list --cask --versions --json", nil)
	if err != nil || r.ExitCode != 0 {
		return Result{Complete: false, Err: inventoryError("homebrew-cask", err, r.ExitCode)}
	}
	var inventory homebrewCaskInventory
	if err := json.Unmarshal([]byte(r.Stdout), &inventory); err != nil || inventory.Casks == nil {
		return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-cask", Message: "invalid Homebrew cask inventory"}}
	}
	caskroomResult, caskroomErr := h.runner.Run(ctx, runtimeutil.QuoteShell(brew)+" --caskroom", nil)
	if caskroomErr != nil || caskroomResult.ExitCode != 0 {
		return Result{Complete: false, Err: inventoryError("homebrew-cask", caskroomErr, caskroomResult.ExitCode)}
	}
	caskroom, pathErr := singleHomebrewPath(caskroomResult.Stdout)
	if pathErr != nil {
		return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-cask", Message: "Homebrew Caskroom path is invalid"}}
	}
	caskroom, pathErr = filepath.EvalSymlinks(caskroom)
	if pathErr != nil {
		return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-cask", Message: "Homebrew Caskroom path is missing"}}
	}
	result := Result{Complete: true}
	for _, item := range *inventory.Casks {
		version, versionErr := activeHomebrewCaskVersion(item)
		if versionErr != nil || !validHomebrewFormulaPathComponent(item.Token) {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-cask", Message: "ambiguous Homebrew cask inventory"}}
		}
		prefix, pathErr := filepath.EvalSymlinks(filepath.Join(caskroom, item.Token, version))
		if pathErr != nil || !pathWithinRoot(prefix, caskroom) {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-cask", Message: "Homebrew cask prefix is invalid"}}
		}
		tap, appNames, receiptErr := homebrewCaskReceipt(caskroom, item.Token)
		if receiptErr != nil {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-cask", Message: "Homebrew cask receipt is incomplete"}}
		}
		paths, pathErr := homebrewCaskApplicationPaths(ctx, prefix, appNames)
		if pathErr != nil {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-cask", Message: "Homebrew cask application inventory is incomplete"}}
		}
		if len(paths) == 0 {
			continue
		}
		canonical, nameErr := homebrewInstalledName(item.Token, tap, "homebrew/cask")
		if nameErr != nil {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-cask", Message: "Homebrew cask source tap is invalid"}}
		}
		ambiguity := ""
		mode := model.ModeAuto
		if len(paths) > 1 {
			ambiguity = "multiple-application-paths"
		}
		reportPackageProgress(request, model.ScanStageApplication, canonical)
		app := model.Application{ID: "pkg-homebrew-cask-" + packageSlug(canonical), Name: canonical, Type: model.ApplicationTypeBundle, InstallPath: paths[0], Enabled: true, UpdateMode: mode, Provider: model.ProviderConfig{Type: model.ProviderHomebrew}, Package: "cask/" + canonical, Identity: model.PackageIdentity("homebrew-cask", canonical), ScanManaged: true}
		candidate := packageCandidate(app, "", "homebrew-cask:"+canonical)
		candidate.Evidence = &InstallationEvidence{Source: "homebrew-cask", Package: app.Package, ApplicationPaths: paths, Ambiguity: ambiguity}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result
}

func NewHomebrewFormula(r Runner) *HomebrewFormulaHandler {
	return &HomebrewFormulaHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, homeDir: os.UserHomeDir, host: func() string { return runtimeutil.HostPlatform().Kernel }}
}
func (*HomebrewFormulaHandler) Domain() Domain { return HomebrewFormula }
func (h *HomebrewFormulaHandler) Scan(ctx context.Context, request Request) Result {
	if h.host() == "windows" {
		return Result{Complete: true}
	}
	brew := h.manager(request.Configured)
	if brew == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "brew"}}
	}
	reportPackageProgress(request, model.ScanStagePackageList, "Homebrew formula")
	r, err := h.runner.Run(ctx, runtimeutil.QuoteShell(brew)+" list --formula --versions --json", nil)
	if err != nil || r.ExitCode != 0 {
		return Result{Complete: false, Err: inventoryError("homebrew-formula", err, r.ExitCode)}
	}
	if err := ctx.Err(); err != nil {
		return Result{Complete: false, Err: err}
	}
	var inventory homebrewFormulaInventory
	if err := json.Unmarshal([]byte(r.Stdout), &inventory); err != nil || inventory.Formulae == nil {
		return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "invalid Homebrew formula inventory"}}
	}
	cellarResult, cellarErr := h.runner.Run(ctx, runtimeutil.QuoteShell(brew)+" --cellar", nil)
	if cellarErr != nil || cellarResult.ExitCode != 0 {
		return Result{Complete: false, Err: inventoryError("homebrew-formula", cellarErr, cellarResult.ExitCode)}
	}
	if err := ctx.Err(); err != nil {
		return Result{Complete: false, Err: err}
	}
	cellar, cellarParseErr := singleHomebrewPath(cellarResult.Stdout)
	if cellarParseErr != nil {
		return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew Cellar path is invalid"}}
	}
	cellar, cellarParseErr = filepath.EvalSymlinks(cellar)
	if cellarParseErr != nil {
		return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew Cellar path is missing"}}
	}
	result := Result{Complete: true}
	seen := make(map[string]struct{}, len(*inventory.Formulae))
	for _, item := range *inventory.Formulae {
		if err := ctx.Err(); err != nil {
			return Result{Complete: false, Err: err}
		}
		rack := item.Name
		if !validHomebrewFormulaPathComponent(rack) {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula name is invalid"}}
		}
		if _, exists := seen[rack]; exists {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula target is not unique"}}
		}
		seen[rack] = struct{}{}
		version, versionErr := activeHomebrewFormulaVersion(item)
		if versionErr != nil {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "ambiguous Homebrew formula inventory"}}
		}
		pinned, pinnedErr := homebrewFormulaPinned(item)
		if pinnedErr != nil {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "invalid Homebrew formula pinned version"}}
		}
		rackPath, pathErr := filepath.EvalSymlinks(filepath.Join(cellar, rack))
		if pathErr != nil || !pathWithinRoot(rackPath, cellar) {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula rack is invalid"}}
		}
		prefix, pathErr := filepath.EvalSymlinks(filepath.Join(rackPath, version))
		if pathErr != nil || !pathWithinRoot(prefix, rackPath) {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula prefix is invalid"}}
		}
		receiptPath, pathErr := filepath.EvalSymlinks(filepath.Join(prefix, "INSTALL_RECEIPT.json"))
		if pathErr != nil || !pathWithinRoot(receiptPath, prefix) {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula receipt is invalid"}}
		}
		receiptInfo, statErr := h.stat(receiptPath)
		if statErr != nil || !receiptInfo.Mode().IsRegular() {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula receipt is invalid"}}
		}
		if err := ctx.Err(); err != nil {
			return Result{Complete: false, Err: err}
		}
		receiptRaw, readErr := os.ReadFile(receiptPath)
		if err := ctx.Err(); err != nil {
			return Result{Complete: false, Err: err}
		}
		var receipt homebrewFormulaReceipt
		if readErr != nil || json.Unmarshal(receiptRaw, &receipt) != nil || receipt.InstalledOnRequest == nil || receipt.Source == nil || receipt.Source.Tap == nil {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula receipt is incomplete"}}
		}
		if !*receipt.InstalledOnRequest {
			continue
		}
		name, nameErr := canonicalHomebrewFormulaName(rack, *receipt.Source.Tap)
		if nameErr != nil {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula source tap is invalid"}}
		}
		reportPackageProgress(request, model.ScanStageApplication, name)
		paths, walkErr := homebrewFormulaExecutablePaths(ctx, prefix, h.stat)
		if walkErr != nil {
			if err := ctx.Err(); err != nil {
				return Result{Complete: false, Err: err}
			}
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula executable inventory is incomplete"}}
		}
		if len(paths) == 0 {
			return Result{Complete: false, Err: &PackageInventoryIncompleteError{Ecosystem: "homebrew-formula", Message: "Homebrew formula has no executable paths"}}
		}
		mode := model.ModeAuto
		if pinned {
			mode = model.ModeCheck
		}
		app := model.Application{ID: "pkg-homebrew-formula-" + packageSlug(name), Name: name, Type: model.ApplicationTypePackage, InstallPath: paths[0], Enabled: true, UpdateMode: mode, Provider: model.ProviderConfig{Type: model.ProviderHomebrew}, Package: "formula/" + name, Identity: model.PackageIdentity("homebrew-formula", name), ScanManaged: true}
		candidate := packageCandidate(app, version, "homebrew-formula:"+name)
		candidate.Evidence = &InstallationEvidence{Source: "homebrew-formula", Package: app.Package, ExecutablePaths: paths, InstallRoot: prefix}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result
}

type homebrewFormulaInventory struct {
	Formulae *[]homebrewFormulaItem `json:"formulae"`
}

type homebrewCaskInventory struct {
	Casks *[]homebrewCaskItem `json:"casks"`
}

type homebrewCaskItem struct {
	Token         string   `json:"token"`
	Versions      []string `json:"versions"`
	PinnedVersion string   `json:"pinned_version"`
}

type homebrewFormulaItem struct {
	Name             string   `json:"name"`
	Versions         []string `json:"versions"`
	LinkedVersion    string   `json:"linked_version"`
	OptlinkedVersion string   `json:"optlinked_version"`
	PinnedVersion    string   `json:"pinned_version"`
}

type homebrewFormulaReceipt struct {
	InstalledOnRequest *bool `json:"installed_on_request"`
	Source             *struct {
		Tap *string `json:"tap"`
	} `json:"source"`
}

func activeHomebrewFormulaVersion(item homebrewFormulaItem) (string, error) {
	versions := make(map[string]struct{}, len(item.Versions))
	for _, version := range item.Versions {
		if !validHomebrewFormulaPathComponent(version) {
			return "", os.ErrInvalid
		}
		if _, exists := versions[version]; exists {
			return "", os.ErrInvalid
		}
		versions[version] = struct{}{}
	}
	for _, reference := range []string{item.LinkedVersion, item.OptlinkedVersion} {
		if reference == "" {
			continue
		}
		if !validHomebrewFormulaPathComponent(reference) {
			return "", os.ErrInvalid
		}
		if _, exists := versions[reference]; !exists {
			return "", os.ErrInvalid
		}
	}
	selected := item.LinkedVersion
	if selected == "" {
		selected = item.OptlinkedVersion
	}
	if selected == "" {
		if len(item.Versions) != 1 {
			return "", os.ErrInvalid
		}
		selected = item.Versions[0]
	}
	if !validHomebrewFormulaPathComponent(selected) {
		return "", os.ErrInvalid
	}
	if _, exists := versions[selected]; !exists {
		return "", os.ErrInvalid
	}
	return selected, nil
}

func validHomebrewFormulaPathComponent(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && value != "." && value != ".." && !filepath.IsAbs(value) && filepath.Base(value) == value && !strings.ContainsAny(value, `/\`)
}

func canonicalHomebrewFormulaName(rack, tap string) (string, error) {
	return homebrewInstalledName(rack, tap, "homebrew/core")
}

func homebrewInstalledName(token, tap, defaultTap string) (string, error) {
	if tap == defaultTap {
		return token, nil
	}
	parts := strings.Split(tap, "/")
	if len(parts) != 2 || !validHomebrewFormulaPathComponent(parts[0]) || !validHomebrewFormulaPathComponent(parts[1]) {
		return "", os.ErrInvalid
	}
	return tap + "/" + token, nil
}

func homebrewFormulaPinned(item homebrewFormulaItem) (bool, error) {
	if item.PinnedVersion == "" {
		return false, nil
	}
	if !validHomebrewFormulaPathComponent(item.PinnedVersion) {
		return false, os.ErrInvalid
	}
	for _, version := range item.Versions {
		if version == item.PinnedVersion {
			return true, nil
		}
	}
	return false, os.ErrInvalid
}

func activeHomebrewCaskVersion(item homebrewCaskItem) (string, error) {
	if len(item.Versions) != 1 || !validHomebrewFormulaPathComponent(item.Versions[0]) {
		return "", os.ErrInvalid
	}
	if item.PinnedVersion != "" && item.PinnedVersion != item.Versions[0] {
		return "", os.ErrInvalid
	}
	return item.Versions[0], nil
}

func homebrewCaskReceipt(caskroom, token string) (string, []string, error) {
	receiptPath, err := filepath.EvalSymlinks(filepath.Join(caskroom, token, ".metadata", "INSTALL_RECEIPT.json"))
	if err != nil || !pathWithinRoot(receiptPath, filepath.Join(caskroom, token)) {
		return "", nil, os.ErrInvalid
	}
	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		return "", nil, err
	}
	var receipt struct {
		Source *struct {
			Tap *string `json:"tap"`
		} `json:"source"`
		UninstallArtifacts []map[string]json.RawMessage `json:"uninstall_artifacts"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Source == nil || receipt.Source.Tap == nil {
		return "", nil, os.ErrInvalid
	}
	var appNames []string
	for _, artifact := range receipt.UninstallArtifacts {
		rawApp, exists := artifact["app"]
		if !exists {
			continue
		}
		var values []json.RawMessage
		if json.Unmarshal(rawApp, &values) != nil || len(values) == 0 {
			return "", nil, os.ErrInvalid
		}
		var name string
		if json.Unmarshal(values[0], &name) != nil || !validHomebrewCaskAppPath(name) {
			return "", nil, os.ErrInvalid
		}
		appNames = append(appNames, filepath.Clean(name))
	}
	return *receipt.Source.Tap, appNames, nil
}

func validHomebrewCaskAppPath(name string) bool {
	clean := filepath.Clean(strings.TrimSpace(name))
	return clean != "." && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator)) && strings.HasSuffix(strings.ToLower(clean), ".app")
}

func homebrewCaskApplicationPaths(ctx context.Context, prefix string, appNames []string) ([]string, error) {
	paths := make([]string, 0, len(appNames))
	for _, name := range appNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		canonical, err := filepath.EvalSymlinks(filepath.Join(prefix, name))
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			return nil, os.ErrInvalid
		}
		paths = append(paths, canonical)
	}
	paths = uniquePaths(paths)
	sort.Strings(paths)
	return paths, nil
}

func homebrewFormulaExecutablePaths(ctx context.Context, prefix string, stat func(string) (os.FileInfo, error)) ([]string, error) {
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	err := filepath.WalkDir(prefix, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == prefix || entry.IsDir() {
			return nil
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || !pathWithinRoot(canonical, prefix) {
			return nil
		}
		info, err := stat(canonical)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return nil
		}
		if _, exists := seen[canonical]; exists {
			return nil
		}
		seen[canonical] = struct{}{}
		paths = append(paths, canonical)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func singleHomebrewPath(raw string) (string, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return "", os.ErrInvalid
	}
	return filepath.Clean(strings.TrimSpace(lines[0])), nil
}

func pathWithinRoot(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
func (h *HomebrewFormulaHandler) manager(configured []model.Application) string {
	return managerPath("brew", configured, h.lookPath, h.stat, h.homeDir)
}
