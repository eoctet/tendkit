package ui

import (
	"strings"
	"testing"
	"time"

	"bytes"
	"context"
	"github.com/eoctet/tendkit/pkg/i18n"

	"fmt"
	"github.com/eoctet/tendkit/internal/model"
)

func tuiQuickSearchListTitle(view *tuiModel) string {
	if view.page == tuiScan {
		return tuiScanApplicationListTitle(view)
	}
	return tuiApplicationListTitle(view)
}

func tuiQuickSearchBaseTitle(view *tuiModel) string {
	if view.page == tuiScan {
		return i18n.T("tui.scan.app_list")
	}
	return i18n.T("tui.app_list")
}

func selectedTUIQuickSearchIndex(view *tuiModel) int {
	if view.page == tuiScan {
		return view.scanSelected
	}
	return view.selected
}

func quickSearchScroll(view *tuiModel) int {
	if view.page == tuiScan {
		return view.scanScroll
	}
	return view.scroll
}

func TestTUINavigationFlow(t *testing.T) {
	t.Run("tui-running-browse-and-exit-semantics", func(t *testing.T) {
		for _, scan := range []bool{false, true} {
			t.Run(fmt.Sprintf("scan=%v", scan), func(t *testing.T) {
				v := sampleTUIView()
				v.width, v.height = 120, 30
				v.catalog.Apps = append(v.catalog.Apps, model.Application{ID: "two", Name: "Two"})
				for index := 0; index < 60; index++ {
					line := fmt.Sprintf("log-%02d", index)
					v.logs = append(v.logs, line)
					v.scanLogs = append(v.scanLogs, line)
				}
				cancelled := 0
				if scan {
					v.page = tuiScan
					v.scanRunning = true
					v.scanCancel = func() { cancelled++ }
				} else {
					v.running = true
					v.cancel = func() { cancelled++ }
				}
				e := make(chan tuiEvent, 1)
				handleTUIKey(context.Background(), &v, "down", TUIActions{}, e)
				if (scan && v.scanSelected != 1) || (!scan && v.selected != 1) {
					t.Fatal("down did not browse")
				}
				handleTUIKey(context.Background(), &v, "l", TUIActions{}, e)
				if !(v.logFocus || v.scanLogFocus) {
					t.Fatal("L did not enter logs")
				}
				handleTUIKey(context.Background(), &v, "home", TUIActions{}, e)
				if (scan && v.scanLogOffset == 0) || (!scan && v.logOffset == 0) || (scan && !v.scanRunning) || (!scan && !v.running) {
					t.Fatalf("running log did not scroll without changing task state: %#v", v)
				}
				logOffset, scanLogOffset := v.logOffset, v.scanLogOffset
				handleTUIKey(context.Background(), &v, "esc", TUIActions{}, e)
				if cancelled != 0 || !(v.logFocus || v.scanLogFocus) || v.logOffset != logOffset || v.scanLogOffset != scanLogOffset {
					t.Fatal("ESC changed running log focus")
				}
				handleTUIKey(context.Background(), &v, "l", TUIActions{}, e)
				for _, key := range []string{"ctrl+c", " ", "s", "ctrl+s"} {
					before := v.message
					handleTUIKey(context.Background(), &v, key, TUIActions{}, e)
					if cancelled != 0 || v.message != before {
						t.Fatalf("hidden %q acted", key)
					}
				}
				handleTUIKey(context.Background(), &v, "esc", TUIActions{}, e)
				if cancelled != 1 || v.quitPending {
					t.Fatal("ESC base wrong")
				}
				v.quitPending = false
				handleTUIKey(context.Background(), &v, "q", TUIActions{}, e)
				if cancelled != 2 || !v.quitPending {
					t.Fatal("Q base wrong")
				}
			})
		}
	})
	t.Run("tui-quick-search-selects-only-unique-ascii-name-prefixes", func(t *testing.T) {
		view := sampleTUIView()
		view.width, view.height = 80, 24
		apps := []model.Application{
			view.catalog.Apps[0],
			{ID: "alpha", Name: "Alpha", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}},
			{ID: "alpine", Name: "Alpine", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}},
			{ID: "zulu", Name: "Zulu9", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}},
		}
		view.catalog.Apps = apps
		view.selected, view.scroll = 1, 0
		events := make(chan tuiEvent, 1)

		handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
		if !view.searchActive || view.searchQuery != "" {
			t.Fatalf("F did not enter quick search: %#v", view)
		}
		handleTUIKey(context.Background(), &view, "A", TUIActions{}, events)
		handleTUIKey(context.Background(), &view, "l", TUIActions{}, events)
		if view.searchQuery != "Al" || view.selected != 1 {
			t.Fatalf("multiple matches changed selection or query: %#v", view)
		}
		handleTUIKey(context.Background(), &view, "p", TUIActions{}, events)
		if view.selected != 1 || view.searchQuery != "Alp" {
			t.Fatalf("multiple matches after prefix changed selection: %#v", view)
		}
		handleTUIKey(context.Background(), &view, "i", TUIActions{}, events)
		if view.selected != 2 || view.scroll > view.selected {
			t.Fatalf("unique match did not select and reveal Alpine: %#v", view)
		}
		handleTUIKey(context.Background(), &view, "!", TUIActions{}, events)
		if view.searchQuery != "Alpi" {
			t.Fatalf("non-ASCII-alphanumeric key changed query: %#v", view)
		}
		handleTUIKey(context.Background(), &view, "esc", TUIActions{}, events)
		if view.searchActive || view.searchQuery != "" {
			t.Fatalf("Esc did not clear quick search: %#v", view)
		}

		for index := 0; index < 20; index++ {
			view.catalog.Apps = append(view.catalog.Apps, model.Application{ID: fmt.Sprintf("app-%d", index), Name: fmt.Sprintf("App%02d", index), StatusManaged: model.ManagedStatus{UpdateStatus: "current"}})
		}
		view.catalog.Apps = append(view.catalog.Apps, model.Application{ID: "xray-late", Name: "Xray9", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}})
		view.selected, view.scroll = 0, 0
		handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
		handleTUIKey(context.Background(), &view, "x", TUIActions{}, events)
		if view.selected != len(view.catalog.Apps)-1 || view.scroll == 0 || view.selected >= view.scroll+tuiApplicationListViewportHeight(&view) {
			t.Fatalf("unique offscreen match was not revealed: selected=%d scroll=%d viewport=%d", view.selected, view.scroll, tuiApplicationListViewportHeight(&view))
		}
		handleTUIKey(context.Background(), &view, "esc", TUIActions{}, events)
		handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
		handleTUIKey(context.Background(), &view, "q", TUIActions{}, events)
		if view.selected != len(view.catalog.Apps)-1 {
			t.Fatalf("zero-match query changed selection: %#v", view)
		}
		handleTUIKey(context.Background(), &view, "esc", TUIActions{}, events)

		view.page, view.scanRunning = tuiScan, true
		handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
		if view.searchActive {
			t.Fatalf("scan in progress accepted quick search: %#v", view)
		}

		view.page, view.scanRunning, view.detailFocus = tuiApps, false, true
		handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
		if view.searchActive {
			t.Fatalf("application details accepted quick search: %#v", view)
		}
		view.detailFocus, view.running = false, true
		handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
		if view.searchActive {
			t.Fatalf("running application page accepted quick search: %#v", view)
		}
	})
	t.Run("tui-quick-search-keeps-list-navigation-and-clears-with-ctrl-c", func(t *testing.T) {
		for _, page := range []tuiPage{tuiApps, tuiScan} {
			t.Run(pageName(page), func(t *testing.T) {
				view := sampleTUIView()
				view.page = page
				view.width, view.height = 80, 24
				view.catalog.Apps[0].Name = "Go"
				view.catalog.Apps = append(view.catalog.Apps,
					model.Application{ID: "node", Name: "Node", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}},
					model.Application{ID: "chrome", Name: "Chrome", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}},
				)
				for index := 0; index < 12; index++ {
					view.catalog.Apps = append(view.catalog.Apps, model.Application{ID: fmt.Sprintf("item-%d", index), Name: fmt.Sprintf("Item%02d", index), StatusManaged: model.ManagedStatus{UpdateStatus: "current"}})
				}
				events := make(chan tuiEvent, 1)
				handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
				handleTUIKey(context.Background(), &view, "g", TUIActions{}, events)
				handleTUIKey(context.Background(), &view, "o", TUIActions{}, events)
				if selectedTUIQuickSearchIndex(&view) != 0 || view.searchQuery != "go" {
					t.Fatalf("go did not select Go: %#v", view)
				}
				handleTUIKey(context.Background(), &view, "n", TUIActions{}, events)
				if selectedTUIQuickSearchIndex(&view) != 0 || view.searchQuery != "gon" {
					t.Fatalf("zero-match continuation unexpectedly restarted: %#v", view)
				}
				selectedBeforeClear, scrollBeforeClear := selectedTUIQuickSearchIndex(&view), quickSearchScroll(&view)
				handleTUIKey(context.Background(), &view, "ctrl+c", TUIActions{}, events)
				if !view.searchActive || view.searchQuery != "" || selectedTUIQuickSearchIndex(&view) != selectedBeforeClear || quickSearchScroll(&view) != scrollBeforeClear {
					t.Fatalf("CTRL+C did not clear only the non-empty query: %#v", view)
				}
				handleTUIKey(context.Background(), &view, "c", TUIActions{}, events)
				if view.searchQuery != "c" || selectedTUIQuickSearchIndex(&view) != 2 {
					t.Fatalf("c on an empty query did not search Chrome: %#v", view)
				}
				handleTUIKey(context.Background(), &view, "ctrl+c", TUIActions{}, events)
				handleTUIKey(context.Background(), &view, "ctrl+c", TUIActions{}, events)
				if !view.searchActive || view.searchQuery != "" || selectedTUIQuickSearchIndex(&view) != 2 {
					t.Fatalf("CTRL+C on an empty query was not a safe no-op: %#v", view)
				}
				handleTUIKey(context.Background(), &view, "C", TUIActions{}, events)
				if view.searchQuery != "C" || selectedTUIQuickSearchIndex(&view) != 2 {
					t.Fatalf("uppercase C did not remain a search character: %#v", view)
				}
				handleTUIKey(context.Background(), &view, "ctrl+c", TUIActions{}, events)
				handleTUIKey(context.Background(), &view, "n", TUIActions{}, events)
				if view.searchQuery != "n" || selectedTUIQuickSearchIndex(&view) != 1 {
					t.Fatalf("new query after clearing did not select Node: %#v", view)
				}
				handleTUIKey(context.Background(), &view, "j", TUIActions{}, events)
				handleTUIKey(context.Background(), &view, "k", TUIActions{}, events)
				if selectedTUIQuickSearchIndex(&view) != 1 || view.searchQuery != "njk" {
					t.Fatalf("j/k navigated instead of extending the query: %#v", view)
				}

				for _, key := range []string{"up", "down", "pagedown", "pageup", "home", "end"} {
					handleTUIKey(context.Background(), &view, key, TUIActions{}, events)
				}
				if selectedTUIQuickSearchIndex(&view) != len(view.catalog.Apps)-1 || quickSearchScroll(&view) == 0 {
					t.Fatalf("navigation did not reach and reveal final item during search: %#v", view)
				}
				screen := newTUIScreen(view.width, view.height)
				renderFooter(screen, &view, view.height-3, 3)
				if footer := stripTUIANSI(screen.string()); !strings.Contains(footer, "↑↓") {
					t.Fatalf("quick-search footer omits selection hint: %q", footer)
				}
			})
		}
	})
	t.Run("tui-quick-search-titles-show-query-and-limit-input", func(t *testing.T) {
		for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
			t.Run(string(language), func(t *testing.T) {
				useLanguage(t, language)
				for _, page := range []tuiPage{tuiApps, tuiScan} {
					t.Run(pageName(page), func(t *testing.T) {
						view := sampleTUIView()
						view.page = page
						view.width, view.height = 120, 30
						events := make(chan tuiEvent, 1)
						handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
						if title := tuiQuickSearchListTitle(&view); title != tuiQuickSearchBaseTitle(&view)+" ["+i18n.T("tui.app_list_search")+"]" {
							t.Fatalf("empty-query title = %q", title)
						}
						handleTUIKey(context.Background(), &view, "o", TUIActions{}, events)
						if title := tuiQuickSearchListTitle(&view); title != i18n.T("tui.app_list_search_query", tuiQuickSearchBaseTitle(&view), i18n.T("tui.app_list_search"), "o") {
							t.Fatalf("query title = %q", title)
						}
						handleTUIKey(context.Background(), &view, "ctrl+c", TUIActions{}, events)
						if !view.searchActive || view.searchQuery != "" || tuiQuickSearchListTitle(&view) != tuiQuickSearchBaseTitle(&view)+" ["+i18n.T("tui.app_list_search")+"]" {
							t.Fatalf("clear did not restore the empty-query title: %#v", view)
						}
						selected, scroll := selectedTUIQuickSearchIndex(&view), quickSearchScroll(&view)
						for index := 0; index < 20; index++ {
							handleTUIKey(context.Background(), &view, "x", TUIActions{}, events)
						}
						if view.searchQuery != strings.Repeat("x", 20) {
							t.Fatalf("20-character query = %q", view.searchQuery)
						}
						handleTUIKey(context.Background(), &view, "y", TUIActions{}, events)
						if view.searchQuery != strings.Repeat("x", 20) || selectedTUIQuickSearchIndex(&view) != selected || quickSearchScroll(&view) != scroll {
							t.Fatalf("21st character changed quick-search state: %#v", view)
						}
					})
				}
			})
		}
	})
	t.Run("tui-confirmation-dialog-grows-only-for-wrapped-prompt", func(t *testing.T) {
		view := sampleTUIView()
		view.width, view.height = 100, 30
		screen := newTUIScreen(view.width, view.height)
		prompt := strings.Repeat("wrapped prompt ", 8)
		lines := wrapTUI(prompt, 62)
		renderConfirmationDialog(screen, &view, "Title", prompt, tuiCyan, "Confirm", "Cancel")

		width, height := 68, len(lines)+6
		x, y := (screen.width-width)/2, (screen.height-height)/2
		if len(lines) < 2 {
			t.Fatal("test prompt did not wrap")
		}
		if screen.cells[y][x].value != '┌' || screen.cells[y+height-1][x].value != '└' {
			t.Fatalf("wrapped dialog height = %d, frame not found", height)
		}
		if screen.cells[y+height-3][x+3].value != '[' {
			t.Fatal("wrapped dialog buttons are not positioned after the prompt")
		}
	})
	t.Run("tui-header-messages-expire", func(t *testing.T) {
		view := sampleTUIView()
		view.setMessage("saved", false)
		deadline := view.messageUntil
		if deadline.IsZero() || view.expireMessage(deadline.Add(-time.Nanosecond)) {
			t.Fatal("message expired before its deadline")
		}
		if !view.expireMessage(deadline) {
			t.Fatal("message did not expire at its deadline")
		}
		if view.message != "" || view.messageError || !view.messageUntil.IsZero() {
			t.Fatalf("expired message state was not cleared: %#v", view)
		}

		view.setMessage("failure", true)
		if time.Until(view.messageUntil) <= tuiMessageDuration {
			t.Fatal("error message did not receive the longer visibility period")
		}
	})
}

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
