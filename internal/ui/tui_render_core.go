package ui

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/ui/component"
	"github.com/eoctet/tendkit/pkg/i18n"
	logutil "github.com/eoctet/tendkit/pkg/logger"
)

const (
	tuiNormal      = ""
	tuiBold        = "bold"
	tuiCyan        = "cyan"
	tuiBlue        = "blue"
	tuiGreen       = "green"
	tuiYellow      = "yellow"
	tuiOrange      = "orange"
	tuiRed         = "red"
	tuiWhite       = "white"
	tuiDim         = "dim"
	tuiFocus       = "focus"
	tuiSelect      = "select"
	tuiSelectMatch = "select_match"
)

type tuiCell struct {
	value        rune
	style        string
	continuation bool
}

type tuiScreen struct {
	width  int
	height int
	cells  [][]tuiCell
	color  bool
}

func newTUIScreen(width, height int) *tuiScreen {
	cells := make([][]tuiCell, height)
	for row := range cells {
		cells[row] = make([]tuiCell, width)
		for column := range cells[row] {
			cells[row][column].value = ' '
		}
	}
	return &tuiScreen{width: width, height: height, cells: cells, color: os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"}
}

func (screen *tuiScreen) put(x, y int, value, style string) {
	if y < 0 || y >= screen.height || x >= screen.width {
		return
	}
	column := x
	for _, char := range value {
		char = safeTUIRune(char)
		charWidth := runeWidth(char)
		if column < 0 {
			column += charWidth
			continue
		}
		if column+charWidth > screen.width {
			break
		}
		screen.cells[y][column] = tuiCell{value: char, style: style}
		if charWidth == 2 {
			screen.cells[y][column+1] = tuiCell{continuation: true, style: style}
		}
		column += charWidth
	}
}

func safeTUIRune(value rune) rune {
	if value == '\t' {
		return '⇥'
	}
	if value < 0x20 || value == 0x7f || (value >= 0x80 && value <= 0x9f) {
		return '�'
	}
	return value
}

func (screen *tuiScreen) fill(x, y, width int, style string) {
	if y < 0 || y >= screen.height {
		return
	}
	for column := max(0, x); column < min(screen.width, x+width); column++ {
		screen.cells[y][column] = tuiCell{value: ' ', style: style}
	}
}

func (screen *tuiScreen) box(x, y, width, height int, title string, style string) {
	if width < 2 || height < 2 {
		return
	}
	screen.put(x, y, "┌"+strings.Repeat("─", width-2)+"┐", tuiDim)
	for row := y + 1; row < y+height-1; row++ {
		screen.put(x, row, "│", tuiDim)
		screen.put(x+width-1, row, "│", tuiDim)
	}
	screen.put(x, y+height-1, "└"+strings.Repeat("─", width-2)+"┘", tuiDim)
	if title != "" && width > 6 {
		label := " " + truncateTUI(title, width-6) + " "
		screen.put(x+2, y, label, style)
	}
}

func (screen *tuiScreen) string() string {
	var output strings.Builder
	activeStyle := ""
	for row := 0; row < screen.height; row++ {
		// Address every row explicitly. Writing exactly terminal-width cells and
		// then CRLF can trigger DEC auto-wrap in macOS terminals, advancing two
		// rows and leaving fragments from the previous frame behind.
		output.WriteString("\033[")
		output.WriteString(strconv.Itoa(row + 1))
		output.WriteString(";1H")
		for column := 0; column < screen.width; column++ {
			cell := screen.cells[row][column]
			if cell.continuation {
				continue
			}
			style := cell.style
			if style != activeStyle {
				output.WriteString("\033[0m")
				output.WriteString(screen.ansi(style))
				activeStyle = style
			}
			if cell.value == 0 {
				output.WriteByte(' ')
			} else {
				output.WriteRune(cell.value)
			}
		}
	}
	output.WriteString("\033[0m")
	return output.String()
}

func (screen *tuiScreen) ansi(style string) string {
	if !screen.color {
		if style == tuiSelectMatch {
			return "\033[1;4;7m"
		}
		if style == tuiFocus || style == tuiSelect {
			return "\033[7m"
		}
		if style == tuiBold {
			return "\033[1m"
		}
		return ""
	}
	switch style {
	case tuiBold:
		return "\033[1m"
	case tuiCyan:
		return "\033[36m"
	case tuiBlue:
		return "\033[34m"
	case tuiGreen:
		return "\033[32m"
	case tuiYellow:
		return "\033[33m"
	case tuiOrange:
		return "\033[38;5;208m"
	case tuiRed:
		return "\033[31m"
	case tuiWhite:
		return "\033[37m"
	case tuiDim:
		return "\033[90m"
	case tuiFocus:
		return "\033[30;46m"
	case tuiSelect:
		return "\033[30;46m"
	case tuiSelectMatch:
		return "\033[1;4;30;46m"
	default:
		return ""
	}
}

func renderTUI(output io.Writer, view *tuiModel) {
	width, height := view.width, view.height
	if width < 80 || height < 24 {
		screen := newTUIScreenForOutput(output, view.colorMode, max(width, 1), max(height, 1))
		screen.put(2, 1, i18n.T("tui.too_small", width, height), tuiYellow)
		_, _ = io.WriteString(output, screen.string())
		return
	}
	screen := newTUIScreenForOutput(output, view.colorMode, width, height)
	renderTUIHeader(screen, view)
	footerHeight := tuiFooterHeight(view)
	contentTop := 3
	switch view.page {
	case tuiConfig:
		configHeight := height - footerHeight - contentTop
		renderConfigPage(screen, view, contentTop, configHeight)
	case tuiScan:
		upperHeight, lowerTop, lowerHeight := stackedPageLayout(height, contentTop, footerHeight)
		renderScanPage(screen, view, contentTop, upperHeight, lowerTop, lowerHeight)
	default:
		upperHeight, lowerTop, lowerHeight := stackedPageLayout(height, contentTop, footerHeight)
		renderAppsPage(screen, view, contentTop, upperHeight)
		renderLogs(screen, view, lowerTop, lowerHeight)
	}
	renderFooter(screen, view, height-footerHeight, footerHeight)
	if view.configExitConfirm {
		renderConfigExitConfirmation(screen, view)
	}
	if view.reloadConfirm {
		renderReloadConfirmation(screen, view)
	}
	if view.confirm {
		renderConfirmation(screen, view)
	}
	if view.scanConfirm != "" {
		renderScanConfirmation(screen, view)
	}
	if view.assetSelection != nil {
		renderDownloadAssetSelection(screen, view)
	}
	_, _ = io.WriteString(output, screen.string())
}

func renderDownloadAssetSelection(screen *tuiScreen, view *tuiModel) {
	selection := view.assetSelection
	if selection == nil {
		return
	}
	const padding, gap = 3, 8
	frame := renderDialogFrame(screen, i18n.T("tui.download_file_title"), i18n.T("tui.download_file_prompt"), tuiCyan, min(len(selection.candidates), max(1, screen.height-10)))
	for index := selection.offset; index < len(selection.candidates); index++ {
		candidate := selection.candidates[index]
		row := frame.contentRow + index - selection.offset
		if row >= frame.buttonRow-1 {
			break
		}
		style := tuiNormal
		if index == selection.selected {
			style = tuiFocus
		}
		screen.put(frame.x+padding, row, truncateTUI(candidate, max(1, frame.width-padding*2)), style)
	}
	renderDialogButtons(screen, view, frame.x, frame.buttonRow, frame.width, padding, gap, i18n.T("tui.confirm"), i18n.T("tui.cancel"))
}

func newTUIScreenForOutput(output io.Writer, mode Mode, width, height int) *tuiScreen {
	screen := newTUIScreen(width, height)
	if mode.Valid() {
		screen.color = colorEnabled(output, mode)
	}
	return screen
}

func stackedPageLayout(height, contentTop, footerHeight int) (upperHeight, lowerTop, lowerHeight int) {
	lowerHeight = max(8, height*31/100)
	lowerTop = height - footerHeight - lowerHeight
	upperHeight = lowerTop - contentTop
	return upperHeight, lowerTop, lowerHeight
}

func renderTUIHeader(screen *tuiScreen, view *tuiModel) {
	title := i18n.T("tui.title")
	switch view.page {
	case tuiConfig:
		title += " / " + i18n.T("tui.config")
	case tuiScan:
		title += " / " + i18n.T("tui.scan")
	}
	screen.put(2, 1, title, tuiCyan)
	badge := tuiPageBadge(view)
	occupied := min(len(view.queue), view.catalog.Settings.Workers)
	available := max(0, view.catalog.Settings.Workers-occupied)
	workers := "[ " + i18n.T("tui.workers_badge", available, view.catalog.Settings.Workers) + " ]"
	language := "[ " + strings.ToUpper(string(i18n.Current())) + " ]"
	x := screen.width - DisplayWidth(badge) - DisplayWidth(workers) - DisplayWidth(language) - 8
	badgeStart := x
	screen.put(x, 1, badge, tuiCyan)
	x += DisplayWidth(badge) + 2
	screen.put(x, 1, workers, tuiNormal)
	x += DisplayWidth(workers) + 2
	screen.put(x, 1, language, tuiDim)
	renderHeaderMessage(screen, view, title, badgeStart)
}

func tuiPageBadge(view *tuiModel) string {
	if view.page != tuiApps {
		return "[ " + strings.ToUpper(pageName(view.page)) + " ]"
	}
	updates := 0
	for _, app := range view.catalog.Apps {
		if app.StatusManaged.HasUpdate {
			updates++
		}
	}
	if updates == 0 {
		return "[ " + i18n.T("tui.apps_badge_total", len(view.catalog.Apps)) + " ]"
	}
	return "[ " + i18n.T("tui.apps_badge_updates", updates, len(view.catalog.Apps)) + " ]"
}

func renderHeaderMessage(screen *tuiScreen, view *tuiModel, title string, badgeStart int) {
	if view.message == "" {
		return
	}
	left := 2 + DisplayWidth(title) + 2
	right := badgeStart - 2
	available := right - left
	if available <= 0 {
		return
	}
	message := truncateTUI(view.message, available)
	messageWidth := DisplayWidth(message)
	x := (screen.width - messageWidth) / 2
	x = max(left, min(x, right-messageWidth))
	style := tuiGreen
	if view.messageError {
		style = tuiRed
	} else if view.dirty {
		style = tuiYellow
	}
	screen.put(x, 1, message, style)
}

func pageName(page tuiPage) string {
	if page == tuiConfig {
		return i18n.T("tui.config")
	}
	if page == tuiScan {
		return i18n.T("tui.scan")
	}
	return i18n.T("tui.apps")
}

func renderAppsPage(screen *tuiScreen, view *tuiModel, top, height int) {
	if len(view.catalog.Apps) == 0 {
		screen.box(0, top, screen.width, height, tuiApplicationListTitle(view), tuiCyan)
		renderEmptyApplicationGuide(screen, 1, top+1, screen.width-2, height-2)
		return
	}
	leftWidth := screen.width * 67 / 100
	rightWidth := screen.width - leftWidth
	screen.box(0, top, leftWidth, height, tuiApplicationListTitle(view), tuiCyan)
	screen.box(leftWidth, top, rightWidth, height, rightTitle(view), tuiCyan)
	renderApplicationTable(screen, view, 1, top+1, leftWidth-2, height-2)
	if view.rightQueue {
		renderQueue(screen, view, leftWidth+1, top+1, rightWidth-2, height-2)
	} else {
		renderApplicationDetails(screen, view, leftWidth+1, top+1, rightWidth-2, height-2)
	}
}

type emptyApplicationAction struct {
	key   string
	label string
}

func renderEmptyApplicationGuide(screen *tuiScreen, x, y, width, height int) {
	if width < 20 || height < 3 {
		return
	}
	contentWidth := min(72, max(1, width-4))
	contentX := x + max(2, (width-contentWidth)/2)
	body := wrapTUI(i18n.T("tui.empty_apps_body"), contentWidth)
	actions := []emptyApplicationAction{
		{key: "CTRL+S", label: i18n.T("tui.empty_apps_scan")},
		{key: "S", label: i18n.T("tui.empty_apps_settings")},
		{key: "L", label: i18n.T("tui.empty_apps_logs")},
		{key: "Q", label: i18n.T("tui.empty_apps_quit")},
	}
	expanded := height >= 9
	totalRows := 1 + len(body) + 2
	if expanded {
		totalRows += 3
	}
	row := y + max(0, (height-totalRows)/2)
	screen.put(contentX, row, truncateTUI(i18n.T("tui.no_apps"), contentWidth), tuiBold)
	row++
	if expanded {
		row++
	}
	for _, line := range body {
		if row >= y+height {
			return
		}
		screen.put(contentX, row, line, tuiNormal)
		row++
	}
	if expanded {
		row++
		screen.put(contentX, row, i18n.T("tui.empty_apps_shortcuts"), tuiDim)
		row++
	}
	columnWidth := max(1, contentWidth/2)
	for index, action := range actions {
		actionRow := row + index/2
		if actionRow >= y+height {
			break
		}
		actionX := contentX + (index%2)*columnWidth
		key := "[ " + action.key + " ]"
		screen.put(actionX, actionRow, key, tuiCyan)
		screen.put(actionX+11, actionRow, truncateTUI(action.label, max(1, columnWidth-11)), tuiNormal)
	}
}

func tuiApplicationListTitle(view *tuiModel) string {
	title := i18n.T("tui.app_list")
	if view.searchActive {
		if view.searchQuery == "" {
			title += " [" + i18n.T("tui.app_list_search") + "]"
		} else {
			title = i18n.T("tui.app_list_search_query", title, i18n.T("tui.app_list_search"), view.searchQuery)
		}
	}
	return title
}

func rightTitle(view *tuiModel) string {
	if view.rightQueue {
		return i18n.T("tui.queue")
	}
	if view.detailFocus {
		maximum := tuiMaxDetailOffset(view)
		view.detailOffset = max(0, min(maximum, view.detailOffset))
		return i18n.T("tui.details_focused", view.detailOffset+1, maximum+1)
	}
	return i18n.T("tui.details")
}

func renderApplicationTable(screen *tuiScreen, view *tuiModel, x, y, width, height int) {
	if width < 20 || height < 3 {
		return
	}
	widths := applicationTableColumnWidths(width)
	numberWidth, nameWidth, modeWidth := widths[0], widths[1], widths[2]
	currentWidth, latestWidth := widths[3], widths[4]
	columnX := []int{x + 1, x + 1 + numberWidth, x + 1 + numberWidth + nameWidth, x + 1 + numberWidth + nameWidth + modeWidth, x + 1 + numberWidth + nameWidth + modeWidth + currentWidth, x + 1 + numberWidth + nameWidth + modeWidth + currentWidth + latestWidth}
	headings := []string{i18n.T("label.number"), i18n.T("label.name"), i18n.T("label.update_mode"), i18n.T("label.current_version"), i18n.T("label.latest_version"), i18n.T("label.status")}
	for index := range headings {
		screen.put(columnX[index], y+1, truncateTUI(headings[index], widths[index]-1), tuiBold)
	}
	screen.put(x+1, y+2, strings.Repeat("─", width-2), tuiDim)
	visible := max(1, height-4)
	if view.selected < view.scroll {
		view.scroll = view.selected
	}
	if view.selected >= view.scroll+visible {
		view.scroll = view.selected - visible + 1
	}
	end := min(len(view.catalog.Apps), view.scroll+visible)
	for index := view.scroll; index < end; index++ {
		row := y + 3 + index - view.scroll
		app := view.catalog.Apps[index]
		state := app.StatusManaged
		style := tuiNormal
		if index == view.selected {
			style = tuiSelect
			screen.fill(x, row, width, tuiSelect)
		}
		status := state.UpdateStatus
		if queued, exists := view.queue[app.ID]; exists {
			status = queued.Status
		}
		label := StatusLabel(status)
		values := []string{strconv.Itoa(index + 1), app.Name, UpdateModeLabel(app.UpdateMode), displayValue(state.CurrentVersion), displayValue(state.LatestVersion), label}
		for column := range values {
			cellStyle := style
			if column == 1 && tuiApplicationMatchesQuickSearch(app.Name, view.searchQuery) {
				cellStyle = tuiBlue
				if style == tuiSelect {
					cellStyle = tuiSelectMatch
				}
			}
			if style != tuiSelect && column == len(values)-1 {
				cellStyle = tuiStatusStyle(status)
			}
			screen.put(columnX[column], row, truncateTUI(values[column], widths[column]-1), cellStyle)
		}
	}
}

func applicationTableColumnWidths(width int) []int {
	// Each width includes one trailing gutter. At the common 120-column
	// terminal size the application panel has 76 usable cells; reserve enough room for
	// complete operational headings instead of assigning all surplus to Name.
	widths := []int{5, 14, 14, 16, 15, 12}
	minimums := []int{4, 8, 9, 9, 10, 9}
	caps := []int{5, 28, 16, 22, 22, 14}
	available := max(1, width-2)
	total := sumInts(widths)
	for total > available {
		changed := false
		for _, index := range []int{1, 3, 4, 2, 5, 0} {
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
	for total < available {
		changed := false
		for _, index := range []int{3, 4, 2, 5, 1} {
			if total >= available {
				break
			}
			if widths[index] < caps[index] {
				widths[index]++
				total++
				changed = true
			}
		}
		if !changed {
			widths[1] += available - total
			break
		}
	}
	return widths
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

type tuiDetailLine struct {
	label      string
	value      string
	valueStyle string
	fullWidth  bool
}

type tuiDetailField struct {
	label     string
	value     string
	style     string
	separator bool
}

func renderApplicationDetails(screen *tuiScreen, view *tuiModel, x, y, width, height int) {
	if len(view.catalog.Apps) == 0 {
		screen.put(x+1, y+2, i18n.T("tui.no_apps"), tuiDim)
		return
	}
	lines, labelWidth := applicationDetailLines(view, width)
	available := max(1, height-2)
	view.detailOffset = max(0, min(max(0, len(lines)-available), view.detailOffset))
	end := min(len(lines), view.detailOffset+available)
	valueX := x + labelWidth + 2
	for index, line := range lines[view.detailOffset:end] {
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

func applicationDetailLines(view *tuiModel, width int) ([]tuiDetailLine, int) {
	if len(view.catalog.Apps) == 0 {
		return nil, 1
	}
	app := view.catalog.Apps[view.selected]
	state := app.StatusManaged
	return applicationDetailLinesFor(app, state, width)
}

func applicationDetailLinesFor(app model.Application, state model.ManagedStatus, width int) ([]tuiDetailLine, int) {
	return buildApplicationDetailLines(baseApplicationDetailFields(app, state), state, width)
}

func baseApplicationDetailFields(app model.Application, state model.ManagedStatus) []tuiDetailField {
	status := StatusLabel(state.UpdateStatus)
	fields := []tuiDetailField{
		{label: "ID", value: app.ID, style: tuiNormal},
		{label: i18n.T("label.name"), value: app.Name, style: tuiNormal},
		{label: i18n.T("label.type"), value: ApplicationTypeLabel(app.Type), style: tuiNormal},
		{label: i18n.T("tui.config.app_description"), value: displayValue(app.Description), style: tuiNormal},
		{label: "URL", value: displayValue(app.URL), style: tuiNormal},
		{label: i18n.T("label.provider"), value: string(app.Provider.Type), style: tuiNormal},
	}
	if strings.TrimSpace(app.Package) != "" {
		fields = append(fields, tuiDetailField{label: i18n.T("tui.config.app_package"), value: app.Package, style: tuiNormal})
	}
	fields = append(fields,
		tuiDetailField{label: i18n.T("label.status"), value: status, style: tuiStatusStyle(state.UpdateStatus)},
		tuiDetailField{label: i18n.T("label.enabled"), value: boolText(app.Enabled), style: tuiNormal},
		tuiDetailField{label: i18n.T("label.update_mode"), value: UpdateModeLabel(app.UpdateMode), style: tuiNormal},
		tuiDetailField{label: i18n.T("label.current_version"), value: displayValue(state.CurrentVersion), style: tuiNormal},
		tuiDetailField{label: i18n.T("label.latest_version"), value: displayValue(state.LatestVersion), style: tuiNormal},
		tuiDetailField{label: i18n.T("label.install_path"), value: app.InstallPath, style: tuiNormal},
	)
	return fields
}

func buildApplicationDetailLines(fields []tuiDetailField, state model.ManagedStatus, width int) ([]tuiDetailLine, int) {
	labelWidth := 1
	for _, field := range fields {
		if field.separator {
			continue
		}
		labelWidth = max(labelWidth, DisplayWidth(field.label))
	}
	labelWidth = min(labelWidth, max(8, width/2))
	valueWidth := max(1, width-labelWidth-3)
	lines := make([]tuiDetailLine, 0, len(fields)+8)
	for _, field := range fields {
		if field.separator {
			lines = append(lines, tuiDetailLine{value: strings.Repeat("─", max(1, width-2)), valueStyle: tuiDim, fullWidth: true})
			continue
		}
		labels := wrapTUI(field.label, labelWidth)
		values := wrapTUI(field.value, valueWidth)
		lineCount := max(len(labels), len(values))
		for line := 0; line < lineCount; line++ {
			item := tuiDetailLine{valueStyle: field.style}
			if line < len(labels) {
				item.label = labels[line]
			}
			if line < len(values) {
				item.value = values[line]
			}
			lines = append(lines, item)
		}
	}
	if state.Error != "" {
		lines = append(lines, tuiDetailLine{})
		lines = append(lines, tuiDetailLine{value: i18n.T("label.error"), valueStyle: tuiRed, fullWidth: true})
		for _, line := range wrapTUI(i18n.Localize(state.Error), max(1, width-3)) {
			lines = append(lines, tuiDetailLine{value: line, valueStyle: tuiRed, fullWidth: true})
		}
	}
	return lines, labelWidth
}

func renderConfigPage(screen *tuiScreen, view *tuiModel, top, height int) {
	leftWidth := screen.width * 68 / 100
	rightWidth := screen.width - leftWidth
	screen.box(0, top, leftWidth, height, i18n.T("tui.config_all"), tuiCyan)
	rows := configRows(&view.working)
	if len(rows) == 0 {
		screen.box(leftWidth, top, rightWidth, height, i18n.T("tui.config_help"), tuiCyan)
		return
	}
	view.configIndex = max(0, min(len(rows)-1, view.configIndex))
	selected := rows[view.configIndex]
	rightTitle := i18n.T("tui.config_help")
	if selected.rowType == configRowApplication {
		rightTitle = i18n.T("tui.config.app_panel", selected.label)
	}
	screen.box(leftWidth, top, rightWidth, height, rightTitle, tuiCyan)
	visual := configVisualLines(rows, &view.working, &view.catalog)
	selectedVisual := 0
	for index, line := range visual {
		if line.rowIndex == view.configIndex {
			selectedVisual = index
			break
		}
	}
	visible := max(1, height-3)
	if selectedVisual < visible {
		view.configScroll = 0
	} else if selectedVisual < view.configScroll {
		view.configScroll = selectedVisual
	}
	if selectedVisual >= view.configScroll+visible {
		view.configScroll = selectedVisual - visible + 1
	}
	view.configScroll = max(0, min(view.configScroll, max(0, len(visual)-visible)))
	end := min(len(visual), view.configScroll+visible)
	for visualIndex := view.configScroll; visualIndex < end; visualIndex++ {
		line := visual[visualIndex]
		rowY := top + 2 + visualIndex - view.configScroll
		if line.rowIndex < 0 {
			style := tuiDim
			if line.modified {
				style = tuiGreen
			}
			screen.put(2, rowY, configSectionDivider(line.title, max(1, leftWidth-4)), style)
			continue
		}
		index := line.rowIndex
		style := tuiNormal
		if index == view.configIndex {
			style = tuiSelect
			screen.fill(1, rowY, leftWidth-2, tuiSelect)
		} else if line.modified {
			style = tuiGreen
		}
		screen.put(2, rowY, truncateTUI(rows[index].label, 27), style)
		value := rows[index].value
		if index == view.configIndex && view.editing && !view.configAppFocus {
			value = editValueViewport(view.editValue, view.editCursor, max(1, leftWidth-32))
		}
		screen.put(30, rowY, truncateTUI(value, max(1, leftWidth-32)), style)
	}
	if selected.rowType == configRowApplication {
		renderApplicationConfigPanel(screen, view, selected, leftWidth, top, rightWidth, height)
		return
	}
	heading := selected.label
	if view.editing {
		heading += "  " + i18n.T("tui.edit_position", view.editCursor, utf8.RuneCountInString(view.editValue))
	}
	screen.put(leftWidth+2, top+2, truncateTUI(heading, rightWidth-4), tuiBold)
	rowY := top + 4
	for _, line := range wrapTUI(configHelp(selected), max(1, rightWidth-4)) {
		if rowY >= top+height-2 {
			return
		}
		screen.put(leftWidth+2, rowY, line, tuiNormal)
		rowY++
	}
	rowY++
	for _, line := range wrapTUI(selected.value, max(1, rightWidth-4)) {
		if rowY >= top+height-2 {
			return
		}
		screen.put(leftWidth+2, rowY, line, tuiDim)
		rowY++
	}
	if selected.min != 0 || selected.max != 0 {
		rowY++
		if rowY < top+height-1 {
			screen.put(leftWidth+2, rowY, i18n.T("tui.range", selected.min, selected.max), tuiDim)
		}
	}
}

type configVisualLine struct {
	rowIndex int
	title    string
	modified bool
}

func configVisualLines(rows []configRow, working, baseline *model.Config) []configVisualLine {
	modifiedSections := make(map[configSection]bool)
	modifiedRows := make([]bool, len(rows))
	for index, row := range rows {
		modifiedRows[index] = configRowModified(row, working, baseline)
		modifiedSections[row.section] = modifiedSections[row.section] || modifiedRows[index]
	}
	lines := make([]configVisualLine, 0, len(rows)+6)
	sections := []configSection{
		configSectionBasic, configSectionHTTP, configSectionDownload,
		configSectionProvider, configSectionScan, configSectionApplication,
	}
	for _, section := range sections {
		title := configSectionTitle(section, len(working.Apps))
		if modifiedSections[section] {
			title += " " + i18n.T("tui.config.modified")
		}
		lines = append(lines, configVisualLine{rowIndex: -1, title: title, modified: modifiedSections[section]})
		for index, row := range rows {
			if row.section == section {
				lines = append(lines, configVisualLine{rowIndex: index, modified: modifiedRows[index]})
			}
		}
	}
	return lines
}

func configSectionDivider(title string, width int) string {
	prefix := "── " + title + " "
	if DisplayWidth(prefix) >= width {
		return truncateTUI(prefix, width)
	}
	return prefix + strings.Repeat("─", width-DisplayWidth(prefix))
}

func renderApplicationConfigPanel(screen *tuiScreen, view *tuiModel, selected configRow, x, y, width, height int) {
	fields := applicationConfigRows(&view.working, selected.appID)
	if len(fields) == 0 {
		return
	}
	view.appFieldIndex = max(0, min(len(fields)-1, view.appFieldIndex))
	listHeight := applicationConfigListHeight(height)
	if view.appFieldIndex < view.appFieldScroll {
		view.appFieldScroll = view.appFieldIndex
	}
	if view.appFieldIndex >= view.appFieldScroll+listHeight {
		view.appFieldScroll = view.appFieldIndex - listHeight + 1
	}
	end := min(len(fields), view.appFieldScroll+listHeight)
	labelWidth := min(17, max(8, width/3))
	valueX := x + 2 + labelWidth
	valueWidth := max(1, width-labelWidth-4)
	baselineFields := applicationConfigRows(&view.catalog, selected.appID)
	baselineValues := make(map[string]string, len(baselineFields))
	for _, field := range baselineFields {
		baselineValues[field.key] = field.value
	}
	for index := view.appFieldScroll; index < end; index++ {
		rowY := y + 2 + index - view.appFieldScroll
		labelStyle := tuiDim
		valueStyle := tuiNormal
		if fields[index].rowType == configRowReadOnly {
			valueStyle = tuiDim
		}
		if baseline, exists := baselineValues[fields[index].key]; !exists || baseline != fields[index].value {
			labelStyle = tuiGreen
			valueStyle = tuiGreen
		}
		if index == view.appFieldIndex {
			labelStyle = tuiSelect
			valueStyle = tuiSelect
			screen.fill(x+1, rowY, width-2, tuiSelect)
		}
		screen.put(x+2, rowY, truncateTUI(fields[index].label, labelWidth-1), labelStyle)
		value := displayValue(fields[index].value)
		if view.editing && index == view.appFieldIndex {
			value = editValueViewport(view.editValue, view.editCursor, valueWidth)
		}
		screen.put(valueX, rowY, truncateTUI(value, valueWidth), valueStyle)
	}

	previewY := y + 2 + listHeight
	if previewY >= y+height-1 {
		return
	}
	screen.put(x+1, previewY, strings.Repeat("─", max(1, width-2)), tuiDim)
	previewY++
	field := fields[view.appFieldIndex]
	heading := field.label
	if field.rowType == configRowReadOnly {
		heading += " · " + i18n.T("tui.readonly")
	}
	if view.editing {
		heading += " · " + i18n.T("tui.edit_position", view.editCursor, utf8.RuneCountInString(view.editValue))
	}
	screen.put(x+2, previewY, truncateTUI(heading, width-4), tuiBold)
	previewY++
	preview := displayValue(field.value)
	if view.editing {
		preview = view.editValue
	}
	for _, line := range wrapTUI(preview, max(1, width-4)) {
		if previewY >= y+height-1 {
			break
		}
		screen.put(x+2, previewY, line, tuiDim)
		previewY++
	}
}

func applicationConfigListHeight(height int) int {
	contentHeight := max(1, height-3)
	previewHeight := max(3, contentHeight*20/100)
	previewHeight = min(previewHeight, max(1, contentHeight-1))
	return max(1, contentHeight-previewHeight)
}

func configHelp(row configRow) string {
	switch row.key {
	case "workers":
		return i18n.T("tui.help.workers")
	case "http_concurrency":
		return i18n.T("tui.help.http_concurrency")
	case "timeout":
		return i18n.T("tui.help.timeout")
	case "scan_exclude":
		return i18n.T("tui.help.scan_exclude")
	case "scan_bundle_id":
		return i18n.T("tui.help.scan_bundle_id")
	default:
		return i18n.T("tui.help.default")
	}
}

func renderLogs(screen *tuiScreen, view *tuiModel, top, height int) {
	available := max(1, height-2)
	lines := wrappedTUILogs(view)
	empty := len(lines) == 0
	if empty {
		view.logOffset = 0
	}
	view.logOffset = min(view.logOffset, max(0, len(lines)-available))

	title := i18n.T("tui.live_logs")
	if view.logFocus {
		position := i18n.T("tui.log_following")
		if view.logOffset > 0 {
			position = i18n.T("tui.log_offset", view.logOffset)
		}
		title += "  [" + i18n.T("tui.focused") + " · " + position + "]"
	}
	screen.box(0, top, screen.width, height, title, tuiCyan)
	if empty {
		renderEmptyLogsBanner(screen, top, available)
		return
	}
	end := max(0, len(lines)-view.logOffset)
	start := max(0, end-available)
	for index, line := range lines[start:end] {
		style := tuiNormal
		switch LogLevelFromLine(line) {
		case LogError:
			style = tuiRed
		case LogWarn:
			style = tuiYellow
		case LogDebug:
			style = tuiDim
		}
		screen.put(2, top+1+index, line, style)
	}
}

func renderEmptyLogsBanner(screen *tuiScreen, top, available int) {
	lines := strings.Split(i18n.Banner(), "\n")
	if len(lines) > available {
		lines = lines[:available]
	}
	canvasWidth := 0
	for _, line := range lines {
		canvasWidth = max(canvasWidth, DisplayWidth(line))
	}
	canvasWidth = min(canvasWidth, max(1, screen.width-4))
	x := max(2, (screen.width-canvasWidth)/2)
	y := top + 1 + max(0, (available-len(lines))/2)
	for index, line := range lines {
		line = truncateTUI(line, canvasWidth)
		screen.put(x, y+index, line, tuiCyan)
	}
}

func renderFooter(screen *tuiScreen, view *tuiModel, top, height int) {
	screen.box(0, top, screen.width, height, "", tuiDim)
	for index, line := range tuiCurrentKeymap(view).FooterLines(screen.width) {
		if index >= height-2 {
			break
		}
		screen.put(2, top+1+index, line, tuiNormal)
	}
}

func editValueViewport(value string, cursor, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	cursor = max(0, min(len(runes), cursor))
	for index, value := range runes {
		switch value {
		case '\n':
			runes[index] = '↵'
		case '\r':
			runes[index] = '␍'
		case '\t':
			runes[index] = '⇥'
		}
	}
	if width == 1 {
		return "│"
	}
	if width == 2 {
		if cursor > 0 {
			return "‹│"
		}
		if len(runes) > 0 {
			return "│›"
		}
		return "│"
	}
	start, end := cursor, cursor
	leftWidth, rightWidth := 0, 0
	contentBudget := max(0, width-1) // reserve one cell for the insertion caret
	leftTarget := contentBudget / 2
	for start > 0 {
		charWidth := runeWidth(runes[start-1])
		if leftWidth+charWidth > leftTarget {
			break
		}
		start--
		leftWidth += charWidth
	}
	for end < len(runes) {
		charWidth := runeWidth(runes[end])
		if leftWidth+rightWidth+charWidth > contentBudget {
			break
		}
		end++
		rightWidth += charWidth
	}
	for start > 0 {
		charWidth := runeWidth(runes[start-1])
		if leftWidth+rightWidth+charWidth > contentBudget {
			break
		}
		start--
		leftWidth += charWidth
	}

	leftHidden, rightHidden := start > 0, end < len(runes)
	used := leftWidth + 1 + rightWidth
	if leftHidden {
		used++
	}
	if rightHidden {
		used++
	}
	for used > width && (start < cursor || end > cursor) {
		if start < cursor && (leftWidth >= rightWidth || end == cursor) {
			leftWidth -= runeWidth(runes[start])
			start++
		} else if end > cursor {
			end--
			rightWidth -= runeWidth(runes[end])
		}
		used = leftWidth + 1 + rightWidth
		if start > 0 {
			used++
		}
		if end < len(runes) {
			used++
		}
	}

	var result strings.Builder
	if start > 0 {
		result.WriteRune('‹')
	}
	result.WriteString(string(runes[start:cursor]))
	result.WriteRune('│')
	result.WriteString(string(runes[cursor:end]))
	if end < len(runes) {
		result.WriteRune('›')
	}
	return result.String()
}

func wrappedTUILogs(view *tuiModel) []string {
	lines := make([]string, 0, len(view.logs))
	for _, logLine := range view.logs {
		lines = append(lines, wrapTUI(logLine, max(1, view.width-4))...)
	}
	return lines
}

func tuiLogViewportHeight(view *tuiModel) int {
	logHeight := max(8, view.height*31/100)
	return max(1, logHeight-2)
}

func tuiDetailViewportHeight(view *tuiModel) int {
	footerHeight := tuiFooterHeight(view)
	contentTop := 3
	logHeight := max(8, view.height*31/100)
	logTop := view.height - footerHeight - logHeight
	upperHeight := logTop - contentTop
	return max(1, upperHeight-4)
}

func tuiApplicationListViewportHeight(view *tuiModel) int {
	footerHeight := tuiFooterHeight(view)
	if view.page == tuiScan {
		upperHeight, _, _ := stackedPageLayout(view.height, 3, footerHeight)
		return max(1, upperHeight-4)
	}
	_, _, lowerHeight := stackedPageLayout(view.height, 3, footerHeight)
	upperHeight := view.height - 3 - lowerHeight - footerHeight
	return max(1, upperHeight-4)
}

func tuiApplicationMatchesQuickSearch(name, query string) bool {
	return query != "" && strings.HasPrefix(tuiASCIIFold(name), tuiASCIIFold(query))
}

func tuiMaxDetailOffset(view *tuiModel) int {
	if len(view.catalog.Apps) == 0 {
		return 0
	}
	leftWidth := view.width * 67 / 100
	rightWidth := view.width - leftWidth
	lines, _ := applicationDetailLines(view, max(1, rightWidth-2))
	return max(0, len(lines)-tuiDetailViewportHeight(view))
}

func tuiMaxLogOffset(view *tuiModel) int {
	return max(0, len(wrappedTUILogs(view))-tuiLogViewportHeight(view))
}

func renderConfirmation(screen *tuiScreen, view *tuiModel) {
	if len(view.catalog.Apps) == 0 {
		return
	}
	app := view.catalog.Apps[view.selected]
	prompt := i18n.T("tui.confirm_prompt", app.Name)
	if view.confirmAll {
		prompt = i18n.T("tui.confirm_all_prompt")
	}
	primary, secondary := tuiConfirmationLabels(view)
	renderConfirmationDialog(screen, view, i18n.T("tui.confirm_update"), prompt, tuiCyan, i18n.T(primary), i18n.T(secondary))
}

func renderConfigExitConfirmation(screen *tuiScreen, view *tuiModel) {
	primary, secondary := tuiConfirmationLabels(view)
	renderConfirmationDialog(
		screen,
		view,
		i18n.T("tui.config.unsaved_title"),
		i18n.T("tui.config.unsaved_prompt"),
		tuiOrange,
		i18n.T(primary),
		i18n.T(secondary),
	)
}

func renderReloadConfirmation(screen *tuiScreen, view *tuiModel) {
	primary, secondary := tuiConfirmationLabels(view)
	renderConfirmationDialog(screen, view, i18n.T("tui.reload_title"), i18n.T("tui.reload_prompt"), tuiOrange, i18n.T(primary), i18n.T(secondary))
}

func renderConfirmationDialog(screen *tuiScreen, view *tuiModel, title, prompt, titleStyle, primary, secondary string) {
	frame := renderDialogFrame(screen, title, prompt, titleStyle, 0)
	renderDialogButtons(screen, view, frame.x, frame.buttonRow, frame.width, 3, 8, primary, secondary)
}

type dialogFrame struct{ x, y, width, height, contentRow, buttonRow int }

func renderDialogFrame(screen *tuiScreen, title, prompt, titleStyle string, bodyRows int) dialogFrame {
	const preferredWidth, padding = 68, 3
	width := min(preferredWidth, screen.width-8)
	lines := wrapTUI(prompt, max(1, width-padding*2))
	maxLines := max(1, screen.height-10-bodyRows)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	height := min(screen.height-4, len(lines)+bodyRows+6)
	x, y := (screen.width-width)/2, (screen.height-height)/2
	for row := y; row < y+height; row++ {
		screen.fill(x, row, width, tuiNormal)
	}
	screen.box(x, y, width, height, title, titleStyle)
	row := y + 2
	for _, line := range lines {
		screen.put(x+padding, row, line, tuiNormal)
		row++
	}
	return dialogFrame{x: x, y: y, width: width, height: height, contentRow: row, buttonRow: y + height - 3}
}

func renderDialogButtons(screen *tuiScreen, view *tuiModel, x, row, width, padding, gap int, primary, secondary string) {
	primaryStyle, secondaryStyle := tuiConfirmationStyles(view)
	primaryButton := "[ ENTER " + primary + " ]"
	secondaryButton := "[ ESC " + secondary + " ]"
	primaryX := x + padding
	secondaryX := primaryX + DisplayWidth(primaryButton) + gap
	if secondaryX+DisplayWidth(secondaryButton) > x+width-padding {
		secondaryX = x + width - padding - DisplayWidth(secondaryButton)
	}
	screen.put(primaryX, row, primaryButton, primaryStyle)
	screen.put(secondaryX, row, secondaryButton, secondaryStyle)
}

func tuiConfirmationStyles(view *tuiModel) (primary, secondary string) {
	if view.confirmChoice == tuiConfirmationSecondary {
		return tuiNormal, tuiFocus
	}
	return tuiFocus, tuiNormal
}

func tuiStatusStyle(status string) string {
	switch status {
	case model.StatusUpdated, model.StatusUpdating, model.StatusDownloaded, model.StatusUpdateAvailable, model.StatusDownloading, model.StatusChecking:
		return tuiGreen
	case model.StatusDownloadedUnverified:
		return tuiYellow
	case model.StatusFailed:
		return tuiRed
	case model.StatusWaiting, model.StatusSkipped, model.StatusMissing, model.StatusUnchecked:
		return tuiDim
	default:
		return tuiWhite
	}
}

func truncateTUI(value string, width int) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " ")
	if width <= 0 {
		return ""
	}
	if DisplayWidth(value) <= width {
		return value
	}
	return strings.TrimRight(Column(value, width), " ")
}

func wrapTUI(value string, width int) []string {
	if width <= 0 {
		return nil
	}
	if value == "" {
		return []string{""}
	}
	result := make([]string, 0, 2)
	for _, source := range strings.Split(value, "\n") {
		if source == "" {
			result = append(result, "")
			continue
		}
		var line strings.Builder
		lineWidth := 0
		for _, char := range source {
			charWidth := runeWidth(char)
			if lineWidth+charWidth > width && line.Len() > 0 {
				result = append(result, line.String())
				line.Reset()
				lineWidth = 0
			}
			line.WriteRune(char)
			lineWidth += charWidth
		}
		if line.Len() > 0 {
			result = append(result, line.String())
		}
	}
	return result
}

// Mode controls whether terminal color escape sequences are emitted.
type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeAlways Mode = "always"
	ModeNever  Mode = "never"
)

// Valid reports whether the color mode is supported.
func (m Mode) Valid() bool { return m == ModeAuto || m == ModeAlways || m == ModeNever }

// StatusLabel returns the localized label for a status code.
func StatusLabel(status string) string {
	switch status {
	case "":
		return "-"
	case model.StatusCurrent:
		return i18n.T("status.current")
	case model.StatusUpdateAvailable:
		return i18n.T("status.update_available")
	case model.StatusUpdated:
		return i18n.T("status.updated")
	case model.StatusUpdating:
		return i18n.T("status.updating")
	case model.StatusDownloaded:
		return i18n.T("status.downloaded")
	case model.StatusDownloadedUnverified:
		return i18n.T("status.downloaded_unverified")
	case model.StatusDownloading:
		return i18n.T("status.downloading")
	case model.StatusChecking:
		return i18n.T("status.checking")
	case model.StatusWaiting:
		return i18n.T("status.waiting")
	case model.StatusFailed:
		return i18n.T("status.failed")
	case model.StatusSkipped:
		return i18n.T("status.skipped")
	case model.StatusMissing:
		return i18n.T("status.missing")
	case model.StatusUnchecked:
		return i18n.T("status.unchecked")
	default:
		return strings.ToUpper(status)
	}
}

// UpdateModeLabel returns the localized label for an update mode.
func UpdateModeLabel(mode model.UpdateMode) string {
	switch mode {
	case model.ModeAuto:
		return i18n.T("mode.auto")
	case model.ModeDownload:
		return i18n.T("mode.download")
	case model.ModeInstall:
		return i18n.T("mode.install")
	case model.ModeCheck:
		return i18n.T("mode.check")
	default:
		return string(mode)
	}
}

// ApplicationTypeLabel returns the localized label for an application type.
func ApplicationTypeLabel(applicationType string) string {
	switch applicationType {
	case model.ApplicationTypeCLI:
		return i18n.T("type.cli")
	case model.ApplicationTypeBundle:
		return i18n.T("type.application")
	case model.ApplicationTypePackage:
		return i18n.T("type.package")
	case model.ApplicationTypeSDK:
		return i18n.T("type.sdk")
	default:
		return applicationType
	}
}

func displayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func colorEnabled(writer io.Writer, mode Mode) bool {
	if mode == ModeAlways {
		return true
	}
	if mode == ModeNever || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Column truncates or pads text to a terminal display width.
func Column(value string, width int) string {
	if DisplayWidth(value) <= width {
		return value + strings.Repeat(" ", width-DisplayWidth(value))
	}
	var kept []rune
	used := 0
	for _, char := range value {
		charWidth := runeWidth(char)
		if used+charWidth > width-1 {
			break
		}
		kept = append(kept, char)
		used += charWidth
	}
	return string(kept) + "…" + strings.Repeat(" ", max(0, width-used-1))
}

// DisplayWidth returns the terminal cell width of a string.
func DisplayWidth(value string) int {
	width := 0
	for _, char := range value {
		width += runeWidth(char)
	}
	return width
}

func runeWidth(char rune) int {
	if unicode.In(char, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana) ||
		(char >= 0xFF01 && char <= 0xFF60) || (char >= 0xFFE0 && char <= 0xFFE6) {
		return 2
	}
	return 1
}

// LogLevel is the stable, language-independent severity used by terminal logs.
type LogLevel = component.LogLevel

const (
	LogDebug = component.LogDebug
	LogInfo  = component.LogInfo
	LogWarn  = component.LogWarn
	LogError = component.LogError
)

// FormatLogLines renders one physical record per message line so copied TUI
// output remains sortable and every continuation retains its context.
func FormatLogLines(at time.Time, level LogLevel, operation, subject, message string) []string {
	return logutil.FormatOperationLines(at, string(level), operation, subject, message)
}

// LogLevelFromLine returns the severity encoded by FormatLogLines.
func LogLevelFromLine(line string) LogLevel {
	return component.LevelFromLine(line)
}
