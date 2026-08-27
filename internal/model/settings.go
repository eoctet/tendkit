package model

import downloadutil "github.com/eoctet/tendkit/pkg/downloader"

// SchemaVersion is the only catalog and state schema accepted by this binary.
const SchemaVersion = 1

// Configuration limits are shared by validation and interactive editors.
const (
	MinWorkers                       = 1
	MaxWorkers                       = 64
	MaxTimeoutSeconds                = 24 * 60 * 60
	DefaultHTTPTimeoutSeconds        = 60
	MaxHTTPTimeoutSeconds            = 10 * 60
	DefaultHTTPMaxConcurrencyPerHost = 1
	MaxHTTPConcurrencyPerHost        = 16
	DefaultHTTPRetries               = 2
	MaxHTTPRetries                   = 5
)

// Config is the complete trusted application and updater configuration. Runtime
// status is deliberately embedded in each application so one JSON document is
// the only persistent source of truth.
type Config struct {
	SchemaVersion      int                                      `json:"schema_version"`
	Settings           Settings                                 `json:"settings"`
	Apps               []Application                            `json:"apps"`
	ScanVersionControl map[string]map[string]ScanKeepResolution `json:"scan_version_control"`
}

// Settings contains process-wide scanner, updater, and network limits.
type Settings struct {
	Language       string             `json:"language,omitempty"`
	TimeoutSeconds int                `json:"timeout_seconds"`
	Workers        int                `json:"workers"`
	HTTP           *HTTPSettings      `json:"http,omitempty"`
	Downloader     DownloaderSettings `json:"downloader"`
	LogDir         string             `json:"log_dir"`
	LogLevel       string             `json:"log_level"`
	ProviderURLs   map[string]string  `json:"provider_urls"`
	Scan           ScanSettings       `json:"scan"`
}

// DownloaderSettings selects the aria2c or curl adapter for every download.
type DownloaderSettings = downloadutil.Settings

// HTTPSettings bounds provider requests and per-host concurrency.
type HTTPSettings struct {
	TimeoutSeconds        int `json:"timeout_seconds"`
	MaxConcurrencyPerHost int `json:"max_concurrency_per_host"`
	Retries               int `json:"retries"`
}

// ScanSettings controls discovery sources and exclusion patterns.
type ScanSettings struct {
	Path        bool                `json:"path"`
	Application bool                `json:"application"`
	Packages    PackageScanSettings `json:"packages"`
	BundleID    []string            `json:"bundle_id"`
	Exclude     []string            `json:"exclude"`
}

// PackageScanSettings enables individual package ecosystems.
type PackageScanSettings struct {
	Python          bool `json:"python"`
	Node            bool `json:"node"`
	Go              bool `json:"go"`
	UV              bool `json:"uv"`
	Ruby            bool `json:"ruby"`
	HomebrewFormula bool `json:"homebrew-formula"`
	HomebrewCask    bool `json:"homebrew-cask"`
	Cargo           bool `json:"cargo"`
}
