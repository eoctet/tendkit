package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

type reloadRequiredTestError struct{}

func (reloadRequiredTestError) Error() string        { return "external configuration change" }
func (reloadRequiredTestError) ReloadRequired() bool { return true }

type testOutputWriteCloser struct {
	io.Writer
	close func() error
}

type failingTestWriter struct{}

func (failingTestWriter) Write([]byte) (int, error) { return 0, errors.New("persist failed") }

func (writer testOutputWriteCloser) Close() error {
	if writer.close != nil {
		return writer.close()
	}
	return nil
}

func TestTUIExternalSaveConflictRequiresConfirmedReload(t *testing.T) {
	view := sampleTUIView()
	original := cloneConfig(view.catalog)
	external := cloneConfig(view.catalog)
	external.Apps[0].Name = "Externally edited"
	reloads := 0
	actions := TUIActions{
		SaveConfig: func(model.Config, model.Config) (model.Config, error) {
			return model.Config{}, reloadRequiredTestError{}
		},
		Reload: func() (model.Config, model.RuntimeState, error) {
			reloads++
			return external, model.RuntimeState{}, nil
		},
	}

	toggleSelectedApplication(&view, actions)
	if !view.reloadConfirm || view.catalog.Apps[0].Enabled != original.Apps[0].Enabled {
		t.Fatalf("external conflict did not preserve memory and request reload: %#v", view)
	}
	handleTUIReloadConfirmation(&view, "esc", actions)
	if view.reloadConfirm || reloads != 0 || view.catalog.Apps[0].Name != original.Apps[0].Name {
		t.Fatalf("cancelled reload changed state: reloads=%d view=%#v", reloads, view)
	}

	toggleSelectedApplication(&view, actions)
	handleTUIReloadConfirmation(&view, "enter", actions)
	if view.reloadConfirm || reloads != 1 || view.catalog.Apps[0].Name != external.Apps[0].Name {
		t.Fatalf("confirmed reload did not replace the snapshot: reloads=%d view=%#v", reloads, view)
	}
}

func TestTUIFailedReloadPreservesMemorySnapshot(t *testing.T) {
	view := sampleTUIView()
	original := cloneConfig(view.catalog)
	view.reloadConfirm = true
	actions := TUIActions{Reload: func() (model.Config, model.RuntimeState, error) {
		return model.Config{}, model.RuntimeState{}, errors.New("invalid external JSON")
	}}
	handleTUIReloadConfirmation(&view, "enter", actions)
	if !view.reloadConfirm || view.catalog.Apps[0].Name != original.Apps[0].Name {
		t.Fatalf("failed reload replaced memory snapshot: %#v", view)
	}
}

func TestTUIRenderHonorsConfiguredColorMode(t *testing.T) {
	var output bytes.Buffer
	if screen := newTUIScreenForOutput(&output, ModeAlways, 80, 24); !screen.color {
		t.Fatal("always color mode did not enable TUI colors")
	}
	if screen := newTUIScreenForOutput(&output, ModeNever, 80, 24); screen.color {
		t.Fatal("never color mode enabled TUI colors")
	}
}

func TestDecodeTUIKeys(t *testing.T) {
	keys := decodeTUIKeys([]byte("\x1b[A\x1b[B\x1b[C\x1b[D\x1b[5~\x1b[6~\x1b[3~\x1b[H\x1b[F\t\x03\x13\x15\r界"))
	want := []string{"up", "down", "right", "left", "pageup", "pagedown", "delete", "home", "end", "tab", "ctrl+c", "ctrl+s", "ctrl+u", "enter", "界"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %#v, want %#v", keys, want)
	}
}

func TestTUIInputDecoderPreservesSplitCSIAndUTF8(t *testing.T) {
	decoder := tuiInputDecoder{}
	var keys []string
	for _, chunk := range [][]byte{{0x1b}, {'['}, {'A', 0xe7}, {0x95}, {0x8c}} {
		keys = append(keys, decoder.decode(chunk)...)
	}
	want := []string{"up", "界"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %#v, want %#v", keys, want)
	}
}

func TestTUIInputDecoderFlushesIncompleteCSIAsEscapeAndText(t *testing.T) {
	for _, input := range []string{"\x1b[", "\x1b[5"} {
		decoder := tuiInputDecoder{}
		if keys := decoder.decode([]byte(input)); len(keys) != 0 {
			t.Fatalf("decode(%q) = %#v, want pending input", input, keys)
		}
		want := []string{"esc", "["}
		if input == "\x1b[5" {
			want = append(want, "5")
		}
		if keys := decoder.flushPending(); strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Fatalf("flushPending(%q) = %#v, want %#v", input, keys, want)
		}
	}
}

func TestReadTUIInputFlushesStandaloneEscape(t *testing.T) {
	const testDeadline = time.Second

	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = input.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan tuiEvent, 4)
	done := make(chan struct{})
	go func() { defer close(done); readTUIInput(ctx, input, events) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testDeadline):
			t.Error("input reader did not stop")
		}
	})
	if _, err := writer.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		select {
		case event := <-events:
			if event.eventType != tuiEventKey || event.key != "esc" {
				t.Fatalf("event = %#v, want standalone esc key", event)
			}
		case <-time.After(testDeadline):
			t.Fatal("standalone Escape was not flushed")
		}
		if attempt == 0 {
			if _, err := writer.Write([]byte{0x1b}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestReadTUIInputFlushesIncompleteCSIOnEOF(t *testing.T) {
	const testDeadline = time.Second

	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	events := make(chan tuiEvent, 4)
	done := make(chan struct{})
	go func() { defer close(done); readTUIInput(context.Background(), input, events) }()
	if _, err := writer.Write([]byte("\x1b[5")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"esc", "[", "5"} {
		select {
		case event := <-events:
			if event.eventType != tuiEventKey || event.key != want {
				t.Fatalf("event = %#v, want key %q", event, want)
			}
		case <-time.After(testDeadline):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	select {
	case <-done:
	case <-time.After(testDeadline):
		t.Fatal("input reader did not stop after EOF")
	}
}

func TestReadTUIInputFlushesIncompleteCSIOnTimeout(t *testing.T) {
	const testDeadline = time.Second

	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testDeadline):
			t.Error("input reader did not stop")
		}
		_ = input.Close()
		_ = writer.Close()
	}()
	events := make(chan tuiEvent, 4)
	go func() { defer close(done); readTUIInput(ctx, input, events) }()
	if _, err := writer.Write([]byte("\x1b[")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"esc", "["} {
		select {
		case event := <-events:
			if event.eventType != tuiEventKey || event.key != want {
				t.Fatalf("event = %#v, want key %q", event, want)
			}
		case <-time.After(testDeadline):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestReadTUIInputStopsAfterCancel(t *testing.T) {
	const testDeadline = time.Second

	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); readTUIInput(ctx, input, make(chan tuiEvent, 1)) }()
	cancel()
	select {
	case <-done:
	case <-time.After(testDeadline):
		t.Fatal("input reader did not stop after cancel")
	}
}

func TestTUIShortcutsUseLowercaseInputAndLowercaseCombinations(t *testing.T) {
	for _, key := range []string{"C", "A", "U", "L", "S", "Q"} {
		view := sampleTUIView()
		if quit := handleTUIKey(context.Background(), &view, key, TUIActions{}, make(chan tuiEvent, 1)); quit || view.page != tuiApps || view.confirm || view.logFocus || view.running {
			t.Fatalf("uppercase shortcut %q was accepted: %#v", key, view)
		}
	}

	selected := sampleTUIView()
	handleTUIKey(context.Background(), &selected, "u", TUIActions{}, make(chan tuiEvent, 1))
	if !selected.confirm || selected.confirmAll {
		t.Fatalf("lowercase u did not select single update: %#v", selected)
	}
	all := sampleTUIView()
	handleTUIKey(context.Background(), &all, "ctrl+u", TUIActions{}, make(chan tuiEvent, 1))
	if !all.confirm || !all.confirmAll {
		t.Fatalf("lowercase ctrl+u did not select update all: %#v", all)
	}
	scan := sampleTUIView()
	handleTUIKey(context.Background(), &scan, "ctrl+s", TUIActions{}, make(chan tuiEvent, 1))
	if scan.page != tuiScan {
		t.Fatalf("lowercase ctrl+s did not open scan management: %#v", scan)
	}
	configuration := sampleTUIView()
	configuration.page = tuiConfig
	configuration.dirty = true
	handleConfigKey(&configuration, "R", TUIActions{})
	if !configuration.dirty {
		t.Fatal("uppercase R reverted configuration")
	}
	handleConfigKey(&configuration, "r", TUIActions{})
	if configuration.dirty {
		t.Fatal("lowercase r did not revert configuration")
	}
}

func TestTUIKeymapBlocksHiddenStateActions(t *testing.T) {
	view := sampleTUIView()
	view.width, view.height = 80, 24
	view.detailFocus = true
	handleTUIKey(context.Background(), &view, "u", TUIActions{}, make(chan tuiEvent, 1))
	if view.confirm {
		t.Fatal("application detail allowed hidden update")
	}

	scan := sampleTUIView()
	scan.page, scan.width, scan.height, scan.scanRunning = tuiScan, 80, 24, true
	cancelled := false
	scan.scanCancel = func() { cancelled = true }
	handleTUIKey(context.Background(), &scan, "x", TUIActions{}, make(chan tuiEvent, 1))
	if scan.scanConfirm != "" || cancelled {
		t.Fatal("scan running allowed hidden exclusion action")
	}
	handleTUIKey(context.Background(), &scan, "esc", TUIActions{}, make(chan tuiEvent, 1))
	if !cancelled {
		t.Fatal("scan running did not allow visible ESC cancel")
	}

	keymap := tuiCurrentKeymap(&scan)
	if !strings.Contains(strings.Join(keymap.FooterLines(scan.width), " "), "ESC") || !keymap.Permits("esc") || keymap.Permits("x") {
		t.Fatalf("scan running keymap = %#v", keymap)
	}
}

func TestTUIKeymapMatchesVisibleModalAndPartialBindings(t *testing.T) {
	view := sampleTUIView()
	view.width, view.height, view.confirm = 80, 24, true
	keymap := tuiCurrentKeymap(&view)
	if !strings.Contains(strings.Join(keymap.FooterLines(120), " "), "ENTER") || !keymap.Permits("left") || !keymap.Permits("enter") || keymap.Permits("y") || keymap.Permits("n") {
		t.Fatalf("confirmation keymap = %#v", keymap)
	}
	screen := newTUIScreen(view.width, view.height)
	renderFooter(screen, &view, view.height-3, 3)
	if footer := stripTUIANSI(screen.string()); !strings.Contains(footer, "ENTER") || !strings.Contains(footer, "ESC") {
		t.Fatalf("confirmation footer omits visible controls: %q", footer)
	}

	partial := sampleTUIView()
	partial.width, partial.height, partial.page, partial.scanPartial = 80, 24, tuiScan, true
	partial.scanChanges = map[string]model.ScanApplicationChange{partial.catalog.Apps[0].ID: {
		Current: partial.catalog.Apps[0], Proposed: partial.catalog.Apps[0], Fields: []model.ScanFieldChange{{Field: "name"}},
	}}
	partial.scanConfirmID = partial.catalog.Apps[0].ID
	partial.partialFields = map[string]bool{}
	handleTUIKey(context.Background(), &partial, " ", TUIActions{}, make(chan tuiEvent, 1))
	if !partial.partialFields["name"] {
		t.Fatal("visible SPACE did not reach partial-merge toggle")
	}

	config := sampleTUIView()
	config.width, config.height, config.page = 80, 24, tuiConfig
	if tuiCurrentKeymap(&config).Permits("pageup") {
		t.Fatalf("configuration keymap retains hidden paging: %#v", tuiCurrentKeymap(&config))
	}
	config.configAppFocus = true
	if tuiCurrentKeymap(&config).Permits("q") {
		t.Fatalf("application configuration keymap retains hidden quit: %#v", tuiCurrentKeymap(&config))
	}

	running := sampleTUIView()
	running.width, running.height, running.running = 80, 24, true
	runningMap := tuiCurrentKeymap(&running)
	for _, key := range []string{"up", "down", "c", "a", "u", "ctrl+u", "l", "tab", "q"} {
		if !runningMap.Permits(key) {
			t.Fatalf("running keymap lost visible legacy action %q: %#v", key, runningMap)
		}
	}
	if runningMap.Permits("s") || runningMap.Permits("ctrl+s") || runningMap.Permits(" ") || runningMap.Permits("ctrl+c") {
		t.Fatalf("running keymap retained hidden actions: %#v", runningMap)
	}
	screen = newTUIScreen(running.width, running.height)
	renderFooter(screen, &running, running.height-3, 3)
	if footer := stripTUIANSI(screen.string()); strings.Contains(footer, "受阻") || strings.Contains(footer, "Blocked") {
		t.Fatalf("running footer retained blocked controls: %q", footer)
	}
}

func TestTUIScanAddedCandidateEditBindingMatchesHandler(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		useLanguage(t, language)
		view := sampleTUIView()
		view.page, view.width, view.height = tuiScan, 120, 36
		view.scanAdded = map[string]bool{view.catalog.Apps[0].ID: true}

		keymap := tuiCurrentKeymap(&view)
		footer := strings.Join(keymap.FooterLines(view.width), " ")
		if !strings.Contains(footer, i18n.T("tui.key.scan_edit")) {
			t.Fatalf("%s scan candidate footer = %q, want E edit binding", language, footer)
		}
		if strings.Contains(footer, i18n.T("tui.key.edit")) || !keymap.Permits("e") || keymap.Permits("enter") {
			t.Fatalf("%s scan candidate keymap disagrees with E binding: %#v footer=%q", language, keymap, footer)
		}

		enterView := view
		handleTUIKey(context.Background(), &enterView, "enter", TUIActions{}, make(chan tuiEvent, 1))
		if enterView.scanEditFocus {
			t.Fatal("hidden ENTER action opened scan candidate editor")
		}
		handleTUIKey(context.Background(), &view, "e", TUIActions{}, make(chan tuiEvent, 1))
		if !view.scanEditFocus || view.scanEditID != view.catalog.Apps[0].ID {
			t.Fatalf("visible E action did not open selected scan candidate editor: %#v", view)
		}
	}
}

func TestTUIStatefulKeymapEventMatrix(t *testing.T) {
	conflict := func(view *tuiModel) {
		app := view.catalog.Apps[0]
		view.page = tuiScan
		view.scanChanges = map[string]model.ScanApplicationChange{app.ID: {Current: app, Proposed: app, Fields: []model.ScanFieldChange{{Field: "name"}}}}
	}
	added := func(view *tuiModel) {
		view.page = tuiScan
		view.scanAdded = map[string]bool{view.catalog.Apps[0].ID: true}
	}
	cases := []struct {
		name    string
		setup   func(*tuiModel)
		visible string
		hidden  string
	}{
		{"apps", nil, "f", "x"},
		{"app-detail", func(v *tuiModel) { v.detailFocus = true }, "down", "u"},
		{"app-log", func(v *tuiModel) { v.logFocus = true }, "down", "u"},
		{"app-running", func(v *tuiModel) { v.running = true }, "c", "s"},
		{"app-search", func(v *tuiModel) { v.searchActive = true }, "z", "ctrl+u"},
		{"config-list", func(v *tuiModel) { v.page = tuiConfig }, "down", "pageup"},
		{"config-app", func(v *tuiModel) { v.page = tuiConfig; v.configAppFocus = true }, "down", "q"},
		{"config-edit", func(v *tuiModel) { v.page = tuiConfig; v.editing = true }, "z", "ctrl+u"},
		{"scan-normal", func(v *tuiModel) { v.page = tuiScan }, "s", "u"},
		{"scan-conflict", conflict, "p", "x"},
		{"scan-added", added, "j", "s"},
		{"scan-running", func(v *tuiModel) { v.page = tuiScan; v.scanRunning = true }, "esc", "x"},
		{"scan-detail", func(v *tuiModel) { v.page = tuiScan; v.detailFocus = true }, "down", "x"},
		{"scan-log", func(v *tuiModel) { v.page = tuiScan; v.scanLogFocus = true }, "down", "x"},
		{"scan-partial", func(v *tuiModel) {
			conflict(v)
			v.scanPartial = true
			v.scanConfirmID = v.catalog.Apps[0].ID
			v.partialFields = map[string]bool{}
		}, " ", "x"},
		{"scan-candidate", func(v *tuiModel) { added(v); v.scanEditFocus = true }, "down", "x"},
		{"scan-value-edit", func(v *tuiModel) { added(v); v.scanEditFocus = true; v.editing = true }, "z", "ctrl+u"},
		{"scan-search", func(v *tuiModel) { v.page = tuiScan; v.searchActive = true }, "z", "ctrl+u"},
		{"reload-confirm", func(v *tuiModel) { v.reloadConfirm = true }, "enter", "y"},
		{"config-exit-confirm", func(v *tuiModel) { v.configExitConfirm = true }, "esc", "q"},
		{"run-confirm", func(v *tuiModel) { v.confirm = true }, "left", "u"},
		{"scan-confirm", func(v *tuiModel) { conflict(v); v.scanConfirm = scanConfirmMerge }, "left", "x"},
		{"too-small", func(v *tuiModel) { v.width, v.height = 79, 23 }, "", "u"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := sampleTUIView()
			view.width, view.height = 240, 30
			if tc.setup != nil {
				tc.setup(&view)
			}
			keymap := tuiCurrentKeymap(&view)
			screen := newTUIScreen(view.width, view.height)
			renderFooter(screen, &view, view.height-3, 3)
			footer := stripTUIANSI(screen.string())
			for _, line := range keymap.FooterLines(view.width) {
				if !strings.Contains(footer, line) {
					t.Fatalf("footer line %q is not rendered: %q", line, footer)
				}
			}
			starts, scans, saves := 0, 0, 0
			actions := TUIActions{
				StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) {
					starts++
					return nil, errors.New("unexpected")
				},
				Scan: func(context.Context, TUIScanRequest, TUIScanObserver) (TUIScanSnapshot, error) {
					scans++
					return TUIScanSnapshot{}, errors.New("unexpected")
				},
				SaveConfig: func(model.Config, model.Config) (model.Config, error) {
					saves++
					return model.Config{}, errors.New("unexpected")
				},
				SaveScan: func(model.Config, model.Config) (model.Config, error) {
					saves++
					return model.Config{}, errors.New("unexpected")
				},
			}
			events := make(chan tuiEvent, 1)
			if tc.hidden != "" {
				if keymap.Permits(tc.hidden) {
					t.Fatalf("hidden key %q is exposed by %#v", tc.hidden, keymap)
				}
				handleTUIKey(context.Background(), &view, tc.hidden, actions, events)
				if starts != 0 || scans != 0 || saves != 0 {
					t.Fatalf("hidden key %q invoked action: run=%d scan=%d save=%d", tc.hidden, starts, scans, saves)
				}
			}
			if tc.visible != "" && !keymap.Permits(tc.visible) {
				t.Fatalf("visible key %q is not permitted by %#v", tc.visible, keymap)
			}
		})
	}
}

func TestTUIStatefulKeymapFooterWrapsAllBindingsAtEightyColumns(t *testing.T) {
	states := []struct {
		name  string
		setup func(*tuiModel)
	}{
		{"apps", nil}, {"detail", func(v *tuiModel) { v.detailFocus = true }}, {"logs", func(v *tuiModel) { v.logFocus = true }},
		{"running", func(v *tuiModel) { v.running = true }}, {"search", func(v *tuiModel) { v.searchActive = true }},
		{"config", func(v *tuiModel) { v.page = tuiConfig }}, {"scan", func(v *tuiModel) { v.page = tuiScan }},
		{"scan-running", func(v *tuiModel) { v.page = tuiScan; v.scanRunning = true }}, {"confirm", func(v *tuiModel) { v.confirm = true }},
	}
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		useLanguage(t, language)
		for _, state := range states {
			t.Run(string(language)+"/"+state.name, func(t *testing.T) {
				view := sampleTUIView()
				view.width, view.height = 80, 24
				if state.setup != nil {
					state.setup(&view)
				}
				var output bytes.Buffer
				renderTUI(&output, &view)
				rendered := stripTUIANSI(output.String())
				for _, line := range tuiCurrentKeymap(&view).FooterLines(view.width) {
					if !strings.Contains(rendered, line) {
						t.Fatalf("footer dropped line %q:\n%s", line, rendered)
					}
				}
				for _, line := range tuiCurrentKeymap(&view).FooterLines(view.width) {
					if DisplayWidth(line) > view.width-4 {
						t.Fatalf("footer line exceeds budget: %q", line)
					}
				}
			})
		}
	}
}

func TestTUIDynamicFooterViewportMatchesRenderedLayout(t *testing.T) {
	for _, page := range []tuiPage{tuiApps, tuiScan} {
		view := sampleTUIView()
		view.page, view.width, view.height, view.searchActive = page, 80, 24, true
		for index := 0; index < 24; index++ {
			view.catalog.Apps = append(view.catalog.Apps, model.Application{ID: fmt.Sprintf("extra-%d", index), Name: fmt.Sprintf("Extra %d", index)})
		}
		_, selected, scroll := tuiQuickSearchList(&view)
		setTUIQuickSearchSelection(&view, selected, scroll, len(view.catalog.Apps)-1)
		if *selected < *scroll || *selected >= *scroll+tuiApplicationListViewportHeight(&view) {
			t.Fatalf("%v quick-search selection escaped dynamic viewport: %#v", page, view)
		}
	}
	detail := sampleTUIView()
	detail.width, detail.height, detail.detailFocus = 80, 24, true
	detail.catalog.Apps[0].StatusManaged.Error = strings.Repeat("long error ", 100)
	maxOffset := tuiMaxDetailOffset(&detail)
	scrollTUIDetails(&detail, maxOffset+100)
	if detail.detailOffset != maxOffset {
		t.Fatalf("detail max offset=%d, want %d", detail.detailOffset, maxOffset)
	}
}

func TestTUIKeymapTextAndSmallTerminalSafety(t *testing.T) {
	search := sampleTUIView()
	search.searchActive = true
	for _, key := range []string{"a", "Z", "0", "9"} {
		if !tuiCurrentKeymap(&search).Permits(key) {
			t.Fatalf("search rejected %q", key)
		}
	}
	for _, key := range []string{"!", "中"} {
		if tuiCurrentKeymap(&search).Permits(key) {
			t.Fatalf("search accepted %q", key)
		}
	}
	edit := sampleTUIView()
	edit.editing = true
	if !tuiCurrentKeymap(&edit).Permits("!") || !tuiCurrentKeymap(&edit).Permits("中") {
		t.Fatal("editor text binding narrowed unexpectedly")
	}
	keys := []string{"up", "down", "pageup", "pagedown", "home", "end", "enter", " ", "tab", "esc", "q", "ctrl+c", "a", "u", "x", "ctrl+s", "ctrl+u"}
	for _, size := range [][2]int{{79, 23}, {80, 23}, {79, 24}} {
		view := sampleTUIView()
		view.width, view.height = size[0], size[1]
		cancelled, starts, scans, saves := false, 0, 0, 0
		view.cancel = func() { cancelled = true }
		actions := TUIActions{StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) { starts++; return nil, nil }, Scan: func(context.Context, TUIScanRequest, TUIScanObserver) (TUIScanSnapshot, error) {
			scans++
			return TUIScanSnapshot{}, nil
		}, SaveConfig: func(model.Config, model.Config) (model.Config, error) { saves++; return model.Config{}, nil }, SaveScan: func(model.Config, model.Config) (model.Config, error) { saves++; return model.Config{}, nil }}
		if len(tuiCurrentKeymap(&view).FooterLines(view.width)) != 0 {
			t.Fatalf("small %v retains bindings", size)
		}
		before := fmt.Sprintf("%d/%d/%v/%v/%s", view.page, view.selected, view.confirm, view.searchActive, view.message)
		for _, key := range keys {
			if handleTUIKey(context.Background(), &view, key, actions, make(chan tuiEvent, 1)) {
				t.Fatalf("small %v key %q quit", size, key)
			}
		}
		after := fmt.Sprintf("%d/%d/%v/%v/%s", view.page, view.selected, view.confirm, view.searchActive, view.message)
		if cancelled || starts != 0 || scans != 0 || saves != 0 || before != after {
			t.Fatalf("small %v changed state", size)
		}
	}
}

func TestTUIScanLogQuitCancelsOnlyRunningScan(t *testing.T) {
	running := sampleTUIView()
	running.page, running.scanRunning, running.scanLogFocus = tuiScan, true, true
	cancelled := 0
	running.scanCancel = func() { cancelled++ }
	if quit := handleTUIKey(context.Background(), &running, "q", TUIActions{}, make(chan tuiEvent, 1)); quit || cancelled != 1 || !running.quitPending || !running.scanRunning {
		t.Fatalf("running scan log quit=%v cancelled=%d pending=%v running=%v", quit, cancelled, running.quitPending, running.scanRunning)
	}
	plain := sampleTUIView()
	plain.page, plain.scanLogFocus = tuiScan, true
	if quit := handleTUIKey(context.Background(), &plain, "q", TUIActions{}, make(chan tuiEvent, 1)); !quit || plain.quitPending {
		t.Fatalf("plain scan log quit=%v pending=%v", quit, plain.quitPending)
	}
}

