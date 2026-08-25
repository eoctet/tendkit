package handler

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

type NodeHandler struct {
	runner   Runner
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	env      func() []string
	homeDir  func() (string, error)
	readFile func(string) ([]byte, error)
}

func NewNode(r Runner) *NodeHandler {
	return &NodeHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, env: os.Environ, homeDir: os.UserHomeDir, readFile: os.ReadFile}
}
func (*NodeHandler) Domain() Domain { return Node }
func (h *NodeHandler) Scan(ctx context.Context, r Request) Result {
	npm := h.manager(r.Configured)
	if npm == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "npm"}}
	}
	report := func(stage, subject string) {
		if r.Report != nil {
			r.Report(Progress{Stage: stage, Subject: subject})
		}
	}
	report(model.ScanStagePackageList, "Node.js")
	result, err := h.runner.Run(ctx, runtimeutil.QuoteShell(npm)+" list -g --depth=0 --json", h.environment(npm, r.Configured))
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
	if rr, e := h.runner.Run(ctx, runtimeutil.QuoteShell(npm)+" root -g", h.environment(npm, r.Configured)); e == nil {
		root = strings.TrimSpace(rr.Stdout)
	}
	out := Result{Complete: complete}
	for name, item := range list.Dependencies {
		if e := ctx.Err(); e != nil {
			out.Complete = false
			out.Err = e
			return out
		}
		report(model.ScanStageApplication, name)
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
		app := model.Application{ID: "pkg-node-" + packageSlug(name), Name: name, Type: model.ApplicationTypePackage, Description: meta.description, URL: meta.url, InstallPath: path, Enabled: true, UpdateMode: model.ModeAuto, Provider: model.ProviderConfig{Type: model.ProviderNPM, Actions: &model.ProviderActions{Version: versionCommand, Update: updateCommand}}, Package: name, Identity: model.PackageIdentity("node", name), ScanManaged: true}
		out.Candidates = append(out.Candidates, Candidate{Application: app, CurrentVersion: version.Normalize(item.Version), Aliases: []string{"node:" + name}})
	}
	if !out.Complete && out.Err == nil {
		out.Err = &PackageInventoryIncompleteError{Ecosystem: "Node.js", Message: "incomplete Node.js package inventory"}
	}
	return out
}

type nodeMeta struct{ description, url string }

func (h *NodeHandler) metadata(installPath string) nodeMeta {
	content, err := h.readFile(filepath.Join(installPath, "package.json"))
	if err != nil {
		return nodeMeta{}
	}
	var manifest struct {
		Description string          `json:"description"`
		Homepage    string          `json:"homepage"`
		Repository  json.RawMessage `json:"repository"`
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
	return nodeMeta{description: strings.TrimSpace(manifest.Description), url: url}
}
func (h *NodeHandler) manager(configured []model.Application) string {
	if p, e := h.lookPath("npm"); e == nil {
		return p
	}
	for _, a := range configured {
		if strings.EqualFold(a.ID, "npm") || strings.EqualFold(a.Name, "npm") {
			path := expandConfiguredPath(a.InstallPath, h.homeDir)
			if info, e := h.stat(path); e == nil && !info.IsDir() {
				return path
			}
		}
	}
	return ""
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
