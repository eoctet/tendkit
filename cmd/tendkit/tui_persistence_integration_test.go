//go:build darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/eoctet/tendkit/internal/config"
	"github.com/eoctet/tendkit/internal/model"
)

const tuiSmokeTimeout = 4 * time.Second

type tuiSmokeProcess struct {
	t        *testing.T
	cmd      *exec.Cmd
	master   *os.File
	done     chan error
	mu       sync.Mutex
	output   bytes.Buffer
	update   chan struct{}
	waitMu   sync.Mutex
	finished bool
	result   error
}

func buildTUISmokeBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "tendkit")
	command := exec.Command("go", "build", "-o", binary, ".")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build TUI binary: %v\n%s", err, output)
	}
	return binary
}

func startTUISmokeProcess(t *testing.T, binary, configPath, lockPath, home, path string) *tuiSmokeProcess {
	t.Helper()
	command := exec.Command(binary, "--config", configPath, "--lock", lockPath, "--no-env-file", "--color", "never", "--lang=en")
	command.Env = tuiSmokeEnv(os.Environ(), home, path)
	master, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	process := &tuiSmokeProcess{t: t, cmd: command, master: master, done: make(chan error, 1), update: make(chan struct{}, 1)}
	go process.capture()
	go func() { process.done <- command.Wait() }()
	t.Cleanup(process.close)
	return process
}

func tuiSmokeEnv(base []string, home, path string) []string {
	covered := map[string]bool{"HOME": true, "PATH": true, "NO_COLOR": true, "TERM": true, "TENDKIT_PLATFORM_TESTS": true}
	environment := make([]string, 0, len(base)+4)
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if !found || covered[key] {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "HOME="+home, "PATH="+path, "NO_COLOR=1", "TERM=xterm-256color")
}

func (p *tuiSmokeProcess) close() {
	_ = p.master.Close()
	if _, finished := p.waitResult(0); !finished && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	if _, finished := p.waitResult(tuiSmokeTimeout); !finished {
		p.t.Errorf("timed out cleaning up TUI process")
	}
}

func (p *tuiSmokeProcess) waitResult(timeout time.Duration) (error, bool) {
	p.waitMu.Lock()
	if p.finished {
		result := p.result
		p.waitMu.Unlock()
		return result, true
	}
	p.waitMu.Unlock()
	if timeout == 0 {
		select {
		case result := <-p.done:
			p.waitMu.Lock()
			p.finished, p.result = true, result
			p.waitMu.Unlock()
			return result, true
		default:
			return nil, false
		}
	}
	select {
	case result := <-p.done:
		p.waitMu.Lock()
		p.finished, p.result = true, result
		p.waitMu.Unlock()
		return result, true
	case <-time.After(timeout):
		return nil, false
	}
}

