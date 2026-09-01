package assetsync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// ActiveShow is [ResolveActiveShow]'s result: either a configured active
// show, or the honest absence of one.
type ActiveShow struct {
	Configured bool
	ShowID     string

	// Generation is show.active's own config revision number
	// (TRACK-H-H3-SPEC.md section 2): the value that changes whenever
	// show.active changes or is deliberately reissued, because
	// writeShowConfigRevision computes nextRevisionNo unconditionally
	// rather than deduplicating an identical payload. Nothing new is
	// minted here — every consumer of ResolveActiveShow gets it for free.
	// Meaningful ONLY when Configured is true: an unconfigured show.active
	// has no generation and authorizes nothing (the existing honest-
	// absence case, not generation zero), so Generation stays the zero
	// value and no caller may treat that zero as a real grant.
	Generation int64
}

// alwaysTrue is passed to config.DecodeShowActivePayload/
// DecodeShowSurfacePayload wherever this package re-decodes an
// ALREADY-PERSISTED, already-validated revision: those decoders exist to
// validate a write, and re-running their cross-reference checks against a
// row this package only ever reads back would reject a payload that was
// valid when it was written (there is no delete for a show or a node
// declaration — see CLAUDE.md's Track E known-gaps list — so a persisted
// reference can never go stale by deletion either).
func alwaysTrue(string) bool { return true }

// ResolveActiveShow reads the show.active singleton. Configured is false,
// with no error, when nothing has ever been activated — this is the §4.1
// point 1 case every node's manifest renders as unknown for, never ready
// and never an empty expectation that reads as fine.
func ResolveActiveShow(ctx context.Context, st *store.Store) (ActiveShow, error) {
	obj, err := st.GetConfigObject(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return ActiveShow{}, nil
	}
	if err != nil {
		return ActiveShow{}, fmt.Errorf("assetsync: resolve active show: %w", err)
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID, obj.CurrentRevision)
	if err != nil {
		return ActiveShow{}, fmt.Errorf("assetsync: resolve active show: read revision %d: %w", obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeShowActivePayload(rev.PayloadJSON, alwaysTrue)
	if verr != nil {
		return ActiveShow{}, fmt.Errorf("assetsync: resolve active show: decode stored payload: %s", verr.Detail)
	}
	return ActiveShow{Configured: true, ShowID: payload.Show, Generation: obj.CurrentRevision}, nil
}

// ExpectedAsset is one asset a node is expected to hold, per §4.1 point 2:
// a current asset whose show is the active show and whose target is
// either this node or the whole show.
type ExpectedAsset struct {
	AssetID     string
	SequenceID  string
	MediaType   string // "fseq" | "audio" | "media", per store.AssetRecord.MediaType
	ContentHash string
	Filename    string
	SizeBytes   int64
}

// SurfaceGap names a sequence the active show has SOME current asset for,
// that a surfaced node has NO current asset for (neither node-targeted
// nor show-wide) — see this package's own doc comment for why this is an
// inferred relationship, not a stored one. SurfaceIDs lists every
// show.surface assigned to the node in the active show, since nothing in
// the current schema narrows a gap to just one of them.
type SurfaceGap struct {
	SequenceID string
	SurfaceIDs []string
}

// ExpectedSet is [ExpectedAssetsForNode]'s result.
type ExpectedSet struct {
	Assets []ExpectedAsset
	Gaps   []SurfaceGap
}

// ExpectedAssetsForNode computes what nodeID should hold for showID (which
// must already be the resolved active show id — this function does not
// itself consult show.active), per §4.1 points 2 and 3.
func ExpectedAssetsForNode(ctx context.Context, st *store.Store, showID, nodeID string) (ExpectedSet, error) {
	nodeAssets, err := st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindNode, nodeID)
	if err != nil {
		return ExpectedSet{}, fmt.Errorf("assetsync: expected assets for node %q: list node-targeted assets: %w", nodeID, err)
	}
	showAssets, err := st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindShow, "")
	if err != nil {
		return ExpectedSet{}, fmt.Errorf("assetsync: expected assets for node %q: list show-wide assets: %w", nodeID, err)
	}

	assets := make([]ExpectedAsset, 0, len(nodeAssets)+len(showAssets))
	coveredSequences := make(map[string]bool, len(nodeAssets)+len(showAssets))
	for _, rec := range append(append([]store.AssetRecord{}, nodeAssets...), showAssets...) {
		assets = append(assets, ExpectedAsset{
			AssetID: rec.ID, SequenceID: rec.SequenceID, MediaType: rec.MediaType, ContentHash: rec.ContentHash,
			Filename: rec.RuntimeFilename, SizeBytes: rec.SizeBytes,
		})
		coveredSequences[rec.SequenceID] = true
	}

	surfaceIDs, err := surfaceIDsForNode(ctx, st, showID, nodeID)
	if err != nil {
		return ExpectedSet{}, err
	}

	var gaps []SurfaceGap
	if len(surfaceIDs) > 0 {
		nodeSequences, err := NodeCueSequenceIDs(ctx, st, showID, nodeID)
		if err != nil {
			return ExpectedSet{}, err
		}
		sortedSequences := make([]string, 0, len(nodeSequences))
		for seq := range nodeSequences {
			sortedSequences = append(sortedSequences, seq)
		}
		sort.Strings(sortedSequences)
		for _, seq := range sortedSequences {
			if !coveredSequences[seq] {
				gaps = append(gaps, SurfaceGap{SequenceID: seq, SurfaceIDs: surfaceIDs})
			}
		}
	}

	return ExpectedSet{Assets: assets, Gaps: gaps}, nil
}

