package provider

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/eoctet/tendkit/pkg/version"
)

func verifyInstallOwnership(path, root string) error {
	path, root = strings.TrimSpace(path), strings.TrimSpace(root)
	if path == "" || root == "" {
		return errors.New("installation path or root missing")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(realRoot, realPath)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return errors.New("installation path is outside manager root")
	}
	return nil
}

func trustedDownloadURL(endpoint, candidate string) (string, error) {
	source, err := url.Parse(endpoint)
	if err != nil || source.Hostname() == "" {
		return "", NewError("provider.download_url_untrusted")
	}
	download, err := url.Parse(strings.TrimSpace(candidate))
	if err != nil || download.Scheme != "https" || download.Hostname() == "" {
		return "", NewError("provider.download_url_untrusted")
	}
	sourceHost := strings.ToLower(source.Hostname())
	downloadHost := strings.ToLower(download.Hostname())
	if sourceHost == downloadHost || trustedOfficialDownloadHost(sourceHost, downloadHost) {
		return download.String(), nil
	}
	return "", NewError("provider.download_url_untrusted")
}

func trustedOfficialDownloadHost(source, download string) bool {
	switch source {
	case "api.github.com":
		return download == "github.com" || download == "api.github.com"
	case "pypi.org":
		return download == "files.pythonhosted.org"
	case "data.services.jetbrains.com":
		return download == "download.jetbrains.com"
	default:
		return false
	}
}

func downloadFilename(downloadURL, fallback string) string {
	parsed, err := url.Parse(downloadURL)
	if err == nil {
		name := path.Base(parsed.Path)
		if safeFilename(name) {
			return name
		}
	}
	if safeFilename(fallback) {
		return fallback
	}
	return ""
}

func safeFilename(name string) bool {
	return name != "" && name != "." && name != "/" && !strings.ContainsAny(name, `/\\`)
}

func expandEndpoint(endpoint, packageValue string) string {
	return strings.ReplaceAll(endpoint, "{package}", packageValue)
}

func normalizeAny(value any) (string, error) {
	if value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return "", NewError("provider.empty_value")
	}
	return version.Normalize(fmt.Sprint(value)), nil
}
