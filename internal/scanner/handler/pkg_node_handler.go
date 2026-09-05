package handler

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type NodeHandler struct {
	runner       Runner
	lookPath     func(string) (string, error)
	stat         func(string) (os.FileInfo, error)
	env          func() []string
	homeDir      func() (string, error)
	readFile     func(string) ([]byte, error)
	evalSymlinks func(string) (string, error)
}

func NewNode(r Runner) *NodeHandler {
	return &NodeHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, env: os.Environ, homeDir: os.UserHomeDir, readFile: os.ReadFile, evalSymlinks: filepath.EvalSymlinks}
}
func (*NodeHandler) Domain() Domain { return Node }
func (h *NodeHandler) Scan(ctx context.Context, r Request) Result {
	npm := h.manager(r.Configured)
	if npm == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "npm"}}
	}
	environment := h.environment(npm, r.Configured)
	reportPackageProgress(r, model.ScanStagePackageList, "Node.js")
	result, err := h.runner.Run(ctx, runtimeutil.QuoteShell(npm)+" list -g --depth=0 --json", environment)
	if e := ctx.Err(); e != nil {
		return Result{Complete: false, Err: e}
	}
	if err != nil && result.Stdout == "" {
		return Result{Complete: false, Err: err}
	}
	var list struct {
		Dependencies map[string]struct {
			Version string `json:"version"`
			Path    string `json:"path"`
		} `json:"dependencies"`
	}
	if e := json.Unmarshal([]byte(result.Stdout), &list); e != nil {
		return Result{Complete: false, Err: e}
	}
	complete := err == nil && result.ExitCode == 0
	root := ""
	rootOK := false
	if rr, e := h.runner.Run(ctx, runtimeutil.QuoteShell(npm)+" root -g", environment); e == nil && rr.ExitCode == 0 {
		root = strings.TrimSpace(rr.Stdout)
		rootOK = filepath.IsAbs(root)
	}
	if e := ctx.Err(); e != nil {
		return Result{Complete: false, Err: e}
	}
	prefixResult, prefixErr := h.runner.Run(ctx, runtimeutil.QuoteShell(npm)+" prefix -g", environment)
	prefix := strings.TrimSpace(prefixResult.Stdout)
	prefixOK := prefixErr == nil && prefixResult.ExitCode == 0 && filepath.IsAbs(prefix)
	if e := ctx.Err(); e != nil {
		return Result{Complete: false, Err: e}
	}
	out := Result{Complete: complete && rootOK && prefixOK}
	names := make([]string, 0, len(list.Dependencies))
	for name := range list.Dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		item := list.Dependencies[name]
		if e := ctx.Err(); e != nil {
			out.Complete = false
			out.Err = e
			return out
		}
		reportPackageProgress(r, model.ScanStageApplication, name)
		if e := ctx.Err(); e != nil {
			out.Complete = false
			out.Err = e
			return out
		}
		path := item.Path
		if path == "" && root != "" {
			path = filepath.Join(root, filepath.FromSlash(name))
		}
		if strings.TrimSpace(path) == "" {
			out.Complete = false
			continue
		}
		meta := h.metadata(path)
		target := metadatautil.PackageTarget{Ecosystem: metadatautil.PackageNode, Manager: npm, Name: name, InstallPath: path}
		versionCommand, _ := metadatautil.PackageVersionCommand(target)
		updateCommand, _ := metadatautil.PackageUpdateCommand(target)
		app := model.Application{ID: "pkg-node-" + packageSlug(name), Name: name, Type: model.ApplicationTypePackage, Description: meta.description, URL: meta.url, InstallPath: path, Enabled: true, UpdateMode: model.ModeAuto, Provider: model.ProviderConfig{Type: model.ProviderNPM, Actions: &model.ProviderActions{Version: versionCommand, Update: updateCommand}}, Package: name, Identity: model.PackageIdentity(string(h.Domain()), name), ScanManaged: true}
		candidate := packageCandidate(app, item.Version, "node:"+name)
		if !meta.manifestComplete || (meta.binDeclared && (!meta.binValid || !prefixOK)) {
			out.Complete = false
			continue
		}
		if len(meta.bin) > 0 {
			paths, valid := h.binEvidence(path, prefix, meta.bin)
			if !valid {
				out.Complete = false
				continue
			}
			candidate.Evidence = &InstallationEvidence{Source: string(h.Domain()), Package: name, ExecutablePaths: paths, InstallRoot: path}
		}
		out.Candidates = append(out.Candidates, candidate)
	}
	if !out.Complete && out.Err == nil {
		out.Err = &PackageInventoryIncompleteError{Ecosystem: "Node.js", Message: "incomplete Node.js package inventory"}
	}
	return out
}

