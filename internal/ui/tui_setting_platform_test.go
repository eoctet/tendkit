package ui

import "testing"

func TestScanApplicationSettingIsReadOnlyOnlyOutsideMacOS(t *testing.T) {
	previous := tuiSettingsSupportApplicationBundles
	t.Cleanup(func() { tuiSettingsSupportApplicationBundles = previous })
	tuiSettingsSupportApplicationBundles = func() bool { return false }
	if got := scanApplicationRowType(); got != configRowReadOnly {
		t.Fatalf("non-macOS row type = %v, want read-only", got)
	}
	tuiSettingsSupportApplicationBundles = func() bool { return true }
	if got := scanApplicationRowType(); got != configRowBoolean {
		t.Fatalf("macOS row type = %v, want boolean", got)
	}
}
