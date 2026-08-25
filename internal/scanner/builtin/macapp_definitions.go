package builtin

import "strings"

type MacAppDefinition struct{ BundleIDPrefix, NameContains, GitHubProject string }

var macApps = []MacAppDefinition{
	{BundleIDPrefix: "com.apple.dt."}, {BundleIDPrefix: "com.jetbrains."}, {BundleIDPrefix: "org.eclipse."}, {BundleIDPrefix: "com.microsoft.vscode", GitHubProject: "microsoft/vscode"}, {BundleIDPrefix: "com.google.android.studio"}, {BundleIDPrefix: "org.jkiss.dbeaver", GitHubProject: "dbeaver/dbeaver"}, {BundleIDPrefix: "com.docker.docker"}, {BundleIDPrefix: "org.godotengine.", GitHubProject: "godotengine/godot"}, {BundleIDPrefix: "com.postmanlabs."}, {BundleIDPrefix: "com.sublimetext."}, {BundleIDPrefix: "dev.zed.", GitHubProject: "zed-industries/zed"}, {BundleIDPrefix: "com.todesktop.230313mzl4w4u92"}, {BundleIDPrefix: "com.github.wez.wezterm", GitHubProject: "wezterm/wezterm"}, {BundleIDPrefix: "net.kovidgoyal.kitty", GitHubProject: "kovidgoyal/kitty"}, {BundleIDPrefix: "com.mitchellh.ghostty", GitHubProject: "ghostty-org/ghostty"}, {BundleIDPrefix: "com.github.github-desktop", GitHubProject: "desktop/desktop"}, {BundleIDPrefix: "com.torusknot.sourceTree"}, {BundleIDPrefix: "com.fournova.tower"},
	{NameContains: "xcode"}, {NameContains: "android studio"}, {NameContains: "visual studio code"}, {NameContains: "vscode"}, {NameContains: "cursor"}, {NameContains: "zed"}, {NameContains: "intellij"}, {NameContains: "pycharm"}, {NameContains: "webstorm"}, {NameContains: "goland"}, {NameContains: "clion"}, {NameContains: "datagrip"}, {NameContains: "rider"}, {NameContains: "rubymine"}, {NameContains: "appcode"}, {NameContains: "dbeaver"}, {NameContains: "docker"}, {NameContains: "postman"}, {NameContains: "insomnia"}, {NameContains: "visualvm"}, {NameContains: "cmake"}, {NameContains: "godot"}, {NameContains: "sublime text"}, {NameContains: "nova"}, {NameContains: "github desktop"}, {NameContains: "sourcetree"}, {NameContains: "iterm"}, {NameContains: "ghostty"}, {NameContains: "wezterm"}, {NameContains: "kitty"}, {NameContains: "warp"}, {NameContains: "wireshark"},
}

func MacAppDefinitions() []MacAppDefinition { return append([]MacAppDefinition(nil), macApps...) }
func FindMacAppProject(bundleID string) string {
	b := strings.ToLower(strings.TrimSpace(bundleID))
	for _, d := range macApps {
		if d.GitHubProject != "" && strings.HasPrefix(b, strings.ToLower(d.BundleIDPrefix)) {
			return d.GitHubProject
		}
	}
	return ""
}
func MatchesMacAppCatalog(bundleID, name string) bool {
	b, n := strings.ToLower(strings.TrimSpace(bundleID)), strings.ToLower(strings.TrimSpace(name))
	for _, d := range macApps {
		if d.BundleIDPrefix != "" && strings.HasPrefix(b, strings.ToLower(d.BundleIDPrefix)) {
			return true
		}
		if d.NameContains != "" && strings.Contains(n, strings.ToLower(d.NameContains)) {
			return true
		}
	}
	return false
}
