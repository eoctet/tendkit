package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/ui/component"
	"github.com/eoctet/tendkit/pkg/i18n"
)

// TUIRunRequest selects the applications and operation mode for a TUI batch.
type TUIRunRequest struct {
	Names          []string
	CheckOnly      bool
	DownloadAssets map[string]string
}

// TUIObserver receives bounded operation events and output streams.
type TUIObserver struct {
	AppStart         func(model.Result)
	Result           func(model.Result)
	UpdateStart      func(model.Result)
	DownloadStart    func(model.Result)
	DownloadProgress func(model.DownloadProgress)
	DownloadOutput   func(model.Application) (io.WriteCloser, io.WriteCloser)
	CommandOutput    func(model.CommandOutput)
}

// TUIDownloadAssetObserver reports download preflight progress before a run starts.
type TUIDownloadAssetObserver struct {
	Progress func(model.DownloadAssetPreflightProgress)
}

// TUIActions defines the persistence and execution boundary supplied by the app layer.
type TUIActions struct {
	Load                    func() (model.Config, model.RuntimeState, error)
	Reload                  func() (model.Config, model.RuntimeState, error)
	SaveConfig              func(model.Config, model.Config) (model.Config, error)
	StartRun                func(context.Context, TUIRunRequest, TUIObserver) (*TUIRunBatch, error)
	DownloadAssetCandidates func(context.Context, TUIRunRequest, TUIDownloadAssetObserver) (map[string]model.DownloadAssetChoices, map[string]error, error)
	Scan                    func(context.Context, TUIScanRequest, TUIScanObserver) (TUIScanSnapshot, error)
	SaveScan                func(model.Config, model.Config) (model.Config, error)
	GenerateIdentity        func(model.Application) (string, error)
	OperationLog            func(model.Config, string, string, string, string) ([]string, error)
	OperationText           func(model.Config, string, string, string, string) ([]string, error)
	CommandOutputWriter     func(model.Config, string, string, string, string) (io.WriteCloser, error)
}

// TUIScanRequest selects a full scan or one concrete application scan.
type TUIScanRequest struct {
	Application *model.Application
}

// TUIScanObserver reports scanner milestones without coupling the UI to scanner internals.
type TUIScanObserver struct {
	Progress func(stage, subject string)
}

// TUIScanSnapshot is an unpersisted scan candidate displayed by the scan page.
type TUIScanSnapshot struct {
	BaseConfig model.Config
	BaseState  model.RuntimeState
	Config     model.Config
	State      model.RuntimeState
	Changes    []model.ScanApplicationChange
	Added      []model.Application
	Removed    []model.Application
	Excluded   []model.Application
}

// TUIRunBatch exposes an active, dynamically extensible application batch.
type TUIRunBatch struct {
	AddRequest func(TUIRunRequest) error
	WaitResult func() (model.Config, []model.Result, error)
}

// Add appends a request while the active batch still accepts work.
func (batch *TUIRunBatch) Add(request TUIRunRequest) error {
	if batch == nil || batch.AddRequest == nil {
		return errors.New(i18n.T("tui.backend_missing"))
	}
	return batch.AddRequest(request)
}

// Wait blocks until the active batch completes and returns its final snapshot.
func (batch *TUIRunBatch) Wait() (model.Config, []model.Result, error) {
	if batch == nil || batch.WaitResult == nil {
		return model.Config{}, nil, errors.New(i18n.T("tui.backend_missing"))
	}
	return batch.WaitResult()
}

type tuiPage int

const (
	tuiApps tuiPage = iota
	tuiConfig
	tuiScan
)

const (
	tuiConfirmationPrimary = iota
	tuiConfirmationSecondary
)

type tuiEvent struct {
	eventType string
	key       string
	text      string
	logLevel  LogLevel
	operation string
	result    model.Result
	progress  model.DownloadProgress
	config    model.Config
	items     []model.Result
	scan      TUIScanSnapshot
	err       error
	stage     string
	subject   string
	preflight model.DownloadAssetPreflightProgress
	assets    map[string]model.DownloadAssetChoices
	failures  map[string]error
	sequence  uint64
}

// Event names are internal protocol values shared by producers and the single
// TUI event loop. Keeping them centralized prevents silent producer/consumer
// mismatches when an event is renamed.
const (
	tuiEventInputError        = "input_error"
	tuiEventKey               = "key"
	tuiEventLog               = "log"
	tuiEventAppStart          = "app_start"
	tuiEventUpdateStart       = "update_start"
	tuiEventDownloadStart     = "download_start"
	tuiEventDownloadProgress  = "download_progress"
	tuiEventResult            = "result"
	tuiEventRunDone           = "run_done"
	tuiEventScanDone          = "scan_done"
	tuiEventScanProgress      = "scan_progress"
	tuiEventDownloadAssets    = "download_assets"
	tuiEventDownloadPreflight = "download_preflight"
)

type tuiRunSpec struct {
	request TUIRunRequest
	target  string
	apps    []model.Application
}

type tuiModel struct {
	appsPageState
	configPageState
	runState
	scanPageState
	viewportState
	interactionState
	colorMode           Mode
	assetSelection      *tuiAssetSelection
	operationLog        func(model.Config, string, string, string, string) ([]string, error)
	operationText       func(model.Config, string, string, string, string) ([]string, error)
	commandOutputWriter func(model.Config, string, string, string, string) (io.WriteCloser, error)
}