// NodeCueSequenceIDs returns the set of sequence ids referenced by every
// Cue that actually participates for nodeID in showID's resolved Cue
// catalog — computed with the IDENTICAL cue-inclusion and per-output
// node-scoping rules [ResolveCueCatalog] applies (referenced by a Playlist
// entry or a "safeCue" safeCueRef, or declaring the announcement output;
// render scoped by show.surface assignment, audio scoped by an audio.node
// object) — but WITHOUT resolving any asset hash, so [ExpectedAssetsForNode]
// can call this to scope its own gap detection to nodeID's OWN cues (a
// render node must never be asked to hold a sequence only an audio-only
// Cue elsewhere in the show references) rather than recursing into
// [ResolveCueCatalog], which itself calls ExpectedAssetsForNode for asset
// hashes.
//
// This is the one per-target-node "which sequences does this node's cues
// resolve to" resolution: nodeID is a plain parameter, not render-specific,
// so a future audio/LTC/announcement node readiness computation can call
// the identical function for an audio.node target instead of
// reimplementing this scoping a second time. LTC and Announcement outputs
// contribute no sequence of their own — they play the SAME Cue's own audio
// output (config.ShowCuePayload's "outputs.ltc/announcement requires
// outputs.audio" authoring-time rule), which this function already
// collects.
func NodeCueSequenceIDs(ctx context.Context, st *store.Store, showID, nodeID string) (map[string]bool, error) {
	referencedCues, err := referencedCueIDs(ctx, st, showID)
	if err != nil {
		return nil, err
	}
	surfaceIDs, err := surfaceIDsForNode(ctx, st, showID, nodeID)
	if err != nil {
		return nil, err
	}
	nodeHasSurface := len(surfaceIDs) > 0
	_, nodeHasAudioNode, err := loadAudioNodePayload(ctx, st, nodeID)
	if err != nil {
		return nil, err
	}
	targets, err := loadAudioTargets(ctx, st, nodeID)
	if err != nil {
		return nil, err
	}

	cueObjs, err := st.ListConfigObjects(ctx, config.ShowCueConfigKind)
	if err != nil {
		return nil, fmt.Errorf("assetsync: node cue sequence ids: list show.cue objects: %w", err)
	}

	seqs := make(map[string]bool)
	for _, obj := range cueObjs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			if errors.Is(err, store.ErrConfigRevisionNotFound) {
				continue
			}
			return nil, fmt.Errorf("assetsync: node cue sequence ids: read show.cue %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, verr := config.DecodeShowCuePayload(rev.PayloadJSON, alwaysTrue, alwaysTrue)
		if verr != nil {
			return nil, fmt.Errorf("assetsync: node cue sequence ids: decode stored show.cue %q: %s", obj.ID, verr.Detail)
		}
		if payload.Show != showID {
			continue
		}
		directlyActivatableAnnouncement := payload.Outputs.Announcement != nil
		if !referencedCues[obj.ID] && !directlyActivatableAnnouncement {
			continue
		}
		if payload.Outputs.Render != nil && nodeHasSurface {
			seqs[payload.Outputs.Render.Sequence] = true
		}
		if payload.Outputs.Audio != nil && nodeHasAudioNode && targets.Owns(payload.Outputs.Audio.Target) {
			seqs[payload.Outputs.Audio.Asset] = true
		}
	}
	return seqs, nil
}

