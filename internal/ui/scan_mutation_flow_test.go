package ui

import (
	"strings"
	"testing"

	"bytes"
	"context"
	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

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
	view.scanChanges = map[string]model.ScanApplicationChange{conflict.ID: {Current: view.catalog.Apps[0], Proposed: conflict, Fields: []model.ScanFieldChange{{Field: "description", Current: "current", Proposed: "candidate"}}}}
	view.scanAdded = map[string]bool{alpha.ID: true, beta.ID: true}
	view.scanSelected = 1
	ensureScanMaps(&view)
	return view
}

func TestTUIScanMutationFlow(t *testing.T) {
	t.Run("tui-scan-added-candidate-edit-binding-matches-handler", func(t *testing.T) {
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
	})
	t.Run("tui-scan-log-quit-cancels-only-running-scan", func(t *testing.T) {
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
	})
	t.Run("tui-scan-page-during-update-run-has-only-browse-and-exit-keys", func(t *testing.T) {
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
	})
	t.Run("scan-application-list-uses-color-without-name-prefixes", func(t *testing.T) {
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
	})
	t.Run("empty-scan-panels-place-guidance-on-second-content-row", func(t *testing.T) {
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
	})
	t.Run("tui-scan-new-candidate-can-be-edited-added-or-excluded", func(t *testing.T) {
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
	})
	t.Run("tui-scan-add-all-candidates-rejects-whole-batch-when-one-is-invalid", func(t *testing.T) {
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
	})
	t.Run("tui-scan-delete-without-exclusion-can-be-rediscovered", func(t *testing.T) {
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
	})
	t.Run("tui-scan-identity-checks-implicit-global-identity", func(t *testing.T) {
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
	})
	t.Run("tui-scan-identity-binding-only-appears-when-identity-is-missing", func(t *testing.T) {
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
	})
	t.Run("apply-tui-scan-field-applies-provider-fields-independently", func(t *testing.T) {
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
	})
	t.Run("tui-scan-candidate-edit-snapshot-stages-resets-and-discards", func(t *testing.T) {
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
	})
	t.Run("tui-scan-candidate-enums-environment-and-identity-validation", func(t *testing.T) {
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
	})
	t.Run("tui-scan-candidate-parameter-labels-are-dim", func(t *testing.T) {
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
	})
}