type appsPageState struct {
	catalog      model.Config
	state        model.RuntimeState
	page         tuiPage
	selected     int
	scroll       int
	rightQueue   bool
	detailFocus  bool
	detailOffset int
	logFocus     bool
	logOffset    int
	logs         []string
	searchActive bool
	searchQuery  string
}

type configPageState struct {
	working           model.Config
	configIndex       int
	configScroll      int
	configAppFocus    bool
	appFieldIndex     int
	appFieldScroll    int
	dirty             bool
	editing           bool
	editValue         string
	editCursor        int
	configExitConfirm bool
}

type runState struct {
	queue            map[string]model.Result
	queueOrder       []string
	downloadProgress map[string]int
	activeRunIDs     map[string]bool
	batch            *TUIRunBatch
	running          bool
	cancel           context.CancelFunc
	preflightCancel  context.CancelFunc
	preflightSeq     uint64
	pendingRun       *tuiRunSpec
}

type scanPageState struct {
	scanSelected     int
	scanScroll       int
	scanDetail       int
	scanLogs         []string
	scanRunning      bool
	scanCancel       context.CancelFunc
	scanProposed     map[string]model.Application
	scanObservations map[string]model.ScanObservation
	scanChanges      map[string]model.ScanApplicationChange
	scanAdded        map[string]bool
	scanRemoved      map[string]bool
	scanExcluded     map[string]bool
	scanIgnored      map[string]bool
	scanConfirm      string
	scanConfirmID    string
	scanIdentity     string
	scanPartial      bool
	partialIndex     int
	partialFields    map[string]bool
	partialOffset    int
	scanLogFocus     bool
	scanLogOffset    int
	scanCompleted    bool
	scanAutoDone     bool
	scanRescan       bool
	scanEditFocus    bool
	scanEditID       string
	scanEditSnapshot model.Application
	scanEditDraft    model.Application
	scanFieldIndex   int
	scanFieldScroll  int
}

type viewportState struct {
	width  int
	height int
}

type interactionState struct {
	confirm       bool
	confirmAll    bool
	confirmChoice int
	quitPending   bool
	message       string
	messageError  bool
	messageUntil  time.Time
	reloadConfirm bool
}

const (
	tuiMessageDuration      = 3 * time.Second
	tuiErrorMessageDuration = 6 * time.Second
	// tuiInputEscapeTimeout distinguishes a standalone Escape key from the
	// beginning of a terminal CSI sequence split across reads.
	tuiInputEscapeTimeout   = 25 * time.Millisecond
	tuiMaxEditValueBytes    = 64 * 1024
	tuiMaxQuickSearchLength = 20
	tuiShutdownGracePeriod  = 4 * time.Second
)

type tuiWriter struct {
	mu        sync.Mutex
	buffer    string
	events    chan<- tuiEvent
	level     LogLevel
	operation string
	subject   string
	catalog   model.Config
	format    func(model.Config, string, string, string, string) ([]string, error)
	dropped   int
}

const tuiMaxLogLineBytes = 16 * 1024

const (
	tuiEnterScreen = "\033[?1049h\033[?7l\033[2J\033[H\033[?25l"
	tuiExitScreen  = "\033[0m\033[?25h\033[?7h\033[?1049l"
)

func (writer *tuiWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.buffer += string(data)
	for {
		index := strings.IndexAny(writer.buffer, "\r\n")
		if index < 0 {
			break
		}
		line := writer.buffer[:index]
		next := index + 1
		if next < len(writer.buffer) && writer.buffer[index] == '\r' && writer.buffer[next] == '\n' {
			next++
		}
		writer.buffer = writer.buffer[next:]
		if line != "" {
			writer.emitLog(truncateTUILogLine(line))
		}
	}
	for len(writer.buffer) > tuiMaxLogLineBytes {
		end := tuiMaxLogLineBytes
		for end > 0 && !utf8.RuneStart(writer.buffer[end]) {
			end--
		}
		chunk := writer.buffer[:end]
		writer.buffer = writer.buffer[end:]
		writer.emitLog(strings.ToValidUTF8(chunk, "�") + "…")
	}
	return len(data), nil
}

func (writer *tuiWriter) Flush() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.buffer != "" {
		writer.emitLog(truncateTUILogLine(strings.TrimSuffix(writer.buffer, "\r")))
		writer.buffer = ""
	}
	writer.emitDropped()
}

func (writer *tuiWriter) emitLog(line string) {
	writer.emitDropped()
	level := writer.level
	if level == "" {
		level = LogInfo
	}
	if writer.format != nil {
		lines, err := writer.format(writer.catalog, string(level), writer.operation, writer.subject, line)
		if err == nil {
			for _, formatted := range lines {
				select {
				case writer.events <- tuiEvent{eventType: tuiEventLog, text: formatted}:
				default:
					writer.dropped++
				}
			}
			return
		}
	}
	select {
	case writer.events <- tuiEvent{eventType: tuiEventLog, logLevel: level, operation: writer.operation, subject: writer.subject, text: line}:
	default:
		writer.dropped++
	}
}

func (writer *tuiWriter) emitDropped() {
	if writer.dropped == 0 {
		return
	}
	select {
	case writer.events <- tuiEvent{eventType: tuiEventLog, logLevel: LogWarn, operation: writer.operation, subject: writer.subject, text: i18n.T("tui.output_dropped", writer.dropped)}:
		writer.dropped = 0
	default:
	}
}

