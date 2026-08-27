package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type providerRunnerResponse struct {
	result runtimeutil.Result
	err    error
}

type recordingProviderRunner struct {
	commands  []string
	responses []providerRunnerResponse
}

func (r *recordingProviderRunner) Run(_ context.Context, command string, _ map[string]string) (runtimeutil.Result, error) {
	r.commands = append(r.commands, command)
	if len(r.responses) == 0 {
		return runtimeutil.Result{}, fmt.Errorf("unexpected command: %s", command)
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.result, response.err
}

func TestHomebrewPackageParserRejectsAmbiguousOrEmptyNames(t *testing.T) {
	for _, test := range []struct {
		value string
		kind  string
		name  string
		valid bool
	}{
		{"ripgrep", "formula", "ripgrep", true},
		{"formula/Homebrew/core/ripgrep", "formula", "Homebrew/core/ripgrep", true},
		{"cask/visual-studio-code", "cask", "visual-studio-code", true},
		{"formula/", "", "", false}, {"cask/", "", "", false}, {"formula//rg", "", "", false}, {"formula/../rg", "", "", false}, {"", "", "", false},
	} {
		kind, name, err := parseHomebrewPackage(test.value)
		if test.valid {
			if err != nil || kind != test.kind || name != test.name {
				t.Fatalf("parseHomebrewPackage(%q) = %q, %q, %v", test.value, kind, name, err)
			}
		} else if err == nil {
			t.Fatalf("parseHomebrewPackage(%q) unexpectedly succeeded", test.value)
		}
	}
}

func TestHomebrewCapabilityMatrix(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry, nil, nil); err != nil {
		t.Fatal(err)
	}
	capabilities, ok := registry.Resolve("homebrew")
	if !ok {
		t.Fatal("homebrew was not registered")
	}
	if capabilities.Current == nil || capabilities.Latest == nil || capabilities.Update == nil || capabilities.Download != nil || capabilities.Install != nil || capabilities.Checksum != nil || capabilities.Artifact != nil {
		t.Fatalf("homebrew capabilities = %#v", capabilities)
	}
}

func TestHomebrewCurrentUsesFastInventoryWithoutInfo(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "sample", "1.0.0")
	installPath := filepath.Join(prefix, "bin", "sample")
	if err := os.MkdirAll(filepath.Dir(installPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installPath, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingProviderRunner{responses: []providerRunnerResponse{
		{result: runtimeutil.Result{Stdout: `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0","optlinked_version":"1.0.0","pinned_version":null}],"casks":[]}`}},
		{result: runtimeutil.Result{Stdout: root + "\n"}},
	}}
	p := HomebrewProvider{Runner: runner, lookup: func(string, map[string]string) (string, error) { return "/fixture/bin/brew", nil }}
	current, err := p.Current(context.Background(), homebrewRequest("formula/sample", installPath))
	if err != nil || current != "1.0.0" {
		t.Fatalf("current=%q error=%v", current, err)
	}
	if got := strings.Join(runner.commands, "\n"); strings.Contains(got, " info ") {
		t.Fatalf("commands use brew info: %s", got)
	}
}

func TestRegistryKeepsHomebrewCurrentImplementation(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry, nil, nil); err != nil {
		t.Fatal(err)
	}
	capabilities, ok := registry.Resolve(string(model.ProviderHomebrew))
	if !ok {
		t.Fatal("homebrew was not registered")
	}
	if _, ok := capabilities.Current.(HomebrewProvider); !ok {
		t.Fatalf("homebrew Current implementation=%T", capabilities.Current)
	}
}

