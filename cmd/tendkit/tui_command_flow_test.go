package main

import (
	"go/token"

	"runtime"

	"github.com/eoctet/tendkit/internal/service"

	"context"
	"github.com/eoctet/tendkit/pkg/i18n"

	"os"
	"path/filepath"

	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"

	"github.com/eoctet/tendkit/internal/ui"
	"go/parser"
)

func TestTUICommandFlow(t *testing.T) {
	t.Run("run-interactive-tui-builds-service-callbacks", func(t *testing.T) {
		directory := t.TempDir()
		store := config.New(filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock"))
		saveCommandTestConfig(t, store, config.Default())
		previous := executeTUI
		t.Cleanup(func() { executeTUI = previous })
		called := false
		executeTUI = func(_ context.Context, _ *os.File, _ *os.File, actions ui.TUIActions, color ui.Mode) error {
			called = true
			if color != ui.ModeNever {
				t.Fatalf("color = %q", color)
			}
			catalog, _, err := actions.Load()
			if err != nil || catalog.SchemaVersion != model.SchemaVersion {
				t.Fatalf("Load() = %#v, %v", catalog, err)
			}
			if _, _, err := actions.Reload(); err != nil {
				t.Fatalf("Reload() = %v", err)
			}
			if actions.DownloadAssetCandidates == nil || actions.StartRun == nil {
				t.Fatal("download asset preflight or TUI run callback is not wired")
			}

			if _, _, err := actions.DownloadAssetCandidates(context.Background(), ui.TUIRunRequest{Names: []string{"missing"}}, ui.TUIDownloadAssetObserver{}); err == nil {
				t.Fatal("download asset preflight accepted an unknown application")
			}
			return context.Canceled
		}
		if err := runInteractiveTUI(context.Background(), service.NewWithConfig(store), ui.ModeNever); err != nil {
			t.Fatalf("interactive TUI cancellation = %v", err)
		}
		if !called {
			t.Fatal("interactive TUI did not invoke the UI executor")
		}
	})
	t.Run("tuidepends-on-service-not-updater", func(t *testing.T) {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("locate main test")
		}
		files, err := filepath.Glob(filepath.Join(filepath.Dir(file), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range files {
			if strings.HasSuffix(source, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), source, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imported := range parsed.Imports {
				path := strings.Trim(imported.Path.Value, "\"")
				if path == "github.com/eoctet/tendkit/internal/config" || strings.HasPrefix(path, "github.com/eoctet/tendkit/internal/config/") {
					t.Fatalf("%s imports config directly", filepath.Base(source))
				}
				if path == "github.com/eoctet/tendkit/internal/updater" || strings.HasPrefix(path, "github.com/eoctet/tendkit/internal/updater/") {
					t.Fatalf("%s imports updater directly", filepath.Base(source))
				}
				if path == "github.com/eoctet/tendkit/internal/scanner" || strings.HasPrefix(path, "github.com/eoctet/tendkit/internal/scanner/") {
					t.Fatalf("%s imports scanner directly", filepath.Base(source))
				}
			}
		}
	})
	t.Run("resolve-command-rejects-removed-tui-init-and-scan-commands", func(t *testing.T) {
		bootstrap := service.DefaultBootstrap()
		for _, command := range []string{"tui", "init", "scan"} {
			if _, _, code, done := resolveCommand([]string{command}, bootstrap); !done || code != 2 {
				t.Fatalf("resolveCommand(%q) = done %v, code %d; want rejected with code 2", command, done, code)
			}
		}
	})
	t.Run("default-tui-auto-initializes-missing-configuration-and-accepts-global-options", func(t *testing.T) {
		workingDirectory, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
		directory := t.TempDir()
		t.Setenv("HOME", directory)
		if err := os.Chdir(directory); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(directory, "custom", "config.json")
		lockPath := filepath.Join(directory, "config.lock")
		envPath := filepath.Join(directory, "options.env")
		_ = os.Unsetenv("TENDKIT_MAIN_TEST")
		t.Cleanup(func() { _ = os.Unsetenv("TENDKIT_MAIN_TEST") })
		if err := os.WriteFile(envPath, []byte("TENDKIT_MAIN_TEST=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		called := false
		code := runWithTUI([]string{
			"--config", configPath, "--lock", lockPath, "--color", "never", "--lang", "en", "--env-file", envPath,
		}, func(_ context.Context, store *service.Service, color ui.Mode) error {
			called = true
			if color != ui.ModeNever {
				t.Fatalf("color mode = %q, want %q", color, ui.ModeNever)
			}
			if _, _, err := store.Load(); err != nil {
				t.Fatalf("TUI received uninitialized config: %v", err)
			}
			return nil
		})
		if code != 0 || !called {
			t.Fatalf("runWithTUI returned %d, called=%v", code, called)
		}
		if _, err := os.Stat(configPath); err != nil {
			t.Fatalf("automatic initialization did not create %s: %v", configPath, err)
		}
	})
	t.Run("default-tui-creates-derived-lock-beside-custom-configuration", func(t *testing.T) {
		directory := t.TempDir()
		t.Setenv("HOME", directory)
		configPath := filepath.Join(directory, "custom", "catalog.json")
		lockPath := configPath + ".lock"
		code := runWithTUI([]string{"--config", configPath, "--no-env-file", "--color", "never"}, func(_ context.Context, _ *service.Service, _ ui.Mode) error {
			if _, err := os.Stat(lockPath); err != nil {
				t.Fatalf("derived lock was not created at %s: %v", lockPath, err)
			}
			return nil
		})
		if code != 0 {
			t.Fatalf("runWithTUI returned %d", code)
		}
		if _, err := os.Stat(configPath); err != nil {
			t.Fatalf("configuration was not created at %s: %v", configPath, err)
		}
	})
	t.Run("default-tui-uses-and-creates-user-config-directory", func(t *testing.T) {
		workingDirectory, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
		directory := t.TempDir()
		t.Setenv("HOME", directory)
		if err := os.Chdir(directory); err != nil {
			t.Fatal(err)
		}

		code := runWithTUI([]string{"--no-env-file"}, func(_ context.Context, store *service.Service, _ ui.Mode) error {
			if _, _, err := store.Load(); err != nil {
				t.Fatalf("TUI received uninitialized default config: %v", err)
			}
			return nil
		})
		if code != 0 {
			t.Fatalf("runWithTUI returned %d", code)
		}
		for _, path := range []string{
			filepath.Join(directory, ".config", "tendkit", "config.json"),
			filepath.Join(directory, ".config", "tendkit", "config.json.lock"),
			filepath.Join(directory, ".config", "tendkit", "logs", "run.log"),
		} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("default initialization did not create %s: %v", path, err)
			}
		}
		if _, err := os.Stat(filepath.Join(directory, "conf")); !os.IsNotExist(err) {
			t.Fatalf("legacy startup-directory config was created: %v", err)
		}
	})
	t.Run("default-tuidoes-not-overwrite-invalid-existing-catalog", func(t *testing.T) {
		directory := t.TempDir()
		configPath := filepath.Join(directory, "config.json")
		lockPath := filepath.Join(directory, "config.lock")
		invalid := []byte("{invalid json\n")
		if err := os.WriteFile(configPath, invalid, 0o600); err != nil {
			t.Fatal(err)
		}
		called := false
		code := runWithTUI([]string{
			"--config", configPath, "--lock", lockPath, "--no-env-file",
		}, func(context.Context, *service.Service, ui.Mode) error {
			called = true
			return nil
		})
		if code != 2 || called {
			t.Fatalf("runWithTUI returned %d, called=%v; want invalid configuration rejection", code, called)
		}
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != string(invalid) {
			t.Fatalf("invalid config was overwritten: %q", content)
		}
	})
	t.Run("run-interactive-tui-requires-terminal-before-using-store", func(t *testing.T) {
		i18n.Set(i18n.English)
		err := runInteractiveTUI(context.Background(), &service.Service{}, ui.ModeNever)
		if err == nil || err.Error() != i18n.T("tui.terminal_required") {
			t.Fatalf("runInteractiveTUI() error = %v, want terminal-required error", err)
		}
	})
	t.Run("run-with-tui-rejects-invalid-options-before-initializing-configuration", func(t *testing.T) {
		missingEnv := filepath.Join(t.TempDir(), "missing.env")
		for _, arguments := range [][]string{
			{"--color", "invalid", "--no-env-file"},
			{"--lang", "fr", "--no-env-file"},
			{"--env-file", missingEnv},
			{"version", "--help"},
		} {
			called := false
			if code := runWithTUI(arguments, func(context.Context, *service.Service, ui.Mode) error {
				called = true
				return nil
			}); code != 2 && !(len(arguments) == 2 && arguments[0] == "version" && code == 0) {
				t.Fatalf("runWithTUI(%v) = %d", arguments, code)
			} else if called {
				t.Fatalf("runWithTUI(%v) unexpectedly started TUI", arguments)
			}
		}
	})
}
