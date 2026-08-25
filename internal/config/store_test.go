package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func testUnifiedStore(t *testing.T) store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	return newStore(path, path+".lock")
}
func testUnifiedConfig() model.Config {
	value := defaultConfig()
	value.Apps = []model.Application{{ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: "/tmp/sample", Enabled: true, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "check", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}}
	return value
}

func providerConfig(kind model.ProviderType, version, check, update string, download *model.Download, install string) model.ProviderConfig {
	if kind == model.ProviderDefault && version == "" && (check != "" || update != "" || download != nil || install != "") {
		version = "version"
	}
	provider := model.ProviderConfig{Type: kind}
	if version != "" || check != "" || update != "" || download != nil || install != "" {
		provider.Actions = &model.ProviderActions{Version: version, Check: check, Update: update, Download: download, Install: install}
	}
	return provider
}

func TestTargetProviderAndDownloaderSchema(t *testing.T) {
	valid := func(t *testing.T) []byte {
		t.Helper()
		data, err := json.Marshal(defaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		document["apps"] = []any{map[string]any{
			"id": "sample", "name": "Sample", "type": model.ApplicationTypeCLI,
			"install_path": "/tmp/sample", "enabled": true, "update_mode": "check",
			"provider":       map[string]any{"type": "default", "actions": map[string]any{"version": "printf 1.0.0", "check": "printf 1.0.0"}},
			"status_managed": map[string]any{"update_status": model.StatusUnchecked},
		}}
		return mustMarshalJSON(t, document)
	}

	t.Run("accepts target object form", func(t *testing.T) {
		store := testUnifiedStore(t)
		if err := os.WriteFile(store.ConfigPath, valid(t), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.load(); err != nil {
			t.Fatalf("target schema rejected: %v", err)
		}
	})

	for name, mutate := range map[string]func(map[string]any){
		"provider string": func(document map[string]any) { document["apps"].([]any)[0].(map[string]any)["provider"] = "none" },
		"operation command": func(document map[string]any) {
			document["apps"].([]any)[0].(map[string]any)["operation_command"] = map[string]any{}
		},
		"downloader binary": func(document map[string]any) {
			document["settings"].(map[string]any)["downloader"].(map[string]any)["binary"] = "aria2c"
		},
		"continue download": func(document map[string]any) {
			document["settings"].(map[string]any)["downloader"].(map[string]any)["continue_download"] = true
		},
		"split number": func(document map[string]any) {
			document["settings"].(map[string]any)["downloader"].(map[string]any)["split_num"] = 64
		},
		"max connection number": func(document map[string]any) {
			document["settings"].(map[string]any)["downloader"].(map[string]any)["max_connection_num"] = 10
		},
		"empty actions": func(document map[string]any) {
			document["apps"].([]any)[0].(map[string]any)["provider"].(map[string]any)["actions"] = map[string]any{}
		},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(valid(t), &document); err != nil {
				t.Fatal(err)
			}
			mutate(document)
			store := testUnifiedStore(t)
			if err := os.WriteFile(store.ConfigPath, mustMarshalJSON(t, document), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.load(); err == nil {
				t.Fatal("legacy or noncanonical schema accepted")
			}
		})
	}
}

func TestStoreRejectsInstallWithoutUpdateAction(t *testing.T) {
	store := testUnifiedStore(t)
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: "/tmp/sample", Enabled: true, UpdateMode: model.ModeInstall,
		Provider: providerConfig(model.ProviderDefault, "", "", "", nil, "installer"), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ConfigPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load(); err == nil {
		t.Fatal("install without update action was accepted")
	}
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestUnifiedConfigPersistsSchemaV1AppsAndStatus(t *testing.T) {
	store := testUnifiedStore(t)
	value := testUnifiedConfig()
	value.Apps[0].StatusManaged.CurrentVersion = "1.2.3"
	value.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"sample": {"name": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00+08:00"}}}
	if err := store.createIfMissing(value); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil || got.Apps[0].StatusManaged.CurrentVersion != "1.2.3" || got.ScanVersionControl["sample"]["name"].Fingerprint == "" {
		t.Fatalf("%#v %v", got, err)
	}
}
func TestLoadConfigPersistsV1ScanVersionControl(t *testing.T) {
	store := testUnifiedStore(t)
	value := testUnifiedConfig()
	value.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"sample": {"description": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00+08:00"}}}
	if err := store.createIfMissing(value); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil || got.ScanVersionControl["sample"]["description"].Fingerprint == "" {
		t.Fatalf("%#v %v", got, err)
	}
}

func TestNonMacOSLoadBacksUpAndCleansApplicationConfiguration(t *testing.T) {
	store := testUnifiedStore(t)
	store.systemInfo = func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "linux"} }
	catalog := testUnifiedConfig()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	catalog.Settings.Scan.Application = true
	catalog.Apps = append(catalog.Apps, model.Application{ID: "bundle", Name: "Bundle", Type: model.ApplicationTypeBundle, InstallPath: "/Applications/Bundle.app", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}})
	catalog.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"bundle": {"name": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00Z"}}}
	raw, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(store.ConfigPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.Scan.Application || len(got.Apps) != 1 || len(got.ScanVersionControl) != 0 {
		t.Fatalf("migration = %#v", got)
	}
	backups, err := filepath.Glob(store.ConfigPath + ".backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(raw) {
		t.Fatal("backup did not preserve original bytes")
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions: %v %v", info, err)
	}
	runLog, err := os.ReadFile(filepath.Join(catalog.Settings.LogDir, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runLog), "config_application_migrated") || !strings.Contains(string(runLog), "application configuration migrated") {
		t.Fatalf("migration warning = %s", runLog)
	}
	if _, err := store.load(); err != nil {
		t.Fatal(err)
	}
	backups, _ = filepath.Glob(store.ConfigPath + ".backup-*")
	if len(backups) != 1 {
		t.Fatalf("idempotent load created %d backups", len(backups))
	}
}

func TestNonMacOSCreateIfMissingAppliesApplicationInvariantWithoutMigrationArtifacts(t *testing.T) {
	store := testUnifiedStore(t)
	store.systemInfo = func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "linux"} }
	catalog := testUnifiedConfig()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	catalog.Settings.Scan.Application = true
	catalog.Apps = append(catalog.Apps, model.Application{ID: "bundle", Name: "Bundle", Type: model.ApplicationTypeBundle, InstallPath: "/Applications/Bundle.app", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}})
	if err := store.createIfMissing(catalog); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.Scan.Application || len(got.Apps) != 1 {
		t.Fatalf("fresh config = %#v", got)
	}
	backups, _ := filepath.Glob(store.ConfigPath + ".backup-*")
	if len(backups) != 0 {
		t.Fatalf("fresh config created backups: %v", backups)
	}
	if _, err := os.Stat(filepath.Join(catalog.Settings.LogDir, "run.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh config wrote migration log: %v", err)
	}
}

