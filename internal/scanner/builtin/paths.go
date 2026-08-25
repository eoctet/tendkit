package builtin

import (
	"strings"

	"github.com/eoctet/tendkit/internal/model"
)

type PathDefinition struct {
	ID, Name, Binary, VersionCommand, CheckCommand, UpdateCommand, UpdateProbe string
	Provider                                                                   model.ProviderType
	Package, Description, URL, DownloadURL, DownloadFilename                   string
}

func PathDefinitions() []PathDefinition {
	definitions := make([]PathDefinition, 0, 100)
	definitions = append(definitions, LanguagePathDefinitions()...)
	definitions = append(definitions, AIPathDefinitions()...)
	definitions = append(definitions, DevelopmentPathDefinitions()...)
	return definitions
}

func FindPathByID(id string) (PathDefinition, bool) {
	for _, item := range PathDefinitions() {
		if strings.EqualFold(item.ID, id) {
			return item, true
		}
	}
	return PathDefinition{}, false
}
func FindPathByProviderPackage(provider model.ProviderType, name string) (PathDefinition, bool) {
	key := pathPackageKey(provider, name)
	for _, item := range PathDefinitions() {
		if pathPackageKey(item.Provider, item.Package) == key {
			return item, true
		}
	}
	return PathDefinition{}, false
}
func pathPackageKey(provider model.ProviderType, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	if provider == "" || name == "" {
		return ""
	}
	return strings.ToLower(string(provider)) + ":" + name
}
