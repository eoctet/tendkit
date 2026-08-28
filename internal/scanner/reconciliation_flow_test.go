package scanner

import (
	"context"

	"path/filepath"
	"reflect"

	"testing"

	"github.com/eoctet/tendkit/pkg/i18n"
	"os"

	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/handler"
)

func TestScannerReconciliationFlow(t *testing.T) {
	t.Run("canonical-owned-proposal-preserves-owner-actions-without-baseline", func(t *testing.T) {
		canonical := model.Application{Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease, Actions: &model.ProviderActions{Version: "path --version"}}}
		owner := model.Application{Provider: model.ProviderConfig{Type: model.ProviderDefault, Actions: &model.ProviderActions{Version: "owner --version", Check: "owner check", Update: "owner update"}}}
		got := canonicalOwnedProposal(canonical, owner, model.Application{}, false)
		if got.Provider.CheckAction() != "owner check" || got.Provider.UpdateAction() != "owner update" || got.Provider.VersionAction() != "path --version" {
			t.Fatalf("actions = %#v", got.Provider.Actions)
		}
	})
	t.Run("canonical-owned-proposal-merges-baseline-actions-per-capability", func(t *testing.T) {
		canonical := model.Application{Provider: model.ProviderConfig{Actions: &model.ProviderActions{Version: "path version"}}}
		owner := model.Application{Provider: model.ProviderConfig{Actions: &model.ProviderActions{Check: "owner check", Update: "owner update"}}}
		baseline := model.Application{Provider: model.ProviderConfig{Actions: &model.ProviderActions{Version: "baseline version", Check: "baseline check"}}}
		got := canonicalOwnedProposal(canonical, owner, baseline, true)
		if got.Provider.VersionAction() != "baseline version" || got.Provider.CheckAction() != "baseline check" || got.Provider.UpdateAction() != "owner update" {
			t.Fatalf("actions=%#v", got.Provider.Actions)
		}
	})
	t.Run("canonical-owned-proposal-clones-baseline-download", func(t *testing.T) {
		canonical := model.Application{Provider: model.ProviderConfig{Actions: &model.ProviderActions{Version: "path version"}}}
		owner := model.Application{Provider: model.ProviderConfig{Actions: &model.ProviderActions{Update: "owner update"}}}
		baseline := model.Application{Provider: model.ProviderConfig{Actions: &model.ProviderActions{Download: &model.Download{URL: "https://example.invalid/tool", ExtraArgs: []string{"--retry", "2"}}}}}
		got := canonicalOwnedProposal(canonical, owner, baseline, true)
		if got.Provider.Actions.Download == baseline.Provider.Actions.Download || !reflect.DeepEqual(got.Provider.Actions.Download, baseline.Provider.Actions.Download) {
			t.Fatalf("download was not value-cloned: got=%#v baseline=%#v", got.Provider.Actions.Download, baseline.Provider.Actions.Download)
		}
	})
	t.Run("reconcile-managed-installations-evidence-sources", func(t *testing.T) {
		for _, source := range []string{"go", "uv", "node", "python", "ruby"} {
			path := writeScannerFixture(t, filepath.Join(t.TempDir(), source))
			canonical := model.Application{ID: "cli-" + source, Name: source, Type: model.ApplicationTypeCLI, InstallPath: path, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease, Actions: &model.ProviderActions{Version: "v"}}}
			owner := managedOwner("pkg-"+source, source, model.ProviderDefault, source, "package:"+source+":"+source, source, path, model.ModeAuto, "1")
			session := scanSession{discovered: []model.Application{canonical}, observed: map[string]model.ManagedStatus{}, packages: packageScanResult{Complete: map[string]bool{source: true}}, installationDiscoveries: []discovery{owner}}
			session.reconcileManagedInstallations()
			if len(session.discovered) != 1 || session.discovered[0].ID != canonical.ID || session.discovered[0].Package != source {
				t.Errorf("%s result=%#v", source, session.discovered)
			}
		}
	})
	t.Run("reconcile-managed-installations-incomplete-evidence-sources-hold-baseline", func(t *testing.T) {
		for _, source := range []string{"go", "uv", "node", "python", "ruby"} {
			path := writeScannerFixture(t, filepath.Join(t.TempDir(), source))
			previous := model.ManagedStatus{CurrentVersion: "1.2.3", UpdateStatus: model.StatusCurrent}
			baseline := model.Application{ID: "cli-" + source, Name: source, Type: model.ApplicationTypeCLI, InstallPath: path, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Identity: "cli:" + source, StatusManaged: previous}
			owner := managedOwner("pkg-"+source, source, model.ProviderDefault, source, "package:"+source+":"+source, source, path, model.ModeAuto, "1")
			session := scanSession{catalog: model.Config{Apps: []model.Application{baseline}}, discovered: []model.Application{baseline}, observed: map[string]model.ManagedStatus{baseline.ID: previous}, packages: packageScanResult{Complete: map[string]bool{source: false}}, installationDiscoveries: []discovery{owner}}
			session.reconcileManagedInstallations()
			got, found := catalogApplicationByID(session.catalog.Apps, baseline.ID)
			if !found {
				t.Errorf("%s baseline disappeared from catalog: %#v", source, session.catalog.Apps)
				continue
			}
			if got.Provider.Type != model.ProviderGitHubRelease || got.Identity != baseline.Identity {
				t.Errorf("%s baseline migrated: %#v", source, got)
			}
			if got.StatusManaged != previous || applicationByID(t, session.discovered, baseline.ID).StatusManaged != previous || session.observed[baseline.ID] != previous || session.observed[baseline.ID].UpdateStatus == model.StatusMissing {
				t.Errorf("%s incomplete inventory changed baseline state: catalog=%#v discovered=%#v observed=%#v", source, got.StatusManaged, applicationByID(t, session.discovered, baseline.ID).StatusManaged, session.observed[baseline.ID])
			}
		}
	})
	t.Run("reconcile-managed-installations-incomplete-evidence-sources-keep-new-canonical", func(t *testing.T) {
		for _, source := range []string{"go", "uv", "node", "python", "ruby"} {
			path := writeScannerFixture(t, filepath.Join(t.TempDir(), source))
			status := model.ManagedStatus{CurrentVersion: "1.2.3", UpdateStatus: model.StatusCurrent}
			canonical := model.Application{ID: "cli-" + source, Name: source, Type: model.ApplicationTypeCLI, InstallPath: path, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Identity: "cli:" + source, StatusManaged: status}
			owner := managedOwner("pkg-"+source, source, model.ProviderDefault, source, "package:"+source+":"+source, source, path, model.ModeAuto, "1")
			session := scanSession{discovered: []model.Application{canonical}, observed: map[string]model.ManagedStatus{canonical.ID: status}, packages: packageScanResult{Complete: map[string]bool{source: false}}, installationDiscoveries: []discovery{owner}}

			session.reconcileManagedInstallations()

			if len(session.discovered) != 1 || session.discovered[0].ID != canonical.ID {
				t.Errorf("%s incomplete inventory replaced the canonical discovery: %#v", source, session.discovered)
				continue
			}
			if got := session.discovered[0]; got.Provider.Type != canonical.Provider.Type || got.Identity != canonical.Identity || got.StatusManaged != status {
				t.Errorf("%s incomplete inventory migrated the canonical discovery: %#v", source, got)
			}
			if session.observed[canonical.ID] != status {
				t.Errorf("%s incomplete inventory changed the canonical observation: %#v", source, session.observed)
			}
		}
	})
	t.Run("reconcile-managed-installations-incomplete-evidence-source-keeps-independent-owner-healthy", func(t *testing.T) {
		path := writeScannerFixture(t, filepath.Join(t.TempDir(), "node"))
		status := model.ManagedStatus{CurrentVersion: "1.2.3", UpdateStatus: model.StatusCurrent}
		owner := managedOwner("pkg-node-tool", "tool", model.ProviderNPM, "tool", "package:node:tool", "node", path, model.ModeAuto, status.CurrentVersion)
		owner.State = status
		session := scanSession{observed: map[string]model.ManagedStatus{}, packages: packageScanResult{Complete: map[string]bool{"node": false}}, installationDiscoveries: []discovery{owner}}

		session.reconcileManagedInstallations()

		if len(session.discovered) != 1 || session.discovered[0].ID != owner.App.ID {
			t.Fatalf("independent owner disappeared from incomplete inventory: %#v", session.discovered)
		}
		if got := session.observed[owner.App.ID]; got != status {
			t.Fatalf("independent owner was turned into an installation reconciliation conflict: %#v", got)
		}
	})
	t.Run("reconcile-managed-installations-complete-conflict-keeps-failed-owners-visible", func(t *testing.T) {
		path := writeScannerFixture(t, filepath.Join(t.TempDir(), "node"))
		canonical := model.Application{ID: "cli-node", Name: "node", Type: model.ApplicationTypeCLI, InstallPath: path, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Identity: "cli:node"}
		first := managedOwner("pkg-node-first", "first", model.ProviderNPM, "first", "package:node:first", "node", path, model.ModeAuto, "1")
		second := managedOwner("pkg-node-second", "second", model.ProviderNPM, "second", "package:node:second", "node", path, model.ModeAuto, "1")
		session := scanSession{discovered: []model.Application{canonical}, observed: map[string]model.ManagedStatus{}, packages: packageScanResult{Complete: map[string]bool{"node": true}}, installationDiscoveries: []discovery{first, second}}

		session.reconcileManagedInstallations()

		if len(session.discovered) != 2 {
			t.Fatalf("complete installation reconciliation conflict disappeared: %#v", session.discovered)
		}
		for _, id := range []string{first.App.ID, second.App.ID} {
			applicationByID(t, session.discovered, id)
			if got := session.observed[id]; got.UpdateStatus != model.StatusFailed || got.Error == "" {
				t.Fatalf("conflicting owner %s is not visible as failed: %#v", id, got)
			}
		}
	})
	t.Run("reconcile-managed-installations-keeps-displaced-owner-independent", func(t *testing.T) {
		dir := t.TempDir()
		homebrewPath := filepath.Join(dir, "homebrew", "bin", "rg")
		cargoPath := filepath.Join(dir, "cargo", "bin", "rg")
		for _, path := range []string{homebrewPath, cargoPath} {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		canonical := model.Application{ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: cargoPath, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease, Actions: &model.ProviderActions{Version: "rg --version"}}}
		homebrew := discovery{App: model.Application{ID: "pkg-homebrew-formula-ripgrep", Name: "ripgrep", Type: model.ApplicationTypePackage, InstallPath: homebrewPath, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderHomebrew}, Package: "formula/ripgrep", Identity: "package:homebrew-formula:ripgrep", UpdateMode: model.ModeAuto}, Evidence: &handler.InstallationEvidence{Source: "homebrew-formula", ExecutablePaths: []string{homebrewPath}}}
		cargo := discovery{App: model.Application{ID: "pkg-cargo-ripgrep", Name: "ripgrep", Type: model.ApplicationTypePackage, InstallPath: cargoPath, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderCargo}, Package: "ripgrep", Identity: "package:cargo:ripgrep", UpdateMode: model.ModeCheck}, Evidence: &handler.InstallationEvidence{Source: "cargo", ExecutablePaths: []string{cargoPath}}}
		session := scanSession{discovered: []model.Application{canonical}, observed: map[string]model.ManagedStatus{}, packages: packageScanResult{Complete: map[string]bool{"homebrew-formula": true, "cargo": true}}, installationDiscoveries: []discovery{homebrew, cargo}}
		session.reconcileManagedInstallations()
		if len(session.discovered) != 2 {
			t.Fatalf("discoveries=%#v", session.discovered)
		}
		if got := session.discovered[0]; got.ID != "cli-ripgrep" || got.Provider.Type != model.ProviderCargo || got.Identity != "package:cargo:ripgrep" || got.UpdateMode != model.ModeCheck {
			t.Fatalf("canonical=%#v", got)
		} else if got.Provider.VersionAction() != "rg --version" {
			t.Fatalf("new canonical lost PATH version action: %#v", got.Provider.Actions)
		}
		if got := session.discovered[1]; got.ID != "pkg-homebrew-formula-ripgrep" || got.Provider.Type != model.ProviderHomebrew {
			t.Fatalf("independent owner=%#v", got)
		}
	})
	t.Run("reconcile-managed-installations-keeps-existing-standalone-cli-separate-from-homebrew", func(t *testing.T) {
		dir := t.TempDir()
		standalonePath := writeScannerFixture(t, filepath.Join(dir, "standalone", "rg"))
		homebrewPath := writeScannerFixture(t, filepath.Join(dir, "homebrew", "Cellar", "ripgrep", "15.2.0", "bin", "rg"))
		homebrewLink := filepath.Join(dir, "homebrew", "bin", "rg")
		if err := os.MkdirAll(filepath.Dir(homebrewLink), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(homebrewPath, homebrewLink); err != nil {
			t.Fatal(err)
		}
		standalone := model.Application{
			ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI,
			InstallPath: standalonePath, ScanManaged: true, Identity: "cli:ripgrep",
			Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}, Package: "BurntSushi/ripgrep",
		}
		pathDiscovery := cloneApplication(standalone)
		pathDiscovery.InstallPath = homebrewLink
		homebrew := managedOwner(
			"pkg-homebrew-formula-ripgrep", "ripgrep", model.ProviderHomebrew,
			"formula/ripgrep", "package:homebrew-formula:ripgrep", "homebrew-formula",
			homebrewPath, model.ModeAuto, "15.2.0",
		)
		session := scanSession{
			catalog:                 model.Config{Apps: []model.Application{standalone}},
			discovered:              []model.Application{pathDiscovery},
			observed:                map[string]model.ManagedStatus{standalone.ID: {CurrentVersion: "15.2.0"}},
			packages:                packageScanResult{Complete: map[string]bool{"homebrew-formula": true}},
			installationDiscoveries: []discovery{homebrew},
		}

		session.reconcileManagedInstallations()

		kept := applicationByID(t, session.catalog.Apps, standalone.ID)
		if kept.InstallPath != standalonePath || kept.Provider.Type != model.ProviderGitHubRelease || kept.Identity != "cli:ripgrep" {
			t.Fatalf("standalone installation was overwritten: %#v", kept)
		}
		brew := applicationByID(t, session.discovered, homebrew.App.ID)
		if brew.InstallPath != homebrewPath || brew.Provider.Type != model.ProviderHomebrew || brew.Identity != "package:homebrew-formula:ripgrep" {
			t.Fatalf("Homebrew installation was not kept independently: %#v", brew)
		}
		catalog, _, err := session.finalize(context.Background())
		if err != nil || len(catalog.Apps) != 2 {
			t.Fatalf("finalized applications=%#v error=%v", catalog.Apps, err)
		}
		if got := applicationByID(t, catalog.Apps, standalone.ID); got.InstallPath != standalonePath || got.Provider.Type != model.ProviderGitHubRelease {
			t.Fatalf("finalized standalone installation=%#v", got)
		}
		if got := applicationByID(t, catalog.Apps, homebrew.App.ID); got.InstallPath != homebrewPath || got.Provider.Type != model.ProviderHomebrew {
			t.Fatalf("finalized Homebrew installation=%#v", got)
		}
	})
	t.Run("reconcile-managed-installations-uses-stable-identity-across-multiple-groups", func(t *testing.T) {
		dir := t.TempDir()
		standalonePath := writeScannerFixture(t, filepath.Join(dir, "standalone", "rg"))
		homebrewPath := writeScannerFixture(t, filepath.Join(dir, "homebrew", "Cellar", "ripgrep", "15.2.0", "bin", "rg"))
		homebrewLink := filepath.Join(dir, "homebrew", "bin", "rg")
		if err := os.MkdirAll(filepath.Dir(homebrewLink), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(homebrewPath, homebrewLink); err != nil {
			t.Fatal(err)
		}
		cargoPath := writeScannerFixture(t, filepath.Join(dir, "cargo", "bin", "tool"))
		standalone := model.Application{ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: standalonePath, ScanManaged: true, Identity: "cli:ripgrep", Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}}
		displaced := cloneApplication(standalone)
		displaced.InstallPath = homebrewLink
		canonical := model.Application{ID: "cli-tool", Name: "tool", Type: model.ApplicationTypeCLI, InstallPath: cargoPath, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}}
		homebrew := managedOwner("pkg-homebrew-formula-ripgrep", "ripgrep", model.ProviderHomebrew, "formula/ripgrep", "package:homebrew-formula:ripgrep", "homebrew-formula", homebrewPath, model.ModeAuto, "15.2.0")
		cargo := managedOwner("pkg-cargo-tool", "tool", model.ProviderCargo, "tool", "package:cargo:tool", "cargo", cargoPath, model.ModeCheck, "1.0.0")
		session := scanSession{
			catalog:                 model.Config{Apps: []model.Application{standalone}},
			discovered:              []model.Application{displaced, canonical},
			observed:                map[string]model.ManagedStatus{standalone.ID: {CurrentVersion: "15.2.0"}, canonical.ID: {CurrentVersion: "1.0.0"}},
			packages:                packageScanResult{Complete: map[string]bool{"homebrew-formula": true, "cargo": true}},
			installationDiscoveries: []discovery{homebrew, cargo},
		}

		session.reconcileManagedInstallations()

		independent := applicationByID(t, session.discovered, homebrew.App.ID)
		if independent.Provider.Type != model.ProviderHomebrew || independent.InstallPath != homebrewPath {
			t.Fatalf("displaced owner was overwritten: %#v", independent)
		}
		merged := applicationByID(t, session.discovered, canonical.ID)
		if merged.Provider.Type != model.ProviderCargo || merged.Package != cargo.App.Package || merged.Identity != cargo.App.Identity {
			t.Fatalf("later reconciliation group was not merged by stable identity: %#v", merged)
		}
	})
	t.Run("reconcile-managed-installations-switches-canonical-owner-atomically-in-both-directions", func(t *testing.T) {
		dir := t.TempDir()
		brewPath := writeScannerFixture(t, filepath.Join(dir, "brew", "bin", "rg"))
		cargoPath := writeScannerFixture(t, filepath.Join(dir, "cargo", "bin", "rg"))
		owners := map[string]discovery{
			"homebrew": managedOwner("pkg-homebrew-formula-ripgrep", "ripgrep", model.ProviderHomebrew, "formula/ripgrep", "package:homebrew-formula:ripgrep", "homebrew-formula", brewPath, model.ModeAuto, "14.1.0"),
			"cargo":    managedOwner("pkg-cargo-ripgrep", "ripgrep", model.ProviderCargo, "ripgrep", "package:cargo:ripgrep", "cargo", cargoPath, model.ModeCheck, "14.2.0"),
		}
		for _, test := range []struct{ name, before, after string }{{"homebrew-to-cargo", "homebrew", "cargo"}, {"cargo-to-homebrew", "cargo", "homebrew"}} {
			t.Run(test.name, func(t *testing.T) {
				before, after := owners[test.before], owners[test.after]
				canonical := model.Application{ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: before.App.InstallPath, Enabled: true, ScanManaged: true, UpdateMode: before.App.UpdateMode, Provider: before.App.Provider, Package: before.App.Package, Identity: before.App.Identity, StatusManaged: model.ManagedStatus{CurrentVersion: "old", FirstDetectedTime: "2026-01-01"}}
				independent := before
				independent.App = cloneApplication(before.App)
				independent.App.ID = after.App.ID
				independent.App.Provider, independent.App.Package, independent.App.Identity, independent.App.InstallPath, independent.App.UpdateMode = after.App.Provider, after.App.Package, after.App.Identity, after.App.InstallPath, after.App.UpdateMode
				session := scanSession{
					catalog:                 model.Config{Apps: []model.Application{canonical, independent.App}, ScanVersionControl: map[string]map[string]model.ScanKeepResolution{canonical.ID: {"description": {}}, independent.App.ID: {"package": {}}}},
					state:                   model.RuntimeState{Observations: map[string]model.ScanObservation{canonical.ID: {Found: true}, independent.App.ID: {Found: true}}},
					discovered:              []model.Application{{ID: canonical.ID, Name: canonical.Name, Type: canonical.Type, InstallPath: after.App.InstallPath, Enabled: true, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease, Actions: &model.ProviderActions{Version: "rg --version"}}}},
					observed:                map[string]model.ManagedStatus{canonical.ID: {CurrentVersion: "path-version", UpdateStatus: model.StatusUnchecked}},
					packages:                packageScanResult{Complete: map[string]bool{"homebrew-formula": true, "cargo": true}},
					installationDiscoveries: []discovery{owners["homebrew"], owners["cargo"]},
				}
				session.reconcileManagedInstallations()
				got := applicationByID(t, session.catalog.Apps, canonical.ID)
				if got.Provider.Type != after.App.Provider.Type || got.Package != after.App.Package || got.Identity != after.App.Identity || got.InstallPath != after.App.InstallPath || got.UpdateMode != before.App.UpdateMode {
					t.Fatalf("canonical switch was not atomic: %#v", got)
				}
				if got.Provider.VersionAction() != "rg --version" {
					t.Fatalf("PATH version action was lost: %#v", got.Provider.Actions)
				}
				if _, found := session.catalog.ScanVersionControl[independent.App.ID]; found {
					t.Fatalf("absorbed keep survived for %s", independent.App.ID)
				}
				if _, found := session.state.Observations[independent.App.ID]; found {
					t.Fatalf("absorbed observation survived for %s", independent.App.ID)
				}
				oldIndependent := applicationByID(t, session.discovered, before.App.ID)
				if oldIndependent.Type != model.ApplicationTypePackage || oldIndependent.InstallPath != before.App.InstallPath {
					t.Fatalf("displaced owner not retained independently: %#v", oldIndependent)
				}
			})
		}
	})
	t.Run("reconcile-managed-installations-holds-whole-group-on-incomplete-ambiguous-or-broken-evidence", func(t *testing.T) {
		dir := t.TempDir()
		brewPath := writeScannerFixture(t, filepath.Join(dir, "brew", "bin", "rg"))
		cargoPath := writeScannerFixture(t, filepath.Join(dir, "cargo", "bin", "rg"))
		baseline := model.Application{ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: brewPath, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderHomebrew}, Package: "formula/ripgrep", Identity: "package:homebrew-formula:ripgrep", StatusManaged: model.ManagedStatus{CurrentVersion: "baseline", UpdateStatus: model.StatusCurrent}}
		for _, test := range []struct {
			name     string
			complete map[string]bool
			owners   []discovery
			visible  bool
		}{
			{"incomplete-baseline-owner", map[string]bool{"homebrew-formula": false, "cargo": true}, []discovery{managedOwner("pkg-cargo-ripgrep", "ripgrep", model.ProviderCargo, "ripgrep", "package:cargo:ripgrep", "cargo", cargoPath, model.ModeCheck, "new")}, false},
			{"two-owners-one-path", map[string]bool{"homebrew-formula": true, "cargo": true}, []discovery{managedOwner("pkg-homebrew-formula-ripgrep", "ripgrep", model.ProviderHomebrew, "formula/ripgrep", "package:homebrew-formula:ripgrep", "homebrew-formula", cargoPath, model.ModeAuto, "new"), managedOwner("pkg-cargo-ripgrep", "ripgrep", model.ProviderCargo, "ripgrep", "package:cargo:ripgrep", "cargo", cargoPath, model.ModeCheck, "new")}, true},
			{"broken-symlink", map[string]bool{"homebrew-formula": true, "cargo": true}, []discovery{managedOwner("pkg-cargo-ripgrep", "ripgrep", model.ProviderCargo, "ripgrep", "package:cargo:ripgrep", "cargo", filepath.Join(dir, "missing", "rg"), model.ModeCheck, "new")}, true},
		} {
			t.Run(test.name, func(t *testing.T) {
				session := scanSession{catalog: model.Config{Apps: []model.Application{baseline}, ScanVersionControl: map[string]map[string]model.ScanKeepResolution{baseline.ID: {"provider": {}}}}, state: model.RuntimeState{Observations: map[string]model.ScanObservation{baseline.ID: {Found: true, Path: brewPath}}}, discovered: []model.Application{{ID: baseline.ID, Name: baseline.Name, Type: baseline.Type, InstallPath: cargoPath, ScanManaged: true}}, observed: map[string]model.ManagedStatus{baseline.ID: {CurrentVersion: "candidate", UpdateStatus: model.StatusUnchecked}}, packages: packageScanResult{Complete: test.complete}, installationDiscoveries: test.owners}
				session.reconcileManagedInstallations()
				if got := applicationByID(t, session.catalog.Apps, baseline.ID); got.Provider.Type != model.ProviderHomebrew || got.InstallPath != brewPath || got.StatusManaged.CurrentVersion != "baseline" {
					t.Fatalf("baseline changed in failed group: %#v", got)
				}
				if got := session.observed[baseline.ID]; got.CurrentVersion != "baseline" || got.UpdateStatus != model.StatusCurrent {
					t.Fatalf("baseline status not restored: %#v", got)
				} else if test.visible && (!strings.Contains(got.Error, i18n.T("scanner.install_recon_conflict_label")) || !strings.Contains(got.Error, "ripgrep")) {
					t.Fatalf("installation reconciliation conflict was not user-visible: %#v", got)
				} else if !test.visible && got.Error != "" {
					t.Fatalf("incomplete inventory changed observed error: %#v", got)
				}
				for _, found := range session.discovered {
					if !test.visible && found.ID == baseline.ID {
						if found.StatusManaged != baseline.StatusManaged {
							t.Fatalf("incomplete inventory changed discovered baseline: %#v", found)
						}
						continue
					}
					if found.ID == baseline.ID || found.Name == "ripgrep" {
						t.Fatalf("failed group leaked a candidate: %#v", session.discovered)
					}
				}
			})
		}
	})
	t.Run("reconcile-managed-installations-reports-broken-evidence-without-baseline", func(t *testing.T) {
		broken := managedOwner("pkg-cargo-ripgrep", "ripgrep", model.ProviderCargo, "ripgrep", "package:cargo:ripgrep", "cargo", filepath.Join(t.TempDir(), "missing", "rg"), model.ModeCheck, "14")
		session := scanSession{observed: map[string]model.ManagedStatus{}, packages: packageScanResult{Complete: map[string]bool{"cargo": true}}, installationDiscoveries: []discovery{broken}}
		session.reconcileManagedInstallations()
		got := applicationByID(t, session.discovered, broken.App.ID)
		status := session.observed[got.ID]
		if status.UpdateStatus != model.StatusFailed || !strings.Contains(status.Error, i18n.T("scanner.install_recon_conflict_label")) || !strings.Contains(status.Error, i18n.T("scanner.install_recon_conflict_claim_path")) {
			t.Fatalf("broken first-scan evidence was silent: app=%#v status=%#v", got, status)
		}
	})
	t.Run("reconcile-managed-installations-preserves-baseline-actions-and-update-mode", func(t *testing.T) {
		path := writeScannerFixture(t, filepath.Join(t.TempDir(), "cargo", "bin", "rg"))
		actions := &model.ProviderActions{Version: "user-version", Check: "user-check", Update: "user-update", Install: "user-install"}
		baseline := model.Application{ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: path, Enabled: false, ScanManaged: true, UpdateMode: model.ModeDownload, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease, Actions: actions}, Environment: map[string]string{"CUSTOM": "kept"}}
		canonical := cloneApplication(baseline)
		canonical.Provider.Actions = &model.ProviderActions{Version: "path-version"}
		canonical.Enabled = true
		canonical.Environment = nil
		owner := managedOwner("pkg-cargo-ripgrep", "ripgrep", model.ProviderCargo, "ripgrep", "package:cargo:ripgrep", "cargo", path, model.ModeCheck, "14")
		session := scanSession{catalog: model.Config{Apps: []model.Application{baseline}}, discovered: []model.Application{canonical}, observed: map[string]model.ManagedStatus{baseline.ID: {UpdateStatus: model.StatusUnchecked}}, packages: packageScanResult{Complete: map[string]bool{"cargo": true}}, installationDiscoveries: []discovery{owner}}
		session.reconcileManagedInstallations()
		got := applicationByID(t, session.catalog.Apps, baseline.ID)
		if got.UpdateMode != model.ModeDownload || got.Enabled || got.Environment["CUSTOM"] != "kept" || got.Provider.VersionAction() != "user-version" || got.Provider.CheckAction() != "user-check" || got.Provider.UpdateAction() != "user-update" || got.Provider.InstallAction() != "user-install" {
			t.Fatalf("baseline user policy/actions changed: %#v", got)
		}
	})
	t.Run("reconcile-managed-installations-protects-unmanaged-and-keeps-different-path-independent", func(t *testing.T) {
		dir := t.TempDir()
		protectedPath := writeScannerFixture(t, filepath.Join(dir, "brew", "bin", "rg"))
		otherPath := writeScannerFixture(t, filepath.Join(dir, "cargo", "bin", "rg"))
		protected := model.Application{ID: "custom-rg", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: protectedPath, Enabled: false, UpdateMode: model.ModeDownload, ScanManaged: false, Provider: providerConfig(model.ProviderDefault, "custom-version", "custom-check", "custom-update", nil), Package: "user-package", Identity: "user:rg", Environment: map[string]string{"TOKEN": "kept"}}
		brew := managedOwner("pkg-homebrew-formula-ripgrep", "ripgrep", model.ProviderHomebrew, "formula/ripgrep", "package:homebrew-formula:ripgrep", "homebrew-formula", protectedPath, model.ModeAuto, "14.1")
		cargo := managedOwner("pkg-cargo-ripgrep", "ripgrep", model.ProviderCargo, "ripgrep", "package:cargo:ripgrep", "cargo", otherPath, model.ModeCheck, "14.2")
		session := scanSession{catalog: model.Config{Apps: []model.Application{protected}}, discovered: nil, observed: map[string]model.ManagedStatus{}, packages: packageScanResult{Complete: map[string]bool{"homebrew-formula": true, "cargo": true}}, installationDiscoveries: []discovery{brew, cargo}}
		session.reconcileManagedInstallations()
		if got := applicationByID(t, session.catalog.Apps, protected.ID); got.Provider.Type != model.ProviderDefault || got.Package != "user-package" || got.Identity != "user:rg" || got.Enabled || got.Environment["TOKEN"] != "kept" || got.UpdateMode != model.ModeDownload {
			t.Fatalf("protected fields changed: %#v", got)
		}
		if got := applicationByID(t, session.discovered, cargo.App.ID); got.InstallPath != otherPath {
			t.Fatalf("different-path install was not retained: %#v", got)
		}
		for _, found := range session.discovered {
			if found.ID == brew.App.ID {
				t.Fatalf("same-path protected duplicate leaked: %#v", found)
			}
		}
	})
	t.Run("reconcile-managed-installations-merges-cask-into-canonical-application", func(t *testing.T) {
		path := writeScannerFixture(t, filepath.Join(t.TempDir(), "Visual Studio Code.app"))
		baseline := model.Application{ID: "app-visual-studio-code", Name: "Visual Studio Code", Type: model.ApplicationTypeBundle, InstallPath: path, Enabled: true, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderDefault}}
		cask := managedOwner("pkg-homebrew-cask-visual-studio-code", "Visual Studio Code", model.ProviderHomebrew, "cask/visual-studio-code", "package:homebrew-cask:visual-studio-code", "homebrew-cask", path, model.ModeAuto, "1.2.3")
		cask.Evidence.ExecutablePaths = nil
		cask.Evidence.ApplicationPaths = []string{path}
		session := scanSession{catalog: model.Config{Apps: []model.Application{baseline}}, discovered: []model.Application{baseline}, observed: map[string]model.ManagedStatus{baseline.ID: {UpdateStatus: model.StatusUnchecked}}, packages: packageScanResult{Complete: map[string]bool{"homebrew-cask": true}}, installationDiscoveries: []discovery{cask}}
		session.reconcileManagedInstallations()
		got := applicationByID(t, session.catalog.Apps, baseline.ID)
		if got.Type != model.ApplicationTypeBundle || got.Provider.Type != model.ProviderHomebrew || got.Package != "cask/visual-studio-code" || got.Identity != "package:homebrew-cask:visual-studio-code" || got.UpdateMode != model.ModeAuto {
			t.Fatalf("cask reconciliation = %#v", got)
		}
	})
	t.Run("reconcile-managed-installations-reports-ambiguous-multi-application-cask-per-group", func(t *testing.T) {
		dir := t.TempDir()
		firstPath := writeScannerFixture(t, filepath.Join(dir, "First.app"))
		secondPath := writeScannerFixture(t, filepath.Join(dir, "Second.app"))
		baseline := model.Application{ID: "app-first", Name: "First", Type: model.ApplicationTypeBundle, InstallPath: firstPath, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderDefault}, StatusManaged: model.ManagedStatus{CurrentVersion: "baseline", UpdateStatus: model.StatusCurrent}}
		owner := managedOwner("pkg-homebrew-cask-multi", "multi", model.ProviderHomebrew, "cask/multi", "package:homebrew-cask:multi", "homebrew-cask", firstPath, model.ModeCheck, "1.0")
		owner.App.Type = model.ApplicationTypeBundle
		owner.Evidence.ExecutablePaths = nil
		owner.Evidence.ApplicationPaths = []string{firstPath, secondPath}
		owner.Evidence.Ambiguity = "multiple-application-paths"
		session := scanSession{catalog: model.Config{Apps: []model.Application{baseline}}, discovered: []model.Application{baseline}, observed: map[string]model.ManagedStatus{baseline.ID: {CurrentVersion: "candidate", UpdateStatus: model.StatusUnchecked}}, packages: packageScanResult{Complete: map[string]bool{"homebrew-cask": true}}, installationDiscoveries: []discovery{owner}}
		session.reconcileManagedInstallations()
		got := applicationByID(t, session.catalog.Apps, baseline.ID)
		if got.Provider.Type != model.ProviderDefault || got.Package != "" || got.StatusManaged.CurrentVersion != "baseline" {
			t.Fatalf("ambiguous cask folded into application: %#v", got)
		}
		status := session.observed[baseline.ID]
		if status.UpdateStatus == model.StatusMissing || !strings.Contains(status.Error, i18n.T("scanner.install_recon_conflict_multiple_products")) || len(session.discovered) != 0 {
			t.Fatalf("ambiguous cask conflict was not isolated: status=%#v discovered=%#v", status, session.discovered)
		}

		firstScan := scanSession{observed: map[string]model.ManagedStatus{}, packages: packageScanResult{Complete: map[string]bool{"homebrew-cask": true}}, installationDiscoveries: []discovery{owner}}
		firstScan.reconcileManagedInstallations()
		reported := applicationByID(t, firstScan.discovered, owner.App.ID)
		if reported.Provider.Type != model.ProviderHomebrew || firstScan.observed[reported.ID].UpdateStatus != model.StatusFailed || !strings.Contains(firstScan.observed[reported.ID].Error, i18n.T("scanner.install_recon_conflict_multiple_products")) {
			t.Fatalf("first-scan ambiguity was silent: app=%#v status=%#v", reported, firstScan.observed[reported.ID])
		}
	})
	t.Run("reconcile-managed-installations-commits-unrelated-group-when-another-group-conflicts", func(t *testing.T) {
		dir := t.TempDir()
		rgPath := writeScannerFixture(t, filepath.Join(dir, "cargo", "bin", "rg"))
		jqPath := writeScannerFixture(t, filepath.Join(dir, "brew", "bin", "jq"))
		rg := model.Application{ID: "cli-ripgrep", Name: "ripgrep", Type: model.ApplicationTypeCLI, InstallPath: rgPath, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}}
		jq := model.Application{ID: "cli-jq", Name: "jq", Type: model.ApplicationTypeCLI, InstallPath: jqPath, ScanManaged: true, Provider: model.ProviderConfig{Type: model.ProviderGitHubRelease}}
		brokenRG := managedOwner("pkg-cargo-ripgrep", "ripgrep", model.ProviderCargo, "ripgrep", "package:cargo:ripgrep", "cargo", filepath.Join(dir, "missing", "rg"), model.ModeCheck, "14")
		brewJQ := managedOwner("pkg-homebrew-formula-jq", "jq", model.ProviderHomebrew, "formula/jq", "package:homebrew-formula:jq", "homebrew-formula", jqPath, model.ModeAuto, "1.7")
		session := scanSession{catalog: model.Config{Apps: []model.Application{rg, jq}}, discovered: []model.Application{rg, jq}, observed: map[string]model.ManagedStatus{rg.ID: {UpdateStatus: model.StatusUnchecked}, jq.ID: {UpdateStatus: model.StatusUnchecked}}, packages: packageScanResult{Complete: map[string]bool{"cargo": true, "homebrew-formula": true}}, installationDiscoveries: []discovery{brokenRG, brewJQ}}
		session.reconcileManagedInstallations()
		if got := applicationByID(t, session.catalog.Apps, rg.ID); got.Provider.Type != model.ProviderGitHubRelease {
			t.Fatalf("conflicted group changed: %#v", got)
		}
		if got := applicationByID(t, session.catalog.Apps, jq.ID); got.Provider.Type != model.ProviderHomebrew || got.Package != "formula/jq" {
			t.Fatalf("unrelated valid group did not commit: %#v", got)
		}
	})
	t.Run("reconcile-managed-installations-holds-owner-that-maps-to-multiple-products", func(t *testing.T) {
		dir := t.TempDir()
		firstPath := writeScannerFixture(t, filepath.Join(dir, "bin", "tool-a"))
		secondPath := writeScannerFixture(t, filepath.Join(dir, "bin", "tool-b"))
		first := model.Application{ID: "cli-tool-a", Name: "tool", Type: model.ApplicationTypeCLI, InstallPath: firstPath, ScanManaged: true, StatusManaged: model.ManagedStatus{CurrentVersion: "a", UpdateStatus: model.StatusCurrent}}
		second := model.Application{ID: "cli-tool-b", Name: "tool", Type: model.ApplicationTypeCLI, InstallPath: secondPath, ScanManaged: true, StatusManaged: model.ManagedStatus{CurrentVersion: "b", UpdateStatus: model.StatusCurrent}}
		owner := managedOwner("pkg-cargo-tool", "tool", model.ProviderCargo, "tool", "package:cargo:tool", "cargo", firstPath, model.ModeCheck, "new")
		owner.Evidence.ExecutablePaths = []string{firstPath, secondPath}
		session := scanSession{catalog: model.Config{Apps: []model.Application{first, second}}, discovered: []model.Application{first, second}, observed: map[string]model.ManagedStatus{first.ID: {CurrentVersion: "candidate"}, second.ID: {CurrentVersion: "candidate"}}, packages: packageScanResult{Complete: map[string]bool{"cargo": true}}, installationDiscoveries: []discovery{owner}}
		session.reconcileManagedInstallations()
		if session.observed[first.ID].CurrentVersion != "a" || session.observed[second.ID].CurrentVersion != "b" || len(session.discovered) != 0 {
			t.Fatalf("multi-product owner was partially applied: observed=%#v discovered=%#v", session.observed, session.discovered)
		}
	})
	t.Run("scan-enabled-for-managed-homebrew-and-cargo", func(t *testing.T) {
		enabled, disabled := true, false
		settings := model.ScanSettings{Path: disabled, Packages: model.PackageScanSettings{HomebrewFormula: enabled, HomebrewCask: enabled, Cargo: enabled}}
		for _, identity := range []string{"package:homebrew-formula:ripgrep", "package:homebrew-cask:visual-studio-code", "package:cargo:ripgrep"} {
			if !scanEnabledFor(model.Application{Identity: identity}, settings) {
				t.Fatalf("%s was not enabled", identity)
			}
		}
	})
	t.Run("existing-index-does-not-match-package-across-providers-by-name", func(t *testing.T) {
		index := indexApps([]model.Application{{
			ID: "pkg-python-tavily-cli", Name: "tavily-cli", Type: model.ApplicationTypePackage,
			Package: "tavily-cli", Provider: model.ProviderConfig{Type: model.ProviderPyPI},
		}})
		uvPackage := model.Application{
			ID: "pkg-uv-tavily-cli", Name: "tavily-cli", Type: model.ApplicationTypePackage,
			Package: "tavily-cli", Provider: model.ProviderConfig{Type: model.ProviderUV},
		}
		if got := index.match(uvPackage); got != "" {
			t.Fatalf("UV package matched existing PyPI package %q", got)
		}

		index = indexApps([]model.Application{{
			ID: "manual-uv-tavily-cli", Name: "tavily-cli", Type: model.ApplicationTypePackage,
			Provider: model.ProviderConfig{Type: model.ProviderUV},
		}})
		if got := index.match(uvPackage); got != "manual-uv-tavily-cli" {
			t.Fatalf("UV package did not match same-provider configuration without package: %q", got)
		}
	})
	t.Run("existing-index-fails-closed-on-normalized-package-identity-collision", func(t *testing.T) {
		for _, values := range [][]string{{"foo.bar", "foobar"}} {
			apps := []model.Application{
				{ID: "first", Name: values[0], Type: model.ApplicationTypePackage, Package: values[0], Provider: model.ProviderConfig{Type: model.ProviderPyPI}},
				{ID: "second", Name: values[1], Type: model.ApplicationTypePackage, Package: values[1], Provider: model.ProviderConfig{Type: model.ProviderPyPI}},
			}
			index := indexApps(apps)
			if got := index.match(apps[0]); got != "" {
				t.Fatalf("%q/%q collision matched %q", values[0], values[1], got)
			}
		}
		if model.PackageIdentity("python", "foo_bar") == model.PackageIdentity("python", "foo-bar") {
			t.Fatal("underscore deletion and an existing hyphen unexpectedly collided")
		}
	})
	t.Run("path-instances-canonicalize-deduplicate-and-keep-fingerprints-order-independent", func(t *testing.T) {
		directory := t.TempDir()
		first := writeScannerFixture(t, filepath.Join(directory, "first", "git"))
		second := writeScannerFixture(t, filepath.Join(directory, "second", "git"))
		link := filepath.Join(directory, "linked-git")
		if err := os.Symlink(first, link); err != nil {
			t.Fatal(err)
		}
		candidate := func(path string) handler.Candidate {
			return handler.Candidate{Application: model.Application{ID: handler.PathApplicationID("git"), Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: path, ScanManaged: true}}
		}
		single, err := assignPathInstances("git", []handler.Candidate{candidate(first), candidate(link)}, nil)
		if err != nil || len(single.Candidates) != 1 || single.Candidates[0].Application.ID != handler.PathApplicationID("git") || single.Candidates[0].Application.Identity != "cli:git" {
			t.Fatalf("single canonical instance=%#v err=%v", single, err)
		}
		byPath := func(values []handler.Candidate) map[string]model.Application {
			assigned, assignmentErr := assignPathInstances("git", values, nil)
			if assignmentErr != nil {
				t.Fatal(assignmentErr)
			}
			result := map[string]model.Application{}
			for _, value := range assigned.Candidates {
				canonical, canonicalErr := handler.CanonicalExecutablePath(value.Application.InstallPath)
				if canonicalErr != nil {
					t.Fatal(canonicalErr)
				}
				result[canonical] = value.Application
			}
			return result
		}
		forward, reverse := byPath([]handler.Candidate{candidate(first), candidate(second)}), byPath([]handler.Candidate{candidate(second), candidate(link)})
		if len(forward) != 2 || len(reverse) != 2 {
			t.Fatalf("PATH order or symlink changed assignments: forward=%#v reverse=%#v", forward, reverse)
		}
		for canonical, application := range forward {
			other, found := reverse[canonical]
			if !found || application.ID != other.ID || application.Identity != other.Identity {
				t.Fatalf("PATH order changed stable assignment for %q: %#v / %#v", canonical, application, other)
			}
		}
		for path, application := range forward {
			fingerprint := pathInstanceFingerprint(path)
			if application.ID != handler.PathApplicationID("git")+"-"+fingerprint || application.Identity != "cli:git@"+fingerprint {
				t.Fatalf("unstable path fingerprint for %q: %#v", path, application)
			}
		}
	})
	t.Run("path-instances-migrate-history-by-exact-canonical-owner-and-fail-closed-on-conflict", func(t *testing.T) {
		directory := t.TempDir()
		first := writeScannerFixture(t, filepath.Join(directory, "first", "git"))
		second := writeScannerFixture(t, filepath.Join(directory, "second", "git"))
		candidate := func(path string) handler.Candidate {
			return handler.Candidate{Application: model.Application{ID: handler.PathApplicationID("git"), Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: path, ScanManaged: true}}
		}
		oldID := handler.PathApplicationID("git")
		history := model.Application{ID: oldID, Identity: "cli:git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: first, ScanManaged: true}
		assignment, err := assignPathInstances("git", []handler.Candidate{candidate(first), candidate(second)}, []model.Application{history})
		if err != nil {
			t.Fatal(err)
		}
		canonicalFirst, canonicalErr := handler.CanonicalExecutablePath(first)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		fingerprint := pathInstanceFingerprint(canonicalFirst)
		migratedID := oldID + "-" + fingerprint
		if assignment.Migrations[oldID] != migratedID || assignment.IdentityMigrations[oldID] != "cli:git@"+fingerprint {
			t.Fatalf("historical migration=%#v", assignment)
		}
		if !newExclusionMatcher([]string{migratedID}).excluded(model.Application{ID: migratedID, Identity: assignment.IdentityMigrations[oldID], InstallPath: first}) {
			t.Fatal("exclusion did not follow the migrated stable ID")
		}
		conflict := model.Application{ID: migratedID, Identity: "manual:git", Name: "manual", Type: model.ApplicationTypeCLI, InstallPath: writeScannerFixture(t, filepath.Join(directory, "other", "git")), ScanManaged: true}
		if _, err := assignPathInstances("git", []handler.Candidate{candidate(first), candidate(second)}, []model.Application{history, conflict}); err == nil {
			t.Fatal("migration with an unrelated exact-ID owner was accepted")
		}
	})
	t.Run("path-instance-migration-moves-runtime-and-version-control-but-keeps-package-identity", func(t *testing.T) {
		path := writeScannerFixture(t, filepath.Join(t.TempDir(), "bin", "git"))
		oldID := handler.PathApplicationID("git")
		history := model.Application{ID: oldID, Identity: "package:node:git-wrapper", Name: "Git", Type: model.ApplicationTypePackage, InstallPath: path, ScanManaged: true}
		assignment, err := assignPathInstances("git", []handler.Candidate{{Application: model.Application{ID: oldID, Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: path, ScanManaged: true}}}, []model.Application{history})
		if err != nil {
			t.Fatal(err)
		}
		newID := oldID + "-" + pathInstanceFingerprint(mustCanonicalPath(t, path))
		// A single live instance keeps the base ID, so create the real migration
		// shape used when a second instance causes the historical entry to split.
		assignment.Migrations = map[string]string{oldID: newID}
		assignment.IdentityMigrations = map[string]string{}
		keep := map[string]map[string]model.ScanKeepResolution{oldID: {"version": {Fingerprint: "kept"}}}
		observation := model.ScanObservation{Found: true}
		session := scanSession{catalog: model.Config{Apps: []model.Application{history}, ScanVersionControl: keep}, state: model.RuntimeState{Observations: map[string]model.ScanObservation{oldID: observation}}}
		session.applyPathInstanceMigrations(assignment)
		if got := session.catalog.Apps[0]; got.ID != newID || got.Identity != history.Identity {
			t.Fatalf("package history identity was changed by migration: %#v", got)
		}
		if got, exists := session.state.Observations[newID]; !exists || got != observation || session.state.Observations[oldID].Found {
			t.Fatalf("runtime observation did not move atomically: %#v", session.state.Observations)
		}
		if _, exists := session.catalog.ScanVersionControl[oldID]; exists || session.catalog.ScanVersionControl[newID]["version"].Fingerprint != "kept" {
			t.Fatalf("version-control migration=%#v", session.catalog.ScanVersionControl)
		}
	})
	t.Run("path-instance-single-replacement-keeps-base-id-and-retains-disappeared-history-as-missing", func(t *testing.T) {
		directory := t.TempDir()
		oldPath := filepath.Join(directory, "old", "git")
		newPath := writeScannerFixture(t, filepath.Join(directory, "new", "git"))
		oldID := handler.PathApplicationID("git")
		history := model.Application{ID: oldID, Identity: "package:node:git-wrapper", Name: "Git", Type: model.ApplicationTypePackage, InstallPath: oldPath, ScanManaged: true, StatusManaged: model.ManagedStatus{CurrentVersion: "old", UpdateStatus: model.StatusCurrent}}
		assignment, err := assignPathInstances("git", []handler.Candidate{{Application: model.Application{ID: oldID, Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: newPath, ScanManaged: true}}}, []model.Application{history})
		if err != nil {
			t.Fatal(err)
		}
		if len(assignment.Candidates) != 1 || assignment.Candidates[0].Application.ID != oldID {
			t.Fatalf("single replacement did not restore base ID ownership: %#v", assignment)
		}
		oldFingerprint := pathInstanceFingerprint(filepath.Clean(oldPath))
		extendedOldID := oldID + "-" + oldFingerprint
		if assignment.Migrations[oldID] != extendedOldID {
			t.Fatalf("disappeared historical instance migration=%#v", assignment.Migrations)
		}
		session := scanSession{catalog: model.Config{Apps: []model.Application{history}}, observed: map[string]model.ManagedStatus{}}
		session.applyPathInstanceMigrations(assignment)
		if got := session.catalog.Apps[0]; got.ID != extendedOldID || session.observed[extendedOldID].UpdateStatus != model.StatusMissing {
			t.Fatalf("disappeared history was not retained as missing: app=%#v observed=%#v", got, session.observed)
		}
	})
	t.Run("path-instance-identity-migration-target-conflict-fails-closed", func(t *testing.T) {
		first := writeScannerFixture(t, filepath.Join(t.TempDir(), "first", "git"))
		second := writeScannerFixture(t, filepath.Join(t.TempDir(), "second", "git"))
		oldID := handler.PathApplicationID("git")
		fingerprint := pathInstanceFingerprint(mustCanonicalPath(t, first))
		history := model.Application{ID: oldID, Identity: "cli:git", Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: first, ScanManaged: true}
		conflict := model.Application{ID: "manual", Identity: "cli:git@" + fingerprint, Name: "manual", Type: model.ApplicationTypeCLI, InstallPath: second, ScanManaged: true}
		candidates := []handler.Candidate{{Application: model.Application{ID: oldID, Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: first, ScanManaged: true}}, {Application: model.Application{ID: oldID, Name: "Git", Type: model.ApplicationTypeCLI, InstallPath: second, ScanManaged: true}}}
		if _, err := assignPathInstances("git", candidates, []model.Application{history, conflict}); err == nil {
			t.Fatal("identity migration target conflict was accepted")
		}
	})
}

func mustCanonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := handler.CanonicalExecutablePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
