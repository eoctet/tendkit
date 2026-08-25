package component

import "testing"

func TestProgressBarClampsAndFillsAvailableWidth(t *testing.T) {
	for _, test := range []struct {
		name    string
		percent int
		width   int
		want    string
	}{
		{name: "half", percent: 50, width: 10, want: "[####----]"},
		{name: "below zero", percent: -1, width: 6, want: "[----]"},
		{name: "above one hundred", percent: 101, width: 6, want: "[####]"},
		{name: "too narrow", percent: 50, width: 1, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ProgressBar(test.percent, test.width); got != test.want {
				t.Fatalf("ProgressBar(%d, %d) = %q, want %q", test.percent, test.width, got, test.want)
			}
		})
	}
}
