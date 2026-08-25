package builtin

import (
	"strings"
	"testing"
	"unicode"

	"github.com/eoctet/tendkit/internal/model"
)

func TestBuiltInPathApplicationDefinitionsContainCoreManagers(t *testing.T) {
	wanted := map[string]bool{"go": false, "npm": false, "python3": false, "uv": false, "ruby": false, "gem": false}
	for _, definition := range PathDefinitions() {
		if _, exists := wanted[definition.ID]; exists {
			wanted[definition.ID] = true
		}
	}
	for id, found := range wanted {
		if !found {
			t.Fatalf("missing built-in path application %s", id)
		}
	}
}

func TestBuiltInPathApplicationCatalogHasOneHundredUniqueDefinitions(t *testing.T) {
	if got, want := len(LanguagePathDefinitions()), 46; got != want {
		t.Fatalf("language definition count = %d, want %d", got, want)
	}
	if got, want := len(AIPathDefinitions()), 15; got != want {
		t.Fatalf("AI definition count = %d, want %d", got, want)
	}
	if got, want := len(DevelopmentPathDefinitions()), 39; got != want {
		t.Fatalf("development definition count = %d, want %d", got, want)
	}
	definitions := PathDefinitions()
	if len(definitions) != 100 {
		t.Fatalf("built-in definition count = %d, want 100", len(definitions))
	}
	ids := make(map[string]bool, len(definitions))
	binaries := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		if definition.ID == "" || definition.Name == "" || definition.Binary == "" || definition.VersionCommand == "" || definition.Description == "" || definition.URL == "" {
			t.Fatalf("incomplete built-in definition: %#v", definition)
		}
		if !strings.HasPrefix(definition.URL, "https://") {
			t.Fatalf("definition %s uses non-HTTPS URL %q", definition.ID, definition.URL)
		}
		if !strings.Contains(definition.VersionCommand, definition.Binary) {
			t.Fatalf("definition %s version command %q does not invoke binary %q", definition.ID, definition.VersionCommand, definition.Binary)
		}
		if definition.UpdateCommand != "" && definition.UpdateProbe == "" {
			t.Fatalf("definition %s has an update command without a safe probe", definition.ID)
		}
		if definition.UpdateProbe != "" && !strings.Contains(definition.UpdateProbe, "--help") {
			t.Fatalf("definition %s uses a non-help update probe %q", definition.ID, definition.UpdateProbe)
		}
		if ids[definition.ID] {
			t.Fatalf("duplicate built-in ID %q", definition.ID)
		}
		if binaries[definition.Binary] {
			t.Fatalf("duplicate built-in binary %q", definition.Binary)
		}
		ids[definition.ID] = true
		binaries[definition.Binary] = true
	}
}

func TestBuiltInDefinitionProvidersUseRegisteredVocabulary(t *testing.T) {
	registered := map[model.ProviderType]bool{
		model.ProviderDefault: true, model.ProviderGitHubRelease: true,
		model.ProviderGitHubTag: true, model.ProviderNPM: true, model.ProviderPyPI: true,
		model.ProviderGo: true, model.ProviderNodeLTS: true,
	}
	for _, definition := range PathDefinitions() {
		if !definition.Provider.Valid() || !registered[definition.Provider] {
			t.Fatalf("definition %s uses unregistered provider %q", definition.ID, definition.Provider)
		}
	}
}

func TestPathProviderDefinitionContract(t *testing.T) {
	want := map[string]struct {
		provider    model.ProviderType
		packageName string
	}{
		"git": {model.ProviderGitHubTag, "git/git"}, "aws": {model.ProviderGitHubTag, "aws/aws-cli"},
		"kubectl": {model.ProviderGitHubRelease, "kubernetes/kubernetes"}, "redis_cli": {model.ProviderGitHubRelease, "redis/redis"},
		"ruby": {model.ProviderGitHubRelease, "ruby/ruby"}, "rustc": {model.ProviderGitHubRelease, "rust-lang/rust"},
		"dotnet": {model.ProviderGitHubRelease, "dotnet/sdk"}, "swift": {model.ProviderGitHubRelease, "swiftlang/swift"},
		"clang": {model.ProviderGitHubRelease, "llvm/llvm-project"}, "php": {model.ProviderGitHubRelease, "php/php-src"},
		"lua": {model.ProviderGitHubRelease, "lua/lua"}, "scala": {model.ProviderGitHubRelease, "scala/scala"},
		"rustup": {model.ProviderGitHubTag, "rust-lang/rustup"}, "nim": {model.ProviderGitHubTag, "nim-lang/Nim"},
		"mvn": {model.ProviderDefault, ""},
	}
	for _, definition := range PathDefinitions() {
		expected, ok := want[definition.ID]
		if !ok {
			continue
		}
		if definition.Provider != expected.provider || definition.Package != expected.packageName {
			t.Fatalf("%s = provider=%q package=%q", definition.ID, definition.Provider, definition.Package)
		}
	}
	for id := range want {
		found := false
		for _, definition := range PathDefinitions() {
			if definition.ID == id {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing verified definition %s", id)
		}
	}
}

func TestNodeDefinitionLeavesDownloadToNodeLTSProvider(t *testing.T) {
	for _, definition := range LanguagePathDefinitions() {
		if definition.ID == "node" {
			if definition.DownloadURL != "" || definition.DownloadFilename != "" {
				t.Fatalf("node embeds a download action: %#v", definition)
			}
			return
		}
	}
	t.Fatal("missing Node definition")
}

func TestBuiltInDescriptionsAreEnglishDefaults(t *testing.T) {
	for _, definition := range PathDefinitions() {
		if strings.TrimSpace(definition.Description) == "" {
			t.Fatalf("definition %s has no description", definition.ID)
		}
		for _, r := range definition.Description {
			if unicode.Is(unicode.Han, r) {
				t.Fatalf("definition %s has a localized description %q", definition.ID, definition.Description)
			}
		}
	}
}

func TestFlutterDefinitionUsesSupportedVerificationFlag(t *testing.T) {
	for _, definition := range PathDefinitions() {
		if definition.ID != "flutter" {
			continue
		}
		if definition.CheckCommand != "flutter upgrade --verify-only" || strings.Contains(definition.CheckCommand, "--dry-run") {
			t.Fatalf("unexpected Flutter check command %q", definition.CheckCommand)
		}
		return
	}
	t.Fatal("Flutter definition not found")
}

func TestPathDefinitionsPreserveMigratedCatalogContract(t *testing.T) {
	definitions := PathDefinitions()
	if got, want := len(definitions), 100; got != want {
		t.Fatalf("definition count = %d, want %d", got, want)
	}

	ids := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if strings.TrimSpace(definition.ID) == "" {
			t.Fatalf("definition has an empty ID: %#v", definition)
		}
		key := strings.ToLower(definition.ID)
		if _, exists := ids[key]; exists {
			t.Fatalf("duplicate definition ID ignoring case: %q", definition.ID)
		}
		ids[key] = struct{}{}

		if !definition.Provider.Valid() {
			t.Fatalf("definition %q has invalid provider %q", definition.ID, definition.Provider)
		}
	}
}