func TestTUIConfirmationFooterMatchesDialogButtons(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		useLanguage(t, language)
		for _, state := range []struct {
			name  string
			setup func(*tuiModel)
		}{
			{"run", func(v *tuiModel) { v.confirm = true }}, {"config", func(v *tuiModel) { v.configExitConfirm = true }}, {"reload", func(v *tuiModel) { v.reloadConfirm = true }},
			{"scan", func(v *tuiModel) { v.page = tuiScan; v.scanConfirm = scanConfirmMerge }}, {"skip", func(v *tuiModel) { v.page = tuiScan; v.scanConfirm = scanConfirmDeleteExclude }},
		} {
			t.Run(string(language)+"/"+state.name, func(t *testing.T) {
				view := sampleTUIView()
				view.width, view.height = 120, 30
				state.setup(&view)
				primary, secondary := tuiConfirmationLabels(&view)
				footer := strings.Join(tuiCurrentKeymap(&view).FooterLines(view.width), " ")
				if !strings.Contains(footer, "ENTER "+i18n.T(primary)) || !strings.Contains(footer, "ESC "+i18n.T(secondary)) {
					t.Fatalf("footer=%q", footer)
				}
				for _, key := range []string{"left", "right", "enter", "esc"} {
					if !tuiCurrentKeymap(&view).Permits(key) {
						t.Fatalf("missing %s", key)
					}
				}
				for _, key := range []string{"y", "n", "q"} {
					if tuiCurrentKeymap(&view).Permits(key) {
						t.Fatalf("hidden %s", key)
					}
				}
				var output bytes.Buffer
				renderTUI(&output, &view)
				rendered := stripTUIANSI(output.String())
				if !strings.Contains(rendered, "[ ENTER "+i18n.T(primary)+" ]") || !strings.Contains(rendered, "[ ESC "+i18n.T(secondary)+" ]") {
					t.Fatalf("dialog=%q", rendered)
				}
			})
		}
	}
}

func TestTUIRunningBrowseAndExitSemantics(t *testing.T) {
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
}

func TestTUIScanPageDuringUpdateRunHasOnlyBrowseAndExitKeys(t *testing.T) {
	v := sampleTUIView()
	v.page, v.running = tuiScan, true
	v.catalog.Apps = append(v.catalog.Apps, model.Application{ID: "two", Name: "Two"})
	cancelled := 0
	v.cancel = func() { cancelled++ }
	events := make(chan tuiEvent, 1)
	for _, key := range []string{"a", "s", "x", "ctrl+c", " "} {
		before := v.message
		handleTUIKey(context.Background(), &v, key, TUIActions{}, events)
		if v.message != before || cancelled != 0 {
			t.Fatalf("hidden %q acted", key)
		}
	}
	handleTUIKey(context.Background(), &v, "down", TUIActions{}, events)
	if v.scanSelected != 1 || !v.running {
		t.Fatal("down did not browse scan during update")
	}
	handleTUIKey(context.Background(), &v, "l", TUIActions{}, events)
	if !v.scanLogFocus || !v.running {
		t.Fatal("L did not enter scan logs")
	}
	handleTUIKey(context.Background(), &v, "q", TUIActions{}, events)
	if cancelled != 1 || !v.quitPending || !v.running {
		t.Fatal("Q did not safely exit update from scan log")
	}
}

func TestTUIFeedbackFooterLabelsAreUnambiguous(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		useLanguage(t, language)
		assertLabels := func(name string, view tuiModel, expected []string, forbidden []string) {
			t.Helper()
			labels := map[string]bool{}
			for _, line := range tuiCurrentKeymap(&view).FooterLines(view.width) {
				for _, label := range strings.Split(line, "  ") {
					labels[label] = true
				}
			}
			for _, label := range expected {
				if !labels[i18n.T(label)] {
					t.Fatalf("%s missing label %q: %#v", name, label, labels)
				}
			}
			for _, label := range forbidden {
				if labels[i18n.T(label)] {
					t.Fatalf("%s retained ambiguous label %q: %#v", name, label, labels)
				}
			}
		}

		search := sampleTUIView()
		search.searchActive = true
		assertLabels("search", search, []string{"tui.key.exit_search_long", "tui.key.clear"}, []string{"tui.key.exit_search"})

		logs := sampleTUIView()
		logs.logFocus = true
		assertLabels("logs", logs, []string{"tui.key.back_logs_only"}, []string{"tui.key.back_logs"})
		if tuiCurrentKeymap(&logs).Permits("esc") {
			t.Fatal("log focus still permits ESC")
		}

		configEdit := sampleTUIView()
		configEdit.page, configEdit.editing = tuiConfig, true
		assertLabels("config-edit", configEdit, []string{"tui.key.confirm_edit", "tui.key.stage_only"}, []string{"tui.key.stage"})

		scanEdit := sampleTUIView()
		scanEdit.page, scanEdit.scanEditFocus, scanEdit.editing = tuiScan, true, true
		assertLabels("scan-value-edit", scanEdit, []string{"tui.key.apply_edit", "tui.key.stage_only"}, []string{"tui.key.stage"})

		config := sampleTUIView()
		config.page = tuiConfig
		assertLabels("config", config, []string{"tui.key.edit", "tui.key.back_only"}, []string{"tui.key.back"})

		scanList := sampleTUIView()
		scanList.page = tuiScan
		assertLabels("scan", scanList, []string{"tui.key.back_only"}, []string{"tui.key.back"})
	}
}

func TestTUIQuickSearchSelectsOnlyUniqueASCIINamePrefixes(t *testing.T) {
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
}

func TestTUIQuickSearchKeepsListNavigationAndClearsWithCtrlC(t *testing.T) {
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
}

func TestTUIQuickSearchFooterShowsCtrlCClearAtEightyColumns(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Run(string(language), func(t *testing.T) {
			useLanguage(t, language)
			clear := "CTRL+C 清除"
			if language == i18n.English {
				clear = "CTRL+C Clear"
			}
			for _, page := range []tuiPage{tuiApps, tuiScan} {
				view := sampleTUIView()
				view.page, view.width, view.height = page, 80, 24
				view.searchActive, view.searchQuery = true, "o"
				screen := newTUIScreen(view.width, view.height)
				renderFooter(screen, &view, view.height-3, 3)
				if footer := stripTUIANSI(screen.string()); !strings.Contains(footer, clear) {
					t.Fatalf("%s quick-search footer omits clear at 80 columns: %q", pageName(page), footer)
				}
			}
		})
	}
}

func TestTUIQuickSearchTitlesShowQueryAndLimitInput(t *testing.T) {
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
}

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

func TestTUIQuickSearchRendersBadgeTitlesAndBlueNamesAtEightyColumns(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Run(string(language), func(t *testing.T) {
			useLanguage(t, language)
			view := sampleTUIView()
			view.width, view.height = 80, 24
			view.catalog.Apps = append(view.catalog.Apps,
				model.Application{ID: "current", Name: "Current", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}},
			)
			view.selected = 0
			view.searchActive, view.searchQuery = true, "o"
			screen := newTUIScreen(80, 24)
			renderTUIHeader(screen, &view)
			renderAppsPage(screen, &view, 3, 13)
			plain := stripTUIANSI(screen.string())
			if !strings.Contains(plain, i18n.T("tui.apps_badge_updates", 1, 2)) || !strings.Contains(plain, i18n.T("tui.app_list_search")) {
				t.Fatalf("header or list quick-search title missing at 80 columns:\n%s", plain)
			}
			nameX := 2 + applicationTableColumnWidths(80*67/100 - 2)[0]
			assertTUISelectedSearchMatchStyle(t, screen, screen.cells[7][nameX])
			view.selected = 1
			screen = newTUIScreen(80, 24)
			renderAppsPage(screen, &view, 3, 13)
			if screen.cells[7][nameX].style != tuiBlue {
				t.Fatalf("unselected matching name style = %q, want blue", screen.cells[7][nameX].style)
			}
			view.catalog.Apps[0].StatusManaged.HasUpdate = false
			if badge := tuiPageBadge(&view); badge != "[ "+i18n.T("tui.apps_badge_total", 2)+" ]" {
				t.Fatalf("zero-update badge = %q", badge)
			}
		})
	}
}

func TestTUIScanQuickSearchAndFocusedDetailTitle(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Run(string(language), func(t *testing.T) {
			useLanguage(t, language)
			view := sampleTUIView()
			view.page = tuiScan
			view.width, view.height = 80, 24
			view.catalog.Apps[0].Description = strings.Repeat("long scan detail ", 20)
			events := make(chan tuiEvent, 1)

			handleTUIKey(context.Background(), &view, "f", TUIActions{}, events)
			handleTUIKey(context.Background(), &view, "o", TUIActions{}, events)
			if !view.searchActive || view.scanSelected != 0 {
				t.Fatalf("scan list did not handle quick search: %#v", view)
			}
			handleTUIKey(context.Background(), &view, "esc", TUIActions{}, events)
			view.width = 120
			handleTUIKey(context.Background(), &view, "tab", TUIActions{}, events)
			if !view.detailFocus {
				t.Fatal("Tab did not focus scan details")
			}
			handleTUIKey(context.Background(), &view, "end", TUIActions{}, events)
			var output bytes.Buffer
			renderTUI(&output, &view)
			plain := stripTUIANSI(output.String())
			if !strings.Contains(plain, i18n.T("tui.details_focused", view.scanDetail+1, tuiMaxScanDetailOffset(&view, stackedScanUpperHeight(&view))+1)) {
				t.Fatalf("focused scan detail title missing scroll position:\n%s", plain)
			}
			handleTUIKey(context.Background(), &view, "esc", TUIActions{}, events)
			if view.detailFocus {
				t.Fatal("Esc did not leave scan detail focus")
			}
		})
	}
}

func TestTUIScanDetailFocusShortContentShowsOneOfOneAndRestoresTitle(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Run(string(language), func(t *testing.T) {
			useLanguage(t, language)
			view := sampleTUIView()
			view.page = tuiScan
			view.width, view.height = 120, 48
			view.detailFocus = true
			upperHeight, activityTop, activityHeight := stackedPageLayout(view.height, 3, 3)

			screen := newTUIScreen(view.width, view.height)
			renderScanPage(screen, &view, 3, upperHeight, activityTop, activityHeight)
			if title := stripTUIANSI(screen.string()); !strings.Contains(title, i18n.T("tui.details_focused", 1, 1)) {
				t.Fatalf("focused short scan detail title missing 1/1: %q", title)
			}

			handleTUIKey(context.Background(), &view, "esc", TUIActions{}, make(chan tuiEvent, 1))
			screen = newTUIScreen(view.width, view.height)
			renderScanPage(screen, &view, 3, upperHeight, activityTop, activityHeight)
			plain := stripTUIANSI(screen.string())
			if view.detailFocus || !strings.Contains(plain, i18n.T("tui.scan.application_details")) || strings.Contains(plain, i18n.T("tui.details_focused", 1, 1)) {
				t.Fatalf("unfocused short scan detail title was not restored: %q", plain)
			}
		})
	}
}

func TestTUIScanQuickSearchSelectsUniqueMatchAndRendersSelectedBlueName(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Run(string(language), func(t *testing.T) {
			useLanguage(t, language)
			view := sampleTUIView()
			view.page = tuiScan
			view.width, view.height = 120, 30
			view.catalog.Apps = append(view.catalog.Apps,
				model.Application{ID: "alpha", Name: "Alpha", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}},
				model.Application{ID: "bravo", Name: "Bravo", StatusManaged: model.ManagedStatus{UpdateStatus: "current"}},
			)
			handleTUIKey(context.Background(), &view, "f", TUIActions{}, make(chan tuiEvent, 1))
			handleTUIKey(context.Background(), &view, "B", TUIActions{}, make(chan tuiEvent, 1))
			if view.scanSelected != 2 {
				t.Fatalf("unique scan match selected %d, want 2", view.scanSelected)
			}

			upperHeight, activityTop, activityHeight := stackedPageLayout(view.height, 3, 3)
			screen := newTUIScreen(view.width, view.height)
			renderScanPage(screen, &view, 3, upperHeight, activityTop, activityHeight)
			plain := stripTUIANSI(screen.string())
			if !strings.Contains(plain, i18n.T("tui.app_list_search_query", i18n.T("tui.scan.app_list"), i18n.T("tui.app_list_search"), "B")) {
				t.Fatalf("scan quick-search title missing: %q", plain)
			}
			nameX := 2 + scanTableColumnWidths(view.width*67/100 - 2)[0]
			assertTUISelectedSearchMatchStyle(t, screen, screen.cells[9][nameX])
		})
	}
}

func TestTUIScanListFootersAdvertiseQuickSearch(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Run(string(language), func(t *testing.T) {
			useLanguage(t, language)
			shortcut := "F 搜索"
			if language == i18n.English {
				shortcut = "F Search"
			}
			for _, state := range []struct {
				name  string
				apply func(*tuiModel)
			}{
				{name: "normal", apply: func(*tuiModel) {}},
				{name: "conflict", apply: func(view *tuiModel) {
					app := view.catalog.Apps[0]
					view.scanChanges = map[string]model.ScanApplicationChange{app.ID: {Current: app, Proposed: app, Fields: []model.ScanFieldChange{{Field: "name"}}}}
				}},
				{name: "added", apply: func(view *tuiModel) {
					view.scanAdded = map[string]bool{view.catalog.Apps[0].ID: true}
				}},
			} {
				t.Run(state.name, func(t *testing.T) {
					view := sampleTUIView()
					view.page = tuiScan
					view.width, view.height = 120, 24
					state.apply(&view)
					screen := newTUIScreen(view.width, view.height)
					renderFooter(screen, &view, view.height-3, 3)
					if footer := stripTUIANSI(screen.string()); !strings.Contains(footer, shortcut) {
						t.Fatalf("%s scan footer does not advertise quick search: %q", state.name, footer)
					}
				})
			}
		})
	}
}

func TestTUIScanListFootersShowEarlyDetailsShortcutAtEightyColumns(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Run(string(language), func(t *testing.T) {
			useLanguage(t, language)
			details := "TAB 详情"
			if language == i18n.English {
				details = "TAB Details"
			}
			for _, state := range []struct {
				name  string
				apply func(*tuiModel)
			}{
				{name: "normal", apply: func(*tuiModel) {}},
				{name: "conflict", apply: func(view *tuiModel) {
					app := view.catalog.Apps[0]
					view.scanChanges = map[string]model.ScanApplicationChange{app.ID: {Current: app, Proposed: app, Fields: []model.ScanFieldChange{{Field: "name"}}}}
				}},
				{name: "added", apply: func(view *tuiModel) { view.scanAdded = map[string]bool{view.catalog.Apps[0].ID: true} }},
			} {
				t.Run(state.name, func(t *testing.T) {
					view := sampleTUIView()
					view.page = tuiScan
					view.width, view.height = 80, 24
					state.apply(&view)
					screen := newTUIScreen(view.width, view.height)
					renderFooter(screen, &view, view.height-3, 3)
					if footer := stripTUIANSI(screen.string()); !strings.Contains(footer, details) {
						t.Fatalf("%s scan footer omits early details shortcut at 80 columns: %q", state.name, footer)
					}
				})
			}
		})
	}
}

func stackedScanUpperHeight(view *tuiModel) int {
	upperHeight, _, _ := stackedPageLayout(view.height, 3, 3)
	return upperHeight
}

func TestTUIKeyLabelsUseUppercasePresentation(t *testing.T) {
	states := []func(*tuiModel){
		func(*tuiModel) {},
		func(v *tuiModel) { v.searchActive = true },
		func(v *tuiModel) { v.detailFocus = true },
		func(v *tuiModel) { v.logFocus = true },
		func(v *tuiModel) { v.running = true },
		func(v *tuiModel) { v.confirm = true },
		func(v *tuiModel) { v.page = tuiConfig },
		func(v *tuiModel) { v.page, v.configAppFocus = tuiConfig, true },
		func(v *tuiModel) { v.page, v.editing = tuiConfig, true },
		func(v *tuiModel) { v.page = tuiScan },
		func(v *tuiModel) { v.page, v.scanRunning = tuiScan, true },
		func(v *tuiModel) { v.page, v.scanPartial = tuiScan, true },
		func(v *tuiModel) { v.page, v.scanEditFocus = tuiScan, true },
		func(v *tuiModel) { v.page, v.scanEditFocus, v.editing = tuiScan, true, true },
	}
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		useLanguage(t, language)
		for _, setup := range states {
			view := sampleTUIView()
			view.width, view.height = 240, 30
			setup(&view)
			value := strings.Join(tuiCurrentKeymap(&view).FooterLines(view.width), " ")
			for _, forbidden := range []string{"Shift+", "Ctrl+", "Enter", "Esc", "Tab", "Space", "PageUp", "PageDown", "Home", "End", "Backspace"} {
				if strings.Contains(value, forbidden) {
					t.Fatalf("footer contains non-uppercase key label %q: %q", forbidden, value)
				}
			}
		}
	}
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.confirm = true
	view.width, view.height = 100, 30
	screen := newTUIScreen(view.width, view.height)
	renderConfirmation(screen, &view)
	confirmation := stripTUIANSI(screen.string())
	if !strings.Contains(confirmation, "[ ENTER ") || !strings.Contains(confirmation, "[ ESC ") {
		t.Fatalf("confirmation keys are not uppercase: %q", confirmation)
	}
}

func TestTUIRunConfirmationSupportsHorizontalSelection(t *testing.T) {
	view := sampleTUIView()
	view.confirm = true
	started := 0
	actions := TUIActions{StartRun: func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error) {
		started++
		return nil, errors.New("unexpected run")
	}}
	events := make(chan tuiEvent, 1)

	handleTUIKey(context.Background(), &view, "right", actions, events)
	if view.confirmChoice != tuiConfirmationSecondary {
		t.Fatalf("right did not select cancel: %#v", view)
	}
	handleTUIKey(context.Background(), &view, "enter", actions, events)
	if view.confirm || started != 0 {
		t.Fatalf("selected cancellation was not applied: confirm=%v started=%d", view.confirm, started)
	}

	view.confirm = true
	view.confirmChoice = tuiConfirmationSecondary
	handleTUIKey(context.Background(), &view, "left", actions, events)
	if view.confirmChoice != tuiConfirmationPrimary {
		t.Fatalf("left did not select confirmation: %#v", view)
	}
	handleTUIKey(context.Background(), &view, "enter", actions, events)
	if view.confirm || started != 1 {
		t.Fatalf("selected confirmation was not applied: confirm=%v started=%d", view.confirm, started)
	}
}

func TestTUIConfirmationRendersSelectedButtonFocus(t *testing.T) {
	view := sampleTUIView()
	view.confirm = true
	view.confirmChoice = tuiConfirmationSecondary
	view.width, view.height = 100, 30
	screen := newTUIScreen(view.width, view.height)
	renderConfirmation(screen, &view)
	width, height := min(68, screen.width-8), 7
	x, y := (screen.width-width)/2, (screen.height-height)/2
	secondaryX := x + 3 + DisplayWidth("[ ENTER "+i18n.T("tui.confirm")+" ]") + 8
	if screen.cells[y+4][x+3].style == tuiFocus || screen.cells[y+4][secondaryX].style != tuiFocus {
		t.Fatal("confirmation focus did not move to the secondary button")
	}
}

func TestTUIScanConfirmationSupportsHorizontalSelection(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	view.scanConfirm = scanConfirmMerge
	view.scanConfirmID = "obsidian"
	saves := 0
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		saves++
		return cloneConfig(catalog), nil
	}}

	handleScanConfirmationKey(&view, "right", actions)
	if view.confirmChoice != tuiConfirmationSecondary || view.scanConfirm == "" {
		t.Fatalf("right did not select scan cancellation: %#v", view)
	}
	handleScanConfirmationKey(&view, "enter", actions)
	if view.scanConfirm != "" || saves != 0 {
		t.Fatalf("selected scan cancellation changed configuration: confirm=%q saves=%d", view.scanConfirm, saves)
	}
}

func TestTUIScanConfirmationRendersSelectedButtonFocus(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	view.scanConfirm = scanConfirmDelete
	view.scanConfirmID = "obsidian"
	view.confirmChoice = tuiConfirmationSecondary
	view.width, view.height = 100, 30
	screen := newTUIScreen(view.width, view.height)
	renderScanConfirmation(screen, &view)
	width, height := min(68, screen.width-8), 7
	x, y := (screen.width-width)/2, (screen.height-height)/2
	secondaryX := x + 3 + DisplayWidth("[ ENTER "+i18n.T("tui.confirm")+" ]") + 8
	if screen.cells[y+height-3][x+3].style == tuiFocus || screen.cells[y+height-3][secondaryX].style != tuiFocus {
		t.Fatal("scan confirmation focus did not move to the secondary button")
	}
}

func TestTUIConfirmationDialogsShareCompactLayout(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		useLanguage(t, language)
		view := sampleTUIView()
		view.width, view.height = 100, 30
		view.confirmAll = true
		view.scanConfirm = scanConfirmDelete
		view.scanConfirmID = "obsidian"

		renders := []struct {
			name   string
			render func(*tuiScreen, *tuiModel)
		}{
			{name: "update", render: renderConfirmation},
			{name: "configuration exit", render: renderConfigExitConfirmation},
			{name: "scan", render: renderScanConfirmation},
		}
		for _, item := range renders {
			screen := newTUIScreen(view.width, view.height)
			item.render(screen, &view)
			width, height := 68, 7
			x, y := (screen.width-width)/2, (screen.height-height)/2
			if screen.cells[y][x].value != '┌' || screen.cells[y][x+width-1].value != '┐' ||
				screen.cells[y+height-1][x].value != '└' || screen.cells[y+height-1][x+width-1].value != '┘' {
				t.Fatalf("%s dialog does not use the shared 68x7 frame", item.name)
			}
			if screen.cells[y+4][x+3].value != '[' {
				t.Fatalf("%s dialog button row is not compact", item.name)
			}
		}
	}
}

func TestTUIConfirmationDialogGrowsOnlyForWrappedPrompt(t *testing.T) {
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
}

func TestTUIScanDeleteExclusionSecondarySelectionSkipsExclusion(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	view.scanConfirm = scanConfirmDeleteExclude
	view.scanConfirmID = "obsidian"
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}

	handleScanConfirmationKey(&view, "right", actions)
	handleScanConfirmationKey(&view, "enter", actions)
	if len(view.catalog.Apps) != 0 || len(view.catalog.Settings.Scan.Exclude) != 0 {
		t.Fatalf("secondary delete choice did not skip exclusion: %#v", view.catalog)
	}
}

func TestTUIStringEditorMovesAndEditsUnicodeAtCursor(t *testing.T) {
	view := sampleTUIView()
	view.editing = true
	view.editValue = "abc界def"
	view.editCursor = 4
	events := make(chan tuiEvent, 1)
	actions := TUIActions{}

	handleTUIKey(context.Background(), &view, "left", actions, events)
	handleTUIKey(context.Background(), &view, "backspace", actions, events)
	handleTUIKey(context.Background(), &view, "中", actions, events)
	handleTUIKey(context.Background(), &view, "delete", actions, events)
	if view.editValue != "ab中def" || view.editCursor != 3 {
		t.Fatalf("middle edit = %q at %d, want %q at 3", view.editValue, view.editCursor, "ab中def")
	}

	handleTUIKey(context.Background(), &view, "home", actions, events)
	handleTUIKey(context.Background(), &view, "delete", actions, events)
	handleTUIKey(context.Background(), &view, "end", actions, events)
	handleTUIKey(context.Background(), &view, "backspace", actions, events)
	if view.editValue != "b中de" || view.editCursor != 4 {
		t.Fatalf("boundary edit = %q at %d, want %q at 4", view.editValue, view.editCursor, "b中de")
	}
}

func TestEditValueViewportKeepsCursorVisibleForLongUnicode(t *testing.T) {
	value := "python3, ruby, dart, java, docker_cli, kubectl, 安卓应用, chrome"
	cursor := utf8.RuneCountInString("python3, ruby, dart, java, docker_cli")
	viewport := editValueViewport(value, cursor, 24)
	if DisplayWidth(viewport) > 24 {
		t.Fatalf("viewport width = %d, want <= 24: %q", DisplayWidth(viewport), viewport)
	}
	for _, marker := range []string{"‹", "│", "›"} {
		if !strings.Contains(viewport, marker) {
			t.Fatalf("viewport missing %q: %q", marker, viewport)
		}
	}
	if !strings.Contains(viewport, "docker_cli") {
		t.Fatalf("viewport does not follow cursor: %q", viewport)
	}
	for width := 1; width <= 32; width++ {
		viewport = editValueViewport(value, cursor, width)
		if DisplayWidth(viewport) > width || !strings.Contains(viewport, "│") {
			t.Fatalf("width %d produced invalid viewport %q (%d cells)", width, viewport, DisplayWidth(viewport))
		}
	}
}

