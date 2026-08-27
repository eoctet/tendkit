package scanner

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/pkg/i18n"
)

// canonicalProposal is a PATH or bundle candidate reduced to its ownership path.
type canonicalProposal struct {
	discoveryIndex int
	app            model.Application
	path           string
	pathOK         bool
}

// independentOwner is a package-manager claim kept separate until canonical
// ownership is proven unique.
type independentOwner struct {
	found  discovery
	paths  []string
	pathOK bool
}

type reconciliationConflict struct {
	group  int
	reason string
}

// managedReconciliationPlan is deliberately explicit about every class of
// mutation. Planning and validation only populate this value; catalog, status,
// observations, and keep records change together in commitManagedReconciliation.
type managedReconciliationPlan struct {
	canonicalProposals []canonicalProposal
	independentOwners  []independentOwner
	baselineApps       []model.Application
	absorbedIDs        map[string]string
	heldBaselineIDs    map[string]bool
	conflicts          []reconciliationConflict
	groups             []reconciliationGroup
}

// reconciliationGroup is the atomic conflict and commit boundary for all
// candidates connected by path, identity, or managed product.
type reconciliationGroup struct {
	canonicalIndexes []int
	ownerIndexes     []int
	baselineIndexes  []int
	conflicted       bool
}

// reconcileManagedInstallations uses a strict per-group plan -> validate ->
// commit transaction. No catalog-owned value is modified while groups are
// being assembled or checked.
func (session *scanSession) reconcileManagedInstallations() {
	plan := session.planManagedReconciliation()
	session.validateManagedReconciliation(&plan)
	session.commitManagedReconciliation(plan)
	session.installationDiscoveries = nil
}

// planManagedReconciliation collects proposed mutations without changing the session.
func (session *scanSession) planManagedReconciliation() managedReconciliationPlan {
	plan := managedReconciliationPlan{baselineApps: cloneApplications(session.catalog.Apps), absorbedIDs: map[string]string{}, heldBaselineIDs: map[string]bool{}}
	for index, app := range session.discovered {
		if app.Type != model.ApplicationTypeCLI && app.Type != model.ApplicationTypeBundle {
			continue
		}
		path, ok := canonicalEvidencePath(app.InstallPath)
		plan.canonicalProposals = append(plan.canonicalProposals, canonicalProposal{discoveryIndex: index, app: cloneApplication(app), path: path, pathOK: ok})
	}
	// Protected catalog entries may have suppressed their PATH/Application
	// discovery in add(). They still participate as immutable canonical nodes so
	// an owner at the same path is absorbed instead of duplicated.
	for _, app := range session.catalog.Apps {
		if app.ScanManaged || (app.Type != model.ApplicationTypeCLI && app.Type != model.ApplicationTypeBundle) {
			continue
		}
		already := false
		for _, candidate := range plan.canonicalProposals {
			if candidate.app.ID == app.ID {
				already = true
				break
			}
		}
		if !already {
			path, ok := canonicalEvidencePath(app.InstallPath)
			plan.canonicalProposals = append(plan.canonicalProposals, canonicalProposal{discoveryIndex: -1, app: cloneApplication(app), path: path, pathOK: ok})
		}
	}
	for _, found := range session.installationDiscoveries {
		owner := independentOwner{found: found, pathOK: found.Evidence != nil && strings.TrimSpace(found.Evidence.Source) != ""}
		if owner.pathOK {
			claims := append(append([]string{}, found.Evidence.ExecutablePaths...), found.Evidence.ApplicationPaths...)
			for _, claim := range claims {
				path, ok := canonicalEvidencePath(claim)
				if !ok {
					owner.pathOK = false
					continue
				}
				owner.paths = append(owner.paths, path)
			}
			owner.paths = uniqueStrings(owner.paths)
		}
		plan.independentOwners = append(plan.independentOwners, owner)
	}
	plan.groups = buildReconciliationGroups(plan.canonicalProposals, plan.independentOwners, plan.baselineApps)
	return plan
}

