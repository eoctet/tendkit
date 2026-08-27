package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type realHomebrewRunner struct {
	runner   runtimeutil.Runner
	commands []string
}

func (r *realHomebrewRunner) Run(ctx context.Context, command string, environment map[string]string) (runtimeutil.Result, error) {
	r.commands = append(r.commands, command)
	return r.runner.Run(ctx, command, environment)
}

func TestHomebrewRealRipgrepCurrentAndLatest(t *testing.T) {
	if os.Getenv("TENDKIT_REAL_HOMEBREW") != "1" {
		t.Skip("set TENDKIT_REAL_HOMEBREW=1 to run against the local Homebrew installation")
	}
	brew, err := exec.LookPath("brew")
	if err != nil {
		t.Fatal(err)
	}
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Fatal(err)
	}
	runner := &realHomebrewRunner{runner: runtimeutil.Runner{IdleTimeout: 2 * time.Minute}}
	provider := HomebrewProvider{
		Runner: runner,
		lookup: func(string, map[string]string) (string, error) { return brew, nil },
	}
	request := Request{App: model.Application{
		Name: "ripgrep", InstallPath: rg, Package: "formula/ripgrep",
		Provider: model.ProviderConfig{Type: model.ProviderHomebrew},
	}}
	current, err := provider.Current(context.Background(), request)
	if err != nil || current == "" {
		t.Fatalf("Current()=%q, %v", current, err)
	}
	latest, err := provider.Latest(context.Background(), request)
	if err != nil || latest == "" {
		t.Fatalf("Latest()=%q, %v", latest, err)
	}
	t.Logf("real Homebrew ripgrep: current=%s latest=%s brew=%s rg=%s", current, latest, brew, rg)
}

func TestHomebrewRealMissingCaskUsesFastInventory(t *testing.T) {
	if os.Getenv("TENDKIT_REAL_HOMEBREW") != "1" {
		t.Skip("set TENDKIT_REAL_HOMEBREW=1 to run against the local Homebrew installation")
	}
	brew, err := exec.LookPath("brew")
	if err != nil {
		t.Fatal(err)
	}
	runner := &realHomebrewRunner{runner: runtimeutil.Runner{IdleTimeout: 2 * time.Minute}}
	provider := HomebrewProvider{
		Runner: runner,
		host:   func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "darwin"} },
		lookup: func(string, map[string]string) (string, error) { return brew, nil },
	}
	_, err = provider.Current(context.Background(), Request{App: model.Application{
		Name: "missing-cask", InstallPath: "/Applications/TendKitMissingCask.app",
		Package: "cask/tendkit-real-missing-cask", Provider: model.ProviderConfig{Type: model.ProviderHomebrew},
	}})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Capability != CapabilityCurrent {
		t.Fatalf("Current() error=%#v", err)
	}
	commands := strings.Join(runner.commands, "\n")
	if strings.Contains(commands, " info ") || !strings.Contains(commands, "list --cask --versions --json") {
		t.Fatalf("unexpected real Cask commands: %s", commands)
	}
	t.Logf("real missing Cask failed through fast inventory as expected: %v", err)
}

func TestHomebrewRealInstalledCaskCurrentAndLatest(t *testing.T) {
	if os.Getenv("TENDKIT_REAL_HOMEBREW") != "1" {
		t.Skip("set TENDKIT_REAL_HOMEBREW=1 to run against the local Homebrew installation")
	}
	token, application := os.Getenv("TENDKIT_REAL_HOMEBREW_CASK"), os.Getenv("TENDKIT_REAL_HOMEBREW_CASK_APP")
	if token == "" || application == "" {
		t.Skip("set TENDKIT_REAL_HOMEBREW_CASK and TENDKIT_REAL_HOMEBREW_CASK_APP")
	}
	brew, err := exec.LookPath("brew")
	if err != nil {
		t.Fatal(err)
	}
	runner := &realHomebrewRunner{runner: runtimeutil.Runner{IdleTimeout: 2 * time.Minute}}
	provider := HomebrewProvider{
		Runner: runner,
		host:   func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "darwin"} },
		lookup: func(string, map[string]string) (string, error) { return brew, nil },
	}
	request := Request{App: model.Application{
		Name: token, InstallPath: application, Package: "cask/" + token,
		Provider: model.ProviderConfig{Type: model.ProviderHomebrew},
	}}
	current, err := provider.Current(context.Background(), request)
	if err != nil || current == "" {
		t.Fatalf("Current()=%q, %v", current, err)
	}
	latest, err := provider.Latest(context.Background(), request)
	if err != nil || latest == "" {
		t.Fatalf("Latest()=%q, %v", latest, err)
	}
	if strings.Contains(strings.Join(runner.commands, "\n"), " info ") {
		t.Fatalf("real installed Cask used brew info: %q", runner.commands)
	}
	t.Logf("real Homebrew Cask %s: current=%s latest=%s app=%s", token, current, latest, application)
}
