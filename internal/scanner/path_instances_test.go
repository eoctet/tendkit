package scanner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/handler"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestAssignPathInstancesUsesStableIDsIndependentOfCandidateOrder(t *testing.T) {
	first := writePathInstanceExecutable(t, "first/tool")
	second := writePathInstanceExecutable(t, "second/tool")
	candidates := []handler.Candidate{
		{Application: pathInstanceApplication(second)},
		{Application: pathInstanceApplication(first)},
	}

	forward, err := assignPathInstances("tool", candidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := assignPathInstances("tool", []handler.Candidate{candidates[1], candidates[0]}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(forward.Candidates, reverse.Candidates) {
		t.Fatalf("assignment changed with input order:\nforward=%#v\nreverse=%#v", forward.Candidates, reverse.Candidates)
	}
	if len(forward.Candidates) != 2 {
		t.Fatalf("candidate count = %d", len(forward.Candidates))
	}
	for index, candidate := range forward.Candidates {
		canonical, err := handler.CanonicalExecutablePath(candidate.Application.InstallPath)
		if err != nil {
			t.Fatal(err)
		}
		wantID := "cli-tool-" + pathInstanceFingerprint(canonical)
		if candidate.Application.ID != wantID {
			t.Fatalf("candidate %d ID = %q, want %q", index, candidate.Application.ID, wantID)
		}
	}
	for index, candidate := range forward.Candidates {
		want := "cli:tool@" + pathInstanceFingerprint(mustCanonicalExecutablePath(t, candidate.Application.InstallPath))
		if candidate.Application.Identity != want {
			t.Fatalf("candidate %d identity = %q, want %q", index, candidate.Application.Identity, want)
		}
	}
}

func TestAssignPathInstancesUsesBaseIdentityForSingleCandidate(t *testing.T) {
	path := writePathInstanceExecutable(t, "only/tool")
	assignment, err := assignPathInstances("tool", []handler.Candidate{{Application: pathInstanceApplication(path)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := assignment.Candidates[0].Application
	if app.ID != "cli-tool" || app.Identity != "cli:tool" {
		t.Fatalf("single assignment = id=%q identity=%q", app.ID, app.Identity)
	}
}

func TestAssignPathInstancesMigratesHistoricalBaseIDAndRetainsVisiblePath(t *testing.T) {
	first := writePathInstanceExecutable(t, "targets/first")
	second := writePathInstanceExecutable(t, "targets/second")
	visibleDir := filepath.Join(t.TempDir(), "visible")
	if err := os.MkdirAll(visibleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	visible := filepath.Join(visibleDir, "tool")
	if err := os.Symlink(first, visible); err != nil {
		t.Fatal(err)
	}
	history := []model.Application{{
		ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: visible,
		Identity: "cli:ripgrep", ScanManaged: true,
	}}
	assignment, err := assignPathInstances("ripgrep", []handler.Candidate{
		{Application: ripgrepPathInstanceApplication(first)},
		{Application: ripgrepPathInstanceApplication(second)},
	}, history)
	if err != nil {
		t.Fatal(err)
	}
	wantID := "cli-ripgrep-" + pathInstanceFingerprint(mustCanonicalExecutablePath(t, first))
	if assignment.Migrations["cli-ripgrep"] != wantID {
		t.Fatalf("migrations = %v, want cli-ripgrep -> %s", assignment.Migrations, wantID)
	}
	wantIdentity := "cli:ripgrep@" + pathInstanceFingerprint(mustCanonicalExecutablePath(t, first))
	if assignment.Candidates[0].Application.InstallPath != visible || assignment.Candidates[0].Application.Identity != wantIdentity || assignment.Candidates[0].Application.Provider.VersionAction() != visible+" --version" {
		t.Fatalf("historical visible path not retained: %#v", assignment.Candidates[0].Application)
	}
	if assignment.IdentityMigrations["cli-ripgrep"] != wantIdentity {
		t.Fatalf("identity migrations = %v, want cli-ripgrep -> %s", assignment.IdentityMigrations, wantIdentity)
	}
}

func TestApplyPathInstanceMigrationsPreservesHistoricalPackageIdentity(t *testing.T) {
	first := writePathInstanceExecutable(t, "targets/first")
	second := writePathInstanceExecutable(t, "targets/second")
	history := model.Application{
		ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: first,
		Identity: "package:homebrew-formula:ripgrep", Package: "formula/ripgrep",
		Provider: model.ProviderConfig{Type: model.ProviderHomebrew}, ScanManaged: true,
	}
	assignment, err := assignPathInstances("ripgrep", []handler.Candidate{
		{Application: ripgrepPathInstanceApplication(first)},
		{Application: ripgrepPathInstanceApplication(second)},
	}, []model.Application{history})
	if err != nil {
		t.Fatal(err)
	}
	wantID := "cli-ripgrep-" + pathInstanceFingerprint(mustCanonicalExecutablePath(t, first))
	if assignment.Migrations[history.ID] != wantID {
		t.Fatalf("migrations = %v, want %s -> %s", assignment.Migrations, history.ID, wantID)
	}
	if _, migrated := assignment.IdentityMigrations[history.ID]; migrated {
		t.Fatalf("package identity was scheduled for Path migration: %v", assignment.IdentityMigrations)
	}
	session := scanSession{
		catalog: model.Config{Apps: []model.Application{history}, ScanVersionControl: map[string]map[string]model.ScanKeepResolution{}},
		state:   model.RuntimeState{Observations: map[string]model.ScanObservation{}},
	}
	session.applyPathInstanceMigrations(assignment)
	got := applicationByID(t, session.catalog.Apps, wantID)
	if got.Identity != history.Identity {
		t.Fatalf("package identity changed during Path ID migration: got=%q want=%q", got.Identity, history.Identity)
	}
}

func TestScannerPreservesHistoricalPackageIdentityWhenPackageScanDisabled(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "first")
	secondDir := filepath.Join(root, "second")
	first := writeNamedExecutable(t, firstDir, "rg", "ripgrep 1.0.0")
	writeNamedExecutable(t, secondDir, "rg", "ripgrep 2.0.0")
	history := model.Application{
		ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: first,
		Identity: "package:homebrew-formula:ripgrep", Package: "formula/ripgrep",
		Provider: model.ProviderConfig{Type: model.ProviderHomebrew}, ScanManaged: true,
	}
	catalog := scanPathInstances(t, strings.Join([]string{firstDir, secondDir}, string(os.PathListSeparator)), []model.Application{history})
	wantID := "cli-ripgrep-" + pathInstanceFingerprint(mustCanonicalExecutablePath(t, first))
	got := applicationByID(t, catalog.Apps, wantID)
	if got.Identity != history.Identity || got.Provider.Type != history.Provider.Type || got.Package != history.Package {
		t.Fatalf("disabled package scan changed historical ownership: %#v", got)
	}
}

func TestScannerRehomesMissingHistoricalPackageIdentityWithoutChangingOwnership(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	oldPath := writeNamedExecutable(t, oldDir, "rg", "ripgrep 1.0.0")
	history := model.Application{
		ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: oldPath,
		Identity: "package:homebrew-formula:ripgrep", Package: "formula/ripgrep",
		Provider: model.ProviderConfig{Type: model.ProviderHomebrew}, ScanManaged: true,
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	newPath := writeNamedExecutable(t, newDir, "rg", "ripgrep 2.0.0")
	catalog := scanPathInstances(t, newDir, []model.Application{history})
	if len(catalog.Apps) != 2 {
		t.Fatalf("missing package-owned history was not preserved: %#v", catalog.Apps)
	}
	for _, app := range catalog.Apps {
		switch app.InstallPath {
		case newPath:
			if app.ID != "cli-ripgrep" || app.Identity != "cli:ripgrep" || app.StatusManaged.UpdateStatus == model.StatusMissing {
				t.Fatalf("new single instance = %#v", app)
			}
		case oldPath:
			if !strings.HasPrefix(app.ID, "cli-ripgrep-") || app.Identity != history.Identity || app.StatusManaged.UpdateStatus != model.StatusMissing {
				t.Fatalf("missing package-owned instance = %#v", app)
			}
		default:
			t.Fatalf("unexpected instance %#v", app)
		}
	}
}

func TestExistingIndexDoesNotUseNameFallbackForDifferentPathInstance(t *testing.T) {
	first := writePathInstanceExecutable(t, "first/tool")
	second := writePathInstanceExecutable(t, "second/tool")
	index := indexApps([]model.Application{{
		ID: "cli-ripgrep-deadbeefdeadbeef", Name: "ripgrep", Type: model.ApplicationTypeCLI,
		InstallPath: first, Identity: "cli:ripgrep@deadbeefdeadbeef", ScanManaged: true,
	}})
	candidate := pathInstanceApplication(second)
	candidate.ID = "cli-ripgrep-cafebabecafebabe"
	candidate.Name = "ripgrep"
	candidate.Identity = "cli:ripgrep@cafebabecafebabe"
	if got := index.match(candidate); got != "" {
		t.Fatalf("different path instance matched by name as %q", got)
	}
}

func TestBuiltInPathDeduplicationKeyIncludesCanonicalPath(t *testing.T) {
	first := pathInstanceApplication(writePathInstanceExecutable(t, "first/tool"))
	second := pathInstanceApplication(writePathInstanceExecutable(t, "second/tool"))
	first.ID, first.Name, first.Identity = "cli-ripgrep", "ripgrep", "cli:ripgrep"
	second.ID, second.Name, second.Identity = "cli-ripgrep", "ripgrep", "cli:ripgrep"
	active := map[string]bool{"ripgrep": true}
	if left, right := deduplicationKey(first, active), deduplicationKey(second, active); left == right {
		t.Fatalf("different installations share deduplication key %q", left)
	}
}

func TestScannerDiscoversMultiplePathInstancesWithStableIdentity(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "first")
	secondDir := filepath.Join(root, "second")
	writeNamedExecutable(t, firstDir, "rg", "ripgrep 1.0.0")
	writeNamedExecutable(t, secondDir, "rg", "ripgrep 2.0.0")

	forward := scanPathInstances(t, strings.Join([]string{secondDir, firstDir}, string(os.PathListSeparator)), nil)
	if len(forward.Apps) != 2 {
		t.Fatalf("apps = %#v", forward.Apps)
	}
	for _, app := range forward.Apps {
		canonical := mustCanonicalExecutablePath(t, app.InstallPath)
		if app.ID != "cli-ripgrep-"+pathInstanceFingerprint(canonical) {
			t.Fatalf("unexpected ID %q for %q", app.ID, app.InstallPath)
		}
		if !strings.HasPrefix(app.Provider.VersionAction(), app.InstallPath) {
			t.Fatalf("version action %q is not bound to %q", app.Provider.VersionAction(), app.InstallPath)
		}
	}
	identities := pathIdentityByCanonical(t, forward.Apps)
	for canonical, identity := range identities {
		if want := "cli:ripgrep@" + pathInstanceFingerprint(canonical); identity != want {
			t.Fatalf("identity assignment for %q = %q, want %q", canonical, identity, want)
		}
	}

	reversed := scanPathInstances(t, strings.Join([]string{firstDir, secondDir}, string(os.PathListSeparator)), forward.Apps)
	if got := pathIdentityByCanonical(t, reversed.Apps); !reflect.DeepEqual(got, identities) {
		t.Fatalf("identity changed after PATH reorder: got=%v want=%v", got, identities)
	}
}

func TestScannerUsesBaseIDForSinglePathInstanceAndDeduplicatesSymlink(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target")
	aliasDir := filepath.Join(root, "alias")
	target := writeNamedExecutable(t, targetDir, "rg", "ripgrep 1.0.0")
	if err := os.MkdirAll(aliasDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(aliasDir, "rg")); err != nil {
		t.Fatal(err)
	}
	catalog := scanPathInstances(t, strings.Join([]string{aliasDir, targetDir, aliasDir}, string(os.PathListSeparator)), nil)
	if len(catalog.Apps) != 1 || catalog.Apps[0].ID != "cli-ripgrep" || catalog.Apps[0].Identity != "cli:ripgrep" {
		t.Fatalf("catalog apps = %#v", catalog.Apps)
	}
}

func TestScannerMigratesHistoricalBaseIDWhenSecondInstanceAppears(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "first")
	secondDir := filepath.Join(root, "second")
	first := writeNamedExecutable(t, firstDir, "rg", "ripgrep 1.0.0")
	writeNamedExecutable(t, secondDir, "rg", "ripgrep 2.0.0")
	history := pathInstanceApplication(first)
	history.ID, history.Name, history.Identity = "cli-ripgrep", "ripgrep", "cli:ripgrep"
	history.StatusManaged = model.ManagedStatus{FirstDetectedTime: "2026-01-02T03:04:05Z", CurrentVersion: "0.9.0"}

	catalog := scanPathInstances(t, strings.Join([]string{firstDir, secondDir}, string(os.PathListSeparator)), []model.Application{history})
	if len(catalog.Apps) != 2 {
		t.Fatalf("apps = %#v", catalog.Apps)
	}
	wantID := "cli-ripgrep-" + pathInstanceFingerprint(mustCanonicalExecutablePath(t, first))
	for _, app := range catalog.Apps {
		if app.ID == "cli-ripgrep" {
			t.Fatalf("multi-instance catalog retained base ID: %#v", catalog.Apps)
		}
		if app.Identity == "cli:ripgrep" {
			t.Fatalf("multi-instance catalog retained base identity: %#v", catalog.Apps)
		}
		if app.ID == wantID && app.StatusManaged.FirstDetectedTime != history.StatusManaged.FirstDetectedTime {
			t.Fatalf("historical state not migrated: %#v", app.StatusManaged)
		}
		if app.ID == wantID && !strings.HasPrefix(app.Provider.VersionAction(), app.InstallPath) {
			t.Fatalf("historical version action was not rebound: %#v", app.Provider)
		}
	}
}

func TestInstallationReconciliationAssignsOwnerOnlyToExactPathInstance(t *testing.T) {
	first := writePathInstanceExecutable(t, "first/rg")
	second := writePathInstanceExecutable(t, "second/rg")
	assignment, err := assignPathInstances("ripgrep", []handler.Candidate{
		{Application: ripgrepPathInstanceApplication(first)},
		{Application: ripgrepPathInstanceApplication(second)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	session := scanSession{
		catalog:    model.Config{ScanVersionControl: map[string]map[string]model.ScanKeepResolution{}},
		state:      model.RuntimeState{Observations: map[string]model.ScanObservation{}},
		discovered: make([]model.Application, 0, len(assignment.Candidates)),
		observed:   map[string]model.ManagedStatus{},
		packages:   packageScanResult{Complete: map[string]bool{string(handler.HomebrewFormula): true}, Errors: map[string]error{}},
	}
	for _, candidate := range assignment.Candidates {
		session.discovered = append(session.discovered, candidate.Application)
		session.observed[candidate.Application.ID] = model.ManagedStatus{CurrentVersion: "1.0.0", UpdateStatus: model.StatusUnchecked}
	}
	owner := model.Application{
		ID: "pkg-homebrew-formula-ripgrep", Name: "ripgrep", Type: model.ApplicationTypePackage,
		InstallPath: filepath.Dir(second), Provider: model.ProviderConfig{Type: model.ProviderHomebrew},
		Package: "formula/ripgrep", Identity: "package:homebrew-formula:ripgrep", ScanManaged: true,
	}
	session.installationDiscoveries = []discovery{{
		App: owner,
		Evidence: &handler.InstallationEvidence{
			Source: string(handler.HomebrewFormula), Package: "formula/ripgrep", ExecutablePaths: []string{second}, InstallRoot: filepath.Dir(second),
		},
	}}
	session.reconcileManagedInstallations()
	if len(session.discovered) != 2 {
		t.Fatalf("discoveries = %#v", session.discovered)
	}
	providers := map[string]model.ProviderType{}
	for _, app := range session.discovered {
		providers[mustCanonicalExecutablePath(t, app.InstallPath)] = app.Provider.Type
	}
	if providers[mustCanonicalExecutablePath(t, first)] == model.ProviderHomebrew || providers[mustCanonicalExecutablePath(t, second)] != model.ProviderHomebrew {
		t.Fatalf("provider ownership = %v", providers)
	}
}

func TestScannerSingleRemainingInstanceTakesBaseIdentityWithoutDeletingMissingHistory(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "first")
	secondDir := filepath.Join(root, "second")
	first := writeNamedExecutable(t, firstDir, "rg", "ripgrep 1.0.0")
	second := writeNamedExecutable(t, secondDir, "rg", "ripgrep 2.0.0")
	initial := scanPathInstances(t, strings.Join([]string{firstDir, secondDir}, string(os.PathListSeparator)), nil)
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}

	remaining := scanPathInstances(t, secondDir, initial.Apps)
	if len(remaining.Apps) != 2 {
		t.Fatalf("missing history was deleted: %#v", remaining.Apps)
	}
	for _, app := range remaining.Apps {
		switch app.InstallPath {
		case second:
			if app.ID != "cli-ripgrep" || app.Identity != "cli:ripgrep" || app.StatusManaged.UpdateStatus == model.StatusMissing {
				t.Fatalf("remaining instance = %#v", app)
			}
		case first:
			if !strings.HasPrefix(app.ID, "cli-ripgrep-") || !strings.HasPrefix(app.Identity, "cli:ripgrep@") || app.StatusManaged.UpdateStatus != model.StatusMissing {
				t.Fatalf("missing historical instance = %#v", app)
			}
		default:
			t.Fatalf("unexpected instance %#v", app)
		}
	}
}

func TestScannerRehomesMissingHistoricalBaseWhenDifferentSingleInstanceAppears(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	oldPath := writeNamedExecutable(t, oldDir, "rg", "ripgrep 1.0.0")
	initial := scanPathInstances(t, oldDir, nil)
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	newPath := writeNamedExecutable(t, newDir, "rg", "ripgrep 2.0.0")

	current := scanPathInstances(t, newDir, initial.Apps)
	if len(current.Apps) != 2 {
		t.Fatalf("missing history was not preserved: %#v", current.Apps)
	}
	for _, app := range current.Apps {
		switch app.InstallPath {
		case newPath:
			if app.ID != "cli-ripgrep" || app.Identity != "cli:ripgrep" || app.StatusManaged.UpdateStatus == model.StatusMissing {
				t.Fatalf("new single instance = %#v", app)
			}
		case oldPath:
			if !strings.HasPrefix(app.ID, "cli-ripgrep-") || !strings.HasPrefix(app.Identity, "cli:ripgrep@") || app.StatusManaged.UpdateStatus != model.StatusMissing {
				t.Fatalf("re-homed missing instance = %#v", app)
			}
		default:
			t.Fatalf("unexpected instance %#v", app)
		}
	}
}

func TestPathInstanceExclusionsApplyByNameAndTargetByIDOrPath(t *testing.T) {
	first := writePathInstanceExecutable(t, "first/rg")
	second := writePathInstanceExecutable(t, "second/rg")
	assignment, err := assignPathInstances("ripgrep", []handler.Candidate{
		{Application: ripgrepPathInstanceApplication(first)},
		{Application: ripgrepPathInstanceApplication(second)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	nameMatcher := newExclusionMatcher([]string{"ripgrep"})
	for _, candidate := range assignment.Candidates {
		if !nameMatcher.excluded(candidate.Application) {
			t.Fatalf("name exclusion missed %#v", candidate.Application)
		}
	}
	target := assignment.Candidates[1].Application
	for _, pattern := range []string{target.ID, target.InstallPath} {
		matcher := newExclusionMatcher([]string{pattern})
		matched := 0
		for _, candidate := range assignment.Candidates {
			if matcher.excluded(candidate.Application) {
				matched++
			}
		}
		if matched != 1 {
			t.Fatalf("pattern %q matched %d instances", pattern, matched)
		}
	}
}

func TestAssignPathInstancesRejectsCatalogIDAndIdentityConflicts(t *testing.T) {
	path := writePathInstanceExecutable(t, "only/rg")
	candidate := handler.Candidate{Application: ripgrepPathInstanceApplication(path)}
	for _, existing := range []model.Application{
		{ID: "cli-ripgrep", Name: "Other", Type: model.ApplicationTypeCLI, InstallPath: writePathInstanceExecutable(t, "id/other"), ScanManaged: false},
		{ID: "other", Name: "Other", Type: model.ApplicationTypeCLI, InstallPath: writePathInstanceExecutable(t, "identity/other"), Identity: "cli:ripgrep", ScanManaged: false},
	} {
		if _, err := assignPathInstances("ripgrep", []handler.Candidate{candidate}, []model.Application{existing}); err == nil {
			t.Fatalf("conflict was accepted: %#v", existing)
		}
	}
}

func TestAssignPathInstancesRejectsIdentityMigrationTargetConflict(t *testing.T) {
	current := writePathInstanceExecutable(t, "current/rg")
	missing := writePathInstanceExecutable(t, "missing/rg")
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	const historicalID = "cli-ripgrep-deadbeefdeadbeef"
	existing := []model.Application{
		{
			ID: historicalID, Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: missing,
			Identity: "cli:ripgrep", ScanManaged: true,
		},
		{
			ID: "other", Name: "Other", Type: model.ApplicationTypeCLI,
			InstallPath: writePathInstanceExecutable(t, "other/tool"), Identity: "cli:ripgrep@deadbeefdeadbeef",
		},
	}
	if _, err := assignPathInstances("ripgrep", []handler.Candidate{{Application: ripgrepPathInstanceApplication(current)}}, existing); err == nil {
		t.Fatal("identity migration target conflict was accepted")
	}
}

func pathInstanceApplication(path string) model.Application {
	return model.Application{
		ID: "cli-tool", Name: "Tool", Type: model.ApplicationTypeCLI, InstallPath: path,
		Identity: "cli:tool", Provider: model.ProviderConfig{Type: model.ProviderDefault}, ScanManaged: true,
	}
}

func ripgrepPathInstanceApplication(path string) model.Application {
	app := pathInstanceApplication(path)
	app.ID, app.Name, app.Identity = "cli-ripgrep", "ripgrep", "cli:ripgrep"
	app.Provider = model.ProviderConfig{Type: model.ProviderGitHubRelease}
	app.Package = "BurntSushi/ripgrep"
	return app
}

func writePathInstanceExecutable(t *testing.T, relative string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustCanonicalExecutablePath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := handler.CanonicalExecutablePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func writeNamedExecutable(t *testing.T, directory, name, versionOutput string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	content := "#!/bin/sh\nprintf '%s\\n' '" + versionOutput + "'\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func scanPathInstances(t *testing.T, pathValue string, apps []model.Application) model.Config {
	t.Helper()
	t.Setenv("PATH", pathValue)
	catalog := model.Config{
		Settings:           model.Settings{Scan: model.ScanSettings{Path: true}},
		Apps:               apps,
		ScanVersionControl: map[string]map[string]model.ScanKeepResolution{},
	}
	scanner := Scanner{Runner: runtimeutil.Runner{IdleTimeout: 5 * time.Second}}
	result, _, err := scanner.Scan(context.Background(), catalog, model.RuntimeState{Observations: map[string]model.ScanObservation{}})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(result.Apps, func(i, j int) bool { return result.Apps[i].InstallPath < result.Apps[j].InstallPath })
	return result
}

func pathIdentityByCanonical(t *testing.T, apps []model.Application) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, app := range apps {
		result[mustCanonicalExecutablePath(t, app.InstallPath)] = app.Identity
	}
	return result
}
