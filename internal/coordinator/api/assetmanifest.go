package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is Track E seam E5's own HTTP surface: GET /assets/manifest
// (every declared node) and GET /nodes/{nodeId}/assets (one node).
// internal/coordinator/assetsync.ComputeNodeManifest — reached here only
// through BuildManifest/BuildNodeManifest — is the ONLY function in this
// codebase permitted to decide a node's asset readiness (see that
// package's own doc comment); this file fetches the coordinator's current
// time, calls it, and maps its result onto the wire. It adds no second
// readiness rule.

// unwiredAssetManifestReason is what both handlers render when
// [Dependencies.AssetManifests] is nil — an embedder that has not wired a
// *store.Store in for this seam. There is no safe no-op *store.Store to
// default to (unlike every interface-typed dependency in this package),
// so both handlers check for nil explicitly and render "unknown" with
// this reason, rather than reaching into a nil pointer.
const unwiredAssetManifestReason = "this coordinator has no asset manifest data source wired in"

// --- GET /assets/manifest ---

func (h *handlers) handleAssetManifest(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	if h.deps.AssetManifests == nil {
		jsonWrite(w, v1.AssetManifestResponse{ServerTime: formatTime(now), Nodes: []v1.NodeAssetManifest{}})
		return
	}

	manifests, err := assetsync.BuildManifest(r.Context(), h.deps.AssetManifests, now, h.deps.AssetInventoryInterval)
	if err != nil {
		h.writeInternalError(w, now, "build asset manifest", err)
		return
	}
	out := make([]v1.NodeAssetManifest, 0, len(manifests))
	for _, m := range manifests {
		out = append(out, mapNodeAssetManifest(m))
	}
	jsonWrite(w, v1.AssetManifestResponse{ServerTime: formatTime(now), Nodes: out})
}

// --- GET /nodes/{nodeId}/assets ---

func (h *handlers) handleNodeAssetManifest(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}
	if !h.nodeDeclared(r.Context())(nodeID) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("no declared node with id "+strconv.Quote(nodeID)))
		return
	}

	if h.deps.AssetManifests == nil {
		reason := unwiredAssetManifestReason
		jsonWrite(w, v1.NodeAssetManifestResponse{ServerTime: formatTime(now), Manifest: v1.NodeAssetManifest{
			Node: nodeID, State: string(assetsync.ManifestUnknown), Reason: &reason,
			Missing: []v1.MissingAsset{}, Gaps: []v1.AssetGap{}, Extra: []v1.ExtraAsset{},
		}})
		return
	}

	m, err := assetsync.BuildNodeManifest(r.Context(), h.deps.AssetManifests, now, h.deps.AssetInventoryInterval, nodeID)
	if err != nil {
		h.writeInternalError(w, now, "build node asset manifest", err)
		return
	}
	jsonWrite(w, v1.NodeAssetManifestResponse{ServerTime: formatTime(now), Manifest: mapNodeAssetManifest(m)})
}

// --- mapping: assetsync.NodeManifest -> v1 wire types ---

// mapNodeAssetManifest renders one assetsync.NodeManifest verdict onto the
// wire. State and the Missing/Gaps/Extra contents are exactly what
// assetsync computed — this function classifies nothing itself. The one
// thing it adds is Reason for the NotReady case: assetsync.NodeManifest's
// own Reason field is set ONLY for State == Unknown (see that type's doc
// comment — "UnknownCause and Reason are set only when State ==
// ManifestUnknown"), but ADR-020 requires "reason... always present when
// state is not ready", so notReadyReason below summarizes the SAME
// Missing/Gaps data this response already carries into one sentence. It
// is presentation of assetsync's own output, not a second opinion about
// what is missing.
func mapNodeAssetManifest(m assetsync.NodeManifest) v1.NodeAssetManifest {
	out := v1.NodeAssetManifest{
		Node:  m.NodeID,
		State: string(m.State),
	}

	missing := make([]v1.MissingAsset, 0, len(m.Missing))
	for _, a := range m.Missing {
		missing = append(missing, v1.MissingAsset{
			AssetID: a.AssetID, Sequence: a.SequenceID, Filename: a.Filename,
			ContentHash: a.ContentHash, SizeBytes: a.SizeBytes,
		})
	}
	out.Missing = missing

	gaps := make([]v1.AssetGap, 0, len(m.Gaps))
	for _, g := range m.Gaps {
		gaps = append(gaps, v1.AssetGap{Sequence: g.SequenceID, Surfaces: append([]string{}, g.SurfaceIDs...)})
	}
	out.Gaps = gaps

	extra := make([]v1.ExtraAsset, 0, len(m.Extra))
	for _, e := range m.Extra {
		extra = append(extra, v1.ExtraAsset{ContentHash: e.ContentHash, Filename: e.Filename, SizeBytes: e.SizeBytes})
	}
	out.Extra = extra

	switch m.State {
	case assetsync.ManifestUnknown:
		reason := m.Reason
		out.Reason = &reason
		// ObservedAt stays nil: assetsync.NodeManifest's own doc comment —
		// "the zero time when State is Unknown... there is no evidence an
		// Unknown verdict rests on, so there is nothing to date it by."
	case assetsync.ManifestNotReady:
		reason := notReadyReason(missing, gaps)
		out.Reason = &reason
		observedAt := formatTime(m.ObservedAt)
		out.ObservedAt = &observedAt
	default: // ManifestReady
		observedAt := formatTime(m.ObservedAt)
		out.ObservedAt = &observedAt
	}

	return out
}

// notReadyReason summarizes ALREADY-COMPUTED missing/gap counts into one
// operator-facing sentence — see mapNodeAssetManifest's own doc comment
// for why this exists at the wire layer instead of in assetsync.
func notReadyReason(missing []v1.MissingAsset, gaps []v1.AssetGap) string {
	var parts []string
	if n := len(missing); n > 0 {
		parts = append(parts, fmt.Sprintf("missing %d expected asset(s)", n))
	}
	if n := len(gaps); n > 0 {
		parts = append(parts, fmt.Sprintf("%d sequence(s) with no coverage on this node at all", n))
	}
	return strings.Join(parts, "; ")
}
