package ui

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

const (
	scanConfirmDelete        = "delete"
	scanConfirmDeleteExclude = "delete_exclude"
	scanConfirmExclude       = "exclude"
	scanConfirmIdentity      = "identity"
	scanConfirmMerge         = "merge"
	scanConfirmPartial       = "partial"
	scanConfirmKeep          = "keep"
	scanConfirmAdd           = "add"
	scanConfirmAddAll        = "add_all"
	scanConfirmRescan        = "rescan"
)

func startTUIScan(parent context.Context, view *tuiModel, actions TUIActions, events chan<- tuiEvent, appID string) {
	view.clearTUIQuickSearch()
	if view.running || view.scanRunning {
		view.setMessage(i18n.T("tui.operation_running"), false)
		return
	}
	if actions.Scan == nil {
		view.setMessage(i18n.T("tui.backend_missing"), true)
		return
	}
	request := TUIScanRequest{}
	if application, found := scanApplicationByID(view, appID); found && appID != "" {
		candidate := application
		request.Application = &candidate
	}
	view.scanLogs = nil
	view.scanLogFocus = false
	view.scanLogOffset = 0
	clearScanCandidateEdit(view)
	scanContext, cancel := context.WithCancel(parent)
	view.scanRunning, view.scanCancel = true, cancel
	view.setMessage(i18n.T("tui.scan.running"), false)
	subject := i18n.T("tui.scan.scope_all")
	if request.Application != nil {
		subject = request.Application.Name
	}
	view.appendScanStructuredLog(LogInfo, subject, i18n.T("tui.scan.started"))
	go func() {
		observer := TUIScanObserver{Progress: func(stage, subject string) {
			select {
			case events <- tuiEvent{eventType: tuiEventScanProgress, stage: stage, subject: subject}:
			case <-scanContext.Done():
			}
		}}
		snapshot, err := actions.Scan(scanContext, request, observer)
		events <- tuiEvent{eventType: tuiEventScanDone, key: appID, scan: snapshot, err: err}
	}()
}

func finishTUIScan(view *tuiModel, event tuiEvent) {
	view.scanRunning = false
	view.scanCancel = nil
	if event.err != nil {
		if errors.Is(event.err, context.Canceled) {
			view.appendScanStructuredLog(LogWarn, i18n.T("tui.scan.scope_all"), i18n.T("tui.scan.cancelled"))
			view.setMessage(i18n.T("tui.scan.cancelled"), false)
		} else {
			view.setMessage(i18n.ErrorText(event.err), true)
		}
		return
	}
	ensureScanMaps(view)
	view.catalog = event.scan.BaseConfig
	view.working = cloneConfig(event.scan.BaseConfig)
	if event.key == "" {
		view.state = event.scan.BaseState
	} else {
		view.state = mergeSingleScanState(view.state, event.scan.BaseState, event.key)
	}
	view.dirty = false
	view.scanCompleted = true
	if event.key == "" {
		view.scanAutoDone = true
	}
	clearScanCandidateEdit(view)
	view.scanLogOffset = 0
	if event.key == "" {
		view.scanProposed = applicationMap(event.scan.Config.Apps)
		view.scanObservations = cloneScanObservations(event.scan.State.Observations)
		view.scanChanges = changeMap(event.scan.Changes)
		view.scanAdded = applicationFlagMap(event.scan.Added)
		view.scanRemoved = applicationFlagMap(event.scan.Removed)
		view.scanExcluded = applicationFlagMap(event.scan.Excluded)
	} else {
		id := event.key
		delete(view.scanProposed, id)
		delete(view.scanObservations, id)
		delete(view.scanChanges, id)
		delete(view.scanAdded, id)
		delete(view.scanRemoved, id)
		delete(view.scanExcluded, id)
		delete(view.scanIgnored, id)
		if application, found := findApplicationValue(event.scan.Config.Apps, id); found {
			view.scanProposed[id] = application
		}
		if observation, found := event.scan.State.Observations[id]; found {
			view.scanObservations[id] = observation
		}
		if change, found := findScanChange(event.scan.Changes, id); found {
			view.scanChanges[id] = change
		}
		view.scanAdded[id] = containsApplication(event.scan.Added, id)
		view.scanRemoved[id] = containsApplication(event.scan.Removed, id)
		view.scanExcluded[id] = containsApplication(event.scan.Excluded, id)
	}
	removeResolvedScanCandidates(view)
	statistics := currentScanStatistics(view)
	subject := i18n.T("tui.scan.scope_all")
	if event.key != "" {
		statistics = currentScanApplicationStatistics(view, event.key)
		if application, found := scanApplicationByID(view, event.key); found {
			subject = application.Name
		}
	}
	view.scanSelected = max(0, min(view.scanSelected, len(scanDisplayApps(view))-1))
	view.scanDetail = 0
	message := i18n.T("tui.scan.finished", statistics.total, statistics.added, statistics.changed, statistics.excluded, statistics.invalid)
	view.appendScanStructuredLog(LogInfo, subject, message)
	view.setMessage(message, false)
}

func resetScanPreview(view *tuiModel, appID string) {
	ensureScanMaps(view)
	if appID == "" {
		view.scanProposed = map[string]model.Application{}
		view.scanObservations = map[string]model.ScanObservation{}
		view.scanChanges = map[string]model.ScanApplicationChange{}
		view.scanAdded = map[string]bool{}
		view.scanRemoved = map[string]bool{}
		view.scanExcluded = map[string]bool{}
		view.scanIgnored = map[string]bool{}
		return
	}
	delete(view.scanProposed, appID)
	delete(view.scanObservations, appID)
	delete(view.scanChanges, appID)
	delete(view.scanAdded, appID)
	delete(view.scanRemoved, appID)
	delete(view.scanExcluded, appID)
	delete(view.scanIgnored, appID)
}

func removeResolvedScanCandidates(view *tuiModel) {
	for id, change := range view.scanChanges {
		keeps := view.catalog.ScanVersionControl[id]
		allFields := append([]model.ScanFieldChange(nil), change.Fields...)
		remaining := make([]model.ScanFieldChange, 0, len(allFields))
		for _, field := range allFields {
			if resolution, found := keeps[field.Field]; !found || resolution.Fingerprint != model.ScanKeepFingerprint(id, change.Current, change.Proposed, allFields, field) {
				remaining = append(remaining, field)
			}
		}
		if len(remaining) == 0 {
			delete(view.scanChanges, id)
			continue
		}
		change.Fields = remaining
		view.scanChanges[id] = change
	}
}

func handleScanKey(parent context.Context, view *tuiModel, key string, actions TUIActions, events chan<- tuiEvent) bool {
	if view.scanLogFocus {
		return handleScanLogKey(view, key)
	}
	if view.scanEditFocus {
		return handleScanCandidateConfigKey(view, key)
	}
	if view.detailFocus {
		return handleScanDetailKey(view, key)
	}
	if key == "f" && !view.running && !view.scanRunning {
		view.searchActive = true
		return false
	}
	if handleScanExactAction(parent, view, key, actions, events) {
		return false
	}
	return handleScanListKey(parent, view, key, actions, events)
}

func handleScanLogKey(view *tuiModel, key string) bool {
	switch key {
	case "l":
		view.scanLogFocus = false
		view.scanLogOffset = 0
	case "up", "k":
		scrollTUIScanLogs(view, 1)
	case "down", "j":
		scrollTUIScanLogs(view, -1)
	case "pageup":
		scrollTUIScanLogs(view, max(1, tuiScanLogViewportHeight(view)-1))
	case "pagedown":
		scrollTUIScanLogs(view, -max(1, tuiScanLogViewportHeight(view)-1))
	case "home":
		view.scanLogOffset = tuiMaxScanLogOffset(view)
	case "end":
		view.scanLogOffset = 0
	case "q":
		return true
	}
	return false
}

func handleScanDetailKey(view *tuiModel, key string) bool {
	switch key {
	case "tab", "esc", "enter":
		view.detailFocus = false
	case "up", "k":
		view.scanDetail = max(0, view.scanDetail-1)
	case "down", "j":
		view.scanDetail++
	case "pageup":
		view.scanDetail = max(0, view.scanDetail-8)
	case "pagedown":
		view.scanDetail += 8
	case "home":
		view.scanDetail = 0
	case "end":
		view.scanDetail = 1 << 20
	case "q":
		return true
	}
	return false
}

