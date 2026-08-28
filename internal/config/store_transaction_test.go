package config

import (
	"errors"

	"os"
	"path/filepath"

	"testing"
)

func TestConfigStoreTransaction(t *testing.T) {
	t.Run("sync-directory-after-atomic-write", func(t *testing.T) {
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
	})
	t.Run("create-json-if-missing-does-not-replace-existing-file", func(t *testing.T) {
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
	})
	t.Run("save-rename-failure-preserves-original-config-and-cache", func(t *testing.T) {
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
	})
	t.Run("save-rejects-stale-external-config", func(t *testing.T) {
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
	})
	t.Run("save-updates-cached-config", func(t *testing.T) {
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
	})
	t.Run("load-keeps-memory-snapshot-until-explicit-reload", func(t *testing.T) {
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
		replacement, err := store.load()
		if err != nil {
			t.Fatal(err)
		}
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
	})
	t.Run("cached-load-does-not-stat-or-hash-file", func(t *testing.T) {
		store := testUnifiedStore(t)
		if err := store.createIfMissing(testUnifiedConfig()); err != nil {
			t.Fatal(err)
		}
		store.stat = func(string) (os.FileInfo, error) { t.Fatal("cached Load called stat"); return nil, nil }
		store.hash = func(string) (string, error) { t.Fatal("cached Load called hash"); return "", nil }
		if _, err := store.load(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("save-snapshot-rejects-stale-operation-revision", func(t *testing.T) {
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
	})
	t.Run("reload-failure-preserves-memory-snapshot", func(t *testing.T) {
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
	})
	t.Run("process-lock-rejects-second-store", func(t *testing.T) {
		store := testUnifiedStore(t)
		other := newStore(store.ConfigPath, store.LockPath)
		if err := store.acquireProcessLock(); err != nil {
			t.Fatal(err)
		}
		defer store.releaseProcessLock()
		if err := other.acquireProcessLock(); err == nil {
			t.Fatal("second lock accepted")
		}
	})
}
