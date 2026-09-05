package ui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/ui/component"
	"github.com/eoctet/tendkit/pkg/i18n"
)

type tuiCommandOutputRouter struct {
	mu       sync.Mutex
	commands map[tuiCommandOutputKey]*tuiCommandOutputState
	events   chan<- tuiEvent
	catalog  model.Config
	format   func(model.Config, string, string, string, string) ([]string, error)
	open     func(model.Config, string, string, string, string) (io.WriteCloser, error)
}

type tuiCommandOutputKey struct {
	commandID uint64
	appID     string
}

type tuiCommandOutputState struct {
	output  io.WriteCloser
	display *tuiWriter
}

func (router *tuiCommandOutputRouter) Write(output model.CommandOutput) {
	router.mu.Lock()
	defer router.mu.Unlock()
	key := tuiCommandOutputKey{commandID: output.CommandID, appID: output.AppID}
	state := router.commands[key]
	if state == nil && (len(output.Data) > 0 || output.Done) {
		state = &tuiCommandOutputState{
			output:  discardOutputCloser{Writer: io.Discard},
			display: &tuiWriter{events: router.events, level: LogInfo, operation: output.Operation, subject: output.AppName, catalog: router.catalog, format: router.format},
		}
		if router.open != nil {
			if writer, err := router.open(router.catalog, string(LogInfo), output.Operation, output.AppID, output.AppName); err == nil && writer != nil {
				state.output = writer
			}
		}
		router.commands[key] = state
	}
	if state == nil {
		return
	}
	if len(output.Data) > 0 {
		_, _ = state.output.Write(output.Data)
		_, _ = state.display.Write(output.Data)
	}
	if output.Done {
		state.display.Flush()
		_ = state.output.Close()
		delete(router.commands, key)
	}
}

func (router *tuiCommandOutputRouter) Flush() {
	router.mu.Lock()
	defer router.mu.Unlock()
	for key, state := range router.commands {
		state.display.Flush()
		_ = state.output.Close()
		delete(router.commands, key)
	}
}

type discardOutputCloser struct{ io.Writer }

func (discardOutputCloser) Close() error { return nil }

type tuiDownloadOutputState struct {
	mu     sync.Mutex
	output io.WriteCloser
	open   int
}

type tuiDownloadOutputStream struct {
	state   *tuiDownloadOutputState
	display *tuiWriter
	closed  bool
}

func newTUIDownloadOutput(
	catalog model.Config,
	format func(model.Config, string, string, string, string) ([]string, error),
	open func(model.Config, string, string, string, string) (io.WriteCloser, error),
	events chan<- tuiEvent,
	app model.Application,
) (io.WriteCloser, io.WriteCloser) {
	output := io.WriteCloser(discardOutputCloser{Writer: io.Discard})
	if open != nil {
		writer, err := open(catalog, string(LogInfo), model.OperationDownload, app.ID, app.Name)
		if err == nil && writer != nil {
			output = writer
		}
	}
	state := &tuiDownloadOutputState{output: output, open: 2}
	stream := func(level LogLevel) io.WriteCloser {
		return &tuiDownloadOutputStream{
			state: state,
			display: &tuiWriter{
				events: events, level: level, operation: model.OperationDownload,
				subject: app.Name, catalog: catalog, format: format,
			},
		}
	}
	return stream(LogInfo), stream(LogWarn)
}

func (stream *tuiDownloadOutputStream) Write(data []byte) (int, error) {
	stream.state.mu.Lock()
	defer stream.state.mu.Unlock()
	if stream.closed {
		return 0, io.ErrClosedPipe
	}
	_, _ = stream.state.output.Write(data)
	_, _ = stream.display.Write(data)
	return len(data), nil
}

func (stream *tuiDownloadOutputStream) Close() error {
	stream.state.mu.Lock()
	defer stream.state.mu.Unlock()
	if stream.closed {
		return nil
	}
	stream.closed = true
	stream.display.Flush()
	stream.state.open--
	if stream.state.open == 0 {
		_ = stream.state.output.Close()
	}
	return nil
}

