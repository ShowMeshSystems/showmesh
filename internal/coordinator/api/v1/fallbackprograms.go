package v1

// This file is Track J's J1 own wire shapes under
// /api/v1/fallback-programs (ADR-048): a listing, a per-FPP-host current
// program read, and an acknowledgement write. It holds no compilation or
// resolution logic of its own, internal/coordinator/fallbackcompile and
// internal/coordinator/fallbackreconcile own that, matching every other
// v1 file's "wire shape only" posture.

// FallbackProgramRenderActivation mirrors
// [github.com/showmeshsystems/showmesh/pkg/fallbackprogram.RenderActivation].
type FallbackProgramRenderActivation struct {
	Sequence    string   `json:"sequence"`
	Filename    string   `json:"filename"`
	AssetHashes []string `json:"assetHashes"`
}

// FallbackProgramAudioActivation mirrors
// [github.com/showmeshsystems/showmesh/pkg/fallbackprogram.AudioActivation].
type FallbackProgramAudioActivation struct {
	Asset                string   `json:"asset"`
	Filename             string   `json:"filename"`
	StartOffsetMillis    int      `json:"startOffsetMillis"`
	AssetHashes          []string `json:"assetHashes"`
	LTCStartOffsetMillis *int     `json:"ltcStartOffsetMillis,omitempty"`
}

// FallbackProgramTarget mirrors
// [github.com/showmeshsystems/showmesh/pkg/fallbackprogram.NodeTarget].
type FallbackProgramTarget struct {
	NodeID string                           `json:"nodeId"`
	Render *FallbackProgramRenderActivation `json:"render,omitempty"`
	Audio  *FallbackProgramAudioActivation  `json:"audio,omitempty"`
}

// FallbackProgramEntry mirrors
// [github.com/showmeshsystems/showmesh/pkg/fallbackprogram.EntryMapping].
type FallbackProgramEntry struct {
	EntryKey    string                  `json:"entryKey"`
	CueID       string                  `json:"cueId"`
	CueRevision int64                   `json:"cueRevision"`
	Targets     []FallbackProgramTarget `json:"targets"`
}

// FallbackProgramRules mirrors
// [github.com/showmeshsystems/showmesh/pkg/fallbackprogram.Rules].
type FallbackProgramRules struct {
	FallbackBoundary string `json:"fallbackBoundary"`
	RestHold         string `json:"restHold"`
	LocalShutdown    string `json:"localShutdown"`
	RecoveryBoundary string `json:"recoveryBoundary"`
}

// FallbackProgramBody is the signed program itself, as delivered on the
// wire: the same shape
// [github.com/showmeshsystems/showmesh/pkg/fallbackprogram.SignedProgram]
// carries, so an FPP host can verify SignatureBase64 against exactly the
// canonicalized Program fields present here.
type FallbackProgramBody struct {
	SchemaVersion int    `json:"schemaVersion"`
	PackageID     string `json:"packageId"`
	Revision      string `json:"revision"`
	ExpiresAt     string `json:"expiresAt"`
	CompiledAt    string `json:"compiledAt"`

	FPPInstanceUUID string `json:"fppInstanceUuid"`
	Show            string `json:"show"`
	Generation      int64  `json:"generation"`

	PlaylistRevisions map[string]int64       `json:"playlistRevisions"`
	CatalogRevisions  map[string]string      `json:"catalogRevisions"`
	Entries           []FallbackProgramEntry `json:"entries"`
	Rules             FallbackProgramRules   `json:"rules"`

	SignatureBase64 string `json:"signatureBase64"`
}

// FallbackProgramResponse is GET
// /fallback-programs/{fppInstanceId}'s body. Published is false exactly
// when this coordinator has never successfully compiled and published a
// program for this host, the honest-absence case, matching
// CueCatalogResponse.Configured's identical posture next door.
type FallbackProgramResponse struct {
	ServerTime      string               `json:"serverTime"`
	FPPInstanceUUID string               `json:"fppInstanceUuid"`
	Published       bool                 `json:"published"`
	Program         *FallbackProgramBody `json:"program,omitempty"`

	AcknowledgedStatus  string  `json:"acknowledgedStatus"`
	AcknowledgedPackage *string `json:"acknowledgedPackageId,omitempty"`
	AcknowledgedAt      *string `json:"acknowledgedAt,omitempty"`
}

// The members of FallbackProgramResponse.AcknowledgedStatus, on
// CueCatalogStatus's identical three-way vocabulary.
const (
	FallbackProgramStatusCurrent           = "fallback-program-current"
	FallbackProgramStatusStale             = "fallback-program-stale"
	FallbackProgramStatusNeverAcknowledged = "fallback-program-unacknowledged"
)

// FallbackProgramListEntry is one row of GET /fallback-programs, metadata
// only, never the signed payload itself, matching
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 3.6's identical
// list-metadata-only/read-full-by-id split for playlist definitions.
type FallbackProgramListEntry struct {
	FPPInstanceUUID string `json:"fppInstanceUuid"`
	PackageID       string `json:"packageId"`
	Revision        string `json:"revision"`
	Show            string `json:"show"`
	Generation      int64  `json:"generation"`
	ExpiresAt       string `json:"expiresAt"`
	CompiledAt      string `json:"compiledAt"`
}

// FallbackProgramListResponse is GET /fallback-programs's body.
type FallbackProgramListResponse struct {
	ServerTime string                     `json:"serverTime"`
	Programs   []FallbackProgramListEntry `json:"programs"`
}

// FallbackProgramAcknowledgeRequest is POST
// /fallback-programs/{fppInstanceId}/acknowledge's body (ADR-048 decision
// 1: "The plugin reports the package id, revision, verification result,
// installed time, and age."). Age is not a field: it is derived from
// InstalledAt at read time, never accepted as a caller-supplied value
// that could disagree with the clock that received it.
type FallbackProgramAcknowledgeRequest struct {
	PackageID          string `json:"packageId"`
	Revision           string `json:"revision"`
	VerificationResult string `json:"verificationResult"`
	InstalledAt        string `json:"installedAt"`
}

// The closed vocabulary FallbackProgramAcknowledgeRequest.VerificationResult
// accepts. "verified" is the plugin actually having checked the signature
// and accepted it; the other two are refusal evidence the plugin reports
// about itself, so the coordinator (and an operator reading readiness)
// can tell "never fetched" apart from "fetched, but rejected."
const (
	FallbackProgramVerificationVerified          = "verified"
	FallbackProgramVerificationSignatureInvalid  = "signature-invalid"
	FallbackProgramVerificationMismatchedProgram = "mismatched-program"
)

// FallbackProgramAcknowledgeResponse is POST
// /fallback-programs/{fppInstanceId}/acknowledge's response body.
type FallbackProgramAcknowledgeResponse struct {
	ServerTime      string `json:"serverTime"`
	FPPInstanceUUID string `json:"fppInstanceUuid"`
	AcknowledgedAt  string `json:"acknowledgedAt"`
}
