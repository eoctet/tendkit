package config

import (
	"strings"

	"encoding/json"
	"errors"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"

	"os"
	"path/filepath"

	"testing"

	"github.com/eoctet/tendkit/internal/model"
	"reflect"
)

func TestConfigPlatformMigration(t *testing.T) {
	t.Run("non-macos-load-backs-up-and-cleans-application-configuration", func(t *testing.T) {
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
	})
	t.Run("non-macos-create-if-missing-applies-application-invariant-without-migration-artifacts", func(t *testing.T) {
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
	})
	t.Run("non-macos-save-applies-application-invariant-without-migration-artifacts", func(t *testing.T) {
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
	})
	t.Run("non-macos-reload-cleans-external-application-configuration", func(t *testing.T) {
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
	})
	t.Run("macos-load-leaves-application-configuration-untouched", func(t *testing.T) {
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
	})
	t.Run("non-macos-backup-failure-leaves-config-and-cache-unchanged", func(t *testing.T) {
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
	})
	t.Run("non-macos-logger-initialization-failure-does-not-interrupt-migration", func(t *testing.T) {
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
	})
	t.Run("non-macos-migration-save-failure-preserves-backup-and-cache", func(t *testing.T) {
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
	})
}