func startTUIRun(parent context.Context, view *tuiModel, checkOnly, all bool, actions TUIActions, events chan<- tuiEvent) {
	view.clearTUIQuickSearch()
	if len(view.catalog.Apps) == 0 {
		return
	}
	if view.queue == nil {
		view.queue = map[string]model.Result{}
	}
	selected := view.catalog.Apps[view.selected]
	apps := []model.Application{selected}
	names := []string{selected.ID}
	target := selected.Name
	if all {
		apps = append([]model.Application(nil), view.catalog.Apps...)
		if !view.running {
			names = nil
		} else {
			names = names[:0]
			filtered := apps[:0]
			for _, app := range apps {
				if tuiAppScheduled(view, app.ID) {
					continue
				}
				names = append(names, app.ID)
				filtered = append(filtered, app)
			}
			apps = filtered
		}
		target = i18n.T("tui.all_apps")
	}
	if len(apps) == 0 {
		view.setMessage(i18n.T("tui.operation_already_queued", target), false)
		return
	}
	spec := tuiRunSpec{request: TUIRunRequest{Names: names, CheckOnly: checkOnly, AllRequested: all}, target: target, apps: apps}
	if !checkOnly && actions.DownloadAssetCandidates != nil {
		if view.preflightCancel != nil {
			view.preflightCancel()
		}
		preflight, cancel := context.WithCancel(parent)
		view.preflightCancel, view.preflightSeq = cancel, view.preflightSeq+1
		view.pendingRun = &spec
		sequence := view.preflightSeq
		view.appendStructuredLog(LogDebug, model.OperationDownload, spec.target, i18n.T("tui.download_preflight_started"))
		go func() {
			observer := TUIDownloadAssetObserver{Progress: func(progress model.DownloadAssetPreflightProgress) {
				select {
				case events <- tuiEvent{eventType: tuiEventDownloadPreflight, preflight: progress, sequence: sequence}:
				case <-preflight.Done():
				}
			}}
			choices, failures, err := actions.DownloadAssetCandidates(preflight, spec.request, observer)
			select {
			case events <- tuiEvent{eventType: tuiEventDownloadAssets, assets: choices, failures: failures, err: err, sequence: sequence}:
			case <-preflight.Done():
			}
		}()
		return
	}
	dispatchTUIRun(parent, view, spec, actions, events)
}

func handleDownloadAssetEvent(parent context.Context, view *tuiModel, event tuiEvent, actions TUIActions, events chan<- tuiEvent) {
	if event.sequence != view.preflightSeq || view.pendingRun == nil {
		return
	}
	spec := *view.pendingRun
	view.preflightCancel, view.pendingRun = nil, nil
	if event.err != nil {
		if event.err != context.Canceled {
			view.appendStructuredLog(LogError, model.OperationDownload, spec.target, i18n.T("tui.download_preflight_failed", i18n.ErrorText(event.err)))
			view.setMessage(i18n.ErrorText(event.err), true)
		}
		return
	}
	view.appendStructuredLog(LogDebug, model.OperationDownload, spec.target, i18n.T("tui.download_preflight_completed"))
	rejected := make(map[string]string, len(event.failures)+len(event.assets))
	for appID, err := range event.failures {
		rejected[appID] = i18n.ErrorText(err)
	}
	for appID, choices := range event.assets {
		if len(choices.Candidates) == 0 {
			rejected[appID] = i18n.T("tui.download_file_empty")
		}
	}
	if len(rejected) > 0 {
		remaining := make([]model.Application, 0, len(spec.apps))
		names := make([]string, 0, len(spec.apps))
		for _, app := range spec.apps {
			reason, drop := rejected[app.ID]
			if drop {
				view.appendStructuredLog(LogWarn, model.OperationUpdate, app.Name, i18n.T("tui.download_file_rejected", reason))
				continue
			}
			remaining = append(remaining, app)
			names = append(names, app.ID)
		}
		spec.apps = remaining
		spec.request.Names = names
		if len(spec.apps) == 0 {
			view.setMessage(i18n.T("tui.download_file_none_remaining"), true)
			return
		}
	}
	manual := make(map[string][]string)
	for appID, choices := range event.assets {
		candidates := choices.Candidates
		switch {
		case len(candidates) == 0:
			continue
		case len(candidates) == 1 && !choices.SelectionRequired:
			if spec.request.DownloadAssets == nil {
				spec.request.DownloadAssets = map[string]string{}
			}
			spec.request.DownloadAssets[appID] = candidates[0]
		default:
			manual[appID] = candidates
		}
	}
	if len(manual) > 0 {
		appID := firstDownloadAssetChoice(manual)
		view.confirmChoice = tuiConfirmationPrimary
		view.assetSelection = &tuiAssetSelection{spec: spec, appID: appID, candidates: manual[appID], remaining: manual}
		delete(view.assetSelection.remaining, appID)
		return
	}
	dispatchTUIRun(parent, view, spec, actions, events)
}

