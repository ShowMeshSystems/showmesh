package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// assetSyncServiceSatisfiesAssetSyncNudger is a compile-time assertion that
// *assetsync.Service already satisfies [AssetSyncNudger] with no adapter
// needed — the identical property [storeSatisfiesCommandStore] (api.go)
// notes for *store.Store.
var _ AssetSyncNudger = (*assetsync.Service)(nil)

// assetSyncServiceSatisfiesAssetSettingsSource is Track G seam G-4's own
// compile-time assertion, alongside the one above: *assetsync.Service's
// ContentBaseURL/MaxUploadBytes/InventoryInterval methods already satisfy
// [AssetSettingsSource] with no adapter needed, which is what lets
// coordinator.go wire the SAME Service value into both
// [Dependencies.AssetSettings] and [Dependencies.AssetSyncNudger].
var _ AssetSettingsSource = (*assetsync.Service)(nil)

// assetSyncServiceSatisfiesAssetFetchFailureSource is this seam's own
// compile-time assertion, alongside the two above: *assetsync.Service's
// LastFetchFailure method already satisfies [AssetFetchFailureSource] with
// no adapter needed.
var _ AssetFetchFailureSource = (*assetsync.Service)(nil)

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
		out, err := h.unwiredAssetManifestNodes(r.Context())
		if err != nil {
			h.writeInternalError(w, now, "list declared nodes", err)
			return
		}
		jsonWrite(w, v1.AssetManifestResponse{ServerTime: formatTime(now), Nodes: out})
		return
	}

	manifests, err := assetsync.BuildManifest(r.Context(), h.deps.AssetManifests, now, h.deps.AssetSettings.InventoryInterval())
	if err != nil {
		h.writeInternalError(w, now, "build asset manifest", err)
		return
	}
	syncEnabled := h.deps.AssetSettings.ContentBaseURL() != ""
	out := make([]v1.NodeAssetManifest, 0, len(manifests))
	for _, m := range manifests {
		out = append(out, mapNodeAssetManifest(m, syncEnabled, h.deps.AssetFetchFailures))
	}
	jsonWrite(w, v1.AssetManifestResponse{ServerTime: formatTime(now), Nodes: out})
}

// unwiredAssetManifestNodes is [handleAssetManifest]'s AssetManifests==nil
// path: it still enumerates every declared node (through
// [Dependencies.Discovery], independently wired) and renders each as
// "unknown" with [unwiredAssetManifestReason], the SAME verdict
// [handleNodeAssetManifest] already gives one node at a time. Before this,
// the fleet route rendered an EMPTY node list instead — "nothing is wrong"
// rather than "I cannot tell" — which made `showmeshctl assets manifest
// --require-ready` exit 0 for a coordinator that could not actually answer
// the question. An empty result here now means only "this coordinator has
// no declared nodes to report on", never "everything is fine".
func (h *handlers) unwiredAssetManifestNodes(ctx context.Context) ([]v1.NodeAssetManifest, error) {
	decls, err := h.deps.Discovery.ListNodeDeclarations(ctx)
	if err != nil {
		return nil, err
	}
	reason := unwiredAssetManifestReason
	out := make([]v1.NodeAssetManifest, 0, len(decls))
	for _, d := range decls {
		out = append(out, v1.NodeAssetManifest{
			Node: d.NodeID, State: string(assetsync.ManifestUnknown), Reason: &reason,
			Missing: []v1.MissingAsset{}, Gaps: []v1.AssetGap{}, Extra: []v1.ExtraAsset{},
		})
	}
	return out, nil
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

	m, err := assetsync.BuildNodeManifest(r.Context(), h.deps.AssetManifests, now, h.deps.AssetSettings.InventoryInterval(), nodeID)
	if err != nil {
		h.writeInternalError(w, now, "build node asset manifest", err)
		return
	}
	jsonWrite(w, v1.NodeAssetManifestResponse{ServerTime: formatTime(now), Manifest: mapNodeAssetManifest(m, h.deps.AssetSettings.ContentBaseURL() != "", h.deps.AssetFetchFailures)})
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
//
// syncEnabled is [Dependencies.AssetSyncEnabled]: when false and State is
// NotReady, notReadyReason appends [assetSyncDisabledNote] — the promise
// config.Config.AssetContentBaseURL's own doc comment and assetsync/sync.go's
// startup log line both make ("the asset manifest states it as the reason
// no node can be confirmed ready"), unplumbed until now. State itself is
// unaffected: a node with an unset content base URL is still genuinely
// not_ready, never ready and never a different state, per this seam's own
// "do not change its state" instruction.
func mapNodeAssetManifest(m assetsync.NodeManifest, syncEnabled bool, failures AssetFetchFailureSource) v1.NodeAssetManifest {
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
		reason := notReadyReason(m.NodeID, missing, gaps, syncEnabled, failures)
		out.Reason = &reason
		observedAt := formatTime(m.ObservedAt)
		out.ObservedAt = &observedAt
	default: // ManifestReady
		observedAt := formatTime(m.ObservedAt)
		out.ObservedAt = &observedAt
	}

	return out
}

