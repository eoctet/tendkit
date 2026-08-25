package handler

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type runnerResponse struct {
	result runtimeutil.Result
	err    error
}

func TestAllBuiltinPathDefinitionsUseNormalizedCLIIdentity(t *testing.T) {
	for _, definition := range builtin.PathDefinitions() {
		got := identity(model.Application{Name: definition.Name, Type: model.ApplicationTypeCLI, Provider: model.ProviderConfig{Type: definition.Provider}, Package: definition.Package})
		want := "cli:" + model.NormalizeIdentityName(definition.Name)
		switch definition.Provider {
		case model.ProviderNPM:
			if definition.Package != "" {
				want = model.PackageIdentity("node", definition.Package)
			}
		case model.ProviderPyPI:
			if definition.Package != "" {
				want = model.PackageIdentity("python", definition.Package)
			}
		case model.ProviderGo:
			if definition.Package != "" {
				want = model.PackageIdentity("go", definition.Package)
			}
		}
		if got != want {
			t.Fatalf("%s identity=%q want=%q", definition.ID, got, want)
		}
	}
	for _, test := range []struct {
		app  model.Application
		want string
	}{
		{model.Application{Name: "Git", Provider: model.ProviderConfig{Type: model.ProviderGitHubTag}, Package: "git/git"}, "cli:git"},
		{model.Application{Name: "kubectl", Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Package: "kubernetes/kubernetes"}, "cli:kubectl"},
		{model.Application{Name: "Codex", Provider: model.ProviderConfig{Type: model.ProviderNPM}, Package: "@openai/codex"}, "package:node:openai-codex"},
		{model.Application{Name: "Py", Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Package: "foo.bar"}, "package:python:foobar"},
		{model.Application{Name: "Py", Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Package: "foo_bar"}, "package:python:foobar"},
		{model.Application{Name: "Tool", Provider: model.ProviderConfig{Type: model.ProviderGo}, Package: "github.com/acme/tool/cmd/tool"}, "package:go:tool"},
	} {
		if got := identity(test.app); got != test.want {
			t.Fatalf("PATH identity=%q want=%q", got, test.want)
		}
	}
}

type stubRunner struct {
	responses map[string]runnerResponse
	calls     []string
}

func (r *stubRunner) Run(_ context.Context, script string, _ map[string]string) (runtimeutil.Result, error) {
	r.calls = append(r.calls, script)
	response, ok := r.responses[script]
	if !ok {
		return runtimeutil.Result{}, errors.New("unexpected command: " + script)
	}
	return response.result, response.err
}

func newPathHandler(r Runner, definitions []builtin.PathDefinition, paths map[string]string) *PathHandler {
	handler := NewPath(r, definitions)
	handler.lookPath = func(binary string) (string, error) {
		path, ok := paths[binary]
		if !ok {
			return "", ErrNotFound
		}
		return path, nil
	}
	return handler
}

func TestPathHandlerScanPreservesDefinitionOrderReportsProgressAndSkipsMissing(t *testing.T) {
	definitions := []builtin.PathDefinition{
		{ID: "first", Name: "First", Binary: "first", VersionCommand: "first --version", Provider: model.ProviderDefault},
		{ID: "missing", Name: "Missing", Binary: "missing", VersionCommand: "missing --version", Provider: model.ProviderDefault},
		{ID: "last", Name: "Last", Binary: "last", VersionCommand: "last --version", Provider: model.ProviderDefault},
	}
	runner := &stubRunner{responses: map[string]runnerResponse{
		"first --version": {result: runtimeutil.Result{Stdout: "first version v1.2.3\n"}},
		"last --version":  {result: runtimeutil.Result{Stdout: "last 4.5.6\n"}},
	}}
	handler := newPathHandler(runner, definitions, map[string]string{"first": "/fixture/first", "last": "/fixture/last"})
	var progress []Progress
	result := handler.Scan(context.Background(), Request{Report: func(value Progress) { progress = append(progress, value) }})

	if !result.Complete || result.Err != nil {
		t.Fatalf("unexpected scan result: %#v", result)
	}
	if got := []string{result.Candidates[0].Application.ID, result.Candidates[1].Application.ID}; !reflect.DeepEqual(got, []string{"cli-first", "cli-last"}) {
		t.Fatalf("candidate order = %v, want definition order", got)
	}
	if got := []string{progress[0].Subject, progress[1].Subject, progress[2].Subject}; !reflect.DeepEqual(got, []string{"First", "Missing", "Last"}) {
		t.Fatalf("progress subjects = %v", got)
	}
	for _, value := range progress {
		if value.Stage != model.ScanStageApplication {
			t.Fatalf("progress stage = %q, want %q", value.Stage, model.ScanStageApplication)
		}
	}
	first := result.Candidates[0]
	if first.CurrentVersion != "1.2.3" || first.Application.Description != definitions[0].Description {
		t.Fatalf("first candidate = %#v", first)
	}
	if !reflect.DeepEqual(runner.calls, []string{"first --version", "last --version"}) {
		t.Fatalf("runner calls = %v", runner.calls)
	}
	if result.Candidates[1].Application.Description != definitions[2].Description {
		t.Fatalf("descriptions=%q/%q", result.Candidates[0].Application.Description, result.Candidates[1].Application.Description)
	}
	for _, candidate := range result.Candidates {
		assertActiveProvider(t, candidate.Application.Provider.Type)
	}
}

