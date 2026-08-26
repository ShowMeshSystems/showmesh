package v1

// This file is TRACK-H-H3-SPEC.md section 4's own wire shapes: what one
// node's resolved Cue catalog looks like on the wire (GET
// /nodes/{nodeId}/cue-catalog), and what acknowledging it looks like
// (POST .../cue-catalog/acknowledge). Both are read-only projections of
// internal/coordinator/assetsync's own resolver and store methods — this
// package holds no resolution logic of its own, matching every other v1
// file's "wire shape only" posture.

// CueCatalogRenderOutput is one Cue's resolved render output for the
// requested node. Filename is the runtime filename a node must open (and
// verify against AssetHashes) to render it; Sequence is a logical
// identity only, mirroring [cuecatalog.RenderOutput]'s own doc comment.
type CueCatalogRenderOutput struct {
	Sequence    string   `json:"sequence"`
	Filename    string   `json:"filename"`
	AssetHashes []string `json:"assetHashes"`
}

// CueCatalogAudioOutput is one Cue's resolved audio output. Filename
// mirrors CueCatalogRenderOutput.Filename one output over.
type CueCatalogAudioOutput struct {
	Asset             string   `json:"asset"`
	Filename          string   `json:"filename"`
	StartOffsetMillis int      `json:"startOffsetMillis"`
	AssetHashes       []string `json:"assetHashes"`
}

// CueCatalogLTCOutput is one Cue's resolved LTC output.
type CueCatalogLTCOutput struct {
	StartOffsetMillis int `json:"startOffsetMillis"`
}

// CueCatalogAnnouncementOutput is one Cue's resolved announcement policy.
type CueCatalogAnnouncementOutput struct {
	Policy     string   `json:"policy"`
	DuckGainDb *float64 `json:"duckGainDb,omitempty"`
	FadeMillis int      `json:"fadeMillis"`
}

// CueCatalogOutputs is one Cue's resolved outputs, restricted to the ones
// that concern the requested node (H3 spec section 3, point 3).
type CueCatalogOutputs struct {
	Render       *CueCatalogRenderOutput       `json:"render,omitempty"`
	Audio        *CueCatalogAudioOutput        `json:"audio,omitempty"`
	LTC          *CueCatalogLTCOutput          `json:"ltc,omitempty"`
	Announcement *CueCatalogAnnouncementOutput `json:"announcement,omitempty"`
}

// CueCatalogEntry is one Cue's row in a resolved catalog.
type CueCatalogEntry struct {
	CueID       string            `json:"cueId"`
	CueRevision int64             `json:"cueRevision"`
	Outputs     CueCatalogOutputs `json:"outputs"`
}

// CueCatalogResponse is GET /nodes/{nodeId}/cue-catalog's body. Configured
// is false exactly when no show.active is configured — the honest-absence
// case (H3 spec section 2): show, generation, and revision are all absent
// (the JSON zero value / omitted) and entries is an empty array, never a
// fabricated generation of 0 that could be mistaken for a real grant.
//
// AcknowledgedStatus/AcknowledgedRevision/AcknowledgedAt project
// [github.com/showmeshsystems/showmesh/internal/coordinator/store.NodeCueCatalogAckRecord],
// the SAME persisted acknowledgement CueCatalogAcknowledgeResponse and
// CueCatalogDeployResult already record, now made readable without
// performing either write. AcknowledgedStatus is one of the
// three [CueCatalogStatusCurrent]/[CueCatalogStatusStale]/
// [CueCatalogStatusNeverAcknowledged] values below and is always present;
// AcknowledgedRevision and AcknowledgedAt are both null exactly when
// AcknowledgedStatus is CueCatalogStatusNeverAcknowledged (the node has
// never acknowledged anything): a caller must read the status, never
// infer "never acknowledged" from a missing revision, matching this
// package's CueCatalogAcknowledgeResponse.CurrentRevision precedent for
// the identical class of mistake. Stale names both revisions: the
// acknowledged one here, and the currently required one in Revision
// above.
type CueCatalogResponse struct {
	ServerTime string            `json:"serverTime"`
	Node       string            `json:"node"`
	Configured bool              `json:"configured"`
	Show       string            `json:"show,omitempty"`
	Generation *int64            `json:"generation,omitempty"`
	Revision   string            `json:"revision,omitempty"`
	Entries    []CueCatalogEntry `json:"entries"`

	AcknowledgedStatus   string  `json:"acknowledgedStatus"`
	AcknowledgedRevision *string `json:"acknowledgedRevision,omitempty"`
	AcknowledgedAt       *string `json:"acknowledgedAt,omitempty"`
}