func handleScanExactAction(parent context.Context, view *tuiModel, key string, actions TUIActions, events chan<- tuiEvent) bool {
	switch key {
	case "s":
		if view.scanAutoDone {
			view.scanConfirm, view.scanConfirmID = scanConfirmRescan, ""
			view.confirmChoice = tuiConfirmationPrimary
		} else {
			startTUIScan(parent, view, actions, events, "")
		}
	case "j":
		beginAddCandidateConfirmation(view)
	case "k":
		beginKeepConflictConfirmation(view)
	case "a":
		if _, added := selectedScanAdded(view); added {
			beginAddAllCandidatesConfirmation(view)
		} else {
			beginConflictAction(view, scanConfirmMerge)
		}
	case "p":
		if _, found := selectedScanConflict(view); found {
			beginPartialMerge(view)
		} else {
			view.setMessage(i18n.T("tui.scan.no_conflict"), false)
		}
	default:
		return false
	}
	return true
}

func beginAddCandidateConfirmation(view *tuiModel) {
	if application, found := selectedScanAdded(view); found {
		view.scanConfirmID, view.scanConfirm = application.ID, scanConfirmAdd
		view.confirmChoice = tuiConfirmationPrimary
	} else {
		view.setMessage(i18n.T("tui.scan.new_candidate_only"), false)
	}
}

func beginAddAllCandidatesConfirmation(view *tuiModel) {
	if len(pendingScanAddedIDs(view)) == 0 {
		view.setMessage(i18n.T("tui.scan.new_candidate_only"), false)
		return
	}
	view.scanConfirmID, view.scanConfirm = "", scanConfirmAddAll
	view.confirmChoice = tuiConfirmationPrimary
}

func beginKeepConflictConfirmation(view *tuiModel) {
	if application, found := selectedScanConflict(view); found {
		view.scanConfirmID, view.scanConfirm = application.ID, scanConfirmKeep
		view.confirmChoice = tuiConfirmationPrimary
	} else {
		view.setMessage(i18n.T("tui.scan.no_conflict"), false)
	}
}

func beginConflictAction(view *tuiModel, action string) {
	if _, found := selectedScanConflict(view); found {
		beginScanConfirmation(view, action)
	} else {
		view.setMessage(i18n.T("tui.scan.no_conflict"), false)
	}
}

func handleScanListKey(parent context.Context, view *tuiModel, key string, actions TUIActions, events chan<- tuiEvent) bool {
	apps := scanDisplayApps(view)
	switch key {
	case "q":
		return true
	case "esc":
		view.page = tuiApps
		view.clearTUIQuickSearch()
		view.scanLogs = nil
		view.clearMessage()
	case "up":
		view.scanSelected = max(0, view.scanSelected-1)
		view.scanDetail = 0
	case "down":
		if len(apps) > 0 {
			view.scanSelected = min(len(apps)-1, view.scanSelected+1)
		}
		view.scanDetail = 0
	case "pageup":
		view.scanSelected = max(0, view.scanSelected-10)
	case "pagedown":
		if len(apps) > 0 {
			view.scanSelected = min(len(apps)-1, view.scanSelected+10)
		}
	case "home":
		view.scanSelected = 0
	case "end":
		view.scanSelected = max(0, len(apps)-1)
	case "tab":
		view.detailFocus = true
	case "t":
		if application, found := selectedScanApplication(view); found {
			startTUIScan(parent, view, actions, events, application.ID)
		}
	case "l":
		view.scanLogFocus = true
		view.scanLogOffset = 0
	case "e":
		if application, found := selectedScanAdded(view); found {
			beginScanCandidateEdit(view, application)
		} else {
			view.setMessage(i18n.T("tui.scan.edit_new_only"), false)
		}
	case "d":
		if _, added := selectedScanAdded(view); added {
			view.setMessage(i18n.T("tui.scan.new_candidate_actions"), false)
		} else {
			beginScanConfirmation(view, scanConfirmDelete)
		}
	case "x":
		if application, found := selectedScanApplication(view); found {
			if _, existing := findApplicationValue(view.catalog.Apps, application.ID); existing && containsFoldValue(view.catalog.Settings.Scan.Exclude, application.ID) {
				persistScanUnexclusion(view, actions, application)
				break
			}
		}
		beginScanConfirmation(view, scanConfirmExclude)
	case "i":
		if _, added := selectedScanAdded(view); added {
			view.setMessage(i18n.T("tui.scan.new_candidate_actions"), false)
		} else {
			beginIdentityConfirmation(view, actions)
		}
	case "m":
		if application, found := selectedScanApplication(view); found {
			if view.scanAdded[application.ID] {
				view.setMessage(i18n.T("tui.scan.new_candidate_actions"), false)
			} else {
				view.scanConfirmID = application.ID
				persistScanManaged(view, actions)
			}
		}
	}
	return false
}

func selectedScanConflict(view *tuiModel) (model.Application, bool) {
	application, found := selectedScanApplication(view)
	return application, found && hasScanChange(view, application.ID)
}

func selectedScanAdded(view *tuiModel) (model.Application, bool) {
	application, found := selectedScanApplication(view)
	return application, found && view.scanAdded[application.ID] && !view.scanIgnored[application.ID]
}

func handleScanCandidateConfigKey(view *tuiModel, key string) bool {
	rows := scanCandidateConfigRows(view)
	if len(rows) == 0 {
		clearScanCandidateEdit(view)
		return false
	}
	view.scanFieldIndex = max(0, min(view.scanFieldIndex, len(rows)-1))
	switch key {
	case "esc":
		discardScanCandidateEdit(view)
	case "ctrl+s":
		stageScanCandidateEdit(view)
	case "r":
		resetScanCandidateEdit(view)
	case "up":
		view.scanFieldIndex = max(0, view.scanFieldIndex-1)
	case "down":
		view.scanFieldIndex = min(len(rows)-1, view.scanFieldIndex+1)
	case "pageup":
		view.scanFieldIndex = max(0, view.scanFieldIndex-6)
	case "pagedown":
		view.scanFieldIndex = min(len(rows)-1, view.scanFieldIndex+6)
	case "home":
		view.scanFieldIndex = 0
	case "end":
		view.scanFieldIndex = len(rows) - 1
	case "left":
		adjustScanCandidateConfig(view, rows[view.scanFieldIndex], -1)
	case "right":
		adjustScanCandidateConfig(view, rows[view.scanFieldIndex], 1)
	case "enter":
		row := rows[view.scanFieldIndex]
		if row.rowType == configRowReadOnly {
			view.setMessage(i18n.T("tui.readonly"), false)
			break
		}
		if row.rowType == configRowBoolean || row.rowType == configRowMode || row.rowType == configRowChoice {
			view.setMessage(i18n.T("tui.enum_keys_only"), false)
			break
		}
		if len(row.value) > tuiMaxEditValueBytes {
			view.setMessage(i18n.T("tui.edit_too_long", tuiMaxEditValueBytes), true)
			break
		}
		view.editing = true
		view.editValue = row.value
		view.editCursor = len([]rune(row.value))
	}
	return false
}

func scanCandidateConfigRows(view *tuiModel) []configRow {
	return scanCandidateConfigRowsFor(view, view.scanConfirmID)
}

func scanCandidateConfigRowsFor(view *tuiModel, id string) []configRow {
	application, found := view.scanProposed[id]
	if view.scanEditFocus && view.scanEditID == id {
		application, found = view.scanEditDraft, true
	}
	if !found || !view.scanAdded[application.ID] {
		return nil
	}
	catalog := model.Config{Apps: []model.Application{application}}
	rows := applicationConfigRows(&catalog, application.ID)
	for index := range rows {
		if rows[index].field == "environment" {
			rows[index].rowType = configRowString
			rows[index].value = formatTUIEnvironment(application.Environment)
		}
	}
	return rows
}

func applyScanCandidateConfigEdit(view *tuiModel, value string) error {
	rows := scanCandidateConfigRows(view)
	if len(rows) == 0 {
		return errors.New(i18n.T("tui.config_selection_missing"))
	}
	view.scanFieldIndex = max(0, min(view.scanFieldIndex, len(rows)-1))
	row := rows[view.scanFieldIndex]
	value, err := normalizeTUIApplicationField(row, value)
	if err != nil {
		return err
	}
	application, found := activeScanCandidateDraft(view)
	if !found {
		return errors.New(i18n.T("tui.config_selection_missing"))
	}
	catalog := model.Config{Apps: []model.Application{application}}
	if err := setConfigValue(&catalog, row, value); err != nil {
		return err
	}
	view.scanEditDraft = cloneScanApplication(catalog.Apps[0])
	return nil
}

