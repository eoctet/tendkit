package handler

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type goRunner struct {
	run func(context.Context, string) (runtimeutil.Result, error)
}

type goSymlinkEntry struct{ name string }

func (e goSymlinkEntry) Name() string             { return e.name }
func (goSymlinkEntry) IsDir() bool                { return false }
func (goSymlinkEntry) Type() fs.FileMode          { return fs.ModeSymlink }
func (goSymlinkEntry) Info() (fs.FileInfo, error) { return fixtureFileInfo{}, nil }

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
	if c.Evidence == nil || c.Evidence.Source != "go" || !slices.Equal(c.Evidence.ExecutablePaths, []string{"/custom/bin/tool"}) {
		t.Fatalf("evidence=%#v", c.Evidence)
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
	if !result.Complete || result.Err != nil || len(result.Candidates) != 0 {
		t.Fatalf("unconfirmed binary=%#v", result)
	}
	h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{dir: true}, nil }
	missing := h.Scan(context.Background(), Request{Configured: []model.Application{{ID: "GO", InstallPath: "/fixture/go"}}})
	if missing.Complete || missing.Err == nil {
		t.Fatalf("missing=%#v", missing)
	}
}

func TestGoHandlerIgnoresUnconfirmedFilesButProtectsKnownManagedOwner(t *testing.T) {
	r := goRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		if strings.Contains(command, " env GOPATH GOBIN") {
			return runtimeutil.Result{Stdout: "/gopath\n/gobin\n"}, nil
		}
		return runtimeutil.Result{}, errors.New("no build info")
	}}
	h := newGo(r, "/fixture/go")
	h.readDir = func(dir string) ([]fs.DirEntry, error) {
		if dir == "/gobin" {
			return []fs.DirEntry{
				fixtureDirEntry{name: "non-executable"},
				goSymlinkEntry{name: "linked"},
				fixtureDirEntry{name: "unrelated"},
			}, nil
		}
		return nil, fs.ErrNotExist
	}
	h.stat = func(path string) (fs.FileInfo, error) {
		if path == "/gobin/non-executable" {
			return fixtureFileInfo{mode: 0o644}, nil
		}
		return fixtureFileInfo{}, nil
	}
	if result := h.Scan(context.Background(), Request{}); !result.Complete || len(result.Candidates) != 0 {
		t.Fatalf("unrelated files made Go incomplete: %#v", result)
	}
	configured := model.Application{Identity: "package:go:example.invalid/known", InstallPath: "/gobin/unrelated", ScanManaged: true}
	if result := h.Scan(context.Background(), Request{Configured: []model.Application{configured}}); result.Complete || result.Err == nil || len(result.Candidates) != 0 {
		t.Fatalf("known Go owner was not protected: %#v", result)
	}
}

func TestGoHandlerProtectsConfiguredSymlinkTargetWhenMetadataFails(t *testing.T) {
	r := goRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		if strings.Contains(command, " env GOPATH GOBIN") {
			return runtimeutil.Result{Stdout: "/gopath\n/gobin\n"}, nil
		}
		return runtimeutil.Result{}, errors.New("metadata failed")
	}}
	h := newGo(r, "/fixture/go")
	h.readDir = func(dir string) ([]fs.DirEntry, error) {
		if dir == "/gobin" {
			return fixtureEntries(fixtureDirEntry{name: "tool"}), nil
		}
		return nil, fs.ErrNotExist
	}
	h.evalSymlinks = func(path string) (string, error) {
		if path == "/path-link/tool" || path == "/gobin/tool" {
			return "/real/tool", nil
		}
		return "", ErrNotFound
	}
	configured := model.Application{Identity: "package:go:tool", InstallPath: "/path-link/tool", ScanManaged: true}
	if result := h.Scan(context.Background(), Request{Configured: []model.Application{configured}}); result.Complete || result.Err == nil {
		t.Fatalf("configured symlink target was not protected: %#v", result)
	}
}

func TestGoHandlerIgnoresUnrelatedSymlinkButUsesValidSymlinkEvidence(t *testing.T) {
	r := goRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		if strings.Contains(command, " env GOPATH GOBIN") {
			return runtimeutil.Result{Stdout: "/gopath\n/gobin\n"}, nil
		}
		if strings.Contains(command, " version -m ") {
			return runtimeutil.Result{Stdout: "path example.invalid/tool\nmod example.invalid/tool v1.0.0\n"}, nil
		}
		return runtimeutil.Result{}, errors.New("unexpected")
	}}
	h := newGo(r, "/fixture/go")
	h.readDir = func(dir string) ([]fs.DirEntry, error) {
		if dir == "/gobin" {
			return []fs.DirEntry{goSymlinkEntry{name: "tool"}, goSymlinkEntry{name: "unrelated"}}, nil
		}
		return nil, fs.ErrNotExist
	}
	h.stat = func(path string) (fs.FileInfo, error) {
		if path == "/gobin/unrelated" {
			return nil, ErrNotFound
		}
		return fixtureFileInfo{}, nil
	}
	result := h.Scan(context.Background(), Request{})
	if !result.Complete || len(result.Candidates) != 1 || result.Candidates[0].Evidence == nil || !slices.Equal(result.Candidates[0].Evidence.ExecutablePaths, []string{"/gobin/tool"}) {
		t.Fatalf("valid symlink evidence / unrelated link handling failed: %#v", result)
	}
}

func TestGoHandlerDuplicateConfirmedCommandIsIncomplete(t *testing.T) {
	r := goRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		if strings.Contains(command, " env GOPATH GOBIN") {
			return runtimeutil.Result{Stdout: "/gopath\n/gobin\n"}, nil
		}
		return runtimeutil.Result{Stdout: "path example.invalid/tool\nmod example.invalid/tool v1.0.0\n"}, nil
	}}
	h := newGo(r, "/fixture/go")
	h.readDir = func(dir string) ([]fs.DirEntry, error) {
		if dir == "/gopath/bin" || dir == "/gobin" {
			return fixtureEntries(fixtureDirEntry{name: "tool"}), nil
		}
		return nil, fs.ErrNotExist
	}
	result := h.Scan(context.Background(), Request{})
	if result.Complete || result.Err == nil || len(result.Candidates) != 1 {
		t.Fatalf("duplicate command was not incomplete: %#v", result)
	}
}
