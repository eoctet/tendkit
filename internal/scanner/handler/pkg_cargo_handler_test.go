package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestCargoHandlerContract(t *testing.T) {
	t.Run("cargo-install-root-rejects-complex-or-duplicate-cargo-home-config", func(t *testing.T) {
		for _, test := range []struct {
			name, content string
		}{
			{"complex value", "[install]\nroot = { path = \"root\" }\n"},
			{"duplicate key", "install.root = \"one\"\ninstall.root = \"two\"\n"},
		} {
			t.Run(test.name, func(t *testing.T) {
				workspace := t.TempDir()
				cargoHome := filepath.Join(workspace, "cargo-home")
				if err := os.MkdirAll(cargoHome, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte(test.content), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CARGO_INSTALL_ROOT", "")
				t.Setenv("CARGO_HOME", cargoHome)
				handler := NewCargo(noCommandRunner{})
				handler.lookPath = func(string) (string, error) { return "/fixture/bin/cargo", nil }
				handler.cwd = func() (string, error) { return workspace, nil }
				handler.homeDir = func() (string, error) { return workspace, nil }
				result := handler.Scan(context.Background(), Request{})
				var incomplete *PackageInventoryIncompleteError
				if result.Complete || !errors.As(result.Err, &incomplete) || len(result.Candidates) != 0 {
					t.Fatalf("invalid install.root result=%#v", result)
				}
			})
		}
	})
	t.Run("cargo-install-root-uses-only-configured-environment-or-process-defaults", func(t *testing.T) {
		t.Setenv("CARGO_INSTALL_ROOT", "/process/install-root")
		t.Setenv("CARGO_HOME", "/process/cargo-home")
		handler := NewCargo(noCommandRunner{})
		configured := []model.Application{{Provider: model.ProviderConfig{Type: model.ProviderCargo}, Environment: map[string]string{"CARGO_INSTALL_ROOT": "/app/install-root"}}}
		if root := handler.cargoInstallRoot(configured); root != "/app/install-root" {
			t.Fatalf("configured root=%q", root)
		}
		if root := handler.cargoInstallRoot(nil); root != "/process/install-root" {
			t.Fatalf("process install root=%q", root)
		}
		t.Setenv("CARGO_INSTALL_ROOT", "")
		configured = []model.Application{{ID: "cargo", Environment: map[string]string{"CARGO_HOME": "/app/cargo-home"}}}
		if root := handler.cargoInstallRoot(configured); root != "/app/cargo-home" {
			t.Fatalf("application cargo home root=%q", root)
		}
		if root := handler.cargoInstallRoot(nil); root != "/process/cargo-home" {
			t.Fatalf("cargo home root=%q", root)
		}
	})
	t.Run("cargoscan-never-queries-config-or-info", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "bin", "sample")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		canonicalPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		runner := &scriptedRunner{responses: []runtimeutil.Result{{Stdout: "sample v1.2.3:\n    sample\n"}}}
		handler := NewCargo(runner)
		handler.lookPath = func(string) (string, error) { return "/fixture/bin/cargo", nil }
		handler.getenv = func(key string) string {
			if key == "PATH" {
				return "/existing/bin" + string(os.PathListSeparator) + "/fixture/bin" + string(os.PathListSeparator) + "/after/bin"
			}
			return "secret"
		}
		configured := []model.Application{{Provider: model.ProviderConfig{Type: model.ProviderCargo}, Environment: map[string]string{"CARGO_INSTALL_ROOT": root}}}
		result := handler.Scan(context.Background(), Request{Configured: configured})
		if !result.Complete || result.Err != nil || len(result.Candidates) != 1 || result.Candidates[0].Application.InstallPath != canonicalPath || result.Candidates[0].Application.Enabled || result.Candidates[0].CurrentVersion != "1.2.3" {
			t.Fatalf("result=%#v", result)
		}
		if !slices.Equal(runner.commands, []string{"/fixture/bin/cargo install --list --root " + runtimeutil.QuoteShell(root)}) {
			t.Fatalf("commands=%q", runner.commands)
		}
		environment := result.Candidates[0].Application.Environment
		wantPath := "/fixture/bin" + string(os.PathListSeparator) + "/existing/bin" + string(os.PathListSeparator) + "/after/bin"
		if environment["CARGO_INSTALL_ROOT"] != root || environment["PATH"] != wantPath || len(environment) != 2 {
			t.Fatalf("candidate environment=%#v", environment)
		}
	})
	t.Run("cargo-handler-stores-canonical-install-path-in-candidate", func(t *testing.T) {
		root := t.TempDir()
		managerDir := t.TempDir()
		manager := filepath.Join(managerDir, "cargo")
		if err := os.WriteFile(manager, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		realPath := filepath.Join(root, "bin", "sample-real")
		linkPath := filepath.Join(root, "bin", "sample")
		if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realPath, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realPath, linkPath); err != nil {
			t.Fatal(err)
		}
		canonicalRealPath, err := filepath.EvalSymlinks(realPath)
		if err != nil {
			t.Fatal(err)
		}
		runner := &scriptedRunner{responses: []runtimeutil.Result{{Stdout: "sample v1.2.3:\n    sample\n"}}}
		handler := NewCargo(runner)
		handler.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
		configured := []model.Application{
			{ID: "cargo", Name: "Cargo", InstallPath: manager},
			{Provider: model.ProviderConfig{Type: model.ProviderCargo}, Environment: map[string]string{"CARGO_INSTALL_ROOT": root}},
		}
		result := handler.Scan(context.Background(), Request{Configured: configured})
		if !result.Complete || result.Err != nil || len(result.Candidates) != 1 || result.Candidates[0].Application.InstallPath != canonicalRealPath {
			t.Fatalf("result=%#v", result)
		}
		if !strings.HasPrefix(result.Candidates[0].Application.Environment["PATH"], managerDir) {
			t.Fatalf("candidate environment=%#v", result.Candidates[0].Application.Environment)
		}
	})
	t.Run("cargo-handler-rejects-unsafe-inventory-binary-names", func(t *testing.T) {
		for _, binary := range []string{"../escape", "/absolute", "nested/name", `nested\name`, ".", ".."} {
			t.Run(strings.ReplaceAll(binary, "/", "_"), func(t *testing.T) {
				_, err := parseCargoInstallList("sample v1.2.3:\n    " + binary + "\n")
				if err == nil {
					t.Fatalf("unsafe binary %q was accepted", binary)
				}
			})
		}
	})
	t.Run("cargo-handler-rejects-binary-symlink-outside-root-without-evidence", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "sample-real")
		linkPath := filepath.Join(root, "bin", "sample")
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, linkPath); err != nil {
			t.Fatal(err)
		}
		runner := &scriptedRunner{responses: []runtimeutil.Result{{Stdout: "sample v1.2.3:\n    sample\n"}}}
		handler := NewCargo(runner)
		handler.lookPath = func(string) (string, error) { return "/fixture/bin/cargo", nil }
		configured := []model.Application{{Provider: model.ProviderConfig{Type: model.ProviderCargo}, Environment: map[string]string{"CARGO_INSTALL_ROOT": root}}}
		result := handler.Scan(context.Background(), Request{Configured: configured})
		var incomplete *PackageInventoryIncompleteError
		if result.Complete || !errors.As(result.Err, &incomplete) || len(result.Candidates) != 0 {
			t.Fatalf("result=%#v", result)
		}
	})
	t.Run("cargo-handler-uses-cargo-home-config-root-and-ignores-project-config", func(t *testing.T) {
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
		canonicalPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(projectConfigDir, "config.toml"), []byte("install.root = \"ignored-project-root\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte("install.root = \"install-root\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CARGO_INSTALL_ROOT", "")
		t.Setenv("CARGO_HOME", cargoHome)
		runner := &scriptedRunner{responses: []runtimeutil.Result{{Stdout: "sample v1.2.3:\n    sample\n"}}}
		handler := NewCargo(runner)
		handler.lookPath = func(string) (string, error) { return "/fixture/bin/cargo", nil }
		handler.cwd = func() (string, error) { return project, nil }
		handler.homeDir = func() (string, error) { return workspace, nil }
		result := handler.Scan(context.Background(), Request{})
		if !result.Complete || result.Err != nil || len(result.Candidates) != 1 || result.Candidates[0].Application.InstallPath != canonicalPath {
			t.Fatalf("result=%#v", result)
		}
		if !slices.Equal(runner.commands, []string{"/fixture/bin/cargo install --list --root " + runtimeutil.QuoteShell(root)}) {
			t.Fatalf("commands=%q", runner.commands)
		}
	})
}
