package handler

import (
	"context"
	"os/exec"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	"github.com/eoctet/tendkit/pkg/version"
)

type PathHandler struct {
	runner      Runner
	definitions []builtin.PathDefinition
	lookPath    func(string) (string, error)
}

func NewPath(r Runner, definitions []builtin.PathDefinition) *PathHandler {
	return &PathHandler{runner: r, definitions: append([]builtin.PathDefinition(nil), definitions...), lookPath: exec.LookPath}
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
		if c, ok := h.discover(ctx, d); ok {
			result.Candidates = append(result.Candidates, c)
		}
	}
	return result
}
func (h *PathHandler) ScanApplication(ctx context.Context, app model.Application, request Request) (Candidate, bool, error) {
	if app.Type != model.ApplicationTypeCLI && app.Type != model.ApplicationTypeSDK {
		return Candidate{}, false, nil
	}
	for _, d := range h.definitions {
		if pathApplicationID(d.ID) == app.ID || strings.EqualFold(d.Name, app.Name) || (d.Package != "" && strings.EqualFold(d.Package, app.Package)) {
			c, ok := h.discover(ctx, d)
			return c, ok, ctx.Err()
		}
	}
	return Candidate{}, false, ErrNotFound
}
func (h *PathHandler) discover(ctx context.Context, d builtin.PathDefinition) (Candidate, bool) {
	path, err := h.lookPath(d.Binary)
	if err != nil {
		return Candidate{}, false
	}
	desc := d.Description
	actions := &model.ProviderActions{Version: d.VersionCommand, Check: d.CheckCommand, Update: d.UpdateCommand}
	if d.DownloadURL != "" {
		actions.Download = &model.Download{URL: d.DownloadURL, Filename: d.DownloadFilename}
	}
	enabled := d.Provider != model.ProviderDefault || strings.TrimSpace(d.CheckCommand) != ""
	app := model.Application{ID: pathApplicationID(d.ID), Name: d.Name, Type: model.ApplicationTypeCLI, Description: desc, URL: d.URL, InstallPath: path, Enabled: enabled, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: d.Provider, Actions: actions}, Package: d.Package, ScanManaged: true}
	if d.DownloadURL != "" {
		app.UpdateMode = model.ModeDownload
	} else if d.UpdateCommand != "" && !h.probe(ctx, d.UpdateProbe) {
		actions.Update = ""
	} else if d.UpdateCommand != "" {
		app.UpdateMode = model.ModeAuto
	}
	app.Identity = identity(app)
	c := Candidate{Application: app}
	r, e := h.runner.Run(ctx, d.VersionCommand, nil)
	if e != nil {
		c.ObservationErr = e
	} else if r.ExitCode != 0 {
		c.ObservationErr = CommandExitError{ExitCode: r.ExitCode}
	} else {
		c.CurrentVersion, e = version.Extract(r.Combined())
		c.ObservationErr = e
	}
	return c, true
}

func pathApplicationID(id string) string {
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
	if app.Package != "" {
		switch app.Provider.Type {
		case model.ProviderNPM:
			return model.PackageIdentity("node", app.Package)
		case model.ProviderPyPI:
			return model.PackageIdentity("python", app.Package)
		case model.ProviderGo:
			return model.PackageIdentity("go", app.Package)
		}
	}
	return "cli:" + model.NormalizeIdentityName(app.Name)
}