func TestFindPathByIDIsCaseInsensitiveAndReportsMissingDefinitions(t *testing.T) {
	definition, found := FindPathByID("CoDeX")
	if !found || definition.ID != "codex" {
		t.Fatalf("FindPathByID(CoDeX) = (%#v, %t), want codex and true", definition, found)
	}

	if definition, found := FindPathByID("missing-definition"); found || definition != (PathDefinition{}) {
		t.Fatalf("FindPathByID(missing-definition) = (%#v, %t), want zero value and false", definition, found)
	}
}

func TestFindPathByProviderPackageNormalizesPackageNamesAndReportsMissingDefinitions(t *testing.T) {
	definition, found := FindPathByProviderPackage(model.ProviderPyPI, "  SHELL_GPT  ")
	if !found || definition.ID != "shell_gpt" {
		t.Fatalf("FindPathByProviderPackage(normalized shell_gpt) = (%#v, %t), want shell_gpt and true", definition, found)
	}

	if definition, found := FindPathByProviderPackage(model.ProviderNPM, "missing-package"); found || definition != (PathDefinition{}) {
		t.Fatalf("FindPathByProviderPackage(missing-package) = (%#v, %t), want zero value and false", definition, found)
	}
}

func TestPathDefinitionsReturnsFreshSlice(t *testing.T) {
	first := PathDefinitions()
	second := PathDefinitions()
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("PathDefinitions returned an empty catalog")
	}

	originalID := second[0].ID
	first[0].ID = "mutated"
	if second[0].ID != originalID {
		t.Fatalf("mutating one result changed another result: got %q, want %q", second[0].ID, originalID)
	}
}

func TestMacAppDefinitionsContainMatchCriteriaAndReturnFreshSlices(t *testing.T) {
	first := MacAppDefinitions()
	if len(first) == 0 {
		t.Fatal("MacAppDefinitions returned an empty catalog")
	}
	for _, definition := range first {
		if definition.BundleIDPrefix == "" && definition.NameContains == "" {
			t.Fatalf("Mac application definition has no match criteria: %#v", definition)
		}
	}

	second := MacAppDefinitions()
	original := second[0]
	first[0] = MacAppDefinition{BundleIDPrefix: "mutated"}
	if second[0] != original {
		t.Fatalf("mutating one result changed another result: got %#v, want %#v", second[0], original)
	}
}

func TestMatchesMacAppCatalogNormalizesKeyPrefixesAndNames(t *testing.T) {
	tests := []struct {
		name, bundleID, appName string
		want                    bool
	}{
		{name: "case-insensitive bundle prefix with whitespace", bundleID: "  COM.MICROSOFT.VSCODE.INSIDERS  ", want: true},
		{name: "case-insensitive application name with whitespace", appName: "  Visual Studio Code  ", want: true},
		{name: "unknown application", bundleID: "org.example.unknown", appName: "Unknown Application", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MatchesMacAppCatalog(test.bundleID, test.appName); got != test.want {
				t.Fatalf("MatchesMacAppCatalog(%q, %q) = %t, want %t", test.bundleID, test.appName, got, test.want)
			}
		})
	}
}

func TestFindMacAppProjectRecognizesKnownProjectsAndRejectsUnknownBundles(t *testing.T) {
	tests := []struct {
		bundleID, want string
	}{
		{bundleID: "  COM.MICROSOFT.VSCODE.INSIDERS  ", want: "microsoft/vscode"},
		{bundleID: "  Com.GitHub.GitHub-Desktop  ", want: "desktop/desktop"},
		{bundleID: "org.example.unknown", want: ""},
	}
	for _, test := range tests {
		if got := FindMacAppProject(test.bundleID); got != test.want {
			t.Fatalf("FindMacAppProject(%q) = %q, want %q", test.bundleID, got, test.want)
		}
	}
}
