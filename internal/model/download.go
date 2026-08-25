package model

// DownloadAssetChoices contains one provider's preflight candidates. When
// SelectionRequired is true, presentation layers must not silently choose even
// a single candidate because the provider could not infer host compatibility.
type DownloadAssetChoices struct {
	Candidates        []string
	SelectionRequired bool
}

// DownloadAssetPreflightStage identifies a provider-candidate lookup milestone.
type DownloadAssetPreflightStage string

const (
	DownloadAssetPreflightStarted   DownloadAssetPreflightStage = "started"
	DownloadAssetPreflightCompleted DownloadAssetPreflightStage = "completed"
	DownloadAssetPreflightFailed    DownloadAssetPreflightStage = "failed"
)

// DownloadAssetPreflightProgress reports read-only candidate lookup progress.
type DownloadAssetPreflightProgress struct {
	AppID          string
	AppName        string
	Stage          DownloadAssetPreflightStage
	CandidateCount int
	Err            error
}