func validateScanCandidateIdentity(view *tuiModel, candidateID, identity string) error {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil
	}
	for _, existing := range view.catalog.Apps {
		if existing.ID != candidateID && strings.EqualFold(strings.TrimSpace(existing.Identity), identity) {
			return errors.New(i18n.T("tui.scan.identity_conflict", identity, existing.Name))
		}
	}
	for id, existing := range view.scanProposed {
		if id != candidateID && strings.EqualFold(strings.TrimSpace(existing.Identity), identity) {
			return errors.New(i18n.T("tui.scan.identity_conflict", identity, existing.Name))
		}
	}
	return nil
}

func adjustScanCandidateConfig(view *tuiModel, row configRow, delta int) {
	var value string
	switch row.rowType {
	case configRowBoolean:
		value = boolText(row.value != i18n.T("value.yes"))
	case configRowMode:
		modes := []model.UpdateMode{model.ModeAuto, model.ModeDownload, model.ModeCheck, model.ModeInstall}
		current, _ := parseUpdateMode(row.value)
		index := 0
		for candidate := range modes {
			if modes[candidate] == current {
				index = candidate
				break
			}
		}
		value = string(modes[(index+delta+len(modes))%len(modes)])
	case configRowChoice:
		var exists bool
		value, exists = adjacentChoice(row.value, row.choices, delta)
		if !exists {
			return
		}
	default:
		return
	}
	application, found := activeScanCandidateDraft(view)
	if !found {
		view.setMessage(i18n.T("tui.config_selection_missing"), true)
		return
	}
	catalog := model.Config{Apps: []model.Application{application}}
	if err := setConfigValue(&catalog, row, value); err != nil {
		view.setMessage(i18n.ErrorText(err), true)
		return
	}
	view.scanEditDraft = cloneScanApplication(catalog.Apps[0])
	view.setMessage(i18n.T("tui.scan.candidate_modified"), false)
}

func beginScanCandidateEdit(view *tuiModel, application model.Application) {
	view.scanConfirmID = application.ID
	view.scanEditID = application.ID
	view.scanEditSnapshot = cloneScanApplication(application)
	view.scanEditDraft = cloneScanApplication(application)
	view.scanEditFocus = true
	view.scanFieldIndex, view.scanFieldScroll = 0, 0
}

func activeScanCandidateDraft(view *tuiModel) (model.Application, bool) {
	if !view.scanEditFocus || view.scanEditID == "" || view.scanEditDraft.ID != view.scanEditID {
		return model.Application{}, false
	}
	return view.scanEditDraft, true
}

func stageScanCandidateEdit(view *tuiModel) bool {
	application, found := activeScanCandidateDraft(view)
	if !found {
		view.setMessage(i18n.T("tui.config_selection_missing"), true)
		return false
	}
	if err := validateTUIApplication(application); err != nil {
		view.setMessage(i18n.ErrorText(err), true)
		return false
	}
	if err := validateScanCandidateIdentity(view, application.ID, application.Identity); err != nil {
		view.setMessage(i18n.ErrorText(err), true)
		return false
	}
	view.scanProposed[application.ID] = cloneScanApplication(application)
	selectScanApplicationID(view, application.ID)
	view.setMessage(i18n.T("tui.scan.candidate_staged"), false)
	return true
}

func discardScanCandidateEdit(view *tuiModel) {
	id := view.scanEditID
	draft := view.scanEditDraft
	staged, found := view.scanProposed[id]
	discarded := found && !reflect.DeepEqual(draft, staged)
	clearScanCandidateEdit(view)
	if discarded {
		view.setMessage(i18n.T("tui.scan.candidate_discarded"), false)
	}
}

func resetScanCandidateEdit(view *tuiModel) {
	if !view.scanEditFocus || view.scanEditSnapshot.ID == "" {
		return
	}
	view.scanEditDraft = cloneScanApplication(view.scanEditSnapshot)
	view.editing = false
	view.editValue = ""
	view.editCursor = 0
	view.setMessage(i18n.T("tui.scan.candidate_reset"), false)
}

func clearScanCandidateEdit(view *tuiModel) {
	view.scanEditFocus = false
	view.scanEditID = ""
	view.scanEditSnapshot = model.Application{}
	view.scanEditDraft = model.Application{}
	view.scanFieldIndex, view.scanFieldScroll = 0, 0
	view.editing = false
	view.editValue = ""
	view.editCursor = 0
}

func cloneScanApplication(application model.Application) model.Application {
	cloned := application
	if application.Provider.Actions != nil {
		actions := *application.Provider.Actions
		cloned.Provider.Actions = &actions
	}
	if application.Environment != nil {
		cloned.Environment = make(map[string]string, len(application.Environment))
		for key, value := range application.Environment {
			cloned.Environment[key] = value
		}
	}
	if application.Provider.DownloadAction() != nil {
		download := *application.Provider.DownloadAction()
		download.ExtraArgs = append([]string(nil), application.Provider.DownloadAction().ExtraArgs...)
		cloned.Provider.Actions.Download = &download
	}
	return cloned
}

func selectScanApplicationID(view *tuiModel, id string) {
	for index, application := range scanDisplayApps(view) {
		if application.ID == id {
			view.scanSelected = index
			return
		}
	}
}

func scanProgressMessage(stage, subject string) string {
	switch stage {
	case model.ScanStagePrepare:
		return i18n.T("tui.scan.progress_prepare")
	case model.ScanStagePath:
		return i18n.T("tui.scan.progress_path")
	case model.ScanStageMacOS:
		return i18n.T("tui.scan.progress_macos")
	case model.ScanStagePackages:
		return i18n.T("tui.scan.progress_packages")
	case model.ScanStagePackageManager:
		return i18n.T("tui.scan.progress_package_manager", subject)
	case model.ScanStagePackageList:
		return i18n.T("tui.scan.progress_package_list", subject)
	case model.ScanStagePackageMetadata:
		return i18n.T("tui.scan.progress_package_metadata", subject)
	case model.ScanStagePackagePaths:
		return i18n.T("tui.scan.progress_package_paths", subject)
	case model.ScanStageApplication:
		return i18n.T("tui.scan.progress_application", subject)
	case model.ScanStageFinalize:
		return i18n.T("tui.scan.progress_finalize")
	default:
		return i18n.T("tui.scan.running")
	}
}

func beginScanConfirmation(view *tuiModel, action string) {
	application, found := selectedScanApplication(view)
	if !found {
		return
	}
	view.scanConfirm = action
	view.scanConfirmID = application.ID
	view.confirmChoice = tuiConfirmationPrimary
}

func beginIdentityConfirmation(view *tuiModel, actions TUIActions) {
	application, found := selectedScanApplication(view)
	if !found {
		return
	}
	if _, exists := findApplicationValue(view.catalog.Apps, application.ID); !exists {
		view.setMessage(i18n.T("tui.scan.new_merge_first"), false)
		return
	}
	if strings.TrimSpace(application.Identity) != "" {
		view.setMessage(i18n.T("tui.scan.identity_exists"), false)
		return
	}
	if actions.GenerateIdentity == nil {
		view.setMessage(i18n.T("tui.backend_missing"), true)
		return
	}
	identity, err := actions.GenerateIdentity(application)
	if err != nil {
		view.setMessage(i18n.ErrorText(err), true)
		return
	}
	for _, existing := range scanDisplayApps(view) {
		if existing.ID == application.ID {
			continue
		}
		existingIdentity := existing.Identity
		if existingIdentity == "" {
			existingIdentity, _ = actions.GenerateIdentity(existing)
		}
		if strings.EqualFold(existingIdentity, identity) {
			view.setMessage(i18n.T("tui.scan.identity_conflict", identity, existing.Name), true)
			return
		}
	}
	view.scanIdentity = identity
	beginScanConfirmation(view, scanConfirmIdentity)
}