func handleDownloadAssetPreflightEvent(view *tuiModel, event tuiEvent) {
	if event.sequence != view.preflightSeq || view.pendingRun == nil {
		return
	}
	progress := event.preflight
	subject := strings.TrimSpace(progress.AppName)
	if subject == "" {
		subject = progress.AppID
	}
	switch progress.Stage {
	case model.DownloadAssetPreflightStarted:
		view.appendStructuredLog(LogDebug, model.OperationDownload, subject, i18n.T("tui.download_preflight_app_started"))
	case model.DownloadAssetPreflightCompleted:
		view.appendStructuredLog(LogInfo, model.OperationDownload, subject, i18n.T("tui.download_preflight_app_completed", progress.CandidateCount))
	case model.DownloadAssetPreflightFailed:
		view.appendStructuredLog(LogWarn, model.OperationDownload, subject, i18n.T("tui.download_preflight_app_failed", i18n.ErrorText(progress.Err)))
	}
}

// dispatchTUIRun is shared by direct and post-modal execution so an active
// transaction always receives work through its existing batch.
func dispatchTUIRun(parent context.Context, view *tuiModel, spec tuiRunSpec, actions TUIActions, events chan<- tuiEvent) {
	if view.running {
		for _, app := range spec.apps {
			if tuiAppScheduled(view, app.ID) {
				view.setMessage(i18n.T("tui.operation_already_queued", app.Name), false)
				return
			}
		}
		occupied := min(len(view.queue), view.catalog.Settings.Workers)
		available := max(0, view.catalog.Settings.Workers-occupied)
		if available == 0 {
			view.setMessage(i18n.T("tui.worker_pool_full", occupied, view.catalog.Settings.Workers), false)
			return
		}
		if view.batch == nil || view.batch.Add(spec.request) != nil {
			view.setMessage(i18n.T("tui.worker_pool_closing"), false)
			return
		}
		queueTUIApps(view, spec)
		for _, app := range spec.apps {
			view.activeRunIDs[app.ID] = true
		}
		view.rightQueue = true
		view.detailFocus = false
		view.setMessage(i18n.T("tui.operation_queued", spec.target), false)
		return
	}
	launchTUIRun(parent, view, spec, actions, events)
}

type tuiAssetSelection struct {
	spec       tuiRunSpec
	appID      string
	candidates []string
	selected   int
	offset     int
	remaining  map[string][]string
}

func handleTUIAssetSelection(parent context.Context, view *tuiModel, key string, actions TUIActions, events chan<- tuiEvent) bool {
	selection := view.assetSelection
	if selection == nil {
		return false
	}
	if moveTUIConfirmationChoice(view, key) {
		return true
	}
	switch key {
	case "up":
		selection.selected = max(0, selection.selected-1)
		selection.offset = min(selection.offset, selection.selected)
	case "down":
		selection.selected = min(len(selection.candidates)-1, selection.selected+1)
		selection.offset = max(selection.offset, selection.selected-assetSelectionViewport(view))
	case "esc", "n", "q":
		view.assetSelection = nil
		view.confirmChoice = tuiConfirmationPrimary
		view.setMessage(i18n.T("tui.download_file_cancelled"), false)
	case "enter":
		if view.confirmChoice == tuiConfirmationSecondary {
			view.assetSelection = nil
			view.confirmChoice = tuiConfirmationPrimary
			view.setMessage(i18n.T("tui.download_file_cancelled"), false)
			return true
		}
		fallthrough
	case "y":
		if selection.selected < 0 || selection.selected >= len(selection.candidates) {
			view.setMessage(i18n.T("tui.download_file_invalid"), true)
			return true
		}
		if selection.spec.request.DownloadAssets == nil {
			selection.spec.request.DownloadAssets = map[string]string{}
		}
		selection.spec.request.DownloadAssets[selection.appID] = selection.candidates[selection.selected]
		if len(selection.remaining) > 0 {
			next := firstDownloadAssetChoice(selection.remaining)
			selection.appID, selection.candidates, selection.selected, selection.offset = next, selection.remaining[next], 0, 0
			view.confirmChoice = tuiConfirmationPrimary
			delete(selection.remaining, next)
			return true
		}
		view.assetSelection = nil
		view.confirmChoice = tuiConfirmationPrimary
		dispatchTUIRun(parent, view, selection.spec, actions, events)
	}
	return true
}

func assetSelectionViewport(view *tuiModel) int { return max(1, view.height-11) }

func firstDownloadAssetChoice(choices map[string][]string) string {
	ids := make([]string, 0, len(choices))
	for id := range choices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids[0]
}

