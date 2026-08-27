package handler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

type GoHandler struct {
	runner       Runner
	lookPath     func(string) (string, error)
	stat         func(string) (os.FileInfo, error)
	evalSymlinks func(string) (string, error)
	readDir      func(string) ([]os.DirEntry, error)
	homeDir      func() (string, error)
}

func NewGo(r Runner) *GoHandler {
	return &GoHandler{runner: r, lookPath: exec.LookPath, stat: os.Stat, evalSymlinks: filepath.EvalSymlinks, readDir: os.ReadDir, homeDir: os.UserHomeDir}
}
func (*GoHandler) Domain() Domain { return Go }
func (h *GoHandler) Scan(ctx context.Context, r Request) Result {
	goBin := h.manager(r.Configured)
	if goBin == "" {
		return Result{Complete: false, Err: &PackageManagerUnavailableError{Manager: "go"}}
	}
	report := func(stage, subject string) {
		if r.Report != nil {
			r.Report(Progress{Stage: stage, Subject: subject})
		}
	}
	report(model.ScanStagePackagePaths, "Go")
	env, err := h.runner.Run(ctx, runtimeutil.QuoteShell(goBin)+" env GOPATH GOBIN", nil)
	if err != nil || env.ExitCode != 0 {
		if err == nil {
			err = fmt.Errorf("go env exited with code %d", env.ExitCode)
		}
		return Result{Complete: false, Err: err}
	}
	dirs := goBinDirs(env.Stdout)
	out := Result{Complete: true}
	seen := map[string]bool{}
	for _, dir := range dirs {
		entries, e := h.readDir(dir)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			out.Complete = false
			out.Err = e
			continue
		}
		for _, entry := range entries {
			if e := ctx.Err(); e != nil {
				out.Complete = false
				out.Err = e
				return out
			}
			binary := filepath.Join(dir, entry.Name())
			protected := h.goManagedOwnerAtPath(r.Configured, binary)
			// Stat follows valid symlinks, so a linked Go command can provide build
			// evidence. Broken and non-executable unrelated entries remain ignored.
			info, infoErr := h.stat(binary)
			if infoErr != nil || !validEvidenceFile(info) {
				if protected {
					out.Complete = false
				}
				continue
			}
			report(model.ScanStageApplication, entry.Name())
			if e := ctx.Err(); e != nil {
				out.Complete = false
				out.Err = e
				return out
			}
			meta, e := h.runner.Run(ctx, runtimeutil.QuoteShell(goBin)+" version -m "+runtimeutil.QuoteShell(binary), nil)
			if ectx := ctx.Err(); ectx != nil {
				out.Complete = false
				out.Err = ectx
				return out
			}
			if e != nil || meta.ExitCode != 0 {
				if protected {
					out.Complete = false
				}
				continue
			}
			command, module, current := goModule(meta.Stdout)
			if module == "" || command == "" {
				if protected {
					out.Complete = false
				}
				continue
			}
			key := strings.ToLower(command)
			if seen[key] {
				out.Complete = false
				continue
			}
			seen[key] = true
			target := metadatautil.PackageTarget{Ecosystem: metadatautil.PackageGo, Manager: goBin, Name: command, InstallPath: binary}
			versionCommand, _ := metadatautil.PackageVersionCommand(target)
			updateCommand, _ := metadatautil.PackageUpdateCommand(target)
			app := model.Application{ID: "pkg-go-" + packageSlug(entry.Name()), Name: entry.Name(), Type: model.ApplicationTypePackage, Description: "Go command provided by " + module, URL: goGitHubURL(module), InstallPath: binary, Enabled: true, UpdateMode: model.ModeAuto, Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{Version: versionCommand, Check: runtimeutil.QuoteShell(goBin) + " list -m -f '{{.Version}}' " + runtimeutil.QuoteShell(module+"@latest"), Update: updateCommand}}, Package: command, Identity: model.PackageIdentity(string(h.Domain()), command), ScanManaged: true}
			out.Candidates = append(out.Candidates, Candidate{Application: app, CurrentVersion: version.Normalize(current), Aliases: []string{"go:" + module}, Evidence: &InstallationEvidence{Source: string(h.Domain()), Package: command, ExecutablePaths: []string{binary}, InstallRoot: dir}})
		}
	}
	if !out.Complete && out.Err == nil {
		out.Err = &PackageInventoryIncompleteError{Ecosystem: "Go", Message: "incomplete Go inventory"}
	}
	return out
}

func (h *GoHandler) goManagedOwnerAtPath(configured []model.Application, binary string) bool {
	binary = filepath.Clean(binary)
	for _, app := range configured {
		if !app.ScanManaged || !strings.HasPrefix(app.Identity, "package:go:") || strings.TrimSpace(app.InstallPath) == "" {
			continue
		}
		installPath := filepath.Clean(app.InstallPath)
		if installPath == binary {
			return true
		}
		canonicalInstallPath, installErr := h.evalSymlinks(installPath)
		canonicalBinary, binaryErr := h.evalSymlinks(binary)
		if installErr == nil && binaryErr == nil && filepath.Clean(canonicalInstallPath) == filepath.Clean(canonicalBinary) {
			return true
		}
	}
	return false
}
func (h *GoHandler) manager(c []model.Application) string {
	if p, e := h.lookPath("go"); e == nil {
		return p
	}
	for _, a := range c {
		if strings.EqualFold(a.ID, "go") || strings.EqualFold(a.Name, "Go") {
			path := expandConfiguredPath(a.InstallPath, h.homeDir)
			if info, e := h.stat(path); e == nil && !info.IsDir() {
				return path
			}
		}
	}
	return ""
}
func goBinDirs(out string) []string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dirs := []string{}
	if len(lines) > 0 {
		for _, p := range filepath.SplitList(strings.TrimSpace(lines[0])) {
			if p != "" {
				dirs = append(dirs, filepath.Join(p, "bin"))
			}
		}
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		dirs = append(dirs, strings.TrimSpace(lines[1]))
	}
	seen := map[string]bool{}
	result := []string{}
	for _, d := range dirs {
		if d != "" && !seen[d] {
			seen[d] = true
			result = append(result, d)
		}
	}
	return result
}
func goModule(out string) (string, string, string) {
	command := ""
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "path" {
			command = f[1]
		}
		if len(f) >= 3 && f[0] == "mod" {
			return command, f[1], f[2]
		}
	}
	return "", "", ""
}
func goGitHubURL(value string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "github.com/") {
		return ""
	}
	return githubProjectURL(value)
}
