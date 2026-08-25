package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type nodeRunner struct {
	calls []string
	envs  []map[string]string
	run   func(context.Context, string) (runtimeutil.Result, error)
}

func (r *nodeRunner) Run(ctx context.Context, command string, env map[string]string) (runtimeutil.Result, error) {
	r.calls, r.envs = append(r.calls, command), append(r.envs, env)
	return r.run(ctx, command)
}

func newNode(r Runner, npm string) *NodeHandler {
	h := NewNode(r)
	h.lookPath = func(string) (string, error) {
		if npm == "" {
			return "", ErrNotFound
		}
		return npm, nil
	}
	h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
	h.env = func() []string { return []string{"PATH=/inherited/bin", "OTHER=value"} }
	return h
}

func TestNodeHandlerGoldenCandidateAndEnvironment(t *testing.T) {
	npm := "/fixture/node/bin/npm"
	r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		switch {
		case strings.Contains(command, " list -g "):
			return runtimeutil.Result{Stdout: `{"dependencies":{"@scope/name":{"version":"v1.2.3","path":"/fixture/modules/@scope/name"}}}`}, nil
		case strings.Contains(command, " root -g"):
			return runtimeutil.Result{Stdout: "/fixture/modules\n"}, nil
		case strings.Contains(command, " view "):
			return runtimeutil.Result{Stdout: `{"description":"fixture","repository.url":"https://github.com/acme/name.git","homepage":"https://example.invalid"}`}, nil
		}
		return runtimeutil.Result{}, errors.New("unexpected command")
	}}
	h := newNode(r, npm)
	h.readFile = func(path string) ([]byte, error) {
		if path != "/fixture/modules/@scope/name/package.json" {
			return nil, os.ErrNotExist
		}
		return []byte(`{"description":"fixture","repository":{"url":"https://github.com/acme/name.git"},"homepage":"https://example.invalid"}`), nil
	}
	configured := []model.Application{{ID: "node", InstallPath: "/fixture/node/bin/node"}}
	var progress []Progress
	result := h.Scan(context.Background(), Request{Configured: configured, Report: func(p Progress) { progress = append(progress, p) }})
	if !result.Complete || result.Err != nil || len(result.Candidates) != 1 {
		t.Fatalf("result=%#v", result)
	}
	c := result.Candidates[0]
	if c.CurrentVersion != "1.2.3" || c.Application.ID != "pkg-node-scope-name" || c.Application.Identity != "package:node:scope-name" || c.Application.Provider.Type != model.ProviderNPM || c.Application.URL != "https://github.com/acme/name" || c.Application.Description != "fixture" || c.Application.Provider.VersionAction() == "" || c.Application.Provider.UpdateAction() == "" || len(c.Aliases) != 1 || c.Aliases[0] != "node:@scope/name" {
		t.Fatalf("candidate=%#v", c)
	}
	if len(progress) != 2 || progress[0] != (Progress{Stage: model.ScanStagePackageList, Subject: "Node.js"}) || progress[1] != (Progress{Stage: model.ScanStageApplication, Subject: "@scope/name"}) {
		t.Fatalf("progress=%#v", progress)
	}
	for _, env := range r.envs {
		if env["PATH"] != strings.Join([]string{filepath.Dir(npm), "/inherited/bin"}, string(os.PathListSeparator)) {
			t.Fatalf("PATH=%q", env["PATH"])
		}
	}
	for _, candidate := range result.Candidates {
		assertActiveProvider(t, candidate.Application.Provider.Type)
	}
}