func TestTUIScreenUsesAddressedRowsAndDisablesAutoWrap(t *testing.T) {
	screen := newTUIScreen(80, 24)
	rendered := screen.string()
	if strings.Contains(rendered, "\r\n") || strings.Contains(rendered, "\n") {
		t.Fatal("screen renderer must not advance rows with newlines")
	}
	for row := 1; row <= 24; row++ {
		address := "\033[" + strconv.Itoa(row) + ";1H"
		if !strings.Contains(rendered, address) {
			t.Fatalf("screen renderer missing absolute row address %q", address)
		}
	}
	if !strings.Contains(tuiEnterScreen, "\033[?7l") || !strings.Contains(tuiExitScreen, "\033[?7h") {
		t.Fatal("TUI lifecycle must disable and restore terminal auto-wrap")
	}
}

func TestTUILogScrollingAndLiveOutputAnchoring(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.width, view.height = 120, 30
	view.logFocus = true
	for index := 0; index < 40; index++ {
		view.logs = append(view.logs, "实时日志 "+strconv.Itoa(index))
	}
	events := make(chan tuiEvent, 1)
	actions := TUIActions{}

	handleTUIKey(context.Background(), &view, "pageup", actions, events)
	pageOffset := max(1, tuiLogViewportHeight(&view)-1)
	if view.logOffset != pageOffset {
		t.Fatalf("PageUp offset = %d, want %d", view.logOffset, pageOffset)
	}

	anchoredOffset := view.logOffset
	oldMaxOffset := tuiMaxLogOffset(&view)
	view.appendLog("新的实时日志")
	wantOffset := anchoredOffset + tuiMaxLogOffset(&view) - oldMaxOffset
	if view.logOffset != wantOffset {
		t.Fatalf("live output moved focused viewport: offset = %d, want %d", view.logOffset, wantOffset)
	}
	if len(view.logs) != 41 || view.logs[len(view.logs)-1] != "新的实时日志" {
		t.Fatalf("live output was not appended: %v", view.logs)
	}

	handleTUIKey(context.Background(), &view, "home", actions, events)
	if view.logOffset != tuiMaxLogOffset(&view) {
		t.Fatalf("Home offset = %d, want oldest offset %d", view.logOffset, tuiMaxLogOffset(&view))
	}
	handleTUIKey(context.Background(), &view, "end", actions, events)
	if view.logOffset != 0 {
		t.Fatalf("End offset = %d, want live tail", view.logOffset)
	}
	view.appendLog("继续跟随")
	if view.logOffset != 0 {
		t.Fatalf("live tail stopped following: offset = %d", view.logOffset)
	}
}

func TestRenderTUILogFocusShowsPositionAndDedicatedKeys(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 120, 30
	view.logFocus = true
	for index := 0; index < 30; index++ {
		view.logs = append(view.logs, "日志 "+strconv.Itoa(index))
	}
	view.logOffset = 3

	var output bytes.Buffer
	renderTUI(&output, &view)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{"实时日志", "日志焦点 · 距底部 3 行", "PAGEUP/PAGEDOWN 翻页", "END 最新"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("focused log view missing %q:\n%s", expected, plain)
		}
	}
}

func TestTUILiveLogFocusUsesSessionOutput(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 120, 30
	view.logs = []string{"实时下载第一行", "实时下载第二行"}
	actions := TUIActions{}
	events := make(chan tuiEvent, 1)

	handleTUIKey(context.Background(), &view, "l", actions, events)
	if !view.logFocus {
		t.Fatal("live log focus was not entered")
	}
	view.appendLog("聚焦期间产生的新输出")
	var focused bytes.Buffer
	renderTUI(&focused, &view)
	focusedText := stripTUIANSI(focused.String())
	for _, expected := range []string{"实时日志", "实时下载第一行", "实时下载第二行", "聚焦期间产生的新输出"} {
		if !strings.Contains(focusedText, expected) {
			t.Fatalf("focused live logs missing %q:\n%s", expected, focusedText)
		}
	}

	handleTUIKey(context.Background(), &view, "l", actions, events)
	if view.logFocus {
		t.Fatal("live log focus was not exited")
	}
	handleTUIKey(context.Background(), &view, "l", actions, events)
	if !view.logFocus || len(view.logs) != 3 {
		t.Fatalf("live log buffer was not preserved: focus=%v logs=%v", view.logFocus, view.logs)
	}
}

func TestRenderTUIApplicationManagementLayout(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 160, 50
	view.logs = []string{"14:32:11  开始下载 Obsidian", "[PROGRESS] 42MiB/144MiB CN:4 DL:8.4MiB ETA:18s"}
	var output bytes.Buffer
	renderTUI(&output, &view)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{"TendKit", "应用列表", "应用详情", "实时日志", "Obsidian", "1.13.6", "1.13.7", "SPACE 启停", "S 配置"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("render missing %q:\n%s", expected, plain)
		}
	}
	lines := strings.Split(strings.ReplaceAll(plain, "\r\n", "\n"), "\n")
	if len(lines) < view.height {
		t.Fatalf("rendered %d lines, want at least %d", len(lines), view.height)
	}
	for index, line := range lines[:view.height] {
		if DisplayWidth(line) != view.width {
			t.Fatalf("line %d width = %d, want %d", index, DisplayWidth(line), view.width)
		}
	}
}

func TestEmptyApplicationListRendersGuidanceWithoutDetails(t *testing.T) {
	useLanguage(t, i18n.English)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 120, 30
	view.catalog.Apps = nil
	view.working.Apps = nil
	var output bytes.Buffer
	renderTUI(&output, &view)
	plain := stripTUIANSI(output.String())
	for _, key := range []string{"tui.no_apps", "tui.empty_apps_body", "tui.empty_apps_scan", "tui.empty_apps_settings", "tui.empty_apps_logs", "tui.empty_apps_quit"} {
		if text := i18n.T(key); !strings.Contains(plain, text) {
			t.Fatalf("cold-start guidance missing %q:\n%s", text, plain)
		}
	}
	if strings.Contains(plain, i18n.T("tui.details")) {
		t.Fatalf("empty application details panel remained visible:\n%s", plain)
	}
	keymap := tuiCurrentKeymap(&view)
	for _, key := range []string{"ctrl+s", "s", "l", "q"} {
		if !keymap.Permits(key) {
			t.Fatalf("cold-start keymap does not permit %q", key)
		}
	}
	for _, key := range []string{"enter", "c", "a", "u", "ctrl+u", "tab", " "} {
		if keymap.Permits(key) {
			t.Fatalf("cold-start keymap still permits unavailable action %q", key)
		}
	}
}

func TestEmptyApplicationGuidanceFitsMinimumTerminal(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		useLanguage(t, language)
		t.Setenv("NO_COLOR", "1")
		view := sampleTUIView()
		view.width, view.height = 80, 24
		view.catalog.Apps = nil
		view.working.Apps = nil
		var output bytes.Buffer
		renderTUI(&output, &view)
		plain := stripTUIANSI(output.String())
		for _, key := range []string{"tui.no_apps", "tui.empty_apps_body", "tui.empty_apps_scan", "tui.empty_apps_settings", "tui.empty_apps_logs", "tui.empty_apps_quit"} {
			if text := i18n.T(key); !strings.Contains(plain, text) {
				t.Fatalf("%s minimum terminal missing %q:\n%s", language, text, plain)
			}
		}
		if strings.Contains(plain, "…") || strings.Contains(plain, i18n.T("tui.details")) {
			t.Fatalf("%s minimum terminal clipped guidance or rendered details:\n%s", language, plain)
		}
	}
}

func TestEmptyQueueMessageWrapsInsideNarrowPanel(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		for _, width := range []int{80, 100} {
			t.Run(fmt.Sprintf("%s/%d", language, width), func(t *testing.T) {
				useLanguage(t, language)
				t.Setenv("NO_COLOR", "1")
				view := sampleTUIView()
				view.width, view.height = width, 24
				view.rightQueue = true
				screen := newTUIScreen(view.width, 14)
				renderAppsPage(screen, &view, 0, 14)
				rightEdge := screen.width - 1
				for row := 1; row < 13; row++ {
					if got := screen.cells[row][rightEdge].value; got != '│' {
						t.Fatalf("queue content overwrote right border at row %d with %q", row, got)
					}
				}
				var queueText strings.Builder
				for row := 4; row < 13; row++ {
					for column := screen.width*67/100 + 1; column < rightEdge; column++ {
						if value := screen.cells[row][column].value; value != 0 && value != ' ' {
							queueText.WriteRune(value)
						}
					}
				}
				want := strings.ReplaceAll(i18n.T("tui.queue_empty"), " ", "")
				if !strings.Contains(queueText.String(), want) {
					t.Fatalf("wrapped queue guidance is incomplete: got %q want %q", queueText.String(), want)
				}
			})
		}
	}
}

func TestEmptyLogsRenderTendKitBanner(t *testing.T) {
	useLanguage(t, i18n.English)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 80, 24
	view.logs = nil
	var output bytes.Buffer
	renderTUI(&output, &view)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{"████████╗███████╗███╗   ██╗", "╚═╝   ╚══════╝╚═╝  ╚═══╝"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("empty log panel missing banner line %q:\n%s", expected, plain)
		}
	}
	if strings.Contains(plain, "No logs in this session") {
		t.Fatalf("empty log placeholder remained visible:\n%s", plain)
	}
	renderedLines := strings.Split(plain, "\n")
	canvasOrigin := -1
	for _, bannerLine := range strings.Split(i18n.Banner(), "\n") {
		content := strings.TrimLeft(bannerLine, " ")
		leading := len(bannerLine) - len(content)
		column := -1
		for _, renderedLine := range renderedLines {
			if candidate := strings.Index(renderedLine, content); candidate >= 0 {
				column = candidate - leading
				break
			}
		}
		if column < 0 {
			t.Fatalf("banner line was not rendered: %q", bannerLine)
		}
		if canvasOrigin < 0 {
			canvasOrigin = column
		} else if column != canvasOrigin {
			t.Fatalf("banner canvas origin shifted from %d to %d for %q", canvasOrigin, column, bannerLine)
		}
	}
}

func TestHomeApplicationDetailsKeepOriginalFields(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	lines, _ := applicationDetailLines(&view, 160)
	labels := make([]string, 0, len(lines))
	for _, line := range lines {
		if line.label != "" {
			labels = append(labels, line.label)
		}
	}
	want := []string{"ID", "Name", "Type", "Description", "URL", "Provider", "Status", "Enabled", "Update mode", "Current version", "Latest version", "Install path"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("home application detail labels = %v, want %v", labels, want)
	}
}

func TestApplicationTableColumnsPrioritizeOperationalFields(t *testing.T) {
	widths := applicationTableColumnWidths(78)
	want := []int{5, 14, 14, 16, 15, 12}
	if len(widths) != len(want) {
		t.Fatalf("column count = %d, want %d", len(widths), len(want))
	}
	for index := range want {
		if widths[index] != want[index] {
			t.Fatalf("column widths = %v, want %v", widths, want)
		}
	}
	if sumInts(widths) != 76 {
		t.Fatalf("column total = %d, want 76", sumInts(widths))
	}
	for _, tableWidth := range []int{51, 78, 105, 132} {
		responsive := applicationTableColumnWidths(tableWidth)
		if sumInts(responsive) != tableWidth-2 {
			t.Fatalf("width %d uses %d cells, want %d", tableWidth, sumInts(responsive), tableWidth-2)
		}
		for index, columnWidth := range responsive {
			if columnWidth < 2 {
				t.Fatalf("width %d column %d is invalid: %d", tableWidth, index, columnWidth)
			}
		}
	}

	useLanguage(t, i18n.English)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 120, 30
	var output bytes.Buffer
	renderTUI(&output, &view)
	plain := stripTUIANSI(output.String())
	for _, heading := range []string{"Update mode", "Current version", "Latest version", "Status"} {
		if !strings.Contains(plain, heading) {
			t.Fatalf("120-column table truncates heading %q:\n%s", heading, plain)
		}
	}
}

func TestRenderTUIConfigurationAndEdit(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.page = tuiConfig
	view.width, view.height = 160, 90
	view.logs = []string{"配置页不应显示这条实时日志"}
	view.configIndex = findConfigRowIndex(configRows(&view.working), "http_concurrency")
	adjustConfig(&view, 1)
	if view.working.Settings.HTTP.MaxConcurrencyPerHost != 3 || !view.dirty {
		t.Fatalf("config adjustment not applied: %#v", view.working.Settings.HTTP)
	}
	var output bytes.Buffer
	renderTUI(&output, &view)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{"配置管理", "设置与应用", "HTTP 最大并发", "Provider · github_release", "应用 · Obsidian", "CTRL+S 保存", "HTTP 版本检查请求"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("configuration render missing %q:\n%s", expected, plain)
		}
	}
	for _, hidden := range []string{"实时日志", "配置页不应显示这条实时日志"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("configuration page still renders %q:\n%s", hidden, plain)
		}
	}
	lines := strings.Split(plain, "\n")
	configBottom := view.height - 4
	if len(lines) <= configBottom || !strings.Contains(lines[configBottom], "└") || !strings.Contains(lines[configBottom], "┘") {
		t.Fatalf("configuration columns do not extend to the footer: row=%d\n%s", configBottom, plain)
	}
	view.editValue = "99"
	if err := applyConfigEdit(&view, view.editValue); err == nil {
		t.Fatal("out-of-range concurrency was accepted")
	}
}

func TestTUIConfigurationSectionsAndModifiedStylesClearAfterSave(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.page = tuiConfig
	view.width, view.height = 160, 60
	view.working.Settings.HTTP.Retries++
	view.working.Apps[0].Name = "Obsidian Next"
	view.dirty = true
	view.configIndex = 0

	visual := configVisualLines(configRows(&view.working), &view.working, &view.catalog)
	var titles []string
	for _, line := range visual {
		if line.rowIndex < 0 {
			titles = append(titles, line.title)
		}
	}
	wantTitles := []string{"基础设置", "HTTP 设置 [修改]", "下载设置", "Provider", "扫描设置", "应用 (1) [修改]"}
	if !reflect.DeepEqual(titles, wantTitles) {
		t.Fatalf("configuration section titles = %#v, want %#v", titles, wantTitles)
	}

	screen := newTUIScreen(view.width, view.height)
	renderConfigPage(screen, &view, 3, view.height-7)
	rows := configRows(&view.working)
	for visualIndex, line := range visual {
		isTarget := line.title == "HTTP 设置 [修改]" || line.title == "应用 (1) [修改]"
		if line.rowIndex >= 0 {
			key := rows[line.rowIndex].key
			isTarget = key == "http_retries" || key == "app:obsidian"
		}
		if isTarget && screen.cells[3+2+visualIndex][2].style != tuiGreen {
			t.Fatalf("modified configuration visual line %#v is not green", line)
		}
	}

	actions := TUIActions{SaveConfig: func(_ model.Config, proposed model.Config) (model.Config, error) {
		return cloneConfig(proposed), nil
	}}
	saveTUIConfig(&view, configRows(&view.working), actions)
	if view.dirty {
		t.Fatal("successful save retained the dirty state")
	}
	for _, line := range configVisualLines(configRows(&view.working), &view.working, &view.catalog) {
		if line.modified || strings.Contains(line.title, "[修改]") {
			t.Fatalf("successful save retained a modified marker: %#v", line)
		}
	}
}

func TestTUIConfigurationApplicationModifiedStateIgnoresRuntimeAndEmptySliceRepresentation(t *testing.T) {
	view := sampleTUIView()
	view.catalog.Apps[0].StatusManaged.CurrentVersion = "refreshed-at-runtime"
	if configApplicationModified(&view.working, &view.catalog, view.working.Apps[0].ID) {
		t.Fatal("runtime-only application status produced a configuration modified marker")
	}
	if view.working.Apps[0].Provider.Actions == nil {
		view.working.Apps[0].Provider.Actions = &model.ProviderActions{}
	}
	view.working.Apps[0].Provider.Actions.Download = &model.Download{ExtraArgs: []string{}}
	if configApplicationModified(&view.working, &view.catalog, view.working.Apps[0].ID) {
		t.Fatal("nil and empty download arguments produced a configuration modified marker")
	}
}

func TestTUIConfigurationSectionsScrollAtMinimumTerminalSize(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.page = tuiConfig
	view.width, view.height = 80, 24
	rows := configRows(&view.working)
	view.configIndex = len(rows) - 1
	screen := newTUIScreen(view.width, view.height)
	renderConfigPage(screen, &view, 3, view.height-7)
	if view.configScroll == 0 {
		t.Fatal("minimum terminal did not scroll to the selected application")
	}
	visual := configVisualLines(rows, &view.working, &view.catalog)
	selectedVisual := -1
	for index, line := range visual {
		if line.rowIndex == view.configIndex {
			selectedVisual = index
		}
		if line.rowIndex < 0 && index >= view.configScroll && index < view.configScroll+view.height-10 {
			rowY := 3 + 2 + index - view.configScroll
			if screen.cells[rowY][2].style == tuiSelect {
				t.Fatalf("section title at visual row %d became selectable", index)
			}
		}
	}
	selectedY := 3 + 2 + selectedVisual - view.configScroll
	if selectedVisual < 0 || selectedY >= 3+view.height-7-1 || screen.cells[selectedY][1].style != tuiSelect {
		t.Fatalf("selected application is not visible after scrolling: visual=%d scroll=%d y=%d", selectedVisual, view.configScroll, selectedY)
	}
	handleConfigKey(&view, "up", TUIActions{})
	if view.configIndex != len(rows)-2 {
		t.Fatalf("up navigation focused a visual title: index=%d", view.configIndex)
	}
}

func TestTUIConfigurationRestoresBasicSectionTitleAfterScrollAndSave(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.page = tuiConfig
	view.width, view.height = 160, 31
	rows := configRows(&view.working)
	view.configIndex = findConfigRowIndex(rows, "http_concurrency")
	view.configScroll = 1
	const panelHeight = 24
	visual := configVisualLines(rows, &view.working, &view.catalog)
	selectedVisual := 0
	for index, line := range visual {
		if line.rowIndex == view.configIndex {
			selectedVisual = index
			break
		}
	}
	visible := panelHeight - 3
	if len(visual) <= visible || selectedVisual >= visible {
		t.Fatalf("test precondition invalid: visual=%d selected=%d visible=%d", len(visual), selectedVisual, visible)
	}

	renderAndAssertTitle := func(stage string) {
		screen := newTUIScreen(view.width, view.height)
		renderConfigPage(screen, &view, 3, panelHeight)
		if view.configScroll != 0 {
			t.Fatalf("%s retained top scroll offset %d", stage, view.configScroll)
		}
		var row strings.Builder
		for _, cell := range screen.cells[5] {
			if cell.value != 0 {
				row.WriteRune(cell.value)
			}
		}
		if !strings.Contains(row.String(), "基础设置") {
			t.Fatalf("%s hid the basic settings title: %q", stage, row.String())
		}
	}
	renderAndAssertTitle("scroll")

	view.working.Settings.HTTP.MaxConcurrencyPerHost++
	view.dirty = true
	view.configScroll = 1
	actions := TUIActions{SaveConfig: func(_ model.Config, proposed model.Config) (model.Config, error) {
		return cloneConfig(proposed), nil
	}}
	saveTUIConfig(&view, configRows(&view.working), actions)
	if selected := configRows(&view.working)[view.configIndex].key; selected != "http_concurrency" {
		t.Fatalf("save changed selected configuration key to %q", selected)
	}
	renderAndAssertTitle("save")
}

func TestRenderTUIScanManagementLayoutAndConflictColors(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	view := sampleTUIView()
	view.page = tuiScan
	view.width, view.height = 160, 42
	view.catalog.Apps[0].StatusManaged = model.ManagedStatus{
		CurrentVersion: "1.13.6", FirstDetectedTime: "2026-08-15T10:00:00+08:00", UpdateStatus: model.StatusUpdateAvailable,
	}
	proposed := view.catalog.Apps[0]
	proposed.InstallPath = "/Applications/Obsidian Next.app"
	view.scanChanges = map[string]model.ScanApplicationChange{"obsidian": {
		Current: view.catalog.Apps[0], Proposed: proposed,
		Fields: []model.ScanFieldChange{{Field: "install_path", Current: view.catalog.Apps[0].InstallPath, Proposed: proposed.InstallPath}},
	}}
	view.scanProposed = map[string]model.Application{"obsidian": proposed}

	var output bytes.Buffer
	renderTUI(&output, &view)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{"扫描管理", "应用列表", "是否纳管", "加入日期", "应用详情", "配置检查", "全部合并"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("scan page missing %q:\n%s", expected, plain)
		}
	}
	if strings.Contains(plain, "配置详情") || strings.Contains(plain, "扩展信息") {
		t.Fatalf("scan page still renders duplicated configuration details:\n%s", plain)
	}
	if !strings.Contains(output.String(), "\033[38;5;208m") {
		t.Fatalf("scan conflict is not orange: %q", output.String())
	}
	if !strings.Contains(output.String(), "\033[31m") || !strings.Contains(output.String(), "\033[32m") {
		t.Fatalf("scan old/new values do not use red and green: %q", output.String())
	}
	screen := newTUIScreen(view.width, view.height)
	upperHeight, activityTop, activityHeight := stackedPageLayout(view.height, 3, 3)
	renderScanPage(screen, &view, 3, upperHeight, activityTop, activityHeight)
	leftWidth := view.width * 67 / 100
	if screen.cells[3][leftWidth-1].value != '┐' || screen.cells[3][leftWidth].value != '┌' {
		t.Fatalf("scan columns do not match home layout: left=%q right=%q", screen.cells[3][leftWidth-1].value, screen.cells[3][leftWidth].value)
	}
	if screen.cells[activityTop][0].value != '┌' || screen.cells[activityTop][view.width-1].value != '┐' {
		t.Fatalf("scan activity panel does not span the homepage width at row %d", activityTop)
	}
	if upperHeight != activityTop-3 || activityHeight != max(8, view.height*31/100) {
		t.Fatalf("scan vertical proportions differ from homepage: upper=%d activityTop=%d activityHeight=%d", upperHeight, activityTop, activityHeight)
	}
}

func TestScanApplicationListUsesColorWithoutNamePrefixes(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	conflict := view.catalog.Apps[0]
	conflict.ID, conflict.Name = "conflict", "Conflict"
	excluded := conflict
	excluded.ID, excluded.Name = "excluded", "Excluded"
	invalid := conflict
	invalid.ID, invalid.Name = "invalid", "Invalid"
	added := conflict
	added.ID, added.Name = "added", "Added"
	view.catalog.Apps = append(view.catalog.Apps, conflict, excluded, invalid)
	view.scanProposed = map[string]model.Application{
		"obsidian": view.catalog.Apps[0], conflict.ID: conflict, excluded.ID: excluded, invalid.ID: invalid, added.ID: added,
	}
	view.scanChanges = map[string]model.ScanApplicationChange{conflict.ID: {
		Current: conflict, Proposed: conflict,
		Fields: []model.ScanFieldChange{{Field: "description", Proposed: "changed"}},
	}}
	view.scanAdded = map[string]bool{added.ID: true}
	view.scanExcluded = map[string]bool{excluded.ID: true}
	view.scanObservations = map[string]model.ScanObservation{
		"obsidian": {Found: true, Path: view.catalog.Apps[0].InstallPath}, conflict.ID: {Found: true, Path: conflict.InstallPath}, excluded.ID: {Found: true, Path: excluded.InstallPath}, invalid.ID: {Found: false, Path: invalid.InstallPath}, added.ID: {Found: true, Path: added.InstallPath},
	}
	view.scanCompleted = true
	screen := newTUIScreen(120, 14)
	renderScanApplicationTable(screen, &view, 1, 1, 110, 12)
	nameX := 2 + scanTableColumnWidths(110)[0]
	for row, style := range map[int]string{5: tuiOrange, 6: tuiDim, 7: tuiDim, 8: tuiGreen} {
		if screen.cells[row][nameX].style != style {
			t.Fatalf("row %d style = %q, want %q", row, screen.cells[row][nameX].style, style)
		}
	}
	plain := stripTUIANSI(screen.string())
	for _, prefix := range []string{"! Conflict", "+ Added", "- Invalid"} {
		if strings.Contains(plain, prefix) {
			t.Fatalf("application name still contains marker %q:\n%s", prefix, plain)
		}
	}
}

