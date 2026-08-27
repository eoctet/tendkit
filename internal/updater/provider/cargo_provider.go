package provider

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/cargoroot"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

// CargoProvider is read-only: cargo install --list cannot prove original
// install options, so automatic update is deliberately not registered.
type CargoProvider struct {
	Runner   commandRunner
	lookup   func(string, map[string]string) (string, error)
	cwd      func() (string, error)
	readFile func(string) ([]byte, error)
	homeDir  func() (string, error)
}

func (p CargoProvider) Current(ctx context.Context, request Request) (string, error) {
	manager, err := managerPath("cargo", request.App.Environment, p.lookup)
	if err != nil {
		return "", p.error(request, CapabilityCurrent, "provider.cargo_current_failed", err)
	}
	return p.current(ctx, request, manager, CapabilityCurrent)
}

func (p CargoProvider) current(ctx context.Context, request Request, manager string, capability Capability) (string, error) {
	name, err := requiredPackage(request, capability)
	if err != nil {
		return "", err
	}
	root, err := p.installRoot(request)
	if err != nil {
		return "", p.error(request, capability, "provider.cargo_current_failed", err)
	}
	r, err := p.Runner.Run(ctx, runtimeutil.QuoteShell(manager)+" install --list --root "+runtimeutil.QuoteShell(root), request.App.Environment)
	if err != nil {
		return "", p.error(request, capability, "provider.cargo_current_failed", err)
	}
	if r.ExitCode != 0 {
		return "", p.error(request, capability, "provider.cargo_current_exit", fmt.Errorf("exit %d", r.ExitCode), r.ExitCode)
	}
	current, binaries, err := parseCargoInstalledBinaries(r.Combined(), name)
	if err != nil {
		return "", p.error(request, capability, "provider.cargo_parse_failed", err)
	}
	if err := verifyInstallOwnership(request.App.InstallPath, filepath.Join(root, "bin")); err != nil {
		return "", p.error(request, capability, "provider.target_conflict", err)
	}
	if err := verifyCargoBinaryPaths(request.App.InstallPath, filepath.Join(root, "bin"), binaries); err != nil {
		return "", p.error(request, capability, "provider.target_conflict", err)
	}
	return current, nil
}

func verifyCargoBinaryPaths(installPath, binaryRoot string, binaries []string) error {
	if len(binaries) == 0 {
		return errors.New("crate has no binaries")
	}
	realInstallPath, err := filepath.EvalSymlinks(installPath)
	if err != nil {
		return err
	}
	matched := false
	for _, binary := range binaries {
		if binary == "" || binary == "." || binary == ".." || filepath.Base(binary) != binary {
			return errors.New("invalid Cargo binary path")
		}
		candidate := filepath.Join(binaryRoot, binary)
		if err := verifyInstallOwnership(candidate, binaryRoot); err != nil {
			return err
		}
		realCandidate, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return err
		}
		if filepath.Clean(realCandidate) == filepath.Clean(realInstallPath) {
			matched = true
		}
	}
	if !matched {
		return errors.New("installation path is not a crate binary")
	}
	return nil
}

func (p CargoProvider) installRoot(request Request) (string, error) {
	return cargoroot.InstallRoot(request.App.Environment, cargoroot.Dependencies{Getwd: p.cwd, ReadFile: p.readFile, UserHomeDir: p.homeDir})
}
func (p CargoProvider) error(request Request, capability Capability, key string, cause error, details ...any) error {
	args := append([]any{request.App.Name}, details...)
	return &Error{Key: key, Args: args, Provider: string(model.ProviderCargo), Capability: capability, Cause: cause}
}

var cargoInstalledLine = regexp.MustCompile(`(?m)^([^\s]+)\s+v([^\s:]+):\s*$`)

func parseCargoInstalledBinaries(raw, name string) (string, []string, error) {
	var found string
	var binaries []string
	active := false
	for _, line := range strings.Split(raw, "\n") {
		if match := cargoInstalledLine.FindStringSubmatch(line); len(match) == 3 {
			active = match[1] == name
			if active {
				if found != "" {
					return "", nil, errors.New("installed crate is not unique")
				}
				if _, err := version.Extract(match[2]); err != nil {
					return "", nil, err
				}
				found = version.Normalize(match[2])
			}
			continue
		}
		if active && strings.TrimSpace(line) != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			binary := strings.TrimSpace(line)
			if strings.ContainsAny(binary, " \t") {
				return "", nil, errors.New("invalid cargo binary")
			}
			binaries = append(binaries, binary)
		}
	}
	if found == "" {
		return "", nil, errors.New("installed crate not found")
	}
	return found, binaries, nil
}
