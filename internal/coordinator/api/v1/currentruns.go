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