func (p *tuiSmokeProcess) capture() {
	buffer := make([]byte, 4096)
	for {
		count, err := p.master.Read(buffer)
		if count > 0 {
			p.mu.Lock()
			_, _ = p.output.Write(buffer[:count])
			p.mu.Unlock()
			select {
			case p.update <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *tuiSmokeProcess) waitFor(marker string) {
	p.t.Helper()
	timer := time.NewTimer(tuiSmokeTimeout)
	defer timer.Stop()
	for {
		p.mu.Lock()
		found := bytes.Contains(p.output.Bytes(), []byte(marker))
		p.mu.Unlock()
		if found {
			return
		}
		select {
		case <-p.update:
		case err := <-p.done:
			p.waitMu.Lock()
			p.finished, p.result = true, err
			p.waitMu.Unlock()
			p.t.Fatalf("process exited before %q: %v", marker, err)
		case <-timer.C:
			p.t.Fatalf("timed out waiting for %q", marker)
		}
	}
}

func (p *tuiSmokeProcess) write(text string) {
	p.t.Helper()
	if _, err := io.WriteString(p.master, text); err != nil {
		p.t.Fatal(err)
	}
}

func (p *tuiSmokeProcess) wait() error {
	p.t.Helper()
	err, finished := p.waitResult(tuiSmokeTimeout)
	if !finished {
		p.t.Fatal("timed out waiting for process exit")
	}
	return err
}

func (p *tuiSmokeProcess) contains(marker string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Contains(p.output.Bytes(), []byte(marker))
}

func requireTUISmokeExitCode(t *testing.T, err error, code int) {
	t.Helper()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != code {
		t.Fatalf("exit error = %v, want exit code %d", err, code)
	}
}
func TestTUISmokeFlow(t *testing.T) {
	t.Run("tui-smoke-env-overrides-are-unique", func(t *testing.T) {
		environment := tuiSmokeEnv([]string{"HOME=/user/home", "PATH=/user/bin", "NO_COLOR=0", "TERM=dumb", "KEEP=value", "PATH=/duplicate"}, "/fixture/home", "/fixture/bin")
		want := map[string]string{"HOME": "/fixture/home", "PATH": "/fixture/bin", "NO_COLOR": "1", "TERM": "xterm-256color", "KEEP": "value"}
		seen := map[string]string{}
		for _, entry := range environment {
			key, value, found := strings.Cut(entry, "=")
			if !found {
				t.Fatalf("malformed environment entry %q", entry)
			}
			if _, exists := seen[key]; exists {
				t.Fatalf("duplicate environment key %q", key)
			}
			seen[key] = value
		}
		for key, value := range want {
			if seen[key] != value {
				t.Fatalf("%s = %q, want %q", key, seen[key], value)
			}
		}
	})
	t.Run("tui-smoke-auto-init-q-and-lock", func(t *testing.T) {
		binary := buildTUISmokeBinary(t)
		directory := t.TempDir()
		configPath, lockPath := filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock")
		first := startTUISmokeProcess(t, binary, configPath, lockPath, directory, directory)
		first.waitFor("\x1b[?1049h")
		second := startTUISmokeProcess(t, binary, configPath, lockPath, directory, directory)
		requireTUISmokeExitCode(t, second.wait(), 2)
		first.write("q")
		if err := first.wait(); err != nil {
			t.Fatalf("first process = %v", err)
		}
		if !first.contains("\x1b[?1049l") {
			t.Fatal("first process did not leave the alternate screen")
		}
		store := config.New(configPath, lockPath)
		if snapshot, err := store.Load(); err != nil || snapshot.SchemaVersion == 0 {
			t.Fatalf("auto-init config = %#v, %v", snapshot, err)
		}
	})
	t.Run("tui-smoke-invalid-config-does-not-change-file", func(t *testing.T) {
		binary := buildTUISmokeBinary(t)
		directory := t.TempDir()
		configPath, lockPath := filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock")
		contents := []byte("{invalid")
		if err := os.WriteFile(configPath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		process := startTUISmokeProcess(t, binary, configPath, lockPath, directory, directory)
		requireTUISmokeExitCode(t, process.wait(), 2)
		if saved, err := os.ReadFile(configPath); err != nil || !bytes.Equal(saved, contents) {
			t.Fatalf("invalid config changed: %q, %v", saved, err)
		}
	})
	t.Run("tui-smoke-persists-local-command-check", func(t *testing.T) {
		binary := buildTUISmokeBinary(t)
		directory := t.TempDir()
		configPath, lockPath := filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock")
		store := config.New(configPath, lockPath)
		catalog := config.Default()
		catalog.Apps = []model.Application{{
			ID: "local", Name: "Local", Type: model.ApplicationTypeCLI, InstallPath: "/bin/sh", Enabled: true,
			UpdateMode: model.ModeCheck,
			Provider:   providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "printf '1.1.0\\n'", "", nil, ""),
		}}
		saveCommandTestConfig(t, store, catalog)
		process := startTUISmokeProcess(t, binary, configPath, lockPath, directory, directory)
		process.waitFor("\x1b[?1049h")
		process.write("c")
		process.waitFor("Operation complete: 1 results")
		process.write("\x1bq")
		if err := process.wait(); err != nil {
			t.Fatalf("check process = %v", err)
		}
		reader := config.New(configPath, lockPath)
		saved, err := reader.Load()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Apps[0].StatusManaged.CurrentVersion != "1.0.0" || saved.Apps[0].StatusManaged.LatestVersion != "1.1.0" {
			t.Fatalf("local command check was not persisted: %#v", saved.Apps[0].StatusManaged)
		}
	})
	t.Run("tui-smoke-persists-local-command-update", func(t *testing.T) {
		binary := buildTUISmokeBinary(t)
		directory := t.TempDir()
		configPath, lockPath := filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock")
		marker := filepath.Join(directory, "updated")
		store := config.New(configPath, lockPath)
		catalog := config.Default()
		catalog.Apps = []model.Application{{
			ID: "local-update", Name: "Local update", Type: model.ApplicationTypeCLI, InstallPath: "/bin/sh", Enabled: true,
			UpdateMode: model.ModeAuto,
			Provider:   providerConfig(model.ProviderDefault, fmt.Sprintf("if test -f %q; then printf '1.1.0\\n'; else printf '1.0.0\\n'; fi", marker), "printf '1.1.0\\n'", fmt.Sprintf(": > %q", marker), nil, ""),
		}}
		saveCommandTestConfig(t, store, catalog)
		process := startTUISmokeProcess(t, binary, configPath, lockPath, directory, directory)
		process.waitFor("\x1b[?1049h")
		process.write("u\r")
		process.waitFor("Operation complete: 1 results")
		process.write("q")
		if err := process.wait(); err != nil {
			t.Fatalf("update process = %v", err)
		}
		reader := config.New(configPath, lockPath)
		saved, err := reader.Load()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Apps[0].StatusManaged.UpdateStatus != model.StatusUpdated || saved.Apps[0].StatusManaged.LastUpdateTime == "" {
			t.Fatalf("local command update was not persisted: %#v", saved.Apps[0].StatusManaged)
		}
	})
	t.Run("tui-smoke-persists-workers-edit", func(t *testing.T) {
		binary := buildTUISmokeBinary(t)
		directory := t.TempDir()
		configPath, lockPath := filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock")
		store := config.New(configPath, lockPath)
		catalog := config.Default()
		originalWorkers := catalog.Settings.Workers
		saveCommandTestConfig(t, store, catalog)
		process := startTUISmokeProcess(t, binary, configPath, lockPath, directory, directory)
		process.waitFor("\x1b[?1049h")
		// Settings → workers (third row) → increment → persist → exit.
		process.write("s\x1b[B\x1b[B\x1b[C\x13q")
		if err := process.wait(); err != nil {
			t.Fatalf("workers process = %v", err)
		}
		reader := config.New(configPath, lockPath)
		saved, err := reader.Load()
		if err != nil {
			t.Fatal(err)
		}
		if saved.Settings.Workers != originalWorkers+1 {
			t.Fatalf("workers = %d, want %d", saved.Settings.Workers, originalWorkers+1)
		}
	})
	t.Run("tui-smoke-persists-path-scan-candidate", func(t *testing.T) {
		github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
		}))
		t.Cleanup(github.Close)
		binary := buildTUISmokeBinary(t)
		directory := t.TempDir()
		fixturePath := filepath.Join(directory, "bin")
		if err := os.Mkdir(fixturePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixturePath, "git"), []byte("#!/bin/sh\nprintf 'git version 9.9.9\\n'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		configPath, lockPath := filepath.Join(directory, "config.json"), filepath.Join(directory, "config.lock")
		store := config.New(configPath, lockPath)
		catalog := config.Default()
		catalog.Settings.ProviderURLs[string(model.ProviderGitHubRelease)] = github.URL + "/{package}"
		catalog.Settings.ProviderURLs[string(model.ProviderGitHubTag)] = github.URL + "/{package}"
		disabled, enabled := false, true
		catalog.Settings.Scan.Path, catalog.Settings.Scan.Application = enabled, disabled
		catalog.Settings.Scan.Packages.Python, catalog.Settings.Scan.Packages.Node, catalog.Settings.Scan.Packages.Go, catalog.Settings.Scan.Packages.UV, catalog.Settings.Scan.Packages.Ruby = disabled, disabled, disabled, disabled, disabled
		saveCommandTestConfig(t, store, catalog)
		process := startTUISmokeProcess(t, binary, configPath, lockPath, directory, fixturePath)
		process.waitFor("\x1b[?1049h")
		process.write("\x13s")
		process.waitFor("Scan complete:")
		process.write("j\r")
		process.waitFor("Applied scan changes for cli-git")
		process.write("\x1bq")
		if err := process.wait(); err != nil {
			t.Fatalf("scan process = %v", err)
		}
		reader := config.New(configPath, lockPath)
		saved, err := reader.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(saved.Apps) != 1 || saved.Apps[0].ID != "cli-git" || saved.Apps[0].InstallPath != filepath.Join(fixturePath, "git") {
			t.Fatalf("scan candidate was not persisted: %#v", saved.Apps)
		}
	})
}