func TestHomebrewSuccessCommandMatrixUsesOneAbsoluteManager(t *testing.T) {
	root := t.TempDir()
	formulaRoot := filepath.Join(root, "Cellar")
	formulaPath := filepath.Join(formulaRoot, "sample", "1.0.0", "bin", "sample")
	if err := os.MkdirAll(filepath.Dir(formulaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(formulaPath, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	caskRoot := filepath.Join(root, "Caskroom")
	caskPath := filepath.Join(caskRoot, "sample", "1.0.0", "Sample.app")
	if err := os.MkdirAll(caskPath, 0o755); err != nil {
		t.Fatal(err)
	}
	formulaInstalled := `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0","optlinked_version":"1.0.0","pinned_version":null}],"casks":[]}`
	caskInstalled := `{"formulae":[],"casks":[{"token":"sample","versions":["1.0.0"],"pinned_version":null}]}`
	outdated := `{"formulae":[{"name":"sample","versions":{"stable":"2.0.0"}}]}`
	caskOutdated := `{"casks":[{"name":"sample","installed_versions":["1.0.0"],"current_version":"2.0.0"}]}`

	tests := []struct {
		name      string
		request   Request
		responses []providerRunnerResponse
		want      string
		commands  []string
		call      func(HomebrewProvider, context.Context, Request) (string, error)
	}{
		{
			name: "current formula", request: homebrewRequest("formula/sample", formulaPath), want: "1.0.0",
			responses: []providerRunnerResponse{{result: runtimeutil.Result{Stdout: formulaInstalled}}, {result: runtimeutil.Result{Stdout: formulaRoot + "\n"}}},
			commands:  []string{"/fixture/bin/brew list --formula --versions --json", "/fixture/bin/brew --cellar"},
			call: func(p HomebrewProvider, ctx context.Context, request Request) (string, error) {
				return p.Current(ctx, request)
			},
		},
		{
			name: "current cask", request: homebrewRequest("cask/sample", caskPath), want: "1.0.0",
			responses: []providerRunnerResponse{{result: runtimeutil.Result{Stdout: caskInstalled}}, {result: runtimeutil.Result{Stdout: caskRoot + "\n"}}},
			commands:  []string{"/fixture/bin/brew list --cask --versions --json", "/fixture/bin/brew --caskroom"},
			call: func(p HomebrewProvider, ctx context.Context, request Request) (string, error) {
				return p.Current(ctx, request)
			},
		},
		{
			name: "latest", request: homebrewRequest("formula/sample", formulaPath), want: "2.0.0",
			responses: []providerRunnerResponse{{result: runtimeutil.Result{Stdout: formulaInstalled}}, {result: runtimeutil.Result{Stdout: formulaRoot + "\n"}}, {result: runtimeutil.Result{ExitCode: 1, Stdout: outdated}}},
			commands:  []string{"/fixture/bin/brew list --formula --versions --json", "/fixture/bin/brew --cellar", "/fixture/bin/brew outdated --json=v2 --formula sample"},
			call: func(p HomebrewProvider, ctx context.Context, request Request) (string, error) {
				return p.Latest(ctx, request)
			},
		},
		{
			name: "latest current with progress on stderr", request: homebrewRequest("formula/sample", formulaPath), want: "1.0.0",
			responses: []providerRunnerResponse{{result: runtimeutil.Result{Stdout: formulaInstalled}}, {result: runtimeutil.Result{Stdout: formulaRoot + "\n"}}, {result: runtimeutil.Result{Stdout: `{"formulae":[],"casks":[]}`, Stderr: "==> Downloading Homebrew API data"}}},
			commands:  []string{"/fixture/bin/brew list --formula --versions --json", "/fixture/bin/brew --cellar", "/fixture/bin/brew outdated --json=v2 --formula sample"},
			call: func(p HomebrewProvider, ctx context.Context, request Request) (string, error) {
				return p.Latest(ctx, request)
			},
		},
		{
			name: "latest cask", request: homebrewRequest("cask/sample", caskPath), want: "2.0.0",
			responses: []providerRunnerResponse{{result: runtimeutil.Result{Stdout: caskInstalled}}, {result: runtimeutil.Result{Stdout: caskRoot + "\n"}}, {result: runtimeutil.Result{ExitCode: 1, Stdout: caskOutdated}}},
			commands:  []string{"/fixture/bin/brew list --cask --versions --json", "/fixture/bin/brew --caskroom", "/fixture/bin/brew outdated --json=v2 --cask sample"},
			call: func(p HomebrewProvider, ctx context.Context, request Request) (string, error) {
				return p.Latest(ctx, request)
			},
		},
		{
			name: "update", request: homebrewRequest("formula/sample", formulaPath),
			responses: []providerRunnerResponse{{result: runtimeutil.Result{Stdout: formulaInstalled}}, {result: runtimeutil.Result{Stdout: formulaRoot + "\n"}}, {}},
			commands:  []string{"/fixture/bin/brew list --formula --versions --json", "/fixture/bin/brew --cellar", "/fixture/bin/brew upgrade --formula sample"},
			call: func(p HomebrewProvider, ctx context.Context, request Request) (string, error) {
				return "", p.Update(ctx, request)
			},
		},
		{
			name: "update cask", request: homebrewRequest("cask/sample", caskPath),
			responses: []providerRunnerResponse{{result: runtimeutil.Result{Stdout: caskInstalled}}, {result: runtimeutil.Result{Stdout: caskRoot + "\n"}}, {}},
			commands:  []string{"/fixture/bin/brew list --cask --versions --json", "/fixture/bin/brew --caskroom", "/fixture/bin/brew upgrade --cask sample"},
			call: func(p HomebrewProvider, ctx context.Context, request Request) (string, error) {
				return "", p.Update(ctx, request)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingProviderRunner{responses: slices.Clone(test.responses)}
			p := HomebrewProvider{
				Runner: runner,
				host:   func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "darwin"} },
				lookup: func(name string, _ map[string]string) (string, error) {
					if name != "brew" {
						t.Fatalf("lookup name=%q", name)
					}
					return "/fixture/bin/brew", nil
				},
			}
			got, err := test.call(p, context.Background(), test.request)
			if err != nil || got != test.want {
				t.Fatalf("result=%q error=%v", got, err)
			}
			if !slices.Equal(runner.commands, test.commands) {
				t.Fatalf("commands=%q want=%q", runner.commands, test.commands)
			}
		})
	}
}

func TestHomebrewRunnerFailureMatrixReturnsTypedErrors(t *testing.T) {
	root := t.TempDir()
	formulaRoot := filepath.Join(root, "Cellar")
	formulaPath := filepath.Join(formulaRoot, "sample", "1.0.0", "bin", "sample")
	if err := os.MkdirAll(filepath.Dir(formulaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(formulaPath, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	caskRoot := filepath.Join(root, "Caskroom")
	caskPath := filepath.Join(caskRoot, "sample", "1.0.0", "Sample.app")
	if err := os.MkdirAll(caskPath, 0o755); err != nil {
		t.Fatal(err)
	}
	formulaInstalled := `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0","optlinked_version":"1.0.0"}],"casks":[]}`
	caskInstalled := `{"formulae":[],"casks":[{"token":"sample","versions":["1.0.0"]}]}`
	outdated := `{"formulae":[{"name":"sample","versions":{"stable":"2.0.0"}}]}`
	sentinel := errors.New("runner failed")

	operations := []struct {
		name       string
		capability Capability
		request    Request
		base       []providerRunnerResponse
		failAt     []int
		call       func(HomebrewProvider, context.Context, Request) error
	}{
		{"current formula", CapabilityCurrent, homebrewRequest("formula/sample", formulaPath), []providerRunnerResponse{{result: runtimeutil.Result{Stdout: formulaInstalled}}, {result: runtimeutil.Result{Stdout: formulaRoot + "\n"}}}, []int{0, 1}, func(p HomebrewProvider, ctx context.Context, r Request) error {
			_, err := p.Current(ctx, r)
			return err
		}},
		{"current cask", CapabilityCurrent, homebrewRequest("cask/sample", caskPath), []providerRunnerResponse{{result: runtimeutil.Result{Stdout: caskInstalled}}, {result: runtimeutil.Result{Stdout: caskRoot + "\n"}}}, []int{0, 1}, func(p HomebrewProvider, ctx context.Context, r Request) error {
			_, err := p.Current(ctx, r)
			return err
		}},
		{"latest", CapabilityLatest, homebrewRequest("formula/sample", formulaPath), []providerRunnerResponse{{result: runtimeutil.Result{Stdout: formulaInstalled}}, {result: runtimeutil.Result{Stdout: formulaRoot + "\n"}}, {result: runtimeutil.Result{Stdout: outdated}}}, []int{0, 1, 2}, func(p HomebrewProvider, ctx context.Context, r Request) error { _, err := p.Latest(ctx, r); return err }},
		{"update", CapabilityUpdate, homebrewRequest("formula/sample", formulaPath), []providerRunnerResponse{{result: runtimeutil.Result{Stdout: formulaInstalled}}, {result: runtimeutil.Result{Stdout: formulaRoot + "\n"}}, {}}, []int{0, 1, 2}, func(p HomebrewProvider, ctx context.Context, r Request) error { return p.Update(ctx, r) }},
	}
	failures := []struct {
		name  string
		set   func(*providerRunnerResponse)
		ctx   func() context.Context
		cause error
	}{
		{"runner error", func(r *providerRunnerResponse) { r.err = sentinel }, context.Background, sentinel},
		{"nonzero", func(r *providerRunnerResponse) { r.result = runtimeutil.Result{ExitCode: 17, Stderr: "failed"} }, context.Background, nil},
		{"cancellation", func(r *providerRunnerResponse) { r.err = context.Canceled }, canceledContext, context.Canceled},
	}
	for _, operation := range operations {
		for _, failAt := range operation.failAt {
			for _, failure := range failures {
				t.Run(fmt.Sprintf("%s/command %d/%s", operation.name, failAt+1, failure.name), func(t *testing.T) {
					responses := slices.Clone(operation.base)
					failure.set(&responses[failAt])
					runner := &recordingProviderRunner{responses: responses}
					p := HomebrewProvider{Runner: runner, host: func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "darwin"} }, lookup: func(string, map[string]string) (string, error) { return "/fixture/bin/brew", nil }}
					err := operation.call(p, failure.ctx(), operation.request)
					assertProviderError(t, err, model.ProviderHomebrew, operation.capability)
					if failure.cause != nil && !errors.Is(err, failure.cause) {
						t.Fatalf("error=%v does not wrap %v", err, failure.cause)
					}
					if failure.name == "nonzero" {
						var typed *Error
						if !errors.As(err, &typed) || len(typed.Args) < 2 || typed.Args[1] != 17 {
							t.Fatalf("exit error=%#v", err)
						}
						if operation.name == "update" && failAt == 2 && !slices.Equal(typed.Args, []any{"Sample", 17, "failed"}) {
							t.Fatalf("update exit args=%#v", typed.Args)
						}
					}
					if len(runner.commands) != failAt+1 {
						t.Fatalf("commands after failure=%q", runner.commands)
					}
				})
			}
		}
	}
}

func TestHomebrewCaskIsUnavailableOffDarwinBeforeLookupOrCommand(t *testing.T) {
	for _, test := range []struct {
		name       string
		capability Capability
		call       func(HomebrewProvider, Request) error
	}{
		{"current", CapabilityCurrent, func(p HomebrewProvider, r Request) error { _, err := p.Current(context.Background(), r); return err }},
		{"latest", CapabilityLatest, func(p HomebrewProvider, r Request) error { _, err := p.Latest(context.Background(), r); return err }},
		{"update", CapabilityUpdate, func(p HomebrewProvider, r Request) error { return p.Update(context.Background(), r) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookedUp := false
			runner := &recordingProviderRunner{}
			p := HomebrewProvider{Runner: runner, host: func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "linux"} }, lookup: func(string, map[string]string) (string, error) { lookedUp = true; return "", nil }}
			err := test.call(p, homebrewRequest("cask/sample", "/Applications/Sample.app"))
			assertProviderError(t, err, model.ProviderHomebrew, test.capability)
			if !errors.Is(err, ErrUnavailable) || lookedUp || len(runner.commands) != 0 {
				t.Fatalf("error=%v lookup=%v commands=%q", err, lookedUp, runner.commands)
			}
		})
	}
}

