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
	app    model.Application
	path   string
	pathOK bool
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
	baselinePaths      [][]string
	baselineByID       map[string]model.Application
	absorbedIDs        map[string]string
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
	onlyIncomplete   bool
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
	baselineApps := cloneApplications(session.catalog.Apps)
	plan := managedReconciliationPlan{
		baselineApps:  baselineApps,
		baselinePaths: make([][]string, len(baselineApps)),
		baselineByID:  make(map[string]model.Application, len(baselineApps)),
		absorbedIDs:   map[string]string{},
	}
	for _, app := range baselineApps {
		plan.baselineByID[app.ID] = app
	}
	for _, app := range session.discovered {
		if app.Type != model.ApplicationTypeCLI && app.Type != model.ApplicationTypeBundle {
			continue
		}
		path, ok := canonicalEvidencePath(app.InstallPath)
		plan.canonicalProposals = append(plan.canonicalProposals, canonicalProposal{app: cloneApplication(app), path: path, pathOK: ok})
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
			plan.canonicalProposals = append(plan.canonicalProposals, canonicalProposal{app: cloneApplication(app), path: path, pathOK: ok})
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
	if len(plan.independentOwners) > 0 {
		for index, app := range baselineApps {
			plan.baselinePaths[index] = canonicalPathIfValid(app.InstallPath)
		}
	}
	plan.groups = buildReconciliationGroups(plan.canonicalProposals, plan.independentOwners, plan.baselineApps, plan.baselinePaths)
	return plan
}

