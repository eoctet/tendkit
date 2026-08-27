package scanner

import (
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
)

// existingIndex resolves discoveries against stable catalog identity, path, and
// name keys while retaining ambiguity markers for collisions.
type existingIndex struct {
	byIdentity map[string]string
	byPath     map[string]string
	byName     map[string]string
	apps       map[string]model.Application
}

func indexApps(apps []model.Application) existingIndex {
	index := existingIndex{byIdentity: map[string]string{}, byPath: map[string]string{}, byName: map[string]string{}, apps: map[string]model.Application{}}
	for _, app := range apps {
		index.apps[app.ID] = app
		identity := inferIdentity(app)
		if previous, exists := index.byIdentity[identity]; exists && previous != app.ID {
			// Normalization collisions (for example foo.bar and foobar) are
			// ambiguous package identities. Never silently pick the later app.
			index.byIdentity[identity] = ""
		} else if !exists {
			index.byIdentity[identity] = app.ID
		}
		if app.InstallPath != "" {
			index.byPath[canonicalPath(app.InstallPath)] = app.ID
		}
		index.byName[strings.ToLower(strings.TrimSpace(app.Name))] = app.ID
	}
	return index
}

func (i existingIndex) match(app model.Application) string {
	identity := inferIdentity(app)
	if id, exists := i.byIdentity[identity]; exists {
		if id != "" {
			return id
		}
		if strings.HasPrefix(identity, "package:") {
			return ""
		}
	}
	if app.InstallPath != "" && !strings.HasPrefix(identity, "package:") {
		if id := i.byPath[canonicalPath(app.InstallPath)]; id != "" {
			return id
		}
	}
	id := i.byName[strings.ToLower(strings.TrimSpace(app.Name))]
	configured := i.apps[id]
	if _, installationScoped := matchingBuiltInPathDefinition(app); installationScoped && app.InstallPath != "" {
		if _, configuredScoped := matchingBuiltInPathDefinition(configured); configuredScoped || strings.HasPrefix(strings.ToLower(strings.TrimSpace(configured.Identity)), "cli:") {
			return ""
		}
	}
	sameType := configured.Type == app.Type
	if sameType && app.Type == model.ApplicationTypePackage && configured.Provider.Type != app.Provider.Type {
		sameType = false
	}
	if configured.ID != "" && (sameType ||
		(configured.Provider.Type == app.Provider.Type && configured.Package != "" && configured.Package == app.Package)) {
		return id
	}
	return ""
}

// deduplicateCatalog selects one deterministic winner per stable key and moves
// loser observations to that winner in the same snapshot transformation.
func deduplicateCatalog(apps []model.Application, state model.RuntimeState) ([]model.Application, model.RuntimeState) {
	apps = cloneApplications(apps)
	state = cloneRuntimeState(state)
	type candidate struct {
		app   model.Application
		order int
	}
	groups := map[string][]candidate{}
	keys := make([]string, 0)
	activeBuiltInCLIs := make(map[string]bool)
	for _, app := range apps {
		if app.Type == model.ApplicationTypeCLI {
			if item, ok := matchingBuiltInPathDefinition(app); ok {
				activeBuiltInCLIs[item.ID] = true
			}
		}
	}
	for index, app := range apps {
		key := deduplicationKey(app, activeBuiltInCLIs)
		if strings.HasPrefix(key, "package:") {
			if existing := groups[key]; len(existing) > 0 && packageIdentityCollision(existing[0].app, app) {
				// A lossy normalized package identity is ambiguous. Keep the
				// candidate visible, but clear its identity so it cannot be
				// silently merged or later persisted as an invalid duplicate.
				app.Identity = ""
				key = "ambiguous-package:" + app.ID
			}
		}
		if _, exists := groups[key]; !exists {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], candidate{app: app, order: index})
	}
	sort.Slice(keys, func(i, j int) bool { return groups[keys[i]][0].order < groups[keys[j]][0].order })
	result := make([]model.Application, 0, len(groups))
	if state.Observations == nil {
		state.Observations = map[string]model.ScanObservation{}
	}
	for _, key := range keys {
		items := groups[key]
		winner := items[0]
		for _, item := range items[1:] {
			if candidatePreferred(item.app, winner.app) {
				winner = item
			}
		}
		winnerObservation := state.Observations[winner.app.ID]
		for _, item := range items {
			if item.app.ID == winner.app.ID {
				continue
			}
			winner.app = mergeDuplicateApplication(winner.app, item.app)
			winner.app.StatusManaged = mergeApplicationState(winner.app.StatusManaged, item.app.StatusManaged)
			loserObservation := state.Observations[item.app.ID]
			winnerObservation.Found = winnerObservation.Found || loserObservation.Found
			if winnerObservation.Path == "" {
				winnerObservation.Path = loserObservation.Path
			}
			delete(state.Observations, item.app.ID)
		}
		state.Observations[winner.app.ID] = winnerObservation
		result = append(result, winner.app)
	}
	return result, state
}

