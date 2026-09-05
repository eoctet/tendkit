package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestModelVocabularyContract(t *testing.T) {
	t.Run("identity normalization", func(t *testing.T) {
		for _, test := range []struct{ ecosystem, name, want string }{
			{"cli", "gnu make", "cli:gnu-make"},
			{"node", "@playwright/cli", "package:node:playwright-cli"},
			{"go", "github.com/haya14busa/goplay/cmd/goplay", "package:go:goplay"},
			{"uv", "foo.bar", "package:uv:foobar"},
			{"uv", "foo_bar", "package:uv:foobar"},
			{"uv", "foo-bar", "package:uv:foo-bar"},
		} {
			got := "cli:" + NormalizeIdentityName(test.name)
			if test.ecosystem != "cli" {
				got = PackageIdentity(test.ecosystem, test.name)
			}
			if got != test.want {
				t.Fatalf("identity(%s,%q)=%q, want %q", test.ecosystem, test.name, got, test.want)
			}
		}
	})

	t.Run("update modes and provider types", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			valid bool
		}{
			{"auto", ModeAuto.Valid()}, {"download", ModeDownload.Valid()}, {"check", ModeCheck.Valid()}, {"install", ModeInstall.Valid()},
			{"empty mode", UpdateMode("").Valid() == false}, {"unknown mode", UpdateMode("later").Valid() == false},
			{"default provider", ProviderDefault.Valid()}, {"github release", ProviderGitHubRelease.Valid()}, {"github tag", ProviderGitHubTag.Valid()},
			{"npm", ProviderNPM.Valid()}, {"pypi", ProviderPyPI.Valid()}, {"uv", ProviderUV.Valid()}, {"jetbrains", ProviderJetBrains.Valid()}, {"go", ProviderGo.Valid()},
			{"node lts", ProviderNodeLTS.Valid()}, {"sparkle", ProviderSparkle.Valid()}, {"homebrew", ProviderHomebrew.Valid()}, {"cargo", ProviderCargo.Valid()},
			{"unknown provider", ProviderType("custom").Valid() == false},
		} {
			t.Run(test.name, func(t *testing.T) {
				if !test.valid {
					t.Fatal("expected valid vocabulary value")
				}
			})
		}
	})

	t.Run("status values and timestamps", func(t *testing.T) {
		for _, value := range []string{StatusUnchecked, StatusChecking, StatusWaiting, StatusSkipped, StatusMissing, StatusFailed, StatusCurrent, StatusUpdateAvailable, StatusUpdated, StatusUpdating, StatusDownloading, StatusDownloaded, StatusDownloadedUnverified, StatusSuccess, StatusStarted, StatusCancelled, StatusCompletedWithErrors} {
			if !ValidStatus(value) {
				t.Fatalf("ValidStatus(%q) = false", value)
			}
		}
		if ValidStatus("unknown") {
			t.Fatal("unknown status accepted")
		}
		if _, err := time.Parse(time.RFC3339, Now()); err != nil {
			t.Fatalf("Now() is not RFC3339: %v", err)
		}
	})

	t.Run("application type fields", func(t *testing.T) {
		want := map[string]string{
			"cli": ApplicationTypeCLI, "application": ApplicationTypeBundle,
			"package": ApplicationTypePackage, "sdk": ApplicationTypeSDK,
		}
		for expected, actual := range want {
			if actual != expected {
				t.Fatalf("application type constant = %q, want %q", actual, expected)
			}
		}
	})

	t.Run("provider action fields", func(t *testing.T) {
		want := map[string]string{
			"provider.type":             ApplicationFieldProviderType,
			"provider.actions.version":  ApplicationFieldActionVersion,
			"provider.actions.check":    ApplicationFieldActionCheck,
			"provider.actions.update":   ApplicationFieldActionUpdate,
			"provider.actions.download": ApplicationFieldActionDownload,
			"provider.actions.install":  ApplicationFieldActionInstall,
		}
		for expected, actual := range want {
			if actual != expected {
				t.Fatalf("provider field constant = %q, want %q", actual, expected)
			}
		}
	})
}

