package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	httpx "github.com/eoctet/tendkit/pkg/http"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type nodeRunner struct {
	calls []string
	envs  []map[string]string
	run   func(context.Context, string) (runtimeutil.Result, error)
}

type fixtureFileInfo struct {
	dir  bool
	mode fs.FileMode
}

func assertActiveProvider(t *testing.T, provider model.ProviderType) {
	t.Helper()
	for _, retired := range []string{"none", "command", "vscode", "chrome", "firefox", "url_json", "url_text"} {
		if string(provider) == retired {
			t.Fatalf("retired provider %q", provider)
		}
	}
	if !provider.Valid() {
		t.Fatalf("invalid provider %q", provider)
	}
}

func (fixtureFileInfo) Name() string { return "fixture" }
func (f fixtureFileInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o755
	}
	if f.mode != 0 {
		return f.mode
	}
	return 0o755
}
func (f fixtureFileInfo) IsDir() bool      { return f.dir }
func (fixtureFileInfo) ModTime() time.Time { return time.Time{} }
func (fixtureFileInfo) Size() int64        { return 0 }
func (fixtureFileInfo) Sys() any           { return nil }

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
func TestPackageHandlerContract(t *testing.T) {
	t.Run("manager-path-requires-expanded-absolute-path", func(t *testing.T) {
		t.Setenv("TENDKIT_TEST_MANAGER_ROOT", "/fixture")
		for _, tc := range []struct {
			name, lookup, configured, want string
			homeFails                      bool
		}{
			{name: "absolute PATH wins", lookup: "/path/npm", configured: "tools/npm", want: "/path/npm"},
			{name: "absolute fallback", configured: "/fixture/npm", want: "/fixture/npm"},
			{name: "relative fallback rejected", configured: "tools/npm"},
			{name: "relative PATH rejected", lookup: "tools/npm"},
			{name: "relative PATH continues fallback", lookup: "tools/npm", configured: "/fixture/npm", want: "/fixture/npm"},
			{name: "home expanded", configured: "~/npm", want: "/fixture/npm"},
			{name: "environment expanded", configured: "$TENDKIT_TEST_MANAGER_ROOT/npm", want: "/fixture/npm"},
			{name: "home failure rejected", configured: "~/npm", homeFails: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				lookup := func(string) (string, error) {
					if tc.lookup == "" {
						return "", ErrNotFound
					}
					return tc.lookup, nil
				}
				stat := func(path string) (fs.FileInfo, error) {
					if !filepath.IsAbs(path) {
						t.Fatalf("relative path reached stat: %q", path)
					}
					return fixtureFileInfo{}, nil
				}
				home := func() (string, error) {
					if tc.homeFails {
						return "", ErrNotFound
					}
					return "/fixture", nil
				}
				apps := []model.Application{{ID: "NPM", InstallPath: tc.configured}}
				if got := managerPath("npm", apps, lookup, stat, home); got != tc.want {
					t.Fatalf("manager = %q, want %q", got, tc.want)
				}
			})
		}
		t.Run("skip relative and directory before valid absolute fallback", func(t *testing.T) {
			var checked []string
			apps := []model.Application{{ID: "npm", InstallPath: "tools/npm"}, {Name: "npm", InstallPath: "/directory"}, {Name: "NPM", InstallPath: "/fixture/npm"}}
			got := managerPath("npm", apps, func(string) (string, error) { return "", ErrNotFound }, func(path string) (fs.FileInfo, error) {
				checked = append(checked, path)
				return fixtureFileInfo{dir: path == "/directory"}, nil
			}, func() (string, error) { return "/fixture", nil })
			if got != "/fixture/npm" || !slices.Equal(checked, []string{"/directory", "/fixture/npm"}) {
				t.Fatalf("manager=%q stat calls=%v", got, checked)
			}
		})
	})
	t.Run("relative-manager-config-is-unavailable-without-execution", func(t *testing.T) {
		lookup := func(string) (string, error) { return "", ErrNotFound }
		stat := func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
		for _, name := range []string{"npm", "uv", "cargo", "brew-formula", "brew-cask", "ruby"} {
			t.Run(name, func(t *testing.T) {
				runner := &nodeRunner{run: func(context.Context, string) (runtimeutil.Result, error) {
					t.Fatal("relative manager must not execute")
					return runtimeutil.Result{}, nil
				}}
				var h Handler
				binary := name
				switch name {
				case "npm":
					value := NewNode(runner)
					value.lookPath, value.stat = lookup, stat
					h = value
				case "uv":
					value := NewUV(runner)
					value.lookPath, value.stat = lookup, stat
					h = value
				case "cargo":
					value := NewCargo(runner)
					value.lookPath, value.stat = lookup, stat
					h = value
				case "brew-formula":
					value := NewHomebrewFormula(runner)
					value.lookPath, value.stat = lookup, stat
					value.host = func() string { return "darwin" }
					h, binary = value, "brew"
				case "brew-cask":
					value := NewHomebrewCask(runner)
					value.lookPath, value.stat = lookup, stat
					value.host = func() string { return "darwin" }
					h, binary = value, "brew"
				case "ruby":
					value := NewRuby(runner)
					value.lookPath, value.stat = lookup, stat
					h = value
				}
				result := h.Scan(context.Background(), Request{Configured: []model.Application{{ID: binary, InstallPath: "tools/" + binary}}})
				var unavailable *PackageManagerUnavailableError
				if result.Complete || len(result.Candidates) != 0 || !errors.As(result.Err, &unavailable) || len(runner.calls) != 0 {
					t.Fatalf("result=%#v commands=%v", result, runner.calls)
				}
			})
		}
	})
	t.Run("node-handler-golden-candidate-and-environment", func(t *testing.T) {
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
	})
	t.Run("node-handler-produces-evidence-for-verified-global-bin", func(t *testing.T) {
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
		if paths, ok := h.binEvidence(packagePath, prefix, map[string]string{"tool": "bin/tool.js"}); !ok || !slices.Equal(paths, []string{link}) {
			t.Fatalf("direct evidence paths=%v ok=%v", paths, ok)
		}
		result := h.Scan(context.Background(), Request{})
		if !result.Complete || len(result.Candidates) != 1 || result.Candidates[0].Evidence == nil || !slices.Equal(result.Candidates[0].Evidence.ExecutablePaths, []string{link}) {
			t.Fatalf("result=%#v", result)
		}
	})
	t.Run("node-handler-string-bin-uses-scoped-package-basename", func(t *testing.T) {
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
	})
	t.Run("node-handler-bin-evidence-sorts-object-commands-and-rejects-unsafe-claims", func(t *testing.T) {
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
			paths, ok := h.binEvidence("/package", "/prefix", map[string]string{"z-tool": "bin/z", "a-tool": "bin/a"})
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
				if paths, ok := h.binEvidence("/package", "/prefix", test.bins); ok || len(paths) != 0 {
					t.Fatalf("unsafe evidence paths=%v ok=%v", paths, ok)
				}
			})
		}
	})
	t.Run("node-handler-propagates-root-and-prefix-cancellation-for-empty-inventory", func(t *testing.T) {
		for _, stage := range []string{"root -g", "prefix -g"} {
			ctx, cancel := context.WithCancel(context.Background())
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
			cancel()
			if result.Complete || !errors.Is(result.Err, context.Canceled) || len(result.Candidates) != 0 {
				t.Errorf("stage=%s result=%#v", stage, result)
			}
		}
	})
	t.Run("node-handler-bin-requires-successful-absolute-prefix", func(t *testing.T) {
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
	})
	t.Run("node-handler-requires-readable-valid-manifest", func(t *testing.T) {
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
	})
	t.Run("node-handler-listing-failures-root-fallback-metadata-and-cancellation", func(t *testing.T) {
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
	})
	t.Run("node-handler-fallback-missing-and-partial-inventory", func(t *testing.T) {
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
	})
	t.Run("node-handler-forty-packages-uses-bounded-commands-and-local-metadata", func(t *testing.T) {
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
	})
}