// assetSyncDisabledNote is notReadyReason's appended sentence when
// assets.settings' contentBaseUrl is unset — see [mapNodeAssetManifest]'s
// syncEnabled doc. It states the missing assets will never arrive over the
// network, not merely that they are currently absent, matching
// assetsync/sync.go's own Run-disabled log line in substance. Track G seam
// G-4 moved contentBaseUrl from SHOWMESH_ASSET_CONTENT_BASE_URL into this
// store-backed configuration kind (ADR-039); this note names the kind
// rather than the now-retired environment variable.
const assetSyncDisabledNote = "asset sync is disabled (assets.settings' contentBaseUrl is not set): this coordinator will never deliver these assets to the node over the network"

// notReadyReason summarizes ALREADY-COMPUTED missing/gap counts, plus
// [assetSyncDisabledNote] when syncEnabled is false, into one operator-facing
// sentence — see mapNodeAssetManifest's own doc comment for why this exists
// at the wire layer instead of in assetsync.
//
// failures adds the one thing missing/gap counts alone cannot say: WHY a
// missing asset is missing. Before this, a node whose asset.fetch had
// genuinely failed (an unreachable content endpoint, a 404, a hash
// mismatch) read identically to a node the sync service simply had not
// gotten to yet: both rendered as bare "missing N expected asset(s)".
// fetchFailureSummary below consults failures for every missing asset by
// its own content hash and, for whichever ones have a known failure on
// record, appends the real cause instead of leaving the reader to guess.
func notReadyReason(nodeID string, missing []v1.MissingAsset, gaps []v1.AssetGap, syncEnabled bool, failures AssetFetchFailureSource) string {
	var parts []string
	if n := len(missing); n > 0 {
		parts = append(parts, fmt.Sprintf("missing %d expected asset(s)", n))
	}
	if n := len(gaps); n > 0 {
		parts = append(parts, fmt.Sprintf("%d sequence(s) with no coverage on this node at all", n))
	}
	if s := fetchFailureSummary(nodeID, missing, failures); s != "" {
		parts = append(parts, s)
	}
	if !syncEnabled {
		parts = append(parts, assetSyncDisabledNote)
	}
	return strings.Join(parts, "; ")
}

// fetchFailureSummary reports, for whichever of missing's assets
// [AssetFetchFailureSource.LastFetchFailure] has a known asset.fetch
// failure on record, the real cause instead of leaving a bare "missing"
// verdict indistinguishable from "sync has not gotten to it yet". Distinct
// reasons are reported once each with a count, rather than repeating the
// identical text once per asset (a single unreachable content endpoint
// fails every missing asset with the SAME reason), never conflating two
// genuinely different causes into one, and never fabricating a reason for
// an asset failures has no record of.
func fetchFailureSummary(nodeID string, missing []v1.MissingAsset, failures AssetFetchFailureSource) string {
	if failures == nil {
		return ""
	}
	var order []string
	counts := make(map[string]int)
	for _, a := range missing {
		reason, _, ok := failures.LastFetchFailure(nodeID, a.ContentHash)
		if !ok {
			continue
		}
		if counts[reason] == 0 {
			order = append(order, reason)
		}
		counts[reason]++
	}
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, 0, len(order))
	for _, reason := range order {
		parts = append(parts, fmt.Sprintf("%d asset(s) last failed to fetch: %s", counts[reason], reason))
	}
	return strings.Join(parts, "; ")
}