func TestModelConfigStateContract(t *testing.T) {
	t.Run("provider action accessors", func(t *testing.T) {
		download := &Download{URL: "https://example.invalid/tool.zip"}
		for _, test := range []struct {
			name     string
			provider ProviderConfig
			version  string
			check    string
			update   string
			download *Download
			install  string
			has      bool
		}{
			{name: "no actions"},
			{name: "empty actions", provider: ProviderConfig{Actions: &ProviderActions{}}},
			{name: "all actions", provider: ProviderConfig{Actions: &ProviderActions{Version: "version", Check: "check", Update: "update", Download: download, Install: "install"}}, version: "version", check: "check", update: "update", download: download, install: "install", has: true},
			{name: "download only", provider: ProviderConfig{Actions: &ProviderActions{Download: download}}, download: download, has: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				if got := test.provider.VersionAction(); got != test.version {
					t.Fatalf("VersionAction() = %q, want %q", got, test.version)
				}
				if got := test.provider.CheckAction(); got != test.check {
					t.Fatalf("CheckAction() = %q, want %q", got, test.check)
				}
				if got := test.provider.UpdateAction(); got != test.update {
					t.Fatalf("UpdateAction() = %q, want %q", got, test.update)
				}
				if got := test.provider.DownloadAction(); got != test.download {
					t.Fatalf("DownloadAction() = %#v, want %#v", got, test.download)
				}
				if got := test.provider.InstallAction(); got != test.install {
					t.Fatalf("InstallAction() = %q, want %q", got, test.install)
				}
				if got := test.provider.HasActions(); got != test.has {
					t.Fatalf("HasActions() = %v, want %v", got, test.has)
				}
			})
		}
	})

	t.Run("comparison and status copy boundaries", func(t *testing.T) {
		base := Config{Settings: Settings{Workers: 2}, Apps: []Application{{ID: "one", Name: "One", StatusManaged: ManagedStatus{CurrentVersion: "1.0.0"}}, {ID: "two", Name: "Two", StatusManaged: ManagedStatus{UpdateStatus: StatusCurrent}}}, ScanVersionControl: map[string]map[string]ScanKeepResolution{"one": {"name": {Fingerprint: "keep"}}}}
		changedStatus := base
		changedStatus.Apps = append([]Application(nil), base.Apps...)
		changedStatus.Apps[0].StatusManaged.LatestVersion = "1.1.0"
		if !ConfigEqualExceptStatuses(base, changedStatus) || !ConfigEqualExceptRuntime(base, changedStatus) {
			t.Fatal("status-only change should be ignored by both comparisons")
		}
		changedKeep := changedStatus
		changedKeep.ScanVersionControl = nil
		if ConfigEqualExceptStatuses(base, changedKeep) || !ConfigEqualExceptRuntime(base, changedKeep) {
			t.Fatal("scan keep should only be ignored by runtime comparison")
		}
		changedConfig := changedStatus
		changedConfig.Apps = append([]Application(nil), changedStatus.Apps...)
		changedConfig.Apps[0].Name = "Renamed"
		if ConfigEqualExceptStatuses(base, changedConfig) || ConfigEqualExceptRuntime(base, changedConfig) {
			t.Fatal("application configuration change should not be ignored")
		}

		destination := Config{Apps: []Application{{ID: "one", StatusManaged: ManagedStatus{UpdateStatus: StatusFailed}}, {ID: "missing", StatusManaged: ManagedStatus{UpdateStatus: StatusSkipped}}}}
		CopyStatuses(nil, base)
		CopyStatuses(&destination, base)
		if got := destination.Apps[0].StatusManaged.CurrentVersion; got != "1.0.0" || destination.Apps[1].StatusManaged.UpdateStatus != StatusSkipped {
			t.Fatalf("CopyStatuses() = %#v", destination.Apps)
		}
	})
}

func TestModelJSONContract(t *testing.T) {
	t.Run("result uses abbreviated application fields", func(t *testing.T) {
		data, err := json.Marshal(Result{AppID: "go", Name: "Go", Mode: ModeCheck, Status: "current"})
		if err != nil {
			t.Fatal(err)
		}
		encoded := string(data)
		for _, field := range []string{`"app_id":"go"`, `"app_name":"Go"`, `"update_mode":"check"`} {
			if !strings.Contains(encoded, field) {
				t.Fatalf("result JSON missing %s: %s", field, encoded)
			}
		}
		for _, legacy := range []string{`"tool_id"`, `"application_id"`, `"AppID"`} {
			if strings.Contains(encoded, legacy) {
				t.Fatalf("result JSON contains obsolete field %s: %s", legacy, encoded)
			}
		}
	})

	t.Run("download omits empty URL", func(t *testing.T) {
		data, err := json.Marshal(Download{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), `"url"`) {
			t.Fatalf("empty download URL was encoded: %s", data)
		}
	})

	t.Run("clone preserves JSON canonicalization", func(t *testing.T) {
		original := Config{
			Settings: Settings{
				ProviderURLs: map[string]string{},
				Downloader:   DownloaderSettings{ExtraArgs: []string{}},
				Scan:         ScanSettings{BundleID: []string{}, Exclude: []string{}},
			},
			Apps: []Application{{
				Environment: map[string]string{},
				Provider:    ProviderConfig{Actions: &ProviderActions{Download: &Download{ExtraArgs: []string{}}}},
			}},
			ScanVersionControl: map[string]map[string]ScanKeepResolution{},
		}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var canonical Config
		if err := json.Unmarshal(data, &canonical); err != nil {
			t.Fatal(err)
		}
		if cloned := CloneConfig(original); !reflect.DeepEqual(cloned, canonical) {
			t.Fatalf("explicit clone no longer matches established JSON clone semantics:\nclone=%#v\njson=%#v", cloned, canonical)
		}
	})
}

