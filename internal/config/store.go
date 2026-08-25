package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
	logutil "github.com/eoctet/tendkit/pkg/logger"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// Store owns the unified document. cache is a pointer so copies share the
// process-local lock, cache, and baseline signature.
type store struct {
	ConfigPath string
	LockPath   string
	rename     func(string, string) error
	stat       func(string) (os.FileInfo, error)
	hash       func(string) (string, error)
	systemInfo func() runtimeutil.SystemInfo
	backup     func(string, []byte) (string, error)
	cache      *storeCache
}
type storeCache struct {
	mu       sync.RWMutex
	loaded   bool
	catalog  model.Config
	hash     string
	fileInfo os.FileInfo
	revision uint64
	lockFile *os.File
}

// NewStore constructs a Store whose process-local cache is intentionally
// shared by value copies passed to services and the TUI.
func newStore(configPath, lockPath string) store {
	return store{ConfigPath: configPath, LockPath: lockPath, systemInfo: runtimeutil.HostPlatform, cache: &storeCache{}}
}

func (s *store) runtime() *storeCache {
	if s.cache == nil {
		s.cache = &storeCache{}
	}
	return s.cache
}

func (s *store) load() (model.Config, error) {
	snapshot, err := s.loadSnapshot()
	return snapshot.Config, err
}

// LoadSnapshot loads and validates the file once, then serves deep-copied memory
// snapshots without touching the file
func (s *store) loadSnapshot() (Snapshot, error) {
	c := s.runtime()
	c.mu.RLock()
	if c.loaded {
		value := Snapshot{Config: cloneConfig(c.catalog), Revision: c.revision}
		c.mu.RUnlock()
		return value, nil
	}
	c.mu.RUnlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return Snapshot{Config: cloneConfig(c.catalog), Revision: c.revision}, nil
	}
	value, hash, info, raw, err := s.readDiskConfig()
	if err != nil {
		return Snapshot{}, err
	}
	if value, hash, info, err = s.migrateNonMacOSLocked(value, hash, info, raw); err != nil {
		return Snapshot{}, err
	}
	c.catalog, c.hash, c.fileInfo, c.loaded = cloneConfig(value), hash, info, true
	c.revision++
	return Snapshot{Config: cloneConfig(value), Revision: c.revision}, nil
}

// Reload explicitly replaces the memory snapshot with the current disk file.
// Parse or validation failures leave the previous snapshot untouched.
func (s *store) reload() (Snapshot, error) {
	c := s.runtime()
	c.mu.Lock()
	defer c.mu.Unlock()
	value, hash, info, raw, err := s.readDiskConfig()
	if err != nil {
		return Snapshot{}, err
	}
	if value, hash, info, err = s.migrateNonMacOSLocked(value, hash, info, raw); err != nil {
		return Snapshot{}, err
	}
	c.catalog, c.hash, c.fileInfo, c.loaded = cloneConfig(value), hash, info, true
	c.revision++
	return Snapshot{Config: cloneConfig(value), Revision: c.revision}, nil
}

func (s *store) createIfMissing(value model.Config) error {
	s.enforceNonMacOSConfig(&value)
	normalizeConfig(&value)
	if err := validateConfig(value); err != nil {
		return err
	}
	if err := createJSONIfMissing(s.ConfigPath, value); err != nil {
		return err
	}
	return s.setCache(value)
}

