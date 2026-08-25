package model

import (
	"path"
	"strings"
)

// NormalizeIdentityName produces the stable token used in CLI and package
// identities: lowercase ASCII words joined by one hyphen.
func NormalizeIdentityName(value string) string {
	var output strings.Builder
	separator := false
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			if separator && output.Len() > 0 {
				output.WriteByte('-')
			}
			output.WriteRune(character)
			separator = false
		} else if character == ' ' || character == '/' || character == '-' {
			separator = true
		}
	}
	return strings.Trim(output.String(), "-")
}

// PackageIdentity returns the normalized stable identity for one package.
func PackageIdentity(ecosystem, name string) string {
	if ecosystem == "go" {
		name = path.Base(strings.TrimSuffix(strings.TrimSpace(name), "/"))
	}
	return "package:" + NormalizeIdentityName(ecosystem) + ":" + NormalizeIdentityName(name)
}

// Application type values are persisted in the catalog and must remain stable.
const (
	ApplicationTypeCLI     = "cli"
	ApplicationTypeBundle  = "application"
	ApplicationTypePackage = "package"
	ApplicationTypeSDK     = "sdk"
)

// Scan stage values form the language-independent progress protocol shared by
// scanners and presentation surfaces.
const (
	ScanStagePrepare         = "prepare"
	ScanStagePath            = "path"
	ScanStageMacOS           = "macos"
	ScanStagePackages        = "packages"
	ScanStagePackageManager  = "package_manager"
	ScanStagePackageList     = "package_list"
	ScanStagePackageMetadata = "package_metadata"
	ScanStagePackagePaths    = "package_paths"
	ScanStageApplication     = "application"
	ScanStageFinalize        = "finalize"
)

// Application field paths identify scan differences without coupling the app
// service to a particular presentation implementation.
const (
	ApplicationFieldName           = "name"
	ApplicationFieldType           = "type"
	ApplicationFieldDescription    = "description"
	ApplicationFieldURL            = "url"
	ApplicationFieldInstallPath    = "install_path"
	ApplicationFieldEnabled        = "enabled"
	ApplicationFieldUpdateMode     = "update_mode"
	ApplicationFieldProviderType   = "provider.type"
	ApplicationFieldPackage        = "package"
	ApplicationFieldActionVersion  = "provider.actions.version"
	ApplicationFieldActionCheck    = "provider.actions.check"
	ApplicationFieldActionUpdate   = "provider.actions.update"
	ApplicationFieldActionDownload = "provider.actions.download"
	ApplicationFieldActionInstall  = "provider.actions.install"
	ApplicationFieldIdentity       = "identity"
	ApplicationFieldScanManaged    = "scan_managed"
)

// Operation values identify language-independent execution and operation-log actions.
const (
	OperationVersion  = "version"
	OperationCheck    = "check"
	OperationUpdate   = "update"
	OperationDownload = "download"
	OperationInstall  = "install"
	OperationBatch    = "batch"
)

// Status values are persisted in Config apps[].status_managed and consumed by every presentation
// surface. Centralizing them prevents scanner, engine, and UI behavior from
// drifting when a status is added or renamed.
const (
	StatusUnchecked            = "unchecked"
	StatusChecking             = "checking"
	StatusWaiting              = "waiting"
	StatusSkipped              = "skipped"
	StatusMissing              = "missing"
	StatusFailed               = "failed"
	StatusCurrent              = "current"
	StatusUpdateAvailable      = "update_available"
	StatusUpdated              = "updated"
	StatusUpdating             = "updating"
	StatusDownloading          = "downloading"
	StatusDownloaded           = "downloaded"
	StatusDownloadedUnverified = "downloaded_unverified"
	StatusSuccess              = "success"
	StatusStarted              = "started"
	StatusCancelled            = "cancelled"
	StatusCompletedWithErrors  = "completed_with_errors"
)

// ValidStatus reports whether value is part of the stable status vocabulary.
func ValidStatus(value string) bool {
	switch value {
	case StatusUnchecked, StatusChecking, StatusWaiting, StatusSkipped, StatusMissing,
		StatusFailed, StatusCurrent, StatusUpdateAvailable, StatusUpdated, StatusUpdating,
		StatusDownloading, StatusDownloaded, StatusDownloadedUnverified, StatusSuccess,
		StatusStarted, StatusCancelled, StatusCompletedWithErrors:
		return true
	default:
		return false
	}
}