func TestNodeHandlerListingFailuresRootFallbackMetadataAndCancellation(t *testing.T) {
	t.Run("invalid and empty-error listings fail", func(t *testing.T) {
		for _, response := range []struct {
			out string
			err error
		}{{"not json", nil}, {"", errors.New("list failed")}} {
			r := &nodeRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
				return runtimeutil.Result{Stdout: response.out}, response.err
			}}
			result := newNode(r, "/fixture/npm").Scan(context.Background(), Request{})
			if result.Complete || result.Err == nil {
				t.Fatalf("result=%#v", result)
			}
		}
	})
	t.Run("root fallback and optional metadata", func(t *testing.T) {
		r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			switch {
			case strings.Contains(command, " list -g "):
				return runtimeutil.Result{Stdout: `{"dependencies":{"one":{"version":"1.0"}}}`}, nil
			case strings.Contains(command, " root -g"):
				return runtimeutil.Result{Stdout: "/fixture/modules\n"}, nil
			default:
				return runtimeutil.Result{}, errors.New("metadata optional")
			}
		}}
		result := newNode(r, "/fixture/npm").Scan(context.Background(), Request{})
		if !result.Complete || len(result.Candidates) != 1 || result.Candidates[0].Application.InstallPath != "/fixture/modules/one" || result.Candidates[0].Application.URL != "" {
			t.Fatalf("result=%#v", result)
		}
	})
	t.Run("nonzero partial and progress cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			switch {
			case strings.Contains(command, " list -g "):
				return runtimeutil.Result{ExitCode: 1, Stdout: `{"dependencies":{"one":{"version":"1","path":"/one"},"two":{"version":"2","path":"/two"}}}`}, nil
			case strings.Contains(command, " root -g"):
				return runtimeutil.Result{}, nil
			default:
				return runtimeutil.Result{}, errors.New("optional")
			}
		}}
		applications := 0
		result := newNode(r, "/fixture/npm").Scan(ctx, Request{Report: func(p Progress) {
			if p.Stage == model.ScanStageApplication {
				applications++
				if applications == 2 {
					cancel()
				}
			}
		}})
		if result.Complete || !errors.Is(result.Err, context.Canceled) || len(result.Candidates) != 1 {
			t.Fatalf("result=%#v", result)
		}
	})
}

func TestNodeHandlerFallbackMissingAndPartialInventory(t *testing.T) {
	r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		if strings.Contains(command, " list -g ") {
			return runtimeutil.Result{ExitCode: 1, Stdout: `{"dependencies":{"one":{"version":"1.0","path":""}}}`}, nil
		}
		if strings.Contains(command, " root -g") {
			return runtimeutil.Result{}, errors.New("optional")
		}
		return runtimeutil.Result{}, errors.New("optional")
	}}
	h := newNode(r, "")
	h.stat = func(path string) (fs.FileInfo, error) {
		if path == "/fixture/npm" {
			return fixtureFileInfo{}, nil
		}
		return nil, ErrNotFound
	}
	result := h.Scan(context.Background(), Request{Configured: []model.Application{{Name: "npm", InstallPath: "/fixture/npm"}}})
	if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
		t.Fatalf("fallback partial=%#v", result)
	}
	h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{dir: true}, nil }
	missing := h.Scan(context.Background(), Request{Configured: []model.Application{{ID: "npm", InstallPath: "/fixture/npm"}}})
	if missing.Complete || missing.Err == nil {
		t.Fatalf("missing=%#v", missing)
	}
}

func TestNodeHandlerFortyPackagesUsesBoundedCommandsAndLocalMetadata(t *testing.T) {
	const packageCount = 40
	dependencies := make(map[string]map[string]string, packageCount)
	for index := 0; index < packageCount; index++ {
		name := fmt.Sprintf("package-%02d", index)
		dependencies[name] = map[string]string{
			"version": "1.0.0",
			"path":    "/fixture/modules/" + name,
		}
	}
	listing, err := json.Marshal(map[string]any{"dependencies": dependencies})
	if err != nil {
		t.Fatal(err)
	}
	r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		switch {
		case strings.Contains(command, " list -g "):
			return runtimeutil.Result{Stdout: string(listing)}, nil
		case strings.Contains(command, " root -g"):
			return runtimeutil.Result{Stdout: "/fixture/modules\n"}, nil
		default:
			return runtimeutil.Result{}, errors.New("unexpected external metadata command")
		}
	}}
	h := newNode(r, "/fixture/npm")
	started := time.Now()
	result := h.Scan(context.Background(), Request{})
	elapsed := time.Since(started)
	t.Logf("40-package Node fixture: commands=%d elapsed=%s", len(r.calls), elapsed)
	if !result.Complete || len(result.Candidates) != packageCount {
		t.Fatalf("result=%#v", result)
	}
	if len(r.calls) > 10 {
		t.Fatalf("40-package Node fixture used %d commands; want at most 10", len(r.calls))
	}
}
