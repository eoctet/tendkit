package ui

import (
	"github.com/eoctet/tendkit/internal/model"
	"strings"

	"os"
	"testing"

	"bytes"

	"errors"

	"fmt"
	"time"

	"unicode/utf8"

	"context"

	"github.com/eoctet/tendkit/pkg/i18n"
)

func decodeTUIKeys(data []byte) []string {
	return (&tuiInputDecoder{}).decode(data)
}

// sampleTUIView is the shared baseline for user-input and page-flow scenarios.
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
	return tuiModel{appsPageState: appsPageState{catalog: catalog, state: state}, configPageState: configPageState{working: cloneConfig(catalog)}, runState: runState{queue: map[string]model.Result{}}}
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

func useLanguage(t *testing.T, language i18n.Language) {
	t.Helper()
	previous := i18n.Current()
	i18n.Set(language)
	t.Cleanup(func() { i18n.Set(previous) })
}

func TestTUIInputContract(t *testing.T) {
	t.Run("decode-tui-keys", func(t *testing.T) {
		keys := decodeTUIKeys([]byte("\x1b[A\x1b[B\x1b[C\x1b[D\x1b[5~\x1b[6~\x1b[3~\x1b[H\x1b[F\t\x03\x13\x15\r界"))
		want := []string{"up", "down", "right", "left", "pageup", "pagedown", "delete", "home", "end", "tab", "ctrl+c", "ctrl+s", "ctrl+u", "enter", "界"}
		if strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Fatalf("keys = %#v, want %#v", keys, want)
		}
	})
	t.Run("tui-input-decoder-preserves-split-csi-and-utf8", func(t *testing.T) {
		decoder := tuiInputDecoder{}
		var keys []string
		for _, chunk := range [][]byte{{0x1b}, {'['}, {'A', 0xe7}, {0x95}, {0x8c}} {
			keys = append(keys, decoder.decode(chunk)...)
		}
		want := []string{"up", "界"}
		if strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Fatalf("keys = %#v, want %#v", keys, want)
		}
	})
	t.Run("tui-input-decoder-flushes-incomplete-csi-as-escape-and-text", func(t *testing.T) {
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
	})
	t.Run("read-tui-input-flushes-standalone-escape", func(t *testing.T) {
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
	})
	t.Run("read-tui-input-flushes-incomplete-csion-eof", func(t *testing.T) {
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
	})
	t.Run("read-tui-input-flushes-incomplete-csion-timeout", func(t *testing.T) {
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
	})
	t.Run("read-tui-input-stops-after-cancel", func(t *testing.T) {
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
	})
	t.Run("tui-shortcuts-use-lowercase-input-and-lowercase-combinations", func(t *testing.T) {
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
	})
	t.Run("tui-keymap-blocks-hidden-state-actions", func(t *testing.T) {
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
	})
	t.Run("tui-keymap-matches-visible-modal-and-partial-bindings", func(t *testing.T) {
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
	})
	t.Run("tui-stateful-keymap-event-matrix", func(t *testing.T) {
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
	})
	t.Run("tui-stateful-keymap-footer-wraps-all-bindings-at-eighty-columns", func(t *testing.T) {
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
	})
	t.Run("tui-keymap-text-and-small-terminal-safety", func(t *testing.T) {
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
	})
	t.Run("tui-key-labels-use-uppercase-presentation", func(t *testing.T) {
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
	})
	t.Run("tui-string-editor-moves-and-edits-unicode-at-cursor", func(t *testing.T) {
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
	})
	t.Run("edit-value-viewport-keeps-cursor-visible-for-long-unicode", func(t *testing.T) {
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
	})
}
func TestTUIKeymapPermissionsAndFooterWrapping(t *testing.T) {
	keymap := newTUIKeymap(
		tuiKey("tui.key.select", "up", "down"),
		tuiDynamicKey([]string{"enter"}, func() string { return "ENTER apply" }),
		tuiTextKeyMatching("tui.key.type", func(value string) bool { return value != "!" }),
	)
	if !keymap.Permits("up") || !keymap.Permits("中") || keymap.Permits("!") || keymap.Permits("esc") {
		t.Fatal("keymap permissions do not match bindings")
	}
	lines := keymap.FooterLines(20)
	if len(lines) < 2 || !strings.Contains(strings.Join(lines, " "), "ENTER apply") {
		t.Fatalf("footer lines = %#v", lines)
	}
	if !newTUIKeymap(tuiTextKey("tui.key.type")).Permits("x") {
		t.Fatal("unfiltered text binding rejected text")
	}
}
