package ui

import (
	"context"
	"errors"
	"maps"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"

	"strings"
	"testing"
	"time"
)

func TestTUIScanResolutionFlow(t *testing.T) {
	t.Run("conflict-keys-ignore-no-difference-or-detail-focus", func(t *testing.T) {
		for _, focused := range []bool{false, true} {
			for _, key := range []string{"a", "p", "k"} {
				view := sampleTUIView()
				view.page, view.detailFocus = tuiScan, focused
				if !focused {
					other := view.catalog.Apps[0]
					other.ID, other.Name = "other", "Other"
					candidate := other
					candidate.Description = "changed elsewhere"
					view.catalog.Apps = append(view.catalog.Apps, other)
					view.scanProposed = map[string]model.Application{other.ID: candidate}
					view.scanChanges = map[string]model.ScanApplicationChange{other.ID: {Current: other, Proposed: candidate, Fields: []model.ScanFieldChange{{Field: "description", Current: other.Description, Proposed: candidate.Description}}}}
				}
				handleScanKey(context.Background(), &view, key, TUIActions{}, make(chan tuiEvent, 1))
				if view.scanConfirm != "" || view.scanPartial || view.scanIgnored["obsidian"] {
					t.Fatalf("key %q changed inactive conflict: %#v", key, view)
				}
			}
		}
	})
	t.Run("cancelled-scan-keeps-preview-and-publishes-completion", func(t *testing.T) {
		view := sampleTUIView()
		view.page = tuiScan
		proposed := view.catalog.Apps[0]
		proposed.Description = "pending"
		view.scanProposed = map[string]model.Application{proposed.ID: proposed}
		view.scanChanges = map[string]model.ScanApplicationChange{proposed.ID: {Current: view.catalog.Apps[0], Proposed: proposed, Fields: []model.ScanFieldChange{{Field: "description", Proposed: proposed.Description}}}}
		events := make(chan tuiEvent, 2)
		actions := TUIActions{Scan: func(ctx context.Context, _ TUIScanRequest, _ TUIScanObserver) (TUIScanSnapshot, error) {
			<-ctx.Done()
			return TUIScanSnapshot{}, ctx.Err()
		}}
		startTUIScan(context.Background(), &view, actions, events, "")
		view.scanCancel()
		select {
		case event := <-events:
			if event.eventType != "scan_done" || !errors.Is(event.err, context.Canceled) {
				t.Fatalf("cancel event = %#v", event)
			}
			handleTUIEvent(context.Background(), &view, event, actions, events)
		case <-time.After(time.Second):
			t.Fatal("scan cancellation did not publish completion")
		}
		if !hasScanChange(&view, proposed.ID) || view.scanProposed[proposed.ID].Description != proposed.Description {
			t.Fatalf("cancellation discarded preview: %#v", view.scanChanges)
		}
	})
	t.Run("single-scan-delta-preserves-unrelated-pending-state", func(t *testing.T) {
		view := sampleTUIView()
		view.page = tuiScan
		target := view.catalog.Apps[0]
		other := target
		other.ID, other.Name = "other", "Other"
		view.catalog.Apps = append(view.catalog.Apps, other)
		view.working = cloneConfig(view.catalog)
		pending := other
		pending.Description = "pending"
		view.scanProposed = map[string]model.Application{other.ID: pending}
		view.scanChanges = map[string]model.ScanApplicationChange{other.ID: {Current: other, Proposed: pending, Fields: []model.ScanFieldChange{{Field: "description", Proposed: pending.Description}}}}
		view.scanCompleted = true
		view.scanObservations = map[string]model.ScanObservation{target.ID: {Found: true, Path: target.InstallPath}, other.ID: {Found: true, Path: other.InstallPath}}
		view.state.Observations = maps.Clone(view.scanObservations)
		base := cloneConfig(view.catalog)
		candidate := cloneConfig(base)
		updated, _ := findApplication(&candidate, target.ID)
		updated.StatusManaged.CurrentVersion = "2.0.0"
		finishTUIScan(&view, tuiEvent{eventType: "scan_done", key: target.ID, scan: TUIScanSnapshot{BaseConfig: base, BaseState: model.RuntimeState{Observations: map[string]model.ScanObservation{target.ID: {Found: true, Path: target.InstallPath}}}, Config: candidate, State: model.RuntimeState{Observations: map[string]model.ScanObservation{target.ID: {Found: true, Path: target.InstallPath}}}}})
		if !hasScanChange(&view, other.ID) || view.scanProposed[other.ID].Description != "pending" || scanApplicationInvalid(&view, other.ID) || !view.state.Observations[other.ID].Found {
			t.Fatalf("target delta replaced unrelated state: %#v", view)
		}
	})
	t.Run("ignored-scan-difference-stays-resolved-after-automatic-scan", func(t *testing.T) {
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
	})
	t.Run("tui-scan-keep-persists-per-field-and-requires-save", func(t *testing.T) {
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
	})
	t.Run("tui-scan-keep-merges-fields-across-rounds", func(t *testing.T) {
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
	})
	t.Run("tui-scan-keep-reappears-when-scan-snapshot-context-changes", func(t *testing.T) {
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
	})
	t.Run("remove-resolved-scan-candidates-does-not-mutate-fingerprint-context", func(t *testing.T) {
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
	})
	t.Run("tui-scan-k-is-reserved-for-keep-instead-of-navigation", func(t *testing.T) {
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
	})
	t.Run("tui-scan-managed-toggle-clears-keeps-only-when-unmanaging", func(t *testing.T) {
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
	})
	t.Run("tui-scan-managed-toggle-keeps-unrelated-differences", func(t *testing.T) {
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
	})
	t.Run("tui-scan-selection-keeps-unselected-apps-and-supports-partial-merge", func(t *testing.T) {
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

		handleScanPartialKey(&view, "enter")
		if view.scanConfirm != scanConfirmPartial || saves != 0 {
			t.Fatalf("partial merge skipped confirmation: confirm=%q saves=%d", view.scanConfirm, saves)
		}
		handleScanConfirmationKey(&view, "enter", actions)
		remaining := view.scanChanges[current.ID]
		if saves != 1 || view.scanPartial || view.catalog.Apps[0].Description != proposed.Description || view.catalog.Apps[0].InstallPath != current.InstallPath || len(remaining.Fields) != 1 || remaining.Fields[0].Field != "install_path" {
			t.Fatalf("partial merge result: saves=%d view=%#v", saves, view)
		}
	})
	t.Run("tui-scan-conflict-supports-merge-all-and-keep-current", func(t *testing.T) {
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
	})
	t.Run("tui-scan-existing-application-exclusion-keeps-catalog-record", func(t *testing.T) {
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
	})
	t.Run("tui-scan-add-all-candidates-keeps-whole-batch-when-save-fails", func(t *testing.T) {
		view := scanAddAllTestView()
		actions := TUIActions{SaveScan: func(_ model.Config, _ model.Config) (model.Config, error) {
			return model.Config{}, errors.New("save failed")
		}}
		handleScanKey(context.Background(), &view, "a", actions, make(chan tuiEvent, 1))
		handleScanConfirmationKey(&view, "enter", actions)
		if len(view.catalog.Apps) != 1 || !view.scanAdded["new-alpha"] || !view.scanAdded["new-beta"] || !view.messageError || !strings.Contains(view.message, "save failed") {
			t.Fatalf("failed add-all save changed candidates: apps=%#v added=%#v message=%q", view.catalog.Apps, view.scanAdded, view.message)
		}
	})
	t.Run("tui-scan-identity-generation-keeps-unrelated-differences", func(t *testing.T) {
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
	})
}