// SaveSnapshot commits work only when its process-local operation baseline is current.
func (s *store) saveSnapshot(expectedRevision uint64, value model.Config) error {
	return s.withLock(func() error { return s.saveLocked(expectedRevision, value) })
}
func (s *store) saveLocked(expectedRevision uint64, value model.Config) error {
	s.enforceNonMacOSConfig(&value)
	normalizeConfig(&value)
	if err := validateConfig(value); err != nil {
		return err
	}
	c := s.runtime()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || expectedRevision != c.revision {
		return ErrStaleOperation
	}
	info, err := s.statFile(s.ConfigPath)
	if err != nil {
		return err
	}
	if !sameFileMetadataInfo(info, c.fileInfo) {
		h, err := s.hashFile(s.ConfigPath)
		if err != nil {
			return err
		}
		if h != c.hash {
			return ErrExternalConfigChange
		}
	}
	if err := s.atomicWriteJSON(s.ConfigPath, value); err != nil {
		return err
	}
	h, err := s.hashFile(s.ConfigPath)
	if err != nil {
		return err
	}
	info, err = s.statFile(s.ConfigPath)
	if err != nil {
		return err
	}
	c.catalog, c.hash, c.fileInfo, c.loaded = cloneConfig(value), h, info, true
	c.revision++
	return nil
}
func (s *store) setCache(value model.Config) error {
	h, err := s.hashFile(s.ConfigPath)
	if err != nil {
		return err
	}
	c := s.runtime()
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := s.statFile(s.ConfigPath)
	if err != nil {
		return err
	}
	c.catalog, c.hash, c.fileInfo, c.loaded = cloneConfig(value), h, info, true
	c.revision++
	return nil
}

func (s *store) statFile(path string) (os.FileInfo, error) {
	if s.stat != nil {
		return s.stat(path)
	}
	return os.Stat(path)
}

func (s *store) hashFile(path string) (string, error) {
	if s.hash != nil {
		return s.hash(path)
	}
	return fileSHA256(path)
}

func (s *store) readDiskConfig() (model.Config, string, os.FileInfo, []byte, error) {
	var value model.Config
	raw, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		return value, "", nil, nil, fmt.Errorf("%s: %w", i18n.T("config.read_config"), err)
	}
	if err := parseConfigJSON(raw, &value, s.ConfigPath); err != nil {
		return value, "", nil, nil, fmt.Errorf("%s: %w", i18n.T("config.read_config"), err)
	}
	normalizeConfig(&value)
	if err := validateConfig(value); err != nil {
		return value, "", nil, nil, err
	}
	hash, err := s.hashFile(s.ConfigPath)
	if err != nil {
		return value, "", nil, nil, err
	}
	info, err := s.statFile(s.ConfigPath)
	return value, hash, info, raw, err
}

func (s *store) supportsApplicationBundles() bool {
	if s.systemInfo != nil {
		return s.systemInfo().SupportsApplicationBundles()
	}
	return runtimeutil.HostPlatform().SupportsApplicationBundles()
}

func (s *store) enforceNonMacOSConfig(value *model.Config) {
	if value == nil || s.supportsApplicationBundles() {
		return
	}
	value.Settings.Scan.Application = false
	removed := make(map[string]struct{})
	for _, app := range value.Apps {
		if app.Type == model.ApplicationTypeBundle {
			removed[app.ID] = struct{}{}
		}
	}
	value.Apps = filterBundleApplications(value.Apps)
	for id := range removed {
		delete(value.ScanVersionControl, id)
	}
}

func filterBundleApplications(apps []model.Application) []model.Application {
	result := make([]model.Application, 0, len(apps))
	for _, app := range apps {
		if app.Type != model.ApplicationTypeBundle {
			result = append(result, app)
		}
	}
	return result
}

