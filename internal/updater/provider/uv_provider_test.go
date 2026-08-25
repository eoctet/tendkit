package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type uvLatestRunner struct {
	command string
	env     map[string]string
	result  runtimeutil.Result
	err     error
}

func (r *uvLatestRunner) Run(_ context.Context, command string, environment map[string]string) (runtimeutil.Result, error) {
	r.command, r.env = command, environment
	return r.result, r.err
}

func TestUVProviderLatestUsesUVContextAndParsesFixtures(t *testing.T) {
	directory := t.TempDir()
	uv := filepath.Join(directory, "uv")
	if err := os.WriteFile(uv, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	runner := &uvLatestRunner{result: runtimeutil.Result{Stdout: "ruff v0.8.0 [required: >=0.7] [latest: 0.9.1]\n- ruff\nother v1.0.0 [latest: 1.1.0]\n"}}
	app := model.Application{Name: "Ruff", Type: model.ApplicationTypePackage, Package: "ruff", Identity: "package:uv:ruff", Environment: map[string]string{"UV_INDEX": "https://private.invalid/simple"}, Provider: model.ProviderConfig{Type: model.ProviderUV}}
	latest, err := (UVProvider{Runner: runner}).Latest(context.Background(), Request{App: app, CurrentVersion: "0.8.0"})
	if err != nil || latest != "0.9.1" {
		t.Fatalf("Latest() = %q, %v", latest, err)
	}
	if runner.command != uv+" tool list --outdated --show-version-specifiers --no-progress" || runner.env["UV_INDEX"] == "" {
		t.Fatalf("command/environment = %q %#v", runner.command, runner.env)
	}
}

func TestUVProviderAcceptsComparableVersions(t *testing.T) {
	for _, value := range []string{"1.0", "1.2.3-rc.1", "v2.0.0+local"} {
		if normalized, valid := normalizeUVVersion(value); !valid || normalized != strings.TrimPrefix(value, "v") {
			t.Fatalf("normalizeUVVersion(%q) = %q, %t", value, normalized, valid)
		}
	}
	for _, value := range []string{"9.9-...", "nonsense", "prefix1.0", "1.0 trailing"} {
		if normalized, valid := normalizeUVVersion(value); valid || normalized != "" {
			t.Fatalf("normalizeUVVersion(%q) = %q, %t", value, normalized, valid)
		}
	}
}

func TestUVProviderFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, output string
		result       runtimeutil.Result
		err          error
	}{
		{"malformed", "ruff v0.8.0 [latest:]", runtimeutil.Result{Stdout: "ruff v0.8.0 [latest:]"}, nil},
		{"garbage request current", "", runtimeutil.Result{}, nil},
		{"garbage line current", "", runtimeutil.Result{Stdout: "ruff vnot-a-version [latest: 0.9.0]"}, nil},
		{"garbage latest", "", runtimeutil.Result{Stdout: "ruff v0.8.0 [latest: nonsense]"}, nil},
		{"duplicate", "", runtimeutil.Result{Stdout: "ruff v0.8.0 [latest: 0.9.0]\nruff v0.8.0 [latest: 0.9.1]"}, nil},
		{"exit", "", runtimeutil.Result{ExitCode: 2, Stdout: "https://private.invalid/token", Stderr: "secret-token"}, nil},
		{"runner", "", runtimeutil.Result{}, errors.New("runner failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			uv := filepath.Join(directory, "uv")
			if err := os.WriteFile(uv, []byte("#!/bin/sh\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory)
			current := "0.8.0"
			if test.name == "garbage request current" {
				current = "nonsense"
			}
			_, err := (UVProvider{Runner: &uvLatestRunner{result: test.result, err: test.err}}).Latest(context.Background(), Request{App: model.Application{Name: "ruff", Type: model.ApplicationTypePackage, Package: "ruff", Identity: "package:uv:ruff"}, CurrentVersion: current})
			if err == nil || !strings.Contains(err.Error(), "provider.uv_") {
				t.Fatalf("Latest error = %v", err)
			}
			if test.name == "exit" && (strings.Contains(err.Error(), "private.invalid") || strings.Contains(err.Error(), "secret-token")) {
				t.Fatalf("UV exit leaked output: %v", err)
			}
		})
	}
}

func TestUVProviderIgnoresIdentityAndPreservesFailureCauses(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "uv"), []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	for _, identity := range []string{"", "package:python:other"} {
		app := model.Application{Name: "ruff", Type: model.ApplicationTypePackage, Package: "ruff", Identity: identity, Provider: model.ProviderConfig{Type: model.ProviderUV}}
		latest, err := (UVProvider{Runner: &uvLatestRunner{result: runtimeutil.Result{}}}).Latest(context.Background(), Request{App: app, CurrentVersion: "0.8.0"})
		if err != nil || latest != "0.8.0" {
			t.Fatalf("identity %q changed UV routing: Latest() = %q, %v", identity, latest, err)
		}
	}
	for _, app := range []model.Application{
		{Name: "missing package", Type: model.ApplicationTypePackage, Provider: model.ProviderConfig{Type: model.ProviderUV}},
		{Name: "dot package", Type: model.ApplicationTypePackage, Package: ".", Provider: model.ProviderConfig{Type: model.ProviderUV}},
		{Name: "wrong type", Type: model.ApplicationTypeCLI, Package: "ruff", Provider: model.ProviderConfig{Type: model.ProviderUV}},
	} {
		if _, err := (UVProvider{}).Latest(context.Background(), Request{App: app, CurrentVersion: "0.8.0"}); err == nil {
			t.Fatalf("invalid UV target was accepted: %#v", app)
		}
	}

	for _, test := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"cancelled", func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }(), context.Canceled},
		{"deadline", func() context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			return ctx
		}(), context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &uvLatestRunner{err: test.err}
			_, err := (UVProvider{Runner: runner}).Latest(test.ctx, Request{App: model.Application{Name: "ruff", Type: model.ApplicationTypePackage, Package: "ruff", Identity: "package:uv:ruff"}, CurrentVersion: "0.8.0"})
			if !errors.Is(err, test.err) {
				t.Fatalf("Latest() error = %v, want cause %v", err, test.err)
			}
		})
	}

	var err error
	t.Setenv("PATH", t.TempDir())
	_, err = (UVProvider{}).Latest(context.Background(), Request{App: model.Application{Name: "ruff", Type: model.ApplicationTypePackage, Package: "ruff", Identity: "package:uv:ruff"}, CurrentVersion: "0.8.0"})
	if err == nil || !strings.Contains(err.Error(), "provider.uv_manager_unavailable") {
		t.Fatalf("manager error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (UVProvider{}).Latest(ctx, Request{App: model.Application{Name: "ruff", Type: model.ApplicationTypePackage, Package: "ruff", Identity: "package:uv:ruff"}, CurrentVersion: "0.8.0"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled manager lookup error = %v", err)
	}
}
