package updater

import (
	"fmt"
	"io"

	"strings"

	"crypto/sha256"
	"testing"

	"path/filepath"

	"context"
	"sync"

	"errors"

	"os"

	"time"

	"github.com/eoctet/tendkit/internal/model"
)

func TestUpdaterDownloadFlow(t *testing.T) {
	t.Run("download-asset-candidates-collects-application-failures", func(t *testing.T) {
		apps := []model.Application{
			{ID: "failed", Name: "Failed", StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}},
			{ID: "empty", Name: "Empty", StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}},
			{ID: "ready", Name: "Ready", StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}},
		}
		updater := &Updater{engine: engine{checker: assetCandidateChecker{
			checkerFunc: checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil }),
			choices: map[string]model.DownloadAssetChoices{
				"empty": {Candidates: []string{}},
				"ready": {Candidates: []string{"ready.dmg"}, SelectionRequired: true},
			},
			failures: map[string]error{"failed": errors.New("provider failed")},
		}}}

		var progress []model.DownloadAssetPreflightProgress
		choices, failures, err := updater.DownloadAssetCandidates(context.Background(), apps, func(event model.DownloadAssetPreflightProgress) {
			progress = append(progress, event)
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(choices["empty"].Candidates) != 0 || len(choices["ready"].Candidates) != 1 || choices["ready"].Candidates[0] != "ready.dmg" || !choices["ready"].SelectionRequired {
			t.Fatalf("choices = %#v", choices)
		}
		if len(failures) != 1 || failures["failed"] == nil {
			t.Fatalf("failures = %#v", failures)
		}
		wantStages := []model.DownloadAssetPreflightStage{
			model.DownloadAssetPreflightStarted, model.DownloadAssetPreflightFailed,
			model.DownloadAssetPreflightStarted, model.DownloadAssetPreflightCompleted,
			model.DownloadAssetPreflightStarted, model.DownloadAssetPreflightCompleted,
		}
		if len(progress) != len(wantStages) {
			t.Fatalf("progress = %#v", progress)
		}
		for index, stage := range wantStages {
			if progress[index].Stage != stage {
				t.Fatalf("progress[%d] stage=%q want=%q", index, progress[index].Stage, stage)
			}
		}
		if progress[5].AppID != "ready" || progress[5].AppName != "Ready" || progress[5].CandidateCount != 1 {
			t.Fatalf("ready progress = %#v", progress[5])
		}
	})
	t.Run("download-asset-candidates-skips-selection-when-version-is-current", func(t *testing.T) {
		calls := map[string]int{}
		app := model.Application{ID: "current", Name: "Current", StatusManaged: model.ManagedStatus{CurrentVersion: "2.0.0"}}
		updater := &Updater{engine: engine{checker: assetCandidateChecker{
			checkerFunc: checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil }),
			choices:     map[string]model.DownloadAssetChoices{"current": {Candidates: []string{"current.dmg"}, SelectionRequired: true}},
			calls:       calls,
		}}}

		choices, failures, err := updater.DownloadAssetCandidates(context.Background(), []model.Application{app}, nil)
		if err != nil || len(choices) != 0 || len(failures) != 0 || calls[app.ID] != 0 {
			t.Fatalf("current preflight choices=%#v failures=%#v calls=%d err=%v", choices, failures, calls[app.ID], err)
		}
	})
	t.Run("download-asset-candidates-returns-cancellation-as-global-error", func(t *testing.T) {
		updater := &Updater{engine: engine{checker: assetCandidateChecker{failures: map[string]error{"cancelled": context.Canceled}}}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		choices, failures, err := updater.DownloadAssetCandidates(ctx, []model.Application{{ID: "cancelled"}, {ID: "later"}}, nil)
		if !errors.Is(err, context.Canceled) || choices != nil || failures != nil {
			t.Fatalf("cancelled preflight choices=%#v failures=%#v err=%v", choices, failures, err)
		}
	})
	t.Run("engine-passes-run-option-download-asset-to-resolver", func(t *testing.T) {
		directory := t.TempDir()
		installed := filepath.Join(directory, "installed")
		if err := os.WriteFile(installed, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := model.Application{ID: "github", Name: "GitHub", Enabled: true, InstallPath: installed, UpdateMode: model.ModeDownload, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, StatusManaged: model.ManagedStatus{CurrentVersion: "1.0.0"}}
		checker := &selectedArtifactCaptureChecker{}
		worker := engine{Config: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{app}}, checker: checker}
		_, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{DownloadAssets: map[string]string{app.ID: "second.dmg"}})
		if checker.selected != "second.dmg" || len(results) != 1 || results[0].Status != model.StatusFailed {
			t.Fatalf("engine resolver selection=%q results=%#v", checker.selected, results)
		}
	})
	t.Run("download-integrity-controls-result-status", func(t *testing.T) {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte("payload")))
		for _, test := range []struct {
			name       string
			enabled    bool
			expected   string
			wantStatus string
			wantPath   bool
		}{
			{name: "verified", enabled: true, expected: digest, wantStatus: "downloaded", wantPath: true},
			{name: "disabled", wantStatus: "downloaded", wantPath: true},
			{name: "mismatch", enabled: true, expected: strings.Repeat("0", 64), wantStatus: "downloaded_unverified"},
		} {
			t.Run(test.name, func(t *testing.T) {
				dir := t.TempDir()
				installedPath := filepath.Join(dir, "installed")
				if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
					t.Fatal(err)
				}
				downloaderPath := filepath.Join(dir, "aria2c")
				script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
				if err := os.WriteFile(downloaderPath, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				app := model.Application{
					ID: "sample", Name: "Sample", InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeDownload,
					Provider: providerConfig(model.ProviderDefault, "printf '1.0.0\\n'", "", "", &model.Download{URL: "https://example.invalid/file", Filename: "file", ChecksumEnabled: test.enabled, ChecksumValue: test.expected}),
				}
				worker := engine{
					Config:  model.Config{Settings: model.Settings{Workers: 1, Downloader: testDownloaderSettings(downloaderPath, dir)}, Apps: []model.Application{app}},
					checker: checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil }),
				}
				state, results := runFixedRequest(&worker, context.Background(), configWithStatuses(worker.Config, map[string]model.ManagedStatus{"sample": {DownloadPath: "old-path"}}), RunOptions{})
				if len(results) != 1 || results[0].Status != test.wantStatus || configStatus(state, app.ID).UpdateStatus != test.wantStatus {
					t.Fatalf("unexpected download result: results=%#v state=%#v", results, configStatus(state, app.ID))
				}
				if got := configStatus(state, app.ID).DownloadPath; (got != "") != test.wantPath {
					t.Fatalf("download path = %q, wantPath=%v", got, test.wantPath)
				}
			})
		}
	})
	t.Run("download-explicit-checksum-overrides-github-artifact-digest", func(t *testing.T) {
		dir := t.TempDir()
		installedPath := filepath.Join(dir, "installed")
		if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		downloaderPath := filepath.Join(dir, "aria2c")
		script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
		if err := os.WriteFile(downloaderPath, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte("payload")))
		app := model.Application{
			ID: "sample", Name: "Sample", InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeDownload,
			Provider: providerConfig(model.ProviderGitHubRelease, "printf '1.0.0\\n'", "", "", &model.Download{
				URL: "https://example.invalid/file", Filename: "file", ChecksumEnabled: true, ChecksumValue: digest,
			}), Package: "owner/repo",
		}
		worker := engine{
			Config:  model.Config{Settings: model.Settings{Workers: 1, Downloader: testDownloaderSettings(downloaderPath, dir)}, Apps: []model.Application{app}},
			checker: artifactChecker{download: model.Download{ChecksumValue: strings.Repeat("0", 64)}},
		}
		_, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{})
		if len(results) != 1 || results[0].Status != model.StatusDownloaded {
			t.Fatalf("explicit checksum was overridden by provider digest: %#v", results)
		}
	})
	t.Run("download-resolves-dynamic-provider-artifact", func(t *testing.T) {
		dir := t.TempDir()
		installedPath := filepath.Join(dir, "installed")
		if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		downloaderPath := filepath.Join(dir, "aria2c")
		script := "#!/bin/sh\ndir=''\nout=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    -d) dir=$2; shift 2 ;;\n    -o) out=$2; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nmkdir -p \"$dir\"\nprintf payload > \"$dir/$out\"\n"
		if err := os.WriteFile(downloaderPath, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		app := model.Application{
			ID: "sparkle", Name: "Sparkle", InstallPath: installedPath, Enabled: true, UpdateMode: model.ModeDownload,
			Provider: providerConfig(model.ProviderSparkle, "printf '1.0.0\\n'", "", "", &model.Download{URL: "https://example.invalid/app.zip", Filename: "app.zip"}),
		}
		log, err := newLogger(filepath.Join(dir, "logs"))
		if err != nil {
			t.Fatal(err)
		}
		worker := engine{
			Config:  model.Config{Settings: model.Settings{Workers: 1, Downloader: testDownloaderSettings(downloaderPath, dir)}, Apps: []model.Application{app}},
			checker: artifactChecker{download: model.Download{URL: "https://example.invalid/app.zip", Filename: "app.zip"}, artifact: "app@2.0.0"}, logger: log,
		}
		state, results := runFixedRequest(&worker, context.Background(), worker.Config, RunOptions{})
		if len(results) != 1 || results[0].Status != "downloaded" || configStatus(state, app.ID).DownloadPath != filepath.Join(dir, "app.zip") {
			t.Fatalf("unexpected dynamic download result: results=%#v state=%#v", results, configStatus(state, app.ID))
		}
		runLog, err := os.ReadFile(filepath.Join(dir, "logs", runLogFile))
		if err != nil || !strings.Contains(string(runLog), `"artifact":"app@2.0.0"`) {
			t.Fatalf("download artifact was not consumed by run log: %q, %v", runLog, err)
		}
	})
	t.Run("cancelled-download-closes-live-output-without-final-result", func(t *testing.T) {
		dir := t.TempDir()
		installedPath := filepath.Join(dir, "installed")
		if err := os.WriteFile(installedPath, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		downloaderPath := filepath.Join(dir, "aria2c")
		if err := os.WriteFile(downloaderPath, []byte("#!/bin/sh\nprintf '[#abc 5MiB/10MiB(50%%) CN:1 DL:1MiB]\\r'\nsleep 5\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		app := model.Application{
			ID: "obsidian", Name: "Obsidian", InstallPath: installedPath, Enabled: true,
			UpdateMode: model.ModeDownload, Provider: providerConfig(model.ProviderDefault, "", "", "", &model.Download{URL: "https://example.invalid/app.dmg", Filename: "app.dmg"}),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var cancelAfterProgress sync.Once
		var events []string
		var outputAppID string
		worker := engine{
			Config:        model.Config{Settings: model.Settings{Workers: 1, Downloader: testDownloaderSettings(downloaderPath, dir)}, Apps: []model.Application{app}},
			checker:       checkerFunc(func(context.Context, model.Application, string) (string, error) { return "2.0.0", nil }),
			DownloadStart: func(result model.Result) { events = append(events, result.Status) },
			DownloadProgress: func(progress model.DownloadProgress) {
				events = append(events, fmt.Sprintf("%s:%s:%d", progress.AppID, progress.Name, progress.Percent))
				cancelAfterProgress.Do(cancel)
			},
			DownloadOutput: func(app model.Application) (io.WriteCloser, io.WriteCloser) {
				outputAppID = app.ID
				return testDownloadWriteCloser{Writer: io.Discard, close: func() { events = append(events, "finished") }}, testDownloadWriteCloser{Writer: io.Discard}
			},
			Output: func(model.Result) { events = append(events, "result") },
		}
		_, results := runFixedRequest(&worker, ctx, configWithStatuses(worker.Config, map[string]model.ManagedStatus{"obsidian": {CurrentVersion: "1.0.0"}}), RunOptions{})
		if len(results) != 1 || results[0].Status != "failed" {
			t.Fatalf("unexpected results: %#v", results)
		}
		if strings.Join(events, ",") != "downloading,obsidian:Obsidian:50,finished" || outputAppID != "obsidian" {
			t.Fatalf("unexpected live output events: %#v app=%q", events, outputAppID)
		}
	})
}