func TestScanApplicationDetailsMatchHomeLabelAndStatusStyles(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.page = tuiScan
	screen := newTUIScreen(120, 36)
	renderScanApplicationDetails(screen, &view, 81, 4, 37, 28)
	if screen.cells[5][82].style != tuiDim {
		t.Fatalf("scan detail label style = %q, want dim", screen.cells[5][82].style)
	}
	lines, labelWidth := scanApplicationDetailLines(view.catalog.Apps[0], view.catalog.Apps[0].StatusManaged, 37)
	statusRow := -1
	for index, line := range lines {
		if line.label == "Status" {
			statusRow = 5 + index
			break
		}
	}
	if statusRow < 0 || screen.cells[statusRow][81+labelWidth+2].style != tuiGreen {
		t.Fatalf("scan status value does not use homepage status styling")
	}
}

func TestEmptyScanApplicationDetailsWrapsInsideNarrowPanel(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		for _, width := range []int{80, 100} {
			t.Run(fmt.Sprintf("%s/%d", language, width), func(t *testing.T) {
				useLanguage(t, language)
				view := sampleTUIView()
				view.page = tuiScan
				view.width, view.height = width, 24
				view.catalog.Apps = nil
				view.scanProposed = nil
				screen := newTUIScreen(width, 14)
				leftWidth := width * 67 / 100
				rightWidth := width - leftWidth
				screen.box(leftWidth, 0, rightWidth, 14, i18n.T("tui.scan.application_details"), tuiCyan)
				renderScanApplicationDetails(screen, &view, leftWidth+1, 1, rightWidth-2, 12)
				rightEdge := width - 1
				for row := 1; row < 13; row++ {
					if got := screen.cells[row][rightEdge].value; got != '│' {
						t.Fatalf("scan details content overwrote right border at row %d with %q", row, got)
					}
				}
				var detailsText strings.Builder
				for row := 2; row < 13; row++ {
					for column := leftWidth + 2; column < rightEdge; column++ {
						if value := screen.cells[row][column].value; value != 0 && value != ' ' {
							detailsText.WriteRune(value)
						}
					}
				}
				want := strings.ReplaceAll(i18n.T("tui.scan.empty"), " ", "")
				if !strings.Contains(detailsText.String(), want) {
					t.Fatalf("wrapped scan details guidance is incomplete: got %q want %q", detailsText.String(), want)
				}
			})
		}
	}
}

func TestEmptyScanPanelsPlaceGuidanceOnSecondContentRow(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Run(string(language), func(t *testing.T) {
			useLanguage(t, language)
			view := sampleTUIView()
			view.page = tuiScan
			view.width, view.height = 120, 30
			view.catalog.Apps = nil
			view.scanProposed = nil
			view.scanLogs = nil
			screen := newTUIScreen(view.width, view.height)
			leftWidth := view.width * 67 / 100
			rightWidth := view.width - leftWidth

			screen.box(0, 0, leftWidth, 14, i18n.T("tui.scan.app_list"), tuiCyan)
			renderScanApplicationTable(screen, &view, 1, 1, leftWidth-2, 12)
			screen.box(leftWidth, 0, rightWidth, 14, i18n.T("tui.scan.application_details"), tuiCyan)
			renderScanApplicationDetails(screen, &view, leftWidth+1, 1, rightWidth-2, 12)
			screen.box(0, 14, view.width, 12, i18n.T("tui.scan.output"), tuiCyan)
			renderScanOutput(screen, &view, 1, 15, view.width-2, 10)

			emptyRune := []rune(i18n.T("tui.scan.empty"))[0]
			outputRune := []rune(i18n.T("tui.scan.output_empty"))[0]
			for name, position := range map[string]struct {
				row, column int
				want        rune
			}{
				"application list":    {row: 2, column: 3, want: emptyRune},
				"application details": {row: 2, column: leftWidth + 2, want: emptyRune},
				"scan output":         {row: 16, column: 2, want: outputRune},
			} {
				if got := screen.cells[position.row][position.column].value; got != position.want {
					t.Fatalf("%s guidance starts with %q at row %d, want %q", name, got, position.row, position.want)
				}
			}
		})
	}
}

func TestScanApplicationDetailsOmitRedundantAndEmptyFields(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	application := view.catalog.Apps[0]
	application.Package = ""
	state := view.catalog.Apps[0].StatusManaged
	view.state.Observations["obsidian"] = model.ScanObservation{Found: true, Path: "/Applications/Detected.app"}
	state.LastCheckTime = "2026-08-15T02:27:53+08:00"
	state.DownloadPath = ""
	lines, _ := scanApplicationDetailLines(application, state, 80)
	for _, line := range lines {
		switch line.label {
		case "Detected path", "Last check", "Download path", "Package":
			t.Fatalf("unexpected application detail field %q", line.label)
		}
	}

	application.Package = "obsidian"
	state.DownloadPath = "/tmp/obsidian.dmg"
	lines, _ = scanApplicationDetailLines(application, state, 80)
	foundDownload, foundPackage := false, false
	for _, line := range lines {
		if line.label == "Download path" && strings.Contains(line.value, state.DownloadPath) {
			foundDownload = true
		}
		if line.label == "Package" && strings.Contains(line.value, application.Package) {
			foundPackage = true
		}
	}
	if !foundDownload {
		t.Fatal("non-empty download path was not rendered")
	}
	if !foundPackage {
		t.Fatal("non-empty package was not rendered")
	}
}

func TestScanApplicationDetailsUseRequestedFieldOrderAndSeparator(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	application := view.catalog.Apps[0]
	application.Package = "obsidian"
	state := view.catalog.Apps[0].StatusManaged
	state.DownloadPath = "/tmp/obsidian.dmg"
	lines, _ := scanApplicationDetailLines(application, state, 160)

	labels := make([]string, 0)
	separatorIndex := -1
	for index, line := range lines {
		if line.fullWidth && strings.Trim(line.value, "─") == "" {
			separatorIndex = index
			continue
		}
		if line.label != "" {
			labels = append(labels, line.label)
		}
	}
	want := []string{"ID", "Name", "Type", "Description", "URL", "Provider", "Package", "Status", "Enabled", "Update mode", "Current version", "Latest version", "Install path", "Scan identity", "Managed", "Added date", "Last update", "Download path"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("scan application detail labels = %v, want %v", labels, want)
	}
	if separatorIndex < 0 || separatorIndex+1 >= len(lines) || lines[separatorIndex+1].label != "Scan identity" {
		t.Fatalf("scan detail separator is not immediately before Identity: %#v", lines)
	}
}

func TestScanPartialMergeUsesInlineComparisonChecklist(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.scanPartial = true
	view.scanConfirmID = "obsidian"
	view.scanChanges = map[string]model.ScanApplicationChange{"obsidian": {
		Current: view.catalog.Apps[0], Proposed: view.catalog.Apps[0],
		Fields: []model.ScanFieldChange{
			{Field: "update_mode", Current: "check", Proposed: "download"},
			{Field: model.ApplicationFieldActionDownload, Current: "-", Proposed: strings.Repeat("candidate/", 30)},
		},
	}}
	view.partialFields = map[string]bool{}
	lines, fieldRows := scanComparisonLines(&view, view.catalog.Apps[0], 80)
	if len(fieldRows) != 2 || lines[fieldRows[0]].value != "[ ] update_mode" || lines[fieldRows[0]].valueStyle != tuiBold {
		t.Fatalf("inline field selection is not rendered in white: rows=%v lines=%#v", fieldRows, lines)
	}
	foundSeparator, foundCurrent, foundCandidate := false, false, false
	separatorCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line.value, "──") {
			foundSeparator = true
			separatorCount++
		}
		foundCurrent = foundCurrent || (line.value == "- check" && line.valueStyle == tuiRed)
		foundCandidate = foundCandidate || (line.value == "+ download" && line.valueStyle == tuiGreen)
	}
	if !foundSeparator || separatorCount != 1 || strings.HasPrefix(lines[0].value, "──") || strings.HasPrefix(lines[len(lines)-1].value, "──") || !foundCurrent || !foundCandidate {
		t.Fatalf("inline comparison styles are incomplete: %#v", lines)
	}
	handleScanPartialKey(&view, " ", TUIActions{})
	lines, _ = scanComparisonLines(&view, view.catalog.Apps[0], 80)
	if lines[fieldRows[0]].value != "[x] update_mode" {
		t.Fatalf("inline checkbox was not toggled: %#v", lines[fieldRows[0]])
	}

	view.partialIndex = 1
	screen := newTUIScreen(100, 8)
	renderScanComparison(screen, &view, 0, 0, 80, 4)
	plain := stripTUIANSI(screen.string())
	if view.partialOffset == 0 || !strings.Contains(plain, model.ApplicationFieldActionDownload) {
		t.Fatalf("selected inline field was not scrolled into view: offset=%d\n%s", view.partialOffset, plain)
	}
	if strings.Contains(plain, "A merge all") {
		t.Fatalf("comparison still contains duplicate action help:\n%s", plain)
	}
}

func TestTUIScreenEscapesUntrustedTerminalControlCharacters(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	screen := newTUIScreen(60, 3)
	payload := "safe\x1b]52;c;clipboard\a\nnext\u009b31m"
	screen.put(0, 0, payload, tuiNormal)
	output := screen.string()
	for _, forbidden := range []string{"\x1b]52;c;clipboard", "\u009b31m"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("terminal control payload was emitted: %q", output)
		}
	}
	if !strings.Contains(output, "safe�]52;c;clipboard��next�31m") {
		t.Fatalf("control characters were not rendered safely: %q", output)
	}
}

func TestTUICtrlSOnlyOpensScanPage(t *testing.T) {
	view := sampleTUIView()
	events := make(chan tuiEvent, 4)
	started := 0
	actions := TUIActions{Scan: func(_ context.Context, _ TUIScanRequest, observer TUIScanObserver) (TUIScanSnapshot, error) {
		started++
		observer.Progress("prepare", "")
		return TUIScanSnapshot{BaseConfig: view.catalog, BaseState: view.state, Config: view.catalog, State: view.state}, nil
	}}
	handleTUIKey(context.Background(), &view, "ctrl+s", actions, events)
	if view.page != tuiScan || view.scanRunning || started != 0 {
		t.Fatalf("CTRL+S should only open scan page: started=%d view=%#v", started, view)
	}
	handleScanKey(context.Background(), &view, "s", actions, events)
	for view.scanRunning {
		event := <-events
		handleTUIEvent(context.Background(), &view, event, actions, events)
	}
	if started != 1 || len(scanDisplayApps(&view)) != 1 || len(view.scanLogs) < 2 || len(view.logs) != 0 {
		t.Fatalf("explicit automatic scan did not isolate progress: started=%d scanLogs=%v homeLogs=%v view=%#v", started, view.scanLogs, view.logs, view)
	}
	handleScanKey(context.Background(), &view, "ctrl+s", actions, events)
	if started != 1 || view.scanConfirm != "" || view.scanRunning {
		t.Fatalf("CTRL+S triggered scan-page auto scan: started=%d view=%#v", started, view)
	}
	handleScanKey(context.Background(), &view, "s", actions, events)
	if view.scanConfirm != scanConfirmRescan || started != 1 {
		t.Fatalf("repeat automatic scan skipped confirmation: started=%d confirm=%q", started, view.scanConfirm)
	}
	handleTUIKey(context.Background(), &view, "enter", actions, events)
	for view.scanRunning {
		handleTUIEvent(context.Background(), &view, <-events, actions, events)
	}
	if started != 2 {
		t.Fatalf("confirmed automatic rescan count = %d, want 2", started)
	}
}

func TestTUIScanCompletionLogsDetailedStatistics(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.page = tuiScan
	conflict := view.catalog.Apps[0]
	conflict.ID, conflict.Name = "conflict", "Conflict"
	excluded := conflict
	excluded.ID, excluded.Name = "excluded", "Excluded"
	invalid := conflict
	invalid.ID, invalid.Name = "invalid", "Invalid"
	view.catalog.Apps = append(view.catalog.Apps, conflict, excluded, invalid)
	view.working = cloneConfig(view.catalog)
	added := conflict
	added.ID, added.Name = "added", "Added"
	proposedConflict := conflict
	proposedConflict.Description = "changed"
	candidateApps := append(cloneConfig(view.catalog).Apps, added)
	for index := range candidateApps {
		if candidateApps[index].ID == conflict.ID {
			candidateApps[index] = proposedConflict
		}
	}
	candidateState := cloneTUIState(view.state)
	for index := range candidateApps {
		if candidateApps[index].ID == invalid.ID {
			candidateApps[index].StatusManaged = model.ManagedStatus{UpdateStatus: model.StatusMissing}
		} else if candidateApps[index].StatusManaged.UpdateStatus == "" {
			candidateApps[index].StatusManaged.UpdateStatus = model.StatusUnchecked
		}
	}
	candidateState.Observations = map[string]model.ScanObservation{
		"obsidian": {Found: true, Path: view.catalog.Apps[0].InstallPath}, conflict.ID: {Found: true, Path: conflict.InstallPath}, excluded.ID: {Found: true, Path: excluded.InstallPath}, invalid.ID: {Found: false, Path: invalid.InstallPath}, added.ID: {Found: true, Path: added.InstallPath},
	}
	baseState := cloneTUIState(view.state)
	finishTUIScan(&view, tuiEvent{eventType: "scan_done", scan: TUIScanSnapshot{
		BaseConfig: view.catalog, BaseState: baseState,
		Config: model.Config{SchemaVersion: model.SchemaVersion, Settings: view.catalog.Settings, Apps: candidateApps}, State: candidateState,
		Changes: []model.ScanApplicationChange{{Current: conflict, Proposed: proposedConflict, Fields: []model.ScanFieldChange{{Field: "description", Proposed: "changed"}}}},
		Added:   []model.Application{added}, Excluded: []model.Application{excluded},
	}})
	want := "扫描完成：共 5 个应用，其中新增 1 个应用，1 个应用配置存在差异，1 个应用已被排除，1 个应用已失效"
	if len(view.scanLogs) == 0 || !strings.Contains(view.scanLogs[len(view.scanLogs)-1], want) || !strings.Contains(view.message, want) {
		t.Fatalf("scan completion statistics missing: message=%q logs=%v", view.message, view.scanLogs)
	}
}

func TestTUIScanLogsAreIsolatedAndClearedAcrossPages(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.logs = []string{"home operation log"}
	view.scanLogs = []string{"stale scan log"}
	handleTUIKey(context.Background(), &view, "ctrl+s", TUIActions{}, make(chan tuiEvent, 1))
	if len(view.scanLogs) != 0 || len(view.logs) != 1 {
		t.Fatalf("opening scan page did not isolate logs: scan=%v home=%v", view.scanLogs, view.logs)
	}
	view.width, view.height = 120, 32
	var output bytes.Buffer
	renderTUI(&output, &view)
	if strings.Contains(stripTUIANSI(output.String()), "home operation log") {
		t.Fatalf("scan output rendered a home log:\n%s", stripTUIANSI(output.String()))
	}
	view.scanLogs = []string{"scan-only log"}
	handleScanKey(context.Background(), &view, "esc", TUIActions{}, make(chan tuiEvent, 1))
	if view.page != tuiApps || len(view.scanLogs) != 0 || len(view.logs) != 1 {
		t.Fatalf("leaving scan page did not clear only scan logs: %#v", view)
	}
}

func TestTUIScanLogsSupportHomepageStyleFocusedScrolling(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	view.width, view.height = 100, 30
	for index := 0; index < 40; index++ {
		view.scanLogs = append(view.scanLogs, fmt.Sprintf("scan log %02d", index))
	}
	handleScanKey(context.Background(), &view, "l", TUIActions{}, make(chan tuiEvent, 1))
	if !view.scanLogFocus {
		t.Fatal("scan log focus was not enabled")
	}
	handleScanKey(context.Background(), &view, "pageup", TUIActions{}, make(chan tuiEvent, 1))
	if view.scanLogOffset == 0 {
		t.Fatal("scan log PageUp did not scroll")
	}
	anchored := view.scanLogOffset
	view.appendScanStructuredLog(LogInfo, "scanner", "new line")
	if view.scanLogOffset <= anchored {
		t.Fatalf("scan log viewport was not preserved after append: before=%d after=%d", anchored, view.scanLogOffset)
	}
	var output bytes.Buffer
	renderTUI(&output, &view)
	if !strings.Contains(stripTUIANSI(output.String()), "日志焦点") {
		t.Fatalf("focused scan log status is not rendered:\n%s", stripTUIANSI(output.String()))
	}
	handleScanKey(context.Background(), &view, "end", TUIActions{}, make(chan tuiEvent, 1))
	if view.scanLogOffset != 0 {
		t.Fatalf("scan log End offset = %d, want 0", view.scanLogOffset)
	}
}

func TestTUISingleScanSendsOnlySelectedApplication(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	events := make(chan tuiEvent, 2)
	requestedID := ""
	actions := TUIActions{Scan: func(_ context.Context, request TUIScanRequest, _ TUIScanObserver) (TUIScanSnapshot, error) {
		if request.Application != nil {
			requestedID = request.Application.ID
		}
		return TUIScanSnapshot{BaseConfig: view.catalog, BaseState: view.state, Config: view.catalog, State: view.state}, nil
	}}
	handleScanKey(context.Background(), &view, "enter", actions, events)
	if view.scanRunning || requestedID != "" {
		t.Fatalf("Enter still triggered a single scan: running=%v id=%q", view.scanRunning, requestedID)
	}
	handleScanKey(context.Background(), &view, "t", actions, events)
	for view.scanRunning {
		handleTUIEvent(context.Background(), &view, <-events, actions, events)
	}
	if requestedID != "obsidian" {
		t.Fatalf("single scan request ID = %q", requestedID)
	}
}

func TestCancelledTUIScanKeepsBaselineFirstDetectedTime(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	baseline := view.catalog.Apps[0].StatusManaged
	baseline.FirstDetectedTime = "2026-08-15T10:00:00+08:00"
	view.catalog.Apps[0].StatusManaged = baseline
	events := make(chan tuiEvent, 2)
	actions := TUIActions{Scan: func(ctx context.Context, _ TUIScanRequest, _ TUIScanObserver) (TUIScanSnapshot, error) {
		<-ctx.Done()
		return TUIScanSnapshot{}, ctx.Err()
	}}
	startTUIScan(context.Background(), &view, actions, events, "")
	if got := scanApplicationState(&view, "obsidian").FirstDetectedTime; got != baseline.FirstDetectedTime {
		t.Fatalf("first detected time while scanning = %q, want %q", got, baseline.FirstDetectedTime)
	}
	view.scanCancel()
	handleTUIEvent(context.Background(), &view, <-events, actions, events)
	if got := view.catalog.Apps[0].StatusManaged.FirstDetectedTime; got != baseline.FirstDetectedTime {
		t.Fatalf("first detected time after cancellation = %q, want %q", got, baseline.FirstDetectedTime)
	}
}

func TestCancelledTUIScanPreservesPreviousPreviewAndColors(t *testing.T) {
	view := sampleTUIView()
	view.page, view.width, view.height = tuiScan, 120, 32
	conflict := view.catalog.Apps[0]
	proposedConflict := conflict
	proposedConflict.Description = "pending change"
	neutral := conflict
	neutral.ID, neutral.Name = "neutral", "Neutral"
	view.catalog.Apps = append(view.catalog.Apps, neutral)
	view.working = cloneConfig(view.catalog)
	added := conflict
	added.ID, added.Name = "new-tool", "New Tool"
	view.scanProposed = map[string]model.Application{conflict.ID: proposedConflict, added.ID: added}
	view.scanChanges = map[string]model.ScanApplicationChange{conflict.ID: {
		Current: conflict, Proposed: proposedConflict,
		Fields: []model.ScanFieldChange{{Field: "description", Current: conflict.Description, Proposed: proposedConflict.Description}},
	}}
	view.scanAdded = map[string]bool{added.ID: true}
	view.scanSelected = 1
	beforeCatalog := cloneConfig(view.catalog)

	events := make(chan tuiEvent, 2)
	actions := TUIActions{Scan: func(ctx context.Context, _ TUIScanRequest, _ TUIScanObserver) (TUIScanSnapshot, error) {
		<-ctx.Done()
		return TUIScanSnapshot{}, ctx.Err()
	}}
	startTUIScan(context.Background(), &view, actions, events, "")
	view.scanCancel()
	handleTUIEvent(context.Background(), &view, <-events, actions, events)

	if !reflect.DeepEqual(view.catalog, beforeCatalog) || !view.scanAdded[added.ID] || !hasScanChange(&view, conflict.ID) {
		t.Fatalf("cancelled scan discarded baseline or preview: catalog=%#v added=%#v changes=%#v", view.catalog, view.scanAdded, view.scanChanges)
	}
	if candidate, found := view.scanProposed[added.ID]; !found || candidate.Name != added.Name {
		t.Fatalf("cancelled scan discarded added candidate: %#v", view.scanProposed)
	}

	screen := newTUIScreen(view.width, 14)
	renderScanApplicationTable(screen, &view, 1, 1, 110, 12)
	nameX := 2 + scanTableColumnWidths(110)[0]
	if screen.cells[4][nameX].style != tuiOrange || screen.cells[6][nameX].style != tuiGreen {
		t.Fatalf("cancelled scan lost preview colors: conflict=%q added=%q", screen.cells[4][nameX].style, screen.cells[6][nameX].style)
	}
}