func handleScanConfirmationKey(view *tuiModel, key string, actions TUIActions) {
	if moveTUIConfirmationChoice(view, key) {
		return
	}
	accepted := key == "y" || (key == "enter" && view.confirmChoice == tuiConfirmationPrimary)
	rejected := key == "esc" || key == "n" || key == "q"
	if key == "enter" && view.confirmChoice == tuiConfirmationSecondary {
		rejected = true
	}
	if !accepted && !rejected {
		return
	}
	action := view.scanConfirm
	if action == scanConfirmDelete && accepted {
		view.scanConfirm = scanConfirmDeleteExclude
		view.confirmChoice = tuiConfirmationPrimary
		return
	}
	if action == scanConfirmDeleteExclude {
		persistScanDeletion(view, actions, accepted)
		view.scanConfirm = ""
		view.confirmChoice = tuiConfirmationPrimary
		return
	}
	view.scanConfirm = ""
	view.confirmChoice = tuiConfirmationPrimary
	if !accepted {
		return
	}
	switch action {
	case scanConfirmExclude:
		persistScanExclusion(view, actions)
	case scanConfirmIdentity:
		persistScanIdentity(view, actions)
	case scanConfirmMerge:
		persistScanMerge(view, actions, nil)
	case scanConfirmPartial:
		selected := selectedPartialFields(view)
		if len(selected) > 0 && persistScanMerge(view, actions, selected) {
			finishPartialMerge(view)
		}
	case scanConfirmKeep:
		persistScanKeep(view, actions)
	case scanConfirmAdd:
		persistScanMerge(view, actions, nil)
	case scanConfirmAddAll:
		persistScanAddAll(view, actions)
	case scanConfirmRescan:
		view.scanRescan = true
	}
}

func beginPartialMerge(view *tuiModel) {
	application, found := selectedScanApplication(view)
	change, changed := view.scanChanges[application.ID]
	if !found || !changed || len(change.Fields) == 0 {
		view.setMessage(i18n.T("tui.scan.partial_unavailable"), false)
		return
	}
	view.scanPartial = true
	view.scanConfirmID = application.ID
	view.partialIndex = 0
	view.partialOffset = 0
	view.partialFields = map[string]bool{}
}

func handleScanPartialKey(view *tuiModel, key string, actions TUIActions) {
	change, found := view.scanChanges[view.scanConfirmID]
	if !found {
		finishPartialMerge(view)
		return
	}
	switch key {
	case "esc", "q":
		finishPartialMerge(view)
	case "up":
		view.partialIndex = max(0, view.partialIndex-1)
	case "down":
		view.partialIndex = min(len(change.Fields)-1, view.partialIndex+1)
	case "home":
		view.partialIndex = 0
	case "end":
		view.partialIndex = len(change.Fields) - 1
	case " ":
		field := change.Fields[view.partialIndex].Field
		view.partialFields[field] = !view.partialFields[field]
	case "a":
		for _, field := range change.Fields {
			view.partialFields[field.Field] = true
		}
	case "enter":
		selected := selectedPartialFields(view)
		if len(selected) == 0 {
			view.setMessage(i18n.T("tui.scan.partial_empty"), false)
			return
		}
		view.scanConfirm = scanConfirmPartial
		view.confirmChoice = tuiConfirmationPrimary
	}
}

func selectedPartialFields(view *tuiModel) map[string]bool {
	selected := make(map[string]bool)
	for field, enabled := range view.partialFields {
		if enabled {
			selected[field] = true
		}
	}
	return selected
}

func finishPartialMerge(view *tuiModel) {
	view.scanPartial = false
	view.scanConfirmID = ""
	view.partialIndex = 0
	view.partialOffset = 0
	view.partialFields = nil
}

func persistScanDeletion(view *tuiModel, actions TUIActions, exclude bool) {
	application, found := scanApplicationByID(view, view.scanConfirmID)
	if !found {
		return
	}
	catalog := cloneConfig(view.catalog)
	catalog.Apps = removeTUIApplication(catalog.Apps, application.ID)
	if exclude {
		catalog.Settings.Scan.Exclude = appendUniqueFold(catalog.Settings.Scan.Exclude, strings.TrimSpace(application.ID))
	}
	delete(catalog.ScanVersionControl, application.ID)
	if saveTUIScan(view, actions, catalog) {
		ignoreScanCandidate(view, application.ID)
		view.setMessage(i18n.T("tui.scan.deleted", application.Name), false)
	}
}

func persistScanExclusion(view *tuiModel, actions TUIActions) {
	ensureScanMaps(view)
	if view.scanExcluded[view.scanConfirmID] {
		return
	}
	application, found := scanApplicationByID(view, view.scanConfirmID)
	if !found {
		return
	}
	catalog := cloneConfig(view.catalog)
	keyword := strings.TrimSpace(application.ID)
	wasAdded := view.scanAdded[application.ID]
	catalog.Settings.Scan.Exclude = appendUniqueFold(catalog.Settings.Scan.Exclude, keyword)
	delete(catalog.ScanVersionControl, application.ID)
	if saveTUIScan(view, actions, catalog) {
		if wasAdded {
			resetScanPreview(view, application.ID)
			view.scanSelected = max(0, min(view.scanSelected, len(scanDisplayApps(view))-1))
		} else {
			view.scanExcluded[application.ID] = true
			ignoreScanCandidate(view, application.ID)
		}
		view.setMessage(i18n.T("tui.scan.excluded", keyword), false)
	}
}

func persistScanUnexclusion(view *tuiModel, actions TUIActions, application model.Application) {
	catalog := cloneConfig(view.catalog)
	catalog.Settings.Scan.Exclude = removeFoldValue(catalog.Settings.Scan.Exclude, application.ID)
	if saveTUIScan(view, actions, catalog) {
		delete(view.scanExcluded, application.ID)
		delete(view.scanIgnored, application.ID)
		view.setMessage(i18n.T("tui.scan.unexcluded", application.ID), false)
	}
}

func persistScanIdentity(view *tuiModel, actions TUIActions) {
	catalog := cloneConfig(view.catalog)
	application, found := findApplication(&catalog, view.scanConfirmID)
	if !found {
		view.setMessage(i18n.T("tui.scan.new_merge_first"), false)
		return
	}
	application.Identity = view.scanIdentity
	clearScanKeeps(&catalog, view.scanConfirmID, map[string]bool{model.ApplicationFieldIdentity: true})
	if saveTUIScan(view, actions, catalog) {
		resolveScanFields(view, view.scanConfirmID, map[string]bool{model.ApplicationFieldIdentity: true})
		view.setMessage(i18n.T("tui.scan.identity_generated", view.scanIdentity), false)
	}
}

func persistScanManaged(view *tuiModel, actions TUIActions) {
	catalog := cloneConfig(view.catalog)
	application, found := findApplication(&catalog, view.scanConfirmID)
	if !found {
		view.setMessage(i18n.T("tui.scan.new_merge_first"), false)
		return
	}
	application.ScanManaged = !application.ScanManaged
	model.ClearScanVersionControlForUnmanagedTransitions(&view.catalog, &catalog)
	if saveTUIScan(view, actions, catalog) {
		resolveScanFields(view, view.scanConfirmID, map[string]bool{model.ApplicationFieldScanManaged: true})
		view.setMessage(i18n.T("tui.scan.managed_changed", application.Name, boolText(application.ScanManaged)), false)
	}
}

func persistScanMerge(view *tuiModel, actions TUIActions, selectedFields map[string]bool) bool {
	id := view.scanConfirmID
	catalog := cloneConfig(view.catalog)
	if view.scanRemoved[id] {
		catalog.Apps = removeTUIApplication(catalog.Apps, id)
		delete(catalog.ScanVersionControl, id)
	} else {
		candidate, found := view.scanProposed[id]
		if !found {
			return false
		}
		if view.scanAdded[id] {
			candidate = normalizeScanCandidate(candidate)
			if err := validateTUIApplication(candidate); err != nil {
				view.setMessage(i18n.ErrorText(err), true)
				return false
			}
			view.scanProposed[id] = candidate
		}
		if selectedFields != nil {
			current, exists := findApplication(&catalog, id)
			if !exists {
				return false
			}
			resolved := *current
			for field := range selectedFields {
				applyTUIScanField(&resolved, candidate, field)
			}
			candidate = resolved
		}
		if selectedFields == nil {
			delete(catalog.ScanVersionControl, id)
		} else {
			clearScanKeeps(&catalog, id, selectedFields)
		}
		if current, exists := findApplication(&catalog, id); exists {
			*current = candidate
		} else {
			catalog.Apps = append(catalog.Apps, candidate)
		}
	}
	if !saveTUIScan(view, actions, catalog) {
		return false
	}
	if selectedFields == nil {
		ignoreScanCandidate(view, id)
	} else {
		resolveScanFields(view, id, selectedFields)
	}
	view.setMessage(i18n.T("tui.scan.merged", id), false)
	return true
}

