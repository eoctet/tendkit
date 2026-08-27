package cargoroot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallRootIgnoresProjectConfigAndUsesCargoHomeConfig(t *testing.T) {
	workspace := t.TempDir()
	project := filepath.Join(workspace, "project")
	cwd := filepath.Join(project, "nested")
	home := filepath.Join(workspace, "home")
	cargoHome := filepath.Join(home, ".cargo")
	for _, directory := range []string{cwd, filepath.Join(project, ".cargo"), filepath.Join(workspace, ".cargo"), cargoHome} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, ".cargo", "config.toml"), []byte("[install]\nroot = '../project-root'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".cargo", "config.toml"), []byte("install.root = \"../workspace-root\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte("[install]\nroot = \"../home-root\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARGO_INSTALL_ROOT", "")
	t.Setenv("CARGO_HOME", cargoHome)
	root, err := InstallRoot(nil, Dependencies{Getwd: func() (string, error) { return cwd, nil }, UserHomeDir: func() (string, error) { return home, nil }})
	want := filepath.Join(workspace, "home-root")
	if err != nil || root != want {
		t.Fatalf("root=%q error=%v want=%q", root, err, want)
	}
}

func TestInstallRootExplicitEnvironmentOverridesConfig(t *testing.T) {
	workspace := t.TempDir()
	configDir := filepath.Join(workspace, ".cargo")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("install.root = \"config-root\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARGO_INSTALL_ROOT", filepath.Join(workspace, "process-root"))
	root, err := InstallRoot(map[string]string{"CARGO_INSTALL_ROOT": "app-root"}, Dependencies{Getwd: func() (string, error) { return workspace, nil }, UserHomeDir: func() (string, error) { return workspace, nil }})
	want := filepath.Join(workspace, "app-root")
	if err != nil || root != want {
		t.Fatalf("root=%q error=%v want=%q", root, err, want)
	}
}

func TestInstallRootApplicationCargoHomeOverridesProcessCargoHome(t *testing.T) {
	workspace := t.TempDir()
	appHome := filepath.Join(workspace, "app-cargo")
	processHome := filepath.Join(workspace, "process-cargo")
	if err := os.MkdirAll(appHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARGO_INSTALL_ROOT", "")
	t.Setenv("CARGO_HOME", processHome)
	root, err := InstallRoot(map[string]string{"CARGO_HOME": appHome}, Dependencies{Getwd: func() (string, error) { return workspace, nil }, UserHomeDir: func() (string, error) { return workspace, nil }})
	if err != nil || root != appHome {
		t.Fatalf("root=%q error=%v want=%q", root, err, appHome)
	}
}

func TestInstallRootRejectsComplexOrAmbiguousRelevantConfig(t *testing.T) {
	for _, content := range []string{
		"[install]\nroot = { path = \"root\" }\n",
		"install.root = \"one\"\ninstall.root = \"two\"\n",
	} {
		t.Run(content[:7], func(t *testing.T) {
			workspace := t.TempDir()
			configDir := filepath.Join(workspace, "cargo-home")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CARGO_INSTALL_ROOT", "")
			t.Setenv("CARGO_HOME", configDir)
			if _, err := InstallRoot(nil, Dependencies{Getwd: func() (string, error) { return workspace, nil }, UserHomeDir: func() (string, error) { return workspace, nil }}); err == nil {
				t.Fatal("invalid install.root was accepted")
			}
		})
	}
}