// surfaceIDsForNode returns the id of every show.surface object whose
// decoded payload names showID and nodeID, in the order [store.
// ListConfigObjects] returns them.
func surfaceIDsForNode(ctx context.Context, st *store.Store, showID, nodeID string) ([]string, error) {
	objs, err := st.ListConfigObjects(ctx, config.ShowSurfaceConfigKind)
	if err != nil {
		return nil, fmt.Errorf("assetsync: list show.surface objects: %w", err)
	}

	var ids []string
	for _, obj := range objs {
		rev, err := st.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("assetsync: read show.surface %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, verr := config.DecodeShowSurfacePayload(rev.PayloadJSON, alwaysTrue, alwaysTrue)
		if verr != nil {
			return nil, fmt.Errorf("assetsync: decode stored show.surface %q: %s", obj.ID, verr.Detail)
		}
		if payload.Show == showID && payload.Node == nodeID {
			ids = append(ids, obj.ID)
		}
	}
	return ids, nil
}

// ManifestState is [ComputeNodeManifest]'s three-valued verdict.
type ManifestState string

const (
	ManifestReady    ManifestState = "ready"
	ManifestNotReady ManifestState = "not_ready"
	ManifestUnknown  ManifestState = "unknown"
)

// UnknownCause names exactly why [ComputeNodeManifest] returned
// [ManifestUnknown] — a distinct value per §4.3 cause, so a caller (or a
// test) can distinguish them without parsing Reason's free text.
type UnknownCause string

const (
	// UnknownCauseNoActiveShow: nothing has ever been activated
	// (§4.1 point 1).
	UnknownCauseNoActiveShow UnknownCause = "no_active_show"
	// UnknownCauseNeverReported: this node has never published an
	// inventory report.
	UnknownCauseNeverReported UnknownCause = "never_reported"
	// UnknownCauseStaleReport: the last report is older than
	// [StalenessWindow]. Never rendered as not_ready — a stale report is
	// not evidence of absence.
	UnknownCauseStaleReport UnknownCause = "stale_report"
	// UnknownCauseReportIncomplete: the last report said complete:false.
	// The agent's own reason is carried into Reason.
	UnknownCauseReportIncomplete UnknownCause = "report_incomplete"
)

// MissingAsset is one expected asset [ComputeNodeManifest] found the node
// does not currently hold, named by sequence, filename, and content hash
// per §4.3.
type MissingAsset struct {
	AssetID     string
	SequenceID  string
	Filename    string
	ContentHash string
	SizeBytes   int64
}

// ExtraAsset is one asset a node holds that this manifest did not expect.
// Never an error and never a basis for deletion (§4.3) — see
// [ComputeNodeManifest]'s doc comment for why this is only ever populated
// from a report already known to be fresh.
type ExtraAsset struct {
	ContentHash string
	Filename    string
	SizeBytes   int64
}

// NodeManifest is [ComputeNodeManifest]'s result for one node.
type NodeManifest struct {
	NodeID string
	State  ManifestState

	// UnknownCause and Reason are set only when State == ManifestUnknown.
	// Reason is operator-facing free text; UnknownCause is what a caller
	// should actually branch on.
	UnknownCause UnknownCause
	Reason       string

	// Missing and Gaps are set only when State == ManifestNotReady.
	Missing []MissingAsset
	Gaps    []SurfaceGap

	// Extra is populated whenever a fresh report exists, regardless of
	// State — see ComputeNodeManifest's doc comment.
	Extra []ExtraAsset

	// ObservedAt is the report's own ReportedAt when State is Ready or
	// NotReady, and the zero time when State is Unknown: there is no
	// evidence an Unknown verdict rests on, so there is nothing to date it
	// by. Callers must not default this to time.Now — see pkg/observation's
	// identical rule for ObservedAt.
	ObservedAt time.Time
}

// StalenessWindow returns how old an inventory report may be and still
// count as fresh: 3x inventoryInterval. Exported so the manifest
// computation here, a future API handler, showmeshctl, and every test in
// this package all read the identical number — this project has hit the
// "two timeouts on opposite sides of one contract" defect three times
// already (Step 7's CLI/server pair, Step 9's SIGTERM budget, D-3's write
// deadline).
func StalenessWindow(inventoryInterval time.Duration) time.Duration {
	return 3 * inventoryInterval
}

// ComputeNodeManifest is the ONLY function in this codebase permitted to
// decide a node's asset readiness — see this package's own doc comment.
// Every input is caller-supplied evidence rather than fetched here, so
// this function performs no I/O and is trivial to test against
// constructed inputs:
//
//   - active: [ResolveActiveShow]'s result.
//   - expected: [ExpectedAssetsForNode]'s result for this node against
//     active.ShowID (ignored when !active.Configured).
//   - report: this node's [store.NodeAssetReportRecord], or nil if it has
//     never reported (distinct from a report with zero inventory rows —
//     see that type's own doc comment).
//   - reportFresh: whether report.ReportedAt falls within
//     [StalenessWindow] of the caller's own "now" — computed by the
//     caller (not here) so this function needs no clock.
//   - inventory: this node's currently-held assets, only meaningful when
//     report is non-nil.
//
// Evaluation order (checked top to bottom, first match wins) is what
// makes "unknown never renders as ready" and "a stale report never
// renders as not_ready" true by construction rather than by convention:
//  1. !active.Configured -> Unknown/NoActiveShow.
//  2. report == nil -> Unknown/NeverReported.
//  3. !reportFresh -> Unknown/StaleReport. Checked BEFORE completeness and
//     BEFORE comparing held assets, so a stale report's own stale
//     "complete" flag and stale inventory contents never reach the
//     Ready/NotReady branches at all.
//  4. !report.Complete -> Unknown/ReportIncomplete, carrying report.Reason.
//  5. Otherwise: Ready if every expected asset is held and there is no
//     gap, NotReady (naming every miss) otherwise.
//
// Extra is populated only in cases 4 and 5 — i.e. only once a report is
// known to exist and be fresh, even if it is incomplete. A report that is
// stale (case 3) never populates Extra: what a stale report says a node
// holds is exactly as unreliable as what it says a node lacks.
func ComputeNodeManifest(nodeID string, active ActiveShow, expected ExpectedSet, report *store.NodeAssetReportRecord, reportFresh bool, inventory []store.NodeAssetInventoryRecord) NodeManifest {
	m := NodeManifest{NodeID: nodeID}

	if !active.Configured {
		m.State = ManifestUnknown
		m.UnknownCause = UnknownCauseNoActiveShow
		m.Reason = "no active show is configured"
		return m
	}
	if report == nil {
		m.State = ManifestUnknown
		m.UnknownCause = UnknownCauseNeverReported
		m.Reason = "no inventory report has ever been received from this node"
		return m
	}
	if !reportFresh {
		m.State = ManifestUnknown
		m.UnknownCause = UnknownCauseStaleReport
		m.Reason = fmt.Sprintf("the last inventory report from this node was received at %s, which is older than the staleness window; a stale report is not evidence of what the node currently holds",
			report.ReportedAt.Format(time.RFC3339))
		return m
	}
	if !report.Complete {
		m.State = ManifestUnknown
		m.UnknownCause = UnknownCauseReportIncomplete
		m.Reason = fmt.Sprintf("the node's last inventory report could not fully enumerate its own asset directory: %s", report.Reason)
		return m
	}

	held := make(map[string]bool, len(inventory))
	for _, item := range inventory {
		held[item.ContentHash] = true
	}

	var missing []MissingAsset
	for _, a := range expected.Assets {
		if !held[a.ContentHash] {
			missing = append(missing, MissingAsset{
				AssetID: a.AssetID, SequenceID: a.SequenceID, Filename: a.Filename,
				ContentHash: a.ContentHash, SizeBytes: a.SizeBytes,
			})
		}
	}

	expectedHashes := make(map[string]bool, len(expected.Assets))
	for _, a := range expected.Assets {
		expectedHashes[a.ContentHash] = true
	}
	var extra []ExtraAsset
	for _, item := range inventory {
		if !expectedHashes[item.ContentHash] {
			extra = append(extra, ExtraAsset{ContentHash: item.ContentHash, Filename: item.RuntimeFilename, SizeBytes: item.SizeBytes})
		}
	}
	m.Extra = extra

	if len(missing) > 0 || len(expected.Gaps) > 0 {
		m.State = ManifestNotReady
		m.Missing = missing
		m.Gaps = expected.Gaps
		m.ObservedAt = report.ReportedAt
		return m
	}

	m.State = ManifestReady
	m.ObservedAt = report.ReportedAt
	return m
}

// BuildNodeManifest is the single entry point a caller (the sync service,
// or a future API handler) uses to get one node's full manifest: it
// resolves the active show, computes what nodeID should hold, reads its
// report and inventory evidence, and calls [ComputeNodeManifest]. now and
// inventoryInterval derive the staleness window via [StalenessWindow].
func BuildNodeManifest(ctx context.Context, st *store.Store, now time.Time, inventoryInterval time.Duration, nodeID string) (NodeManifest, error) {
	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		return NodeManifest{}, err
	}
	return buildNodeManifestForActiveShow(ctx, st, now, inventoryInterval, nodeID, active)
}

