package handler

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
)

const (
	systemAppsDirectory = "/Applications"
	userAppsDirectory   = "Applications"
)

type MacAppHandler struct {
	definitions             []builtin.MacAppDefinition
	custom                  map[string]bool
	readDir                 func(string) ([]os.DirEntry, error)
	homeDir                 func() (string, error)
	inspect                 func(context.Context, string) (macInfo, error)
	resolveJetBrainsProduct func(string) (string, bool)
}
type macInfo struct{ path, name, bundleID, category, description, version, feed, jetBrainsProductCode string }

func NewMacApp(defs []builtin.MacAppDefinition, ids []string) *MacAppHandler {
	custom := map[string]bool{}
	for _, id := range ids {
		custom[strings.ToLower(strings.TrimSpace(id))] = true
	}
	return &MacAppHandler{definitions: append([]builtin.MacAppDefinition(nil), defs...), custom: custom, readDir: os.ReadDir, homeDir: os.UserHomeDir, inspect: inspectMacApp, resolveJetBrainsProduct: canonicalJetBrainsProduct}
}
func (h *MacAppHandler) Domain() Domain { return MacApp }
func (h *MacAppHandler) BundleID(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := h.inspect(ctx, path)
	return info.bundleID, err
}

func (h *MacAppHandler) Inspect(ctx context.Context, path string) (BundleMetadata, error) {
	if err := ctx.Err(); err != nil {
		return BundleMetadata{}, err
	}
	info, err := h.inspect(ctx, path)
	return BundleMetadata{
		Path: info.path, Name: info.name, BundleID: info.bundleID,
		Category: info.category, Description: info.description,
		Version: info.version, FeedURL: info.feed,
	}, err
}
func (h *MacAppHandler) Scan(ctx context.Context, r Request) Result {
	dirs := []string{systemAppsDirectory}
	if home, e := h.homeDir(); e == nil {
		dirs = append(dirs, filepath.Join(home, userAppsDirectory))
	}
	out := Result{Complete: true}
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries, e := h.readDir(dir)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return Result{Candidates: out.Candidates, Complete: false, Err: e}
		}
		for _, entry := range entries {
			if e := ctx.Err(); e != nil {
				return Result{Candidates: out.Candidates, Complete: false, Err: e}
			}
			if !entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".app") {
				continue
			}
			c, ok, inspectErr := h.candidate(ctx, filepath.Join(dir, entry.Name()), r)
			if inspectErr != nil {
				return Result{Candidates: out.Candidates, Complete: false, Err: inspectErr}
			}
			if ok && !seen[c.Application.Identity] {
				seen[c.Application.Identity] = true
				out.Candidates = append(out.Candidates, c)
			}
		}
	}
	sort.Slice(out.Candidates, func(i, j int) bool {
		return strings.ToLower(out.Candidates[i].Application.Name) < strings.ToLower(out.Candidates[j].Application.Name)
	})
	return out
}
func (h *MacAppHandler) ScanApplication(ctx context.Context, app model.Application, r Request) (Candidate, bool, error) {
	if app.Type != model.ApplicationTypeBundle {
		return Candidate{}, false, nil
	}
	c, ok, err := h.candidate(ctx, app.InstallPath, r)
	return c, ok, err
}
func (h *MacAppHandler) candidate(ctx context.Context, path string, r Request) (Candidate, bool, error) {
	info, err := h.inspect(ctx, path)
	if err != nil {
		return Candidate{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return Candidate{}, false, err
	}
	if !h.matches(info) {
		return Candidate{}, false, nil
	}
	if r.Report != nil {
		r.Report(Progress{Stage: model.ScanStageApplication, Subject: info.name})
	}
	id := "app-" + slug(info.name)
	identity := "app-path:" + strings.ToLower(filepath.Clean(path))
	if info.bundleID != "" {
		identity = "app:" + strings.ToLower(info.bundleID)
	}
	app := model.Application{ID: id, Name: info.name, Type: model.ApplicationTypeBundle, Description: info.description, InstallPath: path, Enabled: false, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, Identity: identity, ScanManaged: true}
	if project := h.project(info.bundleID); project != "" {
		app.URL = "https://github.com/" + project
	}
	if info.feed != "" {
		app.Enabled = true
		app.Provider.Type = model.ProviderSparkle
		app.UpdateMode = model.ModeDownload
	}
	if strings.HasPrefix(strings.ToLower(info.bundleID), "com.jetbrains.") {
		if code, ok := h.resolveJetBrainsProduct(info.jetBrainsProductCode); ok {
			app.Enabled = true
			app.Provider.Type = model.ProviderJetBrains
			app.Package = code
			app.UpdateMode = model.ModeCheck
		}
	}
	return Candidate{Application: app, CurrentVersion: info.version, Aliases: []string{"bundle:" + strings.ToLower(info.bundleID)}}, true, nil
}
func inspectMacApp(parent context.Context, path string) (macInfo, error) {
	metadata, err := metadatautil.ReadMacApplicationMetadata(parent, path)
	info := macInfo{path: metadata.Path, name: metadata.Name, bundleID: metadata.BundleID,
		category: metadata.Category, description: metadata.Description,
		version: metadata.Version, feed: metadata.SparkleFeedURL, jetBrainsProductCode: metadata.JetBrainsProductCode}
	if err != nil && parent.Err() != nil {
		return info, parent.Err()
	}
	// Scanner discovery keeps unreadable third-party plist metadata non-fatal;
	// updater capability resolution reports the same utility error explicitly.
	return info, nil
}

// canonicalJetBrainsProduct is the scanner's embedded snapshot of the
// official JetBrains products catalog. product-info's productCode is not the
// releases API key (for example IU -> IIU and PY -> PCP), so unknown codes
// deliberately remain disabled until the catalog can map them uniquely.
func canonicalJetBrainsProduct(productCode string) (string, bool) {
	canonical, ok := map[string]string{
		"IU": "IIU", "PY": "PCP", "PC": "PCP", "IC": "IIC",
		"PS": "PS", "WS": "WS", "GO": "GO", "CL": "CL",
		"DB": "DG", "RD": "RD", "RR": "RR", "RM": "RM",
	}[strings.TrimSpace(productCode)]
	return canonical, ok
}
func (h *MacAppHandler) matches(i macInfo) bool {
	if strings.EqualFold(i.category, "public.app-category.developer-tools") || h.custom[strings.ToLower(i.bundleID)] {
		return true
	}
	b, n := strings.ToLower(strings.TrimSpace(i.bundleID)), strings.ToLower(strings.TrimSpace(i.name))
	for _, d := range h.definitions {
		if d.BundleIDPrefix != "" && strings.HasPrefix(b, strings.ToLower(d.BundleIDPrefix)) {
			return true
		}
		if d.NameContains != "" && strings.Contains(n, strings.ToLower(d.NameContains)) {
			return true
		}
	}
	return false
}
func (h *MacAppHandler) project(bundleID string) string {
	b := strings.ToLower(strings.TrimSpace(bundleID))
	for _, d := range h.definitions {
		if d.GitHubProject != "" && strings.HasPrefix(b, strings.ToLower(d.BundleIDPrefix)) {
			return d.GitHubProject
		}
	}
	return ""
}
func slug(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '-'
	}, s)
}
