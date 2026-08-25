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

type uvRunner struct {
	run func(context.Context, string) (runtimeutil.Result, error)
}

func (r uvRunner) Run(c context.Context, s string, _ map[string]string) (runtimeutil.Result, error) {
	return r.run(c, s)
}
func newUV(r Runner, uv string) *UVHandler {
	h := NewUV(r)
	h.lookPath = func(string) (string, error) {
		if uv == "" {
			return "", ErrNotFound
		}
		return uv, nil
	}
	h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
	h.homeDir = func() (string, error) { return "/home", nil }
	return h
}

func TestUVHandlerGoldenMetadataAndFallback(t *testing.T) {
	uv := "/fixture/uv"
	r := uvRunner{run: func(_ context.Context, s string) (runtimeutil.Result, error) {
		switch {
		case strings.Contains(s, "tool list --show-paths"):
			return runtimeutil.Result{Stdout: "tool-name v1.2.3-rc.1 (/tools/tool-name)\n  - tool-name (/fixture/tool)\n"}, nil
		case strings.Contains(s, "tool dir"):
			return runtimeutil.Result{Stdout: "/tools\n"}, nil
		case strings.Contains(s, "pip show"):
			return runtimeutil.Result{Stdout: "Name: tool-name\nSummary: fixture\nProject-URL: Source, https://github.com/acme/tool.git\n"}, nil
		}
		return runtimeutil.Result{}, errors.New("unexpected")
	}}
	h := newUV(r, uv)
	h.stat = func(p string) (fs.FileInfo, error) {
		if p == "/tools/tool-name/bin/python3" {
			return fixtureFileInfo{}, nil
		}
		return nil, ErrNotFound
	}
	var progress []Progress
	result := h.Scan(context.Background(), Request{Report: func(p Progress) { progress = append(progress, p) }})
	if !result.Complete || len(result.Candidates) != 1 {
		t.Fatalf("result=%#v", result)
	}
	c := result.Candidates[0]
	if c.CurrentVersion != "1.2.3-rc.1" || c.Application.ID != "pkg-uv-tool-name" || c.Application.Identity != "package:uv:tool-name" || c.Application.Provider.Type != model.ProviderUV || c.Application.URL != "https://github.com/acme/tool" || c.Application.InstallPath != "/fixture/tool" || len(c.Aliases) != 1 || c.Aliases[0] != "uv:tool-name" {
		t.Fatalf("candidate=%#v", c)
	}
	if len(progress) != 3 || progress[0].Stage != model.ScanStagePackageList || progress[1].Stage != model.ScanStageApplication || progress[2].Stage != model.ScanStagePackageMetadata {
		t.Fatalf("progress=%#v", progress)
	}
	if !uvAppLine.MatchString("x v1.2.3-beta.1 (/tools/x)") {
		t.Fatal("uv helpers")
	}
	if c.Application.Provider.Actions != nil {
		t.Fatalf("UV scanner actions = %#v", c.Application.Provider.Actions)
	}
	for _, candidate := range result.Candidates {
		assertActiveProvider(t, candidate.Application.Provider.Type)
	}
}

func TestUVHandlerInventoryFailuresAndManager(t *testing.T) {
	for _, tc := range []struct {
		out string
		err error
	}{{"bad", nil}, {"", errors.New("list failed")}, {"tool name is invalid", nil}} {
		r := uvRunner{run: func(_ context.Context, s string) (runtimeutil.Result, error) {
			if strings.Contains(s, "show-paths") {
				return runtimeutil.Result{Stdout: tc.out}, tc.err
			}
			return runtimeutil.Result{}, nil
		}}
		result := newUV(r, "/uv").Scan(context.Background(), Request{})
		if result.Complete || result.Err == nil {
			t.Fatalf("result=%#v", result)
		}
	}
	empty := newUV(uvRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) { return runtimeutil.Result{}, nil }}, "/uv").Scan(context.Background(), Request{})
	if !empty.Complete {
		t.Fatalf("empty=%#v", empty)
	}
	h := newUV(uvRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) { return runtimeutil.Result{}, nil }}, "")
	h.stat = func(p string) (fs.FileInfo, error) {
		if p == "/home/bin/uv" {
			return fixtureFileInfo{}, nil
		}
		return nil, ErrNotFound
	}
	if result := h.Scan(context.Background(), Request{Configured: []model.Application{{Name: "UV", InstallPath: "~/bin/uv"}}}); !result.Complete {
		t.Fatalf("fallback=%#v", result)
	}
	h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{dir: true}, nil }
	if result := h.Scan(context.Background(), Request{Configured: []model.Application{{ID: "uv", InstallPath: "/uv"}}}); result.Complete || result.Err == nil {
		t.Fatalf("missing=%#v", result)
	}
}
