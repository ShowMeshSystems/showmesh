package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// nightRestingRenderRetry is how long a node that did not take the resting
// render activation waits before it is sent again.
var nightRestingRenderRetry = 15 * time.Second

// nightRestingRenderMaxAttempts bounds the resends for one resting period.
const nightRestingRenderMaxAttempts = 8

// nightRestingRender is one resting period's resting render progress.
type nightRestingRender struct {
	fppFilename string
	attempts    int
	inFlight    bool
	nextAt      time.Time
	done        map[string]bool
	finished    bool
}

// nightRestingRenderKey names one resting period: a new cycle, state or
// playlist start is a new period that renders again.
func nightRestingRenderKey(rec store.NightSessionRecord, anchor nightContentAnchor) string {
	return rec.ID + ":" + strconv.FormatInt(rec.Cycle, 10) + ":" + rec.State + ":" + anchor.DispatchedAt.UTC().Format(time.RFC3339Nano)
}

// nightAdvanceRestingRender sends the resting render activation (ADR-061
// decision 3) once FPP is observed playing the resting sequence from the
// resting playlist. The caller has already ruled out a weather delay and a
// stop hold.
func (h *handlers) nightAdvanceRestingRender(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	switch rec.State {
	case nightStatePreshow, nightStateRestingIntershow, nightStateEndOfNightResting:
	default:
		return
	}
	if h.deps.AssetManifests == nil || h.deps.Commands == nil {
		return
	}
	anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON)
	if !has || anchor.ObservedAt.IsZero() ||
		(anchor.Purpose != nightAnchorPurposeRestingOneShot && anchor.Purpose != nightAnchorPurposeRestingRepeat) {
		return
	}
	key := nightRestingRenderKey(rec, anchor)

	h.nightRestingRenderMu.Lock()
	if h.nightRestingRenders == nil {
		h.nightRestingRenders = make(map[string]*nightRestingRender, 1)
	}
	st := h.nightRestingRenders[key]
	if st == nil {
		st = &nightRestingRender{done: map[string]bool{}}
		h.nightRestingRenders[key] = st
	}
	busy := st.finished || st.inFlight || now.Before(st.nextAt)
	fppFilename := st.fppFilename
	h.nightRestingRenderMu.Unlock()
	if busy {
		return
	}

	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: resting render: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}
	sequence := payload.Resting.TimelineAsset.Sequence
	if fppFilename == "" {
		res := nightResolveFSEQDuration(ctx, h.deps, h.deps.Assets, payload.Show, payload.Resting.TimelineAsset)
		if res.Filename == "" {
			return
		}
		fppFilename = res.Filename
	}
	obs := nightObservePlayback(ctx, h.deps.Observations, anchor.FPPInstanceID, anchor.DispatchedAt, now)
	if !nightRestingSequencePlaying(obs, anchor, fppFilename) {
		h.nightRestingRenderMu.Lock()
		st.fppFilename = fppFilename
		h.nightRestingRenderMu.Unlock()
		return
	}

	h.nightRestingRenderMu.Lock()
	st.fppFilename = fppFilename
	st.inFlight = true
	st.attempts++
	attempt := st.attempts
	done := make(map[string]bool, len(st.done))
	for id := range st.done {
		done[id] = true
	}
	h.nightRestingRenderMu.Unlock()

	evidence := nightRestingRenderEvidence{
		key: key, attempt: attempt, show: payload.Show, sequence: sequence,
		runnerInstance: anchor.FPPInstanceID, positionMS: obs.PositionMS, evidenceAt: anchor.ObservedAt,
	}
	issuer := nightControllerIssuer(rec)
	h.nightRestingRenderWG.Add(1)
	go func() {
		defer h.nightRestingRenderWG.Done()
		defer func() {
			if r := recover(); r != nil {
				h.logWarn("night loop: resting render: dispatch panicked; recovered", "sessionId", rec.ID, "panic", r)
			}
		}()
		confirmed, pending := h.nightDispatchRestingRender(ctx, now, evidence, done, cueActivationIssuer{
			PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName, Form: issuer.Form, CredentialID: issuer.CredentialID,
		})
		h.nightRestingRenderMu.Lock()
		defer h.nightRestingRenderMu.Unlock()
		st.inFlight = false
		for _, id := range confirmed {
			st.done[id] = true
		}
		switch {
		case pending == 0:
			st.finished = true
		case st.attempts >= nightRestingRenderMaxAttempts:
			st.finished = true
			h.logWarn("night loop: resting render: some render nodes never took the resting sequence; giving up for this resting period", "sessionId", rec.ID, "nodes", pending)
		default:
			st.nextAt = h.now().Add(nightRestingRenderRetry)
		}
	}()
}