func persistScanAddAll(view *tuiModel, actions TUIActions) bool {
	ids := pendingScanAddedIDs(view)
	if len(ids) == 0 {
		return false
	}
	catalog := cloneConfig(view.catalog)
	normalized := make(map[string]model.Application, len(ids))
	for _, id := range ids {
		candidate, found := view.scanProposed[id]
		if !found {
			return false
		}
		candidate = normalizeScanCandidate(candidate)
		if err := validateTUIApplication(candidate); err != nil {
			view.setMessage(i18n.ErrorText(err), true)
			return false
		}
		normalized[id] = candidate
		if current, exists := findApplication(&catalog, id); exists {
			*current = candidate
		} else {
			catalog.Apps = append(catalog.Apps, candidate)
		}
	}
	if !saveTUIScan(view, actions, catalog) {
		return false
	}
	for _, id := range ids {
		view.scanProposed[id] = normalized[id]
		ignoreScanCandidate(view, id)
	}
	view.setMessage(i18n.T("tui.scan.added_all", len(ids)), false)
	return true
}

func pendingScanAddedIDs(view *tuiModel) []string {
	ids := make([]string, 0, len(view.scanAdded))
	for id, added := range view.scanAdded {
		if added && !view.scanIgnored[id] {
			if _, found := view.scanProposed[id]; found {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func normalizeScanCandidate(application model.Application) model.Application {
	application.Name = strings.TrimSpace(application.Name)
	application.Type = strings.TrimSpace(application.Type)
	application.Description = strings.TrimSpace(application.Description)
	application.URL = strings.TrimSpace(application.URL)
	application.InstallPath = strings.TrimSpace(application.InstallPath)
	application.Provider.Type = model.ProviderType(strings.TrimSpace(string(application.Provider.Type)))
	application.Package = strings.TrimSpace(application.Package)
	if application.Provider.Actions != nil {
		application.Provider.Actions.Version = strings.TrimSpace(application.Provider.Actions.Version)
		application.Provider.Actions.Check = strings.TrimSpace(application.Provider.Actions.Check)
		application.Provider.Actions.Update = strings.TrimSpace(application.Provider.Actions.Update)
		application.Provider.Actions.Install = strings.TrimSpace(application.Provider.Actions.Install)
	}
	if application.Provider.DownloadAction() != nil {
		download := *application.Provider.DownloadAction()
		download.ExtraArgs = append([]string(nil), download.ExtraArgs...)
		download.URL = strings.TrimSpace(download.URL)
		download.Filename = strings.TrimSpace(download.Filename)
		download.StorePath = strings.TrimSpace(download.StorePath)
		download.ChecksumURL = strings.TrimSpace(download.ChecksumURL)
		download.ChecksumValue = strings.TrimSpace(download.ChecksumValue)
		for index := range download.ExtraArgs {
			download.ExtraArgs[index] = strings.TrimSpace(download.ExtraArgs[index])
		}
		application.Provider.Actions.Download = &download
	}
	application.Identity = strings.TrimSpace(application.Identity)
	return application
}

func saveTUIScan(view *tuiModel, actions TUIActions, catalog model.Config) bool {
	if actions.SaveScan == nil {
		view.setMessage(i18n.T("tui.backend_missing"), true)
		return false
	}
	savedCatalog, err := actions.SaveScan(view.catalog, catalog)
	if err != nil {
		view.offerReload(err)
		view.setMessage(i18n.ErrorText(err), true)
		return false
	}
	view.catalog, view.working = savedCatalog, cloneConfig(savedCatalog)
	view.dirty = false
	view.scanSelected = max(0, min(view.scanSelected, len(scanDisplayApps(view))-1))
	return true
}

func ignoreScanCandidate(view *tuiModel, id string) {
	ensureScanMaps(view)
	view.scanIgnored[id] = true
	delete(view.scanChanges, id)
	delete(view.scanAdded, id)
	delete(view.scanRemoved, id)
}

func persistScanKeep(view *tuiModel, actions TUIActions) {
	id := view.scanConfirmID
	change, found := view.scanChanges[id]
	if !found || len(change.Fields) == 0 {
		return
	}
	catalog := cloneConfig(view.catalog)
	if catalog.ScanVersionControl == nil {
		catalog.ScanVersionControl = map[string]map[string]model.ScanKeepResolution{}
	}
	fields := make(map[string]model.ScanKeepResolution, len(catalog.ScanVersionControl[id])+len(change.Fields))
	for field, resolution := range catalog.ScanVersionControl[id] {
		fields[field] = resolution
	}
	for _, field := range change.Fields {
		fingerprint := model.ScanKeepFingerprint(id, change.Current, change.Proposed, change.Fields, field)
		if fingerprint == "" {
			view.setMessage(i18n.T("tui.backend_missing"), true)
			return
		}
		fields[field.Field] = model.ScanKeepResolution{Fingerprint: fingerprint, RecordedAt: model.Now()}
	}
	catalog.ScanVersionControl[id] = fields
	if saveTUIScan(view, actions, catalog) {
		ignoreScanCandidate(view, id)
	}
}

func clearScanKeeps(config *model.Config, id string, fields map[string]bool) {
	keeps := config.ScanVersionControl[id]
	for field := range fields {
		delete(keeps, field)
	}
	if len(keeps) == 0 {
		delete(config.ScanVersionControl, id)
	}
}

func resolveScanFields(view *tuiModel, id string, resolved map[string]bool) {
	saved, found := findApplicationValue(view.catalog.Apps, id)
	if !found {
		return
	}
	if candidate, exists := view.scanProposed[id]; exists {
		for field := range resolved {
			applyTUIScanField(&candidate, saved, field)
		}
		view.scanProposed[id] = candidate
	}
	change, exists := view.scanChanges[id]
	if !exists {
		return
	}
	change.Current = saved
	if candidate, found := view.scanProposed[id]; found {
		change.Proposed = candidate
	}
	remaining := change.Fields[:0]
	for _, field := range change.Fields {
		if !resolved[field.Field] {
			remaining = append(remaining, field)
		}
	}
	if len(remaining) == 0 {
		delete(view.scanChanges, id)
		return
	}
	change.Fields = remaining
	view.scanChanges[id] = change
}

func scanCandidatePending(view *tuiModel, id string) bool {
	return !view.scanIgnored[id] && (view.scanAdded[id] || view.scanRemoved[id] || hasScanChange(view, id))
}

type scanStatistics struct {
	total    int
	added    int
	changed  int
	excluded int
	invalid  int
}

func currentScanStatistics(view *tuiModel) scanStatistics {
	return scanStatisticsForApplications(view, scanDisplayApps(view))
}

func currentScanApplicationStatistics(view *tuiModel, id string) scanStatistics {
	application, found := scanApplicationByID(view, id)
	if !found {
		return scanStatistics{}
	}
	return scanStatisticsForApplications(view, []model.Application{application})
}

func scanStatisticsForApplications(view *tuiModel, applications []model.Application) scanStatistics {
	statistics := scanStatistics{total: len(applications)}
	for _, application := range applications {
		id := application.ID
		switch {
		case view.scanAdded[id]:
			statistics.added++
		case view.scanExcluded[id]:
			statistics.excluded++
		case scanApplicationInvalid(view, id):
			statistics.invalid++
		}
		if hasScanChange(view, id) {
			statistics.changed++
		}
	}
	return statistics
}

func mergeSingleScanState(current, scanned model.RuntimeState, id string) model.RuntimeState {
	merged := cloneTUIState(current)
	if merged.Observations == nil {
		merged.Observations = map[string]model.ScanObservation{}
	}
	if observation, found := scanned.Observations[id]; found {
		merged.Observations[id] = observation
	} else {
		delete(merged.Observations, id)
	}
	return merged
}

func scanApplicationInvalid(view *tuiModel, id string) bool {
	if view.scanRemoved[id] {
		return true
	}
	_, observed := view.scanProposed[id]
	observation := view.scanObservations[id]
	return view.scanCompleted && observed && !observation.Found
}

func hasScanChange(view *tuiModel, id string) bool {
	if view.scanIgnored[id] {
		return false
	}
	_, found := view.scanChanges[id]
	return found
}

func (view *tuiModel) appendScanStructuredLog(level LogLevel, subject, message string) {
	oldMaximum := 0
	preserveViewport := view.scanLogFocus && view.scanLogOffset > 0
	if preserveViewport {
		oldMaximum = tuiMaxScanLogOffset(view)
	}
	lines := FormatLogLines(time.Now(), level, "scan", subject, message)
	if view.operationLog != nil {
		if persisted, err := view.operationLog(view.catalog, string(level), "scan", subject, message); err == nil {
			lines = persisted
		} else {
			view.setMessage(i18n.ErrorText(err), true)
			return
		}
	}
	for _, line := range lines {
		view.scanLogs = append(view.scanLogs, truncateTUILogLine(line))
	}
	if len(view.scanLogs) > 2000 {
		view.scanLogs = append([]string(nil), view.scanLogs[len(view.scanLogs)-2000:]...)
	}
	if preserveViewport {
		newMaximum := tuiMaxScanLogOffset(view)
		view.scanLogOffset = max(0, min(newMaximum, view.scanLogOffset+newMaximum-oldMaximum))
	}
}

func scrollTUIScanLogs(view *tuiModel, delta int) {
	view.scanLogOffset = max(0, min(tuiMaxScanLogOffset(view), view.scanLogOffset+delta))
}

func selectedScanApplication(view *tuiModel) (model.Application, bool) {
	apps := scanDisplayApps(view)
	if len(apps) == 0 {
		return model.Application{}, false
	}
	view.scanSelected = max(0, min(view.scanSelected, len(apps)-1))
	return apps[view.scanSelected], true
}

func scanDisplayApps(view *tuiModel) []model.Application {
	apps := append([]model.Application(nil), view.catalog.Apps...)
	known := make(map[string]bool, len(apps))
	for _, application := range apps {
		known[application.ID] = true
	}
	type addedApplication struct {
		application model.Application
		key         string
	}
	added := make([]addedApplication, 0)
	for id, application := range view.scanProposed {
		if !known[id] && !view.scanIgnored[id] {
			added = append(added, addedApplication{application: application, key: id})
		}
	}
	sort.Slice(added, func(left, right int) bool {
		leftApp, rightApp := added[left].application, added[right].application
		if leftName, rightName := strings.ToLower(leftApp.Name), strings.ToLower(rightApp.Name); leftName != rightName {
			return leftName < rightName
		}
		if leftApp.ID != rightApp.ID {
			return leftApp.ID < rightApp.ID
		}
		if leftApp.InstallPath != rightApp.InstallPath {
			return leftApp.InstallPath < rightApp.InstallPath
		}
		return added[left].key < added[right].key
	})
	for _, application := range added {
		apps = append(apps, application.application)
	}
	return apps
}

func scanApplicationByID(view *tuiModel, id string) (model.Application, bool) {
	if application, found := findApplicationValue(view.catalog.Apps, id); found {
		return application, true
	}
	application, found := view.scanProposed[id]
	return application, found
}

func ensureScanMaps(view *tuiModel) {
	if view.scanProposed == nil {
		view.scanProposed = map[string]model.Application{}
	}
	if view.scanObservations == nil {
		view.scanObservations = map[string]model.ScanObservation{}
	}
	if view.scanChanges == nil {
		view.scanChanges = map[string]model.ScanApplicationChange{}
	}
	if view.scanAdded == nil {
		view.scanAdded = map[string]bool{}
	}
	if view.scanRemoved == nil {
		view.scanRemoved = map[string]bool{}
	}
	if view.scanExcluded == nil {
		view.scanExcluded = map[string]bool{}
	}
	if view.scanIgnored == nil {
		view.scanIgnored = map[string]bool{}
	}
}

func applicationMap(applications []model.Application) map[string]model.Application {
	result := make(map[string]model.Application, len(applications))
	for _, application := range applications {
		result[application.ID] = application
	}
	return result
}

func applicationFlagMap(applications []model.Application) map[string]bool {
	result := make(map[string]bool, len(applications))
	for _, application := range applications {
		result[application.ID] = true
	}
	return result
}

func changeMap(changes []model.ScanApplicationChange) map[string]model.ScanApplicationChange {
	result := make(map[string]model.ScanApplicationChange, len(changes))
	for _, change := range changes {
		result[change.Current.ID] = change
	}
	return result
}

func cloneScanObservations(observations map[string]model.ScanObservation) map[string]model.ScanObservation {
	if observations == nil {
		return nil
	}
	result := make(map[string]model.ScanObservation, len(observations))
	for id, observation := range observations {
		result[id] = observation
	}
	return result
}

func cloneTUIState(state model.RuntimeState) model.RuntimeState {
	state.Observations = cloneScanObservations(state.Observations)
	return state
}

func findApplicationValue(applications []model.Application, id string) (model.Application, bool) {
	for _, application := range applications {
		if application.ID == id {
			return application, true
		}
	}
	return model.Application{}, false
}

func findScanChange(changes []model.ScanApplicationChange, id string) (model.ScanApplicationChange, bool) {
	for _, change := range changes {
		if change.Current.ID == id {
			return change, true
		}
	}
	return model.ScanApplicationChange{}, false
}

func containsApplication(applications []model.Application, id string) bool {
	_, found := findApplicationValue(applications, id)
	return found
}

func removeTUIApplication(applications []model.Application, id string) []model.Application {
	kept := make([]model.Application, 0, len(applications))
	for _, application := range applications {
		if application.ID != id {
			kept = append(kept, application)
		}
	}
	return kept
}

func appendUniqueFold(values []string, value string) []string {
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return values
		}
	}
	return append(values, value)
}

func containsFoldValue(values []string, value string) bool {
	value = strings.TrimSpace(value)
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), value) {
			return true
		}
	}
	return false
}

