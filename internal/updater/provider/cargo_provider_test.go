package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestCargoCapabilityMatrix(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry, nil, nil); err != nil {
		t.Fatal(err)
	}
	capabilities, ok := registry.Resolve("cargo")
	if !ok {
		t.Fatal("cargo was not registered")
	}
	if capabilities.Current == nil || capabilities.Latest != nil || capabilities.Update != nil || capabilities.Download != nil || capabilities.Install != nil || capabilities.Checksum != nil || capabilities.Artifact != nil {
		t.Fatalf("cargo capabilities = %#v", capabilities)
	}
}

func TestRegistryCargoCurrentUsesOnlyStableInstallList(t *testing.T) {
	root, path := cargoFixture(t)
	bin := t.TempDir()
	manager := filepath.Join(bin, "cargo")
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2 $3 $4\" = \"install --list --root " + root + "\" ]; then printf 'sample v1.2.3:\\n    sample\\n'; exit 0; fi\n" +
		"exit 91\n"
	if err := os.WriteFile(manager, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := RegisterBuiltins(registry, nil, nil, runtimeutil.Runner{}); err != nil {
		t.Fatal(err)
	}
	capabilities, ok := registry.Resolve(string(model.ProviderCargo))
	if !ok || capabilities.Current == nil || capabilities.Latest != nil {
		t.Fatalf("cargo capabilities=%#v", capabilities)
	}
	request := cargoRequest(root, path)
	request.App.Environment["PATH"] = bin
	current, err := capabilities.Current.Current(context.Background(), request)
	if err != nil || current != "1.2.3" {
		t.Fatalf("current=%q error=%v", current, err)
	}
}

func TestCargoCurrentUsesAbsoluteManagerWithoutConfigGetOrInfo(t *testing.T) {
	root, path := cargoFixture(t)
	runner := &recordingProviderRunner{responses: cargoCurrentResponses()}
	current, err := cargoProvider(runner).Current(context.Background(), cargoRequest(root, path))
	if err != nil || current != "1.2.3" {
		t.Fatalf("current=%q error=%v", current, err)
	}
	want := []string{"/fixture/bin/cargo install --list --root " + runtimeutil.QuoteShell(root)}
	if !slices.Equal(runner.commands, want) {
		t.Fatalf("commands=%q want=%q", runner.commands, want)
	}
}

func TestCargoCurrentRunnerFailuresRemainTyped(t *testing.T) {
	root, path := cargoFixture(t)
	sentinel := errors.New("runner failed")
	for _, failure := range runnerFailures(sentinel) {
		t.Run(failure.name, func(t *testing.T) {
			responses := cargoCurrentResponses()
			failure.set(&responses[0])
			runner := &recordingProviderRunner{responses: responses}
			_, err := cargoProvider(runner).Current(failure.ctx(), cargoRequest(root, path))
			assertProviderError(t, err, model.ProviderCargo, CapabilityCurrent)
			if failure.cause != nil && !errors.Is(err, failure.cause) {
				t.Fatalf("error=%v does not wrap %v", err, failure.cause)
			}
			if len(runner.commands) != 1 {
				t.Fatalf("commands=%q", runner.commands)
			}
		})
	}
}

func TestCargoCurrentExitIncludesExitCode(t *testing.T) {
	root, path := cargoFixture(t)
	runner := &recordingProviderRunner{responses: []providerRunnerResponse{{result: runtimeutil.Result{ExitCode: 23}}}}
	_, err := cargoProvider(runner).Current(context.Background(), cargoRequest(root, path))
	var typed *Error
	if !errors.As(err, &typed) || typed.Key != "provider.cargo_current_exit" {
		t.Fatalf("error=%#v", err)
	}
	if !slices.Equal(typed.Args, []any{"Sample", 23}) {
		t.Fatalf("args=%#v", typed.Args)
	}
}

func TestCargoCurrentFailsClosedForWrongBinaryOrRoot(t *testing.T) {
	root, path := cargoFixture(t)
	tests := []struct {
		name     string
		path     string
		root     string
		response providerRunnerResponse
	}{
		{"wrong binary", path, root, providerRunnerResponse{result: runtimeutil.Result{Stdout: "sample v1.2.3:\n    other\n"}}},
		{"outside root", filepath.Join(t.TempDir(), "sample"), root, cargoCurrentResponses()[0]},
		{"inside root but outside bin", filepath.Join(root, "tools", "sample"), root, cargoCurrentResponses()[0]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(test.path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(test.path, []byte("fixture"), 0o755); err != nil {
				t.Fatal(err)
			}
			runner := &recordingProviderRunner{responses: []providerRunnerResponse{test.response}}
			_, err := cargoProvider(runner).Current(context.Background(), cargoRequest(test.root, test.path))
			var typed *Error
			if !errors.As(err, &typed) || typed.Key != "provider.target_conflict" || typed.Provider != string(model.ProviderCargo) || typed.Capability != CapabilityCurrent {
				t.Fatalf("error=%#v", err)
			}
			if len(runner.commands) != 1 {
				t.Fatalf("commands=%q", runner.commands)
			}
		})
	}
}

func TestCargoCurrentFailsClosedWhenAnyListedBinaryIsMissing(t *testing.T) {
	root, path := cargoFixture(t)
	runner := &recordingProviderRunner{responses: []providerRunnerResponse{{result: runtimeutil.Result{Stdout: "sample v1.2.3:\n    sample\n    missing\n"}}}}
	_, err := cargoProvider(runner).Current(context.Background(), cargoRequest(root, path))
	var typed *Error
	if !errors.As(err, &typed) || typed.Key != "provider.target_conflict" || typed.Provider != string(model.ProviderCargo) || typed.Capability != CapabilityCurrent {
		t.Fatalf("error=%#v", err)
	}
}

func TestCargoInstallRootUsesOnlyKnownEnvironmentOrDefaults(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("CARGO_INSTALL_ROOT", "/process/install-root")
	t.Setenv("CARGO_HOME", "/process/cargo-home")
	provider := CargoProvider{cwd: func() (string, error) { return cwd, nil }, homeDir: func() (string, error) { return home, nil }}
	tests := []struct {
		name        string
		environment map[string]string
		want        string
	}{
		{"application install root", map[string]string{"CARGO_INSTALL_ROOT": "/app/install-root"}, "/app/install-root"},
		{"process install root", nil, "/process/install-root"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := provider.installRoot(Request{App: model.Application{Environment: test.environment}})
			if err != nil || got != test.want {
				t.Fatalf("root=%q error=%v want=%q", got, err, test.want)
			}
		})
	}
	t.Setenv("CARGO_INSTALL_ROOT", "")
	appCargoHome := filepath.Join(cwd, "app-cargo-home")
	root, err := provider.installRoot(Request{App: model.Application{Environment: map[string]string{"CARGO_HOME": appCargoHome}}})
	if err != nil || root != appCargoHome {
		t.Fatalf("application CARGO_HOME root=%q error=%v", root, err)
	}
	root, err = provider.installRoot(Request{})
	if err != nil || root != "/process/cargo-home" {
		t.Fatalf("CARGO_HOME root=%q error=%v", root, err)
	}
	t.Setenv("CARGO_HOME", "")
	root, err = provider.installRoot(Request{})
	if err != nil || root != filepath.Join(home, ".cargo") {
		t.Fatalf("default root=%q error=%v", root, err)
	}
}

