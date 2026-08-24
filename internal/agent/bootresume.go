package agent

import (
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

// This file is TRACK-H-H3-SPEC.md section 7's own boot-clearing rule,
// isolated into one pure decision function so it can be tested directly
// without standing up the whole of agent.go's Run(): "a resumed assignment
// whose authorization tuple does not match the node's current authorized
// Show, generation, and catalog revision is discarded rather than applied,
// and the node comes up cleared." Without this rule, rebooting a node in
// November puts Halloween back on the wall (H0.7's own framing of the
// identical rule, one seam earlier).

// resumeDecision is decideBootResume's result: whether a persisted
// [pipeline.Assignment] may be resumed at boot, and — when it may not — the
// human-readable reason [pipeline.Supervisor.MarkResumeFailed] should
// record, so the discard is stated evidence (build item 4) and never a
// silent no-op.
type resumeDecision struct {
	Authorized bool
	Reason     string
}

// decideBootResume applies TRACK-H-H3-SPEC.md section 7's boot-clearing
// rule to one persisted assignment a against the node's currently held Cue
// catalog:
//
//   - hasCatalog false (no catalog has ever been deployed to this node):
//     every assignment is discarded — "a node with no held catalog at all
//     resumes nothing."
//   - a.Auth == nil (an assignment persisted before this field existed, or
//     applied without a coordinator that sends it yet): discarded, never
//     grandfathered in.
//   - a.Auth's Show, Generation, and CatalogRevision must ALL equal held's
//     — any one of the three differing means the assignment was authorized
//     under a Show, generation, or catalog this node no longer holds.
func decideBootResume(a pipeline.Assignment, held heldcatalog.HeldCatalog, hasCatalog bool) resumeDecision {
	if !hasCatalog {
		return resumeDecision{Authorized: false, Reason: fmt.Sprintf(
			"surface %q held a persisted assignment at boot, but this node holds no Cue catalog at all; a node with no held catalog resumes nothing (TRACK-H-H3-SPEC.md section 7)", a.SurfaceID)}
	}
	if a.Auth == nil {
		return resumeDecision{Authorized: false, Reason: fmt.Sprintf(
			"surface %q held a persisted assignment at boot with no authorization tuple (persisted before TRACK-H-H3-SPEC.md section 7 existed, or applied by a coordinator not yet sending one); treated as unauthorized, never grandfathered", a.SurfaceID)}
	}
	if a.Auth.Show != held.Show || a.Auth.Generation != held.Generation || a.Auth.CatalogRevision != held.Revision {
		return resumeDecision{Authorized: false, Reason: fmt.Sprintf(
			"surface %q held a persisted assignment authorized under show=%q generation=%d catalogRevision=%q, but this node currently holds show=%q generation=%d catalogRevision=%q",
			a.SurfaceID, a.Auth.Show, a.Auth.Generation, a.Auth.CatalogRevision, held.Show, held.Generation, held.Revision)}
	}
	return resumeDecision{Authorized: true}
}