type nodeMeta struct {
	description, url      string
	bin                   map[string]string
	binDeclared, binValid bool
	manifestComplete      bool
}

func (h *NodeHandler) metadata(installPath string) nodeMeta {
	content, err := h.readFile(filepath.Join(installPath, "package.json"))
	if err != nil {
		return nodeMeta{}
	}
	var manifest struct {
		Description string          `json:"description"`
		Homepage    string          `json:"homepage"`
		Repository  json.RawMessage `json:"repository"`
		Bin         json.RawMessage `json:"bin"`
	}
	if json.Unmarshal(content, &manifest) != nil {
		return nodeMeta{}
	}
	url := githubProjectURL(strings.TrimSpace(manifest.Homepage))
	if url == "" && len(manifest.Repository) > 0 {
		var repository string
		if json.Unmarshal(manifest.Repository, &repository) != nil {
			var value struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(manifest.Repository, &value) == nil {
				repository = value.URL
			}
		}
		url = githubProjectURL(strings.TrimSpace(repository))
	}
	meta := nodeMeta{description: strings.TrimSpace(manifest.Description), url: url, manifestComplete: true}
	if len(manifest.Bin) > 0 {
		meta.binDeclared = true
		var single string
		if json.Unmarshal(manifest.Bin, &single) == nil {
			meta.bin = map[string]string{nodeBinName(filepath.Base(installPath)): single}
		} else {
			if json.Unmarshal(manifest.Bin, &meta.bin) != nil {
				return meta
			}
		}
		meta.binValid = len(meta.bin) > 0
	}
	return meta
}

func nodeBinName(name string) string { return strings.TrimPrefix(filepath.Base(name), "@") }
func validExecutableName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}
func (h *NodeHandler) binEvidence(installPath, prefix string, bins map[string]string) ([]string, bool) {
	if !filepath.IsAbs(prefix) {
		return nil, false
	}
	names := make([]string, 0, len(bins))
	for name := range bins {
		names = append(names, name)
	}
	sort.Strings(names)
	paths := make([]string, 0, len(names))
	seen := map[string]bool{}
	root := filepath.Clean(installPath)
	canonicalRoot, err := h.evalSymlinks(root)
	if err != nil {
		return nil, false
	}
	for _, name := range names {
		target := bins[name]
		if !validExecutableName(name) || !filepath.IsAbs(installPath) || filepath.IsAbs(target) {
			return nil, false
		}
		want := filepath.Clean(filepath.Join(root, filepath.FromSlash(target)))
		// Reject traversal lexically before resolving symlinks, then compare the
		// canonical manifest target to the global wrapper target exactly.
		if want != root && !strings.HasPrefix(want, root+string(filepath.Separator)) {
			return nil, false
		}
		canonicalWant, e := h.evalSymlinks(want)
		if e != nil || (canonicalWant != canonicalRoot && !strings.HasPrefix(canonicalWant, canonicalRoot+string(filepath.Separator))) {
			return nil, false
		}
		link := filepath.Join(prefix, "bin", name)
		info, e := h.stat(link)
		if e != nil || !validEvidenceFile(info) {
			return nil, false
		}
		actual, e := h.evalSymlinks(link)
		if e != nil || filepath.Clean(actual) != filepath.Clean(canonicalWant) {
			return nil, false
		}
		if !seen[link] {
			seen[link] = true
			paths = append(paths, link)
		}
	}
	return paths, len(paths) > 0
}
func (h *NodeHandler) manager(configured []model.Application) string {
	return managerPath("npm", configured, h.lookPath, h.stat, h.homeDir)
}
func (h *NodeHandler) environment(npm string, configured []model.Application) map[string]string {
	dirs := []string{filepath.Dir(npm)}
	for _, a := range configured {
		if strings.EqualFold(a.ID, "node") || strings.EqualFold(a.ID, "npm") || strings.EqualFold(a.Name, "Node.js") || strings.EqualFold(a.Name, "npm") {
			dirs = append(dirs, filepath.Dir(expandConfiguredPath(a.InstallPath, h.homeDir)))
		}
	}
	for _, value := range h.env() {
		if strings.HasPrefix(value, "PATH=") {
			dirs = append(dirs, strings.TrimPrefix(value, "PATH="))
			break
		}
	}
	dirs = uniquePaths(dirs)
	return map[string]string{"PATH": strings.Join(dirs, string(os.PathListSeparator))}
}

func uniquePaths(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
