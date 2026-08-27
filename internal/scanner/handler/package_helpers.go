package handler

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/version"
)

func packageKey(value string) string {
	return model.NormalizeIdentityName(value)
}

func packageCandidate(app model.Application, current string, aliases ...string) Candidate {
	return Candidate{
		Application:    app,
		CurrentVersion: version.Normalize(current),
		Aliases:        aliases,
	}
}

func packageProvider(provider model.ProviderType, version, update string) model.ProviderConfig {
	actions := &model.ProviderActions{Version: version, Update: update}
	if actions.Version == "" && actions.Update == "" {
		actions = nil
	}
	return model.ProviderConfig{Type: provider, Actions: actions}
}

func reportPackageProgress(request Request, stage, subject string) {
	if request.Report != nil {
		request.Report(Progress{Stage: stage, Subject: subject})
	}
}

func githubProjectURL(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".git")
	value = strings.TrimPrefix(value, "git+")
	value = strings.TrimPrefix(value, "git://")
	if strings.HasPrefix(value, "git@github.com:") {
		value = "https://github.com/" + strings.TrimPrefix(value, "git@github.com:")
	}
	if strings.HasPrefix(value, "github.com/") {
		value = "https://" + value
	}
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "https://github.com/") && !strings.HasPrefix(lower, "http://github.com/") {
		return ""
	}
	value = strings.TrimPrefix(strings.TrimPrefix(value, "https://github.com/"), "http://github.com/")
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return "https://github.com/" + parts[0] + "/" + parts[1]
}

var packageSlugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func packageSlug(name string) string {
	cleaned := strings.Trim(packageSlugCleaner.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if cleaned != "" {
		return cleaned
	}
	hash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%x", hash[:5])
}

func expandConfiguredPath(value string, homeDir func() (string, error)) string {
	value = strings.TrimSpace(os.ExpandEnv(value))
	if strings.HasPrefix(value, "~/") {
		if home, err := homeDir(); err == nil {
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

func managerPath(binary string, configured []model.Application, lookPath func(string) (string, error), stat func(string) (os.FileInfo, error), homeDir func() (string, error)) string {
	if path, err := lookPath(binary); err == nil {
		return path
	}
	for _, app := range configured {
		if !strings.EqualFold(app.ID, binary) && !strings.EqualFold(app.Name, binary) {
			continue
		}
		path := expandConfiguredPath(app.InstallPath, homeDir)
		if info, err := stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func inventoryError(ecosystem string, err error, exit int) error {
	if err != nil {
		return err
	}
	return &PackageInventoryIncompleteError{Ecosystem: ecosystem, Message: "package inventory command failed"}
}