func TestCargoCurrentPassesApplicationCargoHomeAsExplicitRoot(t *testing.T) {
	workspace := t.TempDir()
	appCargoHome := filepath.Join(workspace, "app-cargo-home")
	path := filepath.Join(appCargoHome, "bin", "sample")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARGO_INSTALL_ROOT", "")
	t.Setenv("CARGO_HOME", filepath.Join(workspace, "process-cargo-home"))
	runner := &recordingProviderRunner{responses: cargoCurrentResponses()}
	request := cargoRequest("", path)
	request.App.Environment = map[string]string{"CARGO_HOME": appCargoHome}
	current, err := cargoProvider(runner).Current(context.Background(), request)
	if err != nil || current != "1.2.3" {
		t.Fatalf("current=%q error=%v", current, err)
	}
	want := []string{"/fixture/bin/cargo install --list --root " + runtimeutil.QuoteShell(appCargoHome)}
	if !slices.Equal(runner.commands, want) {
		t.Fatalf("commands=%q want=%q", runner.commands, want)
	}
}

func TestCargoProviderUsesCargoHomeConfigRootAndIgnoresProjectConfig(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "project")
	projectConfigDir := filepath.Join(project, ".cargo")
	cargoHome := filepath.Join(workspace, "cargo-home")
	root := filepath.Join(workspace, "install-root")
	path := filepath.Join(root, "bin", "sample")
	if err := os.MkdirAll(projectConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cargoHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectConfigDir, "config.toml"), []byte("[install]\nroot = \"ignored-project-root\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte("[install]\nroot = \"install-root\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARGO_INSTALL_ROOT", "")
	t.Setenv("CARGO_HOME", cargoHome)
	runner := &recordingProviderRunner{responses: cargoCurrentResponses()}
	provider := cargoProvider(runner)
	provider.cwd = func() (string, error) { return project, nil }
	provider.homeDir = func() (string, error) { return workspace, nil }
	request := cargoRequest("", path)
	delete(request.App.Environment, "CARGO_INSTALL_ROOT")
	current, err := provider.Current(context.Background(), request)
	if err != nil || current != "1.2.3" {
		t.Fatalf("current=%q error=%v", current, err)
	}
	if !slices.Equal(runner.commands, []string{"/fixture/bin/cargo install --list --root " + runtimeutil.QuoteShell(root)}) {
		t.Fatalf("commands=%q", runner.commands)
	}
}

type runnerFailure struct {
	name  string
	set   func(*providerRunnerResponse)
	ctx   func() context.Context
	cause error
}

func runnerFailures(sentinel error) []runnerFailure {
	return []runnerFailure{
		{"runner error", func(r *providerRunnerResponse) { r.err = sentinel }, context.Background, sentinel},
		{"nonzero", func(r *providerRunnerResponse) { r.result = runtimeutil.Result{ExitCode: 17, Stderr: "failed"} }, context.Background, nil},
		{"cancellation", func(r *providerRunnerResponse) { r.err = context.Canceled }, canceledContext, context.Canceled},
	}
}

func cargoFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "bin", "sample")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, path
}

func cargoCurrentResponses() []providerRunnerResponse {
	return []providerRunnerResponse{{result: runtimeutil.Result{Stdout: "sample v1.2.3:\n    sample\n"}}}
}

func cargoProvider(runner commandRunner) CargoProvider {
	return CargoProvider{Runner: runner, lookup: func(name string, _ map[string]string) (string, error) {
		if name != "cargo" {
			return "", fmt.Errorf("unexpected manager %q", name)
		}
		return "/fixture/bin/cargo", nil
	}}
}

func cargoRequest(root, path string) Request {
	return Request{App: model.Application{Name: "Sample", Package: "sample", InstallPath: path, Environment: map[string]string{"CARGO_INSTALL_ROOT": root}, Provider: model.ProviderConfig{Type: model.ProviderCargo}}}
}
