package v1

// Current-runs wire types are runner-neutral. A response can contain no runs,
// one FPP run, one or more showmesh-audio runs, or both runner families at
// once. Nil Next is the explicit answer that no authoritative next item is
// available; clients must not infer it from playlist order.

type CurrentRunsResponse struct {
	ServerTime string             `json:"serverTime"`
	ActiveShow CurrentShowContext `json:"activeShow"`
	Runs       []CurrentRun       `json:"runs"`
}

type CurrentShowContext struct {
	Configured bool    `json:"configured"`
	Show       *string `json:"show"`
	Generation *int64  `json:"generation"`
}

type CurrentRun struct {
	ID               string                `json:"id"`
	Runner           string                `json:"runner"`
	Show             string                `json:"show"`
	Generation       int64                 `json:"generation"`
	PlaylistID       string                `json:"playlistId"`
	PlaylistRevision int64                 `json:"playlistRevision"`
	Status           string                `json:"status"`
	StatusReason     string                `json:"statusReason"`
	Playback         CurrentPlayback       `json:"playback"`
	Freshness        CurrentRunFreshness   `json:"freshness"`
	Reconciliation   CurrentReconciliation `json:"reconciliation"`
	Activation       CurrentRunActivation  `json:"activation"`
	Targets          []CurrentRunTarget    `json:"targets"`
	Next             *CurrentRunNext       `json:"next"`
}

type CurrentPlayback struct {
	State      string     `json:"state"`
	Reason     string     `json:"reason"`
	ItemID     string     `json:"itemId"`
	ItemIndex  *int       `json:"itemIndex"`
	PositionMs *int64     `json:"positionMs"`
	Media      string     `json:"media"`
	Evidence   []Evidence `json:"evidence"`
}

type CurrentRunFreshness struct {
	State       string  `json:"state"`
	Reason      string  `json:"reason"`
	ObservedAt  *string `json:"observedAt"`
	CollectedAt *string `json:"collectedAt"`
}

type CurrentReconciliation struct {
	State  string `json:"state"`
	Reason string `json:"reason"`

	// OperatorInstruction is populated only for an fpp-runner run whose
	// fppreconcile.Outcome.IsMismatch is true (State one of stale-import,
	// unknown-entry, evidence-mismatch, cross-show), and absent for every
	// other run -- including a showmesh-audio run, whose own State can
	// independently read "stale-import" for an unrelated playlist-revision
	// check (currentruns.go's audioSessionReconciliation) that carries no
	// FPP-specific remedy and must never be told to "restart FPP". A
	// one-sentence, operator-facing notice naming both remedies when
	// present. Reported ADDITIVELY, the collapsed form of
	// FPPPlaylistEntryReconciliationResponse.OperatorInstruction
	// (fppreconciliation.go), matching that field's own EvidenceBrokenAt
	// precedent: a notice only, never a change to the configured mismatch
	// policy's own dispatch effect.
	OperatorInstruction string `json:"operatorInstruction,omitempty"`
}

type CurrentRunActivation struct {
	Show       string `json:"show"`
	Generation int64  `json:"generation"`
	PlaylistID string `json:"playlistId"`
	Revision   int64  `json:"revision"`
	Runner     string `json:"runner"`
}

type CurrentRunTarget struct {
	Kind     string     `json:"kind"`
	ID       string     `json:"id"`
	Evidence []Evidence `json:"evidence"`
}

type CurrentRunNext struct {
	ItemID    string `json:"itemId"`
	ItemIndex int    `json:"itemIndex"`
	Media     string `json:"media"`
	Source    string `json:"source"`
}

type CurrentRunsChangedEvent struct {
	Seq        uint64             `json:"seq"`
	ServerTime string             `json:"serverTime"`
	ActiveShow CurrentShowContext `json:"activeShow"`
	Runs       []CurrentRun       `json:"runs"`
}
