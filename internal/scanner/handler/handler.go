package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

type Domain string

const (
	Path   Domain = "path"
	MacApp Domain = "macapp"
	Python Domain = "python"
	Node   Domain = "node"
	UV     Domain = "uv"
	Go     Domain = "go"
	Ruby   Domain = "ruby"
)

type Progress struct{ Stage, Subject string }
type Request struct {
	Configured []model.Application
	Report     func(Progress)
}
type Candidate struct {
	Application    model.Application
	CurrentVersion string
	ObservationErr error
	Aliases        []string
}
type Result struct {
	Candidates []Candidate
	Complete   bool
	Err        error
}
type Handler interface {
	Domain() Domain
	Scan(context.Context, Request) Result
}
type TargetHandler interface {
	Handler
	ScanApplication(context.Context, model.Application, Request) (Candidate, bool, error)
}
type BundleHandler interface {
	TargetHandler
	BundleID(context.Context, string) (string, error)
	Inspect(context.Context, string) (BundleMetadata, error)
}

// BundleMetadata is the normalized metadata needed by scanner policies that
// operate on configured bundles without enumerating the macOS application domain.
type BundleMetadata struct {
	Path        string
	Name        string
	BundleID    string
	Category    string
	Description string
	Version     string
	FeedURL     string
}
type Runner interface {
	Run(context.Context, string, map[string]string) (runtimeutil.Result, error)
}
type CommandExitError struct{ ExitCode int }

func (e CommandExitError) Error() string        { return "command exited with non-zero status" }
func (e CommandExitError) Is(target error) bool { _, ok := target.(CommandExitError); return ok }

var ErrNotFound = errors.New("target not found")

// PackageManagerUnavailableError identifies a package-manager lookup failure
// while preserving an English error for non-presentation callers.
type PackageManagerUnavailableError struct{ Manager string }

func (e *PackageManagerUnavailableError) Error() string {
	return fmt.Sprintf("package manager %s not found", e.Manager)
}

// PackageInventoryIncompleteError identifies a parsed but incomplete package
// inventory so Scanner can render a stable localized message.
type PackageInventoryIncompleteError struct{ Ecosystem, Message string }

func (e *PackageInventoryIncompleteError) Error() string { return e.Message }