func TestCancelledTUIScanAlwaysPublishesCompletion(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	parent, cancelParent := context.WithCancel(context.Background())
	events := make(chan tuiEvent, 2)
	actions := TUIActions{Scan: func(ctx context.Context, _ TUIScanRequest, _ TUIScanObserver) (TUIScanSnapshot, error) {
		<-ctx.Done()
		return TUIScanSnapshot{}, ctx.Err()
	}}
	startTUIScan(parent, &view, actions, events, "")
	cancelParent()
	select {
	case event := <-events:
		if event.eventType != "scan_done" || !errors.Is(event.err, context.Canceled) {
			t.Fatalf("cancel completion event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("scan completion was dropped after parent cancellation")
	}
}

func TestIgnoredScanDifferenceStaysResolvedAfterAutomaticScan(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	proposed := view.catalog.Apps[0]
	proposed.Description = "repeated candidate"
	change := model.ScanApplicationChange{Current: view.catalog.Apps[0], Proposed: proposed, Fields: []model.ScanFieldChange{{Field: "description", Proposed: proposed.Description}}}
	view.scanProposed = map[string]model.Application{"obsidian": proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{"obsidian": change}
	actions := TUIActions{SaveScan: func(_ model.Config, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &view, "k", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)
	resetScanPreview(&view, "")
	finishTUIScan(&view, tuiEvent{eventType: "scan_done", scan: TUIScanSnapshot{
		BaseConfig: view.catalog, BaseState: view.state,
		Config: model.Config{Apps: []model.Application{proposed}}, State: view.state,
		Changes: []model.ScanApplicationChange{change},
	}})
	if scanCandidatePending(&view, "obsidian") || hasScanChange(&view, "obsidian") {
		t.Fatalf("identical kept difference returned: changes=%v", view.scanChanges)
	}
	wantStatistics := i18n.T("tui.scan.finished", 1, 0, 0, 0, 0)
	if view.message != wantStatistics {
		t.Fatalf("resolved difference was included in completion statistics: %q", view.message)
	}
	newProposed := proposed
	newProposed.Description = "genuinely new candidate"
	newChange := model.ScanApplicationChange{Current: view.catalog.Apps[0], Proposed: newProposed, Fields: []model.ScanFieldChange{{Field: "description", Proposed: newProposed.Description}}}
	resetScanPreview(&view, "")
	finishTUIScan(&view, tuiEvent{eventType: "scan_done", scan: TUIScanSnapshot{
		BaseConfig: view.catalog, BaseState: view.state,
		Config: model.Config{Apps: []model.Application{newProposed}}, State: view.state, Changes: []model.ScanApplicationChange{newChange},
	}})
	if !scanCandidatePending(&view, "obsidian") || !hasScanChange(&view, "obsidian") {
		t.Fatalf("new difference was incorrectly suppressed: changes=%v", view.scanChanges)
	}
}

func TestIgnoredScanDifferenceStaysResolvedAfterSingleScan(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	proposed := view.catalog.Apps[0]
	proposed.Description = "repeated candidate"
	change := model.ScanApplicationChange{
		Current:  view.catalog.Apps[0],
		Proposed: proposed,
		Fields:   []model.ScanFieldChange{{Field: "description", Proposed: proposed.Description}},
	}
	view.scanProposed = map[string]model.Application{"obsidian": proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{"obsidian": change}
	actions := TUIActions{SaveScan: func(_ model.Config, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &view, "k", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)
	resetScanPreview(&view, "obsidian")
	finishTUIScan(&view, tuiEvent{eventType: "scan_done", key: "obsidian", scan: TUIScanSnapshot{
		BaseConfig: view.catalog,
		BaseState:  view.state,
		Config:     model.Config{Apps: []model.Application{proposed}},
		State:      view.state,
		Changes:    []model.ScanApplicationChange{change},
	}})
	if scanCandidatePending(&view, "obsidian") || hasScanChange(&view, "obsidian") {
		t.Fatalf("identical kept difference returned after single scan: changes=%v", view.scanChanges)
	}
}

func TestTUIScanKeepPersistsPerFieldAndRequiresSave(t *testing.T) {
	newChange := func(current model.Application, description, path string) model.ScanApplicationChange {
		proposed := current
		proposed.Description, proposed.InstallPath = description, path
		return model.ScanApplicationChange{Current: current, Proposed: proposed, Fields: []model.ScanFieldChange{
			{Field: "description", Current: current.Description, Proposed: description},
			{Field: "install_path", Current: current.InstallPath, Proposed: path},
		}}
	}
	view := sampleTUIView()
	view.page = tuiScan
	current := view.catalog.Apps[0]
	change := newChange(current, "candidate secret", "/Applications/Obsidian Next.app")
	view.scanProposed = map[string]model.Application{current.ID: change.Proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{current.ID: change}
	failure := TUIActions{SaveScan: func(model.Config, model.Config) (model.Config, error) {
		return model.Config{}, errors.New("write failed")
	}}
	handleScanKey(context.Background(), &view, "k", failure, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", failure)
	if !hasScanChange(&view, current.ID) {
		t.Fatal("failed keep save hid the pending difference")
	}
	actions := TUIActions{SaveScan: func(_ model.Config, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &view, "k", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)
	if hasScanChange(&view, current.ID) || len(view.catalog.ScanVersionControl[current.ID]) != 2 {
		t.Fatalf("successful keep was not persisted by field: %#v", view.catalog.ScanVersionControl)
	}
	for _, resolution := range view.catalog.ScanVersionControl[current.ID] {
		if resolution.Fingerprint == "" || resolution.Fingerprint == "candidate secret" {
			t.Fatalf("unsafe keep resolution: %#v", resolution)
		}
	}

	remounted := sampleTUIView()
	remounted.page, remounted.catalog, remounted.state = tuiScan, cloneConfig(view.catalog), cloneTUIState(view.state)
	finishTUIScan(&remounted, tuiEvent{eventType: "scan_done", scan: TUIScanSnapshot{
		BaseConfig: remounted.catalog, BaseState: remounted.state,
		Config: model.Config{Apps: []model.Application{change.Proposed}}, State: remounted.state,
		Changes: []model.ScanApplicationChange{change},
	}})
	if hasScanChange(&remounted, current.ID) {
		t.Fatal("persisted matching fields reappeared after remount")
	}
	changed := newChange(current, "new candidate", change.Proposed.InstallPath)
	finishTUIScan(&remounted, tuiEvent{eventType: "scan_done", scan: TUIScanSnapshot{
		BaseConfig: remounted.catalog, BaseState: remounted.state,
		Config: model.Config{Apps: []model.Application{changed.Proposed}}, State: remounted.state,
		Changes: []model.ScanApplicationChange{changed},
	}})
	remaining := remounted.scanChanges[current.ID].Fields
	if len(remaining) != 2 {
		t.Fatalf("changed scan candidate did not reappear in full: %#v", remaining)
	}
}

func TestTUIScanKeepMergesFieldsAcrossRounds(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	current := view.catalog.Apps[0]
	proposed := current
	proposed.Description, proposed.InstallPath = "kept description", "/Applications/Obsidian Next.app"
	change := model.ScanApplicationChange{Current: current, Proposed: proposed, Fields: []model.ScanFieldChange{
		{Field: "description", Current: current.Description, Proposed: proposed.Description},
		{Field: "install_path", Current: current.InstallPath, Proposed: proposed.InstallPath},
	}}
	actions := TUIActions{SaveScan: func(_ model.Config, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	view.scanProposed = map[string]model.Application{current.ID: proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{current.ID: {
		Current: current, Proposed: proposed, Fields: []model.ScanFieldChange{change.Fields[0]},
	}}
	handleScanKey(context.Background(), &view, "k", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)
	delete(view.scanIgnored, current.ID)
	view.scanProposed = map[string]model.Application{current.ID: proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{current.ID: {
		Current: current, Proposed: proposed, Fields: []model.ScanFieldChange{change.Fields[1]},
	}}
	handleScanKey(context.Background(), &view, "k", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)
	if len(view.catalog.ScanVersionControl[current.ID]) != 2 {
		t.Fatalf("second keep discarded first field: %#v", view.catalog.ScanVersionControl)
	}
}

func TestTUIScanKeepReappearsWhenScanSnapshotContextChanges(t *testing.T) {
	current := sampleTUIView().catalog.Apps[0]
	proposed := current
	proposed.Description = "kept description"
	baseline := model.ScanApplicationChange{Current: current, Proposed: proposed, Fields: []model.ScanFieldChange{{Field: "description", Current: current.Description, Proposed: proposed.Description}}}
	view := sampleTUIView()
	view.page = tuiScan
	view.scanProposed = map[string]model.Application{current.ID: proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{current.ID: baseline}
	actions := TUIActions{SaveScan: func(_ model.Config, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &view, "k", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)

	for _, test := range []struct {
		name   string
		change model.ScanApplicationChange
	}{
		{name: "current application", change: func() model.ScanApplicationChange {
			changedCurrent, changedProposed := current, proposed
			changedCurrent.Environment = map[string]string{"SCAN_CONTEXT": "current"}
			changedProposed.Environment = map[string]string{"SCAN_CONTEXT": "current"}
			return model.ScanApplicationChange{Current: changedCurrent, Proposed: changedProposed, Fields: baseline.Fields}
		}()},
		{name: "proposed application", change: func() model.ScanApplicationChange {
			changedProposed := proposed
			changedProposed.Environment = map[string]string{"SCAN_CONTEXT": "proposed"}
			return model.ScanApplicationChange{Current: current, Proposed: changedProposed, Fields: baseline.Fields}
		}()},
		{name: "new difference field", change: func() model.ScanApplicationChange {
			changedProposed := proposed
			changedProposed.InstallPath = "/Applications/Obsidian Next.app"
			return model.ScanApplicationChange{Current: current, Proposed: changedProposed, Fields: append(append([]model.ScanFieldChange(nil), baseline.Fields...), model.ScanFieldChange{Field: "install_path", Current: current.InstallPath, Proposed: changedProposed.InstallPath})}
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			remounted := sampleTUIView()
			remounted.page, remounted.catalog, remounted.state = tuiScan, cloneConfig(view.catalog), cloneTUIState(view.state)
			finishTUIScan(&remounted, tuiEvent{eventType: "scan_done", scan: TUIScanSnapshot{
				BaseConfig: remounted.catalog, BaseState: remounted.state,
				Config: model.Config{Apps: []model.Application{test.change.Proposed}}, State: remounted.state,
				Changes: []model.ScanApplicationChange{test.change},
			}})
			changed, found := remounted.scanChanges[current.ID]
			if !found || len(changed.Fields) != len(test.change.Fields) {
				t.Fatalf("changed scan context was incorrectly suppressed: %#v", remounted.scanChanges)
			}
		})
	}
}

func TestRemoveResolvedScanCandidatesDoesNotMutateFingerprintContext(t *testing.T) {
	view := sampleTUIView()
	current := view.catalog.Apps[0]
	proposed := current
	proposed.Description, proposed.InstallPath, proposed.URL = "candidate", "/Applications/Obsidian Next.app", "https://example.test/obsidian"
	fields := []model.ScanFieldChange{
		{Field: "description", Current: current.Description, Proposed: proposed.Description},
		{Field: "install_path", Current: current.InstallPath, Proposed: proposed.InstallPath},
		{Field: "url", Current: current.URL, Proposed: proposed.URL},
	}
	change := model.ScanApplicationChange{Current: current, Proposed: proposed, Fields: fields}
	view.scanChanges = map[string]model.ScanApplicationChange{current.ID: change}
	view.catalog.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{current.ID: {
		"description": {Fingerprint: model.ScanKeepFingerprint(current.ID, current, proposed, fields, fields[0])},
		"url":         {Fingerprint: model.ScanKeepFingerprint(current.ID, current, proposed, fields, fields[2])},
	}}
	removeResolvedScanCandidates(&view)
	remaining := view.scanChanges[current.ID].Fields
	if len(remaining) != 1 || remaining[0].Field != "install_path" {
		t.Fatalf("resolved filtering corrupted later fingerprint context: %#v", remaining)
	}
}

func TestTUISingleScanPreservesUnrelatedPendingCandidates(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	target := view.catalog.Apps[0]
	other := target
	other.ID, other.Name = "other", "Other"
	view.catalog.Apps = append(view.catalog.Apps, other)
	view.working = cloneConfig(view.catalog)
	otherCandidate := other
	otherCandidate.Description = "pending"
	view.scanProposed = map[string]model.Application{other.ID: otherCandidate}
	view.scanChanges = map[string]model.ScanApplicationChange{other.ID: {
		Current: other, Proposed: otherCandidate,
		Fields: []model.ScanFieldChange{{Field: "description", Proposed: otherCandidate.Description}},
	}}
	ensureScanMaps(&view)

	baseState := cloneTUIState(view.state)
	candidateConfig := cloneConfig(view.catalog)
	targetApplication, _ := findApplication(&candidateConfig, target.ID)
	targetApplication.StatusManaged.CurrentVersion = "2.0.0"
	finishTUIScan(&view, tuiEvent{eventType: "scan_done", key: target.ID, scan: TUIScanSnapshot{
		BaseConfig: view.catalog, BaseState: baseState, Config: candidateConfig, State: baseState,
	}})
	if !hasScanChange(&view, other.ID) || view.scanProposed[other.ID].Description != "pending" {
		t.Fatalf("single scan discarded unrelated candidate: changes=%v proposed=%v", view.scanChanges, view.scanProposed)
	}
}

func TestTUISingleScanStatusWriteThroughDoesNotInvalidateOtherApplications(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	target := view.catalog.Apps[0]
	other := target
	other.ID, other.Name = "other", "Other"
	view.catalog.Apps = append(view.catalog.Apps, other)
	view.working = cloneConfig(view.catalog)
	view.scanCompleted = true
	view.scanProposed = applicationMap(view.catalog.Apps)
	view.scanObservations = map[string]model.ScanObservation{
		target.ID: {Found: true, Path: target.InstallPath},
		other.ID:  {Found: true, Path: other.InstallPath},
	}
	view.state.Observations = cloneScanObservations(view.scanObservations)
	ensureScanMaps(&view)

	baseConfig := cloneConfig(view.catalog)
	baseTarget, _ := findApplication(&baseConfig, target.ID)
	baseTarget.StatusManaged.CurrentVersion = "2.0.0"
	candidateConfig := cloneConfig(baseConfig)
	finishTUIScan(&view, tuiEvent{eventType: "scan_done", key: target.ID, scan: TUIScanSnapshot{
		BaseConfig: baseConfig,
		BaseState: model.RuntimeState{Observations: map[string]model.ScanObservation{
			target.ID: {Found: true, Path: target.InstallPath},
		}},
		Config: candidateConfig,
		State: model.RuntimeState{Observations: map[string]model.ScanObservation{
			target.ID: {Found: true, Path: target.InstallPath},
		}},
	}})

	if scanApplicationInvalid(&view, other.ID) {
		t.Fatal("single scan marked an unrelated application invalid")
	}
	if observation, found := view.scanObservations[other.ID]; !found || !observation.Found {
		t.Fatalf("single scan discarded unrelated observation: %#v", view.scanObservations)
	}
	if observation, found := view.state.Observations[other.ID]; !found || !observation.Found {
		t.Fatalf("single scan replaced complete runtime observations with a target delta: %#v", view.state.Observations)
	}
	if statistics := currentScanStatistics(&view); statistics.invalid != 0 {
		t.Fatalf("single scan invalid statistics = %d", statistics.invalid)
	}
	if want := i18n.T("tui.scan.finished", 1, 0, 0, 0, 0); view.message != want {
		t.Fatalf("single scan completion message = %q, want %q", view.message, want)
	}
}

func TestTUIScanKIsReservedForKeepInsteadOfNavigation(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	other := view.catalog.Apps[0]
	other.ID, other.Name = "other", "Other"
	view.catalog.Apps = append(view.catalog.Apps, other)
	view.scanSelected = 1
	handleScanKey(context.Background(), &view, "k", TUIActions{}, make(chan tuiEvent, 1))
	if view.scanSelected != 1 || view.scanConfirm != "" || !strings.Contains(view.message, i18n.T("tui.scan.no_conflict")) {
		t.Fatalf("k was not reserved for keep: selected=%d confirm=%q message=%q", view.scanSelected, view.scanConfirm, view.message)
	}
}

func TestTUIScanMutationKeysRequireConfirmation(t *testing.T) {
	for _, key := range []string{"d", "x", "a"} {
		view := sampleTUIView()
		view.page = tuiScan
		view.scanChanges = map[string]model.ScanApplicationChange{"obsidian": {
			Current: view.catalog.Apps[0], Proposed: view.catalog.Apps[0],
			Fields: []model.ScanFieldChange{{Field: "description", Proposed: "candidate"}},
		}}
		view.scanProposed = map[string]model.Application{"obsidian": view.catalog.Apps[0]}
		saves := 0
		actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
			saves++
			return catalog, nil
		}}
		handleScanKey(context.Background(), &view, key, actions, make(chan tuiEvent, 1))
		if saves != 0 || view.scanConfirm == "" {
			t.Fatalf("key %q changed configuration without confirmation: saves=%d confirm=%q", key, saves, view.scanConfirm)
		}
	}
}

func TestTUIScanManagedToggleSavesImmediately(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	saves := 0
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		saves++
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &view, "m", actions, make(chan tuiEvent, 1))
	if saves != 1 || view.scanConfirm != "" || !view.catalog.Apps[0].ScanManaged {
		t.Fatalf("managed toggle was not saved directly: saves=%d confirm=%q app=%#v", saves, view.scanConfirm, view.catalog.Apps[0])
	}
}

func TestTUIScanManagedToggleClearsKeepsOnlyWhenUnmanaging(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	view.catalog.Apps[0].ScanManaged = true
	view.working = cloneConfig(view.catalog)
	view.catalog.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{"obsidian": {}, "other": {}}
	actions := TUIActions{SaveScan: func(_ model.Config, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &view, "m", actions, make(chan tuiEvent, 1))
	if view.catalog.ScanVersionControl["obsidian"] != nil || view.catalog.ScanVersionControl["other"] == nil {
		t.Fatalf("unmanage did not clear only the target keeps: %#v", view.catalog.ScanVersionControl)
	}
	view.catalog.ScanVersionControl["obsidian"] = map[string]model.ScanKeepResolution{}
	handleScanKey(context.Background(), &view, "m", actions, make(chan tuiEvent, 1))
	if view.catalog.ScanVersionControl["obsidian"] == nil {
		t.Fatalf("manage unexpectedly cleared keeps: %#v", view.catalog.ScanVersionControl)
	}
}

func TestTUIScanManagedToggleKeepsUnrelatedDifferences(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	proposed := view.catalog.Apps[0]
	proposed.ScanManaged = true
	proposed.InstallPath = "/Applications/Obsidian Next.app"
	view.scanProposed = map[string]model.Application{"obsidian": proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{"obsidian": {
		Current: view.catalog.Apps[0], Proposed: proposed,
		Fields: []model.ScanFieldChange{
			{Field: "scan_managed", Current: "false", Proposed: "true"},
			{Field: "install_path", Current: view.catalog.Apps[0].InstallPath, Proposed: proposed.InstallPath},
		},
	}}
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &view, "m", actions, make(chan tuiEvent, 1))
	change := view.scanChanges["obsidian"]
	if len(change.Fields) != 1 || change.Fields[0].Field != "install_path" || view.scanProposed["obsidian"].ScanManaged != view.catalog.Apps[0].ScanManaged {
		t.Fatalf("managed toggle discarded or can overwrite unrelated differences: %#v proposed=%#v", change, view.scanProposed["obsidian"])
	}
}

func TestTUIScanSelectionKeepsUnselectedAppsAndSupportsPartialMerge(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	current := view.catalog.Apps[0]
	proposed := current
	proposed.InstallPath = "/Applications/New.app"
	proposed.Description = "New description"
	view.scanProposed = map[string]model.Application{current.ID: proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{current.ID: {
		Current: current, Proposed: proposed,
		Fields: []model.ScanFieldChange{
			{Field: "description", Current: current.Description, Proposed: proposed.Description},
			{Field: "install_path", Current: current.InstallPath, Proposed: proposed.InstallPath},
		},
	}}
	view.scanConfirmID = current.ID
	view.scanPartial = true
	view.partialFields = map[string]bool{"description": true}
	saves := 0
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		saves++
		return cloneConfig(catalog), nil
	}}

	handleScanPartialKey(&view, "enter", actions)
	if view.scanConfirm != scanConfirmPartial || saves != 0 {
		t.Fatalf("partial merge skipped confirmation: confirm=%q saves=%d", view.scanConfirm, saves)
	}
	handleScanConfirmationKey(&view, "enter", actions)
	remaining := view.scanChanges[current.ID]
	if saves != 1 || view.scanPartial || view.catalog.Apps[0].Description != proposed.Description || view.catalog.Apps[0].InstallPath != current.InstallPath || len(remaining.Fields) != 1 || remaining.Fields[0].Field != "install_path" {
		t.Fatalf("partial merge result: saves=%d view=%#v", saves, view)
	}
}

func TestTUIScanConflictSupportsMergeAllAndKeepCurrent(t *testing.T) {
	newView := func() tuiModel {
		view := sampleTUIView()
		view.page = tuiScan
		proposed := view.catalog.Apps[0]
		proposed.InstallPath = "/Applications/Obsidian New.app"
		view.scanProposed = map[string]model.Application{"obsidian": proposed}
		view.scanChanges = map[string]model.ScanApplicationChange{"obsidian": {
			Current: view.catalog.Apps[0], Proposed: proposed,
			Fields: []model.ScanFieldChange{{Field: "install_path", Current: view.catalog.Apps[0].InstallPath, Proposed: proposed.InstallPath}},
		}}
		return view
	}

	kept := newView()
	keepActions := TUIActions{SaveScan: func(_ model.Config, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &kept, "k", keepActions, make(chan tuiEvent, 1))
	if kept.scanConfirm != scanConfirmKeep || !hasScanChange(&kept, "obsidian") {
		t.Fatalf("keep current skipped confirmation: %#v", kept)
	}
	handleScanConfirmationKey(&kept, "enter", keepActions)
	if hasScanChange(&kept, "obsidian") || kept.catalog.Apps[0].InstallPath != "/Applications/Obsidian.app" {
		t.Fatalf("keep current changed catalog: %#v", kept)
	}
	partial := newView()
	handleScanKey(context.Background(), &partial, "p", TUIActions{}, make(chan tuiEvent, 1))
	if !partial.scanPartial || partial.scanConfirmID != "obsidian" {
		t.Fatalf("partial merge was not opened for selected conflict: %#v", partial)
	}

	merged := newView()
	merged.scanConfirm = scanConfirmMerge
	merged.scanConfirmID = "obsidian"
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanConfirmationKey(&merged, "enter", actions)
	if merged.catalog.Apps[0].InstallPath != "/Applications/Obsidian New.app" || hasScanChange(&merged, "obsidian") {
		t.Fatalf("merge all did not apply candidate: %#v", merged)
	}
}

func TestTUIScanConflictKeysIgnoreApplicationsWithoutDifferences(t *testing.T) {
	for _, key := range []string{"a", "p", "k"} {
		view := sampleTUIView()
		view.page = tuiScan
		other := view.catalog.Apps[0]
		other.ID, other.Name = "other", "Other"
		candidate := other
		candidate.Description = "changed elsewhere"
		view.catalog.Apps = append(view.catalog.Apps, other)
		view.scanProposed = map[string]model.Application{other.ID: candidate}
		view.scanChanges = map[string]model.ScanApplicationChange{other.ID: {
			Current: other, Proposed: candidate,
			Fields: []model.ScanFieldChange{{Field: "description", Current: other.Description, Proposed: candidate.Description}},
		}}
		originalApps := len(scanDisplayApps(&view))
		handleScanKey(context.Background(), &view, key, TUIActions{}, make(chan tuiEvent, 1))
		if view.scanConfirm != "" || view.scanPartial || view.scanIgnored["obsidian"] {
			t.Fatalf("conflict key %q affected an application without differences: %#v", key, view)
		}
		if len(scanDisplayApps(&view)) != originalApps {
			t.Fatalf("conflict key %q changed the application list", key)
		}
	}
	for _, key := range []string{"a", "p", "k"} {
		view := sampleTUIView()
		view.page = tuiScan
		candidate := view.catalog.Apps[0]
		candidate.Description = "candidate"
		view.scanProposed = map[string]model.Application{candidate.ID: candidate}
		view.scanChanges = map[string]model.ScanApplicationChange{candidate.ID: {
			Current: view.catalog.Apps[0], Proposed: candidate,
			Fields: []model.ScanFieldChange{{Field: "description", Proposed: candidate.Description}},
		}}
		view.detailFocus = true
		handleScanKey(context.Background(), &view, key, TUIActions{}, make(chan tuiEvent, 1))
		if view.scanConfirm != "" || view.scanPartial {
			t.Fatalf("conflict key %q was active outside the application-list focus", key)
		}
	}
}

func TestTUIScanNewCandidateCanBeEditedAddedOrExcluded(t *testing.T) {
	useLanguage(t, i18n.English)
	newView := func() tuiModel {
		view := sampleTUIView()
		view.page = tuiScan
		candidate := model.Application{
			ID: "new-tool", Name: "New Tool", Type: model.ApplicationTypeCLI,
			Description: "candidate", InstallPath: "/tmp/new-tool", Enabled: true,
			UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: true,
		}
		view.scanProposed = map[string]model.Application{candidate.ID: candidate}
		view.scanAdded = map[string]bool{candidate.ID: true}
		view.scanSelected = 1
		ensureScanMaps(&view)
		return view
	}

	view := newView()
	candidate := view.scanProposed["new-tool"]
	lines, fieldRows := scanComparisonLines(&view, candidate, 100)
	if len(fieldRows) != len(applicationConfigRows(&model.Config{Apps: []model.Application{candidate}}, candidate.ID)) || len(lines) < len(fieldRows) {
		t.Fatalf("new candidate does not show its complete configuration: rows=%d lines=%d", len(fieldRows), len(lines))
	}
	handleScanKey(context.Background(), &view, "e", TUIActions{}, make(chan tuiEvent, 1))
	handleScanKey(context.Background(), &view, "s", TUIActions{}, make(chan tuiEvent, 1))
	if view.scanConfirm != "" || view.scanRunning || view.messageError {
		t.Fatalf("scan shortcut was active while candidate configuration had focus: %#v", view)
	}
	rows := scanCandidateConfigRows(&view)
	lines, fieldRows = scanComparisonLines(&view, candidate, 100)
	if lines[fieldRows[0]].valueStyle != tuiDim {
		t.Fatalf("read-only candidate field style = %q, want dim", lines[fieldRows[0]].valueStyle)
	}
	handleScanCandidateConfigKey(&view, "enter")
	if view.editing || !strings.Contains(view.message, i18n.T("tui.readonly")) {
		t.Fatalf("read-only candidate field entered edit mode: %#v", view)
	}
	for index, row := range rows {
		if row.field == "name" {
			view.scanFieldIndex = index
			break
		}
	}
	var screen = newTUIScreen(120, 24)
	renderScanComparison(screen, &view, 0, 0, 100, 20)
	if screen.cells[fieldRows[view.scanFieldIndex]][0].style != tuiSelect {
		t.Fatalf("selected candidate field does not use row background highlighting")
	}
	view.width, view.height = 120, 36
	var focused bytes.Buffer
	renderTUI(&focused, &view)
	focusTitle := i18n.T("tui.scan.diff") + " · [" + i18n.T("tui.scan.edit_focus") + "]"
	if !strings.Contains(stripTUIANSI(focused.String()), focusTitle) {
		t.Fatalf("candidate edit focus is not shown in panel title:\n%s", stripTUIANSI(focused.String()))
	}
	handleScanCandidateConfigKey(&view, "enter")
	view.editValue = "  Edited Tool  "
	handleTUIKey(context.Background(), &view, "enter", TUIActions{}, make(chan tuiEvent, 1))
	if view.scanEditDraft.Name != "Edited Tool" || view.scanProposed["new-tool"].Name != "New Tool" {
		t.Fatalf("candidate edit did not remain isolated in the draft: draft=%#v staged=%#v", view.scanEditDraft, view.scanProposed["new-tool"])
	}
	handleScanCandidateConfigKey(&view, "ctrl+s")
	if view.scanProposed["new-tool"].Name != "Edited Tool" {
		t.Fatalf("candidate edit was not staged by CTRL+S: %#v", view.scanProposed["new-tool"])
	}
	handleScanCandidateConfigKey(&view, "enter")
	view.editValue = "   "
	handleTUIKey(context.Background(), &view, "enter", TUIActions{}, make(chan tuiEvent, 1))
	if !view.editing || !view.messageError || view.scanProposed["new-tool"].Name != "Edited Tool" {
		t.Fatalf("empty required candidate value was accepted: %#v", view)
	}
	view.editing = false
	for index, row := range rows {
		if row.field == "update_mode" {
			view.scanFieldIndex = index
			break
		}
	}
	handleScanCandidateConfigKey(&view, "enter")
	if view.editing || !strings.Contains(view.message, i18n.T("tui.scan.enum_keys_only")) {
		t.Fatalf("enum candidate field entered text editing: %#v", view)
	}
	view.editing = true
	view.editValue = strings.Repeat("x", tuiMaxEditValueBytes)
	view.editCursor = len([]rune(view.editValue))
	handleTUIKey(context.Background(), &view, "y", TUIActions{}, make(chan tuiEvent, 1))
	if len(view.editValue) != tuiMaxEditValueBytes || !view.messageError {
		t.Fatalf("oversized candidate input was accepted: length=%d", len(view.editValue))
	}
	handleTUIKey(context.Background(), &view, "\x01", TUIActions{}, make(chan tuiEvent, 1))
	if len(view.editValue) != tuiMaxEditValueBytes || !strings.Contains(view.message, i18n.T("tui.edit_control_character")) {
		t.Fatalf("control-character candidate input was accepted: %q", view.message)
	}
	view.editing = false
	handleScanCandidateConfigKey(&view, "esc")
	handleScanKey(context.Background(), &view, "J", TUIActions{}, make(chan tuiEvent, 1))
	if view.scanConfirm != "" {
		t.Fatalf("uppercase J triggered the lowercase j add action: %#v", view)
	}
	handleScanKey(context.Background(), &view, "j", TUIActions{}, make(chan tuiEvent, 1))
	if view.scanConfirm != scanConfirmAdd {
		t.Fatalf("new candidate add skipped confirmation: %#v", view)
	}
	saves := 0
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		saves++
		return cloneConfig(catalog), nil
	}}
	handleScanConfirmationKey(&view, "enter", actions)
	added, found := findApplicationValue(view.catalog.Apps, "new-tool")
	if saves != 1 || !found || added.Name != "Edited Tool" {
		t.Fatalf("edited candidate was not saved after confirmation: saves=%d app=%#v", saves, added)
	}

	excluded := newView()
	handleScanKey(context.Background(), &excluded, "x", actions, make(chan tuiEvent, 1))
	if excluded.scanConfirm != scanConfirmExclude {
		t.Fatalf("new candidate exclusion skipped confirmation: %#v", excluded)
	}
	handleScanConfirmationKey(&excluded, "enter", actions)
	if len(excluded.catalog.Settings.Scan.Exclude) != 1 || excluded.catalog.Settings.Scan.Exclude[0] != "new-tool" || excluded.scanAdded["new-tool"] || len(scanDisplayApps(&excluded)) != 1 {
		t.Fatalf("excluded new candidate was not removed from the candidate list: %#v", excluded)
	}
	if _, found := excluded.scanProposed["new-tool"]; found || excluded.scanExcluded["new-tool"] || excluded.scanIgnored["new-tool"] {
		t.Fatalf("excluded new candidate retained temporary state: %#v", excluded)
	}

	invalid := newView()
	invalidCandidate := invalid.scanProposed["new-tool"]
	invalidCandidate.InstallPath = "   "
	invalid.scanProposed["new-tool"] = invalidCandidate
	invalidSaves := 0
	invalidActions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		invalidSaves++
		return catalog, nil
	}}
	handleScanKey(context.Background(), &invalid, "j", invalidActions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&invalid, "enter", invalidActions)
	if invalidSaves != 0 || !invalid.messageError || !strings.Contains(invalid.message, i18n.T("label.install_path")) {
		t.Fatalf("invalid candidate bypassed final validation: saves=%d message=%q", invalidSaves, invalid.message)
	}
}

func TestTUIScanExistingApplicationExclusionKeepsCatalogRecord(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.page, view.width, view.height = tuiScan, 120, 36
	view.catalog.Apps[0].ID = "existing-app-id"
	view.working = cloneConfig(view.catalog)
	ensureScanMaps(&view)
	before := cloneConfig(view.catalog)
	saves := 0
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		saves++
		return cloneConfig(catalog), nil
	}}

	handleTUIKey(context.Background(), &view, "x", actions, make(chan tuiEvent, 1))
	if view.scanConfirm != scanConfirmExclude {
		t.Fatalf("existing application exclusion skipped confirmation: %#v", view)
	}
	if prompt := scanConfirmationPrompt(&view, view.catalog.Apps[0]); prompt != "Add Obsidian to the exclusion list?" {
		t.Fatalf("existing application exclusion prompt = %q", prompt)
	}
	handleTUIKey(context.Background(), &view, "enter", actions, make(chan tuiEvent, 1))
	if saves != 1 || len(view.catalog.Apps) != len(before.Apps) || len(scanDisplayApps(&view)) != len(before.Apps) {
		t.Fatalf("existing application exclusion changed the application list: saves=%d view=%#v", saves, view)
	}
	if application, found := findApplicationValue(view.catalog.Apps, before.Apps[0].ID); !found || application.Name != before.Apps[0].Name {
		t.Fatalf("existing application was deleted or changed by exclusion: %#v", view.catalog.Apps)
	}
	if len(view.catalog.Settings.Scan.Exclude) != 1 || view.catalog.Settings.Scan.Exclude[0] != before.Apps[0].ID || !view.scanExcluded[before.Apps[0].ID] {
		t.Fatalf("existing application exclusion was not persisted: %#v", view)
	}
	if keymap := tuiCurrentKeymap(&view); !keymap.Permits("x") {
		t.Fatalf("excluded existing application no longer permits X toggle: %#v", keymap)
	}
	handleTUIKey(context.Background(), &view, "x", actions, make(chan tuiEvent, 1))
	if saves != 2 || view.scanConfirm != "" || len(view.catalog.Settings.Scan.Exclude) != 0 || view.scanExcluded[before.Apps[0].ID] || len(view.catalog.Apps) != len(before.Apps) || len(scanDisplayApps(&view)) != len(before.Apps) {
		t.Fatalf("existing application exclusion was not cancelled immediately: saves=%d view=%#v", saves, view)
	}
}

func TestTUIScanAddAllCandidatesRequiresConfirmationAndSavesOnce(t *testing.T) {
	useLanguage(t, i18n.English)
	view := scanAddAllTestView()
	view.width, view.height = 160, 24
	screen := newTUIScreen(view.width, view.height)
	renderFooter(screen, &view, view.height-3, 3)
	footer := strings.Split(stripTUIANSI(screen.string()), "\n")[view.height-2]
	addAll := strings.Index(footer, "A Add all")
	addOne := strings.Index(footer, "J Add")
	if addAll < 0 || addOne < 0 || addAll >= addOne {
		t.Fatalf("add-all shortcut was not rendered before add: %q", footer)
	}

	saves := 0
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		saves++
		return cloneConfig(catalog), nil
	}}
	handleScanKey(context.Background(), &view, "a", actions, make(chan tuiEvent, 1))
	if saves != 0 || view.scanConfirm != "add_all" {
		t.Fatalf("add all skipped confirmation: saves=%d confirm=%q", saves, view.scanConfirm)
	}
	handleScanConfirmationKey(&view, "esc", actions)
	if saves != 0 || len(view.catalog.Apps) != 1 {
		t.Fatalf("cancelled add all changed configuration: saves=%d apps=%#v", saves, view.catalog.Apps)
	}

	handleScanKey(context.Background(), &view, "a", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)
	for _, id := range []string{"new-alpha", "new-beta"} {
		if _, found := findApplicationValue(view.catalog.Apps, id); !found || view.scanAdded[id] || !view.scanIgnored[id] {
			t.Fatalf("candidate %s was not resolved by add all: apps=%#v added=%#v ignored=%#v", id, view.catalog.Apps, view.scanAdded, view.scanIgnored)
		}
		if _, found := findApplicationValue(view.catalog.Apps, id); !found {
			t.Fatalf("candidate %s was not saved: %#v", id, view.catalog.Apps)
		}
	}
	if saves != 1 || len(view.catalog.Apps) != 3 || view.catalog.Apps[0].Description != "current" {
		t.Fatalf("add all was not one isolated save: saves=%d apps=%#v", saves, view.catalog.Apps)
	}
}

func TestTUIScanAddAllCandidatesRejectsWholeBatchWhenOneIsInvalid(t *testing.T) {
	view := scanAddAllTestView()
	invalid := view.scanProposed["new-beta"]
	invalid.InstallPath = "   "
	view.scanProposed[invalid.ID] = invalid
	saves := 0
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		saves++
		return catalog, nil
	}}
	handleScanKey(context.Background(), &view, "a", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)
	if saves != 0 || len(view.catalog.Apps) != 1 || !view.scanAdded["new-alpha"] || !view.scanAdded["new-beta"] || !view.messageError {
		t.Fatalf("invalid add-all batch was partially saved: saves=%d apps=%#v added=%#v message=%q", saves, view.catalog.Apps, view.scanAdded, view.message)
	}
}

func TestTUIScanAddAllCandidatesKeepsWholeBatchWhenSaveFails(t *testing.T) {
	view := scanAddAllTestView()
	actions := TUIActions{SaveScan: func(_ model.Config, _ model.Config) (model.Config, error) {
		return model.Config{}, errors.New("save failed")
	}}
	handleScanKey(context.Background(), &view, "a", actions, make(chan tuiEvent, 1))
	handleScanConfirmationKey(&view, "enter", actions)
	if len(view.catalog.Apps) != 1 || !view.scanAdded["new-alpha"] || !view.scanAdded["new-beta"] || !view.messageError || !strings.Contains(view.message, "save failed") {
		t.Fatalf("failed add-all save changed candidates: apps=%#v added=%#v message=%q", view.catalog.Apps, view.scanAdded, view.message)
	}
}

func scanAddAllTestView() tuiModel {
	view := sampleTUIView()
	view.page = tuiScan
	view.catalog.Apps[0].Description = "current"
	view.working = cloneConfig(view.catalog)
	conflict := view.catalog.Apps[0]
	conflict.Description = "candidate"
	alpha := model.Application{ID: "new-alpha", Name: "Alpha", Type: model.ApplicationTypeCLI, InstallPath: "/tmp/alpha", Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: true}
	beta := model.Application{ID: "new-beta", Name: "Beta", Type: model.ApplicationTypeCLI, InstallPath: "/tmp/beta", Enabled: true, UpdateMode: model.ModeCheck, Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: true}
	view.scanProposed = map[string]model.Application{conflict.ID: conflict, alpha.ID: alpha, beta.ID: beta}
	view.scanChanges = map[string]model.ScanApplicationChange{conflict.ID: {
		Current: view.catalog.Apps[0], Proposed: conflict,
		Fields: []model.ScanFieldChange{{Field: "description", Current: "current", Proposed: "candidate"}},
	}}
	view.scanAdded = map[string]bool{alpha.ID: true, beta.ID: true}
	view.scanSelected = 1
	ensureScanMaps(&view)
	return view
}

func TestTUIScanConflictActionsAllRequireConfirmation(t *testing.T) {
	newView := func() tuiModel {
		view := sampleTUIView()
		view.page = tuiScan
		candidate := view.catalog.Apps[0]
		candidate.Description = "candidate"
		view.scanProposed = map[string]model.Application{candidate.ID: candidate}
		view.scanChanges = map[string]model.ScanApplicationChange{candidate.ID: {
			Current: view.catalog.Apps[0], Proposed: candidate,
			Fields: []model.ScanFieldChange{{Field: "description", Proposed: candidate.Description}},
		}}
		return view
	}
	for _, key := range []string{"A", "P", "K"} {
		view := newView()
		handleScanKey(context.Background(), &view, key, TUIActions{}, make(chan tuiEvent, 1))
		if view.scanConfirm != "" || view.scanPartial {
			t.Fatalf("uppercase conflict shortcut %q was accepted: %#v", key, view)
		}
	}

	for _, item := range []struct {
		key     string
		confirm string
	}{
		{key: "a", confirm: scanConfirmMerge},
		{key: "k", confirm: scanConfirmKeep},
	} {
		view := newView()
		handleScanKey(context.Background(), &view, item.key, TUIActions{}, make(chan tuiEvent, 1))
		if view.scanConfirm != item.confirm || !hasScanChange(&view, "obsidian") {
			t.Fatalf("action %s changed conflict without confirmation: %#v", item.key, view)
		}
	}
	partial := newView()
	handleScanKey(context.Background(), &partial, "p", TUIActions{}, make(chan tuiEvent, 1))
	handleScanPartialKey(&partial, " ", TUIActions{})
	handleScanPartialKey(&partial, "enter", TUIActions{})
	if partial.scanConfirm != scanConfirmPartial || !hasScanChange(&partial, "obsidian") {
		t.Fatalf("partial merge changed conflict without confirmation: %#v", partial)
	}
}

func TestTUIScanDeleteRequiresTwoConfirmationsAndCanAddExclusion(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	view.catalog.Apps[0].ID = "app-obsidian"
	view.scanConfirm = scanConfirmDelete
	view.scanConfirmID = "app-obsidian"
	saves := 0
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		saves++
		return cloneConfig(catalog), nil
	}}

	handleScanConfirmationKey(&view, "enter", actions)
	if view.scanConfirm != scanConfirmDeleteExclude || saves != 0 {
		t.Fatalf("delete skipped exclusion confirmation: confirm=%q saves=%d", view.scanConfirm, saves)
	}
	handleScanConfirmationKey(&view, "enter", actions)
	if saves != 1 || len(view.catalog.Apps) != 0 || len(view.catalog.Settings.Scan.Exclude) != 1 || view.catalog.Settings.Scan.Exclude[0] != "app-obsidian" {
		t.Fatalf("confirmed delete result: catalog=%#v state=%#v", view.catalog, view.state)
	}
}

func TestTUIScanDeleteWithoutExclusionCanBeRediscovered(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	deleted := view.catalog.Apps[0]
	view.scanConfirm = scanConfirmDelete
	view.scanConfirmID = deleted.ID
	actions := TUIActions{SaveScan: func(_, catalog model.Config) (model.Config, error) {
		return cloneConfig(catalog), nil
	}}
	handleScanConfirmationKey(&view, "enter", actions)
	handleScanConfirmationKey(&view, "esc", actions)
	if len(view.catalog.Apps) != 0 || len(view.catalog.Settings.Scan.Exclude) != 0 {
		t.Fatalf("delete without exclusion result: %#v", view.catalog)
	}
	resetScanPreview(&view, "")
	state := model.ManagedStatus{CurrentVersion: "1.13.7"}
	deleted.StatusManaged = state
	finishTUIScan(&view, tuiEvent{eventType: "scan_done", scan: TUIScanSnapshot{
		BaseConfig: view.catalog, BaseState: view.state,
		Config: model.Config{Apps: []model.Application{deleted}}, State: model.RuntimeState{Observations: map[string]model.ScanObservation{deleted.ID: {Found: true, Path: deleted.InstallPath}}},
		Added: []model.Application{deleted},
	}})
	if !view.scanAdded[deleted.ID] || len(scanDisplayApps(&view)) != 1 {
		t.Fatalf("deleted non-excluded application was not rediscovered: added=%v apps=%v", view.scanAdded, scanDisplayApps(&view))
	}
}

func TestTUIScanIdentityChecksImplicitGlobalIdentity(t *testing.T) {
	view := sampleTUIView()
	view.catalog.Apps = append(view.catalog.Apps, model.Application{ID: "other", Name: "Obsidian", Type: model.ApplicationTypeCLI})
	view.working = cloneConfig(view.catalog)
	actions := TUIActions{GenerateIdentity: func(application model.Application) (string, error) {
		return "cli:" + strings.ToLower(application.Name), nil
	}}
	beginIdentityConfirmation(&view, actions)
	if view.scanConfirm != "" || !view.messageError || !strings.Contains(view.message, "cli:obsidian") {
		t.Fatalf("identity collision was not rejected: %#v", view)
	}
}

func TestTUIScanIdentityBindingOnlyAppearsWhenIdentityIsMissing(t *testing.T) {
	view := sampleTUIView()
	view.page, view.width, view.height = tuiScan, 120, 36
	generated := 0
	actions := TUIActions{GenerateIdentity: func(model.Application) (string, error) {
		generated++
		return "bundle:generated", nil
	}}

	missing := tuiCurrentKeymap(&view)
	if !missing.Permits("i") || !strings.Contains(strings.Join(missing.FooterLines(view.width), " "), i18n.T("tui.key.identity")) {
		t.Fatalf("identity-missing application does not expose I: %#v", missing)
	}

	view.catalog.Apps[0].Identity = "bundle:existing"
	view.working = cloneConfig(view.catalog)
	existing := tuiCurrentKeymap(&view)
	if existing.Permits("i") || strings.Contains(strings.Join(existing.FooterLines(view.width), " "), i18n.T("tui.key.identity")) {
		t.Fatalf("identity-present application still exposes I: %#v", existing)
	}
	handleTUIKey(context.Background(), &view, "i", actions, make(chan tuiEvent, 1))
	if generated != 0 || view.scanConfirm != "" {
		t.Fatalf("hidden I reached identity action: generated=%d view=%#v", generated, view)
	}
}

func TestTUIScanIdentityGenerationKeepsUnrelatedDifferences(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiScan
	proposed := view.catalog.Apps[0]
	proposed.Identity = "bundle:discovered"
	proposed.Provider = model.ProviderConfig{Type: model.ProviderSparkle}
	view.scanProposed = map[string]model.Application{"obsidian": proposed}
	view.scanChanges = map[string]model.ScanApplicationChange{"obsidian": {
		Current: view.catalog.Apps[0], Proposed: proposed,
		Fields: []model.ScanFieldChange{
			{Field: "identity", Proposed: proposed.Identity},
			{Field: model.ApplicationFieldProviderType, Current: string(view.catalog.Apps[0].Provider.Type), Proposed: string(proposed.Provider.Type)},
		},
	}}
	actions := TUIActions{
		GenerateIdentity: func(model.Application) (string, error) { return "bundle:user-choice", nil },
		SaveScan: func(_, catalog model.Config) (model.Config, error) {
			return cloneConfig(catalog), nil
		},
	}
	beginIdentityConfirmation(&view, actions)
	handleScanConfirmationKey(&view, "enter", actions)
	change := view.scanChanges["obsidian"]
	if len(change.Fields) != 1 || change.Fields[0].Field != model.ApplicationFieldProviderType || view.scanProposed["obsidian"].Identity != "bundle:user-choice" {
		t.Fatalf("identity generation discarded or can overwrite unrelated differences: %#v proposed=%#v", change, view.scanProposed["obsidian"])
	}
}

func TestApplyTUIScanFieldAppliesProviderFieldsIndependently(t *testing.T) {
	target := model.Application{Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{
		Version: "old-version", Check: "old-check", Update: "old-update", Download: &model.Download{URL: "https://example.test/old"}, Install: "old-install",
	}}}
	source := model.Application{Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease, Actions: &model.ProviderActions{
		Version: "new-version", Check: "new-check", Update: "new-update", Download: &model.Download{URL: "https://example.test/new"}, Install: "new-install",
	}}}

	applyTUIScanField(&target, source, model.ApplicationFieldProviderType)
	if target.Provider.Type != model.ProviderGitHubRelease || target.Provider.VersionAction() != "old-version" {
		t.Fatalf("provider type application replaced unrelated actions: %#v", target.Provider)
	}
	for _, field := range []string{
		model.ApplicationFieldActionVersion, model.ApplicationFieldActionCheck, model.ApplicationFieldActionUpdate,
		model.ApplicationFieldActionDownload, model.ApplicationFieldActionInstall,
	} {
		applyTUIScanField(&target, source, field)
	}
	if target.Provider.VersionAction() != "new-version" || target.Provider.CheckAction() != "new-check" || target.Provider.UpdateAction() != "new-update" || target.Provider.DownloadAction().URL != "https://example.test/new" || target.Provider.InstallAction() != "new-install" {
		t.Fatalf("provider action fields were not applied: %#v", target.Provider)
	}

	empty := model.Application{Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}}
	for _, field := range []string{
		model.ApplicationFieldActionVersion, model.ApplicationFieldActionCheck, model.ApplicationFieldActionUpdate,
		model.ApplicationFieldActionDownload, model.ApplicationFieldActionInstall,
	} {
		applyTUIScanField(&target, empty, field)
	}
	if target.Provider.Actions != nil {
		t.Fatalf("empty provider actions were retained: %#v", target.Provider.Actions)
	}
}

