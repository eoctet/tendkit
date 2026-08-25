package handler

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type pythonRunner struct {
	calls []string
	run   func(context.Context, string) (runtimeutil.Result, error)
}

func (r *pythonRunner) Run(ctx context.Context, command string, _ map[string]string) (runtimeutil.Result, error) {
	r.calls = append(r.calls, command)
	return r.run(ctx, command)
}

type fixtureFileInfo struct{ dir bool }

func (fixtureFileInfo) Name() string       { return "python3" }
func (fixtureFileInfo) Mode() fs.FileMode  { return 0 }
func (f fixtureFileInfo) IsDir() bool      { return f.dir }
func (fixtureFileInfo) ModTime() time.Time { return time.Time{} }
func (fixtureFileInfo) Size() int64        { return 0 }
func (fixtureFileInfo) Sys() any           { return nil }

func newPython(r Runner, manager string) *PythonHandler {
	handler := NewPython(r)
	handler.lookPath = func(string) (string, error) {
		if manager == "" {
			return "", ErrNotFound
		}
		return manager, nil
	}
	handler.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
	handler.homeDir = func() (string, error) { return "/fixture/home", nil }
	return handler
}

func TestPythonHandlerGoldenCandidate(t *testing.T) {
	manager := "/fixture/python3"
	runner := &pythonRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		switch {
		case strings.Contains(command, "pip list"):
			return runtimeutil.Result{Stdout: `[{"name":"Example_Package","version":"1.2.3"}]`}, nil
		case strings.Contains(command, "pip show"):
			return runtimeutil.Result{Stdout: "Name: Example_Package\nSummary: Fixture package\nHome-page: https://example.invalid\nProject-URL: Source, https://github.com/acme/example.git\n"}, nil
		case strings.Contains(command, "importlib.metadata"):
			return runtimeutil.Result{Stdout: `{"Example_Package":{"path":" /fixture/site-packages ","scope":"user"}}`}, nil
		default:
			t.Fatalf("unexpected command: %s", command)
			return runtimeutil.Result{}, nil
		}
	}}
	handler := newPython(runner, manager)
	var progress []Progress
	result := handler.Scan(context.Background(), Request{Report: func(value Progress) { progress = append(progress, value) }})
	if !result.Complete || result.Err != nil || len(result.Candidates) != 1 {
		t.Fatalf("scan result = %#v", result)
	}
	candidate := result.Candidates[0]
	app := candidate.Application
	if candidate.CurrentVersion != "1.2.3" || app.ID != "pkg-python-example-package" || app.Type != model.ApplicationTypePackage || app.Provider.Type != model.ProviderPyPI || app.Identity != "package:python:examplepackage" {
		t.Fatalf("candidate = %#v", candidate)
	}
	if app.Description != "Fixture package" || app.URL != "https://github.com/acme/example" || app.InstallPath != "/fixture/site-packages" || app.UpdateMode != model.ModeAuto || !strings.Contains(app.Provider.VersionAction(), "importlib.metadata") || !strings.Contains(app.Provider.UpdateAction(), "--user") || !strings.Contains(app.Provider.UpdateAction(), "--upgrade") || !slices.Equal(candidate.Aliases, []string{"python:Example_Package"}) {
		t.Fatalf("golden application = %#v", app)
	}
	if want := []Progress{{model.ScanStagePackageList, "Python"}, {model.ScanStagePackageMetadata, "Python 1/1"}, {model.ScanStagePackagePaths, "Python 1/1"}, {model.ScanStageApplication, "Example_Package"}}; !slices.Equal(progress, want) {
		t.Fatalf("progress = %#v", progress)
	}
	if !strings.Contains(runner.calls[0], "pip list --not-required --format=json") || strings.Contains(app.Provider.VersionAction(), "|") {
		t.Fatalf("unsafe Python commands: %v / %q", runner.calls, app.Provider.VersionAction())
	}
	for _, candidate := range result.Candidates {
		assertActiveProvider(t, candidate.Application.Provider.Type)
	}
}

func TestPythonHandlerManagerFallbackAndMissing(t *testing.T) {
	runner := &pythonRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		if strings.Contains(command, "pip list") {
			return runtimeutil.Result{Stdout: "[]"}, nil
		}
		return runtimeutil.Result{}, nil
	}}
	handler := newPython(runner, "")
	handler.stat = func(path string) (fs.FileInfo, error) {
		if path == "/fixture/python3" {
			return fixtureFileInfo{}, nil
		}
		return nil, ErrNotFound
	}
	result := handler.Scan(context.Background(), Request{Configured: []model.Application{{Name: "Python 3", InstallPath: "/fixture/python3"}}})
	if !result.Complete || result.Err != nil || len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "/fixture/python3") {
		t.Fatalf("configured fallback result=%#v calls=%v", result, runner.calls)
	}
	missing := newPython(runner, "").Scan(context.Background(), Request{})
	if missing.Complete || missing.Err == nil || !strings.Contains(missing.Err.Error(), "python3 not found") {
		t.Fatalf("missing manager result=%#v", missing)
	}
}

