package ui

import (
	"reflect"
	"strings"
	"testing"
)

func TestTUIPureAlgorithms(t *testing.T) {
	t.Run("offset-bounds-and-selection-reveal", func(t *testing.T) {
		for _, test := range []struct {
			current, delta, maximum, want int
		}{
			{-3, 0, 4, 0},
			{0, -1, 4, 0},
			{2, 3, 4, 4},
			{9, 0, 4, 4},
			{3, -2, 4, 1},
		} {
			if got := boundedOffset(test.current, test.delta, test.maximum); got != test.want {
				t.Errorf("boundedOffset(%d, %d, %d) = %d, want %d", test.current, test.delta, test.maximum, got, test.want)
			}
		}
		for _, test := range []struct {
			selected, offset, visible, want int
		}{
			{0, 0, 4, 0},
			{0, 3, 4, 0},
			{4, 3, 4, 3},
			{7, 3, 4, 4},
			{9, 4, 4, 6},
		} {
			if got := revealSelection(test.selected, test.offset, test.visible); got != test.want {
				t.Errorf("revealSelection(%d, %d, %d) = %d, want %d", test.selected, test.offset, test.visible, got, test.want)
			}
		}
		if got := maximumOffset(3, 4); got != 0 {
			t.Fatalf("maximumOffset(3, 4) = %d, want 0", got)
		}
		if got := maximumOffset(8, 3); got != 5 {
			t.Fatalf("maximumOffset(8, 3) = %d, want 5", got)
		}
		if got := maximumOffset(0, 3); got != 0 {
			t.Fatalf("maximumOffset(0, 3) = %d, want 0", got)
		}
	})

	t.Run("wraps-log-lines-with-utf8-width", func(t *testing.T) {
		got := wrapLogLines([]string{"ab界cd", "", "ef", strings.Repeat("界", 3)}, 4)
		want := []string{"ab界", "cd", "", "ef", "界界", "界"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wrapLogLines() = %#v, want %#v", got, want)
		}
		long := "alpha界beta界gamma界delta界epsilon界zeta界eta"
		got = wrapLogLines([]string{long}, 7)
		if strings.Join(got, "") != long {
			t.Fatalf("long wrapped log lost or reordered content: %#v", got)
		}
		if len(got) < 4 {
			t.Fatalf("long wrapped log = %#v, want multiple wrapped lines", got)
		}
		for index, line := range got {
			if width := DisplayWidth(line); width > 7 {
				t.Fatalf("long wrapped log line %d display width = %d, want <= 7: %q", index, width, line)
			}
		}
	})

	t.Run("preserves-application-and-scan-column-widths", func(t *testing.T) {
		for _, test := range []struct {
			width int
			app   []int
			scan  []int
		}{
			{20, []int{4, 8, 9, 9, 10, 9}, []int{4, 10, 10, 8, 12}},
			{80, []int{5, 14, 14, 17, 16, 12}, []int{5, 23, 18, 12, 20}},
			{110, []int{5, 29, 16, 22, 22, 14}, []int{5, 53, 18, 12, 20}},
			{120, []int{5, 39, 16, 22, 22, 14}, []int{5, 63, 18, 12, 20}},
		} {
			if got := applicationTableColumnWidths(test.width); !reflect.DeepEqual(got, test.app) {
				t.Errorf("applicationTableColumnWidths(%d) = %v, want %v", test.width, got, test.app)
			}
			if got := scanTableColumnWidths(test.width); !reflect.DeepEqual(got, test.scan) {
				t.Errorf("scanTableColumnWidths(%d) = %v, want %v", test.width, got, test.scan)
			}
		}
		if got, want := tableColumnStarts(2, []int{5, 14, 14}), []int{2, 7, 21}; !reflect.DeepEqual(got, want) {
			t.Fatalf("tableColumnStarts() = %v, want %v", got, want)
		}
		for _, test := range []struct{ count, offset, visible, start, end int }{
			{0, 0, 4, 0, 0},
			{1, 0, 4, 0, 1},
			{10, 3, 4, 3, 7},
			{10, 8, 4, 8, 10},
		} {
			if start, end := visibleTableRange(test.count, test.offset, test.visible); start != test.start || end != test.end {
				t.Errorf("visibleTableRange(%d, %d, %d) = (%d, %d), want (%d, %d)", test.count, test.offset, test.visible, start, end, test.start, test.end)
			}
		}
	})

	t.Run("renders-full-header-cells-at-their-column-starts", func(t *testing.T) {
		screen := newTUIScreen(24, 4)
		columns, headings, widths := []int{1, 8}, []string{"Alpha", "Beta"}, []int{6, 6}
		renderTableHeader(screen, columns, 1, headings, widths)
		for index, heading := range headings {
			want := truncateTUI(heading, widths[index]-1)
			if got := tuiScreenCellText(screen, 1, columns[index], widths[index]-1); got != want {
				t.Errorf("header %d = %q, want %q", index, got, want)
			}
			for column := columns[index]; column < columns[index]+DisplayWidth(want); column++ {
				if screen.cells[1][column].style != tuiBold {
					t.Errorf("header %d column %d style = %q, want %q", index, column, screen.cells[1][column].style, tuiBold)
				}
			}
		}
	})

	t.Run("renders-80x24-tables-and-preserves-extreme-narrow-application-early-return", func(t *testing.T) {
		view := sampleTUIView()
		view.width, view.height = 80, 24
		applicationScreen := newTUIScreen(80, 24)
		renderApplicationTable(applicationScreen, &view, 1, 1, 50, 10)
		if applicationScreen.cells[3][2].value != '─' || applicationScreen.cells[3][49].value != '─' {
			t.Fatalf("application separator did not retain width - 2 behavior")
		}
		if applicationScreen.cells[2][2].style != tuiBold {
			t.Fatalf("application table header style = %q, want %q", applicationScreen.cells[2][2].style, tuiBold)
		}

		scanWideScreen := newTUIScreen(80, 24)
		renderScanApplicationTable(scanWideScreen, &view, 1, 1, 50, 10)
		if scanWideScreen.cells[2][2].style != tuiBold {
			t.Fatalf("scan table header style = %q, want %q", scanWideScreen.cells[2][2].style, tuiBold)
		}

		scanScreen := newTUIScreen(3, 4)
		renderScanApplicationTable(scanScreen, &view, 0, 0, 1, 3)
		if scanScreen.cells[2][1].value != '─' || scanScreen.cells[2][1].style != tuiDim {
			t.Fatalf("scan separator did not retain max(1, width - 2) behavior: %#v", scanScreen.cells[2][1])
		}

		narrowScreen := newTUIScreen(30, 10)
		renderApplicationTable(narrowScreen, &view, 1, 1, 19, 8)
		for _, row := range narrowScreen.cells {
			for _, cell := range row {
				if cell.value != ' ' || cell.style != tuiNormal {
					t.Fatalf("narrow application table wrote %#v despite width < 20", cell)
				}
			}
		}
	})
}

func tuiScreenCellText(screen *tuiScreen, row, column, width int) string {
	var text strings.Builder
	for index := column; index < min(screen.width, column+width); index++ {
		cell := screen.cells[row][index]
		if !cell.continuation {
			text.WriteRune(cell.value)
		}
	}
	return strings.TrimRight(text.String(), " ")
}
