package runtime

import (
	"context"
	"errors"
	"testing"
)

func resetSystemInfoCache() {
	cachedSystemInfo.Lock()
	cachedSystemInfo.info = SystemInfo{}
	cachedSystemInfo.valid = false
	cachedSystemInfo.Unlock()
}

func TestParseMacOSVersion(t *testing.T) {
	for _, test := range []struct {
		output string
		want   string
		err    bool
	}{
		{"15.5\n", "15.5", false},
		{" 14.4 \n", "14.4", false},
		{"\n", "", true},
	} {
		got, err := parseMacOSVersion(test.output)
		if got != test.want || (err != nil) != test.err {
			t.Fatalf("parseMacOSVersion(%q) = %q, %v", test.output, got, err)
		}
	}
}

func TestReadMacOSVersionPropagatesCancellationAndErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readMacOSVersion(ctx, func(context.Context) (string, error) { return "15.5", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled version read = %v", err)
	}
	want := errors.New("sw_vers unavailable")
	_, err = readMacOSVersion(context.Background(), func(context.Context) (string, error) { return "", want })
	if !errors.Is(err, want) {
		t.Fatalf("version read error = %v", err)
	}
}

func TestHostPlatformAndDistributionArchitecture(t *testing.T) {
	platform := HostPlatform()
	if platform.OS == "" || platform.Architecture == "" {
		t.Fatalf("HostPlatform() = %#v", platform)
	}
	if got := distributionArchitecture("amd64"); got != "x64" {
		t.Fatalf("amd64 alias = %q", got)
	}
	if got := distributionArchitecture("arm64"); got != "arm64" {
		t.Fatalf("arm64 alias = %q", got)
	}
	if got := ActionArchitecture(); got == "" {
		t.Fatal("ActionArchitecture() is empty")
	}
}

