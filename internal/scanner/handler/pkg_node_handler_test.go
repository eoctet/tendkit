package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
		case strings.Contains(command, " prefix -g"):
			return runtimeutil.Result{Stdout: "/fixture/prefix\n"}, nil
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

func TestNodeHandlerProducesEvidenceForVerifiedGlobalBin(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "prefix")
	packagePath := filepath.Join(prefix, "lib", "node_modules", "tool")
	target := filepath.Join(packagePath, "bin", "tool.js")
	link := filepath.Join(prefix, "bin", "tool")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "package.json"), []byte(`{"bin":"bin/tool.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		switch {
		case strings.Contains(command, " list -g "):
			return runtimeutil.Result{Stdout: fmt.Sprintf(`{"dependencies":{"tool":{"version":"1.0.0","path":%q}}}`, packagePath)}, nil
		case strings.Contains(command, " root -g"):
			return runtimeutil.Result{Stdout: filepath.Dir(packagePath)}, nil
		case strings.Contains(command, " prefix -g"):
			return runtimeutil.Result{Stdout: prefix}, nil
		}
		return runtimeutil.Result{}, errors.New("unexpected")
	}}
	h := newNode(r, filepath.Join(dir, "npm"))
	h.evalSymlinks = func(path string) (string, error) {
		if path == link {
			return target, nil
		}
		return path, nil
	}
	if paths, ok := h.binEvidence(packagePath, prefix, "tool", map[string]string{"tool": "bin/tool.js"}); !ok || !slices.Equal(paths, []string{link}) {
		t.Fatalf("direct evidence paths=%v ok=%v", paths, ok)
	}
	result := h.Scan(context.Background(), Request{})
	if !result.Complete || len(result.Candidates) != 1 || result.Candidates[0].Evidence == nil || !slices.Equal(result.Candidates[0].Evidence.ExecutablePaths, []string{link}) {
		t.Fatalf("result=%#v", result)
	}
}

func TestNodeHandlerStringBinUsesScopedPackageBasename(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "prefix")
	packagePath := filepath.Join(prefix, "lib", "node_modules", "@scope", "name")
	target := filepath.Join(packagePath, "bin", "cli.js")
	link := filepath.Join(prefix, "bin", "name")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "package.json"), []byte(`{"bin":"bin/cli.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
		switch {
		case strings.Contains(command, " list -g "):
			return runtimeutil.Result{Stdout: fmt.Sprintf(`{"dependencies":{"@scope/name":{"version":"1.0.0","path":%q}}}`, packagePath)}, nil
		case strings.Contains(command, " root -g"):
			return runtimeutil.Result{Stdout: filepath.Dir(filepath.Dir(packagePath))}, nil
		case strings.Contains(command, " prefix -g"):
			return runtimeutil.Result{Stdout: prefix}, nil
		}
		return runtimeutil.Result{}, errors.New("unexpected command")
	}}
	h := newNode(r, filepath.Join(dir, "npm"))
	h.stat = os.Stat
	result := h.Scan(context.Background(), Request{})
	if !result.Complete || len(result.Candidates) != 1 || result.Candidates[0].Evidence == nil || !slices.Equal(result.Candidates[0].Evidence.ExecutablePaths, []string{link}) {
		t.Fatalf("scoped string-bin evidence=%#v", result)
	}
}

func TestNodeHandlerBinEvidenceSortsObjectCommandsAndRejectsUnsafeClaims(t *testing.T) {
	t.Run("object commands are sorted", func(t *testing.T) {
		h := newNode(nil, "/npm")
		h.evalSymlinks = func(path string) (string, error) {
			switch path {
			case "/prefix/bin/a-tool":
				return "/package/bin/a", nil
			case "/prefix/bin/z-tool":
				return "/package/bin/z", nil
			default:
				return path, nil
			}
		}
		h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
		paths, ok := h.binEvidence("/package", "/prefix", "tool", map[string]string{"z-tool": "bin/z", "a-tool": "bin/a"})
		if !ok || !slices.Equal(paths, []string{"/prefix/bin/a-tool", "/prefix/bin/z-tool"}) {
			t.Fatalf("paths=%v ok=%v", paths, ok)
		}
	})

	for _, test := range []struct {
		name string
		bins map[string]string
		stat func(string) (fs.FileInfo, error)
		eval func(string) (string, error)
	}{
		{
			name: "broken wrapper symlink",
			bins: map[string]string{"tool": "bin/tool"},
			stat: func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil },
			eval: func(path string) (string, error) {
				if path == "/prefix/bin/tool" {
					return "", os.ErrNotExist
				}
				return path, nil
			},
		},
		{
			name: "manifest target mismatch",
			bins: map[string]string{"tool": "bin/tool"},
			stat: func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil },
			eval: func(path string) (string, error) {
				if path == "/prefix/bin/tool" {
					return "/other/tool", nil
				}
				return path, nil
			},
		},
		{
			name: "manifest traversal",
			bins: map[string]string{"tool": "../outside"},
			stat: func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil },
			eval: func(path string) (string, error) { return path, nil },
		},
		{
			name: "non-executable wrapper",
			bins: map[string]string{"tool": "bin/tool"},
			stat: func(string) (fs.FileInfo, error) { return fixtureFileInfo{mode: 0o644}, nil },
			eval: func(path string) (string, error) { return path, nil },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newNode(nil, "/npm")
			h.stat, h.evalSymlinks = test.stat, test.eval
			if paths, ok := h.binEvidence("/package", "/prefix", "tool", test.bins); ok || len(paths) != 0 {
				t.Fatalf("unsafe evidence paths=%v ok=%v", paths, ok)
			}
		})
	}
}

