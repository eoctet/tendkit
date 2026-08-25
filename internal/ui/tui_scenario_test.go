package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

type tuiScenario struct {
	name        string
	setup       func(*tuiModel)
	keys        []string
	events      func(*tuiModel) []tuiEvent
	afterEvent  func(*testing.T, *tuiModel, tuiEvent)
	waitFor     func(*tuiModel) bool
	wantActions tuiActionCounts
	assert      func(*testing.T, *tuiModel, *tuiActionSpy, string)
}

type tuiActionCounts struct {
	saveConfig int
	saveScan   int
	startRun   int
	scan       int
}

type tuiActionSpy struct {
	counts       tuiActionCounts
	savedConfig  model.Config
	savedScan    model.Config
	runRequests  []TUIRunRequest
	scanRequests []TUIScanRequest
}

func newTUIActionSpy(view *tuiModel) (*tuiActionSpy, TUIActions) {
	spy := &tuiActionSpy{}
	initialCatalog := cloneConfig(view.catalog)
	initialState := cloneTUIState(view.state)
	actions := TUIActions{
		SaveConfig: func(_, proposed model.Config) (model.Config, error) {
			spy.counts.saveConfig++
			spy.savedConfig = cloneConfig(proposed)
			return cloneConfig(proposed), nil
		},
		SaveScan: func(_, proposed model.Config) (model.Config, error) {
			spy.counts.saveScan++
			spy.savedScan = cloneConfig(proposed)
			return cloneConfig(proposed), nil
		},
		StartRun: func(_ context.Context, request TUIRunRequest, _ TUIObserver) (*TUIRunBatch, error) {
			spy.counts.startRun++
			spy.runRequests = append(spy.runRequests, request)
			return &TUIRunBatch{
				AddRequest: func(TUIRunRequest) error { return nil },
				WaitResult: func() (model.Config, []model.Result, error) {
					return cloneConfig(initialCatalog), nil, nil
				},
			}, nil
		},
		Scan: func(_ context.Context, request TUIScanRequest, _ TUIScanObserver) (TUIScanSnapshot, error) {
			spy.counts.scan++
			spy.scanRequests = append(spy.scanRequests, request)
			return TUIScanSnapshot{
				BaseConfig: cloneConfig(initialCatalog),
				BaseState:  cloneTUIState(initialState),
				Config:     cloneConfig(initialCatalog),
				State:      cloneTUIState(initialState),
			}, nil
		},
		GenerateIdentity: func(application model.Application) (string, error) {
			return application.Identity, nil
		},
	}
	return spy, actions
}

func runTUIScenario(t *testing.T, scenario tuiScenario) {
	t.Helper()
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 120, 36
	if scenario.setup != nil {
		scenario.setup(&view)
	}
	spy, actions := newTUIActionSpy(&view)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan tuiEvent, 256)
	for _, key := range scenario.keys {
		handleTUIKey(ctx, &view, key, actions, events)
	}
	if scenario.events != nil {
		for _, event := range scenario.events(&view) {
			handleTUIEvent(ctx, &view, event, actions, events)
			if scenario.afterEvent != nil {
				scenario.afterEvent(t, &view, event)
			}
		}
	}
	if scenario.waitFor != nil {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for !scenario.waitFor(&view) {
			select {
			case event := <-events:
				handleTUIEvent(ctx, &view, event, actions, events)
				if scenario.afterEvent != nil {
					scenario.afterEvent(t, &view, event)
				}
			case <-timer.C:
				t.Fatalf("timed out waiting for scenario completion: running=%v scanRunning=%v", view.running, view.scanRunning)
			}
		}
	}
	if view.running || view.scanRunning {
		t.Fatal("scenario left an asynchronous operation running; add a waitFor condition")
	}
	var output bytes.Buffer
	renderTUI(&output, &view)
	rendered := stripTUIANSI(output.String())
	assertTUISemanticScreen(t, &view, rendered)
	if spy.counts != scenario.wantActions {
		t.Fatalf("action counts = %#v, want %#v", spy.counts, scenario.wantActions)
	}
	if scenario.assert != nil {
		scenario.assert(t, &view, spy, rendered)
	}
}

func assertTUISemanticScreen(t *testing.T, view *tuiModel, rendered string) {
	t.Helper()
	if !strings.Contains(rendered, pageName(view.page)) {
		t.Fatalf("screen omits current page %q:\n%s", pageName(view.page), rendered)
	}
	footerHeight := tuiFooterHeight(view)
	screenLines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	footerStart := max(0, len(screenLines)-footerHeight)
	footer := strings.Join(screenLines[footerStart:], "\n")
	for _, line := range tuiCurrentKeymap(view).FooterLines(view.width) {
		if !strings.Contains(footer, line) {
			t.Fatalf("footer omits visible binding line %q:\n%s", line, footer)
		}
	}
}

