package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	metadatautil "github.com/eoctet/tendkit/pkg/metadata"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

// localMetadataProvider is the shared local-state capability used by all
// providers. Remote metadata providers must not duplicate host inspection.
type localMetadataProvider struct{ runner runtimeutil.Runner }

func (p localMetadataProvider) Current(ctx context.Context, request Request) (string, error) {
	app := request.App
	var value string
	var err error
	switch app.Type {
	case model.ApplicationTypeBundle:
		var metadata metadatautil.MacApplicationMetadata
		metadata, err = metadatautil.ReadMacApplicationMetadata(ctx, app.InstallPath)
		value = metadata.Version
	case model.ApplicationTypePackage:
		ecosystem := packageEcosystem(app.Provider.Type)
		if ecosystem == "" {
			return "", CapabilityUnavailable(string(app.Provider.Type), CapabilityCurrent)
		}
		packageName := ""
		if ecosystem != metadatautil.PackageGo {
			packageName, err = requiredPackage(request, CapabilityCurrent)
			if err != nil {
				return "", err
			}
		}
		manager, managerErr := metadatautil.FindPackageManager(ecosystem)
		if managerErr != nil {
			err = managerErr
			break
		}
		if ecosystem == metadatautil.PackageGo {
			var metadata metadatautil.GoComponentMetadata
			metadata, err = metadatautil.ReadGoComponentMetadata(ctx, p.runner, manager, app.InstallPath, app.Environment)
			value = metadata.Version
		} else {
			value, err = metadatautil.ReadPackageVersion(ctx, p.runner, metadatautil.PackageTarget{
				Ecosystem: ecosystem, Manager: manager, Name: packageName,
				InstallPath: app.InstallPath, Environment: app.Environment,
			})
		}
	case model.ApplicationTypeCLI, model.ApplicationTypeSDK:
		value, err = metadatautil.DetectCLIVersion(ctx, p.runner, app.InstallPath, app.Environment)
	default:
		return "", CapabilityUnavailable(string(app.Provider.Type), CapabilityCurrent)
	}
	if err != nil {
		return "", &Error{Key: "provider.current_failed", Args: []any{app.Name}, Provider: string(app.Provider.Type), Capability: CapabilityCurrent, Cause: err}
	}
	if strings.TrimSpace(value) == "" {
		return "", &Error{Key: "provider.current_failed", Args: []any{app.Name}, Provider: string(app.Provider.Type), Capability: CapabilityCurrent, Cause: errors.New("current version is empty")}
	}
	return value, nil
}

type packageUpdateProvider struct{ runner runtimeutil.Runner }

func (p packageUpdateProvider) Update(ctx context.Context, request Request) error {
	app := request.App
	ecosystem := packageEcosystem(app.Provider.Type)
	if app.Type != model.ApplicationTypePackage || ecosystem == "" {
		return CapabilityUnavailable(string(app.Provider.Type), CapabilityUpdate)
	}
	packageName := ""
	var err error
	if ecosystem != metadatautil.PackageGo {
		packageName, err = requiredPackage(request, CapabilityUpdate)
		if err != nil {
			return err
		}
	}
	manager, err := metadatautil.FindPackageManager(ecosystem)
	if err != nil {
		return &Error{Key: "provider.package_update_failed", Args: []any{app.Name}, Provider: string(app.Provider.Type), Capability: CapabilityUpdate, Cause: err}
	}
	if ecosystem == metadatautil.PackageGo {
		metadata, metadataErr := metadatautil.ReadGoComponentMetadata(ctx, p.runner, manager, app.InstallPath, app.Environment)
		if metadataErr != nil {
			return &Error{Key: "provider.package_update_failed", Args: []any{app.Name}, Provider: string(app.Provider.Type), Capability: CapabilityUpdate, Cause: metadataErr}
		}
		packageName = metadata.Command
	}
	command, err := metadatautil.PackageUpdateCommand(metadatautil.PackageTarget{
		Ecosystem: ecosystem, Manager: manager, Name: packageName,
		InstallPath: app.InstallPath, Environment: app.Environment,
	})
	if err != nil {
		return &Error{Key: "provider.package_update_failed", Args: []any{app.Name}, Provider: string(app.Provider.Type), Capability: CapabilityUpdate, Cause: err}
	}
	result, err := p.runner.Run(ctx, command, app.Environment)
	if err != nil {
		return &Error{Key: "provider.package_update_failed", Args: []any{app.Name}, Provider: string(app.Provider.Type), Capability: CapabilityUpdate, Cause: err}
	}
	if result.ExitCode != 0 {
		return &Error{Key: "provider.package_update_exit", Args: []any{app.Name, result.ExitCode, strings.TrimSpace(result.Combined())}, Provider: string(app.Provider.Type), Capability: CapabilityUpdate}
	}
	return nil
}

func packageEcosystem(provider model.ProviderType) metadatautil.PackageEcosystem {
	switch provider {
	case model.ProviderNPM:
		return metadatautil.PackageNode
	case model.ProviderPyPI:
		return metadatautil.PackagePython
	case model.ProviderUV:
		return metadatautil.PackageUV
	case model.ProviderGo:
		return metadatautil.PackageGo
	default:
		return ""
	}
}