func TestPathHandlerVersionObservations(t *testing.T) {
	definition := builtin.PathDefinition{ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version", Provider: model.ProviderDefault}
	runnerErr := errors.New("runner failed")
	tests := []struct {
		name     string
		response runnerResponse
		version  string
		wantErr  error
	}{
		{name: "normalizes version", response: runnerResponse{result: runtimeutil.Result{Stderr: "Tool version V2.3.4\n"}}, version: "2.3.4"},
		{name: "runner error", response: runnerResponse{err: runnerErr}, wantErr: runnerErr},
		{name: "nonzero exit", response: runnerResponse{result: runtimeutil.Result{ExitCode: 17}}, wantErr: CommandExitError{ExitCode: 17}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &stubRunner{responses: map[string]runnerResponse{"tool --version": test.response}}
			handler := newPathHandler(runner, []builtin.PathDefinition{definition}, map[string]string{"tool": "/fixture/tool"})
			candidate, found, err := handler.ScanApplication(context.Background(), model.Application{ID: "cli-tool", Type: model.ApplicationTypeCLI}, Request{})
			if err != nil || !found {
				t.Fatalf("found=%t err=%v", found, err)
			}
			if candidate.CurrentVersion != test.version {
				t.Fatalf("version = %q, want %q", candidate.CurrentVersion, test.version)
			}
			if test.wantErr == nil {
				if candidate.ObservationErr != nil {
					t.Fatalf("observation error = %v", candidate.ObservationErr)
				}
				return
			}
			if !errors.Is(candidate.ObservationErr, test.wantErr) {
				t.Fatalf("observation error = %v, want %v", candidate.ObservationErr, test.wantErr)
			}
			if _, ok := test.wantErr.(CommandExitError); ok {
				var exitError CommandExitError
				if !errors.As(candidate.ObservationErr, &exitError) || exitError.ExitCode != 17 {
					t.Fatalf("observation error is not typed CommandExitError: %T %[1]v", candidate.ObservationErr)
				}
			}
		})
	}
}

