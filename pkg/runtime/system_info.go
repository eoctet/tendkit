// Package runtime contains shared process and host runtime helpers.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// SystemInfo describes immutable host facts used for provider artifact selection.
type SystemInfo struct {
	// Kernel is the normalized operating-system kernel identifier.
	Kernel string
	OS     string
	// Product is the normalized operating-system product or Linux distribution.
	Product      string
	Architecture string
	OSVersion    string
	// FullName is a stable identifier suitable for logs and diagnostics.
	FullName string
}

var cachedSystemInfo struct {
	sync.Mutex
	info  SystemInfo
	valid bool
}

var cachedMacOSVersion struct {
	sync.Mutex
	value string
}

var executeMacOSVersion = func(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
	return string(output), err
}

var readOSRelease = func() ([]byte, error) { return os.ReadFile("/etc/os-release") }

var (
	hostGOOS   = runtime.GOOS
	hostGOARCH = runtime.GOARCH
)

// HostPlatform returns the successfully detected host snapshot. Before a
// successful detection it returns only the normalized Go runtime facts.
func HostPlatform() SystemInfo {
	cachedSystemInfo.Lock()
	defer cachedSystemInfo.Unlock()
	if cachedSystemInfo.valid {
		return cachedSystemInfo.info
	}
	return normalizeSystemInfo(SystemInfo{OS: hostGOOS, Architecture: hostGOARCH})
}

// DetectSystemInfo returns host information including macOS version. The
// version uses the stable read-only sw_vers interface and is cached only after
// a successful read.
func DetectSystemInfo(ctx context.Context) (SystemInfo, error) {
	cachedSystemInfo.Lock()
	if cachedSystemInfo.valid {
		info := cachedSystemInfo.info
		cachedSystemInfo.Unlock()
		return info, nil
	}
	cachedSystemInfo.Unlock()
	info, err := detectSystemInfo(ctx, normalizeSystemInfo(SystemInfo{OS: hostGOOS, Architecture: hostGOARCH}), macOSVersion, readOSRelease)
	if err != nil {
		return SystemInfo{}, err
	}
	cachedSystemInfo.Lock()
	defer cachedSystemInfo.Unlock()
	if !cachedSystemInfo.valid {
		cachedSystemInfo.info, cachedSystemInfo.valid = info, true
	}
	return cachedSystemInfo.info, nil
}

// IsSupportedSystem is the shared support-matrix predicate for application code.
func IsSupportedSystem(info SystemInfo) bool { return info.Supported() }

func detectSystemInfo(ctx context.Context, info SystemInfo, version func(context.Context) (string, error), osRelease func() ([]byte, error)) (SystemInfo, error) {
	info = normalizeSystemInfo(info)
	switch info.Kernel {
	case "linux":
		if err := ctx.Err(); err != nil {
			return SystemInfo{}, err
		}
		output, err := osRelease()
		if err != nil {
			return SystemInfo{}, err
		}
		return systemInfoFromOSRelease(info, string(output))
	case "darwin":
		value, err := version(ctx)
		if err != nil {
			return SystemInfo{}, err
		}
		info.OSVersion = value
		return normalizeSystemInfo(info), nil
	default:
		return info, nil
	}
}

// Supported reports whether this exact host combination is in the target matrix.
func (info SystemInfo) Supported() bool {
	info = normalizeSystemInfo(info)
	if info.Architecture != "x86_64" && info.Architecture != "arm64" {
		return false
	}
	if info.Kernel == "darwin" {
		return info.Product == "macOS"
	}
	return info.Kernel == "linux" && (info.Product == "Ubuntu" || info.Product == "Debian" || info.Product == "CentOS" || info.Product == "Red Hat")
}

// GoArchitecture returns the spelling used by Go distribution manifests.
func (info SystemInfo) GoArchitecture() (string, bool) {
	if !info.Supported() {
		return "", false
	}
	switch normalizeSystemInfo(info).Architecture {
	case "x86_64":
		return "amd64", true
	case "arm64":
		return "arm64", true
	default:
		return "", false
	}
}

