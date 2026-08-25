package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ScanProgress is a presentation-independent scanner milestone.
type ScanProgress struct {
	Stage   string
	Subject string
}

// ScanKeepResolution records a user's decision to retain one scanned field.
// It deliberately stores only a fingerprint, never candidate configuration.
type ScanKeepResolution struct {
	Fingerprint string `json:"fingerprint"`
	RecordedAt  string `json:"recorded_at"`
}

// ScanObservation is transient discovery data. Found and Path are never
// persisted; an accepted candidate stores Path in Application.InstallPath.
type ScanObservation struct {
	Found bool
	Path  string
}

// RuntimeState contains only transient discovery observations. Persistent
// application status and scan version control always live in Config.
type RuntimeState struct {
	Observations map[string]ScanObservation
}

// ScanFieldChange describes one catalog field changed by a scan candidate.
type ScanFieldChange struct {
	Field    string
	Current  string
	Proposed string
}

// ScanKeepFingerprint returns the stable SHA-256 identity of one field in a
// complete scan difference. Raw values are used only while calculating the hash.
func ScanKeepFingerprint(applicationID string, current, proposed Application, changes []ScanFieldChange, field ScanFieldChange) string {
	// Runtime updates are independent of scan candidates and must not invalidate
	// a user's keep decision for otherwise identical differences.
	current.StatusManaged = ManagedStatus{}
	proposed.StatusManaged = ManagedStatus{}
	fields := append([]ScanFieldChange(nil), changes...)
	sort.Slice(fields, func(left, right int) bool {
		if fields[left].Field != fields[right].Field {
			return fields[left].Field < fields[right].Field
		}
		if fields[left].Current != fields[right].Current {
			return fields[left].Current < fields[right].Current
		}
		return fields[left].Proposed < fields[right].Proposed
	})
	payload := struct {
		ApplicationID string            `json:"application_id"`
		Field         string            `json:"field"`
		Current       Application       `json:"current"`
		Proposed      Application       `json:"proposed"`
		Fields        []ScanFieldChange `json:"fields"`
	}{applicationID, field.Field, current, proposed, fields}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ClearScanVersionControlForUnmanagedTransitions removes scan keep records only when
// an existing application changes from scan-managed to unmanaged.
func ClearScanVersionControlForUnmanagedTransitions(current, proposed *Config) bool {
	if current == nil || proposed == nil || len(proposed.ScanVersionControl) == 0 {
		return false
	}
	previous := make(map[string]Application, len(current.Apps))
	for _, application := range current.Apps {
		previous[application.ID] = application
	}
	changed := false
	for _, application := range proposed.Apps {
		if old, found := previous[application.ID]; found && old.ScanManaged && !application.ScanManaged {
			if _, found := proposed.ScanVersionControl[application.ID]; found {
				delete(proposed.ScanVersionControl, application.ID)
				changed = true
			}
		}
	}
	return changed
}

// ScanApplicationChange groups proposed field changes for an existing app.
type ScanApplicationChange struct {
	Current  Application
	Proposed Application
	Fields   []ScanFieldChange
}
