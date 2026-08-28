package handler

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
)

type fixtureDirEntry struct {
	name string
	dir  bool
}

func (e fixtureDirEntry) Name() string { return e.name }
func (e fixtureDirEntry) IsDir() bool  { return e.dir }
func (e fixtureDirEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}
func (e fixtureDirEntry) Info() (fs.FileInfo, error) { return fixtureFileInfo{dir: e.dir}, nil }

func fixtureEntries(values ...fixtureDirEntry) []fs.DirEntry {
	entries := make([]fs.DirEntry, len(values))
	for index := range values {
		entries[index] = values[index]
	}
	return entries
}
func TestMacAppHandlerContract(t *testing.T) {
	t.Run("mac-app-inspection-accepts-only-strict-jetbrains-product-codes", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("inspectMacApp relies on the Darwin plist inspection contract")
		}
		appPath := filepath.Join(t.TempDir(), "PyCharm.app")
		infoPath := filepath.Join(appPath, "Contents", "Info.plist")
		productPath := filepath.Join(appPath, "Contents", "Resources", "product-info.json")
		if err := os.MkdirAll(filepath.Dir(productPath), 0o700); err != nil {
			t.Fatal(err)
		}
		plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.jetbrains.pycharm</string><key>CFBundleShortVersionString</key><string>2026.1</string></dict></plist>`
		if err := os.WriteFile(infoPath, []byte(plist), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			name, product string
			want          string
		}{
			{"valid", `{"productCode":"PY"}`, "PY"},
			{"lowercase", `{"productCode":"Py"}`, ""},
			{"missing", `{}`, ""},
		} {
			t.Run(test.name, func(t *testing.T) {
				if err := os.WriteFile(productPath, []byte(test.product), 0o600); err != nil {
					t.Fatal(err)
				}
				info, err := inspectMacApp(context.Background(), appPath)
				if err != nil || info.jetBrainsProductCode != test.want {
					t.Fatalf("product code=%q err=%v", info.jetBrainsProductCode, err)
				}
			})
		}
	})
	t.Run("mac-app-handler-scan-enumerates-filters-deduplicates-and-sorts", func(t *testing.T) {
		handler := NewMacApp(builtin.MacAppDefinitions(), []string{" COM.EXAMPLE.CUSTOM "})
		home := "/fixture/home"
		userApps := filepath.Join(home, userAppsDirectory)
		entries := map[string][]fs.DirEntry{
			systemAppsDirectory: fixtureEntries(
				fixtureDirEntry{name: "VS Code.app", dir: true},
				fixtureDirEntry{name: "Category Tool.app", dir: true},
				fixtureDirEntry{name: "Ignored.app", dir: true},
				fixtureDirEntry{name: "readme.txt"},
			),
			userApps: fixtureEntries(
				fixtureDirEntry{name: "Duplicate VS Code.app", dir: true},
				fixtureDirEntry{name: "Custom Tool.app", dir: true},
			),
		}
		handler.homeDir = func() (string, error) { return home, nil }
		handler.readDir = func(path string) ([]fs.DirEntry, error) { return entries[path], nil }
		infos := map[string]macInfo{
			filepath.Join(systemAppsDirectory, "VS Code.app"):       {name: "Visual Studio Code", bundleID: "com.microsoft.vscode", version: "1.99.0"},
			filepath.Join(systemAppsDirectory, "Category Tool.app"): {name: "Category Tool", bundleID: "com.example.category", category: "public.app-category.developer-tools", version: "2.0.0"},
			filepath.Join(systemAppsDirectory, "Ignored.app"):       {name: "Ignored", bundleID: "com.example.ignored"},
			filepath.Join(userApps, "Duplicate VS Code.app"):        {name: "Other VS Code", bundleID: "COM.MICROSOFT.VSCODE"},
			filepath.Join(userApps, "Custom Tool.app"):              {name: "Custom Tool", bundleID: "com.example.custom"},
		}
		handler.inspect = func(_ context.Context, path string) (macInfo, error) { return infos[path], nil }

		var progress []Progress
		result := handler.Scan(context.Background(), Request{Report: func(value Progress) { progress = append(progress, value) }})

		if !result.Complete || result.Err != nil {
			t.Fatalf("scan result = %#v", result)
		}
		if got := []string{result.Candidates[0].Application.Name, result.Candidates[1].Application.Name, result.Candidates[2].Application.Name}; !reflect.DeepEqual(got, []string{"Category Tool", "Custom Tool", "Visual Studio Code"}) {
			t.Fatalf("sorted candidates = %v", got)
		}
		if got := []string{progress[0].Subject, progress[1].Subject, progress[2].Subject, progress[3].Subject}; !reflect.DeepEqual(got, []string{"Visual Studio Code", "Category Tool", "Other VS Code", "Custom Tool"}) {
			t.Fatalf("progress = %v", got)
		}
		vsCode := result.Candidates[2]
		if vsCode.Application.Identity != "app:com.microsoft.vscode" || vsCode.Application.URL != "https://github.com/microsoft/vscode" || vsCode.CurrentVersion != "1.99.0" {
			t.Fatalf("VS Code candidate = %#v", vsCode)
		}
		if result.Candidates[1].Application.Identity != "app:com.example.custom" {
			t.Fatalf("custom candidate = %#v", result.Candidates[1])
		}
		for _, candidate := range result.Candidates {
			assertActiveProvider(t, candidate.Application.Provider.Type)
		}
	})
	t.Run("mac-app-handler-uses-injected-definitions-without-global-fallback", func(t *testing.T) {
		path := "/fixture/Injected.app"
		info := macInfo{name: "Injected Tool", bundleID: "com.example.injected.tool"}
		inspect := func(_ context.Context, gotPath string) (macInfo, error) {
			if gotPath != path {
				t.Fatalf("inspect path = %q, want %q", gotPath, path)
			}
			return info, nil
		}

		injected := NewMacApp([]builtin.MacAppDefinition{{BundleIDPrefix: "com.example.injected.", GitHubProject: "acme/injected"}}, nil)
		injected.inspect = inspect
		candidate, found, err := injected.ScanApplication(context.Background(), model.Application{Type: model.ApplicationTypeBundle, InstallPath: path}, Request{})
		if err != nil || !found || candidate.Application.URL != "https://github.com/acme/injected" {
			t.Fatalf("injected definition candidate=%#v found=%t err=%v", candidate, found, err)
		}

		noDefinitions := NewMacApp(nil, nil)
		noDefinitions.inspect = inspect
		if _, found, err := noDefinitions.ScanApplication(context.Background(), model.Application{Type: model.ApplicationTypeBundle, InstallPath: path}, Request{}); err != nil || found {
			t.Fatalf("nil definitions unexpectedly fell back to catalog: found=%t err=%v", found, err)
		}
	})
	t.Run("mac-app-handler-sparkle-target-and-bundle-id", func(t *testing.T) {
		handler := NewMacApp(nil, nil)
		path := "/fixture/Sparkle.app"
		handler.inspect = func(_ context.Context, gotPath string) (macInfo, error) {
			if gotPath != path {
				t.Fatalf("inspect path = %q", gotPath)
			}
			return macInfo{name: "Sparkle Tool", bundleID: "com.example.sparkle", category: "public.app-category.developer-tools", description: "fixture", version: "3.4.5", feed: "https://updates.example.invalid/app.xml"}, nil
		}

		candidate, found, err := handler.ScanApplication(context.Background(), model.Application{Type: model.ApplicationTypeBundle, InstallPath: path}, Request{})
		if err != nil || !found {
			t.Fatalf("found=%t err=%v", found, err)
		}
		application := candidate.Application
		if application.Provider.Type != model.ProviderSparkle || application.UpdateMode != model.ModeDownload || application.Package != "" || application.Provider.Actions != nil {
			t.Fatalf("Sparkle application = %#v", application)
		}
		if candidate.CurrentVersion != "3.4.5" || !reflect.DeepEqual(candidate.Aliases, []string{"bundle:com.example.sparkle"}) {
			t.Fatalf("Sparkle candidate = %#v", candidate)
		}
		if _, found, err := handler.ScanApplication(context.Background(), model.Application{Type: model.ApplicationTypeCLI, InstallPath: path}, Request{}); err != nil || found {
			t.Fatalf("non-bundle target found=%t err=%v", found, err)
		}
		if bundleID, err := handler.BundleID(context.Background(), path); err != nil || bundleID != "com.example.sparkle" {
			t.Fatalf("BundleID = %q, %v", bundleID, err)
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := handler.BundleID(cancelled, path); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled BundleID error = %v", err)
		}
	})
	t.Run("mac-app-handler-maps-jet-brains-product-code-to-canonical-provider-package", func(t *testing.T) {
		for _, test := range []struct {
			productCode, canonical string
			found                  bool
		}{
			{"IU", "IIU", true}, {"PY", "PCP", true}, {"PC", "PCP", true}, {"DB", "DG", true}, {"QA", "", false},
		} {
			got, found := canonicalJetBrainsProduct(test.productCode)
			if got != test.canonical || found != test.found {
				t.Fatalf("catalog %s = %q,%t", test.productCode, got, found)
			}
		}
		handler := NewMacApp(nil, nil)
		path := "/fixture/PyCharm.app"
		handler.inspect = func(_ context.Context, gotPath string) (macInfo, error) {
			return macInfo{name: "PyCharm", bundleID: "com.jetbrains.pycharm", category: "public.app-category.developer-tools", jetBrainsProductCode: "PY"}, nil
		}
		candidate, found, err := handler.ScanApplication(context.Background(), model.Application{Type: model.ApplicationTypeBundle, InstallPath: path}, Request{})
		if err != nil || !found {
			t.Fatalf("found=%t err=%v", found, err)
		}
		if got := candidate.Application; !got.Enabled || got.UpdateMode != model.ModeCheck || got.Provider.Type != model.ProviderJetBrains || got.Package != "PCP" {
			t.Fatalf("JetBrains candidate = %#v", got)
		}

		handler.inspect = func(_ context.Context, _ string) (macInfo, error) {
			return macInfo{name: "Unknown", bundleID: "com.jetbrains.unknown", category: "public.app-category.developer-tools", jetBrainsProductCode: "NOPE"}, nil
		}
		candidate, found, err = handler.ScanApplication(context.Background(), model.Application{Type: model.ApplicationTypeBundle, InstallPath: path}, Request{})
		if err != nil || !found || candidate.Application.Enabled || candidate.Application.Provider.Type != model.ProviderDefault || candidate.Application.Package != "" {
			t.Fatalf("unmapped JetBrains candidate = %#v, found=%t, err=%v", candidate.Application, found, err)
		}
	})
	t.Run("mac-app-handler-scan-handles-directory-and-home-errors", func(t *testing.T) {
		readErr := errors.New("read directory")
		t.Run("missing system directory is skipped", func(t *testing.T) {
			handler := NewMacApp(nil, nil)
			handler.homeDir = func() (string, error) { return "/fixture/home", nil }
			handler.readDir = func(path string) ([]fs.DirEntry, error) {
				if path == systemAppsDirectory {
					return nil, fs.ErrNotExist
				}
				return nil, nil
			}
			if result := handler.Scan(context.Background(), Request{}); !result.Complete || result.Err != nil {
				t.Fatalf("result = %#v", result)
			}
		})
		t.Run("read directory error is incomplete", func(t *testing.T) {
			handler := NewMacApp(nil, nil)
			handler.readDir = func(string) ([]fs.DirEntry, error) { return nil, readErr }
			result := handler.Scan(context.Background(), Request{})
			if result.Complete || !errors.Is(result.Err, readErr) {
				t.Fatalf("result = %#v", result)
			}
		})
		t.Run("home directory error still scans system applications", func(t *testing.T) {
			handler := NewMacApp(nil, nil)
			handler.homeDir = func() (string, error) { return "", errors.New("home directory") }
			handler.readDir = func(path string) ([]fs.DirEntry, error) {
				if path != systemAppsDirectory {
					t.Fatalf("unexpected directory %q", path)
				}
				return nil, nil
			}
			if result := handler.Scan(context.Background(), Request{}); !result.Complete || result.Err != nil {
				t.Fatalf("result = %#v", result)
			}
		})
	})
	t.Run("mac-app-handler-stops-when-inspection-cancels-context", func(t *testing.T) {
		handler := NewMacApp(nil, nil)
		handler.homeDir = func() (string, error) { return "", errors.New("not needed") }
		handler.readDir = func(string) ([]fs.DirEntry, error) {
			return fixtureEntries(fixtureDirEntry{name: "Tool.app", dir: true}), nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		handler.inspect = func(_ context.Context, path string) (macInfo, error) {
			cancel()
			return macInfo{path: path, name: "Tool", bundleID: "com.example.tool", category: "public.app-category.developer-tools"}, nil
		}

		result := handler.Scan(ctx, Request{})
		if result.Complete || !errors.Is(result.Err, context.Canceled) || len(result.Candidates) != 0 {
			t.Fatalf("scan did not stop after inspection cancellation: %#v", result)
		}
	})
	t.Run("mac-app-handler-propagates-inspection-errors", func(t *testing.T) {
		inspectErr := errors.New("inspect failed")
		path := "/fixture/Broken.app"
		newHandler := func() *MacAppHandler {
			handler := NewMacApp(nil, nil)
			handler.inspect = func(_ context.Context, _ string) (macInfo, error) {
				return macInfo{}, inspectErr
			}
			return handler
		}

		t.Run("scan is incomplete", func(t *testing.T) {
			handler := newHandler()
			handler.homeDir = func() (string, error) { return "", errors.New("not needed") }
			handler.readDir = func(string) ([]fs.DirEntry, error) {
				return fixtureEntries(fixtureDirEntry{name: "Broken.app", dir: true}), nil
			}
			result := handler.Scan(context.Background(), Request{})
			if result.Complete || !errors.Is(result.Err, inspectErr) || len(result.Candidates) != 0 {
				t.Fatalf("scan result = %#v", result)
			}
		})
		t.Run("target returns error", func(t *testing.T) {
			_, found, err := newHandler().ScanApplication(context.Background(), model.Application{Type: model.ApplicationTypeBundle, InstallPath: path}, Request{})
			if found || !errors.Is(err, inspectErr) {
				t.Fatalf("found=%t err=%v", found, err)
			}
		})
		t.Run("bundle ID returns error", func(t *testing.T) {
			bundleID, err := newHandler().BundleID(context.Background(), path)
			if bundleID != "" || !errors.Is(err, inspectErr) {
				t.Fatalf("bundleID=%q err=%v", bundleID, err)
			}
		})
	})
}
