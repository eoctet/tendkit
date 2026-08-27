package scanner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/handler"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestPackageEvidenceRealScanner(t *testing.T) {
	if os.Getenv("TENDKIT_REAL_PACKAGE_EVIDENCE") != "1" {
		t.Skip("set TENDKIT_REAL_PACKAGE_EVIDENCE=1 to scan local package-manager inventories")
	}
	catalog := model.Config{
		SchemaVersion: model.SchemaVersion,
		Settings: model.Settings{Scan: model.ScanSettings{
			Path: true,
			Packages: model.PackageScanSettings{
				Go: true, UV: true, Node: true, Python: true, Ruby: true,
			},
		}},
		ScanVersionControl: map[string]map[string]model.ScanKeepResolution{},
	}
	runner := runtimeutil.Runner{IdleTimeout: 2 * time.Minute}
	nodeInventory := handler.NewNode(runner).Scan(context.Background(), handler.Request{})
	var nodeIncompleteError *handler.PackageInventoryIncompleteError
	nodeIncomplete := errors.As(nodeInventory.Err, &nodeIncompleteError)
	if nodeInventory.Err != nil && !nodeIncomplete {
		t.Fatalf("real Node inventory failed unexpectedly: %v", nodeInventory.Err)
	}
	updated, state, err := (Scanner{Runner: runner}).Scan(context.Background(), catalog, model.RuntimeState{})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, app := range updated.Apps {
		for _, ecosystem := range []string{"go", "uv", "node", "python", "ruby"} {
			if strings.HasPrefix(app.Identity, "package:"+ecosystem+":") {
				counts[ecosystem]++
				t.Logf("real scanner %s: id=%s type=%s provider=%s package=%s path=%s", ecosystem, app.ID, app.Type, app.Provider.Type, app.Package, app.InstallPath)
			}
		}
	}
	for _, ecosystem := range []string{"go", "uv", "node", "python", "ruby"} {
		if counts[ecosystem] == 0 {
			t.Fatalf("real scanner produced no %s catalog records: counts=%v", ecosystem, counts)
		}
	}
	if nodeIncomplete {
		expected := []struct{ id, binary, packageName, updateAction string }{
			{id: "cli-codex", binary: "codex", packageName: "@openai/codex", updateAction: "codex update"},
			{id: "cli-claude", binary: "claude", packageName: "@anthropic-ai/claude-code", updateAction: "claude update"},
			{id: "cli-gemini", binary: "gemini", packageName: "@google/gemini-cli"},
		}
		for _, item := range expected {
			if _, err := exec.LookPath(item.binary); err != nil {
				continue
			}
			app := applicationByID(t, updated.Apps, item.id)
			if app.Type != model.ApplicationTypeCLI || app.Provider.Type != model.ProviderNPM || app.Package != item.packageName || app.Identity != model.PackageIdentity("node", item.packageName) || app.Provider.UpdateAction() != item.updateAction {
				t.Fatalf("incomplete Node inventory migrated PATH canonical %s: %#v", item.id, app)
			}
		}
	}
	if _, err := exec.LookPath("pip3"); err == nil {
		pip := applicationByID(t, updated.Apps, "cli-pip3")
		if pip.Type != model.ApplicationTypeCLI || pip.Provider.Type != model.ProviderPyPI || pip.Package != "pip" {
			t.Fatalf("complete Python evidence did not merge into the PATH canonical pip3 record: %#v", pip)
		}
		if _, found := catalogApplicationByID(updated.Apps, "pkg-python-pip"); found {
			t.Fatal("complete Python evidence left an independent pip package record")
		}
	}
	for _, id := range []string{"pkg-node-ccusage", "pkg-ruby-cocoapods"} {
		if app, found := catalogApplicationByID(updated.Apps, id); found && (app.StatusManaged.UpdateStatus == model.StatusFailed || app.StatusManaged.Error != "") {
			t.Fatalf("incomplete owner-only package was reported as an installation reconciliation conflict: %#v", app)
		}
	}
	if len(state.Observations) == 0 {
		t.Fatal("real scanner produced no runtime observations")
	}
	t.Logf("real scanner catalog: apps=%d observations=%d package_counts=%v", len(updated.Apps), len(state.Observations), counts)
}
