package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type noCommandRunner struct{}

func (noCommandRunner) Run(context.Context, string, map[string]string) (runtimeutil.Result, error) {
	return runtimeutil.Result{}, errors.New("command must not run")
}

type scriptedRunner struct {
	responses []runtimeutil.Result
	commands  []string
	index     int
	afterRun  func(int)
}

func (r *scriptedRunner) Run(_ context.Context, command string, _ map[string]string) (runtimeutil.Result, error) {
	r.commands = append(r.commands, command)
	result := r.responses[r.index]
	r.index++
	if r.afterRun != nil {
		r.afterRun(r.index)
	}
	return result, nil
}

func TestHomebrewCaskIsNotApplicableOffDarwin(t *testing.T) {
	handler := NewHomebrewCask(noCommandRunner{})
	handler.host = func() string { return "linux" }
	result := handler.Scan(context.Background(), Request{})
	if !result.Complete || result.Err != nil || len(result.Candidates) != 0 {
		t.Fatalf("not-applicable cask result=%#v", result)
	}
}

func TestHomebrewFormulaUsesFastListCellarReceiptsAndKegWalk(t *testing.T) {
	cellar := t.TempDir()
	rack := filepath.Join(cellar, "sample")
	inactive := filepath.Join(rack, "1.2.3")
	root := filepath.Join(rack, "2.0.0")
	first := filepath.Join(root, "bin", "a tool")
	second := filepath.Join(root, "libexec", "z-tool")
	nonExecutable := filepath.Join(root, "share", "metadata.txt")
	for _, path := range []string{first, second, nonExecutable} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(first, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nonExecutable, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeHomebrewFormulaReceipt(t, inactive, false, "homebrew/core")
	writeHomebrewFormulaReceipt(t, root, true, "homebrew/core")
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	realFirst, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	realSecond, err := filepath.EvalSymlinks(second)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{responses: []runtimeutil.Result{
		{Stdout: `{"formulae":[{"name":"sample","versions":["1.2.3","2.0.0"],"linked_version":"2.0.0","optlinked_version":"","pinned_version":"2.0.0"}],"casks":[]}`},
		{Stdout: cellar + "\n"},
	}}
	handler := NewHomebrewFormula(runner)
	handler.lookPath = func(string) (string, error) { return "/opt/homebrew/bin/brew", nil }
	result := handler.Scan(context.Background(), Request{})
	if !result.Complete || len(result.Candidates) != 1 {
		t.Fatalf("result=%#v", result)
	}
	candidate := result.Candidates[0]
	if candidate.Application.UpdateMode != model.ModeCheck || candidate.CurrentVersion != "2.0.0" || candidate.Application.InstallPath != realFirst {
		t.Fatalf("candidate=%#v", candidate)
	}
	if candidate.Evidence == nil || candidate.Evidence.InstallRoot != realRoot || !slices.Equal(candidate.Evidence.ExecutablePaths, []string{realFirst, realSecond}) {
		t.Fatalf("evidence=%#v", candidate.Evidence)
	}
	wantCommands := []string{
		"/opt/homebrew/bin/brew list --formula --versions --json",
		"/opt/homebrew/bin/brew --cellar",
	}
	if !slices.Equal(runner.commands, wantCommands) {
		t.Fatalf("commands=%q want=%q", runner.commands, wantCommands)
	}
}

func writeHomebrewFormulaReceipt(t *testing.T, prefix string, installedOnRequest bool, tap string) {
	t.Helper()
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	value := `{"installed_on_request":` + map[bool]string{true: "true", false: "false"}[installedOnRequest] + `,"source":{"tap":` + strconv.Quote(tap) + `}}`
	if err := os.WriteFile(filepath.Join(prefix, "INSTALL_RECEIPT.json"), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func homebrewFormulaTestRunner(inventory, cellar string) *scriptedRunner {
	return &scriptedRunner{responses: []runtimeutil.Result{
		{Stdout: inventory},
		{Stdout: cellar + "\n"},
	}}
}

func homebrewFormulaTestHandler(runner Runner) *HomebrewFormulaHandler {
	handler := NewHomebrewFormula(runner)
	handler.lookPath = func(string) (string, error) { return "/opt/homebrew/bin/brew", nil }
	return handler
}

func writeHomebrewFormulaExecutable(t *testing.T, prefix, name string) string {
	t.Helper()
	path := filepath.Join(prefix, "bin", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestHomebrewFormulaDependencyReceiptIsSkipped(t *testing.T) {
	cellar := t.TempDir()
	prefix := filepath.Join(cellar, "dependency", "1.0.0")
	writeHomebrewFormulaReceipt(t, prefix, false, "homebrew/core")
	runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"dependency","versions":["1.0.0"],"linked_version":"1.0.0"}],"casks":[]}`, cellar)

	result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

	if !result.Complete || result.Err != nil || len(result.Candidates) != 0 {
		t.Fatalf("dependency result=%#v", result)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("commands=%q", runner.commands)
	}
}

func TestHomebrewFormulaInvalidActiveVersionFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		inventory string
	}{
		{name: "multiple versions without active pointer", inventory: `{"formulae":[{"name":"sample","versions":["1.0.0","2.0.0"]}],"casks":[]}`},
		{name: "linked version is not installed", inventory: `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"2.0.0"}],"casks":[]}`},
		{name: "optlinked version is not installed", inventory: `{"formulae":[{"name":"sample","versions":["1.0.0"],"optlinked_version":"2.0.0"}],"casks":[]}`},
		{name: "linked valid but optlinked is not installed", inventory: `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0","optlinked_version":"2.0.0"}],"casks":[]}`},
		{name: "linked valid but optlinked is invalid", inventory: `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0","optlinked_version":"../1.0.0"}],"casks":[]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cellar := t.TempDir()
			runner := homebrewFormulaTestRunner(test.inventory, cellar)

			result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

			if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
				t.Fatalf("invalid active version result=%#v", result)
			}
		})
	}
}

func TestHomebrewFormulaFastInventoryRequiresFormulaeArray(t *testing.T) {
	tests := []struct {
		name      string
		inventory string
	}{
		{name: "empty object", inventory: `{}`},
		{name: "missing formulae", inventory: `{"casks":[]}`},
		{name: "null formulae", inventory: `{"formulae":null,"casks":[]}`},
		{name: "formulae wrong type", inventory: `{"formulae":{},"casks":[]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cellar := t.TempDir()
			runner := homebrewFormulaTestRunner(test.inventory, cellar)

			result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

			if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
				t.Fatalf("invalid fast inventory result=%#v", result)
			}
		})
	}
}