func removeFoldValue(values []string, value string) []string {
	value = strings.TrimSpace(value)
	kept := make([]string, 0, len(values))
	for _, existing := range values {
		if !strings.EqualFold(strings.TrimSpace(existing), value) {
			kept = append(kept, existing)
		}
	}
	return kept
}

func applyTUIScanField(target *model.Application, source model.Application, field string) {
	switch field {
	case model.ApplicationFieldName:
		target.Name = source.Name
	case model.ApplicationFieldType:
		target.Type = source.Type
	case model.ApplicationFieldDescription:
		target.Description = source.Description
	case model.ApplicationFieldURL:
		target.URL = source.URL
	case model.ApplicationFieldInstallPath:
		target.InstallPath = source.InstallPath
	case model.ApplicationFieldEnabled:
		target.Enabled = source.Enabled
	case model.ApplicationFieldUpdateMode:
		target.UpdateMode = source.UpdateMode
	case model.ApplicationFieldProviderType:
		target.Provider.Type = source.Provider.Type
	case model.ApplicationFieldPackage:
		target.Package = source.Package
	case model.ApplicationFieldActionVersion:
		ensureTUIProviderActions(target)
		target.Provider.Actions.Version = source.Provider.VersionAction()
	case model.ApplicationFieldActionCheck:
		ensureTUIProviderActions(target)
		target.Provider.Actions.Check = source.Provider.CheckAction()
	case model.ApplicationFieldActionUpdate:
		ensureTUIProviderActions(target)
		target.Provider.Actions.Update = source.Provider.UpdateAction()
	case model.ApplicationFieldActionDownload:
		ensureTUIProviderActions(target)
		target.Provider.Actions.Download = cloneTUIDownload(source.Provider.DownloadAction())
	case model.ApplicationFieldActionInstall:
		ensureTUIProviderActions(target)
		target.Provider.Actions.Install = source.Provider.InstallAction()
	case model.ApplicationFieldIdentity:
		target.Identity = source.Identity
	case model.ApplicationFieldScanManaged:
		target.ScanManaged = source.ScanManaged
	}
	if !target.Provider.HasActions() {
		target.Provider.Actions = nil
	}
}

func ensureTUIProviderActions(target *model.Application) {
	if target.Provider.Actions == nil {
		target.Provider.Actions = &model.ProviderActions{}
	}
}

func cloneTUIDownload(source *model.Download) *model.Download {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.ExtraArgs = append([]string(nil), source.ExtraArgs...)
	return &cloned
}