func tuiAppScheduled(view *tuiModel, appID string) bool {
	if view.activeRunIDs[appID] {
		return true
	}
	_, exists := view.queue[appID]
	return exists
}

func launchTUIRun(parent context.Context, view *tuiModel, spec tuiRunSpec, actions TUIActions, events chan<- tuiEvent) {
	operationContext, cancel := context.WithCancel(parent)
	view.running, view.cancel, view.rightQueue, view.detailFocus = true, cancel, true, false
	if view.queue == nil {
		view.queue = map[string]model.Result{}
	}
	queueTUIApps(view, spec)
	view.activeRunIDs = make(map[string]bool, len(spec.apps))
	for _, app := range spec.apps {
		view.activeRunIDs[app.ID] = true
	}
	operationCode := model.OperationUpdate
	if spec.request.CheckOnly {
		operationCode = model.OperationCheck
	}
	view.appendStructuredLog(LogInfo, operationCode, spec.target, i18n.T("tui.log.run_started", len(spec.apps), view.catalog.Settings.Workers))
	commandOutput := &tuiCommandOutputRouter{commands: map[tuiCommandOutputKey]*tuiCommandOutputState{}, events: events, catalog: view.catalog, format: view.operationText, open: view.commandOutputWriter}
	downloadCatalog, downloadFormat, downloadWriter := view.catalog, view.operationText, view.commandOutputWriter
	observer := TUIObserver{
		AppStart: func(result model.Result) { events <- tuiEvent{eventType: tuiEventAppStart, result: result} },
		Result: func(result model.Result) {
			switch result.Status {
			case model.StatusFailed:
				events <- tuiEvent{eventType: tuiEventResult, logLevel: LogError, result: result}
			case model.StatusSkipped, model.StatusMissing, model.StatusCancelled, model.StatusDownloadedUnverified:
				events <- tuiEvent{eventType: tuiEventResult, logLevel: LogWarn, result: result}
			case model.StatusWaiting, model.StatusChecking, model.StatusUpdating, model.StatusDownloading:
				events <- tuiEvent{eventType: tuiEventResult, logLevel: LogDebug, result: result}
			default:
				events <- tuiEvent{eventType: tuiEventResult, logLevel: LogInfo, result: result}
			}
		},
		UpdateStart: func(result model.Result) {
			events <- tuiEvent{eventType: tuiEventUpdateStart, result: result}
		},
		DownloadStart: func(result model.Result) {
			events <- tuiEvent{eventType: tuiEventDownloadStart, result: result}
		},
		DownloadProgress: func(progress model.DownloadProgress) {
			events <- tuiEvent{eventType: tuiEventDownloadProgress, progress: progress}
		},
		PreprocessProgress: func(progress model.PreprocessProgress) {
			level, message := preprocessTUILog(progress)
			emitTUIOperationText(events, level, "system", preprocessAction(progress), message)
		},
		DownloadOutput: func(app model.Application) (io.WriteCloser, io.WriteCloser) {
			return newTUIDownloadOutput(downloadCatalog, downloadFormat, downloadWriter, events, app)
		},
		CommandOutput: commandOutput.Write,
	}
	batch, err := actions.StartRun(operationContext, spec.request, observer)
	if err != nil {
		commandOutput.Flush()
		cancel()
		view.running, view.cancel, view.batch = false, nil, nil
		view.activeRunIDs = nil
		view.queue = map[string]model.Result{}
		view.queueOrder = nil
		view.downloadProgress = nil
		view.setMessage(i18n.ErrorText(err), true)
		return
	}
	view.batch = batch
	go func() {
		config, results, err := batch.Wait()
		commandOutput.Flush()
		events <- tuiEvent{eventType: tuiEventRunDone, config: config, items: results, err: err}
	}()
}

func preprocessTUILog(progress model.PreprocessProgress) (LogLevel, string) {
	subject := preprocessSubject(progress)
	switch progress.Status {
	case model.StatusStarted:
		return LogInfo, i18n.T("tui.preprocess_started", subject)
	case model.StatusSuccess:
		return LogInfo, i18n.T("tui.preprocess_completed", subject)
	case model.StatusSkipped:
		return LogWarn, i18n.T("tui.preprocess_skipped", subject)
	case model.StatusCancelled:
		return LogWarn, i18n.T("tui.preprocess_cancelled", subject)
	default:
		return LogError, i18n.T("tui.preprocess_failed", subject)
	}
}

func preprocessSubject(progress model.PreprocessProgress) string {
	if subject := strings.TrimSpace(progress.Subject); subject != "" {
		return subject
	}
	return preprocessAction(progress)
}