func TestNonMacOSSaveAppliesApplicationInvariantWithoutMigrationArtifacts(t *testing.T) {
	store := testUnifiedStore(t)
	store.systemInfo = func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "linux"} }
	catalog := testUnifiedConfig()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	if err := store.createIfMissing(catalog); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.loadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	draft := snapshot.Config
	draft.Settings.Scan.Application = true
	draft.Apps = append(draft.Apps, model.Application{ID: "bundle", Name: "Bundle", Type: model.ApplicationTypeBundle, InstallPath: "/Applications/Bundle.app", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}})
	draft.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{
		"sample": {"name": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-16T00:00:00Z"}},
		"bundle": {"name": {Fingerprint: strings.Repeat("b", 64), RecordedAt: "2026-08-16T00:00:00Z"}},
	}
	if err := store.saveSnapshot(snapshot.Revision, draft); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.Scan.Application || len(got.Apps) != 1 || got.Apps[0].ID != "sample" {
		t.Fatalf("saved config = %#v", got)
	}
	if _, exists := got.ScanVersionControl["bundle"]; exists || got.ScanVersionControl["sample"]["name"].Fingerprint == "" {
		t.Fatalf("scan version control = %#v", got.ScanVersionControl)
	}
	backups, _ := filepath.Glob(store.ConfigPath + ".backup-*")
	if len(backups) != 0 {
		t.Fatalf("save created migration backups: %v", backups)
	}
	if _, err := os.Stat(filepath.Join(catalog.Settings.LogDir, "run.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("save wrote migration log: %v", err)
	}
	var persisted model.Config
	if err := readConfigJSON(store.ConfigPath, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Settings.Scan.Application || len(persisted.Apps) != 1 {
		t.Fatalf("persisted config = %#v", persisted)
	}
}

func TestNonMacOSReloadCleansExternalApplicationConfiguration(t *testing.T) {
	store := testUnifiedStore(t)
	store.systemInfo = func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "linux"} }
	catalog := testUnifiedConfig()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	if err := store.createIfMissing(catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load(); err != nil {
		t.Fatal(err)
	}
	catalog.Settings.Scan.Application = true
	catalog.Apps = append(catalog.Apps, model.Application{ID: "bundle", Name: "Bundle", Type: model.ApplicationTypeBundle, InstallPath: "/Applications/Bundle.app", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}})
	data, _ := json.Marshal(catalog)
	if err := os.WriteFile(store.ConfigPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.reload()
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Config
	if got.Settings.Scan.Application || len(got.Apps) != 1 {
		t.Fatalf("reload migration = %#v", got)
	}
}

func TestMacOSLoadLeavesApplicationConfigurationUntouched(t *testing.T) {
	store := testUnifiedStore(t)
	store.systemInfo = func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "darwin"} }
	catalog := testUnifiedConfig()
	catalog.Settings.Scan.Application = true
	catalog.Apps = append(catalog.Apps, model.Application{ID: "bundle", Name: "Bundle", Type: model.ApplicationTypeBundle, InstallPath: "/Applications/Bundle.app", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}})
	if err := store.createIfMissing(catalog); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Settings.Scan.Application || len(got.Apps) != 2 {
		t.Fatalf("macOS changed config: %#v", got)
	}
	backups, _ := filepath.Glob(store.ConfigPath + ".backup-*")
	if len(backups) != 0 {
		t.Fatalf("unexpected backups: %v", backups)
	}
}