func TestHomebrewFormulaValidLinkedVersionDoesNotHideInvalidOptlinkedVersion(t *testing.T) {
	tests := []struct {
		name             string
		optlinkedVersion string
	}{
		{name: "not installed", optlinkedVersion: "2.0.0"},
		{name: "invalid component", optlinkedVersion: "../1.0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cellar := t.TempDir()
			prefix := filepath.Join(cellar, "sample", "1.0.0")
			writeHomebrewFormulaExecutable(t, prefix, "sample")
			writeHomebrewFormulaReceipt(t, prefix, true, "homebrew/core")
			inventory := `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0","optlinked_version":` + strconv.Quote(test.optlinkedVersion) + `}],"casks":[]}`
			runner := homebrewFormulaTestRunner(inventory, cellar)

			result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

			if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
				t.Fatalf("invalid optlinked version result=%#v", result)
			}
		})
	}
}

func TestHomebrewFormulaInvalidReceiptFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		receipt string
	}{
		{name: "damaged", receipt: `{`},
		{name: "missing installed on request", receipt: `{"source":{"tap":"homebrew/core"}}`},
		{name: "missing source", receipt: `{"installed_on_request":true}`},
		{name: "missing tap", receipt: `{"installed_on_request":true,"source":{}}`},
		{name: "empty tap", receipt: `{"installed_on_request":true,"source":{"tap":""}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cellar := t.TempDir()
			prefix := filepath.Join(cellar, "sample", "1.0.0")
			writeHomebrewFormulaExecutable(t, prefix, "sample")
			if err := os.WriteFile(filepath.Join(prefix, "INSTALL_RECEIPT.json"), []byte(test.receipt), 0o644); err != nil {
				t.Fatal(err)
			}
			runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0"}],"casks":[]}`, cellar)

			result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

			if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
				t.Fatalf("invalid receipt result=%#v", result)
			}
		})
	}
}

func TestHomebrewFormulaInvalidPinnedVersionFailsClosed(t *testing.T) {
	tests := []struct {
		name          string
		pinnedVersion string
	}{
		{name: "not installed", pinnedVersion: "2.0.0"},
		{name: "invalid component", pinnedVersion: "../1.0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cellar := t.TempDir()
			prefix := filepath.Join(cellar, "sample", "1.0.0")
			writeHomebrewFormulaExecutable(t, prefix, "sample")
			writeHomebrewFormulaReceipt(t, prefix, true, "homebrew/core")
			inventory := `{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0","pinned_version":` + strconv.Quote(test.pinnedVersion) + `}],"casks":[]}`
			runner := homebrewFormulaTestRunner(inventory, cellar)

			result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

			if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
				t.Fatalf("invalid pinned version result=%#v", result)
			}
		})
	}
}