func preprocessAction(progress model.PreprocessProgress) string {
	if action := strings.TrimSpace(progress.Action); action != "" {
		return action
	}
	return "-"
}

// emitTUIOperationText intentionally bypasses the persistent log threshold:
// lifecycle feedback must remain visible while a batch is blocked in preprocessing.
// Messages are localized summaries; detailed command errors remain in the redacted run log.
func emitTUIOperationText(events chan<- tuiEvent, level LogLevel, operation, subject, message string) {
	for _, line := range FormatLogLines(time.Now(), level, operation, subject, message) {
		events <- tuiEvent{eventType: tuiEventLog, text: line}
	}
}

func queueTUIApps(view *tuiModel, spec tuiRunSpec) {
	for _, app := range spec.apps {
		mode := app.UpdateMode
		if spec.request.CheckOnly {
			mode = model.ModeCheck
		}
		view.queue[app.ID] = model.Result{
			AppID: app.ID, Name: app.Name, Mode: mode, Status: model.StatusWaiting, State: app.StatusManaged,
		}
		if !slices.Contains(view.queueOrder, app.ID) {
			view.queueOrder = append(view.queueOrder, app.ID)
		}
	}
}

func setTUIApplicationStatus(config *model.Config, id string, status model.ManagedStatus) {
	for index := range config.Apps {
		if config.Apps[index].ID == id {
			config.Apps[index].StatusManaged = status
			return
		}
	}
}

func toggleSelectedApplication(view *tuiModel, actions TUIActions) {
	if len(view.catalog.Apps) == 0 {
		return
	}
	index := view.selected
	name := view.catalog.Apps[index].Name
	proposed := cloneConfig(view.catalog)
	proposed.Apps[index].Enabled = !proposed.Apps[index].Enabled
	catalog, err := actions.SaveConfig(view.catalog, proposed)
	if err != nil {
		view.offerReload(err)
		view.setMessage(i18n.ErrorText(err), true)
		return
	}
	view.catalog = catalog
	if view.dirty {
		if workingApp, exists := findApplication(&view.working, catalog.Apps[index].ID); exists {
			workingApp.Enabled = catalog.Apps[index].Enabled
		}
	} else {
		view.working = cloneConfig(catalog)
	}
	view.selected = max(0, min(view.selected, len(catalog.Apps)-1))
	view.setMessage(i18n.T("tui.app_toggled", name), false)
}

func renderQueue(screen *tuiScreen, view *tuiModel, x, y, width, height int) {
	running := 0
	for _, item := range view.queue {
		if item.Status != model.StatusWaiting {
			running++
		}
	}
	screen.put(x+1, y+1, i18n.T("tui.queue_running", running, view.working.Settings.Workers), tuiBold)
	dots := ""
	for index := 0; index < view.working.Settings.Workers && index < 8; index++ {
		if index < running {
			dots += "● "
		} else {
			dots += "○ "
		}
	}
	screen.put(max(x+1, x+width-DisplayWidth(dots)-1), y+1, dots, tuiGreen)
	screen.put(x+1, y+2, strings.Repeat("─", max(1, width-2)), tuiDim)
	row := y + 4
	for _, id := range view.queueOrder {
		item, exists := view.queue[id]
		if !exists || row >= y+height-1 {
			continue
		}
		label := StatusLabel(item.Status)
		itemStyle := tuiGreen
		if item.Status == model.StatusWaiting {
			itemStyle = tuiYellow
		}
		screen.put(x+1, row, "● "+truncateTUI(item.Name, max(1, width-5)), itemStyle)
		row++
		progress := view.downloadProgress[id]
		if item.Status == model.StatusDownloading {
			percent := fmt.Sprintf(" %3d%%", progress)
			barWidth := max(3, width-5-DisplayWidth(percent))
			line := component.ProgressBar(progress, barWidth) + percent
			screen.put(x+3, row, truncateTUI(line, max(1, width-5)), tuiNormal)
		} else {
			screen.put(x+3, row, truncateTUI(label+"  "+displayValue(item.State.CurrentVersion)+" → "+displayValue(item.State.LatestVersion), max(1, width-5)), tuiNormal)
		}
		row += 2
	}
	if !view.running && running == 0 {
		lines := wrapTUI(i18n.T("tui.queue_empty"), max(1, width-2))
		for index, line := range lines[:min(len(lines), max(0, height-5))] {
			screen.put(x+1, y+4+index, line, tuiDim)
		}
	}
}