func TestModelScanStateContract(t *testing.T) {
	t.Run("fingerprint is stable across field order", func(t *testing.T) {
		current := Application{ID: "sample", Name: "Sample", InstallPath: "/Applications/Sample.app"}
		proposed := current
		proposed.Description, proposed.InstallPath = "candidate", "/Applications/Sample Next.app"
		description := ScanFieldChange{Field: "description", Current: current.Description, Proposed: proposed.Description}
		path := ScanFieldChange{Field: "install_path", Current: current.InstallPath, Proposed: proposed.InstallPath}
		first := ScanKeepFingerprint(current.ID, current, proposed, []ScanFieldChange{description, path}, description)
		second := ScanKeepFingerprint(current.ID, current, proposed, []ScanFieldChange{path, description}, description)
		if first == "" || first != second {
			t.Fatalf("fingerprint depends on field order: %q != %q", first, second)
		}
	})

	t.Run("fingerprint ignores managed status", func(t *testing.T) {
		current := Application{ID: "sample", Name: "Sample", Description: "current", StatusManaged: ManagedStatus{CurrentVersion: "1.0.0", UpdateStatus: StatusUnchecked}}
		proposed := current
		proposed.Description = "candidate"
		change := ScanFieldChange{Field: "description", Current: current.Description, Proposed: proposed.Description}
		first := ScanKeepFingerprint(current.ID, current, proposed, []ScanFieldChange{change}, change)
		current.StatusManaged = ManagedStatus{CurrentVersion: "1.0.1", LatestVersion: "1.0.2", UpdateStatus: StatusUpdateAvailable, LastCheckTime: "2026-08-17T00:00:00+08:00"}
		proposed.StatusManaged = current.StatusManaged
		second := ScanKeepFingerprint(current.ID, current, proposed, []ScanFieldChange{change}, change)
		if first == "" || first != second {
			t.Fatalf("fingerprint changed with managed status: %q != %q", first, second)
		}
	})

	t.Run("run status merge preserves concurrent scan keep", func(t *testing.T) {
		base := Config{Apps: []Application{{ID: "sample", Description: "configured", StatusManaged: ManagedStatus{CurrentVersion: "1.0.0", UpdateStatus: StatusUnchecked}}}}
		completed := base
		completed.Apps = append([]Application(nil), base.Apps...)
		completed.Apps[0].StatusManaged = ManagedStatus{CurrentVersion: "1.0.0", LatestVersion: "1.1.0", UpdateStatus: StatusUpdateAvailable}
		latest := base
		latest.ScanVersionControl = map[string]map[string]ScanKeepResolution{"sample": {"description": {Fingerprint: strings.Repeat("a", 64), RecordedAt: "2026-08-17T00:00:00+08:00"}}}
		merged, ok := MergeRunStatuses(base, latest, completed)
		if !ok || merged.Apps[0].StatusManaged.UpdateStatus != StatusUpdateAvailable || merged.ScanVersionControl["sample"]["description"].Fingerprint == "" {
			t.Fatalf("run status merge = %#v, ok=%v", merged, ok)
		}
		latest.Apps = append([]Application(nil), latest.Apps...)
		latest.Apps[0].StatusManaged.UpdateStatus = StatusCurrent
		if _, ok := MergeRunStatuses(base, latest, completed); ok {
			t.Fatal("run status merge accepted concurrent status update")
		}
		latest = base
		latest.Apps = append([]Application(nil), latest.Apps...)
		latest.Apps[0].Description = "changed configuration"
		if _, ok := MergeRunStatuses(base, latest, completed); ok {
			t.Fatal("run status merge accepted concurrent application configuration")
		}
		latest = base
		latest.Settings.Language = "en"
		if _, ok := MergeRunStatuses(base, latest, completed); ok {
			t.Fatal("run status merge accepted concurrent global settings")
		}
	})

	t.Run("unmanaged transition clears scan version control", func(t *testing.T) {
		managed := Application{ID: "managed", ScanManaged: true}
		other := Application{ID: "other", ScanManaged: true}
		state := Config{Apps: []Application{managed, other}, ScanVersionControl: map[string]map[string]ScanKeepResolution{"managed": {}, "other": {}}}
		proposed := Config{Apps: []Application{{ID: "managed"}, other}, ScanVersionControl: state.ScanVersionControl}
		changed := ClearScanVersionControlForUnmanagedTransitions(&state, &proposed)
		if !changed || proposed.ScanVersionControl["managed"] != nil || proposed.ScanVersionControl["other"] == nil {
			t.Fatalf("unmanage cleanup changed wrong records: %#v", proposed.ScanVersionControl)
		}
		state = Config{Apps: []Application{{ID: "managed"}}, ScanVersionControl: map[string]map[string]ScanKeepResolution{"managed": {}}}
		proposed = Config{Apps: []Application{managed}, ScanVersionControl: state.ScanVersionControl}
		if ClearScanVersionControlForUnmanagedTransitions(&state, &proposed) || proposed.ScanVersionControl["managed"] == nil {
			t.Fatalf("manage transition changed scan keeps: %#v", proposed.ScanVersionControl)
		}
	})
}

