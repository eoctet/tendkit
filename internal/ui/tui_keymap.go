package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/eoctet/tendkit/pkg/i18n"
)

// tuiKeyBinding describes one visible operation and the keys that can trigger it.
type tuiKeyBinding struct {
	keys      []string
	label     string
	labelFunc func() string
	text      bool
	predicate func(string) bool
}

// tuiKeymap is the shared permission and footer model used by every TUI domain.
type tuiKeymap struct{ bindings []tuiKeyBinding }

func tuiKey(label string, keys ...string) tuiKeyBinding {
	return tuiKeyBinding{keys: keys, label: label}
}

func tuiDynamicKey(keys []string, label func() string) tuiKeyBinding {
	return tuiKeyBinding{keys: keys, labelFunc: label}
}

func tuiTextKey(label string) tuiKeyBinding {
	return tuiKeyBinding{label: label, text: true}
}

func tuiTextKeyMatching(label string, predicate func(string) bool) tuiKeyBinding {
	return tuiKeyBinding{label: label, text: true, predicate: predicate}
}

func newTUIKeymap(bindings ...tuiKeyBinding) tuiKeymap {
	return tuiKeymap{bindings: bindings}
}

func (keymap tuiKeymap) Permits(key string) bool {
	for _, binding := range keymap.bindings {
		for _, candidate := range binding.keys {
			if key == candidate {
				return true
			}
		}
		if binding.text && utf8.RuneCountInString(key) == 1 && (binding.predicate == nil || binding.predicate(key)) {
			return true
		}
	}
	return false
}