func TestPathHandlerConfiguresDownloadAndUpdateModes(t *testing.T) {
	tests := []struct {
		name       string
		definition builtin.PathDefinition
		responses  map[string]runnerResponse
		wantMode   model.UpdateMode
		wantUpdate string
		wantDL     bool
	}{
		{
			name:       "download configuration wins",
			definition: builtin.PathDefinition{ID: "download", Name: "Download", Binary: "download", VersionCommand: "download --version", UpdateCommand: "download update", UpdateProbe: "download update --help", Provider: model.ProviderGitHubRelease, DownloadURL: "https://example.invalid/download.tgz", DownloadFilename: "download.tgz"},
			responses:  map[string]runnerResponse{"download --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}},
			wantMode:   model.ModeDownload, wantUpdate: "download update", wantDL: true,
		},
		{
			name:       "successful probe enables auto",
			definition: builtin.PathDefinition{ID: "auto", Name: "Auto", Binary: "auto", VersionCommand: "auto --version", UpdateCommand: "auto update", UpdateProbe: "auto update --help", Provider: model.ProviderDefault},
			responses:  map[string]runnerResponse{"auto --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}, "auto update --help": {result: runtimeutil.Result{}}},
			wantMode:   model.ModeAuto, wantUpdate: "auto update",
		},
		{
			name:       "failed probe clears update",
			definition: builtin.PathDefinition{ID: "check", Name: "Check", Binary: "check", VersionCommand: "check --version", UpdateCommand: "check update", UpdateProbe: "check update --help", Provider: model.ProviderDefault},
			responses:  map[string]runnerResponse{"check --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}, "check update --help": {result: runtimeutil.Result{ExitCode: 1}}},
			wantMode:   model.ModeCheck,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &stubRunner{responses: test.responses}
			handler := newPathHandler(runner, []builtin.PathDefinition{test.definition}, map[string]string{test.definition.Binary: "/fixture/" + test.definition.Binary})
			result := handler.Scan(context.Background(), Request{})
			if len(result.Candidates) != 1 {
				t.Fatalf("candidates = %#v", result.Candidates)
			}
			application := result.Candidates[0].Application
			if application.UpdateMode != test.wantMode || application.Provider.UpdateAction() != test.wantUpdate {
				t.Fatalf("application mode/actions = %#v", application)
			}
			download := application.Provider.DownloadAction()
			if (download != nil) != test.wantDL {
				t.Fatalf("download = %#v, want present=%t", download, test.wantDL)
			}
			if test.wantDL && (download.URL != test.definition.DownloadURL || download.Filename != test.definition.DownloadFilename) {
				t.Fatalf("download = %#v", download)
			}
		})
	}
}

func TestPathHandlerScanApplicationMatchesOnlyCLITargets(t *testing.T) {
	definition := builtin.PathDefinition{ID: "tool-id", Name: "Tool Name", Binary: "tool", VersionCommand: "tool --version", Provider: model.ProviderNPM, Package: "@scope/tool-package"}
	tests := []struct {
		name   string
		target model.Application
		found  bool
		err    error
	}{
		{name: "id", target: model.Application{ID: "cli-tool-id", Type: model.ApplicationTypeCLI}, found: true},
		{name: "name is case insensitive", target: model.Application{Name: "tOoL nAmE", Type: model.ApplicationTypeCLI}, found: true},
		{name: "package is case insensitive", target: model.Application{Package: "@SCOPE/TOOL-PACKAGE", Type: model.ApplicationTypeCLI}, found: true},
		{name: "non CLI target skipped", target: model.Application{ID: "cli-tool-id", Type: model.ApplicationTypeBundle}, found: false},
		{name: "missing target", target: model.Application{ID: "other", Type: model.ApplicationTypeCLI}, found: false, err: ErrNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &stubRunner{responses: map[string]runnerResponse{"tool --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}}}
			handler := newPathHandler(runner, []builtin.PathDefinition{definition}, map[string]string{"tool": "/fixture/tool"})
			_, found, err := handler.ScanApplication(context.Background(), test.target, Request{})
			if found != test.found || !errors.Is(err, test.err) {
				t.Fatalf("found=%t err=%v, want found=%t err=%v", found, err, test.found, test.err)
			}
		})
	}
}

func TestPathHandlerCancellationAndDefinitionIsolation(t *testing.T) {
	definitions := []builtin.PathDefinition{{ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version", Provider: model.ProviderDefault}}
	original := append([]builtin.PathDefinition(nil), definitions...)
	runner := &stubRunner{responses: map[string]runnerResponse{"tool --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}}}
	handler := newPathHandler(runner, definitions, map[string]string{"tool": "/fixture/tool"})
	definitions[0].Name = "Mutated input"
	definitions[0].VersionCommand = "mutated --version"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := handler.Scan(ctx, Request{Report: func(Progress) { t.Fatal("cancelled scan reported progress") }})
	if result.Complete || !errors.Is(result.Err, context.Canceled) || len(result.Candidates) != 0 {
		t.Fatalf("cancelled result = %#v", result)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("cancelled scan ran commands: %v", runner.calls)
	}
	if !reflect.DeepEqual(original, []builtin.PathDefinition{{ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version", Provider: model.ProviderDefault}}) {
		t.Fatalf("test fixture unexpectedly changed: %#v", original)
	}

	result = handler.Scan(context.Background(), Request{})
	if len(result.Candidates) != 1 || result.Candidates[0].Application.Name != "Tool" || !reflect.DeepEqual(runner.calls, []string{"tool --version"}) {
		t.Fatalf("handler retained caller mutation: result=%#v calls=%v", result, runner.calls)
	}
}
