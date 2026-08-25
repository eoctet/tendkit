package config

import (
	"errors"
	"os"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

type externalConfigChangeError struct{}

func (externalConfigChangeError) Error() string        { return i18n.T("config.external_change") }
func (externalConfigChangeError) ReloadRequired() bool { return true }

var (
	ErrExternalConfigChange error = externalConfigChangeError{}
	ErrStaleOperation             = errors.New("configuration changed after the operation started")
)

// Snapshot is an immutable configuration copy paired with its process-local revision.
type Snapshot struct {
	Config   model.Config
	Revision uint64
}

// Bootstrap defines startup paths loaded before the catalog is available.
type Bootstrap struct {
	ConfigPath string `json:"config_path"`
	LockPath   string `json:"lock_path"`
	EnvFile    string `json:"env_file"`
}

// EnvLoadResult describes a dotenv load without exposing variable values.
type EnvLoadResult struct {
	Path   string
	Exists bool
	Loaded int
}

// UnsafeConfigError reports why a catalog must not be trusted as executable configuration.
type UnsafeConfigError struct {
	Path   string
	Reason string
}

func (err *UnsafeConfigError) Error() string { return i18n.T("config.unsafe", err.Path, err.Reason) }

// Center is the only public entry point for persistent configuration access.
// Its implementation remains private so callers cannot bypass validation,
// revision checks, atomic writes, or process locking.
type Center struct {
	store *store
}

// New constructs one configuration center for the process lifecycle.
func New(configPath, lockPath string) *Center {
	value := newStore(configPath, lockPath)
	return &Center{store: &value}
}

// Default returns an independent copy of the embedded default configuration.
func Default() model.Config { return defaultConfig() }

// DefaultBootstrap returns the embedded startup path configuration.
func DefaultBootstrap() Bootstrap { return defaultBootstrap() }

// ResolvePath expands a leading ~ and returns an absolute, cleaned path.
func ResolvePath(path, defaultPath string) (string, error) { return resolvePath(path, defaultPath) }

// LoadEnvFile loads an optional dotenv file without overwriting existing variables.
func LoadEnvFile(path string, required bool, defaultPath string) (EnvLoadResult, error) {
	return loadEnvFile(path, required, defaultPath)
}

// Initialize creates the default document when absent and loads a validated snapshot.
func (c *Center) Initialize() error {
	return c.store.withLock(func() error {
		if _, err := os.Stat(c.store.ConfigPath); os.IsNotExist(err) {
			if err := c.store.createIfMissing(defaultConfig()); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
		_, err := c.store.load()
		return err
	})
}

func (c *Center) Load() (model.Config, error) { return c.store.load() }
func (c *Center) Snapshot() (Snapshot, error) { return c.store.loadSnapshot() }
func (c *Center) Reload() (model.Config, error) {
	value, err := c.store.reload()
	return value.Config, err
}
func (c *Center) Save(revision uint64, value model.Config) error {
	return c.store.saveSnapshot(revision, value)
}

func (c *Center) WithLock(fn func() error) error   { return c.store.withLock(fn) }
func (c *Center) AcquireProcessLock() error        { return c.store.acquireProcessLock() }
func (c *Center) ReleaseProcessLock() error        { return c.store.releaseProcessLock() }
func (c *Center) ValidateExecutionSecurity() error { return c.store.validateExecutionSecurity() }