func TestHomebrewUpdatePreflightFailuresNeverRunUpgrade(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bin", "sample")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		responses []providerRunnerResponse
		key       string
	}{
		{"parse failure", []providerRunnerResponse{{result: runtimeutil.Result{Stdout: `{broken`}}}, "provider.homebrew_parse_failed"},
		{"ownership failure", []providerRunnerResponse{{result: runtimeutil.Result{Stdout: `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0"}],"casks":[]}`}}, {result: runtimeutil.Result{Stdout: t.TempDir() + "\n"}}}, "provider.target_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingProviderRunner{responses: slices.Clone(test.responses)}
			p := HomebrewProvider{Runner: runner, lookup: func(string, map[string]string) (string, error) { return "/fixture/bin/brew", nil }}
			err := p.Update(context.Background(), homebrewRequest("formula/sample", path))
			var typed *Error
			if !errors.As(err, &typed) || typed.Provider != string(model.ProviderHomebrew) || typed.Capability != CapabilityUpdate || typed.Key != test.key {
				t.Fatalf("error=%#v", err)
			}
			if len(runner.commands) != len(test.responses) || strings.Contains(strings.Join(runner.commands, "\n"), " upgrade ") {
				t.Fatalf("commands=%q", runner.commands)
			}
		})
	}
}

