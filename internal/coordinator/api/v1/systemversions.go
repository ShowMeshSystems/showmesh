package v1

// SystemVersionsResponse is the 200 body of GET /api/v1/system/versions.
type SystemVersionsResponse struct {
	ServerTime  string                   `json:"serverTime"`
	Coordinator SystemCoordinatorVersion `json:"coordinator"`
	Nodes       []SystemComponentVersion `json:"nodes"`
	Plugins     []SystemComponentVersion `json:"plugins"`
	Release     SystemRelease            `json:"release"`
}

// SystemCoordinatorVersion is the version this coordinator was built as.
type SystemCoordinatorVersion struct {
	Version string `json:"version"`
}

// SystemComponentVersion is one node's agent or one FPP player's plugin.
// Reason says why Version is null. Warning is set only when Mismatch is true.
type SystemComponentVersion struct {
	ID       string  `json:"id"`
	Version  *string `json:"version"`
	Reason   *string `json:"reason"`
	Mismatch bool    `json:"mismatch"`
	Warning  *string `json:"warning"`
}

// SystemRelease is the cached answer of the newest-release check. Tag,
// PublishedAt, InstallCommand and CheckedAt are null until a check has answered.
type SystemRelease struct {
	CheckEnabled         bool    `json:"checkEnabled"`
	CheckIntervalSeconds int64   `json:"checkIntervalSeconds"`
	CheckedAt            *string `json:"checkedAt"`
	Tag                  *string `json:"tag"`
	PublishedAt          *string `json:"publishedAt"`
	InstallCommand       *string `json:"installCommand"`
	// Available is true when Tag is a later version than the coordinator's.
	Available bool `json:"available"`
	// Problem says why the latest check produced no answer, null when it did.
	Problem *string `json:"problem"`
}