// RunTUI runs the single-page terminal interface until the user exits or the
// context is cancelled, restoring terminal state on every return path.
func RunTUI(ctx context.Context, input, output *os.File, actions TUIActions, color Mode) error {
	if !IsTerminal(input) || !IsTerminal(output) {
		return errors.New(i18n.T("tui.terminal_required"))
	}
	if actions.Load == nil || actions.Reload == nil || actions.SaveConfig == nil || actions.StartRun == nil || actions.Scan == nil || actions.SaveScan == nil || actions.GenerateIdentity == nil {
		return errors.New(i18n.T("tui.backend_missing"))
	}
	catalog, state, err := actions.Load()
	if err != nil {
		return err
	}
	raw, err := enterRawMode(input)
	if err != nil {
		return err
	}
	defer raw.restore()
	_, _ = io.WriteString(output, tuiEnterScreen)
	defer func() { _, _ = io.WriteString(output, tuiExitScreen) }()

	width, height := terminalSize(output)
	modelState := tuiModel{
		appsPageState:       appsPageState{catalog: catalog, state: state},
		configPageState:     configPageState{working: cloneConfig(catalog)},
		runState:            runState{queue: map[string]model.Result{}},
		viewportState:       viewportState{width: width, height: height},
		colorMode:           color,
		operationLog:        actions.OperationLog,
		operationText:       actions.OperationText,
		commandOutputWriter: actions.CommandOutputWriter,
	}
	events := make(chan tuiEvent, 256)
	inputContext, cancelInput := context.WithCancel(ctx)
	inputDone := make(chan struct{})
	go func() { defer close(inputDone); readTUIInput(inputContext, input, events) }()
	defer func() {
		cancelInput()
		if modelState.preflightCancel != nil {
			modelState.preflightCancel()
		}
		<-inputDone
	}()
	refresh := time.NewTicker(250 * time.Millisecond)
	defer refresh.Stop()
	renderTUI(output, &modelState)
	for {
		select {
		case <-ctx.Done():
			if modelState.scanCancel != nil {
				modelState.scanCancel()
				if !waitForTUIScan(&modelState, actions, events, output, tuiShutdownGracePeriod) {
					return errors.Join(ctx.Err(), errors.New(i18n.T("tui.scan.shutdown_timeout", tuiShutdownGracePeriod)))
				}
			}
			if modelState.cancel != nil {
				modelState.cancel()
				waitForTUIRun(&modelState, actions, events, output)
			}
			return ctx.Err()
		case now := <-refresh.C:
			newWidth, newHeight := terminalSize(output)
			redraw := modelState.expireMessage(now)
			if newWidth != modelState.width || newHeight != modelState.height {
				modelState.width, modelState.height = newWidth, newHeight
				redraw = true
			}
			if redraw {
				renderTUI(output, &modelState)
			}
		case event := <-events:
			quit := handleTUIEvent(ctx, &modelState, event, actions, events)
			renderTUI(output, &modelState)
			if quit {
				return nil
			}
		}
	}
}

func waitForTUIScan(view *tuiModel, actions TUIActions, events chan tuiEvent, output io.Writer, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for view.scanRunning {
		select {
		case event := <-events:
			handleTUIEvent(context.Background(), view, event, actions, events)
			renderTUI(output, view)
		case <-timer.C:
			return false
		}
	}
	return true
}

func waitForTUIRun(view *tuiModel, actions TUIActions, events chan tuiEvent, output io.Writer) {
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for view.running {
		select {
		case event := <-events:
			handleTUIEvent(context.Background(), view, event, actions, events)
			renderTUI(output, view)
		case <-timer.C:
			return
		}
	}
}

func readTUIInput(ctx context.Context, input *os.File, events chan<- tuiEvent) {
	decoder := tuiInputDecoder{}
	buffer := make([]byte, 64)
	for {
		ready, err := waitTUIInput(input, tuiInputEscapeTimeout)
		if err != nil {
			emitTUIInputEvent(ctx, events, tuiEvent{eventType: tuiEventInputError, err: err})
			return
		}
		if !ready {
			if ctx.Err() != nil {
				return
			}
			for _, key := range decoder.flushPending() {
				if !emitTUIInputEvent(ctx, events, tuiEvent{eventType: tuiEventKey, key: key}) {
					return
				}
			}
			continue
		}
		count, err := input.Read(buffer)
		if count > 0 {
			for _, key := range decoder.decode(buffer[:count]) {
				if !emitTUIInputEvent(ctx, events, tuiEvent{eventType: tuiEventKey, key: key}) {
					return
				}
			}
		}
		if err == nil {
			continue
		}
		for _, key := range decoder.flushPending() {
			if !emitTUIInputEvent(ctx, events, tuiEvent{eventType: tuiEventKey, key: key}) {
				return
			}
		}
		emitTUIInputEvent(ctx, events, tuiEvent{eventType: tuiEventInputError, err: err})
		return
	}
}

