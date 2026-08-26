package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// activateRender is TRACK-H-cues-and-playlists.md section H4's renderer
// requirement: on an authorized activation whose Cue declares a render
// output, select THAT CUE's resolved FSEQ for every surface this node
// currently has an assignment for, replacing whatever it is rendering.
// Never [multisync.Timeline.Snapshot]'s Filename: selecting by filename is
// the cross-show hazard ADR-043 decision 6 exists to prevent, so the
// filename is corroboration and mismatch evidence only.
//
// There is no in-place source swap: renderops.go's buildAssignedSpec opens
// a fresh *fseq.File and startFrameWriter hands it, already open, to a new
// FrameWriter — so this is stop-then-start, and a surface must never be
// left half-stopped. The new file is opened and hash-verified BEFORE the
// old frame writer is ever stopped, mirroring applySurface's own
// "validate before persist" ordering (renderops.go, finding 10) for the
// identical reason: a bad new file must leave the old one running and
// report a stated failure, never go dark.
//
// That "never go dark" guarantee covers open-and-validate failures only.
// Once the OLD writer has actually been stopped, a startFrameWriter
// failure for the NEW file DOES leave the surface dark — there is no third
// file to fall back to, and store.Upsert (persisting the new assignment)
// already ran before this point, so the persisted assignment names a file
// nothing is currently rendering. surfaceAlreadyActivated below consults
// [renderOperations.hasRunningFrameWriter], not only the store, precisely
// so a later activation of the same Cue on that surface repairs this
// rather than reporting it healthy.
//
// The stop-then-start swap itself is still a brief, real, VISIBLE gap on
// the surface being switched — the old frame writer's last frame stops
// reaching the pipeline before the new one's first frame does. This is
// deliberate, and — per buildAssignedSpec's own "no in-place source swap"
// constraint — is not currently avoidable; it is not hidden here, and it
// must not be discovered later on a wall.
func (o *renderOperations) activateRender(act cueactivation.Activation, out cuecatalog.RenderOutput, now func() time.Time) error {
	assignments, err := o.store.Load()
	if err != nil {
		return fmt.Errorf("cue.activate: loading persisted render assignments: %w", err)
	}
	if len(assignments) == 0 {
		return fmt.Errorf("cue.activate: no surface is currently assigned on this node; nothing to activate Cue %q's render output onto", act.CueID)
	}

	var errs []string
	for _, a := range assignments {
		if err := o.activateSurfaceRender(a, act, out, now); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("cue.activate: %s", strings.Join(errs, "; "))
	}
	return nil
}

// firstAssetHash picks the content hash a render (or audio) swap persists
// and validates against: the first of hashes, or "" when the resolved
// catalog named none — buildAssignedSpec's own content-hash check then
// refuses the swap outright (an FSEQ output with no recorded hash at all
// is a coordinator-side bug, not a node-side "trust it").
func firstAssetHash(hashes []string) string {
	if len(hashes) == 0 {
		return ""
	}
	return hashes[0]
}

