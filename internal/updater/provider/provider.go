package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type commandRunner interface {
	Run(context.Context, string, map[string]string) (runtimeutil.Result, error)
}

// Request is the complete, immutable input for one Provider capability.
type Request struct {
	App              model.Application
	CurrentVersion   string
	Values           map[string]string
	SelectedArtifact string
}

// Error is a stable provider-domain error. Presentation layers may localize Key and Args.
type Error struct {
	Key        string
	Args       []any
	Provider   string
	Capability Capability
	Cause      error
}

func (e *Error) Error() string {
	if e.Provider != "" || e.Capability != "" {
		return fmt.Sprintf("%s: provider=%s capability=%s args=%v", e.Key, e.Provider, e.Capability, e.Args)
	}
	return fmt.Sprintf("%s: %v", e.Key, e.Args)
}
func (e *Error) Unwrap() error               { return e.Cause }
func NewError(key string, args ...any) error { return &Error{Key: key, Args: args} }
func WrapError(key string, cause error, args ...any) error {
	return &Error{Key: key, Args: args, Cause: cause}
}

func requiredPackage(request Request, capability Capability) (string, error) {
	name := strings.TrimSpace(request.App.Package)
	if name == "" {
		return "", &Error{
			Key: "provider.package_required", Args: []any{request.App.Name},
			Provider: string(request.App.Provider.Type), Capability: capability,
		}
	}
	return name, nil
}

var ErrUnavailable = NewError("provider.unavailable")

type Capability string

const (
	CapabilityCurrent  Capability = "current"
	CapabilityLatest   Capability = "latest"
	CapabilityUpdate   Capability = "update"
	CapabilityDownload Capability = "download"
	CapabilityInstall  Capability = "install"
	CapabilityChecksum Capability = "checksum"
	CapabilityArtifact Capability = "artifact"
)

type CurrentVersioner interface {
	Current(context.Context, Request) (string, error)
}
type LatestVersioner interface {
	Latest(context.Context, Request) (string, error)
}
type UpdateExecutor interface {
	Update(context.Context, Request) error
}

// DownloadResolver returns a complete, executable download description.
type DownloadResolver interface {
	Download(context.Context, Request) (model.Download, error)
}
type InstallExecutor interface {
	Install(context.Context, Request) error
}

// Checksummer returns only a normalized SHA256 value.
type Checksummer interface {
	Checksum(context.Context, Request) (string, error)
}

// ArtifactProvider returns only a package name or artifact identifier.
type ArtifactProvider interface {
	Artifact(context.Context, Request) (string, error)
}

// ArtifactCandidateProvider exposes safe, host-compatible choices before
// execution while remaining presentation-free.
type ArtifactCandidateProvider interface {
	ArtifactCandidates(context.Context, Request) ([]string, error)
}

// ArtifactChoiceProvider additionally reports when candidates require explicit
// human selection because host compatibility could not be inferred.
type ArtifactChoiceProvider interface {
	ArtifactChoices(context.Context, Request) (model.DownloadAssetChoices, error)
}

// Capabilities records only operations an implementation actually supports.
// A nil field is unavailable and must never be interpreted as success.
type Capabilities struct {
	Current  CurrentVersioner
	Latest   LatestVersioner
	Update   UpdateExecutor
	Download DownloadResolver
	Install  InstallExecutor
	Checksum Checksummer
	Artifact ArtifactProvider
}