func TestHomebrewFormulaExecutableSymlinkOutsideKegDoesNotFormEvidence(t *testing.T) {
	cellar := t.TempDir()
	prefix := filepath.Join(cellar, "sample", "1.0.0")
	writeHomebrewFormulaReceipt(t, prefix, true, "homebrew/core")
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(prefix, "bin", "sample")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0"}],"casks":[]}`, cellar)

	result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

	if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
		t.Fatalf("escaping symlink result=%#v", result)
	}
}

func TestHomebrewFormulaDirectorySymlinkIsNotFollowed(t *testing.T) {
	cellar := t.TempDir()
	prefix := filepath.Join(cellar, "sample", "1.0.0")
	writeHomebrewFormulaReceipt(t, prefix, true, "homebrew/core")
	externalDir := t.TempDir()
	writeHomebrewFormulaExecutable(t, externalDir, "external")
	if err := os.Symlink(filepath.Join(externalDir, "bin"), filepath.Join(prefix, "linked-bin")); err != nil {
		t.Fatal(err)
	}
	runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0"}],"casks":[]}`, cellar)

	result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

	if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
		t.Fatalf("directory symlink result=%#v", result)
	}
}

func TestHomebrewFormulaFilesystemSymlinkEscapeFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "rack",
			setup: func(t *testing.T, cellar string) {
				externalRack := filepath.Join(t.TempDir(), "sample")
				prefix := filepath.Join(externalRack, "1.0.0")
				writeHomebrewFormulaExecutable(t, prefix, "sample")
				writeHomebrewFormulaReceipt(t, prefix, true, "homebrew/core")
				if err := os.Symlink(externalRack, filepath.Join(cellar, "sample")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "version",
			setup: func(t *testing.T, cellar string) {
				rack := filepath.Join(cellar, "sample")
				if err := os.MkdirAll(rack, 0o755); err != nil {
					t.Fatal(err)
				}
				externalPrefix := filepath.Join(t.TempDir(), "1.0.0")
				writeHomebrewFormulaExecutable(t, externalPrefix, "sample")
				writeHomebrewFormulaReceipt(t, externalPrefix, true, "homebrew/core")
				if err := os.Symlink(externalPrefix, filepath.Join(rack, "1.0.0")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "receipt",
			setup: func(t *testing.T, cellar string) {
				prefix := filepath.Join(cellar, "sample", "1.0.0")
				writeHomebrewFormulaExecutable(t, prefix, "sample")
				externalReceipt := filepath.Join(t.TempDir(), "INSTALL_RECEIPT.json")
				if err := os.WriteFile(externalReceipt, []byte(`{"installed_on_request":true,"source":{"tap":"homebrew/core"}}`), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(externalReceipt, filepath.Join(prefix, "INSTALL_RECEIPT.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cellar := t.TempDir()
			test.setup(t, cellar)
			runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0"}],"casks":[]}`, cellar)

			result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

			if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
				t.Fatalf("escaping %s result=%#v", test.name, result)
			}
		})
	}
}

func TestHomebrewFormulaLateFailureDiscardsEarlierCandidate(t *testing.T) {
	cellar := t.TempDir()
	prefix := filepath.Join(cellar, "first", "1.0.0")
	writeHomebrewFormulaExecutable(t, prefix, "first")
	writeHomebrewFormulaReceipt(t, prefix, true, "homebrew/core")
	runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"first","versions":["1.0.0"],"linked_version":"1.0.0"},{"name":"second","versions":["1.0.0","2.0.0"]}],"casks":[]}`, cellar)

	result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

	if result.Complete || result.Err == nil || len(result.Candidates) != 0 {
		t.Fatalf("late failure result=%#v", result)
	}
}

func TestHomebrewFormulaCancellationAfterRunnerFailsClosed(t *testing.T) {
	cellar := t.TempDir()
	prefix := filepath.Join(cellar, "sample", "1.0.0")
	writeHomebrewFormulaExecutable(t, prefix, "sample")
	writeHomebrewFormulaReceipt(t, prefix, true, "homebrew/core")
	ctx, cancel := context.WithCancel(context.Background())
	runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0"}],"casks":[]}`, cellar)
	runner.afterRun = func(count int) {
		if count == 2 {
			cancel()
		}
	}

	result := homebrewFormulaTestHandler(runner).Scan(ctx, Request{})

	if result.Complete || !errors.Is(result.Err, context.Canceled) || len(result.Candidates) != 0 {
		t.Fatalf("runner cancellation result=%#v", result)
	}
}