func emitTUIInputEvent(ctx context.Context, events chan<- tuiEvent, event tuiEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// tuiInputDecoder preserves incomplete terminal sequences between Read calls.
// A terminal is a byte stream, so CSI and UTF-8 boundaries are not guaranteed
// to align with the reader buffer.
type tuiInputDecoder struct {
	pending []byte
}

func (decoder *tuiInputDecoder) decode(data []byte) []string {
	decoder.pending = append(decoder.pending, data...)
	keys := make([]string, 0, len(decoder.pending))
	index := 0
	for index < len(decoder.pending) {
		incomplete := false
		if decoder.pending[index] == 0x1b {
			if index+1 == len(decoder.pending) {
				break
			}
			if decoder.pending[index+1] == '[' {
				if index+2 >= len(decoder.pending) {
					break
				}
				key, size, complete := decodeTUICSI(decoder.pending[index:])
				if !complete {
					break
				}
				if key != "" {
					keys = append(keys, key)
					index += size
					continue
				}
			}
			keys = append(keys, "esc")
			index++
			continue
		}
		switch decoder.pending[index] {
		case '\r', '\n':
			keys = append(keys, "enter")
			index++
		case '\t':
			keys = append(keys, "tab")
			index++
		case 0x03:
			keys = append(keys, "ctrl+c")
			index++
		case 0x7f, 0x08:
			keys = append(keys, "backspace")
			index++
		case 0x13:
			keys = append(keys, "ctrl+s")
			index++
		case 0x15:
			keys = append(keys, "ctrl+u")
			index++
		default:
			r, size := utf8.DecodeRune(decoder.pending[index:])
			if r == utf8.RuneError && size == 1 {
				if !utf8.FullRune(decoder.pending[index:]) {
					incomplete = true
					break
				}
				index++
				continue
			}
			keys = append(keys, string(r))
			index += size
		}
		if incomplete {
			break
		}
	}
	decoder.pending = append(decoder.pending[:0], decoder.pending[index:]...)
	return keys
}

func (decoder *tuiInputDecoder) flushPending() []string {
	if len(decoder.pending) == 0 {
		return nil
	}
	pending := append([]byte(nil), decoder.pending...)
	decoder.pending = decoder.pending[:0]
	keys := make([]string, 0, len(pending))
	for len(pending) > 0 {
		if pending[0] == 0x1b {
			keys = append(keys, "esc")
			pending = pending[1:]
			continue
		}
		flushed := (&tuiInputDecoder{}).decode(pending)
		if len(flushed) == 0 {
			break
		}
		keys = append(keys, flushed...)
		break
	}
	return keys
}

func decodeTUICSI(data []byte) (key string, size int, complete bool) {
	if len(data) < 3 || data[0] != 0x1b || data[1] != '[' {
		return "", 0, false
	}
	switch data[2] {
	case 'A':
		return "up", 3, true
	case 'B':
		return "down", 3, true
	case 'C':
		return "right", 3, true
	case 'D':
		return "left", 3, true
	case 'H':
		return "home", 3, true
	case 'F':
		return "end", 3, true
	case '5', '6', '1', '3', '4':
		if len(data) < 4 {
			return "", 0, false
		}
		if data[3] != '~' {
			return "", 0, true
		}
		switch data[2] {
		case '5':
			return "pageup", 4, true
		case '6':
			return "pagedown", 4, true
		case '1':
			return "home", 4, true
		case '3':
			return "delete", 4, true
		case '4':
			return "end", 4, true
		}
	}
	return "", 0, true
}

func decodeTUIKeys(data []byte) []string {
	return (&tuiInputDecoder{}).decode(data)
}

func handleTUIEvent(parent context.Context, view *tuiModel, event tuiEvent, actions TUIActions, events chan<- tuiEvent) bool {
	switch event.eventType {
	case tuiEventInputError:
		view.setMessage(i18n.ErrorText(event.err), true)
		if view.scanRunning && view.scanCancel != nil {
			view.scanCancel()
			view.quitPending = true
			return false
		}
		if view.running && view.cancel != nil {
			view.cancel()
			view.quitPending = true
			return false
		}
		return true
	case tuiEventLog:
		if event.operation != "" {
			view.appendStructuredLog(event.logLevel, event.operation, event.subject, event.text)
		} else {
			view.appendLog(event.text)
		}
	case tuiEventAppStart:
		view.queue[event.result.AppID] = event.result
		if !containsString(view.queueOrder, event.result.AppID) {
			view.queueOrder = append(view.queueOrder, event.result.AppID)
		}
		view.rightQueue = true
		view.detailFocus = false
		view.appendStructuredLog(LogDebug, tuiResultOperation(event.result), event.result.Name, i18n.T("tui.log.started"))
	case tuiEventUpdateStart:
		view.queue[event.result.AppID] = event.result
		if !containsString(view.queueOrder, event.result.AppID) {
			view.queueOrder = append(view.queueOrder, event.result.AppID)
		}
		view.rightQueue = true
		view.detailFocus = false
	case tuiEventDownloadStart:
		view.queue[event.result.AppID] = event.result
		if !containsString(view.queueOrder, event.result.AppID) {
			view.queueOrder = append(view.queueOrder, event.result.AppID)
		}
		view.rightQueue = true
		view.detailFocus = false
		view.appendStructuredLog(LogDebug, model.OperationDownload, event.result.Name, i18n.T("tui.log.started"))
	case tuiEventDownloadProgress:
		if _, exists := view.queue[event.progress.AppID]; exists {
			if view.downloadProgress == nil {
				view.downloadProgress = map[string]int{}
			}
			view.downloadProgress[event.progress.AppID] = max(0, min(100, event.progress.Percent))
		}
	case tuiEventResult:
		delete(view.queue, event.result.AppID)
		delete(view.downloadProgress, event.result.AppID)
		delete(view.activeRunIDs, event.result.AppID)
		setTUIApplicationStatus(&view.catalog, event.result.AppID, event.result.State)
		message := i18n.Localize(event.result.Message)
		if strings.TrimSpace(message) == "" {
			message = StatusLabel(event.result.Status)
		}
		view.appendStructuredLog(event.logLevel, tuiResultOperation(event.result), event.result.Name, message)
	case tuiEventRunDone:
		view.running = false
		view.cancel = nil
		view.batch = nil
		for id := range view.activeRunIDs {
			delete(view.queue, id)
		}
		view.activeRunIDs = nil
		if event.err == nil {
			view.catalog = event.config
			view.working = cloneConfig(event.config)
		} else if actions.Load != nil {
			catalog, state, loadErr := actions.Load()
			if loadErr == nil {
				view.catalog, view.state = catalog, state
				if !view.dirty {
					view.working = cloneConfig(catalog)
				}
				view.selected = max(0, min(view.selected, len(catalog.Apps)-1))
			} else {
				view.setMessage(i18n.ErrorText(loadErr), true)
			}
		}
		if event.err != nil && !errors.Is(event.err, context.Canceled) {
			view.offerReload(event.err)
			view.appendStructuredLog(LogError, "system", "-", i18n.T("tui.log.failed", event.err))
			view.setMessage(i18n.ErrorText(event.err), true)
		} else if errors.Is(event.err, context.Canceled) {
			view.appendStructuredLog(LogWarn, "system", "-", i18n.T("tui.cancelled"))
			view.setMessage(i18n.T("tui.cancelled"), false)
		} else {
			view.appendStructuredLog(LogInfo, "system", "-", i18n.T("tui.operation_finished", len(event.items)))
			view.setMessage(i18n.T("tui.operation_finished", len(event.items)), false)
		}
		if view.quitPending {
			return true
		}
		view.queue = map[string]model.Result{}
		view.queueOrder = nil
		view.downloadProgress = nil
	case tuiEventScanDone:
		finishTUIScan(view, event)
		if event.err != nil {
			view.offerReload(event.err)
		}
		if view.quitPending {
			return true
		}
	case tuiEventScanProgress:
		view.appendScanStructuredLog(LogInfo, displayValue(event.subject), scanProgressMessage(event.stage, event.subject))
	case tuiEventDownloadAssets:
		handleDownloadAssetEvent(parent, view, event, actions, events)
	case tuiEventDownloadPreflight:
		handleDownloadAssetPreflightEvent(view, event)
	case tuiEventKey:
		return handleTUIKey(parent, view, event.key, actions, events)
	}
	return false
}

func handleTUIKey(parent context.Context, view *tuiModel, key string, actions TUIActions, events chan<- tuiEvent) bool {
	if key == "esc" && view.preflightCancel != nil {
		if view.pendingRun != nil {
			view.appendStructuredLog(LogWarn, model.OperationDownload, view.pendingRun.target, i18n.T("tui.download_preflight_cancelled"))
		}
		view.preflightCancel()
		view.preflightCancel, view.pendingRun = nil, nil
		view.preflightSeq++ // discard a late completion from the cancelled request.
		return false
	}
	if handleTUIAssetSelection(parent, view, key, actions, events) {
		return false
	}
	keymap := tuiCurrentKeymap(view)
	if !keymap.Permits(key) {
		return false
	}
	if key == "ctrl+c" {
		if view.searchActive {
			view.searchQuery = ""
			return false
		}
		return handleTUIInterrupt(view)
	}
	if view.editing {
		return handleTUIEditKey(view, key, actions)
	}
	if view.reloadConfirm {
		handleTUIReloadConfirmation(view, key, actions)
		return false
	}
	if view.configExitConfirm {
		handleTUIConfigExitConfirmation(view, key, actions)
		return false
	}
	if view.scanConfirm != "" {
		handlePendingScanConfirmation(parent, view, key, actions, events)
		return false
	}
	if view.scanPartial {
		handleScanPartialKey(view, key, actions)
		return false
	}
	if view.confirm {
		handleTUIRunConfirmation(parent, view, key, actions, events)
		return false
	}
	if key == "esc" {
		if view.running {
			if view.cancel != nil {
				view.cancel()
			}
			return false
		}
		if view.scanRunning {
			if view.scanCancel != nil {
				view.scanCancel()
			}
			return false
		}
	}
	if key == "q" && (view.running || view.scanRunning) {
		if view.running && view.cancel != nil {
			view.cancel()
		}
		if view.scanRunning && view.scanCancel != nil {
			view.scanCancel()
		}
		view.quitPending = true
		return false
	}
	if view.searchActive {
		handleTUIQuickSearchKey(view, key)
		return false
	}
	if view.page == tuiConfig {
		return handleConfigKey(view, key, actions)
	}
	if view.page == tuiScan {
		return handleScanKey(parent, view, key, actions, events)
	}
	if view.detailFocus {
		if handled := handleTUIAppDetailKey(view, key); handled {
			return false
		}
	}
	return handleTUIAppPageKey(parent, view, key, actions, events)
}

func (view *tuiModel) offerReload(err error) {
	var required interface{ ReloadRequired() bool }
	if errors.As(err, &required) && required.ReloadRequired() {
		view.reloadConfirm = true
		view.confirmChoice = tuiConfirmationPrimary
	}
}

func handleTUIReloadConfirmation(view *tuiModel, key string, actions TUIActions) {
	if moveTUIConfirmationChoice(view, key) {
		return
	}
	if key == "esc" || key == "n" || (key == "enter" && view.confirmChoice == tuiConfirmationSecondary) {
		view.reloadConfirm = false
		view.confirmChoice = tuiConfirmationPrimary
		return
	}
	if key != "enter" && key != "y" {
		return
	}
	catalog, state, err := actions.Reload()
	if err != nil {
		view.setMessage(i18n.ErrorText(err), true)
		return
	}
	view.catalog, view.working, view.state = catalog, cloneConfig(catalog), state
	view.dirty, view.reloadConfirm = false, false
	view.confirmChoice = tuiConfirmationPrimary
	resetScanPreview(view, "")
	view.setMessage(i18n.T("tui.reload_complete"), false)
}

func handleTUIInterrupt(view *tuiModel) bool {
	if view.scanRunning && view.scanCancel != nil {
		view.scanCancel()
		view.quitPending = true
		return false
	}
	if view.running && view.cancel != nil {
		view.cancel()
		view.quitPending = true
		return false
	}
	if view.preflightCancel != nil {
		view.preflightCancel()
		view.preflightCancel, view.pendingRun = nil, nil
		view.preflightSeq++
		return false
	}
	return true
}

// tuiConfirmationLabels is shared by the footer keymap and dialog renderers.
func tuiConfirmationLabels(view *tuiModel) (primaryKey, secondaryKey string) {
	if view.configExitConfirm {
		return "tui.save", "tui.discard"
	}
	if view.reloadConfirm {
		return "tui.reload", "tui.cancel"
	}
	if view.scanConfirm == scanConfirmDeleteExclude {
		return "tui.confirm", "tui.scan.skip_exclusion"
	}
	return "tui.confirm", "tui.cancel"
}

func handleTUIEditKey(view *tuiModel, key string, actions TUIActions) bool {
	switch key {
	case "enter", "ctrl+s":
		return finishTUIEdit(view, key == "ctrl+s", actions)
	case "esc":
		view.editing = false
		view.editCursor = 0
	case "left":
		view.editCursor = max(0, view.editCursor-1)
	case "right":
		view.editCursor = min(utf8.RuneCountInString(view.editValue), view.editCursor+1)
	case "home":
		view.editCursor = 0
	case "end":
		view.editCursor = utf8.RuneCountInString(view.editValue)
	case "backspace":
		deleteTUIEditRune(view, -1)
	case "delete":
		deleteTUIEditRune(view, 0)
	default:
		insertTUIEditRune(view, key)
	}
	return false
}

func finishTUIEdit(view *tuiModel, save bool, actions TUIActions) bool {
	if err := applyActiveTUIEdit(view, view.editValue); err != nil {
		view.setMessage(i18n.ErrorText(err), true)
		return false
	}
	view.editing = false
	view.editCursor = 0
	if view.page == tuiScan && view.scanEditFocus {
		if save {
			stageScanCandidateEdit(view)
		} else {
			view.setMessage(i18n.T("tui.scan.candidate_modified"), false)
		}
		return false
	}
	view.dirty = true
	if save {
		return handleConfigKey(view, "ctrl+s", actions)
	}
	view.setMessage(i18n.T("tui.unsaved"), false)
	return false
}

func deleteTUIEditRune(view *tuiModel, offset int) {
	runes := []rune(view.editValue)
	index := view.editCursor + offset
	if index < 0 || index >= len(runes) {
		return
	}
	view.editValue = string(append(runes[:index], runes[index+1:]...))
	if offset < 0 {
		view.editCursor--
	}
}

func insertTUIEditRune(view *tuiModel, key string) {
	if utf8.RuneCountInString(key) != 1 {
		return
	}
	character := []rune(key)[0]
	if unicode.IsControl(character) {
		view.setMessage(i18n.T("tui.edit_control_character"), true)
		return
	}
	if len(view.editValue)+len(key) > tuiMaxEditValueBytes {
		view.setMessage(i18n.T("tui.edit_too_long", tuiMaxEditValueBytes), true)
		return
	}
	runes := []rune(view.editValue)
	cursor := max(0, min(len(runes), view.editCursor))
	runes = append(runes, 0)
	copy(runes[cursor+1:], runes[cursor:])
	runes[cursor] = character
	view.editValue = string(runes)
	view.editCursor = cursor + 1
}

func handlePendingScanConfirmation(parent context.Context, view *tuiModel, key string, actions TUIActions, events chan<- tuiEvent) {
	handleScanConfirmationKey(view, key, actions)
	if view.scanRescan {
		view.scanRescan = false
		startTUIScan(parent, view, actions, events, "")
	}
}

func handleTUIRunConfirmation(parent context.Context, view *tuiModel, key string, actions TUIActions, events chan<- tuiEvent) {
	if moveTUIConfirmationChoice(view, key) {
		return
	}
	switch key {
	case "enter":
		if view.confirmChoice == tuiConfirmationSecondary {
			closeTUIRunConfirmation(view)
			return
		}
		view.confirm = false
		startTUIRun(parent, view, false, view.confirmAll, actions, events)
		view.confirmAll = false
		view.confirmChoice = tuiConfirmationPrimary
	case "y":
		view.confirm = false
		startTUIRun(parent, view, false, view.confirmAll, actions, events)
		view.confirmAll = false
		view.confirmChoice = tuiConfirmationPrimary
	case "esc", "n", "q":
		closeTUIRunConfirmation(view)
	}
}

func handleTUIConfigExitConfirmation(view *tuiModel, key string, actions TUIActions) {
	if moveTUIConfirmationChoice(view, key) {
		return
	}
	save := key == "y" || (key == "enter" && view.confirmChoice == tuiConfirmationPrimary)
	discard := key == "esc" || key == "n" || (key == "enter" && view.confirmChoice == tuiConfirmationSecondary)
	if !save && !discard {
		return
	}
	view.configExitConfirm = false
	view.confirmChoice = tuiConfirmationPrimary
	if discard {
		revertTUIConfig(view)
		leaveTUIConfig(view)
		return
	}
	saveTUIConfig(view, configRows(&view.working), actions)
	if !view.dirty {
		leaveTUIConfig(view)
	}
}

func leaveTUIConfig(view *tuiModel) {
	view.page = tuiApps
	view.configAppFocus = false
	view.appFieldScroll = 0
	view.clearTUIQuickSearch()
}

func closeTUIRunConfirmation(view *tuiModel) {
	view.confirm = false
	view.confirmAll = false
	view.confirmChoice = tuiConfirmationPrimary
}

func moveTUIConfirmationChoice(view *tuiModel, key string) bool {
	switch key {
	case "left":
		view.confirmChoice = tuiConfirmationPrimary
		return true
	case "right":
		view.confirmChoice = tuiConfirmationSecondary
		return true
	default:
		return false
	}
}

func handleTUIAppDetailKey(view *tuiModel, key string) bool {
	switch key {
	case "esc", "enter":
		view.detailFocus = false
	case "up", "k":
		scrollTUIDetails(view, -1)
	case "down", "j":
		scrollTUIDetails(view, 1)
	case "pageup":
		scrollTUIDetails(view, -max(1, tuiDetailViewportHeight(view)-1))
	case "pagedown":
		scrollTUIDetails(view, max(1, tuiDetailViewportHeight(view)-1))
	case "home":
		view.detailOffset = 0
	case "end":
		view.detailOffset = tuiMaxDetailOffset(view)
	default:
		return false
	}
	return true
}

func handleTUIAppPageKey(parent context.Context, view *tuiModel, key string, actions TUIActions, events chan<- tuiEvent) bool {
	if handleTUIAppNavigationKey(view, key) {
		return false
	}
	switch key {
	case "f":
		if !view.detailFocus && !view.rightQueue && !view.logFocus && !view.running {
			view.searchActive = true
		}
	case "q":
		return true
	case "enter":
		if view.rightQueue {
			view.rightQueue = false
			view.detailFocus = false
		} else if len(view.catalog.Apps) > 0 {
			view.detailFocus = true
			view.clearTUIQuickSearch()
		}
	case "tab":
		view.rightQueue = !view.rightQueue
		view.detailFocus = false
		view.clearTUIQuickSearch()
	case "l":
		view.detailFocus = false
		view.logOffset = 0
		view.logFocus = !view.logFocus
		view.clearTUIQuickSearch()
	case "s":
		view.page = tuiConfig
		view.detailFocus = false
		view.clearTUIQuickSearch()
		view.clearMessage()
	case "c":
		startTUIRun(parent, view, true, false, actions, events)
	case "a":
		startTUIRun(parent, view, true, true, actions, events)
	case "u":
		if len(view.catalog.Apps) > 0 {
			view.confirm = true
			view.confirmAll = false
			view.confirmChoice = tuiConfirmationPrimary
		}
	case "ctrl+u":
		if len(view.catalog.Apps) > 0 {
			view.confirm = true
			view.confirmAll = true
			view.confirmChoice = tuiConfirmationPrimary
		}
	case "ctrl+s":
		openTUIScanPage(view)
	case " ":
		toggleSelectedApplication(view, actions)
	}
	return false
}

func handleTUIAppNavigationKey(view *tuiModel, key string) bool {
	switch key {
	case "up", "k":
		if view.logFocus {
			scrollTUILogs(view, 1)
		} else {
			view.moveApplication(-1)
		}
	case "down", "j":
		if view.logFocus {
			scrollTUILogs(view, -1)
		} else {
			view.moveApplication(1)
		}
	case "pageup":
		if view.logFocus {
			scrollTUILogs(view, max(1, tuiLogViewportHeight(view)-1))
		}
	case "pagedown":
		if view.logFocus {
			scrollTUILogs(view, -max(1, tuiLogViewportHeight(view)-1))
		}
	case "home":
		if view.logFocus {
			view.logOffset = tuiMaxLogOffset(view)
		}
	case "end":
		if view.logFocus {
			view.logOffset = 0
		}
	default:
		return false
	}
	return true
}

func openTUIScanPage(view *tuiModel) {
	if view.dirty {
		view.setMessage(i18n.T("tui.scan.save_config_first"), false)
		return
	}
	view.page = tuiScan
	view.clearTUIQuickSearch()
	view.detailFocus = false
	view.logFocus = false
	view.scanLogs = nil
	view.clearMessage()
}

func (view *tuiModel) clearTUIQuickSearch() {
	view.searchActive = false
	view.searchQuery = ""
}

func handleTUIQuickSearchKey(view *tuiModel, key string) {
	if key == "esc" {
		view.clearTUIQuickSearch()
		return
	}
	if handleTUIQuickSearchNavigation(view, key) {
		return
	}
	if !isTUIQuickSearchCharacter(key) {
		return
	}
	if len(view.searchQuery) >= tuiMaxQuickSearchLength {
		return
	}
	apps, selected, scroll := tuiQuickSearchList(view)
	view.searchQuery += key
	matches := tuiQuickSearchMatches(apps, view.searchQuery)
	if len(matches) != 1 {
		return
	}
	setTUIQuickSearchSelection(view, selected, scroll, matches[0])
}

func handleTUIQuickSearchNavigation(view *tuiModel, key string) bool {
	apps, selected, scroll := tuiQuickSearchList(view)
	if len(apps) == 0 {
		return false
	}
	target := *selected
	pageSize := max(1, tuiApplicationListViewportHeight(view)-1)
	switch key {
	case "up":
		target--
	case "down":
		target++
	case "pageup":
		target -= pageSize
	case "pagedown":
		target += pageSize
	case "home":
		target = 0
	case "end":
		target = len(apps) - 1
	default:
		return false
	}
	setTUIQuickSearchSelection(view, selected, scroll, target)
	return true
}

func tuiQuickSearchList(view *tuiModel) ([]model.Application, *int, *int) {
	if view.page == tuiScan {
		return scanDisplayApps(view), &view.scanSelected, &view.scanScroll
	}
	return view.catalog.Apps, &view.selected, &view.scroll
}

func setTUIQuickSearchSelection(view *tuiModel, selected, scroll *int, target int) {
	apps, _, _ := tuiQuickSearchList(view)
	if len(apps) == 0 {
		return
	}
	*selected = max(0, min(len(apps)-1, target))
	visible := max(1, tuiApplicationListViewportHeight(view))
	if *selected < *scroll {
		*scroll = *selected
	} else if *selected >= *scroll+visible {
		*scroll = *selected - visible + 1
	}
	if view.page == tuiScan {
		view.scanDetail = 0
	}
}

func isTUIQuickSearchCharacter(key string) bool {
	if len(key) != 1 {
		return false
	}
	return (key[0] >= 'a' && key[0] <= 'z') || (key[0] >= 'A' && key[0] <= 'Z') || (key[0] >= '0' && key[0] <= '9')
}

func tuiQuickSearchMatches(apps []model.Application, query string) []int {
	if query == "" {
		return nil
	}
	foldedQuery := tuiASCIIFold(query)
	matches := make([]int, 0, 1)
	for index, app := range apps {
		if strings.HasPrefix(tuiASCIIFold(app.Name), foldedQuery) {
			matches = append(matches, index)
		}
	}
	return matches
}

func tuiASCIIFold(value string) string {
	return strings.Map(func(character rune) rune {
		if character >= 'A' && character <= 'Z' {
			return character + ('a' - 'A')
		}
		return character
	}, value)
}

func (view *tuiModel) moveApplication(delta int) {
	if view.logFocus || len(view.catalog.Apps) == 0 {
		return
	}
	view.selected = max(0, min(len(view.catalog.Apps)-1, view.selected+delta))
	view.detailOffset = 0
}

func (view *tuiModel) appendLog(line string) {
	oldMaxOffset := 0
	preserveViewport := view.logFocus && view.logOffset > 0
	if preserveViewport {
		oldMaxOffset = tuiMaxLogOffset(view)
	}
	for _, value := range strings.Split(strings.ReplaceAll(line, "\r", "\n"), "\n") {
		if value != "" {
			value = truncateTUILogLine(value)
			view.logs = append(view.logs, value)
		}
	}
	if len(view.logs) > 2000 {
		view.logs = append([]string(nil), view.logs[len(view.logs)-2000:]...)
	}
	if preserveViewport {
		newMaxOffset := tuiMaxLogOffset(view)
		view.logOffset = max(0, min(newMaxOffset, view.logOffset+newMaxOffset-oldMaxOffset))
	}
}

func (view *tuiModel) appendStructuredLog(level LogLevel, operation, subject, message string) {
	if view.operationLog != nil {
		if lines, err := view.operationLog(view.catalog, string(level), operation, subject, message); err == nil || len(lines) > 0 {
			for _, line := range lines {
				view.appendLog(line)
			}
			return
		}
	}
	for _, line := range FormatLogLines(time.Now(), level, operation, subject, message) {
		view.appendLog(line)
	}
}

func tuiResultOperation(result model.Result) string {
	switch result.Status {
	case model.StatusChecking, model.StatusCurrent, model.StatusUpdateAvailable:
		return model.OperationCheck
	case model.StatusUpdating:
		return model.OperationUpdate
	case model.StatusDownloading, model.StatusDownloaded, model.StatusDownloadedUnverified:
		return model.OperationDownload
	case model.StatusUpdated:
		return model.OperationUpdate
	}
	if result.Mode == model.ModeCheck {
		return model.OperationCheck
	}
	if result.Mode == model.ModeDownload {
		return model.OperationDownload
	}
	return model.OperationUpdate
}

func scrollTUILogs(view *tuiModel, delta int) {
	view.logOffset = max(0, min(tuiMaxLogOffset(view), view.logOffset+delta))
}

func scrollTUIDetails(view *tuiModel, delta int) {
	view.detailOffset = max(0, min(tuiMaxDetailOffset(view), view.detailOffset+delta))
}

func truncateTUILogLine(value string) string {
	if len(value) <= tuiMaxLogLineBytes {
		return strings.ToValidUTF8(value, "�")
	}
	end := tuiMaxLogLineBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return strings.ToValidUTF8(value[:end], "�") + "…"
}

func (view *tuiModel) setMessage(message string, isError bool) {
	bubble := component.MessageBubble{Message: view.message, Error: view.messageError, Until: view.messageUntil}
	bubble.Set(message, isError, time.Now(), tuiMessageDuration, tuiErrorMessageDuration)
	view.message, view.messageError, view.messageUntil = bubble.Message, bubble.Error, bubble.Until
}

func (view *tuiModel) clearMessage() {
	bubble := component.MessageBubble{Message: view.message, Error: view.messageError, Until: view.messageUntil}
	bubble.Clear()
	view.message, view.messageError, view.messageUntil = bubble.Message, bubble.Error, bubble.Until
}

func (view *tuiModel) expireMessage(now time.Time) bool {
	bubble := component.MessageBubble{Message: view.message, Error: view.messageError, Until: view.messageUntil}
	expired := bubble.Expire(now)
	view.message, view.messageError, view.messageUntil = bubble.Message, bubble.Error, bubble.Until
	return expired
}

func cloneConfig(catalog model.Config) model.Config {
	return model.CloneConfig(catalog)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