func TestHomebrewCaskOutdatedUsesDedicatedSchema(t *testing.T) {
	raw := `{"casks":[{"name":"sample","installed_versions":["1.0.0"],"current_version":"2.0.0"}]}`
	latest, found, err := parseHomebrewLatest(raw, "cask", "sample")
	if err != nil || !found || latest != "2.0.0" {
		t.Fatalf("latest=%q found=%v error=%v", latest, found, err)
	}
	if _, _, err := parseHomebrewLatest(`{"casks":[{"name":"sample","installed_versions":["1.0.0","1.1.0"],"current_version":"2.0.0"}]}`, "cask", "sample"); err == nil {
		t.Fatal("ambiguous installed_versions was accepted")
	}
}

func TestVerifyCaskOwnershipPathAcceptsCaskroomAppSymlink(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "Caskroom", "sample", "1.0.0")
	application := filepath.Join(root, "Applications", "Sample.app")
	if err := os.MkdirAll(application, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(application, filepath.Join(prefix, "Sample.app")); err != nil {
		t.Fatal(err)
	}
	if err := verifyCaskOwnershipPath(application, prefix); err != nil {
		t.Fatalf("Caskroom app symlink ownership failed: %v", err)
	}
}

func TestHomebrewCaskOutdatedNonzeroWithoutTargetFails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample", "1.0.0", "Sample.app")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := `{"formulae":[],"casks":[{"token":"sample","versions":["1.0.0"]}]}`
	runner := &recordingProviderRunner{responses: []providerRunnerResponse{
		{result: runtimeutil.Result{Stdout: installed}},
		{result: runtimeutil.Result{Stdout: root + "\n"}},
		{result: runtimeutil.Result{ExitCode: 1, Stdout: `{"casks":[]}`}},
	}}
	provider := HomebrewProvider{Runner: runner, host: func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "darwin"} }, lookup: func(string, map[string]string) (string, error) { return "/fixture/bin/brew", nil }}
	_, err := provider.Latest(context.Background(), homebrewRequest("cask/sample", path))
	var typed *Error
	if !errors.As(err, &typed) || typed.Key != "provider.homebrew_latest_exit" || typed.Provider != string(model.ProviderHomebrew) || typed.Capability != CapabilityLatest {
		t.Fatalf("error=%#v", err)
	}
	if !slices.Equal(typed.Args, []any{"Sample", 1}) {
		t.Fatalf("args=%#v", typed.Args)
	}
}

func homebrewRequest(packageName, installPath string) Request {
	return Request{App: model.Application{Name: "Sample", Package: packageName, InstallPath: installPath, Provider: model.ProviderConfig{Type: model.ProviderHomebrew}}}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func assertProviderError(t *testing.T, err error, providerType model.ProviderType, capability Capability) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Provider != string(providerType) || typed.Capability != capability {
		t.Fatalf("error=%#v; want provider=%s capability=%s", err, providerType, capability)
	}
}