func (keymap tuiKeymap) FooterLines(width int) []string {
	budget := max(1, width-4)
	lines := []string{}
	line := ""
	for _, binding := range keymap.bindings {
		label := i18n.T(binding.label)
		if binding.labelFunc != nil {
			label = binding.labelFunc()
		}
		if label == "" {
			continue
		}
		candidate := label
		if line != "" {
			candidate = line + "  " + label
		}
		if line != "" && DisplayWidth(candidate) > budget {
			lines = append(lines, line)
			line = label
			continue
		}
		line = candidate
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func tuiFooterHeight(view *tuiModel) int {
	return max(3, len(tuiCurrentKeymap(view).FooterLines(view.width))+2)
}

func tuiScanApplicationKeymap(view *tuiModel) tuiKeymap {
	bindings := []tuiKeyBinding{
		tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.details_tab", "tab"), tuiKey("tui.key.search", "f"),
		tuiKey("tui.key.auto_scan", "s"), tuiKey("tui.key.scan_selected", "t"), tuiKey("tui.key.logs", "l"),
		tuiKey("tui.key.delete_app", "d"),
	}
	bindings = append(bindings, tuiKey("tui.key.exclude", "x"))
	if application, found := selectedScanApplication(view); found && strings.TrimSpace(application.Identity) == "" {
		bindings = append(bindings, tuiKey("tui.key.identity", "i"))
	}
	bindings = append(bindings, tuiKey("tui.key.managed", "m"), tuiKey("tui.key.back_only", "esc"))
	return newTUIKeymap(bindings...)
}

func tuiCurrentKeymap(view *tuiModel) tuiKeymap {
	if (view.width > 0 && view.width < 80) || (view.height > 0 && view.height < 24) {
		return newTUIKeymap()
	}
	if view.assetSelection != nil {
		return newTUIKeymap(
			tuiKey("tui.key.select", "up", "down"),
			tuiKey("tui.key.confirm_select", "left", "right"),
			tuiDynamicKey([]string{"enter"}, func() string { return "ENTER " + i18n.T("tui.confirm") }),
			tuiDynamicKey([]string{"esc"}, func() string { return "ESC " + i18n.T("tui.cancel") }),
		)
	}
	if view.searchActive {
		return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.page", "pageup", "pagedown"), tuiKey("tui.key.bounds", "home", "end"), tuiKey("tui.key.clear", "ctrl+c"), tuiTextKeyMatching("tui.key.type", isTUIQuickSearchCharacter), tuiKey("tui.key.exit_search_long", "esc"))
	}
	if view.editing {
		if view.page == tuiScan && view.scanEditFocus {
			return newTUIKeymap(tuiKey("tui.key.move", "left", "right"), tuiKey("tui.key.bounds", "home", "end"), tuiKey("tui.key.delete", "backspace", "delete"), tuiTextKey("tui.key.type"), tuiKey("tui.key.apply_edit", "enter"), tuiKey("tui.key.stage_only", "ctrl+s"), tuiKey("tui.key.cancel", "esc"))
		}
		return newTUIKeymap(tuiKey("tui.key.move", "left", "right"), tuiKey("tui.key.bounds", "home", "end"), tuiKey("tui.key.delete", "backspace", "delete"), tuiTextKey("tui.key.type"), tuiKey("tui.key.confirm_edit", "enter"), tuiKey("tui.key.stage_only", "ctrl+s"), tuiKey("tui.key.cancel", "esc"))
	}
	if view.reloadConfirm || view.configExitConfirm || view.confirm || view.scanConfirm != "" {
		primary, secondary := tuiConfirmationLabels(view)
		return newTUIKeymap(tuiKey("tui.key.confirm_select", "left", "right"), tuiDynamicKey([]string{"enter"}, func() string { return "ENTER " + i18n.T(primary) }), tuiDynamicKey([]string{"esc"}, func() string { return "ESC " + i18n.T(secondary) }))
	}
	if view.scanPartial {
		return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.toggle", " "), tuiKey("tui.key.all", "a"), tuiKey("tui.key.apply", "enter"), tuiKey("tui.key.cancel", "esc"))
	}
	if view.logFocus || view.scanLogFocus {
		return newTUIKeymap(tuiKey("tui.key.scroll", "up", "down"), tuiKey("tui.key.page", "pageup", "pagedown"), tuiKey("tui.key.log_bounds", "home", "end"), tuiKey("tui.key.back_logs_only", "l"), tuiKey("tui.key.quit", "q"))
	}
	if view.scanRunning {
		return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.logs", "l"), tuiKey("tui.key.scan_cancel", "esc"), tuiKey("tui.key.quit", "q"))
	}
	if view.page == tuiScan && view.running {
		return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.logs", "l"), tuiKey("tui.key.cancel", "esc"), tuiKey("tui.key.quit", "q"))
	}
	if view.running {
		return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.cancel", "esc"), tuiKey("tui.key.quit", "q"), tuiKey("tui.key.check", "c"), tuiKey("tui.key.check_all", "a"), tuiKey("tui.key.update", "u"), tuiKey("tui.key.update_all", "ctrl+u"), tuiKey("tui.key.logs", "l"), tuiKey("tui.key.queue", "tab"))
	}
	if view.detailFocus {
		return newTUIKeymap(tuiKey("tui.key.detail_scroll", "up", "down"), tuiKey("tui.key.page", "pageup", "pagedown"), tuiKey("tui.key.bounds", "home", "end"), tuiKey("tui.key.back", "enter", "esc"), tuiKey("tui.key.quit", "q"))
	}
	if view.page == tuiConfig {
		bindings := []tuiKeyBinding{tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.adjust", "left", "right"), tuiKey("tui.key.edit", "enter"), tuiKey("tui.key.save_apply", "ctrl+s"), tuiKey("tui.key.revert", "r"), tuiKey("tui.key.back_only", "esc")}
		if !view.configAppFocus {
			bindings = append(bindings, tuiKey("tui.key.quit", "q"))
		}
		return newTUIKeymap(bindings...)
	}
	if view.page == tuiScan {
		if view.scanEditFocus {
			return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.adjust", "left", "right"), tuiKey("tui.key.edit", "enter"), tuiKey("tui.key.stage_only", "ctrl+s"), tuiKey("tui.key.revert", "r"), tuiKey("tui.key.back_only", "esc"))
		}
		if _, ok := selectedScanAdded(view); ok {
			return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.details_tab", "tab"), tuiKey("tui.key.search", "f"), tuiKey("tui.key.scan_edit", "e"), tuiKey("tui.key.add_all", "a"), tuiKey("tui.key.add", "j"), tuiKey("tui.key.exclude", "x"), tuiKey("tui.key.scan_selected", "t"), tuiKey("tui.key.logs", "l"), tuiKey("tui.key.back_only", "esc"))
		}
		if _, ok := selectedScanConflict(view); ok {
			return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.details_tab", "tab"), tuiKey("tui.key.search", "f"), tuiKey("tui.key.auto_scan", "s"), tuiKey("tui.key.scan_selected", "t"), tuiKey("tui.key.logs", "l"), tuiKey("tui.key.merge_all", "a"), tuiKey("tui.key.partial", "p"), tuiKey("tui.key.keep", "k"), tuiKey("tui.key.back_only", "esc"))
		}
		return tuiScanApplicationKeymap(view)
	}
	if len(view.catalog.Apps) == 0 {
		return newTUIKeymap(tuiKey("tui.key.scan", "ctrl+s"), tuiKey("tui.key.settings", "s"), tuiKey("tui.key.logs", "l"), tuiKey("tui.key.quit", "q"))
	}
	return newTUIKeymap(tuiKey("tui.key.select", "up", "down"), tuiKey("tui.key.search", "f"), tuiKey("tui.key.details", "enter"), tuiKey("tui.key.queue", "tab"), tuiKey("tui.key.app_toggle", " "), tuiKey("tui.key.check", "c"), tuiKey("tui.key.check_all", "a"), tuiKey("tui.key.update", "u"), tuiKey("tui.key.update_all", "ctrl+u"), tuiKey("tui.key.logs", "l"), tuiKey("tui.key.settings", "s"), tuiKey("tui.key.scan", "ctrl+s"), tuiKey("tui.key.quit", "q"))
}