// CueCatalogAcknowledgeRequest is POST
// /nodes/{nodeId}/cue-catalog/acknowledge's body: the catalog revision
// (for the show and generation it was resolved from) the node reports
// holding right now.
type CueCatalogAcknowledgeRequest struct {
	Revision   string `json:"revision"`
	Show       string `json:"show"`
	Generation int64  `json:"generation"`
}

// CueCatalogAcknowledgeResponse is POST
// /nodes/{nodeId}/cue-catalog/acknowledge's response body.
//
// Status is "catalog-current" only when AcknowledgedRevision equals
// CurrentRevision (the revision the coordinator resolves RIGHT NOW, at
// acknowledgement time); otherwise "catalog-stale", naming both revisions
// (H3 spec section 4: "there is no partial state"). CurrentRevision is
// empty exactly when Configured is false — an unconfigured show.active has
// no revision to compare against, and an acknowledgement made under those
// conditions is always "catalog-stale" (there is no "current" for it to
// match).
//
// Acknowledging a catalog is explicitly NOT readiness (H3 spec section 4's
// own closing rule): this response says nothing about asset presence,
// which stays Track E's own manifest (GET /nodes/{nodeId}/assets).
type CueCatalogAcknowledgeResponse struct {
	ServerTime           string `json:"serverTime"`
	Node                 string `json:"node"`
	Configured           bool   `json:"configured"`
	Status               string `json:"status"`
	AcknowledgedRevision string `json:"acknowledgedRevision"`
	CurrentRevision      string `json:"currentRevision,omitempty"`
	AcknowledgedAt       string `json:"acknowledgedAt"`
}

// The members of CueCatalogAcknowledgeResponse.Status. CueCatalogResponse
// .AcknowledgedStatus reuses the same two for a node that has
// acknowledged something, and adds CueCatalogStatusNeverAcknowledged for
// one that never has, a state CueCatalogAcknowledgeResponse never needs
// since making that request IS an acknowledgement.
const (
	CueCatalogStatusCurrent           = "catalog-current"
	CueCatalogStatusStale             = "catalog-stale"
	CueCatalogStatusNeverAcknowledged = "catalog-unacknowledged"
)

// CueCatalogDeployResult is the outcome of dispatching cuecatalog.deploy
// to one node — the coordinator's own push half of TRACK-H-H3-SPEC.md
// section 4, complementing CueCatalogAcknowledgeResponse's pull/report
// half. Show, Generation, and Revision are always THIS coordinator's own
// resolution (assetsync.ResolveCueCatalog), never a caller-supplied value:
// there is no request body field for any of them. Outcome/Reason reuse
// pkg/mqttproto's own closed vocabulary ("confirmed", "unconfirmed",
// "refused", "failed") verbatim, since cuecatalog.deploy's node-side
// result already reports in exactly that vocabulary with no
// project-specific narrowing the way audio.session.* needed. Outcome is
// also "" and DispatchedAt is nil for a REPLAY of a command still in
// flight, the same accepted-empty case FPPCommandResult.outcome
// documents.
// AcknowledgedRevision is the revision the node reported holding, present
// only when Outcome is "confirmed".
type CueCatalogDeployResult struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Node           string `json:"node"`
	Replay         bool   `json:"replay"`

	Show       string `json:"show"`
	Generation int64  `json:"generation"`
	Revision   string `json:"revision"`

	Outcome              string `json:"outcome"`
	Reason               string `json:"reason,omitempty"`
	AcknowledgedRevision string `json:"acknowledgedRevision,omitempty"`

	DispatchedAt *string `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt,omitempty"`
}

// CueCatalogDeployResponse wraps CueCatalogDeployResult with the standard
// serverTime envelope (contract section 6.2).
type CueCatalogDeployResponse struct {
	ServerTime string                 `json:"serverTime"`
	Command    CueCatalogDeployResult `json:"command"`
}

// CueAuthorizationOutcome documents, read-only, TRACK-H-H3-SPEC.md section
// 6's seven refusal reasons for checking a Cue authorization tuple
// (pkg/cueauth.Outcome's own wire spellings): "cross-show",
// "stale-generation", "unknown-generation", "stale-catalog", "unknown-cue",
// "stale-cue", "asset-missing". The Go package (pkg/cueauth)
// owns this vocabulary; this package only projects its spelling, matching
// fppreconciliation.go's identical posture for fppreconcile.Outcome. No
// route in this seam emits it yet — H4 owns wiring an activation or
// dispatch refusal onto the wire — but H3 spec section 6 fixes the
// vocabulary now, as a closed decision, so H4 has one thing to project
// rather than one to invent.
type CueAuthorizationOutcome = string