func buildReconciliationGroups(canonicals []canonicalProposal, owners []independentOwner, baseline []model.Application) []reconciliationGroup {
	total := len(canonicals) + len(owners) + len(baseline)
	parent := make([]int, total)
	for index := range parent {
		parent[index] = index
	}
	var root func(int) int
	root = func(value int) int {
		if parent[value] != value {
			parent[value] = root(parent[value])
		}
		return parent[value]
	}
	join := func(left, right int) {
		left, right = root(left), root(right)
		if left != right {
			parent[right] = left
		}
	}
	ownerOffset, baselineOffset := len(canonicals), len(canonicals)+len(owners)
	for ci, canonical := range canonicals {
		for oi, owner := range owners {
			if pathsIntersect([]string{canonical.path}, owner.paths) || sameManagedProduct(canonical.app, owner.found.App) {
				join(ci, ownerOffset+oi)
			}
		}
		for bi, app := range baseline {
			if canonical.app.ID == app.ID || sameStableIdentity(canonical.app, app) || sameManagedProduct(canonical.app, app) {
				join(ci, baselineOffset+bi)
			}
		}
	}
	for oi, owner := range owners {
		for otherIndex := oi + 1; otherIndex < len(owners); otherIndex++ {
			other := owners[otherIndex]
			if pathsIntersect(owner.paths, other.paths) || sameStableIdentity(owner.found.App, other.found.App) {
				join(ownerOffset+oi, ownerOffset+otherIndex)
			}
		}
		for bi, app := range baseline {
			if owner.found.App.ID == app.ID || sameStableIdentity(owner.found.App, app) || pathsIntersect(owner.paths, canonicalPathIfValid(app.InstallPath)) {
				join(ownerOffset+oi, baselineOffset+bi)
			}
		}
	}
	groupsByRoot := map[int]*reconciliationGroup{}
	for index := 0; index < total; index++ {
		key := root(index)
		group := groupsByRoot[key]
		if group == nil {
			group = &reconciliationGroup{}
			groupsByRoot[key] = group
		}
		switch {
		case index < ownerOffset:
			group.canonicalIndexes = append(group.canonicalIndexes, index)
		case index < baselineOffset:
			group.ownerIndexes = append(group.ownerIndexes, index-ownerOffset)
		default:
			group.baselineIndexes = append(group.baselineIndexes, index-baselineOffset)
		}
	}
	keys := make([]int, 0, len(groupsByRoot))
	for key := range groupsByRoot {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	groups := make([]reconciliationGroup, 0, len(keys))
	for _, key := range keys {
		group := *groupsByRoot[key]
		if len(group.ownerIndexes) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

// validateManagedReconciliation marks every ambiguous or incomplete group before
// commit so no member can be migrated independently of its conflicts.
func (session *scanSession) validateManagedReconciliation(plan *managedReconciliationPlan) {
	for groupIndex := range plan.groups {
		group := &plan.groups[groupIndex]
		conflict := func(reason string) {
			group.conflicted = true
			plan.conflicts = append(plan.conflicts, reconciliationConflict{group: groupIndex, reason: reason})
		}
		for _, baselineIndex := range group.baselineIndexes {
			ecosystem := managedPackageEcosystem(plan.baselineApps[baselineIndex])
			if complete, applies := session.packages.Complete[ecosystem]; applies && !complete {
				conflict("baseline_incomplete")
			}
		}
		ownersByPath := map[string]int{}
		for _, ownerIndex := range group.ownerIndexes {
			owner := plan.independentOwners[ownerIndex]
			if owner.found.Evidence == nil || !session.packages.Complete[owner.found.Evidence.Source] {
				conflict("claimant_incomplete")
			}
			if !owner.pathOK || len(owner.paths) == 0 {
				conflict("claim_path")
			}
			if owner.found.Evidence != nil && owner.found.Evidence.Ambiguity != "" {
				conflict("multiple_products")
			}
			matchedProducts := map[string]bool{}
			for _, path := range owner.paths {
				ownersByPath[path]++
				for _, canonicalIndex := range group.canonicalIndexes {
					canonical := plan.canonicalProposals[canonicalIndex]
					if canonical.pathOK && canonical.path == path {
						matchedProducts[canonical.app.ID] = true
					}
				}
			}
			if len(matchedProducts) > 1 {
				conflict("multiple_products")
			}
		}
		for _, count := range ownersByPath {
			if count > 1 {
				conflict("multiple_owners")
			}
		}
		for _, canonicalIndex := range group.canonicalIndexes {
			if !plan.canonicalProposals[canonicalIndex].pathOK {
				conflict("canonical_path")
			}
		}
		if group.conflicted {
			for _, baselineIndex := range group.baselineIndexes {
				plan.heldBaselineIDs[plan.baselineApps[baselineIndex].ID] = true
			}
		}
	}
}

// commitManagedReconciliation applies only fully validated groups and restores
// held baselines for every conflicted group.
func (session *scanSession) commitManagedReconciliation(plan managedReconciliationPlan) {
	heldGroups := make([]reconciliationGroup, 0)
	protectedIDs := map[string]bool{}
	conflictReportIDs := map[string]bool{}
	for groupIndex, group := range plan.groups {
		if group.conflicted {
			message := reconciliationConflictMessage(plan, groupIndex, group)
			session.restoreHeldReconciliationStatus(plan, group, message)
			if len(group.baselineIndexes) == 0 {
				for _, ownerIndex := range group.ownerIndexes {
					found := plan.independentOwners[ownerIndex].found
					found.State.UpdateStatus = model.StatusFailed
					found.State.Error = message
					session.addIndependentPackage(found)
					conflictReportIDs[found.App.ID] = true
				}
			}
			heldGroups = append(heldGroups, group)
			continue
		}
		matchedOwners := map[int]bool{}
		for _, ownerIndex := range group.ownerIndexes {
			owner := plan.independentOwners[ownerIndex]
			if protectedID := protectedOwnerTarget(owner, group, plan.baselineApps); protectedID != "" {
				matchedOwners[ownerIndex] = true
				protectedIDs[protectedID] = true
				if owner.found.App.ID != protectedID {
					plan.absorbedIDs[owner.found.App.ID] = protectedID
				}
				if baseline, found := catalogApplicationByID(plan.baselineApps, protectedID); found {
					session.observed[protectedID] = baseline.StatusManaged
				}
			}
		}
		for _, canonicalIndex := range group.canonicalIndexes {
			canonical := plan.canonicalProposals[canonicalIndex]
			baseline, baselineFound := catalogApplicationByID(session.catalog.Apps, canonical.app.ID)
			if baselineFound && existingStandalonePathDiffers(baseline, canonical) {
				// PATH may now resolve a package-manager executable before an
				// independently installed CLI. Keep the still-existing standalone
				// record and let the package owner remain a separate candidate.
				session.removeDiscoveredID(canonical.app.ID)
				if baseline.ScanManaged {
					delete(session.observed, baseline.ID)
				} else {
					session.observed[baseline.ID] = baseline.StatusManaged
				}
				continue
			}
			ownerIndex := -1
			for _, candidateOwner := range group.ownerIndexes {
				if matchedOwners[candidateOwner] {
					continue
				}
				if pathsIntersect([]string{canonical.path}, plan.independentOwners[candidateOwner].paths) {
					ownerIndex = candidateOwner
					break
				}
			}
			if ownerIndex < 0 {
				continue
			}
			matchedOwners[ownerIndex] = true
			owner := plan.independentOwners[ownerIndex]
			if baselineFound && !baseline.ScanManaged {
				session.removeDiscoveredID(canonical.app.ID)
				session.observed[baseline.ID] = baseline.StatusManaged
				plan.absorbedIDs[owner.found.App.ID] = baseline.ID
				continue
			}
			proposal := canonicalOwnedProposal(canonical.app, owner.found.App, baseline, baselineFound)
			status := mergeApplicationState(owner.found.State, session.observed[canonical.app.ID])
			if baselineFound {
				status = mergeApplicationState(status, baseline.StatusManaged)
			}
			if absorbedBaseline, found := catalogApplicationByID(plan.baselineApps, owner.found.App.ID); found && absorbedBaseline.ID != canonical.app.ID {
				status = mergeApplicationState(status, absorbedBaseline.StatusManaged)
			}
			proposal.StatusManaged = status
			session.upsertCatalogApplication(proposal)
			session.replaceDiscoveredApplication(canonical.discoveryIndex, proposal)
			session.observed[proposal.ID] = status
			if owner.found.App.ID != proposal.ID {
				plan.absorbedIDs[owner.found.App.ID] = proposal.ID
				mergeScanObservation(session.state.Observations, proposal.ID, owner.found.App.ID)
			}
		}
		for _, ownerIndex := range group.ownerIndexes {
			if !matchedOwners[ownerIndex] {
				session.addIndependentPackage(plan.independentOwners[ownerIndex].found)
			}
		}
	}
	if len(heldGroups) > 0 || len(protectedIDs) > 0 {
		filtered := session.discovered[:0]
		for _, app := range session.discovered {
			blocked := protectedIDs[app.ID]
			for _, group := range heldGroups {
				if !conflictReportIDs[app.ID] && reconciliationGroupContainsApplication(app, group, plan, plan.baselineApps) {
					blocked = true
					break
				}
			}
			if !blocked {
				filtered = append(filtered, app)
			}
		}
		session.discovered = filtered
	}
	for absorbedID := range plan.absorbedIDs {
		session.removeCatalogID(absorbedID)
		session.removeDiscoveredID(absorbedID)
		delete(session.observed, absorbedID)
		delete(session.state.Observations, absorbedID)
		delete(session.catalog.ScanVersionControl, absorbedID)
	}
}

func existingStandalonePathDiffers(baseline model.Application, canonical canonicalProposal) bool {
	if strings.HasPrefix(inferIdentity(baseline), "package:") {
		return false
	}
	baselinePath, ok := canonicalEvidencePath(baseline.InstallPath)
	return ok && canonical.pathOK && baselinePath != canonical.path
}

func reconciliationConflictMessage(plan managedReconciliationPlan, groupIndex int, group reconciliationGroup) string {
	subjects := make([]string, 0, len(group.canonicalIndexes)+len(group.ownerIndexes)+len(group.baselineIndexes))
	for _, index := range group.canonicalIndexes {
		subjects = append(subjects, plan.canonicalProposals[index].app.Name)
	}
	for _, index := range group.ownerIndexes {
		subjects = append(subjects, plan.independentOwners[index].found.App.Name)
	}
	for _, index := range group.baselineIndexes {
		subjects = append(subjects, plan.baselineApps[index].Name)
	}
	subjects = uniqueStrings(subjects)
	sort.Strings(subjects)
	subject := strings.Join(subjects, ", ")
	if strings.TrimSpace(subject) == "" {
		subject = fmt.Sprintf("group-%d", groupIndex+1)
	}
	reasons := make([]string, 0)
	for _, conflict := range plan.conflicts {
		if conflict.group == groupIndex {
			reasons = append(reasons, i18n.T("scanner.ownership_conflict_"+conflict.reason))
		}
	}
	reasons = uniqueStrings(reasons)
	sort.Strings(reasons)
	return i18n.T("scanner.ownership_conflict", i18n.T("scanner.ownership_conflict_label"), subject, strings.Join(reasons, ", "))
}

func mergeScanObservation(observations map[string]model.ScanObservation, targetID, absorbedID string) {
	if observations == nil {
		return
	}
	target, absorbed := observations[targetID], observations[absorbedID]
	target.Found = target.Found || absorbed.Found
	if target.Path == "" {
		target.Path = absorbed.Path
	}
	observations[targetID] = target
}

func protectedOwnerTarget(owner independentOwner, group reconciliationGroup, baseline []model.Application) string {
	for _, baselineIndex := range group.baselineIndexes {
		app := baseline[baselineIndex]
		if app.ScanManaged {
			continue
		}
		if app.ID == owner.found.App.ID || sameStableIdentity(app, owner.found.App) || pathsIntersect(canonicalPathIfValid(app.InstallPath), owner.paths) {
			return app.ID
		}
	}
	return ""
}

func (session *scanSession) restoreHeldReconciliationStatus(plan managedReconciliationPlan, group reconciliationGroup, message string) {
	for _, baselineIndex := range group.baselineIndexes {
		baseline := plan.baselineApps[baselineIndex]
		status := baseline.StatusManaged
		status.Error = message
		session.observed[baseline.ID] = status
	}
}

func reconciliationGroupContainsApplication(app model.Application, group reconciliationGroup, plan managedReconciliationPlan, baseline []model.Application) bool {
	for _, index := range group.canonicalIndexes {
		candidate := plan.canonicalProposals[index].app
		if app.ID == candidate.ID || sameManagedProduct(app, candidate) {
			return true
		}
	}
	for _, index := range group.ownerIndexes {
		owner := plan.independentOwners[index].found.App
		if app.ID == owner.ID || sameStableIdentity(app, owner) {
			return true
		}
	}
	for _, index := range group.baselineIndexes {
		if app.ID == baseline[index].ID || sameManagedProduct(app, baseline[index]) {
			return true
		}
	}
	return false
}

func canonicalOwnedProposal(canonical, owner, baseline model.Application, baselineFound bool) model.Application {
	proposal := cloneApplication(canonical)
	proposal.Provider.Type = owner.Provider.Type
	proposal.Package = owner.Package
	proposal.Identity = owner.Identity
	proposal.UpdateMode = owner.UpdateMode
	// Version belongs to the concrete PATH/Application discovery. Other explicit
	// actions, environment, enablement, and scan policy remain user-owned.
	versionAction := canonical.Provider.VersionAction()
	if baselineFound {
		proposal.Enabled = baseline.Enabled
		proposal.Environment = cloneApplication(baseline).Environment
		proposal.ScanManaged = baseline.ScanManaged
		if baseline.UpdateMode != "" {
			proposal.UpdateMode = baseline.UpdateMode
		}
		if baseline.Provider.Actions != nil {
			proposal.Provider.Actions = cloneApplication(baseline).Provider.Actions
		}
	}
	if versionAction != "" && (!baselineFound || baseline.Provider.VersionAction() == "") {
		actionConfig(&proposal).Version = versionAction
	}
	return proposal
}

func catalogApplicationByID(apps []model.Application, id string) (model.Application, bool) {
	for _, app := range apps {
		if app.ID == id {
			return cloneApplication(app), true
		}
	}
	return model.Application{}, false
}

func (session *scanSession) upsertCatalogApplication(app model.Application) {
	for index := range session.catalog.Apps {
		if session.catalog.Apps[index].ID == app.ID {
			session.catalog.Apps[index] = cloneApplication(app)
			return
		}
	}
	session.catalog.Apps = append(session.catalog.Apps, cloneApplication(app))
}

func (session *scanSession) replaceDiscoveredApplication(index int, app model.Application) {
	if index >= 0 && index < len(session.discovered) {
		session.discovered[index] = cloneApplication(app)
		return
	}
	session.discovered = append(session.discovered, cloneApplication(app))
}

func (session *scanSession) removeCatalogID(id string) {
	filtered := session.catalog.Apps[:0]
	for _, app := range session.catalog.Apps {
		if app.ID != id {
			filtered = append(filtered, app)
		}
	}
	session.catalog.Apps = filtered
}

func (session *scanSession) removeDiscoveredID(id string) {
	filtered := session.discovered[:0]
	for _, app := range session.discovered {
		if app.ID != id {
			filtered = append(filtered, app)
		}
	}
	session.discovered = filtered
}

func canonicalPathIfValid(value string) []string {
	if path, ok := canonicalEvidencePath(value); ok {
		return []string{path}
	}
	return nil
}

func pathsIntersect(left, right []string) bool {
	for _, first := range left {
		if first == "" {
			continue
		}
		for _, second := range right {
			if first == second {
				return true
			}
		}
	}
	return false
}

func sameStableIdentity(left, right model.Application) bool {
	first, second := strings.TrimSpace(inferIdentity(left)), strings.TrimSpace(inferIdentity(right))
	return first != "" && first == second
}

func sameManagedProduct(left, right model.Application) bool {
	if strings.ToLower(strings.TrimSpace(left.Name)) != strings.ToLower(strings.TrimSpace(right.Name)) {
		return false
	}
	leftCanonical := left.Type == model.ApplicationTypeCLI || left.Type == model.ApplicationTypeBundle
	rightCanonical := right.Type == model.ApplicationTypeCLI || right.Type == model.ApplicationTypeBundle
	leftPackage, rightPackage := left.Type == model.ApplicationTypePackage, right.Type == model.ApplicationTypePackage
	return (leftCanonical && (rightCanonical || rightPackage)) || (rightCanonical && leftPackage)
}
