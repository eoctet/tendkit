package scanner

import (
	"context"
	"strings"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/builtin"
	"github.com/eoctet/tendkit/internal/scanner/handler"
)

// appInfo is the scanner's presentation-free view of inspected bundle metadata.
type appInfo struct {
	Path        string
	Name        string
	Description string
	BundleID    string
	Category    string
	Version     string
	FeedURL     string
}

// inspectApplication deliberately treats unavailable metadata as an empty view;
// callers decide which fields are required for their operation.
func inspectApplication(parent context.Context, path string) appInfo {
	metadata, _ := handler.NewMacApp(nil, nil).Inspect(parent, path)
	return appInfo{
		Path: metadata.Path, Name: metadata.Name, BundleID: metadata.BundleID,
		Category: metadata.Category, Description: metadata.Description,
		Version: metadata.Version, FeedURL: metadata.FeedURL,
	}
}

func (s Scanner) isDevelopmentApplication(info appInfo) bool {
	if strings.EqualFold(info.Category, "public.app-category.developer-tools") {
		return true
	}
	bundleID := strings.ToLower(info.BundleID)
	if builtin.MatchesMacAppCatalog(bundleID, info.Name) {
		return true
	}
	for _, configured := range s.bundleIDs {
		if bundleID != "" && bundleID == strings.ToLower(strings.TrimSpace(configured)) {
			return true
		}
	}
	return false
}

// ReconcileNewlyManagedBundleIDs registers custom Bundle IDs at the moment a
// bundle transitions into scan-managed ownership. Keeping this in the same
// save transaction prevents the following scan from filtering the app first.
func ReconcileNewlyManagedBundleIDs(ctx context.Context, previous, proposed model.Config) model.Config {
	previousApps := make(map[string]model.Application, len(previous.Apps))
	for _, app := range previous.Apps {
		previousApps[app.ID] = app
	}
	scanner := New(proposed.Settings.Scan)
	for _, app := range proposed.Apps {
		if err := ctx.Err(); err != nil {
			break
		}
		old, existed := previousApps[app.ID]
		if !app.ScanManaged || app.Type != model.ApplicationTypeBundle || (existed && old.ScanManaged) {
			continue
		}
		scanner.registerManagedBundleID(ctx, app)
	}
	proposed.Settings.Scan.BundleID = scanner.bundleIDs
	return proposed
}

func (s *Scanner) registerManagedBundleID(ctx context.Context, app model.Application) {
	info := inspectApplication(ctx, app.InstallPath)
	bundleID := strings.ToLower(strings.TrimSpace(info.BundleID))
	if bundleID == "" || (Scanner{}).isDevelopmentApplication(info) || containsFold(s.bundleIDs, bundleID) {
		return
	}
	s.bundleIDs = append(s.bundleIDs, bundleID)
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

// actionConfig creates the optional action container only when a merge needs it.
func actionConfig(application *model.Application) *model.ProviderActions {
	if application.Provider.Actions == nil {
		application.Provider.Actions = &model.ProviderActions{}
	}
	return application.Provider.Actions
}