func TestTUIApplicationConfigurationStagesUntilCtrlSSaveAndApply(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.page = tuiConfig
	view.width, view.height = 160, 50
	rows := configRows(&view.working)
	view.configIndex = findConfigRowIndex(rows, "app:obsidian")

	handleConfigKey(&view, "enter", TUIActions{})
	if !view.configAppFocus {
		t.Fatal("Enter on an application did not focus its field list")
	}
	fields := selectedApplicationConfigRows(&view)
	for index, field := range fields {
		if field.field == "name" {
			view.appFieldIndex = index
			break
		}
	}
	handleConfigKey(&view, "enter", TUIActions{})
	if !view.editing {
		t.Fatal("Enter on an application field did not start editing")
	}
	view.editValue = "Obsidian Next"
	view.editCursor = utf8.RuneCountInString(view.editValue)
	handleTUIKey(context.Background(), &view, "enter", TUIActions{}, make(chan tuiEvent, 1))
	if view.catalog.Apps[0].Name != "Obsidian" {
		t.Fatalf("unsaved edit changed effective catalog: %q", view.catalog.Apps[0].Name)
	}
	if view.working.Apps[0].Name != "Obsidian Next" || !view.dirty {
		t.Fatalf("application edit was not staged: %#v", view.working.Apps[0])
	}

	var rendered bytes.Buffer
	renderTUI(&rendered, &view)
	plain := stripTUIANSI(rendered.String())
	for _, expected := range []string{"应用 · Obsidian Next", "名称", "版本命令", "下载 URL", "下载扩展参数", "CTRL+S 保存并生效"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("application configuration render missing %q:\n%s", expected, plain)
		}
	}

	saves := 0
	actions := TUIActions{SaveConfig: func(_ model.Config, proposed model.Config) (model.Config, error) {
		saves++
		if proposed.Apps[0].Name != "Obsidian Next" {
			t.Fatalf("saved application name = %q", proposed.Apps[0].Name)
		}
		return cloneConfig(proposed), nil
	}}
	handleConfigKey(&view, "ctrl+s", actions)
	if saves != 1 || view.dirty || view.catalog.Apps[0].Name != "Obsidian Next" {
		t.Fatalf("Ctrl+S did not save and apply the staged application: saves=%d dirty=%v catalog=%#v", saves, view.dirty, view.catalog.Apps[0])
	}
}

func TestTUIConfigurationEditsCustomBundleIDList(t *testing.T) {
	view := sampleTUIView()
	rows := configRows(&view.working)
	index := findConfigRowIndex(rows, "scan_bundle_id")
	if index < 0 {
		t.Fatal("custom Bundle ID configuration row is missing")
	}
	if err := setSettingsConfigValue(&view.working, rows[index], "md.obsidian, com.example.Editor"); err != nil {
		t.Fatal(err)
	}
	got := view.working.Settings.Scan.BundleID
	if len(got) != 2 || got[0] != "md.obsidian" || got[1] != "com.example.Editor" {
		t.Fatalf("custom Bundle ID list = %#v", got)
	}
}