func TestTUIScenarioPrimaryPageFlows(t *testing.T) {
	scenarios := []tuiScenario{
		{
			name:    "check action completes through asynchronous event pump",
			keys:    []string{"c"},
			waitFor: func(view *tuiModel) bool { return !view.running },
			wantActions: tuiActionCounts{
				startRun: 1,
			},
			assert: func(t *testing.T, view *tuiModel, spy *tuiActionSpy, rendered string) {
				if len(spy.runRequests) != 1 || !spy.runRequests[0].CheckOnly || len(spy.runRequests[0].Names) != 1 || spy.runRequests[0].Names[0] != "obsidian" {
					t.Fatalf("check request = %#v, want one obsidian check", spy.runRequests)
				}
				if view.running || view.batch != nil || !strings.Contains(rendered, i18n.T("tui.operation_finished", 0)) {
					t.Fatalf("asynchronous completion was not rendered: running=%v batch=%#v\n%s", view.running, view.batch, rendered)
				}
			},
		},
		{
			name: "update events update queue status and screen",
			setup: func(view *tuiModel) {
				view.activeRunIDs = map[string]bool{"obsidian": true}
			},
			events: func(*tuiModel) []tuiEvent {
				started := model.Result{AppID: "obsidian", Name: "Obsidian", Status: model.StatusChecking}
				finished := started
				finished.Status = model.StatusCurrent
				finished.State.CurrentVersion = "1.13.7"
				return []tuiEvent{
					{eventType: tuiEventAppStart, result: started},
					{eventType: tuiEventResult, logLevel: LogInfo, result: finished},
				}
			},
			afterEvent: func(t *testing.T, view *tuiModel, event tuiEvent) {
				switch event.eventType {
				case tuiEventAppStart:
					if !view.rightQueue || view.queue["obsidian"].Status != model.StatusChecking {
						t.Fatalf("app_start was not applied: rightQueue=%v queue=%#v", view.rightQueue, view.queue)
					}
				case tuiEventResult:
					if _, queued := view.queue["obsidian"]; queued {
						t.Fatalf("result did not remove completed queue entry: %#v", view.queue)
					}
				}
			},
			assert: func(t *testing.T, view *tuiModel, _ *tuiActionSpy, rendered string) {
				if _, queued := view.queue["obsidian"]; queued || view.activeRunIDs["obsidian"] {
					t.Fatalf("completed application remains scheduled: queue=%#v active=%#v", view.queue, view.activeRunIDs)
				}
				if got := view.catalog.Apps[0].StatusManaged.CurrentVersion; got != "1.13.7" {
					t.Fatalf("current version = %q, want 1.13.7", got)
				}
				if !strings.Contains(rendered, "Obsidian") {
					t.Fatalf("screen omits updated application:\n%s", rendered)
				}
			},
		},
		{
			name: "configuration save crosses action boundary",
			setup: func(view *tuiModel) {
				view.page = tuiConfig
				view.working.Settings.Workers = 7
				view.dirty = true
			},
			keys:        []string{"ctrl+s"},
			wantActions: tuiActionCounts{saveConfig: 1},
			assert: func(t *testing.T, view *tuiModel, spy *tuiActionSpy, rendered string) {
				if view.dirty || view.catalog.Settings.Workers != 7 || spy.savedConfig.Settings.Workers != 7 {
					t.Fatalf("saved configuration not applied: dirty=%v catalog=%d saved=%d", view.dirty, view.catalog.Settings.Workers, spy.savedConfig.Settings.Workers)
				}
				if !strings.Contains(rendered, i18n.T("tui.saved")) {
					t.Fatalf("screen omits save result:\n%s", rendered)
				}
			},
		},
		{
			name: "scan progress and completion update isolated scan state",
			setup: func(view *tuiModel) {
				view.page = tuiScan
				view.scanRunning = true
			},
			events: func(view *tuiModel) []tuiEvent {
				snapshot := TUIScanSnapshot{
					BaseConfig: cloneConfig(view.catalog),
					BaseState:  cloneTUIState(view.state),
					Config:     cloneConfig(view.catalog),
					State:      cloneTUIState(view.state),
				}
				return []tuiEvent{
					{eventType: tuiEventScanProgress, stage: "prepare", subject: "Obsidian"},
					{eventType: tuiEventScanDone, scan: snapshot},
				}
			},
			afterEvent: func(t *testing.T, view *tuiModel, event tuiEvent) {
				switch event.eventType {
				case tuiEventScanProgress:
					if !view.scanRunning || !strings.Contains(strings.Join(view.scanLogs, "\n"), "Obsidian") {
						t.Fatalf("scan progress was not applied: running=%v logs=%v", view.scanRunning, view.scanLogs)
					}
				case tuiEventScanDone:
					if view.scanRunning || !view.scanCompleted {
						t.Fatalf("scan_done was not applied: running=%v completed=%v", view.scanRunning, view.scanCompleted)
					}
				}
			},
			assert: func(t *testing.T, view *tuiModel, _ *tuiActionSpy, rendered string) {
				if view.scanRunning || !view.scanCompleted || !view.scanAutoDone {
					t.Fatalf("scan completion state = running:%v completed:%v auto:%v", view.scanRunning, view.scanCompleted, view.scanAutoDone)
				}
				if len(view.scanLogs) < 2 || len(view.logs) != 0 {
					t.Fatalf("scan logs are not isolated: scan=%v home=%v", view.scanLogs, view.logs)
				}
				summaryRunes := []rune(view.message)
				summaryPrefix := string(summaryRunes[:min(12, len(summaryRunes))])
				if summaryPrefix == "" || !strings.Contains(rendered, summaryPrefix) {
					t.Fatalf("screen omits scan completion summary prefix %q:\n%s", summaryPrefix, rendered)
				}
			},
		},
		{
			name: "modal blocks hidden update action",
			setup: func(view *tuiModel) {
				view.confirm = true
			},
			keys: []string{"u"},
			assert: func(t *testing.T, view *tuiModel, _ *tuiActionSpy, rendered string) {
				if !view.confirm {
					t.Fatal("hidden update key escaped the active confirmation modal")
				}
				primary, secondary := tuiConfirmationLabels(view)
				if primary == "" || secondary == "" || !strings.Contains(rendered, i18n.T(primary)) || !strings.Contains(rendered, i18n.T(secondary)) {
					t.Fatalf("modal confirmation labels are inconsistent: primary=%q secondary=%q", primary, secondary)
				}
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			runTUIScenario(t, scenario)
		})
	}
}
