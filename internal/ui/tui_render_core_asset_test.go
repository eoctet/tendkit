package ui

import (
	"strings"
	"testing"

	"github.com/eoctet/tendkit/pkg/i18n"
)

func TestDownloadAssetDialogSharesConfirmationFrameAndButtons(t *testing.T) {
	for _, language := range []string{"en", "zh"} {
		t.Run(language, func(t *testing.T) {
			i18n.Set(i18n.Language(language))
			view := tuiModel{assetSelection: &tuiAssetSelection{candidates: []string{"非常非常长的资产名称-with-a-long-suffix-that-must-not-cross-the-border.dmg"}}, viewportState: viewportState{width: 80, height: 24}}
			asset := newTUIScreen(80, 24)
			renderDownloadAssetSelection(asset, &view)
			confirm := newTUIScreen(80, 24)
			renderConfirmationDialog(confirm, &view, "Title", "A prompt that wraps consistently in the shared dialog frame.", tuiCyan, i18n.T("tui.confirm"), i18n.T("tui.cancel"))
			probe := newTUIScreen(80, 24)
			assetFrame := renderDialogFrame(probe, i18n.T("tui.download_file_title"), i18n.T("tui.download_file_prompt"), tuiCyan, 1)
			confirmFrame := renderDialogFrame(probe, "Title", "A prompt that wraps consistently in the shared dialog frame.", tuiCyan, 0)
			if assetFrame.width != confirmFrame.width || assetFrame.x+3 >= assetFrame.x+assetFrame.width-1 {
				t.Fatalf("frames asset=%#v confirm=%#v", assetFrame, confirmFrame)
			}
			line := stringScreenRow(asset, assetFrame.buttonRow)
			if !strings.Contains(line, "ENTER") || !strings.Contains(line, "ESC") || asset.cells[assetFrame.buttonRow][assetFrame.x].value != '│' || asset.cells[assetFrame.buttonRow][assetFrame.x+assetFrame.width-1].value != '│' {
				t.Fatalf("button row=%q", line)
			}
		})
	}
}

func TestDownloadAssetDialogViewportKeepsSelectedAssetVisible(t *testing.T) {
	items := make([]string, 30)
	for i := range items {
		items[i] = "asset-" + string(rune('A'+i%26)) + ".dmg"
	}
	for _, selected := range []int{0, 10, 29} {
		view := tuiModel{assetSelection: &tuiAssetSelection{candidates: items, selected: selected, offset: selected}, viewportState: viewportState{width: 80, height: 24}}
		screen := newTUIScreen(80, 24)
		renderDownloadAssetSelection(screen, &view)
		if !strings.Contains(screen.string(), items[selected]) {
			t.Fatalf("selected %q is not visible", items[selected])
		}
	}
}

func stringScreenRow(screen *tuiScreen, row int) string {
	var value strings.Builder
	for _, cell := range screen.cells[row] {
		if !cell.continuation {
			value.WriteRune(cell.value)
		}
	}
	return value.String()
}
