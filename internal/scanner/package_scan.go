package scanner

import (
	"context"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/handler"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

// packageScanResult preserves completeness independently per ecosystem so one
// failed inventory cannot erase another ecosystem's stable state.
type packageScanResult struct {
	Discoveries []discovery
	Complete    map[string]bool
	Errors      map[string]error
}

// ecosystemScanResult is one handler's discoveries and completeness boundary.
type ecosystemScanResult struct {
	Discoveries []discovery
	Complete    bool
	Err         error
}

// scanPackages runs enabled ecosystems in stable order and records each result
// even when another ecosystem is unavailable or incomplete.
func scanPackages(ctx context.Context, settings model.PackageScanSettings, runner runtimeutil.Runner, exclusions exclusionMatcher, configured []model.Application, progress func(string, string)) packageScanResult {
	result := packageScanResult{Complete: make(map[string]bool), Errors: make(map[string]error)}
	appendResult := func(ecosystem string, scanned ecosystemScanResult) {
		result.Discoveries = append(result.Discoveries, scanned.Discoveries...)
		result.Complete[ecosystem] = scanned.Complete
		if scanned.Err != nil {
			result.Errors[ecosystem] = scanned.Err
		}
	}
	type configuredHandler struct {
		enabled bool
		label   string
		handler handler.Handler
	}
	handlers := []configuredHandler{
		{settings.Python, "Python", handler.NewPython(runner)},
		{settings.Node, "Node.js", handler.NewNode(runner)},
		{settings.Go, "Go", handler.NewGo(runner)},
		{settings.UV, "uv", handler.NewUV(runner)},
		{settings.Ruby, "Ruby", handler.NewRuby(runner)},
		{settings.HomebrewFormula, "Homebrew formula", handler.NewHomebrewFormula(runner)},
		{settings.HomebrewCask, "Homebrew cask", handler.NewHomebrewCask(runner)},
		{settings.Cargo, "Cargo", handler.NewCargo(runner)},
	}
	for _, item := range handlers {
		if !item.enabled || ctx.Err() != nil {
			continue
		}
		reportPackageManager(progress, item.label)
		appendResult(string(item.handler.Domain()), packageHandlerResult(
			item.handler.Scan(ctx, packageHandlerRequest(configured, progress)), exclusions,
		))
	}
	return result
}

func packageHandlerRequest(configured []model.Application, progress func(string, string)) handler.Request {
	return handler.Request{Configured: configured, Report: func(value handler.Progress) { reportPackageStep(progress, value.Stage, value.Subject) }}
}

func packageHandlerResult(scanned handler.Result, exclusions exclusionMatcher) ecosystemScanResult {
	converted := ecosystemScanResult{Complete: scanned.Complete, Err: scanned.Err}
	for _, candidate := range scanned.Candidates {
		if exclusions.excluded(candidate.Application, candidate.Aliases...) {
			continue
		}
		converted.Discoveries = append(converted.Discoveries, packageDiscovery(candidate.Application, candidate.CurrentVersion, candidate.Evidence))
	}
	return converted
}

func reportPackageManager(progress func(string, string), manager string) {
	if progress != nil {
		progress(model.ScanStagePackageManager, manager)
	}
}

func reportPackageStep(progress func(string, string), stage, subject string) {
	if progress != nil {
		progress(stage, subject)
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func packageDiscovery(app model.Application, current string, evidence *handler.InstallationEvidence) discovery {
	return discovery{App: app, State: model.ManagedStatus{CurrentVersion: version.Normalize(current), UpdateStatus: model.StatusUnchecked}, Evidence: evidence}
}