func TestTUIApplicationEditorsShareNormalizationAndValidation(t *testing.T) {
	useLanguage(t, i18n.English)
	configView := sampleTUIView()
	configView.page = tuiConfig
	rows := configRows(&configView.working)
	configView.configIndex = findConfigRowIndex(rows, "app:obsidian")
	configView.configAppFocus = true
	fields := selectedApplicationConfigRows(&configView)
	nameIndex := -1
	for index, field := range fields {
		if field.field == "name" {
			nameIndex = index
			break
		}
	}
	if nameIndex < 0 {
		t.Fatal("configuration manager name field is missing")
	}
	configView.appFieldIndex = nameIndex
	if err := applyConfigEdit(&configView, "  Renamed App  "); err != nil || configView.working.Apps[0].Name != "Renamed App" {
		t.Fatalf("configuration manager did not trim the application value: app=%#v err=%v", configView.working.Apps[0], err)
	}
	for _, value := range []string{"   ", strings.Repeat("x", tuiMaxEditValueBytes+1)} {
		if err := applyConfigEdit(&configView, value); err == nil {
			t.Fatalf("configuration manager accepted invalid application value of length %d", len(value))
		}
	}

	scanView := sampleTUIView()
	scanView.page = tuiScan
	candidate := cloneScanApplication(scanView.catalog.Apps[0])
	candidate.ID, candidate.Name = "new-tool", "New Tool"
	scanView.scanProposed = map[string]model.Application{candidate.ID: candidate}
	scanView.scanAdded = map[string]bool{candidate.ID: true}
	ensureScanMaps(&scanView)
	beginScanCandidateEdit(&scanView, candidate)
	for index, field := range scanCandidateConfigRows(&scanView) {
		if field.field == "name" {
			scanView.scanFieldIndex = index
			break
		}
	}
	if err := applyScanCandidateConfigEdit(&scanView, "  Renamed App  "); err != nil || scanView.scanEditDraft.Name != "Renamed App" {
		t.Fatalf("scan manager did not apply the shared normalization: draft=%#v err=%v", scanView.scanEditDraft, err)
	}
	for _, value := range []string{"   ", strings.Repeat("x", tuiMaxEditValueBytes+1)} {
		if err := applyScanCandidateConfigEdit(&scanView, value); err == nil {
			t.Fatalf("scan manager accepted invalid application value of length %d", len(value))
		}
	}

	configView.working.Apps[0].InstallPath = " "
	configView.dirty = true
	saves := 0
	actions := TUIActions{SaveConfig: func(_ model.Config, catalog model.Config) (model.Config, error) {
		saves++
		return catalog, nil
	}}
	handleConfigKey(&configView, "ctrl+s", actions)
	if saves != 0 || !configView.messageError || !strings.Contains(configView.message, i18n.T("label.install_path")) {
		t.Fatalf("configuration manager bypassed shared full-form validation: saves=%d message=%q", saves, configView.message)
	}
}

func TestTUIApplicationConfigurationParameterNamesAreDim(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.page = tuiConfig
	view.width, view.height = 160, 50
	rows := configRows(&view.working)
	view.configIndex = findConfigRowIndex(rows, "app:obsidian")
	view.configAppFocus = true
	view.appFieldIndex = 1
	screen := newTUIScreen(view.width, view.height)
	renderConfigPage(screen, &view, 3, 44)
	leftWidth := screen.width * 68 / 100
	labelX := leftWidth + 2
	valueX := leftWidth + 2 + min(17, max(8, (screen.width-leftWidth)/3))
	idRowY := 3 + 2
	descriptionRowY := 3 + 2 + 3
	if screen.cells[idRowY][labelX].style != tuiDim || screen.cells[idRowY][valueX].style != tuiDim {
		t.Fatalf("read-only application field is not dim: label=%q value=%q", screen.cells[idRowY][labelX].style, screen.cells[idRowY][valueX].style)
	}
	if screen.cells[descriptionRowY][labelX].style != tuiDim || screen.cells[descriptionRowY][valueX].style != tuiNormal {
		t.Fatalf("application parameter name/value styles differ from scan management: label=%q value=%q", screen.cells[descriptionRowY][labelX].style, screen.cells[descriptionRowY][valueX].style)
	}
	if screen.cells[descriptionRowY][valueX].value != '-' {
		t.Fatalf("empty application parameter value = %q, want '-'", screen.cells[descriptionRowY][valueX].value)
	}
}

func TestTUISettingsBecomeEffectiveOnlyAfterCtrlSSave(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.page = tuiConfig
	view.width, view.height = 120, 30
	view.configIndex = findConfigRowIndex(configRows(&view.working), "workers")
	adjustConfig(&view, 1)
	if view.working.Settings.Workers != 5 || view.catalog.Settings.Workers != 4 {
		t.Fatalf("worker edit was not isolated: working=%d effective=%d", view.working.Settings.Workers, view.catalog.Settings.Workers)
	}
	var before bytes.Buffer
	renderTUI(&before, &view)
	if !strings.Contains(stripTUIANSI(before.String()), i18n.T("tui.workers_badge", 4, 4)) {
		t.Fatal("unsaved worker count appeared as effective in the header")
	}
	actions := TUIActions{SaveConfig: func(_ model.Config, proposed model.Config) (model.Config, error) { return cloneConfig(proposed), nil }}
	handleConfigKey(&view, "ctrl+s", actions)
	var after bytes.Buffer
	renderTUI(&after, &view)
	if !strings.Contains(stripTUIANSI(after.String()), i18n.T("tui.workers_badge", 5, 5)) {
		t.Fatal("saved worker count was not applied to the current session")
	}
}

func TestTUICtrlSSavesTheActiveEditor(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.page = tuiConfig
	view.configIndex = findConfigRowIndex(configRows(&view.working), "workers")
	handleConfigKey(&view, "enter", TUIActions{})
	view.editValue = "7"
	view.editCursor = 1
	if view.working.Settings.Workers != 4 || view.catalog.Settings.Workers != 4 {
		t.Fatal("opening the editor changed configuration")
	}
	saves := 0
	actions := TUIActions{SaveConfig: func(_ model.Config, proposed model.Config) (model.Config, error) {
		saves++
		return cloneConfig(proposed), nil
	}}
	handleTUIKey(context.Background(), &view, "ctrl+s", actions, make(chan tuiEvent, 1))
	if saves != 1 || view.editing || view.dirty || view.working.Settings.Workers != 7 || view.catalog.Settings.Workers != 7 {
		t.Fatalf("Ctrl+S did not commit, save, and apply the active editor: saves=%d view=%#v", saves, view)
	}
}

func TestTUILanguageSaveUsesTheNewLanguageForConfirmation(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.page = tuiConfig
	view.catalog.Settings.Language = "en"
	view.working = cloneConfig(view.catalog)
	view.working.Settings.Language = "zh"
	view.dirty = true
	view.configIndex = findConfigRowIndex(configRows(&view.working), "language")
	actions := TUIActions{SaveConfig: func(_ model.Config, proposed model.Config) (model.Config, error) { return cloneConfig(proposed), nil }}

	handleConfigKey(&view, "ctrl+s", actions)

	if i18n.Current() != i18n.Chinese {
		t.Fatalf("current language = %q, want zh", i18n.Current())
	}
	if view.message != "配置已保存并生效" {
		t.Fatalf("save confirmation used the previous language: %q", view.message)
	}
}

func TestTUILeavingConfigurationClearsTheHeaderMessage(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.page = tuiConfig
	view.setMessage(i18n.T("tui.saved"), false)

	handleConfigKey(&view, "esc", TUIActions{})

	if view.page != tuiApps || view.message != "" || view.messageError || !view.messageUntil.IsZero() {
		t.Fatalf("leaving configuration retained its header message: %#v", view)
	}
}

func TestTUILeavingDirtyConfigurationRequiresSaveOrDiscard(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	newView := func() tuiModel {
		view := sampleTUIView()
		view.page = tuiConfig
		view.working.Apps[0].Name = "Pending Name"
		view.dirty = true
		return view
	}

	discarded := newView()
	handleConfigKey(&discarded, "esc", TUIActions{})
	if !discarded.configExitConfirm || discarded.page != tuiConfig || discarded.working.Apps[0].Name != "Pending Name" {
		t.Fatalf("dirty configuration left without confirmation: %#v", discarded)
	}
	discarded.width, discarded.height = 100, 30
	screen := newTUIScreen(discarded.width, discarded.height)
	renderConfigExitConfirmation(screen, &discarded)
	plain := stripTUIANSI(screen.string())
	if !strings.Contains(plain, i18n.T("tui.config.unsaved_title")) || !strings.Contains(plain, "[ ENTER "+i18n.T("tui.save")+" ]") || !strings.Contains(plain, "[ ESC "+i18n.T("tui.discard")+" ]") {
		t.Fatalf("unsaved configuration confirmation is incomplete:\n%s", plain)
	}
	handleTUIKey(context.Background(), &discarded, "right", TUIActions{}, make(chan tuiEvent, 1))
	handleTUIKey(context.Background(), &discarded, "enter", TUIActions{}, make(chan tuiEvent, 1))
	if discarded.page != tuiApps || discarded.dirty || discarded.working.Apps[0].Name != discarded.catalog.Apps[0].Name {
		t.Fatalf("discard did not restore and leave configuration: %#v", discarded)
	}

	saved := newView()
	saves := 0
	actions := TUIActions{SaveConfig: func(_ model.Config, catalog model.Config) (model.Config, error) {
		saves++
		return cloneConfig(catalog), nil
	}}
	handleConfigKey(&saved, "esc", actions)
	handleTUIKey(context.Background(), &saved, "enter", actions, make(chan tuiEvent, 1))
	if saves != 1 || saved.page != tuiApps || saved.dirty || saved.catalog.Apps[0].Name != "Pending Name" {
		t.Fatalf("save choice did not persist and leave configuration: saves=%d view=%#v", saves, saved)
	}

	failed := newView()
	failureActions := TUIActions{SaveConfig: func(model.Config, model.Config) (model.Config, error) {
		return model.Config{}, errors.New("save failed")
	}}
	handleConfigKey(&failed, "esc", failureActions)
	handleTUIKey(context.Background(), &failed, "enter", failureActions, make(chan tuiEvent, 1))
	if failed.page != tuiConfig || !failed.dirty || failed.configExitConfirm || !failed.messageError {
		t.Fatalf("failed save left configuration or lost changes: %#v", failed)
	}
}

func TestTUIConfigNestedEscapeDoesNotPromptForPageExit(t *testing.T) {
	view := sampleTUIView()
	view.page = tuiConfig
	view.dirty = true
	view.configAppFocus = true
	handleConfigKey(&view, "esc", TUIActions{})
	if view.configAppFocus || view.configExitConfirm || view.page != tuiConfig {
		t.Fatalf("application-field ESC should only return to the configuration list: %#v", view)
	}
	handleConfigKey(&view, "esc", TUIActions{})
	if !view.configExitConfirm || view.page != tuiConfig {
		t.Fatalf("configuration-list ESC did not request save or discard: %#v", view)
	}
}

func TestTUIHeaderMessagesExpire(t *testing.T) {
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
}

func TestTUIMessageIsCenteredInHeaderWithoutCoveringBadgesOrFooter(t *testing.T) {
	useLanguage(t, i18n.English)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 160, 30
	view.message = "Batch update capacity is full (10/10 in use); wait for an operation to finish"

	screen := newTUIScreen(view.width, view.height)
	renderTUIHeader(screen, &view)
	renderFooter(screen, &view, view.height-3, 3)
	lines := strings.Split(stripTUIANSI(screen.string()), "\n")
	if len(lines) != view.height {
		t.Fatalf("rendered rows = %d, want %d", len(lines), view.height)
	}
	header := lines[1]
	for _, expected := range []string{"TendKit", "Batch update capacity", "[ APPS (1/1) ]", "[ CONCURRENCY 4/4 ]", "[ EN ]"} {
		if !strings.Contains(header, expected) {
			t.Fatalf("header missing %q: %q", expected, header)
		}
	}
	footer := lines[view.height-2]
	keys := strings.Join(tuiCurrentKeymap(&view).FooterLines(view.width), " ")
	if !strings.Contains(footer, keys) {
		t.Fatalf("footer keys were covered or truncated by the message: %q", footer)
	}
	if strings.Contains(footer, "Batch update capacity") {
		t.Fatalf("message still rendered in footer: %q", footer)
	}

	view.message = "Queued"
	centered := newTUIScreen(view.width, view.height)
	renderTUIHeader(centered, &view)
	centeredHeader := strings.Split(stripTUIANSI(centered.string()), "\n")[1]
	if start := strings.Index(centeredHeader, view.message); start != (view.width-len(view.message))/2 {
		t.Fatalf("message starts at column %d, want centered at %d: %q", start, (view.width-len(view.message))/2, centeredHeader)
	}
}

func TestTUIApplicationDownloadFieldsAndEnvironmentEditingAreStrict(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.page = tuiConfig
	view.working.Apps[0].Environment = map[string]string{"GITHUB_TOKEN": "must-not-be-rendered"}
	fields := applicationConfigRows(&view.working, "obsidian")
	downloadRows := make(map[string]configRow)
	var environmentRow configRow
	environmentIndex := -1
	for index, field := range fields {
		switch field.field {
		case "download_url", "download_filename", "download_store_path", "download_extra_args":
			downloadRows[field.field] = field
		case "environment":
			environmentRow = field
			environmentIndex = index
		}
	}
	if environmentRow.value != "GITHUB_TOKEN=must-not-be-rendered" || environmentRow.rowType != configRowString {
		t.Fatalf("environment field is not editable using the scan format: %#v", environmentRow)
	}
	rows := configRows(&view.working)
	view.configIndex = findConfigRowIndex(rows, "app:obsidian")
	view.configAppFocus = true
	view.appFieldIndex = environmentIndex
	handleConfigKey(&view, "enter", TUIActions{})
	if !view.editing || view.editValue != "GITHUB_TOKEN=must-not-be-rendered" {
		t.Fatalf("environment field did not enter text editing: %#v", view)
	}
	view.editValue = " FOO=bar, EMPTY= , TOKEN=a=b "
	handleTUIKey(context.Background(), &view, "enter", TUIActions{}, make(chan tuiEvent, 1))
	if view.editing {
		t.Fatal("valid environment form value did not leave text editing")
	}
	environment := view.working.Apps[0].Environment
	if environment["FOO"] != "bar" || environment["EMPTY"] != "" || environment["TOKEN"] != "a=b" || len(environment) != 3 {
		t.Fatalf("environment form value parsed incorrectly: %#v", environment)
	}
	for _, invalid := range []string{"NO_EQUALS", "1BAD=value", "DUP=one,DUP=two", "TRAILING=value,"} {
		if err := setConfigValue(&view.working, environmentRow, invalid); err == nil {
			t.Fatalf("invalid environment form value %q was accepted", invalid)
		}
	}
	if err := setConfigValue(&view.working, downloadRows["download_extra_args"], "--quiet, --retry=2"); err != nil {
		t.Fatalf("setting download extra arguments failed: %v", err)
	}
	for field, value := range map[string]string{
		"download_url": "https://example.invalid/application", "download_filename": "application.zip", "download_store_path": "~/Artifacts",
	} {
		if err := setConfigValue(&view.working, downloadRows[field], value); err != nil {
			t.Fatalf("setting %s failed: %v", field, err)
		}
	}
	if view.working.Apps[0].Provider.DownloadAction() == nil || view.working.Apps[0].Provider.DownloadAction().Filename != "application.zip" || view.working.Apps[0].Provider.DownloadAction().StorePath != "~/Artifacts" {
		t.Fatalf("download JSON was not applied: %#v", view.working.Apps[0].Provider.DownloadAction())
	}
	if got := view.working.Apps[0].Provider.DownloadAction().ExtraArgs; !reflect.DeepEqual(got, []string{"--quiet", "--retry=2"}) {
		t.Fatalf("download extra arguments = %#v", got)
	}
	for _, row := range applicationConfigRows(&view.working, "obsidian") {
		if row.field == "download_extra_args" && row.value != "--quiet, --retry=2" {
			t.Fatalf("download extra arguments display = %q", row.value)
		}
	}
}

func TestTUIScanCandidateEditSnapshotStagesResetsAndDiscards(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.page = tuiScan
	candidate := model.Application{
		ID: "new-tool", Name: "Original", Type: model.ApplicationTypeCLI,
		InstallPath: "/tmp/new-tool", Enabled: true, UpdateMode: model.ModeCheck,
		Environment: map[string]string{"TOKEN": "original"},
		Provider:    model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{Download: &model.Download{URL: "https://example.test/file", ExtraArgs: []string{"--quiet"}}}},
	}
	view.scanProposed = map[string]model.Application{candidate.ID: cloneScanApplication(candidate)}
	view.scanAdded = map[string]bool{candidate.ID: true}
	view.scanSelected = 1
	ensureScanMaps(&view)
	beginScanCandidateEdit(&view, candidate)

	nameRow := -1
	for index, row := range scanCandidateConfigRows(&view) {
		if row.field == "name" {
			nameRow = index
			break
		}
	}
	if nameRow < 0 {
		t.Fatal("candidate name row is missing")
	}
	view.scanFieldIndex = nameRow
	if err := applyScanCandidateConfigEdit(&view, "Draft"); err != nil {
		t.Fatalf("edit candidate draft: %v", err)
	}
	view.scanEditDraft.Environment["TOKEN"] = "draft"
	view.scanEditDraft.Provider.DownloadAction().ExtraArgs[0] = "--draft"
	if view.scanProposed[candidate.ID].Name != "Original" || view.scanProposed[candidate.ID].Environment["TOKEN"] != "original" || view.scanProposed[candidate.ID].Provider.DownloadAction().ExtraArgs[0] != "--quiet" {
		t.Fatalf("draft mutation leaked into staged configuration: %#v", view.scanProposed[candidate.ID])
	}

	handleScanCandidateConfigKey(&view, "r")
	if view.scanEditDraft.Name != "Original" || view.scanEditDraft.Environment["TOKEN"] != "original" || view.scanEditDraft.Provider.DownloadAction().ExtraArgs[0] != "--quiet" {
		t.Fatalf("reset did not restore the entry snapshot: %#v", view.scanEditDraft)
	}

	view.scanFieldIndex = nameRow
	if err := applyScanCandidateConfigEdit(&view, "Staged"); err != nil {
		t.Fatalf("edit candidate before staging: %v", err)
	}
	handleScanCandidateConfigKey(&view, "ctrl+s")
	if view.scanProposed[candidate.ID].Name != "Staged" {
		t.Fatalf("CTRL+S did not stage the draft: %#v", view.scanProposed[candidate.ID])
	}
	if err := applyScanCandidateConfigEdit(&view, "Unstaged"); err != nil {
		t.Fatalf("edit candidate after staging: %v", err)
	}
	handleScanCandidateConfigKey(&view, "esc")
	if view.scanEditFocus || view.scanProposed[candidate.ID].Name != "Staged" {
		t.Fatalf("ESC did not discard only unstaged changes: %#v", view)
	}
}

func TestTUIScanCandidateEnumsEnvironmentAndIdentityValidation(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	view.page = tuiScan
	view.catalog.Apps[0].Identity = "application:existing"
	candidate := model.Application{
		ID: "new-tool", Name: "New Tool", Type: model.ApplicationTypeCLI,
		InstallPath: "/tmp/new-tool", Enabled: true, UpdateMode: model.ModeCheck,
		Provider: model.ProviderConfig{Type: model.ProviderDefault}, Identity: "cli:new-tool",
		Environment: map[string]string{"B": "two", "A": "one"},
	}
	view.scanProposed = map[string]model.Application{candidate.ID: candidate}
	view.scanAdded = map[string]bool{candidate.ID: true}
	view.scanConfirmID = candidate.ID
	view.scanSelected = 1
	ensureScanMaps(&view)
	beginScanCandidateEdit(&view, candidate)

	rows := scanCandidateConfigRows(&view)
	rowIndex := func(field string) int {
		for index, row := range rows {
			if row.field == field {
				return index
			}
		}
		t.Fatalf("missing candidate field %q", field)
		return -1
	}
	typeIndex, providerIndex, environmentIndex := rowIndex("type"), rowIndex("provider"), rowIndex("environment")
	if rows[typeIndex].rowType != configRowChoice || rows[providerIndex].rowType != configRowChoice {
		t.Fatalf("type/provider are not enum choices: type=%#v provider=%#v", rows[typeIndex], rows[providerIndex])
	}
	view.scanFieldIndex = typeIndex
	handleScanCandidateConfigKey(&view, "enter")
	if view.editing || !strings.Contains(view.message, i18n.T("tui.enum_keys_only")) {
		t.Fatalf("type enum entered text editing: %#v", view)
	}
	adjustScanCandidateConfig(&view, rows[typeIndex], 1)
	if view.scanEditDraft.Type != model.ApplicationTypeBundle || view.scanProposed[candidate.ID].Type != model.ApplicationTypeCLI {
		t.Fatalf("type enum did not remain in the draft: draft=%#v staged=%#v", view.scanEditDraft, view.scanProposed[candidate.ID])
	}
	rows = scanCandidateConfigRows(&view)
	adjustScanCandidateConfig(&view, rows[providerIndex], 1)
	if view.scanEditDraft.Provider.Type != model.ProviderGitHubRelease || view.scanProposed[candidate.ID].Provider.Type != model.ProviderDefault {
		t.Fatalf("provider enum did not remain in the draft: draft=%#v staged=%#v", view.scanEditDraft, view.scanProposed[candidate.ID])
	}

	rows = scanCandidateConfigRows(&view)
	if rows[environmentIndex].rowType != configRowString || rows[environmentIndex].value != "A=one,B=two" {
		t.Fatalf("candidate environment field = %#v", rows[environmentIndex])
	}
	view.scanFieldIndex = environmentIndex
	if err := applyScanCandidateConfigEdit(&view, " FOO=bar, EMPTY= , TOKEN=a=b "); err != nil {
		t.Fatalf("valid candidate environment failed: %v", err)
	}
	environment := view.scanEditDraft.Environment
	if environment["FOO"] != "bar" || environment["EMPTY"] != "" || environment["TOKEN"] != "a=b" || len(environment) != 3 {
		t.Fatalf("candidate environment parsed incorrectly: %#v", environment)
	}
	for _, invalid := range []string{"NO_EQUALS", "1BAD=value", "DUP=one,DUP=two", "TRAILING=value,"} {
		if err := applyScanCandidateConfigEdit(&view, invalid); err == nil {
			t.Fatalf("invalid environment %q was accepted", invalid)
		}
	}

	rows = scanCandidateConfigRows(&view)
	view.scanFieldIndex = -1
	for index, row := range rows {
		if row.field == "identity" {
			view.scanFieldIndex = index
			break
		}
	}
	if err := applyScanCandidateConfigEdit(&view, " APPLICATION:EXISTING "); err != nil {
		t.Fatalf("identity draft was rejected before staging: %v", err)
	}
	if stageScanCandidateEdit(&view) || !view.messageError || !strings.Contains(view.message, i18n.T("tui.scan.identity_conflict", "APPLICATION:EXISTING", "Obsidian")) {
		t.Fatalf("duplicate candidate identity was not rejected while staging: %q", view.message)
	}
	if view.scanProposed[candidate.ID].Identity != "cli:new-tool" {
		t.Fatalf("rejected identity changed the staged candidate: %#v", view.scanProposed[candidate.ID])
	}
	resetScanCandidateEdit(&view)
	otherCandidate := candidate
	otherCandidate.ID, otherCandidate.Name, otherCandidate.Identity = "other-tool", "Other Tool", "package:other"
	view.scanProposed[otherCandidate.ID] = otherCandidate
	if err := applyScanCandidateConfigEdit(&view, "PACKAGE:OTHER"); err != nil {
		t.Fatalf("identity draft was rejected before staging: %v", err)
	}
	if stageScanCandidateEdit(&view) || !strings.Contains(view.message, i18n.T("tui.scan.identity_conflict", "PACKAGE:OTHER", "Other Tool")) {
		t.Fatalf("identity duplicated by another scan candidate was not rejected while staging: %q", view.message)
	}
}

func TestTUIScanCandidateParameterLabelsAreDim(t *testing.T) {
	useLanguage(t, i18n.English)
	view := sampleTUIView()
	candidate := model.Application{
		ID: "new-tool", Name: "New Tool", Type: model.ApplicationTypeCLI,
		InstallPath: "/tmp/new-tool", Enabled: true, UpdateMode: model.ModeCheck,
		Provider: model.ProviderConfig{Type: model.ProviderDefault},
	}
	view.scanProposed = map[string]model.Application{candidate.ID: candidate}
	view.scanAdded = map[string]bool{candidate.ID: true}
	view.scanSelected = 1
	ensureScanMaps(&view)
	rows := scanCandidateConfigRowsFor(&view, candidate.ID)
	lines, fieldRows := scanComparisonLines(&view, candidate, 80)
	nameIndex := 0
	for index, row := range rows {
		if row.field == "name" {
			nameIndex = index
			break
		}
	}
	line := lines[fieldRows[nameIndex]]
	if line.label == "" {
		t.Fatal("candidate parameter label was merged into the value")
	}
	screen := newTUIScreen(80, 10)
	renderScanComparison(screen, &view, 0, 0, 80, 10)
	if screen.cells[fieldRows[nameIndex]][1].style != tuiDim {
		t.Fatalf("candidate parameter label style = %q, want dim", screen.cells[fieldRows[nameIndex]][1].style)
	}
	valueColumn := 1 + DisplayWidth(line.label)
	if screen.cells[fieldRows[nameIndex]][valueColumn].style != tuiNormal {
		t.Fatalf("candidate parameter value style = %q, want normal", screen.cells[fieldRows[nameIndex]][valueColumn].style)
	}
}

