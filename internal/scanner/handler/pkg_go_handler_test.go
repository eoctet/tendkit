package handler

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type goRunner struct {
	run func(context.Context, string) (runtimeutil.Result, error)
}

func (r goRunner) Run(ctx context.Context, command string, _ map[string]string) (runtimeutil.Result, error) {
	return r.run(ctx, command)
}

func newGo(r Runner, manager string) *GoHandler {
	h := NewGo(r)
	h.lookPath = func(string) (string, error) {
		if manager == "" {
			return "", ErrNotFound
		}
		return manager, nil
	}
	h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
	h.readDir = func(string) ([]fs.DirEntry, error) { return nil, fs.ErrNotExist }
	return h
}

func TestGoHandlerEnvNonzeroHasWarning(t *testing.T) {
	h := newGo(goRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
		return runtimeutil.Result{ExitCode: 2}, nil
	}}, "/fixture/go")
	result := h.Scan(context.Background(), Request{})
	if result.Complete || result.Err == nil {
		t.Fatalf("nonzero go env result=%#v", result)
	}
}

func TestGoHandlerGoldenInventoryAndHelpers(t *testing.T) {
	r := goRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		switch {
		case strings.Contains(command, " env GOPATH GOBIN"):
			return runtimeutil.Result{Stdout: "/one:/two\n/custom/bin\n"}, nil
		case strings.Contains(command, " version -m "):
			return runtimeutil.Result{Stdout: "path github.com/acme/tool/cmd/tool\nmod github.com/acme/tool v1.2.3\n"}, nil
		}
		return runtimeutil.Result{}, errors.New("unexpected")
	}}
	h := newGo(r, "/fixture/go")
	h.readDir = func(dir string) ([]fs.DirEntry, error) {
		if dir == "/custom/bin" {
			return fixtureEntries(fixtureDirEntry{name: "tool"}), nil
		}
		return nil, fs.ErrNotExist
	}
	var p []Progress
	result := h.Scan(context.Background(), Request{Report: func(v Progress) { p = append(p, v) }})
	if !result.Complete || len(result.Candidates) != 1 {
		t.Fatalf("result=%#v", result)
	}
	c := result.Candidates[0]
	if c.Application.ID != "pkg-go-tool" || c.Application.Identity != "package:go:tool" || c.Application.Provider.Type != model.ProviderDefault || c.CurrentVersion != "1.2.3" || c.Application.URL != "https://github.com/acme/tool" || len(c.Aliases) != 1 {
		t.Fatalf("candidate=%#v", c)
	}
	if strings.Contains(c.Application.Provider.VersionAction(), "exit") || !strings.Contains(c.Application.Provider.VersionAction(), "found=1") {
		t.Fatalf("unsafe Go version action %q", c.Application.Provider.VersionAction())
	}
	if len(p) != 2 || p[0] != (Progress{Stage: model.ScanStagePackagePaths, Subject: "Go"}) || p[1].Stage != model.ScanStageApplication {
		t.Fatalf("progress=%#v", p)
	}
	if got := goBinDirs("/one:/two\n/custom/bin\n"); strings.Join(got, ",") != "/one/bin,/two/bin,/custom/bin" {
		t.Fatalf("dirs=%v", got)
	}
	if cmd, mod, v := goModule("path x/y\nmod github.com/a/b v1.0.0\n"); cmd != "x/y" || mod != "github.com/a/b" || v != "v1.0.0" {
		t.Fatal(cmd, mod, v)
	}
	if goGitHubURL("example.com/a/b") != "" {
		t.Fatal("non github accepted")
	}
	for _, candidate := range result.Candidates {
		assertActiveProvider(t, candidate.Application.Provider.Type)
	}
}

func TestGoHandlerManagerAndPartialInventory(t *testing.T) {
	r := goRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		if strings.Contains(command, " env GOPATH GOBIN") {
			return runtimeutil.Result{Stdout: "/one\n\n"}, nil
		}
		if strings.Contains(command, " version -m ") {
			return runtimeutil.Result{}, errors.New("metadata failed")
		}
		return runtimeutil.Result{}, nil
	}}
	h := newGo(r, "")
	h.homeDir = func() (string, error) { return "/fixture/home", nil }
	h.stat = func(path string) (fs.FileInfo, error) {
		if path == "/fixture/home/bin/go" {
			return fixtureFileInfo{}, nil
		}
		return nil, ErrNotFound
	}
	h.readDir = func(string) ([]fs.DirEntry, error) {
		return fixtureEntries(fixtureDirEntry{name: "tool"}, fixtureDirEntry{name: "sub", dir: true}), nil
	}
	result := h.Scan(context.Background(), Request{Configured: []model.Application{{Name: "gO", InstallPath: "~/bin/go"}}})
	if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
		t.Fatalf("partial=%#v", result)
	}
	h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{dir: true}, nil }
	missing := h.Scan(context.Background(), Request{Configured: []model.Application{{ID: "GO", InstallPath: "/fixture/go"}}})
	if missing.Complete || missing.Err == nil {
		t.Fatalf("missing=%#v", missing)
	}
}