// GoPlatform returns the operating-system spelling used by Go manifests.
func (info SystemInfo) GoPlatform() (string, bool) {
	info = normalizeSystemInfo(info)
	if !info.Supported() {
		return "", false
	}
	return info.Kernel, true
}

// NodeArchiveArchitecture returns the spelling used in Node release archives.
func (info SystemInfo) NodeArchiveArchitecture() (string, bool) {
	if !info.Supported() {
		return "", false
	}
	switch normalizeSystemInfo(info).Architecture {
	case "x86_64":
		return "x64", true
	case "arm64":
		return "arm64", true
	default:
		return "", false
	}
}

// NodeArchivePlatform returns Node's filename platform spelling.
func (info SystemInfo) NodeArchivePlatform() string { return normalizeSystemInfo(info).Kernel }

// NodeManifestPlatform returns Node's release-manifest platform spelling.
func (info SystemInfo) NodeManifestPlatform() string {
	if normalizeSystemInfo(info).Kernel == "darwin" {
		return "osx"
	}
	return "linux"
}

// NodeReleaseFileKey returns the Node release-manifest key for this target.
func (info SystemInfo) NodeReleaseFileKey() (string, bool) {
	arch, ok := info.NodeArchiveArchitecture()
	if !ok {
		return "", false
	}
	return info.NodeManifestPlatform() + "-" + arch + "-tar", true
}

// SupportsApplicationBundles reports whether macOS application-bundle APIs
// are available on this host.
func (info SystemInfo) SupportsApplicationBundles() bool {
	return normalizeSystemInfo(info).Kernel == "darwin"
}

// SupportsSparkle reports whether Sparkle can operate on this host.
func (info SystemInfo) SupportsSparkle() bool { return info.SupportsApplicationBundles() }

// JetBrainsPlatformKey returns the product-download key for this host.
func (info SystemInfo) JetBrainsPlatformKey() (string, bool) {
	info = normalizeSystemInfo(info)
	if !info.Supported() {
		return "", false
	}
	if info.Kernel == "darwin" {
		if info.Architecture == "arm64" {
			return "macM1", true
		}
		return "mac", true
	}
	if info.Architecture == "arm64" {
		return "linuxARM64", true
	}
	return "linux", true
}

// MatchesGitHubArtifact reports whether an automatic GitHub release asset is
// compatible with this target's operating system, distribution, and CPU.
func (info SystemInfo) MatchesGitHubArtifact(name string) bool {
	info = normalizeSystemInfo(info)
	if !info.Supported() {
		return false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || strings.Contains(name, "windows") || strings.Contains(name, "win32") || strings.Contains(name, "win64") || strings.HasPrefix(name, "win-") || strings.HasPrefix(name, "win_") || strings.Contains(name, "-win-") || strings.Contains(name, "-win_") || strings.Contains(name, "_win_") || strings.Contains(name, "mingw") || strings.Contains(name, "msvc") || strings.Contains(name, "freebsd") || strings.Contains(name, "solaris") {
		return false
	}
	archive := strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tar.xz") || strings.HasSuffix(name, ".tgz")
	if info.Kernel == "darwin" {
		if strings.Contains(name, "linux") {
			return false
		}
		if !(strings.HasSuffix(name, ".dmg") || strings.HasSuffix(name, ".pkg") || (archive && (strings.Contains(name, "darwin") || strings.Contains(name, "macos") || strings.Contains(name, "osx")))) {
			return false
		}
	} else if info.Kernel == "linux" {
		if strings.HasSuffix(name, ".dmg") || strings.HasSuffix(name, ".pkg") || strings.Contains(name, "darwin") || strings.Contains(name, "macos") || strings.Contains(name, "osx") {
			return false
		}
		packageAsset := strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".rpm")
		if !packageAsset && !strings.Contains(name, "linux") {
			return false
		}
		if strings.HasSuffix(name, ".deb") && !(info.Product == "Ubuntu" || info.Product == "Debian") {
			return false
		}
		if strings.HasSuffix(name, ".rpm") && !(info.Product == "CentOS" || info.Product == "Red Hat") {
			return false
		}
		if !(archive || packageAsset) {
			return false
		}
	} else {
		return false
	}
	arm := strings.Contains(name, "arm64") || strings.Contains(name, "aarch64")
	intel := strings.Contains(name, "amd64") || strings.Contains(name, "x64") || strings.Contains(name, "x86_64")
	if arm && intel {
		return false
	}
	if info.Architecture == "arm64" {
		return !intel || arm
	}
	return !arm || intel
}

