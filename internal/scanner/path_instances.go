package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	"github.com/eoctet/tendkit/internal/scanner/handler"
)

const pathInstanceFingerprintLength = handler.PathInstanceFingerprintLength

type pathInstanceAssignment struct {
	Candidates         []handler.Candidate
	Migrations         map[string]string
	IdentityMigrations map[string]string
}

type pathInstanceCandidate struct {
	candidate handler.Candidate
	canonical string
	history   *model.Application
}

func assignPathInstances(definitionID string, candidates []handler.Candidate, existing []model.Application) (pathInstanceAssignment, error) {
	baseID := handler.PathApplicationID(definitionID)
	definition, definitionFound := builtin.FindPathByID(definitionID)
	currentByCanonical := make(map[string]pathInstanceCandidate, len(candidates))
	for _, candidate := range candidates {
		canonical, err := handler.CanonicalExecutablePath(candidate.Application.InstallPath)
		if err != nil {
			return pathInstanceAssignment{}, fmt.Errorf("canonicalize %s installation %q: %w", baseID, candidate.Application.InstallPath, err)
		}
		if _, duplicate := currentByCanonical[canonical]; duplicate {
			continue
		}
		currentByCanonical[canonical] = pathInstanceCandidate{candidate: candidate, canonical: canonical}
	}
	canonicals := make([]string, 0, len(currentByCanonical))
	for canonical := range currentByCanonical {
		canonicals = append(canonicals, canonical)
	}
	sort.Strings(canonicals)

	historyByCanonical := map[string]model.Application{}
	for _, app := range existing {
		if !historicalPathInstance(definitionID, app) {
			continue
		}
		canonical, err := handler.CanonicalExecutablePath(app.InstallPath)
		if err != nil {
			continue
		}
		if previous, exists := historyByCanonical[canonical]; exists && previous.ID != app.ID {
			return pathInstanceAssignment{}, fmt.Errorf("multiple historical %s instances resolve to %q", baseID, canonical)
		}
		historyByCanonical[canonical] = app
	}
	for _, canonical := range canonicals {
		value := currentByCanonical[canonical]
		if history, ok := historyByCanonical[canonical]; ok {
			copy := history
			value.history = &copy
			if definitionFound {
				rebound, err := handler.RebindPathCandidateActions(value.candidate.Application, definition, history.InstallPath)
				if err != nil {
					return pathInstanceAssignment{}, fmt.Errorf("rebind historical %s instance: %w", baseID, err)
				}
				value.candidate.Application = rebound
			} else {
				value.candidate.Application.InstallPath = history.InstallPath
			}
			currentByCanonical[canonical] = value
		}
	}

	assignment := pathInstanceAssignment{Candidates: make([]handler.Candidate, 0, len(canonicals)), Migrations: map[string]string{}, IdentityMigrations: map[string]string{}}
	if len(canonicals) == 0 {
		return assignment, nil
	}
	baseIdentity := "cli:" + model.NormalizeIdentityName(currentByCanonical[canonicals[0]].candidate.Application.Name)

	assignedIDs := map[string]string{}
	assignedIdentities := map[string]string{}
	resolvedHistoricalIDs := map[string]bool{}
	for _, canonical := range canonicals {
		value := currentByCanonical[canonical]
		fingerprint := pathInstanceFingerprint(canonical)
		id := baseID
		if len(canonicals) > 1 {
			id = baseID + "-" + fingerprint
			if value.history != nil && pathInstanceExtendedID(definitionID, value.history.ID) {
				id = value.history.ID
			}
		}
		identity := baseIdentity
		if len(canonicals) > 1 {
			identity = baseIdentity + "@" + fingerprint
			if value.history != nil && pathInstanceExtendedIdentity(baseIdentity, value.history.Identity) {
				identity = value.history.Identity
			}
		}
		if previous, exists := assignedIDs[id]; exists && previous != canonical {
			return pathInstanceAssignment{}, fmt.Errorf("path instance ID collision %q for %q and %q", id, previous, canonical)
		}
		if previous, exists := assignedIdentities[strings.ToLower(identity)]; exists && previous != canonical {
			return pathInstanceAssignment{}, fmt.Errorf("path instance identity collision %q for %q and %q", identity, previous, canonical)
		}
		assignedIDs[id] = canonical
		assignedIdentities[strings.ToLower(identity)] = canonical
		value.candidate.Application.ID = id
		value.candidate.Application.Identity = identity
		assignment.Candidates = append(assignment.Candidates, value.candidate)
		if value.history != nil && value.history.ID != id {
			assignment.Migrations[value.history.ID] = id
		}
		if value.history != nil && defaultPathInstanceIdentity(baseIdentity, value.history.Identity) && !strings.EqualFold(value.history.Identity, identity) {
			assignment.IdentityMigrations[value.history.ID] = identity
		}
		if value.history != nil {
			resolvedHistoricalIDs[value.history.ID] = true
		}
	}
	for _, app := range existing {
		if !historicalPathInstance(definitionID, app) || resolvedHistoricalIDs[app.ID] {
			continue
		}
		if app.ID == baseID {
			fingerprintSource, err := historicalPathFingerprintSource(app.InstallPath)
			if err != nil {
				return pathInstanceAssignment{}, fmt.Errorf("fingerprint missing historical %s instance: %w", baseID, err)
			}
			fingerprint := pathInstanceFingerprint(fingerprintSource)
			assignment.Migrations[app.ID] = baseID + "-" + fingerprint
			if defaultPathInstanceIdentity(baseIdentity, app.Identity) {
				assignment.IdentityMigrations[app.ID] = baseIdentity + "@" + fingerprint
			}
			continue
		}
		if !defaultPathInstanceIdentity(baseIdentity, app.Identity) {
			continue
		}
		if pathInstanceExtendedID(definitionID, app.ID) {
			fingerprint := strings.TrimPrefix(app.ID, baseID+"-")
			assignment.IdentityMigrations[app.ID] = baseIdentity + "@" + fingerprint
		}
	}
	if err := validatePathInstanceAssignments(assignment, existing); err != nil {
		return pathInstanceAssignment{}, err
	}
	return assignment, nil
}