// activateSurfaceRender swaps one surface's rendered FSEQ to out's
// resolved sequence, under act's authorization tuple.
func (o *renderOperations) activateSurfaceRender(a pipeline.Assignment, act cueactivation.Activation, out cuecatalog.RenderOutput, now func() time.Time) error {
	const action = "cue.activate (render)"

	if out.Filename == "" {
		return fmt.Errorf(
			"%s: surface %q: activated Cue %q (revision %d) resolves render sequence %q to no runtime filename (no matching asset uploaded); refusing to open anything by sequence id (ADR-043 decision 6)",
			action, a.SurfaceID, act.CueID, act.CueRevision, out.Sequence)
	}

	// Ruling 1: MultiSync's own Filename is corroboration/mismatch
	// evidence only, never a selection authority. An empty Filename means
	// MultiSync has not reported anything yet (nothing to compare against,
	// not a mismatch); any non-empty Filename that disagrees with the
	// activated Cue's own resolved RUNTIME filename (out.Filename — the
	// name a node actually opens, never out.Sequence, a logical identity
	// FPP's own MultiSync report can never itself carry) is a stated
	// mismatch, and this surface's content is left exactly as it was —
	// never switched on a filename's say-so, never silently continued as
	// if healthy.
	if snap := o.timeline.Snapshot(); snap.Filename != "" && snap.Filename != out.Filename {
		return fmt.Errorf(
			"%s: surface %q: MultiSync reports filename %q but activated Cue %q (revision %d) resolves to %q; refusing to switch content on a filename disagreement (ADR-043 decision 6)",
			action, a.SurfaceID, snap.Filename, act.CueID, act.CueRevision, out.Filename)
	}

	// Already exactly this Cue's resolved FSEQ, applied under exactly this
	// authorization tuple, AND actually running: a redelivered/duplicate
	// activation must not disturb a surface that is already correct (H4's
	// own "full state, idempotent" requirement) by re-opening and
	// re-swapping a file that is already playing. o.hasRunningFrameWriter
	// is real running state, not the persisted assignment alone — see
	// surfaceAlreadyActivated's own doc comment for why a dark surface
	// whose store already names the right file must still be repaired.
	if surfaceAlreadyActivated(a, act, out, o.hasRunningFrameWriter(a.SurfaceID)) {
		return nil
	}

	var params map[string]any
	if err := json.Unmarshal(a.RawParams, &params); err != nil {
		return fmt.Errorf("%s: surface %q: decoding persisted assignment params: %w", action, a.SurfaceID, err)
	}
	params["fseqFilename"] = out.Filename
	params["fseqContentHash"] = firstAssetHash(out.AssetHashes)
	params["show"] = act.Show
	params["generation"] = float64(act.Generation)
	params["catalogRevision"] = act.CatalogRevision

	// Validate the NEW file before anything about the OLD one is touched.
	spec, f, parsedA, _, err := buildAssignedSpec(action, o.assetDir, a.SurfaceID, params, o.logger)
	if err != nil {
		return fmt.Errorf("surface %q: %w", a.SurfaceID, err)
	}
	_ = spec // This swap deliberately never re-applies the pipeline spec — see startFrameWriter call below.

	rawParams, err := json.Marshal(params)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: surface %q: encoding updated assignment params: %w", action, a.SurfaceID, err)
	}
	auth := &pipeline.AssignmentAuth{Show: act.Show, Generation: act.Generation, CatalogRevision: act.CatalogRevision}
	if err := o.store.Upsert(pipeline.Assignment{
		SurfaceID: a.SurfaceID, RawParams: rawParams, AppliedAt: now(), Auth: auth, CueID: act.CueID,
	}); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: surface %q: persisting updated assignment: %w", action, a.SurfaceID, err)
	}

	// Only now, with the new file already open and validated and the new
	// assignment already durable, does the old frame writer stop. The
	// underlying GStreamer pipeline process is deliberately NOT re-applied
	// (o.sup.Apply is not called here): only the FSEQ source is changing,
	// not the surface's channel range, geometry, frame rate, or output
	// sink, and reusing the already-running pipeline process keeps the
	// visible gap to the frame-writer swap itself rather than a full
	// pipeline restart.
	o.stopFrameWriter(a.SurfaceID)
	if err := o.startFrameWriter(a.SurfaceID, f, parsedA); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: surface %q: starting frame writer for the newly activated FSEQ: %w", action, a.SurfaceID, err)
	}
	// The SHARED timeline step time moves only after the new writer is
	// actually running — never before, or a startFrameWriter failure above
	// would already have left every OTHER surface on this node stepping to
	// a file that is not running (o.timeline is one shared instance across
	// every surface on this node, applyTimelineStepTime's own "SHARED-
	// TIMELINE DECISION" doc comment, renderops.go).
	o.applyTimelineStepTime(a.SurfaceID, f.StepTimeMS())
	return nil
}

// surfaceAlreadyActivated reports whether a's persisted assignment already
// names out's resolved sequence and content hash, under exactly act's
// authorization tuple, AND running is true — the "nothing to disturb" case
// a redelivered activation must detect before touching a running surface.
// A decode failure or a missing/mismatched authorization tuple is
// conservatively "not already activated" (never grandfathers a legacy or
// foreign assignment into skipping the swap).
//
// running must come from real running state
// ([renderOperations.hasRunningFrameWriter]), never the store alone:
// activateSurfaceRender's store.Upsert persists the new assignment BEFORE
// the swap is known to have worked, so a startFrameWriter failure leaves a
// surface that is dark while its persisted assignment already names the
// new file. Without running, THIS function would then read that surface
// as already-activated on the very next activation of the same Cue,
// skipping the repair forever and letting the node report Confirmed:true,
// outcome "authorized", for a dark surface.
func surfaceAlreadyActivated(a pipeline.Assignment, act cueactivation.Activation, out cuecatalog.RenderOutput, running bool) bool {
	if !running {
		return false
	}
	if a.Auth == nil {
		return false
	}
	if a.Auth.Show != act.Show || a.Auth.Generation != act.Generation || a.Auth.CatalogRevision != act.CatalogRevision {
		return false
	}
	var params map[string]any
	if err := json.Unmarshal(a.RawParams, &params); err != nil {
		return false
	}
	filename, _ := params["fseqFilename"].(string)
	hash, _ := params["fseqContentHash"].(string)
	return filename == out.Filename && hash == firstAssetHash(out.AssetHashes)
}
