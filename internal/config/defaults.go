package config

import (
	_ "embed"
	"encoding/json"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

const (
	minWorkers                = model.MinWorkers
	maxWorkers                = model.MaxWorkers
	maxTimeoutSeconds         = model.MaxTimeoutSeconds
	maxHTTPTimeoutSeconds     = model.MaxHTTPTimeoutSeconds
	maxHTTPConcurrencyPerHost = model.MaxHTTPConcurrencyPerHost
	maxHTTPRetries            = model.MaxHTTPRetries
)

var (
	//go:embed template/default_config.json
	defaultConfigJSON []byte
	//go:embed template/bootstrap.json
	bootstrapJSON []byte
)

// DefaultConfig returns a fresh copy of the unified config embedded in the binary.
func defaultConfig() model.Config {
	var catalog model.Config
	if err := json.Unmarshal(defaultConfigJSON, &catalog); err != nil {
		panic(i18n.T("config.default_config_parse", err))
	}
	return catalog
}

func defaultBootstrap() Bootstrap {
	var bootstrap Bootstrap
	if err := json.Unmarshal(bootstrapJSON, &bootstrap); err != nil {
		panic(i18n.T("config.bootstrap_parse", err))
	}
	return bootstrap
}