func TestModelCloneContract(t *testing.T) {
	t.Run("scan keep maps preserve nil and empty shapes", func(t *testing.T) {
		original := Config{ScanVersionControl: map[string]map[string]ScanKeepResolution{
			"nil": nil, "empty": {},
		}}
		cloned := CloneConfig(original)
		if !reflect.DeepEqual(cloned.ScanVersionControl, original.ScanVersionControl) {
			t.Fatalf("scan keep shapes changed: %#v", cloned.ScanVersionControl)
		}
		cloned.ScanVersionControl["empty"]["name"] = ScanKeepResolution{Fingerprint: "changed"}
		delete(cloned.ScanVersionControl, "nil")
		if _, exists := original.ScanVersionControl["nil"]; !exists || len(original.ScanVersionControl["empty"]) != 0 {
			t.Fatalf("scan keep clone shares maps: %#v", original.ScanVersionControl)
		}
	})
	t.Run("clone does not share mutable state", func(t *testing.T) {
		original := Config{
			Settings: Settings{
				HTTP:         &HTTPSettings{TimeoutSeconds: 10},
				ProviderURLs: map[string]string{"github": "https://example.invalid"},
				Downloader:   DownloaderSettings{ExtraArgs: []string{"--retry=1"}},
				Scan:         ScanSettings{BundleID: []string{"com.example.app"}, Exclude: []string{"ignored"}},
			},
			Apps: []Application{{
				ID: "sample", Environment: map[string]string{"TOKEN": "value"},
				Provider: ProviderConfig{Actions: &ProviderActions{Download: &Download{ExtraArgs: []string{"--retry=2"}}}},
			}},
			ScanVersionControl: map[string]map[string]ScanKeepResolution{
				"sample": {"description": {Fingerprint: "original"}},
			},
		}
		cloned := CloneConfig(original)
		cloned.Settings.HTTP.TimeoutSeconds = 20
		cloned.Settings.ProviderURLs["github"] = "changed"
		cloned.Settings.Downloader.ExtraArgs[0] = "changed"
		cloned.Settings.Scan.BundleID[0] = "changed"
		cloned.Settings.Scan.Exclude[0] = "changed"
		cloned.Apps[0].Environment["TOKEN"] = "changed"
		cloned.Apps[0].Provider.Actions.Update = "changed"
		cloned.Apps[0].Provider.Actions.Download.ExtraArgs[0] = "changed"
		cloned.ScanVersionControl["sample"]["description"] = ScanKeepResolution{Fingerprint: "changed"}

		if original.Settings.HTTP.TimeoutSeconds != 10 ||
			original.Settings.ProviderURLs["github"] != "https://example.invalid" ||
			original.Settings.Downloader.ExtraArgs[0] != "--retry=1" ||
			original.Settings.Scan.BundleID[0] != "com.example.app" || original.Settings.Scan.Exclude[0] != "ignored" ||
			original.Apps[0].Environment["TOKEN"] != "value" || original.Apps[0].Provider.Actions.Update != "" ||
			original.Apps[0].Provider.Actions.Download.ExtraArgs[0] != "--retry=2" ||
			original.ScanVersionControl["sample"]["description"].Fingerprint != "original" {
			t.Fatalf("clone mutation leaked into original: %#v", original)
		}
	})
}
