package provider

import (
	"context"
	"regexp"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
	"github.com/eoctet/tendkit/pkg/version"
)

var (
	latestVersionLine = regexp.MustCompile(`(?im)^\s*(?:the\s+)?latest\s+version\s*:\s*(.+)$`)
	noUpdateMessage   = regexp.MustCompile(`(?i)\b(?:no updates?|up[ ._-]?to[ ._-]?date|already current|already (?:on )?(?:the )?latest)\b`)
)

// actionBackedProvider adapts configured default actions without advertising an
// action that is absent. It is intentionally constructed per application.
type actionBackedProvider struct {
	runner       runtimeutil.Runner
	allowInstall bool
}

func ActionCapabilities(runner runtimeutil.Runner, request Request, allowInstall bool) Capabilities {
	implementation := &actionBackedProvider{runner: runner, allowInstall: allowInstall}
	capabilities := Capabilities{}
	if strings.TrimSpace(request.App.Provider.VersionAction()) != "" {
		capabilities.Current = implementation
	}
	if strings.TrimSpace(request.App.Provider.CheckAction()) != "" {
		capabilities.Latest = implementation
	}
	if strings.TrimSpace(request.App.Provider.UpdateAction()) != "" {
		capabilities.Update = implementation
	}
	if request.App.Provider.DownloadAction() != nil {
		capabilities.Download = implementation
		if strings.TrimSpace(request.App.Provider.DownloadAction().ChecksumValue) != "" {
			capabilities.Checksum = implementation
		}
	}
	if allowInstall && strings.TrimSpace(request.App.Provider.InstallAction()) != "" {
		capabilities.Install = implementation
	}
	return capabilities
}
func (p *actionBackedProvider) Current(ctx context.Context, request Request) (string, error) {
	output, err := p.run(ctx, request, request.App.Provider.VersionAction(), "engine.version_exit")
	if err != nil {
		return "", err
	}
	return version.Extract(output)
}
func (p *actionBackedProvider) Latest(ctx context.Context, request Request) (string, error) {
	output, err := p.run(ctx, request, request.App.Provider.CheckAction(), "provider.command_exit")
	if err != nil {
		return "", err
	}
	if latestLine := latestVersionLine.FindStringSubmatch(output); len(latestLine) > 1 {
		return version.Extract(latestLine[1])
	}
	if noUpdateMessage.MatchString(output) && request.CurrentVersion != "" {
		return request.CurrentVersion, nil
	}
	return version.Extract(output)
}
func (p *actionBackedProvider) Update(ctx context.Context, request Request) error {
	_, err := p.run(ctx, request, request.App.Provider.UpdateAction(), "engine.update_exit")
	return err
}
func (p *actionBackedProvider) Download(_ context.Context, request Request) (model.Download, error) {
	action := request.App.Provider.DownloadAction()
	if action == nil {
		return model.Download{}, CapabilityUnavailable(string(request.App.Provider.Type), CapabilityDownload)
	}
	return *action, nil
}
func (p *actionBackedProvider) Install(ctx context.Context, request Request) error {
	if !p.allowInstall {
		return CapabilityUnavailable(string(request.App.Provider.Type), CapabilityInstall)
	}
	_, err := p.run(ctx, request, request.App.Provider.InstallAction(), "engine.install_exit")
	return err
}
func (p *actionBackedProvider) Checksum(_ context.Context, request Request) (string, error) {
	action := request.App.Provider.DownloadAction()
	if action == nil {
		return "", CapabilityUnavailable(string(request.App.Provider.Type), CapabilityChecksum)
	}
	return normalizeSHA256(action.ChecksumValue)
}
func (p *actionBackedProvider) run(ctx context.Context, request Request, action, exitKey string) (string, error) {
	rendered, err := runtimeutil.Render(action, request.Values, true)
	if err != nil {
		return "", err
	}
	result, err := p.runner.Run(ctx, rendered, request.App.Environment)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", NewError(exitKey, result.ExitCode, strings.TrimSpace(result.Combined()))
	}
	return result.Combined(), nil
}
