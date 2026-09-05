package ui

import (
	"time"

	"bytes"
	"errors"
	"fmt"
	"github.com/eoctet/tendkit/internal/model"

	"context"
	"github.com/eoctet/tendkit/pkg/i18n"

	"slices"
	"strings"
	"testing"
)

func TestTUIUpdateFlow(t *testing.T) {
	t.Run("tui-run-events-update-queue-and-state", func(t *testing.T) {
		useLanguage(t, i18n.Chinese)
		view := sampleTUIView()
		view.activeRunIDs = map[string]bool{"obsidian": true}
		events := make(chan tuiEvent, 2)
		actions := TUIActions{}
		started := model.Result{AppID: "obsidian", Name: "Obsidian", Status: "checking"}
		handleTUIEvent(context.Background(), &view, tuiEvent{eventType: "app_start", result: started}, actions, events)
		if !view.rightQueue || view.queue["obsidian"].Status != "checking" {
			t.Fatalf("queue event not applied: %#v", view.queue)
		}
		updating := started
		updating.Status = model.StatusUpdating
		handleTUIEvent(context.Background(), &view, tuiEvent{eventType: tuiEventUpdateStart, result: updating}, actions, events)
		if view.queue["obsidian"].Status != model.StatusUpdating {
			t.Fatalf("updating event not applied: %#v", view.queue)
		}
		finished := started
		finished.Status = "current"
		finished.State.CurrentVersion = "1.13.7"
		handleTUIEvent(context.Background(), &view, tuiEvent{eventType: "result", logLevel: LogInfo, result: finished}, actions, events)
		if _, exists := view.queue["obsidian"]; exists || view.activeRunIDs["obsidian"] || view.catalog.Apps[0].StatusManaged.CurrentVersion != "1.13.7" {
			t.Fatalf("result event not applied: queue=%#v state=%#v", view.queue, view.state)
		}
	})
	t.Run("tuidownload-progress-updates-execution-queue", func(t *testing.T) {
		useLanguage(t, i18n.Chinese)
		t.Setenv("NO_COLOR", "1")
		view := sampleTUIView()
		view.width, view.height = 100, 30
		view.running = true
		view.rightQueue = true
		view.queue = map[string]model.Result{
			"obsidian": {AppID: "obsidian", Name: "Obsidian", Status: model.StatusDownloading},
		}
		view.queueOrder = []string{"obsidian"}
		events := make(chan tuiEvent, 1)

		handleTUIEvent(context.Background(), &view, tuiEvent{
			eventType: tuiEventDownloadProgress,
			progress:  model.DownloadProgress{AppID: "obsidian", Name: "Obsidian", Percent: 42},
		}, TUIActions{}, events)

		if got := view.downloadProgress["obsidian"]; got != 42 {
			t.Fatalf("download progress = %d, want 42", got)
		}
		var output bytes.Buffer
		renderTUI(&output, &view)
		rendered := stripTUIANSI(output.String())
		if !strings.Contains(rendered, "42%") || !strings.Contains(rendered, "[") || !strings.Contains(rendered, "#") {
			t.Fatalf("execution queue did not render progress bar:\n%s", rendered)
		}

		finished := model.Result{AppID: "obsidian", Name: "Obsidian", Status: model.StatusDownloaded}
		handleTUIEvent(context.Background(), &view, tuiEvent{eventType: tuiEventResult, logLevel: LogInfo, result: finished}, TUIActions{}, events)
		if _, exists := view.downloadProgress["obsidian"]; exists {
			t.Fatal("terminal result retained stale download progress")
		}
	})
	t.Run("tuidownload-start-renders-zero-progress-before-first-progress-event", func(t *testing.T) {
		useLanguage(t, i18n.Chinese)
		t.Setenv("NO_COLOR", "1")
		view := sampleTUIView()
		view.width, view.height = 100, 30
		view.running = true
		view.rightQueue = true
		view.queue = map[string]model.Result{
			"obsidian": {AppID: "obsidian", Name: "Obsidian", Status: model.StatusDownloading},
		}
		view.queueOrder = []string{"obsidian"}

		var output bytes.Buffer
		renderTUI(&output, &view)
		rendered := stripTUIANSI(output.String())
		if !strings.Contains(rendered, "0%") || !strings.Contains(rendered, "[") || !strings.Contains(rendered, "-") {
			t.Fatalf("downloading task did not render zero progress before the first event:\n%s", rendered)
		}
	})
	t.Run("tui-run-observer-forwards-download-progress-event", func(t *testing.T) {
		view := sampleTUIView()
		events := make(chan tuiEvent, 4)
		var observer TUIObserver
		release := make(chan struct{})
		defer close(release)
		actions := TUIActions{StartRun: func(_ context.Context, _ TUIRunRequest, candidate TUIObserver) (*TUIRunBatch, error) {
			observer = candidate
			return &TUIRunBatch{WaitResult: func() (model.Config, []model.Result, error) {
				<-release
				return view.catalog, nil, nil
			}}, nil
		}}

		startTUIRun(context.Background(), &view, false, false, actions, events)
		if observer.DownloadProgress == nil {
			t.Fatal("run observer did not expose download progress")
		}
		want := model.DownloadProgress{AppID: "obsidian", Name: "Obsidian", Percent: 37}
		observer.DownloadProgress(want)
		select {
		case event := <-events:
			if event.eventType != tuiEventDownloadProgress || event.progress != want {
				t.Fatalf("forwarded event = %#v, want %#v", event, want)
			}
		case <-time.After(time.Second):
			t.Fatal("download progress event was not forwarded")
		}
	})
	t.Run("tui-run-observer-selects-standard-level-for-each-result-event", func(t *testing.T) {
		view := sampleTUIView()
		events := make(chan tuiEvent, 4)
		var observer TUIObserver
		release := make(chan struct{})
		defer close(release)
		actions := TUIActions{StartRun: func(_ context.Context, _ TUIRunRequest, candidate TUIObserver) (*TUIRunBatch, error) {
			observer = candidate
			return &TUIRunBatch{WaitResult: func() (model.Config, []model.Result, error) {
				<-release
				return view.catalog, nil, nil
			}}, nil
		}}

		startTUIRun(context.Background(), &view, false, false, actions, events)
		for _, test := range []struct {
			status string
			level  LogLevel
		}{
			{model.StatusFailed, LogError},
			{model.StatusSkipped, LogWarn},
			{model.StatusChecking, LogDebug},
			{model.StatusCurrent, LogInfo},
		} {
			observer.Result(model.Result{Status: test.status})
			event := <-events
			if event.eventType != tuiEventResult || event.logLevel != test.level {
				t.Fatalf("status %q event = %#v, want level %q", test.status, event, test.level)
			}
		}
	})
	t.Run("start-tui-run-can-check-all-apps", func(t *testing.T) {
		view := sampleTUIView()
		requests := make(chan TUIRunRequest, 1)
		events := make(chan tuiEvent, 4)
		actions := TUIActions{StartRun: func(_ context.Context, request TUIRunRequest, _ TUIObserver) (*TUIRunBatch, error) {
			requests <- request
			return &TUIRunBatch{
				AddRequest: func(TUIRunRequest) error { return nil },
				WaitResult: func() (model.Config, []model.Result, error) { return view.catalog, nil, nil },
			}, nil
		}}
		startTUIRun(context.Background(), &view, true, true, actions, events)
		request := <-requests
		if !request.CheckOnly || request.Names != nil || !request.AllRequested {
			t.Fatalf("request = %#v", request)
		}
		if view.queue["obsidian"].Status != "waiting" {
			t.Fatalf("all-apps run did not create waiting queue entries: %#v", view.queue)
		}
	})
	t.Run("preprocess-progress-is-streamed-to-live-log", func(t *testing.T) {
		useLanguage(t, i18n.Chinese)
		view := sampleTUIView()
		view.operationText = func(model.Config, string, string, string, string) ([]string, error) {
			t.Fatal("Homebrew lifecycle feedback must bypass persistent log filtering")
			return nil, nil
		}
		events := make(chan tuiEvent, 8)
		release := make(chan struct{})
		var observer TUIObserver
		actions := TUIActions{StartRun: func(_ context.Context, _ TUIRunRequest, current TUIObserver) (*TUIRunBatch, error) {
			observer = current
			return &TUIRunBatch{
				AddRequest: func(TUIRunRequest) error { return nil },
				WaitResult: func() (model.Config, []model.Result, error) {
					<-release
					return view.catalog, nil, nil
				},
			}, nil
		}}
		startTUIRun(context.Background(), &view, true, true, actions, events)
		for _, item := range []struct {
			progress        model.PreprocessProgress
			level           LogLevel
			expectedSubject string
		}{
			{progress: model.PreprocessProgress{Action: model.PreprocessActionHomebrew, Subject: "Homebrew", Status: model.StatusStarted}, level: LogInfo, expectedSubject: "Homebrew"},
			{progress: model.PreprocessProgress{Action: model.PreprocessActionHomebrew, Subject: "Homebrew", Status: model.StatusSuccess}, level: LogInfo, expectedSubject: "Homebrew"},
			{progress: model.PreprocessProgress{Action: model.PreprocessActionHomebrew, Subject: "Homebrew", Status: model.StatusSkipped}, level: LogWarn, expectedSubject: "Homebrew"},
			{progress: model.PreprocessProgress{Action: model.PreprocessActionHomebrew, Subject: "Homebrew", Status: model.StatusCancelled}, level: LogWarn, expectedSubject: "Homebrew"},
			{progress: model.PreprocessProgress{Action: model.PreprocessActionHomebrew, Subject: "Homebrew", Status: model.StatusFailed}, level: LogError, expectedSubject: "Homebrew"},
			{progress: model.PreprocessProgress{Action: "custom-cache", Status: model.StatusStarted}, level: LogInfo, expectedSubject: "custom-cache"},
		} {
			observer.PreprocessProgress(item.progress)
			event := <-events
			if event.eventType != tuiEventLog || !strings.Contains(event.text, item.expectedSubject) || !strings.Contains(event.text, string(item.level)) {
				t.Fatalf("event=%#v", event)
			}
			handleTUIEvent(context.Background(), &view, event, actions, events)
		}
		logs := strings.Join(view.logs, "\n")
		for _, expected := range []string{"开始预处理：Homebrew", "预处理完成：Homebrew", "已跳过预处理：Homebrew", "预处理已取消：Homebrew", "预处理失败：Homebrew"} {
			if !strings.Contains(logs, expected) {
				t.Fatalf("missing %q in logs: %s", expected, logs)
			}
		}
		if !strings.Contains(logs, "custom-cache") || !strings.Contains(logs, "预处理") {
			t.Fatalf("missing generic preprocess fallback in logs: %s", logs)
		}
		close(release)
		select {
		case event := <-events:
			if event.eventType != tuiEventRunDone {
				t.Fatalf("event=%#v", event)
			}
		case <-time.After(time.Second):
			t.Fatal("run goroutine did not finish")
		}
	})
	t.Run("start-tui-run-failure-rolls-back-waiting-queue", func(t *testing.T) {
		view := sampleTUIView()
		actions := TUIActions{StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) {
			return nil, errors.New("start failed")
		}}

		startTUIRun(context.Background(), &view, true, true, actions, make(chan tuiEvent, 1))

		if view.running || view.cancel != nil || view.batch != nil || len(view.activeRunIDs) != 0 || len(view.queue) != 0 {
			t.Fatalf("failed run retained active state: running=%t active=%#v queue=%#v", view.running, view.activeRunIDs, view.queue)
		}
		if !view.messageError || !strings.Contains(view.message, "start failed") {
			t.Fatalf("failed run did not report its error: %q", view.message)
		}
	})
	t.Run("tui-adds-another-application-to-active-batch", func(t *testing.T) {
		view := sampleTUIView()
		second := model.Application{ID: "git", Name: "Git", Type: "cli", InstallPath: "git", Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}}
		view.catalog.Apps = append(view.catalog.Apps, second)
		view.working = cloneConfig(view.catalog)
		view.catalog.Apps[1].StatusManaged = model.ManagedStatus{CurrentVersion: "2.50.1", UpdateStatus: "current"}
		view.running = true
		view.activeRunIDs = map[string]bool{"obsidian": true}
		view.queue = map[string]model.Result{"obsidian": {AppID: "obsidian", Name: "Obsidian", Status: "downloading"}}
		view.queueOrder = []string{"obsidian"}
		view.selected = 1

		requests := make(chan TUIRunRequest, 1)
		events := make(chan tuiEvent, 4)
		view.batch = &TUIRunBatch{
			AddRequest: func(request TUIRunRequest) error { requests <- request; return nil },
			WaitResult: func() (model.Config, []model.Result, error) { return view.catalog, nil, nil },
		}
		actions := TUIActions{}
		startTUIRun(context.Background(), &view, true, false, actions, events)
		if view.queue[second.ID].Status != "waiting" || !view.activeRunIDs[second.ID] {
			t.Fatalf("second application was not added to the active batch: active=%#v queue=%#v", view.activeRunIDs, view.queue)
		}
		if !strings.Contains(view.message, "Git") {
			t.Fatalf("batch confirmation missing application name: %q", view.message)
		}
		view.width, view.height = 120, 30
		var rendered bytes.Buffer
		renderTUI(&rendered, &view)
		if !strings.Contains(stripTUIANSI(rendered.String()), i18n.T("tui.workers_badge", 2, 4)) {
			t.Fatal("header did not show the remaining worker capacity")
		}
		request := <-requests
		if !request.CheckOnly || len(request.Names) != 1 || request.Names[0] != second.ID {
			t.Fatalf("batch addition = %#v", request)
		}
	})
	t.Run("tui-adds-all-applications-when-only-part-of-worker-pool-is-occupied", func(t *testing.T) {
		view := sampleTUIView()
		view.catalog.Settings.Workers = 10
		for index := 0; index < 11; index++ {
			view.catalog.Apps = append(view.catalog.Apps, model.Application{
				ID: fmt.Sprintf("app-%d", index), Name: fmt.Sprintf("App %d", index), Type: "cli", UpdateMode: model.ModeCheck,
			})
		}
		view.working = cloneConfig(view.catalog)
		view.running = true
		view.activeRunIDs = map[string]bool{"obsidian": true}
		view.queue = map[string]model.Result{"obsidian": {AppID: "obsidian", Name: "Obsidian", Status: "downloading"}}
		view.queueOrder = []string{"obsidian"}
		requests := make(chan TUIRunRequest, 1)
		view.batch = &TUIRunBatch{AddRequest: func(request TUIRunRequest) error {
			requests <- request
			return nil
		}}

		startTUIRun(context.Background(), &view, true, true, TUIActions{}, make(chan tuiEvent, 1))
		request := <-requests
		if !request.CheckOnly || len(request.Names) != 11 || slices.Contains(request.Names, "obsidian") {
			t.Fatalf("all-applications request = %#v", request)
		}
		if len(view.queue) != 12 {
			t.Fatalf("all applications did not join the shared queue: %d", len(view.queue))
		}
		if strings.Contains(view.message, "容量不足") {
			t.Fatalf("partially occupied worker pool rejected a bulk request: %q", view.message)
		}
	})
	t.Run("tui-completed-application-can-rejoin-active-queue", func(t *testing.T) {
		view := sampleTUIView()
		blocker := model.Application{ID: "blocker", Name: "Blocker", Type: "cli", UpdateMode: model.ModeCheck}
		view.catalog.Apps = append(view.catalog.Apps, blocker)
		view.running = true
		view.activeRunIDs = map[string]bool{"obsidian": true, blocker.ID: true}
		view.queue = map[string]model.Result{
			"obsidian": {AppID: "obsidian", Name: "Obsidian", Status: "checking"},
			blocker.ID: {AppID: blocker.ID, Name: blocker.Name, Status: "checking"},
		}
		requests := make(chan TUIRunRequest, 1)
		view.batch = &TUIRunBatch{AddRequest: func(request TUIRunRequest) error {
			requests <- request
			return nil
		}}

		completed := model.Result{AppID: "obsidian", Name: "Obsidian", Status: "current", State: view.catalog.Apps[0].StatusManaged}
		handleTUIEvent(context.Background(), &view, tuiEvent{eventType: "result", logLevel: LogInfo, result: completed}, TUIActions{}, make(chan tuiEvent, 1))
		startTUIRun(context.Background(), &view, true, false, TUIActions{}, make(chan tuiEvent, 1))
		request := <-requests
		if len(request.Names) != 1 || request.Names[0] != "obsidian" || !request.CheckOnly {
			t.Fatalf("repeated request = %#v", request)
		}
		if view.queue["obsidian"].Status != "waiting" || !view.activeRunIDs["obsidian"] {
			t.Fatalf("completed application did not rejoin the queue: active=%#v queue=%#v", view.activeRunIDs, view.queue)
		}
	})
	t.Run("tui-rejects-addition-when-worker-pool-is-full", func(t *testing.T) {
		view := sampleTUIView()
		second := model.Application{ID: "git", Name: "Git", Type: "cli", UpdateMode: model.ModeCheck}
		view.catalog.Apps = append(view.catalog.Apps, second)
		view.catalog.Settings.Workers = 1
		view.running = true
		view.queue["obsidian"] = model.Result{AppID: "obsidian", Name: "Obsidian", Status: "downloading"}
		view.activeRunIDs = map[string]bool{"obsidian": true}
		view.selected = 1
		added := false
		view.batch = &TUIRunBatch{AddRequest: func(TUIRunRequest) error { added = true; return nil }}

		startTUIRun(context.Background(), &view, true, false, TUIActions{}, make(chan tuiEvent, 1))
		if added {
			t.Fatal("full worker pool accepted another application")
		}
		if _, exists := view.queue[second.ID]; exists {
			t.Fatalf("rejected application appeared in queue: %#v", view.queue)
		}
		if !strings.Contains(view.message, "1/1") {
			t.Fatalf("worker-pool capacity missing from message: %q", view.message)
		}
	})
}
func TestTUIDownloadPreflightFlow(t *testing.T) {
	t.Run("tuidownload-preflight-logs-before-slow-request-completes", func(t *testing.T) {
		useLanguage(t, i18n.English)
		view := tuiModel{appsPageState: appsPageState{catalog: model.Config{
			Settings: model.Settings{Workers: 1},
			Apps:     []model.Application{{ID: "app", Name: "App", Enabled: true, UpdateMode: model.ModeDownload}},
		}}}
		started := make(chan struct{})
		release := make(chan struct{})
		actions := TUIActions{DownloadAssetCandidates: func(_ context.Context, _ TUIRunRequest, observer TUIDownloadAssetObserver) (map[string]model.DownloadAssetChoices, map[string]error, error) {
			observer.Progress(model.DownloadAssetPreflightProgress{AppID: "app", AppName: "App", Stage: model.DownloadAssetPreflightStarted})
			close(started)
			<-release
			observer.Progress(model.DownloadAssetPreflightProgress{AppID: "app", AppName: "App", Stage: model.DownloadAssetPreflightCompleted, CandidateCount: 2})
			return map[string]model.DownloadAssetChoices{}, map[string]error{}, nil
		}}
		events := make(chan tuiEvent, 4)

		startTUIRun(context.Background(), &view, false, false, actions, events)
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("download preflight did not start")
		}
		handleTUIEvent(context.Background(), &view, <-events, actions, events)
		logs := strings.Join(view.logs, "\n")
		for _, expected := range []string{i18n.T("tui.download_preflight_started"), "App", i18n.T("tui.download_preflight_app_started")} {
			if !strings.Contains(logs, expected) {
				t.Fatalf("preflight did not log %q before request completed: %q", expected, logs)
			}
		}
		close(release)
		handleTUIEvent(context.Background(), &view, <-events, actions, events)
		if logs := strings.Join(view.logs, "\n"); !strings.Contains(logs, i18n.T("tui.download_preflight_app_completed", 2)) {
			t.Fatalf("preflight completion progress missing: %q", logs)
		}
		<-events
	})
	t.Run("tui-asset-preflight-fails-closed-for-zero-autoselects-one-and-prompts-many", func(t *testing.T) {
		newView := func() tuiModel {
			return tuiModel{appsPageState: appsPageState{catalog: model.Config{Settings: model.Settings{Workers: 1}, Apps: []model.Application{{ID: "app", Name: "App", Enabled: true, UpdateMode: model.ModeDownload}}}}}
		}
		for _, test := range []struct {
			name              string
			choices           []string
			selectionRequired bool
			starts            int
			modal             bool
		}{
			{name: "zero", choices: []string{}},
			{name: "one inferred", choices: []string{"App.dmg"}, starts: 1},
			{name: "one fallback", choices: []string{"opaque.payload"}, selectionRequired: true, modal: true},
			{name: "many", choices: []string{"App-arm64.dmg", "App-x64.dmg"}, modal: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				view, starts := newView(), 0
				actions := TUIActions{
					DownloadAssetCandidates: func(context.Context, TUIRunRequest, TUIDownloadAssetObserver) (map[string]model.DownloadAssetChoices, map[string]error, error) {
						return map[string]model.DownloadAssetChoices{"app": {Candidates: test.choices, SelectionRequired: test.selectionRequired}}, nil, nil
					},
					StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) { starts++; return nil, nil },
				}
				events := make(chan tuiEvent, 1)
				startTUIRun(context.Background(), &view, false, false, actions, events)
				event := <-events
				handleDownloadAssetEvent(context.Background(), &view, event, actions, events)
				if starts != test.starts || (view.assetSelection != nil) != test.modal {
					t.Fatalf("starts=%d selection=%#v", starts, view.assetSelection)
				}
			})
		}
	})
	t.Run("tui-asset-preflight-drops-only-rejected-applications", func(t *testing.T) {
		apps := []model.Application{
			{ID: "failed", Name: "Failed", Enabled: true, UpdateMode: model.ModeDownload},
			{ID: "empty", Name: "Empty", Enabled: true, UpdateMode: model.ModeDownload},
			{ID: "ready", Name: "Ready", Enabled: true, UpdateMode: model.ModeDownload},
		}
		view := tuiModel{
			appsPageState: appsPageState{catalog: model.Config{Settings: model.Settings{Workers: 2}, Apps: apps}},
			runState:      runState{queue: map[string]model.Result{}},
		}
		requests := make(chan TUIRunRequest, 1)
		actions := TUIActions{
			DownloadAssetCandidates: func(context.Context, TUIRunRequest, TUIDownloadAssetObserver) (map[string]model.DownloadAssetChoices, map[string]error, error) {
				return map[string]model.DownloadAssetChoices{"empty": {Candidates: []string{}}, "ready": {Candidates: []string{"ready.dmg"}}}, map[string]error{"failed": errors.New("provider failed")}, nil
			},
			StartRun: func(_ context.Context, request TUIRunRequest, _ TUIObserver) (*TUIRunBatch, error) {
				requests <- request
				return &TUIRunBatch{WaitResult: func() (model.Config, []model.Result, error) { return view.catalog, nil, nil }}, nil
			},
		}
		events := make(chan tuiEvent, 4)

		startTUIRun(context.Background(), &view, false, true, actions, events)
		handleDownloadAssetEvent(context.Background(), &view, <-events, actions, events)
		request := <-requests

		if len(request.Names) != 1 || request.Names[0] != "ready" || request.DownloadAssets["ready"] != "ready.dmg" || !request.AllRequested {
			t.Fatalf("partial preflight request = %#v", request)
		}
		if len(view.queue) != 1 || view.queue["ready"].Status != model.StatusWaiting {
			t.Fatalf("rejected applications remained queued: %#v", view.queue)
		}
		logs := strings.Join(view.logs, "\n")
		for _, expected := range []string{
			i18n.T("tui.download_preflight_started"), i18n.T("tui.download_preflight_completed"),
			"Failed", "provider failed", "Empty", i18n.T("tui.download_file_empty"),
		} {
			if !strings.Contains(logs, expected) {
				t.Fatalf("rejection log missing %q: %s", expected, logs)
			}
		}
	})
	t.Run("tui-asset-preflight-does-not-start-when-every-application-is-rejected", func(t *testing.T) {
		apps := []model.Application{{ID: "failed", Name: "Failed"}, {ID: "empty", Name: "Empty"}}
		view := tuiModel{
			appsPageState: appsPageState{catalog: model.Config{Settings: model.Settings{Workers: 2}, Apps: apps}},
			runState:      runState{queue: map[string]model.Result{}},
		}
		starts := 0
		actions := TUIActions{
			DownloadAssetCandidates: func(context.Context, TUIRunRequest, TUIDownloadAssetObserver) (map[string]model.DownloadAssetChoices, map[string]error, error) {
				return map[string]model.DownloadAssetChoices{"empty": {Candidates: []string{}}}, map[string]error{"failed": errors.New("provider failed")}, nil
			},
			StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) {
				starts++
				return nil, nil
			},
		}
		events := make(chan tuiEvent, 1)

		startTUIRun(context.Background(), &view, false, true, actions, events)
		handleDownloadAssetEvent(context.Background(), &view, <-events, actions, events)

		if starts != 0 || view.running || len(view.queue) != 0 || !view.messageError || !strings.Contains(view.message, i18n.T("tui.download_file_none_remaining")) {
			t.Fatalf("fully rejected preflight started=%d running=%t queue=%#v message=%q", starts, view.running, view.queue, view.message)
		}
	})
	t.Run("tui-asset-preflight-adds-remaining-applications-to-active-batch", func(t *testing.T) {
		apps := []model.Application{
			{ID: "running", Name: "Running"},
			{ID: "failed", Name: "Failed"},
			{ID: "ready", Name: "Ready"},
		}
		requests := make(chan TUIRunRequest, 1)
		view := tuiModel{
			appsPageState: appsPageState{catalog: model.Config{Settings: model.Settings{Workers: 2}, Apps: apps}},
			runState: runState{
				running:      true,
				queue:        map[string]model.Result{"running": {AppID: "running", Status: model.StatusChecking}},
				activeRunIDs: map[string]bool{"running": true},
				batch: &TUIRunBatch{AddRequest: func(request TUIRunRequest) error {
					requests <- request
					return nil
				}},
			},
		}
		actions := TUIActions{DownloadAssetCandidates: func(context.Context, TUIRunRequest, TUIDownloadAssetObserver) (map[string]model.DownloadAssetChoices, map[string]error, error) {
			return map[string]model.DownloadAssetChoices{"ready": {Candidates: []string{"ready.dmg"}}}, map[string]error{"failed": errors.New("provider failed")}, nil
		}}
		events := make(chan tuiEvent, 1)

		startTUIRun(context.Background(), &view, false, true, actions, events)
		handleDownloadAssetEvent(context.Background(), &view, <-events, actions, events)
		request := <-requests

		if len(request.Names) != 1 || request.Names[0] != "ready" || request.DownloadAssets["ready"] != "ready.dmg" {
			t.Fatalf("active batch request = %#v", request)
		}
		if !view.activeRunIDs["running"] || !view.activeRunIDs["ready"] || view.activeRunIDs["failed"] {
			t.Fatalf("active IDs after partial add = %#v", view.activeRunIDs)
		}
		if _, exists := view.queue["failed"]; exists {
			t.Fatalf("failed application joined active queue: %#v", view.queue)
		}
	})
	t.Run("download-asset-modal-render-uses-shared-dialog-frame-and-viewport", func(t *testing.T) {
		for _, language := range []i18n.Language{"en", "zh"} {
			t.Run(string(language), func(t *testing.T) {
				i18n.Set(language)
				items := make([]string, 30)
				for index := range items {
					items[index] = "超长-asset-name-with-wide-text-" + string(rune('A'+index%26)) + ".dmg"
				}
				view := tuiModel{assetSelection: &tuiAssetSelection{candidates: items, selected: 10, offset: 10}, viewportState: viewportState{width: 80, height: 24}}
				asset := newTUIScreen(80, 24)
				renderDownloadAssetSelection(asset, &view)
				confirmation := newTUIScreen(80, 24)
				renderConfirmationDialog(confirmation, &view, "Title", "A shared dialog prompt", tuiCyan, i18n.T("tui.confirm"), i18n.T("tui.cancel"))
				if !strings.Contains(asset.string(), "ENTER") || !strings.Contains(asset.string(), "ESC") || !strings.Contains(asset.string(), "asset-name") {
					t.Fatalf("asset modal missing shared controls or selected item: %q", asset.string())
				}
				if strings.Count(asset.string(), "│") < 2 || strings.Count(confirmation.string(), "│") < 2 {
					t.Fatal("shared dialog border missing")
				}
			})
		}
	})
	t.Run("tui-asset-selection-uses-confirmation-buttons-and-dialog-keymap", func(t *testing.T) {
		useLanguage(t, i18n.English)
		newView := func() tuiModel {
			return tuiModel{
				assetSelection: &tuiAssetSelection{
					spec:  tuiRunSpec{request: TUIRunRequest{Names: []string{"app"}}},
					appID: "app", candidates: []string{"opaque.payload"},
				},
				viewportState: viewportState{width: 120, height: 36},
			}
		}
		starts := 0
		actions := TUIActions{StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) {
			starts++
			return nil, nil
		}}

		cancelled := newView()
		handleTUIAssetSelection(context.Background(), &cancelled, "right", actions, make(chan tuiEvent, 1))
		if cancelled.confirmChoice != tuiConfirmationSecondary {
			t.Fatalf("right did not focus cancel: %#v", cancelled)
		}
		handleTUIAssetSelection(context.Background(), &cancelled, "enter", actions, make(chan tuiEvent, 1))
		if starts != 0 || cancelled.assetSelection != nil || cancelled.confirmChoice != tuiConfirmationPrimary {
			t.Fatalf("focused cancel started=%d selection=%#v choice=%d", starts, cancelled.assetSelection, cancelled.confirmChoice)
		}

		confirmed := newView()
		handleTUIAssetSelection(context.Background(), &confirmed, "right", actions, make(chan tuiEvent, 1))
		handleTUIAssetSelection(context.Background(), &confirmed, "left", actions, make(chan tuiEvent, 1))
		handleTUIAssetSelection(context.Background(), &confirmed, "enter", actions, make(chan tuiEvent, 1))
		if starts != 1 || confirmed.assetSelection != nil || confirmed.confirmChoice != tuiConfirmationPrimary {
			t.Fatalf("focused confirm started=%d selection=%#v choice=%d", starts, confirmed.assetSelection, confirmed.confirmChoice)
		}

		footerView := newView()
		keymap := tuiCurrentKeymap(&footerView)
		for _, key := range []string{"up", "down", "left", "right", "enter", "esc"} {
			if !keymap.Permits(key) {
				t.Fatalf("asset dialog keymap rejected %q", key)
			}
		}
		if keymap.Permits("f") || keymap.Permits("u") {
			t.Fatalf("asset dialog leaked application keymap: %#v", keymap)
		}
		footer := strings.Join(keymap.FooterLines(footerView.width), "\n")
		for _, expected := range []string{i18n.T("tui.key.select"), i18n.T("tui.key.confirm_select"), "ENTER " + i18n.T("tui.confirm"), "ESC " + i18n.T("tui.cancel")} {
			if !strings.Contains(footer, expected) {
				t.Fatalf("asset dialog footer missing %q: %q", expected, footer)
			}
		}
	})
	t.Run("tui-asset-selection-cancel-and-illegal-choice-do-not-start-run", func(t *testing.T) {
		view := tuiModel{assetSelection: &tuiAssetSelection{spec: tuiRunSpec{request: TUIRunRequest{Names: []string{"app"}}}, appID: "app", candidates: []string{"a.dmg"}, selected: 7}}
		starts := 0
		actions := TUIActions{StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) { starts++; return nil, nil }}
		handleTUIAssetSelection(context.Background(), &view, "enter", actions, make(chan tuiEvent, 1))
		if starts != 0 || view.assetSelection == nil {
			t.Fatalf("illegal selection started=%d state=%#v", starts, view.assetSelection)
		}
		handleTUIAssetSelection(context.Background(), &view, "esc", actions, make(chan tuiEvent, 1))
		if starts != 0 || view.assetSelection != nil {
			t.Fatalf("cancel started=%d state=%#v", starts, view.assetSelection)
		}
	})
	t.Run("tui-asset-selection-adds-to-active-batch-instead-of-launching-second-run", func(t *testing.T) {
		added, starts := 0, 0
		view := tuiModel{
			appsPageState: appsPageState{catalog: model.Config{Settings: model.Settings{Workers: 2}}},
			runState: runState{running: true, queue: map[string]model.Result{}, activeRunIDs: map[string]bool{}, batch: &TUIRunBatch{AddRequest: func(request TUIRunRequest) error {
				added++
				if request.DownloadAssets["next"] != "next-arm64.dmg" {
					t.Fatalf("asset request = %#v", request)
				}
				return nil
			}}},
			assetSelection: &tuiAssetSelection{spec: tuiRunSpec{target: "Next", apps: []model.Application{{ID: "next", Name: "Next"}}}, appID: "next", candidates: []string{"next-arm64.dmg"}},
		}
		actions := TUIActions{StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) { starts++; return nil, nil }}
		handleTUIAssetSelection(context.Background(), &view, "enter", actions, make(chan tuiEvent, 1))
		if starts != 0 || added != 1 || !view.activeRunIDs["next"] || view.assetSelection != nil {
			t.Fatalf("starts=%d added=%d active=%v selection=%#v", starts, added, view.activeRunIDs, view.assetSelection)
		}
	})
	t.Run("tui-asset-selection-keyboard-scrolls-and-resets-for-next-modal", func(t *testing.T) {
		items := make([]string, 30)
		for index := range items {
			items[index] = "asset-" + string(rune('a'+index%26)) + ".dmg"
		}
		view := tuiModel{
			viewportState: viewportState{height: 16},
			assetSelection: &tuiAssetSelection{
				spec:       tuiRunSpec{request: TUIRunRequest{Names: []string{"first", "second"}}},
				appID:      "first",
				candidates: items,
				remaining:  map[string][]string{"second": {"second.dmg"}},
			},
		}
		actions := TUIActions{StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) { return nil, nil }}
		events := make(chan tuiEvent, 1)
		for index := 0; index < len(items)-1; index++ {
			handleTUIAssetSelection(context.Background(), &view, "down", actions, events)
		}
		selection := view.assetSelection
		if selection.selected != len(items)-1 || selection.offset == 0 {
			t.Fatalf("end selection = %#v", selection)
		}
		for index := 0; index < 10; index++ {
			handleTUIAssetSelection(context.Background(), &view, "up", actions, events)
		}
		if selection.selected != len(items)-11 {
			t.Fatalf("middle selection = %#v", selection)
		}
		for selection.selected > 0 {
			handleTUIAssetSelection(context.Background(), &view, "up", actions, events)
		}
		if selection.offset != 0 {
			t.Fatalf("first selection offset = %d", selection.offset)
		}
		for index := 0; index < len(items)-1; index++ {
			handleTUIAssetSelection(context.Background(), &view, "down", actions, events)
		}
		handleTUIAssetSelection(context.Background(), &view, "enter", actions, events)
		selection = view.assetSelection
		if selection.appID != "second" || selection.selected != 0 || selection.offset != 0 || len(selection.candidates) != 1 {
			t.Fatalf("next modal was not reset: %#v", selection)
		}
	})
}
