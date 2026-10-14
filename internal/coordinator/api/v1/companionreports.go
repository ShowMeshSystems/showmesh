package v1

// CompanionReportUploadRequest is the body of POST /companion-reports: one
// file a ShowMesh Companion collected on its own machine.
type CompanionReportUploadRequest struct {
	CompanionID   string `json:"companionId"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	FileName      string `json:"fileName"`
	CapturedAt    string `json:"capturedAt"`
	ContentSHA256 string `json:"contentSha256"`
	Content       string `json:"content"`
}

// CompanionReportSummary is one stored report without its content.
type CompanionReportSummary struct {
	ID            string `json:"id"`
	CompanionID   string `json:"companionId"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	FileName      string `json:"fileName"`
	CapturedAt    string `json:"capturedAt"`
	ReceivedAt    string `json:"receivedAt"`
	SizeBytes     int64  `json:"sizeBytes"`
	ContentSHA256 string `json:"contentSha256"`
}

// CompanionReportUploadResponse answers POST /companion-reports. Created is
// false when the companion had already uploaded identical content.
type CompanionReportUploadResponse struct {
	ServerTime string                 `json:"serverTime"`
	Created    bool                   `json:"created"`
	Report     CompanionReportSummary `json:"report"`
}

// CompanionReportListResponse answers GET /companion-reports, newest first.
type CompanionReportListResponse struct {
	ServerTime string                   `json:"serverTime"`
	Reports    []CompanionReportSummary `json:"reports"`
}

// CompanionReportResponse answers GET /companion-reports/{reportId}.
type CompanionReportResponse struct {
	ServerTime string                 `json:"serverTime"`
	Report     CompanionReportSummary `json:"report"`
	Content    string                 `json:"content"`
}