func TestNonMacOSBackupFailureLeavesConfigAndCacheUnchanged(t *testing.T) {
	store := testUnifiedStore(t)
	store.systemInfo = func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "linux"} }
	store.backup = func(string, []byte) (string, error) { return "", errors.New("backup failed") }
	catalog := testUnifiedConfig()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	catalog.Settings.Scan.Application = true
	data, _ := json.Marshal(catalog)
	if err := os.WriteFile(store.ConfigPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load(); err == nil {
		t.Fatal("load succeeded despite backup failure")
	}
	after, err := os.ReadFile(store.ConfigPath)
	if err != nil || string(after) != string(data) {
		t.Fatalf("config changed after failed backup: %v", err)
	}
	if store.runtime().loaded {
		t.Fatal("cache changed after failed backup")
	}
}

func TestNonMacOSLoggerInitializationFailureDoesNotInterruptMigration(t *testing.T) {
	store := testUnifiedStore(t)
	store.systemInfo = func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "linux"} }
	baseline := testUnifiedConfig()
	if err := store.createIfMissing(baseline); err != nil {
		t.Fatal(err)
	}
	blockedLogDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedLogDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := testUnifiedConfig()
	legacy.Settings.LogDir = blockedLogDir
	legacy.Settings.Scan.Application = true
	legacy.Apps = append(legacy.Apps, model.Application{ID: "bundle", Name: "Bundle", Type: model.ApplicationTypeBundle, InstallPath: "/Applications/Bundle.app", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}})
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ConfigPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.reload()
	if err != nil {
		t.Fatalf("reload was interrupted by logger initialization failure: %v", err)
	}
	for _, app := range reloaded.Config.Apps {
		if app.ID == "bundle" {
			t.Fatalf("unsupported application was not migrated: %#v", reloaded.Config.Apps)
		}
	}
	cache := store.runtime()
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	if !cache.loaded || !reflect.DeepEqual(cache.catalog, reloaded.Config) {
		t.Fatalf("cache was not updated after successful migration: loaded=%v catalog=%#v", cache.loaded, cache.catalog)
	}
	backups, _ := filepath.Glob(store.ConfigPath + ".backup-*")
	if len(backups) != 1 {
		t.Fatalf("successful migration backups = %v", backups)
	}
}