func packageIdentityCollision(first, second model.Application) bool {
	return inferIdentity(first) == inferIdentity(second) && !strings.EqualFold(strings.TrimSpace(first.Package), strings.TrimSpace(second.Package))
}

func mergeDuplicateMetadata(primary, secondary model.Application) model.Application {
	if strings.TrimSpace(primary.Description) == "" {
		primary.Description = secondary.Description
	}
	if strings.TrimSpace(primary.URL) == "" {
		primary.URL = secondary.URL
	}
	return primary
}

// candidatePriority protects user-owned entries first, then prefers canonical
// discovery sources and richer executable capabilities.
type candidatePriority struct {
	protected       bool
	sourceRank      int
	capabilityScore int
}

func priorityForCandidate(app model.Application) candidatePriority {
	priority := candidatePriority{protected: !app.ScanManaged}
	if _, ok := matchingBuiltInPathDefinition(app); ok && app.Type == model.ApplicationTypeCLI {
		priority.sourceRank = 30
	} else {
		switch app.Type {
		case model.ApplicationTypeBundle:
			priority.sourceRank = 20
		case model.ApplicationTypePackage:
			priority.sourceRank = 10
		}
	}
	priority.capabilityScore = appCapabilityScore(app)
	return priority
}

func candidatePreferred(candidate, current model.Application) bool {
	left, right := priorityForCandidate(candidate), priorityForCandidate(current)
	if left.protected != right.protected {
		return left.protected
	}
	if left.sourceRank != right.sourceRank {
		return left.sourceRank > right.sourceRank
	}
	return left.capabilityScore > right.capabilityScore
}

func appCapabilityScore(app model.Application) int {
	score := 0
	if app.Provider.Type != model.ProviderDefault {
		score += 20
	}
	if app.UpdateMode != model.ModeCheck {
		score += 10
	}
	if app.Package != "" {
		score += 5
	}
	if app.Provider.VersionAction() != "" || app.Provider.CheckAction() != "" || app.Provider.UpdateAction() != "" {
		score += 5
	}
	return score
}

func mergeDuplicateApplication(primary, secondary model.Application) model.Application {
	if !primary.ScanManaged {
		return primary
	}
	primary = mergeDuplicateMetadata(primary, secondary)
	if !compatibleBuiltInCapabilities(primary, secondary) {
		return primary
	}
	if primary.Provider.CheckAction() == "" && secondary.Provider.CheckAction() != "" {
		actionConfig(&primary).Check = secondary.Provider.CheckAction()
	}
	if primary.Provider.UpdateAction() == "" && secondary.Provider.UpdateAction() != "" {
		actionConfig(&primary).Update = secondary.Provider.UpdateAction()
	}
	if primary.Provider.DownloadAction() == nil && secondary.Provider.DownloadAction() != nil {
		actionConfig(&primary).Download = cloneApplication(secondary).Provider.DownloadAction()
	}
	if primary.UpdateMode == model.ModeCheck {
		if primary.Provider.UpdateAction() != "" && secondary.UpdateMode == model.ModeAuto {
			primary.UpdateMode = model.ModeAuto
		} else if primary.Provider.DownloadAction() != nil && secondary.UpdateMode == model.ModeDownload {
			primary.UpdateMode = model.ModeDownload
		}
	}
	return primary
}

func compatibleBuiltInCapabilities(primary, secondary model.Application) bool {
	primaryDefinition, primaryOK := matchingBuiltInPathDefinition(primary)
	secondaryDefinition, secondaryOK := matchingBuiltInPathDefinition(secondary)
	return primaryOK && secondaryOK && primaryDefinition.ID == secondaryDefinition.ID && primary.Provider.Type == secondary.Provider.Type && normalizePackage(primary.Package) == normalizePackage(secondary.Package)
}

func mergeApplicationState(primary, secondary model.ManagedStatus) model.ManagedStatus {
	if primary.CurrentVersion == "" {
		primary.CurrentVersion = secondary.CurrentVersion
	}
	if primary.LatestVersion == "" {
		primary.LatestVersion = secondary.LatestVersion
	}
	if secondary.FirstDetectedTime != "" && (primary.FirstDetectedTime == "" || secondary.FirstDetectedTime < primary.FirstDetectedTime) {
		primary.FirstDetectedTime = secondary.FirstDetectedTime
	}
	if primary.Error == "" {
		primary.Error = secondary.Error
	}
	primary.HasUpdate = primary.HasUpdate || secondary.HasUpdate
	return primary
}
