package ui

import (
	"reflect"
	"testing"

	"context"
	"github.com/eoctet/tendkit/internal/model"

	"errors"
	"unicode/utf8"

	"bytes"

	"strings"

	"github.com/eoctet/tendkit/pkg/i18n"
)

type reloadRequiredTestError struct{}

func (reloadRequiredTestError) Error() string        { return "external configuration change" }
func (reloadRequiredTestError) ReloadRequired() bool { return true }

func TestTUIConfigurationFlow(t *testing.T) {
	t.Run("tui-external-save-conflict-requires-confirmed-reload", func(t *testing.T) {
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
	})
	t.Run("tui-failed-reload-preserves-memory-snapshot", func(t *testing.T) {
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
	})
	t.Run("tui-render-honors-configured-color-mode", func(t *testing.T) {
		var output bytes.Buffer
		if screen := newTUIScreenForOutput(&output, ModeAlways, 80, 24); !screen.color {
			t.Fatal("always color mode did not enable TUI colors")
		}
		if screen := newTUIScreenForOutput(&output, ModeNever, 80, 24); screen.color {
			t.Fatal("never color mode enabled TUI colors")
		}
	})
	t.Run("render-tui-configuration-and-edit", func(t *testing.T) {
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
	})
	t.Run("tui-configuration-sections-and-modified-styles-clear-after-save", func(t *testing.T) {
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
	})
	t.Run("tui-configuration-application-modified-state-ignores-runtime-and-empty-slice-representation", func(t *testing.T) {
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
	})
	t.Run("tui-configuration-sections-scroll-at-minimum-terminal-size", func(t *testing.T) {
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
	})
	t.Run("tui-configuration-restores-basic-section-title-after-scroll-and-save", func(t *testing.T) {
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
	})
	t.Run("tui-application-configuration-stages-until-ctrl-s-save-and-apply", func(t *testing.T) {
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
	})
	t.Run("tui-configuration-edits-custom-bundle-id-list", func(t *testing.T) {
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
	})
	t.Run("tui-application-editors-share-normalization-and-validation", func(t *testing.T) {
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
	})
	t.Run("tui-application-configuration-parameter-names-are-dim", func(t *testing.T) {
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
	})
	t.Run("tui-settings-become-effective-only-after-ctrl-s-save", func(t *testing.T) {
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
	})
	t.Run("tui-ctrl-s-saves-the-active-editor", func(t *testing.T) {
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
	})
	t.Run("tui-language-save-uses-the-new-language-for-confirmation", func(t *testing.T) {
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
	})
	t.Run("tui-leaving-configuration-clears-the-header-message", func(t *testing.T) {
		useLanguage(t, i18n.Chinese)
		view := sampleTUIView()
		view.page = tuiConfig
		view.setMessage(i18n.T("tui.saved"), false)

		handleConfigKey(&view, "esc", TUIActions{})

		if view.page != tuiApps || view.message != "" || view.messageError || !view.messageUntil.IsZero() {
			t.Fatalf("leaving configuration retained its header message: %#v", view)
		}
	})
	t.Run("tui-leaving-dirty-configuration-requires-save-or-discard", func(t *testing.T) {
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
	})
	t.Run("tui-config-nested-escape-does-not-prompt-for-page-exit", func(t *testing.T) {
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
	})
	t.Run("tui-application-download-fields-and-environment-editing-are-strict", func(t *testing.T) {
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
	})
	t.Run("tuidownloader-fields-are-split-and-extra-arguments-use-comma-list", func(t *testing.T) {
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
	})
	t.Run("tui-config-application-rows-use-normal-text-and-right-panel-uses-eighty-twenty-split", func(t *testing.T) {
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
	})
}
func TestScanApplicationSettingIsReadOnlyOnlyOutsideMacOS(t *testing.T) {
	previous := tuiSettingsSupportApplicationBundles
	t.Cleanup(func() { tuiSettingsSupportApplicationBundles = previous })
	tuiSettingsSupportApplicationBundles = func() bool { return false }
	if got := scanApplicationRowType(); got != configRowReadOnly {
		t.Fatalf("non-macOS row type = %v, want read-only", got)
	}
	tuiSettingsSupportApplicationBundles = func() bool { return true }
	if got := scanApplicationRowType(); got != configRowBoolean {
		t.Fatalf("macOS row type = %v, want boolean", got)
	}
}