// Shell returns the standard shell available on a supported target platform.
func (info SystemInfo) Shell() string {
	if normalizeSystemInfo(info).Kernel == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/sh"
}

func normalizeSystemInfo(info SystemInfo) SystemInfo {
	kernel := strings.ToLower(strings.TrimSpace(info.Kernel))
	if kernel == "" {
		kernel = strings.ToLower(strings.TrimSpace(info.OS))
	}
	architecture := strings.ToLower(strings.TrimSpace(info.Architecture))
	if architecture == "amd64" || architecture == "x64" {
		architecture = "x86_64"
	}
	product := strings.TrimSpace(info.Product)
	if kernel == "darwin" {
		product = "macOS"
	} else if kernel == "linux" {
		switch strings.ToLower(strings.ReplaceAll(product, " ", "")) {
		case "ubuntu":
			product = "Ubuntu"
		case "debian":
			product = "Debian"
		case "centos":
			product = "CentOS"
		case "redhat", "rhel", "redhatenterpriselinux":
			product = "Red Hat"
		}
	}
	info.Kernel, info.OS, info.Product, info.Architecture = kernel, kernel, product, architecture
	info.OSVersion = strings.TrimSpace(info.OSVersion)
	distribution := strings.ToLower(strings.ReplaceAll(product, " ", ""))
	if distribution == "" {
		distribution = "unknown"
	}
	version := info.OSVersion
	if version == "" {
		version = "unknown"
	}
	info.FullName = fmt.Sprintf("%s_%s_%s_%s", kernel, distribution, version, architecture)
	return info
}

func systemInfoFromOSRelease(info SystemInfo, output string) (SystemInfo, error) {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return SystemInfo{}, fmt.Errorf("invalid os-release line %q", line)
		}
		values[key] = strings.Trim(strings.TrimSpace(value), "\"")
	}
	if strings.TrimSpace(values["ID"]) == "" {
		return SystemInfo{}, errors.New("os-release ID is empty")
	}
	products := map[string]string{"ubuntu": "Ubuntu", "debian": "Debian", "centos": "CentOS", "rhel": "Red Hat", "redhat": "Red Hat", "redhatenterpriselinux": "Red Hat"}
	info.Product = products[strings.ToLower(values["ID"])]
	info.OSVersion = values["VERSION_ID"]
	return normalizeSystemInfo(info), nil
}

func macOSVersion(ctx context.Context) (string, error) {
	cachedMacOSVersion.Lock()
	defer cachedMacOSVersion.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cachedMacOSVersion.value != "" {
		return cachedMacOSVersion.value, nil
	}
	version, err := readMacOSVersion(ctx, executeMacOSVersion)
	if err != nil {
		return "", err
	}
	cachedMacOSVersion.value = version
	return version, nil
}

// ActionArchitecture returns the architecture spelling used by action templates
// and common distribution filenames.
func ActionArchitecture() string { return HostPlatform().Architecture }

func distributionArchitecture(goarch string) string {
	if goarch == "amd64" {
		return "x64"
	}
	return goarch
}

func readMacOSVersion(ctx context.Context, read func(context.Context) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	output, err := read(ctx)
	if err != nil {
		return "", err
	}
	return parseMacOSVersion(output)
}

func parseMacOSVersion(output string) (string, error) {
	version := strings.TrimSpace(output)
	if version == "" {
		return "", errors.New("macOS version is empty")
	}
	return version, nil
}