func validatePathInstanceAssignments(assignment pathInstanceAssignment, existing []model.Application) error {
	assignedByID := map[string]model.Application{}
	assignedByIdentity := map[string]model.Application{}
	for _, candidate := range assignment.Candidates {
		assignedByID[candidate.Application.ID] = candidate.Application
		assignedByIdentity[strings.ToLower(candidate.Application.Identity)] = candidate.Application
	}
	finalExistingByID := map[string]model.Application{}
	finalExistingByIdentity := map[string]model.Application{}
	for _, app := range existing {
		finalID := app.ID
		if migrated, ok := assignment.Migrations[app.ID]; ok {
			finalID = migrated
		}
		finalIdentity := app.Identity
		if migrated, ok := assignment.IdentityMigrations[app.ID]; ok {
			finalIdentity = migrated
		}
		if previous, exists := finalExistingByID[finalID]; exists {
			return fmt.Errorf("path instance migration ID %q conflicts with existing %q", finalID, previous.Name)
		}
		finalExistingByID[finalID] = app
		if assigned, exists := assignedByID[finalID]; exists {
			if !sameCanonicalExecutable(app.InstallPath, assigned.InstallPath) {
				return fmt.Errorf("path instance ID %q conflicts with existing %q", assigned.ID, app.Name)
			}
		}
		if finalIdentity != "" {
			identityKey := strings.ToLower(finalIdentity)
			if previous, exists := finalExistingByIdentity[identityKey]; exists {
				return fmt.Errorf("path instance migration identity %q conflicts with existing %q", finalIdentity, previous.Name)
			}
			finalExistingByIdentity[identityKey] = app
			if assigned, exists := assignedByIdentity[identityKey]; exists {
				if !sameCanonicalExecutable(app.InstallPath, assigned.InstallPath) {
					return fmt.Errorf("path instance identity %q conflicts with existing %q", assigned.Identity, app.Name)
				}
			}
		}
	}
	return nil
}

func historicalPathFingerprintSource(path string) (string, error) {
	if canonical, err := handler.CanonicalExecutablePath(path); err == nil {
		return canonical, nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func sameCanonicalExecutable(left, right string) bool {
	leftCanonical, leftErr := handler.CanonicalExecutablePath(left)
	rightCanonical, rightErr := handler.CanonicalExecutablePath(right)
	return leftErr == nil && rightErr == nil && leftCanonical == rightCanonical
}

func pathInstanceFingerprint(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:pathInstanceFingerprintLength]
}

func historicalPathInstance(definitionID string, app model.Application) bool {
	if !app.ScanManaged || (app.Type != model.ApplicationTypeCLI && app.Type != model.ApplicationTypePackage) {
		return false
	}
	return app.ID == handler.PathApplicationID(definitionID) || pathInstanceExtendedID(definitionID, app.ID)
}

func pathInstanceExtendedID(definitionID, id string) bool {
	prefix := handler.PathApplicationID(definitionID) + "-"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	value := strings.TrimPrefix(id, prefix)
	if len(value) != pathInstanceFingerprintLength {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func pathInstanceExtendedIdentity(base, identity string) bool {
	prefix := strings.ToLower(base) + "@"
	value := strings.ToLower(identity)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+pathInstanceFingerprintLength {
		return false
	}
	for _, character := range strings.TrimPrefix(value, prefix) {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func defaultPathInstanceIdentity(base, identity string) bool {
	identity = strings.TrimSpace(identity)
	return identity == "" || strings.EqualFold(identity, base) || pathInstanceExtendedIdentity(base, identity)
}
