package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

func TestTUIDownloadPreflightLogsBeforeSlowRequestCompletes(t *testing.T) {
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
}

func TestTUIAssetPreflightFailsClosedForZeroAutoSelectsOneAndPromptsMany(t *testing.T) {
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
}

func TestTUIAssetPreflightDropsOnlyRejectedApplications(t *testing.T) {
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

	if len(request.Names) != 1 || request.Names[0] != "ready" || request.DownloadAssets["ready"] != "ready.dmg" {
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
}

func TestTUIAssetPreflightDoesNotStartWhenEveryApplicationIsRejected(t *testing.T) {
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
}

func TestTUIAssetPreflightAddsRemainingApplicationsToActiveBatch(t *testing.T) {
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
}

func TestDownloadAssetModalRenderUsesSharedDialogFrameAndViewport(t *testing.T) {
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
}

func TestTUIAssetSelectionUsesConfirmationButtonsAndDialogKeymap(t *testing.T) {
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
}

func TestTUIAssetSelectionCancelAndIllegalChoiceDoNotStartRun(t *testing.T) {
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
}

func TestTUIAssetSelectionAddsToActiveBatchInsteadOfLaunchingSecondRun(t *testing.T) {
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
}

func TestTUIAssetSelectionKeyboardScrollsAndResetsForNextModal(t *testing.T) {
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
}