func TestNodeHandlerPropagatesRootAndPrefixCancellationForEmptyInventory(t *testing.T) {
	for _, stage := range []string{"root -g", "prefix -g"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
				switch {
				case strings.Contains(command, " list -g "):
					return runtimeutil.Result{Stdout: `{"dependencies":{}}`}, nil
				case strings.Contains(command, stage):
					cancel()
					return runtimeutil.Result{Stdout: "/fixture/value"}, nil
				case strings.Contains(command, " root -g"):
					return runtimeutil.Result{Stdout: "/fixture/root"}, nil
				default:
					return runtimeutil.Result{}, errors.New("unexpected command")
				}
			}}
			result := newNode(r, "/fixture/npm").Scan(ctx, Request{})
			if result.Complete || !errors.Is(result.Err, context.Canceled) || len(result.Candidates) != 0 {
				t.Fatalf("stage=%s result=%#v", stage, result)
			}
		})
	}
}

func TestNodeHandlerBinRequiresSuccessfulAbsolutePrefix(t *testing.T) {
	for _, response := range []runtimeutil.Result{{}, {ExitCode: 1, Stdout: "/prefix"}, {Stdout: "relative"}} {
		r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			switch {
			case strings.Contains(command, " list -g "):
				return runtimeutil.Result{Stdout: `{"dependencies":{"tool":{"version":"1","path":"/tool"}}}`}, nil
			case strings.Contains(command, " prefix -g"):
				return response, nil
			}
			return runtimeutil.Result{}, nil
		}}
		h := newNode(r, "/npm")
		h.readFile = func(string) ([]byte, error) { return []byte(`{"bin":"tool.js"}`), nil }
		h.evalSymlinks = func(path string) (string, error) {
			if path == "/prefix/bin/tool" {
				return "/tool/tool.js", nil
			}
			return path, nil
		}
		result := h.Scan(context.Background(), Request{})
		if result.Complete {
			t.Fatalf("prefix=%#v result=%#v", response, result)
		}
	}
}

func TestNodeHandlerRequiresReadableValidManifest(t *testing.T) {
	for _, content := range [][]byte{nil, []byte(`{`)} {
		r := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			if strings.Contains(command, " list -g ") {
				return runtimeutil.Result{Stdout: `{"dependencies":{"library":{"version":"1","path":"/library"}}}`}, nil
			}
			if strings.Contains(command, " root -g") {
				return runtimeutil.Result{Stdout: "/root"}, nil
			}
			return runtimeutil.Result{Stdout: "/prefix"}, nil
		}}
		h := newNode(r, "/npm")
		h.readFile = func(string) ([]byte, error) {
			if content == nil {
				return nil, os.ErrNotExist
			}
			return content, nil
		}
		if result := h.Scan(context.Background(), Request{}); result.Complete || len(result.Candidates) != 0 {
			t.Fatalf("content=%q result=%#v", content, result)
		}
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
			case strings.Contains(command, " prefix -g"):
				return runtimeutil.Result{Stdout: "/fixture/prefix\n"}, nil
			default:
				return runtimeutil.Result{}, errors.New("metadata optional")
			}
		}}
		h := newNode(r, "/fixture/npm")
		h.readFile = func(string) ([]byte, error) { return []byte(`{}`), nil }
		result := h.Scan(context.Background(), Request{})
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
		h := newNode(r, "/fixture/npm")
		h.readFile = func(string) ([]byte, error) { return []byte(`{}`), nil }
		result := h.Scan(ctx, Request{Report: func(p Progress) {
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
		case strings.Contains(command, " prefix -g"):
			return runtimeutil.Result{Stdout: "/fixture/prefix\n"}, nil
		default:
			return runtimeutil.Result{}, errors.New("unexpected external metadata command")
		}
	}}
	h := newNode(r, "/fixture/npm")
	h.readFile = func(string) ([]byte, error) { return []byte(`{}`), nil }
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
