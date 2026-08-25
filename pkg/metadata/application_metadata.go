package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

const (
	ApplicationExtension = ".app"
	plistApplicationPath = "/usr/bin/plutil"
	applicationInfoPlist = "Contents/Info.plist"
	jetBrainsProductInfo = "Contents/Resources/product-info.json"
)

// CommandRunner is the common read/execute boundary shared by scanner handlers
// and updater providers.
type CommandRunner interface {
	Run(context.Context, string, map[string]string) (runtimeutil.Result, error)
}

// MacApplicationMetadata contains normalized Info.plist values used by both
// scanner policy and updater capabilities.
type MacApplicationMetadata struct {
	Path                       string
	Name                       string
	BundleID                   string
	Category                   string
	Description                string
	Version                    string
	SparkleFeedURL             string
	SparklePublicEDKey         string
	SparkleAllowsAutoUpdates   bool
	SparkleAutomaticallyUpdate bool
	JetBrainsProductCode       string
}

// ReadMacApplicationMetadata reads one application bundle's Info.plist through
// the system plist utility. It never launches the application.
func ReadMacApplicationMetadata(parent context.Context, appPath string) (MacApplicationMetadata, error) {
	metadata := MacApplicationMetadata{Path: appPath, Name: strings.TrimSuffix(filepath.Base(appPath), filepath.Ext(appPath))}
	if err := parent.Err(); err != nil {
		return metadata, err
	}
	if !strings.EqualFold(filepath.Ext(appPath), ApplicationExtension) {
		return metadata, errors.New("path is not a macOS application bundle")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	infoPath := filepath.Join(appPath, filepath.FromSlash(applicationInfoPlist))
	// #nosec G204 -- The executable and flags are fixed; appPath contributes only the final argv path and is never interpreted by a shell.
	output, err := exec.CommandContext(ctx, plistApplicationPath, "-convert", "json", "-o", "-", infoPath).Output()
	if err != nil {
		if parent.Err() != nil {
			return metadata, parent.Err()
		}
		return metadata, err
	}
	var values map[string]any
	if err := json.Unmarshal(output, &values); err != nil {
		return metadata, err
	}
	text := func(key string) string {
		value, _ := values[key].(string)
		return strings.TrimSpace(value)
	}
	boolean := func(key string) bool {
		value, _ := values[key].(bool)
		return value
	}
	metadata.BundleID = text("CFBundleIdentifier")
	metadata.Category = text("LSApplicationCategoryType")
	metadata.Description = text("CFBundleGetInfoString")
	if metadata.Description == "" {
		metadata.Description = text("CFBundleSpokenName")
	}
	if name := text("CFBundleDisplayName"); name != "" {
		metadata.Name = name
	} else if name := text("CFBundleName"); name != "" {
		metadata.Name = name
	}
	metadata.Version = version.Normalize(text("CFBundleShortVersionString"))
	if metadata.Version == "" {
		metadata.Version = version.Normalize(text("CFBundleVersion"))
	}
	metadata.SparkleFeedURL = text("SUFeedURL")
	metadata.SparklePublicEDKey = text("SUPublicEDKey")
	metadata.SparkleAllowsAutoUpdates = boolean("SUAllowsAutomaticUpdates")
	metadata.SparkleAutomaticallyUpdate = boolean("SUAutomaticallyUpdate")
	metadata.JetBrainsProductCode = readJetBrainsProductCode(appPath, metadata.BundleID)
	return metadata, nil
}

// readJetBrainsProductCode reads only the installed product identifier. The
// scanner maps it to the releases API code separately; product-info.json must
// never be treated as a provider URL or package value on its own.
func readJetBrainsProductCode(appPath, bundleID string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(bundleID)), "com.jetbrains.") {
		return ""
	}
	info, err := os.Stat(filepath.Join(appPath, filepath.FromSlash(jetBrainsProductInfo)))
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return ""
	}
	// #nosec G304 -- The fixed product-info.json path is read only after validating the bundle identity, file type, and size.
	contents, err := os.ReadFile(filepath.Join(appPath, filepath.FromSlash(jetBrainsProductInfo)))
	if err != nil {
		return ""
	}
	var product struct {
		ProductCode string `json:"productCode"`
	}
	if json.Unmarshal(contents, &product) != nil {
		return ""
	}
	code := strings.TrimSpace(product.ProductCode)
	if len(code) < 2 || len(code) > 4 {
		return ""
	}
	for _, character := range code {
		if character < 'A' || character > 'Z' {
			return ""
		}
	}
	return code
}