func (s *store) migrateNonMacOSLocked(value model.Config, hash string, info os.FileInfo, raw []byte) (model.Config, string, os.FileInfo, error) {
	if s.supportsApplicationBundles() {
		return value, hash, info, nil
	}
	needsMigration := value.Settings.Scan.Application
	removed := 0
	for _, app := range value.Apps {
		if app.Type == model.ApplicationTypeBundle {
			removed++
		}
	}
	if !needsMigration && removed == 0 {
		return value, hash, info, nil
	}
	log, _ := logutil.NewLogger(runtimeutil.ExpandPath(value.Settings.LogDir), value.Settings.LogLevel)
	_, err := s.backupConfig(raw)
	if err != nil {
		return value, hash, info, err
	}
	s.enforceNonMacOSConfig(&value)
	if err := s.atomicWriteJSON(s.ConfigPath, value); err != nil {
		return value, hash, info, err
	}
	hash, err = s.hashFile(s.ConfigPath)
	if err != nil {
		return value, "", nil, err
	}
	info, err = s.statFile(s.ConfigPath)
	if err != nil {
		return value, "", nil, err
	}
	if log != nil {
		_ = log.Warn(logutil.LogEntry{Event: "config_application_migrated", Operation: "config", Message: "application configuration migrated", ResultCount: removed})
	}
	return value, hash, info, nil
}

func (s *store) backupConfig(raw []byte) (string, error) {
	if s.backup != nil {
		return s.backup(s.ConfigPath, raw)
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	for suffix := 0; ; suffix++ {
		path := fmt.Sprintf("%s.backup-%s", s.ConfigPath, stamp)
		if suffix > 0 {
			path = fmt.Sprintf("%s-%d", path, suffix)
		}
		// #nosec G304 -- The path is derived from the configured catalog path, and O_EXCL plus mode 0600 prevents replacement or disclosure.
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := file.Write(raw); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
		return path, nil
	}
}

// AcquireProcessLock holds a non-blocking exclusive lock for the TUI lifetime.
func (s *store) acquireProcessLock() error {
	c := s.runtime()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lockFile != nil {
		return nil
	}
	if strings.TrimSpace(s.LockPath) == "" {
		return errors.New(i18n.T("config.lock_missing"))
	}
	if err := os.MkdirAll(filepath.Dir(s.LockPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: %w", i18n.T("config.already_running"), err)
	}
	c.lockFile = f
	return nil
}
func (s *store) releaseProcessLock() error {
	c := s.runtime()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lockFile == nil {
		return nil
	}
	err := errors.Join(syscall.Flock(int(c.lockFile.Fd()), syscall.LOCK_UN), c.lockFile.Close())
	c.lockFile = nil
	return err
}
func (s *store) withLock(fn func() error) error {
	c := s.runtime()
	c.mu.RLock()
	held := c.lockFile != nil
	c.mu.RUnlock()
	if held {
		return fn()
	}
	if err := s.acquireProcessLock(); err != nil {
		return err
	}
	defer func() { _ = s.releaseProcessLock() }()
	return fn()
}

func normalizeConfig(value *model.Config) {
	if value == nil {
		return
	}
	if value.ScanVersionControl == nil {
		value.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{}
	}
	if value.Settings.Scan.BundleID == nil {
		value.Settings.Scan.BundleID = []string{}
	}
	if strings.TrimSpace(value.Settings.LogLevel) == "" {
		value.Settings.LogLevel = "DEBUG"
	} else if level, err := logutil.NormalizeLevel(value.Settings.LogLevel); err == nil {
		value.Settings.LogLevel = level
	}
	for index := range value.Apps {
		if strings.TrimSpace(value.Apps[index].StatusManaged.UpdateStatus) == "" {
			value.Apps[index].StatusManaged.UpdateStatus = model.StatusUnchecked
		}
	}
}
func validateScanVersionControl(values map[string]map[string]model.ScanKeepResolution) error {
	for id, fields := range values {
		if strings.TrimSpace(id) == "" {
			return errors.New(i18n.T("config.scan_keep_app_id_invalid"))
		}
		for field, item := range fields {
			if strings.TrimSpace(field) == "" || len(item.Fingerprint) != sha256.Size*2 {
				return errors.New(i18n.T("config.scan_keep_resolution_invalid"))
			}
			if _, err := hex.DecodeString(item.Fingerprint); err != nil {
				return errors.New(i18n.T("config.scan_keep_fingerprint_invalid"))
			}
			if _, err := time.Parse(time.RFC3339, item.RecordedAt); err != nil {
				return errors.New(i18n.T("config.scan_keep_recorded_at_invalid"))
			}
		}
	}
	return nil
}

func readConfigJSON(path string, target *model.Config) error {
	// #nosec G304 -- The caller supplies the explicitly configured catalog path; strict parsing validates its contents.
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return parseConfigJSON(data, target, path)
}

func parseConfigJSON(data []byte, target *model.Config, path string) error {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("config.invalid_json", path), err)
	}
	var schemaVersion int
	if err := json.Unmarshal(document["schema_version"], &schemaVersion); err != nil || schemaVersion != model.SchemaVersion {
		return fmt.Errorf("schema_version must equal %d", model.SchemaVersion)
	}
	controls, exists := document["scan_version_control"]
	if !exists || string(controls) == "null" || len(controls) == 0 || controls[0] != '{' {
		return errors.New("scan_version_control must be present and must be an object")
	}
	appsRaw, exists := document["apps"]
	if !exists {
		return errors.New("apps must be present")
	}
	var apps []json.RawMessage
	if err := json.Unmarshal(appsRaw, &apps); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("config.invalid_json", path), err)
	}
	if err := requireScanSwitches(document); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("config.invalid_json", path), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New(i18n.T("config.multiple_json", path))
		}
		return fmt.Errorf("%s: %w", i18n.T("config.trailing_json", path), err)
	}
	return nil
}