func renderScanPage(screen *tuiScreen, view *tuiModel, top, upperHeight, activityTop, activityHeight int) {
	leftWidth := screen.width * 67 / 100
	rightWidth := screen.width - leftWidth
	screen.box(0, top, leftWidth, upperHeight, tuiScanApplicationListTitle(view), tuiCyan)
	renderScanApplicationTable(screen, view, 1, top+1, leftWidth-2, upperHeight-2)
	screen.box(leftWidth, top, rightWidth, upperHeight, tuiScanApplicationDetailsTitle(view, upperHeight), tuiCyan)
	renderScanApplicationDetails(screen, view, leftWidth+1, top+1, rightWidth-2, upperHeight-2)

	if !view.scanLogFocus && scanShowsComparison(view) {
		title := i18n.T("tui.scan.diff")
		if view.scanEditFocus {
			title += " · [" + i18n.T("tui.scan.edit_focus") + "]"
		}
		screen.box(0, activityTop, screen.width, activityHeight, title, tuiOrange)
		renderScanComparison(screen, view, 1, activityTop+1, screen.width-2, activityHeight-2)
	} else {
		title := i18n.T("tui.scan.output")
		if view.scanLogFocus {
			position := i18n.T("tui.log_following")
			if view.scanLogOffset > 0 {
				position = i18n.T("tui.log_offset", view.scanLogOffset)
			}
			title += "  [" + i18n.T("tui.focused") + " · " + position + "]"
		}
		screen.box(0, activityTop, screen.width, activityHeight, title, tuiCyan)
		renderScanOutput(screen, view, 1, activityTop+1, screen.width-2, activityHeight-2)
	}
}

func tuiScanApplicationListTitle(view *tuiModel) string {
	title := i18n.T("tui.scan.app_list")
	if view.searchActive {
		if view.searchQuery == "" {
			title += " [" + i18n.T("tui.app_list_search") + "]"
		} else {
			title = i18n.T("tui.app_list_search_query", title, i18n.T("tui.app_list_search"), view.searchQuery)
		}
	}
	return title
}

func tuiScanApplicationDetailsTitle(view *tuiModel, upperHeight int) string {
	if !view.detailFocus {
		return i18n.T("tui.scan.application_details")
	}
	maximum := tuiMaxScanDetailOffset(view, upperHeight)
	view.scanDetail = max(0, min(view.scanDetail, maximum))
	return i18n.T("tui.details_focused", view.scanDetail+1, maximum+1)
}

func renderScanApplicationTable(screen *tuiScreen, view *tuiModel, x, y, width, height int) {
	apps := scanDisplayApps(view)
	if len(apps) == 0 {
		screen.put(x+2, y+1, i18n.T("tui.scan.empty"), tuiDim)
		return
	}
	view.scanSelected = max(0, min(view.scanSelected, len(apps)-1))
	widths := scanTableColumnWidths(width)
	columnX := make([]int, len(widths))
	columnX[0] = x + 1
	for index := 1; index < len(widths); index++ {
		columnX[index] = columnX[index-1] + widths[index-1]
	}
	headings := []string{i18n.T("label.number"), i18n.T("label.name"), i18n.T("label.current_version"), i18n.T("tui.scan.managed"), i18n.T("tui.scan.added_at")}
	for index, heading := range headings {
		screen.put(columnX[index], y+1, truncateTUI(heading, widths[index]-1), tuiBold)
	}
	screen.put(x+1, y+2, strings.Repeat("─", max(1, width-2)), tuiDim)
	visible := max(1, height-4)
	if view.scanSelected < view.scanScroll {
		view.scanScroll = view.scanSelected
	}
	if view.scanSelected >= view.scanScroll+visible {
		view.scanScroll = view.scanSelected - visible + 1
	}
	end := min(len(apps), view.scanScroll+visible)
	for index := view.scanScroll; index < end; index++ {
		row := y + 3 + index - view.scanScroll
		application := apps[index]
		state := scanApplicationState(view, application.ID)
		style := tuiNormal
		if index == view.scanSelected {
			style = tuiSelect
			screen.fill(x, row, width, tuiSelect)
		} else if view.scanExcluded[application.ID] || scanApplicationInvalid(view, application.ID) {
			style = tuiDim
		} else if hasScanChange(view, application.ID) {
			style = tuiOrange
		} else if view.scanAdded[application.ID] {
			style = tuiGreen
		}
		values := []string{
			strconv.Itoa(index + 1), application.Name, displayValue(state.CurrentVersion),
			boolText(application.ScanManaged), formatScanAddedAt(state.FirstDetectedTime),
		}
		for column, value := range values {
			cellStyle := style
			if column == 1 && tuiApplicationMatchesQuickSearch(application.Name, view.searchQuery) {
				cellStyle = tuiBlue
				if style == tuiSelect {
					cellStyle = tuiSelectMatch
				}
			}
			screen.put(columnX[column], row, truncateTUI(value, widths[column]-1), cellStyle)
		}
	}
}

func tuiMaxScanDetailOffset(view *tuiModel, upperHeight int) int {
	application, found := selectedScanApplication(view)
	if !found {
		return 0
	}
	rightWidth := view.width - view.width*67/100
	lines, _ := scanApplicationDetailLines(application, scanApplicationState(view, application.ID), max(1, rightWidth-2))
	return max(0, len(lines)-max(1, upperHeight-4))
}