// DetectCLIVersion tries the conventional read-only version flags in a stable
// order. Applications with exceptional syntax use provider.actions.version.
func DetectCLIVersion(ctx context.Context, runner CommandRunner, executable string, environment map[string]string) (string, error) {
	if runner == nil {
		return "", errors.New("command runner is nil")
	}
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return "", errors.New("CLI executable is empty")
	}
	var lastErr error
	for _, argument := range []string{"--version", "version", "-v"} {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		result, err := runner.Run(ctx, runtimeutil.QuoteShell(executable)+" "+argument, environment)
		if err != nil {
			lastErr = err
			continue
		}
		if result.ExitCode != 0 {
			lastErr = fmt.Errorf("version command exited with code %d", result.ExitCode)
			continue
		}
		value, extractErr := version.Extract(result.Combined())
		if extractErr == nil && value != "" {
			return value, nil
		}
		lastErr = extractErr
	}
	if lastErr == nil {
		lastErr = errors.New("CLI version is unavailable")
	}
	return "", lastErr
}

type PackageEcosystem string

const (
	PackagePython PackageEcosystem = "python"
	PackageNode   PackageEcosystem = "node"
	PackageGo     PackageEcosystem = "go"
	PackageUV     PackageEcosystem = "uv"
	PackageRuby   PackageEcosystem = "ruby"
)

type PackageTarget struct {
	Ecosystem   PackageEcosystem
	Manager     string
	Name        string
	InstallPath string
	Environment map[string]string
	UserInstall bool
}

type GoComponentMetadata struct {
	Command string
	Module  string
	Version string
}

// PackageEcosystemFromIdentity interprets the stable scanner identity contract.
func PackageEcosystemFromIdentity(identity string) PackageEcosystem {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(identity)), ":", 3)
	if len(parts) != 3 || parts[0] != "package" {
		return ""
	}
	switch PackageEcosystem(parts[1]) {
	case PackagePython, PackageNode, PackageGo, PackageUV, PackageRuby:
		return PackageEcosystem(parts[1])
	default:
		return ""
	}
}

func FindPackageManager(ecosystem PackageEcosystem) (string, error) {
	names := map[PackageEcosystem][]string{
		PackagePython: {"python3", "python"},
		PackageNode:   {"npm"},
		PackageGo:     {"go"},
		PackageUV:     {"uv"},
		PackageRuby:   {"gem"},
	}[ecosystem]
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("package manager for %s is unavailable", ecosystem)
}

func FindSparkleCLI() (string, error) {
	path, err := exec.LookPath("sparkle")
	if err != nil {
		return "", errors.New("official Sparkle CLI is unavailable")
	}
	return path, nil
}

func ReadGoComponentMetadata(ctx context.Context, runner CommandRunner, manager, binary string, environment map[string]string) (GoComponentMetadata, error) {
	if runner == nil || strings.TrimSpace(manager) == "" || strings.TrimSpace(binary) == "" {
		//lint:ignore ST1005 Go is a product name and must retain its capitalization.
		return GoComponentMetadata{}, errors.New("Go manager and component path are required")
	}
	result, err := runner.Run(ctx, shellCommand(manager, "version", "-m", binary), environment)
	if err != nil {
		return GoComponentMetadata{}, err
	}
	if result.ExitCode != 0 {
		//lint:ignore ST1005 Go is a product name and must retain its capitalization.
		return GoComponentMetadata{}, fmt.Errorf("Go metadata command exited with code %d", result.ExitCode)
	}
	return ParseGoComponentMetadata(result.Stdout)
}

func ParseGoComponentMetadata(output string) (GoComponentMetadata, error) {
	metadata := GoComponentMetadata{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "path" {
			metadata.Command = fields[1]
		}
		if len(fields) >= 3 && fields[0] == "mod" {
			metadata.Module = fields[1]
			metadata.Version = version.Normalize(fields[2])
		}
	}
	if metadata.Command == "" || metadata.Module == "" || metadata.Version == "" {
		//lint:ignore ST1005 Go is a product name and must retain its capitalization.
		return GoComponentMetadata{}, errors.New("Go module metadata is unavailable")
	}
	return metadata, nil
}