// nightRestingSequencePlaying reports current evidence that FPP plays
// fppFilename from the resting playlist the anchor started.
func nightRestingSequencePlaying(obs nightPlaybackObservation, anchor nightContentAnchor, fppFilename string) bool {
	return obs.Current && obs.Status == fppStatusValuePlaying &&
		obs.PlaylistCurrent && obs.Playlist == anchor.Playlist &&
		obs.ItemCurrent && fppFilename != "" && obs.Item == fppFilename
}

// nightRestingRenderEvidence is what one resting render dispatch carries.
type nightRestingRenderEvidence struct {
	key, show, sequence, runnerInstance string
	attempt                             int
	positionMS                          int64
	evidenceAt                          time.Time
}

// nightDispatchRestingRender sends the resting render activation to every
// node whose catalog holds the resting entry and has not yet confirmed it.
// It returns the nodes that confirmed and how many still need it.
func (h *handlers) nightDispatchRestingRender(ctx context.Context, now time.Time, ev nightRestingRenderEvidence, done map[string]bool, issuer cueActivationIssuer) (confirmed []string, pending int) {
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil || !active.Configured || active.ShowID != ev.show {
		return nil, 0
	}
	nodes, err := h.deps.AssetManifests.ListNodes(ctx)
	if err != nil {
		h.logWarn("night loop: resting render: list nodes failed", "error", err)
		return nil, 1
	}
	activations := make(map[string]cueactivation.Activation)
	for _, n := range nodes {
		if done[n.NodeID] {
			continue
		}
		catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, n.NodeID)
		if err != nil {
			h.logWarn("night loop: resting render: resolve cue catalog failed", "nodeId", n.NodeID, "error", err)
			pending++
			continue
		}
		if catalog.Resting == nil || catalog.Resting.Outputs.Render == nil || catalog.Resting.Outputs.Render.Sequence != ev.sequence {
			continue
		}
		activations[n.NodeID] = cueactivation.Activation{
			Runner: "fpp", RunnerInstance: ev.runnerInstance,
			ActivationID:    nightRestingRenderActivationID(n.NodeID, catalog.Revision, ev),
			Show:            active.ShowID,
			Generation:      active.Generation,
			CatalogRevision: catalog.Revision,
			CueID:           catalog.Resting.CueID,
			CueRevision:     catalog.Resting.CueRevision,
			PositionMS:      max(ev.positionMS, 0),
			EvidenceAt:      ev.evidenceAt,
		}
	}
	if len(activations) == 0 {
		return nil, pending
	}
	for _, out := range dispatchCueActivationsConcurrently(activations, func(nodeID string, act cueactivation.Activation) cueActivationDispatchOutcome {
		return h.dispatchOneCueActivation(ctx, now, nodeID, act, issuer, nil)
	}) {
		if out.Confirmed {
			confirmed = append(confirmed, out.NodeID)
			continue
		}
		pending++
		h.logWarn("night loop: resting render: node did not take the resting sequence",
			"nodeId", out.NodeID, "authorizeOutcome", out.AuthorizeOutcome, "authorizeReason", out.AuthorizeReason,
			"nodeOutcome", out.NodeOutcome, "error", out.Err)
	}
	return confirmed, pending
}

// nightRestingRenderActivationID is unique per node, catalog revision,
// resting period and attempt, so a resend is a new command, not a replay.
func nightRestingRenderActivationID(nodeID, catalogRevision string, ev nightRestingRenderEvidence) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("resting-render\x00%s\x00%s\x00%s\x00%d", nodeID, catalogRevision, ev.key, ev.attempt)))
	return "resting-" + hex.EncodeToString(sum[:16])
}