func TestDetectSystemInfoPlatformBranches(t *testing.T) {
	nonDarwin := SystemInfo{OS: "windows", Architecture: "amd64"}
	called := false
	got, err := detectSystemInfo(context.Background(), nonDarwin, func(context.Context) (string, error) {
		called = true
		return "", nil
	}, func() ([]byte, error) { return nil, nil })
	if err != nil || got != normalizeSystemInfo(nonDarwin) || called {
		t.Fatalf("non-darwin detect = %#v, %v, called=%t", got, err, called)
	}
	darwin := SystemInfo{OS: "darwin", Architecture: "arm64"}
	got, err = detectSystemInfo(context.Background(), darwin, func(context.Context) (string, error) { return "15.5", nil }, func() ([]byte, error) { return nil, nil })
	if err != nil || got.OSVersion != "15.5" {
		t.Fatalf("darwin detect = %#v, %v", got, err)
	}
	want := errors.New("version failed")
	if _, err := detectSystemInfo(context.Background(), darwin, func(context.Context) (string, error) { return "", want }, func() ([]byte, error) { return nil, nil }); !errors.Is(err, want) {
		t.Fatalf("darwin detect error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	actual, actualErr := DetectSystemInfo(ctx)
	if HostPlatform().OS == "darwin" && !errors.Is(actualErr, context.Canceled) {
		t.Fatalf("cancelled DetectSystemInfo() = %#v, %v", actual, actualErr)
	}
}

func TestMacOSVersionCachesOnlySuccessAndHonorsCancellation(t *testing.T) {
	original := executeMacOSVersion
	cachedMacOSVersion.Lock()
	originalCache := cachedMacOSVersion.value
	cachedMacOSVersion.value = ""
	cachedMacOSVersion.Unlock()
	t.Cleanup(func() {
		executeMacOSVersion = original
		cachedMacOSVersion.Lock()
		cachedMacOSVersion.value = originalCache
		cachedMacOSVersion.Unlock()
	})
	calls := 0
	executeMacOSVersion = func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("temporary failure")
		}
		return "15.5\n", nil
	}
	if _, err := macOSVersion(context.Background()); err == nil {
		t.Fatal("macOSVersion accepted failed read")
	}
	if got, err := macOSVersion(context.Background()); err != nil || got != "15.5" {
		t.Fatalf("macOSVersion() = %q, %v", got, err)
	}
	if got, err := macOSVersion(context.Background()); err != nil || got != "15.5" || calls != 2 {
		t.Fatalf("cached macOSVersion() = %q, %v, calls=%d", got, err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := macOSVersion(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cached macOSVersion() = %v", err)
	}
}

func TestParseOSReleaseAndNormalizeSystemInfo(t *testing.T) {
	info, err := systemInfoFromOSRelease(SystemInfo{OS: "linux", Architecture: "amd64"}, "ID=ubuntu\nVERSION_ID=\"24.04\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kernel != "linux" || info.Product != "Ubuntu" || info.Architecture != "x86_64" || info.FullName != "linux_ubuntu_24.04_x86_64" || !info.Supported() {
		t.Fatalf("ubuntu info = %#v", info)
	}
	for _, test := range []struct {
		name string
		info SystemInfo
		want bool
	}{
		{"mac arm", normalizeSystemInfo(SystemInfo{OS: "darwin", OSVersion: "15.5", Architecture: "arm64"}), true},
		{"debian x64", normalizeSystemInfo(SystemInfo{OS: "linux", Product: "Debian", OSVersion: "12", Architecture: "amd64"}), true},
		{"centos arm", normalizeSystemInfo(SystemInfo{OS: "linux", Product: "CentOS", OSVersion: "9", Architecture: "arm64"}), true},
		{"red hat x64", normalizeSystemInfo(SystemInfo{OS: "linux", Product: "Red Hat Enterprise Linux", OSVersion: "9", Architecture: "amd64"}), true},
		{"unknown distro", normalizeSystemInfo(SystemInfo{OS: "linux", Product: "Fedora", OSVersion: "42", Architecture: "amd64"}), false},
		{"windows", normalizeSystemInfo(SystemInfo{OS: "windows", Architecture: "amd64"}), false},
		{"unsupported arch", normalizeSystemInfo(SystemInfo{OS: "darwin", Architecture: "386"}), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.info.Supported(); got != test.want {
				t.Fatalf("Supported(%#v) = %t, want %t", test.info, got, test.want)
			}
		})
	}
}

func TestSystemInfoDistributionMappings(t *testing.T) {
	info := normalizeSystemInfo(SystemInfo{OS: "linux", Product: "Ubuntu", OSVersion: "24.04", Architecture: "amd64"})
	if got, ok := info.GoArchitecture(); !ok || got != "amd64" {
		t.Fatalf("GoArchitecture() = %q, %t", got, ok)
	}
	if got, ok := info.NodeArchiveArchitecture(); !ok || got != "x64" {
		t.Fatalf("NodeArchiveArchitecture() = %q, %t", got, ok)
	}
	if got := info.NodeArchivePlatform(); got != "linux" {
		t.Fatalf("NodeArchivePlatform() = %q", got)
	}
}

func TestOSReleaseUsesOnlyWhitelistedIDs(t *testing.T) {
	for _, test := range []struct {
		name, release, product string
		wantSupported          bool
	}{
		{"ubuntu", "ID=ubuntu\nVERSION_ID=24.04\n", "Ubuntu", true},
		{"debian", "ID=debian\nVERSION_ID=12\n", "Debian", true},
		{"centos", "ID=centos\nVERSION_ID=9\n", "CentOS", true},
		{"rhel", "ID=rhel\nVERSION_ID=9\n", "Red Hat", true},
		{"name spoof", "ID=fedora\nNAME=Ubuntu\nVERSION_ID=42\n", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			info, err := systemInfoFromOSRelease(SystemInfo{OS: "linux", Architecture: "amd64"}, test.release)
			if err != nil || info.Product != test.product || info.Supported() != test.wantSupported {
				t.Fatalf("systemInfoFromOSRelease() = %#v, %v", info, err)
			}
		})
	}
	for _, input := range []string{"", "NAME=Ubuntu\n", "ID=ubuntu\nbroken\n"} {
		if _, err := systemInfoFromOSRelease(SystemInfo{OS: "linux", Architecture: "amd64"}, input); err == nil {
			t.Fatalf("malformed os-release %q was accepted", input)
		}
	}
}