// ReadPackageVersion reads installed package/component metadata with the
// ecosystem's own command line interface.
func ReadPackageVersion(ctx context.Context, runner CommandRunner, target PackageTarget) (string, error) {
	if runner == nil {
		return "", errors.New("command runner is nil")
	}
	if strings.TrimSpace(target.Manager) == "" || strings.TrimSpace(target.Name) == "" {
		return "", errors.New("package manager and name are required")
	}
	command, err := PackageVersionCommand(target)
	if err != nil {
		return "", err
	}
	result, err := runner.Run(ctx, command, target.Environment)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("package metadata command exited with code %d", result.ExitCode)
	}
	switch target.Ecosystem {
	case PackageNode:
		var listing struct {
			Dependencies map[string]struct {
				Version string `json:"version"`
			} `json:"dependencies"`
		}
		if err := json.Unmarshal([]byte(result.Stdout), &listing); err != nil {
			return "", err
		}
		for name, item := range listing.Dependencies {
			if strings.EqualFold(name, target.Name) {
				return version.Normalize(item.Version), nil
			}
		}
		//lint:ignore ST1005 Node.js is a product name and must retain its capitalization.
		return "", errors.New("Node.js package is absent from metadata")
	case PackageGo, PackageUV:
		return version.Extract(result.Combined())
	default:
		return version.Extract(result.Combined())
	}
}

// PackageVersionCommand returns the official read-only metadata command for a
// package/component. Handler-generated actions and Provider fallback use the
// same command contract.
func PackageVersionCommand(target PackageTarget) (string, error) {
	if strings.TrimSpace(target.Manager) == "" || strings.TrimSpace(target.Name) == "" {
		return "", errors.New("package manager and name are required")
	}
	switch target.Ecosystem {
	case PackagePython:
		program := `import importlib.metadata as metadata, sys; print(metadata.version(sys.argv[1]))`
		return shellCommand(target.Manager, "-c", program, target.Name), nil
	case PackageNode:
		return shellCommand(target.Manager, "list", "--global", "--depth=0", "--json", target.Name), nil
	case PackageGo:
		if strings.TrimSpace(target.InstallPath) == "" {
			//lint:ignore ST1005 Go is a product name and must retain its capitalization.
			return "", errors.New("Go component path is required")
		}
		return shellCommand(target.Manager, "version", "-m", target.InstallPath) + ` | awk '$1 == "mod" && !found {print $3; found=1}'`, nil
	case PackageUV:
		return shellCommand(target.Manager, "tool", "list") + " | awk -v target=" + runtimeutil.QuoteShell(target.Name) + ` '$1 == target && !found {print $2; found=1}'`, nil
	case PackageRuby:
		return shellCommand(target.Manager, "list", "--local", "--exact", target.Name), nil
	default:
		return "", errors.New("package ecosystem is unsupported")
	}
}

// PackageUpdateCommand returns the official package-manager command for a
// package/component. It never supplies privilege escalation.
func PackageUpdateCommand(target PackageTarget) (string, error) {
	if strings.TrimSpace(target.Manager) == "" || strings.TrimSpace(target.Name) == "" {
		return "", errors.New("package manager and name are required")
	}
	switch target.Ecosystem {
	case PackagePython:
		arguments := []string{"-m", "pip", "install", "--upgrade", target.Name}
		if target.UserInstall || userPackagePath(target.InstallPath) {
			arguments = []string{"-m", "pip", "install", "--user", "--upgrade", target.Name}
		}
		return shellCommand(target.Manager, arguments...), nil
	case PackageNode:
		return shellCommand(target.Manager, "install", "--global", target.Name+"@latest"), nil
	case PackageGo:
		return shellCommand(target.Manager, "install", target.Name+"@latest"), nil
	case PackageUV:
		return shellCommand(target.Manager, "tool", "upgrade", target.Name), nil
	case PackageRuby:
		arguments := []string{"install", "--no-document", target.Name}
		if target.UserInstall || userPackagePath(target.InstallPath) {
			arguments = []string{"install", "--user-install", "--no-document", target.Name}
		}
		return shellCommand(target.Manager, arguments...), nil
	default:
		return "", errors.New("package ecosystem is unsupported")
	}
}

func shellCommand(executable string, arguments ...string) string {
	command := runtimeutil.QuoteShell(executable)
	for _, argument := range arguments {
		command += " " + runtimeutil.QuoteShell(argument)
	}
	return command
}

func userPackagePath(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(path) == "" {
		return false
	}
	relative, err := filepath.Rel(home, filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