func requireScanSwitches(document map[string]json.RawMessage) error {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(document["settings"], &settings); err != nil {
		return errors.New("settings must be present and must be an object")
	}
	for _, key := range []string{"language", "http", "downloader", "log_dir", "provider_urls", "scan"} {
		if _, exists := settings[key]; !exists {
			return fmt.Errorf("settings.%s must be present", key)
		}
	}
	var scan map[string]json.RawMessage
	if err := json.Unmarshal(settings["scan"], &scan); err != nil {
		return errors.New("settings.scan must be present and must be an object")
	}
	for _, key := range []string{"path", "application", "packages", "bundle_id", "exclude"} {
		if _, exists := scan[key]; !exists {
			return fmt.Errorf("settings.scan.%s must be present", key)
		}
	}
	if string(scan["bundle_id"]) == "null" {
		return errors.New("settings.scan.bundle_id must be an array")
	}
	var packages map[string]json.RawMessage
	if err := json.Unmarshal(scan["packages"], &packages); err != nil {
		return errors.New("settings.scan.packages must be present and must be an object")
	}
	for _, key := range []string{"python", "node", "go", "uv", "ruby"} {
		if _, exists := packages[key]; !exists {
			return fmt.Errorf("settings.scan.packages.%s must be present", key)
		}
	}
	return nil
}
func (s *store) atomicWriteJSON(path string, value any) error {
	tmp, err := prepareJSONFile(path, value)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	rename := s.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(tmp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
func createJSONIfMissing(path string, value any) error {
	tmp, err := prepareJSONFile(path, value)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Link(tmp, path); err != nil {
		return err
	}
	if err := os.Remove(tmp); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
func prepareJSONFile(path string, value any) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(name)
		}
	}()
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "    ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return "", err
	}
	success = true
	return name, nil
}
func fileSHA256(path string) (string, error) {
	// #nosec G304 -- The path is the already selected catalog or temporary file whose integrity is being compared.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:]), nil
}

func sameFileMetadataInfo(info, baseline os.FileInfo) bool {
	if baseline == nil {
		return false
	}
	return os.SameFile(info, baseline) && info.Size() == baseline.Size() && info.ModTime().Equal(baseline.ModTime())
}
func syncDirectory(path string) error {
	// #nosec G304 -- The path is the parent directory of the configured catalog and is opened only for fsync.
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func cloneConfig(value model.Config) model.Config {
	return model.CloneConfig(value)
}