func TestTUIDownloaderFieldsAreSplitAndExtraArgumentsUseCommaList(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	downloaderRows := make(map[string]configRow)
	for _, row := range configRows(&view.working) {
		if strings.HasPrefix(row.key, "downloader_") {
			downloaderRows[row.key] = row
		}
	}
	if len(downloaderRows) != 3 {
		t.Fatalf("downloader field count = %d, want 3", len(downloaderRows))
	}
	for key, value := range map[string]string{
		"downloader_cli": "aria2c", "downloader_store_path": "~/Artifacts", "downloader_extra_args": "--summary-interval=2, --retry=2",
	} {
		if err := setConfigValue(&view.working, downloaderRows[key], value); err != nil {
			t.Fatalf("setting %s failed: %v", key, err)
		}
	}
	settings := view.working.Settings.Downloader
	if settings.CLI != "aria2c" || settings.StorePath != "~/Artifacts" || !reflect.DeepEqual(settings.ExtraArgs, []string{"--summary-interval=2", "--retry=2"}) {
		t.Fatalf("downloader JSON was not applied: %#v", settings)
	}
	rows := configRows(&view.working)
	if value := rows[findConfigRowIndex(rows, "downloader_extra_args")].value; value != "--summary-interval=2, --retry=2" {
		t.Fatalf("downloader extra arguments display = %q", value)
	}
}

func TestTUISelectionsUseFullRowBackground(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	view := sampleTUIView()
	view.width, view.height = 160, 50

	appScreen := newTUIScreen(100, 20)
	renderApplicationTable(appScreen, &view, 0, 0, 80, 16)
	assertTUIRowStyle(t, appScreen, 3, 0, 80, tuiSelect)

	view.page = tuiConfig
	view.configIndex = 2
	configScreen := newTUIScreen(160, 50)
	renderConfigPage(configScreen, &view, 3, 44)
	configLeftWidth := configScreen.width * 68 / 100
	selectedVisual := 0
	for index, line := range configVisualLines(configRows(&view.working), &view.working, &view.catalog) {
		if line.rowIndex == view.configIndex {
			selectedVisual = index
			break
		}
	}
	assertTUIRowStyle(t, configScreen, 3+2+selectedVisual, 1, configLeftWidth-2, tuiSelect)

	rows := configRows(&view.working)
	view.configIndex = findConfigRowIndex(rows, "app:obsidian")
	view.configAppFocus = true
	view.appFieldIndex = 1
	fieldScreen := newTUIScreen(160, 50)
	renderConfigPage(fieldScreen, &view, 3, 44)
	leftWidth := fieldScreen.width * 68 / 100
	assertTUIRowStyle(t, fieldScreen, 6, leftWidth+1, fieldScreen.width-leftWidth-2, tuiSelect)

	var output bytes.Buffer
	renderTUI(&output, &view)
	if !strings.Contains(output.String(), "\033[30;46m") {
		t.Fatal("selected rows are missing the background highlight escape sequence")
	}
}

func TestTUIConfigApplicationRowsUseNormalTextAndRightPanelUsesEightyTwentySplit(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	view := sampleTUIView()
	view.page = tuiConfig
	view.width, view.height = 160, 100
	rows := configRows(&view.working)
	appIndex := findConfigRowIndex(rows, "app:obsidian")
	view.configIndex = 0
	screen := newTUIScreen(view.width, view.height)
	renderConfigPage(screen, &view, 3, 94)
	appVisual := 0
	for index, line := range configVisualLines(rows, &view.working, &view.catalog) {
		if line.rowIndex == appIndex {
			appVisual = index
			break
		}
	}
	appRowY := 3 + 2 + appVisual
	if screen.cells[appRowY][2].style != tuiNormal {
		t.Fatalf("unselected application configuration row style = %q, want normal", screen.cells[appRowY][2].style)
	}

	const panelHeight = 44
	listHeight := applicationConfigListHeight(panelHeight)
	contentHeight := panelHeight - 3
	if listHeight != 33 || contentHeight-listHeight != 8 {
		t.Fatalf("right panel split = %d:%d, want 33:8 (approximately 80%%:20%%)", listHeight, contentHeight-listHeight)
	}
	view.configIndex = appIndex
	view.configAppFocus = true
	renderConfigPage(screen, &view, 3, panelHeight)
	leftWidth := screen.width * 68 / 100
	separatorY := 3 + 2 + listHeight
	if screen.cells[separatorY][leftWidth+1].value != '─' {
		t.Fatalf("right panel separator missing at 80%% split row %d", separatorY)
	}
}

func TestTUIStatusStylesUseGreenRedGrayAndWhite(t *testing.T) {
	cases := map[string]string{
		"update_available": tuiGreen,
		"failed":           tuiRed,
		"waiting":          tuiDim,
		"current":          tuiWhite,
	}
	for status, expected := range cases {
		if actual := tuiStatusStyle(status); actual != expected {
			t.Errorf("status %q style = %q, want %q", status, actual, expected)
		}
	}
}

func TestTUIApplicationDetailsScrollThroughLongErrors(t *testing.T) {
	useLanguage(t, i18n.Chinese)
	t.Setenv("NO_COLOR", "1")
	view := sampleTUIView()
	view.width, view.height = 80, 24
	state := view.catalog.Apps[0].StatusManaged
	state.UpdateStatus = "failed"
	state.Error = strings.Repeat("无法匹配可用版本，", 40) + "错误信息末尾"
	view.catalog.Apps[0].StatusManaged = state
	if maxOffset := tuiMaxDetailOffset(&view); maxOffset <= 0 {
		t.Fatalf("long error did not create a scrollable detail view: offset=%d", maxOffset)
	}

	events := make(chan tuiEvent, 1)
	handleTUIKey(context.Background(), &view, "enter", TUIActions{}, events)
	if !view.detailFocus {
		t.Fatal("Enter did not focus application details")
	}
	var top bytes.Buffer
	renderTUI(&top, &view)
	if strings.Contains(stripTUIANSI(top.String()), "错误信息末尾") {
		t.Fatal("detail view unexpectedly showed the end of the error before scrolling")
	}
	handleTUIKey(context.Background(), &view, "end", TUIActions{}, events)
	if view.detailOffset != tuiMaxDetailOffset(&view) {
		t.Fatalf("End offset = %d, want %d", view.detailOffset, tuiMaxDetailOffset(&view))
	}
	var bottom bytes.Buffer
	renderTUI(&bottom, &view)
	bottomText := stripTUIANSI(bottom.String())
	if !strings.Contains(bottomText, "错误信") || !strings.Contains(bottomText, "息末尾") {
		t.Fatalf("scrolled detail view did not reach the error tail:\n%s", bottomText)
	}
	for _, expected := range []string{"滚动详情", "应用详情 · 滚动"} {
		if !strings.Contains(bottomText, expected) {
			t.Fatalf("scrolled detail view missing %q:\n%s", expected, bottomText)
		}
	}
	handleTUIKey(context.Background(), &view, "esc", TUIActions{}, events)
	if view.detailFocus {
		t.Fatal("Esc did not return focus to the application list")
	}
}

func assertTUIRowStyle(t *testing.T, screen *tuiScreen, row, start, width int, style string) {
	t.Helper()
	for column := start; column < start+width; column++ {
		if screen.cells[row][column].style != style {
			t.Fatalf("row %d column %d style = %q, want %q", row, column, screen.cells[row][column].style, style)
		}
	}
}

func assertTUISelectedSearchMatchStyle(t *testing.T, screen *tuiScreen, cell tuiCell) {
	t.Helper()
	if cell.style != tuiSelectMatch {
		t.Fatalf("selected matching name style = %q, want high-contrast selected match", cell.style)
	}
	screen.color = true
	if sequence := screen.ansi(tuiSelectMatch); sequence != "\033[1;4;30;46m" {
		t.Fatalf("color selected match ANSI = %q", sequence)
	}
	screen.color = false
	if sequence := screen.ansi(tuiSelectMatch); sequence != "\033[1;4;7m" {
		t.Fatalf("no-color selected match ANSI = %q", sequence)
	}
}

func TestTUIRunEventsUpdateQueueAndState(t *testing.T) {
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
}

func TestTUIDownloadProgressUpdatesExecutionQueue(t *testing.T) {
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
}

func TestTUIDownloadStartRendersZeroProgressBeforeFirstProgressEvent(t *testing.T) {
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
}

func TestTUIRunObserverForwardsDownloadProgressEvent(t *testing.T) {
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
}

func TestTUIRunObserverSelectsStandardLevelForEachResultEvent(t *testing.T) {
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
}

func TestTUIWriterStreamsCarriageReturnProgress(t *testing.T) {
	events := make(chan tuiEvent, 4)
	writer := &tuiWriter{events: events}
	if _, err := writer.Write([]byte("10%\r20%\n")); err != nil {
		t.Fatal(err)
	}
	first, second := <-events, <-events
	if first.text != "10%" || second.text != "20%" || first.logLevel != LogInfo || second.logLevel != LogInfo {
		t.Fatalf("progress events = %q, %q", first.text, second.text)
	}
}

func TestTUIWriterBoundsUnterminatedLines(t *testing.T) {
	events := make(chan tuiEvent, 4)
	writer := &tuiWriter{events: events}
	if _, err := writer.Write([]byte(strings.Repeat("x", tuiMaxLogLineBytes+10))); err != nil {
		t.Fatal(err)
	}
	event := <-events
	if len(event.text) > tuiMaxLogLineBytes+len("…") || len(writer.buffer) > tuiMaxLogLineBytes {
		t.Fatalf("unbounded log line: event=%d buffer=%d", len(event.text), len(writer.buffer))
	}
}

func TestStartTUIRunCanCheckAllApps(t *testing.T) {
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
	if !request.CheckOnly || request.Names != nil {
		t.Fatalf("request = %#v", request)
	}
	if view.queue["obsidian"].Status != "waiting" {
		t.Fatalf("all-apps run did not create waiting queue entries: %#v", view.queue)
	}
}

func TestStartTUIRunFailureRollsBackWaitingQueue(t *testing.T) {
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
}

func TestTUIAddsAnotherApplicationToActiveBatch(t *testing.T) {
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
}

func TestTUIAddsAllApplicationsWhenOnlyPartOfWorkerPoolIsOccupied(t *testing.T) {
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
	if !request.CheckOnly || len(request.Names) != 11 || containsString(request.Names, "obsidian") {
		t.Fatalf("all-applications request = %#v", request)
	}
	if len(view.queue) != 12 {
		t.Fatalf("all applications did not join the shared queue: %d", len(view.queue))
	}
	if strings.Contains(view.message, "容量不足") {
		t.Fatalf("partially occupied worker pool rejected a bulk request: %q", view.message)
	}
}

func TestTUICompletedApplicationCanRejoinActiveQueue(t *testing.T) {
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
}

func TestTUIRejectsAdditionWhenWorkerPoolIsFull(t *testing.T) {
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
}

func TestTUIExitKeysCancelWorkersBeforeLeaving(t *testing.T) {
	for _, test := range []struct {
		key         string
		quitPending bool
	}{
		{key: "esc", quitPending: false},
		{key: "q", quitPending: true},
		{key: "ctrl+c", quitPending: false},
	} {
		t.Run(test.key, func(t *testing.T) {
			view := sampleTUIView()
			view.running = true
			cancelled := false
			view.cancel = func() { cancelled = true }
			if quit := handleTUIKey(context.Background(), &view, test.key, TUIActions{}, make(chan tuiEvent, 1)); quit {
				t.Fatal("TUI exited before active workers reported completion")
			}
			if cancelled != (test.key != "ctrl+c") || view.quitPending != test.quitPending {
				t.Fatalf("cancelled=%v quitPending=%v", cancelled, view.quitPending)
			}
		})
	}
}

func TestTUIInputFailureCancelsWorkersBeforeLeaving(t *testing.T) {
	view := sampleTUIView()
	view.running = true
	cancelled := false
	view.cancel = func() { cancelled = true }
	quit := handleTUIEvent(context.Background(), &view, tuiEvent{eventType: "input_error", err: errors.New("input failed")}, TUIActions{}, make(chan tuiEvent, 1))
	if quit || !cancelled || !view.quitPending {
		t.Fatalf("quit=%v cancelled=%v quitPending=%v", quit, cancelled, view.quitPending)
	}
}

func sampleTUIView() tuiModel {
	trueValue := true
	catalog := model.Config{
		SchemaVersion: model.SchemaVersion,
		Settings: model.Settings{
			Language: "zh", TimeoutSeconds: 300, Workers: 4,
			HTTP:         &model.HTTPSettings{TimeoutSeconds: 60, MaxConcurrencyPerHost: 2, Retries: 2},
			Downloader:   model.DownloaderSettings{CLI: "aria2c", StorePath: "~/Downloads"},
			LogDir:       "./logs",
			ProviderURLs: map[string]string{"github_release": "https://api.github.com/repos/{package}/releases/latest"},
			Scan:         model.ScanSettings{Path: trueValue, Application: trueValue, Packages: model.PackageScanSettings{Python: trueValue}},
		},
		Apps: []model.Application{{ID: "obsidian", Name: "Obsidian", Type: "application", InstallPath: "/Applications/Obsidian.app", Enabled: true, UpdateMode: model.ModeDownload, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, StatusManaged: model.ManagedStatus{CurrentVersion: "1.13.6", LatestVersion: "1.13.7", HasUpdate: true, UpdateStatus: "update_available"}}},
	}
	state := model.RuntimeState{Observations: map[string]model.ScanObservation{"obsidian": {Found: true, Path: "/Applications/Obsidian.app"}}}
	return tuiModel{
		appsPageState: appsPageState{catalog: catalog, state: state}, configPageState: configPageState{working: cloneConfig(catalog)},
		runState: runState{queue: map[string]model.Result{}},
	}
}

func stripTUIANSI(value string) string {
	var output strings.Builder
	for index := 0; index < len(value); {
		if value[index] == 0x1b && index+1 < len(value) && value[index+1] == '[' {
			index += 2
			sequenceStart := index
			for index < len(value) {
				char := value[index]
				index++
				if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') {
					if char == 'H' && strings.HasSuffix(value[sequenceStart:index-1], ";1") && output.Len() > 0 {
						output.WriteByte('\n')
					}
					break
				}
			}
			continue
		}
		output.WriteByte(value[index])
		index++
	}
	return output.String()
}

func TestCommandOutputRouterPersistsOneCompleteRecordPerCommand(t *testing.T) {
	type persisted struct{ operation, appID, appName, message string }
	var events []tuiEvent
	var saved []persisted
	outputEvents := make(chan tuiEvent, 8)
	router := &tuiCommandOutputRouter{
		commands: map[tuiCommandOutputKey]*tuiCommandOutputState{}, events: outputEvents,
		format: func(_ model.Config, _ string, _ string, _ string, message string) ([]string, error) {
			return []string{message}, nil
		},
		open: func(_ model.Config, _ string, operation, appID, appName string) (io.WriteCloser, error) {
			var buffer bytes.Buffer
			return testOutputWriteCloser{Writer: &buffer, close: func() error {
				saved = append(saved, persisted{operation, appID, appName, buffer.String()})
				return nil
			}}, nil
		},
	}
	router.Write(model.CommandOutput{CommandID: 1, AppID: "sample", AppName: "Sample", Operation: model.OperationCheck, Stream: "stdout", Data: []byte("one\n")})
	router.Write(model.CommandOutput{CommandID: 1, AppID: "sample", AppName: "Sample", Operation: model.OperationCheck, Stream: "stderr", Data: []byte("warn\n")})
	router.Write(model.CommandOutput{CommandID: 1, AppID: "sample", AppName: "Sample", Operation: model.OperationCheck, Stream: "stdout", Data: []byte("two\n"), Done: true})
	router.Flush()
	for len(outputEvents) > 0 {
		events = append(events, <-outputEvents)
	}
	if len(events) != 3 {
		t.Fatalf("display events = %#v, want one per line", events)
	}
	if len(saved) != 1 {
		t.Fatalf("persisted = %#v, want one event per command", saved)
	}
	if saved[0].operation != model.OperationCheck || saved[0].appID != "sample" || saved[0].appName != "Sample" {
		t.Fatalf("persisted identity = %#v", saved[0])
	}
	if saved[0].message != "one\nwarn\ntwo\n" {
		t.Fatalf("command aggregation lost arrival order: %#v", saved)
	}
	router.Write(model.CommandOutput{CommandID: 2, AppID: "silent", AppName: "Silent", Operation: model.OperationCheck, Done: true})
	if len(saved) != 2 || saved[1].appID != "silent" || saved[1].message != "" {
		t.Fatalf("silent command record = %#v", saved)
	}
}

func TestDownloadOutputUsesConcreteApplicationAndPersistsOneCompleteRecord(t *testing.T) {
	type persisted struct{ operation, appID, appName, message string }
	var saved []persisted
	events := make(chan tuiEvent, 8)
	view := sampleTUIView()
	view.operationText = func(_ model.Config, _ string, operation, subject, message string) ([]string, error) {
		return []string{operation + "|" + subject + "|" + strings.TrimSpace(message)}, nil
	}
	view.commandOutputWriter = func(_ model.Config, _ string, operation, appID, appName string) (io.WriteCloser, error) {
		var buffer bytes.Buffer
		return testOutputWriteCloser{Writer: &buffer, close: func() error {
			saved = append(saved, persisted{operation, appID, appName, buffer.String()})
			return nil
		}}, nil
	}

	app := model.Application{ID: "drawio", Name: "draw.io"}
	stdout, stderr := newTUIDownloadOutput(view.catalog, view.operationText, view.commandOutputWriter, events, app)
	_, _ = stdout.Write([]byte("Download Results:\nrow one\n"))
	_, _ = stderr.Write([]byte("warning line\n"))
	_, _ = stdout.Write([]byte("download completed\n"))
	_ = stdout.Close()
	_ = stderr.Close()

	if len(saved) != 1 {
		t.Fatalf("persisted = %#v, want one record for the download command", saved)
	}
	wantMessage := "Download Results:\nrow one\nwarning line\ndownload completed\n"
	if saved[0] != (persisted{model.OperationDownload, app.ID, app.Name, wantMessage}) {
		t.Fatalf("persisted download output = %#v", saved)
	}
	for len(events) > 0 {
		event := <-events
		if !strings.Contains(event.text, model.OperationDownload+"|"+app.Name+"|") || strings.Contains(event.text, i18n.T("tui.all_apps")) {
			t.Fatalf("download display identity = %#v", event)
		}
	}
}

func TestDownloadOutputPersistenceFailureDoesNotInterruptDownload(t *testing.T) {
	events := make(chan tuiEvent, 2)
	format := func(_ model.Config, _ string, _ string, _ string, message string) ([]string, error) {
		return []string{message}, nil
	}
	open := func(model.Config, string, string, string, string) (io.WriteCloser, error) {
		return testOutputWriteCloser{Writer: failingTestWriter{}, close: func() error { return errors.New("close failed") }}, nil
	}
	stdout, stderr := newTUIDownloadOutput(model.Config{}, format, open, events, model.Application{ID: "app", Name: "App"})
	data := []byte("download output\n")
	if written, err := stdout.Write(data); written != len(data) || err != nil {
		t.Fatalf("write result = %d, %v", written, err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatalf("stdout close error = %v", err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatalf("stderr close error = %v", err)
	}
	if event := <-events; event.text != "download output" {
		t.Fatalf("display event = %#v", event)
	}
}

func TestCommandOutputRouterKeepsConcurrentApplicationIdentityAndOperation(t *testing.T) {
	type persisted struct{ operation, appID, appName, message string }
	var saved []persisted
	events := make(chan tuiEvent, 4)
	router := &tuiCommandOutputRouter{
		commands: map[tuiCommandOutputKey]*tuiCommandOutputState{}, events: events,
		format: func(_ model.Config, _ string, operation, subject, message string) ([]string, error) {
			return []string{operation + "|" + subject + "|" + strings.TrimSpace(message)}, nil
		},
		open: func(_ model.Config, _ string, operation, appID, appName string) (io.WriteCloser, error) {
			var buffer bytes.Buffer
			return testOutputWriteCloser{Writer: &buffer, close: func() error {
				saved = append(saved, persisted{operation, appID, appName, buffer.String()})
				return nil
			}}, nil
		},
	}

	router.Write(model.CommandOutput{CommandID: 7, AppID: "osv", AppName: "osv-scanner", Operation: model.OperationUpdate, Data: []byte("go: downloading\n")})
	router.Write(model.CommandOutput{CommandID: 7, AppID: "codex", AppName: "OpenAI Codex CLI", Operation: model.OperationCheck, Data: []byte("codex-cli 0.149.1\n")})
	router.Write(model.CommandOutput{CommandID: 7, AppID: "codex", AppName: "OpenAI Codex CLI", Operation: model.OperationCheck, Done: true})
	router.Write(model.CommandOutput{CommandID: 7, AppID: "osv", AppName: "osv-scanner", Operation: model.OperationUpdate, Done: true})

	first, second := <-events, <-events
	if first.text != "update|osv-scanner|go: downloading" || second.text != "check|OpenAI Codex CLI|codex-cli 0.149.1" {
		t.Fatalf("display identity crossed: %q, %q", first.text, second.text)
	}
	if len(saved) != 2 || saved[0] != (persisted{model.OperationCheck, "codex", "OpenAI Codex CLI", "codex-cli 0.149.1\n"}) || saved[1] != (persisted{model.OperationUpdate, "osv", "osv-scanner", "go: downloading\n"}) {
		t.Fatalf("persisted identity crossed: %#v", saved)
	}
}

func TestTUIWriterFlushDoesNotBlockWhenDroppedNotificationQueueIsFull(t *testing.T) {
	events := make(chan tuiEvent, 1)
	events <- tuiEvent{}
	writer := &tuiWriter{events: events}
	_, _ = writer.Write([]byte(strings.Repeat("line\n", 300)))
	done := make(chan struct{}, 1)
	go func() { writer.Flush(); close(done) }()
	select {
	case <-done:
		if writer.dropped == 0 {
			t.Fatal("Flush discarded an undelivered dropped-output count")
		}
	case <-time.After(time.Second):
		t.Fatal("Flush blocked on a full event queue")
	}
}

func TestTUIWriterFormattingFailureFallsBackToDisplayEvent(t *testing.T) {
	events := make(chan tuiEvent, 1)
	writer := &tuiWriter{
		events: events, level: LogInfo, operation: model.OperationCheck, subject: "Sample",
		format: func(model.Config, string, string, string, string) ([]string, error) {
			return nil, errors.New("logger unavailable")
		},
	}
	_, _ = writer.Write([]byte("command output\n"))
	event := <-events
	if event.eventType != tuiEventLog || event.operation != model.OperationCheck || event.subject != "Sample" || event.text != "command output" {
		t.Fatalf("fallback event = %#v", event)
	}
}

func TestStructuredLogPersistenceFailureKeepsRedactedDisplayLines(t *testing.T) {
	view := sampleTUIView()
	view.operationLog = func(model.Config, string, string, string, string) ([]string, error) {
		return []string{"redacted [REDACTED]"}, errors.New("persist failed")
	}
	view.appendStructuredLog(LogInfo, model.OperationCheck, "secret", "message secret")
	if len(view.logs) != 1 || view.logs[0] != "redacted [REDACTED]" || strings.Contains(view.logs[0], "secret") {
		t.Fatalf("display logs = %#v", view.logs)
	}
}

func TestStructuredLogRespectsSuccessfulLevelFiltering(t *testing.T) {
	view := sampleTUIView()
	view.operationLog = func(model.Config, string, string, string, string) ([]string, error) {
		return nil, nil
	}
	view.appendStructuredLog(LogInfo, model.OperationCheck, "Sample", "filtered")
	if len(view.logs) != 0 {
		t.Fatalf("filtered log was displayed: %#v", view.logs)
	}
}

func TestStartTUIRunLoggingFailureDoesNotReplaceOperationFailure(t *testing.T) {
	view := sampleTUIView()
	events := make(chan tuiEvent, 256)
	view.operationText = func(model.Config, string, string, string, string) ([]string, error) { return []string{"line"}, nil }
	view.commandOutputWriter = func(model.Config, string, string, string, string) (io.WriteCloser, error) {
		return testOutputWriteCloser{Writer: io.Discard, close: func() error { return errors.New("persist failed") }}, nil
	}
	actions := TUIActions{
		StartRun: func(_ context.Context, _ TUIRunRequest, observer TUIObserver) (*TUIRunBatch, error) {
			for index := 0; index < 300; index++ {
				observer.CommandOutput(model.CommandOutput{CommandID: 1, AppID: "app", AppName: "App", Operation: model.OperationCheck, Stream: "stdout", Data: []byte("line\n")})
			}
			return nil, errors.New("start failed")
		},
	}
	done := make(chan struct{})
	go func() { startTUIRun(context.Background(), &view, true, false, actions, events); close(done) }()
	select {
	case <-done:
		if !view.messageError || !strings.Contains(view.message, "start failed") || strings.Contains(view.message, "persist failed") {
			t.Fatalf("message=%q error=%v", view.message, view.messageError)
		}
	case <-time.After(time.Second):
		t.Fatal("start failure did not return with a full event queue")
	}
}
