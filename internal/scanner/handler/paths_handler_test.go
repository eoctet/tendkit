package handler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/eoctet/tendkit/internal/scanner/builtin"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"os"

	"reflect"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
)

type runnerResponse struct {
	result runtimeutil.Result
	err    error
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
	handler.lookPaths = func(binary string) ([]string, error) {
		path, ok := paths[binary]
		if !ok {
			return nil, ErrNotFound
		}
		return []string{path}, nil
	}
	return handler
}
func TestPathHandlerContract(t *testing.T) {
	t.Run("all-builtin-path-definitions-use-normalized-cli-identity", func(t *testing.T) {
		for _, definition := range builtin.PathDefinitions() {
			got := identity(model.Application{Name: definition.Name, Type: model.ApplicationTypeCLI, Provider: model.ProviderConfig{Type: definition.Provider}, Package: definition.Package})
			want := "cli:" + model.NormalizeIdentityName(definition.Name)
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
			{model.Application{Name: "Codex", Provider: model.ProviderConfig{Type: model.ProviderNPM}, Package: "@openai/codex"}, "cli:codex"},
			{model.Application{Name: "Py", Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Package: "foo.bar"}, "cli:py"},
			{model.Application{Name: "Py", Provider: model.ProviderConfig{Type: model.ProviderPyPI}, Package: "foo_bar"}, "cli:py"},
			{model.Application{Name: "Tool", Provider: model.ProviderConfig{Type: model.ProviderGo}, Package: "github.com/acme/tool/cmd/tool"}, "cli:tool"},
		} {
			if got := identity(test.app); got != test.want {
				t.Fatalf("PATH identity=%q want=%q", got, test.want)
			}
		}
	})
	t.Run("path-handler-scan-preserves-definition-order-reports-progress-and-skips-missing", func(t *testing.T) {
		definitions := []builtin.PathDefinition{
			{ID: "first", Name: "First", Binary: "first", VersionCommand: "first --version", Provider: model.ProviderDefault},
			{ID: "missing", Name: "Missing", Binary: "missing", VersionCommand: "missing --version", Provider: model.ProviderDefault},
			{ID: "last", Name: "Last", Binary: "last", VersionCommand: "last --version", Provider: model.ProviderDefault},
		}
		runner := &stubRunner{responses: map[string]runnerResponse{
			"/fixture/first --version": {result: runtimeutil.Result{Stdout: "first version v1.2.3\n"}},
			"/fixture/last --version":  {result: runtimeutil.Result{Stdout: "last 4.5.6\n"}},
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
		if !reflect.DeepEqual(runner.calls, []string{"/fixture/first --version", "/fixture/last --version"}) {
			t.Fatalf("runner calls = %v", runner.calls)
		}
		if result.Candidates[1].Application.Description != definitions[2].Description {
			t.Fatalf("descriptions=%q/%q", result.Candidates[0].Application.Description, result.Candidates[1].Application.Description)
		}
		for _, candidate := range result.Candidates {
			assertActiveProvider(t, candidate.Application.Provider.Type)
		}
	})
	t.Run("path-handler-version-observations", func(t *testing.T) {
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
				runner := &stubRunner{responses: map[string]runnerResponse{"/fixture/tool --version": test.response}}
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
	})
	t.Run("path-handler-configures-download-and-update-modes", func(t *testing.T) {
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
				wantMode:   model.ModeDownload, wantUpdate: "/fixture/download update", wantDL: true,
			},
			{
				name:       "successful probe enables auto",
				definition: builtin.PathDefinition{ID: "auto", Name: "Auto", Binary: "auto", VersionCommand: "auto --version", UpdateCommand: "auto update", UpdateProbe: "auto update --help", Provider: model.ProviderDefault},
				responses:  map[string]runnerResponse{"/fixture/auto --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}, "/fixture/auto update --help": {result: runtimeutil.Result{}}},
				wantMode:   model.ModeAuto, wantUpdate: "/fixture/auto update",
			},
			{
				name:       "failed probe clears update",
				definition: builtin.PathDefinition{ID: "check", Name: "Check", Binary: "check", VersionCommand: "check --version", UpdateCommand: "check update", UpdateProbe: "check update --help", Provider: model.ProviderDefault},
				responses:  map[string]runnerResponse{"/fixture/check --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}, "/fixture/check update --help": {result: runtimeutil.Result{ExitCode: 1}}},
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
	})
	t.Run("path-handler-single-instance-binds-all-executable-actions", func(t *testing.T) {
		definition := builtin.PathDefinition{
			ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version",
			CheckCommand: "tool check", UpdateCommand: "tool update", UpdateProbe: "tool update --help",
			Provider: model.ProviderDefault,
		}
		const executable = "/fixture/tool"
		runner := &stubRunner{responses: map[string]runnerResponse{
			executable + " --version":     {result: runtimeutil.Result{Stdout: "1.0.0"}},
			executable + " update --help": {result: runtimeutil.Result{}},
		}}
		handler := newPathHandler(runner, []builtin.PathDefinition{definition}, map[string]string{"tool": executable})
		result := handler.Scan(context.Background(), Request{})
		if result.Err != nil || len(result.Candidates) != 1 {
			t.Fatalf("result = %#v", result)
		}
		app := result.Candidates[0].Application
		if app.Provider.VersionAction() != executable+" --version" || app.Provider.CheckAction() != executable+" check" || app.Provider.UpdateAction() != executable+" update" || app.UpdateMode != model.ModeAuto {
			t.Fatalf("single-instance actions were not uniformly bound: %#v", app.Provider.Actions)
		}
		if !reflect.DeepEqual(runner.calls, []string{executable + " update --help", executable + " --version"}) {
			t.Fatalf("runner calls = %v", runner.calls)
		}
	})
	t.Run("path-handler-scan-application-matches-only-cli-targets", func(t *testing.T) {
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
				runner := &stubRunner{responses: map[string]runnerResponse{"/fixture/tool --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}}}
				handler := newPathHandler(runner, []builtin.PathDefinition{definition}, map[string]string{"tool": "/fixture/tool"})
				_, found, err := handler.ScanApplication(context.Background(), test.target, Request{})
				if found != test.found || !errors.Is(err, test.err) {
					t.Fatalf("found=%t err=%v, want found=%t err=%v", found, err, test.found, test.err)
				}
			})
		}
	})
	t.Run("path-handler-cancellation-and-definition-isolation", func(t *testing.T) {
		definitions := []builtin.PathDefinition{{ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version", Provider: model.ProviderDefault}}
		original := append([]builtin.PathDefinition(nil), definitions...)
		runner := &stubRunner{responses: map[string]runnerResponse{"/fixture/tool --version": {result: runtimeutil.Result{Stdout: "1.0.0"}}}}
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
		if len(result.Candidates) != 1 || result.Candidates[0].Application.Name != "Tool" || !reflect.DeepEqual(runner.calls, []string{"/fixture/tool --version"}) {
			t.Fatalf("handler retained caller mutation: result=%#v calls=%v", result, runner.calls)
		}
	})
	t.Run("look-paths-enumerates-distinct-executables-and-deduplicates-real-paths", func(t *testing.T) {
		root := t.TempDir()
		firstDir := filepath.Join(root, "first")
		secondDir := filepath.Join(root, "second")
		aliasDir := filepath.Join(root, "alias")
		for _, directory := range []string{firstDir, secondDir, aliasDir} {
			if err := os.Mkdir(directory, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		first := filepath.Join(firstDir, "tool")
		second := filepath.Join(secondDir, "tool")
		if err := os.WriteFile(first, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(second, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(first, filepath.Join(aliasDir, "tool")); err != nil {
			t.Fatal(err)
		}

		t.Setenv("PATH", strings.Join([]string{secondDir, aliasDir, firstDir, secondDir}, string(os.PathListSeparator)))
		paths, err := defaultLookPaths("tool")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{filepath.Join(aliasDir, "tool"), second}; !reflect.DeepEqual(paths, want) {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	})
	t.Run("path-handler-scans-every-installation-with-bound-version-command", func(t *testing.T) {
		definition := builtin.PathDefinition{ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version", Provider: model.ProviderDefault}
		first := "/fixture/first/tool"
		second := "/fixture/second/tool"
		runner := &stubRunner{responses: map[string]runnerResponse{
			first + " --version":  {result: runtimeutil.Result{Stdout: "1.0.0"}},
			second + " --version": {result: runtimeutil.Result{Stdout: "2.0.0"}},
		}}
		handler := NewPath(runner, []builtin.PathDefinition{definition})
		handler.lookPaths = func(string) ([]string, error) { return []string{second, first}, nil }

		result := handler.Scan(context.Background(), Request{})
		if result.Err != nil || !result.Complete || len(result.Candidates) != 2 {
			t.Fatalf("result = %#v", result)
		}
		if got := []string{result.Candidates[0].Application.InstallPath, result.Candidates[1].Application.InstallPath}; !reflect.DeepEqual(got, []string{first, second}) {
			t.Fatalf("candidate paths = %v", got)
		}
		if got := []string{result.Candidates[0].CurrentVersion, result.Candidates[1].CurrentVersion}; !reflect.DeepEqual(got, []string{"1.0.0", "2.0.0"}) {
			t.Fatalf("candidate versions = %v", got)
		}
		if want := []string{first + " --version", second + " --version"}; !reflect.DeepEqual(runner.calls, want) {
			t.Fatalf("runner calls = %v, want %v", runner.calls, want)
		}
	})
	t.Run("bind-executable-uses-shell-quoting-and-rejects-unrelated-commands", func(t *testing.T) {
		bound, err := bindExecutable("tool --version", "tool", "/fixture/Tool Bin/tool")
		if err != nil {
			t.Fatal(err)
		}
		if want := "'/fixture/Tool Bin/tool' --version"; bound != want {
			t.Fatalf("bound = %q, want %q", bound, want)
		}
		if _, err := bindExecutable("python3 -m tool", "tool", "/fixture/tool"); err == nil {
			t.Fatal("unrelated command unexpectedly bound")
		}
	})
	t.Run("all-builtin-path-version-commands-can-bind-to-their-executable", func(t *testing.T) {
		for _, definition := range builtin.PathDefinitions() {
			if _, err := bindExecutable(definition.VersionCommand, definition.Binary, "/fixture/"+definition.Binary); err != nil {
				t.Fatalf("%s version command cannot bind: %v", definition.ID, err)
			}
		}
	})
	t.Run("path-handler-extended-instances-drop-unscoped-update-actions", func(t *testing.T) {
		definition := builtin.PathDefinition{
			ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version",
			CheckCommand: "python3 -m tool check", UpdateCommand: "python3 -m tool update", UpdateProbe: "python3 -m tool --help",
			Provider: model.ProviderDefault,
		}
		paths := []string{"/fixture/first/tool", "/fixture/second/tool"}
		runner := &stubRunner{responses: map[string]runnerResponse{
			paths[0] + " --version": {result: runtimeutil.Result{Stdout: "1.0.0"}},
			paths[1] + " --version": {result: runtimeutil.Result{Stdout: "2.0.0"}},
		}}
		handler := NewPath(runner, []builtin.PathDefinition{definition})
		handler.lookPaths = func(string) ([]string, error) { return paths, nil }
		var diagnostics []Diagnostic
		result := handler.Scan(context.Background(), Request{Diagnostic: func(value Diagnostic) {
			diagnostics = append(diagnostics, value)
		}})
		if result.Err != nil || len(result.Candidates) != 2 {
			t.Fatalf("result = %#v", result)
		}
		for _, candidate := range result.Candidates {
			if candidate.Application.Provider.CheckAction() != "" || candidate.Application.Provider.UpdateAction() != "" || candidate.Application.UpdateMode != model.ModeCheck {
				t.Fatalf("unscoped actions retained: %#v", candidate.Application)
			}
		}
		if len(diagnostics) != 6 {
			t.Fatalf("diagnostics = %#v, want three skipped actions for each instance", diagnostics)
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Event != "path_action_binding_skipped" || diagnostic.Subject != definition.ID || diagnostic.Err == nil ||
				!strings.Contains(diagnostic.Detail, "action=") || !strings.Contains(diagnostic.Detail, "path=") {
				t.Fatalf("diagnostic = %#v", diagnostic)
			}
		}
	})
	t.Run("path-handler-extended-instances-bind-scoped-update-actions", func(t *testing.T) {
		definition := builtin.PathDefinition{
			ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version",
			CheckCommand: "tool check", UpdateCommand: "tool update", UpdateProbe: "tool update --help",
			Provider: model.ProviderDefault,
		}
		paths := []string{"/fixture/first/tool", "/fixture/second/tool"}
		responses := map[string]runnerResponse{}
		for index, path := range paths {
			responses[path+" --version"] = runnerResponse{result: runtimeutil.Result{Stdout: string(rune('1'+index)) + ".0.0"}}
			responses[path+" update --help"] = runnerResponse{result: runtimeutil.Result{}}
		}
		runner := &stubRunner{responses: responses}
		handler := NewPath(runner, []builtin.PathDefinition{definition})
		handler.lookPaths = func(string) ([]string, error) { return paths, nil }
		result := handler.Scan(context.Background(), Request{})
		if result.Err != nil || len(result.Candidates) != 2 {
			t.Fatalf("result = %#v", result)
		}
		for _, candidate := range result.Candidates {
			path := candidate.Application.InstallPath
			if candidate.Application.Provider.CheckAction() != path+" check" || candidate.Application.Provider.UpdateAction() != path+" update" || candidate.Application.UpdateMode != model.ModeAuto {
				t.Fatalf("scoped actions not bound: %#v", candidate.Application)
			}
		}
	})
	t.Run("path-handler-scan-application-uses-configured-extended-instance-path", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "Tool Bin", "tool")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		command := "'" + path + "' --version"
		runner := &stubRunner{responses: map[string]runnerResponse{command: {result: runtimeutil.Result{Stdout: "3.4.5"}}}}
		handler := NewPath(runner, []builtin.PathDefinition{{ID: "tool", Name: "Tool", Binary: "tool", VersionCommand: "tool --version", Provider: model.ProviderDefault}})
		handler.lookPaths = func(string) ([]string, error) {
			t.Fatal("configured extended instance fell back to PATH")
			return nil, nil
		}
		app := model.Application{ID: "cli-tool-deadbeefdeadbeef", Name: "Tool", Type: model.ApplicationTypeCLI, InstallPath: path, Identity: "cli:tool@deadbeefdeadbeef"}
		candidate, found, err := handler.ScanApplication(context.Background(), app, Request{})
		if err != nil || !found || candidate.Application.ID != app.ID || candidate.Application.Identity != app.Identity || candidate.CurrentVersion != "3.4.5" {
			t.Fatalf("candidate=%#v found=%t err=%v", candidate, found, err)
		}
	})
}
func TestExpandConfiguredPath(t *testing.T) {
	t.Setenv("PACKAGE_BIN", "bin/tool")
	home := filepath.Join(string(filepath.Separator), "Users", "tester")
	if got := expandConfiguredPath("  ~/$PACKAGE_BIN  ", func() (string, error) { return home, nil }); got != filepath.Join(home, "bin/tool") {
		t.Fatalf("expanded path = %q", got)
	}
	if got := expandConfiguredPath("~/bin/tool", func() (string, error) { return "", errors.New("missing home") }); got != "~/bin/tool" {
		t.Fatalf("path changed after home lookup failure: %q", got)
	}
}