func scanTableColumnWidths(width int) []int {
	available := max(1, width-2)
	widths := []int{5, 24, 18, 12, 20}
	minimums := []int{4, 10, 10, 8, 12}
	total := sumInts(widths)
	for total > available {
		changed := false
		for _, index := range []int{1, 4, 2, 3, 0} {
			if total <= available {
				break
			}
			if widths[index] > minimums[index] {
				widths[index]--
				total--
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	if total < available {
		widths[1] += available - total
	}
	return widths
}

func renderScanApplicationDetails(screen *tuiScreen, view *tuiModel, x, y, width, height int) {
	application, found := selectedScanApplication(view)
	if !found {
		lines := wrapTUI(i18n.T("tui.scan.empty"), max(1, width-2))
		for index, line := range lines[:min(len(lines), max(0, height-2))] {
			screen.put(x+1, y+1+index, line, tuiDim)
		}
		return
	}
	lines, labelWidth := scanApplicationDetailLines(application, scanApplicationState(view, application.ID), width)
	available := max(1, height-2)
	maximum := max(0, len(lines)-available)
	view.scanDetail = max(0, min(view.scanDetail, maximum))
	end := min(len(lines), view.scanDetail+available)
	valueX := x + labelWidth + 2
	for index, line := range lines[view.scanDetail:end] {
		row := y + 1 + index
		if line.fullWidth {
			screen.put(x+1, row, line.value, line.valueStyle)
			continue
		}
		if line.label != "" {
			screen.put(x+1, row, line.label, tuiDim)
		}
		if line.value != "" {
			screen.put(valueX, row, line.value, line.valueStyle)
		}
	}
}

func scanShowsComparison(view *tuiModel) bool {
	if view.scanRunning {
		return false
	}
	application, found := selectedScanApplication(view)
	return found && scanCandidatePending(view, application.ID)
}

func renderScanComparison(screen *tuiScreen, view *tuiModel, x, y, width, height int) {
	application, found := selectedScanApplication(view)
	if !found {
		return
	}
	lines, fieldRows := scanComparisonLines(view, application, width)
	available := max(1, height)
	maximum := max(0, len(lines)-available)
	offset := 0
	if view.scanPartial && len(fieldRows) > 0 {
		view.partialIndex = max(0, min(view.partialIndex, len(fieldRows)-1))
		selectedRow := fieldRows[view.partialIndex]
		if selectedRow < view.partialOffset || selectedRow >= view.partialOffset+available {
			view.partialOffset = max(0, selectedRow-1)
		}
		view.partialOffset = max(0, min(view.partialOffset, maximum))
		offset = view.partialOffset
	} else if view.scanEditFocus && view.scanAdded[application.ID] && len(fieldRows) > 0 {
		view.scanFieldIndex = max(0, min(view.scanFieldIndex, len(fieldRows)-1))
		selectedRow := fieldRows[view.scanFieldIndex]
		if selectedRow < view.scanFieldScroll || selectedRow >= view.scanFieldScroll+available {
			view.scanFieldScroll = selectedRow
		}
		view.scanFieldScroll = max(0, min(view.scanFieldScroll, maximum))
		offset = view.scanFieldScroll
	} else {
		view.partialOffset = 0
		view.scanFieldScroll = 0
	}
	end := min(len(lines), offset+available)
	for index, line := range lines[offset:end] {
		style := line.valueStyle
		labelStyle := tuiDim
		absoluteRow := offset + index
		if view.scanEditFocus && view.scanAdded[application.ID] && view.scanFieldIndex < len(fieldRows) && absoluteRow == fieldRows[view.scanFieldIndex] {
			screen.fill(x, y+index, width, tuiSelect)
			style = tuiSelect
			labelStyle = tuiSelect
		}
		if line.label != "" {
			screen.put(x+1, y+index, line.label, labelStyle)
			screen.put(x+1+DisplayWidth(line.label), y+index, truncateTUI(line.value, max(1, width-2-DisplayWidth(line.label))), style)
			continue
		}
		screen.put(x+1, y+index, truncateTUI(line.value, max(1, width-2)), style)
	}
}

func scanComparisonLines(view *tuiModel, application model.Application, width int) ([]tuiDetailLine, []int) {
	lines := make([]tuiDetailLine, 0)
	fieldRows := make([]int, 0)
	if change, changed := view.scanChanges[application.ID]; changed {
		separator := strings.Repeat("─", max(1, width-2))
		for fieldIndex, field := range change.Fields {
			if fieldIndex > 0 {
				lines = append(lines, tuiDetailLine{value: separator, valueStyle: tuiDim, fullWidth: true})
			}
			fieldRows = append(fieldRows, len(lines))
			checkbox := "[ ]"
			if view.partialFields[field.Field] {
				checkbox = "[x]"
			}
			style := tuiNormal
			if view.scanPartial && fieldIndex == view.partialIndex {
				style = tuiBold
			}
			lines = append(lines, tuiDetailLine{value: checkbox + " " + field.Field, valueStyle: style, fullWidth: true})
			for _, wrapped := range wrapTUI("- "+displayScanDiffValue(field.Current), max(1, width-2)) {
				lines = append(lines, tuiDetailLine{value: wrapped, valueStyle: tuiRed, fullWidth: true})
			}
			for _, wrapped := range wrapTUI("+ "+displayScanDiffValue(field.Proposed), max(1, width-2)) {
				lines = append(lines, tuiDetailLine{value: wrapped, valueStyle: tuiGreen, fullWidth: true})
			}
		}
	} else if view.scanAdded[application.ID] {
		rows := scanCandidateConfigRowsFor(view, application.ID)
		labelWidth := 0
		for _, row := range rows {
			labelWidth = max(labelWidth, DisplayWidth(row.label))
		}
		labelWidth = min(labelWidth, 24)
		valueWidth := max(1, width-labelWidth-5)
		for index, row := range rows {
			fieldRows = append(fieldRows, len(lines))
			style := tuiNormal
			if row.rowType == configRowReadOnly {
				style = tuiDim
			}
			value := row.value
			if view.scanEditFocus && view.editing && index == view.scanFieldIndex {
				value = editValueViewport(view.editValue, view.editCursor, valueWidth)
			}
			prefix := padTUI(row.label, labelWidth) + "  "
			wrapped := wrapTUI(displayValue(value), valueWidth)
			for lineIndex, line := range wrapped {
				text := strings.Repeat(" ", labelWidth+2) + line
				if lineIndex == 0 {
					lines = append(lines, tuiDetailLine{label: prefix, value: line, valueStyle: style, fullWidth: true})
					continue
				}
				lines = append(lines, tuiDetailLine{value: text, valueStyle: style, fullWidth: true})
			}
		}
	} else if view.scanRemoved[application.ID] {
		lines = append(lines, tuiDetailLine{value: i18n.T("tui.scan.removed_candidate"), valueStyle: tuiOrange, fullWidth: true})
	}
	return lines, fieldRows
}

func renderScanOutput(screen *tuiScreen, view *tuiModel, x, y, width, height int) {
	lines := wrappedTUIScanLogs(view)
	empty := len(lines) == 0
	if len(lines) == 0 {
		lines = []string{i18n.T("tui.scan.output_empty")}
		view.scanLogOffset = 0
	}
	available := max(1, height)
	view.scanLogOffset = min(view.scanLogOffset, max(0, len(lines)-available))
	end := max(0, len(lines)-view.scanLogOffset)
	start := max(0, end-available)
	for index, line := range lines[start:end] {
		row := y + index
		if empty {
			row++
		}
		style := tuiNormal
		switch LogLevelFromLine(line) {
		case LogError:
			style = tuiRed
		case LogWarn:
			style = tuiYellow
		case LogDebug:
			style = tuiDim
		}
		screen.put(x+1, row, truncateTUI(line, max(1, width-2)), style)
	}
}

func wrappedTUIScanLogs(view *tuiModel) []string {
	width := max(1, view.width-4)
	lines := make([]string, 0, len(view.scanLogs))
	for _, logLine := range view.scanLogs {
		lines = append(lines, wrapTUI(logLine, width)...)
	}
	return lines
}

func tuiScanLogViewportHeight(view *tuiModel) int {
	_, _, lowerHeight := stackedPageLayout(view.height, 3, 3)
	return max(1, lowerHeight-2)
}

func tuiMaxScanLogOffset(view *tuiModel) int {
	return max(0, len(wrappedTUIScanLogs(view))-tuiScanLogViewportHeight(view))
}

func scanApplicationDetailLines(application model.Application, state model.ManagedStatus, width int) ([]tuiDetailLine, int) {
	fields := baseApplicationDetailFields(application, state)
	fields = append(fields,
		tuiDetailField{separator: true},
		tuiDetailField{label: i18n.T("tui.config.app_identity"), value: displayValue(application.Identity), style: tuiNormal},
		tuiDetailField{label: i18n.T("tui.scan.managed"), value: boolText(application.ScanManaged), style: tuiNormal},
		tuiDetailField{label: i18n.T("tui.scan.added_at"), value: displayValue(state.FirstDetectedTime), style: tuiNormal},
		tuiDetailField{label: i18n.T("tui.scan.last_update"), value: displayValue(state.LastUpdateTime), style: tuiNormal},
	)
	if strings.TrimSpace(state.DownloadPath) != "" {
		fields = append(fields, tuiDetailField{label: i18n.T("tui.scan.download_path"), value: state.DownloadPath, style: tuiNormal})
	}
	return buildApplicationDetailLines(fields, state, width)
}

func renderScanConfirmation(screen *tuiScreen, view *tuiModel) {
	application, _ := scanApplicationByID(view, view.scanConfirmID)
	title := i18n.T("tui.scan.confirm_title")
	if view.scanConfirm == scanConfirmRescan {
		title = i18n.T("tui.scan.confirm_rescan_title")
	}
	prompt := scanConfirmationPrompt(view, application)
	primary, secondary := tuiConfirmationLabels(view)
	renderConfirmationDialog(screen, view, title, prompt, tuiOrange, i18n.T(primary), i18n.T(secondary))
}

func scanConfirmationPrompt(view *tuiModel, application model.Application) string {
	switch view.scanConfirm {
	case scanConfirmDelete:
		return i18n.T("tui.scan.confirm_delete", application.Name)
	case scanConfirmDeleteExclude:
		return i18n.T("tui.scan.confirm_delete_exclude", strings.ToLower(application.Name))
	case scanConfirmExclude:
		return i18n.T("tui.scan.confirm_exclude", application.Name)
	case scanConfirmIdentity:
		return i18n.T("tui.scan.confirm_identity", application.Name, view.scanIdentity)
	case scanConfirmMerge:
		return i18n.T("tui.scan.confirm_merge", application.Name)
	case scanConfirmPartial:
		return i18n.T("tui.scan.confirm_partial", len(selectedPartialFields(view)), application.Name)
	case scanConfirmKeep:
		return i18n.T("tui.scan.confirm_keep", application.Name)
	case scanConfirmAdd:
		return i18n.T("tui.scan.confirm_add", application.Name)
	case scanConfirmAddAll:
		return i18n.T("tui.scan.confirm_add_all", len(pendingScanAddedIDs(view)))
	case scanConfirmRescan:
		return i18n.T("tui.scan.confirm_rescan")
	default:
		return application.Name
	}
}

func scanApplicationState(view *tuiModel, id string) model.ManagedStatus {
	if application, found := view.scanProposed[id]; found {
		return application.StatusManaged
	}
	if application, found := findApplicationValue(view.catalog.Apps, id); found {
		return application.StatusManaged
	}
	return model.ManagedStatus{}
}

func displayScanDiffValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatScanAddedAt(value string) string {
	if len(value) >= 10 {
		return value[:10]
	}
	return displayValue(value)
}

func padTUI(value string, width int) string {
	value = truncateTUI(value, width)
	return value + strings.Repeat(" ", max(0, width-DisplayWidth(value)))
}