// buildNodeManifestForActiveShow is BuildNodeManifest's body once active
// has already been resolved, factored out so [BuildManifest] resolves the
// active show exactly once for every node rather than once per node.
func buildNodeManifestForActiveShow(ctx context.Context, st *store.Store, now time.Time, inventoryInterval time.Duration, nodeID string, active ActiveShow) (NodeManifest, error) {
	var expected ExpectedSet
	if active.Configured {
		var err error
		expected, err = ExpectedAssetsForNode(ctx, st, active.ShowID, nodeID)
		if err != nil {
			return NodeManifest{}, err
		}
	}

	report, err := st.GetNodeAssetReport(ctx, nodeID)
	var reportPtr *store.NodeAssetReportRecord
	switch {
	case err == nil:
		reportPtr = &report
	case errors.Is(err, store.ErrNodeAssetReportNotFound):
		// reportPtr stays nil: ComputeNodeManifest's own
		// UnknownCauseNeverReported case.
	default:
		return NodeManifest{}, fmt.Errorf("assetsync: read node asset report %q: %w", nodeID, err)
	}

	var reportFresh bool
	var inventory []store.NodeAssetInventoryRecord
	if reportPtr != nil {
		reportFresh = !now.After(reportPtr.ReportedAt.Add(StalenessWindow(inventoryInterval)))
		inventory, err = st.GetNodeAssetInventory(ctx, nodeID)
		if err != nil {
			return NodeManifest{}, fmt.Errorf("assetsync: read node asset inventory %q: %w", nodeID, err)
		}
	}

	return ComputeNodeManifest(nodeID, active, expected, reportPtr, reportFresh, inventory), nil
}

// BuildManifest returns [BuildNodeManifest]'s result for every declared
// node, resolving the active show exactly once.
func BuildManifest(ctx context.Context, st *store.Store, now time.Time, inventoryInterval time.Duration) ([]NodeManifest, error) {
	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		return nil, err
	}
	nodes, err := st.ListNodeDeclarations(ctx)
	if err != nil {
		return nil, fmt.Errorf("assetsync: list node declarations: %w", err)
	}

	manifests := make([]NodeManifest, 0, len(nodes))
	for _, n := range nodes {
		m, err := buildNodeManifestForActiveShow(ctx, st, now, inventoryInterval, n.NodeID, active)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}