// TestPackageHandlerEcosystemContract keeps cross-ecosystem input and safety
// boundaries in one table-driven contract without duplicating discovery flows.
func TestPackageHandlerEcosystemContract(t *testing.T) {
	t.Run("python/strict-json-and-cancel", func(t *testing.T) {
		for _, tc := range []struct {
			name, output string
			cancel       bool
			complete     bool
		}{
			{"valid", `[{"name":"tool","version":"1.2.3"}]`, false, true},
			{"invalid-json", `not json`, false, false},
			{"cancel", `[]`, true, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				runner := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
					if strings.Contains(command, "pip list") {
						if tc.cancel {
							cancel()
						}
						return runtimeutil.Result{Stdout: tc.output}, nil
					}
					if strings.Contains(command, "pip show") {
						return runtimeutil.Result{Stdout: "Name: tool\nSummary: fixture\n"}, nil
					}
					return runtimeutil.Result{Stdout: `{"tool":{"path":"/fixture/tool","scope":"user","complete":true}}`}, nil
				}}
				h := NewPython(runner)
				h.lookPath = func(string) (string, error) { return "/fixture/python3", nil }
				h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
				h.homeDir = func() (string, error) { return "/fixture/home", nil }
				result := h.Scan(ctx, Request{})
				if result.Complete != tc.complete || (tc.complete && len(result.Candidates) != 1) || (!tc.complete && result.Err == nil) {
					t.Fatalf("result=%#v", result)
				}
			})
		}
	})
	t.Run("go/known-owner-is-held-on-unconfirmed-metadata", func(t *testing.T) {
		runner := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			if strings.Contains(command, "env GOPATH GOBIN") {
				return runtimeutil.Result{Stdout: "/gopath\n/gobin\n"}, nil
			}
			return runtimeutil.Result{}, errors.New("metadata failed")
		}}
		h := NewGo(runner)
		h.lookPath = func(string) (string, error) { return "/fixture/go", nil }
		h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
		h.readDir = func(dir string) ([]fs.DirEntry, error) {
			if dir == "/gobin" {
				return []fs.DirEntry{fixtureDirEntry{name: "known"}}, nil
			}
			return nil, fs.ErrNotExist
		}
		result := h.Scan(context.Background(), Request{Configured: []model.Application{{Identity: "package:go:known", InstallPath: "/gobin/known", ScanManaged: true}}})
		if result.Complete || result.Err == nil {
			t.Fatalf("known owner was silently removed: %#v", result)
		}
	})
	t.Run("go/confirmed-binaries-produce-canonical-evidence", func(t *testing.T) {
		runner := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			switch {
			case strings.Contains(command, "env GOPATH GOBIN"):
				return runtimeutil.Result{Stdout: "/one:/two\n/custom/bin\n"}, nil
			case strings.Contains(command, "version -m"):
				return runtimeutil.Result{Stdout: "path github.com/acme/tool/cmd/tool\nmod github.com/acme/tool v1.2.3\n"}, nil
			default:
				return runtimeutil.Result{}, errors.New("unexpected command")
			}
		}}
		h := NewGo(runner)
		h.lookPath = func(string) (string, error) { return "/fixture/go", nil }
		h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
		h.readDir = func(dir string) ([]fs.DirEntry, error) {
			if dir == "/custom/bin" {
				return fixtureEntries(fixtureDirEntry{name: "tool"}), nil
			}
			return nil, fs.ErrNotExist
		}
		result := h.Scan(context.Background(), Request{})
		if !result.Complete || len(result.Candidates) != 1 {
			t.Fatalf("result=%#v", result)
		}
		candidate := result.Candidates[0]
		if candidate.Application.Identity != "package:go:tool" || candidate.CurrentVersion != "1.2.3" || candidate.Application.URL != "https://github.com/acme/tool" {
			t.Fatalf("candidate=%#v", candidate)
		}
		if candidate.Evidence == nil || !slices.Equal(candidate.Evidence.ExecutablePaths, []string{"/custom/bin/tool"}) {
			t.Fatalf("evidence=%#v", candidate.Evidence)
		}
		if dirs := strings.Join(goBinDirs("/one:/two\n/custom/bin\n"), ","); dirs != "/one/bin,/two/bin,/custom/bin" {
			t.Fatalf("dirs=%q", dirs)
		}
		if goGitHubURL("example.com/acme/tool") != "" {
			t.Fatal("non-GitHub module produced URL")
		}
	})
	t.Run("go/nonzero-environment-and-duplicate-confirmation-are-incomplete", func(t *testing.T) {
		for _, test := range []struct {
			name, environment string
			exitCode          int
			wantCandidates    int
		}{
			{"nonzero-env", "", 2, 0},
			{"duplicate-command", "/gopath\n/gobin\n", 0, 1},
		} {
			t.Run(test.name, func(t *testing.T) {
				runner := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
					if strings.Contains(command, "env GOPATH GOBIN") {
						return runtimeutil.Result{Stdout: test.environment, ExitCode: test.exitCode}, nil
					}
					return runtimeutil.Result{Stdout: "path example.invalid/tool\nmod example.invalid/tool v1.0.0\n"}, nil
				}}
				h := NewGo(runner)
				h.lookPath = func(string) (string, error) { return "/fixture/go", nil }
				h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
				h.readDir = func(dir string) ([]fs.DirEntry, error) {
					if dir == "/gopath/bin" || dir == "/gobin" {
						return fixtureEntries(fixtureDirEntry{name: "tool"}), nil
					}
					return nil, fs.ErrNotExist
				}
				result := h.Scan(context.Background(), Request{})
				if result.Complete || result.Err == nil || len(result.Candidates) != test.wantCandidates {
					t.Fatalf("result=%#v", result)
				}
			})
		}
	})
	t.Run("cargo/rejects-traversal-and-symlink-escape", func(t *testing.T) {
		for _, name := range []string{"../escape", "/absolute", "nested/name", `nested\\name`} {
			t.Run(name, func(t *testing.T) {
				if _, err := parseCargoInstallList("tool v1.0.0:\n    " + name + "\n"); err == nil {
					t.Fatalf("accepted unsafe binary %q", name)
				}
			})
		}
		root, outside := t.TempDir(), filepath.Join(t.TempDir(), "outside")
		link := filepath.Join(root, "bin", "tool")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		h := NewCargo(&scriptedRunner{responses: []runtimeutil.Result{{Stdout: "tool v1.0.0:\n    tool\n"}}})
		h.lookPath = func(string) (string, error) { return "/fixture/cargo", nil }
		result := h.Scan(context.Background(), Request{Configured: []model.Application{{Provider: model.ProviderConfig{Type: model.ProviderCargo}, Environment: map[string]string{"CARGO_INSTALL_ROOT": root}}}})
		var incomplete *PackageInventoryIncompleteError
		if result.Complete || !errors.As(result.Err, &incomplete) {
			t.Fatalf("escape result=%#v", result)
		}
	})
	t.Run("ruby/inventory-is-deterministic-and-fallback-actions-are-absolute", func(t *testing.T) {
		command := rubyGemListCommand("ruby")
		for _, fragment := range []string{"s.version == old.version", "latest.values.sort_by", "Gem.bindir"} {
			if !strings.Contains(command, fragment) {
				t.Fatalf("missing stable Ruby contract %q", fragment)
			}
		}
		runner := &nodeRunner{run: func(_ context.Context, _ string) (runtimeutil.Result, error) {
			return runtimeutil.Result{Stdout: `[{"name":"tool","version":"1.0.0","install_path":"/u","install_scope":"user"}]`}, nil
		}}
		h := NewRuby(runner)
		h.lookPath = func(string) (string, error) { return "", ErrNotFound }
		h.homeDir = func() (string, error) { return "/home", nil }
		h.stat = func(path string) (fs.FileInfo, error) {
			if path == "/home/bin/ruby" {
				return fixtureFileInfo{}, nil
			}
			return nil, ErrNotFound
		}
		result := h.Scan(context.Background(), Request{Configured: []model.Application{{Name: "Ruby", InstallPath: "~/bin/ruby"}}})
		if !result.Complete || len(result.Candidates) != 1 {
			t.Fatalf("result=%#v", result)
		}
		candidate := result.Candidates[0]
		if candidate.Application.Identity != "package:ruby:tool" || candidate.CurrentVersion != "1.0.0" {
			t.Fatalf("candidate=%#v", candidate)
		}
		for _, action := range []string{result.Candidates[0].Application.Provider.VersionAction(), result.Candidates[0].Application.Provider.UpdateAction()} {
			if !strings.HasPrefix(action, "/home/bin/ruby -rrubygems/gem_runner -e ") || strings.Contains(action, " -S gem") {
				t.Fatalf("unsafe action %q", action)
			}
		}
	})
	t.Run("uv/rejects-duplicate-claims", func(t *testing.T) {
		for _, output := range []string{"tool v1.0.0\n  - tool (/tool)\ntool v1.0.1\n  - tool (/other)\n", "tool v1.0.0\n  - tool (/tool)\n  - tool (/tool)\n"} {
			if tools, ok := parseUVTools(output); ok || tools != nil {
				t.Fatalf("duplicate claim accepted: %#v", tools)
			}
		}
	})
	t.Run("uv/discovery-requires-an-absolute-tool-directory", func(t *testing.T) {
		runner := &nodeRunner{run: func(_ context.Context, command string) (runtimeutil.Result, error) {
			if strings.Contains(command, "tool list --show-paths") {
				return runtimeutil.Result{Stdout: "tool v1.0.0\n  - tool (/tool)\n"}, nil
			}
			return runtimeutil.Result{}, errors.New("tool dir unavailable")
		}}
		h := NewUV(runner)
		h.lookPath = func(string) (string, error) { return "/fixture/uv", nil }
		h.stat = func(string) (fs.FileInfo, error) { return fixtureFileInfo{}, nil }
		h.homeDir = func() (string, error) { return "/home", nil }
		if result := h.Scan(context.Background(), Request{}); result.Complete || result.Err == nil {
			t.Fatalf("nonempty inventory without tool dir=%#v", result)
		}
		if !uvAppLine.MatchString("tool v1.2.3-rc.1 (/tools/tool)") {
			t.Fatal("UV line parser rejected valid prerelease")
		}
	})
	t.Run("github/strict-json-and-oversize-response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/bad" {
				_, _ = w.Write([]byte("{"))
				return
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1"}` + strings.Repeat(" ", (1<<20)+1)))
		}))
		defer server.Close()
		source := httpx.NewHTTPSource(server.Client(), httpx.HTTPOptions{Retries: 0, MaxConcurrencyPerHost: 1})
		if _, err := NewGitHubResolver(server.URL+"/bad", server.URL+"/tag", source).Resolve(context.Background(), "owner/repo"); err == nil {
			t.Fatal("accepted malformed release JSON")
		}
		_, err := NewGitHubResolver(server.URL+"/large", server.URL+"/tag", source).Resolve(context.Background(), "owner/repo")
		var oversized *httpx.HTTPResponseTooLargeError
		if !errors.As(err, &oversized) {
			t.Fatalf("oversized response error=%v", err)
		}
	})
	t.Run("github/release-then-tag-resolution-fails-closed", func(t *testing.T) {
		for _, test := range []struct {
			name          string
			release, tags int
			body          string
			want          model.ProviderType
			wantErr       bool
		}{
			{"release", http.StatusOK, http.StatusInternalServerError, `{"tag_name":"v1"}`, model.ProviderGitHubRelease, false},
			{"tag", http.StatusNotFound, http.StatusOK, `[ {"name":"v1"} ]`, model.ProviderGitHubTag, false},
			{"server-error", http.StatusInternalServerError, http.StatusOK, `[]`, "", true},
		} {
			t.Run(test.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					if request.URL.Path == "/release" {
						w.WriteHeader(test.release)
						if test.release == http.StatusOK {
							_, _ = w.Write([]byte(test.body))
						}
						return
					}
					w.WriteHeader(test.tags)
					if test.tags == http.StatusOK {
						_, _ = w.Write([]byte(test.body))
					}
				}))
				defer server.Close()
				source := httpx.NewHTTPSource(server.Client(), httpx.HTTPOptions{Retries: 0, MaxConcurrencyPerHost: 1})
				got, err := NewGitHubResolver(server.URL+"/release", server.URL+"/tags", source).Resolve(context.Background(), "owner/repo")
				if got != test.want || (err != nil) != test.wantErr {
					t.Fatalf("Resolve()=(%q,%v)", got, err)
				}
			})
		}
	})
}