func TestHomebrewFormulaCancellationDuringWalkFailsClosed(t *testing.T) {
	cellar := t.TempDir()
	prefix := filepath.Join(cellar, "sample", "1.0.0")
	writeHomebrewFormulaExecutable(t, prefix, "a")
	writeHomebrewFormulaExecutable(t, prefix, "b")
	writeHomebrewFormulaReceipt(t, prefix, true, "homebrew/core")
	ctx, cancel := context.WithCancel(context.Background())
	runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"sample","versions":["1.0.0"],"linked_version":"1.0.0"}],"casks":[]}`, cellar)
	handler := homebrewFormulaTestHandler(runner)
	handler.stat = func(path string) (os.FileInfo, error) {
		info, err := os.Stat(path)
		if err == nil && filepath.Base(path) == "a" {
			cancel()
		}
		return info, err
	}

	result := handler.Scan(ctx, Request{})

	if result.Complete || !errors.Is(result.Err, context.Canceled) || len(result.Candidates) != 0 {
		t.Fatalf("walk cancellation result=%#v", result)
	}
}

func TestHomebrewFormulaThirdPartyTapUsesCanonicalName(t *testing.T) {
	cellar := t.TempDir()
	prefix := filepath.Join(cellar, "sample", "1.0.0")
	executable := writeHomebrewFormulaExecutable(t, prefix, "sample")
	writeHomebrewFormulaReceipt(t, prefix, true, "user/tools")
	runner := homebrewFormulaTestRunner(`{"formulae":[{"name":"sample","versions":["1.0.0"],"optlinked_version":"1.0.0"}],"casks":[]}`, cellar)

	result := homebrewFormulaTestHandler(runner).Scan(context.Background(), Request{})

	if !result.Complete || result.Err != nil || len(result.Candidates) != 1 {
		t.Fatalf("third-party result=%#v", result)
	}
	app := result.Candidates[0].Application
	if app.Name != "user/tools/sample" || app.Package != "formula/user/tools/sample" || app.Identity != model.PackageIdentity("homebrew-formula", "user/tools/sample") || app.InstallPath != executable {
		t.Fatalf("third-party application=%#v", app)
	}
}

func TestHomebrewCaskMultipleApplicationsRemainCompleteAndDoNotBlockOtherCasks(t *testing.T) {
	root := t.TempDir()
	applications := filepath.Join(t.TempDir(), "Applications")
	first := filepath.Join(applications, "First.app")
	second := filepath.Join(applications, "Second.app")
	single := filepath.Join(applications, "Single.app")
	for _, path := range []string{first, second, single} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(root, "multi", "1.0", "First.app"):   first,
		filepath.Join(root, "multi", "1.0", "Second.app"):  second,
		filepath.Join(root, "single", "2.0", "Single.app"): single,
	} {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		token string
		apps  string
	}{{"multi", `[{"app":["First.app"]},{"app":["Second.app"]}]`}, {"single", `[{"app":["Single.app"]}]`}} {
		receipt := filepath.Join(root, item.token, ".metadata", "INSTALL_RECEIPT.json")
		if err := os.MkdirAll(filepath.Dir(receipt), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receipt, []byte(`{"source":{"tap":"homebrew/cask"},"uninstall_artifacts":`+item.apps+`}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	realFirst, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	realSecond, err := filepath.EvalSymlinks(second)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{responses: []runtimeutil.Result{{Stdout: `{"formulae":[],"casks":[{"token":"multi","versions":["1.0"],"pinned_version":null},{"token":"single","versions":["2.0"],"pinned_version":null}]}`}, {Stdout: root + "\n"}}}
	handler := NewHomebrewCask(runner)
	handler.host = func() string { return "darwin" }
	handler.lookPath = func(string) (string, error) { return "/opt/homebrew/bin/brew", nil }
	result := handler.Scan(context.Background(), Request{})
	if !result.Complete || result.Err != nil || len(result.Candidates) != 2 {
		t.Fatalf("multi-app cask made inventory incomplete: %#v", result)
	}
	ambiguous := result.Candidates[0]
	if ambiguous.Application.Package != "cask/multi" || ambiguous.Application.Identity != "package:homebrew-cask:multi" || ambiguous.Application.UpdateMode != model.ModeAuto {
		t.Fatalf("ambiguous candidate=%#v", ambiguous.Application)
	}
	if ambiguous.Evidence == nil || ambiguous.Evidence.Ambiguity != "multiple-application-paths" || !slices.Equal(ambiguous.Evidence.ApplicationPaths, []string{realFirst, realSecond}) {
		t.Fatalf("ambiguous evidence=%#v", ambiguous.Evidence)
	}
	if app := result.Candidates[1].Application; app.Package != "cask/single" || app.UpdateMode != model.ModeAuto {
		t.Fatalf("later cask was not scanned with automatic updates: %#v", app)
	}
	wantCommands := []string{"/opt/homebrew/bin/brew list --cask --versions --json", "/opt/homebrew/bin/brew --caskroom"}
	if !slices.Equal(runner.commands, wantCommands) {
		t.Fatalf("commands=%q want=%q", runner.commands, wantCommands)
	}
}