func TestDetectSystemInfoCachesSuccessButRetriesFailures(t *testing.T) {
	originalOS, originalArch, originalRead := hostGOOS, hostGOARCH, readOSRelease
	resetSystemInfoCache()
	t.Cleanup(func() {
		hostGOOS, hostGOARCH, readOSRelease = originalOS, originalArch, originalRead
		resetSystemInfoCache()
	})
	hostGOOS, hostGOARCH = "linux", "amd64"
	calls := 0
	readOSRelease = func() ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("temporary read failure")
		}
		return []byte("ID=ubuntu\nVERSION_ID=24.04\n"), nil
	}
	if _, err := DetectSystemInfo(context.Background()); err == nil {
		t.Fatal("failed read was cached")
	}
	info, err := DetectSystemInfo(context.Background())
	if err != nil || calls != 2 || info != HostPlatform() {
		t.Fatalf("successful retry = %#v, %v; calls=%d host=%#v", info, err, calls, HostPlatform())
	}
	if _, err := DetectSystemInfo(context.Background()); err != nil || calls != 2 {
		t.Fatalf("cached detection = %v; calls=%d", err, calls)
	}
}

func TestLinuxGitHubArtifactMatchingFailsClosed(t *testing.T) {
	info := normalizeSystemInfo(SystemInfo{OS: "linux", Product: "Ubuntu", Architecture: "amd64"})
	for _, name := range []string{"tool-x86_64.tar.gz", "tool-freebsd-x86_64.tar.gz", "tool-solaris-x86_64.tar.gz", "tool-win-x86_64.tar.gz", "tool_win_x86_64.tar.gz", "tool-linux-win_x64.zip", "tool-linux-mingw-x86_64.zip", "tool-linux-msvc-x86_64.zip", "tool-linux-darwin-x86_64.tar.gz", "tool-linux-arm64-x86_64.tar.gz"} {
		if info.MatchesGitHubArtifact(name) {
			t.Fatalf("Linux accepted incompatible artifact %q", name)
		}
	}
	if !info.MatchesGitHubArtifact("tool-linux-x86_64.tar.gz") {
		t.Fatal("Linux rejected its explicitly marked archive")
	}
	if !info.MatchesGitHubArtifact("tool-x86_64.deb") {
		t.Fatal("Linux rejected a deb artifact without a linux token")
	}
	rhel := normalizeSystemInfo(SystemInfo{OS: "linux", Product: "Red Hat", Architecture: "amd64"})
	if !rhel.MatchesGitHubArtifact("tool-x86_64.rpm") {
		t.Fatal("Linux rejected an rpm artifact without a linux token")
	}
}

func TestProviderMappingsAndShellCoverSupportedPlatforms(t *testing.T) {
	for _, test := range []struct {
		name, os, product, architecture, goArch, nodeKey, jetBrains, shell string
	}{
		{"mac intel", "darwin", "", "amd64", "amd64", "osx-x64-tar", "mac", "/bin/zsh"},
		{"mac arm", "darwin", "", "arm64", "arm64", "osx-arm64-tar", "macM1", "/bin/zsh"},
		{"linux intel", "linux", "Ubuntu", "amd64", "amd64", "linux-x64-tar", "linux", "/bin/sh"},
		{"linux arm", "linux", "Debian", "arm64", "arm64", "linux-arm64-tar", "linuxARM64", "/bin/sh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := normalizeSystemInfo(SystemInfo{OS: test.os, Product: test.product, Architecture: test.architecture})
			goArch, goOK := info.GoArchitecture()
			goPlatform, goPlatformOK := info.GoPlatform()
			nodeKey, nodeOK := info.NodeReleaseFileKey()
			jetBrains, jetBrainsOK := info.JetBrainsPlatformKey()
			if !goOK || !goPlatformOK || !nodeOK || !jetBrainsOK || goPlatform != test.os || goArch != test.goArch || nodeKey != test.nodeKey || jetBrains != test.jetBrains || info.Shell() != test.shell {
				t.Fatalf("mappings = go=%q/%q/%t node=%q/%t jetbrains=%q/%t shell=%q", goPlatform, goArch, goOK, nodeKey, nodeOK, jetBrains, jetBrainsOK, info.Shell())
			}
		})
	}
}