func buildReconciliationGroups(canonicals []canonicalProposal, owners []independentOwner, baseline []model.Application, baselinePaths [][]string) []reconciliationGroup {
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
	joinMatches := func(node int, matches []int) {
		for _, match := range matches {
			join(node, match)
		}
	}
	baselineByID := map[string][]int{}
	baselineByIdentity := map[string][]int{}
	baselineByProduct := map[string][]int{}
	baselineByPath := map[string][]int{}
	for bi, app := range baseline {
		node := baselineOffset + bi
		indexReconciliationNode(baselineByID, app.ID, node)
		indexReconciliationNode(baselineByIdentity, stableIdentityKey(app), node)
		indexReconciliationNode(baselineByProduct, managedProductKey(app), node)
		for _, path := range baselinePaths[bi] {
			indexReconciliationNode(baselineByPath, path, node)
		}
	}
	canonicalByPath := map[string][]int{}
	canonicalByProduct := map[string][]int{}
	for ci, canonical := range canonicals {
		joinMatches(ci, baselineByID[canonical.app.ID])
		joinMatches(ci, baselineByIdentity[stableIdentityKey(canonical.app)])
		joinMatches(ci, baselineByProduct[managedProductKey(canonical.app)])
		indexReconciliationNode(canonicalByPath, canonical.path, ci)
		indexReconciliationNode(canonicalByProduct, managedProductKey(canonical.app), ci)
	}
	ownerByPath := map[string][]int{}
	ownerByIdentity := map[string][]int{}
	for oi, owner := range owners {
		node := ownerOffset + oi
		for _, path := range owner.paths {
			joinMatches(node, canonicalByPath[path])
			joinMatches(node, ownerByPath[path])
			joinMatches(node, baselineByPath[path])
			indexReconciliationNode(ownerByPath, path, node)
		}
		identity := stableIdentityKey(owner.found.App)
		joinMatches(node, ownerByIdentity[identity])
		joinMatches(node, baselineByIdentity[identity])
		joinMatches(node, baselineByID[owner.found.App.ID])
		joinMatches(node, canonicalByProduct[managedProductKey(owner.found.App)])
		indexReconciliationNode(ownerByIdentity, identity, node)
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

func indexReconciliationNode(index map[string][]int, key string, node int) {
	key = strings.TrimSpace(key)
	if key != "" {
		index[key] = append(index[key], node)
	}
}

func stableIdentityKey(app model.Application) string {
	return strings.TrimSpace(inferIdentity(app))
}

func managedProductKey(app model.Application) string {
	switch app.Type {
	case model.ApplicationTypeCLI, model.ApplicationTypeBundle, model.ApplicationTypePackage:
		return strings.ToLower(strings.TrimSpace(app.Name))
	default:
		return ""
	}
}

// validateManagedReconciliation marks every ambiguous or incomplete group before
// commit so no member can be migrated independently of its conflicts.
func (session *scanSession) validateManagedReconciliation(plan *managedReconciliationPlan) {
	for groupIndex := range plan.groups {
		group := &plan.groups[groupIndex]
		hasReconciliationTarget := len(group.canonicalIndexes) > 0 || len(group.baselineIndexes) > 0
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
			if hasReconciliationTarget && (owner.found.Evidence == nil || !session.packages.Complete[owner.found.Evidence.Source]) {
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
		group.onlyIncomplete = reconciliationGroupOnlyIncomplete(*plan, groupIndex)
	}
}

// commitManagedReconciliation applies only fully validated groups and restores
// held baselines for every conflicted group.
func (session *scanSession) commitManagedReconciliation(plan managedReconciliationPlan) {
	heldGroupIndexes := make([]int, 0)
	protectedIDs := map[string]bool{}
	conflictReportIDs := map[string]bool{}
	for groupIndex, group := range plan.groups {
		if group.conflicted {
			session.commitConflictedReconciliationGroup(plan, groupIndex, group, conflictReportIDs)
			heldGroupIndexes = append(heldGroupIndexes, groupIndex)
			continue
		}
		session.commitValidatedReconciliationGroup(&plan, group, protectedIDs)
	}
	session.filterReconciliationDiscoveries(plan, heldGroupIndexes, protectedIDs, conflictReportIDs)
	session.removeAbsorbedReconciliationRecords(plan.absorbedIDs)
}

func (session *scanSession) commitConflictedReconciliationGroup(plan managedReconciliationPlan, groupIndex int, group reconciliationGroup, conflictReportIDs map[string]bool) {
	message := reconciliationConflictMessage(plan, groupIndex, group)
	if group.onlyIncomplete {
		session.restoreHeldReconciliationStatus(plan, group, "")
	} else {
		session.restoreHeldReconciliationStatus(plan, group, message)
	}
	if len(group.baselineIndexes) > 0 || (group.onlyIncomplete && len(group.canonicalIndexes) > 0) {
		return
	}
	for _, ownerIndex := range group.ownerIndexes {
		found := plan.independentOwners[ownerIndex].found
		found.State.UpdateStatus = model.StatusFailed
		found.State.Error = message
		session.addIndependentPackage(found)
		conflictReportIDs[found.App.ID] = true
	}
}

func (session *scanSession) commitValidatedReconciliationGroup(plan *managedReconciliationPlan, group reconciliationGroup, protectedIDs map[string]bool) {
	matchedOwners := map[int]bool{}
	for _, ownerIndex := range group.ownerIndexes {
		owner := plan.independentOwners[ownerIndex]
		if protectedID := protectedOwnerTarget(owner, group, plan.baselineApps, plan.baselinePaths); protectedID != "" {
			matchedOwners[ownerIndex] = true
			protectedIDs[protectedID] = true
			if owner.found.App.ID != protectedID {
				plan.absorbedIDs[owner.found.App.ID] = protectedID
			}
			if baseline, found := plan.baselineByID[protectedID]; found {
				session.observed[protectedID] = baseline.StatusManaged
			}
		}
	}
	for _, canonicalIndex := range group.canonicalIndexes {
		canonical := plan.canonicalProposals[canonicalIndex]
		baseline, baselineFound := plan.baselineByID[canonical.app.ID]
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
		ownerIndex := matchingReconciliationOwner(canonical, group, plan.independentOwners, matchedOwners)
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
		if absorbedBaseline, found := plan.baselineByID[owner.found.App.ID]; found && absorbedBaseline.ID != canonical.app.ID {
			status = mergeApplicationState(status, absorbedBaseline.StatusManaged)
		}
		proposal.StatusManaged = status
		session.upsertCatalogApplication(proposal)
		session.replaceDiscoveredApplication(canonical.app.ID, proposal)
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

func matchingReconciliationOwner(canonical canonicalProposal, group reconciliationGroup, owners []independentOwner, matched map[int]bool) int {
	for _, ownerIndex := range group.ownerIndexes {
		if !matched[ownerIndex] && pathsIntersect([]string{canonical.path}, owners[ownerIndex].paths) {
			return ownerIndex
		}
	}
	return -1
}

func (session *scanSession) removeAbsorbedReconciliationRecords(absorbedIDs map[string]string) {
	for absorbedID := range absorbedIDs {
		session.removeCatalogID(absorbedID)
		session.removeDiscoveredID(absorbedID)
		delete(session.observed, absorbedID)
		delete(session.state.Observations, absorbedID)
		delete(session.catalog.ScanVersionControl, absorbedID)
	}
}

func reconciliationGroupContainsCanonical(app model.Application, group reconciliationGroup, plan managedReconciliationPlan) bool {
	for _, canonicalIndex := range group.canonicalIndexes {
		if plan.canonicalProposals[canonicalIndex].app.ID == app.ID {
			return true
		}
	}
	return false
}

func reconciliationGroupOnlyIncomplete(plan managedReconciliationPlan, groupIndex int) bool {
	hasIncomplete := false
	for _, conflict := range plan.conflicts {
		if conflict.group != groupIndex {
			continue
		}
		switch conflict.reason {
		case "baseline_incomplete", "claimant_incomplete":
			hasIncomplete = true
		default:
			return false
		}
	}
	return hasIncomplete
}

// filterReconciliationDiscoveries applies held-group suppression in one pass.
// Incomplete inventories restore the matching baseline instead of allowing a
// later finalize stage to interpret the unobserved application as missing.
func (session *scanSession) filterReconciliationDiscoveries(plan managedReconciliationPlan, heldGroupIndexes []int, protectedIDs, conflictReportIDs map[string]bool) {
	if len(heldGroupIndexes) == 0 && len(protectedIDs) == 0 {
		return
	}
	filtered := session.discovered[:0]
	for _, app := range session.discovered {
		blocked := protectedIDs[app.ID]
		var restore *model.Application
		for _, groupIndex := range heldGroupIndexes {
			group := plan.groups[groupIndex]
			if group.onlyIncomplete && len(group.baselineIndexes) == 0 && reconciliationGroupContainsCanonical(app, group, plan) {
				continue
			}
			contains := reconciliationGroupContainsApplication(app, group, plan, plan.baselineApps)
			if !contains {
				continue
			}
			if !conflictReportIDs[app.ID] {
				blocked = true
			}
			if group.onlyIncomplete {
				for _, baselineIndex := range group.baselineIndexes {
					baseline := &plan.baselineApps[baselineIndex]
					if baseline.ID == app.ID {
						restore = baseline
						break
					}
				}
			}
		}
		if !blocked {
			filtered = append(filtered, app)
		} else if restore != nil {
			filtered = append(filtered, cloneApplication(*restore))
		}
	}
	session.discovered = filtered
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
			reasons = append(reasons, i18n.T("scanner.install_recon_conflict_"+conflict.reason))
		}
	}
	reasons = uniqueStrings(reasons)
	sort.Strings(reasons)
	return i18n.T("scanner.install_recon_conflict", i18n.T("scanner.install_recon_conflict_label"), subject, strings.Join(reasons, ", "))
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

func protectedOwnerTarget(owner independentOwner, group reconciliationGroup, baseline []model.Application, baselinePaths [][]string) string {
	for _, baselineIndex := range group.baselineIndexes {
		app := baseline[baselineIndex]
		if app.ScanManaged {
			continue
		}
		if app.ID == owner.found.App.ID || sameStableIdentity(app, owner.found.App) || pathsIntersect(baselinePaths[baselineIndex], owner.paths) {
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
	// Package managers own their capability actions. Preserve them before the
	// PATH version probe is overlaid below; a canonical PATH candidate normally
	// has no update/check action of its own.
	proposal.Provider.Actions = cloneApplication(owner).Provider.Actions
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
			base := cloneApplication(baseline).Provider.Actions
			actions := actionConfig(&proposal)
			if base.Version != "" {
				actions.Version = base.Version
			}
			if base.Check != "" {
				actions.Check = base.Check
			}
			if base.Update != "" {
				actions.Update = base.Update
			}
			if base.Install != "" {
				actions.Install = base.Install
			}
			if base.Download != nil {
				actions.Download = base.Download
			}
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

func (session *scanSession) replaceDiscoveredApplication(id string, app model.Application) {
	for index := range session.discovered {
		if session.discovered[index].ID == id {
			session.discovered[index] = cloneApplication(app)
			return
		}
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
	if !strings.EqualFold(strings.TrimSpace(left.Name), strings.TrimSpace(right.Name)) {
		return false
	}
	leftCanonical := left.Type == model.ApplicationTypeCLI || left.Type == model.ApplicationTypeBundle
	rightCanonical := right.Type == model.ApplicationTypeCLI || right.Type == model.ApplicationTypeBundle
	leftPackage, rightPackage := left.Type == model.ApplicationTypePackage, right.Type == model.ApplicationTypePackage
	return (leftCanonical && (rightCanonical || rightPackage)) || (rightCanonical && leftPackage)
}
