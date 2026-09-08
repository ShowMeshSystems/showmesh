package v1

import "encoding/json"

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

// FallbackProgramBody documents the shape of FallbackProgramResponse's
// "program" member for API consumers (openapi.yaml's $ref target); it is
// never itself constructed or marshaled by this codebase's own HTTP
// handler. handleGetFallbackProgram serves the coordinator's STORED
// program bytes verbatim, extracted as raw JSON from what
// internal/coordinator/fallbackreconcile wrote, rather than decoding
// into a Go struct and re-marshaling one. Re-deriving a byte sequence
// through any Go type, including this one, would risk producing bytes
// that no longer canonicalize identically to what was actually signed
// (Go's encoding/json distinguishes a nil slice, marshaled as `null`,
// from an empty one, marshaled as `[]`, two different JSON values that
// canonicalize to two different byte sequences), which would defeat the
// signature at the last hop: a receiver that re-canonicalizes what it
// was served must get back exactly the bytes
// [github.com/showmeshsystems/showmesh/pkg/fallbackprogram.Program.CanonicalBytes]
// produced when the coordinator signed it. SignatureBase64 is
// deliberately NOT a member of this type: the signature is computed over
// the program's own canonical bytes and can never be part of what it
// signs, so it travels as FallbackProgramResponse's own sibling field,
// never nested inside this shape.
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
}

// FallbackProgramResponse is GET
// /fallback-programs/{fppInstanceId}'s body. Published is false exactly
// when this coordinator has never successfully compiled and published a
// program for this host, the honest-absence case, matching
// CueCatalogResponse.Configured's identical posture next door. Program
// and SignatureBase64 are both present, or both absent, together: see
// [FallbackProgramBody]'s own doc comment for why Program carries the
// coordinator's stored bytes verbatim rather than a re-derived DTO, and
// why the signature is this type's own sibling field rather than nested
// inside Program.
type FallbackProgramResponse struct {
	ServerTime      string          `json:"serverTime"`
	FPPInstanceUUID string          `json:"fppInstanceUuid"`
	Published       bool            `json:"published"`
	Program         json.RawMessage `json:"program,omitempty"`
	SignatureBase64 string          `json:"signatureBase64,omitempty"`

	AcknowledgedStatus  string  `json:"acknowledgedStatus"`
	AcknowledgedPackage *string `json:"acknowledgedPackageId,omitempty"`
	AcknowledgedAt      *string `json:"acknowledgedAt,omitempty"`
}

// The members of FallbackProgramResponse.AcknowledgedStatus.
// FallbackProgramStatusCurrent/Stale/NeverAcknowledged are on
// CueCatalogStatus's identical three-way vocabulary; ADR-048 decision 1
// adds a fourth member that vocabulary has no equivalent for: a host can
// actively REJECT a package (report verificationResult other than
// "verified"), which is a fact about the host's own evidence, never
// collapsed into "current" merely because the acknowledged packageId
// happens to match. A missing, stale, mismatched, or unacknowledged
// package is a readiness failure per ADR-048; reporting a rejected
// package as current would be dishonest evidence.
const (
	FallbackProgramStatusCurrent           = "fallback-program-current"
	FallbackProgramStatusStale             = "fallback-program-stale"
	FallbackProgramStatusRejected          = "fallback-program-rejected"
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