func TestPythonHandlerIncompleteAndInvalidListingPaths(t *testing.T) {
	tests := []struct {
		name string
		list runtimeutil.Result
		err  error
		want string
	}{
		{"invalid JSON", runtimeutil.Result{Stdout: "not json"}, nil, "invalid character"},
		{"runner error no stdout", runtimeutil.Result{}, errors.New("list failed"), "list failed"},
		{"nonzero valid stdout", runtimeutil.Result{ExitCode: 1, Stdout: `[{"name":"one","version":"1.0.0"}]`}, nil, "incomplete Python package inventory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &pythonRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
				if strings.Contains(command, "pip list") {
					return test.list, test.err
				}
				if strings.Contains(command, "pip show") {
					return runtimeutil.Result{}, errors.New("optional")
				}
				return runtimeutil.Result{Stdout: `{"one":{"path":"/fixture/one","scope":"system"}}`}, nil
			}}
			result := newPython(runner, "/fixture/python3").Scan(context.Background(), Request{})
			if result.Complete || result.Err == nil || !strings.Contains(result.Err.Error(), test.want) {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

func TestPythonHandlerOptionalMetadataMissingPathsUnknownScopeAndCancellation(t *testing.T) {
	t.Run("metadata failure is optional and unknown scope checks only", func(t *testing.T) {
		runner := &pythonRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			switch {
			case strings.Contains(command, "pip list"):
				return runtimeutil.Result{Stdout: `[{"name":"one","version":"1.0.0"}]`}, nil
			case strings.Contains(command, "pip show"):
				return runtimeutil.Result{}, errors.New("metadata unavailable")
			default:
				return runtimeutil.Result{Stdout: `{"one":{"path":"/fixture/one","scope":"unexpected"}}`}, nil
			}
		}}
		result := newPython(runner, "/fixture/python3").Scan(context.Background(), Request{})
		if !result.Complete || len(result.Candidates) != 1 || result.Candidates[0].Application.UpdateMode != model.ModeCheck || result.Candidates[0].Application.Provider.UpdateAction() != "" {
			t.Fatalf("result=%#v", result)
		}
	})
	t.Run("missing any path is incomplete", func(t *testing.T) {
		runner := &pythonRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			if strings.Contains(command, "pip list") {
				return runtimeutil.Result{Stdout: `[{"name":"one","version":"1"},{"name":"two","version":"2"}]`}, nil
			}
			if strings.Contains(command, "pip show") {
				return runtimeutil.Result{}, nil
			}
			return runtimeutil.Result{Stdout: `{"one":{"path":"/fixture/one","scope":"system"}}`}, nil
		}}
		result := newPython(runner, "/fixture/python3").Scan(context.Background(), Request{})
		if result.Complete || len(result.Candidates) != 1 || result.Err == nil {
			t.Fatalf("result=%#v", result)
		}
	})
	t.Run("cancelled after a partial result is incomplete", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		runner := &pythonRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			if strings.Contains(command, "pip list") {
				return runtimeutil.Result{Stdout: `[{"name":"one","version":"1"},{"name":"two","version":"2"}]`}, nil
			}
			if strings.Contains(command, "pip show") {
				return runtimeutil.Result{}, nil
			}
			return runtimeutil.Result{Stdout: `{"one":{"path":"/fixture/one","scope":"system"},"two":{"path":"/fixture/two","scope":"system"}}`}, nil
		}}
		result := newPython(runner, "/fixture/python3").Scan(ctx, Request{Report: func(value Progress) {
			if value.Stage == model.ScanStageApplication && value.Subject == "two" {
				cancel()
			}
		}})
		if result.Complete || !errors.Is(result.Err, context.Canceled) || len(result.Candidates) != 1 || result.Candidates[0].Application.Name != "one" {
			t.Fatalf("result=%#v", result)
		}
	})
}

func TestPythonHandlerChunksFiftyPackagesAndParsesGitHubShow(t *testing.T) {
	names := make([]string, 51)
	items := make([]string, 51)
	for i := range names {
		names[i] = fmt.Sprintf("pkg-%02d", i)
		items[i] = fmt.Sprintf(`{"name":%q,"version":"1.0.0"}`, names[i])
	}
	runner := &pythonRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		switch {
		case strings.Contains(command, "pip list"):
			return runtimeutil.Result{Stdout: "[" + strings.Join(items, ",") + "]"}, nil
		case strings.Contains(command, "pip show"):
			return runtimeutil.Result{}, nil
		default:
			values := make([]string, 0, 51)
			for _, name := range names {
				if strings.Contains(command, name) {
					values = append(values, fmt.Sprintf(`%q:{"path":"/fixture/%s","scope":"system"}`, name, name))
				}
			}
			return runtimeutil.Result{Stdout: "{" + strings.Join(values, ",") + "}"}, nil
		}
	}}
	result := newPython(runner, "/fixture/python3").Scan(context.Background(), Request{})
	if !result.Complete || len(result.Candidates) != 51 {
		t.Fatalf("result=%#v", result)
	}
	show, paths := 0, 0
	for _, command := range runner.calls {
		if strings.Contains(command, "pip show") {
			show++
		}
		if strings.Contains(command, "importlib.metadata") {
			paths++
		}
	}
	if show != 2 || paths != 2 {
		t.Fatalf("chunk calls show=%d paths=%d calls=%v", show, paths, runner.calls)
	}
	metadata := parsePipShow("Name: Example_Package\nSummary: fixture\nProject-URL: Source, https://github.com/acme/example.git\n")
	if got := metadata["examplepackage"]; got.Description != "fixture" || got.URL != "https://github.com/acme/example" {
		t.Fatalf("metadata=%#v", metadata)
	}
}