func TestNonMacOSMigrationSaveFailurePreservesBackupAndCache(t *testing.T) {
	store := testUnifiedStore(t)
	store.systemInfo = func() runtimeutil.SystemInfo { return runtimeutil.SystemInfo{Kernel: "linux"} }
	catalog := testUnifiedConfig()
	catalog.Settings.LogDir = filepath.Join(t.TempDir(), "logs")
	catalog.Settings.Scan.Application = true
	data, _ := json.Marshal(catalog)
	if err := os.WriteFile(store.ConfigPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store.rename = func(string, string) error { return errors.New("save failed") }
	if _, err := store.load(); err == nil {
		t.Fatal("load succeeded despite save failure")
	}
	after, err := os.ReadFile(store.ConfigPath)
	if err != nil || string(after) != string(data) {
		t.Fatalf("config changed after failed save: %v", err)
	}
	backups, _ := filepath.Glob(store.ConfigPath + ".backup-*")
	if len(backups) != 1 || store.runtime().loaded {
		t.Fatalf("backups=%v cache=%v", backups, store.runtime().loaded)
	}
}
func TestLoadConfigRejectsInvalidScanVersionControl(t *testing.T) {
	store := testUnifiedStore(t)
	value := testUnifiedConfig()
	value.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"sample": {"x": {Fingerprint: "bad", RecordedAt: "bad"}}}
	if err := store.createIfMissing(value); err == nil {
		t.Fatal("accepted invalid scan_version_control")
	}
}
func TestSyncDirectoryAfterAtomicWrite(t *testing.T) {
	store := testUnifiedStore(t)
	if err := store.atomicWriteJSON(store.ConfigPath, testUnifiedConfig()); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectory(filepath.Dir(store.ConfigPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.ConfigPath); err != nil {
		t.Fatal(err)
	}
}
func TestCreateJSONIfMissingDoesNotReplaceExistingFile(t *testing.T) {
	store := testUnifiedStore(t)
	first := testUnifiedConfig()
	first.Settings.Language = "en"
	if err := store.createIfMissing(first); err != nil {
		t.Fatal(err)
	}
	next := testUnifiedConfig()
	next.Settings.Language = "zh"
	if err := store.createIfMissing(next); !errors.Is(err, os.ErrExist) {
		t.Fatalf("%v", err)
	}
	got, err := store.load()
	if err != nil || got.Settings.Language != "en" {
		t.Fatalf("%#v %v", got, err)
	}
}
func TestSaveRenameFailurePreservesOriginalConfigAndCache(t *testing.T) {
	store := testUnifiedStore(t)
	first := testUnifiedConfig()
	first.Settings.Language = "en"
	if err := store.createIfMissing(first); err != nil {
		t.Fatal(err)
	}
	next := first
	next.Settings.Language = "zh"
	store.rename = func(string, string) error { return errors.New("rename") }
	snapshot, err := store.loadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveSnapshot(snapshot.Revision, next); err == nil {
		t.Fatal("save succeeded")
	}
	got, err := store.load()
	if err != nil || got.Settings.Language != "en" {
		t.Fatalf("%#v %v", got, err)
	}
}
func TestSaveRejectsStaleExternalConfig(t *testing.T) {
	store := testUnifiedStore(t)
	if err := store.createIfMissing(testUnifiedConfig()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.loadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ConfigPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.withLock(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.saveSnapshot(snapshot.Revision, testUnifiedConfig()); !errors.Is(err, ErrExternalConfigChange) {
		t.Fatalf("%v", err)
	}
}
func TestSaveUpdatesCachedConfig(t *testing.T) {
	store := testUnifiedStore(t)
	value := testUnifiedConfig()
	if err := store.createIfMissing(value); err != nil {
		t.Fatal(err)
	}
	value.Settings.Language = "en"
	snapshot, err := store.loadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveSnapshot(snapshot.Revision, value); err != nil {
		t.Fatal(err)
	}
	got, err := store.load()
	if err != nil || got.Settings.Language != "en" {
		t.Fatalf("%#v %v", got, err)
	}
}

func TestLoadKeepsMemorySnapshotUntilExplicitReload(t *testing.T) {
	store := testUnifiedStore(t)
	value := testUnifiedConfig()
	value.Apps[0].Name = "First"
	if err := store.createIfMissing(value); err != nil {
		t.Fatal(err)
	}
	baseline, err := os.Stat(store.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement := value
	replacement.Apps[0].Name = "Other"
	temporary, err := prepareJSONFile(store.ConfigPath, replacement)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, store.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(store.ConfigPath, baseline.ModTime(), baseline.ModTime()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != baseline.Size() || !info.ModTime().Equal(baseline.ModTime()) {
		t.Fatalf("replacement metadata differs: size=%d/%d mtime=%v/%v", info.Size(), baseline.Size(), info.ModTime(), baseline.ModTime())
	}
	loaded, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Apps[0].Name != "First" {
		t.Fatalf("ordinary load absorbed an external replacement: %#v", loaded.Apps[0])
	}
	reloaded, err := store.reload()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Config.Apps[0].Name != "Other" {
		t.Fatalf("explicit reload did not adopt replacement: %#v", reloaded.Config.Apps[0])
	}
}

func TestCachedLoadDoesNotStatOrHashFile(t *testing.T) {
	store := testUnifiedStore(t)
	if err := store.createIfMissing(testUnifiedConfig()); err != nil {
		t.Fatal(err)
	}
	store.stat = func(string) (os.FileInfo, error) { t.Fatal("cached Load called stat"); return nil, nil }
	store.hash = func(string) (string, error) { t.Fatal("cached Load called hash"); return "", nil }
	if _, err := store.load(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveSnapshotRejectsStaleOperationRevision(t *testing.T) {
	store := testUnifiedStore(t)
	if err := store.createIfMissing(testUnifiedConfig()); err != nil {
		t.Fatal(err)
	}
	first, _ := store.loadSnapshot()
	second, _ := store.loadSnapshot()
	first.Config.Settings.Language = "en"
	if err := store.saveSnapshot(first.Revision, first.Config); err != nil {
		t.Fatal(err)
	}
	second.Config.Settings.Language = "zh"
	if err := store.saveSnapshot(second.Revision, second.Config); !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("stale operation was not rejected: %v", err)
	}
}

func TestReloadFailurePreservesMemorySnapshot(t *testing.T) {
	store := testUnifiedStore(t)
	value := testUnifiedConfig()
	value.Apps[0].Name = "Memory"
	if err := store.createIfMissing(value); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ConfigPath, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.reload(); err == nil {
		t.Fatal("invalid external config was reloaded")
	}
	loaded, err := store.load()
	if err != nil || loaded.Apps[0].Name != "Memory" {
		t.Fatalf("reload failure changed cache: %#v %v", loaded, err)
	}
}

func TestProcessLockRejectsSecondStore(t *testing.T) {
	store := testUnifiedStore(t)
	other := newStore(store.ConfigPath, store.LockPath)
	if err := store.acquireProcessLock(); err != nil {
		t.Fatal(err)
	}
	defer store.releaseProcessLock()
	if err := other.acquireProcessLock(); err == nil {
		t.Fatal("second lock accepted")
	}
}
func TestLoadConfigRejectsSchemaV2(t *testing.T) {
	store := testUnifiedStore(t)
	if err := os.WriteFile(store.ConfigPath, []byte(`{"schema_version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load(); err == nil {
		t.Fatal("schema v2 accepted")
	}
}
func TestCatalogExecutionSecurityRejectsWritableAndSymlinkCatalogs(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newStore(path, path+".lock")
	if err := store.validateExecutionSecurity(); err != nil {
		t.Fatalf("secure catalog rejected: %v", err)
	}
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	if err := store.validateExecutionSecurity(); err == nil {
		t.Fatal("group-writable catalog accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := newStore(link, link+".lock").validateExecutionSecurity(); err == nil {
		t.Fatal("symlink catalog accepted")
	}
}

func TestCatalogExecutionSecurityRejectsWritableParentDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o770); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newStore(path, path+".lock").validateExecutionSecurity(); err == nil {
		t.Fatal("catalog in group-writable parent was accepted")
	}
}

func TestLoadConfigUsesApplicationTypeField(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: "cli", InstallPath: "sample", Enabled: true,
		UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "check", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	obsoleteField := "ki" + "nd"
	if !strings.Contains(encoded, `"type":"cli"`) || strings.Contains(encoded, `"`+obsoleteField+`"`) {
		t.Fatalf("unexpected application encoding: %s", encoded)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newStore(path, path+".lock")
	if _, err := store.load(); err != nil {
		t.Fatalf("apps.type was not accepted: %v", err)
	}
	legacy := strings.Replace(encoded, `"type":"cli"`, `"`+obsoleteField+`":"cli"`, 1)
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.reload()
	if err == nil || !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), obsoleteField) {
		t.Fatalf("obsolete application type field was not rejected: %v", err)
	}
}

func TestLoadConfigUsesScanApplicationField(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	catalog := defaultConfig()
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if !strings.Contains(encoded, `"application":true`) || strings.Contains(encoded, `"scan":{"path":true,"apps":true`) {
		t.Fatalf("unexpected scan settings encoding: %s", encoded)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newStore(path, path+".lock")
	if _, err := store.load(); err != nil {
		t.Fatalf("settings.scan.application was not accepted: %v", err)
	}
	legacy := strings.Replace(encoded, `"application":true`, `"apps":true`, 1)
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.reload()
	if err == nil || !strings.Contains(err.Error(), "settings.scan.application") {
		t.Fatalf("legacy settings.scan.apps was not rejected: %v", err)
	}
}

func TestLoadConfigRejectsMissingBundleIDList(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	data, err := json.Marshal(defaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	missing := strings.Replace(string(data), `"bundle_id":[],`, "", 1)
	if missing == string(data) {
		t.Fatalf("default catalog did not encode settings.scan.bundle_id: %s", data)
	}
	if err := os.WriteFile(path, []byte(missing), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newStore(path, path+".lock")
	if _, err := store.load(); err == nil || !strings.Contains(err.Error(), "settings.scan.bundle_id") {
		t.Fatalf("missing settings.scan.bundle_id was accepted: %v", err)
	}
}

func TestLoadConfigRequiresEveryScanSwitch(t *testing.T) {
	for _, key := range []string{"path", "application", "python", "node", "go", "uv", "ruby"} {
		t.Run(key, func(t *testing.T) {
			data, err := json.Marshal(defaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			scan := document["settings"].(map[string]any)["scan"].(map[string]any)
			if key == "path" || key == "application" {
				delete(scan, key)
			} else {
				delete(scan["packages"].(map[string]any), key)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, mustMarshalJSON(t, document), 0o600); err != nil {
				t.Fatal(err)
			}
			store := newStore(path, path+".lock")
			if _, err := store.load(); err == nil {
				t.Fatalf("missing scan switch %q was accepted", key)
			}
		})
	}
}

func TestValidateConfigRejectsOutOfRangeGlobalTimeout(t *testing.T) {
	for _, timeout := range []int{0, maxTimeoutSeconds + 1} {
		catalog := defaultConfig()
		catalog.Settings.TimeoutSeconds = timeout
		if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
			t.Fatalf("timeout_seconds=%d: expected timeout validation, got %v", timeout, err)
		}
	}
}

func TestValidateConfigRejectsInvalidLanguage(t *testing.T) {
	catalog := defaultConfig()
	catalog.Settings.Language = "fr"
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "settings.language") {
		t.Fatalf("expected language validation, got %v", err)
	}
}

func TestValidateConfigAcceptsCustomBundleIDs(t *testing.T) {
	catalog := defaultConfig()
	catalog.Settings.Scan.BundleID = []string{"md.obsidian", "com.example.Editor-Preview"}
	if err := validateConfig(catalog); err != nil {
		t.Fatalf("valid custom Bundle IDs were rejected: %v", err)
	}
}

func TestValidateConfigRejectsInvalidOrDuplicateBundleIDs(t *testing.T) {
	tests := [][]string{
		{""},
		{"obsidian"},
		{"md.obsidian", "MD.OBSIDIAN"},
	}
	for _, bundleIDs := range tests {
		catalog := defaultConfig()
		catalog.Settings.Scan.BundleID = bundleIDs
		if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "settings.scan.bundle_id") {
			t.Fatalf("invalid Bundle IDs %#v were accepted: %v", bundleIDs, err)
		}
	}
}

func TestValidateConfigRejectsDuplicateIdentityIgnoringCaseAndSpace(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{
		{ID: "one", Name: "One", Type: model.ApplicationTypeCLI, InstallPath: "one", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), Identity: "cli:tool", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
		{ID: "two", Name: "Two", Type: model.ApplicationTypeCLI, InstallPath: "two", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), Identity: " CLI:TOOL ", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}},
	}
	if err := validateConfig(catalog); err == nil || !strings.Contains(strings.ToLower(err.Error()), "cli:tool") {
		t.Fatalf("expected duplicate identity validation, got %v", err)
	}
}

func TestValidateConfigRejectsMissingLanguage(t *testing.T) {
	catalog := defaultConfig()
	catalog.Settings.Language = ""
	if err := validateConfig(catalog); err == nil {
		t.Fatal("config without language was accepted")
	}
}

func TestValidateConfigAcceptsDownloaderCLIPaths(t *testing.T) {
	for _, test := range []struct {
		cli       string
		extraArgs []string
	}{
		{cli: "/opt/homebrew/bin/aria2c"},
		{cli: "/usr/bin/curl", extraArgs: []string{"--retry=3", "--connect-timeout=10"}},
	} {
		catalog := defaultConfig()
		catalog.Settings.Downloader.CLI = test.cli
		catalog.Settings.Downloader.ExtraArgs = test.extraArgs
		if err := validateConfig(catalog); err != nil {
			t.Fatalf("full downloader path %q was rejected: %v", test.cli, err)
		}
	}
}

func TestValidateConfigRejectsAria2ArgumentsForCurl(t *testing.T) {
	catalog := defaultConfig()
	catalog.Settings.Downloader.CLI = "curl"
	catalog.Settings.Downloader.ExtraArgs = []string{"--split=64"}
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "settings.downloader.extra_args") {
		t.Fatalf("expected curl extra_args validation error, got %v", err)
	}
}

func TestValidateConfigRejectsAria2ApplicationArgumentsForCurl(t *testing.T) {
	catalog := defaultConfig()
	catalog.Settings.Downloader.CLI = "curl"
	catalog.Settings.Downloader.ExtraArgs = []string{"--retry=3"}
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderDefault, "", "printf latest", "", &model.Download{URL: "https://example.invalid/file", ExtraArgs: []string{"--split=64"}}, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "apps[0].provider.actions.download.extra_args") {
		t.Fatalf("expected curl application extra_args validation error, got %v", err)
	}
}

func TestValidateConfigRejectsUnsafeGlobalDownloaderArguments(t *testing.T) {
	catalog := defaultConfig()
	catalog.Settings.Downloader.ExtraArgs = []string{"--enable-rpc=true"}
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "settings.downloader.extra_args") {
		t.Fatalf("expected unsafe global downloader argument error, got %v", err)
	}
}

func TestValidateConfigRequiresModeConfiguration(t *testing.T) {
	for _, test := range []struct {
		mode  model.UpdateMode
		field string
		check string
	}{
		{mode: model.ModeAuto, field: "update", check: "check"},
		{mode: model.ModeDownload, field: "download.url", check: "check"},
	} {
		catalog := defaultConfig()
		catalog.Apps = []model.Application{{
			ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: test.mode,
			Provider: providerConfig(model.ProviderDefault, "", test.check, "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
		}}
		if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), test.field) {
			t.Fatalf("expected %s validation, got %v", test.field, err)
		}
	}
}

func TestValidateConfigRejectsInvalidApplicationTypeAndInstallPath(t *testing.T) {
	for _, test := range []struct {
		name  string
		app   model.Application
		field string
	}{
		{name: "unknown type", app: model.Application{ID: "sample", Name: "Sample", Type: "desktop", InstallPath: "sample", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}, field: "type"},
		{name: "empty type", app: model.Application{ID: "sample", Name: "Sample", InstallPath: "sample", UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}, field: "type"},
		{name: "empty install path", app: model.Application{ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, UpdateMode: model.ModeCheck, Provider: providerConfig(model.ProviderDefault, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}, field: "install_path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := defaultConfig()
			catalog.Apps = []model.Application{test.app}
			if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %s validation error, got %v", test.field, err)
			}
		})
	}
}

func TestValidateConfigRejectsNonHTTPDownloadURL(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: "sample", UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderDefault, "", "", "", &model.Download{URL: "file:///tmp/application.zip"}, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "download.url") {
		t.Fatalf("expected main download URL validation error, got %v", err)
	}
}

func TestValidateConfigRejectsInvalidDownloadChecksum(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderDefault, "", "", "", &model.Download{URL: "https://example.invalid/file", ChecksumValue: "not-a-digest"}, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "download.checksum_value") {
		t.Fatalf("expected SHA-256 validation error, got %v", err)
	}
}

func TestValidateConfigRequiresChecksumSourceOutsideGitHubRelease(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderDefault, "", "", "", &model.Download{URL: "https://example.invalid/file", ChecksumEnabled: true}, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "checksum_url") {
		t.Fatalf("expected checksum source validation error, got %v", err)
	}
}

func TestValidateConfigRejectsNonHTTPChecksumURL(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderDefault, "", "", "", &model.Download{URL: "https://example.invalid/file", ChecksumEnabled: true, ChecksumURL: "file:///tmp/checksum"}, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "checksum_url") {
		t.Fatalf("expected checksum URL validation error, got %v", err)
	}
}

func TestValidateConfigRejectsIncompleteGitHubReleaseDownloadOverride(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderGitHubRelease, "", "", "", &model.Download{Filename: "sample-{last_version}.zip", ChecksumEnabled: true}, ""), Package: "owner/repo",
		StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil {
		t.Fatal("incomplete GitHub Release download override was accepted")
	}
}

func TestValidateConfigRejectsEmptySparkleDownloadOverride(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sparkle-app", Name: "Sparkle App", Type: model.ApplicationTypeBundle, Enabled: true, InstallPath: "/Applications/App.app",
		UpdateMode: model.ModeDownload, Provider: providerConfig(model.ProviderSparkle, "", "", "", &model.Download{}, ""), Package: "https://example.invalid/appcast.xml", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil {
		t.Fatal("empty Sparkle download override was accepted")
	}
}

func TestValidateConfigAllowsBuiltinDownloadsWithoutAction(t *testing.T) {
	for _, providerType := range []model.ProviderType{model.ProviderGitHubRelease, model.ProviderSparkle} {
		t.Run(string(providerType), func(t *testing.T) {
			catalog := defaultConfig()
			app := model.Application{ID: string(providerType), Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload, Provider: providerConfig(providerType, "", "", "", nil, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked}}
			if providerType == model.ProviderGitHubRelease {
				app.Package = "owner/repo"
			} else {
				app.Package = "https://example.invalid/appcast.xml"
			}
			catalog.Apps = []model.Application{app}
			if err := validateConfig(catalog); err != nil {
				t.Fatalf("builtin %s download was rejected: %v", providerType, err)
			}
		})
	}
}

func TestValidateConfigAllowsSparkleBuiltinDownloadWithoutAction(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sparkle-app", Name: "Sparkle App", Type: model.ApplicationTypeBundle, Enabled: true, InstallPath: "/Applications/App.app",
		UpdateMode: model.ModeDownload, Provider: providerConfig(model.ProviderSparkle, "", "", "", nil, ""), Package: "https://example.invalid/appcast.xml", StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err != nil {
		t.Fatalf("builtin Sparkle download was rejected: %v", err)
	}
}

func TestValidateConfigRejectsUnsafeDownloaderArguments(t *testing.T) {
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, Enabled: true, InstallPath: "sample", UpdateMode: model.ModeDownload,
		Provider: providerConfig(model.ProviderDefault, "", "", "", &model.Download{URL: "https://example.invalid/file", ExtraArgs: []string{"--out=elsewhere"}}, ""), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "extra_args") {
		t.Fatalf("expected unsafe extra_args validation error, got %v", err)
	}
}

func TestValidateConfigAcceptsExactlyTargetProviders(t *testing.T) {
	for _, providerType := range []model.ProviderType{
		model.ProviderDefault, model.ProviderGitHubRelease, model.ProviderGitHubTag, model.ProviderNPM, model.ProviderPyPI,
		model.ProviderJetBrains, model.ProviderGo, model.ProviderNodeLTS, model.ProviderSparkle,
	} {
		t.Run(string(providerType), func(t *testing.T) {
			catalog := defaultConfig()
			application := model.Application{
				ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: "sample", Enabled: true,
				UpdateMode: model.ModeCheck, Provider: providerConfig(providerType, "", "", "", nil, ""),
				StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
			}
			if testProviderUsesPackage(providerType) {
				application.Package = "owner/package"
			}
			if providerType == model.ProviderDefault {
				application.Provider = providerConfig(providerType, "", "check", "", nil, "")
			}
			catalog.Apps = []model.Application{application}
			if err := validateConfig(catalog); err != nil {
				t.Fatalf("target provider %q was rejected: %v", providerType, err)
			}
		})
	}
	catalog := defaultConfig()
	catalog.Apps = []model.Application{{
		ID: "custom-application", Name: "Custom Application", Type: model.ApplicationTypeCLI, InstallPath: "custom-application",
		UpdateMode: model.ModeCheck, Provider: providerConfig("company_release_feed", "", "", "", nil, ""),
		StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(catalog); err == nil {
		t.Fatal("unsupported provider was accepted")
	}
}

func TestValidateConfigRestrictsInstallToDefaultAction(t *testing.T) {
	valid := defaultConfig()
	valid.Apps = []model.Application{{
		ID: "sample", Name: "Sample", Type: model.ApplicationTypeCLI, InstallPath: "sample", Enabled: true, UpdateMode: model.ModeInstall,
		Provider: providerConfig(model.ProviderDefault, "version", "check", "updater", nil, "installer"), StatusManaged: model.ManagedStatus{UpdateStatus: model.StatusUnchecked},
	}}
	if err := validateConfig(valid); err != nil {
		t.Fatalf("default install action was rejected: %v", err)
	}
	for _, provider := range []model.ProviderConfig{
		providerConfig(model.ProviderDefault, "", "", "", nil, "installer"),
		providerConfig(model.ProviderGitHubRelease, "", "", "", nil, "installer"),
	} {
		catalog := valid
		catalog.Apps = append([]model.Application(nil), valid.Apps...)
		catalog.Apps[0].Provider = provider
		if err := validateConfig(catalog); err == nil {
			t.Fatalf("invalid install provider %#v was accepted", provider)
		}
	}
}

func TestProviderActionsOmitWhenAbsent(t *testing.T) {
	encoded, err := json.Marshal(providerConfig(model.ProviderDefault, "", "", "", nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "actions") {
		t.Fatalf("empty provider actions were encoded: %s", encoded)
	}
}

func TestEmbeddedDefaultsAreValidAndIndependent(t *testing.T) {
	first := defaultConfig()
	if err := validateConfig(first); err != nil {
		t.Fatal(err)
	}
	if len(first.Settings.ProviderURLs) == 0 {
		t.Fatal("embedded defaults did not load configurable URLs")
	}
	if first.Settings.Downloader.CLI != "aria2c" || len(first.Settings.ProviderURLs) != 7 {
		t.Fatalf("embedded defaults do not match target downloader/provider schema: %#v %#v", first.Settings.Downloader, first.Settings.ProviderURLs)
	}
	for _, argument := range []string{"--continue=true", "--split=64", "--max-connection-per-server=10"} {
		if !slices.Contains(first.Settings.Downloader.ExtraArgs, argument) {
			t.Fatalf("embedded defaults missing downloader argument %q: %#v", argument, first.Settings.Downloader.ExtraArgs)
		}
	}
	first.Settings.ProviderURLs["go"] = "changed"
	first.Settings.HTTP.TimeoutSeconds = 1
	second := defaultConfig()
	if second.Settings.ProviderURLs["go"] == "changed" {
		t.Fatal("default catalog instances share mutable configuration")
	}
	if second.Settings.HTTP.TimeoutSeconds == 1 {
		t.Fatal("default catalog instances share HTTP configuration")
	}
	bootstrap := DefaultBootstrap()
	if bootstrap.ConfigPath == "" || bootstrap.LockPath == "" || bootstrap.EnvFile == "" {
		t.Fatalf("invalid bootstrap defaults: %+v", bootstrap)
	}
	if bootstrap.ConfigPath != "conf/config.json" || bootstrap.LockPath != "conf/config.json.lock" {
		t.Fatalf("default bootstrap paths are not under conf: %+v", bootstrap)
	}
}

func TestValidateConfigRejectsMissingProviderURL(t *testing.T) {
	catalog := defaultConfig()
	delete(catalog.Settings.ProviderURLs, "github_release")
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "provider_urls.github_release") {
		t.Fatalf("expected provider URL validation, got %v", err)
	}
}

func TestValidateConfigRejectsNonTargetProviderURL(t *testing.T) {
	catalog := defaultConfig()
	catalog.Settings.ProviderURLs["vscode"] = "https://example.invalid/vscode"
	if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), "provider_urls.vscode") {
		t.Fatalf("non-target provider URL was accepted: %v", err)
	}
}

func TestLoadConfigRejectsUnsupportedSchema(t *testing.T) {
	for _, version := range []int{2, 3} {
		directory := t.TempDir()
		path := filepath.Join(directory, "config.json")
		content := fmt.Sprintf(`{"schema_version":%d,"settings":{},"apps":[],"scan_version_control":{}}`, version)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		store := newStore(path, path+".lock")
		_, err := store.load()
		if err == nil || !strings.Contains(err.Error(), "schema_version") {
			t.Fatalf("expected schema validation error for %s, got %v", content, err)
		}
	}
}

func TestLoadConfigRequiresUnifiedStructureFields(t *testing.T) {
	valid, err := json.Marshal(testUnifiedConfig())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"scan version control missing": strings.Replace(string(valid), `,"scan_version_control":{}`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			store := newStore(path, path+".lock")
			if _, err := store.load(); err == nil {
				t.Fatal("missing required unified field was accepted")
			}
		})
	}
}

func TestLoadConfigAllowsEmptyStatusManaged(t *testing.T) {
	data, err := json.Marshal(testUnifiedConfig())
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"status_managed":{"has_update":false,"update_status":"unchecked"}`, `"status_managed":{}`, 1))
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newStore(path, path+".lock")
	loaded, err := store.load()
	if err != nil || loaded.Apps[0].StatusManaged.UpdateStatus != model.StatusUnchecked {
		t.Fatalf("empty status_managed was not normalized: %#v %v", loaded.Apps[0].StatusManaged, err)
	}
}

func TestLoadConfigAllowsMissingStatusManaged(t *testing.T) {
	data, err := json.Marshal(testUnifiedConfig())
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `,"status_managed":{"has_update":false,"update_status":"unchecked"}`, "", 1))
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newStore(path, path+".lock")
	loaded, err := store.load()
	if err != nil || loaded.Apps[0].StatusManaged.UpdateStatus != model.StatusUnchecked {
		t.Fatalf("missing status_managed was not normalized: %#v %v", loaded.Apps[0].StatusManaged, err)
	}
}

func TestLoadConfigRejectsUnknownManagedStatus(t *testing.T) {
	value := testUnifiedConfig()
	value.Apps[0].StatusManaged.UpdateStatus = "future_status"
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newStore(path, path+".lock")
	if _, err := store.load(); err == nil || !strings.Contains(err.Error(), "future_status") {
		t.Fatalf("unknown update_status was accepted: %v", err)
	}
}

func TestValidateConfigRejectsInvalidHTTPSettings(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*model.HTTPSettings)
		field  string
	}{
		{name: "timeout", change: func(settings *model.HTTPSettings) { settings.TimeoutSeconds = 0 }, field: "http.timeout_seconds"},
		{name: "host concurrency", change: func(settings *model.HTTPSettings) { settings.MaxConcurrencyPerHost = 0 }, field: "max_concurrency_per_host"},
		{name: "retries", change: func(settings *model.HTTPSettings) { settings.Retries = maxHTTPRetries + 1 }, field: "http.retries"},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := defaultConfig()
			test.change(catalog.Settings.HTTP)
			if err := validateConfig(catalog); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %s validation, got %v", test.field, err)
			}
		})
	}
}
